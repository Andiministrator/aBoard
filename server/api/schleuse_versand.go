package api

// schleuse_versand.go — Freigabe, Vier-Augen, Hintergrundauftrag,
// Antwortkommentar „extern“ und Protokoll der Ausgangsschleuse, Teil S3
// (ADR-0134 §3.4–§3.7, §4; Karte #686).
//
// Routen (nur bei ABOARD_SCHLEUSE=an, über registerAuszug):
//
//	POST /boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/freigabe       eigenen Entwurf freigeben (editor+)
//	POST /boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/zweitfreigabe  zweite Freigabe bei Vier-Augen (editor+, andere Person)
//	GET  /boards/{id}/cards/{cardId}/schleuse-belege/{aid}                   Beleg zum Antwortkommentar (viewer+)
//	GET  /boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/bilder/{bid}  Bild bereinigt, wie es hinausgeht (Karte #687)
//
// Der Versand läuft im Hintergrund (versandAnstossen): Er überlebt ein
// geschlossenes Browserfenster, nicht aber einen Neustart des Servers —
// ein Auftrag, der beim Start noch auf „gesendet“ steht, wird
// „fehlgeschlagen (unterbrochen)“; automatisch wiederholt wird nie
// (ADR-0134 §3.5). Freigegebene, noch nicht gesendete Aufträge nimmt
// VersandStart beim Start wieder auf — von ihnen ist nichts hinausgegangen.
//
// Was hinausgeht, ist GENAU der gespeicherte Text: BeginneVersand prüft in
// derselben Transaktion, in der es „gesendet“ setzt, dass Text, Hash und
// freigegebener Hash übereinstimmen, und liefert diesen Text; nichts
// dazwischen normalisiert, kürzt oder ergänzt ihn.
//
// Abgrenzung zum Board-Chat (ADR-0133 §2): Der Schlüssel kommt aus
// schleuse_schluessel, nie aus llm_api_keys.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

// versandParallelHoechst: so viele Versände laufen höchstens gleichzeitig.
const versandParallelHoechst = 4

// versandZustand: Hintergrundaufträge dieses Prozesses.
type versandZustand struct {
	boards    *storage.BoardRepo
	broker    *EventBroker
	ua        string
	transport http.RoundTripper // nur Tests (httptest auf 127.0.0.1)
	sem       chan struct{}
	mu        sync.Mutex
	laufend   map[string]bool
	wg        sync.WaitGroup
}

// MitVersand verdrahtet S3: Board-Repo (Antwortkommentar, Verlauf), Broker
// (Board-Update an offene Browser) und User-Agent.
func (s *SchleuseAPI) MitVersand(boards *storage.BoardRepo, broker *EventBroker, ua string) *SchleuseAPI {
	s.versand = &versandZustand{boards: boards, broker: broker, ua: ua,
		sem: make(chan struct{}, versandParallelHoechst), laufend: map[string]bool{}}
	return s
}

// MitVersandTransport ersetzt den Transport des Versands — NUR für Tests:
// Der echte lehnt jede interne Adresse ab, also auch httptest.
func (s *SchleuseAPI) MitVersandTransport(rt http.RoundTripper) *SchleuseAPI {
	if s.versand != nil {
		s.versand.transport = rt
	}
	return s
}

// AufbewahrungTage liefert die Frist der Zuordnungen (ADR-0134 §4).
func (s *SchleuseAPI) AufbewahrungTage() int { return s.cfg.AufbewahrungTage }

// WarteVersand wartet, bis alle laufenden Versände fertig sind (Tests,
// geordnetes Beenden).
func (s *SchleuseAPI) WarteVersand() {
	if s.versand != nil {
		s.versand.wg.Wait()
	}
}

// VersandStart läuft einmal beim Start (main.go): Aufträge, die noch auf
// „gesendet“ stehen, sind unterbrochen worden → „fehlgeschlagen“ samt
// Verlaufseintrag; freigegebene, sendebereite Aufträge werden angestossen.
func (s *SchleuseAPI) VersandStart(ctx context.Context) {
	if s.versand == nil || s.auftraege == nil {
		return
	}
	unterbrochen, err := s.auftraege.UnterbrocheneAbschliessen(ctx)
	if err != nil {
		s.logf("aboard-server: Ausgangsschleuse: unterbrochene Aufträge nicht abgeschlossen: %v", err)
	}
	for _, a := range unterbrochen {
		s.logf("aboard-server: Ausgangsschleuse: Auftrag %s war beim Beenden unterwegs — fehlgeschlagen (unterbrochen), keine Wiederholung", a.ID)
		s.verlaufFehlgeschlagen(ctx, a, storage.SchleuseGrundUnterbrochen)
	}
	ids, err := s.auftraege.Sendebereite(ctx)
	if err != nil {
		s.logf("aboard-server: Ausgangsschleuse: sendebereite Aufträge nicht gelesen: %v", err)
		return
	}
	for _, id := range ids {
		s.versandAnstossen(id)
	}
}

