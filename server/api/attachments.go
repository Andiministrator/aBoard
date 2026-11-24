package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// defaultMaxAttachmentBytes ist das Datei-Limit wenn ABOARD_ATTACHMENT_MAX_BYTES nicht gesetzt.
const defaultMaxAttachmentBytes = 50 * 1024 * 1024 // 50 MB

// AttachmentsAPI bündelt die Handler für Attachment-Upload/Download/Delete.
type AttachmentsAPI struct {
	store   *storage.AttachmentStore
	maxSize int64
	// F-235 (Sprint 28): Optional. Wenn gesetzt, wird nach jeder
	// erfolgreichen Attachment-Mutation board:updated broadcastet, damit
	// SSE-Clients Cover-/Background-/Avatar-Wechsel mitbekommen.
	broker *EventBroker
	// KF-3: Schutz der Dateien an fremden Kommentaren (optional).
	dateiRecht KommentarDateiRecht
	// F-2242: Upload-Sperre für Spiegel und wartende Karten (optional).
	spiegelRecht SpiegelUploadRecht
	// F-3019/F-3020 (#1143): Kartenlage für den nachgeholten Purge
	// (`?nurVerwaist=1`). nil → ein solcher Purge wird abgelehnt (409).
	kartenLage KartenLage
}

// WithKartenLage hängt die Prüfung an, ob eine Karte im gespeicherten Stand
// steht (nachgeholter Purge, F-3019/F-3020).
func (a *AttachmentsAPI) WithKartenLage(l KartenLage) *AttachmentsAPI {
	a.kartenLage = l
	return a
}

// WithSpiegelUploadRecht hängt die Upload-Sperre für Kartenverzeichnisse von
// Spiegeln und wartenden Karten an (F-2242, ADR-0141 §7).
func (a *AttachmentsAPI) WithSpiegelUploadRecht(r SpiegelUploadRecht) *AttachmentsAPI {
	a.spiegelRecht = r
	return a
}

// WithKommentarDateiRecht hängt die Prüfung an, welche Dateien einer Karte an
// fremden Kommentaren hängen (KF-3, ADR-0135/0136).
func (a *AttachmentsAPI) WithKommentarDateiRecht(r KommentarDateiRecht) *AttachmentsAPI {
	a.dateiRecht = r
	return a
}

// NewAttachmentsAPI erstellt eine neue AttachmentsAPI. maxSize ≤ 0 → 50 MB Default.
func NewAttachmentsAPI(store *storage.AttachmentStore, maxSize int64) *AttachmentsAPI {
	if maxSize <= 0 {
		maxSize = defaultMaxAttachmentBytes
	}
	return &AttachmentsAPI{store: store, maxSize: maxSize}
}

// WithBroker hängt einen EventBroker an die AttachmentsAPI (F-235). Optional.
func (a *AttachmentsAPI) WithBroker(broker *EventBroker) *AttachmentsAPI {
	a.broker = broker
	return a
}

// boardAttachmentCardID — Ablage-Bezeichner für board-weite Anhänge
// (Hintergrundbild, Avatare) im AttachmentStore (Karte #443, F-739).
//
// Board-Anhänge gehören zu keiner Karte, brauchen aber denselben Store. Statt
// eines zweiten Ablage-Mechanismus bekommen sie einen reservierten Bezeichner
// an der Stelle, an der sonst die Karten-id steht. Der Wert ist bewusst keine
// UUID-Form: Er kann mit keiner echten Karten-id kollidieren.
//
// Damit die Reservierung mehr ist als eine Absichtserklärung, lehnen die
// KARTEN-Routen ihn ab (siehe cardIDAusPfad). Sonst führten zwei Routen auf
// dieselbe Ablage, und die Trennung wäre nur Konvention.
//
// Nachtrag (Server-Kritiker Sprint 61): Die Ablehnung an DIESEN Routen reicht
// nicht — `handleTransfer` in transfer.go nimmt die Karten-id ebenfalls aus
// dem Pfad und reicht sie an den Store weiter. Der Riegel, auf den man sich
// verlassen kann, sitzt deshalb in `storage.AttachmentStore` selbst
// (`DeleteCard`, `StageCardAttachments`); hier steht nur noch die frühe,
// sprechende 400er-Antwort. Der Wert wird vom Store bezogen, damit es nicht
// zwei Wahrheiten gibt.
const boardAttachmentCardID = storage.BoardAblageId

