package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// runBoardpaar pflegt das Board-Paar intern/extern von der Kommandozeile
// (ADR-0140 §4, Karte #788) — dasselbe wie der Admin-Reiter „Board-Paar“.
//
//	boardpaar liste [--json]
//	boardpaar seite [--setzen aus|innen|aussen]
//	boardpaar koppeln --seite innen --board <id>
//	boardpaar koppeln --seite aussen --board <id> --link https://<innen>/#board=<id>
//	boardpaar entkoppeln --seite innen|aussen --board <id>
//	boardpaar token-neu --typ abs|abe|abr|abb --name <name> [--darf-anlegen]
//	boardpaar token-widerrufen --typ abs|abe|abr|abb --name <name>
//	boardpaar kopie-anlegen --board <id> --ziel <name> [--link https://<innen>/#board=<id>]
//
// `kopie-anlegen` (Karte #825, ADR-0141 §3) legt innen den Auftrag „Externe
// Kopie auf Ziel X anlegen“ an — wie die Admin-Aktion. Ohne --link gilt
// ABOARD_PUBLIC_BASE_URL + „/#board=<id>“. Angelegt wird außen vom Skript.
//
// Erneuern gibt es hier nicht: Das Skript tauscht sein Token selbst über
// POST /agent/boardpaar/erneuern (Karte #796). `token-widerrufen` trifft das
// aktuelle Token eines Namens und nimmt einen Vorgänger in der Übergangszeit mit.
//
// `seite` steht nicht in der Aufzählung von ADR-0140 §4: Ohne ihn liesse sich
// die Instanz nur über den Admin-Dialog umschalten, und das verlangt eine
// angemeldete Admin-Person — beim Einrichten eines Servers per Skript gibt es
// die noch nicht.
func runBoardpaar(args []string) error {
	return runBoardpaarMit(context.Background(), args, os.Stdout)
}

const boardpaarVerwendung = "Verwendung: aboard-server boardpaar liste|seite|koppeln|entkoppeln|token-neu|token-widerrufen|kopie-anlegen [Optionen]"

