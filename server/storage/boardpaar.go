package storage

// boardpaar.go — Board-Paar intern/extern (ADR-0140, Karten #778/#788).
//
// Drei Dinge liegen hier:
//
//   - die Partnerliste innen (`boardpaar_innen`): welche internen Boards ihre
//     Schatten über den Ausgang abgeben dürfen;
//   - die Partnerliste außen (`boardpaar_aussen`, Karte #788): welche externen
//     Boards Schatten annehmen, mit der Adresse des internen Gegenstücks;
//   - die Zugänge des Board-Paars (`boardpaar_tokens`): Typ `abs`
//     (Schatten-Ausgang innen, Präfix abs_) und `abe` (Schatten-Eingang außen,
//     Präfix abe_), `abr` (Rückweg innen, Präfix abr_) und `abb`
//     (Sicherungen lesen, Präfix abb_, ADR-0142). Instanzweit, ohne Person und
//     ohne Board, nur der Hash wird gespeichert (wie kalender_ausgang_tokens).
//
// Ein eigener Präfix je Typ, damit der Server ein Token auf jedem fremden Weg
// erkennt und mit 403 abweist, statt es als unbekanntes Agenten-Token zu
// zählen (api.BoardpaarSchranke).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Typen der Board-Paar-Zugänge und ihre Präfixe. abr (Rückweg, ADR-0141
// §5.3, Migration 0040) gibt es nur innen: Ziele melden, Aufträge
// quittieren, ab Schritt 2c Rückweg-Einträge anwenden. abb (Sicherungen
// lesen, ADR-0142, Migration 0043) gilt unabhängig von boardpaar.seite: nur
// die fertigen, verschlüsselten Board-Sicherungen abholen.
const (
	BoardpaarTypAusgang     = "abs"
	BoardpaarTypEingang     = "abe"
	BoardpaarTypRueckweg    = "abr"
	BoardpaarTypSicherungen = "abb"

	SchattenAusgangPraefix = "abs_"
	SchattenEingangPraefix = "abe_"
	RueckwegPraefix        = "abr_"
	SicherungenPraefix     = "abb_"
)

var (
	ErrBoardpaarTokenUnbekannt  = errors.New("storage: board-paar-token unbekannt oder widerrufen")
	ErrBoardpaarTokenName       = errors.New("storage: ungültiger name für einen board-paar-zugang")
	ErrBoardpaarTokenVergeben   = errors.New("storage: name hat schon einen aktiven board-paar-zugang dieses typs")
	ErrBoardpaarTyp             = errors.New("storage: unbekannter typ eines board-paar-zugangs")
	ErrBoardpaarNichtGekoppelt  = errors.New("storage: board ist nicht gekoppelt")
	ErrBoardpaarBoardUnbekannt  = errors.New("storage: board gibt es auf diesem server nicht")
	ErrBoardpaarNichtFreigebbar = errors.New("storage: das service-board ist nicht koppelbar")
	ErrBoardpaarLinkBasis       = errors.New("storage: adresse des internen boards ungültig")
	ErrBoardpaarBoardFehlt      = errors.New("storage: board-id fehlt")
	// ErrBoardpaarAnlegeRecht: „darf Kopien anlegen“ gibt es nur für abe
	// (ADR-0141 §3, CHECK in Migration 0040).
	ErrBoardpaarAnlegeRecht = errors.New("storage: das recht „darf kopien anlegen“ gibt es nur für abe")
	// ErrBoardpaarIstKopie: Das Board trägt Spiegel-Marken oder die Herkunft
	// „Kopie“ (externe Kopie, Migration 0042) und lässt sich nicht von Hand
	// koppeln — Kopien entstehen über einen Auftrag.
	ErrBoardpaarIstKopie = errors.New("storage: das board ist eine externe kopie; von hand koppeln geht nicht — neue kopie über einen auftrag anlegen")
)

// boardpaarPraefix liefert den Präfix eines Typs ("" bei unbekanntem Typ).
func boardpaarPraefix(typ string) string {
	switch typ {
	case BoardpaarTypAusgang:
		return SchattenAusgangPraefix
	case BoardpaarTypEingang:
		return SchattenEingangPraefix
	case BoardpaarTypRueckweg:
		return RueckwegPraefix
	case BoardpaarTypSicherungen:
		return SicherungenPraefix
	}
	return ""
}

