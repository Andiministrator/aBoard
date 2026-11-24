package api

// schleuse_auszug.go — Auszug, Pseudonymisierung und Durchsicht der
// Ausgangsschleuse, Teil S2 (ADR-0134 §3.1–§3.4, §4; Karte #685).
//
// Routen (nur bei ABOARD_SCHLEUSE=an, über SchleuseAPI.Register):
//
//	GET    /boards/{id}/cards/{cardId}/schleuse-auftraege          eigene Aufträge an der Karte + Lage
//	POST   /boards/{id}/cards/{cardId}/schleuse-auftraege          Auszug erstellen (editor+)
//	GET    /boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}    eigenen Auftrag lesen
//	PUT    /boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}    Text ändern (Durchsicht, editor+)
//	DELETE /boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}    eigenen Entwurf löschen
//	DELETE /users/me/schleuse-auftraege/{aid}                      eigenen Entwurf löschen, ohne Rolle am Board
//
// Rollen (im Handler geprüft, zusätzlich zum ACLGuard — Kritiker KS-11):
// Lesen (Liste, Einzelabruf) verlangt Mitgliedschaft am Board (viewer+),
// Erstellen und Ändern editor+. Löschen des EIGENEN Entwurfs braucht keine
// Rolle (F-1619): Wer zum viewer zurückgestuft wurde oder den Zugang ganz
// verloren hat, soll seine Halde personenbezogener Zuordnungen trotzdem
// abräumen können. Der ACLGuard verlangt für DELETE unter /boards editor —
// deshalb die zweite Route unter /users/me, die er nicht erfasst; die
// Oberfläche löscht immer über sie.
//
// Der Server liest Beschreibung und Kommentare aus dem GESPEICHERTEN
// Board-Stand — Text für diese Teile nimmt er nie vom Client an. Vom Client
// kommen nur die Auswahl (welche Teile) und die eigene Aufgabenstellung.
//
// Fremde Aufträge antworten 404, nie 403: Ob es einen Auftrag gibt, erfährt
// nur seine Person (Muster ADR-0133 Nachtrag 2, A5).
//
// Die Zuordnung Platzhalter → Original verlässt den Server nie: Kein Typ
// dieser Datei trägt sie, und SchleuseAuftrag hat kein Feld dafür.
//
// Abgrenzung zum Board-Chat (ADR-0133 §2): Diese Datei kennt den
// Chat-Schlüsselspeicher nicht; das lokale Modell kommt als
// schleuse.ModellAufruf von main.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Grenzen des Auszugs.
const (
	// auszugBodyHoechst: Aufgabenstellung bis zur Zeichengrenze (bis zu 4
	// Bytes je Zeichen, JSON-Escapes dazu) plus Auswahl. 1 MiB deckt die
	// Höchstgrenze von 200000 Zeichen nicht ganz — die Zeichengrenze greift
	// dann ohnehin vorher.
	auszugBodyHoechst = 1 << 20
	// auszugKommentareHoechst: mehr Kommentare als das trägt kein Auszug.
	auszugKommentareHoechst = 200
	// EntwuerfeJePersonHoechst: offene Entwürfe je Person. Mehr ist kein
	// Arbeitsstand, sondern eine Halde personenbezogener Zuordnungen.
	EntwuerfeJePersonHoechst = 20
	// AuszuegeJeStundeHoechst: Auszüge je Person und Stunde. Jeder kostet
	// einen Aufruf des lokalen Modells; die Grenze für GESENDETE Aufträge
	// (ADR-0134 §1, Vorgabe 10) ist Konfig.GrenzeAuftraege (S3).
	AuszuegeJeStundeHoechst = 30
)

// auszugModellFrist ist die Gesamtfrist des lokalen Modells für die zweite
// Stufe — die Erstbyte-Frist aus ADR-0133 §3 (300 s). Der Header-Deckel des
// LLM-Clients (llm.AntwortHeaderDeckel, 330 s) liegt darüber. var: Tests
// verkürzen sie.
var auszugModellFrist = 300 * time.Second

// auszugLebenszeichen: Solange das lokale Modell rechnet, bekommt der
// Browser alle 25 s ein Leerzeichen (ADR-0133 §3: größte Stille 25 s, damit
// Proxys nicht abbrechen). var: Tests verkürzen ihn.
var auszugLebenszeichen = 25 * time.Second

// auszugSchreibReserve: so viel länger als die Modellfrist darf das
// Schreiben der Antwort dauern.
const auszugSchreibReserve = 15 * time.Second

// MitAuszug verdrahtet S2: das Repo der Aufträge (nil ohne Master-Key → 503)
// und den Aufruf des lokalen Modells (nil ohne Firmenmodus mit eigener
// Basis-URL → jeder Auszug bricht mit 503 ab).
func (s *SchleuseAPI) MitAuszug(auftraege *storage.SchleuseAuftragRepo, modell schleuse.ModellAufruf) *SchleuseAPI {
	s.auftraege = auftraege
	s.modell = modell
	return s
}

// registerAuszug hängt die Routen an (aus Register, also nur bei „an“).
func (s *SchleuseAPI) registerAuszug(mux *http.ServeMux) {
	const basis = "/boards/{id}/cards/{cardId}/schleuse-auftraege"
	mux.Handle("GET "+basis, s.auftragPerson(s.auftraegeListe))
	mux.Handle("POST "+basis, s.auftragPerson(s.auszugErstellen))
	mux.Handle("GET "+basis+"/{aid}", s.auftragPerson(s.auftragLesen))
	mux.Handle("PUT "+basis+"/{aid}", s.auftragPerson(s.auftragAendern))
	mux.Handle("DELETE "+basis+"/{aid}", s.auftragPerson(s.auftragLoeschen))
	mux.Handle("DELETE /users/me/schleuse-auftraege/{aid}", s.auftragPerson(s.eigenenAuftragLoeschen))
	s.registerVersand(mux) // Karte #686 (S3)
}

