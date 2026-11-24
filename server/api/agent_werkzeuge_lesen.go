package api

// agent_werkzeuge_lesen.go — Lese-Werkzeuge aus ADR-0139 Stufe 4a (Karte #755):
// karten_suchen, board_verlauf, mitglieder, anhang_lesen. Dazu der Stand einer
// Karte (`stand`), mit dem schreibende Werkzeuge Freitext nur ändern, wenn er
// seit dem Lesen gleich geblieben ist.
//
// Wie bei allen Werkzeugen: Rechte entscheidet allein werkzeugDarf, jede
// Antwort ist begrenzt (Bytes) und gebremst (werkzeugLesenErlaubt), und
// Inhalte von Menschen gehen nur als JSON-Werte hinaus (sicherheitshinweis).
// E-Mail-Adressen bekommt nur ein Token, dessen Person am Board owner ist
// (ADR-0139 §7); allen anderen zeigen die Werkzeuge Namen.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// kartenSuchenMaxBytes / verlaufMaxBytes: wie board_lesen.
	kartenSuchenMaxBytes = 24 << 10
	verlaufMaxBytes      = 24 << 10
	// kartenSuchenMinZeichen: kürzere Suchbegriffe träfen fast alles.
	kartenSuchenMinZeichen = 2
	// verlaufStandard / verlaufMax: Einträge je Abruf.
	verlaufStandard = 30
	verlaufMax      = 100
	// verlaufMaxWert: Zeichen je Vorher-/Nachher-Wert im Verlauf.
	verlaufMaxWert = 200
	// anhangLesenMaxBytes: Inhalt nur für kleine Text-Anhänge.
	anhangLesenMaxBytes = 32 << 10
	// anhangListeMax: höchstens so viele Anhänge je Antwort (K-6).
	anhangListeMax = 100
)