// BoardpaarPraefix: exportierte Fassung von boardpaarPraefix (für die
// Token-Prüfung am Handler, api.boardpaarZugang).
func BoardpaarPraefix(typ string) string { return boardpaarPraefix(typ) }

// BoardpaarTypAusPraefix liefert den Typ eines Klartexts nach seinem Präfix
// ("" = kein Board-Paar-Token). Nur der Präfix, nicht geprüft.
func BoardpaarTypAusPraefix(klartext string) string {
	for _, typ := range []string{BoardpaarTypAusgang, BoardpaarTypEingang, BoardpaarTypRueckweg, BoardpaarTypSicherungen} {
		if strings.HasPrefix(klartext, boardpaarPraefix(typ)) {
			return typ
		}
	}
	return ""
}

// BoardpaarToken ist ein Eintrag der Liste (ohne Geheimnis).
type BoardpaarToken struct {
	ID         string `json:"id"`
	Typ        string `json:"typ"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	CreatedBy  string `json:"created_by,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	// Erneuerung (Karte #796, Migration 0037). GiltBis: Ende der
	// Übergangszeit eines erneuerten Tokens (danach tot). ErneuertAus/
	// ErneuertVon: am Nachfolger der Vorgänger und die Quelladresse.
	GiltBis     string `json:"gilt_bis,omitempty"`
	ErneuertAus string `json:"erneuert_aus,omitempty"`
	ErneuertVon string `json:"erneuert_von,omitempty"`
	// DarfAnlegen (ADR-0141 §3, Migration 0040): Nur ein abe_ mit diesem
	// Recht legt außen Kopien an. Geht beim Erneuern auf den Nachfolger über.
	DarfAnlegen bool `json:"darf_anlegen"`
}

// BoardpaarInnen ist ein Eintrag der Partnerliste innen.
type BoardpaarInnen struct {
	BoardID string `json:"board_id"`
	Seit    string `json:"seit"`
	Von     string `json:"von,omitempty"`
	// LetzteAbfrage/LetzterFehlgrund (F-2019, Migration 0036): letzter Abruf
	// des Ausgangs für dieses Board; Fehlgrund leer nach einem Erfolg.
	LetzteAbfrage    string `json:"letzte_abfrage,omitempty"`
	LetzterFehlgrund string `json:"letzter_fehlgrund,omitempty"`
}

// BoardpaarRepo verwaltet boardpaar_innen, boardpaar_aussen und boardpaar_tokens.
type BoardpaarRepo struct {
	db *sql.DB
}

func NewBoardpaarRepo(db *sql.DB) *BoardpaarRepo {
	return &BoardpaarRepo{db: db}
}

// TokenAnlegen erzeugt ein Token des Typs und gibt den Klartext GENAU EINMAL
// zurück. Namen wie beim Kalender-Ausgang (1–64 Zeichen aus Buchstaben,
// Ziffern, . _ -; erscheint in Log-Zeilen).
func (r *BoardpaarRepo) TokenAnlegen(ctx context.Context, typ, name, by string) (string, *BoardpaarToken, error) {
	return r.TokenAnlegenMit(ctx, typ, name, by, false)
}

// TokenAnlegenMit wie TokenAnlegen, mit dem Recht „darf Kopien anlegen“
// (ADR-0141 §3). Das Recht gibt es nur für abe → sonst ErrBoardpaarAnlegeRecht.
func (r *BoardpaarRepo) TokenAnlegenMit(ctx context.Context, typ, name, by string, darfAnlegen bool) (string, *BoardpaarToken, error) {
	praefix := boardpaarPraefix(typ)
	if praefix == "" {
		return "", nil, ErrBoardpaarTyp
	}
	if darfAnlegen && typ != BoardpaarTypEingang {
		return "", nil, ErrBoardpaarAnlegeRecht
	}
	name = strings.TrimSpace(name)
	if !gueltigerAusgangName(name) {
		return "", nil, ErrBoardpaarTokenName
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("boardpaar: rand: %w", err)
	}
	klartext := praefix + base64.RawURLEncoding.EncodeToString(buf)
	t := &BoardpaarToken{ID: uuid.NewString(), Typ: typ, Name: name, CreatedAt: nowISO(), CreatedBy: by, DarfAnlegen: darfAnlegen}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO boardpaar_tokens (id, typ, name, token_hash, created_at, created_by, darf_anlegen) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Typ, t.Name, hashToken(klartext), t.CreatedAt, t.CreatedBy, bit(darfAnlegen))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", nil, ErrBoardpaarTokenVergeben
		}
		return "", nil, fmt.Errorf("boardpaar.token-anlegen: %w", err)
	}
	return klartext, t, nil
}

