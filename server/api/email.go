package api

// email.go — Karte als HTML-Mail versenden (#293, ADR-0091).
//
// POST /boards/{b}/cards/{c}/email
// ACL: über ACLGuard (boards-Pfad) → editor+ am Board erforderlich
// (POST auf Sub-Resource = editor+, Sprint-40-Kritiker [D]P2 — Karten-
// Definition + ADR-0091 §4 harmonisiert).
// Rate-Limit: 10 Mails pro Minute pro User (Anti-Spam).
// Markdown-Rendering serverseitig via server/mail.RenderHTML (strikt ohne
// raw HTML, siehe ADR-0091 §2).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"strings"
	"time"

	mailpkg "github.com/Andiministrator/aBoard/server/mail"
	"github.com/Andiministrator/aBoard/server/storage"
)

// EmailAPI sendet Karten als Mail. configRepo wird zur Run-Time-Auflösung
// der SMTP-Konfig benutzt (User-änderbar via #306-Admin).
type EmailAPI struct {
	boards  *storage.BoardRepo
	users   *storage.UserRepo
	cfg     *storage.ConfigRepo
	limiter *rateLimiter
}

func NewEmailAPI(boards *storage.BoardRepo, users *storage.UserRepo, cfg *storage.ConfigRepo) *EmailAPI {
	return &EmailAPI{
		boards:  boards,
		users:   users,
		cfg:     cfg,
		limiter: newRateLimiter(10, time.Minute),
	}
}

// Register hängt die Route an. Liegt unter /boards/* — ACLGuard erzwingt
// bereits editor+ am Board (POST auf eine Unterressource, roleAllows in
// acl.go; bis Sprint 82 stand hier fälschlich „Betrachter genügt“, F-1530),
// hier ist nur noch Empfänger-Validation + Rate-Limit + Send.
func (a *EmailAPI) Register(mux *http.ServeMux) {
	mux.Handle("POST /boards/{boardId}/cards/{cardId}/email",
		WithDefaults(http.HandlerFunc(a.handleSend)))
}

type emailRequest struct {
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	Subject string   `json:"subject"`
	Message string   `json:"message"`
}

func (a *EmailAPI) handleSend(w http.ResponseWriter, r *http.Request) {
	id := IdentityFromContext(r.Context())
	if id == nil {
		writeError(w, http.StatusForbidden, "forbidden: email requires authentication")
		return
	}
	email := storage.NormalizeEmail(id.Email)
	if email == "" {
		writeError(w, http.StatusForbidden, "forbidden: no identity email")
		return
	}
	u, err := a.users.GetByEmail(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
		return
	}
	if !a.limiter.allow(u.ID) {
		writeError(w, http.StatusTooManyRequests, "rate limit: 10 mails per minute per user")
		return
	}

	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	if boardID == "" || cardID == "" {
		writeError(w, http.StatusBadRequest, "board/card id required")
		return
	}

	// Sprint-40-Kritiker-Fix [S]P3: Body-Size-Limit (Defense-in-depth gegen
	// authentifizierten-User-DoS via riesige JSON-Bodies).
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	var req emailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// Sprint-40-Kritiker-Fix [S]P2: Display-Name aus Recipient extrahieren,
	// roher Input geht nie an SMTP-RCPT-TO (Mail-Header-Injection-Schutz).
	toClean, err := normalizeRecipients(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ccClean, err := normalizeRecipients(req.CC)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRecipientCount(toClean, ccClean); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Subject darf kein CRLF enthalten (Header-Injection).
	if strings.ContainsAny(req.Subject, "\r\n") {
		writeError(w, http.StatusBadRequest, "subject contains line breaks")
		return
	}

	// Karte aus dem Board-State holen.
	card, board, err := loadCardFromBoard(r.Context(), a.boards, boardID, cardID)
	if err != nil {
		if errors.Is(err, errCardNotFound) {
			writeError(w, http.StatusNotFound, "card not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "load card failed", err)
		return
	}
	// Sprint-40-Kritiker-Fix [V]P3: Mail-Body-Size-Limit. 256 KB ist groß
	// genug für realistische Karten-Description (Markdown ist kompakt), klein
	// genug um Mail-Provider-Bounces / Reputations-Schäden zu verhindern.
	const maxBodyBytes = 256 * 1024
	if len(card.Description) > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("card description too large for mail (%d > %d bytes)", len(card.Description), maxBodyBytes))
		return
	}

	cfg, _ := mailpkg.LoadConfig(r.Context(), a.cfg)
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "smtp not configured")
		return
	}

	// Subject: vorgegeben oder vorausgefüllt.
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		subject = fmt.Sprintf("[aBoard] %s: %s", labelForCard(card), board.Title)
	}

	bodyText, bodyHTML := buildBodies(card, board, u.Email, req.Message)

	// F-718: SendMailContext statt SendMail — dieser Handler ist der Pfad,
	// in dem ein verstummter SMTP-Server einen HTTP-Request unbegrenzt
	// festhielt. Mit dem Request-Context bricht der Sendeversuch ab, sobald
	// der Client aufgibt oder der Server herunterfährt; die Idle-Deadline aus
	// cfg.Timeout greift zusätzlich.
	if err := mailpkg.SendMailContext(r.Context(), cfg, mailpkg.Message{
		To: toClean, CC: ccClean, Subject: subject,
		BodyText: bodyText, BodyHTML: bodyHTML,
	}); err != nil {
		if errors.Is(err, mailpkg.ErrSMTPNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "smtp not configured")
			return
		}
		if errors.Is(err, mailpkg.ErrAuthFailed) {
			writeError(w, http.StatusBadGateway, "smtp auth failed", err)
			return
		}
		writeError(w, http.StatusBadGateway, "smtp delivery failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// normalizeRecipients parst jede Adresse via mail.ParseAddress und gibt die
// reine `addr-spec`-Form zurück (kein Display-Name) — damit SMTP RCPT TO
// nur gültige Adressen sieht (Sprint-40-Kritiker-Fix [S]P2). Verweigert
// CRLF in der Eingabe als zusätzliche Defense-in-depth gegen Header-
// Injection (ParseAddress schluckt das eigentlich schon).
func normalizeRecipients(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		if strings.ContainsAny(raw, "\r\n") {
			return nil, fmt.Errorf("invalid email %q: contains line breaks", raw)
		}
		parsed, err := mail.ParseAddress(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid email %q: %v", raw, err)
		}
		out = append(out, parsed.Address)
	}
	return out, nil
}

