package wissen

// F-1891 (Kritiker der Sprint-86-End-QA): Der Neuaufbau des Index läuft
// ohne die Sperre der Quelle — Suche, Seitenabruf und Index warten nicht auf
// einen Ordnerlauf. Es baut immer nur einer; ohne bisherigen Index wartet
// der zweite auf den ersten. Gleichzeitige Suchen sind global begrenzt.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// blockiereBau hält jeden Neuaufbau an, bis frei geschlossen wird, und
// meldet seinen Beginn auf begonnen.
func blockiereBau(t *testing.T) (begonnen chan struct{}, frei chan struct{}, zaehler *atomic.Int32) {
	t.Helper()
	begonnen, frei, zaehler = make(chan struct{}, 8), make(chan struct{}), &atomic.Int32{}
	alt := bauNaht
	bauNaht = func() {
		zaehler.Add(1)
		begonnen <- struct{}{}
		<-frei
	}
	t.Cleanup(func() { bauNaht = alt })
	return begonnen, frei, zaehler
}

func mitFrist(t *testing.T, was string, f func()) {
	t.Helper()
	fertig := make(chan struct{})
	go func() { f(); close(fertig) }()
	select {
	case <-fertig:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s wartet auf den laufenden Neuaufbau", was)
	}
}

func TestNeuaufbauOhneSperre(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "# Alpha\nInhalt alpha")
	schreibe(t, d, StandDatei, `{"commit":"aaaaaaa"}`)
	q := oeffneTest(t, d)
	if _, err := q.Index(); err != nil {
		t.Fatal(err)
	}
	begonnen, frei, zaehler := blockiereBau(t)

	// Stand ändert sich → Neuaufbau nötig; er hängt in der Naht.
	schreibe(t, d, "b.md", "# Beta")
	schreibe(t, d, StandDatei, `{"commit":"bbbbbbb"}`)
	erster := make(chan *Index, 1)
	go func() { idx, _ := q.Index(); erster <- idx }()
	<-begonnen

	// Währenddessen: alter Index, Suche und Seite ohne Warten.
	mitFrist(t, "Index", func() {
		if idx, err := q.Index(); err != nil || len(idx.Seiten) != 1 || idx.Stand.Commit != "aaaaaaa" {
			t.Errorf("während des Neuaufbaus: %v %v", idx, err)
		}
	})
	mitFrist(t, "Suche", func() {
		if erg, err := q.Suche(context.Background(), "alpha"); err != nil || len(erg.Treffer) != 1 {
			t.Errorf("Suche: %+v %v", erg, err)
		}
	})
	mitFrist(t, "Seite", func() {
		if _, _, err := q.Seite("a.md"); err != nil {
			t.Errorf("Seite: %v", err)
		}
	})
	if n := zaehler.Load(); n != 1 {
		t.Errorf("%d Neuaufbauten gleichzeitig, erwartet 1", n)
	}

	close(frei)
	if idx := <-erster; idx == nil || len(idx.Seiten) != 2 || idx.Stand.Commit != "bbbbbbb" {
		t.Fatalf("nach dem Neuaufbau: %v", idx)
	}
	if idx, _ := q.Index(); len(idx.Seiten) != 2 {
		t.Errorf("neuer Index nicht übernommen: %v", pfade(idx))
	}
}

// Ohne bisherigen Index gibt es nichts Altes zu liefern: der zweite Aufruf
// wartet auf den ersten und baut nicht selbst.
func TestNeuaufbauErsterIndexWartet(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "a")
	q := oeffneTest(t, d)
	begonnen, frei, zaehler := blockiereBau(t)
	ergebnisse := make(chan *Index, 2)
	go func() { idx, _ := q.Index(); ergebnisse <- idx }()
	<-begonnen
	go func() { idx, _ := q.Index(); ergebnisse <- idx }()
	select {
	case idx := <-ergebnisse:
		t.Fatalf("Ergebnis vor dem Ende des Neuaufbaus: %v", idx)
	case <-time.After(50 * time.Millisecond):
	}
	close(frei)
	for range 2 {
		if idx := <-ergebnisse; idx == nil || len(idx.Seiten) != 1 {
			t.Errorf("Index: %v", idx)
		}
	}
	if n := zaehler.Load(); n != 1 {
		t.Errorf("%d Neuaufbauten, erwartet 1", n)
	}
}

func TestSucheGrenzen(t *testing.T) {
	if MaxSucheBytes > 16<<20 || cap(suchePlaetze) != MaxGleichzeitigeSuchen || MaxGleichzeitigeSuchen < 1 {
		t.Errorf("Grenzen: MaxSucheBytes=%d Plätze=%d", MaxSucheBytes, cap(suchePlaetze))
	}
}

