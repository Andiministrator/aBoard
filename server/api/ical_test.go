package api_test

// ical_test.go — Tests für iCal-Verwaltung + Feed (#308 Etappe 6, ADR-0103).
//
// Deckt: Anlage (cross-board + board-scoped mit ACL-Check), Liste, Feed-Abruf
// per Token (ICS-Format + ACL-Scope), Widerruf → Token 404, 503 ohne Master-Key.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

type icalTestEnv struct {
	srv    *httptest.Server
	db     *sql.DB
	boards *storage.BoardRepo
	acl    *storage.AclRepo
	cfg    *storage.ConfigRepo // ADR-0138 §2: kalender.zeitzone
}

func newICalEnv(t *testing.T, withBox bool) *icalTestEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	users := storage.NewUserRepo(db)
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)

	var tokens *storage.ICalTokenRepo
	if withBox {
		k := make([]byte, secretcrypto.KeySize)
		_, _ = rand.Read(k)
		box, berr := secretcrypto.New(hex.EncodeToString(k))
		if berr != nil {
			t.Fatalf("box: %v", berr)
		}
		tokens = storage.NewICalTokenRepo(db, box)
	}

	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })

	mux := http.NewServeMux()
	api.NewICalAPI(tokens, users, boards, acl).MitKonfig(cfg).Register(mux)
	var h http.Handler = mux
	h = api.WithForwardAuthIfEnabled(h)

	return &icalTestEnv{srv: httptest.NewServer(h), db: db, boards: boards, acl: acl, cfg: cfg}
}

func (e *icalTestEnv) close() { e.srv.Close(); _ = e.db.Close() }

