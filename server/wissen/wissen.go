// Package wissen liest die gemeinsame Wissensquelle (ADR-0137): einen
// Markdown-Ordner, den der Betreiber nur lesend einhängt (`/data/wissen`,
// ein Git-Checkout, den ein Pull-Prozess aktuell hält).
//
// aBoard liest hier nur. Was dieses Paket zusichert (ADR-0137 §6):
//
//   - Zugriff ausschliesslich über os.Root auf den konfigurierten Ordner —
//     kein Pfad kann den Ordner verlassen, auch nicht über `..` oder einen
//     Symlink nach aussen, und es gibt kein Fenster zwischen Prüfung und
//     Öffnen für den AUSBRUCH. Für Links innerhalb des Ordners bleibt ein
//     Mikrofenster zwischen der Elternteil-Prüfung und dem Öffnen (Kritiker
//     #739 K-7a); ausnutzen könnte es nur, wer den Ordner während des Abrufs
//     umbaut — also der Pull-Prozess selbst.
//   - Symlinks werden ABGELEHNT, nicht aufgelöst — auch solche, die im Ordner
//     bleiben. Ein committeter Link `x.md → .git/config` läge sonst „im
//     Ordner“ und gäbe die Token-URL des Checkouts frei.
//   - Punktdateien und -verzeichnisse (`.git/` ausdrücklich) erscheinen weder
//     im Index noch in der Suche und sind nicht abrufbar. Abrufbar ist nur,
//     was im Index steht: Der Index ist die Erlaubnisliste.
//   - Nur reguläre Dateien mit Endung `.md`, Grössenobergrenze je Seite.
//   - Den Stand (Commit, Datum) liest das Paket aus `.wissen-stand.json`, die
//     der Pull-Prozess schreibt — nie aus `.git/`.
//
// Pfade: Dateinamen werden für den Vergleich auf Unicode-NFC normalisiert
// (macOS legt NFD ab). Nach aussen gilt immer der NFC-Pfad; die Zuordnung zum
// Namen auf der Platte hält der Index.
package wissen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	// StandDatei schreibt der Pull-Prozess in den Wurzelordner (ADR-0137 §1).
	// Sie beginnt mit einem Punkt und ist damit selbst nie als Seite abrufbar.
	StandDatei = ".wissen-stand.json"
	// MaxSeitenBytes begrenzt eine Seite. Grössere Dateien stehen im Index
	// (mit Grösse), werden aber nicht ausgeliefert und nicht durchsucht.
	MaxSeitenBytes = 1 << 20
	// MaxDateien begrenzt den Index. Mehr Seiten → Index gekappt (gemeldet).
	MaxDateien = 5000
	// MaxTiefe begrenzt die Ordnertiefe unter der Wurzel.
	MaxTiefe = 16
	// MaxEintraege begrenzt den Walk über ALLE Einträge (auch Nicht-.md,
	// Kritiker #739 K-7b): Ein Repo voller anderer Dateien verlängert sonst
	// jeden Neuaufbau, und der hält den Mutex.
	MaxEintraege = 50000
	// maxStandBytes begrenzt die Stand-Datei; sie trägt zwei kurze Felder.
	maxStandBytes = 4096
	// titelLeseBytes: Für den Titel wird nur der Anfang einer Seite gelesen.
	titelLeseBytes = 4096
	// OhneStandGueltig: Ohne Stand-Datei gibt es kein Änderungssignal. Der
	// Index wird dann nach dieser Zeit neu aufgebaut, damit ein Ordner ohne
	// Pull-Prozess (Erprobung, Handbetrieb) nicht für immer veraltet.
	OhneStandGueltig = 5 * time.Minute
)

// eintraegeGrenze ist MaxEintraege; Tests setzen sie klein (Verify #739 V-5).
var eintraegeGrenze = MaxEintraege

