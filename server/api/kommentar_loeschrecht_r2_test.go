package api

// Sprint-82-Abschluss, Runde 2 (Kritiker k3): Umgehungen des Löschrechts am
// Ergebnis über doppelte Karten-ids (KF-1), eine unter neuer id nachgebaute
// Karte (KF-2) und die Kopie-Ausnahme an bestehenden Karten (KF-4).

import (
	"strings"
	"testing"
)

const r2KA = `{"id":"k1","author":"A","authorEmail":"a@x.de","text":"beleg","created":"2026-10-01"}`
const r2KB = `{"id":"k2","author":"B","authorEmail":"b@x.de","text":"mein","created":"2026-10-01"}`
const r2Verlauf = `[{"id":"e1","author":"A","action":"comment-deleted-by-owner","from":"x@x.de","timestamp":"2026-10-01T00:00:00Z"}]`

func r2Stand(sprints ...string) []byte {
	return []byte(`{"version":"2","board":{"id":"b","title":"t","sprints":[` + strings.Join(sprints, ",") + `]}}`)
}

func r2Karte(id string, displayID string, kommentare, verlauf string) string {
	return `{"id":"` + id + `","title":"x","displayId":` + displayID + `,"columnId":"col-backlog","comments":[` + kommentare + `],"activity":` + verlauf + `}`
}

func r2Arten(v []kommentarVerstoss) string {
	var a []string
	for _, x := range v {
		a = append(a, x.art)
	}
	return strings.Join(a, ",")
}

var r2Editor = kommentarSchreiber{email: "b@x.de", darfFremde: false}

func TestR2_KF1_DoppelteKartenIdIstVerstoss(t *testing.T) {
	vor := r2Stand(`{"id":"s","cards":[` + r2Karte("c1", "1", r2KA+","+r2KB, r2Verlauf) + `]}`)
	nach := r2Stand(`{"id":"s","cards":[`+r2Karte("c1", "1", r2KB, `[]`)+`]}`,
		`{"id":"s2","archived":true,"cards":[`+r2Karte("c1", "1", r2KA+","+r2KB, r2Verlauf)+`]}`)
	v, _ := vergleicheKommentare(vor, nach, r2Editor, "b")
	if !strings.Contains(r2Arten(v), "karte-doppelt") {
		t.Fatalf("Geisterkopie mit gleicher id nicht abgelehnt: %v", v)
	}
	// Auch Owner/Admin: eine neue Doppelung ist nie legitim (Verlaufsschutz gilt für alle).
	v, _ = vergleicheKommentare(vor, nach, kommentarSchreiber{email: "o@x.de", darfFremde: true}, "b")
	if !strings.Contains(r2Arten(v), "karte-doppelt") {
		t.Fatalf("Owner: Geisterkopie nicht abgelehnt: %v", v)
	}
}

func TestR2_KF1_VorbestehendeDoppelungBleibtZulaessig(t *testing.T) {
	k := r2Karte("c1", "1", r2KA+","+r2KB, r2Verlauf)
	vor := r2Stand(`{"id":"s","cards":[`+k+`]}`, `{"id":"s2","cards":[`+k+`]}`)
	nach := r2Stand(`{"id":"s","cards":[`+k+`]}`, `{"id":"s2","cards":[`+k+`]}`)
	if v, _ := vergleicheKommentare(vor, nach, r2Editor, "b"); len(v) != 0 {
		t.Fatalf("unveränderter Altbestand mit doppelter id abgelehnt: %v", v)
	}
}

func TestR2_KF2_NachbauUnterNeuerIdIstVerstoss(t *testing.T) {
	vor := r2Stand(`{"id":"s","cards":[` + r2Karte("c1", "1", r2KA+","+r2KB, r2Verlauf) + `]}`)
	nach := r2Stand(`{"id":"s","cards":[` + r2Karte("c1neu", "1", r2KB, `[]`) + `]}`)
	v, _ := vergleicheKommentare(vor, nach, r2Editor, "b")
	if !strings.Contains(r2Arten(v), "fremder-kommentar") {
		t.Fatalf("Nachbau unter neuer id mit gleicher displayId nicht abgelehnt: %v", v)
	}
	// Karte wirklich löschen bleibt erlaubt (ADR-0136 §Grenzen).
	if v, _ := vergleicheKommentare(vor, r2Stand(`{"id":"s","cards":[]}`), r2Editor, "b"); len(v) != 0 {
		t.Fatalf("Löschen der Karte abgelehnt: %v", v)
	}
}

