package api_test

// schleuse_auszug_nacharbeit_test.go — Nacharbeit zu den Kritiker-Befunden
// über S2 (Karte #685): KS-5 Grenzen und Nebenläufigkeit, KS-6 Board weg
// während das Modell rechnet, KS-10/F-1618 Log, KS-11/F-1619 Rollen.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

func (e *auszugEnv) entwuerfeAnlegen(t *testing.T, user string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := e.repo.Anlegen(t.Context(), storage.SchleuseAuftrag{BoardID: slBoard, CardID: "k1", UserID: user,
			UserEmail: "x", FreigabeID: e.freigabeID, Anbieter: "anthropic", Modell: "claude-x", Text: "t"}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *auszugEnv) logText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.logs, "\n")
}

// KS-5: 20 offene Entwürfe → 409 ohne Modellaufruf; parallele Anfragen
// füllen höchstens die freien Plätze, und das Modell rechnet nur für sie.
func TestSchleuseAuszug_ObergrenzeParallel(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{})
	e.entwuerfeAnlegen(t, slAnnaID, api.EntwuerfeJePersonHoechst)
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug()); code != http.StatusConflict || !strings.Contains(roh, "zu-viele-entwuerfe") {
		t.Errorf("bei 20 offenen: %d %s", code, roh)
	}
	if e.modell.zahl() != 0 {
		t.Errorf("Modell rechnete trotz voller Grenze: %d", e.modell.zahl())
	}
	if _, err := e.db.Exec(`DELETE FROM schleuse_auftraege WHERE rowid IN (SELECT rowid FROM schleuse_auftraege LIMIT 3)`); err != nil {
		t.Fatal(err)
	}
	e.modell.pause = 300 * time.Millisecond
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug())
			mu.Lock()
			codes[code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codes[http.StatusCreated] != 3 || codes[http.StatusConflict] != 5 || e.zeilen(t) != api.EntwuerfeJePersonHoechst || e.modell.zahl() != 3 {
		t.Errorf("parallel: %v, Zeilen %d, Modellaufrufe %d", codes, e.zeilen(t), e.modell.zahl())
	}
}

// KS-5: höchstens 30 Auszüge je Stunde und Person (429), PUT über der
// Zeichengrenze (+25 %) → 413.
func TestSchleuseAuszug_StundengrenzeUndPutGrenze(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{AuszugZeichen: 500})
	e.modell.inhalt = `{"funde":[]}`
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, map[string]any{"freigabe_id": e.freigabeID, "modell": "claude-x", "aufgabe": "kurz"})
	if code != http.StatusCreated {
		t.Fatalf("POST: %d %s", code, roh)
	}
	a := auftragAus(t, roh)
	if code, roh := e.roh(t, http.MethodPut, e.pfad("/"+a["id"].(string)), slAnna, map[string]any{"text": strings.Repeat("x", 626), "basis_sha256": a["sha256"]}); code != http.StatusRequestEntityTooLarge || !strings.Contains(roh, "zu-gross") {
		t.Errorf("PUT über der Grenze: %d %s", code, roh)
	}
	if code, roh := e.roh(t, http.MethodPut, e.pfad("/"+a["id"].(string)), slAnna, map[string]any{"text": strings.Repeat("x", 625), "basis_sha256": a["sha256"]}); code != http.StatusOK {
		t.Errorf("PUT an der Grenze: %d %s", code, roh)
	}
	// 1 Auszug ist gezählt; 29 weitere (hier ungültige) gehen noch durch
	// die Stundengrenze, der 31. nicht.
	for i := 0; i < api.AuszuegeJeStundeHoechst-1; i++ {
		if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, map[string]any{"unbekannt": 1}); code != http.StatusBadRequest {
			t.Fatalf("Anfrage %d: %d %s", i, code, roh)
		}
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug()); code != http.StatusTooManyRequests || !strings.Contains(roh, "zu-viele-auszuege") {
		t.Errorf("31. Auszug: %d %s", code, roh)
	}
}

