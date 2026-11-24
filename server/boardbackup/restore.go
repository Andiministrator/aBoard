package boardbackup

// restore.go — Backup-Archiv zurückspielen (#303, ADR-0098).
//
// Zwei Modi:
//   - overwrite: Ziel-Board-State wird ersetzt (transaktional, alte Atts
//     gelöscht, neue aus Archiv ausgepackt). ACL bleibt erhalten — der
//     Restore-User muss vorher Board-Owner gewesen sein (Caller-ACL-Check).
//   - as-new:    Neue Board-UUID, Inhalt aus Archiv, Restore-User als Owner.
//                Karten-/Sprint-UUIDs bleiben (global eindeutig).
//
// SHA-256-Validierung: Pflicht. meta.json::sha256 wird mit einem Rehash
// über alle non-meta-Entries verglichen. Drift → Fehler.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
)

// ErrBackupCorrupted signalisiert SHA-Mismatch oder strukturelle Schäden.
var ErrBackupCorrupted = errors.New("Backup-Datei beschädigt oder manipuliert")

// ErrFindingVerstoss signalisiert Finding-Karten im Archiv, die gegen die
// Regeln der Ops verstoßen (doppelte nr, Wortschatz; F-1577). Es ist nichts
// geschrieben.
var ErrFindingVerstoss = errors.New("Archiv enthält ungültige Finding-Karten")

// ErrSchemaTooOld signalisiert ein zu altes Archiv-Schema.
var ErrSchemaTooOld = errors.New("Backup zu alt, manuelle Migration nötig")

// ErrRestoreForbidden meldet, dass der Aufrufer das Quell-Board des Archivs
// nicht sehen darf. Eigener Sentinel, damit der Handler 403 statt 500
// antworten kann — und damit die Ablehnung nicht wie ein Defekt aussieht.
var ErrRestoreForbidden = errors.New("Wiederherstellung nicht erlaubt: kein Zugriff auf das Quell-Board dieses Backups")

// ErrRestoreAufKopie: Das Ziel des Überschreibens ist eine externe Kopie des
// Board-Paars (ADR-0141 §7) — sie wird nie überschrieben; als neues Board
// zurückspielen geht.
var ErrRestoreAufKopie = errors.New("Wiederherstellung nicht erlaubt: das Ziel ist eine externe Kopie eines internen Boards; als neues Board zurückspielen")

// ErrSchemaTooNew signalisiert ein zu neues Archiv-Schema.
var ErrSchemaTooNew = errors.New("Backup von neuerer Server-Version, bitte aBoard updaten")

// RestoreMode definiert den Verhaltens-Schalter beim Einspielen.
type RestoreMode string

const (
	RestoreOverwrite RestoreMode = "overwrite"
	RestoreAsNew     RestoreMode = "as-new"
)

