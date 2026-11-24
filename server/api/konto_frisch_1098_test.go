package api

// Karte #1098 (F-2918, ADR-0149 Nachtrag 2026-11-04): frische Anmeldung beim
// schon gebundenen Anbieter als Bestätigung für Personen ohne Passwort.
// Aufbau wie konto_einstellungen_1083_test.go (echter Speicher, Sitzungsweiche,
// CSRF-Schutz, zwei Test-Anbieter). Testdaten nur example.org.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/Andiministrator/aBoard/server/storage"
)

// frischStart beginnt die frische Anmeldung und liefert Anmelde-Cookie und
// Parameter der Adresse beim Anbieter.
func (k *kontoTest) frischStart(t *testing.T, anbieter, aktion, ziel string, sitzung *http.Cookie) (*http.Cookie, url.Values) {
	t.Helper()
	rec := k.post("/frisch/start", map[string]string{"anbieter": anbieter, "aktion": aktion, "ziel": ziel}, sitzung)
	if rec.Code != http.StatusOK {
		t.Fatalf("frisch/start: %d %s", rec.Code, rec.Body.String())
	}
	adr, _ := url.Parse(antwort(t, rec)["ziel"].(string))
	// Die Test-Anbieter nennen prompt_values_supported nicht (wie Google):
	// select_account; max_age=0 und claims mit auth_time immer.
	if adr.Query().Get("prompt") != "select_account" || adr.Query().Get("max_age") != "0" ||
		adr.Query().Get("claims") != `{"id_token":{"auth_time":{"essential":true}}}` {
		t.Errorf("prompt=%q max_age=%q claims=%q", adr.Query().Get("prompt"), adr.Query().Get("max_age"), adr.Query().Get("claims"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == AnmeldungCookie && c.Value != "" {
			return c, adr.Query()
		}
	}
	t.Fatal("kein Anmelde-Cookie")
	return nil, nil
}

// frischCookie liefert das Nachweis-Cookie einer Antwort (nil = keins gesetzt).
func frischCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == FrischCookie && c.Value != "" && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

// bestaetige führt die frische Anmeldung ganz durch (sub-opfer, auth_time
// jetzt) und liefert das Nachweis-Cookie.
func (k *kontoTest) bestaetige(t *testing.T, anbieter, aktion, ziel string, sitzung *http.Cookie) *http.Cookie {
	t.Helper()
	anb := map[string]*testAnbieter{"google": k.google, "authelia": k.authelia}[anbieter]
	ac, q := k.frischStart(t, anbieter, aktion, ziel, sitzung)
	rec := k.rueckruf(t, anb, anbieter, q, "f-"+uuid.NewString(), map[string]any{"sub": "sub-opfer", "auth_time": k.uhr.Unix()}, ac, sitzung)
	if e := ergebnis(t, rec); e != KontoErgebnisBestaetigt {
		t.Fatalf("frische Anmeldung: %q", e)
	}
	n := frischCookie(rec)
	if n == nil {
		t.Fatal("kein Nachweis-Cookie")
	}
	if !n.HttpOnly || !n.Secure || n.SameSite != http.SameSiteStrictMode || n.Path != "/" {
		t.Errorf("Nachweis-Cookie-Attribute: %+v", n)
	}
	return n
}

func (k *kontoTest) bindungAn(email, anbieter string, anb *testAnbieter, sub string) {
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, ?, ?, ?, 1)`, anb.srv.URL, sub, anbieter, "u-"+email)
}

func (k *kontoTest) auditAktionen(t *testing.T) map[string]string {
	t.Helper()
	rows, err := k.db.Query(`SELECT key, action, COALESCE(new_value_redacted, '') FROM server_config_audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, action, wert string
		if err := rows.Scan(&key, &action, &wert); err != nil {
			t.Fatal(err)
		}
		// Die Test-IDs heißen u-<adresse>; geprüft wird der Wert und dass der
		// Schlüssel die users.id ist.
		if !strings.HasPrefix(key, "personen/u-") || strings.Contains(wert, "@") || strings.Contains(wert, "sub-") {
			t.Errorf("Audit %s verrät Adresse oder sub: key=%q wert=%q", action, key, wert)
		}
		out[action] += wert + "|"
	}
	return out
}

