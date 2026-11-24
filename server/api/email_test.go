package api_test

// email_test.go — POST /boards/{b}/cards/{c}/email (#293, ADR-0091).
//
// SMTP-Test gegen einen Mini-Mock-SMTP-Server auf einem TCP-Listener
// (net.Listen("tcp", ":0")) — wir sprechen das nötige Subset (220-Greeting,
// EHLO/HELO, MAIL FROM, RCPT TO, DATA, ., QUIT) und protokollieren die
// gesendete Mail im Test.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

// ─── Mini-SMTP-Mock ──────────────────────────────────────────────────────
type smtpMock struct {
	addr   string
	host   string
	port   int
	mu     sync.Mutex
	mails  []string
	stop   chan struct{}
	done   chan struct{}
	listen net.Listener
}

func newSMTPMock(t *testing.T) *smtpMock {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtp mock listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	m := &smtpMock{
		addr:   ln.Addr().String(),
		host:   "127.0.0.1",
		port:   tcpAddr.Port,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		listen: ln,
	}
	go m.loop()
	t.Cleanup(m.close)
	return m
}

func (m *smtpMock) close() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	_ = m.listen.Close()
	<-m.done
}

func (m *smtpMock) loop() {
	defer close(m.done)
	for {
		conn, err := m.listen.Accept()
		if err != nil {
			return
		}
		go m.serve(conn)
	}
}

func (m *smtpMock) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 mock SMTP ready")
	var inData bool
	var data bytes.Buffer
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				m.mu.Lock()
				m.mails = append(m.mails, data.String())
				m.mu.Unlock()
				data.Reset()
				w("250 Ok queued")
				continue
			}
			data.WriteString(line)
			data.WriteString("\n")
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			w("250-mock.local")
			w("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(upper, "AUTH"):
			w("235 Authenticated")
		case strings.HasPrefix(upper, "MAIL FROM"):
			w("250 Ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			w("250 Ok")
		case upper == "DATA":
			w("354 Start mail input; end with <CRLF>.<CRLF>")
			inData = true
		case upper == "QUIT":
			w("221 Bye")
			return
		case strings.HasPrefix(upper, "RSET"):
			w("250 Ok")
		case strings.HasPrefix(upper, "NOOP"):
			w("250 Ok")
		default:
			w("250 Ok")
		}
	}
}

func (m *smtpMock) mailCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.mails)
}

func (m *smtpMock) lastMail() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.mails) == 0 {
		return ""
	}
	return m.mails[len(m.mails)-1]
}

// ─── Test-Environment ─────────────────────────────────────────────────────

type emailEnv struct {
	srv     *httptest.Server
	db      *sql.DB
	boards  *storage.BoardRepo
	users   *storage.UserRepo
	cfg     *storage.ConfigRepo
	smtp    *smtpMock
	boardID string
	cardID  string
}

