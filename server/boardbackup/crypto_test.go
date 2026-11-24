package boardbackup_test

// crypto_test.go — Encryption-at-Rest für Board-Backups (#336, ADR-0098 §10).
//
// Leitfrage für jeden Test hier: WELCHE Änderung am Produktivcode macht ihn
// rot? Bei einem Krypto-Rahmenformat ist das nicht selbstverständlich —
// ein Roundtrip-Test allein ist gegenüber fast allen Fehlern blind, weil
// Ver- und Entschlüsselung denselben Defekt teilen (dieselbe Falle wie
// „Prüfer und Geprüftes teilen denselben Denkfehler"). Deshalb prüfen die
// Tests unten überwiegend die NEGATIVEN Eigenschaften: falscher Schlüssel,
// manipulierte Bytes, abgeschnittene Datei, vertauschte Blöcke.

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

// Zwei feste Testschlüssel (32 Byte hex). Fest statt zufällig, damit ein
// fehlgeschlagener Lauf reproduzierbar ist.
const (
	keyA = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	keyB = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

func crypterAus(t *testing.T, key string) *boardbackup.Crypter {
	t.Helper()
	c, err := boardbackup.NewCrypter(key)
	if err != nil {
		t.Fatalf("NewCrypter: %v", err)
	}
	if c == nil {
		t.Fatal("NewCrypter lieferte nil für nicht-leeren Schlüssel")
	}
	return c
}

// verschluesselt legt daten verschlüsselt in einem Puffer ab.
func verschluesselt(t *testing.T, c *boardbackup.Crypter, daten []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := c.EncryptWriter(&buf)
	if err != nil {
		t.Fatalf("EncryptWriter: %v", err)
	}
	if _, err := w.Write(daten); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

// entschluesselt liest einen verschlüsselten Puffer vollständig zurück.
func entschluesselt(t *testing.T, c *boardbackup.Crypter, roh []byte) ([]byte, error) {
	t.Helper()
	r, err := c.DecryptReader(bytes.NewReader(roh))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	_, err = out.ReadFrom(r)
	return out.Bytes(), err
}

/* ── Rahmenformat: positive Eigenschaften ──────────────────────────── */

func TestCrypto_Roundtrip_MehrereBlockgroessen(t *testing.T) {
	c := crypterAus(t, keyA)
	// Grössen um die Blockgrenze (64 KiB) herum — der interessante Bereich:
	// leer, ein Byte, exakt ein Block, ein Block plus eins, mehrere Blöcke.
	for _, n := range []int{0, 1, 1000, 65535, 65536, 65537, 200000} {
		daten := bytes.Repeat([]byte("aBoard"), n/6+1)[:n]
		roh := verschluesselt(t, c, daten)
		zurueck, err := entschluesselt(t, c, roh)
		if err != nil {
			t.Fatalf("n=%d: entschlüsseln: %v", n, err)
		}
		if !bytes.Equal(daten, zurueck) {
			t.Fatalf("n=%d: Roundtrip verändert die Daten (%d zurück)", n, len(zurueck))
		}
	}
}

func TestCrypto_CiphertextEnthaeltKlartextNicht(t *testing.T) {
	c := crypterAus(t, keyA)
	geheim := []byte("GEHEIMER-BOARD-INHALT-4711")
	roh := verschluesselt(t, c, bytes.Repeat(geheim, 100))
	if bytes.Contains(roh, geheim) {
		t.Fatal("Klartext steht unverschlüsselt im Ciphertext")
	}
}

func TestCrypto_ZweiLaeufeUnterscheidenSich(t *testing.T) {
	// Gleiche Daten, gleicher Schlüssel → unterschiedlicher Ciphertext,
	// weil die Datei-Nonce zufällig ist. Wäre sie fest, würde eine
	// Nonce-Wiederverwendung bei AES-GCM den Klartext angreifbar machen.
	c := crypterAus(t, keyA)
	daten := []byte("immer dasselbe")
	a := verschluesselt(t, c, daten)
	b := verschluesselt(t, c, daten)
	if bytes.Equal(a, b) {
		t.Fatal("zwei Verschlüsselungen sind byte-gleich — Nonce ist nicht zufällig")
	}
}

func TestCrypto_SplitEncrypted_ErkenntBeideRichtungen(t *testing.T) {
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, []byte("x"))

	ist, rest, err := boardbackup.SplitEncrypted(bytes.NewReader(roh))
	if err != nil || !ist {
		t.Fatalf("verschlüsselter Strom nicht erkannt: ist=%v err=%v", ist, err)
	}
	// SplitEncrypted muss den vollständigen Strom zurückgeben, sonst fehlt
	// dem nachgelagerten Leser das Magic.
	alles, _ := entschluesselt(t, c, mussLesen(t, rest))
	if string(alles) != "x" {
		t.Fatalf("Strom nach SplitEncrypted unvollständig: %q", alles)
	}

	klar := []byte("das ist kein verschlüsseltes Archiv, sondern gzip oder sonstwas")
	ist2, rest2, err := boardbackup.SplitEncrypted(bytes.NewReader(klar))
	if err != nil {
		t.Fatalf("SplitEncrypted klar: %v", err)
	}
	if ist2 {
		t.Fatal("unverschlüsselter Strom als verschlüsselt erkannt")
	}
	if !bytes.Equal(mussLesen(t, rest2), klar) {
		t.Fatal("unverschlüsselter Strom wurde beim Prüfen beschädigt")
	}

	// Kürzer als das Magic — darf nicht in einen Fehler laufen.
	ist3, rest3, err := boardbackup.SplitEncrypted(bytes.NewReader([]byte("ab")))
	if err != nil || ist3 {
		t.Fatalf("Kurzstrom: ist=%v err=%v", ist3, err)
	}
	if string(mussLesen(t, rest3)) != "ab" {
		t.Fatal("Kurzstrom wurde beschädigt")
	}
}

func mussLesen(t *testing.T, r interface{ Read([]byte) (int, error) }) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("lesen: %v", err)
	}
	return buf.Bytes()
}

