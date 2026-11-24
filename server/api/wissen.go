package api

// wissen.go — Lesesicht auf die gemeinsame Wissensquelle (ADR-0137 §3, Karte #739).
//
//	GET /wissen/index        → { name, stand?, bearbeiten_url?, seiten:[{pfad,titel,groesse}], gekappt? }
//	GET /wissen/seite?pfad=  → { pfad, titel, inhalt, bearbeiten? }
//	GET /wissen/suche?q=     → { treffer:[{pfad,titel,schnipsel?,zeile?}], gekappt? }
//
// Wer darf (ADR-0137 §5): angemeldete Personen mit Konto dieser Instanz; bei
// wissen.sichtbar=admins nur Admins; bei wissen.sichtbar=gruppe:<name> nur,
// wen der Anmeldedienst in Remote-Groups dieser Gruppe zuordnet, dazu Admins
// (F-1863: „Konto“ heisst hier jede über den Proxy angemeldete Person). Ohne Identität (Legacy-Bypass) gibt es
// kein Wissen — wie bei /admin/*. Ist die Funktion aus, antworten alle drei
// Wege 404, als gäbe es sie nicht.
//
// Agenten-Token erreichen diese Wege nicht: agentDarf (agent.go) ist eine
// abschliessende Liste, /wissen steht nicht darauf (ADR-0137 §3). Das gilt
// auch für das delegierende Open-WebUI-Token; ein boardgebundenes Token läse
// sonst instanzweites Wissen. agent_wissen_test.go hält das fest.
//
// Die Einstellungen werden je Anfrage gelesen (wie kalender.zeitzone): Ein
// Speichern im Admin-Dialog wirkt sofort, ohne Neustart und ohne Reload.
// Der geöffnete Ordner (os.Root) bleibt zwischen den Anfragen offen und wird
// nur bei geändertem wissen.dir neu geöffnet.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/wissen"
)

// WissenAPI bündelt die Abhängigkeiten der /wissen/*-Wege.
type WissenAPI struct {
	cfg   *storage.ConfigRepo
	users *storage.UserRepo
	// praefix: wissen.dir muss darunter liegen. Im Betrieb immer
	// wissen.ErlaubterPraefix; nur Tests setzen ein eigenes Verzeichnis.
	praefix string
	// serverOrdner: Anhang-Ordner des Servers; dazu kommt zur Prüfzeit der
	// Sicherungsordner aus backup_board.* (F-1887). Den Datenbank-Ordner
	// nicht (Verify V-1): Dort liegt kein .md, und eine DB direkt unter
	// /data sperrte sonst jeden Wissensordner.
	serverOrdner []string
	// suchBremse: Suchen je Person (ADR-0137 §6). leseBremse: Index und
	// Seiten je Person, grosszügig — sie sind billig, aber nicht kostenlos
	// (eine Seite bis 1 MiB, Kritiker #739 K-7c).
	suchBremse *rateLimiter
	leseBremse *rateLimiter

	mu     sync.Mutex
	quelle *wissen.Quelle
	// letzterFehler: ein ungültiger Wert wird einmal geloggt, nicht bei jeder
	// Anfrage (Kritiker #739 K-7e).
	letzterFehler string
}

// wissenSuchenJeMinute: genug für Tippen mit Pause, zu wenig, um den
// Ordner im Dauerlauf zu lesen. wissenLesenJeMinute: weit über Blättern.
const (
	wissenSuchenJeMinute = 30
	wissenLesenJeMinute  = 120
)

// NewWissenAPI legt die API an.
// MitServerOrdnern nennt die Datenordner des Servers, die wissen.dir weder
// sein noch enthalten noch in denen es liegen darf (F-1887).
func (a *WissenAPI) MitServerOrdnern(ordner ...string) *WissenAPI {
	a.serverOrdner = append(a.serverOrdner, ordner...)
	return a
}

// pruefeOrdner: wissen.dir gegen die Server-Datenordner (F-1887). Der
// Sicherungsordner ist konfigurierbar und wird deshalb hier gelesen.
func (a *WissenAPI) pruefeOrdner(ctx context.Context, dir string) error {
	gesperrt := append([]string{}, a.serverOrdner...)
	if a.cfg != nil {
		gesperrt = append(gesperrt, boardbackup.LoadConfig(ctx, a.cfg).Dir)
	}
	return wissen.PruefeKeinServerOrdner(dir, gesperrt)
}

