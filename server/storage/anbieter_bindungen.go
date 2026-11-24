package storage

// anbieter_bindungen.go — Bindung (iss, sub) eines OIDC-Kontos an eine Person
// (ADR-0149 §4, Karte #1069; Vorgänger oidc_bindungen aus ADR-0148 §2, #816).
//
// Google nennt `email` veränderlich und nicht eindeutig; die Kennung eines
// Kontos ist `sub`. Seit Migration 0046 trägt die Tabelle keine Adresse mehr,
// sondern zeigt auf users.id (Person hat Zugang) oder, zwischen Einlösen einer
// Einladung und Bestätigung, auf konten.id. Damit folgt die Bindung einem
// Umzug von selbst, und ein Login über eine Bindung kann künftig (#1071) die
// Person bestimmen statt der gemeldeten Adresse.
//
// Sprint 109, Schritt 1/5 hält das Verhalten von ADR-0148 fest (Pruefen):
// Eine Person hat höchstens eine Bindung, der erste Login bindet, ein Login
// derselben Adresse mit einem anderen Paar wird abgewiesen. Keine
// Verknüpfung per Adressgleichheit (Kritik W4).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrBindungAbweichend: Die Adresse ist an ein anderes (iss, sub) gebunden,
// oder das Paar gehört einer anderen Person.
var ErrBindungAbweichend = errors.New("storage: adresse ist an ein anderes konto gebunden")

// ErrBindungUnbekannt: Für (iss, sub) gibt es keine Bindung.
var ErrBindungUnbekannt = errors.New("storage: anbieter-konto ist nicht gebunden")

// AnbieterStandard ist das Kürzel des Anbieters aus ABOARD_OIDC_ISSUER
// (ADR-0149 §1, reserviert). Bestandsbindungen aus oidc_bindungen tragen es.
const AnbieterStandard = "standard"

// AnbieterBindung ist eine Zeile aus anbieter_bindungen. Genau eines von
// KontoID/UserID kann leer sein, nie beide. Email ist die Adresse der
// gebundenen Person (users.email, sonst konten.email), nicht die vom Anbieter
// gemeldete.
type AnbieterBindung struct {
	Anbieter string
	Iss      string
	Sub      string
	KontoID  string
	UserID   string
	Email    string
	Angelegt int64
}

// AnbieterBindungRepo verwaltet anbieter_bindungen.
type AnbieterBindungRepo struct {
	db    *sql.DB
	jetzt func() time.Time
	// NutzerAnlegen legt beim ersten Login einer zugelassenen Adresse die
	// users-Zeile samt persönlichem Board an (BootstrapNewUser, idempotent),
	// damit die Bindung eine user_id hat. Bis Migration 0046 tat das erst der
	// ACLGuard beim nächsten Aufruf; die Bindung hing an der Adresse. Ohne
	// Funktion scheitert ein erster Login mit Fehler.
	NutzerAnlegen func(ctx context.Context, email string) error
}

// NewAnbieterBindungRepo erzeugt das Repo.
func NewAnbieterBindungRepo(db *sql.DB, nutzerAnlegen func(ctx context.Context, email string) error) *AnbieterBindungRepo {
	return &AnbieterBindungRepo{db: db, jetzt: time.Now, NutzerAnlegen: nutzerAnlegen}
}

const bindungSpalten = `b.anbieter, b.iss, b.sub, COALESCE(b.konto_id, ''), COALESCE(b.user_id, ''),
	COALESCE(u.email, k.email, ''), b.angelegt
	FROM anbieter_bindungen b
	LEFT JOIN users u ON u.id = b.user_id
	LEFT JOIN konten k ON k.id = b.konto_id`

func scanBindung(s interface{ Scan(...any) error }) (*AnbieterBindung, error) {
	var b AnbieterBindung
	if err := s.Scan(&b.Anbieter, &b.Iss, &b.Sub, &b.KontoID, &b.UserID, &b.Email, &b.Angelegt); err != nil {
		return nil, err
	}
	b.Email = NormalizeEmail(b.Email)
	return &b, nil
}

// NachKonto liest die Bindung des Anbieter-Kontos (iss, sub).
// Keine Bindung → ErrBindungUnbekannt.
func (r *AnbieterBindungRepo) NachKonto(ctx context.Context, iss, sub string) (*AnbieterBindung, error) {
	b, err := scanBindung(r.db.QueryRowContext(ctx, `SELECT `+bindungSpalten+` WHERE b.iss = ? AND b.sub = ?`, iss, sub))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBindungUnbekannt
	}
	if err != nil {
		return nil, fmt.Errorf("anbieter-bindung lesen: %w", err)
	}
	return b, nil
}