// auftragPerson löst die Person auf (wie person, aber ohne Schlüssel-Repo-
// Pflicht und mit E-Mail) und räumt alte Entwürfe ab. Ohne Repo: 503.
func (s *SchleuseAPI) auftragPerson(h func(http.ResponseWriter, *http.Request, *storage.User)) http.Handler {
	return WithDefaults(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromContext(r.Context())
		if id == nil {
			writeError(w, http.StatusForbidden, "forbidden: schleuse requires authentication")
			return
		}
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
		u, err := s.users.GetByEmail(r.Context(), email)
		if errors.Is(err, storage.ErrUserNotFound) {
			writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		if s.auftraege == nil || s.schluessel == nil {
			schleuseFehler(w, http.StatusServiceUnavailable, "kein-master-key", "schleuse disabled: configure ABOARD_SECRET_KEY")
			return
		}
		// Frist der nie gesendeten Entwürfe (ADR-0134 §4, Regel aus #685):
		// bei jedem Zugriff nachgezogen — kein Hintergrundprozess nötig.
		if _, err := s.auftraege.EntwuerfeAufraeumen(r.Context(), s.cfg.AufbewahrungTage); err != nil {
			s.logf("aboard-server: Ausgangsschleuse: alte Entwürfe nicht aufgeräumt: %v", err)
		}
		// Karte #686 (ADR-0134 §4): Zuordnungen abgeschlossener Aufträge nach
		// der Frist — danach ist kein Auftrag mehr re-identifizierbar.
		if _, err := s.auftraege.ZuordnungenAufraeumen(r.Context(), s.cfg.AufbewahrungTage); err != nil {
			s.logf("aboard-server: Ausgangsschleuse: alte Zuordnungen nicht gelöscht: %v", err)
		}
		h(w, r, u)
	}))
}

// schleuseFehler schreibt einen Fehler mit maschinenlesbarem Grund — die
// Oberfläche übersetzt ihn (src/schleuse-auszug.js).
func schleuseFehler(w http.ResponseWriter, status int, grund, msg string, err ...error) {
	if len(err) > 0 && err[0] != nil {
		log.Printf("aboard-server: %d %s: %s", status, msg, redactForLog(err[0]))
	}
	writeJSON(w, status, fehlerKoerper(status, grund, msg))
}

func fehlerKoerper(status int, grund, msg string) map[string]any {
	return map[string]any{"error": msg, "grund": grund, "status": status}
}

// rolleMindestens prüft die wirksame Rolle der Person am Board (ADR-0132:
// Domain-Einträge zählen). Der ACLGuard prüft dasselbe schon — hier steht es
// noch einmal, weil die Schleuse Daten nach draussen vorbereitet und ohne
// Identität nicht offen sein darf (Legacy-Bypass des Guards).
func (s *SchleuseAPI) rolleMindestens(ctx context.Context, boardID, email, mindestens string) (bool, error) {
	if s.acl == nil {
		return false, nil
	}
	rolle, found, err := s.acl.GetRole(ctx, boardID, email)
	if err != nil || !found {
		return false, err
	}
	return roleRank[rolle] >= roleRank[mindestens], nil
}

// rolleOderFehler prüft die Mindestrolle und schreibt sonst 403
// („keine-rolle“). true = weiter.
func (s *SchleuseAPI) rolleOderFehler(w http.ResponseWriter, r *http.Request, boardID, email, mindestens string) bool {
	ok, err := s.rolleMindestens(r.Context(), boardID, email, mindestens)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return false
	}
	if !ok {
		schleuseFehler(w, http.StatusForbidden, "keine-rolle", "forbidden: Rolle am Board reicht nicht")
		return false
	}
	return true
}

// ── Liste und Lage ─────────────────────────────────────────────────────

func (s *SchleuseAPI) auftraegeListe(w http.ResponseWriter, r *http.Request, u *storage.User) {
	boardID, cardID := r.PathValue("id"), r.PathValue("cardId")
	gesperrt, err := storage.SchleuseBoardSperre(r.Context(), s.db, boardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse sperre read failed", err)
		return
	}
	if !s.rolleOderFehler(w, r, boardID, u.Email, storage.RoleViewer) {
		return
	}
	editor, err := s.rolleMindestens(r.Context(), boardID, u.Email, storage.RoleEditor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return
	}
	liste, err := s.auftraege.ListFuerKarte(r.Context(), u.ID, boardID, cardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse auftraege list failed", err)
		return
	}
	// Karte #686: Aufträge anderer Personen, die auf die zweite Freigabe
	// warten — nur für editor+ (wer freigeben darf, sieht sie).
	zur := []map[string]any{}
	if editor {
		fremde, err := s.auftraege.ZurZweitfreigabe(r.Context(), boardID, cardID, u.Email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "schleuse zweitfreigabe list failed", err)
			return
		}
		for _, a := range fremde {
			zur = append(zur, zweitSicht(a))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auftraege":         liste,
		"zur_freigabe":      zur,
		"gesperrt":          gesperrt,
		"darf_erstellen":    editor,
		"lokales_modell":    s.modell != nil,
		"grenze_zeichen":    s.cfg.GrenzeZeichen(),
		"grenze_auftraege":  s.cfg.GrenzeAuftraege(),
		"versand":           s.versand != nil,
		"aufbewahrung_tage": s.cfg.AufbewahrungTage,
	})
}

