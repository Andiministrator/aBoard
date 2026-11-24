package api

// Karte #809 (F-2017): Die 422-Antwort des Schatten-Eingangs nennt ALLE
// fehlerhaften Indizes mit Grund je Index (begrenzt), "error" bleibt.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// karte809: ein Karten-Schatten mit Kennung n und dem gegebenen Titel.
func karte809(n int, titel string) string {
	id := strings.Replace(fmt.Sprintf("%043d", n), "0", "A", 1)
	return fmt.Sprintf(`{"schattenId":%q,"art":"karte","nr":%d,"titel":%q,"vertraulich":false,"spalte":"","prioritaet":"",`+
		`"startDatum":null,"startZeit":null,"endeDatum":null,"endeZeit":null,"bearbeiter":[],"vonAgent":false}`, id, n, titel)
}

// put809 schickt anzahl Einträge; fehlerhaft (kyrillischer Titel) sind alle
// ungeraden Indizes sowie alle ab Index 4. Index 0 und 2 sind gültig.
func put809(t *testing.T, anzahl int) (int, map[string]any) {
	t.Helper()
	code, antwort, _ := put809Db(t, anzahl)
	return code, antwort
}

// put809Db wie put809, liefert zusätzlich die Datenbank (für den Merker).
func put809Db(t *testing.T, anzahl int) (int, map[string]any, *sql.DB) {
	t.Helper()
	_, mux, abe, db := eingangUmgebung779(t)
	teile := []string{}
	for i := 0; i < anzahl; i++ {
		titel := "Angebot prüfen"
		if i%2 == 1 || i >= 4 {
			titel = "Привет"
		}
		teile = append(teile, karte809(i+1, titel))
	}
	body := `{"version":"1.0.0","schatten":[` + strings.Join(teile, ",") + `]}`
	req := httptest.NewRequest("PUT", "/schatten/b1", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+abe)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var antwort map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &antwort)
	return rec.Code, antwort, db
}

func TestSchattenEingang809_AlleIndizesInDer422(t *testing.T) {
	code, a := put809(t, 6) // fehlerhaft: 1, 3, 4, 5
	if code != 422 {
		t.Fatalf("Status %d, erwartet 422", code)
	}
	if e, _ := a["error"].(string); !strings.Contains(e, "schatten[1]") {
		t.Errorf("error muss bestehen bleiben und den ersten Fehler nennen: %v", a["error"])
	}
	liste, _ := a["fehler"].([]any)
	var indizes []int
	for _, x := range liste {
		m := x.(map[string]any)
		indizes = append(indizes, int(m["index"].(float64)))
		if g, _ := m["grund"].(string); !strings.Contains(g, "titel") {
			t.Errorf("Grund nennt das Feld nicht: %v", m)
		}
	}
	if fmt.Sprint(indizes) != "[1 3 4 5]" {
		t.Errorf("Indizes %v, erwartet [1 3 4 5]", indizes)
	}
	if _, da := a["weitere"]; da {
		t.Error("weitere darf ohne Überlauf nicht stehen")
	}
	if strings.Contains(fmt.Sprint(a), "Привет") {
		t.Error("die Antwort darf den Wert nicht enthalten")
	}
}

func TestSchattenEingang809_ListeBegrenzt(t *testing.T) {
	code, a := put809(t, 122) // 120 fehlerhaft
	if code != 422 {
		t.Fatalf("Status %d", code)
	}
	liste, _ := a["fehler"].([]any)
	if len(liste) != schatten.EingangMaxFehlerListe {
		t.Errorf("Liste hat %d Einträge, erwartet %d", len(liste), schatten.EingangMaxFehlerListe)
	}
	if w, _ := a["weitere"].(float64); int(w) != 120-schatten.EingangMaxFehlerListe {
		t.Errorf("weitere = %v, erwartet %d", a["weitere"], 120-schatten.EingangMaxFehlerListe)
	}
}

// T-9 (Kritiker #809): Der Merker der Admin-Anzeige trägt den ersten Fehler,
// aber nicht den Hinweis "siehe fehler" — die Anzeige hat kein Feld "fehler".
// Das HTTP-Feld "error" behält den Hinweis (für ältere Skripte).
func TestSchattenEingang809_MerkerOhneVerweisAufFehlerliste(t *testing.T) {
	code, a, db := put809Db(t, 6) // fehlerhaft: 1, 3, 4, 5
	if code != 422 {
		t.Fatalf("Status %d, erwartet 422", code)
	}
	if e, _ := a["error"].(string); !strings.Contains(e, "siehe fehler") || !strings.Contains(e, "und 3 weitere") {
		t.Errorf("error muss den Hinweis auf die Liste behalten: %v", a["error"])
	}
	var grund string
	if err := db.QueryRow(`SELECT COALESCE(letzter_fehlgrund, '') FROM boardpaar_aussen WHERE aussen_board_id = 'b1'`).Scan(&grund); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(grund, "422: schatten[1]: titel") {
		t.Errorf("Merker nennt den ersten Fehler nicht: %q", grund)
	}
	if strings.Contains(grund, "siehe fehler") {
		t.Errorf("Merker darf nicht auf ein Feld verweisen, das die Anzeige nicht hat: %q", grund)
	}
	if !strings.Contains(grund, "(und 3 weitere fehlerhafte Schatten)") {
		t.Errorf("Merker muss die Zahl der weiteren Fehler nennen (V-1): %q", grund)
	}
}

// Q-6 (Sitzungs-QA #809): Die Log-Zeile trägt Merkertext() — Zahl der weiteren
// Fehler, aber nicht den Hinweis "siehe fehler" (das Log hat kein Feld "fehler").
func TestSchattenEingang809_LogZeileOhneVerweisMitWeiteren(t *testing.T) {
	var puffer bytes.Buffer
	altAusgabe, altFlags := log.Writer(), log.Flags()
	log.SetOutput(&puffer)
	t.Cleanup(func() { log.SetOutput(altAusgabe); log.SetFlags(altFlags) })

	if code, _ := put809(t, 6); code != 422 { // fehlerhaft: 1, 3, 4, 5
		t.Fatalf("Status %d, erwartet 422", code)
	}
	zeile := puffer.String()
	if !strings.Contains(zeile, "schatten-eingang:") {
		t.Fatalf("keine Log-Zeile des Eingangs aufgefangen: %q", zeile)
	}
	if !strings.Contains(zeile, "(und 3 weitere fehlerhafte Schatten)") {
		t.Errorf("Log-Zeile muss die Zahl der weiteren Fehler nennen: %q", zeile)
	}
	if strings.Contains(zeile, "siehe fehler") {
		t.Errorf("Log-Zeile darf nicht auf ein Feld verweisen, das das Log nicht hat: %q", zeile)
	}
}
