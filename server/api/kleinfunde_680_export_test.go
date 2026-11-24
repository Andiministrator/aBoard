package api

// SetzeOpsProtokollNahtFuerTest erzwingt ein Fold-Protokoll (F-1556,
// Karte #680). Rückgabe stellt zurück.
func SetzeOpsProtokollNahtFuerTest(f func(string) string) func() {
	alt := opsProtokollNaht
	opsProtokollNaht = f
	return func() { opsProtokollNaht = alt }
}
