package api

// F-3044 (Verify 11.11.): Die Kartenlage für das Aufräumen alter Reste hält ihre
// Richtung fest — invertiert löschte sie Dateien lebender Karten (F-3038).

import (
	"context"
	"errors"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

type lageFest struct {
	besteht bool
	err     error
}

func (l lageFest) KarteBesteht(context.Context, string, string) (bool, error) {
	return l.besteht, l.err
}

func TestF3044_AufraeumLage(t *testing.T) {
	kaputt := errors.New("stand unlesbar")
	for _, f := range []struct {
		name         string
		lage         lageFest
		verwaist     bool
		fehlerErwart bool
	}{
		{"Karte lebt", lageFest{besteht: true}, false, false},
		{"Karte fehlt", lageFest{besteht: false}, true, false},
		{"Board fehlt", lageFest{err: storage.ErrBoardNotFound}, true, false},
		{"anderer Fehler", lageFest{err: kaputt}, false, true},
	} {
		v, err := AufraeumLage(context.Background(), f.lage)("b1", "k1")
		if (err != nil) != f.fehlerErwart || (err == nil && v != f.verwaist) {
			t.Fatalf("%s: verwaist=%v err=%v", f.name, v, err)
		}
	}
}
