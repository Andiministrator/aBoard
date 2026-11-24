package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

func newICalTestRepo(t *testing.T) (*ICalTokenRepo, func()) {
	t.Helper()
	db, err := Open(context.Background(), Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	// Seit Migration 0014 (F-425) verweist ical_subscriptions per Fremdschlüssel
	// auf users und boards. Die in dieser Datei benutzten Kennungen müssen
	// deshalb wirklich existieren — vorher genügten frei erfundene Werte, ein
	// Zustand, den es im Betrieb nie gab.
	repo := NewICalTokenRepo(db, box)
	for _, u := range []string{"user-1", "user-2"} {
		anlegenUser(t, db, u)
	}
	for _, b := range []string{"board-42", "b1"} {
		anlegenBoard(t, db, b)
	}
	return repo, func() { db.Close() }
}

// newICalTestRepoMitDB wie newICalTestRepo, gibt aber die *sql.DB mit heraus —
// für Tests, die neben dem Repo auch direkt in users/boards schreiben oder
// dort löschen müssen (CASCADE-Prüfung).
func newICalTestRepoMitDB(t *testing.T) (*ICalTokenRepo, *sql.DB, func()) {
	t.Helper()
	db, err := Open(context.Background(), Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	return NewICalTokenRepo(db, box), db, func() { db.Close() }
}

// anlegenUser/anlegenBoard schaffen die Zeilen, auf die ical_subscriptions seit
// Migration 0014 per Fremdschlüssel verweist (F-425, Karte #355).
//
// Vor 0014 kamen diese Tests ohne aus: die Tabelle hatte keine Fremdschlüssel,
// also liess sich ein Abo für eine frei erfundene user_id anlegen. Das ist ein
// Zustand, den es im Betrieb nie gibt — `api/ical.go` reicht immer eine echte
// `users.id` durch. Die Tests bilden jetzt ab, was wirklich passiert.
func anlegenUser(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO users (id, email, name, created) VALUES (?, ?, '', '2026-08-18')`,
		id, id+"@example.test")
	if err != nil {
		t.Fatalf("User %s anlegen: %v", id, err)
	}
	return id
}

func anlegenBoard(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES (?, ?, '2', '{}', 0, 0)`, id, id)
	if err != nil {
		t.Fatalf("Board %s anlegen: %v", id, err)
	}
	return id
}

func TestICalToken_CreateResolveCrossBoard(t *testing.T) {
	repo, done := newICalTestRepo(t)
	defer done()
	ctx := context.Background()

	sub, err := repo.Create(ctx, "user-1", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sub.Token == "" {
		t.Fatal("Klartext-Token fehlt bei Create")
	}
	if sub.BoardID != nil {
		t.Errorf("Cross-Board sollte BoardID nil haben")
	}

	got, err := repo.Resolve(ctx, sub.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.UserID != "user-1" || got.BoardID != nil {
		t.Errorf("Resolve falsch: %+v", got)
	}
	// Resolve liefert KEINEN Klartext zurück (Auth, nicht Anzeige).
	if got.Token != "" {
		t.Errorf("Resolve sollte Token nicht zurückgeben")
	}
}

func TestICalToken_BoardScope(t *testing.T) {
	repo, done := newICalTestRepo(t)
	defer done()
	ctx := context.Background()
	bid := "board-42"
	sub, err := repo.Create(ctx, "user-1", &bid)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Resolve(ctx, sub.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.BoardID == nil || *got.BoardID != "board-42" {
		t.Errorf("Board-Scope verloren: %+v", got)
	}
}

func TestICalToken_UnknownToken(t *testing.T) {
	repo, done := newICalTestRepo(t)
	defer done()
	if _, err := repo.Resolve(context.Background(), "gibt-es-nicht"); !errors.Is(err, ErrICalTokenNotFound) {
		t.Errorf("erwartet ErrICalTokenNotFound, bekam %v", err)
	}
}

func TestICalToken_ListDecryptsForDisplay(t *testing.T) {
	repo, done := newICalTestRepo(t)
	defer done()
	ctx := context.Background()
	s1, _ := repo.Create(ctx, "user-1", nil)
	bid := "b1"
	_, _ = repo.Create(ctx, "user-1", &bid)
	_, _ = repo.Create(ctx, "user-2", nil) // fremder User

	list, err := repo.ListForUser(ctx, "user-1")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("erwartet 2 eigene Abos, bekam %d", len(list))
	}
	// Klartext-Token muss in der Liste stehen (für die Abo-URL).
	var found bool
	for _, s := range list {
		if s.Token == s1.Token {
			found = true
		}
		if s.Token == "" {
			t.Errorf("ListForUser-Eintrag ohne Klartext-Token")
		}
	}
	if !found {
		t.Errorf("erstes Token nicht in der Liste")
	}
}

func TestICalToken_DeleteOwnOnly(t *testing.T) {
	repo, done := newICalTestRepo(t)
	defer done()
	ctx := context.Background()
	sub, _ := repo.Create(ctx, "user-1", nil)

	// Fremder User darf nicht löschen (kein IDOR) — idempotent, kein Fehler,
	// aber Token bleibt gültig.
	if err := repo.Delete(ctx, "user-2", sub.ID); err != nil {
		t.Fatalf("Delete (fremd): %v", err)
	}
	if _, err := repo.Resolve(ctx, sub.Token); err != nil {
		t.Errorf("Token nach Fremd-Delete weg — IDOR! %v", err)
	}

	// Eigentümer löscht → Token sofort ungültig.
	if err := repo.Delete(ctx, "user-1", sub.ID); err != nil {
		t.Fatalf("Delete (eigen): %v", err)
	}
	if _, err := repo.Resolve(ctx, sub.Token); !errors.Is(err, ErrICalTokenNotFound) {
		t.Errorf("Token nach eigenem Delete noch gültig: %v", err)
	}
}

func TestICalToken_RotationInvalidatesOld(t *testing.T) {
	repo, done := newICalTestRepo(t)
	defer done()
	ctx := context.Background()
	old, _ := repo.Create(ctx, "user-1", nil)
	_ = repo.Delete(ctx, "user-1", old.ID)
	fresh, _ := repo.Create(ctx, "user-1", nil)

	if old.Token == fresh.Token {
		t.Fatal("Rotation erzeugte identischen Token")
	}
	if _, err := repo.Resolve(ctx, old.Token); !errors.Is(err, ErrICalTokenNotFound) {
		t.Errorf("alter Token nach Rotation noch gültig")
	}
	if _, err := repo.Resolve(ctx, fresh.Token); err != nil {
		t.Errorf("neuer Token ungültig: %v", err)
	}
}

// TestICalToken_CascadeBeimLoeschen sichert F-425 ab (Karte #355).
//
// Migration 0013 legte ical_subscriptions ohne Fremdschlüssel an — anders als
// acl und activities, die beide ON DELETE CASCADE führen. Wurde ein User oder
// ein Board gelöscht, blieben dessen Abo-Zeilen liegen. Jede davon trägt ein
// Bearer-Secret (token_enc); ein Geheimnis, das zu einem gelöschten Konto
// gehört und trotzdem in der Datenbank steht, kann später niemand mehr
// zuordnen.
//
// Migration 0014 baut die Tabelle mit beiden Fremdschlüsseln neu auf.
func TestICalToken_CascadeBeimLoeschen(t *testing.T) {
	repo, db, done := newICalTestRepoMitDB(t)
	defer done()
	ctx := context.Background()

	anlegenUser(t, db, "u-weg")
	anlegenUser(t, db, "u-bleibt")
	anlegenBoard(t, db, "b-weg")

	bWeg := "b-weg"
	if _, err := repo.Create(ctx, "u-weg", nil); err != nil {
		t.Fatalf("Cross-Board-Abo für u-weg: %v", err)
	}
	if _, err := repo.Create(ctx, "u-bleibt", &bWeg); err != nil {
		t.Fatalf("Board-Abo für u-bleibt: %v", err)
	}
	if _, err := repo.Create(ctx, "u-bleibt", nil); err != nil {
		t.Fatalf("Cross-Board-Abo für u-bleibt: %v", err)
	}

	zaehle := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM ical_subscriptions").Scan(&n); err != nil {
			t.Fatalf("zählen: %v", err)
		}
		return n
	}
	if n := zaehle(); n != 3 {
		t.Fatalf("Vorbedingung: %d Abos statt 3", n)
	}

	// User löschen → sein Abo verschwindet mit.
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = 'u-weg'`); err != nil {
		t.Fatalf("User löschen: %v", err)
	}
	if n := zaehle(); n != 2 {
		t.Errorf("nach User-Löschung %d Abos, erwartet 2 — verwaiste Zeile geblieben", n)
	}

	// Board löschen → das board-gebundene Abo verschwindet, das
	// Cross-Board-Abo desselben Users (board_id NULL) bleibt.
	if _, err := db.ExecContext(ctx, `DELETE FROM boards WHERE id = 'b-weg'`); err != nil {
		t.Fatalf("Board löschen: %v", err)
	}
	if n := zaehle(); n != 1 {
		t.Errorf("nach Board-Löschung %d Abos, erwartet 1", n)
	}

	// Und das übrig gebliebene ist das richtige.
	var uid string
	var bid *string
	if err := db.QueryRowContext(ctx,
		`SELECT user_id, board_id FROM ical_subscriptions`).Scan(&uid, &bid); err != nil {
		t.Fatalf("Restzeile lesen: %v", err)
	}
	if uid != "u-bleibt" || bid != nil {
		t.Errorf("falsche Zeile überlebt: user_id=%s board_id=%v", uid, bid)
	}
}

// TestICalToken_IndexNachRebuild: Migration 0014 legt die Tabelle neu an; der
// Index aus 0013 verschwindet dabei mit der alten Tabelle. Bleibt er weg,
// merkt das im Betrieb niemand — die Abfragen laufen weiter, nur langsamer.
// Genau deshalb ein eigener Test.
func TestICalToken_IndexNachRebuild(t *testing.T) {
	_, db, done := newICalTestRepoMitDB(t)
	defer done()

	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM sqlite_master
		  WHERE type='index' AND name='idx_ical_subs_user'`).Scan(&n); err != nil {
		t.Fatalf("sqlite_master lesen: %v", err)
	}
	if n != 1 {
		t.Errorf("idx_ical_subs_user %d-mal vorhanden, erwartet genau 1", n)
	}
}

