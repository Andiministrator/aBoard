package storage

// Nacharbeit #831, Kritik K-1: „entkoppelte Kopie“ entscheidet die Herkunft
// (Migration 0042, boardpaar_kopie_herkunft), nicht die Spiegel-Marken im
// Board-Zustand. Die Marken setzt jeder Owner per Ganz-PUT oder Op; eine
// Sicherung einer früheren Kopie bringt sie per Restore mit.
//
// GEGENPROBE: Entscheiden KopieLoeschen/EntkoppelteKopien/KopieAnlegen wieder
// an den Marken (Stand 677e91f6), werden _GefaelschteMarken und
// _WiederaufnahmeNurMitHerkunft rot.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const herkunftFremd = "0e5d8a2c-1111-5222-8333-44445555f831"

func markierterStand(id string) []byte {
	return []byte(`{"version":"2","board":{"id":"` + id + `","title":"M","sprints":[{"id":"__backlog__","kind":"backlog","cards":[],"spiegel":{"id":"__backlog__"}}],` +
		`"columnDefs":[{"id":"col-backlog","spiegel":{"id":"col-backlog"}}]}}`)
}

func herkunftVorhanden(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM boardpaar_kopie_herkunft WHERE board_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// (a) + (c): Ein gewöhnliches Board bekommt Spiegel-Marken per Ganz-PUT
// (Update — derselbe Weg wie ein Restore aus der Sicherung einer früheren
// Kopie) und per Op (ApplyOps). Es erscheint nicht in EntkoppelteKopien, der
// Notweg lehnt ab, und ein Auftrag mit seiner ID nimmt es nicht wieder auf.
func TestBoardpaar831_GefaelschteMarken(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	boards := NewBoardRepo(db)

	// Echte Kopie anlegen, mit Marken füllen und ihren Stand „sichern“.
	if _, err := r.KopieAnlegen(ctx, kopieID, "Echt", "https://innen.example/#board=b1", "s", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, markierterStand(kopieID), kopieID); err != nil {
		t.Fatal(err)
	}
	var sicherung []byte
	if err := db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, kopieID).Scan(&sicherung); err != nil {
		t.Fatal(err)
	}

	// Zwei gewöhnliche Boards eines Owners (Insert entfernt Marken, F-2225).
	const perPut, perOp = herkunftFremd, "0e5d8a2c-1111-5222-8333-44445555e831"
	for _, id := range []string{perPut, perOp} {
		if _, err := boards.Insert(ctx, id, "Meins", "2", CanonicalEmptyBoardState(id, "Meins")); err != nil {
			t.Fatal(err)
		}
	}
	b, err := boards.Get(ctx, perPut)
	if err != nil {
		t.Fatal(err)
	}
	// Ganz-PUT bzw. Restore: die Sicherung der echten Kopie, nur die ID getauscht.
	restore := []byte(strings.ReplaceAll(string(sicherung), kopieID, perPut))
	if _, err := boards.Update(ctx, perPut, "Meins", "2", restore, b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := boards.ApplyOps(ctx, perOp, func([]byte) ([]byte, error) { return markierterStand(perOp), nil }, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{perPut, perOp} {
		var state []byte
		if err := db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if markiert, lesbar := spiegelMarken(state); !markiert || !lesbar {
			t.Fatalf("%s: Vorbedingung — Marken nicht angekommen (markiert=%v lesbar=%v)", id, markiert, lesbar)
		}
	}

	frei, err := r.EntkoppelteKopien(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(frei) != 0 {
		t.Errorf("EntkoppelteKopien nennt Boards mit selbst gesetzten Marken: %+v", frei)
	}
	for _, id := range []string{perPut, perOp} {
		if _, err := r.KopieLoeschen(ctx, id); !errors.Is(err, ErrBoardpaarKeineKopie) {
			t.Errorf("KopieLoeschen(%s): %v, erwartet ErrBoardpaarKeineKopie", id, err)
		}
		if _, err := r.KopieAnlegen(ctx, id, "X", "https://innen.example/#board=b", "s", nil); !errors.Is(err, ErrBoardpaarKopieBelegt) {
			t.Errorf("KopieAnlegen(%s) als Wiederaufnahme: %v, erwartet belegt", id, err)
		}
		if e, err := r.AussenGekoppelt(ctx, id); err != nil || e != nil {
			t.Errorf("%s nach abgelehnter Wiederaufnahme gekoppelt: %+v %v", id, e, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM boards WHERE id IN (?, ?)`, perPut, perOp).Scan(&n); err != nil || n != 2 {
		t.Errorf("Boards nach Ablehnung: %d %v", n, err)
	}
}

// (b): Eine echte Kopie hat Herkunft; nach dem Entkoppeln steht sie in der
// Liste und ist löschbar — auch ohne Marken (frische, kanonisch leere Kopie).
// Die Herkunft verschwindet mit dem Board (Fremdschlüssel).
func TestBoardpaar831_EchteKopieEntkoppelt(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	if _, err := r.KopieAnlegen(ctx, kopieID, "Echt", "https://innen.example/#board=b1", "s", nil); err != nil {
		t.Fatal(err)
	}
	if !herkunftVorhanden(t, db, kopieID) {
		t.Fatal("KopieAnlegen hat keine Herkunft geschrieben")
	}
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	if !herkunftVorhanden(t, db, kopieID) {
		t.Fatal("Entkoppeln hat die Herkunft gelöscht")
	}
	frei, err := r.EntkoppelteKopien(ctx)
	if err != nil || len(frei) != 1 || frei[0].BoardID != kopieID || frei[0].Titel != "Echt" {
		t.Fatalf("EntkoppelteKopien: %+v %v", frei, err)
	}
	g, err := r.KopieLoeschen(ctx, kopieID)
	if err != nil || !g.Entkoppelt {
		t.Fatalf("KopieLoeschen: %+v %v", g, err)
	}
	if herkunftVorhanden(t, db, kopieID) {
		t.Error("Herkunft überlebt das Löschen des Boards")
	}
	// Kopplungszeile mit kopie = 0 neben Herkunft (über AussenKoppeln seit
	// Sitzungs-Verify V-1 nicht mehr herstellbar, hier roh gesetzt): Der
	// Notweg behandelt das Board als Stufe-1-Board und lehnt ab.
	if _, err := r.KopieAnlegen(ctx, kopieID, "Echt", "https://innen.example/#board=b1", "s", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE boardpaar_aussen SET kopie = 0 WHERE aussen_board_id = ?`, kopieID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.KopieLoeschen(ctx, kopieID); !errors.Is(err, ErrBoardpaarKeineKopie) {
		t.Errorf("Kopie mit kopie = 0: %v, erwartet ErrBoardpaarKeineKopie", err)
	}
}

// Sitzungs-Verify V-1: Eine außen entkoppelte Kopie OHNE Spiegel-Marken (nie
// gespiegelt, oder der Owner hat sie per PUT entfernt) lässt sich nicht von
// Hand als Stufe-1-Board koppeln — die Herkunft entscheidet, nicht die
// Marken. Sonst wäre sie nach erneutem Entkoppeln wieder eine „entkoppelte
// Kopie“ für Notweg und Wiederaufnahme.
//
// GEGENPROBE: Prüft AussenKoppeln nur die Marken (Stand 33fcc5de), wird
// dieser Test rot.
func TestBoardpaar831_KoppelnVonHandMitHerkunft(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	if _, err := r.KopieAnlegen(ctx, kopieID, "Echt", "https://innen.example/#board=b1", "s", nil); err != nil {
		t.Fatal(err)
	}
	var state []byte
	if err := db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, kopieID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if traegtSpiegelMarken(state) {
		t.Fatal("Vorbedingung: frische Kopie trägt Marken")
	}
	// Adresse ändern an der gekoppelten Kopie bleibt erlaubt (kopie bleibt 1).
	if e, err := r.AussenKoppeln(ctx, kopieID, "https://anders.example/#board=b1", "a"); err != nil || !e.Kopie {
		t.Fatalf("Adresse ändern an gekoppelter Kopie: %+v %v", e, err)
	}
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AussenKoppeln(ctx, kopieID, "https://innen.example/#board=b1", "a"); !errors.Is(err, ErrBoardpaarIstKopie) {
		t.Fatalf("entkoppelte Kopie ohne Marken von Hand gekoppelt: %v, erwartet ErrBoardpaarIstKopie", err)
	}
	if e, err := r.AussenGekoppelt(ctx, kopieID); err != nil || e != nil {
		t.Errorf("nach Ablehnung gekoppelt: %+v %v", e, err)
	}
	if !herkunftVorhanden(t, db, kopieID) {
		t.Error("Herkunft nach Ablehnung weg")
	}
	// Bleibt eine entkoppelte Kopie: in der Liste und über den Notweg löschbar.
	frei, err := r.EntkoppelteKopien(ctx)
	if err != nil || len(frei) != 1 || frei[0].BoardID != kopieID {
		t.Errorf("EntkoppelteKopien: %+v %v", frei, err)
	}
	// Ein gewöhnliches Board ohne Herkunft und ohne Marken bleibt koppelbar.
	if _, err := NewBoardRepo(db).Insert(ctx, herkunftFremd, "Meins", "2", CanonicalEmptyBoardState(herkunftFremd, "Meins")); err != nil {
		t.Fatal(err)
	}
	if e, err := r.AussenKoppeln(ctx, herkunftFremd, "https://innen.example/#board=b2", "a"); err != nil || e.Kopie {
		t.Errorf("gewöhnliches Board: %+v %v", e, err)
	}
}

// (e): Wiederaufnahme nur mit Herkunft — eine entkoppelte echte Kopie OHNE
// Marken (nie gespiegelt) wird wieder aufgenommen; Marken ohne Herkunft
// genügen nicht (siehe _GefaelschteMarken).
func TestBoardpaar831_WiederaufnahmeNurMitHerkunft(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	if _, err := r.KopieAnlegen(ctx, kopieID, "Echt", "https://innen.example/#board=b1", "s", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	erg, err := r.KopieAnlegen(ctx, kopieID, "Neu", "https://neu.example/#board=b1", "s", nil)
	if err != nil || !erg.Wiederaufgenommen || erg.Titel != "Echt" {
		t.Fatalf("Wiederaufnahme ohne Marken: %+v %v", erg, err)
	}
	if !herkunftVorhanden(t, db, kopieID) {
		t.Error("Herkunft nach Wiederaufnahme weg")
	}
	// Herkunft von Hand entfernt (Stand einer vor 0042 entkoppelten Kopie):
	// Marken allein reichen nicht.
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, markierterStand(kopieID), kopieID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM boardpaar_kopie_herkunft WHERE board_id = ?`, kopieID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.KopieAnlegen(ctx, kopieID, "X", "https://innen.example/#board=b", "s", nil); !errors.Is(err, ErrBoardpaarKopieBelegt) {
		t.Errorf("Marken ohne Herkunft: %v, erwartet belegt", err)
	}
	if _, err := r.KopieLoeschen(ctx, kopieID); !errors.Is(err, ErrBoardpaarKeineKopie) {
		t.Errorf("Notweg bei Marken ohne Herkunft: %v, erwartet ErrBoardpaarKeineKopie", err)
	}
}

// (d): Backfill der Migration 0042 — eine bestehende Kopie (kopie = 1) bekommt
// Herkunft mit seit als Zeitpunkt, ein Stufe-1-Board (kopie = 0) und eine schon
// entkoppelte Kopie (keine Zeile) nicht.
func TestBoardpaar831_Migration0042Backfill(t *testing.T) {
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
		if m.version > 41 {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 41"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO boards (id,title,version,state_json,updated_at,created_at) VALUES ('k1','K','2','{}',1,1), ('s1','S','2','{}',1,1), ('e1','E','2','{"board":{"sprints":[{"spiegel":{"id":"x"}}]}}',1,1)`,
		`INSERT INTO boardpaar_aussen (aussen_board_id,innen_link_basis,seit,von,kopie) VALUES ('k1','https://i/#board=x','2026-10-01T08:00:00Z','a',1)`,
		`INSERT INTO boardpaar_aussen (aussen_board_id,innen_link_basis,seit,von,kopie) VALUES ('s1','https://i/#board=y','2026-10-02T08:00:00Z','a',0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT board_id, angelegt_at FROM boardpaar_kopie_herkunft ORDER BY board_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ist []string
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			t.Fatal(err)
		}
		ist = append(ist, id+"@"+at)
	}
	if len(ist) != 1 || ist[0] != "k1@2026-10-01T08:00:00Z" {
		t.Errorf("Herkunft nach 0042: %v, erwartet [k1@2026-10-01T08:00:00Z]", ist)
	}
	// Fremdschlüssel: Herkunft ohne Board wird abgelehnt.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO boardpaar_kopie_herkunft (board_id, angelegt_at) VALUES ('gibtsnicht', 'x')`); err == nil {
		t.Error("Herkunft ohne Board angenommen")
	}
}
