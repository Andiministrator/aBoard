package storage

// schleuse_dateien_test.go — Bilder im Auftrag der Ausgangsschleuse: Hash,
// Einzelfreigabe, Versand nur mit unveränderten Bild-Hashes (Karte #687,
// ADR-0134 §5).

import (
	"context"
	"errors"
	"testing"

	"github.com/Andiministrator/aBoard/server/schleuse"
)

func mitBildern(user, text string, bilder ...schleuse.BildEintrag) SchleuseAuftrag {
	a := neuerAuftrag(user, text)
	a.Bilder = bilder
	a.Teile.Dateien = []string{"datei-1"}
	return a
}

var (
	bildA = schleuse.BildEintrag{ID: "bild-a", MIME: "image/png", Bytes: 10, SHA256: schleuse.HashBytes([]byte("a"))}
	bildB = schleuse.BildEintrag{ID: "bild-b", MIME: "image/jpeg", Bytes: 20, SHA256: schleuse.HashBytes([]byte("b"))}
)

// Angelegt wird mit nicht freigegebenen Bildern — der Hash ist der des Texts;
// Bilder und Dateien kommen beim Lesen unverändert zurück.
func TestSchleuseDateien_AnlegenUndLesen(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	a, err := r.Anlegen(ctx, mitBildern("u-a", "Text", bildA, bildB), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != schleuse.Hash("Text") {
		t.Fatalf("Hash ohne freigegebenes Bild: %s", a.SHA256)
	}
	g, err := r.Get(ctx, a.ID, "u-a")
	if err != nil || len(g.Bilder) != 2 || g.Bilder[0] != bildA || g.Bilder[1] != bildB {
		t.Fatalf("Bilder: %+v %v", g.Bilder, err)
	}
	if len(g.Teile.Dateien) != 1 || g.Teile.Dateien[0] != "datei-1" {
		t.Fatalf("Dateien: %+v", g.Teile.Dateien)
	}
	// Bestand ohne Bilder: leere Liste, nie null.
	o, _ := r.Anlegen(ctx, neuerAuftrag("u-a", "ohne"), nil)
	if g, _ := r.Get(ctx, o.ID, "u-a"); g.Bilder == nil || len(g.Bilder) != 0 || g.Teile.Dateien == nil {
		t.Fatalf("ohne Bilder: %+v %+v", g.Bilder, g.Teile)
	}
}

// Die Einzelfreigabe eines Bildes ändert den Hash und hebt eine Freigabe des
// Auftrags auf — wie eine Textänderung (ADR-0134 §3.3).
func TestSchleuseDateien_EinzelfreigabeAendertHash(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	a, _ := r.Anlegen(ctx, mitBildern("u-a", "Text", bildA, bildB), nil)
	f, err := r.Freigeben(ctx, a.ID, "u-a", "u-a@example.com", a.SHA256, false)
	if err != nil {
		t.Fatal(err)
	}
	frei := []string{"bild-b"}
	g, err := r.AendereAuszug(ctx, a.ID, "u-a", f.SHA256, "Text", &frei)
	if err != nil {
		t.Fatal(err)
	}
	if g.Zustand != SchleuseZustandEntwurf || g.Freigegeben || g.FreigabeSHA256 != "" {
		t.Fatalf("Freigabe nicht aufgehoben: %+v", g)
	}
	if g.Bilder[0].Freigegeben || !g.Bilder[1].Freigegeben {
		t.Fatalf("Einzelfreigabe falsch: %+v", g.Bilder)
	}
	if g.SHA256 != schleuse.AuszugHash("Text", g.Bilder) || g.SHA256 == schleuse.Hash("Text") {
		t.Fatalf("Hash umfasst das Bild nicht: %s", g.SHA256)
	}
	// Gespeichert, nicht nur zurückgegeben.
	if l, _ := r.Get(ctx, a.ID, "u-a"); l.SHA256 != g.SHA256 || !l.Bilder[1].Freigegeben {
		t.Fatalf("nicht gespeichert: %+v", l)
	}
	// Unbekanntes Bild: nichts geschrieben.
	falsch := []string{"bild-x"}
	if _, err := r.AendereAuszug(ctx, a.ID, "u-a", g.SHA256, "Text", &falsch); !errors.Is(err, ErrSchleuseBildUnbekannt) {
		t.Fatalf("unbekanntes Bild: %v", err)
	}
	// nil = Bildfreigaben unverändert (Text ändern lässt das Bild freigegeben).
	h, err := r.AendereText(ctx, a.ID, "u-a", g.SHA256, "Text neu")
	if err != nil || !h.Bilder[1].Freigegeben || h.SHA256 != schleuse.AuszugHash("Text neu", h.Bilder) {
		t.Fatalf("Textänderung: %+v %v", h, err)
	}
	// Unveränderter Stand schreibt nichts (geaendert_am bleibt).
	gleich := []string{"bild-b"}
	k, err := r.AendereAuszug(ctx, a.ID, "u-a", h.SHA256, "Text neu", &gleich)
	if err != nil || k.GeaendertAm != h.GeaendertAm {
		t.Fatalf("unverändert: %+v %v", k, err)
	}
}

// Wer die Bild-Hashes in der Datenbank verändert, bekommt den Auftrag nicht
// hinaus: Freigeben und BeginneVersand rechnen den Hash über Text UND Bilder
// nach (Hash-Vertrag, ADR-0134 §5).
func TestSchleuseDateien_BildEingriffStopptVersand(t *testing.T) {
	db, r := versandDB(t)
	ctx := context.Background()
	a, _ := r.Anlegen(ctx, mitBildern("u-a", "Text", bildA), nil)
	frei := []string{"bild-a"}
	g, err := r.AendereAuszug(ctx, a.ID, "u-a", a.SHA256, "Text", &frei)
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.Freigeben(ctx, a.ID, "u-a", "u-a@example.com", g.SHA256, false)
	if err != nil {
		t.Fatal(err)
	}
	// Eingriff: anderes Bild, gleicher Auftrags-Hash.
	anders := `[{"id":"bild-a","mime":"image/png","bytes":10,"sha256":"` + schleuse.HashBytes([]byte("boese")) + `","freigegeben":true}]`
	mustExec(t, db, `UPDATE schleuse_auftraege SET bilder = ? WHERE id = ?`, anders, f.ID)
	v, err := r.BeginneVersand(ctx, f.ID, 10)
	if !errors.Is(err, ErrSchleuseHashAbweichung) || v.Zustand != SchleuseZustandFehlgeschlagen {
		t.Fatalf("Versand trotz Eingriff: %+v %v", v, err)
	}

	// Dasselbe vor der Freigabe: Freigeben lehnt ab.
	b, _ := r.Anlegen(ctx, mitBildern("u-a", "Text", bildA), nil)
	g2, _ := r.AendereAuszug(ctx, b.ID, "u-a", b.SHA256, "Text", &frei)
	mustExec(t, db, `UPDATE schleuse_auftraege SET bilder = ? WHERE id = ?`, anders, b.ID)
	if _, err := r.Freigeben(ctx, b.ID, "u-a", "u-a@example.com", g2.SHA256, false); !errors.Is(err, ErrSchleuseAuftragVeraltet) {
		t.Fatalf("Freigabe trotz Eingriff: %v", err)
	}
}

// Ein nicht freigegebenes Bild verändert nichts am Versand.
func TestSchleuseDateien_NichtFreigegebenGehtNichtMit(t *testing.T) {
	_, r := versandDB(t)
	ctx := context.Background()
	a, _ := r.Anlegen(ctx, mitBildern("u-a", "Text", bildA), nil)
	f, err := r.Freigeben(ctx, a.ID, "u-a", "u-a@example.com", a.SHA256, false)
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.BeginneVersand(ctx, f.ID, 10)
	if err != nil || v.Zustand != SchleuseZustandGesendet {
		t.Fatalf("Versand: %+v %v", v, err)
	}
	if len(schleuse.FreigegebeneBilder(v.Bilder)) != 0 {
		t.Fatalf("nicht freigegebenes Bild würde mitgehen: %+v", v.Bilder)
	}
}
