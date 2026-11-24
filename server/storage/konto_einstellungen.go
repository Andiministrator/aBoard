package storage

// konto_einstellungen.go — Speicher für die Konto-Einstellungen (ADR-0149 §3,
// §4, Nachtrag Karte #1083): Passwort ändern, 2FA neu, Wiederherstellungscodes
// neu, Anbieter-Wege verknüpfen und lösen, andere Sitzungen beenden.
//
// Grundsatz: Wer sein Passwort oder seinen zweiten Faktor ändert, bleibt in der
// laufenden Sitzung angemeldet, alle anderen enden (ADR-0149 §3, „alle
// anderen“). Dafür nehmen die Methoden hier die Klartext-ID der laufenden
// Sitzung und löschen alle Sitzungen der Person bis auf diese — in derselben
// Transaktion wie die Änderung (F-2891). Die neue Kennung der laufenden
// Sitzung (Fixierungsschutz) stellt der Aufrufer danach aus.
//
// Alles, was erst liest und dann schreibt, läuft in BEGIN IMMEDIATE (§50).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrLetzterWeg: Das Lösen würde der Person den letzten Anmeldeweg nehmen
	// (kein Passwort und keine weitere Bindung).
	ErrLetzterWeg = errors.New("das ist der letzte anmeldeweg")
	// ErrBindungFehlt: Die Person hat bei diesem Anbieter keine Bindung.
	ErrBindungFehlt = errors.New("die person hat bei diesem anbieter keine bindung")
	// ErrKeinTOTP: Wiederherstellungscodes gibt es nur mit eingerichtetem 2FA.
	ErrKeinTOTP = errors.New("konto hat kein 2fa")
)

// SitzungsHash ist der gespeicherte Hash einer Sitzungs-ID. Der signierte
// Anmelde-Zustand einer Verknüpfung trägt ihn statt der ID selbst.
func SitzungsHash(klartext string) string { return hashToken(klartext) }

// sitzungenAusser löscht alle Sitzungen der Adresse bis auf die mit dem Hash
// behalte ("" = keine behalten) in der Transaktion des Aufrufers.
func sitzungenAusser(ctx context.Context, c *sql.Conn, email, behalte string) error {
	if _, err := c.ExecContext(ctx, `DELETE FROM sitzungen WHERE email = ? AND id_hash <> ?`, email, behalte); err != nil {
		return fmt.Errorf("andere sitzungen beenden: %w", err)
	}
	return nil
}

// kontoAdresse liest die Adresse eines aktiven Kontos in der Transaktion.
// Nicht aktiv oder unbekannt → ErrKontoUnbekannt (die Einstellungen gelten nur
// für aktive Konten).
func kontoAdresse(ctx context.Context, c *sql.Conn, kontoID string) (string, error) {
	var email string
	err := c.QueryRowContext(ctx, `SELECT email FROM konten WHERE id = ? AND zustand = ?`, kontoID, KontoAktiv).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrKontoUnbekannt
	}
	if err != nil {
		return "", fmt.Errorf("konto lesen: %w", err)
	}
	return email, nil
}

// PasswortAendern speichert einen neuen Passwort-Hash und beendet alle anderen
// Sitzungen der Person; die Sitzung mit der ID behalteKlartext bleibt bis zur
// Neuausstellung durch den Aufrufer.
func (r *KontoRepo) PasswortAendern(ctx context.Context, kontoID, hash, behalteKlartext string, jetzt int64) error {
	if hash == "" {
		return errors.New("konto: leerer passwort-hash")
	}
	return mitImmediate(ctx, r.db, "passwort ändern", func(c *sql.Conn) error {
		email, err := kontoAdresse(ctx, c, kontoID)
		if err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `UPDATE konten SET passwort_hash = ?, geaendert = ? WHERE id = ?`, hash, jetzt, kontoID); err != nil {
			return fmt.Errorf("passwort speichern: %w", err)
		}
		return sitzungenAusser(ctx, c, email, hashOderLeer(behalteKlartext))
	})
}