func runBoardpaarMit(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(boardpaarVerwendung)
	}
	// Unterbefehl vor storage.Open prüfen (Kritiker K zu F-1934): ein
	// Tippfehler legt sonst erst eine leere DB an.
	switch args[0] {
	case "liste", "seite", "koppeln", "entkoppeln", "token-neu", "token-widerrufen", "kopie-anlegen":
	case "-h", "-help", "--help", "help":
		_, err := fmt.Fprintln(out, boardpaarVerwendung+"\nOptionen je Unterbefehl: aboard-server boardpaar <unterbefehl> --help")
		return err
	default:
		return fmt.Errorf("unbekannter Unterbefehl %q (liste, seite, koppeln, entkoppeln, token-neu, token-widerrufen, kopie-anlegen)", args[0])
	}
	fs := flag.NewFlagSet("boardpaar "+args[0], flag.ContinueOnError)
	fs.SetOutput(out)
	dbPath := fs.String("db", "", "DB-Pfad (Standard: ABOARD_DB / "+defaultDBPath+")")
	alsJSON := fs.Bool("json", false, "liste: maschinenlesbar (nie ein Token)")
	setzen := fs.String("setzen", "", "seite: aus, innen oder aussen")
	seite := fs.String("seite", "", "koppeln/entkoppeln: innen oder aussen")
	board := fs.String("board", "", "koppeln/entkoppeln/kopie-anlegen: Board-ID auf diesem Server")
	link := fs.String("link", "", "koppeln --seite aussen, kopie-anlegen: Adresse des internen Boards, https://<host>/#board=<id>")
	typ := fs.String("typ", "", "token-neu/token-widerrufen: abs (Ausgang innen), abe (Eingang außen), abr (Rückweg innen) oder abb (Sicherungen lesen)")
	darfAnlegen := fs.Bool("darf-anlegen", false, "token-neu --typ abe: darf außen Kopien anlegen (ADR-0141 §3)")
	ziel := fs.String("ziel", "", "kopie-anlegen: Name des externen Servers, wie das Skript ihn meldet")
	name := fs.String("name", "", "token-neu/token-widerrufen: Name des Zugangs (1–64 Zeichen: Buchstaben, Ziffern, . _ -)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}
	// Optionen, die zum Unterbefehl nicht passen, ablehnen statt still zu
	// übergehen (F-1934: ein überlesenes Flag hiess dort ein falsches Token).
	erlaubt := map[string][]string{
		"liste":            {"db", "json"},
		"seite":            {"db", "setzen"},
		"koppeln":          {"db", "seite", "board", "link"},
		"entkoppeln":       {"db", "seite", "board"},
		"token-neu":        {"db", "typ", "name", "darf-anlegen"},
		"token-widerrufen": {"db", "typ", "name"},
		"kopie-anlegen":    {"db", "board", "ziel", "link"},
	}[args[0]]
	var fremd []string
	fs.Visit(func(f *flag.Flag) {
		for _, e := range erlaubt {
			if f.Name == e {
				return
			}
		}
		fremd = append(fremd, "--"+f.Name)
	})
	if len(fremd) > 0 {
		return fmt.Errorf("boardpaar %s kennt %s nicht", args[0], strings.Join(fremd, ", "))
	}
	if (args[0] == "koppeln" || args[0] == "entkoppeln") && (*seite != api.BoardpaarSeiteInnen && *seite != api.BoardpaarSeiteAussen || *board == "") {
		return fmt.Errorf("boardpaar %s braucht --seite innen|aussen und --board <id>", args[0])
	}
	if args[0] == "koppeln" && (*seite == api.BoardpaarSeiteAussen) != (*link != "") {
		return errors.New("--link gehört zu koppeln --seite aussen, und nur dorthin")
	}
	if (args[0] == "token-neu" || args[0] == "token-widerrufen") && (*typ == "" || *name == "") {
		return fmt.Errorf("boardpaar %s braucht --typ abs|abe|abr|abb und --name", args[0])
	}
	if args[0] == "kopie-anlegen" && (*board == "" || *ziel == "") {
		return errors.New("boardpaar kopie-anlegen braucht --board <id> und --ziel <name>")
	}
	setzenGesetzt := false
	fs.Visit(func(f *flag.Flag) { setzenGesetzt = setzenGesetzt || f.Name == "setzen" })
	var neueSeite string
	if args[0] == "seite" && setzenGesetzt { // auch --setzen "" (= aus, Kritiker #788 K-6)
		var err error
		if neueSeite, err = api.PruefeBoardpaarSeite(*setzen); err != nil {
			return err
		}
	}

	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}
	// Wie acl (Kritiker S-8): storage.Open legt eine fehlende Datenbank an —
	// ein Tippfehler in --db koppelte sonst in einer frischen, leeren Datei.
	if _, err := os.Stat(resolvedDB); err != nil {
		return fmt.Errorf("Datenbank %s nicht gefunden (--db/ABOARD_DB prüfen): %w", resolvedDB, err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: resolvedDB})
	if err != nil {
		return err
	}
	defer db.Close()
	repo := storage.NewBoardpaarRepo(db)
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	instanz := api.BoardpaarSeite(ctx, cfg)

	switch args[0] {
	case "liste":
		innen, err := repo.InnenListe(ctx)
		if err != nil {
			return err
		}
		aussen, err := repo.AussenListe(ctx)
		if err != nil {
			return err
		}
		zugaenge, err := repo.TokenListe(ctx)
		if err != nil {
			return err
		}
		ziele, err := repo.ZieleListe(ctx)
		if err != nil {
			return err
		}
		auftraege, err := repo.AuftraegeListe(ctx)
		if err != nil {
			return err
		}
		if *alsJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"seite": instanz, "innen": innen, "aussen": aussen, "zugaenge": zugaenge,
				"ziele": ziele, "auftraege": auftraege})
		}
		fmt.Fprintf(out, "Seite dieser Instanz: %s\n", instanz)
		fmt.Fprintf(out, "Partnerliste innen (%d):\n", len(innen))
		for _, e := range innen {
			fmt.Fprintf(out, "  %s\tseit %s\t%s\n", e.BoardID, e.Seit, abgleichText("Abruf", e.LetzteAbfrage, e.LetzterFehlgrund))
		}
		fmt.Fprintf(out, "Partnerliste außen (%d):\n", len(aussen))
		for _, e := range aussen {
			art := "Stufe 1"
			if e.Kopie {
				art = "Kopie"
			}
			fmt.Fprintf(out, "  %s\t%s\t→ %s\tseit %s\t%s\n", e.BoardID, art, e.InnenLinkBasis, e.Seit, abgleichText("Eingang", e.LetzterEingang, e.LetzterFehlgrund))
		}
		if len(ziele) > 0 || len(auftraege) > 0 {
			namen := make([]string, 0, len(ziele))
			for _, z := range ziele {
				namen = append(namen, z.Name)
			}
			fmt.Fprintf(out, "Ziele (vom Skript gemeldet): %s\n", strings.Join(namen, ", "))
			fmt.Fprintf(out, "Aufträge „Kopie anlegen“ (%d):\n", len(auftraege))
			for _, a := range auftraege {
				fmt.Fprintf(out, "  %s\t%s → %s\t%s seit %s\n", a.ID, a.BoardID, a.Ziel, a.Status, a.AngelegtAt)
			}
		}
		fmt.Fprintf(out, "Zugänge (%d):\n", len(zugaenge))
		for _, z := range zugaenge {
			fmt.Fprintf(out, "  %s_ %s\t%s\n", z.Typ, z.Name, zugangText(z))
		}
		return nil
	case "seite":
		if !setzenGesetzt {
			fmt.Fprintln(out, instanz)
			return nil
		}
		if err := api.PruefeSeitenwechsel(ctx, cfg, repo, neueSeite); err != nil {
			return err
		}
		if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, neueSeite, false, "cli"); err != nil {
			return err
		}
		fmt.Fprintf(out, "Seite dieser Instanz: %s (vorher %s). Wirkt ohne Neustart; offene Browser zeigen es nach dem Neuladen.\n", neueSeite, instanz)
		return nil
	case "koppeln":
		if *seite == api.BoardpaarSeiteInnen {
			err = repo.InnenKoppeln(ctx, *board, "cli")
		} else {
			var e *storage.BoardpaarAussen
			if e, err = repo.AussenKoppeln(ctx, *board, *link, "cli"); err == nil {
				*link = e.InnenLinkBasis
			}
		}
		if err != nil {
			return koppelMeldung(*board, err)
		}
		if *seite == api.BoardpaarSeiteAussen {
			fmt.Fprintf(out, "Board %s außen gekoppelt, internes Gegenstück %s.\n", *board, *link)
		} else {
			fmt.Fprintf(out, "Board %s innen gekoppelt: der Ausgang liefert seine Schatten.\n", *board)
		}
		// Kein Fehler, aber ein Hinweis: Die Kopplung wirkt erst, wenn die
		// Instanz auf derselben Seite steht (ADR-0140 §4).
		if instanz != *seite {
			fmt.Fprintf(out, "Achtung: Diese Instanz steht auf „%s“, nicht auf „%s“ — bis zum Umschalten (boardpaar seite --setzen %s) wirkt die Kopplung nicht.\n", instanz, *seite, *seite)
		}
		return nil
	case "entkoppeln":
		if *seite == api.BoardpaarSeiteInnen {
			err = repo.InnenEntkoppeln(ctx, *board)
		} else {
			err = repo.AussenEntkoppeln(ctx, *board)
		}
		if errors.Is(err, storage.ErrBoardpaarNichtGekoppelt) {
			return fmt.Errorf("Board %s ist %s nicht gekoppelt", *board, *seite)
		}
		if err != nil {
			return err
		}
		if *seite == api.BoardpaarSeiteAussen {
			fmt.Fprintf(out, "Board %s außen entkoppelt; alle Schatten dieses Boards gehen mit der Kopplung.\n", *board)
		} else {
			fmt.Fprintf(out, "Board %s innen entkoppelt; der nächste Abruf scheitert mit 404. Die Schatten außen bleiben, bis das externe Board entkoppelt wird — Reihenfolge: erst außen, dann innen.\n", *board)
		}
		return nil
	case "token-neu":
		klartext, t, err := repo.TokenAnlegenMit(ctx, *typ, *name, "cli", *darfAnlegen)
		switch {
		case errors.Is(err, storage.ErrBoardpaarTyp):
			return errors.New("--typ: abs (Ausgang innen), abe (Eingang außen), abr (Rückweg innen) oder abb (Sicherungen lesen)")
		case errors.Is(err, storage.ErrBoardpaarAnlegeRecht):
			return errors.New("--darf-anlegen gibt es nur für --typ abe")
		case errors.Is(err, storage.ErrBoardpaarTokenName):
			return errors.New("--name: 1–64 Zeichen aus Buchstaben, Ziffern, . _ -")
		case errors.Is(err, storage.ErrBoardpaarTokenVergeben):
			return fmt.Errorf("es gibt schon einen aktiven Zugang %s_ %q", *typ, *name)
		case err != nil:
			return err
		}
		recht := ""
		if t.DarfAnlegen {
			recht = " mit dem Recht „darf Kopien anlegen“"
		}
		fmt.Fprintf(out, "Zugang %s_ %q%s angelegt.\nToken (wird nur jetzt angezeigt):\n%s\n", t.Typ, t.Name, recht, klartext)
		return nil
	case "kopie-anlegen":
		if instanz != api.BoardpaarSeiteInnen {
			return fmt.Errorf("externe Kopien legt nur die Seite innen an (diese Instanz: %s)", instanz)
		}
		l := *link
		if l == "" {
			basis, berr := api.ValidiereOeffentlicheBasis(os.Getenv("ABOARD_PUBLIC_BASE_URL"))
			if berr != nil || basis == "" {
				return errors.New("--link fehlt, und ABOARD_PUBLIC_BASE_URL ist nicht gesetzt oder ungültig")
			}
			l = strings.TrimRight(basis, "/") + "/#board=" + url.QueryEscape(*board)
		}
		a, err := repo.AuftragAnlegen(ctx, *board, *ziel, l, "cli")
		switch {
		case errors.Is(err, storage.ErrBoardpaarZielName):
			return errors.New("--ziel: 1–64 Zeichen aus Buchstaben, Ziffern, . _ -")
		case errors.Is(err, storage.ErrBoardpaarAuftragOffen):
			return fmt.Errorf("für Board %s und Ziel %q liegt schon ein offener Auftrag", *board, *ziel)
		case err != nil:
			return koppelMeldung(*board, err)
		}
		fmt.Fprintf(out, "Auftrag %s angelegt: Kopie von Board %s auf %q. Das Skript legt sie beim nächsten Lauf an.\n", a.ID, a.BoardID, a.Ziel)
		return nil
	default: // token-widerrufen
		t, err := repo.TokenAktivNachName(ctx, *typ, *name)
		if errors.Is(err, storage.ErrBoardpaarTyp) {
			return errors.New("--typ: abs (Ausgang innen), abe (Eingang außen), abr (Rückweg innen) oder abb (Sicherungen lesen)")
		}
		if errors.Is(err, storage.ErrBoardpaarTokenUnbekannt) {
			return fmt.Errorf("kein aktiver Zugang %s_ %q", *typ, *name)
		}
		if err != nil {
			return err
		}
		if err := repo.TokenWiderrufen(ctx, t.ID); err != nil {
			return err
		}
		fmt.Fprintf(out, "Zugang %s_ %q widerrufen.\n", *typ, t.Name)
		return nil
	}
}

