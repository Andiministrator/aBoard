package api_test

// notifications_emit_test.go — Tests für POST /boards/{id}/notifications/events
// (handleEmitEvent, ADR-0107 Phase 2 / Karte #338, F-378).
//
// Deckt ab: 202 Happy Path, unbekannter event_type, invalid JSON, 16-KB-Limit,
// Forbidden ohne Auth, Actor-Override-Schutz (body.actor_user_id wird IGNORIERT),
// Rate-Limit-429, recipient_scope-Expansion (board-members-viewer-plus).

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/notifications"
	"github.com/Andiministrator/aBoard/server/storage"
)

func newUUIDForTest(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// spyEmitter sammelt alle Emit-Events thread-safe. Tests inspizieren die
// Empfänger-Liste, ohne den realen Worker mitzuziehen.
type spyEmitter struct {
	mu     sync.Mutex
	events []notifications.Event
}

func (s *spyEmitter) Emit(ev notifications.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *spyEmitter) last() (notifications.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return notifications.Event{}, false
	}
	return s.events[len(s.events)-1], true
}

func (s *spyEmitter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// notifTestEnv: vollständige Notification-Kette (ForwardAuth → ACLGuard → mux).
type notifTestEnv struct {
	srv    *httptest.Server
	db     *sql.DB
	boards *storage.BoardRepo
	acl    *storage.AclRepo
	users  *storage.UserRepo
	spy    *spyEmitter
}

func (e *notifTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

/*
Voreinstellung der Test-Umgebung fuer den Versand-Schalter: an. Der

	Schalter-Test unten baut sich seine Umgebung mit dem jeweils gewuenschten
	Wert selbst ueber newNotifEmitTestEnvMitDelivery.
*/
const deliveryFuerTest = true

func newNotifEmitTestEnv(t *testing.T) *notifTestEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	boards := storage.NewBoardRepo(db)
	aclRepo := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)
	subs := storage.NewNotificationSubscriptionRepo(db)
	cfgs := storage.NewNotificationConfigRepo(db, nil)
	logs := storage.NewNotificationLogRepo(db, func() string { return "test-uuid" })
	spy := &spyEmitter{}

	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(aclRepo).Register(mux)
	/* deliveryEnabled kommt seit dem Sprint-59-Verify als Konstruktor-
	   Argument (ein Lesezeitpunkt statt zwei). Die Test-Umgebung setzt es
	   auf true, damit `delivery_enabled` in der API-Antwort nicht konstant
	   false meldet — der Schalter-Test unten setzt es gezielt selbst. */
	notifAPI := api.NewNotificationsAPI(subs, cfgs, logs, users, boards, aclRepo, spy, nil, nil, deliveryFuerTest)
	notifAPI.Register(mux)

	guard := api.NewACLGuard(aclRepo, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)

	return &notifTestEnv{
		srv: httptest.NewServer(h), db: db,
		boards: boards, acl: aclRepo, users: users, spy: spy,
	}
}

func (e *notifTestEnv) seedBoard(t *testing.T, id string) {
	t.Helper()
	if _, err := e.boards.Insert(context.Background(), id, "Board "+id, "2", sampleBody(id, "Board "+id)); err != nil {
		t.Fatalf("seed board %s: %v", id, err)
	}
}

// upsertUser materialisiert eine users-Row und einen ACL-Eintrag.
func (e *notifTestEnv) upsertUser(t *testing.T, email, name, boardID, role string) string {
	t.Helper()
	ctx := context.Background()
	u, err := e.users.Upsert(ctx, newUUIDForTest(t), email, name)
	if err != nil {
		t.Fatalf("users.Upsert(%s): %v", email, err)
	}
	if boardID != "" {
		if err := e.acl.Set(ctx, boardID, email, role); err != nil {
			t.Fatalf("acl.Set(%s,%s): %v", boardID, role, err)
		}
	}
	return u.ID
}

