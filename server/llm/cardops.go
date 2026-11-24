package llm

// cardops.go — state_json-Mutationen für LLM-Send (#299, ADR-0095 §3, §7).
//
// Bewusste Ausnahme vom „state_json ist opaque"-Prinzip (ADR-0044), analog
// zu server/mail/cardops.go — der Endpoint muss server-seitig einen
// Antwort-Kommentar + Activity-Entry an die Karte anhängen.
//
// Pattern: BoardRepo.Get → state_json parsen → mutieren → BoardRepo.Update
// mit ifMatch (Optimistic-Concurrency). Retry-Schleife bei ErrConflict.
//
// Code-Duplikation gegenüber mail/cardops.applyStateMutation ist bewusst
// (kein gemeinsames Helper-Paket — beide Pakete sind eigenständige Bounded
// Contexts; eine spätere Hebung nach server/storage/mutation kann ein
// eigenes Refaktor sein).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
)

// MaxMutationRetries: bei ErrConflict so oft den Read-Modify-Write neu
// versuchen. LLM-Send ist user-getriggered, parallele Edits am Board sind
// möglich (anderer User bearbeitet andere Karte).
const MaxMutationRetries = 5

// LLMReplyParams bündelt alle Felder, die in Kommentar + Activity-Entry
// landen. ADR-0095 §3 (Activity-Entry-Schema mit meta-Feld) + §4 (Antwort
// als Kommentar mit Provider-Autor-Label).
type LLMReplyParams struct {
	BoardID string
	CardID  string

	// Kommentar-Felder
	CommentID string // UUID — Idempotenz-Anker (doppelter Send mit gleicher ID = no-op)
	Author    string // sichtbarer Autor (z. B. "🤖 anthropic/claude-opus-4-7 (im Auftrag von alice@x.de)")
	Body      string // LLM-Antwort (Markdown, optional gecappt)
	Created   string // ISO-8601 (UTC) — wenn leer, nutzt Mutator now()

	// Activity-Entry-Felder
	ActivityTS      string // ISO-8601 (UTC) — wenn leer, nutzt Mutator now()
	ActivityActor   string // sendender User (Email), nicht der Provider
	Provider        string // provider-Name (z. B. "anthropic")
	Model           string // Modell (z. B. "claude-opus-4-7")
	InputTokens     int
	OutputTokens    int
	CostEstimateUSD float64
	// Optional: User-Hinweis welche Kontext-Bausteine selektiert waren
	// (z. B. "Beschreibung + 10 Kommentare + 3 Verweise"). Im Frontend
	// für die Activity-Render-Note hilfreich.
	ContextNote string

	// Fetch-Audit (ADR-0120 §6). FetchedCards trägt die Kurzbezeichnungen
	// der Karten, die das Modell in diesem Turn nachgeladen hat (z. B.
	// "#123"); FetchesRejected die Zahl der abgelehnten Anfragen.
	//
	// Bewusst EIN Eintrag pro Turn statt einer pro Abruf: Zehn eigene
	// Aktivitäts-Einträge pro Antwort würden das Karten-Log fluten, in dem
	// sonst Owner-Wechsel und Spalten-Bewegungen stehen. Die Information —
	// welche Karte in welchem Send gelesen wurde — bleibt vollständig.
	//
	// Beide Felder landen nur im meta, wenn tatsächlich nachgeladen bzw.
	// abgelehnt wurde: eine Null in jedem Eintrag ist das Rauschen, das dazu
	// führt, dass niemand mehr hinsieht.
	FetchedCards    []string
	FetchesRejected int

	// Attachments (ADR-0121 §6, nachgezogen nach dem Kritiker-Lauf) — die
	// Anhänge, die mit diesem Turn an das Modell gingen, als "<id> (<mime>,
	// <bytes> B)". Die id ist der Anker: Der Dateiname steht im
	// Karten-Zustand, ist nicht eindeutig und jederzeit änderbar. Ohne id
	// lässt sich später nicht feststellen, WELCHE Datei an ein fremdes
	// Modell gegangen ist — der Fließtext in contextNote allein reichte
	// dafür nicht.
	Attachments []string
}

