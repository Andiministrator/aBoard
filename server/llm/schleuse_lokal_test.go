package llm

// schleuse_lokal_test.go — Aufruf des lokalen Modells für die Ausgangsschleuse
// (Karte #685, ADR-0134 §1, §3.2).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

// Ohne Firmenmodus, ohne eigene Basis-URL oder mit nicht erlaubtem Modell
// gibt es keinen Aufruf — dann ginge Text an OpenAI selbst oder an ein
// Modell, das der Betreiber nicht freigegeben hat.
func TestSchleuseModellAufruf_NurLokalesFirmenmodell(t *testing.T) {
	f, err := NeuerFirmenmodus("server-key", []string{"lokal"})
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]func(context.Context, string, string) (string, error){
		"ohne firmenmodus":  SchleuseModellAufruf(nil, "http://llm.internal:8000/v1", "ua", "lokal"),
		"ohne basis-url":    SchleuseModellAufruf(f, " ", "ua", "lokal"),
		"ohne modell":       SchleuseModellAufruf(f, "http://llm.internal:8000/v1", "ua", ""),
		"modell nicht frei": SchleuseModellAufruf(f, "http://llm.internal:8000/v1", "ua", "anderes"),
	} {
		if a != nil {
			t.Errorf("%s: Aufruf vorhanden", name)
		}
	}
	if SchleuseModellAufruf(f, "http://llm.internal:8000/v1", "ua", "lokal") == nil {
		t.Errorf("Firmenmodus mit Basis-URL: kein Aufruf")
	}
}

// Gegen ein OpenAI-kompatibles Gegenüber: Serverschlüssel, Modell, System
// und Text gehen hin, der Text kommt zurück; eine abgeschnittene Antwort ist
// ein Fehler.
func TestSchleuseModellAufruf_GegenHTTPTest(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	finish := "stop"
	var body struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		User string `json:"user"`
	}
	var auth, pfad string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, pfad = r.Header.Get("Authorization"), r.URL.Path
		roh, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(roh, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"lokal","choices":[{"finish_reason":"`+finish+`","message":{"content":"{\"funde\":[]}"}}]}`)
	}))
	defer srv.Close()
	f, _ := NeuerFirmenmodus("server-key", []string{"lokal"})
	aufruf := SchleuseModellAufruf(f, srv.URL+"/v1", "ua", "lokal")
	got, err := aufruf(context.Background(), "SYSTEM", "TEXT")
	if err != nil || got != `{"funde":[]}` {
		t.Fatalf("Antwort: %q %v", got, err)
	}
	if auth != "Bearer server-key" || pfad != "/v1/chat/completions" || body.Model != "lokal" || body.User != "" {
		t.Errorf("Anfrage: auth=%q pfad=%q modell=%q user=%q", auth, pfad, body.Model, body.User)
	}
	if len(body.Messages) != 2 || body.Messages[0].Content != "SYSTEM" || body.Messages[1].Content != "TEXT" {
		t.Errorf("Nachrichten: %+v", body.Messages)
	}
	finish = "length"
	if _, err := aufruf(context.Background(), "S", "T"); !errors.Is(err, ErrSchleuseAntwortAbgeschnitten) {
		t.Errorf("abgeschnitten: %v", err)
	}
}

// F-1617: Die zweite Stufe läuft nur gegen ein internes Ziel — Adresse aus
// schleuse.InternesZiel, localhost, Name ohne Punkt oder mit interner
// Endung. Öffentliche Namen und Adressen: kein Aufruf.
func TestSchleuseBasisIntern(t *testing.T) {
	for basis, intern := range map[string]bool{
		"http://llm.internal:8000/v1":       true,
		"https://owui.intern" + "/api":      true,
		"http://127.0.0.1:8000/v1":          true,
		"http://[::1]:8000/v1":              true,
		"http://10.1.2.3/v1":                true,
		"http://192.168.0.5:11434/v1":       true,
		"http://llm:8000/v1":                true,
		"http://localhost:8000/v1":          true,
		"http://llm.home.arpa/v1":           true,
		"http://gpu.local./v1":              true,
		"http://vllm.internal/v1":           true,
		"https://api.openai.com/v1":         false,
		"https://llm.firma.de/v1":           false,
		"http://8.8.8.8/v1":                 false,
		"http://[2001:4860:4860::8888]/v1":  false,
		"https://openrouter.ai/api/v1":      false,
		"http://intern.example.com.evil/v1": false,
	} {
		if err := SchleuseBasisIntern(basis); (err == nil) != intern {
			t.Errorf("%s: intern=%v, Fehler %v", basis, intern, err)
		}
	}
	f, _ := NeuerFirmenmodus("server-key", []string{"lokal"})
	for _, basis := range []string{"https://api.openai.com/v1", "http://8.8.8.8/v1", "https://llm.firma.de/v1"} {
		if SchleuseModellAufruf(f, basis, "ua", "lokal") != nil {
			t.Errorf("%s: Aufruf trotz öffentlichem Ziel", basis)
		}
	}
}

// KS-10: Eine Fehlerantwort des lokalen Modells (auch ohne JSON) kommt nur
// als Art und Status zurück — ihr Text (womöglich Reste des Auszugs) nicht.
func TestSchleuseModellAufruf_FehlerOhneAntworttext(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	status := http.StatusBadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "Fehler bei Eingabe: Anna Müller GEHEIMRESTE des Auszugs")
	}))
	defer srv.Close()
	f, _ := NeuerFirmenmodus("server-key", []string{"lokal"})
	aufruf := SchleuseModellAufruf(f, srv.URL+"/v1", "ua", "lokal")
	_, err := aufruf(context.Background(), "S", "T")
	var sf *SchleuseModellFehler
	if !errors.As(err, &sf) || sf.Status != http.StatusBadRequest || !errors.Is(err, ErrProviderRejected) {
		t.Fatalf("4xx: %#v", err)
	}
	status = http.StatusBadGateway
	_, err = aufruf(context.Background(), "S", "T")
	if !errors.As(err, &sf) || sf.Status != http.StatusBadGateway || !errors.Is(err, ErrProviderUnreachable) {
		t.Fatalf("5xx: %#v", err)
	}
	for _, s := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		status = s
		if _, err := aufruf(context.Background(), "S", "T"); err == nil || strings.Contains(err.Error(), "GEHEIM") || strings.Contains(err.Error(), "Anna") {
			t.Errorf("%d: %v", s, err)
		}
	}
}
