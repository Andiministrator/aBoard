# Bedienung

[English](../handbook/usage.md) · **Deutsch**

Dieses Kapitel erklärt die Arbeit mit dem Board im Alltag: Spalten, Karten, Swimlanes, Filter, Kalender und Tastatur. Einige Funktionen gibt es nur mit dem Mehrbenutzer-Server, der noch nicht veröffentlicht ist; das steht jeweils dabei.

Welche Variante du vor dir hast, erklärt [Varianten](varianten.md).

## In fünf Minuten: das Wichtigste

- aBoard zeigt ein **Kanban-Board**: Spalten mit Karten, die Aufgaben darstellen. Die Spalten bilden den Status ab, von links nach rechts.
- Als **Einzeldatei** ist aBoard eine einzige HTML-Datei: Doppelklick, im Browser öffnen, fertig. **Speichern** schreibt das Board in diese Datei.
- Mit einer **Ordner-Verbindung** liegt das Board als `aboard-sync.json` in einem Ordner. Du fasst die Datei dann nie von Hand an und hältst **nur einen Browser-Tab** mit dem Board offen.
- Was du selbst steuerst: Karten in die richtige Spalte legen, Flags setzen, Fälligkeiten eintragen.

## Der Bildschirm

**Kopfzeile:** Logo, Board-Titel, Suchfeld, Filter, die Knöpfe **＋** (Spalte hinzufügen), **⊞** (Swimlane hinzufügen) und **Speichern**. Auf schmalen Bildschirmen wandern einige davon in das Menü **⋯**.

**Menü:** Ein Klick auf das **Logo** öffnet die Seitenleiste. Darin:

| Bereich | Inhalt |
|---|---|
| Datei | Öffnen, Exportieren (JSON) |
| Ansichten | Kalender |
| Einstellungen | **Benutzer-Einstellungen** (Identitäten, Theme hell/dunkel/System, Kartendichte, Darstellung — Ecken abgerundet oder eckig, Schrift System/Serif/Monospace, Akzentfarbe —, Sprache Deutsch/Englisch; gilt für dieses Gerät) und **Board-Einstellungen** (Modus und Projektordner, Board-Hintergrund, Vorlagen, Anhang-Grenzen) |
| Dokumentation | nur in `aboard.html` mit Ordner-Verbindung (Projekt-Modus): README und Wissens-Index des Projekts lesen |
| weitere | nur auf dem Server: Wissen (wenn eingerichtet), Server-Verwaltung (für Admins), Clients und Projekte |

Unten in der Seitenleiste steht die Version von aBoard.

**Darstellung:** Kästen sind von Haus aus **eckig**. Unter Benutzer-Einstellungen stellst du Ecken (abgerundet oder eckig), Schrift (System, Serif, Monospace) und Akzentfarbe (Petrol, Blau, Grün, Violett, Graphit) ein. Die Auswahl wirkt sofort als Vorschau; „Abbrechen“ nimmt sie zurück. Sie gilt für dieses Gerät und diesen Browser, nicht für alle am Board.

## Spalten

Ein frisches Board hat vier Spalten: **Backlog → Ready → In Arbeit → Erledigt**.

| Spalte | Bedeutung |
|---|---|
| Backlog | Ideen-Parkplatz: erfasst, aber noch nicht zur Bearbeitung freigegeben |
| Ready | Commitment-Point: das soll als Nächstes erledigt werden |
| In Arbeit | wird gerade bearbeitet |
| Erledigt | abgeschlossen |

Spalten lassen sich umbenennen (Klick auf den Titel), zuklappen, hinzufügen (**＋**) und löschen. Im Spaltenkopf gibt es außerdem Sammelaktionen: alle Karten der Spalte archivieren oder löschen. Gelöscht wird dabei in allen Swimlanes, deshalb fragt aBoard vorher nach.

Die Spaltennamen sind eine Vorgabe, keine feste Regel der Software.

## Swimlanes: Sprints und Lanes

Swimlanes sind waagerechte Zeilen durch das ganze Board. Jede Karte gehört zu genau einer Swimlane. Ganz oben liegt immer die Swimlane **Backlog** für alles, was keinem Sprint zugeordnet ist.

Mit **⊞** legst du eine neue Swimlane an und wählst die Art:

