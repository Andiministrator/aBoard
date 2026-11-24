package mail

// cardops.go — State-JSON-Mutationen für den Mail-Ingest (#295, ADR-0093 §5+§6).
//
// Bewusste Ausnahme vom „state_json ist opaque"-Prinzip (ADR-0044) — analog zu
// server/api/email.go loadCardFromBoard: für Mail-Ingest brauchen wir
// server-seitige Karten-Mutationen.
//
// Pattern: BoardRepo.Get → state_json parsen → mutieren → BoardRepo.Update
// mit ifMatch (Optimistic-Concurrency). Retry-Schleife bei ErrConflict.
//
// Beide Mutationen (Comment hinzufügen / Karte anlegen) operieren über eine
// generische applyStateMutation-Wrapper-Funktion mit fester Retry-Anzahl.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

// MaxMutationRetries: bei ErrConflict so oft den Read-Modify-Write neu
// versuchen. Sehr unwahrscheinlich im Mail-Ingest-Pfad (Worker ist Single-
// Goroutine), aber gegen parallele User-Edits in derselben Board absichern.
const MaxMutationRetries = 5

// AddCommentToCard fügt einen Kommentar an die Karte loc.CardID an. created
// ist das Mail-Datum (RFC-3339). authorEmail wird mit dem 📬-Präfix versehen.
// Optional weitere Markdown-Hinweise (Attachment-Hinweis o. Ä.).
//
// F-358: attachmentIDs werden in `card.attachments` gemerged (nicht in den
// Kommentar — das aBoard-Datenmodell führt Anhänge auf Karten-Ebene, nicht
// pro Kommentar; ADR-0033). Duplikate werden gefiltert.
//
// Idempotent gegen identische commentID (UUID — der Caller hashed ggf. die
// Mail-Message-ID, um Re-Replays zu absorbieren). Bei Re-Play mit demselben
// commentID werden die Anhänge auch NICHT erneut gemerged (sie sind beim
// ersten Pass schon drin).
func AddCommentToCard(ctx context.Context, boards *storage.BoardRepo, loc CardLocation, commentID, authorEmail, body, created string, attachments []IngestAttachment) error {
	if commentID == "" {
		commentID = uuid.NewString()
	}
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339)
	}
	return applyStateMutation(ctx, boards, loc.BoardID, func(state map[string]any) (bool, error) {
		boardObj, _ := state["board"].(map[string]any)
		sprints, _ := boardObj["sprints"].([]any)
		for _, sp := range sprints {
			spMap, _ := sp.(map[string]any)
			cards, _ := spMap["cards"].([]any)
			for _, c := range cards {
				cm, _ := c.(map[string]any)
				if id, _ := cm["id"].(string); id != loc.CardID {
					continue
				}
				// Bestehende Kommentare laden
				existing, _ := cm["comments"].([]any)
				// Idempotenz: doppelte commentId nicht erneut anhängen
				for _, ex := range existing {
					if em, ok := ex.(map[string]any); ok {
						if cid, _ := em["id"].(string); cid == commentID {
							return false, nil // bereits drin — Anhänge wurden beim Erst-Pass schon gemerged
						}
					}
				}
				comment := map[string]any{
					"id":      commentID,
					"author":  "📬 " + authorEmail,
					"text":    truncateCardText(body), // F-361
					"created": created,
				}
				cm["comments"] = append(existing, comment)
				// F-358: Anhänge in card.attachments mergen (dedupliziert)
				// F-855: Bestehende Einträge können in BEIDEN Formen vorliegen —
				// Objekte (Frontend-Upload, Ingest seit Sprint 69) und reine
				// id-Strings (Ingest-Bestand davor). Die Duplikat-Prüfung muss
				// beide kennen.
				if len(attachments) > 0 {
					existingAtts, _ := cm["attachments"].([]any)
					seen := map[string]bool{}
					for _, a := range existingAtts {
						if id := anhangID(a); id != "" {
							seen[id] = true
						}
					}
					heute := time.Now().UTC().Format("2006-01-02")
					for _, att := range attachments {
						if !seen[att.ID] {
							existingAtts = append(existingAtts, att.kartenEintrag(heute))
							seen[att.ID] = true
						}
					}
					cm["attachments"] = existingAtts
				}
				// updated-Date der Karte refreshen
				cm["updated"] = time.Now().UTC().Format("2006-01-02")
				return true, nil
			}
		}
		return false, fmt.Errorf("cardops.AddComment: Karte %s nicht im state_json gefunden", loc.CardID)
	})
}

