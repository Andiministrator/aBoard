package api

// Sprint-94-Kritik S-2: Wer aus boardpaar.team fällt, verliert auf allen
// Kopien viewer und editor — owner und andere Boards bleiben unberührt.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/google/uuid"
)

func TestBoardpaarKritik94_TeamFolgeEntziehtZugriff(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := storage.NewConfigRepo(db, nil, uuid.NewString)
	paar := storage.NewBoardpaarRepo(db)
	acl := storage.NewAclRepo(db)
	boards := storage.NewBoardRepo(db)

	kopie := "0e5d8a2c-1111-5222-8333-44445555eeee"
	team := "anna@team.example, bernd@team.example"
	if err := cfg.SetMitFolge(ctx, KonfigBoardpaarTeam, team, false, "t", BoardpaarTeamFolge); err != nil {
		t.Fatal(err)
	}
	if _, err := paar.KopieAnlegen(ctx, kopie, "K", "https://i.example/#board=b", "s", teamNachsichtig(team)); err != nil {
		t.Fatal(err)
	}
	// Ein gewöhnliches Board, auf dem Bernd von Hand Leser ist: bleibt.
	if _, err := boards.Insert(ctx, "b-normal", "Normal", "2", []byte(`{"version":"2","board":{"sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := acl.Set(ctx, "b-normal", "bernd@team.example", storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	// Bernd ist zusätzlich owner einer zweiten Kopie: owner bleibt.
	zwei := "0e5d8a2c-1111-5222-8333-44445555ffff"
	if _, err := paar.KopieAnlegen(ctx, zwei, "K2", "https://i.example/#board=b", "s", nil); err != nil {
		t.Fatal(err)
	}
	if err := acl.Set(ctx, zwei, "bernd@team.example", storage.RoleOwner); err != nil {
		t.Fatal(err)
	}
	rolle := func(board, wer string) string {
		t.Helper()
		ro, ok, err := acl.GetRole(ctx, board, wer)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return ro
	}
	if rolle(kopie, "bernd@team.example") != storage.RoleViewer {
		t.Fatal("Vorbedingung: Bernd ist Leser der Kopie")
	}

	// F-2268: ohne Bestätigung entzieht die Folge nichts — sie bricht ab, und
	// der Wert bleibt der alte.
	if err := cfg.SetMitFolge(ctx, KonfigBoardpaarTeam, "anna@team.example", false, "t", BoardpaarTeamFolge); !errors.Is(err, ErrBoardpaarTeamEntzugUnbestaetigt) {
		t.Fatalf("ohne Bestätigung: %v, erwartet ErrBoardpaarTeamEntzugUnbestaetigt", err)
	}
	if ro := rolle(kopie, "bernd@team.example"); ro != storage.RoleViewer {
		t.Fatalf("ohne Bestätigung entzogen: %q", ro)
	}
	if v, err := cfg.Get(ctx, KonfigBoardpaarTeam); err != nil || v.Value != team {
		t.Fatalf("ohne Bestätigung geschrieben: %v %+v", err, v)
	}
	// Bernd fällt aus dem Team (bestätigt).
	var erg BoardpaarTeamEntzug
	if err := cfg.SetMitFolge(ctx, KonfigBoardpaarTeam, "anna@team.example", false, "t", BoardpaarTeamFolgeMit(true, nil, &erg)); err != nil {
		t.Fatal(err)
	}
	if len(erg.Weg) != 1 || erg.Weg[0] != "bernd@team.example" || erg.Zeilen != 1 {
		t.Errorf("Ergebnis: %+v, erwartet weg=[bernd] zeilen=1", erg)
	}
	if ro := rolle(kopie, "bernd@team.example"); ro != "" {
		t.Errorf("Bernd auf der Kopie: %q, erwartet entzogen", ro)
	}
	if ro := rolle(kopie, "anna@team.example"); ro != storage.RoleViewer {
		t.Errorf("Anna bleibt: %q", ro)
	}
	if ro := rolle("b-normal", "bernd@team.example"); ro != storage.RoleViewer {
		t.Errorf("gewöhnliches Board unberührt: %q", ro)
	}
	if ro := rolle(zwei, "bernd@team.example"); ro != storage.RoleOwner {
		t.Errorf("owner bleibt: %q", ro)
	}
	// Schlüssel löschen: das ganze Team fällt weg.
	if err := cfg.DeleteMitFolge(ctx, KonfigBoardpaarTeam, "t", BoardpaarTeamFolge); !errors.Is(err, ErrBoardpaarTeamEntzugUnbestaetigt) {
		t.Fatalf("Löschen ohne Bestätigung: %v", err)
	}
	if ro := rolle(kopie, "anna@team.example"); ro != storage.RoleViewer {
		t.Fatalf("Löschen ohne Bestätigung entzog: %q", ro)
	}
	if err := cfg.DeleteMitFolge(ctx, KonfigBoardpaarTeam, "t", BoardpaarTeamFolgeMit(true, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if ro := rolle(kopie, "anna@team.example"); ro != "" {
		t.Errorf("Anna nach Löschen des Schlüssels: %q", ro)
	}
}

// F-2272 (Sitzungs-Kritik N-6): Ein als Geheimwert gespeichertes Team bekam
// die Folge als "" — der Entzug fiel aus. Jetzt wird entschlüsselt, und ohne
// Schlüssel bricht das Speichern ab.
func TestBoardpaarKritik94_TeamFolgeGeheimwert(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	box, err := secretcrypto.New(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	mitBox := storage.NewConfigRepo(db, box, uuid.NewString)
	ohneBox := storage.NewConfigRepo(db, nil, uuid.NewString)
	paar := storage.NewBoardpaarRepo(db)
	acl := storage.NewAclRepo(db)
	kopie := "0e5d8a2c-1111-5222-8333-44445555eeee"
	// Altbestand: Team als Geheimwert (der Admin-Weg lehnt das heute ab).
	if err := mitBox.Set(ctx, KonfigBoardpaarTeam, "anna@team.example, bernd@team.example", true, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := paar.KopieAnlegen(ctx, kopie, "K", "https://i.example/#board=b", "s",
		[]string{"anna@team.example", "bernd@team.example"}); err != nil {
		t.Fatal(err)
	}
	// Ohne Schlüssel: abbrechen, nichts schreiben.
	if err := ohneBox.SetMitFolge(ctx, KonfigBoardpaarTeam, "anna@team.example", false, "t",
		BoardpaarTeamFolgeMit(true, nil, nil)); !errors.Is(err, storage.ErrKonfigGeheimwert) {
		t.Fatalf("ohne Schlüssel: %v, erwartet ErrKonfigGeheimwert", err)
	}
	if err := ohneBox.DeleteMitFolge(ctx, KonfigBoardpaarTeam, "t",
		BoardpaarTeamFolgeMit(true, nil, nil)); !errors.Is(err, storage.ErrKonfigGeheimwert) {
		t.Fatalf("Löschen ohne Schlüssel: %v, erwartet ErrKonfigGeheimwert", err)
	}
	// Die Rückweg-Folge liest das Team in der Transaktion: ebenfalls Abbruch.
	if err := ohneBox.SetMitFolge(ctx, KonfigBoardpaarRueckweg, BoardpaarRueckwegAn, false, "t",
		BoardpaarRueckwegFolge); !errors.Is(err, storage.ErrKonfigGeheimwert) {
		t.Fatalf("Rückweg-Folge mit geheimem Team: %v, erwartet ErrKonfigGeheimwert", err)
	}
	if ro, ok, _ := acl.GetRole(ctx, kopie, "bernd@team.example"); !ok || ro != storage.RoleViewer {
		t.Fatalf("Vorbedingung: Bernd Leser, %q %v", ro, ok)
	}
	// Mit Schlüssel: der alte Wert wird entschlüsselt, Bernd fällt heraus.
	var erg BoardpaarTeamEntzug
	if err := mitBox.SetMitFolge(ctx, KonfigBoardpaarTeam, "anna@team.example", false, "t",
		BoardpaarTeamFolgeMit(true, nil, &erg)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := acl.GetRole(ctx, kopie, "bernd@team.example"); ok {
		t.Error("Bernd behält die Rolle trotz Austritt (Geheimwert nicht entschlüsselt)")
	}
	if erg.Zeilen != 1 {
		t.Errorf("zeilen = %d, erwartet 1", erg.Zeilen)
	}
}

// Sitzungs-QA #835 K-3: Der Kopie-Eingang liest das Team jetzt in der
// Transaktion von KopieAnlegen (KopieAnlegenTeamAusKonfig), nicht mehr davor.
// Das Wettrennen selbst ist nicht deterministisch nachstellbar; geprüft wird,
// dass die Anlage den gespeicherten Stand nimmt — auch nach einem bestätigten
// Austritt — und bei einem nicht lesbaren Team abbricht.
func TestBoardpaarSitzungsQA835_KopieLiestTeamInTransaktion(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := storage.NewConfigRepo(db, nil, uuid.NewString)
	paar := storage.NewBoardpaarRepo(db)
	acl := storage.NewAclRepo(db)
	rolle := func(board, wer string) string {
		t.Helper()
		ro, ok, err := acl.GetRole(ctx, board, wer)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return ro
	}
	if err := cfg.Set(ctx, KonfigBoardpaarTeam, "anna@team.example, Bernd@TEAM.example", false, "t"); err != nil {
		t.Fatal(err)
	}
	eins := "0e5d8a2c-1111-5222-8333-44445555aaaa"
	if _, err := paar.KopieAnlegenTeamAusKonfig(ctx, eins, "K1", "https://i.example/#board=b", "s"); err != nil {
		t.Fatal(err)
	}
	if rolle(eins, "anna@team.example") != storage.RoleViewer || rolle(eins, "bernd@team.example") != storage.RoleViewer {
		t.Fatalf("Team aus der Konfiguration: anna %q, bernd %q", rolle(eins, "anna@team.example"), rolle(eins, "bernd@team.example"))
	}
	// Bestätigter Austritt, dann neue Kopie: Bernd kommt nicht wieder.
	if err := cfg.SetMitFolge(ctx, KonfigBoardpaarTeam, "anna@team.example", false, "t", BoardpaarTeamFolgeMit(true, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(ctx, KonfigBoardpaarRueckweg, BoardpaarRueckwegAn, false, "t"); err != nil {
		t.Fatal(err)
	}
	zwei := "0e5d8a2c-1111-5222-8333-44445555bbbb"
	frei := true // #836: freigegeben, sonst bliebe das Team Leser
	if _, err := paar.KopieAnlegenTeamAusKonfigMit(ctx, zwei, "K2", "https://i.example/#board=b", "s", &frei); err != nil {
		t.Fatal(err)
	}
	if ro := rolle(zwei, "bernd@team.example"); ro != "" {
		t.Errorf("Bernd nach bestätigtem Austritt auf neuer Kopie: %q", ro)
	}
	if ro := rolle(zwei, "anna@team.example"); ro != storage.RoleEditor {
		t.Errorf("Anna bei Rückweg an: %q, erwartet editor", ro)
	}
	// Team als Geheimwert (Altbestand): Anlage bricht ab, statt ohne Team anzulegen.
	if _, err := db.Exec(`UPDATE server_config SET value_plain = NULL, value_encrypted = 'xx', is_secret = 1 WHERE key = ?`, KonfigBoardpaarTeam); err != nil {
		t.Fatal(err)
	}
	if _, err := paar.KopieAnlegenTeamAusKonfig(ctx, "0e5d8a2c-1111-5222-8333-44445555cccc", "K3", "https://i.example/#board=b", "s"); !errors.Is(err, storage.ErrKonfigGeheimwert) || !errors.Is(err, storage.ErrBoardpaarTeamUnlesbar) {
		t.Fatalf("Geheimwert-Team: %v, erwartet ErrKonfigGeheimwert und ErrBoardpaarTeamUnlesbar (Eingang → 503)", err)
	}
}
