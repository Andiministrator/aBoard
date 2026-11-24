package boardbackup

// crypto.go — Encryption-at-Rest für Board-Backup-Archive (#336, ADR-0098 §10).
//
// WARUM EIN EIGENES RAHMENFORMAT UND NICHT secretcrypto.Box.Seal?
//
// `secretcrypto.Box` ist eine Single-Shot-Box für Config-Werte: sie nimmt
// einen kompletten Plaintext, gibt einen Base64-String zurück. Für ein
// Backup-Archiv (Obergrenze `backup_board.max_upload_mb`, Default 500 MB)
// hiesse das: Plaintext + Ciphertext + Base64-Aufblähung gleichzeitig im
// RAM — rund das 3,3-fache der Archivgrösse. Dieses Modul verschlüsselt
// stattdessen einen STROM in Blöcken fester Grösse; der Speicherbedarf ist
// konstant (ein Block), unabhängig von der Archivgrösse.
//
// **Diese Zusage gilt für die SCHREIBSEITE** (Sprint-58-Kritiker, D1). Der
// Restore-Pfad in `restore.go` puffert das entpackte Archiv weiterhin
// vollständig im Speicher (`rehashBuf` plus die `attachments`-Map). Für die
// Leseseite ist die Grenze deshalb nicht der Blockpuffer, sondern
// `RestoreOptions.MaxUncompressedBytes` — und die musste in Sprint 58 erst
// eingeführt werden, weil ein 10-MB-Upload sonst auf zweistellige Gigabyte
// expandieren konnte. Wer den Restore-Pfad streamfähig machen will, findet
// dort die eigentliche Baustelle; hier steht sie nur, damit die Begründung
// dieses Moduls nicht mehr verspricht, als sie hält.
//
// Der SCHLÜSSEL kommt trotzdem aus derselben Quelle: `secretcrypto.NewAEAD`
// parst und validiert `ABOARD_SECRET_KEY`. Es gibt genau ein
// Schlüsselmanagement im Server (Entscheid des Maintainers, 2026-08-19) — dieses Modul
// bringt kein zweites mit, nur eine andere Rahmung derselben Primitive.
//
// FORMAT (binär, nicht Base64). Zwei Versionen, beide lesbar:
//
//	"ABBKENC2"          8 Byte  Magic + Formatversion (seit Sprint 62)
//	keyFingerprint      8 Byte  Erkennungswert des Backup-Subkeys, KLARTEXT
//	fileNonce           8 Byte  zufällig pro Datei (crypto/rand)
//	dann n Blöcke:
//	  flag              1 Byte  0x00 = weiterer Block folgt, 0x01 = letzter Block
//	  len               4 Byte  big-endian, Länge des versiegelten Blocks
//	  sealed          len Byte  AES-256-GCM(Plaintext) inkl. 16-Byte-Tag
//
//	nonce_i = fileNonce(8) || uint32be(i)                    — i = Blockzähler ab 0
//	aad_i   = uint32be(i) || flag || keyFingerprint(8)       — authentifiziert, nicht verschlüsselt
//
// "ABBKENC1" ist das Vorgängerformat: derselbe Aufbau OHNE das
// Fingerabdruck-Feld, und entsprechend `aad_i = uint32be(i) || flag`.
// Bestehende Archive werden weiterhin gelesen; geschrieben wird nur noch V2.
//
// WARUM DER FINGERABDRUCK ([[F-709]], Karte #437): Ohne ihn meldet ein
// Restore mit dem falschen Schlüssel „beschädigt, manipuliert ODER mit
// falschem Schlüssel erzeugt" — der Betreiber kann die Fälle nicht trennen und
// verdächtigt zuerst die Datei, also genau das Artefakt, das er gerade
// dringend braucht. Mit ihm lautet die Antwort „dieses Archiv gehört zu einem
// anderen Schlüssel", und der Restore kann zusätzlich einen konfigurierten
// Vorgängerschlüssel (`ABOARD_SECRET_KEY_PREVIOUS`) gezielt heranziehen.
//
// Er ist eine DIAGNOSEHILFE, kein Sicherheitsmerkmal (siehe
// `secretcrypto.Fingerprint`).
//
// WARUM ER TROTZDEM IN DER AAD STEHT — und was das NICHT leistet: Ein
// umgeschriebener Abdruck führt weiterhin zu einer irreführenden Meldung
// („gehört zu einem anderen Schlüssel" für das eigene Archiv), denn die
// Abdruck-Prüfung läuft VOR dem ersten Auth-Tag; daran ändert die AAD nichts.
// Der Entwurf dieser Karte behauptete das zunächst — der Code deckt es nicht.
//
// Was die AAD wirklich leistet, ist DOWNGRADE-SCHUTZ: Ohne sie liesse sich ein
// V2-Archiv in ein V1-Archiv umschreiben, indem man das Magic ändert und die
// acht Abdruck-Bytes entfernt. Nonce und Blöcke blieben gültig, das Archiv
// ginge auf — und die Schlüsselinformation wäre still verschwunden. Mit dem
// Abdruck in der AAD scheitert genau das am Auth-Tag.
//
// Was die Rahmung leistet und was nicht:
//
//   - Vertraulichkeit + Integrität pro Block: AES-256-GCM.
//   - Nonce-Eindeutigkeit: der 8-Byte-Dateianteil ist pro Datei zufällig,
//     der 4-Byte-Zähler pro Block eindeutig. Eine Nonce wiederholt sich
//     also nur bei einer Kollision der 8 Zufallsbytes UND gleichem
//     Blockindex.
//
//     **Die massgebliche Grösse ist die Zahl der ARCHIVE pro Schlüssel**,
//     nicht die Zahl der Blöcke pro Archiv: Bei n Archiven liegt die
//     Kollisionswahrscheinlichkeit bei rund n²/2^65. Für den realistischen
//     Betrieb (100 Boards, nächtlich, 10 Jahre ≈ 3,7·10^5 Archive) sind das
//     ≈ 2^-28 — tragbar. Bei 2^32 Archiven wäre sie 1/2.
//
//     *Korrektur des Sprint-58-Kritikers:* Hier stand zuvor, `maxChunks`
//     sei „deshalb" die Gegenmassnahme. Das ist falsch — `maxChunks`
//     begrenzt den 4-Byte-Zähler INNERHALB einer Datei und berührt die
//     Geburtstagsschranke der 8 Zufallsbytes mit keinem Bit. Die
//     tatsächliche Gegenmassnahme ist die Domänentrennung des Schlüssels
//     (siehe `backupDomain` unten und `secretcrypto.DeriveKey`): das
//     Nonce-Budget dieses Moduls ist
//     dadurch von dem der `secretcrypto.Box` entkoppelt, die unter
//     demselben Master-Key ein ANDERES Nonce-Schema fährt (12 Byte voll
//     zufällig). Ohne die Trennung teilten sich zwei Schemata ein Budget,
//     das niemand zählt.
//
//     Warum das mehr als Buchhaltung ist: Eine GCM-Nonce-Wiederholung gibt
//     nicht nur den Keystream preis, sondern erlaubt die Rekonstruktion des
//     Auth-Keys H („forbidden attack"). Ohne Domänentrennung träfe das
//     denselben Schlüssel, der die LLM-API-Keys schützt.
//   - Umsortieren/Duplizieren von Blöcken: schlägt fehl, weil der
//     Blockindex in Nonce UND AAD steht.
//   - Abschneiden am Dateiende: der letzte Block trägt flag=0x01, und der
//     Leser meldet einen Fehler, wenn der Strom endet, bevor er ihn
//     gesehen hat. Die Grenze dieser Zusage: sie trägt nur, wenn der
//     Leser den Strom auch bis zum Ende liest. Der Restore-Pfad tut das
//     über gzip — und gzip hat mit CRC32 + Längenfeld im Trailer eine
//     eigene, unabhängige Abschneide-Erkennung. Die flag-Prüfung hier ist
//     die zweite Schicht, nicht die einzige.
//   - Was das Format NICHT leistet: es verbirgt die Archivgrösse nicht
//     (kein Padding) und es schützt nicht gegen das Ersetzen einer ganzen
//     Backup-Datei durch eine andere, mit demselben Schlüssel erzeugte.
//     Gegen Letzteres schützt der SHA-256 in `meta.json`, den der Nutzer
//     beim Download vergleichen kann.

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