// Register hängt die Attachment-Routen an einen ServeMux.
//
//	POST   /boards/{boardId}/attachments/{cardId}           — Upload
//	GET    /boards/{boardId}/attachments/{cardId}/{attId}   — Download
//	DELETE /boards/{boardId}/attachments/{cardId}/{attId}   — Einzeln löschen
//	DELETE /boards/{boardId}/attachments/{cardId}           — Karte purge
//
// Board-weite Anhänge (Karte #443) — dieselbe Ablage, ohne Karten-id:
//
//	POST   /boards/{boardId}/board-attachments              — Upload
//	GET    /boards/{boardId}/board-attachments/{attId}      — Download
//	DELETE /boards/{boardId}/board-attachments/{attId}      — Einzeln löschen
//
// Ein „purge board" gibt es bewusst nicht: Das Löschen aller Anhänge eines
// Boards passiert beim Board-Löschen über AttachmentStore.DeleteBoard und ist
// keine Client-Operation.
//
// Autorisierung: Alle Routen liegen unter /boards/{id}/… und erben damit den
// ACLGuard (GET → viewer+, mutierend → editor+). Kein eigener Check nötig —
// aber auch keiner vergessen: Ein Endpunkt ausserhalb dieses Präfixes hätte
// gar keine Prüfung.
//
// Upload-Route: kein WithDefaults — eigenes MaxBytesReader > MaxBodyBytes (#74).
func (a *AttachmentsAPI) Register(mux *http.ServeMux) {
	mux.Handle("POST /boards/{boardId}/attachments/{cardId}",
		WithSecurityHeaders(http.HandlerFunc(a.upload)))
	mux.Handle("GET /boards/{boardId}/attachments/{cardId}/{attId}",
		WithDefaults(http.HandlerFunc(a.download)))
	mux.Handle("DELETE /boards/{boardId}/attachments/{cardId}/{attId}",
		WithDefaults(http.HandlerFunc(a.deleteOne)))
	mux.Handle("DELETE /boards/{boardId}/attachments/{cardId}",
		WithDefaults(http.HandlerFunc(a.deleteCard)))

	mux.Handle("POST /boards/{boardId}/board-attachments",
		WithSecurityHeaders(http.HandlerFunc(a.uploadBoard)))
	mux.Handle("GET /boards/{boardId}/board-attachments/{attId}",
		WithDefaults(http.HandlerFunc(a.downloadBoard)))
	mux.Handle("DELETE /boards/{boardId}/board-attachments/{attId}",
		WithDefaults(http.HandlerFunc(a.deleteOneBoard)))
}

// cardIDAusPfad liest die Karten-id einer Karten-Route und weist den
// reservierten Board-Bezeichner zurück. Rückgabe ("", false) → der Aufrufer
// hat bereits geantwortet.
func cardIDAusPfad(w http.ResponseWriter, r *http.Request) (string, bool) {
	cardId := r.PathValue("cardId")
	if cardId == boardAttachmentCardID {
		writeError(w, http.StatusBadRequest, "reserved cardId")
		return "", false
	}
	if !validAttachmentID(cardId) {
		writeError(w, http.StatusBadRequest, "invalid cardId")
		return "", false
	}
	return cardId, true
}

// attachmentResponse ist die JSON-Antwort nach einem Upload — spiegelt die
// Frontend-Attachment-Objektstruktur (id, filename, mime, size, created).
type attachmentResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
	Created  string `json:"created"`
}

