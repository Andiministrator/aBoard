package api_test

// schleuse_dateien_test.go — Dateien im Auszug der Ausgangsschleuse über HTTP
// (Karte #687, ADR-0134 §5): Text und PDF gehen als pseudonymisierter Text,
// Bilder nur bereinigt und nur nach Einzelfreigabe; der Hash umfasst die
// Bilder; beim Versand wird jedes Bild frisch gelesen und geprüft.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/schleuse"
)

// TestMain: Der api-Testprozess ist auch der Kindprozess der PDF-Extraktion
// (schleuse.TextAusPDF startet os.Executable, Verify VS4-1). Einziges
// TestMain des api-Testprogramms.
func TestMain(m *testing.M) {
	schleuse.PDFKindprozess()
	os.Exit(m.Run())
}

// ablageAttrappe ist ein Anhangsspeicher im Speicher: board/karte/id → Datei.
type ablageAttrappe struct {
	mu      sync.Mutex
	dateien map[string][]byte
	namen   map[string]string
}

type leser struct{ *bytes.Reader }

func (leser) Close() error { return nil }

func (a *ablageAttrappe) Open(boardID, cardID, attID string) (io.ReadSeekCloser, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := boardID + "/" + cardID + "/" + attID
	d, ok := a.dateien[k]
	if !ok {
		return nil, "", fmt.Errorf("nicht gefunden")
	}
	return leser{bytes.NewReader(d)}, a.namen[k], nil
}

func (a *ablageAttrappe) lege(id, name string, data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dateien[slBoard+"/k1/"+id] = data
	a.namen[slBoard+"/k1/"+id] = name
}

// apiPDF baut eine gültige einseitige PDF (Helvetica, ASCII).
func apiPDF(text string) []byte {
	inhalt := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objekte := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(inhalt), inhalt),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objekte))
	for i, o := range objekte {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objekte)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objekte)+1, x)
	return b.Bytes()
}

const slExifGeheim = "GPS-Kamera-Anna-Müller"