// TokenPruefe schlägt einen Klartext des Typs nach. Nur AKTIVE Tokens des
// passenden Typs zählen; ein abe_-Token öffnet den Ausgang also nicht, auch
// wenn sein Hash bekannt ist. Ein erneuertes Token gilt bis zum Ende seiner
// Übergangszeit (Karte #796). Aktualisiert last_used_at best-effort.
func (r *BoardpaarRepo) TokenPruefe(ctx context.Context, typ, klartext string) (*BoardpaarToken, error) {
	praefix := boardpaarPraefix(typ)
	if praefix == "" || !strings.HasPrefix(klartext, praefix) {
		return nil, ErrBoardpaarTokenUnbekannt
	}
	t := BoardpaarToken{Typ: typ}
	var darf int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, created_at, created_by, coalesce(gilt_bis, ''), darf_anlegen FROM boardpaar_tokens
		 WHERE token_hash = ? AND typ = ? AND revoked_at IS NULL`, hashToken(klartext), typ).
		Scan(&t.ID, &t.Name, &t.CreatedAt, &t.CreatedBy, &t.GiltBis, &darf)
	t.DarfAnlegen = darf == 1
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardpaarTokenUnbekannt
	}
	if err != nil {
		return nil, fmt.Errorf("boardpaar.token-pruefe: %w", err)
	}
	if t.GiltBis != "" {
		// Verify-Lauf Sprint 90, F2: Ein Vorgänger in der Übergangszeit bei
		// ungestellter Uhr ist „später“, nicht „unbekannt“ — dieselbe Antwort
		// wie am Erneuern-Weg (503), sonst schlüge das Skript nach einem
		// Stromausfall in der Übergangszeit Alarm.
		if !uhrGestellt() {
			return nil, ErrBoardpaarUhr
		}
		if !tokenGilt("", t.GiltBis, timeNow(), true) {
			return nil, ErrBoardpaarTokenUnbekannt
		}
	}
	_, _ = r.db.ExecContext(ctx, `UPDATE boardpaar_tokens SET last_used_at = ? WHERE id = ?`, nowISO(), t.ID)
	r.nachfolgerAufraeumen(ctx)
	return &t, nil
}

// TokenListe liefert alle Tokens (aktive und widerrufene), neueste zuerst.
func (r *BoardpaarRepo) TokenListe(ctx context.Context) ([]BoardpaarToken, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, typ, name, created_at, created_by, coalesce(last_used_at, ''), coalesce(revoked_at, ''),
		        coalesce(gilt_bis, ''), coalesce(erneuert_aus, ''), coalesce(erneuert_von, ''), darf_anlegen
		 FROM boardpaar_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.token-liste: %w", err)
	}
	defer rows.Close()
	out := []BoardpaarToken{}
	for rows.Next() {
		var t BoardpaarToken
		var darf int
		if err := rows.Scan(&t.ID, &t.Typ, &t.Name, &t.CreatedAt, &t.CreatedBy, &t.LastUsedAt, &t.RevokedAt,
			&t.GiltBis, &t.ErneuertAus, &t.ErneuertVon, &darf); err != nil {
			return nil, fmt.Errorf("boardpaar.token-liste scan: %w", err)
		}
		t.DarfAnlegen = darf == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// TokenWiderrufen setzt revoked_at eines nicht widerrufenen Tokens — auch
// eines erneuerten in seiner Übergangszeit (Notaus). Mit ihm fallen die
// Vorgänger, die noch auf es zeigen (Karte #796): Wer das aktuelle Token
// widerruft, will die ganze Kette tot, nicht das alte noch zehn Minuten
// gültig. Unbekannt oder schon widerrufen → ErrBoardpaarTokenUnbekannt.
func (r *BoardpaarRepo) TokenWiderrufen(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("boardpaar.token-widerrufen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	jetzt := nowISO()
	res, err := tx.ExecContext(ctx,
		`UPDATE boardpaar_tokens SET revoked_at = ?, nachfolger_enc = NULL WHERE id = ? AND revoked_at IS NULL`, jetzt, id)
	if err != nil {
		return fmt.Errorf("boardpaar.token-widerrufen: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBoardpaarTokenUnbekannt
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE boardpaar_tokens SET revoked_at = ?, nachfolger_enc = NULL WHERE nachfolger_id = ? AND revoked_at IS NULL`,
		jetzt, id); err != nil {
		return fmt.Errorf("boardpaar.token-widerrufen vorgänger: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("boardpaar.token-widerrufen commit: %w", err)
	}
	return nil
}

