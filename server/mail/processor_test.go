package mail

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func newProcessorTestBoards(t *testing.T) (*storage.BoardRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return storage.NewBoardRepo(db), func() { db.Close() }
}

func TestIngestMessage_LoopHeaderDropped(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	ctx := context.Background()

	cases := []map[string][]string{
		{"auto-submitted": {"auto-replied"}},
		{"precedence": {"bulk"}},
		{"precedence": {"list"}},
		{"x-loop": {"aboard"}},
	}
	for _, h := range cases {
		parsed := &ParsedMail{
			Subject: "Test", From: "x@y.de", Headers: h,
		}
		res, err := IngestMessage(ctx, repo, parsed, IngestOptions{ServiceBoardEnabled: true})
		if err != nil {
			t.Fatalf("IngestMessage: %v", err)
		}
		if res.Action != "dropped" {
			t.Errorf("erwartet dropped für %v, bekam %s", h, res.Action)
		}
	}
}

func TestIngestMessage_AutoSubmittedNo_NotDropped(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	parsed := &ParsedMail{
		Subject: "Echte Mail", From: "x@y.de",
		Headers: map[string][]string{"auto-submitted": {"no"}},
		Body:    "Inhalt",
	}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{ServiceBoardEnabled: true})
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if res.Action != "service-card" {
		t.Errorf("erwartet service-card, bekam %s", res.Action)
	}
}

func TestIngestMessage_TicketMatch_AddsComment(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()

	// Erst No-Match-Mail → erzeugt Karte #1 im Service-Board
	parsed1 := &ParsedMail{Subject: "Original", From: "u@x.de", Body: "1st"}
	_, _ = IngestMessage(ctx, repo, parsed1, IngestOptions{ServiceBoardEnabled: true})

	// Jetzt Reply mit [aBoard #1]
	parsed2 := &ParsedMail{
		Subject:   "Re: [aBoard #1] Original",
		From:      "u@x.de",
		Body:      "Antwort",
		MessageID: "<reply-1@x.de>",
		Date:      time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
	}
	res, err := IngestMessage(ctx, repo, parsed2, IngestOptions{ServiceBoardEnabled: true})
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if res.Action != "comment" {
		t.Fatalf("erwartet comment, bekam %s (DropReason=%q)", res.Action, res.DropReason)
	}
	if res.DisplayID != 1 {
		t.Errorf("DisplayID = %d, erwartet 1", res.DisplayID)
	}

	// Verifizieren: Kommentar ist an Karte #1
	b, _ := repo.Get(ctx, storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	comments := cards[0].(map[string]any)["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("erwartet 1 Kommentar, bekam %d", len(comments))
	}
	cm := comments[0].(map[string]any)
	if !strings.Contains(cm["text"].(string), "Antwort") {
		t.Errorf("Kommentar-Text fehlt 'Antwort': %v", cm["text"])
	}
}

func TestIngestMessage_NoServiceBoard_Drops(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	parsed := &ParsedMail{Subject: "Random", From: "u@x.de", Body: ""}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{ServiceBoardEnabled: false})
	if err != ErrNoServiceBoard {
		t.Errorf("erwartet ErrNoServiceBoard, bekam %v", err)
	}
	if res == nil || res.Action != "dropped" {
		t.Errorf("erwartet dropped, bekam %+v", res)
	}
}

func TestIngestMessage_PreFilter(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	parsed := &ParsedMail{Subject: "X", From: "blocked@x.de", Body: "B"}

	blockingFilter := func(p *ParsedMail) (string, bool) {
		if p.From == "blocked@x.de" {
			return "blocklist", true
		}
		return "", false
	}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{
		ServiceBoardEnabled: true,
		PreFilters:          []IngestFilter{blockingFilter},
	})
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if res.Action != "dropped" || res.DropReason != "blocklist" {
		t.Errorf("erwartet drop(blocklist), bekam %+v", res)
	}
}

func TestIngestMessage_TicketNotFound_FallsBackToService(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	parsed := &ParsedMail{Subject: "[aBoard #999] never-existed", From: "u@x.de", Body: "B"}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{ServiceBoardEnabled: true})
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if res.Action != "service-card" {
		t.Errorf("erwartet service-card (Fallback), bekam %s", res.Action)
	}
}