// EncSuffix ist die Dateiendung, die ein verschlüsseltes Archiv zusätzlich
// trägt: `…tar.gz` → `…tar.gz.enc`. Die Endung ist reine Lesehilfe für den
// Operator — erkannt wird die Verschlüsselung am Magic, nicht am Namen
// (siehe SplitEncrypted).
const EncSuffix = ".enc"

// encMagicV1/encMagicV2 stehen am Anfang jeder verschlüsselten Datei; die
// Ziffer ist die Formatversion. Geschrieben wird V2, gelesen werden beide —
// das war der Zweck der Versionsziffer von Anfang an.
var (
	encMagicV1 = []byte("ABBKENC1")
	encMagicV2 = []byte("ABBKENC2")
)

// encMagicLen ist die Länge jedes Magic (beide gleich lang — die Erkennung
// liest einmal fest so viele Bytes und entscheidet danach).
const encMagicLen = 8

const (
	encNonceFileLen = 8                  // zufälliger Dateianteil der Nonce
	encChunkPlain   = 64 * 1024          // Plaintext-Grösse eines Blocks
	encMaxSealed    = encChunkPlain + 64 // Obergrenze für `len` beim Lesen (Tag + Reserve)
	// maxChunks begrenzt den Blockzähler weit unterhalb des uint32-Überlaufs.
	// 2^24 Blöcke à 64 KiB = 1 TiB pro Archiv — drei Grössenordnungen über
	// `backup_board.max_upload_mb` (Default 500 MB) und damit nie im Weg,
	// aber eine harte Schranke gegen einen Zähler, der umläuft und Nonces
	// wiederholt.
	maxChunks = 1 << 24
)

