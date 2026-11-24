package notifications

// bus.go — Notification-Event-Bus.
//
// In-Memory-Queue (gepufferter channel) + Emit-API. Mehrere Emitter-Quellen
// (REST-Endpoint, Server-Worker wie mail-ingest und boardbackup) schreiben
// hinein; der Worker (worker.go) konsumiert.
//
// Bewusst KEINE Outbox-Persistenz für den MVP: bei Server-Restart gehen
// pending Coalescences/Throttle-Buckets verloren, aber das ist im Spec
// akzeptiert (Karte #305 agentContext: „bei Server-Restart gehen pending
// Coalescences verloren — Trade-off akzeptabel").

import (
	"context"
	"log"
	"sync"
	"time"
)

// Bus ist der Emit-Knoten. Threadsafe.
type Bus struct {
	mu     sync.Mutex
	queue  chan Event
	closed bool
	logger func(string, ...interface{})
}

// NewBus erzeugt einen Bus mit Default-Buffer. logger darf nil sein
// (No-Op). Der Worker (siehe worker.go) konsumiert die Queue separat.
func NewBus(logger func(string, ...interface{})) *Bus {
	if logger == nil {
		logger = func(string, ...interface{}) {}
	}
	return &Bus{
		queue:  make(chan Event, BusBuffer),
		logger: logger,
	}
}

// Emit reicht ein Event in die Queue. Nicht-blockierend mit 50ms-Timeout —
// wenn die Queue voll ist, wird das Event verworfen + geloggt. Niemals den
// Mutationspfad bremsen (Karte #305 Anti-Spam-Detail).
//
// Race-Sicherheit gegen Close(): der channel-send läuft UNTER dem Lock
// (siehe Lock-Pattern unten); Close() ist `b.closed=true` UND `close(channel)`
// ebenfalls unter Lock. Ein paralleler Emit kann dadurch nie auf einen
// gerade geschlossenen channel schreiben. Trade-off: ein blockierender
// Konsument im Receive() würde Emit blockieren — der gewählte 50ms-
// Timeout-Select garantiert aber, dass Emit nie länger als 50ms unter Lock
// steht.
func (b *Bus) Emit(ev Event) {
	if b == nil {
		return
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	select {
	case b.queue <- ev:
		// ok
	case <-time.After(50 * time.Millisecond):
		// Queue voll, Drop. Loggen aber NICHT crashen.
		b.logger("notifications: bus full — event dropped: type=%s actor=%s card=%s",
			ev.Type, ev.ActorUserID, ev.CardID)
	}
}

// Receive liefert den Read-Channel für den Worker. Nur einmal aufrufen —
// der Worker liest exklusiv.
func (b *Bus) Receive() <-chan Event {
	return b.queue
}

// Close stoppt den Bus. Nach Close verwirft Emit alle Events. Existing
// Events in der Queue können noch konsumiert werden, bis der Channel
// geleert ist; der Worker beendet sich beim Sehen des channel-Close.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	close(b.queue)
}

// Emitter ist das schmale Interface, das andere Pakete (mail, boardbackup,
// api) injizieren — niemand sieht den vollen Bus, nur Emit.
type Emitter interface {
	Emit(ev Event)
}

// NoopEmitter ist der Default für Test-Setups und für Pfade ohne aktiven
// Notification-Subsystem (z. B. Box=nil, kein Master-Key). Mutations-Pfade
// dürfen einen NoopEmitter immer aufrufen.
type NoopEmitter struct{}

// Emit ist No-Op.
func (NoopEmitter) Emit(_ Event) {}

// CtxKey ist der private Schlüssel für die Bus-Injektion via context.
// Aktuell nicht genutzt — Emitter wird direkt als Feld in API-Structs
// gehalten. Reserviert für spätere Cross-Goroutine-Übergabe.
type ctxBusKey struct{}

// WithEmitter legt einen Emitter in den Context (für Stellen, die kein
// direktes Feld halten können).
func WithEmitter(ctx context.Context, e Emitter) context.Context {
	return context.WithValue(ctx, ctxBusKey{}, e)
}

// FromContext liefert den Emitter aus dem Context oder NoopEmitter.
func FromContext(ctx context.Context) Emitter {
	if v, ok := ctx.Value(ctxBusKey{}).(Emitter); ok && v != nil {
		return v
	}
	return NoopEmitter{}
}

// Logf ist ein expliziter Public-Logger, damit andere Pakete (worker.go,
// digest.go) ohne Bus-Referenz Loggen können (wenn sie einen separaten
// Bus-Handle haben sollten sie diesen nutzen).
func Logf(logger func(string, ...interface{}), format string, args ...interface{}) {
	if logger == nil {
		log.Printf(format, args...)
		return
	}
	logger(format, args...)
}
