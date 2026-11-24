package notifications

// email_context_test.go — F-810: Der Context erreichte den SMTP-Versand nicht.
//
// `sendWithRetry` baut pro Versuch ein `context.WithTimeout(ctx, SendTimeout)`
// und übergibt es an `Sender.SendOne`. `EmailSender.SendOne` reichte es bis
// Sprint 61 nirgends weiter: `sendFunc` hatte die Signatur
// `func(mailpkg.Config, mailpkg.Message) error`, der Default war
// `mailpkg.SendMail` — und das setzt intern `context.Background()`. Damit band
// der `SendTimeout` den E-Mail-Kanal nicht, obwohl die Paket-Doku des Workers
// ausdrücklich mit ihm rechnet.
//
// Geprüft wird beides: dass `SendOne`/`SendDigest` ihren Context an `sendFunc`
// durchreichen (Signatur-Ebene) UND dass die DEFAULT-Verdrahtung ohne
// injizierte `sendFunc` context-fähig ist (sonst wäre der Fix ein Test-Artefakt).

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	mailpkg "github.com/Andiministrator/aBoard/server/mail"
)

// festeSMTPQuelle liefert eine gültige, aber nie erreichbare SMTP-Config.
type festeSMTPQuelle struct{ cfg mailpkg.Config }

func (f festeSMTPQuelle) LoadSMTP(context.Context) (mailpkg.Config, error) { return f.cfg, nil }

func gueltigeSMTPConfig(host string, port int) mailpkg.Config {
	return mailpkg.Config{
		Host: host, Port: port,
		User: "u", Pass: "p", From: "absender@example.com",
		Timeout: 30 * time.Second, // bewusst hoch: die Röte darf nur vom Context kommen
	}
}

// stummerSMTPListener nimmt Verbindungen an, schickt das Greeting und
// schweigt danach — der Fall, in dem allein der Context noch abbricht.
func stummerSMTPListener(t *testing.T) (host string, port int) {
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
				_, _ = c.Write([]byte("220 stumm.example ESMTP\r\n"))
				<-time.After(60 * time.Second)
				_ = c.Close()
			}(conn)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// Signatur-Ebene: Der Context, den SendOne bekommt, muss bei sendFunc ankommen.
func TestEmailSender_SendOne_ReichtContextDurch(t *testing.T) {
	var gesehen context.Context
	s := NewEmailSender(
		festeSMTPQuelle{cfg: gueltigeSMTPConfig("127.0.0.1", 25)},
		"",
		func(ctx context.Context, _ mailpkg.Config, _ mailpkg.Message) error {
			gesehen = ctx
			return nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	if err := s.SendOne(ctx, "u1", "e@example.com", "Empfänger", Event{Type: EventCardMentioned}); err != nil {
		t.Fatalf("SendOne: %v", err)
	}
	if gesehen == nil {
		t.Fatal("sendFunc bekam keinen Context (F-810)")
	}
	cancel()
	select {
	case <-gesehen.Done():
	case <-time.After(time.Second):
		t.Fatal("der an sendFunc gereichte Context reagiert nicht auf das Cancel " +
			"des SendOne-Contexts — es ist ein anderer (F-810)")
	}
}

// Dasselbe für den Digest-Pfad, der dieselbe sendFunc benutzt.
func TestEmailSender_SendDigest_ReichtContextDurch(t *testing.T) {
	var gesehen context.Context
	s := NewEmailSender(
		festeSMTPQuelle{cfg: gueltigeSMTPConfig("127.0.0.1", 25)},
		"",
		func(ctx context.Context, _ mailpkg.Config, _ mailpkg.Message) error {
			gesehen = ctx
			return nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	if err := s.SendDigest(ctx, "e@example.com", "Empfänger",
		[]digestItem{{EventType: EventCardMentioned, Summary: "x", TS: time.Now()}}, time.Now()); err != nil {
		t.Fatalf("SendDigest: %v", err)
	}
	if gesehen == nil {
		t.Fatal("sendFunc (Digest) bekam keinen Context (F-810)")
	}
	cancel()
	select {
	case <-gesehen.Done():
	case <-time.After(time.Second):
		t.Fatal("der an sendFunc gereichte Digest-Context reagiert nicht auf das Cancel (F-810)")
	}
}

/*
Der eigentliche Prüfpunkt: die DEFAULT-Verdrahtung (sendFunc == nil), also

	genau das, was main.go baut. Ohne injizierte Test-Funktion muss ein
	Context-Deadline den Sendeversuch beenden.

	`cfg.Timeout` steht bewusst auf 30 s — käme die Röte von der Idle-Frist
	(F-718), würde der Test 30 s brauchen statt 400 ms. Nur die schnelle
	Rückkehr belegt, dass der Context ausgewertet wird.
*/
func TestEmailSender_DefaultSendFunc_IstContextFaehig(t *testing.T) {
	host, port := stummerSMTPListener(t)
	s := NewEmailSender(festeSMTPQuelle{cfg: gueltigeSMTPConfig(host, port)}, "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := s.SendOne(ctx, "u1", "e@example.com", "Empfänger", Event{Type: EventCardMentioned})
	dauer := time.Since(start)

	if err == nil {
		t.Fatal("SendOne meldete Erfolg gegen einen stummen SMTP-Server")
	}
	if dauer > 5*time.Second {
		t.Fatalf("SendOne brauchte %s trotz 400-ms-Context — der Context erreicht "+
			"den SMTP-Versand nicht (F-810); Fehler war: %v", dauer, err)
	}
}

/*
Und eine Etage höher, weil dort die Zusage steht: Der Worker baut in

	`sendWithRetry` ein `context.WithTimeout(ctx, SendTimeout)`. Diese Deadline
	muss beim Sender ankommen — sonst ist die „~215 s worst case"-Rechnung der
	Paket-Doku für den E-Mail-Kanal unbelegt.
*/
func TestEmailSender_SendTimeoutDeadlineKommtAn(t *testing.T) {
	var frist time.Time
	var hatFrist bool
	s := NewEmailSender(
		festeSMTPQuelle{cfg: gueltigeSMTPConfig("127.0.0.1", 25)},
		"",
		func(ctx context.Context, _ mailpkg.Config, _ mailpkg.Message) error {
			frist, hatFrist = ctx.Deadline()
			return errors.New("absichtlich fehlgeschlagen")
		},
	)

	sendCtx, cancel := context.WithTimeout(context.Background(), SendTimeout)
	defer cancel()
	_ = s.SendOne(sendCtx, "u1", "e@example.com", "Empfänger", Event{Type: EventCardMentioned})

	if !hatFrist {
		t.Fatal("der bei sendFunc angekommene Context hat keine Deadline — " +
			"SendTimeout bindet den E-Mail-Kanal nicht (F-810)")
	}
	if rest := time.Until(frist); rest > SendTimeout+time.Second || rest < SendTimeout-5*time.Second {
		t.Errorf("Deadline liegt %s in der Zukunft, erwartet ~%s", rest, SendTimeout)
	}
}
