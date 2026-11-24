package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/google/uuid"
)

// Karte #778: /me meldet die Seite des Board-Paars, bei „aus“ fehlt das Feld.
func TestMeBoardpaar778(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := storage.NewConfigRepo(db, nil, uuid.NewString)
	mux := http.NewServeMux()
	RegisterMeHandler(mux, nil, MeWithBoardpaar(cfg))
	frage := func() map[string]any {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/me", nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	if _, da := frage()["boardpaar"]; da {
		t.Fatal("ohne Einstellung: Feld da")
	}
	for wert, soll := range map[string]any{"innen": "innen", "aussen": "aussen", "aus": nil, "quatsch": nil} {
		if err := cfg.Set(ctx, KonfigBoardpaarSeite, wert, false, "t"); err != nil {
			t.Fatal(err)
		}
		if ist := frage()["boardpaar"]; ist != soll {
			t.Errorf("%s: %v, erwartet %v", wert, ist, soll)
		}
	}
}

// F-2020: Ist server_config nicht lesbar, meldet /me das als
// `boardpaar_fehler: true` (Status bleibt 200) statt still „aus“ — der
// Browser merkt sich dann kein „aus“ bis zum Neuladen.
func TestMeBoardpaar2020_SeiteNichtLesbar(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "kaputt.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	mux := http.NewServeMux()
	RegisterMeHandler(mux, nil, MeWithBoardpaar(storage.NewConfigRepo(db, nil, uuid.NewString)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/me", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("Status %d, erwartet 200", rec.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m["boardpaar_fehler"] != true {
		t.Errorf("boardpaar_fehler = %v, erwartet true (%s)", m["boardpaar_fehler"], rec.Body.String())
	}
	if _, da := m["boardpaar"]; da {
		t.Errorf("boardpaar trotz Lesefehler gesetzt: %v", m["boardpaar"])
	}
}

// Karte #827 E4 (Plan D10): Außen meldet /me den Rückweg-Schalter als
// `boardpaar_rueckweg: "an"`; bei „aus“, innen und ohne Board-Paar fehlt das
// Feld.
func TestMeBoardpaar827_Rueckweg(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := storage.NewConfigRepo(db, nil, uuid.NewString)
	mux := http.NewServeMux()
	RegisterMeHandler(mux, nil, MeWithBoardpaar(cfg))
	frage := func() map[string]any {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/me", nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	for _, f := range []struct {
		seite, rueckweg string
		soll            any
	}{
		{"aussen", "an", "an"}, {"aussen", "aus", nil}, {"aussen", "quatsch", nil},
		{"innen", "an", nil}, {"aus", "an", nil},
	} {
		if err := cfg.Set(ctx, KonfigBoardpaarSeite, f.seite, false, "t"); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Set(ctx, KonfigBoardpaarRueckweg, f.rueckweg, false, "t"); err != nil {
			t.Fatal(err)
		}
		m := frage()
		if ist := m["boardpaar_rueckweg"]; ist != f.soll {
			t.Errorf("%s/%s: %v, erwartet %v", f.seite, f.rueckweg, ist, f.soll)
		}
		if m["boardpaar_fehler"] != nil {
			t.Errorf("%s/%s: boardpaar_fehler %v", f.seite, f.rueckweg, m["boardpaar_fehler"])
		}
	}
	// Schalter unlesbar (Geheimwert ohne Schlüssel → Lesefehler): wie bei der
	// Seite `boardpaar_fehler`, keine Seite — der Browser fragt neu, statt
	// Spiegelkarten bis zum Neuladen falsch einzustufen.
	if err := cfg.Set(ctx, KonfigBoardpaarSeite, "aussen", false, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE server_config SET is_secret = 1, value_plain = NULL, value_encrypted = 'x' WHERE key = ?`, KonfigBoardpaarRueckweg); err != nil {
		t.Fatal(err)
	}
	m := frage()
	if m["boardpaar_fehler"] != true || m["boardpaar"] != nil || m["boardpaar_rueckweg"] != nil {
		t.Errorf("unlesbar: %v", m)
	}
}