// ── Auszug erstellen ───────────────────────────────────────────────────

type auszugEingabe struct {
	FreigabeID   string   `json:"freigabe_id"`
	Modell       string   `json:"modell"`
	Beschreibung bool     `json:"beschreibung"`
	Kommentare   []string `json:"kommentare"`
	Aufgabe      string   `json:"aufgabe"`
	// Dateien: Anhang-ids der Karte (Karte #687, ADR-0134 §5).
	Dateien []string `json:"dateien"`
}

// kartenStand ist, was der Auszug aus dem gespeicherten Board braucht.
type kartenStand struct {
	boardTitel   string
	identitaeten []map[string]any
	karte        map[string]any
}

// ladeKarte liest die Karte aus dem gespeicherten Board-Stand. Archivierte
// Karten zählen mit (die Person hat die Karte ausdrücklich geöffnet — wie
// loadCardForLLM).
func (s *SchleuseAPI) ladeKarte(ctx context.Context, boardID, cardID string) (kartenStand, error) {
	b, err := storage.NewBoardRepo(s.db).Get(ctx, boardID)
	if err != nil {
		return kartenStand{}, err
	}
	var root map[string]any
	if err := json.Unmarshal(b.StateJSON, &root); err != nil {
		return kartenStand{}, fmt.Errorf("parse state_json: %w", err)
	}
	board, _ := root["board"].(map[string]any)
	st := kartenStand{boardTitel: b.Title}
	if t, _ := board["title"].(string); strings.TrimSpace(t) != "" {
		st.boardTitel = t
	}
	if ids, ok := board["identities"].([]any); ok {
		for _, i := range ids {
			if m, ok := i.(map[string]any); ok {
				st.identitaeten = append(st.identitaeten, m)
			}
		}
	}
	sprints, _ := board["sprints"].([]any)
	for _, sp := range sprints {
		spm, _ := sp.(map[string]any)
		karten, _ := spm["cards"].([]any)
		for _, k := range karten {
			km, _ := k.(map[string]any)
			if id, _ := km["id"].(string); id != "" && id == cardID {
				st.karte = km
				return st, nil
			}
		}
	}
	return st, errCardNotFound
}