// ladeKonfig: wissen.LoadConfig plus Server-Ordner-Prüfung — ein Wert, der
// am Speichern vorbei in die Tabelle kam, schaltet das Wissen aus.
func (a *WissenAPI) ladeKonfig(ctx context.Context) (wissen.Config, error) {
	c, err := wissen.LoadConfig(ctx, a.cfg, a.praefix)
	if err == nil && c.Enabled {
		err = a.pruefeOrdner(ctx, c.Dir)
	}
	return c, err
}

func NewWissenAPI(cfg *storage.ConfigRepo, users *storage.UserRepo) *WissenAPI {
	return &WissenAPI{
		cfg:        cfg,
		users:      users,
		praefix:    wissen.ErlaubterPraefix,
		suchBremse: newRateLimiter(wissenSuchenJeMinute, time.Minute),
		leseBremse: newRateLimiter(wissenLesenJeMinute, time.Minute),
	}
}

// Register hängt die drei Lesewege an.
func (a *WissenAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /wissen/index", WithDefaults(http.HandlerFunc(a.handleIndex)))
	mux.Handle("GET /wissen/seite", WithDefaults(http.HandlerFunc(a.handleSeite)))
	mux.Handle("GET /wissen/suche", WithDefaults(http.HandlerFunc(a.handleSuche)))
}

// PruefeKonfig prüft einen wissen.*-Wert beim Speichern (handleConfigSet).
//
// Kritiker #739 K-1: Die Form allein reicht nicht. Ein Ordner wird nur
// gespeichert, wenn er existiert (ein Tippfehler hiesse sonst „Eintrag da,
// jeder Klick: nicht erreichbar“), und eingeschaltet wird nur, wenn der
// gespeicherte Ordner gültig ist — sonst meldete der Dialog „gespeichert“,
// und das Wissen bliebe still aus.
//
// Verify #739: Geprüft wird nur, was sich ändert. Den unveränderten Ordner
// ein zweites Mal auf Existenz zu prüfen, sperrte genau den Admin aus, der
// bei verschwundenem Mount ausschalten will. Leeren oder Löschen des Ordners
// (value "", auch über DELETE) geht nur, solange das Wissen aus ist — der
// Dialog sendet beim Ausschalten deshalb den Schalter zuerst.
func (a *WissenAPI) PruefeKonfig(key, value string) (string, error) {
	norm, err := wissen.PruefeWert(key, value, a.praefix)
	if err != nil {
		return "", err
	}
	if key == wissen.KeyDir && norm != "" {
		if err := a.pruefeOrdner(context.Background(), norm); err != nil {
			return "", err
		}
	}
	gespeichert := func(k string) (string, error) {
		v, gerr := a.cfg.Get(context.Background(), k)
		if errors.Is(gerr, storage.ErrConfigKeyNotFound) {
			return "", nil
		}
		if gerr != nil {
			return "", gerr
		}
		return v.Value, nil
	}
	switch {
	case key == wissen.KeyDir && norm == "":
		an, gerr := gespeichert(wissen.KeyEnabled)
		if gerr != nil {
			return "", gerr
		}
		if strings.TrimSpace(an) == "true" {
			return "", fmt.Errorf("%s: erst das Wissen ausschalten, dann den Ordner leeren", wissen.KeyDir)
		}
	case key == wissen.KeyDir:
		alt, gerr := gespeichert(wissen.KeyDir)
		if gerr != nil {
			return "", gerr
		}
		if alt != norm {
			if err := ordnerDa(norm); err != nil {
				return "", err
			}
		}
	case key == wissen.KeyEnabled && norm == "true":
		alt, gerr := gespeichert(wissen.KeyDir)
		if gerr != nil {
			return "", gerr
		}
		dir, derr := wissen.PruefeDir(alt, a.praefix)
		if derr == nil {
			derr = a.pruefeOrdner(context.Background(), dir)
		}
		if derr != nil {
			return "", fmt.Errorf("%s: erst einen gültigen Ordner speichern (%v)", wissen.KeyEnabled, derr)
		}
		// Verify #739 W-3: War es schon an, ist das kein Einschalten —
		// sonst scheiterte bei verschwundenem Mount jedes Speichern im
		// Dialog am Ende, obwohl nur der Name geändert wurde.
		schonAn, serr := gespeichert(wissen.KeyEnabled)
		if serr != nil {
			return "", serr
		}
		if strings.TrimSpace(schonAn) != "true" {
			if err := ordnerDa(dir); err != nil {
				return "", err
			}
		}
	}
	return norm, nil
}