const (
	encFlagMore  byte = 0x00
	encFlagFinal byte = 0x01
)

// ErrEncryptedNoKey meldet ein verschlüsseltes Archiv ohne konfigurierten
// Schlüssel. Bewusst ein Sentinel: der REST-Handler mappt ihn auf eine
// sprechende Antwort statt auf „restore failed".
var ErrEncryptedNoKey = errors.New(
	"Backup ist verschlüsselt, ABOARD_SECRET_KEY nicht konfiguriert")

// ErrEncryptionKeyMissing meldet `backup_board.encryption_enabled=true` ohne
// Schlüssel. Das ist ein Konfigurationsfehler des Operators: es wird KEIN
// unverschlüsseltes Backup als Ersatz geschrieben (das wäre eine stille
// Herabstufung des Schutzniveaus).
var ErrEncryptionKeyMissing = errors.New(
	"backup_board.encryption_enabled=true, aber ABOARD_SECRET_KEY ist nicht gesetzt — " +
		"es wird kein unverschlüsseltes Backup als Ersatz geschrieben")

// ErrEncryptedCorrupt meldet einen Schaden am verschlüsselten Rahmen:
// falscher Schlüssel, manipulierter Block, umsortierte oder abgeschnittene
// Datei.
//
// Die Wrap-Texte („abgeschnitten", „Block unvollständig", …) sind für das
// SERVER-LOG gedacht, nicht für die Antwort an den Client: Sie unterscheiden
// einen Rahmenfehler von einem fehlgeschlagenen Auth-Tag und wären damit ein
// schwaches Orakel für jemanden, der den Schlüssel nicht hat. Der REST-Handler
// gibt deshalb nur den nackten Sentinel-Text nach aussen (siehe
// `api/boardbackup.go`, restore-Fehlerzweig).
//
// *Korrektur des Sprint-58-Kritikers (S5):* Hier stand „Der Grund wird nie
// feiner aufgeschlüsselt" — das hielt der eigene Code an sechs Stellen nicht
// ein. Statt die Zusage zu streichen, ist jetzt geregelt, WO sie gilt.
// ErrEncryptedFremderSchluessel meldet ein Archiv, dessen Schlüssel-
// Fingerabdruck zu keinem konfigurierten Schlüssel passt (Karte #437,
// [[F-709]]).
//
// Warum ein eigener Sentinel und nicht ErrEncryptedCorrupt: Das ist die eine
// Unterscheidung, die der Betreiber wirklich braucht. „Beschädigt" schickt ihn
// zur Datei; „anderer Schlüssel" schickt ihn zu seiner Schlüsselverwaltung.
// Nach einer Rotation ist das der Unterschied zwischen einer Stunde Suche am
// falschen Ende und einem gesetzten ABOARD_SECRET_KEY_PREVIOUS.
//
// Ist das ein Orakel? Nein: Der Fingerabdruck steht ohnehin im Klartext im
// Dateikopf — wer die Datei hat, kann ihn lesen, ohne den Server zu fragen.
// Die Antwort verrät nichts, was der Anfragende nicht schon wüsste, und
// insbesondere nichts über den Schlüssel.
var ErrEncryptedFremderSchluessel = errors.New(
	"verschlüsseltes Backup wurde mit einem anderen Schlüssel erzeugt — " +
		"weder ABOARD_SECRET_KEY noch ABOARD_SECRET_KEY_PREVIOUS passt dazu")

var ErrEncryptedCorrupt = errors.New(
	"verschlüsseltes Backup beschädigt, manipuliert oder mit falschem Schlüssel erzeugt")

// Crypter kapselt die AEAD für Backup-Archive. nil-Wert bedeutet
// „keine Verschlüsselung" — alle Aufrufer prüfen darauf.
//
// GESCHRIEBEN wird immer mit `aead`/`fp` (dem aktuellen Schlüssel). GELESEN
// wird mit dem passenden aus `leser` — das sind der aktuelle plus die
// konfigurierten Vorgängerschlüssel. Die Trennung ist Absicht: Ein
// Vorgängerschlüssel soll ein altes Archiv aufmachen können, aber nie ein
// neues erzeugen, sonst verlängert sich seine Lebensdauer unbemerkt.
type Crypter struct {
	aead cipher.AEAD
	fp   []byte
	// leser: aktueller Schlüssel zuerst, danach die Vorgänger in der
	// Reihenfolge ihrer Konfiguration.
	leser []leseSchluessel
}

// leseSchluessel ist ein Schlüssel, mit dem gelesen werden darf, samt seinem
// Fingerabdruck.
type leseSchluessel struct {
	aead cipher.AEAD
	fp   []byte
}

// NewCrypter baut einen Crypter aus dem Master-Key (Roh-String aus
// ABOARD_SECRET_KEY, hex oder base64). Leerer Key → (nil, nil): das ist
// kein Fehler, sondern der reguläre Zustand eines Servers ohne
// Schlüsselkonfiguration.
func NewCrypter(masterKey string) (*Crypter, error) {
	return NewCrypterMitVorgaengern(masterKey)
}