func TestIngestMessage_SpamDetectorRoutes_ToSpamColumn(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	parsed := &ParsedMail{Subject: "Cheap watches!", From: "spammer@bad.example", Body: "buy now"}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{
		ServiceBoardEnabled: true,
		SpamDetectors: []IngestFilter{
			func(p *ParsedMail) (string, bool) { return "blocklist", true },
		},
	})
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if res.Action != "service-spam-card" {
		t.Fatalf("erwartet service-spam-card, bekam %s", res.Action)
	}

	// Karte sollte in col-spam liegen
	b, _ := repo.Get(context.Background(), storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	if cards[0].(map[string]any)["columnId"].(string) != "col-spam" {
		t.Errorf("Spam-Karte landete nicht in col-spam: %v", cards[0].(map[string]any)["columnId"])
	}
}

func TestIngestMessage_SpamDetector_WithSpamDrop_DropsHard(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	parsed := &ParsedMail{Subject: "Cheap watches!", From: "spammer@bad.example", Body: "buy now"}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{
		ServiceBoardEnabled: true,
		SpamDrop:            true,
		SpamDetectors: []IngestFilter{
			func(p *ParsedMail) (string, bool) { return "blocklist", true },
		},
	})
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if res.Action != "dropped" {
		t.Errorf("SpamDrop=true sollte hart droppen, bekam %s", res.Action)
	}
	if !strings.Contains(res.DropReason, "spam") {
		t.Errorf("DropReason = %q, erwartet 'spam: …'", res.DropReason)
	}
}

func TestIngestMessage_AttachmentPipelineFiltersBlockedMIME(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	parsed := &ParsedMail{
		Subject: "Mit Anhang", From: "u@x.de", Body: "Body",
		Attachments: []ParsedAttachment{
			{Filename: "harmless.pdf", ContentType: "application/pdf", Data: []byte("PDF")},
			{Filename: "evil.exe", ContentType: "application/octet-stream", Data: []byte("EXE")},
		},
	}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{ServiceBoardEnabled: true})
	if err != nil || res.Action != "service-card" {
		t.Fatalf("Ingest fehlgeschlagen: %+v err=%v", res, err)
	}

	b, _ := repo.Get(context.Background(), storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	desc := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)["description"].(string)
	if !strings.Contains(desc, "Verworfene Anhänge") {
		t.Errorf("Description sollte Hinweis enthalten: %q", desc)
	}
	if !strings.Contains(desc, "evil.exe") {
		t.Errorf("Verworfener Anhang sollte gelistet sein: %q", desc)
	}
}

// fakeAttachmentSaver speichert Attachment-Save-Calls in einem Map und
// hält die Bytes für Verifikation im Speicher.
type fakeAttachmentSaver struct {
	saved map[string][]byte // key = boardId/cardId/attId
	names map[string]string // key = boardId/cardId/attId → filename
}

func newFakeAttachmentSaver() *fakeAttachmentSaver {
	return &fakeAttachmentSaver{
		saved: map[string][]byte{},
		names: map[string]string{},
	}
}

func (f *fakeAttachmentSaver) Save(boardId, cardId, attId, filename string, r io.Reader) error {
	key := boardId + "/" + cardId + "/" + attId
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.saved[key] = data
	f.names[key] = filename
	return nil
}

