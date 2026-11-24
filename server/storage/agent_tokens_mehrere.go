package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Personen-Token für mehrere Boards (ADR-0139 §5, Entscheid E-1, Karte #754).
//
// Ein solches Token (agent_tokens.mehrere = 1) gehört einer Person und trägt
// eine feste Liste von Boards (agent_token_boards). Es hat selbst kein Board.
// Die wirksame Rolle gibt es nur je Board und je Aufruf: das Minimum aus
// Token-Rolle und dem PERSÖNLICHEN ACL-Eintrag der Person auf genau diesem
// Board (ADR-0130 §6, ADR-0132 §6 — die Domain-Freigabe zählt nicht).

// mehrBoardListeIn prüft in der Anlage-Transaktion die gewünschten Boards und
// liefert die bereinigte, sortierte Liste. Jedes Board muss existieren, darf
// nicht das Service-Board sein, und die Person braucht darauf einen
// persönlichen Eintrag mit mindestens editor; bei Selbstbedienung zusätzlich
// den eingeschalteten Schalter. Bei AlleMeineBoards ist es die Menge aller
// Boards, die das JETZT erfüllen. Eine leere Liste ist ein Fehler — ein Token
// ohne Board erlaubte nichts und täuschte einen Zugang vor.
func mehrBoardListeIn(ctx context.Context, tx *sql.Tx, a AgentTokenAnlage) ([]string, error) {
	var email string
	err := tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, a.UserID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("agent_tokens mehrere person: %w", err)
	}
	// Alle Boards, für die diese Person einen Agenten bekommen darf — dieselbe
	// Regel für die Liste und für „alle meine Boards“.
	bedingung := ""
	if a.Selbstbedienung {
		bedingung = " AND b.agent_selbstbedienung = 1"
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT b.id FROM boards b JOIN acl a ON a.board_id = b.id
		  WHERE a.user_email = ? AND a.role IN ('owner', 'editor') AND b.id <> ?`+bedingung,
		email, ServiceBoardID)
	if err != nil {
		return nil, fmt.Errorf("agent_tokens mehrere boards: %w", err)
	}
	erlaubt := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("agent_tokens mehrere boards scan: %w", err)
		}
		erlaubt[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent_tokens mehrere boards rows: %w", err)
	}

	var liste []string
	if a.AlleMeineBoards {
		for id := range erlaubt {
			liste = append(liste, id)
		}
	} else {
		gesehen := map[string]bool{}
		for _, id := range a.BoardIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("%w: leere board-id in der liste", ErrAgentEingabe)
			}
			if gesehen[id] {
				continue
			}
			gesehen[id] = true
			if !erlaubt[id] {
				// Nicht vorhanden, Service-Board, keine persönliche Rolle ≥ editor
				// oder Schalter aus: dieselbe Antwort für alles (F-1335 — die
				// Anlage verrät nicht, welche Boards es gibt).
				return nil, ErrAgentKeinZugang
			}
			liste = append(liste, id)
		}
	}
	if len(liste) == 0 {
		return nil, ErrAgentKeinZugang
	}
	if len(liste) > AgentTokenMaxBoards {
		return nil, fmt.Errorf("%w: höchstens %d boards je token", ErrAgentEingabe, AgentTokenMaxBoards)
	}
	sort.Strings(liste)
	return liste, nil
}

// mehrBoardRollenAbfrage: je Board der Liste die persönliche ACL-Rolle der
// Person — nur für ein aktives Mehr-Board-Token (nicht widerrufen, nicht
// abgelaufen, Person vorhanden). Boards ohne persönlichen Eintrag fehlen.
const mehrBoardRollenAbfrage = `SELECT tb.board_id, t.role, a.role
  FROM agent_tokens t
  JOIN users u ON u.id = t.user_id
  JOIN agent_token_boards tb ON tb.token_id = t.id
  JOIN acl a ON a.board_id = tb.board_id AND a.user_email = u.email
 WHERE t.id = ? AND t.mehrere = 1 AND t.delegierend = 0 AND t.revoked_at IS NULL
   AND (t.expires_at IS NULL OR t.expires_at > ?) AND tb.board_id <> ?`

// RolleAufBoard liefert die wirksame Rolle eines Mehr-Board-Tokens auf einem
// Board: das Minimum aus Token-Rolle und persönlicher ACL-Rolle der Person.
// "" heisst kein Zugriff — Board nicht in der Liste, keine persönliche Rolle
// (mehr), Service-Board, Token nicht (mehr) aktiv oder kein Mehr-Board-Token.
// Bei jedem Aufruf neu gelesen (ADR-0139 §5: je Aufruf gegen Liste und ACL).
func (r *AgentTokenRepo) RolleAufBoard(ctx context.Context, tokenID, boardID string) (string, error) {
	if boardID == "" || boardID == ServiceBoardID {
		return "", nil
	}
	var b, tokenRolle, aclRolle string
	err := r.db.QueryRowContext(ctx, mehrBoardRollenAbfrage+` AND tb.board_id = ?`,
		tokenID, timeNow().UTC().Format(agentAblaufFormat), ServiceBoardID, boardID).Scan(&b, &tokenRolle, &aclRolle)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("agent_tokens.RolleAufBoard: %w", err)
	}
	return KappeAgentRolle(tokenRolle, aclRolle), nil
}

// BoardRollen liefert für ein Mehr-Board-Token alle Boards, auf denen es
// gerade wirkt, mit der wirksamen Rolle — für die Werkzeuge ohne Board im
// Pfad (`boards`, `termine_lesen`). Dieselbe Regel wie RolleAufBoard.
func (r *AgentTokenRepo) BoardRollen(ctx context.Context, tokenID string) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, mehrBoardRollenAbfrage,
		tokenID, timeNow().UTC().Format(agentAblaufFormat), ServiceBoardID)
	if err != nil {
		return nil, fmt.Errorf("agent_tokens.BoardRollen: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var b, tokenRolle, aclRolle string
		if err := rows.Scan(&b, &tokenRolle, &aclRolle); err != nil {
			return nil, fmt.Errorf("agent_tokens.BoardRollen scan: %w", err)
		}
		if rolle := KappeAgentRolle(tokenRolle, aclRolle); rolle != "" {
			out[b] = rolle
		}
	}
	return out, rows.Err()
}

// boardListenFuellen trägt in eine Token-Liste die Board-Listen der
// Mehr-Board-Tokens ein (Anzeige im Admin- und Selbstbedienungs-Dialog).
// Boards, die inzwischen gelöscht sind, fehlen (Fremdschlüssel, Audit
// agent-token-board-removed).
func (r *AgentTokenRepo) boardListenFuellen(ctx context.Context, tokens []AgentToken) error {
	idx := map[string]int{}
	for i, t := range tokens {
		if t.Mehrere {
			idx[t.ID] = i
			tokens[i].Boards = []string{}
		}
	}
	if len(idx) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT token_id, board_id FROM agent_token_boards ORDER BY board_id`)
	if err != nil {
		return fmt.Errorf("agent_tokens boards lesen: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tid, bid string
		if err := rows.Scan(&tid, &bid); err != nil {
			return fmt.Errorf("agent_tokens boards scan: %w", err)
		}
		if i, ok := idx[tid]; ok {
			tokens[i].Boards = append(tokens[i].Boards, bid)
		}
	}
	return rows.Err()
}
