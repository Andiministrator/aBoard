package wissen

// suche.go — Volltextsuche über den Index (ADR-0137 §6, Suche).
//
// Reiner Textvergleich ohne Regex: Eine Anfrage kann keinen teuren Ausdruck
// bauen (kein ReDoS). Gross-/Kleinschreibung und Unicode-Normalform spielen
// keine Rolle. Alles ist begrenzt: Länge der Anfrage, Zahl der Treffer, Zahl
// der durchsuchten Seiten (der Index ist auf MaxDateien gekappt), Bytes je
// Seite (MaxSeitenBytes) und Bytes je Suche insgesamt. Der Schnipsel ist Text
// — die Oberfläche setzt ihn als Text ein, nie als Markdown.

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	// MinAnfrage/MaxAnfrage: Länge der Anfrage in Zeichen (nach Trimmen).
	MinAnfrage = 2
	MaxAnfrage = 200
	// MaxTreffer: höchstens so viele Seiten in einer Antwort.
	MaxTreffer = 50
	// MaxSucheBytes: so viel wird je Suche höchstens gelesen. Bis F-1891
	// 64 MiB — bei 30 Suchen/min/Person fast 2 GiB Lesen je Minute und Person.
	MaxSucheBytes = 16 << 20
	// MaxGleichzeitigeSuchen: so viele Suchen lesen gleichzeitig, über alle
	// Personen und Quellen; weitere warten (F-1891, globale Bremse).
	MaxGleichzeitigeSuchen = 2
	// schnipselRunen: Umgebung des Treffers in Zeichen.
	schnipselRunen = 160
)

// suchePlaetze begrenzt die gleichzeitig lesenden Suchen.
var suchePlaetze = make(chan struct{}, MaxGleichzeitigeSuchen)

// sucheNaht: nur für Tests — läuft, sobald eine Suche ihren Platz hat.
var sucheNaht func()

// ErrAnfrage: Die Anfrage ist zu kurz oder zu lang.
var ErrAnfrage = errors.New("wissen: suchbegriff zu kurz oder zu lang")

// Treffer ist eine Seite mit Fundstelle.
type Treffer struct {
	Pfad      string `json:"pfad"`
	Titel     string `json:"titel"`
	Schnipsel string `json:"schnipsel,omitempty"`
	// Zeile ist die erste Fundstelle im Text (1-basiert), 0 = nur Titel/Pfad.
	Zeile int `json:"zeile,omitempty"`
}

// Ergebnis der Suche. Gekappt: Es gibt womöglich mehr Treffer (Treffer- oder
// Byte-Grenze erreicht).
type Ergebnis struct {
	Treffer []Treffer `json:"treffer"`
	Gekappt bool      `json:"gekappt,omitempty"`
}

// NormiereAnfrage trimmt, normalisiert und prüft die Länge.
func NormiereAnfrage(q string) (string, error) {
	q = strings.TrimSpace(q)
	if !utf8.ValidString(q) {
		return "", ErrAnfrage
	}
	q = norm.NFC.String(q)
	n := utf8.RuneCountInString(q)
	if n < MinAnfrage || n > MaxAnfrage {
		return "", ErrAnfrage
	}
	return q, nil
}

// Suche durchsucht Titel, Pfad und Text aller Seiten. Endet ctx (der Client
// ist weg), gibt sie ihren Platz auf bzw. wartet nicht weiter auf einen und
// liest keine weitere Seite (Kritiker Sprint-87-Abschluss K-2).
func (q *Quelle) Suche(ctx context.Context, anfrage string) (*Ergebnis, error) {
	a, err := NormiereAnfrage(anfrage)
	if err != nil {
		return nil, err
	}
	klein := strings.ToLower(a)
	idx, platte, err := q.aktuell()
	if err != nil {
		return nil, err
	}
	select {
	case suchePlaetze <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-suchePlaetze }()
	if sucheNaht != nil {
		sucheNaht()
	}

	erg := &Ergebnis{Treffer: []Treffer{}}
	var gelesen int64
	for _, s := range idx.Seiten {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(erg.Treffer) >= MaxTreffer {
			erg.Gekappt = true
			break
		}
		t := Treffer{Pfad: s.Pfad, Titel: s.Titel}
		kopf := strings.Contains(strings.ToLower(s.Titel), klein) ||
			strings.Contains(strings.ToLower(s.Pfad), klein)
		if s.Groesse <= MaxSeitenBytes {
			if gelesen+s.Groesse > MaxSucheBytes {
				erg.Gekappt = true
				break
			}
			if b, lerr := q.leseSeite(platte[s.Pfad]); lerr == nil {
				gelesen += int64(len(b))
				t.Zeile, t.Schnipsel = fundstelle(string(b), klein)
			}
		}
		if kopf || t.Zeile > 0 {
			erg.Treffer = append(erg.Treffer, t)
		}
	}
	return erg, nil
}

// fundstelle sucht zeilenweise. Die Zeile wird zuerst normalisiert; aus der
// normalisierten Fassung stammt auch der Schnipsel, damit Position und Text
// zusammenpassen. strings.ToLower ändert die Zahl der Zeichen nur in
// Ausnahmen (z. B. „İ“); dann verschiebt sich das Fenster um ein Zeichen,
// die Grenzen in schnipsel halten trotzdem.
func fundstelle(text, klein string) (int, string) {
	for i, zeile := range strings.Split(text, "\n") {
		if !utf8.ValidString(zeile) {
			continue
		}
		z := norm.NFC.String(strings.TrimRight(zeile, "\r"))
		pos := strings.Index(strings.ToLower(z), klein)
		if pos < 0 {
			continue
		}
		return i + 1, schnipsel(z, utf8.RuneCountInString(strings.ToLower(z)[:pos]), utf8.RuneCountInString(klein))
	}
	return 0, ""
}

// schnipsel schneidet ein Fenster um den Treffer an Zeichengrenzen.
func schnipsel(z string, start, laenge int) string {
	r := []rune(z)
	rand := (schnipselRunen - laenge) / 2
	if rand < 0 {
		rand = 0
	}
	von, bis := start-rand, start+laenge+rand
	if von < 0 {
		von = 0
	}
	if bis > len(r) {
		bis = len(r)
	}
	if von > len(r) {
		von = len(r)
	}
	s := strings.TrimSpace(string(r[von:bis]))
	if von > 0 {
		s = "…" + s
	}
	if bis < len(r) {
		s += "…"
	}
	return s
}
