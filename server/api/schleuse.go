package api

// schleuse.go — Routen der Ausgangsschleuse, Grundlage S1 (ADR-0134 §1, §2,
// §4; Karte #684).
//
// Bei ABOARD_SCHLEUSE=aus registriert main.go KEINE dieser Routen (404), und
// GET /me meldet nichts — die Oberfläche zeigt dann nichts davon. Der
// Admin-Status meldet den Schalter immer (WithSchleuse).
//
// Routen (nur bei „an“):
//
//	GET    /admin/schleuse/freigaben             Admin: Liste samt Nutzbarkeit
//	POST   /admin/schleuse/freigaben             Admin: anlegen
//	PUT    /admin/schleuse/freigaben/{id}        Admin: ändern
//	DELETE /admin/schleuse/freigaben/{id}        Admin: löschen (mit den Schlüsseln)
//	GET    /users/me/schleuse-schluessel         eigene Schlüssel (nur Anbieter + Datum)
//	PUT    /users/me/schleuse-schluessel/{id}    eigenen Schlüssel setzen
//	DELETE /users/me/schleuse-schluessel/{id}    eigenen Schlüssel löschen
//	GET    /boards/{id}/schleuse-sperre          Sperre lesen (viewer+, ACLGuard)
//	PUT    /boards/{id}/schleuse-sperre          Sperre stellen (nur owner)
//	…/cards/{cardId}/schleuse-auftraege[/{aid}]  Auszug und Durchsicht (S2, schleuse_auszug.go)
//
// Abgrenzung zum Board-Chat (ADR-0133 §2): Diese Datei kennt LLMKeyRepo und
// llm_api_keys nicht. schleuse_test.go prüft das am Quelltext.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

// SchleuseAPI bündelt die Abhängigkeiten der Schleusen-Routen. schluessel ist
// nil, wenn kein Master-Key konfiguriert ist — die Schlüssel-Routen antworten
// dann 503, die übrigen arbeiten.
type SchleuseAPI struct {
	cfg        schleuse.Konfig
	db         *sql.DB
	freigaben  *storage.SchleuseFreigabeRepo
	schluessel *storage.SchleuseSchluesselRepo
	users      *storage.UserRepo
	acl        *storage.AclRepo
	limiter    *rateLimiter
	// politik ist für Tests austauschbar; nil = ausgang.Aktiv().
	politik func() *ausgang.Politik
	logf    func(string, ...any)
	// S2 (Karte #685, schleuse_auszug.go): Aufträge (nil ohne Master-Key),
	// lokales Modell der zweiten Stufe (nil ohne Firmenmodus mit eigener
	// Basis-URL) und die Grenze der Auszüge je Person und Stunde.
	auftraege     *storage.SchleuseAuftragRepo
	modell        schleuse.ModellAufruf
	auszugLimiter *rateLimiter
	// auszugLaufend: Auszüge je Person, deren lokales Modell gerade rechnet
	// (Kritiker KS-5). Sie zählen beim Vorab-Test gegen die Obergrenze der
	// offenen Entwürfe — sonst rechnete das Modell für jede von 28
	// gleichzeitigen Anfragen, obwohl höchstens eine gespeichert würde.
	// Die harte Grenze hält storage.AnlegenGeprueft; dies spart nur Arbeit.
	auszugMu      sync.Mutex
	auszugLaufend map[string]int
	// S3 (Karte #686, schleuse_versand.go): Hintergrundaufträge. nil ohne
	// MitVersand — Freigeben antwortet dann 503.
	versand *versandZustand
	// S4 (Karte #687, schleuse_dateien.go): Anhangsspeicher, nur lesend. nil
	// ohne MitAnhaengen — ein Auszug mit Dateien antwortet dann 503.
	anhaenge attachmentOpener
}

// NewSchleuseAPI legt die API an. schluessel darf nil sein (kein Master-Key).
func NewSchleuseAPI(cfg schleuse.Konfig, db *sql.DB, schluessel *storage.SchleuseSchluesselRepo,
	users *storage.UserRepo, acl *storage.AclRepo) *SchleuseAPI {
	return &SchleuseAPI{
		cfg:        cfg,
		db:         db,
		freigaben:  storage.NewSchleuseFreigabeRepo(db),
		schluessel: schluessel,
		users:      users,
		acl:        acl,
		limiter:    newRateLimiter(10, time.Hour),
		politik:    ausgang.Aktiv,
		// Karte #685: eigener Zähler — Auszüge zählen nicht gegen die
		// Schlüssel-Änderungen.
		auszugLimiter: newRateLimiter(AuszuegeJeStundeHoechst, time.Hour),
		logf:          log.Printf,
	}
}

