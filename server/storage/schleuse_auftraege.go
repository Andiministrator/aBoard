package storage

// schleuse_auftraege.go — Auftragsdatensätze der Ausgangsschleuse, Teil S2
// (ADR-0134 §3.1–§3.4, §4; Karte #685).
//
// Ein Auftrag hält den PSEUDONYMISIERTEN Auszug, dessen Hash und — mit
// secretcrypto.Box verschlüsselt — die Zuordnung Platzhalter → Original. Die
// Zuordnung verlässt dieses Repo nur über Zuordnung(), das ausschliesslich
// für die Re-Identifizierung der Antwort in S3 gedacht ist. Kein Feld von
// SchleuseAuftrag trägt sie; eine JSON-Antwort mit einem Auftrag kann sie
// deshalb nicht versehentlich enthalten.
//
// Zugriff nur durch die Person: Jede Methode, die einen Auftrag liest oder
// ändert, verlangt die user_id und behandelt einen fremden Auftrag wie einen
// nicht vorhandenen (ErrSchleuseAuftragNichtGefunden) — kein Existenz-Orakel.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/uhr"
)

var (
	// ErrSchleuseAuftragNichtGefunden: kein Auftrag mit dieser ID — oder er
	// gehört einer anderen Person.
	ErrSchleuseAuftragNichtGefunden = errors.New("storage: schleuse-auftrag nicht gefunden")
	// ErrSchleuseAuftragNichtEntwurf: Der Auftrag ist schon gesendet (oder
	// danach) und lässt sich weder bearbeiten noch löschen.
	ErrSchleuseAuftragNichtEntwurf = errors.New("storage: schleuse-auftrag ist kein entwurf")
	// ErrSchleuseAuftragVeraltet: Der Text wurde inzwischen geändert (anderer
	// Hash als der, von dem die Bearbeitung ausging).
	ErrSchleuseAuftragVeraltet = errors.New("storage: schleuse-auftrag wurde inzwischen geändert")
	// ErrSchleuseZuVieleEntwuerfe: Die Person hat schon so viele offene
	// Aufträge, wie AnlegenGeprueft zulässt.
	ErrSchleuseZuVieleEntwuerfe = errors.New("storage: zu viele offene schleuse-aufträge")
	// ErrSchleuseBoardGesperrt: Das Board ist für die Ausgangsschleuse
	// gesperrt (ADR-0134 §2).
	ErrSchleuseBoardGesperrt = errors.New("storage: board ist für die ausgangsschleuse gesperrt")
	// ErrSchleuseBildUnbekannt: Eine Bildfreigabe nennt ein Bild, das nicht
	// im Auftrag steht (Karte #687).
	ErrSchleuseBildUnbekannt = errors.New("storage: bild steht nicht im schleuse-auftrag")
)

// Zustände eines Auftrags (ADR-0134 §3.5). Die Übergänge nach „entwurf“
// stehen in schleuse_versand.go (S3, Karte #686).
const (
	SchleuseZustandEntwurf        = "entwurf"
	SchleuseZustandFreigegeben    = "freigegeben"
	SchleuseZustandGesendet       = "gesendet"
	SchleuseZustandBeantwortet    = "beantwortet"
	SchleuseZustandFehlgeschlagen = "fehlgeschlagen"
)

// schleuseZeitFormat: feste Breite, damit der Textvergleich in SQL (Frist
// der Entwürfe) stimmt — nowISO (RFC3339Nano) kürzt Nullen weg.
const schleuseZeitFormat = "2006-01-02T15:04:05.000Z"

func schleuseJetzt() string { return timeNow().UTC().Format(schleuseZeitFormat) }

// SchleuseTeile hält fest, welche Teile der Karte im Auszug stecken — ohne
// Inhalt (für Protokoll und Oberfläche).
type SchleuseTeile struct {
	Beschreibung bool     `json:"beschreibung"`
	Kommentare   []string `json:"kommentare"`
	Aufgabe      bool     `json:"aufgabe"`
	// Dateien: Anhang-ids der Text- und PDF-Dateien, deren Text im Auszug
	// steht (Karte #687). Bilder stehen in SchleuseAuftrag.Bilder.
	Dateien []string `json:"dateien"`
}