// FuerNutzer listet die Bindungen einer Person (users.id), nach Anbieter.
func (r *AnbieterBindungRepo) FuerNutzer(ctx context.Context, userID string) ([]AnbieterBindung, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+bindungSpalten+` WHERE b.user_id = ? ORDER BY b.anbieter`, userID)
	if err != nil {
		return nil, fmt.Errorf("anbieter-bindungen lesen: %w", err)
	}
	defer rows.Close()
	var out []AnbieterBindung
	for rows.Next() {
		b, err := scanBindung(rows)
		if err != nil {
			return nil, fmt.Errorf("anbieter-bindungen scan: %w", err)
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// Pruefen ist der Rückruf-Weg für den Anbieter `standard` (ADR-0148,
// unverändertes Verhalten): siehe PruefenFuer.
func (r *AnbieterBindungRepo) Pruefen(ctx context.Context, email, iss, sub string) error {
	return r.PruefenFuer(ctx, AnbieterStandard, email, iss, sub)
}

// PruefenFuer bindet (iss, sub) an die Person mit der Adresse email, falls
// weder das Paar noch die Person gebunden ist, und prüft sonst, dass beide
// zueinander gehören. Abweichung → ErrBindungAbweichend:
//   - das Paar ist an eine andere Person gebunden (oder nur an ein Konto, das
//     noch nicht bestätigt ist),
//   - die Person hat schon eine Bindung mit einem anderen Paar.
//
// Fehlt die users-Zeile, legt NutzerAnlegen sie an. Das Binden ist ein
// einziges INSERT … SELECT … WHERE NOT EXISTS: Zwei gleichzeitige erste
// Logins derselben Adresse mit verschiedenen Konten binden genau eines, das
// andere wird abgewiesen (wie unter ADR-0148).
func (r *AnbieterBindungRepo) PruefenFuer(ctx context.Context, anbieter, email, iss, sub string) error {
	email = NormalizeEmail(email)
	if email == "" || IstDomainSubjekt(email) || strings.TrimSpace(iss) == "" || strings.TrimSpace(sub) == "" || strings.TrimSpace(anbieter) == "" {
		return fmt.Errorf("anbieter-bindung: unvollständige angaben")
	}
	if b, err := r.NachKonto(ctx, iss, sub); err == nil {
		if b.UserID != "" && b.Email == email {
			return nil
		}
		return ErrBindungAbweichend
	} else if !errors.Is(err, ErrBindungUnbekannt) {
		return err
	}

	userID, err := r.nutzerID(ctx, email)
	if errors.Is(err, ErrUserNotFound) {
		if r.NutzerAnlegen == nil {
			return fmt.Errorf("anbieter-bindung: keine users-zeile und kein NutzerAnlegen")
		}
		if err := r.NutzerAnlegen(ctx, email); err != nil {
			return fmt.Errorf("anbieter-bindung: nutzer anlegen: %w", err)
		}
		userID, err = r.nutzerID(ctx, email)
	}
	if err != nil {
		return err
	}

	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, user_id, angelegt)
		 SELECT ?, ?, ?, NULL, ?, ?
		  WHERE NOT EXISTS (SELECT 1 FROM anbieter_bindungen WHERE user_id = ?)
		 ON CONFLICT DO NOTHING`,
		iss, sub, anbieter, userID, r.jetzt().UTC().Unix(), userID); err != nil {
		return fmt.Errorf("anbieter-bindung anlegen: %w", err)
	}
	b, err := r.NachKonto(ctx, iss, sub)
	if errors.Is(err, ErrBindungUnbekannt) {
		return ErrBindungAbweichend // die Person war schon anders gebunden
	}
	if err != nil {
		return err
	}
	if b.UserID != userID {
		return ErrBindungAbweichend
	}
	return nil
}

// nutzerID liest users.id zur Adresse (Vergleich ohne Groß-/Kleinschreibung
// wie Löschung und Umzug; Altbestände können anders geschrieben sein).
func (r *AnbieterBindungRepo) nutzerID(ctx context.Context, email string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ? ORDER BY created, id LIMIT 1`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", fmt.Errorf("anbieter-bindung: users lesen: %w", err)
	}
	return id, nil
}
