package api_test

// F-2300 / F-2304 (Karte #841): Der Rückweg-Schalter einer Kopie
// (boardpaar.rueckweg = an UND bearbeitbar = 1) wird vor der Sperre gelesen.
// Wird er zwischen diesem Lesen und dem COMMIT abgeschaltet, gilt für den
// Batch die Regel von 2b — der Schreibvorgang liest ihn unter der Sperre
// noch einmal. Dazu der fail-closed-Zweig am API-Weg: ein unlesbarer
// Board-Schalter ist 503, nichts geschrieben; ist der Server-Schalter aus,
// wird der Board-Schalter gar nicht erst gelesen.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// titelGeschrieben2300: Steht der Wert im gespeicherten Board?
func titelGeschrieben2300(t *testing.T, env *boardsTestEnv, wert string) bool {
	t.Helper()
	b, err := storage.NewBoardRepo(env.db).Get(context.Background(), kopie826)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b.StateJSON), wert)
}

// Board-Schalter wird zwischen Lesen und Sperre abgeschaltet: abgelehnt wie
// in 2b, kein Journal, nichts geschrieben.
func TestSchalter2300_BearbeitbarAbgeschaltetVorDerSperre(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, true)
	defer api.SetzeOpsNachSchalterNahtFuerTest(func() {
		if err := storage.NewBoardpaarRepo(env.db).KopieBearbeitbarSetzen(context.Background(), kopie826, false); err != nil {
			t.Error(err)
		}
	})()

	s, out := postOps(t, env, kopie826, feldOp836(karte, "aussen-2300"))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Feld: %d %v", s, out)
	}
	grund827(t, out, karte, "read-only on the external copy until the return path exists")
	if j := journal827(t, env); len(j) != 0 {
		t.Errorf("Journal nach Abschalten: %+v", j)
	}
	if titelGeschrieben2300(t, env, "aussen-2300") {
		t.Error("Titel trotz Abschalten geschrieben")
	}
}

// Server-Schalter wird zwischen Lesen und Sperre abgeschaltet: ebenso.
func TestSchalter2300_ServerSchalterAbgeschaltetVorDerSperre(t *testing.T) {
	env, cfg := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, true)
	defer api.SetzeOpsNachSchalterNahtFuerTest(func() {
		if err := cfg.Set(context.Background(), api.KonfigBoardpaarRueckweg, "aus", false, "test"); err != nil {
			t.Error(err)
		}
	})()

	s, out := postOps(t, env, kopie826, feldOp836(karte, "aussen-2300"))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Feld: %d %v", s, out)
	}
	grund827(t, out, karte, "read-only on the external copy until the return path exists")
	if j := journal827(t, env); len(j) != 0 {
		t.Errorf("Journal nach Abschalten: %+v", j)
	}
}

// Abschalten vor der Sperre trifft nur Spiegel: eine Änderung außerhalb der
// Spiegel geht weiter durch, ohne Journal (Regel von 2b, nicht „alles zu“).
func TestSchalter2300_AbgeschaltetEigeneKarteGehtDurch(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	legeKopie836An(t, env, kopie826, true)
	defer api.SetzeOpsNachSchalterNahtFuerTest(func() {
		if err := storage.NewBoardpaarRepo(env.db).KopieBearbeitbarSetzen(context.Background(), kopie826, false); err != nil {
			t.Error(err)
		}
	})()

	// Eine eigene Lane außen ist kein Spiegel.
	if s, out := postOps(t, env, kopie826, `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"eigen-2300","title":"Eigen","kind":"lane"}}}]`); s != http.StatusOK {
		t.Fatalf("eigene Lane: %d %v", s, out)
	}
	if j := journal827(t, env); len(j) != 0 {
		t.Errorf("Journal: %+v", j)
	}
}

// Unter der Sperre nicht lesbar: 503, nichts geschrieben. Die Spalte wird nach
// dem Lesen vor der Sperre umbenannt — kopie bleibt lesbar, bearbeitbar nicht.
func TestSchalter2300_UnterDerSperreUnlesbar(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, true)
	defer api.SetzeOpsNachSchalterNahtFuerTest(func() {
		if _, err := env.db.ExecContext(context.Background(), `ALTER TABLE boardpaar_aussen RENAME COLUMN bearbeitbar TO weg_2300`); err != nil {
			t.Error(err)
		}
	})()

	s, out := postOps(t, env, kopie826, feldOp836(karte, "aussen-2300"))
	if s != http.StatusServiceUnavailable {
		t.Fatalf("Feld: %d %v, erwartet 503", s, out)
	}
	if titelGeschrieben2300(t, env, "aussen-2300") {
		t.Error("Titel trotz Lesefehler geschrieben")
	}
}

// F-2304: Der Board-Schalter ist schon vor der Sperre unlesbar — 503 am
// API-Weg, nichts geschrieben, kein Journal.
func TestSchalter2304_BoardSchalterUnlesbarIst503(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, true)
	if _, err := env.db.ExecContext(context.Background(), `ALTER TABLE boardpaar_aussen RENAME COLUMN bearbeitbar TO weg_2304`); err != nil {
		t.Fatal(err)
	}

	s, out := postOps(t, env, kopie826, feldOp836(karte, "aussen-2304"))
	if s != http.StatusServiceUnavailable {
		t.Fatalf("Feld: %d %v, erwartet 503", s, out)
	}
	if titelGeschrieben2300(t, env, "aussen-2304") {
		t.Error("Titel trotz Lesefehler geschrieben")
	}
	if j := journal827(t, env); len(j) != 0 {
		t.Errorf("Journal: %+v", j)
	}
}

// F-2304: Server-Schalter aus — der Board-Schalter wird nicht gelesen. Trotz
// unlesbarer Spalte kein 503, sondern die Ablehnung aus 2b.
func TestSchalter2304_ServerAusLiestBoardSchalterNicht(t *testing.T) {
	env, _ := newKopieEnv827(t, "")
	karte := legeKopie836An(t, env, kopie826, true)
	if _, err := env.db.ExecContext(context.Background(), `ALTER TABLE boardpaar_aussen RENAME COLUMN bearbeitbar TO weg_2304`); err != nil {
		t.Fatal(err)
	}

	s, out := postOps(t, env, kopie826, feldOp836(karte, "aussen-2304"))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Feld: %d %v, erwartet 422 (Board-Schalter ungelesen)", s, out)
	}
	grund827(t, out, karte, "read-only on the external copy until the return path exists")
}
