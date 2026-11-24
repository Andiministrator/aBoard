package api

// schleuse_dateien_intern_test.go — bilderPassen, die zweite Prüfung nach
// BeginneVersand (Karte #687): Sie muss für sich allein jede Abweichung
// zwischen vorab geladenen Bildern und dem verbindlich gelesenen Auftrag
// erkennen.

import (
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/schleuse"
)

func TestBilderPassen(t *testing.T) {
	a, b := []byte("bild-a"), []byte("bild-b")
	ea := schleuse.BildEintrag{ID: "a", SHA256: schleuse.HashBytes(a), Freigegeben: true}
	eb := schleuse.BildEintrag{ID: "b", SHA256: schleuse.HashBytes(b), Freigegeben: true}
	la := llm.ImageAttachment{ID: "a", Data: a}
	lb := llm.ImageAttachment{ID: "b", Data: b}
	nichtFrei := eb
	nichtFrei.Freigegeben = false
	fall := []struct {
		name    string
		geladen []llm.ImageAttachment
		bilder  []schleuse.BildEintrag
		soll    bool
	}{
		{"leer", nil, nil, true},
		{"nur nicht freigegebene", nil, []schleuse.BildEintrag{nichtFrei}, true},
		{"gleich", []llm.ImageAttachment{la, lb}, []schleuse.BildEintrag{ea, eb}, true},
		{"Reihenfolge", []llm.ImageAttachment{lb, la}, []schleuse.BildEintrag{ea, eb}, false},
		{"eines fehlt", []llm.ImageAttachment{la}, []schleuse.BildEintrag{ea, eb}, false},
		{"eines zu viel", []llm.ImageAttachment{la, lb}, []schleuse.BildEintrag{ea, nichtFrei}, false},
		{"andere Bytes", []llm.ImageAttachment{{ID: "a", Data: b}}, []schleuse.BildEintrag{ea}, false},
		{"andere id", []llm.ImageAttachment{{ID: "x", Data: a}}, []schleuse.BildEintrag{ea}, false},
	}
	for _, f := range fall {
		if got := bilderPassen(f.geladen, f.bilder); got != f.soll {
			t.Errorf("%s: %v, soll %v", f.name, got, f.soll)
		}
	}
}
