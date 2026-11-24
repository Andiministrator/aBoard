// Package uhr beantwortet für alle Frist-Löschungen des Servers eine Frage:
// Ist die Systemuhr gestellt (F-1192, Kritiker Sprint 74)?
//
// Ein Pi ohne gepufferte Uhr startet mit falscher Zeit, bis ein Zeitdienst
// sie stellt — nach einem Stromausfall womöglich lange, wenn auch das
// Gateway erst selbst gestellt werden muss. Geht die Uhr in dieser Zeit vor, liegen Einträge scheinbar
// jenseits ihrer Frist und würden gelöscht. Eine feste Wartezeit nach dem
// Start löst das nicht: Wie lange der Abgleich dauert, weiß der Server nicht.
//
// Gefragt wird deshalb der Kernel: systemd-timesyncd und chrony nehmen das
// Statusbit STA_UNSYNC zurück, sobald sie die Uhr gestellt haben. Gelesen
// wird es mit adjtimex ohne Änderungswunsch — das braucht keine Rechte und
// geht auch im rootless Container. Ohne Zeitdienst bleibt das Bit gesetzt,
// dann wird nie gelöscht; dafür gibt es den Schalter ABOARD_UHR_PRUEFUNG=aus
// (siehe docs/server-betrieb.md).
//
// Was die Prüfung nicht leistet: Einträge, die geschrieben wurden, solange die
// Uhr falsch ging, tragen den falschen Zeitstempel. Die Prüfung verhindert nur,
// dass mit falscher Uhr gelöscht wird.
package uhr

import (
	"fmt"
	"log"
	"os"
	"sync"
	"testing"
)

// PruefungEnvVar schaltet die Prüfung ab ("aus") — nur für Systeme ohne
// Zeitdienst, deren Uhr trotzdem stimmt. Leer oder "an" heisst: prüfen.
const PruefungEnvVar = "ABOARD_UHR_PRUEFUNG"

// kernelAbfrage liest den Synchron-Status des Kernels (uhr_linux.go bzw.
// uhr_andere.go). Variable, damit Tests den Fehlerfall nachbauen können.
var kernelAbfrage = kernelSynchron

// Gestellt meldet, ob gelöscht werden darf: Schalter aus → ja; unter `go test`
// → ja (Tests sollen nicht vom Uhrzustand des Test-Rechners abhängen; wer das
// Gate prüft, ersetzt Gestellt); sonst der Kernel-Status. Ein Fehler beim
// Lesen heisst „nicht gestellt“ — im Zweifel nicht löschen.
var Gestellt = func() (bool, error) { return gestellt(testing.Testing()) }

// gestellt ist Gestellt mit ausdrücklichem Testlauf-Schalter, damit die Tests
// dieses Pakets den Kernel-Pfad erreichen.
func gestellt(testlauf bool) (bool, error) {
	if os.Getenv(PruefungEnvVar) == "aus" {
		return true, nil
	}
	if testlauf {
		return true, nil
	}
	return kernelAbfrage()
}

// PruefeKonfiguration prüft den Schalter beim Start. Ein unbekannter Wert
// hält den Start an, statt still eine Bedeutung zu raten (wie
// ABOARD_AUSGAENGE).
func PruefeKonfiguration() error {
	switch os.Getenv(PruefungEnvVar) {
	case "", "an", "aus":
		return nil
	default:
		return fmt.Errorf("%s=%q ungültig (erwartet an oder aus)", PruefungEnvVar, os.Getenv(PruefungEnvVar))
	}
}

// Zustand je Löschpfad, damit das Log nur beim Wechsel spricht und nicht bei
// jedem Schreibvorgang.
var (
	zustandMu sync.Mutex
	gesperrt  = map[string]bool{}
)

// Erlaubt meldet, ob der Löschpfad zweck jetzt löschen darf. Wechselt der
// Zustand, sagt es das Log einmal: beim Sperren mit Grund und Abhilfe, beim
// Freigeben mit dem Hinweis, dass wieder gelöscht wird. logf nil → log.Printf.
func Erlaubt(zweck string, logf func(string, ...any)) bool {
	if logf == nil {
		logf = log.Printf
	}
	ok, err := Gestellt()
	zustandMu.Lock()
	vorher := gesperrt[zweck]
	gesperrt[zweck] = !ok
	zustandMu.Unlock()
	switch {
	case !ok && !vorher:
		grund := "der Kernel meldet sie als nicht synchronisiert"
		if err != nil {
			grund = fmt.Sprintf("ihr Status ist nicht lesbar (%v)", err)
		}
		logf("aboard-server: %s: Systemuhr nicht gestellt — %s. Es wird nichts nach Frist gelöscht, "+
			"bis ein Zeitdienst sie stellt. Ohne Zeitdienst: %s=aus (F-1192).", zweck, grund, PruefungEnvVar)
	case ok && vorher:
		logf("aboard-server: %s: Systemuhr jetzt gestellt — Frist-Löschung läuft wieder.", zweck)
	}
	return ok
}
