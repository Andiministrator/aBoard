package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// healthcheck.go — Unterbefehl `aboard-server healthcheck` (ADR-0129 §5,
// Karte #542, Betreiber-Anfrage A-9).
//
// Grund: Der bisherige Docker-HEALTHCHECK rief `wget … http://localhost:8080/healthz`
// auf. Das funktioniert weder mit ABOARD_UNIX_SOCKET (kein TCP-Port) noch mit
// einem abweichenden ABOARD_ADDR (falscher Port/Host fest verdrahtet). Dieser
// Unterbefehl liest DIESELBEN Env-Variablen wie der Server selbst
// (ABOARD_UNIX_SOCKET, sonst ABOARD_ADDR mit Default) und trifft dieselbe
// Bind-Entscheidung wie run() in main.go — ohne irgendetwas davon selbst zu
// öffnen: kein storage.Open, keine Migration, kein Listener. Er ist ein
// reiner HTTP-Client gegen den bereits laufenden Prozess.
//
// Kein Auth-Header wird mitgeschickt — und das ist Absicht, nicht Zufall:
// /healthz steht in server/api/forwardauth.go, Funktion offenerPfad, auf der
// Ausnahmeliste des strengen Forward-Auth-Modus. Würde dieser Unterbefehl
// versuchen, sich zu authentifizieren, bräuchte er Zugangsdaten, die einem
// reinen Liveness-Check nicht zustehen — und die es im Container ohnehin
// nicht gibt.

// runHealthcheck ist der Einstiegspunkt für `aboard-server healthcheck`.
// args sind die Argumente NACH dem Subcommand-Namen (os.Args[2:]).
func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	timeout := fs.Duration("timeout", 3*time.Second, "Timeout für den Healthcheck-Request")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Verwendung: aboard-server healthcheck [--timeout <dauer>]

Prüft GET /healthz über denselben Kanal wie der laufende Server selbst:
  - ABOARD_UNIX_SOCKET gesetzt → Unix-Domain-Socket
  - sonst ABOARD_ADDR (Standard %s) → TCP, Wildcard-/leerer Host wird auf
    127.0.0.1 abgebildet

Erwartet Status 200 und "ok":true im JSON-Body. Ohne Auth-Header — /healthz
ist im strengen Forward-Auth-Modus auf der Ausnahmeliste (offenerPfad).
Exit 0 bei Erfolg, Exit 1 sonst.
`, defaultListenAddr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}

	unixSocket, tcpAddr := healthcheckTarget()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	return checkHealth(ctx, unixSocket, tcpAddr)
}

// healthcheckTarget liest EXAKT dieselben zwei Env-Variablen wie run() in
// main.go (ABOARD_UNIX_SOCKET, ABOARD_ADDR mit defaultListenAddr-Fallback) —
// bewusst dieselbe envOrDefault-Funktion, damit ein künftiger Wechsel des
// Defaults nicht an zwei Stellen gepflegt werden muss.
func healthcheckTarget() (unixSocket string, tcpAddr string) {
	unixSocket = os.Getenv("ABOARD_UNIX_SOCKET")
	tcpAddr = envOrDefault("ABOARD_ADDR", defaultListenAddr)
	return unixSocket, tcpAddr
}

// checkHealth führt den eigentlichen Healthcheck aus — als eigene,
// testbare Funktion statt Logik in runHealthcheck/main() zu verstecken.
// unixSocket hat Vorrang vor tcpAddr, genau wie beim Server-Bind in run().
func checkHealth(ctx context.Context, unixSocket, tcpAddr string) error {
	client, url := healthClient(unixSocket, tcpAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("healthcheck: request bauen: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: /healthz nicht erreichbar: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("healthcheck: Antwort nicht lesbar (status %d): %w", resp.StatusCode, err)
	}
	if !body.OK {
		return fmt.Errorf("healthcheck: /healthz meldet ok=false (status %d)", resp.StatusCode)
	}
	// Der Hilfetext verspricht „Status 200 und ok:true“ — beides prüfen.
	// Ein ok:true hinter einem 401/500 (etwa einer vorgeschalteten Schicht,
	// die einen eigenen JSON-Körper liefert) ist kein gesunder Server.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: /healthz antwortet mit Status %d", resp.StatusCode)
	}
	return nil
}

// healthClient baut den HTTP-Client + die Ziel-URL. Bei gesetztem
// unixSocket dialt der Transport über net.Dialer.DialContext auf "unix" statt
// "tcp" — der Host-Teil der URL ist dabei nur Syntax (der DialContext
// ignoriert ihn), muss aber ein gültiger Host sein.
func healthClient(unixSocket, tcpAddr string) (*http.Client, string) {
	if unixSocket != "" {
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", unixSocket)
			},
		}
		return &http.Client{Transport: transport}, "http://unix/healthz"
	}
	return &http.Client{}, "http://" + normalizeHealthHost(tcpAddr) + "/healthz"
}

// normalizeHealthHost macht aus einer Listen-Adresse (z. B. ":8080",
// "0.0.0.0:8080", "0.0.0.0") ein für den Healthcheck erreichbares Ziel: der
// Healthcheck läuft IMMER im selben Container/Netzwerk-Namespace wie der
// Server, ein leerer oder Wildcard-Host ("", "0.0.0.0", "::") ist aber kein
// gültiges Verbindungsziel — dorthin führt nur 127.0.0.1.
func normalizeHealthHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Kein ":" enthalten (z. B. nur "0.0.0.0") — ganze Angabe als Host
		// behandeln, Port fehlt dann und wird unten auf den Default gesetzt.
		host = addr
		port = ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = strconv.Itoa(8080)
	}
	return net.JoinHostPort(host, port)
}