func (a *AttachmentsAPI) upload(w http.ResponseWriter, r *http.Request) {
	cardId, ok := cardIDAusPfad(w, r)
	if !ok {
		return
	}
	// F-2242: Auf einer Kopie mit Rückweg trägt die Journal-Form keine
	// Anhänge; eine Datei an einem Spiegel oder einer wartenden Karte bliebe
	// verwaist liegen. Wie die Kommentar-Schranke: 422, vor jedem Schreiben.
	if a.spiegelRecht != nil {
		// Fail-closed (Karte #863, B-9): eine ungültige boardId wird HIER abgelehnt, nicht erst in uploadNach —
		// sonst hinge die Sperre an der Reihenfolge zweier Prüfungen, und ein späteres Umordnen ließe die
		// Spiegel-Prüfung für genau diese IDs still aus.
		boardId := r.PathValue("boardId")
		if !validAttachmentID(boardId) {
			writeError(w, http.StatusBadRequest, "invalid boardId")
			return
		}
		gesperrt, err := a.spiegelRecht.UploadGesperrt(r.Context(), boardId, cardId)
		if err != nil {
			// 503, nicht 409: 409 heißt an diesem Endpunkt „attachmentId already exists“; ein Lesefehler ist
			// wiederholbar, wie beim Ops-Weg (F-2405).
			writeError(w, http.StatusServiceUnavailable, "mirror check failed, retry", err)
			return
		}
		if gesperrt {
			writeError(w, http.StatusUnprocessableEntity,
				"mirror or waiting card on the external copy — no attachments (read-only or not carried back, ADR-0141 §7)")
			return
		}
	}
	a.uploadNach(w, r, cardId)
}

// uploadBoard — Upload eines board-weiten Anhangs (Karte #443). Bis auf die
// Ablage identisch zum Karten-Upload; insbesondere gelten dasselbe
// Grössenlimit, dieselbe SVG-Sperre und dieselbe MIME-Erkennung. Das ist der
// Punkt der gemeinsamen Kernfunktion: Zwei Kopien wären zwei Stellen, an denen
// eine Prüfung später fehlen kann.
func (a *AttachmentsAPI) uploadBoard(w http.ResponseWriter, r *http.Request) {
	a.uploadNach(w, r, boardAttachmentCardID)
}

func (a *AttachmentsAPI) uploadNach(w http.ResponseWriter, r *http.Request, cardId string) {
	boardId := r.PathValue("boardId")
	if !validAttachmentID(boardId) {
		writeError(w, http.StatusBadRequest, "invalid boardId")
		return
	}

	// Attachment-eigenes Größenlimit — höher als MaxBodyBytes der JSON-API.
	r.Body = http.MaxBytesReader(w, r.Body, a.maxSize+10*1024)
	if err := r.ParseMultipartForm(4 * 1024 * 1024); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("file exceeds %d bytes", a.maxSize))
		} else {
			writeError(w, http.StatusBadRequest, "multipart parse error")
		}
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "field 'file' missing")
		return
	}
	defer file.Close()

	attId := strings.TrimSpace(r.FormValue("attachmentId"))
	if !validAttachmentID(attId) {
		writeError(w, http.StatusBadRequest, "field 'attachmentId' missing or invalid")
		return
	}

	// MIME sniffing + SVG-Sperrung (F-128-Pattern: SVG erlaubt script/foreignObject)
	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	detectedMime := http.DetectContentType(buf[:n])
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "seek failed", err)
		return
	}
	// SVG-Erkennung: MIME-Typ, Dateiname-Endung oder XML-Inhalt mit SVG-Signatur
	if isSVGMime(detectedMime) || isSVGMime(header.Header.Get("Content-Type")) ||
		isSVGFilename(header.Filename) || isSVGBytes(buf[:n]) {
		writeError(w, http.StatusUnsupportedMediaType, "svg not allowed")
		return
	}

	safeName := attachmentSafeFilename(header.Filename)
	if err := a.store.Save(boardId, cardId, attId, safeName, file); err != nil {
		if errors.Is(err, storage.ErrAttachmentExists) {
			writeError(w, http.StatusConflict, "attachmentId already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "save failed", err)
		return
	}

	writeJSON(w, http.StatusCreated, attachmentResponse{
		ID:       attId,
		Filename: safeName, /* sanitisierter Name = was auf Disk liegt */
		Mime:     sanitizeMime(detectedMime),
		Size:     header.Size,
		Created:  time.Now().UTC().Format("2006-01-02"),
	})
	// F-235: SSE-Subscriber benachrichtigen (Cover-Bild, Background, Avatare).
	publishBoardUpdated(a.broker, boardId, nowMillisFunc(), "attachment-uploaded")
}

func (a *AttachmentsAPI) download(w http.ResponseWriter, r *http.Request) {
	cardId, ok := cardIDAusPfad(w, r)
	if !ok {
		return
	}
	a.downloadAus(w, r, cardId)
}

// downloadBoard — Download eines board-weiten Anhangs (Karte #443).
func (a *AttachmentsAPI) downloadBoard(w http.ResponseWriter, r *http.Request) {
	a.downloadAus(w, r, boardAttachmentCardID)
}

func (a *AttachmentsAPI) downloadAus(w http.ResponseWriter, r *http.Request, cardId string) {
	boardId := r.PathValue("boardId")
	attId := r.PathValue("attId")
	if !validAttachmentID(boardId) || !validAttachmentID(attId) {
		writeError(w, http.StatusBadRequest, "invalid path segment")
		return
	}

	rc, filename, err := a.store.Open(boardId, cardId, attId)
	if errors.Is(err, storage.ErrAttachmentNotFound) {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "open failed", err)
		return
	}
	defer rc.Close()

	// MIME aus ersten 512 Bytes erkennen, dann zurückspulen
	buf := make([]byte, 512)
	n, _ := rc.Read(buf)
	contentType := http.DetectContentType(buf[:n])
	if _, err := rc.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "seek failed", err)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	// Security-Header kommen von WithDefaults — hier nur die Download-spezifischen
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (a *AttachmentsAPI) deleteOne(w http.ResponseWriter, r *http.Request) {
	cardId, ok := cardIDAusPfad(w, r)
	if !ok {
		return
	}
	a.deleteOneAus(w, r, cardId)
}

