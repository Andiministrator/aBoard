package storage

// schleuse_test.go — Speicher der Ausgangsschleuse (Karte #684, ADR-0134 §2).
//
// Kern ist die Trennung vom Board-Chat (ADR-0133 §2, Firmenmodus): Die
// Schleuse liest llm_api_keys nie, llm_keys.go liest schleuse_schluessel nie.
// Geprüft am Verhalten (Schlüssel der einen Tabelle tauchen in der anderen
// Liste nicht auf) und am Quelltext (keine der beiden Dateien nennt die
// fremde Tabelle im Code).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

func schleuseBox(t *testing.T) *secretcrypto.Box {
	t.Helper()
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	b, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func standVon(f SchleuseFreigabe) SchleuseStand {
	return SchleuseStand{Anbieter: f.Anbieter, BasisURL: f.BasisURL}
}

func TestSchleuseFreigabe_VertragsformBestaetigung(t *testing.T) {
	db := loeschDB(t)
	r := NewSchleuseFreigabeRepo(db)
	ctx := context.Background()

	f, err := r.Anlegen(ctx, SchleuseFreigabe{Anbieter: "anthropic", BasisURL: "https://api.anthropic.com",
		Modelle: []string{"m"}, Aktiv: true}, "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.VertragsformBestaetigtAm != "" || f.VertragsformBestaetigtVon != "" {
		t.Fatalf("unbestätigt, aber mit Spuren: %+v", f)
	}
	// Bestätigen: trägt den ändernden Admin.
	f.VertragsformBestaetigt = true
	f, err = r.Aendern(ctx, f, "admin-2")
	if err != nil {
		t.Fatal(err)
	}
	if f.VertragsformBestaetigtVon != "admin-2" || f.VertragsformBestaetigtAm == "" {
		t.Fatalf("Bestätigung ohne Admin: %+v", f)
	}
	am := f.VertragsformBestaetigtAm
	// Weitere Änderung bei bestehender Bestätigung: bleibt bei admin-2.
	f.VierAugen = true
	f, err = r.Aendern(ctx, f, "admin-3")
	if err != nil {
		t.Fatal(err)
	}
	if f.VertragsformBestaetigtVon != "admin-2" || f.VertragsformBestaetigtAm != am || f.GeaendertVon != "admin-3" {
		t.Fatalf("Bestätigung überschrieben: %+v", f)
	}
	// Zurücknehmen: beide Felder leer.
	f.VertragsformBestaetigt = false
	if f, err = r.Aendern(ctx, f, "admin-3"); err != nil || f.VertragsformBestaetigtVon != "" {
		t.Fatalf("Rücknahme: %+v %v", f, err)
	}
	g, err := r.Get(ctx, f.ID)
	if err != nil || g.VertragsformBestaetigt || !g.VierAugen || len(g.Modelle) != 1 {
		t.Fatalf("Get: %+v %v", g, err)
	}
	// Die Tabelle selbst lässt „bestätigt ohne Admin“ nicht zu.
	if _, err := db.Exec(`UPDATE schleuse_freigaben SET vertragsform_bestaetigt = 1, vertragsform_bestaetigt_von = NULL WHERE id = ?`, f.ID); err == nil {
		t.Errorf("CHECK fehlt: bestätigt ohne Admin liess sich schreiben")
	}
	if _, err := r.Anlegen(ctx, SchleuseFreigabe{Anbieter: "anthropic", BasisURL: "https://api.anthropic.com"}, "a"); !errors.Is(err, ErrSchleuseFreigabeDoppelt) {
		t.Errorf("Doppelter Eintrag: %v", err)
	}
	if _, err := r.Aendern(ctx, SchleuseFreigabe{ID: "gibts-nicht", Anbieter: "anthropic", BasisURL: "x"}, "a"); !errors.Is(err, ErrSchleuseFreigabeNichtGefunden) {
		t.Errorf("Ändern unbekannt: %v", err)
	}
}

// Wechselt das Gegenüber (Anbieter oder Basis-URL), gehen die Schlüssel der
// Personen mit — sie wurden für ein anderes Ziel eingetragen. Löschen der
// Freigabe räumt sie ebenfalls ab.
func TestSchleuseFreigabe_GegenueberWechselLoeschtSchluessel(t *testing.T) {
	db := loeschDB(t)
	ctx := context.Background()
	r := NewSchleuseFreigabeRepo(db)
	k := NewSchleuseSchluesselRepo(db, schleuseBox(t))
	f, err := r.Anlegen(ctx, SchleuseFreigabe{Anbieter: "openai-kompatibel", BasisURL: "https://a.example/v1", Aktiv: true, VertragsformBestaetigt: true}, "adm")
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Set(ctx, "u1", f.ID, "k1", standVon(f)); err != nil {
		t.Fatal(err)
	}
	f.Modelle = []string{"neu"} // gleiches Gegenüber → Schlüssel bleibt
	if f, err = r.Aendern(ctx, f, "adm"); err != nil {
		t.Fatal(err)
	}
	if l, _ := k.List(ctx, "u1"); len(l) != 1 {
		t.Fatalf("Schlüssel nach Modelländerung weg: %v", l)
	}
	f.BasisURL = "https://b.example/v1"
	if f, err = r.Aendern(ctx, f, "adm"); err != nil {
		t.Fatal(err)
	}
	if l, _ := k.List(ctx, "u1"); len(l) != 0 {
		t.Fatalf("Schlüssel überlebt den Wechsel des Gegenübers: %v", l)
	}
	if err := k.Set(ctx, "u1", f.ID, "k2", standVon(f)); err != nil {
		t.Fatal(err)
	}
	if err := r.Loeschen(ctx, f.ID, "adm"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM schleuse_schluessel`).Scan(&n)
	if n != 0 {
		t.Errorf("Schlüssel nach Löschen der Freigabe: %d", n)
	}
	if err := k.Set(ctx, "u1", f.ID, "k3", standVon(f)); !errors.Is(err, ErrSchleuseFreigabeNichtGefunden) {
		t.Errorf("Set auf gelöschte Freigabe: %v", err)
	}
}

// KC-4: Ein Altwert in anderer Schreibweise ist dasselbe Gegenüber — Aendern
// vergleicht die Normalform und lässt die Schlüssel stehen.
func TestSchleuseFreigabe_AendernVergleichtNormalform(t *testing.T) {
	db := loeschDB(t)
	ctx := context.Background()
	r := NewSchleuseFreigabeRepo(db)
	k := NewSchleuseSchluesselRepo(db, schleuseBox(t))
	// Altbestand vor der Normalisierung: Grossbuchstaben, Port 443, Schrägstrich.
	f, err := r.Anlegen(ctx, SchleuseFreigabe{Anbieter: "openai-kompatibel", BasisURL: "https://LLM.example.net:443/v1/",
		Aktiv: true, VertragsformBestaetigt: true}, "adm")
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Set(ctx, "u1", f.ID, "k1", standVon(f)); err != nil {
		t.Fatal(err)
	}
	f.BasisURL = "https://llm.example.net/v1"
	if _, err := r.Aendern(ctx, f, "adm"); err != nil {
		t.Fatal(err)
	}
	if l, _ := k.List(ctx, "u1"); len(l) != 1 {
		t.Fatalf("Schlüssel wegen reiner Schreibweise gelöscht: %v", l)
	}
}

// KC-2: Set prüft in seiner Transaktion den Stand, den die Person gesehen hat.
// Hat der Admin dazwischen die Basis-URL umgestellt, deaktiviert oder die
// Vertragsform zurückgenommen, wird nichts geschrieben.
func TestSchleuseSchluessel_SetPrueftStand(t *testing.T) {
	db := loeschDB(t)
	ctx := context.Background()
	r := NewSchleuseFreigabeRepo(db)
	k := NewSchleuseSchluesselRepo(db, schleuseBox(t))
	f, err := r.Anlegen(ctx, SchleuseFreigabe{Anbieter: "openai-kompatibel", BasisURL: "https://a.example/v1",
		Aktiv: true, VertragsformBestaetigt: true}, "adm")
	if err != nil {
		t.Fatal(err)
	}
	geladen, err := r.Get(ctx, f.ID) // so hat ihn der Dialog angezeigt
	if err != nil {
		t.Fatal(err)
	}
	f.BasisURL = "https://b.example/v1"
	if f, err = r.Aendern(ctx, f, "adm"); err != nil {
		t.Fatal(err)
	}
	zeilen := func() int {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM schleuse_schluessel`).Scan(&n)
		return n
	}
	if err := k.Set(ctx, "u1", f.ID, "k-alt", standVon(geladen)); !errors.Is(err, ErrSchleuseStandVeraltet) {
		t.Fatalf("Set mit altem Stand: %v, erwartet ErrSchleuseStandVeraltet", err)
	}
	if n := zeilen(); n != 0 {
		t.Fatalf("Set mit altem Stand hat %d Zeile(n) geschrieben", n)
	}
	if err := k.Set(ctx, "u1", f.ID, "k", SchleuseStand{Anbieter: "anthropic", BasisURL: f.BasisURL}); !errors.Is(err, ErrSchleuseStandVeraltet) {
		t.Errorf("anderer Anbieter: %v", err)
	}
	// Normalform: dieselbe Basis in anderer Schreibweise ist der richtige Stand.
	if err := k.Set(ctx, "u1", f.ID, "k", SchleuseStand{Anbieter: f.Anbieter, BasisURL: "https://B.example/v1/"}); err != nil {
		t.Fatalf("Set mit aktuellem Stand: %v", err)
	}
	for _, aenderung := range []func(*SchleuseFreigabe){
		func(g *SchleuseFreigabe) { g.Aktiv = false },
		func(g *SchleuseFreigabe) { g.VertragsformBestaetigt = false },
	} {
		g := f
		aenderung(&g)
		if _, err := r.Aendern(ctx, g, "adm"); err != nil {
			t.Fatal(err)
		}
		if err := k.Set(ctx, "u2", f.ID, "k", standVon(f)); !errors.Is(err, ErrSchleuseNichtFreigegeben) {
			t.Errorf("nicht freigegeben: %v, erwartet ErrSchleuseNichtFreigegeben", err)
		}
		if _, err := r.Aendern(ctx, f, "adm"); err != nil { // zurück
			t.Fatal(err)
		}
	}
	if n := zeilen(); n != 1 {
		t.Errorf("Zeilen: %d, erwartet 1 (nur der Set mit aktuellem Stand)", n)
	}
}

