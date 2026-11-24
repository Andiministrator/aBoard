package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

// requestTimeout begrenzt die Bearbeitungszeit eines einzelnen Handlers.
// http.Server-Timeouts (F-197) schützen vor Slow-Body — dieser Timeout
// schützt vor langsamen DB-Pfaden (z. B. WAL-Checkpoint unter Last).
const requestTimeout = 5 * time.Second

// BoardsAPI bündelt die CRUD-Handler für die boards-Ressource.
// state_json bleibt opak (ADR-0044): der Server validiert nur die Top-Level-
// Hülle {version, board.id, board.title} und gibt den Rest unverändert
// weiter — Sprints/Cards/Comments leben im JSON-Blob.
//
// broker ist optional. Wenn gesetzt, broadcastet jede erfolgreiche Mutation
// (POST/PUT/DELETE) ein board:updated-Event an alle SSE-Subscriber. Bleibt
// der Broker nil, ist die API funktional unverändert (Test-Setups ohne SSE).
//
// activities ist optional (Karte #200, ADR-0037). Wenn gesetzt, werden alle
// Mutations-Operationen im server-seitigen Activity-Log protokolliert. Bleibt
// activities nil, ist die API funktional unverändert (Test-Setups ohne Logging).
type BoardsAPI struct {
	repo       *storage.BoardRepo
	broker     *EventBroker
	acl        *storage.AclRepo
	activities *storage.ActivityRepo
	attach     *storage.AttachmentStore
	// nachDelete läuft nach einem erfolgreichen DB-Delete (K-A1): main.go
	// legt darüber den Grabstein ins Backup-Verzeichnis des Boards.
	nachDelete func(ctx context.Context, boardID string)
	// fold und opsLimiter bedienen POST /boards/{id}/ops (Karte #552,
	// boards_ops.go). Ohne fold antwortet der Endpunkt mit 503.
	fold            *fold.Motor
	opsLimiter      *rateLimiter // Menschen (Browser) und Legacy-Bypass
	opsLimiterAgent *rateLimiter // Agenten (ADR-0129 §3)
	opsSperre       *sperrBudget // Sperrzeit Menschen/Legacy (F-1149)
	opsSperreAgent  *sperrBudget // Sperrzeit Agenten (F-1149)
	// nachApplyOps ist eine Test-Naht (F-1209): läuft in postOps direkt nach
	// der Sperrzeit-Buchung und vor dem Frist-Trim. Im Produktivbetrieb nil
	// und wirkungslos — kein Aufrufer setzt sie ausserhalb von Tests. Ein
	// Test kann hier reale Verzögerung einbauen und prüfen, dass sie NICHT
	// in die gebuchte Sperrzeit einfliesst (die Buchung muss vorher passiert
	// sein, nicht erst im defer am Funktionsende).
	nachApplyOps func()
	// oeffentlicheBasis: Basis-URL der Links „im Board öffnen“ in den
	// Werkzeug-Antworten (Karte #671, agent_werkzeuge.go). Leer = keine Links.
	oeffentlicheBasis string
	// kalenderCfg: Instanz-Zeitzone für „heute“ in termine_lesen
	// (ADR-0138 §2, Kritiker 07.10., K-5). nil → Standardzone.
	kalenderCfg *storage.ConfigRepo
	// paarCfg/paar/schatten: Board-Paar (Karte #789, ADR-0140 §7, §10 W-4).
	// paarCfg liefert die Seite der Instanz (je Anfrage gelesen), paar die
	// Kopplungen, schatten die Schatten für die Agentensicht außen
	// (agent_werkzeuge_schatten.go). Ohne MitBoardpaar gilt die Seite „aus“.
	paarCfg  *storage.ConfigRepo
	paar     *storage.BoardpaarRepo
	schatten *storage.SchattenRepo
	// werkzeugInit/werkzeugLeser/werkzeugIdem: Ratenbegrenzung der lesenden
	// Werkzeuge und Wiederholungsschutz der anlegenden (Kritiker Sprint 82,
	// K1-4/K1-5, agent_werkzeuge_idem.go). Bei erster Benutzung angelegt.
	werkzeugInit  sync.Once
	werkzeugLeser *rateLimiter
	werkzeugIdem  *wkIdempotenz
	// loeschCodes: Geheimnis und Verbrauchsliste der Bestätigungscodes
	// (Karte #757, agent_loeschen.go). Mit werkzeugInit angelegt.
	loeschCodes *storage.LoeschCodeRepo
	// mcpZustand: SDK-Handler und interne Werkzeug-Weiche des MCP-Eingangs
	// (Karte #753, agent_mcp.go). Bei erster Benutzung angelegt.
	mcpZustand mcpEingang
	// users: Server-Admin darf fremde Kommentare löschen (ADR-0136).
	users *storage.UserRepo
	// nachKommentarGeloescht: Benachrichtigung des Autors, wenn jemand
	// seinen Kommentar gelöscht hat (ADR-0136 §3). Nil = nur Verlauf.
	nachKommentarGeloescht func(context.Context, KommentarGeloescht)
	// internBremse: sofort wirksame Änderungen der Rolle intern, 30 je Stunde
	// und Board (Karte #783, vorschlaege.go). Bei erster Benutzung angelegt.
	internBremseInit sync.Once
	internBremse     *internBremse
	// anlageBremse: Agenten-Anlagen innen mit `vertraulich: false`, 20 je
	// Stunde, Token und Board (Karte #798, anlage_bremse_798.go).
	anlageBremseInit sync.Once
	anlageBremse     *internBremse
}