/*
kopfV2 ist die Länge des Dateikopfs im aktuellen Format: Magic(8) +

	Schlüssel-Fingerabdruck(8) + fileNonce(8). Die Tests unten schneiden an
	festen Offsets im Chiffrat herum; bis Sprint 62 stand dort dreimal `8 + 8`
	ausgeschrieben, und genau diese Zahlen sind beim Formatwechsel (Karte #437)
	still falsch geworden — zwei der Tests prüften danach eine andere Stelle als
	gemeint, einer scheiterte an der Aufbau-Zusicherung. Als benannte Konstante
	wandert der Versatz beim nächsten Mal an einer Stelle mit.
*/
const kopfV2 = 8 + 8 + 8

/* ── Rahmenformat: negative Eigenschaften (der eigentliche Wert) ───── */

// Karte #437 ([[F-709]]): Bis Sprint 62 meldete dieser Fall
// ErrEncryptedCorrupt — „beschädigt, manipuliert ODER mit falschem Schlüssel
// erzeugt". Der Betreiber konnte die Fälle nicht trennen und verdächtigte
// zuerst die Datei. Seit dem Fingerabdruck im Kopf ist es eine eigene Aussage.
func TestCrypto_FalscherSchluessel(t *testing.T) {
	roh := verschluesselt(t, crypterAus(t, keyA), []byte("inhalt"))
	_, err := entschluesselt(t, crypterAus(t, keyB), roh)
	if !errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) {
		t.Fatalf("falscher Schlüssel: erwartet ErrEncryptedFremderSchluessel, bekam %v", err)
	}
	// Und ausdrücklich NICHT der Sammel-Sentinel: Wer beide Fehler
	// ineinander verschachtelt, macht die Unterscheidung wieder zunichte,
	// ohne dass ein Test es merkt.
	if errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("der Schlüsselfehler ist zugleich ErrEncryptedCorrupt — dann "+
			"nützt die Unterscheidung dem Betreiber nichts: %v", err)
	}
}

