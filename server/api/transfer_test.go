package api_test

// transfer_test.go — POST /boards/{b}/cards/{c}/transfer (#296, Sprint 43).
//
// Geprüfte Szenarien:
//   - Editor an beiden Boards → 200, Karte transferiert (UUID erhalten).
//   - Editor nur am Quell-Board → 403 (kein Schreibrecht am Ziel-Board).
//   - Viewer am Quell-Board → 403 (ACLGuard fängt vor Handler).
//   - Same Board (src == dst) → 400.
//   - Karte nicht in Quell-Board → 404.
//   - Ungültiges JSON / leere Felder → 400.
//   - Ziel-Spalte nicht in columnDefs → 400.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type transferEnv struct {
	srv        *httptest.Server
	boards     *storage.BoardRepo
	attach     *storage.AttachmentStore
	attachRoot string
	srcID      string
	dstID      string
	cardID     string
}

// transferEnvOpts steuert den Betriebsmodus des Test-Servers (F-391). Die
// Default-Fassung (newTransferEnv) ist der Mehrnutzer-Fall wie bisher; die
// Legacy-/Single-User-Fälle brauchen eine Instanz OHNE Trust-Schalter und ohne
// menschliche ACL-Einträge — sonst prüfen sie den Betriebsmodus mit, den sie
// gerade widerlegen sollen.
type transferEnvOpts struct {
	trustForwardAuth bool // ABOARD_TRUST_FORWARD_AUTH setzen?
	seedHumanACL     bool // ACL-Einträge für both/srcOnly/viewer anlegen?
	// seedHumanACLSrcOnly lässt den Eintrag für both@ am ZIEL-Board weg. Genau
	// diese Konstellation ist der F-391-Randfall: der Server führt ACL, das
	// Ziel-Board selbst aber nicht. Ohne diesen Schalter prüft der 403-Test
	// nichts — die alte, kaputte Fassung sagt bei einem ACL-behafteten
	// Ziel-Board ebenfalls 403 und der Test wäre grün ohne den Fix.
	seedHumanACLSrcOnly bool
	seedMailWorkerACL   bool // System-Eintrag des Mail-Workers (wie serviceboard.Seed)?
}

func newTransferEnv(t *testing.T) *transferEnv {
	t.Helper()
	return newTransferEnvWith(t, transferEnvOpts{trustForwardAuth: true, seedHumanACL: true})
}

