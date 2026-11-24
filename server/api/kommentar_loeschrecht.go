package api

// kommentar_loeschrecht.go — wer darf welchen Kommentar entfernen oder ändern
// (ADR-0136, Karte #693, F-1596, F-1611–F-1613).
//
// Ein Mensch entfernt oder ändert den eigenen Kommentar
// (comment.authorEmail = eigene E-Mail) oder — als Board-Owner oder
// Server-Admin — jeden. Entfernt jemand einen fremden Kommentar, hängt der
// Server in derselben Transaktion einen Verlaufseintrag an die Karte und
// meldet es nach dem COMMIT dem Autor (nachKommentarGeloescht).
//
// Verbindlich ist der Vergleich am ERGEBNIS (vergleicheKommentare): Stand vor
// dem Schreiben gegen Stand danach, je Karte, mit exakten Schlüsseln wie der
// Fold und der Browser lesen. Welche Op das bewirkt hat, spielt keine Rolle —
// die frühere Aufzählung der Op-Typen hatte Lücken (F-1611: card.fieldIf,
// card.field ohne value, Nicht-Objekte, Schattenschlüssel, unlesbarer Stand).
// Dieselbe Prüfung gilt für den Ganz-Board-PUT (F-1612).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

// AutorEmailFeld ist der Schlüssel, unter dem der Server die Urheberschaft
// eines Kommentars stempelt (ADR-0136 §1).
const AutorEmailFeld = "authorEmail"

// KommentarGeloescht beschreibt einen gelöschten fremden Kommentar — für den
// Verlaufseintrag und die Benachrichtigung des Autors.
type KommentarGeloescht struct {
	BoardID       string
	CardID        string
	CardDisplayID int
	CardTitle     string
	CommentID     string
	AutorEmail    string // leer bei Altbestand ohne Stempel
	AutorName     string // Anzeigename aus dem Kommentar
	Loescher      string // E-Mail der löschenden Person
	Textanfang    string
	// Karte #686: Antwortkommentar der Ausgangsschleuse (kind "extern").
	// Seine Löschung steht immer im Verlauf — auch die durch den Autor
	// selbst (Eigen; dann ohne Benachrichtigung).
	Extern bool
	Eigen  bool
}

// ExternKommentarArt ist der Wert von comment.kind für den Antwortkommentar
// der Ausgangsschleuse (ADR-0134 §3.6). Nur der Server schreibt ihn.
const ExternKommentarArt = "extern"

// WithKommentarLoeschHinweis setzt die Rückmeldung nach dem Löschen eines
// fremden Kommentars (main.go: Benachrichtigung an den Autor). Ohne sie
// bleibt es beim Verlaufseintrag.
func (a *BoardsAPI) WithKommentarLoeschHinweis(f func(context.Context, KommentarGeloescht)) *BoardsAPI {
	a.nachKommentarGeloescht = f
	return a
}

// WithUsers hängt das UserRepo an — für „Server-Admin darf fremde Kommentare
// löschen“ (ADR-0136 §2).
func (a *BoardsAPI) WithUsers(users *storage.UserRepo) *BoardsAPI {
	a.users = users
	return a
}

// stempleAutorEmail setzt comment.authorEmail eines comment.add auf `email`;
// ein mitgeschickter Wert (jede Schreibweise) fällt weg. Leeres `email`
// (Agent, Legacy-Bypass): nur entfernen. Kein lesbares Objekt: unverändert —
// das melden die Formprüfungen.
func stempleAutorEmail(payload json.RawMessage, email string) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil || p == nil {
		return payload
	}
	var c map[string]json.RawMessage
	if json.Unmarshal(p["comment"], &c) != nil || c == nil {
		return payload
	}
	for k := range c {
		if strings.EqualFold(k, AutorEmailFeld) {
			delete(c, k)
		}
	}
	if email != "" {
		c[AutorEmailFeld], _ = json.Marshal(email)
	}
	p["comment"], _ = json.Marshal(c)
	neu, err := json.Marshal(p)
	if err != nil {
		return payload
	}
	return neu
}

// darfFremdeKommentareLoeschen: Board-Owner (ACL) oder Server-Admin. Ein
// Lesefehler heißt „nein“ — im Zweifel bleibt der Kommentar stehen.
func (a *BoardsAPI) darfFremdeKommentareLoeschen(ctx context.Context, boardID, email string) bool {
	if email == "" {
		return false
	}
	if a.acl != nil {
		if role, found, err := a.acl.GetRole(ctx, boardID, email); err == nil && found && role == storage.RoleOwner {
			return true
		}
	}
	if a.users != nil {
		if u, err := a.users.GetByEmail(ctx, email); err == nil && u != nil && u.IsAdmin {
			return true
		}
	}
	return false
}

// kommentarSchreiber: wer schreibt, und was er an fremden Kommentaren darf.
// Genau eines von email (Mensch) und agentAkteur (agent:<name>) ist gesetzt;
// beide leer = Legacy-Bypass ohne Identität — dann wird nicht geprüft (wie
// der Rest der API in diesem Betrieb).
type kommentarSchreiber struct {
	email       string
	agentAkteur string
	darfFremde  bool
	// boardpaar: Rückweg des Board-Paars (ADR-0141 „Umsetzung 2c“ Punkt 23).
	// Kein Legacy-Bypass: eigen sind nur Kommentare von außen („extern: …“).
	boardpaar bool
}

