package api

// llm_keys.go — REST-Endpunkte für /users/me/llm-keys/* (#298, ADR-0094).
//
// ACL: own-User-only — kein Path-Segment {user_id}; der User wird strikt
// aus IdentityFromContext + NormalizeEmail aufgelöst (kein IDOR möglich).
// Rate-Limit: 10 Mutationen pro Stunde pro User.
//
// Test-Calls (POST .../test und der Pre-Save-Test im POST) laufen über
// die llm.Registry-Strategy (server/llm). Provider-Liste wird vom Server
// im Init-Pfad registriert (main.go: llm.RegisterDefaults).

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// LLMKeysAPI bündelt die Abhängigkeiten für /users/me/llm-keys/*.
// Wenn keys == nil (= kein Master-Key konfiguriert), antworten alle Routen
// mit 503 — der Aufrufer in main.go darf das LLMKeysAPI mit nil keys
// initialisieren, um die Routes registriert zu lassen, aber funktional
// deaktiviert (Read-Only-Modus für Secrets, analog #306).
type LLMKeysAPI struct {
	keys     *storage.LLMKeyRepo
	users    *storage.UserRepo
	registry *llm.Registry
	limiter  *rateLimiter
	// Karte #570 (K-B1/K-B2): Ist der Anbieter „openai“ auf ein eigenes
	// Gegenüber umgestellt, erfährt die Oberfläche Ziel und Modellnamen —
	// damit Schlüsseldialog und Datenschutz-Hinweis den echten Empfänger
	// nennen und das Modell-Dropdown die Modelle des Gegenübers anbietet.
	openAIBasis   string
	openAIModelle []string
	// Karte #664 (ADR-0133 §2): nil = kein Firmenmodus. Siehe
	// llm_firmenmodus.go.
	firmenmodus *llm.Firmenmodus
	pii         llm.PIIVorgabe
}

// WithOpenAIKompatibel trägt Basis-URL und Modellnamen ein (leer = OpenAI).
func (a *LLMKeysAPI) WithOpenAIKompatibel(basis string, modelle []string) *LLMKeysAPI {
	a.openAIBasis = basis
	a.openAIModelle = modelle
	return a
}

func NewLLMKeysAPI(keys *storage.LLMKeyRepo, users *storage.UserRepo, registry *llm.Registry) *LLMKeysAPI {
	return &LLMKeysAPI{
		keys:     keys,
		users:    users,
		registry: registry,
		limiter:  newRateLimiter(10, time.Hour),
	}
}

func (a *LLMKeysAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /users/me/llm-keys", a.protect(a.handleList, false))
	mux.Handle("POST /users/me/llm-keys/{provider}", a.protect(a.handleSet, true))
	mux.Handle("DELETE /users/me/llm-keys/{provider}", a.protect(a.handleDelete, true))
	mux.Handle("POST /users/me/llm-keys/{provider}/test", a.protect(a.handleTest, true))
}

// protect prüft Forward-Auth-Identität, lädt den User-Row und (optional)
// erzwingt Rate-Limit. Übergibt den userID via Context an den Handler.
func (a *LLMKeysAPI) protect(h func(w http.ResponseWriter, r *http.Request, userID string), rateLimit bool) http.Handler {
	return WithDefaults(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromContext(r.Context())
		if id == nil {
			writeError(w, http.StatusForbidden, "forbidden: llm-keys require authentication")
			return
		}
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
		u, err := a.users.GetByEmail(r.Context(), email)
		if errors.Is(err, storage.ErrUserNotFound) {
			writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		// 503: Master-Key fehlt → Endpoints deaktiviert. Im Firmenmodus
		// braucht die Liste das Repo nicht (Karte #664); Speichern und Testen
		// lehnen dort ohnehin ab, Löschen prüft selbst.
		if a.keys == nil && !a.firmenmodus.Aktiv() {
			writeError(w, http.StatusServiceUnavailable, "llm-keys disabled: configure ABOARD_SECRET_KEY")
			return
		}
		if rateLimit {
			if !a.limiter.allow(u.ID) {
				writeError(w, http.StatusTooManyRequests, "rate limit: 10 mutations per hour per user")
				return
			}
		}
		h(w, r, u.ID)
	}))
}

func (a *LLMKeysAPI) handleList(w http.ResponseWriter, r *http.Request, userID string) {
	// Karte #664: Im Firmenmodus werden gespeicherte Nutzerschlüssel nicht
	// benutzt — also auch nicht als verfügbar gemeldet. Sie bleiben in der
	// Datenbank (ADR-0133 §2), die Liste ist leer.
	list := []storage.LLMKeyMeta{}
	if !a.firmenmodus.Aktiv() {
		var err error
		list, err = a.keys.List(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "llm-keys list failed", err)
			return
		}
	}
	providers := make([]string, 0, len(list))
	for _, m := range list {
		providers = append(providers, m.Provider)
	}
	body := map[string]any{
		"providers": providers,
		"keys":      list, /* metadaten (provider/created_at/last_used_at) — KEIN Klartext */
	}
	// Karte #664: Firmenmodus und PII-Vorgabe nur melden, wenn sie vom
	// heutigen Verhalten abweichen — ohne die neuen Variablen bleibt die
	// Antwort bit-identisch.
	if a.firmenmodus.Aktiv() {
		body["firmenmodus"] = a.firmenmodusAntwort()
	}
	if a.pii == llm.PIIAus || a.pii == llm.PIIPflicht {
		body["pii"] = string(a.pii)
	}
	// F-1549: ABOARD_LLM_OPENAI_WERKZEUGE=aus meldet der Server der Oberfläche,
	// damit sie für „openai“ weder Änderungsvorschläge noch Karten-Nachladen
	// anbietet (beides hängt an Werkzeugen). Nur bei „aus“ vorhanden, sonst
	// bleibt die Antwort bit-identisch.
	if llm.OpenAIWerkzeugeAus() {
		body["openai_werkzeuge"] = "aus"
	}
	if a.openAIBasis != "" {
		/* Nur der Host, nicht die ganze URL: Mehr braucht die Oberfläche für
		   den Hinweis nicht. Die Basis ist geprüft (keine Zugangsdaten). */
		ziel := a.openAIBasis
		if u, err := url.Parse(a.openAIBasis); err == nil {
			ziel = u.Host
		}
		modelle := a.openAIModelle
		if modelle == nil {
			modelle = []string{}
		}
		body["openai_kompatibel"] = map[string]any{"ziel": ziel, "modelle": modelle}
	}
	writeJSON(w, http.StatusOK, body)
}

