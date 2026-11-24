package storage

// transfer_test.go — Tests für BoardRepo.TransferCard (#296, Sprint 43).
//
// Abgedeckt:
//   - Happy-Path: Karte aus src verschwindet, in dst angekommen, neue displayId
//   - Atomicity: src + dst beide updated_at hochgezählt
//   - Same-Board → ErrTransferSameBoard
//   - Karte nicht gefunden → ErrCardNotFound
//   - dst-Board nicht da → ErrBoardNotFound
//   - dst-Spalte unbekannt → ErrInvalidColumn
//   - Sprint-Meta-Karte → ErrTransferMetaCard
//   - UUID-Erhalt (server-weit eindeutig)
//   - Activity-Eintrag „board-transferred"
//   - Tx-Rollback bei Fehler in 2. Mutation (kein partieller Transfer)

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// newTransferTestRepo legt zwei minimal-gültige Boards an: src mit einer
// Karte, dst leer. Liefert BoardRepo + Karten-ID.
func newTransferTestRepo(t *testing.T) (*BoardRepo, string) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srcState := minimalBoardState("src", "card-uuid-1", 7)
	dstState := minimalBoardState("dst", "", 0)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"src", "Src-Board", "1", srcState, int64(100), int64(100)); err != nil {
		t.Fatalf("insert src: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"dst", "Dst-Board", "1", dstState, int64(200), int64(200)); err != nil {
		t.Fatalf("insert dst: %v", err)
	}
	return NewBoardRepo(db), "card-uuid-1"
}

// minimalBoardState liefert ein gerade noch gültiges state_json mit
// __backlog__-Lane + 3 Standardspalten. Wenn cardID != "" wird eine Karte
// dort eingefügt; nextDisplayId wird gesetzt.
func minimalBoardState(boardID, cardID string, nextDisp int) []byte {
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
			"description": "Eine Karte",
			"columnId":    "col-backlog",
			"labels":      []any{},
			"comments":    []any{},
			"activity":    []any{},
		}
		backlog["cards"] = []any{card}
		board["nextDisplayId"] = float64(nextDisp)
	}
	state, _ := json.Marshal(map[string]any{"version": "1", "board": board})
	return state
}

func TestTransferCard_HappyPath(t *testing.T) {
	ctx := context.Background()
	repo, cardID := newTransferTestRepo(t)

	res, err := repo.TransferCard(ctx, TransferCardParams{
		SrcBoardID:  "src",
		DstBoardID:  "dst",
		CardID:      cardID,
		DstColumnID: "col-in-arbeit",
		Actor:       "andi@example.com",
	})
	if err != nil {
		t.Fatalf("TransferCard: %v", err)
	}
	if res.OldDisplayID != 5 {
		t.Errorf("OldDisplayID: %d ≠ 5", res.OldDisplayID)
	}
	// dst.nextDisplayId war 0 → erster transferred = 0; im Frontend egal,
	// aber wir prüfen es deterministisch.
	if res.NewDisplayID != 0 {
		t.Errorf("NewDisplayID: %d ≠ 0", res.NewDisplayID)
	}

	// src darf die Karte nicht mehr enthalten
	srcCards := cardsInBacklog(t, repo, "src")
	if len(srcCards) != 0 {
		t.Errorf("src enthält nach Transfer %d Karten, erwartet 0", len(srcCards))
	}

	// dst muss die Karte mit gleicher UUID + neuer displayId + dst-Column enthalten
	dstCards := cardsInBacklog(t, repo, "dst")
	if len(dstCards) != 1 {
		t.Fatalf("dst enthält nach Transfer %d Karten, erwartet 1", len(dstCards))
	}
	moved := dstCards[0]
	if id, _ := moved["id"].(string); id != cardID {
		t.Errorf("UUID nach Transfer geändert: %q ≠ %q", id, cardID)
	}
	if col, _ := moved["columnId"].(string); col != "col-in-arbeit" {
		t.Errorf("columnId: %q ≠ col-in-arbeit", col)
	}
	if v, _ := moved["displayId"].(float64); int(v) != 0 {
		t.Errorf("displayId nach Transfer: %v ≠ 0", v)
	}

	// Activity muss „board-transferred" enthalten
	activity, _ := moved["activity"].([]any)
	if len(activity) == 0 {
		t.Fatal("activity-Liste leer — board-transferred-Eintrag fehlt")
	}
	found := false
	for _, a := range activity {
		aMap, _ := a.(map[string]any)
		if typ, _ := aMap["type"].(string); typ == "board-transferred" {
			found = true
			if actor, _ := aMap["actor"].(string); actor != "andi@example.com" {
				t.Errorf("Activity-Actor: %q ≠ andi@example.com", actor)
			}
			break
		}
	}
	if !found {
		t.Error("kein board-transferred-Activity-Eintrag in der transferierten Karte")
	}
}