func TestCrypto_ManipuliertesByteFaelltAuf(t *testing.T) {
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, bytes.Repeat([]byte("A"), 5000))
	// Ein Byte mitten im Ciphertext kippen (hinter Magic + Nonce + Blockkopf).
	kaputt := append([]byte(nil), roh...)
	kaputt[len(kaputt)/2] ^= 0x01
	if _, err := entschluesselt(t, c, kaputt); !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("manipuliertes Byte: erwartet ErrEncryptedCorrupt, bekam %v", err)
	}
}

func TestCrypto_AbgeschnittenMittenImBlock(t *testing.T) {
	// Schnitt MITTEN in einen Block. Das fängt bereits die Längenprüfung des
	// Blockrahmens ab — dieser Test sichert also den Rahmen, NICHT das
	// Abschluss-Flag. Die Trennung ist bewusst: eine Gegenprobe, die das
	// Abschluss-Flag ausbaut, lässt diesen Test grün; rot wird nur der Test
	// darunter. Stünde beides in einem Testfall, sähe die Deckung besser aus,
	// als sie ist.
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, bytes.Repeat([]byte("B"), 200000))
	kurz := roh[:len(roh)/2]
	_, err := entschluesselt(t, c, kurz)
	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("abgeschnitten: erwartet ErrEncryptedCorrupt, bekam %v", err)
	}
	if !strings.Contains(err.Error(), "unvollständig") {
		t.Fatalf("erwartet wird der Rahmen-Fehler „Block unvollständig\", bekam: %v", err)
	}
}

func TestCrypto_LetzterBlockAlleinIstNichtGenug(t *testing.T) {
	// Härtere Variante des Abschneide-Tests: Wir schneiden NUR den letzten
	// Block ab. Der Rest besteht aus lauter gültigen Blöcken; ohne
	// Abschluss-Flag im Format wäre das nicht von einer vollständigen Datei
	// zu unterscheiden.
	c := crypterAus(t, keyA)
	daten := bytes.Repeat([]byte("C"), 130000) // ≥ 2 volle Blöcke + Rest
	roh := verschluesselt(t, c, daten)
	// Ein voller Block ist 5 (Kopf) + 65536 + 16 (Tag) Bytes.
	const blockAufDrahtl = 5 + 65536 + 16
	nurErsterBlock := roh[:kopfV2+blockAufDrahtl]
	err := ersterFehler(t, c, nurErsterBlock)
	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("fehlender Abschlussblock: erwartet ErrEncryptedCorrupt, bekam %v", err)
	}
	// Ausdrücklich die Abschluss-Meldung, nicht irgendeine: Nur so kann der
	// Test nicht versehentlich über den Rahmen-Fehler des Nachbartests grün
	// bzw. rot werden.
	if !strings.Contains(err.Error(), "abgeschnitten") {
		t.Fatalf("erwartet wird die Abschlussblock-Meldung, bekam: %v", err)
	}
}

// ersterFehler liest den Strom und liefert den Fehler (nil bei Erfolg).
func ersterFehler(t *testing.T, c *boardbackup.Crypter, roh []byte) error {
	t.Helper()
	_, err := entschluesselt(t, c, roh)
	return err
}

func TestCrypto_VertauschteBloeckeFallenAuf(t *testing.T) {
	// Der Blockindex steckt in Nonce UND AAD. Wer zwei Blöcke tauscht,
	// bekommt bei beiden einen Auth-Fehler.
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, bytes.Repeat([]byte("D"), 200000))
	const blockAufDraht = 5 + 65536 + 16
	kopf := kopfV2
	b1 := roh[kopf : kopf+blockAufDraht]
	b2 := roh[kopf+blockAufDraht : kopf+2*blockAufDraht]
	getauscht := append([]byte(nil), roh[:kopf]...)
	getauscht = append(getauscht, b2...)
	getauscht = append(getauscht, b1...)
	getauscht = append(getauscht, roh[kopf+2*blockAufDraht:]...)
	if _, err := entschluesselt(t, c, getauscht); !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("vertauschte Blöcke: erwartet ErrEncryptedCorrupt, bekam %v", err)
	}
}

