package api_test

// kleinfunde_680_test.go — Karte #680 (Sprint 82): F-1571 (Idempotency-Key
// nach Ablauf bei gelöschter Karte) und F-1575 (kommentieren nach Löschen).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// loescheKommentare entfernt alle Kommentare der Karte mit dem Titel titel
// direkt im gespeicherten Stand von Board b1.
func (e *env671) loescheKommentare(t *testing.T, titel string) {
	t.Helper()
	ctx := context.Background()
	b, err := e.boards.Get(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b.StateJSON, &st); err != nil {
		t.Fatal(err)
	}
	gefunden := false
	for _, sp := range st["board"].(map[string]any)["sprints"].([]any) {
		karten, _ := sp.(map[string]any)["cards"].([]any)
		for _, k := range karten {
			km := k.(map[string]any)
			if km["title"] == titel {
				gefunden = true
				km["comments"] = []any{}
			}
		}
	}
	if !gefunden {
		t.Fatalf("Karte %q nicht gefunden", titel)
	}
	roh, _ := json.Marshal(st)
	if _, err := e.boards.Update(ctx, "b1", b.Title, b.Version, roh, 0); err != nil {
		t.Fatal(err)
	}
}

// F-1571 / KA-4: Mit Kopfzeile ist derselbe Schlüssel dieselbe Anfrage. Nach
// Ablauf des Fensters und gelöschter Karte meldet der Server weder 400 noch
// legt er neu an: „wiederholt“ mit Hinweis. Erst am Folgetag (die Client-id
// hängt am Tag) gilt die Anfrage als neu.
func TestKleinfunde680_IdempotencyKeyNachAblaufUndLoeschen(t *testing.T) {
	e := newEnv671(t, true)
	pfad := "/werkzeuge/boards/b1/karten"
	body := `{"titel":"Mit Schlüssel gelöscht"}`
	mittag := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	jetzt := mittag
	zurueck := api.SetzeOpsNowFuerTest(func() time.Time { return jetzt })
	defer zurueck()
	s1, b1 := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, body, "Idempotency-Key", "tag-1")
	if s1 != http.StatusOK {
		t.Fatalf("erste Anlage: %d %s", s1, b1)
	}
	e.aendereKarte(t, "Mit Schlüssel gelöscht", true)
	// Fenster abgelaufen, derselbe Tag: nichts neu anlegen.
	jetzt = mittag.Add(11 * time.Minute)
	s2, b2 := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, body, "Idempotency-Key", "tag-1")
	if s2 != http.StatusOK || json671(t, b2)["wiederholt"] != true {
		t.Fatalf("Wiederholung nach dem Löschen: %d %s — erwartet wiederholt, nichts neu", s2, b2)
	}
	if n := e.zaehle(t, "b1", `"title":"Mit Schlüssel gelöscht"`); n != 0 {
		t.Fatalf("%d Karten, erwartet 0 (nicht neu angelegt)", n)
	}
	// Folgetag: neuer Wunsch.
	jetzt = mittag.Add(24 * time.Hour)
	s3, b3 := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, body, "Idempotency-Key", "tag-1")
	if s3 != http.StatusOK || json671(t, b3)["wiederholt"] != nil {
		t.Fatalf("am Folgetag nach dem Löschen: %d %s — erwartet neue Karte", s3, b3)
	}
	if n := e.zaehle(t, "b1", `"title":"Mit Schlüssel gelöscht"`); n != 1 {
		t.Fatalf("%d Karten, erwartet 1", n)
	}
}

// KA-4: Mit Kopfzeile holt eine Wiederholung eine archivierte Karte nicht
// zurück; ohne Kopfzeile (V1-4) entsteht im Fenster eine neue.
func TestKleinfunde680_IdempotencyKeyArchiviertLegtNichtNeuAn(t *testing.T) {
	e := newEnv671(t, true)
	pfad := "/werkzeuge/boards/b1/karten"
	body := `{"titel":"Archiviert mit Schlüssel"}`
	if s, b := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, body, "Idempotency-Key", "arch-1"); s != http.StatusOK {
		t.Fatalf("erste Anlage: %d %s", s, b)
	}
	e.aendereKarte(t, "Archiviert mit Schlüssel", false)
	s2, b2 := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, body, "Idempotency-Key", "arch-1")
	if s2 != http.StatusOK || json671(t, b2)["wiederholt"] != true {
		t.Fatalf("Wiederholung nach dem Archivieren: %d %s", s2, b2)
	}
	if n := e.zaehle(t, "b1", `"title":"Archiviert mit Schlüssel"`); n != 1 {
		t.Fatalf("%d Karten, erwartet 1 (die archivierte)", n)
	}
}

