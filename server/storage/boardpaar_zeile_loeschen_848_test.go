package storage

// Karte #848: abgeschlossene Board-Paar-Zugänge aus der Tabelle entfernen.
// Abnahme laut Karte: nur abgeschlossene Zeilen (widerrufen, oder abgelöst UND
// Übergangszeit vorbei), sonst Ablehnung; das aktuelle Token und seine Kette
// funktionieren danach unverändert (Prüfen, Erneuern, Wiederholung der
// Erneuerung); Aufräumen in einem Zug.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func uhrFuerTest(t *testing.T, gestellt bool) {
	t.Helper()
	alt := uhrGestellt
	uhrGestellt = func() bool { return gestellt }
	t.Cleanup(func() { uhrGestellt = alt })
}

func zeileDa(t *testing.T, r *BoardpaarRepo, id string) bool {
	t.Helper()
	liste, err := r.TokenListe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, z := range liste {
		if z.ID == id {
			return true
		}
	}
	return false
}

func TestBoardpaar848_NurAbgeschlosseneZeilen(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	uhrFuerTest(t, true)

	// Aktiv: bleibt.
	_, aktiv, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "aktiv", "admin")
	if _, err := r.ZugangZeileEntfernen(ctx, aktiv.ID); !errors.Is(err, ErrBoardpaarZugangGiltNoch) {
		t.Errorf("aktiver Zugang: err = %v, erwartet ErrBoardpaarZugangGiltNoch", err)
	}
	if !zeileDa(t, r, aktiv.ID) {
		t.Error("aktiver Zugang ist weg")
	}

	// Widerrufen: geht weg, ein zweites Mal ist er unbekannt.
	_, w, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "widerrufen", "admin")
	if err := r.TokenWiderrufen(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	z, err := r.ZugangZeileEntfernen(ctx, w.ID)
	if err != nil || z.Typ != BoardpaarTypAusgang || z.Name != "widerrufen" {
		t.Fatalf("widerrufener Zugang: %+v, %v", z, err)
	}
	if zeileDa(t, r, w.ID) {
		t.Error("widerrufener Zugang steht noch in der Tabelle")
	}
	if _, err := r.ZugangZeileEntfernen(ctx, w.ID); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("zweites Entfernen: err = %v, erwartet ErrBoardpaarTokenUnbekannt", err)
	}

	// Erneuert, in der Übergangszeit: bleibt, und die Wiederholung der
	// Erneuerung funktioniert danach noch.
	a, alt, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	e, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ZugangZeileEntfernen(ctx, alt.ID); !errors.Is(err, ErrBoardpaarZugangGiltNoch) {
		t.Errorf("Vorgänger in der Übergangszeit: err = %v, erwartet ErrBoardpaarZugangGiltNoch", err)
	}
	if w2, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "192.0.2.1"); err != nil || w2.Token != e.Token || !w2.Wiederholt {
		t.Errorf("Wiederholung nach abgelehntem Entfernen: %+v, %v", w2, err)
	}

	// Nach der Übergangszeit: der Vorgänger geht weg; das aktuelle Token gilt
	// weiter und lässt sich weiter erneuern.
	vor(BoardpaarUebergangszeit + time.Minute)
	if _, err := r.ZugangZeileEntfernen(ctx, alt.ID); err != nil {
		t.Fatalf("abgelöster Vorgänger nach der Übergangszeit: %v", err)
	}
	if _, err := r.ZugangZeileEntfernen(ctx, e.Zugang.ID); !errors.Is(err, ErrBoardpaarZugangGiltNoch) {
		t.Errorf("aktueller Nachfolger: err = %v, erwartet ErrBoardpaarZugangGiltNoch", err)
	}
	pruefeGilt(t, r, BoardpaarTypAusgang, e.Token, "aktuelles Token nach dem Entfernen des Vorgängers", true)
	pruefeGilt(t, r, BoardpaarTypAusgang, a, "altes Token nach dem Entfernen seiner Zeile", false)
	e2, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, e.Token, "192.0.2.1")
	if err != nil {
		t.Fatalf("Erneuern nach dem Entfernen des Vorgängers: %v", err)
	}
	if w3, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, e.Token, "192.0.2.1"); err != nil || w3.Token != e2.Token {
		t.Errorf("Wiederholung dieser Erneuerung: %+v, %v", w3, err)
	}
	// Die Liste trägt den Nachfolger weiter, mit Herkunft auf eine ID, die es
	// nicht mehr gibt — das ist erlaubt (die Anzeige zeigt dann nur Zeit/Adresse).
	if !zeileDa(t, r, e.Zugang.ID) || !zeileDa(t, r, e2.Zugang.ID) {
		t.Error("Kette nach dem Entfernen unvollständig")
	}

	if _, err := r.ZugangZeileEntfernen(ctx, "gibt-es-nicht"); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("unbekannte ID: err = %v", err)
	}
}

