# Welche Variante passt?

[English](../handbook/variants.md) · **Deutsch**

Aus diesem Repository entstehen zwei Fassungen der Board-App. Beide zeigen dasselbe Board mit denselben Spalten, Karten und Swimlanes (waagerechten Zeilen quer durch alle Spalten, etwa je Sprint). Sie unterscheiden sich darin, **wo die Daten liegen**.

Wie man sie einrichtet, steht in [Installation](installation.md).

## Die beiden Fassungen auf einen Blick

| | Einzeldatei | Projekt-Datei |
|---|---|---|
| **Datei** | `aboard.template.html` | `aboard.html` |
| **Bauen mit** | `bun run build:template` | `bun run build` |
| **Wo liegen die Daten** | in der HTML-Datei selbst (beim Speichern eingebettet), dazwischen im Browser-Speicher; Export als JSON; wahlweise in einem freigegebenen Ordner | in einem freigegebenen Ordner (`aboard-sync.json`) |
| **Wer schreibt mit** | nur du im Browser | du im Browser; mit den Projekt-Werkzeugen auch Programme wie Claude Code (Werkzeuge noch nicht veröffentlicht) |
| **Voraussetzungen** | ein aktueller Browser; für die Ordner-Verbindung ein Chromium-Browser (Chrome, Edge …) | ein Chromium-Browser |
| **Internet nötig** | nein | nein |

Faustregel: **Für fast alle die Einzeldatei.** Die Projekt-Datei enthält zusätzlich die Op-Inbox-Schicht, über die später auch Werkzeuge außerhalb des Browsers sicher mitschreiben; ohne diese Werkzeuge bringt sie gegenüber der Einzeldatei mit Ordner-Verbindung keinen Vorteil.

## Einzeldatei (`aboard.template.html`)

Die einfachste Form: eine HTML-Datei, die man per Doppelklick im Browser öffnet. Kein Server, keine Installation, kein Netzwerkzugriff. Alle Bibliotheken sind in die Datei eingebettet.

**So werden die Daten gehalten:**

- Während der Arbeit puffert aBoard jeden Stand im Speicher des Browsers (`localStorage`). Findet aBoard beim nächsten Öffnen dort ungespeicherte Änderungen, bietet es an, sie zu laden oder zu verwerfen.
- **Speichern** schreibt das Board in die HTML-Datei selbst: Die Datei enthält danach App und Daten zusammen und lässt sich weitergeben oder sichern. In Chromium-Browsern geschieht das über die File System Access API direkt in die Datei; andere Browser laden stattdessen eine neue Datei herunter, die man an die Stelle der alten legt.
- **Exportieren** erzeugt eine JSON-Datei. **Öffnen** lädt ein Board aus einer JSON-Datei oder aus einer anderen aBoard-HTML-Datei.

Der Browser-Speicher ist nur ein Puffer. Wer den Browser-Verlauf löscht, verliert alles, was nicht gespeichert oder exportiert wurde.

Felder, die nur für die Zusammenarbeit mit KI-Agenten gedacht sind (Komplexität, Aufwand, Agent-Kontext), blendet sie aus, solange sie nicht mit einem Ordner verbunden ist.

## Ordner-Verbindung

Beide Fassungen können das Board in einen Ordner legen: **Logo → Board-Einstellungen → „Ordner wählen…“**. Danach liegt der Stand als `aboard-sync.json` in diesem Ordner, und aBoard prüft regelmäßig, ob sich dort etwas geändert hat. Das eignet sich, wenn das Board neben einem Projekt liegen und mit git versioniert werden soll.

Im Handbuch heißt das Arbeiten mit Ordner-Verbindung auch **Projekt-Modus**. Wo dort von einem Server die Rede ist, geht es um den Mehrbenutzer-Server, der noch nicht veröffentlicht ist.

Die Ordner-Verbindung braucht die **File System Access API** (Chrome, Edge und andere Chromium-Browser). Nach einem Browser-Neustart fragt der Browser einmal nach der Erlaubnis, auf den Ordner zuzugreifen.

## Projekt-Datei (`aboard.html`)

Die Projekt-Datei enthält zusätzlich die **Op-Inbox- und Projektor-Schicht**: Ist ein Ordner auf die Op-Inbox umgestellt (`aboard-sync.base.json` und `aboard-sync.inbox/` neben `aboard-sync.json`), landet jede Änderung als eigene kleine Datei in der Inbox, und mehrere Schreiber kommen sich nicht in die Quere. Umgestellt wird mit einem Kommandozeilen-Werkzeug, das zusammen mit der Anbindung an Claude Code später veröffentlicht wird. Bis dahin verhält sich die Projekt-Datei wie die Einzeldatei mit Ordner-Verbindung.
