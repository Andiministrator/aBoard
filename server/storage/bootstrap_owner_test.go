package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestIstPersonenadresse(t *testing.T) {
	for e, soll := range map[string]bool{
		"andi@x.de":    true,
		"a.b@sub.x.de": true,
		"":             false,
		"andi":         false,
		"@x.de":        false, // Domain-Schlüssel, keine Person
		"andi@":        false,
		"a@b@x.de":     false,
		"an di@x.de":   false,
	} {
		if ist := IstPersonenadresse(e); ist != soll {
			t.Errorf("IstPersonenadresse(%q) = %v, erwartet %v", e, ist, soll)
		}
	}
}

func TestBootstrapOwnerAusEnv(t *testing.T) {
	t.Setenv(BootstrapOwnerEnv, "")
	if _, gesetzt, ok := BootstrapOwnerAusEnv(); gesetzt || ok {
		t.Fatalf("leer: gesetzt=%v ok=%v", gesetzt, ok)
	}
	t.Setenv(BootstrapOwnerEnv, "  Andi@X.de ")
	if o, gesetzt, ok := BootstrapOwnerAusEnv(); o != "andi@x.de" || !gesetzt || !ok {
		t.Fatalf("gültig: %q %v %v", o, gesetzt, ok)
	}
	t.Setenv(BootstrapOwnerEnv, "@x.de")
	if _, gesetzt, ok := BootstrapOwnerAusEnv(); !gesetzt || ok {
		t.Fatalf("Domain-Schlüssel: gesetzt=%v ok=%v", gesetzt, ok)
	}
}

// F-1855: Ist ABOARD_BOOTSTRAP_OWNER gesetzt, wird nicht der Erste Admin,
// sondern nur die gesetzte Adresse.
func TestBootstrapNewUser_NurGesetzteAdresseWirdAdmin(t *testing.T) {
	t.Setenv(BootstrapOwnerEnv, "chefin@x.de")
	ctx, bs, _, users, _ := openBootstrapTestDB(t)

	if _, err := bs.BootstrapNewUser(ctx, "uid-a", "bid-a", "fremd@x.de", "A"); err != nil {
		t.Fatal(err)
	}
	if a, _ := users.GetByEmail(ctx, "fremd@x.de"); a.IsAdmin {
		t.Fatal("die erste, aber nicht gesetzte Person darf nicht Admin werden")
	}
	if _, err := bs.BootstrapNewUser(ctx, "uid-b", "bid-b", "Chefin@X.de", "B"); err != nil {
		t.Fatal(err)
	}
	if b, _ := users.GetByEmail(ctx, "chefin@x.de"); !b.IsAdmin {
		t.Fatal("die gesetzte Adresse muss Bootstrap-Admin werden")
	}
}

// Kritiker S-2: gesetzte, aber ungültige Variable → fail closed, niemand wird
// automatisch Admin (nicht „wer zuerst kommt“). Ohne Variable: erste Person.
func TestBootstrapNewUser_UngueltigeVariableNiemandWirdAdmin(t *testing.T) {
	for _, wert := range []string{"kein-at", "@x.de", "andi@"} {
		t.Run(wert, func(t *testing.T) {
			t.Setenv(BootstrapOwnerEnv, wert)
			ctx, bs, _, users, _ := openBootstrapTestDB(t)
			if _, err := bs.BootstrapNewUser(ctx, "uid-a", "bid-a", "a@x.de", "A"); err != nil {
				t.Fatal(err)
			}
			if a, _ := users.GetByEmail(ctx, "a@x.de"); a.IsAdmin {
				t.Fatal("gesetzte, ungültige Variable darf niemanden automatisch zum Admin machen")
			}
		})
	}
	t.Run("ungesetzt", func(t *testing.T) {
		t.Setenv(BootstrapOwnerEnv, "")
		ctx, bs, _, users, _ := openBootstrapTestDB(t)
		if _, err := bs.BootstrapNewUser(ctx, "uid-a", "bid-a", "a@x.de", "A"); err != nil {
			t.Fatal(err)
		}
		if a, _ := users.GetByEmail(ctx, "a@x.de"); !a.IsAdmin {
			t.Fatal("ohne Variable bleibt es bei „erste Person wird Admin“ (ADR-0101 §2)")
		}
	})
}

// F-1855: Variable nachträglich auf eine Person mit Konto gesetzt, noch kein
// Admin → Start holt es nach. Gibt es schon einen Admin, passiert nichts.
func TestBootstrapAdminNachholen(t *testing.T) {
	t.Setenv(BootstrapOwnerEnv, "postfach@x.de") // meldet sich nie an
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs, users := NewBootstrapRepo(db), NewUserRepo(db)
	if _, err := bs.BootstrapNewUser(ctx, "uid-a", "bid-a", "andi@x.de", "A"); err != nil {
		t.Fatal(err)
	}
	if neu, err := BootstrapAdminNachholen(ctx, db, "postfach@x.de"); err != nil || neu {
		t.Fatalf("Person ohne Konto: neu=%v err=%v", neu, err)
	}
	if neu, err := BootstrapAdminNachholen(ctx, db, "andi@x.de"); err != nil || !neu {
		t.Fatalf("Person mit Konto, kein Admin: neu=%v err=%v", neu, err)
	}
	if a, _ := users.GetByEmail(ctx, "andi@x.de"); !a.IsAdmin {
		t.Fatal("andi@x.de muss jetzt Admin sein")
	}
	if _, err := bs.BootstrapNewUser(ctx, "uid-b", "bid-b", "b@x.de", "B"); err != nil {
		t.Fatal(err)
	}
	if neu, _ := BootstrapAdminNachholen(ctx, db, "b@x.de"); neu {
		t.Fatal("bei vorhandenem Admin darf niemand nachrücken")
	}
}

func TestBoardsOhneOwner(t *testing.T) {
	ctx, _, boards, _, acl := openBootstrapTestDB(t)
	for _, id := range []string{"b1", "b2", "b3", ServiceBoardID} {
		if _, err := boards.Insert(ctx, id, id, "2", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := acl.Set(ctx, "b1", "a@x.de", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := acl.Set(ctx, "b2", "a@x.de", RoleEditor); err != nil {
		t.Fatal(err)
	}
	ohne, err := BoardsOhneOwner(ctx, acl.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(ohne) != 2 || ohne[0].ID != "b2" || ohne[1].ID != "b3" {
		t.Fatalf("ohne Owner = %v, erwartet b2, b3", ohne)
	}
}
