package api_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

// TestICalFeed_DrosselNichtFaelschbar (F-2109): Die Drossel des Feeds lässt
// sich nicht mit wechselndem X-Forwarded-For umgehen — weder mit gültigem
// noch mit unbekanntem Token —, und zwei Abos hinter derselben Adresse teilen
// sich keinen Topf.
func TestICalFeed_DrosselNichtFaelschbar(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'andi@example.com', 'N', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	tokens := storage.NewICalTokenRepo(db, box)
	ical := api.NewICalAPI(tokens, storage.NewUserRepo(db), storage.NewBoardRepo(db), storage.NewAclRepo(db))
	ical.SetzeFeedGrenzenFuerTest(2)
	mux := http.NewServeMux()
	ical.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var aboID string
	abo := func() string {
		t.Helper()
		s, err := tokens.Create(ctx, "u1", nil)
		if err != nil {
			t.Fatal(err)
		}
		aboID = s.ID
		return s.Token
	}
	n := 0
	abruf := func(token string) int {
		t.Helper()
		n++
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/ical/calendar.ics?token="+token, nil)
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", n))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	a := abo()
	idA := aboID
	b := abo()
	for i, soll := range []int{200, 200} {
		if got := abruf(a); got != soll {
			t.Errorf("Abo A, Abruf %d: %d, erwartet %d", i+1, got, soll)
		}
	}
	// Kritik K-3: Ein gedrosselter Abruf schreibt last_used_at nicht.
	if _, err := db.Exec(`UPDATE ical_subscriptions SET last_used_at = 'vorher' WHERE id = ?`, idA); err != nil {
		t.Fatal(err)
	}
	if got := abruf(a); got != http.StatusTooManyRequests {
		t.Errorf("Abo A, Abruf 3: %d, erwartet 429", got)
	}
	var zuletzt string
	if err := db.QueryRow(`SELECT last_used_at FROM ical_subscriptions WHERE id = ?`, idA).Scan(&zuletzt); err != nil || zuletzt != "vorher" {
		t.Errorf("last_used_at nach 429 = %q (%v), erwartet unverändert", zuletzt, err)
	}
	if got := abruf(b); got != http.StatusOK {
		t.Errorf("Abo B nach erschöpftem A: %d, erwartet 200 (eigener Topf)", got)
	}
	for i, soll := range []int{404, 404, 429} {
		if got := abruf("gibt-es-nicht"); got != soll {
			t.Errorf("unbekanntes Token, Abruf %d: %d, erwartet %d", i+1, got, soll)
		}
	}
}
