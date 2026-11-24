package api

// llm_obergrenze.go — Obergrenze für Chat-Anfragen je Person (Karte #674,
// ADR-0133 Nachtrag 2 A6).
//
// Im Firmenmodus zahlt der Betreiber jede Anfrage über einen gemeinsamen
// Serverschlüssel; eine einzelne Person (oder ein entgleistes Skript in ihrem
// Browser) soll das Kontingent des Dienstkontos nicht allein aufbrauchen.
// Gezählt wird je angemeldeter Person (users.id), nicht je Adresse: Im Team-Betrieb
// sitzen viele Personen hinter demselben Proxy, eine Adress-Grenze träfe alle
// gemeinsam (vgl. agent_bremse.go). Agenten-Token erreichen llm-send und
// llm-send-stream nicht (Positivliste agentDarf), zählen also nicht mit.
//
// Ohne Firmenmodus gilt keine Obergrenze — jede Person zahlt mit ihrem
// eigenen Schlüssel, das Verhalten bleibt wie vor #674.
//
// Gegen einen Einbruch in den aBoard-Server schützt das nicht (ADR-0133
// Nachtrag 2): Wer den Server übernimmt, hat den Schlüssel. Davor schützt nur
// die Ratenbegrenzung des Dienstkontos in OWUI.

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LLMAnfragenEnvVar nennt die Betreiber-Vorgabe: höchstens so viele
// Chat-Anfragen je Person und Stunde.
const LLMAnfragenEnvVar = "ABOARD_LLM_ANFRAGEN_PRO_STUNDE"

// DefaultLLMAnfragenProStunde — Vorgabe ohne Variable. Eine Anfrage je
// Minute im Schnitt deckt auch ein zügiges Hin und Her an einer Karte ab und
// bremst trotzdem eine Schleife, die im Sekundentakt sendet.
const DefaultLLMAnfragenProStunde = 60

// maxLLMAnfragenProStunde — obere Schranke für die Variable. Mehr als das
// ist keine Obergrenze mehr, sondern ein Tippfehler.
const maxLLMAnfragenProStunde = 100000

// LLMAnfragenFenster — Zählfenster der Obergrenze.
const LLMAnfragenFenster = time.Hour

// KindAnfragenObergrenze — Fehlerart der 429-Antwort.
const KindAnfragenObergrenze = "anfragen-obergrenze"

// ParseLLMAnfragenProStunde liest ABOARD_LLM_ANFRAGEN_PRO_STUNDE. Leer →
// Vorgabe. Sonst eine ganze Zahl von 1 bis 100000; alles andere ist ein
// Fehler (Startabbruch wie bei ABOARD_LLM_PII). „0 = aus“ gibt es bewusst
// nicht: Die Obergrenze ist eine Auflage (A6), kein Komfort-Schalter.
func ParseLLMAnfragenProStunde(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLLMAnfragenProStunde, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxLLMAnfragenProStunde {
		return 0, fmt.Errorf("%s=%q: erwartet eine ganze Zahl von 1 bis %d (Chat-Anfragen je Person und Stunde)", LLMAnfragenEnvVar, raw, maxLLMAnfragenProStunde)
	}
	return n, nil
}

// WithAnfragenObergrenze setzt die Obergrenze (Anfragen je Person und
// Stunde). Sie wirkt nur im Firmenmodus. n < 1 lässt die Vorgabe stehen —
// die Prüfung der Variable macht ParseLLMAnfragenProStunde beim Start.
func (a *LLMSendAPI) WithAnfragenObergrenze(n int) *LLMSendAPI {
	if n >= 1 {
		a.anfragen = newRateLimiter(n, LLMAnfragenFenster)
	}
	return a
}

// llmObergrenzeAntwort — 429-Antwort mit den Zahlen, die die Oberfläche für
// eine verständliche Meldung braucht (Grenze, Wartezeit).
type llmObergrenzeAntwort struct {
	Kind           string `json:"kind"`
	Message        string `json:"message"`
	Grenze         int    `json:"grenze"`
	WartenSekunden int    `json:"wartenSekunden"`
}

// pruefeAnfragenObergrenze zählt eine Anfrage für userID. false → die
// 429-Antwort (mit Retry-After) ist geschrieben. Ohne Firmenmodus immer true,
// und es wird nichts gezählt.
func (a *LLMSendAPI) pruefeAnfragenObergrenze(w http.ResponseWriter, userID string) bool {
	if !a.firmenmodus.Aktiv() || a.anfragen == nil {
		return true
	}
	ok, warten := a.anfragen.allowOderWartezeit(userID)
	if ok {
		return true
	}
	sek := int(math.Ceil(warten.Seconds()))
	if sek < 1 {
		sek = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(sek))
	writeJSON(w, http.StatusTooManyRequests, llmObergrenzeAntwort{
		Kind:           KindAnfragenObergrenze,
		Message:        fmt.Sprintf("limit of %d chat requests per hour reached; try again in %d s", a.anfragen.max, sek),
		Grenze:         a.anfragen.max,
		WartenSekunden: sek,
	})
	return false
}

// allowOderWartezeit ist allow (admin.go) mit Auskunft, wie lange das
// Fenster noch läuft, wenn die Grenze erreicht ist.
func (l *rateLimiter) allowOderWartezeit(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	// F-1570: Eimer, deren Fenster abgelaufen ist, zählen bei der nächsten
	// Prüfung ohnehin neu — sie wegzuräumen ändert die Grenze nicht.
	if l.zugriffe++; l.zugriffe >= rateLimiterRaeumAlle {
		l.zugriffe = 0
		for k, e := range l.buckets {
			if now.Sub(e.since) >= l.window {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok || now.Sub(b.since) >= l.window {
		l.buckets[key] = &bucket{count: 1, since: now}
		return true, 0
	}
	if b.count >= l.max {
		return false, l.window - now.Sub(b.since)
	}
	b.count++
	return true, 0
}
