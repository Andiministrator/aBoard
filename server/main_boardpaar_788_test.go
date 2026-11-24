package main

// Karte #788 (ADR-0140 §4): CLI `aboard-server boardpaar …` — dasselbe wie
// der Admin-Reiter, für Server ohne angemeldete Admin-Person.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func boardpaarCLIDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pfad := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	boards := storage.NewBoardRepo(db)
	for _, id := range []string{"b1", "b2"} {
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","sprints":[]}}`)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	return pfad
}

func boardpaarCLI(t *testing.T, db string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runBoardpaarMit(context.Background(), append(append([]string{}, args...), "--db", db), &out)
	return out.String(), err
}

func TestBoardpaarCLI788_Ablauf(t *testing.T) {
	db := boardpaarCLIDB(t)

	out, err := boardpaarCLI(t, db, "seite")
	if err != nil || strings.TrimSpace(out) != "aus" {
		t.Fatalf("seite: %q %v", out, err)
	}
	// Koppeln bei falscher Seite: kein Fehler, aber ein Hinweis.
	out, err = boardpaarCLI(t, db, "koppeln", "--seite", "innen", "--board", "b1")
	if err != nil || !strings.Contains(out, "innen gekoppelt") || !strings.Contains(out, "Achtung") {
		t.Fatalf("koppeln innen: %q %v", out, err)
	}
	if _, err := boardpaarCLI(t, db, "seite", "--setzen", "draussen"); err == nil {
		t.Error("ungültige Seite angenommen")
	}
	if out, err = boardpaarCLI(t, db, "seite", "--setzen", "Innen"); err != nil || !strings.Contains(out, "innen (vorher aus)") {
		t.Fatalf("seite setzen: %q %v", out, err)
	}
	out, err = boardpaarCLI(t, db, "koppeln", "--seite", "aussen", "--board", "b2", "--link", "https://i.example#board=x")
	if err != nil || !strings.Contains(out, "https://i.example/#board=x") || !strings.Contains(out, "Achtung") {
		t.Fatalf("koppeln außen: %q %v", out, err)
	}
	out, err = boardpaarCLI(t, db, "token-neu", "--typ", "abs", "--name", "push")
	if err != nil || !strings.Contains(out, "abs_") {
		t.Fatalf("token-neu: %q %v", out, err)
	}
	klartext := strings.TrimSpace(out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:])
	if !strings.HasPrefix(klartext, "abs_") {
		t.Fatalf("Klartext nicht als letzte Zeile: %q", out)
	}

	out, err = boardpaarCLI(t, db, "liste", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, klartext) {
		t.Error("liste --json trägt das Token")
	}
	var l struct {
		Seite    string                    `json:"seite"`
		Innen    []storage.BoardpaarInnen  `json:"innen"`
		Aussen   []storage.BoardpaarAussen `json:"aussen"`
		Zugaenge []storage.BoardpaarToken  `json:"zugaenge"`
	}
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("liste --json: %v\n%s", err, out)
	}
	if l.Seite != api.BoardpaarSeiteInnen || len(l.Innen) != 1 || l.Innen[0].Von != "cli" || len(l.Aussen) != 1 || len(l.Zugaenge) != 1 || l.Zugaenge[0].CreatedBy != "cli" {
		t.Errorf("liste: %+v", l)
	}
	if out, err = boardpaarCLI(t, db, "liste"); err != nil || !strings.Contains(out, "abs_ push\taktiv, nie genutzt") {
		t.Errorf("liste: %q %v", out, err)
	}

	if out, err = boardpaarCLI(t, db, "token-widerrufen", "--typ", "abs", "--name", "push"); err != nil || !strings.Contains(out, "widerrufen") {
		t.Fatalf("token-widerrufen: %q %v", out, err)
	}
	if _, err = boardpaarCLI(t, db, "token-widerrufen", "--typ", "abs", "--name", "push"); err == nil || !strings.Contains(err.Error(), "kein aktiver Zugang") {
		t.Errorf("zweiter Widerruf: %v", err)
	}
	if out, err = boardpaarCLI(t, db, "entkoppeln", "--seite", "aussen", "--board", "b2"); err != nil || !strings.Contains(out, "gehen mit der Kopplung") {
		t.Errorf("entkoppeln außen: %q %v", out, err)
	}
	if out, err = boardpaarCLI(t, db, "entkoppeln", "--seite", "innen", "--board", "b1"); err != nil || !strings.Contains(out, "erst außen, dann innen") {
		t.Errorf("entkoppeln innen: %q %v", out, err)
	}
	if _, err = boardpaarCLI(t, db, "entkoppeln", "--seite", "innen", "--board", "b1"); err == nil {
		t.Error("zweites Entkoppeln ohne Fehler")
	}
	// Kritiker K-6: --setzen "" heisst aus (wie PruefeBoardpaarSeite), nicht „nur anzeigen“.
	if out, err = boardpaarCLI(t, db, "seite", "--setzen", ""); err != nil || !strings.Contains(out, "aus (vorher innen)") {
		t.Errorf("seite --setzen \"\": %q %v", out, err)
	}
}

