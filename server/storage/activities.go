package storage

// activities.go — Server-seitiges Activity-Log (Karte #200, ADR-0037;
// ergänzt um card_id/op_type/actor_kind und die Frist-Löschung durch
// Migration 0016, ADR-0129 §5, Karte #554).
//
// Persistenz: relationale Tabelle `activities` (Migration 0003/0016).
// Wahl begründet: analog boards/users/acl; effizientes Trimming per
// DELETE ohne JSON-Parsing; querybar nach User/Board/Karte.
//
// Frist statt Kappung (ADR-0129 §5): Bis Karte #554 galt ein harter Cap von
// 500 Einträgen je Board (F-159). Seit Migration 0016 entfällt der Cap
// zugunsten einer Aufbewahrungsfrist — ein Protokoll je Karte (RecordOps)
// erzeugt deutlich mehr Einträge als die bisherigen Board-/ACL-Mutationen,
// ein fester Cap würde dort viel zu früh greifen. Nach jedem Insert werden
// Einträge des betroffenen Boards gelöscht, die älter als die Frist sind.
// Atomare DB-Operation, kein Goroutine-Hintergrundlauf nötig.
//
// Zeitstempel fester Breite und Obergrenze (F-1157, Migration 0019): ts
// steht seit 0019 als "2006-01-02T15:04:05.000Z" (activityTS) statt
// RFC3339Nano, damit Zeichenketten- und Zeitordnung übereinstimmen und der
// Index (board_id, ts) Löschen und Lesen trägt. Die Frist allein begrenzt die
// Tabelle nicht: Ein Agent darf bis zu 60 × 200 Ops je Minute schreiben. Dazu
// kommt deshalb eine Sicherheits-Obergrenze je Board (activityMaxProBoard) —
// nur für die Op-Einträge (action = 'op', Teilindex aus Migration 0021), in
// Einfügereihenfolge statt nach ts (F-1212).
// ACL- und board-*-Einträge fallen nie der Grenze zum Opfer: Sonst könnte
// jeder mit Schreibrecht die ACL-Historie per Op-Flut verdrängen (Kritiker
// Sprint 74).
//
// Legacy-Bypass (ADR-0059 §2): Im No-Auth-Modus bleibt actor = '' —
// kein Crash, kein Pflichtfeld. Logging läuft in beiden Modi.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// ActivityRetentionDaysDefault ist die Voreinstellung für die Frist, wenn
// ABOARD_ACTIVITY_RETENTION_DAYS fehlt oder ungültig ist (ADR-0129 §5).
const ActivityRetentionDaysDefault = 365

// activityRetentionDaysEnvVar ist der Name der überschreibenden Env-Var.
const activityRetentionDaysEnvVar = "ABOARD_ACTIVITY_RETENTION_DAYS"

// activityRetentionDays liest die Frist (in Tagen) aus der Umgebung. Ein
// fehlender Wert fällt still auf die Voreinstellung zurück; ein gesetzter,
// aber ungültiger Wert (keine ganze Zahl, <= 0) ebenfalls — mit einer
// Log-Warnung, damit ein Tippfehler in der Konfiguration nicht lautlos die
// falsche Frist scharf schaltet.
var retentionWarnungEinmal sync.Once

func activityRetentionDays() int {
	raw := os.Getenv(activityRetentionDaysEnvVar)
	if raw == "" {
		return ActivityRetentionDaysDefault
	}
	n, err := strconv.Atoi(raw)
	if err != nil || !fristGueltig(n) {
		// Einmal je Prozess melden (F-1148): Die Funktion läuft bei jedem
		// Schreibvorgang, ein Agent mit vielen Ops flutete sonst das Log.
		retentionWarnungEinmal.Do(func() {
			log.Printf("aboard-server: %s=%q ungültig (erwartet ganze Zahl 1–%d), verwende Voreinstellung %d Tage",
				activityRetentionDaysEnvVar, raw, FristTageMax, ActivityRetentionDaysDefault)
		})
		return ActivityRetentionDaysDefault
	}
	return n
}

