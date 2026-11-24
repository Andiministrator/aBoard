package schatten

// spiegel_rueckweg.go — Spiegel-Schranke in der Fassung von Stufe 2c
// (ADR-0141 §7, Karte #827, D4/D7 des Etappenplans).
//
// Gilt auf einer externen Kopie, wenn dort `boardpaar.rueckweg = an` steht.
// Außen darf dann bearbeitet werden, was der Rückweg nach innen tragen kann;
// alles andere bleibt gesperrt wie in 2b. Geprüft wird wie dort am Ergebnis
// gegen den Stand davor.
//
// Erlaubt an Spiegelkarten und wartenden Karten (außen angelegt, Marke steht
// noch aus):
//   - die Felder der Feldliste (KartenFeldliste), labels, assignees;
//   - Verschieben und Umsortieren zwischen markierten Lanes und Spalten;
//   - `confidential` einschalten (nie aus);
//   - Kommentare anfügen; Text (und `edited`) eines Kommentars ändern, der
//     nicht von innen stammt (Herkunft `aussen` oder noch ohne Herkunft);
//   - die Begleitfelder, die nicht hinausgehen und die Browser bzw. Server bei
//     jeder Bearbeitung schreiben (KartenBegleitfelder).
// Erlaubt an Spiegelterminen und wartenden Terminen: die Felder der
// TerminFeldliste, `confidential` einschalten, TerminBegleitfelder.
// Neue Karten in markierten Lanes (in einer markierten Spalte) und neue
// Termine sind erlaubt — sie werden wartend.
//
// Gesperrt bleibt alles andere: Löschen, eine Karte in eine Lane oder Spalte
// ohne Marke schieben, `confidential` abschalten, die Marke, Anhänge,
// Verweise, Projekt- und Agentenfelder, Kommentare löschen (D7) oder die
// eines inneren Kommentars ändern, jede Änderung an markierten Lanes und
// Spalten, doppelte IDs an Spiegeln.

import (
	"encoding/json"
	"sort"
)

// KartenBegleitfelder: Felder einer Spiegelkarte, die außen geändert werden
// dürfen, ohne hinauszugehen — activity und updated (Browser, jede
// Bearbeitung), claim (Übernahme außen), fristVonAgent und titelVonAgent
// (Agenten-Marken des Servers). Der Eingang bewahrt sie (E2).
var KartenBegleitfelder = []string{"activity", "updated", "claim", "fristVonAgent", "titelVonAgent"}

// TerminBegleitfelder: ebenso an Terminen (Agenten-Marke vonAgent).
var TerminBegleitfelder = []string{"vonAgent"}

func mengeAus(listen ...[]string) map[string]bool {
	m := map[string]bool{}
	for _, l := range listen {
		for _, s := range l {
			m[s] = true
		}
	}
	return m
}

var (
	karteRueckwegFrei  = mengeAus(KartenFeldliste, KartenBegleitfelder, []string{"labels", "assignees", "comments", "columnId", "confidential"})
	terminRueckwegFrei = mengeAus(TerminFeldliste, TerminBegleitfelder, []string{"confidential"})
	kommentarTextFrei  = map[string]bool{"text": true, "edited": true}
)

// objektVerstoesst: Ändert der Schritt a → n ein Feld außerhalb von frei, oder
// schaltet er `confidential` ab?
func objektVerstoesst(a, n map[string]json.RawMessage, frei map[string]bool) bool {
	for k, va := range a {
		vn, ok := n[k]
		if (!ok || !gleicherWert(va, vn)) && !frei[k] {
			return true
		}
	}
	for k := range n {
		if _, war := a[k]; !war && !frei[k] {
			return true
		}
	}
	return Vertraulich(a) && !Vertraulich(n)
}

