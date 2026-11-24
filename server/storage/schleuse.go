package storage

// schleuse.go — Speicher der Ausgangsschleuse, Grundlage S1 (ADR-0134 §2,
// Karte #684): Admin-Freigabeliste, Schlüssel je Person, Board-Sperre.
//
// Abgrenzung zum Board-Chat (ADR-0133 §2, Firmenmodus): Die Schlüssel der
// Schleuse liegen in schleuse_schluessel, NICHT in llm_api_keys. Keine Funktion
// hier liest llm_api_keys, und llm_keys.go liest diese Tabelle nicht —
// schleuse_test.go prüft beides (Quelltext und Verhalten). Würde die Schleuse
// den Chat-Schlüsselspeicher lesen, umginge sie die Sperre des Firmenmodus.
//
// Schlüssel-Klartext gibt nur SchleuseSchluesselRepo.Get heraus — für den
// serverseitigen Versand (S3). Die Liste trägt nie einen Schlüssel.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/schleuse"
)

var (
	// ErrSchleuseFreigabeNichtGefunden: keine Freigabe mit dieser ID.
	ErrSchleuseFreigabeNichtGefunden = errors.New("storage: schleuse-freigabe nicht gefunden")
	// ErrSchleuseFreigabeDoppelt: Anbieter und Basis-URL gibt es schon.
	ErrSchleuseFreigabeDoppelt = errors.New("storage: schleuse-freigabe mit diesem anbieter und dieser basis-url gibt es schon")
	// ErrSchleuseSchluesselNichtGefunden: für (Person, Freigabe) ist kein Schlüssel hinterlegt.
	ErrSchleuseSchluesselNichtGefunden = errors.New("storage: schleuse-schlüssel nicht gefunden")
	// ErrSchleuseStandVeraltet: Die Freigabe hat sich geändert, seit die Person
	// den Dialog geöffnet hat (anderer Anbieter oder andere Basis-URL).
	ErrSchleuseStandVeraltet = errors.New("storage: schleuse-freigabe hat sich geändert")
	// ErrSchleuseNichtFreigegeben: Die Freigabe ist inaktiv oder die
	// Vertragsform nicht (mehr) bestätigt.
	ErrSchleuseNichtFreigegeben = errors.New("storage: schleuse-freigabe ist nicht freigegeben")
)

// Audit-Aktionen der Schleuse in server_config_audit.
const (
	AuditActionSchleuseFreigabeAnlegen  = "schleuse-freigabe-anlegen"
	AuditActionSchleuseFreigabeAendern  = "schleuse-freigabe-aendern"
	AuditActionSchleuseFreigabeLoeschen = "schleuse-freigabe-loeschen"
	AuditActionSchleuseBoardSperre      = "schleuse-board-sperre"
)

// SchleuseFreigabe ist ein Eintrag der Admin-Freigabeliste.
type SchleuseFreigabe struct {
	ID                        string   `json:"id"`
	Anbieter                  string   `json:"anbieter"`
	Bezeichnung               string   `json:"bezeichnung"`
	BasisURL                  string   `json:"basis_url"`
	Modelle                   []string `json:"modelle"`
	VertragsformBestaetigt    bool     `json:"vertragsform_bestaetigt"`
	VertragsformBestaetigtAm  string   `json:"vertragsform_bestaetigt_am,omitempty"`
	VertragsformBestaetigtVon string   `json:"vertragsform_bestaetigt_von,omitempty"`
	VierAugen                 bool     `json:"vier_augen"`
	Aktiv                     bool     `json:"aktiv"`
	ErstelltAm                string   `json:"erstellt_am"`
	ErstelltVon               string   `json:"erstellt_von"`
	GeaendertAm               string   `json:"geaendert_am"`
	GeaendertVon              string   `json:"geaendert_von"`
}

// SchleuseFreigabeRepo verwaltet schleuse_freigaben.
type SchleuseFreigabeRepo struct {
	db *sql.DB
}