// MitLog ersetzt die Log-Funktion (Tests: Warnungen des Abgleichs mitlesen).
func (s *SchleuseAPI) MitLog(logf func(string, ...any)) *SchleuseAPI {
	s.logf = logf
	return s
}

// An meldet den Schalter.
func (s *SchleuseAPI) An() bool { return s != nil && s.cfg.An }

// Freigaben liefert das Repo der Freigabeliste (für S2/S3).
func (s *SchleuseAPI) Freigaben() *storage.SchleuseFreigabeRepo { return s.freigaben }

// RegistriereSchleuse ist die EINE Stelle, an der entschieden wird, ob es die
// Routen gibt (main.go und die Tests rufen sie): Der Admin-Status meldet den
// Schalter immer; Admin-, Personen- und Board-Routen nur bei „an“ — bei „aus“
// antworten sie 404 (ADR-0134 §1).
func RegistriereSchleuse(mux *http.ServeMux, admin *AdminAPI, s *SchleuseAPI) {
	admin.WithSchleuse(s)
	if !s.An() {
		return
	}
	admin.RegisterSchleuse(mux, s)
	s.Register(mux)
}

// Register hängt die Routen für Personen und Boards an. Die Admin-Routen
// hängt AdminAPI.RegisterSchleuse an (requireAdmin). Nur bei „an“ aufrufen —
// über RegistriereSchleuse.
func (s *SchleuseAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /users/me/schleuse-schluessel", s.person(s.schluesselListe, false))
	mux.Handle("PUT /users/me/schleuse-schluessel/{id}", s.person(s.schluesselSetzen, true))
	mux.Handle("DELETE /users/me/schleuse-schluessel/{id}", s.person(s.schluesselLoeschen, true))
	mux.Handle("GET /boards/{id}/schleuse-sperre", WithDefaults(http.HandlerFunc(s.sperreLesen)))
	mux.Handle("PUT /boards/{id}/schleuse-sperre", WithDefaults(http.HandlerFunc(s.sperreSetzen)))
	s.registerAuszug(mux) // Karte #685 (S2)
}

// RegisterSchleuse hängt die Admin-Routen der Freigabeliste an und merkt sich
// die Schleuse für den Status. Nur bei „an“ aufrufen; den Status ohne Routen
// trägt WithSchleuse ein.
func (a *AdminAPI) RegisterSchleuse(mux *http.ServeMux, s *SchleuseAPI) {
	a.schleuse = s
	mux.Handle("GET /admin/schleuse/freigaben", a.protect(s.adminListe))
	mux.Handle("POST /admin/schleuse/freigaben", a.protectMutate(s.adminAnlegen))
	mux.Handle("PUT /admin/schleuse/freigaben/{id}", a.protectMutate(s.adminAendern))
	mux.Handle("DELETE /admin/schleuse/freigaben/{id}", a.protectMutate(s.adminLoeschen))
}

// WithSchleuse trägt die Schleuse für den Admin-Status ein (auch bei „aus“).
func (a *AdminAPI) WithSchleuse(s *SchleuseAPI) *AdminAPI {
	a.schleuse = s
	return a
}

// ── Nutzbarkeit und Abgleich ───────────────────────────────────────────

// freigabeSicht ist ein Eintrag samt Beurteilung, wie ihn der Admin sieht.
type freigabeSicht struct {
	storage.SchleuseFreigabe
	Host        string   `json:"host"`
	Freigegeben bool     `json:"freigegeben"`
	Nutzbar     bool     `json:"nutzbar"`
	Gruende     []string `json:"gruende"`
}

func (s *SchleuseAPI) beurteile(f storage.SchleuseFreigabe) freigabeSicht {
	b := schleuse.Beurteile(f.Aktiv, f.VertragsformBestaetigt, f.Modelle, f.BasisURL, s.politik())
	return freigabeSicht{SchleuseFreigabe: f, Host: schleuse.Host(f.BasisURL),
		Freigegeben: b.Freigegeben, Nutzbar: b.Nutzbar, Gruende: b.Gruende}
}

