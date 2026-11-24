package api

// transfer.go — Cross-Board-Karten-Transfer (#296, Sprint 43, ADR-0044).
//
// POST /boards/{boardId}/cards/{cardId}/transfer
//   Body: { "toBoardId": "<uuid>", "toColumnId": "col-...." }
//   Response: { "newDisplayId": N, "oldDisplayId": M }
//
// ACL: editor+ am Quell-Board (durch ACLGuard erzwungen, weil URL unter
// /boards/* liegt) UND editor+ am Ziel-Board (manuell hier geprüft —
// ACLGuard sieht nur das URL-Board).
//
// Atomicity: BoardRepo.TransferCard läuft in einer BEGIN IMMEDIATE-Tx —
// kein partieller Transfer möglich.
//
// Anhänge laufen als zweiphasige Saga daneben (F-390): §1 kopiert in ein
// unsichtbares Staging-Verzeichnis, §2 benennt es nach erfolgreicher DB-Tx
// atomar an den Ziel-Pfad um, §3 verwirft es bei DB-Fehler. Ein Absturz
// zwischen §1 und §2 hinterlässt damit nichts, was den nächsten Versuch
// blockiert — siehe storage.AttachmentStore.StageCardAttachments.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/Andiministrator/aBoard/server/storage"
)

// TransferAPI hängt den /transfer-Endpoint an. attach ist optional — wenn
// gesetzt, werden Karten-Anhänge per Saga-Pattern mit übertragen.
type TransferAPI struct {
	boards *storage.BoardRepo
	users  *storage.UserRepo
	acl    *storage.AclRepo
	attach *storage.AttachmentStore
}

func NewTransferAPI(boards *storage.BoardRepo, users *storage.UserRepo, acl *storage.AclRepo) *TransferAPI {
	return &TransferAPI{boards: boards, users: users, acl: acl}
}

// WithAttachments aktiviert den Attachment-Volume-Transfer. Ohne wirken
// Anhänge so, als hätte die Karte keine — die IDs bleiben in card.attachments
// stehen, aber die Datei-Pfade zeigen weiter aufs Quell-Board (i. d. R.
// unerwünscht — gesetzt wird der Store immer in production).
func (a *TransferAPI) WithAttachments(store *storage.AttachmentStore) *TransferAPI {
	a.attach = store
	return a
}

func (a *TransferAPI) Register(mux *http.ServeMux) {
	mux.Handle("POST /boards/{boardId}/cards/{cardId}/transfer",
		WithDefaults(http.HandlerFunc(a.handleTransfer)))
}

type transferRequest struct {
	ToBoardID  string `json:"toBoardId"`
	ToColumnID string `json:"toColumnId"`
}

type transferResponse struct {
	NewDisplayID int `json:"newDisplayId"`
	OldDisplayID int `json:"oldDisplayId"`
	// Nur bei Finding-Karten (F-1542): Nummer im Ziel und im Quell-Board.
	NewFindingNr int `json:"newFindingNr,omitempty"`
	OldFindingNr int `json:"oldFindingNr,omitempty"`
}

// multiUserMode beantwortet für den Legacy-Bypass die eine Frage, die zählt:
// läuft dieser Server als Mehrnutzer-Instanz? Nur dann ist ein Request ohne
// Identität ein Problem (F-391).
//
// Zwei Signale, beide notwendig:
//
//  1. ABOARD_TRUST_FORWARD_AUTH gesetzt — der Betreiber hat den Mehrnutzer-
//     Modus eingeschaltet (docs/server-betrieb.md §Zustandsmatrix: die beiden
//     Betriebsfälle hängen an genau dieser Variable). Kommt dann ein Request
//     OHNE Identitäts-Header an, ist er am Reverse-Proxy vorbeigelaufen —
//     das ist kein Single-User-Betrieb, das ist ein Umgehungsversuch.
//  2. Es existiert mindestens ein ACL-Eintrag für eine echte Identität —
//     das Restsignal für den Fall, dass der Trust-Schalter (vorübergehend)
//     aus ist, die Datenbank aber geteilte Boards führt.
//
// Der reine Single-User-Default erfüllt keines der beiden: kein Trust-
// Schalter, keine Identitäten, nur der System-Eintrag des Mail-Workers, den
// HasNonSystemACLEntries bewusst ausklammert.
//
// Grenze dieser Härtung — ehrlich benannt: der ACLGuard lässt Requests OHNE
// Identität weiterhin komplett durch (server/api/acl.go, Legacy-Bypass nach
// ADR-0059 §2). Wer bei aktivem Trust am Proxy vorbeigeht, kann also nach wie
// vor direkt auf das Ziel-Board schreiben — nur eben nicht mehr über den
// Transfer-Endpoint. Das hier ist Defense-in-depth für die eine Route, die
// Board-Grenzen überschreitet, nicht die Reparatur des Bypasses selbst.
func (a *TransferAPI) multiUserMode(ctx context.Context) (bool, error) {
	// F-832-(c)-Nacharbeit (Kritiker Sprint 61): Diese Zeile las den Schalter
	// bis Sprint 61 als `!= ""` und war damit die VIERTE Lesestelle — die
	// Vereinheitlichung hatte drei erfasst und diese übersehen, während der
	// Kommentar an der neuen Funktion „einzige Lesestelle" behauptete.
	// Folge war keine Sicherheitslücke, sondern die restriktivere Richtung:
	// Bei `ABOARD_TRUST_FORWARD_AUTH=0` galt hier weiterhin Multi-User, was
	// einen Cross-Board-Transfer ohne Identität mit 403 abweist.
	// Kritik A-6 (#1051): auch der OIDC-Betrieb ist Mehrnutzer-Betrieb.
	if strengeAnmeldung() {
		return true, nil
	}
	return a.acl.HasNonSystemACLEntries(ctx)
}

