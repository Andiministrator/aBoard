package api_test

// Sprint-95-Kritik-Nacharbeit: F-2402 (termin_aendern mit Bestandstitel ohne Inhaltszeichen),
// F-2403 (finding_anlegen und karte_aendern prüfen den Pflicht-Titel auf Inhaltszeichen),
// F-2405 (Upload-Sperre: Lesefehler ist 503, nicht 409). Das Kombinationszeichen entsteht per rune().

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestKritik2402_TerminAendernBestandstitelOhneInhalt(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	akzent := string(rune(0x301))
	if s, b := e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[`+neuerTermin789("t-ks", akzent, "")+`]}`); s != http.StatusOK {
		t.Fatalf("Termin mit Altbestand-Titel anlegen: %d %s", s, b)
	}
	// Nur der Ort ändert sich: der Bestandstitel wird unverändert übernommen.
	if s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/termine/t-ks", `{"ort":"Raum 1"}`); s != http.StatusOK {
		t.Errorf("termin_aendern ort: %d %s, erwartet 200", s, b)
	}
	// Ein neuer Titel ohne Inhaltszeichen bleibt abgelehnt.
	if s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/termine/t-ks", `{"titel":"`+akzent+akzent+`"}`); s != http.StatusBadRequest || !strings.Contains(string(b), "braucht mindestens") {
		t.Errorf("neuer Titel ohne Inhalt: %d %s, erwartet 400", s, b)
	}
	// Die Anlage verlangt weiter ein Inhaltszeichen.
	if s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/termine", `{"titel":"`+akzent+`","datum":"2026-10-23"}`); s != http.StatusBadRequest {
		t.Errorf("termin_anlegen ohne Inhalt: %d %s, erwartet 400", s, b)
	}
}

func TestKritik2403_FindingUndKarteAendernTitel(t *testing.T) {
	e := newEnv671(t, true)
	akzent := string(rune(0x301))
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/findings", `{"titel":"`+akzent+`","typ":"S","prioritaet":"P1"}`); s != http.StatusBadRequest || !strings.Contains(string(b), "braucht mindestens") {
		t.Errorf("finding_anlegen nur Kombinationszeichen: %d %s, erwartet 400", s, b)
	}

	// karte_aendern: Bestandstitel ohne Inhalt bleibt unverändert zulässig, ein geänderter nicht.
	e.zuruecksetzen755(t)
	st := e.stand(t, "b1")
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b1'`, strings.Replace(st, `"title":"Offene Karte"`, `"title":"`+akzent+`"`, 1)); err != nil {
		t.Fatal(err)
	}
	standDe := func() string {
		_, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
		s, _ := json671(t, b)["stand"].(string)
		return s
	}
	stand := standDe()
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"stand":"`+stand+`","titel":"`+akzent+`","beschreibung":"B"}`); s != http.StatusOK {
		t.Errorf("unveränderter Bestandstitel: %d %v, erwartet 200", s, m)
	}
	stand = standDe()
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"stand":"`+stand+`","titel":"`+akzent+akzent+`"}`); s != http.StatusBadRequest {
		t.Errorf("geänderter Titel ohne Inhalt: %d %v, erwartet 400", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"stand":"`+stand+`","titel":"Neu"}`); s != http.StatusOK {
		t.Errorf("normaler neuer Titel: %d %v, erwartet 200", s, m)
	}
}

type fehlerRecht2405 struct{}

func (fehlerRecht2405) UploadGesperrt(context.Context, string, string) (bool, error) {
	return false, errors.New("kopplung nicht lesbar")
}

func TestKritik2405_UploadLesefehlerIst503(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	api.NewAttachmentsAPI(storage.NewAttachmentStore(dir), 1<<20).WithSpiegelUploadRecht(fehlerRecht2405{}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	env := &boardsTestEnv{srv: srv, attachDir: dir}
	if s := lade2242(t, env, "b1", "k1", "att-1"); s != http.StatusServiceUnavailable {
		t.Errorf("Lesefehler der Upload-Sperre: %d, erwartet 503", s)
	}
}
