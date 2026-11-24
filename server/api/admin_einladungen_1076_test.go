package api_test

// Karte #1076: Einladungen über die Admin-API (ADR-0149 §5.3). Testdaten nur
// example.org.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	mailpkg "github.com/Andiministrator/aBoard/server/mail"
	"github.com/Andiministrator/aBoard/server/storage"
)

type mailSpion struct {
	mu     sync.Mutex
	mails  []mailpkg.Message
	fehler error
}

func (m *mailSpion) senden(_ context.Context, _ mailpkg.Config, msg mailpkg.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mails = append(m.mails, msg)
	return m.fehler
}

type einladungAntwort struct {
	Ergebnisse []struct {
		Email, KontoID, Link, Mail, Fehler string
		Erneut                             bool
	} `json:"ergebnisse"`
}

func (e *einladungAntwort) UnmarshalJSON(b []byte) error {
	var roh struct {
		Ergebnisse []map[string]any `json:"ergebnisse"`
	}
	if err := json.Unmarshal(b, &roh); err != nil {
		return err
	}
	for _, r := range roh.Ergebnisse {
		s := func(k string) string { v, _ := r[k].(string); return v }
		erneut, _ := r["erneut"].(bool)
		e.Ergebnisse = append(e.Ergebnisse, struct {
			Email, KontoID, Link, Mail, Fehler string
			Erneut                             bool
		}{s("email"), s("konto_id"), s("link"), s("mail"), s("fehler"), erneut})
	}
	return nil
}

func einladungEnv(t *testing.T, mitMail bool) (*adminTestEnv, *mailSpion) {
	t.Helper()
	e := newAdminTestEnv(t, true)
	t.Cleanup(e.close)
	e.seedUser(t, "u-admin", "admin@example.org", true)
	e.seedUser(t, "u-ben", "ben@example.org", false)
	spion := &mailSpion{}
	e.admin.WithEinladungen(storage.NewEinladungRepo(e.db), "https://board.example.org/").MitEinladungVersand(spion.senden)
	if mitMail {
		ctx := context.Background()
		for k, v := range map[string]string{"smtp.host": "smtp.example.org", "smtp.from": "board@example.org", "smtp.user": "board"} {
			if err := e.cfg.Set(ctx, k, v, false, "u-admin"); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.cfg.Set(ctx, "smtp.pass", "geheim", true, "u-admin"); err != nil {
			t.Fatal(err)
		}
	}
	return e, spion
}

func einladen(t *testing.T, e *adminTestEnv, wer string, emails ...string) (int, einladungAntwort) {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/admin/einladungen", wer, map[string]any{"emails": emails})
	defer resp.Body.Close()
	var a einladungAntwort
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, a
}

func TestEinladungAPI1076_EinladenMitMail(t *testing.T) {
	e, spion := einladungEnv(t, true)
	code, a := einladen(t, e, "admin@example.org", "Anna@Example.org", "ben@example.org", "kein-adresse", "anna@example.org")
	if code != http.StatusOK || len(a.Ergebnisse) != 3 {
		t.Fatalf("%d %+v", code, a)
	}
	anna, ben, falsch := a.Ergebnisse[0], a.Ergebnisse[1], a.Ergebnisse[2]
	if anna.Email != "anna@example.org" || anna.Fehler != "" || anna.Mail != "gesendet" ||
		!strings.HasPrefix(anna.Link, "https://board.example.org/einladung/") || len(anna.Link) != len("https://board.example.org/einladung/")+43 {
		t.Errorf("anna: %+v", anna)
	}
	if ben.Fehler != "hat_zugang" || ben.Link != "" || falsch.Fehler != "adresse" {
		t.Errorf("ben %+v, falsch %+v", ben, falsch)
	}
	// Genau eine Mail, an Anna, mit dem Link; keine Board-Inhalte.
	if len(spion.mails) != 1 || spion.mails[0].To[0] != "anna@example.org" || !strings.Contains(spion.mails[0].BodyText, anna.Link) {
		t.Fatalf("Mails: %+v", spion.mails)
	}
	// Keine Admin-Rolle, keine users-Zeile.
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'anna@example.org'`).Scan(&n)
	if n != 0 {
		t.Fatal("users-Zeile durch Einladung")
	}
	// Audit ohne Adresse.
	var key, aktion string
	if err := e.db.QueryRow(`SELECT key, action FROM server_config_audit WHERE action LIKE 'einladung%'`).Scan(&key, &aktion); err != nil ||
		key != "konten/"+anna.KontoID || aktion != api.AuditActionEinladung {
		t.Fatalf("Audit: %q %q %v", key, aktion, err)
	}
	// Erneut: neuer Link, erneut=true.
	_, a2 := einladen(t, e, "admin@example.org", "anna@example.org")
	if len(a2.Ergebnisse) != 1 || !a2.Ergebnisse[0].Erneut || a2.Ergebnisse[0].Link == anna.Link || a2.Ergebnisse[0].KontoID != anna.KontoID {
		t.Fatalf("erneut: %+v", a2)
	}
}

func TestEinladungAPI1076_OhneMailUndMailFehler(t *testing.T) {
	e, spion := einladungEnv(t, false)
	_, a := einladen(t, e, "admin@example.org", "anna@example.org")
	if a.Ergebnisse[0].Mail != "nicht_eingerichtet" || a.Ergebnisse[0].Link == "" || len(spion.mails) != 0 {
		t.Fatalf("ohne SMTP: %+v", a)
	}
	e2, spion2 := einladungEnv(t, true)
	spion2.fehler = errors.New("smtp weg")
	_, a = einladen(t, e2, "admin@example.org", "anna@example.org")
	if a.Ergebnisse[0].Mail != "fehlgeschlagen" || a.Ergebnisse[0].Link == "" {
		t.Fatalf("Mailfehler: %+v", a)
	}
}

func TestEinladungAPI1076_Rechte(t *testing.T) {
	e, _ := einladungEnv(t, false)
	if code, _ := einladen(t, e, "ben@example.org", "anna@example.org"); code != http.StatusForbidden {
		t.Fatalf("Nicht-Admin: %d", code)
	}
	resp := e.do(t, http.MethodGet, "/admin/konten", "ben@example.org", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Liste Nicht-Admin: %d", resp.StatusCode)
	}
	// Grenzen des Sammelwegs.
	if code, _ := einladen(t, e, "admin@example.org"); code != http.StatusBadRequest {
		t.Fatalf("leer: %d", code)
	}
	viele := make([]string, 51)
	for i := range viele {
		viele[i] = "x@example.org"
	}
	if code, _ := einladen(t, e, "admin@example.org", viele...); code != http.StatusBadRequest {
		t.Fatalf("51: %d", code)
	}
	resp = e.do(t, http.MethodPost, "/admin/einladungen", "admin@example.org", map[string]any{"emails": []string{"a@example.org"}, "admin": true})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unbekanntes Feld: %d", resp.StatusCode)
	}
}

func TestEinladungAPI1076_ListeUndZurueck(t *testing.T) {
	e, _ := einladungEnv(t, false)
	_, a := einladen(t, e, "admin@example.org", "anna@example.org")
	id := a.Ergebnisse[0].KontoID
	resp := e.do(t, http.MethodGet, "/admin/konten", "admin@example.org", nil)
	var l struct {
		Konten []storage.KontoUebersicht `json:"konten"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&l)
	resp.Body.Close()
	if len(l.Konten) != 1 || l.Konten[0].ID != id || l.Konten[0].Zustand != storage.KontoEingeladen || l.Konten[0].LinkAbgelaufen {
		t.Fatalf("Liste: %+v", l)
	}
	resp = e.do(t, http.MethodDelete, "/admin/einladungen/"+id, "admin@example.org", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("zurück: %d", resp.StatusCode)
	}
	resp = e.do(t, http.MethodDelete, "/admin/einladungen/"+id, "admin@example.org", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("zweimal: %d", resp.StatusCode)
	}
}

