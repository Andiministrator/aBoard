package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// SSE-Konstanten — als var, damit Tests sie temporär verkürzen können.
var (
	// heartbeatInterval steuert das Heartbeat-Intervall (SSE-Comment-Frame).
	// 25 s ist konservativ unter dem typischen 30-s-Idle-Timeout vieler
	// Reverse-Proxies (nginx default proxy_read_timeout, Caddy default).
	heartbeatInterval = 25 * time.Second

	// streamSchreibfrist ist die Frist JE Schreibvorgang auf einem
	// langlebigen Stream (SSE hier, LLM-Stream in llm_stream.go). Kritiker
	// Sprint 70, K-A3: Die erste Fassung von F-1127 hob die Frist ganz auf.
	// Verschwindet der Client ohne FIN/RST oder liest er nicht mehr,
	// blockierte Write dann bis zum TCP-Timeout des Kernels. Jetzt wird die
	// Frist vor jedem Schreiben neu gesetzt: Ein lebender Stream läuft
	// beliebig lange, ein einzelner hängender Write höchstens so lange.
	streamSchreibfrist = 60 * time.Second

	// sseHoechstdauer beendet einen Board-Stream nach dieser Zeit (Kritiker
	// Sprint 70, K-A2). Die Rechte prüft der ACLGuard nur beim Verbinden;
	// solange die 30-s-Schreibfrist des Servers galt, geschah das nebenbei
	// alle 30 s. Ohne Obergrenze bekäme ein Tab nach einem Rechteentzug
	// weiter jedes board:updated. EventSource-Clients verbinden nach dem
	// Schliessen selbst neu — und werden dabei erneut geprüft.
	sseHoechstdauer = 10 * time.Minute

	// subscriberBuffer ist die Kapazität pro Subscriber-Channel. Klein, aber
	// > 0 — Publish darf nicht blockieren wenn der Subscriber noch das letzte
	// Event verarbeitet (Frame-Schreiben + Flush). Bei Überlauf wird das
	// Event verworfen statt den Publisher zu blockieren (siehe Publish).
	subscriberBuffer = 8
)

// Event ist die Nutzlast eines an einen Subscriber gepushten Ereignisses.
// Type ist der SSE-event-Name (z. B. "board:updated"), Data der Payload-
// String (typischerweise ein JSON-Objekt). ID darf leer sein.
type Event struct {
	Type string
	Data string
	ID   string
}

// EventBroker hält pro Board-ID eine Liste von Subscriber-Channels und
// broadcastet Events an alle Subscribent:innen dieses Boards.
//
// Konkurrierender Zugriff:
//   - Subscribe/Unsubscribe/Publish verwenden RWMutex.
//   - Schreibe-Operationen auf Subscriber-Channels sind nicht-blockierend
//     (default-case): ist der Channel voll, wird das Event verworfen, damit
//     ein langsamer Client nicht den ganzen Broker blockiert.
type EventBroker struct {
	mu          sync.RWMutex
	subscribers map[string]map[*subscriber]struct{}
}

// subscriber ist ein einzelner Subscribent eines Boards. Wir benutzen einen
// Pointer-als-Key, damit Unsubscribe O(1) statt O(n) ist.
type subscriber struct {
	boardID string
	ch      chan Event
}

// NewEventBroker erzeugt einen leeren Broker.
func NewEventBroker() *EventBroker {
	return &EventBroker{
		subscribers: make(map[string]map[*subscriber]struct{}),
	}
}

// Subscribe registriert einen neuen Subscriber für boardID und liefert den
// Empfangs-Channel + eine Unsubscribe-Funktion zurück. Die Unsubscribe-
// Funktion ist idempotent (mehrfacher Aufruf ist safe).
func (b *EventBroker) Subscribe(boardID string) (<-chan Event, func()) {
	sub := &subscriber{
		boardID: boardID,
		ch:      make(chan Event, subscriberBuffer),
	}
	b.mu.Lock()
	subs, ok := b.subscribers[boardID]
	if !ok {
		subs = make(map[*subscriber]struct{})
		b.subscribers[boardID] = subs
	}
	subs[sub] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if subs, ok := b.subscribers[boardID]; ok {
				if _, present := subs[sub]; present {
					delete(subs, sub)
					close(sub.ch)
				}
				if len(subs) == 0 {
					delete(b.subscribers, boardID)
				}
			}
		})
	}
	return sub.ch, unsubscribe
}

