package api

// llm_vision_internal_test.go — Auflösung und Prüfung der Anhang-Auswahl
// (#429, ADR-0121).
//
// Interner Test, weil ladeBilder Teil des Handlers ist. Der Store wird
// gestellt: Es geht um die Prüflogik, nicht um das Dateisystem — und der
// wichtigste Fall lässt sich nur so bauen (eine Datei, die grösser ist, als
// der Karten-Zustand behauptet).

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

// stubStore liefert für jede id feste Bytes. Fehlt die id, meldet er einen
// Fehler wie der echte Store bei einer unbekannten Datei.
type stubStore struct {
	dateien   map[string][]byte
	geoeffnet []string
}

func (s *stubStore) Open(boardId, cardId, attId string) (io.ReadSeekCloser, string, error) {
	s.geoeffnet = append(s.geoeffnet, attId)
	b, ok := s.dateien[attId]
	if !ok {
		return nil, "", errors.New("nicht gefunden")
	}
	return nopSeekCloser{bytes.NewReader(b)}, attId + ".png", nil
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func kartenMitAnhaengen(anh ...map[string]any) map[string]any {
	arr := make([]any, 0, len(anh))
	for _, a := range anh {
		arr = append(arr, a)
	}
	return map[string]any{"id": "c1", "attachments": arr}
}

func anhang(id, name, mime string, size int) map[string]any {
	return map[string]any{"id": id, "filename": name, "mime": mime, "size": float64(size)}
}

func TestLadeBilderNormalfall(t *testing.T) {
	// Seit F-1094 bestimmt der Inhalt den Typ auch bei Objekt-Einträgen —
	// die Testdatei muss also wirklich ein PNG sein.
	inhalt := append(append([]byte{}, pngKopf...), 1, 2, 3)
	store := &stubStore{dateien: map[string][]byte{"a1": inhalt}}
	a := &LLMSendAPI{attachments: store}
	karte := kartenMitAnhaengen(anhang("a1", "bild.png", "image/png", 3))

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"a1"})
	if grund != "" {
		t.Fatalf("abgelehnt: %s", grund)
	}
	if len(bilder) != 1 || bilder[0].Filename != "bild.png" || bilder[0].MIME != "image/png" {
		t.Fatalf("unerwartet: %+v", bilder)
	}
	if !bytes.Equal(bilder[0].Data, inhalt) {
		t.Fatalf("Bytes falsch: %v", bilder[0].Data)
	}
}

func TestLadeBilderOhneAuswahlIstKeinFehler(t *testing.T) {
	a := &LLMSendAPI{}
	bilder, grund := a.ladeBilder(kartenMitAnhaengen(), "b1", "c1", "tester@example.com", nil)
	if grund != "" || bilder != nil {
		t.Fatalf("leere Auswahl abgelehnt: %q / %+v", grund, bilder)
	}
}

// Berechtigung: eine id, die nicht in der attachments-Liste DIESER Karte
// steht, wird abgelehnt — und der Store wird gar nicht erst gefragt.
func TestLadeBilderLehntFremdeIDAb(t *testing.T) {
	store := &stubStore{dateien: map[string][]byte{"fremd": {9}}}
	a := &LLMSendAPI{attachments: store}
	karte := kartenMitAnhaengen(anhang("a1", "bild.png", "image/png", 3))

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"fremd"})
	if bilder != nil {
		t.Fatal("fremder Anhang wurde geliefert")
	}
	if !strings.Contains(grund, "gehoert nicht zu dieser Karte") {
		t.Fatalf("Grund = %q", grund)
	}
	if len(store.geoeffnet) != 0 {
		t.Fatalf("der Store wurde trotzdem gefragt: %v", store.geoeffnet)
	}
}

// DER wichtige Fall: Die Grössenangabe im Karten-Zustand ist eine Behauptung
// des Uploads, die Datei ist die Tatsache. Ein Limit auf der Behauptung ist
// kein Limit auf der Datei.
func TestLadeBilderPrueftDieDateiNichtNurDieAngabe(t *testing.T) {
	zuGross := make([]byte, llm.MaxImageBytes+10)
	copy(zuGross, pngKopf)
	store := &stubStore{dateien: map[string][]byte{"a1": zuGross}}
	a := &LLMSendAPI{attachments: store}
	// Der Zustand behauptet 3 Bytes — die Vorprüfung lässt das durch.
	karte := kartenMitAnhaengen(anhang("a1", "luegt.png", "image/png", 3))

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"a1"})
	if bilder != nil {
		t.Fatal("zu grosse Datei wurde geliefert")
	}
	if !strings.Contains(grund, "groesser als im Karten-Zustand vermerkt") {
		t.Fatalf("Grund benennt die Abweichung nicht: %q", grund)
	}
}

