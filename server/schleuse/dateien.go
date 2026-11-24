package schleuse

// dateien.go — Dateien im Auszug der Ausgangsschleuse, Stufe 2 (ADR-0134 §5,
// Karte #687 — Teil S4).
//
// Zwei Wege, streng getrennt:
//
//   - Text und PDF werden zu TEXT. Er läuft als eigener Abschnitt durch
//     BaueAuszug und damit durch dieselbe Normalisierung und dieselbe
//     zweistufige Pseudonymisierung wie Beschreibung und Kommentare, steht
//     markiert in der Durchsicht und ist Teil des gehashten Texts. Was die
//     Extraktion nicht erfasst (Bilder in einer PDF, Formularfelder, Text als
//     Grafik), geht NICHT hinaus — die Durchsicht zeigt genau das, was gesendet
//     wird.
//   - Bilder gehen NIE pseudonymisiert hinaus. Sie werden neu kodiert
//     (BildOhneMetadaten): Was in der Datei außer den Bildpunkten steht —
//     EXIF samt GPS und Kamera, XMP, IPTC, Autor, Kommentare, eingebettete
//     Vorschaubilder, angehängte Bytes —, gibt es in der neuen Datei nicht.
//     Mit dürfen sie nur nach Einzelfreigabe (BildEintrag.Freigegeben); der
//     Hash des Auftrags (AuszugHash) umfasst die bereinigten Bytes jedes
//     freigegebenen Bildes.
//
// Was nicht in eine der beiden Arten fällt (Office-Dateien, Archive, WebP,
// alles Unbekannte), lehnt ErkenneArt ab: Für solche Dateien gibt es weder
// eine Textextraktion, die durch die Durchsicht ginge, noch eine Bereinigung
// der Metadaten.
//
// Den Typ bestimmt der Inhalt (http.DetectContentType), nie Name oder
// Angabe im Karten-Zustand (Muster F-1094): Eine „notiz.txt" mit PDF-Inhalt
// wird als PDF gelesen, eine „bild.png" mit Text als Text.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Arten einer Datei im Auszug.
const (
	DateiArtText = "text"
	DateiArtPDF  = "pdf"
	DateiArtBild = "bild"
)

// Grenzen der Dateien eines Auszugs. Die Bildgrenzen (Anzahl, Bytes je Bild,
// Summe) prüft der Aufrufer gegen die des LLM-Pakets (llm.PruefeBild,
// llm.PruefeBildMenge) — dasselbe Maß wie beim Board-Chat (ADR-0121).
const (
	// DateienHoechst: so viele Dateien darf ein Auszug wählen.
	DateienHoechst = 10
	// DateiQuelleHoechstBytes: größte Datei, die gelesen wird (Text, PDF,
	// Bild vor der Bereinigung). Die Text-Grenze des Auszugs (Zeichen)
	// greift danach zusätzlich.
	DateiQuelleHoechstBytes = 20 << 20
	// DateienQuelleSummeHoechst: Summe aller gewählten Dateien.
	DateienQuelleSummeHoechst = 40 << 20
	// PDFSeitenHoechst: mehr Seiten liest die Extraktion nicht — eine
	// längere PDF wird abgelehnt, nicht stillschweigend gekürzt.
	PDFSeitenHoechst = 300
	// BildPixelHoechst / BildKanteHoechst: Schutz vor Dekompressionsbomben,
	// geprüft am Kopf der Datei, BEVOR die Bildpunkte dekodiert werden.
	BildPixelHoechst = 25_000_000
	BildKanteHoechst = 12_000
	// bildJPEGQualitaet der Neukodierung.
	bildJPEGQualitaet = 90
)

// PDFFrist begrenzt die Textextraktion einer PDF; danach wird der
// Kindprozess beendet. var: Tests verkürzen sie.
var PDFFrist = 20 * time.Second

var (
	// ErrDateiArt: Die Datei ist weder Text (UTF-8), PDF noch ein Bild, das
	// sich bereinigen lässt (JPEG, PNG, GIF).
	ErrDateiArt = errors.New("schleuse: dateiart wird nicht unterstützt")
	// ErrDateiZuGross: Datei oder Bild überschreitet eine der Grenzen.
	ErrDateiZuGross = errors.New("schleuse: datei zu groß")
	// ErrDateiUnlesbar: Inhalt passt nicht zur erkannten Art (kaputte PDF,
	// verschlüsselte PDF, Bild nicht dekodierbar).
	ErrDateiUnlesbar = errors.New("schleuse: datei nicht lesbar")
	// ErrPDFOhneText: Die PDF enthält keinen auslesbaren Text (Scan) — es
	// gibt keine Texterkennung.
	ErrPDFOhneText = errors.New("schleuse: pdf ohne auslesbaren text")
	// ErrPDFZeit: Die Extraktion hat PDFFrist überschritten.
	ErrPDFZeit = errors.New("schleuse: pdf-extraktion hat zu lange gedauert")
)