// NewSchleuseFreigabeRepo legt das Repo an.
func NewSchleuseFreigabeRepo(db *sql.DB) *SchleuseFreigabeRepo {
	return &SchleuseFreigabeRepo{db: db}
}

const freigabeSpalten = `id, anbieter, bezeichnung, basis_url, modelle, vertragsform_bestaetigt,
	coalesce(vertragsform_bestaetigt_am, ''), coalesce(vertragsform_bestaetigt_von, ''),
	vier_augen, aktiv, erstellt_am, erstellt_von, geaendert_am, geaendert_von`

type zeilenLeser interface{ Scan(dest ...any) error }

func scanFreigabe(z zeilenLeser) (SchleuseFreigabe, error) {
	var f SchleuseFreigabe
	var modelle string
	var vertrag, vier, aktiv int
	if err := z.Scan(&f.ID, &f.Anbieter, &f.Bezeichnung, &f.BasisURL, &modelle, &vertrag,
		&f.VertragsformBestaetigtAm, &f.VertragsformBestaetigtVon, &vier, &aktiv,
		&f.ErstelltAm, &f.ErstelltVon, &f.GeaendertAm, &f.GeaendertVon); err != nil {
		return f, err
	}
	f.VertragsformBestaetigt, f.VierAugen, f.Aktiv = vertrag == 1, vier == 1, aktiv == 1
	if err := json.Unmarshal([]byte(modelle), &f.Modelle); err != nil || f.Modelle == nil {
		f.Modelle = []string{}
	}
	return f, nil
}

