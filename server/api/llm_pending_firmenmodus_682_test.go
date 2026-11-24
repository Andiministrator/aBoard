package api_test

// llm_pending_firmenmodus_682_test.go — Karte #682, F-1553 (ADR-0133
// Nachtrag 4): Im Firmenmodus verrät die Kartensperre nur „belegt". Wer gerade
// fragt und mit welchem Modell, sieht nur die fragende Person selbst. Ohne
// Firmenmodus bleibt alles sichtbar wie bisher (Gegenprobe je Fall).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

const pfad682Pending = "/boards/b1/cards/c1/llm-pending"

// halter liest das Feld holder aus einer Antwort (llm-pending bzw. 423).
func halter682(t *testing.T, body string) (map[string]any, map[string]any) {
	t.Helper()
	var erg map[string]any
	if err := json.Unmarshal([]byte(body), &erg); err != nil {
		t.Fatalf("Antwort unlesbar: %v — %s", err, body)
	}
	h, ok := erg["holder"].(map[string]any)
	if !ok {
		t.Fatalf("holder fehlt oder null — die Sperre muss sichtbar bleiben (ADR-0095 §5): %s", body)
	}
	return erg, h
}

// A hält die Sperre (Modell firmenModell). Abgefragt wird als a (Halter), b
// (anderer Editor) und c (Viewer).
func TestPending682_PersonUndModellNurFuerFragende(t *testing.T) {
	for _, fall := range []struct {
		name  string
		firma bool
	}{{"firmenmodus", true}, {"ohne-firmenmodus", false}} {
		t.Run(fall.name, func(t *testing.T) {
			var fm *llm.Firmenmodus
			if fall.firma {
				fm = neuerFirmenmodus(t)
			}
			env := newEnv674(t, fm, 0)
			if _, ok, _ := env.locks.Acquire(llm.SperrSchluessel("b1", "c1"), "u-a", "openai", firmenModell); !ok {
				t.Fatal("Sperre nicht erhalten")
			}
			for _, wer := range []struct {
				email  string
				selbst bool
			}{{"a@x.de", true}, {"b@x.de", false}, {"c@x.de", false}} {
				status, body, _ := env.rufe(t, wer.email, http.MethodGet, pfad682Pending, nil)
				if status != http.StatusOK {
					t.Fatalf("%s: Status %d %s", wer.email, status, body)
				}
				erg, h := halter682(t, body)
				if got, _ := erg["isSelf"].(bool); got != wer.selbst {
					t.Errorf("%s: isSelf = %v, erwartet %v", wer.email, got, wer.selbst)
				}
				if s, _ := h["since"].(string); s == "" {
					t.Errorf("%s: since fehlt — „belegt seit“ muss bleiben: %s", wer.email, body)
				}
				offen := wer.selbst || !fall.firma
				if offen {
					if h["user"] != "a@x.de" || h["model"] != firmenModell || h["provider"] != "openai" {
						t.Errorf("%s: Halter unvollständig: %s", wer.email, body)
					}
					continue
				}
				for _, feld := range []string{"user", "provider", "model"} {
					if _, da := h[feld]; da {
						t.Errorf("%s: Feld %q im Firmenmodus sichtbar: %s", wer.email, feld, body)
					}
				}
				if strings.Contains(body, "a@x.de") || strings.Contains(body, "u-a") || strings.Contains(body, firmenModell) {
					t.Errorf("%s: Antwort nennt Person oder Modell: %s", wer.email, body)
				}
			}
		})
	}
}

// Der 423 von llm-send trägt denselben Halter — im Firmenmodus darf auch er
// Person und Modell einer fremden Anfrage nicht verraten.
func TestSend682_GesperrtOhnePersonImFirmenmodus(t *testing.T) {
	// Send und Stream geben denselben 423 — beide Wege prüfen.
	for _, fall := range []struct {
		name, pfad string
		firma      bool
	}{
		{"send/firmenmodus", pfad674Send, true}, {"send/ohne-firmenmodus", pfad674Send, false},
		{"stream/firmenmodus", pfad674Stream, true}, {"stream/ohne-firmenmodus", pfad674Stream, false},
	} {
		t.Run(fall.name, func(t *testing.T) {
			var fm *llm.Firmenmodus
			if fall.firma {
				fm = neuerFirmenmodus(t)
			}
			env := newEnv674(t, fm, 0)
			if _, ok, _ := env.locks.Acquire(llm.SperrSchluessel("b1", "c1"), "u-a", "openai", firmenModell); !ok {
				t.Fatal("Sperre nicht erhalten")
			}
			status, body, _ := env.rufe(t, "b@x.de", http.MethodPost, fall.pfad,
				map[string]any{"provider": "openai", "model": firmenModell})
			if status != http.StatusLocked {
				t.Fatalf("Status %d %s, erwartet 423", status, body)
			}
			_, h := halter682(t, body)
			if fall.firma {
				for _, feld := range []string{"user", "provider", "model"} {
					if _, da := h[feld]; da {
						t.Errorf("Feld %q im Firmenmodus sichtbar: %s", feld, body)
					}
				}
				return
			}
			if h["user"] != "u-a" || h["model"] != firmenModell {
				t.Errorf("ohne Firmenmodus Halter unvollständig: %s", body)
			}
		})
	}
}