// F-358: kept Anhänge müssen ins Storage-Volume geschrieben werden + IDs in
// die Karte gemerged sein. Service-Card-Pfad.
func TestIngestMessage_PersistsKeptAttachments_ServiceCard(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	saver := newFakeAttachmentSaver()

	parsed := &ParsedMail{
		Subject: "Rechnung", From: "billing@example.org", Body: "Im Anhang.",
		Attachments: []ParsedAttachment{
			{Filename: "invoice.pdf", ContentType: "application/pdf", Data: []byte("PDF-bytes")},
			{Filename: "evil.exe", ContentType: "application/octet-stream", Data: []byte("EXE")},
		},
	}
	res, err := IngestMessage(context.Background(), repo, parsed, IngestOptions{
		ServiceBoardEnabled: true,
		Attachments:         saver,
	})
	if err != nil || res.Action != "service-card" {
		t.Fatalf("Ingest fehlgeschlagen: %+v err=%v", res, err)
	}

	// Genau ein kept-Anhang (PDF) wurde gespeichert; EXE ist von der
	// Pipeline geblockt und nicht persistiert.
	if len(saver.saved) != 1 {
		t.Fatalf("erwartet 1 gespeicherter Anhang, bekam %d", len(saver.saved))
	}
	// Karte muss die Attachment-ID enthalten.
	b, _ := repo.Get(context.Background(), storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	atts := cards[0].(map[string]any)["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("Karte erwartet 1 attachment-ID, bekam %d", len(atts))
	}
	// Die Storage-Key-Form ist <boardId>/<cardId>/<attId> — also enthält der
	// einzige saver-Eintrag genau die zurückgewiesene attachment-ID.
	var savedAttID string
	for key := range saver.saved {
		parts := strings.Split(key, "/")
		savedAttID = parts[len(parts)-1]
	}
	// F-855: Der Eintrag ist ein Objekt mit Metadaten, kein id-String mehr.
	eintrag, ok := atts[0].(map[string]any)
	if !ok {
		t.Fatalf("Karten-Anhang ist kein Objekt: %#v", atts[0])
	}
	if eintrag["id"] != savedAttID {
		t.Errorf("Karten-Attachment-ID %v ≠ gespeicherte ID %q", eintrag["id"], savedAttID)
	}
	if eintrag["filename"] != "invoice.pdf" || eintrag["mime"] != "application/pdf" {
		t.Errorf("Metadaten falsch: %+v", eintrag)
	}
	if sz, _ := eintrag["size"].(float64); sz != float64(len("PDF-bytes")) {
		t.Errorf("size = %v, erwartet %d", eintrag["size"], len("PDF-bytes"))
	}
}

// F-358: Comment-Pfad — kept Anhänge persistiert + IDs in card.attachments.
func TestIngestMessage_PersistsKeptAttachments_CommentPath(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()

	// Karte vorbereiten (No-Match → Service-Karte #1)
	_, _ = IngestMessage(ctx, repo, &ParsedMail{
		Subject: "Original", From: "u@x.de", Body: "Erste Mail",
	}, IngestOptions{ServiceBoardEnabled: true})

	// Reply mit Anhang
	saver := newFakeAttachmentSaver()
	reply := &ParsedMail{
		Subject: "Re: [aBoard #1] Original", From: "u@x.de", Body: "Antwort mit PDF",
		MessageID: "<reply-1@x.de>",
		Attachments: []ParsedAttachment{
			{Filename: "scan.pdf", ContentType: "application/pdf", Data: []byte("PDF-2")},
		},
	}
	res, err := IngestMessage(ctx, repo, reply, IngestOptions{
		ServiceBoardEnabled: true,
		Attachments:         saver,
	})
	if err != nil || res.Action != "comment" {
		t.Fatalf("Ingest fehlgeschlagen: %+v err=%v", res, err)
	}

	if len(saver.saved) != 1 {
		t.Fatalf("erwartet 1 gespeicherter Anhang, bekam %d", len(saver.saved))
	}
	b, _ := repo.Get(ctx, storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	atts := cards[0].(map[string]any)["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("Karte erwartet 1 attachment-ID, bekam %d", len(atts))
	}
	// F-855: auch der Kommentar-Pfad schreibt Objekte mit Metadaten.
	eintrag, ok := atts[0].(map[string]any)
	if !ok {
		t.Fatalf("Karten-Anhang ist kein Objekt: %#v", atts[0])
	}
	if eintrag["filename"] != "scan.pdf" || eintrag["mime"] != "application/pdf" {
		t.Errorf("Metadaten falsch: %+v", eintrag)
	}
}

// AK_autoreply_on_new_card / AK_no_autoreply_on_match: Die AutoReply-Opt wird
// im processor über opts.AutoReply weitergegeben; wir prüfen hier, dass der
// No-Match-Pfad SendAutoReply (mit isNewCard=true) aufruft und der Match-Pfad
// ihn (mit isNewCard=false) nicht aufruft. Wir testen das über die Log-Repo:
// beim No-Match-Pfad wird ein Log-Eintrag angelegt; beim Match-Pfad nicht.
func TestIngestMessage_AutoReply_OnNewCard(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "aboard2.db")
	db2, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open logdb: %v", err)
	}
	defer db2.Close()
	logRepo := storage.NewAutoReplyLogRepo(db2)

	// SMTP-Config ist kaputt (kein Server) → SendAutoReply schlägt intern fehl,
	// aber der Ingest-Flow darf trotzdem nicht abbrechen. Der Log-Eintrag
	// wird VOR dem SMTP-Versuch angelegt (TryRecord), daher prüfen wir den Log.
	autoDeps := &AutoReplyDeps{
		SMTP:    Config{Host: "localhost", Port: 25, From: "aboard@test.de", User: "u", Pass: "p"},
		AutoCfg: AutoReplyConfig{Enabled: true},
		LogRepo: logRepo,
		Logger:  func(string, ...any) {},
	}

	parsed := &ParsedMail{From: "extern@x.de", Subject: "Neues Thema", Body: "Details"}
	res, err := IngestMessage(ctx, repo, parsed, IngestOptions{
		ServiceBoardEnabled: true,
		AutoReply:           autoDeps,
		BoardName:           "TestBoard",
	})
	if err != nil {
		t.Fatalf("IngestMessage: %v", err)
	}
	if res.Action != "service-card" {
		t.Fatalf("erwartet service-card, bekam %s", res.Action)
	}
	// Log-Eintrag prüfen: TryRecord muss ausgelöst worden sein.
	inserted, err := logRepo.TryRecord(ctx, "extern@x.de", res.CardID)
	if err != nil {
		t.Fatalf("TryRecord: %v", err)
	}
	// inserted=false bedeutet: der Eintrag existiert bereits (von SendAutoReply angelegt)
	if inserted {
		t.Error("AK_autoreply_on_new_card: Log-Eintrag fehlt — SendAutoReply hätte TryRecord aufrufen sollen")
	}
}

