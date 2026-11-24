package uhr

// loeschpfade_test.go — F-1205: Jeder Löschpfad des Servers ist eingeordnet.
//
// Wissen §38 (knowledge/topics/nebenlaeufigkeit-und-lesefenster.md) teilt die
// Löschpfade in zwei Spalten: nach Alter (rechnet gegen die Systemuhr, braucht
// das Gate uhr.Erlaubt) und nicht nach Alter. Die Liste entstand zweimal aus
// den Befunden statt aus dem Code und war zweimal unvollständig (F-1201,
// F-1210). Dieser Test macht aus der Liste eine Prüfung: Er sucht im Code
// jede Funktion, die löscht, und verlangt ihre Einordnung.
//
// Was als „löscht“ gilt: ein Zeichenketten-Literal mit „DELETE FROM“, ein
// Bezeichner auf eine Paket-Konstante/-Variable mit solchem Literal (das
// Activity-Log hält sein SQL in Konstanten — ohne diesen Schritt fehlten
// gerade Frist und Kappung) oder ein Aufruf von os.Remove/os.RemoveAll im
// Funktionsrumpf (auch in Funktionsliteralen darin). Nicht erfasst: Löschen über Bibliotheken (IMAP-Expunge
// u. ä.) — solche Pfade stehen trotzdem in nachAlter, damit ihr Gate geprüft
// wird, und der Test meldet es, wenn ihre Funktion verschwindet.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fundstelle ist der Schlüssel einer Funktion: Pfad relativ zu server/ und
// Funktionsname, bei Methoden mit Empfängertyp („storage/x.go:Repo.Evict“).
type fundstelle = string

type funktion struct {
	loescht  bool     // Rumpf enthält DELETE FROM oder os.Remove(All)
	gate     bool     // Rumpf ruft uhr.Erlaubt
	aufrufe  []string // Namen aller aufgerufenen Funktionen/Methoden (ohne Paket/Empfänger)
	position string
}

// scanneServer liest alle Nicht-Test-Dateien unter server/ (ohne uhr/ selbst).
func scanneServer(t *testing.T) map[fundstelle]*funktion {
	t.Helper()
	wurzel := ".."
	ergebnis := map[fundstelle]*funktion{}
	fset := token.NewFileSet()
	// Erster Durchgang: je Paketverzeichnis die Namen der Konstanten und
	// Variablen, deren Wert ein Literal mit DELETE FROM enthält.
	type geparst struct {
		rel   string
		datei *ast.File
	}
	var dateien []geparst
	loeschNamen := map[string]map[string]bool{} // Verzeichnis → Name
	err := filepath.WalkDir(wurzel, func(pfad string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "web" || name == "node_modules" || strings.HasPrefix(name, ".") && pfad != wurzel {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(pfad, ".go") || strings.HasSuffix(pfad, "_test.go") {
			return nil
		}
		datei, err := parser.ParseFile(fset, pfad, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(wurzel, pfad)
		rel = filepath.ToSlash(rel)
		dateien = append(dateien, geparst{rel, datei})
		verz := filepath.ToSlash(filepath.Dir(rel))
		for _, decl := range datei.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, wert := range vs.Values {
					if i < len(vs.Names) && enthaeltDelete(wert) {
						if loeschNamen[verz] == nil {
							loeschNamen[verz] = map[string]bool{}
						}
						loeschNamen[verz][vs.Names[i].Name] = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("server/ lesen: %v", err)
	}
	for _, g := range dateien {
		rel, datei := g.rel, g.datei
		verz := filepath.ToSlash(filepath.Dir(rel))
		for _, decl := range datei.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				name = empfaenger(fd.Recv.List[0].Type) + "." + name
			}
			f := &funktion{position: fset.Position(fd.Pos()).String()}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if enthaeltDelete(x) {
						f.loescht = true
					}
				case *ast.Ident:
					if loeschNamen[verz][x.Name] {
						f.loescht = true
					}
				case *ast.CallExpr:
					switch fn := x.Fun.(type) {
					case *ast.SelectorExpr:
						if pkg, ok := fn.X.(*ast.Ident); ok {
							if pkg.Name == "os" && (fn.Sel.Name == "Remove" || fn.Sel.Name == "RemoveAll") {
								f.loescht = true
							}
							if pkg.Name == "uhr" && fn.Sel.Name == "Erlaubt" {
								f.gate = true
							}
						}
						f.aufrufe = append(f.aufrufe, fn.Sel.Name)
					case *ast.Ident:
						f.aufrufe = append(f.aufrufe, fn.Name)
					}
				}
				return true
			})
			ergebnis[rel+":"+name] = f
		}
	}
	return ergebnis
}