- **Sprint** — ein Arbeitsabschnitt mit Anfang und Ende. Jeder Sprint bekommt zwei Sonderkarten: **📋 Sprint-Start** für Anweisungen und Notizen zu Beginn, **🏁 Sprint-Abschluss** für den Rückblick am Ende. Sobald die erste Karte des Sprints den Backlog verlässt, wandert die Start-Karte beim Laden des Boards nach Erledigt; das zeigt an, dass der Sprint läuft. Der Sprint-Kopf zeigt den Fortschritt, etwa „3/8 Karten“, und sobald Aufwände geschätzt sind, erledigte gegen geschätzte Stunden.
- **Lane** — eine Themen- oder Personenzeile ohne Sprint-Mechanik: keine Sonderkarten, keine Fortschrittsanzeige.

Eine Swimlane lässt sich später zwischen Sprint und Lane umschalten, ein- und ausklappen und verschieben (am Griff ziehen oder mit den Pfeiltasten hoch/runter). Beim Löschen eines Sprints wandern seine Karten in den Backlog.

Die Sonderkarten 📋 und 🏁 lassen sich weder löschen noch verschieben noch archivieren; nur Beschreibung und Kommentare sind bearbeitbar.

**Swimlanes archivieren:** Über **📦** im Sprint-Kopf verschwindet eine ganze Swimlane aus dem Board. Ihre Karten bleiben unverändert erhalten. Mit dem Filter „Archiv anzeigen“ erscheint sie wieder, eingeklappt und mit dem Hinweis „Archiviert“.

## Karten

### Anlegen und verschieben

- **„+ Karte hinzufügen“** unten in einer Spalte, oder die Taste `n`. Gibt es Vorlagen, wählst du zuerst „Leer“ oder eine Vorlage.
- **Verschieben** per Drag & Drop, auch zwischen Swimlanes. Auf dem Smartphone funktioniert das ebenfalls.
- **Mehrere Karten zugleich:** Mit Strg-Klick (Mac: Cmd-Klick) Karten einzeln auswählen, mit Umschalt-Klick einen Bereich, mit `Strg+A` alle sichtbaren. Eine Leiste bietet dann Verschieben, Owner setzen, Label hinzufügen/entfernen und Löschen an. `Esc` hebt die Auswahl auf.

Jede Karte hat eine fortlaufende Nummer (`#12`), über die man sie eindeutig benennen kann.

### Das Karten-Fenster

Ein Klick auf eine Karte öffnet sie. Das Fenster hat mehrere Reiter:

| Reiter | Inhalt |
|---|---|
| Details | Titel, Beschreibung (Markdown), Labels |
| Kommentare | Kommentar-Verlauf; `Strg+Enter` schickt einen Kommentar ab |
| ⛓ Verknüpfungen | Verweise auf andere Karten |
| 📎 Anhänge | Dateien und Bilder |
| 📋 Verlauf | wer wann was geändert hat |
| 🤖 Agent-Kontext | nur im Projekt-Modus und auf dem Server, wenn Claude Owner oder beteiligt ist: ein Kontextpaket für KI-Agenten |

Darunter stehen die Felder der Karte (siehe nächster Abschnitt). Beim Schließen des Fensters speichert aBoard die Änderungen automatisch.

**Beschreibung:** Sie öffnet sich im Lesemodus. Der Knopf **✏** (Hinweis „Bearbeiten“) schaltet in den Bearbeitungsmodus: Ein Klick auf einen Absatz macht ihn editierbar, eine Werkzeugleiste hilft beim Formatieren (`Strg+B` fett, `Strg+I` kursiv, Überschriften, Listen, Checklisten, Links, Zitate). **„✓ Fertig“** schaltet zurück. Häkchen in Checklisten lassen sich auch im Lesemodus setzen. Bilder von fremden Servern lädt aBoard erst nach einem Klick, damit nichts unbemerkt ins Netz geht.

**Export einer Karte:** Das Menü „📄 Export“ im Karten-Fenster druckt die Karte bzw. speichert sie als PDF, speichert sie als Markdown oder öffnet eine E-Mail mit ihrem Inhalt.

### Karten-Felder

| Feld | Bedeutung |
|---|---|
| **Priorität** | Niedrig, Mittel, Hoch |
| **Labels** | freie Schlagworte, durch Komma getrennt, etwa `bug, feature` |
| **Owner** | wer die Karte verantwortet: eine Person oder, im Projekt-Modus und auf dem Server, ein Claude-Modell |
| **Beteiligte** | weitere Personen oder Modelle, die mitwirken |
| **Komplexität** (`einfach`/`mittel`/`hoch`) | wie aufwendig die Aufgabe grob eingeschätzt wird — nur im Projekt-Modus und auf dem Server |
| **Aufwand (h)** | geschätzte Stunden, etwa `0.5` — nur im Projekt-Modus und auf dem Server |
| **⚡ Kritisch** | die Karte ist dringend und blockiert den Fortschritt |
| **⊘ Blockiert** | die Karte wartet auf etwas anderes (eine Antwort, ein externes System) |
| **✓ QA** („QA abgeschlossen“) | die Karte wurde geprüft und ist abnahmebereit |
| **Fällig am** | ein Termin; optional mit Uhrzeit |
| **Start am** | ein Starttermin; optional mit Uhrzeit, nur zur Information |
| **Wiederkehrend** | täglich, wöchentlich (mit Wochentag) oder monatlich (mit Tag im Monat) |

