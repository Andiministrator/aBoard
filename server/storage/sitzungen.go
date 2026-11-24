package storage

// sitzungen.go — Sitzungen der eingebauten Anmeldung (ADR-0148 §2, Karte #1051).
//
// Eine Sitzung entsteht nach erfolgreichem OIDC-Rückruf (Karte #816) und wird
// über den Cookie `__Host-aboard_sitzung` getragen. Gespeichert ist nur der
// SHA-256 der Zufalls-ID (hashToken), nie die ID selbst — auch nicht im Log.
//
// Ablauf: 8 h ohne Nutzung, spätestens 7 Tage nach der Anlage. „Zuletzt
// genutzt“ wird höchstens einmal pro Minute geschrieben, weil jeder offene Tab
// alle paar Sekunden fragt. Abgelaufene Zeilen räumt der Server beim Start
// (AbgelaufeneAbraeumen) und bei jeder Neuanlage ab.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

const (
	// SitzungLeerlauf: so lange darf eine Sitzung ungenutzt bleiben.
	SitzungLeerlauf = 8 * time.Hour
	// SitzungHoechstdauer: harte Grenze ab der Anlage, auch bei Nutzung.
	SitzungHoechstdauer = 7 * 24 * time.Hour
	// sitzungBeruehrTakt: seltener wird „zuletzt“ nicht geschrieben.
	sitzungBeruehrTakt = time.Minute
	// sitzungIDLaenge: Länge der Klartext-ID (32 Byte, Base64 ohne Padding).
	sitzungIDLaenge = 43
	// sitzungenZweck: Name des Löschpfads für das Uhr-Gate.
	sitzungenZweck = "sitzungen"
)

// ErrSitzungUnbekannt: keine gültige Sitzung zu dieser ID (nie angelegt,
// beendet oder abgelaufen). Der Aufrufer unterscheidet die Fälle bewusst nicht.
var ErrSitzungUnbekannt = errors.New("storage: sitzung unbekannt oder abgelaufen")

// Sitzung ist eine gültige Sitzung, wie Pruefen sie zurückgibt.
type Sitzung struct {
	Email string
	Name  string
	// Ablauf ist der frühere der beiden Endpunkte (Leerlauf, Höchstdauer),
	// gerechnet ab dem Zeitpunkt der Prüfung.
	Ablauf time.Time
}

// SitzungRepo verwaltet die Tabelle sitzungen.
type SitzungRepo struct {
	db    *sql.DB
	jetzt func() time.Time
}

// NewSitzungRepo erzeugt das Repo mit der echten Uhr.
func NewSitzungRepo(db *sql.DB) *SitzungRepo {
	return &SitzungRepo{db: db, jetzt: time.Now}
}

// MitUhr setzt eine andere Uhr (Tests).
func (r *SitzungRepo) MitUhr(jetzt func() time.Time) *SitzungRepo {
	r.jetzt = jetzt
	return r
}

