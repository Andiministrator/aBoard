package api

import (
	"strings"
	"testing"
)

// Kritik Sprint 93 K-6: Ein ungültiger Eintrag in boardpaar.team lässt nur
// sich selbst wegfallen, nicht das ganze Team.
func TestBoardpaar93_TeamNachsichtig(t *testing.T) {
	got := teamNachsichtig("a@x.de, kaputt\nb@x.de;a@x.de")
	if strings.Join(got, ",") != "a@x.de,b@x.de" {
		t.Errorf("team: %v", got)
	}
	if got := teamNachsichtig(""); len(got) != 0 {
		t.Errorf("leer: %v", got)
	}
}
