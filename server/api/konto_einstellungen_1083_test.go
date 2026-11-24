package api

// Karte #1083: Konto-Einstellungen (ADR-0149 §3, §4, Nachtrag #1083).
// Aufbau wie in main: echter Speicher, Sitzungsweiche und CSRF-Schutz um den
// Mux, zwei Test-Anbieter (google, authelia). Testdaten nur example.org.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

type kontoTest struct {
	*einloeseTest
	ke        *KontoEinstellungen
	h         http.Handler
	box       *secretcrypto.Box
	bindungen *storage.AnbieterBindungRepo
	uhr       time.Time
	geheimnis []byte // 2FA-Geheimnis von anna
	codes     []string
	annaKonto string
}

const kontoPw = "richtiges-passwort-1"

func neuerKontoTest(t *testing.T, liste string) *kontoTest {
	t.Helper()
	t.Setenv("ABOARD_AUTH_MODE", "builtin")
	x := neuerEinloeseTest(t, liste)
	box, err := secretcrypto.New(testSchluessel1077)
	if err != nil {
		t.Fatal(err)
	}
	k := &kontoTest{einloeseTest: x, box: box, uhr: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	k.bindungen = storage.NewAnbieterBindungRepo(x.db, NutzerAnlegen(storage.NewBootstrapRepo(x.db)))
	k.ke, err = NeueKontoEinstellungen(x.konten, k.bindungen, x.sitzungen, x.tabelle, box, "")
	if err != nil {
		t.Fatal(err)
	}
	k.ke.jetzt = func() time.Time { return k.uhr }
	k.ke.pruefen = func(_ context.Context, pw, hash string) (bool, error) { return hash == "h:"+pw, nil }
	k.ke.hashen = func(_ context.Context, pw string) (string, error) {
		if err := PasswortZulaessig(pw); err != nil {
			return "", err
		}
		return "h:" + pw, nil
	}
	k.ke.Anmelden(x.mux)
	weiche := &Sitzungsweiche{Sitzungen: x.sitzungen, Zugelassen: x.tor.Erlaubt}
	k.h = weiche.Middleware(MitCSRFSchutz(x.mux))
	return k
}

// person legt eine Person mit users-Zeile an; mitKonto: aktives Konto mit
// Passwort kontoPw und 2FA (Geheimnis und zehn Codes werden gemerkt).
func (k *kontoTest) person(t *testing.T, email string, mitKonto bool) string {
	t.Helper()
	k.exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, '', '2026-01-01T00:00:00Z', 0)`, "u-"+email, email)
	if !mitKonto {
		return ""
	}
	e, err := NeueTOTPEinrichtung(email)
	if err != nil {
		t.Fatal(err)
	}
	versiegelt, err := k.box.Seal(e.Geheimnis)
	if err != nil {
		t.Fatal(err)
	}
	id := "k-" + email
	k.exec(`INSERT INTO konten (id, email, zustand, weg, passwort_hash, totp_geheimnis, angelegt, geaendert) VALUES (?, ?, 'active', 'password', ?, ?, 1, 1)`,
		id, email, "h:"+kontoPw, versiegelt)
	for _, h := range e.CodeHashes {
		k.exec(`INSERT INTO konto_codes (konto_id, code_hash, angelegt) VALUES (?, ?, 1)`, id, h)
	}
	k.geheimnis, k.codes, k.annaKonto = e.Geheimnis, e.Codes, id
	return id
}

func (k *kontoTest) sitzung(t *testing.T, email string) *http.Cookie {
	t.Helper()
	id, _, err := k.sitzungen.Anlegen(context.Background(), email, "")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: SitzungsCookie, Value: id}
}

// code liefert den aktuellen TOTP-Code von anna und rückt die Uhr um zwei
// Minuten vor (jeder Zeitschritt gilt einmal).
func (k *kontoTest) code() string {
	k.uhr = k.uhr.Add(2 * time.Minute)
	return totpCode(k.geheimnis, TOTPSchritt(k.uhr))
}

func (k *kontoTest) anfrage(method, pfad string, body any, cs ...*http.Cookie) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		roh, _ := json.Marshal(body)
		rd = bytes.NewReader(roh)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "https://board.example"+pfad, rd)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.RemoteAddr = "192.0.2.7:4711"
	for _, c := range cs {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	return rec
}

func (k *kontoTest) post(pfad string, body any, cs ...*http.Cookie) *httptest.ResponseRecorder {
	return k.anfrage(http.MethodPost, KontoPfad+pfad, body, cs...)
}

func antwort(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func (k *kontoTest) sitzungGueltig(c *http.Cookie) bool {
	_, err := k.sitzungen.Pruefen(context.Background(), c.Value)
	return err == nil
}

func (k *kontoTest) hashVon(t *testing.T, id string) string {
	t.Helper()
	var h string
	if err := k.db.QueryRow(`SELECT passwort_hash FROM konten WHERE id = ?`, id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func (k *kontoTest) bindungsZahl(t *testing.T, email string) int {
	t.Helper()
	var n int
	if err := k.db.QueryRow(`SELECT COUNT(*) FROM anbieter_bindungen b JOIN users u ON u.id = b.user_id WHERE u.email = ?`, email).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---------------------------------------------------------------------------

func TestKonto1083_OhneSitzungKeinZugriff(t *testing.T) {
	k := neuerKontoTest(t, "")
	for _, p := range []string{"/passwort", "/2fa/start", "/2fa/abschluss", "/codes", "/sitzungen/beenden", "/wege/verknuepfen", "/wege/loesen"} {
		if rec := k.post(p, map[string]string{}); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s ohne Sitzung: %d, erwartet 401", p, rec.Code)
		}
	}
	if rec := k.anfrage(http.MethodGet, KontoPfad, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET ohne Sitzung: %d", rec.Code)
	}
}

// CSRF: Jeder schreibende Weg lehnt eine Anfrage von einer fremden Seite ab,
// auch mit gültiger Sitzung und richtiger Bestätigung.
func TestKonto1083_CSRF(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	for _, p := range []string{"/passwort", "/2fa/start", "/2fa/abschluss", "/codes", "/sitzungen/beenden", "/wege/verknuepfen", "/wege/loesen"} {
		roh, _ := json.Marshal(map[string]string{"aktuell": kontoPw, "neu": "ganz-neues-passwort-1", "anbieter": "google"})
		r := httptest.NewRequest(http.MethodPost, "https://board.example"+KontoPfad+p, bytes.NewReader(roh))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Origin", "https://boese.example")
		r.AddCookie(c)
		rec := httptest.NewRecorder()
		k.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s von fremder Seite: %d, erwartet 403", p, rec.Code)
		}
	}
	if k.hashVon(t, k.annaKonto) != "h:"+kontoPw {
		t.Error("Passwort durch fremde Anfrage geändert")
	}
	if !k.sitzungGueltig(c) {
		t.Error("Sitzung durch fremde Anfrage beendet")
	}
}

func TestKonto1083_Stand(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-a', 'google', 'u-anna@example.org', 1)`, k.google.srv.URL)
	c := k.sitzung(t, "anna@example.org")
	rec := k.anfrage(http.MethodGet, KontoPfad, nil, c)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Stand: %d %v", rec.Code, rec.Header())
	}
	var s kontoStand
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Email != "anna@example.org" || !s.Konto || !s.Passwort || !s.ZweiterFaktor || s.CodesOffen != WiederherstellungsAnzahl ||
		!s.NachFrage.Passwort || !s.NachFrage.Code {
		t.Errorf("Stand: %+v", s)
	}
	if len(s.Wege) != 2 || !s.Wege[0].Verknuepft || !s.Wege[0].Loesbar || s.Wege[1].Verknuepft || s.Wege[1].Anbieter != "authelia" {
		t.Errorf("Wege: %+v", s.Wege)
	}
	if strings.Contains(rec.Body.String(), "h:") || strings.Contains(rec.Body.String(), "sub-a") {
		t.Errorf("Stand verrät Hash oder Kennung: %s", rec.Body.String())
	}
}