// TOTPNeu ersetzt das 2FA-Geheimnis und alle Wiederherstellungscodes und
// beendet alle anderen Sitzungen. schritt ist der Zeitschritt des Codes, mit
// dem die Person das neue Geheimnis bestätigt hat — er gilt als verbraucht,
// damit derselbe Code nicht gleich noch einmal zum Anmelden taugt.
func (r *KontoRepo) TOTPNeu(ctx context.Context, kontoID, versiegelt string, codeHashes []string, schritt int64, behalteKlartext string, jetzt int64) error {
	if versiegelt == "" || len(codeHashes) == 0 {
		return errors.New("konto: 2fa ohne geheimnis oder codes")
	}
	return mitImmediate(ctx, r.db, "2fa neu", func(c *sql.Conn) error {
		email, err := kontoAdresse(ctx, c, kontoID)
		if err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `UPDATE konten SET totp_geheimnis = ?, totp_letzter_schritt = ?, geaendert = ? WHERE id = ?`,
			versiegelt, schritt, jetzt, kontoID); err != nil {
			return fmt.Errorf("2fa speichern: %w", err)
		}
		if err := codesSetzen(ctx, c, kontoID, codeHashes, jetzt); err != nil {
			return err
		}
		return sitzungenAusser(ctx, c, email, hashOderLeer(behalteKlartext))
	})
}

// CodesErsetzen ersetzt alle Wiederherstellungscodes (die alten verfallen, auch
// die noch nicht benutzten). Nur mit eingerichtetem 2FA. Sitzungen bleiben.
func (r *KontoRepo) CodesErsetzen(ctx context.Context, kontoID string, codeHashes []string, jetzt int64) error {
	if len(codeHashes) == 0 {
		return errors.New("konto: keine codes")
	}
	return mitImmediate(ctx, r.db, "codes ersetzen", func(c *sql.Conn) error {
		if _, err := kontoAdresse(ctx, c, kontoID); err != nil {
			return err
		}
		var hat bool
		if err := c.QueryRowContext(ctx, `SELECT totp_geheimnis IS NOT NULL FROM konten WHERE id = ?`, kontoID).Scan(&hat); err != nil {
			return fmt.Errorf("2fa lesen: %w", err)
		}
		if !hat {
			return ErrKeinTOTP
		}
		return codesSetzen(ctx, c, kontoID, codeHashes, jetzt)
	})
}

func codesSetzen(ctx context.Context, c *sql.Conn, kontoID string, codeHashes []string, jetzt int64) error {
	if _, err := c.ExecContext(ctx, `DELETE FROM konto_codes WHERE konto_id = ?`, kontoID); err != nil {
		return fmt.Errorf("codes ersetzen: %w", err)
	}
	for _, h := range codeHashes {
		if _, err := c.ExecContext(ctx, `INSERT INTO konto_codes (konto_id, code_hash, angelegt) VALUES (?, ?, ?)`, kontoID, h, jetzt); err != nil {
			return fmt.Errorf("codes ersetzen: %w", err)
		}
	}
	return nil
}

func hashOderLeer(klartext string) string {
	if klartext == "" {
		return ""
	}
	return hashToken(klartext)
}

