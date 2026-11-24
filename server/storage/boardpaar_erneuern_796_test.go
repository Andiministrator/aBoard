package storage

// Karte #796 (T-R12, Vertrag zum Board-Paar, 10.10.2026): Board-Paar-Zugänge
// erneuern sich selbst. Abnahme laut Karte: Erneuern mit gültigem Token →
// neues Token, das alte ist nach der Übergangszeit tot; Wiederholung in der
// Übergangszeit → dasselbe Token; widerrufen → 401; zweimal erneuern → nur
// das jüngste gilt; Protokoll mit Zeit und Adresse.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

func erneuernTestDB(t *testing.T) (*BoardpaarRepo, func(time.Duration)) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	alt := timeNow
	basis := time.Now()
	versatz := time.Duration(0)
	var mu sync.Mutex
	timeNow = func() time.Time { mu.Lock(); defer mu.Unlock(); return basis.Add(versatz) }
	t.Cleanup(func() { timeNow = alt })
	vor := func(d time.Duration) { mu.Lock(); versatz += d; mu.Unlock() }
	return NewBoardpaarRepo(db), vor
}

func pruefeGilt(t *testing.T, r *BoardpaarRepo, typ, token, was string, soll bool) {
	t.Helper()
	_, err := r.TokenPruefe(context.Background(), typ, token)
	switch {
	case soll && err != nil:
		t.Errorf("%s: gilt nicht (%v), sollte gelten", was, err)
	case !soll && !errors.Is(err, ErrBoardpaarTokenUnbekannt):
		t.Errorf("%s: err = %v, erwartet ErrBoardpaarTokenUnbekannt", was, err)
	}
}

func TestBoardpaar796_ErneuernUndUebergangszeit(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	alt, _, err := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, alt, "192.0.2.7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(e.Token, SchattenAusgangPraefix) || e.Token == alt || e.Wiederholt {
		t.Fatalf("Erneuerung: %+v", e)
	}
	if e.Zugang.Name != "push" || e.Zugang.ErneuertVon != "192.0.2.7" {
		t.Errorf("Nachfolger: %+v", e.Zugang)
	}
	pruefeGilt(t, r, BoardpaarTypAusgang, e.Token, "neues Token", true)
	pruefeGilt(t, r, BoardpaarTypAusgang, alt, "altes Token in der Übergangszeit", true)

	// Wiederholung in der Übergangszeit: dasselbe neue Token, kein drittes.
	vor(9 * time.Minute)
	w, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, alt, "192.0.2.7")
	if err != nil {
		t.Fatal(err)
	}
	if w.Token != e.Token || !w.Wiederholt || w.AltGiltBis != e.AltGiltBis {
		t.Errorf("Wiederholung: %+v, erwartet dasselbe Token wie %+v", w, e)
	}
	liste, _ := r.TokenListe(ctx)
	if len(liste) != 2 {
		t.Errorf("nach Wiederholung %d Zugänge, erwartet 2", len(liste))
	}

	// Nach der Übergangszeit: das alte ist tot, auch fürs Erneuern.
	vor(2 * time.Minute)
	pruefeGilt(t, r, BoardpaarTypAusgang, alt, "altes Token nach der Übergangszeit", false)
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, alt, "x"); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("Erneuern mit abgelaufenem Token: %v", err)
	}
	pruefeGilt(t, r, BoardpaarTypAusgang, e.Token, "neues Token nach der Übergangszeit", true)

	// Liste: Vorgänger mit gilt_bis, Nachfolger mit Herkunft und Adresse.
	liste, _ = r.TokenListe(ctx)
	var vorg, nachf *BoardpaarToken
	for i := range liste {
		if liste[i].ErneuertAus != "" {
			nachf = &liste[i]
		} else {
			vorg = &liste[i]
		}
	}
	if vorg == nil || nachf == nil || vorg.GiltBis == "" || nachf.ErneuertAus != vorg.ID || nachf.ErneuertVon != "192.0.2.7" ||
		nachf.GiltBis != "" || vorg.RevokedAt != "" {
		t.Errorf("Liste: Vorgänger %+v, Nachfolger %+v", vorg, nachf)
	}
}