// MitKalenderKonfig hängt den Konfig-Store für die Instanz-Zeitzone an
// (termine_lesen bildet „heute“ in dieser Zone, ADR-0138 §2).
func (a *BoardsAPI) MitKalenderKonfig(cfg *storage.ConfigRepo) *BoardsAPI {
	a.kalenderCfg = cfg
	return a
}

func NewBoardsAPI(repo *storage.BoardRepo) *BoardsAPI {
	return &BoardsAPI{repo: repo}
}

// WithBroker hängt einen EventBroker an die API. Mutations-Pfade publishen
// danach board:updated-Events. Idempotent: erneutes Setzen ersetzt den
// vorherigen Broker.
func (a *BoardsAPI) WithBroker(broker *EventBroker) *BoardsAPI {
	a.broker = broker
	return a
}

// WithAttachments hängt den AttachmentStore an die API (F-200, Karte #376).
// Wirkt nur im Delete-Pfad: dort wird nach dem DB-Delete auch das
// Anhang-Verzeichnis des Boards entfernt. Bleibt attach nil (Test-Setups ohne
// Attachment-Volume), ist die API funktional unverändert. Idempotent.
func (a *BoardsAPI) WithAttachments(store *storage.AttachmentStore) *BoardsAPI {
	a.attach = store
	return a
}

// WithNachDelete hängt einen Schritt an, der nach jedem erfolgreichen
// DELETE /boards/{id} läuft (Kritiker Sprint 70, K-A1). main.go markiert
// darüber das Backup-Verzeichnis des Boards mit einem Grabstein; nur so
// markierte Verzeichnisse räumt der Backup-Scheduler nach Ablauf der Frist
// ab. Nicht blockierend: Der Schritt meldet Fehler selbst per Log. Nil =
// nichts tun.
func (a *BoardsAPI) WithNachDelete(fn func(ctx context.Context, boardID string)) *BoardsAPI {
	a.nachDelete = fn
	return a
}

// WithACL hängt das AclRepo an die API (Karte #198, ADR-0059). Wirkt nur bei
// vorhandener Forward-Auth-Identität: dann filtert list auf die Boards des
// Users und create vergibt dem Ersteller die owner-Rolle. Bleibt acl nil
// (Test-Setups ohne Multi-User), ist die API funktional unverändert
// (Legacy-Verhalten). Idempotent.
func (a *BoardsAPI) WithACL(acl *storage.AclRepo) *BoardsAPI {
	a.acl = acl
	return a
}

// WithActivities hängt das ActivityRepo an die API (Karte #200, ADR-0037).
// Wenn gesetzt, protokolliert jede Mutation (Board-create/update/delete, ACL-
// set/delete) einen Eintrag im Activity-Log mit Akteur (Forward-Auth-Email)
// und Aktion. Bleibt activities nil (Test-Setups ohne Logging), ist die API
// funktional unverändert. Idempotent.
func (a *BoardsAPI) WithActivities(activities *storage.ActivityRepo) *BoardsAPI {
	a.activities = activities
	return a
}

// actorFromContext liest die Email aus dem Forward-Auth-Kontext.
// Gibt "" (leeren String) zurück wenn keine Identität gesetzt ist (Legacy-Bypass, ADR-0059 §2).
// Kein Crash bei fehlendem Kontext — Logging läuft in beiden Modi.
//
// Agenten (Karte #553) stempeln als `agent:<name>` — geprüft VOR der
// Forward-Auth-Identität, obwohl auf dem Agenten-Listener keine entsteht:
// Käme doch einmal beides zusammen, soll der Agent nie unter einer
// Menschen-Adresse schreiben.
func actorFromContext(ctx context.Context) string {
	if person := boardpaarAkteurAus(ctx); person != "" {
		// ADR-0141 §5.3, „Umsetzung 2c“ Punkt 23: Rückweg des Board-Paars.
		return boardpaarAkteurPraefix + person
	}
	if p := AgentFromContext(ctx); p != nil {
		return p.Akteur()
	}
	if id := IdentityFromContext(ctx); id != nil {
		return storage.NormalizeEmail(id.Email) // F-260: konsistente Akteur-Email
	}
	return ""
}

// recordActivity schreibt einen Activity-Eintrag wenn activities gesetzt ist.
// Schlägt der Log-Schreibzugriff fehl (nicht-kritisch), wird nur geloggt —
// die primäre Mutation gilt als erfolgreich. Nie blockierend.
func (a *BoardsAPI) recordActivity(ctx context.Context, boardID, actor, action, field, fromVal, toVal, note string) {
	if a.activities == nil {
		return
	}
	if err := a.activities.Record(ctx, boardID, actor, action, field, fromVal, toVal, note); err != nil {
		log.Printf("aboard-server: activity-log-fehler board=%s action=%s: %v", boardID, action, err)
	}
}