// AddLLMReply hängt den LLM-Antwort-Kommentar + den passenden Activity-
// Eintrag an die Karte an. Idempotent gegenüber identischer CommentID
// (wird in dem Fall gar nichts angehängt, auch nicht der Activity-Entry —
// der Send wurde schon vorher erfolgreich persistiert).
//
// Schema des Activity-Entry (ADR-0095 §3):
//
//	{
//	  "ts": "2026-05-23T12:34:56Z",
//	  "actor": "alice@x.de",
//	  "action": "llm-sent",
//	  "from": "",
//	  "to": "anthropic/claude-opus-4-7",
//	  "meta": {
//	    "commentId": "<UUID>",
//	    "inputTokens": 1234,
//	    "outputTokens": 567,
//	    "costEstimateUSD": 0.0042,
//	    "contextNote": "Beschreibung + 10 Kommentare"
//	  }
//	}
//
// Das `meta`-Feld ist NEU im ActivityEntry-Schema (Plan-Etappe 2b) — Frontend-
// Schema-Migration in src/activity.js kommt in Etappe 4 mit dem UI-Render.
// Backend persistiert es roh als Map.
func AddLLMReply(ctx context.Context, boards *storage.BoardRepo, params LLMReplyParams) error {
	if params.BoardID == "" || params.CardID == "" {
		return errors.New("llm.AddLLMReply: BoardID/CardID erforderlich")
	}
	if params.CommentID == "" {
		return errors.New("llm.AddLLMReply: CommentID erforderlich")
	}
	return applyStateMutation(ctx, boards, params.BoardID, func(state map[string]any) (bool, error) {
		boardObj, _ := state["board"].(map[string]any)
		sprints, _ := boardObj["sprints"].([]any)
		for _, sp := range sprints {
			spMap, _ := sp.(map[string]any)
			cards, _ := spMap["cards"].([]any)
			for _, c := range cards {
				cm, _ := c.(map[string]any)
				if id, _ := cm["id"].(string); id != params.CardID {
					continue
				}
				// Idempotenz: gleicher commentId schon vorhanden → no-op (Activity
				// ebenfalls nicht doppeln, falls zu identischem Send-Versuch).
				existingComments, _ := cm["comments"].([]any)
				for _, ex := range existingComments {
					if em, ok := ex.(map[string]any); ok {
						if cid, _ := em["id"].(string); cid == params.CommentID {
							return false, nil
						}
					}
				}
				// Kommentar anhängen
				comment := map[string]any{
					"id":      params.CommentID,
					"author":  params.Author,
					"text":    params.Body,
					"created": params.Created,
				}
				// ADR-0136 §1: Die KI-Antwort gehört der Person, die gefragt
				// hat — sie darf sie verwerfen (comment.delete), andere nur
				// als Board-Owner.
				if params.ActivityActor != "" {
					comment["authorEmail"] = params.ActivityActor
				}
				cm["comments"] = append(existingComments, comment)

				// Activity-Entry anhängen
				activity, _ := cm["activity"].([]any)
				meta := map[string]any{
					"commentId":       params.CommentID,
					"inputTokens":     params.InputTokens,
					"outputTokens":    params.OutputTokens,
					"costEstimateUSD": params.CostEstimateUSD,
					"contextNote":     params.ContextNote,
				}
				if len(params.FetchedCards) > 0 {
					// []string → []any, damit der Eintrag denselben dynamischen
					// Typ hat wie alles andere im state_json (der Round-Trip
					// durch json.Unmarshal liefert []any).
					geladen := make([]any, 0, len(params.FetchedCards))
					for _, l := range params.FetchedCards {
						geladen = append(geladen, l)
					}
					meta["fetchedCards"] = geladen
				}
				if params.FetchesRejected > 0 {
					meta["fetchesRejected"] = params.FetchesRejected
				}
				if len(params.Attachments) > 0 {
					anh := make([]any, 0, len(params.Attachments))
					for _, a := range params.Attachments {
						anh = append(anh, a)
					}
					meta["attachments"] = anh
				}
				entry := map[string]any{
					"ts":     params.ActivityTS,
					"actor":  params.ActivityActor,
					"action": "llm-sent",
					"from":   "",
					"to":     fmt.Sprintf("%s/%s", params.Provider, params.Model),
					"meta":   meta,
				}
				cm["activity"] = append(activity, entry)
				return true, nil
			}
		}
		return false, fmt.Errorf("llm.AddLLMReply: Karte %s nicht im Board %s gefunden", params.CardID, params.BoardID)
	})
}