func (s *SchleuseAPI) sichten(ctx context.Context) ([]freigabeSicht, error) {
	liste, err := s.freigaben.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]freigabeSicht, 0, len(liste))
	for _, f := range liste {
		out = append(out, s.beurteile(f))
	}
	return out, nil
}

// Abgleich vergleicht die Freigabeliste mit der Ausgangsliste (ADR-0134 §1,
// zweiter Riegel) und liefert je freigegebenem Eintrag, dessen Host bei
// ABOARD_AUSGAENGE=aus nicht in ABOARD_AUSGAENGE_ERLAUBT steht, eine
// Warnzeile. Der Eintrag gilt dann als nicht nutzbar (Grund
// „ausgang-gesperrt“), bis der Host freigegeben und der Server neu gestartet
// ist. Läuft beim Start und nach jeder Änderung der Freigabeliste.
func (s *SchleuseAPI) Abgleich(ctx context.Context) ([]string, error) {
	sichten, err := s.sichten(ctx)
	if err != nil {
		return nil, err
	}
	var w []string
	for _, f := range sichten {
		if !f.Freigegeben {
			continue
		}
		for _, g := range f.Gruende {
			if g == schleuse.GrundAusgangGesperrt {
				w = append(w, fmt.Sprintf("aboard-server: WARNUNG: Ausgangsschleuse: Anbieter %s (%s) ist freigegeben, aber bei ABOARD_AUSGAENGE=aus nicht erreichbar — %s in ABOARD_AUSGAENGE_ERLAUBT aufnehmen (und im Proxy); bis dahin nicht nutzbar",
					f.Anbieter, f.BasisURL, f.Host))
			}
		}
	}
	return w, nil
}

func (s *SchleuseAPI) protokolliereAbgleich(ctx context.Context) {
	w, err := s.Abgleich(ctx)
	if err != nil {
		s.logf("aboard-server: Ausgangsschleuse: Abgleich mit der Ausgangsliste gescheitert: %v", err)
		return
	}
	for _, z := range w {
		s.logf("%s", z)
	}
}

// statusFelder liefert die Felder für GET /admin/status.
func (s *SchleuseAPI) statusFelder(ctx context.Context) map[string]any {
	if !s.An() {
		return map[string]any{"schleuse": "aus"}
	}
	m := map[string]any{"schleuse": "an", "schleuse_aufbewahrung_tage": s.cfg.AufbewahrungTage, "schleuse_auftraege_pro_stunde": s.cfg.GrenzeAuftraege()}
	sichten, err := s.sichten(ctx)
	if err != nil {
		m["schleuse_fehler"] = "freigabeliste nicht lesbar"
		return m
	}
	freigegeben, nutzbar := 0, 0
	for _, f := range sichten {
		if f.Freigegeben {
			freigegeben++
		}
		if f.Nutzbar {
			nutzbar++
		}
	}
	m["schleuse_anbieter_freigegeben"] = freigegeben
	m["schleuse_anbieter_nutzbar"] = nutzbar
	m["schleuse_schluessel_aktiv"] = s.schluessel != nil
	return m
}

// ── Admin: Freigabeliste ───────────────────────────────────────────────

// freigabeEingabe ist der Body von POST und PUT. aktiv, vertragsform_bestaetigt
// und vier_augen sind Zeiger: Beim PUT ersetzt der Body den ganzen Eintrag, ein
// fehlendes Feld hiesse dort sonst still „aktiv“, „Bestätigung zurückgenommen“
// bzw. „Vier-Augen aus“ (KC-6, Verify VS-1) — der PUT verlangt alle drei.
type freigabeEingabe struct {
	Anbieter               string   `json:"anbieter"`
	Bezeichnung            string   `json:"bezeichnung"`
	BasisURL               string   `json:"basis_url"`
	Modelle                []string `json:"modelle"`
	VertragsformBestaetigt *bool    `json:"vertragsform_bestaetigt"`
	VierAugen              *bool    `json:"vier_augen"`
	Aktiv                  *bool    `json:"aktiv"`
}

const bezeichnungHoechstLen = 120

