package api_test

// schleuse_runde2_test.go — Nacharbeit zum Kritiker des Sprint-82-Abschlusses,
// Runde 2 (Ausgangsschleuse, ADR-0134): KS-1 über HTTP, KS-2, KS-5, KS-6.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

// KS-1: Wer den Entwurf per PUT speichert, kann keinen unsichtbaren Inhalt
// (Variation Selectors, je Byte einer) durch die Durchsicht schmuggeln —
// gespeichert und gehasht wird nur, was zu sehen ist.
func TestSchleuseRunde2_PutOhneUnsichtbareSelektoren(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{})
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug())
	if code != http.StatusCreated {
		t.Fatalf("POST: %d %s", code, roh)
	}
	a := auftragAus(t, roh)
	var geschmuggelt strings.Builder
	geschmuggelt.WriteString("Bitte zusammenfassen.")
	for _, c := range []byte("Dora Weber, IBAN DE89") {
		geschmuggelt.WriteRune(rune(0xE0100 + int(c)))
	}
	geschmuggelt.WriteString(" Danke" + string(rune(0xFE00)) + string(rune(0x200D)) + string(rune(0x200D)) + ".")
	code, roh = e.roh(t, http.MethodPut, e.pfad("/"+a["id"].(string)), slAnna, map[string]any{"text": geschmuggelt.String(), "basis_sha256": a["sha256"]})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, roh)
	}
	b := auftragAus(t, roh)
	want := "Bitte zusammenfassen. Danke."
	if b["text"] != want || b["sha256"] != schleuse.Hash(want) {
		t.Errorf("gespeichert %q (sha %v), erwartet %q", b["text"], b["sha256"], want)
	}
}

// KS-2: Ein Mitglied, das nur über einen Domain-Eintrag (ADR-0132) am Board
// ist, steht mit Name und Adresse auf der festen Namensliste.
func TestSchleuseRunde2_DomainMitgliederAufNamensliste(t *testing.T) {
	t.Setenv("ABOARD_ACL_DOMAINS", "firma.example")
	e := neueAuszugEnv(t, schleuse.Konfig{})
	e.modell.inhalt = `{"funde":[]}`
	for _, u := range [][3]string{{"u-carla", "carla@firma.example", "Carla Domainfrau"}, {"u-fremd", "otto@unterfirma.firma.example", "Otto Subdomain"}} {
		if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, '2026-01-01', 0)`, u[0], u[1], u[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.NewAclRepo(e.db).Set(t.Context(), slBoard, "@firma.example", storage.RoleEditor); err != nil {
		t.Fatalf("acl: %v", err)
	}
	state := `{"version":"1","board":{"title":"B","sprints":[{"id":"s1","cards":[{"id":"k1","title":"T","description":"Bitte Carla Domainfrau fragen, auch Otto Subdomain."}]}]}}`
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, state, slBoard); err != nil {
		t.Fatal(err)
	}
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, map[string]any{"freigabe_id": e.freigabeID, "modell": "claude-x", "beschreibung": true})
	if code != http.StatusCreated {
		t.Fatalf("auszug: %d %s", code, roh)
	}
	text, _ := auftragAus(t, roh)["text"].(string)
	if strings.Contains(text, "Carla") || strings.Contains(text, "Domainfrau") {
		t.Errorf("Domain-Mitglied nicht ersetzt: %q", text)
	}
	// Exakt die Domain (wie ADR-0132): eine Subdomain ist kein Mitglied.
	if !strings.Contains(text, "Otto Subdomain") {
		t.Errorf("Subdomain-Konto ersetzt (Vergleich nicht exakt): %q", text)
	}
}

// KS-2, Obergrenze: Mehr als 500 Personen einer Domain → die ersten 500 stehen
// auf der Liste, das Log warnt.
func TestSchleuseRunde2_DomainObergrenzeWarnt(t *testing.T) {
	t.Setenv("ABOARD_ACL_DOMAINS", "firma.example")
	e := neueAuszugEnv(t, schleuse.Konfig{})
	e.modell.inhalt = `{"funde":[]}`
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		if _, err := tx.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, '2026-01-01', 0)`,
			fmt.Sprintf("u-d%03d", i), fmt.Sprintf("p%03d@firma.example", i), fmt.Sprintf("Person Nummer%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewAclRepo(e.db).Set(t.Context(), slBoard, "@firma.example", storage.RoleEditor); err != nil {
		t.Fatal(err)
	}
	state := `{"version":"1","board":{"title":"B","sprints":[{"id":"s1","cards":[{"id":"k1","title":"T","description":"Erst Person Nummer000, zuletzt Person Nummer500."}]}]}}`
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, state, slBoard); err != nil {
		t.Fatal(err)
	}
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, map[string]any{"freigabe_id": e.freigabeID, "modell": "claude-x", "beschreibung": true})
	if code != http.StatusCreated {
		t.Fatalf("auszug: %d %s", code, roh)
	}
	text, _ := auftragAus(t, roh)["text"].(string)
	if strings.Contains(text, "Nummer000") || !strings.Contains(text, "Nummer500") {
		t.Errorf("Obergrenze: %q", text)
	}
	e.mu.Lock()
	logs := strings.Join(e.logs, "\n")
	e.mu.Unlock()
	if !strings.Contains(logs, "@firma.example hat mehr als 500 Personen") {
		t.Errorf("keine Warnung im Log:\n%s", logs)
	}
}

