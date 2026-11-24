package storage

// konten_einloesen.go — Einladung einlösen, bestätigen, ablehnen, sperren,
// entsperren und zurücksetzen (ADR-0149 §5.4–§5.6, Nachtrag #1077).
//
// Zustände eines Kontos: invited → (Einlösen) pending → (Bestätigen) active;
// jederzeit blocked (Sperren); Zurücksetzen führt zurück nach invited.
//
// Alles, was erst liest und dann schreibt, läuft in BEGIN IMMEDIATE auf einer
// gepinnten Verbindung (Nebenläufigkeit §50): In einer DEFERRED-Transaktion
// scheitert das unter WAL mit „database is locked“, sobald ein zweiter
// Schreiber dazwischen committet.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrKontoZustand: Das Konto steht nicht im Zustand, den die Handlung
	// verlangt (z. B. Bestätigen eines Kontos, das nicht wartet).
	ErrKontoZustand = errors.New("konto steht nicht im passenden zustand")
	// ErrKontoHatZugang: Ablehnen eines Kontos, dessen Person schon eine
	// users-Zeile hat (zurückgesetzte Person) — dafür Sperren oder Löschen.
	ErrKontoHatZugang = errors.New("die person hat schon zugang")
	// ErrBindungBelegt: Das Anbieter-Konto ist schon an eine Person oder ein
	// Konto gebunden; Einlösen über dieses Anbieter-Konto geht nicht.
	ErrBindungBelegt = errors.New("anbieter-konto ist schon gebunden")
	// ErrBindungPersonBelegt: Die Person hat bei diesem Anbieter schon eine
	// Bindung (z. B. über die Vorab-Liste gebunden, F-2922); Bestätigen würde
	// die zweite Bindung des Kontos an dieselbe Person hängen. Vorher die
	// Bindung der Person lösen (CLI `nutzer bindung-loesen`) oder ablehnen.
	ErrBindungPersonBelegt = errors.New("die person hat bei diesem anbieter schon eine bindung")
)

// EinladungTokenHash ist der gespeicherte Hash eines Einladungs-Tokens (der
// Anmelde-Zustand des Anbieter-Wegs trägt ihn statt des Klartexts).
func EinladungTokenHash(token string) string { return hashToken(token) }

// mitImmediate führt fn in BEGIN IMMEDIATE auf einer gepinnten Verbindung aus.
// fn gibt nil zurück → COMMIT, sonst ROLLBACK. Sperre nicht bekommen ist
// ErrDBBelegt (vorübergehend, §49).
func mitImmediate(ctx context.Context, db *sql.DB, wofuer string, fn func(c *sql.Conn) error) error {
	c, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", wofuer, err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return fmt.Errorf("%s: begin: %w", wofuer, err)
	}
	fertig := false
	defer func() {
		if !fertig {
			if _, err := c.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				// Eine Verbindung mit offener Transaktion darf nicht zurück in
				// den Pool (wie EinladungRepo.Einladen, Verify N2).
				_ = c.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	if err := fn(c); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("%s: commit: %w", wofuer, err)
	}
	fertig = true
	return nil
}