func TestBoardpaar796_Abweisungen(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	abs, z, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	abe, _, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "eingang", "admin")
	for name, f := range map[string]struct{ typ, token string }{
		"abe_ als abs":    {BoardpaarTypAusgang, abe},
		"abs_ als abe":    {BoardpaarTypEingang, abs},
		"erfunden":        {BoardpaarTypAusgang, "abs_erfunden"},
		"leer":            {BoardpaarTypAusgang, ""},
		"unbekannter Typ": {"abk", abs},
	} {
		if _, err := r.TokenErneuern(ctx, f.typ, f.token, ""); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := r.TokenWiderrufen(ctx, z.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, abs, ""); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("widerrufenes Token: %v", err)
	}
}

// Zweimal erneuern → nur das jüngste gilt (nach der Übergangszeit des
// mittleren); das erste endet sofort, nicht erst nach seinen zehn Minuten.
func TestBoardpaar796_ZweimalErneuern(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "eingang", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypEingang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	vor(time.Minute)
	c, err := r.TokenErneuern(ctx, BoardpaarTypEingang, b.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	pruefeGilt(t, r, BoardpaarTypEingang, a, "erstes Token nach zweiter Erneuerung", false)
	if _, err := r.TokenErneuern(ctx, BoardpaarTypEingang, a, ""); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("Wiederholung mit dem ersten Token: %v", err)
	}
	pruefeGilt(t, r, BoardpaarTypEingang, b.Token, "mittleres Token in seiner Übergangszeit", true)
	vor(11 * time.Minute)
	pruefeGilt(t, r, BoardpaarTypEingang, b.Token, "mittleres Token danach", false)
	pruefeGilt(t, r, BoardpaarTypEingang, c.Token, "jüngstes Token", true)
	// Der Name gehört jetzt dem jüngsten.
	akt, err := r.TokenAktivNachName(ctx, BoardpaarTypEingang, "eingang")
	if err != nil || akt.ID != c.Zugang.ID {
		t.Errorf("TokenAktivNachName: %+v, %v; erwartet %s", akt, err, c.Zugang.ID)
	}
	if _, _, err := r.TokenAnlegen(ctx, BoardpaarTypEingang, "eingang", "admin"); !errors.Is(err, ErrBoardpaarTokenVergeben) {
		t.Errorf("Anlegen mit vergebenem Namen: %v", err)
	}
}

// Notaus: Wer das aktuelle Token widerruft, nimmt den Vorgänger in seiner
// Übergangszeit mit — auch die Wiederholung liefert dann nichts mehr.
func TestBoardpaar796_WiderrufNimmtVorgaengerMit(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.TokenWiderrufen(ctx, b.Zugang.ID); err != nil {
		t.Fatal(err)
	}
	pruefeGilt(t, r, BoardpaarTypAusgang, a, "Vorgänger nach Widerruf des Nachfolgers", false)
	pruefeGilt(t, r, BoardpaarTypAusgang, b.Token, "widerrufener Nachfolger", false)
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, ""); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("Wiederholung nach Widerruf: %v", err)
	}
	// Und umgekehrt: Der Vorgänger in der Übergangszeit lässt sich einzeln
	// widerrufen, der Nachfolger bleibt.
	c, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push2", "admin")
	d, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, c, "")
	if err != nil {
		t.Fatal(err)
	}
	vorg, _ := r.TokenAktivNachName(ctx, BoardpaarTypAusgang, "push2")
	if vorg.ID != d.Zugang.ID {
		t.Fatalf("aktuelles Token von push2: %s, erwartet %s", vorg.ID, d.Zugang.ID)
	}
	var cID string
	liste, _ := r.TokenListe(ctx)
	for _, z := range liste {
		if z.Name == "push2" && z.ID != d.Zugang.ID {
			cID = z.ID
		}
	}
	if err := r.TokenWiderrufen(ctx, cID); err != nil {
		t.Fatalf("Vorgänger in der Übergangszeit widerrufen: %v", err)
	}
	pruefeGilt(t, r, BoardpaarTypAusgang, c, "einzeln widerrufener Vorgänger", false)
	pruefeGilt(t, r, BoardpaarTypAusgang, d.Token, "Nachfolger nach Widerruf des Vorgängers", true)
}