func schleuseTexte(v any) []string {
	var out []string
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// namensliste sammelt die feste Stufe (ADR-0134 §3.2): ACL-Mitglieder samt
// E-Mail und Namen, Identitäten des Boards (Personen, nicht Modelle), Owner
// und Assignees der Karte, ALLE Kommentar-Autoren der Karte (ein Kommentar
// kann einen anderen Autor nennen) und den Board-Titel.
func (s *SchleuseAPI) namensliste(ctx context.Context, boardID string, st kartenStand) ([]schleuse.Namenseintrag, error) {
	l := schleuse.NeueNamensliste()
	eintraege, err := s.acl.ListByBoard(ctx, boardID)
	if err != nil {
		return nil, err
	}
	for _, e := range eintraege {
		l.Email(e.UserEmail)
		if storage.IstDomainSubjekt(e.UserEmail) {
			// Kritiker KS-2 (Sprint-82-Abschluss, Runde 2): Ein Domain-Eintrag
			// (ADR-0132) macht jede Person dieser Domain zum Mitglied — ihre
			// Namen und Adressen gehören deshalb genauso auf die Liste wie die
			// eines einzeln eingetragenen Mitglieds.
			if err := s.domainNutzerAufnehmen(ctx, l, strings.TrimPrefix(e.UserEmail, "@")); err != nil {
				return nil, err
			}
			continue
		}
		if u, err := s.users.GetByEmail(ctx, e.UserEmail); err == nil {
			l.Person(u.Name)
		}
	}
	for _, i := range st.identitaeten {
		if k, _ := i["kind"].(string); k != "user" {
			continue
		}
		if name, _ := i["label"].(string); name != "" {
			l.Person(name)
		}
		if m, _ := i["email"].(string); m != "" {
			l.Email(m)
		}
	}
	if o, _ := st.karte["owner"].(string); o != "" {
		l.Person(o)
	}
	for _, a := range schleuseTexte(st.karte["assignees"]) {
		l.Person(a)
	}
	if ks, ok := st.karte["comments"].([]any); ok {
		for _, k := range ks {
			if km, ok := k.(map[string]any); ok {
				if a, _ := km["author"].(string); a != "" {
					l.Person(a)
				}
			}
		}
	}
	l.Board(st.boardTitel)
	return l.Eintraege(), nil
}

// domainNutzerHoechst begrenzt, wie viele Personen ein einzelner
// Domain-Eintrag auf die Namensliste bringt. Jeder Name wird ein eigener
// Suchausdruck; eine Domain mit Tausenden Konten machte jeden Auszug teuer.
// Wird die Grenze überschritten, warnt das Log — die übrigen Personen
// schützt dann nur noch die Modellstufe und die Durchsicht.
const domainNutzerHoechst = 500

// domainNutzerAufnehmen nimmt Name und E-Mail jeder Person mit einer Adresse
// in genau dieser Domain auf (keine Subdomains, wie ADR-0132 vergleicht).
// Ein Lesefehler bricht den Auszug ab: eine unvollständige Namensliste wäre
// „nicht lesbar heißt erlaubt“ (Fehlerklasse 122).
func (s *SchleuseAPI) domainNutzerAufnehmen(ctx context.Context, l *schleuse.Namensliste, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil
	}
	ersetzer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	rows, err := s.db.QueryContext(ctx,
		`SELECT email, name FROM users WHERE lower(email) LIKE ? ESCAPE '\' ORDER BY email LIMIT ?`,
		"%@"+ersetzer.Replace(domain), domainNutzerHoechst+1)
	if err != nil {
		return fmt.Errorf("schleuse namensliste domain: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if n > domainNutzerHoechst {
			break
		}
		var email, name string
		if err := rows.Scan(&email, &name); err != nil {
			return fmt.Errorf("schleuse namensliste domain: %w", err)
		}
		l.Email(email)
		l.Person(name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("schleuse namensliste domain: %w", err)
	}
	if n > domainNutzerHoechst {
		s.logf("aboard-server: WARNUNG: Ausgangsschleuse: Domain-Eintrag @%s hat mehr als %d Personen — nur die ersten %d stehen auf der festen Namensliste", domain, domainNutzerHoechst, domainNutzerHoechst)
	}
	return nil
}

// waehleKommentare liefert die gewählten Kommentare in Kartenreihenfolge.
// Eine unbekannte ID ist ein Fehler (der Client sah einen anderen Stand).
func waehleKommentare(karte map[string]any, ids []string) ([]schleuse.KommentarTeil, []string, error) {
	gewollt := map[string]bool{}
	for _, id := range ids {
		gewollt[id] = true
	}
	var teile []schleuse.KommentarTeil
	var gefunden []string
	if ks, ok := karte["comments"].([]any); ok {
		for _, k := range ks {
			km, _ := k.(map[string]any)
			id, _ := km["id"].(string)
			if id == "" || !gewollt[id] {
				continue
			}
			autor, _ := km["author"].(string)
			text, _ := km["text"].(string)
			teile = append(teile, schleuse.KommentarTeil{Autor: autor, Text: text})
			gefunden = append(gefunden, id)
			delete(gewollt, id)
		}
	}
	if len(gewollt) > 0 {
		return nil, nil, errors.New("kommentar unbekannt")
	}
	return teile, gefunden, nil
}

func (s *SchleuseAPI) auszugErstellen(w http.ResponseWriter, r *http.Request, u *storage.User) {
	ctx := r.Context()
	boardID, cardID := r.PathValue("id"), r.PathValue("cardId")

	editor, err := s.rolleMindestens(ctx, boardID, u.Email, storage.RoleEditor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return
	}
	if !editor {
		schleuseFehler(w, http.StatusForbidden, "keine-rolle", "forbidden: Auszug verlangt mindestens editor am Board")
		return
	}
	if !s.auszugLimiter.allow("auszug:" + u.ID) {
		schleuseFehler(w, http.StatusTooManyRequests, "zu-viele-auszuege", fmt.Sprintf("rate limit: %d Auszüge je Stunde und Person", AuszuegeJeStundeHoechst))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, auszugBodyHoechst)
	var e auszugEingabe
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		schleuseFehler(w, http.StatusBadRequest, "eingabe", "invalid json body")
		return
	}
	if len(e.Kommentare) > auszugKommentareHoechst {
		schleuseFehler(w, http.StatusBadRequest, "eingabe", fmt.Sprintf("kommentare: höchstens %d", auszugKommentareHoechst))
		return
	}
	if len(e.Dateien) > schleuse.DateienHoechst {
		schleuseFehler(w, http.StatusBadRequest, "zu-viele-dateien", fmt.Sprintf("höchstens %d Dateien je Auszug", schleuse.DateienHoechst))
		return
	}
	if !e.Beschreibung && len(e.Kommentare) == 0 && strings.TrimSpace(e.Aufgabe) == "" && len(e.Dateien) == 0 {
		schleuseFehler(w, http.StatusBadRequest, "auszug-leer", "Auszug leer: Beschreibung, Kommentare, Dateien oder Aufgabe wählen")
		return
	}

	// Board-Sperre — beim Auszug gelesen (ADR-0134 §2), und vor dem
	// Speichern noch einmal (das lokale Modell kann Minuten brauchen).
	if gesperrt, err := storage.SchleuseBoardSperre(ctx, s.db, boardID); errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse sperre read failed", err)
		return
	} else if gesperrt {
		schleuseFehler(w, http.StatusConflict, "board-gesperrt", "Board ist für die Ausgangsschleuse gesperrt")
		return
	}

	// Eigener, nutzbarer Anbieter mit hinterlegtem Schlüssel und erlaubtem Modell.
	f, err := s.freigaben.Get(ctx, e.FreigabeID)
	if errors.Is(err, storage.ErrSchleuseFreigabeNichtGefunden) {
		schleuseFehler(w, http.StatusNotFound, "anbieter-unbekannt", "freigabe not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse freigabe lookup failed", err)
		return
	}
	if !s.beurteile(f).Nutzbar {
		schleuseFehler(w, http.StatusConflict, "anbieter-nicht-nutzbar", "Anbieter ist nicht nutzbar")
		return
	}
	modellErlaubt := false
	for _, m := range f.Modelle {
		modellErlaubt = modellErlaubt || m == e.Modell
	}
	if !modellErlaubt {
		schleuseFehler(w, http.StatusBadRequest, "modell-nicht-erlaubt", "Modell ist für diesen Anbieter nicht freigegeben")
		return
	}
	meine, err := s.schluessel.List(ctx, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse keys list failed", err)
		return
	}
	hatSchluessel := false
	for _, m := range meine {
		hatSchluessel = hatSchluessel || m.FreigabeID == f.ID
	}
	if !hatSchluessel {
		schleuseFehler(w, http.StatusConflict, "kein-schluessel", "Für diesen Anbieter ist kein eigener Schlüssel hinterlegt")
		return
	}
	// Ohne lokales Modell keine Schleuse (ADR-0134 §1): VOR jeder Arbeit am
	// Text, damit kein Datensatz entsteht.
	if s.modell == nil {
		schleuseFehler(w, http.StatusServiceUnavailable, "kein-lokales-modell", "lokales Modell nicht eingerichtet (Firmenmodus mit ABOARD_LLM_OPENAI_BASE_URL auf ein internes Ziel; Grund im Start-Log) — ohne zweite Pseudonymisierungsstufe kein Auszug")
		return
	}
	// Entwürfe an Karten, die es nicht mehr gibt, zählen nicht gegen die
	// Grenze (F-1623) — sie sind in keiner Oberfläche mehr zu sehen.
	s.verwaisteEntwuerfeAufraeumen(ctx, u.ID)
	// Obergrenze der offenen Entwürfe — vorab, samt der Auszüge, die gerade
	// rechnen (KS-5). Verbindlich prüft sie AnlegenGeprueft beim Speichern.
	if ok, err := s.auszugReservieren(ctx, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse auftraege count failed", err)
		return
	} else if !ok {
		schleuseFehler(w, http.StatusConflict, "zu-viele-entwuerfe", fmt.Sprintf("höchstens %d offene Entwürfe je Person", EntwuerfeJePersonHoechst))
		return
	}
	defer s.auszugFreigeben(u.ID)

	// Karte aus dem GESPEICHERTEN Board — nie Text vom Client.
	st, err := s.ladeKarte(ctx, boardID, cardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if errors.Is(err, errCardNotFound) {
		schleuseFehler(w, http.StatusNotFound, "karte-unbekannt", "card not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load card failed", err)
		return
	}
	teile := schleuse.AuszugTeile{Aufgabe: e.Aufgabe}
	if e.Beschreibung {
		teile.Beschreibung, _ = st.karte["description"].(string)
	}
	kommentare, kommentarIDs, err := waehleKommentare(st.karte, e.Kommentare)
	if err != nil {
		schleuseFehler(w, http.StatusBadRequest, "kommentar-unbekannt", "Kommentar gibt es an dieser Karte nicht (mehr)")
		return
	}
	teile.Kommentare = kommentare
	// Karte #687 (ADR-0134 §5): Dateien lesen — Text/PDF als Teil des
	// Auszugs, Bilder nur als Hash. Die Schreibfrist der Antwort gilt ab
	// hier schon verlängert: Eine PDF kann bis zu schleuse.PDFFrist brauchen.
	if len(e.Dateien) > 0 {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(schleuse.PDFFrist*time.Duration(len(e.Dateien)) + auszugSchreibReserve)); err != nil {
			log.Printf("schleuse-auszug: SetWriteDeadline nicht möglich (%v) — es gilt die Server-Frist", err)
		}
	}
	dateien, fe := s.ladeDateien(st.karte, boardID, cardID, e.Dateien, f.Anbieter)
	if fe != nil {
		writeJSON(w, fe.status, fe.koerper())
		return
	}
	teile.Dateien = dateien.teile
	roh, err := schleuse.Normalisiere(schleuse.BaueAuszug(teile))
	if err != nil {
		schleuseFehler(w, http.StatusBadRequest, "eingabe", "Text ist kein gültiges UTF-8")
		return
	}
	if strings.TrimSpace(roh) == "" {
		// Auch ein Auszug nur aus Bildern landet hier: Ohne Text — wenigstens
		// eine Aufgabe — wüsste das Modell nicht, was es mit ihnen soll.
		schleuseFehler(w, http.StatusBadRequest, "auszug-leer", "Auszug leer: die gewählten Teile enthalten keinen Text")
		return
	}
	if n := schleuse.Zeichen(roh); n > s.cfg.GrenzeZeichen() {
		schleuseFehler(w, http.StatusRequestEntityTooLarge, "zu-gross", fmt.Sprintf("Auszug hat %d Zeichen, erlaubt sind %d", n, s.cfg.GrenzeZeichen()))
		return
	}
	liste, err := s.namensliste(ctx, boardID, st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl list failed", err)
		return
	}
	if kommentarIDs == nil {
		kommentarIDs = []string{}
	}
	auftrag := storage.SchleuseAuftrag{
		BoardID: boardID, CardID: cardID, UserID: u.ID, UserEmail: u.Email,
		FreigabeID: f.ID, Anbieter: f.Anbieter, Modell: e.Modell,
		Teile: storage.SchleuseTeile{Beschreibung: e.Beschreibung && teile.Beschreibung != "",
			Kommentare: kommentarIDs, Aufgabe: strings.TrimSpace(e.Aufgabe) != "", Dateien: dateien.textIDs},
		Bilder: dateien.bilder,
	}

	// Ab hier rechnet das lokale Modell — die Schreibfrist des Servers
	// (30 s) wird für DIESE Antwort verlängert, und der Browser bekommt
	// Lebenszeichen (ADR-0133 §3).
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(auszugModellFrist + auszugSchreibReserve)); err != nil {
		log.Printf("schleuse-auszug: SetWriteDeadline nicht möglich (%v) — es gilt die Server-Frist", err)
	}
	antwortMitLebenszeichen(w, auszugLebenszeichen, s.logf, func() (int, map[string]any) {
		return s.pseudonymisiereUndSpeichere(ctx, roh, liste, auftrag)
	})
}

