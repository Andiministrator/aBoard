package api

// llm_vision.go — Anhänge als Bilder an den Prompt hängen (#429, ADR-0121).
//
// Der Client schickt nur Anhang-ids. Diese Datei löst sie auf, prüft sie und
// lädt die Bytes. Drei Dinge, die dabei schieflaufen können, und wo sie
// abgefangen werden:
//
//	Berechtigung — die id muss in der attachments-Liste DIESER Karte stehen.
//	               Board und Karte kommen aus der URL und sind durch den
//	               ACLGuard gegangen; ein Anhang einer fremden Karte ist damit
//	               nicht erreichbar, und geratene ids laufen ins Leere.
//	Grösse       — zweimal geprüft: gegen die Angabe im Karten-Zustand (vor
//	               dem Laden, spart Arbeit) UND beim Lesen gegen die Datei
//	               selbst. Die Angabe im Zustand ist eine Behauptung des
//	               Uploads; ein Limit auf ihr ist kein Limit auf der Datei.
//	Modell       — kann das gewählte Modell überhaupt Bilder? Das wird VOR
//	               dem Stream-Start beantwortet, damit ein Send an ein
//	               Nicht-Vision-Modell sauber scheitert statt Kosten zu
//	               erzeugen.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/Andiministrator/aBoard/server/llm"
)

// WithAttachments rüstet den Attachment-Store nach. Ohne ihn werden Anhänge
// nicht mitgeschickt — eine Auswahl läuft dann in eine benannte Ablehnung,
// nicht in ein stilles Weglassen (ein Send, der die Bilder klammheimlich
// vergisst, ist schlimmer als einer, der sagt warum).
func (a *LLMSendAPI) WithAttachments(store attachmentOpener) *LLMSendAPI {
	a.attachments = store
	return a
}

// attachmentOpener ist der Ausschnitt des AttachmentStore, den diese Datei
// braucht. Ein Interface statt des konkreten Typs, damit der Test den Laden
// ohne Dateisystem stellen kann — und damit hier sichtbar ist, dass nur
// gelesen wird.
type attachmentOpener interface {
	Open(boardId, cardId, attId string) (io.ReadSeekCloser, string, error)
}

// kartenAnhang bündelt, was der Karten-Zustand über einen Anhang behauptet.
type kartenAnhang struct {
	ID       string
	Filename string
	MIME     string
	Size     int64
	// nurID: Eintrag stammt aus dem Mail-Ingest-Pfad und ist ein blosser
	// id-String ohne Metadaten (siehe anhaengeDerKarte). MIME und Size sind
	// dann leer, bis metadatenAusDatei sie aus der Datei bestimmt hat.
	nurID bool
}

// anhaengeDerKarte liest card.attachments in eine id-indizierte Übersicht.
//
// Das Feld trägt ZWEI Repräsentationen, und das ist Bestand, nicht Absicht:
// Das Frontend schreibt Objekte (`{id, filename, mime, size}`), der
// Mail-Ingest schreibt reine id-Strings (`server/mail/cardops.go`, F-358).
// Ein String-Eintrag hat keine Metadaten — weder MIME noch Größe. Er wird
// hier als `nurID`-Eintrag erfasst; ladeBilder bestimmt Typ und Größe dann
// aus der gespeicherten Datei (metadatenAusDatei, F-855), bevor Allowlist und
// Grenzen greifen. Seit F-855 schreibt der Ingest selbst Objekte; String-
// Einträge sind Bestand aus der Zeit davor und werden nicht migriert.
//
// Einträge ohne id werden übergangen — sie sind nicht adressierbar.
func anhaengeDerKarte(card map[string]any) map[string]kartenAnhang {
	out := map[string]kartenAnhang{}
	arr, ok := card["attachments"].([]any)
	if !ok {
		return out
	}
	for _, a := range arr {
		if s, ok := a.(string); ok {
			if s != "" {
				out[s] = kartenAnhang{ID: s, nurID: true}
			}
			continue
		}
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		id, _ := am["id"].(string)
		if id == "" {
			continue
		}
		e := kartenAnhang{ID: id}
		e.Filename, _ = am["filename"].(string)
		e.MIME, _ = am["mime"].(string)
		if v, ok := am["size"].(float64); ok {
			e.Size = int64(v)
		}
		out[id] = e
	}
	return out
}

