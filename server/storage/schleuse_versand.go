package storage

// schleuse_versand.go — Freigabe, Vier-Augen und Versandzustände der
// Ausgangsschleuse, Teil S3 (ADR-0134 §3.4–§3.7, §4; Karte #686).
//
// Ablauf eines Auftrags:
//
//	entwurf ──Freigeben──▶ freigegeben ──BeginneVersand──▶ gesendet ──Abschliessen──▶ beantwortet
//	   ▲                    │  (Vier-Augen: erst ZweitFreigeben)   └──Fehlschlagen──▶ fehlgeschlagen
//	   └── Textänderung, Sperre oder Grenze beim Versand ┘
//
// Jeder Übergang prüft seinen Ausgangszustand in DERSELBEN Anweisung bzw.
// Transaktion, in der er schreibt (Auflage aus #685: Zustand, Hash, Sperre
// und Board-Existenz in einem Schreibvorgang). Die Transaktionen beginnen mit
// einem Leer-UPDATE: Damit hält SQLite die Schreibsperre ab der ersten
// Anweisung, und was danach gelesen wird, gilt für genau den Stand, auf den
// geschrieben wird — wie AendereText.
//
// Es gibt KEINE automatische Wiederholung (ADR-0134 §3.5): Ein Auftrag, der
// einmal „gesendet“ war, kommt nie zurück nach „freigegeben“. Erneut senden
// heisst neuer Auszug, neuer Hash, neue Freigabe. Zurück auf „entwurf“ geht
// nur ein Auftrag, der noch nicht hinausgegangen ist.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/uhr"
)

var (
	// ErrSchleuseGrenzeErreicht: Die Person hat in der letzten Stunde schon
	// so viele Aufträge gesendet, wie ABOARD_SCHLEUSE_AUFTRAEGE_PRO_STUNDE
	// erlaubt.
	ErrSchleuseGrenzeErreicht = errors.New("storage: grenze der gesendeten schleuse-aufträge erreicht")
	// ErrSchleuseNichtSendebereit: Der Auftrag ist nicht (mehr) im Zustand
	// „freigegeben“ — inzwischen geändert, gelöscht oder schon gesendet.
	ErrSchleuseNichtSendebereit = errors.New("storage: schleuse-auftrag ist nicht sendebereit")
	// ErrSchleuseWartetZweitfreigabe: Vier-Augen verlangt eine zweite Person.
	ErrSchleuseWartetZweitfreigabe = errors.New("storage: schleuse-auftrag wartet auf die zweite freigabe")
	// ErrSchleuseSelbstfreigabe: Die zweite Freigabe kommt von der
	// antragstellenden Person.
	ErrSchleuseSelbstfreigabe = errors.New("storage: zweite freigabe durch die antragstellende person")
	// ErrSchleuseNichtZweitfreigebbar: Der Auftrag wartet nicht auf eine
	// zweite Freigabe (kein Vier-Augen, schon freigegeben, nicht freigegeben).
	ErrSchleuseNichtZweitfreigebbar = errors.New("storage: schleuse-auftrag wartet nicht auf eine zweite freigabe")
	// ErrSchleuseHashAbweichung: Gespeicherter Text, sein Hash und der
	// freigegebene Hash passen nicht zusammen. Kann nur ein Schreibfehler
	// oder ein Eingriff an der Datenbank sein — der Auftrag geht nicht hinaus.
	ErrSchleuseHashAbweichung = errors.New("storage: schleuse-auftrag: text und freigegebener hash weichen ab")
)

// lockAuftrag nimmt die Schreibsperre (Leer-UPDATE) und liest den Auftrag.
// wo/args grenzen die Zeile ein (immer samt id).
func lockAuftrag(ctx context.Context, tx *sql.Tx, wo string, args ...any) (SchleuseAuftrag, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE schleuse_auftraege SET geaendert_am = geaendert_am WHERE `+wo, args...); err != nil {
		return SchleuseAuftrag{}, fmt.Errorf("sperre: %w", err)
	}
	a, err := scanAuftrag(tx.QueryRowContext(ctx, `SELECT `+auftragSpalten+` FROM schleuse_auftraege WHERE `+wo, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrSchleuseAuftragNichtGefunden
	}
	return a, err
}

// boardSperreTx liest die Board-Sperre innerhalb der Transaktion.
func boardSperreTx(ctx context.Context, tx *sql.Tx, boardID string) (bool, error) {
	var g int
	err := tx.QueryRowContext(ctx, `SELECT schleuse_gesperrt FROM boards WHERE id = ?`, boardID).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrBoardNotFound
	}
	if err != nil {
		return false, fmt.Errorf("sperre lesen: %w", err)
	}
	return g == 1, nil
}

func gesendetSeitTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, seit string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM schleuse_auftraege WHERE user_id = ? AND gesendet_am IS NOT NULL AND gesendet_am >= ?`,
		userID, seit).Scan(&n)
	return n, err
}

