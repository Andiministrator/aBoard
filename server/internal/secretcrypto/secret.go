// Package secretcrypto liefert eine schlanke Wrap-Schicht über AES-256-GCM
// für authentifizierte Verschlüsselung von Bestands-Daten (Karte #306,
// ADR-0101 §6).
//
// Verwendet ausschließlich crypto/aes + crypto/cipher aus der Stdlib —
// keine externen Krypto-Abhängigkeiten (modernc.org/sqlite ist die einzige
// nicht-Stdlib-Dep des Server-Moduls, das soll so bleiben). AES-256-GCM
// bietet authentifizierte Verschlüsselung mit konstanter Schlüssellänge
// (32 Bytes) und einer 12-Byte-Nonce — die ist hier ZUFÄLLIG pro Seal
// (crypto/rand), nicht zählerbasiert: bei <2^32 Operationen pro Master-Key
// ist die Kollisionswahrscheinlichkeit vernachlässigbar (Geburtstagsschranke
// 2^-32 ≪ jedes realistische Angriffsfenster für Config-Werte).
//
// Wiederverwendung in Karte #298 (LLM-Key-Storage, ADR-0094): genau diese
// Box wird dort eingebunden — ein einziger Master-Key ABOARD_SECRET_KEY,
// eine einzige Audit-Spur. Daher generischer Paketname (nicht "configcrypto").
//
// Format (Base64-StdEncoding):
//
//	nonce(12B) || ciphertext-with-tag(N+16B)
//
// Bei Master-Key-Rotation: alte Werte müssen explizit neu verschlüsselt
// werden (Migration-Tool, Folge-Karte). Diese Box selbst hält genau einen
// Master-Key.
package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
)

// KeySize ist die geforderte Master-Key-Länge (AES-256).
const KeySize = 32

// NonceSize ist die GCM-Standardlänge (12 Bytes).
const NonceSize = 12

// ErrInvalidKey signalisiert eine Master-Key-Validierungsfehler: leer,
// falsche Länge oder nicht-dekodierbar.
var ErrInvalidKey = errors.New("secretcrypto: ungültiger Master-Key")

// ErrCiphertextCorrupt zeigt einen Open-Fehler an (zu kurz, manipulierte
// Auth-Tag, falscher Key).
var ErrCiphertextCorrupt = errors.New("secretcrypto: Ciphertext beschädigt oder Schlüssel falsch")

// Box kapselt einen GCM-AEAD mit gepinntem Master-Key. Sicher für parallele
// Nutzung (cipher.AEAD aus der Stdlib ist goroutine-sicher für Seal/Open).
type Box struct {
	aead cipher.AEAD
}

