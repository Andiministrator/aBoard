package api_test

// mitglieder_699_test.go — GET /boards/{id}/mitglieder (Karte #699).
// Namen der Personen des Boards für alle Rollen, aber nie Adressen (F-261).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// mitgliederAbruf ruft den Endpunkt als Person mit Anzeigenamen (Remote-Name).
func mitgliederAbruf(t *testing.T, env *aclTestEnv, board, email, name string) (int, []string, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, env.srv.URL+"/boards/"+board+"/mitglieder", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	r.Header.Set("Remote-Email", email)
	r.Header.Set("Remote-User", email)
	if name != "" {
		r.Header.Set("Remote-Name", name)
	}
	resp := mustDo(t, r)
	defer resp.Body.Close()
	roh := mustReadAll(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, string(roh)
	}
	var out struct {
		Namen []string `json:"namen"`
	}
	if err := json.Unmarshal(roh, &out); err != nil {
		t.Fatalf("json: %v (%s)", err, roh)
	}
	return resp.StatusCode, out.Namen, string(roh)
}

func nutzerMitNamen(t *testing.T, env *aclTestEnv, email, name string) {
	t.Helper()
	if _, err := env.users.Upsert(context.Background(), "id-"+email, email, name); err != nil {
		t.Fatalf("Upsert %s: %v", email, err)
	}
}

// Viewer und Editor bekommen die Namen — die ACL-Liste bleibt ihnen verwehrt.
func TestMitglieder699_ViewerUndEditorSehenNamenOhneAdressen(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)
	_ = env.acl.Set(ctx, "B", "editor@example.com", storage.RoleEditor)
	_ = env.acl.Set(ctx, "B", "eingeladen@example.com", storage.RoleViewer) // nie angemeldet
	nutzerMitNamen(t, env, "owner@example.com", "Olga Owner")
	nutzerMitNamen(t, env, "editor@example.com", "Erik Editor")

	for _, wer := range []struct{ email, name string }{
		{"viewer@example.com", "Vera Viewer"},
		{"editor@example.com", "Erik Editor"},
		{"owner@example.com", "Olga Owner"},
	} {
		code, namen, roh := mitgliederAbruf(t, env, "B", wer.email, wer.name)
		if code != http.StatusOK {
			t.Fatalf("%s: Status %d (%s)", wer.email, code, roh)
		}
		want := []string{"Erik Editor", "Olga Owner", "Vera Viewer"}
		if strings.Join(namen, "|") != strings.Join(want, "|") {
			t.Fatalf("%s: Namen %v, erwartet %v", wer.email, namen, want)
		}
		if strings.Contains(roh, "@") {
			t.Fatalf("%s: Antwort enthält eine Adresse: %s", wer.email, roh)
		}
		if strings.Contains(roh, "editor\"") || strings.Contains(roh, "role") {
			t.Fatalf("%s: Antwort verrät Rollen: %s", wer.email, roh)
		}
	}
	// Gegenprobe F-261: die ACL-Liste selbst bleibt für den Viewer zu.
	resp := env.do(t, http.MethodGet, "/boards/B/acl", "viewer@example.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET /acl = %d, erwartet weiterhin 403", resp.StatusCode)
	}
}

// Ein Name, der selbst eine Adresse ist (AnzeigeName-Rückfall), geht nie raus.
func TestMitglieder699_NameAlsAdresseWirdVerworfen(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "ohnename@example.com", storage.RoleEditor)
	nutzerMitNamen(t, env, "ohnename@example.com", "ohnename@example.com")

	code, namen, roh := mitgliederAbruf(t, env, "B", "owner@example.com", "Olga Owner")
	if code != http.StatusOK {
		t.Fatalf("Status %d (%s)", code, roh)
	}
	if strings.Join(namen, "|") != "Olga Owner" || strings.Contains(roh, "@") {
		t.Fatalf("Namen %v / roh %s — Adresse darf nicht erscheinen", namen, roh)
	}
}

