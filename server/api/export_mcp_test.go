package api

// Test-Zugang für agent_753_test.go (Paket api_test): die Obergrenze einer
// Werkzeug-Antwort im MCP-Eingang vorübergehend senken (Sitzungs-QA 08.10.,
// K-2). Nur in Tests kompiliert. ⚠️ Ändert eine globale Variable ohne Sperre:
// nie aus einem Test mit t.Parallel() aufrufen (Verify 08.10., W-2) — heute
// läuft im Paket api kein Test parallel.
func SetzeMCPAntwortMaxFuerTest(n int) (zurueck func()) {
	alt := werkzeugMCPAntwortMax
	werkzeugMCPAntwortMax = n
	return func() { werkzeugMCPAntwortMax = alt }
}

// SetzeOWUILogFuerTest ersetzt owuiLog vorübergehend (Verify 08.10., W-1).
// Gleiche Einschränkung: nie mit t.Parallel().
func SetzeOWUILogFuerTest(f func(string, ...any)) (zurueck func()) {
	alt := owuiLog
	owuiLog = f
	return func() { owuiLog = alt }
}