// InnenKoppeln nimmt ein internes Board in die Partnerliste auf. Schon
// gekoppelt → unverändert, kein Fehler. Das Service-Board ist nie
// freigebbar (ADR-0140 §2) → ErrBoardpaarNichtFreigebbar; ein unbekanntes
// Board → ErrBoardpaarBoardUnbekannt (Karte #788: lesbar statt
// Fremdschlüsselfehler).
func (r *BoardpaarRepo) InnenKoppeln(ctx context.Context, boardID, von string) error {
	if err := r.koppelbar(ctx, boardID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO boardpaar_innen (board_id, seit, von) VALUES (?, ?, ?) ON CONFLICT(board_id) DO NOTHING`,
		boardID, nowISO(), von)
	if err != nil {
		return koppelSchreibfehler("innen-koppeln", err)
	}
	return nil
}

// InnenEntkoppeln entfernt ein Board aus der Partnerliste innen. Nicht
// gekoppelt → ErrBoardpaarNichtGekoppelt.
//
// Alle Aufträge „Kopie anlegen“ dieses Boards fallen mit: offene (Kritik #825
// A-3 — sonst wären sie nach einem erneuten Koppeln ohne neuen Admin-Akt
// wieder abholbar) und erledigte (Verify Sprint 93 V-4 — ein erledigter
// Auftrag öffnet den Hinweg v2 mit vertraulichen Titeln; nach einem Koppeln
// von Hand als Stufe-1-Board darf er das nicht mehr, ADR-0141 Punkt 6).
func (r *BoardpaarRepo) InnenEntkoppeln(ctx context.Context, boardID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("boardpaar.innen-entkoppeln: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM boardpaar_innen WHERE board_id = ?`, boardID)
	if err != nil {
		return fmt.Errorf("boardpaar.innen-entkoppeln: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBoardpaarNichtGekoppelt
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM boardpaar_auftraege WHERE board_id = ?`, boardID); err != nil {
		return fmt.Errorf("boardpaar.innen-entkoppeln: aufträge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("boardpaar.innen-entkoppeln: %w", err)
	}
	return nil
}

// InnenGekoppelt: Steht das Board in der Partnerliste innen? Das
// Service-Board ist es nie, auch wenn eine Zeile von Hand eingetragen wäre.
func (r *BoardpaarRepo) InnenGekoppelt(ctx context.Context, boardID string) (bool, error) {
	if boardID == "" || boardID == ServiceBoardID {
		return false, nil
	}
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM boardpaar_innen WHERE board_id = ?`, boardID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("boardpaar.innen-gekoppelt: %w", err)
	}
	return n > 0, nil
}

// InnenListe liefert die Partnerliste innen, nach Board-ID sortiert.
func (r *BoardpaarRepo) InnenListe(ctx context.Context) ([]BoardpaarInnen, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT board_id, seit, von, COALESCE(letzte_abfrage, ''), COALESCE(letzter_fehlgrund, '')
		FROM boardpaar_innen ORDER BY board_id`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.innen-liste: %w", err)
	}
	defer rows.Close()
	out := []BoardpaarInnen{}
	for rows.Next() {
		var e BoardpaarInnen
		if err := rows.Scan(&e.BoardID, &e.Seit, &e.Von, &e.LetzteAbfrage, &e.LetzterFehlgrund); err != nil {
			return nil, fmt.Errorf("boardpaar.innen-liste scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// gueltigerAusgangName: Name eines Zugangs, 1–64 Zeichen aus Buchstaben,
// Ziffern, . _ -; erscheint in Log-Zeilen. Lag bis Karte #795 beim
// Kalender-Ausgang der Außenstelle (abk_), der zurückgebaut ist.
func gueltigerAusgangName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// koppelbar prüft ein Board vor dem Koppeln (beide Seiten): nicht leer,
// nicht das Service-Board, auf diesem Server vorhanden.
func (r *BoardpaarRepo) koppelbar(ctx context.Context, boardID string) error {
	if boardID == "" {
		return ErrBoardpaarBoardFehlt
	}
	if boardID == ServiceBoardID {
		return ErrBoardpaarNichtFreigebbar
	}
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM boards WHERE id = ?`, boardID).Scan(&n); err != nil {
		return fmt.Errorf("boardpaar.koppelbar: %w", err)
	}
	if n == 0 {
		return ErrBoardpaarBoardUnbekannt
	}
	return nil
}

