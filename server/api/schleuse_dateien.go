package api

// schleuse_dateien.go — Dateien im Auszug der Ausgangsschleuse, Teil S4
// (ADR-0134 §5; Karte #687).
//
// Die Person wählt Anhänge der Karte (auch Dateien an Kommentaren — sie
// stehen ebenfalls in card.attachments). Der Server
//
//   - liest sie aus dem Anhangsspeicher (<ABOARD_ATTACHMENTS_DIR>/<board>/
//     <karte>/) und kopiert nichts: Text- und PDF-Inhalt geht als Text in den
//     Auszug (und damit durch dieselbe Pseudonymisierung), von Bildern bleiben
//     im Auftrag nur Hash und Größe der bereinigten Fassung;
//   - bestimmt die Art am Inhalt, nie an Name oder MIME-Angabe der Karte
//     (Muster F-1094);
//   - liest beim Versand jedes freigegebene Bild FRISCH, bereinigt es erneut
//     und sendet es nur, wenn der Hash dem freigegebenen entspricht.
//
// Was geprüft wird: Die id muss in card.attachments DIESER Karte stehen
// (sonst „datei-unbekannt“ — dieselbe Auskunft für fremd und nicht
// vorhanden, wie llm_vision.go), Anzahl und Größen (schleuse.Dateien*,
// llm.PruefeBild*), die Art (Text in UTF-8, PDF mit Text, JPEG/PNG/GIF) und
// bei Bildern, ob die Anbieter-Art Bilder senden kann.
//
// Nie im Log: Dateiname und Inhalt — nur id, Art und Bytes.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

// MitAnhaengen verdrahtet den Anhangsspeicher (S4). Ohne ihn lehnt ein Auszug
// mit Dateien ab („kein-anhangspeicher“), statt sie still wegzulassen.
func (s *SchleuseAPI) MitAnhaengen(store attachmentOpener) *SchleuseAPI {
	s.anhaenge = store
	return s
}

// dateiFehler ist eine Ablehnung beim Lesen der Dateien: HTTP-Status,
// maschinenlesbarer Grund und — wo es eine Datei betrifft — deren id.
type dateiFehler struct {
	status int
	grund  string
	msg    string
	id     string
}

func (f *dateiFehler) koerper() map[string]any {
	k := fehlerKoerper(f.status, f.grund, f.msg)
	if f.id != "" {
		k["datei_id"] = f.id
	}
	return k
}

// dateiAuswahl ist, was die gewählten Dateien zum Auszug beitragen.
type dateiAuswahl struct {
	teile   []schleuse.DateiTeil   // extrahierter Text, in Auswahlreihenfolge
	textIDs []string               // ids der Text-/PDF-Dateien
	bilder  []schleuse.BildEintrag // bereinigte Bilder, alle zunächst NICHT freigegeben
}

// ladeDatei liest eine Datei der Karte vollständig, höchstens bis zur
// Quellgrenze. Der Name kommt aus dem Karten-Zustand, sonst aus der Ablage.
func (s *SchleuseAPI) ladeDatei(boardID, cardID string, e kartenAnhang) ([]byte, string, *dateiFehler) {
	f, dateiname, err := s.anhaenge.Open(boardID, cardID, e.ID)
	if err != nil {
		return nil, "", &dateiFehler{http.StatusNotFound, "datei-fehlt", "Datei ist im Anhangsspeicher nicht (mehr) vorhanden", e.ID}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, schleuse.DateiQuelleHoechstBytes+1))
	if err != nil {
		return nil, "", &dateiFehler{http.StatusInternalServerError, "datei-unlesbar", "Datei konnte nicht gelesen werden", e.ID}
	}
	if len(data) > schleuse.DateiQuelleHoechstBytes {
		return nil, "", &dateiFehler{http.StatusRequestEntityTooLarge, "datei-zu-gross",
			fmt.Sprintf("Datei ist größer als %d B", schleuse.DateiQuelleHoechstBytes), e.ID}
	}
	name := e.Filename
	if name == "" {
		name = dateiname
	}
	return data, name, nil
}

