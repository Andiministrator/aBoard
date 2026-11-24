package mail

// smtp_close_test.go — Sprint-61-Kritiker (B2): Jeder SMTP-Timeout hinterliess
// ein offenes Socket.
//
// `SendMailContext` schloss die Verbindung allein über `defer c.Quit()`.
// Go's `net/smtp.Quit` ruft `c.Text.Close()` erst NACH `c.cmd(221, "QUIT")` —
// schlägt das QUIT fehl (und genau das tut es nach einem Idle-Timeout, weil
// die Gegenstelle ohnehin nicht mehr antwortet), kehrt Quit vorher zurück und
// das Socket bleibt offen. Der Context-Watchdog deckt nur das Ende von `ctx`
// ab, nicht den Deadline-Fall.
//
// Erst F-718 hat diesen Pfad verlässlich erreichbar gemacht: Vorher hing der
// Aufrufer unbegrenzt, statt mit einem Timeout zurückzukehren. Der
// Notification-Worker versucht es viermal je Empfänger (RetryDelays) — also
// vier offene Sockets pro hängendem Empfänger.
//
// Gemessen wird aus Sicht der GEGENSTELLE: Sie liest die Verbindung und
// wartet auf EOF. Kein EOF = das Client-Socket ist noch offen. Das ist der
// einzige Weg, der ohne Blick in Prozess-interne Zustände auskommt.

import (
	"net"
	"testing"
	"time"
)

// stummerSMTPmitEOFWache verhält sich wie `stummerSMTP(t, true)` — Greeting,
// danach Schweigen —, meldet aber zusätzlich über den zurückgegebenen Kanal,
// sobald die Gegenstelle das Socket schliesst (Read liefert EOF).
func stummerSMTPmitEOFWache(t *testing.T) (host string, port int, geschlossen <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	eof := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("220 stumm.example ESMTP\r\n"))
		/* Ab hier antworten wir nicht mehr. Gelesen wird trotzdem — ohne
		   Deadline, damit der Read ausschliesslich dann zurückkehrt, wenn die
		   Gegenstelle die Verbindung schliesst. Genau das ist der Messpunkt. */
		buf := make([]byte, 256)
		for {
			if _, err := conn.Read(buf); err != nil {
				select {
				case eof <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, eof
}

// Nach einem Idle-Timeout muss die Verbindung geschlossen sein — unabhängig
// davon, ob das QUIT noch durchging.
func TestSendMail_SchliesstVerbindungNachIdleTimeout(t *testing.T) {
	host, port, geschlossen := stummerSMTPmitEOFWache(t)
	cfg := stummeConfig(host, port, 300*time.Millisecond)

	err := SendMail(cfg, Message{To: []string{"e@example.com"}, Subject: "x"})
	if err == nil {
		t.Fatal("SendMail meldete Erfolg gegen einen stummen Server")
	}

	select {
	case <-geschlossen:
		// Gegenstelle hat EOF gesehen — das Socket ist zu.
	case <-time.After(3 * time.Second):
		t.Fatalf("nach %q sieht die Gegenstelle 3 s lang kein EOF — das "+
			"Client-Socket bleibt offen (Sprint-61-Kritiker B2: `c.Quit()` "+
			"schliesst bei fehlgeschlagenem QUIT nicht)", err)
	}
}

// Derselbe Pfad im Admin-Test-Hook: `TestConnection` hat dieselbe
// Konstruktion und damit dasselbe Leck. Der Hook läuft im HTTP-Handler und
// wird bei Störungen typischerweise mehrfach hintereinander geklickt.
func TestTestConnection_SchliesstVerbindungNachIdleTimeout(t *testing.T) {
	host, port, geschlossen := stummerSMTPmitEOFWache(t)
	cfg := stummeConfig(host, port, 300*time.Millisecond)

	err := TestConnection(cfg)
	if err == nil {
		t.Fatal("TestConnection meldete Erfolg gegen einen stummen Server")
	}

	select {
	case <-geschlossen:
	case <-time.After(3 * time.Second):
		t.Fatalf("nach %q sieht die Gegenstelle 3 s lang kein EOF — "+
			"TestConnection lässt das Socket offen (Sprint-61-Kritiker B2)", err)
	}
}