func TestR2_KF4_KopieNurAnNeuerKarte(t *testing.T) {
	c1 := r2Karte("c1", "1", r2KA, `[]`)
	vor := r2Stand(`{"id":"s","cards":[` + c1 + `,` + r2Karte("c2", "2", "", `[]`) + `]}`)
	// Fremder Kommentar byte-gleich an eine BESTEHENDE andere Karte kopiert.
	nach := r2Stand(`{"id":"s","cards":[` + c1 + `,` + r2Karte("c2", "2", r2KA, `[]`) + `]}`)
	v, _ := vergleicheKommentare(vor, nach, r2Editor, "b")
	if !strings.Contains(r2Arten(v), "fremder-kommentar") {
		t.Fatalf("Kopie an bestehende Karte nicht abgelehnt: %v", v)
	}
	// Karte kopieren (neue Karte, neue displayId) bleibt erlaubt.
	nach = r2Stand(`{"id":"s","cards":[` + c1 + `,` + r2Karte("c2", "2", "", `[]`) + `,` + r2Karte("c3", "3", r2KA, `[]`) + `]}`)
	if v, _ := vergleicheKommentare(vor, nach, r2Editor, "b"); len(v) != 0 {
		t.Fatalf("Karte kopieren abgelehnt: %v", v)
	}
}

// VR2-3 (Verify Runde 2): Ein Nicht-Objekt in attachments (Legacy-Anhang-id
// als String) schaltet den Schutz der übrigen Einträge nicht ab.
func TestR2_VR23_StringElementSchaltetDateischutzNichtAb(t *testing.T) {
	karte := func(anh string) string {
		return `{"id":"c1","title":"x","displayId":1,"columnId":"col-backlog","comments":[` + r2KA + `],"attachments":[` + anh + `],"activity":[]}`
	}
	vor := r2Stand(`{"id":"s","cards":[` + karte(`"x",{"id":"a1","commentId":"k1"}`) + `]}`)
	nach := r2Stand(`{"id":"s","cards":[` + karte(`"x"`) + `]}`)
	v, _ := vergleicheKommentare(vor, nach, r2Editor, "b")
	if !strings.Contains(r2Arten(v), "fremder-kommentar") {
		t.Fatalf("Datei des fremden Kommentars trotz String-Element entfernbar: %v", v)
	}
}

// VR2-2: Teilen sich mehrere verschwundene Karten eine displayId, wird nicht
// zugeordnet — kein Fehlalarm gegen eine Karte, die nichts damit zu tun hat.
func TestR2_VR22_MehrdeutigeDisplayIdOhneZuordnung(t *testing.T) {
	// Reihenfolge: die Karte mit fremdem Kommentar zuletzt — ohne Schutz gewänne
	// sie die Zuordnung und meldete einen Verstoß gegen die unbeteiligte c3.
	vor := r2Stand(`{"id":"s","cards":[` + r2Karte("c2", "1", "", `[]`) + `,` + r2Karte("c1", "1", r2KA, r2Verlauf) + `]}`)
	nach := r2Stand(`{"id":"s","cards":[` + r2Karte("c3", "1", r2KB, `[]`) + `]}`)
	if v, _ := vergleicheKommentare(vor, nach, kommentarSchreiber{email: "b@x.de"}, "b"); len(v) != 0 {
		t.Fatalf("Mehrdeutige displayId falsch zugeordnet: %v", v)
	}
}

// VR2-8b: Die PUT-Meldung nennt bei doppelter Karten-id den richtigen Grund.
func TestR2_VR28_MeldungKarteDoppelt(t *testing.T) {
	m := kommentarVerstossMeldung([]kommentarVerstoss{{art: "karte-doppelt", cardID: "c1"}})
	if !strings.Contains(m, "duplicate card") || strings.Contains(m, "comments of other people") {
		t.Fatalf("Meldung: %q", m)
	}
}

// VR3-2: anhangIDsDerKarte liest Element für Element und kennt Legacy-ids als
// String — sonst blieben beim Teil-Leeren alle Dateien liegen.
func TestR2_VR32_AnhangIDsMitLegacyString(t *testing.T) {
	st := r2Stand(`{"id":"s","cards":[{"id":"c1","title":"x","displayId":1,"columnId":"col-backlog","attachments":["alt1",{"id":"a1","commentId":"k1"},null]}]}`)
	got := strings.Join(anhangIDsDerKarte(st, "c1"), ",")
	if got != "alt1,a1" {
		t.Fatalf("anhangIDsDerKarte: %q, erwartet alt1,a1", got)
	}
}