// New parst masterKey als hex- oder base64-StdEncoding-kodierte 32 Bytes
// und initialisiert die AEAD. Akzeptiert beide Formate, weil
// Container/Compose-Configs typischerweise Base64-Strings benutzen,
// systemd-Env-Files oft Hex.
//
// Rückgabe ErrInvalidKey bei: leerem String, nicht-dekodierbarer Input,
// Länge != 32 Bytes nach Dekodierung.
func New(masterKey string) (*Box, error) {
	aead, err := NewAEAD(masterKey)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// NewAEAD parst und validiert masterKey wie New und liefert die ROHE AEAD
// statt der Box. Gedacht für Konsumenten, die dieselbe Schlüsselquelle
// nutzen, aber eine andere Rahmung brauchen als Box (Base64-Single-Shot):
// server/boardbackup verschlüsselt damit Archiv-STRÖME in Blöcken, weil ein
// 500-MB-Backup als Single-Shot-Base64 rund das 3,3-fache seiner Grösse im
// RAM belegen würde (#336, ADR-0098 §10).
//
// Der Punkt dieser Funktion ist, dass die Schlüssel-Validierung an GENAU
// EINER Stelle bleibt: Wer sie umgeht und selbst dekodiert, bekommt eine
// zweite Auffassung davon, was ein gültiger ABOARD_SECRET_KEY ist.
func NewAEAD(masterKey string) (cipher.AEAD, error) {
	if masterKey == "" {
		return nil, fmt.Errorf("%w: leer", ErrInvalidKey)
	}
	key, err := decodeKey(masterKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: Länge %d, erwartet %d", ErrInvalidKey, len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		// Konstruktion eines AES-Blocks mit korrekter Schlüssellänge schlägt
		// in der Stdlib NIE fehl — defensiv trotzdem propagieren.
		return nil, fmt.Errorf("%w: aes init: %v", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm init: %v", ErrInvalidKey, err)
	}
	return aead, nil
}

func decodeKey(s string) ([]byte, error) {
	// Hex-First: erzeugt 32 Bytes aus 64 Hex-Zeichen — der häufigere Fehlfall
	// (Base64-String, der zufällig nur Hex-Zeichen enthält) ist kürzer als
	// 64 Zeichen und schlägt früh fehl.
	if len(s) == hex.EncodedLen(KeySize) {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	// Sonst Base64 (Standard- oder URL-Encoding mit/ohne Padding).
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("weder hex noch base64 dekodierbar")
}

// Seal verschlüsselt plaintext und gibt Base64-StdEncoding zurück.
// Nonce wird zufällig erzeugt (crypto/rand) und im Output-Prefix mitgeführt.
// Determinismus-frei: gleicher Input → unterschiedliche Ciphertexts.
func (b *Box) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretcrypto: nonce: %w", err)
	}
	ct := b.aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, NonceSize+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open entschlüsselt einen mit Seal erzeugten Base64-Ciphertext.
// Bei jeder Form von Manipulation (zu kurz, falsche Auth-Tag, falscher Key)
// → ErrCiphertextCorrupt, NIE Teil-Plaintext.
func (b *Box) Open(ciphertextB64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("%w: base64: %v", ErrCiphertextCorrupt, err)
	}
	if len(raw) < NonceSize+b.aead.Overhead() {
		return nil, fmt.Errorf("%w: zu kurz", ErrCiphertextCorrupt)
	}
	nonce, ct := raw[:NonceSize], raw[NonceSize:]
	pt, err := b.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCiphertextCorrupt, err)
	}
	return pt, nil
}

