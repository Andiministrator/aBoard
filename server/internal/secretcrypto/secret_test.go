package secretcrypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestNew_HexAndBase64(t *testing.T) {
	k := randomKey(t)
	for name, s := range map[string]string{
		"hex":           hex.EncodeToString(k),
		"base64-std":    base64.StdEncoding.EncodeToString(k),
		"base64-rawstd": base64.RawStdEncoding.EncodeToString(k),
		"base64-url":    base64.URLEncoding.EncodeToString(k),
		"base64-rawurl": base64.RawURLEncoding.EncodeToString(k),
	} {
		t.Run(name, func(t *testing.T) {
			box, err := New(s)
			if err != nil {
				t.Fatalf("New(%s): %v", name, err)
			}
			ct, err := box.SealString("hallo welt")
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			pt, err := box.OpenString(ct)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if pt != "hallo welt" {
				t.Fatalf("Roundtrip: %q", pt)
			}
		})
	}
}

func TestNew_RejectsBadKey(t *testing.T) {
	cases := map[string]string{
		"leer":                  "",
		"kurz":                  "abc",
		"falscheLaenge":         hex.EncodeToString([]byte("kurz-key")),
		"weder-hex-noch-base64": "@@@@@@@",
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(s)
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("erwartet ErrInvalidKey, got %v", err)
			}
		})
	}
}

func TestSealOpen_RoundTrip(t *testing.T) {
	box, err := New(hex.EncodeToString(randomKey(t)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, plain := range [][]byte{
		nil,
		[]byte(""),
		[]byte("kurz"),
		bytes.Repeat([]byte("a"), 4096),
		{0x00, 0xff, 0x10, 0x20},
	} {
		ct, err := box.Seal(plain)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		pt, err := box.Open(ct)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if !bytes.Equal(pt, plain) && !(len(pt) == 0 && len(plain) == 0) {
			t.Fatalf("Roundtrip mismatch: got %x want %x", pt, plain)
		}
	}
}

func TestSeal_NotDeterministic(t *testing.T) {
	box, _ := New(hex.EncodeToString(randomKey(t)))
	a, _ := box.SealString("gleicher input")
	b, _ := box.SealString("gleicher input")
	if a == b {
		t.Fatal("Seal sollte zufällige Nonce nutzen (a == b deutet auf fehlende Nonce-Randomisierung)")
	}
}

func TestOpen_RejectsCorruptCiphertext(t *testing.T) {
	box, _ := New(hex.EncodeToString(randomKey(t)))
	good, _ := box.SealString("geheim")

	cases := map[string]string{
		"leer":          "",
		"kein-base64":   "@@@nicht_b64@@@",
		"zu-kurz":       base64.StdEncoding.EncodeToString([]byte("abc")),
		"manipuliert":   flipFirstByteB64(t, good),
		"abgeschnitten": good[:len(good)/2],
	}
	for name, ct := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := box.Open(ct)
			if !errors.Is(err, ErrCiphertextCorrupt) {
				t.Fatalf("erwartet ErrCiphertextCorrupt, got %v", err)
			}
		})
	}
}

func TestOpen_RejectsWrongKey(t *testing.T) {
	boxA, _ := New(hex.EncodeToString(randomKey(t)))
	boxB, _ := New(hex.EncodeToString(randomKey(t)))
	ct, _ := boxA.SealString("nur fuer A")
	_, err := boxB.Open(ct)
	if !errors.Is(err, ErrCiphertextCorrupt) {
		t.Fatalf("erwartet ErrCiphertextCorrupt bei falschem Key, got %v", err)
	}
}

func TestNew_LeerSchluesselNachTrim(t *testing.T) {
	// Whitespace-only Eingaben sollen wie leer behandelt werden, weil
	// fehlerhafte Compose-Configs gerne Leerzeichen-Schlüssel produzieren.
	for _, s := range []string{" ", "  ", "\t", "\n"} {
		if !strings.HasPrefix(strings.TrimSpace(s), "") {
			continue
		}
		_, err := New(s)
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Whitespace %q sollte ErrInvalidKey ergeben, got %v", s, err)
		}
	}
}

func flipFirstByteB64(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) == 0 {
		return b64
	}
	raw[0] ^= 0xff
	return base64.StdEncoding.EncodeToString(raw)
}

/* ── Fingerprint (Karte #437, [[F-709]]) ───────────────────────────── */

func TestFingerprint_StabilUeberKodierungen(t *testing.T) {
	// Der Grund, warum diese Funktion hier steht und nicht beim Aufrufer:
	// Ein Operator, der denselben Schlüssel einmal hex und einmal base64
	// notiert, bekäme sonst zwei Abdrücke — und hielte seine eigenen Archive
	// für fremde.
	k := randomKey(t)
	a, err := Fingerprint(hex.EncodeToString(k))
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	b, err := Fingerprint(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("hex %x != base64 %x", a, b)
	}
	if len(a) != FingerprintSize {
		t.Fatalf("Länge %d, erwartet %d", len(a), FingerprintSize)
	}
}