// auszugReservieren zählt die offenen Aufträge der Person plus ihre gerade
// rechnenden Auszüge und reserviert einen Platz, wenn noch einer frei ist.
// Zählen und Reservieren unter einer Sperre: Parallele Anfragen derselben
// Person sehen einander. auszugFreigeben gibt den Platz zurück.
func (s *SchleuseAPI) auszugReservieren(ctx context.Context, userID string) (bool, error) {
	s.auszugMu.Lock()
	defer s.auszugMu.Unlock()
	n, err := s.auftraege.ZaehleEntwuerfe(ctx, userID)
	if err != nil {
		return false, err
	}
	if n+s.auszugLaufend[userID] >= EntwuerfeJePersonHoechst {
		return false, nil
	}
	if s.auszugLaufend == nil {
		s.auszugLaufend = map[string]int{}
	}
	s.auszugLaufend[userID]++
	return true, nil
}

// verwaisteEntwuerfeAufraeumen löscht die offenen Aufträge der Person, deren
// Karte es im gespeicherten Board nicht mehr gibt (F-1623). Ein Entwurf an
// einer gelöschten oder in ein anderes Board verschobenen Karte ist in keiner
// Oberfläche mehr erreichbar (die Liste hängt an der Karte), zählte aber bis
// zur Frist gegen die Grenze von EntwuerfeJePersonHoechst. Fehler beim Lesen
// heißen „nichts löschen“ — im Zweifel bleibt der Entwurf bis zur Frist.
// Gelöschte Boards räumt BoardRepo.Delete selbst ab.
func (s *SchleuseAPI) verwaisteEntwuerfeAufraeumen(ctx context.Context, userID string) {
	offen, err := s.auftraege.OffeneAuftraege(ctx, userID)
	if err != nil || len(offen) == 0 {
		return
	}
	karten := map[string]*kommentarBestand{} // board → Bestand (nil: nicht lesbar)
	var weg []string
	for _, o := range offen {
		b, gelesen := karten[o.BoardID]
		if !gelesen {
			if board, err := storage.NewBoardRepo(s.db).Get(ctx, o.BoardID); err == nil {
				if best, ok := leseKommentarBestand(board.StateJSON); ok {
					b = best
				}
			}
			karten[o.BoardID] = b
		}
		if b != nil && b.karten[o.CardID] == nil {
			weg = append(weg, o.ID)
		}
	}
	if len(weg) == 0 {
		return
	}
	if n, err := s.auftraege.LoescheOffene(ctx, userID, weg); err != nil {
		s.logf("aboard-server: Ausgangsschleuse: verwaiste Entwürfe nicht gelöscht: %v", err)
	} else if n > 0 {
		s.logf("aboard-server: Ausgangsschleuse: %d Entwurf/Entwürfe an gelöschten Karten entfernt", n)
	}
}