func eineStundeZurueck() string {
	return timeNow().UTC().Add(-time.Hour).Format(schleuseZeitFormat)
}

// GesendetLetzteStunde zählt die Aufträge, die die Person in der letzten
// Stunde gesendet hat (Vorab-Prüfung beim Freigeben; verbindlich prüft
// BeginneVersand).
func (r *SchleuseAuftragRepo) GesendetLetzteStunde(ctx context.Context, userID string) (int, error) {
	n, err := gesendetSeitTx(ctx, r.db, userID, eineStundeZurueck())
	if err != nil {
		return 0, fmt.Errorf("schleuse-aufträge.gesendet-zählen: %w", err)
	}
	return n, nil
}

// Freigeben bindet die Freigabe der Person an den Hash, den sie gesehen hat
// (ADR-0134 §3.4). basisHash muss dem gespeicherten Hash entsprechen (sonst
// ErrSchleuseAuftragVeraltet); der Auftrag muss ein Entwurf der Person sein,
// das Board darf nicht gesperrt sein. vierAugen ist die Abschrift des
// Schalters der Freigabe; mit ihm wartet der Auftrag auf ZweitFreigeben.
//
// Eine wiederholte Freigabe desselben Hashes ist kein Fehler (Doppelklick,
// zweiter Tab): Sie liefert den Auftrag unverändert.
func (r *SchleuseAuftragRepo) Freigeben(ctx context.Context, id, userID, email, basisHash string, vierAugen bool) (SchleuseAuftrag, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SchleuseAuftrag{}, fmt.Errorf("schleuse-aufträge.freigeben begin: %w", err)
	}
	defer tx.Rollback()
	a, err := lockAuftrag(ctx, tx, `id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return a, err
	}
	if a.Zustand == SchleuseZustandFreigegeben && a.FreigabeSHA256 == basisHash && a.SHA256 == basisHash {
		return a, nil
	}
	if a.Zustand != SchleuseZustandEntwurf {
		return a, ErrSchleuseAuftragNichtEntwurf
	}
	if a.SHA256 != basisHash || schleuse.AuszugHash(a.Text, a.Bilder) != a.SHA256 {
		return a, ErrSchleuseAuftragVeraltet
	}
	if gesperrt, err := boardSperreTx(ctx, tx, a.BoardID); err != nil {
		return a, err
	} else if gesperrt {
		return a, ErrSchleuseBoardGesperrt
	}
	jetzt := schleuseJetzt()
	if _, err := tx.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET zustand = ?, freigabe_sha256 = ?, freigegeben_von = ?, freigegeben_am = ?,
		   vier_augen = ?, zweit_freigegeben_von = NULL, zweit_freigegeben_am = NULL, fehler_grund = NULL, geaendert_am = ?
		 WHERE id = ? AND user_id = ? AND zustand = ? AND text_sha256 = ?`,
		SchleuseZustandFreigegeben, basisHash, email, jetzt, boolInt(vierAugen), jetzt,
		id, userID, SchleuseZustandEntwurf, basisHash); err != nil {
		return a, fmt.Errorf("schleuse-aufträge.freigeben: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return a, fmt.Errorf("schleuse-aufträge.freigeben commit: %w", err)
	}
	a.Zustand, a.Freigegeben, a.FreigabeSHA256, a.FreigegebenVon, a.FreigegebenAm = SchleuseZustandFreigegeben, true, basisHash, email, jetzt
	a.VierAugen, a.ZweitFreigegebenVon, a.ZweitFreigegebenAm, a.FehlerGrund, a.GeaendertAm = vierAugen, "", "", "", jetzt
	return a, nil
}