Komplexität und Aufwand helfen, eine Aufgabe realistisch einzuschätzen, bevor sie losgeht; die Kachel zeigt beide als kleine Plaketten. Die Flags Kritisch, Blockiert und QA sind einfache Ja/Nein-Schalter, sichtbar als Symbole auf der Karte.

**Fälligkeit:** Eine überfällige Karte bekommt eine rote Plakette, eine in den nächsten drei Tagen fällige eine gelbe. In der Spalte Erledigt entfällt die Plakette.

**Owner und Identitäten:** Welche Namen im Owner-Feld zur Wahl stehen, legst du unter Benutzer-Einstellungen → Identitäten fest (Anzeigename, Farbe). Auf einem Server-Board bietet das Owner-Feld stattdessen die Personen an, die eine Rolle am Board haben; Claude-Modelle nur auf Boards, die sie schon nutzen.

### Wiederkehrende Karten

Eine Karte mit Wiederholung ist eine Vorlage, die im Board stehen bleibt. Fällt der nächste Termin an, legt aBoard beim Laden des Boards eine neue Karte als Kopie an. Die Kopien tragen selbst keine Wiederholung.

### Vorlagen

Im Karten-Fenster speichert **„Als Template speichern“** eine Karte als Vorlage. Beim Anlegen einer neuen Karte erscheint die Vorlage dann zur Auswahl. Verwaltet werden Vorlagen in den Board-Einstellungen.

### Verweise zwischen Karten

Im Reiter **⛓ Verknüpfungen** verbindest du Karten miteinander:

| Typ | Gegenstück auf der anderen Karte | Bedeutung |
|---|---|---|
| blockiert | blockiert von | diese Karte hält die andere auf |
| blockiert von | blockiert | diese Karte wartet auf die andere |
| hängt ab von | Voraussetzung für | weiche Abhängigkeit, parallel bearbeitbar |
| Voraussetzung für | hängt ab von | die andere Karte braucht diese |
| verwandt mit | verwandt mit | nur zur Information |

aBoard trägt das Gegenstück auf der anderen Karte automatisch ein. Eine Karte mit offenem „blockiert von“ zeigt die Plakette ⛔. Verschiebst du sie trotzdem, fragt aBoard nach. Wird die blockierende Karte erledigt, meldet aBoard, dass die andere jetzt frei ist.

### Anhänge

Im Reiter **📎 Anhänge** fügst du Dateien hinzu: über „Datei wählen…“ oder per Drag & Drop auf das Fenster. Ein Bild kann als **Cover** der Karte dienen und erscheint dann auf der Kachel.

Wo die Dateien landen, hängt von der Variante ab:

- **Einzeldatei:** in der HTML-Datei selbst. Deshalb gibt es Grenzen für die einzelne Datei und für die Gesamtgröße des Boards (einstellbar in den Board-Einstellungen). Große Anhänge machen die Datei träge.
- **Projekt-Modus:** im Unterordner `attachments/` des Projektordners.
- **Server:** auf dem Server. Dort lassen sich Dateien auch an Kommentare hängen. Löschen geht nur, solange der Server erreichbar ist und du angemeldet bist: Klappt es nicht, bleibt der Anhang in der Karte und ein Hinweis sagt, warum (ohne Berechtigung oder später erneut versuchen). Löschst du eine ganze Karte, während der Server nicht antwortet oder deine Anmeldung abgelaufen ist, versucht aBoard das Entfernen ihrer Dateien noch einmal, sobald dein Löschen am Server angekommen ist — solange der Tab offen bleibt. Schließt du ihn vorher, bleiben die Dateien am Server liegen.

### Archivieren

Erledigte Karten, die du nicht mehr sehen willst, aber behalten möchtest, archivierst du (**📦 Archivieren** im Karten-Fenster, oder für eine ganze Spalte im Spaltenkopf). Archivierte Karten verschwinden aus der Spalte, bleiben aber erhalten und im Export enthalten. Der Spaltenkopf zeigt mit 📦 und einer Zahl, wie viele dort liegen. Die Sonderkarten 📋 und 🏁 lassen sich nicht archivieren.

## Suchen und filtern