func TestLadeBilderGrenzen(t *testing.T) {
	store := &stubStore{dateien: map[string][]byte{}}
	for i := 0; i < llm.MaxImagesPerSend+1; i++ {
		store.dateien[string(rune('a'+i))] = pngKopf
	}
	a := &LLMSendAPI{attachments: store}

	t.Run("MIME nicht erlaubt", func(t *testing.T) {
		// Seit F-1094 zählt der Inhalt: die Datei muss wirklich ein PDF sein.
		store.dateien["pdf"] = []byte("%PDF-1.4\n")
		karte := kartenMitAnhaengen(anhang("pdf", "doc.pdf", "application/pdf", 10))
		_, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"pdf"})
		if !strings.Contains(grund, "kann nicht mitgeschickt werden") {
			t.Fatalf("Grund = %q", grund)
		}
		if !strings.Contains(grund, "doc.pdf") {
			t.Fatalf("Grund nennt die Datei nicht: %q", grund)
		}
	})

	t.Run("zu viele", func(t *testing.T) {
		var anh []map[string]any
		var ids []string
		for i := 0; i < llm.MaxImagesPerSend+1; i++ {
			id := string(rune('a' + i))
			anh = append(anh, anhang(id, id+".png", "image/png", 1))
			ids = append(ids, id)
		}
		_, grund := a.ladeBilder(kartenMitAnhaengen(anh...), "b1", "c1", "tester@example.com", ids)
		if !strings.Contains(grund, "hoechstens") {
			t.Fatalf("Grund = %q", grund)
		}
	})

	t.Run("Duplikat", func(t *testing.T) {
		karte := kartenMitAnhaengen(anhang("a", "bild.png", "image/png", 1))
		_, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"a", "a"})
		if !strings.Contains(grund, "mehrfach") {
			t.Fatalf("Grund = %q", grund)
		}
	})

	t.Run("Summe zu gross", func(t *testing.T) {
		je := llm.MaxImageBytes
		var anh []map[string]any
		var ids []string
		for i := 0; i < 3; i++ {
			id := string(rune('a' + i))
			anh = append(anh, anhang(id, id+".png", "image/png", je))
			ids = append(ids, id)
		}
		_, grund := a.ladeBilder(kartenMitAnhaengen(anh...), "b1", "c1", "tester@example.com", ids)
		if !strings.Contains(grund, "zusammen") {
			t.Fatalf("Grund = %q", grund)
		}
	})
}

// Ohne Store wird die Auswahl BENANNT abgelehnt, nicht still übergangen. Ein
// Send, der die Bilder klammheimlich vergisst, ist schlimmer als einer, der
// sagt warum.
func TestLadeBilderOhneStore(t *testing.T) {
	a := &LLMSendAPI{}
	karte := kartenMitAnhaengen(anhang("a1", "bild.png", "image/png", 3))
	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"a1"})
	if bilder != nil || grund == "" {
		t.Fatalf("still uebergangen: %+v / %q", bilder, grund)
	}
}

func TestAnhaengeDerKarte(t *testing.T) {
	karte := kartenMitAnhaengen(
		anhang("a1", "eins.png", "image/png", 10),
		map[string]any{"filename": "ohne-id.png"}, // wird uebergangen
	)
	got := anhaengeDerKarte(karte)
	if len(got) != 1 {
		t.Fatalf("%d Eintraege, erwartet 1: %+v", len(got), got)
	}
	if got["a1"].Size != 10 || got["a1"].MIME != "image/png" {
		t.Fatalf("Metadaten falsch: %+v", got["a1"])
	}
	// Karte ganz ohne attachments-Feld darf nicht knallen.
	if len(anhaengeDerKarte(map[string]any{"id": "c1"})) != 0 {
		t.Fatal("Karte ohne attachments liefert Eintraege")
	}
}