// DiscardLLMReplyParams bündelt die Felder, die für den Discard-Pfad
// (ADR-0095 §3, Etappe 4) gebraucht werden.
type DiscardLLMReplyParams struct {
	BoardID   string
	CardID    string
	CommentID string // UUID der LLM-Antwort, die entfernt werden soll
	// Activity-Entry-Felder
	ActivityTS    string // ISO-8601 (UTC)
	ActivityActor string // discardender User (Email)
	Provider      string // gespiegelt vom Original-llm-sent-Entry (Audit-Symmetrie)
	Model         string // dito
}

// ErrLLMCommentNotFound — Discard-Pfad: angegebene CommentID nicht (mehr) an
// der Karte gefunden. Erlaubt dem Endpoint eine 404-Antwort statt eines
// stummen 200.
var ErrLLMCommentNotFound = errors.New("llm.DiscardLLMReply: comment not found on card")

// DiscardLLMReply entfernt den LLM-Antwort-Kommentar mit gegebener
// CommentID und hängt einen 'llm-response-discarded'-Activity-Entry an.
// Idempotenz-Charakter: zweimaliger Discard (Kommentar bereits weg) liefert
// ErrLLMCommentNotFound — der Endpoint kann das in einen 404 mappen oder als
// no-op behandeln.
//
// Audit-Symmetrie: der Discard-Entry trägt dasselbe to="<provider>/<model>"
// wie der ursprüngliche llm-sent-Entry, plus meta.commentId zum Verschnüren.
// Token-/Cost-Zahlen werden NICHT dupliziert (die stehen am llm-sent-Entry).
//
// Berechtigt zum Discard ist im MVP jeder editor+ am Board — Audit-Trail
// macht die Aktion nachvollziehbar (Diskussion: ADR-0095 §3 hat keinen
// expliziten „nur Sender darf verwerfen"-Constraint — sinnvolle Erweiterung
// bei Bedarf in Folgekarte).
func DiscardLLMReply(ctx context.Context, boards *storage.BoardRepo, params DiscardLLMReplyParams) error {
	if params.BoardID == "" || params.CardID == "" {
		return errors.New("llm.DiscardLLMReply: BoardID/CardID erforderlich")
	}
	if params.CommentID == "" {
		return errors.New("llm.DiscardLLMReply: CommentID erforderlich")
	}
	return applyStateMutation(ctx, boards, params.BoardID, func(state map[string]any) (bool, error) {
		boardObj, _ := state["board"].(map[string]any)
		sprints, _ := boardObj["sprints"].([]any)
		for _, sp := range sprints {
			spMap, _ := sp.(map[string]any)
			cards, _ := spMap["cards"].([]any)
			for _, c := range cards {
				cm, _ := c.(map[string]any)
				if id, _ := cm["id"].(string); id != params.CardID {
					continue
				}
				existing, _ := cm["comments"].([]any)
				filtered := make([]any, 0, len(existing))
				found := false
				for _, ex := range existing {
					exMap, _ := ex.(map[string]any)
					// ADR-0136 Nachtrag (Karte #693): Verworfen wird nur eine
					// KI-Antwort der Person, die verwirft — sonst entfernte
					// dieser Weg jeden Kommentar (auch Menschen-Kommentare),
					// vorbei am Löschrecht. Alles andere gilt als „nicht
					// gefunden“; fremde Kommentare entfernt der Owner über
					// comment.delete (mit Verlauf und Benachrichtigung).
					if id, _ := exMap["id"].(string); id == params.CommentID && istEigeneKIAntwort(exMap, params.ActivityActor) {
						found = true
						continue
					}
					filtered = append(filtered, ex)
				}
				if !found {
					return false, ErrLLMCommentNotFound
				}
				cm["comments"] = filtered

				// Activity-Entry anhängen
				activity, _ := cm["activity"].([]any)
				entry := map[string]any{
					"ts":     params.ActivityTS,
					"actor":  params.ActivityActor,
					"action": "llm-response-discarded",
					"from":   "",
					"to":     fmt.Sprintf("%s/%s", params.Provider, params.Model),
					"meta": map[string]any{
						"commentId": params.CommentID,
					},
				}
				cm["activity"] = append(activity, entry)
				return true, nil
			}
		}
		return false, fmt.Errorf("llm.DiscardLLMReply: Karte %s nicht im Board %s gefunden", params.CardID, params.BoardID)
	})
}

