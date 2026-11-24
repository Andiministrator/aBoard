package api

// Tests zur Passwort-Anmeldung (ADR-0149 §2, §3; Karte #1074). Testdaten nur
// example.org.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const richtig = "richtiges-passwort-1074"

// ---------------------------------------------------------------------------
// Hashen und Prüfen

func TestPasswortHashen1074_RundUndGrenzen(t *testing.T) {
	ctx := context.Background()
	h, err := PasswortHashen(ctx, richtig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("Format: %q", h)
	}
	if h2, _ := PasswortHashen(ctx, richtig); h2 == h {
		t.Fatal("gleiches Salz bei zwei Hashes")
	}
	if ok, err := PasswortPruefen(ctx, richtig, h); !ok || err != nil {
		t.Fatalf("richtiges Passwort abgewiesen: %v %v", ok, err)
	}
	for _, falsch := range []string{"", "richtiges-passwort-1075", richtig + " ", strings.ToUpper(richtig)} {
		if ok, err := PasswortPruefen(ctx, falsch, h); ok || err != nil {
			t.Errorf("%q angenommen: %v %v", falsch, ok, err)
		}
	}
	// Leerer Hash (kein Konto, kein Passwort-Weg): immer false, kein Fehler.
	if ok, err := PasswortPruefen(ctx, richtig, ""); ok || err != nil {
		t.Errorf("leerer Hash: %v %v", ok, err)
	}
	// Überlange Eingabe wird nie angenommen, auch wenn ihr Anfang passt.
	lang := richtig + strings.Repeat("x", PasswortHoechstLaenge)
	if ok, _ := PasswortPruefen(ctx, lang, h); ok {
		t.Error("überlange Eingabe angenommen")
	}
}

func TestPasswortZulaessig1074(t *testing.T) {
	for pw, want := range map[string]error{
		"elf-zeichen":                     ErrPasswortZuKurz, // 11
		"zwölf-zeiche":                    nil,               // 12 Zeichen, 13 Bytes
		"äöüäöüäöüäö":                     ErrPasswortZuKurz, // 11 Zeichen, 22 Bytes
		strings.Repeat("a", 1025):         ErrPasswortZuLang,
		strings.Repeat("a", 1024):         nil,
		"gültig\xffaber-kaputt-utf8-lang": ErrPasswortZuKurz,
	} {
		if got := PasswortZulaessig(pw); !errors.Is(got, want) && got != want {
			t.Errorf("%q: %v, erwartet %v", pw, got, want)
		}
	}
	if _, err := PasswortHashen(context.Background(), "zu-kurz"); !errors.Is(err, ErrPasswortZuKurz) {
		t.Errorf("Hashen zu kurz: %v", err)
	}
}

