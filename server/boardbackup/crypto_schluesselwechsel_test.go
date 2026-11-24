package boardbackup_test

// Karte #437 / [[F-709]] — Schlüsselwechsel: Archive bleiben lesbar, und der
// Fehler sagt, was los ist.
//
// DER ZUSTAND VORHER: Nach einer Rotation von ABOARD_SECRET_KEY gab es keinen
// benutzbaren Weg zu einem älteren verschlüsselten Backup. Der Restore-Schlüssel
// war der eine prozessweite Env-Wert; um ein altes Archiv einzuspielen, hätte
// der Server mit dem ALTEN Schlüssel starten müssen — dann wären die aktuellen
// Config-Secrets und LLM-Keys unlesbar gewesen. Es gab keinen Zustand, in dem
// beides ging. Verschärfend meldete der Restore „beschädigt, manipuliert ODER
// mit falschem Schlüssel erzeugt": Der Betreiber konnte die Fälle nicht trennen
// und verdächtigte zuerst die Datei.
//
// WAS HIER GEPRÜFT WIRD, in der Reihenfolge der Wichtigkeit:
//  1. Bestehende ABBKENC1-Archive bleiben lesbar — gegen einen FEST
//     EINGECHECKTEN Testvektor, nicht gegen selbst erzeugte Bytes (siehe
//     Kommentar bei v1Vektor).
//  2. Ein Archiv mit fremdem Schlüssel meldet „anderer Schlüssel", nicht
//     „beschädigt".
//  3. Ein manipuliertes Archiv mit RICHTIGEM Schlüssel meldet weiterhin
//     „beschädigt" — die neue Unterscheidung darf die alte nicht aufweichen.
//  4. Der Vorgängerschlüssel öffnet ein altes Archiv, in beiden Formaten.
//  5. Der Fingerabdruck ist authentifiziert und verrät den Schlüssel nicht.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

/*
v1Vektor ist ein echtes ABBKENC1-Archiv, erzeugt mit dem Code VOR dieser

	Karte (Commit-Stand e92c271) und dem Schlüssel v1VektorKey.

	WARUM EIN FESTER VEKTOR und nicht „mit dem alten Writer erzeugen": Den alten
	Writer gibt es nicht mehr — geschrieben wird nur noch V2. Ein Test, der sein
	Alt-Archiv selbst herstellt, prüft eine Nachbildung des Formats gegen den
	Leser; passt die Nachbildung nicht zur Wirklichkeit, ist er grün und die
	echten Archive bleiben trotzdem unlesbar. Genau diese Archive sind der
	Anlass der Karte: Sie liegen bereits auf den Platten der Betreiber und
	niemand kann sie nachträglich anpassen.

	Der Vektor enthält den Klartext v1VektorKlartext.
*/
const (
	v1VektorKey      = "0000000000000000000000000000000000000000000000000000000000000001"
	v1VektorKlartext = "aBoard-Backup-Testvektor ABBKENC1"
	v1Vektor         = "4142424b454e43319c5581b81ba809b901000000317e6ff72035fea9d842ffda" +
		"1e37bf0850203639560b421a12f0a8a28e3ee28d81d09d9cfc6cd5d709ce509f296876d92c35"
)