// NewCrypterMitVorgaengern baut einen Crypter, der zusätzlich mit früheren
// Schlüsseln LESEN darf (Karte #437, [[F-709]]).
//
// Geschrieben wird ausschliesslich mit `masterKey`. Die `frueher`-Schlüssel
// kommen nur beim Entschlüsseln zum Zug, und auch dort gezielt: Ein
// V2-Archiv nennt den Fingerabdruck seines Schlüssels im Kopf, es wird also
// nicht geraten. Bei einem V1-Archiv (ohne Fingerabdruck) probiert der Leser
// die Schlüssel am ERSTEN Block der Reihe nach durch — das kostet nichts
// ausser einem AEAD-Open pro Kandidat und braucht kein Zurückspulen, weil der
// erste Block ohnehin vollständig im Speicher liegt.
//
// Leerer `masterKey` → (nil, nil), wie bisher: ein Server ohne
// Schlüsselkonfiguration. Etwaige `frueher`-Werte sind dann wirkungslos —
// ohne aktuellen Schlüssel gibt es keinen Betrieb, in dem sie gebraucht
// würden. Leere Einträge in `frueher` werden übersprungen, damit ein
// gesetztes-aber-leeres `ABOARD_SECRET_KEY_PREVIOUS` kein Fehler ist.
func NewCrypterMitVorgaengern(masterKey string, frueher ...string) (*Crypter, error) {
	if masterKey == "" {
		return nil, nil
	}
	aead, fp, err := ableiten(masterKey)
	if err != nil {
		return nil, err
	}
	c := &Crypter{aead: aead, fp: fp, leser: []leseSchluessel{{aead: aead, fp: fp}}}
	for _, k := range frueher {
		if k == "" {
			continue
		}
		vAead, vFp, err := ableiten(k)
		if err != nil {
			// Ein unbrauchbarer Vorgängerschlüssel ist ein
			// Konfigurationsfehler und wird gemeldet, nicht verschluckt:
			// Sonst startet der Server scheinbar richtig und der Betreiber
			// merkt erst beim Restore, dass sein Rettungsweg nie existierte.
			return nil, fmt.Errorf("boardbackup: Vorgängerschlüssel unbrauchbar: %w", err)
		}
		c.leser = append(c.leser, leseSchluessel{aead: vAead, fp: vFp})
	}
	return c, nil
}

// ableiten macht aus einem Master-Key den Backup-Subkey (AEAD) und dessen
// Fingerabdruck. Beides kommt aus `secretcrypto`, damit das Schlüsselmaterial
// dieses Paket nicht verlässt und es keine zweite Auffassung davon gibt, was
// ein gültiger Schlüssel ist.
func ableiten(masterKey string) (cipher.AEAD, []byte, error) {
	sub, err := secretcrypto.DeriveKey(masterKey, backupDomain)
	if err != nil {
		// Ungültiger Master-Key — dieselbe Meldung wie bei jedem anderen
		// Konsumenten. Wichtig, dass die Prüfung NICHT hinter der Ableitung
		// verschwindet: sonst akzeptiert dieses Modul einen Schlüssel, den
		// der Config-Store ablehnt, und dieselbe Fehlkonfiguration hätte
		// zwei verschiedene Wirkungen.
		return nil, nil, err
	}
	aead, err := secretcrypto.NewAEAD(sub)
	if err != nil {
		return nil, nil, err
	}
	// Der Fingerabdruck bekommt einen EIGENEN Subkey — weder den Master-Key
	// noch den Schlüssel, mit dem verschlüsselt wird.
	//
	// Die erste Fassung nahm `sub`, also den AES-Schlüssel selbst, mit der
	// Begründung, was im Klartext im Kopf steht solle mit dem Master-Key
	// wenig zu tun haben. Die Begründung trug nicht (Sprint-62-Kritiker, S1b):
	// Ein HMAC-Ausgang über den Master-Key verrät genauso wenig wie einer über
	// den Subkey, beides sind PRF-Ausgaben. Was wirklich zählt, ist die
	// Schlüsseltrennung, für die dieses Modul an anderer Stelle ausführlich
	// argumentiert: Derselbe 32-Byte-Wert diente als AES-256-GCM-Schlüssel UND
	// als HMAC-SHA256-Schlüssel. Kein bekannter Angriff — aber eine dritte
	// Ableitung kostet nichts und macht die Aussage wahr.
	fpSub, err := secretcrypto.DeriveKey(masterKey, fingerprintDomain)
	if err != nil {
		return nil, nil, err
	}
	fp, err := secretcrypto.Fingerprint(fpSub)
	if err != nil {
		return nil, nil, err
	}
	return aead, fp, nil
}

// backupDomain trennt den Backup-Schlüssel vom Master-Key. Der String ist
// Teil des Formats: eine Änderung macht alle bestehenden Archive unlesbar.
const backupDomain = "aboard-backup-v1"