// pruefe macht aus der Eingabe einen Eintrag oder einen Fehlertext für 400.
// ersetzen (PUT): aktiv, vertragsform_bestaetigt und vier_augen sind Pflicht.
func (e freigabeEingabe) pruefe(ersetzen bool) (storage.SchleuseFreigabe, string) {
	var f storage.SchleuseFreigabe
	if ersetzen && (e.Aktiv == nil || e.VertragsformBestaetigt == nil || e.VierAugen == nil) {
		return f, "aktiv, vertragsform_bestaetigt und vier_augen: beim Ändern Pflicht (true|false)"
	}
	if !schleuse.GueltigeArt(e.Anbieter) {
		return f, "anbieter: erlaubt sind " + strings.Join(schleuse.AnbieterArten, ", ")
	}
	basis, err := schleuse.PruefeBasis(e.Anbieter, e.BasisURL)
	if err != nil {
		return f, err.Error()
	}
	modelle, err := schleuse.PruefeModelle(e.Modelle)
	if err != nil {
		return f, err.Error()
	}
	bez := strings.TrimSpace(e.Bezeichnung)
	if len(bez) > bezeichnungHoechstLen || strings.IndexFunc(bez, unicode.IsControl) >= 0 {
		return f, fmt.Sprintf("bezeichnung: höchstens %d Zeichen, keine Steuerzeichen", bezeichnungHoechstLen)
	}
	aktiv, vertrag, vier := true, false, false // Vorgaben beim Anlegen
	if e.Aktiv != nil {
		aktiv = *e.Aktiv
	}
	if e.VertragsformBestaetigt != nil {
		vertrag = *e.VertragsformBestaetigt
	}
	if e.VierAugen != nil {
		vier = *e.VierAugen
	}
	return storage.SchleuseFreigabe{Anbieter: e.Anbieter, Bezeichnung: bez, BasisURL: basis, Modelle: modelle,
		VertragsformBestaetigt: vertrag, VierAugen: vier, Aktiv: aktiv}, ""
}

func leseFreigabe(w http.ResponseWriter, r *http.Request, ersetzen bool) (storage.SchleuseFreigabe, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	var e freigabeEingabe
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return storage.SchleuseFreigabe{}, false
	}
	f, fehler := e.pruefe(ersetzen)
	if fehler != "" {
		writeError(w, http.StatusBadRequest, fehler)
		return f, false
	}
	return f, true
}

func (s *SchleuseAPI) adminListe(w http.ResponseWriter, r *http.Request) {
	sichten, err := s.sichten(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse freigaben list failed", err)
		return
	}
	ausgaenge := "an"
	if s.politik().Aus() {
		ausgaenge = "aus"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"freigaben":         sichten,
		"anbieter_arten":    schleuse.AnbieterArten,
		"ausgaenge":         ausgaenge,
		"aufbewahrung_tage": s.cfg.AufbewahrungTage,
	})
}

func (s *SchleuseAPI) freigabeFehler(w http.ResponseWriter, err error, was string) {
	switch {
	case errors.Is(err, storage.ErrSchleuseFreigabeNichtGefunden):
		writeError(w, http.StatusNotFound, "freigabe not found")
	case errors.Is(err, storage.ErrSchleuseFreigabeDoppelt):
		writeError(w, http.StatusConflict, "anbieter und basis_url gibt es schon")
	default:
		writeError(w, http.StatusInternalServerError, "schleuse freigabe "+was+" failed", err)
	}
}

func (s *SchleuseAPI) adminAnlegen(w http.ResponseWriter, r *http.Request) {
	f, ok := leseFreigabe(w, r, false)
	if !ok {
		return
	}
	neu, err := s.freigaben.Anlegen(r.Context(), f, adminUserIDFromContext(r.Context()))
	if err != nil {
		s.freigabeFehler(w, err, "create")
		return
	}
	s.protokolliereAbgleich(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{"freigabe": s.beurteile(neu)})
}

func (s *SchleuseAPI) adminAendern(w http.ResponseWriter, r *http.Request) {
	f, ok := leseFreigabe(w, r, true)
	if !ok {
		return
	}
	f.ID = r.PathValue("id")
	neu, err := s.freigaben.Aendern(r.Context(), f, adminUserIDFromContext(r.Context()))
	if err != nil {
		s.freigabeFehler(w, err, "update")
		return
	}
	s.protokolliereAbgleich(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"freigabe": s.beurteile(neu)})
}

