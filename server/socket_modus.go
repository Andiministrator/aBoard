package main

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// socketModusVoreinstellung ist der Modus der Unix-Sockets, wenn
// ABOARD_UNIX_SOCKET_MODE nicht gesetzt ist — derselbe Wert, der bis v1.42.0
// fest im Code stand (Besitzer und Gruppe, z. B. Caddy in der Gruppe).
const socketModusVoreinstellung os.FileMode = 0o660

// socketModusErlaubt nennt die zulässigen Werte von ABOARD_UNIX_SOCKET_MODE.
// Abschliessend: Ein Socket braucht Lesen+Schreiben, Ausführen hat keine
// Bedeutung, und Mischformen (0640, 0606) wären fast immer ein Tippfehler.
var socketModusErlaubt = map[string]os.FileMode{
	"0600": 0o600, "600": 0o600,
	"0660": 0o660, "660": 0o660,
	"0666": 0o666, "666": 0o666,
}

// socketModus beschreibt, mit welchem Modus die Unix-Sockets (Menschen- und
// Agenten-Listener) angelegt werden.
type socketModus struct {
	modus os.FileMode
	// ausdruecklich: vom Betreiber gesetzt. Dann bricht ein gescheitertes
	// chmod den Start ab, statt nur zu warnen — wer 0666 verlangt, weil der
	// Proxy in einem anderen ID-Bereich läuft, soll nicht erst an 502ern
	// merken, dass der Modus nicht angekommen ist.
	ausdruecklich bool
}

// unixSocketModus liest ABOARD_UNIX_SOCKET_MODE (Karte #577). Hintergrund:
// In einem Team-Betrieb laufen Proxy und aBoard mit `UserNS=auto` in verschiedenen
// ID-Bereichen; dort reicht Besitzer+Gruppe nicht. Ein ungültiger Wert
// bricht den Start ab — im Zweifel nicht offener als verlangt.
func unixSocketModus() (socketModus, error) {
	roh := strings.TrimSpace(os.Getenv("ABOARD_UNIX_SOCKET_MODE"))
	if roh == "" {
		return socketModus{modus: socketModusVoreinstellung}, nil
	}
	m, ok := socketModusErlaubt[roh]
	if !ok {
		return socketModus{}, fmt.Errorf("ABOARD_UNIX_SOCKET_MODE=%q ungültig — erlaubt sind 0600, 0660 und 0666", roh)
	}
	return socketModus{modus: m, ausdruecklich: true}, nil
}

// chmodSocket ist os.Chmod — als Variable, damit ein Test den Fehlschlag
// per Austausch erzeugen kann, statt ihn von aussen herbeizuführen.
var chmodSocket = os.Chmod

// anwenden setzt den Modus auf die Socket-Datei pfad.
func (s socketModus) anwenden(pfad string) error {
	if err := chmodSocket(pfad, s.modus); err != nil {
		if s.ausdruecklich {
			return fmt.Errorf("chmod %q auf %04o (ABOARD_UNIX_SOCKET_MODE): %w", pfad, s.modus, err)
		}
		log.Printf("aboard-server: Warnung: chmod %q: %v", pfad, err)
	}
	return nil
}
