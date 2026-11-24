package storage

// einladungen.go — Einladungs-Links (ADR-0149 §5.3, §5.7, §7; Karte #1076).
//
// Eine Einladung legt eine Zeile in konten (Zustand invited) und eine in
// einladungen an — keine users-Zeile (die entsteht erst bei der Bestätigung,
// #1077). Gespeichert wird nur der SHA-256 des 256-Bit-Tokens; der Klartext
// geht genau einmal an den Aufrufer (Admin-Antwort, Mail, CLI-Ausgabe).
//
// Erneut einladen (Konto noch invited): neuer Link, alle offenen Links des
// Kontos verfallen. Zurückziehen: Konto samt Links weg (nur solange invited
// und solange das Konto keinen Rücksetz-Link hat, F-2921/F-2926).
//
// Fristen (§7): Links laufen nach EinladungGueltig ab. Abgelaufene und
// eingelöste Links und verbrauchte Wiederherstellungscodes löscht
// AbgelaufeneAbraeumen nach EinladungAufbewahrung (Uhr-Gate). Ein Konto, dessen
// Link abgelaufen ist, bleibt als „eingeladen, Link abgelaufen“ stehen, bis der
// Admin erneut einlädt oder zurückzieht.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/uhr"
)

const (
	// EinladungGueltig: so lange gilt ein Link (ADR-0149 §5.3).
	EinladungGueltig = 7 * 24 * time.Hour
	// EinladungAufbewahrung: so lange bleiben abgelaufene oder eingelöste
	// Links und verbrauchte Codes stehen, bevor AbgelaufeneAbraeumen sie
	// löscht — der Admin soll „abgelaufen“ noch eine Weile sehen.
	EinladungAufbewahrung = 30 * 24 * time.Hour
	// EinladungZweckInvite / EinladungZweckReset: Spalte einladungen.zweck.
	EinladungZweckInvite = "invite"
	EinladungZweckReset  = "reset"
	// einladungenZweck: Name des Löschpfads für das Uhr-Gate.
	einladungenZweck = "einladungen"
)

var (
	// ErrEinladungAdresse: keine einzelne, gültige E-Mail-Adresse.
	ErrEinladungAdresse = errors.New("keine gültige e-mail-adresse")
	// ErrEinladungHatZugang: zur Adresse gibt es schon eine Person (users).
	ErrEinladungHatZugang = errors.New("die adresse hat schon zugang")
	// ErrEinladungKontoWeiter: das Konto ist schon eingelöst (pending,
	// active oder blocked) — dafür gibt es Bestätigen, Sperren, Zurücksetzen.
	ErrEinladungKontoWeiter = errors.New("das konto ist schon eingelöst")
	// ErrEinladungPersonHatZugang: Zurückziehen eines zurückgesetzten Kontos
	// (es hat einen Rücksetz-Link, F-2921/F-2926): Das Löschen ließe die Person
	// mit users-Zeile ohne Konto zurück — kein Zugang, nie wieder einladbar.
	// Dafür gibt Zuruecksetzen einen neuen Link. Eine frische Einladung lässt
	// sich immer zurückziehen, auch bei vorhandener users-Zeile.
	ErrEinladungPersonHatZugang = errors.New("die person hat schon zugang")
	// ErrEinladungUnbekannt: Link unbekannt, abgelaufen oder benutzt.
	ErrEinladungUnbekannt = errors.New("einladung unbekannt, abgelaufen oder benutzt")
)