func TestKonto1083_PasswortAendern(t *testing.T) {
	k := neuerKontoTest(t, "")
	id := k.person(t, "anna@example.org", true)
	lauf := k.sitzung(t, "anna@example.org")
	anderes := k.sitzung(t, "anna@example.org")
	fremd := k.sitzung(t, "anna@example.org")
	_ = fremd
	neu := "ganz-neues-passwort-1"

	// Falsches Passwort, fehlender Code: nichts ändert sich.
	if rec := k.post("/passwort", map[string]string{"aktuell": "falsch-falsch-falsch", "code": k.code(), "neu": neu}, lauf); rec.Code != http.StatusForbidden || antwort(t, rec)["fehler"] != "falsch" {
		t.Fatalf("falsches Passwort: %d %s", rec.Code, rec.Body.String())
	}
	if rec := k.post("/passwort", map[string]string{"aktuell": kontoPw, "neu": neu}, lauf); rec.Code != http.StatusForbidden {
		t.Fatalf("ohne Code: %d", rec.Code)
	}
	if rec := k.post("/passwort", map[string]string{"aktuell": kontoPw, "code": "000000", "neu": neu}, lauf); rec.Code != http.StatusForbidden {
		t.Fatalf("falscher Code: %d", rec.Code)
	}
	if k.hashVon(t, id) != "h:"+kontoPw || !k.sitzungGueltig(anderes) {
		t.Fatal("Fehlversuch hat etwas geändert")
	}
	// Zu kurz: abgewiesen, bevor ein Code verbraucht wird.
	code := k.code()
	if rec := k.post("/passwort", map[string]string{"aktuell": kontoPw, "code": code, "neu": "kurz"}, lauf); rec.Code != http.StatusBadRequest || antwort(t, rec)["fehler"] != "zu_kurz" {
		t.Fatalf("zu kurz: %d %s", rec.Code, rec.Body.String())
	}
	// Derselbe Code gilt danach noch.
	rec := k.post("/passwort", map[string]string{"aktuell": kontoPw, "code": code, "neu": neu}, lauf)
	if rec.Code != http.StatusOK {
		t.Fatalf("Passwort ändern: %d %s", rec.Code, rec.Body.String())
	}
	if k.hashVon(t, id) != "h:"+neu {
		t.Error("Hash nicht gespeichert")
	}
	// Die laufende Sitzung ist neu ausgestellt (neue Kennung), die alte und alle
	// anderen sind beendet — die Person bleibt angemeldet.
	neuC := sitzungsCookie(rec)
	if neuC == nil || neuC.Value == lauf.Value {
		t.Fatal("laufende Sitzung nicht neu ausgestellt")
	}
	if k.sitzungGueltig(lauf) || k.sitzungGueltig(anderes) || k.sitzungGueltig(fremd) {
		t.Error("alte Sitzung gilt noch")
	}
	if !k.sitzungGueltig(neuC) {
		t.Error("neue Sitzung gilt nicht")
	}
	if rec := k.anfrage(http.MethodGet, KontoPfad, nil, neuC); rec.Code != http.StatusOK {
		t.Errorf("mit neuer Sitzung: %d", rec.Code)
	}
}

