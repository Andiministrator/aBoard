//go:build race

package fold

// raceAktiv: unter `go test -race` sind Laufzeiten um Faktor ~10 verfälscht;
// die 200-ms-Grenze wird dann nur protokolliert, nicht geprüft.
const raceAktiv = true
