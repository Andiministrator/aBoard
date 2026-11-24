package main

// main_llm_firmenmodus_test.go — Karte #664 (ADR-0133 §2 + Nachtrag):
// Startprüfungen für ABOARD_LLM_SERVERSCHLUESSEL_FILE und ABOARD_LLM_PII.
//
// Derselbe Fail-fast-Stil wie main_llm_baseurl_test.go: ein ungültiger Wert
// hält den Start an, und zwar VOR dem Öffnen der Datenbank.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/llm"
)

const firmenAttrappe = "test-schluessel-firma-664-main"

func schluesseldatei(t *testing.T, inhalt string) string {
	t.Helper()
	pfad := filepath.Join(t.TempDir(), "llm-schluessel")
	if err := os.WriteFile(pfad, []byte(inhalt), 0o600); err != nil {
		t.Fatal(err)
	}
	return pfad
}

// Ohne die neuen Variablen: kein Firmenmodus, Vorgabe „nutzer“ — heutiges
// Verhalten. Die Variablen werden hier ausdrücklich entfernt.
func TestLLMBetreiberVorgaben_OhneVariablen(t *testing.T) {
	t.Setenv(llm.ServerschluesselEnvVar, "")
	t.Setenv(llm.PIIEnvVar, "")
	fm, pii, err := llmBetreiberVorgaben("http://llm.example:8000/v1", []string{"m"})
	if err != nil || fm.Aktiv() || pii != llm.PIINutzer {
		t.Fatalf("fm=%v pii=%q err=%v — erwartet kein Firmenmodus, nutzer", fm, pii, err)
	}
}

func TestLLMBetreiberVorgaben_Voreinstellungen(t *testing.T) {
	t.Setenv(llm.PIIEnvVar, "")
	t.Setenv(llm.ServerschluesselEnvVar, schluesseldatei(t, firmenAttrappe+"\n"))
	fm, pii, err := llmBetreiberVorgaben("http://llm.example:8000/v1", []string{"m"})
	if err != nil || !fm.Aktiv() || fm.Schluessel() != firmenAttrappe || pii != llm.PIIAus {
		t.Fatalf("mit Basis-URL: aktiv=%v pii=%q err=%v — erwartet Firmenmodus, aus", fm.Aktiv(), pii, err)
	}
	_, pii, err = llmBetreiberVorgaben("", []string{"m"})
	if err != nil || pii != llm.PIIPflicht {
		t.Fatalf("ohne Basis-URL: pii=%q err=%v — erwartet pflicht", pii, err)
	}
	t.Setenv(llm.PIIEnvVar, "nutzer")
	if _, pii, _ = llmBetreiberVorgaben("", []string{"m"}); pii != llm.PIINutzer {
		t.Errorf("Übersteuerung nutzer wirkt nicht: %q", pii)
	}
}

// Startabbruch über run(): Schlüsseldatei fehlt, Datei leer, Modellliste leer,
// PII-Wert ungültig. Keine Meldung nennt den Schlüssel, keine DB-Datei
// entsteht.
func TestRun_FirmenmodusStartpruefungen(t *testing.T) {
	faelle := []struct {
		name, datei, modelle, pii, erwartet string
	}{
		{"datei-fehlt", "FEHLT", "m", "", llm.ServerschluesselEnvVar},
		{"datei-leer", "\n", "m", "", llm.ServerschluesselEnvVar},
		{"modelle-leer", firmenAttrappe, "", "", "ABOARD_LLM_OPENAI_MODELLE"},
		{"pii-ungueltig", "", "", "vielleicht", llm.PIIEnvVar},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ABOARD_DB", filepath.Join(dir, "aboard.db"))
			t.Setenv("ABOARD_ATTACHMENTS_DIR", filepath.Join(dir, "anhaenge"))
			t.Setenv("ABOARD_ADDR", "127.0.0.1:0")
			t.Setenv("ABOARD_UNIX_SOCKET", "")
			t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
			t.Setenv("ABOARD_AGENT_LISTEN", "")
			t.Setenv("ABOARD_AGENT_UNIX_SOCKET", "")
			t.Setenv("ABOARD_AUSGAENGE", "")
			t.Setenv("ABOARD_OWUI_URL", "")
			t.Setenv("ABOARD_LLM_OPENAI_BASE_URL", "")
			t.Setenv("ABOARD_LLM_OPENAI_MODELLE", f.modelle)
			t.Setenv(llm.PIIEnvVar, f.pii)
			datei := ""
			switch f.datei {
			case "":
			case "FEHLT":
				datei = filepath.Join(dir, "gibt-es-nicht")
			default:
				datei = schluesseldatei(t, f.datei)
			}
			t.Setenv(llm.ServerschluesselEnvVar, datei)

			fertig := make(chan error, 1)
			go func() { fertig <- run() }()
			select {
			case err := <-fertig:
				if err == nil || !strings.Contains(err.Error(), f.erwartet) {
					t.Fatalf("run() = %v, erwartet Fehler zu %s", err, f.erwartet)
				}
				if strings.Contains(err.Error(), firmenAttrappe) {
					t.Errorf("Fehlermeldung enthält den Schlüssel: %v", err)
				}
				if _, serr := os.Stat(filepath.Join(dir, "aboard.db")); serr == nil {
					t.Fatal("DB-Datei angelegt — die Prüfung läuft erst nach DB-Open/Migration")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("run() startet trotz ungültiger Firmenmodus-Konfiguration")
			}
		})
	}
}
