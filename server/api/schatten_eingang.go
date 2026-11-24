package api

// schatten_eingang.go — Board-Paar intern/extern, Seite außen: der
// Schatten-Eingang (ADR-0140 §3, Karte #779).
//
//	PUT /agent/schatten/{boardId}   — ganze Schattenmenge eines gekoppelten Boards
//
// Nur am Agenten-Eingang (Pfad /agent/ oder Agenten-Listener) und nur mit
// einem Token vom Typ `abe` (Präfix abe_). Das Token geht wie abs_ direkt in
// den Mux, ohne Agentenrolle und ohne ACL (agentHandler in main_agent.go);
// die BoardpaarSchranke lässt es nur auf PUT /schatten/<board> durch.
// Absender ist das Push-Skript des Partnerprojekts (Vertrag Push-Skript, Ziffer 9).
//
// Den Eingang gibt es nur, wenn boardpaar.seite „aussen“ ist (je Anfrage
// gelesen); sonst und für jedes nicht gekoppelte Board antwortet er 404.
//
// Der Handler wertet NIE eine Proxy-Identität aus (Remote-Email o. Ä.): Führt
// der Proxy /agent/* an der Anmeldung vorbei, schützen allein Token, Schranke
// und Drossel (ADR-0140 §3).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// SchattenEreignis ist der SSE-Typ nach einem Abgleich mit Änderung. Bewusst
// nicht board:updated: Der Blob hat sich nicht geändert, ein Neuladen oder
// ETag-Konflikt wäre falsch (ADR-0140 §3). Abonnent ist die Anzeige außen
// (#781).
const SchattenEreignis = "schatten:aktualisiert"

// schattenEingangFrist: Lesen des Bodys (bis 32 MB) und die Transaktion über
// bis zu 5000 Zeilen brauchen mehr als requestTimeout. Die Frist gilt für den
// Kontext UND für Lese- und Schreibfrist der Verbindung: Die Listener setzen
// ReadTimeout/WriteTimeout 30 s, ohne eigene Frist käme die Antwort eines
// längeren Laufs nach dem Commit nie an (Kritiker #779, K-3). Dauert schon der
// Upload länger, endet er mit 400 „body unreadable“. Variable nur, damit der
// Test sie verkürzen kann (TestSchattenEingang779_FristBegrenztUpload).
var schattenEingangFrist = 60 * time.Second

// SchattenEingangAPI nimmt die Schattenmenge an.
type SchattenEingangAPI struct {
	cfg      *storage.ConfigRepo
	paar     *storage.BoardpaarRepo
	schatten *storage.SchattenRepo
	broker   *EventBroker
	// limit: Fehlversuche je IP (eigenes Kontingent, nicht das des Ausgangs).
	limit *rateLimiter
	// adresse: Schlüssel-Adresse der Drossel (F-2030); MitAdressregel.
	adresse func(*http.Request) string
	// laeuft: je Board höchstens ein Lauf gleichzeitig. Ein zweiter PUT
	// bekommt 429, statt sich hinter der Schreibsperre zu stapeln — auch ein
	// gültiges Token darf nicht beliebig viele 32-MB-Läufe parallel starten
	// (Vorprüfung #779, P-14).
	laeuft sync.Map // boardID → struct{}
}

func NewSchattenEingangAPI(cfg *storage.ConfigRepo, paar *storage.BoardpaarRepo, repo *storage.SchattenRepo, broker *EventBroker) *SchattenEingangAPI {
	return &SchattenEingangAPI{cfg: cfg, paar: paar, schatten: repo, broker: broker, limit: newRateLimiter(120, time.Hour), adresse: tcpAdresse}
}

// MitAdressregel wie beim Ausgang (F-2030).
func (a *SchattenEingangAPI) MitAdressregel(fn func(*http.Request) string) *SchattenEingangAPI {
	if fn != nil {
		a.adresse = fn
	}
	return a
}

// Register: ohne WithDefaults — dessen Body-Grenze (10 MiB) läge unter den
// 32 MB des Eingangs; der Handler setzt seine eigene (Muster attachments.go).
func (a *SchattenEingangAPI) Register(mux *http.ServeMux) {
	mux.Handle("PUT "+SchattenPfad+"{boardId}", WithSecurityHeaders(http.HandlerFunc(a.eingang)))
}

// schattenEreignisDaten: Nutzlast des SSE-Ereignisses — ohne ETag, damit
// niemand sie als Stand des Blobs liest.
type schattenEreignisDaten struct {
	BoardID         string `json:"board_id"`
	Anzahl          int    `json:"anzahl"`
	LetzteAenderung string `json:"letzte_aenderung"`
}

