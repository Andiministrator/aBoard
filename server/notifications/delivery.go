package notifications

// delivery.go — der Hauptschalter für den tatsächlichen Versand (Karte #420).
//
// Entscheid des Maintainers vom 2026-08-19: Die fünf card.*-Events werden vollständig
// verdrahtet und getestet, der Versand bleibt aber hinter einem Schalter, den
// er bewusst einlegt. Grund: kein Risiko, dass beim Entwickeln oder in einem
// Testlauf Post an echte Empfänger geht.
//
// ZWEI Auflagen aus dieser Entscheidung prägen den Entwurf:
//
//  1. Der Schalter sitzt dort, wo GESENDET wird — nicht dort, wo ausgelöst
//     wird. Ein Frontend-seitiges „nicht emittieren" schützt nur vor genau
//     dem einen Client, der die Bremse kennt; Mail-Ingest, Backup-Scheduler
//     und jeder künftige Server-Emitter gingen daran vorbei.
//  2. Dass er aus ist, darf nicht still passieren. Sonst sucht später jemand
//     stundenlang den Fehler in der Verdrahtung, während die Verdrahtung in
//     Ordnung ist. Deshalb: eine Zeile beim Serverstart, eine Zeile pro
//     unterdrückter Zustellung, und ein Feld in der API, aus dem die
//     Einstellungs-UI einen Hinweis baut.
//
// Warum unterdrückte Events trotzdem den ganzen Weg gehen — Bus, Worker,
// Empfänger-Auflösung, Coalescence, Log — und erst im letzten Zoll gestoppt
// werden: Genau dieser Weg ist das, was hier getestet werden soll. Ein
// Schalter weiter vorne würde die Kette stilllegen, die er absichern soll,
// und der erste Lauf mit eingeschaltetem Versand wäre der erste echte Test.

import (
	"context"
	"strings"
)

// DeliveryEnvVar ist der Name der Umgebungsvariable, die den Versand freigibt.
const DeliveryEnvVar = "ABOARD_NOTIFICATIONS_DELIVERY"

// DeliveryEnabled entscheidet, ob wirklich gesendet werden darf.
//
// Default ist AUS: Ein leerer, fehlender oder unverständlicher Wert bedeutet
// „nicht senden". Das ist die Richtung, in der ein Irrtum billig ist — eine
// nicht zugestellte Benachrichtigung ist ärgerlich, eine versehentlich an
// echte Empfänger zugestellte nicht zurückholbar.
//
// Rein und ohne Env-Zugriff, damit beide Konsumenten (Worker-Verdrahtung in
// main.go und die Einstellungs-API) dieselbe Funktion über denselben Wert
// aufrufen und keine zweite Wahrheit entsteht.
func DeliveryEnabled(wert string) bool {
	switch strings.ToLower(strings.TrimSpace(wert)) {
	case "1", "on", "true", "yes", "ja", "an":
		return true
	default:
		return false
	}
}

// suppressedSender ersetzt einen echten Sender, solange der Versand aus ist.
// Er meldet Erfolg — der Worker soll den Fall NICHT als Fehlschlag behandeln
// und in seinen Retry-Backoff gehen (5s/30s/2min), denn nichts davon würde
// besser: es ist kein Fehler, sondern eine Entscheidung.
type suppressedSender struct {
	channel string
	logger  func(string, ...interface{})
}

// NewSuppressedSender verpackt einen Kanal in einen Sender, der protokolliert
// statt zuzustellen. `inner` wird nur nach seinem Kanalnamen gefragt und nie
// aufgerufen — der Aufrufer soll den echten Sender trotzdem übergeben, damit
// die Kanalliste des Workers dieselbe bleibt wie im eingeschalteten Zustand.
func NewSuppressedSender(inner Sender, logger func(string, ...interface{})) Sender {
	if logger == nil {
		logger = func(string, ...interface{}) {}
	}
	return &suppressedSender{channel: inner.Channel(), logger: logger}
}