// Ein Wiederherstellungscode bestätigt ebenfalls (Handy weg), und gilt einmal.
func TestKonto1083_BestaetigungMitWiederherstellungscode(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	if rec := k.post("/sitzungen/beenden", nil, c); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	rec := k.post("/codes", map[string]string{"aktuell": kontoPw, "code": k.codes[0]}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("mit Wiederherstellungscode: %d %s", rec.Code, rec.Body.String())
	}
	if rec := k.post("/codes", map[string]string{"aktuell": kontoPw, "code": k.codes[0]}, c); rec.Code != http.StatusForbidden {
		t.Errorf("Code zweimal: %d", rec.Code)
	}
}

func TestKonto1083_Bremse(t *testing.T) {
	k := neuerKontoTest(t, "")
	id := k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	for i := 0; i < KontoMaxFehler; i++ {
		if rec := k.post("/passwort", map[string]string{"aktuell": "falsch-falsch-falsch", "code": k.code(), "neu": "ganz-neues-passwort-1"}, c); rec.Code != http.StatusForbidden {
			t.Fatalf("Fehlversuch %d: %d", i, rec.Code)
		}
	}
	// Jetzt gesperrt — auch mit allem Richtigen.
	rec := k.post("/passwort", map[string]string{"aktuell": kontoPw, "code": k.code(), "neu": "ganz-neues-passwort-1"}, c)
	if rec.Code != http.StatusTooManyRequests || antwort(t, rec)["fehler"] != "gesperrt" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("nach %d Fehlversuchen: %d %s", KontoMaxFehler, rec.Code, rec.Body.String())
	}
	if k.hashVon(t, id) != "h:"+kontoPw {
		t.Error("Passwort trotz Sperre geändert")
	}
	// Die Sperre gilt für die Einstellungen, nicht für die Anmeldung: Andere
	// Wege (Sitzung beenden braucht keine Bestätigung) laufen weiter.
	if rec := k.post("/sitzungen/beenden", nil, c); rec.Code != http.StatusOK {
		t.Errorf("Sitzungen beenden bei Sperre: %d", rec.Code)
	}
	// Nach dem Fenster geht es wieder.
	k.ke.gegenstelle.jetzt = func() time.Time { return time.Now().Add(KontoFenster + time.Minute) }
	k.ke.personen.jetzt = k.ke.gegenstelle.jetzt
	if rec := k.post("/passwort", map[string]string{"aktuell": kontoPw, "code": k.code(), "neu": "ganz-neues-passwort-1"}, c); rec.Code != http.StatusOK {
		t.Errorf("nach dem Fenster: %d %s", rec.Code, rec.Body.String())
	}
}

