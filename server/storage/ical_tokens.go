package storage

// ical_tokens.go — iCal-Subscription-Token-Storage (#308 Etappe 6, ADR-0103).
//
// Ein Token ist ein Bearer-Secret in der Feed-URL. Speicherung zweispurig:
//   - token_hash (SHA-256-Hex): UNIQUE-Lookup beim Feed-Request. Der
//     Hash-Treffer validiert den Token — kein Decrypt zum Prüfen nötig.
//   - token_enc (AES-256-GCM via secretcrypto.Box): nur, damit die Settings-UI
//     die Abo-URL erneut anzeigen kann (encrypted-at-rest).
//
// box MUSS != nil sein (analog LLMKeyRepo) — ohne Master-Key kein Token-Pfad;
// der Aufrufer (main.go) bringt das Repo nur auf, wenn ein Key konfiguriert
// ist, sonst antwortet der Handler 503.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/google/uuid"
)

// ErrICalTokenNotFound signalisiert einen unbekannten/rotierten Token.
var ErrICalTokenNotFound = errors.New("storage: ical subscription token nicht gefunden")

// ICalSubscription beschreibt ein Abo. Token ist der ENTSCHLÜSSELTE Klartext —
// nur in den Rückgaben von Create/ListForUser gesetzt (für die Abo-URL),
// niemals in Resolve (dort interessiert nur Scope/Owner).
type ICalSubscription struct {
	ID         string
	UserID     string
	BoardID    *string // nil → Cross-Board (alle Boards mit Lese-Zugriff)
	Token      string  // Klartext, nur bei Create/ListForUser
	CreatedAt  string
	LastUsedAt string
	// Aussen: Abo „für außen“ aus der Zeit der Außenstelle (ADR-0138 §6,
	// zurückgebaut mit Karte #795, ADR-0140 §11). Neue gibt es nicht mehr;
	// ein Bestand-Abo öffnet keinen Feed und lässt sich nur löschen. Gesetzt,
	// sobald eine der beiden Außen-Spalten belegt ist (fail-closed, F-1971).
	Aussen bool
	// Defekt: das Token ist nicht entschlüsselbar (z. B. nach einem
	// Master-Key-Wechsel). Nur in ListForUser gesetzt, dann ohne Token — die
	// Zeile bleibt sichtbar, damit der Besitzer sie löschen kann (F-A).
	Defekt bool
}

// ICalTokenRepo verwaltet ical_subscriptions. box != nil zwingend.
type ICalTokenRepo struct {
	db  *sql.DB
	box *secretcrypto.Box
}

func NewICalTokenRepo(db *sql.DB, box *secretcrypto.Box) *ICalTokenRepo {
	return &ICalTokenRepo{db: db, box: box}
}

// hashToken bildet den deterministischen Lookup-Hash (SHA-256-Hex).
func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// newToken erzeugt ein 32-Byte-Zufallstoken als URL-sicheren Base64-String
// (43 Zeichen, kein Padding).
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ical-token: rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Create legt ein neues Abo an. boardID == nil → Cross-Board. Gibt das Abo
// inkl. Klartext-Token zurück (einzige Stelle neben ListForUser, an der der
// Klartext sichtbar wird).
func (r *ICalTokenRepo) Create(ctx context.Context, userID string, boardID *string) (*ICalSubscription, error) {
	if userID == "" {
		return nil, fmt.Errorf("ical-token.create: leere user_id")
	}
	plaintext, err := newToken()
	if err != nil {
		return nil, err
	}
	enc, err := r.box.SealString(plaintext)
	if err != nil {
		return nil, fmt.Errorf("ical-token.create seal: %w", err)
	}
	id := uuid.NewString()
	created := nowISO()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO ical_subscriptions (id, user_id, board_id, token_hash, token_enc, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, boardID, hashToken(plaintext), enc, created); err != nil {
		return nil, fmt.Errorf("ical-token.create insert: %w", err)
	}
	return &ICalSubscription{
		ID: id, UserID: userID, BoardID: boardID,
		Token: plaintext, CreatedAt: created,
	}, nil
}

// ListForUser liefert alle Abos eines Users mit ENTSCHLÜSSELTEM Token (für die
// Settings-UI-Abo-URL). Eine nicht entschlüsselbare Zeile (kaputter
// Master-Key-Wechsel) kippt den Call nicht und fällt auch nicht weg: sie kommt
// mit Defekt=true, ohne Token, damit die Oberfläche sie zum Löschen anbieten
// kann (Kritiker Sprint 88, F-A). Ein Bestand-Abo „für außen“ kommt mit
// Aussen=true und ohne Token: es öffnet keinen Feed (Karte #795).
func (r *ICalTokenRepo) ListForUser(ctx context.Context, userID string) ([]ICalSubscription, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, board_id, token_enc, created_at, coalesce(last_used_at, ''),
		        aussen_hash IS NOT NULL OR aussen_enc IS NOT NULL
		 FROM ical_subscriptions WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("ical-token.list: %w", err)
	}
	defer rows.Close()
	var out []ICalSubscription
	for rows.Next() {
		var s ICalSubscription
		var enc string
		var boardID sql.NullString
		if err := rows.Scan(&s.ID, &boardID, &enc, &s.CreatedAt, &s.LastUsedAt, &s.Aussen); err != nil {
			return nil, fmt.Errorf("ical-token.list scan: %w", err)
		}
		s.UserID = userID
		if boardID.Valid {
			b := boardID.String
			s.BoardID = &b
		}
		// Bestand „für außen“: kein Token in der Anzeige, kein Defekt —
		// es gibt nur noch Löschen (Karte #795).
		if !s.Aussen {
			if plain, derr := r.box.OpenString(enc); derr == nil {
				s.Token = plain
			} else {
				s.Defekt = true
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Resolve schlägt einen eingehenden Klartext-Token per Hash nach und liefert
// Owner + Scope. Schreibt nichts — last_used_at setzt Touch, und zwar erst,
// wenn der Abruf die Drossel passiert hat (F-2109, Kritik K-3). Token NICHT in der
// Rückgabe (Resolve dient der Feed-Auth, nicht der Anzeige).
func (r *ICalTokenRepo) Resolve(ctx context.Context, plaintext string) (*ICalSubscription, error) {
	if plaintext == "" {
		return nil, ErrICalTokenNotFound
	}
	var s ICalSubscription
	var boardID sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, board_id, created_at, coalesce(last_used_at, ''),
		        aussen_hash IS NOT NULL OR aussen_enc IS NOT NULL
		 FROM ical_subscriptions WHERE token_hash = ?`, hashToken(plaintext)).
		Scan(&s.ID, &s.UserID, &boardID, &s.CreatedAt, &s.LastUsedAt, &s.Aussen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrICalTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("ical-token.resolve: %w", err)
	}
	if boardID.Valid {
		b := boardID.String
		s.BoardID = &b
	}
	return &s, nil
}

// Touch setzt last_used_at eines Abos, best-effort — Fehler sind für die
// Auth irrelevant.
func (r *ICalTokenRepo) Touch(ctx context.Context, id string) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE ical_subscriptions SET last_used_at = ? WHERE id = ?`, nowISO(), id)
}

// Delete entfernt ein Abo des Users (own-only — id UND user_id müssen passen,
// kein IDOR). Kein Fehler, wenn nichts getroffen wird (idempotent).
func (r *ICalTokenRepo) Delete(ctx context.Context, userID, id string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM ical_subscriptions WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return fmt.Errorf("ical-token.delete: %w", err)
	}
	return nil
}
