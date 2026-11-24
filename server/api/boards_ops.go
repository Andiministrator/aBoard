package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Andiministrator/aBoard/server/schatten"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// Grenzen des Batch-Endpunkts (ADR-0129 §4, K-8).
const (
	// MaxOpsJeBatch begrenzt die Zahl der Ops in einem Aufruf.
	MaxOpsJeBatch = 200
	// MaxOpsBodyBytes begrenzt den Request-Body. Deutlich kleiner als
	// MaxBodyBytes (10 MiB, Ganz-Board-PUT): Ein Batch trägt Änderungen,
	// kein Board.
	MaxOpsBodyBytes = 1 << 20
	// opsRateMax Batches je Agent und opsRateFenster. Grosszügig für
	// einen Agenten, der Karte für Karte arbeitet. Es begrenzt die ZAHL der
	// Aufrufe; wie lange sie die datenbankweite Schreibsperre halten,
	// begrenzt das Sperrzeit-Budget (sperrbudget.go, F-1149).
	opsRateMax     = 60
	opsRateFenster = time.Minute
	// opsRateMaxMensch gilt für Menschen (und den Legacy-Bypass, in dem sich
	// alle einen Eimer teilen). Der Browser speichert ab Sprint 72 über
	// diesen Weg, entprellt mit 500 ms — ein Tab erzeugt also bis zu 120
	// Batches pro Minute, und mehrere Tabs einer Person teilen den Eimer.
	// 60 wie bei Agenten hätte eine aktive Nutzerin in 429 laufen lassen
	// (Kritiker Sprint 71). Die Sperrzeit begrenzt opsSperrBudgetMensch.
	opsRateMaxMensch = 600
)

// WithFold hängt den Fold-Motor an (Karte #552). Ohne ihn antwortet
// POST /boards/{id}/ops mit 503 — Test-Setups und Aufrufer, die den Endpunkt
// nicht brauchen, bleiben unverändert. Idempotent.
func (a *BoardsAPI) WithFold(m *fold.Motor) *BoardsAPI {
	a.fold = m
	if a.opsLimiter == nil {
		a.opsLimiter = newRateLimiter(opsRateMaxMensch, opsRateFenster)
	}
	if a.opsLimiterAgent == nil {
		a.opsLimiterAgent = newRateLimiter(opsRateMax, opsRateFenster)
	}
	if a.opsSperre == nil {
		a.opsSperre = newSperrBudget(opsSperrBudgetMensch, opsRateFenster)
	}
	if a.opsSperreAgent == nil {
		a.opsSperreAgent = newSperrBudget(opsSperrBudgetAgent, opsRateFenster)
	}
	return a
}

// opsAnfrage ist der Body von POST /boards/{id}/ops. Jede Op hat die Form
// der Inbox (ADR-0067/0074), damit ein Agent lokal und am Server dasselbe
// schreibt. Gelesen werden nur `type` und `payload`: id, ts, seq, actor und
// schemaVersion stempelt der Server und überschreibt mitgeschickte Werte
// (ADR-0129 §4, K-5 — die Ordnung ist die Eingangsordnung am Server, nicht
// die Uhr des Clients).
type opsAnfrage struct {
	Ops []map[string]json.RawMessage `json:"ops"`
}

