package api

// Karte #1099 (F-2919, ADR-0149 Nachtrag 2026-11-04): Passwort mit Pflicht-2FA
// nachträglich anlegen — nur mit Schalter ABOARD_ACCOUNT_SELF_PASSWORD und nach
// frischer Anmeldung beim gebundenen Anbieter (#1098). Testdaten nur example.org.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

// anlegenStart: Nachweis holen, Start aufrufen; liefert Zwischenstand und
// das neue Geheimnis (der Test öffnet das Siegel).
func (k *kontoTest) anlegenStart(t *testing.T, c *http.Cookie) (string, []byte) {
	t.Helper()
	n := k.bestaetige(t, "google", frischPasswort, "", c)
	rec := k.post("/passwort/anlegen/start", map[string]string{}, c, n)
	if rec.Code != http.StatusOK {
		t.Fatalf("anlegen/start: %d %s", rec.Code, rec.Body.String())
	}
	st := antwort(t, rec)
	zw, _ := st["zwischen"].(string)
	if zw == "" || st["geheimnis"] == "" || !strings.HasPrefix(st["link"].(string), "otpauth://totp/") || !strings.Contains(st["qr"].(string), "<svg") {
		t.Fatalf("Start-Antwort unvollständig: %v", st)
	}
	roh, err := k.box.Open(zw)
	if err != nil {
		t.Fatal(err)
	}
	var z anlegeZwischen
	_ = json.Unmarshal(roh, &z)
	g, _ := base64.StdEncoding.DecodeString(z.Geheimnis)
	// Der Nachweis ist verbraucht.
	if rec := k.post("/passwort/anlegen/start", map[string]string{}, c, n); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != kennungFrischNoetig {
		t.Fatalf("Nachweis zweimal: %d %s", rec.Code, rec.Body.String())
	}
	return zw, g
}

func (k *kontoTest) kontoZeile(t *testing.T, email string) (zustand, weg, hash string, totp bool) {
	t.Helper()
	var h *string
	if err := k.db.QueryRow(`SELECT zustand, weg, passwort_hash, totp_geheimnis IS NOT NULL FROM konten WHERE email = ?`, email).Scan(&zustand, &weg, &h, &totp); err != nil {
		t.Fatal(err)
	}
	if h != nil {
		hash = *h
	}
	return
}

// Vorab-Liste ohne Konto: neues aktives Konto mit Passwort und 2FA.
func TestAnlegen1099_VorabListeOhneKonto(t *testing.T) {
	k, c := pmMitBindung(t)
	k.ke.SelbstPasswort = true
	k.ke.Audit = storage.NewConfigRepo(k.db, nil, uuid.NewString)
	andere := k.sitzung(t, "pm@example.org")
	var s kontoStand
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c).Body.Bytes(), &s)
	if !s.PasswortAnlegen {
		t.Fatalf("Stand bietet Anlegen nicht an: %+v", s)
	}
	zw, g := k.anlegenStart(t, c)
	code := func() string { return totpCode(g, TOTPSchritt(k.uhr)) }

	// Zu kurz, falscher Code, fremder Zwischenstand: nichts entsteht.
	if rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "kurz", "code": code()}, c); rec.Code != http.StatusBadRequest || antwort(t, rec)["fehler"] != "zu_kurz" {
		t.Fatalf("zu kurz: %d %s", rec.Code, rec.Body.String())
	}
	if rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "ganz-neues-passwort-1", "code": "000000"}, c); rec.Code != http.StatusForbidden {
		t.Fatalf("falscher Code: %d", rec.Code)
	}
	if rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "ganz-neues-passwort-1", "code": code()}, andere); rec.Code != http.StatusBadRequest {
		t.Fatalf("andere Sitzung: %d", rec.Code)
	}
	var n int
	_ = k.db.QueryRow(`SELECT COUNT(*) FROM konten`).Scan(&n)
	if n != 0 {
		t.Fatal("Konto vor dem Abschluss angelegt")
	}
	rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "ganz-neues-passwort-1", "code": code()}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("Abschluss: %d %s", rec.Code, rec.Body.String())
	}
	codes, _ := antwort(t, rec)["codes"].([]any)
	if len(codes) != WiederherstellungsAnzahl {
		t.Fatalf("Codes: %v", codes)
	}
	zustand, weg, hash, totp := k.kontoZeile(t, "pm@example.org")
	if zustand != storage.KontoAktiv || weg != "password" || hash != "h:ganz-neues-passwort-1" || !totp {
		t.Fatalf("Konto: %s %s %s %v", zustand, weg, hash, totp)
	}
	neu := sitzungsCookie(rec)
	if neu == nil || k.sitzungGueltig(c) || k.sitzungGueltig(andere) || !k.sitzungGueltig(neu) {
		t.Fatal("Sitzungen nach Anlegen falsch")
	}
	// Der Zeitschritt des bestätigenden Codes ist verbraucht.
	var id string
	_ = k.db.QueryRow(`SELECT id FROM konten WHERE email = 'pm@example.org'`).Scan(&id)
	if _, schritt, _ := k.konten.TOTPGeheimnis(context.Background(), id); schritt != TOTPSchritt(k.uhr) {
		t.Errorf("verbrauchter Schritt %d", schritt)
	}
	// Danach: Stand mit Passwort, Anlegen nicht mehr möglich, zweiter Abschluss abgewiesen.
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, neu).Body.Bytes(), &s)
	if !s.Passwort || !s.ZweiterFaktor || s.PasswortAnlegen || s.FrischNoetig {
		t.Errorf("Stand danach: %+v", s)
	}
	if rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "noch-ein-passwort-2", "code": code()}, neu); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "hat_passwort" {
		t.Errorf("zweiter Abschluss: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(k.auditAktionen(t)[AuditActionKontoPasswortAngelegt], "ergebnis=ok") {
		t.Error("Audit fehlt")
	}
}