// PruefeLoeschen prüft DELETE /admin/config/wissen.* (Verify #739 W-1):
// Nur der Ordner ist geschützt (wie das Leeren beim Speichern); jeder andere
// Schlüssel — auch ein Alteintrag, den PruefeWert nicht mehr kennt — lässt
// sich löschen.
func (a *WissenAPI) PruefeLoeschen(key string) error {
	if key != wissen.KeyDir {
		return nil
	}
	_, err := a.PruefeKonfig(key, "")
	return err
}

func ordnerDa(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("%s: Ordner %s nicht gefunden oder nicht lesbar", wissen.KeyDir, dir)
	}
	return nil
}

// SichtbarFuer nennt den Anzeigenamen, wenn die Funktion an ist und u sie
// sehen darf; sonst "". Für /me (die Oberfläche zeigt den Eintrag danach).
func (a *WissenAPI) SichtbarFuer(ctx context.Context, u *storage.User) string {
	if u == nil {
		return ""
	}
	c, err := a.ladeKonfig(ctx)
	if err != nil || !c.Enabled || !darfWissen(c, u, gruppenAus(ctx)) {
		return ""
	}
	return c.Name
}

// gruppenAus liest Remote-Groups der Anfrage (kommagetrennt); ohne Identität
// leer.
func gruppenAus(ctx context.Context) string {
	if id := IdentityFromContext(ctx); id != nil {
		return id.Groups
	}
	return ""
}

func darfWissen(c wissen.Config, u *storage.User, gruppen string) bool {
	if u.IsAdmin {
		return true
	}
	switch {
	case c.Sichtbar == wissen.SichtbarAdmins:
		return false
	case c.Gruppe() != "":
		for _, g := range strings.Split(gruppen, ",") {
			if strings.TrimSpace(g) == c.Gruppe() {
				return true
			}
		}
		return false
	}
	return true
}

// zugang prüft Funktion, Person und Ordner. Bei false ist die Antwort
// geschrieben.
func (a *WissenAPI) zugang(w http.ResponseWriter, r *http.Request) (wissen.Config, *storage.User, *wissen.Quelle, bool) {
	c, err := a.ladeKonfig(r.Context())
	if err == nil {
		// Verify #739 V-3: nach einer Genesung meldet derselbe Fehler sich
		// wieder.
		a.mu.Lock()
		a.letzterFehler = ""
		a.mu.Unlock()
	}
	if err != nil {
		// Ein Wert, der am Speichern vorbei in die Tabelle kam: aus, aber
		// sichtbar im Log, damit es niemand für „nicht eingeschaltet" hält.
		a.mu.Lock()
		neu := err.Error() != a.letzterFehler
		a.letzterFehler = err.Error()
		a.mu.Unlock()
		if neu {
			log.Printf("aboard-server: wissen.* ungültig, Wissen bleibt aus: %v", err)
		}
		writeError(w, http.StatusNotFound, "not found")
		return c, nil, nil, false
	}
	if !c.Enabled {
		writeError(w, http.StatusNotFound, "not found")
		return c, nil, nil, false
	}
	id := IdentityFromContext(r.Context())
	if id == nil {
		writeError(w, http.StatusForbidden, "forbidden: wissen requires authentication")
		return c, nil, nil, false
	}
	email := storage.NormalizeEmail(id.Email)
	if email == "" {
		writeError(w, http.StatusForbidden, "forbidden: no identity email")
		return c, nil, nil, false
	}
	u, err := a.users.GetByEmail(r.Context(), email)
	if errors.Is(err, storage.ErrUserNotFound) {
		writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
		return c, nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user lookup failed", err)
		return c, nil, nil, false
	}
	if !darfWissen(c, u, id.Groups) {
		writeError(w, http.StatusForbidden, "forbidden: wissen is not visible to you")
		return c, nil, nil, false
	}
	q, err := a.oeffne(c.Dir)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "wissen: ordner nicht lesbar", err)
		return c, nil, nil, false
	}
	return c, u, q, true
}

// oeffne hält genau einen geöffneten Ordner und tauscht ihn bei geändertem
// wissen.dir.
func (a *WissenAPI) oeffne(dir string) (*wissen.Quelle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.quelle != nil && a.quelle.Dir() == dir && a.quelle.Aktuell() {
		return a.quelle, nil
	}
	q, err := wissen.Oeffne(dir)
	if err != nil {
		return nil, err
	}
	if a.quelle != nil {
		// Laufende Anfragen auf dem alten Ordner sehen danach einen
		// Lesefehler — ein Admin hat gerade den Ordner gewechselt.
		_ = a.quelle.Schliesse()
	}
	a.quelle = q
	return q, nil
}

