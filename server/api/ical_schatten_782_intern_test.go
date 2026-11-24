package api

// Karte #782: schattenEvent entscheidet selbst, dass ein Schatten ohne Datum
// kein Eintrag ist. Im fertigen ICS fiele er auch ohne diese Prüfung weg
// (calendar.BuildICS lässt VEVENTs ohne Startdatum aus) — ein Feed-Test kann
// die Prüfung deshalb nicht trennen, dieser hier schon.

import (
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSchattenEvent782_OhneDatumKeinEintrag(t *testing.T) {
	titel, nr := "Ohne Datum", 5
	z := storage.SchattenZeile{Eingang: schatten.Eingang{SchattenID: "x", Art: schatten.ArtKarte, Titel: &titel, Nr: &nr}}
	if _, ok := schattenEvent(z, "https://innen.example/#board=b", "h"); ok {
		t.Fatal("Schatten ohne Datum wurde ein Eintrag")
	}
	d := "2026-10-21"
	z.EndeDatum = &d
	ev, ok := schattenEvent(z, "https://innen.example/#board=b", "h")
	if !ok || ev.Start != d || ev.End != d {
		t.Fatalf("nur Ende-Datum: ok=%v start=%q end=%q", ok, ev.Start, ev.End)
	}
}

// Sitzungs-QA C-2: Feed und Werkzeuge benennen einen Schatten nach derselben
// Regel (schatten.AnzeigeTitel). Ein leerer oder nur aus Leerraum bestehender
// Titel hieß im Feed früher leer (mit Nummer: „i-5 “), in den Werkzeugen schon
// „Interne Karte“.
func TestSchattenTitel782_FeedUndWerkzeugGleich(t *testing.T) {
	d := "2026-10-21"
	nr := 5
	for _, titel := range []*string{nil, new(string), ptrStr(" \t "), ptrStr("Echt")} {
		z := storage.SchattenZeile{Eingang: schatten.Eingang{SchattenID: "x", Art: schatten.ArtKarte, Titel: titel, Nr: &nr, StartDatum: &d}}
		ev, ok := schattenEvent(z, "https://innen.example/#board=b", "h")
		if !ok {
			t.Fatal("kein Eintrag")
		}
		wk := baueWkSchatten(z).Titel
		want := wk
		if titel != nil && *titel == "Echt" {
			want = "i-5 " + wk
		}
		if ev.Summary != want {
			t.Errorf("Titel %v: Feed %q, Werkzeug %q", titel, ev.Summary, wk)
		}
		if titel != nil && *titel != "Echt" && (ev.Summary != schatten.InterneKarteTitel || wk != schatten.InterneKarteTitel) {
			t.Errorf("Titel %q: Feed %q / Werkzeug %q, erwartet %q", *titel, ev.Summary, wk, schatten.InterneKarteTitel)
		}
	}
}

func ptrStr(s string) *string { return &s }