// ZweitFreigeben setzt die zweite Freigabe bei Vier-Augen (ADR-0134 §3.4):
// eine andere Person als die antragstellende, an den Hash gebunden, den sie
// gesehen hat. Die Rolle (editor+) prüft der Aufrufer. Der Auftrag muss zu
// Board und Karte gehören.
func (r *SchleuseAuftragRepo) ZweitFreigeben(ctx context.Context, id, boardID, cardID, email, basisHash string) (SchleuseAuftrag, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SchleuseAuftrag{}, fmt.Errorf("schleuse-aufträge.zweitfreigabe begin: %w", err)
	}
	defer tx.Rollback()
	a, err := lockAuftrag(ctx, tx, `id = ? AND board_id = ? AND card_id = ?`, id, boardID, cardID)
	if err != nil {
		return a, err
	}
	if a.Zustand != SchleuseZustandFreigegeben || !a.VierAugen || a.ZweitFreigegebenVon != "" {
		return a, ErrSchleuseNichtZweitfreigebbar
	}
	if strings.EqualFold(strings.TrimSpace(a.UserEmail), strings.TrimSpace(email)) ||
		strings.EqualFold(strings.TrimSpace(a.FreigegebenVon), strings.TrimSpace(email)) {
		return a, ErrSchleuseSelbstfreigabe
	}
	if a.FreigabeSHA256 != basisHash || a.SHA256 != basisHash {
		return a, ErrSchleuseAuftragVeraltet
	}
	if gesperrt, err := boardSperreTx(ctx, tx, a.BoardID); err != nil {
		return a, err
	} else if gesperrt {
		return a, ErrSchleuseBoardGesperrt
	}
	jetzt := schleuseJetzt()
	if _, err := tx.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET zweit_freigegeben_von = ?, zweit_freigegeben_am = ?, geaendert_am = ?
		 WHERE id = ? AND zustand = ? AND vier_augen = 1 AND zweit_freigegeben_von IS NULL AND freigabe_sha256 = ?`,
		email, jetzt, jetzt, id, SchleuseZustandFreigegeben, basisHash); err != nil {
		return a, fmt.Errorf("schleuse-aufträge.zweitfreigabe: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return a, fmt.Errorf("schleuse-aufträge.zweitfreigabe commit: %w", err)
	}
	a.ZweitFreigegebenVon, a.ZweitFreigegebenAm, a.GeaendertAm = email, jetzt, jetzt
	return a, nil
}

// ZurZweitfreigabe liefert die Aufträge an einer Karte, die auf eine zweite
// Freigabe warten und NICHT von email stammen — was diese Person freigeben
// könnte. Die Rolle prüft der Aufrufer.
func (r *SchleuseAuftragRepo) ZurZweitfreigabe(ctx context.Context, boardID, cardID, email string) ([]SchleuseAuftrag, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+auftragSpalten+` FROM schleuse_auftraege
		  WHERE board_id = ? AND card_id = ? AND zustand = ? AND vier_augen = 1 AND zweit_freigegeben_von IS NULL
		    AND lower(user_email) <> lower(?) AND lower(COALESCE(freigegeben_von, '')) <> lower(?)
		  ORDER BY freigegeben_am, id`, boardID, cardID, SchleuseZustandFreigegeben, email, email)
	if err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.zur-zweitfreigabe: %w", err)
	}
	defer rows.Close()
	out := []SchleuseAuftrag{}
	for rows.Next() {
		a, err := scanAuftrag(rows)
		if err != nil {
			return nil, fmt.Errorf("schleuse-aufträge.zur-zweitfreigabe scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAnKarte liest einen Auftrag an Board und Karte, gleich wessen — für die
// zweite Freigabe und den Beleg am Antwortkommentar. Die Rolle prüft der
// Aufrufer; die Zuordnung liefert diese Funktion so wenig wie Get.
func (r *SchleuseAuftragRepo) GetAnKarte(ctx context.Context, id, boardID, cardID string) (SchleuseAuftrag, error) {
	a, err := scanAuftrag(r.db.QueryRowContext(ctx,
		`SELECT `+auftragSpalten+` FROM schleuse_auftraege WHERE id = ? AND board_id = ? AND card_id = ?`, id, boardID, cardID))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrSchleuseAuftragNichtGefunden
	}
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.get-an-karte: %w", err)
	}
	return a, nil
}

// GetIntern liest einen Auftrag ohne Personenfilter — NUR für den
// Hintergrundauftrag des Servers.
func (r *SchleuseAuftragRepo) GetIntern(ctx context.Context, id string) (SchleuseAuftrag, error) {
	a, err := scanAuftrag(r.db.QueryRowContext(ctx, `SELECT `+auftragSpalten+` FROM schleuse_auftraege WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrSchleuseAuftragNichtGefunden
	}
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.get-intern: %w", err)
	}
	return a, nil
}

