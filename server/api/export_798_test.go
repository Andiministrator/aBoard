package api

// SetzeAnlageBremseNahtFuerTest lässt den Schreibvorgang nach der Reservierung
// der Anlage-Bremse scheitern (Karte #798, Kritik Befund 1). Rückgabe stellt
// zurück.
func SetzeAnlageBremseNahtFuerTest(f func() error) func() {
	alt := anlageBremseNaht
	anlageBremseNaht = f
	return func() { anlageBremseNaht = alt }
}
