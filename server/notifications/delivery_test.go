package notifications

// delivery_test.go — der Hauptschalter für den Versand (Karte #420).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// Der Default ist die eigentliche Zusage dieser Karte: Ohne bewusst gesetzten
// Wert geht nichts raus. Deshalb steht der leere Wert hier nicht als
// Einzelfall, sondern zusammen mit allem, was ein Operator versehentlich
// hineinschreiben könnte.
func TestDeliveryEnabled_DefaultIstAus(t *testing.T) {
	aus := []string{
		"", " ", "off", "0", "false", "no", "nein", "aus",
		"ON;", "true ich meine es ernst", "vielleicht", "enabled",
	}
	for _, w := range aus {
		if DeliveryEnabled(w) {
			t.Errorf("DeliveryEnabled(%q) = true — jeder nicht eindeutige Wert muss AUS bedeuten", w)
		}
	}
}

func TestDeliveryEnabled_NurEindeutigeZustimmung(t *testing.T) {
	an := []string{"1", "on", "ON", "On", "true", "TRUE", "yes", "ja", "an", "  on  "}
	for _, w := range an {
		if !DeliveryEnabled(w) {
			t.Errorf("DeliveryEnabled(%q) = false — dieser Wert ist eindeutige Zustimmung", w)
		}
	}
}

// Der unterdrückte Sender muss ERFOLG melden. Ein Fehler brächte den Worker in
// seinen Retry-Backoff (5s/30s/2min) und liesse das Event am Ende als
// gescheitert im Log stehen — beides falsch: Es ist kein Fehlschlag, sondern
// eine Entscheidung.
func TestSuppressedSender_MeldetErfolgUndSendetNicht(t *testing.T) {
	/* Der Spy FORMATIERT — sonst prüft der Test den Format-String und nicht
	   die Zeile, die der Operator tatsächlich im Log liest. Die erste Fassung
	   dieses Tests tat genau das und suchte nach "an=%s". */
	var zeilen []string
	s := NewSuppressedSender(&zaehlenderSender{kanal: ChannelEmail},
		func(f string, a ...interface{}) { zeilen = append(zeilen, fmt.Sprintf(f, a...)) })

	if s.Channel() != ChannelEmail {
		t.Fatalf("Channel()=%q, erwartet %q — der Worker matcht Sender über den Kanalnamen; "+
			"ein anderer Name hiesse, dass der Kanal im Trockenbetrieb komplett fehlt", s.Channel(), ChannelEmail)
	}
	if err := s.SendOne(context.Background(), "u1", "wohin@example.com", "Wer", Event{Type: EventCardAssigned}); err != nil {
		t.Fatalf("SendOne = %v, erwartet nil", err)
	}
	if len(zeilen) != 1 {
		t.Fatalf("%d Log-Zeilen, erwartet genau 1 — eine unterdrückte Zustellung, die nichts protokolliert, "+
			"ist von einer erfolgten nicht zu unterscheiden", len(zeilen))
	}
	/* Die Empfängeradresse ist im Trockenbetrieb die einzige Möglichkeit
	   nachzusehen, WOHIN es gegangen wäre — genau die Frage, für die der
	   Schalter existiert. */
	if !strings.Contains(zeilen[0], "wohin@example.com") {
		t.Errorf("Log-Zeile nennt die Empfängeradresse nicht: %q", zeilen[0])
	}
	if !strings.Contains(zeilen[0], EventCardAssigned) {
		t.Errorf("Log-Zeile nennt den Ereignistyp nicht: %q", zeilen[0])
	}
	if !strings.Contains(zeilen[0], DeliveryEnvVar) {
		t.Errorf("Log-Zeile nennt %s nicht — wer sie liest, weiss dann nicht, welchen Schalter er umlegen muss: %q",
			DeliveryEnvVar, zeilen[0])
	}
}

