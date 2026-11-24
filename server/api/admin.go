package api

// admin.go — /admin/*-API + requireAdmin-Middleware (Karte #306, ADR-0101).
//
// Liegt außerhalb der /boards-API → der ACLGuard greift hier nicht; statt
// dessen prüft requireAdmin selbst gegen users.is_admin. Sektion-Test- und
// Reload-Callbacks werden zur Run-Time registriert (Subsystem-Registry,
// ADR-0101 §4) — die einzelnen Sektionen kommen in Folge-Karten (#293/#295/
// #297/#303 etc.) hinzu.
//
// Rate-Limit: 30 Mutationen/Stunde pro Admin (token-bucket im RAM, kein
// DB-Hit pro Request). Im MVP genügt eine schlichte Map[user_id]→bucket;
// bei mehreren Server-Replicas wäre das nicht stateful, aber wir laufen
// single-process.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	mailpkg "github.com/Andiministrator/aBoard/server/mail"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// AdminAPI bündelt die Abhängigkeiten für die /admin/*-Routen.
type AdminAPI struct {
	cfg       *storage.ConfigRepo
	users     *storage.UserRepo
	startedAt time.Time
	version   string
	limiter   *rateLimiter
	registry  *SubsystemRegistry
	// llmOpenAIBaseURL (Karte #570, ADR-0129 §5) — nur lesend im
	// Admin-Status angezeigt (handleStatus). Leer = Default
	// (https://api.openai.com/v1). Es gibt für LLM keine
	// server_config-Konfiguration, deshalb kein PUT-Weg dafür.
	llmOpenAIBaseURL string
	// Firmenmodus und PII-Vorgabe (F-1546, Karte #664) — nur lesend im
	// Admin-Status. Bewusst ohne Schlüssel und ohne Schlüssellänge: Der Status
	// nennt, DASS der Modus gilt, nie womit er bedient wird.
	llmFirmenmodus bool
	llmModelle     []string
	llmPII         string
	// agentZugang (Karte #608): schliesst beim Löschen einer Person die
	// offenen Verbindungen ihrer Agenten. Gesetzt von RegisterAgentTokens.
	agentZugang *AgentZugang
	// boardpaar (F-2021): Kopplungen für die Prüfung beim Seitenwechsel.
	// Gesetzt von RegisterBoardpaar; nil = keine Board-Paar-Verdrahtung.
	boardpaar *storage.BoardpaarRepo
	// boardpaarBasis (Karte #825): ABOARD_PUBLIC_BASE_URL, geprüft — Vorgabe
	// für die Adresse des Boards innen beim Auftrag „Externe Kopie anlegen“.
	boardpaarBasis string
	// boardGeloescht (Karte #831): Schritte nach dem Admin-Notweg „Kopie
	// außen löschen“ — Anhänge, Backup-Grabstein, Lösch-Ereignis, wie
	// DELETE /boards/{id} (BoardsAPI.NachBoardGeloescht). nil = nur die DB.
	boardGeloescht func(ctx context.Context, boardID string)
	// broker (F-2299): Event-Weg zu offenen Browsern; nil = keine Ereignisse.
	broker *EventBroker
	// schleuse (Karte #684, ADR-0134): Schalter, Aufbewahrung und Zahl der
	// freigegebenen/nutzbaren Anbieter im Status. Gesetzt von WithSchleuse
	// bzw. RegisterSchleuse (schleuse.go); nil = Feld fehlt im Status.
	schleuse *SchleuseAPI
	// konfigPruefung (Karte #739): Prüf- und Normalform-Funktionen je
	// Schlüssel-Präfix, die beim Speichern greifen (z. B. "wissen.").
	konfigPruefung map[string]func(key, value string) (string, error)
	// loeschPruefung: dasselbe für DELETE. Eigene Funktion, weil Löschen
	// auch Alteinträge treffen muss, die keine Speichern-Prüfung mehr kennt
	// (Verify #739 W-1).
	loeschPruefung map[string]func(key string) error
	// konfigFolge (Karte #827 E4): je Schlüssel (genau, kein Präfix) eine
	// Folge, die beim Setzen und Löschen in derselben Transaktion läuft.
	konfigFolge map[string]storage.KonfigFolge
	// einladungen (Karte #1076, ADR-0149 §5.3): nur im eingebauten
	// Anmeldebetrieb gesetzt (WithEinladungen); nil = Routen antworten 404.
	einladungen     Einladungen
	einladungBasis  string
	einladungSenden func(ctx context.Context, cfg mailpkg.Config, msg mailpkg.Message) error
	einladungFrist  time.Duration
	// konten (Karte #1077): Bestätigen, Ablehnen, Sperren, Entsperren,
	// Zurücksetzen; nil = Routen antworten 404 (WithKontenVerwaltung).
	konten             KontenVerwaltung
	kontoNutzerAnlegen func(ctx context.Context, email string) error
	kontoGeaendert     func(email, kontoID string)
}

