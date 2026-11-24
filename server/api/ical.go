package api

// ical.go — iCal-Subscription-Verwaltung + Feed-Endpoint (#308 Etappe 6,
// ADR-0103 §iCal-Export).
//
// Architektur-Verfeinerung gegenüber dem ADR-Wortlaut (begründet im ADR):
// Externe Kalender-Apps (Apple/Outlook/Google) können kein Forward-Auth →
// der Feed authentifiziert allein über einen Subscription-Token in der URL.
// Damit der ACLGuard (greift auf /boards/*) den token-authentifizierten Feed
// nicht blockt, liegt er unter /ical/* — außerhalb von /boards/*. Die
// Token-Verwaltung dagegen läuft unter /me/ical/* (Forward-Auth-Identität).
//
//	GET    /me/ical/subscriptions          — eigene Abos listen (mit Feed-URL)
//	POST   /me/ical/subscriptions          — Abo anlegen ({board_id?} optional)
//	DELETE /me/ical/subscriptions/{id}      — Abo widerrufen (own-only)
//	GET    /ical/calendar.ics?token=<t>     — Feed (token-auth, text/calendar)
//
// Karte #782 (ADR-0140 §6): Auf der Seite außen (boardpaar.seite) enthält der
// Feed zusätzlich die Schatten mit Datum der gekoppelten Boards; auf der Seite
// innen lassen sich keine neuen Abos anlegen. aus/innen: Feed unverändert.
//
// Die Außenstelle (ADR-0138 §6: Abo „für außen“, /ical/aussen/*, Token
// kalender-ausgang) ist mit Karte #795 zurückgebaut (ADR-0140 §11).
// Bestand-Abos „für außen“ öffnen keinen Feed und lassen sich nur löschen.
//
// Token-Scope: board_id gesetzt → genau dieses Board (Lese-ACL zur Feed-Zeit
// erzwungen); board_id leer → Cross-Board (alle Boards mit Lese-Zugriff).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Andiministrator/aBoard/server/calendar"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// ICalAPI bündelt die iCal-Endpoints. tokens darf nil sein (kein Master-Key) —
// dann antworten alle Endpoints 503.
type ICalAPI struct {
	tokens *storage.ICalTokenRepo
	users  *storage.UserRepo
	boards *storage.BoardRepo
	acl    *storage.AclRepo
	// F-418/F-2109: Drossel am Feed. feedLimit zählt je gültigem Abo — der
	// Schlüssel ist nicht fälschbar, und Abonnenten hinter einem gemeinsamen
	// Proxy teilen sich keinen Topf. fehlLimit zählt Abrufe mit unbekanntem
	// Token je Adresse, nach der Adressregel der Agenten-Bremse (adresse).
	feedLimit *rateLimiter
	fehlLimit *rateLimiter
	adresse   func(*http.Request) string
	cfg       *storage.ConfigRepo // ADR-0138 §2: kalender.zeitzone; nil → Standard
	// Karte #782 (ADR-0140 §6): Kopplung und Schatten für den Feed der
	// Seite außen. Beide nil → der Feed bleibt, wie er war.
	boardpaar *storage.BoardpaarRepo
	schatten  *storage.SchattenRepo
}

// MitBoardpaar hängt Kopplung und Schatten an (Karte #782, ADR-0140 §6).
// Wirksam nur, solange die Instanz `boardpaar.seite = aussen` führt; die
// Seite wird je Abruf gelesen (§4), nicht beim Start.
func (a *ICalAPI) MitBoardpaar(bp *storage.BoardpaarRepo, sr *storage.SchattenRepo) *ICalAPI {
	a.boardpaar, a.schatten = bp, sr
	return a
}

// MitKonfig hängt den Konfig-Store an (Instanz-Zeitzone, ADR-0138 §2).
func (a *ICalAPI) MitKonfig(cfg *storage.ConfigRepo) *ICalAPI {
	a.cfg = cfg
	return a
}