// Aktives Konto ohne Passwort (Anbieter-Weg): Passwort und 2FA werden ergänzt;
// `weg` steht danach auf password (F-2923: Anzeige im Admin-Reiter).
func TestAnlegen1099_AnbieterKontoErgaenzt(t *testing.T) {
	k, c := pmMitBindung(t)
	k.ke.SelbstPasswort = true
	k.exec(`INSERT INTO konten (id, email, zustand, weg, angelegt, geaendert) VALUES ('k-pm', 'pm@example.org', 'active', 'google', 1, 1)`)
	zw, g := k.anlegenStart(t, c)
	rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "ganz-neues-passwort-1", "code": totpCode(g, TOTPSchritt(k.uhr))}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("Abschluss: %d %s", rec.Code, rec.Body.String())
	}
	zustand, weg, hash, totp := k.kontoZeile(t, "pm@example.org")
	if zustand != storage.KontoAktiv || weg != "password" || hash != "h:ganz-neues-passwort-1" || !totp {
		t.Fatalf("Konto: %s %s %s %v", zustand, weg, hash, totp)
	}
}

// Ohne Nachweis, mit Nachweis für eine andere Aktion, Schalter aus: nichts.
func TestAnlegen1099_Schranken(t *testing.T) {
	k, c := pmMitBindung(t)
	// Schalter aus: 404, Stand ohne Angebot, frisch/start lehnt die Aktion ab.
	for _, p := range []string{"/passwort/anlegen/start", "/passwort/anlegen/abschluss"} {
		if rec := k.post(p, map[string]string{}, c); rec.Code != http.StatusNotFound {
			t.Errorf("%s bei Schalter aus: %d", p, rec.Code)
		}
	}
	var s kontoStand
	_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c).Body.Bytes(), &s)
	if s.PasswortAnlegen {
		t.Error("Stand bietet Anlegen bei Schalter aus an")
	}
	k.ke.SelbstPasswort = true
	if rec := k.post("/passwort/anlegen/start", map[string]string{}, c); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != kennungFrischNoetig {
		t.Fatalf("ohne Nachweis: %d %s", rec.Code, rec.Body.String())
	}
	n := k.bestaetige(t, "google", frischLoesen, "google", c)
	if rec := k.post("/passwort/anlegen/start", map[string]string{}, c, n); rec.Code != http.StatusConflict {
		t.Fatalf("Nachweis für andere Aktion: %d", rec.Code)
	}
	// Ein 2FA-Zwischenstand taugt nicht als Anlege-Zwischenstand.
	roh, _ := json.Marshal(zwischenstand{KontoID: "k", Sitzung: storage.SitzungsHash(c.Value), Geheimnis: base64.StdEncoding.EncodeToString(make([]byte, totpGeheimnisBytes)), Bis: k.uhr.Add(time.Hour).Unix()})
	fremd, _ := k.box.Seal(roh)
	if rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": fremd, "neu": "ganz-neues-passwort-1", "code": "000000"}, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("2FA-Zwischenstand: %d", rec.Code)
	}
	// Abgelaufener Zwischenstand.
	zw, g := k.anlegenStart(t, c)
	k.uhr = k.uhr.Add(kontoZwischenGueltig + time.Minute)
	if rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "ganz-neues-passwort-1", "code": totpCode(g, TOTPSchritt(k.uhr))}, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("abgelaufen: %d", rec.Code)
	}
	var anzahl int
	_ = k.db.QueryRow(`SELECT COUNT(*) FROM konten`).Scan(&anzahl)
	if anzahl != 0 {
		t.Fatal("Konto trotz Schranke angelegt")
	}
}

