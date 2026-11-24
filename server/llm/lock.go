package llm

// lock.go — In-Memory-Pending-Lock pro Karte (#299, ADR-0095 §5).
//
// Verhindert zwei gleichzeitige LLM-Sends auf dieselbe Karte (Race auf
// Kommentar-Ordnung, doppelte Kosten, verwirrende UX). Bewusst KEIN DB-
// Persist: Locks sind ephemer — Server-Neustart soll keine "vergessenen"
// Locks aktiv lassen, und nach einem Crash ist der Provider-Call sowieso
// verloren.
//
// Lauf-Kennung (F-1538): Acquire liefert eine Kennung, die nur dieser eine
// Lauf kennt. Release und Auffrischen wirken nur mit ihr. Bis F-1538 gab
// Release frei, sobald der NUTZER passte — beendete sich ein alter Lauf
// desselben Nutzers, löschte er die Sperre des neuen gleich mit.
//
// Stale-Cleanup und Auffrischen (F-1538): Eine Sperre gilt als verwaist,
// wenn sie länger als ttl NICHT AUFGEFRISCHT wurde — nicht, wenn sie älter
// als ttl ist. Ein Stream hat keine Gesamtfrist (Erstbyte-Frist 300 s,
// danach beliebig lange, solange Daten kommen); eine feste TTL ab Lauf-Start
// hätte ihn nach 90 s ungeschützt gelassen und einen zweiten Send auf
// dieselbe Karte zugelassen. Der Lauf hält die Sperre deshalb per Halten()
// im festen Takt frisch — zeitgesteuert, nicht pro Chunk, weil gerade die
// lange Wartezeit bis zum ersten Token (lokale Modelle: 30–104 s) keine
// Chunks liefert. Endet der Lauf, endet das Auffrischen, und spätestens
// ttl danach ist die Karte wieder frei — auch wenn Release nie ankäme.

import (
	"log"
	"strconv"
	"sync"
	"time"
)

// DefaultLockTTL ist die Standard-Stale-Schwelle (ADR-0095 §5): so lange
// darf ein Lauf ohne Auffrischen bleiben, bevor seine Sperre als verwaist
// gilt. Halten() frischt im Takt ttl/3 auf — zwei verpasste Takte werden
// verziehen, erst der dritte gibt die Karte frei.
const DefaultLockTTL = 90 * time.Second

// SperrSchluessel bildet den Schlüssel einer Kartensperre aus Board- und
// Karten-ID (Kritiker K2-3, Sprint 82). Bis dahin war der Schlüssel die
// Karten-ID allein. Ein Import behält aber die Karten-IDs und vergibt nur eine
// neue Board-ID — dieselbe Karten-ID steht dann in zwei Boards, und ein Send
// im einen Board sperrte die gleichnamige Karte im anderen. Alle Aufrufer
// (Acquire, Release, Halten, Holder) gehen über diese Funktion.
func SperrSchluessel(boardID, cardID string) string {
	return boardID + "/" + cardID
}

// LockHolder beschreibt einen aktiven Lock — zurückgegeben an den Caller,
// wenn Acquire wegen Konflikt fehlschlägt, damit das Frontend "läuft gerade
// von [User]" anzeigen kann.
type LockHolder struct {
	User     string    // User-ID (nicht Email, damit kein PII im Lock-Status)
	Provider string    // provider-Name (z. B. "anthropic")
	Model    string    // Modell (z. B. "claude-opus-4-7")
	Since    time.Time // Lock-Start (UTC) — bleibt beim Auffrischen unverändert
}

// lockEintrag ist der interne Zustand einer Sperre: der nach aussen
// sichtbare Halter plus Lauf-Kennung und Zeitpunkt der letzten Auffrischung.
type lockEintrag struct {
	holder  LockHolder
	lauf    string
	zuletzt time.Time // letzte Auffrischung (bzw. Acquire) — Basis der TTL
}

// LockManager hält die Lock-Map. Goroutine-sicher via Mutex (sync.Mutex
// reicht — keine Hot-Read-Pfade, nur kurze Acquire/Release-Critical-Sections).
type LockManager struct {
	mu    sync.Mutex
	locks map[string]lockEintrag
	ttl   time.Duration
	now   func() time.Time // injizierbar für Tests
	seq   uint64           // Zähler für Lauf-Kennungen, nie wiederverwendet
}

