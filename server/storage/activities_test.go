package storage

// activities_test.go — Tests für ActivityRepo (Karte #200, ADR-0037;
// ergänzt um card_id/op_type/actor_kind und die Frist-Löschung, Migration
// 0016, ADR-0129 §5, Karte #554).
//
// Abgedeckt:
//  - Record + List (Basis-Round-Trip)
//  - Legacy-Bypass: actor = '' kein Fehler
//  - Frist-Löschung (ADR-0129 §5, ersetzt die Kappung ActivityCap=500/F-159):
//    Altes weg, Junges bleibt; Sekunden-Kürzung im Vergleich (kein
//    Fraktions-Artefakt)
//  - RecordOps: n Einträge atomar, Felder korrekt, Rollback bei CHECK-Fehler
//  - ListFiltered: Karten-Filter
//  - List mit Limit
//  - ON DELETE CASCADE: Activity-Zeilen verschwinden mit dem Board

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// newActivityTestDB öffnet eine temporäre DB mit allen Migrationen.
func newActivityTestDB(t *testing.T) *ActivityRepo {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Testboard anlegen (FK-Referenz für activities).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 'Test-Board', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	return NewActivityRepo(db)
}

func TestActivityRepo_RecordAndList(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	// Basis-Eintrag: Board-create mit Akteur.
	if err := repo.Record(ctx, "b1", "user@example.com", "board-created", "", "", "", "Test-Board"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	entries, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("erwartet 1 Eintrag, erhalten %d", len(entries))
	}
	e := entries[0]
	if e.BoardID != "b1" {
		t.Errorf("BoardID: %q ≠ %q", e.BoardID, "b1")
	}
	if e.Actor != "user@example.com" {
		t.Errorf("Actor: %q ≠ %q", e.Actor, "user@example.com")
	}
	if e.Action != "board-created" {
		t.Errorf("Action: %q ≠ %q", e.Action, "board-created")
	}
	if e.Note != "Test-Board" {
		t.Errorf("Note: %q ≠ %q", e.Note, "Test-Board")
	}
	if e.TS == "" {
		t.Error("TS darf nicht leer sein")
	}
}

func TestActivityRepo_LegacyBypass_EmptyActor(t *testing.T) {
	// Legacy-Bypass (ADR-0059 §2): kein Forward-Auth → actor = '' — kein Crash,
	// kein Pflichtfeld-Fehler, Eintrag wird normal gespeichert.
	ctx := context.Background()
	repo := newActivityTestDB(t)

	if err := repo.Record(ctx, "b1", "", "board-updated", "title", "", "Neuer Titel", ""); err != nil {
		t.Fatalf("Record mit leerem Actor: %v", err)
	}

	entries, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("erwartet 1 Eintrag, erhalten %d", len(entries))
	}
	if entries[0].Actor != "" {
		t.Errorf("Actor sollte leer sein, ist %q", entries[0].Actor)
	}
}

// TestActivityRepo_FristLoeschtAltesBehaeltJunges ersetzt die frühere
// Cap-Prüfung (TestActivityRepo_Trimming, F-159/ActivityCap=500): seit
// Migration 0016 (Karte #554) gilt eine Frist statt einer Höchstzahl.
//
// GEGENPROBE: Wird der `_ = r.trim(ctx, boardID, 1)`-Aufruf in Record()
// entfernt, bleibt der "alt"-Eintrag stehen und der Test wird rot.
func TestActivityRepo_FristLoeschtAltesBehaeltJunges(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(activityRetentionDaysEnvVar, "10")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Alter Eintrag: 20 Tage vor "jetzt" — bei Frist 10 Tage muss er weg sein.
	timeNow = func() time.Time { return base.AddDate(0, 0, -20) }
	if err := repo.Record(ctx, "b1", "u@example.com", "board-updated", "", "", "", "alt"); err != nil {
		t.Fatalf("Record alt: %v", err)
	}

	// Junger Eintrag: 2 Tage vor "jetzt" — muss bleiben.
	timeNow = func() time.Time { return base.AddDate(0, 0, -2) }
	if err := repo.Record(ctx, "b1", "u@example.com", "board-updated", "", "", "", "jung"); err != nil {
		t.Fatalf("Record jung: %v", err)
	}

	// "Jetzt": löst den entscheidenden Trim aus.
	timeNow = func() time.Time { return base }
	if err := repo.Record(ctx, "b1", "u@example.com", "board-updated", "", "", "", "jetzt"); err != nil {
		t.Fatalf("Record jetzt: %v", err)
	}

	entries, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	notes := map[string]bool{}
	for _, e := range entries {
		notes[e.Note] = true
	}
	if notes["alt"] {
		t.Error("alter Eintrag (20 Tage) hätte bei Frist 10 Tage gelöscht werden müssen")
	}
	if !notes["jung"] {
		t.Error("junger Eintrag (2 Tage) hätte bei Frist 10 Tage bleiben müssen")
	}
	if !notes["jetzt"] {
		t.Error("aktueller Eintrag fehlt")
	}
}