// Kollision: Ein Konto im Zustand invited oder pending wird nie überschrieben;
// eine Person mit Passwort legt keins an.
func TestAnlegen1099_Kollision(t *testing.T) {
	for _, zustand := range []string{storage.KontoEingeladen, storage.KontoWartet} {
		t.Run(zustand, func(t *testing.T) {
			k, c := pmMitBindung(t)
			k.ke.SelbstPasswort = true
			// Seit F-2922 lässt das Tor eine Person der Vorab-Liste mit wartendem
			// Konto nicht herein; die Sitzung entstand vorher. Eine Anfrage wärmt
			// den Zwischenspeicher des Tors an, bevor das Konto entsteht (Rennen).
			k.anfrage(http.MethodGet, KontoPfad, nil, c)
			k.exec(`INSERT INTO konten (id, email, zustand, weg, angelegt, geaendert) VALUES ('k-pm', 'pm@example.org', ?, '', 1, 1)`, zustand)
			var s kontoStand
			_ = json.Unmarshal(k.anfrage(http.MethodGet, KontoPfad, nil, c).Body.Bytes(), &s)
			if s.PasswortAnlegen {
				t.Error("Stand bietet Anlegen an")
			}
			if rec := k.post("/frisch/start", map[string]string{"anbieter": "google", "aktion": frischPasswort}, c); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "konto_zustand" {
				t.Errorf("frisch/start: %d", rec.Code)
			}
			if rec := k.post("/passwort/anlegen/start", map[string]string{}, c); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "konto_zustand" {
				t.Errorf("start: %d %s", rec.Code, rec.Body.String())
			}
			if z, _, h, _ := k.kontoZeile(t, "pm@example.org"); z != zustand || h != "" {
				t.Error("Konto verändert")
			}
		})
	}
	k := neuerKontoTest(t, "")
	k.ke.SelbstPasswort = true
	k.person(t, "anna@example.org", true)
	anna := k.sitzung(t, "anna@example.org")
	if rec := k.post("/passwort/anlegen/start", map[string]string{}, anna); rec.Code != http.StatusConflict || antwort(t, rec)["fehler"] != "hat_passwort" {
		t.Errorf("mit Passwort: %d %s", rec.Code, rec.Body.String())
	}
}

// Speicher: PasswortAnlegen in jedem Zustand.
func TestAnlegen1099_Speicher(t *testing.T) {
	k := neuerKontoTest(t, "")
	ctx := context.Background()
	hashes := []string{strings.Repeat("a", 64)}
	anlegen := func(email string) error {
		_, err := k.konten.PasswortAnlegen(ctx, email, uuid.NewString(), "h:x", "v", hashes, 1, 2)
		return err
	}
	if err := anlegen("niemand@example.org"); err != storage.ErrUserNotFound {
		t.Errorf("ohne users-Zeile: %v", err)
	}
	for i, f := range []struct {
		zustand, hash string
		want          error
	}{
		{storage.KontoEingeladen, "", storage.ErrKontoZustand},
		{storage.KontoWartet, "h:alt", storage.ErrKontoZustand},
		{storage.KontoGesperrt, "", storage.ErrKontoZustand},
		{storage.KontoAktiv, "h:alt", storage.ErrHatPasswort},
	} {
		email := "p" + string(rune('a'+i)) + "@example.org"
		k.person(t, email, false)
		k.exec(`INSERT INTO konten (id, email, zustand, weg, passwort_hash, angelegt, geaendert) VALUES (?, ?, ?, '', NULLIF(?, ''), 1, 1)`, "k-"+email, email, f.zustand, f.hash)
		if err := anlegen(email); err != f.want {
			t.Errorf("%s: %v, erwartet %v", f.zustand, err, f.want)
		}
		if z, _, h, _ := k.kontoZeile(t, email); z != f.zustand || h != f.hash {
			t.Errorf("%s: überschrieben (%s, %s)", f.zustand, z, h)
		}
	}
}

// B4 (Sicherheitsprüfung S110): Scheitert nach dem Anlegen nur die neue
// Sitzung, kommen die Codes trotzdem (200, neu_anmelden), keine Sitzung gilt.
func TestAnlegen1099_CodesAuchOhneNeueSitzung(t *testing.T) {
	k, c := pmMitBindung(t)
	k.ke.SelbstPasswort = true
	zw, g := k.anlegenStart(t, c)
	k.ke.Sitzungen = kaputteSitzungen{k.sitzungen}
	rec := k.post("/passwort/anlegen/abschluss", map[string]string{"zwischen": zw, "neu": "ganz-neues-passwort-1", "code": totpCode(g, TOTPSchritt(k.uhr))}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("Status %d %s", rec.Code, rec.Body.String())
	}
	m := antwort(t, rec)
	codes, _ := m["codes"].([]any)
	if len(codes) != WiederherstellungsAnzahl || m["neu_anmelden"] != true {
		t.Fatalf("Antwort: %v", m)
	}
	if sitzungsCookie(rec) != nil || k.sitzungGueltig(c) {
		t.Fatal("Sitzung gesetzt oder alte gilt noch")
	}
	if _, _, h, _ := k.kontoZeile(t, "pm@example.org"); h != "h:ganz-neues-passwort-1" {
		t.Error("Anlegen nicht gespeichert")
	}
}
