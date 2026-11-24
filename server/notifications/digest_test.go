package notifications

// digest_test.go — Tests für DigestScheduler, insbesondere das F-379-Cleanup
// (notification_log-Eviction, Karte #411). Vor #411 gab es hier noch KEINE
// Tests — DigestScheduler.RunOnce war ungetestet.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// newTestDigestScheduler baut einen DigestScheduler auf einer frischen
// Test-DB. now wird als deps.Now injiziert (steuert nextDigestTime). Liefert
// zusätzlich das rohe *sql.DB-Handle — NotificationLogRepo kapselt seine
// Connection paketintern, für Backdating/Existenz-Checks in Tests reicht
// direktes SQL gegen dieselbe Datei.
func newTestDigestScheduler(t *testing.T, now func() time.Time) (*DigestScheduler, *storage.NotificationLogRepo, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	idGen := func() string {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}
	logs := storage.NewNotificationLogRepo(db, idGen)
	s := NewDigestScheduler(DigestDeps{
		Logs:   logs,
		Logger: t.Logf,
		Now:    now,
	})
	return s, logs, db
}

func notifLogExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM notification_log WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("notifLogExists(%s): %v", id, err)
	}
	return n > 0
}

func backdateNotifLog(t *testing.T, db *sql.DB, id, ts string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE notification_log SET ts = ? WHERE id = ?`, ts, id); err != nil {
		t.Fatalf("backdateNotifLog(%s): %v", id, err)
	}
}

// TestDigestScheduler_EvictOld_RemovesOldKeepsYoung sichert F-379 direkt am
// Scheduler-Aufruf ab: evictOld() muss alte Zeilen löschen UND junge stehen
// lassen. Ein Test, der nur das Löschen prüft, wäre auch mit einem blinden
// `DELETE FROM notification_log` grün — deshalb beide Seiten.
//
// GEGENPROBE (Karte #411): Entfernt man den `s.evictOld(ctx)`-Aufruf in
// DigestScheduler.loop(), bleibt DIESER Test grün (er ruft evictOld direkt
// auf, nicht über loop()) — die Wiring zwischen loop() und evictOld() ist
// NICHT durch einen automatisierten Test gedeckt, siehe Hinweis unten.
func TestDigestScheduler_EvictOld_RemovesOldKeepsYoung(t *testing.T) {
	s, logs, db := newTestDigestScheduler(t, nil)
	ctx := context.Background()

	oldID, err := logs.Insert(ctx, storage.NotificationLogEntry{
		UserID: "u1", EventType: "card.commented", Channel: "email", Status: storage.NotifyStatusSent,
	})
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}
	youngID, err := logs.Insert(ctx, storage.NotificationLogEntry{
		UserID: "u1", EventType: "card.commented", Channel: "email", Status: storage.NotifyStatusSent,
	})
	if err != nil {
		t.Fatalf("insert young: %v", err)
	}
	oldTs := time.Now().UTC().Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	backdateNotifLog(t, db, oldID, oldTs)

	s.evictOld(ctx)

	if notifLogExists(t, db, oldID) {
		t.Errorf("alte Zeile (id=%s) sollte nach evictOld() weg sein", oldID)
	}
	if !notifLogExists(t, db, youngID) {
		t.Errorf("junge Zeile (id=%s) sollte erhalten bleiben", youngID)
	}
}

// HINWEIS zur loop()-Wiring (kein automatisierter Timing-Test): loop()
// berechnet den Wartezeitpunkt über `time.Until(nextDigestTime(deps.Now()))`
// — `time.Until` vergleicht IMMER gegen die reale Wanduhr, deps.Now() steuert
// nur, welcher Kalendertag/welche Uhrzeit als "jetzt" in die 18:00-Berechnung
// eingeht. Ein fingierter deps.Now() kann den realen Timer-Fire deshalb nicht
// auf Millisekunden verkürzen, ohne loop() selbst umzubauen (eigener
// injizierbarer Waiter) — das ist eine grössere, hier nicht beauftragte
// Änderung an gemeinsam genutztem Produktivcode. Die Wiring-Zusage — dass
// evictOld() im SELBEN case-Zweig wie RunOnce() hängt, also am selben
// 18:00-Tick, kein zweiter Scheduler — ist stattdessen durch Code-Inspektion
// belegt: beide Aufrufe stehen direkt nacheinander in `loop()`s
// `case <-timer.C:`-Zweig (server/notifications/digest.go). Siehe
// Karten-Bericht #411, Abschnitt „Offene Punkte".

// TestDigestScheduler_StartRaeumtSofortAuf schliesst die Lücke, die der
// Sprint-57-Kritiker gefunden hat: Die Eviction hing ALLEIN am 18:00-Tick.
// Startet der Prozess öfter neu als einmal täglich — der Normalfall dieses
// Boards, jeder :latest-Deploy startet den Container neu —, lief sie nie, und
// notification_log wuchs exakt so unbegrenzt weiter wie vor F-379. Das
// damalige Vorbild (mail.RunWorker, bis F-1192 Cleanup vor der Poll-Schleife)
// machte den Cleanup von Anfang an beim Start; Karte #411 hat beim Übernehmen
// genau diesen Teil ausgelassen.
//
// Der Test prüft die VERDRAHTUNG, nicht die Evict-Logik (das tut der Test
// darüber): Start() ohne jeden Timer-Ablauf muss die alte Zeile entfernen.
// Rot, sobald der s.evictOld(ctx)-Aufruf am Anfang von loop() wegfällt.
func TestDigestScheduler_StartRaeumtSofortAuf(t *testing.T) {
	s, logs, db := newTestDigestScheduler(t, nil)
	ctx := context.Background()

	oldID, err := logs.Insert(ctx, storage.NotificationLogEntry{
		UserID: "u1", EventType: "card.commented", Channel: "email", Status: storage.NotifyStatusSent,
	})
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}
	backdateNotifLog(t, db, oldID, time.Now().UTC().Add(-40*24*time.Hour).Format(time.RFC3339Nano))

	cancel := s.Start(ctx)
	defer cancel()

	// Kein Timer wird hier ausgelöst — der nächste 18:00-Tick liegt Stunden
	// entfernt. Wenn die Zeile trotzdem verschwindet, kam der Cleanup vom
	// Start. Warten statt sleep-und-hoffen: aktiv pollen mit Deadline.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !notifLogExists(t, db, oldID) {
			return // Start hat aufgeräumt — das ist die Zusage.
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("alte Zeile (id=%s) lebt noch — Start() hat nicht aufgeräumt, "+
		"die Eviction hängt wieder allein am Tages-Tick", oldID)
}

// ── Der Versand-Schalter gilt auch für den Digest (Karte #420, Sprint-59-
//    Kritiker [S] P1) ────────────────────────────────────────────────────
//
// Die Lücke, die dieser Test schliesst, war nicht theoretisch, sondern der
// Normalbetrieb: Der `suppressedSender` meldet Erfolg, der Worker schreibt
// `status='sent'`, ab der 31. Zustellung pro Stunde greift die Drosselung
// und schreibt `status='throttled'` — und genau die sammelt der Digest um
// 18:00 ein. Der Schalter umhüllte die Sender des Workers; der Digest
// bekam elf Zeilen später die rohe `*EmailSender`-Instanz und ging vorbei.
//
// Auslösbar war es zusätzlich sofort über den Admin-Test-Hook, ohne auf
// 18:00 zu warten.

// digestSpionSender zählt SendDigest-Aufrufe. Ein echter *EmailSender lässt
// sich hier nicht einsetzen (SendDigest würde SMTP wählen) — deshalb prüft
// der Test die Entscheidung DAVOR: läuft der Code überhaupt bis zum Sender?
type digestUserStub struct{ email, name string }

func (u digestUserStub) Resolve(context.Context, string) (string, string, bool, error) {
	return u.email, u.name, false, nil
}

// Ein Digest-Lauf mit ausgeschaltetem Versand darf den echten Sender nicht
// erreichen. Der Beleg ist indirekt, aber eindeutig: `Email` bleibt `nil`.
// Würde der Code bis zum Sendeaufruf laufen, gäbe es eine Nil-Dereferenz und
// der Test würde panicken — was ein Testfehlschlag ist, kein Durchrutschen.
func TestDigest_VersandAus_ErreichtDenSenderNicht(t *testing.T) {
	jetzt := time.Date(2026, 8, 27, 18, 0, 0, 0, time.Local)
	s, logs, db := newTestDigestScheduler(t, func() time.Time { return jetzt })
	s.deps.Users = digestUserStub{email: "admin@example.com", name: "Admin"}
	s.deps.Email = nil // absichtlich: ein Zugriff darauf ist der Fehlerfall
	s.deps.DeliveryEnabled = false

	ctx := context.Background()
	id, err := logs.Insert(ctx, storage.NotificationLogEntry{
		UserID: "u1", EventType: EventCardCommented, Channel: ChannelEmail,
		Summary: "gedrosselt", Status: "throttled",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	/* Die Menge behaupten, bevor über sie iteriert wird: Ein Lauf über null
	   pending-Einträge wäre von einem bestandenen nicht zu unterscheiden
	   (Fehlerklasse 26). */
	pending, err := logs.PendingDigest(ctx, "u1")
	if err != nil || len(pending) != 1 {
		t.Fatalf("Vorbedingung: PendingDigest = %d Einträge, err=%v — erwartet genau 1", len(pending), err)
	}

	if err := s.runOneUser(ctx, "u1", jetzt); err != nil {
		t.Fatalf("runOneUser: %v", err)
	}

	/* Als digested markiert — bewusst, damit sich im Trockenbetrieb nichts
	   unbegrenzt aufstaut und der erste Lauf nach dem Einschalten keine
	   Sammelmail über Wochen schickt. */
	if notifLogPendingCount(t, db, "u1") != 0 {
		t.Error("Eintrag ist nach dem unterdrückten Digest noch pending — beim Einschalten " +
			"des Versands entstünde daraus eine Sammelmail über den gesamten Trockenzeitraum")
	}
	_ = id
}

// Und die Gegenrichtung: eingeschaltet MUSS der Sender erreicht werden.
// Ohne diesen zweiten Teil wäre der Test oben auch mit einem `return nil`
// ganz am Anfang von runOneUser grün — er prüfte dann, dass nichts passiert,
// und nicht, dass der Schalter wirkt.
func TestDigest_VersandAn_ErreichtDenSender(t *testing.T) {
	jetzt := time.Date(2026, 8, 27, 18, 0, 0, 0, time.Local)
	s, logs, _ := newTestDigestScheduler(t, func() time.Time { return jetzt })
	s.deps.Users = digestUserStub{email: "admin@example.com", name: "Admin"}
	s.deps.Email = nil
	s.deps.DeliveryEnabled = true

	ctx := context.Background()
	if _, err := logs.Insert(ctx, storage.NotificationLogEntry{
		UserID: "u2", EventType: EventCardCommented, Channel: ChannelEmail,
		Summary: "gedrosselt", Status: "throttled",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	/* Mit `Email == nil` und eingeschaltetem Versand läuft der Code in die
	   Nil-Dereferenz — genau das ist hier der BELEG, dass er den Sender
	   erreicht. Er wird deshalb erwartet und abgefangen. */
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("runOneUser hat den Sender NICHT erreicht, obwohl der Versand an ist — " +
				"der Schalter hängt in der falschen Richtung, oder der Digest sendet gar nicht mehr")
		}
	}()
	_ = s.runOneUser(ctx, "u2", jetzt)
}

// TestNextDigestTime_UeberlebtDSTWechsel (F-373, Sprint-42-Kritiker; behoben
// Sprint 61, Karte #445): Der „morgen"-Zweig von nextDigestTime durfte nicht
// mehr per Dauer-Arithmetik (`Add(24h)`) rechnen — an einem DST-Wechseltag
// hat der lokale Tag 23 oder 25 Stunden, nicht 24. Beide 2026er
// Europe/Berlin-Wechsel werden geprüft: Frühjahr (23h-Tag, 29.03.) und
// Herbst (25h-Tag, 25.10.). Rot, sobald der Fix wieder durch `Add(24 *
// time.Hour)` ersetzt wird.
func TestNextDigestTime_UeberlebtDSTWechsel(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("Europe/Berlin nicht verfügbar (kein tzdata): %v", err)
	}

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "Fruehjahr — Umstellung CET zu CEST (23h-Tag)",
			// Kurz nach dem 18:00-Fire am 28.03. — der "morgen"-Zweig muss
			// greifen und exakt 18:00 CEST am 29.03. treffen, nicht 19:00.
			now:  time.Date(2026, 3, 28, 18, 0, 1, 0, loc),
			want: time.Date(2026, 3, 29, 18, 0, 0, 0, loc),
		},
		{
			name: "Herbst — Umstellung CEST zu CET (25h-Tag)",
			// Kurz nach dem 18:00-Fire am 24.10. — muss exakt 18:00 CET am
			// 25.10. treffen, nicht 17:00.
			now:  time.Date(2026, 10, 24, 18, 0, 1, 0, loc),
			want: time.Date(2026, 10, 25, 18, 0, 0, 0, loc),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextDigestTime(tc.now)
			if !got.Equal(tc.want) {
				t.Fatalf("nextDigestTime(%s) = %s, want %s (Differenz: %s)",
					tc.now, got, tc.want, got.Sub(tc.want))
			}
		})
	}
}

