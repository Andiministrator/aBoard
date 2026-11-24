package api_test

// Karte #1077: Kontenverwaltung über die Admin-API (ADR-0149 §5.5, Nachtrag
// #1077). Testdaten nur example.org.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type kontenVerwaltung struct {
	*storage.KontoRepo
	*storage.EinladungRepo
}

func kontenEnv(t *testing.T, mitMail bool) (*adminTestEnv, *mailSpion, *[]string) {
	t.Helper()
	e, spion := einladungEnv(t, mitMail)
	geaendert := &[]string{}
	b := storage.NewBootstrapRepo(e.db)
	e.admin.WithKontenVerwaltung(kontenVerwaltung{storage.NewKontoRepo(e.db), storage.NewEinladungRepo(e.db)},
		func(ctx context.Context, email string) error {
			_, err := b.BootstrapNewUser(ctx, uuid.NewString(), uuid.NewString(), email, "")
			return err
		},
		func(email, kontoID string) { *geaendert = append(*geaendert, email+"|"+kontoID) })
	return e, spion, geaendert
}

// eingeloest lädt ein und löst über einen Anbieter ein (Konto pending).
func eingeloest(t *testing.T, e *adminTestEnv, email string) string {
	t.Helper()
	r := storage.NewEinladungRepo(e.db)
	n, err := r.Einladen(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(context.Background(), storage.EinladungTokenHash(n.Token),
		storage.Einloesung{Anbieter: "heim", Iss: "https://id.example.org", Sub: "sub-" + email}); err != nil {
		t.Fatal(err)
	}
	return n.KontoID
}

func kontoAktion(t *testing.T, e *adminTestEnv, wer, id, aktion string) (int, map[string]any) {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/admin/konten/"+id+"/"+aktion, wer, nil)
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAdminKonten1077_Ablauf(t *testing.T) {
	e, _, geaendert := kontenEnv(t, false)
	id := eingeloest(t, e, "anna@example.org")

	if c, _ := kontoAktion(t, e, "ben@example.org", id, "bestaetigen"); c != http.StatusForbidden {
		t.Fatalf("Nicht-Admin: %d", c)
	}
	if c, out := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusOK || out["zustand"] != storage.KontoAktiv {
		t.Fatalf("bestätigen: %d %v", c, out)
	}
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusConflict {
		t.Errorf("zweimal bestätigen: %d", c)
	}
	var admin bool
	if err := e.db.QueryRow(`SELECT is_admin FROM users WHERE email = 'anna@example.org'`).Scan(&admin); err != nil || admin {
		t.Errorf("bestätigte Person: is_admin=%v %v (eine Einladung gibt keine Admin-Rolle, H4)", admin, err)
	}
	if c, out := kontoAktion(t, e, "admin@example.org", id, "sperren"); c != http.StatusOK || out["zustand"] != storage.KontoGesperrt {
		t.Fatalf("sperren: %d %v", c, out)
	}
	// A-2: Entsperren führt nach „wartet“; aktiv erst nach erneutem Bestätigen.
	if c, out := kontoAktion(t, e, "admin@example.org", id, "entsperren"); c != http.StatusOK || out["zustand"] != storage.KontoWartet {
		t.Fatalf("entsperren: %d %v", c, out)
	}
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusOK {
		t.Fatalf("nach Entsperren bestätigen: %d", c)
	}
	if len(*geaendert) != 4 || (*geaendert)[0] != "anna@example.org|"+id {
		t.Errorf("Haken nach Änderung: %v", *geaendert)
	}
	// Audit unter konten/<id>, nie die Adresse.
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM server_config_audit WHERE key = ?`, "konten/"+id).Scan(&n); err != nil || n < 3 {
		t.Errorf("Audit-Einträge: %d %v", n, err)
	}
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "loeschen"); c != http.StatusNotFound {
		t.Errorf("unbekannte Aktion: %d", c)
	}
	if c, _ := kontoAktion(t, e, "admin@example.org", "gibt-es-nicht", "sperren"); c != http.StatusNotFound {
		t.Errorf("unbekanntes Konto: %d", c)
	}
}

func TestAdminKonten1077_Ablehnen(t *testing.T) {
	e, _, _ := kontenEnv(t, false)
	id := eingeloest(t, e, "anna@example.org")
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "ablehnen"); c != http.StatusOK {
		t.Fatalf("ablehnen: %d", c)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM konten WHERE id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Konto nach Ablehnen: %d %v", n, err)
	}
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "ablehnen"); c != http.StatusNotFound {
		t.Errorf("zweimal ablehnen: %d", c)
	}
}

func TestAdminKonten1077_ZuruecksetzenMitLinkUndMail(t *testing.T) {
	e, spion, _ := kontenEnv(t, true)
	id := eingeloest(t, e, "anna@example.org")
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusOK {
		t.Fatal(c)
	}
	c, out := kontoAktion(t, e, "admin@example.org", id, "zuruecksetzen")
	link, _ := out["link"].(string)
	if c != http.StatusOK || out["zustand"] != storage.KontoEingeladen || !strings.HasPrefix(link, "https://board.example.org/einladung/") || out["mail"] != "gesendet" {
		t.Fatalf("zurücksetzen: %d %v", c, out)
	}
	tok := strings.TrimPrefix(link, "https://board.example.org/einladung/")
	o, err := storage.NewEinladungRepo(e.db).Pruefen(context.Background(), tok)
	if err != nil || o.Zweck != storage.EinladungZweckReset {
		t.Fatalf("Link: %+v %v", o, err)
	}
	spion.mu.Lock()
	mails := len(spion.mails)
	enthaelt := mails > 0 && strings.Contains(spion.mails[0].BodyText, link)
	spion.mu.Unlock()
	if mails != 1 || !enthaelt {
		t.Fatalf("Mail: %d", mails)
	}
	// F-2921: Ein zurückgesetztes Konto (invited, Rücksetz-Link) bekommt über
	// Zurücksetzen einen neuen Link; der alte verfällt.
	c2, out2 := kontoAktion(t, e, "admin@example.org", id, "zuruecksetzen")
	link2, _ := out2["link"].(string)
	if c2 != http.StatusOK || link2 == "" || link2 == link {
		t.Errorf("noch eingeladen zurücksetzen: %d %v", c2, out2)
	}
	if _, err := storage.NewEinladungRepo(e.db).Pruefen(context.Background(), tok); err == nil {
		t.Errorf("alter Link gilt nach neuem Link noch")
	}
}

// Das eigene Konto: nicht sperren, nicht zurücksetzen.
func TestAdminKonten1077_EigenesKonto(t *testing.T) {
	e, _, _ := kontenEnv(t, false)
	e.db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-admin', 'admin@example.org', 'active', 1, 1)`)
	for _, a := range []string{"sperren", "zuruecksetzen"} {
		if c, _ := kontoAktion(t, e, "Admin@Example.org", "k-admin", a); c != http.StatusConflict {
			t.Errorf("eigenes Konto %s: %d", a, c)
		}
	}
	if z, _ := storage.NewKontoRepo(e.db).Zustand(context.Background(), "admin@example.org"); z != storage.KontoAktiv {
		t.Errorf("Zustand %q", z)
	}
}

func TestAdminKonten1077_OhneVerwaltung404(t *testing.T) {
	e := newAdminTestEnv(t, true)
	t.Cleanup(e.close)
	e.seedUser(t, "u-admin", "admin@example.org", true)
	if c, _ := kontoAktion(t, e, "admin@example.org", "x", "sperren"); c != http.StatusNotFound {
		t.Fatalf("%d", c)
	}
	_ = api.AuditActionKontoGesperrt
}
