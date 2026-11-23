# Installation

[English](../handbook/installation.md) · **Deutsch**

Dieses Kapitel beschreibt, wie du aBoard beschaffst, einrichtest und aktualisierst. Welche Fassung zu dir passt, klärt [Varianten](varianten.md).

## Beschaffen

**Fertige Datei:** An jedem Release dieses Repositorys (Seite „Releases“) hängt die fertige `aboard.template.html`. Herunterladen, fertig.

**Selbst bauen:** Du brauchst [Bun](https://bun.sh) in einer aktuellen Version.

```bash
git clone <repo-url> aBoard
cd aBoard
bun install
bun run build:template      # aboard.template.html (Einzeldatei)
bun run build               # aboard.html (Projekt-Datei)
```

`<repo-url>` ist die Adresse dieses Repositorys. Beide Dateien entstehen im Wurzelverzeichnis.

## Einzeldatei

### Einrichten

1. `aboard.template.html` an einen festen Ort legen und nach Belieben umbenennen, etwa `meine-planung.html`.
2. Per Doppelklick im Browser öffnen. Fertig.

Beim ersten Öffnen legt aBoard ein leeres Board mit den Spalten Backlog, Ready, In Arbeit und Erledigt an.

**Speichern nicht vergessen.** Bis zum ersten Speichern liegt das Board nur im Browser-Speicher. Der Knopf **Speichern** im Kopf schreibt es in die HTML-Datei:

- In Chrome, Edge und anderen Chromium-Browsern fragt aBoard beim ersten Mal nach dem Speicherort und schreibt danach direkt in diese Datei.
- In Firefox und Safari lädt der Browser eine neue Datei herunter. Sie enthält App und Daten; lege sie an die Stelle der alten Datei.

Zusätzlich sichert **Menü → Datei → Exportieren** das Board als JSON-Datei. Das Menü öffnet sich mit einem Klick auf das Logo oben links.

### Aktualisieren

Die Daten stecken in deiner gespeicherten HTML-Datei, eine neue Version von aBoard bringt dagegen ein leeres Board mit. So nimmst du dein Board mit:

1. In der alten Datei **Speichern** und zur Sicherheit **Exportieren** (JSON).
2. Die alte Datei behalten, bis der Umzug geklappt hat.
3. Die neue `aboard.template.html` öffnen.
4. **Menü → Datei → Öffnen** und die exportierte JSON-Datei oder die alte HTML-Datei wählen.
5. **Speichern** — damit steckt das Board in der neuen Datei.

## Board in einem Ordner

1. Die Datei (`aboard.template.html` oder `aboard.html`) in den Ordner legen, in dem das Board liegen soll.
2. In Chrome oder Edge öffnen.
3. **Logo → Board-Einstellungen → „Ordner wählen…“** und denselben Ordner wählen. Der Browser fragt nach der Erlaubnis zum Lesen und Schreiben.

Danach liegt das Board als `aboard-sync.json` im Ordner. Bearbeite die Datei nicht von Hand, und halte **nur einen Browser-Tab** mit dem Board offen. Wer den Ordner mit git versioniert, nimmt `aboard-sync.json` mit auf.

**Aktualisieren:** Den Browser-Tab schließen, die neue Datei an die Stelle der alten legen und wieder öffnen. Die Daten liegen im Ordner und bleiben erhalten; nach einem Browser-Neustart einmal die Ordner-Freigabe bestätigen.