func TestTransferCard_SameBoard(t *testing.T) {
	ctx := context.Background()
	repo, cardID := newTransferTestRepo(t)
	_, err := repo.TransferCard(ctx, TransferCardParams{
		SrcBoardID: "src", DstBoardID: "src", CardID: cardID, DstColumnID: "col-backlog", Actor: "x",
	})
	if !errors.Is(err, ErrTransferSameBoard) {
		t.Fatalf("erwartet ErrTransferSameBoard, erhalten %v", err)
	}
}

func TestTransferCard_CardNotFound(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTransferTestRepo(t)
	_, err := repo.TransferCard(ctx, TransferCardParams{
		SrcBoardID: "src", DstBoardID: "dst", CardID: "unbekannt-uuid", DstColumnID: "col-backlog", Actor: "x",
	})
	if !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("erwartet ErrCardNotFound, erhalten %v", err)
	}
}

func TestTransferCard_DstBoardNotFound(t *testing.T) {
	ctx := context.Background()
	repo, cardID := newTransferTestRepo(t)
	_, err := repo.TransferCard(ctx, TransferCardParams{
		SrcBoardID: "src", DstBoardID: "nicht-da", CardID: cardID, DstColumnID: "col-backlog", Actor: "x",
	})
	if !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("erwartet ErrBoardNotFound, erhalten %v", err)
	}
	// src muss unverändert sein (Tx-Rollback)
	srcCards := cardsInBacklog(t, repo, "src")
	if len(srcCards) != 1 {
		t.Errorf("src-Rollback fehlgeschlagen: %d Karten, erwartet 1", len(srcCards))
	}
}

func TestTransferCard_InvalidColumn(t *testing.T) {
	ctx := context.Background()
	repo, cardID := newTransferTestRepo(t)
	_, err := repo.TransferCard(ctx, TransferCardParams{
		SrcBoardID: "src", DstBoardID: "dst", CardID: cardID, DstColumnID: "col-nicht-da", Actor: "x",
	})
	if !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("erwartet ErrInvalidColumn, erhalten %v", err)
	}
	// src muss unverändert sein
	srcCards := cardsInBacklog(t, repo, "src")
	if len(srcCards) != 1 {
		t.Errorf("src-Rollback fehlgeschlagen: %d Karten, erwartet 1", len(srcCards))
	}
}

func TestTransferCard_MetaCardRejected(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// src mit Meta-Karte
	metaState, _ := json.Marshal(map[string]any{
		"version": "1",
		"board": map[string]any{
			"title": "src",
			"columnDefs": []any{
				map[string]any{"id": "col-backlog", "title": "Backlog"},
			},
			"nextDisplayId": float64(1),
			"sprints": []any{
				map[string]any{
					"id":    "__backlog__",
					"title": "Backlog",
					"kind":  "backlog",
					"cards": []any{
						map[string]any{
							"id":       "meta-uuid",
							"kind":     "sprint-meta",
							"title":    "📋 Sprint-Start",
							"columnId": "col-backlog",
						},
					},
				},
			},
		},
	})
	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('src','S','1',?,?,?)`,
		metaState, int64(100), int64(100)); err != nil {
		t.Fatalf("insert src: %v", err)
	}
	dstState := minimalBoardState("dst", "", 0)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('dst','D','1',?,?,?)`,
		dstState, int64(200), int64(200)); err != nil {
		t.Fatalf("insert dst: %v", err)
	}

	repo := NewBoardRepo(db)
	_, err = repo.TransferCard(ctx, TransferCardParams{
		SrcBoardID: "src", DstBoardID: "dst", CardID: "meta-uuid", DstColumnID: "col-backlog", Actor: "x",
	})
	if !errors.Is(err, ErrTransferMetaCard) {
		t.Fatalf("erwartet ErrTransferMetaCard, erhalten %v", err)
	}
}

