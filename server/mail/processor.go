package mail

// processor.go — zentrale Verarbeitungslogik einer einzelnen Mail (#295, ADR-0093).
//
// IngestMessage ist der zentrale Eintrittspunkt: parsed Mail rein → Routing-
// Entscheidung (Ticket-Match vs. Service-Board-Inbox) → State-Mutation.
//
// Spam-/Loop-Filter ist als optionales Hook konfiguriert — wird in Etappe 3
// (#295) befüllt; im MVP-Default leer (alles wird verarbeitet außer harten
// Header-Loops).
//
// Worker-Polling-Goroutine (worker.go) ruft IngestMessage pro Mail-UID auf,
// nach erfolgreichem Aufruf wird die Mail in imap.processed_folder verschoben.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

// AttachmentSaver ist die minimal nötige Schnittstelle, um persistente
// Anhänge zu schreiben. Production-Implementierung: *storage.AttachmentStore.
// Tests können ein In-Memory-Fake einhängen, ohne FS anzufassen.
//
// F-358 (Sprint-41-Kritiker): bis #295 wurden `parsed.Attachments` nirgends
// abgelegt — `CreateCardInServiceBoard.Attachments` und der Kommentar-Pfad
// hatten gar kein Attachment-Feld. PDF-Rechnungen per Mail landeten als
// reine Body-Karten ohne Anhang. ADR-0093 §5/§6.
type AttachmentSaver interface {
	Save(boardId, cardId, attId, filename string, r io.Reader) error
}

// IngestResult beschreibt den Ausgang eines Verarbeitungslaufs.
type IngestResult struct {
	Action     string // "comment", "service-card", "service-spam-card", "dropped", "skipped"
	BoardID    string // bei comment/service-card gefüllt
	CardID     string // bei comment/service-card gefüllt
	DisplayID  int    // bei comment gefüllt (Ticket-Treffer)
	DropReason string // bei dropped/skipped gesetzt
}

// IngestFilter ist ein Hook für Pre-Routing-Filter (Loop/Spam, ADR-0093 §7).
// Liefert (skipReason, skip) — skip=true droppt die Mail vor jeder Mutation.
type IngestFilter func(*ParsedMail) (string, bool)

// IngestOptions bündelt zur Laufzeit konfigurierbare Optionen.
type IngestOptions struct {
	// PreFilters: harte Drop-Filter (Eigenversand, Loop-Header-Custom, …).
	// Werden VOR jedem Routing aufgerufen. Treffer = Mail wird verworfen,
	// keine Karte angelegt. Reihenfolge wie eingetragen, erster Skip gewinnt.
	PreFilters []IngestFilter
	// SpamDetectors: weichere Heuristik (Blocklist, Rate-Limit). Treffer
	// routet die Mail nach col-spam (Aufbewahrung); bei SpamDrop=true wird
	// statt geroutet gedroppt. Greifen NUR im No-Match-Pfad (Tickets sind
	// User-initiierte Replies — kein Spam-Routing).
	SpamDetectors []IngestFilter
	// ServiceBoardEnabled: wenn false (ADR-0092 §4 service-board.enabled=false),
	// werden No-Match-Mails verworfen (kein Routing-Ziel).
	ServiceBoardEnabled bool
	// SpamDrop: wenn true (ADR-0092 §4 mail.spam_drop=true), werden als
	// Spam erkannte Mails gedroppt statt nach col-spam geroutet.
	SpamDrop bool
	// SkipAttachmentFilters: nur für Tests. Default-Pipeline läuft immer:
	// FilterBlockedMIME → FilterInlineTracking → DedupAttachments.
	SkipAttachmentFilters bool
	// Attachments persistiert kept-Anhänge im Storage-Volume (ADR-0093 §5/§6,
	// F-358). Wenn nil, werden Anhänge nicht abgelegt — Karten-/Kommentar-Pfad
	// erhalten dann KEINE attachment-IDs (Rückwärtskompatibel zum bisherigen
	// Verhalten; Daten gehen verloren). Production-Pfad setzt das in
	// server/main.go aus dem geteilten *storage.AttachmentStore.
	Attachments AttachmentSaver
	// AutoReply enthält die optionalen Auto-Reply-Abhängigkeiten (#297).
	// Wenn nil oder AutoReply.AutoCfg.Enabled=false, wird kein Auto-Reply gesendet.
	// Fehler beim Auto-Reply-Versand brechen den Ingest-Flow NICHT ab.
	AutoReply *AutoReplyDeps
	// BoardName wird für das Auto-Reply-Template benötigt ({{.BoardName}}).
	// Leer = "aBoard".
	BoardName string
	// OnServiceCardCreated ist ein optionaler Callback nach erfolgreichem
	// Anlegen einer Service-Board-Karte. Wird vom Notification-Subsystem
	// (#305, ADR-0107) genutzt um service.new-card-Events zu emittieren.
	// Fehler im Callback brechen den Ingest-Flow NICHT ab.
	OnServiceCardCreated func(boardID, cardID, cardTitle, senderEmail string, displayID int)
}