// wkStand: Kennung des Freitext-Stands einer Karte (Titel, Beschreibung,
// Agent-Kontext). karte_zeigen nennt ihn, karte_aendern und
// checkliste_abhaken verlangen ihn, wenn sie Freitext ändern: Ändert ein
// Mensch die Karte zwischen Lesen und Schreiben, passt der Stand nicht mehr
// (ADR-0139, Werkzeugliste: „Freitext nur, wenn seit dem Lesen gleich“).
func wkStand(k *wkKarte) string {
	h := sha256.New()
	for _, f := range []string{"title", "description", "agentContext"} {
		s := zeichenketteVon(k.Roh[f])
		fmt.Fprintf(h, "%d:%s\x00", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// werkzeugErlaubteBoards: die Boards, auf denen dieser Principal ohne Board
// im Pfad lesen darf, mit wirksamer Rolle — dieselbe Regel wie das Werkzeug
// `boards` (delegierend: ACL der Person gekappt; mehrere Boards: Liste ∩
// persönliche ACL; sonst das eigene Board). Das Service-Board nie.
func (a *BoardsAPI) werkzeugErlaubteBoards(ctx context.Context, p *AgentPrincipal) (map[string]string, error) {
	rollen := map[string]string{}
	switch {
	case p.Delegiert:
		if a.acl == nil {
			return nil, errors.New("board list not available (no acl)")
		}
		eintraege, err := a.acl.ListBoardsWithRoleForUser(ctx, p.OnBehalfOf)
		if err != nil {
			return nil, err
		}
		for _, e := range eintraege {
			if r := storage.KappeAgentRolle(p.tokenRolle, e.Role); r != "" {
				rollen[e.BoardID] = r
			}
		}
	case p.MehrBoard:
		for b, r := range p.boardRollen {
			rollen[b] = r
		}
	case p.BoardID != "":
		rollen[p.BoardID] = p.Role
	}
	delete(rollen, storage.ServiceBoardID)
	return rollen, nil
}

// personIstOwner: Ist die Person hinter dem Token am Board owner? Projekt-
// Tokens haben keine Person → nein. Grundlage für die Herausgabe von
// E-Mail-Adressen (ADR-0139 §7).
func (a *BoardsAPI) personIstOwner(ctx context.Context, p *AgentPrincipal, boardID string) bool {
	if p.OnBehalfOf == "" || a.acl == nil {
		return false
	}
	rolle, gefunden, err := a.acl.GetRole(ctx, boardID, p.OnBehalfOf)
	return err == nil && gefunden && rolle == storage.RoleOwner
}

// personAnzeige: wie eine Person in einer Werkzeug-Antwort erscheint. Mit
// mitMail die Adresse, sonst der Name aus dem Konto oder „(Person)“ — nie
// der Teil vor dem @, der verriete die Adresse halb.
func (a *BoardsAPI) personAnzeige(ctx context.Context, wer string, mitMail bool, namen map[string]string) string {
	if wer == "" || !strings.Contains(wer, "@") {
		return wer
	}
	if mitMail {
		return wer
	}
	if n, ok := namen[wer]; ok {
		return n
	}
	name := ""
	if a.users != nil {
		if u, err := a.users.GetByEmail(ctx, wer); err == nil && u != nil {
			name = strings.TrimSpace(u.Name)
		}
	}
	if name == "" || strings.Contains(name, "@") {
		name = "(Person)"
	}
	namen[wer] = name
	return name
}

// schreibeMitHinweis schreibt eine Lese-Antwort mit `sicherheitshinweis` als
// ERSTEM Feld (V1-3): Das Modell liest ihn vor jedem Fremdtext. Eine Map
// ordnete encoding/json alphabetisch — dann stünden Verlauf, Namen oder der
// Inhalt eines Anhangs vor dem Hinweis (Kritiker #755 Teil 1, K-2).
func schreibeMitHinweis(w http.ResponseWriter, m map[string]any) {
	delete(m, "sicherheitshinweis")
	rest, err := json.Marshal(m)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode failed", err)
		return
	}
	hinweis, _ := json.Marshal(werkzeugFremdtextHinweis)
	aus := append([]byte(`{"sicherheitshinweis":`), hinweis...)
	if len(rest) > 2 {
		aus = append(append(aus, ','), rest[1:]...)
	} else {
		aus = append(aus, '}')
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(aus, '\n'))
}

// --- karten_suchen ------------------------------------------------------------

type wkSuchTreffer struct {
	BoardID   string  `json:"boardId"`
	Board     string  `json:"board"`
	DisplayID int     `json:"displayId"`
	Titel     string  `json:"titel"`
	Spalte    string  `json:"spalte"`
	Swimlane  string  `json:"swimlane"`
	Fundort   string  `json:"fundort"`
	Archiv    bool    `json:"archiviert,omitempty"`
	Link      *string `json:"link"`
}

// wkSchattenTreffer: ein Schatten-Treffer der Suche (Karte #789) — Board ist
// das EXTERNE Board, auf dem der Schatten liegt; kein Link.
type wkSchattenTreffer struct {
	BoardID string `json:"boardId"`
	Board   string `json:"board"`
	Fundort string `json:"fundort"`
	wkSchatten
}

// werkzeugKartenSuchen — GET /werkzeuge/karten?q=…[&boardId=…]: Volltext über
// Titel, Beschreibung, Labels und Owner der Karten aller Boards, die der
// Principal lesen darf (oder eines davon). Ohne Mail-Eingang und Sprint-Meta-
// Karten; Erledigt und Archiv nur auf Wunsch.
func (a *BoardsAPI) werkzeugKartenSuchen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	q := r.URL.Query()
	such := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(such) < kartenSuchenMinZeichen || utf8.RuneCountInString(such) > maxVorschlagTitel {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("q: Suchbegriff mit %d bis %d Zeichen", kartenSuchenMinZeichen, maxVorschlagTitel))
		return
	}
	nurBoard := strings.TrimSpace(q.Get("boardId"))
	mitErledigten, mitArchiv := wahrParameter(q.Get("mitErledigten")), wahrParameter(q.Get("mitArchivierten"))
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	rollen, err := a.werkzeugErlaubteBoards(ctx, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board list failed", err)
		return
	}
	if nurBoard != "" {
		if _, ok := rollen[nurBoard]; !ok {
			writeError(w, http.StatusForbidden, verbotenMeldungFuer(p))
			return
		}
		rollen = map[string]string{nurBoard: rollen[nurBoard]}
	}
	ids := make([]string, 0, len(rollen))
	for id := range rollen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	klein := strings.ToLower(such)
	treffer := []wkSuchTreffer{}
	belegt, gekuerzt, anzahl := 0, false, 0
	// Karte #789 (ADR-0140 §7): auf der Seite außen auch Schatten, nur über
	// Titel (nicht vertraulich) oder Nummer i-<nr>. Auf aus/innen nil.
	var intern []wkSchattenTreffer
	// Kritiker #789, K-4: eigenes Budget wie in board_lesen — Schatten
	// verdrängen keine externen Treffer und umgekehrt.
	internAnzahl, internBelegt, internGekuerzt, suchNr := 0, 0, false, schattenNummer(such)
	for _, id := range ids {
		b, err := a.repo.Get(ctx, id)
		if err != nil {
			continue // gelöscht zwischen ACL und Lesen: kein Treffer
		}
		wb, err := leseWkBoard(b.StateJSON)
		if err != nil {
			continue
		}
		titel := wb.Titel
		if titel == "" {
			titel = b.Title
		}
		schattenListe, paarAktiv, err := a.werkzeugSchattenLesen(ctx, id)
		if err != nil {
			schattenLeseFehler(w, err)
			return
		}
		if paarAktiv && intern == nil {
			intern = []wkSchattenTreffer{}
		}
		for _, sch := range schattenListe {
			fundort := ""
			switch {
			case suchNr > 0 && sch.nr == suchNr && sch.Art == "karte":
				fundort = "nummer"
			case !sch.vertraulich && strings.Contains(strings.ToLower(sch.Titel), klein):
				fundort = "titel"
			}
			if fundort == "" {
				continue
			}
			internAnzahl++
			t := wkSchattenTreffer{BoardID: id, Board: titel, Fundort: fundort, wkSchatten: sch}
			roh, _ := json.Marshal(t)
			if internBelegt+len(roh) > boardLesenMaxSchattenBytes {
				internGekuerzt = true
				continue
			}
			internBelegt += len(roh)
			intern = append(intern, t)
		}
		for i := range wb.Swimlanes {
			lane := &wb.Swimlanes[i]
			if lane.Kind == "service" || (lane.Archiviert && !mitArchiv) {
				continue
			}
			for j := range lane.Karten {
				k := &lane.Karten[j]
				if k.Kind == "sprint-meta" || (k.Archiviert && !mitArchiv) || (k.Spalte == "col-erledigt" && !mitErledigten) {
					continue
				}
				fundort := ""
				switch {
				case strings.Contains(strings.ToLower(k.Titel), klein):
					fundort = "titel"
				case strings.Contains(strings.ToLower(zeichenketteVon(k.Roh["description"])), klein):
					fundort = "beschreibung"
				case strings.Contains(strings.ToLower(k.Owner), klein):
					fundort = "owner"
				default:
					for _, l := range zeichenkettenListe(k.Roh["labels"]) {
						if strings.Contains(strings.ToLower(l), klein) {
							fundort = "label"
							break
						}
					}
				}
				if fundort == "" {
					continue
				}
				anzahl++
				t := wkSuchTreffer{BoardID: id, Board: titel, DisplayID: k.DisplayID, Titel: kuerze(k.Titel, boardLesenMaxTitel),
					Spalte: wb.spaltenTitel(k.Spalte), Swimlane: swimlaneName(lane), Fundort: fundort,
					Archiv: k.Archiviert || lane.Archiviert, Link: a.kartenLink(id, k.DisplayID)}
				roh, _ := json.Marshal(t)
				if belegt+len(roh) > kartenSuchenMaxBytes {
					gekuerzt = true
					continue
				}
				belegt += len(roh)
				treffer = append(treffer, t)
			}
		}
	}
	antwort := map[string]any{"suche": such, "treffer": treffer, "anzahl": anzahl, "gekuerzt": gekuerzt}
	if intern != nil {
		antwort["intern"], antwort["internAnzahl"] = intern, internAnzahl
		if internGekuerzt {
			antwort["internGekuerzt"] = true
			antwort["internHinweis"] = fmt.Sprintf("Zu viele interne Treffer: %d von %d gezeigt. %s", len(intern), internAnzahl, sucheEingrenzen(p))
		}
	}
	if gekuerzt {
		antwort["hinweis"] = fmt.Sprintf("Zu viele Treffer: %d von %d gezeigt. %s", len(treffer), anzahl, sucheEingrenzen(p))
	}
	schreibeMitHinweis(w, antwort)
}