// kommentareVerstossen: Kommentare dürfen dazukommen; keiner verschwindet;
// an einem bestehenden ändern sich höchstens Text und `edited`, und das nur,
// wenn er nicht von innen stammt.
func kommentareVerstossen(a, n map[string]json.RawMessage) bool {
	idsA, ka := kommentareNachID(a["comments"])
	_, kn := kommentareNachID(n["comments"])
	for _, id := range idsA {
		alt := ka[id]
		neu, gibtEs := kn[id]
		if !gibtEs {
			return true
		}
		if gleichesObjekt(alt, neu) {
			continue
		}
		if zeichenkette(alt["herkunft"]) == HerkunftInnen || objektVerstoesst(alt, neu, kommentarTextFrei) {
			return true
		}
	}
	// Sprint-94-Kritik K-2/K-4: ein NEUER Kommentar muss den Rückweg
	// überstehen. Eine ID außerhalb von GueltigeKennung spiegelt der Hinweg
	// nie zurück, und Anhänge trägt die Journal-Form nicht (§7: kein
	// Hochladen an Spiegeln) — beides verschwände nach dem Bestätigen außen.
	idsN, _ := kommentareNachID(n["comments"])
	for _, id := range idsN {
		if _, war := ka[id]; !war && neuerKommentarVerboten(kn[id]) {
			return true
		}
	}
	// F-2269 (Sitzungs-Kritik N-2): Ein Kommentar ohne Zeichenketten-ID
	// (fehlt, Zahl, leer) übersprang kommentareNachID — kein Verstoß, kein
	// Journal-Eintrag, beim nächsten Hinweg weg. Ein solcher Kommentar darf
	// bleiben, wenn er schon vorher genau so dastand; ein neuer ist ein Verstoß.
	return neuerKommentarOhneID(a["comments"], n["comments"])
}

// kommentareOhneID: die Kommentare einer Liste ohne Zeichenketten-ID.
func kommentareOhneID(roh json.RawMessage) []map[string]json.RawMessage {
	var l []map[string]json.RawMessage
	_ = json.Unmarshal(roh, &l)
	var aus []map[string]json.RawMessage
	for _, c := range l {
		if c != nil && zeichenkette(c["id"]) == "" {
			aus = append(aus, c)
		}
	}
	return aus
}

// neuerKommentarOhneID: Steht in n ein Kommentar ohne Zeichenketten-ID, der
// in a nicht (gleich oft) genau so stand?
func neuerKommentarOhneID(a, n json.RawMessage) bool {
	vorher := kommentareOhneID(a)
	benutzt := make([]bool, len(vorher))
	for _, c := range kommentareOhneID(n) {
		gefunden := false
		for i, v := range vorher {
			if !benutzt[i] && gleichesObjekt(v, c) {
				benutzt[i], gefunden = true, true
				break
			}
		}
		if !gefunden {
			return true
		}
	}
	return false
}

// neuerKommentarVerboten: ID nicht hinausgehbar, oder Anhänge am Kommentar.
func neuerKommentarVerboten(c map[string]json.RawMessage) bool {
	return !GueltigeKennung(zeichenkette(c["id"])) || nichtLeereListe(c["attachments"])
}

// nichtLeereListe: ein Feld, das eine nicht leere Liste ist (oder keine
// lesbare Liste — dann vorsichtshalber ja). Fehlt es oder ist es null: nein.
func nichtLeereListe(r json.RawMessage) bool {
	if len(r) == 0 || istNull(r) {
		return false
	}
	var l []json.RawMessage
	return json.Unmarshal(r, &l) != nil || len(l) > 0
}

