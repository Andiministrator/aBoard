package storage

// schleuse_versand_test.go — Freigabe, Vier-Augen und Versandzustände der
// Ausgangsschleuse (Karte #686, ADR-0134 §3.4–§3.7, §4).

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schleuse"
)

func versandDB(t *testing.T) (*sql.DB, *SchleuseAuftragRepo) {
	t.Helper()
	db := loeschDB(t)
	mustExec(t, db, `INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b1', 'B', '1', '{}', 1, 1)`)
	return db, NewSchleuseAuftragRepo(db, schleuseBox(t))
}

func freigegeben(t *testing.T, r *SchleuseAuftragRepo, user, text string, vierAugen bool) SchleuseAuftrag {
	t.Helper()
	ctx := context.Background()
	a, err := r.Anlegen(ctx, neuerAuftrag(user, text), map[string]string{"[PERSON_1]": "Anna"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.Freigeben(ctx, a.ID, user, user+"@example.com", a.SHA256, vierAugen)
	if err != nil {
		t.Fatalf("Freigeben: %v", err)
	}
	return f
}

// Die Freigabe ist an den gezeigten Hash gebunden; ein anderer Hash, ein
// fremder Auftrag oder ein gesperrtes Board geben nichts frei.
func TestSchleuseVersand_FreigabeAnHashGebunden(t *testing.T) {
	db, r := versandDB(t)
	ctx := context.Background()
	a, _ := r.Anlegen(ctx, neuerAuftrag("u-a", "Hallo [PERSON_1]"), nil)
	if _, err := r.Freigeben(ctx, a.ID, "u-a", "u-a@example.com", schleuse.Hash("anders"), false); !errors.Is(err, ErrSchleuseAuftragVeraltet) {
		t.Errorf("fremder Hash: %v", err)
	}
	if _, err := r.Freigeben(ctx, a.ID, "u-b", "u-b@example.com", a.SHA256, false); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("fremde Person: %v", err)
	}
	mustExec(t, db, `UPDATE boards SET schleuse_gesperrt = 1 WHERE id = 'b1'`)
	if _, err := r.Freigeben(ctx, a.ID, "u-a", "u-a@example.com", a.SHA256, false); !errors.Is(err, ErrSchleuseBoardGesperrt) {
		t.Errorf("gesperrt: %v", err)
	}
	mustExec(t, db, `UPDATE boards SET schleuse_gesperrt = 0 WHERE id = 'b1'`)
	f, err := r.Freigeben(ctx, a.ID, "u-a", "u-a@example.com", a.SHA256, false)
	if err != nil || f.Zustand != SchleuseZustandFreigegeben || f.FreigabeSHA256 != a.SHA256 || f.FreigegebenVon != "u-a@example.com" {
		t.Fatalf("Freigabe: %+v %v", f, err)
	}
	// Doppelklick: dieselbe Freigabe noch einmal ist kein Fehler.
	if _, err := r.Freigeben(ctx, a.ID, "u-a", "u-a@example.com", a.SHA256, false); err != nil {
		t.Errorf("zweite gleiche Freigabe: %v", err)
	}
	// Eine Textänderung hebt die Freigabe auf (ADR-0134 §3.3).
	g, err := r.AendereText(ctx, a.ID, "u-a", a.SHA256, "Hallo [PERSON_1]!")
	if err != nil || g.Zustand != SchleuseZustandEntwurf || g.Freigegeben || g.FreigabeSHA256 != "" {
		t.Fatalf("Änderung nach Freigabe: %+v %v", g, err)
	}
	if _, err := r.BeginneVersand(ctx, a.ID, 10); !errors.Is(err, ErrSchleuseNichtSendebereit) {
		t.Errorf("Versand nach Änderung: %v", err)
	}
}

// Vier-Augen: erst nach der zweiten Freigabe einer anderen Person sendebereit.
func TestSchleuseVersand_VierAugen(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	a := freigegeben(t, r, "u-a", "Text", true)
	if _, err := r.BeginneVersand(ctx, a.ID, 10); !errors.Is(err, ErrSchleuseWartetZweitfreigabe) {
		t.Fatalf("Versand ohne zweite Freigabe: %v", err)
	}
	if ids, _ := r.Sendebereite(ctx); len(ids) != 0 {
		t.Errorf("sendebereit ohne zweite Freigabe: %v", ids)
	}
	if _, err := r.ZweitFreigeben(ctx, a.ID, "b1", "k1", "U-A@example.com", a.SHA256); !errors.Is(err, ErrSchleuseSelbstfreigabe) {
		t.Errorf("Selbstfreigabe: %v", err)
	}
	if _, err := r.ZweitFreigeben(ctx, a.ID, "b1", "k2", "u-b@example.com", a.SHA256); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("falsche Karte: %v", err)
	}
	if _, err := r.ZweitFreigeben(ctx, a.ID, "b1", "k1", "u-b@example.com", schleuse.Hash("x")); !errors.Is(err, ErrSchleuseAuftragVeraltet) {
		t.Errorf("falscher Hash: %v", err)
	}
	if l, _ := r.ZurZweitfreigabe(ctx, "b1", "k1", "u-b@example.com"); len(l) != 1 {
		t.Errorf("Liste für die zweite Person: %d", len(l))
	}
	if l, _ := r.ZurZweitfreigabe(ctx, "b1", "k1", "u-a@example.com"); len(l) != 0 {
		t.Errorf("Antragstellerin sieht den eigenen Auftrag zur Zweitfreigabe")
	}
	z, err := r.ZweitFreigeben(ctx, a.ID, "b1", "k1", "u-b@example.com", a.SHA256)
	if err != nil || z.ZweitFreigegebenVon != "u-b@example.com" {
		t.Fatalf("Zweitfreigabe: %+v %v", z, err)
	}
	if _, err := r.ZweitFreigeben(ctx, a.ID, "b1", "k1", "u-c@example.com", a.SHA256); !errors.Is(err, ErrSchleuseNichtZweitfreigebbar) {
		t.Errorf("dritte Freigabe: %v", err)
	}
	g, err := r.BeginneVersand(ctx, a.ID, 10)
	if err != nil || g.Zustand != SchleuseZustandGesendet || g.Text != "Text" {
		t.Fatalf("Versand: %+v %v", g, err)
	}
}

