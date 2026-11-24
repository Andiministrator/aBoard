package api

// konto_frisch.go — frische Anmeldung beim schon gebundenen Anbieter als
// Bestätigung für Personen ohne Passwort (ADR-0149, Nachtrag 2026-11-04 zu
// Karte #1098, F-2918).
//
//	POST /users/me/konto/frisch/start {anbieter, aktion, ziel} → {ziel: Adresse beim Anbieter}
//	GET  /anmeldung/rueckruf[/<kürzel>] mit Zweck reauth → Nachweis-Cookie, /?konto=bestaetigt
//
// Ablauf: Die Person wählt eine Änderung (Weg verknüpfen, Weg lösen, Passwort
// anlegen) und meldet sich dafür beim Anbieter neu an, an den sie schon
// gebunden ist (prompt=login, max_age=0). Der Rückruf prüft dasselbe sub,
// auth_time (höchstens 5 Minuten alt) und dieselbe, noch gültige Sitzung und
// legt dann einen Nachweis „frisch bestätigt“ an: versiegelt, gebunden an
// Adresse, Sitzung und Aktion, 5 Minuten gültig, einmal einlösbar (die Kennung
// steht zusätzlich im Speicher und wird beim Einlösen gestrichen). Er reist im
// Cookie __Host-aboard_frisch (HttpOnly, SameSite=Strict), nicht in der URL.
// Der Nachweis ersetzt bei Personen ohne Passwort die Passwort-Bestätigung —
// nur für die Aktion, die er nennt.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// FrischCookie trägt den versiegelten Nachweis „frisch bestätigt“.
	FrischCookie = "__Host-aboard_frisch"
	// frischGueltig: so lange gilt ein Nachweis.
	frischGueltig = 5 * time.Minute
	// authTimeHoechstalter: so alt darf die Anmeldung beim Anbieter höchstens
	// sein; authTimeVorlauf: so weit darf auth_time vor unserer Uhr liegen
	// (Uhrenabweichung).
	authTimeHoechstalter = 5 * time.Minute
	authTimeVorlauf      = time.Minute

	// Ergebnisse der Rückkehr nach der frischen Anmeldung.
	KontoErgebnisBestaetigt      = "bestaetigt"
	KontoErgebnisNichtBestaetigt = "nicht_bestaetigt"
	// KontoErgebnisNichtUnterstuetzt: Der Anbieter liefert kein auth_time und
	// ABOARD_OIDC_REAUTH_FALLBACK ist aus — Ab- und Anmelden hilft dann nie
	// (#1101, F-2941). Abweisungen wegen sub bleiben nicht_bestaetigt.
	KontoErgebnisNichtUnterstuetzt = "nicht_unterstuetzt"
	// KontoErgebnisVorgangAlt: Auf dem iat-Weg (Fallback) stammt das Token nicht
	// aus diesem Vorgang (F-2945). Ab- und Anmelden beim Anbieter hilft nicht;
	// der Vorgang muss neu beginnen.
	KontoErgebnisVorgangAlt = "vorgang_alt"

	// Audit-Aktionen der frischen Anmeldung.
	AuditActionKontoFrischOk     = "konto-frisch-bestaetigt"
	AuditActionKontoFrischFehler = "konto-frisch-fehlgeschlagen"

	// Aktionen, die ein Nachweis bestätigen kann.
	frischVerknuepfen = "verknuepfen"
	frischLoesen      = "loesen"
	frischPasswort    = "passwort"

	// KontoReauthFallbackEnv: fehlt auth_time, genügt ein in diesem Vorgang
	// frisch ausgestelltes ID-Token (ADR-0149, Nachtrag 2026-11-09, #1101).
	KontoReauthFallbackEnv = "ABOARD_OIDC_REAUTH_FALLBACK"

	// kennungFrischNoetig: Abweisung ohne gültigen Nachweis (ersetzt
	// nur_mit_passwort aus #1083).
	kennungFrischNoetig = "frische_anmeldung_noetig"
)

// KontoReauthFallback liest ABOARD_OIDC_REAUTH_FALLBACK (streng ein/aus,
// Tippfehler halten den Start an).
func KontoReauthFallback(getenv func(string) string) (bool, error) {
	return schalterStreng(getenv, KontoReauthFallbackEnv)
}

