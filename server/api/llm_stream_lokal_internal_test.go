package api

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Karte #665: Lebenszeichen nur bei Stille, nach dem Stoppen nichts mehr.
func TestSSEWriter_Lebenszeichen_NurBeiStille_UndStoppt(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	s.letzteAus = time.Now()
	stopp := s.starteLebenszeichen(20 * time.Millisecond)

	time.Sleep(130 * time.Millisecond)
	stopp()
	s.mu.Lock()
	n := strings.Count(rec.Body.String(), ": lebenszeichen\n\n")
	s.mu.Unlock()
	if n < 2 {
		t.Fatalf("erwartet mindestens 2 Lebenszeichen bei Stille, gezählt %d", n)
	}

	vorher := rec.Body.Len()
	time.Sleep(60 * time.Millisecond)
	if rec.Body.Len() != vorher {
		t.Error("nach stopp() wurde noch geschrieben")
	}
	stopp() // idempotent
}

func TestSSEWriter_Lebenszeichen_SchweigtWennEventsFliessen(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec)
	s.letzteAus = time.Now()
	stopp := s.starteLebenszeichen(60 * time.Millisecond)
	for i := 0; i < 8; i++ { // alle 15 ms ein Event, 120 ms lang
		time.Sleep(15 * time.Millisecond)
		_ = s.writeEvent("token", map[string]string{"text": "x"})
	}
	stopp()
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(rec.Body.String(), "lebenszeichen") {
		t.Errorf("Lebenszeichen trotz fließender Events: %q", rec.Body.String())
	}
}

// zeitSchreiber merkt sich den Zeitpunkt jedes Schreibens (für die Lückenmessung).
type zeitSchreiber struct {
	*httptest.ResponseRecorder
	mu     sync.Mutex
	zeiten []time.Time
}

func (z *zeitSchreiber) Write(b []byte) (int, error) {
	z.mu.Lock()
	z.zeiten = append(z.zeiten, time.Now())
	z.mu.Unlock()
	return z.ResponseRecorder.Write(b)
}

// WriteString: io.WriteString nimmt sonst die (hochgereichte) Methode des Recorders.
func (z *zeitSchreiber) WriteString(str string) (int, error) { return z.Write([]byte(str)) }

// K3-1: Die GRÖSSTE Stille zwischen zwei Schreibvorgängen darf `abstand` nicht
// nennenswert überschreiten — auch nicht, wenn ein Event knapp nach einem
// Lebenszeichen kommt (alter Ticker mit 0,9-Schwelle: bis ≈ 1,9 × abstand).
func TestSSEWriter_Lebenszeichen_GroessteLuecke_HoechstensAbstand(t *testing.T) {
	zs := &zeitSchreiber{ResponseRecorder: httptest.NewRecorder()}
	s := newSSEWriter(zs)
	abstand := 100 * time.Millisecond
	s.letzteAus = time.Now()
	start := time.Now()
	zs.mu.Lock()
	zs.zeiten = append(zs.zeiten, start)
	zs.mu.Unlock()
	stopp := s.starteLebenszeichen(abstand)
	time.Sleep(112 * time.Millisecond) // Event kurz nach dem ersten Lebenszeichen
	_ = s.writeEvent("token", map[string]string{"text": "x"})
	time.Sleep(450 * time.Millisecond)
	stopp()
	zs.mu.Lock()
	defer zs.mu.Unlock()
	var max time.Duration
	for i := 1; i < len(zs.zeiten); i++ {
		if d := zs.zeiten[i].Sub(zs.zeiten[i-1]); d > max {
			max = d
		}
	}
	t.Logf("größte Stille %v, Schreibvorgänge %d", max, len(zs.zeiten))
	if max > abstand+abstand/2 {
		t.Fatalf("größte Stille %v = %.2f × Abstand (erlaubt ≈ 1×, Toleranz 1,5×)", max, float64(max)/float64(abstand))
	}
}
