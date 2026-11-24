package api

// boardbackup.go — REST-Endpunkte für pro-Board-Backups (#303, ADR-0098).
//
// Endpunkte:
//   GET    /boards/{id}/backup/files                — Liste der vorhandenen Backups
//   GET    /boards/{id}/backup/download/{filename}  — tar.gz-Download
//   POST   /boards/{id}/backup/now                  — manuelles Backup auslösen
//   DELETE /boards/{id}/backup/files/{filename}     — Backup löschen
//   POST   /backup/restore                          — Multipart-Upload + Restore
//
// ACL: Board-Owner ODER Server-Admin. Diese API ist NICHT durch den
// ACLGuard (acl.go) abgedeckt, weil dort viewer+ schon GET dürfen — Backup-
// Endpoints sind sensibler (kompletter Board-Dump). Der ACL-Check wird hier
// im Handler gemacht.
//
// Rate-Limit: 10 manuelle Backups/Stunde pro User (token-bucket im RAM).
//
// Abholen per Token (ohne Anmeldung) geht nur über den Zugang „Sicherungen
// lesen“ (abb_, ADR-0142, sicherungen_lesen.go) und nur für verschlüsselte
// Archive; zurückgespielt wird ausschliesslich hier über POST /backup/restore.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/storage"
)

// BoardBackupAPI bündelt die Handler.
type BoardBackupAPI struct {
	boards   *storage.BoardRepo
	acl      *storage.AclRepo
	users    *storage.UserRepo
	activity *storage.ActivityRepo

	attachmentsDir string
	cfg            *storage.ConfigRepo
	aboardVersion  string
	// secretKey ist der Roh-Wert von ABOARD_SECRET_KEY (#336). Leer =
	// keine Verschlüsselung möglich: Backups werden dann unverschlüsselt
	// geschrieben, und ein verschlüsseltes Archiv lässt sich nicht
	// restaurieren (klarer Fehler statt stiller Fehlschlag). Gesetzt wird
	// er über SetSecretKey, nicht über den Konstruktor — der hat schon
	// sieben Parameter, und ein achter optionaler ginge zwischen ihnen unter.
	secretKey string
	// previousSecretKey ist der Roh-Wert von ABOARD_SECRET_KEY_PREVIOUS
	// (Karte #437, [[F-709]]). Er wird AUSSCHLIESSLICH beim Restore
	// herangezogen und nie zum Schreiben: Nach einer Schlüsselrotation muss
	// ein altes Archiv lesbar bleiben, ohne dass der Server mit dem alten
	// Schlüssel startet — dann wären nämlich die aktuellen Config-Secrets
	// und LLM-Keys unlesbar, und es gäbe keinen Zustand, in dem beides geht.
	previousSecretKey string

	limiter *rateLimiter

	// onFailure meldet fehlgeschlagene Boards eines Alle-Boards-Laufs wie der
	// Scheduler (Notification an Admins). Optional (#735).
	onFailure func(boardID, boardTitle string, err error)

	// baseCtx ist die Abbruchkette des Alle-Boards-Laufs (K-5, Sprint 85):
	// vom Request gelöst (der Lauf überlebt den Client), aber an den
	// Server-Kontext aus main gehängt — beim Herunterfahren startet kein
	// weiteres Board mehr. Default context.Background.
	baseCtx context.Context
}

// SetBaseContext hängt den Alle-Boards-Lauf an den Server-Kontext (K-5).
func (a *BoardBackupAPI) SetBaseContext(ctx context.Context) { a.baseCtx = ctx }

// SetOnFailure hinterlegt den Fehler-Callback für den Alle-Boards-Lauf (#735).
func (a *BoardBackupAPI) SetOnFailure(fn func(boardID, boardTitle string, err error)) {
	a.onFailure = fn
}

// NewBoardBackupAPI legt die API an. cfg darf nil sein (Default-Config wird
// dann benutzt). attachmentsDir muss zur AttachmentStore-Basis identisch sein.
func NewBoardBackupAPI(
	boards *storage.BoardRepo,
	acl *storage.AclRepo,
	users *storage.UserRepo,
	activity *storage.ActivityRepo,
	cfg *storage.ConfigRepo,
	attachmentsDir string,
	aboardVersion string,
) *BoardBackupAPI {
	return &BoardBackupAPI{
		boards:         boards,
		acl:            acl,
		users:          users,
		activity:       activity,
		cfg:            cfg,
		attachmentsDir: attachmentsDir,
		aboardVersion:  aboardVersion,
		limiter:        newRateLimiter(10, time.Hour),
	}
}

// SetSecretKey hinterlegt den Master-Key für Backup-Verschlüsselung (#336).
// Leerer String schaltet sie ab. Wird beim Server-Start aus der Env gesetzt.
func (a *BoardBackupAPI) SetSecretKey(key string) { a.secretKey = key }

