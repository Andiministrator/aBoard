package boardbackup

// verwaist.go — Backups gelöschter Boards abräumen (F-1125, Karte #545).
//
// Der Scheduler sichert und rotiert nur Boards, die `Boards.List` liefert.
// `DELETE /boards/{id}` entfernte die Anhänge, nicht die Backups — die
// Inhalte eines gelöschten Boards lagen deshalb unbegrenzt unter
// <BackupBaseDir>/<id>/ (Datenschutz, Anfrage A-8 eines Team-Betriebs).
//
// GRABSTEIN STATT ABLEITUNG (Kritiker Sprint 70, K-A1): Die erste Fassung
// schloss „gelöscht“ aus dem FEHLEN eines Boards in der Datenbank. Nach einem
// DB-Verlust oder dem Zurückspielen eines älteren Server-Backups fehlen aber
// alle später angelegten Boards — und ihre Board-Backups sind dann oft die
// einzige Kopie. Der Abräumlauf hätte genau die gelöscht. Jetzt gilt:
//
//   - `DELETE /boards/{id}` legt über MarkiereGeloescht einen Grabstein
//     `.geloescht-seit` in das Backup-Verzeichnis des Boards. Nur das ist die
//     Aussage „dieses Board wurde absichtlich gelöscht“.
//   - Der Nachtlauf entfernt nur Verzeichnisse MIT Grabstein, und erst wenn
//     der Grabstein älter als KeepDays ist, keine Backup-Datei jünger — und
//     der Grabstein mindestens KeepDays gezählte Läufe erlebt hat (F-1135).
//   - Verzeichnisse ohne Board und ohne Grabstein bleiben unangetastet; der
//     Lauf meldet ihre Zahl (Hinweis auf DB-Verlust oder Rückspielung).
//
// SICHERUNGEN GEGEN DAS FALSCHE LÖSCHEN — jede für sich hinreichend:
//   - Nur Namen, die eine gültige Board-ID sein können (kein Punkt vorn,
//     keine Pfadzeichen); der Pfad entsteht aus Root + diesem Namen.
//   - Nur Verzeichnisse, die mindestens eine Backup-Datei DIESES Boards
//     enthalten, und keine Symlinks.
//   - Existiert das Board (Liste des Laufs ODER BoardRepo.Get), wird ein
//     Grabstein entfernt statt befolgt — etwa nach Wiederherstellung mit
//     derselben ID. Ein DB-Fehler bei der Einzelabfrage führt zu nichts.
//   - Gelöscht werden nur Backup-Dateien (istBackupDatei) und der
//     Grabstein. Das Verzeichnis selbst nur per os.Remove, das an einem nicht
//     leeren Verzeichnis scheitert — nie os.RemoveAll.

// UHRSPRUNG (F-1135, Karte #581, Sprint 74): Ein Pi ohne gepufferte Uhr
// startet mit falscher Zeit, bis NTP sie stellt. Die mtime des Grabsteins
// ist dann womöglich Tage zu alt — nach dem Abgleich wäre die Frist sofort
// abgelaufen. Deshalb zählt der Grabstein zusätzlich die Läufe, die er
// erlebt hat (zaehleLauf). Der erste Lauf, der ihn sieht, setzt nur den
// Anker „zuletzt“ (Kritiker Sprint 74: zählte er schon mit, lag bei
// keep_days = 1 zwischen Löschen und Abräumen womöglich keine Minute).
// Danach wird ein Lauf nur gezählt, wenn er mindestens grabsteinLaufAbstand
// nach dem zuletzt gezählten liegt, gemessen an der Uhr des jeweiligen
// Laufs. Zwischen Anker und Abräumen liegen damit mindestens
// keep_days × grabsteinLaufAbstand, solange die Uhr dazwischen nicht
// vorspringt. Eine nachgehende Uhr zählt nicht; eine vorgehende blockiert
// das Zählen, bis die echte Zeit das gespeicherte „zuletzt“ eingeholt hat —
// das meldet der Lauf im Log. Beide Fehler verzögern; beschleunigen kann nur
// ein Vorsprung der Uhr, um höchstens einen Lauf je Sprung.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// grabsteinName markiert ein Backup-Verzeichnis, dessen Board per API
// gelöscht wurde. Maßgeblich ist die mtime der Datei (Zeitpunkt des
// Löschens). Der führende Punkt schliesst eine Kollision mit Backup-Namen
// aus, die immer mit "boardbackup-" beginnen.
const grabsteinName = ".geloescht-seit"

// grabsteinLaufAbstand ist der Mindestabstand zwischen zwei gezählten Läufen
// (F-1135). Der Nachtlauf kommt einmal täglich; 20 h lassen Spielraum für
// Verschiebungen des Cron-Zeitpunkts, zählen aber keinen zweiten Lauf am
// selben Tag.
const grabsteinLaufAbstand = 20 * time.Hour