// TestActivityRepo_Frist_KeineFraktionsUnschaerfe: Ein Eintrag in genau der
// Stichtags-Sekunde, aber mit Sekundenbruchteil, ist jünger als der Stichtag
// und darf NICHT gelöscht werden. Bis F-1157 hing das an substr(ts, 1, 19);
// seit Migration 0019 an der festen Breite von ts (24 Zeichen) gegenüber dem
// 19-stelligen Stichtag — der längere String mit gleichem Anfang ist größer.
//
// GEGENPROBE (Karte #582): Hängt trim dem Stichtag ein "Z" an (volle
// ISO-Form, "…12:00:00Z"), sortiert der Grenzfall "…12:00:00.500Z" darunter
// ('.' < 'Z') und wird gelöscht — der Test wird rot.
func TestActivityRepo_Frist_KeineFraktionsUnschaerfe(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(activityRetentionDaysEnvVar, "10")

	cutoffSecond := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// 0,5 s NACH dem Cutoff, aber innerhalb derselben Sekunde — trägt einen
	// Sekundenbruchteil im ts (nowISO/RFC3339Nano).
	entryTime := cutoffSecond.Add(500 * time.Millisecond)

	timeNow = func() time.Time { return entryTime }
	if err := repo.Record(ctx, "b1", "u@example.com", "board-updated", "", "", "", "grenzfall"); err != nil {
		t.Fatalf("Record grenzfall: %v", err)
	}

	// Löst den Trim mit Cutoff = cutoffSecond aus (Frist 10 Tage vorher).
	timeNow = func() time.Time { return cutoffSecond.AddDate(0, 0, 10) }
	if err := repo.Record(ctx, "b1", "u@example.com", "board-updated", "", "", "", "ausloeser"); err != nil {
		t.Fatalf("Record ausloeser: %v", err)
	}

	entries, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Note == "grenzfall" {
			found = true
		}
	}
	if !found {
		t.Error("Eintrag 0,5s NACH dem Cutoff (aber in derselben Sekunde) wurde fälschlich gelöscht — Sekunden-Kürzung fehlt im Vergleich")
	}
}

// TestActivityRepo_RecordOps_FelderKorrekt prüft, dass RecordOps card_id,
// op_type und actor_kind korrekt schreibt — inklusive Normalisierung von
// ActorKind="" auf "mensch".
func TestActivityRepo_RecordOps_FelderKorrekt(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	entries := []OpActivityEntry{
		{CardID: "k1", OpType: "card.field", ActorKind: "mensch"},
		{CardID: "k2", OpType: "comment.add", ActorKind: ""}, // '' → "mensch"
		{CardID: "", OpType: "card.upsert", ActorKind: "agent"},
	}
	if err := repo.RecordOps(ctx, "b1", "agent@example.com", entries); err != nil {
		t.Fatalf("RecordOps: %v", err)
	}

	got, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("erwartet 3 Einträge, erhalten %d", len(got))
	}
	byKey := map[string]ActivityEntry{}
	for _, e := range got {
		byKey[e.CardID+"|"+e.OpType] = e
	}
	if e, ok := byKey["k1|card.field"]; !ok || e.Action != "op" || e.ActorKind != "mensch" || e.Actor != "agent@example.com" {
		t.Errorf("k1-Eintrag falsch: %+v (ok=%v)", e, ok)
	}
	if e, ok := byKey["k2|comment.add"]; !ok || e.ActorKind != "mensch" {
		t.Errorf("k2-Eintrag: ActorKind sollte auf 'mensch' normalisiert sein: %+v (ok=%v)", e, ok)
	}
	if e, ok := byKey["|card.upsert"]; !ok || e.ActorKind != "agent" {
		t.Errorf("Eintrag ohne Karte: ActorKind %+v, erwartet agent (ok=%v)", e, ok)
	}
}

