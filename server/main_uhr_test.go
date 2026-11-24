package main

// main_uhr_test.go — F-1192: Start-Log nennt den Zustand der Uhr-Prüfung.

import (
	"errors"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/uhr"
)

func TestUhrZustand(t *testing.T) {
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	for _, fall := range []struct {
		env    string
		ok     bool
		err    error
		enthae string
	}{
		{"aus", false, nil, "Uhr-Prüfung aus"},
		{"", true, nil, "Frist-Löschungen aktiv"},
		{"", false, nil, "warten"},
		{"", false, errors.New("EPERM"), "ABOARD_UHR_PRUEFUNG=aus"},
	} {
		t.Setenv(uhr.PruefungEnvVar, fall.env)
		uhr.Gestellt = func() (bool, error) { return fall.ok, fall.err }
		if got := uhrZustand(); !strings.Contains(got, fall.enthae) {
			t.Errorf("env=%q ok=%v err=%v: %q enthält nicht %q", fall.env, fall.ok, fall.err, got, fall.enthae)
		}
	}
}