// Publish broadcastet event an alle Subscriber des Boards. Volle Subscriber-
// Channels werden übersprungen (nicht-blockierend) — die SSE-Schicht puffert
// und ein langsamer Client darf nicht den Mutationspfad bremsen.
//
// Liefert die Anzahl der Subscriber, an die zugestellt werden konnte (für
// Tests / Metriken).
func (b *EventBroker) Publish(boardID string, event Event) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	subs, ok := b.subscribers[boardID]
	if !ok {
		return 0
	}
	delivered := 0
	for sub := range subs {
		select {
		case sub.ch <- event:
			delivered++
		default:
			// Channel voll — verwerfen; Client kann den State per
			// nächster Mutation oder beim Reconnect via /boards/{id} neu
			// holen. ETag/UpdatedAt steht im Server-State.
		}
	}
	return delivered
}

// SubscriberCount liefert die aktuelle Subscriber-Anzahl für boardID. Nur
// für Tests gedacht.
func (b *EventBroker) SubscriberCount(boardID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[boardID])
}

// EventsAPI registriert den SSE-Endpoint /boards/{id}/events.
type EventsAPI struct {
	broker *EventBroker
	repo   *storage.BoardRepo
}

// NewEventsAPI erstellt einen neuen SSE-Handler. Der repo-Parameter dient
// dem initialen Existenz-Check und dem optionalen Initial-Snapshot beim
// Connect (404 wenn das Board nicht existiert).
func NewEventsAPI(broker *EventBroker, repo *storage.BoardRepo) *EventsAPI {
	return &EventsAPI{broker: broker, repo: repo}
}

// Register hängt die Events-Route an einen ServeMux. Bewusst ohne
// withBodyLimit (kein Body) und ohne Cache-Control-Wrap — SSE setzt eigene
// Header (siehe stream).
//
// F-233 / Sprint-28-Kritiker (akzeptiert, damals noch offen): Der SSE-Endpoint
// hat weiterhin keinen eigenen Origin-Check, ist aber inzwischen kein
// ungeschützter Pfad mehr — die Route liegt unter /boards/{id}/events und
// wird damit vom ACLGuard mit-erfasst (Karte #198, ADR-0059/0066: GET
// verlangt mindestens viewer-Rolle am Board). Der Forward-Auth-Stack (Karte
// #73, ADR-0057, seit Sprint 70 im strengen Modus per ADR-0129 §2) liefert
// die dafür nötige Identität. Ohne aktiven Forward-Auth-Trust (auth-loser
// Einzelbetrieb) greift weiterhin kein Auth — dann läuft der Server per
// Konvention hinter einem Reverse-Proxy + Trusted Network. Bei eigenständigem
// Bind ohne Proxy: vor Public-Exposure CORS strikt konfigurieren oder
// Reverse-Proxy davorsetzen.
func (a *EventsAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /boards/{id}/events", http.HandlerFunc(a.stream))
}