// apiJPEG: kleines JPEG mit einem EXIF-Abschnitt, der slExifGeheim trägt.
func apiJPEG(t *testing.T, farbe uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{farbe, uint8(x * 30), uint8(y * 30), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	roh := b.Bytes()
	nutz := append([]byte("Exif\x00\x00"), []byte(slExifGeheim)...)
	seg := append([]byte{0xFF, 0xE1, byte((len(nutz) + 2) >> 8), byte(len(nutz) + 2)}, nutz...)
	return append(append(append([]byte{}, roh[:2]...), seg...), roh[2:]...)
}

// slStateDateien: slState plus Anhänge an k1 — Text, PDF, Bild, ZIP.
func slStateDateien() string {
	return strings.Replace(slState, `"comments":[{"id":"c1"`,
		`"attachments":[{"id":"t1","filename":"notiz.txt","mime":"text/plain","size":10},`+
			`{"id":"p1","filename":"angebot.pdf","mime":"application/pdf","size":10},`+
			`{"id":"b1","filename":"foto.jpg","mime":"image/jpeg","size":10},`+
			`{"id":"z1","filename":"daten.zip","mime":"application/zip","size":10}],`+
			`"comments":[{"id":"c1"`, 1)
}

type dateienEnv struct {
	*versandEnv
	ablage *ablageAttrappe
	jpeg   []byte
}

func neueDateienEnv(t *testing.T) *dateienEnv {
	t.Helper()
	e := &dateienEnv{versandEnv: neueVersandEnv(t), ablage: &ablageAttrappe{dateien: map[string][]byte{}, namen: map[string]string{}}}
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, slStateDateien(), slBoard); err != nil {
		t.Fatal(err)
	}
	e.jpeg = apiJPEG(t, 200)
	e.ablage.lege("t1", "notiz.txt", []byte("Anna Müller bittet Dora Weber um Rückruf."))
	e.ablage.lege("p1", "angebot.pdf", apiPDF("Gartenbau Huber GmbH bietet 1200 EUR."))
	e.ablage.lege("b1", "foto.jpg", e.jpeg)
	e.ablage.lege("z1", "daten.zip", []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00geheim"))
	e.sl.MitAnhaengen(e.ablage)
	return e
}

func (e *dateienEnv) auszugMit(dateien ...string) map[string]any {
	a := e.auszug()
	a["dateien"] = dateien
	return a
}

// Text und PDF stehen pseudonymisiert im Auszug (beide Stufen: der Name aus
// dem Board fest, die Firma aus der PDF über das lokale Modell); das lokale
// Modell sieht keinen Originalnamen; Bilder stehen nur als Hash darin.
func TestSchleuseDateien_TextUndPDFImAuszug(t *testing.T) {
	e := neueDateienEnv(t)
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("t1", "p1", "b1"))
	if code != http.StatusCreated {
		t.Fatalf("Auszug: %d %s", code, roh)
	}
	a := auftragAus(t, roh)
	text := a["text"].(string)
	for _, soll := range []string{"## Datei 1: notiz.txt", "## Datei 2: angebot.pdf", "bittet", "1200 EUR"} {
		if !strings.Contains(text, soll) {
			t.Errorf("Auszug ohne %q:\n%s", soll, text)
		}
	}
	keinOriginal(t, "Auszug mit Dateien", roh)
	for _, anfrage := range e.modell.anfragen {
		if strings.Contains(anfrage, "Anna") || strings.Contains(anfrage, "Dora") {
			t.Errorf("lokales Modell sah einen Namen aus der Textdatei: %s", anfrage)
		}
	}
	teile := a["teile"].(map[string]any)
	if d := teile["dateien"].([]any); len(d) != 2 || d[0] != "t1" || d[1] != "p1" {
		t.Errorf("teile.dateien: %v", d)
	}
	bilder := a["bilder"].([]any)
	if len(bilder) != 1 {
		t.Fatalf("bilder: %v", bilder)
	}
	b := bilder[0].(map[string]any)
	if b["id"] != "b1" || b["freigegeben"] != false || b["mime"] != "image/jpeg" {
		t.Errorf("Bild: %v", b)
	}
	sauber, _, err := schleuse.BildOhneMetadaten(e.jpeg)
	if err != nil || b["sha256"] != schleuse.HashBytes(sauber) {
		t.Errorf("Bild-Hash nicht der bereinigten Fassung: %v", b["sha256"])
	}
	if a["sha256"] != schleuse.Hash(text) {
		t.Errorf("Hash ohne freigegebenes Bild muss der Text-Hash sein")
	}
	// Die Datenbank hält keinen Dateinamen und keine Bildbytes.
	var spalte string
	_ = e.db.QueryRow(`SELECT bilder FROM schleuse_auftraege WHERE id = ?`, a["id"]).Scan(&spalte)
	if strings.Contains(spalte, "foto.jpg") || len(spalte) > 400 {
		t.Errorf("bilder-Spalte: %s", spalte)
	}
}

