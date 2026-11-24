package storage

// boardpaar_erneuern.go — Board-Paar-Zugänge erneuern sich selbst (Karte #796,
// T-R12, Vertrag zum Board-Paar, 10.10.2026, Migration 0037).
//
// Das Push-Skript des Betreibers tauscht sein Token abs_/abe_ regelmäßig gegen ein
// neues, mit dem alten als Ausweis. Kein Admin-Zugang im KI-Netz: Wer das
// Token hat, darf genau das und sonst nichts. Ein Dieb, der erneuert, sperrt
// das echte Skript aus — dessen nächster Lauf bekommt 401 und schlägt Alarm.
// Der Diebstahl wird sichtbar, statt still zu bleiben.
//
// Vertragspunkte, die hier hängen:
//  1. Wiederholbar: Wer in der Übergangszeit nochmals mit dem alten Token
//     erneuert, bekommt DASSELBE neue Token. Dafür liegt der Klartext des
//     Nachfolgers am Vorgänger, verschlüsselt mit einem Schlüssel aus dem
//     Klartext des Vorgängers (nachfolger_enc). Kein Server-Geheimnis nötig
//     (ABOARD_SECRET_KEY ist optional), und die Datenbank allein verrät den
//     Nachfolger nicht — gespeichert ist vom Vorgänger nur sein SHA-256.
//  2. Übergangszeit 10 min (BoardpaarUebergangszeit).
//  3. Wer wann von wo erneuert hat: erneuert_von am Nachfolger + Log-Zeile im
//     Handler.
//  4. Zweimal erneuern → nur das jüngste gilt: Erneuern beendet die
//     Übergangszeit aller Vorgänger, die noch auf das erneuerte Token zeigen.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// BoardpaarUebergangszeit: so lange gilt ein erneuertes Token noch, damit ein
// abgebrochener Austausch nicht aussperrt (Vertrag Punkt 2).
const BoardpaarUebergangszeit = 10 * time.Minute

// giltBisFormat: feste Breite, damit der Wert lesbar bleibt; verglichen wird
// trotzdem nur nach dem Parsen, nie als Zeichenkette (RFC3339Nano kürzt
// Nullen und sortiert dann falsch).
const giltBisFormat = "2006-01-02T15:04:05.000000000Z07:00"

// BoardpaarErneuerung ist das Ergebnis von TokenErneuern.
type BoardpaarErneuerung struct {
	// Token ist der Klartext des Nachfolgers.
	Token string
	// Zugang ist der Nachfolger (ohne Geheimnis).
	Zugang *BoardpaarToken
	// AltGiltBis: Ende der Übergangszeit des alten Tokens.
	AltGiltBis string
	// Wiederholt: Das alte Token war schon erneuert; Token ist derselbe
	// Nachfolger wie beim ersten Mal.
	Wiederholt bool
}

// ErrBoardpaarUhr: Die Systemuhr ist nicht gestellt; Erneuern geht dann
// nicht (Sprint-90-Kritiker K-1, Wissen §38). Der Handler antwortet 503 —
// für das Skript „später wieder versuchen“, kein Alarm.
var ErrBoardpaarUhr = errors.New("storage: systemuhr nicht gestellt, erneuern später")

// uhrGestellt fragt den Kernel (server/uhr), ohne zu loggen. Variable, damit
// Tests den ungestellten Fall nachbauen.
var uhrGestellt = func() bool {
	ok, _ := uhr.Gestellt()
	return ok
}

// tokenGilt: nicht widerrufen und, falls erneuert, noch in der
// Übergangszeit. Ein unlesbarer Zeitpunkt gilt als abgelaufen (fail closed),
// ebenso jede Übergangszeit, solange die Uhr nicht gestellt ist: Ein Pi ohne
// Batterie startet in der Vergangenheit, und jeder Vorgänger läge dann
// scheinbar noch in seinen zehn Minuten (K-1). Ein Token ohne Erneuerung
// hängt an keiner Frist und gilt auch bei ungestellter Uhr.
func tokenGilt(revokedAt, giltBis string, jetzt time.Time, uhrOK bool) bool {
	if revokedAt != "" {
		return false
	}
	if giltBis == "" {
		return true
	}
	if !uhrOK {
		return false
	}
	bis, err := time.Parse(time.RFC3339Nano, giltBis)
	return err == nil && bis.After(jetzt)
}