/*
v2Vektor ist ein ABBKENC2-Archiv, erzeugt mit v2VektorKey und dem Stand

	dieses Sprints (nach der Kritiker-Nacharbeit S1b).

	WOZU EIN ZWEITER VEKTOR: Der Verify-Lauf zum Sprint-62-Abschluss hat zu
	Recht angemerkt, dass die S1b-Korrektur die HERKUNFT des Fingerabdrucks
	geändert hat — von `Fingerprint(aes-subkey)` auf
	`Fingerprint(DeriveKey(master, "aboard-backup-fingerprint-v1"))`. Derselbe
	Master-Key ergibt damit einen ANDEREN Kopf-Wert, und `schluesselFuer`
	vergleicht exakt, bevor überhaupt ein Entschlüsselungsversuch läuft. Wäre
	das Format schon ausgeliefert gewesen, hätte ein reines Code-Upgrade
	bestehende Archive als „fremder Schlüssel" abgelehnt.

	Nachgeprüft: `ABBKENC2` existiert in KEINEM Tag — `git tag --contains
	671b858` ist leer, und in `v1.37.0` kommt die Zeichenfolge nur in einem
	Kommentar vor („ein künftiges Format bekommt ABBKENC2"). Die Änderung war
	also gratis, und zwar zum letztmöglichen Zeitpunkt.

	Genau deshalb steht der Vektor jetzt hier: Ab dem ersten Tag, der V2
	ausliefert, ist jede Änderung an Magic, Kopf-Layout, Domänen-Strings oder
	Fingerabdruck-Herkunft ein Kompatibilitätsbruch. Dieser Test macht sie
	sichtbar, statt sie einem Betreiber im Ernstfall zu überlassen. Wer ihn rot
	sieht, hat nicht „den Test kaputtgemacht", sondern das Format geändert.
*/
const (
	v2VektorKey      = "0000000000000000000000000000000000000000000000000000000000000002"
	v2VektorKlartext = "aBoard-Backup-Testvektor ABBKENC2"
	v2Vektor         = "4142424b454e433287d9d8fbee3c1f6cbe7c6692a8d459340100000031872aac" +
		"171f28675ee299a0f9dc7c5a258a415abc1a09ad3d838e262625d565bbc0d9f2e661db2c5d263c164bd4d95cc281"
)

func mussHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("Testvektor unlesbar: %v", err)
	}
	return b
}

/* ── 1. Rückwärtskompatibilität ────────────────────────────────────── */

func TestSchluesselwechsel_V1ArchivBleibtLesbar(t *testing.T) {
	roh := mussHex(t, v1Vektor)
	if !bytes.HasPrefix(roh, []byte("ABBKENC1")) {
		t.Fatal("Aufbau: der Vektor trägt nicht das V1-Magic — dann prüft dieser Test nichts")
	}

	zurueck, err := entschluesselt(t, crypterAus(t, v1VektorKey), roh)
	if err != nil {
		t.Fatalf("bestehendes ABBKENC1-Archiv nicht mehr lesbar — genau der Schaden, "+
			"den die Karte verhindern soll: %v", err)
	}
	if string(zurueck) != v1VektorKlartext {
		t.Fatalf("Klartext falsch: %q", zurueck)
	}
}

func TestSchluesselwechsel_V1WirdAlsVerschluesseltErkannt(t *testing.T) {
	// SplitEncrypted entscheidet, ob überhaupt entschlüsselt wird. Kennt es
	// nur noch V2, läuft ein Alt-Archiv als „unverschlüsselt" in den
	// gzip-Leser und kommt als „beschädigtes Archiv" heraus.
	ok, rest, err := boardbackup.SplitEncrypted(bytes.NewReader(mussHex(t, v1Vektor)))
	if err != nil {
		t.Fatalf("SplitEncrypted: %v", err)
	}
	if !ok {
		t.Fatal("ein ABBKENC1-Archiv gilt nicht mehr als verschlüsselt")
	}
	// Und der Strom bleibt vollständig — der Aufrufer liest weiter ab Byte 0.
	alles := mussLesen(t, rest)
	if !bytes.Equal(alles, mussHex(t, v1Vektor)) {
		t.Fatal("SplitEncrypted hat den Strom beschädigt")
	}
}

func TestSchluesselwechsel_NeueArchiveTragenV2(t *testing.T) {
	roh := verschluesselt(t, crypterAus(t, keyA), []byte("neu"))
	if !bytes.HasPrefix(roh, []byte("ABBKENC2")) {
		t.Fatalf("neues Archiv trägt nicht das V2-Magic: %x", roh[:8])
	}
	ok, _, err := boardbackup.SplitEncrypted(bytes.NewReader(roh))
	if err != nil || !ok {
		t.Fatalf("V2 wird nicht als verschlüsselt erkannt (ok=%v, err=%v)", ok, err)
	}
}

/* ── 2./3. Die Unterscheidung, um die es geht ──────────────────────── */

