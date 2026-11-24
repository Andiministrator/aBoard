package api

// tor.go — Ein Tor für die eingebaute Anmeldung (ADR-0149 §5.1, Kritik B1;
// Karte #1070).
//
// Hereingelassen wird, wer auf der Vorab-Liste steht (ABOARD_OIDC_ZUGELASSEN
// samt ABOARD_BOOTSTRAP_OWNER) ODER ein aktives Konto hat. Ein gesperrtes
// Konto lässt nie herein, auch wenn die Adresse auf der Liste steht: Sonst
// bliebe „Sperren“ (Sprint 110) für jede gelistete Person wirkungslos.
//
// F-2922: Dasselbe gilt für ein Konto, das auf Bestätigung wartet (pending)
// oder zurückgesetzt ist (invited mit Rücksetz-Link): Dort geht der Konto-
// Zustand der Vorab-Liste vor, sonst bände der Rückruf jedes Anbieter-Konto
// mit bestätigter Adresse neu und „Zurücksetzen“ wäre wirkungslos. Eine frische
// Einladung (Zweck invite) einer gelisteten Person bleibt durch die Liste gedeckt.
//
// Das Tor fragt bei jeder Anfrage (Sitzungsweiche), beim Start
// (BeendeNichtZugelassene) und im Rückruf. Damit nicht jede Anfrage die
// Datenbank liest, merkt es sich den Kontozustand je Adresse kurz (TorCacheTTL).
// Wer sperrt, ruft Vergessen — dann wirkt die Sperre mit der nächsten Anfrage.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// TorCacheTTL: so lange gilt ein gelesener Kontozustand. Kurz, weil eine
// Sperre ohne Vergessen höchstens so lange nachläuft.
const TorCacheTTL = 5 * time.Second

// torCacheMax begrenzt den Speicher; ist er voll, beginnt er leer.
const torCacheMax = 4096

// KontoZustaende ist der Teil von storage.KontoRepo, den das Tor braucht.
type KontoZustaende interface {
	Zustand(ctx context.Context, email string) (string, error)
}

// VorabListeAusgesetzt ist der Teil von storage.KontoRepo, mit dem die Liste
// für ein wartendes oder zurückgesetztes Konto nicht gilt (F-2922). Fehlt er
// (Test-Attrappen), gilt die Liste bei invited wie bisher.
type VorabListeAusgesetzt interface {
	VorabListeAusgesetzt(ctx context.Context, email string) (bool, error)
}

// Tor entscheidet, wer herein darf.
type Tor struct {
	// Vorab ist die Vorab-Zulassung; nil heisst „niemand steht darauf“.
	Vorab *Zulassung
	// Konten liest den Kontozustand; nil heisst „es gibt keine Konten“.
	Konten KontoZustaende
	// TTL überschreibt TorCacheTTL (Tests); 0 = Voreinstellung.
	TTL time.Duration

	jetzt func() time.Time
	mu    sync.Mutex
	cache map[string]torEintrag
}

type torEintrag struct {
	zustand    string
	ausgesetzt bool // F-2922: Vorab-Liste gilt für diese Adresse nicht
	bis        time.Time
}

// NeuesTor baut das Tor aus Liste und Kontospeicher.
func NeuesTor(liste *Zulassung, konten KontoZustaende) *Tor {
	return &Tor{Vorab: liste, Konten: konten}
}

// Erlaubt sagt, ob die Adresse herein darf. Fehler beim Lesen des Kontos
// sind ein Fehler, kein „nein“ und kein „ja“: Der Aufrufer antwortet 500
// (fail-closed, aber unterscheidbar von „nicht zugelassen“).
func (t *Tor) Erlaubt(ctx context.Context, email string) (bool, error) {
	if t == nil {
		return false, nil
	}
	email = storage.NormalizeEmail(email)
	if email == "" || storage.IstDomainSubjekt(email) {
		return false, nil
	}
	zustand, ausgesetzt, err := t.zustand(ctx, email)
	if err != nil {
		return false, err
	}
	switch {
	case zustand == storage.KontoGesperrt:
		return false, nil
	case t.Vorab.Erlaubt(email) && !ausgesetzt:
		return true, nil
	default:
		return zustand == storage.KontoAktiv, nil
	}
}

// Vergessen verwirft den gemerkten Zustand einer Adresse (nach Sperren,
// Bestätigen, Zurücksetzen).
func (t *Tor) Vergessen(email string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.cache, storage.NormalizeEmail(email))
	t.mu.Unlock()
}

func (t *Tor) zustand(ctx context.Context, email string) (string, bool, error) {
	if t.Konten == nil {
		return "", false, nil
	}
	jetzt := time.Now()
	if t.jetzt != nil {
		jetzt = t.jetzt()
	}
	t.mu.Lock()
	if e, ok := t.cache[email]; ok && jetzt.Before(e.bis) {
		t.mu.Unlock()
		return e.zustand, e.ausgesetzt, nil
	}
	t.mu.Unlock()

	z, err := t.Konten.Zustand(ctx, email)
	if err != nil {
		return "", false, fmt.Errorf("tor: kontozustand: %w", err)
	}
	ausgesetzt := false
	if z == storage.KontoWartet {
		ausgesetzt = true
	} else if v, ok := t.Konten.(VorabListeAusgesetzt); ok && z == storage.KontoEingeladen {
		if ausgesetzt, err = v.VorabListeAusgesetzt(ctx, email); err != nil {
			return "", false, fmt.Errorf("tor: kontozustand: %w", err)
		}
	}
	ttl := t.TTL
	if ttl <= 0 {
		ttl = TorCacheTTL
	}
	t.mu.Lock()
	if t.cache == nil || len(t.cache) >= torCacheMax {
		t.cache = map[string]torEintrag{}
	}
	t.cache[email] = torEintrag{zustand: z, ausgesetzt: ausgesetzt, bis: jetzt.Add(ttl)}
	t.mu.Unlock()
	return z, ausgesetzt, nil
}