// --- board_verlauf ------------------------------------------------------------

type wkVerlaufZeile struct {
	Zeit    string `json:"zeit"`
	Wer     string `json:"wer"`
	Fuer    string `json:"fuer,omitempty"`
	Art     string `json:"art"`
	Karte   *int   `json:"karte,omitempty"`
	Feld    string `json:"feld,omitempty"`
	Vorher  string `json:"vorher,omitempty"`
	Nachher string `json:"nachher,omitempty"`
}

// werkzeugBoardVerlauf — GET /werkzeuge/boards/{boardId}/verlauf: die
// jüngsten Einträge des Protokolls, auf Wunsch einer Karte. Wer ein Mensch
// war, nennt die Antwort mit Namen; die Adresse nur, wenn die Person hinter
// dem Token owner ist.
func (a *BoardsAPI) werkzeugBoardVerlauf(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	boardID := r.PathValue("boardId")
	q := r.URL.Query()
	anzahl := verlaufStandard
	if v := strings.TrimSpace(q.Get("anzahl")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > verlaufMax {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("anzahl: 1 bis %d", verlaufMax))
			return
		}
		anzahl = n
	}
	if a.activities == nil {
		writeError(w, http.StatusServiceUnavailable, "Verlauf ist auf diesem Server nicht eingerichtet.")
		return
	}
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	kartenID := ""
	if ref := strings.TrimSpace(q.Get("karte")); ref != "" {
		k, _ := wb.findeWerkzeugKarte(w, ref)
		if k == nil {
			return
		}
		kartenID = k.ID
	}
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	mitMail := a.personIstOwner(ctx, p, boardID)
	// Kritiker #755 Teil 1, K-3: Freigabe-Einträge (acl-*) gibt es nur für
	// eine owner-Person — wie der Verlauf im Browser (F-261, ListGefiltert).
	// Karte #783 (Kritik K-A4): Einträge zu Vorschlägen sieht kein Agent —
	// ListGefiltert blendet sie aus.
	eintraege, err := a.activities.ListGefiltert(ctx, boardID, kartenID, anzahl, !mitMail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history read failed", err)
		return
	}
	// Nummern der sichtbaren Karten. Verborgen bleiben Karten des Mail-
	// Eingangs und archivierter Swimlanes (wie in board_lesen) — und Karten,
	// die es nicht mehr gibt: Von denen weiß das Werkzeug nicht, wo sie lagen
	// (K-4). Ihr Löschen selbst (card.delete) bleibt sichtbar.
	nummern, verborgen := map[string]int{}, map[string]bool{}
	for i := range wb.Swimlanes {
		for _, k := range wb.Swimlanes[i].Karten {
			nummern[k.ID] = k.DisplayID
			if !swimlaneSichtbar(&wb.Swimlanes[i]) {
				verborgen[k.ID] = true
			}
		}
	}
	namen := map[string]string{}
	zeilen := []wkVerlaufZeile{}
	belegt, gekuerzt := 0, false
	for _, e := range eintraege {
		if e.CardID != "" {
			if _, bekannt := nummern[e.CardID]; verborgen[e.CardID] || (!bekannt && e.OpType != "card.delete") {
				continue
			}
		}
		z := wkVerlaufZeile{Zeit: e.TS, Wer: a.personAnzeige(ctx, e.Actor, mitMail, namen), Art: e.Action,
			Fuer: a.personAnzeige(ctx, e.OnBehalfOf, mitMail, namen), Feld: e.Field,
			Vorher: kuerze(e.FromVal, verlaufMaxWert), Nachher: kuerze(e.ToVal, verlaufMaxWert)}
		if e.OpType != "" {
			z.Art = e.OpType
		}
		if n, ok := nummern[e.CardID]; ok && n > 0 {
			n := n
			z.Karte = &n
		}
		roh, _ := json.Marshal(z)
		if belegt+len(roh) > verlaufMaxBytes {
			gekuerzt = true
			break
		}
		belegt += len(roh)
		zeilen = append(zeilen, z)
	}
	schreibeMitHinweis(w, map[string]any{"boardId": boardID, "board": wb.Titel, "eintraege": zeilen, "gekuerzt": gekuerzt})
}