// Register hängt die Boards-Routen an einen ServeMux. Method+Pattern-Routing
// braucht Go 1.22+ (vorhanden — go.mod: go 1.25.0).
func (a *BoardsAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /boards", WithDefaults(http.HandlerFunc(a.list)))
	mux.Handle("POST /boards", WithDefaults(http.HandlerFunc(a.create)))
	mux.Handle("GET /boards/{id}", WithDefaults(http.HandlerFunc(a.get)))
	mux.Handle("PUT /boards/{id}", WithDefaults(http.HandlerFunc(a.update)))
	mux.Handle("DELETE /boards/{id}", WithDefaults(http.HandlerFunc(a.deleteHandler)))
	// Batch-Schreibweg für Ops (ADR-0129 §4, Karte #552)
	mux.Handle("POST /boards/{id}/ops", WithDefaults(http.HandlerFunc(a.postOps)))
	// Lese-Endpunkt fürs Protokoll je Karte (ADR-0129 §5, Karte #554)
	mux.Handle("GET /boards/{id}/activities", WithDefaults(http.HandlerFunc(a.getActivities)))
	// Wer bin ich — nur für Agenten (Karte #602, ADR-0130 §6/§8): Name,
	// Rolle und Person des Tokens. Das Werkzeug `tools/aboard-remote.mjs`
	// holt daraus die gemeinsame Personenkennung (E-Mail), ohne dass sie im
	// Projekt oder beim Token stehen muss.
	mux.Handle("GET /boards/{id}/ich", WithDefaults(http.HandlerFunc(agentIch)))
	// ACL-Endpoints (Karte #199, ADR-0059)
	mux.Handle("GET /boards/{id}/acl", WithDefaults(http.HandlerFunc(a.aclList)))
	mux.Handle("POST /boards/{id}/acl", WithDefaults(http.HandlerFunc(a.aclSet)))
	mux.Handle("DELETE /boards/{id}/acl", WithDefaults(http.HandlerFunc(a.aclDelete)))
	// Namen der Personen des Boards, ohne Adressen (Karte #699, F-261 gewahrt)
	mux.Handle("GET /boards/{id}/mitglieder", WithDefaults(http.HandlerFunc(a.mitglieder)))
}

// boardEnvelope spiegelt die Top-Level-Struktur von aboard-sync.json — der
// Server liest nur die Felder, die er kennen muss; der Rest des Bodys wird
// als raw state_json persistiert.
type boardEnvelope struct {
	Version string    `json:"version"`
	Board   boardCore `json:"board"`
}

type boardCore struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// listEntry ist die JSON-Form eines Listen-Eintrags. state_json wird
// bewusst nicht mitgeschickt — bei vielen Boards würde das die Antwort
// aufblähen. Frontend nutzt /boards/{id} für den vollen State.
// role ist optional: nur im Forward-Auth-Modus (ADR-0059) befüllt —
// zeigt dem Frontend ob der User owner/editor/viewer ist (Karte #201).
type listEntry struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	UpdatedAt int64   `json:"updated_at"`
	Role      *string `json:"role,omitempty"` // Karte #201: nur bei Forward-Auth gesetzt
	// AwaitingUser: board.awaitingUser des Boards (Abbildung Person → Eintrag),
	// nur wenn gesetzt (ADR-0156, Karte #1197). Welcher Eintrag der eigene ist,
	// entscheidet der Browser wie beim Banner. Keine neue Preisgabe: dasselbe
	// Feld steht im vollen Stand, den die Person ohnehin lesen darf.
	AwaitingUser json.RawMessage `json:"awaiting_user,omitempty"`
}