// Ohne eingebaute Anmeldung (WithEinladungen nicht aufgerufen): 404.
func TestEinladungAPI1076_NurEingebaut(t *testing.T) {
	e := newAdminTestEnv(t, false)
	defer e.close()
	e.seedUser(t, "u-admin", "admin@example.org", true)
	for _, f := range []struct{ m, p string }{{http.MethodPost, "/admin/einladungen"}, {http.MethodGet, "/admin/konten"}, {http.MethodDelete, "/admin/einladungen/x"}} {
		resp := e.do(t, f.m, f.p, "admin@example.org", map[string]any{"emails": []string{"a@example.org"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: %d", f.m, f.p, resp.StatusCode)
		}
	}
}

// F-2890: Hängt der Mailserver, kommt die Antwort mit allen Links trotzdem
// rechtzeitig — die Mails laufen parallel unter einer gemeinsamen Frist.
func TestEinladungAPI1092_LangsamerMailserver(t *testing.T) {
	e, spion := einladungEnv(t, true)
	e.admin.MitEinladungMailFrist(300 * time.Millisecond).MitEinladungVersand(func(ctx context.Context, cfg mailpkg.Config, msg mailpkg.Message) error {
		<-ctx.Done()
		_ = spion.senden(ctx, cfg, msg)
		return ctx.Err()
	})
	adressen := make([]string, 12)
	for i := range adressen {
		adressen[i] = fmt.Sprintf("p%d@example.org", i)
	}
	start := time.Now()
	code, a := einladen(t, e, "admin@example.org", adressen...)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Antwort nach %v", d)
	}
	if code != http.StatusOK || len(a.Ergebnisse) != 12 {
		t.Fatalf("%d %d", code, len(a.Ergebnisse))
	}
	for _, r := range a.Ergebnisse {
		if r.Link == "" || r.Mail != "fehlgeschlagen" {
			t.Errorf("%+v", r)
		}
	}
}

// Verify N3: Die Mails laufen wirklich parallel — zwölf Mails à 150 ms passen
// nur mit mehreren Plätzen in die Frist von 1 s.
func TestEinladungAPI1092_MailsParallel(t *testing.T) {
	e, spion := einladungEnv(t, true)
	e.admin.MitEinladungMailFrist(time.Second).MitEinladungVersand(func(ctx context.Context, cfg mailpkg.Config, msg mailpkg.Message) error {
		select {
		case <-time.After(150 * time.Millisecond):
			return spion.senden(ctx, cfg, msg)
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	adressen := make([]string, 12)
	for i := range adressen {
		adressen[i] = fmt.Sprintf("q%d@example.org", i)
	}
	_, a := einladen(t, e, "admin@example.org", adressen...)
	for _, r := range a.Ergebnisse {
		if r.Mail != "gesendet" {
			t.Errorf("%+v", r)
		}
	}
}
