package storage

// Karte #1083 (Nachprüfung): Die Konto-Einstellungen und die CLI
// `nutzer bindung-loesen` schreiben Audit-Zeilen unter „personen/<users.id>“.
// Die Personen-Löschung schreibt auch diesen Schlüssel auf das Pseudonym um.

import (
	"context"
	"testing"
)

func TestNutzerLoeschen_AuditSchluesselPersonenWirdPseudonymisiert(t *testing.T) {
	db := loeschDB(t)
	befuelle(t, db)
	mustExec(t, db, `INSERT INTO server_config_audit (id, key, user_id, action, new_value_redacted, ts) VALUES ('aud-p', ?, ?, 'konto-passwort-geaendert', 'ergebnis=ok', '2026-01-01')`,
		"personen/"+loeschAID, loeschAID)
	mustExec(t, db, `INSERT INTO server_config_audit (id, key, user_id, action, new_value_redacted, ts) VALUES ('aud-q', ?, ?, 'konto-passwort-geaendert', 'ergebnis=ok', '2026-01-01')`,
		"personen/"+loeschCID, loeschCID)
	erg, err := NewUserRepo(db).Loeschen(context.Background(), LoeschAnweisung{
		UserID: loeschAID, AdminID: loeschBID, AdminEmail: loeschB,
		Uebertragen: map[string]string{boardEigen: loeschB},
	})
	if err != nil {
		t.Fatalf("Loeschen: %v", err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM server_config_audit WHERE key = ?`, "personen/"+loeschAID); n != 0 {
		t.Errorf("%d Audit-Zeilen unter der alten ID, erwartet 0", n)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM server_config_audit WHERE key = ?`, "personen/"+erg.Pseudonym); n != 1 {
		t.Errorf("%d Audit-Zeilen unter dem Pseudonym, erwartet 1", n)
	}
	// Eine andere Person bleibt unberührt.
	if n := zaehle(t, db, `SELECT COUNT(*) FROM server_config_audit WHERE key = ?`, "personen/"+loeschCID); n != 1 {
		t.Errorf("Zeile einer anderen Person verändert (%d)", n)
	}
}