func notifLogPendingCount(t *testing.T, db *sql.DB, userID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM notification_log WHERE user_id = ? AND status = 'throttled' AND digested_at IS NULL`,
		userID).Scan(&n); err != nil {
		t.Fatalf("pendingCount: %v", err)
	}
	return n
}

// TestDigestScheduler_EvictOld_WartetAufGestellteUhr (F-1192).
// GEGENPROBE: Ohne uhr.Erlaubt in evictOld ist die alte Zeile weg — rot.
func TestDigestScheduler_EvictOld_WartetAufGestellteUhr(t *testing.T) {
	s, logs, db := newTestDigestScheduler(t, nil)
	ctx := context.Background()
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	oldID, err := logs.Insert(ctx, storage.NotificationLogEntry{
		UserID: "u1", EventType: "card.commented", Channel: "email", Status: storage.NotifyStatusSent,
	})
	if err != nil {
		t.Fatal(err)
	}
	backdateNotifLog(t, db, oldID, time.Now().UTC().Add(-40*24*time.Hour).Format(time.RFC3339Nano))

	uhr.Gestellt = func() (bool, error) { return false, nil }
	s.evictOld(ctx)
	if !notifLogExists(t, db, oldID) {
		t.Fatal("alte Zeile mit nicht gestellter Uhr gelöscht")
	}
	uhr.Gestellt = func() (bool, error) { return true, nil }
	s.evictOld(ctx)
	if notifLogExists(t, db, oldID) {
		t.Fatal("alte Zeile nach dem Stellen der Uhr nicht gelöscht")
	}
}
