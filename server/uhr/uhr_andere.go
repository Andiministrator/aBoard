//go:build !linux

package uhr

// kernelSynchron: Ausserhalb von Linux (Entwicklung auf anderen Systemen)
// gibt es kein adjtimex; die Uhr gilt als gestellt. Ausgeliefert wird der
// Server nur als Linux-Image.
func kernelSynchron() (bool, error) { return true, nil }