var (
	// ErrNichtGefunden: Der Pfad steht nicht im Index (gibt es nicht, ist
	// eine Punktdatei, ein Symlink, kein `.md` oder zu tief).
	ErrNichtGefunden = errors.New("wissen: seite nicht gefunden")
	// ErrZuGross: Die Seite überschreitet MaxSeitenBytes.
	ErrZuGross = errors.New("wissen: seite zu gross")
	// ErrVeraendert: Die Datei hat sich zwischen Index und Abruf in etwas
	// verwandelt, das nicht ausgeliefert wird (Symlink, Verzeichnis).
	ErrVeraendert = errors.New("wissen: seite hat sich verändert")
)

// Stand ist der Inhalt von `.wissen-stand.json`, soweit aBoard ihn zeigt.
// Andere Felder der Datei werden ignoriert.
type Stand struct {
	Commit string `json:"commit,omitempty"`
	Datum  string `json:"datum,omitempty"`
}

// Seite ist ein Eintrag im Index.
type Seite struct {
	Pfad    string `json:"pfad"`
	Titel   string `json:"titel"`
	Groesse int64  `json:"groesse"`
}

// Index ist die Momentaufnahme des Ordners. Seiten ist nach Pfad sortiert.
type Index struct {
	Seiten  []Seite `json:"seiten"`
	Stand   *Stand  `json:"stand,omitempty"`
	Gekappt bool    `json:"gekappt,omitempty"`
}

// Quelle ist ein geöffneter Wissensordner. Goroutine-sicher.
type Quelle struct {
	dir  string
	root *os.Root

	// mu schützt nur die Felder darunter — nie den Ordnerlauf (F-1891).
	mu       sync.Mutex
	idx      *Index
	platte   map[string]string // NFC-Pfad → Pfad auf der Platte
	standRoh []byte
	gebaut   time.Time
	jetzt    func() time.Time
	// baut ist offen, solange ein Neuaufbau läuft; geschlossen, wenn er
	// fertig ist (nil: keiner läuft).
	baut chan struct{}
}

// bauNaht: nur für Tests — läuft zu Beginn jedes Neuaufbaus, ohne mu.
var bauNaht func()

// Oeffne öffnet den Ordner dir. Ob dir zulässig ist (Präfix `/data/`),
// prüft PruefeDir — hier wird nur geöffnet.
func Oeffne(dir string) (*Quelle, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("wissen: ordner %q nicht lesbar: %w", dir, err)
	}
	return &Quelle{dir: dir, root: r, jetzt: time.Now}, nil
}

// Dir nennt den geöffneten Ordner.
func (q *Quelle) Dir() string { return q.dir }

// Aktuell sagt, ob unter dem Pfad noch derselbe Ordner liegt, den die Quelle
// geöffnet hat. Ersetzt ein Betreiber den Checkout (rename, neu klonen, Mount
// neu), hinge os.Root sonst für immer am alten Verzeichnis (Kritiker #739 K-3).
func (q *Quelle) Aktuell() bool {
	jetzt, err := os.Stat(q.dir)
	if err != nil {
		return false
	}
	offen, err := q.root.Stat(".")
	if err != nil {
		return false
	}
	return os.SameFile(jetzt, offen)
}

// Schliesse gibt den Ordner frei.
func (q *Quelle) Schliesse() error { return q.root.Close() }

// Index liefert den aktuellen Index. Neu aufgebaut wird, wenn sich die
// Stand-Datei geändert hat, beim ersten Aufruf, und ohne Stand-Datei nach
// OhneStandGueltig.
func (q *Quelle) Index() (*Index, error) {
	idx, _, err := q.aktuell()
	return idx, err
}

// aktuell liefert Index und Plattenpfade. Der Neuaufbau läuft OHNE mu
// (F-1891): Ein Ordnerlauf über bis zu MaxEintraege Einträge hielt sonst
// jede Suche und jeden Seitenabruf auf. Es baut immer nur einer; wer
// währenddessen fragt, bekommt den bisherigen Index — gibt es noch keinen,
// wartet er auf den laufenden Aufbau.
func (q *Quelle) aktuell() (*Index, map[string]string, error) {
	for {
		roh := q.leseStandRoh()
		q.mu.Lock()
		if q.idx != nil && bytes.Equal(roh, q.standRoh) &&
			(roh != nil || q.jetzt().Sub(q.gebaut) < OhneStandGueltig) {
			idx, platte := q.idx, q.platte
			q.mu.Unlock()
			return idx, platte, nil
		}
		if q.baut != nil {
			if q.idx != nil {
				idx, platte := q.idx, q.platte
				q.mu.Unlock()
				return idx, platte, nil
			}
			warte := q.baut
			q.mu.Unlock()
			<-warte
			continue
		}
		fertig := make(chan struct{})
		q.baut = fertig
		q.mu.Unlock()
		return q.neuAufbauen(roh, fertig)
	}
}

