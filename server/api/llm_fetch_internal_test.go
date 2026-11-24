package api

// llm_fetch_internal_test.go — die Autorisierung des Lese-Werkzeugs
// (#428, ADR-0120 §1).
//
// Jede der beiden Bedingungen bekommt hier einen Test, der den VERSTOSS
// versucht — nicht nur einen, der den Normalfall bestätigt. Ein Wächter, der
// nur im erlaubten Fall geprüft wird, bewacht nichts.
//
// Interner Test (package api), weil resolveFetch und fetchVerfuegbar bewusst
// nicht exportiert sind: Sie sind Teil des Handlers, keine öffentliche API.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	fetchTestEmail  = "alice@example.com"
	fetchTestBoard  = "b1"
	fetchTestFremd  = "b2"
	fetchQuellKarte = "c1"
)

// fetchTestAPI baut eine LLMSendAPI mit echtem ACL-Repo. Nur die Felder, die
// resolveFetch anfasst, sind belegt.
func fetchTestAPI(t *testing.T, rollen map[string]string) *LLMSendAPI {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Boards zuerst — die acl-Tabelle hat einen Fremdschlüssel darauf.
	boards := storage.NewBoardRepo(db)
	for _, id := range []string{fetchTestBoard, fetchTestFremd} {
		if _, err := boards.Insert(ctx, id, id, "2", []byte(`{"version":"2","board":{"sprints":[]}}`)); err != nil {
			t.Fatalf("board %s anlegen: %v", id, err)
		}
	}
	acl := storage.NewAclRepo(db)
	for board, rolle := range rollen {
		if err := acl.Set(ctx, board, storage.NormalizeEmail(fetchTestEmail), rolle); err != nil {
			t.Fatalf("acl.Set(%s): %v", board, err)
		}
	}
	return &LLMSendAPI{acl: acl}
}

// karte baut eine Karten-Map wie sie aus state_json kommt (displayId als
// float64 — das ist der Typ, den json.Unmarshal liefert).
func karte(id string, displayID int, titel string, linkZiele ...string) map[string]any {
	links := make([]any, 0, len(linkZiele))
	for _, z := range linkZiele {
		links = append(links, map[string]any{"type": "related", "targetId": z})
	}
	return map[string]any{
		"id":          id,
		"displayId":   float64(displayID),
		"title":       titel,
		"description": "Inhalt von " + titel,
		"links":       links,
	}
}

// standardSnapshot: c1 (Quelle) ist mit c2 verknüpft. c3 ist NICHT verknüpft,
// liegt aber im selben Board. c4 zeigt AUF c1, ohne Rückverweis von c1.
func standardSnapshot() (map[string]any, map[string]map[string]any) {
	quelle := karte(fetchQuellKarte, 1, "Quelle", "c2")
	snap := map[string]map[string]any{
		fetchQuellKarte: quelle,
		"c2":            karte("c2", 2, "Verknuepft", fetchQuellKarte),
		"c3":            karte("c3", 3, "Nicht verknuepft"),
		"c4":            karte("c4", 4, "Zeigt einseitig auf die Quelle", fetchQuellKarte),
	}
	return quelle, snap
}

func TestResolveFetchErlaubtVerknuepfteKarte(t *testing.T) {
	a := fetchTestAPI(t, map[string]string{fetchTestBoard: storage.RoleViewer})
	quelle, snap := standardSnapshot()

	out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "c2", []string{"description"})
	if !out.OK {
		t.Fatalf("verknuepfte Karte abgelehnt: %s", out.Reason)
	}
	if out.DisplayID != 2 || out.Label() != "#2" {
		t.Fatalf("Label falsch: %+v", out)
	}
	if !strings.Contains(out.Content, "Inhalt von Verknuepft") {
		t.Fatalf("Inhalt fehlt:\n%s", out.Content)
	}
}

// Die Symmetrie der Verweise ist Konvention, kein Zwang (ADR-0032). Zeigt nur
// die Zielkarte auf die Quelle, ist die Verknüpfung trotzdem eine Ebene weit.
func TestResolveFetchErlaubtEinseitigenRueckverweis(t *testing.T) {
	a := fetchTestAPI(t, map[string]string{fetchTestBoard: storage.RoleViewer})
	quelle, snap := standardSnapshot()

	// Vorbedingung: die Quelle zeigt NICHT auf c4.
	if hatLinkAuf(quelle, "c4") {
		t.Fatal("Vorbedingung verletzt: die Quelle verweist auf c4 — der Test prueft dann die andere Richtung nicht")
	}
	out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "c4", []string{"description"})
	if !out.OK {
		t.Fatalf("einseitiger Rueckverweis abgelehnt: %s", out.Reason)
	}
}