func TestFingerprint_UnterscheidetSchluessel(t *testing.T) {
	a, err := Fingerprint(hex.EncodeToString(randomKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fingerprint(hex.EncodeToString(randomKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("zwei verschiedene Schlüssel haben denselben Abdruck")
	}
}

func TestFingerprint_IstNichtDerSchluessel(t *testing.T) {
	// Der Abdruck steht im Klartext in jedem Archivkopf. Wäre er ein
	// Präfix des Schlüssels, gäbe dieser Kopf 8 Schlüsselbytes preis.
	k := randomKey(t)
	fp, err := Fingerprint(hex.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(fp, k[:FingerprintSize]) {
		t.Fatal("der Abdruck ist ein Präfix des Schlüssels")
	}
	if bytes.Equal(fp, k[len(k)-FingerprintSize:]) {
		t.Fatal("der Abdruck ist ein Suffix des Schlüssels")
	}
}

func TestFingerprint_TrenntSichVonDeriveKey(t *testing.T) {
	// Der Abdruck darf mit keiner Verschlüsselungs-Ableitung zusammenfallen,
	// sonst stünden acht Bytes eines regulär ableitbaren Subkeys im Klartext
	// in jedem Archivkopf.
	//
	// Dieser Test hat den Fehler gefunden, für den er geschrieben wurde: Die
	// erste Fassung von Fingerprint war HMAC(key, fingerprintDomain) und
	// damit Byte für Byte DeriveKey mit derselben Domäne. Ein eigener
	// Domänen-String reicht dafür NICHT — er ist ein Wert, kein
	// Strukturmerkmal, und DeriveKey nimmt jeden Wert entgegen. Erst das
	// Typ-Byte trennt die beiden Konstruktionen.
	//
	// Geprüft wird gegen die Domänen, die im Repo real vorkommen, plus die
	// Fingerabdruck-Domäne selbst (der Fall, der schiefging).
	key := hex.EncodeToString(randomKey(t))
	fp, err := Fingerprint(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{
		"aboard-backup-v1",
		"aboard-backup-fingerprint-v1",
		"aboard-key-fingerprint-v1",
	} {
		sub, err := DeriveKey(key, domain)
		if err != nil {
			t.Fatalf("DeriveKey(%q): %v", domain, err)
		}
		subBytes, err := hex.DecodeString(sub)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(fp, subBytes[:FingerprintSize]) {
			t.Fatalf("der Abdruck ist ein Präfix des Subkeys für Domäne %q", domain)
		}
	}

	// Der Fall, an dem die erste Fassung scheiterte (Sprint-62-Kritiker, S1):
	// Mit einem Typ-Byte allein war die Trennung nur eine Absprache — wer
	// `"\x01" + fingerprintDomain` als Domäne wählte, bekam denselben Wert
	// heraus. Nachgerechnet und belegt. Heute kann diese Domäne gar nicht
	// mehr angenommen werden, und GENAU DAS ist die Trennung. Der Test prüft
	// deshalb nicht „andere Domäne", sondern „diese Domäne ist unmöglich".
	if _, err := DeriveKey(key, "\x01"+fingerprintDomain); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("DeriveKey akzeptiert eine Domäne mit Steuerzeichen — damit ist der "+
			"Fingerabdruck wieder als Subkey-Präfix herstellbar: err=%v", err)
	}
}

func TestDeriveKey_LehntUnzulaessigeDomaenenAb(t *testing.T) {
	// Allowlist statt Blockliste: Eine Blockliste müsste bei jedem neuen
	// Sonderfall nachgezogen werden, und genau daran ist die erste Fassung
	// der Fingerabdruck-Trennung gescheitert.
	key := hex.EncodeToString(randomKey(t))
	for name, domain := range map[string]string{
		"leer":                "",
		"Steuerzeichen vorn":  "\x01aboard-backup-v1",
		"Steuerzeichen innen": "aboard\x00backup",
		"Leerzeichen":         "aboard backup",
		"Doppelpunkt":         "aboard:backup",
		"Nicht-ASCII":         "aboard-bäckup",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DeriveKey(key, domain); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("Domäne %q wurde akzeptiert, erwartet ErrInvalidKey (got %v)", domain, err)
			}
		})
	}

	// Und die Gegenrichtung — sonst prüft der Test nur, dass alles abgelehnt wird.
	for _, domain := range []string{"aboard-backup-v1", "aboard-backup-fingerprint-v1", "a", "A_b.c-1"} {
		if _, err := DeriveKey(key, domain); err != nil {
			t.Fatalf("gültige Domäne %q wurde abgelehnt: %v", domain, err)
		}
	}
}

func TestFingerprint_LehntUngueltigeSchluesselAb(t *testing.T) {
	for name, key := range map[string]string{
		"leer":     "",
		"kein hex": "keinhex",
		"zu kurz":  hex.EncodeToString(make([]byte, 16)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Fingerprint(key); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("erwartet ErrInvalidKey, bekam %v", err)
			}
		})
	}
}