// neuAufbauen: der Ordnerlauf ohne mu. Der Abschluss (baut zurücksetzen,
// fertig schließen) steht im defer — auch eine Panik im Lauf darf die
// Wartenden nicht für immer hängen lassen (Kritiker Sprint 87 K-3).
func (q *Quelle) neuAufbauen(roh []byte, fertig chan struct{}) (idx *Index, platte map[string]string, err error) {
	defer func() {
		q.mu.Lock()
		if err == nil && idx != nil {
			q.idx, q.platte, q.standRoh, q.gebaut = idx, platte, roh, q.jetzt()
		}
		q.baut = nil
		close(fertig)
		q.mu.Unlock()
	}()
	// Stand vor dem Lauf auswerten: nach baue() darf nichts mehr scheitern,
	// sonst veröffentlichte der defer einen Index ohne Stand (Verify W-1).
	stand := parseStand(roh)
	if bauNaht != nil {
		bauNaht()
	}
	idx, platte, err = q.baue()
	if err != nil {
		return nil, nil, err
	}
	idx.Stand = stand
	return idx, platte, nil
}

// leseStandRoh liest die Stand-Datei, nur wenn sie eine reguläre Datei und
// klein genug ist. Fehlt sie oder ist sie es nicht → nil.
func (q *Quelle) leseStandRoh() []byte {
	fi, err := q.root.Lstat(StandDatei)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxStandBytes {
		return nil
	}
	b, err := q.leseRegulaer(StandDatei, fi, maxStandBytes)
	if err != nil {
		return nil
	}
	return b
}

// parseStand übernimmt nur kurze, druckbare Werte — der Inhalt kommt aus
// einer Datei im Repo und landet in der Oberfläche.
func parseStand(roh []byte) *Stand {
	if roh == nil {
		return nil
	}
	var s Stand
	if json.Unmarshal(roh, &s) != nil {
		return nil
	}
	s.Commit = kurzerText(s.Commit, 64)
	s.Datum = kurzerText(s.Datum, 40)
	if s.Commit == "" && s.Datum == "" {
		return nil
	}
	return &s
}

func kurzerText(s string, max int) string {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return ""
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return ""
		}
	}
	return s
}