func (a *BoardsAPI) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	items, err := a.repo.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}

	// ACL-Filter (#198, ADR-0059 §1): bei vorhandener Forward-Auth-Identität
	// nur Boards, auf deren ACL die Email steht. Ohne Identität/acl =
	// Legacy-Verhalten (alle Boards), exakt wie vor Phase 2.
	// Karte #201: im Forward-Auth-Modus zusätzlich die Rolle pro Board liefern.
	if id := IdentityFromContext(r.Context()); id != nil && a.acl != nil {
		email := storage.NormalizeEmail(id.Email) // F-260
		if email == "" {
			writeJSON(w, http.StatusOK, []listEntry{})
			return
		}
		withRoles, aerr := a.acl.ListBoardsWithRoleForUser(ctx, email)
		if aerr != nil {
			writeError(w, http.StatusInternalServerError, "list failed", aerr)
			return
		}
		permitted := make(map[string]string, len(withRoles)) // board_id → role
		for _, e := range withRoles {
			permitted[e.BoardID] = e.Role
		}
		out := make([]listEntry, 0, len(items))
		for _, s := range items {
			if role, ok := permitted[s.ID]; ok {
				r := role // lokale Kopie für Pointer
				out = append(out, listEntry{ID: s.ID, Title: s.Title, UpdatedAt: s.UpdatedAt, Role: &r, AwaitingUser: s.Awaiting})
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	out := make([]listEntry, 0, len(items))
	for _, s := range items {
		out = append(out, listEntry{ID: s.ID, Title: s.Title, UpdatedAt: s.UpdatedAt, AwaitingUser: s.Awaiting})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *BoardsAPI) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	b, err := a.repo.Get(ctx, id)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}
	et := etag(b.UpdatedAt)
	w.Header().Set("ETag", et)
	// F-254/ADR-0055: Conditional-GET. Der Client-Poll sendet
	// If-None-Match: <etag> und erwartet 304, wenn sich nichts geändert
	// hat. Ohne das lieferte JEDER ~10-s-Poll 200 → Dauer-„Board
	// aktualisiert"-Toast + Echo-PUT-Schleife (Save-Race). Exakter
	// Vergleich genügt: der Client schickt den ETag unverändert zurück.
	if match := r.Header.Get("If-None-Match"); match != "" && match == et {
		w.WriteHeader(http.StatusNotModified) // kein Body (RFC 7232), ETag-Header bleibt
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.StateJSON)
}

func (a *BoardsAPI) create(w http.ResponseWriter, r *http.Request) {
	raw, env, err := readEnvelope(r)
	if err != nil {
		writeError(w, statusForBodyError(err), err.Error())
		return
	}
	if env.Version == "" || env.Board.ID == "" || env.Board.Title == "" {
		writeError(w, http.StatusBadRequest, "version, board.id, board.title required")
		return
	}
	// F-1572: Finding-Karten eines neuen Boards müssen den Regeln der Ops
	// entsprechen (keine doppelte nr, Wortschatz, finding-Objekt).
	if v := storage.NeueFindingVerstoesse(nil, raw); len(v) > 0 {
		writeError(w, http.StatusUnprocessableEntity, storage.FindingVerstossMeldung(v))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	b, err := a.repo.Insert(ctx, env.Board.ID, env.Board.Title, env.Version, raw)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "board id already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "create failed", err)
		return
	}

	// Owner-Grant (#198, ADR-0059 §1): bei Forward-Auth-Identität wird der
	// Ersteller owner des neuen Boards — sonst könnte er sein eigenes Board
	// danach nicht mehr lesen (ACL-Filter). Schlägt der Grant fehl, wird das
	// gerade angelegte Board kompensierend gelöscht, statt ein für niemanden
	// erreichbares Waisen-Board zu hinterlassen.
	//
	// F-264-SICHERHEITS-INVARIANTE (#244): Der kompensierende Delete trifft
	// IMMER nur das soeben FRISCH angelegte Board, nie ein fremdes. Garant:
	// a.repo.Insert (storage.BoardRepo.Insert) ist ein schlichtes
	// `INSERT INTO` — eine id-Kollision liefert eine UNIQUE-Verletzung, die
	// oben via isUniqueViolation als 409 abgefangen wird, BEVOR dieser Block
	// je erreicht wird. Reaching here ⇒ b.ID war eine neue Row. Wird Insert
	// je zu `INSERT OR REPLACE`/Upsert geändert, bricht
	// TestBoardsCreate_DuplicateID_DifferentUser_KeinFremdLoesch — diese
	// Kopplung NICHT lautlos auflösen (sonst lebt F-264 wieder auf).
	if id := IdentityFromContext(r.Context()); id != nil && a.acl != nil {
		if email := storage.NormalizeEmail(id.Email); email != "" { // F-260
			if aerr := a.acl.Set(ctx, b.ID, email, storage.RoleOwner); aerr != nil {
				if derr := a.repo.Delete(ctx, b.ID); derr != nil {
					log.Printf("aboard-server: owner-grant fehlgeschlagen UND Rollback-Delete fehlgeschlagen board=%s: grant=%v delete=%v", b.ID, aerr, derr)
				}
				writeError(w, http.StatusInternalServerError, "create failed", aerr)
				return
			}
		}
	}

	// Activity-Log: Board wurde angelegt (Karte #200, ADR-0037).
	a.recordActivity(r.Context(), b.ID, actorFromContext(r.Context()),
		"board-created", "", "", "", env.Board.Title)

	publishBoardUpdated(a.broker, b.ID, b.UpdatedAt, "create")
	w.Header().Set("ETag", etag(b.UpdatedAt))
	w.Header().Set("Location", "/boards/"+b.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(b.StateJSON)
}

func (a *BoardsAPI) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	raw, env, err := readEnvelope(r)
	if err != nil {
		writeError(w, statusForBodyError(err), err.Error())
		return
	}
	eingang := raw // SR-1: zum Vergleich, ob der Server den Stand verändert hat
	if env.Version == "" || env.Board.ID == "" || env.Board.Title == "" {
		writeError(w, http.StatusBadRequest, "version, board.id, board.title required")
		return
	}
	if env.Board.ID != id {
		writeError(w, http.StatusBadRequest, "url id and body board.id mismatch")
		return
	}

	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if ifMatch == 0 {
		// Phase 1: PUT verlangt explizites If-Match. Schützt vor
		// blinden Überschreibungen aus parallelen Tabs (Multi-Device).
		writeError(w, http.StatusPreconditionRequired, "If-Match header required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	// F-1572: Finding-Karten prüfen wie bei den Ops. Zählt nur, was gegenüber
	// dem gespeicherten Stand NEU verstösst — ein Board, das schon vorher
	// einen Verstoss trug, soll sich weiter speichern lassen. Ohne gespeicherten
	// Stand (404) entscheidet Update.
	var gespeichert []byte
	if cur, gerr := a.repo.Get(ctx, id); gerr == nil {
		gespeichert = cur.StateJSON
	}
	if v := storage.NeueFindingVerstoesse(gespeichert, raw); len(v) > 0 {
		writeError(w, http.StatusUnprocessableEntity, storage.FindingVerstossMeldung(v))
		return
	}
	// ADR-0136 Nachtrag (F-1612): derselbe Vergleich wie bei den Ops — fehlt
	// danach ein fremder Kommentar oder ist seine Urheberschaft geändert,
	// lehnt der PUT für alle außer Owner/Admin ab. Das trifft gewollt auch
	// „Meine Version behalten“ mit einem veralteten Stand, in dem ein
	// inzwischen von jemand anderem geschriebener Kommentar fehlt (KA-1): Er
	// verschwände sonst still. Geprüft wird gegen den Stand, den If-Match
	// festhält — weicht er ab, scheitert Update ohnehin mit 409.
	// Entfernt Owner/Admin per PUT einen fremden Kommentar, gibt es keinen
	// Verlaufseintrag und keine Benachrichtigung (der PUT schreibt das
	// Board-JSON, wie es kommt); die Vorlage dafür ist der Ops-Weg.
	if gespeichert != nil {
		schreiber := a.kommentarSchreiberFuer(r.Context(), id)
		bestand, okBestand := leseKommentarBestand(gespeichert)
		raw = stempleBoardKommentare(bestand, okBestand, raw, schreiber.email)
		if v, _ := vergleicheMitBestand(bestand, okBestand, raw, schreiber, id, nil); len(v) > 0 {
			writeError(w, http.StatusUnprocessableEntity, kommentarVerstossMeldung(v))
			return
		}
	}
	b, err := a.repo.Update(ctx, id, env.Board.Title, env.Version, raw, ifMatch)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if errors.Is(err, storage.ErrSpiegelGeschuetzt) {
		// ADR-0141 §7 (Karte #826): Spiegel einer externen Kopie sind bis 2c
		// nur lesbar. 403 — der Browser zeigt den Grund und versucht es nicht
		// erneut mit demselben Stand.
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if errors.Is(err, storage.ErrConflict) {
		// Konflikt: aktuellen Server-State + ETag zurück, damit das
		// Frontend einen Diff-Dialog zeigen kann (Karte #77).
		current, cerr := a.repo.Get(ctx, id)
		if cerr != nil {
			writeError(w, http.StatusConflict, "conflict")
			return
		}
		w.Header().Set("ETag", etag(current.UpdatedAt))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(current.StateJSON)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed", err)
		return
	}
	// ADR-0135 §3: Der PUT löscht keine Kommentar-Dateien (KA-1). Er kommt auch
	// von veralteten Ständen („Meine Version behalten“); eine gelöschte Datei
	// ist endgültig, ein verlorener Eintrag nicht.
	// Activity-Log: Board wurde aktualisiert (Karte #200, ADR-0037).
	// Titelwechsel wird explizit als field-update geloggt; der State-JSON-Blob
	// bleibt opak (ADR-0044) — keine Karten/Sprint-Diff auf Server-Seite.
	actor := actorFromContext(r.Context())
	if env.Board.Title != "" {
		a.recordActivity(r.Context(), b.ID, actor, "board-updated", "title", "", env.Board.Title, "")
	} else {
		a.recordActivity(r.Context(), b.ID, actor, "board-updated", "", "", "", "")
	}

	publishBoardUpdated(a.broker, b.ID, b.UpdatedAt, "update")
	// SR-1 (Sitzungs-Kritiker): Hat der Server den Stand verändert (Kommentare
	// gestempelt, Finding umnummeriert, Zähler bewahrt), sagt er es — der
	// Browser übernimmt seinen eigenen Stand dann nicht als Server-Stand und
	// holt den echten mit dem nächsten Poll.
	if !gleicherInhalt(eingang, b.StateJSON) {
		w.Header().Set("X-Aboard-Stand-Geaendert", "1")
	}
	// ADR-0140 §10 (Karte #778): „vertraulich“ wollte der Schreiber
	// abschalten, Update hat es stehen lassen — still wäre das ein Rätsel,
	// deshalb ein Hinweis. Der Body bleibt das Board.
	if h := vertraulichBewahrtHeader(b.VertraulichBewahrt); h != "" {
		w.Header().Set(VertraulichBewahrtHeader, h)
	}
	w.Header().Set("ETag", etag(b.UpdatedAt))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.StateJSON)
}

func (a *BoardsAPI) deleteHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	err := a.repo.Delete(ctx, id)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	var offen *storage.OffeneAenderungenError
	if errors.As(err, &offen) {
		// Kritiker Sprint 97 K-1: eine Kopie mit Änderungen, die innen noch
		// nicht angekommen sind — nicht still verwerfen, sondern auf den
		// Admin-Notweg verweisen, der die Zahl vorher nennt (F-2240).
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":              "board has changes not yet carried to the internal board; delete it via admin → board pair → delete copy",
			"offene_aenderungen": offen.Anzahl,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed", err)
		return
	}
	// KEIN Activity-Eintrag für „board-deleted" (F-259, Karte #200/ADR-0037).
	//
	// Bis hierher stand an dieser Stelle ein `recordActivity(..., "board-deleted", ...)`.
	// Der Aufruf konnte nie gelingen: `activities` ist per-Board mit
	// ON DELETE CASCADE auf `board_id`, `foreign_keys` ist per DSN-Pragma
	// aktiv (storage/sqlite.go), und `repo.Delete` hat oben bereits
	// committet. Der Insert lief also bei JEDEM Löschvorgang in einen
	// FK-Fehler, den `recordActivity` zu einer `log.Printf`-Zeile machte —
	// eine garantierte Fehlermeldung für einen erfolgreichen Vorgang.
	//
	// Die Reihenfolge zu drehen (erst loggen, dann löschen) wäre keine
	// Verbesserung: Der Eintrag würde vom CASCADE eine Zeile später
	// mitgelöscht. Ein Lösch-Audit ist nur in einer board-UNABHÄNGIGEN
	// Tabelle sinnvoll (kein CASCADE) — die gibt es bewusst nicht, und
	// Karte #200 liefert ausdrücklich das board-scoped Pendant zum
	// Client-`activity[]` aus #58. Wer je ein übergreifendes Audit braucht,
	// baut den separaten Store; bis dahin ist das Weglassen die ehrliche
	// Variante.

	a.NachBoardGeloescht(r.Context(), id)
	w.WriteHeader(http.StatusNoContent)
}

// NachBoardGeloescht führt die Schritte NACH einem committeten Board-Löschen
// aus: Anhang-Verzeichnis, Nach-Delete-Schritt (Backup-Grabstein) und das
// Lösch-Ereignis an die Abonnenten. Geteilt von DELETE /boards/{id} und dem
// Admin-Notweg „Kopie außen löschen“ (ADR-0141 Nachtrag #831), damit beide
// dasselbe hinterlassen. Nicht blockierend: Fehler gehen ins Log.
func (a *BoardsAPI) NachBoardGeloescht(ctx context.Context, id string) {
	// F-200 (Karte #376): Anhang-Verzeichnis des Boards mit entfernen. Die
	// Dateien liegen ausschliesslich im Dateisystem — es gibt keine
	// `attachments`-Tabelle, das ON DELETE CASCADE der übrigen Tabellen läuft
	// für sie also ins Leere, und verwaiste Nutzerdaten blieben unbegrenzt
	// liegen.
	//
	// NACH dem DB-Delete und bewusst nicht blockierend: der Delete ist
	// committed, ein Dateisystem-Fehler darf daraus keinen 500 machen (der
	// Client würde sonst einen erfolgreichen Löschvorgang als gescheitert
	// lesen und ggf. wiederholen). Fehlschlag wird geloggt — anders als der
	// entfallene Activity-Insert (F-259) ist diese Log-Zeile aber die
	// Ausnahme und nicht der Normalfall.
	if a.attach != nil {
		if err := a.attach.DeleteBoard(id); err != nil {
			log.Printf("boards.delete: Anhang-Verzeichnis für Board %s nicht entfernt: %v", id, err)
		}
	}
	if a.nachDelete != nil {
		a.nachDelete(ctx, id)
	}

	// Beim Delete kennen wir kein neues UpdatedAt — der Subscriber bekommt
	// nur das Signal „weg", muss selbst per GET den 404 prüfen.
	publishBoardUpdated(a.broker, id, nowMillisFunc(), "delete")
}

// nowMillisFunc liefert die aktuelle Zeit als Unix-ms — als Variable, damit
// Tests sie monoton hochzählen können (analog storage.nowMillis). Wird nur
// für das Delete-Event gebraucht (kein UpdatedAt vom Repo).
var nowMillisFunc = func() int64 {
	return time.Now().UnixMilli()
}

// statusForBodyError trennt 413 (Payload Too Large) von 400. readEnvelope
// markiert Body-Limit-Verletzungen mit dem Prefix "request body exceeds".
func statusForBodyError(err error) int {
	if err != nil && strings.HasPrefix(err.Error(), "request body exceeds ") {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// aclEntry ist die JSON-Ausgabestruktur eines ACL-Eintrags im Share-Dialog.
type aclEntry struct {
	Email   string `json:"email"`
	Role    string `json:"role"`
	Created string `json:"created"`
}

// aclSetRequest ist der Request-Body für POST /boards/{id}/acl.
type aclSetRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// aclList — GET /boards/{id}/acl
// Liefert alle ACL-Einträge des Boards als JSON-Array.
//
// F-261: NUR der owner des Boards darf die Mitgliederliste lesen (analog
// aclSet/aclDelete). Vorher fehlte JEDER Rollen-Check → jeder viewer/editor
// konnte die komplette user_email-/Rollen-Liste abziehen (PII-/Topologie-
// Leak). Der ACL-Guard (acl.go) lässt für GET nur viewer+ durch — er ist
// hier KEIN ausreichender Schutz. Legacy-Bypass (keine Identität) bleibt
// erhalten: ohne Forward-Auth kein owner-Konzept (ADR-0059 §2).
func (a *BoardsAPI) aclList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "fehlende Board-ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if _, err := a.repo.Get(ctx, id); errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}

	// Owner-only-Guard (F-261, analog aclSet/aclDelete). Legacy-Bypass
	// (keine Identität) erlaubt den Zugriff — operator-kontrolliert,
	// kein owner-Konzept ohne Forward-Auth (ADR-0059 §2).
	if ident := IdentityFromContext(r.Context()); ident != nil && a.acl != nil {
		callerEmail := storage.NormalizeEmail(ident.Email) // F-260
		role, found, err := a.acl.GetRole(ctx, id, callerEmail)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		}
		if !found || role != storage.RoleOwner {
			writeError(w, http.StatusForbidden, "forbidden: nur der Owner darf die ACL-Liste lesen")
			return
		}
	}

	entries, err := a.acl.ListByBoard(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl list failed", err)
		return
	}

	out := make([]aclEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, aclEntry{Email: e.UserEmail, Role: e.Role, Created: e.Created})
	}
	writeJSON(w, http.StatusOK, out)
}

// mitgliederAntwort ist die Antwort von GET /boards/{id}/mitglieder.
type mitgliederAntwort struct {
	Namen []string `json:"namen"`
	// Gekappt: es gab mehr als storage.MaxMitgliederNamen Namen (KS-1).
	Gekappt bool `json:"gekappt"`
}

// mitglieder — GET /boards/{id}/mitglieder (Karte #699)
// Anzeigenamen der Personen mit wirksamer Rolle — für die Bearbeiter-Auswahl
// (Karte #698) auch für Editor und Viewer. Bewusst NICHT die ACL-Liste:
// keine Adressen, keine Rollen, keine Domain-Einträge (F-261 bleibt gewahrt,
// storage.MitgliederNamen verwirft auch Namen, die eine Adresse sind).
//
// Zugang: jede wirksame Rolle (der ACL-Guard lässt GET auf Subressourcen ab
// viewer durch; hier zusätzlich geprüft, damit der Endpunkt nicht an einer
// geänderten Guard-Regel hängt). Agenten erreichen den Pfad nicht — die
// Positivliste in agent.go kennt ihn nicht. Legacy-Bypass (keine Identität)
// wie bei aclList: operator-kontrolliert, ADR-0059 §2.
func (a *BoardsAPI) mitglieder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "fehlende Board-ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if _, err := a.repo.Get(ctx, id); errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}
	if a.acl == nil {
		writeJSON(w, http.StatusOK, mitgliederAntwort{Namen: []string{}})
		return
	}
	if ident := IdentityFromContext(r.Context()); ident != nil {
		_, found, err := a.acl.GetRole(ctx, id, storage.NormalizeEmail(ident.Email))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		}
		if !found {
			writeError(w, http.StatusForbidden, "forbidden: no access to board")
			return
		}
	}
	namen, gekappt, err := a.acl.MitgliederNamen(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mitglieder failed", err)
		return
	}
	writeJSON(w, http.StatusOK, mitgliederAntwort{Namen: namen, Gekappt: gekappt})
}

