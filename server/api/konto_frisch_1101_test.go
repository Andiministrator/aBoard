package api

// Karte #1101 (F-2920, ADR-0149 Nachtrag 2026-11-09): ABOARD_OIDC_REAUTH_FALLBACK.
// Fehlt auth_time und ist der Schalter an, genügt ein in diesem Vorgang frisch
// ausgestelltes ID-Token (iat). Liefert der Anbieter auth_time, bleibt die
// strenge Prüfung; ohne Schalter bleibt es bei fail closed.

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestFrisch1101_Schalter(t *testing.T) {
	for w, soll := range map[string]bool{"": false, "0": false, "off": false, "1": true, "on": true, "ja": true} {
		an, err := KontoReauthFallback(func(string) string { return w })
		if err != nil || an != soll {
			t.Errorf("%q: an=%v err=%v", w, an, err)
		}
	}
	for _, w := range []string{"aus", "nein", "jein"} {
		if _, err := KontoReauthFallback(func(string) string { return w }); err == nil || !strings.Contains(err.Error(), KontoReauthFallbackEnv) {
			t.Errorf("%q: Tippfehler muss den Start anhalten, err=%v", w, err)
		}
	}
}

// Der Schalter ist kein unbekannter ABOARD_OIDC_*-Name.
func TestFrisch1101_SchalterKeinFremderName(t *testing.T) {
	env := map[string]string{
		"ABOARD_OIDC_ISSUER": "https://accounts.google.com", "ABOARD_OIDC_CLIENT_ID": "c", "ABOARD_OIDC_CLIENT_SECRET": "s",
		KontoReauthFallbackEnv: "1",
	}
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	_, _, fehler := AnbieterMitAuswahl(func(k string) string { return env[k] }, environ)
	for _, f := range fehler {
		if strings.Contains(f, KontoReauthFallbackEnv) {
			t.Fatalf("Schalter als unbekannt gemeldet: %v", fehler)
		}
	}
}

func TestFrisch1101_OhneAuthTime(t *testing.T) {
	k, c := pmMitBindung(t)
	k.ke.Audit = storage.NewConfigRepo(k.db, nil, uuid.NewString)
	// Der Anmelde-Zustand läuft auf der echten Uhr; die Konto-Uhr hier auch,
	// damit „nicht vor Beginn des Vorgangs“ prüfbar ist.
	k.uhr = time.Now().UTC().Truncate(time.Second)

	// Schalter aus: fail closed, eigener Grund.
	ac, q := k.frischStart(t, "google", frischLoesen, "google", c)
	rec := k.rueckruf(t, k.google, "google", q, "o-"+uuid.NewString(), map[string]any{"sub": "sub-opfer", "iat": k.uhr.Unix()}, ac, c)
	if e := ergebnis(t, rec); e != KontoErgebnisNichtUnterstuetzt || frischCookie(rec) != nil {
		t.Fatalf("Schalter aus: %q, Nachweis %v", e, frischCookie(rec) != nil)
	}

	k.ke.ReauthFallback = true
	for _, f := range []struct {
		name   string
		claims map[string]any
		soll   string
	}{
		{"iat frisch", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Unix()}, KontoErgebnisBestaetigt},
		{"iat vor Beginn des Vorgangs", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Add(-3 * time.Minute).Unix()}, KontoErgebnisVorgangAlt},
		{"iat in der Zukunft", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Add(authTimeVorlauf + time.Minute).Unix()}, KontoErgebnisVorgangAlt},
		{"fremdes sub", map[string]any{"sub": "sub-angreifer", "iat": k.uhr.Unix()}, KontoErgebnisNichtBestaetigt},
		// auth_time vorhanden: strenge Prüfung, der Schalter hilft nicht.
		// Kritik 10.11. (F-B): iat fehlt ganz → fail closed; auth_time 0 ist
		// vorhanden, aber ungültig → nie der Ersatzweg.
		{"iat fehlt", map[string]any{"sub": "sub-opfer", "iat": nil}, KontoErgebnisVorgangAlt},
		{"auth_time 0 trotz frischem iat", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Unix(), "auth_time": 0}, KontoErgebnisNichtBestaetigt},
		{"auth_time unlesbar trotz frischem iat", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Unix(), "auth_time": "gestern"}, KontoErgebnisNichtBestaetigt},
		{"auth_time zu alt trotz frischem iat", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Unix(), "auth_time": k.uhr.Add(-authTimeHoechstalter - time.Minute).Unix()}, KontoErgebnisNichtBestaetigt},
	} {
		t.Run(f.name, func(t *testing.T) {
			// Der Vorgang beginnt erst 2 Minuten nach „jetzt minus 3“ — so
			// liegt ein 3 Minuten altes iat vor seinem Beginn.
			ac, q := k.frischStart(t, "google", frischLoesen, "google", c)
			rec := k.rueckruf(t, k.google, "google", q, "f-"+uuid.NewString(), f.claims, ac, c)
			if e := ergebnis(t, rec); e != f.soll {
				t.Fatalf("Ergebnis %q, erwartet %q", e, f.soll)
			}
			if (frischCookie(rec) != nil) != (f.soll == KontoErgebnisBestaetigt) {
				t.Fatalf("Nachweis-Cookie passt nicht zum Ergebnis %q", f.soll)
			}
		})
	}
	a := k.auditAktionen(t)
	if !strings.Contains(a[AuditActionKontoFrischOk], "ergebnis=ok_ohne_auth_time") {
		t.Errorf("Audit nennt den Ersatzweg nicht: %v", a)
	}
	for _, g := range []string{"ergebnis=auth_time_fehlt", "ergebnis=iat_alt", "ergebnis=sub", "ergebnis=auth_time_alt", "ergebnis=auth_time_ungueltig"} {
		if !strings.Contains(a[AuditActionKontoFrischFehler], g) {
			t.Errorf("Audit ohne %s: %v", g, a[AuditActionKontoFrischFehler])
		}
	}
}