// MaxCardTextBytes begrenzt Mail-Text, der in eine Karten-Description oder
// einen Kommentar geschrieben wird (F-361).
//
// Ohne dieses Limit war nur die Mail ALS GANZES begrenzt (MaxMailBytes, 25 MB)
// — eine einzelne Karte konnte also ~20 MB Plain-Text aufnehmen. Das bläht das
// state_json des Service-Boards auf, das bei jeder Mutation vollständig
// gelesen, geparst und zurückgeschrieben wird; im Browser trifft es zusätzlich
// den Renderer. F-345 hat denselben Missstand für den REST-Endpoint
// (`server/api/email.go`) bereits auf 256 KB begrenzt — der IMAP-Pfad war nie
// angeglichen worden. Gleicher Wert, gleiche Semantik.
const MaxCardTextBytes = 256 * 1024

// truncateCardText kürzt zu langen Mail-Text und hängt einen sichtbaren
// Hinweis an, statt still abzuschneiden — sonst wirkt eine gekürzte Mail wie
// eine vollständige und der Empfänger übersieht, dass etwas fehlt.
//
// Schneidet an einer UTF-8-Grenze, damit die Description gültiges UTF-8 bleibt
// (ein zerschnittenes Multibyte-Zeichen hätte JSON-Encoding gebrochen — die
// gleiche Klasse wie F-422 im iCal-Export).
func truncateCardText(s string) string {
	if len(s) <= MaxCardTextBytes {
		return s
	}
	cut := MaxCardTextBytes
	// Rückwärts bis zum Anfang einer UTF-8-Sequenz (Bytes 10xxxxxx sind
	// Fortsetzungs-Bytes und dürfen nicht das letzte bleiben).
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf(
		"\n\n---\n_[Text gekürzt: %d KB von ursprünglich %d KB. Der vollständige Inhalt steht in der Original-Mail.]_",
		cut/1024, len(s)/1024)
}

// NewServiceCard beschreibt eine im Service-Board anzulegende Karte.
type NewServiceCard struct {
	CardID      string // optional: vor-generierte cardID. Wenn leer, wird intern UUID erzeugt.
	Title       string
	Description string
	SenderEmail string
	MessageID   string // RFC-5322 Message-ID
	IsSpam      bool   // direkt nach col-spam (statt col-inbox)
	// Attachments: bereits gespeicherte Anhänge (Storage-Volume schreibt der
	// Caller). Seit F-855 mit Metadaten, nicht mehr als reine ids.
	Attachments []IngestAttachment
}

// CreateCardInServiceBoard legt eine neue Karte im Service-Board (id="service")
// an. Spalte: col-inbox (default) oder col-spam (bei IsSpam=true).
//
// Karte bekommt eine frische UUID. displayId wird via state.board.nextDisplayId
// vergeben + inkrementiert (Server-side äquivalent zum Browser-/Projektor-Pfad).
//
// Returns: die neu vergebene cardId (UUID) und displayId (Ticket-Nummer) für
// den Caller (z. B. Attachment-Storage-Pfad zu setzen, Auto-Reply-Subject).
func CreateCardInServiceBoard(ctx context.Context, boards *storage.BoardRepo, card NewServiceCard) (cardID string, displayID int, err error) {
	// F-358: cardID kann vom Caller vor-generiert sein (z. B. weil bereits
	// Anhänge unter <boardId>/<cardId>/... abgelegt wurden). Leer = frische UUID.
	newCardID := card.CardID
	if newCardID == "" {
		newCardID = uuid.NewString()
	}
	colID := "col-inbox"
	if card.IsSpam {
		colID = "col-spam"
	}
	var assignedDisplayID int
	err = applyStateMutation(ctx, boards, storage.ServiceBoardID, func(state map[string]any) (bool, error) {
		boardObj, _ := state["board"].(map[string]any)
		if boardObj == nil {
			return false, fmt.Errorf("cardops.CreateInService: state.board fehlt")
		}
		// nextDisplayId hochzählen
		next := 1
		if v, ok := boardObj["nextDisplayId"].(float64); ok {
			next = int(v)
		}
		assignedDisplayID = next
		boardObj["nextDisplayId"] = float64(next + 1)

		// Karten-Objekt nach src/model.js-Schema
		today := time.Now().UTC().Format("2006-01-02")
		newCard := map[string]any{
			"id":                newCardID,
			"displayId":         float64(next),
			"title":             card.Title,
			"description":       truncateCardText(card.Description), // F-361
			"status":            "offen",
			"priority":          "mittel",
			"labels":            []any{},
			"milestone":         nil,
			"session":           nil,
			"comments":          []any{},
			"qa":                false,
			"critical":          false,
			"blocked":           false,
			"owner":             "",
			"assignees":         []any{},
			"complexity":        "",
			"effort":            nil,
			"agentContext":      "",
			"links":             []any{},
			"attachments":       kartenEintraege(card.Attachments, today),
			"coverAttachmentId": nil,
			"dueDate":           nil,
			"startDate":         nil,
			"recurrence":        nil,
			"archived":          false,
			"activity":          []any{},
			"columnId":          colID,
			"created":           today,
			"updated":           today,
			"senderEmail":       card.SenderEmail,
			"messageId":         card.MessageID,
		}

		// In __service__-Lane einsortieren (am Ende der gewählten Spalte)
		sprints, _ := boardObj["sprints"].([]any)
		for _, sp := range sprints {
			spMap, _ := sp.(map[string]any)
			if id, _ := spMap["id"].(string); id == "__service__" {
				cards, _ := spMap["cards"].([]any)
				spMap["cards"] = append(cards, newCard)
				return true, nil
			}
		}
		return false, fmt.Errorf("cardops.CreateInService: __service__-Lane fehlt im Service-Board")
	})
	if err != nil {
		return "", 0, err
	}
	return newCardID, assignedDisplayID, nil
}

