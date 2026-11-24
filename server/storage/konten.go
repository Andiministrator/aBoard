package storage

// konten.go — Konten im Werden und eigene Anmeldedaten (ADR-0149 §3, §5;
// Karte #1069). Sprint 109 braucht nur den Zustand für das Tor (#1070):
// hereingelassen wird, wer auf der Vorab-Liste steht ODER ein aktives Konto
// hat. Sprint 110: Passwort-Anmeldung (#1074); Anlegen, Einlösen, Bestätigen
// und 2FA folgen mit #1075–#1077.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Zustände eines Kontos (Spalte konten.zustand).
const (
	KontoEingeladen = "invited"
	KontoWartet     = "pending"
	KontoAktiv      = "active"
	KontoGesperrt   = "blocked"
)

// KontoRepo verwaltet konten.
type KontoRepo struct {
	db *sql.DB
}

// NewKontoRepo erzeugt das Repo.
func NewKontoRepo(db *sql.DB) *KontoRepo { return &KontoRepo{db: db} }

// Zustand liefert den Zustand des Kontos zur Adresse, "" wenn es keines gibt.
// Domain-Subjekte und leere Adressen haben nie ein Konto.
func (r *KontoRepo) Zustand(ctx context.Context, email string) (string, error) {
	email = NormalizeEmail(email)
	if email == "" || IstDomainSubjekt(email) {
		return "", nil
	}
	var z string
	err := r.db.QueryRowContext(ctx, `SELECT zustand FROM konten WHERE email = ?`, email).Scan(&z)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("konto lesen: %w", err)
	}
	return z, nil
}

// VorabListeAusgesetzt sagt, ob für die Adresse die Vorab-Liste nicht gilt
// (F-2922): Das Konto wartet auf Bestätigung (pending) oder ist zurückgesetzt
// (invited mit einem Rücksetz-Link, offen oder abgelaufen). Eine frische
// Einladung (Zweck invite) einer gelisteten Person bleibt durch die Liste
// gedeckt. Ohne Konto: false.
func (r *KontoRepo) VorabListeAusgesetzt(ctx context.Context, email string) (bool, error) {
	email = NormalizeEmail(email)
	if email == "" || IstDomainSubjekt(email) {
		return false, nil
	}
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM konten k WHERE k.email = ? AND
		(k.zustand = ? OR (k.zustand = ? AND EXISTS
			(SELECT 1 FROM einladungen e WHERE e.konto_id = k.id AND e.zweck = ?)))`,
		email, KontoWartet, KontoEingeladen, EinladungZweckReset).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("konto lesen: %w", err)
	}
	return n > 0, nil
}

// AnzahlAktiv zählt die aktiven Konten (Startwarnung „niemand ist
// zugelassen“, ADR-0149 §5.7).
func (r *KontoRepo) AnzahlAktiv(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM konten WHERE zustand = ?`, KontoAktiv).Scan(&n); err != nil {
		return 0, fmt.Errorf("konten zählen: %w", err)
	}
	return n, nil
}

// ErrKontoUnbekannt: Zur Adresse gibt es kein Konto.
var ErrKontoUnbekannt = errors.New("konto unbekannt")

// KontoAnmeldung ist, was die Passwort-Anmeldung über ein Konto wissen muss
// (ADR-0149 §3, Karte #1074). PasswortHash ist leer, wenn das Konto keinen
// Passwort-Weg hat (Anbieter-Weg oder noch nicht eingelöst).
type KontoAnmeldung struct {
	ID, Email, Zustand, PasswortHash string
	// HatTOTP: ein 2FA-Geheimnis ist eingerichtet (#1075).
	HatTOTP bool
}

// FuerAnmeldung liest das Konto zur Adresse; ErrKontoUnbekannt, wenn es
// keines gibt. Domain-Subjekte und leere Adressen haben nie ein Konto.
func (r *KontoRepo) FuerAnmeldung(ctx context.Context, email string) (KontoAnmeldung, error) {
	email = NormalizeEmail(email)
	if email == "" || IstDomainSubjekt(email) {
		return KontoAnmeldung{}, ErrKontoUnbekannt
	}
	var k KontoAnmeldung
	var hash sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, email, zustand, passwort_hash, totp_geheimnis IS NOT NULL FROM konten WHERE email = ?`, email).
		Scan(&k.ID, &k.Email, &k.Zustand, &hash, &k.HatTOTP)
	if errors.Is(err, sql.ErrNoRows) {
		return KontoAnmeldung{}, ErrKontoUnbekannt
	}
	if err != nil {
		return KontoAnmeldung{}, fmt.Errorf("konto lesen: %w", err)
	}
	k.PasswortHash = hash.String
	return k, nil
}

// SetzePasswortHash speichert den Passwort-Hash eines Kontos (argon2id im
// PHC-Format, erzeugt von api.PasswortHashen) und beendet in derselben
// Transaktion alle Sitzungen der Person (ADR-0149 §3: Passwort ändern beendet
// die anderen Sitzungen; F-2891). Die Prüfung der Mindestlänge liegt beim
// Aufrufer; hier wird nur ein nicht leerer Hash angenommen.
func (r *KontoRepo) SetzePasswortHash(ctx context.Context, kontoID, hash string, jetzt int64) error {
	if hash == "" {
		return errors.New("konto: leerer passwort-hash")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("passwort speichern: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE konten SET passwort_hash = ?, geaendert = ? WHERE id = ?`, hash, jetzt, kontoID)
	if err != nil {
		return fmt.Errorf("passwort speichern: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKontoUnbekannt
	}
	if err := sitzungenDesKontosBeenden(ctx, tx, kontoID); err != nil {
		return err
	}
	return tx.Commit()
}