// Ein Bild geht nur nach Einzelfreigabe hinaus — bereinigt, ohne EXIF, mit
// dem Hash im Auftrags-Hash; ohne Freigabe geht es nicht mit.
func TestSchleuseDateien_BildNurNachEinzelfreigabe(t *testing.T) {
	e := neueDateienEnv(t)
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("b1"))
	if code != http.StatusCreated {
		t.Fatalf("Auszug: %d %s", code, roh)
	}
	a := auftragAus(t, roh)
	id, sha, text := a["id"].(string), a["sha256"].(string), a["text"].(string)

	// Einzelfreigabe: unbekanntes Bild abgelehnt, b1 freigegeben → neuer Hash.
	if code, roh := e.roh(t, http.MethodPut, e.pfad("/"+id), slAnna, map[string]any{"text": text, "basis_sha256": sha, "bilder_freigegeben": []string{"x"}}); code != http.StatusBadRequest || !strings.Contains(roh, "bild-unbekannt") {
		t.Fatalf("unbekanntes Bild: %d %s", code, roh)
	}
	code, roh = e.roh(t, http.MethodPut, e.pfad("/"+id), slAnna, map[string]any{"text": text, "basis_sha256": sha, "bilder_freigegeben": []string{"b1"}})
	if code != http.StatusOK {
		t.Fatalf("Einzelfreigabe: %d %s", code, roh)
	}
	g := auftragAus(t, roh)
	neuSha := g["sha256"].(string)
	if neuSha == sha {
		t.Fatal("Einzelfreigabe ändert den Hash nicht")
	}
	// Die alte Freigabe auf den Text-Hash greift nicht mehr.
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != http.StatusConflict {
		t.Fatalf("Freigabe mit altem Hash: %d %s", code, roh)
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": neuSha}); code != http.StatusOK {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if e.anbieter.zahl() != 1 {
		t.Fatalf("Anfragen: %d", e.anbieter.zahl())
	}
	raw, _ := json.Marshal(e.anbieter.anfragen[0])
	sauber, _, _ := schleuse.BildOhneMetadaten(e.jpeg)
	if !strings.Contains(string(raw), base64.StdEncoding.EncodeToString(sauber)) {
		t.Fatal("bereinigtes Bild nicht in der Anfrage")
	}
	if strings.Contains(string(raw), base64.StdEncoding.EncodeToString(e.jpeg)) || strings.Contains(string(raw), "foto.jpg") {
		t.Fatal("Original-Bild oder Dateiname in der Anfrage")
	}
	keinOriginal(t, "Anfrage mit Bild", string(raw))
	if !strings.Contains(e.logText(), "Bild b1 typ=image/jpeg") {
		t.Errorf("Bild nicht protokolliert:\n%s", e.logText())
	}
	// Beleg nennt das gesendete Bild.
	code, roh = e.roh(t, http.MethodGet, "/boards/"+slBoard+"/cards/k1/schleuse-belege/"+id, slVera, nil)
	if code != 200 || !strings.Contains(roh, schleuse.HashBytes(sauber)) {
		t.Errorf("Beleg ohne Bild: %d %s", code, roh)
	}
}

func TestSchleuseDateien_BildOhneFreigabeBleibtDrinnen(t *testing.T) {
	e := neueDateienEnv(t)
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("b1"))
	if code != http.StatusCreated {
		t.Fatalf("Auszug: %d %s", code, roh)
	}
	a := auftragAus(t, roh)
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+a["id"].(string)+"/freigabe"), slAnna, map[string]any{"sha256": a["sha256"]}); code != http.StatusOK {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	raw, _ := json.Marshal(e.anbieter.anfragen)
	if e.anbieter.zahl() != 1 || strings.Contains(string(raw), "base64") || strings.Contains(string(raw), `"image"`) {
		t.Fatalf("nicht freigegebenes Bild ging mit: %s", raw)
	}
}