// baue läuft den Ordner ab. fs.WalkDir folgt keinen Symlinks: ein Eintrag
// vom Typ Symlink ist weder Verzeichnis noch reguläre Datei und fällt
// unten durch beide Prüfungen.
func (q *Quelle) baue() (*Index, map[string]string, error) {
	idx := &Index{Seiten: []Seite{}}
	platte := map[string]string{}
	fsys := q.root.FS()
	eintraege := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if eintraege++; eintraege > eintraegeGrenze {
			idx.Gekappt = true
			return fs.SkipAll
		}
		if err != nil {
			if p == "." {
				return err
			}
			// Ein unlesbares Unterverzeichnis kostet nur dieses.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == "." {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || !utf8.ValidString(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.Count(p, "/")+1 > MaxTiefe {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.EqualFold(path.Ext(name), ".md") {
			return nil
		}
		nfc := norm.NFC.String(p)
		if _, doppelt := platte[nfc]; doppelt {
			// Zwei Namen, die nur in der Normalform abweichen: der erste gilt.
			return nil
		}
		if len(idx.Seiten) >= MaxDateien {
			idx.Gekappt = true
			return fs.SkipAll
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		platte[nfc] = p
		idx.Seiten = append(idx.Seiten, Seite{
			Pfad:    nfc,
			Titel:   q.titel(p, fi),
			Groesse: fi.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("wissen: ordner nicht lesbar: %w", err)
	}
	sort.Slice(idx.Seiten, func(i, j int) bool { return idx.Seiten[i].Pfad < idx.Seiten[j].Pfad })
	return idx, platte, nil
}

// titel nimmt die erste Überschrift `# …` aus dem Anfang der Seite, sonst
// den Dateinamen ohne Endung.
func (q *Quelle) titel(p string, fi fs.FileInfo) string {
	fallback := norm.NFC.String(strings.TrimSuffix(path.Base(p), path.Ext(p)))
	b, err := q.leseRegulaer(p, fi, titelLeseBytes)
	if err != nil && !errors.Is(err, ErrZuGross) {
		return fallback
	}
	for _, zeile := range strings.Split(string(b), "\n") {
		zeile = strings.TrimSpace(zeile)
		if strings.HasPrefix(zeile, "# ") {
			if t := kurzerText(norm.NFC.String(strings.TrimSpace(zeile[2:])), 200); t != "" {
				return t
			}
			break
		}
	}
	return fallback
}

// leseRegulaer öffnet p über die Root und liest höchstens max Bytes. lst ist
// die Lstat-Angabe von p; die geöffnete Datei muss dieselbe sein. Liefert
// ErrZuGross samt den ersten max Bytes, wenn die Datei grösser ist.
func (q *Quelle) leseRegulaer(p string, lst fs.FileInfo, max int64) ([]byte, error) {
	f, err := q.root.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// os.Root folgt Symlinks innerhalb des Ordners. Dass hier eine reguläre
	// Datei offen ist UND es dieselbe ist, die Lstat ohne Folgen sah, schliesst
	// einen zwischendurch eingetauschten Link aus.
	if !fi.Mode().IsRegular() || !os.SameFile(fi, lst) {
		return nil, ErrVeraendert
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return b[:max], ErrZuGross
	}
	return b, nil
}

// Seite liefert den Inhalt der Seite pfad (NFC- oder Plattenform).
func (q *Quelle) Seite(pfad string) ([]byte, Seite, error) {
	idx, platte, err := q.aktuell()
	if err != nil {
		return nil, Seite{}, err
	}
	if !utf8.ValidString(pfad) {
		return nil, Seite{}, ErrNichtGefunden
	}
	nfc := norm.NFC.String(pfad)
	p, ok := platte[nfc]
	if !ok {
		return nil, Seite{}, ErrNichtGefunden
	}
	var meta Seite
	i := sort.Search(len(idx.Seiten), func(i int) bool { return idx.Seiten[i].Pfad >= nfc })
	if i < len(idx.Seiten) && idx.Seiten[i].Pfad == nfc {
		meta = idx.Seiten[i]
	}
	b, err := q.leseSeite(p)
	if err != nil {
		return nil, meta, err
	}
	return b, meta, nil
}

// leseSeite prüft jeden Pfadteil ohne Symlinks zu folgen und liest dann.
// Der Index wurde ohne Symlinks gebaut; diese zweite Prüfung gilt dem Fall,
// dass der Pull-Prozess den Ordner seitdem verändert hat.
func (q *Quelle) leseSeite(p string) ([]byte, error) {
	teile := strings.Split(p, "/")
	for i := 1; i < len(teile); i++ {
		fi, err := q.root.Lstat(strings.Join(teile[:i], "/"))
		if err != nil {
			return nil, ErrNichtGefunden
		}
		if !fi.IsDir() {
			return nil, ErrVeraendert
		}
	}
	lst, err := q.root.Lstat(p)
	if err != nil {
		return nil, ErrNichtGefunden
	}
	// Vorfilter, damit die Grössenprüfung die Datei selbst misst und nicht
	// einen Link. Tragend gegen einen eingetauschten Link ist der Vergleich
	// in leseRegulaer.
	if !lst.Mode().IsRegular() {
		return nil, ErrVeraendert
	}
	if lst.Size() > MaxSeitenBytes {
		return nil, ErrZuGross
	}
	b, err := q.leseRegulaer(p, lst, MaxSeitenBytes)
	if err != nil {
		if errors.Is(err, ErrZuGross) || errors.Is(err, ErrVeraendert) {
			return nil, err
		}
		return nil, ErrNichtGefunden
	}
	return b, nil
}
