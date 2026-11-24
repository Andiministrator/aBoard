package api

// llm_obergrenze_k2_internal_test.go — Kritiker K2-9 (Sprint 82): Die
// Obergrenze je Person ist ein FESTES Fenster ab der ersten gezählten Anfrage,
// kein gleitendes. Die Tests halten beides fest: dass das Fenster abläuft und
// die Person danach wieder darf, und dass an der Fenstergrenze kurz
// hintereinander fast doppelt so viele Anfragen durchgehen (dokumentiert in
// docs/server-betrieb.md, Firmenmodus).

import (
	"testing"
	"time"
)

// uhrLimiter liefert einen Limiter mit steuerbarer Uhr.
func uhrLimiter(max int, fenster time.Duration) (*rateLimiter, *time.Time) {
	jetzt := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	l := newRateLimiter(max, fenster)
	l.now = func() time.Time { return jetzt }
	return l, &jetzt
}

func TestObergrenze_K29_FensterLaeuftAb(t *testing.T) {
	l, jetzt := uhrLimiter(2, LLMAnfragenFenster)
	for i := 1; i <= 2; i++ {
		if ok, _ := l.allowOderWartezeit("u-a"); !ok {
			t.Fatalf("Anfrage %d abgewiesen", i)
		}
	}
	ok, warten := l.allowOderWartezeit("u-a")
	if ok || warten != LLMAnfragenFenster {
		t.Fatalf("dritte Anfrage: ok=%v warten=%v, erwartet abgewiesen, %v", ok, warten, LLMAnfragenFenster)
	}
	*jetzt = jetzt.Add(LLMAnfragenFenster - time.Second)
	if ok, warten := l.allowOderWartezeit("u-a"); ok || warten != time.Second {
		t.Fatalf("eine Sekunde vor Fensterende: ok=%v warten=%v, erwartet abgewiesen, 1s", ok, warten)
	}
	*jetzt = jetzt.Add(time.Second)
	if ok, _ := l.allowOderWartezeit("u-a"); !ok {
		t.Fatal("nach Ablauf des Fensters weiter abgewiesen")
	}
}

// An der Grenze: eine Anfrage zu Beginn, N-1 kurz vor Fensterende, N direkt
// danach — 2N-1 Anfragen in gut einer Sekunde. So ist das feste Fenster
// gebaut; die Doku sagt es, dieser Test hält fest, dass es so bleibt (oder
// dass die Doku nachgezogen wird, wenn jemand ein gleitendes Fenster baut).
func TestObergrenze_K29_FesteFensterGrenzeBisZweiN(t *testing.T) {
	const n = 5
	l, jetzt := uhrLimiter(n, LLMAnfragenFenster)
	if ok, _ := l.allowOderWartezeit("u-a"); !ok {
		t.Fatal("erste Anfrage abgewiesen")
	}
	*jetzt = jetzt.Add(LLMAnfragenFenster - time.Second)
	durch := 0
	for i := 0; i < n; i++ {
		if ok, _ := l.allowOderWartezeit("u-a"); ok {
			durch++
		}
	}
	*jetzt = jetzt.Add(time.Second)
	for i := 0; i < n; i++ {
		if ok, _ := l.allowOderWartezeit("u-a"); ok {
			durch++
		}
	}
	if durch != 2*n-1 {
		t.Errorf("an der Fenstergrenze %d Anfragen in einer Sekunde durch, erwartet %d (festes Fenster)", durch, 2*n-1)
	}
}

// F-1570: Abgelaufene Eimer werden geräumt, die Grenze wirkt weiter — auch
// für Schlüssel, die nicht abgelaufen sind.
func TestRateLimiter_F1570_RaeumtAbgelaufeneEimer(t *testing.T) {
	l, jetzt := uhrLimiter(2, time.Hour)
	for i := 0; i < 1000; i++ {
		l.allow("alt-" + string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('0'+i/676)))
	}
	if len(l.buckets) < 900 {
		t.Fatalf("Vorbedingung: %d Eimer", len(l.buckets))
	}
	*jetzt = jetzt.Add(2 * time.Hour)
	// Ein frischer Schlüssel erreicht seine Grenze mitten im Räumen.
	for i := 0; i < 2*rateLimiterRaeumAlle; i++ {
		l.allow("neu")
	}
	if n := len(l.buckets); n != 1 {
		t.Fatalf("%d Eimer nach Ablauf, erwartet 1 (nur „neu“)", n)
	}
	if ok, _ := l.allowOderWartezeit("neu"); ok {
		t.Fatal("Grenze wirkt nach dem Räumen nicht mehr")
	}
	*jetzt = jetzt.Add(time.Hour)
	if ok, _ := l.allowOderWartezeit("neu"); !ok {
		t.Fatal("nach Ablauf des Fensters weiter abgewiesen")
	}
}