// Erfolgsfall Lösen: Nachweis für loesen:authelia → Lösen ohne Passwort,
// Sitzungen neu, Nachweis verbraucht.
func TestFrisch1098_LoesenMitNachweis(t *testing.T) {
	k, c := pmMitBindung(t)
	k.ke.Audit = storage.NewConfigRepo(k.db, nil, uuid.NewString)
	k.bindungAn("pm@example.org", "authelia", k.authelia, "sub-z")
	andere := k.sitzung(t, "pm@example.org")
	n := k.bestaetige(t, "google", frischLoesen, "authelia", c)

	var s kontoStand
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c, n).Body.Bytes(), &s)
	if s.Frisch == nil || s.Frisch.Aktion != frischLoesen || s.Frisch.Ziel != "authelia" {
		t.Fatalf("Stand nennt den Nachweis nicht: %+v", s.Frisch)
	}
	if strings.Contains(k.anfrage(http.MethodGet, KontoPfad, nil, c, n).Body.String(), n.Value) {
		t.Fatal("Stand verrät den Nachweis")
	}
	rec := k.post("/wege/loesen", map[string]string{"anbieter": "authelia"}, c, n)
	if rec.Code != http.StatusOK {
		t.Fatalf("lösen mit Nachweis: %d %s", rec.Code, rec.Body.String())
	}
	neu := sitzungsCookie(rec)
	if neu == nil || k.sitzungGueltig(c) || k.sitzungGueltig(andere) || !k.sitzungGueltig(neu) {
		t.Fatal("Sitzungen nach Lösen falsch")
	}
	if k.bindungsZahl(t, "pm@example.org") != 1 || k.opferBindung(t) != "sub-opfer" {
		t.Fatal("falsche Bindung gelöst")
	}
	// Das Cookie ist gelöscht, und auch mit dem alten Wert gilt es nicht noch einmal.
	geloescht := false
	for _, ck := range rec.Result().Cookies() {
		geloescht = geloescht || (ck.Name == FrischCookie && ck.MaxAge < 0)
	}
	if !geloescht {
		t.Error("Nachweis-Cookie nicht gelöscht")
	}
	k.bindungAn("pm@example.org", "authelia", k.authelia, "sub-z2")
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "authelia"}, neu, n); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != kennungFrischNoetig {
		t.Fatalf("Nachweis doppelt: %d %s", rec.Code, rec.Body.String())
	}
	a := k.auditAktionen(t)
	if !strings.Contains(a[AuditActionKontoFrischOk], "aktion=loesen:authelia;ergebnis=ok") || a[AuditActionKontoWegGeloest] == "" {
		t.Errorf("Audit: %v", a)
	}
}

// Lösen des letzten Wegs bleibt verboten, auch mit Nachweis; der Nachweis
// bleibt dabei unverbraucht (Vorbedingung vor der Bestätigung).
func TestFrisch1098_LetzterWegBleibt(t *testing.T) {
	k, c := pmMitBindung(t)
	n := k.bestaetige(t, "google", frischLoesen, "google", c)
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "google"}, c, n); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "letzter_weg" {
		t.Fatalf("letzter Weg: %d %s", rec.Code, rec.Body.String())
	}
	if k.bindungsZahl(t, "pm@example.org") != 1 || !k.nachweisNochDa(t, c, n, frischAktion(frischLoesen, "google")) {
		t.Fatal("Bindung gelöst oder Nachweis verbraucht")
	}
}

func (k *kontoTest) nachweisNochDa(t *testing.T, sitzung, n *http.Cookie, aktion string) bool {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://board.example/", nil)
	r.AddCookie(n)
	return k.ke.nachweisFuer(r, &kontoAnfrage{Email: "pm@example.org", Sitzung: sitzung.Value}, aktion)
}