// Eine Sperre, die nach der Freigabe gesetzt wird, hält den Versand auf —
// geprüft in derselben Transaktion, die „gesendet“ setzt; die Freigabe ist
// danach aufgehoben, gesendet ist nichts.
func TestSchleuseVersand_SperreNachFreigabe(t *testing.T) {
	db, r := versandDB(t)
	ctx := context.Background()
	a := freigegeben(t, r, "u-a", "Text", false)
	mustExec(t, db, `UPDATE boards SET schleuse_gesperrt = 1 WHERE id = 'b1'`)
	g, err := r.BeginneVersand(ctx, a.ID, 10)
	if !errors.Is(err, ErrSchleuseBoardGesperrt) {
		t.Fatalf("Versand trotz Sperre: %+v %v", g, err)
	}
	b, _ := r.Get(ctx, a.ID, "u-a")
	if b.Zustand != SchleuseZustandEntwurf || b.Freigegeben || b.GesendetAm != "" || b.FehlerGrund != SchleuseGrundGesperrt {
		t.Errorf("nach Sperre: %+v", b)
	}
}

// Höchstens n gesendete Aufträge je Person und Stunde; der überzählige geht
// zurück auf „entwurf“, eine andere Person ist nicht betroffen.
func TestSchleuseVersand_Stundengrenze(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	eins := freigegeben(t, r, "u-a", "eins", false)
	zwei := freigegeben(t, r, "u-a", "zwei", false)
	drei := freigegeben(t, r, "u-b", "drei", false)
	if _, err := r.BeginneVersand(ctx, eins.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginneVersand(ctx, zwei.ID, 1); !errors.Is(err, ErrSchleuseGrenzeErreicht) {
		t.Fatalf("Grenze: %v", err)
	}
	if b, _ := r.Get(ctx, zwei.ID, "u-a"); b.Zustand != SchleuseZustandEntwurf || b.FehlerGrund != SchleuseGrundGrenze {
		t.Errorf("überzählig: %+v", b)
	}
	if _, err := r.BeginneVersand(ctx, drei.ID, 1); err != nil {
		t.Errorf("andere Person: %v", err)
	}
	if n, _ := r.GesendetLetzteStunde(ctx, "u-a"); n != 1 {
		t.Errorf("gezählt: %d", n)
	}
	// Ein Auftrag, der vor mehr als einer Stunde ging, zählt nicht mehr.
	alt := timeNow
	t.Cleanup(func() { timeNow = alt })
	timeNow = func() time.Time { return time.Now().Add(61 * time.Minute) }
	vier := freigegeben(t, r, "u-a", "vier", false)
	if _, err := r.BeginneVersand(ctx, vier.ID, 1); err != nil {
		t.Errorf("nach einer Stunde: %v", err)
	}
}

// Gesendet ist gesendet: Kein Übergang führt zurück nach „freigegeben“;
// Abschliessen und Fehlschlagen nur aus dem passenden Zustand.
func TestSchleuseVersand_KeineWiederholung(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	a := freigegeben(t, r, "u-a", "Text", false)
	if _, err := r.BeginneVersand(ctx, a.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginneVersand(ctx, a.ID, 10); !errors.Is(err, ErrSchleuseNichtSendebereit) {
		t.Errorf("zweiter Versand: %v", err)
	}
	if _, err := r.AendereText(ctx, a.ID, "u-a", a.SHA256, "neu"); !errors.Is(err, ErrSchleuseAuftragNichtEntwurf) {
		t.Errorf("Änderung nach Versand: %v", err)
	}
	if err := r.Loeschen(ctx, a.ID, "u-a"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("Löschen nach Versand: %v", err)
	}
	if n, _ := r.LoescheOffene(ctx, "u-a", []string{a.ID}); n != 0 {
		t.Errorf("LoescheOffene nach Versand: %d", n)
	}
	if err := r.Abschliessen(ctx, a.ID, SchleuseErgebnis{AntwortText: "Antwort [PERSON_1]", KommentarID: "c9", KommentarSHA256: "h",
		VeraendertePlatzhalter: []string{"[Person_2]"}, InputTokens: 3, OutputTokens: 4, KostenUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	b, _ := r.Get(ctx, a.ID, "u-a")
	if b.Zustand != SchleuseZustandBeantwortet || b.AntwortSHA256 != schleuse.Hash("Antwort [PERSON_1]") || b.KommentarID != "c9" ||
		len(b.VeraendertePlatzhalter) != 1 || b.InputTokens != 3 || b.KostenUSD != 0.5 || b.AbgeschlossenAm == "" {
		t.Errorf("abgeschlossen: %+v", b)
	}
	if err := r.Abschliessen(ctx, a.ID, SchleuseErgebnis{}); !errors.Is(err, ErrSchleuseNichtSendebereit) {
		t.Errorf("zweiter Abschluss: %v", err)
	}
	if err := r.Fehlschlagen(ctx, a.ID, "x"); !errors.Is(err, ErrSchleuseNichtSendebereit) {
		t.Errorf("Fehlschlag nach Abschluss: %v", err)
	}
}

// Ein Text, der nicht zum freigegebenen Hash passt (Eingriff an der
// Datenbank), geht nicht hinaus.
func TestSchleuseVersand_HashAbweichungSendetNicht(t *testing.T) {
	db, r := versandDB(t)
	ctx := context.Background()
	a := freigegeben(t, r, "u-a", "Text", false)
	mustExec(t, db, `UPDATE schleuse_auftraege SET text = 'Anderer Text' WHERE id = ?`, a.ID)
	if _, err := r.BeginneVersand(ctx, a.ID, 10); !errors.Is(err, ErrSchleuseHashAbweichung) {
		t.Fatalf("Abweichung: %v", err)
	}
	if b, _ := r.Get(ctx, a.ID, "u-a"); b.Zustand != SchleuseZustandFehlgeschlagen || b.GesendetAm != "" {
		t.Errorf("nach Abweichung: %+v", b)
	}
}

// Beim Start: Was noch auf „gesendet“ steht, war unterwegs, als der Server
// endete → fehlgeschlagen, ohne Wiederholung. Freigegebene sind sendebereit.
func TestSchleuseVersand_UnterbrocheneUndSendebereite(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	unterwegs := freigegeben(t, r, "u-a", "unterwegs", false)
	if _, err := r.BeginneVersand(ctx, unterwegs.ID, 10); err != nil {
		t.Fatal(err)
	}
	wartet := freigegeben(t, r, "u-a", "wartet", false)
	aus, err := r.UnterbrocheneAbschliessen(ctx)
	if err != nil || len(aus) != 1 || aus[0].ID != unterwegs.ID {
		t.Fatalf("unterbrochen: %+v %v", aus, err)
	}
	if b, _ := r.Get(ctx, unterwegs.ID, "u-a"); b.Zustand != SchleuseZustandFehlgeschlagen || b.FehlerGrund != SchleuseGrundUnterbrochen {
		t.Errorf("unterbrochener Auftrag: %+v", b)
	}
	ids, err := r.Sendebereite(ctx)
	if err != nil || len(ids) != 1 || ids[0] != wartet.ID {
		t.Errorf("sendebereit: %v %v", ids, err)
	}
}

// ADR-0134 §4: Die Zuordnung abgeschlossener Aufträge geht nach der Frist ab
// Abschluss; Auszug und Antwort bleiben pseudonymisiert stehen.
func TestSchleuseVersand_ZuordnungNachFrist(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	alt := timeNow
	t.Cleanup(func() { timeNow = alt })
	jetzt := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return jetzt.Add(-31 * 24 * time.Hour) }
	altA := freigegeben(t, r, "u-a", "alt", false)
	_, _ = r.BeginneVersand(ctx, altA.ID, 10)
	_ = r.Abschliessen(ctx, altA.ID, SchleuseErgebnis{AntwortText: "a"})
	timeNow = func() time.Time { return jetzt.Add(-29 * 24 * time.Hour) }
	jungA := freigegeben(t, r, "u-a", "jung", false)
	_, _ = r.BeginneVersand(ctx, jungA.ID, 10)
	_ = r.Abschliessen(ctx, jungA.ID, SchleuseErgebnis{AntwortText: "b"})
	timeNow = func() time.Time { return jetzt }
	n, err := r.ZuordnungenAufraeumen(ctx, 30)
	if err != nil || n != 1 {
		t.Fatalf("aufgeräumt: %d %v", n, err)
	}
	if z, _ := r.Zuordnung(ctx, altA.ID, "u-a"); len(z) != 0 {
		t.Errorf("alte Zuordnung noch da: %v", z)
	}
	if z, _ := r.Zuordnung(ctx, jungA.ID, "u-a"); z["[PERSON_1]"] != "Anna" {
		t.Errorf("junge Zuordnung fehlt: %v", z)
	}
	if b, _ := r.Get(ctx, altA.ID, "u-a"); b.Text != "alt" || b.AntwortText != "a" {
		t.Errorf("Beleg nach Frist: %+v", b)
	}
}

// Kritiker KS3-5: Ein freigegebener, nie gesendeter Auftrag (etwa einer, der
// auf die zweite Freigabe wartet) geht nach der Frist samt Zuordnung weg.
func TestSchleuseVersand_WartendeFreigabeHatFrist(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	alt := timeNow
	t.Cleanup(func() { timeNow = alt })
	jetzt := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return jetzt.Add(-31 * 24 * time.Hour) }
	wartet := freigegeben(t, r, "u-a", "wartet", true)
	timeNow = func() time.Time { return jetzt }
	if n, err := r.EntwuerfeAufraeumen(ctx, 30); err != nil || n != 1 {
		t.Fatalf("aufgeräumt: %d %v", n, err)
	}
	if _, err := r.ZweitFreigeben(ctx, wartet.ID, "b1", "k1", "u-b@example.com", wartet.SHA256); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("späte zweite Freigabe: %v", err)
	}
}