func validateRecipientCount(to, cc []string) error {
	if len(to) == 0 {
		return errors.New("at least one recipient (to) required")
	}
	total := len(to) + len(cc)
	if total > 10 {
		return fmt.Errorf("too many recipients (%d, max 10)", total)
	}
	return nil
}

// errCardNotFound ist der Sentinel für loadCardFromBoard.
var errCardNotFound = errors.New("card not found")

type cardMeta struct {
	DisplayID   any
	Title       string
	Description string
	Owner       string
	UpdatedAt   string
	Comments    []map[string]any
}

// loadCardFromBoard parst board.state_json und extrahiert die gewünschte
// Karte. Bewusste Ausnahme vom „state_json ist opaque"-Prinzip (ADR-0044)
// — für den Mail-Versand brauchen wir Karten-Inhalt server-side.
func loadCardFromBoard(ctx context.Context, boards *storage.BoardRepo, boardID, cardID string) (*cardMeta, *storage.Board, error) {
	b, err := boards.Get(ctx, boardID)
	if err != nil {
		return nil, nil, fmt.Errorf("get board: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b.StateJSON, &root); err != nil {
		return nil, nil, fmt.Errorf("parse state_json: %w", err)
	}
	boardObj, _ := root["board"].(map[string]any)
	sprints, _ := boardObj["sprints"].([]any)
	for _, sp := range sprints {
		spMap, _ := sp.(map[string]any)
		cards, _ := spMap["cards"].([]any)
		for _, c := range cards {
			cm, _ := c.(map[string]any)
			if id, _ := cm["id"].(string); id == cardID {
				return extractCardMeta(cm), b, nil
			}
		}
	}
	return nil, nil, errCardNotFound
}

func extractCardMeta(c map[string]any) *cardMeta {
	m := &cardMeta{DisplayID: c["displayId"]}
	if v, ok := c["title"].(string); ok {
		m.Title = v
	}
	if v, ok := c["description"].(string); ok {
		m.Description = v
	}
	if v, ok := c["owner"].(string); ok {
		m.Owner = v
	}
	if v, ok := c["updated"].(string); ok {
		m.UpdatedAt = v
	}
	if cs, ok := c["comments"].([]any); ok {
		for _, x := range cs {
			if mm, ok := x.(map[string]any); ok {
				m.Comments = append(m.Comments, mm)
			}
		}
	}
	return m
}

func labelForCard(c *cardMeta) string {
	if c.DisplayID != nil {
		return fmt.Sprintf("Karte #%v: %s", c.DisplayID, c.Title)
	}
	return c.Title
}

// buildBodies konstruiert Plain-Text- und HTML-Bodies. HTML rendert via
// mailpkg.RenderHTML (kein raw HTML, strikte Allow-Liste).
func buildBodies(card *cardMeta, board *storage.Board, senderEmail, note string) (text, htmlBody string) {
	var t strings.Builder
	if note != "" {
		t.WriteString(note)
		t.WriteString("\n\n---\n\n")
	}
	t.WriteString(fmt.Sprintf("Karte #%v: %s\nBoard: %s\n", card.DisplayID, card.Title, board.Title))
	if card.Owner != "" {
		t.WriteString("Owner: " + card.Owner + "\n")
	}
	t.WriteString("Gesendet von: " + senderEmail + "\n\n")
	t.WriteString(card.Description)

	header := fmt.Sprintf(
		`<h1>Karte #%s: %s</h1><dl><dt>Board</dt><dd>%s</dd><dt>Owner</dt><dd>%s</dd><dt>Gesendet von</dt><dd>%s</dd></dl>`,
		html.EscapeString(fmt.Sprintf("%v", card.DisplayID)),
		html.EscapeString(card.Title),
		html.EscapeString(board.Title),
		html.EscapeString(card.Owner),
		html.EscapeString(senderEmail),
	)
	noteBlock := ""
	if note != "" {
		noteBlock = "<blockquote>" + html.EscapeString(note) + "</blockquote>"
	}
	body := mailpkg.BuildHTMLBody(card.Title, header+noteBlock, card.Description)
	return t.String(), body
}
