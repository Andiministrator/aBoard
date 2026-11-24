package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Agenten-Listener und CLI (ADR-0129 §3, Karte #553).
//
// Agenten erreichen den Server über einen EIGENEN Listener (K-10):
//
//	ABOARD_AGENT_LISTEN       TCP-Adresse, z. B. 127.0.0.1:8081
//	ABOARD_AGENT_UNIX_SOCKET  Unix-Socket, z. B. /run/aboard/agent.sock
//
// Höchstens eine der beiden. Ist keine gesetzt, gibt es keinen Agenten-
// Zugang. Auf diesem Listener läuft AUSSCHLIESSLICH der AgentZugang vor dem
// gemeinsamen Mux: kein Forward-Auth, kein Dev-Fake, kein ACLGuard und damit
// auch kein Legacy-Bypass — unabhängig von ABOARD_TRUST_FORWARD_AUTH.

// agentHandler baut die Kette des Agenten-Listeners. Eine eigene Funktion,
// damit der Test genau diese Kette prüft (K-1) und nicht eine nachgebaute.
func agentHandler(mux http.Handler, zugang *api.AgentZugang) http.Handler {
	agent := zugang.Middleware(mux)
	// Karte #778 (ADR-0140 §2): Das Push-Skript des Board-Paars (Token
	// abs_) kommt über den Agenten-Eingang — /agent/ führt der Betrieb schon
	// an der Anmeldung vorbei. Die Schranke lässt es nur auf
	// GET /schatten/<board> durch; dort prüft der Handler es selbst, nicht
	// die Agenten-Middleware (die kennt es nicht und zählte einen
	// Fehlversuch). Bis Karte #795 galt dasselbe für das Token
	// kalender-ausgang (abk_) der Außenstelle — zurückgebaut; ein abk_-Token
	// ist jetzt ein unbekanntes Agenten-Token (401).
	return api.BoardpaarSchranke(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if api.HatBoardpaarToken(r) {
			// F-2102: Dieser Zweig geht an der Agenten-Middleware vorbei, die
			// die Identitäts-Kopfzeilen sonst löscht. Außen hinter dem
			// Anmelde-Proxy kämen sie samt Geheimnis im Handler an.
			api.FremdeKopfzeilenEntfernen(r)
			mux.ServeHTTP(w, r)
			return
		}
		agent.ServeHTTP(w, r)
	}), true)
}

// AgentPfadPraefix ist der Pfad-Eingang für Agenten am Menschen-Listener
// (Karte #616, Nachtrag zu ADR-0129 K-10). Ein Container, ein Port, ein
// Hostname: Der Betrieb leitet nur `/agent/` am Anmelde-Proxy vorbei, statt
// einen zweiten Port samt Subdomain durchs Ingress zu führen.
const AgentPfadPraefix = "/agent"

// AgentPfadEnvVar schaltet den Pfad-Eingang: "an" (Voreinstellung, auch leer)
// oder "aus". Ein anderer Wert hält den Start an, statt still eine Bedeutung
// zu raten (wie ABOARD_UHR_PRUEFUNG).
const AgentPfadEnvVar = "ABOARD_AGENT_PFAD"

// agentPfadKonfig liest den Schalter.
//
// Warum „an“ die Voreinstellung sein darf: Vor dem Menschen-Listener steht im
// Betrieb ein Anmelde-Proxy (Forward-Auth). Er schützt auch `/agent/`, solange
// der Betreiber diesen Pfad nicht ausdrücklich an ihm vorbeileitet — ein Agent
// ohne Browser-Anmeldung kommt vorher gar nicht an. Ohne Proxy (Legacy-Bypass,
// Trust aus) ist der Menschen-Port ohnehin offen, und `/agent/` verlangt dort
// mehr, nicht weniger: ein gültiges Token. Öffentlich erreichbar wird der
// Eingang also erst durch eine Proxy-Regel, die jemand bewusst schreibt.
func agentPfadKonfig() (bool, error) {
	switch v := os.Getenv(AgentPfadEnvVar); v {
	case "", "an":
		return true, nil
	case "aus":
		return false, nil
	default:
		return false, fmt.Errorf("%s=%q ungültig (erwartet an oder aus)", AgentPfadEnvVar, v)
	}
}

