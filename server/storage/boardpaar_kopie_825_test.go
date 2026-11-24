package storage

// Karte #825 (ADR-0141 Schritt 2a): Migration 0040, Typ abr, Recht
// „darf Kopien anlegen“, Ziele, Aufträge, Kopie anlegen außen, ErsetzeKopie.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
)

func kopieTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Rundweg der Migration: Bestand aus Stufe 1 (Tokens samt Erneuerungs-Spalten,
// Kopplung außen, Schatten) übersteht 0040 unverändert; neue Spalten haben
// ihre Vorgaben; die CHECKs greifen.
func TestBoardpaar825_MigrationRundweg(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", dsnFuer(filepath.Join(t.TempDir(), "a.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	alle, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range alle {
		if m.version > 39 {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 39"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO boards (id,title,version,state_json,updated_at,created_at) VALUES ('b1','B','2','{}',1,1)`,
		`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at,created_by,last_used_at,gilt_bis,nachfolger_id,nachfolger_enc,erneuert_aus,erneuert_von)
		 VALUES ('t1','abe','eingang','h1','2026-01-01','andi','2026-01-02','2026-01-03','t2','enc','t0','192.0.2.1')`,
		`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t2','abs','push','h2','2026-01-01')`,
		`INSERT INTO boardpaar_aussen (aussen_board_id,innen_link_basis,seit,von) VALUES ('b1','https://i/#board=x','2026-01-01','a')`,
		`INSERT INTO schatten (board_id,schatten_id,art,vertraulich,bearbeiter,von_agent,geaendert_at) VALUES ('b1','s1','karte',1,'[]',0,'2026-01-01')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var typ, name, enc, aus, von string
	var darf int
	if err := db.QueryRow(`SELECT typ,name,nachfolger_enc,erneuert_aus,erneuert_von,darf_anlegen FROM boardpaar_tokens WHERE id='t1'`).
		Scan(&typ, &name, &enc, &aus, &von, &darf); err != nil {
		t.Fatal(err)
	}
	if typ != "abe" || name != "eingang" || enc != "enc" || aus != "t0" || von != "192.0.2.1" || darf != 0 {
		t.Errorf("Token nach 0040: %s %s %s %s %s darf=%d", typ, name, enc, aus, von, darf)
	}
	var kopie, fassung int
	if err := db.QueryRow(`SELECT kopie FROM boardpaar_aussen WHERE aussen_board_id='b1'`).Scan(&kopie); err != nil || kopie != 0 {
		t.Errorf("kopie nach 0040: %d (%v), erwartet 0", kopie, err)
	}
	var lane sql.NullString
	if err := db.QueryRow(`SELECT lane, fassung FROM schatten WHERE schatten_id='s1'`).Scan(&lane, &fassung); err != nil || lane.Valid || fassung != 1 {
		t.Errorf("schatten nach 0040: lane=%v fassung=%d (%v)", lane, fassung, err)
	}
	// CHECKs: Typ abr erlaubt, darf_anlegen nur bei abe, fassung nur 1/2.
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t3','abr','rueck','h3','x')`); err != nil {
		t.Errorf("abr abgelehnt: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at,darf_anlegen) VALUES ('t4','abs','x','h4','x',1)`); err == nil {
		t.Error("abs mit darf_anlegen angenommen")
	}
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t5','abx','x','h5','x')`); err == nil {
		t.Error("unbekannter Typ angenommen")
	}
	// Teilindex aus 0037 lebt: zweiter aktiver Name desselben Typs scheitert.
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t6','abs','push','h6','x')`); err == nil {
		t.Error("doppelter aktiver Name angenommen — Teilindex fehlt")
	}
}

func TestBoardpaar825_TokenTypenUndAnlegeRecht(t *testing.T) {
	ctx := context.Background()
	r := NewBoardpaarRepo(kopieTestDB(t))
	abr, z, err := r.TokenAnlegen(ctx, BoardpaarTypRueckweg, "rueck", "admin")
	if err != nil || !strings.HasPrefix(abr, RueckwegPraefix) || z.DarfAnlegen {
		t.Fatalf("abr: %q %+v %v", abr, z, err)
	}
	if BoardpaarTypAusPraefix(abr) != BoardpaarTypRueckweg || BoardpaarTypAusPraefix("abx_1") != "" {
		t.Error("BoardpaarTypAusPraefix")
	}
	if _, _, err := r.TokenAnlegenMit(ctx, BoardpaarTypAusgang, "x", "a", true); !errors.Is(err, ErrBoardpaarAnlegeRecht) {
		t.Errorf("abs mit Recht: %v", err)
	}
	abe, z, err := r.TokenAnlegenMit(ctx, BoardpaarTypEingang, "kopien", "admin", true)
	if err != nil || !z.DarfAnlegen {
		t.Fatalf("abe mit Recht: %+v %v", z, err)
	}
	if g, err := r.TokenPruefe(ctx, BoardpaarTypEingang, abe); err != nil || !g.DarfAnlegen {
		t.Errorf("TokenPruefe: %+v %v", g, err)
	}
	// W2-4: Erneuern übernimmt das Recht, auch bei der Wiederholung.
	e, err := r.TokenErneuern(ctx, BoardpaarTypEingang, abe, "192.0.2.9")
	if err != nil || !e.Zugang.DarfAnlegen {
		t.Fatalf("Erneuern: %+v %v", e, err)
	}
	if g, err := r.TokenPruefe(ctx, BoardpaarTypEingang, e.Token); err != nil || !g.DarfAnlegen {
		t.Errorf("Nachfolger ohne Recht: %+v %v", g, err)
	}
	w, err := r.TokenErneuern(ctx, BoardpaarTypEingang, abe, "192.0.2.9")
	if err != nil || !w.Wiederholt || !w.Zugang.DarfAnlegen {
		t.Errorf("Wiederholung: %+v %v", w, err)
	}
	// Ohne Recht bleibt ohne Recht.
	ohne, _, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "ohne", "admin")
	e2, err := r.TokenErneuern(ctx, BoardpaarTypEingang, ohne, "x")
	if err != nil || e2.Zugang.DarfAnlegen {
		t.Errorf("ohne Recht erneuert: %+v %v", e2, err)
	}
	// abr erneuert sich wie abs/abe.
	e3, err := r.TokenErneuern(ctx, BoardpaarTypRueckweg, abr, "x")
	if err != nil || !strings.HasPrefix(e3.Token, RueckwegPraefix) {
		t.Errorf("abr erneuern: %+v %v", e3, err)
	}
	liste, _ := r.TokenListe(ctx)
	mitRecht := 0
	for _, l := range liste {
		if l.DarfAnlegen {
			mitRecht++
		}
	}
	if mitRecht != 2 {
		t.Errorf("Liste: %d Zugänge mit Recht, erwartet 2 (Vorgänger + Nachfolger)", mitRecht)
	}
}