// SetPreviousSecretKey hinterlegt einen früheren Master-Key, mit dem beim
// Restore zusätzlich ENTSCHLÜSSELT werden darf (Karte #437). Leerer String =
// keiner. Geschrieben wird damit nie.
func (a *BoardBackupAPI) SetPreviousSecretKey(key string) { a.previousSecretKey = key }

// Register hängt die Routen an einen Mux.
func (a *BoardBackupAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /boards/{id}/backup/files",
		WithDefaults(http.HandlerFunc(a.list)))
	mux.Handle("GET /boards/{id}/backup/download/{filename}",
		WithSecurityHeaders(http.HandlerFunc(a.download)))
	mux.Handle("POST /boards/{id}/backup/now",
		WithDefaults(http.HandlerFunc(a.runNow)))
	mux.Handle("DELETE /boards/{id}/backup/files/{filename}",
		WithDefaults(http.HandlerFunc(a.deleteFile)))
	mux.Handle("POST /admin/backup/all-now",
		WithDefaults(http.HandlerFunc(a.runAllNow)))
	mux.Handle("GET /admin/backup/all-status",
		WithDefaults(http.HandlerFunc(a.allStatus)))
	mux.Handle("POST /backup/restore",
		WithSecurityHeaders(http.HandlerFunc(a.restore)))
}

// safeFilenameRe akzeptiert nur die Form, die wir selbst schreiben:
//
//	boardbackup-<boardId>-<YYYYMMDD-HHMMSS>-<sha-short>.tar.gz
//
// Verhindert Path-Traversal über den filename-Parameter.
// Die optionale `.enc`-Endung (#336) gehört ausdrücklich dazu: ohne sie wären
// verschlüsselte Backups zwar auf der Platte, aber weder herunterladbar noch
// löschbar — der Handler würde sie mit „invalid filename" abweisen.
var safeFilenameRe = regexp.MustCompile(`^boardbackup-[a-zA-Z0-9\-_]+-\d{8}-\d{6}-[a-zA-Z0-9]+\.tar\.gz(\.enc)?$`)

// dateinameGehoertZuBoard: der Name hat die Form einer Sicherung (safeFilenameRe)
// UND trägt das Präfix `boardbackup-<boardId>-` dieses Boards (F-2298). Ohne
// den zweiten Teil wäre jede richtig benannte Datei im Board-Ordner
// auslieferbar bzw. löschbar. Gemeinsam für Menschenweg (download, deleteFile)
// und Token-Weg (sicherungAbholbar).
//
// Der Rest nach dem Präfix muss genau Zeitstempel und Endung sein
// (sicherungZeitRe; Kritiker Sprint 97 D-1): sonst ginge bei Board „abc“ die
// Datei „boardbackup-abc-def-…“ des Boards „abc-def“ durch.
func dateinameGehoertZuBoard(boardID, name string) bool {
	rest, ok := strings.CutPrefix(name, "boardbackup-"+boardID+"-")
	return ok && safeFilenameRe.MatchString(name) && sicherungZeitRe.MatchString(rest)
}

// sicherungZeitRe: der Teil einer Sicherung nach `boardbackup-<boardId>-`.
var sicherungZeitRe = regexp.MustCompile(`^\d{8}-\d{6}-[a-zA-Z0-9]+\.tar\.gz(\.enc)?$`)

// requireBoardAccess prüft, dass der Aufrufer Board-Owner ODER Server-Admin ist.
// Im Legacy-Bypass (keine Identität) → ok (Single-User-Modus, identisch zum
// bestehenden ACL-Pattern in boards.go::aclList).
func (a *BoardBackupAPI) requireBoardAccess(w http.ResponseWriter, r *http.Request, boardID string) (string, bool) {
	ident := IdentityFromContext(r.Context())
	if ident == nil {
		return "", true // Legacy-Bypass
	}
	email := storage.NormalizeEmail(ident.Email)
	if email == "" {
		writeError(w, http.StatusForbidden, "forbidden: no identity email")
		return "", false
	}
	if a.acl != nil {
		role, found, err := a.acl.GetRole(r.Context(), boardID, email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return "", false
		}
		if found && role == storage.RoleOwner {
			return email, true
		}
	}
	if a.users != nil {
		u, err := a.users.GetByEmail(r.Context(), email)
		if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
			// F-2312: Ein Lesefehler ist kein fehlendes Recht — wie GetRole oben.
			writeError(w, http.StatusInternalServerError, "admin check failed", err)
			return "", false
		}
		if err == nil && u != nil && u.IsAdmin {
			return email, true
		}
	}
	writeError(w, http.StatusForbidden, "forbidden: nur Board-Owner oder Server-Admin")
	return "", false
}

