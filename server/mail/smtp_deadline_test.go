package mail

// smtp_deadline_test.go — F-718: Deadlines auf der offenen SMTP-Verbindung
// und Abbruch per Context.
//
// Bis Sprint 61 deckte `net.DialTimeout` allein den Verbindungsaufbau ab.
// Alles danach — Greeting, EHLO, STARTTLS, AUTH, MAIL, RCPT, DATA, Body —
// lief auf einer Verbindung ohne jede Frist. Ein SMTP-Server, der nach dem
// Connect verstummt (Netzwerk-Blackhole, überlastete Gegenstelle, Firewall
// die Pakete schluckt statt sie abzulehnen), hielt den Aufrufer damit
// unbegrenzt fest.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// stummerSMTP nimmt Verbindungen an und schweigt danach — der Fall aus dem
// Finding. `nachGreeting` steuert, ob wenigstens das 220-Greeting kommt:
// damit lässt sich zwischen „hängt schon beim Verbindungsaufbau" und „hängt
// mitten in der Session" unterscheiden.
func stummerSMTP(t *testing.T, nachGreeting bool) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				if nachGreeting {
					_, _ = c.Write([]byte("220 stumm.example ESMTP\r\n"))
				}
				// Und danach: nichts. Die Verbindung bleibt offen, es kommt
				// keine Antwort mehr. Geschlossen wird erst am Testende.
				<-time.After(30 * time.Second)
				_ = c.Close()
			}(conn)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func stummeConfig(host string, port int, timeout time.Duration) Config {
	return Config{
		Host: host, Port: port,
		User: "u", Pass: "p", From: "absender@example.com",
		Timeout: timeout,
	}
}

// Der Server nimmt die Verbindung an und schickt nicht einmal das Greeting.
// Ohne Deadline blockiert `smtp.NewClient` beim Lesen der Begrüssung.
func TestSendMail_DeadlineBeimGreeting(t *testing.T) {
	host, port := stummerSMTP(t, false)
	cfg := stummeConfig(host, port, 300*time.Millisecond)

	fertig := make(chan error, 1)
	start := time.Now()
	go func() { fertig <- SendMail(cfg, Message{To: []string{"e@example.com"}, Subject: "x"}) }()

	select {
	case err := <-fertig:
		if err == nil {
			t.Fatal("SendMail meldete Erfolg gegen einen stummen Server")
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("SendMail brauchte %s — die Deadline greift nicht", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendMail hängt gegen einen stummen Server (F-718): " +
			"cfg.Timeout deckt nur den Verbindungsaufbau ab")
	}
}

// Härterer Fall: Der Server begrüsst korrekt und verstummt erst danach —
// `NewClient` kommt durch, `Hello` hängt. Genau das beschreibt F-718 als
// „verstummt NACH dem Connect".
func TestSendMail_DeadlineNachGreeting(t *testing.T) {
	host, port := stummerSMTP(t, true)
	cfg := stummeConfig(host, port, 300*time.Millisecond)

	fertig := make(chan error, 1)
	go func() { fertig <- SendMail(cfg, Message{To: []string{"e@example.com"}, Subject: "x"}) }()

	select {
	case err := <-fertig:
		if err == nil {
			t.Fatal("SendMail meldete Erfolg gegen einen nach dem Greeting stummen Server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendMail hängt, nachdem der Server nach dem Greeting verstummt ist (F-718)")
	}
}

// Der zweite Teil von F-718: Der Aufrufer ist weg (Client hat den Request
// abgebrochen, Server fährt herunter). Die Deadline ist hier bewusst hoch
// gesetzt (30 s), damit die Röte/Grüne allein vom Context kommt.
func TestSendMailContext_AbbruchDurchContext(t *testing.T) {
	host, port := stummerSMTP(t, true)
	cfg := stummeConfig(host, port, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	fertig := make(chan error, 1)
	go func() { fertig <- SendMailContext(ctx, cfg, Message{To: []string{"e@example.com"}, Subject: "x"}) }()

	time.Sleep(150 * time.Millisecond) // Session steht, hängt bei EHLO
	cancel()

	select {
	case err := <-fertig:
		if err == nil {
			t.Fatal("SendMailContext meldete Erfolg trotz abgebrochenem Context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abgebrochener Context beendet den Sendeversuch nicht (F-718)")
	}
}

// Ein bereits abgelaufener Context darf gar nicht erst verbinden.
func TestSendMailContext_BereitsAbgelaufen(t *testing.T) {
	host, port := stummerSMTP(t, true)
	cfg := stummeConfig(host, port, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := SendMailContext(ctx, cfg, Message{To: []string{"e@example.com"}, Subject: "x"})
	dauer := time.Since(start)
	if err == nil {
		t.Fatal("SendMailContext hat mit abgelaufenem Context verbunden")
	}
	/* Die Dauer ist hier der eigentliche Prüfpunkt: Ein Fehler kommt auch
	   ohne Context-Auswertung irgendwann (der stumme Server schliesst nach
	   30 s). Nur die SOFORTIGE Rückkehr belegt, dass ctx gelesen wurde —
	   cfg.Timeout steht in diesem Test bewusst auf 30 s. */
	if dauer > 2*time.Second {
		t.Errorf("SendMailContext brauchte %s trotz abgelaufenem Context — "+
			"der Context wird nicht ausgewertet (F-718)", dauer)
	}
	if !errors.Is(err, context.Canceled) {
		t.Logf("Fehler ist %v (kein context.Canceled) — akzeptabel, solange sofort abgebrochen wurde", err)
	}
}

// TestConnection läuft im Admin-Test-Hook, also im HTTP-Handler. Ein
// verstummter Server darf auch dort nicht unbegrenzt festhalten — sonst
// hängt ausgerechnet die Funktion, die diese Störung diagnostizieren soll.
func TestTestConnection_DeadlineBeimGreeting(t *testing.T) {
	host, port := stummerSMTP(t, false)
	cfg := stummeConfig(host, port, 300*time.Millisecond)

	fertig := make(chan error, 1)
	go func() { fertig <- TestConnection(cfg) }()

	select {
	case err := <-fertig:
		if err == nil {
			t.Fatal("TestConnection meldete Erfolg gegen einen stummen Server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TestConnection hängt gegen einen stummen Server (F-718)")
	}
}