// AndereBeenden löscht alle Sitzungen der Adresse bis auf die mit der ID
// behalteKlartext und gibt ihre Zahl zurück („Andere Sitzungen beenden“).
func (r *SitzungRepo) AndereBeenden(ctx context.Context, email, behalteKlartext string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sitzungen WHERE email = ? AND id_hash <> ?`,
		NormalizeEmail(email), hashOderLeer(behalteKlartext))
	if err != nil {
		return 0, fmt.Errorf("andere sitzungen beenden: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---------------------------------------------------------------------------
// Anbieter-Wege

// FuerEmail listet die Bindungen der Person mit dieser Adresse (users-Zeile),
// nach Anbieter. Keine users-Zeile → leer.
func (r *AnbieterBindungRepo) FuerEmail(ctx context.Context, email string) ([]AnbieterBindung, error) {
	id, err := r.nutzerID(ctx, NormalizeEmail(email))
	if errors.Is(err, ErrUserNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r.FuerNutzer(ctx, id)
}

// Verknuepfen bindet (iss, sub) als weiteren Anmeldeweg an die Person mit der
// Adresse email (ADR-0149 §4: nur aus dem angemeldeten Zustand). Das
// Anbieter-Konto darf nicht schon an eine andere Person oder ein Konto
// gebunden sein (ErrBindungBelegt). Hat die Person bei demselben Anbieter schon
// eine Bindung an ein anderes Anbieter-Konto, ersetzt die neue sie (ersetzt =
// true) — so wechselt eine Person ihr Google-Konto (F-2877). Ist (iss, sub)
// schon an diese Person gebunden, ändert sich nichts.
func (r *AnbieterBindungRepo) Verknuepfen(ctx context.Context, email, anbieter, iss, sub string) (ersetzt bool, err error) {
	if strings.TrimSpace(iss) == "" || strings.TrimSpace(sub) == "" || strings.TrimSpace(anbieter) == "" {
		return false, fmt.Errorf("anbieter-bindung: unvollständige angaben")
	}
	err = mitImmediate(ctx, r.db, "weg verknüpfen", func(c *sql.Conn) error {
		var userID string
		e := c.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ? ORDER BY created, id LIMIT 1`, NormalizeEmail(email)).Scan(&userID)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if e != nil {
			return fmt.Errorf("users lesen: %w", e)
		}
		var vorhanden sql.NullString
		e = c.QueryRowContext(ctx, `SELECT COALESCE(user_id, '') FROM anbieter_bindungen WHERE iss = ? AND sub = ?`, iss, sub).Scan(&vorhanden)
		switch {
		case e == nil && vorhanden.String == userID:
			return nil // schon verknüpft
		case e == nil:
			return ErrBindungBelegt
		case !errors.Is(e, sql.ErrNoRows):
			return fmt.Errorf("bindung lesen: %w", e)
		}
		res, e := c.ExecContext(ctx, `DELETE FROM anbieter_bindungen WHERE user_id = ? AND anbieter = ?`, userID, anbieter)
		if e != nil {
			return fmt.Errorf("alte bindung lösen: %w", e)
		}
		n, _ := res.RowsAffected()
		ersetzt = n > 0
		if _, e = c.ExecContext(ctx,
			`INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, user_id, angelegt) VALUES (?, ?, ?, NULL, ?, ?)`,
			iss, sub, anbieter, userID, r.jetzt().UTC().Unix()); e != nil {
			return fmt.Errorf("bindung anlegen: %w", e)
		}
		if ersetzt {
			// Ein ersetztes Anbieter-Konto beendet alle Sitzungen der Person in
			// derselben Transaktion (Sicherheitsprüfung #1083); der Aufrufer
			// stellt danach die laufende neu aus.
			return sitzungenAusser(ctx, c, NormalizeEmail(email), "")
		}
		return nil
	})
	if err != nil {
		ersetzt = false
	}
	return ersetzt, err
}

// Loesen löst die Bindung der Person bei diesem Anbieter. Nie den letzten
// Anmeldeweg: Hat die Person weder ein Passwort (aktives Konto mit Hash) noch
// eine weitere Bindung, bleibt alles stehen (ErrLetzterWeg). Der Eintrag auf
// der Vorab-Liste zählt nicht als Weg — er lässt nur herein, wer sich bei
// einem Anbieter anmeldet, und nach dem Lösen bände der nächste Anbieter-Login
// ein beliebiges Konto neu (F-2877).
func (r *AnbieterBindungRepo) Loesen(ctx context.Context, email, anbieter string) error {
	return mitImmediate(ctx, r.db, "weg lösen", func(c *sql.Conn) error {
		var userID string
		e := c.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ? ORDER BY created, id LIMIT 1`, NormalizeEmail(email)).Scan(&userID)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if e != nil {
			return fmt.Errorf("users lesen: %w", e)
		}
		var hat int
		if e := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM anbieter_bindungen WHERE user_id = ? AND anbieter = ?`, userID, anbieter).Scan(&hat); e != nil {
			return fmt.Errorf("bindung lesen: %w", e)
		}
		if hat == 0 {
			return ErrBindungFehlt
		}
		var andere, passwort int
		if e := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM anbieter_bindungen WHERE user_id = ? AND anbieter <> ?`, userID, anbieter).Scan(&andere); e != nil {
			return fmt.Errorf("bindungen zählen: %w", e)
		}
		if e := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM konten WHERE email = ? AND zustand = ? AND passwort_hash IS NOT NULL`,
			NormalizeEmail(email), KontoAktiv).Scan(&passwort); e != nil {
			return fmt.Errorf("passwort zählen: %w", e)
		}
		if andere == 0 && passwort == 0 {
			return ErrLetzterWeg
		}
		if _, e := c.ExecContext(ctx, `DELETE FROM anbieter_bindungen WHERE user_id = ? AND anbieter = ?`, userID, anbieter); e != nil {
			return fmt.Errorf("bindung lösen: %w", e)
		}
		if e := wegNachfuehren(ctx, c, NormalizeEmail(email), userID); e != nil {
			return e
		}
		return sitzungenAusser(ctx, c, NormalizeEmail(email), "")
	})
}