// hasBoardAccess ist die stille Variante von requireBoardAccess: dieselbe
// Regel (Board-Owner ODER Server-Admin ODER Legacy-Bypass), aber ohne
// HTTP-Antwort. Gebraucht dort, wo die Entscheidung nicht im Handler fällt,
// sondern in einem Callback tief im Restore-Pfad (S1).
//
// `caller` ist leer im Legacy-Bypass (kein Forward-Auth) — dort lässt der
// Server ohnehin jeden durch, und die Prüfung fiele sonst strenger aus als
// der Rest der API.
//
// F-2312: Ein Lesefehler ist false MIT Fehler, nicht „kein Recht“ — sonst
// sähe ein abgebrochener Kontext oder eine gestörte Datenbank aus wie ein
// fehlendes Recht (403), und niemand suchte an der richtigen Stelle.
func (a *BoardBackupAPI) hasBoardAccess(ctx context.Context, boardID, caller string) (bool, error) {
	if caller == "" {
		return true, nil // Legacy-Bypass, identisch zu requireBoardAccess
	}
	if a.acl != nil {
		role, found, err := a.acl.GetRole(ctx, boardID, caller)
		if err != nil {
			return false, err
		}
		if found && role == storage.RoleOwner {
			return true, nil
		}
	}
	return a.isAdmin(ctx, caller)
}

// isAdmin sagt, ob die Email einem Server-Admin gehört. Leerer Aufrufer =
// Legacy-Bypass = true (siehe hasBoardAccess). Ein unbekannter Nutzer ist
// false ohne Fehler, ein Lesefehler false mit Fehler (F-2312).
func (a *BoardBackupAPI) isAdmin(ctx context.Context, caller string) (bool, error) {
	if caller == "" {
		return true, nil
	}
	if a.users == nil {
		return false, nil
	}
	u, err := a.users.GetByEmail(ctx, caller)
	if errors.Is(err, storage.ErrUserNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return u != nil && u.IsAdmin, nil
}

// errRechtePruefung: Die Rechteprüfung des Restore-Pfads konnte nicht lesen
// (F-2312) — 503, nicht 403.
var errRechtePruefung = errors.New("rechteprüfung nicht möglich")

// restoreFrist: Obergrenze einer Wiederherstellung, nachdem der Upload
// vollständig gelesen ist. Ab dort hängt sie nicht mehr an der Verbindung
// (F-2312).
const restoreFrist = 15 * time.Minute

// requireAdminOrAny prüft, dass der Aufrufer eine Identität hat
// (für /backup/restore — Mode-spezifische ACL läuft im Handler).
func (a *BoardBackupAPI) requireAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	ident := IdentityFromContext(r.Context())
	if ident == nil {
		return "", true // Legacy-Bypass
	}
	email := storage.NormalizeEmail(ident.Email)
	if email == "" {
		writeError(w, http.StatusForbidden, "forbidden: no identity email")
		return "", false
	}
	return email, true
}

func (a *BoardBackupAPI) list(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	if _, err := a.boards.Get(r.Context(), boardID); err != nil {
		if errors.Is(err, storage.ErrBoardNotFound) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "board lookup failed", err)
		return
	}
	if _, ok := a.requireBoardAccess(w, r, boardID); !ok {
		return
	}
	cfg := boardbackup.LoadConfig(r.Context(), a.cfg)
	files, err := boardbackup.ListBackups(cfg.Dir, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files": files,
	})
}