// Erfolgsfall des Angriffs A — aber durch die Person selbst: Mit Nachweis
// (eigene frische Anmeldung mit demselben sub) darf sie ihr Google-Konto
// ersetzen; Sitzungen werden neu ausgestellt, das alte Konto kommt nicht mehr herein.
func TestFrisch1098_ErsetzenMitNachweis(t *testing.T) {
	k, c := pmMitBindung(t)
	andere := k.sitzung(t, "pm@example.org")
	n := k.bestaetige(t, "google", frischVerknuepfen, "google", c)
	ac, q := k.linkStart(t, "google", map[string]string{}, c, n)
	rec := k.rueckruf(t, k.google, "google", q, "e1", map[string]any{"sub": "sub-neu"}, ac, c)
	if e := ergebnis(t, rec); e != KontoErgebnisErsetzt {
		t.Fatalf("ersetzen: %q", e)
	}
	neu := sitzungsCookie(rec)
	if neu == nil || k.sitzungGueltig(c) || k.sitzungGueltig(andere) || !k.sitzungGueltig(neu) {
		t.Fatal("Sitzungen nach Ersetzen falsch")
	}
	if k.opferBindung(t) != "sub-neu" {
		t.Fatal("Bindung nicht ersetzt")
	}
	// Ohne neuen Nachweis geht kein zweites Ersetzen.
	if rec := k.post("/wege/verknuepfen", map[string]string{"anbieter": "google"}, neu, n); rec.Code != http.StatusConflict {
		t.Fatalf("zweites Ersetzen ohne Nachweis: %d", rec.Code)
	}
}

// Verknüpfen eines weiteren Anbieters mit Nachweis; ohne Passwort zeigt der
// Stand danach zwei Wege, das Lösen eines davon ist möglich.
func TestFrisch1098_VerknuepfenMitNachweis(t *testing.T) {
	k, c := pmMitBindung(t)
	n := k.bestaetige(t, "google", frischVerknuepfen, "authelia", c)
	// Der Nachweis gilt nicht für einen anderen Anbieter.
	if rec := k.post("/wege/verknuepfen", map[string]string{"anbieter": "google"}, c, n); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != kennungFrischNoetig {
		t.Fatalf("anderer Anbieter: %d %s", rec.Code, rec.Body.String())
	}
	ac, q := k.linkStart(t, "authelia", map[string]string{}, c, n)
	if e := ergebnis(t, k.rueckruf(t, k.authelia, "authelia", q, "v1", map[string]any{"sub": "sub-a"}, ac, c)); e != KontoErgebnisVerknuepft {
		t.Fatalf("verknüpfen: %q", e)
	}
	if k.bindungsZahl(t, "pm@example.org") != 2 {
		t.Fatal("Bindung fehlt")
	}
	var s kontoStand
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c).Body.Bytes(), &s)
	if len(s.FrischAnbieter) != 2 || !s.Wege[0].Loesbar || !s.Wege[1].Loesbar {
		t.Errorf("Stand nach Verknüpfen: %+v", s)
	}
}