// Der Klartext des Nachfolgers steht nirgends lesbar in der Datenbank, und
// nach der Übergangszeit ist auch seine verschlüsselte Form weg.
func TestBoardpaar796_KeinKlartextInDerDatenbank(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	geheim := strings.TrimPrefix(b.Token, SchattenAusgangPraefix)
	dump := func() (string, int) {
		rows, err := r.db.Query(`SELECT id, typ, name, token_hash, created_at, created_by, coalesce(gilt_bis,''),
			coalesce(nachfolger_id,''), coalesce(nachfolger_enc,''), coalesce(erneuert_aus,''), coalesce(erneuert_von,'') FROM boardpaar_tokens`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var sb strings.Builder
		enc := 0
		for rows.Next() {
			v := make([]string, 11)
			p := make([]any, 11)
			for i := range v {
				p[i] = &v[i]
			}
			if err := rows.Scan(p...); err != nil {
				t.Fatal(err)
			}
			if v[8] != "" {
				enc++
			}
			sb.WriteString(strings.Join(v, "|"))
		}
		return sb.String(), enc
	}
	inhalt, enc := dump()
	if strings.Contains(inhalt, geheim) {
		t.Error("Klartext des neuen Tokens steht in der Datenbank")
	}
	if enc != 1 {
		t.Errorf("%d verschlüsselte Nachfolger in der Übergangszeit, erwartet 1", enc)
	}
	vor(11 * time.Minute)
	_, _ = r.TokenPruefe(ctx, BoardpaarTypAusgang, b.Token) // räumt auf
	if _, enc := dump(); enc != 0 {
		t.Errorf("%d verschlüsselte Nachfolger nach der Übergangszeit, erwartet 0", enc)
	}
}

// Gleichzeitig erneuern mit demselben Token (Skript doppelt gestartet):
// alle bekommen dasselbe neue Token, es entsteht genau ein Nachfolger.
func TestBoardpaar796_Gleichzeitig(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	const n = 8
	var wg sync.WaitGroup
	ergebnisse := make([]string, n)
	fehler := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
			fehler[i] = err
			if e != nil {
				ergebnisse[i] = e.Token
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if fehler[i] != nil {
			t.Errorf("Lauf %d: %v", i, fehler[i])
		} else if ergebnisse[i] != ergebnisse[0] {
			t.Errorf("Lauf %d bekam ein anderes Token", i)
		}
	}
	liste, _ := r.TokenListe(ctx)
	if len(liste) != 2 {
		t.Errorf("%d Zugänge nach gleichzeitiger Erneuerung, erwartet 2", len(liste))
	}
}

// Ein umkopierter verschlüsselter Nachfolger entschlüsselt nicht — die IDs
// beider Tokens sind gebunden.
func TestBoardpaar796_VerschluesselungGebunden(t *testing.T) {
	enc, err := nachfolgerVerschluesseln("abs_alt", "id-a", "id-b", "abs_neu")
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := nachfolgerEntschluesseln("abs_alt", "id-a", "id-b", enc); err != nil || pt != "abs_neu" {
		t.Fatalf("Rundreise: %q, %v", pt, err)
	}
	for name, f := range map[string][3]string{
		"anderes altes Token": {"abs_anders", "id-a", "id-b"},
		"andere alte ID":      {"abs_alt", "id-x", "id-b"},
		"andere neue ID":      {"abs_alt", "id-a", "id-x"},
	} {
		if _, err := nachfolgerEntschluesseln(f[0], f[1], f[2], enc); err == nil {
			t.Errorf("%s: entschlüsselt", name)
		}
	}
}

// Das Rennen gezielt: Zwischen Lesen und Schreiben erneuert ein anderer mit
// demselben Token. Die verlierende Runde darf nichts schreiben und muss
// denselben Nachfolger liefern wie die gewinnende.
func TestBoardpaar796_RennenZwischenLesenUndSchreiben(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	var gewinner *BoardpaarErneuerung
	// Ein Merker statt sync.Once: Die eingeschobene Erneuerung ruft den
	// Haken selbst wieder auf, und Once blockierte dann auf sich selbst.
	eingeschoben := false
	alt := erneuernNachLesen
	erneuernNachLesen = func() {
		if eingeschoben {
			return
		}
		eingeschoben = true
		var err error
		if gewinner, err = r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "gewinner"); err != nil {
			t.Errorf("eingeschobene Erneuerung: %v", err)
		}
	}
	t.Cleanup(func() { erneuernNachLesen = alt })
	e, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "verlierer")
	if err != nil {
		t.Fatal(err)
	}
	if gewinner == nil || e.Token != gewinner.Token || !e.Wiederholt {
		t.Errorf("verlierende Runde: %+v, Gewinner %+v", e, gewinner)
	}
	liste, _ := r.TokenListe(ctx)
	if len(liste) != 2 {
		t.Errorf("%d Zugänge, erwartet 2", len(liste))
	}
}

