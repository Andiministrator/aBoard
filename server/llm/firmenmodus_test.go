package llm_test

// firmenmodus_test.go — Karte #664 (ADR-0133 §2 + Nachtrag 2026-10-04
// abends): Firmenmodus im llm-Paket.
//
// Geprüft: Schlüsseldatei lesen (und dabei nie den Inhalt melden),
// Startprüfungen, PII-Vorgabe samt Voreinstellungen, Registrierung nur von
// „openai“, Schwärzen bei %v/%#v/JSON und das OpenAI-`user`-Feld.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

const attrappenSchluessel = "test-schluessel-firma-664"

func schreibeSchluesseldatei(t *testing.T, inhalt string) string {
	t.Helper()
	pfad := filepath.Join(t.TempDir(), "llm-schluessel")
	if err := os.WriteFile(pfad, []byte(inhalt), 0o600); err != nil {
		t.Fatalf("Schlüsseldatei: %v", err)
	}
	return pfad
}

func TestFirmenmodusAusDatei_LeererPfadIstKeinFirmenmodus(t *testing.T) {
	for _, pfad := range []string{"", "   "} {
		fm, err := llm.FirmenmodusAusDatei(pfad, nil)
		if err != nil || fm != nil || fm.Aktiv() {
			t.Errorf("pfad %q: fm=%v err=%v, erwartet nil/nil/inaktiv", pfad, fm, err)
		}
	}
}

