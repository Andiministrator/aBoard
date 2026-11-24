package llm

// firmenmodus.go — LLM-Firmenmodus (Karte #664, ADR-0133 §2 + Nachtrag
// 2026-10-04 abends).
//
// Ein Schalter statt mehrerer: Setzt der Betreiber
// ABOARD_LLM_SERVERSCHLUESSEL_FILE, gibt es genau einen Anbieter („openai“,
// ggf. mit eigener Basis-URL), einen Schlüssel für alle (aus der Datei), keine
// Nutzerschlüssel und nur die Modelle aus ABOARD_LLM_OPENAI_MODELLE.
//
// Der Schlüssel steht bewusst in einer DATEI und nicht im Variablenwert, damit
// er in keiner Prozessliste auftaucht. Aus demselben Grund nennt keine
// Fehlermeldung hier den Inhalt der Datei — nur ihren Pfad.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// FirmenmodusAnbieter ist der einzige Anbieter, der im Firmenmodus
// registriert wird (ADR-0133 §2).
const FirmenmodusAnbieter = "openai"

// ServerschluesselEnvVar nennt die Variable mit dem PFAD zur Schlüsseldatei.
const ServerschluesselEnvVar = "ABOARD_LLM_SERVERSCHLUESSEL_FILE"

// PIIEnvVar nennt die Betreiber-Vorgabe für den PII-Filter.
const PIIEnvVar = "ABOARD_LLM_PII"

// maxServerschluesselBytes begrenzt die gelesene Datei. Echte Schlüssel sind
// weit unter 1 KB; eine riesige Datei ist eine Verwechslung (falscher Pfad).
const maxServerschluesselBytes = 8 * 1024

// Firmenmodus hält Schlüssel und erlaubte Modelle. Ein nil-Zeiger bedeutet
// „kein Firmenmodus“ — alle Methoden sind nil-sicher, damit Aufrufer ohne
// Sonderfall `f.Aktiv()` fragen können.
//
// Die Felder sind unexportiert, und String/GoString schwärzen: Ein
// versehentliches log.Printf("%v", f) oder json.Marshal(f) gibt den
// Schlüssel nicht preis.
type Firmenmodus struct {
	schluessel string
	modelle    []string
	erlaubt    map[string]bool
}

// NeuerFirmenmodus baut den Firmenmodus aus Schlüssel und Modellliste.
// Leerer Schlüssel oder leere Modellliste sind Fehler: ohne Modellliste wäre
// jedes Modell erlaubt (Nachtrag A1/A2 — die Allowlist ist der zweite Riegel).
func NeuerFirmenmodus(schluessel string, modelle []string) (*Firmenmodus, error) {
	if strings.TrimSpace(schluessel) == "" {
		return nil, errors.New("Firmenmodus: leerer Serverschlüssel")
	}
	if len(modelle) == 0 {
		return nil, fmt.Errorf("%s ist gesetzt (Firmenmodus), aber ABOARD_LLM_OPENAI_MODELLE ist leer — ohne Modellliste wäre jedes Modell erlaubt; die freigegebenen Modelle eintragen", ServerschluesselEnvVar)
	}
	f := &Firmenmodus{schluessel: schluessel, erlaubt: make(map[string]bool, len(modelle))}
	for _, m := range modelle {
		if !f.erlaubt[m] {
			f.erlaubt[m] = true
			f.modelle = append(f.modelle, m)
		}
	}
	return f, nil
}

// FirmenmodusAusDatei liest den Serverschlüssel aus pfad und baut den
// Firmenmodus. Leerer pfad (auch nur Leerraum) = kein Firmenmodus (nil, nil).
//
// Startabbruch-Gründe: Datei nicht lesbar, zu gross, leer, Schlüssel mit
// Leerraum oder Steuerzeichen im Inneren (mehrzeilige Datei, falsche Datei),
// Modellliste leer. Keine Meldung enthält den Dateiinhalt.
func FirmenmodusAusDatei(pfad string, modelle []string) (*Firmenmodus, error) {
	pfad = strings.TrimSpace(pfad)
	if pfad == "" {
		return nil, nil
	}
	schluessel, err := leseServerschluessel(pfad)
	if err != nil {
		return nil, err
	}
	return NeuerFirmenmodus(schluessel, modelle)
}

