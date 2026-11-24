package main

// Karte #760 (O-4, S-11): Neustart des IMAP-Workers beim Reload.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	mailpkg "github.com/Andiministrator/aBoard/server/mail"
)

func TestStarteMitWarten_StoppWartetBisRunZurueckIst(t *testing.T) {
	var fertig atomic.Bool
	stop := starteMitWarten(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(80 * time.Millisecond) // laufende Mail zu Ende bearbeiten
		fertig.Store(true)
	}, 5*time.Second, nil)
	if !stop() {
		t.Fatal("stop() meldet „nicht beendet“")
	}
	if !fertig.Load() {
		t.Fatal("stop() kehrte zurück, bevor der Worker beendet war")
	}
}

// V-3: Hängt der alte Worker über die Frist hinaus, startet KEIN zweiter; der
// Reload meldet einen Fehler. Sobald der alte weg ist, klappt der nächste.
func TestWorkerHalter_HaengenderWorkerVerhindertZweitenStart(t *testing.T) {
	release := make(chan struct{})
	var gestartet atomic.Int32
	hang := func() func() bool {
		gestartet.Add(1)
		return starteMitWarten(context.Background(), func(context.Context) { <-release }, 50*time.Millisecond, func(string, ...any) {})
	}
	h := &workerHalter{}
	if err := h.Neustart(hang); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	err := h.Neustart(hang)
	if !errors.Is(err, errAlterWorkerHaengt) {
		t.Fatalf("err = %v, erwartet errAlterWorkerHaengt", err)
	}
	if time.Since(t0) > 2*time.Second {
		t.Fatal("Neustart wartete über die Zeitgrenze hinaus")
	}
	if gestartet.Load() != 1 {
		t.Fatalf("%d Worker gestartet — der zweite darf nicht laufen, solange der alte hängt", gestartet.Load())
	}
	close(release) // der alte Worker kommt frei
	if err := h.Neustart(hang); err != nil {
		t.Fatalf("Neustart nach Freigabe: %v", err)
	}
	if gestartet.Load() != 2 {
		t.Fatalf("%d Worker gestartet, erwartet 2 (alter ersetzt)", gestartet.Load())
	}
}

// Zwei (hier: viele) gleichzeitige Reloads → nie zwei Worker zugleich, am Ende genau einer.
func TestWorkerHalter_GleichzeitigeNeustartsLassenGenauEinenWorkerLaufen(t *testing.T) {
	var aktiv, maxAktiv atomic.Int32
	start := func() func() bool {
		return starteMitWarten(context.Background(), func(ctx context.Context) {
			n := aktiv.Add(1)
			for {
				m := maxAktiv.Load()
				if n <= m || maxAktiv.CompareAndSwap(m, n) {
					break
				}
			}
			<-ctx.Done()
			time.Sleep(10 * time.Millisecond)
			aktiv.Add(-1)
		}, 5*time.Second, nil)
	}
	h := &workerHalter{}
	_ = h.Neustart(start)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = h.Neustart(start) }()
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond)
	if got := aktiv.Load(); got != 1 {
		t.Fatalf("laufende Worker = %d, erwartet genau 1", got)
	}
	if m := maxAktiv.Load(); m > 1 {
		t.Fatalf("zeitweise %d Worker gleichzeitig", m)
	}
	h.Stop()
	if got := aktiv.Load(); got != 0 {
		t.Fatalf("nach Stop laufen noch %d Worker", got)
	}
}

func TestImapReload_DreiZustaende(t *testing.T) {
	gueltig := mailpkg.IMAPConfig{Enabled: true, Host: "h", Port: 993, User: "u", Pass: "p", Folder: "INBOX"}
	ungueltig := mailpkg.IMAPConfig{Enabled: true, Host: "", Port: 993, User: "u", Pass: "p", Folder: "INBOX"}
	aus := mailpkg.IMAPConfig{Enabled: false}
	for _, tc := range []struct {
		name string
		cfg  mailpkg.IMAPConfig
		want string // "", "disabled" oder "fehler"
	}{{"gueltig", gueltig, ""}, {"aus", aus, "disabled"}, {"ungueltig", ungueltig, "fehler"}} {
		t.Run(tc.name, func(t *testing.T) {
			h := &workerHalter{}
			var gestartet atomic.Int32
			err := imapReload(h, func() mailpkg.IMAPConfig { return tc.cfg }, func() func() bool {
				gestartet.Add(1)
				return func() bool { return true }
			})
			if gestartet.Load() != 1 {
				t.Fatalf("start() %dx aufgerufen — der alte Worker muss immer ersetzt werden", gestartet.Load())
			}
			var rs api.ReloadStatus
			switch tc.want {
			case "":
				if err != nil {
					t.Fatalf("err = %v, erwartet nil", err)
				}
			case "disabled":
				if !errors.As(err, &rs) || string(rs) != "disabled" {
					t.Fatalf("err = %v, erwartet ReloadStatus(disabled)", err)
				}
			default:
				if err == nil || errors.As(err, &rs) {
					t.Fatalf("err = %v, erwartet echten Fehler", err)
				}
			}
		})
	}
}