// Karte #795 (ADR-0140 §11): Neue Abos „für außen“ gibt es nicht mehr. Ein
// Bestand-Abo aus der Zeit der Außenstelle bleibt gesperrt: Resolve meldet
// es als Aussen (der Feed antwortet dann 404), ListForUser zeigt es ohne
// Token und nicht als defekt. Fail-closed: schon EINE der beiden
// Außen-Spalten reicht (F-1971 — vorher entschieden drei Stellen an drei
// verschiedenen Bedingungen).
func TestICalToken795_BestandAussenBleibtGesperrt(t *testing.T) {
	repo, db, done := newICalTestRepoMitDB(t)
	defer done()
	ctx := context.Background()
	u := anlegenUser(t, db, "u1")
	normal, err := repo.Create(ctx, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	var aussenRows []*ICalSubscription
	for _, set := range []string{
		`aussen_hash = 'h1', aussen_enc = 'e1'`, // wie angelegt
		`aussen_hash = 'h2'`,                    // nur Hash (beschädigt)
		`aussen_enc = 'e3'`,                     // nur verschlüsselte Kennung (beschädigt)
		`aussen_enc = ''`,                       // leerer Text, aber gesetzt
	} {
		s, err := repo.Create(ctx, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE ical_subscriptions SET `+set+` WHERE id = ?`, s.ID); err != nil {
			t.Fatal(err)
		}
		aussenRows = append(aussenRows, s)
	}
	if r, err := repo.Resolve(ctx, normal.Token); err != nil || r.Aussen {
		t.Fatalf("normales Abo: %+v %v", r, err)
	}
	for i, s := range aussenRows {
		r, err := repo.Resolve(ctx, s.Token)
		if err != nil || !r.Aussen {
			t.Errorf("Bestand %d: Resolve %+v %v — erwartet Aussen", i, r, err)
		}
	}
	liste, err := repo.ListForUser(ctx, u)
	if err != nil || len(liste) != 5 {
		t.Fatalf("ListForUser: %d %v", len(liste), err)
	}
	for _, s := range liste {
		if s.ID == normal.ID {
			if s.Aussen || s.Token != normal.Token || s.Defekt {
				t.Errorf("normales Abo in der Liste: %+v", s)
			}
			continue
		}
		if !s.Aussen || s.Token != "" || s.Defekt {
			t.Errorf("Bestand „für außen“ in der Liste: %+v — erwartet Aussen, ohne Token, nicht defekt", s)
		}
	}
	// Löschen geht wie bei jedem Abo.
	if err := repo.Delete(ctx, u, aussenRows[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Resolve(ctx, aussenRows[0].Token); err != ErrICalTokenNotFound {
		t.Errorf("gelöschtes Abo: %v", err)
	}
}
