package storage

// loesch_codes.go — Geheimnis und Verbrauchsliste der Bestätigungscodes für
// das Löschen über die Agenten-Werkzeuge (ADR-0139 E-2, Karte #757,
// Migration 0032). Die Code-Bildung selbst (HMAC, Bindung, Ablauf) liegt in
// api/agent_loeschen.go; hier nur, was die Datenbank dafür hält.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// ErrLoeschCodeVerbraucht: Der Code wurde schon einmal angenommen.
var ErrLoeschCodeVerbraucht = errors.New("storage: bestätigungscode schon verbraucht")

// LoeschCodeRepo verwaltet loesch_code_geheimnis und loesch_codes_verbraucht.
type LoeschCodeRepo struct {
	db *sql.DB

	mu        sync.Mutex
	geheimnis []byte // nach dem ersten Lesen gemerkt; ändert sich nie
}

func NewLoeschCodeRepo(db *sql.DB) *LoeschCodeRepo {
	return &LoeschCodeRepo{db: db}
}

// LoeschCodes: der Code-Speicher auf derselben Datenbank wie die Boards.
// Über das BoardRepo, damit jede Stelle, die Boards hat, auch die Codes hat
// (main.go und alle Test-Aufbauten), ohne eine weitere Verdrahtung.
func (r *BoardRepo) LoeschCodes() *LoeschCodeRepo {
	return NewLoeschCodeRepo(r.db)
}

// Geheimnis liefert das Server-Geheimnis; beim ersten Bedarf wird es erzeugt.
// INSERT OR IGNORE und danach lesen: Legen zwei Instanzen gleichzeitig an,
// gewinnt eine Zeile, und beide lesen sie.
func (r *LoeschCodeRepo) Geheimnis(ctx context.Context) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.geheimnis != nil {
		return r.geheimnis, nil
	}
	neu := make([]byte, 32)
	if _, err := rand.Read(neu); err != nil {
		return nil, fmt.Errorf("loesch_codes.Geheimnis: zufall: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO loesch_code_geheimnis (id, geheimnis, created_at) VALUES (1, ?, ?)`,
		neu, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, fmt.Errorf("loesch_codes.Geheimnis: anlegen: %w", err)
	}
	var g []byte
	if err := r.db.QueryRowContext(ctx, `SELECT geheimnis FROM loesch_code_geheimnis WHERE id = 1`).Scan(&g); err != nil {
		return nil, fmt.Errorf("loesch_codes.Geheimnis: lesen: %w", err)
	}
	if len(g) < 32 {
		return nil, errors.New("loesch_codes.Geheimnis: zu kurz")
	}
	r.geheimnis = g
	return g, nil
}

// Verbrauche vermerkt den Zufallswert eines Codes bis zu seinem Ablauf. War
// er schon vermerkt: ErrLoeschCodeVerbraucht. Abgelaufene Einträge fallen
// dabei weg (nach dem Ablauf lehnt die HMAC-Prüfung den Code ohnehin ab) —
// nur bei gestellter Uhr (Wissen §38): Liefe die Uhr vor und danach wieder
// zurück, wäre ein noch gültiger Code aus der Liste gefallen. Liegen bleiben
// schadet nicht.
func (r *LoeschCodeRepo) Verbrauche(ctx context.Context, zufall string, ablauf time.Time) error {
	if uhr.Erlaubt("loesch-codes", nil) {
		if _, err := r.db.ExecContext(ctx, `DELETE FROM loesch_codes_verbraucht WHERE ablauf_unix < ?`, time.Now().Unix()); err != nil {
			return fmt.Errorf("loesch_codes.Verbrauche: aufräumen: %w", err)
		}
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO loesch_codes_verbraucht (zufall, ablauf_unix) VALUES (?, ?)`, zufall, ablauf.Unix())
	if err != nil {
		return fmt.Errorf("loesch_codes.Verbrauche: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("loesch_codes.Verbrauche: %w", err)
	} else if n == 0 {
		return ErrLoeschCodeVerbraucht
	}
	return nil
}

// Freigeben nimmt einen Vermerk zurück — nur für einen Code, mit dem
// nachweislich nichts geschrieben wurde (Ablehnung vor dem COMMIT). Der Code
// ist danach wieder genau so gültig wie vorher, also auch nur bis zu seinem
// Ablauf und nur für denselben Stand.
func (r *LoeschCodeRepo) Freigeben(ctx context.Context, zufall string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM loesch_codes_verbraucht WHERE zufall = ?`, zufall); err != nil {
		return fmt.Errorf("loesch_codes.Freigeben: %w", err)
	}
	return nil
}