// Ungestellte Uhr: eine Frist lässt sich nicht beurteilen — abgelöste Zeilen
// bleiben (ErrBoardpaarUhr). Widerrufene hängen an keiner Frist und gehen.
func TestBoardpaar848_UngestellteUhr(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	uhrFuerTest(t, true)
	a, alt, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, ""); err != nil {
		t.Fatal(err)
	}
	_, w, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "weg", "admin")
	_ = r.TokenWiderrufen(ctx, w.ID)
	vor(BoardpaarUebergangszeit + time.Hour)

	uhrFuerTest(t, false)
	if _, err := r.ZugangZeileEntfernen(ctx, alt.ID); !errors.Is(err, ErrBoardpaarUhr) {
		t.Errorf("abgelöst bei ungestellter Uhr: err = %v, erwartet ErrBoardpaarUhr", err)
	}
	if !zeileDa(t, r, alt.ID) {
		t.Error("abgelöste Zeile bei ungestellter Uhr entfernt")
	}
	if _, err := r.ZugangZeileEntfernen(ctx, w.ID); err != nil {
		t.Errorf("widerrufen bei ungestellter Uhr: %v", err)
	}
	weg, uebersprungen, err := r.ZugaengeAufraeumen(ctx)
	if err != nil || len(weg) != 0 || uebersprungen != 1 {
		t.Errorf("Aufräumen bei ungestellter Uhr: weg %v, übersprungen %d, %v", weg, uebersprungen, err)
	}
}

// F-2324: Ein unlesbares gilt_bis gilt beim Entfernen als „gilt noch“ —
// Behalten ist die vorsichtige Richtung; widerrufen lässt sich die Zeile
// danach entfernen.
func TestBoardpaar848_UnlesbaresGiltBis(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	uhrFuerTest(t, true)
	_, z, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "kaputt", "admin")
	if _, err := r.db.Exec(`UPDATE boardpaar_tokens SET gilt_bis = 'kein-datum' WHERE id = ?`, z.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ZugangZeileEntfernen(ctx, z.ID); !errors.Is(err, ErrBoardpaarZugangGiltNoch) {
		t.Errorf("unlesbares gilt_bis: err = %v, erwartet ErrBoardpaarZugangGiltNoch", err)
	}
	if !zeileDa(t, r, z.ID) {
		t.Fatal("Zeile mit unlesbarem gilt_bis entfernt")
	}
	if err := r.TokenWiderrufen(ctx, z.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ZugangZeileEntfernen(ctx, z.ID); err != nil {
		t.Errorf("nach dem Widerruf: %v", err)
	}
}

// Ändert sich die Zeile zwischen Lesen und Löschen, trifft das DELETE nichts
// und die zweite Runde liest den neuen Stand (hier: die Frist wieder in der
// Zukunft, wie es ohne Uhrfehler nie vorkommt — nur so lässt sich die Bedingung
// im DELETE allein prüfen).
func TestBoardpaar848_AenderungZwischenLesenUndLoeschen(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	uhrFuerTest(t, true)
	a, alt, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, ""); err != nil {
		t.Fatal(err)
	}
	vor(BoardpaarUebergangszeit + time.Minute)
	altHaken := zeileNachLesen
	t.Cleanup(func() { zeileNachLesen = altHaken })
	einmal := false
	zeileNachLesen = func(id string) {
		if einmal {
			return
		}
		einmal = true
		spaeter := timeNow().Add(time.Hour).UTC().Format(giltBisFormat)
		if _, err := r.db.Exec(`UPDATE boardpaar_tokens SET gilt_bis = ? WHERE id = ?`, spaeter, id); err != nil {
			t.Error(err)
		}
	}
	if _, err := r.ZugangZeileEntfernen(ctx, alt.ID); !errors.Is(err, ErrBoardpaarZugangGiltNoch) {
		t.Errorf("err = %v, erwartet ErrBoardpaarZugangGiltNoch", err)
	}
	if !zeileDa(t, r, alt.ID) {
		t.Error("Zeile trotz geänderter Frist gelöscht")
	}
}