func (a *BoardBackupAPI) download(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	filename := r.PathValue("filename")
	if boardID == "" || filename == "" {
		writeError(w, http.StatusBadRequest, "missing parameters")
		return
	}
	if !dateinameGehoertZuBoard(boardID, filename) {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	if _, ok := a.requireBoardAccess(w, r, boardID); !ok {
		return
	}
	cfg := boardbackup.LoadConfig(r.Context(), a.cfg)
	path := filepath.Join(cfg.Dir, boardID, filename)
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "backup not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat failed", err)
		return
	}
	// ADR-0142 §5: Frist nach Dateigröße statt der Server-Frist von 30 s —
	// sonst bricht ein großes Archiv über eine langsame Leitung ab.
	setzeSicherungSchreibfrist(w, fi.Size(), "board-backup-download")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (a *BoardBackupAPI) runNow(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	if _, err := a.boards.Get(r.Context(), boardID); err != nil {
		if errors.Is(err, storage.ErrBoardNotFound) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "board lookup failed", err)
		return
	}
	caller, ok := a.requireBoardAccess(w, r, boardID)
	if !ok {
		return
	}
	// Rate-Limit-Key: Caller-Email oder boardID im Legacy-Bypass.
	limitKey := caller
	if limitKey == "" {
		limitKey = "anon:" + boardID
	}
	if !a.limiter.allow(limitKey) {
		writeError(w, http.StatusTooManyRequests, "rate limit: 10 manual backups per hour")
		return
	}

	cfg := boardbackup.LoadConfig(r.Context(), a.cfg)
	if !cfg.Enabled {
		writeError(w, http.StatusServiceUnavailable, "board-backup ist deaktiviert (backup_board.enabled=false)")
		return
	}

	// Erst mit Platzhalter-Filename schreiben, dann auf den finalen Namen
	// (inkl. SHA-Prefix) umbenennen.
	//
	// F-710: Der Platzhaltername wird über boardbackup.ReservePendingPath
	// ATOMAR reserviert (os.CreateTemp/O_EXCL), nicht mehr per String-Bau
	// aus Board-ID + Sekunden-Zeitstempel. Ein selbst gebauter Name kollidiert
	// mit dem nächtlichen Scheduler-Lauf (server/boardbackup/scheduler.go),
	// wenn beide dasselbe Board in derselben Sekunde sichern — BackupBoard
	// öffnet den Zwischennamen mit os.Create (truncate), der zweite Schreiber
	// hätte den ersten mitten im Schreibvorgang überschrieben.
	ts := time.Now().UTC()
	tmpPath, err := boardbackup.ReservePendingPath(cfg.Dir, boardID, ts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup failed", err)
		return
	}

	crypter, cerr := boardbackup.ResolveCrypter(cfg, a.secretKey)
	if cerr != nil {
		// Konfigurationsfehler des Operators (Verschlüsselung an, aber kein
		// Schlüssel bzw. unbekannte Quelle). Bewusst KEIN unverschlüsseltes
		// Ersatz-Backup — das wäre eine stille Herabstufung.
		//
		// S5: Der Text von ErrUnknownKeySource führt den GESETZTEN
		// Config-Wert mit — Server-Konfiguration gehört nicht in eine
		// Antwort an einen Board-Owner. Nach aussen geht deshalb nur der
		// Sentinel; den vollen Grund bekommt das Log (F-252).
		nachAussen := cerr.Error()
		if errors.Is(cerr, boardbackup.ErrUnknownKeySource) {
			nachAussen = boardbackup.ErrUnknownKeySource.Error()
		}
		// Der per ReservePendingPath angelegte Platzhalter wird nie
		// beschrieben, wenn wir hier abbrechen — aufräumen, sonst bleibt
		// eine leere -pending-Leiche liegen (F-710-Nacharbeit).
		_ = os.Remove(tmpPath)
		writeError(w, http.StatusServiceUnavailable, nachAussen, cerr)
		return
	}

	res, err := boardbackup.BackupBoard(r.Context(), boardbackup.Options{
		BoardID:            boardID,
		Boards:             a.boards,
		ACL:                a.acl,
		AttachmentsBaseDir: a.attachmentsDir,
		Out:                tmpPath,
		AboardVersion:      a.aboardVersion,
		Crypter:            crypter,
	})
	if err != nil {
		_ = os.Remove(tmpPath)
		// F-252: Roh-Fehler (kann lokale Pfade enthalten) nur noch ins
		// Server-Log, nicht mehr in die Antwort.
		writeError(w, http.StatusInternalServerError, "backup failed", err)
		return
	}
	// Bei Verschlüsselung schreibt BackupBoard unter tmpPath+EncSuffix
	// (res.Path), der leere Platzhalter unter tmpPath selbst bleibt sonst
	// als Leiche liegen (F-710-Nacharbeit).
	if res.Path != tmpPath {
		_ = os.Remove(tmpPath)
	}

	// res.Path statt tmpPath: bei Verschlüsselung hat BackupBoard die Datei
	// unter tmpPath+".enc" geschrieben. Wer hier tmpPath umbenennt, benennt
	// eine Datei um, die es nicht gibt.
	finalName := boardbackup.FormatFilename(boardID, ts, res.SHA256)
	if res.Encrypted {
		finalName += boardbackup.EncSuffix
	}
	finalPath := filepath.Join(filepath.Dir(tmpPath), finalName)
	if err := os.Rename(res.Path, finalPath); err != nil {
		writeError(w, http.StatusInternalServerError, "rename failed", err)
		return
	}
	res.Filename = finalName

	// Activity-Log (best-effort).
	if a.activity != nil {
		_ = a.activity.Record(r.Context(), boardID, caller, "board-backup-created", "", "", finalName, "")
	}

	// Rotation laufen lassen (nicht-blockierend für die Response).
	if _, rerr := boardbackup.Rotate(cfg.Dir, boardID, cfg.KeepDays); rerr != nil {
		// Rotation-Fehler ist nicht fatal — Backup steht.
		// Wir loggen über den Default-Logger (kein Pakets-Logger hier).
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filename": res.Filename,
		"size":     res.Size,
		"sha256":   res.SHA256,
		"ts":       res.TS,
	})
}

