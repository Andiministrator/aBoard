package api_test

// anzeigename_test.go — Karte #577: Der Anzeigename kommt nicht mehr blind aus
// Remote-User. Hinter oauth2-proxy mit Google trägt Remote-User die numerische
// Konto-ID; die Oberfläche zeigte dann eine zwanzigstellige Zahl. Zuordnung
// und ACL laufen weiter über die E-Mail — das prüfen die ACL-Tests.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

const googleKontoID = "112233445566778899001"

func TestAnzeigeName_Reihenfolge(t *testing.T) {
	lang := strings.Repeat("ä", 250)
	faelle := []struct {
		name string
		id   *api.ForwardAuthIdentity
		soll string
	}{
		{"nil", nil, ""},
		{"Remote-Name gewinnt", &api.ForwardAuthIdentity{Name: "Andi P.", User: "andi", Email: "a@b.de"}, "Andi P."},
		{"Remote-User, wenn kein Name", &api.ForwardAuthIdentity{User: "andi", Email: "a@b.de"}, "andi"},
		{"numerische Konto-ID → E-Mail", &api.ForwardAuthIdentity{User: googleKontoID, Email: "a@b.de"}, "a@b.de"},
		{"Ziffern mit Buchstaben sind ein Name", &api.ForwardAuthIdentity{User: "andi2026", Email: "a@b.de"}, "andi2026"},
		{"numerisch ohne E-Mail → besser als nichts", &api.ForwardAuthIdentity{User: googleKontoID}, googleKontoID},
		{"nur E-Mail", &api.ForwardAuthIdentity{Email: "a@b.de"}, "a@b.de"},
		{"Leerraum-Name zählt nicht", &api.ForwardAuthIdentity{Name: "  ", User: googleKontoID, Email: "a@b.de"}, "a@b.de"},
		{"Steuerzeichen entfernt", &api.ForwardAuthIdentity{Name: "An\x00di\x1b"}, "Andi"},
		{"Formatzeichen entfernt (F-1174)", &api.ForwardAuthIdentity{Name: "\u202eAn\u200bdi\u2066"}, "Andi"},
		{"ZWNJ in persischem Namen bleibt (F-1180)", &api.ForwardAuthIdentity{Name: "مهر\u200cناز"}, "مهر\u200cناز"},
		{"ZWJ in Emoji-Sequenz bleibt (F-1180)", &api.ForwardAuthIdentity{Name: "Andi \U0001F468\u200d\U0001F4BB"}, "Andi \U0001F468\u200d\U0001F4BB"},
		{"RLM bleibt, RLO fällt (F-1180)", &api.ForwardAuthIdentity{Name: "\u202eأحمد\u200f Ahmed"}, "أحمد\u200f Ahmed"},
		{"nur erlaubte Formatzeichen → Rückfall (F-1183)", &api.ForwardAuthIdentity{Name: "\u200e\u200c", User: googleKontoID, Email: "a@b.de"}, "a@b.de"},
		{"nur Formatzeichen → Rückfall", &api.ForwardAuthIdentity{Name: "\u202e\u200b", User: googleKontoID, Email: "a@b.de"}, "a@b.de"},
		{"Hangul-Füllzeichen → Rückfall (F-1185)", &api.ForwardAuthIdentity{Name: "\u3164\u3164", Email: "a@b.de"}, "a@b.de"},
		{"alle leeren Glyphen → Rückfall (F-1185)", &api.ForwardAuthIdentity{Name: "\u115f\u1160\uffa0\u2800", User: googleKontoID, Email: "a@b.de"}, "a@b.de"},
		{"nur Variation Selectors → Rückfall (Kritiker Sprint 74)", &api.ForwardAuthIdentity{Name: "\ufe0f\ufe0f\U000e0100", Email: "a@b.de"}, "a@b.de"},
		{"nur CGJ und mongolische Selektoren → Rückfall", &api.ForwardAuthIdentity{Name: "\u034f\u180b\u180f", Email: "a@b.de"}, "a@b.de"},
		{"Emoji mit Variation Selector bleibt", &api.ForwardAuthIdentity{Name: "\u2764\ufe0f"}, "\u2764\ufe0f"},
		{"Füllzeichen im Namen entfernt (F-1185)", &api.ForwardAuthIdentity{Name: "An\u3164di\u2800"}, "Andi"},
		{"auf 200 Runen gekürzt", &api.ForwardAuthIdentity{Name: lang}, strings.Repeat("ä", 200)},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			if ist := f.id.AnzeigeName(); ist != f.soll {
				t.Fatalf("AnzeigeName() = %q, soll %q", ist, f.soll)
			}
		})
	}
}