// Negativfälle des Rückrufs: falsches sub, fehlendes, zu altes und zukünftiges
// auth_time — nie ein Nachweis; falsches sub zählt in die Bremse.
func TestFrisch1098_RueckrufNegativ(t *testing.T) {
	k, c := pmMitBindung(t)
	k.ke.Audit = storage.NewConfigRepo(k.db, nil, uuid.NewString)
	k.person(t, "ben@example.org", false)
	k.bindungAn("ben@example.org", "authelia", k.authelia, "sub-ben")
	for _, f := range []struct {
		name   string
		claims map[string]any
	}{
		{"fremdes sub", map[string]any{"sub": "sub-angreifer", "auth_time": k.uhr.Unix()}},
		{"sub einer anderen Person", map[string]any{"sub": "sub-ben", "auth_time": k.uhr.Unix()}},
		{"auth_time fehlt", map[string]any{"sub": "sub-opfer"}},
		{"auth_time zu alt", map[string]any{"sub": "sub-opfer", "auth_time": k.uhr.Add(-authTimeHoechstalter - time.Minute).Unix()}},
		{"auth_time in der Zukunft", map[string]any{"sub": "sub-opfer", "auth_time": k.uhr.Add(authTimeVorlauf + time.Minute).Unix()}},
		{"auth_time kein Wert", map[string]any{"sub": "sub-opfer", "auth_time": "gestern"}},
	} {
		t.Run(f.name, func(t *testing.T) {
			ac, q := k.frischStart(t, "google", frischLoesen, "google", c)
			rec := k.rueckruf(t, k.google, "google", q, "n-"+uuid.NewString(), f.claims, ac, c)
			// #1101: Fehlt auth_time (Schalter aus), meldet die Rückkehr einen
			// eigenen Grund — Ab- und Anmelden hilft dann nie.
			soll := KontoErgebnisNichtBestaetigt
			if f.name == "auth_time fehlt" {
				soll = KontoErgebnisNichtUnterstuetzt
			}
			if e := ergebnis(t, rec); e != soll {
				t.Fatalf("Ergebnis %q, erwartet %q", e, soll)
			}
			if frischCookie(rec) != nil {
				t.Fatal("Nachweis trotz Fehlschlag")
			}
		})
	}
	// Auch ein sub einer anderen Person bei einem anderen Anbieter (authelia):
	// pm ist dort nicht gebunden → frisch/start lehnt ab.
	if rec := k.post("/frisch/start", map[string]string{"anbieter": "authelia", "aktion": frischLoesen, "ziel": "google"}, c); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "nicht_gebunden" {
		t.Fatalf("nicht gebundener Anbieter: %d %s", rec.Code, rec.Body.String())
	}
	a := k.auditAktionen(t)
	if n := strings.Count(a[AuditActionKontoFrischFehler], "|"); n != 6 || !strings.Contains(a[AuditActionKontoFrischFehler], "ergebnis=auth_time_fehlt") {
		t.Errorf("Audit der Fehlschläge (%d): %v", n, a)
	}
	// Zwei falsche sub haben gezählt; drei weitere sperren.
	for i := 0; i < KontoMaxFehler-2; i++ {
		ac, q := k.frischStart(t, "google", frischLoesen, "google", c)
		k.rueckruf(t, k.google, "google", q, "b-"+uuid.NewString(), map[string]any{"sub": "sub-angreifer", "auth_time": k.uhr.Unix()}, ac, c)
	}
	if rec := k.post("/frisch/start", map[string]string{"anbieter": "google", "aktion": frischLoesen, "ziel": "google"}, c); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("nach %d falschen sub: %d", KontoMaxFehler, rec.Code)
	}
}

// Sitzungsbindung: Der Rückruf in einer fremden, einer beendeten oder ganz
// ohne Sitzung legt keinen Nachweis an; ein Nachweis gilt nur in der Sitzung,
// die ihn erworben hat.
func TestFrisch1098_Sitzungsbindung(t *testing.T) {
	k, c := pmMitBindung(t)
	k.person(t, "ben@example.org", false)
	ben := k.sitzung(t, "ben@example.org")
	ok := map[string]any{"sub": "sub-opfer", "auth_time": k.uhr.Unix()}

	ac, q := k.frischStart(t, "google", frischLoesen, "google", c)
	if rec := k.rueckruf(t, k.google, "google", q, "s1", ok, ac, ben); ergebnis(t, rec) != KontoErgebnisFehler || frischCookie(rec) != nil {
		t.Fatal("fremde Sitzung bekam einen Nachweis")
	}
	ac, q = k.frischStart(t, "google", frischLoesen, "google", c)
	if rec := k.rueckruf(t, k.google, "google", q, "s2", ok, ac); ergebnis(t, rec) != KontoErgebnisAbgelaufen || frischCookie(rec) != nil {
		t.Fatal("ohne Sitzung ein Nachweis")
	}
	zweite := k.sitzung(t, "pm@example.org")
	ac, q = k.frischStart(t, "google", frischLoesen, "google", zweite)
	_ = k.sitzungen.Beenden(context.Background(), zweite.Value)
	if rec := k.rueckruf(t, k.google, "google", q, "s3", ok, ac, zweite); ergebnis(t, rec) != KontoErgebnisAbgelaufen || frischCookie(rec) != nil {
		t.Fatal("beendete Sitzung bekam einen Nachweis")
	}
	// Nachweis aus Sitzung c in einer anderen Sitzung derselben Person: gilt nicht.
	k.bindungAn("pm@example.org", "authelia", k.authelia, "sub-z")
	n := k.bestaetige(t, "google", frischLoesen, "authelia", c)
	dritte := k.sitzung(t, "pm@example.org")
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "authelia"}, dritte, n); rec.Code != http.StatusConflict {
		t.Fatalf("Nachweis in anderer Sitzung: %d", rec.Code)
	}
	// Abgelaufen.
	k.uhr = k.uhr.Add(frischGueltig + time.Second)
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "authelia"}, c, n); rec.Code != http.StatusConflict {
		t.Fatalf("abgelaufener Nachweis: %d", rec.Code)
	}
	if k.bindungsZahl(t, "pm@example.org") != 2 {
		t.Fatal("Bindung trotz ungültigem Nachweis gelöst")
	}
}