func (a *BoardBackupAPI) deleteFile(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	filename := r.PathValue("filename")
	if boardID == "" || filename == "" {
		writeError(w, http.StatusBadRequest, "missing parameters")
		return
	}
	if !dateinameGehoertZuBoard(boardID, filename) {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	if _, ok := a.requireBoardAccess(w, r, boardID); !ok {
		return
	}
	cfg := boardbackup.LoadConfig(r.Context(), a.cfg)
	path := filepath.Join(cfg.Dir, boardID, filename)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "backup not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *BoardBackupAPI) restore(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.requireAuth(w, r)
	if !ok {
		return
	}
	cfg := boardbackup.LoadConfig(r.Context(), a.cfg)
	maxBytes := int64(cfg.MaxUploadMB) * 1024 * 1024
	// Vorab-Check via Content-Length — der Browser setzt den Header bei
	// Multipart-Uploads in der Regel. Spart das volle Streaming bis zum
	// MaxBytesReader-EOF und liefert einen klaren 413.
	if r.ContentLength > maxBytes+1024 {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("backup exceeds %d MB", cfg.MaxUploadMB))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1024)

	if err := r.ParseMultipartForm(8 * 1024 * 1024); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("backup exceeds %d MB", cfg.MaxUploadMB))
		} else {
			writeError(w, http.StatusBadRequest, "multipart parse error")
		}
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "field 'file' missing")
		return
	}
	defer file.Close()
	if header != nil && header.Size > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("backup exceeds %d MB", cfg.MaxUploadMB))
		return
	}

	// F-2312: Ab hier ist der Upload vollständig gelesen. Schließt der Client
	// danach seine Schreibseite (curl nicht, nc schon), beendet Go den
	// Anfragekontext — die Rechteprüfung las dann mit abgebrochenem Kontext
	// und meldete 403, eine halb gelaufene Wiederherstellung brach ab. Der
	// Rest läuft deshalb auf einem vom Client gelösten Kontext mit eigener
	// Frist; die Werte (Identität) bleiben erhalten.
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(r.Context()), restoreFrist)
	defer rcancel()
	r = r.WithContext(rctx)

	mode := boardbackup.RestoreMode(r.FormValue("mode"))
	targetBoardID := r.FormValue("target_board_id")

	// Im overwrite-Modus prüfen wir die Ziel-Board-ACL.
	if mode == boardbackup.RestoreOverwrite {
		if targetBoardID == "" {
			writeError(w, http.StatusBadRequest, "target_board_id required for overwrite")
			return
		}
		if _, ok := a.requireBoardAccess(w, r, targetBoardID); !ok {
			return
		}
	}

	// Der Restore-Crypter haengt am Schluessel, NICHT an
	// cfg.EncryptionEnabled: ein Archiv aus einer Phase mit eingeschalteter
	// Verschluesselung muss auch dann zurueckspielbar sein, wenn der Schalter
	// inzwischen wieder aus ist.
	//
	// Der Vorgängerschlüssel kommt NUR hier dazu — im Restore-Pfad. Die
	// Schreibseite (`runNow`, Scheduler) sieht ihn nicht, sonst verlängerte
	// sich seine Lebensdauer unbemerkt (Karte #437).
	//
	// ZWEI SCHLÜSSEL, ZWEI FEHLERWEGE (Sprint-62-Kritiker, K2): Die erste
	// Fassung reichte beide zusammen an `NewCrypterMitVorgaengern` und machte
	// aus jedem Fehler ein `503 ABOARD_SECRET_KEY ungültig`. Ein Betreiber,
	// der den Platzhalter aus der Compose-Vorlage nicht ersetzt hat, bekam
	// damit für JEDEN Restore eine Absage — auch für ein unverschlüsseltes
	// Archiv, auch für eines mit dem aktuellen Schlüssel — und die Meldung
	// schickte ihn zur falschen Variablen. Der Rettungsweg, den diese Karte
	// bauen sollte, war dadurch schlechter als vorher.
	//
	// Deshalb getrennt: Ein kaputter AKTUELLER Schlüssel ist ein harter
	// Fehler (ohne ihn geht nichts). Ein kaputter VORGÄNGER kostet nur die
	// Zusatzfähigkeit — der Restore läuft ohne ihn weiter und sagt im Log,
	// was fehlt. Wer ihn wirklich braucht, bekommt dann den präzisen
	// „anderer Schlüssel"-Fehler statt eines pauschalen 503.
	restoreCrypter, rcErr := boardbackup.NewCrypterMitVorgaengern(a.secretKey, a.previousSecretKey)
	if rcErr != nil && a.previousSecretKey != "" {
		// Trägt der aktuelle Schlüssel allein? Dann lag es am Vorgänger.
		if ohneVorgaenger, err2 := boardbackup.NewCrypterMitVorgaengern(a.secretKey); err2 == nil {
			log.Printf("aboard-server: ABOARD_SECRET_KEY_PREVIOUS unbrauchbar (%s) — "+
				"Restore läuft OHNE Vorgängerschlüssel; Archive des alten Schlüssels "+
				"bleiben unlesbar, bis der Wert korrigiert ist", redactForLog(rcErr))
			restoreCrypter, rcErr = ohneVorgaenger, nil
		}
	}
	if rcErr != nil {
		writeError(w, http.StatusServiceUnavailable, "ABOARD_SECRET_KEY ungültig", rcErr)
		return
	}

	// S1 (Sprint-58-Kritiker): Auch im as-new-Modus muss der Aufrufer das
	// QUELL-Board des Archivs sehen dürfen. Ohne diese Prüfung war der
	// Endpunkt ein Entschlüsselungs-Orakel — jeder angemeldete Nutzer konnte
	// ein fremdes verschlüsseltes Archiv hochladen und bekam den Klartext
	// als neues Board, das ihm gehört. Die Prüfung läuft erst, wenn
	// `meta.board_id` gelesen ist, und immer vor dem ersten Schreibzugriff.
	//
	// Der overwrite-Modus behält seine bisherige Ziel-Board-Prüfung oben —
	// beide zusammen: Wer überschreibt, braucht Rechte am Ziel; wer
	// entschlüsselt, braucht Rechte an der Quelle.
	autorisiereQuelle := func(sourceBoardID string) error {
		if sourceBoardID == "" {
			// Archiv ohne Board-ID im meta.json: nur Server-Admins.
			// Lieber eine abgelehnte Wiederherstellung als eine
			// Autorisierungslücke über ein manipuliertes meta.json.
			admin, err := a.isAdmin(r.Context(), caller)
			if err != nil {
				return fmt.Errorf("%w: %w", errRechtePruefung, err)
			}
			if admin {
				return nil
			}
			return boardbackup.ErrRestoreForbidden
		}
		ok, err := a.hasBoardAccess(r.Context(), sourceBoardID, caller)
		if err != nil {
			return fmt.Errorf("%w: %w", errRechtePruefung, err)
		}
		if ok {
			return nil
		}
		return boardbackup.ErrRestoreForbidden
	}

	// F-1597: Der Notfall-Schalter übergeht eine Datenregel des Servers — das
	// entscheidet der Betrieb, nicht jede Person mit Rückspiel-Recht. Ein
	// Board-Owner ohne Admin-Recht bekommt 403, auch wenn das Archiv sauber
	// ist (der Schalter wird nicht still ignoriert).
	findingsPruefungAus := r.FormValue("findings_pruefung") == "aus"
	if findingsPruefungAus {
		admin, err := a.isAdmin(r.Context(), caller)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "Rechteprüfung gerade nicht möglich; nichts geschrieben", err)
			return
		}
		if !admin {
			writeError(w, http.StatusForbidden, "forbidden: findings_pruefung=aus nur für Server-Admins")
			return
		}
	}

	res, err := boardbackup.RestoreBackup(r.Context(), boardbackup.RestoreOptions{
		ArchiveReader:      file,
		Crypter:            restoreCrypter,
		AuthorizeSource:    autorisiereQuelle,
		Mode:               mode,
		TargetBoardID:      targetBoardID,
		RestoreUserEmail:   caller,
		Boards:             a.boards,
		ACL:                a.acl,
		AttachmentsBaseDir: a.attachmentsDir,
		AboardVersion:      a.aboardVersion,
		// KA-7: Notfall-Schalter für Archive mit Finding-Verstößen aus der Zeit
		// vor F-1572. Nur ausdrücklich, nie voreingestellt; nur Server-Admins
		// (F-1597, geprüft oben).
		FindingsPruefungAus: findingsPruefungAus,
	})
	if err != nil {
		// Karte #726: Jede Rückspielung hinterlässt eine Logzeile — auch die
		// abgelehnte. Bis v1.48.0 schrieb der Server hier nur bei internen
		// Fehlern etwas (writeError mit err); ein abgelehnter oder geglückter
		// Restore war im Nachhinein nicht belegbar (Team-Betrieb, 06.10.). Keine Adresse
		// im Log, nur Modus und Ziel; der Fehlertext läuft durch redactForLog.
		// `mode` ist der rohe Formularwert — deshalb %q, sonst schleust ein
		// Zeilenumbruch eine gefälschte „restore ok“-Zeile ein (Kritiker A-1).
		log.Printf("aboard-server: restore abgelehnt (mode=%q, ziel=%q): %s", string(mode), targetBoardID, redactForLog(err))
		// Mappe bekannte Fehler auf sprechende Status-Codes.
		switch {
		case errors.Is(err, boardbackup.ErrBackupCorrupted):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, boardbackup.ErrSchemaTooOld), errors.Is(err, boardbackup.ErrSchemaTooNew):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, boardbackup.ErrFindingVerstoss):
			// F-1577: wie PUT/POST (F-1572) — 422 mit benannten Verstößen.
			// Rettungsweg: ?findings_pruefung=aus (Formularfeld, nur Server-Admins) — siehe KA-7.
			writeError(w, http.StatusUnprocessableEntity, err.Error()+"; emergency override (server admins only): form field findings_pruefung=aus")
		case errors.Is(err, boardbackup.ErrRestoreAufKopie):
			// ADR-0141 §7 (Karte #826): 409 — das Ziel ist eine Kopie.
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, errRechtePruefung):
			writeError(w, http.StatusServiceUnavailable, "Rechteprüfung gerade nicht möglich; nichts geschrieben", err)
		case errors.Is(err, boardbackup.ErrRestoreForbidden):
			writeError(w, http.StatusForbidden,
				"forbidden: kein Zugriff auf das Quell-Board dieses Backups")
		case errors.Is(err, boardbackup.ErrEncryptedNoKey):
			// Operator-Fehler, nicht Client-Fehler: die Datei ist in Ordnung,
			// dem Server fehlt der Schlüssel.
			writeError(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel):
			// Karte #437: Die eine Unterscheidung, die dem Betreiber wirklich
			// hilft — und hier ausdrücklich MIT dem Wrap-Text, anders als beim
			// Korruptionsfall darunter. Der Wrap nennt den Schlüssel-Abdruck
			// aus dem Dateikopf; der steht dort im Klartext, wer die Datei
			// hochlädt, hat ihn also bereits. Ein Orakel ist das nicht — wohl
			// aber der Unterschied zwischen „such deinen alten Schlüssel" und
			// einer Stunde Suche am falschen Ende.
			writeError(w, http.StatusBadRequest, err.Error(), err)
		case errors.Is(err, boardbackup.ErrEncryptedCorrupt):
			// S5: NUR der Sentinel-Text nach aussen. Die feineren Wrap-Gründe
			// („abgeschnitten", „Block unvollständig", …) unterscheiden einen
			// Rahmenfehler von einem fehlgeschlagenen Auth-Tag — für jemanden
			// ohne Schlüssel ein schwaches Orakel. writeError loggt den vollen
			// Fehler ohnehin (F-252).
			writeError(w, http.StatusBadRequest, boardbackup.ErrEncryptedCorrupt.Error(), err)
		case errors.Is(err, boardbackup.ErrBackupTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		default:
			// F-252: unerwarteter interner Fehler — Roh-Text nur ins Log,
			// die anderen zwei Zweige oben sind bewusst client-sichere
			// Sentinel-Fehler (Korruption/Schema-Version) und bleiben as-is.
			writeError(w, http.StatusInternalServerError, "restore failed", err)
		}
		return
	}

	if a.activity != nil {
		_ = a.activity.Record(r.Context(), res.BoardID, caller, "board-restored", "mode", "", string(res.Mode), "")
	}
	log.Printf("aboard-server: restore ok (mode=%q, board=%q, finding_warnungen=%d)", string(res.Mode), res.BoardID, len(res.FindingWarnungen))

	antwort := map[string]any{
		"board_id":    res.BoardID,
		"mode":        string(res.Mode),
		"restored_at": time.Now().UTC().Format(time.RFC3339),
	}
	if len(res.FindingWarnungen) > 0 {
		// KA-7: Verstöße, die der Notfall-Schalter übergangen hat.
		antwort["finding_warnungen"] = res.FindingWarnungen
	}
	writeJSON(w, http.StatusOK, antwort)
}

