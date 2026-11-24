package api

// kommentar_anhaenge.go — Dateien an Kommentaren (Karte #678, ADR-0135).
//
// Eine Kommentar-Datei ist ein gewöhnlicher Karten-Anhang: dieselbe Ablage
// `<ABOARD_ATTACHMENTS_DIR>/<boardId>/<cardId>/<attId>__<name>`, dieselbe
// Grenze ABOARD_ATTACHMENT_MAX_BYTES, dieselbe Route und damit dieselbe ACL
// wie die Karte. Unterschied ist allein das Feld `commentId` am Eintrag in
// `card.attachments` — der Server kennt es nur an dieser Stelle.
//
// `comment.delete` nimmt im Fold (src/ops.js) die Einträge mit dieser
// `commentId` aus `card.attachments`. Die Dateien liegen aber nur im
// Dateisystem; ohne diesen Schritt blieben sie dort unreferenziert liegen —
// lesbar für jeden mit Leserecht auf das Board, ohne dass eine Oberfläche sie
// noch zeigt.
//
// Bewusst eng gefasst: Gelöscht wird nur, was (1) eine `comment.delete`-Op
// dieses Batches nennt, (2) vor dem Fold an genau diesem Kommentar hing und
// (3) nach dem Fold an der Karte fehlt. Ein Anhang, dessen Eintrag auf
// anderem Weg verschwindet (etwa ein card.upsert ohne `attachments`, F-926),
// verliert dadurch NICHT seine Datei — ein verlorener Eintrag ist aus dem
// Verlauf wiederherstellbar, eine gelöschte Datei nicht.
//
// Aus demselben Grund löscht der Ganz-Board-PUT NIE Dateien (KA-1, ADR-0135
// §3): Er kommt auch von veralteten Browserständen („Meine Version behalten“,
// Rückfall bei 4xx) und würde die Dateien eines Kommentars vernichten, den
// jemand anderes gerade angelegt hat.

import (
	"encoding/json"
	"log"
)

// kommentarAnhang benennt eine Datei, die mit ihrem Kommentar fällt.
type kommentarAnhang struct {
	CardID string
	AttID  string
}

// kommentarAnhaengeZumLoeschen vergleicht den Stand vor und nach dem Fold und
// liefert die Dateien der durch `comment.delete` entfernten Kommentare.
// Unlesbarer Stand → nichts (Ausfall in Richtung Nichtstun).
func kommentarAnhaengeZumLoeschen(vorher, nachher []byte, ops []serverOp) []kommentarAnhang {
	type ziel struct{ card, kommentar string }
	geloescht := map[ziel]bool{}
	for _, op := range ops {
		if op.Type != "comment.delete" {
			continue
		}
		var p struct {
			CardID    string `json:"cardId"`
			CommentID string `json:"commentId"`
		}
		if json.Unmarshal(op.Payload, &p) != nil || p.CardID == "" || p.CommentID == "" {
			continue
		}
		geloescht[ziel{p.CardID, p.CommentID}] = true
	}
	if len(geloescht) == 0 {
		return nil
	}
	alt, ok := kartenAnhaenge(vorher)
	if !ok {
		return nil
	}
	neu, ok := kartenAnhaenge(nachher)
	if !ok {
		return nil
	}
	var out []kommentarAnhang
	for cardID, anhaenge := range alt {
		for _, a := range anhaenge {
			if a.CommentID == "" || !geloescht[ziel{cardID, a.CommentID}] {
				continue
			}
			if _, bleibt := neu[cardID][a.ID]; bleibt {
				continue
			}
			out = append(out, kommentarAnhang{CardID: cardID, AttID: a.ID})
		}
	}
	return out
}

type anhangEintrag struct {
	ID        string
	CommentID string
}

// kartenAnhaenge liest je Karte die Anhänge (id → Eintrag). Einträge ohne
// Objektform (Mail-Altbestand: reiner id-String, F-1088) tragen keine
// `commentId` und werden als solche ohne Kommentarbezug geführt.
func kartenAnhaenge(state []byte) (map[string]map[string]anhangEintrag, bool) {
	var root struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					ID          string            `json:"id"`
					Attachments []json.RawMessage `json:"attachments"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if json.Unmarshal(state, &root) != nil {
		return nil, false
	}
	out := map[string]map[string]anhangEintrag{}
	for _, sp := range root.Board.Sprints {
		for _, c := range sp.Cards {
			if c.ID == "" {
				continue
			}
			m := map[string]anhangEintrag{}
			for _, roh := range c.Attachments {
				var e struct {
					ID        string `json:"id"`
					CommentID string `json:"commentId"`
				}
				if json.Unmarshal(roh, &e) != nil {
					var nurID string
					if json.Unmarshal(roh, &nurID) == nil && nurID != "" {
						m[nurID] = anhangEintrag{ID: nurID}
					}
					continue
				}
				if e.ID != "" {
					m[e.ID] = anhangEintrag{ID: e.ID, CommentID: e.CommentID}
				}
			}
			out[c.ID] = m
		}
	}
	return out, true
}

// loescheKommentarAnhaenge entfernt die Dateien nach dem COMMIT. Best-effort:
// Der Kommentar IST gelöscht; eine Datei, die liegen bleibt, gehört ins Log,
// rechtfertigt aber keinen Fehler an den Client.
func (a *BoardsAPI) loescheKommentarAnhaenge(boardID string, liste []kommentarAnhang) {
	if a.attach == nil {
		return
	}
	for _, k := range liste {
		if !validAttachmentID(k.CardID) || !validAttachmentID(k.AttID) {
			continue
		}
		if err := a.attach.Delete(boardID, k.CardID, k.AttID); err != nil {
			log.Printf("aboard-server: Kommentar-Anhang %s an Karte %s (Board %s) nicht entfernt: %v",
				k.AttID, k.CardID, boardID, err)
		}
	}
}
