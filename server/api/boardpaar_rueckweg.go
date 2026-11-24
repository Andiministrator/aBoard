package api

// boardpaar_rueckweg.go — Rückweg des Board-Paars, Stufe 2c (ADR-0141 §5.3,
// Karte #827).
//
// Die vier bedingten Rückweg-Ops (card.moveIf, card.checkItemIf,
// comment.editIf, termin.fieldIf) nimmt der Ops-Endpunkt nur auf dem Rückweg
// an: Für Browser und Agenten wären sie neue Schreibwege, die keine der
// vorhandenen Regeln kennt (etwa „comment.edit gibt es über ein
// delegierendes Token nicht“). Den Rückweg kennzeichnet ein Kontext-Merkmal,
// das nur der Handler von POST /agent/boardpaar/anwenden setzt — kein
// Request-Wert, kein Token-Typ (dieselbe Begründung wie werkzeugWegSchluessel,
// agent_loeschen.go).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
)

// KonfigBoardpaarRueckweg: Einstellung außen (ADR-0141 §7, „Umsetzung 2c“
// Entscheidung 1). „aus“ ist exakt Schritt 2b: Spiegel und wartende Spiegel
// sind nur lesbar. „an“ ist die feine Schranke plus Journal am Ops-Weg.
const (
	KonfigBoardpaarRueckweg = storage.KonfigBoardpaarRueckwegSchluessel
	BoardpaarRueckwegAus    = "aus"
	BoardpaarRueckwegAn     = "an"
)

// PruefeBoardpaarRueckweg: Prüf- und Normalform-Funktion für den Konfig-Weg
// (WithKonfigPruefung). Leer heißt „aus“. „an“ nimmt der Admin-Weg seit E4 an
// (bis dahin war der Rückweg unvollständig, Plan D1).
func PruefeBoardpaarRueckweg(_ string, value string) (string, error) {
	switch s := strings.TrimSpace(value); s {
	case BoardpaarRueckwegAus, "":
		return BoardpaarRueckwegAus, nil
	case BoardpaarRueckwegAn:
		return BoardpaarRueckwegAn, nil
	default:
		return "", fmt.Errorf("%s: erlaubt sind aus oder an (nicht %q)", KonfigBoardpaarRueckweg, s)
	}
}

// BoardpaarRueckwegAnMitFehler liest den Schalter je Anfrage (Muster
// BoardpaarSeiteMitFehler): Ein fehlender Schlüssel und ein ungültiger Wert
// sind „aus“ ohne Fehler; err ≠ nil nur, wenn server_config nicht lesbar ist
// (dann ebenfalls „aus“). Der Ops-Weg lehnt in diesem Fall ab (fail-closed).
func BoardpaarRueckwegAnMitFehler(ctx context.Context, cfg *storage.ConfigRepo) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	v, err := cfg.Get(ctx, KonfigBoardpaarRueckweg)
	if err != nil {
		if errors.Is(err, storage.ErrConfigKeyNotFound) {
			return false, nil
		}
		return false, err
	}
	switch s := strings.TrimSpace(v.Value); s {
	case BoardpaarRueckwegAn:
		return true, nil
	case BoardpaarRueckwegAus, "":
		return false, nil
	default:
		log.Printf("boardpaar: rueckweg %q ungültig, gilt als aus", s)
		return false, nil
	}
}

// BoardpaarRueckwegFolge gleicht beim Umschalten die Rolle des Board-Paar-Teams
// auf allen Kopien an (Plan D9): an → viewer wird editor, aber nur auf Kopien
// mit bearbeitbar = 1 (#836, Nachtrag Punkt 6; auf den übrigen bleibt bzw.
// wird das Team viewer), aus (auch Löschen oder ein ungültiger Wert) → editor
// wird viewer. Nur Subjekte aus
// boardpaar.team, gelesen in derselben Transaktion; ohne Wechsel des
// Server-Schalters geschieht hier nichts, ein erneutes Speichern dreht also
// keine Rolle zurück, die ein Owner von Hand geändert hat. Anders die Wege, die
// „bearbeitbar“ setzen (POST boards und Hinweg mit Feld, Admin-Reiter, #836
// Punkt 6): Sie gleichen die Team-Rolle der Kopie bei JEDEM Aufruf an die
// Regel an und setzen von Hand geänderte Team-Rollen dort zurück — aber nur,
// solange der Server-Schalter an ist (bearbeitbarSetzenMitTeamTx).
func BoardpaarRueckwegFolge(ctx context.Context, conn *sql.Conn, alt, neu string) error {
	anAlt := strings.TrimSpace(alt) == BoardpaarRueckwegAn
	anNeu := strings.TrimSpace(neu) == BoardpaarRueckwegAn
	if anAlt == anNeu {
		return nil
	}
	roh, err := storage.KonfigKlartextTx(ctx, conn, KonfigBoardpaarTeam)
	if err != nil {
		return err
	}
	n, err := storage.BoardpaarTeamRolleTx(ctx, conn, "", teamNachsichtig(roh), anNeu)
	if err != nil {
		return err
	}
	log.Printf("boardpaar: rueckweg %v → %v, team-rolle auf kopien angeglichen (%d zeile(n))", anAlt, anNeu, n)
	return nil
}

