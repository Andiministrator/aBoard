package api

// Karte #1081, Prüfer B: Absicherungstests der Anmeldewege (gegen aafb5ac0
// grün; braucht einloesenPasswort NICHT). Testdaten nur example.org/example.com.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Ohne ABOARD_BOOTSTRAP_OWNER, zwei gleichzeitige Einlösungen verschiedener
// Einladungen auf einem Server ohne Admin (Nachtrag A-5, §50 Startschranke und
// Runden): höchstens eine wird ohne Bestätigung aktiv, höchstens ein Admin.
func TestPrueferB1081_ErsterAdminParallel(t *testing.T) {
	for runde := 0; runde < 5; runde++ {
		x := neuerEinloeseTest(t, "")
		tokA, tokB := x.einladen(t, "a@example.org"), x.einladen(t, "b@example.org")
		gA, sA := x.schritt1(t, tokA)
		gB, sB := x.schritt1(t, tokB)
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, f := range []struct {
			tok, siegel string
			g           []byte
		}{{tokA, sA, gA}, {tokB, sB, gB}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r := httptest.NewRequest(http.MethodPost, "https://board.example"+EinladungPfadPraefix+f.tok,
					strings.NewReader(url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {f.siegel},
						"code": {totpCode(f.g, TOTPSchritt(x.jetzt))}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				x.mux.ServeHTTP(rec, r)
				codes[i] = rec.Code
			}()
		}
		close(start)
		wg.Wait()
		var admins, aktiv int
		_ = x.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&admins)
		_ = x.db.QueryRow(`SELECT COUNT(*) FROM konten WHERE zustand = 'active'`).Scan(&aktiv)
		if admins > 1 || aktiv > 1 {
			t.Fatalf("Runde %d: %d Admins, %d aktive Konten (Codes %v)", runde, admins, aktiv, codes)
		}
	}
}

// Derselbe Link zweimal gleichzeitig eingelöst (zwei Siegel aus Schritt 1):
// genau einer gewinnt.
func TestPrueferB1081_EinloesenParallelDerselbeLink(t *testing.T) {
	for runde := 0; runde < 5; runde++ {
		x := neuerEinloeseTest(t, "")
		x.mitAdmin()
		tok := x.einladen(t, "anna@example.org")
		g1, s1 := x.schritt1(t, tok)
		g2, s2 := x.schritt1(t, tok)
		start := make(chan struct{})
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, f := range []struct {
			siegel string
			g      []byte
		}{{s1, g1}, {s2, g2}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r := httptest.NewRequest(http.MethodPost, "https://board.example"+EinladungPfadPraefix+tok,
					strings.NewReader(url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {f.siegel},
						"code": {totpCode(f.g, TOTPSchritt(x.jetzt))}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				x.mux.ServeHTTP(rec, r)
				codes[i] = rec.Code
			}()
		}
		close(start)
		wg.Wait()
		ok := 0
		for _, c := range codes {
			if c == http.StatusOK {
				ok++
			}
		}
		if ok != 1 {
			t.Fatalf("Runde %d: %d erfolgreiche Einlösungen (Codes %v)", runde, ok, codes)
		}
		if z := x.zustand(t, "anna@example.org"); z != storage.KontoWartet {
			t.Fatalf("Runde %d: Zustand %q", runde, z)
		}
	}
}

// ---------------------------------------------------------------------------
// Abgelaufene Einladung (7 Tage) — auch zwischen Start beim Anbieter und
// Rückruf, und beim Passwort-Schritt 2.
func TestPrueferB1081_EinladungAbgelaufen(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	x.mitAdmin()
	tok := x.einladen(t, "anna@example.org")
	c, q := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil))
	g, siegel := x.schritt1(t, tok)
	// Ablauf in die Vergangenheit (die Repo-Uhr ist nicht von außen setzbar).
	x.exec(`UPDATE einladungen SET ablauf = ?`, time.Now().Add(-time.Second).Unix())
	if rec := x.hole(t, EinladungPfadPraefix+tok, nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET abgelaufen: %d", rec.Code)
	}
	if rec := x.post(t, tok, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {siegel},
		"code": {totpCode(g, TOTPSchritt(x.jetzt))}}); rec.Code != http.StatusNotFound {
		t.Errorf("Schritt 2 abgelaufen: %d", rec.Code)
	}
	if rec := x.melde(t, x.google, "google", c, q, "ab1", map[string]any{"sub": "sub-anna"}); rec.Code != http.StatusNotFound || sitzungsCookie(rec) != nil {
		t.Errorf("Rückruf nach Ablauf: %d", rec.Code)
	}
	if z := x.zustand(t, "anna@example.org"); z != storage.KontoEingeladen {
		t.Errorf("Zustand %q", z)
	}
	var n int
	_ = x.db.QueryRow(`SELECT COUNT(*) FROM anbieter_bindungen`).Scan(&n)
	if n != 0 {
		t.Errorf("Bindung trotz Ablauf: %d", n)
	}
}