func TestTransferCard_UpdatedAtIncremented(t *testing.T) {
	ctx := context.Background()
	repo, cardID := newTransferTestRepo(t)

	srcBefore, _ := repo.Get(ctx, "src")
	dstBefore, _ := repo.Get(ctx, "dst")

	if _, err := repo.TransferCard(ctx, TransferCardParams{
		SrcBoardID: "src", DstBoardID: "dst", CardID: cardID, DstColumnID: "col-backlog", Actor: "x",
	}); err != nil {
		t.Fatalf("TransferCard: %v", err)
	}

	srcAfter, _ := repo.Get(ctx, "src")
	dstAfter, _ := repo.Get(ctx, "dst")
	if srcAfter.UpdatedAt <= srcBefore.UpdatedAt {
		t.Errorf("src.updated_at nicht erhöht: %d → %d", srcBefore.UpdatedAt, srcAfter.UpdatedAt)
	}
	if dstAfter.UpdatedAt <= dstBefore.UpdatedAt {
		t.Errorf("dst.updated_at nicht erhöht: %d → %d", dstBefore.UpdatedAt, dstAfter.UpdatedAt)
	}
}

// cardsInBacklog liest die Karten der __backlog__-Lane eines Boards
// (Test-Hilfsfunktion, kein Production-Code).
func cardsInBacklog(t *testing.T, repo *BoardRepo, boardID string) []map[string]any {
	t.Helper()
	b, err := repo.Get(context.Background(), boardID)
	if err != nil {
		t.Fatalf("Get(%s): %v", boardID, err)
	}
	var state map[string]any
	if err := json.Unmarshal(b.StateJSON, &state); err != nil {
		t.Fatalf("unmarshal %s: %v", boardID, err)
	}
	board, _ := state["board"].(map[string]any)
	sprints, _ := board["sprints"].([]any)
	for _, sp := range sprints {
		spMap, _ := sp.(map[string]any)
		if id, _ := spMap["id"].(string); id == "__backlog__" {
			cards, _ := spMap["cards"].([]any)
			out := make([]map[string]any, 0, len(cards))
			for _, c := range cards {
				cMap, _ := c.(map[string]any)
				if cMap != nil {
					out = append(out, cMap)
				}
			}
			return out
		}
	}
	return nil
}

// ── F-607: BoardFuerKarte war von keinem Test erreicht ───────────────────