// ErrNoServiceBoard signalisiert dass eine No-Match-Mail ankam, aber das
// Service-Board deaktiviert ist (DropReason).
var ErrNoServiceBoard = errors.New("mail: no-match without service-board-enabled")

// IngestMessage verarbeitet eine bereits geparste Mail. Reine Funktion auf
// (parsed, deps) — kein I/O außer durch boards (DB) + optionalen Spam-Repo
// (in Etappe 3 ergänzt).
//
// Loop-Header-Hard-Skip ist hier verdrahtet (ADR-0093 §7, „Auto-Submitted:
// auto-replied" / „Precedence: bulk/list/junk" / „X-Loop: aboard"). Weitere
// Filter (Rate-Limit, Blocklist) kommen via opts.PreFilters in Etappe 3 dazu.
func IngestMessage(ctx context.Context, boards *storage.BoardRepo, parsed *ParsedMail, opts IngestOptions) (*IngestResult, error) {
	if parsed == nil {
		return nil, errors.New("mail.IngestMessage: parsed nil")
	}

	// 1) Hart-Loop-Header
	if reason, skip := loopHeaderSkip(parsed); skip {
		return &IngestResult{Action: "dropped", DropReason: reason}, nil
	}

	// 2) Caller-konfigurierte Pre-Filter (Eigenversand, custom Loop-Header)
	for _, f := range opts.PreFilters {
		if reason, skip := f(parsed); skip {
			return &IngestResult{Action: "dropped", DropReason: reason}, nil
		}
	}

	// 3) Anhang-Pipeline VOR Routing — sowohl Ticket- als auch No-Match-Pfad
	//    bekommen denselben gefilterten/deduplizierten Anhangs-Stand.
	if !opts.SkipAttachmentFilters {
		runAttachmentPipeline(parsed)
	}

	// 4) Ticket-Match versuchen
	if displayID, ok := FindTicketID(parsed.Subject, parsed.Body); ok {
		loc, err := FindCardByDisplayID(ctx, boards, displayID)
		if err != nil {
			return nil, fmt.Errorf("ingest: find card #%d: %w", displayID, err)
		}
		if loc != nil {
			created := ""
			if !parsed.Date.IsZero() {
				created = parsed.Date.UTC().Format("2006-01-02T15:04:05Z")
			}
			body := composeCommentBody(parsed)
			commentID := DeriveCommentIDFromMessageID(parsed.MessageID)
			// F-358: Anhänge persistieren VOR der State-Mutation, damit die
			// IDs deterministisch in die Karte gemerged werden können.
			anhaenge, attErr := persistKeptAttachments(opts.Attachments, loc.BoardID, loc.CardID, parsed.Attachments)
			if attErr != nil {
				return nil, fmt.Errorf("ingest: persist attachments for #%d: %w", displayID, attErr)
			}
			if err := AddCommentToCard(ctx, boards, *loc, commentID, parsed.From, body, created, anhaenge); err != nil {
				return nil, fmt.Errorf("ingest: add comment to #%d: %w", displayID, err)
			}
			return &IngestResult{
				Action:    "comment",
				BoardID:   loc.BoardID,
				CardID:    loc.CardID,
				DisplayID: displayID,
			}, nil
		}
		// Ticket nicht gefunden → wie No-Match behandeln (Karten-ID war
		// vermutlich aus einem anderen Server, oder Karte gelöscht)
	}

	// 5) Spam-Detection (nur No-Match-Pfad) — bei Treffer entweder Drop
	//    (SpamDrop=true) oder col-spam-Routing.
	isSpam := false
	spamReason := ""
	for _, f := range opts.SpamDetectors {
		if reason, hit := f(parsed); hit {
			isSpam = true
			spamReason = reason
			break
		}
	}
	if isSpam && opts.SpamDrop {
		return &IngestResult{Action: "dropped", DropReason: "spam: " + spamReason}, nil
	}

	// 6) No-Match → Service-Board (oder Drop, wenn deaktiviert)
	if !opts.ServiceBoardEnabled {
		return &IngestResult{Action: "dropped", DropReason: "service-board disabled"}, ErrNoServiceBoard
	}

	desc := composeDescriptionBody(parsed)
	// F-358: cardID vor-generieren, damit Anhänge unter dem korrekten
	// Storage-Pfad <boardId>/<cardId>/<attId>__<filename> abgelegt werden
	// können, bevor die State-Mutation läuft.
	preCardID := uuid.NewString()
	anhaenge, attErr := persistKeptAttachments(opts.Attachments, storage.ServiceBoardID, preCardID, parsed.Attachments)
	if attErr != nil {
		return nil, fmt.Errorf("ingest: persist service-card attachments: %w", attErr)
	}
	cardTitle := SanitizeTitle(parsed.Subject)
	cardID, displayID, err := CreateCardInServiceBoard(ctx, boards, NewServiceCard{
		CardID:      preCardID,
		Title:       cardTitle,
		Description: desc,
		SenderEmail: parsed.From,
		MessageID:   parsed.MessageID,
		IsSpam:      isSpam,
		Attachments: anhaenge,
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: create service-card: %w", err)
	}
	action := "service-card"
	if isSpam {
		action = "service-spam-card"
	}
	// #305/ADR-0107: Service-Board-Notification (service.new-card).
	// Nur für nicht-spam-Karten — Spam-Routing soll keine Push-Mail erzeugen.
	if !isSpam && opts.OnServiceCardCreated != nil {
		opts.OnServiceCardCreated(storage.ServiceBoardID, cardID, cardTitle, parsed.From, displayID)
	}
	// Auto-Reply senden — Fehler nur loggen, Ingest-Flow darf nicht abbrechen.
	if opts.AutoReply != nil {
		boardName := opts.BoardName
		if boardName == "" {
			boardName = "aBoard"
		}
		if err := SendAutoReply(ctx, *opts.AutoReply, parsed, cardID, displayID, cardTitle, boardName, true); err != nil {
			// SendAutoReply loggt intern, gibt hier nie einen harten Fehler zurück.
			_ = err
		}
	}
	return &IngestResult{
		Action:     action,
		BoardID:    storage.ServiceBoardID,
		CardID:     cardID,
		DisplayID:  displayID,
		DropReason: spamReason, // bei service-spam-card im DropReason die Begründung
	}, nil
}

// runAttachmentPipeline wendet die drei Standard-Filter in fester Reihenfolge
// auf parsed.Attachments an. Dropped-Anhänge werden NICHT verworfen sondern
// mit Data=nil + Filename im Set behalten, damit composeCommentBody/Description
// einen Hinweis-Block erzeugen kann.
func runAttachmentPipeline(parsed *ParsedMail) {
	if parsed == nil || len(parsed.Attachments) == 0 {
		return
	}
	// 1) MIME-Blocklist
	kept, droppedBlocked := FilterBlockedMIME(parsed.Attachments)
	// 2) Inline-Tracking
	kept, droppedTracking := FilterInlineTracking(kept, parsed.Body)
	// 3) Dedup (nur auf Data-bearing; verworfene Data=nil bleiben sichtbar)
	kept, _ = DedupAttachments(kept)

	// dropped-Anhänge mit Hinweis-Metadaten anhängen — composeCommentBody
	// liest nur die mit Data=nil + Filename≠"".
	parsed.Attachments = append(kept, droppedBlocked...)
	parsed.Attachments = append(parsed.Attachments, droppedTracking...)
}

// composeDescriptionBody baut den Karten-Description aus Mail-Body. Analog
// zu composeCommentBody, aber für die NEUE Karte (No-Match-Pfad): keine
// Body-Kind-Vorzeichen-Linie, dafür die Anhangs-Hinweise.
func composeDescriptionBody(p *ParsedMail) string {
	var sb strings.Builder
	if p.BodyKind == "html-converted" {
		sb.WriteString("_[Body aus HTML konvertiert]_\n\n")
	}
	sb.WriteString(p.Body)
	dropped := droppedAttachments(p.Attachments)
	if len(dropped) > 0 {
		sb.WriteString("\n\n---\n")
		sb.WriteString("_Verworfene Anhänge:_\n")
		for _, a := range dropped {
			fmt.Fprintf(&sb, "- `%s` (%s)\n", a.Filename, a.ContentType)
		}
	}
	return sb.String()
}

// loopHeaderSkip prüft RFC 3834 Auto-Submitted + RFC 2076 Precedence +
// custom X-Loop-Header. ADR-0093 §7.
func loopHeaderSkip(p *ParsedMail) (string, bool) {
	if v := firstHeader(p.Headers, "auto-submitted"); v != "" {
		lv := strings.ToLower(strings.TrimSpace(v))
		if lv != "no" && lv != "" {
			return "Auto-Submitted: " + v, true
		}
	}
	if v := firstHeader(p.Headers, "precedence"); v != "" {
		lv := strings.ToLower(strings.TrimSpace(v))
		if lv == "bulk" || lv == "list" || lv == "junk" {
			return "Precedence: " + v, true
		}
	}
	if v := firstHeader(p.Headers, "x-loop"); v != "" {
		return "X-Loop: " + v, true
	}
	return "", false
}

// composeCommentBody baut den Kommentar-Text aus dem Mail-Body. Bei
// HTML-konvertiertem Body wird ein Hinweis vorangestellt; bei verworfenen
// Anhängen ein Block am Ende. Plain-Body bleibt unverändert.
func composeCommentBody(p *ParsedMail) string {
	var sb strings.Builder
	if p.BodyKind == "html-converted" {
		sb.WriteString("_[Body aus HTML konvertiert]_\n\n")
	}
	sb.WriteString(p.Body)
	dropped := droppedAttachments(p.Attachments)
	if len(dropped) > 0 {
		sb.WriteString("\n\n---\n")
		sb.WriteString("_Verworfene Anhänge:_\n")
		for _, a := range dropped {
			fmt.Fprintf(&sb, "- `%s` (%s, zu groß)\n", a.Filename, a.ContentType)
		}
	}
	return sb.String()
}

// droppedAttachments liefert die Anhänge mit Data=nil (zu groß für Limit).
func droppedAttachments(atts []ParsedAttachment) []ParsedAttachment {
	var out []ParsedAttachment
	for _, a := range atts {
		if a.Data == nil && a.Filename != "" {
			out = append(out, a)
		}
	}
	return out
}

// persistKeptAttachments legt alle Data-bearing Anhänge unter
// <boardId>/<cardId>/<attId>__<filename> ab und liefert die zugehörigen
// Attachment-IDs zurück. Wenn saver==nil oder kein Anhang Daten trägt,
// liefert die Funktion (nil, nil) — Karten-/Kommentar-Pfad bleibt unverändert.
// Bei Storage-Fehler bricht der ganze Ingest-Vorgang ab (kein Partial-Save,
// damit eine fehlgeschlagene Persistierung nicht halb-stille Karten erzeugt).
//
// F-358 (Sprint-41-Kritiker): vorher gingen die `parsed.Attachments`-Daten
// am Ende der Funktion verloren (GC) — Karten hatten nur den Body.
//
// F-855: Rückgabe sind seit Sprint 69 vollständige Einträge (id, Name, Typ,
// Grösse) statt reiner ids — dieselbe Form, die das Frontend beim Upload
// schreibt. Vorher standen Mail-Anhänge als id-Strings in `card.attachments`
// und konnten weder an ein Modell gehen noch im Anhang-Tab richtig
// erscheinen (beide Leser erwarten `{id, filename, mime, size}`).
func persistKeptAttachments(saver AttachmentSaver, boardID, cardID string, atts []ParsedAttachment) ([]IngestAttachment, error) {
	if saver == nil || len(atts) == 0 {
		return nil, nil
	}
	var out []IngestAttachment
	for _, a := range atts {
		if a.Data == nil || a.Filename == "" {
			continue // dropped/Hinweis-Eintrag — überspringen
		}
		attID := uuid.NewString()
		safeName := sanitizeAttachmentFilename(a.Filename)
		if err := saver.Save(boardID, cardID, attID, safeName, bytes.NewReader(a.Data)); err != nil {
			return nil, fmt.Errorf("attachment %q: %w", a.Filename, err)
		}
		out = append(out, IngestAttachment{
			ID:       attID,
			Filename: safeName,
			MIME:     ingestMIME(a.ContentType, a.Filename, a.Data),
			Size:     int64(len(a.Data)),
		})
	}
	return out, nil
}

// ingestMIME bestimmt den MIME-Typ, der für einen Mail-Anhang in die Karte
// geschrieben wird (F-855, F-1093).
//
// Der Content-Type-Header einer Mail ist eine Behauptung des Absenders — und
// ein Bild-Typ an der Karte entscheidet darüber, ob die Datei an ein fremdes
// Modell gehen darf (llm.AllowedImageMIME) und ob das Frontend sie als
// <img> anzeigt (Vorschau, Cover: dort genügt `image/*`). Deshalb:
//
//	SVG (Header, Dateiname oder Inhalt) → application/octet-stream (F-1093)
//	Inhalt ist erkennbar ein Bild       → der erkannte Typ (Inhalt schlägt Header)
//	Header behauptet IRGENDEIN image/*,
//	der Inhalt bestätigt es nicht        → der erkannte Typ (F-1093; vorher nur
//	                                       bei den Modell-Bildtypen)
//	sonst                                → der Header (ohne Parameter), bzw. der
//	                                       erkannte Typ, wenn der Header fehlt
//
// Nicht-Bild-Typen aus dem Header (PDF, Office …) bleiben erhalten: Das
// Sniffing erkennt sie oft nur als application/zip oder octet-stream, und für
// sie hängt keine Freigabe am Typ.
//
// Warum SVG nicht verworfen, sondern neutral abgelegt wird: Der Upload-Pfad
// lehnt SVG mit 415 ab (F-128, `server/api/attachments.go`) — dort sitzt ein
// Mensch, der die Ablehnung sieht. Beim Mail-Ingest gibt es niemanden, der
// sie sähe; ein stilles Verwerfen verlöre einen Anhang, den der Absender
// geschickt hat. Die Gefahr einer SVG liegt allein im Rendern als Bild, und
// das hängt im Frontend am `image/*`-Typ (modal.js, renderer.js schliessen
// nur den exakten String `image/svg+xml` aus — `image/svg` rutschte durch).
// Mit application/octet-stream gibt es weder Vorschau noch Cover noch
// Modell-Versand, nur den Download, und der liefert den am Inhalt erkannten
// Typ (text/xml bzw. text/plain) mit `Content-Disposition: attachment` aus.
func ingestMIME(behauptet, dateiname string, data []byte) string {
	erkannt := http.DetectContentType(data)
	if i := strings.IndexByte(erkannt, ';'); i >= 0 {
		erkannt = strings.TrimSpace(erkannt[:i])
	}
	header := ""
	if mt, _, err := mime.ParseMediaType(behauptet); err == nil {
		header = strings.ToLower(mt)
	}
	if istSVGAnhang(header, dateiname, erkannt, data) {
		return "application/octet-stream"
	}
	if strings.HasPrefix(erkannt, "image/") {
		return erkannt
	}
	if header == "" || strings.HasPrefix(header, "image/") {
		return erkannt
	}
	return header
}

// istSVGAnhang — SVG-Erkennung für den Mail-Ingest, sinngemäss wie der
// Upload (`isSVGMime`/`isSVGFilename`/`isSVGBytes` in server/api/
// attachments.go; das Paket api lässt sich von hier nicht importieren).
// Der Inhalt wird etwas breiter geprüft als beim Upload: Nicht nur ein
// führendes `<svg` bzw. `<?xml … <svg`, sondern jedes `<svg` im Kopf einer
// Textdatei — auch hinter einem Kommentar oder DOCTYPE. Binärinhalte
// (erkannter Typ nicht text/*) werden nicht durchsucht, damit ein Bild, in
// dessen Bytes zufällig `<svg` steht, sein image/* behält.
func istSVGAnhang(header, dateiname, erkannt string, data []byte) bool {
	if strings.Contains(header, "svg") || strings.Contains(erkannt, "svg") {
		return true
	}
	name := strings.ToLower(dateiname)
	if strings.HasSuffix(name, ".svg") || strings.HasSuffix(name, ".svgz") {
		return true
	}
	if !strings.HasPrefix(erkannt, "text/") {
		return false
	}
	kopf := data
	if len(kopf) > 512 {
		kopf = kopf[:512]
	}
	return strings.Contains(strings.ToLower(string(kopf)), "<svg")
}

// sanitizeAttachmentFilename entfernt Pfadtrenner und Steuerzeichen aus
// Mail-Anhang-Dateinamen — damit der Storage-Pfad nicht aus dem Card-Verzeichnis
// heraus geschrieben werden kann (Defense-in-depth gegen pathologische Mails).
// Spiegelt den Filename-Sanitisierungs-Pfad aus `attachmentSafeFilename` in
// `server/api/attachments.go` so weit es die Mail-Domäne braucht.
func sanitizeAttachmentFilename(name string) string {
	if name == "" {
		return "attachment"
	}
	// nur Basename behalten (Pfadtrenner aus Filename entfernen)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	// Null-Byte + Steuerzeichen + Doppelpunkte/Sternchen entfernen
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "attachment"
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// DeriveCommentIDFromMessageID erzeugt eine deterministische Kommentar-UUID
// aus der Mail-Message-ID. Replays derselben Mail (z. B. nach Restore) ergeben
// dieselbe Comment-ID → AddCommentToCard ist idempotent.
//
// UUIDv5 mit fester Namespace-UUID, damit bit-stabil über Worker-Restarts.
func DeriveCommentIDFromMessageID(messageID string) string {
	if messageID == "" {
		return "" // Caller bekommt frische UUID — kein Idempotenz-Schutz möglich
	}
	return uuidV5("", messageID)
}

// commentNamespace ist eine fixe UUID (random einmal generiert), die als
// Namespace für die deterministische Comment-ID-Ableitung dient. Bit-stabil
// über Worker-Restarts und Server-Migrationen — ändern wäre eine
// Idempotenz-Bruch-Änderung.
var commentNamespace = uuid.MustParse("8a3b4c5d-6e7f-4a01-9b02-c3d4e5f60718")

// uuidV5 deriviert eine UUIDv5 (SHA-1) aus Namespace + Name. Wrapper um
// google/uuid.NewSHA1 für lesbarere Aufrufer.
func uuidV5(_, name string) string {
	return uuid.NewSHA1(commentNamespace, []byte(name)).String()
}