// Ein Nachweis von Hand: verfälscht, für eine andere Person, ohne Kennung im
// Speicher (z. B. nach Neustart) — alles gilt nicht.
func TestFrisch1098_NachweisNichtFaelschbar(t *testing.T) {
	k, c := pmMitBindung(t)
	k.bindungAn("pm@example.org", "authelia", k.authelia, "sub-z")
	baue := func(n nachweis) *http.Cookie {
		roh, _ := json.Marshal(n)
		v, err := k.box.Seal(roh)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: FrischCookie, Value: v}
	}
	gut := nachweis{ID: "x1", Email: "pm@example.org", Sitzung: storage.SitzungsHash(c.Value), Aktion: "loesen:authelia", Bis: k.uhr.Add(time.Minute).Unix()}
	for name, ck := range map[string]*http.Cookie{
		"ohne Kennung im Speicher": baue(gut),
		"verfälscht":               {Name: FrischCookie, Value: baue(gut).Value + "x"},
		"Klartext":                 {Name: FrischCookie, Value: `{"i":"x1"}`},
	} {
		if rec := k.post("/wege/loesen", map[string]string{"anbieter": "authelia"}, c, ck); rec.Code != http.StatusConflict {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	// Mit Kennung im Speicher, aber für eine andere Person.
	k.ke.frisch = map[string]int64{"x1": gut.Bis}
	fremd := gut
	fremd.Email = "ben@example.org"
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "authelia"}, c, baue(fremd)); rec.Code != http.StatusConflict {
		t.Errorf("andere Person: %d", rec.Code)
	}
	if k.bindungsZahl(t, "pm@example.org") != 2 {
		t.Fatal("Bindung gelöst")
	}
}

// Cookie-Konflikt: Ein paralleler Anmeldevorgang überschreibt den Zustand der
// frischen Anmeldung; der Rückruf weist ab, es entsteht kein Nachweis.
func TestFrisch1098_ParallelerVorgang(t *testing.T) {
	k, c := pmMitBindung(t)
	_, q := k.frischStart(t, "google", frischLoesen, "google", c)
	loginC, _ := k.starte(t, "google")
	rec := k.rueckruf(t, k.google, "google", q, "p1", map[string]any{"sub": "sub-opfer", "auth_time": k.uhr.Unix()}, loginC, c)
	if rec.Code != http.StatusBadRequest || frischCookie(rec) != nil {
		t.Fatalf("überschriebener Vorgang: %d", rec.Code)
	}
	// Ein Zustand reauth ohne Aktion oder ohne Sitzungs-Hash wird gar nicht
	// angenommen (kein Code-Tausch).
	a := k.tabelle.Finde("google")
	for _, z := range []anmeldeZustand{
		{State: "s", Nonce: "n", Verifier: "v", Bis: time.Now().Add(time.Minute).Unix(), Kuerzel: "google", Zweck: ZweckReauth, Sitzung: storage.SitzungsHash(c.Value)},
		{State: "s", Nonce: "n", Verifier: "v", Bis: time.Now().Add(time.Minute).Unix(), Kuerzel: "google", Zweck: ZweckReauth, Aktion: "loesen:google"},
	} {
		roh, err := a.zustandKodieren(z)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungRueckrufPfad+"/google?code=x&state=s", nil)
		r.AddCookie(&http.Cookie{Name: AnmeldungCookie, Value: roh})
		r.AddCookie(c)
		rec := httptest.NewRecorder()
		k.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden || k.google.tokenAufrufe != 0 {
			t.Fatalf("unvollständiger reauth-Zustand: %d, Token-Aufrufe %d", rec.Code, k.google.tokenAufrufe)
		}
	}
}