// BoardpaarTeamEntzug: was ein Speichern von boardpaar.team auf den Kopien
// entzieht (oder entziehen würde) — die herausgefallenen Einträge und die Zahl
// der ACL-Zeilen (viewer/editor) auf allen Kopien.
type BoardpaarTeamEntzug struct {
	Weg    []string `json:"weg"`
	Zeilen int64    `json:"zeilen"`
}

var (
	// ErrBoardpaarTeamEntzugUnbestaetigt: Das Speichern entzöge Zugriff,
	// ohne dass jemand die Liste gesehen und bestätigt hat (Sitzungs-Kritik
	// zum Sprint-94-Abschluss N-5, F-2268). Der allgemeine Konfig-Weg
	// (PUT/DELETE /admin/config/boardpaar.team) kennt keine Bestätigung und
	// endet deshalb hier; der Admin-Dialog geht über PUT /admin/boardpaar/team.
	ErrBoardpaarTeamEntzugUnbestaetigt = errors.New("boardpaar.team: das Speichern entzieht Zugriff auf Kopien — " +
		"nur über PUT /admin/boardpaar/team mit Bestätigung")
	// ErrBoardpaarTeamVeraltet: boardpaar.team hat sich seit dem Lesen geändert.
	ErrBoardpaarTeamVeraltet = errors.New("boardpaar.team wurde inzwischen geändert")
	// errBoardpaarTeamVorschau: Vorschau gerechnet, nichts geschrieben.
	errBoardpaarTeamVorschau = errors.New("boardpaar.team: vorschau")
)

// boardpaarTeamWeg: Einträge aus alt, die in neu fehlen (normalisiert).
func boardpaarTeamWeg(alt, neu string) []string {
	bleibt := map[string]bool{}
	for _, s := range teamNachsichtig(neu) {
		bleibt[storage.NormalizeEmail(s)] = true
	}
	weg := []string{}
	for _, s := range teamNachsichtig(alt) {
		if s = storage.NormalizeEmail(s); s != "" && !bleibt[s] && !slices.Contains(weg, s) {
			weg = append(weg, s)
		}
	}
	return weg
}

// teamNormalform: die gespeicherte Form eines Teams (wie PruefeBoardpaarTeam),
// damit „bisher“ in Schreibweise des Dialogs und gespeicherter Wert vergleichbar
// sind; ein ungültiger Wert bleibt, wie er ist.
func teamNormalform(roh string) string {
	if norm, _, err := storage.TeamAusText(roh); err == nil {
		return norm
	}
	return strings.TrimSpace(roh)
}

// BoardpaarTeamFolgeMit: Wer aus boardpaar.team fällt (auch beim Löschen des
// Schlüssels), verliert auf allen Kopien viewer und editor (Sprint-94-Kritik
// S-2). Neuzugänge trägt die Folge bewusst nicht ein (ADR-0141, „Umsetzung
// 2c“): sie kommen mit der nächsten Kopie oder durch einen Owner.
//
// F-2268: Der Entzug läuft nur mit bestaetigt = true. Ohne Bestätigung
// rechnet die Folge ihn in der Transaktion aus und bricht ab, sobald er eine
// Zeile träfe — der Aufrufer rollt zurück und zeigt erg als Vorschau.
// bisher ≠ nil: der Wert, den der Admin gesehen hat; weicht der gespeicherte
// ab, ErrBoardpaarTeamVeraltet (sonst bestätigte er eine andere Liste). erg
// darf nil sein.
func BoardpaarTeamFolgeMit(bestaetigt bool, bisher *string, erg *BoardpaarTeamEntzug) storage.KonfigFolge {
	return func(ctx context.Context, conn *sql.Conn, alt, neu string) error {
		if bisher != nil && teamNormalform(alt) != teamNormalform(*bisher) {
			return ErrBoardpaarTeamVeraltet
		}
		weg := boardpaarTeamWeg(alt, neu)
		var n int64
		if len(weg) > 0 {
			var err error
			if n, err = storage.BoardpaarTeamEntziehenTx(ctx, conn, weg); err != nil {
				return err
			}
		}
		if erg != nil {
			erg.Weg, erg.Zeilen = weg, n
		}
		if n > 0 && !bestaetigt {
			if bisher == nil {
				return ErrBoardpaarTeamEntzugUnbestaetigt
			}
			return errBoardpaarTeamVorschau
		}
		if len(weg) > 0 {
			log.Printf("boardpaar: %d subjekt(e) aus boardpaar.team entfernt, %d acl-zeile(n) auf kopien entzogen", len(weg), n)
		}
		return nil
	}
}

// BoardpaarTeamFolge: die Folge des allgemeinen Konfig-Wegs — ändert nichts,
// was Zugriff entzöge (ErrBoardpaarTeamEntzugUnbestaetigt, F-2268).
var BoardpaarTeamFolge = BoardpaarTeamFolgeMit(false, nil, nil)