// RestoreOptions bündelt die Restore-Parameter.
type RestoreOptions struct {
	ArchivePath string
	// ArchiveReader ist ein optionaler Stream-Reader. Wenn gesetzt, wird er
	// statt ArchivePath benutzt (für direkte Upload-Verarbeitung ohne tmp).
	ArchiveReader io.Reader
	// Mode entscheidet zwischen overwrite und as-new.
	Mode RestoreMode
	// TargetBoardID ist Pflicht bei Mode=overwrite. Bei as-new ignoriert.
	TargetBoardID string
	// NewBoardID wird im as-new-Modus benutzt; leer = neue UUID erzeugen.
	NewBoardID string
	// RestoreUserEmail ist der ausführende User (für ACL-Owner-Eintrag im
	// as-new-Modus). Leer = kein Owner-Set.
	RestoreUserEmail string
	// Boards/ACL/Attachments — Pflicht-Repos.
	Boards             *storage.BoardRepo
	ACL                *storage.AclRepo
	AttachmentsBaseDir string
	// AboardVersion wird im Activity-Log mitgeschrieben; leer = unbekannt.
	AboardVersion string
	// MaxUncompressedBytes begrenzt die ENTPACKTE Grösse (S2). 0 → Default.
	//
	// `http.MaxBytesReader` im Handler begrenzt nur den KOMPRIMIERTEN
	// Upload. Gemessen (Sprint-58-Kritiker): gzip erreicht bei Nullen ein
	// Verhältnis von 1029:1 — ein 10-MB-Upload expandiert also auf rund
	// 10 GB Heap, weil `extractArchive` jeden Eintrag komplett in den
	// Speicher liest (und für den Rehash ein zweites Mal). Ohne Grenze
	// genügt ein Request eines beliebigen angemeldeten Nutzers, um den
	// Container per OOM zu beenden und damit ALLE Boards offline zu nehmen.
	MaxUncompressedBytes int64
	// AuthorizeSource wird — falls gesetzt — aufgerufen, SOBALD `meta.json`
	// gelesen ist und BEVOR irgendetwas geschrieben wird. Der Aufrufer
	// entscheidet damit, ob der Nutzer das QUELL-Board dieses Archivs
	// überhaupt sehen darf.
	//
	// WARUM das hier und nicht im Handler liegt (Sprint-58-Kritiker, S1):
	// Die Board-ID steht in `meta.json`, also INNERHALB des Archivs — bei
	// einem verschlüsselten Archiv hinter der Entschlüsselung. Ein Handler
	// kann sie ohne dieses Modul nicht kennen. Ohne den Haken war der
	// `as-new`-Modus ein Entschlüsselungs-Orakel: Jeder angemeldete Nutzer
	// konnte ein fremdes `.tar.gz.enc` hochladen, der Server entschlüsselte
	// es mit dem Master-Key, legte ein neues Board an und trug den
	// Hochladenden als Eigentümer ein. Genau der Schutz, für den #336
	// gebaut wurde, wäre über einen REST-Aufruf zu umgehen gewesen.
	//
	// nil = keine Prüfung (CLI-/Test-Pfad, wo es keine Identität gibt).
	AuthorizeSource func(sourceBoardID string) error
	// Crypter entschlüsselt verschlüsselte Archive (#336). nil ist zulässig
	// und der Normalfall für unverschlüsselte Backups; trifft der Restore
	// dann auf ein verschlüsseltes Archiv, endet er mit ErrEncryptedNoKey.
	//
	// Wichtig für den Aufrufer: Dieser Wert hängt NICHT an
	// `backup_board.encryption_enabled`. Ein Archiv, das entstand, als die
	// Verschlüsselung eingeschaltet war, muss auch dann restaurierbar
	// bleiben, wenn der Schalter später wieder aus ist — der Crypter gehört
	// also immer gesetzt, sobald ein Master-Key existiert.
	Crypter *Crypter
	// FindingsPruefungAus ist der ausdrückliche Notfall-Schalter (KA-7): Ein
	// Archiv aus der Zeit vor F-1572 kann Finding-Verstöße tragen und ließe
	// sich sonst gar nicht mehr zurückspielen (422, kein Rettungsweg). Mit
	// dem Schalter wird trotzdem geschrieben; die Verstöße stehen im Log und
	// in RestoreResult.FindingWarnungen. Standard false — dann bleibt es bei
	// ErrFindingVerstoss. Wer ihn setzen darf, entscheidet der Aufrufer.
	FindingsPruefungAus bool
}

// RestoreResult beschreibt das Ergebnis eines erfolgreichen Restores.
type RestoreResult struct {
	BoardID    string
	Mode       RestoreMode
	RestoredAt string // RFC3339 UTC
	// FindingWarnungen: bei FindingsPruefungAus die übergangenen Verstöße.
	FindingWarnungen []string
}

// pruefeFindingsOderWarne prüft wie der Ganz-Board-PUT (F-1577). Verstöße
// sind ein Fehler (ErrFindingVerstoss) — außer FindingsPruefungAus: dann
// kommen sie als Warnung zurück und ins Log (KA-7).
func pruefeFindingsOderWarne(opts RestoreOptions, alt, neu []byte) ([]string, error) {
	v := storage.NeueFindingVerstoesse(alt, neu)
	if len(v) == 0 {
		return nil, nil
	}
	if !opts.FindingsPruefungAus {
		return nil, fmt.Errorf("%w: %s", ErrFindingVerstoss, storage.FindingVerstossMeldung(v))
	}
	log.Printf("aboard-server: Rückspielung mit abgeschalteter Finding-Prüfung — %d Verstoß/Verstöße übergangen: %s",
		len(v), storage.FindingVerstossMeldung(v))
	return v, nil
}