func (s *SchleuseAPI) adminLoeschen(w http.ResponseWriter, r *http.Request) {
	if err := s.freigaben.Loeschen(r.Context(), r.PathValue("id"), adminUserIDFromContext(r.Context())); err != nil {
		s.freigabeFehler(w, err, "delete")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Person: eigene Schlüssel ───────────────────────────────────────────

// person prüft die Identität und löst den User strikt aus ihr auf (kein
// Pfadsegment mit User-ID, kein IDOR) — Muster wie protect in llm_keys.go.
func (s *SchleuseAPI) person(h func(http.ResponseWriter, *http.Request, string), mutierend bool) http.Handler {
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
		if s.schluessel == nil {
			writeError(w, http.StatusServiceUnavailable, "schleuse keys disabled: configure ABOARD_SECRET_KEY")
			return
		}
		if mutierend && !s.limiter.allow(u.ID) {
			writeError(w, http.StatusTooManyRequests, "rate limit: 10 mutations per hour per user")
			return
		}
		h(w, r, u.ID)
	}))
}

// anbieterSicht ist, was eine Person über einen freigegebenen Anbieter erfährt.
// anbieter und basis_url sind zugleich der Stand, den der Schlüsseldialog
// beim Speichern mitschickt (KC-2).
type anbieterSicht struct {
	ID          string   `json:"id"`
	Anbieter    string   `json:"anbieter"`
	Bezeichnung string   `json:"bezeichnung"`
	Host        string   `json:"host"`
	BasisURL    string   `json:"basis_url"`
	Modelle     []string `json:"modelle"`
	Nutzbar     bool     `json:"nutzbar"`
	Gruende     []string `json:"gruende"`
}

func (s *SchleuseAPI) schluesselListe(w http.ResponseWriter, r *http.Request, userID string) {
	sichten, err := s.sichten(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse freigaben list failed", err)
		return
	}
	anbieter := []anbieterSicht{}
	for _, f := range sichten {
		if !f.Freigegeben {
			continue
		}
		anbieter = append(anbieter, anbieterSicht{ID: f.ID, Anbieter: f.Anbieter, Bezeichnung: f.Bezeichnung,
			Host: f.Host, BasisURL: f.BasisURL, Modelle: f.Modelle, Nutzbar: f.Nutzbar, Gruende: f.Gruende})
	}
	liste, err := s.schluessel.List(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse keys list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"anbieter":   anbieter,
		"schluessel": liste, /* nur Anbieter und Datum — KEIN Klartext */
	})
}

const schleuseSchluesselHoechstLen = 4096