// enthaeltDelete meldet, ob der Ausdruck ein Zeichenketten-Literal mit
// „DELETE FROM“ enthält (auch in Verkettungen).
func enthaeltDelete(e ast.Node) bool {
	gefunden := false
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(strings.ToUpper(s), "DELETE FROM") {
				gefunden = true
			}
		}
		return !gefunden
	})
	return gefunden
}

func empfaenger(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return empfaenger(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return empfaenger(x.X)
	}
	return "?"
}

// gatePfad ist ein Löschpfad nach Alter, dessen Gate beim Aufrufer sitzt:
// Aufrufer ruft die löschende Funktion (auch über ein Funktionsliteral), Gate
// ist der Aufrufer selbst oder eine Funktion, die er aufruft, und ruft
// uhr.Erlaubt.
type gatePfad struct {
	aufrufer fundstelle
	gate     fundstelle
}

// nachAlter: löscht, was älter als eine Frist ist — braucht das Gate. Ein
// leerer gatePfad heißt: die Funktion ruft uhr.Erlaubt selbst.
var nachAlter = map[fundstelle][]gatePfad{
	"storage/activities.go:ActivityRepo.trim":                  nil,
	"storage/attachments.go:AttachmentStore.sweepStaleStaging": nil,
	// F-3030 (#1149): Absturzreste nachgeholter Purges, älter als eine Stunde; Gate in der Funktion.
	// F-3050: beim Serverstart (beimStart) ohne Alter und ohne Gate — kein Frist-Löschen, nur Kartenlage.
	"storage/attachments_purge.go:AttachmentStore.raeumeBeiseiteAuf": nil,
	// F-3048: leere Hüllen nach Alter, nur aus raeumeBeiseiteAuf (Gate dort).
	"storage/attachments_purge.go:leereHuelleEntfernen": {{aufrufer: "storage/attachments_purge.go:AttachmentStore.raeumeBeiseiteAuf", gate: "storage/attachments_purge.go:AttachmentStore.raeumeBeiseiteAuf"}},
	// Sprint-92-Verify 1d1ee964 (F-2127): Teil-Dateien abgebrochener Uploads,
	// älter als eine Stunde; das Gate (uhr.Erlaubt) sitzt in der Funktion.
	"storage/attachments.go:raeumeTeile":        nil,
	"boardbackup/rotation.go:Rotate":            nil,
	"boardbackup/verwaist.go:raeumeVerwaisteAb": nil,
	"storage/imap_state.go:ImapProcessedRepo.EvictOlderThan": {
		{"mail/worker.go:RunWorker", "mail/worker.go:evictWennFaellig"},
	},
	"storage/notifications.go:NotificationLogRepo.EvictOlderThan": {
		{"notifications/digest.go:DigestScheduler.evictOld", "notifications/digest.go:DigestScheduler.evictOld"},
	},
	"storage/llm_conversations.go:LLMConversationRepo.EvictExpired": {
		{"retention/scheduler.go:Scheduler.RunOnce", "retention/scheduler.go:Scheduler.RunOnce"},
	},
	"storage/server_config.go:ConfigRepo.EvictAuditExpired": {
		{"retention/scheduler.go:Scheduler.RunOnce", "retention/scheduler.go:Scheduler.RunOnce"},
	},
	"storage/op_ids.go:OpIDRepo.EvictExpired": {
		{"retention/scheduler.go:Scheduler.RunOnce", "retention/scheduler.go:Scheduler.RunOnce"},
	},
	// Karte #827 (ADR-0141 §5.2): bestätigte Einträge des Board-Paar-Journals,
	// 30 Tage nach der Bestätigung.
	"storage/boardpaar_journal.go:BoardRepo.JournalAufraeumen": {
		{"retention/scheduler.go:Scheduler.RunOnce", "retention/scheduler.go:Scheduler.RunOnce"},
	},
	// Karte #685: nie gesendete Entwürfe der Ausgangsschleuse.
	"storage/schleuse_auftraege.go:SchleuseAuftragRepo.EntwuerfeAufraeumen": nil,
	// Karte #757: abgelaufene Einträge der verbrauchten Bestätigungscodes.
	"storage/loesch_codes.go:LoeschCodeRepo.Verbrauche": nil,
	// Karte #1051 (ADR-0148 §2): abgelaufene Sitzungen der eingebauten
	// Anmeldung; das Gate sitzt in beiden Funktionen.
	"storage/sitzungen.go:SitzungRepo.Pruefen":              nil,
	"storage/sitzungen.go:SitzungRepo.AbgelaufeneAbraeumen": nil,
	// Karte #1076 (ADR-0149 §7): abgelaufene/eingelöste Links und verbrauchte
	// Wiederherstellungscodes nach 30 Tagen; das Gate sitzt in der Funktion.
	"storage/einladungen.go:EinladungRepo.AbgelaufeneAbraeumen": nil,
	// Karte #783: abgelaufene Vorschläge des internen Agenten (24 h).
	"storage/vorschlaege.go:VorschlagRepo.AbgelaufeneLoeschen": {
		{"api/vorschlaege.go:BoardsAPI.VorschlaegeAufraeumen", "api/vorschlaege.go:BoardsAPI.VorschlaegeAufraeumen"},
	},
}