// koppelSchreibfehler: Wird das Board zwischen der Prüfung und dem INSERT
// gelöscht, scheitert der Fremdschlüssel — das ist ein unbekanntes Board,
// kein Serverfehler (Kritiker #788, K-6).
func koppelSchreibfehler(wo string, err error) error {
	if strings.Contains(err.Error(), "FOREIGN KEY") {
		return ErrBoardpaarBoardUnbekannt
	}
	return fmt.Errorf("boardpaar.%s: %w", wo, err)
}

// TokenAktivNachName liefert das AKTUELLE Token eines Typs mit diesem Namen
// (für den Widerruf per CLI). Der Name ist nur unter den Tokens ohne Widerruf
// und ohne Erneuerung eindeutig (Teilindex boardpaar_tokens_name_aktiv) —
// widerrufene und erneuerte Vorgänger zählen nicht; ein Vorgänger in der
// Übergangszeit fällt beim Widerruf des aktuellen mit (TokenWiderrufen).
// Kein aktuelles → ErrBoardpaarTokenUnbekannt.
func (r *BoardpaarRepo) TokenAktivNachName(ctx context.Context, typ, name string) (*BoardpaarToken, error) {
	if boardpaarPraefix(typ) == "" {
		return nil, ErrBoardpaarTyp
	}
	t := BoardpaarToken{Typ: typ}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, created_at, created_by, coalesce(last_used_at, '') FROM boardpaar_tokens
		 WHERE typ = ? AND name = ? AND revoked_at IS NULL AND gilt_bis IS NULL`, typ, strings.TrimSpace(name)).
		Scan(&t.ID, &t.Name, &t.CreatedAt, &t.CreatedBy, &t.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardpaarTokenUnbekannt
	}
	if err != nil {
		return nil, fmt.Errorf("boardpaar.token-nach-name: %w", err)
	}
	return &t, nil
}

// BoardpaarAussen ist ein Eintrag der Partnerliste außen (ADR-0140 §4).
// InnenLinkBasis ist nur für den Menschen-Link da und geht nie an Agenten.
type BoardpaarAussen struct {
	BoardID        string `json:"board_id"`
	InnenLinkBasis string `json:"innen_link_basis"`
	Seit           string `json:"seit"`
	Von            string `json:"von,omitempty"`
	// LetzterEingang/LetzterFehlgrund (F-2019, Migration 0036): letzter
	// Aufruf des Eingangs für dieses Board; Fehlgrund leer nach einem Erfolg.
	LetzterEingang   string `json:"letzter_eingang,omitempty"`
	LetzterFehlgrund string `json:"letzter_fehlgrund,omitempty"`
	// Kopie (ADR-0141 §3, W2-1, Migration 0040): Das Board ist über
	// POST /agent/boardpaar/boards als Kopie entstanden. Hinweg v2 gilt nur
	// dort, der alte Schatten-Eingang nur bei false.
	Kopie bool `json:"kopie"`
	// Bearbeitbar (ADR-0141 Nachtrag #836, Migration 0044): Auf dieser Kopie
	// darf außen bearbeitet werden — wirksam nur zusammen mit
	// boardpaar.rueckweg = an. Nur bei Kopie bedeutsam; Voreinstellung false.
	Bearbeitbar bool `json:"bearbeitbar"`
	// OffeneAenderungen (F-2240): unbestätigtes Journal dieser Kopie — nur in der Admin-Liste gefüllt, für die Warnung vor
	// „Kopie außen löschen“.
	OffeneAenderungen int `json:"offene_aenderungen"`
}

// innenLinkBasisMax begrenzt die gespeicherte Adresse (sie landet in jedem
// Menschen-Link auf einen Schatten).
const innenLinkBasisMax = 512

// PruefeInnenLinkBasis prüft die Adresse des internen Gegenstücks und
// liefert ihre Normalform. Erwartet wird das Deep-Link-Format von aBoard
// (src/deeplink.js): https://host[:port][/pfad]#board=<id> — https, ohne
// Zugangsdaten, ohne Abfrage, und im Fragment genau der Schlüssel `board`
// mit einem Wert aus Buchstaben, Ziffern, . _ ~ -. Der Link auf eine Karte entsteht durch Anhängen von
// `&card=<nr>`, deshalb darf das Fragment nichts anderes tragen.
func PruefeInnenLinkBasis(roh string) (string, error) {
	roh = strings.TrimSpace(roh)
	fehler := func(grund string) (string, error) {
		return "", fmt.Errorf("%w: %s (erwartet https://host/#board=<id>)", ErrBoardpaarLinkBasis, grund)
	}
	if roh == "" {
		return fehler("leer")
	}
	if len(roh) > innenLinkBasisMax {
		return fehler("zu lang")
	}
	u, err := url.Parse(roh)
	if err != nil {
		return fehler("keine adresse")
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return fehler("nur https mit host, ohne zugangsdaten")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fehler("ohne abfrage")
	}
	werte, err := url.ParseQuery(u.EscapedFragment())
	if err != nil || len(werte) != 1 || len(werte["board"]) != 1 {
		return fehler("fragment muss genau board=<id> sein")
	}
	// Board-IDs von aBoard sind UUIDs oder kurze Namen (default, service).
	// Eng gefasst, damit `#`, `&` oder `=` in der ID den Kartenlink nicht
	// ins Leere führen (Kritiker #788, K-1).
	id := werte["board"][0]
	if id == "" || len(id) > 128 || strings.IndexFunc(id, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-')
	}) >= 0 {
		return fehler("board-id: 1–128 Zeichen aus Buchstaben, Ziffern, . _ ~ -")
	}
	if p := u.Port(); p != "" {
		if n, perr := strconv.Atoi(p); perr != nil || n < 1 || n > 65535 {
			return fehler("port ungültig")
		}
	}
	pfad := u.EscapedPath()
	if pfad == "" {
		pfad = "/"
	}
	return "https://" + u.Host + pfad + "#board=" + url.QueryEscape(id), nil
}