func TestCrypto_UnplausibleBlocklaengeAllokiertNicht(t *testing.T) {
	// Manipulierter Längenwert: der Leser darf daraus keine Gigabyte-
	// Allokation machen, sondern muss ablehnen.
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, []byte("klein"))
	kaputt := append([]byte(nil), roh...)
	// Blockkopf liegt direkt nach dem Dateikopf: flag(1) + len(4).
	kaputt[kopfV2+1] = 0xFF
	kaputt[kopfV2+2] = 0xFF
	_, err := entschluesselt(t, c, kaputt)
	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("unplausible Länge: erwartet ErrEncryptedCorrupt, bekam %v", err)
	}
	// Ausdrücklich DIESE Meldung. Ohne die Schranke läuft der Code in eine
	// 4-GB-Allokation und scheitert danach an `io.ReadFull` — mit demselben
	// Sentinel, nur über „Block unvollständig". Der Test bliebe grün, obwohl
	// der Schutz weg ist (Sprint-58-Kritiker, V1).
	if !strings.Contains(err.Error(), "unplausible Blocklänge") {
		t.Fatalf("erwartet wird die Längen-Schranke, bekam: %v", err)
	}
}

// V2 (Sprint-58-Kritiker): Die AAD ist die EINZIGE Absicherung gegen einen
// stillen Trunkierungs-Angriff — und war von keinem Test berührt.
//
// Der Blockindex steht in Nonce UND AAD; ein Blocktausch fällt schon an der
// Nonce auf. Das Abschluss-FLAG dagegen steht nur in der AAD. Wer es im
// Blockkopf von 0x00 auf 0x01 kippt, bekäme ohne AAD einen sauber
// entschlüsselten Block, `sawFinal` würde gesetzt — und der Leser lieferte
// die ersten 64 KiB als vollständiges Archiv aus. Ersetzt man `chunkAAD`
// beidseitig durch nil, bleiben alle anderen Krypto-Tests grün; nur dieser
// wird rot.
func TestCrypto_FlagManipulationFaelltAuf(t *testing.T) {
	c := crypterAus(t, keyA)
	daten := bytes.Repeat([]byte("E"), 130000) // ≥ 2 volle Blöcke
	roh := verschluesselt(t, c, daten)

	// Blockkopf des ERSTEN Blocks: flag(1) direkt hinter dem Dateikopf.
	const kopf = kopfV2
	if roh[kopf] != 0x00 {
		t.Fatalf("Aufbau: erster Block sollte flag=0x00 tragen, hat %#x", roh[kopf])
	}
	kaputt := append([]byte(nil), roh...)
	kaputt[kopf] = 0x01 // „das ist der letzte Block"

	zurueck, err := entschluesselt(t, c, kaputt)
	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("Flag-Manipulation: erwartet ErrEncryptedCorrupt, bekam %v (len=%d)",
			err, len(zurueck))
	}
	// Und ganz sicher darf kein Teil-Klartext herauskommen.
	if len(zurueck) > 0 {
		t.Fatalf("trotz Manipulation wurden %d Bytes Klartext geliefert", len(zurueck))
	}
}

func TestCrypto_OhneSchluesselKeinLeser(t *testing.T) {
	var leer *boardbackup.Crypter
	if _, err := leer.DecryptReader(bytes.NewReader([]byte("x"))); !errors.Is(err, boardbackup.ErrEncryptedNoKey) {
		t.Fatalf("nil-Crypter: erwartet ErrEncryptedNoKey, bekam %v", err)
	}
}

func TestCrypto_NewCrypter_LeerUndUngueltig(t *testing.T) {
	c, err := boardbackup.NewCrypter("")
	if err != nil || c != nil {
		t.Fatalf("leerer Schlüssel: erwartet (nil, nil), bekam (%v, %v)", c, err)
	}
	if _, err := boardbackup.NewCrypter("viel zu kurz"); err == nil {
		t.Fatal("ungültiger Schlüssel wurde akzeptiert")
	}
	// Ein hex-Schlüssel mit falscher Länge muss ebenfalls scheitern —
	// die Validierung liegt in secretcrypto und wird hier nur mitgeprüft.
	if _, err := boardbackup.NewCrypter(hex.EncodeToString([]byte("nur16bytes......"))); err == nil {
		t.Fatal("16-Byte-Schlüssel wurde akzeptiert, erwartet werden 32")
	}
}