// WithKonfigFolge hängt an genau diesen Schlüssel eine Folge, die Setzen und
// Löschen in derselben Transaktion ausführen (Karte #827 E4: Team-Rolle mit
// boardpaar.rueckweg). Scheitert sie, bleibt die Einstellung unverändert.
func (a *AdminAPI) WithKonfigFolge(key string, fn storage.KonfigFolge) *AdminAPI {
	if a.konfigFolge == nil {
		a.konfigFolge = map[string]storage.KonfigFolge{}
	}
	a.konfigFolge[key] = fn
	return a
}

// WithLoeschPruefung hängt eine Prüfung für DELETE aller Schlüssel mit dem
// Präfix an; ein Fehler geht als 400 mit Grund an den Admin.
func (a *AdminAPI) WithLoeschPruefung(praefix string, fn func(key string) error) *AdminAPI {
	if a.loeschPruefung == nil {
		a.loeschPruefung = map[string]func(key string) error{}
	}
	a.loeschPruefung[praefix] = fn
	return a
}

// WithBoardpaarBasis setzt die öffentliche Basis-Adresse (schon geprüft) als
// Vorgabe für Aufträge „Externe Kopie anlegen“ (Karte #825).
func (a *AdminAPI) WithBoardpaarBasis(basis string) *AdminAPI {
	a.boardpaarBasis = basis
	return a
}

// WithBoardGeloescht hängt die Schritte an, die nach dem Notweg „Kopie außen
// löschen“ laufen (Karte #831) — main.go reicht BoardsAPI.NachBoardGeloescht
// durch, damit Anhänge, Backup-Grabstein und Lösch-Ereignis wie beim
// REST-Löschen entstehen.
func (a *AdminAPI) WithBoardGeloescht(fn func(ctx context.Context, boardID string)) *AdminAPI {
	a.boardGeloescht = fn
	return a
}

// WithBroker reicht den Event-Broker an die Admin-API (F-2299): Der Schalter
// „bearbeitbar“ einer Kopie meldet offenen Browsern schatten:aktualisiert,
// wie es der Hinweg bei einem vom Skript umgestellten Schalter tut.
func (a *AdminAPI) WithBroker(b *EventBroker) *AdminAPI {
	a.broker = b
	return a
}

// WithKonfigPruefung hängt eine Prüfung für alle Schlüssel mit dem Präfix an
// (Karte #739). Die Funktion liefert die Normalform oder einen Fehler, der
// als 400 mit Grund an den Admin geht. Geheimwerte gibt es unter einem
// geprüften Präfix nicht.
func (a *AdminAPI) WithKonfigPruefung(praefix string, fn func(key, value string) (string, error)) *AdminAPI {
	if a.konfigPruefung == nil {
		a.konfigPruefung = map[string]func(key, value string) (string, error){}
	}
	a.konfigPruefung[praefix] = fn
	return a
}

// NewAdminAPI legt die API an. version stammt aus main.aboardVersion
// (ldflag); startedAt ist time.Now() beim Boot.
func NewAdminAPI(cfg *storage.ConfigRepo, users *storage.UserRepo, version string, startedAt time.Time) *AdminAPI {
	return &AdminAPI{
		cfg:       cfg,
		users:     users,
		startedAt: startedAt,
		version:   version,
		limiter:   newRateLimiter(30, time.Hour),
		registry:  NewSubsystemRegistry(),
	}
}

// Registry liefert die Subsystem-Registry zurück. Subsysteme registrieren
// Test/Reload-Hooks per `api.Registry().RegisterTest(...)` aus dem Init-Pfad
// in main.go.
func (a *AdminAPI) Registry() *SubsystemRegistry { return a.registry }

// WithLLMOpenAIBaseURL trägt die konfigurierte Basis-URL des
// OpenAI-kompatiblen Anbieters für den Admin-Status ein (Karte #570). Leer
// bleibt leer — handleStatus lässt das Feld dann aus der Antwort weg.
func (a *AdminAPI) WithLLMOpenAIBaseURL(url string) *AdminAPI {
	a.llmOpenAIBaseURL = url
	return a
}

// WithLLMVorgaben trägt die Betreiber-Vorgaben für den Admin-Status ein
// (F-1546): ob der Firmenmodus gilt, welche Modelle er erlaubt und welche
// PII-Vorgabe (aus|pflicht|nutzer) gilt. Der Serverschlüssel gehört nicht
// hierher und wird nicht übergeben.
func (a *AdminAPI) WithLLMVorgaben(firmenmodus bool, modelle []string, pii string) *AdminAPI {
	a.llmFirmenmodus = firmenmodus
	a.llmModelle = append([]string(nil), modelle...)
	a.llmPII = pii
	return a
}