func TestBoardpaar825_ZieleUndAuftraege(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	if _, err := NewBoardRepo(db).Insert(ctx, "b1", "Projekt X", "2", CanonicalEmptyBoardState("b1", "Projekt X")); err != nil {
		t.Fatal(err)
	}
	z, err := r.ZieleSetzen(ctx, []string{"team-extern", "team-extern", " zwei "})
	if err != nil || len(z) != 2 || z[0].Name != "team-extern" || z[1].Name != "zwei" {
		t.Fatalf("Ziele: %+v %v", z, err)
	}
	if z, err = r.ZieleSetzen(ctx, []string{"zwei"}); err != nil || len(z) != 1 {
		t.Errorf("Ziele ersetzen: %+v %v", z, err)
	}
	if _, err := r.ZieleSetzen(ctx, []string{"mit leerzeichen"}); !errors.Is(err, ErrBoardpaarZielName) {
		t.Errorf("ungültiger Name: %v", err)
	}
	viele := make([]string, BoardpaarZieleMax+1)
	for i := range viele {
		viele[i] = "z" + strings.Repeat("x", i+1)
	}
	if _, err := r.ZieleSetzen(ctx, viele); !errors.Is(err, ErrBoardpaarZuVieleZiele) {
		t.Errorf("zu viele: %v", err)
	}

	a, err := r.AuftragAnlegen(ctx, "b1", "zwei", "https://innen.example/#board=b1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.InnenGekoppelt(ctx, "b1"); !ok {
		t.Error("Auftrag hat das Board nicht gekoppelt")
	}
	if _, err := r.AuftragAnlegen(ctx, "b1", "zwei", "https://innen.example/#board=b1", "admin"); !errors.Is(err, ErrBoardpaarAuftragOffen) {
		t.Errorf("zweiter offener Auftrag: %v", err)
	}
	if _, err := r.AuftragAnlegen(ctx, "service", "zwei", "https://i/#board=service", "a"); !errors.Is(err, ErrBoardpaarNichtFreigebbar) && !errors.Is(err, ErrBoardpaarBoardUnbekannt) {
		t.Errorf("Service-Board: %v", err)
	}
	if _, err := r.AuftragAnlegen(ctx, "b1", "drei", "http://unsicher/#board=b1", "a"); !errors.Is(err, ErrBoardpaarLinkBasis) {
		t.Errorf("Link http: %v", err)
	}
	offen, err := r.AuftraegeOffen(ctx)
	if err != nil || len(offen) != 1 || offen[0].Titel != "Projekt X" || offen[0].InnenLinkBasis != "https://innen.example/#board=b1" {
		t.Fatalf("offen: %+v %v", offen, err)
	}
	// Entkoppelt: Der offene Auftrag fällt mit (Kritik A-3) — auch nach
	// erneutem Koppeln ist er nicht wieder abholbar.
	if err := r.InnenEntkoppeln(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	_ = r.InnenKoppeln(ctx, "b1", "a")
	if offen, _ := r.AuftraegeOffen(ctx); len(offen) != 0 {
		t.Errorf("nach Entkoppeln und Koppeln wieder abholbar: %+v", offen)
	}
	if _, err := r.AuftragErledigt(ctx, a.ID); !errors.Is(err, ErrBoardpaarAuftragUnbekannt) {
		t.Errorf("zurückgezogener Auftrag: %v", err)
	}
	a, err = r.AuftragAnlegen(ctx, "b1", "zwei", "https://innen.example/#board=b1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.AuftragErledigt(ctx, a.ID)
	if err != nil || e.Status != AuftragErledigt || e.ErledigtAt == "" {
		t.Fatalf("erledigt: %+v %v", e, err)
	}
	// Wiederholbar, ohne erledigt_at zu verschieben.
	e2, err := r.AuftragErledigt(ctx, a.ID)
	if err != nil || e2.ErledigtAt != e.ErledigtAt {
		t.Errorf("Wiederholung: %+v %v", e2, err)
	}
	if _, err := r.AuftragErledigt(ctx, "gibt-es-nicht"); !errors.Is(err, ErrBoardpaarAuftragUnbekannt) {
		t.Errorf("unbekannt: %v", err)
	}
	if err := r.AuftragZurueckziehen(ctx, a.ID); !errors.Is(err, ErrBoardpaarAuftragErledigt) {
		t.Errorf("erledigten zurückziehen: %v", err)
	}
	// Nach dem Erledigen ist ein neuer Auftrag für dasselbe Ziel möglich.
	b, err := r.AuftragAnlegen(ctx, "b1", "zwei", "https://innen.example/#board=b1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AuftragZurueckziehen(ctx, b.ID); err != nil {
		t.Errorf("zurückziehen: %v", err)
	}
	if liste, _ := r.AuftraegeListe(ctx); len(liste) != 1 {
		t.Errorf("Liste nach Zurückziehen: %d, erwartet 1", len(liste))
	}
	// Löschen des Boards löscht die Aufträge (Fremdschlüssel).
	if err := NewBoardRepo(db).Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if liste, _ := r.AuftraegeListe(ctx); len(liste) != 0 {
		t.Errorf("nach Board-Löschung: %d Aufträge", len(liste))
	}
}

const kopieID = "6f1c2c1e-0d4b-5e2a-9d5a-2b1f0c3a4e5d"

func TestBoardpaar825_KopieAnlegen(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	if _, err := db.Exec(`INSERT INTO users (id,email,name,created,is_admin) VALUES ('u1','Admin@Firma.de','A','x',1),('u2','leser@firma.de','L','x',0)`); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ACLDomainsEnv, "team.example")
	team := []string{"Team@team.example", "@team.example", "@nicht-erlaubt.example", "admin@firma.de"}
	erg, err := r.KopieAnlegen(ctx, kopieID, "  Projekt X  ", "https://innen.example/#board=b1", "skript:kopien", team)
	if err != nil {
		t.Fatal(err)
	}
	if erg.Wiederholt || erg.Titel != "Projekt X" || len(erg.Mitglieder) != 3 {
		t.Errorf("Ergebnis: %+v", erg)
	}
	e, err := r.AussenGekoppelt(ctx, kopieID)
	if err != nil || e == nil || !e.Kopie || e.Von != "skript:kopien" {
		t.Fatalf("Kopplung: %+v %v", e, err)
	}
	acl := NewAclRepo(db)
	for subjekt, soll := range map[string]string{"admin@firma.de": RoleOwner, "team@team.example": RoleViewer, "@team.example": RoleViewer} {
		if rolle, ok, _ := acl.GetRole(ctx, kopieID, subjekt); !ok || rolle != soll {
			t.Errorf("%s: %q %v, erwartet %s", subjekt, rolle, ok, soll)
		}
	}
	if _, ok, _ := acl.GetRole(ctx, kopieID, "leser@firma.de"); ok {
		t.Error("Nicht-Admin ist Mitglied geworden")
	}
	// Wiederholung: dasselbe Ergebnis, nichts geändert (auch kein neuer Titel).
	w, err := r.KopieAnlegen(ctx, kopieID, "Anderer Titel", "https://anders.example/#board=b9", "skript:x", nil)
	if err != nil || !w.Wiederholt || w.Titel != "Projekt X" || w.InnenLinkBasis != "https://innen.example/#board=b1" {
		t.Errorf("Wiederholung: %+v %v", w, err)
	}
	// Belegte ID (kein Kopie-Board) → nie überschreiben.
	anders := "0e5d8a2c-1111-5222-8333-444455556666"
	if _, err := NewBoardRepo(db).Insert(ctx, anders, "Fremd", "2", CanonicalEmptyBoardState(anders, "Fremd")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.KopieAnlegen(ctx, anders, "X", "https://i.example/#board=b", "s", nil); !errors.Is(err, ErrBoardpaarKopieBelegt) {
		t.Errorf("belegte ID: %v", err)
	}
	_, _ = r.AussenKoppeln(ctx, anders, "https://i.example/#board=b", "a") // Stufe-1-Kopplung (kopie = 0)
	if _, err := r.KopieAnlegen(ctx, anders, "X", "https://i.example/#board=b", "s", nil); !errors.Is(err, ErrBoardpaarKopieBelegt) {
		t.Errorf("Stufe-1-Board: %v", err)
	}
	for _, f := range []struct {
		id, titel, link string
		soll            error
	}{
		{"KEINE-UUID", "X", "https://i/#board=b", ErrBoardpaarKopieID},
		{strings.ToUpper(kopieID), "X", "https://i/#board=b", ErrBoardpaarKopieID},
		{"0e5d8a2c-1111-5222-8333-444455556667", "", "https://i/#board=b", ErrBoardpaarKopieTitel},
		{"0e5d8a2c-1111-5222-8333-444455556667", "a\x07b", "https://i/#board=b", ErrBoardpaarKopieTitel},
		{"0e5d8a2c-1111-5222-8333-444455556667", "X", "ftp://i/#board=b", ErrBoardpaarLinkBasis},
	} {
		if _, err := r.KopieAnlegen(ctx, f.id, f.titel, f.link, "s", nil); !errors.Is(err, f.soll) {
			t.Errorf("%q/%q/%q: %v, erwartet %v", f.id, f.titel, f.link, err, f.soll)
		}
	}
}

// Verify V-2: Marken werden über das JSON erkannt, unabhängig von der
// Formatierung; Kartentext mit derselben Zeichenfolge ist keine Marke.
func TestBoardpaar825_TraegtSpiegelMarken(t *testing.T) {
	for st, soll := range map[string]bool{
		`{"board":{"sprints":[{"id":"x","spiegel": { "id" : "k" }}]}}`:                 true,
		`{"board":{"columnDefs":[{"id":"c","\u0073piegel":{"id":"k"}}]}}`:              true,
		`{"board":{"sprints":[{"id":"x","cards":[{"title":"\"spiegel\":{\"id\""}]}]}}`: false,
		`{"board":{"sprints":[]}}`: false,
		`kein json`:                true,
	} {
		if got := traegtSpiegelMarken([]byte(st)); got != soll {
			t.Errorf("%s: %v, erwartet %v", st, got, soll)
		}
	}
}

func TestBoardpaar825_TeamAusText(t *testing.T) {
	t.Setenv(ACLDomainsEnv, "team.example")
	norm, liste, err := TeamAusText(" A@x.de;\n@team.example, a@x.de ")
	if err != nil || norm != "a@x.de,@team.example" || len(liste) != 2 {
		t.Errorf("%q %v %v", norm, liste, err)
	}
	for _, roh := range []string{"kein-at", "@fremd.example", "a b@x.de"} {
		if _, _, err := TeamAusText(roh); err == nil {
			t.Errorf("%q angenommen", roh)
		}
	}
	if norm, _, err := TeamAusText(""); err != nil || norm != "" {
		t.Errorf("leer: %q %v", norm, err)
	}
}

func TestBoardpaar825_ErsetzeKopie(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	sr := NewSchattenRepo(db)
	if _, err := r.KopieAnlegen(ctx, kopieID, "K", "https://i.example/#board=b1", "s", nil); err != nil {
		t.Fatal(err)
	}
	vorher, _ := NewBoardRepo(db).Get(ctx, kopieID)
	lane, spalte, titel, name := "__backlog__", "col-ready", "Titel", "Ready"
	menge := []schatten.Eingang{{SchattenID: strings.Repeat("A", 43), Art: schatten.ArtKarte, Titel: &titel,
		Vertraulich: true, Lane: &lane, SpalteID: &spalte, Spalte: &name, Bearbeiter: []string{}}}
	unveraendert := func(s []byte) ([]byte, error) { return s, nil }
	erg, err := sr.ErsetzeKopie(ctx, kopieID, unveraendert, menge)
	if err != nil || erg.StrukturGeaendert || erg.Neu != 1 {
		t.Fatalf("erster Lauf: %+v %v", erg, err)
	}
	if b, _ := NewBoardRepo(db).Get(ctx, kopieID); b.UpdatedAt != vorher.UpdatedAt {
		t.Error("ohne Strukturänderung trotzdem geschrieben (updated_at)")
	}
	zeilen, _ := sr.Liste(ctx, kopieID)
	if len(zeilen) != 1 || zeilen[0].Fassung != 2 || *zeilen[0].Lane != lane || *zeilen[0].SpalteID != spalte {
		t.Fatalf("Zeilen: %+v", zeilen)
	}
	// Strukturänderung: geschrieben, updated_at steigt.
	neu := func(s []byte) ([]byte, error) {
		return []byte(strings.Replace(string(s), `"title":"K"`, `"title":"K2"`, 1)), nil
	}
	erg, err = sr.ErsetzeKopie(ctx, kopieID, neu, menge)
	if err != nil || !erg.StrukturGeaendert || erg.HatAenderung() || erg.UpdatedAt <= vorher.UpdatedAt {
		t.Errorf("Strukturänderung: %+v %v", erg, err)
	}
	if b, _ := NewBoardRepo(db).Get(ctx, kopieID); b.Title != "K2" || b.UpdatedAt != erg.UpdatedAt {
		t.Errorf("Board: %q %d", b.Title, b.UpdatedAt)
	}
	// Ein Fehler der Struktur rollt alles zurück, auch die Schatten.
	_, err = sr.ErsetzeKopie(ctx, kopieID, func([]byte) ([]byte, error) { return nil, schatten.ErrStand }, nil)
	if !errors.Is(err, schatten.ErrStand) {
		t.Errorf("Fehler: %v", err)
	}
	if z, _ := sr.Liste(ctx, kopieID); len(z) != 1 {
		t.Error("Schatten trotz Fehler entfernt")
	}
	// Hülle verletzt (andere ID) → Fehler, nichts geschrieben.
	_, err = sr.ErsetzeKopie(ctx, kopieID, func(s []byte) ([]byte, error) {
		return []byte(strings.Replace(string(s), kopieID, "fremd", 1)), nil
	}, nil)
	if !errors.Is(err, ErrHuelleVerletzt) {
		t.Errorf("Hülle: %v", err)
	}
	// Stufe-1-Weg auf der Kopie: nicht gekoppelt. Kopie-Weg auf einem Stufe-1-Board: ebenso.
	if _, err := sr.Ersetze(ctx, kopieID, nil); !errors.Is(err, ErrBoardpaarNichtGekoppelt) {
		t.Errorf("Ersetze auf Kopie: %v", err)
	}
	if _, err := NewBoardRepo(db).Insert(ctx, "s1", "S", "2", CanonicalEmptyBoardState("s1", "S")); err != nil {
		t.Fatal(err)
	}
	_, _ = r.AussenKoppeln(ctx, "s1", "https://i.example/#board=x", "a")
	if _, err := sr.ErsetzeKopie(ctx, "s1", unveraendert, nil); !errors.Is(err, ErrBoardpaarNichtGekoppelt) {
		t.Errorf("ErsetzeKopie auf Stufe-1-Board: %v", err)
	}
	if _, err := sr.Ersetze(ctx, "s1", nil); err != nil {
		t.Errorf("Stufe 1 läuft nicht mehr: %v", err)
	}

	// Kritik A-2: Eine Kopie mit Marken lässt sich nach dem Entkoppeln nicht
	// von Hand als Stufe-1-Board koppeln; eine bestehende Kopplung behält kopie.
	struktur := schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: "__backlog__", Titel: "Backlog", Kind: "backlog"}},
		Spalten: []schatten.StrukturSpalte{{ID: "col-ready", Titel: "Ready"}}}
	if _, err := sr.ErsetzeKopie(ctx, kopieID, func(s []byte) ([]byte, error) { return schatten.StrukturAnwenden(s, struktur) }, nil); err != nil {
		t.Fatal(err)
	}
	if e, err := r.AussenKoppeln(ctx, kopieID, "https://anders.example/#board=b1", "a"); err != nil || !e.Kopie {
		t.Errorf("Adresse ändern an bestehender Kopie: %+v %v", e, err)
	}
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AussenKoppeln(ctx, kopieID, "https://i.example/#board=b1", "a"); !errors.Is(err, ErrBoardpaarIstKopie) {
		t.Errorf("Kopie von Hand gekoppelt: %v", err)
	}
}