func TestKonto1083_ZweiFaktorNeu(t *testing.T) {
	k := neuerKontoTest(t, "")
	id := k.person(t, "anna@example.org", true)
	lauf := k.sitzung(t, "anna@example.org")
	anderes := k.sitzung(t, "anna@example.org")

	// Start mit falschem Passwort: nichts entsteht.
	if rec := k.post("/2fa/start", map[string]string{"aktuell": "falsch-falsch-falsch", "code": k.code()}, lauf); rec.Code != http.StatusForbidden {
		t.Fatalf("Start falsch: %d", rec.Code)
	}
	rec := k.post("/2fa/start", map[string]string{"aktuell": kontoPw, "code": k.code()}, lauf)
	if rec.Code != http.StatusOK {
		t.Fatalf("Start: %d %s", rec.Code, rec.Body.String())
	}
	st := antwort(t, rec)
	zw, _ := st["zwischen"].(string)
	if zw == "" || st["geheimnis"] == "" || !strings.HasPrefix(st["link"].(string), "otpauth://totp/") || !strings.Contains(st["qr"].(string), "<svg") {
		t.Fatalf("Start-Antwort unvollständig: %v", st)
	}
	// Das alte Geheimnis gilt bis zur Bestätigung unverändert.
	altVers, _, _ := k.ke.Konten.TOTPGeheimnis(context.Background(), id)
	alt, _ := k.box.Open(altVers)
	if !bytes.Equal(alt, k.geheimnis) {
		t.Fatal("Geheimnis schon vor der Bestätigung ersetzt")
	}
	// Das neue Geheimnis aus dem Zwischenstand (der Test öffnet das Siegel).
	roh, err := k.box.Open(zw)
	if err != nil {
		t.Fatal(err)
	}
	var z zwischenstand
	_ = json.Unmarshal(roh, &z)
	neuG := mustB64(t, z.Geheimnis)

	// Code aus der ALTEN App taugt nicht; ein falscher Code ändert nichts.
	if rec := k.post("/2fa/abschluss", map[string]string{"zwischen": zw, "code": totpCode(k.geheimnis, TOTPSchritt(k.uhr))}, lauf); rec.Code != http.StatusForbidden {
		t.Fatalf("Code der alten App: %d", rec.Code)
	}
	// Zwischenstand einer anderen Sitzung: abgewiesen.
	if rec := k.post("/2fa/abschluss", map[string]string{"zwischen": zw, "code": totpCode(neuG, TOTPSchritt(k.uhr))}, anderes); rec.Code != http.StatusBadRequest {
		t.Fatalf("fremde Sitzung: %d", rec.Code)
	}
	// Abgelaufen: abgewiesen.
	jetzt := k.uhr
	k.uhr = jetzt.Add(kontoZwischenGueltig + time.Minute)
	if rec := k.post("/2fa/abschluss", map[string]string{"zwischen": zw, "code": totpCode(neuG, TOTPSchritt(k.uhr))}, lauf); rec.Code != http.StatusBadRequest {
		t.Fatalf("abgelaufen: %d", rec.Code)
	}
	k.uhr = jetzt
	if v, _, _ := k.ke.Konten.TOTPGeheimnis(context.Background(), id); v != altVers {
		t.Fatal("Geheimnis nach Fehlversuchen verändert")
	}
	// Richtiger Code aus der neuen App.
	rec = k.post("/2fa/abschluss", map[string]string{"zwischen": zw, "code": totpCode(neuG, TOTPSchritt(k.uhr))}, lauf)
	if rec.Code != http.StatusOK {
		t.Fatalf("Abschluss: %d %s", rec.Code, rec.Body.String())
	}
	codes, _ := antwort(t, rec)["codes"].([]any)
	if len(codes) != WiederherstellungsAnzahl {
		t.Fatalf("Codes: %v", codes)
	}
	neuVers, schritt, _ := k.ke.Konten.TOTPGeheimnis(context.Background(), id)
	g, err := k.box.Open(neuVers)
	if err != nil || !bytes.Equal(g, neuG) {
		t.Fatalf("neues Geheimnis nicht gespeichert: %v", err)
	}
	if schritt != TOTPSchritt(k.uhr) {
		t.Errorf("verbrauchter Schritt %d, erwartet %d", schritt, TOTPSchritt(k.uhr))
	}
	// Die alten Wiederherstellungscodes sind ungültig, die neuen gelten.
	if ok, _ := k.ke.Konten.CodeVerbrauchen(context.Background(), id, WiederherstellungsHash(k.codes[0]), 1); ok {
		t.Error("alter Wiederherstellungscode gilt noch")
	}
	if ok, _ := k.ke.Konten.CodeVerbrauchen(context.Background(), id, WiederherstellungsHash(codes[0].(string)), 1); !ok {
		t.Error("neuer Wiederherstellungscode gilt nicht")
	}
	// Sitzungen: laufende neu ausgestellt, andere beendet.
	neuC := sitzungsCookie(rec)
	if neuC == nil || neuC.Value == lauf.Value || k.sitzungGueltig(lauf) || k.sitzungGueltig(anderes) || !k.sitzungGueltig(neuC) {
		t.Error("Sitzungen nach 2FA-Wechsel falsch")
	}
	// Derselbe Zwischenstand lässt sich nicht noch einmal einlösen.
	k.uhr = k.uhr.Add(2 * time.Minute)
	if rec := k.post("/2fa/abschluss", map[string]string{"zwischen": zw, "code": totpCode(neuG, TOTPSchritt(k.uhr))}, neuC); rec.Code != http.StatusBadRequest && rec.Code != http.StatusConflict {
		t.Errorf("zweiter Abschluss: %d", rec.Code)
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKonto1083_CodesNeu(t *testing.T) {
	k := neuerKontoTest(t, "")
	id := k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	if rec := k.post("/codes", map[string]string{"aktuell": "falsch-falsch-falsch", "code": k.code()}, c); rec.Code != http.StatusForbidden {
		t.Fatalf("falsch: %d", rec.Code)
	}
	rec := k.post("/codes", map[string]string{"aktuell": kontoPw, "code": k.code()}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("codes: %d %s", rec.Code, rec.Body.String())
	}
	codes, _ := antwort(t, rec)["codes"].([]any)
	if len(codes) != WiederherstellungsAnzahl {
		t.Fatalf("Codes: %v", codes)
	}
	if ok, _ := k.ke.Konten.CodeVerbrauchen(context.Background(), id, WiederherstellungsHash(k.codes[1]), 1); ok {
		t.Error("alter Code gilt noch")
	}
	if ok, _ := k.ke.Konten.CodeVerbrauchen(context.Background(), id, WiederherstellungsHash(codes[3].(string)), 1); !ok {
		t.Error("neuer Code gilt nicht")
	}
	if !k.sitzungGueltig(c) {
		t.Error("Sitzung bei Codes neu beendet — soll bleiben")
	}
}

func TestKonto1083_AndereSitzungenBeenden(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "ben@example.org", true)
	k.person(t, "anna@example.org", true)
	lauf, a2, a3 := k.sitzung(t, "anna@example.org"), k.sitzung(t, "anna@example.org"), k.sitzung(t, "anna@example.org")
	ben := k.sitzung(t, "ben@example.org")
	rec := k.post("/sitzungen/beenden", nil, lauf)
	if rec.Code != http.StatusOK || antwort(t, rec)["beendet"] != float64(2) {
		t.Fatalf("beenden: %d %s", rec.Code, rec.Body.String())
	}
	if !k.sitzungGueltig(lauf) || k.sitzungGueltig(a2) || k.sitzungGueltig(a3) || !k.sitzungGueltig(ben) {
		t.Error("falsche Sitzungen beendet")
	}
}

// Wer kein Passwort hat (nur Anbieter oder Vorab-Liste), kann Passwort und 2FA
// nicht ändern; Verknüpfen läuft über die bestehende Sitzung.
func TestKonto1083_OhneKonto(t *testing.T) {
	k := neuerKontoTest(t, "pm@example.org")
	k.person(t, "pm@example.org", false)
	c := k.sitzung(t, "pm@example.org")
	var s kontoStand
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c).Body.Bytes(), &s)
	if s.Konto || s.Passwort || s.NachFrage.Passwort {
		t.Errorf("Stand: %+v", s)
	}
	for _, p := range []string{"/passwort", "/2fa/start", "/codes"} {
		if rec := k.post(p, map[string]string{"neu": "ganz-neues-passwort-1"}, c); rec.Code != http.StatusConflict {
			t.Errorf("%s ohne Konto: %d, erwartet 409", p, rec.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Verknüpfen

// linkStart beginnt das Verknüpfen und liefert Anmelde-Cookie und Parameter.
func (k *kontoTest) linkStart(t *testing.T, anbieter string, bestaetigung map[string]string, sitzung *http.Cookie, weitere ...*http.Cookie) (*http.Cookie, url.Values) {
	t.Helper()
	body := map[string]string{"anbieter": anbieter}
	for a, b := range bestaetigung {
		body[a] = b
	}
	rec := k.post("/wege/verknuepfen", body, append([]*http.Cookie{sitzung}, weitere...)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("Verknüpfen starten: %d %s", rec.Code, rec.Body.String())
	}
	ziel, _ := url.Parse(antwort(t, rec)["ziel"].(string))
	if ziel.Query().Get("prompt") != "select_account" {
		t.Errorf("prompt=%q, erwartet select_account", ziel.Query().Get("prompt"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == AnmeldungCookie && c.Value != "" {
			if !c.HttpOnly || !c.Secure {
				t.Errorf("Cookie-Attribute: %+v", c)
			}
			return c, ziel.Query()
		}
	}
	t.Fatal("kein Anmelde-Cookie")
	return nil, nil
}

// rueckruf ruft den Rückruf mit beliebigen Cookies auf.
func (k *kontoTest) rueckruf(t *testing.T, anb *testAnbieter, kuerzel string, q url.Values, code string, claims map[string]any, cs ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	anb.mu.Lock()
	anb.challenge[code] = q.Get("code_challenge")
	anb.claims = map[string]any{"nonce:" + code: q.Get("nonce")}
	for a, b := range claims {
		anb.claims[a] = b
	}
	anb.mu.Unlock()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungRueckrufPfad+"/"+kuerzel+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")), nil)
	for _, c := range cs {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	return rec
}

func ergebnis(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		return "kein-redirect:" + strings.TrimSpace(rec.Body.String())
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Path != "/" {
		t.Errorf("Ziel %q: nur die Wurzel ist erlaubt", loc.Path)
	}
	return loc.Query().Get(KontoErgebnisParam)
}

func TestKonto1083_Verknuepfen(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	best := map[string]string{"aktuell": kontoPw}

	// Ohne richtige Bestätigung beginnt nichts.
	if rec := k.post("/wege/verknuepfen", map[string]string{"anbieter": "google", "aktuell": "falsch-falsch-falsch", "code": k.code()}, c); rec.Code != http.StatusForbidden {
		t.Fatalf("falsche Bestätigung: %d", rec.Code)
	}
	if rec := k.post("/wege/verknuepfen", map[string]string{"anbieter": "github", "aktuell": kontoPw}, c); rec.Code != http.StatusNotFound {
		t.Fatalf("unbekannter Anbieter: %d", rec.Code)
	}
	best["code"] = k.code()
	ac, q := k.linkStart(t, "google", best, c)
	rec := k.rueckruf(t, k.google, "google", q, "l1", map[string]any{"sub": "sub-anna-g", "email": "irgendwer@example.com"}, ac, c)
	if e := ergebnis(t, rec); e != KontoErgebnisVerknuepft {
		t.Fatalf("Rückruf: %q", e)
	}
	if k.bindungsZahl(t, "anna@example.org") != 1 {
		t.Fatal("Bindung fehlt")
	}
	if sitzungsCookie(rec) != nil {
		t.Error("Verknüpfen hat eine Sitzung ausgestellt")
	}
	// Das Anmelde-Cookie ist verbraucht.
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == AnmeldungCookie && ck.MaxAge >= 0 {
			t.Error("Anmelde-Cookie nicht gelöscht")
		}
	}
	// Mit dem verknüpften Konto kommt Anna jetzt über Google herein, als Anna —
	// gleich, welche Adresse Google meldet (Bindung schlägt Adresse).
	lc, lq := k.starte(t, "google")
	if adr := k.sitzungsAdresse(t, k.melde(t, k.google, "google", lc, lq, "l2", map[string]any{"sub": "sub-anna-g", "email": "irgendwer@example.com"})); adr != "anna@example.org" {
		t.Fatalf("Anmeldung über die neue Bindung: %q", adr)
	}
	// Stand zeigt es.
	var s kontoStand
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c).Body.Bytes(), &s)
	if !s.Wege[0].Verknuepft {
		t.Errorf("Stand: %+v", s.Wege)
	}
}

func TestKonto1083_VerknuepfenBelegtUndErsetzen(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	k.person(t, "ben@example.org", false)
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-ben', 'google', 'u-ben@example.org', 1)`, k.google.srv.URL)
	c := k.sitzung(t, "anna@example.org")

	// Das Anbieter-Konto gehört Ben: belegt, nichts ändert sich.
	ac, q := k.linkStart(t, "google", map[string]string{"aktuell": kontoPw, "code": k.code()}, c)
	if e := ergebnis(t, k.rueckruf(t, k.google, "google", q, "b1", map[string]any{"sub": "sub-ben"}, ac, c)); e != KontoErgebnisBelegt {
		t.Fatalf("belegt: %q", e)
	}
	if k.bindungsZahl(t, "anna@example.org") != 0 || k.bindungsZahl(t, "ben@example.org") != 1 {
		t.Fatal("Bindungen nach belegt verändert")
	}
	// Erst ein Konto, dann ein anderes beim selben Anbieter: ersetzt.
	ac, q = k.linkStart(t, "google", map[string]string{"aktuell": kontoPw, "code": k.code()}, c)
	if e := ergebnis(t, k.rueckruf(t, k.google, "google", q, "b2", map[string]any{"sub": "sub-anna-1"}, ac, c)); e != KontoErgebnisVerknuepft {
		t.Fatalf("erstes: %q", e)
	}
	ac, q = k.linkStart(t, "google", map[string]string{"aktuell": kontoPw, "code": k.code()}, c)
	if e := ergebnis(t, k.rueckruf(t, k.google, "google", q, "b3", map[string]any{"sub": "sub-anna-2"}, ac, c)); e != KontoErgebnisErsetzt {
		t.Fatalf("ersetzen: %q", e)
	}
	var sub string
	if err := k.db.QueryRow(`SELECT sub FROM anbieter_bindungen WHERE user_id = 'u-anna@example.org' AND anbieter = 'google'`).Scan(&sub); err != nil || sub != "sub-anna-2" {
		t.Fatalf("Bindung nach Ersetzen: %q %v", sub, err)
	}
	// Das alte Google-Konto kommt nicht mehr herein (Anna steht nicht auf der
	// Vorab-Liste, also ohne Bindung: nicht frei).
	lc, lq := k.starte(t, "google")
	if rec := k.melde(t, k.google, "google", lc, lq, "b4", map[string]any{"sub": "sub-anna-1", "email": "anna@example.org"}); sitzungsCookie(rec) != nil {
		t.Error("altes Anbieter-Konto meldet noch an")
	}
}

// Kommentar 1 der Karte: Es gibt nur ein Anmelde-Cookie. Ein paralleler
// Anmeldevorgang überschreibt den Verknüpfungs-Zustand; der Rückruf bindet dann
// nichts (fail closed).
func TestKonto1083_ParalleleVorgaengeBindenNichtsFalsch(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "ben@example.org", false)
	k.person(t, "anna@example.org", true)
	annaC := k.sitzung(t, "anna@example.org")
	benC := k.sitzung(t, "ben@example.org")
	best := map[string]string{"aktuell": kontoPw}

	// (1) Anna beginnt zu verknüpfen, startet dann in demselben Browser eine
	// Anmeldung (überschreibt das Cookie). Der Rückruf des ersten Vorgangs
	// kommt mit dem neuen Cookie an: abgewiesen, keine Bindung.
	best["code"] = k.code()
	_, q1 := k.linkStart(t, "google", best, annaC)
	loginC, _ := k.starte(t, "google") // neues Cookie, Zweck login
	rec := k.rueckruf(t, k.google, "google", q1, "p1", map[string]any{"sub": "sub-x"}, loginC, annaC)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("überschriebener Vorgang: %d, erwartet 400", rec.Code)
	}
	if k.bindungsZahl(t, "anna@example.org") != 0 {
		t.Fatal("Bindung trotz überschriebenem Vorgang")
	}

	// (2) Der Verknüpfungs-Zustand von Anna kommt in einem Browser an, dessen
	// Sitzung Ben gehört (Anna-Cookie gestohlen/weitergereicht, oder zwei
	// Personen an einem Rechner): nichts wird gebunden, weder an Ben noch an Anna.
	best["code"] = k.code()
	annaAC, q2 := k.linkStart(t, "google", best, annaC)
	rec = k.rueckruf(t, k.google, "google", q2, "p2", map[string]any{"sub": "sub-y"}, annaAC, benC)
	if e := ergebnis(t, rec); e != KontoErgebnisFehler {
		t.Fatalf("fremde Sitzung: %q", e)
	}
	if k.bindungsZahl(t, "ben@example.org") != 0 || k.bindungsZahl(t, "anna@example.org") != 0 {
		t.Fatal("Bindung an die falsche Person")
	}

	// (3) Ohne Sitzung (abgemeldet): nichts.
	best["code"] = k.code()
	annaAC, q3 := k.linkStart(t, "google", best, annaC)
	rec = k.rueckruf(t, k.google, "google", q3, "p3", map[string]any{"sub": "sub-z"}, annaAC)
	if e := ergebnis(t, rec); e != KontoErgebnisAbgelaufen {
		t.Fatalf("ohne Sitzung: %q", e)
	}
	// (4) Sitzung zwischendurch beendet (z. B. „Andere Sitzungen beenden“ von
	// einem anderen Gerät): abgelaufen, nichts gebunden.
	best["code"] = k.code()
	annaAC, q4 := k.linkStart(t, "google", best, annaC)
	if err := k.sitzungen.Beenden(context.Background(), annaC.Value); err != nil {
		t.Fatal(err)
	}
	rec = k.rueckruf(t, k.google, "google", q4, "p4", map[string]any{"sub": "sub-w"}, annaAC, annaC)
	if e := ergebnis(t, rec); e != KontoErgebnisAbgelaufen {
		t.Fatalf("Sitzung beendet: %q", e)
	}
	if k.bindungsZahl(t, "anna@example.org") != 0 {
		t.Fatal("Bindung trotz beendeter Sitzung")
	}
}

// Ein Login-Zustand lässt sich nicht als Verknüpfung ausgeben und umgekehrt:
// Der Zweck steht im signierten Zustand, ein Rückruf ohne Sitzungs-Hash für
// link wird nicht angenommen.
func TestKonto1083_ZweckLinkNurMitSitzungsBindung(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	annaC := k.sitzung(t, "anna@example.org")
	a := k.tabelle.Finde("google")
	roh, err := a.zustandKodieren(anmeldeZustand{State: "s", Nonce: "n", Verifier: "v", Bis: time.Now().Add(time.Minute).Unix(), Kuerzel: "google", Zweck: ZweckLink})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungRueckrufPfad+"/google?code=x&state=s", nil)
	r.AddCookie(&http.Cookie{Name: AnmeldungCookie, Value: roh})
	r.AddCookie(annaC)
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || k.google.tokenAufrufe != 0 {
		t.Fatalf("link ohne Sitzungs-Bindung: %d, Token-Aufrufe %d", rec.Code, k.google.tokenAufrufe)
	}
}