// emitBody hilft Tests, JSON-Bodies kurz zu bauen.
func emitBody(eventType string, extra map[string]any) []byte {
	m := map[string]any{
		"event_type":      eventType,
		"board_name":      "Test-Board",
		"card_id":         "card-1",
		"card_title":      "Test-Karte",
		"card_display_id": 42,
		"snippet":         "Snippet",
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// post baut einen POST-Request mit Forward-Auth-Identität.
func (e *notifTestEnv) post(t *testing.T, path, email string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if email != "" {
		req.Header.Set("Remote-Email", email)
		req.Header.Set("Remote-User", email)
	}
	return mustDo(t, req)
}

// get spiegelt post fuer lesende Endpunkte (Karte #420 — der
// event-types-Endpunkt meldet seit dieser Karte den Versand-Schalter).
func (e *notifTestEnv) get(t *testing.T, path, email string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if email != "" {
		req.Header.Set("Remote-Email", email)
		req.Header.Set("Remote-User", email)
	}
	return mustDo(t, req)
}

// ── 202 Happy Path ────────────────────────────────────────────────────

func TestEmit_202_HappyPath(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	editorID := env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)

	body := emitBody(notifications.EventCardCommented, nil)
	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202; body=%s", resp.StatusCode, mustReadAll(t, resp.Body))
	}
	ev, ok := env.spy.last()
	if !ok {
		t.Fatal("kein Event emittiert")
	}
	if ev.Type != notifications.EventCardCommented {
		t.Errorf("Type=%q, erwartet card.commented", ev.Type)
	}
	if ev.ActorUserID != editorID {
		t.Errorf("ActorUserID=%q, erwartet %q (Caller)", ev.ActorUserID, editorID)
	}
	if ev.BoardID != "B" {
		t.Errorf("BoardID=%q, erwartet B", ev.BoardID)
	}
}

// ── Unknown event_type → 400 ──────────────────────────────────────────

func TestEmit_400_UnknownEventType(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)

	body := emitBody("card.foo-bar", nil)
	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, erwartet 400", resp.StatusCode)
	}
	if env.spy.count() != 0 {
		t.Errorf("unerwartetes Emit bei unknown event_type")
	}
}

// ── Invalid JSON → 400 ─────────────────────────────────────────────────

func TestEmit_400_InvalidJSON(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)

	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", []byte("{not-json"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, erwartet 400", resp.StatusCode)
	}
}

// ── 16-KB-Limit ────────────────────────────────────────────────────────

func TestEmit_400_BodyExceedsLimit(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)

	// Snippet auf >16 KB aufblasen.
	huge := strings.Repeat("x", 17*1024)
	body := emitBody(notifications.EventCardCommented, map[string]any{"snippet": huge})
	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
	defer resp.Body.Close()
	// http.MaxBytesReader liefert json.Decode-Fehler → 400.
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, erwartet 400/413 für >16 KB Body", resp.StatusCode)
	}
}

// ── Forbidden ohne Auth ────────────────────────────────────────────────

func TestEmit_401_WithoutAuth(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)

	body := emitBody(notifications.EventCardCommented, nil)
	// kein Email-Header → keine Identität → 401 aus der Forward-Auth-
	// Middleware (ADR-0129 §2, F-1124), noch vor dem ACLGuard.
	resp := env.post(t, "/boards/B/notifications/events", "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, erwartet 401", resp.StatusCode)
	}
}

// ── Actor-Override-Schutz (Kritiker Sprint 42 [S]P2 / F-382) ──────────

func TestEmit_ActorOverrideIgnored(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	editorID := env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)
	// Anderer User auf demselben Board.
	otherID := env.upsertUser(t, "other@example.com", "Other", "B", storage.RoleViewer)

	body := emitBody(notifications.EventCardCommented, map[string]any{
		"actor_user_id": otherID,
		"actor_email":   "other@example.com",
	})
	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202", resp.StatusCode)
	}
	ev, _ := env.spy.last()
	if ev.ActorUserID != editorID {
		t.Errorf("ActorUserID=%q, erwartet %q (Caller, NICHT body-spoofed)", ev.ActorUserID, editorID)
	}
	if ev.ActorEmail != "editor@example.com" {
		t.Errorf("ActorEmail=%q, erwartet editor@example.com", ev.ActorEmail)
	}
}

// ── Rate-Limit (60/h pro User) ────────────────────────────────────────

func TestEmit_429_RateLimit(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)

	body := emitBody(notifications.EventCardCommented, nil)
	// 60 Calls passen, der 61. wird gedrosselt.
	var lastStatus int
	for i := 0; i < 61; i++ {
		resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
		lastStatus = resp.StatusCode
		resp.Body.Close()
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("61. Call status=%d, erwartet 429", lastStatus)
	}
}

// ── recipient_scope: board-members-viewer-plus ────────────────────────