// nichtNachAlter: löscht, aber nicht nach Alter — mit Grund. Wer eine
// Funktion hier einträgt, schreibt dazu, warum die Uhr keine Rolle spielt.
var nichtNachAlter = map[fundstelle]string{
	"storage/sitzungen.go:SitzungRepo.Beenden":                               "Abmelden: löscht genau die Sitzung des Cookies (Karte #1051)",
	"storage/einladungen.go:EinladungRepo.Zurueckziehen":                     "Admin zieht eine Einladung zurück: genau ein Konto im Zustand invited, unabhängig vom Alter (Karte #1076)",
	"storage/konten_einloesen.go:EinladungRepo.Einloesen":                    "Einladung einlösen: ersetzt Wiederherstellungscodes und beendet Sitzungen der Person, unabhängig vom Alter (Karte #1077)",
	"storage/konten_einloesen.go:EinladungRepo.Zuruecksetzen":                "Admin setzt ein Konto zurück: Codes, Bindungen und Sitzungen der Person, ausdrückliche Handlung (Karte #1077)",
	"storage/konten_einloesen.go:KontoRepo.Ablehnen":                         "Admin lehnt ein wartendes Konto ab: Konto samt Links, Codes und Bindungen, ausdrückliche Handlung (ADR-0149 §5.5, Karte #1077)",
	"storage/konten_einloesen.go:sitzungenDerAdresseBeenden":                 "Einlösen, Sperren, Ablehnen, Zurücksetzen: alle Sitzungen einer Adresse, unabhängig vom Alter (Karte #1077)",
	"storage/konten.go:sitzungenDesKontosBeenden":                            "Passwort setzen / 2FA neu einrichten: beendet alle Sitzungen der Person, unabhängig vom Alter (F-2891, Karte #1092)",
	"storage/konten.go:KontoRepo.TOTPEinrichten":                             "2FA neu einrichten: ersetzt alle Wiederherstellungscodes eines Kontos, unabhängig vom Alter (Karte #1075)",
	"storage/konto_einstellungen.go:sitzungenAusser":                         "Passwort ändern / 2FA neu: alle Sitzungen der Person bis auf die laufende, unabhängig vom Alter, in derselben Transaktion (Karte #1083)",
	"storage/konto_einstellungen.go:codesSetzen":                             "2FA neu / Codes neu: ersetzt alle Wiederherstellungscodes eines Kontos, unabhängig vom Alter (Karte #1083)",
	"storage/konto_einstellungen.go:SitzungRepo.AndereBeenden":               "„Andere Sitzungen beenden“: alle Sitzungen der Person bis auf die laufende, ausdrückliche Handlung (Karte #1083)",
	"storage/konto_einstellungen.go:AnbieterBindungRepo.Verknuepfen":         "Weg verknüpfen: ersetzt die Bindung der Person beim selben Anbieter, ausdrückliche Handlung, unabhängig vom Alter (Karte #1083)",
	"storage/konto_einstellungen.go:AnbieterBindungRepo.Loesen":              "Weg lösen: genau eine Bindung der Person, ausdrückliche Handlung, unabhängig vom Alter (Karte #1083, F-2877)",
	"storage/konto_einstellungen.go:AnbieterBindungRepo.LoesenOhnePruefung":  "Betreiber-CLI nutzer bindung-loesen: genau eine Bindung, ausdrückliche Handlung, unabhängig vom Alter (Karte #1083, F-2877)",
	"storage/sitzungen.go:SitzungRepo.BeendeNichtZugelassene":                "Start: Sitzungen von Adressen, die das Tor nicht mehr hereinlässt (Liste, Konto), nicht nach Alter (Kritik A-2, ADR-0149 §5.1)",
	"storage/nutzer_umzug.go:NutzerUmziehen":                                 "Umzug auf andere Adresse: löscht alle Sitzungen der alten Adresse, unabhängig vom Alter, in derselben Transaktion (Karte #1052)",
	"storage/boardpaar_journal.go:BoardpaarJournalSchreiben":                 "Verschmelzen: löscht einen nicht abgeholten Journal-Eintrag per seq, der durch die neue Änderung wirkungslos wurde (Karte #827)",
	"storage/boardpaar_journal.go:BoardpaarWartendErledigt":                  "Eingang außen: wartende Zeilen, die er markiert oder nach ursprung/rueckweg_bis entfernt hat — entscheidet nach der Menge, nicht nach der Uhr (Karte #827 E2)",
	"storage/activities.go:ActivityRepo.kappen":                              "zählt Einträge, Ordnung nach rowid (F-1212)",
	"backup/backup.go:applyRetention":                                        "zählt nach Anzahl, Reihenfolge aus dem Dateinamen",
	"storage/attachments.go:AttachmentStore.ReconcileStagedTransfers":        "entscheidet nach der Datenbank, nicht nach der Uhr (F-1216)",
	"api/boardbackup.go:BoardBackupAPI.deleteFile":                           "Admin löscht eine Backup-Datei per Name",
	"api/boardbackup.go:BoardBackupAPI.runNow":                               "Temp-Datei nach Fehlschlag",
	"backup/backup.go:Run":                                                   "Temp-Kopie und halbes Ergebnis nach Fehlschlag",
	"backup/backup.go:vacuumIntoCopy":                                        "Temp-Datei",
	"backup/restore.go:writeFile":                                            "Temp-Datei beim atomaren Schreiben",
	"backup/restore.go:extractEntry":                                         "eigene Zwischendatei der DB nach Fehlschlag oder Übernahme (F-1888)",
	"boardbackup/builder.go:BackupBoard":                                     "Temp-Datei und halbfertiges Backup (K7)",
	"boardbackup/restore.go:RestoreBackup":                                   "Temp-Verzeichnis des Restores",
	"boardbackup/restore.go:restoreOverwrite":                                "Anhänge des überschriebenen Boards",
	"boardbackup/scheduler.go:runBackupAllBoards":                            "leerer -pending-Platzhalter",
	"boardbackup/verwaist.go:entferneGrabstein":                              "Grabstein, wenn das Board wieder existiert",
	"boardbackup/verwaist.go:zaehleLauf":                                     "Temp-Datei beim atomaren Schreiben des Laufzählers",
	"main_agent.go:entferneAltenSocket":                                      "Socket-Datei beim Start",
	"storage/acl.go:AclRepo.Delete":                                          "Einzel-Löschung per ID",
	"storage/boardpaar.go:BoardpaarRepo.InnenEntkoppeln":                     "Admin entkoppelt ein Board (Karte #778/#788)",
	"storage/boardpaar.go:BoardpaarRepo.AussenEntkoppeln":                    "Admin entkoppelt ein externes Board (Karte #788)",
	"storage/schatten.go:ersetzeAufConn":                                     "PUT ersetzt die Schattenmenge eines Boards, Fehlende fallen weg — keine Alterslöschung (Karte #779; seit #825 gemeinsamer Teil von Ersetze und ErsetzeKopie)",
	"storage/boardpaar_kopie.go:BoardpaarRepo.ZieleSetzen":                   "PUT ersetzt die Menge der Ziele, nicht gemeldete fallen weg — keine Alterslöschung (Karte #825)",
	"storage/boardpaar_kopie_loeschen.go:BoardpaarRepo.KopieLoeschen":        "Admin löscht außen eine externe Kopie (Notweg) — ausdrückliche Handlung mit Bestätigung, keine Frist (Karte #831)",
	"storage/boardpaar_zeile_loeschen.go:BoardpaarRepo.ZugangZeileEntfernen": "Admin entfernt eine abgeschlossene Zugangszeile — ausdrückliche Handlung mit Rückfrage; die Frist eines abgelösten Zugangs prüft sie nur bei gestellter Uhr (uhrGestellt, sonst ErrBoardpaarUhr/503), widerrufene hängen an keiner Frist (Karte #848)",
	"storage/boardpaar_kopie.go:BoardpaarTeamEntziehenTx":                    "Admin nimmt Subjekte aus boardpaar.team, ihre viewer-/editor-Zeilen auf Kopien fallen weg — Konfig-Folge, keine Frist (Sprint-94-Kritik S-2)",
	"storage/boardpaar_kopie.go:BoardpaarRepo.AuftragZurueckziehen":          "Admin zieht einen offenen Auftrag zurück — ausdrückliche Handlung, keine Frist (Karte #825)",
	"storage/attachments.go:AttachmentStore.Delete":                          "Einzel-Löschung per ID",
	"storage/attachments.go:AttachmentStore.DeleteBoard":                     "Anhänge eines gelöschten Boards",
	"storage/attachments.go:AttachmentStore.DeleteCard":                      "Anhänge einer gelöschten Karte",
	"storage/attachments_purge.go:AttachmentStore.LoescheKarteBestaetigt":    "Anhänge einer gelöschten Karte, nach zweiter Prüfung (F-3030)",
	"storage/attachments_purge.go:AttachmentStore.schiebeZurueck":            "Leeres Beiseite-Verzeichnis nach dem Zurückschieben (F-3030); bei belegter id bleibt es (F-3051)",
	"storage/attachments_purge.go:huelleEntfernen":                           "Leere Hüllverzeichnisse in .purge-beiseite (os.Remove nur wenn leer, F-3046)",
	"storage/attachments.go:AttachmentStore.Save":                            "Temp-Datei beim atomaren Schreiben",
	"storage/attachments.go:AttachmentStore.StageCardAttachments":            "Staging-Reste nach Fehlschlag desselben Transfers",
	"storage/attachments.go:StagedAttachments.Abort":                         "Staging eines abgebrochenen Transfers",
	"storage/attachments.go:StagedAttachments.Commit":                        "Staging nach übernommenem Transfer",
	"storage/boards.go:BoardRepo.Delete":                                     "Einzel-Löschung per ID, samt Schleuse-Aufträgen des Boards (#685, KS-6)",
	"storage/clients_projects.go:ClientRepo.HardDelete":                      "Einzel-Löschung per ID",
	"storage/clients_projects.go:ProjectRepo.HardDelete":                     "Einzel-Löschung per ID",
	"storage/ical_tokens.go:ICalTokenRepo.Delete":                            "Einzel-Löschung per ID",
	"storage/llm_conversations.go:LLMConversationRepo.DeleteConversation":    "Einzel-Löschung per ID",
	"storage/llm_keys.go:LLMKeyRepo.Delete":                                  "Einzel-Löschung per ID",
	"storage/schleuse.go:SchleuseSchluesselRepo.Delete":                      "Einzel-Löschung per ID (eigene Person, #684)",
	"storage/schleuse.go:SchleuseFreigabeRepo.Loeschen":                      "Einzel-Löschung per ID durch den Admin (#684)",
	"storage/schleuse.go:SchleuseFreigabeRepo.Aendern":                       "Wechsel von Anbieter/Basis-URL löscht die Schlüssel dieser Freigabe (#684, ADR-0134 §2)",
	"storage/schleuse_auftraege.go:SchleuseAuftragRepo.Loeschen":             "Einzel-Löschung per ID (eigene Person, #685)",
	"storage/schleuse_auftraege.go:SchleuseAuftragRepo.LoescheOffene":        "Einzel-Löschung per ID: Entwürfe an Karten, die es im Board nicht mehr gibt (F-1623)",
	"storage/notifications.go:NotificationConfigRepo.Delete":                 "Einzel-Löschung per ID",
	"storage/server_config.go:ConfigRepo.DeleteMitFolge":                     "Einzel-Löschung per Schlüssel (Delete ruft es ohne Folge)",
	"storage/spam.go:SpamRepo.RemoveSpamSender":                              "Einzel-Löschung per Absender",
	"storage/user_loeschung.go:UserRepo.Loeschen":                            "Nutzer-Löschung auf Anforderung (A-8)",
	"storage/vorschlaege.go:VorschlagRepo.Entfernen":                         "Einzel-Löschung per id, wenn ein Mensch bestätigt oder ablehnt — nie nach Alter (#783)",
	"storage/loesch_codes.go:LoeschCodeRepo.Freigeben":                       "Einzel-Löschung per Zufallswert: Code nach abgelehntem Schreiben wieder gültig (#757)",
}