// RestoreBackup spielt ein Backup zurück. Liefert das angelegte/ersetzte
// Ziel-Board (BoardID + Modus).
func RestoreBackup(ctx context.Context, opts RestoreOptions) (*RestoreResult, error) {
	if opts.Mode != RestoreOverwrite && opts.Mode != RestoreAsNew {
		return nil, fmt.Errorf("restore: ungültiger Modus %q", opts.Mode)
	}
	if opts.Mode == RestoreOverwrite && opts.TargetBoardID == "" {
		return nil, errors.New("restore: target_board_id fehlt für overwrite-Modus")
	}
	if opts.Boards == nil || opts.ACL == nil {
		return nil, errors.New("restore: Boards/ACL Repos sind nil")
	}

	// 1. Archiv in Tmp-Verzeichnis entpacken (gzip → tar → meta.json + Rest).
	tmpDir, err := os.MkdirTemp("", "boardbackup-restore-*")
	if err != nil {
		return nil, fmt.Errorf("restore: tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	maxEntpackt := opts.MaxUncompressedBytes
	if maxEntpackt <= 0 {
		maxEntpackt = DefaultMaxUncompressedBytes
	}
	meta, boardJSON, baseJSON, attachments, contentTarBuf, err := extractArchive(
		opts.ArchivePath, opts.ArchiveReader, opts.Crypter, maxEntpackt)
	if err != nil {
		return nil, err
	}
	_ = baseJSON // base.json wird derzeit nicht separat verwendet (MVP)

	// 2. Schema-Version validieren.
	if meta.SchemaVersion < SchemaVersion {
		return nil, fmt.Errorf("%w: archive=%d, server=%d", ErrSchemaTooOld, meta.SchemaVersion, SchemaVersion)
	}
	if meta.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%w: archive=%d, server=%d", ErrSchemaTooNew, meta.SchemaVersion, SchemaVersion)
	}

	// 3. SHA-256 validieren (Rehash über alle non-meta-Entries).
	rehash, err := rehashTarEntries(bytes.NewReader(contentTarBuf))
	if err != nil {
		return nil, fmt.Errorf("restore: rehash: %w", err)
	}
	if rehash != meta.SHA256 {
		return nil, fmt.Errorf("%w (sha-mismatch: meta=%s, computed=%s)", ErrBackupCorrupted, meta.SHA256, rehash)
	}

	// 3b. Quell-Board-Autorisierung — VOR jedem Schreibzugriff und vor dem
	// Anlegen eines neuen Boards. Erst hier möglich, weil `meta.board_id`
	// aus dem (ggf. entschlüsselten) Archiv stammt.
	if opts.AuthorizeSource != nil {
		if err := opts.AuthorizeSource(meta.BoardID); err != nil {
			return nil, err
		}
	}

	// 4. board.json parsen — Board-Envelope.
	var env struct {
		Version string                 `json:"version"`
		Board   map[string]interface{} `json:"board"`
	}
	if err := json.Unmarshal(boardJSON, &env); err != nil {
		return nil, fmt.Errorf("%w (board.json invalid: %v)", ErrBackupCorrupted, err)
	}
	if env.Version == "" || env.Board == nil {
		return nil, fmt.Errorf("%w (board.json fehlt version/board)", ErrBackupCorrupted)
	}

	// 5. Modus-spezifischer Restore.
	var resultBoardID string
	var warnungen []string
	switch opts.Mode {
	case RestoreOverwrite:
		warnungen, err = restoreOverwrite(ctx, opts, env, boardJSON, attachments)
		if err != nil {
			return nil, err
		}
		resultBoardID = opts.TargetBoardID
	case RestoreAsNew:
		var newID string
		newID, warnungen, err = restoreAsNew(ctx, opts, meta, env, boardJSON, attachments)
		if err != nil {
			return nil, err
		}
		resultBoardID = newID
	}

	ts := meta.CreatedAt
	if ts == "" {
		ts = meta.CreatedAt
	}
	return &RestoreResult{
		BoardID:    resultBoardID,
		Mode:       opts.Mode,
		RestoredAt: ts,

		FindingWarnungen: warnungen,
	}, nil
}

// restoreOverwrite ersetzt den Inhalt eines bestehenden Boards.
//
// Transaktion: Wir nutzen BoardRepo.Update (kein If-Match, ifMatch=0) für
// einen Force-Update. Attachments werden nach erfolgreichem DB-Update ersetzt
// — kommt der Att-Schritt schief, ist die DB schon konsistent (kein Rollback
// auf Datei-Ebene, akzeptiert: Worst-Case ist „neuer JSON-State + alte Att-
// Files" und der nächste Backup-Lauf hat trotzdem ein konsistentes Bild).
func restoreOverwrite(
	ctx context.Context,
	opts RestoreOptions,
	env struct {
		Version string                 `json:"version"`
		Board   map[string]interface{} `json:"board"`
	},
	boardJSON []byte,
	attachments map[string][]byte,
) ([]string, error) {
	// Board muss existieren.
	bestehend, err := opts.Boards.Get(ctx, opts.TargetBoardID)
	if err != nil {
		return nil, fmt.Errorf("restore overwrite: ziel-board: %w", err)
	}
	// ADR-0141 §7 (Sitzungs-QA QA-6 d, Karte #826): Eine externe Kopie wird
	// nicht überschrieben — ihr Original liegt innen, der Hinweg baut sie neu
	// auf. Sonst stünden dort Spiegel ohne Marke, und der nächste Hinweg legte
	// Duplikate an. Rückspielen als NEUES Board bleibt erlaubt.
	if kopie, err := opts.Boards.IstKopie(ctx, opts.TargetBoardID); err != nil {
		return nil, fmt.Errorf("restore overwrite: kopplung: %w", err)
	} else if kopie {
		return nil, ErrRestoreAufKopie
	}

	title, _ := env.Board["title"].(string)
	if title == "" {
		title = "Wiederhergestellt"
	}

	// board.id im JSON auf TargetBoardID korrigieren — sonst hat das Frontend
	// nach Reload eine fremde id im state_json (Drift).
	env.Board["id"] = opts.TargetBoardID
	patched, err := json.Marshal(struct {
		Version string                 `json:"version"`
		Board   map[string]interface{} `json:"board"`
	}{Version: env.Version, Board: env.Board})
	if err != nil {
		return nil, fmt.Errorf("restore overwrite: json: %w", err)
	}

	// F-1577: Finding-Karten prüfen wie der Ganz-Board-PUT (F-1572) — abgelehnt
	// wird nur, was gegenüber dem überschriebenen Stand NEU ist. Eine strenge
	// Prüfung gegen nil würde die Rückspielung genau dort sperren, wo sie
	// helfen soll: ein Board, das schon Altlasten trägt, ließe sich nicht mehr
	// aus einem ebenso belasteten Archiv zurückholen. Es wird vor dem
	// Schreiben geprüft, also bleibt bei Verstoß alles unverändert.
	warnungen, err := pruefeFindingsOderWarne(opts, bestehend.StateJSON, patched)
	if err != nil {
		return nil, err
	}

	// F-1685: Die Rückspielung bringt Finding-Karten mit ihrer ursprünglichen
	// Nummer zurück — nicht umnummerieren (storage.MitFindingNummernBehalten).
	if _, err := opts.Boards.Update(storage.MitFindingNummernBehalten(ctx), opts.TargetBoardID, title, env.Version, patched, 0); err != nil {
		return nil, fmt.Errorf("restore overwrite: update: %w", err)
	}

	// Attachments synchronisieren: alte Files entfernen, neue aus Archiv kopieren.
	if opts.AttachmentsBaseDir != "" {
		boardAttDir := filepath.Join(opts.AttachmentsBaseDir, opts.TargetBoardID)
		_ = os.RemoveAll(boardAttDir)
		if err := writeAttachmentsFromArchive(boardAttDir, attachments); err != nil {
			return nil, fmt.Errorf("restore overwrite: attachments: %w", err)
		}
	}
	return warnungen, nil
}

// restoreAsNew legt ein neues Board mit eigener UUID an und kopiert den
// Backup-Inhalt dorthin. Der Restore-User wird Owner (falls Email gesetzt).
func restoreAsNew(
	ctx context.Context,
	opts RestoreOptions,
	meta Meta,
	env struct {
		Version string                 `json:"version"`
		Board   map[string]interface{} `json:"board"`
	},
	boardJSON []byte,
	attachments map[string][]byte,
) (string, []string, error) {
	newID := opts.NewBoardID
	if newID == "" {
		newID = generateBoardUUID()
	}

	originalTitle, _ := env.Board["title"].(string)
	title := originalTitle
	if title == "" {
		title = "Restored Board"
	} else {
		title = title + " (Restore)"
	}

	env.Board["id"] = newID
	env.Board["title"] = title
	patched, err := json.Marshal(struct {
		Version string                 `json:"version"`
		Board   map[string]interface{} `json:"board"`
	}{Version: env.Version, Board: env.Board})
	if err != nil {
		return "", nil, fmt.Errorf("restore as-new: json: %w", err)
	}

	// F-1577: wie der POST — ein neues Board hat keinen Altstand, es gilt alles.
	warnungen, err := pruefeFindingsOderWarne(opts, nil, patched)
	if err != nil {
		return "", nil, err
	}

	if _, err := opts.Boards.Insert(ctx, newID, title, env.Version, patched); err != nil {
		return "", nil, fmt.Errorf("restore as-new: insert: %w", err)
	}

	// F-1583: Die Board-Sperre der Ausgangsschleuse geht mit. Insert kennt die
	// Spalte nicht; ohne diesen Schritt wäre die Rückspielung ein Weg, die
	// Sperre aufzuheben. Quelle: das Archiv (meta.json) UND — falls es das
	// Quell-Board noch gibt — dessen heutiger Stand, damit ein bearbeitetes
	// meta.json die Sperre nicht abstreifen kann.
	gesperrt := meta.SchleuseGesperrt
	if !gesperrt && meta.BoardID != "" {
		if live, err := opts.Boards.SchleuseGesperrt(ctx, meta.BoardID); err == nil {
			gesperrt = live
		}
	}
	if gesperrt {
		wer := opts.RestoreUserEmail
		if wer == "" {
			wer = "restore"
		}
		if err := opts.Boards.SetzeSchleuseSperre(ctx, newID, true, wer); err != nil {
			// Ohne Sperre darf das Board nicht entstehen — wieder löschen.
			_ = opts.Boards.Delete(ctx, newID)
			return "", nil, fmt.Errorf("restore as-new: schleusen-sperre: %w", err)
		}
	}

	// Owner-ACL für ausführenden User.
	if opts.RestoreUserEmail != "" {
		if err := opts.ACL.Set(ctx, newID, opts.RestoreUserEmail, storage.RoleOwner); err != nil {
			// Owner-Set fehlgeschlagen → Board wieder löschen, statt Waise
			// zu hinterlassen.
			_ = opts.Boards.Delete(ctx, newID)
			return "", nil, fmt.Errorf("restore as-new: owner-acl: %w", err)
		}
	}

	// Attachments unter neuer Board-ID auspacken.
	if opts.AttachmentsBaseDir != "" && len(attachments) > 0 {
		boardAttDir := filepath.Join(opts.AttachmentsBaseDir, newID)
		if err := writeAttachmentsFromArchive(boardAttDir, attachments); err != nil {
			return "", nil, fmt.Errorf("restore as-new: attachments: %w", err)
		}
	}
	return newID, warnungen, nil
}

// extractArchive entpackt das gzip+tar-Archiv und liefert:
//   - meta.json deserialisiert
//   - board.json roh
//   - base.json roh
//   - attachments map[relativ-pfad]bytes (z. B. "<cardId>/<attId>__filename")
//   - contentTarBuf: alle non-meta-Entries als gepackter tar-Stream (für Rehash)
func extractArchive(archivePath string, reader io.Reader, crypter *Crypter, maxEntpackt int64) (Meta, []byte, []byte, map[string][]byte, []byte, error) {
	var src io.Reader
	if reader != nil {
		src = reader
	} else {
		f, err := os.Open(archivePath)
		if err != nil {
			return Meta{}, nil, nil, nil, nil, fmt.Errorf("archive open: %w", err)
		}
		defer f.Close()
		src = f
	}
	// #732 (F-1816): Quellfehler merken, um Lesefehler der Eingabe (Platte,
	// Netz) von Formatfehlern des Inhalts zu trennen — nur Letztere sind
	// „beschädigt“.
	quelle := &quellFehlerReader{r: src}
	src = quelle

	// #336: Verschlüsselte Archive erkennen und auspacken, BEVOR gzip/tar
	// drankommen. Erkannt wird am Magic, nicht an der Dateiendung — ein
	// umbenanntes Archiv bleibt lesbar, und eine `.enc`-benannte Datei ohne
	// Magic wird korrekt als unverschlüsselt behandelt statt mit einem
	// irreführenden Schlüsselfehler abgelehnt.
	verschluesselt, src, err := SplitEncrypted(src)
	if err != nil {
		if quelle.err != nil {
			return Meta{}, nil, nil, nil, nil, fmt.Errorf("archive lesen: %w", quelle.err)
		}
		return Meta{}, nil, nil, nil, nil, fmt.Errorf("archive lesen: %w", err)
	}
	if verschluesselt {
		if crypter == nil {
			return Meta{}, nil, nil, nil, nil, ErrEncryptedNoKey
		}
		dr, derr := crypter.DecryptReader(src)
		if derr != nil {
			/* K-3 (Sprint 85): Kopflesefehler der QUELLE nicht als
			   „beschädigt“ ausgeben (DecryptReader bildet jeden Lesefehler auf
			   ErrEncryptedCorrupt ab) — der Quellfehler selbst führt zu 500. */
			if quelle.err != nil {
				return Meta{}, nil, nil, nil, nil, fmt.Errorf("archive lesen: %w", quelle.err)
			}
			return Meta{}, nil, nil, nil, nil, derr
		}
		src = dr
	}

	/* F-716 (c): `src` wird ABSICHTLICH selbst gepuffert, statt gzip einen
	   eigenen bufio anlegen zu lassen. Zwei Gründe:

	   1. `gzip.NewReader` verpackt jeden Reader, der kein `flate.Reader`
	      ist, in einen eigenen bufio — dessen Restinhalt kommt man danach
	      nicht mehr bei. Ein `bufio.Reader` IST ein `flate.Reader`
	      (Read + ReadByte), wird also unverändert durchgereicht.
	   2. Weil `flate` einen `io.ByteReader` bekommt, liest es garantiert
	      nicht über das Ende des Deflate-Stroms hinaus. Nach dem Ende des
	      gzip-Members steht `gepuffert` deshalb exakt auf dem ersten Byte
	      DANACH — und genau das brauchen wir, um angehängten Müll zu sehen. */
	gepuffert := bufio.NewReader(src)
	gr, err := gzip.NewReader(gepuffert)
	if err != nil {
		return Meta{}, nil, nil, nil, nil, formatFehler(quelle, "gzip", err)
	}
	defer gr.Close()
	/* Kein Multistream: Der Leser soll am Ende des ersten gzip-Members
	   halten, statt still einen zweiten anzufangen. Sonst könnte ein
	   angehängter (für sich gültiger) gzip-Strom unbemerkt mitgelesen
	   werden. */
	gr.Multistream(false)

	/* S2: Harte Obergrenze auf den ENTPACKTEN Strom. `io.LimitReader` liefert
	   bei Überschreitung EOF; der tar-Leser meldet das dann als abgeschnittenes
	   Archiv. Damit die Meldung nicht in die Irre führt (ein Nutzer mit einem
	   legitim zu grossen Backup soll nicht „beschädigt" lesen), zählen wir
	   selbst mit und unterscheiden die beiden Fälle. */
	limitiert := &zaehlenderReader{r: io.LimitReader(gr, maxEntpackt+1), max: maxEntpackt}
	tr := tar.NewReader(limitiert)
	var meta Meta
	var boardJSON, baseJSON []byte
	attachments := map[string][]byte{}

	// Wir bauen während des Lesens einen Rehash-tar parallel auf — alle
	// non-meta-Entries werden ein zweites Mal in einen Puffer geschrieben,
	// in derselben Reihenfolge wie sie im Archiv stehen.
	rehashBuf := &bytes.Buffer{}
	rehashTW := tar.NewWriter(rehashBuf)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if limitiert.ueberschritten() {
				return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (entpackt > %d Bytes)",
					ErrBackupTooLarge, maxEntpackt)
			}
			return Meta{}, nil, nil, nil, nil, formatFehler(quelle, "tar read", err)
		}

		// Path-Traversal-Schutz (analog server/backup F-236).
		if strings.Contains(hdr.Name, "..") {
			return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (unsafe path: %s)", ErrBackupCorrupted, hdr.Name)
		}
		if filepath.IsAbs(hdr.Name) || strings.HasPrefix(hdr.Name, "/") || strings.Contains(hdr.Name, `\`) {
			return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (absolute path: %s)", ErrBackupCorrupted, hdr.Name)
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (symlink/hardlink)", ErrBackupCorrupted)
		}

		if hdr.Name == "meta.json" {
			data, err := io.ReadAll(tr)
			if err != nil {
				if limitiert.ueberschritten() {
					return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (entpackt > %d Bytes)",
						ErrBackupTooLarge, maxEntpackt)
				}
				return Meta{}, nil, nil, nil, nil, formatFehler(quelle, "meta read", err)
			}
			if err := json.Unmarshal(data, &meta); err != nil {
				return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (meta.json: %v)", ErrBackupCorrupted, err)
			}
			continue
		}

		// Non-meta-Entry: in Rehash-tar parallel schreiben.
		if err := rehashTW.WriteHeader(hdr); err != nil {
			return Meta{}, nil, nil, nil, nil, fmt.Errorf("rehash header %s: %w", hdr.Name, err)
		}
		var body []byte
		if hdr.Typeflag == tar.TypeReg {
			body, err = io.ReadAll(tr)
			if err != nil {
				if limitiert.ueberschritten() {
					return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (entpackt > %d Bytes)",
						ErrBackupTooLarge, maxEntpackt)
				}
				return Meta{}, nil, nil, nil, nil, formatFehler(quelle, "read body "+hdr.Name, err)
			}
			if _, err := rehashTW.Write(body); err != nil {
				return Meta{}, nil, nil, nil, nil, fmt.Errorf("rehash body %s: %w", hdr.Name, err)
			}
		}

		switch {
		case hdr.Name == "board.json":
			boardJSON = body
		case hdr.Name == "base.json":
			baseJSON = body
		case strings.HasPrefix(hdr.Name, "attachments/"):
			rel := strings.TrimPrefix(hdr.Name, "attachments/")
			if rel == "" {
				continue
			}
			if hdr.Typeflag == tar.TypeReg {
				attachments[rel] = body
			}
		case hdr.Name == "inbox/" || hdr.Name == "inbox/.gitkeep" || strings.HasPrefix(hdr.Name, "inbox/"):
			// Inbox ist im MVP leer — kein State zu rekonstruieren.
		default:
			// Vorwärts-Kompat: unbekannte Entries werden ignoriert (im Rehash
			// trotzdem berücksichtigt, was sie auch beim Build wären).
		}
	}

	if limitiert.ueberschritten() {
		return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (entpackt > %d Bytes)",
			ErrBackupTooLarge, maxEntpackt)
	}

	/* F-716 (c): Angehängte Bytes am Dateiende erkennen.
	   `tar.Reader` hört bei den zwei Null-Blöcken auf und schaut nie
	   weiter; gemessen wurden 4096 Byte Müll an einem 400-KB-Archiv, ohne
	   dass irgendetwas gemeldet hätte. Der SHA-256 aus meta.json deckt nur
	   die tar-EINTRÄGE ab, nicht das, was hinter dem Archiv klebt — die
	   Zusage „das Format erkennt Manipulation" war an dieser Stelle also
	   nicht gedeckt.

	   Zwei Schritte: erst den Rest des gzip-Members leerlesen (tar-Padding),
	   dann prüfen, ob hinter dem Member noch etwas liegt. */
	if _, err := io.Copy(io.Discard, limitiert); err != nil {
		if limitiert.ueberschritten() {
			return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (entpackt > %d Bytes)",
				ErrBackupTooLarge, maxEntpackt)
		}
		return Meta{}, nil, nil, nil, nil, formatFehler(quelle, "Reststrom", err)
	}
	if limitiert.ueberschritten() {
		return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (entpackt > %d Bytes)",
			ErrBackupTooLarge, maxEntpackt)
	}
	if _, perr := gepuffert.Peek(1); perr != io.EOF {
		/* Abgrenzung, damit die Meldung nicht in die Irre führt: Abgelehnt
		   wird auch ein Archiv, das aus MEHREREN gzip-Membern besteht (z. B.
		   mit `pigz` neu komprimiert). Das ist bewusst so — aBoard schreibt
		   immer genau einen Member, und die Alternative wäre, angehängte
		   gültige gzip-Ströme wieder durchzulassen. Wer ein solches Archiv
		   hat, packt es einmal mit `gunzip`/`gzip` um. */
		return Meta{}, nil, nil, nil, nil, fmt.Errorf(
			"%w (Daten hinter dem Archiv-Ende — angehängte Bytes oder mehrteiliger gzip-Strom)",
			ErrBackupCorrupted)
	}

	if err := rehashTW.Close(); err != nil {
		return Meta{}, nil, nil, nil, nil, fmt.Errorf("rehash close: %w", err)
	}

	if boardJSON == nil {
		return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (board.json fehlt)", ErrBackupCorrupted)
	}
	if meta.SchemaVersion == 0 && meta.CreatedAt == "" {
		return Meta{}, nil, nil, nil, nil, fmt.Errorf("%w (meta.json fehlt)", ErrBackupCorrupted)
	}
	return meta, boardJSON, baseJSON, attachments, rehashBuf.Bytes(), nil
}

// DefaultMaxUncompressedBytes ist die Obergrenze für den entpackten Inhalt
// eines Restore-Archivs, wenn der Aufrufer keine setzt: 2 GiB.
//
// Die Zahl ist bewusst grosszügig — sie soll ein OOM verhindern, nicht
// legitime Boards aussperren. Ein Board-Backup besteht aus JSON plus
// Anhängen; 2 GiB entpackt liegen weit über allem, was die
// Attachment-Limits (ADR-0033) je erlauben.
const DefaultMaxUncompressedBytes int64 = 2 << 30

// ErrBackupTooLarge meldet, dass der ENTPACKTE Inhalt die Grenze reisst —
// eigener Sentinel, damit der Handler 413 statt „beschädigt" antwortet.
// Die Unterscheidung ist wichtig: Wer ein legitim zu grosses Archiv
// hochlädt, soll nicht glauben, seine Sicherung sei kaputt.
var ErrBackupTooLarge = errors.New("Backup zu gross: entpackter Inhalt überschreitet das Limit")

// quellFehlerReader merkt sich den ersten Lesefehler der Eingabe, der kein
// EOF ist (#732, F-1816). Damit lässt sich beim Entpacken unterscheiden:
// lieferte die QUELLE den Fehler (Platte/Netz → intern, 500) oder ist der
// Inhalt kein gültiges gzip/tar (→ ErrBackupCorrupted, 400).
type quellFehlerReader struct {
	r   io.Reader
	err error
}

func (q *quellFehlerReader) Read(p []byte) (int, error) {
	n, err := q.r.Read(p)
	if err != nil && err != io.EOF && q.err == nil {
		q.err = err
	}
	return n, err
}

// formatFehler ordnet einen Fehler beim Dekodieren der Eingabe ein:
//   - Die Quelle selbst schlug fehl → interner Fehler, NICHT beschädigt.
//   - Der Fehler trägt schon einen Sentinel (verschlüsselter Rahmen,
//     ErrBackupTooLarge) → unverändert durchreichen, die eigenen Statuscodes
//     des Handlers bleiben erhalten und werden nicht von „beschädigt“
//     überdeckt (der Handler prüft ErrBackupCorrupted zuerst).
//   - Sonst: Format-/Lesefehler des Archivs → ErrBackupCorrupted; der
//     Ursprungsfehler bleibt im Text (keine Pfade: gzip/tar-Meldungen).
func formatFehler(q *quellFehlerReader, was string, err error) error {
	if q.err != nil {
		/* K-3 (Sprint 85): den Quellfehler selbst zurückgeben. `err` kann ein
		   von encReader/DecryptReader „übersetzter“ ErrEncryptedCorrupt sein
		   (jeder ReadFull-Fehler wird dort zu „Block unvollständig“) — das
		   ergäbe 400 „beschädigt“, obwohl die Platte/das Netz schuld ist.
		   Der quellFehlerReader sitzt UNTER dem Entschlüsseler, sieht also
		   den Ursprungsfehler. */
		return fmt.Errorf("%s: %w", was, q.err)
	}
	for _, s := range []error{ErrEncryptedCorrupt, ErrEncryptedFremderSchluessel,
		ErrEncryptedNoKey, ErrBackupTooLarge, ErrBackupCorrupted} {
		if errors.Is(err, s) {
			return fmt.Errorf("%s: %w", was, err)
		}
	}
	return fmt.Errorf("%w (%s: %v)", ErrBackupCorrupted, was, err)
}

// zaehlenderReader zählt die gelesenen Bytes, um die Grenzüberschreitung
// von einem echten Lesefehler unterscheiden zu können.
type zaehlenderReader struct {
	r       io.Reader
	max     int64
	gelesen int64
}

func (z *zaehlenderReader) Read(p []byte) (int, error) {
	n, err := z.r.Read(p)
	z.gelesen += int64(n)
	return n, err
}

// ueberschritten ist true, sobald mehr als `max` Bytes gelesen wurden. Der
// vorgeschaltete io.LimitReader lässt genau ein Byte mehr durch als erlaubt —
// daran erkennen wir die Überschreitung, statt sie mit einem abgeschnittenen
// Archiv zu verwechseln.
func (z *zaehlenderReader) ueberschritten() bool { return z.gelesen > z.max }

// writeAttachmentsFromArchive schreibt die im Archiv enthaltenen Attachments
// in das Ziel-Verzeichnis. Erwartet die Schlüssel der Form "<cardId>/<attId>__<filename>".
func writeAttachmentsFromArchive(boardAttDir string, attachments map[string][]byte) error {
	if len(attachments) == 0 {
		return nil
	}
	if err := os.MkdirAll(boardAttDir, 0o755); err != nil {
		return err
	}
	for rel, body := range attachments {
		dest := filepath.Join(boardAttDir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// generateBoardUUID wrappt eine uuid-Quelle. In Tests kann es per BackupUUID
// überschrieben werden — wir verwenden hier eine schlichte Deferred-Variable
// (gesetzt in main per uuid.NewString-Adapter).
var generateBoardUUID = func() string { return defaultBoardUUID() }

// defaultBoardUUID ist ein Fallback (nur wenn niemand BackupUUID setzt) —
// in Production wird main.go SetUUIDFunc(uuid.NewString) aufrufen.
func defaultBoardUUID() string {
	return fmt.Sprintf("restored-%d", nowFunc().UnixNano())
}

// SetUUIDFunc setzt die UUID-Quelle (vom main-Paket aus, um die uuid-
// Dependency aus boardbackup rauszuhalten — analog storage-Konvention).
func SetUUIDFunc(fn func() string) {
	if fn != nil {
		generateBoardUUID = fn
	}
}