// zurueckAufEntwurf hebt eine Freigabe auf, die noch nicht hinausgegangen
// ist (Sperre oder Grenze beim Versand). Der Grund bleibt lesbar.
func zurueckAufEntwurf(ctx context.Context, tx *sql.Tx, id, grund string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET zustand = ?, freigabe_sha256 = NULL, freigegeben_von = NULL, freigegeben_am = NULL,
		   vier_augen = 0, zweit_freigegeben_von = NULL, zweit_freigegeben_am = NULL, fehler_grund = ?, geaendert_am = ?
		 WHERE id = ? AND zustand = ?`,
		SchleuseZustandEntwurf, grund, schleuseJetzt(), id, SchleuseZustandFreigegeben)
	return err
}

// Gründe, die BeginneVersand und Fehlschlagen in fehler_grund schreiben.
const (
	SchleuseGrundGesperrt       = "board-gesperrt"
	SchleuseGrundGrenze         = "zu-viele-auftraege"
	SchleuseGrundHashAbweichung = "hash-abweichung"
	SchleuseGrundUnterbrochen   = "unterbrochen"
)

// BeginneVersand setzt einen sendebereiten Auftrag auf „gesendet“ — als
// LETZTER Schritt vor dem Verbindungsaufbau, in EINER Transaktion mit allen
// Prüfungen (ADR-0134 §3.4/§3.5, Auflage aus #685):
//   - Zustand „freigegeben“, bei Vier-Augen mit zweiter Freigabe,
//   - gespeicherter Text samt Bild-Hashes, der Auftrags-Hash und der
//     freigegebene Hash stimmen überein (der Aufrufer sendet danach GENAU
//     a.Text und die Bilder, deren bereinigte Bytes zu a.Bilder passen —
//     Karte #687),
//   - das Board existiert und ist nicht gesperrt,
//   - die Person hat in der letzten Stunde weniger als hoechstProStunde
//     Aufträge gesendet.
//
// Sperre und Grenze heben die Freigabe auf (zurück auf „entwurf“, Grund in
// fehler_grund) — es ist nichts hinausgegangen. Eine Hash-Abweichung macht
// den Auftrag „fehlgeschlagen“.
func (r *SchleuseAuftragRepo) BeginneVersand(ctx context.Context, id string, hoechstProStunde int) (SchleuseAuftrag, error) {
	if hoechstProStunde < 1 {
		return SchleuseAuftrag{}, fmt.Errorf("schleuse-aufträge.versand: hoechstProStunde muss >= 1 sein")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SchleuseAuftrag{}, fmt.Errorf("schleuse-aufträge.versand begin: %w", err)
	}
	defer tx.Rollback()
	a, err := lockAuftrag(ctx, tx, `id = ?`, id)
	if err != nil {
		return a, err
	}
	if a.Zustand != SchleuseZustandFreigegeben {
		return a, ErrSchleuseNichtSendebereit
	}
	if a.VierAugen && a.ZweitFreigegebenVon == "" {
		return a, ErrSchleuseWartetZweitfreigabe
	}
	commitMit := func(fehler error) (SchleuseAuftrag, error) {
		if err := tx.Commit(); err != nil {
			return a, fmt.Errorf("schleuse-aufträge.versand commit: %w", err)
		}
		return a, fehler
	}
	if a.FreigabeSHA256 == "" || a.FreigabeSHA256 != a.SHA256 || schleuse.AuszugHash(a.Text, a.Bilder) != a.SHA256 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE schleuse_auftraege SET zustand = ?, fehler_grund = ?, abgeschlossen_am = ?, geaendert_am = ? WHERE id = ? AND zustand = ?`,
			SchleuseZustandFehlgeschlagen, SchleuseGrundHashAbweichung, schleuseJetzt(), schleuseJetzt(), id, SchleuseZustandFreigegeben); err != nil {
			return a, fmt.Errorf("schleuse-aufträge.versand hash: %w", err)
		}
		a.Zustand, a.FehlerGrund = SchleuseZustandFehlgeschlagen, SchleuseGrundHashAbweichung
		return commitMit(ErrSchleuseHashAbweichung)
	}
	gesperrt, err := boardSperreTx(ctx, tx, a.BoardID)
	if err != nil {
		return a, err
	}
	if gesperrt {
		if err := zurueckAufEntwurf(ctx, tx, id, SchleuseGrundGesperrt); err != nil {
			return a, fmt.Errorf("schleuse-aufträge.versand sperre: %w", err)
		}
		a.Zustand, a.FehlerGrund = SchleuseZustandEntwurf, SchleuseGrundGesperrt
		return commitMit(ErrSchleuseBoardGesperrt)
	}
	n, err := gesendetSeitTx(ctx, tx, a.UserID, eineStundeZurueck())
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.versand zählen: %w", err)
	}
	if n >= hoechstProStunde {
		if err := zurueckAufEntwurf(ctx, tx, id, SchleuseGrundGrenze); err != nil {
			return a, fmt.Errorf("schleuse-aufträge.versand grenze: %w", err)
		}
		a.Zustand, a.FehlerGrund = SchleuseZustandEntwurf, SchleuseGrundGrenze
		return commitMit(ErrSchleuseGrenzeErreicht)
	}
	jetzt := schleuseJetzt()
	res, err := tx.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET zustand = ?, gesendet_am = ?, geaendert_am = ?
		 WHERE id = ? AND zustand = ? AND freigabe_sha256 = text_sha256`,
		SchleuseZustandGesendet, jetzt, jetzt, id, SchleuseZustandFreigegeben)
	if err != nil {
		return a, fmt.Errorf("schleuse-aufträge.versand: %w", err)
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return a, ErrSchleuseNichtSendebereit
	}
	a.Zustand, a.GesendetAm, a.GeaendertAm = SchleuseZustandGesendet, jetzt, jetzt
	return commitMit(nil)
}

// SchleuseErgebnis ist, was nach der Antwort im Auftragsdatensatz steht.
type SchleuseErgebnis struct {
	AntwortText            string // pseudonymisiert, wie vom Anbieter
	KommentarID            string // "" = kein Kommentar geschrieben (Karte fehlt)
	KommentarSHA256        string
	VeraendertePlatzhalter []string
	FehlerGrund            string // bei „beantwortet“ ein Hinweis (etwa karte-fehlt)
	InputTokens            int
	OutputTokens           int
	KostenUSD              float64
	FinishReason           string
}

// Abschliessen setzt einen gesendeten Auftrag auf „beantwortet“.
func (r *SchleuseAuftragRepo) Abschliessen(ctx context.Context, id string, e SchleuseErgebnis) error {
	if e.VeraendertePlatzhalter == nil {
		e.VeraendertePlatzhalter = []string{}
	}
	veraendert, _ := json.Marshal(e.VeraendertePlatzhalter)
	jetzt := schleuseJetzt()
	res, err := r.db.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET zustand = ?, abgeschlossen_am = ?, geaendert_am = ?, antwort_text = ?, antwort_sha256 = ?,
		   kommentar_id = NULLIF(?, ''), kommentar_sha256 = NULLIF(?, ''), veraenderte_platzhalter = ?, fehler_grund = NULLIF(?, ''),
		   input_tokens = ?, output_tokens = ?, kosten_usd = ?, finish_reason = NULLIF(?, '')
		 WHERE id = ? AND zustand = ?`,
		SchleuseZustandBeantwortet, jetzt, jetzt, e.AntwortText, schleuse.Hash(e.AntwortText),
		e.KommentarID, e.KommentarSHA256, string(veraendert), e.FehlerGrund,
		e.InputTokens, e.OutputTokens, e.KostenUSD, e.FinishReason, id, SchleuseZustandGesendet)
	if err != nil {
		return fmt.Errorf("schleuse-aufträge.abschliessen: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrSchleuseNichtSendebereit
	}
	return nil
}