// SchleuseAuftrag ist ein Auftragsdatensatz OHNE Zuordnung. user_id und
// E-Mail tragen keinen JSON-Namen: Die Antwort geht ohnehin nur an die
// Person selbst, und was sie nicht braucht, steht nicht darin.
type SchleuseAuftrag struct {
	ID         string        `json:"id"`
	BoardID    string        `json:"board_id"`
	CardID     string        `json:"card_id"`
	UserID     string        `json:"-"`
	UserEmail  string        `json:"-"`
	FreigabeID string        `json:"freigabe_id"`
	Anbieter   string        `json:"anbieter"`
	Modell     string        `json:"modell"`
	Zustand    string        `json:"zustand"`
	Teile      SchleuseTeile `json:"teile"`
	Text       string        `json:"text"`
	// SHA256 ist der Hash des Auftrags (schleuse.AuszugHash): Text und
	// freigegebene Bilder; ohne Bild der Hash des Texts.
	SHA256      string               `json:"sha256"`
	Ersetzungen []schleuse.Ersetzung `json:"ersetzungen"`
	Freigegeben bool                 `json:"freigegeben"`
	ErstelltAm  string               `json:"erstellt_am"`
	GeaendertAm string               `json:"geaendert_am"`

	// S3 (Karte #686, Migration 0028): Freigabe, Versand, Antwort.
	FreigabeSHA256         string   `json:"freigabe_sha256,omitempty"`
	FreigegebenVon         string   `json:"freigegeben_von,omitempty"`
	FreigegebenAm          string   `json:"freigegeben_am,omitempty"`
	VierAugen              bool     `json:"vier_augen"`
	ZweitFreigegebenVon    string   `json:"zweit_freigegeben_von,omitempty"`
	ZweitFreigegebenAm     string   `json:"zweit_freigegeben_am,omitempty"`
	GesendetAm             string   `json:"gesendet_am,omitempty"`
	AbgeschlossenAm        string   `json:"abgeschlossen_am,omitempty"`
	AntwortText            string   `json:"antwort_text,omitempty"`
	AntwortSHA256          string   `json:"antwort_sha256,omitempty"`
	KommentarID            string   `json:"kommentar_id,omitempty"`
	KommentarSHA256        string   `json:"-"`
	VeraendertePlatzhalter []string `json:"veraenderte_platzhalter"`
	FehlerGrund            string   `json:"fehler_grund,omitempty"`
	InputTokens            int      `json:"input_tokens"`
	OutputTokens           int      `json:"output_tokens"`
	KostenUSD              float64  `json:"kosten_usd"`
	FinishReason           string   `json:"finish_reason,omitempty"`

	// S4 (Karte #687, Migration 0029): gewählte Bilder, bereinigt gehasht —
	// ohne Inhalt und Dateinamen.
	Bilder []schleuse.BildEintrag `json:"bilder"`
}

// SchleuseAuftragRepo verwaltet schleuse_auftraege. box darf nicht nil sein;
// ohne Master-Key bringt main.go das Repo nicht auf (Routen → 503).
type SchleuseAuftragRepo struct {
	db  *sql.DB
	box *secretcrypto.Box
}

// NewSchleuseAuftragRepo legt das Repo an. box muss != nil sein.
func NewSchleuseAuftragRepo(db *sql.DB, box *secretcrypto.Box) *SchleuseAuftragRepo {
	return &SchleuseAuftragRepo{db: db, box: box}
}

const auftragSpalten = `id, board_id, card_id, user_id, user_email, freigabe_id, anbieter, modell, zustand,
	teile, text, text_sha256, ersetzungen, freigabe_sha256 IS NOT NULL, erstellt_am, geaendert_am,
	freigabe_sha256, freigegeben_von, freigegeben_am, vier_augen, zweit_freigegeben_von, zweit_freigegeben_am,
	gesendet_am, abgeschlossen_am, antwort_text, antwort_sha256, kommentar_id, kommentar_sha256,
	veraenderte_platzhalter, fehler_grund, input_tokens, output_tokens, kosten_usd, finish_reason, bilder`