/* ── ResolveCrypter: die Schreibseiten-Entscheidung ────────────────── */

func TestResolveCrypter_Faelle(t *testing.T) {
	basis := boardbackup.DefaultConfig()

	// 1. Verschlüsselung aus → kein Crypter, kein Fehler.
	if c, err := boardbackup.ResolveCrypter(basis, keyA); c != nil || err != nil {
		t.Fatalf("aus: erwartet (nil,nil), bekam (%v,%v)", c, err)
	}

	// 2. An, Schlüssel da → Crypter.
	an := basis
	an.EncryptionEnabled = true
	c, err := boardbackup.ResolveCrypter(an, keyA)
	if err != nil || c == nil {
		t.Fatalf("an+Schlüssel: erwartet Crypter, bekam (%v,%v)", c, err)
	}

	// 3. An, KEIN Schlüssel → Fehler. Das ist der wichtigste Fall: es darf
	//    NICHT ersatzweise unverschlüsselt geschrieben werden.
	if _, err := boardbackup.ResolveCrypter(an, ""); !errors.Is(err, boardbackup.ErrEncryptionKeyMissing) {
		t.Fatalf("an ohne Schlüssel: erwartet ErrEncryptionKeyMissing, bekam %v", err)
	}

	// 4. Unbekannte Schlüsselquelle → Fehler statt stillem Ignorieren.
	fremd := an
	fremd.EncryptionKeySource = "backup_specific"
	if _, err := boardbackup.ResolveCrypter(fremd, keyA); !errors.Is(err, boardbackup.ErrUnknownKeySource) {
		t.Fatalf("fremde Quelle: erwartet ErrUnknownKeySource, bekam %v", err)
	}

	// 5. Leere Quelle wird wie master_key behandelt (Altbestand ohne den Key).
	leer := an
	leer.EncryptionKeySource = ""
	if c, err := boardbackup.ResolveCrypter(leer, keyA); err != nil || c == nil {
		t.Fatalf("leere Quelle: erwartet Crypter, bekam (%v,%v)", c, err)
	}
}

/* ── Zusammenspiel mit Builder und Restore ─────────────────────────── */

func TestBackup_Verschluesselt_DateinameUndInhalt(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-enc", "Verschlüsselt")
	out := filepath.Join(env.backupDir, "b-enc", "boardbackup-b-enc-20260820-120000-deadbeef.tar.gz")

	res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID: "b-enc", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir, Out: out,
		Crypter: crypterAus(t, keyA),
	})
	if err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}
	if !res.Encrypted {
		t.Error("Result.Encrypted ist false trotz gesetztem Crypter")
	}
	if res.Path != out+boardbackup.EncSuffix {
		t.Errorf("Result.Path = %q, erwartet %q", res.Path, out+boardbackup.EncSuffix)
	}
	if !strings.HasSuffix(res.Filename, ".tar.gz.enc") {
		t.Errorf("Result.Filename = %q, erwartet .tar.gz.enc-Endung", res.Filename)
	}
	// Die Datei unter dem UNVERSCHLÜSSELTEN Namen darf es nicht geben —
	// sonst läge ein Klartext-Archiv daneben.
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("es liegt zusätzlich eine Datei ohne .enc-Endung auf der Platte")
	}
	roh, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("lesen: %v", err)
	}
	// gzip-Magic darf im verschlüsselten Archiv nicht am Anfang stehen.
	if len(roh) > 2 && roh[0] == 0x1f && roh[1] == 0x8b {
		t.Fatal("Archiv beginnt mit gzip-Magic — es ist nicht verschlüsselt")
	}
	if bytes.Contains(roh, []byte("Verschlüsselt")) {
		t.Fatal("Board-Titel steht im Klartext in der Datei")
	}
}