// fingerprintDomain liefert den Subkey, aus dem der Schlüssel-Fingerabdruck
// im Archivkopf gebildet wird — getrennt von `backupDomain`, damit der
// AES-Schlüssel nicht zugleich HMAC-Schlüssel ist.
//
// ⚠️ DIESER STRING IST TEIL DES FORMATS — und zwar an einer besonders
// unangenehmen Stelle. `schluesselFuer` vergleicht den Kopf-Abdruck EXAKT und
// gibt bei Nichtübereinstimmung `ErrEncryptedFremderSchluessel` zurück, BEVOR
// überhaupt ein Entschlüsselungsversuch läuft. Wer diese Domäne (oder die
// Herkunft des Abdrucks, oder `secretcrypto.Fingerprint`) ändert, sorgt also
// dafür, dass bestehende V2-Archive mit UNVERÄNDERTEM Master-Key als „mit
// einem anderen Schlüssel erzeugt" abgelehnt werden — ein reines Code-Upgrade
// nähme dem Betreiber seine Backups weg, und zwar mit einer Meldung, die ihn
// zur Schlüsselverwaltung schickt statt zum Changelog.
//
// Dass die Umstellung auf diesen eigenen Subkey trotzdem möglich war, liegt
// an einer nachgeprüften Bedingung, nicht an Glück: `ABBKENC2` war zu diesem
// Zeitpunkt in KEINEM Tag enthalten (`git tag --contains 671b858` leer; in
// `v1.37.0` kommt die Zeichenfolge nur in einem Kommentar vor), und Images
// entstehen ausschliesslich aus `v*`-Tags. Es gab also kein Archiv im Umlauf.
// **Ab dem ersten Tag, der V2 ausliefert, gilt das nicht mehr.** Der Wächter
// dafür ist der eingecheckte Testvektor in
// `crypto_schluesselwechsel_test.go` (`TestSchluesselwechsel_V2FormatBleibtStabil`):
// Er wird rot, sobald sich hier etwas ändert. Wer ihn rot sieht, hat nicht
// den Test kaputtgemacht, sondern das Format.
const fingerprintDomain = "aboard-backup-fingerprint-v1"

// EncryptWriter verpackt w so, dass alles Geschriebene verschlüsselt in w
// landet. Der zurückgegebene WriteCloser MUSS geschlossen werden — erst
// Close() schreibt den Restblock mit dem Abschluss-Flag.
func (c *Crypter) EncryptWriter(w io.Writer) (io.WriteCloser, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("boardbackup: EncryptWriter ohne Schlüssel")
	}
	fileNonce := make([]byte, encNonceFileLen)
	if _, err := rand.Read(fileNonce); err != nil {
		return nil, fmt.Errorf("boardbackup: nonce: %w", err)
	}
	// Kopf in einem Stück: Magic, Fingerabdruck, Nonce. Ein halb
	// geschriebener Kopf wäre für den Leser nicht von einem beschädigten
	// Archiv zu unterscheiden.
	kopf := make([]byte, 0, encMagicLen+secretcrypto.FingerprintSize+encNonceFileLen)
	kopf = append(kopf, encMagicV2...)
	kopf = append(kopf, c.fp...)
	kopf = append(kopf, fileNonce...)
	if _, err := w.Write(kopf); err != nil {
		return nil, fmt.Errorf("boardbackup: Kopf schreiben: %w", err)
	}
	return &encWriter{
		w:         w,
		aead:      c.aead,
		fileNonce: fileNonce,
		aadSuffix: c.fp,
		buf:       make([]byte, 0, encChunkPlain),
	}, nil
}

// DecryptReader liefert einen Reader, der den verschlüsselten Strom r
// entschlüsselt. r muss am Anfang des Magic stehen.
func (c *Crypter) DecryptReader(r io.Reader) (io.Reader, error) {
	if c == nil || c.aead == nil {
		return nil, ErrEncryptedNoKey
	}
	magic := make([]byte, encMagicLen)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, fmt.Errorf("%w (Kopf unlesbar)", ErrEncryptedCorrupt)
	}

	switch {
	case bytes.Equal(magic, encMagicV2):
		rest := make([]byte, secretcrypto.FingerprintSize+encNonceFileLen)
		if _, err := io.ReadFull(r, rest); err != nil {
			return nil, fmt.Errorf("%w (Kopf unlesbar)", ErrEncryptedCorrupt)
		}
		fp := rest[:secretcrypto.FingerprintSize]
		passend := c.schluesselFuer(fp)
		if passend == nil {
			// Das ist der eigentliche Ertrag von Karte #437: Statt „beschädigt
			// oder falscher Schlüssel" sagt der Fehler, WAS los ist. Der
			// Abdruck wird mitgegeben, damit der Betreiber zwei Archive
			// einander zuordnen kann, ohne ihre Schlüssel zu kennen.
			return nil, fmt.Errorf("%w (Archiv-Abdruck %x)",
				ErrEncryptedFremderSchluessel, fp)
		}
		return &encReader{
			r:          r,
			kandidaten: []cipher.AEAD{passend},
			fileNonce:  rest[secretcrypto.FingerprintSize:],
			aadSuffix:  fp,
		}, nil

	case bytes.Equal(magic, encMagicV1):
		// Vorgängerformat ohne Fingerabdruck: Es lässt sich nicht vorab
		// entscheiden, welcher Schlüssel gemeint ist — der Leser probiert sie
		// am ersten Block durch. Genau diese Archive sind der Anlass der
		// Karte; sie existieren bereits und niemand kann sie nachträglich
		// kennzeichnen.
		fileNonce := make([]byte, encNonceFileLen)
		if _, err := io.ReadFull(r, fileNonce); err != nil {
			return nil, fmt.Errorf("%w (Kopf unlesbar)", ErrEncryptedCorrupt)
		}
		return &encReader{
			r:          r,
			kandidaten: c.leseAeads(),
			fileNonce:  fileNonce,
		}, nil

	default:
		return nil, fmt.Errorf("%w (falsches Magic)", ErrEncryptedCorrupt)
	}
}