// ActivityMaxProBoardDefault ist die Sicherheits-Obergrenze der Op-Einträge
// (action = 'op') je Board (F-1157), wenn ABOARD_ACTIVITY_MAX_PER_BOARD fehlt oder ungültig ist.
// Sie greift nur, wo die Frist nicht reicht — ein Mensch erreicht sie in
// 365 Tagen kaum, ein Agent unter Volllast nach gut acht Minuten.
const ActivityMaxProBoardDefault = 100000

// ActivityMaxProBoardMax begrenzt die Env-Var nach oben: Die Grenzsuche in
// kappen überspringt so viele Index-Einträge, wenn sie fällig ist
// (kappenFaellig, alle 1 % der Grenze).
const ActivityMaxProBoardMax = 10000000

const activityMaxProBoardEnvVar = "ABOARD_ACTIVITY_MAX_PER_BOARD"

var maxProBoardWarnungEinmal sync.Once

// activityMaxProBoard liest die Obergrenze aus der Umgebung. Ungültig (keine
// ganze Zahl, < 1, > ActivityMaxProBoardMax) → Voreinstellung, einmal je
// Prozess geloggt wie bei activityRetentionDays (F-1148).
func activityMaxProBoard() int {
	raw := os.Getenv(activityMaxProBoardEnvVar)
	if raw == "" {
		return ActivityMaxProBoardDefault
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > ActivityMaxProBoardMax {
		maxProBoardWarnungEinmal.Do(func() {
			log.Printf("aboard-server: %s=%q ungültig (erwartet ganze Zahl 1–%d), verwende Voreinstellung %d",
				activityMaxProBoardEnvVar, raw, ActivityMaxProBoardMax, ActivityMaxProBoardDefault)
		})
		return ActivityMaxProBoardDefault
	}
	return n
}

// kappenStueck ist die Höchstzahl gelöschter Zeilen je Statement der Kappung
// (F-1195). Variable, damit der Test mit kleinen Stücken prüfen kann.
var kappenStueck = 5000

// kappenNachStueck ist eine Test-Naht (F-1203): Tests fügen hier zwischen zwei
// Stücken Zeilen ein, wie es ein paralleler Schreiber täte. Im Betrieb nil.
var kappenNachStueck func()

// activityTSFormat ist das Format der ts-Spalte seit Migration 0019
// (F-1157): feste Breite, Millisekunden, UTC — dieselbe Form wie
// strftime('%Y-%m-%dT%H:%M:%fZ') in SQLite. Nicht RFC3339Nano: Das lässt
// Null-Nachkommastellen weg, und dann sortiert "…00.5Z" als Zeichenkette vor
// "…00Z" (knowledge/topics/nebenlaeufigkeit-und-lesefenster.md §22, §31).
// Millisekunden genügen: Innerhalb gleicher ts ordnet rowid.
const activityTSFormat = "2006-01-02T15:04:05.000Z"

// activityTS liefert den Zeitstempel für einen neuen Activity-Eintrag. Go
// schneidet bei ".000" ab, rundet also nie in die nächste Sekunde.
func activityTS() string {
	return timeNow().UTC().Format(activityTSFormat)
}

// ActivityEntry ist ein Eintrag im server-seitigen Activity-Log.
// Schema analog ADR-0037 §Variante-A, ergänzt um board_id und note, seit
// Migration 0016 (Karte #554) um card_id/op_type/actor_kind.
type ActivityEntry struct {
	ID      string // UUID
	BoardID string
	Actor   string // Email aus Forward-Auth, oder '' (Legacy-Bypass)
	// F-832 (b): "board-deleted" stand hier bis Sprint 61 weiter als gültiger
	// Wert; seit F-259 kann ihn nichts mehr erzeugen. Ein Leser, der die
	// Liste für den Wertebereich hält, sucht nach Einträgen, die es nicht
	// gibt — oder baut eine Verzweigung für einen toten Fall.
	Action    string // "board-created" | "board-updated" | "acl-set" | "acl-deleted" | "op"
	Field     string // Feld-Name bei field-updates, sonst ''
	FromVal   string // Vorher-Wert als String, sonst ''
	ToVal     string // Nachher-Wert als String, sonst ''
	Note      string // freier Ergänzungstext (z. B. board-title bei create)
	TS        string // ISO-8601-Zeitstempel, seit Migration 0019 feste Breite (activityTSFormat)
	CardID    string // betroffene Karte bei action="op" (sonst '')
	OpType    string // Op-Typ bei action="op", z. B. "card.field" (sonst '')
	ActorKind string // "mensch" | "agent" | "system" (Default "mensch")
	// Karte #608: für wen ein Agent handelte, und in welcher Sitzung.
	OnBehalfOf string
	Session    string
}

// ActivityRepo bündelt die Zugriffe auf die activities-Tabelle.
type ActivityRepo struct {
	db *sql.DB

	// seitKappung zählt je Board die Einträge seit der letzten Prüfung der
	// Obergrenze (kappenFaellig, F-1157). Nur Arbeitsspeicher: Nach einem
	// Neustart prüft der erste Schreibvorgang je Board sofort.
	kappMu      sync.Mutex
	seitKappung map[string]int
}

// NewActivityRepo erzeugt einen neuen ActivityRepo.
func NewActivityRepo(db *sql.DB) *ActivityRepo {
	return &ActivityRepo{db: db, seitKappung: map[string]int{}}
}

// Record schreibt einen neuen Activity-Eintrag (card_id/op_type bleiben leer,
// actor_kind "mensch" — für Board-/ACL-Mutationen, die keiner Karte und
// keiner Op zugeordnet sind) und trimmt danach auf die Frist. Schlägt der
// Trim fehl (nicht-kritisch), wird nur geloggt — der Insert selbst gilt als
// erfolgreich.
//
// board_id darf nicht leer sein; alle anderen Felder sind optional.
// actor = "" (leerer String) signalisiert Legacy-Bypass (kein Forward-Auth — kein Crash).
func (r *ActivityRepo) Record(ctx context.Context, boardID, actor, action, field, fromVal, toVal, note string) error {
	return r.RecordArt(ctx, boardID, actor, "mensch", action, field, fromVal, toVal, note)
}

// RecordArt ist Record mit Akteur-Art ("mensch" | "agent" | "system") — für
// Einträge, die ein Agent oder der Server selbst auslöst, ohne Op (Karte #783:
// Vorschlag angelegt, abgelaufen; Kritik K-A4).
func (r *ActivityRepo) RecordArt(ctx context.Context, boardID, actor, art, action, field, fromVal, toVal, note string) error {
	if art != "agent" && art != "system" {
		art = "mensch"
	}
	if boardID == "" {
		return fmt.Errorf("activities.Record: boardID darf nicht leer sein")
	}
	if action == "" {
		return fmt.Errorf("activities.Record: action darf nicht leer sein")
	}

	id := uuid.New().String()
	ts := activityTS()

	const insQ = `INSERT INTO activities (id, board_id, actor, action, field, from_val, to_val, note, ts, card_id, op_type, actor_kind)
	               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?)`
	if _, err := r.db.ExecContext(ctx, insQ, id, boardID, actor, action, field, fromVal, toVal, note, ts, art); err != nil {
		return fmt.Errorf("activities.Record insert: %w", err)
	}

	// Frist-Trim (ADR-0129 §5): alte Einträge des Boards löschen.
	// Fehler beim Trimming wird bewusst ignoriert (nicht-kritisch — der Insert ist bereits committed).
	_ = r.trim(ctx, boardID, 1)

	return nil
}

// OpActivityEntry ist ein Eintrag im RecordOps-Batch (ADR-0129 §5,
// Karte #554): eine Op aus einem POST /boards/{id}/ops-Batch, protokolliert
// als eigener Activity-Eintrag statt als Teil eines Sammel-Eintrags.
type OpActivityEntry struct {
	CardID    string // betroffene Karte, '' wenn die Op keine Karte trifft
	OpType    string // Op-Typ, z. B. "card.field"
	ActorKind string // "mensch" | "agent" | "system" — '' wird zu "mensch" normalisiert
	// Karte #608 (ADR-0130 §6): für wen ein Agent handelte (E-Mail, '' bei
	// Projekt-Tokens und Menschen) und in welcher Sitzung ('' = unbekannt).
	OnBehalfOf string
	Session    string
}

// RecordOps ist RecordOpsIn mit eigener Transaktion und Trim — für Aufrufer
// ohne offene Board-Transaktion. Der Ops-Endpunkt benutzt sie seit F-1156
// NICHT mehr (er schreibt über ApplyOps/imSchreibvorgang); heute rufen sie nur
// Tests, die Einfüge-Logik und Atomarität prüfen (F-1187).
//
// RecordOps schreibt für einen Op-Batch je Op einen eigenen Activity-Eintrag
// (action="op") in EINER Transaktion — entweder wirken alle Inserts, oder
// keiner. actor gilt für den gesamten Batch (der Aufrufer des Endpunkts,
// nicht je Op — Ops selbst tragen keine eigene Identität am Server).
//
// Der Frist-Trim läuft wie bei Record() NACH dem Commit und nicht-kritisch:
// ein Batch mit vielen Ops soll nicht an einem fehlschlagenden Trim scheitern.
func (r *ActivityRepo) RecordOps(ctx context.Context, boardID, actor string, entries []OpActivityEntry) error {
	if boardID == "" {
		return fmt.Errorf("activities.RecordOps: boardID darf nicht leer sein")
	}
	if len(entries) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("activities.RecordOps: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := RecordOpsIn(ctx, tx, boardID, actor, entries); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("activities.RecordOps commit: %w", err)
	}
	committed = true

	// Frist-Trim (ADR-0129 §5): nicht-kritisch, analog Record().
	_ = r.trim(ctx, boardID, len(entries))

	return nil
}

// RecordOpsIn schreibt die Einträge eines Op-Batches in eine fremde, bereits
// offene Transaktion (F-1156): ApplyOps ruft das vor seinem COMMIT, damit
// Board und Protokoll nur gemeinsam wirksam werden. Kein Trim — der gehört
// hinter den COMMIT (Trim bleibt nicht-kritisch, siehe TrimNach).
func RecordOpsIn(ctx context.Context, ex interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, boardID, actor string, entries []OpActivityEntry) error {
	if boardID == "" {
		return fmt.Errorf("activities.RecordOpsIn: boardID darf nicht leer sein")
	}
	const insQ = `INSERT INTO activities (id, board_id, actor, action, field, from_val, to_val, note, ts, card_id, op_type, actor_kind, on_behalf_of, session)
	               VALUES (?, ?, ?, 'op', '', '', '', '', ?, ?, ?, ?, ?, ?)`
	ts := activityTS()
	for _, e := range entries {
		actorKind := e.ActorKind
		if actorKind == "" {
			actorKind = "mensch"
		}
		if _, err := ex.ExecContext(ctx, insQ, uuid.New().String(), boardID, actor, ts, e.CardID, e.OpType, actorKind,
			NormalizeEmail(e.OnBehalfOf), e.Session); err != nil {
			return fmt.Errorf("activities.RecordOps insert: %w", err)
		}
	}
	return nil
}

// TrimNach räumt nach einem RecordOpsIn die Frist ab — nicht-kritisch, nach
// dem COMMIT des Aufrufers. neu ist die Zahl der eben geschriebenen Einträge
// (für die Obergrenze, kappenFaellig).
func (r *ActivityRepo) TrimNach(ctx context.Context, boardID string, neu int) {
	_ = r.trim(ctx, boardID, neu)
}

// Die Lösch- und Leseabfragen stehen als Konstanten bzw. in
// activityListeQuery, damit TestActivityRepo_AbfragenNutzenIndex genau diese
// Texte mit EXPLAIN QUERY PLAN prüft und keine Kopie davon (F-1157).
const (
	// activityFristQ: Frist-Löschung über den Index (board_id, ts), siehe trim.
	activityFristQ = `DELETE FROM activities WHERE board_id = ? AND ts < ?`
	// activityGrenzeQ: erster Op-Eintrag jenseits der Obergrenze, siehe
	// kappen. Geordnet nach rowid, also nach Einfügereihenfolge (F-1212);
	// `action = 'op'` wählt den Teilindex idx_activities_op_rowid (Migration
	// 0021), der nach (board_id, rowid) sortiert ist.
	activityGrenzeQ = `SELECT rowid FROM activities WHERE board_id = ? AND action = 'op'
	                    ORDER BY rowid DESC LIMIT 1 OFFSET ?`
	// activityKappenQ löscht ein Stück von höchstens LIMIT Zeilen (F-1195):
	// alle Op-Einträge des Boards bis einschließlich der Grenz-rowid. Zeilen,
	// die während der Stücke eingefügt werden, liegen über jeder vorhandenen
	// rowid und damit über der Grenze — gleich, welchen ts sie tragen (F-1203,
	// F-1212).
	activityKappenQ = `DELETE FROM activities WHERE rowid IN (
	                    SELECT rowid FROM activities WHERE board_id = ? AND action = 'op' AND rowid <= ? LIMIT ?)`
)

// trim löscht Einträge des Boards, die älter als die konfigurierte Frist
// sind (activityRetentionDays, ADR-0129 §5 — ersetzt die frühere Kappung auf
// ActivityCap=500 Einträge, F-159), und kappt danach auf die
// Sicherheits-Obergrenze (activityMaxProBoard, F-1157).
//
// Vergleich `ts < stichtag` direkt auf der Spalte, damit der Index
// (board_id, ts) greift. Bis F-1157 stand hier substr(ts, 1, 19): SQLite
// suchte dann nur über board_id und prüfte jeden Eintrag des Boards — unter
// der Schreibsperre, bei jedem Record/RecordOps. Der Stichtag hat 19 Zeichen
// ("YYYY-MM-DDTHH:MM:SS"), ts hat 24 (activityTSFormat). Bei gleichen ersten
// 19 Zeichen ist der längere String der größere: Einträge in der
// Stichtags-Sekunde bleiben, frühere Sekunden fallen — dieselbe
// Sekunden-Semantik wie der frühere substr-Vergleich. Sie gilt auch für
// Altbestand, den Migration 0019 nicht normalisiert hat, solange er mit
// "YYYY-MM-DDTHH:MM:SS" beginnt.
func (r *ActivityRepo) trim(ctx context.Context, boardID string, neu int) error {
	// F-1192: nach Frist nur mit gestellter Uhr löschen (server/uhr). Die
	// Kappung darunter hängt nicht an der Uhr — sie zählt Einträge.
	if cutoff, ok := fristStichtag(activityRetentionDays()); ok && uhr.Erlaubt("activities-frist", nil) {
		if _, err := r.db.ExecContext(ctx, activityFristQ, boardID, cutoff); err != nil {
			return fmt.Errorf("activities.trim: %w", err)
		}
	}
	max := activityMaxProBoard()
	if !r.kappenFaellig(boardID, neu, max) {
		return nil
	}
	return r.kappen(ctx, boardID, max)
}

// kappenFaellig zählt neu Einträge auf das Board und meldet, ob die
// Obergrenze jetzt geprüft wird: beim ersten Schreibvorgang je Board seit dem
// Start, danach jeweils nach max/100 neuen Einträgen (mindestens einem).
// Grund (Messung Karte #582): Die Grenzsuche in kappen überspringt max
// Index-Einträge, bei 100000 rund 5 ms je Aufruf, bei 1 Mio. rund 50 ms. Je
// Schreibvorgang wäre das die Hälfte der Kosten, die F-1157 beseitigt.
// Der Preis: Ein Board kann die Grenze um bis zu 1 % plus einen Batch
// überschreiten, bevor gekappt wird.
func (r *ActivityRepo) kappenFaellig(boardID string, neu, max int) bool {
	intervall := max / 100
	if intervall < 1 {
		intervall = 1
	}
	r.kappMu.Lock()
	defer r.kappMu.Unlock()
	bisher, bekannt := r.seitKappung[boardID]
	if bekannt && bisher+neu < intervall {
		r.seitKappung[boardID] = bisher + neu
		return false
	}
	r.seitKappung[boardID] = 0
	return true
}

// kappen behält die zuletzt eingefügten max Op-Einträge des Boards und löscht
// die früheren (F-1157); alle anderen Einträge bleiben unberührt (Kritiker
// Sprint 74, ACL-Historie). Jede Kappung wird geloggt — sie löscht Protokoll.
// Zuerst eine Leseabfrage, die keine Schreibsperre nimmt (WAL): Sie sucht
// über den Index rückwärts den ersten Eintrag jenseits der Grenze. Nur wenn es
// ihn gibt, folgt das DELETE — unter der Grenze, dem Normalfall, wird nichts
// gesperrt.
//
// Geordnet wird nach rowid, nicht nach ts (F-1212): Die Kappung zählt
// Einträge und darf nicht an der Uhr hängen. Nach ts sortierte eine mit
// rückwärts gestellter Uhr geschriebene Zeile als älteste und fiel beim
// nächsten fälligen Lauf, obwohl sie die neueste war. Bei laufender Uhr sind
// beide Ordnungen gleich; nur dann stimmt auch „gelöscht wird, was im
// Lese-Endpunkt (ts, dann rowid) hinter dem max-ten Eintrag stünde“.
func (r *ActivityRepo) kappen(ctx context.Context, boardID string, max int) error {
	var grenzeRowid int64
	err := r.db.QueryRowContext(ctx, activityGrenzeQ, boardID, max).Scan(&grenzeRowid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("activities.kappen grenze: %w", err)
	}
	// In Stücken (F-1195): Jedes Stück ist ein eigenes kurzes Statement, andere
	// Schreiber kommen dazwischen zum Zug. Vorher löschte nach einer Senkung
	// der Grenze ein einziges DELETE ~99000 Zeilen unter der Schreibsperre.
	var gesamt int64
	for ctx.Err() == nil {
		res, err := r.db.ExecContext(ctx, activityKappenQ, boardID, grenzeRowid, kappenStueck)
		if err != nil {
			return fmt.Errorf("activities.kappen: %w", err)
		}
		n, _ := res.RowsAffected()
		gesamt += n
		if n < int64(kappenStueck) {
			break
		}
		if kappenNachStueck != nil {
			kappenNachStueck()
		}
	}
	if gesamt > 0 {
		log.Printf("aboard-server: activities board=%s: Obergrenze %d Op-Einträge (%s) erreicht — %d älteste gelöscht",
			boardID, max, activityMaxProBoardEnvVar, gesamt)
	}
	return nil
}

// List gibt die neuesten Einträge eines Boards zurück (neueste zuerst).
// limit <= 0 → alle (nicht empfohlen bei großen Boards). Dünner Wrapper um
// ListFiltered ohne Karten-Filter — Bestandssignatur bleibt unverändert
// (activity_test.go und andere Aufrufer).
func (r *ActivityRepo) List(ctx context.Context, boardID string, limit int) ([]ActivityEntry, error) {
	return r.ListFiltered(ctx, boardID, "", limit)
}

// activityListeQuery baut die Leseabfrage für ListGefiltert.
func activityListeQuery(boardID, cardID string, limit int, ohneACL bool) (string, []any) {
	return activityListeQueryMit(boardID, cardID, limit, ohneACL, false)
}

// activityListeQueryMit: wie activityListeQuery, ohneVorschlaege blendet die
// Einträge zu Vorschlägen des internen Agenten aus (action vorschlag-*, Karte
// #783). Offene Vorschläge sind für Agenten und Viewer unsichtbar (Vertrag Push-Skript, Ziffer 9.6
// (b)); dazu gehört, dass sie nicht über das Protokoll von ihrer Existenz
// erfahren (Kritik K-A4).
func activityListeQueryMit(boardID, cardID string, limit int, ohneACL, ohneVorschlaege bool) (string, []any) {
	const cols = `id, board_id, actor, action, field, from_val, to_val, note, ts, card_id, op_type, actor_kind, on_behalf_of, session`
	q := `SELECT ` + cols + ` FROM activities WHERE board_id = ?`
	args := []any{boardID}
	if cardID != "" {
		q += ` AND card_id = ?`
		args = append(args, cardID)
	}
	if ohneACL {
		q += ` AND action NOT LIKE 'acl-%'`
	}
	if ohneVorschlaege {
		q += ` AND action NOT LIKE 'vorschlag-%'`
	}
	q += ` ORDER BY ts DESC, rowid DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return q, args
}

// ListFiltered gibt die neuesten Einträge eines Boards zurück (neueste
// zuerst), optional auf eine Karte eingeschränkt (cardID != "" — Karte #554,
// GET /boards/{id}/activities?card=...). limit <= 0 → alle.
//
// Sortierung (F-1147, F-1157): ts hat seit Migration 0019 feste Breite, die
// Zeichenkettenordnung ist also die Zeitordnung. RecordOps gibt allen Ops
// eines Batches denselben ts; innerhalb gleicher ts entscheidet die
// Einfügereihenfolge (rowid) — der Server stempelt beim Einfügen.
// `ORDER BY ts DESC, rowid DESC` ist genau der Rückwärtslauf über den Index
// (board_id, ts) bzw. (board_id, card_id, ts), ohne Sortierschritt. Bis
// F-1157 sortierte jeder Aufruf alle Einträge des Boards (TEMP B-TREE).
//
// ⚠️ Liefert ALLES, auch acl-* und vorschlag-*. Für Antworten an Menschen oder
// Agenten ListGefiltert nehmen (Verify V-6, Karte #783).
func (r *ActivityRepo) ListFiltered(ctx context.Context, boardID, cardID string, limit int) ([]ActivityEntry, error) {
	return r.ListGefiltertMit(ctx, boardID, cardID, limit, false, false)
}

// ListGefiltert ist ListFiltered mit einem zusätzlichen Filter: ohneACL
// blendet die Einträge `acl-*` aus (Kritiker Sprint 71). Sie tragen E-Mail
// und Rolle der Zugriffsberechtigten, und die ACL-Liste selbst darf nur der
// Owner lesen (F-261). Gefiltert wird in SQL, damit `limit` weiter die Zahl
// der gelieferten Einträge ist.
//
// Einträge zu Vorschlägen (vorschlag-*, Karte #783) blendet ListGefiltert
// IMMER aus — sicher als Standard für jeden künftigen Aufrufer (Verify V-6).
// Wer sie zeigen darf (Personen ab editor), ruft ListGefiltertMit.
func (r *ActivityRepo) ListGefiltert(ctx context.Context, boardID, cardID string, limit int, ohneACL bool) ([]ActivityEntry, error) {
	return r.ListGefiltertMit(ctx, boardID, cardID, limit, ohneACL, true)
}

// ListGefiltertMit: wie ListGefiltert, ohneVorschlaege blendet die Einträge
// zu Vorschlägen aus (activityListeQueryMit).
func (r *ActivityRepo) ListGefiltertMit(ctx context.Context, boardID, cardID string, limit int, ohneACL, ohneVorschlaege bool) ([]ActivityEntry, error) {
	q, args := activityListeQueryMit(boardID, cardID, limit, ohneACL, ohneVorschlaege)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("activities.ListFiltered: %w", err)
	}
	defer rows.Close()

	var out []ActivityEntry
	for rows.Next() {
		var e ActivityEntry
		if err := rows.Scan(&e.ID, &e.BoardID, &e.Actor, &e.Action,
			&e.Field, &e.FromVal, &e.ToVal, &e.Note, &e.TS,
			&e.CardID, &e.OpType, &e.ActorKind, &e.OnBehalfOf, &e.Session); err != nil {
			return nil, fmt.Errorf("activities.ListFiltered scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("activities.ListFiltered rows: %w", err)
	}
	return out, nil
}