func scanAuftrag(z zeilenLeser) (SchleuseAuftrag, error) {
	var a SchleuseAuftrag
	var teile, ersetzungen, veraendert, bilder string
	var freigegeben, vierAugen int
	var fSHA, fVon, fAm, zVon, zAm, gAm, abAm, antw, antwSHA, kID, kSHA, grund, finish sql.NullString
	if err := z.Scan(&a.ID, &a.BoardID, &a.CardID, &a.UserID, &a.UserEmail, &a.FreigabeID, &a.Anbieter,
		&a.Modell, &a.Zustand, &teile, &a.Text, &a.SHA256, &ersetzungen, &freigegeben,
		&a.ErstelltAm, &a.GeaendertAm,
		&fSHA, &fVon, &fAm, &vierAugen, &zVon, &zAm, &gAm, &abAm, &antw, &antwSHA, &kID, &kSHA,
		&veraendert, &grund, &a.InputTokens, &a.OutputTokens, &a.KostenUSD, &finish, &bilder); err != nil {
		return a, err
	}
	a.Freigegeben = freigegeben == 1
	a.VierAugen = vierAugen == 1
	a.FreigabeSHA256, a.FreigegebenVon, a.FreigegebenAm = fSHA.String, fVon.String, fAm.String
	a.ZweitFreigegebenVon, a.ZweitFreigegebenAm = zVon.String, zAm.String
	a.GesendetAm, a.AbgeschlossenAm = gAm.String, abAm.String
	a.AntwortText, a.AntwortSHA256 = antw.String, antwSHA.String
	a.KommentarID, a.KommentarSHA256 = kID.String, kSHA.String
	a.FehlerGrund, a.FinishReason = grund.String, finish.String
	if json.Unmarshal([]byte(veraendert), &a.VeraendertePlatzhalter) != nil || a.VeraendertePlatzhalter == nil {
		a.VeraendertePlatzhalter = []string{}
	}
	_ = json.Unmarshal([]byte(teile), &a.Teile)
	if a.Teile.Kommentare == nil {
		a.Teile.Kommentare = []string{}
	}
	if a.Teile.Dateien == nil {
		a.Teile.Dateien = []string{}
	}
	if err := json.Unmarshal([]byte(ersetzungen), &a.Ersetzungen); err != nil || a.Ersetzungen == nil {
		a.Ersetzungen = []schleuse.Ersetzung{}
	}
	if err := json.Unmarshal([]byte(bilder), &a.Bilder); err != nil || a.Bilder == nil {
		a.Bilder = []schleuse.BildEintrag{}
	}
	return a, nil
}

// Anlegen speichert einen neuen Entwurf OHNE Prüfung von Board, Sperre und
// Obergrenze — für Tests und Werkzeuge. Der Weg über die API ist
// AnlegenGeprueft.
func (r *SchleuseAuftragRepo) Anlegen(ctx context.Context, a SchleuseAuftrag, zuordnung map[string]string) (SchleuseAuftrag, error) {
	return r.anlegen(ctx, a, zuordnung, 0)
}

// AnlegenGeprueft speichert einen neuen Entwurf nur, wenn — im SELBEN
// Schreibvorgang geprüft — das Board noch existiert, nicht für die Schleuse
// gesperrt ist und die Person weniger als hoechst offene Aufträge
// (entwurf, freigegeben) hat (Kritiker KS-5/KS-6). Ein vorgelagerter
// Zähl-Test im Handler genügt nicht: Zwischen Zählen und Speichern rechnet
// das lokale Modell Minuten, und parallele Anfragen zählten alle denselben
// Stand. Hier ist es EINE Anweisung (INSERT … SELECT … WHERE), die SQLite
// unter der Schreibsperre ausführt — zwei Anlagen können nicht beide den
// letzten freien Platz sehen, und ein inzwischen gelöschtes oder gesperrtes
// Board bekommt keine Zeile mehr.
//
// Fehler: ErrBoardNotFound, ErrSchleuseBoardGesperrt, ErrSchleuseZuVieleEntwuerfe.
func (r *SchleuseAuftragRepo) AnlegenGeprueft(ctx context.Context, a SchleuseAuftrag, zuordnung map[string]string, hoechst int) (SchleuseAuftrag, error) {
	if hoechst < 1 {
		return a, fmt.Errorf("schleuse-aufträge.anlegen: hoechst muss >= 1 sein")
	}
	return r.anlegen(ctx, a, zuordnung, hoechst)
}