// KA-4: Mit Kopfzeile schreibt eine Wiederholung einen gelöschten Kommentar
// nicht erneut.
func TestKleinfunde680_IdempotencyKeyKommentarGeloeschtSchreibtNichtNeu(t *testing.T) {
	e := newEnv671(t, true)
	pfad := "/werkzeuge/boards/b1/karten/5/kommentare"
	body := `{"text":"Einmal mit Schlüssel."}`
	if s, b := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, body, "Idempotency-Key", "kom-1"); s != http.StatusOK {
		t.Fatalf("erster Aufruf: %d %s", s, b)
	}
	e.loescheKommentare(t, "Im Sprint")
	s2, b2 := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, body, "Idempotency-Key", "kom-1")
	if s2 != http.StatusOK || json671(t, b2)["wiederholt"] != true {
		t.Fatalf("Wiederholung: %d %s", s2, b2)
	}
	if n := e.zaehle(t, "b1", `Einmal mit Schlüssel.`); n != 0 {
		t.Fatalf("%d Kommentare im Stand, erwartet 0", n)
	}
}

// KA-8: Wird die Karte zwischen Laden und Wiederholungsprüfung gelöscht,
// meldet kommentieren nicht „bereits geschrieben“, sondern dass die Karte
// gelöscht ist. Der Wettlauf wird über die Zeit-Naht erzwungen: Beim ersten
// Zeitabruf des zweiten Aufrufs (nach dem Laden des Boards) verschwindet die
// Karte.
func TestKleinfunde680_KommentierenKarteInzwischenGeloescht(t *testing.T) {
	e := newEnv671(t, true)
	pfad := "/werkzeuge/boards/b1/karten/5/kommentare"
	body := `{"text":"Karte verschwindet."}`
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", pfad, body); s != http.StatusOK {
		t.Fatalf("erster Aufruf: %d %s", s, b)
	}
	e.loescheKommentare(t, "Im Sprint") // Ledger kennt die Op, der Kommentar fehlt
	einmal := false
	zurueck := api.SetzeOpsNowFuerTest(func() time.Time {
		if !einmal {
			einmal = true
			e.aendereKarte(t, "Im Sprint", true)
		}
		return time.Now().UTC()
	})
	defer zurueck()
	s2, b2 := e.rufe(t, e.token, "anna@firma.de", "POST", pfad, body)
	if !einmal {
		t.Fatal("Naht lief nicht")
	}
	erg, _ := json671(t, b2)["ergebnis"].(string)
	if s2 != http.StatusOK || !strings.Contains(erg, "gelöscht") || strings.Contains(erg, "bereits geschrieben") {
		t.Fatalf("%d %s — erwartet Hinweis „Karte inzwischen gelöscht“", s2, b2)
	}
}

// F-1575: kommentieren nach dem Löschen des Kommentars im Fenster schreibt
// wieder; die Wiederholung DIESES Aufrufs bleibt eine Wiederholung.
func TestKleinfunde680_KommentierenNachLoeschen(t *testing.T) {
	e := newEnv671(t, true)
	pfad := "/werkzeuge/boards/b1/karten/5/kommentare"
	body := `{"text":"Bitte bis Freitag klären."}`
	s1, b1 := e.rufe(t, e.token, "anna@firma.de", "POST", pfad, body)
	if s1 != http.StatusOK || json671(t, b1)["wiederholt"] != nil {
		t.Fatalf("erster Aufruf: %d %s", s1, b1)
	}
	titel := "Im Sprint"
	e.loescheKommentare(t, titel)
	s2, b2 := e.rufe(t, e.token, "anna@firma.de", "POST", pfad, body)
	if s2 != http.StatusOK || json671(t, b2)["wiederholt"] != nil {
		t.Fatalf("nach dem Löschen: %d %s — erwartet neuer Kommentar", s2, b2)
	}
	if n := e.zaehle(t, "b1", `Bitte bis Freitag klären.`); n != 1 {
		t.Fatalf("%d Kommentare im Stand, erwartet 1", n)
	}
	s3, b3 := e.rufe(t, e.token, "anna@firma.de", "POST", pfad, body)
	if s3 != http.StatusOK || json671(t, b3)["wiederholt"] != true {
		t.Fatalf("Wiederholung der Neuanlage: %d %s", s3, b3)
	}
	if n := e.zaehle(t, "b1", `Bitte bis Freitag klären.`); n != 1 {
		t.Fatalf("nach der Wiederholung %d Kommentare, erwartet 1", n)
	}
}