// Anlegen erzeugt eine neue Sitzung für email und gibt die Klartext-ID für den
// Cookie zurück. Jede Anmeldung bekommt eine neue ID (Schutz gegen
// Session-Fixation, ADR-0148 §2). Vorher werden abgelaufene Sitzungen
// abgeräumt.
func (r *SitzungRepo) Anlegen(ctx context.Context, email, name string) (string, time.Time, error) {
	email = NormalizeEmail(email)
	if email == "" || IstDomainSubjekt(email) {
		return "", time.Time{}, fmt.Errorf("sitzung anlegen: ungültige Adresse")
	}
	if _, err := r.AbgelaufeneAbraeumen(ctx); err != nil {
		return "", time.Time{}, err
	}
	klartext, err := newToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sitzung anlegen: %w", err)
	}
	jetzt := r.jetzt().UTC()
	ablauf := jetzt.Add(SitzungHoechstdauer)
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO sitzungen (id_hash, email, name, angelegt, zuletzt, ablauf) VALUES (?, ?, ?, ?, ?, ?)`,
		hashToken(klartext), email, name, jetzt.Unix(), jetzt.Unix(), ablauf.Unix()); err != nil {
		return "", time.Time{}, fmt.Errorf("sitzung anlegen: %w", err)
	}
	return klartext, ablauf, nil
}

// Pruefen liefert die Sitzung zur Klartext-ID oder ErrSitzungUnbekannt. Eine
// gültige Sitzung wird dabei als genutzt vermerkt (höchstens einmal pro
// Minute). Eine abgelaufene wird sofort gelöscht.
func (r *SitzungRepo) Pruefen(ctx context.Context, klartext string) (*Sitzung, error) {
	if len(klartext) != sitzungIDLaenge {
		return nil, ErrSitzungUnbekannt
	}
	h := hashToken(klartext)
	var s Sitzung
	var zuletzt, ablauf int64
	err := r.db.QueryRowContext(ctx,
		`SELECT email, name, zuletzt, ablauf FROM sitzungen WHERE id_hash = ?`, h).
		Scan(&s.Email, &s.Name, &zuletzt, &ablauf)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSitzungUnbekannt
	}
	if err != nil {
		return nil, fmt.Errorf("sitzung prüfen: %w", err)
	}
	jetzt := r.jetzt().UTC()
	hart := time.Unix(ablauf, 0).UTC()
	leer := time.Unix(zuletzt, 0).UTC().Add(SitzungLeerlauf)
	if !jetzt.Before(hart) || !jetzt.Before(leer) {
		// Abgewiesen wird immer; gelöscht nur mit gestellter Uhr (F-1192,
		// Wissen §38): Ginge die Uhr vor, verlöre sonst jede Person ihre
		// Sitzung endgültig statt nur bis zur richtigen Zeit.
		if uhr.Erlaubt(sitzungenZweck, nil) {
			if _, derr := r.db.ExecContext(ctx, `DELETE FROM sitzungen WHERE id_hash = ?`, h); derr != nil {
				return nil, fmt.Errorf("sitzung prüfen: abgelaufene löschen: %w", derr)
			}
		}
		return nil, ErrSitzungUnbekannt
	}
	if jetzt.Sub(time.Unix(zuletzt, 0)) >= sitzungBeruehrTakt {
		// Bedingung im WHERE: Zwei gleichzeitige Anfragen schreiben höchstens
		// einmal, und ein Schreibfehler macht die Anfrage nicht ungültig.
		if _, uerr := r.db.ExecContext(ctx,
			`UPDATE sitzungen SET zuletzt = ? WHERE id_hash = ? AND zuletzt < ?`,
			jetzt.Unix(), h, jetzt.Add(-sitzungBeruehrTakt).Unix()+1); uerr != nil {
			// Kritik A-3: Die Sitzung bleibt gültig (SQLITE_BUSY unter
			// Schreiblast darf keine Anfrage abbrechen); der nächste Takt
			// versucht es wieder. Gedrosselt geloggt, ohne Sitzungs-ID.
			if drossel(&beruehrLogZuletzt, jetzt) {
				log.Printf("aboard-server: sitzung: „zuletzt genutzt“ nicht geschrieben (%v) — Anfrage läuft weiter", uerr)
			}
		} else {
			leer = jetzt.Add(SitzungLeerlauf)
		}
	}
	s.Ablauf = hart
	if leer.Before(hart) {
		s.Ablauf = leer
	}
	return &s, nil
}

// Beenden löscht die Sitzung zur Klartext-ID (Abmelden). Eine unbekannte ID
// ist kein Fehler.
func (r *SitzungRepo) Beenden(ctx context.Context, klartext string) error {
	if klartext == "" {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM sitzungen WHERE id_hash = ?`, hashToken(klartext)); err != nil {
		return fmt.Errorf("sitzung beenden: %w", err)
	}
	return nil
}

// BeendeNichtZugelassene löscht beim Start die Sitzungen aller Adressen, die
// das Tor nicht mehr hereinlässt (Kritik A-2: sonst lebte ein alter Cookie
// wieder auf, sobald die Adresse erneut zugelassen wird). Das Tor fragt die
// Datenbank (ADR-0149 §5.1); deshalb erst alle Adressen lesen, dann prüfen.
// Ein Fehler des Tors bricht ab, ohne zu löschen.
func (r *SitzungRepo) BeendeNichtZugelassene(ctx context.Context, erlaubt func(context.Context, string) (bool, error)) (int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT email FROM sitzungen`)
	if err != nil {
		return 0, fmt.Errorf("sitzungen lesen: %w", err)
	}
	var alle []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return 0, fmt.Errorf("sitzungen lesen: %w", err)
		}
		alle = append(alle, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	var weg []string
	for _, e := range alle {
		ok := false
		if erlaubt != nil {
			if ok, err = erlaubt(ctx, e); err != nil {
				return 0, fmt.Errorf("sitzungen: zulassung prüfen: %w", err)
			}
		}
		if !ok {
			weg = append(weg, e)
		}
	}
	var n int64
	for _, e := range weg {
		res, err := r.db.ExecContext(ctx, `DELETE FROM sitzungen WHERE email = ?`, e)
		if err != nil {
			return n, fmt.Errorf("sitzungen beenden: %w", err)
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}

// AbgelaufeneAbraeumen löscht alle Sitzungen, deren Höchstdauer oder
// Leerlaufzeit vorbei ist — nur mit gestellter Uhr (uhr.Erlaubt, F-1192).
func (r *SitzungRepo) AbgelaufeneAbraeumen(ctx context.Context) (int64, error) {
	if !uhr.Erlaubt(sitzungenZweck, nil) {
		return 0, nil
	}
	jetzt := r.jetzt().UTC()
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM sitzungen WHERE ablauf <= ? OR zuletzt <= ?`,
		jetzt.Unix(), jetzt.Add(-SitzungLeerlauf).Unix())
	if err != nil {
		return 0, fmt.Errorf("sitzungen abräumen: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

var beruehrLogZuletzt atomic.Int64

// drossel: höchstens ein true pro Minute je Zähler.
func drossel(z *atomic.Int64, jetzt time.Time) bool {
	zuletzt := z.Load()
	if zuletzt != 0 && jetzt.UnixNano()-zuletzt < int64(time.Minute) {
		return false
	}
	return z.CompareAndSwap(zuletzt, jetzt.UnixNano())
}