// aclSet — POST /boards/{id}/acl
// Legt einen ACL-Eintrag an oder ändert die Rolle (Upsert).
// Nur der owner darf Mutationen vornehmen (owner-only-Guard hier im Handler).
func (a *BoardsAPI) aclSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "fehlende Board-ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if _, err := a.repo.Get(ctx, id); errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}

	// Owner-only-Guard: Aufrufer muss owner sein (wenn Identität vorhanden).
	// Legacy-Bypass (no identity) erlaubt die Mutation — kein owner-Konzept
	// ohne Identität, operator-kontrolliert (ADR-0059 §2).
	if ident := IdentityFromContext(r.Context()); ident != nil && a.acl != nil {
		callerEmail := storage.NormalizeEmail(ident.Email) // F-260
		role, found, err := a.acl.GetRole(ctx, id, callerEmail)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		}
		if !found || role != storage.RoleOwner {
			writeError(w, http.StatusForbidden, "forbidden: nur der Owner darf ACL-Einträge setzen")
			return
		}
	}

	var req aclSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "ungültiger JSON-Body")
		return
	}
	// F-260: Ziel-Email zentral normalisieren BEVOR sie validiert, gegen den
	// Last-Owner-Schutz geprüft, gespeichert und im Activity-Log/Response
	// ausgegeben wird. Vorher wurde req.Email ungetrimmt gespeichert, während
	// der Middleware-/Caller-Lookup TrimSpace(+ToLower) sah → stille
	// Doppel-Identitäten und nicht treffende Owner-Guards.
	req.Email = storage.NormalizeEmail(req.Email)
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "email darf nicht leer sein")
		return
	}
	if !storage.ValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, "ungültige Rolle: owner, editor oder viewer erwartet")
		return
	}
	// ADR-0132: Freigabe für eine Domain ("@<domain>") nur, wenn der Betreiber
	// sie in ABOARD_ACL_DOMAINS erlaubt hat, und höchstens als editor.
	if err := storage.PruefeACLSubjektFuerBoard(id, req.Email, req.Role); err != nil {
		switch {
		case errors.Is(err, storage.ErrDomainRolle):
			writeError(w, http.StatusBadRequest, "eine Domain kann nicht owner sein: editor oder viewer erwartet")
		case errors.Is(err, storage.ErrDomainServiceBoard):
			writeError(w, http.StatusBadRequest, "das Service-Board kann nicht per Domain freigegeben werden")
		default:
			writeError(w, http.StatusBadRequest, "Domain ist für Freigaben nicht erlaubt (ABOARD_ACL_DOMAINS)")
		}
		return
	}

	// Last-Owner-Schutz: würde diese Operation die owner-Zahl auf 0 senken?
	if a.acl != nil {
		if err := a.checkLastOwnerSet(ctx, id, req.Email, req.Role); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}

	if err := a.acl.Set(ctx, id, req.Email, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "acl set failed", err)
		return
	}

	// Activity-Log: ACL-Eintrag gesetzt (Karte #200, ADR-0037).
	// note = "email:rolle" für Nachvollziehbarkeit ohne JSON-Parsing.
	a.recordActivity(r.Context(), id, actorFromContext(r.Context()),
		"acl-set", "role", "", req.Role, req.Email)

	writeJSON(w, http.StatusOK, map[string]string{"email": req.Email, "role": req.Role})
}