func koppelMeldung(board string, err error) error {
	switch {
	case errors.Is(err, storage.ErrBoardpaarBoardUnbekannt):
		return fmt.Errorf("Board %s gibt es auf diesem Server nicht", board)
	case errors.Is(err, storage.ErrBoardpaarNichtFreigebbar):
		return errors.New("das Service-Board lässt sich nicht koppeln")
	}
	return err
}

// abgleichText: letzter Abruf (innen) bzw. Eingang (außen) eines gekoppelten
// Boards für die Liste (F-2019). Ein Fehlgrund steht vorn und groß, damit er
// in einer langen Liste auffällt.
func abgleichText(was, zeit, fehlgrund string) string {
	switch {
	case zeit == "":
		return "noch kein " + was
	case fehlgrund != "":
		return fmt.Sprintf("FEHLER beim letzten %s (%s): %s", was, zeit, fehlgrund)
	default:
		return fmt.Sprintf("letzter %s %s, ok", was, zeit)
	}
}

// zugangText: Zustand eines Zugangs für `boardpaar liste` — aktiv, erneuert
// (gilt noch bis …) oder widerrufen, dazu die letzte Nutzung und, wenn das
// Token aus einer Erneuerung stammt, wann und von wo (Karte #796).
func zugangText(z storage.BoardpaarToken) string {
	zustand := "aktiv"
	switch {
	case z.RevokedAt != "":
		zustand = "widerrufen " + z.RevokedAt
	case z.GiltBis != "":
		// Verify F5: wie der Admin-Reiter — nach der Übergangszeit „abgelöst“.
		zustand = "erneuert, gilt bis " + z.GiltBis
		if bis, err := time.Parse(time.RFC3339Nano, z.GiltBis); err == nil && !bis.After(time.Now()) {
			zustand = "abgelöst (erneuert, galt bis " + z.GiltBis + ")"
		}
	}
	genutzt := "nie genutzt"
	if z.LastUsedAt != "" {
		genutzt = "zuletzt " + z.LastUsedAt
	}
	text := zustand + ", " + genutzt
	if z.ErneuertAus != "" {
		von := z.ErneuertVon
		if von == "" {
			von = "?"
		}
		text += ", erneuert " + z.CreatedAt + " von " + von
	}
	return text
}
