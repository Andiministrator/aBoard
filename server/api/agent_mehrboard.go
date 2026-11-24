package api

import (
	"context"
	"net/http"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Personen-Token für mehrere Boards (ADR-0139 §5, Entscheid E-1, Karte #754).
//
// Ein solches Token trägt eine feste Liste von Boards. Welches Board eine
// Anfrage meint, steht im Werkzeug-Pfad (`/werkzeuge/boards/{id}/…`); dieses
// Board wird bei JEDEM Aufruf gegen die Liste und gegen den persönlichen
// ACL-Eintrag der Person geprüft (AgentTokenRepo.RolleAufBoard). Die rohen
// `/boards/{id}/…`-Wege erreicht es nicht — die bleiben bei einem Board je
// Token. Aufbau wie beim delegierenden Token (delegierterPrincipalFuer), damit
// Middleware und MCP-Schritt B dieselbe Funktion benutzen.

// mehrBoardPruefer ist der Teil von storage.AgentTokenRepo, den ein
// Mehr-Board-Token braucht. Ein Prüfer ohne diese Methoden (Test-Stubs)
// lässt kein solches Token durch — Ausfall zu.
type mehrBoardPruefer interface {
	RolleAufBoard(ctx context.Context, tokenID, boardID string) (string, error)
	BoardRollen(ctx context.Context, tokenID string) (map[string]string, error)
}

// mehrBoardNurWerkzeuge: Meldung für die rohen Wege. Sie nennt den Grund,
// damit ein falsch eingerichteter Client (aboard-op mit diesem Token) den
// Fehler versteht.
const mehrBoardNurWerkzeuge = "forbidden for agent: a token for several boards may only use the tool endpoints (/werkzeuge/…, /openapi.json, /mcp); raw ops need a token for one board"

// mehrBoardPrincipalFuer bildet den Principal eines Mehr-Board-Tokens für den
// Pfad segs. 403 für jedes Board, das nicht in der Liste steht, auf dem die
// Person keine persönliche Rolle (mehr) hat, das es nicht gibt oder das das
// Service-Board ist — dieselbe Antwort für alles, damit sie nicht verrät,
// welche Boards es gibt.
func (z *AgentZugang) mehrBoardPrincipalFuer(ctx context.Context, segs []string, segsOK bool, t *storage.AgentToken) (*AgentPrincipal, int, string) {
	mp, ok := z.tokens.(mehrBoardPruefer)
	if !ok || t.UserEmail == "" {
		return nil, http.StatusForbidden, "forbidden for agent"
	}
	basis := AgentPrincipal{TokenID: t.ID, Name: t.Name, OnBehalfOf: t.UserEmail, MehrBoard: true, tokenRolle: t.Role}
	if segsOK && (werkzeugIstSpezifikation(segs) || werkzeugIstMCP(segs)) {
		// Beschreibung und MCP-Eingang: kein Board, keine Rolle. Was ein
		// tools/call darf, entscheidet Schritt B mit dieser Funktion für den
		// Werkzeug-Pfad.
		return &basis, 0, ""
	}
	if !segsOK || len(segs) == 0 || segs[0] != werkzeugSegment {
		return nil, http.StatusForbidden, mehrBoardNurWerkzeuge
	}
	if werkzeugOhneBoard(segs) {
		rollen, err := mp.BoardRollen(ctx, t.ID)
		if err != nil {
			return nil, http.StatusInternalServerError, "role check failed"
		}
		basis.boardRollen = rollen
		return &basis, 0, ""
	}
	board, ok := agentBoardAusPfad(segs)
	if !ok {
		return nil, http.StatusForbidden, werkzeugVerbotenMeldung
	}
	rolle, err := mp.RolleAufBoard(ctx, t.ID, board)
	if err != nil {
		return nil, http.StatusInternalServerError, "role check failed"
	}
	if rolle == "" {
		return nil, http.StatusForbidden, werkzeugVerbotenMeldung
	}
	basis.BoardID, basis.Role = board, rolle
	return &basis, 0, ""
}

// mehrBoardNochBerechtigt: Die Wache fragt das bei offenen Verbindungen
// zusätzlich zum Token ab (wie delegiertNochBerechtigt).
func (z *AgentZugang) mehrBoardNochBerechtigt(ctx context.Context, p *AgentPrincipal) (bool, error) {
	mp, ok := z.tokens.(mehrBoardPruefer)
	if !ok {
		return false, nil
	}
	rolle, err := mp.RolleAufBoard(ctx, p.TokenID, p.BoardID)
	if err != nil {
		return false, err
	}
	return rolle != "", nil
}