// AussenKoppeln nimmt ein externes Board in die Partnerliste außen auf oder
// ändert die Adresse des internen Gegenstücks (seit/von bleiben dann). Das
// Service-Board ist nie koppelbar, ein unbekanntes Board scheitert lesbar.
func (r *BoardpaarRepo) AussenKoppeln(ctx context.Context, boardID, linkBasis, von string) (*BoardpaarAussen, error) {
	if err := r.koppelbar(ctx, boardID); err != nil {
		return nil, err
	}
	// Kritik #825 A-2: Ein Board mit Spiegel-Marken ist eine Kopie (ADR-0141
	// §3). Nach dem Entkoppeln fehlt seine Zeile; ein Koppeln von Hand legte
	// sie mit kopie = 0 neu an — ein Stufe-1-Board mit Kopie-Struktur, dessen
	// Hinweg v2 dann 404 bekäme. Eine bestehende Zeile behält kopie ohnehin
	// (ON CONFLICT ändert nur die Adresse).
	// Sitzungs-Verify V-1 (#831): Auch ein Board mit Herkunft „Kopie“
	// (boardpaar_kopie_herkunft, Migration 0042) ist abgelehnt — die Marken
	// kann der Owner per PUT entfernen, die Herkunft nicht. Sonst würde aus
	// der entkoppelten Kopie ein Stufe-1-Board (kopie = 0), und nach erneutem
	// Entkoppeln wäre es wieder eine „entkoppelte Kopie“ für Notweg und
	// Wiederaufnahme.
	var state []byte
	var kopie sql.NullInt64
	var herkunft int
	if err := r.db.QueryRowContext(ctx, `SELECT b.state_json, a.kopie,
		EXISTS (SELECT 1 FROM boardpaar_kopie_herkunft h WHERE h.board_id = b.id)
		FROM boards b LEFT JOIN boardpaar_aussen a ON a.aussen_board_id = b.id WHERE b.id = ?`, boardID).Scan(&state, &kopie, &herkunft); err != nil {
		return nil, koppelSchreibfehler("aussen-koppeln", err)
	}
	if !kopie.Valid && (herkunft == 1 || traegtSpiegelMarken(state)) {
		return nil, ErrBoardpaarIstKopie
	}
	norm, err := PruefeInnenLinkBasis(linkBasis)
	if err != nil {
		return nil, err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO boardpaar_aussen (aussen_board_id, innen_link_basis, seit, von) VALUES (?, ?, ?, ?)
		 ON CONFLICT(aussen_board_id) DO UPDATE SET innen_link_basis = excluded.innen_link_basis`,
		boardID, norm, nowISO(), von)
	if err != nil {
		return nil, koppelSchreibfehler("aussen-koppeln", err)
	}
	return r.aussenEintrag(ctx, boardID)
}

func (r *BoardpaarRepo) aussenEintrag(ctx context.Context, boardID string) (*BoardpaarAussen, error) {
	var e BoardpaarAussen
	var kopie, bearbeitbar int
	err := r.db.QueryRowContext(ctx,
		`SELECT aussen_board_id, innen_link_basis, seit, von, kopie, bearbeitbar FROM boardpaar_aussen WHERE aussen_board_id = ?`, boardID).
		Scan(&e.BoardID, &e.InnenLinkBasis, &e.Seit, &e.Von, &kopie, &bearbeitbar)
	e.Kopie = kopie == 1
	e.Bearbeitbar = e.Kopie && bearbeitbar == 1
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardpaarNichtGekoppelt
	}
	if err != nil {
		return nil, fmt.Errorf("boardpaar.aussen-eintrag: %w", err)
	}
	return &e, nil
}

// traegtSpiegelMarken: Steht an einer Lane oder Spalte des Zustands eine
// Marke `spiegel` (ADR-0141 §4)? Über das JSON, nicht über eine Zeichenfolge —
// ein Zustand aus einem Ganz-PUT kann anders formatiert sein (Verify #825 V-2).
// Ein nicht lesbarer Zustand gilt als markiert (fail closed: lieber ein
// abgelehntes Koppeln als ein Stufe-1-Board mit Kopie-Struktur).
func traegtSpiegelMarken(state []byte) bool {
	markiert, lesbar := spiegelMarken(state)
	return markiert || !lesbar
}

// spiegelMarken liest die Marken; lesbar = false, wenn der Zustand nicht in
// die erwartete Form passt.
func spiegelMarken(state []byte) (markiert, lesbar bool) {
	var stand struct {
		Board struct {
			Sprints    []map[string]json.RawMessage `json:"sprints"`
			ColumnDefs []map[string]json.RawMessage `json:"columnDefs"`
		} `json:"board"`
	}
	if json.Unmarshal(state, &stand) != nil {
		return false, false
	}
	for _, liste := range [][]map[string]json.RawMessage{stand.Board.Sprints, stand.Board.ColumnDefs} {
		for _, o := range liste {
			if _, ok := o["spiegel"]; ok {
				return true, true
			}
		}
	}
	return false, true
}

// AussenEntkoppeln entfernt ein Board aus der Partnerliste außen. Ab #779
// löscht der Fremdschlüssel damit auch seine Schatten (ADR-0140 §1). Nicht
// gekoppelt → ErrBoardpaarNichtGekoppelt.
func (r *BoardpaarRepo) AussenEntkoppeln(ctx context.Context, boardID string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM boardpaar_aussen WHERE aussen_board_id = ?`, boardID)
	if err != nil {
		return fmt.Errorf("boardpaar.aussen-entkoppeln: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBoardpaarNichtGekoppelt
	}
	return nil
}

// AussenGekoppelt liefert den Eintrag eines gekoppelten externen Boards
// oder nil. Das Service-Board ist es nie, auch wenn eine Zeile von Hand
// eingetragen wäre.
func (r *BoardpaarRepo) AussenGekoppelt(ctx context.Context, boardID string) (*BoardpaarAussen, error) {
	if boardID == "" || boardID == ServiceBoardID {
		return nil, nil
	}
	e, err := r.aussenEintrag(ctx, boardID)
	if errors.Is(err, ErrBoardpaarNichtGekoppelt) {
		return nil, nil
	}
	return e, err
}

// AussenListe liefert die Partnerliste außen, nach Board-ID sortiert.
func (r *BoardpaarRepo) AussenListe(ctx context.Context) ([]BoardpaarAussen, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT aussen_board_id, innen_link_basis, seit, von, COALESCE(letzter_eingang, ''), COALESCE(letzter_fehlgrund, ''), kopie, bearbeitbar,
		`+fmt.Sprintf(offeneAenderungenSQL, "aussen_board_id")+`
		FROM boardpaar_aussen ORDER BY aussen_board_id`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.aussen-liste: %w", err)
	}
	defer rows.Close()
	out := []BoardpaarAussen{}
	for rows.Next() {
		var e BoardpaarAussen
		var kopie, bearbeitbar int
		if err := rows.Scan(&e.BoardID, &e.InnenLinkBasis, &e.Seit, &e.Von, &e.LetzterEingang, &e.LetzterFehlgrund, &kopie, &bearbeitbar, &e.OffeneAenderungen); err != nil {
			return nil, fmt.Errorf("boardpaar.aussen-liste scan: %w", err)
		}
		e.Kopie = kopie == 1
		e.Bearbeitbar = e.Kopie && bearbeitbar == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// maxFehlgrund begrenzt den gemerkten Fehlgrund (Zeichen) — er stammt aus
// eigenen Meldungen, kann aber Teile fremder Eingaben tragen.
const maxFehlgrund = 300

func kuerzeFehlgrund(g string) string {
	if r := []rune(g); len(r) > maxFehlgrund {
		return string(r[:maxFehlgrund]) + "…"
	}
	return g
}

// InnenAbfrageMerken hält den letzten Abruf des Ausgangs für ein gekoppeltes
// Board fest (F-2019): Zeitpunkt und Fehlgrund, leer bei Erfolg. Ist das
// Board nicht (mehr) gekoppelt, ändert sich nichts.
func (r *BoardpaarRepo) InnenAbfrageMerken(ctx context.Context, boardID, fehlgrund string) error {
	var grund any
	if fehlgrund != "" {
		grund = kuerzeFehlgrund(fehlgrund)
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE boardpaar_innen SET letzte_abfrage = ?, letzter_fehlgrund = ? WHERE board_id = ?`,
		nowISO(), grund, boardID); err != nil {
		return fmt.Errorf("boardpaar.innen-abfrage-merken: %w", err)
	}
	return nil
}

// AussenEingangMerken: dasselbe für den Eingang außen (F-2019).
func (r *BoardpaarRepo) AussenEingangMerken(ctx context.Context, boardID, fehlgrund string) error {
	var grund any
	if fehlgrund != "" {
		grund = kuerzeFehlgrund(fehlgrund)
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE boardpaar_aussen SET letzter_eingang = ?, letzter_fehlgrund = ? WHERE aussen_board_id = ?`,
		nowISO(), grund, boardID); err != nil {
		return fmt.Errorf("boardpaar.aussen-eingang-merken: %w", err)
	}
	return nil
}