// IngestAttachment beschreibt einen vom Mail-Ingest abgelegten Anhang (F-855).
//
// In `card.attachments` landet er als Objekt in derselben Form, die das
// Frontend beim Upload schreibt (`src/attachments.js` uploadFile):
// `{id, filename, mime, size, created}`. Bis Sprint 69 schrieb der Ingest
// reine id-Strings; die bleiben als Bestand stehen und werden nicht migriert
// — Leser müssen weiterhin beide Formen vertragen.
type IngestAttachment struct {
	ID       string
	Filename string // bereits entschärft (sanitizeAttachmentFilename) — derselbe Name wie in der Ablage
	MIME     string // siehe ingestMIME
	Size     int64  // Bytes der abgelegten Datei
}

// kartenEintrag liefert die state_json-Form. Zahlen als float64, weil das
// die Form ist, die jeder Round-Trip durch json.Unmarshal ohnehin erzeugt.
func (a IngestAttachment) kartenEintrag(created string) map[string]any {
	return map[string]any{
		"id":       a.ID,
		"filename": a.Filename,
		"mime":     a.MIME,
		"size":     float64(a.Size),
		"created":  created,
	}
}

// kartenEintraege konvertiert die Anhänge zu []any für die JSON-Mutation.
func kartenEintraege(atts []IngestAttachment, created string) []any {
	out := make([]any, 0, len(atts))
	for _, a := range atts {
		out = append(out, a.kartenEintrag(created))
	}
	return out
}

// anhangID liest die id eines `card.attachments`-Eintrags in beiden Formen
// (Objekt oder Bestands-String). "" = nicht adressierbar.
func anhangID(eintrag any) string {
	switch v := eintrag.(type) {
	case string:
		return v
	case map[string]any:
		id, _ := v["id"].(string)
		return id
	}
	return ""
}

// applyStateMutation lädt state_json, ruft den Mutator auf und schreibt zurück
// mit ifMatch-Schutz. Bei ErrConflict wird neu geladen und maximal
// MaxMutationRetries-mal probiert. Mutator gibt (mutated, error) zurück;
// mutated=false → keine Änderung, kein Write (no-op).
//
// Sicherer „read-modify-write": map-Mutation wirkt durch (maps sind Ref).
// Mutator MUSS state mutieren (nicht ersetzen) — wir marshalen `state` nach
// dem Aufruf.
func applyStateMutation(ctx context.Context, boards *storage.BoardRepo, boardID string, mutate func(map[string]any) (bool, error)) error {
	if boards == nil {
		return errors.New("cardops.applyStateMutation: boards-Repo nil")
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
			// Optimistic-Concurrency-Konflikt → Retry mit frischem State
			continue
		}
		return fmt.Errorf("update board %s: %w", boardID, err)
	}
	return fmt.Errorf("cardops.applyStateMutation: board %s nach %d Retries weiterhin im Konflikt", boardID, MaxMutationRetries)
}

// truncate kürzt einen String auf maxLen UTF-8-Codepoints (nicht Bytes).
// Wird für Subject→Title-Kürzung verwendet (ADR-0093 §6: max 200 Zeichen).
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

// SanitizeTitle bereitet das Subject als Karten-Titel auf: Reply-Strip war
// schon im ParsedMail, hier noch Truncate + Trim + Whitespace-Normalisierung.
func SanitizeTitle(subject string) string {
	s := strings.TrimSpace(subject)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		s = "(kein Betreff)"
	}
	return truncate(s, 200)
}