// deleteOneBoard — Löschen eines board-weiten Anhangs (Karte #443).
func (a *AttachmentsAPI) deleteOneBoard(w http.ResponseWriter, r *http.Request) {
	a.deleteOneAus(w, r, boardAttachmentCardID)
}

func (a *AttachmentsAPI) deleteOneAus(w http.ResponseWriter, r *http.Request, cardId string) {
	boardId := r.PathValue("boardId")
	attId := r.PathValue("attId")
	if !validAttachmentID(boardId) || !validAttachmentID(attId) {
		writeError(w, http.StatusBadRequest, "invalid path segment")
		return
	}
	// KF-3: Die Datei eines fremden Kommentars löschen nur sein Autor und
	// Owner/Admin — wie den Kommentar selbst (ADR-0136).
	if a.dateiRecht != nil && cardId != boardAttachmentCardID {
		geschuetzt, _, err := a.dateiRecht.GeschuetzteDateien(r.Context(), boardId, cardId)
		if err != nil {
			writeError(w, http.StatusConflict, "comment attachment check failed", err)
			return
		}
		if geschuetzt[attId] {
			writeError(w, http.StatusForbidden, "attachment belongs to a comment by someone else (ADR-0136)")
			return
		}
	}
	if err := a.store.Delete(boardId, cardId, attId); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	// F-235: SSE-Subscriber benachrichtigen.
	publishBoardUpdated(a.broker, boardId, nowMillisFunc(), "attachment-deleted")
}