// TestActivityRepo_RecordOps_AtomarBeiFehler: Ein ungültiger actor_kind
// (verletzt den CHECK-Constraint aus Migration 0016) muss den GESAMTEN Batch
// scheitern lassen — kein Teil-Schreiben (EINE Transaktion).
//
// GEGENPROBE: Läuft RecordOps die Inserts einzeln (ohne gemeinsame
// Transaktion / Rollback), bleibt der k1-Eintrag stehen und der Test wird rot.
func TestActivityRepo_RecordOps_AtomarBeiFehler(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	entries := []OpActivityEntry{
		{CardID: "k1", OpType: "card.field", ActorKind: "mensch"},
		{CardID: "k2", OpType: "card.field", ActorKind: "roboter"}, // verletzt CHECK
	}
	if err := repo.RecordOps(ctx, "b1", "u@example.com", entries); err == nil {
		t.Fatal("RecordOps mit ungültigem actor_kind hätte scheitern müssen")
	}

	got, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("erwartet 0 Einträge nach gescheitertem Batch (Atomarität), erhalten %d", len(got))
	}
}

// TestActivityRepo_ListFiltered_KartenFilter prüft den Karten-Filter, den
// GET /boards/{id}/activities?card=... nutzt (Karte #554).
func TestActivityRepo_ListFiltered_KartenFilter(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	entries := []OpActivityEntry{
		{CardID: "k1", OpType: "card.field", ActorKind: "mensch"},
		{CardID: "k2", OpType: "card.field", ActorKind: "mensch"},
		{CardID: "k1", OpType: "comment.add", ActorKind: "mensch"},
	}
	if err := repo.RecordOps(ctx, "b1", "u@example.com", entries); err != nil {
		t.Fatalf("RecordOps: %v", err)
	}

	got, err := repo.ListFiltered(ctx, "b1", "k1", 0)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("erwartet 2 Einträge für k1, erhalten %d", len(got))
	}
	for _, e := range got {
		if e.CardID != "k1" {
			t.Errorf("Eintrag mit CardID %q im k1-Filter", e.CardID)
		}
	}
}