// VERSTOSS gegen Bedingung (b): eine Karte anfordern, die nicht verknüpft ist.
func TestResolveFetchLehntUnverknuepfteKarteAb(t *testing.T) {
	a := fetchTestAPI(t, map[string]string{fetchTestBoard: storage.RoleViewer})
	quelle, snap := standardSnapshot()

	// Vorbedingung: c3 existiert im Snapshot — sonst prueft der Test nur,
	// dass eine unbekannte id abgelehnt wird, und das ist ein anderer Fall.
	if _, da := snap["c3"]; !da {
		t.Fatal("Vorbedingung verletzt: c3 liegt nicht im Snapshot")
	}
	out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "c3", []string{"description"})
	if out.OK {
		t.Fatalf("unverknuepfte Karte wurde geliefert: %s", out.Content)
	}
	if out.Content != "" {
		t.Fatalf("Ablehnung traegt trotzdem Inhalt: %q", out.Content)
	}
	// Die Ablehnung darf nicht verraten, ob die id existiert — das waere ein
	// Orakel ueber den Kartenbestand.
	unbekannt := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "gibt-es-nicht", []string{"description"})
	if unbekannt.Reason != out.Reason {
		t.Fatalf("unterscheidbare Gruende: existierend=%q unbekannt=%q", out.Reason, unbekannt.Reason)
	}
}

// VERSTOSS gegen Bedingung (b), zweite Spielart: eine Karte anfordern, die auf
// einem anderen Board liegt. Sie ist im Snapshot des Turns nicht enthalten —
// und es wird bewusst nicht anderswo gesucht.
//
// Der erste Anlauf dieses Tests war wertlos (Sicherheits-Kritiker S7): Er
// forderte die id "fremde-karte" an, die es NIRGENDS gab — der Weg durch den
// Code war damit Zeile für Zeile derselbe wie bei „unbekannte id", und der
// Test konnte nicht rot werden, wenn jemand die Board-Grenze aufweicht. Jetzt
// existiert die Karte wirklich, auf einem zweiten Board, mit einer
// beidseitigen Verknüpfung zur Quellkarte — sie erfüllt Bedingung (b) also
// vollständig und scheitert einzig daran, dass sie nicht im Snapshot liegt.
func TestResolveFetchLehntFremdesBoardAb(t *testing.T) {
	// Der Nutzer hat sogar Zugriff auf das fremde Board — die Karte ist
	// trotzdem tabu, weil der Suchraum der Snapshot dieses Turns ist.
	a := fetchTestAPI(t, map[string]string{
		fetchTestBoard: storage.RoleViewer,
		fetchTestFremd: storage.RoleOwner,
	})
	quelle, snap := standardSnapshot()

	// Die Karte auf dem fremden Board: existiert, ist beidseitig mit der
	// Quellkarte verknüpft — und liegt NICHT im Snapshot des Quell-Boards.
	fremdeKarte := karte("fremd-1", 99, "Karte auf Board b2", fetchQuellKarte)
	quelle["links"] = append(quelle["links"].([]any),
		map[string]any{"type": "related", "targetId": "fremd-1"})

	// Vorbedingung 1: Die Verknüpfung ist echt — an ihr scheitert es nicht.
	if !istEinHopVerknuepft(quelle, fremdeKarte) {
		t.Fatal("Vorbedingung verletzt: die Karten sind nicht verknuepft — der Test prueft dann nur (b)")
	}
	// Vorbedingung 2: Sie ist wirklich nicht im Snapshot.
	if _, da := snap["fremd-1"]; da {
		t.Fatal("Vorbedingung verletzt: die fremde Karte liegt im Snapshot")
	}

	out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "fremd-1", []string{"description"})
	if out.OK {
		t.Fatalf("Karte eines fremden Boards wurde geliefert: %s", out.Content)
	}
	if strings.Contains(out.Content, "Board b2") {
		t.Fatalf("Inhalt der fremden Karte ist durchgesickert: %q", out.Content)
	}
}

// VERSTOSS gegen Bedingung (a): der Nutzer hat keine ACL auf dem Board, auf
// dem die Karte liegt. Die Verknüpfung allein genügt nicht.
func TestResolveFetchLehntFehlendeACLAb(t *testing.T) {
	a := fetchTestAPI(t, map[string]string{fetchTestFremd: storage.RoleOwner}) // NICHT auf b1
	quelle, snap := standardSnapshot()

	out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "c2", []string{"description"})
	if out.OK {
		t.Fatalf("Karte ohne Board-Berechtigung geliefert: %s", out.Content)
	}
	if !strings.Contains(out.Reason, "kein Zugriff") {
		t.Fatalf("Grund benennt die fehlende Berechtigung nicht: %q", out.Reason)
	}
}