// anlegen: text muss normalisiert sein (schleuse.Normalisiere); der Hash
// wird HIER berechnet, damit gespeicherter Text und Hash nicht
// auseinanderlaufen können. zuordnung wird verschlüsselt. hoechst 0 = ohne
// Prüfung.
func (r *SchleuseAuftragRepo) anlegen(ctx context.Context, a SchleuseAuftrag, zuordnung map[string]string, hoechst int) (SchleuseAuftrag, error) {
	if strings.TrimSpace(a.UserID) == "" || strings.TrimSpace(a.BoardID) == "" || strings.TrimSpace(a.CardID) == "" {
		return a, fmt.Errorf("schleuse-aufträge.anlegen: user_id, board_id und card_id Pflicht")
	}
	if zuordnung == nil {
		zuordnung = map[string]string{}
	}
	roh, err := json.Marshal(zuordnung)
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.anlegen zuordnung: %w", err)
	}
	enc, err := r.box.Seal(roh)
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.anlegen seal: %w", err)
	}
	if a.Teile.Kommentare == nil {
		a.Teile.Kommentare = []string{}
	}
	if a.Teile.Dateien == nil {
		a.Teile.Dateien = []string{}
	}
	if a.Ersetzungen == nil {
		a.Ersetzungen = []schleuse.Ersetzung{}
	}
	if a.Bilder == nil {
		a.Bilder = []schleuse.BildEintrag{}
	}
	teile, _ := json.Marshal(a.Teile)
	ersetzungen, _ := json.Marshal(a.Ersetzungen)
	bilder, _ := json.Marshal(a.Bilder)
	a.ID = uuid.NewString()
	a.Zustand = SchleuseZustandEntwurf
	a.SHA256 = schleuse.AuszugHash(a.Text, a.Bilder)
	a.Freigegeben = false
	a.VeraendertePlatzhalter = []string{}
	a.ErstelltAm = schleuseJetzt()
	a.GeaendertAm = a.ErstelltAm
	werte := []any{a.ID, a.BoardID, a.CardID, a.UserID, a.UserEmail, a.FreigabeID, a.Anbieter, a.Modell,
		a.Zustand, string(teile), a.Text, a.SHA256, string(ersetzungen), enc, a.ErstelltAm, a.GeaendertAm, string(bilder)}
	const spalten = `INSERT INTO schleuse_auftraege (id, board_id, card_id, user_id, user_email, freigabe_id, anbieter, modell,
		   zustand, teile, text, text_sha256, ersetzungen, zuordnung_verschluesselt, erstellt_am, geaendert_am, bilder)`
	if hoechst == 0 {
		if _, err := r.db.ExecContext(ctx, spalten+` VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, werte...); err != nil {
			return a, fmt.Errorf("schleuse-aufträge.anlegen: %w", err)
		}
		return a, nil
	}
	res, err := r.db.ExecContext(ctx, spalten+` SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		 WHERE EXISTS (SELECT 1 FROM boards WHERE id = ? AND schleuse_gesperrt = 0)
		   AND (SELECT COUNT(*) FROM schleuse_auftraege WHERE user_id = ? AND zustand IN (?, ?)) < ?`,
		append(werte, a.BoardID, a.UserID, SchleuseZustandEntwurf, SchleuseZustandFreigegeben, hoechst)...)
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.anlegen: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return a, fmt.Errorf("schleuse-aufträge.anlegen affected: %w", err)
	} else if n == 1 {
		return a, nil
	}
	// Keine Zeile: Grund nachlesen (nur für die Meldung — geschrieben ist
	// nichts, und das bleibt so, auch wenn sich der Grund inzwischen ändert).
	gesperrt, err := SchleuseBoardSperre(ctx, r.db, a.BoardID)
	switch {
	case err != nil:
		return a, err
	case gesperrt:
		return a, ErrSchleuseBoardGesperrt
	default:
		return a, ErrSchleuseZuVieleEntwuerfe
	}
}

// Get liefert einen Auftrag der Person. Fremde Aufträge: ErrSchleuseAuftragNichtGefunden.
func (r *SchleuseAuftragRepo) Get(ctx context.Context, id, userID string) (SchleuseAuftrag, error) {
	a, err := scanAuftrag(r.db.QueryRowContext(ctx,
		`SELECT `+auftragSpalten+` FROM schleuse_auftraege WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrSchleuseAuftragNichtGefunden
	}
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.get: %w", err)
	}
	return a, nil
}