// Wird das Bild nach der Freigabe ausgetauscht, geht NICHTS hinaus.
func TestSchleuseDateien_BildGetauschtNichtsGeht(t *testing.T) {
	e := neueDateienEnv(t)
	_, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("b1"))
	a := auftragAus(t, roh)
	id := a["id"].(string)
	_, roh = e.roh(t, http.MethodPut, e.pfad("/"+id), slAnna, map[string]any{"text": a["text"], "basis_sha256": a["sha256"], "bilder_freigegeben": []string{"b1"}})
	g := auftragAus(t, roh)
	// Vier-Augen gibt es hier nicht — der Versand startet mit der Freigabe.
	// Deshalb wird VOR der Freigabe getauscht: der Hash stammt aus der
	// Durchsicht, die Datei ist inzwischen eine andere.
	e.ablage.lege("b1", "foto.jpg", apiJPEG(t, 10))
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": g["sha256"]}); code != http.StatusOK {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if e.anbieter.zahl() != 0 {
		t.Fatalf("trotz getauschtem Bild gesendet: %d", e.anbieter.zahl())
	}
	_, roh = e.roh(t, http.MethodGet, e.pfad("/"+id), slAnna, nil)
	if f := auftragAus(t, roh); f["zustand"] != "fehlgeschlagen" || f["fehler_grund"] != "datei-veraendert" {
		t.Fatalf("Auftrag: %v", f)
	} else if f["gesendet_am"] != nil {
		// Die Vorprüfung greift VOR BeginneVersand: Der Auftrag zählt nicht
		// gegen die Stundengrenze, er ist nie „gesendet“ gewesen.
		t.Errorf("gesendet_am gesetzt — die Prüfung lief erst nach BeginneVersand: %v", f["gesendet_am"])
	}
	if got := strings.Join(schleuseVerlauf(e.karte(t)), ","); got != "schleuse-fehlgeschlagen" {
		t.Errorf("Verlauf: %s", got)
	}
}

// Abgelehnt, ohne Datensatz und ohne Aufruf des lokalen Modells: unbekannte
// Datei, nicht unterstützte Art, fehlende Ablage.
func TestSchleuseDateien_Ablehnungen(t *testing.T) {
	e := neueDateienEnv(t)
	fall := []struct {
		dateien []string
		status  int
		grund   string
	}{
		{[]string{"fremd"}, http.StatusBadRequest, "datei-unbekannt"},
		{[]string{"z1"}, http.StatusUnsupportedMediaType, "datei-art"},
		{[]string{"t1", "t1"}, http.StatusBadRequest, "eingabe"},
		{[]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, http.StatusBadRequest, "zu-viele-dateien"},
	}
	for _, f := range fall {
		code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit(f.dateien...))
		if code != f.status || !strings.Contains(roh, `"grund":"`+f.grund+`"`) {
			t.Errorf("%v: %d %s", f.dateien, code, roh)
		}
	}
	// Datei an der Karte, aber nicht in der Ablage.
	e.ablage.mu.Lock()
	delete(e.ablage.dateien, slBoard+"/k1/t1")
	e.ablage.mu.Unlock()
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("t1")); code != http.StatusNotFound || !strings.Contains(roh, "datei-fehlt") || !strings.Contains(roh, `"datei_id":"t1"`) {
		t.Errorf("fehlende Datei: %d %s", code, roh)
	}
	if e.zeilen(t) != 0 || e.modell.zahl() != 0 {
		t.Fatalf("Datensätze %d, Modellaufrufe %d", e.zeilen(t), e.modell.zahl())
	}
	// Ohne Ablage: 503 statt still ohne Dateien.
	e.sl.MitAnhaengen(nil)
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("p1")); code != http.StatusServiceUnavailable || !strings.Contains(roh, "kein-anhangspeicher") {
		t.Errorf("ohne Ablage: %d %s", code, roh)
	}
}