// aclDelete — DELETE /boards/{id}/acl?email=...
// Entfernt einen ACL-Eintrag. Idempotent.
// Nur der owner darf Mutationen vornehmen.
func (a *BoardsAPI) aclDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "fehlende Board-ID")
		return
	}
	targetEmail := storage.NormalizeEmail(r.URL.Query().Get("email")) // F-260
	if targetEmail == "" {
		writeError(w, http.StatusBadRequest, "query-parameter email fehlt")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if _, err := a.repo.Get(ctx, id); errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}

	// Owner-only-Guard (analog aclSet).
	if ident := IdentityFromContext(r.Context()); ident != nil && a.acl != nil {
		callerEmail := storage.NormalizeEmail(ident.Email) // F-260
		role, found, err := a.acl.GetRole(ctx, id, callerEmail)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		}
		if !found || role != storage.RoleOwner {
			writeError(w, http.StatusForbidden, "forbidden: nur der Owner darf ACL-Einträge löschen")
			return
		}
	}

	// Last-Owner-Schutz: würde dieses Delete die owner-Zahl auf 0 senken?
	if a.acl != nil {
		if err := a.checkLastOwnerDelete(ctx, id, targetEmail); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}

	if err := a.acl.Delete(ctx, id, targetEmail); err != nil {
		writeError(w, http.StatusInternalServerError, "acl delete failed", err)
		return
	}

	// Activity-Log: ACL-Eintrag entfernt (Karte #200, ADR-0037).
	a.recordActivity(r.Context(), id, actorFromContext(r.Context()),
		"acl-deleted", "", targetEmail, "", "")

	w.WriteHeader(http.StatusNoContent)
}