func TestPhcLesen1074_Grenzen(t *testing.T) {
	gut, err := PasswortHashen(context.Background(), richtig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phcLesen(gut); err != nil {
		t.Fatalf("eigener Hash unlesbar: %v", err)
	}
	teile := strings.Split(gut, "$")
	salz, hash := teile[4], teile[5]
	for name, h := range map[string]string{
		"argon2i":          "$argon2i$v=19$m=65536,t=3,p=2$" + salz + "$" + hash,
		"Version 16":       "$argon2id$v=16$m=65536,t=3,p=2$" + salz + "$" + hash,
		"4 GiB":            "$argon2id$v=19$m=4194304,t=3,p=2$" + salz + "$" + hash,
		"t=1000":           "$argon2id$v=19$m=65536,t=1000,p=2$" + salz + "$" + hash,
		"p=0":              "$argon2id$v=19$m=65536,t=3,p=0$" + salz + "$" + hash,
		"Zusatz":           "$argon2id$v=19$m=65536,t=3,p=2,x=1$" + salz + "$" + hash,
		"führende Null":    "$argon2id$v=19$m=065536,t=3,p=2$" + salz + "$" + hash,
		"Salz kaputt":      "$argon2id$v=19$m=65536,t=3,p=2$!!!$" + hash,
		"Teil zu viel":     gut + "$x",
		"bcrypt":           "$2b$12$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz01234",
		"Klartext":         richtig,
		"Hash zu kurz":     "$argon2id$v=19$m=65536,t=3,p=2$" + salz + "$AAAA",
		"Salz mit Padding": "$argon2id$v=19$m=65536,t=3,p=2$" + salz + "==$" + hash,
	} {
		if _, err := phcLesen(h); err == nil {
			t.Errorf("%s angenommen: %q", name, h)
		}
		if ok, err := PasswortPruefen(context.Background(), richtig, h); ok || err == nil {
			t.Errorf("%s: Prüfen %v %v, erwartet Fehler", name, ok, err)
		}
	}
}

// Kritik H2: Sind alle Plätze belegt, wartet eine Prüfung höchstens bis zur
// Frist und meldet dann Überlastung — sie rechnet nicht zusätzlich.
func TestPasswortPruefen1074_Ueberlastet(t *testing.T) {
	for i := 0; i < cap(argonSemaphor); i++ {
		argonSemaphor <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(argonSemaphor); i++ {
			<-argonSemaphor
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := PasswortPruefen(ctx, richtig, ""); !errors.Is(err, ErrPasswortUeberlastet) {
		t.Fatalf("erwartet Überlastung, bekam %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Wartezeit %v trotz kurzer Frist", d)
	}
}

func TestWartezeitNach1074(t *testing.T) {
	for n, want := range map[int]time.Duration{0: 0, 1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 7: 30 * time.Second, 100: 30 * time.Second} {
		if got := wartezeitNach(n); got != want {
			t.Errorf("n=%d: %v, erwartet %v", n, got, want)
		}
	}
}

func TestPasswortAngeboten1074(t *testing.T) {
	for wert, want := range map[string]bool{"": true, "  ": true, "password": true, "google, password": true,
		"google": false, "standard,authelia": false, "passwort": false} {
		if got := PasswortAngeboten(wert); got != want {
			t.Errorf("%q: %v", wert, got)
		}
	}
	// password ist ein gültiger Weg in ABOARD_AUTH_METHODS, auch ganz allein.
	alle := []AnbieterKonfig{{Kuerzel: "google"}}
	if got, f := FiltereAnbieter(alle, "password"); len(got) != 0 || len(f) != 0 {
		t.Errorf("nur password: %v %v", got, f)
	}
	if got, f := FiltereAnbieter(alle, "password,google"); len(got) != 1 || len(f) != 0 {
		t.Errorf("password,google: %v %v", got, f)
	}
	if got, f := FiltereAnbieter(nil, "password"); len(got) != 0 || len(f) != 0 {
		t.Errorf("ohne Anbieter, nur password: %v %v", got, f)
	}
}

// ---------------------------------------------------------------------------
// Handler

type fakePwKonten struct {
	konten map[string]storage.KontoAnmeldung
	fehler error
}

func (f *fakePwKonten) FuerAnmeldung(_ context.Context, email string) (storage.KontoAnmeldung, error) {
	if f.fehler != nil {
		return storage.KontoAnmeldung{}, f.fehler
	}
	k, ok := f.konten[storage.NormalizeEmail(email)]
	if !ok {
		return storage.KontoAnmeldung{}, storage.ErrKontoUnbekannt
	}
	return k, nil
}

type testUhr struct{ t time.Time }

func (u *testUhr) jetzt() time.Time { return u.t }

type passwortTest struct {
	m       *Anmeldungen
	p       *PasswortAnmeldung
	konten  *fakePwKonten
	uhr     *testUhr
	tor     map[string]bool
	torErr  error
	nach    []string
	gerufen int // so oft wurde gerechnet
}

func neuerPasswortTest(t *testing.T) *passwortTest {
	t.Helper()
	pt := &passwortTest{
		konten: &fakePwKonten{konten: map[string]storage.KontoAnmeldung{
			"anna@example.org":  {ID: "k-anna", Email: "anna@example.org", Zustand: storage.KontoAktiv, PasswortHash: "h-anna", HatTOTP: true},
			"ben@example.org":   {ID: "k-ben", Email: "ben@example.org", Zustand: storage.KontoWartet, PasswortHash: "h-ben"},
			"carla@example.org": {ID: "k-carla", Email: "carla@example.org", Zustand: storage.KontoGesperrt, PasswortHash: "h-carla"},
			"dora@example.org":  {ID: "k-dora", Email: "dora@example.org", Zustand: storage.KontoAktiv},
			"emil@example.org":  {ID: "k-emil", Email: "emil@example.org", Zustand: storage.KontoEingeladen, PasswortHash: "h-emil"},
		}},
		uhr: &testUhr{t: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)},
		tor: map[string]bool{"anna@example.org": true, "ben@example.org": true, "carla@example.org": true},
	}
	p, err := NeuePasswortAnmeldung(pt.konten, func(_ context.Context, e string) (bool, error) {
		return pt.tor[e], pt.torErr
	}, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p.gegenstelle.jetzt = pt.uhr.jetzt
	p.adressen.jetzt = pt.uhr.jetzt
	// Schnelle Prüfung: Hash "h-<name>" passt zu richtig.
	p.pruefen = func(_ context.Context, pw, hash string) (bool, error) {
		pt.gerufen++
		return hash != "" && pw == richtig, nil
	}
	p.NachPasswort = func(w http.ResponseWriter, r *http.Request, k storage.KontoAnmeldung) {
		pt.nach = append(pt.nach, k.Email)
		w.WriteHeader(http.StatusAccepted)
	}
	pt.p = p
	pt.m = &Anmeldungen{Passwort: p}
	return pt
}

// post schickt das Formular; remote ist die TCP-Gegenstelle.
func (pt *passwortTest) post(email, pw, remote string, kopf ...string) *httptest.ResponseRecorder {
	form := url.Values{"email": {email}, "passwort": {pw}}
	r := httptest.NewRequest(http.MethodPost, "https://board.example"+AnmeldungPasswortPfad, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = remote + ":4711"
	for i := 0; i+1 < len(kopf); i += 2 {
		r.Header.Set(kopf[i], kopf[i+1])
	}
	rec := httptest.NewRecorder()
	pt.m.PasswortPost(rec, r)
	return rec
}

func TestPasswortPost1074_Erfolg(t *testing.T) {
	pt := neuerPasswortTest(t)
	rec := pt.post(" Anna@Example.org ", richtig, "192.0.2.1")
	if rec.Code != http.StatusAccepted || len(pt.nach) != 1 || pt.nach[0] != "anna@example.org" {
		t.Fatalf("Erfolg: %d %v", rec.Code, pt.nach)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("Cache-Control fehlt")
	}
}

// Unbekannte Adresse, Konto ohne Passwort und falsches Passwort: gleiche
// Antwort, und in jedem Fall wird gerechnet (gleiche Rechenzeit).
func TestPasswortPost1074_KeinHinweisAufExistenz(t *testing.T) {
	var antworten []string
	var codes []int
	for i, email := range []string{"niemand@example.org", "dora@example.org", "anna@example.org", ""} {
		pt := neuerPasswortTest(t)
		pw := "falsches-passwort"
		if email == "dora@example.org" {
			pw = richtig // Konto ohne Passwort-Hash
		}
		rec := pt.post(email, pw, "192.0.2."+string(rune('1'+i)))
		if pt.gerufen != 1 {
			t.Errorf("%q: %d Prüfungen, erwartet 1", email, pt.gerufen)
		}
		if len(pt.nach) != 0 {
			t.Errorf("%q: durchgelassen", email)
		}
		body := strings.ReplaceAll(rec.Body.String(), `value="`+email+`"`, `value=""`)
		antworten = append(antworten, body)
		codes = append(codes, rec.Code)
	}
	for i := range antworten {
		if codes[i] != http.StatusUnauthorized || antworten[i] != antworten[0] {
			t.Errorf("Antwort %d weicht ab: %d\n%s\n---\n%s", i, codes[i], antworten[i], antworten[0])
		}
	}
	if !strings.Contains(antworten[0], "E-Mail-Adresse oder Passwort stimmt nicht.") {
		t.Errorf("Meldung fehlt: %s", antworten[0])
	}
}

func TestPasswortPost1074_Zustaende(t *testing.T) {
	for _, f := range []struct {
		email, titel string
		code         int
	}{
		{"ben@example.org", "Wartet auf Bestätigung", http.StatusForbidden},
		{"carla@example.org", "Kein Zugang", http.StatusForbidden},
		{"emil@example.org", "Kein Zugang", http.StatusForbidden},
	} {
		pt := neuerPasswortTest(t)
		rec := pt.post(f.email, richtig, "192.0.2.1")
		if rec.Code != f.code || !strings.Contains(rec.Body.String(), f.titel) || len(pt.nach) != 0 {
			t.Errorf("%s: %d %v\n%s", f.email, rec.Code, pt.nach, rec.Body.String())
		}
	}
	// Tor sagt nein (z. B. gestrichen) → kein Zugang; Torfehler → 500.
	pt := neuerPasswortTest(t)
	pt.tor["anna@example.org"] = false
	if rec := pt.post("anna@example.org", richtig, "192.0.2.1"); rec.Code != http.StatusForbidden || len(pt.nach) != 0 {
		t.Errorf("Tor nein: %d", rec.Code)
	}
	pt = neuerPasswortTest(t)
	pt.torErr = errors.New("db weg")
	if rec := pt.post("anna@example.org", richtig, "192.0.2.1"); rec.Code != http.StatusInternalServerError || len(pt.nach) != 0 {
		t.Errorf("Torfehler: %d", rec.Code)
	}
	// Lesefehler der Konten → 500, nicht „stimmt nicht“.
	pt = neuerPasswortTest(t)
	pt.konten.fehler = errors.New("db weg")
	if rec := pt.post("anna@example.org", richtig, "192.0.2.1"); rec.Code != http.StatusInternalServerError {
		t.Errorf("Kontenfehler: %d", rec.Code)
	}
}

// Ohne Code-Schritt (#1075) gibt es nie eine Sitzung: 2FA ist Pflicht.
func TestPasswortPost1074_OhneCodeSchrittKeineSitzung(t *testing.T) {
	pt := neuerPasswortTest(t)
	pt.p.NachPasswort = nil
	rec := pt.post("anna@example.org", richtig, "192.0.2.1")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Zwei-Faktor fehlt") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if c := rec.Header().Values("Set-Cookie"); len(c) != 0 {
		t.Fatalf("Cookie gesetzt: %v", c)
	}
}

func TestPasswortPost1074_BremseGegenstelle(t *testing.T) {
	pt := neuerPasswortTest(t)
	// Fünf Fehlversuche mit wechselnden Adressen von einem Anschluss.
	for i := 0; i < PasswortMaxFehlversuche; i++ {
		email := "x" + string(rune('a'+i)) + "@example.org"
		if rec := pt.post(email, "falsch-falsch-falsch", "192.0.2.7"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("Versuch %d: %d", i, rec.Code)
		}
	}
	// Danach ist der Anschluss gesperrt — auch mit richtigem Passwort, und
	// ohne zu rechnen.
	vorher := pt.gerufen
	rec := pt.post("anna@example.org", richtig, "192.0.2.7")
	if rec.Code != http.StatusTooManyRequests || len(pt.nach) != 0 || pt.gerufen != vorher {
		t.Fatalf("gesperrt erwartet: %d %v", rec.Code, pt.nach)
	}
	if rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "Minuten") {
		t.Errorf("Retry-After/Meldung fehlt: %v %s", rec.Header(), rec.Body.String())
	}
	// X-Forwarded-For und X-Real-IP von einer fremden Gegenstelle ändern nichts.
	if rec := pt.post("anna@example.org", richtig, "192.0.2.7", "X-Forwarded-For", "198.51.100.9", "X-Real-IP", "198.51.100.9"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("Kopfzeile umgeht die Sperre: %d", rec.Code)
	}
	// Ein anderer Anschluss ist nicht betroffen.
	if rec := pt.post("anna@example.org", richtig, "192.0.2.8"); rec.Code != http.StatusAccepted {
		t.Errorf("anderer Anschluss: %d", rec.Code)
	}
	// Nach dem Fenster ist der Anschluss wieder frei.
	pt.uhr.t = pt.uhr.t.Add(PasswortFenster + time.Second)
	if rec := pt.post("anna@example.org", richtig, "192.0.2.7"); rec.Code != http.StatusAccepted {
		t.Errorf("nach dem Fenster: %d", rec.Code)
	}
}

// Hinter dem Proxy zählt X-Real-IP: zwei Clients hinter demselben Proxy
// sperren sich nicht gegenseitig.
func TestPasswortPost1074_BremseHinterProxy(t *testing.T) {
	pt := neuerPasswortTest(t)
	for i := 0; i < PasswortMaxFehlversuche; i++ {
		pt.post("anna@example.org", "falsch-falsch-falsch", "10.0.0.1", "X-Real-IP", "198.51.100.1")
		pt.uhr.t = pt.uhr.t.Add(PasswortWarteMax)
	}
	if rec := pt.post("anna@example.org", richtig, "10.0.0.1", "X-Real-IP", "198.51.100.1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("Client 1 nicht gesperrt: %d", rec.Code)
	}
	if rec := pt.post("anna@example.org", richtig, "10.0.0.1", "X-Real-IP", "198.51.100.2"); rec.Code != http.StatusAccepted {
		t.Fatalf("Client 2 mitgesperrt: %d", rec.Code)
	}
}

// Je Adresse keine harte Sperre, sondern eine ansteigende Wartezeit.
func TestPasswortPost1074_WartezeitJeAdresse(t *testing.T) {
	pt := neuerPasswortTest(t)
	anschluss := 0
	versuch := func(pw string) *httptest.ResponseRecorder {
		anschluss++ // jeder Versuch von einem neuen Anschluss: nur die Adresse zählt
		return pt.post("anna@example.org", pw, "198.51.100."+string(rune('0'+anschluss%10))+string(rune('0'+anschluss/10)))
	}
	if rec := versuch("falsch-falsch-falsch"); rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
	// Sofort danach: warten, ohne zu rechnen — auch mit richtigem Passwort.
	vorher := pt.gerufen
	rec := versuch(richtig)
	if rec.Code != http.StatusTooManyRequests || pt.gerufen != vorher || !strings.Contains(rec.Body.String(), "Sekunden warten") {
		t.Fatalf("Wartezeit nicht wirksam: %d %s", rec.Code, rec.Body.String())
	}
	// Die Wartezeit steigt: nach vier Fehlversuchen 8 s.
	for i := 0; i < 3; i++ {
		pt.uhr.t = pt.uhr.t.Add(PasswortWarteMax)
		versuch("falsch-falsch-falsch")
	}
	pt.uhr.t = pt.uhr.t.Add(7 * time.Second)
	if rec := versuch(richtig); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("nach 7 s schon frei: %d", rec.Code)
	}
	pt.uhr.t = pt.uhr.t.Add(2 * time.Second)
	if rec := versuch(richtig); rec.Code != http.StatusAccepted {
		t.Fatalf("nach 9 s nicht frei: %d", rec.Code)
	}
	// Ein richtiges Passwort zählt nicht als Fehlversuch. Zurückgesetzt wird
	// die Wartezeit erst nach dem Code-Schritt (#1075).
	if rec := versuch(richtig); rec.Code != http.StatusAccepted {
		t.Fatalf("nach Erfolg: %d", rec.Code)
	}
	// Eine fremde Adresse ist nicht betroffen, auch eine unbekannte nicht.
	versuch("falsch-falsch-falsch")
	if rec := pt.post("ben@example.org", "falsch-falsch-falsch", "203.0.113.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("fremde Adresse wartet mit: %d", rec.Code)
	}
}

func TestPasswortPost1074_UeberlastetUndHashfehler(t *testing.T) {
	pt := neuerPasswortTest(t)
	pt.p.pruefen = func(context.Context, string, string) (bool, error) { return false, ErrPasswortUeberlastet }
	rec := pt.post("anna@example.org", richtig, "192.0.2.1")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("Überlastung: %d", rec.Code)
	}
	// Überlastung zählt nicht als Fehlversuch.
	if w := pt.p.adressen.warten("anna@example.org", "192.0.2.1"); w != 0 {
		t.Errorf("Überlastung als Fehlversuch gezählt: %v", w)
	}
	pt.p.pruefen = func(context.Context, string, string) (bool, error) { return false, errHashFormat }
	if rec := pt.post("anna@example.org", richtig, "192.0.2.1"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("Hashfehler: %d", rec.Code)
	}
}