// --- mitglieder ---------------------------------------------------------------

type wkMitglied struct {
	Name  string `json:"name"`
	Rolle string `json:"rolle"`
	Email string `json:"email,omitempty"`
}

// werkzeugMitglieder — GET /werkzeuge/boards/{boardId}/mitglieder: wer am
// Board welche Rolle hat, damit Owner und Beteiligte richtig gesetzt werden.
// Adressen und Domain-Freigaben nur für ein Token, dessen Person owner ist.
func (a *BoardsAPI) werkzeugMitglieder(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	boardID := r.PathValue("boardId")
	if a.acl == nil {
		writeError(w, http.StatusServiceUnavailable, "Mitglieder sind auf diesem Server nicht eingerichtet.")
		return
	}
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	eintraege, err := a.acl.ListByBoard(ctx, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "member list failed", err)
		return
	}
	mitMail := a.personIstOwner(ctx, p, boardID)
	namen := map[string]string{}
	liste := []wkMitglied{}
	for _, e := range eintraege {
		mail := storage.NormalizeEmail(e.UserEmail)
		if storage.IstDomainSubjekt(mail) {
			if mitMail {
				liste = append(liste, wkMitglied{Name: "alle mit " + mail, Rolle: e.Role, Email: mail})
			}
			continue
		}
		m := wkMitglied{Name: a.personAnzeige(ctx, mail, false, namen), Rolle: e.Role}
		if mitMail {
			m.Email = mail
		}
		liste = append(liste, m)
	}
	sort.SliceStable(liste, func(i, j int) bool { return strings.ToLower(liste[i].Name) < strings.ToLower(liste[j].Name) })
	antwort := map[string]any{"boardId": boardID, "mitglieder": liste}
	if !mitMail {
		antwort["hinweis"] = "E-Mail-Adressen und Domain-Freigaben sieht nur ein Agent, dessen Person owner dieses Boards ist."
	}
	schreibeMitHinweis(w, antwort)
}