// KS-5/KS-6: Sperre oder Löschung des Boards, während das Modell rechnet,
// und ein abgebrochener Browser hinterlassen keine Zeile.
func TestSchleuseAuszug_WaehrendModellrechnung(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{})
	e.modell.pause = 400 * time.Millisecond
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = e.db.Exec(`UPDATE boards SET schleuse_gesperrt = 1 WHERE id = ?`, slBoard)
	}()
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug()); code != http.StatusConflict || !strings.Contains(roh, "board-gesperrt") {
		t.Errorf("Sperre während Modell: %d %s", code, roh)
	}
	if e.zeilen(t) != 0 || e.modell.zahl() != 1 {
		t.Errorf("Zeilen %d, Modellaufrufe %d", e.zeilen(t), e.modell.zahl())
	}
	_, _ = e.db.Exec(`UPDATE boards SET schleuse_gesperrt = 0 WHERE id = ?`, slBoard)

	// Browser bricht nach dem ersten Lebenszeichen ab.
	defer api.SetzeAuszugTakte(50*time.Millisecond, 5*time.Second)()
	raw, _ := json.Marshal(e.auszug())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+e.pfad(""), bytes.NewReader(raw))
	r.Header.Set("Remote-Email", slAnna)
	r.Header.Set("Remote-User", slAnna)
	r.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(r); err == nil {
		// Der Kopf (200 + Lebenszeichen) kann schon da sein; den Rest liest
		// der Browser nicht mehr.
		resp.Body.Close()
	}
	time.Sleep(700 * time.Millisecond)
	if e.zeilen(t) != 0 || e.modell.zahl() != 2 {
		t.Errorf("nach Abbruch: Zeilen %d, Modellaufrufe %d", e.zeilen(t), e.modell.zahl())
	}

	// Board wird gelöscht, während das Modell rechnet.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = storage.NewBoardRepo(e.db).Delete(context.Background(), slBoard)
	}()
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug())
	var m map[string]any
	_ = json.Unmarshal([]byte(roh), &m)
	if code != http.StatusOK || m["status"] != float64(http.StatusNotFound) {
		t.Errorf("Board gelöscht während Modell: %d %s", code, roh)
	}
	if e.zeilen(t) != 0 {
		t.Errorf("Zeilen nach Board-Löschung: %d", e.zeilen(t))
	}
}

// KS-10/F-1618: Der Fehlertext des lokalen Modells steht nicht im Log, der
// echte Status nach einem Lebenszeichen schon.
func TestSchleuseAuszug_LogOhneModelltextMitStatus(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{})
	e.modell.status = http.StatusBadRequest
	e.modell.fehlerBody = "Ungültige Eingabe: [PERSON_1] bestellt bei GEHEIMRESTE"
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug()); code != http.StatusBadGateway || strings.Contains(roh, "GEHEIM") {
		t.Errorf("Modell 400: %d %s", code, roh)
	}
	defer api.SetzeAuszugTakte(20*time.Millisecond, 5*time.Second)()
	e.modell.pause = 100 * time.Millisecond
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug()); code != http.StatusOK || !strings.Contains(roh, `"status":502`) {
		t.Errorf("mit Lebenszeichen: %d %s", code, roh)
	}
	logs := e.logText()
	if strings.Contains(logs, "GEHEIM") || strings.Contains(logs, "bestellt") {
		t.Errorf("Modelltext im Log:\n%s", logs)
	}
	if !strings.Contains(logs, "HTTP 400") || !strings.Contains(logs, "eigentlicher Status 502") {
		t.Errorf("Status fehlt im Log:\n%s", logs)
	}
}

// KS-11/F-1619: Wer zum viewer zurückgestuft wurde, liest seinen Entwurf und
// löscht ihn (über /users/me), ändert ihn aber nicht; ohne jeden Zugang
// bleibt das Löschen. Fremde löschen nichts. Gesperrtes Board: PUT → 409.
func TestSchleuseAuszug_RollenNachEntzug(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{})
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug())
	if code != http.StatusCreated {
		t.Fatalf("POST: %d %s", code, roh)
	}
	a := auftragAus(t, roh)
	id := a["id"].(string)
	if code, roh := e.roh(t, http.MethodPut, "/boards/"+slBoard+"/schleuse-sperre", slAnna, map[string]any{"gesperrt": true}); code != http.StatusOK {
		t.Fatalf("sperren: %d %s", code, roh)
	}
	if code, roh := e.roh(t, http.MethodPut, e.pfad("/"+id), slAnna, map[string]any{"text": "neu", "basis_sha256": a["sha256"]}); code != http.StatusConflict || !strings.Contains(roh, "board-gesperrt") {
		t.Errorf("PUT bei Sperre: %d %s", code, roh)
	}
	_, _ = e.db.Exec(`UPDATE boards SET schleuse_gesperrt = 0 WHERE id = ?`, slBoard)

	acl := storage.NewAclRepo(e.db)
	if err := acl.Set(t.Context(), slBoard, slAnna, storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if code, roh := e.roh(t, http.MethodGet, e.pfad("/"+id), slAnna, nil); code != http.StatusOK {
		t.Errorf("viewer liest eigenen Entwurf: %d %s", code, roh)
	}
	if code, roh := e.roh(t, http.MethodPut, e.pfad("/"+id), slAnna, map[string]any{"text": "neu", "basis_sha256": a["sha256"]}); code != http.StatusForbidden {
		t.Errorf("viewer ändert: %d %s", code, roh)
	}
	if code, _ := e.roh(t, http.MethodDelete, "/users/me/schleuse-auftraege/"+id, slBert, nil); code != http.StatusNotFound {
		t.Errorf("Fremder löscht über /users/me: %d", code)
	}
	if err := acl.Delete(t.Context(), slBoard, slAnna); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.roh(t, http.MethodGet, e.pfad("/"+id), slAnna, nil); code != http.StatusForbidden {
		t.Errorf("ohne Zugang liest: %d", code)
	}
	// Auch ohne ACLGuard davor (Legacy-Bypass, künftige Kette) prüft der
	// Handler selbst: kein Lesen ohne Mitgliedschaft.
	ohneGuard := http.NewServeMux()
	e.sl.Register(ohneGuard)
	nackt := httptest.NewServer(api.WithForwardAuthIfEnabled(ohneGuard))
	defer nackt.Close()
	for _, pfad := range []string{e.pfad("/" + id), e.pfad("")} {
		r, _ := http.NewRequest(http.MethodGet, nackt.URL+pfad, nil)
		r.Header.Set("Remote-Email", slAnna)
		r.Header.Set("Remote-User", slAnna)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("ohne Guard, ohne Zugang: GET %s → %d", pfad, resp.StatusCode)
		}
	}
	if code, roh := e.roh(t, http.MethodDelete, "/users/me/schleuse-auftraege/"+id, slAnna, nil); code != http.StatusOK {
		t.Errorf("ohne Zugang eigenen Entwurf löschen: %d %s", code, roh)
	}
	if e.zeilen(t) != 0 {
		t.Errorf("Zeilen: %d", e.zeilen(t))
	}
	if code, _ := e.roh(t, http.MethodDelete, "/users/me/schleuse-auftraege/"+id, slAnna, nil); code != http.StatusNotFound {
		t.Errorf("zweites Löschen: %d", code)
	}
}