// List liefert alle Einträge, geordnet nach Anbieter und Bezeichnung.
func (r *SchleuseFreigabeRepo) List(ctx context.Context) ([]SchleuseFreigabe, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+freigabeSpalten+` FROM schleuse_freigaben ORDER BY anbieter, bezeichnung, basis_url`)
	if err != nil {
		return nil, fmt.Errorf("schleuse-freigaben.list: %w", err)
	}
	defer rows.Close()
	out := []SchleuseFreigabe{}
	for rows.Next() {
		f, err := scanFreigabe(rows)
		if err != nil {
			return nil, fmt.Errorf("schleuse-freigaben.list scan: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Get liefert einen Eintrag.
func (r *SchleuseFreigabeRepo) Get(ctx context.Context, id string) (SchleuseFreigabe, error) {
	f, err := scanFreigabe(r.db.QueryRowContext(ctx, `SELECT `+freigabeSpalten+` FROM schleuse_freigaben WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrSchleuseFreigabeNichtGefunden
	}
	if err != nil {
		return f, fmt.Errorf("schleuse-freigaben.get: %w", err)
	}
	return f, nil
}

func istUniqueFehler(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func modelleJSON(m []string) string {
	if m == nil {
		m = []string{}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// schleuseAudit schreibt einen Audit-Eintrag in derselben Transaktion.
func schleuseAudit(ctx context.Context, tx *sql.Tx, key, userID, action, alt, neu string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), key, userID, action, nullableString(alt), nullableString(neu), nowISO())
	return err
}

// auditText fasst einen Eintrag für das Audit zusammen — ohne Personenbezug.
func (f SchleuseFreigabe) auditText() string {
	return fmt.Sprintf("anbieter=%s;basis=%s;modelle=%s;vertragsform=%t;vier_augen=%t;aktiv=%t",
		f.Anbieter, f.BasisURL, strings.Join(f.Modelle, ","), f.VertragsformBestaetigt, f.VierAugen, f.Aktiv)
}

// Anlegen legt einen Eintrag an. Die Felder sind vom Aufrufer geprüft
// (server/schleuse: PruefeBasis, PruefeModelle). ID, Zeitstempel und — bei
// bestätigter Vertragsform — Zeitpunkt und Admin der Bestätigung setzt das Repo.
func (r *SchleuseFreigabeRepo) Anlegen(ctx context.Context, f SchleuseFreigabe, adminID string) (SchleuseFreigabe, error) {
	f.ID = uuid.NewString()
	jetzt := nowISO()
	f.ErstelltAm, f.ErstelltVon, f.GeaendertAm, f.GeaendertVon = jetzt, adminID, jetzt, adminID
	f.VertragsformBestaetigtAm, f.VertragsformBestaetigtVon = "", ""
	if f.VertragsformBestaetigt {
		f.VertragsformBestaetigtAm, f.VertragsformBestaetigtVon = jetzt, adminID
	}
	if f.Modelle == nil {
		f.Modelle = []string{}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return f, fmt.Errorf("schleuse-freigaben.anlegen begin: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO schleuse_freigaben (id, anbieter, bezeichnung, basis_url, modelle, vertragsform_bestaetigt,
		   vertragsform_bestaetigt_am, vertragsform_bestaetigt_von, vier_augen, aktiv,
		   erstellt_am, erstellt_von, geaendert_am, geaendert_von)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Anbieter, f.Bezeichnung, f.BasisURL, modelleJSON(f.Modelle), boolInt(f.VertragsformBestaetigt),
		nullableString(f.VertragsformBestaetigtAm), nullableString(f.VertragsformBestaetigtVon),
		boolInt(f.VierAugen), boolInt(f.Aktiv), f.ErstelltAm, f.ErstelltVon, f.GeaendertAm, f.GeaendertVon)
	if istUniqueFehler(err) {
		return f, ErrSchleuseFreigabeDoppelt
	}
	if err != nil {
		return f, fmt.Errorf("schleuse-freigaben.anlegen: %w", err)
	}
	if err := schleuseAudit(ctx, tx, "schleuse-freigabe:"+f.ID, adminID, AuditActionSchleuseFreigabeAnlegen, "", f.auditText()); err != nil {
		return f, fmt.Errorf("schleuse-freigaben.anlegen audit: %w", err)
	}
	return f, tx.Commit()
}

// Aendern ersetzt die änderbaren Felder eines Eintrags (Anbieter, Bezeichnung,
// Basis-URL, Modelle, Vertragsform, Vier-Augen, aktiv). Die Bestätigung der
// Vertragsform behält Zeitpunkt und Admin, solange sie bestätigt bleibt; wird
// sie neu gesetzt, trägt sie den ändernden Admin, wird sie zurückgenommen,
// verschwinden beide.
//
// Wechseln Anbieter oder Basis-URL, gehört der hinterlegte Schlüssel einer
// Person zu einem ANDEREN Gegenüber als dem, für das sie ihn eingetragen hat.
// Die Schlüssel dieses Eintrags werden dann gelöscht (im selben Commit).
func (r *SchleuseFreigabeRepo) Aendern(ctx context.Context, f SchleuseFreigabe, adminID string) (SchleuseFreigabe, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return f, fmt.Errorf("schleuse-freigaben.aendern begin: %w", err)
	}
	defer tx.Rollback()
	alt, err := scanFreigabe(tx.QueryRowContext(ctx, `SELECT `+freigabeSpalten+` FROM schleuse_freigaben WHERE id = ?`, f.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrSchleuseFreigabeNichtGefunden
	}
	if err != nil {
		return f, fmt.Errorf("schleuse-freigaben.aendern lesen: %w", err)
	}
	jetzt := nowISO()
	f.ErstelltAm, f.ErstelltVon = alt.ErstelltAm, alt.ErstelltVon
	f.GeaendertAm, f.GeaendertVon = jetzt, adminID
	switch {
	case !f.VertragsformBestaetigt:
		f.VertragsformBestaetigtAm, f.VertragsformBestaetigtVon = "", ""
	case alt.VertragsformBestaetigt:
		f.VertragsformBestaetigtAm, f.VertragsformBestaetigtVon = alt.VertragsformBestaetigtAm, alt.VertragsformBestaetigtVon
	default:
		f.VertragsformBestaetigtAm, f.VertragsformBestaetigtVon = jetzt, adminID
	}
	if f.Modelle == nil {
		f.Modelle = []string{}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE schleuse_freigaben SET anbieter = ?, bezeichnung = ?, basis_url = ?, modelle = ?,
		   vertragsform_bestaetigt = ?, vertragsform_bestaetigt_am = ?, vertragsform_bestaetigt_von = ?,
		   vier_augen = ?, aktiv = ?, geaendert_am = ?, geaendert_von = ?
		 WHERE id = ?`,
		f.Anbieter, f.Bezeichnung, f.BasisURL, modelleJSON(f.Modelle), boolInt(f.VertragsformBestaetigt),
		nullableString(f.VertragsformBestaetigtAm), nullableString(f.VertragsformBestaetigtVon),
		boolInt(f.VierAugen), boolInt(f.Aktiv), f.GeaendertAm, f.GeaendertVon, f.ID)
	if istUniqueFehler(err) {
		return f, ErrSchleuseFreigabeDoppelt
	}
	if err != nil {
		return f, fmt.Errorf("schleuse-freigaben.aendern: %w", err)
	}
	// Verglichen wird die Normalform (KC-4): Ein Altwert in anderer
	// Schreibweise ist dasselbe Gegenüber und kostet niemanden den Schlüssel.
	if alt.Anbieter != f.Anbieter || schleuse.NormalformOderRoh(alt.BasisURL) != schleuse.NormalformOderRoh(f.BasisURL) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schleuse_schluessel WHERE freigabe_id = ?`, f.ID); err != nil {
			return f, fmt.Errorf("schleuse-freigaben.aendern schlüssel: %w", err)
		}
	}
	if err := schleuseAudit(ctx, tx, "schleuse-freigabe:"+f.ID, adminID, AuditActionSchleuseFreigabeAendern, alt.auditText(), f.auditText()); err != nil {
		return f, fmt.Errorf("schleuse-freigaben.aendern audit: %w", err)
	}
	return f, tx.Commit()
}

// Loeschen entfernt einen Eintrag samt allen Schlüsseln, die Personen dafür
// hinterlegt haben. Der Fremdschlüssel räumt sie ohnehin ab; das ausdrückliche
// DELETE hängt nicht davon ab, dass foreign_keys an ist.
func (r *SchleuseFreigabeRepo) Loeschen(ctx context.Context, id, adminID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("schleuse-freigaben.loeschen begin: %w", err)
	}
	defer tx.Rollback()
	alt, err := scanFreigabe(tx.QueryRowContext(ctx, `SELECT `+freigabeSpalten+` FROM schleuse_freigaben WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSchleuseFreigabeNichtGefunden
	}
	if err != nil {
		return fmt.Errorf("schleuse-freigaben.loeschen lesen: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schleuse_schluessel WHERE freigabe_id = ?`, id); err != nil {
		return fmt.Errorf("schleuse-freigaben.loeschen schlüssel: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schleuse_freigaben WHERE id = ?`, id); err != nil {
		return fmt.Errorf("schleuse-freigaben.loeschen: %w", err)
	}
	if err := schleuseAudit(ctx, tx, "schleuse-freigabe:"+id, adminID, AuditActionSchleuseFreigabeLoeschen, alt.auditText(), ""); err != nil {
		return fmt.Errorf("schleuse-freigaben.loeschen audit: %w", err)
	}
	return tx.Commit()
}

// ── Schlüssel je Person ─────────────────────────────────────────────────

// SchleuseSchluesselMeta beschreibt einen hinterlegten Schlüssel OHNE Klartext.
type SchleuseSchluesselMeta struct {
	FreigabeID                  string `json:"freigabe_id"`
	Anbieter                    string `json:"anbieter"`
	Bezeichnung                 string `json:"bezeichnung"`
	ErstelltAm                  string `json:"erstellt_am"`
	GeschaeftskontoBestaetigtAm string `json:"geschaeftskonto_bestaetigt_am"`
}

// SchleuseSchluesselRepo verwaltet schleuse_schluessel. box darf nicht nil
// sein; ohne Master-Key bringt main.go das Repo nicht auf (Routen → 503).
type SchleuseSchluesselRepo struct {
	db  *sql.DB
	box *secretcrypto.Box
}

// NewSchleuseSchluesselRepo legt das Repo an. box muss != nil sein.
func NewSchleuseSchluesselRepo(db *sql.DB, box *secretcrypto.Box) *SchleuseSchluesselRepo {
	return &SchleuseSchluesselRepo{db: db, box: box}
}

// List liefert die Schlüssel einer Person — Anbieter und Datum, NIE den Schlüssel.
func (r *SchleuseSchluesselRepo) List(ctx context.Context, userID string) ([]SchleuseSchluesselMeta, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.freigabe_id, f.anbieter, f.bezeichnung, s.erstellt_am, s.geschaeftskonto_bestaetigt_am
		   FROM schleuse_schluessel s JOIN schleuse_freigaben f ON f.id = s.freigabe_id
		  WHERE s.user_id = ? ORDER BY f.anbieter, f.bezeichnung`, userID)
	if err != nil {
		return nil, fmt.Errorf("schleuse-schlüssel.list: %w", err)
	}
	defer rows.Close()
	out := []SchleuseSchluesselMeta{}
	for rows.Next() {
		var m SchleuseSchluesselMeta
		if err := rows.Scan(&m.FreigabeID, &m.Anbieter, &m.Bezeichnung, &m.ErstelltAm, &m.GeschaeftskontoBestaetigtAm); err != nil {
			return nil, fmt.Errorf("schleuse-schlüssel.list scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SchleuseStand ist der Stand einer Freigabe, den die Person im Dialog
// gesehen hat — das Gegenüber, für das sie ihren Schlüssel einträgt.
type SchleuseStand struct {
	Anbieter string
	BasisURL string
}

// Set legt den Schlüssel einer Person für eine Freigabe an oder ersetzt ihn.
// Die Bestätigung „geschäftliches Konto“ hat der Aufrufer verlangt; ihr
// Zeitpunkt wird bei jedem Setzen neu geschrieben.
//
// In DERSELBEN Transaktion wie das Schreiben prüft Set, dass die Freigabe
// noch aktiv und bestätigt ist und noch auf das Gegenüber zeigt, das die
// Person gesehen hat (stand). Sonst landete ein Schlüssel, der für
// api.anthropic.com gedacht war, bei einer inzwischen umgestellten
// Basis-URL — Aendern löscht die alten Schlüssel ja gerade deshalb (KC-2).
// Fehler: ErrSchleuseFreigabeNichtGefunden, ErrSchleuseNichtFreigegeben,
// ErrSchleuseStandVeraltet.
func (r *SchleuseSchluesselRepo) Set(ctx context.Context, userID, freigabeID, key string, stand SchleuseStand) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("schleuse-schlüssel.set: leere user_id")
	}
	enc, err := r.box.SealString(key)
	if err != nil {
		return fmt.Errorf("schleuse-schlüssel.set seal: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("schleuse-schlüssel.set begin: %w", err)
	}
	defer tx.Rollback()
	// Erst die Schreibsperre nehmen (Leer-UPDATE), DANN lesen: So kann kein
	// Aendern zwischen Prüfung und Schreiben committen — die Prüfung gilt für
	// genau den Stand, auf den geschrieben wird. Ohne das liefe ein
	// gleichzeitiges Aendern in SQLITE_BUSY_SNAPSHOT statt in ein sauberes 409.
	if _, err := tx.ExecContext(ctx, `UPDATE schleuse_freigaben SET geaendert_am = geaendert_am WHERE id = ?`, freigabeID); err != nil {
		return fmt.Errorf("schleuse-schlüssel.set sperre: %w", err)
	}
	f, err := scanFreigabe(tx.QueryRowContext(ctx, `SELECT `+freigabeSpalten+` FROM schleuse_freigaben WHERE id = ?`, freigabeID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSchleuseFreigabeNichtGefunden
	} else if err != nil {
		return fmt.Errorf("schleuse-schlüssel.set freigabe: %w", err)
	}
	if !f.Aktiv || !f.VertragsformBestaetigt {
		return ErrSchleuseNichtFreigegeben
	}
	if stand.Anbieter != f.Anbieter || schleuse.NormalformOderRoh(stand.BasisURL) != schleuse.NormalformOderRoh(f.BasisURL) {
		return ErrSchleuseStandVeraltet
	}
	jetzt := nowISO()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schleuse_schluessel (user_id, freigabe_id, key_encrypted, geschaeftskonto_bestaetigt_am, erstellt_am)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, freigabe_id) DO UPDATE SET key_encrypted = excluded.key_encrypted,
		   geschaeftskonto_bestaetigt_am = excluded.geschaeftskonto_bestaetigt_am, erstellt_am = excluded.erstellt_am`,
		userID, freigabeID, enc, jetzt, jetzt); err != nil {
		return fmt.Errorf("schleuse-schlüssel.set: %w", err)
	}
	return tx.Commit()
}

// Get liefert den Klartext — NUR für den serverseitigen Versand (S3). Nie
// loggen, nie an den Browser.
func (r *SchleuseSchluesselRepo) Get(ctx context.Context, userID, freigabeID string) (string, error) {
	var enc string
	err := r.db.QueryRowContext(ctx,
		`SELECT key_encrypted FROM schleuse_schluessel WHERE user_id = ? AND freigabe_id = ?`,
		userID, freigabeID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSchleuseSchluesselNichtGefunden
	}
	if err != nil {
		return "", fmt.Errorf("schleuse-schlüssel.get: %w", err)
	}
	plain, err := r.box.OpenString(enc)
	if err != nil {
		return "", fmt.Errorf("schleuse-schlüssel.get decrypt: %w", err)
	}
	return plain, nil
}

// Delete entfernt einen Schlüssel (idempotent).
func (r *SchleuseSchluesselRepo) Delete(ctx context.Context, userID, freigabeID string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM schleuse_schluessel WHERE user_id = ? AND freigabe_id = ?`, userID, freigabeID); err != nil {
		return fmt.Errorf("schleuse-schlüssel.delete: %w", err)
	}
	return nil
}

// ── Board-Sperre ────────────────────────────────────────────────────────

// SchleuseBoardSperre liest boards.schleuse_gesperrt (ADR-0134 §2).
// ErrBoardNotFound, wenn es das Board nicht gibt.
func SchleuseBoardSperre(ctx context.Context, db *sql.DB, boardID string) (bool, error) {
	var an int
	err := db.QueryRowContext(ctx, `SELECT schleuse_gesperrt FROM boards WHERE id = ?`, boardID).Scan(&an)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrBoardNotFound
	}
	if err != nil {
		return false, fmt.Errorf("schleuse.board-sperre: %w", err)
	}
	return an == 1, nil
}

// SetzeSchleuseBoardSperre stellt die Sperre eines Boards, mit Audit (wer,
// wann). Die Owner-Prüfung macht der Aufrufer.
func SetzeSchleuseBoardSperre(ctx context.Context, db *sql.DB, boardID string, gesperrt bool, userID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("schleuse.setze-board-sperre begin: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE boards SET schleuse_gesperrt = ? WHERE id = ?`, boolInt(gesperrt), boardID)
	if err != nil {
		return fmt.Errorf("schleuse.setze-board-sperre: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBoardNotFound
	}
	if err := schleuseAudit(ctx, tx, "schleuse-board-sperre:"+boardID, userID, AuditActionSchleuseBoardSperre, "", fmt.Sprint(gesperrt)); err != nil {
		return fmt.Errorf("schleuse.setze-board-sperre audit: %w", err)
	}
	return tx.Commit()
}
