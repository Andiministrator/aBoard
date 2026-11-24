package api_test

// Karte #735: POST /admin/backup/all-now + GET /admin/backup/all-status.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

// freigeben beendet eine von ReserviereAll gehaltene Sperre über den
// öffentlichen Weg (Lauf über eine leere Board-Liste ist nicht möglich —
// deshalb mit dem echten Repo der Test-Umgebung).
func (e *bbTestEnv) sperreFreigeben() {
	boardbackup.RunAllBoardsReserviert(context.Background(), boardbackup.SchedulerDeps{
		Boards: e.boards, ACL: e.acl, BackupBaseDir: e.bkpDir, KeepDays: 7, Logger: log.Printf,
	})
}

func (e *bbTestEnv) dateienImBackupVerz(t *testing.T) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(e.bkpDir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestBackupAPI_AlleBoards_735_NurAdmin(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedUser(t, "owner@x.de", false)

	for _, c := range []struct{ m, p, mail string }{
		{"POST", "/admin/backup/all-now", "owner@x.de"},
		{"POST", "/admin/backup/all-now", ""},
		{"GET", "/admin/backup/all-status", "owner@x.de"},
	} {
		resp := env.do(t, c.m, c.p, c.mail, nil, "")
		if resp.StatusCode != 403 && !(c.mail == "" && resp.StatusCode == 401) {
			t.Errorf("%s %s (%q): erwartet 403, got %d", c.m, c.p, c.mail, resp.StatusCode)
		}
	}
	if n := env.dateienImBackupVerz(t); n != 0 {
		t.Errorf("Nicht-Admin darf nichts sichern, Dateien: %d", n)
	}
}

func TestBackupAPI_AlleBoards_735_LaufUndStatus(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedBoard(t, "b2", "Zwei", "owner@x.de")
	env.seedUser(t, "adm@x.de", true)

	resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, "")
	if resp.StatusCode != 202 {
		t.Fatalf("erwartet 202, got %d", resp.StatusCode)
	}
	var st struct {
		Running bool `json:"running"`
		Last    *struct {
			Total  int `json:"total"`
			OK     int `json:"ok"`
			Failed int `json:"failed"`
		} `json:"last"`
	}
	for i := 0; i < 100; i++ {
		r := env.do(t, "GET", "/admin/backup/all-status", "adm@x.de", nil, "")
		st.Last = nil
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if !st.Running && st.Last != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.Last == nil || st.Last.Total != 2 || st.Last.OK != 2 || st.Last.Failed != 0 {
		t.Fatalf("Ergebnis erwartet 2/2/0, got %+v", st.Last)
	}
	if n := env.dateienImBackupVerz(t); n != 2 {
		t.Errorf("erwartet 2 Sicherungsdateien, got %d", n)
	}
}

// Doppelstart: Ist die Sperre belegt (laufender Scheduler- oder Knopf-Lauf),
// antwortet der Endpunkt 409 und sichert nichts; ein Scheduler-Aufruf
// (BackupAllBoardsOnce) schreibt ebenfalls nichts.
func TestBackupAPI_AlleBoards_735_Doppelstart409(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedUser(t, "adm@x.de", true)

	if !boardbackup.ReserviereAll() {
		t.Fatal("Sperre sollte frei sein")
	}
	released := false
	defer func() {
		if !released {
			env.sperreFreigeben()
		}
	}()

	resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, "")
	if resp.StatusCode != 409 {
		t.Fatalf("erwartet 409, got %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["error"] == "" {
		t.Errorf("409 braucht einen verständlichen Text")
	}

	boardbackup.BackupAllBoardsOnce(context.Background(), boardbackup.SchedulerDeps{
		Boards: env.boards, ACL: env.acl, BackupBaseDir: env.bkpDir, KeepDays: 7, Logger: log.Printf,
	})
	if n := env.dateienImBackupVerz(t); n != 0 {
		t.Errorf("Scheduler-Lauf bei belegter Sperre darf nichts schreiben, Dateien: %d", n)
	}

	env.sperreFreigeben()
	released = true
	resp = env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, "")
	if resp.StatusCode != 202 {
		t.Errorf("nach Freigabe erwartet 202, got %d", resp.StatusCode)
	}
	// Lauf abwarten, damit er nicht in den nächsten Test hineinläuft.
	for i := 0; i < 100; i++ {
		if l, _ := boardbackup.AllStatus(); !l {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// warteAufLaufEnde wartet, bis kein Alle-Boards-Lauf mehr aktiv ist.
func warteAufLaufEnde(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if laeuft, _ := boardbackup.AllStatus(); !laeuft {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Lauf endet nicht")
}

// K-6b: Beim Start steht der Auslöser (User-ID) im Log, nie die Adresse.
func TestBackupAPI_AlleBoards_735_LogzeileMitAausloeser(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedUser(t, "adm@x.de", true)
	u, err := env.users.GetByEmail(context.Background(), "adm@x.de")
	if err != nil {
		t.Fatal(err)
	}
	buf := logMitschneiden(t)
	if resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, ""); resp.StatusCode != 202 {
		t.Fatalf("erwartet 202, got %d", resp.StatusCode)
	}
	warteAufLaufEnde(t)
	got := buf.String()
	if !strings.Contains(got, "Sicherung über alle Boards ausgelöst") || !strings.Contains(got, u.ID) {
		t.Errorf("Logzeile mit Auslöser fehlt:\n%s", got)
	}
	// Die Test-ID „u-<adresse>“ enthält die Adresse selbst; in Produktion sind IDs Zufallswerte.
	if strings.Contains(strings.ReplaceAll(got, u.ID, "<ID>"), "adm@x.de") {
		t.Errorf("Adresse gehört nicht ins Log:\n%s", got)
	}
}

// K-6a: Rate-Limit wie bei runNow (10/h je User) — der 11. Start wird abgewiesen.
func TestBackupAPI_AlleBoards_735_RateLimit(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedUser(t, "adm@x.de", true)
	defer warteAufLaufEnde(t)
	for i := 1; i <= 10; i++ {
		resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, "")
		if resp.StatusCode == 429 {
			t.Fatalf("Aufruf %d schon limitiert", i)
		}
		warteAufLaufEnde(t)
	}
	if resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, ""); resp.StatusCode != 429 {
		t.Errorf("11. Aufruf: erwartet 429, got %d", resp.StatusCode)
	}
}

// F-1825 (Kritiker/Verify Sprint 85): Abgewiesene Versuche (409, solange ein
// Lauf aktiv ist) verbrauchen kein Kontingent — gezählt werden Läufe.
func TestBackupAPI_AlleBoards_735_409VerbrauchtKeinKontingent(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedUser(t, "adm@x.de", true)
	if !boardbackup.ReserviereAll() {
		t.Fatal("Sperre sollte frei sein")
	}
	for i := 1; i <= 12; i++ {
		if resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, ""); resp.StatusCode != 409 {
			env.sperreFreigeben()
			t.Fatalf("Versuch %d bei belegter Sperre: erwartet 409, got %d", i, resp.StatusCode)
		}
	}
	env.sperreFreigeben()
	defer warteAufLaufEnde(t)
	if resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, ""); resp.StatusCode != 202 {
		t.Errorf("nach zwölf 409 muss ein Start noch gehen: erwartet 202, got %d", resp.StatusCode)
	}
}