// Ist der Nachfolger nicht mehr das aktuelle Token (hier: von Hand in der
// Datenbank widerrufen, ohne die Kaskade von TokenWiderrufen), liefert die
// Wiederholung ihn nicht aus — der Vorgänger ist dann kein Ausweis mehr.
// Über die Wege des Servers ist der Zustand nicht erreichbar (Widerruf und
// Kettenkappung beenden den Vorgänger mit); die Prüfung ist die zweite Linie.
func TestBoardpaar796_WiederholungNurZumAktuellenNachfolger(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	for _, spalte := range []string{"revoked_at", "gilt_bis"} {
		a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push-"+spalte, "admin")
		b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.db.Exec(`UPDATE boardpaar_tokens SET `+spalte+` = ? WHERE id = ?`, nowISO(), b.Zugang.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, ""); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
			t.Errorf("Nachfolger mit %s: Wiederholung %v, erwartet ErrBoardpaarTokenUnbekannt", spalte, err)
		}
	}
}

// Kritiker K-1 (Wissen §38): Ohne gestellte Uhr wird nicht erneuert, und
// kein Vorgänger gilt in einer Übergangszeit, die gegen diese Uhr rechnet.
// Ein Token ohne Erneuerung gilt weiter — es hängt an keiner Frist.
func TestBoardpaar796_UngestellteUhr(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "frisch", "admin")
	alt := uhrGestellt
	uhrGestellt = func() bool { return false }
	t.Cleanup(func() { uhrGestellt = alt })

	// Verify F2: der Vorgänger ist bei ungestellter Uhr „später“, nicht „unbekannt“.
	if _, err := r.TokenPruefe(ctx, BoardpaarTypAusgang, a); !errors.Is(err, ErrBoardpaarUhr) {
		t.Errorf("Vorgänger bei ungestellter Uhr: %v, erwartet ErrBoardpaarUhr", err)
	}
	pruefeGilt(t, r, BoardpaarTypAusgang, b.Token, "aktuelles Token bei ungestellter Uhr", true)
	pruefeGilt(t, r, BoardpaarTypEingang, c, "nie erneuertes Token bei ungestellter Uhr", true)
	vorher, _ := r.TokenListe(ctx)
	if _, err := r.TokenErneuern(ctx, BoardpaarTypEingang, c, ""); !errors.Is(err, ErrBoardpaarUhr) {
		t.Errorf("Erneuern bei ungestellter Uhr: %v, erwartet ErrBoardpaarUhr", err)
	}
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, ""); !errors.Is(err, ErrBoardpaarUhr) {
		t.Errorf("Wiederholung bei ungestellter Uhr: %v, erwartet ErrBoardpaarUhr", err)
	}
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, "abs_erfunden", ""); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("erfundenes Token bei ungestellter Uhr: %v, erwartet ErrBoardpaarTokenUnbekannt", err)
	}
	// Verify F3: ein widerrufenes Token ist auch bei ungestellter Uhr 401
	// (Notaus muss beim Skript Alarm auslösen, nicht „später“).
	w, zw, _ := r.TokenAnlegen(ctx, BoardpaarTypEingang, "widerrufen", "admin")
	if err := r.TokenWiderrufen(ctx, zw.ID); err != nil {
		t.Fatal(err)
	}
	vorher, _ = r.TokenListe(ctx)
	if _, err := r.TokenErneuern(ctx, BoardpaarTypEingang, w, ""); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("widerrufenes Token bei ungestellter Uhr: %v, erwartet ErrBoardpaarTokenUnbekannt", err)
	}
	nachher, _ := r.TokenListe(ctx)
	if len(nachher) != len(vorher) {
		t.Errorf("bei ungestellter Uhr geschrieben: %d → %d Zugänge", len(vorher), len(nachher))
	}
}