func (a *AttachmentsAPI) deleteCard(w http.ResponseWriter, r *http.Request) {
	boardId := r.PathValue("boardId")
	cardId, ok := cardIDAusPfad(w, r)
	if !ok {
		return
	}
	if !validAttachmentID(boardId) {
		writeError(w, http.StatusBadRequest, "invalid path segment")
		return
	}
	// F-3019/F-3020 (#1143): Ein NACHGEHOLTER Purge (der Client wiederholt einen
	// gescheiterten) darf nur eine Karte treffen, die im gespeicherten Stand
	// fehlt. Ob das Löschen der Karte am Server angekommen ist, weiß nur der
	// Server; jeder Client-Stand ist ein Lesefenster (knowledge §56). Steht die
	// Karte noch da (Löschen nie angekommen, Karte wiederhergestellt) → 409,
	// der Client verwirft den Eintrag. Ohne Prüfmöglichkeit oder bei
	// unlesbarem Stand ebenso 409: Waisen-Dateien sind das kleinere Übel.
	// Der Sofort-Purge beim Löschen trägt den Parameter nicht — dort leert der
	// Browser die Ablage, BEVOR er die Karte aus dem Stand nimmt.
	if r.URL.Query().Get("nurVerwaist") == "1" {
		if a.kartenLage == nil {
			writeError(w, http.StatusConflict, "card check unavailable")
			return
		}
		besteht, err := a.kartenLage.KarteBesteht(r.Context(), boardId, cardId)
		switch {
		case errors.Is(err, storage.ErrBoardNotFound):
			writeError(w, http.StatusNotFound, "board not found")
			return
		case err != nil:
			writeError(w, http.StatusConflict, "card check failed", err)
			return
		case besteht:
			writeError(w, http.StatusConflict, "card still exists")
			return
		}
		// F-3030 (#1149): Zwischen dieser Prüfung und dem Löschen kann ein gleichzeitiges
		// Schreiben die Karte zurückholen. Deshalb: Verzeichnis beiseite schieben, NOCH
		// EINMAL prüfen, erst dann endgültig löschen — sonst zurückschieben. Fehlt die
		// Karte, gibt es keine KF-3-geschützten Dateien (die schützt nur die lebende
		// Karte, ADR-0136 Nachtrag 2026-11-11), also geht die ganze Ablage.
		var pruefFehler error
		ctx := r.Context()
		geloescht, err := a.store.LoescheKarteBestaetigt(boardId, cardId, func() (bool, error) {
			b, e := a.kartenLage.KarteBesteht(ctx, boardId, cardId)
			pruefFehler = e
			return !b, e
		}, AufraeumLage(ctx, a.kartenLage))
		switch {
		case pruefFehler != nil && errors.Is(pruefFehler, storage.ErrBoardNotFound):
			writeError(w, http.StatusNotFound, "board not found")
			return
		case pruefFehler != nil:
			writeError(w, http.StatusConflict, "card check failed", pruefFehler)
			return
		case err != nil:
			status, msg := attachmentFehlerAntwort(err, "delete failed")
			writeError(w, status, msg, err)
			return
		case !geloescht:
			writeError(w, http.StatusConflict, "card still exists")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		publishBoardUpdated(a.broker, boardId, nowMillisFunc(), "attachment-card-purged")
		return
	}
	// KF-3: Hängen an der (noch bestehenden) Karte Dateien fremder Kommentare,
	// werden nur die übrigen Dateien entfernt. Der Browser leert die Ablage,
	// BEVOR er die Karte aus dem Stand nimmt; eine Sperre hier verhinderte das
	// erlaubte Löschen der Karte (ADR-0136 §Grenzen). Die geschützten Dateien
	// bleiben dann liegen (KF-3, Grenze in ADR-0136 Nachtrag).
	if a.dateiRecht != nil {
		geschuetzt, alle, err := a.dateiRecht.GeschuetzteDateien(r.Context(), boardId, cardId)
		if err != nil {
			writeError(w, http.StatusConflict, "comment attachment check failed", err)
			return
		}
		if len(geschuetzt) > 0 {
			for _, aid := range alle {
				if geschuetzt[aid] || !validAttachmentID(aid) {
					continue
				}
				if err := a.store.Delete(boardId, cardId, aid); err != nil {
					writeError(w, http.StatusInternalServerError, "delete failed", err)
					return
				}
			}
			// F-3013 (#1141, ADR-0136 Nachtrag 2026-11-11): Der Rest bleibt, solange die
			// Karte lebt. 200 mit Anzahl statt 204 — der Browser merkt sich den Rest und
			// holt ihn nach dem Speichern mit `?nurVerwaist=1` nach (dann fehlt die Karte,
			// und alles geht). Ältere Browser werten jedes 2xx als erledigt.
			writeJSON(w, http.StatusOK, map[string]int{"geschuetztGeblieben": len(geschuetzt)})
			publishBoardUpdated(a.broker, boardId, nowMillisFunc(), "attachment-card-purged")
			return
		}
	}
	if err := a.store.DeleteCard(boardId, cardId); err != nil {
		status, msg := attachmentFehlerAntwort(err, "delete failed")
		writeError(w, status, msg, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	// F-235: SSE-Subscriber benachrichtigen.
	publishBoardUpdated(a.broker, boardId, nowMillisFunc(), "attachment-card-purged")
}

// ── Hilfsfunktionen ──────────────────────────────────────────────────────────

// attachmentFehlerAntwort ordnet einem Store-Fehler Statuscode und Meldung zu.
//
// F-878 (Teil 2): `DeleteCard` lehnt die reservierte Board-Ablage mit
// ErrReservierteAblage ab (Karte #443) — das ist eine abgelehnte
// CLIENT-Anfrage, kein Serverfehler. Bis Sprint 62 wurde daraus pauschal 500;
// der Anrufer las daraus „versuch es spaeter nochmal", wo „diese id ist keine
// Karte" gemeint war.
//
// Ueber die HTTP-Routen ist dieser Fall heute nicht erreichbar: `cardIDAusPfad`
// weist die Board-Ablage schon vor dem Store-Aufruf mit 400 ab, und
// `handleTransfer` tut dasselbe. Diese Funktion ist die zweite Schranke — sie
// traegt, wenn jemand einen Vorab-Check verschiebt oder einen weiteren
// Aufrufer anschliesst. Genau deshalb steht sie hier einzeln und nicht als
// `if` im Handler: eine nicht erreichbare Verzweigung im Handler koennte kein
// Test pruefen, ohne den Vorab-Check zu umgehen — und ein Test, der in
// Wahrheit den Vorab-Check misst, sichert diese Zuordnung nicht zu.
func attachmentFehlerAntwort(err error, fallback string) (int, string) {
	if errors.Is(err, storage.ErrReservierteAblage) {
		return http.StatusBadRequest, "reserved cardId"
	}
	return http.StatusInternalServerError, fallback
}

// safeIDRe erlaubt nur Zeichen die in UUIDs vorkommen + Unterstrich.
// Blockiert Pfad-Traversal-Versuche (keine Slashes, keine Punkte).
var safeIDRe = regexp.MustCompile(`^[a-zA-Z0-9\-_]{1,128}$`)

func validAttachmentID(s string) bool { return safeIDRe.MatchString(s) }

func isSVGMime(mime string) bool {
	return strings.Contains(strings.ToLower(mime), "svg")
}

func isSVGFilename(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".svg") || strings.HasSuffix(lower, ".svgz")
}

// isSVGBytes prüft ob der Inhalt nach SVG aussieht — Go's DetectContentType
// liefert für SVG nur "text/xml", nicht "image/svg+xml".
func isSVGBytes(buf []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(buf)))
	return strings.HasPrefix(s, "<svg") ||
		(strings.HasPrefix(s, "<?xml") && strings.Contains(s, "<svg"))
}

