package api

import (
	"errors"

	"github.com/Andiministrator/aBoard/server/llm"
)

// Karte #665 (ADR-0133 §3): Fehlerarten aus einer 4xx-Antwort des Anbieters.
// Bei einem lokalen Modell sind sie die häufigste Störung und früher alle als
// „nicht erreichbar“ gemeldet worden.
const (
	// KindContextTooLong — Eingabe länger als das Kontextfenster des Modells.
	KindContextTooLong = "context-too-long"
	// KindToolsUnsupported — das Gegenüber nimmt keine Werkzeuge an.
	KindToolsUnsupported = "tools-unsupported"
	// KindProviderRejected — der Anbieter lehnt die Anfrage ab (übrige 4xx).
	KindProviderRejected = "provider-rejected"
)

// anbieterFehlerArt liefert das `kind` für einen Fehler aus classifyHTTPResponse
// oder "" für jeden anderen Fehler.
func anbieterFehlerArt(err error) string {
	switch {
	case errors.Is(err, llm.ErrContextTooLong):
		return KindContextTooLong
	case errors.Is(err, llm.ErrToolsUnsupported):
		return KindToolsUnsupported
	case errors.Is(err, llm.ErrProviderRejected):
		return KindProviderRejected
	}
	return ""
}

// anbieterFehlerMeldung: gekürzte Meldung des Anbieters, geschwärzt wie jede
// Fehlermeldung, die den Server verlässt. Leer → feste Kurzfassung.
func anbieterFehlerMeldung(err error) string {
	if m := redactString(llm.AnbieterMeldung(err)); m != "" {
		return m
	}
	return "provider rejected the request"
}