func newTransferEnvWith(t *testing.T, opts transferEnvOpts) *transferEnv {
	t.Helper()
	if opts.trustForwardAuth {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	} else {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)
	boards := storage.NewBoardRepo(db)

	// Zwei Boards anlegen — eines mit Karte, eines leer
	srcState := minimalTransferState("src", "card-uuid-1", 7)
	dstState := minimalTransferState("dst", "", 0)
	if _, err := boards.Insert(ctx, "src", "Src-Board", "1", srcState); err != nil {
		t.Fatalf("insert src: %v", err)
	}
	if _, err := boards.Insert(ctx, "dst", "Dst-Board", "1", dstState); err != nil {
		t.Fatalf("insert dst: %v", err)
	}

	// User-Tabelle: drei Identitäten
	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES
		 ('u-both','both@example.org','Both','2026-01-01',0),
		 ('u-srcOnly','srconly@example.org','SrcOnly','2026-01-01',0),
		 ('u-viewer','viewer@example.org','Viewer','2026-01-01',0)`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	// ACL: both = editor an src + dst; srcOnly = editor an src; viewer = viewer an src
	if opts.seedHumanACL || opts.seedHumanACLSrcOnly {
		if err := acl.Set(ctx, "src", "both@example.org", storage.RoleEditor); err != nil {
			t.Fatalf("acl both src: %v", err)
		}
		if opts.seedHumanACL {
			if err := acl.Set(ctx, "dst", "both@example.org", storage.RoleEditor); err != nil {
				t.Fatalf("acl both dst: %v", err)
			}
		}
		if err := acl.Set(ctx, "src", "srconly@example.org", storage.RoleEditor); err != nil {
			t.Fatalf("acl srcOnly: %v", err)
		}
		if err := acl.Set(ctx, "src", "viewer@example.org", storage.RoleViewer); err != nil {
			t.Fatalf("acl viewer: %v", err)
		}
	}
	// System-Identität des Mail-Workers — serviceboard.Seed schreibt sie beim
	// Start JEDER Instanz, auch im Single-User-Default (Service-Board ist per
	// Default an). Sie darf deshalb NICHT als „Mehrnutzer-Betrieb" zählen.
	// Bewusst auf dem ZIEL-Board: das Service-Board ist der typische
	// Transfer-Ausgangs-/Zielpunkt, und so prüft der Single-User-Test zugleich,
	// dass die alte Regel („Ziel-Board hat ACL-Einträge → 403") hier nicht mehr
	// greift.
	if opts.seedMailWorkerACL {
		if err := acl.Set(ctx, "dst", storage.MailWorkerEmail, storage.RoleEditor); err != nil {
			t.Fatalf("acl mail-worker: %v", err)
		}
	}

	attachRoot := filepath.Join(t.TempDir(), "att")
	if err := os.MkdirAll(attachRoot, 0o755); err != nil {
		t.Fatalf("mkdir attach: %v", err)
	}
	attach := storage.NewAttachmentStore(attachRoot)

	transferAPI := api.NewTransferAPI(boards, users, acl).WithAttachments(attach)
	mux := http.NewServeMux()
	transferAPI.Register(mux)
	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &transferEnv{
		srv:        srv,
		attach:     attach,
		attachRoot: attachRoot,
		boards:     boards,
		srcID:      "src",
		dstID:      "dst",
		cardID:     "card-uuid-1",
	}
}

func minimalTransferState(boardID, cardID string, nextDisp int) []byte {
	board := map[string]any{
		"title": boardID,
		"columnDefs": []any{
			map[string]any{"id": "col-backlog", "title": "Backlog"},
			map[string]any{"id": "col-in-arbeit", "title": "In Arbeit"},
			map[string]any{"id": "col-erledigt", "title": "Erledigt"},
		},
		"nextDisplayId": float64(nextDisp),
		"sprints": []any{
			map[string]any{
				"id":    "__backlog__",
				"title": "Backlog",
				"kind":  "backlog",
				"cards": []any{},
			},
		},
	}
	if cardID != "" {
		backlog := board["sprints"].([]any)[0].(map[string]any)
		card := map[string]any{
			"id":          cardID,
			"displayId":   float64(5),
			"title":       "Test-Karte",
			"description": "",
			"columnId":    "col-backlog",
			"activity":    []any{},
		}
		backlog["cards"] = []any{card}
	}
	state, _ := json.Marshal(map[string]any{"version": "1", "board": board})
	return state
}

func (e *transferEnv) post(t *testing.T, email string, body any) *http.Response {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST",
		e.srv.URL+"/boards/"+e.srcID+"/cards/"+e.cardID+"/transfer",
		bytes.NewReader(buf))
	if email != "" {
		req.Header.Set("Remote-User", email)
		req.Header.Set("Remote-Email", email)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestTransfer_HappyPath_EditorBothBoards(t *testing.T) {
	env := newTransferEnv(t)
	resp := env.post(t, "both@example.org", map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-in-arbeit",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status: %d, body: %s", resp.StatusCode, string(body))
	}
	var out struct {
		NewDisplayID int `json:"newDisplayId"`
		OldDisplayID int `json:"oldDisplayId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.OldDisplayID != 5 {
		t.Errorf("OldDisplayID: %d ≠ 5", out.OldDisplayID)
	}
}

func TestTransfer_EditorOnlyOnSrc_403OnDst(t *testing.T) {
	env := newTransferEnv(t)
	resp := env.post(t, "srconly@example.org", map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-backlog",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 403, erhalten %d, body: %s", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "target board") {
		t.Errorf("Fehler nennt nicht das Ziel-Board: %s", string(body))
	}
}