// sanitizeMime kürzt den MIME-Typ auf type/subtype, ohne Parameter.
func sanitizeMime(m string) string {
	if idx := strings.IndexByte(m, ';'); idx >= 0 {
		m = m[:idx]
	}
	return strings.TrimSpace(m)
}

// attachmentSafeFilename entfernt Null-Bytes, Slashes und Backslashes.
// Analog safeFilename() in src/attachments.js.
func attachmentSafeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == 0 || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "datei"
	}
	return name
}

// isMaxBytesError prüft ob ein http.MaxBytesError vorliegt.
func isMaxBytesError(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// AufraeumLage: Kartenlage für das Aufräumen von Resten in .purge-beiseite
// (Purge-Request und Serverstart, F-3050)
// (F-3038): verwaist, wenn die Karte im Stand fehlt oder das Board nicht mehr
// existiert; jeder andere Fehler → Rest bleibt liegen. Eigene Funktion, damit
// ein Test die Richtung festhält (F-3044) — invertiert löschte sie Dateien
// lebender Karten.
func AufraeumLage(ctx context.Context, l KartenLage) storage.KartenLageFn {
	return func(boardID, cardID string) (bool, error) {
		besteht, err := l.KarteBesteht(ctx, boardID, cardID)
		if errors.Is(err, storage.ErrBoardNotFound) {
			return true, nil
		}
		return !besteht, err
	}
}