// sitzungenDesKontosBeenden löscht alle Sitzungen der Adresse des Kontos
// (in der Transaktion des Aufrufers). Beide Spalten sind normalisiert
// gespeichert, darum ohne lower() — so greift der Index auf sitzungen.email
// (Verify N5). Wer danach eine neue Sitzung will,
// meldet sich mit den neuen Anmeldedaten an.
func sitzungenDesKontosBeenden(ctx context.Context, tx *sql.Tx, kontoID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sitzungen WHERE email = (SELECT email FROM konten WHERE id = ?)`, kontoID); err != nil {
		return fmt.Errorf("sitzungen beenden: %w", err)
	}
	return nil
}

// TOTPEinrichten speichert ein neues (versiegeltes) 2FA-Geheimnis und ersetzt
// alle Wiederherstellungscodes des Kontos in einer Transaktion (ADR-0149 §3,
// Karte #1075). Der letzte angenommene Zeitschritt beginnt bei 0. Alle
// Sitzungen der Person enden in derselben Transaktion (F-2891).
func (r *KontoRepo) TOTPEinrichten(ctx context.Context, kontoID, versiegelt string, codeHashes []string, jetzt int64) error {
	if versiegelt == "" || len(codeHashes) == 0 {
		return errors.New("konto: 2fa ohne geheimnis oder codes")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("2fa speichern: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE konten SET totp_geheimnis = ?, totp_letzter_schritt = 0, geaendert = ? WHERE id = ?`,
		versiegelt, jetzt, kontoID)
	if err != nil {
		return fmt.Errorf("2fa speichern: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKontoUnbekannt
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM konto_codes WHERE konto_id = ?`, kontoID); err != nil {
		return fmt.Errorf("codes ersetzen: %w", err)
	}
	for _, h := range codeHashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO konto_codes (konto_id, code_hash, angelegt) VALUES (?, ?, ?)`, kontoID, h, jetzt); err != nil {
			return fmt.Errorf("codes ersetzen: %w", err)
		}
	}
	if err := sitzungenDesKontosBeenden(ctx, tx, kontoID); err != nil {
		return err
	}
	return tx.Commit()
}

// TOTPGeheimnis liefert das versiegelte Geheimnis und den zuletzt
// angenommenen Zeitschritt; "" wenn keines eingerichtet ist.
func (r *KontoRepo) TOTPGeheimnis(ctx context.Context, kontoID string) (string, int64, error) {
	var g sql.NullString
	var letzter int64
	err := r.db.QueryRowContext(ctx, `SELECT totp_geheimnis, totp_letzter_schritt FROM konten WHERE id = ?`, kontoID).Scan(&g, &letzter)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrKontoUnbekannt
	}
	if err != nil {
		return "", 0, fmt.Errorf("2fa lesen: %w", err)
	}
	return g.String, letzter, nil
}

// TOTPSchrittVerbrauchen nimmt einen Zeitschritt an, wenn er jünger ist als
// der zuletzt angenommene — atomar, damit derselbe Code auch bei zwei
// gleichzeitigen Anfragen nur einmal gilt. false = schon verbraucht.
func (r *KontoRepo) TOTPSchrittVerbrauchen(ctx context.Context, kontoID string, schritt int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE konten SET totp_letzter_schritt = ? WHERE id = ? AND totp_letzter_schritt < ? AND totp_geheimnis IS NOT NULL`,
		schritt, kontoID, schritt)
	if err != nil {
		return false, fmt.Errorf("2fa-schritt: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CodeVerbrauchen löst einen Wiederherstellungscode ein (einmal, atomar).
// false = unbekannt oder schon benutzt.
func (r *KontoRepo) CodeVerbrauchen(ctx context.Context, kontoID, codeHash string, jetzt int64) (bool, error) {
	if codeHash == "" {
		return false, nil
	}
	res, err := r.db.ExecContext(ctx, `UPDATE konto_codes SET verbraucht = ? WHERE konto_id = ? AND code_hash = ? AND verbraucht IS NULL`,
		jetzt, kontoID, codeHash)
	if err != nil {
		return false, fmt.Errorf("wiederherstellungscode: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// OffeneCodes zählt die noch nicht benutzten Wiederherstellungscodes.
func (r *KontoRepo) OffeneCodes(ctx context.Context, kontoID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM konto_codes WHERE konto_id = ? AND verbraucht IS NULL`, kontoID).Scan(&n); err != nil {
		return 0, fmt.Errorf("codes zählen: %w", err)
	}
	return n, nil
}