// TestBoardFuerKarte deckt die Lücke ab, die der Sprint-55-Go-Kritiker gefunden
// hat: `BoardFuerKarte` ist die EINZIGE Auskunft, auf der der gesamte
// Transfer-Abgleich (F-600) seine Entscheidung aufbaut — und alle sechs
// ursprünglichen Reconcile-Tests benutzten dafür eine Attrappe. Die Funktion
// selbst lief in keinem Test.
//
// Die Gegenprobe des Kritikers: `BoardFuerKarte` auf `return "", nil` verkürzt →
// die gesamte storage-Suite blieb GRÜN. Damit hätte ein Fehler in der
// JSON-Navigation jedes Staging verworfen statt fertigzustellen — F-600 in sein
// Gegenteil verkehrt, ohne dass ein Test angeschlagen hätte. Der Hinweis wäre
// eine Logzeile gewesen, die nach geordnetem Aufräumen aussieht.
//
// Genau der blinde Wächter, nach dem dieser Sprint benannt ist.
func TestBoardFuerKarte(t *testing.T) {
	repo, cardID := newTransferTestRepo(t)
	ctx := context.Background()

	t.Run("findet die Karte in ihrem Board", func(t *testing.T) {
		got, err := BoardFuerKarte(ctx, repo, cardID)
		if err != nil {
			t.Fatalf("BoardFuerKarte: %v", err)
		}
		if got != "src" {
			t.Fatalf("Board = %q, erwartet \"src\" — die JSON-Navigation trifft die "+
				"Karten nicht", got)
		}
	})

	t.Run("liefert leer für eine unbekannte Karte", func(t *testing.T) {
		got, err := BoardFuerKarte(ctx, repo, "gibt-es-nicht")
		if err != nil {
			t.Fatalf("BoardFuerKarte: %v", err)
		}
		if got != "" {
			t.Fatalf("Board = %q, erwartet \"\"", got)
		}
	})

	t.Run("leere Karten-ID fragt die Datenbank gar nicht erst", func(t *testing.T) {
		got, err := BoardFuerKarte(ctx, repo, "")
		if err != nil || got != "" {
			t.Fatalf("got=%q err=%v, erwartet \"\" ohne Fehler", got, err)
		}
	})

	t.Run("nil-Repo ist kein Absturz", func(t *testing.T) {
		got, err := BoardFuerKarte(ctx, nil, cardID)
		if err != nil || got != "" {
			t.Fatalf("got=%q err=%v, erwartet \"\" ohne Fehler", got, err)
		}
	})

	// Der Fall, der den Abgleich überhaupt erst auslöst: die Karte ist bereits
	// im Ziel-Board angekommen. Ohne diesen Test bewiese der erste nur, dass
	// die Funktion IRGENDEIN Board findet.
	t.Run("folgt der Karte nach dem Transfer ins Ziel-Board", func(t *testing.T) {
		res, err := repo.TransferCard(ctx, TransferCardParams{
			SrcBoardID: "src", DstBoardID: "dst", CardID: cardID, DstColumnID: "col-backlog",
		})
		if err != nil {
			t.Fatalf("TransferCard: %v", err)
		}
		if res == nil {
			t.Fatal("TransferCard lieferte kein Ergebnis")
		}
		got, err := BoardFuerKarte(ctx, repo, cardID)
		if err != nil {
			t.Fatalf("BoardFuerKarte nach Transfer: %v", err)
		}
		if got != "dst" {
			t.Fatalf("Board = %q, erwartet \"dst\" — der Abgleich würde das Staging "+
				"dieses Transfers verwerfen statt es fertigzustellen", got)
		}
	})
}

// TestBoardFuerKarte_UeberspringtKaputtesBoard: Ein Board mit unlesbarem
// state_json darf den Abgleich für alle anderen Boards nicht blockieren.
//
// Wichtig ist hier die Grenze, die der Test mit festhält: Die Karte gilt dann
// als „nicht gefunden" — und der Aufrufer VERWIRFT daraufhin das Staging (siehe
// F-611). Das ist heute so und wird hier festgeschrieben, damit die Folge
// sichtbar bleibt, falls jemand die Richtung ändern will.
func TestBoardFuerKarte_UeberspringtKaputtesBoard(t *testing.T) {
	repo, cardID := newTransferTestRepo(t)
	ctx := context.Background()

	// Das Board, in dem die Karte NICHT liegt, bekommt kaputtes JSON.
	if _, err := repo.db.ExecContext(ctx,
		`UPDATE boards SET state_json = ? WHERE id = ?`, []byte("{kein json"), "dst"); err != nil {
		t.Fatalf("state_json beschädigen: %v", err)
	}

	got, err := BoardFuerKarte(ctx, repo, cardID)
	if err != nil {
		t.Fatalf("ein kaputtes Board blockiert die Suche: %v", err)
	}
	if got != "src" {
		t.Fatalf("Board = %q, erwartet \"src\" — das kaputte Board hat die Suche "+
			"abgebrochen statt übersprungen zu werden", got)
	}
}