// rueckwegAnMitFehler: der Schalter des Servers; ohne MitBoardpaar „aus“.
// Für eine Kopie zählt er nur zusammen mit ihrem Schalter „bearbeitbar“ —
// Schreibwege fragen kopieRueckwegAnMitFehler.
func (a *BoardsAPI) rueckwegAnMitFehler(ctx context.Context) (bool, error) {
	return BoardpaarRueckwegAnMitFehler(ctx, a.paarCfg)
}

// kopieRueckwegAnMitFehler: der wirksame Rückweg auf einer Kopie (ADR-0141
// Nachtrag #836) — boardpaar.rueckweg = an UND bearbeitbar = 1. Ist eins von
// beiden falsch, gilt für dieses Board der Schreibschutz aus 2b. Ein
// Lesefehler an einer der beiden Stellen ist false mit Fehler (fail-closed;
// der Ops-Weg schreibt dann nichts). Den Schalter des Boards liest er nur,
// wenn der des Servers an ist.
func (a *BoardsAPI) kopieRueckwegAnMitFehler(ctx context.Context, boardID string) (bool, error) {
	an, err := a.rueckwegAnMitFehler(ctx)
	if err != nil || !an {
		return false, err
	}
	return a.repo.KopieBearbeitbar(ctx, boardID)
}

// rueckwegOps: die Op-Typen, die es nur auf dem Rückweg gibt.
var rueckwegOps = map[string]bool{
	"card.moveIf": true, "card.checkItemIf": true, "comment.editIf": true, "termin.fieldIf": true,
}

type boardpaarRueckwegSchluessel struct{}

// mitBoardpaarRueckweg kennzeichnet einen Schreibvorgang als Rückweg des
// Board-Paars. Nur der Anwenden-Handler ruft das.
func mitBoardpaarRueckweg(ctx context.Context) context.Context {
	return context.WithValue(ctx, boardpaarRueckwegSchluessel{}, true)
}

// boardpaarRueckwegAus: Ist dieser Schreibvorgang der Rückweg des Board-Paars?
func boardpaarRueckwegAus(ctx context.Context) bool {
	v, _ := ctx.Value(boardpaarRueckwegSchluessel{}).(bool)
	return v
}

// rueckwegOpGrund: Grund für die Ablehnung einer Rückweg-Op außerhalb des
// Rückwegs, sonst "".
func rueckwegOpGrund(typ string, rueckweg bool) string {
	if rueckwegOps[typ] && !rueckweg {
		return typ + " is only accepted on the board-pair return path (ADR-0141 §5.3)"
	}
	return ""
}

type boardpaarAkteurSchluessel struct{}

// BoardpaarKonfliktAutor: Autor des Konflikt-Kommentars (ADR-0141 §6). Der
// Hinweg führt ihn als Herkunft „innen“ — außen ist er nicht bearbeitbar.
const BoardpaarKonfliktAutor = "Board-Paar"

// boardpaarAkteurPraefix: Akteur im Aktivitätslog und in den Ops des Rückwegs
// („Umsetzung 2c“ Punkt 23).
const boardpaarAkteurPraefix = "boardpaar: "

// mitBoardpaarAkteur setzt die Person außen, für die der Rückweg schreibt. Nur
// der Anwenden-Handler ruft das, zusammen mit mitBoardpaarRueckweg.
func mitBoardpaarAkteur(ctx context.Context, person string) context.Context {
	return context.WithValue(ctx, boardpaarAkteurSchluessel{}, person)
}

// boardpaarAkteurAus: die Person außen auf dem Rückweg, sonst "".
func boardpaarAkteurAus(ctx context.Context) string {
	if !boardpaarRueckwegAus(ctx) {
		return ""
	}
	v, _ := ctx.Value(boardpaarAkteurSchluessel{}).(string)
	return v
}

// istExternerAutor: Kommentar, der über das Board-Paar von außen kam
// (Autor „extern: …“, ohne authorEmail).
func istExternerAutor(autor, autorEmail string) bool {
	return autorEmail == "" && strings.HasPrefix(autor, "extern: ") && len(autor) > len("extern: ")
}

type boardpaarHashSchluessel struct{}

// mitRueckwegHash: Der Idempotenz-Hash der Rückweg-Op läuft über den Eintrag
// des Skripts, nicht über die Op, die der Server daraus baut — die trägt
// Zeitstempel und den gelesenen Ort und wäre bei einer Wiederholung nicht
// gleich. So gilt: derselbe Eintrag → Wiederholung, anderer Eintrag unter
// derselben op_id → 409 (ADR-0141 §5.3, „Umsetzung 2c“ Punkt 25).
func mitRueckwegHash(ctx context.Context, eintrag json.RawMessage) context.Context {
	return context.WithValue(ctx, boardpaarHashSchluessel{}, eintrag)
}

func rueckwegHashAus(ctx context.Context) json.RawMessage {
	if !boardpaarRueckwegAus(ctx) {
		return nil
	}
	v, _ := ctx.Value(boardpaarHashSchluessel{}).(json.RawMessage)
	return v
}