// TokenErneuern tauscht das Token klartext des Typs gegen ein neues (Karte
// #796). adresse ist die Quelladresse der Anfrage (nach der Proxy-Regel der
// Agenten-Bremse). Unbekannt, widerrufen, abgelaufen oder vom falschen Typ →
// ErrBoardpaarTokenUnbekannt; bekanntes Token, aber ungestellte Uhr →
// ErrBoardpaarUhr (dann nichts geschrieben).
func (r *BoardpaarRepo) TokenErneuern(ctx context.Context, typ, klartext, adresse string) (*BoardpaarErneuerung, error) {
	praefix := boardpaarPraefix(typ)
	if praefix == "" || !strings.HasPrefix(klartext, praefix) {
		return nil, ErrBoardpaarTokenUnbekannt
	}
	// Zwei Runden: Verliert die erste das Rennen gegen eine gleichzeitige
	// Erneuerung mit demselben Token, ist das Token danach erneuert, und die
	// zweite Runde liefert denselben Nachfolger wie die gewinnende.
	for runde := 0; runde < 2; runde++ {
		e, verloren, err := r.erneuernRunde(ctx, typ, klartext, adresse)
		if err != nil || !verloren {
			// Aufräumen erst nach dem Ausweis (Kritiker K-2): Eine Anfrage
			// mit erfundenem Token löst keine Arbeit an der Tabelle aus.
			if err == nil {
				r.nachfolgerAufraeumen(ctx)
			}
			return e, err
		}
	}
	return nil, fmt.Errorf("boardpaar.token-erneuern: zweimal überholt")
}

// erneuernNachLesen läuft zwischen dem Lesen des alten Tokens und dem
// Schreiben — nur für Tests, die dort eine gleichzeitige Erneuerung
// einschieben (Wissen §29: ein Wettlauf lässt sich sonst nicht provozieren).
var erneuernNachLesen = func() {}

type erneuernAlt struct {
	id, name, revokedAt, giltBis, nachfolgerID, nachfolgerEnc string
	// darfAnlegen (ADR-0141 §5.3, W2-4): geht auf den Nachfolger über —
	// sonst bekäme das Skript nach der ersten Erneuerung 403 beim Anlegen.
	darfAnlegen int
}