func (s *SchleuseAPI) schluesselSetzen(w http.ResponseWriter, r *http.Request, userID string) {
	freigabeID := r.PathValue("id")
	f, err := s.freigaben.Get(r.Context(), freigabeID)
	if errors.Is(err, storage.ErrSchleuseFreigabeNichtGefunden) {
		writeError(w, http.StatusNotFound, "freigabe not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse freigabe lookup failed", err)
		return
	}
	// Nur für aktive, freigegebene Anbieter (Vertragsform bestätigt).
	if !f.Aktiv || !f.VertragsformBestaetigt {
		writeError(w, http.StatusConflict, "anbieter ist nicht freigegeben")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	// anbieter und basis_url: der Stand der Freigabe, den der Dialog angezeigt
	// hat (KC-2). Set prüft ihn in seiner Transaktion; hat der Admin die
	// Freigabe inzwischen umgestellt, antwortet die Route 409 statt den
	// Schlüssel an ein Gegenüber zu binden, das die Person nicht gesehen hat.
	var body struct {
		Key                       string  `json:"key"`
		GeschaeftskontoBestaetigt *bool   `json:"geschaeftskonto_bestaetigt"`
		Anbieter                  *string `json:"anbieter"`
		BasisURL                  *string `json:"basis_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Anbieter == nil || body.BasisURL == nil {
		writeError(w, http.StatusBadRequest, "anbieter und basis_url: Stand der Freigabe Pflicht")
		return
	}
	// ADR-0134 §2: Die Person bestätigt „geschäftliches Konto“ — Pflichtfeld,
	// nur `true` zählt.
	if body.GeschaeftskontoBestaetigt == nil || !*body.GeschaeftskontoBestaetigt {
		writeError(w, http.StatusBadRequest, "geschaeftskonto_bestaetigt: true required")
		return
	}
	key := strings.TrimSpace(body.Key)
	if key == "" || len(key) > schleuseSchluesselHoechstLen || strings.IndexFunc(key, func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsControl(c)
	}) >= 0 {
		writeError(w, http.StatusBadRequest, "key: nicht leer, ohne Leerraum")
		return
	}
	stand := storage.SchleuseStand{Anbieter: *body.Anbieter, BasisURL: *body.BasisURL}
	if err := s.schluessel.Set(r.Context(), userID, freigabeID, key, stand); err != nil {
		switch {
		case errors.Is(err, storage.ErrSchleuseFreigabeNichtGefunden):
			writeError(w, http.StatusNotFound, "freigabe not found")
			return
		case errors.Is(err, storage.ErrSchleuseStandVeraltet):
			writeError(w, http.StatusConflict, "freigabe hat sich geändert — Dialog neu laden")
			return
		case errors.Is(err, storage.ErrSchleuseNichtFreigegeben):
			writeError(w, http.StatusConflict, "anbieter ist nicht freigegeben")
			return
		}
		writeError(w, http.StatusInternalServerError, "schleuse key set failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *SchleuseAPI) schluesselLoeschen(w http.ResponseWriter, r *http.Request, userID string) {
	// Löschen des EIGENEN Schlüssels geht auch für eine inzwischen
	// deaktivierte Freigabe (eigene Daten).
	if err := s.schluessel.Delete(r.Context(), userID, r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse key delete failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Board-Sperre ───────────────────────────────────────────────────────

// istOwner meldet, ob die anfragende Person Owner des Boards ist. Ohne
// Identität: nein (fail-closed — die Schleuse ist an Personen gebunden).
func (s *SchleuseAPI) istOwner(ctx context.Context, r *http.Request, boardID string) (bool, string, error) {
	id := IdentityFromContext(r.Context())
	if id == nil || s.acl == nil {
		return false, "", nil
	}
	email := storage.NormalizeEmail(id.Email)
	if email == "" {
		return false, "", nil
	}
	role, found, err := s.acl.GetRole(ctx, boardID, email)
	if err != nil {
		return false, "", err
	}
	if !found || role != storage.RoleOwner {
		return false, "", nil
	}
	userID := ""
	if u, err := s.users.GetByEmail(ctx, email); err == nil {
		userID = u.ID
	}
	return true, userID, nil
}

func (s *SchleuseAPI) sperreLesen(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	gesperrt, err := storage.SchleuseBoardSperre(r.Context(), s.db, boardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse sperre read failed", err)
		return
	}
	owner, _, err := s.istOwner(r.Context(), r, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gesperrt": gesperrt, "darf_aendern": owner})
}

// sperreSetzen stellt die Sperre. Nur Owner — ein Domain-Eintrag (ADR-0132)
// ist höchstens editor und darf deshalb nie sperren oder entsperren.
//
// ⚠️ Für S2/S3: Die Sperre wirkt nur, wenn sie an JEDEM Schritt neu gelesen
// wird, nicht einmal beim Öffnen des Dialogs. Beim Erstellen des Auszugs UND
// beim Freigeben/Senden ist SchleuseBoardSperre erneut zu lesen (beim Senden
// im Hintergrundauftrag, unmittelbar vor dem Verbindungsaufbau). Ein Owner,
// der sperrt, während ein Auszug auf Freigabe wartet, muss den Versand
// verhindern.
func (s *SchleuseAPI) sperreSetzen(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	owner, userID, err := s.istOwner(r.Context(), r, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return
	}
	if !owner {
		writeError(w, http.StatusForbidden, "forbidden: nur der Owner darf die Schleuse für das Board sperren")
		return
	}
	if !s.limiter.allow("sperre:" + userID) {
		writeError(w, http.StatusTooManyRequests, "rate limit: 10 mutations per hour per user")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Gesperrt *bool `json:"gesperrt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Gesperrt == nil {
		writeError(w, http.StatusBadRequest, "gesperrt: true|false required")
		return
	}
	if err := storage.SetzeSchleuseBoardSperre(r.Context(), s.db, boardID, *body.Gesperrt, userID); err != nil {
		if errors.Is(err, storage.ErrBoardNotFound) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "schleuse sperre set failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gesperrt": *body.Gesperrt})
}
