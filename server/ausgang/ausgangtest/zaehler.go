// Package ausgangtest hilft den Tests des Ausgangs-Schalters (Karte #569):
// ein lokaler TCP-Listener, der zählt, wie oft jemand eine Verbindung
// aufgebaut hat. So lässt sich „kein Dial“ am eigenen Gegenüber messen, statt
// einen fremden Prozess zu beobachten.
package ausgangtest

import (
	"net"
	"sync/atomic"
	"testing"
)

// Zaehler ist ein Listener auf 127.0.0.1, der jede Verbindung annimmt, zählt
// und sofort schliesst.
type Zaehler struct {
	ln net.Listener
	n  atomic.Int64
}

// NeuerZaehler startet den Listener; t.Cleanup schliesst ihn.
func NeuerZaehler(t *testing.T) *Zaehler {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	z := &Zaehler{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			z.n.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return z
}

// Adresse liefert host:port.
func (z *Zaehler) Adresse() string { return z.ln.Addr().String() }

// Port liefert den Port.
func (z *Zaehler) Port() int { return z.ln.Addr().(*net.TCPAddr).Port }

// Anzahl der bisher angenommenen Verbindungen.
func (z *Zaehler) Anzahl() int64 { return z.n.Load() }