// Ohne Rolle am Board: 403, keine Namen.
func TestMitglieder699_OhneRolle403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	env.seedBoard(t, "C")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "C", "fremd@example.com", storage.RoleOwner)

	code, _, roh := mitgliederAbruf(t, env, "B", "fremd@example.com", "Fritz Fremd")
	if code != http.StatusForbidden {
		t.Fatalf("Fremder: Status %d, erwartet 403 (%s)", code, roh)
	}
	if strings.Contains(roh, "Olga") {
		t.Fatalf("403-Antwort verrät Namen: %s", roh)
	}
}

// Domain-Freigabe (ADR-0132): Personen der erlaubten Domain mit Konto zählen,
// fremde Domains nicht; ohne Erlaubnis wirkt der Domain-Eintrag gar nicht.
func TestMitglieder699_DomainFreigabe(t *testing.T) {
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	if err := env.acl.Set(ctx, "B", "@firma.de", storage.RoleEditor); err != nil {
		t.Fatalf("Domain-Eintrag: %v", err)
	}
	nutzerMitNamen(t, env, "owner@example.com", "Olga Owner")
	nutzerMitNamen(t, env, "anna@firma.de", "Anna Firma")
	nutzerMitNamen(t, env, "bernd@firma.de", "Bernd Firma")
	nutzerMitNamen(t, env, "carl@nichtfirma.de", "Carl Anders") // anderes Postfach-Ende
	nutzerMitNamen(t, env, "dora@sub.firma.de", "Dora Sub")     // Subdomain zählt nicht (ADR-0132)

	code, namen, roh := mitgliederAbruf(t, env, "B", "anna@firma.de", "Anna Firma")
	if code != http.StatusOK {
		t.Fatalf("Status %d (%s)", code, roh)
	}
	want := "Anna Firma|Bernd Firma|Olga Owner"
	if strings.Join(namen, "|") != want {
		t.Fatalf("Namen %v, erwartet %s", namen, want)
	}

	// Domain aus der Erlaubnis genommen → Eintrag wirkungslos, Firmen-Personen fallen raus.
	t.Setenv(storage.ACLDomainsEnv, "")
	code, namen, roh = mitgliederAbruf(t, env, "B", "owner@example.com", "Olga Owner")
	if code != http.StatusOK || strings.Join(namen, "|") != "Olga Owner" {
		t.Fatalf("ohne Erlaubnis: %d %v (%s)", code, namen, roh)
	}
}

// F-738-Nebenbedingung: Kein Endpunkt darf User-IDs ausliefern — sonst ist die
// Begründung in api/notifications.go hinfällig. /mitglieder zählt fremde
// Nutzer auf (nach /known-users der zweite solche Endpunkt) und steht deshalb
// unter derselben Wache wie TestF738_KeinEndpunktLiefertUserIDsAus.
func TestMitglieder699_F738_KeineUserIDs(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	const ownerID, fremdeID = "uid-owner-nur-intern", "uid-4a2d8c50-fremd"
	if _, err := env.users.Upsert(ctx, ownerID, "owner@example.com", "Olga Owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.users.Upsert(ctx, fremdeID, "fremd@example.com", "Fremder"); err != nil {
		t.Fatal(err)
	}
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "fremd@example.com", storage.RoleViewer)

	code, namen, roh := mitgliederAbruf(t, env, "B", "fremd@example.com", "Fremder")
	if code != http.StatusOK || len(namen) != 2 {
		t.Fatalf("Status %d, Namen %v (%s)", code, namen, roh)
	}
	for _, id := range []string{ownerID, fremdeID} {
		if strings.Contains(roh, id) {
			t.Errorf("GET /mitglieder enthält die User-ID %q — F-738-Begründung hinfällig: %s", id, roh)
		}
	}
}

// KS-5: Doubletten ohne Gross-/Kleinschreibung fallen zusammen.
func TestMitglieder699_DoublettenOhneGrossKlein(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "anna1@example.com", storage.RoleEditor)
	_ = env.acl.Set(ctx, "B", "anna2@example.com", storage.RoleViewer)
	nutzerMitNamen(t, env, "anna1@example.com", "Anna Müller")
	nutzerMitNamen(t, env, "anna2@example.com", "anna müller")

	code, namen, roh := mitgliederAbruf(t, env, "B", "owner@example.com", "Olga Owner")
	if code != http.StatusOK || len(namen) != 2 {
		t.Fatalf("Status %d, Namen %v — „Anna Müller“ und „anna müller“ müssen zu einem Eintrag werden (%s)", code, namen, roh)
	}
}