func TestSchluesselwechsel_FremdesArchivWirdBenannt(t *testing.T) {
	roh := verschluesselt(t, crypterAus(t, keyA), []byte("inhalt"))
	_, err := entschluesselt(t, crypterAus(t, keyB), roh)

	if !errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) {
		t.Fatalf("erwartet ErrEncryptedFremderSchluessel, bekam %v", err)
	}
	if errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatal("der Schlüsselfehler ist zugleich ErrEncryptedCorrupt — die " +
			"Unterscheidung wäre damit wieder wertlos")
	}
	// Der Abdruck gehört in die Meldung: Ohne ihn kann der Betreiber zwei
	// Archive nicht einander zuordnen.
	fpA := fingerabdruckVon(t, keyA)
	if !strings.Contains(err.Error(), hex.EncodeToString(fpA)) {
		t.Fatalf("die Meldung nennt den Archiv-Abdruck nicht: %v", err)
	}
}

func TestSchluesselwechsel_ManipulationBleibtKorruption(t *testing.T) {
	// Die neue Aussage darf die alte nicht verdrängen: Wer das Archiv
	// verändert, bekommt weiterhin „beschädigt" — und zwar mit dem RICHTIGEN
	// Schlüssel, sonst misst der Test den Fingerabdruck statt das Auth-Tag.
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, bytes.Repeat([]byte("A"), 5000))
	kaputt := append([]byte(nil), roh...)
	kaputt[len(kaputt)/2] ^= 0x01

	_, err := entschluesselt(t, c, kaputt)
	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("manipuliertes Archiv: erwartet ErrEncryptedCorrupt, bekam %v", err)
	}
	if errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) {
		t.Fatal("ein beschädigtes Archiv wird als „falscher Schlüssel\" gemeldet — " +
			"dann sucht der Betreiber einen Schlüssel, den es nicht gibt")
	}
}

/* ── 4. Der Vorgängerschlüssel ─────────────────────────────────────── */

func mitVorgaenger(t *testing.T, aktuell, frueher string) *boardbackup.Crypter {
	t.Helper()
	c, err := boardbackup.NewCrypterMitVorgaengern(aktuell, frueher)
	if err != nil {
		t.Fatalf("NewCrypterMitVorgaengern: %v", err)
	}
	return c
}

func TestSchluesselwechsel_VorgaengerOeffnetV2Archiv(t *testing.T) {
	altesArchiv := verschluesselt(t, crypterAus(t, keyA), []byte("altbestand"))

	// Server nach der Rotation: aktuell keyB, vorher keyA.
	nachRotation := mitVorgaenger(t, keyB, keyA)
	zurueck, err := entschluesselt(t, nachRotation, altesArchiv)
	if err != nil {
		t.Fatalf("Archiv mit dem Vorgängerschlüssel nicht lesbar: %v", err)
	}
	if string(zurueck) != "altbestand" {
		t.Fatalf("Klartext falsch: %q", zurueck)
	}
}

func TestSchluesselwechsel_VorgaengerOeffnetV1Archiv(t *testing.T) {
	// Der praktisch wichtigste Fall: ein Archiv aus der Zeit VOR dem
	// Fingerabdruck, gelesen von einem Server nach der Rotation. Hier kann
	// nichts anhand des Kopfes ausgewählt werden — der Leser muss die
	// Kandidaten am ersten Block durchprobieren.
	nachRotation := mitVorgaenger(t, keyB, v1VektorKey)
	zurueck, err := entschluesselt(t, nachRotation, mussHex(t, v1Vektor))
	if err != nil {
		t.Fatalf("V1-Archiv mit dem Vorgängerschlüssel nicht lesbar: %v", err)
	}
	if string(zurueck) != v1VektorKlartext {
		t.Fatalf("Klartext falsch: %q", zurueck)
	}
}