// frischAktion baut die Aktion eines Nachweises, z. B. verknuepfen:google.
func frischAktion(art, ziel string) string {
	if ziel == "" {
		return art
	}
	return art + ":" + ziel
}

// nachweis steht versiegelt im Cookie.
type nachweis struct {
	ID      string `json:"i"`
	Email   string `json:"e"`
	Sitzung string `json:"s"` // SHA-256 der Sitzungs-ID
	Aktion  string `json:"a"`
	Bis     int64  `json:"b"`
}

// frischeAnmeldungNoetig: 409 für Personen ohne Passwort und ohne gültigen
// Nachweis. Bewusst ohne Audit: Eine Abweisung kommt ohne Bestätigungsversuch
// und ließe sich ungebremst wiederholen (Nachprüfung #1083).
func (h *KontoEinstellungen) frischeAnmeldungNoetig(w http.ResponseWriter) {
	fehlerAntwort(w, http.StatusConflict, kennungFrischNoetig)
}

// nachweisLesen liest den Nachweis aus dem Cookie und prüft Person, Sitzung,
// Ablauf und die Kennung im Speicher. Aufrufer hält h.frischMu.
func (h *KontoEinstellungen) nachweisLesen(r *http.Request, a *kontoAnfrage) (nachweis, bool) {
	var n nachweis
	c, err := r.Cookie(FrischCookie)
	if err != nil || c.Value == "" {
		return n, false
	}
	klar, err := h.Box.Open(c.Value)
	if err != nil || json.Unmarshal(klar, &n) != nil {
		return n, false
	}
	jetzt := h.uhr().Unix()
	if n.ID == "" || n.Email != a.Email || jetzt >= n.Bis ||
		subtle.ConstantTimeCompare([]byte(n.Sitzung), []byte(storage.SitzungsHash(a.Sitzung))) != 1 {
		return n, false
	}
	bis, ok := h.frisch[n.ID]
	if !ok || jetzt >= bis {
		return n, false
	}
	return n, true
}

// nachweisFuer sagt, ob ein gültiger Nachweis für genau diese Aktion vorliegt
// (ohne ihn einzulösen) — für die Vorbedingungen vor der Bestätigung.
func (h *KontoEinstellungen) nachweisFuer(r *http.Request, a *kontoAnfrage, aktion string) bool {
	h.frischMu.Lock()
	defer h.frischMu.Unlock()
	n, ok := h.nachweisLesen(r, a)
	return ok && aktion != "" && n.Aktion == aktion
}

// nachweisEinloesen löst den Nachweis für diese Aktion ein: genau einmal. Das
// Cookie wird gelöscht.
func (h *KontoEinstellungen) nachweisEinloesen(w http.ResponseWriter, r *http.Request, a *kontoAnfrage, aktion string) bool {
	h.frischMu.Lock()
	defer h.frischMu.Unlock()
	n, ok := h.nachweisLesen(r, a)
	if !ok || aktion == "" || n.Aktion != aktion {
		return false
	}
	delete(h.frisch, n.ID)
	setzeFrischCookie(w, "", -1)
	return true
}

// nachweisAnsicht: was GET /users/me/konto über einen vorliegenden Nachweis
// sagt (nie seinen Wert).
func (h *KontoEinstellungen) nachweisAnsicht(r *http.Request, a *kontoAnfrage) *frischAnsicht {
	h.frischMu.Lock()
	defer h.frischMu.Unlock()
	n, ok := h.nachweisLesen(r, a)
	if !ok {
		return nil
	}
	art, ziel, _ := strings.Cut(n.Aktion, ":")
	return &frischAnsicht{Aktion: art, Ziel: ziel, Bis: n.Bis}
}

type frischAnsicht struct {
	Aktion string `json:"aktion"`
	Ziel   string `json:"ziel,omitempty"`
	Bis    int64  `json:"bis"`
}

