package api

// KF-3 (Sprint-82-Abschluss, Runde 2): Eine Datei an einem Kommentar gehört
// zu ihm (ADR-0135) — und ein fremder Kommentar darf nur von seinem Autor
// und von Owner/Admin gelöscht werden (ADR-0136). Der Vergleich am Ergebnis
// (pruefeKommentarAnhaenge) deckt PUT und Ops. Die Datei selbst liegt aber
// im AttachmentStore und wird über DELETE /boards/{b}/attachments/… entfernt,
// BEVOR der Browser den Eintrag aus dem Stand nimmt — ohne diese Prüfung
// bliebe der Kommentar stehen und sein Beleg wäre weg.

import (
	"context"
	"encoding/json"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// KommentarDateiRecht beantwortet für die Anhang-Routen, welche Dateien einer
// Karte an fremden Kommentaren hängen. nil = keine Prüfung (Betrieb ohne
// Identität, Tests).
type KommentarDateiRecht interface {
	// GeschuetzteDateien: Anhang-ids der Karte, die an einem Kommentar hängen,
	// den der Aufrufer nicht löschen darf, und — nur wenn es solche gibt —
	// alle Anhang-ids, die der Stand an der Karte führt. Fehler → Aufrufer
	// lehnt ab.
	GeschuetzteDateien(ctx context.Context, boardID, cardID string) (geschuetzt map[string]bool, alle []string, err error)
}

// GeschuetzteDateien liest den gespeicherten Stand des Boards. Eine Karte,
// die es nicht (mehr) gibt, schützt nichts — Karte löschen ist erlaubt
// (ADR-0136 §Grenzen).
func (a *BoardsAPI) GeschuetzteDateien(ctx context.Context, boardID, cardID string) (map[string]bool, []string, error) {
	s := a.kommentarSchreiberFuer(ctx, boardID)
	if !s.prueft() || s.darfFremde {
		return nil, nil, nil
	}
	b, err := a.repo.Get(ctx, boardID)
	if err != nil {
		return nil, nil, err
	}
	bestand, ok := leseKommentarBestand(b.StateJSON)
	if !ok {
		return nil, nil, errStandUnlesbar
	}
	kb := bestand.karten[cardID]
	if kb == nil || len(kb.anhaenge) == 0 {
		return nil, nil, nil
	}
	kb.parse()
	autorFremd := map[string]bool{}
	for _, k := range kb.kommentare {
		autorFremd[k.id] = !s.eigen(k)
	}
	aus := map[string]bool{}
	for aid, kid := range kb.anhaenge {
		if autorFremd[kid] {
			aus[aid] = true
		}
	}
	if len(aus) == 0 {
		return nil, nil, nil
	}
	return aus, anhangIDsDerKarte(b.StateJSON, cardID), nil
}

type standUnlesbarFehler struct{}

func (standUnlesbarFehler) Error() string { return "board state not readable" }

var errStandUnlesbar error = standUnlesbarFehler{}

// anhangIDsDerKarte: alle Anhang-ids, die der gespeicherte Stand an der Karte
// führt (für das teilweise Leeren einer noch bestehenden Karte).
func anhangIDsDerKarte(state []byte, cardID string) []string {
	var ids []string
	durchlaufeKarten(state, func(karte map[string][]byte) {
		var id string
		if json.Unmarshal(karte["id"], &id) != nil || id != cardID {
			return
		}
		var liste []json.RawMessage
		if json.Unmarshal(karte["attachments"], &liste) != nil {
			return
		}
		for _, el := range liste {
			var aid string
			var e map[string]json.RawMessage
			if json.Unmarshal(el, &e) == nil {
				_ = json.Unmarshal(e["id"], &aid)
			} else {
				_ = json.Unmarshal(el, &aid) // Legacy: Anhang-id als String (kommentar_anhaenge.go)
			}
			if aid != "" {
				ids = append(ids, aid)
			}
		}
	})
	return ids
}

// SpiegelUploadRecht (F-2242): Darf in das Anhang-Verzeichnis dieser Karte
// hochgeladen werden? nil = keine Prüfung (Betrieb ohne Board-Paar, Tests).
type SpiegelUploadRecht interface {
	// UploadGesperrt: true, wenn das Board eine Kopie ist (mit oder ohne
	// wirksamen Rückweg, F-2374) und die Karte dort ein Spiegel, eine wartende oder eine neue Karte
	// in einer markierten Lane ist (dieselbe Unterscheidung wie die
	// Kommentar-Schranke, schatten.KartenVerzeichnisGesperrt). Fehler →
	// Aufrufer lehnt ab (fail-closed).
	UploadGesperrt(ctx context.Context, boardID, cardID string) (bool, error)
}

// UploadGesperrt: siehe SpiegelUploadRecht. Auf Boards, die keine Kopie sind,
// ändert sich nichts (false).
func (a *BoardsAPI) UploadGesperrt(ctx context.Context, boardID, cardID string) (bool, error) {
	// F-2374: Die Sperre gilt für jede Kopie, mit und ohne Rückweg — im Lesemodus
	// (Rückweg aus oder bearbeitbar = 0) ist alles Gespiegelte nur lesbar, die
	// Spiegel-Schranke der Ops (schatten.SpiegelVerstoesse, WartendVerstoesse)
	// trifft dieselbe Kartenmenge wie KartenVerzeichnisGesperrt.
	kopie, err := a.repo.IstKopie(ctx, boardID)
	if err != nil || !kopie {
		return false, err
	}
	b, err := a.repo.Get(ctx, boardID)
	if err != nil {
		return false, err
	}
	wartend, err := a.repo.Wartend(ctx, boardID)
	if err != nil {
		return false, err
	}
	gesperrt, unlesbar := schatten.KartenVerzeichnisGesperrt(b.StateJSON, wartend, cardID)
	if unlesbar {
		return false, errStandUnlesbar
	}
	return gesperrt, nil
}

// KartenLage (F-3019/F-3020, #1143): Steht die Karte im gespeicherten Stand des
// Boards? Grundlage für den nachgeholten Karten-Purge (`?nurVerwaist=1`): Der
// Client hält einen Stand ohne die Karte nie für einen Beweis, dass sie am
// Server fehlt — das entscheidet der Server (knowledge §56).
type KartenLage interface {
	// KarteBesteht: true, wenn eine Karte mit dieser id im gespeicherten Stand
	// steht. Fehler (Board fehlt → storage.ErrBoardNotFound, Stand unlesbar →
	// errStandUnlesbar) → Aufrufer löscht nichts.
	KarteBesteht(ctx context.Context, boardID, cardID string) (bool, error)
}

// KarteBesteht: siehe KartenLage.
func (a *BoardsAPI) KarteBesteht(ctx context.Context, boardID, cardID string) (bool, error) {
	b, err := a.repo.Get(ctx, boardID)
	if err != nil {
		return false, err
	}
	da := false
	if !durchlaufeKarten(b.StateJSON, func(karte map[string][]byte) {
		var id string
		if json.Unmarshal(karte["id"], &id) == nil && id == cardID {
			da = true
		}
	}) {
		return false, errStandUnlesbar
	}
	return da, nil
}