// dateiFehlerAus übersetzt einen Fehler aus dem Paket schleuse.
func dateiFehlerAus(err error, id string) *dateiFehler {
	switch {
	case errors.Is(err, schleuse.ErrDateiArt):
		return &dateiFehler{http.StatusUnsupportedMediaType, "datei-art", "Dateiart wird nicht unterstützt (Text in UTF-8, PDF mit Text, JPEG, PNG, GIF)", id}
	case errors.Is(err, schleuse.ErrDateiZuGross):
		return &dateiFehler{http.StatusRequestEntityTooLarge, "datei-zu-gross", "Datei überschreitet eine Grenze (Größe, Seiten oder Bildpunkte)", id}
	case errors.Is(err, schleuse.ErrPDFOhneText):
		return &dateiFehler{http.StatusUnprocessableEntity, "pdf-ohne-text", "PDF enthält keinen auslesbaren Text (Scan?) — es gibt keine Texterkennung", id}
	case errors.Is(err, schleuse.ErrPDFZeit):
		return &dateiFehler{http.StatusUnprocessableEntity, "datei-unlesbar", "Text der PDF ließ sich nicht in der Frist auslesen", id}
	}
	return &dateiFehler{http.StatusUnprocessableEntity, "datei-unlesbar", "Datei ist beschädigt, verschlüsselt oder nicht lesbar", id}
}

// ladeDateien liest die gewählten Dateien für einen Auszug (ADR-0134 §5).
// anbieter ist die Art der Freigabe — sie entscheidet, ob Bilder gehen.
func (s *SchleuseAPI) ladeDateien(karte map[string]any, boardID, cardID string, ids []string, anbieter string) (dateiAuswahl, *dateiFehler) {
	var aus dateiAuswahl
	if len(ids) == 0 {
		return aus, nil
	}
	if s.anhaenge == nil {
		return aus, &dateiFehler{http.StatusServiceUnavailable, "kein-anhangspeicher", "Dateien können auf diesem Server nicht mitgesendet werden", ""}
	}
	if len(ids) > schleuse.DateienHoechst {
		return aus, &dateiFehler{http.StatusBadRequest, "zu-viele-dateien", fmt.Sprintf("höchstens %d Dateien je Auszug", schleuse.DateienHoechst), ""}
	}
	bekannt := anhaengeDerKarte(karte)
	gesehen := map[string]bool{}
	var summe int
	var bildSumme int64
	for _, id := range ids {
		if gesehen[id] {
			return aus, &dateiFehler{http.StatusBadRequest, "eingabe", "Datei mehrfach gewählt", id}
		}
		gesehen[id] = true
		e, ok := bekannt[id]
		if !ok {
			return aus, &dateiFehler{http.StatusBadRequest, "datei-unbekannt", "Datei gehört nicht (mehr) zu dieser Karte", id}
		}
		data, name, fe := s.ladeDatei(boardID, cardID, e)
		if fe != nil {
			return aus, fe
		}
		if summe += len(data); summe > schleuse.DateienQuelleSummeHoechst {
			return aus, &dateiFehler{http.StatusRequestEntityTooLarge, "datei-zu-gross",
				fmt.Sprintf("gewählte Dateien sind zusammen größer als %d B", schleuse.DateienQuelleSummeHoechst), id}
		}
		kopf := data
		if len(kopf) > 512 {
			kopf = kopf[:512]
		}
		art, _, err := schleuse.ErkenneArt(kopf)
		if err != nil {
			return aus, dateiFehlerAus(err, id)
		}
		switch art {
		case schleuse.DateiArtText, schleuse.DateiArtPDF:
			var text string
			if art == schleuse.DateiArtText {
				text, err = schleuse.TextAusDatei(data)
			} else {
				text, err = schleuse.TextAusPDF(data)
			}
			if err != nil {
				return aus, dateiFehlerAus(err, id)
			}
			aus.teile = append(aus.teile, schleuse.DateiTeil{Name: name, Text: text})
			aus.textIDs = append(aus.textIDs, id)
		case schleuse.DateiArtBild:
			if !llm.SchleuseBilderMoeglich(anbieter) {
				return aus, &dateiFehler{http.StatusConflict, "keine-bilder", "Dieser Anbieter kann über die Schleuse keine Bilder empfangen", id}
			}
			sauber, mime, err := schleuse.BildOhneMetadaten(data)
			if err != nil {
				return aus, dateiFehlerAus(err, id)
			}
			if grund := llm.PruefeBild(mime, int64(len(sauber))); grund != "" {
				return aus, &dateiFehler{http.StatusRequestEntityTooLarge, "datei-zu-gross", "Bild ist nach dem Bereinigen zu groß: " + grund, id}
			}
			bildSumme += int64(len(sauber))
			if grund := llm.PruefeBildMenge(len(aus.bilder)+1, bildSumme); grund != "" {
				return aus, &dateiFehler{http.StatusRequestEntityTooLarge, "zu-viele-bilder", grund, id}
			}
			aus.bilder = append(aus.bilder, schleuse.BildEintrag{
				ID: id, MIME: mime, Bytes: int64(len(sauber)), SHA256: schleuse.HashBytes(sauber)})
		}
	}
	return aus, nil
}