// Der innere Sender darf NIE gerufen werden — sonst ginge im Trockenbetrieb
// doch Post raus, und zwar genau dann, wenn niemand damit rechnet.
func TestSuppressedSender_RuftDenEchtenSenderNie(t *testing.T) {
	echt := &zaehlenderSender{kanal: ChannelGotify}
	s := NewSuppressedSender(echt, nil)
	for i := 0; i < 3; i++ {
		if err := s.SendOne(context.Background(), "u", "a@b.de", "N", Event{Type: EventCardMoved}); err != nil {
			t.Fatalf("SendOne: %v", err)
		}
	}
	if echt.aufrufe != 0 {
		t.Fatalf("echter Sender %d× gerufen, erwartet 0 — im Trockenbetrieb darf nichts zugestellt werden", echt.aufrufe)
	}
}

type zaehlenderSender struct {
	kanal   string
	aufrufe int
}

func (z *zaehlenderSender) Channel() string { return z.kanal }
func (z *zaehlenderSender) SendOne(context.Context, string, string, string, Event) error {
	z.aufrufe++
	return nil
}

// ── Der neue Ereignistyp (Karte #420, Phase-1-Nachtrag zu #336) ───────

// EventSubsystemOff muss im Katalog stehen, sonst kann ihn niemand
// abonnieren und der Worker fände keinen Default.
func TestEventSubsystemOff_IstImKatalogUndDefaultAn(t *testing.T) {
	drin := false
	for _, tp := range AllEventTypes {
		if tp == EventSubsystemOff {
			drin = true
		}
	}
	if !drin {
		t.Fatal("EventSubsystemOff fehlt in AllEventTypes")
	}
	for _, ch := range AllChannels {
		if !DefaultSubscribed(EventSubsystemOff, ch) {
			t.Errorf("DefaultSubscribed(%s, %s)=false — „keine Sicherungen mehr\" ist der Fall, "+
				"bei dem ein Default-OFF am teuersten ist", EventSubsystemOff, ch)
		}
	}
	if GotifyPriority(EventSubsystemOff) != GotifyPriority(EventBackupFailed) {
		t.Errorf("GotifyPriority(%s)=%d, erwartet dieselbe Dringlichkeit wie %s (%d)",
			EventSubsystemOff, GotifyPriority(EventSubsystemOff),
			EventBackupFailed, GotifyPriority(EventBackupFailed))
	}
	if HumanLabel(EventSubsystemOff) == EventSubsystemOff {
		t.Error("HumanLabel liefert den rohen Typ-String zurück — der Fallback greift, " +
			"es fehlt ein Fall in HumanLabel")
	}
}

// Der Grund gehört in die Meldung. Ohne ihn weiss der Operator nur, DASS
// etwas aus ist — und das ist der Zustand, in dem er vorher schon war.
func TestBuildSummary_SubsystemOffNenntDenGrund(t *testing.T) {
	mit := buildSummary(Event{Type: EventSubsystemOff, Snippet: "Board-Backup — Schlüssel fehlt"})
	if !strings.Contains(mit, "Board-Backup") || !strings.Contains(mit, "Schlüssel fehlt") {
		t.Errorf("buildSummary = %q — der Grund aus Snippet fehlt", mit)
	}
	ohne := buildSummary(Event{Type: EventSubsystemOff})
	if ohne == "" {
		t.Error("buildSummary ohne Snippet ist leer — auch die grundlose Meldung braucht einen Text")
	}
}

// F-809: Für die DB-Spalte (notification_log.summary) gilt eine Obergrenze —
// NotifLogSummaryMax, NICHT EmailSnippetMax/GotifySnippetMax (die Mail/Gotify-
// Kürzung ist seit F-723 bewusst deaktiviert). Rune-sicher: das Ellipsis-Zeichen
// darf keine Multi-Byte-Rune zerschneiden.
func TestBuildSummary_SubsystemOffKuerztDieLogSpalte(t *testing.T) {
	langerGrund := strings.Repeat("ä", NotifLogSummaryMax+500) // Multi-Byte-Rune
	mit := buildSummary(Event{Type: EventSubsystemOff, Snippet: langerGrund})
	if runeLen := len([]rune(mit)); runeLen > NotifLogSummaryMax {
		t.Errorf("buildSummary lieferte %d Runen, erwartet <= %d (F-809)", runeLen, NotifLogSummaryMax)
	}
	if !strings.HasSuffix(mit, "…") {
		t.Errorf("buildSummary = %q — gekürzter Text ohne Auslassungszeichen", mit)
	}
	if !strings.HasPrefix(mit, "Subsystem aus: ") {
		t.Errorf("buildSummary = %q — Präfix fehlt nach der Kürzung", mit)
	}
}