// ladeBilder löst die angeforderten Anhang-ids auf und liefert sie als
// Bild-Anhänge. Zweiter Rückgabewert ist ein Ablehnungsgrund ("" = in
// Ordnung); er geht als Fehlermeldung an den Client, bevor der Stream
// beginnt.
func (a *LLMSendAPI) ladeBilder(card map[string]any, boardID, cardID, akteur string, ids []string) ([]llm.ImageAttachment, string) {
	if len(ids) == 0 {
		return nil, ""
	}
	if a.attachments == nil {
		return nil, "Anhaenge koennen auf diesem Server nicht mitgeschickt werden"
	}

	bekannt := anhaengeDerKarte(card)

	// Erst prüfen, dann laden: Eine zu grosse oder zu zahlreiche Auswahl soll
	// nicht erst den Speicher füllen.
	var summe int64
	gewaehlt := make([]kartenAnhang, 0, len(ids))
	gesehen := map[string]bool{}
	for _, id := range ids {
		if gesehen[id] {
			return nil, fmt.Sprintf("Anhang %q ist mehrfach ausgewaehlt", id)
		}
		gesehen[id] = true
		e, ok := bekannt[id]
		if !ok {
			// Bewusst dieselbe Auskunft für „gibt es nicht" und „gehört zu
			// einer anderen Karte" — die Unterscheidung wäre ein Orakel.
			return nil, fmt.Sprintf("Anhang %q gehoert nicht zu dieser Karte", id)
		}
		// Den Typ bestimmt der Inhalt, für JEDEN Eintrag (F-1094). Bis
		// dahin galt das nur für Alt-Einträge ohne Metadaten (F-855); bei
		// Objekten ging `mime` aus dem Karten-Zustand ungeprüft an
		// PruefeBild und an den Anbieter — und jeder, der das Board
		// schreiben darf, kann `mime` per Op umetikettieren. Weicht der
		// gespeicherte Typ ab, gilt der erkannte; ist der kein erlaubtes
		// Bild, lehnt PruefeBild unten ab. Bei `nurID` kommen Grösse und
		// Name ebenfalls aus der Datei.
		ergaenzt, grund := a.metadatenAusDatei(boardID, cardID, e)
		if grund != "" {
			return nil, grund
		}
		e = ergaenzt
		if grund := llm.PruefeBild(e.MIME, e.Size); grund != "" {
			return nil, fmt.Sprintf("%s: %s", anhangName(e), grund)
		}
		summe += e.Size
		gewaehlt = append(gewaehlt, e)
	}
	if grund := llm.PruefeBildMenge(len(gewaehlt), summe); grund != "" {
		return nil, grund
	}

	out := make([]llm.ImageAttachment, 0, len(gewaehlt))
	// Zweiter Zähler, diesmal aus den TATSÄCHLICH gelesenen Bytes. Die
	// Vorprüfung oben rechnet mit `e.Size` aus dem Karten-Zustand, und das
	// ist eine Behauptung des Uploads: Fehlt das Feld oder ist es nicht
	// numerisch, steht dort 0, und fünf Bilder à 5 MB liefen als „0 B
	// insgesamt" durch die Mengenprüfung. Die Einzelgrenze hielt sie
	// (jedes für sich unter 5 MB) — die Gesamtgrenze, die eigentliche
	// Kostenschranke, nicht. Nachgewiesen im Kritiker-Lauf mit 25 MB.
	var echteSumme int64
	for _, e := range gewaehlt {
		f, dateiname, err := a.attachments.Open(boardID, cardID, e.ID)
		if err != nil {
			return nil, fmt.Sprintf("%s konnte nicht gelesen werden", anhangName(e))
		}
		// LimitReader auf ein Byte über der Grenze: Die Grössenangabe im
		// Karten-Zustand ist eine Behauptung des Uploads, die Datei ist die
		// Tatsache. Weicht sie ab, bricht das hier ab statt beim Anbieter.
		data, err := io.ReadAll(io.LimitReader(f, llm.MaxImageBytes+1))
		_ = f.Close()
		if err != nil {
			return nil, fmt.Sprintf("%s konnte nicht gelesen werden", anhangName(e))
		}
		if int64(len(data)) > llm.MaxImageBytes {
			return nil, fmt.Sprintf("%s ist groesser als im Karten-Zustand vermerkt und ueberschreitet %d B",
				anhangName(e), llm.MaxImageBytes)
		}
		echteSumme += int64(len(data))
		if echteSumme > llm.MaxImageBytesTotal {
			return nil, fmt.Sprintf(
				"ausgewaehlte Anhaenge sind zusammen groesser als im Karten-Zustand vermerkt und ueberschreiten %d B",
				llm.MaxImageBytesTotal)
		}
		name := e.Filename
		if name == "" {
			name = dateiname
		}
		out = append(out, llm.ImageAttachment{
			ID: e.ID, Filename: name, MIME: e.MIME, Data: data,
		})
	}
	// Audit (ADR-0121 §6): Was an ein fremdes Modell geht, gehört ins Log —
	// mit id UND Byte-Zahl, nicht nur mit dem Dateinamen. Der Name kommt aus
	// dem Karten-Zustand, ist nicht eindeutig und jederzeit änderbar; die id
	// ist der einzige Anker, über den sich später rekonstruieren lässt,
	// welche Datei es war. Der Inhalt wird NICHT geloggt.
	for _, b := range out {
		log.Printf("aboard-server: llm-anhang: board=%s karte=%s akteur=%s anhang=%s typ=%s bytes=%d",
			boardID, cardID, akteur, b.ID, b.MIME, len(b.Data))
	}
	return out, ""
}