// sitzungenDerAdresseBeenden löscht alle Sitzungen einer (normalisierten)
// Adresse in der Transaktion des Aufrufers.
func sitzungenDerAdresseBeenden(ctx context.Context, c *sql.Conn, email string) error {
	if _, err := c.ExecContext(ctx, `DELETE FROM sitzungen WHERE email = ?`, email); err != nil {
		return fmt.Errorf("sitzungen beenden: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Einlösen

// Einloesung beschreibt, womit eingelöst wird: genau einer der beiden Wege.
type Einloesung struct {
	// Passwort-Weg: Hash (argon2id/PHC), versiegeltes 2FA-Geheimnis, der beim
	// Einrichten bestätigte Zeitschritt (gilt danach als verbraucht) und die
	// Hashes der Wiederherstellungscodes.
	PasswortHash   string
	TOTPVersiegelt string
	TOTPSchritt    int64
	CodeHashes     []string

	// Anbieter-Weg: Kürzel, (iss, sub) und was der Anbieter gemeldet hat.
	Anbieter, Iss, Sub             string
	GemeldeteEmail, GemeldeterName string
	// GemeldeteEmailUnbestaetigt: Der Anbieter meldete die Adresse ohne
	// email_verified=true (F-2911 Teil 3, F-2961). Die Adresse bleibt rein in
	// gemeldete_email, der Zustand steht in gemeldete_email_unbestaetigt; die
	// Oberfläche übersetzt und nennt den Grund. Ein Anbieter mit ungeprüften
	// Adressen könnte sonst die eingeladene Adresse melden, und die Warnung in
	// der Admin-Liste bliebe aus.
	GemeldeteEmailUnbestaetigt bool
}

// gemeldeteAdresse kürzt die gemeldete Adresse (ohne jeden Zusatz).
func gemeldeteAdresse(adresse string) string {
	return kuerze(adresse, 320)
}

// unbestaetigtFlag: das Flag nur, wenn überhaupt eine Adresse gespeichert wird.
func unbestaetigtFlag(d Einloesung) bool {
	return d.GemeldeteEmailUnbestaetigt && gemeldeteAdresse(d.GemeldeteEmail) != ""
}

// Eingeloest ist das Ergebnis von Einloesen.
type Eingeloest struct {
	KontoID, Email, Zweck, Weg string
}

func (e Einloesung) weg() (string, error) {
	pw := e.PasswortHash != "" || e.TOTPVersiegelt != "" || len(e.CodeHashes) > 0
	an := e.Anbieter != "" || e.Iss != "" || e.Sub != ""
	switch {
	case pw && !an:
		if e.PasswortHash == "" || e.TOTPVersiegelt == "" || len(e.CodeHashes) == 0 {
			return "", errors.New("einlösen: passwort-weg unvollständig")
		}
		return "password", nil
	case an && !pw:
		if strings.TrimSpace(e.Anbieter) == "" || strings.TrimSpace(e.Iss) == "" || strings.TrimSpace(e.Sub) == "" ||
			e.Anbieter == "password" {
			return "", errors.New("einlösen: anbieter-weg unvollständig")
		}
		return e.Anbieter, nil
	}
	return "", errors.New("einlösen: genau ein weg")
}

// Einloesen löst den Link mit dem Token-Hash ein (ADR-0149 §5.4): Link
// unbenutzt und nicht abgelaufen, Konto invited → Anmeldedaten bzw. Bindung,
// weg, Zustand pending, Link eingelöst — alles in einer Transaktion. Die
// Sitzungen der Adresse enden dabei (Zurücksetzen: alte Geräte draußen).
// Fehler: ErrEinladungUnbekannt (Link unbekannt, abgelaufen, benutzt oder das
// Konto ist nicht mehr invited), ErrBindungBelegt.
func (r *EinladungRepo) Einloesen(ctx context.Context, tokenHash string, d Einloesung) (Eingeloest, error) {
	weg, err := d.weg()
	if err != nil {
		return Eingeloest{}, err
	}
	if len(tokenHash) != 64 {
		return Eingeloest{}, ErrEinladungUnbekannt
	}
	jetzt := r.jetzt().UTC().Unix()
	var out Eingeloest
	err = mitImmediate(ctx, r.db, "einlösen", func(c *sql.Conn) error {
		var zustand string
		err := c.QueryRowContext(ctx, `SELECT e.konto_id, k.email, e.zweck, k.zustand
			FROM einladungen e JOIN konten k ON k.id = e.konto_id
			WHERE e.token_hash = ? AND e.eingeloest IS NULL AND e.ablauf > ?`, tokenHash, jetzt).
			Scan(&out.KontoID, &out.Email, &out.Zweck, &zustand)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEinladungUnbekannt
		}
		if err != nil {
			return fmt.Errorf("einlösen: %w", err)
		}
		if zustand != KontoEingeladen {
			return ErrEinladungUnbekannt
		}
		out.Weg = weg
		if weg == "password" {
			if _, err := c.ExecContext(ctx, `UPDATE konten SET passwort_hash = ?, totp_geheimnis = ?, totp_letzter_schritt = ?,
				weg = ?, gemeldete_email = '', gemeldete_email_unbestaetigt = 0, gemeldeter_name = '', zustand = ?, geaendert = ? WHERE id = ?`,
				d.PasswortHash, d.TOTPVersiegelt, d.TOTPSchritt, weg, KontoWartet, jetzt, out.KontoID); err != nil {
				return fmt.Errorf("einlösen: konto: %w", err)
			}
			if _, err := c.ExecContext(ctx, `DELETE FROM konto_codes WHERE konto_id = ?`, out.KontoID); err != nil {
				return fmt.Errorf("einlösen: codes: %w", err)
			}
			for _, h := range d.CodeHashes {
				if _, err := c.ExecContext(ctx, `INSERT INTO konto_codes (konto_id, code_hash, angelegt) VALUES (?, ?, ?)`,
					out.KontoID, h, jetzt); err != nil {
					return fmt.Errorf("einlösen: codes: %w", err)
				}
			}
		} else {
			var n int
			if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM anbieter_bindungen WHERE iss = ? AND sub = ?`, d.Iss, d.Sub).Scan(&n); err != nil {
				return fmt.Errorf("einlösen: bindung: %w", err)
			}
			if n > 0 {
				return ErrBindungBelegt
			}
			if _, err := c.ExecContext(ctx, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, user_id, angelegt)
				VALUES (?, ?, ?, ?, NULL, ?)`, d.Iss, d.Sub, d.Anbieter, out.KontoID, jetzt); err != nil {
				return fmt.Errorf("einlösen: bindung: %w", err)
			}
			if _, err := c.ExecContext(ctx, `UPDATE konten SET passwort_hash = NULL, totp_geheimnis = NULL, totp_letzter_schritt = 0,
				weg = ?, gemeldete_email = ?, gemeldete_email_unbestaetigt = ?, gemeldeter_name = ?, zustand = ?, geaendert = ? WHERE id = ?`,
				weg, gemeldeteAdresse(d.GemeldeteEmail), unbestaetigtFlag(d), kuerze(d.GemeldeterName, 200), KontoWartet, jetzt, out.KontoID); err != nil {
				return fmt.Errorf("einlösen: konto: %w", err)
			}
			if _, err := c.ExecContext(ctx, `DELETE FROM konto_codes WHERE konto_id = ?`, out.KontoID); err != nil {
				return fmt.Errorf("einlösen: codes: %w", err)
			}
		}
		if _, err := c.ExecContext(ctx, `UPDATE einladungen SET eingeloest = ? WHERE token_hash = ?`, jetzt, tokenHash); err != nil {
			return fmt.Errorf("einlösen: link: %w", err)
		}
		return sitzungenDerAdresseBeenden(ctx, c, out.Email)
	})
	if err != nil {
		return Eingeloest{}, err
	}
	return out, nil
}

// kuerze begrenzt einen vom Anbieter gemeldeten Text (Anzeige im Admin).
func kuerze(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// An einer Zeichengrenze schneiden.
	for n > 0 && n < len(s) && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// Zurücksetzen

// Zuruecksetzen setzt ein eingelöstes Konto zurück (ADR-0149 §5.5): Passwort,
// 2FA, Codes und alle Bindungen der Person gehen, Sitzungen enden, offene
// Links verfallen, Zustand invited, neuer Link mit Zweck reset.
// Fehler: ErrKontoUnbekannt; ErrKontoZustand, wenn das Konto noch invited ist
// und weder Rücksetz-Link (Zweck reset, offen oder abgelaufen) noch users-Zeile
// hat (eine frische Einladung: dann erneut einladen). F-2921/F-2925/F-2926: Ein
// zurückgesetztes Konto bleibt invited, auch wenn sein Link abgelaufen ist;
// Zurücksetzen gibt dann einen neuen Link aus (alte Links verfallen). F-2930: Auch
// ein invited-Konto mit users-Zeile ohne Rücksetz-Link (nach CLI-Lösen,
// Sperren, Entsperren) bekommt so einen Link.
func (r *EinladungRepo) Zuruecksetzen(ctx context.Context, kontoID string) (NeueEinladung, error) {
	token, err := newToken()
	if err != nil {
		return NeueEinladung{}, fmt.Errorf("zurücksetzen: %w", err)
	}
	jetzt := r.jetzt().UTC()
	out := NeueEinladung{KontoID: kontoID, Token: token, Ablauf: jetzt.Add(EinladungGueltig)}
	err = mitImmediate(ctx, r.db, "zurücksetzen", func(c *sql.Conn) error {
		var zustand string
		err := c.QueryRowContext(ctx, `SELECT email, zustand FROM konten WHERE id = ?`, kontoID).Scan(&out.Email, &zustand)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKontoUnbekannt
		}
		if err != nil {
			return fmt.Errorf("zurücksetzen: %w", err)
		}
		if zustand == KontoEingeladen {
			// F-2921/F-2930: Im Zustand invited bekommt einen neuen Link, wer einen
			// Rücksetz-Link ODER eine users-Zeile hat (Person mit Zugang gehabt).
			var n int
			if err := c.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM einladungen WHERE konto_id = ? AND zweck = ?)
				+ (SELECT COUNT(*) FROM users WHERE lower(email) = ?)`,
				kontoID, EinladungZweckReset, out.Email).Scan(&n); err != nil {
				return fmt.Errorf("zurücksetzen: %w", err)
			}
			if n == 0 {
				return ErrKontoZustand
			}
		}
		t := jetzt.Unix()
		schritte := []struct {
			wofuer, sql string
			args        []any
		}{
			{"konto", `UPDATE konten SET passwort_hash = NULL, totp_geheimnis = NULL, totp_letzter_schritt = 0, weg = '',
				gemeldete_email = '', gemeldete_email_unbestaetigt = 0, gemeldeter_name = '', zustand = ?, geaendert = ? WHERE id = ?`, []any{KontoEingeladen, t, kontoID}},
			{"codes", `DELETE FROM konto_codes WHERE konto_id = ?`, []any{kontoID}},
			{"bindungen", `DELETE FROM anbieter_bindungen WHERE konto_id = ?
				OR user_id IN (SELECT id FROM users WHERE lower(email) = ?)`, []any{kontoID, out.Email}},
		}
		for _, s := range schritte {
			if _, err := c.ExecContext(ctx, s.sql, s.args...); err != nil {
				return fmt.Errorf("zurücksetzen: %s: %w", s.wofuer, err)
			}
		}
		if err := ruecksetzLinkAnlegen(ctx, c, kontoID, token, t, out.Ablauf.Unix()); err != nil {
			return err
		}
		return sitzungenDerAdresseBeenden(ctx, c, out.Email)
	})
	if err != nil {
		return NeueEinladung{}, err
	}
	return out, nil
}

// ruecksetzLinkAnlegen lässt offene Links des Kontos verfallen und legt einen
// Link mit Zweck reset an (Merkmal „zurückgesetzt“ für Tor, „Neuer Link“ und
// Widerrufen). Einziger Codepfad für Zuruecksetzen und Entsperren (F-2932).
func ruecksetzLinkAnlegen(ctx context.Context, c *sql.Conn, kontoID, token string, jetzt, ablauf int64) error {
	if _, err := c.ExecContext(ctx, `UPDATE einladungen SET ablauf = ? WHERE konto_id = ? AND eingeloest IS NULL AND ablauf > ?`,
		jetzt, kontoID, jetzt); err != nil {
		return fmt.Errorf("zurücksetzen: alte links: %w", err)
	}
	if _, err := c.ExecContext(ctx, `INSERT INTO einladungen (token_hash, konto_id, zweck, angelegt, ablauf) VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), kontoID, EinladungZweckReset, jetzt, ablauf); err != nil {
		return fmt.Errorf("zurücksetzen: link: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Bestätigen, Ablehnen, Sperren, Entsperren

// KontoKurz: Adresse und Zustand eines Kontos.
type KontoKurz struct{ ID, Email, Zustand string }

// NachID liest Adresse und Zustand; ErrKontoUnbekannt, wenn es keines gibt.
func (r *KontoRepo) NachID(ctx context.Context, kontoID string) (KontoKurz, error) {
	k := KontoKurz{ID: kontoID}
	err := r.db.QueryRowContext(ctx, `SELECT email, zustand FROM konten WHERE id = ?`, kontoID).Scan(&k.Email, &k.Zustand)
	if errors.Is(err, sql.ErrNoRows) {
		return KontoKurz{}, ErrKontoUnbekannt
	}
	if err != nil {
		return KontoKurz{}, fmt.Errorf("konto lesen: %w", err)
	}
	return k, nil
}

// GibtAdmin: Hat irgendeine Person Admin-Rechte? (ADR-0149 §5.7, Nachtrag
// #1076: ohne Admin gilt eine eingelöste Einladung als bestätigt.)
func (r *KontoRepo) GibtAdmin(ctx context.Context) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n); err != nil {
		return false, fmt.Errorf("admins zählen: %w", err)
	}
	return n > 0, nil
}

// Bestaetigen: pending → active (ADR-0149 §5.5). Zuerst legt nutzerAnlegen
// die users-Zeile samt persönlichem Board an (BootstrapNewUser, idempotent;
// die erste Person wird dort Admin), dann setzt eine Transaktion den Zustand
// und die user_id der Bindungen des Kontos. Liefert die Adresse.
// Fehler: ErrKontoUnbekannt, ErrKontoZustand.
func (r *KontoRepo) Bestaetigen(ctx context.Context, kontoID string, nutzerAnlegen func(ctx context.Context, email string) error, jetzt int64) (string, error) {
	k, err := r.NachID(ctx, kontoID)
	if err != nil {
		return "", err
	}
	if k.Zustand != KontoWartet {
		return "", ErrKontoZustand
	}
	if nutzerAnlegen == nil {
		return "", errors.New("bestätigen: kein nutzerAnlegen")
	}
	if err := nutzerAnlegen(ctx, k.Email); err != nil {
		return "", fmt.Errorf("bestätigen: nutzer anlegen: %w", err)
	}
	err = mitImmediate(ctx, r.db, "bestätigen", func(c *sql.Conn) error {
		var userID string
		err := c.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ? ORDER BY created, id LIMIT 1`, k.Email).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("bestätigen: users-zeile fehlt nach dem anlegen")
		}
		if err != nil {
			return fmt.Errorf("bestätigen: %w", err)
		}
		res, err := c.ExecContext(ctx, `UPDATE konten SET zustand = ?, geaendert = ? WHERE id = ? AND zustand = ?`,
			KontoAktiv, jetzt, kontoID, KontoWartet)
		if err != nil {
			return fmt.Errorf("bestätigen: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrKontoZustand
		}
		if _, err := c.ExecContext(ctx, `UPDATE anbieter_bindungen SET user_id = ? WHERE konto_id = ?`, userID, kontoID); err != nil {
			if istUniqueFehler(err) {
				// F-2922: Die Person hat bei diesem Anbieter schon eine Bindung.
				return ErrBindungPersonBelegt
			}
			return fmt.Errorf("bestätigen: bindung: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return k.Email, nil
}

// Ablehnen löscht ein wartendes Konto samt Links, Codes und Bindungen in
// einer Transaktion (ADR-0149 §5.5, Verify 02.11. N-1: dasselbe Anbieter-
// Konto lässt sich danach neu einladen). Hat die Person schon eine
// users-Zeile (zurückgesetzt), ErrKontoHatZugang. Liefert die Adresse.
func (r *KontoRepo) Ablehnen(ctx context.Context, kontoID string) (string, error) {
	var email string
	err := mitImmediate(ctx, r.db, "ablehnen", func(c *sql.Conn) error {
		var zustand string
		err := c.QueryRowContext(ctx, `SELECT email, zustand FROM konten WHERE id = ?`, kontoID).Scan(&email, &zustand)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKontoUnbekannt
		}
		if err != nil {
			return fmt.Errorf("ablehnen: %w", err)
		}
		if zustand != KontoWartet {
			return ErrKontoZustand
		}
		var n int
		if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE lower(email) = ?`, email).Scan(&n); err != nil {
			return fmt.Errorf("ablehnen: %w", err)
		}
		if n > 0 {
			return ErrKontoHatZugang
		}
		// Ausdrücklich statt über ON DELETE CASCADE: Das Löschen soll nicht
		// davon abhängen, dass foreign_keys an ist.
		for _, s := range []string{
			`DELETE FROM anbieter_bindungen WHERE konto_id = ?`,
			`DELETE FROM einladungen WHERE konto_id = ?`,
			`DELETE FROM konto_codes WHERE konto_id = ?`,
			`DELETE FROM konten WHERE id = ?`,
		} {
			if _, err := c.ExecContext(ctx, s, kontoID); err != nil {
				return fmt.Errorf("ablehnen: %w", err)
			}
		}
		return sitzungenDerAdresseBeenden(ctx, c, email)
	})
	if err != nil {
		return "", err
	}
	return email, nil
}

// Sperren setzt das Konto auf blocked und beendet alle Sitzungen der Person
// in derselben Transaktion (ADR-0149 §5.1, §5.5). Offene Einladungs- und
// Rücksetz-Links des Kontos verfallen mit (F-2924): Sonst würde „Entsperren“
// einen alten Link wieder gültig machen. Schon gesperrt ist kein Fehler.
// Liefert die Adresse.
func (r *KontoRepo) Sperren(ctx context.Context, kontoID string, jetzt int64) (string, error) {
	var email string
	err := mitImmediate(ctx, r.db, "sperren", func(c *sql.Conn) error {
		err := c.QueryRowContext(ctx, `SELECT email FROM konten WHERE id = ?`, kontoID).Scan(&email)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKontoUnbekannt
		}
		if err != nil {
			return fmt.Errorf("sperren: %w", err)
		}
		if _, err := c.ExecContext(ctx, `UPDATE konten SET zustand = ?, geaendert = ? WHERE id = ? AND zustand <> ?`,
			KontoGesperrt, jetzt, kontoID, KontoGesperrt); err != nil {
			return fmt.Errorf("sperren: %w", err)
		}
		if _, err := c.ExecContext(ctx, `UPDATE einladungen SET ablauf = ? WHERE konto_id = ? AND eingeloest IS NULL AND ablauf > ?`,
			jetzt, kontoID, jetzt); err != nil {
			return fmt.Errorf("sperren: links: %w", err)
		}
		return sitzungenDerAdresseBeenden(ctx, c, email)
	})
	if err != nil {
		return "", err
	}
	return email, nil
}

