package api_test

// attachments_routes_test.go — sichert das Ist-Bild aus dem
// ADR-0044-Nachtrag vom 2026-08-19 (F-201, Karte #412) gegen künftiges
// stilles Verschieben ab. Der Nachtrag hält fest: die Attachment-API hängt
// direkt an /boards/{boardId}/attachments/{cardId}[/{attId}] — NICHT an
// /boards/{boardId}/cards/{cardId}/attachments (das war die nie
// implementierte Ursprungsfassung aus ADR-0044 §"Attachments-Speicherung").
//
// Die Tests prüfen die Route selbst (per ServeMux.Handler-Introspektion),
// nicht nur, dass irgendein Handler existiert — ein Test, der nur einen
// Response-Status prüft, könnte durch einen zufällig passenden Fallback
// grün bleiben, obwohl sich das URL-Muster verschoben hat.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

// TestAttachmentRoutes_URLHierarchie_F201 verifiziert alle vier
// registrierten Attachment-Routen gegen die im ADR-0044-Nachtrag
// dokumentierte Hierarchie.
//
// Rot-Bedingung: Jemand ändert server/api/attachments.go Register() — z. B.
// beim (fälschlichen) Versuch, die ADR-Ursprungshierarchie
// (.../cards/{cardId}/attachments) nachträglich umzusetzen, oder verschiebt
// eine der vier Routen anders — und vergisst, diesen Test (und den
// ADR-Nachtrag) mitzuziehen.
func TestAttachmentRoutes_URLHierarchie_F201(t *testing.T) {
	a := api.NewAttachmentsAPI(nil, 0)
	mux := http.NewServeMux()
	a.Register(mux)

	cases := []struct {
		name    string
		method  string
		path    string
		pattern string
	}{
		{"upload", http.MethodPost, "/boards/b1/attachments/c1", "POST /boards/{boardId}/attachments/{cardId}"},
		{"download", http.MethodGet, "/boards/b1/attachments/c1/a1", "GET /boards/{boardId}/attachments/{cardId}/{attId}"},
		{"delete-eins", http.MethodDelete, "/boards/b1/attachments/c1/a1", "DELETE /boards/{boardId}/attachments/{cardId}/{attId}"},
		{"delete-karte", http.MethodDelete, "/boards/b1/attachments/c1", "DELETE /boards/{boardId}/attachments/{cardId}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			_, pattern := mux.Handler(req)
			if pattern != tc.pattern {
				t.Fatalf("Pattern = %q, erwartet %q (ADR-0044-Nachtrag 2026-08-19 veraltet?)", pattern, tc.pattern)
			}
		})
	}
}

// TestAttachmentRoutes_ADRUrsprungshierarchieNichtRegistriert_F201 hält
// fest, dass die im ADR-0044-Ursprungstext beschriebene Hierarchie
// (.../cards/{cardId}/attachments) nie implementiert wurde.
//
// Rot-Bedingung: Jemand registriert diese Route zusätzlich (z. B. als
// vermeintliche ADR-Konformität), ohne den ADR-0044-Nachtrag zu pflegen —
// dann existieren zwei URL-Hierarchien für dieselbe Ressource nebeneinander,
// und genau das soll dieser Test verhindern.
func TestAttachmentRoutes_ADRUrsprungshierarchieNichtRegistriert_F201(t *testing.T) {
	a := api.NewAttachmentsAPI(nil, 0)
	mux := http.NewServeMux()
	a.Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/boards/b1/cards/c1/attachments", nil)
	_, pattern := mux.Handler(req)
	if pattern != "" {
		t.Fatalf("ADR-Ursprungsroute matcht unerwartet Pattern %q", pattern)
	}
}
