package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// runNutzer: Konto-Verwaltung von der Kommandozeile (Karte #1052, ADR-0148 §7;
// Karte #1076, ADR-0149 §5.7).
//
//	nutzer umziehen --von <alt> --nach <neu> [--dry-run]
//	nutzer einladen --email <adresse>
//	nutzer bindung-loesen --email <adresse> --anbieter <kürzel>
//
// Schreibt die Adresse eines Kontos in allen Spalten um, die sie als Schlüssel
// tragen (storage.NutzerUmziehen). Gilt in jeder Betriebsart — auch bei
// Forward-Auth, wenn sich die Adresse in Authelia ändert. Der Rückweg ist der
// Befehl mit vertauschten Adressen.
func runNutzer(args []string) error {
	return runNutzerMit(context.Background(), args, os.Stdout)
}

func runNutzerMit(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "einladen" {
		return runNutzerEinladen(ctx, args[1:], out)
	}
	if len(args) > 0 && args[0] == "bindung-loesen" {
		return runNutzerBindungLoesen(ctx, args[1:], out)
	}
	if len(args) == 0 || args[0] != "umziehen" {
		return errors.New("Unterbefehl fehlt oder unbekannt: nutzer umziehen --von <alt> --nach <neu> [--dry-run] | nutzer einladen --email <adresse> | nutzer bindung-loesen --email <adresse> --anbieter <kürzel>")
	}
	fs := flag.NewFlagSet("nutzer umziehen", flag.ContinueOnError)
	dbPath := fs.String("db", "", "DB-Pfad (Standard: ABOARD_DB / "+defaultDBPath+")")
	von := fs.String("von", "", "alte Adresse (Pflicht)")
	nach := fs.String("nach", "", "neue Adresse (Pflicht)")
	trocken := fs.Bool("dry-run", false, "nur anzeigen, was umgeschrieben würde")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}
	if *von == "" || *nach == "" {
		return errors.New("nutzer umziehen braucht --von und --nach")
	}
	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}
	// Wie bei `acl`: storage.Open legt eine fehlende Datenbank an — bei einem
	// Tippfehler im Pfad würde sonst an einer leeren Datei „kein Konto“ gemeldet.
	if _, err := os.Stat(resolvedDB); err != nil {
		return fmt.Errorf("Datenbank %s nicht gefunden (--db/ABOARD_DB prüfen): %w", resolvedDB, err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: resolvedDB})
	if err != nil {
		return err
	}
	defer db.Close()

	erg, err := storage.NutzerUmziehen(ctx, db, *von, *nach, *trocken)
	if err != nil {
		var k *storage.UmzugAclKonflikt
		if errors.As(err, &k) {
			fmt.Fprintf(out, "Abbruch, nichts geschrieben: %s hat schon Freigaben auf Board(s), auf denen auch %s steht:\n", storage.NormalizeEmail(*nach), storage.NormalizeEmail(*von))
			for _, b := range k.Boards {
				fmt.Fprintf(out, "  %s\n", b)
			}
			fmt.Fprintln(out, "Die Einträge der neuen Adresse zuerst entfernen (acl set / Board-Freigaben), dann erneut.")
		}
		if errors.Is(err, storage.ErrUmzugZielBesetzt) {
			fmt.Fprintln(out, "Hat sich die neue Adresse schon angemeldet, entstand dort ein eigenes (meist leeres) Konto. "+
				"Es zuerst im Admin-Bereich löschen, dann erneut umziehen.")
		}
		return err
	}
	kopf := "Umgezogen"
	if erg.Trockenlauf {
		kopf = "Trockenlauf (nichts geschrieben), würde umziehen"
	}
	fmt.Fprintf(out, "%s: %s -> %s\n", kopf, erg.Von, erg.Nach)
	for _, k := range erg.Schluessel() {
		fmt.Fprintf(out, "  %-45s %d\n", k, erg.Gezaehlt[k])
	}
	fmt.Fprintf(out, "  %-45s %d\n", "sitzungen (gelöscht, Neuanmeldung nötig)", erg.SitzungenGeloescht)
	// Kritik A-7: Im OIDC-Betrieb entscheidet die Zulassungsliste, wer
	// hereinkommt — der Umzug ändert sie nicht.
	if api.AnmeldungOIDCAktiv() { // Verify V-3: nur im OIDC-Betrieb
		fmt.Fprintf(out, "Hinweis (eingebaute Anmeldung): %s in die Vorab-Liste (ABOARD_OIDC_ALLOWED/ABOARD_OIDC_ZUGELASSEN) aufnehmen und %s dort streichen, dann neu starten.\n", erg.Nach, erg.Von)
	}
	// K2 F-E: Die Bootstrap-Adresse ist immer zugelassen und wird bei jedem
	// Start Owner des Service-Boards — sie muss mit umziehen.
	if storage.NormalizeEmail(os.Getenv("ABOARD_BOOTSTRAP_OWNER")) == erg.Von {
		fmt.Fprintf(out, "Hinweis: ABOARD_BOOTSTRAP_OWNER steht noch auf %s — auf %s umstellen, sonst bleibt die alte Adresse zugelassen und Owner des Service-Boards.\n", erg.Von, erg.Nach)
	}
	return nil
}

