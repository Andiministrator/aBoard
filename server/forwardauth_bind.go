package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Andiministrator/aBoard/server/api"
)

// forwardAuthBindOutcome ist das Ergebnis der Bind-Sicherheitsprüfung
// (ADR-0061 (c), F-262).
type forwardAuthBindOutcome int

const (
	// bindOK: Trust=AUS, oder Trust=AN mit sicherem (Loopback-/Unix-)Bind.
	bindOK forwardAuthBindOutcome = iota
	// bindRefuse: Trust=AN + unsicherer Bind + kein Opt-out → Start abbrechen.
	bindRefuse
	// bindWarn: Trust=AN + unsicherer Bind, aber Opt-out gesetzt → starten,
	// aber laut warnen.
	bindWarn
)

// isLoopbackBind entscheidet heuristisch, ob ein TCP-Listen-Bind
// ausschließlich auf das Loopback-Interface zeigt.
//
// Sicher (true): explizit 127.x / localhost / IPv6-Loopback ::1.
// Unsicher (false): leerer Host (":8080" = alle Interfaces), 0.0.0.0,
// IPv6-Any [::], oder eine konkrete Nicht-Loopback-Adresse. Konservativ:
// im Zweifel unsicher — der Betreiber kann bewusst per Opt-out überstimmen.
func isLoopbackBind(addr string) bool {
	a := strings.TrimSpace(addr)
	if a == "" {
		return false // leerer Bind → von net/http als alle Interfaces interpretiert
	}

	// Host-Teil von einem optionalen ":port" trennen.
	host := a
	switch {
	case strings.HasPrefix(a, "["):
		// [host]:port → IPv6-Literal zwischen den Klammern ("[::1]:8080").
		if end := strings.Index(a, "]"); end != -1 {
			host = a[1:end]
		}
	case strings.Count(a, ":") > 1:
		// Mehr als ein ':' ohne Klammern = nacktes IPv6-Literal OHNE Port
		// ("::1", "2001:db8::1"). net.Listen verlangt für IPv6+Port die
		// Klammer-Form; ohne Klammern gibt es also keinen Port abzuschneiden.
		host = a
	case strings.Contains(a, ":"):
		// "host:port" oder ":port" → Host vor dem (einzigen) Doppelpunkt.
		host = a[:strings.LastIndex(a, ":")]
	}
	host = strings.TrimSpace(host)

	if host == "" {
		return false // ":8080" → alle Interfaces
	}
	switch host {
	case "localhost", "::1":
		return true
	case "0.0.0.0", "::":
		return false // explizit alle Interfaces
	}
	// 127.0.0.0/8 ist komplett Loopback.
	if strings.HasPrefix(host, "127.") {
		return true
	}
	return false
}

// forwardAuthBindDecision ist die reine, testbare Kernfunktion für ADR-0061
// (c). Sie trifft KEINE Seiteneffekte (kein Log, kein os.Exit) — der Aufrufer
// (run()) interpretiert das Ergebnis. Parameter explizit übergeben (kein
// os.Getenv hier), damit Tests sie ohne Env-Setzen durchspielen können.
//
//   - trustEnabled: ABOARD_TRUST_FORWARD_AUTH gesetzt?
//   - listenAddr:   ABOARD_ADDR (oder Default)
//   - unixSocket:   ABOARD_UNIX_SOCKET (leer = TCP)
//   - insecureOptOut: ABOARD_FORWARD_AUTH_INSECURE_BIND gesetzt?
func forwardAuthBindDecision(trustEnabled bool, listenAddr, unixSocket string, insecureOptOut bool) forwardAuthBindOutcome {
	if !trustEnabled {
		return bindOK // Trust=AUS → ADR-0057/0059 unverändert, keine Prüfung
	}
	if strings.TrimSpace(unixSocket) != "" {
		return bindOK // Unix-Socket → kein interface-erreichbarer TCP-Port
	}
	if isLoopbackBind(listenAddr) {
		return bindOK // nur Loopback erreichbar → Proxy-davor-Annahme hält
	}
	// Trust=AN + nicht-loopback TCP-Bind = spoofbar.
	if insecureOptOut {
		return bindWarn
	}
	return bindRefuse
}

