package api_test

// Karte #848: abgeschlossene Zugänge aus der Tabelle entfernen — HTTP-Wege.
// DELETE …/zugaenge/{id}/zeile entfernt nur abgeschlossene (widerrufene)
// Zeilen, ein geltender Zugang gibt 409 und bleibt; DELETE …/zugaenge/{id}
// heisst weiter „widerrufen“. POST …/zugaenge/aufraeumen räumt alle
// abgeschlossenen ab. Beides nur für Server-Admins, mit Audit je Zeile.
// Die Fristfälle (abgelöst, Uhr) prüft storage/boardpaar_zeile_loeschen_848_test.go.

import (
	"context"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

func TestBoardpaarAdmin848_ZeileEntfernen(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	const admin = "admin@firma.de"
	_, aktiv, err := e.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "aktiv", "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, w, _ := e.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "weg", "admin")
	if err := e.paar.TokenWiderrufen(ctx, w.ID); err != nil {
		t.Fatal(err)
	}

	// Nur Server-Admins.
	for _, weg := range []struct{ m, p string }{
		{http.MethodDelete, "/admin/boardpaar/zugaenge/" + w.ID + "/zeile"},
		{http.MethodPost, "/admin/boardpaar/zugaenge/aufraeumen"},
	} {
		if code, _, raw := e.do(t, weg.m, weg.p, "owner@firma.de", nil); code != http.StatusForbidden {
			t.Errorf("%s %s als Board-Owner: %d %s, erwartet 403", weg.m, weg.p, code, raw)
		}
	}
	if l, _ := e.paar.TokenListe(ctx); len(l) != 2 {
		t.Fatalf("Nicht-Admin hat entfernt: %d Zugänge", len(l))
	}

	// Geltender Zugang: 409, bleibt.
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+aktiv.ID+"/zeile", admin, nil); code != http.StatusConflict {
		t.Errorf("geltender Zugang: %d %s, erwartet 409", code, raw)
	}
	// Widerrufener: 200, weg, Audit-Eintrag mit Typ und Name.
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+w.ID+"/zeile", admin, nil); code != http.StatusOK {
		t.Fatalf("widerrufener Zugang: %d %s", code, raw)
	}
	if code, _, _ := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+w.ID+"/zeile", admin, nil); code != http.StatusNotFound {
		t.Errorf("zweites Entfernen: %d, erwartet 404", code)
	}
	liste, _ := e.paar.TokenListe(ctx)
	if len(liste) != 1 || liste[0].ID != aktiv.ID {
		t.Errorf("nach dem Entfernen: %+v", liste)
	}
	eintraege, _ := e.cfg.Audit(ctx, "", 50)
	gefunden := 0
	for _, a := range eintraege {
		if a.Action == storage.AuditActionBoardpaarZugangEntfernen {
			gefunden++
			if a.Key != "boardpaar/zugaenge/"+w.ID || a.OldValueRedacted != "abs_ weg" || a.UserID != "u-admin" {
				t.Errorf("Audit-Eintrag: %+v", a)
			}
		}
	}
	if gefunden != 1 {
		t.Errorf("%d Audit-Einträge fürs Entfernen, erwartet 1", gefunden)
	}

	// DELETE …/{id} heisst weiter „widerrufen“, nicht „entfernen“.
	if code, _, _ := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+aktiv.ID, admin, nil); code != http.StatusOK {
		t.Fatalf("Widerruf: %d", code)
	}
	liste, _ = e.paar.TokenListe(ctx)
	if len(liste) != 1 || liste[0].RevokedAt == "" {
		t.Errorf("Widerruf hat die Zeile entfernt oder nicht widerrufen: %+v", liste)
	}
}

func TestBoardpaarAdmin848_Aufraeumen(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	const admin = "admin@firma.de"
	_, aktiv, _ := e.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "aktiv", "admin")
	for _, n := range []string{"a", "b", "c"} {
		_, z, _ := e.paar.TokenAnlegen(ctx, storage.BoardpaarTypSicherungen, n, "admin")
		_ = e.paar.TokenWiderrufen(ctx, z.ID)
	}
	code, m, raw := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge/aufraeumen", admin, nil)
	if code != http.StatusOK || m["entfernt"] != float64(3) || m["uebersprungen"] != float64(0) {
		t.Fatalf("Aufräumen: %d %s", code, raw)
	}
	liste, _ := e.paar.TokenListe(ctx)
	if len(liste) != 1 || liste[0].ID != aktiv.ID {
		t.Errorf("nach dem Aufräumen: %+v", liste)
	}
	eintraege, _ := e.cfg.Audit(ctx, "", 50)
	gefunden := 0
	for _, a := range eintraege {
		if a.Action == storage.AuditActionBoardpaarZugangEntfernen {
			gefunden++
		}
	}
	if gefunden != 3 {
		t.Errorf("%d Audit-Einträge, erwartet 3 (je Zeile einer)", gefunden)
	}
	// Nichts mehr abzuräumen: 0, kein Fehler.
	if code, m, raw := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge/aufraeumen", admin, nil); code != http.StatusOK || m["entfernt"] != float64(0) {
		t.Errorf("zweites Aufräumen: %d %s", code, raw)
	}
}