// Gegenrichtung zu F-723: Mail/Gotify bleiben von NotifLogSummaryMax unberührt
// — die Kürzung betrifft ausschliesslich buildSummary/die Log-Spalte.
func TestEmailBody_SubsystemOffKuerztDenGrundWeiterhinNicht_TrotzLogLimit(t *testing.T) {
	langerGrund := strings.Repeat("x", NotifLogSummaryMax+500)
	_, htmlOut := buildEmailBody(Event{Type: EventSubsystemOff, Snippet: langerGrund}, "", "")
	if !strings.Contains(htmlOut, strings.Repeat("x", NotifLogSummaryMax+500)) {
		t.Error("buildEmailBody kürzt den Grund trotz F-723-Zusage — NotifLogSummaryMax ist auf die Mail durchgesickert")
	}
}

// ── MeldungsDrossel: beide Richtungen (Phase-2b-Verify, [K] P2) ───────
//
// Die erste Fassung dieser Logik war eine `map[string]bool` im Aufrufer OHNE
// Rückweg. Ein Zustands-Flag, das nur gesetzt und nie gelöscht wird, ist die
// Fehlerklasse, für die ADR-0115 den Verify-Lauf eingeführt hat — und sie kam
// ausgerechnet in der Nacharbeit an einem Kritiker-Fund wieder herein.

func TestMeldungsDrossel_MeldetEinmalUndDannNichtMehr(t *testing.T) {
	d := NewMeldungsDrossel()
	if !d.SollMelden("Board-Backup") {
		t.Fatal("erste Störung wird nicht gemeldet — die Drossel drosselt zu früh")
	}
	for i := 0; i < 5; i++ {
		if d.SollMelden("Board-Backup") {
			t.Fatalf("Wiederholung %d wird gemeldet — ein Crash-Restart-Loop erzeugte damit "+
				"eine Admin-Mail pro Neustart", i+2)
		}
	}
}

// Der eigentliche Punkt: Nach der Reparatur muss eine NEUE Störung wieder
// durchkommen. Ohne diesen Rückweg bliebe eine zweite, unabhängige Störung
// bis zum Prozessende stumm — und ein Admin repariert per Reload, ohne
// Neustart.
func TestMeldungsDrossel_NachReparaturWirdWiederGemeldet(t *testing.T) {
	d := NewMeldungsDrossel()
	if !d.SollMelden("Board-Backup") {
		t.Fatal("erste Störung nicht gemeldet")
	}
	if war := d.Laeuft("Board-Backup"); !war {
		t.Error("Laeuft() meldet nicht, dass vorher eine Störung offen war — " +
			"dann fehlt die „läuft wieder\"-Zeile im Log")
	}
	if !d.SollMelden("Board-Backup") {
		t.Fatal("Eine ZWEITE, unabhängige Störung nach der Reparatur wird nicht gemeldet. " +
			"Genau dieser Fall trat ohne Rückweg ein: gemeldet wurde ja schon mal, " +
			"und bis zum nächsten Prozessende bliebe es dabei.")
	}
}

func TestMeldungsDrossel_TrenntSubsysteme(t *testing.T) {
	d := NewMeldungsDrossel()
	d.SollMelden("Board-Backup")
	if !d.SollMelden("Mail-Ingest") {
		t.Fatal("ein zweites Subsystem wird gedrosselt, obwohl es noch nie gemeldet hat")
	}
	d.Laeuft("Board-Backup")
	if d.SollMelden("Mail-Ingest") {
		t.Error("das Zurücknehmen für ein Subsystem hat ein anderes mit zurückgesetzt")
	}
}

