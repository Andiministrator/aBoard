package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

// Das historische, byte-genaue Default-Seed (vor der Zentralisierung in
// CanonicalEmptyBoardState). Parität dagegen schützt die ADR-0044-Ausnahme
// + den Frontend-checkVersion-Vertrag (ADR-0052/0054).
const historicDefaultSeed = `{"version":"2","board":{"id":"default","title":"Board","nextDisplayId":1,"columnDefs":[{"id":"col-backlog","title":"Backlog","color":null},{"id":"col-ready","title":"Ready","color":"#E9C85D"},{"id":"col-in-arbeit","title":"In Arbeit","color":"#ea580c"},{"id":"col-erledigt","title":"Erledigt","color":"#63D2B0"}],"sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]}],"templates":[],"background":null,"attachments":[],"awaitingUser":null}}`

func TestCanonicalEmptyBoardState_ParityAndInjectionSafe(t *testing.T) {
	got := string(CanonicalEmptyBoardState("default", "Board"))
	if got != historicDefaultSeed {
		t.Fatalf("Parität verletzt — Seed weicht vom historischen Literal ab:\n got: %s\nwant: %s",
			got, historicDefaultSeed)
	}
	// Unterschiedliche id → unterschiedliche, valide JSON-Bytes.
	a := CanonicalEmptyBoardState("11111111-1111-1111-1111-111111111111", "Board")
	if string(a) == got {
		t.Fatalf("id wird nicht in den State eingesetzt")
	}
	var v map[string]any
	if err := json.Unmarshal(a, &v); err != nil {
		t.Fatalf("Personal-Seed kein valides JSON: %v", err)
	}
	// Injektionssicher: ein Titel mit Anführungszeichen darf das JSON nicht
	// zerbrechen (json.Marshal-Token statt naivem String-Concat).
	evil := CanonicalEmptyBoardState("id-x", `a"b\c`)
	if err := json.Unmarshal(evil, &v); err != nil {
		t.Fatalf("Titel mit Sonderzeichen zerbricht das JSON: %v", err)
	}
}

func openBootstrapTestDB(t *testing.T) (context.Context, *BootstrapRepo, *BoardRepo, *UserRepo, *AclRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ctx, NewBootstrapRepo(db), NewBoardRepo(db), NewUserRepo(db), NewAclRepo(db)
}

func TestBootstrapNewUser_FirstContact(t *testing.T) {
	ctx, bs, boards, users, acl := openBootstrapTestDB(t)

	created, err := bs.BootstrapNewUser(ctx, "uid-1", "bid-1", "Neu@Example.com ", "Neu")
	if err != nil {
		t.Fatalf("BootstrapNewUser: %v", err)
	}
	if !created {
		t.Fatalf("created=false bei Erstkontakt, erwartet true")
	}

	// users-Row in kanonischer (normalisierter) Email-Form.
	u, err := users.GetByEmail(ctx, "neu@example.com")
	if err != nil {
		t.Fatalf("users-Row fehlt: %v", err)
	}
	if u.ID != "uid-1" || u.Email != "neu@example.com" {
		t.Fatalf("users-Row unerwartet: %+v", u)
	}
	// Genau ein Board, kanonisch leer.
	list, err := boards.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("Board-Liste = %v (err=%v), erwartet genau 1", list, err)
	}
	b, err := boards.Get(ctx, "bid-1")
	if err != nil {
		t.Fatalf("Personal-Board nicht abrufbar: %v", err)
	}
	if string(b.StateJSON) != string(CanonicalEmptyBoardState("bid-1", "Board")) {
		t.Fatalf("Personal-Board nicht kanonisch leer:\n%s", b.StateJSON)
	}
	// owner-ACL (normalisierte Email).
	role, found, err := acl.GetRole(ctx, "bid-1", "neu@example.com")
	if err != nil || !found || role != RoleOwner {
		t.Fatalf("owner-ACL = (%q,%v,%v), erwartet owner", role, found, err)
	}

	// #306/ADR-0101: erster bootstrap'ter User → is_admin=TRUE.
	if !u.IsAdmin {
		t.Errorf("Bootstrap-Admin: erster User sollte is_admin=true sein, ist false")
	}
}

// #306/ADR-0101: nur der ERSTE neue User wird Admin. Spätere Bootstraps
// laufen mit is_admin=FALSE auf (sonst hätten alle Forward-Auth-Erstkontakte
// Admin-Rechte → Privilege-Eskalation).
func TestBootstrapNewUser_OnlyFirstIsAdmin(t *testing.T) {
	ctx, bs, _, users, _ := openBootstrapTestDB(t)

	if _, err := bs.BootstrapNewUser(ctx, "uid-a", "bid-a", "a@x.de", "A"); err != nil {
		t.Fatalf("Bootstrap A: %v", err)
	}
	if _, err := bs.BootstrapNewUser(ctx, "uid-b", "bid-b", "b@x.de", "B"); err != nil {
		t.Fatalf("Bootstrap B: %v", err)
	}

	a, _ := users.GetByEmail(ctx, "a@x.de")
	b, _ := users.GetByEmail(ctx, "b@x.de")
	if !a.IsAdmin {
		t.Error("A (erster Bootstrap) sollte Admin sein")
	}
	if b.IsAdmin {
		t.Error("B (zweiter Bootstrap) darf NICHT Admin sein (Privilege-Eskalation)")
	}

	count, err := users.CountAdmins(ctx)
	if err != nil {
		t.Fatalf("CountAdmins: %v", err)
	}
	if count != 1 {
		t.Errorf("CountAdmins = %d, erwartet 1", count)
	}
}