// ListFuerKarte liefert die Aufträge der Person an einer Karte, neueste zuerst.
func (r *SchleuseAuftragRepo) ListFuerKarte(ctx context.Context, userID, boardID, cardID string) ([]SchleuseAuftrag, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+auftragSpalten+` FROM schleuse_auftraege
		  WHERE user_id = ? AND board_id = ? AND card_id = ? ORDER BY erstellt_am DESC, id`, userID, boardID, cardID)
	if err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.list: %w", err)
	}
	defer rows.Close()
	out := []SchleuseAuftrag{}
	for rows.Next() {
		a, err := scanAuftrag(rows)
		if err != nil {
			return nil, fmt.Errorf("schleuse-aufträge.list scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ZaehleEntwuerfe zählt die offenen Aufträge der Person — Entwürfe UND
// freigegebene, noch nicht gesendete (Kritiker KS-11): Beide halten eine
// Zuordnung, beide zählen gegen die Obergrenze. Dieselbe Menge prüft
// AnlegenGeprueft.
func (r *SchleuseAuftragRepo) ZaehleEntwuerfe(ctx context.Context, userID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schleuse_auftraege WHERE user_id = ? AND zustand IN (?, ?)`,
		userID, SchleuseZustandEntwurf, SchleuseZustandFreigegeben).Scan(&n); err != nil {
		return 0, fmt.Errorf("schleuse-aufträge.zähle: %w", err)
	}
	return n, nil
}

// OffenerAuftrag: Kennung und Ort eines offenen Auftrags (entwurf,
// freigegeben) — für das Aufräumen verwaister Entwürfe (F-1623).
type OffenerAuftrag struct {
	ID, BoardID, CardID string
}

// OffeneAuftraege liefert die offenen Aufträge der Person — dieselbe Menge,
// die ZaehleEntwuerfe zählt.
func (r *SchleuseAuftragRepo) OffeneAuftraege(ctx context.Context, userID string) ([]OffenerAuftrag, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, board_id, card_id FROM schleuse_auftraege WHERE user_id = ? AND zustand IN (?, ?) ORDER BY id`,
		userID, SchleuseZustandEntwurf, SchleuseZustandFreigegeben)
	if err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.offene: %w", err)
	}
	defer rows.Close()
	var out []OffenerAuftrag
	for rows.Next() {
		var o OffenerAuftrag
		if err := rows.Scan(&o.ID, &o.BoardID, &o.CardID); err != nil {
			return nil, fmt.Errorf("schleuse-aufträge.offene scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// LoescheOffene löscht die genannten Aufträge der Person, soweit sie noch
// offen sind (entwurf, freigegeben) — samt Zuordnung. Ein inzwischen
// gesendeter Auftrag bleibt (Protokoll).
func (r *SchleuseAuftragRepo) LoescheOffene(ctx context.Context, userID string, ids []string) (int64, error) {
	var n int64
	for _, id := range ids {
		res, err := r.db.ExecContext(ctx, `DELETE FROM schleuse_auftraege WHERE id = ? AND user_id = ? AND zustand IN (?, ?)`,
			id, userID, SchleuseZustandEntwurf, SchleuseZustandFreigegeben)
		if err != nil {
			return n, fmt.Errorf("schleuse-aufträge.lösche-offene: %w", err)
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}

// bearbeitbar: Zustände, in denen die Person den Text noch ändern oder den
// Auftrag löschen darf — alles, was noch nicht hinausgegangen ist. Eine
// Änderung nach der Freigabe setzt zurück auf „entwurf“ (ADR-0134 §3.3), die
// Freigabe (auch eine zweite) ist danach weg. BeginneVersand prüft deshalb
// Zustand UND Hash in derselben Transaktion, die „gesendet“ setzt.
func bearbeitbar(zustand string) bool {
	return zustand == SchleuseZustandEntwurf || zustand == SchleuseZustandFreigegeben
}

// AendereText ersetzt den Text eines noch nicht gesendeten Auftrags der
// Person (ADR-0134 §3.3) — AendereAuszug ohne Änderung der Bildfreigaben.
func (r *SchleuseAuftragRepo) AendereText(ctx context.Context, id, userID, basisHash, text string) (SchleuseAuftrag, error) {
	return r.AendereAuszug(ctx, id, userID, basisHash, text, nil)
}

// AendereAuszug ersetzt Text und — wenn bilderFrei nicht nil ist — die
// Einzelfreigaben der Bilder (ADR-0134 §3.3, §5): *bilderFrei ist die
// vollständige Liste der Bild-ids, die mitgehen sollen; ein unbekanntes Bild
// ist ErrSchleuseBildUnbekannt. basisHash ist der Hash, von dem die
// Bearbeitung ausging; weicht der gespeicherte ab, ErrSchleuseAuftragVeraltet
// (zweiter Tab). Jede Änderung setzt den Auftrag auf „entwurf“, leert die
// Freigabe-Felder und berechnet den Hash neu. Unveränderter Stand schreibt
// nichts.
func (r *SchleuseAuftragRepo) AendereAuszug(ctx context.Context, id, userID, basisHash, text string, bilderFrei *[]string) (SchleuseAuftrag, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SchleuseAuftrag{}, fmt.Errorf("schleuse-aufträge.ändern begin: %w", err)
	}
	defer tx.Rollback()
	// Erst die Schreibsperre (Leer-UPDATE), dann lesen — wie
	// SchleuseSchluesselRepo.Set: Die Prüfung gilt für genau den Stand, auf
	// den geschrieben wird.
	if _, err := tx.ExecContext(ctx, `UPDATE schleuse_auftraege SET geaendert_am = geaendert_am WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return SchleuseAuftrag{}, fmt.Errorf("schleuse-aufträge.ändern sperre: %w", err)
	}
	a, err := scanAuftrag(tx.QueryRowContext(ctx, `SELECT `+auftragSpalten+` FROM schleuse_auftraege WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrSchleuseAuftragNichtGefunden
	}
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.ändern lesen: %w", err)
	}
	if !bearbeitbar(a.Zustand) {
		return a, ErrSchleuseAuftragNichtEntwurf
	}
	if a.SHA256 != basisHash {
		return a, ErrSchleuseAuftragVeraltet
	}
	bilder := a.Bilder
	if bilderFrei != nil {
		frei := map[string]bool{}
		for _, b := range *bilderFrei {
			frei[b] = true
		}
		bilder = make([]schleuse.BildEintrag, len(a.Bilder))
		for i, b := range a.Bilder {
			b.Freigegeben = frei[b.ID]
			delete(frei, b.ID)
			bilder[i] = b
		}
		if len(frei) > 0 {
			return a, ErrSchleuseBildUnbekannt
		}
	}
	neuHash := schleuse.AuszugHash(text, bilder)
	if neuHash == a.SHA256 && text == a.Text && bilderGleich(bilder, a.Bilder) {
		return a, nil
	}
	bilderJSON, _ := json.Marshal(bilder)
	jetzt := schleuseJetzt()
	if _, err := tx.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET text = ?, text_sha256 = ?, bilder = ?, zustand = ?,
		   freigabe_sha256 = NULL, freigegeben_von = NULL, freigegeben_am = NULL,
		   vier_augen = 0, zweit_freigegeben_von = NULL, zweit_freigegeben_am = NULL, geaendert_am = ?
		 WHERE id = ? AND user_id = ? AND zustand IN ('entwurf', 'freigegeben')`,
		text, neuHash, string(bilderJSON), SchleuseZustandEntwurf, jetzt, id, userID); err != nil {
		return a, fmt.Errorf("schleuse-aufträge.ändern: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return a, fmt.Errorf("schleuse-aufträge.ändern commit: %w", err)
	}
	a.Text, a.SHA256, a.Zustand, a.Freigegeben, a.GeaendertAm = text, neuHash, SchleuseZustandEntwurf, false, jetzt
	a.Bilder = bilder
	a.FreigabeSHA256, a.FreigegebenVon, a.FreigegebenAm, a.VierAugen = "", "", "", false
	a.ZweitFreigegebenVon, a.ZweitFreigegebenAm = "", ""
	return a, nil
}

func bilderGleich(a, b []schleuse.BildEintrag) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Loeschen entfernt einen noch nicht gesendeten Auftrag der Person samt
// Zuordnung. Gesendete Aufträge sind Protokoll und lassen sich nicht löschen
// (sie gehen mit dem Board; bei der Person werden sie pseudonymisiert).
func (r *SchleuseAuftragRepo) Loeschen(ctx context.Context, id, userID string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM schleuse_auftraege WHERE id = ? AND user_id = ? AND zustand IN (?, ?)`,
		id, userID, SchleuseZustandEntwurf, SchleuseZustandFreigegeben)
	if err != nil {
		return fmt.Errorf("schleuse-aufträge.löschen: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSchleuseAuftragNichtGefunden
	}
	return nil
}

// Zuordnung entschlüsselt die Zuordnung Platzhalter → Original — NUR für die
// Re-Identifizierung der Antwort auf dem Server (S3). Nie loggen, nie an den
// Browser. Ist sie nach Ablauf der Frist gelöscht, kommt eine leere Abbildung.
func (r *SchleuseAuftragRepo) Zuordnung(ctx context.Context, id, userID string) (map[string]string, error) {
	var enc sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT zuordnung_verschluesselt FROM schleuse_auftraege WHERE id = ? AND user_id = ?`, id, userID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSchleuseAuftragNichtGefunden
	}
	if err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.zuordnung: %w", err)
	}
	out := map[string]string{}
	if !enc.Valid || enc.String == "" {
		return out, nil
	}
	roh, err := r.box.Open(enc.String)
	if err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.zuordnung decrypt: %w", err)
	}
	if err := json.Unmarshal(roh, &out); err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.zuordnung parse: %w", err)
	}
	return out, nil
}

// EntwuerfeAufraeumen löscht Entwürfe, die seit mehr als tage Tagen nicht
// geändert wurden — samt Zuordnung. Ein nie gesendeter Entwurf hat keine
// Antwort, ab der die Frist aus ADR-0134 §4 liefe; er bekommt deshalb
// dieselbe Frist ab seiner letzten Änderung. tage < 1 tut nichts.
//
// Frist-Löschung nach der Systemuhr: nur mit gestellter Uhr (F-1192,
// uhr.Erlaubt) — sonst löschte ein Pi mit vorgehender Uhr nach einem
// Stromausfall Entwürfe, die noch in der Frist liegen.
func (r *SchleuseAuftragRepo) EntwuerfeAufraeumen(ctx context.Context, tage int) (int64, error) {
	if tage < 1 || !uhr.Erlaubt("schleuse-entwuerfe", nil) {
		return 0, nil
	}
	grenze := timeNow().UTC().Add(-time.Duration(tage) * 24 * time.Hour).Format(schleuseZeitFormat)
	// Karte #686 (Kritiker KS3-5): auch freigegebene, nie gesendete Aufträge
	// — etwa einer, der auf die zweite Freigabe wartet. Sonst hielte er
	// Zuordnung und Freigabe unbegrenzt, und eine zweite Person könnte ihn
	// Monate später noch hinausschicken. geaendert_am setzen Freigabe und
	// zweite Freigabe neu.
	res, err := r.db.ExecContext(ctx, `DELETE FROM schleuse_auftraege WHERE zustand IN (?, ?) AND geaendert_am < ?`,
		SchleuseZustandEntwurf, SchleuseZustandFreigegeben, grenze)
	if err != nil {
		return 0, fmt.Errorf("schleuse-aufträge.aufräumen: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