// DER Befund des Sicherheits- und des Server-Kritikers (S3): Die
// Gesamtgrenze wurde nur gegen die BEHAUPTETE Größe geprüft. Fehlt `size`
// im Karten-Zustand (oder ist es nicht numerisch), steht dort 0 — fünf
// Bilder à 5 MB liefen dann als „0 B insgesamt" durch die Mengenprüfung,
// jedes einzeln unter der Einzelgrenze. Nachgewiesen wurden 25 MB gegen
// eine Grenze von 12 MB.
//
// Der bestehende Test `TestLadeBilderGrenzen/Summe_zu_gross` fand das
// nicht, weil er ausschliesslich mit ehrlichen Angaben arbeitet.
func TestLadeBilderPrueftDieGESAMTgroesseGegenDieDateien(t *testing.T) {
	// Jede Datei knapp unter der Einzelgrenze, zusammen weit über der
	// Gesamtgrenze.
	je := int(llm.MaxImageBytes) - 1024
	store := &stubStore{dateien: map[string][]byte{}}
	var anh []map[string]any
	var ids []string
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		d := make([]byte, je)
		copy(d, pngKopf)
		store.dateien[id] = d
		// Der Karten-Zustand behauptet 0 Bytes — genau der Fall „size fehlt".
		anh = append(anh, map[string]any{"id": id, "filename": id + ".png", "mime": "image/png"})
		ids = append(ids, id)
	}
	a := &LLMSendAPI{attachments: store}

	// Vorbedingung: Die Vorprüfung lässt das durch (sonst prüft der Test die
	// falsche Schranke).
	bekannt := anhaengeDerKarte(kartenMitAnhaengen(anh...))
	var behauptet int64
	for _, id := range ids {
		behauptet += bekannt[id].Size
	}
	if grund := llm.PruefeBildMenge(len(ids), behauptet); grund != "" {
		t.Fatalf("Vorbedingung verletzt: die Vorpruefung greift schon (%q) — der Test prueft dann nicht die Nachpruefung", grund)
	}

	bilder, grund := a.ladeBilder(kartenMitAnhaengen(anh...), "b1", "c1", "tester@example.com", ids)
	if bilder != nil {
		var summe int
		for _, b := range bilder {
			summe += len(b.Data)
		}
		t.Fatalf("%d Bilder mit zusammen %d B durchgelassen, Grenze ist %d B", len(bilder), summe, llm.MaxImageBytesTotal)
	}
	if !strings.Contains(grund, "zusammen groesser") {
		t.Fatalf("Grund benennt die Gesamtgrenze nicht: %q", grund)
	}
}

// pngKopf ist die PNG-Signatur — genug, damit http.DetectContentType die
// Datei als image/png erkennt.
var pngKopf = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// F-855: Mail-Ingest-Anhänge stehen (im Bestand) als reine id-Strings in
// `card.attachments` — ohne MIME und Grösse. Der Server bestimmt beides beim
// Senden aus der gespeicherten Datei; danach geht ein echtes Bild genauso ans
// Modell wie ein Anhang mit Metadaten.
func TestLadeBilderMailIngestBildGehtMitErmitteltemTyp(t *testing.T) {
	inhalt := append(append([]byte{}, pngKopf...), 1, 2, 3)
	store := &stubStore{dateien: map[string][]byte{"mail-att": inhalt}}
	a := &LLMSendAPI{attachments: store}
	// So legt der Mail-Ingest vor F-855 an: ein blosser String.
	karte := map[string]any{"id": "c1", "attachments": []any{"mail-att"}}

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"mail-att"})
	if grund != "" {
		t.Fatalf("Mail-Ingest-Bild abgelehnt: %q", grund)
	}
	if len(bilder) != 1 {
		t.Fatalf("%d Bilder, erwartet 1", len(bilder))
	}
	if bilder[0].MIME != "image/png" {
		t.Fatalf("MIME = %q, erwartet image/png (aus dem Inhalt bestimmt)", bilder[0].MIME)
	}
	if !bytes.Equal(bilder[0].Data, inhalt) {
		t.Fatalf("Bytes falsch: %v", bilder[0].Data)
	}
	// Ohne Dateinamen an der Karte gilt der Name aus der Ablage.
	if bilder[0].Filename != "mail-att.png" {
		t.Fatalf("Filename = %q", bilder[0].Filename)
	}
}

// F-855: Die Dateiendung zählt nicht. Eine als .png benannte Textdatei (der
// Stub liefert jeden Anhang als „<id>.png") wird am Inhalt erkannt und
// abgelehnt — sie darf nicht als Bild an ein fremdes Modell gehen.
func TestLadeBilderMailIngestNichtBildMitPngNamenWirdAbgelehnt(t *testing.T) {
	store := &stubStore{dateien: map[string][]byte{"mail-att": []byte("Das ist kein Bild, nur Text.")}}
	a := &LLMSendAPI{attachments: store}
	karte := map[string]any{"id": "c1", "attachments": []any{"mail-att"}}

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"mail-att"})
	if bilder != nil {
		t.Fatalf("Nicht-Bild wurde mitgeschickt: %+v", bilder)
	}
	if !strings.Contains(grund, "text/plain") || !strings.Contains(grund, "kann nicht mitgeschickt werden") {
		t.Fatalf("Grund benennt den ermittelten Typ nicht: %q", grund)
	}
	if !strings.Contains(grund, "mail-att.png") {
		t.Fatalf("Grund nennt die Datei nicht: %q", grund)
	}
}

