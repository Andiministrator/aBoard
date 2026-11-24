package schatten

// Karte #809 (F-2017, T16): gemeinsame Testvektoren der Titelbehandlung.
// Die Datei server/testdata/schatten/t16-vektoren.json ist ein ENTWURF von
// aBoard; das Partnerprojekt übernimmt sie, danach ist deren Fassung führend. Das
// Push-Skript prüft dieselben Vektoren ohne Kenntnis von aBoard — Aufbau und
// Auslegung stehen im Feld "hinweis" der Datei.
//
// Der Test ist streng: unbekannte Schlüssel, unbekannte "quelle", fehlende
// Felder oder ein Vektor mit "erwartet" UND "ablehnung" lassen ihn scheitern.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

type t16Vektor struct {
	Name   string `json:"name"`
	Quelle string `json:"quelle"`
	// Feld: welches Feld der Vektor prüft ("titel" | "spalte" | "bearbeiter");
	// fehlt es, ist es "titel" (Absprache Board-Paar 11.10.2026, Ziffer 5).
	Feld      string                     `json:"feld"`
	Eingabe   map[string]json.RawMessage `json:"eingabe"`
	Erwartet  json.RawMessage            `json:"erwartet"`
	Ablehnung *string                    `json:"ablehnung"`
}

type t16Herkunft struct {
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
}

type t16Datei struct {
	Stand      string      `json:"stand"`
	Herkunft   t16Herkunft `json:"herkunft"`
	Hinweis    string      `json:"hinweis"`
	Quellen    []string    `json:"quellen"`
	Ersatztext string      `json:"ersatztext"`
	Vektoren   []t16Vektor `json:"vektoren"`
}

var t16CommitMuster = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// pruefeHerkunft: repo ist Pflicht. Solange stand mit "Entwurf" beginnt, darf
// commit ein Platzhalter sein; sonst muss er ein Commit-Kürzel sein (V-5).
func pruefeHerkunft(d t16Datei) error {
	if d.Herkunft.Repo == "" || d.Herkunft.Commit == "" {
		return errors.New("kopf: herkunft braucht repo und commit")
	}
	if strings.HasPrefix(d.Stand, "Entwurf") {
		return nil
	}
	if !t16CommitMuster.MatchString(d.Herkunft.Commit) {
		return fmt.Errorf("kopf: herkunft.commit %q ist kein Commit-Kürzel (stand ist kein Entwurf mehr)", d.Herkunft.Commit)
	}
	return nil
}

func TestT16_Herkunft(t *testing.T) {
	entwurf := t16Datei{Stand: "Entwurf aBoard", Herkunft: t16Herkunft{Repo: "aBoard", Commit: "Platzhalter, später gesetzt"}}
	if err := pruefeHerkunft(entwurf); err != nil {
		t.Errorf("Entwurf mit Platzhalter muss erlaubt sein: %v", err)
	}
	fertig := entwurf
	fertig.Stand = "Stand Partnerprojekt 2026-10-12"
	if err := pruefeHerkunft(fertig); err == nil {
		t.Error("kein Entwurf mit Platzhalter-Commit muss abgelehnt werden")
	}
	fertig.Herkunft.Commit = "96d8fc01"
	if err := pruefeHerkunft(fertig); err != nil {
		t.Errorf("Commit-Kürzel muss gelten: %v", err)
	}
	fertig.Herkunft.Commit = "96D8FC01"
	if err := pruefeHerkunft(fertig); err == nil {
		t.Error("Großbuchstaben sind kein Commit-Kürzel")
	}
	fertig.Herkunft.Commit = "abc"
	if err := pruefeHerkunft(fertig); err == nil {
		t.Error("zu kurz")
	}
	leer := entwurf
	leer.Herkunft.Repo = ""
	if err := pruefeHerkunft(leer); err == nil {
		t.Error("repo ist Pflicht")
	}
}