// SpiegelVerstoesseRueckweg ist die Spiegel-Schranke mit Rückweg (2c). wartend:
// Außen-IDs aus boardpaar_wartend. Ist ein Stand nicht lesbar: unlesbar = true.
func SpiegelVerstoesseRueckweg(vorher, nachher []byte, wartend map[string]bool) (verstoesse []SpiegelOrt, unlesbar bool) {
	alt, ok1 := liesSpiegelBild(vorher)
	neu, ok2 := liesSpiegelBild(nachher)
	ja, ok3 := liesJournalBild(vorher, wartend)
	jn, ok4 := liesJournalBild(nachher, wartend)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, true
	}
	gemeldet := map[SpiegelOrt]bool{}
	melde := func(art, id string) {
		o := SpiegelOrt{Art: art, ID: id}
		if !gemeldet[o] {
			gemeldet[o] = true
			verstoesse = append(verstoesse, o)
		}
	}
	for _, o := range neu.doppelt {
		melde(o.Art, o.ID)
	}
	// Markierte Lanes und Spalten: wie in 2b.
	vergleicheReihe := func(art string, a, b []string, metaA, metaB map[string]map[string]json.RawMessage) {
		for i, id := range a {
			m, gibtEs := metaB[id]
			if !gibtEs || i >= len(b) || b[i] != id || !gleichesObjekt(metaA[id], m) {
				melde(art, id)
			}
		}
		for _, id := range b {
			if _, war := metaA[id]; !war {
				melde(art, id)
			}
		}
	}
	vergleicheReihe("lane", alt.lanes, neu.lanes, alt.laneMeta, neu.laneMeta)
	vergleicheReihe("spalte", alt.spalten, neu.spalten, alt.spaltenMeta, neu.spaltenMeta)

	// Erfasste Karten (Spiegel und wartende, ohne Altkarten ohne Marke).
	for s, a := range ja.karten {
		if a.wartend && ja.neuIDs[a.kennung] {
			continue
		}
		id := zeichenkette(a.karte["id"])
		n, gibtEs := jn.karten[s]
		switch {
		case !gibtEs:
			melde("karte", id)
		case n.lane == "" || n.spalte == "":
			melde("karte", id) // in eine Lane oder Spalte ohne Marke
		case objektVerstoesst(a.karte, n.karte, karteRueckwegFrei) || kommentareVerstossen(a.karte, n.karte):
			melde("karte", id)
		}
	}
	// Neue Karten (werden wartend): nur in einer markierten Spalte. Altkarten
	// ohne Marke (schon vorher unmarkiert und nicht wartend) gehören nur außen
	// und bleiben frei.
	for _, jk := range jn.neu {
		// K-2: Die ID einer neuen Karte ist ihre Kennung (ursprung) — der
		// Hinweg spiegelt nur [A-Za-z0-9_-]{1,64}; eine andere Kennung ginge
		// innen ohne Ursprung an und verschwände außen nach dem Bestätigen.
		// K-4: ihre Kommentare prüft dieselbe Regel wie an Spiegeln.
		// Verify F-V4: Anhänge und Verweise trägt die Journal-Form nicht
		// (KartenFeldliste) — eine neue Karte, die sie mitbringt, stünde innen
		// ohne sie, außen mit ihnen (mischeKarte behält sie; Abweichung, kein
		// Verlust — berichtigt nach Sitzungs-Kritik N-9). Bewusst gesperrt.
		if id := zeichenkette(jk.karte["id"]); !ja.neuIDs[id] && (jk.spalte == "" || !GueltigeKennung(id) ||
			kommentareVerstossen(map[string]json.RawMessage{}, jk.karte) ||
			nichtLeereListe(jk.karte["attachments"]) || nichtLeereListe(jk.karte["links"])) {
			melde("karte", id)
		}
	}

	// F-2267 (Sitzungs-Kritik N-1): neue Termine wie neue Karten — die ID
	// wird ihre Kennung (ursprung), und pruefe() am Anwenden-Weg nimmt nur
	// [A-Za-z0-9_-]{1,64}. Ein Termin mit anderer ID hielte dort das Skript an
	// (422, HALT) und staute den Rückweg des ganzen Boards. Was vorher schon
	// stand (vorhandenT), erfasst das Journal nicht — es bleibt frei.
	for _, t := range jn.neuT {
		if id := zeichenkette(t["id"]); !ja.vorhandenT[id] && !GueltigeKennung(id) {
			melde("termin", id)
		}
	}

	// Marken setzt nur der Eingang: eine neu auftauchende Marke (neue Karte
	// oder neuer Termin mit Marke) und eine doppelte Marke sind Verstöße
	// (Sitzungs-Kritik A-5; auf dem Ops-Weg verhindert der Fold das schon,
	// VOM_FOLD_VERWALTET — hier die Verteidigung am Ergebnis).
	for s, n := range jn.karten {
		if _, war := ja.karten[s]; (!war && s[0] == 'k') || jn.doppelt[s] {
			melde("karte", zeichenkette(n.karte["id"]))
		}
	}
	for s, n := range jn.termine {
		if _, war := ja.termine[s]; (!war && s[0] == 't') || jn.doppelt[s] {
			melde("termin", zeichenkette(n["id"]))
		}
	}

	// Termine.
	for s, a := range ja.termine {
		n, gibtEs := jn.termine[s]
		if !gibtEs || objektVerstoesst(a, n, terminRueckwegFrei) {
			melde("termin", zeichenkette(a["id"]))
		}
	}
	sort.SliceStable(verstoesse, func(i, j int) bool {
		if verstoesse[i].Art != verstoesse[j].Art {
			return verstoesse[i].Art < verstoesse[j].Art
		}
		return verstoesse[i].ID < verstoesse[j].ID
	})
	return verstoesse, false
}

