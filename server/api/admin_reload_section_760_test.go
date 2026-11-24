package api_test

// Karte #760 (O-3, O-5): POST /admin/config/reload?section=<name> führt nur den
// Hook dieser Sektion aus; ohne Parameter bleibt es bei „alle“. Ein Hook kann
// per api.ReloadStatus ein Nicht-Fehler-Ergebnis (z. B. "disabled") melden.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func reloadErgebnis(t *testing.T, resp *http.Response) map[string]string {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Results map[string]string `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Results
}

func TestAdmin_ReloadSection_NurDieGenannteSektion(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-adm", "adm@x.de", true)
	var imap, backup atomic.Int32
	env.admin.Registry().RegisterReload("imap", func(context.Context, map[string]string) error { imap.Add(1); return nil })
	env.admin.Registry().RegisterReload("backup_board", func(context.Context, map[string]string) error { backup.Add(1); return nil })

	resp := env.do(t, "POST", "/admin/config/reload?section=imap", "adm@x.de", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	res := reloadErgebnis(t, resp)
	if len(res) != 1 || res["imap"] != "ok" {
		t.Fatalf("results = %v, erwartet nur imap=ok", res)
	}
	if imap.Load() != 1 || backup.Load() != 0 {
		t.Fatalf("imap=%d backup=%d — der Backup-Hook darf nicht laufen", imap.Load(), backup.Load())
	}
}

func TestAdmin_ReloadSection_UnbekannteSektion400(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-adm", "adm@x.de", true)
	var n atomic.Int32
	env.admin.Registry().RegisterReload("imap", func(context.Context, map[string]string) error { n.Add(1); return nil })
	resp := env.do(t, "POST", "/admin/config/reload?section=gibtsnicht", "adm@x.de", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status %d, erwartet 400", resp.StatusCode)
	}
	if n.Load() != 0 {
		t.Fatalf("bei unbekannter Sektion darf kein Hook laufen")
	}
}

func TestAdmin_ReloadOhneParameter_FuehrtAlleAus(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-adm", "adm@x.de", true)
	env.admin.Registry().RegisterReload("a", func(context.Context, map[string]string) error { return nil })
	env.admin.Registry().RegisterReload("b", func(context.Context, map[string]string) error { return errors.New("kaputt") })
	res := reloadErgebnis(t, env.do(t, "POST", "/admin/config/reload", "adm@x.de", nil))
	if res["a"] != "ok" || res["b"] != "error: kaputt" {
		t.Fatalf("results = %v", res)
	}
}

func TestAdmin_ReloadStatus_WirdAlsTextGemeldetNichtAlsFehler(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-adm", "adm@x.de", true)
	env.admin.Registry().RegisterReload("imap", func(context.Context, map[string]string) error {
		return api.ReloadStatus("disabled")
	})
	res := reloadErgebnis(t, env.do(t, "POST", "/admin/config/reload?section=imap", "adm@x.de", nil))
	if res["imap"] != "disabled" {
		t.Fatalf("results = %v, erwartet imap=disabled", res)
	}
}