func (a *EventsAPI) stream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}

	// Existenz-Check: kein Subscribe für unbekannte Boards. 404 ist klarer
	// als ein offener Stream, der nie ein Event sieht.
	checkCtx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	_, err := a.repo.Get(checkCtx, id)
	cancel()
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// SSE-Header. text/event-stream, no-cache, keep-alive.
	// X-Accel-Buffering: no schaltet nginx-Proxy-Buffering ab — sonst
	// landen Events erst beim Connection-Close beim Client.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	// F-1127 (#544): main.go setzt WriteTimeout=30s für den ganzen
	// http.Server — eine ABSOLUTE Deadline auf der Connection ab
	// Anfrage-Start. Für diesen Stream wird sie vor jedem Schreiben um
	// streamSchreibfrist verlängert (K-A3), statt sie aufzuheben. Der
	// server-weite WriteTimeout bleibt für alle anderen Endpunkte der
	// Slowloris-Schutz aus F-197.
	frist := neueRollendeFrist(w, "events")
	frist.verlaengern()

	// Subscribe NACH den Headern — Schließreihenfolge ist wichtig: zuerst
	// Unsubscribe (closed channel), dann return aus stream().
	events, unsubscribe := a.broker.Subscribe(id)
	defer unsubscribe()

	// Initial-Hello, damit Proxies die Verbindung als „aktiv" sehen und der
	// Client weiß, dass der Stream offen ist.
	if _, err := fmt.Fprintf(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	ende := time.NewTimer(sseHoechstdauer)
	defer ende.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ende.C:
			// K-A2: Stream beenden; der Client verbindet neu und durchläuft
			// dabei wieder die Rechteprüfung.
			return
		case <-heartbeat.C:
			frist.verlaengern()
			// SSE-Comment ist die kanonische Heartbeat-Form (RFC 8895 nicht
			// formell, aber WHATWG-EventSource-Standard).
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				// Channel wurde geschlossen — Broker hat uns rausgeworfen.
				return
			}
			frist.verlaengern()
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent serialisiert ein Event ins SSE-Wire-Format. Mehrzeilige
// Daten werden Zeile-für-Zeile mit `data:` gepräfixt — so verlangt es die
// EventSource-Spec.
func writeSSEEvent(w http.ResponseWriter, ev Event) error {
	if ev.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", ev.ID); err != nil {
			return err
		}
	}
	if ev.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", ev.Type); err != nil {
			return err
		}
	}
	// data:-Frame — wir senden den Payload als eine Zeile (typisch JSON).
	// Falls in Zukunft Multi-Line-Daten anfallen, hier per strings.Split
	// ergänzen.
	if _, err := fmt.Fprintf(w, "data: %s\n\n", ev.Data); err != nil {
		return err
	}
	return nil
}

// boardUpdatedPayload ist die JSON-Form des board:updated-Events. Der ETag-
// String ist identisch zum HTTP-ETag-Header (gequotetes updated_at).
//
// Bewusst minimal: kein State im Event — der Client holt den Board-Body
// per GET /boards/{id} und vergleicht ETag. Das hält die SSE-Frames klein
// und vermeidet Race-Conditions zwischen Event-Order und State-Snapshot.
type boardUpdatedPayload struct {
	BoardID   string `json:"board_id"`
	UpdatedAt int64  `json:"updated_at"`
	ETag      string `json:"etag"`
	Action    string `json:"action"` // "create" | "update" | "delete"
}

// publishBoardUpdated ist der zentrale Helper für Mutations-Pfade. Der
// Aufrufer sollte ihn NACH erfolgreichem DB-Commit aufrufen — vor dem
// Schreiben der HTTP-Response ist okay (Publish ist nicht-blockierend).
func publishBoardUpdated(broker *EventBroker, boardID string, updatedAt int64, action string) {
	if broker == nil || boardID == "" {
		return
	}
	payload := boardUpdatedPayload{
		BoardID:   boardID,
		UpdatedAt: updatedAt,
		ETag:      etag(updatedAt),
		Action:    action,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		// Marshalling kann hier praktisch nicht fehlschlagen — Logfehler
		// ist nicht kritisch genug, um die Mutation zu rollen.
		return
	}
	broker.Publish(boardID, Event{
		Type: "board:updated",
		Data: string(data),
		ID:   fmt.Sprintf("%d", updatedAt),
	})
}

// rollendeFrist setzt die Schreibfrist einer langlebigen Antwort vor jedem
// Schreibvorgang neu (K-A3). Unterstützt der ResponseWriter das nicht (etwa
// httptest.ResponseRecorder), wird das einmal geloggt und der Stream läuft
// mit der Server-Frist weiter.
type rollendeFrist struct {
	rc       *http.ResponseController
	name     string
	gemeldet bool
}

func neueRollendeFrist(w http.ResponseWriter, name string) *rollendeFrist {
	return &rollendeFrist{rc: http.NewResponseController(w), name: name}
}

func (f *rollendeFrist) verlaengern() {
	if err := f.rc.SetWriteDeadline(time.Now().Add(streamSchreibfrist)); err != nil && !f.gemeldet {
		f.gemeldet = true
		log.Printf("aboard-server: %s: SetWriteDeadline nicht unterstützt: %v", f.name, err)
	}
}