func ladeT16(t *testing.T) t16Datei {
	t.Helper()
	roh, err := os.ReadFile("../testdata/schatten/t16-vektoren.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(roh))
	dec.DisallowUnknownFields()
	var d t16Datei
	if err := dec.Decode(&d); err != nil {
		t.Fatalf("t16-vektoren.json: %v", err)
	}
	return d
}

// t16Huelle baut einen Karten-Schatten mit gegebenem titel und vertraulich
// (rohe JSON-Werte) im Wire-Format des Eingangs; spalte "" und bearbeiter [].
func t16Huelle(titel, vertraulich string) []byte {
	return t16HuelleFeld(titel, vertraulich, `""`, `[]`)
}

// t16HuelleFeld: dasselbe mit rohen JSON-Werten für spalte und bearbeiter.
func t16HuelleFeld(titel, vertraulich, spalte, bearbeiter string) []byte {
	return []byte(fmt.Sprintf(`{"version":"1.0.0","schatten":[{"schattenId":%q,"art":"karte","nr":1,"titel":%s,`+
		`"vertraulich":%s,"spalte":%s,"prioritaet":"","startDatum":null,"startZeit":null,"endeDatum":null,`+
		`"endeZeit":null,"bearbeiter":%s,"vonAgent":false}]}`, sid(1), titel, vertraulich, spalte, bearbeiter))
}

// t16BaueSpalte lässt die Rohfassung einer Spalte durch den echten Ausgang
// laufen (Baue): eine Karte in einer Spalte dieses Namens.
func t16BaueSpalte(t *testing.T, roh string) string {
	t.Helper()
	stand, _ := json.Marshal(map[string]any{"board": map[string]any{
		"columnDefs": []any{map[string]any{"id": "c1", "title": roh}},
		"sprints": []any{map[string]any{"id": "s1", "cards": []any{
			map[string]any{"id": "k1", "title": "x", "columnId": "c1"}}}},
	}})
	erg, err := Baue(stand)
	if err != nil || len(erg.Eintraege) != 1 || erg.Eintraege[0].Spalte == nil {
		t.Fatalf("Baue: %v %v", err, erg)
	}
	return *erg.Eintraege[0].Spalte
}

// t16BaueBearbeiter lässt eine rohe Namensliste durch den echten Ausgang
// laufen: Jeder nicht leere Name steht als Label in board.identities (so
// ist er „dem Board bekannt“), die Karte trägt die Liste als assignees.
func t16BaueBearbeiter(t *testing.T, roh []string) []string {
	t.Helper()
	ids := []any{}
	for _, n := range roh {
		ids = append(ids, map[string]any{"label": n, "kind": "user"})
	}
	stand, _ := json.Marshal(map[string]any{"board": map[string]any{
		"identities": ids,
		"sprints": []any{map[string]any{"id": "s1", "cards": []any{
			map[string]any{"id": "k1", "title": "x", "assignees": roh}}}},
	}})
	erg, err := Baue(stand)
	if err != nil || len(erg.Eintraege) != 1 {
		t.Fatalf("Baue: %v %v", err, erg)
	}
	return erg.Eintraege[0].Bearbeiter
}

// t16Abgelehnt: LiesEingang lehnt mit 422 ab und nennt das Feld (genau ein
// Fehler, Meldung beginnt mit feld und dann ":" bzw. "[").
func t16Abgelehnt(err error, feld string) error {
	var f *EingangFehler
	if eingangStatus(err) != 422 || !errors.As(err, &f) || len(f.Fehler) != 1 {
		return fmt.Errorf("kein 422 mit genau einem Fehler: %v", err)
	}
	g := f.Fehler[0].Grund
	if !strings.HasPrefix(g, feld+":") && !strings.HasPrefix(g, feld+"[") {
		return fmt.Errorf("Meldung nennt nicht das Feld %q: %q", feld, g)
	}
	return nil
}

func TestT16_Vektoren(t *testing.T) {
	d := ladeT16(t)
	if len(d.Vektoren) == 0 {
		t.Fatal("keine Vektoren in der Datei")
	}
	if err := pruefeHerkunft(d); err != nil {
		t.Fatal(err)
	}
	if d.Ersatztext != Ersatztext {
		t.Fatalf("ersatztext der Datei %q weicht vom Code ab (%q)", d.Ersatztext, Ersatztext)
	}
	quellen := map[string]bool{}
	for _, q := range d.Quellen {
		quellen[q] = true
	}
	if len(quellen) == 0 {
		t.Fatal("kopf nennt keine quellen")
	}
	namen := map[string]bool{}
	for i, v := range d.Vektoren {
		v := v
		if v.Name == "" {
			t.Fatalf("Vektor %d ohne name", i)
		}
		if namen[v.Name] {
			t.Fatalf("Vektor %d: name %q doppelt", i, v.Name)
		}
		namen[v.Name] = true
		if !quellen[v.Quelle] {
			t.Fatalf("Vektor %q: unbekannte quelle %q", v.Name, v.Quelle)
		}
		if (len(v.Erwartet) == 0) == (v.Ablehnung == nil) {
			t.Fatalf("Vektor %q: genau eines von erwartet und ablehnung muss stehen", v.Name)
		}
		feld := v.Feld
		if feld == "" {
			feld = "titel"
		}
		erlaubt, ablehnungen := t16FeldSchema(t, v, feld)
		for k := range v.Eingabe {
			if !erlaubt[k] {
				t.Fatalf("Vektor %q (feld %s): unbekannter eingabe-Schlüssel %q", v.Name, feld, k)
			}
		}
		if v.Ablehnung != nil && !ablehnungen[*v.Ablehnung] {
			t.Fatalf("Vektor %q (feld %s): ablehnung %q ist kein Feldname dieses Feldes", v.Name, feld, *v.Ablehnung)
		}
		t.Run(v.Quelle+"/"+v.Name, func(t *testing.T) {
			switch feld {
			case "titel":
				pruefeT16Titel(t, v)
			case "spalte":
				pruefeT16Spalte(t, v)
			case "bearbeiter":
				pruefeT16Bearbeiter(t, v)
			}
		})
	}
}

// t16FeldSchema legt je Feld fest, welche eingabe-Schlüssel erlaubt sind und
// welche Pflicht (Konsistenz: feld=spalte verlangt eingabe.spalte usw.), und
// welche Feldnamen eine ablehnung tragen darf. Ein unbekanntes feld ist ein
// Testfehler.
func t16FeldSchema(t *testing.T, v t16Vektor, feld string) (erlaubt, ablehnungen map[string]bool) {
	t.Helper()
	switch feld {
	case "titel":
		if _, da := v.Eingabe["titel"]; !da {
			t.Fatalf("Vektor %q: eingabe.titel fehlt", v.Name)
		}
		if v.Ablehnung != nil {
			if _, da := v.Eingabe["vertraulich"]; !da {
				t.Fatalf("Vektor %q: ablehnung braucht eingabe.vertraulich", v.Name)
			}
		}
		return map[string]bool{"titel": true, "vertraulich": true}, map[string]bool{"titel": true, "vertraulich": true}
	case "spalte", "bearbeiter":
		if _, da := v.Eingabe[feld]; !da {
			t.Fatalf("Vektor %q: feld=%s verlangt eingabe.%s", v.Name, feld, feld)
		}
		return map[string]bool{feld: true}, map[string]bool{feld: true}
	}
	t.Fatalf("Vektor %q: unbekanntes feld %q", v.Name, feld)
	return nil, nil
}

func pruefeT16Titel(t *testing.T, v t16Vektor) {
	titelRoh := v.Eingabe["titel"]
	if v.Ablehnung != nil {
		_, err := LiesEingang(t16Huelle(string(titelRoh), string(v.Eingabe["vertraulich"])), "1.0.0")
		if eingangStatus(err) != 422 {
			t.Fatalf("Eingang nahm den Schatten an oder lehnte anders ab (%v), erwartet 422", err)
		}
		var f *EingangFehler
		if !errors.As(err, &f) || len(f.Fehler) != 1 || !strings.HasPrefix(f.Fehler[0].Grund, *v.Ablehnung+":") {
			t.Fatalf("Ablehnung nennt nicht das Feld %q: %v", *v.Ablehnung, err)
		}
		return
	}
	// Behandlung: roher Titel + Schalter → Titel oder null.
	var roh string
	if err := json.Unmarshal(titelRoh, &roh); err != nil {
		t.Fatalf("eingabe.titel eines erwartet-Vektors muss Text sein: %v", err)
	}
	vertraulich := false
	if sch, da := v.Eingabe["vertraulich"]; da {
		vertraulich = Vertraulich(map[string]json.RawMessage{"confidential": sch})
	}
	var ist *string
	if !vertraulich {
		s := Titel(roh)
		ist = &s
	}
	var soll *string
	if string(v.Erwartet) != "null" {
		var s string
		if err := json.Unmarshal(v.Erwartet, &s); err != nil {
			t.Fatalf("erwartet muss Text oder null sein: %v", err)
		}
		soll = &s
	}
	if (ist == nil) != (soll == nil) || (ist != nil && *ist != *soll) {
		t.Fatalf("Titel %s, Schalter %s: erwartet %s, ist %s",
			titelRoh, v.Eingabe["vertraulich"], zeig(soll), zeig(ist))
	}
	// Gegenrichtung (T-3): Wurde der Titel verändert, ist der ROHE
	// Titel kein Fixpunkt — der Eingang lehnt ihn mit 422 am Feld titel
	// ab. Ausgenommen sind vertrauliche Vektoren (kein Titel) und der
	// Ersatztext (roh == erwartet).
	if soll != nil && roh != *soll {
		_, err := LiesEingang(t16Huelle(string(titelRoh), "false"), "1.0.0")
		var f *EingangFehler
		if eingangStatus(err) != 422 || !errors.As(err, &f) || len(f.Fehler) != 1 ||
			!strings.HasPrefix(f.Fehler[0].Grund, "titel:") {
			t.Fatalf("Eingang lehnt den unbehandelten Titel nicht mit 422 am Feld titel ab: %v", err)
		}
	}
	// Fixpunkt: der Eingang nimmt genau das an, was die Behandlung liefert.
	titel, vert := "null", "true"
	if soll != nil {
		b, _ := json.Marshal(*soll)
		titel, vert = string(b), "false"
	}
	menge, err := LiesEingang(t16Huelle(titel, vert), "1.0.0")
	if err != nil || len(menge) != 1 {
		t.Fatalf("Eingang lehnt das Ergebnis der Behandlung ab: %v", err)
	}
}

func pruefeT16Spalte(t *testing.T, v t16Vektor) {
	spalteRoh := string(v.Eingabe["spalte"])
	if v.Ablehnung != nil {
		_, err := LiesEingang(t16HuelleFeld(`"Titel"`, "false", spalteRoh, `[]`), "1.0.0")
		if f := t16Abgelehnt(err, *v.Ablehnung); f != nil {
			t.Fatalf("Spalte %s: %v", spalteRoh, f)
		}
		return
	}
	var roh, soll string
	if err := json.Unmarshal(v.Eingabe["spalte"], &roh); err != nil {
		t.Fatalf("eingabe.spalte eines erwartet-Vektors muss Text sein: %v", err)
	}
	if err := json.Unmarshal(v.Erwartet, &soll); err != nil {
		t.Fatalf("erwartet einer Spalte muss Text sein: %v", err)
	}
	// Behandlung durch den echten Ausgang (Baue, columnDefs → Schatten).
	if ist := t16BaueSpalte(t, roh); ist != soll {
		t.Fatalf("Spalte %q: erwartet %q, ist %q", roh, soll, ist)
	}
	// Gegenrichtung (T-3): Ist der rohe Name nicht der behandelte, lehnt der
	// Eingang ihn mit 422 am Feld spalte ab.
	if roh != soll {
		_, err := LiesEingang(t16HuelleFeld(`"Titel"`, "false", spalteRoh, `[]`), "1.0.0")
		if f := t16Abgelehnt(err, "spalte"); f != nil {
			t.Fatalf("Eingang lehnt die unbehandelte Spalte nicht ab: %v", f)
		}
	}
	// Fixpunkt: der Eingang nimmt das Ergebnis der Behandlung an.
	b, _ := json.Marshal(soll)
	if m, err := LiesEingang(t16HuelleFeld(`"Titel"`, "false", string(b), `[]`), "1.0.0"); err != nil || len(m) != 1 {
		t.Fatalf("Eingang lehnt das Ergebnis der Behandlung ab: %v", err)
	}
}

func pruefeT16Bearbeiter(t *testing.T, v t16Vektor) {
	listeRoh := string(v.Eingabe["bearbeiter"])
	if v.Ablehnung != nil {
		_, err := LiesEingang(t16HuelleFeld(`"Titel"`, "false", `""`, listeRoh), "1.0.0")
		if f := t16Abgelehnt(err, *v.Ablehnung); f != nil {
			t.Fatalf("Bearbeiter %s: %v", listeRoh, f)
		}
		return
	}
	var roh, soll []string
	if err := json.Unmarshal(v.Eingabe["bearbeiter"], &roh); err != nil {
		t.Fatalf("eingabe.bearbeiter eines erwartet-Vektors muss eine Liste von Text sein: %v", err)
	}
	if err := json.Unmarshal(v.Erwartet, &soll); err != nil || soll == nil {
		t.Fatalf("erwartet der Bearbeiter muss eine Liste von Text sein: %v", err)
	}
	// Behandlung durch den echten Ausgang (Baue, Identitäten = die Eingabe).
	if ist := t16BaueBearbeiter(t, roh); !reflect.DeepEqual(ist, soll) {
		t.Fatalf("Bearbeiter %q: erwartet %q, ist %q", roh, soll, ist)
	}
	// Gegenrichtung (T-3): Ist die rohe Liste nicht die behandelte, lehnt der
	// Eingang sie mit 422 am Feld bearbeiter ab.
	// Ausnahme: unterscheidet sich die Liste nur durch exakte Doppel, prüft der
	// Eingang das nicht (Absprache Board-Paar 11.10.2026, Karte #812) und nimmt sie an —
	// bis zu 10 Einträge; mehr als 10 lehnt der Eingang weiter ab, auch mit Doppeln
	// (elemente, BearbeiterMax), die Ablehnungsprüfung läuft dann also.
	nurDoppel := !reflect.DeepEqual(roh, soll) && len(roh) <= BearbeiterMax && reflect.DeepEqual(ohneExakteDoppel(roh), soll)
	if !reflect.DeepEqual(roh, soll) && !nurDoppel {
		_, err := LiesEingang(t16HuelleFeld(`"Titel"`, "false", `""`, listeRoh), "1.0.0")
		if f := t16Abgelehnt(err, "bearbeiter"); f != nil {
			t.Fatalf("Eingang lehnt die unbehandelte Liste nicht ab: %v", f)
		}
	}
	// Ausnahmezweig (V-1): Die Zusage „der Eingang nimmt solche Listen an“ wird
	// gemessen, nicht nur behauptet: angenommen und unverändert (mit Doppeln) übernommen.
	if nurDoppel {
		m, err := LiesEingang(t16HuelleFeld(`"Titel"`, "false", `""`, listeRoh), "1.0.0")
		if err != nil || len(m) != 1 || !reflect.DeepEqual(m[0].Bearbeiter, roh) {
			t.Fatalf("Eingang nimmt die Liste mit exakten Doppeln %q nicht unverändert an: %v", roh, err)
		}
	}
	// Fixpunkt: der Eingang nimmt die behandelte Liste an.
	b, _ := json.Marshal(soll)
	m, err := LiesEingang(t16HuelleFeld(`"Titel"`, "false", `""`, string(b)), "1.0.0")
	if err != nil || len(m) != 1 || !reflect.DeepEqual(m[0].Bearbeiter, soll) {
		t.Fatalf("Eingang lehnt das Ergebnis der Behandlung ab oder ändert es: %v", err)
	}
}

// ohneExakteDoppel: erstes Vorkommen bleibt, Reihenfolge bleibt.
func ohneExakteDoppel(l []string) []string {
	aus := []string{}
	g := map[string]bool{}
	for _, s := range l {
		if !g[s] {
			g[s] = true
			aus = append(aus, s)
		}
	}
	return aus
}

func zeig(s *string) string {
	if s == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *s)
}

// Jede Quelle des Kopfes wird von mindestens einem Vektor belegt.
func TestT16_JedeQuelleBelegt(t *testing.T) {
	d := ladeT16(t)
	n := map[string]int{}
	for _, v := range d.Vektoren {
		n[v.Quelle]++
	}
	for _, q := range d.Quellen {
		if n[q] == 0 {
			t.Errorf("quelle %q ohne Vektor", q)
		}
	}
}