// Bestehende Bestands-DB ohne Admin (z. B. Pre-#306-Stand): der nächste
// Bootstrap macht den ersten Neuling zum Admin — bequemer Migrationspfad
// für Pre-existing Server. Wird via direkter Insert simuliert (am Bootstrap
// vorbei).
func TestBootstrapNewUser_AdminAdoptionFuerBestandsDB(t *testing.T) {
	ctx, bs, _, users, _ := openBootstrapTestDB(t)

	// Pre-existing User direkt einfügen (simuliert Bestands-Migration: User
	// existieren, aber niemand hat is_admin=1).
	if _, err := bs.db.ExecContext(ctx,
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('uid-old','old@x.de','Old','2026-01-01',0)`); err != nil {
		t.Fatalf("seed old user: %v", err)
	}

	// Neuer Bootstrap → bekommt is_admin=1 (Bestands-User ist nicht Admin).
	if _, err := bs.BootstrapNewUser(ctx, "uid-new", "bid-new", "new@x.de", "New"); err != nil {
		t.Fatalf("Bootstrap New: %v", err)
	}
	newU, _ := users.GetByEmail(ctx, "new@x.de")
	if !newU.IsAdmin {
		t.Error("New-User sollte Bestands-DB-Admin-Adoption bekommen")
	}
}

func TestBootstrapNewUser_Idempotent(t *testing.T) {
	ctx, bs, boards, _, _ := openBootstrapTestDB(t)

	for i := 0; i < 3; i++ {
		created, err := bs.BootstrapNewUser(ctx, "uid-x", "bid-x", "u@example.com", "U")
		if err != nil {
			t.Fatalf("Lauf %d: %v", i, err)
		}
		if i == 0 && !created {
			t.Fatalf("Lauf 0: created=false, erwartet true")
		}
		if i > 0 && created {
			t.Fatalf("Lauf %d: created=true — Doppel-Provisionierung", i)
		}
	}
	list, _ := boards.List(ctx)
	if len(list) != 1 {
		t.Fatalf("nach 3 Läufen %d Boards, erwartet genau 1 (idempotent)", len(list))
	}
}

func TestBootstrapNewUser_Concurrent_KeinDoppelBoard(t *testing.T) {
	ctx, bs, boards, _, _ := openBootstrapTestDB(t)

	const n = 6
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	errs := make([]error, 0)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Jede Goroutine erzeugt eigene „UUIDs" — nur der In-Tx-Check
			// darf entscheiden, wer real anlegt.
			created, err := bs.BootstrapNewUser(ctx,
				"uid-"+string(rune('a'+i)), "bid-"+string(rune('a'+i)),
				"race@example.com", "Race")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if created {
				createdCount++
			}
		}(i)
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("Fehler bei parallelem Bootstrap: %v", errs)
	}
	if createdCount != 1 {
		t.Fatalf("createdCount=%d, erwartet genau 1 (BEGIN IMMEDIATE serialisiert)", createdCount)
	}
	list, _ := boards.List(ctx)
	if len(list) != 1 {
		t.Fatalf("%d Boards nach %d parallelen Erst-Requests, erwartet genau 1", len(list), n)
	}
}

// ADR-0066 §1: Trigger ist „users-Row neu" — NICHT „0 Boards". Löscht der
// Nutzer sein Personal-Board, wird es NICHT resurrektiert.
func TestBootstrapNewUser_KeinResurrectNachLoeschung(t *testing.T) {
	ctx, bs, boards, users, _ := openBootstrapTestDB(t)

	if _, err := bs.BootstrapNewUser(ctx, "uid-1", "bid-1", "u@example.com", "U"); err != nil {
		t.Fatalf("Erst-Bootstrap: %v", err)
	}
	if err := boards.Delete(ctx, "bid-1"); err != nil {
		t.Fatalf("Board-Delete: %v", err)
	}

	created, err := bs.BootstrapNewUser(ctx, "uid-2", "bid-2", "u@example.com", "U")
	if err != nil {
		t.Fatalf("zweiter Bootstrap: %v", err)
	}
	if created {
		t.Fatalf("created=true nach Lösch — Board resurrektiert (Trigger falsch)")
	}
	list, _ := boards.List(ctx)
	if len(list) != 0 {
		t.Fatalf("%d Boards, erwartet 0 (kein Resurrect)", len(list))
	}
	if _, err := users.GetByEmail(ctx, "u@example.com"); err != nil {
		t.Fatalf("users-Row verschwand bei Board-Lösch: %v", err)
	}
}