func TestBackup_SHABleibtAufDemKlartext(t *testing.T) {
	// Invariante aus ADR-0098 §2: Der SHA-256 läuft über den ENTSCHLÜSSELTEN
	// Inhalt. Zwei Backups desselben Board-Zustands — eines verschlüsselt,
	// eines nicht — müssen denselben SHA melden. Wäre der Hash über die
	// Ciphertext-Bytes gerechnet, wären sie verschieden (zufällige Nonce).
	env := newTestEnv(t)
	env.seedBoard(t, "b-sha", "Gleicher Inhalt")

	klar, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID: "b-sha", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                filepath.Join(env.backupDir, "b-sha", "klar.tar.gz"),
	})
	if err != nil {
		t.Fatalf("BackupBoard klar: %v", err)
	}
	enc, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID: "b-sha", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                filepath.Join(env.backupDir, "b-sha", "enc.tar.gz"),
		Crypter:            crypterAus(t, keyA),
	})
	if err != nil {
		t.Fatalf("BackupBoard verschlüsselt: %v", err)
	}
	if klar.SHA256 != enc.SHA256 {
		t.Fatalf("SHA weicht ab: klar=%s verschlüsselt=%s — der Hash läuft nicht über den Klartext",
			klar.SHA256, enc.SHA256)
	}
}

func TestRestore_VerschluesseltesArchiv(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-src", "Quelle")
	env.seedBoard(t, "b-ziel", "Ziel")
	env.seedAttachment(t, "b-src", "c1", "a1", "datei.txt", []byte("Anhang-Inhalt"))

	out := filepath.Join(env.backupDir, "b-src", "sicherung.tar.gz")
	res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID: "b-src", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir, Out: out,
		Crypter: crypterAus(t, keyA),
	})
	if err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}

	// 1. Mit Schlüssel → geht durch.
	if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: res.Path, Mode: boardbackup.RestoreOverwrite,
		TargetBoardID: "b-ziel", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir,
		Crypter:            crypterAus(t, keyA),
	}); err != nil {
		t.Fatalf("Restore mit Schlüssel: %v", err)
	}
	b, err := env.boards.Get(context.Background(), "b-ziel")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Contains(b.StateJSON, []byte("Card 3")) {
		t.Error("Board-Inhalt nach Restore unvollständig")
	}
	// Anhang muss ebenfalls angekommen sein.
	att := filepath.Join(env.attDir, "b-ziel", "c1", "a1__datei.txt")
	if body, rerr := os.ReadFile(att); rerr != nil || string(body) != "Anhang-Inhalt" {
		t.Errorf("Anhang nach Restore: %v / %q", rerr, string(body))
	}

	// 2. OHNE Schlüssel → klarer Fehler, kein stiller Fehlschlag.
	_, err = boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: res.Path, Mode: boardbackup.RestoreOverwrite,
		TargetBoardID: "b-ziel", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir,
	})
	if !errors.Is(err, boardbackup.ErrEncryptedNoKey) {
		t.Fatalf("Restore ohne Schlüssel: erwartet ErrEncryptedNoKey, bekam %v", err)
	}

	// 3. Mit FALSCHEM Schlüssel → seit Karte #437 die eigene Aussage
	//    „anderer Schlüssel", nicht mehr der Sammel-Korruptionsfehler und
	//    erst recht nicht „Schlüssel fehlt". Der Unterschied ist der ganze
	//    Zweck des Fingerabdrucks: Der Betreiber soll nach seinem Schlüssel
	//    suchen, nicht nach einem Schaden an der Datei.
	_, err = boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: res.Path, Mode: boardbackup.RestoreOverwrite,
		TargetBoardID: "b-ziel", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir,
		Crypter:            crypterAus(t, keyB),
	})
	if !errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) {
		t.Fatalf("Restore mit falschem Schlüssel: erwartet ErrEncryptedFremderSchluessel, bekam %v", err)
	}
}

func TestRestore_UnverschluesseltMitGesetztemSchluessel(t *testing.T) {
	// Rückwärtskompatibilität: Ein Server MIT Schlüssel muss Alt-Archive
	// OHNE Verschlüsselung weiterhin einspielen können. Die Erkennung läuft
	// über das Magic, nicht über die Konfiguration.
	env := newTestEnv(t)
	env.seedBoard(t, "b-alt", "Altbestand")
	env.seedBoard(t, "b-ziel2", "Ziel")
	out := filepath.Join(env.backupDir, "b-alt", "alt.tar.gz")
	res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID: "b-alt", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir, Out: out,
	})
	if err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}
	if res.Encrypted || res.Path != out {
		t.Fatalf("unverschlüsselter Lauf meldet Encrypted=%v Path=%q", res.Encrypted, res.Path)
	}
	if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: out, Mode: boardbackup.RestoreOverwrite,
		TargetBoardID: "b-ziel2", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir,
		Crypter:            crypterAus(t, keyA),
	}); err != nil {
		t.Fatalf("Alt-Archiv mit gesetztem Schlüssel: %v", err)
	}
}