// NewLockManager legt einen Manager mit der angegebenen Stale-TTL an.
// ttl <= 0 → DefaultLockTTL.
func NewLockManager(ttl time.Duration) *LockManager {
	if ttl <= 0 {
		ttl = DefaultLockTTL
	}
	return &LockManager{
		locks: map[string]lockEintrag{},
		ttl:   ttl,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// SetClock setzt eine Test-Clock. Nur für Unit-Tests.
func (m *LockManager) SetClock(clock func() time.Time) {
	m.mu.Lock()
	m.now = clock
	m.mu.Unlock()
}

// TTL liefert die Stale-Schwelle (für den Auffrisch-Takt der Aufrufer).
func (m *LockManager) TTL() time.Duration { return m.ttl }

// Acquire versucht, einen Lock auf cardID zu setzen. Bei Erfolg liefert es
// die Lauf-Kennung, mit der allein Release und Refresh wirken. Bei Konflikt
// (acquired=false, lauf="") liefert holder den aktuellen Halter. Verwaiste
// Locks (länger als ttl nicht aufgefrischt) werden ersetzt; die Kennung des
// verdrängten Laufs ist danach wirkungslos.
func (m *LockManager) Acquire(cardID, user, provider, model string) (lauf string, acquired bool, holder LockHolder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.locks[cardID]; ok {
		if now.Sub(existing.zuletzt) < m.ttl {
			return "", false, existing.holder
		}
		// Verwaist → ersetzen.
	}
	m.seq++
	entry := lockEintrag{
		holder:  LockHolder{User: user, Provider: provider, Model: model, Since: now},
		lauf:    strconv.FormatUint(m.seq, 10),
		zuletzt: now,
	}
	m.locks[cardID] = entry
	return entry.lauf, true, entry.holder
}

// Refresh frischt die Sperre des Laufs lauf auf cardID auf. false, wenn
// dieser Lauf die Sperre nicht (mehr) hält — dann ändert sich nichts. Eine
// bereits verwaiste, aber noch nicht ersetzte Sperre desselben Laufs wird
// wiederbelebt: Niemand anderes hat sie übernommen, der Lauf lebt also noch
// als einziger.
func (m *LockManager) Refresh(cardID, lauf string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.locks[cardID]
	if !ok || lauf == "" || existing.lauf != lauf {
		return false
	}
	existing.zuletzt = m.now()
	m.locks[cardID] = existing
	return true
}

// Release gibt den Lock des Laufs lauf frei. Mit fremder, alter oder leerer
// Kennung ein No-op; doppelter Release ebenso (F-1538).
func (m *LockManager) Release(cardID, lauf string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.locks[cardID]
	if !ok || lauf == "" || existing.lauf != lauf {
		return
	}
	delete(m.locks, cardID)
}

// Halten frischt die Sperre im Takt ttl/3 auf, bis die zurückgegebene
// stop-Funktion gerufen wird (idempotent; per defer direkt nach Acquire).
// Verliert der Lauf die Sperre trotzdem (Takt um mehr als ttl verschleppt,
// ein anderer Send hat übernommen), endet das Auffrischen mit einer
// Log-Zeile — der Lauf selbst wird nicht abgebrochen.
func (m *LockManager) Halten(cardID, lauf string) (stop func()) {
	return m.halten(cardID, lauf, m.ttl/3)
}

func (m *LockManager) halten(cardID, lauf string, takt time.Duration) func() {
	fertig := make(chan struct{})
	beendet := make(chan struct{})
	go func() {
		defer close(beendet)
		ticker := time.NewTicker(takt)
		defer ticker.Stop()
		for {
			select {
			case <-fertig:
				return
			case <-ticker.C:
				if !m.Refresh(cardID, lauf) {
					log.Printf("llm-lock: Sperre auf Karte %s nicht mehr bei Lauf %s — Auffrischen beendet", cardID, lauf)
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(fertig)
			<-beendet
		})
	}
}

// Holder liefert den aktuellen Halter für cardID oder (LockHolder{}, false),
// wenn keiner aktiv ist. Verwaiste Locks werden NICHT von Holder() ersetzt
// (read-only); erst Acquire räumt sie weg.
func (m *LockManager) Holder(cardID string) (LockHolder, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.locks[cardID]
	if !ok {
		return LockHolder{}, false
	}
	if m.now().Sub(e.zuletzt) >= m.ttl {
		return LockHolder{}, false
	}
	return e.holder, true
}