// hauptHandler baut die Kette des Menschen-Listeners, mit dem Pfad-Eingang
// für Agenten davor (Karte #616).
//
// Die Trennung aus K-10 bleibt im Server so streng wie mit zwei Ports: Über
// den Eingang entscheidet ausschliesslich der PFAD, nie der Inhalt der Anfrage
// (kein „Bearer dabei, also Agent“). Unter `/agent/` läuft genau die Kette des
// Agenten-Listeners (agentHandler: nur Bearer, Identitäts-Header gelöscht,
// Positivliste) — kein Forward-Auth, kein ACLGuard, kein CSRF-Schutz, kein
// Legacy-Bypass. Überall sonst läuft die Menschen-Kette unverändert, und sie
// weist Bearer-Tokens weiter ab (OhneBearer).
//
// Pfade mit `..` unter `/agent/` landen NICHT in der Menschen-Kette: Die
// Entscheidung fällt am ungesäuberten Pfad, und die Positivliste lehnt
// `..`-Segmente ab (agentSegmente). Ein kodiertes Präfix (`/%61gent/`) passt
// nicht zu RawPath, StripPrefix antwortet 404 — beides fällt zu.
func hauptHandler(mux http.Handler, acl func(http.Handler) http.Handler, zugang *api.AgentZugang, mitPfad bool, opts ...menschenOption) http.Handler {
	mensch := menschenHandler(mux, acl, opts...)
	if !mitPfad || zugang == nil {
		// Auch ohne Pfad-Eingang (ABOARD_AGENT_PFAD=aus) kann ein Proxy noch
		// `handle /agent/*` führen und solche Anfragen an der Anmeldung vorbei
		// schicken (Verify Sprint 77, F-1273). Dann gehört /agent/ keiner Kette
		// — 400, statt es der Menschen-Kette ohne Identität zu geben.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if agentPfadMehrdeutig(r.URL.Path) {
				http.Error(w, "agent path is not available on this server", http.StatusBadRequest)
				return
			}
			mensch.ServeHTTP(w, r)
		})
	}
	agent := http.StripPrefix(AgentPfadPraefix, agentHandler(mux, zugang))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, AgentPfadPraefix+"/") {
			agent.ServeHTTP(w, r)
			return
		}
		if agentPfadMehrdeutig(r.URL.Path) {
			http.Error(w, "ambiguous path: agent requests must start with exactly "+AgentPfadPraefix+"/", http.StatusBadRequest)
			return
		}
		mensch.ServeHTTP(w, r)
	})
}

// agentPfadMehrdeutig erkennt Pfade, die ein Proxy dem Agenten-Zweig zuordnen
// kann, die hier aber NICHT mit /agent/ beginnen (Abstimmung mit dem vorgeschalteten Proxy,
// 2026-09-30, gemessen mit Caddy 2.11.3): Caddy matcht `handle /agent/*` am
// BEREINIGTEN, dekodierten Pfad und ohne Rücksicht auf Groß-/Kleinschreibung,
// reicht aber den ROHEN Pfad weiter. `/AGENT/x`, `//agent/x` oder
// `/boards/../agent/x` gehen dort an der Anmeldung vorbei und landeten hier in
// der Menschen-Kette ohne Identität. Offen war das nicht (gemessen, Sprint-77-
// Abschluss, auch im Legacy-Bypass): `/AGENT/…` trifft keine Route (404), die
// anderen leitet der Mux auf den bereinigten Pfad um (307). Das hängt aber an
// der Routen-Tabelle, nicht an einer Regel — deshalb lehnt der Server solche
// Pfade ausdrücklich ab, statt zu raten, welcher Kette sie gehören. r.URL.Path ist bereits dekodiert (`/%41GENT/` kommt als
// `/AGENT/` an). `/agent` ohne Schrägstrich zählt mit (Caddy leitet es in die
// Anmeldung, hier ist es kein Endpunkt); `/agentfoo` nicht.
func agentPfadMehrdeutig(p string) bool {
	c := path.Clean("/" + strings.ToLower(p))
	return c == AgentPfadPraefix || strings.HasPrefix(c, AgentPfadPraefix+"/")
}

