package api

import "time"

// SetzeOpsNowFuerTest stellt die Eingangsuhr des Ops-Endpunkts (Karte #606:
// Lease-Ablauf mit gestellter Uhr, knowledge §38). Rückgabe stellt zurück.
func SetzeOpsNowFuerTest(f func() time.Time) func() {
	alt := opsNow
	opsNow = f
	return func() { opsNow = alt }
}