**Suche:** Taste `/` oder Klick ins Suchfeld. Gesucht wird ohne Rücksicht auf Groß- und Kleinschreibung in Titel, Beschreibung, Kommentaren, Labels und Owner. Karten ohne Treffer werden ausgeblendet, Treffer hervorgehoben. Ein Kreuz im Feld setzt die Suche zurück.

**Filter:** In der Kopfzeile filterst du nach Owner (trifft auch Beteiligte), nach Label sowie nach den Flags ⚡ Kritisch und ⊘ Blockiert. Anders als die Suche blendet der Filter nicht aus, sondern **dimmt** die nicht passenden Karten; so bleibt der Zusammenhang sichtbar. Der Schalter „Archiv anzeigen“ blendet archivierte Karten und Swimlanes ein.

Suche und Filter werden nicht gespeichert; nach dem Neuladen ist alles wieder sichtbar.

## Kalender

**Menü → Ansichten → Kalender** zeigt alle Karten mit Fälligkeits- oder Startdatum. Es gibt drei Ansichten: **Monat**, **Woche** und **Agenda** (die nächsten 30 Tage). Karten mit Uhrzeit erscheinen zur jeweiligen Zeit.

- Eine Karte auf einen anderen Tag **ziehen** verschiebt ihre Termine.
- Ein Klick in einen leeren Tag legt eine **neue Karte** mit diesem Datum an.
- Filter: erledigte Karten zeigen, nach Owner einschränken.

Nur auf dem Server zusätzlich:

- **Alle Boards** — der Kalender zeigt Karten aus allen Boards, die du lesen darfst,
- **Termine ohne Karte** — „＋ Termin“ legt einen reinen Termin an (Titel, Datum, ggf. mehrtägig, von/bis, Ort, Beschreibung),
- **Abonnieren** — eine geheime Kalender-Adresse für Kalender-Apps,
- Filter nach Projekt.

## Board-Übersicht und mehrere Boards

Ein Klick auf den Board-Titel im Kopf öffnet ein Menü mit dem aktuellen und den zuletzt benutzten Boards; „Alle Boards anzeigen…“ zeigt die volle Liste. Der Stift ✎ neben dem Titel benennt das Board um. Auf dem Server gibt es dort außerdem „Neues Board“.

**Reiter:** Wer mehrere Boards nutzt, sieht unter dem Kopf eine Reiterleiste mit den zuletzt benutzten Boards, höchstens sechs. Ein Klick auf einen Reiter wechselt das Board; das aktuelle ist hervorgehoben. Neue Boards kommen hinten dazu, und bei mehr als sechs fällt das am längsten nicht benutzte heraus. Das × an einem Reiter nimmt ihn aus der Leiste, das Board selbst bleibt. Die Leiste merkt sich jeder Browser für sich. Mit nur einem Board erscheint sie nicht. Es gibt sie auf dem Server und im Claude-Code-Modus, nicht im einzelnen Datei-Board (Vorlage `aboard.template.html`). Auf dem Server zeigt ein Reiter ein **💬**, wenn Claude auf diesem Board auf deine Antwort wartet; die Frage selbst steht im Banner, sobald du das Board öffnest. Der Reiter wird höchstens einmal je Minute aufgefrischt.

Auf einem **Server-Board** öffnet „Alle Boards anzeigen…“ die **Board-Übersicht**: alle Boards, die du sehen darfst, als Kacheln mit Hintergrundbild, getrennt nach „Eigene“ und „Mit mir geteilt“. Jede Kachel nennt Rolle, Änderungsdatum und die Board-ID, die sich per Knopf kopieren lässt. Freigeben lässt sich ein Board in den Board-Einstellungen im Abschnitt „Freigabe“.

## Tastatur

| Taste | Wirkung |
|---|---|
| `/` | Suchfeld fokussieren |
| `n` | neue Karte in der ersten sichtbaren Spalte |
| `j` / `k` | nächste / vorherige Karte markieren |
| `Enter` | markierte Karte öffnen |
| `Esc` | Mehrfachauswahl aufheben, sonst Markierung aufheben bzw. Hilfe schließen |
| `?` | Übersicht der Tastenkürzel öffnen/schließen |
| `Strg+A` / `Cmd+A` | alle sichtbaren Karten auswählen |

Die Kürzel wirken nicht, solange ein Eingabefeld den Fokus hat oder ein Karten-Fenster offen ist. Im Karten-Fenster gelten eigene Kürzel: `Esc` schließt, `Strg+B`/`Strg+I` formatieren, `Strg+Enter` schickt einen Kommentar ab.

## Speichern, Öffnen, Exportieren