// Ohne ACL-Repo gibt es keine Standort-Pruefung — dann wird auch nichts
// geliefert. Fail-closed statt „geht schon".
func TestResolveFetchOhneACLRepoLehntAb(t *testing.T) {
	a := &LLMSendAPI{}
	quelle, snap := standardSnapshot()
	out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "c2", []string{"description"})
	if out.OK {
		t.Fatal("ohne ACL-Repo wurde geliefert")
	}
}

func TestResolveFetchLehntQuellkarteUndLeereIDAb(t *testing.T) {
	a := fetchTestAPI(t, map[string]string{fetchTestBoard: storage.RoleViewer})
	quelle, snap := standardSnapshot()

	if out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, fetchQuellKarte, nil); out.OK {
		t.Fatal("die Quellkarte selbst wurde nachgeladen")
	}
	if out := a.resolveFetch(context.Background(), fetchTestEmail, fetchTestBoard, quelle, snap, "   ", nil); out.OK {
		t.Fatal("leere cardId wurde akzeptiert")
	}
}

// Eine unbekannte Rolle sperrt (roleRank == 0 → fail-closed).
//
// Das lässt sich bewusst NICHT als Ende-zu-Ende-Fall bauen: Die acl-Tabelle
// trägt ein CHECK auf ('owner','editor','viewer') (Migration 0002), und
// AclRepo.Set validiert zusätzlich. Ein Test, der die Rolle einzuschleusen
// versucht, würde über eine leere Menge laufen und nichts prüfen. Geprüft
// wird deshalb die Eigenschaft, auf der die Sperre beruht — sie ist die
// Zusicherung, die trägt, falls die Werteliste je erweitert wird, ohne dass
// hier jemand nachzieht.
func TestUnbekannteRolleHatRangNull(t *testing.T) {
	if roleRank["gast"] != 0 {
		t.Fatal("unbekannte Rolle hat einen Rang — die Fetch-Pruefung waere nicht fail-closed")
	}
	if roleRank[storage.RoleViewer] == 0 {
		t.Fatal("viewer hat Rang 0 — dann sperrt die Pruefung ALLE, und die Verstoss-Tests oben sagen nichts aus")
	}
}

func TestFetchVerfuegbar(t *testing.T) {
	mitACL := fetchTestAPI(t, map[string]string{fetchTestBoard: storage.RoleViewer})
	ohneACL := &LLMSendAPI{}

	if mitACL.fetchVerfuegbar(false, "", false) {
		t.Fatal("ohne Anforderung des Clients aktiv")
	}
	if !mitACL.fetchVerfuegbar(true, "", false) {
		t.Fatal("Normalfall nicht verfuegbar")
	}
	if ohneACL.fetchVerfuegbar(true, "", false) {
		t.Fatal("ohne ACL-Repo verfuegbar")
	}
	// ADR-0120 §4: mit redigiertem Prompt kein Nachladen — die nachgeladene
	// Karte hat den Browser-Filter strukturell nicht passiert.
	if mitACL.fetchVerfuegbar(true, "redigierter Prompt", false) {
		t.Fatal("mit redigiertem Prompt trotzdem verfuegbar")
	}
	// Ein Override aus reinem Whitespace gilt im Handler als gesetzt (er
	// prüft `!= ""`, nicht getrimmt) — dann muss er es hier auch. Zwei
	// Schreibweisen derselben Frage driften auseinander.
	if mitACL.fetchVerfuegbar(true, "   ", false) {
		t.Fatal("Whitespace-Override zaehlt hier nicht als gesetzt, im Handler aber schon")
	}
}

// DER Fall, den der erste Anlauf offen liess (Sicherheits-Kritiker S1):
// Der PII-Filter ist AKTIV, findet aber nichts. Dann öffnet sich kein Modal,
// es entsteht kein Override — und trotzdem darf nicht nachgeladen werden.
// Vorher hing die Sperre allein am Override und griff hier nicht.
func TestFetchVerfuegbarAktiverFilterOhneTreffer(t *testing.T) {
	a := fetchTestAPI(t, map[string]string{fetchTestBoard: storage.RoleViewer})
	if a.fetchVerfuegbar(true, "", true) {
		t.Fatal("aktiver PII-Filter ohne Treffer erlaubt Nachladen — die Zusage aus ADR-0120 §4 haelt nicht")
	}
	// Gegenprobe zur Gegenprobe: ohne aktiven Filter bleibt es erlaubt,
	// sonst prüfte der Test nur, dass gar nichts mehr geht.
	if !a.fetchVerfuegbar(true, "", false) {
		t.Fatal("ohne aktiven Filter gesperrt — die Sperre ist zu breit")
	}
}