func leseServerschluessel(pfad string) (string, error) {
	fehler := func(grund string) error {
		return fmt.Errorf("%s=%q: %s", ServerschluesselEnvVar, pfad, grund)
	}
	info, err := os.Stat(pfad)
	if err != nil {
		return "", fehler("Datei nicht lesbar (" + grundOhneInhalt(err) + ")")
	}
	if info.IsDir() {
		return "", fehler("ist ein Verzeichnis, keine Datei")
	}
	if info.Size() > maxServerschluesselBytes {
		return "", fehler(fmt.Sprintf("Datei zu gross (%d B, erlaubt höchstens %d B) — falscher Pfad?", info.Size(), maxServerschluesselBytes))
	}
	roh, err := os.ReadFile(pfad)
	if err != nil {
		return "", fehler("Datei nicht lesbar (" + grundOhneInhalt(err) + ")")
	}
	schluessel := strings.TrimSpace(string(roh))
	if schluessel == "" {
		return "", fehler("Datei ist leer")
	}
	if strings.IndexFunc(schluessel, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fehler("Schlüssel enthält Leerraum oder Steuerzeichen (mehrzeilige Datei?) — erwartet ist genau ein Schlüssel")
	}
	return schluessel, nil
}

// ServerschluesselRechteWarnung liefert einen Warnsatz, wenn die
// Schlüsseldatei gröbere Rechte hat als 0640 — also für Andere lesbar ist oder
// für die Gruppe beschreib-/ausführbar (Kritiker K2-8, Sprint 82). Sonst "",
// auch wenn pfad leer oder nicht lesbar ist (das meldet FirmenmodusAusDatei).
// Nur eine Warnung, kein Startabbruch: Ob die Gruppe des Containers den
// Schlüssel lesen darf, ist eine Entscheidung des Betreibers; dass ihn jedes
// Konto auf dem Rechner lesen kann, sollte er aber wissen.
func ServerschluesselRechteWarnung(pfad string) string {
	return DateiRechteWarnung(ServerschluesselEnvVar, pfad, "der Serverschlüssel")
}

// DateiRechteWarnung ist die Prüfung hinter ServerschluesselRechteWarnung für
// jede Geheimnis-Datei, deren Pfad in der Variable envName steht (Kritiker
// Sprint 82 Runde 2, KO-2: auch ABOARD_OWUI_JWT_SECRET_FILE). was benennt
// den Inhalt im Warnsatz („der Serverschlüssel“). "" bei leerem, nicht
// lesbarem Pfad, bei einem Verzeichnis und bei Rechten bis 0640.
func DateiRechteWarnung(envName, pfad, was string) string {
	pfad = strings.TrimSpace(pfad)
	if pfad == "" {
		return ""
	}
	info, err := os.Stat(pfad)
	if err != nil || info.IsDir() {
		return ""
	}
	if rechte := info.Mode().Perm(); rechte&0o037 != 0 {
		return fmt.Sprintf("WARNUNG: %s=%q hat die Rechte %04o — offener als 0640, %s ist damit für weitere Konten lesbar; chmod 600 (oder 640 mit passender Gruppe) setzen",
			envName, pfad, rechte, was)
	}
	return ""
}

// grundOhneInhalt liefert nur die Fehlerart (z. B. „permission denied“),
// ohne den Pfad doppelt zu nennen. Inhalt kann ein Stat-/Lesefehler ohnehin
// nicht tragen; der Pfad steht schon in der Meldung.
func grundOhneInhalt(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return "unbekannter Fehler"
}

// Aktiv meldet, ob der Firmenmodus gilt.
func (f *Firmenmodus) Aktiv() bool { return f != nil }

// Schluessel liefert den Serverschlüssel ("" ohne Firmenmodus).
func (f *Firmenmodus) Schluessel() string {
	if f == nil {
		return ""
	}
	return f.schluessel
}

// Modelle liefert die erlaubten Modelle (Kopie; nil ohne Firmenmodus).
func (f *Firmenmodus) Modelle() []string {
	if f == nil {
		return nil
	}
	return append([]string(nil), f.modelle...)
}

// ModellErlaubt prüft gegen ABOARD_LLM_OPENAI_MODELLE (Nachtrag A1/A2). Ohne
// Firmenmodus gibt es keine Allowlist — dann true (heutiges Verhalten).
func (f *Firmenmodus) ModellErlaubt(modell string) bool {
	if f == nil {
		return true
	}
	return f.erlaubt[modell]
}

