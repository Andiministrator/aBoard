package storage

// fristen.go — gemeinsame Grenzen der Aufbewahrungsfristen (Aktivitäten,
// LLM-Verläufe, Audit; ADR-0129 §5, Session-Kritiker Sprint 72, K-B1).
//
// Ohne Obergrenze lief AddDate(0, 0, -n) bei sehr großen Werten über: Wer
// „nie löschen“ meinte und ABOARD_AUDIT_RETENTION_DAYS=9223372036854775807
// setzte, bekam einen Stichtag um „jetzt“ — und verlor beim nächsten Lauf
// das ganze Audit, ohne Warnung, weil der Wert als gültig galt.

import "time"

// FristTageMax ist die größte zulässige Frist (100 Jahre). Wer länger
// aufbewahren will, meint „nie löschen“ — dafür genügt dieser Wert.
const FristTageMax = 36500

// fristGueltig prüft einen aus der Umgebung gelesenen Fristwert.
func fristGueltig(n int) bool { return n >= 1 && n <= FristTageMax }

// fristStichtag liefert den Stichtag (UTC, Sekunden, feste Breite) für eine
// Frist in Tagen. ok=false, wenn der Stichtag nicht vor „jetzt“ liegt — dann
// löscht der Aufrufer nichts (Ausfall in Richtung Nichtstun).
func fristStichtag(tage int) (stichtag string, ok bool) {
	jetzt := timeNow().UTC()
	s := jetzt.AddDate(0, 0, -tage)
	if !s.Before(jetzt.Add(-time.Hour)) {
		return "", false
	}
	return s.Format("2006-01-02T15:04:05"), true
}