// schluesselFuer sucht den Lese-Schlüssel mit diesem Fingerabdruck.
//
// Verglichen wird mit `subtle.ConstantTimeCompare`, obwohl der Abdruck
// öffentlich im Dateikopf steht und hier nichts Geheimes verglichen wird. Der
// Grund ist Hygiene an einer Stelle, die aussieht wie eine Schlüsselprüfung:
// Wer später etwas Geheimes durch dieselbe Funktion schickt, erbt kein
// `bytes.Equal`.
func (c *Crypter) schluesselFuer(fp []byte) cipher.AEAD {
	for _, k := range c.leser {
		if subtle.ConstantTimeCompare(k.fp, fp) == 1 {
			return k.aead
		}
	}
	return nil
}

// leseAeads liefert alle Schlüssel, mit denen gelesen werden darf — aktueller
// zuerst.
func (c *Crypter) leseAeads() []cipher.AEAD {
	out := make([]cipher.AEAD, 0, len(c.leser))
	for _, k := range c.leser {
		out = append(out, k.aead)
	}
	return out
}

// chunkNonce baut nonce_i = fileNonce(8) || uint32be(i).
func chunkNonce(fileNonce []byte, counter uint32) []byte {
	n := make([]byte, 0, encNonceFileLen+4)
	n = append(n, fileNonce...)
	n = binary.BigEndian.AppendUint32(n, counter)
	return n
}

// chunkAAD baut aad_i = uint32be(i) || flag || suffix. Der Blockindex steht
// bewusst in BEIDEN — in der Nonce (macht die Nonce eindeutig) und in der AAD
// (bindet den Block an seine Position, sodass Umsortieren auffällt).
//
// `suffix` ist im Format V2 der Schlüssel-Fingerabdruck aus dem Dateikopf und
// im Vorgängerformat V1 leer. Dadurch ist der Abdruck authentifiziert: Wer ihn
// in einer bestehenden Datei umschreibt, macht sie nicht etwa fremd, sondern
// unlesbar — und das ist die richtige Antwort, denn der Kopf gehört zum
// Archiv.
func chunkAAD(counter uint32, flag byte, suffix []byte) []byte {
	a := binary.BigEndian.AppendUint32(make([]byte, 0, 5+len(suffix)), counter)
	a = append(a, flag)
	return append(a, suffix...)
}

/* ---------------------------------------------------------------- Writer */

type encWriter struct {
	w         io.Writer
	aead      cipher.AEAD
	fileNonce []byte
	aadSuffix []byte // V2: Schlüssel-Fingerabdruck; V1: leer
	buf       []byte
	counter   uint32
	closed    bool
}

func (e *encWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("boardbackup: Write nach Close")
	}
	written := len(p)
	for len(p) > 0 {
		space := encChunkPlain - len(e.buf)
		n := len(p)
		if n > space {
			n = space
		}
		e.buf = append(e.buf, p[:n]...)
		p = p[n:]
		if len(e.buf) == encChunkPlain {
			if err := e.flush(encFlagMore); err != nil {
				return written - len(p), err
			}
		}
	}
	return written, nil
}

// Close schreibt den Restpuffer als Abschlussblock. Der Block wird IMMER
// geschrieben, auch wenn der Puffer leer ist — sonst hätte ein leeres
// Archiv keinen Abschluss-Marker und wäre von einem abgeschnittenen nicht
// zu unterscheiden.
func (e *encWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush(encFlagFinal)
}

func (e *encWriter) flush(flag byte) error {
	if e.counter >= maxChunks {
		return fmt.Errorf("boardbackup: Archiv zu gross (> %d Blöcke)", maxChunks)
	}
	sealed := e.aead.Seal(nil,
		chunkNonce(e.fileNonce, e.counter),
		e.buf,
		chunkAAD(e.counter, flag, e.aadSuffix))

	head := make([]byte, 5)
	head[0] = flag
	binary.BigEndian.PutUint32(head[1:], uint32(len(sealed)))
	if _, err := e.w.Write(head); err != nil {
		return fmt.Errorf("boardbackup: Blockkopf schreiben: %w", err)
	}
	if _, err := e.w.Write(sealed); err != nil {
		return fmt.Errorf("boardbackup: Block schreiben: %w", err)
	}
	e.buf = e.buf[:0]
	e.counter++
	return nil
}