// ErkenneArt bestimmt die Art einer Datei aus ihren ersten Bytes. Für Bilder
// liefert sie zusätzlich den erkannten MIME-Typ. Unbekannt: ErrDateiArt.
func ErkenneArt(kopf []byte) (art, mime string, err error) {
	erkannt := http.DetectContentType(kopf)
	basis := erkannt
	if i := strings.IndexByte(basis, ';'); i >= 0 {
		basis = strings.TrimSpace(basis[:i])
	}
	switch {
	case basis == "application/pdf":
		return DateiArtPDF, basis, nil
	case basis == "image/jpeg", basis == "image/png", basis == "image/gif":
		return DateiArtBild, basis, nil
	case erkannt == "text/plain; charset=utf-8":
		// UTF-16 erkennt DetectContentType als eigenen Zeichensatz — nur UTF-8
		// ist Text im Sinne der Schleuse (Normalisiere verlangt es).
		return DateiArtText, basis, nil
	}
	return "", "", fmt.Errorf("%w (%s)", ErrDateiArt, basis)
}

// TextAusDatei liefert den Inhalt einer Textdatei. Ein BOM fällt weg;
// ungültiges UTF-8 oder NUL-Bytes sind ErrDateiUnlesbar (Binärdaten, die der
// Kopf als Text durchgelassen hat).
func TextAusDatei(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", ErrDateiUnlesbar
	}
	return string(data), nil
}

// TextAusPDF extrahiert den Text einer PDF, Seite für Seite, getrennt durch
// eine Leerzeile — in einem KINDPROZESS (Verify VS4-1, Karte #687).
//
// Warum ein eigener Prozess: Die Bibliothek parst Fremddaten, und ihr
// Seitenbaum-Abstieg hat keinen Zyklenschutz. Eine 0,5-KB-PDF, deren /Pages
// sich selbst als Kind führt, lässt Reader.Page endlos rechnen. Eine
// Goroutine lässt sich in Go nicht abbrechen: Die frühere Fassung meldete
// nach der Frist ErrPDFZeit, die Goroutine hielt aber einen Kern bis zum
// Neustart fest — bis zu 30-mal je Stunde und Person. Einen Prozess beendet
// das Betriebssystem. Der Kindprozess ist der Server selbst
// (os.Executable), gestartet mit PDFKindEnv; PDFKindprozess am Anfang von
// main() bzw. TestMain fängt ihn ab. Er bekommt KEINE Umgebung des Servers
// (also keinen ABOARD_SECRET_KEY, keine Schlüssel), nur die PDF auf stdin,
// höchstens einen Kern und eine weiche Speichergrenze. Läuft PDFFrist ab,
// wird er mit SIGKILL beendet und eingesammelt, bevor TextAusPDF zurückkehrt.
func TextAusPDF(data []byte) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("%w: kindprozess: %v", ErrDateiUnlesbar, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), PDFFrist)
	defer cancel()
	// Sitzungs-Kritiker SK4-4: höchstens pdfKinderHoechst Kindprozesse
	// gleichzeitig, serverweit. Das Warten zählt gegen dieselbe Frist.
	select {
	case pdfKinder <- struct{}{}:
		defer func() { <-pdfKinder }()
	case <-ctx.Done():
		return "", ErrPDFZeit
	}
	cmd := exec.CommandContext(ctx, exe, pdfKindMarker)
	cmd.Env = kindUmgebung()
	cmd.Stdin = bytes.NewReader(data)
	var aus bytes.Buffer
	cmd.Stdout = &begrenzt{w: &aus, rest: pdfAusgabeHoechst}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		return "", ErrPDFZeit
	}
	if err != nil {
		// SK4-5: Startet der Kindprozess gar nicht (typisch: Binärdatei im
		// laufenden Betrieb ersetzt, os.Executable zeigt dann auf „(deleted)“),
		// scheitert jede PDF — das gehört ins Log, mit Anlass, nie mit Inhalt.
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			log.Printf("aboard-server: Ausgangsschleuse: PDF-Kindprozess nicht startbar (%v) — nach Austausch der Binärdatei Server neu starten", err)
		}
		return "", fmt.Errorf("%w: kindprozess endete mit fehler", ErrDateiUnlesbar)
	}
	var e pdfErgebnis
	if json.Unmarshal(aus.Bytes(), &e) != nil {
		return "", fmt.Errorf("%w: kindprozess-ausgabe unlesbar", ErrDateiUnlesbar)
	}
	switch e.Fehler {
	case "":
		return e.Text, nil
	case "ohne-text":
		return "", ErrPDFOhneText
	case "zu-gross":
		return "", fmt.Errorf("%w: zu viele seiten", ErrDateiZuGross)
	}
	return "", ErrDateiUnlesbar
}