func TestTransfer_ViewerOnSrc_403(t *testing.T) {
	env := newTransferEnv(t)
	resp := env.post(t, "viewer@example.org", map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-backlog",
	})
	defer resp.Body.Close()
	// Viewer wird durch ACLGuard am Quell-Board abgefangen (POST = editor+)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("erwartet 403 (Viewer am Quell-Board), erhalten %d", resp.StatusCode)
	}
}

func TestTransfer_SameBoard_400(t *testing.T) {
	env := newTransferEnv(t)
	resp := env.post(t, "both@example.org", map[string]any{
		"toBoardId":  "src",
		"toColumnId": "col-backlog",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 400, erhalten %d, body: %s", resp.StatusCode, string(body))
	}
}

func TestTransfer_CardNotFound_404(t *testing.T) {
	env := newTransferEnv(t)
	// Karte unbekannt → 404
	buf, _ := json.Marshal(map[string]any{"toBoardId": "dst", "toColumnId": "col-backlog"})
	req, _ := http.NewRequest("POST",
		env.srv.URL+"/boards/src/cards/unbekannte-uuid/transfer",
		bytes.NewReader(buf))
	req.Header.Set("Remote-User", "both@example.org")
	req.Header.Set("Remote-Email", "both@example.org")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 404, erhalten %d, body: %s", resp.StatusCode, string(body))
	}
}

func TestTransfer_InvalidColumn_400(t *testing.T) {
	env := newTransferEnv(t)
	resp := env.post(t, "both@example.org", map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-existiert-nicht",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("erwartet 400, erhalten %d", resp.StatusCode)
	}
}

func TestTransfer_MissingBody_400(t *testing.T) {
	env := newTransferEnv(t)
	resp := env.post(t, "both@example.org", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("erwartet 400 (leere toBoardId), erhalten %d", resp.StatusCode)
	}
}

// ── F-391: Legacy-Bypass ohne Identität ──────────────────────────────
//
// Beide Richtungen sind Pflicht. Der Angriffsfall allein wäre kein Beleg: ein
// Fix, der pauschal 403 liefert, nimmt dem Single-User-Betreiber den Zugang zu
// seinem eigenen Board — und ein Test, der nur die Ablehnung prüft, sieht das
// nicht.

// Richtung 1 — der dokumentierte Single-User-Default (docs/server-betrieb.md
// §Betriebsfall A) muss weiter funktionieren: kein Forward-Auth, keine
// Identitäten, nur der System-Eintrag des Mail-Workers vom Service-Board.
func TestTransfer_LegacyBypass_SingleUser_Funktioniert(t *testing.T) {
	env := newTransferEnvWith(t, transferEnvOpts{
		trustForwardAuth:  false,
		seedHumanACL:      false,
		seedMailWorkerACL: true,
	})
	resp := env.post(t, "", map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-in-arbeit",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Single-User-Transfer abgelehnt: %d, body: %s", resp.StatusCode, string(body))
	}
}

// Richtung 2 — genau der Randfall aus F-391: das ZIEL-Board führt keine
// ACL-Einträge, auf demselben Server liegen aber geteilte Boards. Vorher
// durchgewinkt, weil nur das Ziel-Board befragt wurde.
func TestTransfer_LegacyBypass_ZielOhneACL_AberServerGeteilt_403(t *testing.T) {
	env := newTransferEnvWith(t, transferEnvOpts{
		trustForwardAuth:    false,
		seedHumanACLSrcOnly: true, // ACL-Einträge NUR an "src", nicht an "dst"
	})
	resp := env.post(t, "", map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-in-arbeit",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 403 (Server führt ACL, Anrufer ohne Identität), erhalten %d, body: %s",
			resp.StatusCode, string(body))
	}
}

