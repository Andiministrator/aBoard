package mail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/ausgang/ausgangtest"
)

// Karte #569: Bei ABOARD_AUSGAENGE=aus bauen SMTP-Versand, SMTP-Test und
// IMAP keine Verbindung auf. Gegenprobe im selben Test: Steht das Ziel auf
// der Liste, kommt am Zähler eine Verbindung an — der Test kann sie also sehen.

func smtpCfg(z *ausgangtest.Zaehler) Config {
	return Config{Host: "127.0.0.1", Port: z.Port(), User: "u", Pass: "p", From: "a@b.c", Timeout: 2 * time.Second}
}

func TestAusgang_SMTPVersand(t *testing.T) {
	z := ausgangtest.NeuerZaehler(t)
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))
	msg := Message{To: []string{"x@y.z"}, Subject: "s", BodyText: "b"}
	err := SendMailContext(context.Background(), smtpCfg(z), msg)
	if !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if n := z.Anzahl(); n != 0 {
		t.Fatalf("%d Verbindung(en) trotz aus", n)
	}
	ausgang.Setze(ausgang.Neu(true, z.Adresse()))
	_ = SendMailContext(context.Background(), smtpCfg(z), msg)
	warteAuf(t, z, 1)
}

func TestAusgang_SMTPTest(t *testing.T) {
	z := ausgangtest.NeuerZaehler(t)
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))
	if err := TestConnection(smtpCfg(z)); !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if n := z.Anzahl(); n != 0 {
		t.Fatalf("%d Verbindung(en) trotz aus", n)
	}
	ausgang.Setze(ausgang.Neu(false))
	_ = TestConnection(smtpCfg(z))
	warteAuf(t, z, 1)
}

func TestAusgang_IMAP(t *testing.T) {
	z := ausgangtest.NeuerZaehler(t)
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))
	cfg := IMAPConfig{Host: "127.0.0.1", Port: z.Port(), User: "u", Pass: "p", DialTimeout: 2 * time.Second}
	if _, err := dialMailbox(cfg); !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if n := z.Anzahl(); n != 0 {
		t.Fatalf("%d Verbindung(en) trotz aus", n)
	}
	ausgang.Setze(ausgang.Neu(true, "127.0.0.1"))
	_, _ = dialMailbox(cfg)
	warteAuf(t, z, 1)
}

// Der Admin-Test „IMAP“ (main.go RegisterTest) geht über TestIMAPConnection.
func TestAusgang_IMAPAdminTest(t *testing.T) {
	z := ausgangtest.NeuerZaehler(t)
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))
	cfg := IMAPConfig{Host: "127.0.0.1", Port: z.Port(), User: "u", Pass: "p", Folder: "INBOX", Enabled: true, DialTimeout: 2 * time.Second}
	if err := TestIMAPConnection(cfg); !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if n := z.Anzahl(); n != 0 {
		t.Fatalf("%d Verbindung(en) trotz aus", n)
	}
	ausgang.Setze(ausgang.Neu(false))
	_ = TestIMAPConnection(cfg)
	warteAuf(t, z, 1)
}

func warteAuf(t *testing.T, z *ausgangtest.Zaehler, min int64) {
	t.Helper()
	for i := 0; i < 100 && z.Anzahl() < min; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if z.Anzahl() < min {
		t.Fatalf("Gegenprobe: keine Verbindung angekommen — der Test sähe einen Dial gar nicht")
	}
}
