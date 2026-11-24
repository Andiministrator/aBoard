package api_test

// Karte #782 (ADR-0140 §6): Kalender außen. Auf einer Instanz mit
// boardpaar.seite = aussen trägt der iCal-Feed zusätzlich die Schatten mit
// Datum der gekoppelten Boards; auf aus/innen bleibt er, wie er war; auf innen
// lassen sich keine neuen Abos anlegen.
//
// Geprüft wird auf dem ganzen ICS-Text, nicht nur auf SUMMARY: Der Titel eines
// vertraulichen Schattens darf nirgends im Text vorkommen. Die Tabelle trägt
// ihn dort gar nicht (der Eingang lässt ihn nicht zu); der Test legt ihn über
// Ersetze trotzdem hinein, damit der Feed als zweite Schicht geprüft wird und
// nicht der Eingang.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	geheimKarte  = "GEHEIMTITEL-KARTE-782"
	geheimTermin = "GEHEIMTITEL-TERMIN-782"
	ohneDatum    = "OHNEDATUM-782"
	fremdTitel   = "FREMD-SCHATTEN-782"
	linkBasis1   = "https://innen.example/#board=innen-ext1"
)

type env782 struct {
	db     *sql.DB
	cfg    *storage.ConfigRepo
	tokens *storage.ICalTokenRepo
	schatt *storage.SchattenRepo
	acl    *storage.AclRepo
	alt    *httptest.Server // Verdrahtung wie vor #782 (ohne MitBoardpaar)
	neu    *httptest.Server // Verdrahtung wie main.go
}

func sid782(prefix string, n int) string {
	return prefix + strings.Repeat("x", schatten.SchattenIDZeichen-len(prefix)-1) + fmt.Sprint(n%10)
}

func s782(s string) *string { return &s }
func n782(n int) *int       { return &n }

