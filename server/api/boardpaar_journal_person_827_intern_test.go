package api

// Karte #827, F-2221 K-7 (ADR-0141 §5.1, §7; „Umsetzung 2c“ Punkt 12): Person
// und Akteur-Art im Journal außen, über den echten Ops-Weg (postOps) mit dem
// Kontext, den die Middleware bzw. die Vorschlags-Bestätigung setzt.
//   - Agenten-Batch: Person = agent:<Name>, Akteur-Art agent;
//   - bestätigter Vorschlag (vorschlagAusfuehren setzt Agent UND Bestätiger):
//     Person = bestätigender Mensch, Akteur-Art bleibt agent;
//   - Mensch: Person = seine E-Mail (normalisiert), Akteur-Art mensch.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestJournal827_PersonUndAkteurArt(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	// Direkt in server_config: Der Admin-Weg nimmt „an“ bis E4 nicht an.
	if err := cfg.Set(ctx, KonfigBoardpaarRueckweg, BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	m, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.NewBoardRepo(db)
	a := NewBoardsAPI(repo).WithFold(m).MitBoardpaar(cfg, storage.NewBoardpaarRepo(db), storage.NewSchattenRepo(db))

	const kopie = "0e5d8a2c-1111-5222-8333-44445555e827"
	if _, err := storage.NewBoardpaarRepo(db).KopieAnlegen(ctx, kopie, "Kopie", "https://i.example/#board=b", "skript:t", nil); err != nil {
		t.Fatal(err)
	}
	// #836: freigegeben, damit allein der Server-Schalter entscheidet.
	if err := storage.NewBoardpaarRepo(db).KopieBearbeitbarSetzen(ctx, kopie, true); err != nil {
		t.Fatal(err)
	}
	kennung := strings.Repeat("K", 43)
	k := &schatten.Kopie{
		Struktur: schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: schatten.BacklogLane, Titel: "Backlog", Kind: "backlog"}},
			Spalten: []schatten.StrukturSpalte{{ID: "col-backlog", Titel: "Backlog"}}},
		Spiegel: []schatten.SpiegelEingang{{SpiegelID: kennung, Karte: &schatten.SpiegelKarte{Art: schatten.ArtKarte,
			Lane: schatten.BacklogLane, Spalte: "col-backlog", Title: "Spiegel", Labels: []string{}, Assignees: []string{},
			Comments: []schatten.SpiegelKommentar{}}}}}
	if _, err := storage.NewSchattenRepo(db).ErsetzeKopie(ctx, kopie, func(state []byte) ([]byte, error) {
		neu, _, err := schatten.KopieAnwenden(state, k)
		return neu, err
	}, nil); err != nil {
		t.Fatal(err)
	}
	karte := schatten.AbgeleiteteID(kennung)

	agent := &AgentPrincipal{Name: "recherche", BoardID: kopie, Role: storage.AgentRolleIntern, tokenRolle: storage.AgentRolleIntern}
	agentCtx := context.WithValue(ctx, contextKeyAgent, agent)
	faelle := []struct {
		name, feld, person, art string
		ctx                     context.Context
	}{
		{"Agent", "title", "agent:recherche", "agent", agentCtx},
		{"bestätigter Vorschlag", "description", "gerd@team.example", "agent",
			context.WithValue(agentCtx, vorschlagBestaetigerSchluessel{}, "gerd@team.example")},
		{"Mensch", "priority", "anna@firma.de", "mensch",
			context.WithValue(ctx, contextKeyIdentity, &ForwardAuthIdentity{Email: "Anna@Firma.de"})},
	}
	for _, f := range faelle {
		wert := `"neu"`
		if f.feld == "priority" {
			wert = `"high"`
		}
		body := `{"ops":[{"type":"card.field","payload":{"cardId":"` + karte + `","field":"` + f.feld + `","value":` + wert + `}}]}`
		req := httptest.NewRequest(http.MethodPost, "/boards/"+kopie+"/ops", strings.NewReader(body)).WithContext(f.ctx)
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", kopie)
		rec := httptest.NewRecorder()
		a.postOps(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", f.name, rec.Code, rec.Body.String())
		}
	}
	abruf, err := repo.JournalAbholen(ctx, kopie, 0, 0, time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(abruf.Eintraege) != len(faelle) {
		t.Fatalf("Journal: %+v", abruf.Eintraege)
	}
	for _, f := range faelle {
		gefunden := false
		for _, e := range abruf.Eintraege {
			if e.Art == schatten.JArtFeld && e.Stelle == f.feld {
				gefunden = true
				if e.Person != f.person || e.Akteur != f.art {
					t.Errorf("%s: Person %q, Akteur-Art %q; erwartet %q, %q", f.name, e.Person, e.Akteur, f.person, f.art)
				}
			}
		}
		if !gefunden {
			t.Errorf("%s: kein Eintrag feld %s: %+v", f.name, f.feld, abruf.Eintraege)
		}
	}
}
