package api_test

// activity_test.go — Integrations-Tests für das server-seitige Activity-Log
// (Karte #200, ADR-0037). Prüft:
//
//  - Board-create loggt "board-created"-Eintrag
//  - Board-update loggt "board-updated"-Eintrag
//  - Board-delete: Eintrag wird per ON DELETE CASCADE mitgelöscht
//  - ACL-set loggt "acl-set"-Eintrag
//  - ACL-delete loggt "acl-deleted"-Eintrag
//  - Legacy-Bypass (kein Forward-Auth): actor = '', kein Crash
//  - activities = nil (kein WithActivities): Mutation weiterhin erfolgreich

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// activityTestEnv bündelt Server + Repos für Activity-Tests.
type activityTestEnv struct {
	srv          *httptest.Server
	db           *sql.DB
	activityRepo *storage.ActivityRepo
}

func (e *activityTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

func newActivityAPITestEnv(t *testing.T) *activityTestEnv {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	repo := storage.NewBoardRepo(db)
	aclRepo := storage.NewAclRepo(db)
	actRepo := storage.NewActivityRepo(db)

	mux := http.NewServeMux()
	boardsAPI := api.NewBoardsAPI(repo).
		WithACL(aclRepo).
		WithActivities(actRepo)
	boardsAPI.Register(mux)

	return &activityTestEnv{
		srv:          httptest.NewServer(mux),
		db:           db,
		activityRepo: actRepo,
	}
}

// listActivities ist ein Hilfswrapper für Tests.
func listActivities(t *testing.T, repo *storage.ActivityRepo, boardID string) []storage.ActivityEntry {
	t.Helper()
	entries, err := repo.List(context.Background(), boardID, 0)
	if err != nil {
		t.Fatalf("List activities: %v", err)
	}
	return entries
}

// ── Board-create loggt Eintrag ────────────────────────────────────────────

func TestActivityLog_BoardCreate(t *testing.T) {
	env := newActivityAPITestEnv(t)
	defer env.close()

	resp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("act1", "Aktivitäts-Test"), nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /boards: Status %d", resp.StatusCode)
	}

	entries := listActivities(t, env.activityRepo, "act1")
	if len(entries) != 1 {
		t.Fatalf("erwartet 1 Activity-Eintrag nach create, erhalten %d", len(entries))
	}
	if entries[0].Action != "board-created" {
		t.Errorf("Action: %q ≠ board-created", entries[0].Action)
	}
	// Legacy-Bypass: kein Forward-Auth-Header → actor leer.
	if entries[0].Actor != "" {
		t.Errorf("Actor sollte leer sein (Legacy-Bypass), ist %q", entries[0].Actor)
	}
	if entries[0].Note != "Aktivitäts-Test" {
		t.Errorf("Note sollte den Board-Titel enthalten, ist %q", entries[0].Note)
	}
}

// ── Board-update loggt Eintrag ────────────────────────────────────────────

func TestActivityLog_BoardUpdate(t *testing.T) {
	env := newActivityAPITestEnv(t)
	defer env.close()

	create := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("upd1", "Orig"), nil))
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: Status %d", create.StatusCode)
	}
	etag := create.Header.Get("ETag")

	putReq := mustReq(t, "PUT", env.srv.URL+"/boards/upd1", sampleBody("upd1", "Geändert"), http.Header{"If-Match": {etag}})
	update := mustDo(t, putReq)
	update.Body.Close()
	if update.StatusCode != http.StatusOK {
		t.Fatalf("PUT /boards/upd1: Status %d", update.StatusCode)
	}

	entries := listActivities(t, env.activityRepo, "upd1")
	// create + update = 2 Einträge.
	if len(entries) != 2 {
		t.Fatalf("erwartet 2 Activity-Einträge, erhalten %d", len(entries))
	}
	// Neueste zuerst (List sortiert DESC).
	if entries[0].Action != "board-updated" {
		t.Errorf("neuester Eintrag: Action %q ≠ board-updated", entries[0].Action)
	}
}