// istEigeneKIAntwort: Ist der Kommentar eine KI-Antwort, die `email`
// angefragt hat? Seit ADR-0136 trägt sie authorEmail. Ältere (F-1615) erkennt
// man am Autor-Label, das der Server vergibt („🤖 <anbieter>/<modell> (im
// Auftrag von <e-mail>)“). Nicht am llm-sent-Verlaufseintrag: activity ist
// für Menschen schreibbar. Ein Kommentar ohne „🤖 “ am Anfang ist keine
// KI-Antwort und wird hier nie entfernt.
func istEigeneKIAntwort(k map[string]any, email string) bool {
	if email == "" {
		return false
	}
	autor, _ := k["author"].(string)
	if !strings.HasPrefix(autor, "🤖 ") {
		return false
	}
	if ae, da := k["authorEmail"]; da {
		s, _ := ae.(string)
		return strings.EqualFold(s, email)
	}
	const auftrag = " (im Auftrag von "
	i := strings.LastIndex(autor, auftrag)
	return i >= 0 && strings.HasSuffix(autor, ")") && strings.EqualFold(autor[i+len(auftrag):len(autor)-1], email)
}

// applyStateMutation lädt state_json, ruft den Mutator auf und schreibt
// zurück mit ifMatch-Schutz. Bei ErrConflict wird neu geladen und maximal
// MaxMutationRetries-mal probiert. Mutator gibt (mutated, error) zurück;
// mutated=false → keine Änderung, kein Write (no-op).
//
// Bewusste Code-Duplikation gegenüber mail/cardops.applyStateMutation —
// siehe Datei-Header.
func applyStateMutation(ctx context.Context, boards *storage.BoardRepo, boardID string, mutate func(map[string]any) (bool, error)) error {
	if boards == nil {
		return errors.New("llm.applyStateMutation: boards-Repo nil")
	}
	for attempt := 0; attempt < MaxMutationRetries; attempt++ {
		b, err := boards.Get(ctx, boardID)
		if err != nil {
			return fmt.Errorf("get board %s: %w", boardID, err)
		}
		var state map[string]any
		if err := json.Unmarshal(b.StateJSON, &state); err != nil {
			return fmt.Errorf("parse state_json (board %s): %w", boardID, err)
		}
		mutated, mErr := mutate(state)
		if mErr != nil {
			return mErr
		}
		if !mutated {
			return nil // no-op
		}
		newJSON, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("marshal state_json (board %s): %w", boardID, err)
		}
		_, err = boards.Update(ctx, b.ID, b.Title, b.Version, newJSON, b.UpdatedAt)
		if err == nil {
			return nil
		}
		if errors.Is(err, storage.ErrConflict) {
			continue
		}
		return fmt.Errorf("update board %s: %w", boardID, err)
	}
	return fmt.Errorf("llm.applyStateMutation: board %s nach %d Retries weiterhin im Konflikt", boardID, MaxMutationRetries)
}