// ADR-0133 §2 / ADR-0134 §1: Schleuse und Board-Chat teilen keinen
// Schlüsselspeicher — in keiner Richtung.
func TestSchleuseSchluessel_GetrenntVonLLMKeys(t *testing.T) {
	db := loeschDB(t)
	ctx := context.Background()
	box := schleuseBox(t)
	f, err := NewSchleuseFreigabeRepo(db).Anlegen(ctx, SchleuseFreigabe{Anbieter: "anthropic", BasisURL: "https://api.anthropic.com", Aktiv: true, VertragsformBestaetigt: true}, "adm")
	if err != nil {
		t.Fatal(err)
	}
	sk := NewSchleuseSchluesselRepo(db, box)
	lk := NewLLMKeyRepo(db, box)

	// Chat-Schlüssel vorhanden → Schleuse sieht keinen.
	if err := lk.Set(ctx, "u1", "anthropic", "chat-key"); err != nil {
		t.Fatal(err)
	}
	if l, _ := sk.List(ctx, "u1"); len(l) != 0 {
		t.Fatalf("Schleuse listet Chat-Schlüssel: %v", l)
	}
	if _, err := sk.Get(ctx, "u1", f.ID); !errors.Is(err, ErrSchleuseSchluesselNichtGefunden) {
		t.Fatalf("Schleuse liefert ohne eigenen Schlüssel: %v", err)
	}
	if _, err := sk.Get(ctx, "u1", "anthropic"); !errors.Is(err, ErrSchleuseSchluesselNichtGefunden) {
		t.Fatalf("Schleuse liefert über den Anbieternamen: %v", err)
	}
	// Schleusen-Schlüssel vorhanden, Chat-Schlüssel weg → Chat sieht keinen.
	if err := lk.Delete(ctx, "u1", "anthropic"); err != nil {
		t.Fatal(err)
	}
	if err := sk.Set(ctx, "u1", f.ID, "schleuse-key", standVon(f)); err != nil {
		t.Fatal(err)
	}
	if l, _ := lk.List(ctx, "u1"); len(l) != 0 {
		t.Fatalf("Chat listet Schleusen-Schlüssel: %v", l)
	}
	if _, err := lk.Get(ctx, "u1", "anthropic"); !errors.Is(err, ErrLLMKeyNotFound) {
		t.Fatalf("Chat liefert Schleusen-Schlüssel: %v", err)
	}
	if got, err := sk.Get(ctx, "u1", f.ID); err != nil || got != "schleuse-key" {
		t.Fatalf("Schleuse Get: %q %v", got, err)
	}

	// Quelltext: keine Datei nennt die Tabelle der anderen.
	pruefe := func(datei string, verboten ...string) {
		roh, err := os.ReadFile(datei)
		if err != nil {
			t.Fatal(err)
		}
		code := string(roh)
		if i := strings.Index(code, "\nimport ("); i > 0 {
			code = code[i:] // Kopfkommentar nennt die Abgrenzung
		}
		for _, v := range verboten {
			if strings.Contains(code, v) {
				t.Errorf("%s nennt %s", datei, v)
			}
		}
	}
	pruefe("schleuse.go", "llm_api_keys", "LLMKeyRepo")
	pruefe("llm_keys.go", "schleuse_schluessel", "SchleuseSchluessel")
}

func TestSchleuseBoardSperre(t *testing.T) {
	db := loeschDB(t)
	ctx := context.Background()
	mustExec(t, db, `INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b1', 'B', '1', '{}', 1, 1)`)
	if an, err := SchleuseBoardSperre(ctx, db, "b1"); err != nil || an {
		t.Fatalf("Vorgabe muss offen sein: %v %v", an, err)
	}
	if err := SetzeSchleuseBoardSperre(ctx, db, "b1", true, "u1"); err != nil {
		t.Fatal(err)
	}
	if an, _ := SchleuseBoardSperre(ctx, db, "b1"); !an {
		t.Fatalf("nicht gesperrt")
	}
	if err := SetzeSchleuseBoardSperre(ctx, db, "fehlt", true, "u1"); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("unbekanntes Board: %v", err)
	}
	if _, err := SchleuseBoardSperre(ctx, db, "fehlt"); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("unbekanntes Board lesen: %v", err)
	}
}