// frisch/start: Vorbedingungen.
func TestFrisch1098_StartVorbedingungen(t *testing.T) {
	k, c := pmMitBindung(t)
	for name, f := range map[string]struct {
		body   map[string]string
		status int
		fehler string
	}{
		"unbekannte Aktion":         {map[string]string{"anbieter": "google", "aktion": "alles"}, 400, "aktion"},
		"Lösen ohne Bindung":        {map[string]string{"anbieter": "google", "aktion": "loesen", "ziel": "authelia"}, 404, "anbieter"},
		"Verknüpfen unbekannt":      {map[string]string{"anbieter": "google", "aktion": "verknuepfen", "ziel": "github"}, 404, "anbieter"},
		"Passwort bei Schalter aus": {map[string]string{"anbieter": "google", "aktion": "passwort"}, 404, "aktion"},
		"nicht gebunden":            {map[string]string{"anbieter": "authelia", "aktion": "verknuepfen", "ziel": "authelia"}, 409, "nicht_gebunden"},
	} {
		rec := k.post("/frisch/start", f.body, c)
		if rec.Code != f.status || antwort(t, rec)["fehler"] != f.fehler {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Wer ein Passwort hat, bestätigt mit dem Passwort.
	k.person(t, "anna@example.org", true)
	k.bindungAn("anna@example.org", "google", k.google, "sub-anna")
	anna := k.sitzung(t, "anna@example.org")
	if rec := k.post("/frisch/start", map[string]string{"anbieter": "google", "aktion": "loesen", "ziel": "google"}, anna); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "passwort_nutzen" {
		t.Errorf("mit Passwort: %d %s", rec.Code, rec.Body.String())
	}
	// Ein Nachweis ersetzt bei Personen mit Passwort nie das Passwort.
	n := &http.Cookie{Name: FrischCookie, Value: "egal"}
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "google"}, anna, n); rec.Code != http.StatusForbidden {
		t.Errorf("mit Passwort ohne Passwort-Eingabe: %d", rec.Code)
	}
}