// serverOp ist eine gestempelte Op in der Form von makeOp.
type serverOp struct {
	ID            string          `json:"id"`
	TS            string          `json:"ts"`
	Seq           int             `json:"seq"`
	Actor         string          `json:"actor"`
	SchemaVersion int64           `json:"schemaVersion"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	// ClientID: die `id`, die der Client der Op gab — Idempotenz-Schlüssel
	// (ADR-0130 §5, Karte #607). Geht nicht an den Fold; dort zählt die
	// Server-id, auf die sich das 422-Protokoll bezieht.
	ClientID string `json:"-"`
	// hashNutzlast: Grundlage von opHash statt Payload (Karte #670). Ein
	// Vorschlag wird vom Server neu gebaut, mit Zeitstempel und zufälliger
	// Verlaufs-id — gehasht wird deshalb, was der Client geschickt hat.
	hashNutzlast json.RawMessage
}

// opFehler beschreibt, warum eine einzelne Op abgelehnt wurde.
type opFehler struct {
	Index     int                 `json:"index"`
	Type      string              `json:"type,omitempty"`
	Grund     string              `json:"grund"`
	Fehlend   []string            `json:"fehlend,omitempty"`
	Unbekannt []unbekannterSchlue `json:"unbekannt,omitempty"`
}

type unbekannterSchlue struct {
	Key   string  `json:"key"`
	Meant *string `json:"meant"`
}

// opsTSFormat ist die feste Form von makeOp (`new Date().toISOString()`):
// orderedOps in ops.js vergleicht diese Form.
const opsTSFormat = "2006-01-02T15:04:05.000Z"

// opsNow liefert die Eingangszeit — als Variable, damit Tests sie festsetzen.
var opsNow = func() time.Time { return time.Now().UTC() }

// opsProtokollNaht ist eine Test-Naht (F-1556): Im Produktivbetrieb nil,
// wirkungslos. Ersetzt im Test das Protokoll des Folds, um den Zweig
// „Vorschlags-Token, Op abgelehnt“ zu erzwingen — der Server-Payload eines
// Vorschlags löst ihn konstruktionsbedingt nicht aus.
var opsProtokollNaht func(protokoll string) string

// anlageBremseNaht ist eine Test-Naht (Karte #798, Kritik Befund 1): Im
// Produktivbetrieb nil. Liefert sie einen Fehler, scheitert der Schreibvorgang
// NACH der Reservierung der Anlage-Bremse — der Test prüft, dass die
// Reservierung zurückkommt.
var anlageBremseNaht func() error

// opsNachSchalterNaht ist eine Test-Naht (F-2300): Im Produktivbetrieb nil.
// Läuft zwischen dem Lesen des Rückweg-Schalters einer Kopie und der Sperre,
// damit ein Test genau dort umschalten kann.
var opsNachSchalterNaht func()

// opsAbgelehnt trägt das Protokoll eines Batches, der nicht wie beabsichtigt
// wirkt, aus der Transaktion heraus.
type opsAbgelehnt struct{ protokoll json.RawMessage }

func (e *opsAbgelehnt) Error() string { return "ops wirken nicht wie beabsichtigt" }
func (e *opsAbgelehnt) Unwrap() error { return storage.ErrOpsAbgelehnt }

// postOps — POST /boards/{id}/ops (ADR-0129 §4, Karte #552).
//
// Ablauf: annehmen und stempeln → vorprüfen (ohne Sperre) → unter
// BEGIN IMMEDIATE falten und schreiben → SSE.
//
// Der Batch ist atomar: Entweder wirken alle Ops, oder es wird nichts
// geschrieben. „Wirken“ heisst auch: fold() meldet nichts im Protokoll.
// Eine Op, die nach den Regeln verworfen wird (wirkungslos), eine Karte, die
// in eine andere als die genannte Spalte geklemmt wird, oder ein card.upsert,
// das Felder einer bestehenden Karte verlöre (Entkernung, F-926), oder eine
// Op auf eine Karte oder Swimlane, die es im Stand vor ihr nicht gibt
// (ziel-fehlt, F-1146; Löschungen ausgenommen) — jede davon führt zu 422 mit
// dem Protokoll. Der lokale Projektor schreibt solche Ops
// trotzdem und meldet sie nur; am Server, wo Agenten ohne Aufsicht schreiben,
// ist die Ablehnung der sichere Weg.
//
// If-Match wird NICHT verlangt: Ops sind Änderungen, kein Ganz-Board-Stand,
// und genau dafür gibt es diesen Weg (keine 409 bei nebenläufigen Schreibern).
func (a *BoardsAPI) postOps(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	if a.fold == nil {
		writeError(w, http.StatusServiceUnavailable, "ops endpoint not configured")
		return
	}
	actor := actorFromContext(r.Context())
	schluessel := actor
	if schluessel == "" {
		// Legacy-Bypass ohne Identität: alle teilen einen Eimer.
		schluessel = "anonym"
	}
	limiter, sperre := a.opsLimiter, a.opsSperre
	if boardpaarAkteurAus(r.Context()) != "" {
		// Kritik E3b K-6: Die Person wählt das Skript frei — ein Eimer je
		// Person ließe sich mit wechselnden Namen umgehen. Der Rückweg teilt
		// sich je Board einen Eimer der Menschen (600/min, ein Lauf braucht
		// ≤ 400; Verify E3b V-4: je Board, damit ein Paar die übrigen nicht
		// ausbremst).
		schluessel = "boardpaar\x00rueckweg\x00" + id
	}
	if bestaetiger := vorschlagBestaetigerAus(r.Context()); bestaetiger != "" {
		// Karte #783 (ADR-0140 §8): Ein bestätigter Vorschlag läuft unter dem
		// Agenten, ausgelöst hat ihn aber ein Mensch — er zählt in dessen Eimer,
		// nicht im Ratenbegrenzer der Agenten.
		schluessel = bestaetiger
	} else if agent := AgentFromContext(r.Context()); agent != nil {
		limiter, sperre = a.opsLimiterAgent, a.opsSperreAgent
		if agent.Delegiert {
			// ADR-0133 §4: je Person ein Eimer, nicht einer für alle hinter
			// Open WebUI — sonst bremste ein Chat die ganze Firma aus.
			schluessel += "\x00" + agent.OnBehalfOf
		}
	}
	if !limiter.allow(schluessel) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	if !sperre.frei(schluessel) {
		// F-1149: Die Sperrzeit dieses Fensters ist aufgebraucht. Retry-After
		// nennt kein festes Ende — das Fenster läuft je Prinzipal.
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "lock time budget exceeded")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxOpsBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d bytes", mb.Limit))
			return
		}
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}
	var anfrage opsAnfrage
	if err := json.Unmarshal(raw, &anfrage); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: expected {\"ops\": [...]}")
		return
	}
	if len(anfrage.Ops) == 0 {
		writeError(w, http.StatusBadRequest, "ops must be a non-empty array")
		return
	}
	if len(anfrage.Ops) > MaxOpsJeBatch {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("too many ops: %d > %d", len(anfrage.Ops), MaxOpsJeBatch))
		return
	}
	vorschlag := istVorschlag(AgentFromContext(r.Context()))
	if vorschlag && len(anfrage.Ops) > MaxVorschlaegeJeBatch {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("too many suggestions in one batch: %d > %d", len(anfrage.Ops), MaxVorschlaegeJeBatch))
		return
	}

	sitzung, ok := sitzungAusHeader(r)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be at most %d printable characters", SitzungHeader, maxSessionLaenge))
		return
	}
	ops, fehler := a.stempleOps(anfrage.Ops, actor, AgentFromContext(r.Context()), werkzeugWegAus(r.Context()),
		boardpaarRueckwegAus(r.Context()))
	if len(fehler) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid ops", "ops": fehler})
		return
	}
	if n := rueckwegHashAus(r.Context()); n != nil {
		// Karte #827: Rückweg des Board-Paars — Hash über den Eintrag.
		for i := range ops {
			ops[i].hashNutzlast = n
		}
	}
	opsJSON, err := json.Marshal(ops)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ops marshal failed", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if fehler, err := a.pruefeOps(ctx, ops, string(opsJSON)); err != nil {
		writeFoldError(w, err)
		return
	} else if len(fehler) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid ops", "ops": fehler})
		return
	}

	// Protokoll je Op (ADR-0129 §5, Karte #554): ein Activity-Eintrag pro Op,
	// mit der betroffenen Karte (card_id) und dem Op-Typ. Seit F-1156 in
	// derselben Transaktion wie das Board: Scheitert der Eintrag, ist auch
	// das Board nicht geschrieben — eine Änderung ohne Protokoll gibt es am
	// Ops-Endpunkt nicht mehr.
	// `angewandt` setzt die Falt-Funktion unten: nur die Ops, die nicht schon
	// angenommen waren (Idempotenz, Karte #607). Das Protokoll zählt nur sie.
	var angewandt []serverOp
	var protokoll func(context.Context, storage.Ausfuehrer, *storage.Board) error
	if a.activities != nil {
		kind := akteurArt(r.Context())
		// Karte #608 (ADR-0130 §6): Für wen und in welcher Sitzung — damit ein
		// Abfluss einem Projekt und einer Sitzung zuzuordnen ist.
		fuer := ""
		if agent := AgentFromContext(r.Context()); agent != nil {
			fuer = agent.OnBehalfOf
		}
		protokoll = func(ctx context.Context, ex storage.Ausfuehrer, b *storage.Board) error {
			entries := make([]storage.OpActivityEntry, len(angewandt))
			for i, op := range angewandt {
				entries[i] = storage.OpActivityEntry{CardID: cardIDFromOp(op), OpType: op.Type, ActorKind: kind,
					OnBehalfOf: fuer, Session: sitzungDerOp(sitzung, op)}
			}
			return storage.RecordOpsIn(ctx, ex, b.ID, actor, entries)
		}
	}
	opSchluessel := make([]storage.OpSchluessel, len(ops))
	imBatch := map[string]string{}
	for i, op := range ops {
		opSchluessel[i] = storage.OpSchluessel{ID: op.ClientID, Hash: opHash(op)}
		// Verify V-2: dieselbe id zweimal im SELBEN Batch mit anderem Inhalt —
		// sonst verschluckte der Batch die zweite still als „wiederholt“.
		if op.ClientID == "" {
			continue
		}
		if h, ok := imBatch[op.ClientID]; ok && h != opSchluessel[i].Hash {
			writeError(w, http.StatusBadRequest, "op id "+op.ClientID+" used twice in this batch for different ops")
			return
		}
		imBatch[op.ClientID] = opSchluessel[i].Hash
	}
	wiederholt := make([]bool, len(ops))
	// Karte #666 (ADR-0133 §1): vom Fold neu angelegte Karten je Server-Op-id —
	// für displayId und Finding-nr in der Antwort.
	angelegt := map[string]fold.Angelegt{}

	// Fehlt das Board, gar nicht erst sperren (F-1186): ApplyOps nähme
	// BEGIN IMMEDIATE, fände nichts und riefe falte nie — die Sperre wäre
	// gehalten, aber nicht gebucht. Die Abfrage liest ohne Sperre. Ein Board,
	// das zwischen dieser Prüfung und der Sperre gelöscht wird, bleibt als
	// ungebuchter Rest (ein SELECT unter Sperre) — hingenommen.
	if ok, err := a.repo.Existiert(ctx, id); err != nil {
		writeFoldError(w, err)
		return
	} else if !ok {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}

	// Sperrzeit messen (F-1149): vom ersten Schritt unter der Sperre bis
	// nach dem COMMIT. Die Wartezeit auf BEGIN IMMEDIATE zählt nicht — sie
	// hält niemanden auf. Gebucht wird direkt nach ApplyOps (F-1198): Trim,
	// Event und das Schreiben der Antwort an einen womöglich langsamen Client
	// halten keine Sperre und gehören nicht ins Budget. Der defer ist nur die
	// Rückfallebene für einen Abbruch in ApplyOps; bucheSperre bucht einmal.
	var sperrBeginn time.Time
	bucheSperre := func() {
		if !sperrBeginn.IsZero() {
			sperre.buche(schluessel, opsSperrUhr().Sub(sperrBeginn))
			sperrBeginn = time.Time{}
		}
	}
	defer bucheSperre()

	// Karte #678 (ADR-0135): Dateien der per comment.delete entfernten
	// Kommentare — ermittelt unter der Sperre, gelöscht nach dem COMMIT.
	var kommentarDateien []kommentarAnhang

	// ADR-0136 (F-1596, F-1611): Löschrecht für fremde Kommentare. Die Rolle
	// wird VOR der Sperre gelesen (ACL-Abfrage hält niemanden auf); geprüft
	// wird unter der Sperre am Ergebnis des Folds, für jeden Batch — welche
	// Op einen Kommentar entfernt, spielt keine Rolle.
	schreiber := a.kommentarSchreiberFuer(r.Context(), id)
	vorbedingung := opsVorbedingungAus(r.Context())
	anhangSperre := AgentFromContext(r.Context()) != nil && !werkzeugWegAus(r.Context())
	// Karte #789 (ADR-0140 §10, W-4): Auf der Seite innen sind Karten und
	// Termine, die ein Agent neu anlegt, vertraulich — gleich ob roh, über ein
	// Werkzeug oder MCP (alle laufen hier durch). Die Seite wird vor der
	// Sperre gelesen (ADR-0140 §4: je Anfrage).
	anlageVertraulich := false
	agentInnen := false
	if AgentFromContext(r.Context()) != nil {
		seite, err := a.paarSeiteMitFehler(r.Context())
		if err != nil {
			// Kritiker #789, K-2: Ist die Seite nicht lesbar, weiß der Server
			// nicht, ob W-4 gilt — fail-closed, nichts geschrieben (fehlender
			// Schlüssel oder ungültiger Wert sind „aus“, kein Fehler).
			log.Printf("ops: boardpaar.seite nicht lesbar, Agenten-Batch abgelehnt: %v", err)
			writeError(w, http.StatusServiceUnavailable, "Die Einstellung des Board-Paars ist gerade nicht lesbar. Bitte erneut versuchen; nichts geschrieben.")
			return
		}
		anlageVertraulich = seite == BoardpaarSeiteInnen
		// Karte #798: Herkunftsmarke für jede Agenten-Neuanlage innen —
		// auch wenn W-4 gleich zurückgenommen wird.
		agentInnen = anlageVertraulich
		// Karte #797 (ADR-0141 W-A): Nur ein Werkzeug-Aufruf mit
		// ausdrücklich gesendetem `vertraulich: false` nimmt W-4 zurück.
		if anlageVertraulich && werkzeugWegAus(r.Context()) && anlageOffenAus(r.Context()) {
			anlageVertraulich = false
		}
	}
	// Karte #826 (ADR-0141 §7): Spiegel einer externen Kopie sind bis 2c nur
	// lesbar. Ob das Board eine Kopie ist, wird vor der Sperre gelesen;
	// unlesbar → nichts geschrieben (wie die Seite oben).
	kopie, err := a.repo.IstKopie(r.Context(), id)
	if err != nil {
		log.Printf("ops: Kopplung des Boards nicht lesbar, Batch abgelehnt: %v", err)
		writeError(w, http.StatusServiceUnavailable, "Die Kopplung des Boards ist gerade nicht lesbar. Bitte erneut versuchen; nichts geschrieben.")
		return
	}
	// Karte #827 (ADR-0141 §7, „Umsetzung 2c“ 1–3): Auf einer Kopie entscheidet
	// der Rückweg zwischen Schreibschutz (aus) und feiner Schranke mit
	// Journal (an). Seit #836 (Nachtrag) ist er je Board: boardpaar.rueckweg =
	// an UND bearbeitbar = 1. Gelesen vor der Sperre; unlesbar → nichts
	// geschrieben. War er an, liest der Schreibvorgang ihn unter der Sperre
	// noch einmal (F-2300, spiegelSchreibvorgang.pruefeUndSchreibe).
	var spiegelSchritt *spiegelSchreibvorgang
	var spiegelVorher []byte
	if kopie {
		an, err := a.kopieRueckwegAnMitFehler(r.Context(), id)
		if err != nil {
			log.Printf("ops: boardpaar.rueckweg oder bearbeitbar nicht lesbar, Batch auf Kopie abgelehnt: %v", err)
			writeError(w, http.StatusServiceUnavailable, "Die Einstellung des Board-Paars ist gerade nicht lesbar. Bitte erneut versuchen; nichts geschrieben.")
			return
		}
		spiegelSchritt = &spiegelSchreibvorgang{boardID: id, rueckwegAn: an, person: journalPerson(r.Context(), actor),
			akteurArt: akteurArt(r.Context()), jetzt: time.Now}
	}
	if opsNachSchalterNaht != nil {
		opsNachSchalterNaht()
	}
	var fremdGeloescht []KommentarGeloescht
	// Karte #798: Bremse für Agenten-Anlagen, die nach außen gehen — nur wo
	// W-4 zurückgenommen ist (Werkzeug mit `vertraulich: false`, Seite innen).
	// Reserviert wird im Callback am Ergebnis; scheitert der Schreibvorgang
	// danach, kommt die Reservierung zurück.
	anlageBremseAktiv := agentInnen && !anlageVertraulich
	anlageBremseSchl, anlageBremseGenommen := "", 0
	if anlageBremseAktiv {
		anlageBremseSchl = anlageBremseSchluessel(AgentFromContext(r.Context()).Akteur(), id)
	}
	b, err := a.repo.ApplyOpsIdempotent(ctx, id, opSchluessel, func(state []byte, bekannt map[string]bool) ([]byte, error) {
		sperrBeginn = opsSperrUhr()
		// Schon angenommene Ops (und Doppelte im selben Batch) nicht erneut
		// falten. Die übrigen behalten ts und seq — aufsteigend bleibt es.
		angewandt = angewandt[:0]
		gesehen := map[string]bool{}
		for i, op := range ops {
			wiederholt[i] = op.ClientID != "" && (bekannt[op.ClientID] || gesehen[op.ClientID])
			if op.ClientID != "" {
				gesehen[op.ClientID] = true
			}
			if !wiederholt[i] {
				angewandt = append(angewandt, op)
			}
		}
		if len(angewandt) == 0 {
			return nil, nil
		}
		if vorbedingung != nil {
			// Karte #757 (ADR-0139 E-2): Der Stand, den das Lösch-Werkzeug
			// bestätigt bekam, wird hier unter der Sperre erneut geprüft —
			// zwischen dieser Prüfung und dem COMMIT ändert niemand das Board.
			if err := vorbedingung(state); err != nil {
				return nil, err
			}
		}
		if vorschlag {
			// Karte #670: Swimlane „Vorschläge“ finden oder anlegen, Spalte
			// wählen, Grenzen prüfen — gegen den Stand unter der Sperre. Die
			// Anlage-Op steht danach in `angewandt` und damit im Protokoll.
			var err error
			if angewandt, err = vorschlagVorbereiten(state, angewandt); err != nil {
				return nil, err
			}
		}
		// Stand einmal lesen (F-1628) — für Stempel, Verlauf und Vergleich.
		// Kommentarlisten der Menschen stempeln (authorEmail), Verlauf
		// zusammenführen (F-1621) — dann neu serialisieren, auch wenn die
		// Zahl der Ops gleich bleibt.
		bestand, okBestand := leseKommentarBestand(state)
		neuGestempelt := (schreiber.email != "" && opsSetzenFeld(angewandt, "comments")) || opsSetzenFeld(angewandt, "activity")
		angewandt = stempleKommentarListen(bestand, okBestand, angewandt, schreiber.email)
		angewandt = fuehreVerlaufZusammen(bestand, okBestand, angewandt)
		faltJSON := opsJSON
		if len(angewandt) != len(ops) || vorschlag || neuGestempelt {
			var err error
			if faltJSON, err = json.Marshal(angewandt); err != nil {
				return nil, err
			}
		}
		falte := func(optJSON string) (fold.Ergebnis, error) {
			erg, err := a.fold.FalteMitZielen(ctx, string(state), string(faltJSON), optJSON)
			if err != nil {
				return erg, err
			}
			if opsProtokollNaht != nil {
				erg.Protokoll = opsProtokollNaht(erg.Protokoll)
			}
			if strings.TrimSpace(erg.Protokoll) != "[]" {
				return erg, &opsAbgelehnt{protokoll: mitIndex(erg.Protokoll, ops)}
			}
			return erg, nil
		}
		optJSON := faltOptionen(r.Context(), angewandt)
		if anlageVertraulich {
			optJSON = mitFoldOption(optJSON, "anlageVertraulich")
		}
		if agentInnen {
			optJSON = mitFoldOption(optJSON, "anlageHerkunft")
		}
		erg, err := falte(optJSON)
		if err != nil {
			return nil, err
		}
		// ADR-0136 (F-1611): verbindlich ist der Vergleich am Ergebnis.
		var erlaubt map[string]bool
		if vorschlag {
			erlaubt = map[string]bool{"vorschlag": true} // vorschlagVorbereiten setzt ihn selbst
		}
		verstoesse, geloescht := vergleicheMitBestand(bestand, okBestand, []byte(erg.Projektion), schreiber, id, erlaubt)
		if len(verstoesse) > 0 {
			roh, _ := json.Marshal(kommentarVerstossProtokoll(verstoesse, angewandt))
			return nil, &opsAbgelehnt{protokoll: mitIndex(string(roh), ops)}
		}
		if anhangSperre && opsBeruehrenAnhaenge(angewandt) {
			// Karte #766 (ADR-0139 E-3, Rest W-9): Agenten auf dem rohen Weg
			// ergänzen Anhang-Listen nur — am Ergebnis geprüft, unter der Sperre.
			// Der Vorfilter (F-1886) spart das Lesen beider Stände bei Batches,
			// die keine Anhang-Liste erreichen können.
			if v := anhangListenVerstoesse(state, []byte(erg.Projektion)); len(v) > 0 {
				roh, _ := json.Marshal(anhangVerstossProtokoll(v, angewandt))
				return nil, &opsAbgelehnt{protokoll: mitIndex(string(roh), ops)}
			}
		}
		if AgentFromContext(r.Context()) != nil {
			// Karte #778 (ADR-0140 §10, Stelle 2): „vertraulich“ schaltet nur
			// ein Mensch ab — für jeden Agentenweg, roh wie Werkzeug, am
			// Ergebnis geprüft.
			if v, unlesbar := vertraulichAbgeschaltet(state, []byte(erg.Projektion)); len(v) > 0 || unlesbar {
				roh, _ := json.Marshal(vertraulichVerstossProtokoll(v, unlesbar, angewandt))
				return nil, &opsAbgelehnt{protokoll: mitIndex(string(roh), ops)}
			}
		}
		if kopie && !spiegelSchritt.rueckwegAn {
			if v, unlesbar := schatten.SpiegelVerstoesse(state, []byte(erg.Projektion)); len(v) > 0 || unlesbar {
				roh, _ := json.Marshal(spiegelVerstossProtokoll(v, unlesbar, angewandt))
				return nil, &opsAbgelehnt{protokoll: mitIndex(string(roh), ops)}
			}
		}
		if len(geloescht) > 0 {
			// Owner/Admin hat fremde Kommentare entfernt: derselbe Batch noch
			// einmal, mit dem Verlaufseintrag nach der letzten Op — in
			// derselben Transaktion. Der Fold ist deterministisch; der zweite
			// Lauf unterscheidet sich nur um die Verlaufseinträge.
			verlauf := kommentarVerlauf(geloescht, angewandt[len(angewandt)-1].ID, time.Now())
			if erg, err = falte(mitVerlauf(optJSON, verlauf)); err != nil {
				return nil, err
			}
		}
		fremdGeloescht = geloescht
		neu, err := erg.AngelegteKarten()
		if err != nil {
			return nil, err
		}
		clear(angelegt)
		for _, a := range neu {
			angelegt[a.Op] = a
		}
		if anlageBremseAktiv {
			// Läuft der Callback erneut, zählt nur der letzte Lauf.
			if anlageBremseGenommen > 0 {
				a.anlageBremseHolen().gib(anlageBremseSchl, anlageBremseGenommen)
				anlageBremseGenommen = 0
			}
			if n := len(neu) + neueTermine(state, []byte(erg.Projektion)); n > 0 {
				ok, warten := a.anlageBremseHolen().nimm(anlageBremseSchl, n)
				if !ok {
					log.Printf("ops: Anlage-Bremse (#798): %s auf Board %s, %d neue offene Anlage(n) abgelehnt, Grenze %d je Stunde", actor, id, n, anlageBremseMax)
					return nil, &errAnlageBremse{warten: warten}
				}
				anlageBremseGenommen = n
				if anlageBremseNaht != nil {
					if err := anlageBremseNaht(); err != nil {
						return nil, err
					}
				}
			}
		}
		if vorschlag {
			// Karte #670: Jede Karte eines Vorschlags muss NEU sein. Ein
			// card.upsert auf eine bestehende Karte ersetzte sie ganz; die
			// Server-id macht das schon unwahrscheinlich, hier wird es am
			// Ergebnis des Folds ausgeschlossen — in derselben Transaktion.
			for _, op := range angewandt {
				if op.Type != "card.upsert" {
					continue
				}
				if _, ok := angelegt[op.ID]; !ok {
					return nil, errVorschlagNichtNeu
				}
			}
		}
		kommentarDateien = kommentarAnhaengeZumLoeschen(state, []byte(erg.Projektion), angewandt)
		spiegelVorher = state
		if len(erg.Projektion) > MaxBodyBytes {
			// Sonst entstünde ein Board, das kein Client mehr per PUT
			// zurückschreiben kann.
			return nil, errBoardZuGross
		}
		return []byte(erg.Projektion), nil
	}, schreibvorgangMitSpiegel(spiegelSchritt, func() []byte { return spiegelVorher }, func() []serverOp { return angewandt }, ops, protokoll))
	bucheSperre()
	if err != nil && anlageBremseGenommen > 0 {
		// Nichts geschrieben (auch ErrAllesBekannt): Reservierung zurück.
		a.anlageBremseHolen().gib(anlageBremseSchl, anlageBremseGenommen)
	}
	eb, bremse := istAnlageBremse(err)
	var abgelehnt *opsAbgelehnt
	var vorbedingungVerletzt *opsVorbedingungVerletzt
	var wiederverwendet *storage.IDWiederverwendetError
	switch {
	case errors.As(err, &wiederverwendet):
		// Sitzungs-QA K-3: dieselbe Client-id mit anderem Inhalt — ein Fehler
		// des Absenders, kein Konflikt. Nichts geschrieben.
		writeError(w, http.StatusBadRequest, "op id "+wiederverwendet.ID+" "+opIDWiederverwendetText)
		return
	case errors.Is(err, storage.ErrAllesBekannt):
		// Reine Wiederholung (Karte #607): nichts geschrieben, kein Event.
		quittung := make([]map[string]any, len(ops))
		for i, op := range ops {
			quittung[i] = map[string]any{"id": op.ID, "type": op.Type, "wiederholt": true}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ops": quittung, "wiederholt": true})
		return
	case err == nil:
	case bremse:
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(eb.warten.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("anlage-bremse: höchstens %d nicht vertrauliche Anlagen je Stunde und Token auf diesem Board; vertraulich anlegen geht weiter. Nichts geschrieben.", anlageBremseMax))
		return
	case errors.Is(err, storage.ErrBoardNotFound):
		writeError(w, http.StatusNotFound, "board not found")
		return
	case errors.Is(err, errVorschlagArchiviert):
		writeError(w, http.StatusConflict, "suggestions are paused: the suggestion lane is archived on this board")
		return
	case errors.Is(err, errVorschlagVoll):
		writeError(w, http.StatusConflict, fmt.Sprintf("the suggestion lane is full (at most %d cards); try again after people have reviewed it", MaxVorschlaegeInSwimlane))
		return
	case errors.Is(err, errVorschlagNichtNeu):
		writeError(w, http.StatusConflict, "a suggestion may only create a new card; nothing written")
		return
	case errors.As(err, &abgelehnt) && vorschlag:
		// Das Protokoll nennt Karten und Felder des Boards — nichts davon
		// geht an ein Vorschlags-Token (kein Rückkanal, ADR-0133 §6).
		writeError(w, http.StatusUnprocessableEntity, "suggestion would not apply as intended; nothing written")
		return
	case errors.As(err, &abgelehnt):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":     "ops would not apply as intended; nothing written",
			"protokoll": abgelehnt.protokoll,
		})
		return
	case errors.Is(err, errKopieSchalterUnlesbar):
		log.Printf("ops: %v", err)
		writeError(w, http.StatusServiceUnavailable, "Die Einstellung des Board-Paars ist gerade nicht lesbar. Bitte erneut versuchen; nichts geschrieben.")
		return
	case errors.Is(err, storage.ErrHuelleVerletzt):
		writeError(w, http.StatusUnprocessableEntity, "resulting board would violate the envelope (id, title, version)")
		return
	case errors.Is(err, errBoardZuGross):
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("resulting board exceeds %d bytes", MaxBodyBytes))
		return
	case errors.As(err, &vorbedingungVerletzt):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "precondition failed; nothing written",
			"art": opsVorbedingungArt, "grund": vorbedingungVerletzt.grund})
		return
	default:
		writeFoldError(w, err)
		return
	}

	if a.nachApplyOps != nil {
		// Test-Naht F-1209: im Produktivbetrieb nil, wirkungslos. Simuliert
		// im Test die Dauer der nachgelagerten Arbeit (Trim, Event, Antwort),
		// um zu prüfen, dass bucheSperre() bereits vorher gebucht hat.
		a.nachApplyOps()
	}

	if a.activities != nil {
		// Frist-Trim nach dem COMMIT, nicht-kritisch (wie Record/RecordOps).
		a.activities.TrimNach(context.WithoutCancel(r.Context()), b.ID, len(angewandt))
	}
	publishBoardUpdated(a.broker, b.ID, b.UpdatedAt, "ops")
	a.loescheKommentarAnhaenge(b.ID, kommentarDateien)
	if a.nachKommentarGeloescht != nil {
		// ADR-0136 §3: nach dem COMMIT, nicht-kritisch — die Löschung gilt.
		for _, g := range fremdGeloescht {
			if g.Eigen {
				continue // eigener Antwortkommentar „extern“: Verlauf ja, Benachrichtigung nein
			}
			a.nachKommentarGeloescht(context.WithoutCancel(r.Context()), g)
		}
	}

	// Karte #666 (ADR-0133 §1): Hat die Op eine Karte NEU angelegt, nennt die
	// Quittung die vergebene displayId und bei Findings die nr — beide vergibt
	// der Fold, der Schreiber kennt sie sonst nicht. Ein card.upsert auf eine
	// bestehende Karte und eine wiederholte Op tragen beides nicht.
	type opQuittung struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Wiederholt bool   `json:"wiederholt,omitempty"`
		CardID     string `json:"cardId,omitempty"`
		DisplayID  int    `json:"displayId,omitempty"`
		Nr         *int   `json:"nr,omitempty"`
	}
	quittung := make([]opQuittung, len(ops))
	for i, op := range ops {
		quittung[i] = opQuittung{ID: op.ID, Type: op.Type, Wiederholt: wiederholt[i]}
		if a, ok := angelegt[op.ID]; ok && !wiederholt[i] {
			quittung[i].CardID, quittung[i].DisplayID, quittung[i].Nr = a.CardID, a.DisplayID, a.Nr
			if vorschlag {
				// Karte #670: nur die eigene Karten-id. displayId und nr zählen
				// über das ganze Board hoch — ihre Differenz zwischen zwei
				// Vorschlägen verriete, wie viel andere angelegt haben.
				quittung[i].DisplayID, quittung[i].Nr = 0, nil
			}
		}
	}
	if vorschlag {
		// Kein ETag, kein updated_at: Auch der Stand des Boards ist für ein
		// Vorschlags-Token kein Rückkanal.
		writeJSON(w, http.StatusOK, map[string]any{"ops": quittung})
		return
	}
	w.Header().Set("ETag", etag(b.UpdatedAt))
	writeJSON(w, http.StatusOK, map[string]any{
		"updated_at": b.UpdatedAt,
		"ops":        quittung,
	})
}

var errBoardZuGross = errors.New("gefaltetes Board überschreitet MaxBodyBytes")

// opIDWiederverwendetText: Text der 400-Antwort bei wiederverwendeter Client-id.
// Der Rückweg des Board-Paars erkennt den Fall daran (boardpaar_anwenden.go).
const opIDWiederverwendetText = "was already used for a different op"

// opHash: SHA-256 über Akteur, Typ und kompakte Nutzlast — erkennt eine
// wiederverwendete Client-id mit anderem Inhalt (Sitzungs-QA K-3). Leer ohne
// Client-id. Gerechnet über die Nutzlast NACH der Server-Stempelung — mit einer
// Ausnahme (F-1447): `edited` (comment.edit, #639) trägt die Request-Zeit und
// ist für dieselbe Eingabe NICHT gleich; es bleibt aus dem Hash, sonst bekäme
// jede Wiederholung 400. Die übrigen Stempel (`onBehalfOf` je Token, `author`
// je Prinzipal, `ts` steht gar nicht in der Nutzlast) sind für denselben
// Akteur zeitunabhängig.
//
// Der Akteur gehört dazu (Sprint-77-Kritiker): Ohne ihn bekam ein ANDERER
// Schreiber mit gleicher id und gleicher Nutzlast „wiederholt“ quittiert —
// für eine Op, die nie seine war, und ohne dass seine gewirkt hätte. Jetzt
// ist das 400 „id already used for a different op“. Übergangsfolge: Eine vor
// dem Update gemerkte id passt danach nicht mehr zum Hash; wiederholt ein
// Client genau diese Op über das Update hinweg, bekommt er ebenfalls 400.
func opHash(op serverOp) string {
	if op.ClientID == "" {
		return ""
	}
	nutzlast := op.Payload
	if op.hashNutzlast != nil {
		nutzlast = op.hashNutzlast
	}
	if op.Type == "comment.edit" || op.Type == "comment.editIf" {
		nutzlast = ohneBearbeitetStempel(nutzlast)
	}
	var kompakt bytes.Buffer
	if err := json.Compact(&kompakt, nutzlast); err != nil {
		kompakt.Write(nutzlast)
	}
	h := sha256.Sum256(append([]byte(op.Actor+"\x00"+op.Type+"\x00"), kompakt.Bytes()...))
	return hex.EncodeToString(h[:])
}

// mitFoldOption setzt eine boolesche Server-Option des Folds auf true. Nur
// für Agenten auf der Seite innen:
//   - anlageVertraulich (Karte #789, ADR-0140 §10 W-4): Der Fold macht Karten
//     und Termine, die dieser Batch neu anlegt, vertraulich (server/fold,
//     neuAnlage).
//   - anlageHerkunft (Karte #798): neu angelegte Karten bekommen die
//     Herkunftsmarke angelegtVonAgent, auch bei zurückgenommenem W-4.
func mitFoldOption(optJSON, name string) string {
	opt := map[string]any{}
	if optJSON != "" {
		_ = json.Unmarshal([]byte(optJSON), &opt)
	}
	opt[name] = true
	aus, err := json.Marshal(opt)
	if err != nil {
		return optJSON
	}
	return string(aus)
}

// faltOptionen liefert die Server-Optionen für FalteMitZielen. Für Agenten
// steht dort der eigene Stempel: comment.edit auf einen Kommentar mit anderem
// author lehnt der Fold dann als „fremder-kommentar“ ab (ADR-0130 §4, Karte
// #601). Der Autor steht im Board, nicht in der Op — deshalb dort geprüft und
// nicht in agentPayload.
//
// Dazu uhrUnsicher (ADR-0130 §1, Karte #606): Ist die Systemuhr nicht
// gestellt, wirkt seit dem Sprint-77-Abschluss KEIN card.claim (422
// uhr-unsicher) — die ts der Op und damit jedes Lease-Ende kommt aus genau
// dieser Uhr. Gefragt wird nur, wenn der Batch ein card.claim enthält. Ein
// Lesefehler heisst „nicht gestellt“ (server/uhr). Der Zustandswechsel steht
// im Log (Session-QA, F-1287): Ohne Zeitdienst scheitert sonst jede Übernahme
// ohne einen Hinweis für den Betrieb.
// Form wie uhr.Erlaubt (Grund ohne „<nil>“, Abhilfe genannt), aber eigener
// Text: Erlaubt spricht von Frist-Löschungen (Verify Session-QA, F-1306).
var (
	claimUhrGesperrt atomic.Bool
	claimUhrLog      = log.Printf
)

func faltOptionen(ctx context.Context, ops []serverOp) string {
	// Karte #748 (ADR-0138 §6): Akteur-Art für die Agenten-Marke an Fristen
	// und Terminen — der Fold setzt sie bei Agenten und nimmt sie bei
	// Menschen zurück (server/fold, markeNachher). Immer gesetzt: Auch ein
	// Mensch-Batch muss die Marke zurücknehmen können.
	opt := map[string]any{"akteurArt": akteurArt(ctx)}
	if boardpaarAkteurAus(ctx) != "" {
		// ADR-0141 „Umsetzung 2c“ Punkt 5/23: Der Rückweg schreibt für einen
		// Menschen außen — Agenten-Marken fallen; boardpaarUrsprung bleibt an
		// neu angelegten Karten und Terminen stehen.
		opt["akteurArt"] = "mensch"
		opt["boardpaarAnlage"] = true
	}
	if agent := AgentFromContext(ctx); agent != nil {
		opt["eigenerAutor"] = agent.Akteur()
	}
	for _, op := range ops {
		if op.Type == "card.claim" {
			ok, err := uhr.Gestellt()
			if !ok {
				opt["uhrUnsicher"] = true
				if !claimUhrGesperrt.Swap(true) {
					grund := "der Kernel meldet sie als nicht synchronisiert"
					if err != nil {
						grund = fmt.Sprintf("ihr Status ist nicht lesbar (%v)", err)
					}
					claimUhrLog("aboard-server: card.claim: Systemuhr nicht gestellt — %s. Jede Übernahme wird mit "+
						"422 uhr-unsicher abgelehnt, bis ein Zeitdienst sie stellt. Ohne Zeitdienst: %s=aus "+
						"(schaltet auch den Löschschutz der Fristen ab; ADR-0130 §1).", grund, uhr.PruefungEnvVar)
				}
			} else if claimUhrGesperrt.Swap(false) {
				claimUhrLog("aboard-server: card.claim: Systemuhr jetzt gestellt — Übernahmen wirken wieder.")
			}
			break
		}
	}
	aus, _ := json.Marshal(opt)
	return string(aus)
}

// stempleOps baut aus den eingegangenen Ops die Server-Ops. Geprüft wird hier
// nur, was Go ohne ops.js sehen kann: `type` ist eine Zeichenkette,
// `payload` ein Objekt, keine Wartungs-Ops.
//
// werkzeugWeg: Die Ops kommen von einem Agenten-Werkzeug (werkzeugOpsRoh),
// nicht über den rohen Weg. Nur dort darf ein Agent die Board-Struktur
// schreiben (ADR-0139 E-3, agentRohSperre).
func (a *BoardsAPI) stempleOps(eingang []map[string]json.RawMessage, actor string, agent *AgentPrincipal, werkzeugWeg, rueckweg bool) ([]serverOp, []opFehler) {
	ts := opsNow().Format(opsTSFormat)
	ops := make([]serverOp, 0, len(eingang))
	var fehler []opFehler
	for i, roh := range eingang {
		var typ string
		if err := json.Unmarshal(roh["type"], &typ); err != nil || typ == "" {
			fehler = append(fehler, opFehler{Index: i, Grund: "type fehlt oder ist keine Zeichenkette"})
			continue
		}
		if strings.HasPrefix(typ, "maintenance.") {
			// Seitenkanal-Signale des lokalen Projektors (ADR-0071/0106) —
			// am Server ohne Bedeutung.
			fehler = append(fehler, opFehler{Index: i, Type: typ, Grund: "maintenance ops are not accepted by the server"})
			continue
		}
		if grund := rueckwegOpGrund(typ, rueckweg); grund != "" {
			// Karte #827 (ADR-0141 §5.3): die bedingten Rückweg-Ops nur auf dem
			// Rückweg des Board-Paars (boardpaar_rueckweg.go).
			fehler = append(fehler, opFehler{Index: i, Type: typ, Grund: grund})
			continue
		}
		payload := roh["payload"]
		if t := strings.TrimSpace(string(payload)); !strings.HasPrefix(t, "{") {
			fehler = append(fehler, opFehler{Index: i, Type: typ, Grund: "payload fehlt oder ist kein Objekt"})
			continue
		}
		// Fehlt das id-Feld (oder ist es null), gilt die Op ohne Schlüssel.
		// Ist es da, aber untauglich (keine Zeichenkette, länger als 128 Byte —
		// Bytes, nicht Zeichen: `len` in Go), wird die Op
		// abgelehnt (Sprint-77-Kritiker): Vorher fiel die Idempotenz dann still
		// weg — ein Client, der sich auf Wiederholungssicherheit verlässt, bekam
		// kein Signal, und seine Wiederholung wirkte doppelt (Klasse F-1152).
		// Die leere Zeichenkette zählt ebenfalls als untauglich (Session-QA,
		// F-1288): Sie ist gültiges JSON und kurz genug, liess die Idempotenz
		// aber genauso still wegfallen.
		var clientID string
		if idRoh, da := roh["id"]; da && strings.TrimSpace(string(idRoh)) != "null" {
			if json.Unmarshal(idRoh, &clientID) != nil || len(clientID) > storage.MaxClientIDLaenge || strings.TrimSpace(clientID) == "" {
				fehler = append(fehler, opFehler{Index: i, Type: typ,
					Grund: fmt.Sprintf("id must be a non-empty string of at most %d bytes", storage.MaxClientIDLaenge)})
				continue
			}
		}
		if istVorschlag(agent) {
			// Karte #670 (ADR-0133 §6): Der Server baut die Karte selbst; die
			// übrigen Prüfungen und Stempel unten gelten Ops, die es auf diesem
			// Weg nicht gibt.
			neu, grund := vorschlagPayload(typ, payload, agent, clientID, ts)
			if grund != "" {
				fehler = append(fehler, opFehler{Index: i, Type: typ, Grund: grund})
				continue
			}
			ops = append(ops, serverOp{ClientID: clientID, ID: uuid.NewString(), TS: ts, Seq: i, Actor: actor,
				SchemaVersion: a.fold.SchemaVersion(), Type: typ, Payload: neu, hashNutzlast: payload})
			continue
		}
		if typ == "comment.add" && !kommentarIstObjekt(payload) {
			// inspectOpPayload prüft nur `!= null`, und applyOp hängt auch ein
			// Array an (typeof [] === 'object'). Ein solcher „Kommentar“ trägt
			// keinen Autor — für Agenten hebelte das die Stempelung aus
			// (Kritiker Sprint 71). Fehlt comment ganz, meldet das die
			// Vorprüfung mit dem Feldnamen.
			fehler = append(fehler, opFehler{Index: i, Type: typ, Grund: "comment muss ein Objekt sein"})
			continue
		}
		if grund := payloadFormFehler(typ, payload); grund != "" {
			// F-1152 (Karte #561): Formen, die fold() still verwirft. Hier und
			// nicht in ops.js (Maintainer-Entscheid 2026-09-28 zu F-1146): Der lokale
			// Projektor muss die Historie weiter falten können. Geprüft VOR der
			// Schreibsperre — kostet keine Sperrzeit.
			fehler = append(fehler, opFehler{Index: i, Type: typ, Grund: grund})
			continue
		}
		if typ == "comment.edit" || typ == "comment.editIf" {
			// Karte #639 (F-1264): Der Bearbeitungsvermerk stammt allein vom
			// Server — ein mitgeschickter Wert (jede Schreibweise) fällt weg,
			// auch bei Menschen. Sonst könnte ein Agent seinen Kommentar nach
			// der Antwort eines Menschen unsichtbar umschreiben.
			payload = stempleBearbeitet(payload, ts)
		}
		if typ == "comment.add" {
			// ADR-0136 §1 (F-1596): Die Urheberschaft eines Kommentars stempelt
			// der Server — die E-Mail der Identität bei Menschen, nichts bei
			// Agenten (die tragen author agent:<name>). Ein mitgeschickter Wert
			// fällt immer weg. Darauf hängt das Löschrecht.
			autorEmail := ""
			if agent == nil && !rueckweg {
				// Der Rückweg des Board-Paars stempelt keine E-Mail: Seine
				// Kommentare tragen Autor „extern: …“ (ADR-0141 Punkt 23).
				autorEmail = actor
			}
			payload = stempleAutorEmail(payload, autorEmail)
		}
		if typ == "card.claim" {
			// Karte #602: „für wen“ an der Übernahme stempelt nur der Server —
			// ein mitgeschickter Wert (jede Schreibweise) fällt weg, auch bei
			// Menschen; ein Agenten-Token mit Person setzt seine E-Mail.
			fuer := ""
			if agent != nil {
				fuer = agent.OnBehalfOf
			}
			payload = stempleFuerWen(payload, fuer)
		}
		if agent != nil {
			var grund string
			payload, grund = agentPayload(typ, payload, agent)
			if grund == "" && !werkzeugWeg {
				grund = agentRohSperre(typ, payload)
			}
			if grund != "" {
				fehler = append(fehler, opFehler{Index: i, Type: typ, Grund: grund})
				continue
			}
		}
		ops = append(ops, serverOp{
			ClientID:      clientID,
			ID:            uuid.NewString(),
			TS:            ts,
			Seq:           i,
			Actor:         actor,
			SchemaVersion: a.fold.SchemaVersion(),
			Type:          typ,
			Payload:       payload,
		})
	}
	return ops, fehler
}

// payloadFormFehler prüft Payload-Formen, die applyOp wortlos verwirft
// (F-1152, Karte #561). Nur die Form, nie den Stand: Eine Op, die den Stand
// nicht ändert, bleibt erlaubt, damit ein Batch nach verlorener Antwort
// wiederholt werden kann (ADR-0129 §4, Nachtrag Sprint 72, Punkt 4).
//
// Fehlt ein Pflichtfeld ganz, meldet das die Vorprüfung (inspectOpPayload)
// mit dem Feldnamen; hier geht es um vorhandene Felder mit falschem Typ.
//
// Bewusst NICHT geprüft:
//   - card.field ohne `value`: applyOp setzt das Feld auf undefined, es fällt
//     beim Serialisieren weg. Das ist ein „Feld leeren“, kein No-op, und
//     diffToOps kann es für ein im Speicher undefined gewordenes Feld erzeugen.
//   - card.move ohne toColId: die Karte behält ihre Spalte — gültig.
func payloadFormFehler(typ string, payload json.RawMessage) string {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	switch typ {
	case "card.upsert":
		roh, ok := p["card"]
		if !ok {
			return ""
		}
		var karte map[string]json.RawMessage
		if json.Unmarshal(roh, &karte) != nil || karte == nil {
			return "card muss ein Objekt sein"
		}
		if !nichtLeereZeichenkette(karte["id"]) {
			return "card.id muss eine nicht leere Zeichenkette sein"
		}
		for _, k := range []string{"sprintId", "columnId"} {
			if v, ok := p[k]; ok && !nullOderZeichenkette(v) {
				return k + " muss eine Zeichenkette sein"
			}
		}
	case "card.fieldIf":
		// ADR-0130 §2 (Karte #604): Ohne erwarteten Vorwert gibt es nichts zu
		// prüfen. ops.js wiese die Op als erwartung-fehlt ab (422); hier schon
		// als Formfehler (400), weil es ein Fehler des Schreibers ist, kein
		// Konflikt. `expect: null` ist erlaubt.
		if _, ok := p["expect"]; !ok {
			return "expect fehlt (für eine bedingungslose Änderung card.field verwenden)"
		}
	case "termin.fieldIf":
		// Karte #827 (ADR-0141 §5.3): wie card.fieldIf — `expect: null` erlaubt.
		if _, ok := p["expect"]; !ok {
			return "expect fehlt"
		}
	case "card.moveIf":
		for _, k := range []string{"toSprintId", "toColId", "expectSprintId", "expectColId"} {
			if v, ok := p[k]; ok && !nichtLeereZeichenkette(v) {
				return k + " muss eine nicht leere Zeichenkette sein"
			}
		}
	case "card.checkItemIf":
		var c struct {
			Text      *string  `json:"text"`
			Vorkommen *float64 `json:"vorkommen"`
			Erledigt  *bool    `json:"erledigt"`
			Expect    *bool    `json:"expect"`
		}
		if json.Unmarshal(payload, &c) != nil || c.Text == nil || c.Vorkommen == nil || c.Erledigt == nil || c.Expect == nil ||
			*c.Vorkommen < 1 || *c.Vorkommen != float64(int64(*c.Vorkommen)) {
			return "text (Zeichenkette), vorkommen (ganze Zahl ab 1), erledigt und expect (true/false) sind Pflicht"
		}
	case "comment.editIf":
		for _, k := range []string{"text", "expect"} {
			var t string
			if json.Unmarshal(p[k], &t) != nil {
				return k + " muss eine Zeichenkette sein"
			}
		}
		if !nichtLeereZeichenkette(p["commentId"]) {
			return "commentId muss eine nicht leere Zeichenkette sein"
		}
	case "card.claim", "card.release":
		// ADR-0130 §1 (Karte #606): Formen, die ops.js als wirkungslos meldet
		// (lease-ungueltig, session-ungueltig, erzwingen-ungueltig) — hier schon
		// als Fehler des Schreibers (400), nicht als Konflikt (422).
		if v, ok := p["leaseSeconds"]; ok && typ == "card.claim" && strings.TrimSpace(string(v)) != "null" {
			var n float64
			if json.Unmarshal(v, &n) != nil {
				return "leaseSeconds muss eine Zahl (Sekunden) sein"
			}
		}
		if v, ok := p["session"]; ok {
			var sitzung string
			if !nullOderZeichenkette(v) || (json.Unmarshal(v, &sitzung) == nil && len([]rune(sitzung)) > maxSessionLaenge) {
				return fmt.Sprintf("session muss eine Zeichenkette mit höchstens %d Zeichen sein", maxSessionLaenge)
			}
		}
		if v, ok := p["erzwingen"]; ok && typ == "card.release" {
			if t := strings.TrimSpace(string(v)); t != "true" && t != "false" && t != "null" {
				return "erzwingen muss true oder false sein"
			}
		}
		// Karte #618 (F-1257): der erwartete Halter — ein Objekt mit
		// Zeichenketten (oder null) für by/session/since. ops.js wiese eine
		// andere Form als expect-ungueltig ab (422); hier schon als 400.
		if v, ok := p["expect"]; ok && typ == "card.release" && strings.TrimSpace(string(v)) != "null" {
			var e map[string]json.RawMessage
			if json.Unmarshal(v, &e) != nil || e == nil {
				return "expect muss ein Objekt { by, session, since } sein"
			}
			for _, k := range []string{"by", "session", "since"} {
				if w, ok := e[k]; ok && !nullOderZeichenkette(w) {
					return "expect." + k + " muss eine Zeichenkette sein"
				}
			}
		}
	case "board.awaiting":
		// ADR-0130 §7 (Karte #609): genau ein Schlüssel der awaitingUser-
		// Abbildung. ops.js wiese eine andere Form als wirkungslos ab (422);
		// hier schon als Fehler des Schreibers (400). Die Grenzen (Länge,
		// Prototyp-Schlüssel) prüft allein der Fold — eine Regel, ein Aufschrieb.
		if v, ok := p["person"]; ok && !nichtLeereZeichenkette(v) {
			return "person muss eine nicht leere Zeichenkette sein"
		}
		// F-1336 (Karte #645): optionale Sitzung — ein Eintrag je Sitzung.
		// Das Muster prüft der Fold (awaitingSitzungGueltig), hier nur die Form.
		if v, ok := p["session"]; ok && strings.TrimSpace(string(v)) != "null" && !nichtLeereZeichenkette(v) {
			return "session muss eine nicht leere Zeichenkette sein (oder fehlen)"
		}
		if v, ok := p["value"]; ok && strings.TrimSpace(string(v)) != "null" {
			var e map[string]json.RawMessage
			if json.Unmarshal(v, &e) != nil || e == nil {
				return "value muss null oder ein Objekt { question, since, askedBy } sein"
			}
			if !nichtLeereZeichenkette(e["question"]) || !nichtLeereZeichenkette(e["since"]) {
				return "value.question und value.since müssen nicht leere Zeichenketten sein"
			}
			if w, ok := e["askedBy"]; !ok || !nullOderZeichenkette(w) || strings.TrimSpace(string(w)) == "null" {
				return "value.askedBy muss eine Zeichenkette sein"
			}
		}
	case "comment.edit":
		// ADR-0130 §4 (Karte #601). fold() überspränge eine solche Op still.
		if v, ok := p["commentId"]; ok && !nichtLeereZeichenkette(v) {
			return "commentId muss eine nicht leere Zeichenkette sein"
		}
		if v, ok := p["text"]; ok {
			var t string
			if json.Unmarshal(v, &t) != nil {
				return "text muss eine Zeichenkette sein"
			}
		}
	case "swimlane.upsert":
		roh, ok := p["sprint"]
		if !ok {
			return ""
		}
		var sp map[string]json.RawMessage
		if json.Unmarshal(roh, &sp) != nil || sp == nil {
			return "sprint muss ein Objekt sein"
		}
		if !nichtLeereZeichenkette(sp["id"]) {
			return "sprint.id muss eine nicht leere Zeichenkette sein"
		}
	}
	return ""
}

// SitzungHeader nennt die Sitzungskennung eines Clients (Karte #608, ADR-0130
// §6): aboard-remote (#602) schickt sie mit jedem Batch, der Server schreibt
// sie ins Protokoll. Ohne Header gilt die `session` einer claim/release-Op.
const SitzungHeader = "X-Aboard-Session"

// sitzungAusHeader liest den Header; ok=false bei unzulässigem Inhalt
// (zu lang, Steuerzeichen) — der landet sonst ungeprüft im Protokoll.
func sitzungAusHeader(r *http.Request) (string, bool) {
	s := strings.TrimSpace(r.Header.Get(SitzungHeader))
	if len([]rune(s)) > maxSessionLaenge {
		return "", false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	return s, true
}

// sitzungDerOp: der Header, sonst die `session` aus der Payload einer
// claim/release-Op (Formfehler hat payloadFormFehler vorher abgewiesen).
func sitzungDerOp(header string, op serverOp) string {
	if header != "" || (op.Type != "card.claim" && op.Type != "card.release") {
		return header
	}
	// Exakter Map-Schlüssel wie im Fold (Session-Kritiker Sprint 78, K-7):
	// Ein Struct-Unmarshal nähme auch "SESSION" — dann stünde im Protokoll
	// eine Sitzung, die der Fold nie gesehen hat.
	var p map[string]json.RawMessage
	if json.Unmarshal(op.Payload, &p) != nil {
		return ""
	}
	var sitzung string
	_ = json.Unmarshal(p["session"], &sitzung)
	return sitzung
}

// maxSessionLaenge: Obergrenze für die Sitzungskennung einer Übernahme —
// dieselbe Zahl wie CLAIM_SESSION_MAX in src/ops.js.
const maxSessionLaenge = 200

func nichtLeereZeichenkette(v json.RawMessage) bool {
	var s string
	return v != nil && json.Unmarshal(v, &s) == nil && s != ""
}

func nullOderZeichenkette(v json.RawMessage) bool {
	if strings.TrimSpace(string(v)) == "null" {
		return true
	}
	var s string
	return json.Unmarshal(v, &s) == nil
}

// kommentarIstObjekt: comment fehlt (das meldet die Vorprüfung) oder ist ein
// JSON-Objekt.
func kommentarIstObjekt(payload json.RawMessage) bool {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return true
	}
	c, ok := p["comment"]
	if !ok {
		return true
	}
	t := strings.TrimSpace(string(c))
	return t == "null" || strings.HasPrefix(t, "{")
}

// cardIDFromOp leitet die betroffene Karte aus dem Payload einer Op ab —
// fürs Protokoll je Karte (ADR-0129 §5, Karte #554). Die meisten Op-Typen
// tragen `cardId` direkt im Payload (card.field, card.move, comment.add, …);
// `card.upsert` trägt sie stattdessen unter `card.id` (F-451: eigenes
// Payload-Schema, siehe OP_PAYLOAD_SPEC in ops.js). Op-Typen ohne
// Karten-Bezug (z. B. swimlane.upsert, board.field) liefern "" (leer).
func cardIDFromOp(op serverOp) string {
	var mitCardID struct {
		CardID string `json:"cardId"`
	}
	if err := json.Unmarshal(op.Payload, &mitCardID); err == nil && mitCardID.CardID != "" {
		return mitCardID.CardID
	}
	if op.Type == "card.upsert" {
		var mitCard struct {
			Card struct {
				ID string `json:"id"`
			} `json:"card"`
		}
		if err := json.Unmarshal(op.Payload, &mitCard); err == nil {
			return mitCard.Card.ID
		}
	}
	return ""
}

// akteurArt liefert die Akteur-Art fürs Protokoll (ADR-0129 §5, Karte #554):
// "mensch" | "agent" | "system". Ein AgentPrincipal im Kontext (Karte #553)
// ergibt "agent", sonst "mensch". "system" vergibt heute niemand über diesen
// Weg — außer dem Rückweg des Board-Paars (ADR-0141 „Umsetzung 2c“ Punkt 23).
func akteurArt(ctx context.Context) string {
	if boardpaarAkteurAus(ctx) != "" {
		return "system"
	}
	if AgentFromContext(ctx) != nil {
		return "agent"
	}
	return "mensch"
}

// agentPayload setzt die Urheberschaft für Agenten auch INNERHALB des
// Payloads durch (Kritiker Sprint 71, K-A2). Der Server stempelt `actor` auf
// die Op, aber was im Board sichtbar ist, kommt aus dem Payload: der Autor
// eines Kommentars und die Felder `comments`/`activity` einer Karte. Ohne
// diese Regeln könnte ein Agent Kommentare und Verlaufseinträge unter einem
// Menschennamen ablegen — oder mit `comments: []` alle Kommentare einer
// Karte leeren, ohne dass die Entkernungs-Prüfung anschlägt (der Schlüssel
// ist ja vorhanden).
//
//   - comment.add: comment.author wird auf agent:<name> gesetzt.
//   - card.field auf comments/activity: abgelehnt.
//   - card.upsert mit comments/activity in der Karte: abgelehnt. Bestehende
//     Karten ändert ein Agent über card.field; eine neue Karte legt er ohne
//     diese beiden Felder an.
//   - card.claim ohne session: abgelehnt (Sitzungs-QA #606, K-3).
//   - card.release mit erzwingen: abgelehnt (ADR-0130 §1).
//   - Schlüssel in zwei Schreibweisen (Payload, card, comment): abgelehnt
//     (Sitzungs-QA #606, K-1) — Go und der Fold lasen sonst verschiedene Werte.
//   - comment.delete: abgelehnt (Kritiker Sprint 71). Sonst leerte ein
//     Agent mit einem Batch alle Kommentare der Menschen.
//   - comment.edit: erlaubt, aber nur auf eigene Kommentare. Das prüft der
//     Fold gegen den Stand (faltOptionen, ADR-0130 §4), nicht diese Funktion.
//   - board.field auf identities: abgelehnt (Kritiker Sprint 71). Aus
//     identities leitet der Browser Anzeigenamen und die Mail-Adressen für
//     Benachrichtigungen ab (src/identity.js).
//   - board.awaiting: nur für die eigene Person, `person` = E-Mail des Tokens;
//     Projekt-Tokens gar nicht (F-1310, Karte #602).
//   - termin.upsert: `termin.erstelltVon` setzt der Server (Karte #743).
//
// Menschen (Browser) sind ausgenommen: Sie schreiben ab Sprint 72 ganze
// Karten über diesen Weg.
func agentPayload(typ string, payload json.RawMessage, agent *AgentPrincipal) (json.RawMessage, string) {
	// Sitzungs-QA #606/#616, K-1: Go ordnet JSON-Schlüssel an Struct-Felder
	// OHNE Beachtung der Groß-/Kleinschreibung zu, und bei mehreren Treffern
	// gewinnt der letzte. Der Fold in goja liest dagegen exakt. Aus
	// {"erzwingen":true,"ERZWINGEN":false} las diese Prüfung false, der Fold
	// true — ein Agent hob so eine fremde Übernahme auf. Deshalb: Schlüssel,
	// die sich nur in der Schreibweise unterscheiden, lehnt der Server für
	// Agenten ab, und gelesen wird hier nur noch über exakte Map-Schlüssel.
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return payload, ""
	}
	if k := schreibweisenDoppel(p); k != "" {
		return payload, "payload key " + k + " appears twice in different case"
	}
	zeichenkette := func(roh json.RawMessage) string {
		var s string
		_ = json.Unmarshal(roh, &s)
		return s
	}
	if agent.Delegiert {
		// ADR-0133 §4, Karte #669: Hinter einem delegierenden Token teilen sich
		// alle Personen denselben Akteur `agent:<name>`. Wo die Erlaubnis am
		// Akteur hängt — eigenen Kommentar bearbeiten (Fold: eigenerAutor),
		// Übernahme halten und freigeben (claim.by + session) —, könnte Person
		// A sonst Kommentare und Übernahmen von Person B anfassen. Diese Ops
		// gibt es deshalb über den delegierenden Weg nicht.
		switch typ {
		case "comment.edit", "card.claim", "card.release":
			return payload, typ + " is not available via a delegating token (all persons share its actor)"
		}
	}
	switch typ {
	case "comment.add":
		var c map[string]json.RawMessage
		if json.Unmarshal(p["comment"], &c) != nil || c == nil {
			// Fehlt: meldet die Vorprüfung. Kein Objekt (etwa ein Array):
			// stempleOps lehnt das für alle ab, bevor es hier ankommt.
			return payload, ""
		}
		if k := schreibweisenDoppel(c); k != "" {
			return payload, "comment key " + k + " appears twice in different case"
		}
		autor, _ := json.Marshal(agent.Akteur())
		c["author"] = autor
		// Karte #608: für wen — vom Server gesetzt, nie vom Agenten. Ein
		// Projekt-Token ohne Person trägt keinen Stempel (auch keinen
		// mitgeschickten).
		for k := range c {
			if strings.EqualFold(k, "onBehalfOf") {
				delete(c, k)
			}
		}
		if agent.OnBehalfOf != "" {
			c["onBehalfOf"], _ = json.Marshal(agent.OnBehalfOf)
		}
		p["comment"], _ = json.Marshal(c)
		neu, _ := json.Marshal(p)
		return neu, ""
	case "card.field", "card.fieldIf":
		if f := zeichenkette(p["field"]); f == "comments" || f == "activity" {
			return payload, "agents may not set comments/activity via card.field (use comment.add)"
		}
	case "comment.delete":
		return payload, "agents may not delete comments"
	case "card.claim":
		// Sitzungs-QA K-3: Ohne Sitzung wären zwei Sitzungen mit demselben
		// Token derselbe Halter — beide „gewönnen“ (ADR-0130 §1).
		if strings.TrimSpace(zeichenkette(p["session"])) == "" {
			return payload, "agents must send a session with card.claim (two sessions sharing a token would otherwise both hold the card)"
		}
	case "card.release":
		// ADR-0130 §1 (Karte #606): Eine fremde Übernahme aufheben darf nur
		// ein Mensch. Ein Agent gibt nur frei, was er selbst hält.
		if t := strings.TrimSpace(string(p["erzwingen"])); t != "" && t != "false" && t != "null" {
			return payload, "agents may not force a release (only the holder releases; a human can force it)"
		}
	case "board.field":
		if zeichenkette(p["field"]) == "identities" {
			return payload, "agents may not set identities"
		}
	case "board.awaiting":
		// F-1310 (Karte #602): Ein Agent setzt oder löscht nur den Eintrag
		// SEINER Person. Gemeinsame Personenkennung auf Server-Boards ist die
		// E-Mail (onBehalfOf); der Browser erkennt sie über getCurrentUser()
		// ohne Rücksicht auf Groß-/Kleinschreibung (src/awaiting.js), deshalb
		// hier derselbe Vergleich. Ein Projekt-Token hat keine Person — ein
		// Eintrag „für jemanden“ wäre geraten, also abgelehnt (Ausfall in
		// Richtung Nichtstun).
		if agent.OnBehalfOf == "" {
			return payload, "board.awaiting needs an agent token with a person (project tokens have none)"
		}
		if storage.NormalizeEmail(zeichenkette(p["person"])) != storage.NormalizeEmail(agent.OnBehalfOf) {
			return payload, "agents may only set their own person's awaiting entry (person must be the token's e-mail)"
		}
		// BEWUSST NICHT auf eine Normalform gestempelt (Verify F-C, Session-QA
		// Sprint 78): Der Fold setzt und löscht `person` wörtlich. Ein im Browser
		// angelegter Schlüssel „Anna@Firma.DE“ ließe sich sonst vom Agenten nicht
		// mehr löschen (Banner bliebe, Op je Prompt). Die Hooks wählen den
		// vorhandenen Schlüssel ohne Rücksicht auf Groß-/Kleinschreibung und
		// löschen alle Varianten (_common.py `_schluessel_alle`).
	case "termin.upsert":
		// Karte #743 (ADR-0138 §4): Herkunft stempelt der Server, wie den
		// Autor eines Kommentars. Ein mitgeschicktes erstelltVon (in jeder
		// Schreibweise) fällt weg. Wirksam wird der Stempel nur bei der
		// Anlage — auf einen vorhandenen Termin übernimmt der Fold die
		// Herkunft immer vom Bestand (src/ops.js TERMIN_HERKUNFT). Kein
		// Objekt: Die Form prüft der Fold (termin-kein-objekt → 422).
		var t map[string]json.RawMessage
		if json.Unmarshal(p["termin"], &t) != nil || t == nil {
			return payload, ""
		}
		if k := schreibweisenDoppel(t); k != "" {
			return payload, "termin key " + k + " appears twice in different case"
		}
		for k := range t {
			if strings.EqualFold(k, "erstelltVon") {
				delete(t, k)
			}
		}
		t["erstelltVon"], _ = json.Marshal(agentTerminHerkunft(agent))
		p["termin"], _ = json.Marshal(t)
		neu, _ := json.Marshal(p)
		return neu, ""
	case "card.upsert":
		var karte map[string]json.RawMessage
		_ = json.Unmarshal(p["card"], &karte)
		if k := schreibweisenDoppel(karte); k != "" {
			return payload, "card key " + k + " appears twice in different case"
		}
		_, hatC := karte["comments"]
		_, hatA := karte["activity"]
		if hatC || hatA {
			return payload, "agents may not send comments/activity in card.upsert (use card.field / comment.add)"
		}
	}
	return payload, ""
}

// agentTerminHerkunft: `erstelltVon` eines Termins, den ein Agent anlegt
// (Karte #743) — `agent:<name>`, mit Person „agent:<name> für <E-Mail>“, wie
// der Browser Kommentare im Auftrag anzeigt. Ein Feld, damit der Termin die
// Felder aus ADR-0138 §4 behält.
func agentTerminHerkunft(agent *AgentPrincipal) string {
	if agent.OnBehalfOf != "" {
		return agent.Akteur() + " für " + agent.OnBehalfOf
	}
	return agent.Akteur()
}

// schreibweisenDoppel liefert einen Schlüssel, der in m in mehr als einer
// Schreibweise vorkommt (etwa "erzwingen" und "ERZWINGEN"), sonst "".
// Verglichen wird mit strings.EqualFold — derselben Faltung, die
// encoding/json beim Zuordnen benutzt (inklusive "ſ" ~ "s" und "K" ~ "k");
// strings.ToLower sähe "commentſ" und "comments" als verschieden.
func schreibweisenDoppel(m map[string]json.RawMessage) string {
	schluessel := make([]string, 0, len(m))
	for k := range m {
		schluessel = append(schluessel, k)
	}
	for i := range schluessel {
		for j := i + 1; j < len(schluessel); j++ {
			if strings.EqualFold(schluessel[i], schluessel[j]) {
				return schluessel[j]
			}
		}
	}
	return ""
}

// mitIndex ergänzt jeden Eintrag des Fold-Protokolls um den Index der Op im
// Batch (ADR-0129 §4, Nachtrag Sprint 72, Punkt 4; Karte #565). Der Fold nennt
// die Op nur über `op`, die id, die der Server selbst vergeben hat und die der
// Browser nicht kennt. Mit dem Index kann der Browser die abgelehnten Ops aus
// seinem Batch nehmen und den Rest einmal neu senden. Einträge ohne bekannte
// id (sollte es nicht geben) bleiben ohne Index; lässt sich das Protokoll
// nicht lesen, geht es unverändert hinaus — die Ablehnung selbst darf an der
// Zusatzangabe nicht scheitern.
func mitIndex(protokoll string, ops []serverOp) json.RawMessage {
	var eintraege []map[string]any
	if err := json.Unmarshal([]byte(protokoll), &eintraege); err != nil {
		return json.RawMessage(protokoll)
	}
	index := make(map[string]int, len(ops))
	for i, op := range ops {
		index[op.ID] = i
	}
	for _, e := range eintraege {
		if id, ok := e["op"].(string); ok {
			if i, ok := index[id]; ok {
				e["index"] = i
			}
		}
	}
	aus, err := json.Marshal(eintraege)
	if err != nil {
		return json.RawMessage(protokoll)
	}
	return aus
}

// pruefeOps wendet validateOp und inspectOpPayload aus ops.js an. Anders als
// der lokale Projektor lehnt der Server auch unbekannte Payload-Schlüssel ab:
// Sie sind fast immer ein Namensdreher (`toColumnId` statt `toColId`, F-451),
// und die Op wirkte dann nur halb.
func (a *BoardsAPI) pruefeOps(ctx context.Context, ops []serverOp, opsJSON string) ([]opFehler, error) {
	pr, err := a.fold.Pruefe(ctx, opsJSON)
	if err != nil {
		return nil, err
	}
	if len(pr) != len(ops) {
		return nil, fmt.Errorf("Vorprüfung: %d Ergebnisse für %d Ops", len(pr), len(ops))
	}
	var fehler []opFehler
	for i, p := range pr {
		if p.Gueltig && len(p.Unbekannt) == 0 {
			continue
		}
		f := opFehler{Index: i, Type: ops[i].Type, Fehlend: p.Fehlend}
		for _, u := range p.Unbekannt {
			f.Unbekannt = append(f.Unbekannt, unbekannterSchlue{Key: u.Key, Meant: u.Meant})
		}
		switch {
		case !p.Gueltig && len(p.Fehlend) > 0:
			f.Grund = "pflichtfelder fehlen"
		case !p.Gueltig:
			f.Grund = "ungültige op (unbekannter type?)"
		default:
			f.Grund = "unbekannte payload-schlüssel"
		}
		fehler = append(fehler, f)
	}
	return fehler, nil
}

// writeFoldError bildet Fehler des Fold-Motors und der Transaktion ab.
func writeFoldError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fold.ErrZeitgrenze):
		writeError(w, http.StatusServiceUnavailable, "fold time limit exceeded", err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, storage.ErrDBBelegt):
		writeError(w, http.StatusServiceUnavailable, "server busy", err)
	default:
		writeError(w, http.StatusInternalServerError, "ops failed", err)
	}
}

// stempleFuerWen entfernt jede Schreibweise von `onBehalfOf` aus der Payload
// und setzt `fuer`, wenn nicht leer. Ist die Payload kein Objekt, bleibt sie
// unverändert (die Formprüfung davor hat das schon abgewiesen).
func stempleFuerWen(payload json.RawMessage, fuer string) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil || p == nil {
		return payload
	}
	for k := range p {
		if strings.EqualFold(k, "onBehalfOf") {
			delete(p, k)
		}
	}
	if fuer != "" {
		p["onBehalfOf"], _ = json.Marshal(fuer)
	}
	neu, err := json.Marshal(p)
	if err != nil {
		return payload
	}
	return neu
}

// ohneBearbeitetStempel entfernt `edited` (jede Schreibweise) für die
// Hash-Rechnung. Kein Objekt oder nicht lesbar: unverändert.
func ohneBearbeitetStempel(payload json.RawMessage) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil || p == nil {
		return payload
	}
	for k := range p {
		if strings.EqualFold(k, "edited") {
			delete(p, k)
		}
	}
	neu, err := json.Marshal(p)
	if err != nil {
		return payload
	}
	return neu
}

// stempleBearbeitet entfernt jede Schreibweise von `edited` aus der Payload
// und setzt die Server-Zeit (Karte #639). Ist die Payload kein Objekt, bleibt
// sie unverändert (die Formprüfung davor hat das schon abgewiesen).
func stempleBearbeitet(payload json.RawMessage, ts string) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil || p == nil {
		return payload
	}
	for k := range p {
		if strings.EqualFold(k, "edited") {
			delete(p, k)
		}
	}
	p["edited"], _ = json.Marshal(ts)
	neu, err := json.Marshal(p)
	if err != nil {
		return payload
	}
	return neu
}

// schreibvorgangMitSpiegel setzt die Arbeit im Schreibvorgang zusammen (vor dem
// COMMIT, auf dessen Verbindung): erst Spiegel-Schranke und Journal einer
// Kopie (Karte #827), dann das Protokoll je Op. Beides läuft unabhängig
// davon, ob das Protokoll eingerichtet ist (a.activities). vorher und
// angewandt liest es erst beim Aufruf — die Falt-Funktion setzt sie.
func schreibvorgangMitSpiegel(spiegel *spiegelSchreibvorgang, vorher func() []byte, angewandt func() []serverOp, ops []serverOp,
	protokoll func(context.Context, storage.Ausfuehrer, *storage.Board) error) func(context.Context, storage.Ausfuehrer, *storage.Board) error {
	if spiegel == nil {
		return protokoll
	}
	return func(ctx context.Context, ex storage.Ausfuehrer, b *storage.Board) error {
		if err := spiegel.pruefeUndSchreibe(ctx, ex, vorher(), b.StateJSON, angewandt(), ops); err != nil {
			return err
		}
		if protokoll != nil {
			return protokoll(ctx, ex, b)
		}
		return nil
	}
}