type anzeigeEnv struct {
	srv   *httptest.Server
	users *storage.UserRepo
}

func newAnzeigeEnv(t *testing.T) *anzeigeEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).WithACL(acl).Register(mux)
	api.RegisterMeHandler(mux, users)
	var h http.Handler = api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &anzeigeEnv{srv: srv, users: users}
}

func (e *anzeigeEnv) get(t *testing.T, pfad string, kopf map[string]string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, e.srv.URL+pfad, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kopf {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *anzeigeEnv) gespeicherterName(t *testing.T, email string) string {
	t.Helper()
	u, err := e.users.GetByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("GetByEmail(%s): %v", email, err)
	}
	return u.Name
}

// Gegenprobe: vor #577 lieferte /me die Konto-ID.
func TestMe_NumerischeKontoIDZeigtEMail(t *testing.T) {
	env := newAnzeigeEnv(t)
	resp := env.get(t, "/me", map[string]string{"Remote-User": googleKontoID, "Remote-Email": "team@example.com"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/me: %d", resp.StatusCode)
	}
	var me map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if me["name"] != "team@example.com" {
		t.Fatalf("/me name = %v, soll die E-Mail statt der Konto-ID", me["name"])
	}
	if me["email"] != "team@example.com" {
		t.Fatalf("/me email = %v", me["email"])
	}
}

// Gegenprobe: vor #577 legte der Erstkontakt die Konto-ID als users.name an.
func TestACLGuard_ErstkontaktSpeichertAnzeigeName(t *testing.T) {
	env := newAnzeigeEnv(t)
	resp := env.get(t, "/boards", map[string]string{"Remote-User": googleKontoID, "Remote-Email": "team@example.com"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards: %d", resp.StatusCode)
	}
	if n := env.gespeicherterName(t, "team@example.com"); n != "team@example.com" {
		t.Fatalf("users.name = %q, soll die E-Mail", n)
	}
}

// Gegenprobe: vor #577 blieb ein einmal gespeicherter Name für immer stehen.
func TestACLGuard_ZiehtBestandsnamenNach(t *testing.T) {
	env := newAnzeigeEnv(t)
	ctx := context.Background()
	if _, err := env.users.Upsert(ctx, "u-alt", "team@example.com", googleKontoID); err != nil {
		t.Fatal(err)
	}
	kopf := map[string]string{"Remote-User": googleKontoID, "Remote-Email": "team@example.com"}
	if resp := env.get(t, "/boards", kopf); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards: %d", resp.StatusCode)
	}
	if n := env.gespeicherterName(t, "team@example.com"); n != "team@example.com" {
		t.Fatalf("Bestandsname = %q, nicht nachgezogen", n)
	}

	// Liefert der Proxy später einen echten Namen, gewinnt er.
	kopf["Remote-Name"] = "Tanja G."
	if resp := env.get(t, "/boards", kopf); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards: %d", resp.StatusCode)
	}
	if n := env.gespeicherterName(t, "team@example.com"); n != "Tanja G." {
		t.Fatalf("Bestandsname = %q, soll Remote-Name", n)
	}
	// Die Zuordnung bleibt dieselbe Zeile.
	u, _ := env.users.GetByEmail(ctx, "team@example.com")
	if u.ID != "u-alt" {
		t.Fatalf("users.id = %q — Nachziehen hat eine neue Zeile angelegt", u.ID)
	}
}