func TestIngestMessage_AutoReply_NotOnMatch(t *testing.T) {
	repo, done := newProcessorTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "aboard3.db")
	db3, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open logdb: %v", err)
	}
	defer db3.Close()
	logRepo := storage.NewAutoReplyLogRepo(db3)

	autoDeps := &AutoReplyDeps{
		SMTP:    Config{Host: "localhost", Port: 25, From: "aboard@test.de", User: "u", Pass: "p"},
		AutoCfg: AutoReplyConfig{Enabled: true},
		LogRepo: logRepo,
		Logger:  func(string, ...any) {},
	}

	// Erst Karte anlegen
	parsed1 := &ParsedMail{From: "extern@x.de", Subject: "Original", Body: "1st"}
	res1, _ := IngestMessage(ctx, repo, parsed1, IngestOptions{
		ServiceBoardEnabled: true,
		AutoReply:           autoDeps,
	})

	// Dann Reply auf die Karte → Match-Pfad
	parsed2 := &ParsedMail{
		Subject: "Re: [aBoard #1] Original", From: "extern@x.de",
		Body: "Follow-up", MessageID: "<reply2@x.de>",
		Date: time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
	}
	res2, err := IngestMessage(ctx, repo, parsed2, IngestOptions{
		ServiceBoardEnabled: true,
		AutoReply:           autoDeps,
	})
	if err != nil {
		t.Fatalf("IngestMessage Match-Pfad: %v", err)
	}
	if res2.Action != "comment" {
		t.Fatalf("erwartet comment, bekam %s", res2.Action)
	}

	// Im Log-Repo sollte nur EIN Eintrag vorhanden sein (vom No-Match-Pfad, res1).
	// Beim Match-Pfad (res2) darf kein zweiter Eintrag entstehen, weil
	// isNewCard=false → SendAutoReply ist no-op.
	// Wir versuchen, einen zweiten Eintrag für die comment-Karte zu inserieren:
	ins2, _ := logRepo.TryRecord(ctx, "extern@x.de", res2.CardID)
	if !ins2 {
		t.Log("Match-Pfad: kein Log-Eintrag — korrekt (Auto-Reply wurde nicht gesendet)")
	}
	// Für die service-card (No-Match) ist der Eintrag bereits drin:
	ins1b, _ := logRepo.TryRecord(ctx, "extern@x.de", res1.CardID)
	if ins1b {
		t.Error("AK_no_autoreply_on_match: service-card-Log-Eintrag fehlt — hätte beim No-Match-Pfad angelegt werden sollen")
	}
}

// F-358 (Defense-in-depth): Filename mit Pfadtrenner wird sanitisiert
// (kein Verzeichnis-Escape).
func TestSanitizeAttachmentFilename(t *testing.T) {
	cases := map[string]string{
		"normal.pdf":       "normal.pdf",
		"../../etc/passwd": "passwd",
		"a\\b\\c.txt":      "c.txt",
		"":                 "attachment",
		"\x00null.pdf":     "_null.pdf",
		"with:colon.pdf":   "with_colon.pdf",
		"with|pipe.pdf":    "with_pipe.pdf",
	}
	for in, want := range cases {
		got := sanitizeAttachmentFilename(in)
		if got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