| | Einzeldatei | Projekt-Modus | Server |
|---|---|---|---|
| **automatisch** | Zwischenstand im Browser-Speicher | jede Änderung landet im Projektordner | jede Änderung landet auf dem Server |
| **Speichern** | schreibt Board und App in die HTML-Datei | schreibt in den Projektordner | — |
| **Öffnen** | lädt ein Board aus JSON- oder aBoard-HTML-Datei | wie Einzeldatei | — |
| **Exportieren** | JSON-Datei | JSON-Datei | JSON-Datei |

In der Einzeldatei gibt es außerdem den Knopf **KI-Kontext** unten links im Einstellungsdialog: Er erzeugt eine Markdown-Zusammenfassung des Boards (`ki-kontext.md`) zum Weitergeben an ein Sprachmodell.

Findet die Einzeldatei beim Öffnen ungespeicherte Änderungen aus der letzten Sitzung, bietet sie an, diese zu laden oder zu verwerfen.

Manchmal liegt im Browser noch eine ältere Zwischenkopie, also ein Zwischenstand, den sich der Browser gemerkt hat. Was dann passiert, hängt davon ab, wie du arbeitest:

- **Einzeldatei:** Beim Öffnen erscheint der Hinweis „Nicht gespeicherte Änderungen der letzten Session gefunden.“ mit den Knöpfen „Laden“ und „Verwerfen“.
- **Mit verbundenem Projektordner:** Maßgeblich ist der Stand im Projektordner. Ist dort ein Board, verschwindet die alte Browser-Kopie still, ohne Meldung. Nur wenn die Projektdatei gerade nicht lesbar ist oder fehlt, erscheint der Hinweis „Der Projektordner konnte gerade nicht gelesen werden. Im Browser liegt noch eine ältere Zwischenkopie — sie bleibt erhalten; maßgeblich ist weiter der Projektordner.“ mit dem Knopf „Alles klar“.
- **Projektordner bekannt, aber noch nicht freigegeben:** Dann erscheint nur der Hinweis zur Freigabe des Projektordners mit „Erlauben“. Bis zur Freigabe bleibt das Board leer; die Browser-Kopie bleibt dabei unangetastet. Das Banner verschwindet erst, wenn der Ordner verbunden ist. Antwortet der Browser auf die Freigabe nicht, steht dort „Der Browser antwortet nicht auf die Freigabe des Projektordners. Wähle den Ordner bitte neu.“ mit dem Knopf „Ordner neu wählen“.

Auf dem Server warnt aBoard, wenn jemand anderes das Board gleichzeitig geändert hat, und lässt dich wählen: eigene Version behalten oder die des Servers laden.

## Board in einem Ordner (Projekt-Modus)

Mit **Logo → Board-Einstellungen → „Ordner wählen…“** legt aBoard das Board als `aboard-sync.json` in einen Ordner und prüft regelmäßig, ob sich dort etwas geändert hat. Das braucht die **File System Access API** (Chrome, Edge und andere Chromium-Browser); nach einem Browser-Neustart fragt der Browser einmal nach der Erlaubnis. Bearbeite `aboard-sync.json` nie von Hand, sondern immer über die Board-Oberfläche.

### Der Ein-Tab-Grundsatz

**Halte immer nur einen Browser-Tab mit dem Board gleichzeitig offen.**

Solange ein Tab offen ist, hält der Browser einen eigenen Zwischenspeicher (`localStorage`) mit deinem letzten Bearbeitungsstand. Öffnest du einen zweiten Tab, etwa weil der erste eingefroren wirkt, bringt der einen **eigenen, möglicherweise veralteten** Zwischenspeicher mit. Wird daraus ein Stand wiederhergestellt oder verglichen, kann das aussehen wie eine riesige Menge gelöschter Karten, obwohl nichts davon beabsichtigt war.

`aboard.html` hat dagegen eine eingebaute Bremse (die Einzeldatei nicht): Ein Löschvorgang, der ungewöhnlich viele Karten (mehr als 20) oder Swimlanes (mehr als 2) auf einmal betrifft, löst eine Sicherheitsabfrage mit der genauen Anzahl aus, bevor etwas geschrieben wird. Die Voreinstellung dieser Abfrage ist „Abbrechen“. Die Bremse ist ein zusätzliches Netz, aber kein Ersatz für die Regel: **ein Tab, ein aktueller Stand.**

Praktisch heißt das: Vor dem Öffnen eines neuen Tabs den alten wirklich schließen, nicht nur ein neues Fenster daneben aufmachen.

## Weiterlesen

- [Varianten](varianten.md) — welche Betriebsart was kann
- [Installation](installation.md) — einrichten und aktualisieren