// bilderFuerVersand liest die freigegebenen Bilder eines Auftrags frisch,
// bereinigt sie erneut und vergleicht sie mit dem Hash aus der Durchsicht.
// Grund "" = in Ordnung; sonst ein Grund für fehler_grund (es ist dann noch
// nichts hinausgegangen). karte ist der aktuelle Stand der Karte: Ein Bild,
// das dort nicht mehr hängt, geht nicht mit.
func (s *SchleuseAPI) bilderFuerVersand(karte map[string]any, boardID, cardID string, bilder []schleuse.BildEintrag) ([]llm.ImageAttachment, string) {
	frei := schleuse.FreigegebeneBilder(bilder)
	if len(frei) == 0 {
		return nil, ""
	}
	if s.anhaenge == nil {
		return nil, "kein-anhangspeicher"
	}
	out := make([]llm.ImageAttachment, 0, len(frei))
	for _, b := range frei {
		sauber, grund := s.bereinigtesBild(karte, boardID, cardID, b)
		if grund != "" {
			return nil, grund
		}
		out = append(out, llm.ImageAttachment{ID: b.ID, MIME: b.MIME, Data: sauber})
	}
	return out, ""
}

// bereinigtesBild liest ein Bild des Auftrags frisch aus der Ablage,
// bereinigt es und liefert die Bytes NUR, wenn sie dem Hash aus der
// Durchsicht entsprechen — dieselbe Prüfung für Versand und Vorschau, damit
// die Vorschau zeigt, was hinausgeht (Verify VS4-2). Grund "" = in Ordnung.
func (s *SchleuseAPI) bereinigtesBild(karte map[string]any, boardID, cardID string, b schleuse.BildEintrag) ([]byte, string) {
	if s.anhaenge == nil {
		return nil, "kein-anhangspeicher"
	}
	e, ok := anhaengeDerKarte(karte)[b.ID]
	if !ok {
		return nil, "datei-fehlt"
	}
	data, _, fe := s.ladeDatei(boardID, cardID, e)
	if fe != nil {
		return nil, fe.grund
	}
	sauber, mime, err := schleuse.BildOhneMetadaten(data)
	if err != nil || schleuse.HashBytes(sauber) != b.SHA256 || mime != b.MIME {
		return nil, "datei-veraendert"
	}
	return sauber, ""
}