// Kürzel-Vertauschung beim Einlösen: Vorgang bei google begonnen, Rückruf am
// Pfad von authelia mit einem Code von authelia → nicht eingelöst.
func TestPrueferB1081_KuerzelVertauschungInvite(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	x.mitAdmin()
	tok := x.einladen(t, "anna@example.org")
	c, q := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil))
	rec := x.melde(t, x.authelia, "authelia", c, q, "kv1", map[string]any{"sub": "sub-x"})
	if rec.Code != http.StatusBadRequest || sitzungsCookie(rec) != nil {
		t.Fatalf("Vertauschung: %d %s", rec.Code, rec.Body.String())
	}
	if z := x.zustand(t, "anna@example.org"); z != storage.KontoEingeladen {
		t.Fatalf("eingelöst über fremden Rückruf: %q", z)
	}
	// Umgekehrt: Login-Vorgang bei authelia, Code von google am google-Pfad.
	c, q = x.starte(t, "authelia")
	if rec := x.melde(t, x.google, "google", c, q, "kv2", map[string]any{"sub": "sub-y", "email": "admin@example.org"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("Login-Vertauschung: %d", rec.Code)
	}
}

// Bindung schlägt Adresse: Das an X gebundene Anbieter-Konto meldet die
// (bestätigte) Adresse von Y, die ein aktives Passwort-Konto hat, und sogar
// email_verified=false — angemeldet wird X.
func TestPrueferB1081_BindungSchlaegtAdresse(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	x.mitAdmin()
	x.exec(`INSERT INTO konten (id, email, zustand, weg, passwort_hash, angelegt, geaendert) VALUES ('k-y', 'yvonne@example.org', 'active', 'password', 'x', 1, 1)`)
	tok := x.einladen(t, "xaver@example.org")
	c, q := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil))
	if rec := x.melde(t, x.google, "google", c, q, "b1", map[string]any{"sub": "sub-x"}); rec.Code != http.StatusOK {
		t.Fatalf("Einlösen: %d", rec.Code)
	}
	if _, err := x.konten.Bestaetigen(context.Background(), kontoID(t, x.db, "xaver@example.org"), NutzerAnlegen(storage.NewBootstrapRepo(x.db)), 1); err != nil {
		t.Fatal(err)
	}
	x.tor.Vergessen("xaver@example.org")
	for i, claims := range []map[string]any{
		{"sub": "sub-x", "email": "yvonne@example.org"},
		{"sub": "sub-x", "email": "yvonne@example.org", "email_verified": false},
	} {
		c, q := x.starte(t, "google")
		rec := x.melde(t, x.google, "google", c, q, "b2"+string(rune('a'+i)), claims)
		if got := x.sitzungsAdresse(t, rec); got != "xaver@example.org" {
			t.Errorf("Fall %d: Sitzung %q, erwartet xaver@example.org (%d)", i, got, rec.Code)
		}
	}
	// Dasselbe sub an einem anderen Anbieter (anderer Issuer) ist ein anderes
	// Anbieter-Konto: nicht gebunden, nicht auf der Liste → kein Zugang.
	c, q = x.starte(t, "authelia")
	if rec := x.melde(t, x.authelia, "authelia", c, q, "b3", map[string]any{"sub": "sub-x", "email": "xaver@example.org"}); rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Errorf("gleiches sub, anderer Issuer: %d", rec.Code)
	}
}