// runNutzerEinladen: Einladungslink von der Kommandozeile (ADR-0149 §5.7,
// Kritik B2) — der Weg zum ersten Admin auf einem leeren Server, und für den
// Betreiber jederzeit. Der Link steht nur in der Ausgabe dieses Befehls, nie
// im Log des Servers. Wie über die Admin-API: keine Admin-Rolle; Admin wird
// nur die erste bestätigte Person überhaupt (#1077).
func runNutzerEinladen(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("nutzer einladen", flag.ContinueOnError)
	dbPath := fs.String("db", "", "DB-Pfad (Standard: ABOARD_DB / "+defaultDBPath+")")
	email := fs.String("email", "", "Adresse der eingeladenen Person (Pflicht)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("nutzer einladen braucht --email")
	}
	if !api.AnmeldungOIDCAktiv() {
		return errors.New("Einladungen gibt es nur mit der eingebauten Anmeldung (ABOARD_AUTH_MODE=builtin)")
	}
	basis := os.Getenv("ABOARD_PUBLIC_BASE_URL")
	if err := pruefeAnmeldungBasis(basis); err != nil {
		return err
	}
	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}
	if _, err := os.Stat(resolvedDB); err != nil {
		return fmt.Errorf("Datenbank %s nicht gefunden (--db/ABOARD_DB prüfen): %w", resolvedDB, err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: resolvedDB})
	if err != nil {
		return err
	}
	defer db.Close()

	e, err := storage.NewEinladungRepo(db).Einladen(ctx, *email)
	switch {
	case errors.Is(err, storage.ErrEinladungAdresse):
		return fmt.Errorf("%q ist keine gültige E-Mail-Adresse", *email)
	case errors.Is(err, storage.ErrEinladungHatZugang):
		return fmt.Errorf("%s hat schon Zugang — keine Einladung nötig", storage.NormalizeEmail(*email))
	case errors.Is(err, storage.ErrEinladungKontoWeiter):
		return fmt.Errorf("%s hat die Einladung schon eingelöst — im Admin-Bereich bestätigen oder zurücksetzen", storage.NormalizeEmail(*email))
	case err != nil:
		return err
	}
	if e.Erneut {
		fmt.Fprintf(out, "Neuer Einladungslink für %s (der vorige gilt nicht mehr).\n", e.Email)
	} else {
		fmt.Fprintf(out, "Einladungslink für %s:\n", e.Email)
	}
	fmt.Fprintf(out, "\n  %s\n\n", api.EinladungsLink(basis, e.Token))
	fmt.Fprintf(out, "Gültig bis %s, nur einmal nutzbar. Gib ihn nur an diese Person weiter.\n", e.Ablauf.Format("02.01.2006 15:04 UTC"))
	fmt.Fprintln(out, "Gibt es noch keinen Admin, wird die Person mit dem Einlösen aktiv und Admin; sonst bestätigt ein Admin den Zugang.")
	return nil
}

// runNutzerBindungLoesen löst die Bindung einer Person an einen Anbieter, ohne
// Boards, Rechte oder Konto anzufassen (F-2877, Karte #1083). Der Weg für den
// Betreiber, wenn die Person ihr altes Anbieter-Konto nicht mehr hat und sich
// deshalb nicht in die Konto-Einstellungen einloggen kann, um es dort zu lösen.
// Alle Sitzungen der Person enden. Anders als in den Konto-Einstellungen gibt es
// keine Prüfung auf den letzten Weg: Wer auf der Vorab-Liste steht, bindet beim nächsten Login neu.
func runNutzerBindungLoesen(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("nutzer bindung-loesen", flag.ContinueOnError)
	dbPath := fs.String("db", "", "DB-Pfad (Standard: ABOARD_DB / "+defaultDBPath+")")
	email := fs.String("email", "", "Adresse der Person (Pflicht)")
	anbieter := fs.String("anbieter", "", "Kürzel des Anbieters, z. B. standard, google (Pflicht)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}
	if *email == "" || *anbieter == "" {
		return errors.New("nutzer bindung-loesen braucht --email und --anbieter")
	}
	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}
	if _, err := os.Stat(resolvedDB); err != nil {
		return fmt.Errorf("Datenbank %s nicht gefunden (--db/ABOARD_DB prüfen): %w", resolvedDB, err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: resolvedDB})
	if err != nil {
		return err
	}
	defer db.Close()
	userID, err := storage.NewAnbieterBindungRepo(db, nil).LoesenOhnePruefung(ctx, *email, *anbieter)
	switch {
	case errors.Is(err, storage.ErrUserNotFound):
		return fmt.Errorf("%s hat keinen Zugang (keine Person mit dieser Adresse)", storage.NormalizeEmail(*email))
	case errors.Is(err, storage.ErrBindungFehlt):
		return fmt.Errorf("%s hat bei %q keine Bindung", storage.NormalizeEmail(*email), *anbieter)
	case err != nil:
		return err
	}
	// Audit-Eintrag (Sicherheitsprüfung #1083): Person-ID statt Adresse, kein
	// Geheimnis. Scheitert er, steht das in der Ausgabe, die Lösung gilt trotzdem.
	if aerr := storage.NewConfigRepo(db, nil, uuid.NewString).AppendAudit(ctx, "personen/"+userID, "",
		api.AuditActionKontoBindungGeloestCLI, "", "anbieter="+*anbieter+";ergebnis=ok"); aerr != nil {
		fmt.Fprintf(out, "Hinweis: Audit-Eintrag konnte nicht geschrieben werden: %v\n", aerr)
	}
	fmt.Fprintf(out, "Bindung von %s an %q gelöst, alle Sitzungen der Person sind beendet. Boards und Rechte sind unverändert.\n", storage.NormalizeEmail(*email), *anbieter)
	fmt.Fprintln(out, "Wer auf der Vorab-Liste steht, bindet beim nächsten Anbieter-Login neu (mit dem Konto, das dann die Adresse bestätigt).")
	fmt.Fprintln(out, "Ohne Vorab-Eintrag und ohne Passwort kommt die Person nicht mehr herein — dann im Admin-Bereich zurücksetzen (neue Einladung).")
	return nil
}
