package api

import "time"

// Test-Zugang für agent_757_test.go (Paket api_test, Karte #757). Nur in
// Tests kompiliert. ⚠️ Ändern globale Variablen ohne Sperre: nie aus einem
// Test mit t.Parallel() aufrufen — heute läuft im Paket api kein Test parallel.

// SetzeLoeschCodeUhrFuerTest ersetzt die Uhr der Bestätigungscodes.
func SetzeLoeschCodeUhrFuerTest(f func() time.Time) (zurueck func()) {
	alt := loeschCodeUhr
	loeschCodeUhr = f
	return func() { loeschCodeUhr = alt }
}

// SetzeVorDemSchreibenFuerTest setzt die Naht werkzeugVorDemSchreiben.
func SetzeVorDemSchreibenFuerTest(f func()) (zurueck func()) {
	alt := werkzeugVorDemSchreiben
	werkzeugVorDemSchreiben = f
	return func() { werkzeugVorDemSchreiben = alt }
}