// wegNachfuehren führt konten.weg nach, wenn sich die Anmeldewege der Person
// geändert haben (F-2923): Der Admin-Reiter zeigt und Entsperren entscheidet
// nach `weg`. Bleibt keine Anbieter-Bindung, steht `weg` auf password (das
// Lösen lässt das nur zu, wenn das Konto ein Passwort hat); zeigt `weg` auf
// einen Anbieter ohne Bindung, nimmt es den ersten verbliebenen. Bleibt weder
// Bindung noch Passwort (betreiberseitig gelöst, F-2928), wird `weg` leer;
// Entsperren führt ein solches Konto nach invited, nie nach pending. Ohne Konto
// oder bei `weg` = password/passendem Anbieter ändert sich nichts. Läuft in der
// Transaktion des Aufrufers.
func wegNachfuehren(ctx context.Context, c *sql.Conn, email, userID string) error {
	var weg string
	var hash sql.NullString
	e := c.QueryRowContext(ctx, `SELECT weg, passwort_hash FROM konten WHERE email = ?`, email).Scan(&weg, &hash)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return fmt.Errorf("weg lesen: %w", e)
	}
	var rest sql.NullString
	if e := c.QueryRowContext(ctx, `SELECT MIN(anbieter) FROM anbieter_bindungen WHERE user_id = ?`, userID).Scan(&rest); e != nil {
		return fmt.Errorf("bindungen lesen: %w", e)
	}
	neu := weg
	switch {
	case !rest.Valid && hash.Valid && hash.String != "":
		neu = "password"
	case !rest.Valid:
		neu = ""
	case rest.Valid && weg != "password":
		var n int
		if e := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM anbieter_bindungen WHERE user_id = ? AND anbieter = ?`, userID, weg).Scan(&n); e != nil {
			return fmt.Errorf("bindungen lesen: %w", e)
		}
		if n == 0 {
			neu = rest.String
		}
	}
	if neu == weg {
		return nil
	}
	if _, e := c.ExecContext(ctx, `UPDATE konten SET weg = ? WHERE email = ?`, neu, email); e != nil {
		return fmt.Errorf("weg nachführen: %w", e)
	}
	return nil
}

// NutzerID liefert users.id zur Adresse (für den Audit-Eintrag der
// Konto-Einstellungen); ErrUserNotFound, wenn es keine gibt.
func (r *AnbieterBindungRepo) NutzerID(ctx context.Context, email string) (string, error) {
	return r.nutzerID(ctx, NormalizeEmail(email))
}

// LoesenOhnePruefung löst die Bindung der Person bei diesem Anbieter ohne die
// Prüfung auf den letzten Weg — der Betreiber-Weg (`aboard-server nutzer
// bindung-loesen`, F-2877) für eine Person, die ihr Anbieter-Konto verloren hat.
// Alle Sitzungen der Person enden in derselben Transaktion (der Anlass kann ein
// Verdacht sein). Liefert die users.id für den Audit-Eintrag.
func (r *AnbieterBindungRepo) LoesenOhnePruefung(ctx context.Context, email, anbieter string) (string, error) {
	var userID string
	err := mitImmediate(ctx, r.db, "bindung lösen", func(c *sql.Conn) error {
		e := c.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ? ORDER BY created, id LIMIT 1`, NormalizeEmail(email)).Scan(&userID)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if e != nil {
			return fmt.Errorf("users lesen: %w", e)
		}
		res, e := c.ExecContext(ctx, `DELETE FROM anbieter_bindungen WHERE user_id = ? AND anbieter = ?`, userID, anbieter)
		if e != nil {
			return fmt.Errorf("bindung lösen: %w", e)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrBindungFehlt
		}
		if e := wegNachfuehren(ctx, c, NormalizeEmail(email), userID); e != nil {
			return e
		}
		return sitzungenAusser(ctx, c, NormalizeEmail(email), "")
	})
	return userID, err
}

// ErrHatPasswort: Die Person hat schon ein aktives Konto mit Passwort (Passwort
// nachträglich anlegen geht dann nicht, Karte #1099).
var ErrHatPasswort = errors.New("konto hat schon ein passwort")

// PasswortAnlegen legt für eine Person mit users-Zeile ein Passwort mit 2FA an
// (ADR-0149, Nachtrag #1099): ohne Konto ein neues aktives Konto mit der ID
// neueID (weg password), bei einem aktiven Konto ohne Passwort (Anbieter-Weg)
// ergänzt es Passwort und 2FA. Ein Konto in invited, pending oder blocked wird
// nie überschrieben (ErrKontoZustand), eines mit Passwort ebenso
// (ErrHatPasswort); ohne users-Zeile ErrUserNotFound. schritt ist der
// Zeitschritt des bestätigenden Codes (gilt als verbraucht). Die
// Wiederherstellungscodes werden ersetzt, alle Sitzungen der Person enden in
// derselben Transaktion. Liefert die Konto-ID.
func (r *KontoRepo) PasswortAnlegen(ctx context.Context, email, neueID, hash, versiegelt string, codeHashes []string, schritt, jetzt int64) (string, error) {
	email = NormalizeEmail(email)
	if hash == "" || versiegelt == "" || len(codeHashes) == 0 || strings.TrimSpace(neueID) == "" || email == "" || IstDomainSubjekt(email) {
		return "", errors.New("konto: passwort anlegen ohne vollständige angaben")
	}
	var kontoID string
	err := mitImmediate(ctx, r.db, "passwort anlegen", func(c *sql.Conn) error {
		var userID string
		e := c.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ? ORDER BY created, id LIMIT 1`, email).Scan(&userID)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if e != nil {
			return fmt.Errorf("users lesen: %w", e)
		}
		var zustand string
		var alt sql.NullString
		e = c.QueryRowContext(ctx, `SELECT id, zustand, passwort_hash FROM konten WHERE email = ?`, email).Scan(&kontoID, &zustand, &alt)
		switch {
		case errors.Is(e, sql.ErrNoRows):
			kontoID = neueID
			if _, e := c.ExecContext(ctx, `INSERT INTO konten (id, email, zustand, weg, passwort_hash, totp_geheimnis, totp_letzter_schritt, angelegt, geaendert)
				VALUES (?, ?, ?, 'password', ?, ?, ?, ?, ?)`, kontoID, email, KontoAktiv, hash, versiegelt, schritt, jetzt, jetzt); e != nil {
				return fmt.Errorf("konto anlegen: %w", e)
			}
		case e != nil:
			return fmt.Errorf("konto lesen: %w", e)
		case zustand != KontoAktiv:
			return ErrKontoZustand
		case alt.Valid && alt.String != "":
			return ErrHatPasswort
		default:
			// F-2923: Mit dem Passwort steht `weg` auf password (Anzeige im
			// Admin-Reiter; Entsperren entscheidet nach `weg`).
			res, e := c.ExecContext(ctx, `UPDATE konten SET passwort_hash = ?, totp_geheimnis = ?, totp_letzter_schritt = ?, weg = 'password', geaendert = ?
				WHERE id = ? AND zustand = ? AND (passwort_hash IS NULL OR passwort_hash = '')`, hash, versiegelt, schritt, jetzt, kontoID, KontoAktiv)
			if e != nil {
				return fmt.Errorf("passwort speichern: %w", e)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return ErrKontoZustand
			}
		}
		if err := codesSetzen(ctx, c, kontoID, codeHashes, jetzt); err != nil {
			return err
		}
		return sitzungenAusser(ctx, c, email, "")
	})
	if err != nil {
		return "", err
	}
	return kontoID, nil
}