// Kritiker V-4: Ein unlesbarer Zeitpunkt gilt als abgelaufen.
func TestBoardpaar796_TokenGiltFailClosed(t *testing.T) {
	jetzt := time.Now()
	for name, f := range map[string]struct {
		revoked, bis string
		uhr, soll    bool
	}{
		"kaputt":             {"", "kaputt", true, false},
		"leer gilt":          {"", "", true, true},
		"leer, Uhr falsch":   {"", "", false, true},
		"künftig":            {"", jetzt.Add(time.Minute).Format(giltBisFormat), true, true},
		"künftig, Uhr fehlt": {"", jetzt.Add(time.Minute).Format(giltBisFormat), false, false},
		"vergangen":          {"", jetzt.Add(-time.Minute).Format(giltBisFormat), true, false},
		"widerrufen":         {"x", "", true, false},
	} {
		if got := tokenGilt(f.revoked, f.bis, jetzt, f.uhr); got != f.soll {
			t.Errorf("%s: %v, erwartet %v", name, got, f.soll)
		}
	}
}

func zaehleEnc(t *testing.T, r *BoardpaarRepo) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM boardpaar_tokens WHERE nachfolger_enc IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Kritiker V-5: Wer einen Vorgänger in der Übergangszeit widerruft, löscht
// auch seinen verschlüsselten Nachfolger.
func TestBoardpaar796_WiderrufLeertNachfolger(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	a, za, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, ""); err != nil {
		t.Fatal(err)
	}
	if n := zaehleEnc(t, r); n != 1 {
		t.Fatalf("%d verschlüsselte Nachfolger vor dem Widerruf, erwartet 1", n)
	}
	if err := r.TokenWiderrufen(ctx, za.ID); err != nil {
		t.Fatal(err)
	}
	if n := zaehleEnc(t, r); n != 0 {
		t.Errorf("%d verschlüsselte Nachfolger nach dem Widerruf des Vorgängers, erwartet 0", n)
	}
}

// Kritiker K-2: Eine Anfrage mit erfundenem Token räumt nicht auf — das
// Aufräumen läuft erst nach dem Ausweis.
func TestBoardpaar796_AufraeumenErstNachAusweis(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	vor(11 * time.Minute)
	_, _ = r.TokenErneuern(ctx, BoardpaarTypAusgang, "abs_erfunden", "")
	if n := zaehleEnc(t, r); n != 1 {
		t.Errorf("erfundenes Token hat aufgeräumt (%d übrig, erwartet 1)", n)
	}
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, b.Token, ""); err != nil {
		t.Fatal(err)
	}
	if n := zaehleEnc(t, r); n != 1 {
		// Der abgelaufene des ersten Vorgängers ist weg, der neue der zweiten
		// Erneuerung liegt in seiner Übergangszeit.
		t.Errorf("nach gültiger Erneuerung %d verschlüsselte Nachfolger, erwartet 1", n)
	}
}

