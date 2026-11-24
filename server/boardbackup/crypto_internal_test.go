package boardbackup

// Sprint-62-Kritiker, V3 — die Lücke, die beide eingecheckten Testvektoren
// offenlassen.
//
// `v1Vektor` und `v2Vektor` in `crypto_schluesselwechsel_test.go` bewachen das
// Format zuverlässig gegen Änderungen an Magic, Kopf-Layout, Domänen-Strings,
// Fingerabdruck-Herkunft und AAD-Aufbau — jede davon wurde einzeln
// zurückgebaut und machte sie rot. Sie bestehen aber aus je EINEM kurzen
// Block, und deshalb sagen sie nichts über die Blockgrösse.
//
// Ein Vektor mit zwei vollen Blöcken wäre 128 KiB Hex im Quelltext. Die
// Zusicherung auf die Konstanten kostet nichts und fängt denselben Fall —
// sie steht hier statt in der externen Testdatei, weil beide Werte
// paket-privat sind.

import "testing"

func TestBlockgroesseBleibtStabil(t *testing.T) {
	// Ein Block, wie ihn jedes bisher geschriebene Archiv enthält:
	// 64 KiB Plaintext plus 16 Byte GCM-Tag.
	const bestandsBlockAufDraht = 64*1024 + 16

	if encMaxSealed < bestandsBlockAufDraht {
		t.Fatalf("encMaxSealed = %d < %d: Archive mit 64-KiB-Blöcken — also alles, was "+
			"bis heute geschrieben wurde — werden künftig mit „unplausible Blocklänge\" "+
			"abgelehnt. Ein Formatbruch ohne Formatwechsel, den kein Testvektor fängt.",
			encMaxSealed, bestandsBlockAufDraht)
	}

	if encChunkPlain != 64*1024 {
		t.Fatalf("encChunkPlain = %d statt 65536. Alte Archive bleiben zwar lesbar (die "+
			"Länge steht in jedem Blockkopf), aber die Leseschranke `encMaxSealed` muss "+
			"dann bewusst auf dem alten Wert bleiben. Diese Zeile ist der Anlass, darüber "+
			"nachzudenken — nicht ein Grund, sie anzupassen.", encChunkPlain)
	}
}
