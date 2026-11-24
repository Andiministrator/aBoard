package api

// kommentar_anhaenge_test.go — Karte #678 (ADR-0135): Dateien an Kommentaren
// fallen mit dem Kommentar, und nur dann.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

// kommentarStand: Karte c1 mit zwei Kommentaren; k1 trägt die Datei a1 (und
// ist Cover), k2 die Datei a2, a3 ist ein gewöhnlicher Karten-Anhang, a4
// Mail-Altbestand als reiner id-String.
const kommentarStand = `{"version":"2","board":{"id":"b1","title":"T","columnDefs":[{"id":"col-ready","title":"Ready"}],
 "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"c1","title":"K","columnId":"col-ready",
  "coverAttachmentId":"a1",
  "comments":[{"id":"k1","author":"Andi","text":"mit Datei","created":"2026-10-04"},
              {"id":"k2","author":"Andi","text":"auch","created":"2026-10-04"}],
  "attachments":[{"id":"a1","filename":"bild.png","mime":"image/png","size":3,"commentId":"k1"},
                 {"id":"a2","filename":"b.txt","mime":"text/plain","size":3,"commentId":"k2"},
                 {"id":"a3","filename":"c.txt","mime":"text/plain","size":3},
                 "a4"]}]}]}}`

func kommentarOp(t *testing.T, typ string, payload any) serverOp {
	t.Helper()
	roh, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return serverOp{ID: "op-" + typ, TS: "2026-10-04T12:00:00.000Z", Actor: "Andi", SchemaVersion: 1, Type: typ, Payload: roh}
}

func falteKommentarOps(t *testing.T, ops []serverOp) string {
	t.Helper()
	m, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	opsJSON, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	erg, err := m.Falte(context.Background(), kommentarStand, string(opsJSON), "")
	if err != nil {
		t.Fatal(err)
	}
	return erg.Projektion
}

// Der echte Fold (Bündel aus src/ops.js) nimmt mit dem Kommentar seine
// Anhang-Einträge und den Cover-Verweis — und nur diese.
func TestKommentarDeleteNimmtAnhangEintraegeMit(t *testing.T) {
	op := kommentarOp(t, "comment.delete", map[string]string{"cardId": "c1", "commentId": "k1"})
	nachher := falteKommentarOps(t, []serverOp{op})

	var root struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					Cover       *string           `json:"coverAttachmentId"`
					Attachments []json.RawMessage `json:"attachments"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(nachher), &root); err != nil {
		t.Fatal(err)
	}
	karte := root.Board.Sprints[0].Cards[0]
	var ids []string
	for _, roh := range karte.Attachments {
		s := string(roh)
		ids = append(ids, s)
	}
	alle := strings.Join(ids, " ")
	if strings.Contains(alle, `"a1"`) {
		t.Errorf("Anhang a1 des gelöschten Kommentars steht noch an der Karte: %s", alle)
	}
	for _, bleibt := range []string{`"a2"`, `"a3"`, `"a4"`} {
		if !strings.Contains(alle, bleibt) {
			t.Errorf("Anhang %s ist mit weggefallen: %s", bleibt, alle)
		}
	}
	if karte.Cover != nil {
		t.Errorf("coverAttachmentId zeigt noch auf %q", *karte.Cover)
	}

	got := kommentarAnhaengeZumLoeschen([]byte(kommentarStand), []byte(nachher), []serverOp{op})
	if len(got) != 1 || got[0] != (kommentarAnhang{CardID: "c1", AttID: "a1"}) {
		t.Fatalf("zu löschende Dateien = %+v, erwartet genau c1/a1", got)
	}
}

// Ohne comment.delete wird keine Datei gelöscht — auch dann nicht, wenn ein
// card.upsert die Anhänge der Karte verliert (F-926): Der Eintrag ist aus dem
// Verlauf wiederherstellbar, die Datei nicht.
func TestKommentarAnhangOhneCommentDeleteBleibt(t *testing.T) {
	op := kommentarOp(t, "card.upsert", map[string]any{
		"sprintId": "s1", "columnId": "col-ready",
		"card": map[string]any{"id": "c1", "title": "ohne Anhänge"},
	})
	nachher := falteKommentarOps(t, []serverOp{op})
	if strings.Contains(nachher, `"a1"`) {
		t.Fatalf("Vorbedingung: der Voll-Upsert sollte die Anhänge verlieren")
	}
	if got := kommentarAnhaengeZumLoeschen([]byte(kommentarStand), []byte(nachher), []serverOp{op}); len(got) != 0 {
		t.Fatalf("ohne comment.delete darf nichts gelöscht werden, bekommen %+v", got)
	}
}

// comment.delete auf einen Kommentar ohne Dateien und unlesbarer Stand
// liefern nichts.
func TestKommentarAnhangGrenzfaelle(t *testing.T) {
	ohneDatei := kommentarOp(t, "comment.delete", map[string]string{"cardId": "c1", "commentId": "k-gibt-es-nicht"})
	nachher := falteKommentarOps(t, []serverOp{ohneDatei})
	if got := kommentarAnhaengeZumLoeschen([]byte(kommentarStand), []byte(nachher), []serverOp{ohneDatei}); len(got) != 0 {
		t.Errorf("Kommentar ohne Dateien: bekommen %+v", got)
	}
	k1 := kommentarOp(t, "comment.delete", map[string]string{"cardId": "c1", "commentId": "k1"})
	if got := kommentarAnhaengeZumLoeschen([]byte("kein json"), []byte(kommentarStand), []serverOp{k1}); got != nil {
		t.Errorf("unlesbarer Stand vorher: bekommen %+v", got)
	}
	// Steht die Datei nach dem Fold noch an der Karte (etwa ein Fold ohne
	// die Kaskade), bleibt sie — gelöscht wird nur, was der Fold entfernt hat.
	if got := kommentarAnhaengeZumLoeschen([]byte(kommentarStand), []byte(kommentarStand), []serverOp{k1}); len(got) != 0 {
		t.Errorf("Eintrag unverändert vorhanden: bekommen %+v", got)
	}
}

// loescheKommentarAnhaenge entfernt die Datei aus der Karten-Ablage und lässt
// die übrigen Dateien der Karte stehen.
func TestLoescheKommentarAnhaengeEntferntNurDieGenannten(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAttachmentStore(dir)
	for _, id := range []string{"a1", "a2"} {
		if err := store.Save("b1", "c1", id, id+".txt", strings.NewReader("xyz")); err != nil {
			t.Fatal(err)
		}
	}
	a := &BoardsAPI{attach: store}
	a.loescheKommentarAnhaenge("b1", []kommentarAnhang{{CardID: "c1", AttID: "a1"}, {CardID: "../x", AttID: "a2"}})

	if _, _, err := store.Open("b1", "c1", "a1"); !errors.Is(err, storage.ErrAttachmentNotFound) {
		t.Errorf("a1 sollte gelöscht sein, Open: %v", err)
	}
	if rc, _, err := store.Open("b1", "c1", "a2"); err != nil {
		t.Errorf("a2 muss bleiben (ungültige Karten-id im Auftrag), Open: %v", err)
	} else {
		rc.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "b1", "c1")); err != nil {
		t.Errorf("Karten-Verzeichnis muss bleiben: %v", err)
	}

	// Ohne Store (Test-Setups ohne Volume) kein Absturz.
	(&BoardsAPI{}).loescheKommentarAnhaenge("b1", []kommentarAnhang{{CardID: "c1", AttID: "a2"}})
}
