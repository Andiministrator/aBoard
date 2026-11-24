package api_test

// me_anmeldung_1053_test.go — Karte #1053 (ADR-0148 §5): GET /me meldet die
// Betriebsart. Nur im OIDC-Betrieb steht `anmeldung: "oidc"`; daran erkennt
// die Oberfläche, dass ein 401 „Sitzung abgelaufen“ heißt und „Abmelden“ etwas
// tut. Im Forward-Auth-Betrieb und ohne Anmeldung fehlt das Feld.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func meLesen1053(t *testing.T, kopf map[string]string) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	api.RegisterMeHandler(mux, nil)
	var h http.Handler = api.WithForwardAuthIfEnabled(mux)
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	for k, v := range kopf {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me: %d", rec.Code)
	}
	var me map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	return me
}

func TestMeAnmeldung1053_OIDCOhneIdentitaet(t *testing.T) {
	t.Setenv("ABOARD_ANMELDUNG", "oidc")
	if me := meLesen1053(t, nil); me["anmeldung"] != "oidc" {
		t.Fatalf("anmeldung = %v, erwartet \"oidc\"", me["anmeldung"])
	}
}

func TestMeAnmeldung1053_OIDCMitIdentitaet(t *testing.T) {
	t.Setenv("ABOARD_ANMELDUNG", "oidc")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	me := meLesen1053(t, map[string]string{"Remote-User": "andi", "Remote-Email": "a@example.com"})
	if me["email"] != "a@example.com" {
		t.Fatalf("Zweig mit Identität nicht erreicht: %v", me)
	}
	if me["anmeldung"] != "oidc" {
		t.Fatalf("anmeldung = %v, erwartet \"oidc\"", me["anmeldung"])
	}
}

func TestMeAnmeldung1053_OhneOIDCFehltDasFeld(t *testing.T) {
	t.Setenv("ABOARD_ANMELDUNG", "")
	if me := meLesen1053(t, nil); me["anmeldung"] != nil {
		t.Fatalf("anmeldung = %v, soll fehlen", me["anmeldung"])
	}
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	me := meLesen1053(t, map[string]string{"Remote-User": "andi", "Remote-Email": "a@example.com"})
	if me["email"] != "a@example.com" {
		t.Fatalf("Zweig mit Identität nicht erreicht: %v", me)
	}
	if _, da := me["anmeldung"]; da {
		t.Fatalf("Forward-Auth-Betrieb: Feld anmeldung soll fehlen, ist %v", me["anmeldung"])
	}
}