func (s *SchleuseAPI) auszugFreigeben(userID string) {
	s.auszugMu.Lock()
	defer s.auszugMu.Unlock()
	if s.auszugLaufend[userID]--; s.auszugLaufend[userID] <= 0 {
		delete(s.auszugLaufend, userID)
	}
}

// pseudonymisiereUndSpeichere führt beide Stufen aus und legt den Entwurf
// an. Jeder Fehler des lokalen Modells bricht ab, OHNE Datensatz.
func (s *SchleuseAPI) pseudonymisiereUndSpeichere(ctx context.Context, roh string, liste []schleuse.Namenseintrag,
	auftrag storage.SchleuseAuftrag) (int, map[string]any) {
	p := schleuse.NeuerPseudonymisierer(roh)
	p.FesteStufe(liste)
	mctx, cancel := context.WithTimeout(ctx, auszugModellFrist)
	defer cancel()
	erg, err := p.ModellStufe(mctx, s.modell)
	if err != nil {
		grund, msg := "lokales-modell-fehler", "lokales Modell: Fehler"
		switch {
		case errors.Is(err, schleuse.ErrModellAntwortUnlesbar):
			msg = "lokales Modell lieferte keine auswertbare Liste"
		case errors.Is(err, llm.ErrSchleuseAntwortAbgeschnitten):
			msg = "lokales Modell hat die Liste abgeschnitten (Auszug kürzen)"
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, llm.ErrProviderUnreachable):
			grund, msg = "lokales-modell-unerreichbar", "lokales Modell nicht erreichbar"
		}
		// Nur Fehlerart und Status — nie Text des Modells oder seiner
		// Fehlerantwort (KS-10): Der kann Reste des Auszugs enthalten.
		// llm.SchleuseModellAufruf liefert die Fehler schon ohne Text.
		s.logf("aboard-server: Ausgangsschleuse: Auszug abgebrochen (%s): %s", grund, redactForLog(err))
		return http.StatusBadGateway, fehlerKoerper(http.StatusBadGateway, grund, msg+" — Auszug abgebrochen, nichts gespeichert")
	}
	// Hat der Browser aufgegeben, entsteht kein Entwurf, den niemand gesehen
	// hat (KS-5).
	if ctx.Err() != nil {
		s.logf("aboard-server: Ausgangsschleuse: Auszug verworfen, Anfrage abgebrochen: %v", ctx.Err())
		return http.StatusServiceUnavailable, fehlerKoerper(http.StatusServiceUnavailable, "abgebrochen", "Anfrage abgebrochen — nichts gespeichert")
	}
	text, err := schleuse.Normalisiere(p.Text())
	if err != nil {
		return http.StatusInternalServerError, fehlerKoerper(http.StatusInternalServerError, "intern", "normalisierung gescheitert")
	}
	auftrag.Text = text
	auftrag.Ersetzungen = p.Ersetzungen()
	// Speichern nur, wenn das Board noch existiert, nicht inzwischen gesperrt
	// wurde (das Modell rechnete Minuten) und die Obergrenze hält — alles in
	// EINEM Schreibvorgang (KS-5/KS-6, ADR-0134 §2).
	neu, err := s.auftraege.AnlegenGeprueft(ctx, auftrag, p.Zuordnung(), EntwuerfeJePersonHoechst)
	switch {
	case errors.Is(err, storage.ErrSchleuseBoardGesperrt):
		return http.StatusConflict, fehlerKoerper(http.StatusConflict, "board-gesperrt", "Board wurde inzwischen für die Ausgangsschleuse gesperrt")
	case errors.Is(err, storage.ErrBoardNotFound):
		return http.StatusNotFound, fehlerKoerper(http.StatusNotFound, "karte-unbekannt", "Board wurde inzwischen gelöscht")
	case errors.Is(err, storage.ErrSchleuseZuVieleEntwuerfe):
		return http.StatusConflict, fehlerKoerper(http.StatusConflict, "zu-viele-entwuerfe", fmt.Sprintf("höchstens %d offene Entwürfe je Person", EntwuerfeJePersonHoechst))
	case err != nil:
		s.logf("aboard-server: Ausgangsschleuse: Auftrag nicht gespeichert: %v", err)
		return http.StatusInternalServerError, fehlerKoerper(http.StatusInternalServerError, "intern", "schleuse auftrag create failed")
	}
	return http.StatusCreated, map[string]any{
		"auftrag":     neu,
		"entschaerft": p.Entschaerft(),
		"modell_funde": map[string]int{
			"gemeldet": erg.Funde, "ersetzt": erg.Ersetzt, "verworfen": erg.Verworfen,
		},
	}
}