// Ein Agent kommt nie an; CSRF schützt die neuen Wege.
func TestFrisch1098_AgentUndCSRF(t *testing.T) {
	k, c := pmMitBindung(t)
	for _, p := range []string{"/frisch/start", "/passwort/anlegen/start", "/passwort/anlegen/abschluss"} {
		r := httptest.NewRequest(http.MethodPost, "https://board.example"+KontoPfad+p, bytes.NewReader([]byte(`{"anbieter":"google","aktion":"loesen","ziel":"google"}`)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Origin", "https://boese.example")
		r.AddCookie(c)
		rec := httptest.NewRecorder()
		k.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("CSRF %s: %d", p, rec.Code)
		}
		// Agent im Kontext: abgewiesen, bevor irgendetwas geschieht.
		r = httptest.NewRequest(http.MethodPost, "https://board.example"+KontoPfad+p, bytes.NewReader([]byte(`{}`)))
		r = r.WithContext(context.WithValue(r.Context(), contextKeyAgent, &AgentPrincipal{}))
		r.AddCookie(c)
		rec = httptest.NewRecorder()
		k.mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "agent") {
			t.Errorf("Agent %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	if k.google.tokenAufrufe != 0 {
		t.Error("Anbieter angefragt")
	}
}

// B1 (Sicherheitsprüfung S110): prompt=login nur, wenn das Discovery-Dokument
// es unter prompt_values_supported nennt; sonst select_account. claims und
// max_age=0 immer.
func TestFrisch1098_PromptNachDiscovery(t *testing.T) {
	for _, f := range []struct {
		name  string
		werte []string
		want  string
	}{
		{"Feld fehlt (Google)", nil, "select_account"},
		{"ohne login", []string{"none", "consent", "select_account"}, "select_account"},
		{"mit login", []string{"none", "login", "consent"}, "login"},
	} {
		t.Run(f.name, func(t *testing.T) {
			k, c := pmMitBindung(t)
			k.google.promptWerte = f.werte
			rec := k.post("/frisch/start", map[string]string{"anbieter": "google", "aktion": frischLoesen, "ziel": "google"}, c)
			if rec.Code != http.StatusOK {
				t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
			}
			adr, _ := url.Parse(antwort(t, rec)["ziel"].(string))
			q := adr.Query()
			if q.Get("prompt") != f.want || q.Get("max_age") != "0" || q.Get("claims") != reauthClaims {
				t.Errorf("prompt=%q max_age=%q claims=%q", q.Get("prompt"), q.Get("max_age"), q.Get("claims"))
			}
			// Andere Zwecke tragen weder claims noch max_age.
			_, lq := k.starte(t, "google")
			if lq.Get("claims") != "" || lq.Get("max_age") != "" {
				t.Errorf("Login trägt claims/max_age: %v", lq)
			}
		})
	}
}

// B3 (Sicherheitsprüfung S110, übernommen aus TestSecS110_KontoZustandLoesen,
// Erwartung umgedreht): Eine Person der Vorab-Liste mit Konto in invited oder
// pending ändert keine Wege — weder Start noch Rückruf noch Lösen.
func TestFrisch1098_KontoZustand(t *testing.T) {
	for _, z := range []string{storage.KontoEingeladen, storage.KontoWartet} {
		t.Run(z, func(t *testing.T) {
			k, c := pmMitBindung(t)
			k.bindungAn("pm@example.org", "authelia", k.authelia, "sub-z")
			// Erst einen gültigen Nachweis holen, solange es noch kein Konto gibt.
			n := k.bestaetige(t, "google", frischLoesen, "authelia", c)
			ac, q := k.frischStart(t, "google", frischVerknuepfen, "google", c)
			k.exec(`INSERT INTO konten (id, email, zustand, weg, angelegt, geaendert) VALUES ('k-x', 'pm@example.org', ?, 'password', 1, 1)`, z)
			if rec := k.post("/frisch/start", map[string]string{"anbieter": "google", "aktion": frischLoesen, "ziel": "authelia"}, c); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "konto_zustand" {
				t.Errorf("frisch/start: %d %s", rec.Code, rec.Body.String())
			}
			for _, p := range []string{"/wege/loesen", "/wege/verknuepfen"} {
				if rec := k.post(p, map[string]string{"anbieter": "authelia"}, c, n); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "konto_zustand" {
					t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
				}
			}
			// Rückruf eines vorher begonnenen Vorgangs: kein Nachweis.
			rec := k.rueckruf(t, k.google, "google", q, "kz-"+z, map[string]any{"sub": "sub-opfer", "auth_time": k.uhr.Unix()}, ac, c)
			if e := ergebnis(t, rec); e != KontoErgebnisNichtBestaetigt || frischCookie(rec) != nil {
				t.Errorf("Rückruf: %q", e)
			}
			// Verknüpfen-Rückruf mit Frisch-Merkmal: bindet nichts.
			a := k.tabelle.Finde("authelia")
			verifier := oauth2.GenerateVerifier()
			zs, err := a.zustandKodieren(anmeldeZustand{State: "st", Nonce: "no", Verifier: verifier, Bis: time.Now().Add(time.Minute).Unix(),
				Kuerzel: "authelia", Zweck: ZweckLink, Sitzung: storage.SitzungsHash(c.Value), Frisch: true})
			if err != nil {
				t.Fatal(err)
			}
			lq := url.Values{"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "nonce": {"no"}, "state": {"st"}}
			if e := ergebnis(t, k.rueckruf(t, k.authelia, "authelia", lq, "kl-"+z, map[string]any{"sub": "sub-neu"}, &http.Cookie{Name: AnmeldungCookie, Value: zs}, c)); e != KontoErgebnisFehler {
				t.Errorf("Verknüpfen-Rückruf: %q", e)
			}
			if k.bindungsZahl(t, "pm@example.org") != 2 {
				t.Fatal("Bindungen verändert")
			}
		})
	}
}