// metadatenAusDatei bestimmt den MIME-Typ eines Anhangs aus der gespeicherten
// Datei, nicht aus einer Behauptung — für jeden Eintrag (F-1094). Bei einem
// `nurID`-Eintrag (Mail-Ingest-Bestand, reiner id-String) ergänzt sie
// zusätzlich Grösse und Dateinamen (F-855).
//
//	MIME   — per Inhalt (http.DetectContentType über die ersten 512 Bytes),
//	         für JEDEN Eintrag; ein abweichender Typ im Karten-Zustand wird
//	         überschrieben. Die Dateiendung zählt ausdrücklich NICHT: Ein
//	         Mail-Absender wählt den Namen frei, und eine „bild.png" mit
//	         Textinhalt darf nicht als image/png an ein fremdes Modell gehen.
//	Grösse — nur bei `nurID` aus der Datei (Seek ans Ende), ohne sie ganz zu
//	         lesen. Bei Objekten bleibt die Angabe aus dem Karten-Zustand
//	         für die Vorprüfung stehen; die Nachprüfung beim eigentlichen
//	         Laden in ladeBilder greift in beiden Fällen gegen die Datei.
//
// Zweiter Rückgabewert ist ein Ablehnungsgrund ("" = in Ordnung).
func (a *LLMSendAPI) metadatenAusDatei(boardID, cardID string, e kartenAnhang) (kartenAnhang, string) {
	f, dateiname, err := a.attachments.Open(boardID, cardID, e.ID)
	if err != nil {
		return e, fmt.Sprintf("%s konnte nicht gelesen werden", anhangName(e))
	}
	defer f.Close()
	if e.Filename == "" {
		e.Filename = dateiname
	}
	groesse, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return e, fmt.Sprintf("%s konnte nicht gelesen werden", anhangName(e))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return e, fmt.Sprintf("%s konnte nicht gelesen werden", anhangName(e))
	}
	kopf := make([]byte, 512)
	n, err := io.ReadFull(f, kopf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return e, fmt.Sprintf("%s konnte nicht gelesen werden", anhangName(e))
	}
	erkannt := http.DetectContentType(kopf[:n])
	if !e.nurID && e.MIME != "" && e.MIME != erkannt {
		// Kein Fehler für sich — ein Upload kann `image/jpg` statt
		// `image/jpeg` notiert haben. Aber ein Hinweis, falls jemand den
		// Typ umetikettiert hat; der Inhalt wird NICHT geloggt.
		log.Printf("aboard-server: llm-anhang: karte=%s anhang=%s gespeicherter Typ %q weicht vom Inhalt %q ab, es gilt der Inhalt",
			cardID, e.ID, e.MIME, erkannt)
	}
	e.MIME = erkannt
	if e.nurID {
		e.Size = groesse
		e.nurID = false
	}
	return e, ""
}

// anhangName liefert die Bezeichnung für Meldungen — Dateiname, wenn es
// einen gibt, sonst die id. Eine Meldung, die nur eine UUID nennt, hilft
// niemandem beim Wiederfinden.
func anhangName(e kartenAnhang) string {
	if e.Filename != "" {
		return e.Filename
	}
	return e.ID
}