// agentListenerWarnung ist log.Printf — als Variable, damit ein Test die
// Warnung sieht.
var agentListenerWarnung = log.Printf

// agentListenerKonfig liest und prüft die Env-Variablen. menschAddr und
// menschSocket sind die Adressen des Menschen-Listeners: Derselbe Eingang für
// beide wäre genau die geteilte Angriffsfläche, die K-10 ausschliesst.
func agentListenerKonfig(menschAddr, menschSocket string) (netz, adresse string, err error) {
	tcp := os.Getenv("ABOARD_AGENT_LISTEN")
	sock := os.Getenv("ABOARD_AGENT_UNIX_SOCKET")
	switch {
	case tcp != "" && sock != "":
		return "", "", errors.New("ABOARD_AGENT_LISTEN und ABOARD_AGENT_UNIX_SOCKET schliessen sich aus")
	case tcp != "":
		if menschSocket == "" && tcp == menschAddr {
			return "", "", fmt.Errorf("ABOARD_AGENT_LISTEN=%s ist dieselbe Adresse wie der Menschen-Listener", tcp)
		}
		if !isLoopbackBind(tcp) {
			// Kritiker Sprint 71, K-A8: Bearer-Tokens gingen hier unverschlüsselt
			// übers Netz. Kein Abbruch — der Netzweg ist Sache des Betriebs
			// (ADR-0129 §3), ein TLS-Proxy davor kann gewollt sein —, aber laut.
			agentListenerWarnung("aboard-server: WARNUNG: ABOARD_AGENT_LISTEN=%s lauscht nicht nur auf localhost. "+
				"Bearer-Tokens laufen im Klartext, sofern kein TLS-Proxy davorsteht.", tcp)
		}
		return "tcp", tcp, nil
	case sock != "":
		if sock == menschSocket {
			return "", "", fmt.Errorf("ABOARD_AGENT_UNIX_SOCKET=%s ist derselbe Socket wie der Menschen-Listener", sock)
		}
		return "unix", sock, nil
	}
	return "", "", nil
}

// starteAgentListener startet den Agenten-Listener, falls konfiguriert. Die
// Rückgabe stop beendet ihn (nil, wenn keiner läuft). Fehler beim Binden
// brechen den Start ab: Ein Operator, der einen Agenten-Zugang konfiguriert,
// soll nicht erst am ausbleibenden Agenten merken, dass er fehlt.
func starteAgentListener(mux http.Handler, zugang *api.AgentZugang, menschAddr, menschSocket string, sockModus socketModus, serverErr chan<- error) (func(context.Context), error) {
	netz, adresse, err := agentListenerKonfig(menschAddr, menschSocket)
	if err != nil || netz == "" {
		return nil, err
	}
	if netz == "unix" {
		if err := entferneAltenSocket(adresse); err != nil {
			return nil, fmt.Errorf("agent-listener: %w", err)
		}
	}
	ln, err := net.Listen(netz, adresse)
	if err != nil {
		return nil, fmt.Errorf("agent-listener %s:%s: %w", netz, adresse, err)
	}
	if netz == "unix" {
		if err := sockModus.anwenden(adresse); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("agent-listener: %w", err)
		}
	}
	srv := &http.Server{
		Handler:           agentHandler(mux, zugang),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("aboard-server: Agenten-Listener auf %s:%s (nur Bearer-Tokens, Positivliste)", netz, adresse)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Nicht blockieren: Der Kanal hat Platz für einen Fehler, und
			// run() liest nur den ersten.
			select {
			case serverErr <- fmt.Errorf("agent-listener: %w", err):
			default:
			}
		}
	}()
	return func(ctx context.Context) {
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
		if netz == "unix" {
			_ = entferneAltenSocket(adresse)
		}
	}, nil
}

