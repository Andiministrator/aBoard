package api_test

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

func TestHealthHandler_OK(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := httptest.NewServer(api.HealthHandler(db))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, erwartet application/json", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, erwartet nosniff (F-195)", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, erwartet no-store (F-195)", got)
	}

	var body struct {
		OK      bool   `json:"ok"`
		DB      string `json:"db"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK {
		t.Errorf("ok = false")
	}
	if body.DB != "up" {
		t.Errorf("db = %q, erwartet up", body.DB)
	}
	if body.Version == "" {
		t.Errorf("version leer")
	}
}

// Karte #347: /healthz nennt zusaetzlich Commit und Build-Datum. Das ist der
// unauthentifizierte Weg, den Stand eines Deployments zu erfahren — ADR-0085
// (Drift-Erkennung) musste bis dahin die Registry befragen. Der Test haelt die
// drei Felder fest; verschwindet eines, faellt er.
func TestHealthHandler_BuildMetadaten(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Build-Variablen wie ein ldflags-Build setzen und danach zuruecksetzen.
	altV, altC, altD := api.BuildVersion, api.BuildCommit, api.BuildDate
	api.BuildVersion, api.BuildCommit, api.BuildDate = "v9.9.9", "abc1234", "2026-08-18T08:30:00Z"
	defer func() { api.BuildVersion, api.BuildCommit, api.BuildDate = altV, altC, altD }()

	srv := httptest.NewServer(api.HealthHandler(db))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildDate string `json:"build_date"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != "v9.9.9" {
		t.Errorf("version = %q, erwartet v9.9.9", body.Version)
	}
	if body.Commit != "abc1234" {
		t.Errorf("commit = %q, erwartet abc1234", body.Commit)
	}
	if body.BuildDate != "2026-08-18T08:30:00Z" {
		t.Errorf("build_date = %q, erwartet 2026-08-18T08:30:00Z", body.BuildDate)
	}
}

// VersionLine ist die Form, die im Startlog erscheint — sie muss alle drei
// Angaben enthalten, sonst nuetzt die Zeile im Container-Log nichts.
func TestVersionLine_EnthaeltAlleAngaben(t *testing.T) {
	altV, altC, altD := api.BuildVersion, api.BuildCommit, api.BuildDate
	api.BuildVersion, api.BuildCommit, api.BuildDate = "v1.2.3", "deadbee", "2026-01-01T00:00:00Z"
	defer func() { api.BuildVersion, api.BuildCommit, api.BuildDate = altV, altC, altD }()

	line := api.VersionLine()
	for _, teil := range []string{"v1.2.3", "deadbee", "2026-01-01T00:00:00Z"} {
		if !strings.Contains(line, teil) {
			t.Errorf("VersionLine() = %q, enthaelt %q nicht", line, teil)
		}
	}

	info := api.VersionInfo()
	if info["version"] != "v1.2.3" || info["commit"] != "deadbee" || info["build_date"] != "2026-01-01T00:00:00Z" {
		t.Errorf("VersionInfo() = %v", info)
	}
}

func TestHealthHandler_NurGETundHEAD(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := httptest.NewServer(api.HealthHandler(db))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("Status = %d, erwartet 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, erwartet \"GET, HEAD\" (F-196)", got)
	}
}

// F-196: Docker `HEALTHCHECK`, K8s `livenessProbe.httpGet` und die
// Upstream-Prüfungen von Caddy/Traefik senden teils HEAD. Wurde HEAD mit 405
// beantwortet, meldete der Orchestrator einen gesunden Container als krank.
// Der Test prüft beide Richtungen: gesunde DB → 200, tote DB → 503.
func TestHealthHandler_HEADErlaubt(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	srv := httptest.NewServer(api.HealthHandler(db))
	defer srv.Close()

	resp, err := http.Head(srv.URL)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD-Status = %d, erwartet 200 — ein Healthcheck per HEAD "+
			"würde den gesunden Container als krank melden", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, erwartet application/json", got)
	}

	// Tote DB muss auch per HEAD als 503 durchschlagen — sonst wäre der
	// Healthcheck zwar erlaubt, aber blind.
	db.Close()
	resp2, err := http.Head(srv.URL)
	if err != nil {
		t.Fatalf("HEAD (db down): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("HEAD-Status bei toter DB = %d, erwartet 503", resp2.StatusCode)
	}
}

func TestHealthHandler_DBDown(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close() // simuliert defekte Verbindung

	srv := httptest.NewServer(api.HealthHandler(db))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Status = %d, erwartet 503", resp.StatusCode)
	}

	var body struct {
		OK bool   `json:"ok"`
		DB string `json:"db"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.OK {
		t.Errorf("ok = true bei geschlossener DB")
	}
	if body.DB != "down" {
		t.Errorf("db = %q, erwartet down", body.DB)
	}
}
