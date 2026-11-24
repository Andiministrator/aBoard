package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Test-Zugang für vorschlaege_783_test.go (Paket api_test, Karte #783). Nur in
// Tests kompiliert.

// SetzeAgentenOpsGrenzeFuerTest ersetzt den Ratenbegrenzer der Agenten am
// Ops-Weg durch einen mit max Batches je Minute. Nach WithFold aufrufen.
func (a *BoardsAPI) SetzeAgentenOpsGrenzeFuerTest(max int) {
	a.opsLimiterAgent = newRateLimiter(max, opsRateFenster)
}

// InternWerkzeugDarfFuerTest: werkzeugDarf für einen Principal der Rolle
// intern (ADR-0140 §8, Stelle 2). agentDarf lässt diese Wege für die Rolle
// gar nicht erst durch; werkzeugDarf läuft aber auch im MCP-Schritt B und
// muss die Rolle deshalb selbst ausschliessen.
func InternWerkzeugDarfFuerTest(methode string, segs []string, nurTokenRolle bool) bool {
	p := &AgentPrincipal{Name: "assistent", BoardID: "b1", Role: storage.AgentRolleIntern, tokenRolle: storage.AgentRolleIntern}
	if nurTokenRolle {
		p.Role = storage.AgentRolleEditor
	}
	return werkzeugDarf(p, methode, segs)
}

// SetzeWerkzeugLeseGrenzeFuerTest ersetzt die Anfragen-Grenze der Werkzeuge
// (werkzeugLesenErlaubt) — sie gilt seit Kritik K-A6 auch für die schreibenden
// Wege der Rolle intern.
func (a *BoardsAPI) SetzeWerkzeugLeseGrenzeFuerTest(max int) {
	a.werkzeugZustand()
	a.werkzeugLeser = newRateLimiter(max, time.Minute)
}

// VorschlagAusfuehrenFuerTest: vorschlagAusfuehren mit einem Anfrage-Kontext
// nach Wahl — für den abgebrochenen Browser (F-2090). Liefert Status und Antwort.
func (a *BoardsAPI) VorschlagAusfuehrenFuerTest(ctx context.Context, board, email string, v *storage.Vorschlag) (int, string) {
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	p := a.vorschlagAusfuehren(r, board, email, v)
	return p.status, p.body.String()
}