// Laeuft() auf einem nie gemeldeten Subsystem ist der Normalfall — es wird bei
// JEDEM erfolgreichen Start gerufen, nicht nur nach einer Störung.
func TestMeldungsDrossel_LaeuftOhneVorherigeStoerung(t *testing.T) {
	d := NewMeldungsDrossel()
	if d.Laeuft("Board-Backup") {
		t.Error("Laeuft() behauptet eine vorherige Störung, die es nicht gab — " +
			"das erzeugte eine „läuft wieder\"-Zeile bei jedem normalen Start")
	}
	if !d.SollMelden("Board-Backup") {
		t.Error("Laeuft() ohne vorherige Störung hat den Zustand verfälscht")
	}
}

// ── Der Grund muss ungekürzt in die Mail (F-723) ──────────────────────
//
// Dieser Test verankert eine Zahl, die vorher nur als Prosa in Kommentar und
// Finding stand — der Phase-2b-Verify konnte sie deshalb nicht reproduzieren
// und meldete sie als unbelegt. Sie stimmt (nachgemessen: 328 Runen für den
// alten Text mit `ErrEncryptionKeyMissing`), aber eine Zahl, die man nur
// glauben kann, ist so gut wie keine. Jetzt misst der Test sie.
func TestEmailBody_SubsystemOffKuerztDenGrundNicht(t *testing.T) {
	/* Ein Grund, der LÄNGER als das Snippet-Limit ist — genau der reale
	   Fall: Der Text zu `encryption_enabled=true ohne Schlüssel` kam in der
	   ersten Fassung auf 328 Runen und wurde auf EmailSnippetMax (200)
	   gekappt, wodurch ausgerechnet „es werden KEINE Sicherungen mehr
	   erstellt" wegfiel. */
	const schluss = "DIESER SATZ IST DER WICHTIGE"
	grund := "Board-Backup — " + strings.Repeat("Vorgeplänkel. ", 20) + schluss
	if utf8.RuneCountInString(grund) <= EmailSnippetMax {
		t.Fatalf("Testaufbau falsch: Der Grund ist mit %d Runen kürzer als das Limit (%d) — "+
			"dann kann der Test die Kürzung gar nicht sehen (der Zweig wird nicht betreten)",
			utf8.RuneCountInString(grund), EmailSnippetMax)
	}

	text, html := buildEmailBody(Event{Type: EventSubsystemOff, Snippet: grund}, "Admin", "")
	if !strings.Contains(text, schluss) {
		t.Errorf("Der Schluss des Grundes fehlt im Mailtext — er wurde gekürzt.\nText:\n%s", text)
	}
	if !strings.Contains(html, schluss) {
		t.Error("Der Schluss des Grundes fehlt in der HTML-Fassung")
	}
	/* Und der Vorgabetext darf NICHT mehr erscheinen: Er verwies auf ein
	   Board, das es bei diesem Ereignis nicht gibt. */
	if strings.Contains(text, "Details siehe Board") {
		t.Error("Der Mailtext verweist weiterhin auf „Details siehe Board" +
			"\" — bei einem Subsystem-Ereignis gibt es kein Board")
	}
}

// Auf Gotify wird bewusst gekürzt (80 Zeichen) — dort zählt die
// Handlungsaufforderung, die Einzelheiten stehen in der Mail. Geprüft wird
// hier nur, dass die Nachricht nicht mit einem Trenner beginnt: `CardTitle`
// ist bei diesem Ereignis leer, und der Vorgabezweig baute daraus ein
// führendes „— ".
func TestGotifyPayload_SubsystemOffOhneFuehrendenTrenner(t *testing.T) {
	title, msg := buildGotifyPayload(Event{Type: EventSubsystemOff, Snippet: "Board-Backup — Grund"})
	if title == "" {
		t.Error("kein Titel")
	}
	if strings.HasPrefix(strings.TrimSpace(msg), "—") {
		t.Errorf("Nachricht beginnt mit einem Trenner: %q", msg)
	}
	if !strings.Contains(msg, "Board-Backup") {
		t.Errorf("Nachricht nennt das Subsystem nicht: %q", msg)
	}
}