// Kritiker F-I: Eine Erneuerung lässt abgelaufene Vorgänger in Ruhe — ihr
// gilt_bis bleibt, statt auf „jetzt“ vorzurücken.
func TestBoardpaar796_AbgelaufenerVorgaengerBehaeltGiltBis(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	a, za, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	giltBis := func() string {
		var g string
		if err := r.db.QueryRow(`SELECT coalesce(gilt_bis,'') FROM boardpaar_tokens WHERE id = ?`, za.ID).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	vorher := giltBis()
	vor(24 * time.Hour)
	if _, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, b.Token, ""); err != nil {
		t.Fatal(err)
	}
	if nachher := giltBis(); nachher != vorher {
		t.Errorf("gilt_bis des abgelaufenen Vorgängers: %s → %s, erwartet unverändert", vorher, nachher)
	}
}

// Verify F4: Die Kettenkappung löscht den verschlüsselten Nachfolger des
// gekappten Vorgängers — nach A→B→C liegt nur noch der von B.
func TestBoardpaar796_KettenkappungLeertNachfolger(t *testing.T) {
	ctx := context.Background()
	r, _ := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	// Das spätere Aufräumen leerte denselben Wert und verdeckte die Zeile in
	// der Transaktion; deshalb hier abgeschaltet (es fragt uhr.Gestellt,
	// das Erneuern uhrGestellt).
	altUhr, altGestellt := uhr.Gestellt, uhrGestellt
	t.Cleanup(func() { uhr.Gestellt, uhrGestellt = altUhr, altGestellt })
	uhr.Gestellt = func() (bool, error) { return false, nil }
	uhrGestellt = func() bool { return true }
	_, err = r.TokenErneuern(ctx, BoardpaarTypAusgang, b.Token, "")
	uhr.Gestellt, uhrGestellt = altUhr, altGestellt
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM boardpaar_tokens WHERE nachfolger_enc IS NOT NULL AND id <> ?`, b.Zugang.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || zaehleEnc(t, r) != 1 {
		t.Errorf("nach A→B→C: %d fremde verschlüsselte Nachfolger, %d insgesamt; erwartet 0 und 1", n, zaehleEnc(t, r))
	}
}

// Verify F6: Das Aufräumen rechnet gegen die Uhr und läuft bei ungestellter
// Uhr nicht — sonst sperrte eine vorgehende Uhr eine Wiederholung aus.
func TestBoardpaar796_AufraeumenNurMitGestellterUhr(t *testing.T) {
	ctx := context.Background()
	r, vor := erneuernTestDB(t)
	a, _, _ := r.TokenAnlegen(ctx, BoardpaarTypAusgang, "push", "admin")
	b, err := r.TokenErneuern(ctx, BoardpaarTypAusgang, a, "")
	if err != nil {
		t.Fatal(err)
	}
	vor(11 * time.Minute)
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	uhr.Gestellt = func() (bool, error) { return false, nil }
	_, _ = r.TokenPruefe(ctx, BoardpaarTypAusgang, b.Token)
	uhr.Gestellt = alt
	if n := zaehleEnc(t, r); n != 1 {
		t.Errorf("bei ungestellter Uhr aufgeräumt (%d übrig, erwartet 1)", n)
	}
	_, _ = r.TokenPruefe(ctx, BoardpaarTypAusgang, b.Token)
	if n := zaehleEnc(t, r); n != 0 {
		t.Errorf("bei gestellter Uhr nicht aufgeräumt (%d übrig, erwartet 0)", n)
	}
}