func NewICalAPI(tokens *storage.ICalTokenRepo, users *storage.UserRepo, boards *storage.BoardRepo, acl *storage.AclRepo) *ICalAPI {
	return &ICalAPI{
		tokens:    tokens,
		users:     users,
		boards:    boards,
		acl:       acl,
		feedLimit: newRateLimiter(120, time.Hour), // 120 Feed-Abrufe/h je Abo
		fehlLimit: newRateLimiter(120, time.Hour), // 120 Fehlabrufe/h je Adresse
		adresse:   tcpAdresse,
	}
}

// MitAdressregel setzt die Adressregel für Abrufe mit unbekanntem Token — im
// Server die der Agenten-Bremse (AgentZugang.ClientAdresse, F-2109). Ohne
// Aufruf zählt die TCP-Gegenstelle.
func (a *ICalAPI) MitAdressregel(fn func(*http.Request) string) *ICalAPI {
	if fn != nil {
		a.adresse = fn
	}
	return a
}

func (a *ICalAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /me/ical/subscriptions", WithDefaults(http.HandlerFunc(a.list)))
	mux.Handle("POST /me/ical/subscriptions", WithDefaults(http.HandlerFunc(a.create)))
	mux.Handle("DELETE /me/ical/subscriptions/{id}", WithDefaults(http.HandlerFunc(a.delete)))
	mux.Handle("GET /ical/calendar.ics", WithDefaults(http.HandlerFunc(a.feed)))
}

// authUser löst die Forward-Auth-Identität zu einem provisionierten User auf.
// Liefert nil + bereits geschriebene Fehlerantwort, wenn nicht möglich.
func (a *ICalAPI) authUser(w http.ResponseWriter, r *http.Request, ctx context.Context) *storage.User {
	if a.tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "ical: no master key configured")
		return nil
	}
	id := IdentityFromContext(r.Context())
	if id == nil {
		writeError(w, http.StatusForbidden, "forbidden: authentication required")
		return nil
	}
	email := storage.NormalizeEmail(id.Email)
	if email == "" {
		writeError(w, http.StatusForbidden, "forbidden: no identity email")
		return nil
	}
	u, err := a.users.GetByEmail(ctx, email)
	if errors.Is(err, storage.ErrUserNotFound) {
		writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
		return nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user lookup failed", err)
		return nil
	}
	return u
}

type icalSubDTO struct {
	ID         string `json:"id"`
	BoardID    string `json:"board_id,omitempty"` // leer = Cross-Board
	Scope      string `json:"scope"`              // "board" | "cross-board"
	Path       string `json:"path"`               // relative Feed-URL (Frontend prependt origin); leer bei „für außen“
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	// Aussen: Bestand-Abo „für außen“ aus der Zeit der Außenstelle (#748,
	// zurückgebaut mit #795) — ohne Feed, nur noch zum Löschen.
	Aussen bool `json:"aussen,omitempty"`
	// Defekt: nicht entschlüsselbar — kein Link, nur Löschen (F-A, Sprint 88).
	Defekt bool `json:"defekt,omitempty"`
}

func toICalSubDTO(s storage.ICalSubscription) icalSubDTO {
	scope := "cross-board"
	boardID := ""
	if s.BoardID != nil {
		scope = "board"
		boardID = *s.BoardID
	}
	d := icalSubDTO{
		ID:         s.ID,
		BoardID:    boardID,
		Scope:      scope,
		Path:       "/ical/calendar.ics?token=" + s.Token,
		CreatedAt:  s.CreatedAt,
		LastUsedAt: s.LastUsedAt,
	}
	if s.Defekt {
		// Kein Feed-Link: das Token ist nicht lesbar. Die Zeile existiert
		// nur, damit der Besitzer sie löschen kann.
		d.Path, d.Defekt = "", true
		return d
	}
	if s.Aussen {
		// Bestand „für außen“: das Token öffnet keinen Feed (feed()), es gibt
		// nichts mehr anzuzeigen außer der Zeile zum Löschen (Karte #795).
		d.Path, d.Aussen = "", true
	}
	return d
}