// Sperren, Entsperren und Zurücksetzen gegen eine Sitzung aus einem
// Anbieter-Login: Sperren beendet sie, danach kein neuer Login; Entsperren
// führt nach pending (kein Login); Zurücksetzen löst die Bindung.
func TestPrueferB1081_SperrenBeendetAnbieterSitzung(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	x.mitAdmin()
	tok := x.einladen(t, "anna@example.org")
	c, q := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil))
	x.melde(t, x.google, "google", c, q, "s1", map[string]any{"sub": "sub-anna"})
	id := kontoID(t, x.db, "anna@example.org")
	if _, err := x.konten.Bestaetigen(context.Background(), id, NutzerAnlegen(storage.NewBootstrapRepo(x.db)), 1); err != nil {
		t.Fatal(err)
	}
	x.tor.Vergessen("anna@example.org")
	c, q = x.starte(t, "google")
	rec := x.melde(t, x.google, "google", c, q, "s2", map[string]any{"sub": "sub-anna"})
	sc := sitzungsCookie(rec)
	if sc == nil {
		t.Fatalf("Login: %d", rec.Code)
	}
	if _, err := x.konten.Sperren(context.Background(), id, 2); err != nil {
		t.Fatal(err)
	}
	x.tor.Vergessen("anna@example.org")
	if _, err := x.sitzungen.Pruefen(context.Background(), sc.Value); err == nil {
		t.Error("Sitzung lebt nach dem Sperren")
	}
	login := func(code string) *httptest.ResponseRecorder {
		c, q := x.starte(t, "google")
		return x.melde(t, x.google, "google", c, q, code, map[string]any{"sub": "sub-anna"})
	}
	if rec := login("s3"); sitzungsCookie(rec) != nil {
		t.Error("Login trotz Sperre")
	}
	if _, _, err := x.konten.Entsperren(context.Background(), id, 3); err != nil {
		t.Fatal(err)
	}
	x.tor.Vergessen("anna@example.org")
	if rec := login("s4"); sitzungsCookie(rec) != nil {
		t.Error("Login nach Entsperren ohne neue Bestätigung")
	}
	if _, err := x.einladung.Zuruecksetzen(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	x.tor.Vergessen("anna@example.org")
	if rec := login("s5"); sitzungsCookie(rec) != nil || rec.Code != http.StatusForbidden {
		t.Errorf("Login nach Zurücksetzen: %d", rec.Code)
	}
}

// TOTP über die Fenstergrenze: Ein angenommener Code gilt im nächsten
// Zeitschritt nicht noch einmal; ein Code aus der Zukunft (s+1) sperrt danach
// den Code von s.
func TestPrueferB1081_TOTPFenstergrenze(t *testing.T) {
	g := []byte("12345678901234567890")
	t0 := time.Unix(1_800_000_000, 0)
	s := TOTPSchritt(t0)
	code := totpCode(g, s)
	got, ok := TOTPPasst(g, code, t0, 0)
	if !ok || got != s {
		t.Fatalf("erste Annahme: %d %v", got, ok)
	}
	if _, ok := TOTPPasst(g, code, t0.Add(TOTPSchrittweite), got); ok {
		t.Error("derselbe Code im nächsten Schritt erneut angenommen")
	}
	vor, ok := TOTPPasst(g, totpCode(g, s+1), t0, 0)
	if !ok || vor != s+1 {
		t.Fatalf("Code s+1: %d %v", vor, ok)
	}
	if _, ok := TOTPPasst(g, code, t0, vor); ok {
		t.Error("Code s nach angenommenem s+1 angenommen")
	}
	if _, ok := TOTPPasst(g, totpCode(g, s+2), t0, 0); ok {
		t.Error("Code s+2 angenommen (Fenster zu weit)")
	}
}