func TestSchluesselwechsel_OhnePassendenSchluesselWeiterhinFehler(t *testing.T) {
	// Gegenprobe zur Kandidatenliste: Sie darf nicht dazu führen, dass
	// irgendetwas aufgeht. keyA-Archiv, Server kennt keyB und den
	// V1-Vektor-Schlüssel — beides passt nicht.
	roh := verschluesselt(t, crypterAus(t, keyA), []byte("x"))
	c := mitVorgaenger(t, keyB, v1VektorKey)
	if _, err := entschluesselt(t, c, roh); !errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) {
		t.Fatalf("erwartet ErrEncryptedFremderSchluessel, bekam %v", err)
	}
}

func TestSchluesselwechsel_GeschriebenWirdImmerMitDemAktuellen(t *testing.T) {
	// Ein Vorgängerschlüssel darf LESEN, nie SCHREIBEN — sonst verlängert
	// sich seine Lebensdauer unbemerkt und die Rotation war folgenlos.
	c := mitVorgaenger(t, keyB, keyA)
	roh := verschluesselt(t, c, []byte("frisch"))

	if _, err := entschluesselt(t, crypterAus(t, keyB), roh); err != nil {
		t.Fatalf("das neue Archiv ist nicht mit dem AKTUELLEN Schlüssel lesbar: %v", err)
	}
	if _, err := entschluesselt(t, crypterAus(t, keyA), roh); !errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) {
		t.Fatalf("das neue Archiv liess sich mit dem VORGÄNGER öffnen — dann "+
			"wurde damit geschrieben: %v", err)
	}
}

func TestSchluesselwechsel_LeererVorgaengerIstKeinFehler(t *testing.T) {
	// ABOARD_SECRET_KEY_PREVIOUS gesetzt-aber-leer ist der Normalfall in
	// jeder Deployment-Vorlage. Er darf den Server nicht anhalten.
	c, err := boardbackup.NewCrypterMitVorgaengern(keyA, "")
	if err != nil || c == nil {
		t.Fatalf("leerer Vorgängerschlüssel: err=%v c=%v", err, c)
	}
	roh := verschluesselt(t, c, []byte("y"))
	if _, err := entschluesselt(t, c, roh); err != nil {
		t.Fatalf("Roundtrip: %v", err)
	}
}

func TestSchluesselwechsel_UnbrauchbarerVorgaengerWirdGemeldet(t *testing.T) {
	// Nicht verschlucken: Sonst startet der Server scheinbar richtig, und der
	// Betreiber merkt erst im Ernstfall, dass sein Rettungsweg nie existierte.
	if _, err := boardbackup.NewCrypterMitVorgaengern(keyA, "keinhex"); err == nil {
		t.Fatal("unbrauchbarer Vorgängerschlüssel wurde stillschweigend übergangen")
	}
}

/* ── 5. Der Fingerabdruck selbst ───────────────────────────────────── */

/*
Der Abdruck kommt aus einem EIGENEN Subkey (`aboard-backup-fingerprint-v1`),

	nicht aus dem Verschlüsselungs-Subkey `aboard-backup-v1` — Schlüsseltrennung,
	siehe `ableiten()` in crypto.go (Sprint-62-Kritiker, S1b).
*/
func fingerabdruckVon(t *testing.T, masterKey string) []byte {
	t.Helper()
	sub, err := secretcrypto.DeriveKey(masterKey, "aboard-backup-fingerprint-v1")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	fp, err := secretcrypto.Fingerprint(sub)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	return fp
}

func TestSchluesselwechsel_FingerabdruckKommtAusEigenemSubkey(t *testing.T) {
	roh := verschluesselt(t, crypterAus(t, keyA), []byte("z"))
	imKopf := roh[8:16]

	if !bytes.Equal(imKopf, fingerabdruckVon(t, keyA)) {
		t.Fatalf("Kopf-Abdruck %x passt nicht zum Fingerabdruck-Subkey", imKopf)
	}

	// Und ausdrücklich WEDER der Master-Key NOCH der Verschlüsselungs-Subkey.
	// Der zweite Teil ist der Sprint-62-Befund S1b: Bis dahin war der
	// AES-Schlüssel zugleich HMAC-Schlüssel für den Abdruck. Kein bekannter
	// Angriff, aber es ist genau die Schlüsseltrennung, für die dieses Modul
	// an anderer Stelle ausführlich argumentiert.
	masterFp, err := secretcrypto.Fingerprint(keyA)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if bytes.Equal(imKopf, masterFp) {
		t.Fatal("der Kopf trägt den Abdruck des MASTER-Keys")
	}

	aesSub, err := secretcrypto.DeriveKey(keyA, "aboard-backup-v1")
	if err != nil {
		t.Fatal(err)
	}
	aesFp, err := secretcrypto.Fingerprint(aesSub)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(imKopf, aesFp) {
		t.Fatal("der Kopf trägt den Abdruck des VERSCHLÜSSELUNGS-Subkeys — " +
			"derselbe Wert wäre dann AES-Schlüssel und HMAC-Schlüssel zugleich")
	}
}

