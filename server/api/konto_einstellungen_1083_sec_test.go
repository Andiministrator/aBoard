package api

// Nacharbeit aus der Sicherheitsprüfung (Karte #1083): Angriffe müssen
// scheitern (P1), Vorbedingungen vor der Bestätigung (P3a), Audit (P3b),
// alte Sitzung nie gültig, wenn das Ausstellen scheitert (P3c). Testdaten nur
// example.org.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/Andiministrator/aBoard/server/storage"
)

// pmMitBindung: Person nur mit Anbieter (kein Passwort), auf der Vorab-Liste.
func pmMitBindung(t *testing.T) (*kontoTest, *http.Cookie) {
	t.Helper()
	k := neuerKontoTest(t, "pm@example.org")
	k.person(t, "pm@example.org", false)
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-opfer', 'google', 'u-pm@example.org', 1)`, k.google.srv.URL)
	return k, k.sitzung(t, "pm@example.org")
}

func (k *kontoTest) opferBindung(t *testing.T) string {
	t.Helper()
	var sub string
	if err := k.db.QueryRow(`SELECT sub FROM anbieter_bindungen WHERE user_id = 'u-pm@example.org' AND anbieter = 'google'`).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	return sub
}

// Angriff A: gestohlene Sitzung, eigenes Google-Konto ersetzt die Bindung des
// Opfers. Muss scheitern: ohne Nachweis „frisch bestätigt“ 409 schon am Start
// (seit #1098 frische_anmeldung_noetig), und ein von Hand gebauter Rückruf
// ohne das Merkmal Frisch (zweite Schranke) bindet ebenfalls nichts.
func TestSec1083_UebernahmeDurchErsetzenScheitert(t *testing.T) {
	k, gestohlen := pmMitBindung(t)
	if rec := k.post("/wege/verknuepfen", map[string]string{"anbieter": "google"}, gestohlen); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != kennungFrischNoetig {
		t.Fatalf("Start ohne Passwort: %d %s", rec.Code, rec.Body.String())
	}
	// Zweite Schranke: ein gültig signierter link-Zustand der gestohlenen Sitzung.
	a := k.tabelle.Finde("google")
	verifier := oauth2.GenerateVerifier()
	z, err := a.zustandKodieren(anmeldeZustand{State: "st", Nonce: "no", Verifier: verifier, Bis: time.Now().Add(time.Minute).Unix(),
		Kuerzel: "google", Zweck: ZweckLink, Sitzung: storage.SitzungsHash(gestohlen.Value)})
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "nonce": {"no"}, "state": {"st"}}
	rec := k.rueckruf(t, k.google, "google", q, "a1", map[string]any{"sub": "sub-angreifer", "email": "angreifer@example.org"},
		&http.Cookie{Name: AnmeldungCookie, Value: z}, gestohlen)
	if e := ergebnis(t, rec); e != KontoErgebnisFehler {
		t.Fatalf("Rückruf: %q", e)
	}
	if k.opferBindung(t) != "sub-opfer" || !k.sitzungGueltig(gestohlen) {
		t.Fatal("Bindung oder Sitzung des Opfers verändert")
	}
	// Das Konto des Angreifers kommt nicht herein.
	lc, lq := k.starte(t, "google")
	if rec := k.melde(t, k.google, "google", lc, lq, "a2", map[string]any{"sub": "sub-angreifer", "email": "angreifer@example.org", "email_verified": true}); sitzungsCookie(rec) != nil {
		t.Fatal("Angreifer meldet sich an")
	}
}

// Angriff A2: zweiten Anbieter binden, dann den ersten lösen.
func TestSec1083_UebernahmeUeberZweitenAnbieterScheitert(t *testing.T) {
	k, gestohlen := pmMitBindung(t)
	if rec := k.post("/wege/verknuepfen", map[string]string{"anbieter": "authelia"}, gestohlen); rec.Code != http.StatusConflict {
		t.Fatalf("verknüpfen: %d", rec.Code)
	}
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "google"}, gestohlen); rec.Code != http.StatusConflict {
		t.Fatalf("lösen: %d", rec.Code)
	}
	if k.bindungsZahl(t, "pm@example.org") != 1 || k.opferBindung(t) != "sub-opfer" {
		t.Fatal("Bindung verändert")
	}
}

// Angriff B (P3a): Ein Fehlschlag verbraucht keinen Wiederherstellungscode.
func TestSec1083_FehlschlagVerbrauchtKeinenCode(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	offen := func() int {
		n, _ := k.konten.OffeneCodes(context.Background(), k.annaKonto)
		return n
	}
	vorher := offen()
	// Lösen ohne Bindung → 404, Code unberührt.
	if r := k.post("/wege/loesen", map[string]string{"anbieter": "authelia", "aktuell": kontoPw, "code": k.codes[0]}, c); r.Code != http.StatusNotFound {
		t.Fatalf("lösen: %d", r.Code)
	}
	// Unbekannter Anbieter → 404.
	if r := k.post("/wege/verknuepfen", map[string]string{"anbieter": "github", "aktuell": kontoPw, "code": k.codes[1]}, c); r.Code != http.StatusNotFound {
		t.Fatalf("verknüpfen: %d", r.Code)
	}
	// Anbieter nicht erreichbar → 503, Code unberührt.
	k.google.srv.Close()
	if r := k.post("/wege/verknuepfen", map[string]string{"anbieter": "google", "aktuell": kontoPw, "code": k.codes[2]}, c); r.Code != http.StatusServiceUnavailable || antwort(t, r)["fehler"] != "nicht_erreichbar" {
		t.Fatalf("Anbieter weg: %d %s", r.Code, r.Body.String())
	}
	if nachher := offen(); nachher != vorher {
		t.Fatalf("Codes %d → %d: ein Fehlschlag hat einen Code verbraucht", vorher, nachher)
	}
}