func TestEmit_RecipientScope_ExpandsToBoardMembers(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	editorID := env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)
	viewerID := env.upsertUser(t, "viewer@example.com", "Viewer", "B", storage.RoleViewer)
	ownerID := env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)

	body := emitBody(notifications.EventBoardAwaiting, map[string]any{
		"recipient_scope": "board-members-viewer-plus",
	})
	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202", resp.StatusCode)
	}
	ev, _ := env.spy.last()
	if len(ev.ExplicitRecipientUserIDs) != 2 {
		t.Fatalf("ExplicitRecipientUserIDs len=%d, erwartet 2 (Caller=Editor raus, Owner+Viewer in); ev=%+v", len(ev.ExplicitRecipientUserIDs), ev)
	}
	got := map[string]bool{}
	for _, id := range ev.ExplicitRecipientUserIDs {
		got[id] = true
	}
	if got[editorID] {
		t.Error("Caller (Editor) wurde NICHT aus Scope-Expansion gefiltert (Anti-Loop verletzt)")
	}
	if !got[ownerID] {
		t.Errorf("Owner-ID %q fehlt in Scope-Expansion", ownerID)
	}
	if !got[viewerID] {
		t.Errorf("Viewer-ID %q fehlt in Scope-Expansion", viewerID)
	}
}

// ── recipient_scope: unbekannter Wert → 400 ───────────────────────────

func TestEmit_RecipientScope_UnknownReturns400(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)

	body := emitBody(notifications.EventBoardAwaiting, map[string]any{
		"recipient_scope": "all-the-things",
	})
	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, erwartet 400 für unbekannten Scope", resp.StatusCode)
	}
}

// ── recipient_scope merged mit explizit übergebenen UserIDs ──────────

func TestEmit_RecipientScope_MergesWithExplicit(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	editorID := env.upsertUser(t, "editor@example.com", "Editor", "B", storage.RoleEditor)
	_ = editorID
	ownerID := env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)
	// User auf anderem Board, der explizit als Empfänger durchgereicht wird.
	env.seedBoard(t, "OTHER")
	extraID := env.upsertUser(t, "extra@example.com", "Extra", "OTHER", storage.RoleEditor)

	body := emitBody(notifications.EventBoardAwaiting, map[string]any{
		"recipient_scope":    "board-members-viewer-plus",
		"recipient_user_ids": []string{extraID},
	})
	resp := env.post(t, "/boards/B/notifications/events", "editor@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202", resp.StatusCode)
	}
	ev, _ := env.spy.last()
	got := map[string]bool{}
	for _, id := range ev.ExplicitRecipientUserIDs {
		got[id] = true
	}
	if !got[ownerID] || !got[extraID] {
		t.Errorf("Merge unvollständig: ev.Recipients=%v (owner=%s extra=%s)", ev.ExplicitRecipientUserIDs, ownerID, extraID)
	}
}

// ── recipient_emails: der ADR-0108-Weg für die card.*-Events (Karte #420) ──
//
// Bis Sprint 59 hatte das Frontend keinen Weg von einem Karten-Owner-String
// zu einer Empfänger-UserID; genau das hat Karte #339 87 Tage blockiert.
// ADR-0108 Option A löst es über `board.identities[].email`: Das Frontend
// schickt Adressen, der Server mappt sie auf UserIDs. Der Server bleibt dabei
// opak gegenüber der Kartenstruktur (ADR-0044).

func TestEmit_RecipientEmails_WerdenZuUserIDsAufgeloest(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
	ownerID := env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)

	body := emitBody(notifications.EventCardAssigned, map[string]any{
		"recipient_emails": []string{"owner@example.com"},
		"card_id":          "c1",
	})
	resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202", resp.StatusCode)
	}
	ev, ok := env.spy.last()
	if !ok {
		t.Fatal("kein Event emittiert")
	}
	if len(ev.ExplicitRecipientUserIDs) != 1 || ev.ExplicitRecipientUserIDs[0] != ownerID {
		t.Fatalf("ExplicitRecipientUserIDs=%v, erwartet genau [%s]", ev.ExplicitRecipientUserIDs, ownerID)
	}
}