// F-1834 (Verify Sprint 85): Auch 503 (Sicherung aus) verbraucht kein
// Kontingent — unabhängig vom 409-Vorabblick belegt.
func TestBackupAPI_AlleBoards_735_503VerbrauchtKeinKontingent(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedUser(t, "adm@x.de", true)
	ctx := context.Background()
	if err := env.cfg.Set(ctx, "backup_board.enabled", "false", false, ""); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		if resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, ""); resp.StatusCode != 503 {
			t.Fatalf("Versuch %d bei abgeschalteter Sicherung: erwartet 503, got %d", i, resp.StatusCode)
		}
	}
	if err := env.cfg.Set(ctx, "backup_board.enabled", "true", false, ""); err != nil {
		t.Fatal(err)
	}
	defer warteAufLaufEnde(t)
	if resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, ""); resp.StatusCode != 202 {
		t.Errorf("nach zwölf 503 muss ein Start noch gehen: erwartet 202, got %d", resp.StatusCode)
	}
}

// K-6c: Ein DB-Fehler bei der Admin-Prüfung ist 500, nicht „not an admin“ (403).
func TestBackupAPI_AlleBoards_735_DBFehlerIst500(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.srv.Close()
	env.seedUser(t, "adm@x.de", true)
	_ = env.db.Close()
	for _, c := range []struct{ m, p string }{
		{"POST", "/admin/backup/all-now"}, {"GET", "/admin/backup/all-status"},
	} {
		if resp := env.do(t, c.m, c.p, "adm@x.de", nil, ""); resp.StatusCode != 500 {
			t.Errorf("%s %s: erwartet 500, got %d", c.m, c.p, resp.StatusCode)
		}
	}
}

// K-5: Der Lauf hängt am Basis-Kontext (Server-Shutdown), nicht nur am Request.
func TestBackupAPI_AlleBoards_735_BasisKontextAbbruch(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Eins", "owner@x.de")
	env.seedUser(t, "adm@x.de", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.api.SetBaseContext(ctx)
	if resp := env.do(t, "POST", "/admin/backup/all-now", "adm@x.de", nil, ""); resp.StatusCode != 202 {
		t.Fatalf("erwartet 202, got %d", resp.StatusCode)
	}
	warteAufLaufEnde(t)
	_, last := boardbackup.AllStatus()
	if last == nil || last.Code != "abort" || last.OK != 0 {
		t.Errorf("abgebrochener Basis-Kontext: erwartet Code abort, got %+v", last)
	}
	if n := env.dateienImBackupVerz(t); n != 0 {
		t.Errorf("nach Abbruch keine Sicherungsdateien erwartet, got %d", n)
	}
}