// Angriff C: Der Zwischenstand der 2FA-Erneuerung ist nach einem
// Passwortwechsel (neue Sitzung) unbrauchbar.
func TestSec1083_ZwischenstandNachPasswortwechsel(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	r := k.post("/2fa/start", map[string]string{"aktuell": kontoPw, "code": k.code()}, c)
	if r.Code != http.StatusOK {
		t.Fatalf("start: %d", r.Code)
	}
	zw := antwort(t, r)["zwischen"].(string)
	r = k.post("/passwort", map[string]string{"aktuell": kontoPw, "code": k.code(), "neu": "ganz-neues-passwort-2"}, c)
	if r.Code != http.StatusOK {
		t.Fatalf("passwort: %d %s", r.Code, r.Body.String())
	}
	neu := sitzungsCookie(r)
	if neu == nil || k.sitzungGueltig(c) || !k.sitzungGueltig(neu) {
		t.Fatal("Neuausstellung kaputt")
	}
	if r := k.post("/2fa/abschluss", map[string]string{"zwischen": zw, "code": "000000"}, neu); r.Code != http.StatusBadRequest {
		t.Fatalf("Zwischenstand nach Passwortwechsel noch angenommen: %d", r.Code)
	}
}

// Ersetzen und Lösen beenden alle anderen Sitzungen und stellen die laufende
// neu aus (wie beim Passwortwechsel).
func TestSec1083_ErsetzenUndLoesenBeendenAndereSitzungen(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-alt', 'google', 'u-anna@example.org', 1)`, k.google.srv.URL)
	lauf, andere := k.sitzung(t, "anna@example.org"), k.sitzung(t, "anna@example.org")
	ac, q := k.linkStart(t, "google", map[string]string{"aktuell": kontoPw, "code": k.code()}, lauf)
	rec := k.rueckruf(t, k.google, "google", q, "e1", map[string]any{"sub": "sub-neu"}, ac, lauf)
	if e := ergebnis(t, rec); e != KontoErgebnisErsetzt {
		t.Fatalf("ersetzen: %q", e)
	}
	neu := sitzungsCookie(rec)
	if neu == nil || neu.Value == lauf.Value || k.sitzungGueltig(lauf) || k.sitzungGueltig(andere) || !k.sitzungGueltig(neu) {
		t.Fatal("Sitzungen nach Ersetzen falsch")
	}
	// Lösen: zweite Bindung (anderer Anbieter) anlegen, damit es etwas zu lösen gibt.
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-z', 'authelia', 'u-anna@example.org', 1)`, k.authelia.srv.URL)
	weitere := k.sitzung(t, "anna@example.org")
	rec = k.post("/wege/loesen", map[string]string{"anbieter": "authelia", "aktuell": kontoPw, "code": k.code()}, neu)
	if rec.Code != http.StatusOK {
		t.Fatalf("lösen: %d %s", rec.Code, rec.Body.String())
	}
	neu2 := sitzungsCookie(rec)
	if neu2 == nil || k.sitzungGueltig(neu) || k.sitzungGueltig(weitere) || !k.sitzungGueltig(neu2) {
		t.Fatal("Sitzungen nach Lösen falsch")
	}
}

