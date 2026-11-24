package api

// passwort.go — Passwort-Hashes für eigene Konten (ADR-0149 §3, Karte #1074).
//
// argon2id aus golang.org/x/crypto/argon2 im PHC-Format:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<Salz base64>$<Hash base64>
//
// Die Parameter stehen in jedem Hash. Ein späterer Wechsel der Voreinstellung
// macht alte Hashes also nicht ungültig; geprüft wird immer mit den
// Parametern des gespeicherten Hashs (innerhalb fester Grenzen, damit ein
// verfälschter Datenbankeintrag den Server nicht mit 4 GiB in die Knie
// zwingt).
//
// Parameterwahl (gemessen 03.11.2026, Median
// über 7 Läufe): Entwicklungsrechner amd64, 16 Kerne: m=64 MiB, t=3, p=2 →
// 51 ms, vier parallel 71 ms. Auf einem Raspberry Pi 5 nicht gemessen;
// argon2 ist dort speicherbandbreitengebunden, erwartet wird das Vier- bis
// Sechsfache (rund 0,2–0,3 s je Prüfung). RFC 9106 empfiehlt für
// speicherbegrenzte Umgebungen m=64 MiB, t=3, p=4; p=2 ist gewählt, weil
// zwei gleichzeitige Prüfungen auf dem Pi sonst alle vier Kerne belegten.
//
// Gleichzeitige Prüfungen sind global begrenzt (Kritik H2): Jede belegt
// m Speicher. Wer auf einen Platz wartet, wartet höchstens
// passwortWarteGrenze; danach gilt die Anmeldung als „gerade überlastet“.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	// PasswortMindestLaenge in Zeichen (ADR-0149 §3).
	PasswortMindestLaenge = 12
	// PasswortHoechstLaenge in Bytes: begrenzt die Arbeit je Versuch; länger
	// ist kein sinnvolles Passwort, aber ein billiger Weg, Rechenzeit zu binden.
	PasswortHoechstLaenge = 1024

	argonSpeicherKiB = 64 * 1024
	argonDurchlaeufe = 3
	argonParallel    = 2
	argonSalzLaenge  = 16
	argonHashLaenge  = 32

	// Grenzen für Parameter aus gespeicherten Hashes.
	argonSpeicherMax = 256 * 1024
	argonDurchMax    = 10
	argonParallelMax = 16

	// argonPlaetze: höchstens so viele Prüfungen gleichzeitig (Kritik H2).
	argonPlaetze = 2
	// passwortWarteGrenze: so lange wartet eine Prüfung auf einen Platz.
	passwortWarteGrenze = 5 * time.Second
)

var (
	// ErrPasswortZuKurz / ErrPasswortZuLang: PasswortZulaessig.
	ErrPasswortZuKurz = fmt.Errorf("das Passwort braucht mindestens %d Zeichen", PasswortMindestLaenge)
	ErrPasswortZuLang = fmt.Errorf("das Passwort darf höchstens %d Bytes lang sein", PasswortHoechstLaenge)
	// ErrPasswortUeberlastet: kein Prüfplatz innerhalb von passwortWarteGrenze.
	ErrPasswortUeberlastet = errors.New("passwortprüfung überlastet")
	// errHashFormat: der gespeicherte Hash ist kein argon2id im PHC-Format
	// innerhalb der Grenzen.
	errHashFormat = errors.New("passwort-hash: unbekanntes format")
)

// argonSemaphor begrenzt die gleichzeitigen argon2-Läufe (Hashen und Prüfen).
var argonSemaphor = make(chan struct{}, argonPlaetze)

// argonLauf belegt einen Platz, rechnet und gibt ihn wieder frei.
func argonLauf(ctx context.Context, f func()) error {
	ctx, cancel := context.WithTimeout(ctx, passwortWarteGrenze)
	defer cancel()
	select {
	case argonSemaphor <- struct{}{}:
	case <-ctx.Done():
		return ErrPasswortUeberlastet
	}
	defer func() { <-argonSemaphor }()
	f()
	return nil
}