// Die Auflage aus Entscheid des Maintainers vom 2026-08-19: niemand bekommt Post über
// die eigene Aktion. Der Worker filtert zwar auch gegen ActorUserID — aber
// eine Zusage, die nur zwei Schichten weiter eingelöst wird, geht beim
// nächsten Umbau dort verloren, ohne dass es hier auffiele.
func TestEmit_RecipientEmails_CallerWirdGefiltert(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
	ownerID := env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)

	body := emitBody(notifications.EventCardCommented, map[string]any{
		/* Der Caller steht selbst mit in der Liste — genau der Normalfall,
		   wenn jemand eine Karte kommentiert, die ihm selbst gehört. */
		"recipient_emails": []string{"caller@example.com", "owner@example.com"},
		"card_id":          "c1",
	})
	resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
	defer resp.Body.Close()

	ev, _ := env.spy.last()
	if len(ev.ExplicitRecipientUserIDs) != 1 || ev.ExplicitRecipientUserIDs[0] != ownerID {
		t.Fatalf("ExplicitRecipientUserIDs=%v, erwartet nur [%s] — der Caller muss herausfallen",
			ev.ExplicitRecipientUserIDs, ownerID)
	}
}

// Eine Identity ohne hinterlegten Account ist ein normaler Zustand (ADR-0108:
// „Identities ohne Email funktionieren weiter"). Ein 4xx darauf wäre zudem
// ein Existenz-Orakel über die Nutzertabelle.
func TestEmit_RecipientEmails_UnbekannteAdresseStillVerworfen(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
	ownerID := env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)

	body := emitBody(notifications.EventCardAssigned, map[string]any{
		"recipient_emails": []string{"niemand@example.com", "owner@example.com"},
		"card_id":          "c1",
	})
	resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202 — eine unbekannte Adresse darf den Emit nicht scheitern lassen", resp.StatusCode)
	}
	ev, _ := env.spy.last()
	if len(ev.ExplicitRecipientUserIDs) != 1 || ev.ExplicitRecipientUserIDs[0] != ownerID {
		t.Fatalf("ExplicitRecipientUserIDs=%v, erwartet nur den bekannten Empfänger", ev.ExplicitRecipientUserIDs)
	}
}

// Ohne Grenze löst ein einziger Emit eine DB-Abfrage pro Adresse aus; das
// 60/h-Limit bremst die Anzahl der Emits, nicht ihre Grösse.
func TestEmit_RecipientEmails_GrenzeGreiftUndLehntNichtAb(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
	ownerID := env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)

	/* 60 Fülladressen VOR dem echten Empfänger: Der Empfänger liegt damit
	   jenseits der Grenze und muss herausfallen — das belegt, dass die
	   Grenze wirklich greift. Ein Test, der sie nur „nicht abgelehnt"
	   prüft, wäre auch ohne Grenze grün. */
	mails := make([]string, 0, 61)
	for i := 0; i < 60; i++ {
		mails = append(mails, "fill"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.com")
	}
	mails = append(mails, "owner@example.com")

	body := emitBody(notifications.EventCardAssigned, map[string]any{
		"recipient_emails": mails,
		"card_id":          "c1",
	})
	resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202 — Überzählige werden abgeschnitten, nicht abgelehnt", resp.StatusCode)
	}
	ev, _ := env.spy.last()
	for _, id := range ev.ExplicitRecipientUserIDs {
		if id == ownerID {
			t.Fatal("Empfänger jenseits der Grenze wurde aufgelöst — maxRecipientEmails greift nicht")
		}
	}
}

// ── Der Hauptschalter ist in der API sichtbar (Karte #420) ────────────
//
// Zweite Auflage des Maintainers: dass der Versand aus ist, darf nicht still passieren.
// Ohne dieses Feld sieht der Nutzer eine vollständig konfigurierbare
// Oberfläche, die nichts zustellt — und sucht den Fehler bei sich.

func TestEventTypes_MeldetDenVersandSchalter(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)

	resp := env.get(t, "/users/me/notifications/event-types", "caller@example.com")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, erwartet 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	/* Der Wert kommt seit dem Sprint-59-Verify als Konstruktor-Argument,
	   nicht mehr aus der Umgebung — die Umgebung bei jeder Anfrage neu zu
	   lesen erzeugte einen zweiten Lesezeitpunkt, und die UI konnte damit
	   einen Zustand melden, der von der Sender-Verdrahtung abweicht. Der
	   Test spiegelt jetzt genau das: die API meldet, was ihr uebergeben
	   wurde. */
	if v, ok := out["delivery_enabled"].(bool); !ok || v != deliveryFuerTest {
		t.Errorf("delivery_enabled=%v, erwartet %v — die API muss den uebergebenen "+
			"Schalterzustand melden, sonst zeigt die Einstellungs-UI etwas anderes an "+
			"als tatsaechlich passiert", out["delivery_enabled"], deliveryFuerTest)
	}

	/* Die neuen Ereignistypen muessen in der Liste stehen, sonst kann die
	   Einstellungs-UI sie nicht abonnierbar machen — auch die, die ein
	   Client nicht selbst ausloesen darf. */
	typen, _ := out["event_types"].([]any)
	gefunden := false
	for _, it := range typen {
		if m, ok := it.(map[string]any); ok && m["type"] == notifications.EventSubsystemOff {
			gefunden = true
		}
	}
	if !gefunden {
		t.Errorf("%q fehlt in event_types — die Einstellungs-UI kann es dann nicht abonnierbar machen",
			notifications.EventSubsystemOff)
	}
}