// antwortMitLebenszeichen führt arbeit aus. Ist sie vor dem ersten
// Lebenszeichen fertig, geht die Antwort ganz normal hinaus (Status +
// JSON). Sonst schreibt sie sofort 200 und dann alle abstand ein
// Leerzeichen — JSON erlaubt führenden Leerraum, der Browser liest am Ende
// ein gewöhnliches Objekt. Den eigentlichen Status trägt dieses Objekt dann
// im Feld „status“ (Fehler zusätzlich „error“ und „grund“); die Oberfläche
// wertet das Feld aus, nicht nur den HTTP-Status.
//
// Folge (F-1618): Proxy-Logs und Monitoring sehen dann 200, auch wenn der
// Auszug scheiterte — der Status ist schon gesendet, bevor das Ergebnis
// feststeht. Deshalb schreibt die Funktion in diesem Fall den echten Status
// ins Server-Log (logf); dort ist er zu zählen, nicht im Zugriffsprotokoll.
//
// Die Funktion kehrt erst zurück, wenn arbeit fertig ist — kein Schreiben
// nach dem Ende des Handlers, keine verwaiste Goroutine.
func antwortMitLebenszeichen(w http.ResponseWriter, abstand time.Duration, logf func(string, ...any), arbeit func() (int, map[string]any)) {
	type ergebnis struct {
		status int
		body   map[string]any
	}
	fertig := make(chan ergebnis, 1)
	go func() {
		// F-1632: Eine Panik in dieser Goroutine fängt kein net/http-Recover
		// auf — sie beendete den ganzen Server. Gemeldet wird nur die Art
		// (KS-10: nie Text, der aus dem Auszug stammen könnte), dazu der Stack.
		defer func() {
			if r := recover(); r != nil {
				art := fmt.Sprintf("%T", r)
				if re, ok := r.(runtime.Error); ok {
					art = re.Error()
				}
				if logf != nil {
					logf("aboard-server: Ausgangsschleuse: Panik im Auszug (%s)\n%s", art, debug.Stack())
				}
				fertig <- ergebnis{http.StatusInternalServerError, fehlerKoerper(http.StatusInternalServerError, "intern", "interner Fehler — nichts gespeichert")}
			}
		}()
		st, b := arbeit()
		fertig <- ergebnis{st, b}
	}()
	erstes := time.NewTimer(abstand)
	defer erstes.Stop()
	select {
	case e := <-fertig:
		writeJSON(w, e.status, e.body)
		return
	case <-erstes.C:
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(" "))
	_ = rc.Flush()
	takt := time.NewTicker(abstand)
	defer takt.Stop()
	for {
		select {
		case e := <-fertig:
			if e.body == nil {
				e.body = map[string]any{}
			}
			e.body["status"] = e.status
			if e.status >= 400 && logf != nil {
				grund, _ := e.body["grund"].(string)
				logf("aboard-server: Ausgangsschleuse: Auszug nach Lebenszeichen mit HTTP 200 beantwortet, eigentlicher Status %d (%s)", e.status, grund)
			}
			_ = json.NewEncoder(w).Encode(e.body)
			return
		case <-takt.C:
			_, _ = w.Write([]byte(" "))
			_ = rc.Flush()
		}
	}
}

// ── Lesen, Ändern, Löschen ─────────────────────────────────────────────

// eigenerAuftrag liest den Auftrag der Person und prüft, dass er zu Board und
// Karte im Pfad gehört. Sonst 404 — auch für fremde Aufträge.
func (s *SchleuseAPI) eigenerAuftrag(w http.ResponseWriter, r *http.Request, u *storage.User) (storage.SchleuseAuftrag, bool) {
	a, err := s.auftraege.Get(r.Context(), r.PathValue("aid"), u.ID)
	if errors.Is(err, storage.ErrSchleuseAuftragNichtGefunden) || (err == nil && (a.BoardID != r.PathValue("id") || a.CardID != r.PathValue("cardId"))) {
		schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "auftrag not found")
		return a, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse auftrag read failed", err)
		return a, false
	}
	return a, true
}

func (s *SchleuseAPI) auftragLesen(w http.ResponseWriter, r *http.Request, u *storage.User) {
	if !s.rolleOderFehler(w, r, r.PathValue("id"), u.Email, storage.RoleViewer) {
		return
	}
	a, ok := s.eigenerAuftrag(w, r, u)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auftrag": a})
}

