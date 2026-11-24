package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Agenten-Zugang (ADR-0129 §3, Karte #553).
//
// Agenten erreichen den Server über einen EIGENEN Listener (K-10). Dort gilt
// nur diese Middleware: Bearer-Token → AgentPrincipal → Routen-Positivliste.
// Forward-Auth, Dev-Fake-Header und der ACLGuard laufen dort nicht. Damit
// entsteht für einen Agenten nie eine ForwardAuthIdentity, und er berührt
// users/BootstrapNewUser nicht (K-1) — auch nicht, wenn der Server mit
// Trust aus läuft (Legacy-Bypass gibt es auf diesem Listener nicht).

// AgentPrincipal ist die Identität eines Agenten im Request-Kontext. Ein
// eigener Typ, keine ForwardAuthIdentity: Code, der „gibt es eine Identität?“
// fragt, sieht einen Agenten deshalb NICHT als angemeldeten Menschen.
type AgentPrincipal struct {
	TokenID string
	Name    string
	BoardID string
	// Role ist die WIRKSAME Rolle: bei einem Token mit Person das Minimum aus
	// Token-Rolle und deren aktueller ACL-Rolle (ADR-0130 §6, Karte #608).
	Role string
	// OnBehalfOf: E-Mail der Person, für die der Agent handelt; leer bei
	// Projekt-Tokens (Team-Container). Gestempelt auf Protokoll und Kommentar.
	OnBehalfOf string
	// Delegiert: Der Principal stammt von einem delegierenden Dienst-Token
	// (ADR-0133 §4) — Person aus dem OWUI-JWT, Board aus dem Pfad. Alle
	// Personen teilen sich dann denselben Akteur `agent:<name>`; Ops, deren
	// Erlaubnis am Akteur hängt, lehnt agentPayload deshalb ab.
	Delegiert bool
	// delegiertBis: Ablauf des JWT plus Uhrentoleranz. Länger lebt keine
	// Verbindung, die auf diesem JWT beruht.
	delegiertBis time.Time
	// MehrBoard (ADR-0139 §5, Karte #754): Der Principal stammt von einem
	// Personen-Token für mehrere Boards. Das Board kommt aus dem Werkzeug-Pfad
	// und ist gegen die Liste des Tokens UND die persönliche ACL der Person
	// geprüft (mehrBoardPrincipalFuer); die rohen /boards/…-Wege gibt es für
	// dieses Token nicht.
	MehrBoard bool
	// boardRollen (nur MehrBoard, nur auf Wegen ohne Board — `boards`,
	// `termine_lesen`): die Boards, auf denen das Token gerade wirkt, mit
	// wirksamer Rolle.
	boardRollen map[string]string
	// tokenRolle: die Rolle des Tokens selbst (Obergrenze). Braucht das
	// Werkzeug `boards` (Karte #671), das ohne Board im Pfad die Rollen der
	// Person auf allen Boards kappt — dort ist Role leer.
	tokenRolle string
	// mcpPrincipal (nur auf /mcp, Karte #753, ADR-0139 §3): bildet für einen
	// Werkzeug-Pfad den Principal so, wie die Middleware es für eine Anfrage
	// auf diesen Pfad täte — beim delegierenden Token mit dem in Schritt A
	// aufbewahrten JWT. Schritt B (tools/call) ruft es auf, danach werkzeugDarf.
	mcpPrincipal func(ctx context.Context, segs []string) (*AgentPrincipal, int, string)
	// mcpPerson (nur delegierend auf /mcp): die Person aus dem aufbewahrten
	// JWT, still geprüft — ohne Log und ohne ACL. Nur für den Schlüssel der
	// Listen-Ratenbremse (Verify 08.10., W-1: der Weg über mcpPrincipal
	// schrieb bei jedem ungültigen JWT eine Logzeile, auch vor einem 429).
	mcpPerson func() string
}

// Akteur ist der Name, unter dem der Server Ops und Activity-Einträge
// eines Agenten stempelt.
func (p *AgentPrincipal) Akteur() string { return "agent:" + p.Name }

const contextKeyAgent contextKey = 1000

// AgentFromContext liefert den Agenten des Requests oder nil.
func AgentFromContext(ctx context.Context) *AgentPrincipal {
	p, _ := ctx.Value(contextKeyAgent).(*AgentPrincipal)
	return p
}

// agentTokenPruefer ist der Teil von storage.AgentTokenRepo, den die
// Middleware braucht (Tests setzen einen Stub ein).
type agentTokenPruefer interface {
	Lookup(ctx context.Context, klartext string) (*storage.AgentToken, error)
	IstAktiv(ctx context.Context, id string) (bool, error)
}

// AgentZugang ist die Middleware des Agenten-Listeners samt Register der
// offenen Verbindungen (für den Widerruf, K-4).
type AgentZugang struct {
	tokens agentTokenPruefer
	// pruefIntervall: So oft prüft eine offene Verbindung, ob ihr Token noch
	// gilt. Deckt den Widerruf aus einem anderen Prozess (CLI) ab; ein
	// Widerruf über die Admin-API im selben Prozess schliesst sofort.
	pruefIntervall time.Duration

	mu    sync.Mutex
	offen map[string]map[*verbindung]struct{} // Token-ID → offene Requests

	// bremse: Fehlversuch-Bremse (Karte #608, agent_bremse.go).
	bremse *fehlversuchBremse

	// Delegation für Open WebUI (ADR-0133 §4, agent_owui.go). nil = aus:
	// jedes delegierende Token wird abgelehnt.
	owuiGeheimnis []byte
	owuiACL       owuiRollenLeser
	// owuiHoechstalter: Höchstalter eines OWUI-JWT ab iat (Karte #730,
	// ABOARD_OWUI_JWT_MAX_AGE_SECONDS); 0 = Standard owuiHoechstalter.
	owuiHoechstalter time.Duration
	// uhr: nil = time.Now; Tests stellen sie für die JWT-Fristen.
	uhr func() time.Time
}

func (z *AgentZugang) jetzt() time.Time {
	if z.uhr != nil {
		return z.uhr()
	}
	return time.Now()
}

type verbindung struct{ abbrechen context.CancelFunc }

// NewAgentZugang erzeugt die Middleware.
func NewAgentZugang(tokens agentTokenPruefer) *AgentZugang {
	return &AgentZugang{tokens: tokens, pruefIntervall: 30 * time.Second, offen: map[string]map[*verbindung]struct{}{},
		bremse: neueBremse(nil)}
}

// identitaetsHeader werden auf dem Agenten-Listener entfernt. Kein Handler
// liest sie heute direkt (nur withForwardAuth, der hier nicht läuft) — das
// Entfernen stellt sicher, dass das so bleibt, falls einer es künftig tut.
// Eine Liste für alle Stellen (F-2102): Remote-Name liest withForwardAuth
// ebenfalls, stand aber nur in der Kopie des Dev-Fake-Zweigs.
var identitaetsHeader = []string{"Remote-User", "Remote-Email", "Remote-Groups", "Remote-Name", forwardAuthSecretHeader}

// IdentitaetsHeaderEntfernen löscht die Identitäts-Kopfzeilen der Anmeldung
// samt Proxy-Geheimnis aus der Anfrage.
func IdentitaetsHeaderEntfernen(r *http.Request) {
	for _, h := range identitaetsHeader {
		r.Header.Del(h)
	}
}

// FremdeKopfzeilenEntfernen löscht alles, was die Agenten-Middleware vor
// jedem Token entfernt: Identitäts-Kopfzeilen und die OWUI-Nutzer-Kopfzeilen
// (ADR-0133 §4). Für Wege am Agenten-Eingang, die an der Middleware
// vorbeigehen (Board-Paar-Tokens, F-2102 samt Kritik K-2).
func FremdeKopfzeilenEntfernen(r *http.Request) {
	IdentitaetsHeaderEntfernen(r)
	_, _ = owuiHeaderEntfernen(r)
}

// Middleware: Bearer-Token prüfen, Positivliste anwenden, Verbindung für den
// Widerruf registrieren. Reihenfolge der Antworten: fehlendes/ungültiges
// Token → 401 (auch für Pfade, die es gar nicht gibt — ein Unbekannter
// erfährt nichts über die Routen), gültiges Token auf verbotener Route → 403.
func (z *AgentZugang) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		IdentitaetsHeaderEntfernen(r)
		// ADR-0133 §4: Das OWUI-JWT wird hier gelesen und ALLE OWUI-Nutzer-
		// Kopfzeilen (auch die Klartext-Rückfälle) werden entfernt — bei jedem
		// Token, nicht nur bei delegierenden.
		owuiJWT, owuiAnzahl := owuiHeaderEntfernen(r)
		klartext, ok := bearerToken(r)
		// Fehlversuch-Bremse (Karte #608) VOR der Prüfung: Ein gesperrter
		// Schlüssel bekommt 429, auch mit richtigem Token — sonst wäre die
		// Sperre ein Orakel für das richtige.
		schluessel := z.bremsSchluessel(r, klartext)
		if gesperrt, rest := z.bremse.gesperrt(schluessel); gesperrt {
			w.Header().Set("Retry-After", fmt.Sprint(int(rest.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
			return
		}
		if !ok {
			z.bremse.fehlschlag(schluessel)
			w.Header().Set("WWW-Authenticate", `Bearer realm="aboard-agent"`)
			writeError(w, http.StatusUnauthorized, "bearer token required")
			return
		}
		t, err := z.tokens.Lookup(r.Context(), klartext)
		if errors.Is(err, storage.ErrAgentTokenUnbekannt) {
			z.bremse.fehlschlag(schluessel)
			w.Header().Set("WWW-Authenticate", `Bearer realm="aboard-agent", error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "invalid or revoked token")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "token check failed", err)
			return
		}
		var p *AgentPrincipal
		if t.Delegierend {
			// Zuerst und ausschliesslich: Ein delegierendes Token darf nie in
			// den Rückfall unten laufen (leere WirksameRolle → Token-Rolle).
			// JWT-Fehler zählen bewusst NICHT in der Fehlversuch-Bremse: Das
			// Token ist hier schon gültig, Raten der HS256-Signatur ist
			// aussichtslos, und eine Sperre träfe alle Personen hinter OWUI.
			var status int
			var meldung string
			p, status, meldung = z.delegierterPrincipal(r, t, owuiJWT, owuiAnzahl)
			if p == nil {
				if status == http.StatusUnauthorized {
					w.Header().Set("WWW-Authenticate", `Bearer realm="aboard-agent", error="invalid_token"`)
				}
				writeError(w, status, meldung)
				return
			}
		} else if t.Mehrere {
			// Karte #754: ebenfalls zuerst und ausschliesslich — die leere
			// WirksameRolle darf nie in den Rückfall auf die Token-Rolle laufen.
			segs, segsOK := agentSegmente(r.URL.EscapedPath())
			var status int
			var meldung string
			p, status, meldung = z.mehrBoardPrincipalFuer(r.Context(), segs, segsOK, t)
			if p == nil {
				writeError(w, status, meldung)
				return
			}
		} else {
			rolle := t.WirksameRolle
			if rolle == "" { // Prüfer ohne Rollen-Ermittlung (Test-Stubs)
				rolle = t.Role
			}
			p = &AgentPrincipal{TokenID: t.ID, Name: t.Name, BoardID: t.BoardID, Role: rolle, OnBehalfOf: t.UserEmail, tokenRolle: t.Role}
		}
		if segs, ok := agentSegmente(r.URL.EscapedPath()); ok && werkzeugIstMCP(segs) {
			// Karte #753 (ADR-0139 §3 Schritt A): Bremse, Token und Widerruf
			// sind geprüft (einmal). Das JWT geht nicht verloren, sondern
			// wartet für Schritt B in dieser Funktion; Bremse und Wache
			// zählen dort nicht noch einmal.
			basisP := *p
			if t.Delegierend {
				p.mcpPrincipal = func(c context.Context, segs []string) (*AgentPrincipal, int, string) {
					return z.delegierterPrincipalFuer(c, segs, true, t, owuiJWT, owuiAnzahl)
				}
				p.mcpPerson = func() string { return z.owuiPersonStill(owuiJWT, owuiAnzahl) }
			} else if t.Mehrere {
				// Karte #754: Schritt B bildet das Board aus dem Werkzeug-Pfad
				// des tools/call — dieselbe Funktion wie die Middleware.
				p.mcpPrincipal = func(c context.Context, segs []string) (*AgentPrincipal, int, string) {
					return z.mehrBoardPrincipalFuer(c, segs, true, t)
				}
			} else {
				p.mcpPrincipal = func(context.Context, []string) (*AgentPrincipal, int, string) {
					kopie := basisP
					return &kopie, 0, ""
				}
			}
		}
		if !agentDarf(p, r) {
			if istWerkzeugPfad(r) {
				// Karte #671: Diese Meldung liest ein Modell in Open WebUI. Sie
				// nennt den Grund so, dass es der Person antworten kann, ohne
				// zu verraten, welche Boards es gibt.
				writeError(w, http.StatusForbidden, verbotenMeldungFuer(p))
				return
			}
			writeError(w, http.StatusForbidden, "forbidden for agent")
			return
		}

		basis := context.WithValue(r.Context(), contextKeyAgent, p)
		var nochBerechtigt func(context.Context) (bool, error)
		if p.Delegiert {
			// Keine Verbindung überlebt ihr JWT (Ablauf + Toleranz), und die
			// Wache prüft zusätzlich die ACL-Rolle der Person — sofern die
			// Anfrage ein Board hat (Karte #671: `boards` und die
			// Spezifikation haben keines; dort gibt es nichts nachzuprüfen).
			var stopp context.CancelFunc
			basis, stopp = context.WithDeadline(basis, p.delegiertBis)
			defer stopp()
			if p.BoardID != "" {
				tokenRolle := t.Role
				nochBerechtigt = func(c context.Context) (bool, error) { return z.delegiertNochBerechtigt(c, p, tokenRolle) }
			}
		} else if p.MehrBoard && p.BoardID != "" {
			// Karte #754: Entzieht der Owner der Person das Board (oder fällt
			// es aus der Liste), endet auch die offene Verbindung.
			nochBerechtigt = func(c context.Context) (bool, error) { return z.mehrBoardNochBerechtigt(c, p) }
		}
		ctx, abbrechen := context.WithCancel(basis)
		v := &verbindung{abbrechen: abbrechen}
		z.anmelden(t.ID, v)
		fertig := make(chan struct{})
		defer func() {
			close(fertig)
			z.abmelden(t.ID, v)
			abbrechen()
		}()
		// Zweite Prüfung NACH dem Anmelden (Kritiker Sprint 71, K-A1): Fällt ein
		// Widerruf samt SchliesseAlle zwischen Lookup und anmelden, fände
		// SchliesseAlle diese Verbindung noch nicht, und der Request liefe mit
		// widerrufenem Token weiter. Ab dem Anmelden erreicht jeder Widerruf die
		// Verbindung; davor fängt ihn diese Prüfung. Ein Prüffehler lässt den
		// Request durch — Lookup hat eben erst gültig geantwortet.
		if aktiv, err := z.tokens.IstAktiv(r.Context(), t.ID); err == nil && !aktiv {
			w.Header().Set("WWW-Authenticate", `Bearer realm="aboard-agent", error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "invalid or revoked token")
			return
		}
		go z.wache(ctx, t.ID, abbrechen, fertig, nochBerechtigt)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// wache prüft in Abständen, ob das Token noch gilt, und bricht die
// Verbindung ab, sobald es widerrufen ist. Ist die Prüfung selbst gestört
// (Datenbankfehler), bleibt die Verbindung offen — Ausfall in Richtung
// Nichtstun; der nächste Request prüft ohnehin neu.
//
// nochBerechtigt (nur delegierte Verbindungen, ADR-0133 §4): zusätzlich die
// ACL-Rolle der Person — gleiche Ausfallrichtung wie beim Token.
func (z *AgentZugang) wache(ctx context.Context, tokenID string, abbrechen context.CancelFunc, fertig <-chan struct{},
	nochBerechtigt func(context.Context) (bool, error)) {
	tick := time.NewTicker(z.pruefIntervall)
	defer tick.Stop()
	for {
		select {
		case <-fertig:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			aktiv, err := z.tokens.IstAktiv(context.Background(), tokenID)
			if err != nil {
				log.Printf("aboard-server: agent-token-prüfung fehlgeschlagen (Verbindung bleibt offen): %v", err)
				continue
			}
			if !aktiv {
				abbrechen()
				return
			}
			if nochBerechtigt != nil {
				ok, err := nochBerechtigt(context.Background())
				if err != nil {
					log.Printf("aboard-server: rollen-prüfung der delegierten verbindung fehlgeschlagen (Verbindung bleibt offen): %v", err)
					continue
				}
				if !ok {
					abbrechen()
					return
				}
			}
		}
	}
}

func (z *AgentZugang) anmelden(tokenID string, v *verbindung) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.offen[tokenID] == nil {
		z.offen[tokenID] = map[*verbindung]struct{}{}
	}
	z.offen[tokenID][v] = struct{}{}
}

func (z *AgentZugang) abmelden(tokenID string, v *verbindung) {
	z.mu.Lock()
	defer z.mu.Unlock()
	delete(z.offen[tokenID], v)
	if len(z.offen[tokenID]) == 0 {
		delete(z.offen, tokenID)
	}
}

// SchliesseAlle bricht alle offenen Requests eines Tokens ab — SSE-Streams
// eingeschlossen (K-4). Aufgerufen nach einem Widerruf über die Admin-API.
// Liefert die Zahl der geschlossenen Verbindungen.
func (z *AgentZugang) SchliesseAlle(tokenID string) int {
	z.mu.Lock()
	defer z.mu.Unlock()
	n := 0
	for v := range z.offen[tokenID] {
		v.abbrechen()
		n++
	}
	return n
}

// bearerToken liest `Authorization: Bearer <token>`.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const praefix = "Bearer "
	if len(h) <= len(praefix) || !strings.EqualFold(h[:len(praefix)], praefix) {
		return "", false
	}
	t := strings.TrimSpace(h[len(praefix):])
	return t, t != ""
}

// agentDarf ist die Routen-Positivliste (ADR-0129 §3, K-2). Alles, was hier
// nicht ausdrücklich steht, ist verboten — auch jeder künftige Endpunkt.
//
// Erlaubt, jeweils NUR für das eigene Board:
//
//	GET/HEAD /boards/{eigenes}
//	GET      /boards/{eigenes}/events
//	GET/HEAD /boards/{eigenes}/attachments/{card}/{att}
//	GET/HEAD /boards/{eigenes}/board-attachments/{att}
//	POST     /boards/{eigenes}/ops                          (nur editor)
//	GET/HEAD /boards/{eigenes}/ich                          (Karte #602)
//	POST     /boards/{eigenes}/attachments/{card}           (nur editor)
//	POST     /boards/{eigenes}/board-attachments            (nur editor)
//
// Kein DELETE auf Anhänge (F-1882, Sprint-86-Kritiker): Ein Agent löscht
// eine Anhang-Datei nur über das Werkzeug-Paar anhang_loeschen_pruefen →
// anhang_loeschen (Bestätigungscode, Datei und Eintrag zusammen, Hintergrund-
// bild geschützt). Der rohe DELETE nahm die Datei ohne Code und ließ den
// Eintrag stehen, den ein Agent seit #766 roh nicht mehr entfernen kann.
//
// Rolle vorschlag (ADR-0133 §6, Karte #670) — davor und ausschliesslich:
//
//	POST     /boards/{eigenes}/ops                          (nur card.upsert NEUER Karten, boards_ops_vorschlag.go)
//	GET/HEAD /boards/{eigenes}/ich                          (eigene Token-Angaben, nichts vom Board)
//
// Kein Lesen des Boards, keine Ereignisse, keine Anhänge: Die Instanz hinter
// diesem Token liest fremde Webseiten, jedes Leserecht wäre ein Abflussweg.
//
// Rolle intern (ADR-0140 §8, Karte #783) — ganz am Anfang und ausschliesslich
// die fünf Wege aus internDarf (vorschlaege.go):
//
//	GET  /intern/{eigenes}/board | /suche | /karte/{nr}
//	POST /intern/{eigenes}/karte/{nr}/felder | /vorschlag
//
// Werkzeug-Wege für Open WebUI (ADR-0133 §5, Karte #671) — eigene,
// abschliessende Liste in werkzeugDarf, nie für die Rolle vorschlag. Ein
// DELEGIERENDES Token (Open WebUI) erreicht ausschliesslich diese Wege, keinen
// der `/boards/…`-Wege oben (Kritiker Sprint 82, K1-1):
//
//	GET  /openapi.json                                            (Spezifikation)
//	GET  /werkzeuge/boards                                        (boards)
//	GET  /werkzeuge/boards/{eigenes}                              (board_lesen)
//	POST /werkzeuge/boards/{eigenes}/karten                       (karte_anlegen, nur editor)
//	POST /werkzeuge/boards/{eigenes}/karten/{karte}/kommentare    (kommentieren, nur editor)
//	GET  /werkzeuge/boards/{eigenes}/karten/{karte}               (karte_zeigen)
//	POST /werkzeuge/boards/{eigenes}/karten/{karte}/verschieben   (verschieben, nur editor)
//	POST /werkzeuge/boards/{eigenes}/findings                     (finding_anlegen, nur editor)
//
// Die Pfadsegmente werden wie im ServeMux einzeln dekodiert. Ein Segment,
// das nach dem Dekodieren einen Schrägstrich enthält oder `.`/`..` ist, führt
// zur Ablehnung: Sonst könnte die Prüfung hier ein anderes Board sehen als
// der Mux, der danach routet.
func agentDarf(p *AgentPrincipal, r *http.Request) bool {
	segs, ok := agentSegmente(r.URL.EscapedPath())
	if istInternRolle(p) {
		// Karte #783 (ADR-0140 §8, Stelle 1): Die Rolle intern hat ihre eigene,
		// abschliessende Liste — VOR allen anderen Fällen, sonst erbte sie
		// Werkzeuge, Blob-Lesen, Ereignisse oder Anhänge (Klasse 165).
		return internDarf(p, r.Method, segs, ok)
	}
	if ok && len(segs) > 0 && (segs[0] == werkzeugSpezSegment || segs[0] == werkzeugSegment || segs[0] == werkzeugMCPSegment) {
		return werkzeugDarf(p, r.Method, segs)
	}
	if p.Delegiert || p.MehrBoard {
		// ADR-0133 §4/§5 (Kritiker Sprint 82, K1-1): Open WebUI handelt NUR über
		// die Werkzeuge. Die rohe Agenten-API — Ops beliebiger Art, Anhänge,
		// Ereignisstrom, Board-Blob — gibt es für ein delegierendes Token
		// nicht; sonst löschte ein Modell mit `card.delete` oder benennte das
		// Board um, obwohl kein Werkzeug das kann. Die Werkzeuge schreiben
		// intern über postOps; das ist kein Weg dieses Tokens.
		// Karte #754 (ADR-0139 §5): Ein Token für mehrere Boards ebenso — die
		// rohen Ops bleiben bei einem Board je Token.
		return false
	}
	if !ok || len(segs) < 2 || segs[0] != "boards" || segs[1] != p.BoardID {
		return false
	}
	lesen := r.Method == http.MethodGet || r.Method == http.MethodHead
	editor := p.Role == storage.AgentRolleEditor
	rest := segs[2:]
	if p.Role == storage.AgentRolleVorschlag {
		// Eigene, abschliessende Liste VOR den übrigen Fällen: Die Lese-Fälle
		// unten fragen die Rolle nicht ab und gälten sonst auch hier.
		switch {
		case len(rest) == 1 && rest[0] == "ops":
			return r.Method == http.MethodPost
		case len(rest) == 1 && rest[0] == "ich":
			return lesen
		}
		return false
	}
	switch {
	case len(rest) == 0:
		return lesen
	case len(rest) == 1 && rest[0] == "events":
		return r.Method == http.MethodGet
	case len(rest) == 1 && rest[0] == "ich":
		return lesen
	case len(rest) == 1 && rest[0] == "ops":
		return r.Method == http.MethodPost && editor
	case rest[0] == "attachments" && len(rest) == 2:
		return r.Method == http.MethodPost && editor
	case rest[0] == "attachments" && len(rest) == 3:
		return lesen
	case rest[0] == "board-attachments" && len(rest) == 1:
		return r.Method == http.MethodPost && editor
	case rest[0] == "board-attachments" && len(rest) == 2:
		return lesen
	}
	return false
}

func agentSegmente(escaped string) ([]string, bool) {
	roh := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	segs := make([]string, 0, len(roh))
	for _, s := range roh {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || d == "." || d == ".." || strings.Contains(d, "/") {
			return nil, false
		}
		segs = append(segs, d)
	}
	return segs, true
}

// OhneBearer weist auf dem Menschen-Listener jeden Request mit einem
// Agenten-Token ab (K-10). Ein Agenten-Token soll dort nicht einmal geprüft
// werden: Die beiden Zugänge teilen sich keine Angriffsfläche, und ein
// versehentlich falsch konfigurierter Agent fällt sofort auf, statt über den
// Legacy-Bypass anonym durchzukommen.
//
// Nur Tokens mit dem Präfix abt_ (Kritiker Sprint 71): Ein anderer Bearer
// kann vom vorgeschalteten Proxy stammen (oauth2-proxy reicht auf Wunsch
// seinen eigenen weiter). Ihn abzuweisen, sperrte jeden Menschen aus. Der
// Server wertet einen fremden Bearer auf diesem Listener ohnehin nicht aus.
func OhneBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t, ok := bearerToken(r); ok && strings.HasPrefix(t, storage.AgentTokenPraefix) {
			writeError(w, http.StatusUnauthorized, "bearer tokens are only accepted on the agent entry (/agent/ or the agent listener)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// agentIch — GET /boards/{id}/ich (Karte #602): Auskunft über das eigene
// Token. Das Board ist durch agentDarf schon geprüft (nur das eigene). Für
// Menschen gibt es /me; hier antwortet der Server ihnen mit 404, damit keine
// zweite, abweichende Auskunft über Menschen entsteht.
func agentIch(w http.ResponseWriter, r *http.Request) {
	p := AgentFromContext(r.Context())
	if p == nil {
		writeError(w, http.StatusNotFound, "only for agents (humans: GET /me)")
		return
	}
	var fuer any
	if p.OnBehalfOf != "" {
		fuer = p.OnBehalfOf
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       p.Name,
		"actor":      p.Akteur(),
		"board":      p.BoardID,
		"role":       p.Role,
		"onBehalfOf": fuer,
	})
}
