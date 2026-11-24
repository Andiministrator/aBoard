package schatten

// spiegel_mischen.go — Merge-Eingang außen, Board-Paar Stufe 2c (ADR-0141 §2,
// §4; Karte #827, Etappe E2).
//
// Ab 2c bearbeiten Menschen außen Spiegel. Der Eingang ersetzt sie deshalb
// nicht mehr, er mischt die Menge von innen in den Bestand:
//   - Die Felder der Feldliste kommen von innen — außer an einer Stelle, für
//     die außen ein unbestätigter Journal-Eintrag liegt (§4: „überschreibt
//     keine Stelle, für die außen ein unbestätigter Journal-Eintrag liegt“).
//     Erst nach der Bestätigung gilt wieder innen; dort ist die Änderung dann
//     angewendet oder als Konflikt kommentiert.
//   - Alles außerhalb der Feldliste bleibt vom Bestand: die Begleitfelder
//     (activity, updated, claim, Agenten-Marken), displayId, authorEmail an
//     Kommentaren, die nicht von innen stammen.
//   - Wartende Spiegel (außen angelegt, Marke steht aus) übernimmt ein
//     Spiegel mit `ursprung` = ihre Außen-ID: Marke setzen, ID und Nummer
//     behalten. Entfernt wird eine wartende Karte nur in den beiden Fällen aus
//     §2 (W2-3): `ursprung` an einem Schatten (innen vertraulich geworden) oder
//     nirgends, obwohl ihr `angelegt` bis `rueckweg_bis` bestätigt war (innen
//     gelöscht). Jede übernommene oder entfernte wartende Karte meldet der
//     Abgleich in WartendErledigt; die Zeile in boardpaar_wartend löscht der
//     Speicher in derselben Transaktion.
//   - Karten, die der Eingang nicht platziert — unmarkierte und Spiegel mit
//     geschützter Position —, behalten ihren Platz: Sie stehen nach demselben
//     Vorgänger wie vorher, sonst am Anfang der Lane.

import (
	"encoding/json"
	"strings"
)

// Schutz: was der Eingang aus dem Journal außen beachtet. Leer (oder nil)
// heißt: kein Journal, keine wartenden — der Eingang verhält sich wie in 2b.
type Schutz struct {
	// Offen: unbestätigte Journal-Stellen je Objekt (OffenSchluessel), als
	// Menge „art|stelle“.
	Offen map[string]map[string]bool
	// Wartend: Außen-ID → Zeile aus boardpaar_wartend.
	Wartend map[string]WartendInfo
	// RueckwegBis: Zählmarke des Skripts aus dem PUT; nil = keine wartende
	// Karte wird wegen fehlender Menge entfernt (Sitzungs-QA QA-4).
	RueckwegBis *int64
}

// WartendInfo ist eine Zeile aus boardpaar_wartend.
type WartendInfo struct {
	Objekt      string
	SeqAngelegt int64
}

// OffenSchluessel: Schlüssel eines Objekts in Schutz.Offen (Objektart,
// Kennung bzw. Außen-ID, wartend).
func OffenSchluessel(objekt, kennung string, wartend bool) string {
	w := "0"
	if wartend {
		w = "1"
	}
	return objekt + "\x00" + kennung + "\x00" + w
}

// MerkeOffen trägt eine unbestätigte Stelle ein.
func (s *Schutz) MerkeOffen(objekt, kennung string, wartend bool, art, stelle string) {
	if s.Offen == nil {
		s.Offen = map[string]map[string]bool{}
	}
	k := OffenSchluessel(objekt, kennung, wartend)
	if s.Offen[k] == nil {
		s.Offen[k] = map[string]bool{}
	}
	s.Offen[k][art+"|"+stelle] = true
}

// stellen: die offenen Stellen eines Objekts (Vereinigung über seine
// Schlüssel — jede übernommene Karte hat zwei: ihre Kennung und ihre ID am
// Bestand als wartend, F-2220).
type stellen map[string]bool

func (s *Schutz) stellenVon(schluessel ...string) stellen {
	aus := stellen{}
	if s == nil {
		return aus
	}
	for _, k := range schluessel {
		for st := range s.Offen[k] {
			aus[st] = true
		}
	}
	return aus
}

func (s stellen) hat(art, stelle string) bool { return s[art+"|"+stelle] }

// feld: Ist das Feld f geschützt? Die Beschreibung auch, wenn ein Häkchen
// der Checkliste offen ist — die Checkliste steht in der Beschreibung.
func (s stellen) feld(f string) bool {
	if s.hat(JArtFeld, f) {
		return true
	}
	if f == "description" {
		for st := range s {
			if strings.HasPrefix(st, JArtCheckpunkt+"|") {
				return true
			}
		}
	}
	return false
}

// platz: Lane, Spalte und Position sind geschützt (verschoben, umsortiert
// oder außen angelegt und noch nicht bestätigt).
func (s stellen) platz() bool {
	return s.hat(JArtVerschoben, "ort") || s.hat(JArtReihenfolge, "reihenfolge") || s.hat(JArtAngelegt, "")
}

// werte: die Werte hinter „art|praefix“ (etwa die Labels mit offenem
// liste.add).
func (s stellen) werte(art, praefix string) map[string]bool {
	aus := map[string]bool{}
	p := art + "|" + praefix
	for st := range s {
		if w, ok := strings.CutPrefix(st, p); ok {
			aus[w] = true
		}
	}
	return aus
}