// kindUmgebung ist die GESAMTE Umgebung des Kindprozesses — fest, nichts
// vom Server geerbt.
func kindUmgebung() []string { return []string{PDFKindEnv + "=1", "GOMAXPROCS=1"} }

// pdfKinderHoechst: so viele PDF-Kindprozesse laufen höchstens gleichzeitig.
const pdfKinderHoechst = 4

var pdfKinder = make(chan struct{}, pdfKinderHoechst)

// pdfKindMarker ist das erste Argument des Kindprozesses. Erst Umgebung UND
// Argument zusammen machen den Prozess zum Kind (SK4-5): Steht die Variable
// versehentlich in der Umgebung des Servers, startet er trotzdem normal,
// statt stdin zu lesen und still mit Exit 0 zu enden.
const pdfKindMarker = "__aboard-schleuse-pdf-kind"

// PDFKindEnv markiert den Kindprozess der PDF-Extraktion.
const PDFKindEnv = "ABOARD_SCHLEUSE_PDF_KIND"

// pdfAusgabeHoechst begrenzt, was der Kindprozess zurückgeben darf.
const pdfAusgabeHoechst = 64 << 20

// pdfKindSpeicher ist die weiche Speichergrenze des Kindprozesses.
const pdfKindSpeicher = 1 << 30

type pdfErgebnis struct {
	Text   string `json:"text"`
	Fehler string `json:"fehler,omitempty"`
}

// begrenzt schreibt höchstens rest Bytes und meldet danach einen Fehler —
// ein Kindprozess, der mehr ausgibt, scheitert, statt den Server zu füllen.
type begrenzt struct {
	w    io.Writer
	rest int
}

func (b *begrenzt) Write(p []byte) (int, error) {
	if len(p) > b.rest {
		return 0, errors.New("schleuse: pdf-ausgabe zu groß")
	}
	b.rest -= len(p)
	return b.w.Write(p)
}

// PDFKindprozess läuft als ERSTES in main() und in TestMain der Pakete, die
// TextAusPDF aufrufen: Ist der Prozess der Kindprozess der PDF-Extraktion,
// liest er die PDF von stdin, schreibt das Ergebnis als JSON auf stdout und
// beendet sich — er kehrt dann nie zurück. Sonst tut er nichts.
func PDFKindprozess() {
	if os.Getenv(PDFKindEnv) != "1" || len(os.Args) < 2 || os.Args[1] != pdfKindMarker {
		return
	}
	debug.SetMemoryLimit(pdfKindSpeicher)
	data, err := io.ReadAll(io.LimitReader(os.Stdin, DateiQuelleHoechstBytes+1))
	var e pdfErgebnis
	switch {
	case err != nil || len(data) > DateiQuelleHoechstBytes:
		e.Fehler = "unlesbar"
	default:
		e = pdfKindArbeit(data)
	}
	_ = json.NewEncoder(os.Stdout).Encode(e)
	os.Exit(0)
}

func pdfKindArbeit(data []byte) (e pdfErgebnis) {
	defer func() {
		if r := recover(); r != nil {
			e = pdfErgebnis{Fehler: "unlesbar"}
		}
	}()
	t, err := pdfText(data)
	switch {
	case err == nil:
		return pdfErgebnis{Text: t}
	case errors.Is(err, ErrPDFOhneText):
		return pdfErgebnis{Fehler: "ohne-text"}
	case errors.Is(err, ErrDateiZuGross):
		return pdfErgebnis{Fehler: "zu-gross"}
	}
	return pdfErgebnis{Fehler: "unlesbar"}
}

func pdfText(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		// Auch eine verschlüsselte PDF mit Benutzerkennwort landet hier
		// (ErrInvalidPassword) — es gibt keinen Weg, ein Kennwort anzugeben.
		return "", fmt.Errorf("%w: %v", ErrDateiUnlesbar, err)
	}
	seiten := r.NumPage()
	if seiten < 1 {
		return "", ErrPDFOhneText
	}
	if seiten > PDFSeitenHoechst {
		return "", fmt.Errorf("%w: %d seiten, höchstens %d", ErrDateiZuGross, seiten, PDFSeitenHoechst)
	}
	var teile []string
	for i := 1; i <= seiten; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		// nil: die Schriften DIESER Seite. Die Bibliothek sammelt sie sonst
		// über alle Seiten nach Namen — zwei Seiten mit verschiedenen Schriften
		// gleichen Namens („F1“) ergäben dann Zeichensalat.
		t, err := p.GetPlainText(nil)
		if err != nil {
			return "", fmt.Errorf("%w: seite %d", ErrDateiUnlesbar, i)
		}
		if t = strings.TrimSpace(t); t != "" {
			teile = append(teile, t)
		}
	}
	if len(teile) == 0 {
		return "", ErrPDFOhneText
	}
	return strings.Join(teile, "\n\n"), nil
}