// String schwärzt den Schlüssel.
func (f Firmenmodus) String() string {
	return fmt.Sprintf("Firmenmodus{Schlüssel: geschwärzt, Modelle: %v}", f.modelle)
}

// GoString schwärzt den Schlüssel auch bei %#v.
func (f Firmenmodus) GoString() string { return f.String() }

// PIIVorgabe ist die Betreiber-Vorgabe für den PII-Filter (ADR-0133 §2).
type PIIVorgabe string

const (
	// PIIAus — Filter aus, Nutzer kann ihn nicht einschalten.
	PIIAus PIIVorgabe = "aus"
	// PIIPflicht — Filter immer an, Nutzer kann ihn nicht ausschalten.
	PIIPflicht PIIVorgabe = "pflicht"
	// PIINutzer — heutiges Verhalten: die Nutzerwahl gilt.
	PIINutzer PIIVorgabe = "nutzer"
)

// ParsePIIVorgabe liest ABOARD_LLM_PII. Leer → Vorgabe nach ADR-0133 §2:
// ohne Firmenmodus „nutzer“, im Firmenmodus mit eigener Basis-URL „aus“
// (lokales Modell), ohne Basis-URL „pflicht“. Ungültig → Fehler
// (Startabbruch).
func ParsePIIVorgabe(raw string, firmenmodus bool, openAIBaseURL string) (PIIVorgabe, error) {
	switch v := PIIVorgabe(strings.ToLower(strings.TrimSpace(raw))); v {
	case PIIAus, PIIPflicht, PIINutzer:
		return v, nil
	case "":
		switch {
		case !firmenmodus:
			return PIINutzer, nil
		case openAIBaseURL != "":
			return PIIAus, nil
		default:
			return PIIPflicht, nil
		}
	default:
		return "", fmt.Errorf("%s=%q: erlaubt sind aus, pflicht, nutzer", PIIEnvVar, raw)
	}
}

// PIIOhneFirmenmodusWarnung (Kritiker Sprint 82 Runde 2, KO-3; ADR-0133
// Nachtrag Sprint-82-Abschluss Runde 2): Ein ausdrücklich gesetztes
// ABOARD_LLM_PII gilt auch ohne Firmenmodus. Bei „aus“ heißt das: Jede Person
// sendet mit ihrem eigenen Schlüssel ungefiltert an externe Anbieter und kann
// den Filter im Schlüsseldialog nicht einschalten. Das ist eine bewusste
// Betreiberentscheidung, darf aber nicht still bleiben — der Start schreibt
// diesen Satz ins Log. "" in jedem anderen Fall.
func PIIOhneFirmenmodusWarnung(v PIIVorgabe, firmenmodus bool) string {
	if firmenmodus || v != PIIAus {
		return ""
	}
	return fmt.Sprintf("WARNUNG: %s=aus ohne Firmenmodus (%s nicht gesetzt) — Personen senden mit ihren eigenen Schlüsseln ungefiltert an externe Anbieter (Anthropic, OpenAI, Google, Mistral) und können den PII-Filter nicht einschalten. Gewollt? Sonst %s leer lassen oder auf nutzer setzen",
		PIIEnvVar, ServerschluesselEnvVar, PIIEnvVar)
}

// Wirksam fasst die Vorgabe mit der Meldung des Clients zusammen: Ist der
// PII-Filter für diesen Send aktiv? Bei „pflicht“ immer, bei „aus“ nie,
// sonst zählt die Client-Meldung (heutiges Verhalten). Der leere Wert
// (Aufrufer ohne Konfiguration, z. B. Tests) gilt wie „nutzer“.
func (v PIIVorgabe) Wirksam(clientMeldetAktiv bool) bool {
	switch v {
	case PIIPflicht:
		return true
	case PIIAus:
		return false
	default:
		return clientMeldetAktiv
	}
}

// RegisterFirmenmodus registriert NUR den Anbieter „openai“ (ADR-0133 §2) —
// Anthropic, Google und Mistral gibt es im Firmenmodus nicht. Dieselben
// Client-Einstellungen wie RegisterDefaults (Ausgangs-Schalter, Fristen).
func RegisterFirmenmodus(r *Registry, userAgent string, openAIBaseURL string) {
	client := defaultClient()
	openAIKompatibel.Store(openAIBaseURL != "")
	r.Register(&OpenAIProvider{Client: client, UA: userAgent, BaseURL: openAIBaseURL})
}