// ensureBackupDir legt das Top-Level-Backup-Verzeichnis an (idempotent).
// Wird vom main-Paket beim Start aufgerufen, damit die erste Goroutine
// nicht in einen ENOENT läuft.
func EnsureBackupDir(cfg *storage.ConfigRepo, ctx context.Context) error {
	c := boardbackup.LoadConfig(ctx, cfg)
	return os.MkdirAll(c.Dir, 0o755)
}

// requireStrictAdmin: nur ein bekannter Server-Admin (#735). Anders als
// hasBoardAccess KEIN Legacy-Bypass — dieselbe Strenge wie AdminAPI.requireAdmin.
// Liefert die User-ID (für Rate-Limit und Logzeile; nie die Adresse).
// DB-Fehler → 500 wie admin.go (K-6c, Sprint 85); „kein User“ → 403.
func (a *BoardBackupAPI) requireStrictAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	ident := IdentityFromContext(r.Context())
	if ident == nil {
		writeError(w, http.StatusForbidden, "forbidden: admin endpoints require authentication")
		return "", false
	}
	email := storage.NormalizeEmail(ident.Email)
	if email == "" || a.users == nil {
		writeError(w, http.StatusForbidden, "forbidden: no identity email")
		return "", false
	}
	u, err := a.users.GetByEmail(r.Context(), email)
	if errors.Is(err, storage.ErrUserNotFound) {
		writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
		return "", false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin lookup failed", err)
		return "", false
	}
	if u == nil || !u.IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden: not an admin")
		return "", false
	}
	return u.ID, true
}