// DeriveKey leitet aus dem Master-Key einen DOMÄNENGETRENNTEN Schlüssel ab
// und gibt ihn hex-kodiert zurück (direkt an New/NewAEAD weiterreichbar).
//
// WARUM (Sprint-58-Kritiker): Unter einem Master-Key dürfen nicht zwei
// verschiedene Nonce-Schemata arbeiten. Diese Box nutzt 12 volle Zufallsbytes
// pro Seal; server/boardbackup versiegelt Archiv-Ströme blockweise mit
// 8 Zufallsbytes plus Zähler. Ohne Trennung zehren beide vom selben
// Nonce-Budget (Dateikopf: „<2^32 Operationen pro Master-Key"), und niemand
// zählt über die Konsumenten hinweg. Eine GCM-Nonce-Wiederholung gibt nicht
// nur den Keystream preis, sondern erlaubt die Rekonstruktion des Auth-Keys —
// der Schaden bliebe also nicht beim Backup.
//
// Die Ableitung läuft über die DEKODIERTEN Schlüsselbytes, nicht über den
// Roh-String. Das ist der Punkt, an dem diese Funktion hier und nicht beim
// Aufrufer steht: Ein Operator, der denselben Schlüssel einmal hex und
// einmal base64 notiert, bekommt sonst zwei verschiedene abgeleitete
// Schlüssel — seine Config-Secrets liessen sich weiter lesen, seine Backups
// nicht mehr. Das Schlüsselmaterial verlässt dafür dieses Paket nicht.
//
// Der Domänen-String ist Teil des jeweiligen Formats: Wer ihn ändert, macht
// alles unlesbar, was damit versiegelt wurde.
//
// ERLAUBT SIND NUR `[A-Za-z0-9._-]+` (Sprint-62-Kritiker, S1). Das ist keine
// Kosmetik, sondern die Voraussetzung, unter der `Fingerprint` überhaupt eine
// von dieser Funktion getrennte Konstruktion ist: Solange `domain` beliebige
// Bytes sein darf, ist JEDES `HMAC(key, X)` mit festem X durch `DeriveKey`
// herstellbar — auch eines mit vorangestelltem Typ-Byte. Erst die
// Einschränkung des Zeichensatzes macht Bytes wie `0x01` für Aufrufer
// unerreichbar und damit zu einem Merkmal, das nur eine Seite erzeugen kann.
// Siehe die ausführliche Begründung bei `Fingerprint`.
func DeriveKey(masterKey, domain string) (string, error) {
	if !istGueltigeDomaene(domain) {
		return "", fmt.Errorf("%w: unzulässige Domäne %q — erlaubt sind nur [A-Za-z0-9._-]+",
			ErrInvalidKey, domain)
	}
	key, err := decodeKey(masterKey)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	if len(key) != KeySize {
		return "", fmt.Errorf("%w: Länge %d, erwartet %d", ErrInvalidKey, len(key), KeySize)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(domain))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// domaeneRe hält den Zeichensatz für Ableitungs-Domänen fest. Bewusst als
// Allowlist: Eine Blockliste („keine Steuerzeichen") müsste bei jedem neuen
// Sonderfall nachgezogen werden, und genau daran ist die erste Fassung der
// Fingerabdruck-Trennung gescheitert.
var domaeneRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func istGueltigeDomaene(d string) bool { return domaeneRe.MatchString(d) }

// FingerprintSize ist die Länge eines Schlüssel-Fingerabdrucks in Bytes.
// Acht Bytes: genug, damit zwei zufällige Schlüssel praktisch nie denselben
// Abdruck tragen (Kollision bei n Schlüsseln ≈ n²/2^65), und kurz genug, um
// ihn in einen Dateikopf zu schreiben, ohne dass er ins Gewicht fällt.
const FingerprintSize = 8

// fingerprintDomain trennt den Fingerabdruck von jeder Verschlüsselungs-
// Ableitung. Er ist Teil des Formats, das ihn schreibt: eine Änderung macht
// bestehende Archive nicht unlesbar, aber ihr Abdruck passt dann zu keinem
// Schlüssel mehr.
const fingerprintDomain = "aboard-key-fingerprint-v1"

// Fingerprint bildet einen kurzen Erkennungswert für einen Schlüssel.
//
// WOZU: Ein Format, das den Fingerabdruck seines Schlüssels mitschreibt, kann
// beim Öffnen sagen „dieses Archiv gehört zu einem anderen Schlüssel" statt
// „beschädigt, manipuliert oder falscher Schlüssel". Das ist der Unterschied
// zwischen einem Betreiber, der nach einer Schlüsselrotation den richtigen
// Schlüssel sucht, und einem, der zuerst die Datei verdächtigt — also genau
// das Artefakt, das er gerade dringend braucht ([[F-709]]).
//
// WAS ER NICHT IST: kein Sicherheitsmerkmal. Er beweist nichts über den
// Inhalt und ersetzt keine Authentifizierung; wer ihn fälscht, scheitert
// trotzdem am AEAD-Tag. Wer ihn als Prüfsumme liest, liest ihn falsch.
//
// WAS ER PREISGIBT — zwei Dinge, das zweite ist das unangenehmere:
//
//  1. Er erlaubt, zwei Archive demselben Schlüssel zuzuordnen, ohne den
//     Schlüssel zu kennen. Bewusst in Kauf genommen: Wer die Archive
//     nebeneinander sieht, sieht ohnehin dieselben Dateinamen und Zeitpunkte.
//  2. Er ist ein OFFLINE-VERIFIKATOR für geratene Schlüssel. Weil die
//     HMAC-Nachricht fest ist, genügen die ersten 24 Bytes eines Archivs, um
//     einen Kandidaten mit zwei HMAC-Rechnungen zu bestätigen — ohne
//     Chiffrat, ohne Entschlüsselungsversuch, ohne Serverkontakt. Beim
//     Vorgängerformat brauchte dieselbe Bestätigung mindestens den ersten
//     Block (Sprint-62-Kritiker, S1).
//
// Punkt 2 ist folgenlos, solange `ABOARD_SECRET_KEY` 32 Zufallsbytes sind —
// dann gibt es nichts zu raten. Er wird relevant, wenn jemand einen
// Platzhalter aus einer Vorlage, eine Passphrase oder einen abgeleiteten Wert
// einsetzt: Die Validierung prüft die LÄNGE des Schlüssels, nicht seine
// Entropie, und kann das nicht. Wer den Schlüssel nicht zufällig erzeugt,
// verliert durch das Format also eine Hürde, die vorher da war.
//
// Ein Rückschluss auf den Schlüssel selbst ist der Abdruck nicht
// (HMAC-SHA256, auf 8 Bytes gekürzt).
//
// Wie DeriveKey läuft die Ableitung über die DEKODIERTEN Schlüsselbytes —
// derselbe Grund: hex und base64 desselben Schlüssels müssen denselben
// Abdruck ergeben, sonst hält der Betreiber seinen eigenen Schlüssel für einen
// fremden. Und wie dort verlässt das Schlüsselmaterial dieses Paket nicht.
func Fingerprint(key string) ([]byte, error) {
	k, err := decodeKey(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	if len(k) != KeySize {
		return nil, fmt.Errorf("%w: Länge %d, erwartet %d", ErrInvalidKey, len(k), KeySize)
	}
	mac := hmac.New(sha256.New, k)
	// Typ-Byte VOR der Domäne. Ohne es wäre diese Funktion Byte für Byte
	// `DeriveKey(key, fingerprintDomain)`, auf 8 Bytes gekürzt — und damit
	// stünden acht Bytes eines regulär ableitbaren Subkeys im Klartext in
	// jedem Archivkopf, sobald jemand dieselbe Domäne einmal zum Ableiten
	// benutzt.
	//
	// 0x01 = „Fingerabdruck". DASS DAS TRÄGT, HÄNGT AN `DeriveKey`: Jene
	// Funktion nimmt nur Domänen aus `[A-Za-z0-9._-]+` entgegen und kann ein
	// Steuerzeichen deshalb gar nicht an den Anfang schreiben. Ohne diese
	// Einschränkung wäre das Typ-Byte wertlos — `DeriveKey(key,
	// "\x01aboard-key-fingerprint-v1")` ergäbe exakt denselben Wert.
	// Nachgerechnet und belegt vom Sprint-62-Kritiker (S1); die erste Fassung
	// dieser Zeilen behauptete die Trennung „egal welche Domäne jemand wählt"
	// und war damit falsch. Ein Präfix ist ein Wert; erst der eingeschränkte
	// Zeichensatz auf der Gegenseite macht daraus eine Grenze.
	mac.Write([]byte{fingerprintTag})
	mac.Write([]byte(fingerprintDomain))
	return mac.Sum(nil)[:FingerprintSize], nil
}

// fingerprintTag trennt die Fingerabdruck-Konstruktion strukturell von
// DeriveKey — siehe Begründung in Fingerprint.
const fingerprintTag = 0x01

// SealString ist ein Convenience-Wrap für []byte(s).
func (b *Box) SealString(s string) (string, error) { return b.Seal([]byte(s)) }

// OpenString ist ein Convenience-Wrap, der Plaintext als string liefert.
func (b *Box) OpenString(ciphertextB64 string) (string, error) {
	pt, err := b.Open(ciphertextB64)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
