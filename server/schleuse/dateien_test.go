package schleuse

// dateien_test.go — Dateien im Auszug (ADR-0134 §5, Karte #687).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// minimalPDF baut eine gültige PDF mit einer Seite je Text (Helvetica, nur
// ASCII) — mit korrekter Querverweistabelle, damit der Parser sie ohne
// Reparatur liest.
func TestMain(m *testing.M) {
	// Karte #687: Dieser Testprozess ist auch der Kindprozess der
	// PDF-Extraktion (TextAusPDF startet os.Executable).
	PDFKindprozess()
	os.Exit(m.Run())
}

func minimalPDF(t *testing.T, seiten ...string) []byte {
	t.Helper()
	var objekte []string
	n := len(seiten)
	// 1 Katalog, 2 Seitenbaum, 3 Schrift, dann je Seite: Seite + Inhalt.
	kids := make([]string, n)
	for i := range seiten {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objekte = append(objekte,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	)
	for i, text := range seiten {
		inhalt := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		objekte = append(objekte,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(inhalt), inhalt),
		)
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objekte))
	for i, o := range objekte {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objekte)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objekte)+1, xref)
	return b.Bytes()
}

func TestErkenneArt(t *testing.T) {
	jpg := testJPEG(t, 4, 4)
	fall := []struct {
		name string
		kopf []byte
		art  string
		mime string
	}{
		{"text", []byte("Hallo Welt\nzweite Zeile"), DateiArtText, "text/plain"},
		{"text-umlaut", []byte("Grüße aus Köln"), DateiArtText, "text/plain"},
		{"pdf", minimalPDF(t, "x"), DateiArtPDF, "application/pdf"},
		{"jpeg", jpg, DateiArtBild, "image/jpeg"},
		{"png", testPNG(t, 3, 3), DateiArtBild, "image/png"},
	}
	for _, f := range fall {
		art, mime, err := ErkenneArt(f.kopf)
		if err != nil || art != f.art || mime != f.mime {
			t.Errorf("%s: art=%q mime=%q err=%v, erwartet %q %q", f.name, art, mime, err, f.art, f.mime)
		}
	}
	// Abgelehnt: WebP (kein Bereiniger), ZIP/Office, UTF-16, Binärdaten.
	abgelehnt := map[string][]byte{
		"webp":   append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 20)...),
		"zip":    []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"),
		"utf16":  []byte("\xFF\xFEH\x00a\x00l\x00l\x00o\x00"),
		"binaer": {0x00, 0x01, 0x02, 0x03, 0xff, 0xfe, 0x00, 0x10},
	}
	for name, kopf := range abgelehnt {
		if _, _, err := ErkenneArt(kopf); !errors.Is(err, ErrDateiArt) {
			t.Errorf("%s: err=%v, erwartet ErrDateiArt", name, err)
		}
	}
}

func TestTextAusDatei(t *testing.T) {
	got, err := TextAusDatei([]byte("\xEF\xBB\xBFZeile"))
	if err != nil || got != "Zeile" {
		t.Fatalf("BOM: %q %v", got, err)
	}
	if _, err := TextAusDatei([]byte("a\x00b")); !errors.Is(err, ErrDateiUnlesbar) {
		t.Errorf("NUL: %v", err)
	}
	if _, err := TextAusDatei([]byte("a\xffb")); !errors.Is(err, ErrDateiUnlesbar) {
		t.Errorf("kein UTF-8: %v", err)
	}
}

func TestTextAusPDF(t *testing.T) {
	got, err := TextAusPDF(minimalPDF(t, "Angebot fuer Anna Mueller", "Seite zwei"))
	if err != nil {
		t.Fatalf("TextAusPDF: %v", err)
	}
	if !strings.Contains(got, "Anna Mueller") || !strings.Contains(got, "Seite zwei") {
		t.Fatalf("Text fehlt: %q", got)
	}
	if !strings.Contains(got, "\n\n") {
		t.Errorf("Seiten nicht getrennt: %q", got)
	}
}

func TestTextAusPDFOhneText(t *testing.T) {
	if _, err := TextAusPDF(minimalPDF(t, "")); !errors.Is(err, ErrPDFOhneText) {
		t.Fatalf("err=%v, erwartet ErrPDFOhneText", err)
	}
}