func TestSchluesselwechsel_ManipulierterFingerabdruckFaelltAuf(t *testing.T) {
	// Wer den Abdruck umschreibt, bekommt keinen Klartext. Mehr sichert
	// dieser Test bewusst nicht zu: WELCHER der beiden Fehler kommt, hängt
	// daran, ob der veränderte Abdruck zu einem konfigurierten Schlüssel
	// passt — und die Abdruck-Prüfung läuft vor dem ersten Auth-Tag.
	//
	// Die erste Fassung dieses Tests behauptete in ihrem Kommentar, die
	// AAD-Bindung verhindere die irreführende Meldung. Das stimmt nicht, und
	// der Code sagt es auch: `DecryptReader` wählt den Schlüssel anhand des
	// Abdrucks, bevor irgendein Tag geprüft wird. Was die AAD-Bindung
	// wirklich leistet, steht im Test darunter.
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, []byte("inhalt"))
	kaputt := append([]byte(nil), roh...)
	kaputt[8] ^= 0xFF // erstes Byte des Abdrucks

	_, err := entschluesselt(t, c, kaputt)
	if !errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) &&
		!errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("manipulierter Abdruck lief durch: %v", err)
	}
	// Der Kern: Es kommt kein Klartext heraus. Welcher der beiden Fehler es
	// wird, hängt daran, ob der veränderte Abdruck zufällig zu einem
	// konfigurierten Schlüssel passt (praktisch nie) — deshalb prüft der
	// Test die Aussage, die in BEIDEN Fällen gilt.
}

func TestSchluesselwechsel_FingerabdruckIstStabilUeberKodierungen(t *testing.T) {
	// Derselbe Schlüssel, einmal hex und einmal base64 notiert, muss denselben
	// Abdruck ergeben — sonst hält ein Betreiber sein eigenes Archiv für ein
	// fremdes, nur weil er die Env-Variable anders geschrieben hat. Dieselbe
	// Begründung wie bei secretcrypto.DeriveKey.
	hexKey := keyA
	roh := verschluesselt(t, crypterAus(t, hexKey), []byte("kodierung"))

	rohBytes := mussHex(t, hexKey)
	b64Key := base64Std(rohBytes)
	if _, err := entschluesselt(t, crypterAus(t, b64Key), roh); err != nil {
		t.Fatalf("derselbe Schlüssel in base64 öffnet das Archiv nicht: %v", err)
	}
}

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestSchluesselwechsel_KeinDowngradeAufV1(t *testing.T) {
	// DAS ist der messbare Ertrag des Abdrucks in der AAD.
	//
	// Angriff: Ein V2-Archiv in ein V1-Archiv umschreiben — Magic auf
	// "ABBKENC1" ändern und die acht Abdruck-Bytes herausschneiden. Nonce und
	// Blöcke bleiben unverändert gültig. Ohne den Abdruck in der AAD ginge
	// das Archiv auf, und die Schlüsselinformation wäre still verschwunden:
	// Genau die Angabe, wegen der es dieses Format gibt, liesse sich also
	// von jedem entfernen, der die Datei in die Hand bekommt.
	//
	// Gegenprobe: `chunkAAD` das Suffix ignorieren lassen. Dann werden ZWEI
	// Tests rot — dieser und `V2FormatBleibtStabil`, weil der V2-Vektor
	// dieselbe AAD braucht. Bis Sprint 62 stand hier „als einziger rot"; das
	// war vor dem Vektor richtig und danach irreführend (Kritiker, V4). Wer
	// zwei rote Tests sieht, hat nicht zu grob zurückgebaut.
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, bytes.Repeat([]byte("F"), 3000))

	abgestuft := append([]byte(nil), []byte("ABBKENC1")...)
	abgestuft = append(abgestuft, roh[8+8:]...) // Abdruck herausschneiden

	if _, err := entschluesselt(t, c, abgestuft); err == nil {
		t.Fatal("das auf V1 zurückgeschriebene Archiv ging auf — der Abdruck " +
			"lässt sich damit spurlos entfernen")
	} else if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("erwartet ErrEncryptedCorrupt, bekam %v", err)
	}
}