func newEnv782(t *testing.T) *env782 {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	for _, id := range []string{"ext1", "ext2"} {
		state := `{"version":"2","board":{"id":"` + id + `","title":"Extern ` + id + `","nextDisplayId":2,
		  "columnDefs":[{"id":"col-ready","title":"Ready"}],
		  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[
		    {"id":"k-` + id + `","displayId":1,"title":"Eigene Karte ` + id + `","columnId":"col-ready","dueDate":"2026-10-20","description":"Eigene Beschreibung","comments":[]}]}]}}`
		if _, err := boards.Insert(ctx, id, "Extern "+id, "2", []byte(state)); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range [][]any{{"u-anna", "anna@firma.de"}, {"u-berta", "berta@firma.de"}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	// anna liest beide Boards, berta nur ext1.
	for _, z := range [][2]string{{"ext1", "anna@firma.de"}, {"ext2", "anna@firma.de"}, {"ext1", "berta@firma.de"}} {
		if err := acl.Set(ctx, z[0], z[1], "viewer"); err != nil {
			t.Fatal(err)
		}
	}
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	tokens := storage.NewICalTokenRepo(db, box)
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	bp := storage.NewBoardpaarRepo(db)
	sr := storage.NewSchattenRepo(db)

	baue := func(mitSchatten bool) *httptest.Server {
		mux := http.NewServeMux()
		a := api.NewICalAPI(tokens, users, boards, acl).MitKonfig(cfg)
		if mitSchatten {
			a = a.MitBoardpaar(bp, sr)
		}
		a.Register(mux)
		srv := httptest.NewServer(api.WithForwardAuthIfEnabled(mux))
		t.Cleanup(srv.Close)
		return srv
	}
	e := &env782{db: db, cfg: cfg, tokens: tokens, schatt: sr, acl: acl, alt: baue(false), neu: baue(true)}

	// Kopplung und Schatten. ext1: Karte mit Datum, vertrauliche Karte (Titel
	// trotzdem in der Tabelle), Termin, vertraulicher Termin, Karte ohne
	// Datum. ext2: eine Karte mit Datum.
	for id, basis := range map[string]string{"ext1": linkBasis1, "ext2": "https://innen.example/#board=innen-ext2"} {
		if _, err := bp.AussenKoppeln(ctx, id, basis, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	e.ersetze(t, "ext1", []schatten.Eingang{
		{SchattenID: sid782("A", 1), Art: schatten.ArtKarte, Nr: n782(42), Titel: s782("Angebot Müller"),
			StartDatum: s782("2026-10-21"), StartZeit: s782("09:00"), EndeDatum: s782("2026-10-22"), EndeZeit: s782("17:00")},
		{SchattenID: sid782("B", 2), Art: schatten.ArtKarte, Vertraulich: true, Titel: s782(geheimKarte), Nr: n782(43),
			EndeDatum: s782("2026-10-23")},
		{SchattenID: sid782("C", 3), Art: schatten.ArtTermin, Titel: s782("Jour fixe"),
			StartDatum: s782("2026-10-24"), StartZeit: s782("10:00"), EndeDatum: s782("2026-10-24"), EndeZeit: s782("11:00")},
		{SchattenID: sid782("D", 4), Art: schatten.ArtTermin, Vertraulich: true, Titel: s782(geheimTermin),
			StartDatum: s782("2026-10-25")},
		{SchattenID: sid782("E", 5), Art: schatten.ArtKarte, Nr: n782(44), Titel: s782(ohneDatum)},
	})
	e.ersetze(t, "ext2", []schatten.Eingang{
		{SchattenID: sid782("F", 6), Art: schatten.ArtKarte, Nr: n782(9), Titel: s782(fremdTitel), EndeDatum: s782("2026-10-26")},
	})
	e.seite(t, api.BoardpaarSeiteAussen)
	return e
}

func (e *env782) ersetze(t *testing.T, board string, menge []schatten.Eingang) {
	t.Helper()
	if _, err := e.schatt.Ersetze(context.Background(), board, menge); err != nil {
		t.Fatal(err)
	}
}

func (e *env782) seite(t *testing.T, wert string) {
	t.Helper()
	if err := e.cfg.Set(context.Background(), api.KonfigBoardpaarSeite, wert, false, "test"); err != nil {
		t.Fatal(err)
	}
}

// abo legt ein Abo an (board == "" → alle Boards) und liefert den Token.
func (e *env782) abo(t *testing.T, user, board string) string {
	t.Helper()
	var bid *string
	if board != "" {
		bid = &board
	}
	sub, err := e.tokens.Create(context.Background(), user, bid)
	if err != nil {
		t.Fatal(err)
	}
	return sub.Token
}

func hole782(t *testing.T, srv *httptest.Server, methode, pfad, wer, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(methode, srv.URL+pfad, rdr)
	if wer != "" {
		req.Header.Set("Remote-Email", wer)
		req.Header.Set("Remote-User", wer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func feed782(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	s, b := hole782(t, srv, "GET", "/ical/calendar.ics?token="+url.QueryEscape(token), "", "")
	if s != http.StatusOK {
		t.Fatalf("Feed: %d %s", s, b)
	}
	return entfalte782(b)
}

// entfalte782 löst die Zeilenfaltung (CRLF + Leerzeichen) auf, sonst bricht
// jede lange UID in der Mitte.
func entfalte782(s string) string { return strings.ReplaceAll(s, "\r\n ", "") }

// ereignisse782: die VEVENT-Blöcke des (entfalteten) ICS.
func ereignisse782(ics string) []string {
	var out []string
	for _, teil := range strings.Split(ics, "BEGIN:VEVENT")[1:] {
		out = append(out, teil[:strings.Index(teil, "END:VEVENT")])
	}
	return out
}

func mit782(ics, was string) string {
	for _, ev := range ereignisse782(ics) {
		if strings.Contains(ev, was) {
			return ev
		}
	}
	return ""
}

var dtstamp782 = regexp.MustCompile(`(?m)^DTSTAMP:.*\r?\n`)

func ohneStempel782(s string) string { return dtstamp782.ReplaceAllString(s, "") }

func schattenEreignisse782(ics string) []string {
	var out []string
	for _, ev := range ereignisse782(ics) {
		if strings.Contains(ev, "UID:schatten-") {
			out = append(out, ev)
		}
	}
	return out
}

func TestICal782_FeedAussen(t *testing.T) {
	e := newEnv782(t)
	ics := feed782(t, e.neu, e.abo(t, "u-anna", "ext1"))

	// Karten-Schatten mit Datum: SUMMARY i-<nr> <Titel>, UID, URL, Art.
	k := mit782(ics, "SUMMARY:i-42 Angebot Müller")
	if k == "" {
		t.Fatalf("Karten-Schatten fehlt:\n%s", ics)
	}
	for _, soll := range []string{
		"UID:schatten-" + sid782("A", 1) + "@ext1.schatten.aboard",
		"URL:" + linkBasis1 + "&card=42",
		"X-ABOARD-ART:schatten-card",
		"DTSTART:", // mit Uhrzeit: Zeitpunkt, kein Ganztag
	} {
		if !strings.Contains(k, soll) {
			t.Errorf("Karten-Schatten ohne %q:\n%s", soll, k)
		}
	}
	// Termin-Schatten: Titel ohne Nummer, Link auf das Board (keine interne
	// Termin-Kennung), Art schatten-termin.
	tm := mit782(ics, "SUMMARY:Jour fixe")
	if tm == "" {
		t.Fatalf("Termin-Schatten fehlt:\n%s", ics)
	}
	for _, soll := range []string{"UID:schatten-" + sid782("C", 3) + "@ext1.schatten.aboard", "URL:" + linkBasis1 + "\r\n", "X-ABOARD-ART:schatten-termin"} {
		if !strings.Contains(tm, soll) {
			t.Errorf("Termin-Schatten ohne %q:\n%s", soll, tm)
		}
	}
	if strings.Contains(tm, "&card=") || strings.Contains(tm, "termin=") {
		t.Errorf("Termin-Schatten trägt eine Kennung im Link:\n%s", tm)
	}

	// Vertraulich: Karte und Termin heißen „Interne Karte“; der Titel steht
	// im ganzen ICS-Text nirgends.
	for _, geheim := range []string{geheimKarte, geheimTermin} {
		if strings.Contains(ics, geheim) {
			t.Errorf("vertraulicher Titel %q steht im ICS", geheim)
		}
	}
	for _, id := range []string{sid782("B", 2), sid782("D", 4)} {
		ev := mit782(ics, "UID:schatten-"+id)
		if ev == "" || !strings.Contains(ev, "SUMMARY:Interne Karte\r\n") {
			t.Errorf("vertraulicher Schatten %s: %q", id, ev)
		}
	}

	// Ohne Datum kein Eintrag.
	if strings.Contains(ics, ohneDatum) || strings.Contains(ics, "UID:schatten-"+sid782("E", 5)) {
		t.Errorf("Schatten ohne Datum steht im Feed")
	}
	// Vier Schatten mit Datum aus ext1 — und keiner aus ext2.
	if n := len(schattenEreignisse782(ics)); n != 4 {
		t.Errorf("%d Schatten-Einträge, erwartet 4:\n%s", n, ics)
	}
	if strings.Contains(ics, fremdTitel) {
		t.Errorf("Schatten eines anderen Boards im Board-Abo")
	}

	// Keine Beschreibung an keinem Schatten.
	for _, ev := range schattenEreignisse782(ics) {
		if strings.Contains(ev, "DESCRIPTION") {
			t.Errorf("Schatten mit Beschreibung:\n%s", ev)
		}
	}

	// Eigene Einträge tragen auf der Seite außen ihre Art und behalten ihre
	// Beschreibung.
	eigen := mit782(ics, "UID:k-ext1@ext1.aboard")
	if !strings.Contains(eigen, "X-ABOARD-ART:card") || strings.Contains(eigen, "X-ABOARD-ART:schatten") || !strings.Contains(eigen, "DESCRIPTION:Eigene Beschreibung") {
		t.Errorf("eigene Karte:\n%s", eigen)
	}
}

func TestICal782_BoardUebergreifendMitACL(t *testing.T) {
	e := newEnv782(t)
	// anna liest ext1 und ext2: Schatten beider Boards.
	annaAlle := feed782(t, e.neu, e.abo(t, "u-anna", ""))
	if mit782(annaAlle, "SUMMARY:i-42 Angebot Müller") == "" || mit782(annaAlle, "SUMMARY:i-9 "+fremdTitel) == "" {
		t.Errorf("Alle-Boards-Abo ohne Schatten beider Boards:\n%s", annaAlle)
	}
	if n := len(schattenEreignisse782(annaAlle)); n != 5 {
		t.Errorf("%d Schatten-Einträge, erwartet 5", n)
	}
	// berta liest nur ext1: die Schatten von ext2 fehlen (Gegenfall).
	bertaAlle := feed782(t, e.neu, e.abo(t, "u-berta", ""))
	if strings.Contains(bertaAlle, fremdTitel) || strings.Contains(bertaAlle, sid782("F", 6)) {
		t.Errorf("Schatten ohne Leserecht im Alle-Boards-Abo:\n%s", bertaAlle)
	}
	if n := len(schattenEreignisse782(bertaAlle)); n != 4 {
		t.Errorf("%d Schatten-Einträge für berta, erwartet 4", n)
	}
	// berta mit Board-Abo auf ext2 (ohne Recht): weder Karten noch Schatten.
	bertaFremd := feed782(t, e.neu, e.abo(t, "u-berta", "ext2"))
	if strings.Contains(bertaFremd, fremdTitel) || len(ereignisse782(bertaFremd)) != 0 {
		t.Errorf("Board-Abo ohne Leserecht liefert Einträge:\n%s", bertaFremd)
	}
	// Die ACL gilt zur Abrufzeit: Entzug wirkt auf das bestehende Abo.
	tok := e.abo(t, "u-anna", "")
	if err := e.acl.Delete(context.Background(), "ext2", "anna@firma.de"); err != nil {
		t.Fatal(err)
	}
	if feed := feed782(t, e.neu, tok); strings.Contains(feed, fremdTitel) {
		t.Errorf("Schatten nach Rechteentzug noch im Feed")
	}
}

func TestICal782_AusUndInnenUnveraendert(t *testing.T) {
	e := newEnv782(t)
	tok := e.abo(t, "u-anna", "")
	// Bezug: die Verdrahtung wie vor #782 (kein Schatten-Wissen), gleiche DB.
	e.seite(t, api.BoardpaarSeiteAus)
	// Die beiden Test-Server haben verschiedene Ports; der Host steht in den URLs.
	gleich := func(ics string, srv *httptest.Server) string {
		return strings.ReplaceAll(ohneStempel782(ics), strings.TrimPrefix(srv.URL, "http://"), "HOST")
	}
	bezug := gleich(feed782(t, e.alt, tok), e.alt)
	if len(ereignisse782(bezug)) == 0 {
		t.Fatalf("Bezugsfeed leer")
	}
	for _, seite := range []string{api.BoardpaarSeiteAus, api.BoardpaarSeiteInnen} {
		e.seite(t, seite)
		ics := feed782(t, e.neu, tok)
		if got := gleich(ics, e.neu); got != bezug {
			t.Errorf("Seite %s: Feed weicht vom Stand vor #782 ab:\n%s\n--- erwartet:\n%s", seite, got, bezug)
		}
		for _, verboten := range []string{"schatten-", "X-ABOARD-ART", "Interne Karte", "Angebot Müller", fremdTitel} {
			if strings.Contains(ics, verboten) {
				t.Errorf("Seite %s: Feed enthält %q", seite, verboten)
			}
		}
	}
	// Ein unlesbarer Wert wirkt wie aus (§4).
	e.seite(t, "quatsch")
	if got := api.BoardpaarSeite(context.Background(), e.cfg); got != api.BoardpaarSeiteAus {
		t.Fatalf("ungültige Seite wirkt als %q, erwartet aus", got)
	}
	if ics := feed782(t, e.neu, tok); strings.Contains(ics, "schatten-") {
		t.Errorf("ungültige Seite: Schatten im Feed")
	}
}

func TestICal782_InnenKeineNeuenAbos(t *testing.T) {
	e := newEnv782(t)
	zaehle := func() int {
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM ical_subscriptions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tok := e.abo(t, "u-anna", "ext1") // Bestand
	e.seite(t, api.BoardpaarSeiteInnen)

	for _, body := range []string{"", `{"board_id":"ext1"}`} {
		s, b := hole782(t, e.neu, "POST", "/me/ical/subscriptions", "anna@firma.de", body)
		if s != http.StatusConflict || !strings.Contains(b, "Kalender abonnierst du auf dem externen Board") {
			t.Errorf("Anlegen auf innen (%q): %d %s, erwartet 409 mit Hinweis", body, s, b)
		}
	}
	if n := zaehle(); n != 1 {
		t.Errorf("%d Abos nach abgelehntem Anlegen, erwartet 1", n)
	}
	// Bestehendes Abo: Liste, Feed und Löschen gehen weiter.
	if s, _ := hole782(t, e.neu, "GET", "/me/ical/subscriptions", "anna@firma.de", ""); s != http.StatusOK {
		t.Errorf("Liste auf innen: %d", s)
	}
	if ics := feed782(t, e.neu, tok); mit782(ics, "UID:k-ext1@ext1.aboard") == "" {
		t.Errorf("bestehender Feed auf innen leer")
	}
	// Auf aus und außen geht das Anlegen wie bisher.
	for _, seite := range []string{api.BoardpaarSeiteAus, api.BoardpaarSeiteAussen} {
		e.seite(t, seite)
		before := zaehle()
		if s, b := hole782(t, e.neu, "POST", "/me/ical/subscriptions", "anna@firma.de", `{"board_id":"ext1"}`); s != http.StatusCreated {
			t.Errorf("Anlegen auf %s: %d %s", seite, s, b)
		}
		if zaehle() != before+1 {
			t.Errorf("Anlegen auf %s hat kein Abo erzeugt", seite)
		}
	}
	// Löschen auf innen bleibt möglich (nur Anlegen ist gesperrt).
	e.seite(t, api.BoardpaarSeiteInnen)
	var id string
	if err := e.db.QueryRow(`SELECT id FROM ical_subscriptions LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if s, _ := hole782(t, e.neu, "DELETE", "/me/ical/subscriptions/"+id, "anna@firma.de", ""); s != http.StatusNoContent {
		t.Errorf("Löschen auf innen: %d", s)
	}
}

func TestICal782_SchattenEntferntEintragWeg(t *testing.T) {
	e := newEnv782(t)
	tok := e.abo(t, "u-anna", "ext1")
	if ics := feed782(t, e.neu, tok); mit782(ics, "SUMMARY:Jour fixe") == "" {
		t.Fatalf("Ausgangslage: Termin-Schatten fehlt")
	}
	// Vollabgleich ohne den Termin (§3): der Eintrag verschwindet.
	e.ersetze(t, "ext1", []schatten.Eingang{
		{SchattenID: sid782("A", 1), Art: schatten.ArtKarte, Nr: n782(42), Titel: s782("Angebot Müller"), EndeDatum: s782("2026-10-22")},
	})
	ics := feed782(t, e.neu, tok)
	if strings.Contains(ics, "Jour fixe") || strings.Contains(ics, sid782("C", 3)) {
		t.Errorf("entfernter Schatten steht noch im Feed:\n%s", ics)
	}
	if n := len(schattenEreignisse782(ics)); n != 1 {
		t.Errorf("%d Schatten-Einträge, erwartet 1", n)
	}
	// Leere Menge: keine Schatten mehr, eigene Einträge bleiben.
	e.ersetze(t, "ext1", nil)
	ics = feed782(t, e.neu, tok)
	if n := len(schattenEreignisse782(ics)); n != 0 {
		t.Errorf("%d Schatten nach leerer Menge", n)
	}
	if mit782(ics, "UID:k-ext1@ext1.aboard") == "" {
		t.Errorf("eigene Karte fehlt nach leerer Schattenmenge")
	}
}

// K-3 (Kritik zu #782): Karten-Schatten mit Datum, aber ohne Nummer — SUMMARY
// ohne „i-“, URL ist nur die Basis.
func TestICal782_KarteOhneNummer(t *testing.T) {
	e := newEnv782(t)
	e.ersetze(t, "ext1", []schatten.Eingang{
		{SchattenID: sid782("G", 7), Art: schatten.ArtKarte, Titel: s782("Ohne Nummer"), EndeDatum: s782("2026-10-27")},
	})
	ics := feed782(t, e.neu, e.abo(t, "u-anna", "ext1"))
	ev := mit782(ics, "UID:schatten-"+sid782("G", 7))
	if !strings.Contains(ev, "SUMMARY:Ohne Nummer\r\n") || strings.Contains(ev, "SUMMARY:i-") {
		t.Errorf("Karte ohne Nummer:\n%s", ev)
	}
	if !strings.Contains(ev, "URL:"+linkBasis1+"\r\n") {
		t.Errorf("Link einer Karte ohne Nummer ist nicht die Basis:\n%s", ev)
	}
}

// K-4: Dasselbe interne Board an zwei externe Boards gekoppelt — dieselbe
// schatten_id kommt zweimal vor. Die UIDs sind je externem Board eindeutig und
// hängen nicht am Host des Abrufs.
func TestICal782_UIDJeExternemBoardUndOhneHost(t *testing.T) {
	e := newEnv782(t)
	gleich := []schatten.Eingang{{SchattenID: sid782("H", 8), Art: schatten.ArtKarte, Nr: n782(5), Titel: s782("Doppelt"), EndeDatum: s782("2026-10-28")}}
	e.ersetze(t, "ext1", gleich)
	e.ersetze(t, "ext2", gleich)
	tok := e.abo(t, "u-anna", "")
	ics := feed782(t, e.neu, tok)
	u1 := "UID:schatten-" + sid782("H", 8) + "@ext1.schatten.aboard"
	u2 := "UID:schatten-" + sid782("H", 8) + "@ext2.schatten.aboard"
	if !strings.Contains(ics, u1+"\r\n") || !strings.Contains(ics, u2+"\r\n") {
		t.Fatalf("UIDs je externem Board fehlen:\n%s", ics)
	}
	uids := map[string]int{}
	for _, ev := range ereignisse782(ics) {
		for _, z := range strings.Split(ev, "\r\n") {
			if strings.HasPrefix(z, "UID:") {
				uids[z]++
			}
		}
	}
	for u, n := range uids {
		if n != 1 {
			t.Errorf("UID %s kommt %d-mal vor", u, n)
		}
	}
	// Anderer Host im Abruf: dieselben UIDs.
	req, _ := http.NewRequest("GET", e.neu.URL+"/ical/calendar.ics?token="+url.QueryEscape(tok), nil)
	req.Host = "anderer-host.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	ics2 := entfalte782(string(b))
	if !strings.Contains(ics2, u1+"\r\n") || !strings.Contains(ics2, u2+"\r\n") || strings.Contains(ics2, "@anderer-host.example") {
		t.Errorf("UID hängt am Host:\n%s", ics2)
	}
}

// K-5: In der Link-Basis stehende „,“ und „;“ bleiben im URL-Wert unverändert
// (RFC 5545: URI-Werte werden nicht escaped).
func TestICal782_URLNichtEscaped(t *testing.T) {
	e := newEnv782(t)
	basis := "https://innen.example/a,b;c#board=innen-ext1"
	if _, err := e.db.Exec(`UPDATE boardpaar_aussen SET innen_link_basis = ? WHERE aussen_board_id = 'ext1'`, basis); err != nil {
		t.Fatal(err)
	}
	ics := feed782(t, e.neu, e.abo(t, "u-anna", "ext1"))
	if !strings.Contains(ics, "URL:"+basis+"&card=42\r\n") || !strings.Contains(ics, "URL:"+basis+"\r\n") {
		t.Errorf("URL mit , und ; verändert:\n%s", ics)
	}
}

// V-1 (Verify zu #782): Eine eigene Karte, deren ID wie eine Schatten-UID
// beginnt (schatten-<sid>), darf nicht dieselbe UID ergeben wie der Schatten —
// die Schatten haben einen eigenen Namensraum (@<board>.schatten.aboard).
func TestICal782_UIDKollisionMitEigenerKarte(t *testing.T) {
	e := newEnv782(t)
	eigeneID := "schatten-" + sid782("A", 1)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = replace(state_json, '"k-ext1"', ?) WHERE id = 'ext1'`, `"`+eigeneID+`"`); err != nil {
		t.Fatal(err)
	}
	ics := feed782(t, e.neu, e.abo(t, "u-anna", "ext1"))
	eigen := "UID:" + eigeneID + "@ext1.aboard\r\n"
	schattenUID := "UID:schatten-" + sid782("A", 1) + "@ext1.schatten.aboard\r\n"
	if !strings.Contains(ics, eigen) || !strings.Contains(ics, schattenUID) {
		t.Fatalf("eigene Karte oder Schatten fehlt:\n%s", ics)
	}
	uids := map[string]int{}
	for _, ev := range ereignisse782(ics) {
		for _, z := range strings.Split(ev, "\r\n") {
			if strings.HasPrefix(z, "UID:") {
				uids[z]++
			}
		}
	}
	for u, n := range uids {
		if n != 1 {
			t.Errorf("UID %s kommt %d-mal vor", u, n)
		}
	}
}

// V-2 (Verify zu #782, Klasse 155): Die URL-Zeilen eigener Einträge sind
// wörtlich festgelegt — der Vergleich „aus gegen vor #782“ liefe mit jeder
// Änderung an uriWert oder escapeText grün, weil beide Seiten dieselbe
// Funktion benutzen.
func TestICal782_EigeneLinksWoertlich(t *testing.T) {
	e := newEnv782(t)
	const id = "7c1d2f0e-5a44-4b8e-9d0a-3f6a2b1c9e55"
	state := `{"version":"2","board":{"id":"` + id + `","title":"U","nextDisplayId":8,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "termine":[{"id":"t-1","titel":"Eigener Termin","datum":"2026-10-21"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[
	    {"id":"k-u","displayId":7,"title":"Eigene","columnId":"col-ready","dueDate":"2026-10-20","comments":[]}]}]}}`
	if _, err := storage.NewBoardRepo(e.db).Insert(context.Background(), id, "U", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	if err := e.acl.Set(context.Background(), id, "anna@firma.de", "viewer"); err != nil {
		t.Fatal(err)
	}
	e.seite(t, api.BoardpaarSeiteAus)
	ics := feed782(t, e.neu, e.abo(t, "u-anna", id))
	origin := e.neu.URL // http://127.0.0.1:<port>
	for _, soll := range []string{
		"URL:" + origin + "/#board=" + id + "&card=7\r\n",
		"URL:" + origin + "/#board=" + id + "&termin=t-1\r\n",
	} {
		if !strings.Contains(ics, soll) {
			t.Errorf("fehlt wörtlich %q:\n%s", soll, ics)
		}
	}
}

// Sitzungs-QA C-3: Ist server_config nicht lesbar, antwortet der Feed mit 503
// statt still als „aus“ (ein Kalender-Client löschte sonst die Schatten-
// Einträge). Ein fehlender Schlüssel bleibt „aus“ (200, ohne Schatten); ohne
// Board-Paar-Verdrahtung wird die Konfiguration gar nicht gefragt.
func TestICal782_SeiteNichtLesbarFeed503(t *testing.T) {
	e := newEnv782(t)
	tok := e.abo(t, "u-anna", "ext1")
	e.seite(t, api.BoardpaarSeiteAussen)

	kaputtDB, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "kaputt.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = kaputtDB.Close()
	kaputt := storage.NewConfigRepo(kaputtDB, nil, func() string { return "x" })
	baue := func(mitPaar bool) *httptest.Server {
		mux := http.NewServeMux()
		a := api.NewICalAPI(e.tokens, storage.NewUserRepo(e.db), storage.NewBoardRepo(e.db), e.acl).MitKonfig(kaputt)
		if mitPaar {
			a = a.MitBoardpaar(storage.NewBoardpaarRepo(e.db), e.schatt)
		}
		a.Register(mux)
		srv := httptest.NewServer(api.WithForwardAuthIfEnabled(mux))
		t.Cleanup(srv.Close)
		return srv
	}
	pfad := "/ical/calendar.ics?token=" + url.QueryEscape(tok)
	if s, b := hole782(t, baue(true), "GET", pfad, "", ""); s != http.StatusServiceUnavailable {
		t.Errorf("verdrahtet, Seite unlesbar: %d %.200s, erwartet 503", s, b)
	}
	if s, b := hole782(t, baue(false), "GET", pfad, "", ""); s != http.StatusOK {
		t.Errorf("unverdrahtet: %d %.200s, erwartet 200 (Konfig wird nicht gelesen)", s, b)
	}
}