func TestActivityRepo_ListWithLimit(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	for i := 0; i < 10; i++ {
		if err := repo.Record(ctx, "b1", "", "board-updated", "", "", "", ""); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	entries, err := repo.List(ctx, "b1", 3)
	if err != nil {
		t.Fatalf("List mit Limit: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("erwartet 3 Einträge, erhalten %d", len(entries))
	}
}

func TestActivityRepo_RecordValidation(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	// Leere board_id → Fehler.
	if err := repo.Record(ctx, "", "u@example.com", "board-created", "", "", "", ""); err == nil {
		t.Error("leere board_id muss Fehler liefern")
	}
	// Leere action → Fehler.
	if err := repo.Record(ctx, "b1", "u@example.com", "", "", "", "", ""); err == nil {
		t.Error("leere action muss Fehler liefern")
	}
}

func TestActivityRepo_CascadeDeleteWithBoard(t *testing.T) {
	// ON DELETE CASCADE: Wenn das Board gelöscht wird, verschwinden
	// auch alle zugehörigen Activity-Einträge.
	ctx := context.Background()
	repo := newActivityTestDB(t)

	if err := repo.Record(ctx, "b1", "", "board-created", "", "", "", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Board löschen — FK CASCADE greift.
	db := repo.db
	if _, err := db.ExecContext(ctx, `DELETE FROM boards WHERE id = 'b1'`); err != nil {
		t.Fatalf("DELETE board: %v", err)
	}

	// activities müssen leer sein (Cascade).
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activities WHERE board_id = 'b1'`).Scan(&count); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if count != 0 {
		t.Errorf("erwartet 0 Activity-Einträge nach Board-Delete, erhalten %d", count)
	}
}

// TestActivityRepo_ListFiltered_ReihenfolgeImBatch (F-1147, Kritiker Sprint
// 71 K-B1): Alle Ops eines Batches tragen denselben ts. Neueste zuerst heisst
// dann: die zuletzt eingefügte Op zuerst. Dazu ein Eintrag mit ts ohne
// Nachkommastellen („…00Z“) vor einem mit „…00.5Z“ derselben Sekunde —
// als Zeichenkette sortierte der spätere vor den früheren.
func TestActivityRepo_ListFiltered_ReihenfolgeImBatch(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)
	orig := timeNow
	t.Cleanup(func() { timeNow = orig })

	sekunde := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return sekunde }
	if err := repo.Record(ctx, "b1", "a", "board-updated", "", "", "", "erst"); err != nil {
		t.Fatal(err)
	}
	timeNow = func() time.Time { return sekunde.Add(500 * time.Millisecond) }
	if err := repo.RecordOps(ctx, "b1", "a", []OpActivityEntry{
		{CardID: "k1", OpType: "card.field"}, {CardID: "k2", OpType: "card.move"}, {CardID: "k3", OpType: "comment.add"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var folge []string
	for _, e := range got {
		folge = append(folge, e.CardID+e.Note)
	}
	if strings.Join(folge, ",") != "k3,k2,k1,erst" {
		t.Fatalf("Reihenfolge %v, erwartet [k3 k2 k1 erst]", folge)
	}
}

// TestActivityRetentionDays_WarntNurEinmal (F-1148).
func TestActivityRetentionDays_WarntNurEinmal(t *testing.T) {
	retentionWarnungEinmal = sync.Once{}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	t.Setenv("ABOARD_ACTIVITY_RETENTION_DAYS", "abc")
	for i := 0; i < 5; i++ {
		if activityRetentionDays() != ActivityRetentionDaysDefault {
			t.Fatal("Fehlwert nicht auf Voreinstellung zurückgefallen")
		}
	}
	if n := strings.Count(buf.String(), "ungültig"); n != 1 {
		t.Fatalf("%d Warnungen, erwartet 1", n)
	}
}

// TestActivityTS_FesteBreite (F-1157): Jeder Zeitstempel hat 24 Zeichen, auch
// der auf der vollen Sekunde — und Zeichenkettenordnung ist Zeitordnung. Mit
// RFC3339Nano (bis Migration 0019) war "…00Z" 20 Zeichen lang und sortierte
// hinter "…00.5Z".
func TestActivityTS_FesteBreite(t *testing.T) {
	orig := timeNow
	t.Cleanup(func() { timeNow = orig })
	sekunde := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	var vorher string
	for i, d := range []time.Duration{0, time.Nanosecond, 999 * time.Microsecond, time.Millisecond, 500 * time.Millisecond, 999999999, time.Second} {
		timeNow = func() time.Time { return sekunde.Add(d) }
		ts := activityTS()
		if len(ts) != len(activityTSFormat) {
			t.Fatalf("%v: %q hat %d Zeichen, erwartet %d", d, ts, len(ts), len(activityTSFormat))
		}
		if i > 0 && ts < vorher {
			t.Fatalf("%q sortiert vor %q, ist aber später", ts, vorher)
		}
		vorher = ts
	}
	// Abschneiden, nicht runden: 999999999 ns bleibt in derselben Sekunde.
	timeNow = func() time.Time { return sekunde.Add(999999999) }
	if got := activityTS(); got != "2026-09-28T12:00:00.999Z" {
		t.Fatalf("activityTS = %q, erwartet 2026-09-28T12:00:00.999Z", got)
	}
}

// TestActivityRepo_AbfragenNutzenIndex (F-1157): Frist-Löschung, Kappung und
// Lesen laufen über den Index (board_id, ts) — ohne Sortierschritt und ohne
// Suche nur über board_id. Geprüft werden die Abfragetexte, die der Code
// ausführt (Konstanten bzw. activityListeQuery), nicht Kopien davon.
//
// GEGENPROBE (Karte #582): Mit dem Stand vor F-1157 — `substr(ts, 1, 19) < ?`
// in activityFristQ bzw. `ORDER BY substr(ts, 1, 19) DESC` in
// activityListeQuery — oder mit Index (board_id, ts DESC) aus Migration 0016
// wird der Test rot („TEMP B-TREE“ bzw. fehlendes „ts<?“).
func TestActivityRepo_AbfragenNutzenIndex(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)

	plan := func(q string, args ...any) string {
		t.Helper()
		rows, err := repo.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatalf("EXPLAIN %q: %v", q, err)
		}
		defer rows.Close()
		var teile []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			teile = append(teile, detail)
		}
		return strings.Join(teile, " | ")
	}

	if p := plan(activityFristQ, "b1", "2026-01-01T00:00:00"); !strings.Contains(p, "idx_activities_board (board_id=? AND ts<?)") {
		t.Errorf("Frist-Löschung ohne Index-Bereich auf ts: %s", p)
	}
	// F-1212: Kappung und Grenzsuche nach rowid über idx_activities_op_rowid
	// (Migration 0021) — ohne Sortierschritt und nicht als Tabellen-Scan in
	// rowid-Ordnung über alle Boards.
	if p := plan(activityKappenQ, "b1", 1000, 5000); !strings.Contains(p, "idx_activities_op_rowid (board_id=? AND rowid<?)") {
		t.Errorf("Kappung nicht über den Teilindex der Op-Einträge: %s", p)
	}
	if p := plan(activityGrenzeQ, "b1", 10); strings.Contains(p, "TEMP B-TREE") || !strings.Contains(p, "COVERING INDEX idx_activities_op_rowid") {
		t.Errorf("Grenzsuche nicht als Covering-Scan über idx_activities_op_rowid: %s", p)
	}
	for _, fall := range []struct {
		karte   string
		ohneACL bool
	}{{"", false}, {"", true}, {"k1", false}, {"k1", true}} {
		q, args := activityListeQuery("b1", fall.karte, 100, fall.ohneACL)
		p := plan(q, args...)
		if strings.Contains(p, "TEMP B-TREE") || !strings.Contains(p, "USING INDEX") {
			t.Errorf("Lesen (karte=%q, ohneACL=%v) nicht über den Index: %s", fall.karte, fall.ohneACL, p)
		}
	}
}

// TestActivityRepo_Obergrenze (F-1157): Über der Obergrenze bleiben genau die
// neuesten max Op-Einträge — auch wenn die Grenze mitten durch einen Batch mit
// gleichem ts läuft. Bei laufender Uhr ist das zugleich die Ordnung des
// Lese-Endpunkts (seit F-1212 ordnet die Kappung nach rowid).
// Der Nicht-Op-Eintrag r1 zählt nicht mit und bleibt (Kritiker Sprint 74).
//
// GEGENPROBE (Karte #582): Ohne den Aufruf von r.kappen in trim bleiben alle
// acht Einträge stehen, der Test wird rot.
func TestActivityRepo_Obergrenze(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)
	orig := timeNow
	t.Cleanup(func() { timeNow = orig })
	t.Setenv(activityMaxProBoardEnvVar, "5")

	sekunde := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return sekunde }
	if err := repo.Record(ctx, "b1", "a", "board-updated", "", "", "", "r1"); err != nil {
		t.Fatal(err)
	}
	// Zwei Batches in derselben Millisekunde: gleiche ts, Ordnung nur über rowid.
	timeNow = func() time.Time { return sekunde.Add(time.Second) }
	for _, batch := range [][]string{{"a1", "a2", "a3"}, {"b1", "b2", "b3"}} {
		var es []OpActivityEntry
		for _, k := range batch {
			es = append(es, OpActivityEntry{CardID: k, OpType: "card.field"})
		}
		if err := repo.RecordOps(ctx, "b1", "a", es); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var folge []string
	for _, e := range got {
		folge = append(folge, e.CardID+e.Note)
	}
	if strings.Join(folge, ",") != "b3,b2,b1,a3,a2,r1" {
		t.Fatalf("nach Kappung auf 5: %v, erwartet [b3 b2 b1 a3 a2 r1]", folge)
	}
}

// TestActivityMaxProBoard_Env: gültige Werte gelten, ungültige fallen auf die
// Voreinstellung zurück.
func TestActivityMaxProBoard_Env(t *testing.T) {
	maxProBoardWarnungEinmal = sync.Once{}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for roh, want := range map[string]int{
		"": ActivityMaxProBoardDefault, "250": 250, "0": ActivityMaxProBoardDefault,
		"-3": ActivityMaxProBoardDefault, "abc": ActivityMaxProBoardDefault,
		"10000001": ActivityMaxProBoardDefault, "10000000": ActivityMaxProBoardMax,
	} {
		t.Setenv(activityMaxProBoardEnvVar, roh)
		if got := activityMaxProBoard(); got != want {
			t.Errorf("%s=%q → %d, erwartet %d", activityMaxProBoardEnvVar, roh, got, want)
		}
	}
	if n := strings.Count(buf.String(), "ungültig"); n != 1 {
		t.Fatalf("%d Warnungen, erwartet 1", n)
	}
}

// TestActivityRepo_KappenFaellig_Takt (F-1157): Die Grenze wird beim ersten
// Schreibvorgang je Board geprüft, danach erst wieder nach max/100 neuen
// Einträgen — je Board getrennt gezählt, ein großer Batch löst sofort aus.
//
// GEGENPROBE (Karte #582): Liefert kappenFaellig immer true, wird der Test
// rot (die Grenzsuche liefe wieder bei jedem Schreibvorgang).
func TestActivityRepo_KappenFaellig_Takt(t *testing.T) {
	repo := NewActivityRepo(nil)
	const max = 1000 // Intervall 10
	var folge []bool
	for i := 0; i < 12; i++ {
		folge = append(folge, repo.kappenFaellig("b1", 1, max))
	}
	want := []bool{true, false, false, false, false, false, false, false, false, false, true, false}
	for i := range want {
		if folge[i] != want[i] {
			t.Fatalf("Folge %v, erwartet %v", folge, want)
		}
	}
	if !repo.kappenFaellig("b2", 1, max) {
		t.Error("erstes Schreiben auf ein anderes Board muss prüfen")
	}
	if !repo.kappenFaellig("b1", 200, max) {
		t.Error("Batch über dem Intervall muss sofort prüfen")
	}
	if !repo.kappenFaellig("b3", 1, 5) || !repo.kappenFaellig("b3", 1, 5) {
		t.Error("bei max < 100 muss jeder Schreibvorgang prüfen")
	}
}

// TestActivityRepo_ObergrenzeLaesstACLHistorieStehen (Kritiker Sprint 74,
// [S]): Wer auf einem Board schreiben darf, soll die ACL-Historie nicht per
// Op-Flut verdrängen können. Der Prüfbefehl des Befunds: Grenze 5, erst ein
// acl-set, dann sechs Op-Einträge → acl-set bleibt.
//
// Zwei ACL-Einträge in zwei Lagen (Verify Sprint 74, F-1200), weil jede der
// beiden SQL-Stellen nur von einer Lage gefangen wird: acl-set VOR allen Ops
// liegt unter der Grenze und fängt ein DELETE ohne Filter; acl-deleted
// ZWISCHEN den Ops liegt im OFFSET-Fenster und fängt eine Grenzsuche ohne
// Filter. Mit nur einer Lage blieb je eine Hälfte unbemerkt (gemessen).
//
// GEGENPROBE (Karte #582, Nacharbeit), jede Hälfte einzeln: ohne
// `action = 'op'` in activityKappenQ ist acl-set weg; ohne ihn in
// activityGrenzeQ zählt acl-deleted bei der Grenze mit, und es bleiben nur
// vier Ops — beides rot.
func TestActivityRepo_ObergrenzeLaesstACLHistorieStehen(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)
	t.Setenv(activityMaxProBoardEnvVar, "5")
	op := func(i int) {
		t.Helper()
		if err := repo.RecordOps(ctx, "b1", "bot@x", []OpActivityEntry{{CardID: fmt.Sprintf("k%d", i), OpType: "card.field"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Record(ctx, "b1", "admin@x", "acl-set", "role", "", "editor", "bot@x"); err != nil {
		t.Fatal(err)
	}
	op(0)
	op(1)
	if err := repo.Record(ctx, "b1", "admin@x", "acl-deleted", "", "alt@x", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < 6; i++ {
		op(i)
	}
	got, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	ops, acl := 0, 0
	for _, e := range got {
		switch e.Action {
		case "op":
			ops++
		case "acl-set", "acl-deleted":
			acl++
		}
	}
	if acl != 2 || ops != 5 {
		t.Fatalf("nach Kappung: %d ACL-Einträge, %d Ops — erwartet 2 und 5", acl, ops)
	}
}

// TestActivityRepo_FristWartetAufGestellteUhr (F-1192): Mit nicht gestellter
// Uhr löscht die Frist nichts; die Kappung (zählt Einträge) läuft weiter.
// GEGENPROBE: Ohne uhr.Erlaubt in trim ist „alt“ weg — rot.
func TestActivityRepo_FristWartetAufGestellteUhr(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)
	origTimeNow := timeNow
	origGestellt := uhr.Gestellt
	t.Cleanup(func() { timeNow = origTimeNow; uhr.Gestellt = origGestellt })
	t.Setenv(activityRetentionDaysEnvVar, "10")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	timeNow = func() time.Time { return base.AddDate(0, 0, -20) }
	if err := repo.Record(ctx, "b1", "u", "board-updated", "", "", "", "alt"); err != nil {
		t.Fatal(err)
	}
	uhr.Gestellt = func() (bool, error) { return false, nil }
	timeNow = func() time.Time { return base }
	if err := repo.Record(ctx, "b1", "u", "board-updated", "", "", "", "jetzt"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.List(ctx, "b1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d Einträge, erwartet 2 — mit nicht gestellter Uhr nach Frist gelöscht", len(got))
	}
	uhr.Gestellt = func() (bool, error) { return true, nil }
	if err := repo.Record(ctx, "b1", "u", "board-updated", "", "", "", "danach"); err != nil {
		t.Fatal(err)
	}
	if got, _ = repo.List(ctx, "b1", 0); len(got) != 2 {
		t.Fatalf("%d Einträge nach dem Stellen der Uhr, erwartet 2 (alt gelöscht)", len(got))
	}
}

// TestActivityRepo_KappenInStuecken (F-1195): Die Kappung löscht in Stücken
// von höchstens kappenStueck Zeilen, bis alles über der Grenze weg ist.
// GEGENPROBE: Ohne die Schleife (ein einziges Stück) bleiben 20 − 3 = 17
// statt 5 Op-Einträge — rot.
func TestActivityRepo_KappenInStuecken(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)
	alt := kappenStueck
	t.Cleanup(func() { kappenStueck = alt })
	kappenStueck = 3
	for i := 0; i < 20; i++ {
		if _, err := repo.db.ExecContext(ctx, `INSERT INTO activities (id, board_id, action, ts) VALUES (?, 'b1', 'op', ?)`,
			fmt.Sprintf("o%02d", i), fmt.Sprintf("2026-09-29T12:00:%02d.000Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.kappen(ctx, "b1", 5); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activities WHERE board_id = 'b1' AND action = 'op'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("%d Op-Einträge nach Kappung auf 5 in Stücken zu 3, erwartet 5", n)
	}
}

// TestActivityRepo_KappenLaesstNeueZeilenStehen (Session-Kritiker Sprint 74,
// F-1203): Eine Zeile, die zwischen zwei Stücken eingefügt wird und wegen
// einer rückwärts gestellten Uhr einen alten ts trägt, bleibt in DIESEM Lauf
// stehen — der Lauf trifft nur, was bei seinem Beginn schon da war. Seit
// F-1212 gilt das auch für spätere Läufe (TestActivityRepo_KappungFolgt-
// Einfuegereihenfolge).
// GEGENPROBE: Mit der Ordnung nach ts ohne rowid-Schranke (Stand vor F-1203)
// ist sie weg — rot.
func TestActivityRepo_KappenLaesstNeueZeilenStehen(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)
	altStueck, altNaht := kappenStueck, kappenNachStueck
	t.Cleanup(func() { kappenStueck, kappenNachStueck = altStueck, altNaht })
	kappenStueck = 3
	for i := 0; i < 20; i++ {
		if _, err := repo.db.ExecContext(ctx, `INSERT INTO activities (id, board_id, action, ts) VALUES (?, 'b1', 'op', ?)`,
			fmt.Sprintf("o%02d", i), fmt.Sprintf("2026-09-29T12:00:%02d.000Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	eingefuegt := false
	kappenNachStueck = func() {
		if eingefuegt {
			return
		}
		eingefuegt = true
		if _, err := repo.db.ExecContext(ctx, `INSERT INTO activities (id, board_id, action, ts) VALUES ('neu', 'b1', 'op', '2026-09-29T11:00:00.000Z')`); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.kappen(ctx, "b1", 5); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activities WHERE id = 'neu'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if !eingefuegt || n != 1 {
		t.Fatalf("eingefügt=%v, neue Zeile vorhanden=%d — sie gehört nicht zur Kappung", eingefuegt, n)
	}
}

// TestActivityRepo_KappungFolgtEinfuegereihenfolge (F-1212): Die Kappung
// behält die zuletzt EINGEFÜGTEN max Op-Einträge, nicht die mit dem jüngsten
// ts. Eine Zeile, die mit rückwärts gestellter Uhr geschrieben wurde (ts vor
// allen anderen), ist trotzdem die neueste und bleibt — auch über mehrere
// Läufe. Bis F-1212 ordnete die Kappung nach ts: Die Zeile sortierte als
// älteste und fiel beim ersten fälligen Lauf (Messung Verify 2: Lauf 2 neu=0).
// GEGENPROBE: Mit der Ordnung nach ts (Stand vor F-1212) ist „neu“ nach dem
// ersten Lauf weg — rot.
func TestActivityRepo_KappungFolgtEinfuegereihenfolge(t *testing.T) {
	ctx := context.Background()
	repo := newActivityTestDB(t)
	for i := 0; i < 20; i++ {
		if _, err := repo.db.ExecContext(ctx, `INSERT INTO activities (id, board_id, action, ts) VALUES (?, 'b1', 'op', ?)`,
			fmt.Sprintf("o%02d", i), fmt.Sprintf("2026-09-29T12:00:%02d.000Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO activities (id, board_id, action, ts) VALUES ('neu', 'b1', 'op', '1970-01-01T00:00:07.000Z')`); err != nil {
		t.Fatal(err)
	}
	for lauf := 1; lauf <= 2; lauf++ {
		if err := repo.kappen(ctx, "b1", 5); err != nil {
			t.Fatal(err)
		}
		rows, err := repo.db.QueryContext(ctx, `SELECT id FROM activities WHERE board_id = 'b1' AND action = 'op' ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		if got := strings.Join(ids, ","); got != "o16,o17,o18,o19,neu" {
			t.Fatalf("nach Lauf %d: %s, erwartet o16,o17,o18,o19,neu", lauf, got)
		}
	}
}