// Vergiss schliesst den geöffneten Ordner (Reload-Hook „wissen").
func (a *WissenAPI) Vergiss() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.quelle != nil {
		_ = a.quelle.Schliesse()
		a.quelle = nil
	}
}

// leseErlaubt: Bremse für Index und Seite (je Person).
func (a *WissenAPI) leseErlaubt(w http.ResponseWriter, u *storage.User) bool {
	if a.leseBremse.allow(u.ID) {
		return true
	}
	writeError(w, http.StatusTooManyRequests, "rate limit: zu viele Abrufe, bitte kurz warten")
	return false
}

func (a *WissenAPI) handleIndex(w http.ResponseWriter, r *http.Request) {
	c, u, q, ok := a.zugang(w, r)
	if !ok || !a.leseErlaubt(w, u) {
		return
	}
	idx, err := q.Index()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "wissen: ordner nicht lesbar", err)
		return
	}
	body := map[string]any{
		"name":   c.Name,
		"seiten": idx.Seiten,
	}
	if idx.Stand != nil {
		body["stand"] = idx.Stand
	}
	if idx.Gekappt {
		body["gekappt"] = true
	}
	if c.BearbeitenURL != "" {
		body["bearbeiten_url"] = true
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *WissenAPI) handleSeite(w http.ResponseWriter, r *http.Request) {
	c, u, q, ok := a.zugang(w, r)
	if !ok || !a.leseErlaubt(w, u) {
		return
	}
	// Query().Get dekodiert genau einmal; ein `%25` im Ordnernamen bleibt so
	// ein `%` und wird nicht ein zweites Mal gedeutet.
	pfad := r.URL.Query().Get("pfad")
	if pfad == "" {
		writeError(w, http.StatusBadRequest, "pfad required")
		return
	}
	inhalt, meta, err := q.Seite(pfad)
	switch {
	case errors.Is(err, wissen.ErrNichtGefunden), errors.Is(err, wissen.ErrVeraendert):
		writeError(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, wissen.ErrZuGross):
		writeError(w, http.StatusRequestEntityTooLarge, "wissen: seite zu gross")
		return
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "wissen: ordner nicht lesbar", err)
		return
	}
	body := map[string]any{
		"pfad":   meta.Pfad,
		"titel":  meta.Titel,
		"inhalt": string(inhalt),
	}
	if link := wissen.BearbeitenLink(c.BearbeitenURL, meta.Pfad); link != "" {
		body["bearbeiten"] = link
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *WissenAPI) handleSuche(w http.ResponseWriter, r *http.Request) {
	_, u, q, ok := a.zugang(w, r)
	if !ok {
		return
	}
	anfrage, err := wissen.NormiereAnfrage(r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "q: 2 bis 200 Zeichen")
		return
	}
	if !a.suchBremse.allow(u.ID) {
		writeError(w, http.StatusTooManyRequests, "rate limit: zu viele Suchen, bitte kurz warten")
		return
	}
	erg, err := q.Suche(r.Context(), anfrage)
	if r.Context().Err() != nil {
		return // Client weg — niemand liest die Antwort
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "wissen: ordner nicht lesbar", err)
		return
	}
	writeJSON(w, http.StatusOK, erg)
}

// WissenTest ist der Test-Hook der Admin-Sektion „wissen": öffnet den
// eingetragenen Ordner und nennt Seitenzahl und Stand. Läuft auch bei
// ausgeschalteter Funktion — so lässt sich der Ordner vor dem Einschalten
// prüfen.
func (a *WissenAPI) WissenTest(_ context.Context, cfg map[string]string) (any, error) {
	dir, err := wissen.PruefeDir(cfg[wissen.KeyDir], a.praefix)
	if err == nil {
		err = a.pruefeOrdner(context.Background(), dir)
	}
	if err != nil {
		return nil, err
	}
	q, err := wissen.Oeffne(dir)
	if err != nil {
		return nil, err
	}
	defer q.Schliesse()
	idx, err := q.Index()
	if err != nil {
		return nil, err
	}
	erg := map[string]any{"seiten": len(idx.Seiten)}
	if idx.Stand != nil {
		erg["stand"] = idx.Stand
	}
	if idx.Gekappt {
		erg["gekappt"] = true
	}
	return erg, nil
}
