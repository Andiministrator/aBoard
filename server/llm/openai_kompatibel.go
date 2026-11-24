package llm

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"
)

// Karte #570 (ADR-0129 §5): Ist eine eigene Basis-URL gesetzt, spricht der
// Anbieter „openai“ mit einem OpenAI-KOMPATIBLEN Gegenüber (z. B. einem
// lokalen vLLM), nicht mit OpenAI. Dann gelten die OpenAI-Tabellen nicht mehr:
// Ein vLLM, dessen Modell zufällig `gpt-4o` heisst, hat weder OpenAI-Preise
// noch OpenAI-Kontextgrenzen noch sicher Bildverständnis (K-B1,
// Session-Kritiker Sprint 72). Der Schalter wird von RegisterDefaults gesetzt.
var openAIKompatibel atomic.Bool

// OpenAIKompatibel meldet, ob der Anbieter „openai“ auf eine eigene Basis
// umgestellt ist.
func OpenAIKompatibel() bool { return openAIKompatibel.Load() }

// SetzeOpenAIKompatibel ist für Tests; im Betrieb setzt RegisterDefaults den
// Wert. Liefert den vorherigen zurück.
func SetzeOpenAIKompatibel(an bool) bool { return openAIKompatibel.Swap(an) }

// ValidateOpenAIBaseURL prüft und normalisiert ABOARD_LLM_OPENAI_BASE_URL.
// Leer ist gültig und bedeutet „OpenAI selbst“ (defaultOpenAIBaseURL).
//
// Erlaubt: http/https, ein Host, optional ein Port 1–65535, ein Pfad ohne
// `..`-Segmente; keine Query, kein Fragment, keine Zugangsdaten, kein
// Leerraum (auch kein geschütztes Leerzeichen). Ein abschliessender Slash wird
// entfernt.
//
// Fehlermeldungen nennen den Wert nur OHNE Zugangsdaten, Query und Fragment
// (K-B3): Die Meldung landet über log.Fatalf im Log, und genau dort sollen
// ein versehentlich eingetragenes Passwort oder ein `?api_key=` nicht stehen.
func ValidateOpenAIBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	fehler := func(grund string) error {
		return fmt.Errorf("ABOARD_LLM_OPENAI_BASE_URL=%q: %s", geschwaerzt(raw), grund)
	}
	if strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", fehler("enthält Leerraum")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fehler("keine gültige URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fehler(fmt.Sprintf("Schema %q — erlaubt sind nur http und https", u.Scheme))
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fehler("kein Host")
	}
	if u.User != nil {
		return "", fehler("keine Zugangsdaten in der URL erlaubt (user:pass@)")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fehler("keine Query erlaubt")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fehler("kein Fragment erlaubt")
	}
	if _, portText, err := net.SplitHostPort(u.Host); err == nil {
		port, perr := strconv.Atoi(portText)
		if perr != nil || port < 1 || port > 65535 {
			return "", fehler("Port muss zwischen 1 und 65535 liegen")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", fehler("leerer Port")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." || seg == "." {
			return "", fehler("keine ./..-Segmente im Pfad")
		}
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// geschwaerzt gibt die URL ohne Zugangsdaten, Query und Fragment wieder; ist
// sie nicht lesbar, nur einen Platzhalter — lieber nichts als ein Geheimnis.
func geschwaerzt(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(nicht lesbar)"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u.String()
}

// OpenAIBasisOhnePfad meldet, ob die Basis keinen Pfad hat — ein vLLM erwartet
// fast immer `/v1`; ohne ihn antworten `/models` und `/chat/completions` mit
// 404. Kein Fehler (manche Gegenüber liegen unter der Wurzel), aber eine
// Warnung im Start-Log (K-B6).
func OpenAIBasisOhnePfad(basis string) bool {
	u, err := url.Parse(basis)
	return err == nil && strings.Trim(u.Path, "/") == ""
}

// ParseOpenAIModelle liest ABOARD_LLM_OPENAI_MODELLE: Komma-Liste der
// Modellnamen, die das OpenAI-kompatible Gegenüber bedient (bei vLLM der
// `--served-model-name`). Die Oberfläche bietet dann genau diese an statt der
// OpenAI-Modelle (K-B1). Leer ist gültig.
func ParseOpenAIModelle(raw string) ([]string, error) {
	var modelle []string
	for _, m := range strings.Split(raw, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if strings.IndexFunc(m, unicode.IsSpace) >= 0 || len(m) > 200 {
			return nil, fmt.Errorf("ABOARD_LLM_OPENAI_MODELLE: %q ist kein Modellname (kein Leerraum, höchstens 200 Zeichen)", m)
		}
		modelle = append(modelle, m)
	}
	return modelle, nil
}

// openAIKontext ist die vom Betreiber genannte Kontextgrenze (Token) des
// OpenAI-kompatiblen Gegenübers; 0 = unbekannt, kein Budget-Tor. Wirkt nur bei
// eigener Basis (ContextLimit fragt OpenAIKompatibel() zuerst).
var openAIKontext atomic.Int64

// openAIWerkzeugeAus: ABOARD_LLM_OPENAI_WERKZEUGE=aus.
var openAIWerkzeugeAus atomic.Bool

// OpenAIWerkzeugeAus meldet, ob der Anbieter „openai“ keine Werkzeuge
// (tools/tool_choice) mitschicken soll.
func OpenAIWerkzeugeAus() bool { return openAIWerkzeugeAus.Load() }

// SetzeOpenAIWerkzeugeAus ist für main und Tests. Liefert den vorherigen Wert.
func SetzeOpenAIWerkzeugeAus(aus bool) bool { return openAIWerkzeugeAus.Swap(aus) }

// SetzeOpenAIKontext ist für main und Tests. Liefert den vorherigen Wert.
func SetzeOpenAIKontext(token int) int { return int(openAIKontext.Swap(int64(token))) }

// Grenzen für ABOARD_LLM_OPENAI_KONTEXT: darunter ist das Budget-Tor sinnlos
// (der System-Prompt allein sprengt es), darüber ein Tippfehler.
const (
	minOpenAIKontext = 512
	maxOpenAIKontext = 10_000_000
)

// ParseOpenAIKontext liest ABOARD_LLM_OPENAI_KONTEXT (Karte #665, ADR-0133 §3):
// die Kontextgrenze des Gegenübers in Token. Leer = 0 = kein Budget-Tor. Ein
// Wert ohne eigene Basis-URL ist ein Fehler — gegen echtes OpenAI gelten die
// Tabellen in context_limits.go, und ein stiller Vorrang wäre irreführend.
func ParseOpenAIKontext(raw, basisURL string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < minOpenAIKontext || n > maxOpenAIKontext {
		return 0, fmt.Errorf("ABOARD_LLM_OPENAI_KONTEXT=%q: erwartet wird eine Zahl von %d bis %d (Token)", raw, minOpenAIKontext, maxOpenAIKontext)
	}
	if basisURL == "" {
		return 0, errors.New("ABOARD_LLM_OPENAI_KONTEXT ist gesetzt, aber ABOARD_LLM_OPENAI_BASE_URL nicht — die Kontextgrenze gilt nur für ein eigenes Gegenüber")
	}
	return n, nil
}

// ParseOpenAIWerkzeuge liest ABOARD_LLM_OPENAI_WERKZEUGE: leer oder „an“ =
// Werkzeuge wie bisher, „aus“ = keine. Alles andere hält den Start an.
func ParseOpenAIWerkzeuge(raw string) (aus bool, err error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "an":
		return false, nil
	case "aus":
		return true, nil
	}
	return false, fmt.Errorf("ABOARD_LLM_OPENAI_WERKZEUGE=%q: erlaubt sind „an“ und „aus“", raw)
}