func TestBoardpaar848_Aufraeumen(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	uhrFuerTest(t, true)
	_, aktiv, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "aktiv", "admin")
	_, w, _ := r.TokenAnlegen(ctx, BoardpaarTypRueckweg, "alt", "admin")
	_ = r.TokenWiderrufen(ctx, w.ID)
	a, alt, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	e, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	vor(BoardpaarUebergangszeit + time.Minute)
	// Ein frisch erneuerter, noch in seiner Übergangszeit: bleibt.
	b, frisch, _ := r.TokenAnlegen(ctx, BoardpaarTypSicherungen, "abb", "admin")
	if _, err := r.TokenErneuern(ctx, BoardpaarTypSicherungen, b, ""); err != nil {
		t.Fatal(err)
	}

	weg, uebersprungen, err := r.ZugaengeAufraeumen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, z := range weg {
		ids[z.ID] = true
	}
	if len(weg) != 2 || !ids[w.ID] || !ids[alt.ID] || uebersprungen != 1 {
		t.Errorf("entfernt %v (erwartet %s, %s), übersprungen %d (erwartet 1)", weg, w.ID, alt.ID, uebersprungen)
	}
	for _, id := range []string{aktiv.ID, e.Zugang.ID, frisch.ID} {
		if !zeileDa(t, r, id) {
			t.Errorf("geltender Zugang %s entfernt", id)
		}
	}
	pruefeGilt(t, r, BoardpaarTypAusgang, e.Token, "aktuelles Token nach dem Aufräumen", true)
	pruefeGilt(t, r, BoardpaarTypSicherungen, b, "abb in der Übergangszeit nach dem Aufräumen", true)
}

// Wissen §50 / F-2321: Prüfen und Löschen in einem Statement. 30 Läufe
// entfernen je eine eigene widerrufene Zeile, während vier Schreiber laufend
// committen — jeder Lauf muss gelingen, ohne „database is locked“. (Auf
// dieselbe Zeile gezielt, erreichten die meisten Läufe das DELETE gar nicht —
// der Test trennte dann nicht zwischen einem und zwei Statements.)
func TestBoardpaar848_ParallelNebenSchreibern(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	uhrFuerTest(t, true)
	const n = 30
	ids := make([]string, n)
	for i := range ids {
		_, z, err := r.TokenAnlegen(ctx, BoardpaarTypEingang, fmt.Sprintf("weg-%02d", i), "admin")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.TokenWiderrufen(ctx, z.ID); err != nil {
			t.Fatal(err)
		}
		ids[i] = z.ID
	}
	_, laerm, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "laerm", "admin")
	stopp := make(chan struct{})
	var schreiber sync.WaitGroup
	var geschrieben atomic.Int64
	for s := 0; s < 4; s++ {
		schreiber.Add(1)
		go func() {
			defer schreiber.Done()
			for k := 0; ; k++ {
				select {
				case <-stopp:
					return
				default:
				}
				if _, err := r.db.ExecContext(ctx, `UPDATE boardpaar_tokens SET last_used_at = ? WHERE id = ?`, fmt.Sprint(k), laerm.ID); err == nil {
					geschrieben.Add(1)
				}
			}
		}()
	}
	var wg sync.WaitGroup
	fehler := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, fehler[i] = r.ZugangZeileEntfernen(ctx, ids[i])
		}(i)
	}
	wg.Wait()
	close(stopp)
	schreiber.Wait()
	if geschrieben.Load() == 0 {
		t.Fatal("die Schreiber haben nichts geschrieben — der Test prüft dann nichts")
	}
	for i, err := range fehler {
		if err != nil {
			t.Errorf("Lauf %d: %v", i, err)
		}
	}
	for _, id := range ids {
		if zeileDa(t, r, id) {
			t.Errorf("Zeile %s nach dem Entfernen noch da", id)
		}
	}
}