// lehneImFirmenmodusAb antwortet 409 kind „vom-betreiber-eingerichtet“,
// wenn der Firmenmodus gilt (Karte #664). true → Antwort geschrieben.
func (a *LLMKeysAPI) lehneImFirmenmodusAb(w http.ResponseWriter) bool {
	if !a.firmenmodus.Aktiv() {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"ok":    false,
		"kind":  KindVomBetreiber,
		"error": "llm access is configured by the operator; personal keys are not used",
	})
	return true
}

func (a *LLMKeysAPI) handleSet(w http.ResponseWriter, r *http.Request, userID string) {
	// Karte #664: Im Firmenmodus kein Speichern — auch kein Test-Call, der
	// einen privaten Schlüssel an das Firmen-Gegenüber schicken würde.
	if a.lehneImFirmenmodusAb(w) {
		return
	}
	provider := r.PathValue("provider")
	if !a.providerAllowed(provider) {
		writeError(w, http.StatusBadRequest, "unknown provider")
		return
	}
	// Sprint-40-Kritiker-Fix [S]P3: Body-Size-Limit gegen authentifizierten-
	// User-DoS. Real-Provider-Keys sind <1 KB; 8 KB ist mehr als großzügig.
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	body.Key = strings.TrimSpace(body.Key)
	if body.Key == "" {
		writeError(w, http.StatusBadRequest, "key required")
		return
	}

	// Vor dem Speichern Test-Call gegen den Provider. Bei 401 →
	// 422 "key invalid". Bei Netzwerk-Fehler → 502 (kein Speichern, weil
	// keine Aussage über Key-Gültigkeit möglich).
	prov, err := a.registry.Get(provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider not registered")
		return
	}
	if err := prov.TestKey(r.Context(), body.Key); err != nil {
		if errors.Is(err, llm.ErrInvalidKey) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"ok":    false,
				"error": "key invalid",
			})
			return
		}
		writeError(w, http.StatusBadGateway, "provider unreachable", err)
		return
	}

	if err := a.keys.Set(r.Context(), userID, provider, body.Key); err != nil {
		writeError(w, http.StatusInternalServerError, "llm-keys set failed", err)
		return
	}
	// Erfolgreicher TestKey → last_used_at sofort markieren (Best-effort).
	_ = a.keys.MarkUsed(r.Context(), userID, provider)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *LLMKeysAPI) handleDelete(w http.ResponseWriter, r *http.Request, userID string) {
	// Karte #664: Löschen des EIGENEN gespeicherten Schlüssels bleibt im
	// Firmenmodus erlaubt (eigene Daten) — braucht aber das Repo.
	if a.keys == nil {
		writeError(w, http.StatusServiceUnavailable, "llm-keys disabled: configure ABOARD_SECRET_KEY")
		return
	}
	provider := r.PathValue("provider")
	if !a.providerAllowed(provider) {
		writeError(w, http.StatusBadRequest, "unknown provider")
		return
	}
	if err := a.keys.Delete(r.Context(), userID, provider); err != nil {
		writeError(w, http.StatusInternalServerError, "llm-keys delete failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTest testet den GESPEICHERTEN Key (ohne Schreibversuch). Nützlich
// nach Provider-Plan-Wechsel oder zum Pre-Send-Check.
func (a *LLMKeysAPI) handleTest(w http.ResponseWriter, r *http.Request, userID string) {
	// Karte #664: Ein gespeicherter Nutzerschlüssel ginge sonst an das
	// Firmen-Gegenüber.
	if a.lehneImFirmenmodusAb(w) {
		return
	}
	provider := r.PathValue("provider")
	if !a.providerAllowed(provider) {
		writeError(w, http.StatusBadRequest, "unknown provider")
		return
	}
	key, err := a.keys.Get(r.Context(), userID, provider)
	if errors.Is(err, storage.ErrLLMKeyNotFound) {
		writeError(w, http.StatusNotFound, "no key stored")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "llm-keys read failed", err)
		return
	}
	prov, err := a.registry.Get(provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider not registered")
		return
	}
	if err := prov.TestKey(r.Context(), key); err != nil {
		if errors.Is(err, llm.ErrInvalidKey) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"ok":    false,
				"error": "key invalid",
			})
			return
		}
		writeError(w, http.StatusBadGateway, "provider unreachable", err)
		return
	}
	_ = a.keys.MarkUsed(r.Context(), userID, provider)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// providerAllowed prüft gegen die Registry — Doppel-Gürtel zum
// CHECK-Constraint im SQL. Schützt vor Bad-Input vor dem Provider-Test-
// Call.
func (a *LLMKeysAPI) providerAllowed(name string) bool {
	if name == "" {
		return false
	}
	_, err := a.registry.Get(name)
	return err == nil
}