// KS-1/KS-5: Grenze 500 mit gekappt-Kennzeichen, sortiert und reproduzierbar;
// Adress-Namen verdrängen vor dem LIMIT keine echten Namen.
func TestMitglieder699_GrenzeUndAdressNamenVorDemLimit(t *testing.T) {
	t.Setenv(storage.ACLDomainsEnv, "gross.de,klein.de")
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "G")
	env.seedBoard(t, "K")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "G", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "K", "owner@example.com", storage.RoleOwner)
	if err := env.acl.Set(ctx, "G", "@gross.de", storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := env.acl.Set(ctx, "K", "@klein.de", storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	nutzerMitNamen(t, env, "owner@example.com", "Olga Owner")
	for i := 0; i < 600; i++ {
		e := fmt.Sprintf("p%03d@gross.de", i)
		nutzerMitNamen(t, env, e, fmt.Sprintf("Person %03d", i))
	}
	// klein.de: 550 Konten ohne Anzeigenamen (Name = Adresse) und 30 mit Namen.
	for i := 0; i < 550; i++ {
		e := fmt.Sprintf("a%03d@klein.de", i)
		nutzerMitNamen(t, env, e, e)
	}
	for i := 0; i < 30; i++ {
		nutzerMitNamen(t, env, fmt.Sprintf("n%03d@klein.de", i), fmt.Sprintf("Name %03d", i))
	}

	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/boards/G/mitglieder", nil)
	r.Header.Set("Remote-Email", "owner@example.com")
	r.Header.Set("Remote-User", "owner@example.com")
	r.Header.Set("Remote-Name", "Olga Owner")
	resp := mustDo(t, r)
	var g struct {
		Namen   []string `json:"namen"`
		Gekappt bool     `json:"gekappt"`
	}
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &g); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(g.Namen) != storage.MaxMitgliederNamen || !g.Gekappt {
		t.Fatalf("grosse Domain: %d Namen, gekappt=%v — erwartet %d und gekappt", len(g.Namen), g.Gekappt, storage.MaxMitgliederNamen)
	}
	if g.Namen[0] != "Olga Owner" || g.Namen[1] != "Person 000" {
		t.Fatalf("nicht sortiert/reproduzierbar: %v", g.Namen[:3])
	}

	code, namen, roh := mitgliederAbruf(t, env, "K", "owner@example.com", "Olga Owner")
	if code != http.StatusOK || len(namen) != 31 || strings.Contains(roh, `"gekappt":true`) {
		t.Fatalf("kleine Domain: Status %d, %d Namen (%s) — die 30 echten Namen + Owner müssen trotz 550 Adress-Namen kommen", code, len(namen), roh[:min(len(roh), 200)])
	}
}

// KS-6: Die Rollenprüfung im Handler trägt auch ohne den ACL-Guard davor.
func TestMitglieder699_HandlerPrueftRolleOhneGuard(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	if _, err := boards.Insert(ctx, "B", "Board B", "2", sampleBody("B", "Board B")); err != nil {
		t.Fatal(err)
	}
	_ = acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(acl).Register(mux)
	srv := httptest.NewServer(api.WithForwardAuthIfEnabled(mux)) // bewusst OHNE ACLGuard
	defer srv.Close()

	r, _ := http.NewRequest(http.MethodGet, srv.URL+"/boards/B/mitglieder", nil)
	r.Header.Set("Remote-Email", "fremd@example.com")
	r.Header.Set("Remote-User", "fremd@example.com")
	resp := mustDo(t, r)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ohne Guard: Status %d, erwartet 403 aus dem Handler", resp.StatusCode)
	}
}