// allLaeuftText: 409-Text von POST /admin/backup/all-now (zweimal gebraucht).
const allLaeuftText = "Es läuft bereits eine Sicherung über alle Boards — bitte warten, bis sie fertig ist."

// runAllNow (POST /admin/backup/all-now) startet einen Sicherungslauf über
// ALLE Boards inkl. Rotation — dieselbe Routine wie der nächtliche Scheduler.
//
// ASYNCHRON (202), weil der HTTP-Server ein WriteTimeout von 30 s hat und ein
// Lauf über viele Boards mit Anhängen länger dauern kann; eine synchrone
// Antwort bräche dann ab, während der Lauf im Hintergrund weiterläuft. Der
// Fortschritt wird über GET /admin/backup/all-status abgefragt.
//
// DOPPELSTART: gemeinsame Sperre in boardbackup (ReserviereAll) — gilt für
// Scheduler UND Knopf; wer sie nicht bekommt, erhält 409.
func (a *BoardBackupAPI) runAllNow(w http.ResponseWriter, r *http.Request) {
	adminID, ok := a.requireStrictAdmin(w, r)
	if !ok {
		return
	}
	cfg := boardbackup.LoadConfig(r.Context(), a.cfg)
	if !cfg.Enabled {
		writeError(w, http.StatusServiceUnavailable, "board-backup ist deaktiviert (backup_board.enabled=false)")
		return
	}
	crypter, cerr := boardbackup.ResolveCrypter(cfg, a.secretKey)
	if cerr != nil {
		nachAussen := cerr.Error()
		if errors.Is(cerr, boardbackup.ErrUnknownKeySource) {
			nachAussen = boardbackup.ErrUnknownKeySource.Error()
		}
		writeError(w, http.StatusServiceUnavailable, nachAussen, cerr)
		return
	}
	// K-6a (Sprint 85): wie runNow begrenzt — jeder Lauf legt je Board eine
	// Datei an, und die Rotation behält alles im keep_days-Fenster.
	// Erst NACH 503 und nach dem 409-Vorabblick gezählt (Kritiker/Verify
	// Sprint 85, F-1825): gezählt werden Läufe, nicht Versuche — wer während
	// eines langen nächtlichen Laufs zehnmal 409 bekommt, darf danach trotzdem
	// starten. Der Vorabblick ist nur lesend; die Sperre nimmt weiter allein
	// ReserviereAll. Verliert ein Klick das schmale Rennen zwischen beiden,
	// zählt er einmal mit — hingenommen, statt eine Freigabe ohne Lauf
	// einzuführen.
	if laeuft, _ := boardbackup.AllStatus(); laeuft {
		writeError(w, http.StatusConflict, allLaeuftText)
		return
	}
	if !a.limiter.allow("alle:" + adminID) {
		writeError(w, http.StatusTooManyRequests, "rate limit: 10 manual backups per hour")
		return
	}
	if !boardbackup.ReserviereAll() {
		writeError(w, http.StatusConflict, allLaeuftText)
		return
	}
	deps := boardbackup.SchedulerDeps{
		Crypter:            crypter,
		EncryptionRequired: cfg.EncryptionEnabled,
		Boards:             a.boards,
		ACL:                a.acl,
		AttachmentsBaseDir: a.attachmentsDir,
		BackupBaseDir:      cfg.Dir,
		KeepDays:           cfg.KeepDays,
		CronPattern:        cfg.Cron,
		AboardVersion:      a.aboardVersion,
		Logger:             log.Printf,
		OnFailure:          a.onFailure,
	}
	// K-6b: Auslöser ins Log (User-ID, nie die Adresse — Muster #726).
	log.Printf("aboard-server: Sicherung über alle Boards ausgelöst (admin=%q)", adminID)
	// Vom Request lösen: der Lauf soll den Client überleben, aber das
	// Herunterfahren des Servers beenden (K-5). Panik fängt
	// RunAllBoardsReserviert selbst ab (K-7).
	base := a.baseCtx
	if base == nil {
		base = context.Background()
	}
	go boardbackup.RunAllBoardsReserviert(base, deps)
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// allStatus (GET /admin/backup/all-status): läuft ein Lauf, und was war das
// letzte Ergebnis (seit Prozessstart)?
func (a *BoardBackupAPI) allStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireStrictAdmin(w, r); !ok {
		return
	}
	laeuft, last := boardbackup.AllStatus()
	writeJSON(w, http.StatusOK, map[string]any{"running": laeuft, "last": last})
}