// entferneAltenSocket löscht eine übrig gebliebene Socket-Datei vor dem
// Binden (sonst EADDRINUSE nach einem Neustart) — aber NUR, wenn dort
// wirklich ein Socket liegt (Kritiker Sprint 71). Zeigt die Variable per
// Tippfehler auf eine andere Datei, etwa die Datenbank, bricht der Start ab,
// statt sie zu löschen. Fehlt die Datei, ist nichts zu tun.
func entferneAltenSocket(pfad string) error {
	fi, err := os.Lstat(pfad)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("socket-pfad %q: %w", pfad, err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("socket-pfad %q existiert und ist kein Socket — nicht gelöscht", pfad)
	}
	return os.Remove(pfad)
}

// runAgentToken verarbeitet `aboard-server agent-token create|list|revoke`.
// Arbeitet direkt auf der Datenbank (wie backup/restore) und ist damit der
// Weg für Betriebe ohne Admin-Identität. Ein Widerruf hier erreicht offene
// Verbindungen des laufenden Servers über dessen regelmässige Prüfung
// (spätestens nach 30 s), nicht sofort.
func runAgentToken(args []string) error {
	return runAgentTokenMit(args, os.Stdout)
}

// agentTokenListeZeile ist die maschinenlesbare Form von `list --json`
// (Wunsch des Partnerprojekts, Karte #608): nie ein Token, auch kein Hash — die Kennung ist
// der nicht geheime Teil neuer Tokens.
type agentTokenListeZeile struct {
	Name        string `json:"name"`
	Board       string `json:"board"`
	Role        string `json:"role"`
	Person      string `json:"person,omitempty"`
	Kennung     string `json:"kennung,omitempty"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
	TageRest    *int   `json:"tage_rest,omitempty"`
	Unbefristet bool   `json:"unbefristet,omitempty"`
	Delegierend bool   `json:"delegierend,omitempty"`
	// Karte #754: Token einer Person für mehrere Boards — dann ist Board
	// leer und Boards nennt die Liste (Stand jetzt, gelöschte fehlen).
	Mehrere bool     `json:"mehrere,omitempty"`
	Boards  []string `json:"boards,omitempty"`
	// Karte #799: Anbieter und Beleg des Trainingsausschlusses (leer = nicht angegeben).
	Anbieter string `json:"anbieter,omitempty"`
	Beleg    string `json:"beleg,omitempty"`
	// HerkunftFehlt: auf der Seite außen ein noch geltendes Token ohne vollständige
	// Angaben (Bestand aus der Zeit vor #799; Kritiker K-5). Nur ein Hinweis —
	// das Token bleibt gültig.
	HerkunftFehlt bool `json:"herkunft_fehlt,omitempty"`
}

// errAblaufWarnung: `list --warn-days N` fand aktive Tokens, die in weniger
// als N Tagen ablaufen (oder schon abgelaufen sind). main() macht daraus
// Exit ≠ 0 — für die Ablaufwache eines Dauerbetriebs.
var errAblaufWarnung = errors.New("agent-token: mindestens ein aktives Token läuft bald ab")

func runAgentTokenMit(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("Verwendung: aboard-server agent-token create|list|revoke|revoke-user [Optionen]")
	}
	// Kritiker K zu F-1934: Unterbefehl vor storage.Open prüfen — sonst legt
	// ein Tippfehler erst eine leere DB an und meldet danach den Fehler.
	switch args[0] {
	case "create", "list", "revoke", "revoke-user":
	case "-h", "-help", "--help", "help":
		// Kritiker Sprint 88 (F-C): die Optionen hängen am Unterbefehl,
		// also dorthin verweisen statt „unbekannter Unterbefehl“ zu melden.
		_, err := fmt.Fprintln(out, "Verwendung: aboard-server agent-token create|list|revoke|revoke-user [Optionen]\n"+
			"Optionen je Unterbefehl: aboard-server agent-token <unterbefehl> --help")
		return err
	default:
		return fmt.Errorf("unbekannter Unterbefehl %q (create|list|revoke|revoke-user)", args[0])
	}
	fs := flag.NewFlagSet("agent-token "+args[0], flag.ExitOnError)
	dbPath := fs.String("db", "", "Pfad zur SQLite-Datenbank (Standard: ABOARD_DB)")
	name := fs.String("name", "", "Name des Agenten (a-z, 0-9, '-')")
	board := fs.String("board", "", "Board-ID (nur create)")
	boards := fs.String("boards", "", "create: mehrere Board-IDs, durch Komma getrennt — Token einer Person (--user) für "+
		"diese Boards, nur Werkzeuge/MCP, keine rohen Ops (ADR-0139 §5); jedes Board braucht einen persönlichen Eintrag ≥ editor")
	alleBoards := fs.Bool("alle-boards", false, "create: wie --boards, mit allen Boards, auf denen --user JETZT einen "+
		"persönlichen Eintrag ≥ editor hat (später freigegebene kommen nicht dazu)")
	role := fs.String("role", storage.AgentRolleEditor, "editor, viewer, vorschlag oder intern (nur create; vorschlag: nur neue Karten "+
		"in der Swimlane „Vorschläge“, kein Lesen — nur ohne --user und --delegierend, ADR-0133 §6; intern: der interne "+
		"Agent auf dem externen Server eines Board-Paars, nur dort und nur ohne --user, ADR-0140 §8)")
	person := fs.String("user", "", "E-Mail der Person, für die der Agent handelt (create: optional; revoke-user: Pflicht)")
	tage := fs.Int("gueltig-tage", 0, fmt.Sprintf("Gültigkeit in Tagen (create; 0 = %d, höchstens %d)", storage.AgentTokenGueltigStandard, storage.AgentTokenGueltigMax))
	anbieter := fs.String("anbieter", "", fmt.Sprintf("create: wer den Agenten betreibt (höchstens %d Zeichen); auf dem externen Server "+
		"eines Board-Paars Pflicht, zusammen mit --beleg (ADR-0140 §8: Trainingsausschluss)", api.AgentAnbieterMax))
	beleg := fs.String("beleg", "", fmt.Sprintf("create: Fundstelle des vertraglichen Trainingsausschlusses, z. B. ein Pfad im "+
		"eigenen Vertragsordner (höchstens %d Zeichen); auf dem externen Server Pflicht. Das Feld dokumentiert, es prüft nichts", api.AgentBelegMax))
	alsJSON := fs.Bool("json", false, "list: maschinenlesbar (ein JSON-Array, nie ein Token)")
	warnTage := fs.Int("warn-days", 0, "list: Exit ≠ 0, wenn ein aktives Token in weniger als N Tagen abläuft")
	delegierend := fs.Bool("delegierend", false, "create: Dienst-Token für Open WebUI ohne Board und Person (ADR-0133 §4); "+
		"jede Anfrage braucht ein signiertes Nutzer-JWT, Rolle = Rolle der Person, höchstens --role")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	// F-1934: `create --name x --board b tippfehler --role viewer` legte sonst
	// still ein editor-Token an — `--role` hinter dem Tippfehler blieb ungelesen.
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}
	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: resolvedDB})
	if err != nil {
		return err
	}
	defer db.Close()
	repo := storage.NewAgentTokenRepo(db)
	users := storage.NewUserRepo(db)
	personID := func() (string, error) {
		u, err := users.GetByEmail(ctx, *person)
		if err != nil {
			return "", fmt.Errorf("person %q: %w", *person, err)
		}
		return u.ID, nil
	}

	switch args[0] {
	case "create":
		a := storage.AgentTokenAnlage{Name: *name, BoardID: *board, Role: *role, CreatedBy: "cli", GueltigTage: *tage,
			Delegierend: *delegierend, AlleMeineBoards: *alleBoards}
		for _, b := range strings.Split(*boards, ",") {
			if b = strings.TrimSpace(b); b != "" {
				a.BoardIDs = append(a.BoardIDs, b)
			}
		}
		if (len(a.BoardIDs) > 0 || *alleBoards) && strings.TrimSpace(*person) == "" {
			return errors.New("--boards/--alle-boards braucht --user: ein Token für mehrere Boards gehört immer einer Person (ADR-0139 §5)")
		}
		if *delegierend && (*board != "" || strings.TrimSpace(*person) != "") {
			return errors.New("--delegierend schliesst --board und --user aus: Board und Person kommen je Anfrage aus Pfad und Open-WebUI-JWT")
		}
		if strings.TrimSpace(*person) != "" {
			if a.UserID, err = personID(); err != nil {
				return err
			}
		}
		if a.Role == storage.AgentRolleIntern {
			// Karte #783 (ADR-0140 §8): nur auf der Seite außen.
			if grund, _ := api.InternRolleAnlegbar(ctx, storage.NewConfigRepo(db, nil, uuidString)); grund != "" {
				return errors.New(grund)
			}
		}
		// Karte #799: Anbieter und Beleg — auf der Seite außen Pflicht, sonst leer erlaubt.
		var grund string
		if a.Anbieter, a.Beleg, grund, _ = api.PruefeAgentHerkunft(ctx, storage.NewConfigRepo(db, nil, uuidString), *anbieter, *beleg); grund != "" {
			return errors.New(grund)
		}
		klartext, t, err := repo.Anlegen(ctx, a)
		if err != nil {
			return err
		}
		fuer := ""
		if t.UserID != "" {
			fuer = ", für " + *person
		}
		if t.Role == storage.AgentRolleIntern {
			fmt.Fprintf(out, "Token %q für den internen Agenten angelegt: Board %s, gültig bis %s. Es liest das Board über /agent/intern/%s/…, ändert sofort nur Spalte und Priorität und legt alles andere als Vorschlag ab, den ein Mensch im Board bestätigt.\n", t.Name, t.BoardID, t.ExpiresAt, t.BoardID)
		} else if t.Role == storage.AgentRolleVorschlag {
			fmt.Fprintf(out, "Vorschlags-Token %q angelegt: Board %s, gültig bis %s. Es legt nur neue Karten in der Swimlane „Vorschläge“ an und liest nichts.\n", t.Name, t.BoardID, t.ExpiresAt)
		} else if t.Delegierend {
			fmt.Fprintf(out, "Delegierendes Dienst-Token %q angelegt: kein Board, Rolle höchstens %s, gültig bis %s.\n", t.Name, t.Role, t.ExpiresAt)
			fmt.Fprintf(out, "Es wirkt nur mit signiertem %s und gesetztem %s (docs/handbuch/server/konfiguration.md (ABOARD_OWUI_*)).\n", api.OWUIUserJWTHeader, api.OWUIJWTSecretFileEnv)
		} else if t.Mehrere {
			fmt.Fprintf(out, "Agent %q angelegt: %d Boards (%s), Rolle %s%s, gültig bis %s.\n", t.Name, len(t.Boards), strings.Join(t.Boards, ", "), t.Role, fuer, t.ExpiresAt)
			fmt.Fprintln(out, "Nur Werkzeuge (/werkzeuge/…, /openapi.json, /mcp), keine rohen Ops. Später freigegebene Boards kommen nicht dazu.")
		} else {
			fmt.Fprintf(out, "Agent %q angelegt: Board %s, Rolle %s%s, gültig bis %s.\n", t.Name, t.BoardID, t.Role, fuer, t.ExpiresAt)
		}
		fmt.Fprintf(out, "Token (wird nur jetzt angezeigt):\n%s\n", klartext)
		return nil
	case "list":
		l, err := repo.List(ctx)
		if err != nil {
			return err
		}
		jetzt := time.Now().UTC()
		// Seite des Board-Paars: nur für den Hinweis „fehlt“. Ist sie nicht lesbar,
		// bleibt der Hinweis aus — die Liste ist lesend und soll nicht daran scheitern.
		seite, seiteErr := api.BoardpaarSeiteMitFehler(ctx, storage.NewConfigRepo(db, nil, uuidString))
		aussen := seiteErr == nil && seite == api.BoardpaarSeiteAussen
		zeilen := make([]agentTokenListeZeile, 0, len(l))
		warnen := false
		for _, t := range l {
			z := agentTokenListeZeile{Name: t.Name, Board: t.BoardID, Role: t.Role, Person: t.UserEmail,
				Kennung: t.Kennung, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
				Unbefristet: t.ExpiresAt == "", Delegierend: t.Delegierend, Mehrere: t.Mehrere, Boards: t.Boards,
				Anbieter: t.Anbieter, Beleg: t.Beleg}
			if t.ExpiresAt != "" {
				if ende, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil {
					rest := int(ende.Sub(jetzt).Hours() / 24)
					if ende.Before(jetzt) {
						rest = -1
					}
					z.TageRest = &rest
					// Session-Kritiker Sprint 78, K-2: nur Tokens, die NOCH gelten und
					// bald ablaufen. Ein schon abgelaufenes (nicht widerrufenes) hätte
					// sonst dauerhaft Exit 3 ergeben und jeden echten neuen Ablauf
					// verdeckt; es steht in der Liste mit tage_rest = -1, und ein neues
					// Token gleichen Namens widerruft es beim Anlegen.
					if t.RevokedAt == "" && *warnTage > 0 && !ende.Before(jetzt) && ende.Before(jetzt.Add(time.Duration(*warnTage)*24*time.Hour)) {
						warnen = true
					}
				}
			}
			// Nur geltende Tokens: widerrufene und abgelaufene brauchen keine Nachtragung.
			z.HerkunftFehlt = aussen && t.RevokedAt == "" && (z.TageRest == nil || *z.TageRest >= 0) &&
				(strings.TrimSpace(z.Anbieter) == "" || strings.TrimSpace(z.Beleg) == "")
			zeilen = append(zeilen, z)
		}
		if *alsJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(zeilen); err != nil {
				return err
			}
		} else {
			tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tBOARD\tROLLE\tPERSON\tKENNUNG\tANGELEGT\tLÄUFT AB AM\tWIDERRUFEN\tANBIETER\tBELEG")
			for _, z := range zeilen {
				ab := z.ExpiresAt
				if z.Unbefristet {
					ab = "unbefristet"
				}
				board := z.Board
				if z.Delegierend {
					board = "(delegierend)"
				}
				if z.Mehrere {
					board = "(" + strings.Join(z.Boards, ",") + ")"
				}
				anb, bel := z.Anbieter, z.Beleg
				if z.HerkunftFehlt {
					if anb == "" {
						anb = "(fehlt)"
					}
					if bel == "" {
						bel = "(fehlt)"
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", z.Name, board, z.Role, z.Person, z.Kennung, z.CreatedAt, ab, z.RevokedAt, anb, bel)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
		if warnen {
			return fmt.Errorf("%w (Schwelle %d Tage)", errAblaufWarnung, *warnTage)
		}
		return nil
	case "revoke":
		if _, err := repo.Revoke(ctx, *name, "cli"); err != nil {
			return err
		}
		fmt.Fprintf(out, "Token von %q widerrufen. Offene Verbindungen schliesst der laufende Server spätestens nach 30 s.\n", *name)
		return nil
	case "revoke-user":
		if strings.TrimSpace(*person) == "" {
			return errors.New("revoke-user braucht --user <E-Mail>")
		}
		id, err := personID()
		if err != nil {
			return err
		}
		ids, err := repo.RevokeFuerPerson(ctx, id, "cli")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d Token(s) von %s widerrufen. Offene Verbindungen schliesst der laufende Server spätestens nach 30 s.\n", len(ids), *person)
		return nil
	}
	return fmt.Errorf("unbekannter Unterbefehl %q (create|list|revoke|revoke-user)", args[0])
}
