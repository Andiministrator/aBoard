// Package boardbackup implementiert pro-Board-Backup als User-Feature
// (Karte #303, ADR-0098).
//
// Abgrenzung zu server/backup: Das Schwester-Paket server/backup erstellt
// server-WEITE Disaster-Recovery-Archive (SQLite-VACUUM-INTO + alle
// Attachments + DB-Schema). Dieses Paket erstellt pro-Board-User-Backups
// (JSON-Export des Board-State + Attachments dieses Boards). Beide leben
// nebenbei: das eine ist Server-Admin-Werkzeug (offline-Tool), das andere
// ein In-App-Feature für Board-Owner mit Auto-Rotation und Restore-Pfaden
// (overwrite/as-new).
//
// Archiv-Format (.tar.gz):
//
//	meta.json                        — Schema-Version, ts, Board-ID, SHA-256, ...
//	board.json                       — Board-Projektion (state_json aus SQLite)
//	base.json                        — identisch zu board.json (Migrations-Reserve)
//	inbox/.gitkeep                   — leeres Inbox-Verzeichnis (Format-Konsistenz mit ADR-0067)
//	attachments/<cardId>/<attId>__<filename>
//	                                 — Attachments des Boards, Struktur wie auf Disk
//
// SHA-256-Schema: Der Hash wird über ALLE Files außer meta.json gerechnet
// (nicht über die tar-Bytes vor gzip — meta.json ist Teil des tars, also
// Henne/Ei). Restore liest meta.json, berechnet den Hash über den Rest neu
// und vergleicht — Drift wird abgelehnt.
package boardbackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// SchemaVersion ist die Format-Version dieses Archivs (nicht die SQLite-
// PRAGMA-user_version). Bumpen wenn das tar-Layout sich ändert.
const SchemaVersion = 1

// Meta wird als meta.json ins Archiv eingebettet.
type Meta struct {
	SchemaVersion   int    `json:"schema_version"`
	CreatedAt       string `json:"created_at"` // RFC3339 UTC
	BoardID         string `json:"board_id"`
	BoardTitle      string `json:"board_title"`
	CardCount       int    `json:"card_count"`       // best-effort, parsed from state_json
	UserCount       int    `json:"user_count"`       // ACL-Einträge des Boards
	AttachmentCount int    `json:"attachment_count"` // physisch im Archiv enthaltene Anhänge
	SHA256          string `json:"sha256"`           // hex über ALLES außer meta.json
	AboardVersion   string `json:"aboard_version"`   // leer wenn unbekannt
	// SchleuseGesperrt: Board-Sperre der Ausgangsschleuse (ADR-0134 §2,
	// F-1583). Die Rückspielung als neues Board übernimmt sie.
	SchleuseGesperrt bool `json:"schleuse_gesperrt,omitempty"`
}

// Options bündelt alle Backup-Parameter.
type Options struct {
	// BoardID ist die Board-UUID (Pflicht).
	BoardID string
	// Boards liest den Board-State (Pflicht).
	Boards *storage.BoardRepo
	// ACL liefert den User-Count für meta.json (Pflicht).
	//
	// F-716 (d): Hier stand „User-Count + ACL-Restore-Set". Ein ACL-Restore
	// existiert nicht — die Einträge landen NICHT im Archiv, und
	// `writeContentEntries` nahm sie folgerichtig als ungenutzten Parameter
	// entgegen. Wer sich auf den alten Satz verliess, hätte ein Backup für
	// vollständig gehalten, das die Zugriffsrechte nicht enthält.
	ACL *storage.AclRepo
	// AttachmentsBaseDir ist der attachments-Root (z. B. /data/attachments).
	// Backup-relevant ist nur <Root>/<BoardID>/.
	AttachmentsBaseDir string
	// Out ist die Ziel-Datei (z. B. boardbackup-<id>-<ts>-<sha>.tar.gz).
	Out string
	// AboardVersion wird in meta.json eingetragen; leer = unbekannt.
	AboardVersion string
	// Crypter schaltet Encryption-at-Rest ein (#336, ADR-0098 §10). nil =
	// unverschlüsselt schreiben. Ist er gesetzt, bekommt die Zieldatei
	// zusätzlich die Endung `.enc` — der tatsächliche Pfad steht in
	// Result.Path, weil der Aufrufer sonst am falschen Namen umbenennt.
	Crypter *Crypter
}