// kommentarSchreiberFuer liest Identität und Rolle — VOR der Sperre (die
// ACL-Abfrage hält dann niemanden auf).
func (a *BoardsAPI) kommentarSchreiberFuer(ctx context.Context, boardID string) kommentarSchreiber {
	if boardpaarAkteurAus(ctx) != "" {
		return kommentarSchreiber{boardpaar: true}
	}
	if agent := AgentFromContext(ctx); agent != nil {
		return kommentarSchreiber{agentAkteur: agent.Akteur()}
	}
	email := actorFromContext(ctx)
	if email == "" {
		return kommentarSchreiber{}
	}
	return kommentarSchreiber{email: email, darfFremde: a.darfFremdeKommentareLoeschen(ctx, boardID, email)}
}

func (s kommentarSchreiber) prueft() bool { return s.email != "" || s.agentAkteur != "" || s.boardpaar }

// eigen: Gehört der Kommentar dem Schreiber?
//   - Mensch: authorEmail = eigene E-Mail. Dazu die KI-Antworten von vor
//     ADR-0136 (F-1615): ohne authorEmail, mit dem Autor-Label, das der
//     Server selbst vergibt („🤖 <anbieter>/<modell> (im Auftrag von
//     <e-mail>)“, llm_send.go/llm_stream.go). Das Label ist seit F-1613 vor
//     fremder Bearbeitung geschützt, und ein neuer Kommentar ohne den eigenen
//     Stempel wird abgelehnt — einen solchen Kommentar kann also niemand mehr
//     neu unterschieben.
//   - Agent: kein authorEmail und author = eigener Stempel agent:<name>
//     (ADR-0130 §4).
func (s kommentarSchreiber) eigen(k kommentarEintrag) bool {
	if s.boardpaar {
		return istExternerAutor(k.autor, k.autorEmail)
	}
	if s.agentAkteur != "" {
		return k.autorEmail == "" && k.autor == s.agentAkteur
	}
	if s.email == "" {
		return false
	}
	if k.autorEmail != "" {
		return strings.EqualFold(k.autorEmail, s.email)
	}
	return istAlteKIAntwortVon(k.autor, s.email)
}

// istAlteKIAntwortVon erkennt das Autor-Label einer KI-Antwort, die `email`
// angefragt hat (F-1615).
func istAlteKIAntwortVon(autor, email string) bool {
	const auftrag = " (im Auftrag von "
	if !strings.HasPrefix(autor, "🤖 ") || !strings.HasSuffix(autor, ")") {
		return false
	}
	i := strings.LastIndex(autor, auftrag)
	if i < 0 {
		return false
	}
	wer := autor[i+len(auftrag) : len(autor)-1]
	return wer != "" && strings.EqualFold(wer, email)
}

// neuOK: Trägt ein neuer Kommentar den Stempel dieses Schreibers? Menschen
// ihre E-Mail, Agenten keinen authorEmail und ihren author-Stempel.
func (s kommentarSchreiber) neuOK(k kommentarEintrag) bool {
	if s.boardpaar {
		return istExternerAutor(k.autor, k.autorEmail) || k.autorEmail == "" && k.autor == BoardpaarKonfliktAutor
	}
	if s.agentAkteur != "" {
		return k.autorEmail == "" && k.autor == s.agentAkteur
	}
	return k.autorEmail != "" && k.autorEmail == s.email
}

// ── Bestand lesen ─────────────────────────────────────────────────────────

type kommentarEintrag struct {
	id, autor, autorEmail, text string
	extern                      bool   // kind == "extern" (Karte #686)
	kanon                       string // kanonische JSON-Form (sortierte Schlüssel)
	schatten                    bool   // Schlüssel in zweiter Schreibweise
}

type karteBestand struct {
	titel     string
	displayID int
	// anzahl: wie oft die id im Stand vorkommt. Doppelte ids werden
	// zusammengeführt (F-1628); eine id, die danach öfter vorkommt als vorher,
	// ist ein Verstoß (KF-1, Sprint-82-Abschluss).
	anzahl   int
	schatten map[string]bool // auffällige Schlüssel der Karte
	// kommentareRoh: die Rohbytes aller comments-Werte der Karte (ohne
	// null). Gleiche Rohbytes vorher und nachher = unverändert; erst bei einer
	// Abweichung wird geparst (F-1628).
	kommentareRoh []byte
	kommentarLst  [][]byte
	// anhaengeRoh: Rohbytes aller attachments-Werte; anhaenge: Anhang-id →
	// commentId der Einträge, die an einem Kommentar hängen (ADR-0135, KF-3).
	anhaengeRoh []byte
	anhaenge    map[string]string
	// Erst nach parse() gültig. kaputt: comments ist kein Array, oder ein
	// Eintrag ist kein Objekt bzw. hat keine Text-id. Dann zählt der Rohtext
	// — unverändert darf es bleiben, geändert nicht (siehe
	// vergleicheKommentare).
	geparst    bool
	kaputt     bool
	kommentare []kommentarEintrag
	verlauf    verlaufRoh
}

type kommentarBestand struct {
	reihenfolge []string
	karten      map[string]*karteBestand
	// Board-weit, erst nach alleGeparst() gültig: alle kanonischen Kommentare
	// (Kopie an eine andere Karte) und Kommentar-id → authorEmail.
	alleFertig bool
	kopien     map[string]bool
	autorJeID  map[string]string
}

