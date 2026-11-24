# server/fold — Op-Fold am Server per goja

ADR-0129 §4, Variante B (entschieden nach dem Spike #546, übernommen mit Karte #551).
Der Server faltet Ops mit **derselben** Implementierung wie Browser und Projektor:
`src/ops.js`, gebündelt und in der eingebetteten JS-Engine goja ausgeführt.

## Dateien

| Datei | Zweck |
|---|---|
| `eintrag.js` | Bündel-Einstieg: `globalThis.AboardOps = ops` |
| `ops.bundle.js` | **eingecheckt**, erzeugt mit `bun run build:ops-bundle`, per `go:embed` eingebunden |
| `fold.go` | `Motor`: Pool von Runtimes, Zeitgrenze je Aufruf, Weiche „ohne Klon“ |
| `testdata/fixtures.json.gz` | 866 Konformitäts-Fixtures, mitgeschnitten aus `tests/ops.test.js` und `tests/orphan-ops.test.js` |
| `werkzeug/` | Aufzeichner und Fixture-Erzeuger |

## Wann was zu tun ist

**`src/ops.js` geändert:**

```sh
bun run build:ops-bundle             # Bündel neu erzeugen
(cd server && go test ./fold/)       # Konformität prüfen
```

`bun run test` wird rot, solange das Bündel nicht zur Quelle passt (`tests/ops-buendel.test.js`).
Scheitern danach die Go-Fixtures, hat sich das **Verhalten** geändert. Dann die Fixtures neu erzeugen.

**Fixtures neu erzeugen** (aus der Repo-Wurzel; Arbeitsdateien nach `tmp/`, nie nach `/tmp`):

```sh
GOJA_AUFNAHME_DIR=$PWD/tmp/aufnahmen bun run test:standalone \
  --config server/fold/werkzeug/aufzeichnung/vitest.aufzeichnung.config.js \
  tests/ops.test.js tests/orphan-ops.test.js
GOJA_AUFNAHME_DIR=$PWD/tmp/aufnahmen bun server/fold/werkzeug/erzeuge-fixtures.mjs
(cd server && go test ./fold/)
```

Der Erzeuger rechnet die Referenz aus dem **Bündel** unter bun. Außerdem prüft er, dass Bündel und
JSON-Umweg dasselbe liefern wie die Quelle im Testlauf; Abweichungen werden gezählt und
ausgegeben. Die Aufnahme aus `orphan-ops` enthält Zufallswerte aus `makeOp`. Die Datei ändert
sich deshalb bei jedem Lauf und wird nur neu eingecheckt, wenn es einen Grund dafür gibt.

**goja aktualisieren:** goja hat keine Tags. Die Version ist über `go.sum` gepinnt. Ein Update gibt
es nur zusammen mit einem grünen `go test -race ./fold/`.

## Entscheidungen im Code

- **Pool statt `sync.Pool`:** ein gepufferter Kanal, vorab gefüllt, Voreinstellung 2 Runtimes.
  `sync.Pool` verwirft Objekte beim GC und kennt keine Obergrenze. Mehr als 2 bringen nichts, weil
  `BEGIN IMMEDIATE` die Schreiber ohnehin serialisiert.
- **Ohne Klon als Voreinstellung:** Am Server ist der Snapshot frisch geparst und wird danach
  verworfen. Der Klon in `fold()` wäre also reine Sperrzeit. `TestKonformitaetOhneKlon` und
  `TestWeicheOhneKlonWirkt` sichern die Weiche ab.
- **Runtime ersetzen nach jedem Fehler und jedem ausgelösten Interrupt.** Eine Uhr, die knapp nach
  dem Ende des Aufrufs feuert, ließe sonst ein gesetztes Interrupt-Flag zurück, und der nächste
  Aufruf bräche ab (`TestSpaeteUhrVergiftetNichtDenNaechstenAufruf`).
- **Ziel-Prüfung nur am Server (F-1146):** `FalteMitZielen` faltet Op für Op und meldet vor jeder
  Op ein fehlendes Ziel als `ziel-fehlt`. Das steht in der Brücke, nicht in `src/ops.js`: Im lokalen
  Projektor sind Ops auf später gelöschte Karten normal und dürfen nicht melden. Op für Op zu falten
  ist nur dann dasselbe wie ein Fold über den Batch, wenn die Array-Reihenfolge die Faltordnung ist
  (gleicher `ts`, `seq` aufsteigend, so stempelt der Server); anderes weist die Brücke ab.
  `TestFalteMitZielenGleichFalte` vergleicht beide Wege.
- **Angelegte Karten (Karte #666, ADR-0133 §1):** `FalteMitZielen` liefert als dritten Teil
  (`Ergebnis.Angelegt`, JSON-Text, lesbar über `AngelegteKarten()`) je Op, die eine Karte NEU
  angelegt hat, die vergebene `displayId` und bei Findings die `nr`. Daraus nennt die Antwort von
  `POST /boards/{id}/ops` beide Werte. `Falte` bleibt bei zwei Teilen.
- **Keine Vorab-Prüfung der Ops:** `fold()` überspringt ungültige Ops still, wie im Projektor.
  Der Endpunkt validiert, bevor er faltet.

## Messwerte (amd64, Ryzen 7 7840U, 2026-09-28)

50 Ops, ohne Klon: synthetisches Board mit 500 Karten im Median 23 ms, echtes aBoard-Board
(494 Karten, 3,2 MB) 69 ms. **Auf dem Pi 5 ist noch nichts gemessen** (Karte #550, wartet auf
Stufe 2 beim Partnerprojekt).
