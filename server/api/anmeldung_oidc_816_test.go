package api

// Tests zum OIDC-Ablauf (ADR-0148 §1–§3, §6; Karte #816) gegen einen
// Test-Anbieter (httptest: Discovery, JWKS, Token-Endpunkt mit PKCE-Prüfung).

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/Andiministrator/aBoard/server/storage"
)

// testAnbieter ist ein minimaler OIDC-Anbieter.
type testAnbieter struct {
	t        *testing.T
	srv      *httptest.Server
	schluess *rsa.PrivateKey
	fremd    *rsa.PrivateKey // signiert, wenn falscheSignatur
	mu       sync.Mutex
	weg      bool
	// je Code: die code_challenge aus dem Start und die nonce
	challenge map[string]string
	// Anpassungen der Claims (nil = Standard); Wert nil löscht den Claim
	claims          map[string]any
	falscheSignatur bool
	tokenAufrufe    int
	// rueckruf: erwartete redirect_uri (Karte #1071: je Anbieter eigen).
	rueckruf string
	// promptWerte: prompt_values_supported im Discovery-Dokument (nil = Feld
	// fehlt, wie bei Google; Karte #1098).
	promptWerte []string
}

func neuerTestAnbieter(t *testing.T) *testAnbieter {
	t.Helper()
	a := &testAnbieter{t: t, challenge: map[string]string{}, rueckruf: "https://board.example/anmeldung/rueckruf"}
	var err error
	if a.schluess, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if a.fremd, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		weg := a.weg
		a.mu.Unlock()
		if weg {
			http.Error(w, "weg", http.StatusServiceUnavailable)
			return
		}
		d := map[string]any{
			"issuer":                                a.srv.URL,
			"authorization_endpoint":                a.srv.URL + "/auth",
			"token_endpoint":                        a.srv.URL + "/token",
			"jwks_uri":                              a.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		a.mu.Lock()
		if a.promptWerte != nil {
			d["prompt_values_supported"] = a.promptWerte
		}
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(d)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &a.schluess.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.tokenAufrufe++
		_ = r.ParseForm()
		code := r.Form.Get("code")
		ch, ok := a.challenge[code]
		if !ok {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		delete(a.challenge, code) // ein Code gilt einmal
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
			http.Error(w, `{"error":"invalid_grant","error_description":"pkce"}`, http.StatusBadRequest)
			return
		}
		if id, sec, ok := r.BasicAuth(); !(ok && id == "client-1" && sec == "geheim") &&
			!(r.Form.Get("client_id") == "client-1" && r.Form.Get("client_secret") == "geheim") {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		if r.Form.Get("redirect_uri") != a.rueckruf {
			http.Error(w, `{"error":"invalid_grant","error_description":"redirect_uri"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": a.idToken(code),
		})
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

// idToken baut das ID-Token; die nonce steht unter "nonce:"+code in claims.
func (a *testAnbieter) idToken(code string) string {
	c := map[string]any{
		"iss":            a.srv.URL,
		"aud":            "client-1",
		"sub":            "sub-anna",
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Unix(),
		"email":          "Anna@Example.com",
		"email_verified": true,
		"name":           "Anna A.",
		"nonce":          a.claims["nonce:"+code],
	}
	for k, v := range a.claims {
		if strings.HasPrefix(k, "nonce:") {
			continue
		}
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	key := a.schluess
	if a.falscheSignatur {
		key = a.fremd
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		a.t.Fatal(err)
	}
	roh, _ := json.Marshal(c)
	obj, err := s.Sign(roh)
	if err != nil {
		a.t.Fatal(err)
	}
	out, _ := obj.CompactSerialize()
	return out
}

// anmeldeTest bündelt Anbieter, Ablauf und echten Speicher.
type anmeldeTest struct {
	anb       *testAnbieter
	anm       *OIDCAnmeldung
	sitzungen *storage.SitzungRepo
	db        *sql.DB
}

func neuerAnmeldeTest(t *testing.T, zugelassen string) *anmeldeTest {
	t.Helper()
	anb := neuerTestAnbieter(t)
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	z, err := NeueZulassung(zugelassen, "")
	if err != nil {
		t.Fatal(err)
	}
	s := storage.NewSitzungRepo(db)
	anm := &OIDCAnmeldung{
		Issuer: anb.srv.URL, ClientID: "client-1", ClientSecret: "geheim",
		Basis: "https://board.example", Schluessel: AnmeldeSchluessel("k"),
		Client: anb.srv.Client(), Sitzungen: s, Bindungen: storage.NewAnbieterBindungRepo(db, NutzerAnlegen(storage.NewBootstrapRepo(db))),
		Zulassung: z, Wartezeit: time.Nanosecond,
	}
	return &anmeldeTest{anb: anb, anm: anm, sitzungen: s, db: db}
}

// starte ruft /anmeldung/start und liefert Cookie und Parameter der Weiterleitung.
func (x *anmeldeTest) starte(t *testing.T) (*http.Cookie, url.Values) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungStartPfad, nil)
	// Host und X-Forwarded-* dürfen die Rückruf-Adresse nicht bestimmen.
	r.Host = "boese.example"
	r.Header.Set("X-Forwarded-Host", "boese.example")
	r.Header.Set("X-Forwarded-Proto", "http")
	x.anm.Start(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("Start: %d %s", rec.Code, rec.Body.String())
	}
	ziel, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var c *http.Cookie
	for _, k := range rec.Result().Cookies() {
		if k.Name == AnmeldungCookie {
			c = k
		}
	}
	if c == nil {
		t.Fatal("Start setzt kein Anmelde-Cookie")
	}
	return c, ziel.Query()
}

// melde simuliert den Anbieter: Code ausgeben, dann Rückruf aufrufen.
func (x *anmeldeTest) melde(t *testing.T, c *http.Cookie, q url.Values, code string) *httptest.ResponseRecorder {
	t.Helper()
	x.anb.mu.Lock()
	x.anb.challenge[code] = q.Get("code_challenge")
	if x.anb.claims == nil {
		x.anb.claims = map[string]any{}
	}
	if _, gesetzt := x.anb.claims["nonce:"+code]; !gesetzt {
		x.anb.claims["nonce:"+code] = q.Get("nonce")
	}
	x.anb.mu.Unlock()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungRueckrufPfad+"?code="+url.QueryEscape(code)+
		"&state="+url.QueryEscape(q.Get("state"))+"&next=https://boese.example/", nil)
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	x.anm.Rueckruf(rec, r)
	return rec
}

func sitzungsCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, k := range rec.Result().Cookies() {
		if k.Name == SitzungsCookie && k.Value != "" {
			return k
		}
	}
	return nil
}

func TestAnmeldung816_ErfolgLegtSitzungAn(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	c, q := x.starte(t)
	// Start: PKCE S256, nonce, state, feste Rückruf-Adresse, Bereiche.
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("Start-Parameter unvollständig: %v", q)
	}
	if q.Get("redirect_uri") != "https://board.example/anmeldung/rueckruf" {
		t.Fatalf("redirect_uri %q — muss aus der Basis kommen, nie aus Host/X-Forwarded-*", q.Get("redirect_uri"))
	}
	if q.Get("scope") != "openid email profile" || q.Get("response_mode") == "form_post" {
		t.Fatalf("scope %q response_mode %q", q.Get("scope"), q.Get("response_mode"))
	}
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 600 {
		t.Fatalf("Anmelde-Cookie-Attribute falsch: %+v", c)
	}
	if strings.Contains(c.Value, q.Get("code_challenge")) {
		t.Fatal("Cookie trägt die Challenge statt des Verifiers")
	}

	rec := x.melde(t, c, q, "code-1")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("Rückruf: %d Location=%q %s — erwartet 302 auf / (kein next)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	sc := sitzungsCookie(rec)
	if sc == nil || !sc.Secure || !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode {
		t.Fatalf("Sitzungs-Cookie fehlt oder falsch: %+v", sc)
	}
	s, err := x.sitzungen.Pruefen(context.Background(), sc.Value)
	if err != nil || s.Email != "anna@example.com" || s.Name != "Anna A." {
		t.Fatalf("Sitzung: %+v %v", s, err)
	}
	// Das Anmelde-Cookie wird gelöscht.
	geloescht := false
	for _, k := range rec.Result().Cookies() {
		if k.Name == AnmeldungCookie && k.MaxAge < 0 {
			geloescht = true
		}
	}
	if !geloescht {
		t.Fatal("Anmelde-Cookie nach dem Rückruf nicht gelöscht")
	}
}

func TestAnmeldung816_TokenFehlerWerdenAbgewiesen(t *testing.T) {
	faelle := map[string]func(a *testAnbieter, code string){
		"falsche Signatur":         func(a *testAnbieter, _ string) { a.falscheSignatur = true },
		"falsche aud":              func(a *testAnbieter, _ string) { a.claims["aud"] = "fremder-client" },
		"falscher iss":             func(a *testAnbieter, _ string) { a.claims["iss"] = "https://boese.example" },
		"abgelaufen":               func(a *testAnbieter, _ string) { a.claims["exp"] = time.Now().Add(-time.Hour).Unix() },
		"falsche nonce":            func(a *testAnbieter, code string) { a.claims["nonce:"+code] = "fremde-nonce" },
		"ohne nonce":               func(a *testAnbieter, code string) { a.claims["nonce:"+code] = "" },
		"email nicht bestätigt":    func(a *testAnbieter, _ string) { a.claims["email_verified"] = false },
		"email_verified fehlt":     func(a *testAnbieter, _ string) { a.claims["email_verified"] = nil },
		"email_verified \"false\"": func(a *testAnbieter, _ string) { a.claims["email_verified"] = "false" },
		"ohne email":               func(a *testAnbieter, _ string) { a.claims["email"] = nil },
		"Domain-Schlüssel":         func(a *testAnbieter, _ string) { a.claims["email"] = "@example.com" },
	}
	for name, aendern := range faelle {
		x := neuerAnmeldeTest(t, "anna@example.com, @example.com")
		c, q := x.starte(t)
		x.anb.claims = map[string]any{}
		aendern(x.anb, "code-x")
		rec := x.melde(t, c, q, "code-x")
		if rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
			t.Errorf("%s: %d, Sitzung=%v — erwartet 403 ohne Sitzung", name, rec.Code, sitzungsCookie(rec) != nil)
		}
	}
}

func TestAnmeldung816_EmailVerifiedAlsZeichenkette(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	c, q := x.starte(t)
	x.anb.claims = map[string]any{"email_verified": "true"}
	if rec := x.melde(t, c, q, "c"); rec.Code != http.StatusFound || sitzungsCookie(rec) == nil {
		t.Fatalf("email_verified \"true\": %d", rec.Code)
	}
}

func TestAnmeldung816_StateUndCookie(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	c, q := x.starte(t)

	// Ohne Cookie.
	if rec := x.melde(t, nil, q, "c1"); rec.Code != http.StatusBadRequest || x.anb.tokenAufrufe != 0 {
		t.Errorf("ohne Cookie: %d, Token-Aufrufe %d", rec.Code, x.anb.tokenAufrufe)
	}
	// Fremder state.
	q2 := url.Values{}
	for k, v := range q {
		q2[k] = v
	}
	q2.Set("state", "fremd")
	if rec := x.melde(t, c, q2, "c2"); rec.Code != http.StatusBadRequest {
		t.Errorf("fremder state: %d", rec.Code)
	}
	// Verfälschtes Cookie (Inhalt geändert, Signatur alt).
	teil, sig, _ := strings.Cut(c.Value, ".")
	roh, _ := base64.RawURLEncoding.DecodeString(teil)
	var z anmeldeZustand
	_ = json.Unmarshal(roh, &z)
	z.Verifier = "anderer"
	roh, _ = json.Marshal(z)
	falsch := *c
	falsch.Value = base64.RawURLEncoding.EncodeToString(roh) + "." + sig
	if rec := x.melde(t, &falsch, q, "c3"); rec.Code != http.StatusBadRequest {
		t.Errorf("verfälschtes Cookie: %d", rec.Code)
	}
	// Abgelaufen (Uhr 11 Minuten weiter).
	x.anm.jetzt = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if rec := x.melde(t, c, q, "c4"); rec.Code != http.StatusBadRequest {
		t.Errorf("abgelaufenes Cookie: %d", rec.Code)
	}
	x.anm.jetzt = nil
	if x.anb.tokenAufrufe != 0 {
		t.Errorf("Code wurde trotz ungültigem Zustand getauscht (%d)", x.anb.tokenAufrufe)
	}
	// Abbruch beim Anbieter.
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungRueckrufPfad+"?error=access_denied&state="+url.QueryEscape(q.Get("state")), nil)
	r.AddCookie(c)
	rec := httptest.NewRecorder()
	x.anm.Rueckruf(rec, r)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "access_denied") {
		t.Errorf("Abbruch beim Anbieter: %d", rec.Code)
	}
}

// PKCE: Der Token-Endpunkt verlangt den passenden Verifier. Ein Cookie aus
// einem anderen Start (anderer Verifier) mit dem state dieses Starts geht nicht.
func TestAnmeldung816_PKCEVerifierAusDemCookie(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	c1, q1 := x.starte(t)
	_, q2 := x.starte(t)
	q2.Set("state", q1.Get("state"))
	q2.Set("nonce", q1.Get("nonce"))
	// Anbieter kennt den Code mit der Challenge des ZWEITEN Starts.
	rec := x.melde(t, c1, q2, "c")
	if rec.Code != http.StatusBadGateway || sitzungsCookie(rec) != nil {
		t.Fatalf("falscher Verifier: %d — erwartet 502 ohne Sitzung", rec.Code)
	}
}

// ADR-0148 §3: Zulassung nur über die Liste. Ein Konto, ein ACL-Eintrag und
// eine Domain-Freigabe lassen niemanden herein.
func TestAnmeldung816_NurDieListeLaesstHerein(t *testing.T) {
	x := neuerAnmeldeTest(t, "bert@example.com")
	ctx := context.Background()
	// Bestand: Anna hat Konto, ACL-Eintrag als Editorin, und ihre Domain ist
	// auf einem Board freigegeben.
	raw := x.db
	for _, q := range []string{
		`INSERT INTO users (id, email, name, created) VALUES ('u-anna', 'anna@example.com', 'Anna', '2026-01-01')`,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b1', 'B', '1', '{}', 1, 1)`,
		`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', 'anna@example.com', 'editor', '2026-01-01')`,
		`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', '@example.com', 'viewer', '2026-01-01')`,
	} {
		if _, err := raw.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	c, q := x.starte(t)
	rec := x.melde(t, c, q, "c")
	if rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("Konto + ACL + Domain-Freigabe ohne Listeneintrag: %d — erwartet 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Kein Zugang") {
		t.Fatalf("403-Seite ohne freundlichen Text: %s", rec.Body.String())
	}
	var n int
	_ = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Fatalf("Abweisung hat ein Konto angelegt (%d)", n)
	}
}

func TestAnmeldung816_DomainEintragLaesstHerein(t *testing.T) {
	x := neuerAnmeldeTest(t, "@Example.com")
	c, q := x.starte(t)
	if rec := x.melde(t, c, q, "c"); rec.Code != http.StatusFound {
		t.Fatalf("@domain auf der Liste: %d", rec.Code)
	}
}

// ADR-0148 §2: Bindung an (iss, sub). Dieselbe Adresse mit anderem sub → 403.
func TestAnmeldung816_AnderesSubWirdAbgewiesen(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	c, q := x.starte(t)
	if rec := x.melde(t, c, q, "c1"); rec.Code != http.StatusFound {
		t.Fatalf("erste Anmeldung: %d", rec.Code)
	}
	c, q = x.starte(t)
	x.anb.claims = map[string]any{"sub": "sub-jemand-anders"}
	if rec := x.melde(t, c, q, "c2"); rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("anderes sub: %d — erwartet 403", rec.Code)
	}
	// Dasselbe sub geht weiter, und jede Anmeldung bekommt eine neue Sitzung.
	c, q = x.starte(t)
	x.anb.claims = map[string]any{}
	rec1 := x.melde(t, c, q, "c3")
	c, q = x.starte(t)
	rec2 := x.melde(t, c, q, "c4")
	s1, s2 := sitzungsCookie(rec1), sitzungsCookie(rec2)
	if s1 == nil || s2 == nil || s1.Value == s2.Value {
		t.Fatalf("erneute Anmeldung: neue Sitzungs-ID erwartet (%v, %v)", s1, s2)
	}
}

// ADR-0148 §6: Anbieter weg → 503; wieder da → Anmeldung geht ohne Neustart.
func TestAnmeldung816_AnbieterWegUndWiederDa(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	x.anb.mu.Lock()
	x.anb.weg = true
	x.anb.mu.Unlock()
	rec := httptest.NewRecorder()
	x.anm.Start(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungStartPfad, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Anbieter weg: %d, erwartet 503", rec.Code)
	}
	for _, k := range rec.Result().Cookies() {
		if k.Name == AnmeldungCookie {
			t.Fatal("503 setzt trotzdem ein Anmelde-Cookie")
		}
	}
	x.anb.mu.Lock()
	x.anb.weg = false
	x.anb.mu.Unlock()
	time.Sleep(time.Millisecond)
	c, q := x.starte(t)
	if rec := x.melde(t, c, q, "c"); rec.Code != http.StatusFound {
		t.Fatalf("Anbieter wieder da: %d", rec.Code)
	}
}

// Wiederholung frühestens nach der Wartezeit: kein Discovery-Sturm.
func TestAnmeldung816_DiscoveryWartezeit(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	x.anm.Wartezeit = time.Hour
	x.anb.mu.Lock()
	x.anb.weg = true
	x.anb.mu.Unlock()
	if _, err := x.anm.Anbieter(context.Background()); err == nil {
		t.Fatal("Anbieter weg, aber Discovery gelungen")
	}
	x.anb.mu.Lock()
	x.anb.weg = false
	x.anb.mu.Unlock()
	if _, err := x.anm.Anbieter(context.Background()); err == nil {
		t.Fatal("Discovery vor Ablauf der Wartezeit wiederholt")
	}
}

func TestZulassung816_Liste(t *testing.T) {
	z, err := NeueZulassung(" Anna@Example.com,@Firma.DE; bert@x.org\ncarla@y.org ", "Boss@Example.com")
	if err != nil {
		t.Fatal(err)
	}
	for e, erwartet := range map[string]bool{
		"anna@example.com": true, "ANNA@example.com ": true, "bert@x.org": true, "carla@y.org": true,
		"boss@example.com": true, "wer@firma.de": true, "wer@sub.firma.de": false, "wer@firma.de.boese": false,
		"x@example.com": false, "@firma.de": false, "": false, "firma.de": false,
	} {
		if z.Erlaubt(e) != erwartet {
			t.Errorf("Erlaubt(%q) = %v, erwartet %v", e, !erwartet, erwartet)
		}
	}
	for _, schlecht := range []string{"anna", "@", "@localhost", "a@b@c", "anna@"} {
		if _, err := NeueZulassung(schlecht, ""); err == nil {
			t.Errorf("NeueZulassung(%q) ohne Fehler", schlecht)
		}
	}
	leer, _ := NeueZulassung("", "")
	if !leer.Leer() || leer.Erlaubt("anna@example.com") {
		t.Fatal("leere Liste lässt jemanden herein")
	}
	var nilZ *Zulassung
	if nilZ.Erlaubt("anna@example.com") {
		t.Fatal("nil-Zulassung lässt jemanden herein")
	}
	mitBootstrapDomain, _ := NeueZulassung("", "@example.com")
	if mitBootstrapDomain.Erlaubt("x@example.com") {
		t.Fatal("Domain als Bootstrap-Owner öffnet die Domain")
	}
}

// Kritik A-1: Unicode-Faltung (Kelvin-Zeichen U+212A → „k“) darf keine
// fremde Adresse zur zugelassenen machen.
func TestAnmeldung816_NichtASCIIWirdAbgewiesen(t *testing.T) {
	kelvin := "Karl@example.com"
	x := neuerAnmeldeTest(t, "karl@example.com")
	c, q := x.starte(t)
	x.anb.claims = map[string]any{"email": kelvin}
	if rec := x.melde(t, c, q, "c"); rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("Adresse mit Kelvin-Zeichen: %d — erwartet 403", rec.Code)
	}
	if _, err := NeueZulassung(kelvin, ""); err == nil {
		t.Fatal("Zulassungsliste nimmt Nicht-ASCII an")
	}
	// Verify V-2: auch der Bootstrap-Owner.
	z, _ := NeueZulassung("", kelvin)
	if z.Erlaubt("karl@example.com") {
		t.Fatal("Bootstrap-Owner mit Kelvin-Zeichen lässt die ASCII-Adresse herein")
	}
}

// Kritik A-4: Während einer langsamen Discovery hält niemand das Mutex über
// den Netzaufruf; Wartende brechen mit ihrem Kontext ab.
func TestAnmeldung816_DiscoveryBlockiertNicht(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	freigabe := make(chan struct{})
	langsam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-freigabe
		http.Error(w, "weg", http.StatusServiceUnavailable)
	}))
	defer langsam.Close()
	defer close(freigabe)
	x.anm.Issuer = langsam.URL
	x.anm.Client = langsam.Client()
	// Der Auslöser selbst kehrt mit seinem Kontext zurück (Verify V-4) …
	ctx0, ab0 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer ab0()
	ergebnis := make(chan error, 2)
	go func() { _, err := x.anm.Anbieter(ctx0); ergebnis <- err }()
	time.Sleep(20 * time.Millisecond)
	// … und ein zweiter Wartender ebenso (A-4).
	ctx, abbrechen := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer abbrechen()
	go func() { _, err := x.anm.Anbieter(ctx); ergebnis <- err }()
	// Watchdog statt Hänger bis zum Testtimeout (Verify V-9).
	for i := 0; i < 2; i++ {
		select {
		case err := <-ergebnis:
			if err == nil {
				t.Fatal("Anbieter() trotz hängender Discovery ohne Fehler")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Anbieter() kehrt nicht mit dem eigenen Kontext zurück (Mutex über dem Netzaufruf?)")
		}
	}
}