// methodenName liefert den Namen ohne Pfad und Empfänger („EvictOlderThan“).
func methodenName(k fundstelle) string {
	n := k[strings.LastIndex(k, ":")+1:]
	return n[strings.LastIndex(n, ".")+1:]
}

func enthaelt(liste []string, s string) bool {
	for _, x := range liste {
		if x == s {
			return true
		}
	}
	return false
}

// TestLoeschpfade_AlleEingeordnet (F-1205): Jede löschende Funktion steht in
// genau einer der beiden Listen; jede Frist-Funktion ist gegatet, und jeder
// Aufrufer einer Frist-Funktion mit Gate beim Aufrufer ist als gatePfad
// eingetragen. Einträge, deren Funktion es nicht mehr gibt oder die nicht mehr
// löscht, werden ebenfalls gemeldet — sonst veraltet die Liste still.
//
// GEGENPROBEN (Karte #589): uhr.Erlaubt in boardbackup.Rotate entfernt → rot
// („ruft uhr.Erlaubt nicht“); in retention.RunOnce entfernt → rot (Gate fehlt);
// eine neue Funktion mit DELETE FROM ohne Eintrag → rot („nicht eingeordnet“);
// ein zweiter Aufrufer von EvictExpired → rot („nicht als gatePfad“).
func TestLoeschpfade_AlleEingeordnet(t *testing.T) {
	alle := scanneServer(t)
	hinweis := " — einordnen in server/uhr/loeschpfade_test.go (nachAlter mit Gate oder nichtNachAlter mit Grund), siehe Wissen §38"

	for k, f := range alle {
		if !f.loescht {
			continue
		}
		_, a := nachAlter[k]
		_, n := nichtNachAlter[k]
		switch {
		case a && n:
			t.Errorf("%s steht in beiden Listen", k)
		case !a && !n:
			t.Errorf("%s löscht (%s), ist aber nicht eingeordnet%s", k, f.position, hinweis)
		}
	}

	for k, pfade := range nachAlter {
		f, ok := alle[k]
		if !ok {
			t.Errorf("nachAlter: %s existiert nicht mehr — Eintrag nachführen", k)
			continue
		}
		if !f.loescht && len(pfade) == 0 {
			t.Errorf("nachAlter: %s löscht nicht mehr erkennbar — Eintrag prüfen", k)
		}
		if len(pfade) == 0 {
			if !f.gate {
				t.Errorf("nachAlter: %s löscht nach Alter und ruft uhr.Erlaubt nicht (F-1192)", k)
			}
			continue
		}
		for _, p := range pfade {
			auf, ok1 := alle[p.aufrufer]
			gate, ok2 := alle[p.gate]
			switch {
			case !ok1 || !ok2:
				t.Errorf("nachAlter: %s — Aufrufer %s oder Gate %s existiert nicht mehr", k, p.aufrufer, p.gate)
			case !enthaelt(auf.aufrufe, methodenName(k)):
				t.Errorf("nachAlter: %s ruft %s nicht (mehr) auf — Eintrag prüfen", p.aufrufer, methodenName(k))
			case !gate.gate:
				t.Errorf("nachAlter: Gate %s für %s ruft uhr.Erlaubt nicht (F-1192)", p.gate, k)
			case p.gate != p.aufrufer && !enthaelt(auf.aufrufe, methodenName(p.gate)):
				t.Errorf("nachAlter: %s ruft das Gate %s nicht auf", p.aufrufer, p.gate)
			}
		}
	}

	// Jeder Aufrufer einer Frist-Funktion mit Gate beim Aufrufer muss als
	// gatePfad eingetragen sein. Methodennamen tragen keinen Empfängertyp —
	// EvictOlderThan gibt es zweimal —, deshalb zählt die Vereinigung.
	erlaubt := map[string]map[fundstelle]bool{}
	for k, pfade := range nachAlter {
		for _, p := range pfade {
			m := methodenName(k)
			if erlaubt[m] == nil {
				erlaubt[m] = map[fundstelle]bool{}
			}
			erlaubt[m][p.aufrufer] = true
		}
	}
	for k, f := range alle {
		for m, zulaessig := range erlaubt {
			if enthaelt(f.aufrufe, m) && !zulaessig[k] {
				t.Errorf("%s ruft die Frist-Funktion %s, ist aber nicht als gatePfad eingetragen%s", k, m, hinweis)
			}
		}
	}

	for k := range nichtNachAlter {
		if f, ok := alle[k]; !ok || !f.loescht {
			t.Errorf("nichtNachAlter: %s existiert nicht mehr oder löscht nicht mehr — Eintrag entfernen", k)
		}
	}
}