// auftragAendern speichert den durchgesehenen Text (ADR-0134 §3.3). Body:
// {"text": "...", "basis_sha256": "<Hash, von dem die Bearbeitung ausging>"},
// optional "bilder_freigegeben": [<Bild-ids>] — die vollständige Liste der
// Bilder, die mitgehen sollen (Einzelfreigabe, ADR-0134 §5; fehlt das Feld,
// bleiben die Freigaben, wie sie sind).
// Neue Platzhalter, die es in der Zuordnung nicht gibt, und entfernte
// Platzhalter sind erlaubt — der Mensch entscheidet, was hinausgeht.
func (s *SchleuseAPI) auftragAendern(w http.ResponseWriter, r *http.Request, u *storage.User) {
	editor, err := s.rolleMindestens(r.Context(), r.PathValue("id"), u.Email, storage.RoleEditor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return
	}
	if !editor {
		schleuseFehler(w, http.StatusForbidden, "keine-rolle", "forbidden: Bearbeiten verlangt mindestens editor am Board")
		return
	}
	if _, ok := s.eigenerAuftrag(w, r, u); !ok {
		return
	}
	// Gesperrtes Board: auch keine Änderung am Entwurf (KS-11, konservativ) —
	// er soll nicht weiter für den Versand vorbereitet werden. Löschen bleibt.
	if gesperrt, err := storage.SchleuseBoardSperre(r.Context(), s.db, r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse sperre read failed", err)
		return
	} else if gesperrt {
		schleuseFehler(w, http.StatusConflict, "board-gesperrt", "Board ist für die Ausgangsschleuse gesperrt")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, auszugBodyHoechst)
	var body struct {
		Text              *string   `json:"text"`
		BasisSHA256       *string   `json:"basis_sha256"`
		BilderFreigegeben *[]string `json:"bilder_freigegeben"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Text == nil || body.BasisSHA256 == nil {
		schleuseFehler(w, http.StatusBadRequest, "eingabe", "text und basis_sha256 Pflicht")
		return
	}
	text, err := schleuse.Normalisiere(*body.Text)
	if err != nil {
		schleuseFehler(w, http.StatusBadRequest, "eingabe", "Text ist kein gültiges UTF-8")
		return
	}
	if strings.TrimSpace(text) == "" {
		schleuseFehler(w, http.StatusBadRequest, "auszug-leer", "Auszug leer — zum Verwerfen den Entwurf löschen")
		return
	}
	// Platzhalter können länger sein als die Originale; die bearbeitete
	// Fassung darf deshalb ein Viertel über der Grenze liegen.
	if grenze := s.cfg.GrenzeZeichen() * 5 / 4; schleuse.Zeichen(text) > grenze {
		schleuseFehler(w, http.StatusRequestEntityTooLarge, "zu-gross", fmt.Sprintf("Auszug hat %d Zeichen, erlaubt sind %d", schleuse.Zeichen(text), grenze))
		return
	}
	a, err := s.auftraege.AendereAuszug(r.Context(), r.PathValue("aid"), u.ID, *body.BasisSHA256, text, body.BilderFreigegeben)
	switch {
	case errors.Is(err, storage.ErrSchleuseBildUnbekannt):
		schleuseFehler(w, http.StatusBadRequest, "bild-unbekannt", "Bild steht nicht in diesem Auszug")
	case errors.Is(err, storage.ErrSchleuseAuftragNichtGefunden):
		schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "auftrag not found")
	case errors.Is(err, storage.ErrSchleuseAuftragNichtEntwurf):
		schleuseFehler(w, http.StatusConflict, "kein-entwurf", "Auftrag ist schon gesendet und lässt sich nicht mehr ändern")
	case errors.Is(err, storage.ErrSchleuseAuftragVeraltet):
		schleuseFehler(w, http.StatusConflict, "veraltet", "Auftrag wurde inzwischen geändert — neu laden")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "schleuse auftrag update failed", err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"auftrag": a})
	}
}

func (s *SchleuseAPI) auftragLoeschen(w http.ResponseWriter, r *http.Request, u *storage.User) {
	a, ok := s.eigenerAuftrag(w, r, u)
	if !ok {
		return
	}
	if err := s.auftraege.Loeschen(r.Context(), a.ID, u.ID); err != nil {
		if errors.Is(err, storage.ErrSchleuseAuftragNichtGefunden) {
			schleuseFehler(w, http.StatusConflict, "kein-entwurf", "gesendete Aufträge lassen sich nicht löschen")
			return
		}
		writeError(w, http.StatusInternalServerError, "schleuse auftrag delete failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// eigenenAuftragLoeschen löscht einen nicht gesendeten Auftrag der Person —
// ohne Board und Karte im Pfad und ohne Rolle am Board (F-1619). Fremde
// Aufträge: 404.
func (s *SchleuseAPI) eigenenAuftragLoeschen(w http.ResponseWriter, r *http.Request, u *storage.User) {
	a, err := s.auftraege.Get(r.Context(), r.PathValue("aid"), u.ID)
	if errors.Is(err, storage.ErrSchleuseAuftragNichtGefunden) {
		schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "auftrag not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse auftrag read failed", err)
		return
	}
	if err := s.auftraege.Loeschen(r.Context(), a.ID, u.ID); err != nil {
		if errors.Is(err, storage.ErrSchleuseAuftragNichtGefunden) {
			schleuseFehler(w, http.StatusConflict, "kein-entwurf", "gesendete Aufträge lassen sich nicht löschen")
			return
		}
		writeError(w, http.StatusInternalServerError, "schleuse auftrag delete failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