// Kritiker Sprint 87 K-2: Die Grenze greift — die dritte Suche wartet, bis
// eine der beiden ersten fertig ist.
func TestSucheHoechstensZweiGleichzeitig(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "alpha")
	q := oeffneTest(t, d)
	if _, err := q.Index(); err != nil {
		t.Fatal(err)
	}
	var drin atomic.Int32
	frei := make(chan struct{})
	alt := sucheNaht
	sucheNaht = func() { drin.Add(1); <-frei }
	t.Cleanup(func() { sucheNaht = alt })

	fertig := make(chan struct{}, MaxGleichzeitigeSuchen+1)
	for range MaxGleichzeitigeSuchen + 1 {
		go func() { _, _ = q.Suche(context.Background(), "alpha"); fertig <- struct{}{} }()
	}
	frist := time.Now().Add(2 * time.Second)
	for drin.Load() < MaxGleichzeitigeSuchen && time.Now().Before(frist) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if n := drin.Load(); n != MaxGleichzeitigeSuchen {
		t.Fatalf("%d Suchen gleichzeitig, erwartet %d", n, MaxGleichzeitigeSuchen)
	}
	close(frei)
	for range MaxGleichzeitigeSuchen + 1 {
		select {
		case <-fertig:
		case <-time.After(2 * time.Second):
			t.Fatal("Suche hängt nach dem Freigeben")
		}
	}
	if n := drin.Load(); n != MaxGleichzeitigeSuchen+1 {
		t.Errorf("%d Suchen gelaufen, erwartet %d", n, MaxGleichzeitigeSuchen+1)
	}
}

// Kritiker Sprint 87 K-3: Eine Panik im Neuaufbau lässt niemanden hängen —
// der nächste Aufruf baut neu.
func TestNeuaufbauNachPanik(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "a")
	q := oeffneTest(t, d)
	alt := bauNaht
	bauNaht = func() { panic("F-1891 K-3 Testpanik") }
	func() {
		defer func() { _ = recover() }()
		_, _ = q.Index()
	}()
	bauNaht = alt
	mitFrist(t, "Index nach Panik", func() {
		if idx, err := q.Index(); err != nil || len(idx.Seiten) != 1 {
			t.Errorf("nach Panik: %v %v", idx, err)
		}
	})
}

// Kritiker Sprint-87-Abschluss K-2: Wer auf einen Platz wartet, gibt auf,
// sobald sein Kontext endet (Client weg) — statt bis zur Freigabe zu hängen
// und danach für niemanden zu lesen.
func TestSucheGibtPlatzwartenBeiAbbruchAuf(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "alpha")
	q := oeffneTest(t, d)
	if _, err := q.Index(); err != nil {
		t.Fatal(err)
	}
	var drin atomic.Int32
	frei := make(chan struct{})
	alt := sucheNaht
	sucheNaht = func() { drin.Add(1); <-frei }
	t.Cleanup(func() { sucheNaht = alt })

	fertig := make(chan struct{}, MaxGleichzeitigeSuchen)
	for range MaxGleichzeitigeSuchen {
		go func() { _, _ = q.Suche(context.Background(), "alpha"); fertig <- struct{}{} }()
	}
	frist := time.Now().Add(2 * time.Second)
	for drin.Load() < MaxGleichzeitigeSuchen && time.Now().Before(frist) {
		time.Sleep(time.Millisecond)
	}
	if n := drin.Load(); n != MaxGleichzeitigeSuchen {
		t.Fatalf("%d Suchen haben einen Platz, erwartet %d", n, MaxGleichzeitigeSuchen)
	}

	ctx, abbrechen := context.WithCancel(context.Background())
	ergebnis := make(chan error, 1)
	go func() { _, err := q.Suche(ctx, "alpha"); ergebnis <- err }()
	time.Sleep(30 * time.Millisecond)
	abbrechen()
	select {
	case err := <-ergebnis:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("abgebrochene Suche: %v, erwartet context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abgebrochene Suche wartet weiter auf einen Platz")
	}
	if n := drin.Load(); n != MaxGleichzeitigeSuchen {
		t.Errorf("abgebrochene Suche hat doch gelesen: %d", n)
	}
	close(frei)
	for range MaxGleichzeitigeSuchen {
		<-fertig
	}
	// Abbruch, nachdem die Suche ihren Platz hat: sie liest keine Seite mehr.
	ctx2, abbrechen2 := context.WithCancel(context.Background())
	sucheNaht = abbrechen2
	if erg, err := q.Suche(ctx2, "alpha"); !errors.Is(err, context.Canceled) {
		t.Errorf("Suche nach Abbruch im Platz: %v %v, erwartet context.Canceled", erg, err)
	}
}