func TestSchluesselwechsel_V2FormatBleibtStabil(t *testing.T) {
	// Der Wächter gegen einen unbemerkten Formatbruch — Begründung bei
	// `v2Vektor`. Er prüft BEIDES: dass das Archiv aufgeht (Schlüsselwahl,
	// AAD, Blockrahmen unverändert) und dass der Kopf-Abdruck exakt derselbe
	// ist (Fingerabdruck-Herkunft unverändert).
	roh := mussHex(t, v2Vektor)
	if !bytes.HasPrefix(roh, []byte("ABBKENC2")) {
		t.Fatal("Aufbau: der Vektor trägt nicht das V2-Magic")
	}

	zurueck, err := entschluesselt(t, crypterAus(t, v2VektorKey), roh)
	if err != nil {
		t.Fatalf("bestehendes ABBKENC2-Archiv nicht mehr lesbar — das Format hat sich "+
			"geändert. Ab dem ersten Tag, der V2 ausliefert, ist das ein "+
			"Kompatibilitätsbruch beim Betreiber: %v", err)
	}
	if string(zurueck) != v2VektorKlartext {
		t.Fatalf("Klartext falsch: %q", zurueck)
	}

	// Und der Abdruck selbst, damit auch eine Änderung auffällt, die zufällig
	// noch lesbar bliebe (etwa wenn jemand die Domäne ändert und den Vektor
	// gleich mit neu erzeugt).
	if !bytes.Equal(roh[8:16], fingerabdruckVon(t, v2VektorKey)) {
		t.Fatal("der Kopf-Abdruck des Vektors passt nicht mehr zur heutigen Ableitung — " +
			"die Fingerabdruck-Herkunft wurde geändert")
	}
}

func TestSchluesselwechsel_V1OhnePassendenSchluesselNenntDieRichtung(t *testing.T) {
	// Sprint-62-Kritiker, V5: Ein V1-Archiv trägt keinen Abdruck — „anderer
	// Schlüssel" lässt sich hier nicht beweisen. Die Richtung aber schon, und
	// genau die fehlte: Der Betreiber bekam „beschädigt, manipuliert oder mit
	// falschem Schlüssel erzeugt" und wurde zur Datei geschickt, obwohl sein
	// Schlüssel das Problem war. Für die Archivklasse, die der Anlass von
	// F-709 ist.
	c := mitVorgaenger(t, keyB, keyA) // beide passen NICHT zum V1-Vektor
	_, err := entschluesselt(t, c, mussHex(t, v1Vektor))

	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("erwartet ErrEncryptedCorrupt (schwächere Aussage als bei V2), bekam %v", err)
	}
	if !strings.Contains(err.Error(), "ABOARD_SECRET_KEY_PREVIOUS") {
		t.Fatalf("die Meldung nennt den Schlüssel-Verdacht nicht — der Betreiber sucht "+
			"dann am falschen Ende: %v", err)
	}
	// Und die Aussage darf NICHT zu stark werden: Ohne Abdruck ist ein
	// manipuliertes Archiv von einem fremden nicht zu unterscheiden.
	if errors.Is(err, boardbackup.ErrEncryptedFremderSchluessel) {
		t.Fatal("V1 meldet ErrEncryptedFremderSchluessel — das behauptet eine " +
			"Gewissheit, die es ohne Fingerabdruck nicht gibt")
	}
}