// KartenVerzeichnisGesperrt (F-2242): Ist das Anhang-Verzeichnis dieser Karte
// auf einer Kopie mit Rückweg gesperrt? Ja für jede Karte, die das Journal
// erfasst — Spiegel, wartende und neue Karten in markierten Lanes —, also
// genau die Karten, an denen die Kommentar-Schranke (kommentareVerstossen,
// neuerKommentarVerboten) Anhänge ablehnt (ADR-0141 §7: Hochladen an Spiegeln
// wird abgelehnt; die Journal-Form trägt keine Anhänge). Eine Karte, die es im
// Stand nicht gibt, oder eine eigene Karte ohne Marke außerhalb markierter
// Lanes bleibt frei. unlesbar = true, wenn der Stand nicht lesbar ist.
func KartenVerzeichnisGesperrt(state []byte, wartend map[string]bool, kartenID string) (gesperrt, unlesbar bool) {
	b, ok := liesJournalBild(state, wartend)
	if !ok {
		return false, true
	}
	// b.karten ist nach Marke geschlüsselt: tragen zwei Karten dieselbe Marke, überschreibt die
	// zweite die erste, und die erste Karte würde als frei durchgehen (fail-open). idZuBezug
	// enthält dagegen jede erfasste Karten-ID (Karte #863, A-3).
	if _, erfasst := b.idZuBezug[kartenID]; erfasst {
		return true, false
	}
	return false, false
}

// WartendVerstoesse ergänzt die Schranke im Schutzmodus (2b-Fassung, auch für
// Ganz-PUT und Transfer mit Rückweg, D2 des Etappenplans) um die wartenden
// Spiegel: Jede Änderung an einer Karte oder einem Termin aus `wartend` —
// Feld, Lane, Spalte, Entfernen — ist ein Verstoß. Ohne wartende Einträge
// kostet sie nichts.
func WartendVerstoesse(vorher, nachher []byte, wartend map[string]bool) (verstoesse []SpiegelOrt, unlesbar bool) {
	if len(wartend) == 0 {
		return nil, false
	}
	ja, ok1 := liesJournalBild(vorher, wartend)
	jn, ok2 := liesJournalBild(nachher, wartend)
	if !ok1 || !ok2 {
		return nil, true
	}
	for id := range wartend {
		s := schluesselKarte(id, true)
		if a, war := ja.karten[s]; war {
			n, gibtEs := jn.karten[s]
			if !gibtEs || n.lane != a.lane || n.spalte != a.spalte || !gleichesObjekt(a.karte, n.karte) {
				verstoesse = append(verstoesse, SpiegelOrt{Art: "karte", ID: id})
			}
			continue
		}
		if a, war := ja.termine["w:"+id]; war {
			if n, gibtEs := jn.termine["w:"+id]; !gibtEs || !gleichesObjekt(a, n) {
				verstoesse = append(verstoesse, SpiegelOrt{Art: "termin", ID: id})
			}
		}
	}
	sort.Slice(verstoesse, func(i, j int) bool { return verstoesse[i].ID < verstoesse[j].ID })
	return verstoesse, false
}

// SpiegelOrteVereint: die Vereinigung zweier Verstoß-Listen, ohne Doppel,
// sortiert wie SpiegelVerstoesseRueckweg (Art, dann ID).
func SpiegelOrteVereint(a, b []SpiegelOrt) []SpiegelOrt {
	if len(b) == 0 {
		return a
	}
	gesehen := map[SpiegelOrt]bool{}
	var aus []SpiegelOrt
	for _, o := range append(append([]SpiegelOrt{}, a...), b...) {
		if !gesehen[o] {
			gesehen[o] = true
			aus = append(aus, o)
		}
	}
	sort.SliceStable(aus, func(i, j int) bool {
		if aus[i].Art != aus[j].Art {
			return aus[i].Art < aus[j].Art
		}
		return aus[i].ID < aus[j].ID
	})
	return aus
}