// begleitDefaults: Felder, die SpiegelKarteAussen auf jeder Karte setzt, die
// aber nicht zur Feldliste gehören — der Bestand behält seine, eine Karte ohne
// bekommt den Standard.
var begleitDefaults = []string{"links", "attachments", "activity", "project_id", "coverAttachmentId", "recurrence"}

func klon(o map[string]json.RawMessage) map[string]json.RawMessage {
	k := make(map[string]json.RawMessage, len(o))
	for a, b := range o {
		k[a] = b
	}
	return k
}

// mischeKarte: die Spiegelkarte neu (aus der Menge, mit ID und Nummer des
// Bestands) in den Bestand b gemischt.
func mischeKarte(b, neu map[string]json.RawMessage, offen stellen) map[string]json.RawMessage {
	r := klon(b)
	r["id"], r["displayId"], r["spiegel"] = neu["id"], neu["displayId"], neu["spiegel"]
	for _, f := range KartenFeldliste {
		if offen.feld(f) {
			continue
		}
		if v, ok := neu[f]; ok {
			r[f] = v
		} else {
			delete(r, f)
		}
	}
	for _, f := range []string{"labels", "assignees"} {
		r[f] = mischeListe(b[f], neu[f], offen.werte(JArtListeAdd, f+":"), offen.werte(JArtListeRemove, f+":"))
	}
	r["comments"] = mischeKommentare(b["comments"], neu["comments"], offen)
	if !offen.platz() {
		r["columnId"] = neu["columnId"]
	}
	if !offen.hat(JArtVertraulich, "confidential") {
		// Innen ist die Karte nicht vertraulich (sonst käme sie als Schatten).
		delete(r, "confidential")
	}
	for _, f := range begleitDefaults {
		if _, ok := r[f]; !ok {
			r[f] = neu[f]
		}
	}
	return r
}

// mischeListe: die Liste von innen, mit den offenen Hinzufügungen (in der
// Reihenfolge des Bestands) und ohne die offenen Entfernungen.
func mischeListe(bestand, innen json.RawMessage, dazu, weg map[string]bool) json.RawMessage {
	if len(dazu) == 0 && len(weg) == 0 {
		return innen
	}
	liste := listeAlsMenge(innen)
	aus := make([]string, 0, len(liste)+len(dazu))
	da := map[string]bool{}
	for _, x := range liste {
		if !weg[x] && !da[x] {
			aus = append(aus, x)
			da[x] = true
		}
	}
	for _, x := range listeAlsMenge(bestand) {
		if dazu[x] && !da[x] {
			aus = append(aus, x)
			da[x] = true
		}
	}
	return roh(aus)
}

// mischeKommentare: die Kommentare von innen; ein offen bearbeiteter behält
// Text und edited des Bestands, einer, der nicht von innen stammt, seine
// authorEmail (Server-Stempel außen, geht nicht hinaus). Außen neu angelegte
// mit offenem kommentar.add bleiben, hinter ihrem bisherigen Vorgänger.
func mischeKommentare(bestandRoh, innenRoh json.RawMessage, offen stellen) json.RawMessage {
	var bestand, innen []map[string]json.RawMessage
	_ = json.Unmarshal(bestandRoh, &bestand)
	if json.Unmarshal(innenRoh, &innen) != nil {
		return innenRoh
	}
	nachID := map[string]map[string]json.RawMessage{}
	for _, c := range bestand {
		if c != nil {
			nachID[zeichenkette(c["id"])] = c
		}
	}
	aus := make([]map[string]json.RawMessage, 0, len(innen)+len(bestand))
	da := map[string]int{}
	for _, c := range innen {
		id := zeichenkette(c["id"])
		if bc, ok := nachID[id]; ok {
			if offen.hat(JArtKommentarEdit, "kommentar:"+id) {
				c = klon(c)
				c["text"] = bc["text"]
				if e, ok := bc["edited"]; ok {
					c["edited"] = e
				} else {
					delete(c, "edited")
				}
			}
			if ae, ok := bc["authorEmail"]; ok && zeichenkette(c["herkunft"]) != HerkunftInnen {
				c = klon(c)
				c["authorEmail"] = ae
			}
		}
		da[id] = len(aus)
		aus = append(aus, c)
	}
	for j, bc := range bestand {
		if bc == nil {
			continue
		}
		id := zeichenkette(bc["id"])
		if _, schon := da[id]; schon || !offen.hat(JArtKommentarAdd, "kommentar:"+id) {
			continue
		}
		pos := 0
		for v := j - 1; v >= 0; v-- {
			if bestand[v] == nil {
				continue
			}
			if i, ok := da[zeichenkette(bestand[v]["id"])]; ok {
				pos = i + 1
				break
			}
		}
		aus = append(aus[:pos], append([]map[string]json.RawMessage{bc}, aus[pos:]...)...)
		for k, i := range da {
			if i >= pos {
				da[k] = i + 1
			}
		}
		da[id] = pos
	}
	return roh(aus)
}

// mischeTermin: der Spiegeltermin neu in den Bestand b gemischt.
func mischeTermin(b, neu map[string]json.RawMessage, offen stellen) map[string]json.RawMessage {
	r := klon(b)
	r["id"], r["spiegel"] = neu["id"], neu["spiegel"]
	for _, f := range TerminFeldliste {
		if offen.hat(JArtTerminFeld, f) {
			continue
		}
		if v, ok := neu[f]; ok {
			r[f] = v
		} else {
			delete(r, f)
		}
	}
	if !offen.hat(JArtVertraulich, "confidential") {
		delete(r, "confidential")
	}
	return r
}