func (a *ICalAPI) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	u := a.authUser(w, r, ctx)
	if u == nil {
		return
	}
	subs, err := a.tokens.ListForUser(ctx, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	out := make([]icalSubDTO, 0, len(subs))
	for _, s := range subs {
		out = append(out, toICalSubDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *ICalAPI) create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	u := a.authUser(w, r, ctx)
	if u == nil {
		return
	}
	var body struct {
		BoardID string `json:"board_id"`
		Aussen  bool   `json:"aussen"`
	}
	// Leerer Body = Cross-Board. Ein nicht lesbarer Body (etwa "aussen":"ja")
	// wird abgewiesen, statt still ein Abo mit vollem Feed anzulegen
	// (Sitzungs-QA C-4).
	roh, rerr := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	if rerr != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(bytes.TrimSpace(roh)) > 0 {
		if err := json.Unmarshal(roh, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
	}

	// Karte #795 (ADR-0140 §11): Abos „für außen“ gibt es nicht mehr. Wer es
	// trotzdem verlangt (alter Browser), bekommt eine klare Absage statt
	// still eines Abos mit vollem Feed.
	if body.Aussen {
		writeError(w, http.StatusGone, "external subscriptions are no longer offered")
		return
	}

	// Karte #782 (ADR-0140 §6): Auf der internen Seite gibt es keine neuen
	// Abos. Der interne Server steht nur im VPN, ein Kalender-Client käme
	// ohnehin nicht heran; die Termine stehen im Feed des externen Boards.
	// 409 statt 403: Die Person ist berechtigt, die Instanz steht nur in
	// einem Zustand, der das Anlegen nicht zulässt (403 liest der Browser
	// als fehlende Anmeldung). Bestehende Abos und ihr Feed bleiben gültig.
	if BoardpaarSeite(ctx, a.cfg) == BoardpaarSeiteInnen {
		writeError(w, http.StatusConflict, "Kalender abonnierst du auf dem externen Board")
		return
	}

	var boardID *string
	if body.BoardID != "" {
		// Board-spezifisch: der User muss zur Anlage-Zeit Lese-Zugriff haben.
		if _, found, err := a.acl.GetRole(ctx, body.BoardID, u.Email); err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		} else if !found {
			writeError(w, http.StatusForbidden, "forbidden: no access to board")
			return
		}
		bid := body.BoardID
		boardID = &bid
	}

	sub, err := a.tokens.Create(ctx, u.ID, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, toICalSubDTO(*sub))
}

func (a *ICalAPI) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	u := a.authUser(w, r, ctx)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	if err := a.tokens.Delete(ctx, u.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// feed — GET /ical/calendar.ics?token=<t>. Token-authentifiziert, außerhalb
// /boards/* (kein ACLGuard). Resolved den Token zu User+Scope, sammelt die
// Karten (board-spezifisch oder cross-board, ACL erzwungen) und liefert
// text/calendar.
func (a *ICalAPI) feed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if a.tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "ical: no master key configured")
		return
	}
	// F-418: Der Feed ist ohne Anmeldung erreichbar und löst je Abruf einen
	// Full-Board-Scan aus. Legitime Kalender-Apps pollen alle 15–60 min →
	// 120/h je Abo ist großzügig. F-2109: Bis dahin zählte die Drossel nach
	// dem ersten X-Forwarded-For-Eintrag, den der Client selbst setzt — ein
	// wechselnder Wert je Abruf umging sie. Jetzt zählt der teure Weg je Abo
	// (erst nach dem Nachschlagen des Tokens, einem reinen Index-Lesezugriff;
	// last_used_at schreibt Touch erst nach der Drossel) und der Fehlweg je
	// vertrauter Adresse.
	token := r.URL.Query().Get("token")
	sub, err := a.tokens.Resolve(ctx, token)
	if errors.Is(err, storage.ErrICalTokenNotFound) {
		if !a.fehlLimit.allow(a.adresse(r)) {
			writeError(w, http.StatusTooManyRequests, "rate limit: too many feed requests")
			return
		}
		// 404 statt 403 — keine Auskunft, ob ein Token je existierte.
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token resolve failed", err)
		return
	}
	if !a.feedLimit.allow(sub.ID) {
		writeError(w, http.StatusTooManyRequests, "rate limit: too many feed requests")
		return
	}
	a.tokens.Touch(ctx, sub.ID)
	if sub.Aussen {
		// Karte #748/#795: Ein Bestand-Abo „für außen“ war als datensparsamer
		// Feed gedacht — sein Token öffnet den vollen Feed nicht, auch nach dem
		// Rückbau der Außenstelle nicht. 404 wie ein unbekanntes Token.
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	permitted, scope, status, err := a.aboBoards(ctx, sub)
	if status != 0 {
		writeError(w, status, aboFehlerText(status), err)
		return
	}

	cards, err := collectCalendarCards(ctx, a.boards, permitted, "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "collect failed", err)
		return
	}

	// ADR-0138 §2: Uhrzeiten sind Ortszeit der Instanz. Ist die Zone nicht
	// auflösbar, rechnet der Feed in UTC; der Admin-Dialog meldet das.
	loc, _, zerr := KalenderZeitzone(ctx, a.cfg)
	if zerr != nil {
		log.Printf("ical: %v", zerr)
	}

	origin := requestOrigin(r)
	// Karte #782: Nur auf der Seite außen tragen eigene Einträge eine Art,
	// damit der Leser sie von den Schatten unterscheiden kann. Auf aus und
	// innen bleibt der Feed byte-gleich wie vor #782.
	aussen, serr := a.schattenFeedAktiv(ctx)
	if serr != nil {
		writeError(w, http.StatusServiceUnavailable, "board pair side unreadable", serr)
		return
	}
	events := make([]calendar.Event, 0, len(cards))
	for _, c := range cards {
		summary := c.Title
		if c.DisplayID > 0 {
			summary = "#" + strconv.Itoa(c.DisplayID) + " " + c.Title
		}
		// Lokale Variable bewusst NICHT `url` genannt — das wuerde das
		// gleichnamige Paket verdecken, dessen QueryEscape hier gebraucht wird.
		link := ""
		if origin != "" {
			// F-377-Klasse (beim #366-Fix nachgezogen): dieselbe Escape-Pflicht
			// wie in notifications/email.go. Die BoardID steht als Wert in einer
			// `key=value&key=value`-Kette; enthielte sie `&`, `=` oder `#`, haengte
			// sie weitere Parameter an oder schnitte das Fragment ab — der
			// Abonnent landete auf einem anderen Board. In der Praxis sind das
			// UUIDs aus der DB; der Escape kostet nichts und macht die Annahme
			// ueberfluessig. DisplayID ist ein int und braucht keinen.
			link = origin + "/#board=" + url.QueryEscape(c.BoardID) +
				"&card=" + strconv.Itoa(c.DisplayID)
		}
		events = append(events, calendar.Event{
			UID:         c.CardID + "@" + c.BoardID + ".aboard",
			Start:       firstNonEmpty(c.StartDate, c.DueDate),
			End:         c.DueDate,
			StartTime:   c.StartTime, // ohne startDate leer (collectCalendarCards)
			EndTime:     c.DueTime,
			Summary:     summary,
			Description: truncate(c.Description, 500),
			URL:         link,
			Art:         artEigen(aussen, ArtFeedKarte),
		})
	}

	// Karte #743 (ADR-0138 §4/§6): dazu die Termine ohne Karte derselben
	// Boards — Board-Abo die des Boards, Alle-Boards-Abo die aller Boards mit
	// Leserecht (dieselbe permitted-Menge wie die Karten).
	termine, err := collectTermine(ctx, a.boards, permitted, "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "collect failed", err)
		return
	}
	for _, t := range termine {
		ev := terminEvent(t, origin)
		ev.Art = artEigen(aussen, ArtFeedTermin)
		events = append(events, ev)
	}

	// Karte #782 (ADR-0140 §6): auf der Seite außen dazu die Schatten mit
	// Datum aller gekoppelten Boards der permitted-Menge.
	if aussen {
		schatten, err := a.schattenEvents(ctx, permitted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "collect failed", err)
			return
		}
		events = append(events, schatten...)
	}

	body := calendar.BuildICSInZone(calendar.FormatCalName(scope), time.Now(), loc, events)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="aboard.ics"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