// Register hängt alle /admin/*-Routen an den Mux. Go-1.22-ServeMux-Pattern
// werden benutzt (Method + Path); der Endpoint-spezifische requireAdmin
// liegt VOR jeder Methode.
func (a *AdminAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /admin/config", a.protect(a.handleConfigList))
	mux.Handle("GET /admin/config/{section}", a.protect(a.handleConfigList))
	mux.Handle("PUT /admin/config/{key}", a.protectMutate(a.handleConfigSet))
	mux.Handle("DELETE /admin/config/{key}", a.protectMutate(a.handleConfigDelete))
	mux.Handle("POST /admin/config/{section}/test", a.protectMutate(a.handleSectionTest))
	mux.Handle("POST /admin/config/reload", a.protectMutate(a.handleReload))
	mux.Handle("GET /admin/audit", a.protect(a.handleAudit))
	mux.Handle("GET /admin/status", a.protect(a.handleStatus))
	mux.Handle("GET /admin/users", a.protect(a.handleUserList))
	mux.Handle("GET /admin/boards", a.protect(a.handleBoardList))
	mux.Handle("PATCH /admin/users/{id}/admin", a.protectMutate(a.handleSetAdminFlag))
	mux.Handle("DELETE /admin/users/{id}", a.protectMutate(a.handleUserDelete))
	mux.Handle("POST /admin/einladungen", a.protectMutate(a.handleEinladen))
	mux.Handle("DELETE /admin/einladungen/{id}", a.protectMutate(a.handleEinladungZurueck))
	mux.Handle("GET /admin/konten", a.protect(a.handleKontenListe))
	mux.Handle("POST /admin/konten/{id}/{aktion}", a.protectMutate(a.handleKontoAktion))
}

// protect umhüllt einen Handler mit requireAdmin (kein Rate-Limit).
// Für Read-Routen.
func (a *AdminAPI) protect(h http.HandlerFunc) http.Handler {
	return WithDefaults(a.requireAdmin(h, false))
}

// protectMutate umhüllt einen Handler mit requireAdmin + Rate-Limit.
func (a *AdminAPI) protectMutate(h http.HandlerFunc) http.Handler {
	return WithDefaults(a.requireAdmin(h, true))
}

// requireAdmin prüft, dass der Request einen Admin-User trägt.
// Im Legacy-Bypass (keine Identität / Single-User-Modus) → 403, weil ohne
// Identität niemand admin sein kann. rateLimit=true → zusätzlich Bucket-Check.
func (a *AdminAPI) requireAdmin(h http.HandlerFunc, rateLimit bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromContext(r.Context())
		if id == nil {
			writeError(w, http.StatusForbidden, "forbidden: admin endpoints require authentication")
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
			writeError(w, http.StatusInternalServerError, "admin lookup failed", err)
			return
		}
		if !u.IsAdmin {
			writeError(w, http.StatusForbidden, "forbidden: not an admin")
			return
		}
		if rateLimit {
			if !a.limiter.allow(u.ID) {
				writeError(w, http.StatusTooManyRequests, "rate limit: 30 mutations per hour per admin")
				return
			}
		}
		// User-ID in den Context schieben, damit Handler sie für Audit lesen.
		ctx := withAdminUserID(r.Context(), u.ID)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------- Handler ----------

func (a *AdminAPI) handleConfigList(w http.ResponseWriter, r *http.Request) {
	section := r.PathValue("section")
	entries, err := a.cfg.List(r.Context(), section)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":         entries,
		"secrets_enabled": a.cfg.SecretsEnabled(),
	})
}

