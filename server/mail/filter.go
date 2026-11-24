package mail

// filter.go — Pre-Routing-Filter-Bausteine für #295 (ADR-0093 §7).
//
// Filter sind reine Funktionen mit IngestFilter-Signatur (siehe processor.go).
// Zwei Kategorien:
//   - HardDropFilters: Mail-Loop / Eigenversand / harte Header-Marker → drop
//     ohne Karte. Verlust ist gewollt (Mail kam von uns selbst oder ist
//     bekannt unerwünscht).
//   - SpamDetectors: weichere Heuristik (Blocklist, Rate-Limit). Treffer
//     routet die Mail nach col-spam (Aufbewahrung für False-Positive-Review),
//     außer SpamDrop=true setzt — dann verworfen.
//
// IMPORTANT: Filter dürfen keinen Zustand zwischen Aufrufen leaken, ausser
// dem expliziten in-Memory-State (Rate-Limit-Window).

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// EigenversandFilter droppt Mails, deren From == smtpFrom ist
// (case-insensitive). Verhindert die Loop-Klasse #1: unser SMTP-Versand
// kommt via Mail-Forwarding zurück, wird sonst als Ticket-Mail interpretiert.
//
// smtpFrom wird beim Filter-Bau normalisiert (lowercase + trim).
func EigenversandFilter(smtpFrom string) IngestFilter {
	from := strings.ToLower(strings.TrimSpace(smtpFrom))
	return func(p *ParsedMail) (string, bool) {
		if from == "" {
			return "", false
		}
		if p.From == from {
			return "Eigenversand (From == smtp.from)", true
		}
		return "", false
	}
}

// BlocklistFilter prüft die Sender-Email gegen die mail_spam_senders-Tabelle.
// Bei Treffer: skip=true mit reason="blocklist:…".
//
// CTX-Verbrauch: pro eingehender Mail ein DB-Roundtrip. Bei sehr großen
// Mail-Volumen könnte man die Blocklist in-memory cachen — im MVP nicht
// gerechtfertigt.
func BlocklistFilter(ctx context.Context, repo *storage.SpamRepo) IngestFilter {
	return func(p *ParsedMail) (string, bool) {
		if repo == nil || p.From == "" {
			return "", false
		}
		listed, err := repo.SpamSenderListed(ctx, p.From)
		if err != nil || !listed {
			return "", false
		}
		return "blocklist: " + p.From, true
	}
}

// RateLimitFilter limitiert auf maxPerHour Mails pro Sender-Adresse.
// Sliding-Window in-memory (kein DB-State, Reset bei Worker-Restart akzeptiert).
// Bei Überschreitung: skip=true mit reason="rate-limit:…".
//
// Thread-safe: Worker ist Single-Goroutine, aber für sichere Re-Use auch
// mit mehreren Aufrufern (Tests, paralleles Polling) wird ein sync.Mutex
// verwendet.
func RateLimitFilter(maxPerHour int) IngestFilter {
	if maxPerHour <= 0 {
		// 0 = effektiv deaktiviert (alle Mails passieren)
		return func(*ParsedMail) (string, bool) { return "", false }
	}
	rl := &rateLimiter{
		max:    maxPerHour,
		window: time.Hour,
		seen:   map[string][]time.Time{},
	}
	return rl.check
}

type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string][]time.Time
	// zugriffe zählt die Prüfungen für das gelegentliche Räumen (F-1578).
	zugriffe int
	// nowFn überschreibbar für Tests.
	nowFn func() time.Time
}

// rateLimitRaeumAlle: Nach so vielen Prüfungen werden Absender entfernt, deren
// Mails alle außerhalb des Fensters liegen. Die Schlüssel sind fremde
// Absender-Adressen und wachsen damit unbegrenzt; ohne eigene Goroutine
// (F-1578, analog zum Limiter in server/api).
const rateLimitRaeumAlle = 256

func (rl *rateLimiter) now() time.Time {
	if rl.nowFn != nil {
		return rl.nowFn()
	}
	return time.Now()
}

func (rl *rateLimiter) check(p *ParsedMail) (string, bool) {
	if p == nil || p.From == "" {
		return "", false
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	cutoff := now.Add(-rl.window)

	// Gelegentlich Absender ohne Eintrag im Fenster löschen — sie zählen bei
	// der nächsten Mail ohnehin bei null, die Grenze bleibt unverändert.
	if rl.zugriffe++; rl.zugriffe >= rateLimitRaeumAlle {
		rl.zugriffe = 0
		for k, zeiten := range rl.seen {
			if k == p.From {
				continue
			}
			aktiv := false
			for _, t := range zeiten {
				if t.After(cutoff) {
					aktiv = true
					break
				}
			}
			if !aktiv {
				delete(rl.seen, k)
			}
		}
	}

	// Window-Slice für diesen Sender bereinigen + neuen Eintrag prüfen
	old := rl.seen[p.From]
	var kept []time.Time
	for _, t := range old {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rl.max {
		// Nicht eintragen — sonst kann der Sender den Counter durch Spam
		// weiter hochziehen, obwohl er bereits geblockt ist.
		rl.seen[p.From] = kept
		return "rate-limit: " + p.From, true
	}
	kept = append(kept, now)
	rl.seen[p.From] = kept
	return "", false
}