func TestKonto1083_VerknuepfenAbbruchBeimAnbieter(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true)
	c := k.sitzung(t, "anna@example.org")
	ac, q := k.linkStart(t, "google", map[string]string{"aktuell": kontoPw, "code": k.code()}, c)
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungRueckrufPfad+"/google?error=access_denied&state="+url.QueryEscape(q.Get("state")), nil)
	r.AddCookie(ac)
	r.AddCookie(c)
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, r)
	if e := ergebnis(t, rec); e != KontoErgebnisAbgebrochen {
		t.Fatalf("Abbruch: %q", e)
	}
}

// ---------------------------------------------------------------------------
// Lösen (F-2877)

func TestKonto1083_Loesen(t *testing.T) {
	k := neuerKontoTest(t, "")
	k.person(t, "anna@example.org", true) // Passwort + 2FA
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-a', 'google', 'u-anna@example.org', 1)`, k.google.srv.URL)
	c := k.sitzung(t, "anna@example.org")

	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "google", "aktuell": "falsch-falsch-falsch", "code": k.code()}, c); rec.Code != http.StatusForbidden {
		t.Fatalf("falsche Bestätigung: %d", rec.Code)
	}
	if k.bindungsZahl(t, "anna@example.org") != 1 {
		t.Fatal("gelöst trotz falscher Bestätigung")
	}
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "authelia", "aktuell": kontoPw, "code": k.code()}, c); rec.Code != http.StatusNotFound {
		t.Fatalf("nicht verknüpft: %d", rec.Code)
	}
	// Mit Passwort bleibt ein Weg übrig: lösen geht.
	if rec := k.post("/wege/loesen", map[string]string{"anbieter": "google", "aktuell": kontoPw, "code": k.code()}, c); rec.Code != http.StatusOK {
		t.Fatalf("lösen: %d %s", rec.Code, rec.Body.String())
	}
	if k.bindungsZahl(t, "anna@example.org") != 0 {
		t.Fatal("Bindung nicht gelöst")
	}
	// Das gelöste Anbieter-Konto kommt nicht mehr herein.
	lc, lq := k.starte(t, "google")
	if rec := k.melde(t, k.google, "google", lc, lq, "x1", map[string]any{"sub": "sub-a", "email": "anna@example.org"}); sitzungsCookie(rec) != nil {
		t.Error("gelöstes Anbieter-Konto meldet noch an")
	}
}