func TestTextAusPDFKaputt(t *testing.T) {
	kaputt := []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << >>\n%%EOF")
	if _, err := TextAusPDF(kaputt); !errors.Is(err, ErrDateiUnlesbar) {
		t.Fatalf("err=%v, erwartet ErrDateiUnlesbar", err)
	}
	// Abgeschnittene, sonst gültige PDF: darf nicht abstürzen.
	ganz := minimalPDF(t, "Text")
	for _, n := range []int{20, len(ganz) / 2, len(ganz) - 10} {
		if _, err := TextAusPDF(ganz[:n]); err == nil {
			t.Errorf("abgeschnitten bei %d: kein Fehler", n)
		}
	}
}

func TestTextAusPDFZuVieleSeiten(t *testing.T) {
	seiten := make([]string, PDFSeitenHoechst+1)
	for i := range seiten {
		seiten[i] = "x"
	}
	if _, err := TextAusPDF(minimalPDF(t, seiten...)); !errors.Is(err, ErrDateiZuGross) {
		t.Fatalf("err=%v, erwartet ErrDateiZuGross", err)
	}
}

// zyklischePDF: /Pages führt sich selbst als Kind — der Seitenbaum-Abstieg
// der Bibliothek kommt nie zurück (Verify VS4-1).
func zyklischePDF() []byte {
	var b bytes.Buffer
	var off []int
	obj := func(s string) { off = append(off, b.Len()); b.WriteString(s) }
	b.WriteString("%PDF-1.4\n")
	obj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	obj("2 0 obj\n<< /Type /Pages /Count 1 /Kids [2 0 R] >>\nendobj\n")
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 3\n0000000000 65535 f \n")
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
	return b.Bytes()
}

// eigeneCPU: CPU-Zeit DIESES Testprozesses (Nutzer + System).
func eigeneCPU() time.Duration {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}

