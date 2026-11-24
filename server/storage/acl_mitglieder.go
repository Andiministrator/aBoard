package storage

// acl_mitglieder.go — Anzeigenamen der Personen eines Boards (Karte #699).
//
// Wozu: Die Bearbeiter-Auswahl im Browser (Owner/Assignees, Karte #698)
// braucht die Personen des Boards auch für Editor und Viewer. Die ACL-Liste
// selbst bleibt dem Owner vorbehalten (F-261: keine Adressen, keine Rollen,
// keine Topologie an Nicht-Owner). Hier entsteht deshalb nur eine Namensliste.
//
// Wer zählt: Personen mit WIRKSAMER Rolle (wie GetRole) — eigene ACL-Zeile
// oder Zugriff über eine erlaubte Domain-Freigabe (ADR-0132) —, die schon ein
// Konto haben (users-Zeile). Eingeladene ohne Konto haben keinen Namen, nur
// eine Adresse; sie erscheinen nicht.
//
// Was nie herausgeht: E-Mail-Adressen. users.name ist der AnzeigeName des
// Proxys und fällt dort auf die E-Mail zurück, wenn kein Name kommt (Karte
// #577). Ein Name mit "@" wird deshalb verworfen — sonst wäre es F-261 durch
// die Hintertür.

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// MaxMitgliederNamen begrenzt die Antwort. Eine Domain-Freigabe kann eine
// ganze Firma umfassen; die Auswahlliste im Browser braucht keine tausend
// Einträge, und die Grenze hält die Abfrage billig.
const MaxMitgliederNamen = 500

// MitgliederNamen liefert die Anzeigenamen der Personen mit wirksamer Rolle
// auf dem Board, ohne Adressen, ohne Rollen, ohne Doubletten (Gross-/
// Kleinschreibung zählt nicht), sortiert, höchstens MaxMitgliederNamen.
// gekappt meldet, dass es womöglich mehr Namen gibt, als zurückkommen
// (Kritiker KS-1: das Abschneiden wird nicht verschwiegen). Es ist eine obere
// Schätzung (VS-1): Liefert eine Domain-Abfrage ihr Limit voll, steht es auf
// true, auch wenn Doubletten die eindeutigen Namen danach auf 500 drücken.
// Sortiert wird in SQL mit lower() — das kennt nur ASCII; bei mehr als 500
// Konten kann ein anderer Randname herausfallen als die Endsortierung in Go
// vermuten lässt (VS-3, hingenommen: reproduzierbar ist es trotzdem).
//
// Leere Namen und Namen mit "@" filtert schon die Abfrage — sonst verdrängten
// sie vor dem LIMIT echte Namen (KS-1). Die ACL-Personen kommen in EINER
// Abfrage je Block statt einer je Person (KS-2).
func (r *AclRepo) MitgliederNamen(ctx context.Context, boardID string) (namen []string, gekappt bool, err error) {
	eintraege, err := r.ListByBoard(ctx, boardID)
	if err != nil {
		return nil, false, fmt.Errorf("acl.MitgliederNamen: %w", err)
	}
	var personen []string
	var domains []string
	for _, e := range eintraege {
		s := NormalizeEmail(e.UserEmail)
		if IstDomainSubjekt(s) {
			d := strings.TrimPrefix(s, "@")
			// ADR-0132 §4: ein Domain-Eintrag wirkt nur, solange die Domain
			// erlaubt ist — sonst gibt er niemandem Zugriff.
			if gueltigeDomain(d) && domainErlaubt(d) {
				domains = append(domains, "@"+d)
			}
			continue
		}
		if s != "" {
			personen = append(personen, s)
		}
	}

	const nameGueltig = ` AND trim(name) <> '' AND instr(name, '@') = 0` // VS-6: auch reine Leerzeichen
	sammle := func(q string, args ...any) (int, error) {
		rows, err := r.db.QueryContext(ctx, q, args...)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return n, err
			}
			namen = append(namen, s)
			n++
		}
		return n, rows.Err()
	}
	const block = 400 // unter SQLITE_MAX_VARIABLE_NUMBER, auch bei alten Builds
	for i := 0; i < len(personen); i += block {
		teil := personen[i:min(i+block, len(personen))]
		args := make([]any, len(teil))
		for j, p := range teil {
			args[j] = p
		}
		q := `SELECT name FROM users WHERE email IN (?` + strings.Repeat(",?", len(teil)-1) + `)` + nameGueltig
		if _, err := sammle(q, args...); err != nil {
			return nil, false, fmt.Errorf("acl.MitgliederNamen personen: %w", err)
		}
	}
	for _, d := range domains {
		// Endvergleich statt LIKE: kein Escaping von % und _ nötig. Die
		// Domain ist geprüft (gueltigeDomain), users.email normalisiert
		// (F-260). Sortiert VOR dem LIMIT, damit eine grosse Domain
		// reproduzierbar dieselben Namen liefert statt einer zufälligen
		// Teilmenge (KS-1).
		n, err := sammle(`SELECT name FROM users WHERE substr(email, -length(?)) = ?`+nameGueltig+
			` ORDER BY lower(name) LIMIT ?`, d, d, MaxMitgliederNamen+1)
		if err != nil {
			return nil, false, fmt.Errorf("acl.MitgliederNamen domain: %w", err)
		}
		if n > MaxMitgliederNamen {
			gekappt = true
		}
	}
	out, ueber := bereinigeMitgliederNamen(namen)
	return out, gekappt || ueber, nil
}

// bereinigeMitgliederNamen: leere Namen und Namen mit "@" (E-Mail-Rückfall)
// verwerfen (zweite Linie hinter dem SQL-Filter), Doubletten ohne Rücksicht
// auf Gross-/Kleinschreibung entfernen — zwei Personen mit gleichem Namen
// erscheinen dann einmal (KS-3, Grenze des Label-Modells) —, sortieren, kappen.
func bereinigeMitgliederNamen(roh []string) (out []string, gekappt bool) {
	out = []string{}
	gesehen := map[string]bool{}
	for _, n := range roh {
		n = strings.TrimSpace(n)
		if n == "" || strings.Contains(n, "@") {
			continue
		}
		k := strings.ToLower(n)
		if gesehen[k] {
			continue
		}
		gesehen[k] = true
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	if len(out) > MaxMitgliederNamen {
		return out[:MaxMitgliederNamen], true
	}
	return out, false
}