// BaueSender ist die Verdrahtungs-Entscheidung als PRÜFBARE Funktion
// (Sprint-59-Kritiker [V] P2).
//
// Vorher stand diese Logik als Schleife in `main.go` und wurde von einem
// Quelltext-Wächter bewacht, der drei Zeichenketten suchte. Der Kritiker hat
// ihn ausgehebelt, indem er `notifSenders = wrapped` durch `_ = wrapped`
// ersetzte: Der Worker bekam die rohen Sender — also genau der Zustand, den
// der Wächter verhindern sollte — und der Test blieb grün. Ein Wächter, der
// drei Zeichenketten prüft, prüft drei Zeichenketten und nicht die Zusage.
//
// Als Funktion ist die Zusage direkt prüfbar: „bei ausgeschaltetem Schalter
// enthält das Ergebnis keinen einzigen echten Sender." Das ist eine
// Behauptung über Werte, nicht über Quelltext.
func BaueSender(deliveryEnabled bool, echte []Sender, logger func(string, ...interface{})) []Sender {
	if deliveryEnabled {
		return echte
	}
	out := make([]Sender, 0, len(echte))
	for _, s := range echte {
		if s == nil {
			continue
		}
		out = append(out, NewSuppressedSender(s, logger))
	}
	return out
}

func (s *suppressedSender) Channel() string { return s.channel }

func (s *suppressedSender) SendOne(_ context.Context, recipientUserID, recipientEmail, _ string, ev Event) error {
	// Die Empfängeradresse gehört in diese Zeile: Sie ist im Trockenbetrieb
	// die einzige Möglichkeit nachzusehen, WOHIN es gegangen wäre — genau die
	// Frage, für die der Schalter existiert.
	s.logger("notifications: VERSAND AUS (%s=off) — unterdrückt: kanal=%s typ=%s an=%s user=%s",
		DeliveryEnvVar, s.channel, ev.Type, recipientEmail, recipientUserID)
	return nil
}

// MeldungsDrossel verhindert, dass eine DAUERHAFTE Störung eine
// EREIGNISFÖRMIGE Meldung wiederholt — und hat dafür einen Rückweg.
//
// Der Anlass: `system.subsystem-off` wird beim Serverstart gefeuert, wenn ein
// Subsystem wegen Fehlkonfiguration nicht anläuft. Derselbe Code läuft bei
// jedem Neustart und bei jedem Admin-Reload; ohne Drossel erzeugte ein
// Crash-Restart-Loop eine Admin-Mail pro Neustart, bis die 30/h-Grenze greift
// und den Rest in die Tagesübersicht schiebt — aus „gar keine Meldung" würde
// „so viele, dass sie ignoriert werden".
//
// WARUM ALS EIGENER TYP UND NICHT ALS `map[string]bool` IM AUFRUFER: Die
// erste Fassung war genau das — und liess den Rückweg weg. Ein Admin, der den
// Schlüssel nachträgt, repariert den Zustand ohne Neustart (`RegisterReload`
// läuft im selben Prozess); eine zweite, unabhängige Störung danach wäre bis
// zum Prozessende stumm geblieben, weil ja „schon gemeldet" war. Ein
// Zustands-Flag ohne Rückweg ist die Fehlerklasse, für die ADR-0115 den
// Verify-Lauf eingeführt hat — hier in der Nacharbeit an einem Kritiker-Fund
// wieder eingebaut und vom Verify gefunden.
//
// Als Typ sind beide Richtungen Teil des Vertrags und beide geprüft.
// Nicht threadsafe: Der Aufrufer ist der Serverstart bzw. ein Admin-Reload,
// beide serialisiert.
type MeldungsDrossel struct {
	gemeldet map[string]bool
}

// NewMeldungsDrossel legt eine leere Drossel an.
func NewMeldungsDrossel() *MeldungsDrossel {
	return &MeldungsDrossel{gemeldet: map[string]bool{}}
}

// SollMelden meldet, ob für `subsystem` eine Benachrichtigung rausgehen soll,
// und merkt sich die Ausgabe. Der zweite Aufruf für dasselbe Subsystem
// liefert false — bis Laeuft() gerufen wurde.
func (d *MeldungsDrossel) SollMelden(subsystem string) bool {
	if d == nil {
		return true
	}
	if d.gemeldet[subsystem] {
		return false
	}
	d.gemeldet[subsystem] = true
	return true
}

// Laeuft nimmt die Drossel für `subsystem` zurück — zu rufen, sobald es
// wieder anläuft. Liefert true, wenn vorher tatsächlich eine Störung gemeldet
// war (für die „läuft wieder"-Log-Zeile).
func (d *MeldungsDrossel) Laeuft(subsystem string) bool {
	if d == nil {
		return false
	}
	war := d.gemeldet[subsystem]
	delete(d.gemeldet, subsystem)
	return war
}
