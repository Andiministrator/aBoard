package main

// Verdrahtung der Anmelde-Weiche im OIDC-Betrieb (ADR-0148 §1, §4; Karte
// #1051). Geprüft wird die echte Kette aus hauptHandler/menschenHandler mit
// echtem Sitzungsspeicher, nicht nur die Middleware.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type sitzungKette struct {
	h           http.Handler
	repo        *storage.SitzungRepo
	guardSah    *[]string
	muxErreicht *[]string
}

func baueSitzungKette(t *testing.T, mitWeiche bool) sitzungKette {
	t.Helper()
	t.Setenv("ABOARD_ANMELDUNG", "oidc")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv("ABOARD_DEV_FAKE_USER", "")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := storage.NewSitzungRepo(db)

	var guardSah, muxErreicht []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		muxErreicht = append(muxErreicht, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	spionACL := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guardSah = append(guardSah, r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
	var opts []menschenOption
	if mitWeiche {
		opts = append(opts, mitSitzungsweiche(&api.Sitzungsweiche{
			Sitzungen:  repo,
			Zugelassen: func(_ context.Context, e string) (bool, error) { return e == "anna@example.com", nil },
		}))
	}
	h := hauptHandler(mux, spionACL, api.NewAgentZugang(storage.NewAgentTokenRepo(db)), true, opts...)
	return sitzungKette{h: h, repo: repo, guardSah: &guardSah, muxErreicht: &muxErreicht}
}

// „Anfrage ohne Sitzung an /boards erreicht den ACLGuard nie“ (ADR-0148 §1).
func TestKette1051_OhneSitzungErreichtDenGuardNie(t *testing.T) {
	k := baueSitzungKette(t, true)
	for _, kopf := range []map[string]string{
		{},
		{"X-Forwarded-Uri": "/healthz"},
		{"X-Original-URL": "/healthz"},
		{"X-Forwarded-Prefix": "/healthz"},
		{"Remote-Email": "anna@example.com", "Remote-User": "anna"},
	} {
		for _, accept := range []string{"text/html", "application/json"} {
			r := httptest.NewRequest(http.MethodGet, "https://board.example/boards", nil)
			r.Header.Set("Accept", accept)
			for kk, v := range kopf {
				r.Header.Set(kk, v)
			}
			rec := httptest.NewRecorder()
			k.h.ServeHTTP(rec, r)
			if rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
				t.Errorf("%v %s: %d, erwartet 302/401", kopf, accept, rec.Code)
			}
		}
	}
	if len(*k.guardSah) != 0 || len(*k.muxErreicht) != 0 {
		t.Fatalf("ohne Sitzung erreicht: Guard %v, Mux %v", *k.guardSah, *k.muxErreicht)
	}
}

// Ohne eingehängte Weiche (vergessene Verdrahtung) kommt im OIDC-Betrieb
// niemand herein — auch nicht über Forward-Auth-Kopfzeilen.
func TestKette1051_OhneWeicheFailClosed(t *testing.T) {
	k := baueSitzungKette(t, false)
	id, _, err := k.repo.Anlegen(context.Background(), "anna@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://board.example/boards", nil)
	r.AddCookie(&http.Cookie{Name: api.SitzungsCookie, Value: id})
	r.Header.Set("Remote-Email", "anna@example.com")
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized || len(*k.guardSah) != 0 {
		t.Fatalf("ohne Weiche: %d, Guard sah %v — erwartet 401 ohne Durchgriff", rec.Code, *k.guardSah)
	}
}

func TestKette1051_GueltigeSitzungKommtDurch(t *testing.T) {
	k := baueSitzungKette(t, true)
	id, _, err := k.repo.Anlegen(context.Background(), "anna@example.com", "Anna")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://board.example/boards", nil)
	r.AddCookie(&http.Cookie{Name: api.SitzungsCookie, Value: id})
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || len(*k.guardSah) != 1 {
		t.Fatalf("gültige Sitzung: %d, Guard sah %v", rec.Code, *k.guardSah)
	}
	// Abmelden über die echte Kette: danach ist die Sitzung weg.
	r = httptest.NewRequest(http.MethodPost, "https://board.example"+api.AnmeldungEndePfad, nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(&http.Cookie{Name: api.SitzungsCookie, Value: id})
	rec = httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Abmelden: %d", rec.Code)
	}
	if _, err := k.repo.Pruefen(context.Background(), id); err == nil {
		t.Fatal("Sitzung nach dem Abmelden noch gültig")
	}
}

// Abmelden ist ein POST: Der CSRF-Schutz liegt aussen und weist einen fremden
// Browser ab, bevor die Sitzung beendet wird.
func TestKette1051_AbmeldenHinterCSRFSchutz(t *testing.T) {
	k := baueSitzungKette(t, true)
	id, _, _ := k.repo.Anlegen(context.Background(), "anna@example.com", "")
	r := httptest.NewRequest(http.MethodPost, "https://board.example"+api.AnmeldungEndePfad, strings.NewReader(""))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(&http.Cookie{Name: api.SitzungsCookie, Value: id})
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("fremdes Abmelden: %d, erwartet 403", rec.Code)
	}
	if _, err := k.repo.Pruefen(context.Background(), id); err != nil {
		t.Fatal("fremder Browser hat die Sitzung beendet")
	}
}

// Die Pfadweiche vor der Anmeldung bleibt: mehrdeutige Agenten-Pfade → 400,
// /agent/ ohne Token → 401 vom Agenten-Zweig, nie die Menschen-Kette.
func TestKette1051_AgentenPfadUnveraendert(t *testing.T) {
	k := baueSitzungKette(t, true)
	for _, p := range []string{"//agent/boards", "/AGENT/boards", "/boards/../agent/x", "/agent"} {
		r := httptest.NewRequest(http.MethodGet, "https://board.example/", nil)
		r.URL.Path = p
		rec := httptest.NewRecorder()
		k.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, erwartet 400", p, rec.Code)
		}
	}
	// `/agent/../boards` beginnt mit /agent/ und gehört wie heute dem
	// Agenten-Zweig (Positivliste lehnt `..` ab, ohne Token 401) — nie der
	// Menschen-Kette.
	for _, p := range []string{"/agent/boards", "/agent/../boards"} {
		r := httptest.NewRequest(http.MethodGet, "https://board.example/", nil)
		r.URL.Path = p
		r.Header.Set("Remote-Email", "anna@example.com")
		rec := httptest.NewRecorder()
		k.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s ohne Token: %d, erwartet 401 aus dem Agenten-Zweig", p, rec.Code)
		}
	}
	if len(*k.guardSah) != 0 {
		t.Fatalf("Agenten-/mehrdeutige Pfade erreichten die Menschen-Kette: %v", *k.guardSah)
	}
}