// Wer kein Passwort hat, ändert Anmeldewege nur mit einem Nachweis aus einer
// frischen Anmeldung (Sicherheitsprüfung #1083 P1, seit #1098): ohne ihn 409
// frische_anmeldung_noetig; der Stand nennt die Anbieter, bei denen die frische
// Anmeldung geht. Den letzten Weg schützt zusätzlich der Speicher.
func TestKonto1083_OhnePasswortKeineWegAenderung(t *testing.T) {
	k := neuerKontoTest(t, "pm@example.org")
	k.person(t, "pm@example.org", false) // kein Passwort
	k.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, 'sub-p', 'google', 'u-pm@example.org', 1)`, k.google.srv.URL)
	c := k.sitzung(t, "pm@example.org")
	for _, weg := range []string{"/wege/loesen", "/wege/verknuepfen"} {
		rec := k.post(weg, map[string]string{"anbieter": "google"}, c)
		if rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != kennungFrischNoetig {
			t.Fatalf("%s: %d %s", weg, rec.Code, rec.Body.String())
		}
	}
	if k.bindungsZahl(t, "pm@example.org") != 1 {
		t.Fatal("Bindung verändert")
	}
	var s kontoStand
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c).Body.Bytes(), &s)
	if !s.WegeAenderbar || !s.FrischNoetig || len(s.FrischAnbieter) != 1 || s.FrischAnbieter[0].Anbieter != "google" || s.Frisch != nil {
		t.Errorf("Stand ohne Passwort: %+v", s)
	}
	for _, w := range s.Wege {
		if w.Loesbar {
			t.Errorf("Stand bietet Lösen des einzigen Wegs an: %+v", w)
		}
	}
}
