package storage

// schleuse_auftraege_test.go — Auftragsdatensätze der Ausgangsschleuse
// (Karte #685, ADR-0134 §3.1–§3.4, §4).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schleuse"
)

func neuerAuftrag(user, text string) SchleuseAuftrag {
	return SchleuseAuftrag{BoardID: "b1", CardID: "k1", UserID: user, UserEmail: user + "@example.com",
		FreigabeID: "fg-1", Anbieter: "anthropic", Modell: "m", Text: text,
		Ersetzungen: []schleuse.Ersetzung{{Platzhalter: "[PERSON_1]", Art: "PERSON", Quelle: "fest", Anzahl: 1}}}
}

// Die Zuordnung liegt nur verschlüsselt in der Tabelle; Text und Hash
// gehören zusammen; fremde Personen finden den Auftrag nicht.
func TestSchleuseAuftrag_AnlegenVerschluesseltUndNurEigen(t *testing.T) {
	db := loeschDB(t)
	r := NewSchleuseAuftragRepo(db, schleuseBox(t))
	ctx := context.Background()
	a, err := r.Anlegen(ctx, neuerAuftrag("u-a", "Hallo [PERSON_1]"), map[string]string{"[PERSON_1]": "Anna Geheimname"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Zustand != SchleuseZustandEntwurf || a.SHA256 != schleuse.Hash("Hallo [PERSON_1]") {
		t.Fatalf("angelegt: %+v", a)
	}
	var roh string
	if err := db.QueryRow(`SELECT zuordnung_verschluesselt || text || ersetzungen || teile FROM schleuse_auftraege WHERE id = ?`, a.ID).Scan(&roh); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(roh, "Geheimname") {
		t.Fatalf("Original unverschlüsselt in der Tabelle: %q", roh)
	}
	z, err := r.Zuordnung(ctx, a.ID, "u-a")
	if err != nil || z["[PERSON_1]"] != "Anna Geheimname" {
		t.Fatalf("Zuordnung: %v %v", z, err)
	}
	if _, err := r.Get(ctx, a.ID, "u-b"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("fremde Person liest: %v", err)
	}
	if _, err := r.Zuordnung(ctx, a.ID, "u-b"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("fremde Person liest Zuordnung: %v", err)
	}
	if _, err := r.AendereText(ctx, a.ID, "u-b", a.SHA256, "x"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("fremde Person ändert: %v", err)
	}
	if err := r.Loeschen(ctx, a.ID, "u-b"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("fremde Person löscht: %v", err)
	}
	if n, _ := r.ZaehleEntwuerfe(ctx, "u-a"); n != 1 {
		t.Errorf("Entwürfe: %d", n)
	}
}

// Jede Änderung: Hash neu, Zustand „entwurf“, Freigabe weg. Veralteter
// Ausgangshash → Fehler. Gesendete Aufträge sind nicht mehr änderbar.
func TestSchleuseAuftrag_AendernSetztZurueck(t *testing.T) {
	db := loeschDB(t)
	r := NewSchleuseAuftragRepo(db, schleuseBox(t))
	ctx := context.Background()
	a, err := r.Anlegen(ctx, neuerAuftrag("u-a", "alt"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// So, wie S3 einen freigegebenen Auftrag hinterlassen wird.
	if _, err := db.Exec(`UPDATE schleuse_auftraege SET zustand = 'freigegeben', freigabe_sha256 = text_sha256,
		freigegeben_von = 'u-b', freigegeben_am = '2026-10-04T00:00:00.000Z' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AendereText(ctx, a.ID, "u-a", "falscher-hash", "neu"); !errors.Is(err, ErrSchleuseAuftragVeraltet) {
		t.Fatalf("veralteter Hash: %v", err)
	}
	b, err := r.AendereText(ctx, a.ID, "u-a", a.SHA256, "neu")
	if err != nil {
		t.Fatal(err)
	}
	if b.SHA256 != schleuse.Hash("neu") || b.SHA256 == a.SHA256 || b.Zustand != SchleuseZustandEntwurf || b.Freigegeben {
		t.Fatalf("nach Änderung: %+v", b)
	}
	var zustand string
	var fs, fv, fa *string
	_ = db.QueryRow(`SELECT zustand, freigabe_sha256, freigegeben_von, freigegeben_am FROM schleuse_auftraege WHERE id = ?`, a.ID).Scan(&zustand, &fs, &fv, &fa)
	if zustand != "entwurf" || fs != nil || fv != nil || fa != nil {
		t.Fatalf("Freigabe nicht geleert: %s %v %v %v", zustand, fs, fv, fa)
	}
	// Unveränderter Text: kein neuer Stand.
	c, err := r.AendereText(ctx, a.ID, "u-a", b.SHA256, "neu")
	if err != nil || c.GeaendertAm != b.GeaendertAm || c.SHA256 != b.SHA256 {
		t.Errorf("unverändert: %+v %v", c, err)
	}
	// Gesendet: weder ändern noch löschen.
	if _, err := db.Exec(`UPDATE schleuse_auftraege SET zustand = 'gesendet' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AendereText(ctx, a.ID, "u-a", b.SHA256, "anders"); !errors.Is(err, ErrSchleuseAuftragNichtEntwurf) {
		t.Errorf("gesendet ändern: %v", err)
	}
	if err := r.Loeschen(ctx, a.ID, "u-a"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("gesendet löschen: %v", err)
	}
	// Die Tabelle selbst lässt keinen Entwurf mit Freigabe zu.
	if _, err := db.Exec(`UPDATE schleuse_auftraege SET zustand = 'entwurf', freigabe_sha256 = 'x' WHERE id = ?`, a.ID); err == nil {
		t.Errorf("Entwurf mit Freigabe-Hash angenommen")
	}
	if _, err := db.Exec(`UPDATE schleuse_auftraege SET zustand = 'unbekannt' WHERE id = ?`, a.ID); err == nil {
		t.Errorf("unbekannter Zustand angenommen")
	}
}

// Nie gesendete Entwürfe gehen nach der Frist ab der letzten Änderung weg.
func TestSchleuseAuftrag_EntwuerfeAufraeumen(t *testing.T) {
	db := loeschDB(t)
	r := NewSchleuseAuftragRepo(db, schleuseBox(t))
	ctx := context.Background()
	alt := timeNow
	t.Cleanup(func() { timeNow = alt })
	jetzt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return jetzt.Add(-31 * 24 * time.Hour) }
	altEntwurf, _ := r.Anlegen(ctx, neuerAuftrag("u-a", "alt"), nil)
	altGesendet, _ := r.Anlegen(ctx, neuerAuftrag("u-a", "gesendet"), nil)
	_, _ = db.Exec(`UPDATE schleuse_auftraege SET zustand = 'gesendet' WHERE id = ?`, altGesendet.ID)
	timeNow = func() time.Time { return jetzt.Add(-29 * 24 * time.Hour) }
	jung, _ := r.Anlegen(ctx, neuerAuftrag("u-a", "jung"), nil)
	timeNow = func() time.Time { return jetzt }
	n, err := r.EntwuerfeAufraeumen(ctx, 30)
	if err != nil || n != 1 {
		t.Fatalf("aufgeräumt: %d %v", n, err)
	}
	if _, err := r.Get(ctx, altEntwurf.ID, "u-a"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("alter Entwurf noch da")
	}
	for _, id := range []string{altGesendet.ID, jung.ID} {
		if _, err := r.Get(ctx, id, "u-a"); err != nil {
			t.Errorf("%s fehlt: %v", id, err)
		}
	}
	if n, _ := r.EntwuerfeAufraeumen(ctx, 0); n != 0 {
		t.Errorf("tage=0 räumt auf")
	}
}
