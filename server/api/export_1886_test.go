package api

// Test-Zugang für agent_1886_test.go (Paket api_test, F-1886). Nur in Tests
// kompiliert; wie export_757_test.go nie aus einem Test mit t.Parallel().

// ZaehleAnhangPruefungFuerTest zählt, wie oft der Vorfilter und wie oft die
// eigentliche Anhang-Prüfung läuft.
func ZaehleAnhangPruefungFuerTest(vorfilter, pruefung *int) (zurueck func()) {
	altV, altP := anhangVorfilterNaht, anhangPruefungNaht
	anhangVorfilterNaht = func() { *vorfilter++ }
	anhangPruefungNaht = func() { *pruefung++ }
	return func() { anhangVorfilterNaht, anhangPruefungNaht = altV, altP }
}

// OpsBeruehrenAnhaengeFuerTest: der Vorfilter für eine einzelne Op.
func OpsBeruehrenAnhaengeFuerTest(typ, payload string) bool {
	return opsBeruehrenAnhaenge([]serverOp{{Type: typ, Payload: []byte(payload)}})
}
