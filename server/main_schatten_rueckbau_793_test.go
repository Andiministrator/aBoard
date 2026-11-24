package main

// Karte #793 (ADR-0140 §4): Was die Schatten-Endpunkte nach dem Entkoppeln
// und nach dem Widerruf eines Zugangs antworten. Die Antworten stehen im
// Betriebshandbuch (docs/betrieb.md §4.5, „Prüfen nach dem Entkoppeln“) —
// dieser Test hält sie fest:
//
//	außen entkoppelt   → PUT /agent/schatten/<board> 404, Schatten weg
//	innen entkoppelt   → GET /agent/schatten/<board> 404
//	Zugang widerrufen  → 401 an beiden Enden, Schatten bleiben stehen
//
// Außerdem: Entkoppeln lässt das Board selbst (Zustand, Version) unberührt.

import (
	"context"
	"fmt"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestRueckbau793_AntwortenNachEntkoppelnUndWiderruf(t *testing.T) {
	u := eingangAufbau(t)
	ctx := context.Background()
	body := fmt.Sprintf(eingangKarte, schattenID779(1))
	vorher, err := u.boards.Get(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	gleichesBoard := func(stufe string) {
		t.Helper()
		nachher, err := u.boards.Get(ctx, "b1")
		if err != nil {
			t.Fatal(err)
		}
		if string(nachher.StateJSON) != string(vorher.StateJSON) || nachher.Version != vorher.Version {
			t.Errorf("%s: Board-Zustand oder -Version verändert", stufe)
		}
	}

	// (a) Außen: gekoppelt → 200 und ein Schatten; entkoppelt → 404, Schatten weg.
	u.seite(t, "aussen")
	if s := agentAnfrage(t, u.srv, "PUT", "/agent/schatten/b1", u.abe, body, nil); s != 200 {
		t.Fatalf("PUT vor dem Entkoppeln: %d", s)
	}
	if n := len(mustListe(t, u, "b1")); n != 1 {
		t.Fatalf("Schatten vor dem Entkoppeln: %d", n)
	}
	if err := u.paar.AussenEntkoppeln(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, u.srv, "PUT", "/agent/schatten/b1", u.abe, body, nil); s != 404 {
		t.Errorf("PUT nach Entkoppeln außen: %d, erwartet 404", s)
	}
	if n := len(mustListe(t, u, "b1")); n != 0 {
		t.Errorf("Schatten nach Entkoppeln außen: %d, erwartet 0", n)
	}
	gleichesBoard("außen entkoppelt")

	// (b) Innen: gekoppelt → 200; entkoppelt → 404 (Token bleibt gültig).
	u.seite(t, "innen")
	if err := u.paar.InnenKoppeln(ctx, "b1", "test"); err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, u.srv, "GET", "/agent/schatten/b1", u.abs, "", nil); s != 200 {
		t.Fatalf("GET vor dem Entkoppeln: %d", s)
	}
	if err := u.paar.InnenEntkoppeln(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, u.srv, "GET", "/agent/schatten/b1", u.abs, "", nil); s != 404 {
		t.Errorf("GET nach Entkoppeln innen: %d, erwartet 404", s)
	}
	gleichesBoard("innen entkoppelt")

	// (c) Widerruf: 401 an beiden Enden; die Schatten außen bleiben stehen.
	u.seite(t, "aussen")
	if _, err := u.paar.AussenKoppeln(ctx, "b1", "https://innen.example/#board=innen-b1", "test"); err != nil {
		t.Fatal(err)
	}
	abe, ze, err := u.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "widerruf", "test")
	if err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, u.srv, "PUT", "/agent/schatten/b1", abe, body, nil); s != 200 {
		t.Fatalf("PUT mit frischem abe_: %d", s)
	}
	if err := u.paar.TokenWiderrufen(ctx, ze.ID); err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, u.srv, "PUT", "/agent/schatten/b1", abe, body, nil); s != 401 {
		t.Errorf("PUT nach Widerruf: %d, erwartet 401", s)
	}
	if n := len(mustListe(t, u, "b1")); n != 1 {
		t.Errorf("Schatten nach Widerruf: %d, erwartet 1 (bleiben stehen)", n)
	}
	u.seite(t, "innen")
	if err := u.paar.InnenKoppeln(ctx, "b1", "test"); err != nil {
		t.Fatal(err)
	}
	abs, zs, err := u.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "widerruf", "test")
	if err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, u.srv, "GET", "/agent/schatten/b1", abs, "", nil); s != 200 {
		t.Fatalf("GET mit frischem abs_: %d", s)
	}
	if err := u.paar.TokenWiderrufen(ctx, zs.ID); err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, u.srv, "GET", "/agent/schatten/b1", abs, "", nil); s != 401 {
		t.Errorf("GET nach Widerruf: %d, erwartet 401", s)
	}
}