// Entsperren führt ein gesperrtes Konto zurück: pending, wenn es Anmeldedaten
// hat (weg gesetzt), sonst invited. Nie direkt active (Sitzungs-Kritik 03.11.
// A-2): Ob das Konto vor der Sperre bestätigt war, ist nicht gespeichert, und
// eine zurückgesetzte Person hat ihre users-Zeile noch — „Entsperren“ darf
// die Bestätigung nicht ersetzen. Der Admin bestätigt danach ausdrücklich.
// Liefert Adresse und neuen Zustand. Ein vormals eingeladenes Konto (invited)
// hat nach dem Sperren keinen gültigen Link mehr (F-2924): „Neuer Link“ bzw.
// erneut einladen.
// Fehler: ErrKontoUnbekannt, ErrKontoZustand (nicht gesperrt).
func (r *KontoRepo) Entsperren(ctx context.Context, kontoID string, jetzt int64) (string, string, error) {
	var email, neu string
	err := mitImmediate(ctx, r.db, "entsperren", func(c *sql.Conn) error {
		var zustand, weg string
		err := c.QueryRowContext(ctx, `SELECT email, zustand, weg FROM konten WHERE id = ?`, kontoID).Scan(&email, &zustand, &weg)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKontoUnbekannt
		}
		if err != nil {
			return fmt.Errorf("entsperren: %w", err)
		}
		if zustand != KontoGesperrt {
			return ErrKontoZustand
		}
		neu = KontoEingeladen
		if weg != "" {
			neu = KontoWartet
		}
		if _, err := c.ExecContext(ctx, `UPDATE konten SET zustand = ?, geaendert = ? WHERE id = ?`, neu, jetzt, kontoID); err != nil {
			return fmt.Errorf("entsperren: %w", err)
		}
		if neu == KontoEingeladen {
			// F-2932/F-2933: Ohne Anmeldedaten wird das Konto wie nach Zurücksetzen
			// geführt (Merkmal Rücksetz-Link): Liste ausgesetzt, Widerrufen bei
			// users-Zeile gesperrt, „Neuer Link“ da. Der Link ist sofort abgelaufen
			// und wird nicht ausgegeben; den echten Link gibt „Neuer Link“ aus.
			// Gilt auch für ein nie eingelöstes, gesperrtes Konto (frische Einladung).
			tok, err := newToken()
			if err != nil {
				return fmt.Errorf("entsperren: %w", err)
			}
			if err := ruecksetzLinkAnlegen(ctx, c, kontoID, tok, jetzt, jetzt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return email, neu, nil
}
