package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Andiministrator/aBoard/server/storage"
)

// runACL verwaltet Board-Freigaben von der Kommandozeile (F-1854).
//
// Anlass: Beim Umstieg vom Einzel- auf den Mehrnutzer-Betrieb tragen die
// Alt-Boards keinen ACL-Eintrag, und seedDefaultBoard fasst einen bestehenden
// Bestand bewusst nicht an. Bis hierher ging das nur über die HTTP-API im
// Einzelbetrieb (POST /boards/{id}/acl) vor dem Umschalten. Automatisch
// zugeteilt wird nicht — verborgene Boards können gewollt sein
// (Widerlegungsbedingung des Findings).
//
//	acl orphans                         Boards ohne Owner auflisten
//	acl adopt --email <adresse>         <adresse> wird Owner ALLER Boards ohne Owner
//	acl set --board <id> --email <adresse> --role owner|editor|viewer
func runACL(args []string) error {
	return runACLMit(context.Background(), args, os.Stdout)
}

func runACLMit(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("Unterbefehl fehlt: orphans, adopt oder set")
	}
	fs := flag.NewFlagSet("acl "+args[0], flag.ContinueOnError)
	dbPath := fs.String("db", "", "DB-Pfad (Standard: ABOARD_DB / "+defaultDBPath+")")
	email := fs.String("email", "", "Personenadresse (adopt, set)")
	board := fs.String("board", "", "Board-ID (set)")
	role := fs.String("role", "", "owner, editor oder viewer (set)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}
	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}

	// Personenadresse wie beim Bootstrap-Owner prüfen: kein Domain-Schlüssel,
	// kein Tippfehler ohne '@'.
	person := func() (string, error) {
		e := storage.NormalizeEmail(*email)
		if !storage.IstPersonenadresse(e) {
			return "", fmt.Errorf("--email %q ist keine Personenadresse", *email)
		}
		return e, nil
	}

	switch args[0] {
	case "orphans", "adopt", "set":
	default:
		return fmt.Errorf("unbekannter Unterbefehl %q (orphans, adopt, set)", args[0])
	}

	// Kritiker S-8: storage.Open legt eine fehlende Datenbank an. Bei einem
	// Tippfehler in --db/ABOARD_DB meldete `adopt` dann „0 zugeteilt“ an einer
	// frischen, leeren Datei — und der Betreiber hielte es für erledigt.
	if _, err := os.Stat(resolvedDB); err != nil {
		return fmt.Errorf("Datenbank %s nicht gefunden (--db/ABOARD_DB prüfen): %w", resolvedDB, err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: resolvedDB})
	if err != nil {
		return err
	}
	defer db.Close()
	acl := storage.NewAclRepo(db)

	switch args[0] {
	case "orphans":
		ohne, err := storage.BoardsOhneOwner(ctx, db)
		if err != nil {
			return err
		}
		for _, b := range ohne {
			fmt.Fprintf(out, "%s\t%s\n", b.ID, b.Title)
		}
		fmt.Fprintf(out, "%d Board(s) ohne Owner\n", len(ohne))
		return nil
	case "adopt":
		e, err := person()
		if err != nil {
			return err
		}
		ohne, err := storage.BoardsOhneOwner(ctx, db)
		if err != nil {
			return err
		}
		for _, b := range ohne {
			if err := acl.Set(ctx, b.ID, e, storage.RoleOwner); err != nil {
				return fmt.Errorf("Board %s: %w", b.ID, err)
			}
			fmt.Fprintf(out, "owner %s ← %s (%s)\n", b.ID, e, b.Title)
		}
		fmt.Fprintf(out, "%d Board(s) zugeteilt\n", len(ohne))
		return nil
	default: // set
		e, err := person()
		if err != nil {
			return err
		}
		if *board == "" || !storage.ValidRole(*role) {
			return errors.New("set braucht --board und --role owner|editor|viewer")
		}
		// Kritiker S-9: den letzten Owner nicht herabstufen — das Board wäre
		// danach wieder für niemanden verwaltbar.
		if *role != storage.RoleOwner {
			if alt, ok, err := acl.GetRole(ctx, *board, e); err != nil {
				return err
			} else if ok && alt == storage.RoleOwner {
				eintraege, err := acl.ListByBoard(ctx, *board)
				if err != nil {
					return err
				}
				owner := 0
				for _, x := range eintraege {
					if x.Role == storage.RoleOwner {
						owner++
					}
				}
				if owner <= 1 {
					return fmt.Errorf("%s ist der einzige Owner von %s — erst einen weiteren Owner setzen", e, *board)
				}
			}
		}
		if err := acl.Set(ctx, *board, e, *role); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s ← %s\n", *role, *board, e)
		return nil
	}
}