// Kritik Sprint 93 K-3/R-1: Eine außen entkoppelte Kopie wird beim nächsten
// Anlegen wieder aufgenommen (Board und Mitglieder bleiben); ein Board ohne
// Marken oder mit unlesbarem Zustand bleibt belegt.
func TestBoardpaar93_KopieWiederaufnahme(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	if _, err := r.KopieAnlegen(ctx, kopieID, "Projekt X", "https://innen.example/#board=b1", "s", nil); err != nil {
		t.Fatal(err)
	}
	markiert := `{"version":"2","board":{"id":"` + kopieID + `","sprints":[{"id":"l","spiegel":{"id":"k"},"cards":[]}]}}`
	if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, markiert, kopieID); err != nil {
		t.Fatal(err)
	}
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	erg, err := r.KopieAnlegen(ctx, kopieID, "Neuer Titel", "https://neu.example/#board=b1", "s2", nil)
	if err != nil || !erg.Wiederaufgenommen || erg.Wiederholt || erg.Titel != "Projekt X" || erg.InnenLinkBasis != "https://neu.example/#board=b1" {
		t.Fatalf("Wiederaufnahme: %+v %v", erg, err)
	}
	if e, err := r.AussenGekoppelt(ctx, kopieID); err != nil || e == nil || !e.Kopie {
		t.Errorf("Kopplung nach Wiederaufnahme: %+v %v", e, err)
	}
	// Danach wieder eine gewöhnliche Wiederholung.
	if w, err := r.KopieAnlegen(ctx, kopieID, "X", "https://x.example/#board=b1", "s", nil); err != nil || !w.Wiederholt {
		t.Errorf("Wiederholung danach: %+v %v", w, err)
	}
	// Verify V-2: Board mit Marken, aber Kopplungszeile kopie = 0 (Stufe 1,
	// Marken z. B. per Ganz-PUT) bleibt belegt — nicht 500 über den INSERT.
	stufe1 := "0e5d8a2c-1111-5222-8333-44445555cccc"
	if _, err := NewBoardRepo(db).Insert(ctx, stufe1, "S1", "2", CanonicalEmptyBoardState(stufe1, "S1")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AussenKoppeln(ctx, stufe1, "https://i.example/#board=s", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, strings.Replace(markiert, kopieID, stufe1, 1), stufe1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.KopieAnlegen(ctx, stufe1, "X", "https://i.example/#board=b", "s", nil); !errors.Is(err, ErrBoardpaarKopieBelegt) {
		t.Errorf("Stufe-1-Board mit Marken: %v, erwartet belegt", err)
	}
	for name, state := range map[string]string{
		"ohne Marken": `{"version":"2","board":{"sprints":[]}}`,
		"unlesbar":    `{"board":5}`,
	} {
		id := "0e5d8a2c-1111-5222-8333-44445555aaaa"
		if name == "unlesbar" {
			id = "0e5d8a2c-1111-5222-8333-44445555bbbb"
		}
		if _, err := NewBoardRepo(db).Insert(ctx, id, "Fremd", "2", []byte(state)); err != nil {
			t.Fatal(err)
		}
		if _, err := r.KopieAnlegen(ctx, id, "X", "https://i.example/#board=b", "s", nil); !errors.Is(err, ErrBoardpaarKopieBelegt) {
			t.Errorf("%s: %v, erwartet belegt", name, err)
		}
	}
}

// Kritik Sprint 93 S-1: HatAuftrag — offen und erledigt zählen, ein
// zurückgezogener nicht.
func TestBoardpaar93_HatAuftrag(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	if _, err := NewBoardRepo(db).Insert(ctx, "b1", "B", "2", CanonicalEmptyBoardState("b1", "B")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ZieleSetzen(ctx, []string{"team"}); err != nil {
		t.Fatal(err)
	}
	if ja, err := r.HatAuftrag(ctx, "b1"); err != nil || ja {
		t.Fatalf("ohne Auftrag: %v %v", ja, err)
	}
	a, err := r.AuftragAnlegen(ctx, "b1", "team", "https://innen.example/#board=b1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if ja, _ := r.HatAuftrag(ctx, "b1"); !ja {
		t.Error("offener Auftrag zählt nicht")
	}
	if err := r.AuftragZurueckziehen(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if ja, _ := r.HatAuftrag(ctx, "b1"); ja {
		t.Error("zurückgezogener Auftrag zählt")
	}
	b, _ := r.AuftragAnlegen(ctx, "b1", "team", "https://innen.example/#board=b1", "admin")
	if _, err := r.AuftragErledigt(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if ja, _ := r.HatAuftrag(ctx, "b1"); !ja {
		t.Error("erledigter Auftrag zählt nicht")
	}
	// Verify V-4: Entkoppeln innen nimmt auch den erledigten Auftrag mit — ein
	// späteres Koppeln von Hand (Stufe 1) öffnet den Hinweg v2 nicht wieder.
	if err := r.InnenEntkoppeln(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if err := r.InnenKoppeln(ctx, "b1", "admin"); err != nil {
		t.Fatal(err)
	}
	if ja, _ := r.HatAuftrag(ctx, "b1"); ja {
		t.Error("nach Entkoppeln und Koppeln von Hand zählt der alte Auftrag noch")
	}
}

// Kritik Sprint 93 K-7: Die Normalform besteht PruefeKopieTitel immer.
func TestBoardpaar93_KopieTitelNormalform(t *testing.T) {
	lang := strings.Repeat("ä", KopieTitelMax+20)
	for roh, soll := range map[string]string{
		"  Projekt\tX  ": "Projekt X",
		"":               "Board",
		"\x07\x07":       "Board",
		lang:             strings.Repeat("ä", KopieTitelMax),
		"a\xffb":         "ab",
	} {
		got := KopieTitelNormalform(roh)
		if got != soll {
			t.Errorf("%q: %q, erwartet %q", roh, got, soll)
		}
		if _, err := PruefeKopieTitel(got); err != nil {
			t.Errorf("%q: Normalform %q besteht die Prüfung nicht: %v", roh, got, err)
		}
	}
}