// eingang — PUT /schatten/{boardId}.
func (a *SchattenEingangAPI) eingang(w http.ResponseWriter, r *http.Request) {
	ende := time.Now().Add(schattenEingangFrist)
	ctx, cancel := context.WithDeadline(r.Context(), ende)
	defer cancel()
	// Fristen der Verbindung: Lesen bis zum Ende der Frist, Schreiben mit
	// Reserve für die Antwort. Nicht unterstützt (Test-Recorder) → die
	// Fristen des Servers bleiben.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(ende)
	_ = rc.SetWriteDeadline(ende.Add(10 * time.Second))
	if a.paar == nil || a.schatten == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Die Seite VOR dem Token: Bei „aus“ oder „innen“ gibt es den Weg
	// nicht, auch nicht als 401.
	if BoardpaarSeite(ctx, a.cfg) != BoardpaarSeiteAussen {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	z := boardpaarZugang(w, r, a.paar, a.limit, a.adresse, storage.BoardpaarTypEingang, "schatten-eingang")
	if z == nil {
		return
	}
	boardID := r.PathValue("boardId")
	kopplung, err := a.paar.AussenGekoppelt(ctx, boardID)
	if err != nil {
		log.Printf("schatten-eingang: zugang=%q partnerliste nicht lesbar → 500: %v", z.Name, err)
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	// ADR-0141 §3 (W2-1): Eine Kopie (kopie = 1) bekommt Schatten nur über
	// den Eingang v2; hier ist sie „nicht gekoppelt“.
	if kopplung == nil || kopplung.Kopie {
		log.Printf("schatten-eingang: zugang=%q board=%q → 404 (nicht gekoppelt, unbekannt oder kopie)", z.Name, kurzeKennung(boardID))
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if _, belegt := a.laeuft.LoadOrStore(boardID, struct{}{}); belegt {
		log.Printf("schatten-eingang: zugang=%q board=%s → 429 (ein Lauf läuft schon)", z.Name, boardID)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "a shadow sync for this board is already running")
		return
	}
	defer a.laeuft.Delete(boardID)
	// F-2019: Jeder Eingang eines gekoppelten Boards wird mit Zeitpunkt und
	// Fehlgrund gemerkt (nicht der 429 oben — da lief schon einer).
	merke := func(grund string) {
		merkeStand(ctx, "schatten-eingang", boardID, func(mctx context.Context) error {
			return a.paar.AussenEingangMerken(mctx, boardID, grund)
		})
	}

	// Erst jetzt den Body lesen — Token und Kopplung stehen fest.
	body, err := readAllBegrenzt(w, r, schatten.EingangMaxBytes)
	if err != nil {
		var zuGross *http.MaxBytesError
		if errors.As(err, &zuGross) {
			log.Printf("schatten-eingang: zugang=%q board=%s → 413 (über %d Byte)", z.Name, boardID, schatten.EingangMaxBytes)
			merke(fmt.Sprintf("413: Liste über %d Byte", schatten.EingangMaxBytes))
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		merke("400: Liste nicht lesbar")
		writeError(w, http.StatusBadRequest, "body unreadable")
		return
	}
	menge, err := schatten.LiesEingang(body, BuildVersion)
	if err != nil {
		var f *schatten.EingangFehler
		if !errors.As(err, &f) {
			merke("500: Prüfung fehlgeschlagen")
			writeError(w, http.StatusInternalServerError, "check failed", err)
			return
		}
		log.Printf("schatten-eingang: zugang=%q board=%s → %d (%s), nichts geschrieben", z.Name, boardID, f.Status, f.Merkertext())
		merke(fmt.Sprintf("%d: %s", f.Status, f.Merkertext()))
		if len(f.Fehler) > 0 {
			// T16/F-2017: alle fehlerhaften Indizes mit Grund je Index; "error"
			// bleibt für ältere Skripte bestehen. Nie ein Wert, nur Stelle und Feld.
			antwort := map[string]any{"error": f.Antworttext(), "fehler": f.Fehler}
			if f.Weitere > 0 {
				antwort["weitere"] = f.Weitere
			}
			writeJSON(w, f.Status, antwort)
			return
		}
		writeError(w, f.Status, f.Antworttext())
		return
	}
	erg, err := a.schatten.Ersetze(ctx, boardID, menge)
	if err != nil {
		status, text := ersetzeFehler(err)
		log.Printf("schatten-eingang: zugang=%q board=%s → %d (%s): %v", z.Name, boardID, status, text, err)
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "5")
		}
		merke(fmt.Sprintf("%d: %s", status, text))
		writeError(w, status, text, err)
		return
	}
	if erg.HatAenderung() {
		log.Printf("schatten-eingang: zugang=%q board=%s neu=%d geaendert=%d entfernt=%d anzahl=%d → 200",
			z.Name, boardID, erg.Neu, erg.Geaendert, erg.Entfernt, erg.Anzahl)
		if a.broker != nil {
			if daten, err := json.Marshal(schattenEreignisDaten{BoardID: boardID, Anzahl: erg.Anzahl, LetzteAenderung: erg.LetzteAenderung}); err == nil {
				a.broker.Publish(boardID, Event{Type: SchattenEreignis, Data: string(daten)})
			}
		}
	}
	merke("")
	writeJSON(w, http.StatusOK, erg)
}

// readAllBegrenzt liest den Body bis max Byte; darüber ein *http.MaxBytesError.
func readAllBegrenzt(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, max))
}

// ersetzeFehler bildet einen Fehler von SchattenRepo.Ersetze auf Status und
// Text ab. Eigene Funktion, damit der Test die Zuordnung prüft und nicht den
// Weg dorthin — „während des Laufs entkoppelt“ und „Datenbank belegt“ sind
// über HTTP kaum herzustellen (Kritiker #779, K-7/K-8).
func ersetzeFehler(err error) (int, string) {
	switch {
	case errors.Is(err, storage.ErrBoardpaarNichtGekoppelt):
		// Zwischen Prüfung und Transaktion entkoppelt.
		return http.StatusNotFound, "not found"
	case errors.Is(err, storage.ErrDBBelegt), errors.Is(err, context.DeadlineExceeded):
		// Vorübergehend — vom echten Fehler unterscheidbar (K-8).
		return http.StatusServiceUnavailable, "server busy"
	}
	return http.StatusInternalServerError, "write failed"
}