// erneuernRunde: verloren = true, wenn eine gleichzeitige Erneuerung
// dazwischenkam (dann nichts geschrieben).
func (r *BoardpaarRepo) erneuernRunde(ctx context.Context, typ, klartext, adresse string) (*BoardpaarErneuerung, bool, error) {
	var alt erneuernAlt
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, coalesce(revoked_at, ''), coalesce(gilt_bis, ''), coalesce(nachfolger_id, ''), coalesce(nachfolger_enc, ''), darf_anlegen
		 FROM boardpaar_tokens WHERE token_hash = ? AND typ = ?`, hashToken(klartext), typ).
		Scan(&alt.id, &alt.name, &alt.revokedAt, &alt.giltBis, &alt.nachfolgerID, &alt.nachfolgerEnc, &alt.darfAnlegen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrBoardpaarTokenUnbekannt
	}
	if err != nil {
		return nil, false, fmt.Errorf("boardpaar.token-erneuern: %w", err)
	}
	if alt.revokedAt != "" {
		return nil, false, ErrBoardpaarTokenUnbekannt
	}
	// Uhr vor jeder Frist (K-1): gilt_bis aus einer falschen Uhr sperrte das
	// Skript nach dem Stellen aus, und eine Wiederholung prüfte gegen sie.
	if !uhrGestellt() {
		return nil, false, ErrBoardpaarUhr
	}
	jetzt := timeNow().UTC()
	if !tokenGilt(alt.revokedAt, alt.giltBis, jetzt, true) {
		return nil, false, ErrBoardpaarTokenUnbekannt
	}
	if alt.giltBis != "" {
		e, err := r.erneuerungWiederholen(ctx, typ, klartext, alt)
		return e, false, err
	}

	// Erste Erneuerung dieses Tokens.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, false, fmt.Errorf("boardpaar.token-erneuern: rand: %w", err)
	}
	neu := boardpaarPraefix(typ) + base64.RawURLEncoding.EncodeToString(buf)
	t := &BoardpaarToken{ID: uuid.NewString(), Typ: typ, Name: alt.name, CreatedAt: jetzt.Format(time.RFC3339Nano),
		CreatedBy: "erneuert", ErneuertAus: alt.id, ErneuertVon: adresse, DarfAnlegen: alt.darfAnlegen == 1}
	enc, err := nachfolgerVerschluesseln(klartext, alt.id, t.ID, neu)
	if err != nil {
		return nil, false, err
	}
	giltBis := jetzt.Add(BoardpaarUebergangszeit).Format(giltBisFormat)
	erneuernNachLesen()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("boardpaar.token-erneuern: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Zuerst schreiben: Das UPDATE holt die Schreibsperre und sieht den
	// jüngsten Stand. Ist das Token inzwischen erneuert oder widerrufen,
	// trifft es nichts — dann hat ein anderer gewonnen (Wissen §29: Prüfung
	// und Zugriff in einem Schritt, nicht davor gelesen).
	res, err := tx.ExecContext(ctx,
		`UPDATE boardpaar_tokens SET gilt_bis = ?, nachfolger_id = ?, nachfolger_enc = ?
		 WHERE id = ? AND revoked_at IS NULL AND gilt_bis IS NULL`, giltBis, t.ID, enc, alt.id)
	if err != nil {
		return nil, false, fmt.Errorf("boardpaar.token-erneuern: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, true, nil
	}
	// Vertrag Punkt 4: Vorgänger des alten Tokens, die noch in ihrer
	// Übergangszeit sind, enden jetzt. Ihr verschlüsselter Nachfolger (= das
	// alte Token) wird mit gelöscht. Schon abgelaufene behalten ihr gilt_bis
	// (Kritiker F-I: sonst zeigte der Admin-Reiter ein falsches „gilt bis“).
	if err := vorgaengerBeenden(ctx, tx, alt.id, jetzt); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO boardpaar_tokens (id, typ, name, token_hash, created_at, created_by, erneuert_aus, erneuert_von, darf_anlegen)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Typ, t.Name, hashToken(neu), t.CreatedAt, t.CreatedBy, t.ErneuertAus, nullWennLeer(t.ErneuertVon), alt.darfAnlegen); err != nil {
		return nil, false, fmt.Errorf("boardpaar.token-erneuern: nachfolger: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("boardpaar.token-erneuern commit: %w", err)
	}
	return &BoardpaarErneuerung{Token: neu, Zugang: t, AltGiltBis: giltBis}, false, nil
}

// vorgaengerBeenden: im Schreib-Transaktionsrahmen; der Vergleich der Frist
// läuft in Go, nie als Zeichenkette.
func vorgaengerBeenden(ctx context.Context, tx *sql.Tx, id string, jetzt time.Time) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE boardpaar_tokens SET nachfolger_enc = NULL WHERE nachfolger_id = ? AND revoked_at IS NULL`, id); err != nil {
		return fmt.Errorf("boardpaar.token-erneuern vorgänger: %w", err)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, coalesce(gilt_bis, '') FROM boardpaar_tokens WHERE nachfolger_id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("boardpaar.token-erneuern vorgänger: %w", err)
	}
	var laufend []string
	for rows.Next() {
		var vid, bis string
		if err := rows.Scan(&vid, &bis); err != nil {
			_ = rows.Close()
			return fmt.Errorf("boardpaar.token-erneuern vorgänger: %w", err)
		}
		if tokenGilt("", bis, jetzt, true) {
			laufend = append(laufend, vid)
		}
	}
	// Verify-Lauf Sprint 90, F7: ein Abbruch der Iteration ist ein Fehler,
	// keine kürzere Liste — sonst gälte ein Vorgänger still weiter.
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("boardpaar.token-erneuern vorgänger: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("boardpaar.token-erneuern vorgänger: %w", err)
	}
	for _, vid := range laufend {
		if _, err := tx.ExecContext(ctx, `UPDATE boardpaar_tokens SET gilt_bis = ? WHERE id = ?`,
			jetzt.Format(giltBisFormat), vid); err != nil {
			return fmt.Errorf("boardpaar.token-erneuern vorgänger: %w", err)
		}
	}
	return nil
}