func (a *AdminAPI) handleConfigSet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key required")
		return
	}
	// Sprint-40-Kritiker-Fix [S]P3: Body-Size-Limit (64 KB) gegen
	// authentifizierten-Admin-DoS via riesige JSON-Bodies.
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body struct {
		Value    string `json:"value"`
		IsSecret bool   `json:"is_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// ADR-0138 §2 (Kritiker 07.10., K-4): Eine unbekannte Zeitzone liesse den
	// Feed still in UTC rechnen. Beim Speichern ablehnen; leer heisst Standard.
	if key == KonfigKalenderZeitzone && strings.TrimSpace(body.Value) != "" {
		if _, _, zerr := ladeZeitzone(body.Value); zerr != nil {
			writeError(w, http.StatusBadRequest, zerr.Error())
			return
		}
	}
	// Karte #788 (ADR-0140 §4): Die Seite des Board-Paars nimmt nur aus,
	// innen oder aussen an — gleich, ob sie aus dem Admin-Reiter oder über
	// den allgemeinen Konfig-Weg kommt. Ein ungültiger Wert wirkte zwar wie
	// „aus“, stünde aber still falsch in der Liste. Kein Geheimwert: die
	// Handler lesen den Klartext.
	if key == KonfigBoardpaarSeite {
		norm, berr := PruefeBoardpaarSeite(body.Value)
		if berr == nil && body.IsSecret {
			berr = errors.New(key + ": kein Geheimwert")
		}
		if berr != nil {
			writeError(w, http.StatusBadRequest, berr.Error())
			return
		}
		body.Value = norm
		if werr := PruefeSeitenwechsel(r.Context(), a.cfg, a.boardpaar, norm); werr != nil {
			schreibeSeitenwechselFehler(w, werr)
			return
		}
	}
	for praefix, pruefe := range a.konfigPruefung {
		if !strings.HasPrefix(key, praefix) {
			continue
		}
		if body.IsSecret {
			writeError(w, http.StatusBadRequest, key+": kein Geheimwert")
			return
		}
		norm, perr := pruefe(key, body.Value)
		if perr != nil {
			writeError(w, http.StatusBadRequest, perr.Error())
			return
		}
		body.Value = norm
	}
	uid := adminUserIDFromContext(r.Context())
	if err := a.cfg.SetMitFolge(r.Context(), key, body.Value, body.IsSecret, uid, a.konfigFolge[key]); err != nil {
		if errors.Is(err, storage.ErrSecretsUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "secrets disabled: configure ABOARD_SECRET_KEY")
			return
		}
		if konfigFolgeKonflikt(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "config set failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *AdminAPI) handleConfigDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key required")
		return
	}
	// Verify #739 V-2: Löschen darf keinen Zustand herstellen, den Speichern
	// verbietet — sonst wäre DELETE der Weg vorbei.
	for praefix, pruefe := range a.loeschPruefung {
		if strings.HasPrefix(key, praefix) {
			if perr := pruefe(key); perr != nil {
				writeError(w, http.StatusBadRequest, perr.Error())
				return
			}
		}
	}
	// F-2021: Löschen der Seite heisst „aus“ — derselbe Wechsel, dieselbe Sperre.
	if key == KonfigBoardpaarSeite {
		if werr := PruefeSeitenwechsel(r.Context(), a.cfg, a.boardpaar, BoardpaarSeiteAus); werr != nil {
			schreibeSeitenwechselFehler(w, werr)
			return
		}
	}
	uid := adminUserIDFromContext(r.Context())
	if err := a.cfg.DeleteMitFolge(r.Context(), key, uid, a.konfigFolge[key]); err != nil {
		if konfigFolgeKonflikt(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "config delete failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// konfigFolgeKonflikt: Fehler einer Folge, die keine Serverstörung sind,
// sondern ein Zustand, den der Admin auflösen muss (F-2268, F-2272) → 409.
func konfigFolgeKonflikt(w http.ResponseWriter, err error) bool {
	if errors.Is(err, ErrBoardpaarTeamEntzugUnbestaetigt) || errors.Is(err, storage.ErrKonfigGeheimwert) {
		writeError(w, http.StatusConflict, err.Error())
		return true
	}
	return false
}

func (a *AdminAPI) handleSectionTest(w http.ResponseWriter, r *http.Request) {
	section := r.PathValue("section")
	if section == "" {
		writeError(w, http.StatusBadRequest, "section required")
		return
	}
	values, err := a.cfg.List(r.Context(), section)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "section read failed", err)
		return
	}
	// Plain-Werte sammeln (List liefert für Secrets keinen Klartext —
	// Test-Hook lädt sie ggf. selbst via Get()).
	cfgMap := map[string]string{}
	for _, e := range values {
		if !e.IsSecret {
			cfgMap[e.Key] = e.Value
		}
	}
	result, err := a.registry.RunTest(r.Context(), section, cfgMap)
	if err != nil {
		if errors.Is(err, ErrSectionTestNotRegistered) {
			writeError(w, http.StatusNotImplemented, "no test hook registered for section: "+section)
			return
		}
		// F-686d: bewusste Ausnahme von F-252 (Roh-Fehler nicht an den Client).
		// Diese Route ist admin-gated (requireAdmin, s. Dateikopf) — nur Admins
		// sehen den Text, und genau die brauchen ihn zur Diagnose eines
		// fehlgeschlagenen Subsystem-Tests (z. B. welcher SMTP-Schritt scheiterte).
		resp := map[string]any{
			"ok":    false,
			"error": err.Error(),
		}
		// Sprint-53-Kritiker: Ein Hook, der teilweise durchkam und dabei
		// Diagnosedaten sammelte, verlor sie hier lautlos. Ein SMTP-Test, der
		// "Host erreicht, TLS ok, AUTH abgelehnt" meldet, ist deutlich
		// nützlicher als nur der Fehlertext.
		if istBrauchbaresErgebnis(result) {
			resp["result"] = result
		}
		writeJSON(w, http.StatusUnprocessableEntity, resp)
		return
	}
	// Karte #333: Hook-Ergebnis unter "result" durchreichen. `ok:true` bleibt
	// unverändert an seinem Platz — bestehende Aufrufer, die nur darauf sehen,
	// brechen nicht (additive Erweiterung).
	resp := map[string]any{"ok": true}
	if istBrauchbaresErgebnis(result) {
		// Sprint-53-Kritiker: Ist das Ergebnis nicht serialisierbar (Funktion,
		// Channel, zyklischer Verweis, NaN), schreibt `json.Encoder.Encode`
		// GAR NICHTS — und weil `writeJSON` den Status vorher setzt, ginge ein
		// `200 OK` mit komplett leerem Body raus. Der Client sähe "Test ok"
		// ohne Ergebnis, der Fehler bliebe undiagnostiziert. Deshalb hier
		// vorab prüfen und im Zweifel ehrlich 500 melden.
		//
		// F-686d: bewusste Ausnahme von F-252, wie im Fehlerzweig oben —
		// admin-gated, der Marshal-Fehler ist die Diagnose dafür, welcher
		// Hook einen nicht serialisierbaren Wert zurückgegeben hat.
		if _, merr := json.Marshal(result); merr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": "hook result not serializable: " + merr.Error(),
			})
			return
		}
		resp["result"] = result
	}
	writeJSON(w, http.StatusOK, resp)
}

// istBrauchbaresErgebnis unterscheidet "kein Ergebnis" von "leerem Ergebnis".
//
// Sprint-53-Kritiker: `result != nil` prüft nur das Interface. Ein Hook mit
// `var m map[string]any; return m, nil` liefert ein NICHT-nil Interface mit
// nil-Wert dahinter — die Antwort enthielt dann `"result": null`, obwohl der
// Test `OmitsResultWhenHookReturnsNil` genau das ausschliessen sollte. Ein
// Aufrufer, der `"result" in body` als "es gibt ein Ergebnis" liest, greift
// dann auf null zu.
func istBrauchbaresErgebnis(result any) bool {
	if result == nil {
		return false
	}
	v := reflect.ValueOf(result)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface, reflect.Chan, reflect.Func:
		return !v.IsNil()
	default:
		return true
	}
}

func (a *AdminAPI) handleReload(w http.ResponseWriter, r *http.Request) {
	uid := adminUserIDFromContext(r.Context())
	// ?section=<name>: nur diesen Hook (O-3); unbekannt → 400; ohne Parameter wie bisher alle.
	section := r.URL.Query().Get("section")
	results, bekannt := a.registry.runReloadAuswahl(r.Context(), section)
	if !bekannt {
		writeError(w, http.StatusBadRequest, "unknown reload section", nil)
		return
	}
	// Audit-Eintrag — eine Reload-Aktion pro Aufruf, Liste der Sektionen
	// gehört als 'new'-Spalte; mehrteilige Fehler bekommen einen Sammel-
	// Eintrag.
	sections := make([]string, 0, len(results))
	for s := range results {
		sections = append(sections, s)
	}
	_ = a.cfg.AppendAudit(r.Context(), "reload", uid, storage.AuditActionReload, "", strings.Join(sections, ","))
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
	})
}

func (a *AdminAPI) handleAudit(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	limit := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			limit = v
		}
	}
	entries, err := a.cfg.Audit(r.Context(), since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit query failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (a *AdminAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	adminCount, _ := a.users.CountAdmins(r.Context())
	body := map[string]any{
		"version":         a.version,
		"uptime_sec":      int(time.Since(a.startedAt).Seconds()),
		"started_at":      a.startedAt.UTC().Format(time.RFC3339),
		"admin_count":     adminCount,
		"secrets_enabled": a.cfg.SecretsEnabled(),
	}
	// Karte #570: nur wenn gesetzt — Default (api.openai.com) ist kein
	// Betriebszustand, den ein Admin bestätigt sehen muss.
	if a.llmOpenAIBaseURL != "" {
		body["llm_openai_base_url"] = a.llmOpenAIBaseURL
	}
	// F-1546: Der Betreiber sieht beides sonst nur im Start-Log. Im Betrieb
	// ist llmPII nie leer (ParsePIIVorgabe liefert mindestens „nutzer“), der
	// Block läuft also immer; die Bedingung trifft nur Aufrufer ohne
	// WithLLMVorgaben (Tests, ältere Verdrahtung).
	if a.llmFirmenmodus || a.llmPII != "" {
		body["llm_firmenmodus"] = a.llmFirmenmodus
		if a.llmFirmenmodus {
			body["llm_modelle"] = a.llmModelle
			// Nur der Host, nie der Schlüssel (Karte #703) — wie in
			// firmenmodusAntwort; der Standard ist api.openai.com.
			ziel := "api.openai.com"
			if a.llmOpenAIBaseURL != "" {
				ziel = hostVon(a.llmOpenAIBaseURL)
			}
			body["llm_ziel"] = ziel
		}
		if a.llmPII != "" {
			body["llm_pii"] = a.llmPII
		}
	}
	body["uhr"] = uhrStatus()
	// Karte #684: Ausgangsschleuse — „aus“ oder „an“ samt Zahlen.
	if a.schleuse != nil {
		for k, v := range a.schleuse.statusFelder(r.Context()) {
			body[k] = v
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// uhrStatus zeigt, ob Frist-Löschungen laufen dürfen (Session-Kritiker
// Sprint 74, F-1204). Das Log meldet einen Wechsel nur einmal; ein Zustand
// „nicht gestellt“, der über Monate stehen bleibt, wäre sonst nur in einer
// längst rotierten Log-Zeile zu finden. pruefung: an|aus (ABOARD_UHR_PRUEFUNG),
// gestellt: ob gelöscht werden darf, fehler: nur wenn der Status nicht lesbar ist.
func uhrStatus() map[string]any {
	m := map[string]any{"pruefung": "an"}
	if os.Getenv(uhr.PruefungEnvVar) == "aus" {
		m["pruefung"] = "aus"
	}
	ok, err := uhr.Gestellt()
	m["gestellt"] = ok
	if err != nil {
		m["fehler"] = err.Error()
	}
	return m
}

func (a *AdminAPI) handleSetAdminFlag(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("id")
	if targetID == "" {
		writeError(w, http.StatusBadRequest, "user id required")
		return
	}
	var body struct {
		IsAdmin bool `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	uid := adminUserIDFromContext(r.Context())

	// Sprint-40-Kritiker-Fix [K]P1: Self-Lockout-Schutz NICHT mehr nur bei
	// target==uid + getrenntem Count+Update (race-anfällig), sondern ATOMAR
	// in einer BEGIN IMMEDIATE-Tx im Repo. SetAdminFlagSelfSafe prüft global:
	// jeder Admin-Revoke, der den LETZTEN Admin entzöge, wird abgelehnt —
	// egal ob Self oder Cross. Verhindert auch das Cross-Race „zwei letzte
	// Admins entziehen sich gleichzeitig gegenseitig".
	if err := a.users.SetAdminFlagSelfSafe(r.Context(), targetID, body.IsAdmin); err != nil {
		if errors.Is(err, storage.ErrLastAdminLockout) {
			writeError(w, http.StatusConflict, "cannot revoke last admin (self-lockout protection)")
			return
		}
		if errors.Is(err, storage.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "set admin flag failed", err)
		return
	}
	_ = uid // jetzt nur noch für Audit-Eintrag unten benutzt
	action := storage.AuditActionAdminGrant
	if !body.IsAdmin {
		action = storage.AuditActionAdminRevoke
	}
	uid2 := adminUserIDFromContext(r.Context())
	_ = a.cfg.AppendAudit(r.Context(), "users/"+targetID, uid2, action, "", strconv.FormatBool(body.IsAdmin))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUserList liefert alle Nutzer samt Board-Owner-Zahlen (Karte #575,
// Folge zu #571) — Grundlage für den Nutzer-Tab im Admin-Dialog (Tabelle +
// Löschen). Kein Rate-Limit (Read-Route, s. protect() oben).
func (a *AdminAPI) handleUserList(w http.ResponseWriter, r *http.Request) {
	list, err := a.users.ListMitBoardZahlen(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": list})
}

// handleBoardList liefert ALLE Boards (F-1439): `GET /boards` ist ACL-gefiltert
// und lässt Boards ohne ACL-Eintrag aus. Antwort {"boards":[{id,title,
// updated_at,hat_acl,owners[]}], jüngst geändert zuerst. Nur Admins.
//
// Seitenweise (F-1442): `limit` (Standard 500, höchstens 1000, grössere Werte
// werden gekappt) und `offset` (Standard 0). Die Antwort trägt zusätzlich
// `total` (alle Boards) und, solange es weitere gibt, `next_offset`.
// Ungültige Werte (keine Zahl, negativ, limit 0) → 400.
func (a *AdminAPI) handleBoardList(w http.ResponseWriter, r *http.Request) {
	limit, offset := storage.StandardBoardSeite, 0
	for _, p := range []struct {
		name string
		ziel *int
		min  int
	}{{"limit", &limit, 1}, {"offset", &offset, 0}} {
		if v := r.URL.Query().Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < p.min {
				writeError(w, http.StatusBadRequest, p.name+" must be an integer >= "+strconv.Itoa(p.min))
				return
			}
			*p.ziel = n
		}
	}
	if limit > storage.MaxBoardSeite {
		limit = storage.MaxBoardSeite
	}
	list, total, err := a.users.ListAlleBoards(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board list failed", err)
		return
	}
	antwort := map[string]any{"boards": list, "total": total}
	if next := offset + len(list); next < total {
		antwort["next_offset"] = next
	}
	writeJSON(w, http.StatusOK, antwort)
}

// handleUserDelete entfernt einen Nutzer (ADR-0129 §5 A-8, Karte #571).
//
// Body (optional): {"uebertragen": {"<boardId>": "<email>"}} — für jedes
// Board, dessen einziger Owner der Nutzer ist, der künftige Owner. Fehlt
// eines, antwortet der Endpunkt 409 mit der Liste dieser Boards und ändert
// nichts. Was gelöscht und was pseudonymisiert wird: storage/user_loeschung.go.
//
// Antworten: 200 {ok, pseudonym, uebertragen, gezaehlt} · 400 ungültiger Body
// oder ungültige Übertragung · 404 unbekannter Nutzer · 409 einziger Owner
// (boards), letzter Admin oder Selbstlöschung.
func (a *AdminAPI) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("id")
	if strings.TrimSpace(targetID) == "" {
		writeError(w, http.StatusBadRequest, "user id required")
		return
	}
	var body struct {
		Uebertragen map[string]string `json:"uebertragen"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	adminID := adminUserIDFromContext(r.Context())
	admin, err := a.users.GetByID(r.Context(), adminID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin lookup failed", err)
		return
	}
	erg, err := a.users.Loeschen(r.Context(), storage.LoeschAnweisung{
		UserID:      targetID,
		AdminID:     adminID,
		AdminEmail:  admin.Email,
		Uebertragen: body.Uebertragen,
	})
	var einzel *storage.EinzigerOwnerFehler
	switch {
	case errors.As(err, &einzel):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "user is sole owner of boards; transfer them first (body: uebertragen)",
			"boards": einzel.Boards,
		})
		return
	case errors.Is(err, storage.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user not found")
		return
	case errors.Is(err, storage.ErrLastAdminLockout):
		writeError(w, http.StatusConflict, "cannot delete last admin")
		return
	case errors.Is(err, storage.ErrSelbstloeschung):
		writeError(w, http.StatusConflict, "admins cannot delete themselves")
		return
	case errors.Is(err, storage.ErrUebertragungUngueltig):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "user delete failed", err)
		return
	}
	a.agentZugang.schliesseAlleIDs(erg.AgentTokens)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"pseudonym":   erg.Pseudonym,
		"uebertragen": erg.Uebertragen,
		"gezaehlt":    erg.Gezaehlt,
	})
}

// ---------- Subsystem-Registry ----------

// SectionFunc ist die Signatur für Test/Reload-Callbacks.
//   - ctx: Request-Context (für Cancel/Timeout).
//   - cfg: Plain-Werte der Sektion (Secrets lädt der Hook ggf. selbst per
//     Repo, weil List() Secrets nicht im Klartext liefert).
type SectionFunc func(ctx context.Context, cfg map[string]string) error

// SectionTestFunc ist die Signatur für Test-Callbacks (Karte #333). Wie
// SectionFunc, liefert aber zusätzlich ein optionales Ergebnis, das der
// Handler als JSON an den Aufrufer durchreicht.
//
// Bewusst ein EIGENER Typ statt einer Erweiterung von SectionFunc: Test und
// Reload haben unterschiedliche Verträge. Ein Reload-Hook hat kein Ergebnis
// zu melden — RunReload wertet nur den Fehler aus —, und eine gemeinsame
// Signatur hätte an jeder Reload-Registrierung ein bedeutungsloses
// `return nil, nil` erzwungen. Der Aufwand ist derselbe, die Aussage besser.
//
// result darf nil sein; dann antwortet der Handler wie bisher mit {ok:true}.
type SectionTestFunc func(ctx context.Context, cfg map[string]string) (any, error)

// ErrSectionTestNotRegistered signalisiert dem Handler, dass keine
// Test-Funktion für die Sektion bekannt ist → 501 statt 422.
var ErrSectionTestNotRegistered = errors.New("section test not registered")

// SubsystemRegistry sammelt Test- und Reload-Hooks pro Sektion. Im MVP
// (#306) leer — Sektionen kommen in Folge-Karten. Goroutine-sicher.
type SubsystemRegistry struct {
	mu      sync.RWMutex
	tests   map[string]SectionTestFunc
	reloads map[string]SectionFunc
}

func NewSubsystemRegistry() *SubsystemRegistry {
	return &SubsystemRegistry{
		tests:   map[string]SectionTestFunc{},
		reloads: map[string]SectionFunc{},
	}
}

func (r *SubsystemRegistry) RegisterTest(section string, fn SectionTestFunc) {
	r.mu.Lock()
	r.tests[section] = fn
	r.mu.Unlock()
}

func (r *SubsystemRegistry) RegisterReload(section string, fn SectionFunc) {
	r.mu.Lock()
	r.reloads[section] = fn
	r.mu.Unlock()
}

// RunTest führt den Test-Hook der Sektion aus. Das erste Rückgabefeld ist das
// optionale Hook-Ergebnis (nil, wenn der Hook keines liefert oder fehlschlägt).
func (r *SubsystemRegistry) RunTest(ctx context.Context, section string, cfg map[string]string) (any, error) {
	r.mu.RLock()
	fn, ok := r.tests[section]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSectionTestNotRegistered, section)
	}
	return fn(ctx, cfg)
}

// RunReload feuert alle registrierten Reload-Hooks parallel-frei (sequentiell
// — Subsysteme dürfen einen kurzen Connection-Drain auslösen, kein Aufruf
// wartet auf den anderen). Rückgabe: pro Sektion "ok" oder Fehlertext.
func (r *SubsystemRegistry) RunReload(ctx context.Context) map[string]string {
	out, _ := r.runReloadAuswahl(ctx, "")
	return out
}

// ReloadStatus ist ein Nicht-Fehler-Ergebnis eines Reload-Hooks: Der Hook gibt
// ihn als error zurück, RunReload meldet dann den Text statt „error: …“
// (z. B. "disabled": die Sektion ist ausgeschaltet, es gibt nichts neu zu
// starten). Der Admin-Dialog unterscheidet so „ok“, „aus“ und Fehler (O-5).
type ReloadStatus string

func (s ReloadStatus) Error() string { return string(s) }

// runReloadAuswahl: section == "" → alle Hooks; sonst nur dieser eine.
func (r *SubsystemRegistry) runReloadAuswahl(ctx context.Context, section string) (map[string]string, bool) {
	r.mu.RLock()
	hooks := make(map[string]SectionFunc, len(r.reloads))
	for s, fn := range r.reloads {
		if section == "" || s == section {
			hooks[s] = fn
		}
	}
	r.mu.RUnlock()
	if section != "" && len(hooks) == 0 {
		return nil, false
	}
	out := make(map[string]string, len(hooks))
	for s, fn := range hooks {
		if err := fn(ctx, nil); err != nil {
			var rs ReloadStatus
			if errors.As(err, &rs) {
				out[s] = string(rs)
				continue
			}
			// F-686d: bewusste Ausnahme von F-252 — nur über die admin-gated
			// Route (handleReload, Z. 276) erreichbar; Admins brauchen den
			// Klartext, um zu sehen, welche Sektion beim Reload scheiterte.
			out[s] = "error: " + err.Error()
		} else {
			out[s] = "ok"
		}
	}
	return out, true
}

// ---------- Rate-Limiter (token bucket, per user) ----------

type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	// buckets: einziger Zugriffspfad ist allowOderWartezeit (llm_obergrenze.go),
	// dort läuft auch das Räumen mit (F-1578). Neuer Zugriff von anderer Stelle
	// räumt NICHT mit — stattdessen über allowOderWartezeit gehen.
	// ratelimiter_waechter_test.go schlägt an, sobald ein zweiter Pfad entsteht.
	buckets map[string]*bucket
	now     func() time.Time
	// zugriffe zählt die Prüfungen für das gelegentliche Räumen (F-1570).
	zugriffe int
}

// rateLimiterRaeumAlle: Nach so vielen Prüfungen werden abgelaufene Eimer
// gelöscht. Ohne eigene Goroutine: Der Aufwand hängt an den Anfragen, ein
// ruhender Limiter räumt nichts und braucht es auch nicht.
const rateLimiterRaeumAlle = 256

type bucket struct {
	count int
	since time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		max:     max,
		window:  window,
		buckets: map[string]*bucket{},
		now:     time.Now,
	}
}

func (l *rateLimiter) allow(userID string) bool {
	ok, _ := l.allowOderWartezeit(userID)
	return ok
}

// ---------- Context-Helper ----------

type adminUserIDCtxKey struct{}

func withAdminUserID(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, adminUserIDCtxKey{}, uid)
}

func adminUserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(adminUserIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}