// Fehlschlagen setzt einen freigegebenen oder gesendeten Auftrag auf
// „fehlgeschlagen“ (ohne Wiederholung). grund ist maschinenlesbar und nie
// Text des Anbieters. Tokens und Kosten, soweit bekannt.
func (r *SchleuseAuftragRepo) Fehlschlagen(ctx context.Context, id, grund string) error {
	jetzt := schleuseJetzt()
	res, err := r.db.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET zustand = ?, fehler_grund = ?, abgeschlossen_am = ?, geaendert_am = ?
		 WHERE id = ? AND zustand IN (?, ?)`,
		SchleuseZustandFehlgeschlagen, grund, jetzt, jetzt, id, SchleuseZustandFreigegeben, SchleuseZustandGesendet)
	if err != nil {
		return fmt.Errorf("schleuse-aufträge.fehlschlagen: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrSchleuseNichtSendebereit
	}
	return nil
}

// UnterbrocheneAbschliessen setzt beim Start jeden Auftrag, der noch auf
// „gesendet“ steht, auf „fehlgeschlagen“ (Grund „unterbrochen“): Der Server
// ist während des Versands beendet worden, ob die Anfrage den Anbieter
// erreicht hat, weiss niemand — und automatisch wiederholt wird nicht.
// Nur beim Start aufrufen, bevor der Hintergrundauftrag läuft.
func (r *SchleuseAuftragRepo) UnterbrocheneAbschliessen(ctx context.Context) ([]SchleuseAuftrag, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+auftragSpalten+` FROM schleuse_auftraege WHERE zustand = ? ORDER BY id`, SchleuseZustandGesendet)
	if err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.unterbrochen: %w", err)
	}
	var liste []SchleuseAuftrag
	for rows.Next() {
		a, err := scanAuftrag(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("schleuse-aufträge.unterbrochen scan: %w", err)
		}
		liste = append(liste, a)
	}
	rows.Close()
	var aus []SchleuseAuftrag
	for _, a := range liste {
		if err := r.Fehlschlagen(ctx, a.ID, SchleuseGrundUnterbrochen); err == nil {
			a.Zustand, a.FehlerGrund = SchleuseZustandFehlgeschlagen, SchleuseGrundUnterbrochen
			aus = append(aus, a)
		}
	}
	return aus, nil
}

// Sendebereite liefert die IDs der Aufträge, die freigegeben sind und auf
// nichts mehr warten — beim Start, falls der Server zwischen Freigabe und
// Versand beendet wurde. Hinausgegangen ist von ihnen noch nichts; der
// Versand ist deshalb keine Wiederholung.
func (r *SchleuseAuftragRepo) Sendebereite(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM schleuse_auftraege WHERE zustand = ? AND (vier_augen = 0 OR zweit_freigegeben_von IS NOT NULL) ORDER BY freigegeben_am, id`,
		SchleuseZustandFreigegeben)
	if err != nil {
		return nil, fmt.Errorf("schleuse-aufträge.sendebereit: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ZuordnungenAufraeumen löscht die Zuordnung abgeschlossener Aufträge, deren
// Abschluss mehr als tage Tage zurückliegt (ADR-0134 §4) — danach ist der
// Auftragsdatensatz nicht mehr re-identifizierbar. Auszug und Antwort bleiben
// pseudonymisiert stehen (Beleg). Nur mit gestellter Uhr (F-1192).
func (r *SchleuseAuftragRepo) ZuordnungenAufraeumen(ctx context.Context, tage int) (int64, error) {
	if tage < 1 || !uhr.Erlaubt("schleuse-zuordnungen", nil) {
		return 0, nil
	}
	grenze := timeNow().UTC().Add(-time.Duration(tage) * 24 * time.Hour).Format(schleuseZeitFormat)
	res, err := r.db.ExecContext(ctx,
		`UPDATE schleuse_auftraege SET zuordnung_verschluesselt = NULL
		  WHERE zustand IN (?, ?) AND abgeschlossen_am IS NOT NULL AND abgeschlossen_am < ? AND zuordnung_verschluesselt IS NOT NULL`,
		SchleuseZustandBeantwortet, SchleuseZustandFehlgeschlagen, grenze)
	if err != nil {
		return 0, fmt.Errorf("schleuse-aufträge.zuordnungen-aufräumen: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ZuordnungIntern entschlüsselt die Zuordnung ohne Personenfilter — NUR für
// die Re-Identifizierung im Hintergrundauftrag.
func (r *SchleuseAuftragRepo) ZuordnungIntern(ctx context.Context, id string) (map[string]string, error) {
	var userID string
	if err := r.db.QueryRowContext(ctx, `SELECT user_id FROM schleuse_auftraege WHERE id = ?`, id).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSchleuseAuftragNichtGefunden
		}
		return nil, fmt.Errorf("schleuse-aufträge.zuordnung-intern: %w", err)
	}
	return r.Zuordnung(ctx, id, userID)
}