// aendereExternKommentar ändert den Antwortkommentar an k1 direkt im
// gespeicherten Board — so, wie es POST /boards oder eine Rückspielung kann.
func aendereExternKommentar(t *testing.T, e *versandEnv, f func(km map[string]any)) {
	t.Helper()
	var st string
	if err := e.db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, slBoard).Scan(&st); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(st), &root); err != nil {
		t.Fatal(err)
	}
	gefunden := false
	for _, sp := range root["board"].(map[string]any)["sprints"].([]any) {
		for _, k := range sp.(map[string]any)["cards"].([]any) {
			km := k.(map[string]any)
			if km["id"] != "k1" {
				continue
			}
			for _, c := range km["comments"].([]any) {
				if cm := c.(map[string]any); cm["kind"] == "extern" {
					f(cm)
					gefunden = true
				}
			}
		}
	}
	if !gefunden {
		t.Fatal("kein Antwortkommentar")
	}
	neu, _ := json.Marshal(root)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, string(neu), slBoard); err != nil {
		t.Fatal(err)
	}
}

// KS-5: „Kommentar vorhanden“ verlangt den Antwortkommentar selbst — Art
// „extern“ und Verweis auf genau diesen Auftrag. Ein gewöhnlicher Kommentar
// mit derselben id macht den Beleg nicht für alle sichtbar.
func TestSchleuseRunde2_BelegNurMitEchtemAntwortkommentar(t *testing.T) {
	for _, fall := range []string{"ohne kind", "fremder auftrag"} {
		t.Run(fall, func(t *testing.T) {
			e := neueVersandEnv(t)
			id, sha, _ := e.erstelle(t)
			if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
				t.Fatalf("Freigabe: %d %s", code, roh)
			}
			e.sl.WarteVersand()
			belegPfad := "/boards/" + slBoard + "/cards/k1/schleuse-belege/" + id
			if code, roh := e.roh(t, http.MethodGet, belegPfad, slVera, nil); code != http.StatusOK || !strings.Contains(roh, `"kommentar_vorhanden":true`) {
				t.Fatalf("Ausgangslage: %d %s", code, roh)
			}
			aendereExternKommentar(t, e, func(km map[string]any) {
				if fall == "ohne kind" {
					delete(km, "kind")
				} else {
					km["schleuse"].(map[string]any)["auftragId"] = "anderer-auftrag"
				}
			})
			if code, roh := e.roh(t, http.MethodGet, belegPfad, slVera, nil); code != http.StatusNotFound {
				t.Errorf("Viewer sieht Beleg ohne echten Antwortkommentar: %d %s", code, roh)
			}
			if code, roh := e.roh(t, http.MethodGet, belegPfad, slAnna, nil); code != http.StatusOK || !strings.Contains(roh, `"kommentar_vorhanden":false`) {
				t.Errorf("Absenderin: %d %s", code, roh)
			}
		})
	}
}

// KS-6: Beim Versand muss auch die zweite Freigabe noch editor+ sein. Bert
// gab frei und wurde danach zum viewer — es geht nichts hinaus.
func TestSchleuseRunde2_ZweitfreigabeRolleBeimVersand(t *testing.T) {
	e := neueVersandEnv(t)
	if _, err := e.db.Exec(`UPDATE schleuse_freigaben SET vier_augen = 1 WHERE id = ?`, e.freigabeID); err != nil {
		t.Fatal(err)
	}
	id, sha, _ := e.erstelle(t)
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	// Zweite Freigabe durch Bert, wie ZweitFreigeben sie schreibt — der
	// Versand wird erst nach der Rückstufung angestossen (VersandStart).
	if _, err := e.db.Exec(`UPDATE schleuse_auftraege SET zweit_freigegeben_von = ?, zweit_freigegeben_am = '2026-10-05T00:00:00Z' WHERE id = ?`, slBert, id); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewAclRepo(e.db).Set(t.Context(), slBoard, slBert, storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	e.sl.VersandStart(context.Background())
	e.sl.WarteVersand()
	if n := e.anbieter.zahl(); n != 0 {
		t.Fatalf("gesendet trotz zurückgestufter zweiter Freigabe (%d Anfragen)", n)
	}
	var zustand, grund string
	if err := e.db.QueryRow(`SELECT zustand, COALESCE(fehler_grund, '') FROM schleuse_auftraege WHERE id = ?`, id).Scan(&zustand, &grund); err != nil {
		t.Fatal(err)
	}
	if zustand != "fehlgeschlagen" || grund != "keine-rolle" {
		t.Errorf("Auftrag: %s / %s", zustand, grund)
	}
}