// ---------------------------------------------------------------------------
// Pfad-Ausnahmen (ADR-0149 §5.8): Kein Pfad, der der Ausnahme ähnelt, erreicht
// ohne Sitzung die App (Catch-all) — weder über Groß-/Kleinschreibung,
// Schrägstriche, kodierte Zeichen noch Punkt-Segmente.
func TestPrueferB1081_PfadAusnahmenErreichenNieDieApp(t *testing.T) {
	t.Setenv(AuthModeEnvVar, "builtin")
	t.Setenv(AnmeldungEnvVar, "")
	x := neuerEinloeseTest(t, "")
	x.mux.HandleFunc("GET "+AnmeldungSeitePfad, x.tabelle.Seite)
	x.mux.HandleFunc("GET "+AnmeldungAbgemeldetPfad, x.tabelle.Abgemeldet)
	x.mux.HandleFunc("POST "+AnmeldungPasswortPfad, x.tabelle.PasswortPost)
	x.mux.HandleFunc("POST "+AnmeldungCodePfad, x.tabelle.CodePost)
	x.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("APP")) })
	weiche := (&Sitzungsweiche{Sitzungen: x.sitzungen, Zugelassen: x.tor.Erlaubt}).Middleware(x.mux)
	tok := x.einladen(t, "anna@example.org")
	for _, f := range []struct{ methode, ziel string }{
		{"GET", "/anmeldung/"}, {"GET", "/Anmeldung"}, {"GET", "/ANMELDUNG"}, {"GET", "//anmeldung"},
		{"GET", "/anmeldung/start/"}, {"GET", "/anmeldung/abgemeldet/"}, {"GET", "/anmeldung/rueckruf/"},
		{"GET", "/anmeldung/rueckruf/google/"}, {"GET", "/anmeldung/rueckruf/GOOGLE"}, {"GET", "/anmeldung/rueckruf/google/x"},
		{"GET", "/anmeldung/rueckruf/google%2F..%2F..%2Fboards"}, {"GET", "/anmeldung/rueckruf/..%2F..%2Fadmin%2Fkonten"},
		{"GET", "/anmeldung/rueckruf/../../admin/konten"}, {"GET", "/anmeldung/./rueckruf/google"},
		{"GET", "/anmeldung/rueckruf/" + strings.Repeat("a", 33)}, {"GET", "/anmeldung%2Frueckruf"},
		{"GET", "/einladung/" + tok + "/"}, {"GET", "/einladung/" + tok + "/x"}, {"GET", "/einladung/" + tok[:42]},
		{"GET", "/einladung/" + tok + "A"}, {"GET", "/einladung/" + tok[:41] + "%2F"}, {"GET", "/einladung/" + tok[:40] + "/.."},
		{"GET", "/einladungX/" + tok}, {"GET", "/einladungen/" + tok}, {"GET", "/Einladung/" + tok},
		{"GET", "/einladung/" + tok + "/../../admin/konten"}, {"GET", "/einladung/../admin/konten"},
		{"HEAD", "/einladung/" + tok + "/"}, {"OPTIONS", "/einladung/" + tok}, {"PUT", "/einladung/" + tok},
		{"DELETE", "/anmeldung/rueckruf/google"},
		{"POST", "/anmeldung"}, {"POST", "/anmeldung/passwort/"}, {"POST", "/anmeldung/Passwort"}, {"POST", "/anmeldung/code/"},
		{"POST", "/anmeldung/passwort%2F"}, {"POST", "/anmeldung/rueckruf/google"}, {"POST", "/einladung/" + tok + "/"},
		{"POST", "/anmeldung/start"}, {"POST", "/anmeldung/abgemeldet"},
		{"GET", "/anmeldung/passwort"}, {"GET", "/anmeldung/code"},
	} {
		for _, accept := range []string{"text/html", "*/*"} {
			r := httptest.NewRequest(f.methode, "https://board.example"+f.ziel, nil)
			r.Header.Set("Accept", accept)
			rec := httptest.NewRecorder()
			weiche.ServeHTTP(rec, r)
			if strings.Contains(rec.Body.String(), "APP") {
				t.Errorf("%s %s (%s): ohne Sitzung in der App (%d)", f.methode, f.ziel, accept, rec.Code)
			}
		}
	}
	// Gegenprobe: Die echten Ausnahmen kommen an (nicht 401/Weiterleitung).
	r := httptest.NewRequest("GET", "https://board.example/einladung/"+tok, nil)
	rec := httptest.NewRecorder()
	weiche.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "anna@example.org") {
		t.Errorf("Gegenprobe Einladungsseite: %d", rec.Code)
	}
}