// F-2329 (#861): Abbruch von ZugaengeAufraeumen bei einem Datenbankfehler.
// Der Fehler kommt aus einem Trigger in der eigenen Test-DB, der das DELETE
// einer bestimmten Zeile abbricht. Es gibt KEINE Klammer-Transaktion: jede
// Zeile ist ein eigenes Statement, also bleibt das, was VOR dem Fehler
// entfernt wurde, entfernt (so steht es im Funktionskommentar), und die
// Rückgabe nennt genau diese Zeilen. Dahinter liegende Kandidaten werden
// nicht mehr angefasst.
func TestBoardpaar861_AufraeumenAbbruchBeiDBFehler(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	uhrFuerTest(t, true)
	ids := map[string]string{}
	for i, n := range []string{"eins", "boese", "drei"} {
		_, z, err := r.TokenAnlegen(ctx, BoardpaarTypSicherungen, n, "admin")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.TokenWiderrufen(ctx, z.ID); err != nil {
			t.Fatal(err)
		}
		// Reihenfolge des Aufräumens (ORDER BY created_at) festnageln.
		if _, err := r.db.Exec(`UPDATE boardpaar_tokens SET created_at = ? WHERE id = ?`,
			fmt.Sprintf("2026-01-0%dT00:00:00Z", i+1), z.ID); err != nil {
			t.Fatal(err)
		}
		ids[n] = z.ID
	}
	if _, err := r.db.Exec(`CREATE TRIGGER boese_nicht_loeschen BEFORE DELETE ON boardpaar_tokens
		WHEN OLD.name = 'boese' BEGIN SELECT RAISE(ABORT, 'boese'); END`); err != nil {
		t.Fatal(err)
	}

	weg, uebersprungen, err := r.ZugaengeAufraeumen(ctx)
	// (a) Der Fehler wird gemeldet, nicht als „übersprungen“ verschluckt.
	if err == nil || errors.Is(err, ErrBoardpaarZugangGiltNoch) {
		t.Fatalf("Fehler = %v, erwartet der Datenbankfehler", err)
	}
	if uebersprungen != 0 {
		t.Errorf("übersprungen = %d, erwartet 0", uebersprungen)
	}
	// (b) Teilzustand: die Zeile vor dem Fehler ist weg und steht in der
	// Rückgabe (der Handler schreibt dafür noch Audit); die fehlerhafte und die
	// dahinter bleiben stehen.
	if len(weg) != 1 || weg[0].ID != ids["eins"] {
		t.Errorf("entfernt = %+v, erwartet nur %s", weg, ids["eins"])
	}
	if zeileDa(t, r, ids["eins"]) {
		t.Error("Zeile vor dem Fehler steht noch (Rückgabe und Tabelle widersprechen sich)")
	}
	for _, n := range []string{"boese", "drei"} {
		if !zeileDa(t, r, ids[n]) {
			t.Errorf("Zeile %q fehlt, obwohl der Lauf davor abgebrochen ist", n)
		}
	}
}