// Result beschreibt das fertige Archiv.
type Result struct {
	Filename string
	Size     int64
	SHA256   string
	TS       string // RFC3339 UTC
	// Path ist der tatsächlich geschriebene Pfad. Er weicht von Options.Out
	// ab, wenn verschlüsselt wurde (dann + EncSuffix).
	Path string
	// Encrypted sagt, ob die Datei verschlüsselt ist.
	Encrypted bool
}

// BackupBoard schreibt ein Backup-Archiv für genau ein Board.
// Stream-orientiert: tar+gzip schreiben direkt in die Out-Datei. Der SHA-256
// läuft parallel über die roh-tar-Bytes der Inhalte (vor meta.json).
//
// Implementations-Hinweis: Wir bauen das Archiv in zwei Pässen — zuerst
// werden alle Inhalt-Files (board.json, base.json, inbox/, attachments/) in
// einen MultiWriter aus tar-Writer + sha256-Hash geschrieben; danach kennen
// wir den Hash, schreiben meta.json an den Anfang eines zweiten Archivs und
// hängen die fertige inhaltliche Archiv-Bytes an. Vereinfachte Variante: wir
// puffern die NON-meta-tar-Einträge zuerst in eine Tmp-Datei, berechnen den
// Hash und kopieren sie dann in die Ziel-Datei nach dem meta.json-Header.
//
// Memory-Profil: Der Tmp-File-Ansatz ist O(Disk), nicht O(RAM) — auch bei
// 10 MB Attachments läuft die Allocation konstant (Read-Buffer 32 KB).
func BackupBoard(ctx context.Context, opts Options) (*Result, error) {
	if opts.BoardID == "" {
		return nil, errors.New("boardbackup: BoardID ist leer")
	}
	if opts.Boards == nil || opts.ACL == nil {
		return nil, errors.New("boardbackup: Boards/ACL Repos sind nil")
	}
	if opts.Out == "" {
		return nil, errors.New("boardbackup: Out ist leer")
	}

	// 1. Board-State laden.
	b, err := opts.Boards.Get(ctx, opts.BoardID)
	if err != nil {
		return nil, fmt.Errorf("boardbackup: Board %q laden: %w", opts.BoardID, err)
	}

	// 2. ACL-Einträge laden (User-Count + Restore-Set).
	aclEntries, err := opts.ACL.ListByBoard(ctx, opts.BoardID)
	if err != nil {
		return nil, fmt.Errorf("boardbackup: ACL laden: %w", err)
	}

	schleuseGesperrt, err := opts.Boards.SchleuseGesperrt(ctx, opts.BoardID)
	if err != nil {
		return nil, fmt.Errorf("boardbackup: Schleusen-Sperre lesen: %w", err)
	}

	// 3. Karten-Count parsen (best-effort — bei Parse-Fehler 0, kein Abbruch).
	cardCount := countCardsInState(b.StateJSON)

	// 4. Inhalts-Archiv in Tmp-File schreiben + SHA-256 mitlaufen lassen.
	tmpContent, err := os.CreateTemp("", "boardbackup-content-*.tar")
	if err != nil {
		return nil, fmt.Errorf("boardbackup: tmpfile: %w", err)
	}
	tmpPath := tmpContent.Name()
	defer os.Remove(tmpPath)

	h := sha256.New()
	mw := io.MultiWriter(tmpContent, h)
	tw := tar.NewWriter(mw)

	attCount, err := writeContentEntries(tw, b, opts.AttachmentsBaseDir, opts.BoardID)
	if err != nil {
		_ = tw.Close()
		_ = tmpContent.Close()
		return nil, fmt.Errorf("boardbackup: Inhalt schreiben: %w", err)
	}
	if err := tw.Close(); err != nil {
		_ = tmpContent.Close()
		return nil, fmt.Errorf("boardbackup: tar-content close: %w", err)
	}
	if err := tmpContent.Close(); err != nil {
		return nil, fmt.Errorf("boardbackup: tmp close: %w", err)
	}

	sha := hex.EncodeToString(h.Sum(nil))

	// 5. meta.json bauen.
	meta := Meta{
		SchemaVersion:   SchemaVersion,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		BoardID:         opts.BoardID,
		BoardTitle:      b.Title,
		CardCount:       cardCount,
		UserCount:       personenZahl(aclEntries),
		AttachmentCount: attCount,
		SHA256:          sha,
		AboardVersion:   opts.AboardVersion,

		SchleuseGesperrt: schleuseGesperrt,
	}

	// 6. Ziel-Archiv: meta.json + Inhalts-tar zusammenkleben in einem
	// gemeinsamen gzip-Stream. Trick: gzip erlaubt das Concat-Pattern nicht
	// direkt für tar-Pakete — wir schreiben stattdessen ein zweites tar in
	// denselben gzip-Strom und packen am Ende beide Streams ineinander.
	//
	// Sauberer Weg: ein einziger gzip+tar-Stream — meta.json wird als erstes
	// Entry geschrieben, dann werden die rohen tar-Headers+Bytes aus tmpContent
	// kopiert (das geht, weil tar-Streams konkatenierbar sind, solange wir
	// nicht zwei tw.Close()-EOF-Marker schreiben — der zweite tw schließt
	// am Ende den Stream).
	if err := os.MkdirAll(filepath.Dir(opts.Out), 0o755); err != nil {
		return nil, fmt.Errorf("boardbackup: Zielverzeichnis: %w", err)
	}
	outPath := opts.Out
	if opts.Crypter != nil {
		outPath += EncSuffix
	}
	outFile, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("boardbackup: out create: %w", err)
	}
	defer outFile.Close()

	// Schichtung: tar → gzip → [Verschlüsselung] → Datei. Die Verschlüsselung
	// liegt AUSSEN, damit meta.json und der SHA-256 unverändert über den
	// KLARTEXT-Strom laufen (ADR-0098 §2). Der Restore-Pfad entschlüsselt
	// deshalb vor der Validierung, nicht danach — sonst könnte der Schlüssel
	// allein die Integrität nicht mehr belegen.
	var sink io.Writer = outFile
	var encCloser io.WriteCloser
	if opts.Crypter != nil {
		ew, err := opts.Crypter.EncryptWriter(outFile)
		if err != nil {
			_ = os.Remove(outPath)
			return nil, fmt.Errorf("boardbackup: verschlüsseln: %w", err)
		}
		encCloser = ew
		sink = ew
	}

	gw := gzip.NewWriter(sink)
	finalTW := tar.NewWriter(gw)

	// meta.json als erstes Entry.
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		/* K7 (Sprint-58-Kritiker): Dieser Pfad liess als einziger von acht
		   eine halbe Datei liegen — kein Close, kein Remove. Heute kaum
		   erreichbar (Meta besteht aus String/Int), aber die Asymmetrie wird
		   beim nächsten Feld real, und was liegen bleibt ist genau das
		   `-pending`-Trümmerstück aus K2. */
		_ = finalTW.Close()
		if encCloser != nil {
			_ = encCloser.Close()
		}
		_ = gw.Close()
		_ = os.Remove(outPath)
		return nil, fmt.Errorf("boardbackup: meta marshal: %w", err)
	}
	if err := writeTarFile(finalTW, "meta.json", metaBytes); err != nil {
		_ = finalTW.Close()
		if encCloser != nil {
			_ = encCloser.Close()
		}
		_ = gw.Close()
		_ = os.Remove(outPath)
		return nil, err
	}

	// Inhalts-tar einlesen und Entry für Entry in das finale tar kopieren.
	in, err := os.Open(tmpPath)
	if err != nil {
		_ = finalTW.Close()
		_ = gw.Close()
		_ = os.Remove(outPath)
		return nil, fmt.Errorf("boardbackup: tmp open: %w", err)
	}
	defer in.Close()
	if err := copyTarEntries(finalTW, in); err != nil {
		_ = finalTW.Close()
		_ = gw.Close()
		_ = os.Remove(outPath)
		return nil, fmt.Errorf("boardbackup: tmp copy: %w", err)
	}

	if err := finalTW.Close(); err != nil {
		_ = gw.Close()
		_ = os.Remove(outPath)
		return nil, fmt.Errorf("boardbackup: final tar close: %w", err)
	}
	if err := gw.Close(); err != nil {
		_ = os.Remove(outPath)
		return nil, fmt.Errorf("boardbackup: gzip close: %w", err)
	}
	// Der Verschlüsselungs-Writer MUSS nach gzip geschlossen werden: erst sein
	// Close schreibt den Abschlussblock, und der muss die letzten gzip-Bytes
	// noch enthalten.
	if encCloser != nil {
		if err := encCloser.Close(); err != nil {
			_ = os.Remove(outPath)
			return nil, fmt.Errorf("boardbackup: verschlüsseln abschliessen: %w", err)
		}
	}

	fi, err := os.Stat(outPath)
	if err != nil {
		_ = os.Remove(outPath) /* K7: nichts Halbfertiges zurücklassen */
		return nil, fmt.Errorf("boardbackup: stat: %w", err)
	}
	return &Result{
		Filename:  filepath.Base(outPath),
		Size:      fi.Size(),
		SHA256:    sha,
		TS:        meta.CreatedAt,
		Path:      outPath,
		Encrypted: opts.Crypter != nil,
	}, nil
}