func TestPasswortPost1074_FormularGrenzen(t *testing.T) {
	pt := neuerPasswortTest(t)
	r := httptest.NewRequest(http.MethodPost, "https://board.example"+AnmeldungPasswortPfad,
		strings.NewReader("email=a&passwort="+strings.Repeat("x", passwortFormularMax+1)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	pt.m.PasswortPost(rec, r)
	if rec.Code != http.StatusBadRequest || pt.gerufen != 0 {
		t.Fatalf("zu großer Körper: %d, %d Prüfungen", rec.Code, pt.gerufen)
	}
	// Ohne Passwort-Weg: 404.
	rec = httptest.NewRecorder()
	(&Anmeldungen{}).PasswortPost(rec, httptest.NewRequest(http.MethodPost, "https://board.example"+AnmeldungPasswortPfad, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ohne Passwort-Weg: %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Seite

func TestAnmeldeSeite1074_Formular(t *testing.T) {
	pt := neuerPasswortTest(t)
	pt.m.Anbieter = []*OIDCAnmeldung{{Kuerzel: "google", Label: "Google", Issuer: GoogleIssuer}}

	// Genau ein Anbieter, aber auch das Passwort: keine Direkt-Weiterleitung.
	rec := httptest.NewRecorder()
	pt.m.Seite(rec, httptest.NewRequest(http.MethodGet, "https://board.example/anmeldung", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `<form method="post" action="/anmeldung/passwort">`) ||
		!strings.Contains(body, `autocomplete="current-password"`) || !strings.Contains(body, "Mit Google anmelden") {
		t.Fatalf("%d\n%s", rec.Code, body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP: %q", csp)
	}
	// Englisch.
	r := httptest.NewRequest(http.MethodGet, "https://board.example/anmeldung", nil)
	r.Header.Set("Accept-Language", "en")
	rec = httptest.NewRecorder()
	pt.m.Seite(rec, r)
	if !strings.Contains(rec.Body.String(), "Email address") || !strings.Contains(rec.Body.String(), "Password") {
		t.Errorf("englisch: %s", rec.Body.String())
	}
	// Start ohne Kürzel führt zur Seite, nicht zu Google.
	rec = httptest.NewRecorder()
	pt.m.Start(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungStartPfad, nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != AnmeldungSeitePfad {
		t.Errorf("Start: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Ohne Passwort-Weg bleibt die Direkt-Weiterleitung (pm mit ABOARD_AUTH_METHODS=standard).
	pt.m.Passwort = nil
	rec = httptest.NewRecorder()
	pt.m.Seite(rec, httptest.NewRequest(http.MethodGet, "https://board.example/anmeldung", nil))
	if rec.Code != http.StatusFound {
		t.Errorf("nur Anbieter: %d", rec.Code)
	}
}

// Die zurückgegebene Adresse steht escaped im Formular (sie kommt vom Client).
func TestAnmeldeSeite1074_AdresseEscaped(t *testing.T) {
	pt := neuerPasswortTest(t)
	rec := pt.post(`"><script>alert(1)</script>@example.org`, "falsch-falsch-falsch", "192.0.2.1")
	if strings.Contains(rec.Body.String(), "<script>") || !strings.Contains(rec.Body.String(), "&#34;&gt;&lt;script&gt;") {
		t.Fatalf("nicht escaped:\n%s", rec.Body.String())
	}
}

// Nur Passwort, kein Anbieter: Seite und Abmelde-Seite funktionieren.
func TestAnmeldeSeite1074_OhneAnbieter(t *testing.T) {
	pt := neuerPasswortTest(t)
	rec := httptest.NewRecorder()
	pt.m.Seite(rec, httptest.NewRequest(http.MethodGet, "https://board.example/anmeldung", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") || strings.Contains(rec.Body.String(), "kein Anmeldeweg") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	pt.m.Abgemeldet(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungAbgemeldetPfad, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "abgemeldet") {
		t.Fatalf("Abgemeldet: %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Fixierungsschutz

type fakeAnmeldeSitzungen struct {
	beendet  []string
	angelegt []string
}

func (f *fakeAnmeldeSitzungen) Anlegen(_ context.Context, email, _ string) (string, time.Time, error) {
	f.angelegt = append(f.angelegt, email)
	return testSitzungsID, time.Now().Add(time.Hour), nil
}

func (f *fakeAnmeldeSitzungen) Beenden(_ context.Context, id string) error {
	f.beendet = append(f.beendet, id)
	return nil
}

func TestBeginneSitzung1074_BeendetMitgebrachte(t *testing.T) {
	s := &fakeAnmeldeSitzungen{}
	r := httptest.NewRequest(http.MethodPost, "https://board.example/anmeldung/code", nil)
	r.AddCookie(&http.Cookie{Name: SitzungsCookie, Value: "alte-sitzung"})
	rec := httptest.NewRecorder()
	if !beginneSitzung(rec, r, s, "anna@example.org", "") {
		t.Fatal("false")
	}
	if len(s.beendet) != 1 || s.beendet[0] != "alte-sitzung" || len(s.angelegt) != 1 {
		t.Fatalf("beendet %v, angelegt %v", s.beendet, s.angelegt)
	}
	if c := rec.Header().Get("Set-Cookie"); !strings.Contains(c, SitzungsCookie+"="+testSitzungsID) {
		t.Fatalf("Cookie: %q", c)
	}
}