// F-1623: Entwürfe an Karten, die es nicht mehr gibt, zählen nicht gegen die
// Grenze — sie werden vor dem Zählen entfernt. Entwürfe an bestehenden
// Karten und anderer Personen bleiben.
func TestSchleuseAuszug_VerwaisteEntwuerfeZaehlenNicht(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{})
	for i := 0; i < api.EntwuerfeJePersonHoechst; i++ {
		if _, err := e.repo.Anlegen(t.Context(), storage.SchleuseAuftrag{BoardID: slBoard, CardID: "geloescht", UserID: slAnnaID,
			UserEmail: "x", FreigabeID: e.freigabeID, Anbieter: "anthropic", Modell: "claude-x", Text: "t"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	e.entwuerfeAnlegen(t, slAnnaID, 1)  // an k1 — bleibt
	e.entwuerfeAnlegen(t, "u-fremd", 1) // andere Person — bleibt
	if code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug()); code != http.StatusCreated {
		t.Fatalf("mit verwaisten Entwürfen: %d %s, erwartet 201", code, roh)
	}
	var verwaist, k1 int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM schleuse_auftraege WHERE card_id = 'geloescht'`).Scan(&verwaist)
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM schleuse_auftraege WHERE card_id = 'k1'`).Scan(&k1)
	if verwaist != 0 || k1 != 3 {
		t.Errorf("verwaist %d (erwartet 0), an k1 %d (erwartet 3: eigener alter, fremder, neuer)", verwaist, k1)
	}
	if !strings.Contains(e.logText(), "Entwürfe an gelöschten Karten entfernt") {
		t.Errorf("Log nennt das Aufräumen nicht:\n%s", e.logText())
	}
}

// Kritiker KB-6: Ist der Board-Stand nicht lesbar, wird NICHTS gelöscht —
// die Grenze greift weiter (409), die Entwürfe bleiben.
func TestSchleuseAuszug_VerwaisteEntwuerfeBeiUnlesbaremStand(t *testing.T) {
	e := neueAuszugEnv(t, schleuse.Konfig{})
	for i := 0; i < api.EntwuerfeJePersonHoechst; i++ {
		if _, err := e.repo.Anlegen(t.Context(), storage.SchleuseAuftrag{BoardID: slBoard, CardID: "geloescht", UserID: slAnnaID,
			UserEmail: "x", FreigabeID: e.freigabeID, Anbieter: "anthropic", Modell: "claude-x", Text: "t"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, kaputt := range []string{`{"board":{"sprints":"x"}}`, `{"board":{"sprints":[{"cards":[{"title":"ohne id"}]}]}}`} {
		if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, kaputt, slBoard); err != nil {
			t.Fatal(err)
		}
		code, _ := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug())
		var n int
		_ = e.db.QueryRow(`SELECT COUNT(*) FROM schleuse_auftraege WHERE card_id = 'geloescht'`).Scan(&n)
		if n != api.EntwuerfeJePersonHoechst || code == http.StatusCreated {
			t.Errorf("%s: %d Entwürfe übrig (erwartet %d), HTTP %d", kaputt, n, api.EntwuerfeJePersonHoechst, code)
		}
	}
}
