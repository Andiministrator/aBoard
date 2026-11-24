package api

// SetzeOpsNachSchalterNaht setzt die Test-Naht zwischen dem Lesen des
// Rückweg-Schalters und der Sperre (F-2300). Rückgabe stellt den alten Wert her.
func SetzeOpsNachSchalterNahtFuerTest(f func()) func() {
	alt := opsNachSchalterNaht
	opsNachSchalterNaht = f
	return func() { opsNachSchalterNaht = alt }
}