// aboBoards bestimmt die Boards, die ein Abo zur ABRUF-Zeit zeigt — mit den
// Leserechten der Person, der das Abo gehört (ACL wird bei jedem Abruf neu
// geprüft, nicht nur bei der Anlage), dazu den Kalendernamen. status != 0 →
// Fehlerantwort (404 bei gelöschter Person, F-419; 500 bei Lesefehlern).
func (a *ICalAPI) aboBoards(ctx context.Context, sub *storage.ICalSubscription) (map[string]struct{}, string, int, error) {
	u, err := a.users.GetByID(ctx, sub.UserID)
	if errors.Is(err, storage.ErrUserNotFound) {
		// F-419: verwaister Token (User gelöscht) → 404 wie ein unbekannter
		// Token, nicht 500 (kein Info-Leak über Token-Existenz).
		return nil, "", http.StatusNotFound, nil
	}
	if err != nil {
		return nil, "", http.StatusInternalServerError, err
	}
	email := storage.NormalizeEmail(u.Email)
	permitted := map[string]struct{}{}
	scope := "alle Boards"
	if sub.BoardID != nil {
		role, found, gerr := a.acl.GetRole(ctx, *sub.BoardID, email)
		if gerr != nil {
			return nil, "", http.StatusInternalServerError, gerr
		}
		if found && roleRank[role] >= roleRank[storage.RoleViewer] {
			permitted[*sub.BoardID] = struct{}{}
		}
		// Board-Titel als Kalendername (F-420), Fallback auf die ID.
		scope = *sub.BoardID
		if b, berr := a.boards.Get(ctx, *sub.BoardID); berr == nil {
			scope = b.Title
		}
	} else {
		ids, lerr := a.acl.ListBoardsForUser(ctx, email)
		if lerr != nil {
			return nil, "", http.StatusInternalServerError, lerr
		}
		for _, bid := range ids {
			permitted[bid] = struct{}{}
		}
	}
	return permitted, scope, 0, nil
}

