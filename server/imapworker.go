package main

// imapworker.go — Neustart des IMAP-Arbeitsprozesses beim Config-Reload
// (Karte #760, F-1856; O-4/S-11 aus dem Kritiker-Lauf Sprint 86).
//
// Der Worker liest SMTP-, Service-Board- und Auto-Reply-Config nur beim Start.
// Ein Reload muss ihn deshalb neu starten. Drei Dinge dürfen dabei nicht
// schiefgehen: (1) der alte Worker muss BEENDET sein, bevor der neue startet —
// sonst bearbeiten zwei Worker dieselbe Mail (doppelte Karte, doppelter
// Auto-Reply); (2) eine gerade bearbeitete Mail wird zu Ende gebracht — das
// Stopp-Signal gilt nur zwischen zwei Mails (mail.pollCycle/processOne, V-1);
// (3) zwei gleichzeitige Reloads dürfen keinen Worker verwaisen lassen, und ein
// hängender alter Worker verhindert den Start eines zweiten (V-3).

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	mailpkg "github.com/Andiministrator/aBoard/server/mail"
)

// imapStopTimeout begrenzt das Warten auf den alten Worker. Danach wird
// geloggt und weitergemacht — ein hängender Worker darf den Reload nicht
// blockieren (RunWorker reagiert auf den Context, aber ein IMAP-Dial kann
// länger brauchen).
const imapStopTimeout = 20 * time.Second // unter dem 30-s-WriteTimeout des Servers

// starteMitWarten startet run(ctx) in einer Goroutine und liefert eine
// Stopp-Funktion, die den Context abbricht und wartet, bis run zurückgekehrt
// ist (höchstens `timeout`). Rückgabe true = der Worker ist beendet, false = er
// läuft noch. Die Funktion darf mehrfach aufgerufen werden (erneutes Warten).
func starteMitWarten(parent context.Context, run func(context.Context), timeout time.Duration, logf func(string, ...any)) func() bool {
	runCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(runCtx)
	}()
	return func() bool {
		cancel()
		select {
		case <-done:
			return true
		case <-time.After(timeout):
			if logf != nil {
				logf("aboard-server: imap-worker beendet sich nicht binnen %v", timeout)
			}
			return false
		}
	}
}

// errAlterWorkerHaengt: Der alte Worker hat sich nicht beendet; ein zweiter
// wird NICHT gestartet (sonst liefen beide). Der nächste Neustart wartet
// erneut auf den alten.
var errAlterWorkerHaengt = errors.New("der alte Mail-Worker beendet sich noch (bearbeitet gerade eine Mail?) — bitte in einer Minute erneut speichern")

// workerHalter hält genau einen laufenden Worker. Neustart ist serialisiert.
type workerHalter struct {
	mu   sync.Mutex
	stop func() bool
}

// Neustart stoppt den alten Worker (mit Warten) und startet über start() den
// neuen — beides unter dem Mutex, also nie zwei Läufer gleichzeitig. Ist der
// alte nach der Frist nicht beendet, bleibt er der aktuelle (stop wird gemerkt)
// und es wird kein neuer gestartet.
func (h *workerHalter) Neustart(start func() func() bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil {
		if !h.stop() {
			return errAlterWorkerHaengt
		}
		h.stop = nil
	}
	h.stop = start()
	return nil
}

// Stop beendet den laufenden Worker (Server-Shutdown).
func (h *workerHalter) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil {
		h.stop()
		h.stop = nil
	}
}

// imapReload ist der Reload-Hook der Sektion „imap“: Worker neu starten und
// das Ergebnis so melden, dass der Admin-Dialog drei Zustände unterscheiden
// kann — "ok" (läuft mit neuer Config), "disabled" (imap.enabled aus, nichts
// läuft; api.ReloadStatus) und Fehler (eingeschaltet, aber ungültig: der Worker
// startet nicht, der Text nennt den Grund).
func imapReload(h *workerHalter, laden func() mailpkg.IMAPConfig, start func() func() bool) error {
	if err := h.Neustart(start); err != nil {
		return err
	}
	cfg := laden()
	if !cfg.Enabled {
		return api.ReloadStatus("disabled")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("IMAP-Config ungültig, Worker läuft nicht: %w", err)
	}
	return nil
}