// ── Client-Trigger dürfen keine system.*-Ereignisse auslösen ──────────
//
// Sprint-59-Kritiker [S] P2. Der Angriffsweg war nicht neu, sondern wurde
// durch `recipient_emails` erst brauchbar: Vorher musste ein Aufrufer
// Empfänger-UUIDs kennen, die der Server keinem Nicht-Admin ausliefert —
// diese Unwissenheit war der faktische Schutz. `/known-users` liefert dagegen
// jedem Angemeldeten das vollständige Adressverzeichnis, und `POST /boards`
// verschafft ihm ein eigenes Board für den ACL-Guard.
//
// Damit liessen sich `system.subsystem-off` (Default-Abo auf BEIDEN Kanälen,
// höchste Gotify-Dringlichkeit, kein Opt-out möglich, weil niemand ein Abo
// hat, das er abbestellen könnte) mit frei wählbarem Text an beliebige
// Konten schicken — an Empfänger, die auf dem Board nie eine Rolle hatten.
func TestEmit_SystemEreignisseSindNichtVomClientAusloesbar(t *testing.T) {
	verboten := []string{
		notifications.EventBackupFailed,
		notifications.EventSubsystemOff,
		notifications.EventServiceNewCard,
		notifications.EventCommentDeleted, // ADR-0136: nur der Server meldet es
	}
	for _, typ := range verboten {
		t.Run(typ, func(t *testing.T) {
			env := newNotifEmitTestEnv(t)
			defer env.close()
			env.seedBoard(t, "B")
			env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
			env.upsertUser(t, "opfer@example.com", "Opfer", "B", storage.RoleViewer)

			body := emitBody(typ, map[string]any{
				"recipient_emails": []string{"opfer@example.com"},
				"snippet":          "frei waehlbarer Text",
			})
			resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d, erwartet 400 — %q entsteht ausschliesslich server-intern "+
					"und darf nicht vom Browser ausgelöst werden können", resp.StatusCode, typ)
			}
			if env.spy.count() != 0 {
				t.Fatalf("%d Event(s) emittiert, erwartet 0 — der Bus darf gar nicht erreicht werden", env.spy.count())
			}
		})
	}
}

// Die Gegenrichtung: die fünf Karten-Ereignisse müssen mit `recipient_emails`
// weiterhin durchgehen. Ohne diesen Teil wäre die Begrenzung auch mit einem
// pauschalen `return false` grün — sie prüfte dann, dass nichts funktioniert.
func TestEmit_LegitimeEreignisseGehenWeiterhinDurch(t *testing.T) {
	erlaubt := []string{
		notifications.EventCardAssigned,
		notifications.EventCardCommented,
		notifications.EventCardMentioned,
		notifications.EventCardMoved,
		notifications.EventCardBlocked,
	}
	for _, typ := range erlaubt {
		t.Run(typ, func(t *testing.T) {
			env := newNotifEmitTestEnv(t)
			defer env.close()
			env.seedBoard(t, "B")
			env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
			env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)

			body := emitBody(typ, map[string]any{
				"recipient_emails": []string{"owner@example.com"},
				"card_id":          "c1",
			})
			resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status=%d, erwartet 202 für %q", resp.StatusCode, typ)
			}
		})
	}
}