// F-1556: Lehnt der Fold die Op eines Vorschlags-Tokens ab, bekommt es 422
// OHNE Protokoll — das nennt Karten und Felder des Boards (kein Rückkanal,
// ADR-0133 §6). Der Fall wird über die Test-Naht erzwungen.
func TestKleinfunde680_VorschlagAbgelehntOhneProtokoll(t *testing.T) {
	e := newEnv670(t, stand670)
	lief := false
	zurueck := api.SetzeOpsProtokollNahtFuerTest(func(string) string {
		lief = true
		return `[{"op":"x","grund":"GEHEIMER-KARTENTITEL steht auf Karte k1"}]`
	})
	defer zurueck()
	batch := `{"ops":[{"type":"card.upsert","payload":{"card":{"title":"Neue Quelle"}}}]}`
	s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", e.token, batch)
	if !lief {
		t.Fatal("Naht lief nicht — der Test prüft nichts")
	}
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s, erwartet 422", s, b)
	}
	for _, verboten := range []string{"protokoll", "GEHEIMER", "k1", "grund"} {
		if strings.Contains(string(b), verboten) {
			t.Errorf("Antwort enthält %q: %s", verboten, b)
		}
	}
	if !strings.Contains(string(b), "nothing written") {
		t.Errorf("Antwort nennt nicht, dass nichts geschrieben wurde: %s", b)
	}
}

// --- F-1572: Finding-Karten bei Ganz-Board-PUT und Anlegen ----------------

func boardMitFindings680(titel string, karten ...string) []byte {
	return []byte(`{"version":"2","board":{"id":"fb","title":"` + titel + `","sprints":[{"id":"s","cards":[` +
		strings.Join(karten, ",") + `]}]}}`)
}

func finding680(id, nr, typ, status string) string {
	return `{"id":"` + id + `","kind":"finding","finding":{"nr":` + nr + `,"typ":"` + typ + `","prioritaet":"P2","status":"` + status + `"}}`
}

func TestKleinfunde680_PutUndPostLehnenUngueltigeFindingsAb(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	gut := boardMitFindings680("Gut", finding680("a", "1", "K", "offen"), finding680("b", "2", "S", "offen"))
	resp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", gut, nil))
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("gültiges Board: %d", resp.StatusCode)
	}
	for name, schlecht := range map[string]struct {
		karten []string
		teil   string
	}{
		"doppelte nr":  {[]string{finding680("a", "1", "K", "offen"), finding680("b", "1", "S", "offen")}, "more than one card"},
		"Wortschatz":   {[]string{finding680("a", "1", "Q", "offen")}, "finding.typ"},
		"Status":       {[]string{finding680("a", "1", "K", "eingeplant (Sprint 9)")}, "finding.status"},
		"ohne finding": {[]string{`{"id":"a","kind":"finding"}`}, "without a finding object"},
	} {
		r := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/fb", boardMitFindings680("Schlecht", schlecht.karten...),
			http.Header{"If-Match": []string{etag}}))
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(b), schlecht.teil) {
			t.Errorf("PUT %s: %d %s, erwartet 422 mit %q", name, r.StatusCode, b, schlecht.teil)
		}
		p := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", []byte(strings.Replace(string(boardMitFindings680("Neu", schlecht.karten...)), `"id":"fb"`, `"id":"fb2"`, 1)), nil))
		p.Body.Close()
		if p.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("POST %s: %d, erwartet 422", name, p.StatusCode)
		}
	}
	// Nichts davon wurde geschrieben.
	g := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/fb", nil, nil))
	b, _ := io.ReadAll(g.Body)
	g.Body.Close()
	if !strings.Contains(string(b), `"title":"Gut"`) {
		t.Errorf("Stand verändert: %s", b)
	}
}

// Ein Board, das schon vor dem PUT einen Verstoß trug, bleibt speicherbar —
// sonst wäre es eine Falle ohne Rückweg. Ein ZUSÄTZLICHER Verstoß wird
// abgelehnt.
func TestKleinfunde680_PutMitAltemVerstossBleibtMoeglich(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	// Über den Repo-Weg ablegen (POST würde schon ablehnen).
	altVerstoss := boardMitFindings680("Alt", finding680("a", "5", "K", "offen"), finding680("b", "5", "K", "offen"))
	repo := storage.NewBoardRepo(env.db)
	if _, err := repo.Insert(context.Background(), "fb", "Alt", "2", altVerstoss); err != nil {
		t.Fatal(err)
	}
	hole := func() string {
		g := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/fb", nil, nil))
		g.Body.Close()
		return g.Header.Get("ETag")
	}
	r := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/fb", boardMitFindings680("Alt umbenannt", finding680("a", "5", "K", "offen"), finding680("b", "5", "K", "offen")),
		http.Header{"If-Match": []string{hole()}}))
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("PUT mit altem Verstoß: %d, erwartet 200", r.StatusCode)
	}
	r = mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/fb", boardMitFindings680("Alt", finding680("a", "5", "K", "offen"), finding680("b", "5", "K", "offen"), finding680("c", "6", "Q", "offen")),
		http.Header{"If-Match": []string{hole()}}))
	r.Body.Close()
	if r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT mit zusätzlichem Verstoß: %d, erwartet 422", r.StatusCode)
	}
}