// writeContentEntries schreibt board.json, base.json, inbox/.gitkeep und
// attachments/ in den Inhalts-tar. Liefert die Anzahl physisch geschriebener
// Attachment-Files zurück.
func writeContentEntries(tw *tar.Writer, b *storage.Board, attBaseDir, boardID string) (int, error) {
	// board.json — exakt der state_json-Blob aus SQLite.
	if err := writeTarFile(tw, "board.json", b.StateJSON); err != nil {
		return 0, err
	}
	// base.json — identische Kopie (Migrations-Reserve, ADR-0098).
	if err := writeTarFile(tw, "base.json", b.StateJSON); err != nil {
		return 0, err
	}
	// inbox/.gitkeep — leeres Verzeichnis-Marker (ADR-0067-Format-Konsistenz).
	if err := writeTarDir(tw, "inbox"); err != nil {
		return 0, err
	}
	if err := writeTarFile(tw, "inbox/.gitkeep", []byte{}); err != nil {
		return 0, err
	}

	// attachments/ — falls vorhanden, Struktur board/<cardId>/<attId>__<filename>.
	attCount := 0
	if attBaseDir != "" {
		boardAttDir := filepath.Join(attBaseDir, boardID)
		if fi, err := os.Stat(boardAttDir); err == nil && fi.IsDir() {
			if err := writeTarDir(tw, "attachments"); err != nil {
				return attCount, err
			}
			n, err := addAttachmentsRecursive(tw, boardAttDir, "attachments")
			if err != nil {
				return attCount, err
			}
			attCount = n
		}
	}
	return attCount, nil
}