func setzeFrischCookie(w http.ResponseWriter, wert string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     FrischCookie,
		Value:    wert,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// gebundeneAnbieter: die eingerichteten Anbieter, an die die Person gebunden
// ist — nur bei ihnen ist eine frische Anmeldung möglich. Verglichen wird das
// Kürzel oder der Aussteller (eine Bindung aus der Zeit vor dem Kürzel heißt
// `standard`, der Aussteller ist je Anbieter eindeutig).
func (h *KontoEinstellungen) gebundeneAnbieter(bind []storage.AnbieterBindung) []*OIDCAnmeldung {
	var out []*OIDCAnmeldung
	for _, an := range h.Anmeldungen.Anbieter {
		for _, b := range bind {
			if b.Anbieter == an.kuerzel() || (b.Iss != "" && b.Iss == an.Issuer) {
				out = append(out, an)
				break
			}
		}
	}
	return out
}

// frischStart: POST /users/me/konto/frisch/start.
func (h *KontoEinstellungen) frischStart(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in struct {
		Anbieter string `json:"anbieter"`
		Aktion   string `json:"aktion"`
		Ziel     string `json:"ziel"`
	}
	if !h.lies(w, r, &in) {
		return
	}
	if a.hatPasswort() {
		// Mit Passwort bestätigt die Person mit Passwort (und Code).
		fehlerAntwort(w, http.StatusConflict, "passwort_nutzen")
		return
	}
	if kontoNichtAktiv(w, a) {
		return
	}
	bind, err := h.Bindungen.FuerEmail(r.Context(), a.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bindings lookup failed", err)
		return
	}
	ziel := strings.TrimSpace(in.Ziel)
	var aktion string
	switch strings.TrimSpace(in.Aktion) {
	case frischVerknuepfen:
		if an := h.Anmeldungen.Finde(ziel); an == nil || an.Verknuepfung == nil {
			fehlerAntwort(w, http.StatusNotFound, "anbieter")
			return
		}
		aktion = frischAktion(frischVerknuepfen, ziel)
	case frischLoesen:
		da := false
		for _, b := range bind {
			da = da || b.Anbieter == ziel
		}
		if !IstAnbieterKuerzel(ziel) || !da {
			fehlerAntwort(w, http.StatusNotFound, "anbieter")
			return
		}
		aktion = frischAktion(frischLoesen, ziel)
	case frischPasswort:
		if !h.passwortAnlegenMoeglich(a) {
			fehlerAntwort(w, http.StatusNotFound, "aktion")
			return
		}
		aktion = frischPasswort
	default:
		fehlerAntwort(w, http.StatusBadRequest, "aktion")
		return
	}
	var an *OIDCAnmeldung
	for _, g := range h.gebundeneAnbieter(bind) {
		if g.kuerzel() == strings.TrimSpace(in.Anbieter) {
			an = g
		}
	}
	if an == nil || an.Verknuepfung == nil {
		// Nur beim schon gebundenen Anbieter (dasselbe sub prüft der Rückruf).
		fehlerAntwort(w, http.StatusConflict, "nicht_gebunden")
		return
	}
	if h.gesperrtAntwort(w, a) {
		return
	}
	if _, err := an.Anbieter(r.Context()); err != nil {
		fehlerAntwort(w, http.StatusServiceUnavailable, "nicht_erreichbar")
		return
	}
	adr, ok := an.vorgang(w, r, vorgangOpt{Zweck: ZweckReauth, Sitzung: storage.SitzungsHash(a.Sitzung), Aktion: aktion, AlsJSON: true})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ziel": adr})
}

// kontoNichtAktiv: 409 konto_zustand, wenn die Person eine konten-Zeile hat,
// die nicht aktiv ist (invited, pending, blocked; Sicherheitsprüfung B3). Ohne
// Konto (reine Vorab-Liste) ist das kein Hindernis. Bei true ist die Antwort
// geschrieben.
func kontoNichtAktiv(w http.ResponseWriter, a *kontoAnfrage) bool {
	if a.KontoZustand != "" && a.KontoZustand != storage.KontoAktiv {
		fehlerAntwort(w, http.StatusConflict, "konto_zustand")
		return true
	}
	return false
}

// gesperrtAntwort: 429, wenn eine der Bremsen sperrt. Bei true ist die
// Antwort geschrieben.
func (h *KontoEinstellungen) gesperrtAntwort(w http.ResponseWriter, a *kontoAnfrage) bool {
	if g, rest := h.gegenstelle.gesperrt("a:" + a.Adress); g {
		w.Header().Set("Retry-After", strconv.Itoa(int(rest.Seconds())+1))
		fehlerAntwort(w, http.StatusTooManyRequests, "gesperrt")
		return true
	}
	if g, rest := h.personen.gesperrt("k:" + a.Email); g {
		w.Header().Set("Retry-After", strconv.Itoa(int(rest.Seconds())+1))
		fehlerAntwort(w, http.StatusTooManyRequests, "gesperrt")
		return true
	}
	return false
}

// sitzungDesVorgangs prüft, dass das Sitzungs-Cookie der Anfrage dieselbe,
// noch gültige Sitzung trägt, die den Vorgang begonnen hat. Bei false ist die
// Antwort geschrieben (Weiterleitung abgelaufen/fehler oder 500).
func (h *KontoEinstellungen) sitzungDesVorgangs(ctx context.Context, w http.ResponseWriter, r *http.Request, sitzungHash, wofuer string) (*storage.Sitzung, string, bool) {
	c, err := r.Cookie(SitzungsCookie)
	if err != nil || c.Value == "" {
		h.zurueck(w, r, KontoErgebnisAbgelaufen)
		return nil, "", false
	}
	if subtle.ConstantTimeCompare([]byte(storage.SitzungsHash(c.Value)), []byte(sitzungHash)) != 1 {
		log.Printf("aboard-server: konto: %s abgewiesen — der Vorgang gehört zu einer anderen Sitzung", wofuer)
		h.zurueck(w, r, KontoErgebnisFehler)
		return nil, "", false
	}
	s, err := h.Sitzungen.Pruefen(ctx, c.Value)
	if errors.Is(err, storage.ErrSitzungUnbekannt) {
		h.zurueck(w, r, KontoErgebnisAbgelaufen)
		return nil, "", false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session check failed", err)
		return nil, "", false
	}
	return s, c.Value, true
}

// authTimeVorhanden: Trägt das ID-Token überhaupt einen auth_time-Claim?
// Ein vorhandener, aber unlesbarer Wert ist kein „fehlt“ — er darf nicht auf
// den Ersatzweg über iat führen (#1101).
func authTimeVorhanden(idt *oidc.IDToken) bool {
	var c struct {
		AuthTime json.RawMessage `json:"auth_time"`
	}
	return idt.Claims(&c) == nil && len(c.AuthTime) > 0 && string(c.AuthTime) != "null"
}

// authTime liest auth_time aus dem ID-Token (Zahl, notfalls als Zeichenkette).
func authTime(idt *oidc.IDToken) (int64, bool) {
	var c struct {
		AuthTime json.RawMessage `json:"auth_time"`
	}
	if err := idt.Claims(&c); err != nil || len(c.AuthTime) == 0 || string(c.AuthTime) == "null" {
		return 0, false
	}
	var f float64
	if json.Unmarshal(c.AuthTime, &f) != nil {
		var s string
		if json.Unmarshal(c.AuthTime, &s) != nil {
			return 0, false
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, false
		}
		f = v
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > math.MaxInt64/2 {
		return 0, false
	}
	return int64(f), true
}

// frischAbschliessen: Rückruf mit Zweck reauth. Legt den Nachweis nur an, wenn
// dieselbe Sitzung noch gilt, das Anbieter-Konto (iss, sub) die Bindung dieser
// Person ist und auth_time frisch ist. Alles andere legt nichts an.
func (h *KontoEinstellungen) frischAbschliessen(ctx context.Context, w http.ResponseWriter, r *http.Request, z anmeldeZustand, kuerzel string, idt *oidc.IDToken) {
	s, sitzung, ok := h.sitzungDesVorgangs(ctx, w, r, z.Sitzung, "Frische Anmeldung")
	if !ok {
		return
	}
	email := storage.NormalizeEmail(s.Email)
	uid, err := h.Bindungen.NutzerID(ctx, email)
	if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
		writeError(w, http.StatusInternalServerError, "user lookup failed", err)
		return
	}
	detail := "anbieter=" + kuerzel + ";aktion=" + z.Aktion + ";ergebnis="
	ergebnis := KontoErgebnisNichtBestaetigt
	fehlschlag := func(grund string, bremse bool) {
		h.audit(ctx, uid, AuditActionKontoFrischFehler, detail+grund)
		if bremse {
			adr := h.gegenstelle.proxy.clientAdresse(r)
			h.gegenstelle.fehlschlag("a:" + adr)
			h.personen.fehlschlag("k:" + email)
		}
		log.Printf("aboard-server: konto: frische Anmeldung bei %q nicht anerkannt (Person %s, Grund %s)", kuerzel, uid, grund)
		h.zurueck(w, r, ergebnis)
	}
	if uid == "" {
		fehlschlag("person", false)
		return
	}
	// Ein Konto, das nicht aktiv ist, bekommt keinen Nachweis (B3).
	if k, kerr := h.Konten.FuerAnmeldung(ctx, email); kerr == nil && k.Zustand != storage.KontoAktiv {
		fehlschlag("konto_zustand", false)
		return
	} else if kerr != nil && !errors.Is(kerr, storage.ErrKontoUnbekannt) {
		writeError(w, http.StatusInternalServerError, "account lookup failed", kerr)
		return
	}
	// Dasselbe Anbieter-Konto wie die bestehende Bindung dieser Person.
	b, err := h.Bindungen.NachKonto(ctx, idt.Issuer, idt.Subject)
	switch {
	case errors.Is(err, storage.ErrBindungUnbekannt):
		fehlschlag("sub", true)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "binding lookup failed", err)
		return
	case b.UserID == "" || b.UserID != uid:
		fehlschlag("sub", true)
		return
	}
	// auth_time Pflicht (fail closed): ohne sie ist nicht belegt, dass die
	// Person sich gerade eben angemeldet hat.
	// ADR-0149, Nachtrag 2026-11-09 (#1101): Fehlt auth_time und ist
	// ABOARD_OIDC_REAUTH_FALLBACK an, tritt das Ausstellungsdatum des Tokens
	// (iat) an seine Stelle — es darf nicht vor dem Beginn dieses Vorgangs
	// liegen. Liefert der Anbieter auth_time, gilt immer die strenge Prüfung.
	jetzt := h.uhr()
	gut := "ok"
	at, ok := authTime(idt)
	switch {
	case ok:
		t := time.Unix(at, 0)
		if jetzt.Sub(t) > authTimeHoechstalter || t.Sub(jetzt) > authTimeVorlauf {
			fehlschlag("auth_time_alt", false)
			return
		}
	case authTimeVorhanden(idt):
		fehlschlag("auth_time_ungueltig", false)
		return
	case !h.ReauthFallback:
		ergebnis = KontoErgebnisNichtUnterstuetzt
		fehlschlag("auth_time_fehlt", false)
		return
	default:
		iat := idt.IssuedAt
		beginn := time.Unix(z.Bis, 0).Add(-anmeldungGueltig)
		if iat.IsZero() || iat.Before(beginn.Add(-authTimeVorlauf)) ||
			jetzt.Sub(iat) > authTimeHoechstalter || iat.Sub(jetzt) > authTimeVorlauf {
			ergebnis = KontoErgebnisVorgangAlt
			fehlschlag("iat_alt", false)
			return
		}
		gut = "ok_ohne_auth_time"
	}
	id, err := zufall()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random failed")
		return
	}
	n := nachweis{ID: id, Email: email, Sitzung: storage.SitzungsHash(sitzung), Aktion: z.Aktion, Bis: jetzt.Add(frischGueltig).Unix()}
	roh, _ := json.Marshal(n)
	versiegelt, err := h.Box.Seal(roh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "seal failed", err)
		return
	}
	h.frischMu.Lock()
	if h.frisch == nil {
		h.frisch = map[string]int64{}
	}
	for k, bis := range h.frisch {
		if jetzt.Unix() >= bis {
			delete(h.frisch, k)
		}
	}
	h.frisch[id] = n.Bis
	h.frischMu.Unlock()
	setzeFrischCookie(w, versiegelt, int(frischGueltig.Seconds()))
	h.audit(ctx, uid, AuditActionKontoFrischOk, detail+gut)
	// F-2944: Der Weg steht im Serverlog (ok = auth_time, ok_ohne_auth_time =
	// iat-Ersatzweg), damit ein still herabgestufter Anbieter auffällt.
	log.Printf("aboard-server: konto: frische Anmeldung bei %q bestätigt (Person %s, Aktion %s, Weg %s)", kuerzel, uid, z.Aktion, gut)
	h.zurueck(w, r, KontoErgebnisBestaetigt)
}
