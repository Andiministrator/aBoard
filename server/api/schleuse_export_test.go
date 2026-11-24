package api

// schleuse_export_test.go — Test-Zugang zu den Fristen des Auszugs (Karte
// #685). Nur in Testläufen kompiliert.

import "time"

// SetzeAuszugTakte verkürzt Lebenszeichen und Modellfrist; die Rückgabe
// stellt die alten Werte wieder her.
func SetzeAuszugTakte(lebenszeichen, frist time.Duration) (zurueck func()) {
	altL, altF := auszugLebenszeichen, auszugModellFrist
	auszugLebenszeichen, auszugModellFrist = lebenszeichen, frist
	return func() { auszugLebenszeichen, auszugModellFrist = altL, altF }
}