// Legacy-Bypass (keine Identität, ADR-0059 §2): wie aclList operator-
// kontrolliert — Namen kommen, Adressen nie.
func TestMitglieder699_LegacyBypass(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	nutzerMitNamen(t, env, "owner@example.com", "Olga Owner")
	resp := env.do(t, http.MethodGet, "/boards/B/mitglieder", "", nil)
	roh := string(mustReadAll(t, resp.Body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(roh, "Olga Owner") || strings.Contains(roh, "@") {
		t.Fatalf("Legacy: %d %s", resp.StatusCode, roh)
	}
}

// VS-2/VS-4: Summe zweier Domains über 500 (keine Abfrage allein voll) meldet
// gekappt; die Auswahl hängt nicht an der Einfügereihenfolge (rückwärts angelegt).
func TestMitglieder699_SummeUeberGrenzeUndReihenfolge(t *testing.T) {
	t.Setenv(storage.ACLDomainsEnv, "eins.de,zwei.de")
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "@eins.de", storage.RoleViewer)
	_ = env.acl.Set(ctx, "B", "@zwei.de", storage.RoleViewer)
	nutzerMitNamen(t, env, "owner@example.com", "Olga Owner")
	for i := 299; i >= 0; i-- { // rückwärts: ohne ORDER BY käme „Eins 299“ zuerst
		nutzerMitNamen(t, env, fmt.Sprintf("e%03d@eins.de", i), fmt.Sprintf("Eins %03d", i))
		nutzerMitNamen(t, env, fmt.Sprintf("z%03d@zwei.de", i), fmt.Sprintf("Zwei %03d", i))
	}
	nutzerMitNamen(t, env, "leer@eins.de", "   ")

	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/boards/B/mitglieder", nil)
	r.Header.Set("Remote-Email", "owner@example.com")
	r.Header.Set("Remote-User", "owner@example.com")
	r.Header.Set("Remote-Name", "Olga Owner")
	resp := mustDo(t, r)
	var g struct {
		Namen   []string `json:"namen"`
		Gekappt bool     `json:"gekappt"`
	}
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &g); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(g.Namen) != storage.MaxMitgliederNamen || !g.Gekappt {
		t.Fatalf("Summe: %d Namen, gekappt=%v — erwartet %d und gekappt", len(g.Namen), g.Gekappt, storage.MaxMitgliederNamen)
	}
	if g.Namen[0] != "Eins 000" {
		t.Fatalf("Reihenfolge hängt am Einfügen: erster Name %q", g.Namen[0])
	}
	for _, n := range g.Namen {
		if strings.TrimSpace(n) == "" {
			t.Fatal("Name aus Leerzeichen in der Antwort")
		}
	}
}

// VS-4/VS-6 am Limit: Eine Domain mit mehr Konten als die Abfrage holt.
// Rückwärts angelegt (ohne ORDER BY kämen die höchsten Nummern) und mit
// 501 Namen aus Leerzeichen, die ohne trim-Filter das Limit füllen würden.
func TestMitglieder699_SortierungUndLeerzeichenAmLimit(t *testing.T) {
	t.Setenv(storage.ACLDomainsEnv, "voll.de")
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "@voll.de", storage.RoleViewer)
	nutzerMitNamen(t, env, "owner@example.com", "Olga Owner")
	for i := 0; i < 501; i++ {
		nutzerMitNamen(t, env, fmt.Sprintf("blank%03d@voll.de", i), "   ")
	}
	for i := 699; i >= 0; i-- {
		nutzerMitNamen(t, env, fmt.Sprintf("v%03d@voll.de", i), fmt.Sprintf("Voll %03d", i))
	}
	code, namen, roh := mitgliederAbruf(t, env, "B", "owner@example.com", "Olga Owner")
	if code != http.StatusOK || len(namen) < 2 {
		t.Fatalf("Status %d, %d Namen — Leerzeichen-Namen haben das Limit gefüllt? %s", code, len(namen), roh[:min(len(roh), 200)])
	}
	if namen[0] != "Olga Owner" || namen[1] != "Voll 000" {
		t.Fatalf("Auswahl am Limit hängt an der Einfügereihenfolge: %v", namen[:3])
	}
}