// boardIDVerzeichnisRe — dieselbe Form wie die Board-IDs der API
// (safeIDRe in api/attachments.go, safePathComponent im storage-Paket):
// keine Punkte, keine Schrägstriche, kein führender Punkt.
var boardIDVerzeichnisRe = regexp.MustCompile(`^[a-zA-Z0-9\-_]{1,128}$`)

// VerwaistBericht zählt, was ein Abräumlauf getan hat (für Log und Tests).
type VerwaistBericht struct {
	Entfernt       int // Verzeichnisse vollständig abgeräumt
	Dateien        int // gelöschte Backup-Dateien
	Entwarnt       int // Grabstein entfernt, weil das Board wieder existiert
	Liegengelassen int // Frist abgelaufen, aber etwas blieb liegen (siehe Log)
	OhneGrabstein  int // Board fehlt, aber kein Grabstein — bleibt unangetastet
	// ZuletztVoraus: Grabsteine, deren „zuletzt“ mehr als eine Stunde vor der
	// Uhr dieses Laufs liegt — ein früherer Lauf lief mit vorgehender Uhr.
	ZuletztVoraus int
}

// raeumeVerwaisteAb geht einmal über das Backup-Root. `vorhanden` ist die
// Board-Liste DIESES Laufs. Fehler werden geloggt und brechen nur die
// Behandlung des einen Verzeichnisses ab — im Zweifel bleibt alles liegen.
func raeumeVerwaisteAb(ctx context.Context, deps SchedulerDeps, vorhanden map[string]bool) VerwaistBericht {
	var b VerwaistBericht
	if deps.BackupBaseDir == "" || deps.KeepDays <= 0 || deps.Boards == nil {
		return b
	}
	// F-1192: Mit nicht gestellter Uhr weder zählen noch löschen — auch der
	// Anker eines neuen Grabsteins trüge sonst die falsche Zeit.
	if !uhr.Erlaubt("board-backup-verwaist", deps.Logger) {
		return b
	}
	eintraege, err := os.ReadDir(deps.BackupBaseDir)
	if err != nil {
		if !os.IsNotExist(err) {
			deps.Logger("board-backup: verwaiste Backups: Root lesen: %v", err)
		}
		return b
	}
	jetzt := deps.jetzt()
	grenze := jetzt.Add(-time.Duration(deps.KeepDays) * 24 * time.Hour)

	for _, e := range eintraege {
		if ctx.Err() != nil {
			return b
		}
		// DirEntry.IsDir folgt keinem Symlink — ein verlinktes Verzeichnis
		// wird damit bewusst übergangen.
		if !e.IsDir() || !boardIDVerzeichnisRe.MatchString(e.Name()) {
			continue
		}
		id := e.Name()
		dir := filepath.Join(deps.BackupBaseDir, id)
		markerPfad := filepath.Join(dir, grabsteinName)

		if vorhanden[id] {
			entferneGrabstein(deps, &b, markerPfad, id)
			continue
		}

		dateien, juengste, ok := backupDateienVon(dir, id)
		if !ok || len(dateien) == 0 {
			continue // nichts von uns darin — nicht unser Verzeichnis
		}

		// Existenz per Einzelabfrage. Ein Board, das zwischen Liste und jetzt
		// entstanden ist, zählt als vorhanden; ein DB-Fehler führt zu nichts.
		if _, gerr := deps.Boards.Get(ctx, id); gerr == nil {
			entferneGrabstein(deps, &b, markerPfad, id)
			continue
		} else if !errors.Is(gerr, storage.ErrBoardNotFound) {
			deps.Logger("board-backup: board=%s: Existenz nicht prüfbar, Backups bleiben: %v", id, gerr)
			continue
		}

		mfi, merr := os.Lstat(markerPfad)
		if merr != nil {
			if !os.IsNotExist(merr) {
				deps.Logger("board-backup: board=%s: Grabstein lesen: %v", id, merr)
				continue
			}
			// K-A1: Kein Grabstein heisst NICHT „gelöscht“. Das Board fehlt nur
			// in dieser Datenbank — nach DB-Verlust oder Rückspielung sind diese
			// Backups womöglich die einzige Kopie. Nichts anfassen.
			b.OhneGrabstein++
			continue
		}
		if !mfi.Mode().IsRegular() {
			continue // Grabstein ist keine Datei — nicht unser Marker
		}
		// F-1135: den Lauf auf dem Grabstein zählen. Scheitert das, wird in
		// diesem Lauf nichts gelöscht — der Zähler wäre sonst unbelegt.
		laeufe, voraus, zerr := zaehleLauf(markerPfad, mfi.ModTime(), jetzt)
		if zerr != nil {
			deps.Logger("board-backup: board=%s: Grabstein fortschreiben: %v", id, zerr)
			continue
		}
		if voraus {
			b.ZuletztVoraus++
		}
		if mfi.ModTime().After(grenze) || juengste.After(grenze) || laeufe < deps.KeepDays {
			continue // Frist läuft noch (Zeit oder gezählte Läufe)
		}

		entfernt := 0
		fehler := false
		for _, pfad := range dateien {
			if rerr := os.Remove(pfad); rerr != nil && !os.IsNotExist(rerr) {
				deps.Logger("board-backup: board=%s: verwaistes Backup entfernen: %v", id, rerr)
				fehler = true
				continue
			}
			entfernt++
		}
		b.Dateien += entfernt
		if fehler {
			b.Liegengelassen++
			continue // Grabstein bleibt, nächster Lauf versucht es erneut
		}
		if rerr := os.Remove(markerPfad); rerr != nil && !os.IsNotExist(rerr) {
			deps.Logger("board-backup: board=%s: Grabstein entfernen: %v", id, rerr)
		}
		if rerr := os.Remove(dir); rerr != nil {
			// Nicht leer: Dort liegt etwas, das nicht von uns ist. Stehen
			// lassen und sagen — Löschen ist hier nicht unsere Entscheidung.
			b.Liegengelassen++
			deps.Logger("board-backup: board=%s: %d verwaiste Backup(s) entfernt, Verzeichnis bleibt "+
				"(enthält fremde Dateien): %v", id, entfernt, rerr)
			continue
		}
		b.Entfernt++
		deps.Logger("board-backup: board=%s: Frist abgelaufen — %d Backup(s) des gelöschten Boards entfernt",
			id, entfernt)
	}
	if b.ZuletztVoraus > 0 {
		deps.Logger("board-backup: %d Grabstein(e) tragen einen gezählten Lauf, der nach der Uhr dieses Laufs "+
			"liegt — ein früherer Lauf lief mit vorgehender Uhr. Sie zählen erst weiter, wenn die Uhr ihn "+
			"eingeholt hat; bis dahin bleiben die Backups gelöschter Boards liegen (F-1135).", b.ZuletztVoraus)
	}
	if b.OhneGrabstein > 0 {
		deps.Logger("board-backup: %d Backup-Verzeichnis(se) gehören zu keinem Board der Datenbank und tragen "+
			"keinen Grabstein — sie bleiben unangetastet. Nach einer DB-Rückspielung ist das erwartet; "+
			"sonst prüfen, ob die Datenbank vollständig ist.", b.OhneGrabstein)
	}
	return b
}