// P3b: Jede Änderung steht im Audit, mit Person-ID, ohne Adresse.
func TestSec1083_AuditEintraege(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	k.ke.Audit = storage.NewConfigRepo(k.db, nil, uuid.NewString)
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-a', 'authelia', 'u-anna@example.org', 1)`, k.authelia.srv.URL)
	c := k.sitzung(t, "anna@example.org")
	pw := kontoPw
	best := func() map[string]string { return map[string]string{"aktuell": pw, "code": k.code()} }
	merge := func(m map[string]string, a, b string) map[string]string { m[a] = b; return m }

	k.post("/passwort", merge(best(), "neu", "ganz-neues-passwort-1"), c)
	pw = "ganz-neues-passwort-1"
	k.ke.pruefen = func(_ context.Context, p, hash string) (bool, error) { return hash == "h:"+p, nil }
	neu := k.sitzung(t, "anna@example.org")
	// falsche Bestätigung
	k.post("/codes", map[string]string{"aktuell": "falsch-falsch-falsch", "code": "000000"}, neu)
	k.post("/codes", best(), neu)
	k.post("/sitzungen/beenden", nil, neu)
	if r := k.post("/wege/loesen", merge(best(), "anbieter", "authelia"), neu); sitzungsCookie(r) != nil {
		neu = sitzungsCookie(r) // Lösen stellt die Sitzung neu aus
	} else {
		t.Fatalf("lösen: %d", r.Code)
	}
	// 2FA neu
	zr := k.post("/2fa/start", best(), neu)
	zw := antwort(t, zr)["zwischen"].(string)
	roh, _ := k.box.Open(zw)
	var zs zwischenstand
	_ = json.Unmarshal(roh, &zs)
	g, _ := base64.StdEncoding.DecodeString(zs.Geheimnis)
	k.post("/2fa/abschluss", map[string]string{"zwischen": zw, "code": totpCode(g, TOTPSchritt(k.uhr))}, neu)

	rows, err := k.db.Query(`SELECT key, user_id, action, COALESCE(new_value_redacted, '') FROM server_config_audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	gesehen := map[string]bool{}
	for rows.Next() {
		var key, uid, action, neuW string
		if err := rows.Scan(&key, &uid, &action, &neuW); err != nil {
			t.Fatal(err)
		}
		gesehen[action] = true
		if key != "personen/u-anna@example.org" || uid != "u-anna@example.org" {
			t.Errorf("%s: key=%q user=%q", action, key, uid)
		}
		if strings.Contains(key+uid+action+neuW, "@example") && !strings.HasPrefix(key, "personen/u-") {
			t.Errorf("%s: Adresse im Audit: %s", action, neuW)
		}
		if strings.Contains(neuW, kontoPw) || strings.Contains(neuW, "ganz-neues") {
			t.Errorf("%s: Geheimnis im Audit", action)
		}
	}
	for _, w := range []string{AuditActionKontoPasswort, AuditActionKontoCodes, AuditActionKontoSitzungenEnde, AuditActionKontoWegGeloest,
		AuditActionKonto2FA, AuditActionKontoBestaetigungFalsch} {
		if !gesehen[w] {
			t.Errorf("Audit-Eintrag %s fehlt (gesehen: %v)", w, gesehen)
		}
	}
}

// Eine Abweisung frische_anmeldung_noetig (früher nur_mit_passwort) wird nicht auditiert: Sie kommt ohne
// Bestätigungsversuch und ließe sich ungebremst wiederholen.
func TestSec1083_AbweisungOhneAudit(t *testing.T) {
	k, c := pmMitBindung(t)
	k.ke.Audit = storage.NewConfigRepo(k.db, nil, uuid.NewString)
	for i := 0; i < 5; i++ {
		if r := k.post("/wege/loesen", map[string]string{"anbieter": "google"}, c); r.Code != http.StatusConflict {
			t.Fatalf("Abweisung: %d", r.Code)
		}
	}
	var n int
	_ = k.db.QueryRow(`SELECT COUNT(*) FROM server_config_audit`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d Audit-Zeilen nach reinen Abweisungen, erwartet 0", n)
	}
}

func TestSec1083_AuditVerknuepfen(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	k.ke.Audit = storage.NewConfigRepo(k.db, nil, uuid.NewString)
	c := k.sitzung(t, "anna@example.org")
	ac, q := k.linkStart(t, "google", map[string]string{"aktuell": kontoPw, "code": k.code()}, c)
	if e := ergebnis(t, k.rueckruf(t, k.google, "google", q, "v1", map[string]any{"sub": "sub-v"}, ac, c)); e != KontoErgebnisVerknuepft {
		t.Fatal(e)
	}
	var d string
	if err := k.db.QueryRow(`SELECT new_value_redacted FROM server_config_audit WHERE action = ?`, AuditActionKontoWegVerknuepft).Scan(&d); err != nil || d != "anbieter=google;ergebnis=ok" {
		t.Fatalf("Audit: %q %v", d, err)
	}
}

// kaputteSitzungen: Anlegen scheitert.
type kaputteSitzungen struct{ KontoSitzungen }

func (kaputteSitzungen) Anlegen(context.Context, string, string) (string, time.Time, error) {
	return "", time.Time{}, errors.New("speicher kaputt")
}

// P3c: Scheitert das Ausstellen der neuen Sitzung, meldet der Endpunkt 500, setzt
// keine Sitzung — und die alte Kennung gilt nicht mehr (sie fiel in derselben
// Transaktion wie die Änderung).
func TestSec1083_AlteSitzungNieGueltigWennAusstellenScheitert(t *testing.T) {
	k := neuerKontoTest(t, "")
	id := k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	k.ke.Sitzungen = kaputteSitzungen{k.sitzungen}
	rec := k.post("/passwort", map[string]string{"aktuell": kontoPw, "code": k.code(), "neu": "ganz-neues-passwort-1"}, c)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("Status %d, erwartet 500", rec.Code)
	}
	if sitzungsCookie(rec) != nil {
		t.Error("trotz Fehler eine Sitzung gesetzt")
	}
	if k.sitzungGueltig(c) {
		t.Fatal("alte Sitzung gilt noch")
	}
	if k.hashVon(t, id) != "h:ganz-neues-passwort-1" {
		t.Error("Änderung nicht gespeichert")
	}
}