// addAttachmentsRecursive geht den Attachments-Baum eines Boards durch und
// fügt Verzeichnisse + Dateien dem tar hinzu. Liefert die Anzahl Dateien.
func addAttachmentsRecursive(tw *tar.Writer, srcDir, tarBase string) (int, error) {
	count := 0
	err := filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == srcDir {
			return nil // Root-Verzeichnis selbst schon angelegt
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		tarPath := tarBase + "/" + filepath.ToSlash(rel)
		if fi.IsDir() {
			return writeTarDir(tw, tarPath)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		hdr := &tar.Header{
			Name:    tarPath,
			Mode:    0o644,
			Size:    fi.Size(),
			ModTime: fi.ModTime().UTC(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

// tarFesteZeit ist die Änderungszeit der selbst erzeugten tar-Einträge
// (board.json, Verzeichnisse, ...). F-2831: Mit time.Now() lief der SHA-256
// (Hash über die tar-Bytes samt Header-mtime, auf Sekunden gerundet) über die
// Uhrzeit — zwei Sicherungen desselben Zustands, die eine Sekundengrenze
// trennte, bekamen verschiedene Hashes (Wackeltest ~1 %). Der Zeitpunkt der
// Sicherung steht in meta.json (created_at), außerhalb des Hashes; der Restore
// liest die tar-mtime nirgends. Für die selbst erzeugten Einträge gilt damit:
// gleicher Inhalt = gleicher SHA. Anhänge behalten ihre Datei-mtime (writeTarFile
// mit fi.ModTime()); wird ein Anhang nur neu geschrieben oder berührt, ändert
// sich der SHA trotz gleichem Inhalt (F-2963).
var tarFesteZeit = time.Unix(0, 0).UTC()

// writeTarFile schreibt einen einzelnen Datei-Eintrag.
func writeTarFile(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: tarFesteZeit,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if len(data) > 0 {
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("tar write %s: %w", name, err)
		}
	}
	return nil
}

// writeTarDir schreibt einen Directory-Header (trailing-Slash).
func writeTarDir(tw *tar.Writer, name string) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeDir,
		Name:     strings.TrimSuffix(name, "/") + "/",
		Mode:     0o755,
		ModTime:  tarFesteZeit,
	}
	return tw.WriteHeader(hdr)
}

// copyTarEntries liest ein bestehendes tar-Stream und kopiert ALLE Entries
// (außer dem EOF-Marker) ins Ziel-tar. Wird benutzt um den Inhalts-tar nach
// dem meta.json-Entry anzuhängen.
func copyTarEntries(dst *tar.Writer, src io.Reader) error {
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		if err := dst.WriteHeader(hdr); err != nil {
			return fmt.Errorf("tar copy header %s: %w", hdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(dst, tr); err != nil {
				return fmt.Errorf("tar copy body %s: %w", hdr.Name, err)
			}
		}
	}
}

// countCardsInState parst den state_json-Blob und zählt Karten in allen
// Sprints. Best-effort: bei Parse-Fehler 0, kein Abbruch.
func countCardsInState(raw []byte) int {
	var env struct {
		Board struct {
			Sprints []struct {
				Cards []json.RawMessage `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0
	}
	n := 0
	for _, s := range env.Board.Sprints {
		n += len(s.Cards)
	}
	return n
}

// FormatFilename erzeugt einen normalisierten Dateinamen für ein Backup-Archiv:
//
//	boardbackup-<boardID>-<YYYYMMDD-HHMMSS>-<sha-short>.tar.gz
//
// sha-short = erste 8 Hex-Zeichen. Dient nur der Disk-Lesbarkeit; die
// Echte-SHA-Validierung läuft über meta.json.
func FormatFilename(boardID string, ts time.Time, sha string) string {
	short := sha
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("boardbackup-%s-%s-%s.tar.gz",
		boardID, ts.UTC().Format("20060102-150405"), short)
}

// F-716 (d): Hier stand `HashEntries` — ein exportierter Hash-Helfer, dessen
// Doku-Kommentar behauptete, der Restore-Pfad benutze ihn. Tut er nicht: der
// Restore rehasht über `rehashTarEntries` (Entry für Entry, spiegelt die
// Schreibseite). `HashEntries` hashte stattdessen die rohen Bytes und hatte
// seit der ersten Fassung keinen einzigen Aufrufer. Ein toter Helfer mit
// falscher Beschreibung ist eine Falle für den nächsten Leser, keine Reserve.

// rehashTarEntries liest ein tar-Stream Entry für Entry und schreibt jedes
// Entry (Header + Body) als rohe tar-Bytes in einen sha256-Hash. Spiegelt
// die Schreibseite (writeContentEntries → tar.Writer → MultiWriter inkl.
// sha256). Wird vom Restore-Pfad benutzt, um den meta.sha256 zu validieren.
func rehashTarEntries(src io.Reader) (string, error) {
	h := sha256.New()
	tw := tar.NewWriter(h)
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return "", err
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil {
				return "", err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	// tw.Close() schreibt den EOF-Marker mit; das beim Hashen passt nicht
	// 1:1 zu writeContentEntries (das hat seinen eigenen tw.Close mit
	// gleichem EOF). Damit ist die Reproduzierbarkeit gegeben — beide Seiten
	// schreiben dieselbe Entry-Folge + dieselben EOF-Bytes.
	return hex.EncodeToString(h.Sum(nil)), nil
}

// F-716 (d): Ebenfalls entfernt — `sortedAttachmentFilenames` („nützlich für
// Test-Diff", von keinem Test genutzt) und `hashSumString` (ein Einzeiler um
// `hex.EncodeToString`, ohne Aufrufer).

// personenZahl zählt die ACL-Einträge, die Personen sind. Domain-Freigaben
// ("@<domain>", ADR-0132) sind keine Nutzer und zählen nicht mit (F-1517).
func personenZahl(eintraege []storage.ACLEntry) int {
	n := 0
	for _, e := range eintraege {
		if !storage.IstDomainSubjekt(e.UserEmail) {
			n++
		}
	}
	return n
}