// F-2329: ErrBoardpaarUhr wird im HTTP-Weg zu 503 (nicht 500, nicht 409). Die
// Naht ist die exportierte Variable uhr.Gestellt (storage.uhrGestellt ruft sie
// bei jedem Aufruf); ein Zugang in der Übergangszeit ist erst mit gestellter
// Uhr erneuerbar, danach wird die Uhr „ungestellt“. Die Zeile bleibt stehen.
func TestBoardpaarAdmin848_UngestellteUhr503(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	const admin = "admin@firma.de"
	a, alt, err := e.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.paar.TokenErneuern(ctx, storage.BoardpaarTypAusgang, a, ""); err != nil {
		t.Fatal(err)
	}
	alt0 := uhr.Gestellt
	uhr.Gestellt = func() (bool, error) { return false, nil }
	t.Cleanup(func() { uhr.Gestellt = alt0 })

	code, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+alt.ID+"/zeile", admin, nil)
	if code != http.StatusServiceUnavailable {
		t.Errorf("abgelöster Zugang bei ungestellter Uhr: %d %s, erwartet 503", code, raw)
	}
	if l, _ := e.paar.TokenListe(ctx); len(l) != 2 {
		t.Errorf("Zeile bei ungestellter Uhr entfernt: %d Zugänge, erwartet 2", len(l))
	}
	// Gegenstück: mit gestellter Uhr ist derselbe Zugang noch in der Übergangszeit → 409, nicht 503.
	uhr.Gestellt = func() (bool, error) { return true, nil }
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+alt.ID+"/zeile", admin, nil); code != http.StatusConflict {
		t.Errorf("gleicher Zugang bei gestellter Uhr: %d %s, erwartet 409", code, raw)
	}
}

// F-2329 (#861): Datenbankfehler mitten im Aufräumen → 500, und die Zeilen,
// die davor entfernt wurden, sind weder wieder da noch ohne Audit: der Handler
// schreibt das Audit für `weg` auch im Fehlerfall. Teilzustand (keine
// Klammer-Transaktion) ist so gewollt: die Zeile vor dem Fehler bleibt weg.
func TestBoardpaarAdmin861_AufraeumenDBFehler500(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	const admin = "admin@firma.de"
	ids := map[string]string{}
	for i, n := range []string{"eins", "boese", "drei"} {
		_, z, err := e.paar.TokenAnlegen(ctx, storage.BoardpaarTypSicherungen, n, "admin")
		if err != nil {
			t.Fatal(err)
		}
		_ = e.paar.TokenWiderrufen(ctx, z.ID)
		if _, err := e.db.Exec(`UPDATE boardpaar_tokens SET created_at = ? WHERE id = ?`,
			"2026-01-0"+string(rune('1'+i))+"T00:00:00Z", z.ID); err != nil {
			t.Fatal(err)
		}
		ids[n] = z.ID
	}
	if _, err := e.db.Exec(`CREATE TRIGGER boese_nicht_loeschen BEFORE DELETE ON boardpaar_tokens
		WHEN OLD.name = 'boese' BEGIN SELECT RAISE(ABORT, 'boese'); END`); err != nil {
		t.Fatal(err)
	}
	code, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge/aufraeumen", admin, nil)
	if code != http.StatusInternalServerError {
		t.Fatalf("Aufräumen bei DB-Fehler: %d %s, erwartet 500", code, raw)
	}
	liste, _ := e.paar.TokenListe(ctx)
	rest := map[string]bool{}
	for _, z := range liste {
		rest[z.ID] = true
	}
	if rest[ids["eins"]] || !rest[ids["boese"]] || !rest[ids["drei"]] || len(liste) != 2 {
		t.Errorf("Teilzustand unerwartet: %+v", liste)
	}
	audit, _ := e.cfg.Audit(ctx, "", 50)
	n := 0
	for _, a := range audit {
		if a.Action == storage.AuditActionBoardpaarZugangEntfernen {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d Audit-Einträge, erwartet 1 (für die vor dem Fehler entfernte Zeile)", n)
	}
}