// registerVersand hängt die Routen von S3 an (aus registerAuszug).
func (s *SchleuseAPI) registerVersand(mux *http.ServeMux) {
	const basis = "/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}"
	mux.Handle("POST "+basis+"/freigabe", s.auftragPerson(s.auftragFreigeben))
	mux.Handle("POST "+basis+"/zweitfreigabe", s.auftragPerson(s.auftragZweitFreigeben))
	mux.Handle("GET /boards/{id}/cards/{cardId}/schleuse-belege/{aid}", s.auftragPerson(s.belegLesen))
	// Karte #687 (Verify VS4-2): Vorschau eines Bildes in der bereinigten Fassung.
	mux.Handle("GET "+basis+"/bilder/{bid}", s.auftragPerson(s.bildVorschau))
}

// leseHash liest {"sha256": "..."}.
func leseHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		SHA256 *string `json:"sha256"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.SHA256 == nil || len(*body.SHA256) != 64 {
		schleuseFehler(w, http.StatusBadRequest, "eingabe", "sha256 Pflicht (64 Hex-Zeichen)")
		return "", false
	}
	return strings.ToLower(*body.SHA256), true
}

// anbieterFuer prüft, dass die Freigabe des Auftrags noch nutzbar ist, das
// Modell erlaubt und der Schlüssel der Person hinterlegt — „“ = in Ordnung,
// sonst der Grund.
func (s *SchleuseAPI) anbieterFuer(ctx context.Context, a storage.SchleuseAuftrag) (storage.SchleuseFreigabe, string, error) {
	f, err := s.freigaben.Get(ctx, a.FreigabeID)
	if errors.Is(err, storage.ErrSchleuseFreigabeNichtGefunden) {
		return f, "anbieter-nicht-nutzbar", nil
	}
	if err != nil {
		return f, "", err
	}
	if !s.beurteile(f).Nutzbar {
		return f, "anbieter-nicht-nutzbar", nil
	}
	erlaubt := false
	for _, m := range f.Modelle {
		erlaubt = erlaubt || m == a.Modell
	}
	if !erlaubt {
		return f, "modell-nicht-erlaubt", nil
	}
	meine, err := s.schluessel.List(ctx, a.UserID)
	if err != nil {
		return f, "", err
	}
	for _, m := range meine {
		if m.FreigabeID == f.ID {
			return f, "", nil
		}
	}
	return f, "kein-schluessel", nil
}

func (s *SchleuseAPI) grenzeAuftraege() int { return s.cfg.GrenzeAuftraege() }

// auftragFreigeben: die antragstellende Person gibt ihren Entwurf frei
// (ADR-0134 §3.4). Body {"sha256": "<Hash des gezeigten Auszugs>"}. Ohne
// Vier-Augen beginnt der Versand sofort im Hintergrund.
func (s *SchleuseAPI) auftragFreigeben(w http.ResponseWriter, r *http.Request, u *storage.User) {
	ctx := r.Context()
	if !s.rolleOderFehler(w, r, r.PathValue("id"), u.Email, storage.RoleEditor) {
		return
	}
	if s.versand == nil {
		schleuseFehler(w, http.StatusServiceUnavailable, "kein-versand", "Versand nicht eingerichtet")
		return
	}
	a, ok := s.eigenerAuftrag(w, r, u)
	if !ok {
		return
	}
	sha, ok := leseHash(w, r)
	if !ok {
		return
	}
	f, grund, err := s.anbieterFuer(ctx, a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse freigabe lookup failed", err)
		return
	}
	if grund != "" {
		schleuseFehler(w, http.StatusConflict, grund, "Anbieter, Modell oder Schlüssel nicht (mehr) nutzbar")
		return
	}
	// Vorab gegen die Stundengrenze — verbindlich prüft BeginneVersand.
	if n, err := s.auftraege.GesendetLetzteStunde(ctx, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse count failed", err)
		return
	} else if n >= s.grenzeAuftraege() {
		schleuseFehler(w, http.StatusTooManyRequests, "zu-viele-auftraege", fmt.Sprintf("höchstens %d gesendete Aufträge je Stunde und Person", s.grenzeAuftraege()))
		return
	}
	neu, err := s.auftraege.Freigeben(ctx, a.ID, u.ID, u.Email, sha, f.VierAugen)
	if !s.versandFehler(w, err) {
		return
	}
	s.logf("aboard-server: Ausgangsschleuse: Auftrag %s freigegeben von %s (Hash %s, Vier-Augen %v)", neu.ID, u.Email, neu.FreigabeSHA256, neu.VierAugen)
	if !neu.VierAugen {
		s.versandAnstossen(neu.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"auftrag": neu})
}

// versandFehler schreibt die Antwort zu einem Fehler aus Freigeben oder
// ZweitFreigeben; true = kein Fehler.
func (s *SchleuseAPI) versandFehler(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, storage.ErrSchleuseAuftragNichtGefunden):
		schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "auftrag not found")
	case errors.Is(err, storage.ErrSchleuseAuftragNichtEntwurf):
		schleuseFehler(w, http.StatusConflict, "kein-entwurf", "Auftrag ist schon freigegeben oder gesendet")
	case errors.Is(err, storage.ErrSchleuseAuftragVeraltet):
		schleuseFehler(w, http.StatusConflict, "veraltet", "Auszug wurde inzwischen geändert — neu laden und erneut durchsehen")
	case errors.Is(err, storage.ErrSchleuseBoardGesperrt):
		schleuseFehler(w, http.StatusConflict, "board-gesperrt", "Board ist für die Ausgangsschleuse gesperrt")
	case errors.Is(err, storage.ErrBoardNotFound):
		writeError(w, http.StatusNotFound, "board not found")
	case errors.Is(err, storage.ErrSchleuseSelbstfreigabe):
		schleuseFehler(w, http.StatusConflict, "selbstfreigabe", "Die zweite Freigabe muss eine andere Person geben")
	case errors.Is(err, storage.ErrSchleuseNichtZweitfreigebbar):
		schleuseFehler(w, http.StatusConflict, "keine-zweitfreigabe", "Auftrag wartet nicht auf eine zweite Freigabe")
	default:
		writeError(w, http.StatusInternalServerError, "schleuse freigabe failed", err)
	}
	return false
}

// auftragZweitFreigeben: eine zweite Person (editor+, nicht die
// antragstellende) gibt einen Vier-Augen-Auftrag frei. Body {"sha256": …}.
func (s *SchleuseAPI) auftragZweitFreigeben(w http.ResponseWriter, r *http.Request, u *storage.User) {
	boardID, cardID := r.PathValue("id"), r.PathValue("cardId")
	if !s.rolleOderFehler(w, r, boardID, u.Email, storage.RoleEditor) {
		return
	}
	if s.versand == nil {
		schleuseFehler(w, http.StatusServiceUnavailable, "kein-versand", "Versand nicht eingerichtet")
		return
	}
	sha, ok := leseHash(w, r)
	if !ok {
		return
	}
	neu, err := s.auftraege.ZweitFreigeben(r.Context(), r.PathValue("aid"), boardID, cardID, u.Email, sha)
	if !s.versandFehler(w, err) {
		return
	}
	s.logf("aboard-server: Ausgangsschleuse: Auftrag %s zweite Freigabe von %s (Hash %s)", neu.ID, u.Email, neu.FreigabeSHA256)
	s.versandAnstossen(neu.ID)
	writeJSON(w, http.StatusOK, map[string]any{"auftrag": zweitSicht(neu)})
}

// zweitSicht: was eine andere Person von einem fremden Auftrag sieht — der
// pseudonymisierte Auszug (den sie freigeben soll), Markierungen,
// Antragsteller, Anbieter, Modell, Hash und Zustand. Nicht: die Antwort
// (gehört zum Beleg) und keine Kennung der Person außer der E-Mail.
func zweitSicht(a storage.SchleuseAuftrag) map[string]any {
	return map[string]any{
		"id": a.ID, "anbieter": a.Anbieter, "modell": a.Modell, "zustand": a.Zustand,
		"text": a.Text, "sha256": a.SHA256, "ersetzungen": a.Ersetzungen, "teile": a.Teile,
		"antragsteller": a.UserEmail, "freigegeben_von": a.FreigegebenVon, "freigegeben_am": a.FreigegebenAm,
		"zweit_freigegeben_von": a.ZweitFreigegebenVon, "vier_augen": a.VierAugen,
		// Karte #687: Die zweite Person gibt auch die Bilder frei — sie muss
		// sehen, welche mitgehen.
		"bilder": a.Bilder,
	}
}

// belegLesen: der Beleg zum Antwortkommentar (ADR-0134 §3.6) — für jedes
// Mitglied des Boards (viewer+), denn der Kommentar steht für alle sichtbar
// an der Karte. Er nennt den gesendeten Auszug (pseudonymisiert), wer
// gesendet und freigegeben hat, Anbieter, Modell, Zeit, Hash, Kosten, die
// veränderten Platzhalter und ob der Kommentar heute noch dem entspricht,
// was der Server geschrieben hat („weicht vom Original ab“).
func (s *SchleuseAPI) belegLesen(w http.ResponseWriter, r *http.Request, u *storage.User) {
	boardID, cardID := r.PathValue("id"), r.PathValue("cardId")
	if !s.rolleOderFehler(w, r, boardID, u.Email, storage.RoleViewer) {
		return
	}
	a, err := s.auftraege.GetAnKarte(r.Context(), r.PathValue("aid"), boardID, cardID)
	if errors.Is(err, storage.ErrSchleuseAuftragNichtGefunden) || (err == nil && a.GesendetAm == "") {
		schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "beleg not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "schleuse beleg read failed", err)
		return
	}
	abweichend, kommentarDa := false, false
	if a.KommentarID != "" {
		if st, err := s.ladeKarte(r.Context(), boardID, cardID); err == nil {
			if ks, ok := st.karte["comments"].([]any); ok {
				for _, k := range ks {
					km, _ := k.(map[string]any)
					// Kritiker KS-5 (Sprint-82-Abschluss, Runde 2): Es zählt nur
					// der Antwortkommentar selbst — Art „extern“ UND Verweis auf
					// genau diesen Auftrag. Ein gewöhnlicher Kommentar, der die id
					// übernimmt (POST /boards, Rückspielung), ist kein Beleg.
					if id, _ := km["id"].(string); id == a.KommentarID && istAntwortKommentarZu(km, a.ID) {
						kommentarDa = true
						text, _ := km["text"].(string)
						abweichend = schleuse.Hash(text) != a.KommentarSHA256
					}
				}
			}
		}
	}
	// Kritiker KS3-7: Ohne Antwortkommentar an dieser Karte ist der Beleg kein
	// Beleg zu etwas, das das Board zeigt — dann sieht ihn nur die Person,
	// die gesendet hat (der Auszug enthält ihre frei getippte Aufgabe).
	if !kommentarDa && a.UserID != u.ID {
		schleuseFehler(w, http.StatusNotFound, "auftrag-unbekannt", "beleg not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"beleg": map[string]any{
		"id": a.ID, "zustand": a.Zustand, "anbieter": a.Anbieter, "modell": a.Modell,
		"absender": a.UserEmail, "freigegeben_von": a.FreigegebenVon, "zweit_freigegeben_von": a.ZweitFreigegebenVon,
		"gesendet_am": a.GesendetAm, "abgeschlossen_am": a.AbgeschlossenAm,
		"auszug": a.Text, "sha256": a.SHA256, "antwort_sha256": a.AntwortSHA256,
		"veraenderte_platzhalter": a.VeraendertePlatzhalter, "fehler_grund": a.FehlerGrund,
		"input_tokens": a.InputTokens, "output_tokens": a.OutputTokens, "kosten_usd": a.KostenUSD,
		"kommentar_id": a.KommentarID, "kommentar_vorhanden": kommentarDa, "abweichend": abweichend,
		// Karte #687: was an Bildern hinausging (Hash der bereinigten Fassung).
		"bilder": belegBilder(a.Bilder),
	}})
}

// istAntwortKommentarZu: km ist ein Kommentar „extern“, dessen
// schleuse.auftragId auf den Auftrag aid zeigt.
func istAntwortKommentarZu(km map[string]any, aid string) bool {
	if art, _ := km["kind"].(string); art != ExternKommentarArt {
		return false
	}
	sl, _ := km["schleuse"].(map[string]any)
	ref, _ := sl["auftragId"].(string)
	return ref != "" && ref == aid
}

// belegBilder: die gesendeten Bilder eines Auftrags — id, Typ, Bytes, Hash.
func belegBilder(bilder []schleuse.BildEintrag) []schleuse.BildEintrag {
	frei := schleuse.FreigegebeneBilder(bilder)
	if frei == nil {
		return []schleuse.BildEintrag{}
	}
	return frei
}

// ── Hintergrundauftrag ─────────────────────────────────────────────────

// versandAnstossen startet den Versand eines Auftrags im Hintergrund — je
// Auftrag höchstens einmal gleichzeitig, insgesamt höchstens
// versandParallelHoechst.
func (s *SchleuseAPI) versandAnstossen(id string) {
	v := s.versand
	if v == nil {
		return
	}
	v.mu.Lock()
	if v.laufend[id] {
		v.mu.Unlock()
		return
	}
	v.laufend[id] = true
	v.mu.Unlock()
	v.wg.Add(1)
	go func() {
		defer v.wg.Done()
		defer func() {
			v.mu.Lock()
			delete(v.laufend, id)
			v.mu.Unlock()
		}()
		defer func() {
			if p := recover(); p != nil {
				// Wie F-1632: eine Panik beendete sonst den Server. Gemeldet wird
				// nur die Art — nie Text, der aus Auszug oder Antwort stammen kann.
				s.logf("aboard-server: Ausgangsschleuse: Panik im Versand von %s (%T)", id, p)
				_ = s.auftraege.Fehlschlagen(context.Background(), id, "intern")
			}
		}()
		v.sem <- struct{}{}
		defer func() { <-v.sem }()
		s.sende(context.Background(), id)
	}()
}

// sende führt einen Auftrag aus: Vorprüfungen (nichts ist hinausgegangen),
// BeginneVersand, Anfrage, Re-Identifizierung, Kommentar, Abschluss.
func (s *SchleuseAPI) sende(ctx context.Context, id string) {
	a, err := s.auftraege.GetIntern(ctx, id)
	if err != nil || a.Zustand != storage.SchleuseZustandFreigegeben {
		return
	}
	if a.VierAugen && a.ZweitFreigegebenVon == "" {
		return
	}
	scheitern := func(grund string) {
		if err := s.auftraege.Fehlschlagen(ctx, a.ID, grund); err == nil {
			s.logf("aboard-server: Ausgangsschleuse: Auftrag %s nicht gesendet (%s)", a.ID, grund)
			s.verlaufFehlgeschlagen(ctx, a, grund)
		}
	}
	// Vorprüfungen: Hier ist noch nichts hinausgegangen.
	if ok, err := s.rolleMindestens(ctx, a.BoardID, a.UserEmail, storage.RoleEditor); err != nil {
		s.logf("aboard-server: Ausgangsschleuse: Rolle für Auftrag %s nicht lesbar: %v", a.ID, err)
		return // im Zweifel nicht senden; der Auftrag bleibt freigegeben
	} else if !ok {
		scheitern("keine-rolle")
		return
	}
	// Kritiker KS-6 (Sprint-82-Abschluss, Runde 2): Auch die zweite Freigabe
	// muss beim Versand noch gelten — dieselbe Rolle, die ZweitFreigeben
	// verlangt (editor+). Wer nach der zweiten Freigabe zurückgestuft oder
	// entfernt wurde, trägt den Versand nicht mehr.
	if a.ZweitFreigegebenVon != "" {
		if ok, err := s.rolleMindestens(ctx, a.BoardID, a.ZweitFreigegebenVon, storage.RoleEditor); err != nil {
			s.logf("aboard-server: Ausgangsschleuse: Rolle der zweiten Freigabe für Auftrag %s nicht lesbar: %v", a.ID, err)
			return // im Zweifel nicht senden; der Auftrag bleibt freigegeben
		} else if !ok {
			scheitern("keine-rolle")
			return
		}
	}
	f, grund, err := s.anbieterFuer(ctx, a)
	if err != nil {
		s.logf("aboard-server: Ausgangsschleuse: Anbieter für Auftrag %s nicht lesbar: %v", a.ID, err)
		return
	}
	if grund != "" {
		scheitern(grund)
		return
	}
	schluessel, err := s.schluessel.Get(ctx, a.UserID, f.ID)
	if err != nil {
		scheitern("kein-schluessel")
		return
	}
	st, err := s.ladeKarte(ctx, a.BoardID, a.CardID)
	if errors.Is(err, errCardNotFound) {
		// F-1636: Die Karte ist weg. Ein freigegebener, nie gesendeter Auftrag
		// darf so enden — er geht nicht hinaus, die Antwort hätte keinen Ort.
		scheitern("karte-fehlt")
		return
	} else if err != nil {
		s.logf("aboard-server: Ausgangsschleuse: Karte für Auftrag %s nicht lesbar: %v", a.ID, err)
		return
	}
	// Karte #687 (ADR-0134 §5): freigegebene Bilder frisch lesen, bereinigen
	// und gegen ihren Hash prüfen — noch ist nichts hinausgegangen.
	if len(schleuse.FreigegebeneBilder(a.Bilder)) > 0 && !llm.SchleuseBilderMoeglich(f.Anbieter) {
		scheitern("keine-bilder")
		return
	}
	bilder, grund := s.bilderFuerVersand(st.karte, a.BoardID, a.CardID, a.Bilder)
	if grund != "" {
		scheitern(grund)
		return
	}

	// Ab hier verbindlich: Zustand, Hash, Sperre und Grenze in EINER
	// Transaktion. Was danach gesendet wird, ist a.Text aus genau diesem Lesen.
	a, err = s.auftraege.BeginneVersand(ctx, a.ID, s.grenzeAuftraege())
	switch {
	case err == nil:
	case errors.Is(err, storage.ErrSchleuseBoardGesperrt), errors.Is(err, storage.ErrSchleuseGrenzeErreicht):
		s.logf("aboard-server: Ausgangsschleuse: Auftrag %s nicht gesendet, Freigabe aufgehoben (%s)", a.ID, a.FehlerGrund)
		return
	case errors.Is(err, storage.ErrSchleuseHashAbweichung):
		s.logf("aboard-server: Ausgangsschleuse: Auftrag %s nicht gesendet — Text und freigegebener Hash weichen ab", a.ID)
		s.verlaufFehlgeschlagen(ctx, a, storage.SchleuseGrundHashAbweichung)
		return
	default:
		return // inzwischen geändert, gelöscht oder schon unterwegs
	}
	if !bilderPassen(bilder, a.Bilder) {
		if e := s.auftraege.Fehlschlagen(ctx, a.ID, "datei-veraendert"); e == nil {
			s.logf("aboard-server: Ausgangsschleuse: Auftrag %s nicht gesendet — Bilder passen nicht zur Freigabe", a.ID)
			s.verlaufFehlgeschlagen(ctx, a, "datei-veraendert")
		}
		return
	}
	var bildBytes int
	for _, b := range bilder {
		bildBytes += len(b.Data)
		// Audit wie llm-anhang (ADR-0121 §6): id, Typ, Bytes — nie Name oder Inhalt.
		s.logf("aboard-server: Ausgangsschleuse: Auftrag %s Bild %s typ=%s bytes=%d", a.ID, b.ID, b.MIME, len(b.Data))
	}
	s.logf("aboard-server: Ausgangsschleuse: Auftrag %s gesendet — Person %s, Anbieter %s (%s), Modell %s, Hash %s, freigegeben von %s%s, Bilder %d (%d B)",
		a.ID, a.UserEmail, a.Anbieter, schleuse.Host(f.BasisURL), a.Modell, a.SHA256, a.FreigegebenVon, zweitText(a), len(bilder), bildBytes)
	s.verlaufEintrag(ctx, a, "schleuse-gesendet", "", map[string]any{
		"auftragId": a.ID, "sha256": a.SHA256, "freigegebenVon": a.FreigegebenVon, "zweitFreigegebenVon": a.ZweitFreigegebenVon,
		"bilder": len(bilder),
	})

	vctx, cancel := context.WithTimeout(ctx, llm.SchleuseVersandFrist)
	antw, err := llm.SchleuseVersenden(vctx, llm.SchleuseVersand{
		Anbieter: f.Anbieter, BasisURL: f.BasisURL, Modell: a.Modell, Schluessel: schluessel,
		Text: a.Text, Bilder: bilder, UA: s.versand.ua, Transport: s.versand.transport,
	})
	cancel()
	if err != nil {
		grund := llm.SchleuseFehlerGrund(err)
		s.logf("aboard-server: Ausgangsschleuse: Auftrag %s fehlgeschlagen: %s", a.ID, llm.SchleuseFehlerText(err))
		if e := s.auftraege.Fehlschlagen(ctx, a.ID, grund); e == nil {
			s.verlaufFehlgeschlagen(ctx, a, grund)
		}
		return
	}

	zuordnung, err := s.auftraege.ZuordnungIntern(ctx, a.ID)
	if err != nil {
		zuordnung = map[string]string{} // dann bleiben alle Platzhalter stehen und werden gemeldet
	}
	re := schleuse.ReIdentifiziere(antw.Text, zuordnung)
	erg := storage.SchleuseErgebnis{
		AntwortText: antw.Text, VeraendertePlatzhalter: re.Veraendert,
		InputTokens: antw.InputTokens, OutputTokens: antw.OutputTokens, KostenUSD: antw.KostenUSD, FinishReason: antw.FinishReason,
	}
	if antw.Gekuerzt {
		erg.FehlerGrund = "gekuerzt"
	}
	kommentarID := uuid.NewString()
	// Verify VS3-2: Wurde die Karte während des Versands in ein anderes Board
	// übertragen, hat der Transfer den Auftrag mitgenommen — geschrieben wird
	// dorthin, wo er JETZT steht.
	if akt, err := s.auftraege.GetIntern(ctx, a.ID); err == nil {
		a.BoardID, a.CardID = akt.BoardID, akt.CardID
	}
	geschrieben, err := s.schreibeAntwort(ctx, a, kommentarID, re.Text, erg)
	switch {
	case err != nil:
		s.logf("aboard-server: Ausgangsschleuse: Antwortkommentar zu %s nicht geschrieben: %v", a.ID, err)
		erg.FehlerGrund = "kommentar-fehlt"
	case !geschrieben:
		erg.FehlerGrund = "karte-fehlt"
	default:
		erg.KommentarID, erg.KommentarSHA256 = kommentarID, schleuse.Hash(re.Text)
	}
	if err := s.auftraege.Abschliessen(ctx, a.ID, erg); err != nil {
		s.logf("aboard-server: Ausgangsschleuse: Auftrag %s nicht abgeschlossen: %v", a.ID, err)
		return
	}
	s.logf("aboard-server: Ausgangsschleuse: Auftrag %s beantwortet — Token %d/%d, Kosten %.4f USD, %d Platzhalter zurückgeführt, %d verändert, %d im Linkziel stehen gelassen",
		a.ID, antw.InputTokens, antw.OutputTokens, antw.KostenUSD, re.Ersetzt, len(re.Veraendert), re.ImLink)
}

func zweitText(a storage.SchleuseAuftrag) string {
	if a.ZweitFreigegebenVon == "" {
		return ""
	}
	return " und " + a.ZweitFreigegebenVon
}

// externAutor ist das Autor-Label des Antwortkommentars.
func externAutor(a storage.SchleuseAuftrag) string {
	return fmt.Sprintf("🌐 %s/%s (extern, im Auftrag von %s)", a.Anbieter, a.Modell, a.UserEmail)
}

// schreibeAntwort hängt den Antwortkommentar „extern“ und den Verlaufseintrag
// „schleuse-beantwortet“ an die Karte — in einem Schreibvorgang. false = die
// Karte gibt es nicht mehr.
func (s *SchleuseAPI) schreibeAntwort(ctx context.Context, a storage.SchleuseAuftrag, kommentarID, text string, erg storage.SchleuseErgebnis) (bool, error) {
	jetzt := time.Now().UTC().Format(time.RFC3339)
	return s.karteAendern(ctx, a.BoardID, a.CardID, func(karte map[string]any) {
		kommentare, _ := karte["comments"].([]any)
		karte["comments"] = append(kommentare, map[string]any{
			"id": kommentarID, "author": externAutor(a), AutorEmailFeld: a.UserEmail,
			"text": text, "created": jetzt, "kind": ExternKommentarArt,
			"schleuse": map[string]any{
				"auftragId": a.ID, "anbieter": a.Anbieter, "modell": a.Modell, "gesendetAm": a.GesendetAm,
				"freigegebenVon": a.FreigegebenVon, "zweitFreigegebenVon": a.ZweitFreigegebenVon,
			},
		})
		veraendert := make([]any, 0, len(erg.VeraendertePlatzhalter))
		for _, p := range erg.VeraendertePlatzhalter {
			veraendert = append(veraendert, p)
		}
		verlauf, _ := karte["activity"].([]any)
		karte["activity"] = append(verlauf, map[string]any{
			"id": uuid.NewString(), "ts": jetzt, "actor": a.UserEmail, "action": "schleuse-beantwortet",
			"from": "", "to": a.Anbieter + "/" + a.Modell,
			"meta": map[string]any{
				"auftragId": a.ID, "commentId": kommentarID, "sha256": a.SHA256,
				"inputTokens": erg.InputTokens, "outputTokens": erg.OutputTokens, "costEstimateUSD": erg.KostenUSD,
				"veraenderte": veraendert,
			},
		})
	})
}

// verlaufEintrag hängt einen Protokolleintrag der Schleuse an die Karte
// (best effort — der Auftragsdatensatz und das Server-Log sind die Belege).
func (s *SchleuseAPI) verlaufEintrag(ctx context.Context, a storage.SchleuseAuftrag, aktion, note string, meta map[string]any) {
	jetzt := time.Now().UTC().Format(time.RFC3339)
	eintrag := map[string]any{"id": uuid.NewString(), "ts": jetzt, "actor": a.UserEmail, "action": aktion,
		"from": "", "to": a.Anbieter + "/" + a.Modell, "meta": meta}
	if note != "" {
		eintrag["note"] = note
	}
	if _, err := s.karteAendern(ctx, a.BoardID, a.CardID, func(karte map[string]any) {
		verlauf, _ := karte["activity"].([]any)
		karte["activity"] = append(verlauf, eintrag)
	}); err != nil {
		s.logf("aboard-server: Ausgangsschleuse: Verlaufseintrag %s zu %s nicht geschrieben: %v", aktion, a.ID, err)
	}
}

func (s *SchleuseAPI) verlaufFehlgeschlagen(ctx context.Context, a storage.SchleuseAuftrag, grund string) {
	s.verlaufEintrag(ctx, a, "schleuse-fehlgeschlagen", grund, map[string]any{"auftragId": a.ID, "sha256": a.SHA256, "grund": grund})
}

// karteAendern: Read-Modify-Write einer Karte mit Optimistic Concurrency
// (Muster llm.applyStateMutation), danach Board-Update an offene Browser.
// false = Board oder Karte gibt es nicht (mehr).
func (s *SchleuseAPI) karteAendern(ctx context.Context, boardID, cardID string, aendere func(map[string]any)) (bool, error) {
	if s.versand == nil || s.versand.boards == nil {
		return false, errors.New("schleuse: kein board-repo")
	}
	boards := s.versand.boards
	for versuch := 0; versuch < llm.MaxMutationRetries; versuch++ {
		b, err := boards.Get(ctx, boardID)
		if errors.Is(err, storage.ErrBoardNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var root map[string]any
		if err := json.Unmarshal(b.StateJSON, &root); err != nil {
			return false, fmt.Errorf("parse state_json: %w", err)
		}
		board, _ := root["board"].(map[string]any)
		sprints, _ := board["sprints"].([]any)
		gefunden := false
		for _, sp := range sprints {
			spm, _ := sp.(map[string]any)
			karten, _ := spm["cards"].([]any)
			for _, k := range karten {
				km, _ := k.(map[string]any)
				if id, _ := km["id"].(string); id == cardID && !gefunden {
					aendere(km)
					gefunden = true
				}
			}
		}
		if !gefunden {
			return false, nil
		}
		neu, err := json.Marshal(root)
		if err != nil {
			return false, err
		}
		// Kritiker KS3-9: dieselbe Grenze wie der Ops-Weg — sonst schriebe der
		// Server ein Board, das danach kein Browser mehr speichern kann.
		if len(neu) > MaxBodyBytes {
			return false, fmt.Errorf("board %s überschritte %d Bytes", boardID, MaxBodyBytes)
		}
		nb, err := boards.Update(ctx, b.ID, b.Title, b.Version, neu, b.UpdatedAt)
		if errors.Is(err, storage.ErrConflict) {
			continue
		}
		if err != nil {
			return false, err
		}
		publishBoardUpdated(s.versand.broker, b.ID, nb.UpdatedAt, "schleuse")
		return true, nil
	}
	return false, fmt.Errorf("board %s nach %d Versuchen weiter im Konflikt", boardID, llm.MaxMutationRetries)
}
