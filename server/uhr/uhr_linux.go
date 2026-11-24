//go:build linux

package uhr

import "syscall"

// Werte aus <sys/timex.h>: adjtimex liefert TIME_ERROR, solange die Uhr nicht
// synchronisiert ist, und das Statusbit STA_UNSYNC steht, bis ein Zeitdienst
// es zurücknimmt.
const (
	timeError = 5
	staUnsync = 0x0040
)

// kernelSynchron liest den Status mit adjtimex, ohne etwas zu ändern (modes 0).
// Gemessen auf dem Entwicklungsrechner (systemd-timesyncd): state 0, Status
// 0x2001, STA_UNSYNC nicht gesetzt.
func kernelSynchron() (bool, error) {
	var tx syscall.Timex
	state, err := syscall.Adjtimex(&tx)
	if err != nil {
		return false, err
	}
	return state != timeError && tx.Status&staUnsync == 0, nil
}