// forwardAuthBindMessage liefert die Klartext-Begründung für ein
// refuse/warn-Ergebnis (ADR-0061: Risiko benennen + Abhilfen nennen).
func forwardAuthBindMessage(listenAddr string) string {
	return fmt.Sprintf(
		"ABOARD_TRUST_FORWARD_AUTH ist aktiv, aber der Server bindet auf %q "+
			"(nicht ausschließlich Loopback / kein Unix-Socket). In diesem Zustand "+
			"kann jeder, der den Port erreicht, per gefälschtem Remote-Email-Header "+
			"eine beliebige Identität inkl. Owner-Rechte vortäuschen (Total-Bypass, "+
			"F-262/ADR-0061). Abhilfe — eine der folgenden: "+
			"(1) ABOARD_ADDR=127.0.0.1:8080  "+
			"(2) ABOARD_UNIX_SOCKET=/run/aboard/aboard.sock  "+
			"(3) — nur wenn das Netz anderweitig kontrolliert ist — "+
			"ABOARD_FORWARD_AUTH_INSECURE_BIND=1 (Start trotz unsicherem Bind).",
		listenAddr)
}

// checkForwardAuthBind verbindet die reine Decision mit dem Prozess-Verhalten
// (ADR-0061 (c)): Refuse → Fehler (run() bricht ab, log.Fatalf), Warn → laute
// Warnung + Start, OK → still. Liest die relevanten Env-Vars selbst, damit
// run() nur einen Aufruf braucht. logf wird injiziert (Tests prüfen die
// Warnung ohne globalen Logger).
func checkForwardAuthBind(listenAddr, unixSocket string, logf func(format string, v ...any)) error {
	// F-832 (c): eine Lesestelle, eine Konvention — siehe api.EnvSchalterAn.
	trust := api.TrustForwardAuthEnabled()
	optOut := api.EnvSchalterAn("ABOARD_FORWARD_AUTH_INSECURE_BIND")
	switch forwardAuthBindDecision(trust, listenAddr, unixSocket, optOut) {
	case bindRefuse:
		return fmt.Errorf("unsicherer Forward-Auth-Bind: %s", forwardAuthBindMessage(listenAddr))
	case bindWarn:
		logf("aboard-server: ⚠ WARNUNG (ABOARD_FORWARD_AUTH_INSECURE_BIND): %s",
			forwardAuthBindMessage(listenAddr))
	}
	return nil
}

// forwardAuthSecretMessage begründet den Startabbruch ohne Secret
// (ADR-0129 §2, F-1124) und nennt die Abhilfe.
const forwardAuthSecretMessage = "ABOARD_TRUST_FORWARD_AUTH ist aktiv, aber " +
	"ABOARD_FORWARD_AUTH_SECRET ist nicht gesetzt. Ohne Secret kann jeder, der " +
	"den Server erreicht, per gefälschtem Remote-Email-Header eine beliebige " +
	"Identität vortäuschen (F-1124, ADR-0129 §2). Abhilfe: ein zufälliges " +
	"Secret erzeugen (z. B. `openssl rand -hex 32`), als " +
	"ABOARD_FORWARD_AUTH_SECRET setzen und im Proxy als Header " +
	"X-Aboard-Forward-Auth-Secret mitschicken. Wer keinen Proxy davor hat, " +
	"schaltet ABOARD_TRUST_FORWARD_AUTH aus (auth-loser Einzelbetrieb)."

// forwardAuthSecretFehlt ist die reine Kernfunktion der Secret-Pflicht:
// true, wenn Trust an ist und kein (nicht-leeres) Secret gesetzt ist. Nur
// Leerraum zählt als fehlend — ein Secret aus Leerzeichen wäre gesetzt, aber
// wertlos, und würde sich im Proxy-Config kaum von „leer" unterscheiden.
func forwardAuthSecretFehlt(trustEnabled bool, secret string) bool {
	return trustEnabled && strings.TrimSpace(secret) == ""
}

// checkForwardAuthSecret erzwingt die Secret-Pflicht beim Start
// (ADR-0129 §2). Getrennt von checkForwardAuthBind, weil es eine eigene
// Regel ist: Es gibt kein Opt-out. Ein sicherer Bind ersetzt das Secret
// nicht — auf demselben Host (Podman, Agenten) erreicht auch ein
// Nachbarprozess den Loopback-Port.
func checkForwardAuthSecret() error {
	if forwardAuthSecretFehlt(api.TrustForwardAuthEnabled(), os.Getenv("ABOARD_FORWARD_AUTH_SECRET")) {
		return fmt.Errorf("Forward-Auth ohne Secret: %s", forwardAuthSecretMessage)
	}
	return nil
}