func (e *icalTestEnv) seedUser(t *testing.T, id, email string) {
	t.Helper()
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, 0)`,
		id, storage.NormalizeEmail(email), "N", "2026-01-01"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func (e *icalTestEnv) seedBoard(t *testing.T, boardID, ownerEmail string) {
	t.Helper()
	state := `{"version":"2","board":{"id":"` + boardID + `","title":"B-` + boardID + `","sprints":[
		{"id":"sp1","title":"S","kind":"sprint","cards":[
			{"id":"c1","displayId":1,"title":"Termin ` + boardID + `","columnId":"col-ready","dueDate":"2026-06-15","description":"Hallo, Welt; test"}
		]}
	]}}`
	if _, err := e.boards.Insert(context.Background(), boardID, "B-"+boardID, "2", []byte(state)); err != nil {
		t.Fatalf("Insert board: %v", err)
	}
	if err := e.acl.Set(context.Background(), boardID, ownerEmail, storage.RoleOwner); err != nil {
		t.Fatalf("acl.Set: %v", err)
	}
}

func (e *icalTestEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	}
	r, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// ── Management ────────────────────────────────────────────────────────────

func TestICalAPI_503OhneMasterKey(t *testing.T) {
	env := newICalEnv(t, false)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	resp := env.do(t, http.MethodGet, "/me/ical/subscriptions", "andi@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("ohne Box: status=%d, erwartet 503", resp.StatusCode)
	}
}

// Seit ADR-0129 §2 (F-1124) weist die Forward-Auth-Middleware einen Request
// ohne Identität mit 401 ab, bevor die 403-Prüfung des Handlers greift.
func TestICalAPI_401OhneAuth(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	resp := env.do(t, http.MethodGet, "/me/ical/subscriptions", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ohne Auth: status=%d, erwartet 401", resp.StatusCode)
	}
}

func TestICalAPI_CreateListDeleteRoundtrip(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")

	// Cross-Board-Abo anlegen.
	resp := env.do(t, http.MethodPost, "/me/ical/subscriptions", "andi@example.com", map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Create: status=%d, erwartet 201", resp.StatusCode)
	}
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created["scope"] != "cross-board" {
		t.Errorf("scope=%v, erwartet cross-board", created["scope"])
	}
	path, _ := created["path"].(string)
	if !strings.HasPrefix(path, "/ical/calendar.ics?token=") {
		t.Fatalf("Feed-Pfad falsch: %q", path)
	}
	id, _ := created["id"].(string)

	// Liste enthält das Abo.
	resp = env.do(t, http.MethodGet, "/me/ical/subscriptions", "andi@example.com", nil)
	var list []map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 {
		t.Fatalf("Liste len=%d, erwartet 1", len(list))
	}

	// Löschen → 204, danach Liste leer.
	resp = env.do(t, http.MethodDelete, "/me/ical/subscriptions/"+id, "andi@example.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("Delete: status=%d, erwartet 204", resp.StatusCode)
	}
	resp = env.do(t, http.MethodGet, "/me/ical/subscriptions", "andi@example.com", nil)
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 0 {
		t.Errorf("nach Delete len=%d, erwartet 0", len(list))
	}
}

func TestICalAPI_CreateBoardScopeOhneACL403(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	env.seedBoard(t, "fremd", "other@example.com") // andi hat keinen Zugriff
	resp := env.do(t, http.MethodPost, "/me/ical/subscriptions", "andi@example.com",
		map[string]any{"board_id": "fremd"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Board-Abo ohne ACL: status=%d, erwartet 403", resp.StatusCode)
	}
}

// ── Feed ──────────────────────────────────────────────────────────────────

// feedToken legt ein Cross-Board-Abo an und gibt den Token zurück.
func (e *icalTestEnv) feedToken(t *testing.T, email string, board any) string {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/me/ical/subscriptions", email, board)
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	path, _ := created["path"].(string)
	return strings.TrimPrefix(path, "/ical/calendar.ics?token=")
}

func TestICalAPI_FeedCrossBoardRespektiertACL(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	env.seedBoard(t, "a", "andi@example.com")  // Zugriff
	env.seedBoard(t, "b", "other@example.com") // KEIN Zugriff

	token := env.feedToken(t, "andi@example.com", map[string]any{})

	// Feed ohne Auth-Header (externe Kalender-App) — nur Token.
	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/ical/calendar.ics?token="+token, nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Feed: status=%d, erwartet 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("Content-Type=%q, erwartet text/calendar", ct)
	}
	ics := string(body)
	if !strings.Contains(ics, "SUMMARY:#1 Termin a") {
		t.Errorf("Karte von Board a fehlt:\n%s", ics)
	}
	if strings.Contains(ics, "Termin b") {
		t.Errorf("Karte von fremdem Board b durchgesickert (ACL-Leck):\n%s", ics)
	}
	// Beschreibung mit Sonderzeichen muss escaped sein.
	if !strings.Contains(ics, `DESCRIPTION:Hallo\, Welt\; test`) {
		t.Errorf("DESCRIPTION-Escaping fehlt:\n%s", ics)
	}
}

func TestICalAPI_FeedBoardScope(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	env.seedBoard(t, "a", "andi@example.com")
	env.seedBoard(t, "c", "andi@example.com")

	// Board-scoped Token auf "a".
	token := env.feedToken(t, "andi@example.com", map[string]any{"board_id": "a"})
	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/ical/calendar.ics?token="+token, nil)
	resp, _ := http.DefaultClient.Do(r)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	ics := string(body)
	if !strings.Contains(ics, "Termin a") {
		t.Errorf("Board-a-Karte fehlt:\n%s", ics)
	}
	if strings.Contains(ics, "Termin c") {
		t.Errorf("Board-c-Karte im board-scoped Feed (Scope-Leck):\n%s", ics)
	}
}

func TestICalAPI_FeedRevokedToken404(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	env.seedBoard(t, "a", "andi@example.com")
	token := env.feedToken(t, "andi@example.com", map[string]any{})

	// Alle Abos des Users löschen.
	resp := env.do(t, http.MethodGet, "/me/ical/subscriptions", "andi@example.com", nil)
	var list []map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	for _, s := range list {
		env.do(t, http.MethodDelete, "/me/ical/subscriptions/"+s["id"].(string), "andi@example.com", nil).Body.Close()
	}

	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/ical/calendar.ics?token="+token, nil)
	resp, _ = http.DefaultClient.Do(r)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("widerrufener Token: status=%d, erwartet 404", resp.StatusCode)
	}
}

// F-447/#366: Der Feed-URL trägt seit jeher `#board=<id>&card=<n>` — dieses
// Format wird jetzt auch tatsächlich geroutet. Beim Nachziehen fiel auf, dass
// die BoardID hier im Gegensatz zu notifications/email.go (F-377) NICHT
// escaped wurde. Der Test hält beides fest: das Format und den Escape.
func TestICalAPI_FeedURLFormatUndEscape(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	env.seedBoard(t, "a", "andi@example.com")

	token := env.feedToken(t, "andi@example.com", map[string]any{})
	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/ical/calendar.ics?token="+token, nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	ics := string(body)

	// Das Format, das src/deeplink.js parst — displayId, nicht UUID.
	if !strings.Contains(ics, "board=a") || !strings.Contains(ics, "card=1") {
		t.Errorf("Deep-Link-Format fehlt im Feed:\n%s", ics)
	}
}

// Gegenprobe zum Escape: eine BoardID mit `&` darf keine weiteren Parameter
// an das Fragment haengen — sonst landet der Abonnent auf einem anderen Board.
func TestICalAPI_FeedURLEscapedBoardID(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	env.seedBoard(t, "a&card=99", "andi@example.com")

	token := env.feedToken(t, "andi@example.com", map[string]any{})
	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/ical/calendar.ics?token="+token, nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	ics := string(body)

	if strings.Contains(ics, "board=a&card=99") {
		t.Errorf("BoardID unescaped — Fragment gekapert:\n%s", ics)
	}
	if !strings.Contains(ics, "board=a%26card%3D99") {
		t.Errorf("erwarteter QueryEscape der BoardID fehlt:\n%s", ics)
	}
}