// erneuerungWiederholen liefert den Nachfolger eines schon erneuerten Tokens
// in dessen Übergangszeit (Vertrag Punkt 1). Der Nachfolger muss noch das
// aktuelle Token sein — ist er widerrufen oder selbst erneuert, gilt das alte
// nicht mehr als Ausweis.
func (r *BoardpaarRepo) erneuerungWiederholen(ctx context.Context, typ, klartext string, alt erneuernAlt) (*BoardpaarErneuerung, error) {
	if alt.nachfolgerID == "" || alt.nachfolgerEnc == "" {
		return nil, ErrBoardpaarTokenUnbekannt
	}
	t := BoardpaarToken{Typ: typ}
	var hash, revoked, giltBis string
	var darf int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, created_at, created_by, coalesce(erneuert_aus, ''), coalesce(erneuert_von, ''),
		        coalesce(revoked_at, ''), coalesce(gilt_bis, ''), darf_anlegen
		 FROM boardpaar_tokens WHERE id = ? AND typ = ?`, alt.nachfolgerID, typ).
		Scan(&t.ID, &t.Name, &hash, &t.CreatedAt, &t.CreatedBy, &t.ErneuertAus, &t.ErneuertVon, &revoked, &giltBis, &darf)
	t.DarfAnlegen = darf == 1
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardpaarTokenUnbekannt
	}
	if err != nil {
		return nil, fmt.Errorf("boardpaar.token-erneuern nachfolger: %w", err)
	}
	if revoked != "" || giltBis != "" {
		return nil, ErrBoardpaarTokenUnbekannt
	}
	neu, err := nachfolgerEntschluesseln(klartext, alt.id, t.ID, alt.nachfolgerEnc)
	if err != nil || hashToken(neu) != hash {
		// Darf nicht vorkommen; lieber 401 als ein falsches Token ausliefern.
		log.Printf("boardpaar: erneuerung von %q nicht wiederholbar: %v", alt.name, err)
		return nil, ErrBoardpaarTokenUnbekannt
	}
	return &BoardpaarErneuerung{Token: neu, Zugang: &t, AltGiltBis: alt.giltBis, Wiederholt: true}, nil
}

// nachfolgerAufraeumen leert den verschlüsselten Nachfolger aller Tokens,
// deren Übergangszeit vorbei ist. Danach ist eine gestohlene Kopie des alten
// Tokens auch zusammen mit der Datenbank wertlos. Rechnet gegen die Uhr und
// fragt deshalb zuerst, ob sie gestellt ist (Wissen §38): Bei vorgehender Uhr
// sperrte sonst ein Skript aus, das nach einem Abbruch wiederholt. Fehler
// nur im Log — die Prüfung des Tokens hängt nicht daran.
func (r *BoardpaarRepo) nachfolgerAufraeumen(ctx context.Context) {
	if !uhr.Erlaubt("boardpaar-uebergang", nil) {
		return
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, gilt_bis FROM boardpaar_tokens WHERE nachfolger_enc IS NOT NULL AND gilt_bis IS NOT NULL`)
	if err != nil {
		log.Printf("boardpaar: übergangszeiten nicht lesbar: %v", err)
		return
	}
	jetzt := timeNow()
	var faellig []string
	for rows.Next() {
		var id, bis string
		if err := rows.Scan(&id, &bis); err != nil {
			continue
		}
		if !tokenGilt("", bis, jetzt, true) {
			faellig = append(faellig, id)
		}
	}
	_ = rows.Close()
	for _, id := range faellig {
		if _, err := r.db.ExecContext(ctx, `UPDATE boardpaar_tokens SET nachfolger_enc = NULL WHERE id = ?`, id); err != nil {
			log.Printf("boardpaar: nachfolger von %s nicht geleert: %v", id, err)
		}
	}
}

func nullWennLeer(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nachfolgerSchluessel: AES-256-Schlüssel aus dem Klartext des alten Tokens.
// Der Klartext ist 32 Zufallsbytes stark; HMAC trennt den Zweck.
func nachfolgerSchluessel(altKlartext string) []byte {
	m := hmac.New(sha256.New, []byte(altKlartext))
	m.Write([]byte("aboard boardpaar erneuern v1"))
	return m.Sum(nil)
}

func nachfolgerAEAD(altKlartext string) (cipher.AEAD, error) {
	block, err := aes.NewCipher(nachfolgerSchluessel(altKlartext))
	if err != nil {
		return nil, fmt.Errorf("boardpaar: aes: %w", err)
	}
	return cipher.NewGCM(block)
}

// nachfolgerVerschluesseln: nonce || chiffrat, Base64. Die IDs beider Tokens
// sind zusätzliche Daten — ein umkopierter Wert entschlüsselt nicht.
func nachfolgerVerschluesseln(altKlartext, altID, neuID, neuKlartext string) (string, error) {
	aead, err := nachfolgerAEAD(altKlartext)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("boardpaar: nonce: %w", err)
	}
	ct := aead.Seal(nonce, nonce, []byte(neuKlartext), []byte(altID+"|"+neuID))
	return base64.StdEncoding.EncodeToString(ct), nil
}

func nachfolgerEntschluesseln(altKlartext, altID, neuID, enc string) (string, error) {
	roh, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("boardpaar: nachfolger nicht dekodierbar: %w", err)
	}
	aead, err := nachfolgerAEAD(altKlartext)
	if err != nil {
		return "", err
	}
	if len(roh) < aead.NonceSize() {
		return "", errors.New("boardpaar: nachfolger zu kurz")
	}
	pt, err := aead.Open(nil, roh[:aead.NonceSize()], roh[aead.NonceSize():], []byte(altID+"|"+neuID))
	if err != nil {
		return "", fmt.Errorf("boardpaar: nachfolger nicht entschlüsselbar: %w", err)
	}
	return string(pt), nil
}