// Verify VS4-1: Eine PDF, an der der Parser endlos rechnet, endet mit
// ErrPDFZeit — und danach rechnet NICHTS mehr weiter. Die frühere Fassung
// (Goroutine) meldete ebenfalls ErrPDFZeit, hielt aber einen Kern fest; der
// alte Test prüfte nur den Timer und blieb deshalb grün (VS4-3).
func TestTextAusPDFFristBeendetDieArbeit(t *testing.T) {
	alt := PDFFrist
	PDFFrist = 1500 * time.Millisecond
	defer func() { PDFFrist = alt }()
	g0 := runtime.NumGoroutine()
	// SK4-2: Die eigene CPU-Zeit sieht einen überlebenden Kindprozess nicht.
	// Rückkehr heißt aber: cmd.Run hat das Kind eingesammelt. Bleibt das Kind
	// am Leben (kein Kill), kehrt TextAusPDF nicht zurück — das fängt die
	// Gesamtfrist hier, statt den Test hängen zu lassen.
	fertig := make(chan error, 1)
	go func() { _, err := TextAusPDF(zyklischePDF()); fertig <- err }()
	select {
	case err := <-fertig:
		if !errors.Is(err, ErrPDFZeit) {
			t.Fatalf("err=%v, erwartet ErrPDFZeit", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("TextAusPDF kehrt nach der Frist nicht zurück — der Kindprozess wird nicht beendet")
	}
	c0 := eigeneCPU()
	time.Sleep(1500 * time.Millisecond)
	if d := eigeneCPU() - c0; d > 500*time.Millisecond {
		t.Fatalf("nach der Frist rechnet weiter etwas: %v CPU in 1,5 s", d)
	}
	if g := runtime.NumGoroutine(); g > g0+1 {
		t.Fatalf("Goroutinen %d → %d: etwas läuft weiter", g0, g)
	}
}

// Der Kindprozess bekommt die Umgebung des Servers NICHT (Schlüssel,
// ABOARD_SECRET_KEY): cmd.Env ist kindUmgebung(), fest. Die Extraktion selbst
// läuft dabei über den echten Kindprozess.
func TestPDFKindprozessOhneServerUmgebung(t *testing.T) {
	t.Setenv("ABOARD_SECRET_KEY", "geheim-darf-nicht-ins-kind")
	got, err := TextAusPDF(minimalPDF(t, "Umgebung"))
	if err != nil || !strings.Contains(got, "Umgebung") {
		t.Fatalf("Extraktion im Kindprozess: %q %v", got, err)
	}
	if v := kindUmgebung(); strings.Contains(strings.Join(v, "\n"), "geheim") {
		t.Fatalf("Kindprozess erbt die Umgebung: %v", v)
	}
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 40), uint8(y * 40), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// mitExif setzt hinter den SOI-Marker eines JPEG einen APP1-Abschnitt mit
// EXIF-Kennung und einem auffälligen Text (Kamera, GPS stehen dort so).
func mitExif(jpg []byte, geheim string) []byte {
	nutz := append([]byte("Exif\x00\x00"), []byte(geheim)...)
	seg := []byte{0xFF, 0xE1, byte((len(nutz) + 2) >> 8), byte(len(nutz) + 2)}
	seg = append(seg, nutz...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func TestBildOhneMetadatenJPEG(t *testing.T) {
	const geheim = "GPS 50.9375N 6.9603E Kamera von Anna Mueller"
	roh := append(mitExif(testJPEG(t, 16, 8), geheim), []byte("ANGEHAENGT-"+geheim)...)
	if !bytes.Contains(roh, []byte(geheim)) {
		t.Fatal("Testaufbau: Metadaten fehlen in der Eingabe")
	}
	out, mime, err := BildOhneMetadaten(roh)
	if err != nil || mime != "image/jpeg" {
		t.Fatalf("mime=%q err=%v", mime, err)
	}
	if bytes.Contains(out, []byte(geheim)) || bytes.Contains(out, []byte("Exif")) {
		t.Fatal("Metadaten oder angehängte Bytes stehen noch in der bereinigten Datei")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || cfg.Width != 16 || cfg.Height != 8 {
		t.Fatalf("Ergebnis: %v %q %v", cfg, format, err)
	}
	// Deterministisch: dieselbe Eingabe → dieselben Bytes (Hash beim Versand).
	out2, _, _ := BildOhneMetadaten(roh)
	if !bytes.Equal(out, out2) {
		t.Fatal("Neukodierung nicht deterministisch")
	}
}

func TestBildOhneMetadatenPNG(t *testing.T) {
	const geheim = "Autor: Anna Mueller"
	roh := testPNG(t, 5, 5)
	// Text hinter IEND: Ein Bildbetrachter zeigt ihn nicht, ein Empfänger
	// kann ihn lesen.
	roh = append(roh, []byte(geheim)...)
	out, mime, err := BildOhneMetadaten(roh)
	if err != nil || mime != "image/png" {
		t.Fatalf("mime=%q err=%v", mime, err)
	}
	if bytes.Contains(out, []byte(geheim)) {
		t.Fatal("angehängte Bytes stehen noch in der bereinigten Datei")
	}
}

func TestBildOhneMetadatenGIFWirdPNG(t *testing.T) {
	var b bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 4, 4), []color.Color{color.Black, color.White})
	if err := gif.Encode(&b, pal, nil); err != nil {
		t.Fatal(err)
	}
	out, mime, err := BildOhneMetadaten(b.Bytes())
	if err != nil || mime != "image/png" {
		t.Fatalf("mime=%q err=%v", mime, err)
	}
	if _, f, err := image.DecodeConfig(bytes.NewReader(out)); err != nil || f != "png" {
		t.Fatalf("kein PNG: %q %v", f, err)
	}
	// VS4-4: keine Palette mehr (ungenutzte Einträge wären ein verdeckter Kanal).
	if dec, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatal(err)
	} else if _, ok := dec.(*image.Paletted); ok {
		t.Fatal("Ergebnis ist weiter ein Palettenbild")
	}
}

// SK4-1: Unter Alpha 0 darf keine Farbe überleben (unsichtbar, ungeprüft).
func TestBildOhneMetadatenAlphaNull(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{222, 173, 190, 0})
	img.SetNRGBA(1, 0, color.NRGBA{10, 20, 30, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	out, _, err := BildOhneMetadaten(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, bl, a := dec.At(0, 0).RGBA(); r|g|bl|a != 0 {
		t.Fatalf("durchsichtiges Pixel trägt noch Farbe: %v", color.NRGBAModel.Convert(dec.At(0, 0)))
	}
	if got := color.NRGBAModel.Convert(dec.At(1, 0)).(color.NRGBA); got != (color.NRGBA{10, 20, 30, 255}) {
		t.Fatalf("sichtbares Pixel verändert: %v", got)
	}
	// RGBA() ist vormultipliziert und zeigt Farbe unter Alpha 0 ohnehin als 0 —
	// deshalb zusätzlich die gespeicherten Bytes prüfen.
	if n, ok := dec.(*image.NRGBA); ok && (n.Pix[0] != 0 || n.Pix[1] != 0 || n.Pix[2] != 0) {
		t.Fatalf("Farbbytes unter Alpha 0: %v", n.Pix[:4])
	}
}

func TestBildOhneMetadatenBombe(t *testing.T) {
	// Kopf behauptet 20000×20000 — abgelehnt, BEVOR dekodiert wird.
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	roh := b.Bytes()
	// IHDR: Breite ab Byte 16, Höhe ab Byte 20 (big endian).
	for _, i := range []int{16, 20} {
		roh[i], roh[i+1], roh[i+2], roh[i+3] = 0, 0, 0x4E, 0x20
	}
	// CRC des IHDR-Chunks (Typ + Daten, Bytes 12–28) nachziehen — sonst
	// scheiterte die Datei an der Prüfsumme statt an der Größe.
	binary.BigEndian.PutUint32(roh[29:33], crc32.ChecksumIEEE(roh[12:29]))
	if _, _, err := BildOhneMetadaten(roh); !errors.Is(err, ErrDateiZuGross) {
		t.Fatalf("err=%v, erwartet ErrDateiZuGross", err)
	}
	if _, _, err := BildOhneMetadaten([]byte("kein bild")); !errors.Is(err, ErrDateiUnlesbar) {
		t.Fatalf("err=%v, erwartet ErrDateiUnlesbar", err)
	}
}

func TestAuszugHash(t *testing.T) {
	const text = "## Aufgabe\n\nBitte prüfen"
	if AuszugHash(text, nil) != Hash(text) {
		t.Fatal("ohne Bilder muss der Hash dem Text-Hash entsprechen (Bestand vor S4)")
	}
	nichtFrei := []BildEintrag{{ID: "a", SHA256: HashBytes([]byte("x"))}}
	if AuszugHash(text, nichtFrei) != Hash(text) {
		t.Fatal("ein nicht freigegebenes Bild geht nicht hinaus und darf den Hash nicht ändern")
	}
	frei := []BildEintrag{{ID: "a", SHA256: HashBytes([]byte("x")), Freigegeben: true}}
	h1 := AuszugHash(text, frei)
	if h1 == Hash(text) {
		t.Fatal("ein freigegebenes Bild muss den Hash ändern")
	}
	anders := []BildEintrag{{ID: "a", SHA256: HashBytes([]byte("y")), Freigegeben: true}}
	if AuszugHash(text, anders) == h1 {
		t.Fatal("andere Bildbytes, gleicher Hash")
	}
	zwei := append(append([]BildEintrag{}, frei...), anders...)
	umgedreht := append(append([]BildEintrag{}, anders...), frei...)
	if AuszugHash(text, zwei) == AuszugHash(text, umgedreht) {
		t.Fatal("Reihenfolge der Bilder ändert den Hash nicht")
	}
}

func TestBaueAuszugMitDateien(t *testing.T) {
	got := BaueAuszug(AuszugTeile{Aufgabe: "Prüfen", Dateien: []DateiTeil{
		{Name: "angebot.pdf", Text: "Inhalt eins"}, {Name: "", Text: "  "}, {Name: "notiz.txt", Text: "Inhalt zwei"},
	}})
	want := "## Aufgabe\n\nPrüfen\n\n## Datei 1: angebot.pdf\n\nInhalt eins\n\n## Datei 3: notiz.txt\n\nInhalt zwei"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// SK4-5: Die Umgebungsvariable allein macht den Server nicht zum Kind — sonst
// läse er stdin und endete still mit Exit 0, statt zu starten. Ohne den
// Marker kehrt PDFKindprozess zurück (andernfalls endete dieser Testprozess).
func TestPDFKindprozessNurMitMarker(t *testing.T) {
	t.Setenv(PDFKindEnv, "1")
	PDFKindprozess()
}