// Verify VS4-2: Die Vorschau liefert genau die bereinigten Bytes, die
// hinausgehen — nicht die Miniatur aus dem Karten-Zustand. Sehen darf sie die
// antragstellende Person; eine andere nur als zweite Freigebende bei
// Vier-Augen und nur freigegebene Bilder. Nach einem Tausch: 409.
func TestSchleuseDateien_VorschauZeigtWasHinausgeht(t *testing.T) {
	e := neueDateienEnv(t)
	_, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("b1"))
	a := auftragAus(t, roh)
	id := a["id"].(string)
	pfad := e.pfad("/" + id + "/bilder/b1")
	code, koerper := e.roh(t, http.MethodGet, pfad, slAnna, nil)
	sauber, _, _ := schleuse.BildOhneMetadaten(e.jpeg)
	if code != http.StatusOK || koerper != string(sauber) {
		t.Fatalf("Vorschau: %d, %d Bytes (soll %d)", code, len(koerper), len(sauber))
	}
	if strings.Contains(koerper, slExifGeheim) {
		t.Fatal("Vorschau trägt die Metadaten")
	}
	// Andere Person (editor), kein Vier-Augen: nichts zu sehen.
	if code, _ := e.roh(t, http.MethodGet, pfad, slBert, nil); code != http.StatusNotFound {
		t.Errorf("fremde Person sieht die Vorschau: %d", code)
	}
	if code, _ := e.roh(t, http.MethodGet, e.pfad("/"+id+"/bilder/t1"), slAnna, nil); code != http.StatusNotFound {
		t.Errorf("Nicht-Bild als Vorschau: %d", code)
	}
	// Datei getauscht: 409, keine Bytes.
	e.ablage.lege("b1", "foto.jpg", apiJPEG(t, 10))
	if code, roh := e.roh(t, http.MethodGet, pfad, slAnna, nil); code != http.StatusConflict || !strings.Contains(roh, "datei-veraendert") {
		t.Errorf("getauschtes Bild: %d %s", code, roh)
	}
}

// Vier-Augen: Die zweite Person (Bert, editor) sieht genau die freigegebenen
// Bilder des wartenden Auftrags, Vera (viewer) nicht; nach der zweiten
// Freigabe ist die Vorschau für Bert wieder zu.
func TestSchleuseDateien_VorschauFuerZweiteFreigabe(t *testing.T) {
	e := neueDateienEnv(t)
	if _, err := e.db.Exec(`UPDATE schleuse_freigaben SET vier_augen = 1 WHERE id = ?`, e.freigabeID); err != nil {
		t.Fatal(err)
	}
	e.ablage.lege("t2", "zweites.jpg", apiJPEG(t, 90))
	st := strings.Replace(slStateDateien(), `{"id":"z1"`, `{"id":"t2","filename":"zweites.jpg","mime":"image/jpeg","size":10},{"id":"z1"`, 1)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, st, slBoard); err != nil {
		t.Fatal(err)
	}
	_, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszugMit("b1", "t2"))
	a := auftragAus(t, roh)
	id := a["id"].(string)
	_, roh = e.roh(t, http.MethodPut, e.pfad("/"+id), slAnna, map[string]any{"text": a["text"], "basis_sha256": a["sha256"], "bilder_freigegeben": []string{"b1"}})
	g := auftragAus(t, roh)
	// Vor Annas Freigabe wartet nichts auf Bert.
	if code, _ := e.roh(t, http.MethodGet, e.pfad("/"+id+"/bilder/b1"), slBert, nil); code != http.StatusNotFound {
		t.Fatalf("Bert vor der Freigabe: %d", code)
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": g["sha256"]}); code != http.StatusOK {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	if code, _ := e.roh(t, http.MethodGet, e.pfad("/"+id+"/bilder/b1"), slBert, nil); code != http.StatusOK {
		t.Errorf("Bert sieht das freigegebene Bild nicht: %d", code)
	}
	if code, _ := e.roh(t, http.MethodGet, e.pfad("/"+id+"/bilder/t2"), slBert, nil); code != http.StatusNotFound {
		t.Errorf("Bert sieht ein nicht freigegebenes Bild: %d", code)
	}
	if code, _ := e.roh(t, http.MethodGet, e.pfad("/"+id+"/bilder/b1"), slVera, nil); code != http.StatusNotFound {
		t.Errorf("Viewer sieht die Vorschau: %d", code)
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/zweitfreigabe"), slBert, map[string]any{"sha256": g["sha256"]}); code != http.StatusOK {
		t.Fatalf("Zweitfreigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if code, _ := e.roh(t, http.MethodGet, e.pfad("/"+id+"/bilder/b1"), slBert, nil); code != http.StatusNotFound {
		t.Errorf("Bert nach der Zweitfreigabe: %d", code)
	}
}