// Kritiker K-6: --help je Unterbefehl braucht keine Datenbank und legt keine an.
func TestBoardpaarCLI788_HilfeOhneDB(t *testing.T) {
	fehlt := filepath.Join(t.TempDir(), "fehlt.db")
	for _, u := range []string{"liste", "seite", "koppeln", "entkoppeln", "token-neu", "token-widerrufen"} {
		var out bytes.Buffer
		if err := runBoardpaarMit(context.Background(), []string{u, "--help", "--db", fehlt}, &out); err != nil || !strings.Contains(out.String(), "-db") {
			t.Errorf("%s --help: %v\n%s", u, err, out.String())
		}
	}
	if _, err := os.Stat(fehlt); err == nil {
		t.Error("--help hat eine Datenbank angelegt")
	}
}

func TestBoardpaarCLI788_Fehler(t *testing.T) {
	db := boardpaarCLIDB(t)
	faelle := []struct {
		args []string
		teil string
	}{
		{[]string{"kopeln"}, "unbekannter Unterbefehl"},
		{[]string{"koppeln", "--board", "b1"}, "--seite innen|aussen"},
		{[]string{"koppeln", "--seite", "innen", "--board", "b1", "--link", "https://i.example/#board=x"}, "--link gehört"},
		{[]string{"koppeln", "--seite", "aussen", "--board", "b1"}, "--link gehört"},
		{[]string{"koppeln", "--seite", "innen", "--board", "gibtsnicht"}, "gibt es auf diesem Server nicht"},
		{[]string{"koppeln", "--seite", "innen", "--board", storage.ServiceBoardID}, "Service-Board"},
		{[]string{"koppeln", "--seite", "aussen", "--board", "b1", "--link", "https://i.example:99999/#board=x"}, "port"},
		{[]string{"koppeln", "--seite", "aussen", "--board", "b1", "--link", "http://i.example/#board=x"}, "https"},
		{[]string{"token-neu", "--typ", "abk", "--name", "x"}, "--typ"},
		{[]string{"token-neu", "--typ", "abs"}, "--name"},
		{[]string{"liste", "--name", "x"}, "kennt --name nicht"},
		{[]string{"liste", "rest"}, ""},
	}
	for _, f := range faelle {
		_, err := boardpaarCLI(t, db, f.args...)
		if err == nil || !strings.Contains(err.Error(), f.teil) {
			t.Errorf("%v: Fehler %v, erwartet mit %q", f.args, err, f.teil)
		}
	}
	// Fehlende Datenbank wird nicht angelegt.
	var out bytes.Buffer
	fehlt := filepath.Join(t.TempDir(), "fehlt.db")
	if err := runBoardpaarMit(context.Background(), []string{"liste", "--db", fehlt}, &out); err == nil || !strings.Contains(err.Error(), "nicht gefunden") {
		t.Errorf("fehlende DB: %v", err)
	}
}

// F-2021: Auch die CLI wechselt die Seite nur ohne Kopplung auf der
// bisherigen Seite.
func TestBoardpaarCLI2021_SeitenwechselGesperrt(t *testing.T) {
	db := boardpaarCLIDB(t)
	if _, err := boardpaarCLI(t, db, "seite", "--setzen", "innen"); err != nil {
		t.Fatal(err)
	}
	if _, err := boardpaarCLI(t, db, "koppeln", "--seite", "innen", "--board", "b1"); err != nil {
		t.Fatal(err)
	}
	if out, err := boardpaarCLI(t, db, "seite", "--setzen", "aus"); err == nil || !strings.Contains(err.Error(), "b1") {
		t.Errorf("seite aus, innen gekoppelt: %q %v — erwartet Fehler mit b1", out, err)
	}
	if out, _ := boardpaarCLI(t, db, "seite"); !strings.Contains(out, "innen") {
		t.Errorf("Seite nach Ablehnung: %q, erwartet innen", out)
	}
	if _, err := boardpaarCLI(t, db, "entkoppeln", "--seite", "innen", "--board", "b1"); err != nil {
		t.Fatal(err)
	}
	if out, err := boardpaarCLI(t, db, "seite", "--setzen", "aus"); err != nil || !strings.Contains(out, "aus (vorher innen)") {
		t.Errorf("seite aus nach Entkoppeln: %q %v", out, err)
	}
}

// Sitzungs-QA K-3: `boardpaar liste` nennt je Board den Abgleich-Stand,
// einen Fehlgrund vorn und groß.
func TestBoardpaarCLI2019_ListeZeigtAbgleich(t *testing.T) {
	for _, f := range []struct{ zeit, grund, soll string }{
		{"", "", "noch kein Abruf"},
		{"2026-10-11T09:00:00Z", "", "letzter Abruf 2026-10-11T09:00:00Z, ok"},
		{"2026-10-11T09:00:00Z", "zu viele Einträge", "FEHLER beim letzten Abruf (2026-10-11T09:00:00Z): zu viele Einträge"},
	} {
		if ist := abgleichText("Abruf", f.zeit, f.grund); ist != f.soll {
			t.Errorf("abgleichText(%q, %q) = %q, erwartet %q", f.zeit, f.grund, ist, f.soll)
		}
	}
	db := boardpaarCLIDB(t)
	if _, err := boardpaarCLI(t, db, "koppeln", "--seite", "innen", "--board", "b1"); err != nil {
		t.Fatal(err)
	}
	if out, err := boardpaarCLI(t, db, "liste"); err != nil || !strings.Contains(out, "b1\tseit ") || !strings.Contains(out, "noch kein Abruf") {
		t.Errorf("liste: %q %v", out, err)
	}
}