// ── Board-delete: Einträge werden per Cascade gelöscht ───────────────────

func TestActivityLog_BoardDelete_Cascade(t *testing.T) {
	env := newActivityAPITestEnv(t)
	defer env.close()

	create := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("del1", "ZuLöschen"), nil))
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: Status %d", create.StatusCode)
	}

	del := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/del1", nil, nil))
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /boards/del1: Status %d", del.StatusCode)
	}

	// Nach Board-Delete muss die DB keine Activities mehr enthalten (Cascade).
	var count int
	if err := env.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM activities WHERE board_id = 'del1'`).Scan(&count); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if count != 0 {
		t.Errorf("erwartet 0 Activities nach Board-Delete (Cascade), erhalten %d", count)
	}
}

// ── ACL-set loggt Eintrag ─────────────────────────────────────────────────

func TestActivityLog_ACLSet(t *testing.T) {
	env := newActivityAPITestEnv(t)
	defer env.close()

	create := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("acls1", "ACL-Board"), nil))
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: Status %d", create.StatusCode)
	}

	body := []byte(`{"email":"viewer@example.com","role":"viewer"}`)
	aclSet := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards/acls1/acl", body, nil))
	aclSet.Body.Close()
	if aclSet.StatusCode != http.StatusOK {
		t.Fatalf("POST /boards/acls1/acl: Status %d", aclSet.StatusCode)
	}

	entries := listActivities(t, env.activityRepo, "acls1")
	// create + acl-set = mindestens 2 Einträge.
	if len(entries) < 2 {
		t.Fatalf("erwartet ≥2 Activity-Einträge, erhalten %d", len(entries))
	}
	// Neuester Eintrag ist acl-set.
	if entries[0].Action != "acl-set" {
		t.Errorf("neuester Eintrag: Action %q ≠ acl-set", entries[0].Action)
	}
	if entries[0].Note != "viewer@example.com" {
		t.Errorf("Note sollte Ziel-Email enthalten, ist %q", entries[0].Note)
	}
}

// ── ACL-delete loggt Eintrag ──────────────────────────────────────────────

func TestActivityLog_ACLDelete(t *testing.T) {
	env := newActivityAPITestEnv(t)
	defer env.close()

	create := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("acld1", "ACL-Del-Board"), nil))
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: Status %d", create.StatusCode)
	}

	// ACL-Eintrag setzen.
	body := []byte(`{"email":"del@example.com","role":"viewer"}`)
	aclSet := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards/acld1/acl", body, nil))
	aclSet.Body.Close()
	if aclSet.StatusCode != http.StatusOK {
		t.Fatalf("POST /boards/acld1/acl: Status %d", aclSet.StatusCode)
	}

	// ACL-Eintrag löschen.
	aclDel := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/acld1/acl?email=del@example.com", nil, nil))
	aclDel.Body.Close()
	if aclDel.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /boards/acld1/acl: Status %d", aclDel.StatusCode)
	}

	entries := listActivities(t, env.activityRepo, "acld1")
	// Neuester Eintrag muss acl-deleted sein.
	if len(entries) == 0 {
		t.Fatal("keine Activity-Einträge vorhanden")
	}
	if entries[0].Action != "acl-deleted" {
		t.Errorf("neuester Eintrag: Action %q ≠ acl-deleted", entries[0].Action)
	}
	if entries[0].FromVal != "del@example.com" {
		t.Errorf("FromVal sollte gelöschte Email enthalten, ist %q", entries[0].FromVal)
	}
}

// ── Ohne WithActivities: kein Crash, Mutation erfolgreich ────────────────

func TestActivityLog_NilRepo_NoCrash(t *testing.T) {
	// activities = nil (kein WithActivities) → Mutations müssen trotzdem funktionieren.
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	// Bewusst KEIN WithActivities.
	api.NewBoardsAPI(storage.NewBoardRepo(db)).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := mustDo(t, mustReq(t, "POST", srv.URL+"/boards", sampleBody("nil1", "Ohne-Log"), nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /boards ohne Activities-Repo: Status %d", resp.StatusCode)
	}
}

// ── F-259: Board-Delete erzeugt keine garantierte Fehler-Zeile ────────────

// Bis F-259 rief der Delete-Handler `recordActivity(..., "board-deleted", ...)`
// NACH `repo.Delete`. Der Insert konnte nie gelingen (activities hängt per
// ON DELETE CASCADE am Board, `foreign_keys` ist aktiv, der Delete war
// committet) — jeder erfolgreiche Löschvorgang hinterliess also eine
// `activity-log-fehler`-Zeile im Server-Log. Der Test hält fest, dass der
// Normalfall stumm bleibt; der Cascade-Test darüber deckt weiterhin ab, dass
// auch nichts geschrieben wird.
func TestActivityLog_BoardDelete_KeineFehlerZeile(t *testing.T) {
	env := newActivityAPITestEnv(t)
	defer env.close()

	create := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("del2", "Stiller Delete"), nil))
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: Status %d", create.StatusCode)
	}

	buf := logMitschnitt(t)
	del := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/del2", nil, nil))
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /boards/del2: Status %d", del.StatusCode)
	}

	if ausgabe := buf.String(); strings.Contains(ausgabe, "activity-log-fehler") {
		t.Errorf("erfolgreicher Board-Delete hat eine Activity-Log-Fehlerzeile "+
			"erzeugt (F-259).\nLog:\n%s", ausgabe)
	}
}

// ── Karte #554: Protokoll je Op statt Sammel-Eintrag je Batch ────────────

// newOpsActivitiesEnv baut eine boardsTestEnv MIT Fold-Motor UND
// ActivityRepo — für Tests, die POST /boards/{id}/ops UND das Protokoll
// brauchen (Karte #554, ADR-0129 §5). testMotor/legeOpsBoardAn/postOps
// stammen aus boards_ops_test.go (dasselbe Package).
func newOpsActivitiesEnv(t *testing.T) (*boardsTestEnv, *storage.ActivityRepo) {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	actRepo := storage.NewActivityRepo(db)
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).
		WithFold(testMotor).
		WithActivities(actRepo).
		Register(mux)
	env := &boardsTestEnv{srv: httptest.NewServer(mux), db: db}
	t.Cleanup(env.close)
	return env, actRepo
}

// TestActivityLog_Ops_ProtokollJeOp sichert Karte #554 ab: POST
// /boards/{id}/ops schreibt seit Migration 0016 je Op einen eigenen
// Activity-Eintrag (action="op", card_id/op_type gefüllt) statt EINES
// Sammel-Eintrags ("board-ops", Karte #552). card_id wird aus dem
// Op-Payload abgeleitet: direkt aus `cardId` (card.field, comment.add), bei
// card.upsert aus `card.id` (F-451: eigenes Payload-Schema).
//
// GEGENPROBE (siehe Bericht): Ersetzt man in cardIDFromOp (boards_ops.go)
// den Rückgabewert durch immer "", wird dieser Test rot (card_id-Zuordnung
// verifizierbar leer).
func TestActivityLog_Ops_ProtokollJeOp(t *testing.T) {
	env, actRepo := newOpsActivitiesEnv(t)
	legeOpsBoardAn(t, env)

	code, out := postOps(t, env, "b1", `[
	 {"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}},
	 {"type":"comment.add","payload":{"cardId":"k2","comment":{"id":"c1","author":"Agent","text":"hallo","created":"2026-09-28"}}},
	 {"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k3","title":"Neu","description":"","owner":"",
	   "assignees":[],"labels":[],"comments":[],"activity":[],"links":[],"qa":false,"critical":false,"blocked":false,"archived":false}}}
	]`)
	if code != http.StatusOK {
		t.Fatalf("Status %d: %v", code, out)
	}

	entries := listActivities(t, actRepo, "b1")
	var opEntries []storage.ActivityEntry
	for _, e := range entries {
		if e.Action == "board-ops" {
			t.Errorf("Sammel-Eintrag 'board-ops' hätte durch Protokoll je Op ersetzt sein müssen: %+v", e)
		}
		if e.Action == "op" {
			opEntries = append(opEntries, e)
		}
	}
	if len(opEntries) != 3 {
		t.Fatalf("erwartet 3 Op-Einträge, erhalten %d: %+v", len(opEntries), opEntries)
	}
	byCardOp := map[string]storage.ActivityEntry{}
	for _, e := range opEntries {
		byCardOp[e.CardID+"|"+e.OpType] = e
	}
	if e, ok := byCardOp["k1|card.field"]; !ok || e.ActorKind != "mensch" {
		t.Errorf("card.field-Eintrag fehlt oder ActorKind falsch: %+v (ok=%v)", e, ok)
	}
	if _, ok := byCardOp["k2|comment.add"]; !ok {
		t.Errorf("comment.add-Eintrag fehlt (card_id aus payload.cardId): %+v", opEntries)
	}
	if _, ok := byCardOp["k3|card.upsert"]; !ok {
		t.Errorf("card.upsert-Eintrag fehlt (card_id aus payload.card.id): %+v", opEntries)
	}
}

// TestActivityLog_Ops_ProtokollFehlerSchreibtKeinBoard sichert F-1156 ab:
// Board und Protokoll je Op werden nur gemeinsam wirksam. Scheitert der
// Protokolleintrag, bleibt auch das Board unverändert, und der Aufrufer
// bekommt einen Fehler statt 200.
//
// Der Fehler kommt aus einem Trigger in der eigenen Test-DB (kein Eingriff
// von aussen). GEGENPROBE: Schreibt postOps das Protokoll wieder nach dem
// COMMIT (RecordOps in eigener Transaktion, Fehler nur geloggt), antwortet
// der Server 200 und k1 steht auf qa=true — der Test wird rot.
func TestActivityLog_Ops_ProtokollFehlerSchreibtKeinBoard(t *testing.T) {
	env, actRepo := newOpsActivitiesEnv(t)
	legeOpsBoardAn(t, env)
	if _, err := env.db.Exec(`CREATE TRIGGER protokoll_kaputt BEFORE INSERT ON activities
		BEGIN SELECT RAISE(ABORT, 'protokoll kaputt'); END`); err != nil {
		t.Fatal(err)
	}

	code, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`)
	if code != http.StatusInternalServerError {
		t.Fatalf("Status %d, erwartet 500 (Protokoll gescheitert): %v", code, out)
	}
	s := leseOpsBoard(t, env)
	sp, i := s.karte("k1")
	for _, x := range s.Board.Sprints {
		if x.ID == sp && x.Cards[i].QA {
			t.Fatal("k1 steht auf qa=true — das Board wurde ohne Protokolleintrag geschrieben")
		}
	}
	if _, err := env.db.Exec(`DROP TRIGGER protokoll_kaputt`); err != nil {
		t.Fatal(err)
	}
	eintraege, err := actRepo.List(context.Background(), "b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eintraege {
		if e.Action == "op" {
			t.Fatalf("Protokolleintrag trotz Rollback: %+v", e)
		}
	}

	// Rückweg: Ohne Trigger wirkt derselbe Batch, mit genau einem Eintrag.
	if code, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`); code != http.StatusOK {
		t.Fatalf("nach Reparatur: Status %d: %v", code, out)
	}
	eintraege, _ = actRepo.List(context.Background(), "b1", 0)
	n := 0
	for _, e := range eintraege {
		if e.Action == "op" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d Protokolleinträge, erwartet 1", n)
	}
}
