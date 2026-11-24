package storage

// acl_domain.go — Board-Freigabe für eine ganze E-Mail-Domain (ADR-0132, Karte #653).
//
// Ein Domain-Eintrag ist eine gewöhnliche acl-Zeile mit user_email = "@<domain>".
// Er wirkt nur, solange die Domain in ABOARD_ACL_DOMAINS steht — geprüft beim
// Setzen UND bei jeder Auswertung (ADR-0132 §4). Die persönliche Zeile einer
// E-Mail geht immer vor (§5).

import (
	"errors"
	"os"
	"strings"
)

// ACLDomainsEnv nennt die Domains, für die ein Owner eine Freigabe setzen darf.
// Ein Wert, kein Schalter (docs/betrieb.md §6.1): leer heisst „keine“.
const ACLDomainsEnv = "ABOARD_ACL_DOMAINS"

var (
	// ErrDomainNichtErlaubt: Die Domain steht nicht in ABOARD_ACL_DOMAINS.
	ErrDomainNichtErlaubt = errors.New("storage: Domain ist für Freigaben nicht erlaubt")
	// ErrDomainRolle: Eine Domain darf höchstens editor sein (ADR-0132 §3).
	ErrDomainRolle = errors.New("storage: eine Domain kann nicht owner sein")
	// ErrDomainServiceBoard: Das Service-Board (Mail-Eingang, Spam-Blocklist)
	// wird nie per Domain freigegeben (ADR-0132 §6, F-1514).
	ErrDomainServiceBoard = errors.New("storage: das Service-Board kann nicht per Domain freigegeben werden")
)

// ErlaubteACLDomains liest ABOARD_ACL_DOMAINS pro Aufruf (kein Cache, wie die
// übrigen Env-Werte). Getrimmt, kleingeschrieben, führendes "@" entfernt,
// leere und doppelte Einträge verworfen, Reihenfolge der Angabe bleibt.
func ErlaubteACLDomains() []string {
	out := []string{}
	gesehen := map[string]bool{}
	for _, teil := range strings.Split(os.Getenv(ACLDomainsEnv), ",") {
		d := strings.TrimPrefix(NormalizeEmail(teil), "@")
		if !gueltigeDomain(d) || gesehen[d] {
			continue
		}
		gesehen[d] = true
		out = append(out, d)
	}
	return out
}

// IstDomainSubjekt meldet, ob ein (normalisierter) ACL-Schlüssel ein
// Domain-Eintrag ist. Kein echtes Postfach beginnt mit "@".
func IstDomainSubjekt(subjekt string) bool {
	return strings.HasPrefix(subjekt, "@")
}

// gueltigeDomain: nicht leer, mindestens ein Punkt, kein "@", kein Leerraum,
// kein Punkt am Rand. Bewusst schlicht — die Liste kommt vom Betreiber, und
// verglichen wird exakt.
func gueltigeDomain(d string) bool {
	if d == "" || !strings.Contains(d, ".") || strings.ContainsAny(d, "@ \t\r\n,") {
		return false
	}
	return !strings.HasPrefix(d, ".") && !strings.HasSuffix(d, ".")
}

// domainErlaubt prüft exakt gegen die aktuelle Liste (keine Subdomains).
func domainErlaubt(d string) bool {
	for _, e := range ErlaubteACLDomains() {
		if e == d {
			return true
		}
	}
	return false
}

// domainSubjektFuer liefert "@<domain>" für eine persönliche E-Mail, wenn ihre
// Domain (Teil nach dem LETZTEN "@") erlaubt ist; sonst "".
func domainSubjektFuer(email string) string {
	email = NormalizeEmail(email)
	if IstDomainSubjekt(email) {
		return ""
	}
	i := strings.LastIndex(email, "@")
	if i <= 0 {
		return ""
	}
	d := email[i+1:]
	if !gueltigeDomain(d) || !domainErlaubt(d) {
		return ""
	}
	return "@" + d
}

// PruefeACLSubjektFuerBoard ergänzt PruefeACLSubjekt um die Board-Regel:
// Am Service-Board ist keine Domain-Freigabe erlaubt (ADR-0132 §6, F-1514).
func PruefeACLSubjektFuerBoard(boardID, subjekt, role string) error {
	if IstDomainSubjekt(NormalizeEmail(subjekt)) && boardID == ServiceBoardID {
		return ErrDomainServiceBoard
	}
	return PruefeACLSubjekt(subjekt, role)
}

// PruefeACLSubjekt prüft einen ACL-Schlüssel vor dem Schreiben. Persönliche
// E-Mails bleiben ungeprüft wie bisher; ein Domain-Eintrag muss eine erlaubte
// Domain nennen und darf nicht owner sein.
func PruefeACLSubjekt(subjekt, role string) error {
	subjekt = NormalizeEmail(subjekt)
	if !IstDomainSubjekt(subjekt) {
		return nil
	}
	if role == RoleOwner {
		return ErrDomainRolle
	}
	d := strings.TrimPrefix(subjekt, "@")
	if !gueltigeDomain(d) || !domainErlaubt(d) {
		return ErrDomainNichtErlaubt
	}
	return nil
}