// entferneGrabstein nimmt einen Grabstein zurück, wenn das Board (wieder)
// existiert — etwa nach einer Wiederherstellung mit derselben ID.
func entferneGrabstein(deps SchedulerDeps, b *VerwaistBericht, markerPfad, id string) {
	if _, serr := os.Lstat(markerPfad); serr != nil {
		return
	}
	if rerr := os.Remove(markerPfad); rerr == nil {
		b.Entwarnt++
		deps.Logger("board-backup: board=%s existiert wieder — Grabstein entfernt", id)
	}
}

// MarkiereGeloescht legt den Grabstein für ein per API gelöschtes Board
// (K-A1). Aufgerufen aus DELETE /boards/{id}, nach dem erfolgreichen
// DB-Delete. Existiert kein Backup-Verzeichnis des Boards, gibt es nichts zu
// markieren — es wird NICHTS angelegt. Ein vorhandener Grabstein bleibt
// unverändert (die Frist beginnt beim ersten Löschen). baseDir leer oder id
// ungültig → nichts tun.
func MarkiereGeloescht(baseDir, id string, jetzt time.Time) error {
	if baseDir == "" || !boardIDVerzeichnisRe.MatchString(id) {
		return nil
	}
	dir := filepath.Join(baseDir, id)
	fi, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !fi.IsDir() {
		return nil // Symlink oder Datei — nicht unser Verzeichnis
	}
	err = schreibeMarker(filepath.Join(dir, grabsteinName), jetzt)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

// HatGrabstein sagt, ob im Backup-Verzeichnis des Boards ein Grabstein liegt
// (Board per API gelöscht, Scheduler räumt erst nach der Frist ab). Nur lesend;
// ungültige Kennung, fehlender Grabstein oder jeder Fehler → false.
func HatGrabstein(baseDir, id string) bool {
	if baseDir == "" || !boardIDVerzeichnisRe.MatchString(id) {
		return false
	}
	fi, err := os.Lstat(filepath.Join(baseDir, id, grabsteinName))
	return err == nil && fi.Mode().IsRegular()
}

// grabsteinStand ist der Inhalt eines Grabsteins: Zeile 1 der Zeitpunkt des
// Löschens (wie seit F-1125), danach seit F-1135 die gezählten Läufe und der
// Zeitpunkt des zuletzt gezählten. Ältere Grabsteine haben nur Zeile 1 und
// gelten als „0 Läufe“ — sie warten also die volle Zahl an Läufen ab
// (Ausfall in Richtung Nichtstun).
type grabsteinStand struct {
	seit    string
	laeufe  int
	zuletzt time.Time
}

func leseGrabstein(pfad string) (grabsteinStand, error) {
	roh, err := os.ReadFile(pfad)
	if err != nil {
		return grabsteinStand{}, err
	}
	var g grabsteinStand
	for i, zeile := range strings.Split(strings.TrimRight(string(roh), "\n"), "\n") {
		if i == 0 {
			g.seit = zeile
			continue
		}
		schluessel, wert, ok := strings.Cut(zeile, "=")
		if !ok {
			continue
		}
		switch schluessel {
		case "laeufe":
			if n, cerr := strconv.Atoi(wert); cerr == nil && n >= 0 {
				g.laeufe = n
			}
		case "zuletzt":
			if t, perr := time.Parse(time.RFC3339, wert); perr == nil {
				g.zuletzt = t
			}
		}
	}
	return g, nil
}

// zaehleLauf verbucht den Lauf `jetzt` auf dem Grabstein und liefert die Zahl
// der gezählten Läufe. Ohne „zuletzt“ (neuer oder alter Grabstein) setzt er
// nur den Anker; danach zählt er, wenn `jetzt` mindestens
// grabsteinLaufAbstand nach „zuletzt“ liegt. voraus meldet ein „zuletzt“ mehr
// als eine Stunde nach `jetzt`. Geschrieben wird über eine Nachbardatei und
// rename; die mtime bekommt danach wieder den Wert `seit` — sie ist der
// Zeitpunkt des Löschens, auf dem die Frist beruht.
func zaehleLauf(pfad string, seit, jetzt time.Time) (laeufe int, voraus bool, err error) {
	g, err := leseGrabstein(pfad)
	if err != nil {
		return 0, false, err
	}
	switch {
	case g.zuletzt.IsZero():
		g.zuletzt = jetzt // Anker, noch kein gezählter Lauf
	case g.zuletzt.After(jetzt.Add(time.Hour)):
		return g.laeufe, true, nil
	case jetzt.Sub(g.zuletzt) < grabsteinLaufAbstand:
		return g.laeufe, false, nil // zu früh
	default:
		g.laeufe++
		g.zuletzt = jetzt
	}
	if g.seit == "" {
		g.seit = seit.UTC().Format(time.RFC3339)
	}
	inhalt := fmt.Sprintf("%s\nlaeufe=%d\nzuletzt=%s\n", g.seit, g.laeufe, g.zuletzt.UTC().Format(time.RFC3339))
	neu := pfad + ".neu"
	if werr := os.WriteFile(neu, []byte(inhalt), 0o644); werr != nil {
		_ = os.Remove(neu)
		return 0, false, werr
	}
	if cerr := os.Chtimes(neu, seit, seit); cerr != nil {
		_ = os.Remove(neu)
		return 0, false, cerr
	}
	if rerr := os.Rename(neu, pfad); rerr != nil {
		_ = os.Remove(neu)
		return 0, false, rerr
	}
	return g.laeufe, false, nil
}

// backupDateienVon liefert die Backup-Dateien (fertige und unfertige) eines
// Board-Verzeichnisses samt der jüngsten mtime. ok=false, wenn das
// Verzeichnis nicht lesbar ist.
func backupDateienVon(dir, id string) (pfade []string, juengste time.Time, ok bool) {
	eintraege, err := os.ReadDir(dir)
	if err != nil {
		return nil, time.Time{}, false
	}
	prefix := "boardbackup-" + id + "-"
	for _, e := range eintraege {
		if !e.Type().IsRegular() || !istBackupDatei(e.Name(), prefix) {
			continue
		}
		fi, ierr := e.Info()
		if ierr != nil {
			return nil, time.Time{}, false // lieber gar nicht als halb
		}
		if fi.ModTime().After(juengste) {
			juengste = fi.ModTime()
		}
		pfade = append(pfade, filepath.Join(dir, e.Name()))
	}
	return pfade, juengste, true
}

// schreibeMarker legt den Grabstein exklusiv an und setzt seine mtime auf
// `jetzt` (injizierbar, damit Tests die Frist ohne Warten prüfen können).
func schreibeMarker(pfad string, jetzt time.Time) error {
	f, err := os.OpenFile(pfad, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(jetzt.UTC().Format(time.RFC3339) + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return os.Chtimes(pfad, jetzt, jetzt)
}