// `board.awaiting-user` und `sprint.closed` bleiben auslösbar — aber NUR über
// den ACL-basierten Server-Resolver, nicht mit frei gewählten Adressen
// (Phase-2b-Verify, [S] P1).
//
// Der erste F-722-Fix hatte sie in der Allowlist gelassen, weil es für sie
// einen legitimen Weg gibt. Nur ist es nicht `recipient_emails`: Beide sind
// board-, nicht kartenbezogen, `board.awaiting-user` ist auf beiden Kanälen
// unbedingt abonniert und trägt Gotify-Dringlichkeit 7 — mit freien Adressen
// wären sie dieselbe Angriffsklasse, die der Fix gerade schliessen wollte.
func TestEmit_BoardEreignisseNehmenKeineFreienAdressen(t *testing.T) {
	for _, typ := range []string{notifications.EventBoardAwaiting, notifications.EventSprintClosed} {
		t.Run(typ+"/mit recipient_emails", func(t *testing.T) {
			env := newNotifEmitTestEnv(t)
			defer env.close()
			env.seedBoard(t, "B")
			env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
			env.upsertUser(t, "fremd@example.com", "Fremd", "B", storage.RoleViewer)

			body := emitBody(typ, map[string]any{
				"recipient_emails": []string{"fremd@example.com"},
				"board_name":       "frei waehlbarer Text",
			})
			resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d, erwartet 400 — %q darf keine frei gewählten Adressen annehmen", resp.StatusCode, typ)
			}
			if env.spy.count() != 0 {
				t.Fatalf("%d Event(s) emittiert, erwartet 0", env.spy.count())
			}
		})

		t.Run(typ+"/mit recipient_scope", func(t *testing.T) {
			env := newNotifEmitTestEnv(t)
			defer env.close()
			env.seedBoard(t, "B")
			env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
			ownerID := env.upsertUser(t, "owner@example.com", "Owner", "B", storage.RoleOwner)

			body := emitBody(typ, map[string]any{
				"recipient_scope": "board-members-viewer-plus",
			})
			resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status=%d, erwartet 202 — der Scope-Weg ist der vorgesehene für %q", resp.StatusCode, typ)
			}
			ev, _ := env.spy.last()
			gefunden := false
			for _, id := range ev.ExplicitRecipientUserIDs {
				if id == ownerID {
					gefunden = true
				}
			}
			if !gefunden {
				t.Errorf("Der Board-Owner fehlt in der expandierten Empfängerliste %v — "+
					"dann wäre der Scope-Weg zwar erlaubt, aber wirkungslos", ev.ExplicitRecipientUserIDs)
			}
		})
	}
}

// Die Grenze greift NACH Normalisierung und Entdopplung (Sprint-59-Kritiker
// [K] P3). Vorher lief das Kappen zuerst: Eine Karte, bei der dieselbe Person
// Owner UND Assignee UND erwähnt ist, erzeugt Dopplungen — standen davon 50
// in der Liste, fiel der echte Empfänger dahinter still heraus.
func TestEmit_RecipientEmails_DuplikateVerbrauchenKeinKontingent(t *testing.T) {
	env := newNotifEmitTestEnv(t)
	defer env.close()
	env.seedBoard(t, "B")
	env.upsertUser(t, "caller@example.com", "Caller", "B", storage.RoleEditor)
	dupID := env.upsertUser(t, "doppelt@example.com", "Doppelt", "B", storage.RoleEditor)
	zielID := env.upsertUser(t, "ziel@example.com", "Ziel", "B", storage.RoleOwner)

	/* 60 Schreibweisen DERSELBEN Adresse, dann der echte Empfänger. Nach
	   Normalisierung ist das eine Adresse — der Empfänger muss also drin
	   sein. Vor dem Fix wurde bei 50 gekappt und er fiel heraus. */
	mails := make([]string, 0, 61)
	for i := 0; i < 60; i++ {
		if i%2 == 0 {
			mails = append(mails, "  Doppelt@Example.com ")
		} else {
			mails = append(mails, "DOPPELT@example.com")
		}
	}
	mails = append(mails, "ziel@example.com")

	body := emitBody(notifications.EventCardAssigned, map[string]any{
		"recipient_emails": mails,
		"card_id":          "c1",
	})
	resp := env.post(t, "/boards/B/notifications/events", "caller@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d, erwartet 202", resp.StatusCode)
	}
	ev, _ := env.spy.last()
	got := map[string]bool{}
	for _, id := range ev.ExplicitRecipientUserIDs {
		got[id] = true
	}
	if !got[zielID] {
		t.Fatalf("Der echte Empfänger fehlt in %v — 60 Schreibweisen derselben Adresse "+
			"haben das Kontingent verbraucht, die Grenze greift also vor der Entdopplung",
			ev.ExplicitRecipientUserIDs)
	}
	if len(ev.ExplicitRecipientUserIDs) != 2 {
		t.Errorf("%d Empfänger, erwartet genau 2 (die entdoppelte Adresse + das Ziel): %v",
			len(ev.ExplicitRecipientUserIDs), ev.ExplicitRecipientUserIDs)
	}
	_ = dupID
}
