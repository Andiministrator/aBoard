package mail

// ingest_anhang_test.go — Form der Mail-Anhänge in `card.attachments` (F-855).
//
// Seit Sprint 69 schreibt der Ingest Objekte `{id, filename, mime, size,
// created}` statt reiner id-Strings. Der Bestand behält seine Strings; die
// Duplikat-Prüfung beim Kommentar-Pfad muss beide Formen kennen.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

var pngSignatur = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// Der Typ an der Karte entscheidet mit darüber, ob ein Anhang an ein fremdes
// Modell gehen darf. Der Mail-Header ist eine Behauptung des Absenders; für
// Bild-Typen zählt deshalb der Inhalt.
func TestIngestMIME(t *testing.T) {
	svgNackt := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	svgMitXML := []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + string(svgNackt))
	svgMitKommentar := []byte("<!-- erzeugt von X -->\n" + string(svgNackt))
	faelle := []struct {
		name      string
		behauptet string
		dateiname string
		data      []byte
		want      string
	}{
		{"echtes PNG mit passendem Header", "image/png", "a.png", pngSignatur, "image/png"},
		{"echtes PNG mit falschem Header", "application/octet-stream", "a.bin", pngSignatur, "image/png"},
		{"echtes PNG mit falschem Bild-Header", "image/gif", "a.gif", pngSignatur, "image/png"},
		{"Text, der sich als PNG ausgibt", "image/png", "a.png", []byte("kein Bild, nur Text"), "text/plain"},
		{"Text, der sich als JPEG ausgibt", "image/jpeg; name=x.jpg", "x.jpg", []byte("kein Bild"), "text/plain"},
		{"PDF behält den Header-Typ", "application/pdf", "a.pdf", []byte("PDF-bytes"), "application/pdf"},
		{"Header mit Parametern und Grossschreibung", "Application/PDF; name=\"a.pdf\"", "a.pdf", []byte("x"), "application/pdf"},
		{"fehlender Header → erkannter Typ", "", "a", []byte("hallo"), "text/plain"},
		{"kaputter Header → erkannter Typ", ";;;", "a", pngSignatur, "image/png"},
		// F-1093: jeder image/*-Header, den der Inhalt nicht bestätigt —
		// nicht nur die Modell-Bildtypen.
		{"image/x-foo ohne Bildinhalt", "image/x-foo", "a.foo", []byte{0x00, 0x01, 0x02, 0x03}, "application/octet-stream"},
		{"image/x-foo mit Textinhalt", "image/x-foo", "a.foo", []byte("hallo"), "text/plain"},
		// F-1093: SVG wird nie als Bild-Typ an die Karte geschrieben.
		{"SVG mit image/svg+xml", "image/svg+xml", "a.svg", svgNackt, "application/octet-stream"},
		{"SVG mit <?xml und image/svg+xml", "image/svg+xml", "a.svg", svgMitXML, "application/octet-stream"},
		{"SVG mit image/svg", "image/svg", "bild", svgNackt, "application/octet-stream"},
		{"SVG ohne Hinweis im Header und Namen", "application/octet-stream", "datei", svgNackt, "application/octet-stream"},
		{"SVG mit <?xml ohne Hinweis im Header und Namen", "text/xml", "datei.xml", svgMitXML, "application/octet-stream"},
		{"SVG hinter Kommentar", "text/plain", "notiz.txt", svgMitKommentar, "application/octet-stream"},
		{"SVG-Endung mit harmlosem Header", "text/plain", "Bild.SVG", []byte("text"), "application/octet-stream"},
		{"svgz-Endung", "application/gzip", "a.svgz", []byte{0x1f, 0x8b, 0x08}, "application/octet-stream"},
		// Gegenrichtung: Text, der das Wort nur erwähnt, ohne Tag, bleibt Text.
		{"Text über SVG ohne Tag", "text/plain", "notiz.txt", []byte("SVG ist ein Format"), "text/plain"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			if got := ingestMIME(f.behauptet, f.dateiname, f.data); got != f.want {
				t.Fatalf("ingestMIME(%q, %q) = %q, erwartet %q", f.behauptet, f.dateiname, got, f.want)
			}
		})
	}
}

// Ein neuer Mail-Anhang an einer Karte, die schon einen Bestands-String UND
// ein Frontend-Objekt trägt: Beide bleiben unverändert, der neue kommt als
// Objekt dazu — und eine id, die schon (in irgendeiner Form) da ist, wird
// nicht doppelt angehängt.
func TestAddCommentToCard_AnhaengeBeideFormen(t *testing.T) {
	repo, done := newCardOpsTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()
	cardID, _, err := CreateCardInServiceBoard(ctx, repo, NewServiceCard{Title: "T", SenderEmail: "u@x"})
	if err != nil {
		t.Fatalf("CreateCardInServiceBoard: %v", err)
	}

	// Bestand herstellen: ein alter id-String und ein Frontend-Objekt.
	b, _ := repo.Get(ctx, storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	karte := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	karte["attachments"] = []any{
		"alt-string",
		map[string]any{"id": "frontend-obj", "filename": "f.png", "mime": "image/png", "size": float64(3)},
	}
	neu, _ := json.Marshal(state)
	if _, err := repo.Update(ctx, b.ID, b.Title, b.Version, neu, b.UpdatedAt); err != nil {
		t.Fatalf("Bestand schreiben: %v", err)
	}

	loc := CardLocation{BoardID: storage.ServiceBoardID, CardID: cardID}
	err = AddCommentToCard(ctx, repo, loc, "cm-1", "a@x", "Antwort", "", []IngestAttachment{
		{ID: "alt-string", Filename: "dup1.png", MIME: "image/png", Size: 1},   // schon als String da
		{ID: "frontend-obj", Filename: "dup2.png", MIME: "image/png", Size: 1}, // schon als Objekt da
		{ID: "neu", Filename: "neu.png", MIME: "image/png", Size: 42},
	})
	if err != nil {
		t.Fatalf("AddCommentToCard: %v", err)
	}

	b, _ = repo.Get(ctx, storage.ServiceBoardID)
	state = nil
	_ = json.Unmarshal(b.StateJSON, &state)
	karte = state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	atts := karte["attachments"].([]any)
	if len(atts) != 3 {
		t.Fatalf("%d Eintraege, erwartet 3 (zwei Bestand + einer neu): %#v", len(atts), atts)
	}
	if atts[0] != "alt-string" {
		t.Fatalf("Bestands-String veraendert: %#v", atts[0])
	}
	if obj, _ := atts[1].(map[string]any); obj["filename"] != "f.png" {
		t.Fatalf("Frontend-Objekt veraendert: %#v", atts[1])
	}
	obj, ok := atts[2].(map[string]any)
	if !ok {
		t.Fatalf("neuer Eintrag ist kein Objekt: %#v", atts[2])
	}
	if obj["id"] != "neu" || obj["filename"] != "neu.png" || obj["mime"] != "image/png" || obj["size"] != float64(42) {
		t.Fatalf("neuer Eintrag falsch: %#v", obj)
	}
	if c, _ := obj["created"].(string); c == "" {
		t.Fatalf("created fehlt: %#v", obj)
	}
}