// Richtung 2b — Forward-Auth ist an, der Request kommt aber ohne
// Identitäts-Header an: er ist am Reverse-Proxy vorbeigelaufen. Auch ohne
// jeden ACL-Eintrag ist das kein Single-User-Betrieb. Seit ADR-0129 §2
// (F-1124) weist ihn schon die Forward-Auth-Middleware mit 401 ab; bis
// Sprint 70 kam er bis in den Handler und bekam dort 403.
func TestTransfer_LegacyBypass_TrustAnOhneHeader_401(t *testing.T) {
	env := newTransferEnvWith(t, transferEnvOpts{
		trustForwardAuth: true,
		seedHumanACL:     false,
	})
	resp := env.post(t, "", map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-in-arbeit",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 401 (Trust an, Request ohne Identität), erhalten %d, body: %s",
			resp.StatusCode, string(body))
	}
}

func TestTransfer_AttachmentSaga(t *testing.T) {
	env := newTransferEnv(t)

	// Anhang im src-Board anlegen
	srcCardDir := filepath.Join(env.attachRoot, env.srcID, env.cardID)
	if err := os.MkdirAll(srcCardDir, 0o755); err != nil {
		t.Fatalf("mkdir src-card: %v", err)
	}
	attPath := filepath.Join(srcCardDir, "att-id__rechnung.pdf")
	if err := os.WriteFile(attPath, []byte("pdf-bytes"), 0o644); err != nil {
		t.Fatalf("write att: %v", err)
	}

	resp := env.post(t, "both@example.org", map[string]any{
		"toBoardId":  env.dstID,
		"toColumnId": "col-backlog",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status: %d, body: %s", resp.StatusCode, string(body))
	}

	// Datei muss jetzt unter dst-Pfad existieren
	dstAtt := filepath.Join(env.attachRoot, env.dstID, env.cardID, "att-id__rechnung.pdf")
	if got, err := os.ReadFile(dstAtt); err != nil {
		t.Errorf("dst-Anhang fehlt: %v", err)
	} else if string(got) != "pdf-bytes" {
		t.Errorf("dst-Anhang-Inhalt: %q", string(got))
	}

	// src-Pfad muss leer/weg sein (Saga §2: DeleteCard nach DB-Tx-Commit)
	if _, err := os.Stat(srcCardDir); !os.IsNotExist(err) {
		t.Errorf("src-Card-Dir nicht aufgeräumt nach Transfer: %v", err)
	}
}

// Die reservierte Board-Ablage ist keine Karte — die Transfer-Route muss sie
// abweisen, bevor irgendetwas am Dateisystem passiert (Karte #443, F-865).
//
// Warum dieser Test nachgereicht wurde: Der Riegel in `handleTransfer` hatte
// nach seiner Einführung NULL Abdeckung. Ein Kritiker-Lauf hat den Block
// versuchsweise entfernt — `go test ./...` blieb grün. Der Angriffsweg, den
// der Kommentar in `server/storage/attachments.go` namentlich nennt
// (`POST /boards/{src}/cards/__board__/transfer`), wurde von keinem Test je
// begangen. Der Store-Riegel hält zwar unabhängig davon; ohne diese Zeile
// käme aber ein 500 aus der Tiefe statt einer 400 mit Grund.
func TestTransfer_ReservierteBoardAblageAbgelehnt(t *testing.T) {
	env := newTransferEnv(t)

	buf, _ := json.Marshal(map[string]any{
		"toBoardId":  "dst",
		"toColumnId": "col-in-arbeit",
	})
	req, _ := http.NewRequest("POST",
		env.srv.URL+"/boards/"+env.srcID+"/cards/"+storage.BoardAblageId+"/transfer",
		bytes.NewReader(buf))
	req.Header.Set("Remote-User", "both@example.org")
	req.Header.Set("Remote-Email", "both@example.org")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Transfer der Board-Ablage: want 400, got %d — ohne die frühe "+
			"Ablehnung liest StageCardAttachments erst das komplette "+
			"Board-Anhang-Verzeichnis, bevor irgendetwas scheitert", resp.StatusCode)
	}
}
