package llm

import (
	"context"
	"testing"
	"time"
)

// TestStreamIdleGuard_BrichtNachStilleAb — Kritiker Sprint 70, K-A15 (G4):
// streamIdleGuard ist seit F-1126 der einzige Schutz gegen eine tote, aber
// offene Stream-Verbindung. Ohne touch() muss der Kontext nach der Frist
// abbrechen; mit regelmässigem touch() darf er es nicht.
//
// erstbyte == idle in diesem Test (F-1140 änderte die Signatur um einen
// zweiten Parameter, das Verhalten OHNE jeden touch() bzw. MIT
// regelmässigem touch() bleibt unverändert, wenn beide Fristen gleich
// gewählt sind) — der Erstbyte/Idle-Unterschied selbst ist Gegenstand von
// TestStreamIdleGuard_ErstbyteFristGiltNurBisZumErstenTouch.
func TestStreamIdleGuard_BrichtNachStilleAb(t *testing.T) {
	ctx, _, cancel := streamIdleGuard(context.Background(), 80*time.Millisecond, 80*time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Kontext nach 2 s Stille nicht abgebrochen (Frist 80 ms)")
	}

	ctx2, touch, cancel2 := streamIdleGuard(context.Background(), 80*time.Millisecond, 80*time.Millisecond)
	defer cancel2()
	ende := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(ende) {
		touch()
		time.Sleep(20 * time.Millisecond)
		if ctx2.Err() != nil {
			t.Fatal("Kontext trotz regelmässigem touch() abgebrochen")
		}
	}
}

// TestStreamIdleGuard_ErstbyteFristGiltNurBisZumErstenTouch — F-1140: die
// Erstbyte-Frist gilt NUR bis zum ersten touch(); danach zählt in jedem Fall
// die (kürzere) idle-Frist, auch für lange Stille nach dem ersten Token.
// Ohne den Fix (Timer läuft nach dem ersten touch() weiter mit der
// Erstbyte-Frist statt auf idle zurückzufallen) würde dieser Test NICHT rot
// werden — genau deshalb prüft er die Kehrseite von
// TestStreamIdleGuard_LangeStilleVorErstemByte_WirdToleriert: eine lange
// Erstbyte-Frist darf sich nicht in eine dauerhaft lange Toleranz für
// spätere Stille verwandeln.
func TestStreamIdleGuard_ErstbyteFristGiltNurBisZumErstenTouch(t *testing.T) {
	ctx, touch, cancel := streamIdleGuard(context.Background(), 2*time.Second, 60*time.Millisecond)
	defer cancel()
	touch() // erstes Byte kommt sofort an — Frist fällt auf idle (60ms) zurück
	select {
	case <-ctx.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("Kontext nach 1 s Stille NACH dem ersten touch() nicht abgebrochen (idle-Frist 60 ms, erstbyte 2 s) — fällt die Frist nach dem ersten touch() nicht auf idle zurück?")
	}
}

// TestStreamIdleGuard_LangeStilleVorErstemByte_WirdToleriert — F-1140,
// Kernfall: eine Denkphase, die länger als die (kurz gewählte) idle-Frist,
// aber kürzer als die Erstbyte-Frist dauert, darf NICHT abbrechen. Vor dem
// Fix lief der Timer von Anfang an mit der (dann einzigen) idle-Frist — mit
// idle=60ms und keinem touch() für 300ms wäre das VOR F-1140 längst
// abgebrochen. Gegenprobe: erstbyte auf denselben Wert wie idle gesetzt
// reproduziert exakt den Vorher-Zustand und muss abbrechen (siehe
// Unterschied zu TestStreamIdleGuard_BrichtNachStilleAb).
func TestStreamIdleGuard_LangeStilleVorErstemByte_WirdToleriert(t *testing.T) {
	ctx, touch, cancel := streamIdleGuard(context.Background(), 500*time.Millisecond, 60*time.Millisecond)
	defer cancel()
	// 300ms Stille VOR dem ersten touch(): deutlich mehr als die idle-Frist
	// (60ms), aber unter der Erstbyte-Frist (500ms).
	time.Sleep(300 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("Kontext vor dem ersten touch() abgebrochen, obwohl die Stille (300ms) unter der Erstbyte-Frist (500ms) lag")
	}
	touch()
	if ctx.Err() != nil {
		t.Fatal("Kontext direkt nach dem ersten touch() abgebrochen")
	}
}
