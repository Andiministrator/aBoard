package storage

import (
	"context"
	"testing"
)

func openSpamTestDB(t *testing.T) (context.Context, *SpamRepo) {
	t.Helper()
	ctx := context.Background()
	dbPath := t.TempDir() + "/spam.db"
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, NewSpamRepo(db)
}

func TestSpamRepo_AddListRemove_RoundTrip(t *testing.T) {
	ctx, r := openSpamTestDB(t)

	// Initially leer
	listed, err := r.SpamSenderListed(ctx, "foo@example.org")
	if err != nil || listed {
		t.Fatalf("Initial Listed=true/err: listed=%v err=%v", listed, err)
	}

	// Add
	if err := r.AddSpamSender(ctx, "Foo@Example.ORG", "user-1"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Listed — normalisiert (Casing-Toleranz, F-260-Pattern)
	listed, err = r.SpamSenderListed(ctx, "foo@example.org")
	if err != nil {
		t.Fatalf("Listed: %v", err)
	}
	if !listed {
		t.Errorf("Listed=false nach Add (normalisierter Lookup)")
	}

	// List
	all, err := r.ListSpamSenders(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0] != "foo@example.org" {
		t.Errorf("ListSpamSenders = %v, erwartet [foo@example.org]", all)
	}

	// Add nochmal → idempotent (kein Fehler, count bleibt 1)
	if err := r.AddSpamSender(ctx, "foo@example.org", "user-2"); err != nil {
		t.Fatalf("Re-Add: %v", err)
	}
	all, _ = r.ListSpamSenders(ctx)
	if len(all) != 1 {
		t.Errorf("Re-Add nicht idempotent: %v", all)
	}

	// Remove
	if err := r.RemoveSpamSender(ctx, "FOO@example.org"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	listed, _ = r.SpamSenderListed(ctx, "foo@example.org")
	if listed {
		t.Errorf("Listed=true nach Remove")
	}

	// Remove nochmal → idempotent
	if err := r.RemoveSpamSender(ctx, "foo@example.org"); err != nil {
		t.Fatalf("Re-Remove: %v", err)
	}
}

func TestSpamRepo_EmptyEmail_Rejected(t *testing.T) {
	ctx, r := openSpamTestDB(t)
	if err := r.AddSpamSender(ctx, "", "u"); err == nil {
		t.Errorf("Add(\"\") sollte fehlschlagen")
	}
	if err := r.RemoveSpamSender(ctx, "  "); err == nil {
		t.Errorf("Remove(Whitespace) sollte fehlschlagen")
	}
	listed, err := r.SpamSenderListed(ctx, "")
	if err != nil || listed {
		t.Errorf("Listed(\"\") = (%v, %v), erwartet (false, nil)", listed, err)
	}
}

func TestSpamRepo_Counter_StartsAtZeroAndIncrements(t *testing.T) {
	ctx, r := openSpamTestDB(t)
	n, err := r.SpamCounter(ctx)
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if n != 0 {
		t.Errorf("Counter initial = %d, erwartet 0", n)
	}
	for i := 0; i < 3; i++ {
		if err := r.IncSpamCounter(ctx); err != nil {
			t.Fatalf("Inc: %v", err)
		}
	}
	n, _ = r.SpamCounter(ctx)
	if n != 3 {
		t.Errorf("Counter nach 3x Inc = %d, erwartet 3", n)
	}
}
