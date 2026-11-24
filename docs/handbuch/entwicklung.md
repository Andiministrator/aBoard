# Entwicklung

[English](../handbook/development.md) · **Deutsch**

Dieses Kapitel richtet sich an alle, die am Code von aBoard mitarbeiten.

## Voraussetzungen

- [Bun](https://bun.sh) in einer aktuellen Version (Paketverwaltung, Skripte, Tests)
- ein Chromium-Browser zum Ausprobieren der Ordner-Verbindung

```bash
bun install
```

## Frontend entwickeln

```bash
bun run dev
```

startet den Vite-Entwicklungsserver; die Adresse steht in der Ausgabe. Er lädt `index.html` mit den Quellen aus `src/` und lädt bei Änderungen neu.

## Die beiden Fassungen bauen

```bash
bun run build && bun run build:template
```

| Befehl | Ergebnis | Inhalt |
|---|---|---|
| `bun run build` | `aboard.html` | mit Op-Inbox- und Projektor-Schicht (Projekt-Datei) |
| `bun run build:template` | `aboard.template.html` | ohne Projektor-Schicht, mit Größenlimit (Einzeldatei) |
| `bun run build:server` | `server/web/dist/` | Oberfläche für den Server, die das Server-Programm per `go:embed` einbettet |

Alle entstehen aus demselben `src/`. Wer etwas an `src/`, `assets/`, `index.html` oder `vite.config*.js` ändert, baut deshalb immer die beiden Einzeldateien; die Server-Oberfläche braucht nur, wer den Server baut (das Dockerfile erledigt das selbst).

Die angezeigte Versionsnummer kommt aus `ABOARD_BUILD_VERSION`, sonst aus `git describe`:

```bash
ABOARD_BUILD_VERSION=v1.2.3 bun run build && ABOARD_BUILD_VERSION=v1.2.3 bun run build:template
```

## Build-Flags

aBoard wird aus **einer** Quelle in mehrere Fassungen gebaut. Zwei Konstanten, die Vite zur Compile-Zeit einsetzt, steuern, welcher Code im Ergebnis landet; nicht benötigte Zweige entfernt der Bundler vollständig.

- **`__SERVER_BUILD__`**: Code für den Mehrbenutzer-Server (REST-Anbindung, Konten, Mail, KI). In den beiden Einzeldateien `false`, in der Server-Oberfläche `true`; der Code liegt in `src/`, weil Server und Einzeldatei dieselben Quellen teilen.
- **`__PROJECTOR_BUILD__`**: die Op-Inbox- und Projektor-Schicht (`src/ops.js`, `src/inbox.js`, `src/diff.js` und der Projektor-Teil von `src/sync.js`).

| Build-Konfiguration | Ergebnis | `__SERVER_BUILD__` | `__PROJECTOR_BUILD__` | Größenlimit |
|---|---|---|---|---|
| `vite.config.js` | `aboard.html` | `false` | `true` | keins |
| `vite.config.template.js` | `aboard.template.html` | `false` | `false` | < 360000 Byte |
| `vite.config.server.js` | `server/web/dist/` | `true` | `false` | keins |

Gemeinsame Teile stehen in `vite.config.base.js`.

⚠️ **Nicht jeder Pfad gehört hinter `__PROJECTOR_BUILD__`.** Der reine Dateizugriff (Ordner wählen, lesen, schreiben, regelmäßig nachladen) ist in jedem Build vorhanden. Nur was wirklich die Op-Inbox braucht, gehört hinter das Flag.

**Größenlimit:** `aboard.template.html` muss kleiner als 360000 Byte (dezimal) sein. Die Grenze ist eine Zielvorgabe gegen unbemerktes Wachstum, typischerweise Server- oder Projektor-Code, der versehentlich in die Einzeldatei rutscht; in diesem Repository prüft sie kein Skript, also selbst messen. Messen: `wc -c aboard.template.html`.

## Tests

```bash
bun run test
```

**Nie `bun test`:** Buns eigener Test-Runner kennt die Browser-Umgebung (happy-dom) nicht und meldet reihenweise Fehler wie `document is not defined`.

`bun run test` läuft dreimal, einmal je Fassung:

| vitest-Konfiguration | `__PROJECTOR_BUILD__` | spiegelt | Einzelaufruf |
|---|---|---|---|
| `vitest.config.js` | `true` | `aboard.html` | `bun run test:standalone` |
| `vitest.config.slim.js` | `false` | `aboard.template.html` | `bun run test:slim` |
| `vitest.config.server-frontend.js` | `false` | Server-Oberfläche | `bun run test:server-frontend` |

Die Zahl übersprungener Tests unterscheidet sich zwischen den Läufen; das ist gewollt, weil Code hinter den Flags in den anderen Builds fehlt. Tests, die nur für den Server-Build gelten, laufen nur im dritten Lauf.

Gezielt einzelne Dateien:

```bash
bunx vitest run --config vitest.config.js tests/<datei>.test.js
```

Neue Testdateien nutzen für flag-abhängige Teile die Helfer aus `tests/helpers/build-flags.js` (`describeProjektor` & Co.).

### Server (Go)

Der Server liegt unter `server/` und hat eigene Tests, getrennt von `bun run test`:

```bash
cd server && go test ./...
```

Der Server faltet Board-Änderungen mit demselben Code wie die App (`server/fold/`, eingebettetes Bündel aus `src/ops.js`). Wer `src/ops.js` ändert, erzeugt das Bündel mit `bun run build:ops-bundle` neu. Das Server-Programm samt Oberfläche baut `server/Dockerfile`; siehe [Container](server/container.md).

## Aufbau

| Ort | Inhalt |
|---|---|
| `index.html` | Einstieg, lädt `src/main.js` |
| `src/` | die App: Modell, Darstellung, Dialoge, Speichern, Ordner-Verbindung, Op-Inbox |
| `src/style.css` | alle Stile, Hell/Dunkel über `data-theme` |
| `src/i18n.js` | Texte Deutsch/Englisch |
| `tests/` | vitest-Tests mit happy-dom, Helfer unter `tests/helpers/` |
| `assets/` | Logo und Icon |
| `docs/handbuch/` | dieses Handbuch |

Kommentare im Code verweisen gelegentlich auf Entscheidungen (`ADR-…`), Befunde (`F-…`) oder Karten (`#…`) aus der internen Entwicklung. Die Dokumente dazu liegen nicht in diesem Repository; der Kommentar selbst trägt die Begründung.

## Grundsätze

- **Eine HTML-Datei**, keine Laufzeitabhängigkeiten, keine CDN-Links, kein Netzwerkzugriff im Produkt.
- **Daten bleiben lokal** (Datei, Browser-Speicher, freigegebener Ordner); Export als JSON.
- Neue Funktionen fragen sich zuerst: Gehören sie in beide Fassungen oder nur hinter ein Flag?