func TestFirmenmodusAusDatei_LiestUndTrimmt(t *testing.T) {
	pfad := schreibeSchluesseldatei(t, "\n  "+attrappenSchluessel+"  \n")
	fm, err := llm.FirmenmodusAusDatei(pfad, []string{"modell-a", "modell-b", "modell-a"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !fm.Aktiv() || fm.Schluessel() != attrappenSchluessel {
		t.Fatalf("Aktiv=%v Schlüssel getrimmt? %v", fm.Aktiv(), fm.Schluessel() == attrappenSchluessel)
	}
	if got := fm.Modelle(); !reflect.DeepEqual(got, []string{"modell-a", "modell-b"}) {
		t.Errorf("Modelle = %v, erwartet ohne Doppel", got)
	}
}

// Jeder Startabbruch nennt Variable und Pfad, NIE den Dateiinhalt.
func TestFirmenmodusAusDatei_StartabbruchOhneInhalt(t *testing.T) {
	dir := t.TempDir()
	faelle := []struct {
		name    string
		pfad    string
		modelle []string
		grund   string
	}{
		{"fehlt", filepath.Join(dir, "gibt-es-nicht"), []string{"m"}, "nicht lesbar"},
		{"verzeichnis", dir, []string{"m"}, "Verzeichnis"},
		{"leer", schreibeSchluesseldatei(t, " \n\t\n"), []string{"m"}, "leer"},
		{"mehrzeilig", schreibeSchluesseldatei(t, attrappenSchluessel+"\nzweite-zeile-geheim"), []string{"m"}, "Leerraum"},
		{"modelle-leer", schreibeSchluesseldatei(t, attrappenSchluessel), nil, "ABOARD_LLM_OPENAI_MODELLE"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			fm, err := llm.FirmenmodusAusDatei(f.pfad, f.modelle)
			if err == nil || fm != nil {
				t.Fatalf("erwartet Fehler, bekam fm=%v err=%v", fm, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, llm.ServerschluesselEnvVar) || !strings.Contains(msg, f.grund) {
				t.Errorf("Meldung %q nennt nicht %s und %q", msg, llm.ServerschluesselEnvVar, f.grund)
			}
			for _, geheim := range []string{attrappenSchluessel, "zweite-zeile-geheim"} {
				if strings.Contains(msg, geheim) {
					t.Errorf("Meldung enthält den Dateiinhalt: %q", msg)
				}
			}
		})
	}
}

func TestFirmenmodusAusDatei_ZuGrosseDatei(t *testing.T) {
	pfad := schreibeSchluesseldatei(t, strings.Repeat("x", 9*1024))
	if _, err := llm.FirmenmodusAusDatei(pfad, []string{"m"}); err == nil || !strings.Contains(err.Error(), "zu gross") {
		t.Fatalf("err = %v, erwartet „zu gross“", err)
	}
}

func TestFirmenmodus_NilIstInaktivUndErlaubtAlles(t *testing.T) {
	var fm *llm.Firmenmodus
	if fm.Aktiv() || fm.Schluessel() != "" || fm.Modelle() != nil || !fm.ModellErlaubt("irgendwas") {
		t.Fatal("nil-Firmenmodus muss inaktiv sein und jedes Modell erlauben (heutiges Verhalten)")
	}
}

func TestFirmenmodus_ModellAllowlist(t *testing.T) {
	fm, err := llm.NeuerFirmenmodus(attrappenSchluessel, []string{"qwen3-30b"})
	if err != nil {
		t.Fatal(err)
	}
	if !fm.ModellErlaubt("qwen3-30b") {
		t.Error("freigegebenes Modell abgelehnt")
	}
	for _, m := range []string{"gpt-4o", "", "QWEN3-30B", "qwen3-30b "} {
		if fm.ModellErlaubt(m) {
			t.Errorf("Modell %q erlaubt, steht aber nicht in der Liste", m)
		}
	}
}

// Ein versehentliches %v, %+v, %#v, %s oder json.Marshal darf den Schlüssel
// nicht ausgeben.
func TestFirmenmodus_SchwaerztSich(t *testing.T) {
	fm, _ := llm.NeuerFirmenmodus(attrappenSchluessel, []string{"m"})
	ausgaben := []string{
		fmt.Sprintf("%v", fm), fmt.Sprintf("%+v", fm), fmt.Sprintf("%#v", fm), fmt.Sprintf("%s", fm),
		fmt.Sprintf("%v", *fm), fmt.Sprintf("%+v", *fm), fmt.Sprintf("%#v", *fm), fmt.Sprintf("%x", *fm),
	}
	roh, _ := json.Marshal(fm)
	ausgaben = append(ausgaben, string(roh))
	for _, a := range ausgaben {
		if strings.Contains(a, attrappenSchluessel) || strings.Contains(a, fmt.Sprintf("%x", attrappenSchluessel)) {
			t.Errorf("Ausgabe enthält den Schlüssel: %q", a)
		}
	}
}

func TestParsePIIVorgabe(t *testing.T) {
	faelle := []struct {
		roh   string
		firma bool
		basis string
		want  llm.PIIVorgabe
	}{
		{"", false, "", llm.PIINutzer},
		{"", false, "http://llm.internal:8000/v1", llm.PIINutzer},
		{"", true, "http://llm.internal:8000/v1", llm.PIIAus},
		{"", true, "", llm.PIIPflicht},
		{"aus", true, "", llm.PIIAus},
		{" Pflicht ", true, "http://x/v1", llm.PIIPflicht},
		{"nutzer", true, "", llm.PIINutzer},
		{"pflicht", false, "", llm.PIIPflicht},
	}
	for _, f := range faelle {
		got, err := llm.ParsePIIVorgabe(f.roh, f.firma, f.basis)
		if err != nil || got != f.want {
			t.Errorf("ParsePIIVorgabe(%q, %v, %q) = %q, %v — erwartet %q", f.roh, f.firma, f.basis, got, err, f.want)
		}
	}
	for _, ungueltig := range []string{"an", "ja", "always", "0"} {
		if _, err := llm.ParsePIIVorgabe(ungueltig, false, ""); err == nil || !strings.Contains(err.Error(), llm.PIIEnvVar) {
			t.Errorf("ParsePIIVorgabe(%q): err = %v, erwartet Fehler mit %s", ungueltig, err, llm.PIIEnvVar)
		}
	}
}

func TestPIIVorgabe_Wirksam(t *testing.T) {
	for _, client := range []bool{false, true} {
		if !llm.PIIPflicht.Wirksam(client) {
			t.Errorf("pflicht, Client %v: nicht wirksam", client)
		}
		if llm.PIIAus.Wirksam(client) {
			t.Errorf("aus, Client %v: wirksam", client)
		}
		if llm.PIINutzer.Wirksam(client) != client || llm.PIIVorgabe("").Wirksam(client) != client {
			t.Errorf("nutzer/leer, Client %v: Client-Meldung nicht maßgeblich", client)
		}
	}
}

func TestRegisterFirmenmodus_NurOpenAI(t *testing.T) {
	vorher := llm.SetzeOpenAIKompatibel(false)
	t.Cleanup(func() { llm.SetzeOpenAIKompatibel(vorher) })

	r := llm.NewRegistry()
	llm.RegisterFirmenmodus(r, "test/1", "http://llm.internal:8000/v1")
	if got := r.List(); !reflect.DeepEqual(got, []string{"openai"}) {
		t.Fatalf("Firmenmodus registriert %v, erwartet nur [openai]", got)
	}
	p, _ := r.Get("openai")
	if op, ok := p.(*llm.OpenAIProvider); !ok || op.BaseURL != "http://llm.internal:8000/v1" {
		t.Errorf("openai-Provider ohne Basis-URL: %#v", p)
	}
	if !llm.OpenAIKompatibel() {
		t.Error("OpenAIKompatibel nicht gesetzt")
	}
	// Gegenprobe: ohne Firmenmodus bleiben es vier.
	r2 := llm.NewRegistry()
	llm.RegisterDefaults(r2, "test/1", "")
	if got := r2.List(); len(got) != 4 {
		t.Errorf("RegisterDefaults registriert %v, erwartet vier Anbieter", got)
	}
}

// bodyStub zeichnet den Request-Body auf und antwortet passend für Send oder
// SendStream.
func bodyStub(t *testing.T) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var letzter map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		letzter = body
		mu.Unlock()
		if s, _ := body["stream"].(bool); s {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]any { mu.Lock(); defer mu.Unlock(); return letzter }
}

// Das `user`-Feld: gesetzt → im Body (Send und Stream); leer → das Feld
// fehlt ganz (Body bit-identisch zu vorher); Mistral bekommt es nie.
func TestOpenAI_UserFeld(t *testing.T) {
	srv, letzter := bodyStub(t)
	ctx := context.Background()
	oa := &llm.OpenAIProvider{Client: srv.Client(), UA: "test/1", SendEndpoint: srv.URL}
	mi := &llm.MistralProvider{Client: srv.Client(), UA: "test/1", SendEndpoint: srv.URL}
	mitUser := llm.SendRequest{Model: "m", UserPrompt: "hallo", User: "person@firma.de"}
	ohneUser := llm.SendRequest{Model: "m", UserPrompt: "hallo"}
	nichts := func(llm.StreamChunk) error { return nil }

	pruefe := func(name string, lauf func() error, wantUser any) {
		t.Helper()
		if err := lauf(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, vorhanden := letzter()["user"]
		if wantUser == nil {
			if vorhanden {
				t.Errorf("%s: user-Feld %v im Body, erwartet keins", name, got)
			}
			return
		}
		if got != wantUser {
			t.Errorf("%s: user = %v, erwartet %v", name, got, wantUser)
		}
	}
	pruefe("openai Send mit", func() error { _, err := oa.Send(ctx, "k", mitUser); return err }, "person@firma.de")
	pruefe("openai Stream mit", func() error { _, err := oa.SendStream(ctx, "k", mitUser, nichts); return err }, "person@firma.de")
	pruefe("openai Send ohne", func() error { _, err := oa.Send(ctx, "k", ohneUser); return err }, nil)
	pruefe("openai Stream ohne", func() error { _, err := oa.SendStream(ctx, "k", ohneUser, nichts); return err }, nil)
	pruefe("mistral Send mit", func() error { _, err := mi.Send(ctx, "k", mitUser); return err }, nil)
	pruefe("mistral Stream mit", func() error { _, err := mi.SendStream(ctx, "k", mitUser, nichts); return err }, nil)
}