func newEmailEnv(t *testing.T) *emailEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	boards := storage.NewBoardRepo(db)
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)

	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	var ctr atomic.Int64
	cfg := storage.NewConfigRepo(db, box, func() string { return "audit-" + strconv.FormatInt(ctr.Add(1), 10) })

	smtp := newSMTPMock(t)
	// SMTP-Config in den ConfigStore schreiben.
	_ = cfg.Set(ctx, "smtp.host", smtp.host, false, "")
	_ = cfg.Set(ctx, "smtp.port", strconv.Itoa(smtp.port), false, "")
	_ = cfg.Set(ctx, "smtp.user", "u", false, "")
	_ = cfg.Set(ctx, "smtp.pass", "p", true, "")
	_ = cfg.Set(ctx, "smtp.from", "aboard@example.org", false, "")
	_ = cfg.Set(ctx, "smtp.from_name", "aBoard", false, "")
	_ = cfg.Set(ctx, "smtp.starttls", "false", false, "")

	// Test-Board mit einer Karte.
	state := `{"version":"2","board":{"id":"b1","title":"Test","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":42,"title":"Karte-Titel","description":"# Hi\n\n**bold**","owner":"User","comments":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Test", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}

	emailAPI := api.NewEmailAPI(boards, users, cfg)
	mux := http.NewServeMux()
	emailAPI.Register(mux)

	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		srv.Close()
		_ = db.Close()
	})

	// User mit Lese-Recht aufs Board.
	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1','sender@x.de','S','2026-01-01',0)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// editor+ am Board: Mail-Versand mutiert formal nichts, aber der POST-
	// Pfad geht durch die ACL-Gate (editor+ für Mutationen, ADR-0091 §4).
	if err := acl.Set(ctx, "b1", "sender@x.de", storage.RoleEditor); err != nil {
		t.Fatalf("seed acl: %v", err)
	}

	return &emailEnv{
		srv: srv, db: db, boards: boards, users: users, cfg: cfg, smtp: smtp,
		boardID: "b1", cardID: "c1",
	}
}

func (e *emailEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
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

// ─── Tests ────────────────────────────────────────────────────────────────

func TestEmail_OK(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/boards/%s/cards/%s/email", env.boardID, env.cardID),
		"sender@x.de",
		map[string]any{"to": []string{"recv@example.org"}, "subject": "Test"})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}
	if env.smtp.mailCount() != 1 {
		t.Errorf("Mail-Count: %d, erwartet 1", env.smtp.mailCount())
	}
	mail := env.smtp.lastMail()
	if !strings.Contains(mail, "recv@example.org") {
		t.Errorf("To-Header fehlt: %q", mail)
	}
	if !strings.Contains(mail, "Subject:") || !strings.Contains(mail, "Test") {
		t.Errorf("Subject-Header fehlt")
	}
	if !strings.Contains(mail, "multipart/alternative") {
		t.Errorf("multipart/alternative fehlt")
	}
	if !strings.Contains(mail, "<strong>bold</strong>") {
		t.Errorf("HTML-Render fehlt im Body")
	}
}

func TestEmail_RejectsNonEditorACL(t *testing.T) {
	env := newEmailEnv(t)
	// Anderer User OHNE ACL-Eintrag.
	if _, err := env.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u2','noaccess@x.de','N','2026-01-01',0)`); err != nil {
		t.Fatal(err)
	}
	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/boards/%s/cards/%s/email", env.boardID, env.cardID),
		"noaccess@x.de",
		map[string]any{"to": []string{"recv@example.org"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("erwartet 403 ohne ACL, got %d", resp.StatusCode)
	}
}

func TestEmail_TooManyRecipients(t *testing.T) {
	env := newEmailEnv(t)
	too := make([]string, 11)
	for i := range too {
		too[i] = fmt.Sprintf("u%d@x.de", i)
	}
	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/boards/%s/cards/%s/email", env.boardID, env.cardID),
		"sender@x.de",
		map[string]any{"to": too})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("11 Empfänger sollten 400 ergeben, got %d", resp.StatusCode)
	}
}

func TestEmail_InvalidEmail_400(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/boards/%s/cards/%s/email", env.boardID, env.cardID),
		"sender@x.de",
		map[string]any{"to": []string{"not-an-email"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("ungültige Email: erwartet 400, got %d", resp.StatusCode)
	}
}

func TestEmail_NoSMTPConfig_503(t *testing.T) {
	env := newEmailEnv(t)
	// SMTP-Host löschen.
	_ = env.cfg.Delete(context.Background(), "smtp.host", "")
	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/boards/%s/cards/%s/email", env.boardID, env.cardID),
		"sender@x.de",
		map[string]any{"to": []string{"recv@example.org"}})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("ohne SMTP-Config: erwartet 503, got %d", resp.StatusCode)
	}
}

func TestEmail_CardNotFound_404(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.do(t, http.MethodPost,
		"/boards/"+env.boardID+"/cards/ghost/email",
		"sender@x.de",
		map[string]any{"to": []string{"recv@example.org"}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Ghost-Card: erwartet 404, got %d", resp.StatusCode)
	}
}

func TestEmail_RateLimit_10PerMinute(t *testing.T) {
	env := newEmailEnv(t)
	for i := 0; i < 10; i++ {
		resp := env.do(t, http.MethodPost,
			fmt.Sprintf("/boards/%s/cards/%s/email", env.boardID, env.cardID),
			"sender@x.de",
			map[string]any{"to": []string{"recv@example.org"}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Lauf %d: %d", i, resp.StatusCode)
		}
	}
	resp := env.do(t, http.MethodPost,
		fmt.Sprintf("/boards/%s/cards/%s/email", env.boardID, env.cardID),
		"sender@x.de",
		map[string]any{"to": []string{"recv@example.org"}})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("11. Mail: erwartet 429, got %d", resp.StatusCode)
	}
}