// Schlüssel, die eine zweite Schreibweise nicht neben sich haben dürfen —
// Go-Structs lesen case-insensitiv, der Fold und der Browser exakt (F-1611,
// Punkt 4). Zwei Schlüssel, die sich nur in der Schreibweise unterscheiden,
// sind unabhängig davon immer auffällig.
var kritischeKartenFelder = []string{"id", "title", "displayId", "kind", "comments", "activity", "columnId"}
var kritischeKommentarFelder = []string{"id", "author", AutorEmailFeld, "text", "created", "attachments", "kind", "schleuse"}

func auffaelligeSchluessel[V any](m map[string]V, kritisch []string) map[string]bool {
	aus := map[string]bool{}
	for k := range m {
		for k2 := range m {
			if k != k2 && strings.EqualFold(k, k2) {
				aus[k] = true
			}
		}
		for _, f := range kritisch {
			if k != f && strings.EqualFold(k, f) {
				aus[k] = true
			}
		}
	}
	return aus
}

// kanonisch: dieselbe Bedeutung ergibt denselben Text (Go sortiert die
// Schlüssel einer Map beim Marshal). Bei einem Objekt zählen Schlüssel mit
// null, leerem Array oder leerem Objekt als nicht vorhanden (F-1629): Ein
// Client, der einem fremden Kommentar `attachments: []` ergänzt, ändert ihn
// nicht.
func kanonisch(roh json.RawMessage) (string, bool) {
	var v any
	if json.Unmarshal(roh, &v) != nil {
		return "", false
	}
	if m, ok := v.(map[string]any); ok {
		for k, w := range m {
			switch x := w.(type) {
			case nil:
				delete(m, k)
			case []any:
				if len(x) == 0 {
					delete(m, k)
				}
			case map[string]any:
				if len(x) == 0 {
					delete(m, k)
				}
			}
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// leseKommentarBestand liest je Karte die Kommentare und den Verlauf als
// Rohbytes — in einem Durchlauf mit exakten Schlüsseln, wie der Fold
// (durchlaufeKarten, F-1628). Nur was für die Prüfungen nötig ist, muss
// lesbar sein: Ein Typfehler in einem anderen Feld (etwa title: 123) stört
// nicht (F-1611, Punkt 5). ok=false, wenn die Struktur bis zu den Karten
// nicht stimmt oder eine Karte keine Text-id hat — dann weiss niemand, welche
// Kommentare es gibt.
func leseKommentarBestand(state []byte) (*kommentarBestand, bool) {
	b := &kommentarBestand{karten: map[string]*karteBestand{}}
	idOK := true
	ok := durchlaufeKarten(state, func(karte map[string][]byte) {
		var id string
		if json.Unmarshal(karte["id"], &id) != nil {
			idOK = false
			return
		}
		kb, schonDa := b.karten[id]
		if !schonDa {
			kb = &karteBestand{schatten: map[string]bool{}}
			_ = json.Unmarshal(karte["title"], &kb.titel)
			_ = json.Unmarshal(karte["displayId"], &kb.displayID)
			b.karten[id] = kb
			b.reihenfolge = append(b.reihenfolge, id)
		}
		kb.anzahl++
		for k := range auffaelligeSchluessel(karte, kritischeKartenFelder) {
			kb.schatten[k] = true
		}
		if roh, da := karte["attachments"]; da && string(roh) != "null" {
			kb.anhaengeRoh = append(kb.anhaengeRoh, roh...)
			// VR2-3: Element für Element — ein einziges Nicht-Objekt (etwa eine
			// Legacy-Anhang-id als String, kommentar_anhaenge.go) darf den Schutz
			// der übrigen Einträge nicht abschalten.
			var liste []json.RawMessage
			if json.Unmarshal(roh, &liste) == nil {
				for _, el := range liste {
					var e map[string]json.RawMessage
					if json.Unmarshal(el, &e) != nil {
						continue
					}
					var aid, kid string
					if json.Unmarshal(e["id"], &aid) == nil && json.Unmarshal(e["commentId"], &kid) == nil && aid != "" && kid != "" {
						if kb.anhaenge == nil {
							kb.anhaenge = map[string]string{}
						}
						kb.anhaenge[aid] = kid
					}
				}
			}
		}
		if roh, da := karte["activity"]; da && string(roh) != "null" {
			kb.verlauf.listen = append(kb.verlauf.listen, roh)
			kb.verlauf.roh = append(kb.verlauf.roh, roh...)
		}
		roh, da := karte["comments"]
		if !da || string(roh) == "null" {
			return
		}
		kb.kommentarLst = append(kb.kommentarLst, roh)
		kb.kommentareRoh = append(kb.kommentareRoh, roh...)
	})
	if !ok || !idOK {
		return nil, false
	}
	return b, true
}

// parse liest die Kommentare der Karte (einmal).
func (kb *karteBestand) parse() {
	if kb.geparst {
		return
	}
	kb.geparst = true
	for _, roh := range kb.kommentarLst {
		var liste []json.RawMessage
		if json.Unmarshal(roh, &liste) != nil {
			kb.kaputt = true
			continue
		}
		for _, eRoh := range liste {
			var e map[string]json.RawMessage
			if json.Unmarshal(eRoh, &e) != nil || e == nil {
				kb.kaputt = true
				continue
			}
			k := kommentarEintrag{schatten: len(auffaelligeSchluessel(e, kritischeKommentarFelder)) > 0}
			if json.Unmarshal(e["id"], &k.id) != nil {
				kb.kaputt = true
				continue
			}
			if roh, da := e[AutorEmailFeld]; da && json.Unmarshal(roh, &k.autorEmail) != nil {
				kb.kaputt = true
				continue
			}
			_ = json.Unmarshal(e["author"], &k.autor)
			_ = json.Unmarshal(e["text"], &k.text)
			var art string
			_ = json.Unmarshal(e["kind"], &art)
			k.extern = art == ExternKommentarArt
			var ok bool
			if k.kanon, ok = kanonisch(eRoh); !ok {
				kb.kaputt = true
				continue
			}
			kb.kommentare = append(kb.kommentare, k)
		}
	}
}

// alleGeparst füllt die board-weiten Abbildungen (einmal) — nur nötig, wenn
// ein Kommentar neu ist (Kopie?) oder eine id nicht an ihrer Karte steht.
func (b *kommentarBestand) alleGeparst() {
	if b.alleFertig {
		return
	}
	b.alleFertig = true
	b.kopien, b.autorJeID = map[string]bool{}, map[string]string{}
	for _, id := range b.reihenfolge {
		kb := b.karten[id]
		kb.parse()
		for _, k := range kb.kommentare {
			b.kopien[k.kanon] = true
			b.autorJeID[k.id] = k.autorEmail
		}
	}
}

func (b *kommentarBestand) istKopie(kanon string) bool {
	b.alleGeparst()
	return b.kopien[kanon]
}

// ── Vergleich am Ergebnis ─────────────────────────────────────────────────

// kommentarVerstoss: Art fremder-kommentar (entfernt, geändert, gefälscht),
// schattenschluessel oder stand-unlesbar.
type kommentarVerstoss struct {
	art, cardID, commentID string
}

func (v kommentarVerstoss) String() string {
	if v.cardID == "" {
		return v.art
	}
	if v.commentID == "" {
		return fmt.Sprintf("card %s (%s)", v.cardID, v.art)
	}
	return fmt.Sprintf("card %s comment %s (%s)", v.cardID, v.commentID, v.art)
}

// vergleicheKommentare prüft, was zwischen `vorher` und `nachher` mit den
// Kommentaren geschehen ist, und wendet die Regeln aus ADR-0136 an:
//   - Ein Kommentar fehlt danach (an einer Karte, die es danach noch gibt):
//     eigener frei, fremder nur mit darfFremde — dann steht er in
//     `geloescht` (Verlauf, Benachrichtigung).
//   - Ein Kommentar ist geändert (gleiche id, anderer Inhalt): authorEmail
//     darf sich nie ändern; den übrigen Inhalt eines fremden nur mit
//     darfFremde (F-1613).
//   - Ein Kommentar ist neu: Er trägt den Stempel des Schreibers, oder er ist
//     die unveränderte Kopie eines bestehenden (Karte kopieren).
//   - Neue Schlüssel in zweiter Schreibweise: abgelehnt (F-1611, Punkt 4).
//   - Ein Stand ist nicht lesbar: abgelehnt (fail-closed). Ist nur der Stand
//     vorher kaputt, darf ihn Owner/Admin ersetzen — sonst bliebe das Board
//     für immer gesperrt.
//
// Karten, die es danach nicht mehr gibt, zählen nicht: Eine Karte zu löschen
// bleibt Schreibrecht (ADR-0136 §Grenzen). Wird sie im selben Schreibvorgang
// mit derselben id neu angelegt, gibt es sie danach — dann gilt die Regel.
func vergleicheKommentare(vorher, nachher []byte, s kommentarSchreiber, boardID string) ([]kommentarVerstoss, []KommentarGeloescht) {
	alt, okAlt := leseKommentarBestand(vorher)
	return vergleicheMitBestand(alt, okAlt, nachher, s, boardID, nil)
}

// vergleicheMitBestand: wie vergleicheKommentare, mit schon gelesenem Stand
// vorher (Ops-Weg und PUT lesen ihn für Stempel und Verlauf ohnehin, F-1628).
// erlaubteAktionen: server-eigene Verlaufsaktionen, die dieser Schreibvorgang
// an NEUEN Einträgen setzen darf (Vorschlags-Zugang: "vorschlag").
func vergleicheMitBestand(alt *kommentarBestand, okAlt bool, nachher []byte, s kommentarSchreiber, boardID string, erlaubteAktionen map[string]bool) ([]kommentarVerstoss, []KommentarGeloescht) {
	if !s.prueft() {
		return nil, nil
	}
	neu, okNeu := leseKommentarBestand(nachher)
	if !okNeu || (!okAlt && !s.darfFremde) {
		return []kommentarVerstoss{{art: "stand-unlesbar"}}, nil
	}
	if !okAlt {
		return nil, nil
	}
	var verstoesse []kommentarVerstoss
	var geloescht []KommentarGeloescht
	fremd := func(cardID, commentID string) {
		verstoesse = append(verstoesse, kommentarVerstoss{art: "fremder-kommentar", cardID: cardID, commentID: commentID})
	}
	// KF-2 (Sprint-82-Abschluss): Eine Karte, die danach fehlt, und eine neue
	// mit derselben displayId sind dieselbe Karte — sonst ließe sich eine
	// Karte per PUT unter neuer id nachbauen, ohne den fremden Kommentar und
	// ohne Verlauf. Die displayId schickt der Client und kann nach Import
	// doppelt sein (ADR-0136 Nachtrag, VR2-2/VR3-4).
	// VR2-2: Eine sichtbar NEUE Nummer ist Löschen plus Neuanlage — das darf
	// jedes Schreibrecht (ADR-0136 §Grenzen); geschlossen wird nur der
	// unsichtbare Nachbau unter derselben Nummer. Teilen sich mehrere
	// verschwundene Karten eine displayId (Altbestand, Import), wird nicht
	// geraten: keine Zuordnung.
	verschwunden := map[int]*karteBestand{}
	mehrdeutig := map[int]bool{}
	for _, id := range alt.reihenfolge {
		if ka := alt.karten[id]; neu.karten[id] == nil && ka.displayID > 0 {
			if _, da := verschwunden[ka.displayID]; da {
				mehrdeutig[ka.displayID] = true
			}
			verschwunden[ka.displayID] = ka
		}
	}
	for _, cardID := range neu.reihenfolge {
		kn := neu.karten[cardID]
		ka := alt.karten[cardID]
		neueKarte := false
		if ka == nil && kn.displayID > 0 && !mehrdeutig[kn.displayID] {
			ka = verschwunden[kn.displayID]
		}
		if ka == nil {
			ka = &karteBestand{schatten: map[string]bool{}, geparst: true}
			neueKarte = true
		}
		// KF-1: Eine zweite Karte gleicher id wird mit der ersten
		// zusammengeführt (Fold und Browser sehen nur eine) — in der Kopie
		// ließen sich Kommentare und Verlauf verstecken, die an der
		// sichtbaren Karte fehlen. Was vorher schon doppelt war, bleibt
		// zulässig (Altbestand, F-1628); mehr Kopien als vorher nie.
		if kn.anzahl > 1 && kn.anzahl > ka.anzahl {
			verstoesse = append(verstoesse, kommentarVerstoss{art: "karte-doppelt", cardID: cardID})
			continue
		}
		for k := range kn.schatten {
			if !ka.schatten[k] {
				verstoesse = append(verstoesse, kommentarVerstoss{art: "schattenschluessel", cardID: cardID})
				break
			}
		}
		verstoesse = append(verstoesse, pruefeVerlauf(cardID, ka, kn, s, erlaubteAktionen)...)
		verstoesse = append(verstoesse, pruefeKommentarAnhaenge(cardID, ka, kn, s, neueKarte)...)
		// Gleiche Rohbytes: nichts geändert — auch ein kaputter Bestand darf
		// unverändert bleiben (sonst sperrte er die Karte für alle). Erst
		// bei einer Abweichung wird geparst (F-1628).
		if bytes.Equal(ka.kommentareRoh, kn.kommentareRoh) {
			continue
		}
		ka.parse()
		kn.parse()
		if ka.kaputt || kn.kaputt {
			// Jede Änderung an einem kaputten Bestand ist nicht beurteilbar.
			if kn.kaputt || !s.darfFremde {
				verstoesse = append(verstoesse, kommentarVerstoss{art: "stand-unlesbar", cardID: cardID})
			}
			continue
		}
		// Unverändertes paaren (Mehrfachmenge über die kanonische Form).
		offenNeu := make([]bool, len(kn.kommentare))
		for i := range offenNeu {
			offenNeu[i] = true
		}
		var restAlt []kommentarEintrag
		for _, b := range ka.kommentare {
			gefunden := false
			for i, a := range kn.kommentare {
				if offenNeu[i] && a.kanon == b.kanon {
					offenNeu[i], gefunden = false, true
					break
				}
			}
			if !gefunden {
				restAlt = append(restAlt, b)
			}
		}
		for _, b := range restAlt {
			// Geändert: derselbe id-Wert an der Karte, Inhalt anders.
			geaendert := -1
			for i, a := range kn.kommentare {
				if offenNeu[i] && a.id == b.id {
					geaendert = i
					break
				}
			}
			if geaendert >= 0 {
				a := kn.kommentare[geaendert]
				offenNeu[geaendert] = false
				switch {
				case a.schatten:
					verstoesse = append(verstoesse, kommentarVerstoss{art: "schattenschluessel", cardID: cardID, commentID: b.id})
				case a.extern || b.extern:
					// Karte #686 (ADR-0134 §3.6): Der Antwortkommentar „extern“
					// ist unveränderlich — für alle, auch Owner und Admin. Löschen
					// bleibt (mit Verlaufseintrag); maßgeblich ist der Beleg im
					// Auftragsdatensatz.
					verstoesse = append(verstoesse, kommentarVerstoss{art: "extern-kommentar", cardID: cardID, commentID: b.id})
				case a.autorEmail != b.autorEmail:
					fremd(cardID, b.id) // Urheberschaft umgeschrieben
				case !s.eigen(b) && !s.darfFremde:
					fremd(cardID, b.id) // fremder Kommentar bearbeitet (F-1613)
				}
				continue
			}
			// Entfernt.
			if s.eigen(b) {
				if b.extern {
					geloescht = append(geloescht, KommentarGeloescht{BoardID: boardID, CardID: cardID, CardDisplayID: ka.displayID,
						CardTitle: ka.titel, CommentID: b.id, AutorEmail: b.autorEmail, AutorName: b.autor, Loescher: s.email,
						Textanfang: textanfang(b.text, 120), Extern: true, Eigen: true})
				}
				continue
			}
			if !s.darfFremde {
				fremd(cardID, b.id)
				continue
			}
			geloescht = append(geloescht, KommentarGeloescht{BoardID: boardID, CardID: cardID, CardDisplayID: ka.displayID,
				CardTitle: ka.titel, CommentID: b.id, AutorEmail: b.autorEmail, AutorName: b.autor, Loescher: s.email,
				Textanfang: textanfang(b.text, 120), Extern: b.extern})
		}
		for i, a := range kn.kommentare {
			if !offenNeu[i] {
				continue
			}
			switch {
			case a.schatten:
				verstoesse = append(verstoesse, kommentarVerstoss{art: "schattenschluessel", cardID: cardID, commentID: a.id})
			case a.extern:
				// Karte #686: Einen Antwortkommentar „extern“ legt nur der Server
				// an (Hintergrundauftrag der Schleuse) — sonst liesse sich eine
				// externe Antwort samt Kennzeichnung erfinden. Anders als bei
				// gewöhnlichen Kommentaren gilt die board-weite Kopie-Ausnahme
				// NICHT (Kritiker KS3-3): Eine Kopie an eine andere Karte stünde
				// unter dem Namen der Person an einer Karte, für die sie nie
				// angefragt hat. Was die Karte schon trug, ist oben als
				// unverändert gepaart; ein ZUSÄTZLICHER ist nie legitim — auch
				// nicht als Doppel an derselben Karte.
				verstoesse = append(verstoesse, kommentarVerstoss{art: "extern-kommentar", cardID: cardID, commentID: a.id})
			case !s.neuOK(a) && !(neueKarte && alt.istKopie(a.kanon)):
				// KF-4: Die Kopie-Ausnahme gilt nur für eine neue Karte (Karte
				// kopieren) — an einer bestehenden stünde der Satz unter dem
				// Namen einer Person, die ihn dort nie geschrieben hat.
				fremd(cardID, a.id) // neuer Kommentar unter fremdem Namen
			}
		}
	}
	if len(verstoesse) > 0 {
		return verstoesse, nil
	}
	return nil, geloescht
}

// pruefeKommentarAnhaenge (KF-3, Sprint-82-Abschluss): Eine Datei an einem
// Kommentar gehört zu ihm (ADR-0135) — sie zu entfernen oder umzuhängen ist
// Löschen bzw. Ändern des Kommentars und folgt derselben Regel (ADR-0136):
// eigener frei, fremder nur mit darfFremde. Ebenso darf niemand eine Datei
// unter einen fremden Kommentar hängen: Sie stünde unter dessen Namen. Ein
// fremder Kommentar, der selbst (mit Recht) gelöscht wurde, nimmt seine
// Dateien mit — das prüft die Kommentarregel oben.
func pruefeKommentarAnhaenge(cardID string, ka, kn *karteBestand, s kommentarSchreiber, neueKarte bool) []kommentarVerstoss {
	if s.darfFremde || bytes.Equal(ka.anhaengeRoh, kn.anhaengeRoh) {
		return nil
	}
	ka.parse()
	kn.parse()
	fremderKommentar := func(kb *karteBestand, kid string) bool {
		for _, k := range kb.kommentare {
			if k.id == kid {
				return !s.eigen(k)
			}
		}
		return false
	}
	var aus []kommentarVerstoss
	for aid, kid := range ka.anhaenge {
		if kn.anhaenge[aid] == kid || !fremderKommentar(ka, kid) {
			continue
		}
		// Datei des fremden Kommentars fehlt oder hängt jetzt woanders. Ist
		// der Kommentar selbst weg, meldet das schon die Kommentarregel.
		if fremderKommentar(kn, kid) {
			aus = append(aus, kommentarVerstoss{art: "fremder-kommentar", cardID: cardID, commentID: kid})
		}
	}
	// Eine neue Karte (Karte kopieren) trägt Kopien fremder Kommentare samt
	// ihrer Dateien; ob eine solche Kopie zulässig ist, regelt die
	// Kopie-Ausnahme (KF-4) — das Anheften wird dort nicht geprüft.
	for aid, kid := range kn.anhaenge {
		if neueKarte || ka.anhaenge[aid] == kid {
			continue
		}
		if fremderKommentar(kn, kid) {
			aus = append(aus, kommentarVerstoss{art: "fremder-kommentar", cardID: cardID, commentID: kid})
		}
	}
	return aus
}

// kommentarVerstossProtokoll übersetzt Verstöße in Protokolleinträge des
// Ops-Wegs. Der Vergleich weiss nicht, welche Op es war; genannt werden alle
// Ops, die die Karte berühren (sonst alle des Batches). Der Browser braucht
// den Op-Bezug (index): Ohne ihn fiele er auf den Ganz-Board-PUT zurück
// (storage-plugin.js, speichereUeberOps).
func kommentarVerstossProtokoll(verstoesse []kommentarVerstoss, ops []serverOp) []map[string]any {
	var aus []map[string]any
	for _, v := range verstoesse {
		var betroffen []serverOp
		if v.cardID != "" {
			for _, op := range ops {
				if opBetrifftKarte(op, v.cardID) {
					betroffen = append(betroffen, op)
				}
			}
		}
		if len(betroffen) == 0 {
			betroffen = ops
		}
		for _, op := range betroffen {
			e := map[string]any{"art": v.art, "op": op.ID, "typ": op.Type}
			if v.cardID != "" {
				e["cardId"] = v.cardID
			}
			if v.commentID != "" {
				e["commentId"] = v.commentID
			}
			aus = append(aus, e)
		}
	}
	return aus
}

// opBetrifftKarte: nennt die Op die Karte (payload.cardId oder
// payload.card.id, exakte Schlüssel)?
func opBetrifftKarte(op serverOp, cardID string) bool {
	var p map[string]json.RawMessage
	if json.Unmarshal(op.Payload, &p) != nil {
		return false
	}
	var id string
	if json.Unmarshal(p["cardId"], &id) == nil && id == cardID {
		return true
	}
	var karte map[string]json.RawMessage
	if json.Unmarshal(p["card"], &karte) == nil && json.Unmarshal(karte["id"], &id) == nil && id == cardID {
		return true
	}
	return false
}

// kommentarVerstossMeldung: Fehlertext des PUT (wie FindingVerstossMeldung).
func kommentarVerstossMeldung(v []kommentarVerstoss) string {
	const max = 5
	t := make([]string, 0, max)
	for i, x := range v {
		if i == max {
			break
		}
		t = append(t, x.String())
	}
	grund := "comments of other people (or files attached to them) would be removed or altered; only their author or the board owner may do that"
	for _, x := range v {
		if strings.HasPrefix(x.art, "verlauf-") {
			// F-1621: Der Verlauf (activity) ist ein Protokoll.
			grund = "the activity log of a card would lose entries, or gain entries that only the server may write"
			break
		}
		if x.art == "extern-kommentar" {
			// Karte #686: Antwort eines externen Modells (ADR-0134 §3.6).
			grund = "a reply of an external model (comment kind \"extern\") cannot be changed or created by clients — it may only be deleted"
			break
		}
		if x.art == "karte-doppelt" {
			// KF-1/VR2-8: eine Karten-id käme öfter vor als vorher.
			grund = "a card id would appear more often than before (duplicate card)"
			break
		}
	}
	s := grund + "; nothing written: " + strings.Join(t, "; ")
	if len(v) > max {
		s += fmt.Sprintf("; … and %d more", len(v)-max)
	}
	return s
}

// ── Stempel in Kommentarlisten ────────────────────────────────────────────

// stempleKommentarListen setzt in card.upsert, card.field und card.fieldIf
// auf `comments` den authorEmail jedes Kommentars selbst: den Wert aus dem
// Stand, wenn es die id dort gibt (irgendwo im Board), sonst die E-Mail des
// Schreibers. Ein mitgeschickter Wert (jede Schreibweise) zählt nie. Das ist
// Komfort für den Browser, der seine Liste mit allen Feldern zurückschickt —
// VERBINDLICH ist vergleicheKommentare am Ergebnis. Nur für Menschen.
func stempleKommentarListen(bestand *kommentarBestand, okBestand bool, ops []serverOp, email string) []serverOp {
	if email == "" || !okBestand || !opsSetzenFeld(ops, "comments") {
		return ops // nichts zu stempeln; Agent/Legacy; unlesbar: der Vergleich lehnt ab
	}
	liste := kommentarListenStempler(bestand, email)
	aus := make([]serverOp, 0, len(ops))
	for _, op := range ops {
		var p map[string]json.RawMessage
		if json.Unmarshal(op.Payload, &p) == nil && p != nil {
			switch op.Type {
			case "card.upsert":
				var karte map[string]json.RawMessage
				if json.Unmarshal(p["card"], &karte) == nil && karte != nil {
					var cardID string
					_ = json.Unmarshal(karte["id"], &cardID)
					if roh, mit := karte["comments"]; mit {
						if neu, ok := liste(cardID, roh); ok {
							karte["comments"] = neu
							p["card"], _ = json.Marshal(karte)
							op.Payload, _ = json.Marshal(p)
						}
					}
				}
			case "card.field", "card.fieldIf":
				var feld, cardID string
				_ = json.Unmarshal(p["field"], &feld)
				_ = json.Unmarshal(p["cardId"], &cardID)
				if roh, mit := p["value"]; mit && feld == "comments" {
					if neu, ok := liste(cardID, roh); ok {
						p["value"] = neu
						op.Payload, _ = json.Marshal(p)
					}
				}
			}
		}
		aus = append(aus, op)
	}
	return aus
}

// kommentarListenStempler liefert die Stempel-Funktion für eine
// Kommentarliste einer Karte (siehe stempleKommentarListen). Die Aufrufer
// prüfen vorher, ob jemand zu stempeln ist und der Stand lesbar war.
func kommentarListenStempler(bestand *kommentarBestand, email string) func(cardID string, roh json.RawMessage) (json.RawMessage, bool) {
	// autorVon: zuerst an derselben Karte (eine id kann an zwei Karten
	// stehen), dann board-weit (Kopie an eine neue Karte).
	autorVon := func(cardID, id string) (string, bool) {
		if kb := bestand.karten[cardID]; kb != nil {
			kb.parse()
			for _, k := range kb.kommentare {
				if k.id == id {
					return k.autorEmail, true
				}
			}
		}
		bestand.alleGeparst()
		a, ok := bestand.autorJeID[id]
		return a, ok
	}
	liste := func(cardID string, roh json.RawMessage) (json.RawMessage, bool) {
		var ks []map[string]json.RawMessage
		if json.Unmarshal(roh, &ks) != nil {
			return nil, false
		}
		for _, c := range ks {
			if c == nil {
				continue
			}
			for k := range c {
				if strings.EqualFold(k, AutorEmailFeld) {
					delete(c, k)
				}
			}
			var id string
			_ = json.Unmarshal(c["id"], &id)
			autor, bekannt := autorVon(cardID, id)
			if !bekannt {
				autor = email
			}
			if autor != "" {
				c[AutorEmailFeld], _ = json.Marshal(autor)
			}
		}
		neu, err := json.Marshal(ks)
		return neu, err == nil
	}
	return liste
}

// stempleBoardKommentare bereitet einen ganzen Board-Zustand für den
// Ganz-Board-PUT vor: Kommentarlisten stempeln (Verify VB-1) und den Verlauf
// jeder Karte zusammenführen (F-1621, verlauf_schutz.go). Ohne Stempel trüge
// ein im Browser neu geschriebener Kommentar keinen authorEmail, und
// vergleicheKommentare lehnte jeden PUT mit einem neuen Kommentar ab — auch
// für Owner/Admin (Import, Rückfall bei zu großen Ops). Bekannte ids behalten
// ihren Stempel, neue bekommen den des Schreibers. Nicht lesbar: unverändert
// (der Vergleich entscheidet).
func stempleBoardKommentare(bestand *kommentarBestand, okBestand bool, neu []byte, email string) []byte {
	if !okBestand {
		return neu // der Vergleich entscheidet
	}
	var liste func(cardID string, roh json.RawMessage) (json.RawMessage, bool)
	if email != "" {
		liste = kommentarListenStempler(bestand, email)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(neu, &top) != nil || top == nil {
		return neu
	}
	var board map[string]json.RawMessage
	if json.Unmarshal(top["board"], &board) != nil || board == nil {
		return neu
	}
	var sprints []map[string]json.RawMessage
	if json.Unmarshal(board["sprints"], &sprints) != nil {
		return neu
	}
	geaendert := false
	for _, sp := range sprints {
		var karten []map[string]json.RawMessage
		if sp == nil || json.Unmarshal(sp["cards"], &karten) != nil {
			continue
		}
		kartenNeu := false
		for _, k := range karten {
			if k == nil {
				continue
			}
			var cardID string
			_ = json.Unmarshal(k["id"], &cardID)
			if roh, mit := k["comments"]; mit && liste != nil {
				if gestempelt, ok := liste(cardID, roh); ok && string(gestempelt) != string(roh) {
					k["comments"] = gestempelt
					kartenNeu = true
				}
			}
			if roh, mit := k["activity"]; mit {
				if zus, ok := verlaufZusammen(bestand, cardID, roh); ok && string(zus) != string(roh) {
					k["activity"] = zus
					kartenNeu = true
				}
			}
		}
		if kartenNeu {
			sp["cards"], _ = json.Marshal(karten)
			geaendert = true
		}
	}
	if !geaendert {
		return neu
	}
	board["sprints"], _ = json.Marshal(sprints)
	top["board"], _ = json.Marshal(board)
	aus, err := json.Marshal(top)
	if err != nil {
		return neu
	}
	return aus
}

// ── Verlauf ───────────────────────────────────────────────────────────────

// kommentarVerlauf baut die Verlaufseinträge „Kommentar von X gelöscht“ für
// die Fold-Option `verlauf` (fold.go): angehängt nach der LETZTEN Op des
// Batches — die Karte gibt es dann sicher (vergleicheKommentare nennt nur
// Kommentare an Karten, die es danach noch gibt).
func kommentarVerlauf(geloescht []KommentarGeloescht, letzteOp string, jetzt time.Time) map[string][]map[string]any {
	if len(geloescht) == 0 {
		return nil
	}
	var vl []map[string]any
	for _, g := range geloescht {
		wessen := g.AutorEmail
		if wessen == "" {
			wessen = g.AutorName
		}
		aktion, anfang := "comment-deleted-by-owner", g.Textanfang
		if g.Extern {
			// Karte #686: Löschen nur mit Protokoll — aber ohne Textanfang
			// (Kritiker KS3-4): Der Verlauf ist unveränderlich, und die Antwort
			// ist re-identifiziert. Wer sie wegen Personenbezug löscht, darf
			// keine 120 Zeichen davon dauerhaft zurückbehalten.
			aktion, anfang = "schleuse-kommentar-geloescht", ""
		}
		eintrag := map[string]any{"id": uuid.NewString(), "author": g.Loescher, "action": aktion,
			"from": wessen, "to": anfang, "timestamp": jetzt.UTC().Format(time.RFC3339)}
		vl = append(vl, map[string]any{"cardId": g.CardID, "item": eintrag})
	}
	return map[string][]map[string]any{letzteOp: vl}
}

// mitVerlauf hängt die Verlaufseinträge als Option `verlauf` an die
// Fold-Optionen (fold.go, __falteMitZielen).
func mitVerlauf(optJSON string, verlauf map[string][]map[string]any) string {
	if len(verlauf) == 0 {
		return optJSON
	}
	opt := map[string]any{}
	if optJSON != "" {
		_ = json.Unmarshal([]byte(optJSON), &opt)
	}
	opt["verlauf"] = verlauf
	aus, err := json.Marshal(opt)
	if err != nil {
		return optJSON
	}
	return string(aus)
}

// textanfang kürzt auf höchstens n Zeichen (Runen), mit „…“.
func textanfang(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