// F-855: Die Grössengrenze greift auch für Einträge ohne Grössenangabe — und
// zwar schon in der Vorprüfung (Grösse aus der Datei), nicht erst beim Laden.
func TestLadeBilderMailIngestGroessengrenzeGreift(t *testing.T) {
	zuGross := make([]byte, llm.MaxImageBytes+1)
	copy(zuGross, pngKopf)
	store := &stubStore{dateien: map[string][]byte{"mail-att": zuGross}}
	a := &LLMSendAPI{attachments: store}
	karte := map[string]any{"id": "c1", "attachments": []any{"mail-att"}}

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"mail-att"})
	if bilder != nil {
		t.Fatal("zu grosses Mail-Ingest-Bild wurde mitgeschickt")
	}
	if !strings.Contains(grund, "Grenze fuer den Versand") {
		t.Fatalf("Grund benennt die Einzelgrenze nicht: %q", grund)
	}
}

// F-855: Auch die Gesamtgrenze zählt Mail-Ingest-Einträge mit ihrer echten
// Grösse — vorher gingen sie (hätten sie mitgehen dürfen) mit 0 B ein.
func TestLadeBilderMailIngestZaehltZurGesamtgrenze(t *testing.T) {
	je := int(llm.MaxImageBytes) - 1024
	store := &stubStore{dateien: map[string][]byte{}}
	var arr []any
	var ids []string
	for i := 0; i < 3; i++ {
		id := string(rune('a' + i))
		d := make([]byte, je)
		copy(d, pngKopf)
		store.dateien[id] = d
		arr = append(arr, id)
		ids = append(ids, id)
	}
	a := &LLMSendAPI{attachments: store}
	karte := map[string]any{"id": "c1", "attachments": arr}

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", ids)
	if bilder != nil {
		t.Fatal("Auswahl ueber der Gesamtgrenze wurde mitgeschickt")
	}
	// Die Vorprüfung (nicht erst die Nachprüfung beim Laden) muss greifen.
	if !strings.Contains(grund, "zusammen") || strings.Contains(grund, "groesser als im Karten-Zustand") {
		t.Fatalf("Grund = %q, erwartet die Vorpruefung der Gesamtgrenze", grund)
	}
}

// F-1094: Auch bei Objekt-Einträgen zählt der Inhalt, nicht das `mime`-Feld
// im Karten-Zustand — das kann jeder Board-Schreiber per Op umetikettieren.
// Eine Textdatei mit `mime: image/png` geht nicht als Bild an ein Modell.
func TestLadeBilderObjektEintragMitTextInhaltWirdAbgelehnt(t *testing.T) {
	store := &stubStore{dateien: map[string][]byte{"a1": []byte("Das ist kein Bild, nur Text.")}}
	a := &LLMSendAPI{attachments: store}
	karte := kartenMitAnhaengen(anhang("a1", "umetikettiert.png", "image/png", 28))

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"a1"})
	if bilder != nil {
		t.Fatalf("umetikettierter Text wurde als Bild mitgeschickt: %+v", bilder)
	}
	if !strings.Contains(grund, "text/plain") || !strings.Contains(grund, "kann nicht mitgeschickt werden") {
		t.Fatalf("Grund benennt den erkannten Typ nicht: %q", grund)
	}
	if !strings.Contains(grund, "umetikettiert.png") {
		t.Fatalf("Grund nennt die Datei nicht: %q", grund)
	}
}

// F-1094, Gegenrichtung: Weicht der gespeicherte Typ von einem erlaubten
// Bild-Typ des Inhalts ab, gilt der erkannte — und DER geht an den Anbieter,
// nicht der behauptete.
func TestLadeBilderObjektEintragErkannterTypGilt(t *testing.T) {
	inhalt := append(append([]byte{}, pngKopf...), 1, 2, 3)
	store := &stubStore{dateien: map[string][]byte{"a1": inhalt}}
	a := &LLMSendAPI{attachments: store}
	karte := kartenMitAnhaengen(anhang("a1", "bild.gif", "image/gif", len(inhalt)))

	bilder, grund := a.ladeBilder(karte, "b1", "c1", "tester@example.com", []string{"a1"})
	if grund != "" {
		t.Fatalf("abgelehnt: %s", grund)
	}
	if len(bilder) != 1 || bilder[0].MIME != "image/png" {
		t.Fatalf("MIME = %+v, erwartet image/png aus dem Inhalt", bilder)
	}
}