// PasswortZulaessig prüft die Regeln für ein neues Passwort: mindestens
// PasswortMindestLaenge Zeichen (nicht Bytes), höchstens
// PasswortHoechstLaenge Bytes, gültiges UTF-8.
func PasswortZulaessig(pw string) error {
	if len(pw) > PasswortHoechstLaenge {
		return ErrPasswortZuLang
	}
	if !utf8.ValidString(pw) || utf8.RuneCountInString(pw) < PasswortMindestLaenge {
		return ErrPasswortZuKurz
	}
	return nil
}

// PasswortHashen erzeugt den PHC-Hash mit frischem Salz. Prüft die Regeln
// (PasswortZulaessig) selbst, damit kein zu kurzes Passwort gespeichert wird.
func PasswortHashen(ctx context.Context, pw string) (string, error) {
	if err := PasswortZulaessig(pw); err != nil {
		return "", err
	}
	salz := make([]byte, argonSalzLaenge)
	if _, err := rand.Read(salz); err != nil {
		return "", fmt.Errorf("salz: %w", err)
	}
	var h []byte
	if err := argonLauf(ctx, func() {
		h = argon2.IDKey([]byte(pw), salz, argonDurchlaeufe, argonSpeicherKiB, argonParallel, argonHashLaenge)
	}); err != nil {
		return "", err
	}
	return phcFormat(argonSpeicherKiB, argonDurchlaeufe, argonParallel, salz, h), nil
}

func phcFormat(m, t uint32, p uint8, salz, h []byte) string {
	b := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, m, t, p, b.EncodeToString(salz), b.EncodeToString(h))
}

type argonHash struct {
	m, t uint32
	p    uint8
	salz []byte
	hash []byte
}

// phcLesen zerlegt einen argon2id-PHC-Hash und prüft die Grenzen.
func phcLesen(s string) (argonHash, error) {
	var a argonHash
	teile := strings.Split(s, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salz, hash
	if len(teile) != 6 || teile[0] != "" || teile[1] != "argon2id" || teile[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return a, errHashFormat
	}
	var m, t, p uint32
	if n, err := fmt.Sscanf(teile[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || n != 3 ||
		teile[3] != fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p) {
		return a, errHashFormat
	}
	if m < 8*p || m > argonSpeicherMax || t < 1 || t > argonDurchMax || p < 1 || p > argonParallelMax {
		return a, errHashFormat
	}
	b := base64.RawStdEncoding.Strict()
	salz, err1 := b.DecodeString(teile[4])
	hash, err2 := b.DecodeString(teile[5])
	if err1 != nil || err2 != nil || len(salz) < 8 || len(salz) > 64 || len(hash) < 16 || len(hash) > 64 {
		return a, errHashFormat
	}
	return argonHash{m: m, t: t, p: uint8(p), salz: salz, hash: hash}, nil
}

// blindHash ist ein gültiger Hash ohne passendes Passwort. PasswortPruefen
// rechnet gegen ihn, wenn es kein Konto oder keinen Hash gibt — sonst verriete
// die Antwortzeit, ob eine Adresse ein Passwort-Konto hat (ADR-0149 §3:
// „Die Meldung verrät nicht, ob die Adresse existiert“).
var blindHash = phcFormat(argonSpeicherKiB, argonDurchlaeufe, argonParallel,
	[]byte("aboard-blind-salz"), make([]byte, argonHashLaenge))

// PasswortPruefen vergleicht pw mit dem gespeicherten Hash. Ein leerer Hash
// rechnet gegen blindHash und ergibt immer false. Fehler: Überlastung
// (ErrPasswortUeberlastet) oder ein unlesbarer gespeicherter Hash.
func PasswortPruefen(ctx context.Context, pw, gespeichert string) (bool, error) {
	echt := gespeichert != ""
	if !echt {
		gespeichert = blindHash
	}
	a, err := phcLesen(gespeichert)
	if err != nil {
		return false, err
	}
	if len(pw) > PasswortHoechstLaenge {
		// Nicht rechnen, aber auch nicht schneller antworten als sonst
		// nötig wäre: zu lange Eingaben sind nie ein gültiges Passwort.
		pw = pw[:PasswortHoechstLaenge]
		echt = false
	}
	var h []byte
	if err := argonLauf(ctx, func() {
		h = argon2.IDKey([]byte(pw), a.salz, a.t, a.m, a.p, uint32(len(a.hash)))
	}); err != nil {
		return false, err
	}
	return echt && subtle.ConstantTimeCompare(h, a.hash) == 1, nil
}