// BildOhneMetadaten dekodiert ein Bild und kodiert es neu — JPEG als JPEG,
// PNG und GIF als PNG (bei GIF nur das erste Einzelbild). Die neue Datei
// enthält nur die Bildpunkte. Die Neukodierung ist deterministisch: Dieselbe
// Eingabe ergibt mit derselben Go-Version dieselben Bytes — darauf beruht der
// Vergleich beim Versand mit dem Hash aus der Durchsicht.
//
// Folge, ausdrücklich: Die EXIF-Ausrichtung geht mit den Metadaten verloren;
// ein hochkant fotografiertes Bild kann gedreht ankommen.
func BildOhneMetadaten(data []byte) ([]byte, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrDateiUnlesbar, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", ErrDateiUnlesbar
	}
	if cfg.Width > BildKanteHoechst || cfg.Height > BildKanteHoechst ||
		int64(cfg.Width)*int64(cfg.Height) > BildPixelHoechst {
		return nil, "", fmt.Errorf("%w: %d×%d bildpunkte", ErrDateiZuGross, cfg.Width, cfg.Height)
	}
	var img image.Image
	switch format {
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(data))
	case "png":
		img, err = png.Decode(bytes.NewReader(data))
	case "gif":
		img, err = gif.Decode(bytes.NewReader(data))
	default:
		return nil, "", fmt.Errorf("%w (%s)", ErrDateiArt, format)
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrDateiUnlesbar, err)
	}
	var buf bytes.Buffer
	if format == "jpeg" {
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: bildJPEGQualitaet}); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrDateiUnlesbar, err)
		}
		return buf.Bytes(), "image/jpeg", nil
	}
	// Verify VS4-4: Ein Palettenbild behielte beim Neukodieren ALLE
	// Palettenfarben, auch ungenutzte — ein verdeckter Kanal. Als RGBA gibt es
	// keine Palette mehr, nur die Bildpunkte.
	//
	// Sitzungs-Kritiker SK4-1: Unter Alpha 0 behielte ein NRGBA-Bild seine
	// Farbwerte — unsichtbar, also von niemandem durchgesehen, und trotzdem
	// gesendet. Deshalb wird JEDES PNG/GIF in ein frisches NRGBA gezeichnet
	// und jedes vollständig durchsichtige Pixel auf (0,0,0,0) gesetzt.
	n := image.NewNRGBA(img.Bounds())
	draw.Draw(n, n.Bounds(), img, img.Bounds().Min, draw.Src)
	for i := 0; i+3 < len(n.Pix); i += 4 {
		if n.Pix[i+3] == 0 {
			n.Pix[i], n.Pix[i+1], n.Pix[i+2] = 0, 0, 0
		}
	}
	img = n
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrDateiUnlesbar, err)
	}
	return buf.Bytes(), "image/png", nil
}

// HashBytes ist der SHA-256 (hex, klein) über Bytes.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// BildEintrag ist ein Bild im Auftrag — OHNE Inhalt und ohne Dateinamen
// (der kann Personenbezug tragen; die Oberfläche löst ihn über die id aus
// dem Board auf). SHA256 und Bytes gelten für die BEREINIGTE Fassung, also
// genau das, was hinausgeht.
type BildEintrag struct {
	ID          string `json:"id"`
	MIME        string `json:"mime"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	Freigegeben bool   `json:"freigegeben"`
}

// FreigegebeneBilder liefert die Bilder, die mitgehen — in Auftragsreihenfolge.
func FreigegebeneBilder(bilder []BildEintrag) []BildEintrag {
	var out []BildEintrag
	for _, b := range bilder {
		if b.Freigegeben {
			out = append(out, b)
		}
	}
	return out
}

// AuszugHash ist der Hash eines Auftrags, an den die Freigabe gebunden ist
// (ADR-0134 §3.4, §5): ohne freigegebene Bilder der Hash des Texts (Hash) —
// derselbe Wert wie vor S4, bestehende Aufträge behalten ihn —, sonst ein
// SHA-256 über den Text-Hash und die Hashes der freigegebenen Bilder in
// Auftragsreihenfolge. Ein Bild, das nicht freigegeben ist, geht nicht hinaus
// und zählt nicht; seine Freigabe ändert den Hash.
func AuszugHash(text string, bilder []BildEintrag) string {
	frei := FreigegebeneBilder(bilder)
	if len(frei) == 0 {
		return Hash(text)
	}
	h := sha256.New()
	fmt.Fprintf(h, "aboard-schleuse-auszug/2\ntext %s\n", Hash(text))
	for _, b := range frei {
		fmt.Fprintf(h, "bild %s\n", b.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