/* ---------------------------------------------------------------- Reader */

type encReader struct {
	r io.Reader
	// kandidaten: die Schlüssel, die für dieses Archiv in Frage kommen. Bei
	// V2 genau einer (der Fingerabdruck hat entschieden), bei V1 alle
	// konfigurierten. Nach dem ersten erfolgreichen Block steht der Schlüssel
	// fest und landet in `aead`.
	kandidaten   []cipher.AEAD
	aead         cipher.AEAD
	aadSuffix    []byte
	fileNonce    []byte
	plain        []byte // entschlüsselter, noch nicht ausgelieferter Rest
	counter      uint32
	sawFinal     bool
	restGeprueft bool  // Reststrom-Prüfung nach dem Abschlussblock schon gelaufen?
	restFehler   error // deren Ergebnis — klebrig, damit jeder weitere Read dasselbe sagt
}

func (d *encReader) Read(p []byte) (int, error) {
	for len(d.plain) == 0 {
		if d.sawFinal {
			if err := d.pruefeReststrom(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}

/*
pruefeReststrom prüft EINMAL, ob hinter dem Abschlussblock noch Bytes im

	Chiffrat stehen, und macht daraus einen Fehler statt eines sauberen EOF.

	Warum das hier gehört und nicht in restore.go: `extractArchive` erkennt
	angehängte Bytes über einen eigenen `bufio.Reader` vor gzip
	(Multistream(false) + Peek(1)). Diese Konstruktion sieht nur den
	ENTPACKTEN Strom. Wer an ein `.tar.gz.enc` etwas anhängt, hängt es hinter
	den letzten verschlüsselten Block — also in eine Schicht, die der
	entpackte Strom gar nicht kennt. Ohne diese Prüfung lieferte
	`encReader.Read` nach `sawFinal` sofort `io.EOF`, las die Datei nie zu
	Ende, und `Peek(1)` sah nichts: 4096 Byte Nullen, ein kurzer Textanhang
	und ein einzelnes Byte liefen am verschlüsselten Archiv allesamt durch,
	während derselbe Müll am unverschlüsselten korrekt abgelehnt wurde
	(Sprint-61-Kritiker, gemessen).

	Der Streaming-Charakter bleibt: gelesen wird genau ein Byte, und zwar
	erst NACH dem Abschlussblock — also nachdem ohnehin alles ausgeliefert
	ist. Kein Puffern des Archivs, keine zweite Passage.

	Ein echter Lesefehler (kein EOF) wird durchgereicht statt in
	ErrEncryptedCorrupt umgedeutet — eine abgebrochene Verbindung ist kein
	Manipulationsbefund.
*/
func (d *encReader) pruefeReststrom() error {
	if d.restGeprueft {
		return d.restFehler
	}
	d.restGeprueft = true
	var b [1]byte
	n, err := io.ReadFull(d.r, b[:])
	switch {
	case n > 0:
		d.restFehler = fmt.Errorf("%w (Daten hinter dem Abschlussblock — "+
			"angehängte Bytes)", ErrEncryptedCorrupt)
	case errors.Is(err, io.EOF):
		// Sauberes Ende: nichts hinter dem Abschlussblock.
	case err != nil:
		d.restFehler = err
	}
	return d.restFehler
}

/*
oeffne entschlüsselt einen Block.

	Solange noch kein Schlüssel feststeht (erster Block eines V1-Archivs mit
	mehreren konfigurierten Schlüsseln), werden die Kandidaten der Reihe nach
	probiert; der erste, der das Auth-Tag bestätigt, wird für den Rest des
	Archivs übernommen. Danach probiert nichts mehr — ein späterer
	Blockfehler ist ein Blockfehler und kein Schlüsselproblem.

	Warum das ohne Zurückspulen geht: Der Block liegt bereits vollständig als
	`sealed` im Speicher, und `Open` verändert ihn nicht. Der Strom wird also
	genau einmal gelesen, unabhängig von der Zahl der Kandidaten — die
	Streaming-Zusage dieses Moduls bleibt unberührt.

	Warum überhaupt geraten wird: V1-Archive tragen keinen Fingerabdruck. Sie
	sind der Anlass von Karte #437 und lassen sich nicht nachträglich
	kennzeichnen. Ein V2-Archiv hat immer genau einen Kandidaten.
*/
func (d *encReader) oeffne(sealed []byte, flag byte) ([]byte, error) {
	nonce := chunkNonce(d.fileNonce, d.counter)
	aad := chunkAAD(d.counter, flag, d.aadSuffix)

	if d.aead != nil {
		pt, err := d.aead.Open(nil, nonce, sealed, aad)
		if err != nil {
			return nil, ErrEncryptedCorrupt
		}
		return pt, nil
	}

	for _, k := range d.kandidaten {
		pt, err := k.Open(nil, nonce, sealed, aad)
		if err != nil {
			continue
		}
		d.aead = k
		return pt, nil
	}

	// Kein Kandidat öffnet den ERSTEN Block. Für ein V1-Archiv ist das die
	// einzige Stelle, an der sich „falscher Schlüssel" überhaupt zeigen kann —
	// es trägt keinen Fingerabdruck, also gab es vorher nichts zu vergleichen.
	//
	// Bis Sprint 62 kam hier pauschal `ErrEncryptedCorrupt`: „beschädigt,
	// manipuliert oder mit falschem Schlüssel erzeugt". Genau die Meldung,
	// gegen die Karte #437 angetreten ist — und ausgerechnet für die
	// Archivklasse, die ihr Anlass war (Sprint-62-Kritiker, V5). ADR-0098 §10a
	// sagt die Unterscheidung ohne Einschränkung zu; im Code galt sie nur für
	// V2.
	//
	// Sagbar ist hier weniger als bei V2 — welcher Schlüssel gemeint war,
	// weiss niemand, und ein manipuliertes Archiv sieht genauso aus. Aber die
	// RICHTUNG lässt sich benennen: keiner der konfigurierten Schlüssel passt.
	// Das schickt den Betreiber zu seiner Schlüsselverwaltung statt zur Datei.
	// Der Sentinel bleibt `ErrEncryptedCorrupt` — die Aussage ist schwächer
	// als bei V2 und darf nicht so aussehen, als wäre sie gleich stark.
	if len(d.kandidaten) > 0 {
		return nil, fmt.Errorf("%w (Vorgängerformat ohne Schlüssel-Abdruck: keiner der "+
			"%d konfigurierten Schlüssel öffnet den ersten Block — prüfe "+
			"ABOARD_SECRET_KEY und ABOARD_SECRET_KEY_PREVIOUS, bevor du die Datei "+
			"verdächtigst)", ErrEncryptedCorrupt, len(d.kandidaten))
	}
	return nil, ErrEncryptedCorrupt
}

func (d *encReader) next() error {
	head := make([]byte, 5)
	if _, err := io.ReadFull(d.r, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			// Strom endet, ohne dass ein Abschlussblock kam.
			return fmt.Errorf("%w (abgeschnitten: kein Abschlussblock)", ErrEncryptedCorrupt)
		}
		return err
	}
	flag := head[0]
	if flag != encFlagMore && flag != encFlagFinal {
		return fmt.Errorf("%w (unbekanntes Blockflag)", ErrEncryptedCorrupt)
	}
	length := binary.BigEndian.Uint32(head[1:])
	if length == 0 || length > encMaxSealed {
		// Obergrenze verhindert, dass ein manipulierter Längenwert eine
		// beliebig grosse Allokation auslöst.
		return fmt.Errorf("%w (unplausible Blocklänge %d)", ErrEncryptedCorrupt, length)
	}
	sealed := make([]byte, length)
	if _, err := io.ReadFull(d.r, sealed); err != nil {
		return fmt.Errorf("%w (Block unvollständig)", ErrEncryptedCorrupt)
	}
	if d.counter >= maxChunks {
		return fmt.Errorf("%w (zu viele Blöcke)", ErrEncryptedCorrupt)
	}
	pt, err := d.oeffne(sealed, flag)
	if err != nil {
		return err
	}
	d.plain = pt
	d.counter++
	if flag == encFlagFinal {
		d.sawFinal = true
	}
	return nil
}

/* ------------------------------------------------------------ Erkennung */

// SplitEncrypted liest die ersten Bytes von r und entscheidet, ob ein
// verschlüsseltes Archiv vorliegt. Zurück kommt IMMER ein Reader, der den
// vollständigen Strom liefert (die gelesenen Bytes werden vorangestellt) —
// der Aufrufer kann also in beiden Fällen weiterlesen.
//
// Erkannt wird am Magic, NICHT an der Dateiendung: ein umbenanntes Archiv
// bleibt lesbar, und eine Datei mit `.enc`-Endung ohne Magic wird korrekt
// als unverschlüsselt behandelt statt mit einem irreführenden Schlüssel-
// fehler abgelehnt.
func SplitEncrypted(r io.Reader) (bool, io.Reader, error) {
	head := make([]byte, encMagicLen)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, nil, err
	}
	head = head[:n]
	rest := io.MultiReader(bytes.NewReader(head), r)
	// BEIDE Formatversionen zählen als verschlüsselt. Stünde hier nur V2,
	// liefe ein bestehendes V1-Archiv als „unverschlüsselt" in den
	// gzip-Leser und käme als „beschädigtes Archiv" heraus — die Karte hätte
	// dann genau den Schaden angerichtet, den sie verhindern soll.
	verschluesselt := bytes.Equal(head, encMagicV1) || bytes.Equal(head, encMagicV2)
	return verschluesselt, rest, nil
}

// IsEncryptedName sagt, ob ein Dateiname die Verschlüsselungs-Endung trägt.
// Nur für Anzeige und Namensbau — die inhaltliche Entscheidung trifft
// SplitEncrypted.
func IsEncryptedName(name string) bool {
	return len(name) >= len(EncSuffix) && name[len(name)-len(EncSuffix):] == EncSuffix
}