/* ── Rotation und Liste dürfen .enc nicht übersehen ────────────────── */

func TestRotationUndListe_ErfassenVerschluesselteArchive(t *testing.T) {
	// Der stille Fehlerfall: Ein Suffix-Vergleich, der `.tar.gz.enc` nicht
	// kennt, meldet keinen Fehler — er findet nur nichts. Folge wäre eine
	// volllaufende Platte und ein leeres Backup-Register in der UI.
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "b1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	namen := []string{
		"boardbackup-b1-20260801-020000-aaaaaaaa.tar.gz.enc",
		"boardbackup-b1-20260802-020000-bbbbbbbb.tar.gz.enc",
		"boardbackup-b1-20260803-020000-cccccccc.tar.gz",
		"nicht-unseres.txt",
	}
	for i, n := range namen {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// mtime staffeln, damit die Sortierung deterministisch ist.
		zeit := testZeit(i)
		if err := os.Chtimes(filepath.Join(dir, n), zeit, zeit); err != nil {
			t.Fatal(err)
		}
	}

	liste, err := boardbackup.ListBackups(tmp, "b1")
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(liste) != 3 {
		t.Fatalf("ListBackups fand %d Einträge, erwartet 3 (zwei .enc + eines klar)", len(liste))
	}
	verschl := 0
	for _, e := range liste {
		if e.Encrypted {
			verschl++
		}
		if !strings.HasPrefix(e.Filename, "boardbackup-b1-") {
			t.Errorf("fremde Datei in der Liste: %s", e.Filename)
		}
	}
	if verschl != 2 {
		t.Errorf("Encrypted-Flag: %d von 3, erwartet 2", verschl)
	}

	geloescht, err := boardbackup.Rotate(tmp, "b1", 1)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if geloescht != 2 {
		t.Fatalf("Rotate löschte %d Dateien, erwartet 2", geloescht)
	}
	// Die fremde Datei bleibt unangetastet.
	if _, err := os.Stat(filepath.Join(dir, "nicht-unseres.txt")); err != nil {
		t.Error("Rotate hat eine fremde Datei gelöscht")
	}
}

// testZeit staffelt mtimes deterministisch (i=0 am ältesten).
func testZeit(i int) time.Time {
	return time.Date(2026, 8, 1+i, 2, 0, 0, 0, time.UTC)
}

// F-2831: Die Wackelursache war die Uhr — tar-Header trugen time.Now() (auf
// Sekunden gerundet), der SHA lief darüber. Dieser Test erzwingt, was der
// Zufall nur selten tat: Zwischen den beiden Sicherungen liegt eine
// Sekundengrenze. Gleicher Zustand muss denselben SHA liefern.
func TestBackup_SHAUnabhaengigVonDerUhr_F2831(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-uhr", "Gleicher Inhalt")
	sichern := func(name string) string {
		res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
			BoardID: "b-uhr", Boards: env.boards, ACL: env.acl,
			AttachmentsBaseDir: env.attDir,
			Out:                filepath.Join(env.backupDir, "b-uhr", name),
		})
		if err != nil {
			t.Fatalf("BackupBoard %s: %v", name, err)
		}
		return res.SHA256
	}
	erst := sichern("eins.tar.gz")
	// bis sicher in die nächste Sekunde warten (nicht nur 1 s: die erste
	// Sicherung kann kurz vor oder nach der Grenze begonnen haben)
	start := time.Now()
	for time.Now().Unix() == start.Unix() {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(1100 * time.Millisecond)
	if zweit := sichern("zwei.tar.gz"); zweit != erst {
		t.Fatalf("SHA hängt an der Uhr: %s != %s", erst, zweit)
	}
}