// checkLastOwnerSet prüft, ob ein POST die owner-Zahl auf 0 senken würde
// (Demoting des letzten owners auf editor/viewer).
// Gibt einen Fehler zurück wenn die Operation blockiert werden muss.
func (a *BoardsAPI) checkLastOwnerSet(ctx context.Context, boardID, email, newRole string) error {
	if newRole == storage.RoleOwner {
		return nil // Hinzufügen oder Halten einer owner-Rolle — kein Risiko
	}
	// Neue Rolle ist kein owner. Prüfen ob die Ziel-Email aktuell owner ist
	// und ob sie der letzte owner wäre.
	currentRole, found, err := a.acl.GetRole(ctx, boardID, email)
	if err != nil {
		return err
	}
	if !found || currentRole != storage.RoleOwner {
		return nil // war kein owner — kein Last-Owner-Problem
	}
	return a.enforceOwnerRemains(ctx, boardID, email)
}

// checkLastOwnerDelete prüft, ob ein DELETE die owner-Zahl auf 0 senken würde.
func (a *BoardsAPI) checkLastOwnerDelete(ctx context.Context, boardID, email string) error {
	currentRole, found, err := a.acl.GetRole(ctx, boardID, email)
	if err != nil {
		return err
	}
	if !found || currentRole != storage.RoleOwner {
		return nil // kein owner — kein Last-Owner-Problem
	}
	return a.enforceOwnerRemains(ctx, boardID, email)
}

// enforceOwnerRemains zählt die owner-Einträge; gibt einen Fehler zurück
// wenn nach dem Entfernen von excludeEmail kein owner mehr übrig wäre.
func (a *BoardsAPI) enforceOwnerRemains(ctx context.Context, boardID, excludeEmail string) error {
	entries, err := a.acl.ListByBoard(ctx, boardID)
	if err != nil {
		return err
	}
	ownerCount := 0
	for _, e := range entries {
		if e.Role == storage.RoleOwner && e.UserEmail != excludeEmail {
			ownerCount++
		}
	}
	if ownerCount == 0 {
		return errors.New("letzter Owner darf nicht entfernt werden")
	}
	return nil
}

// gleicherInhalt vergleicht zwei JSON-Stände nach Inhalt, nicht nach Bytes —
// Schritte auf dem Weg (Stempeln, Zähler) serialisieren neu und ändern dabei
// die Schlüsselreihenfolge (SR-1). Unlesbar zählt als verschieden.
func gleicherInhalt(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