func (a *TransferAPI) handleTransfer(w http.ResponseWriter, r *http.Request) {
	srcBoardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	if srcBoardID == "" || cardID == "" {
		writeError(w, http.StatusBadRequest, "missing boardId or cardId")
		return
	}
	// Die reservierte Board-Ablage ist keine Karte (Karte #443). Der Store
	// lehnt sie ohnehin ab — hier steht die frühe Antwort, damit der Aufrufer
	// 400 mit Grund bekommt statt 500 aus der Tiefe, und damit gar nicht erst
	// Verzeichnisse gelesen werden.
	if cardID == storage.BoardAblageId {
		writeError(w, http.StatusBadRequest, "reserved cardId")
		return
	}

	var req transferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.ToBoardID == "" || req.ToColumnID == "" {
		writeError(w, http.StatusBadRequest, "toBoardId and toColumnId required")
		return
	}

	// Identität ermitteln (ACLGuard hat am Quell-Board bereits editor+ erzwungen,
	// aber für den Ziel-Board-Check brauchen wir die Email selbst).
	id := IdentityFromContext(r.Context())
	switch {
	case a.acl == nil:
		// Kein ACL-Repo verdrahtet: dieser Server kennt gar kein Rechtemodell
		// (Test-/Embed-Setup). Es gibt nichts zu prüfen. Vor F-391 war dieser
		// Fall nicht abgedeckt — lag eine Identität an, lief der Code direkt in
		// einen Nil-Pointer-Panic.
	case id == nil:
		// Legacy-Bypass (Forward-Auth aus / Request ohne Identitäts-Header).
		//
		// F-391: Die Vorgänger-Fassung prüfte NUR, ob das ZIEL-Board eigene
		// ACL-Einträge hat. Ein Ziel-Board ohne Einträge war damit für jeden
		// unauthentifizierten Anrufer beschreibbar, obwohl auf demselben Server
		// geteilte Boards lagen. Die Entscheidung „darf ohne Identität
		// geschrieben werden" hängt nicht am einzelnen Board, sondern am
		// Betriebsmodus des Servers — genau den fragen wir jetzt ab.
		multi, err := a.multiUserMode(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		}
		if multi {
			writeError(w, http.StatusForbidden, "forbidden: transfer requires identity in multi-user mode")
			return
		}
	default:
		callerEmail := storage.NormalizeEmail(id.Email)
		if callerEmail == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
		// Ziel-Board: editor+ erforderlich
		role, found, err := a.acl.GetRole(r.Context(), req.ToBoardID, callerEmail)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		}
		if !found {
			writeError(w, http.StatusForbidden, "forbidden: no access to target board")
			return
		}
		if roleRank[role] < roleRank[storage.RoleEditor] {
			writeError(w, http.StatusForbidden, "forbidden: editor+ required on target board")
			return
		}
	}

	actor := ""
	if id != nil {
		actor = storage.NormalizeEmail(id.Email)
	}

	// Attachment-Saga §1: Anhänge der Karte in ein Staging-Verzeichnis kopieren
	// (UUID-Pfad bleibt identisch, nur der boardId-Pfadanteil wechselt). Die
	// Kopie geschieht VOR der DB-Tx, weil sie die langsame, fehleranfällige
	// Hälfte ist — sie darf scheitern, ohne dass Board-Daten angefasst wurden.
	//
	// F-390: Sie landet dabei NICHT am Ziel-Pfad, sondern unsichtbar daneben.
	// Stürzt der Server zwischen §1 und §2 ab, bleibt damit kein Verzeichnis am
	// Ziel-Pfad liegen, das jeden künftigen Transfer derselben Karte mit 500
	// blockiert. Sichtbar wird die Kopie erst durch das Rename in §2.
	var staged *storage.StagedAttachments
	if a.attach != nil {
		st, err := a.attach.StageCardAttachments(srcBoardID, req.ToBoardID, cardID)
		if err != nil {
			// Zweite Tür zur selben Schranke (Sprint-62-Kritiker, D1):
			// `StageCardAttachments` lehnt die reservierte Board-Ablage mit
			// demselben Fehler ab wie `DeleteCard` — Karte #460 hat das
			// Status-Mapping aber nur an der einen Stelle nachgezogen. Der
			// Vorab-Check oben fängt den Fall heute ab; genau dieselbe
			// Begründung galt auch dort, wo das Mapping schon steht.
			status, msg := attachmentFehlerAntwort(err, "attachment copy failed")
			writeError(w, status, msg, err)
			return
		}
		staged = st
	}

	res, err := a.boards.TransferCard(r.Context(), storage.TransferCardParams{
		SrcBoardID:  srcBoardID,
		DstBoardID:  req.ToBoardID,
		CardID:      cardID,
		DstColumnID: req.ToColumnID,
		Actor:       actor,
	})
	if err != nil {
		// Saga §3: DB-Tx schlug fehl → Staging-Kopie verwerfen. Best-effort;
		// bleibt sie wegen eines Filesystem-Fehlers liegen, räumt der nächste
		// Stage-Lauf sie nach stagingMaxAge weg. Der Ziel-Pfad wurde nie
		// angefasst — anders als früher gibt es hier nichts zu löschen, was
		// ein Nutzer sehen könnte.
		staged.Abort()
		switch {
		case errors.Is(err, storage.ErrTransferSameBoard):
			writeError(w, http.StatusBadRequest, "src and dst board must differ")
		case errors.Is(err, storage.ErrBoardNotFound):
			writeError(w, http.StatusNotFound, "board not found")
		case errors.Is(err, storage.ErrCardNotFound):
			writeError(w, http.StatusNotFound, "card not found in source board")
		case errors.Is(err, storage.ErrInvalidColumn):
			writeError(w, http.StatusBadRequest, "target column not defined in target board")
		case errors.Is(err, storage.ErrTransferMetaCard):
			writeError(w, http.StatusBadRequest, "sprint-meta cards cannot be transferred")
		case errors.Is(err, storage.ErrConflict):
			writeError(w, http.StatusConflict, "optimistic-concurrency conflict — retry")
		case errors.Is(err, storage.ErrSpiegelGeschuetzt):
			// ADR-0141 §5.1/§7 (Karte #826): Transfer ist für Spiegel gesperrt.
			writeError(w, http.StatusForbidden, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "transfer failed", err)
		}
		return
	}

	// Saga §2: DB-Tx OK → Staging-Kopie an den Ziel-Pfad umbenennen (atomar,
	// gleiches Volume) und erst danach die alten Dateien im src-Board löschen.
	//
	// Die Reihenfolge ist bindend: schlägt das Rename fehl, bleiben die
	// Original-Dateien stehen. Sonst wären die Anhänge weg — die Karte lebt
	// dann schon im Ziel-Board, ohne dass es einen Rückweg gäbe.
	commitErr := staged.Commit()
	if commitErr != nil {
		// Kein 500: die Karte IST transferiert, das ist die Wahrheit, die der
		// Client erfährt. Die Dateien liegen weiterhin am Quell-Pfad und
		// zusätzlich im Staging — nichts ist verloren, aber ein Mensch muss
		// hinsehen. Deshalb Logzeile mit beiden Pfaden statt stillem Verwerfen.
		log.Printf("aboard-server: transfer %s/%s → %s: Anhang-Commit fehlgeschlagen "+
			"(Quell-Dateien bleiben stehen): %v", srcBoardID, cardID, req.ToBoardID, commitErr)
	} else if a.attach != nil {
		/* Sprint-54-Kritiker: Der Fehler wurde hier verworfen, während der
		   Commit-Fehler direkt darüber sorgfältig geloggt wird. Schlägt das
		   Löschen fehl (Dateisystem, Berechtigungen), bleiben die alten
		   Quell-Dateien unbegrenzt liegen — ein stiller Platzverbrauch ohne
		   jede Spur. Kein HTTP-Fehler: der Transfer IST gelungen, die Karte
		   zeigt auf die richtigen Pfade. Aber jemand muss es erfahren können. */
		if err := a.attach.DeleteCard(srcBoardID, cardID); err != nil {
			log.Printf("aboard-server: transfer %s/%s → %s: Quell-Anhänge nicht gelöscht "+
				"(bleiben als Platzverbrauch liegen, kein Funktionsverlust): %v",
				srcBoardID, cardID, req.ToBoardID, err)
		}
	}

	writeJSON(w, http.StatusOK, transferResponse{
		NewDisplayID: res.NewDisplayID,
		OldDisplayID: res.OldDisplayID,
		NewFindingNr: res.NewFindingNr,
		OldFindingNr: res.OldFindingNr,
	})
}