// aboFehlerText: Fehlertext zu einem Status aus aboBoards.
func aboFehlerText(status int) string {
	if status == http.StatusNotFound {
		return "not found"
	}
	return "feed lookup failed"
}

// requestOrigin rekonstruiert das öffentliche Origin (scheme://host) aus dem
// Request — für absolute Deep-Links im Feed. X-Forwarded-Proto (Caddy) hat
// Vorrang; Default https (iCal wird praktisch nur über TLS ausgeliefert).
func requestOrigin(r *http.Request) string {
	host := r.Host
	if host == "" {
		return ""
	}
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p // Proxy-Angabe hat Vorrang
	} else if r.TLS == nil {
		scheme = "http" // lokaler Klartext-Request ohne Proxy
	}
	return scheme + "://" + host
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// truncate kürzt rune-sicher auf max. n Runen (kein UTF-8-Bruch im .ics).
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Werte von X-ABOARD-ART im Feed der Seite außen (Karte #782). Die Werte aus
// ADR-0138 §6 sind mit ADR-0140 §11 überholt; diese Werte gelten. Schatten
// tragen das Präfix „schatten-“, damit ein Leser sie ohne zweite Auskunft
// von eigenen Einträgen trennt.
const (
	ArtFeedKarte          = "card"
	ArtFeedTermin         = "termin"
	ArtFeedSchattenKarte  = "schatten-card"
	ArtFeedSchattenTermin = "schatten-termin"
)

// artEigen: die Art eines eigenen Eintrags — nur im Feed außen gesetzt.
func artEigen(aussen bool, art string) string {
	if aussen {
		return art
	}
	return ""
}

// schattenFeedAktiv: Der Feed trägt Schatten, wenn die Instanz die Seite
// außen führt und Kopplung und Schatten angeschlossen sind.
//
// Sitzungs-QA C-3: Ist server_config nicht lesbar (Lesefehler, nicht fehlender
// Schlüssel), meldet die Funktion den Fehler; der Feed antwortet dann 503
// statt als „aus“ ohne Schatten — ein Kalender-Client löschte sonst die
// Schatten-Einträge bis zum nächsten Abruf. Ohne Verdrahtung wird gar nicht
// gelesen.
func (a *ICalAPI) schattenFeedAktiv(ctx context.Context) (bool, error) {
	if a.boardpaar == nil || a.schatten == nil {
		return false, nil
	}
	seite, err := BoardpaarSeiteMitFehler(ctx, a.cfg)
	return seite == BoardpaarSeiteAussen, err
}

// schattenEvents baut die Einträge für die Schatten mit Datum aller
// gekoppelten Boards aus permitted (dieselbe Menge wie die Karten des Abos:
// Lese-ACL der Person, zur Abruf-Zeit). Boards ohne Leserecht kommen nicht
// vor, auch wenn sie gekoppelt sind.
func (a *ICalAPI) schattenEvents(ctx context.Context, permitted map[string]struct{}) ([]calendar.Event, error) {
	paare, err := a.boardpaar.AussenListe(ctx)
	if err != nil {
		return nil, err
	}
	var out []calendar.Event
	for _, paar := range paare {
		if _, ok := permitted[paar.BoardID]; !ok {
			continue
		}
		zeilen, err := a.schatten.Liste(ctx, paar.BoardID)
		if err != nil {
			return nil, err
		}
		for _, z := range zeilen {
			if ev, ok := schattenEvent(z, paar.InnenLinkBasis, paar.BoardID); ok {
				out = append(out, ev)
			}
		}
	}
	return out, nil
}

// schattenEvent: ein Schatten als Kalendereintrag (ADR-0140 §6). Ohne Datum
// kein Eintrag. Vertraulich (oder ohne Titel, fail-closed) heißt der Eintrag
// „Interne Karte“; es gibt nie eine Beschreibung. Der Titel eines
// vertraulichen Schattens wird nicht einmal gelesen.
func schattenEvent(z storage.SchattenZeile, linkBasis, aussenBoardID string) (calendar.Event, bool) {
	start, ende := textOrEmpty(z.StartDatum), textOrEmpty(z.EndeDatum)
	if start == "" && ende == "" {
		return calendar.Event{}, false
	}
	// ADR-0141 Beschluss 3: Fassung 2 (Hinweg v2) zeigt den Titel auch vertraulich.
	summary, echt := schatten.AnzeigeTitel(z.Vertraulich && z.Fassung != 2, z.Titel)
	if echt && z.Art == schatten.ArtKarte && z.Nr != nil {
		summary = "i-" + strconv.Itoa(*z.Nr) + " " + summary
	}
	art, link := ArtFeedSchattenTermin, linkBasis
	if z.Art == schatten.ArtKarte {
		art = ArtFeedSchattenKarte
		// Der Link auf eine Karte entsteht durch Anhängen von &card=<nr>
		// (ADR-0140 §4); ohne Nummer führt er auf das Board. Termine haben
		// keine Nummer: Ihre interne Kennung geht nie nach außen.
		if z.Nr != nil {
			link += "&card=" + strconv.Itoa(*z.Nr)
		}
	}
	ev := calendar.Event{
		UID:     "schatten-" + z.SchattenID + "@" + aussenBoardID + ".schatten.aboard",
		Start:   firstNonEmpty(start, ende),
		End:     ende,
		EndTime: textOrEmpty(z.EndeZeit),
		Summary: summary,
		URL:     link,
		Art:     art,
	}
	if start != "" { // Uhrzeit ohne Startdatum gibt es nicht (wie bei Karten)
		ev.StartTime = textOrEmpty(z.StartZeit)
	}
	return ev, true
}

func textOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