// EinladungsAdresse prüft und normalisiert eine eingegebene Adresse: genau
// eine addr-spec ohne Anzeigenamen, nur ASCII, genau ein @, höchstens 254
// Zeichen, keine Domain-Freigabe.
func EinladungsAdresse(eingabe string) (string, error) {
	e := strings.TrimSpace(eingabe)
	if e == "" || len(e) > 254 || strings.ContainsAny(e, "\r\n\t <>,;\"") || strings.Count(e, "@") != 1 {
		return "", ErrEinladungAdresse
	}
	for i := 0; i < len(e); i++ {
		if e[i] > 0x7e || e[i] < 0x21 {
			return "", ErrEinladungAdresse
		}
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Name != "" || a.Address != e || IstDomainSubjekt(e) || strings.HasSuffix(e, "@") {
		return "", ErrEinladungAdresse
	}
	return NormalizeEmail(e), nil
}

// EinladungRepo verwaltet einladungen (und die Konten im Zustand invited).
type EinladungRepo struct {
	db    *sql.DB
	jetzt func() time.Time
}

// NewEinladungRepo erzeugt das Repo.
func NewEinladungRepo(db *sql.DB) *EinladungRepo { return &EinladungRepo{db: db, jetzt: time.Now} }

// MitUhr setzt die Uhr (Tests).
func (r *EinladungRepo) MitUhr(f func() time.Time) *EinladungRepo { r.jetzt = f; return r }

// NeueEinladung ist das Ergebnis von Einladen. Token ist der Klartext —
// er wird nirgends gespeichert.
type NeueEinladung struct {
	KontoID, Email, Token string
	Ablauf                time.Time
	// Erneut: Das Konto stand schon auf invited; alte Links sind verfallen.
	Erneut bool
}

// Einladen legt für email ein Konto (invited) und einen Link an, oder — wenn
// das Konto schon eingeladen ist — einen neuen Link, der alle offenen
// ersetzt. Fehler: ErrEinladungAdresse, ErrEinladungHatZugang,
// ErrEinladungKontoWeiter.
func (r *EinladungRepo) Einladen(ctx context.Context, eingabe string) (NeueEinladung, error) {
	email, err := EinladungsAdresse(eingabe)
	if err != nil {
		return NeueEinladung{}, err
	}
	if _, err := r.AbgelaufeneAbraeumen(ctx); err != nil {
		return NeueEinladung{}, err
	}
	token, err := newToken()
	if err != nil {
		return NeueEinladung{}, fmt.Errorf("einladung: %w", err)
	}
	jetzt := r.jetzt().UTC()
	ablauf := jetzt.Add(EinladungGueltig)

	// F-2892, Nebenläufigkeit §50: Erst lesen, dann schreiben — in einer
	// DEFERRED-Transaktion scheitert das unter WAL mit „database is locked“,
	// sobald zwei Einladungen gleichzeitig laufen. BEGIN IMMEDIATE nimmt die
	// Schreibsperre vorab (busy_timeout wartet), auf einer gepinnten
	// Verbindung, weil sql.TxOptions das nicht kann.
	tx, err := r.db.Conn(ctx)
	if err != nil {
		return NeueEinladung{}, fmt.Errorf("einladung: %w", err)
	}
	defer tx.Close()
	if _, err := tx.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			// Verify N2: Sperre nicht bekommen ist vorübergehend (§49).
			return NeueEinladung{}, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return NeueEinladung{}, fmt.Errorf("einladung: begin: %w", err)
	}
	fertig := false
	defer func() {
		if !fertig {
			if _, err := tx.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				// Verify N2: Eine Verbindung mit offener Transaktion darf
				// nicht zurück in den Pool — als defekt verwerfen.
				_ = tx.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE lower(email) = ?`, email).Scan(&n); err != nil {
		return NeueEinladung{}, fmt.Errorf("einladung: %w", err)
	}
	if n > 0 {
		return NeueEinladung{}, ErrEinladungHatZugang
	}
	out := NeueEinladung{Email: email, Token: token, Ablauf: ablauf}
	var zustand string
	err = tx.QueryRowContext(ctx, `SELECT id, zustand FROM konten WHERE email = ?`, email).Scan(&out.KontoID, &zustand)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		out.KontoID = uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES (?, ?, ?, ?, ?)`,
			out.KontoID, email, KontoEingeladen, jetzt.Unix(), jetzt.Unix()); err != nil {
			return NeueEinladung{}, fmt.Errorf("einladung: konto: %w", err)
		}
	case err != nil:
		return NeueEinladung{}, fmt.Errorf("einladung: %w", err)
	case zustand != KontoEingeladen:
		return NeueEinladung{}, ErrEinladungKontoWeiter
	default:
		out.Erneut = true
		// Offene Links verfallen sofort (Ablauf auf jetzt), bleiben aber
		// stehen, bis AbgelaufeneAbraeumen sie nach der Frist löscht.
		if _, err := tx.ExecContext(ctx, `UPDATE einladungen SET ablauf = ? WHERE konto_id = ? AND eingeloest IS NULL AND ablauf > ?`,
			jetzt.Unix(), out.KontoID, jetzt.Unix()); err != nil {
			return NeueEinladung{}, fmt.Errorf("einladung: alte links: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE konten SET geaendert = ? WHERE id = ?`, jetzt.Unix(), out.KontoID); err != nil {
			return NeueEinladung{}, fmt.Errorf("einladung: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO einladungen (token_hash, konto_id, zweck, angelegt, ablauf) VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), out.KontoID, EinladungZweckInvite, jetzt.Unix(), ablauf.Unix()); err != nil {
		return NeueEinladung{}, fmt.Errorf("einladung: link: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "COMMIT"); err != nil {
		return NeueEinladung{}, fmt.Errorf("einladung: %w", err)
	}
	fertig = true
	return out, nil
}

// OffeneEinladung ist ein gültiger, noch nicht eingelöster Link.
type OffeneEinladung struct {
	KontoID, Email, Zweck, Zustand string
	Ablauf                         time.Time
}

// Pruefen liest den Link zum Klartext-Token: unbenutzt, nicht abgelaufen.
// Den Zustand des Kontos prüft der Aufrufer (Einlösen, #1077).
func (r *EinladungRepo) Pruefen(ctx context.Context, token string) (OffeneEinladung, error) {
	if token == "" {
		return OffeneEinladung{}, ErrEinladungUnbekannt
	}
	var e OffeneEinladung
	var ablauf int64
	err := r.db.QueryRowContext(ctx, `SELECT e.konto_id, k.email, e.zweck, k.zustand, e.ablauf
		FROM einladungen e JOIN konten k ON k.id = e.konto_id
		WHERE e.token_hash = ? AND e.eingeloest IS NULL AND e.ablauf > ?`, hashToken(token), r.jetzt().UTC().Unix()).
		Scan(&e.KontoID, &e.Email, &e.Zweck, &e.Zustand, &ablauf)
	if errors.Is(err, sql.ErrNoRows) {
		return OffeneEinladung{}, ErrEinladungUnbekannt
	}
	if err != nil {
		return OffeneEinladung{}, fmt.Errorf("einladung lesen: %w", err)
	}
	e.Ablauf = time.Unix(ablauf, 0).UTC()
	return e, nil
}

// Zurueckziehen löscht ein eingeladenes Konto samt Links (nur Zustand
// invited). ErrKontoUnbekannt, wenn es keines gibt; ErrEinladungKontoWeiter,
// wenn es schon eingelöst ist (dann Ablehnen/Löschen, #1077).
func (r *EinladungRepo) Zurueckziehen(ctx context.Context, kontoID string) (string, error) {
	var email, zustand string
	err := r.db.QueryRowContext(ctx, `SELECT email, zustand FROM konten WHERE id = ?`, kontoID).Scan(&email, &zustand)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrKontoUnbekannt
	}
	if err != nil {
		return "", fmt.Errorf("einladung zurückziehen: %w", err)
	}
	if zustand != KontoEingeladen {
		return "", ErrEinladungKontoWeiter
	}
	// Die Bedingungen stehen im DELETE selbst: Löst die Person gerade ein,
	// gewinnt das Einlösen. Hat das Konto einen Rücksetz-Link (zurückgesetzt)
	// UND die Person eine users-Zeile, bleibt es stehen: Löschen ließe sie ohne
	// Konto zurück, nie wieder einladbar (F-2921, F-2926). Fehlt eine der beiden
	// Bedingungen, ist Widerrufen sicher (F-2931); eine users-Zeile bleibt stehen.
	res, err := r.db.ExecContext(ctx, `DELETE FROM konten WHERE id = ? AND zustand = ?
		AND NOT (EXISTS (SELECT 1 FROM einladungen WHERE konto_id = konten.id AND zweck = ?)
			AND EXISTS (SELECT 1 FROM users u WHERE lower(u.email) = konten.email))`,
		kontoID, KontoEingeladen, EinladungZweckReset)
	if err != nil {
		return "", fmt.Errorf("einladung zurückziehen: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var hat int
		if err := r.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM einladungen WHERE konto_id = ? AND zweck = ?)
			* (SELECT COUNT(*) FROM users WHERE lower(email) = ?)`,
			kontoID, EinladungZweckReset, email).Scan(&hat); err == nil && hat > 0 {
			return "", ErrEinladungPersonHatZugang
		}
		return "", ErrEinladungKontoWeiter
	}
	return email, nil
}

// KontoUebersicht ist eine Zeile der Admin-Liste (ADR-0149 §6).
type KontoUebersicht struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	Zustand        string `json:"zustand"`
	Weg            string `json:"weg"`
	GemeldeteEmail string `json:"gemeldete_email"`
	// GemeldeteEmailUnbestaetigt: der Anbieter hat die Adresse nicht als
	// bestätigt gemeldet (F-2961); die Adresse selbst bleibt rein.
	GemeldeteEmailUnbestaetigt bool      `json:"gemeldete_email_unbestaetigt"`
	GemeldeterName             string    `json:"gemeldeter_name"`
	Angelegt                   time.Time `json:"angelegt"`
	Geaendert                  time.Time `json:"geaendert"`
	LinkAblauf                 time.Time `json:"link_ablauf,omitzero"`
	LinkAbgelaufen             bool      `json:"link_abgelaufen"`
	HatPasswort                bool      `json:"hat_passwort"`
	HatZweiFaktor              bool      `json:"hat_zwei_faktor"`
	// HatZugang: Die Person hat eine users-Zeile. NeuerLink: invited und
	// (Rücksetz-Link, offen oder abgelaufen, oder users-Zeile) — Zuruecksetzen
	// gibt einen neuen Link aus (F-2921, F-2930). Widerrufbar: invited und nicht
	// (Rücksetz-Link UND users-Zeile) — dort würde Löschen die Person ohne Konto
	// zurücklassen (F-2931). Die Oberfläche zeigt beide Knöpfe, wo beides gilt.
	HatZugang   bool `json:"hat_zugang"`
	NeuerLink   bool `json:"neuer_link"`
	Widerrufbar bool `json:"widerrufbar"`
}

// Uebersicht liefert alle Konten, neueste zuerst. LinkAblauf ist der späteste
// offene Link; LinkAbgelaufen heißt: eingeladen, aber kein gültiger Link mehr.
func (r *EinladungRepo) Uebersicht(ctx context.Context) ([]KontoUebersicht, error) {
	jetzt := r.jetzt().UTC().Unix()
	rows, err := r.db.QueryContext(ctx, `SELECT k.id, k.email, k.zustand, k.weg, k.gemeldete_email, k.gemeldete_email_unbestaetigt, k.gemeldeter_name,
		k.angelegt, k.geaendert, k.passwort_hash IS NOT NULL, k.totp_geheimnis IS NOT NULL,
		(SELECT MAX(e.ablauf) FROM einladungen e WHERE e.konto_id = k.id AND e.eingeloest IS NULL),
		EXISTS (SELECT 1 FROM users u WHERE lower(u.email) = k.email),
		EXISTS (SELECT 1 FROM einladungen e WHERE e.konto_id = k.id AND e.zweck = 'reset')
		FROM konten k ORDER BY k.angelegt DESC, k.email`)
	if err != nil {
		return nil, fmt.Errorf("konten lesen: %w", err)
	}
	defer rows.Close()
	out := []KontoUebersicht{}
	for rows.Next() {
		var k KontoUebersicht
		var angelegt, geaendert int64
		var ablauf sql.NullInt64
		var hatReset bool
		if err := rows.Scan(&k.ID, &k.Email, &k.Zustand, &k.Weg, &k.GemeldeteEmail, &k.GemeldeteEmailUnbestaetigt, &k.GemeldeterName,
			&angelegt, &geaendert, &k.HatPasswort, &k.HatZweiFaktor, &ablauf, &k.HatZugang, &hatReset); err != nil {
			return nil, fmt.Errorf("konten lesen: %w", err)
		}
		k.Angelegt, k.Geaendert = time.Unix(angelegt, 0).UTC(), time.Unix(geaendert, 0).UTC()
		if ablauf.Valid {
			k.LinkAblauf = time.Unix(ablauf.Int64, 0).UTC()
		}
		k.NeuerLink = k.Zustand == KontoEingeladen && (hatReset || k.HatZugang)
		k.Widerrufbar = k.Zustand == KontoEingeladen && !(hatReset && k.HatZugang)
		k.LinkAbgelaufen = k.Zustand == KontoEingeladen && (!ablauf.Valid || ablauf.Int64 <= jetzt)
		out = append(out, k)
	}
	return out, rows.Err()
}

// AbgelaufeneAbraeumen löscht Links, die seit EinladungAufbewahrung
// abgelaufen oder eingelöst sind, und Wiederherstellungscodes, die seit
// EinladungAufbewahrung verbraucht sind (ADR-0149 §7). Uhr-Gate: ohne
// glaubwürdige Uhr wird nichts gelöscht.
func (r *EinladungRepo) AbgelaufeneAbraeumen(ctx context.Context) (int64, error) {
	if !uhr.Erlaubt(einladungenZweck, nil) {
		return 0, nil
	}
	grenze := r.jetzt().UTC().Add(-EinladungAufbewahrung).Unix()
	// F-2925: Der jüngste Rücksetz-Link eines Kontos, das noch invited (oder
	// gesperrt) ist, bleibt stehen, auch abgelaufen: Er ist das Merkmal
	// „zurückgesetzt“ für Tor, „Neuer Link“ und Widerrufen. Ältere Links gehen.
	res, err := r.db.ExecContext(ctx, `DELETE FROM einladungen WHERE (ablauf <= ? OR eingeloest <= ?)
		AND NOT (zweck = 'reset'
			AND konto_id IN (SELECT id FROM konten WHERE zustand IN ('invited', 'blocked'))
			AND token_hash = (SELECT e2.token_hash FROM einladungen e2
				WHERE e2.konto_id = einladungen.konto_id AND e2.zweck = 'reset'
				ORDER BY e2.angelegt DESC, e2.token_hash DESC LIMIT 1))`, grenze, grenze)
	if err != nil {
		return 0, fmt.Errorf("einladungen abräumen: %w", err)
	}
	n, _ := res.RowsAffected()
	res, err = r.db.ExecContext(ctx, `DELETE FROM konto_codes WHERE verbraucht <= ?`, grenze)
	if err != nil {
		return n, fmt.Errorf("codes abräumen: %w", err)
	}
	m, _ := res.RowsAffected()
	return n + m, nil
}
