package api

import (
	"sync"
	"time"
)

// Sperrzeit-Budget des Ops-Endpunkts (F-1149). Ein Batch hält die
// datenbankweite Schreibsperre (BEGIN IMMEDIATE) für die Dauer von Fold,
// Schreiben und Protokoll. Das Aufruf-Limit (opsRateMax) begrenzt nur die
// Zahl der Batches; wie lange sie die Sperre halten, begrenzt dieses Budget.
//
// Gemessen auf dem Pi 5 (#550): 50 Ops ≈ 0,1 s. Ein normaler Agent braucht
// damit wenige Sekunden je Minute. Das Budget schneidet einen Agenten ab,
// der mit grossen Batches die Sperre für alle anderen blockiert.
const (
	// opsSperrBudgetAgent: Sperrzeit je Agent und opsRateFenster.
	opsSperrBudgetAgent = 10 * time.Second
	// opsSperrBudgetMensch: Sperrzeit je Mensch (und den Legacy-Eimer). Der
	// Browser schickt kleine, entprellte Batches; das Budget greift dort nur
	// bei Missbrauch, nicht bei einer aktiven Nutzerin.
	opsSperrBudgetMensch = 30 * time.Second
)

// opsSperrUhr misst die Sperrzeit — als Variable, damit ein Test lange
// Faltzeiten ohne echtes Warten vorspielen kann.
var opsSperrUhr = time.Now

// sperrBudget führt je Schlüssel die in einem Fenster verbrauchte Sperrzeit.
// Die Konten werden nicht abgeräumt, sondern beim nächsten Zugriff nach
// Fensterablauf neu begonnen — wie rateLimiter.buckets (admin.go). Schlüssel
// sind Prinzipale (E-Mail, agent:<name>, "anonym"), ihre Zahl ist durch die
// Nutzer- und Token-Zahl begrenzt, nicht durch Anfragen (F-1189, akzeptiert).
// Geprüft wird VOR dem Batch (ist noch etwas übrig?), gebucht DANACH mit der
// gemessenen Dauer. Ein Batch kann das Budget deshalb einmal überziehen,
// höchstens um seine eigene Dauer — die Fold-Zeitgrenze deckelt sie.
type sperrBudget struct {
	mu      sync.Mutex
	budget  time.Duration
	fenster time.Duration
	konten  map[string]*sperrKonto
	now     func() time.Time
}

type sperrKonto struct {
	verbraucht time.Duration
	seit       time.Time
}

func newSperrBudget(budget, fenster time.Duration) *sperrBudget {
	return &sperrBudget{budget: budget, fenster: fenster, konten: map[string]*sperrKonto{}, now: time.Now}
}

// frei meldet, ob der Schlüssel im laufenden Fenster noch Sperrzeit hat.
func (s *sperrBudget) frei(schluessel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.konto(schluessel)
	return k.verbraucht < s.budget
}

// buche zieht eine gemessene Sperrzeit ab.
func (s *sperrBudget) buche(schluessel string, d time.Duration) {
	if d <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.konto(schluessel).verbraucht += d
}

// konto liefert das Konto im laufenden Fenster; ein abgelaufenes Fenster
// beginnt neu. Aufrufer hält s.mu.
func (s *sperrBudget) konto(schluessel string) *sperrKonto {
	now := s.now()
	k, ok := s.konten[schluessel]
	if !ok || now.Sub(k.seit) >= s.fenster {
		k = &sperrKonto{seit: now}
		s.konten[schluessel] = k
	}
	return k
}
