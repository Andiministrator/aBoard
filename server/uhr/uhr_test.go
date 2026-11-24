package uhr

// uhr_test.go — F-1192: Frist-Löschungen nur mit gestellter Uhr.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func stubKernel(t *testing.T, ok bool, err error) {
	t.Helper()
	alt := kernelAbfrage
	kernelAbfrage = func() (bool, error) { return ok, err }
	t.Cleanup(func() { kernelAbfrage = alt })
}

// GEGENPROBE: Liefert gestellt den Kernel-Status nicht durch (etwa immer
// true), wird der Fall „nicht synchron“ rot.
func TestGestellt_FolgtDemKernel(t *testing.T) {
	t.Setenv(PruefungEnvVar, "")
	for _, fall := range []struct {
		ok   bool
		err  error
		soll bool
	}{
		{true, nil, true},
		{false, nil, false},
		{false, errors.New("EPERM"), false}, // nicht lesbar → im Zweifel nicht löschen
	} {
		stubKernel(t, fall.ok, fall.err)
		if got, _ := gestellt(false); got != fall.soll {
			t.Errorf("Kernel %v/%v → %v, erwartet %v", fall.ok, fall.err, got, fall.soll)
		}
	}
}

func TestGestellt_SchalterAusUndTestlauf(t *testing.T) {
	stubKernel(t, false, errors.New("EPERM"))
	t.Setenv(PruefungEnvVar, "aus")
	if got, _ := gestellt(false); !got {
		t.Error("ABOARD_UHR_PRUEFUNG=aus muss die Prüfung abschalten")
	}
	t.Setenv(PruefungEnvVar, "")
	if got, _ := gestellt(true); !got {
		t.Error("unter go test gilt die Uhr als gestellt")
	}
}

func TestPruefeKonfiguration(t *testing.T) {
	for wert, gut := range map[string]bool{"": true, "an": true, "aus": true, "0": false, "off": false} {
		t.Setenv(PruefungEnvVar, wert)
		if err := PruefeKonfiguration(); (err == nil) != gut {
			t.Errorf("%q: err=%v, erwartet gültig=%v", wert, err, gut)
		}
	}
}

// Das Log spricht nur beim Wechsel — sonst flutete jeder Schreibvorgang es.
// GEGENPROBE: Ohne die Vorher-Abfrage in Erlaubt kommen drei Sperr-Zeilen.
func TestErlaubt_LoggtNurBeimWechsel(t *testing.T) {
	alt := Gestellt
	t.Cleanup(func() { Gestellt = alt })
	var zeilen []string
	logf := func(f string, a ...any) { zeilen = append(zeilen, fmt.Sprintf(f, a...)) }

	Gestellt = func() (bool, error) { return false, nil }
	for i := 0; i < 3; i++ {
		if Erlaubt("test-wechsel", logf) {
			t.Fatal("nicht gestellte Uhr erlaubt das Löschen")
		}
	}
	Gestellt = func() (bool, error) { return true, nil }
	for i := 0; i < 2; i++ {
		if !Erlaubt("test-wechsel", logf) {
			t.Fatal("gestellte Uhr verbietet das Löschen")
		}
	}
	if len(zeilen) != 2 || !strings.Contains(zeilen[0], "nicht gestellt") || !strings.Contains(zeilen[1], "jetzt gestellt") {
		t.Fatalf("Log-Zeilen %q, erwartet eine Sperr- und eine Freigabe-Zeile", zeilen)
	}
}