// bildVorschau liefert ein Bild eines Auftrags in der BEREINIGTEN Fassung —
// genau die Bytes, die beim Versand hinausgehen (Verify VS4-2: Die Miniatur
// aus dem Karten-Zustand kann jeder Board-Schreiber setzen und zeigt dann
// etwas anderes als die Datei). Sehen dürfen es:
//   - die antragstellende Person (Mitglied am Board, viewer+), jedes Bild
//     ihres Auftrags;
//   - bei Vier-Augen eine zweite Person mit editor+, solange der Auftrag auf
//     ihre Freigabe wartet — nur die freigegebenen Bilder, also genau die, die
//     sie mit freigibt.
//
// Alles andere: 404 (kein Existenz-Orakel). Hat sich die Datei seit der
// Durchsicht geändert: 409 „datei-veraendert“ — dann gibt es nichts zu zeigen,
// was hinausginge.
func (s *SchleuseAPI) bildVorschau(w http.ResponseWriter, r *http.Request, u *storage.User) {
	boardID, cardID, aid, bid := r.PathValue("id"), r.PathValue("cardId"), r.PathValue("aid"), r.PathValue("bid")
	if !s.rolleOderFehler(w, r, boardID, u.Email, storage.RoleViewer) {
		return
	}
	a, err := s.auftraege.Get(r.Context(), aid, u.ID)
	eigener := err == nil && a.BoardID == boardID && a.CardID == cardID
	if !eigener {
		a, err = s.auftraege.GetAnKarte(r.Context(), aid, boardID, cardID)
		if err != nil || a.Zustand != storage.SchleuseZustandFreigegeben || !a.VierAugen || a.ZweitFreigegebenVon != "" ||
			strings.EqualFold(a.UserEmail, u.Email) {
			schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "bild not found")
			return
		}
		if ok, err := s.rolleMindestens(r.Context(), boardID, u.Email, storage.RoleEditor); err != nil || !ok {
			schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "bild not found")
			return
		}
	}
	var bild *schleuse.BildEintrag
	for i := range a.Bilder {
		if a.Bilder[i].ID == bid && (eigener || a.Bilder[i].Freigegeben) {
			bild = &a.Bilder[i]
		}
	}
	if bild == nil {
		schleuseFehler(w, http.StatusNotFound, "bild-unbekannt", "bild not found")
		return
	}
	st, err := s.ladeKarte(r.Context(), boardID, cardID)
	if err != nil {
		schleuseFehler(w, http.StatusNotFound, "datei-fehlt", "karte nicht lesbar")
		return
	}
	sauber, grund := s.bereinigtesBild(st.karte, boardID, cardID, *bild)
	if grund != "" {
		status := http.StatusConflict
		if grund == "kein-anhangspeicher" {
			status = http.StatusServiceUnavailable
		}
		schleuseFehler(w, status, grund, "Bild nicht (mehr) wie in der Durchsicht")
		return
	}
	w.Header().Set("Content-Type", bild.MIME)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(sauber)
}

// bilderPassen prüft nach BeginneVersand, dass die vorab geladenen Bilder
// genau die freigegebenen des verbindlich gelesenen Auftrags sind — in
// Reihenfolge und Hash. Zwischen Vorprüfung und BeginneVersand kann der
// Auftrag sich nur ändern, wenn die Freigabe dabei wegfällt; dann hätte
// BeginneVersand abgelehnt. Die Prüfung schließt die Lücke trotzdem
// ausdrücklich, statt sich darauf zu verlassen.
func bilderPassen(geladen []llm.ImageAttachment, bilder []schleuse.BildEintrag) bool {
	frei := schleuse.FreigegebeneBilder(bilder)
	if len(frei) != len(geladen) {
		return false
	}
	for i, b := range frei {
		if geladen[i].ID != b.ID || schleuse.HashBytes(geladen[i].Data) != b.SHA256 {
			return false
		}
	}
	return true
}