// --- anhang_lesen -------------------------------------------------------------

type wkAnhang struct {
	ID        string `json:"id"`
	Dateiname string `json:"dateiname"`
	Mime      string `json:"mime,omitempty"`
	Groesse   int    `json:"groesse"`
	Angelegt  string `json:"angelegt,omitempty"`
}

// anhangIstText: Inhalte gibt das Werkzeug nur für Text heraus.
func anhangIstText(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	switch mime {
	case "application/json", "application/xml", "application/x-yaml", "application/yaml":
		return true
	}
	return strings.HasPrefix(mime, "text/")
}

// werkzeugAnhangLesen — GET /werkzeuge/boards/{boardId}/karten/{karte}/anhaenge
// [?anhang=<id>]: die Anhänge einer Karte (Metadaten); mit anhang zusätzlich
// der Inhalt, wenn es ein kleiner Text-Anhang ist. Hochladen gibt es über die
// Werkzeuge nicht (ADR-0139 §1).
func (a *BoardsAPI) werkzeugAnhangLesen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	boardID := r.PathValue("boardId")
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	k, _ := wb.findeWerkzeugKarte(w, r.PathValue("karte"))
	if k == nil {
		return
	}
	var roh []map[string]json.RawMessage
	_ = json.Unmarshal(k.Roh["attachments"], &roh)
	// alle: jeder Anhang der Karte; liste: was die Antwort zeigt (K-6,
	// begrenzt). Gewählt wird aus allen — sonst wäre ab dem 101. Anhang ein
	// vorhandener „nicht da“ (Verify, V-1).
	alle := []wkAnhang{}
	for _, m := range roh {
		if m == nil || zeichenketteVon(m["id"]) == "" {
			continue
		}
		alle = append(alle, wkAnhang{ID: zeichenketteVon(m["id"]), Dateiname: kuerze(zeichenketteVon(m["filename"]), maxVorschlagTitel),
			Mime: zeichenketteVon(m["mime"]), Groesse: wkZahl(m["size"]), Angelegt: zeichenketteVon(m["created"])})
	}
	liste, gekuerzt := alle, false
	if len(liste) > anhangListeMax {
		liste, gekuerzt = alle[:anhangListeMax], true
	}
	antwort := map[string]any{"boardId": boardID, "displayId": k.DisplayID,
		"anhaenge": liste, "link": a.kartenLink(boardID, k.DisplayID)}
	if gekuerzt {
		antwort["gekuerzt"] = true
	}
	wunsch := strings.TrimSpace(r.URL.Query().Get("anhang"))
	if wunsch == "" {
		schreibeMitHinweis(w, antwort)
		return
	}
	var gewaehlt *wkAnhang
	for i := range alle {
		if alle[i].ID == wunsch {
			gewaehlt = &alle[i]
		}
	}
	if gewaehlt == nil {
		writeError(w, http.StatusNotFound, "Anhang "+strconv.Quote(wunsch)+" gibt es an dieser Karte nicht. Die ids nennt anhang_lesen ohne anhang.")
		return
	}
	antwort["anhang"] = gewaehlt
	switch {
	case !anhangIstText(gewaehlt.Mime):
		antwort["hinweis"] = "Kein Text-Anhang — der Inhalt ist nur im Board einsehbar."
	case a.attach == nil:
		antwort["hinweis"] = "Inhalte von Anhängen liest dieser Server über die Werkzeuge nicht."
	default:
		datei, _, err := a.attach.Open(boardID, k.ID, gewaehlt.ID)
		if err != nil {
			antwort["hinweis"] = "Die Datei des Anhangs ist nicht lesbar (fehlt oder wurde entfernt)."
			break
		}
		inhalt, err := io.ReadAll(io.LimitReader(datei, anhangLesenMaxBytes+1))
		_ = datei.Close()
		switch {
		case err != nil:
			antwort["hinweis"] = "Die Datei des Anhangs ist nicht lesbar."
		case len(inhalt) > anhangLesenMaxBytes:
			antwort["hinweis"] = fmt.Sprintf("Zu groß für die Werkzeuge (höchstens %d KiB) — im Board öffnen.", anhangLesenMaxBytes>>10)
		case !utf8.Valid(inhalt):
			antwort["hinweis"] = "Kein gültiger UTF-8-Text — im Board öffnen."
		default:
			antwort["inhalt"] = string(inhalt)
		}
	}
	schreibeMitHinweis(w, antwort)
}
