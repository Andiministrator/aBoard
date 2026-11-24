package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

// Karte #570 (ADR-0129 §5): Ein ungültiger ABOARD_LLM_OPENAI_BASE_URL-Wert
// hält den Start an — derselbe Fail-fast-Stil wie ABOARD_AUSGAENGE
// (main_ausgang_test.go), und zwar VOR dem Öffnen der Datenbank (K-B7): Bei
// einem Update mit Tippfehler dürfen keine Migrationen laufen, bevor der
// Start abbricht. Geprüft über die DB-Datei, die dann nicht existieren darf.
func TestRun_UngueltigeOpenAIBasisURLStopptStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ABOARD_DB", filepath.Join(dir, "aboard.db"))
	t.Setenv("ABOARD_ATTACHMENTS_DIR", filepath.Join(dir, "anhaenge"))
	t.Setenv("ABOARD_ADDR", "127.0.0.1:0")
	t.Setenv("ABOARD_UNIX_SOCKET", "")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv("ABOARD_AGENT_LISTEN", "")
	t.Setenv("ABOARD_AGENT_UNIX_SOCKET", "")
	t.Setenv("ABOARD_AUSGAENGE", "")
	t.Setenv("ABOARD_LLM_OPENAI_BASE_URL", "ftp://llm.example:8000/v1")

	fertig := make(chan error, 1)
	go func() { fertig <- run() }()
	select {
	case err := <-fertig:
		if err == nil || !strings.Contains(err.Error(), "ABOARD_LLM_OPENAI_BASE_URL") {
			t.Fatalf("run() = %v, erwartet Fehler zu ABOARD_LLM_OPENAI_BASE_URL", err)
		}
		if _, serr := os.Stat(filepath.Join(dir, "aboard.db")); serr == nil {
			t.Fatal("DB-Datei angelegt — die Prüfung läuft erst nach DB-Open/Migration (K-B7)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() startet trotz ungültigem ABOARD_LLM_OPENAI_BASE_URL")
	}
}

// Karte #570, Punkt 4 (ADR-0129 §5): Zusammenspiel mit dem Ausgangs-Schalter.
// openAIBaseURLWarnung ist als eigene Funktion testbar, ohne den ganzen
// Server zu starten (main.go extrahiert sie genau dafür aus run()).
func TestOpenAIBaseURLWarnung_HostGesperrt(t *testing.T) {
	pol := ausgang.Neu(true) // aus, nichts freigegeben
	w := openAIBaseURLWarnung("http://llm.example:8000/v1", pol)
	if w == "" {
		t.Fatal("erwartet Warnzeile, bekam \"\"")
	}
	for _, teil := range []string{"WARNUNG", "llm.example:8000", "ABOARD_AUSGAENGE_ERLAUBT"} {
		if !strings.Contains(w, teil) {
			t.Errorf("Warnzeile %q enthält nicht %q", w, teil)
		}
	}
}

func TestOpenAIBaseURLWarnung_HostFreigegeben(t *testing.T) {
	pol := ausgang.Neu(true, "llm.example:8000")
	if w := openAIBaseURLWarnung("http://llm.example:8000/v1", pol); w != "" {
		t.Errorf("erwartet keine Warnung bei freigegebenem Host, bekam %q", w)
	}
}

func TestOpenAIBaseURLWarnung_AusgangAn(t *testing.T) {
	pol := ausgang.Neu(false) // an — Pruefe lässt immer durch
	if w := openAIBaseURLWarnung("http://llm.example:8000/v1", pol); w != "" {
		t.Errorf("erwartet keine Warnung bei Ausgang=an, bekam %q", w)
	}
}

func TestOpenAIBaseURLWarnung_LeereBasisURL(t *testing.T) {
	pol := ausgang.Neu(true) // aus, nichts freigegeben — würde ohne Leer-Guard warnen
	if w := openAIBaseURLWarnung("", pol); w != "" {
		t.Errorf("erwartet keine Warnung bei leerer Basis-URL, bekam %q", w)
	}
}

// K-B2/K-B6 (Session-Kritiker Sprint 72): Start-Warnungen zur Basis.
func TestOpenAIBasisWarnungen(t *testing.T) {
	an := ausgang.Neu(false)
	enthaelt := func(w []string, teil string) bool {
		for _, z := range w {
			if strings.Contains(z, teil) {
				return true
			}
		}
		return false
	}
	w := openAIBasisWarnungen("http://llm.example:8000", nil, an, false)
	for _, teil := range []string{"Klartext", "keinen Pfad", "ABOARD_LLM_OPENAI_MODELLE ist leer"} {
		if !enthaelt(w, teil) {
			t.Errorf("Warnung %q fehlt in %v", teil, w)
		}
	}
	if w := openAIBasisWarnungen("https://llm.example:8000/v1", []string{"llama"}, an, false); len(w) != 0 {
		t.Errorf("https mit Pfad und Modellen: keine Warnung erwartet, bekam %v", w)
	}
	if w := openAIBasisWarnungen("http://127.0.0.1:8000/v1", []string{"llama"}, an, false); enthaelt(w, "Klartext") {
		t.Errorf("Loopback-http ist kein Klartext durchs Netz: %v", w)
	}
	if w := openAIBasisWarnungen("", nil, an, false); w != nil {
		t.Errorf("ohne Basis keine Warnung: %v", w)
	}
}

// Ungültige Modellliste hält den Start ebenfalls vor der DB an.
func TestRun_UngueltigeOpenAIModelleStopptStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ABOARD_DB", filepath.Join(dir, "aboard.db"))
	t.Setenv("ABOARD_ATTACHMENTS_DIR", filepath.Join(dir, "anhaenge"))
	t.Setenv("ABOARD_ADDR", "127.0.0.1:0")
	t.Setenv("ABOARD_UNIX_SOCKET", "")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv("ABOARD_AGENT_LISTEN", "")
	t.Setenv("ABOARD_AGENT_UNIX_SOCKET", "")
	t.Setenv("ABOARD_AUSGAENGE", "")
	t.Setenv("ABOARD_LLM_OPENAI_BASE_URL", "")
	t.Setenv("ABOARD_LLM_OPENAI_MODELLE", "zwei worte")
	fertig := make(chan error, 1)
	go func() { fertig <- run() }()
	select {
	case err := <-fertig:
		if err == nil || !strings.Contains(err.Error(), "ABOARD_LLM_OPENAI_MODELLE") {
			t.Fatalf("run() = %v, erwartet Fehler zu ABOARD_LLM_OPENAI_MODELLE", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() startet trotz ungültigem ABOARD_LLM_OPENAI_MODELLE")
	}
}
