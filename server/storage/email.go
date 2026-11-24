package storage

import "strings"

// NormalizeEmail ist die EINE zentrale Email-Normalisierungsfunktion (F-260,
// Karte #211). Sie MUSS an jeder Schreib- UND jeder Lookup-Stelle der ACL-/
// User-/Forward-Auth-Schicht angewendet werden — keine verstreuten Ad-hoc-
// Trims mehr. Verhalten:
//
//	strings.TrimSpace → strings.ToLower
//
// Warum ToLower (und bewusst KEINE Unicode-NFC-Normalisierung):
//   - Das real-world-Problem ist Casing-/Whitespace-Varianz zwischen dem IdP
//     (Authelia/OIDC liefert z. B. `Owner@X.de`) und dem ACL-Eintrag
//     (`owner@x.de`). ToLower + TrimSpace deckt diesen Fall vollständig ab.
//   - Vollwertige Unicode-NFC-Normalisierung würde `golang.org/x/text`
//     (unicode/norm) als neue externe Laufzeit-Abhängigkeit erfordern. Das
//     Projekt-Prinzip ist minimale Dependencies (server/go.mod hält nur uuid +
//     sqlite); eine reine Casing-Korrektur rechtfertigt keinen neuen Modul-Pull.
//     NFC-relevante Email-Adressen (kombinierende Diakritika im Local-Part) sind
//     in der Praxis vernachlässigbar selten gegenüber dem Casing-Problem.
//   - ToLower nutzt Unicode-Case-Folding der Go-Stdlib (nicht nur ASCII), deckt
//     also auch nicht-ASCII-Domains/Local-Parts case-insensitiv ab.
//
// Die Funktion ist idempotent: NormalizeEmail(NormalizeEmail(x)) == NormalizeEmail(x).
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
