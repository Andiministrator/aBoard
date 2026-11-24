package api

// wissen_internal_test.go — /wissen/* (Karte #739, ADR-0137 §2/§3/§5).
// Intern, weil der Ordner im Test nicht unter /data/ liegen kann: praefix
// wird auf das Testverzeichnis gesetzt.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/wissen"
)

type wissenEnv struct {
	srv    *httptest.Server
	cfg    *storage.ConfigRepo
	users  *storage.UserRepo
	wissen *WissenAPI
	basis  string // Präfix-Verzeichnis
	dir    string // Wissensordner darunter
}

func newWissenEnv(t *testing.T) *wissenEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	users := storage.NewUserRepo(db)
	var ctr atomic.Int64
	cfg := storage.NewConfigRepo(db, nil, func() string { return "audit-" + strconv.FormatInt(ctr.Add(1), 10) })

	basis := t.TempDir()
	dir := filepath.Join(basis, "wissen")
	for rel, inhalt := range map[string]string{
		"Datenschutz & Consent/cookies.md": "# Cookies\n\nConsent-Banner erklärt.\n",
		"aboard/start.md":                  "# Start\n",
		".git/config":                      "url = https://token:geheim@forge/x.git\n", // abzug:testwert
		".wissen-stand.json":               `{"commit":"c0db7e3","datum":"2026-10-08"}`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(inhalt), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(".git/config", filepath.Join(dir, "x.md")); err != nil {
		t.Fatal(err)
	}

	w := NewWissenAPI(cfg, users)
	w.praefix = basis + "/"
	admin := NewAdminAPI(cfg, users, "test", time.Now())
	admin.WithKonfigPruefung("wissen.", w.PruefeKonfig)
	admin.WithLoeschPruefung("wissen.", w.PruefeLoeschen)
	admin.Registry().RegisterTest("wissen", w.WissenTest)

	mux := http.NewServeMux()
	w.Register(mux)
	admin.Register(mux)
	RegisterMeHandler(mux, users, MeWithWissen(w.SichtbarFuer))
	srv := httptest.NewServer(WithForwardAuthIfEnabled(mux))
	t.Cleanup(srv.Close)
	t.Cleanup(w.Vergiss)

	for _, u := range []struct {
		id, email string
		admin     int
	}{{"u-admin", "admin@x.de", 1}, {"u-anna", "anna@x.de", 0}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, ?)`,
			u.id, u.email, "N", "2026-01-01", u.admin); err != nil {
			t.Fatal(err)
		}
	}
	return &wissenEnv{srv: srv, cfg: cfg, users: users, wissen: w, basis: basis, dir: dir}
}

func (e *wissenEnv) setze(t *testing.T, key, value string) {
	t.Helper()
	if err := e.cfg.Set(context.Background(), key, value, false, "test"); err != nil {
		t.Fatal(err)
	}
}

func (e *wissenEnv) einschalten(t *testing.T) {
	t.Helper()
	e.setze(t, wissen.KeyEnabled, "true")
	e.setze(t, wissen.KeyDir, e.dir)
}

func (e *wissenEnv) get(t *testing.T, pfad, email string) (int, map[string]any) {
	t.Helper()
	return e.anfrage(t, http.MethodGet, pfad, email, nil)
}

func (e *wissenEnv) anfrage(t *testing.T, methode, pfad, email string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(methode, e.srv.URL+pfad, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestWissen_AusHeisst404(t *testing.T) {
	e := newWissenEnv(t)
	for _, p := range []string{"/wissen/index", "/wissen/seite?pfad=aboard/start.md", "/wissen/suche?q=start"} {
		if code, _ := e.get(t, p, "admin@x.de"); code != http.StatusNotFound {
			t.Errorf("%s bei aus: %d", p, code)
		}
	}
	// Eingeschaltet, aber Ordner ausserhalb des Präfixes (am Speichern vorbei
	// in die Tabelle gekommen): bleibt aus.
	e.setze(t, wissen.KeyEnabled, "true")
	e.setze(t, wissen.KeyDir, "/etc")
	if code, _ := e.get(t, "/wissen/index", "admin@x.de"); code != http.StatusNotFound {
		t.Errorf("ungültiges dir: %d", code)
	}
	if code, m := e.get(t, "/me", "admin@x.de"); code != 200 || m["wissen"] != nil {
		t.Errorf("/me bei ungültigem dir: %d %v", code, m)
	}
}

func TestWissen_IndexSeiteSuche(t *testing.T) {
	e := newWissenEnv(t)
	e.einschalten(t)
	e.setze(t, wissen.KeyName, "Team-Wissen")
	e.setze(t, wissen.KeyBearbeitenURL, "https://forge.example/team/team-wissen/_edit/main/{pfad}")

	code, m := e.get(t, "/wissen/index", "anna@x.de")
	if code != 200 || m["name"] != "Team-Wissen" || m["bearbeiten_url"] != true {
		t.Fatalf("index: %d %v", code, m)
	}
	seiten, _ := m["seiten"].([]any)
	if len(seiten) != 2 {
		t.Fatalf("seiten: %v", seiten)
	}
	if st, _ := m["stand"].(map[string]any); st["commit"] != "c0db7e3" {
		t.Fatalf("stand: %v", m["stand"])
	}

	// Pfad mit Leerzeichen und &, genau einmal kodiert
	q := url.Values{"pfad": {"Datenschutz & Consent/cookies.md"}}
	code, m = e.get(t, "/wissen/seite?"+q.Encode(), "anna@x.de")
	if code != 200 || !strings.Contains(m["inhalt"].(string), "Consent-Banner") ||
		m["bearbeiten"] != "https://forge.example/team/team-wissen/_edit/main/Datenschutz%20%26%20Consent/cookies.md" {
		t.Fatalf("seite: %d %v", code, m)
	}
	// Doppelt kodiert ist ein anderer Name — nicht gefunden.
	if code, _ = e.get(t, "/wissen/seite?pfad=Datenschutz%2520%2526%2520Consent/cookies.md", "anna@x.de"); code != 404 {
		t.Errorf("doppelt kodiert: %d", code)
	}
	for _, p := range []string{"x.md", ".git/config", "../wissen/aboard/start.md", "aboard/../.git/config"} {
		if code, m = e.get(t, "/wissen/seite?"+url.Values{"pfad": {p}}.Encode(), "anna@x.de"); code != 404 {
			t.Errorf("seite %q: %d %v", p, code, m)
		}
	}
	if code, _ = e.get(t, "/wissen/seite", "anna@x.de"); code != 400 {
		t.Errorf("ohne pfad: %d", code)
	}

	code, m = e.get(t, "/wissen/suche?q=consent", "anna@x.de")
	treffer, _ := m["treffer"].([]any)
	if code != 200 || len(treffer) != 1 {
		t.Fatalf("suche: %d %v", code, m)
	}
	if code, _ = e.get(t, "/wissen/suche?q=geheim", "anna@x.de"); code != 200 {
		t.Fatalf("suche geheim: %d", code)
	} else if _, m = e.get(t, "/wissen/suche?q=geheim", "anna@x.de"); len(m["treffer"].([]any)) != 0 {
		t.Fatalf(".git durchsucht: %v", m)
	}
	if code, _ = e.get(t, "/wissen/suche?q=x", "anna@x.de"); code != 400 {
		t.Errorf("zu kurze Anfrage: %d", code)
	}
}

func TestWissen_Zugang(t *testing.T) {
	e := newWissenEnv(t)
	e.einschalten(t)
	// Nicht provisioniert (kein users-Eintrag): kein Wissen.
	if code, _ := e.get(t, "/wissen/index", "fremd@y.de"); code != http.StatusForbidden {
		t.Errorf("nicht provisioniert: %d", code)
	}
	// Ohne Identität: schon die Forward-Auth weist ab.
	if code, _ := e.get(t, "/wissen/index", ""); code != http.StatusUnauthorized {
		t.Errorf("ohne Identität: %d", code)
	}
	if _, m := e.get(t, "/me", "anna@x.de"); m["wissen"] != "Wissen" {
		t.Errorf("/me anna bei alle: %v", m)
	}

	e.setze(t, wissen.KeySichtbar, wissen.SichtbarAdmins)
	if code, _ := e.get(t, "/wissen/index", "anna@x.de"); code != http.StatusForbidden {
		t.Errorf("admins-only, anna: %d", code)
	}
	if code, _ := e.get(t, "/wissen/seite?pfad=aboard/start.md", "anna@x.de"); code != http.StatusForbidden {
		t.Errorf("admins-only, anna seite: %d", code)
	}
	if code, _ := e.get(t, "/wissen/index", "admin@x.de"); code != 200 {
		t.Errorf("admins-only, admin: %d", code)
	}
	if _, m := e.get(t, "/me", "anna@x.de"); m["wissen"] != nil {
		t.Errorf("/me anna bei admins: %v", m)
	}
	if _, m := e.get(t, "/me", "admin@x.de"); m["wissen"] != "Wissen" {
		t.Errorf("/me admin bei admins: %v", m)
	}
}

func TestWissen_SuchBremse(t *testing.T) {
	e := newWissenEnv(t)
	e.einschalten(t)
	for i := 0; i < wissenSuchenJeMinute; i++ {
		if code, _ := e.get(t, "/wissen/suche?q=start", "anna@x.de"); code != 200 {
			t.Fatalf("Suche %d: %d", i, code)
		}
	}
	if code, _ := e.get(t, "/wissen/suche?q=start", "anna@x.de"); code != http.StatusTooManyRequests {
		t.Fatalf("über der Grenze: %d", code)
	}
	// Je Person: admin ist davon unberührt.
	if code, _ := e.get(t, "/wissen/suche?q=start", "admin@x.de"); code != 200 {
		t.Fatalf("andere Person gebremst: %d", code)
	}
}

func TestWissen_SpeichernWirdGeprueft(t *testing.T) {
	e := newWissenEnv(t)
	faelle := []struct {
		key, value string
		code       int
	}{
		{wissen.KeyDir, "/", 400},
		{wissen.KeyDir, e.basis, 400}, // der Präfix selbst
		{wissen.KeyDir, e.dir, 200},
		{wissen.KeyBearbeitenURL, "http://forge.example/{pfad}", 400},
		{wissen.KeyBearbeitenURL, "https://forge.example/{pfad}", 200},
		{wissen.KeySichtbar, "jeder", 400},
		{wissen.KeyEnabled, "ja", 400},
		{"wissen.unbekannt", "x", 400},
	}
	for _, f := range faelle {
		code, m := e.anfrage(t, http.MethodPut, "/admin/config/"+f.key, "admin@x.de", map[string]any{"value": f.value})
		if code != f.code {
			t.Errorf("PUT %s=%q: %d %v — erwartet %d", f.key, f.value, code, m, f.code)
		}
	}
	// Kritiker #739 K-1: Ordner muss existieren; einschalten nur mit gültigem Ordner.
	if code, m := e.anfrage(t, http.MethodPut, "/admin/config/"+wissen.KeyDir, "admin@x.de",
		map[string]any{"value": filepath.Join(e.basis, "gibtsnicht")}); code != 400 {
		t.Errorf("fehlender Ordner: %d %v", code, m)
	}
	// Normalform gespeichert
	if v, err := e.cfg.Get(context.Background(), wissen.KeyDir); err != nil || v.Value != e.dir {
		t.Errorf("dir gespeichert: %v %v", v, err)
	}
	code, _ := e.anfrage(t, http.MethodPut, "/admin/config/"+wissen.KeyName, "admin@x.de",
		map[string]any{"value": "x", "is_secret": true})
	if code != 400 {
		t.Errorf("Geheimwert unter wissen.: %d", code)
	}
	// Prüfen-Knopf: läuft auch bei aus und nennt Seiten und Stand.
	code, m := e.anfrage(t, http.MethodPost, "/admin/config/wissen/test", "admin@x.de", nil)
	res, _ := m["result"].(map[string]any)
	if code != 200 || res["seiten"] != float64(2) {
		t.Fatalf("test-hook: %d %v", code, m)
	}
}

// Agenten-Token — Board-Token wie delegierendes OWUI-Token — erreichen
// /wissen nicht (ADR-0137 §3). agentDarf ist eine abschliessende Liste.
func TestWissen_AgentDarfNicht(t *testing.T) {
	for _, p := range []*AgentPrincipal{
		{Name: "e", BoardID: "b1", Role: storage.AgentRolleEditor},
		{Name: "v", BoardID: "b1", Role: storage.AgentRolleViewer},
		{Name: "o", BoardID: "b1", Role: storage.AgentRolleEditor, Delegiert: true},
	} {
		for _, pfad := range []string{"/wissen/index", "/wissen/seite?pfad=a.md", "/wissen/suche?q=ab",
			"/werkzeuge/wissen/index", "/boards/b1/wissen/index"} {
			r := httptest.NewRequest(http.MethodGet, pfad, nil)
			if agentDarf(p, r) {
				t.Errorf("%s darf %s", p.Name, pfad)
			}
		}
	}
}

// Kritiker #739 K-1: „einschalten“ ohne gespeicherten gültigen Ordner wird
// abgelehnt — sonst meldete der Dialog „gespeichert“, und nichts wirkte.
func TestWissen_EinschaltenNurMitOrdner(t *testing.T) {
	e := newWissenEnv(t)
	put := func(key, value string) int {
		code, _ := e.anfrage(t, http.MethodPut, "/admin/config/"+key, "admin@x.de", map[string]any{"value": value})
		return code
	}
	if code := put(wissen.KeyEnabled, "true"); code != 400 {
		t.Fatalf("ohne Ordner eingeschaltet: %d", code)
	}
	if code := put(wissen.KeyEnabled, "false"); code != 200 {
		t.Fatalf("ausschalten geht immer: %d", code)
	}
	if code := put(wissen.KeyDir, e.dir); code != 200 {
		t.Fatalf("Ordner: %d", code)
	}
	if code := put(wissen.KeyEnabled, "true"); code != 200 {
		t.Fatalf("mit Ordner: %d", code)
	}
	if code, _ := e.get(t, "/wissen/index", "anna@x.de"); code != 200 {
		t.Fatalf("danach wirksam: %d", code)
	}
}

// Kritiker #739 K-3: Wird der Ordner ausgetauscht, liest der Server den neuen.
func TestWissen_OrdnerAusgetauscht(t *testing.T) {
	e := newWissenEnv(t)
	e.einschalten(t)
	if _, m := e.get(t, "/wissen/index", "anna@x.de"); len(m["seiten"].([]any)) != 2 {
		t.Fatalf("vorher: %v", m)
	}
	if err := os.Rename(e.dir, e.dir+"-alt"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "neu.md"), []byte("# Neu"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, m := e.get(t, "/wissen/index", "anna@x.de")
	seiten, _ := m["seiten"].([]any)
	if len(seiten) != 1 {
		t.Fatalf("alter Ordner gelesen: %v", m)
	}
}

func TestWissen_LeseBremse(t *testing.T) {
	e := newWissenEnv(t)
	e.einschalten(t)
	for i := 0; i < wissenLesenJeMinute; i++ {
		if code, _ := e.get(t, "/wissen/seite?pfad=aboard/start.md", "anna@x.de"); code != 200 {
			t.Fatalf("Abruf %d: %d", i, code)
		}
	}
	if code, _ := e.get(t, "/wissen/index", "anna@x.de"); code != http.StatusTooManyRequests {
		t.Fatalf("über der Grenze: %d", code)
	}
}

// Verify #739 V-1/V-2: Bei verschwundenem Ordner lässt sich ausschalten und
// sonst speichern; leeren/löschen des Ordners nur, solange das Wissen aus ist.
func TestWissen_AusschaltenBeiFehlendemOrdner(t *testing.T) {
	e := newWissenEnv(t)
	put := func(key, value string) int {
		code, _ := e.anfrage(t, http.MethodPut, "/admin/config/"+key, "admin@x.de", map[string]any{"value": value})
		return code
	}
	if put(wissen.KeyDir, e.dir) != 200 || put(wissen.KeyEnabled, "true") != 200 {
		t.Fatal("Einrichten gescheitert")
	}
	if err := os.Rename(e.dir, e.dir+"-weg"); err != nil {
		t.Fatal(err)
	}
	if code := put(wissen.KeyDir, e.dir); code != 200 {
		t.Fatalf("unveränderter Ordner abgelehnt: %d", code)
	}
	if code := put(wissen.KeyName, "Neu"); code != 200 {
		t.Fatalf("Name: %d", code)
	}
	// W-3: der Dialog sendet bei angelassenem Häkchen am Ende enabled=true.
	if code := put(wissen.KeyEnabled, "true"); code != 200 {
		t.Fatalf("enabled=true bei schon an und fehlendem Ordner: %d", code)
	}
	if code := put(wissen.KeyDir, ""); code != 400 {
		t.Fatalf("Ordner geleert, obwohl an: %d", code)
	}
	if code, _ := e.anfrage(t, http.MethodDelete, "/admin/config/"+wissen.KeyDir, "admin@x.de", nil); code != 400 {
		t.Fatalf("Ordner gelöscht, obwohl an: %d", code)
	}
	if code := put(wissen.KeyEnabled, "false"); code != 200 {
		t.Fatalf("ausschalten: %d", code)
	}
	if code := put(wissen.KeyDir, ""); code != 200 {
		t.Fatalf("leeren nach dem Ausschalten: %d", code)
	}
}

// Verify #739 V-3: ein ungültiger Wert wird einmal geloggt — und nach einer
// Genesung wieder.
func TestWissen_LogEinmalUndNachGenesungWieder(t *testing.T) {
	e := newWissenEnv(t)
	var buf bytes.Buffer
	altOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(altOut) })
	zaehle := func() int { return strings.Count(buf.String(), "wissen.* ungültig") }
	e.setze(t, wissen.KeyEnabled, "true")
	e.setze(t, wissen.KeyDir, "/etc")
	e.get(t, "/wissen/index", "anna@x.de")
	e.get(t, "/wissen/index", "anna@x.de")
	if n := zaehle(); n != 1 {
		t.Fatalf("%d Logzeilen, erwartet 1", n)
	}
	e.setze(t, wissen.KeyDir, e.dir)
	e.get(t, "/wissen/index", "anna@x.de")
	e.setze(t, wissen.KeyDir, "/etc")
	e.get(t, "/wissen/index", "anna@x.de")
	if n := zaehle(); n != 2 {
		t.Fatalf("%d Logzeilen nach Genesung, erwartet 2", n)
	}
}

// Verify #739 W-1: Alteinträge und gewöhnliche Schlüssel lassen sich löschen.
func TestWissen_LoeschenAndererSchluessel(t *testing.T) {
	e := newWissenEnv(t)
	e.setze(t, "wissen.altlast", "x")
	e.setze(t, wissen.KeyName, "N")
	for _, k := range []string{"wissen.altlast", wissen.KeyName, wissen.KeyEnabled} {
		if code, m := e.anfrage(t, http.MethodDelete, "/admin/config/"+k, "admin@x.de", nil); code != 200 {
			t.Errorf("DELETE %s: %d %v", k, code, m)
		}
	}
}

// getMitGruppen wie get, mit Remote-Groups.
func (e *wissenEnv) getMitGruppen(t *testing.T, pfad, email, gruppen string) (int, map[string]any) {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, e.srv.URL+pfad, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Remote-Email", email)
	r.Header.Set("Remote-User", email)
	r.Header.Set("Remote-Groups", gruppen)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

// F-1863: wissen.sichtbar=gruppe:<name> — nur Mitglieder der Gruppe laut
// Remote-Groups, dazu Admins. Alle drei Wege und /me.
func TestWissen_Gruppe(t *testing.T) {
	e := newWissenEnv(t)
	e.einschalten(t)
	e.setze(t, wissen.KeySichtbar, "gruppe:wissen")

	for _, pfad := range []string{"/wissen/index", "/wissen/seite?pfad=aboard/start.md", "/wissen/suche?q=start"} {
		if code, _ := e.getMitGruppen(t, pfad, "anna@x.de", "team,wissen-alt"); code != http.StatusForbidden {
			t.Errorf("anna ohne Gruppe %s: %d", pfad, code)
		}
		if code, _ := e.getMitGruppen(t, pfad, "anna@x.de", "team, wissen"); code != 200 {
			t.Errorf("anna mit Gruppe %s: %d", pfad, code)
		}
	}
	if _, m := e.getMitGruppen(t, "/me", "anna@x.de", "team"); m["wissen"] != nil {
		t.Errorf("/me anna ohne Gruppe: %v", m)
	}
	if _, m := e.getMitGruppen(t, "/me", "anna@x.de", "wissen"); m["wissen"] != "Wissen" {
		t.Errorf("/me anna mit Gruppe: %v", m)
	}
	if code, _ := e.getMitGruppen(t, "/wissen/index", "admin@x.de", ""); code != 200 {
		t.Errorf("Admin ohne Gruppe: %d", code)
	}
}

// F-1887 (Sprint-86-Kritiker K-B1): Über die echten Wege — Speichern, Prüfen-
// Knopf, Einschalten und Lesen — lehnt die API einen wissen.dir ab, der einen
// Server-Datenordner trifft (Klasse 184: die Verdrahtung testen, nicht nur
// die Hilfsfunktion).
func TestWissen_ServerOrdnerGesperrt(t *testing.T) {
	for _, fall := range []string{"anhaenge", "eltern", "sicherung"} {
		e := newWissenEnv(t)
		switch fall {
		case "anhaenge":
			e.wissen.MitServerOrdnern(e.dir) // Wissensordner IST der Anhang-Ordner
		case "eltern":
			e.wissen.MitServerOrdnern(filepath.Join(e.dir, "aboard")) // Server-Ordner liegt darin
		case "sicherung":
			e.setze(t, "backup_board.dir", e.dir)
		}
		if code, m := e.anfrage(t, http.MethodPut, "/admin/config/"+wissen.KeyDir, "admin@x.de", map[string]any{"value": e.dir}); code != 400 {
			t.Errorf("%s: Speichern %d %v", fall, code, m)
		}
		// Am Speichern vorbei eingetragen: Prüfen-Knopf lehnt ab, Lesen ist 404.
		e.setze(t, wissen.KeyDir, e.dir)
		if code, _ := e.anfrage(t, http.MethodPost, "/admin/config/wissen/test", "admin@x.de", nil); code == 200 {
			t.Errorf("%s: Prüfen-Knopf %d", fall, code)
		}
		if code, m := e.anfrage(t, http.MethodPut, "/admin/config/"+wissen.KeyEnabled, "admin@x.de", map[string]any{"value": "true"}); code != 400 {
			t.Errorf("%s: Einschalten %d %v", fall, code, m)
		}
		e.setze(t, wissen.KeyEnabled, "true")
		if code, _ := e.get(t, "/wissen/index", "anna@x.de"); code != http.StatusNotFound {
			t.Errorf("%s: Lesen %d", fall, code)
		}
		e.wissen.Vergiss()
	}
}

// Phase 2b Sprint-87-Abschluss W-1 (F-1909): Ist der Client weg (Kontext
// des Requests beendet), schreibt handleSuche nichts — kein 503 „ordner
// nicht lesbar“ samt Fehlerlog für einen gewöhnlichen Abbruch.
func TestHandleSucheAbbruch(t *testing.T) {
	e := newWissenEnv(t)
	e.einschalten(t)
	// Abbruch nach der Zugangsprüfung (die liest mit dem Request-Kontext aus
	// der DB) und vor der Suche: über die Uhr der Such-Bremse, die genau
	// dazwischen gefragt wird.
	ctx, abbrechen := context.WithCancel(context.Background())
	defer abbrechen()
	e.wissen.suchBremse.now = func() time.Time { abbrechen(); return time.Now() }
	r := httptest.NewRequest(http.MethodGet, "/wissen/suche?q=start", nil).WithContext(ctx)
	r.Header.Set("Remote-Email", "anna@x.de")
	r.Header.Set("Remote-User", "anna@x.de")
	rec := httptest.NewRecorder()
	e.srv.Config.Handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("abgebrochene Suche: %d %q, erwartet nichts geschrieben", rec.Code, rec.Body.String())
	}
}
