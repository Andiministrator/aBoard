/* Gemeinsamer fetch-Fallback-Stub fuer alle vier Test-Kontexte (Karte #397,
   Folgekarte zu F-599, docs/findings.md).

   PROBLEM
   Im Server-Frontend-Kontext (__SERVER_BUILD__=true) holen mehrere Module
   beim Rendern server-seitige Nebendaten per `fetch` mit RELATIVER URL.
   `Config.serverUrl` ist im echten Betrieb absichtlich leer (gleicher
   Ursprung hinter einem Reverse-Proxy) -- im Test loest happy-dom relative
   URLs aber gegen seinen Default-Ursprung `http://localhost:3000` auf, und
   der Testlauf versucht echte TCP-Verbindungen. Das verstoesst gegen das
   Offline-first-Prinzip des Projekts, kostet Verbindungs-Timeouts und ist im
   schlimmsten Fall gefaehrlich: der self-hosted CI-Runner ist eine reale
   Entwicklermaschine, auf der Port 3000 ein ueblicher Dev-Server-Port ist --
   ein Testlauf, der dort echte Requests absetzt, ist weder reproduzierbar
   noch harmlos.

   WARUM ALS setupFiles-MODUL UND NICHT PRO DATEI
   Bis Sprint 54 hatte jede betroffene Testdatei ihren eigenen lokalen Stub
   kopiert (transfer-picker-race.test.js, board-background-race.test.js).
   Sieben aeltere Dateien (sidebar/awaiting-user/cover/modal.dom/
   swimlane-kind/user-avatar/board-background) hatten GAR keinen und oeffnen
   bis heute echte Verbindungen. Ein gemeinsames setupFiles-Modul deckt beide
   Faelle ab UND verhindert, dass der naechste neue Test das Problem erneut
   mitbringt -- genau der in F-599 benannte Nachtrag.

   WARUM SICHER TROTZ VITEST-ISOLATION
   Vitest isoliert standardmaessig jede Testdatei mit einem frischen
   globalThis (`isolate: true`, Default) -- setupFiles-Module werden also PRO
   Testdatei neu ausgefuehrt, nicht einmal fuer den gesamten Lauf. Das macht
   diesen Stub robust gegenueber Dateien, die globalThis.fetch am Ende ihrer
   Suite manipulieren oder loeschen (z. B. admin-dialog.test.js: `delete
   globalThis.fetch` im afterEach) -- das wirkt sich nur auf die eigene Datei
   aus, nie auf die naechste.

   UEBERSCHREIBBARKEIT DURCH LOKALE STUBS
   Dieses Modul registriert nur einen ROOT-LEVEL `beforeEach`/`afterEach` als
   Fallback. Vitest fuehrt root-level Hooks aus setupFiles vor den Hooks aus,
   die eine Testdatei selbst beim Einlesen registriert (setupFiles laden vor
   den Testdateien). Eine Datei mit eigenem `globalThis.fetch = vi.fn(...)`-
   Stub in ihrem eigenen beforeEach ueberschreibt diesen Fallback also wie
   bisher unveraendert -- kein bestehender Test musste dafuer angefasst
   werden (siehe llm-stream/llm-tab/notifications-client/storage-plugin/
   admin-dialog.test.js).

   ANTWORTFORM: `{}`, NICHT `[]` (F-602-Lehre)
   Die vorherige Nacharbeit lieferte testweise `new Response('[]')` aus und
   riss damit `bun run test` ab: `[].entries` ist die eingebaute
   Array-Methode und damit truthy, wodurch das Produktionsmuster
   `(body && body.entries || []).forEach(...)` NICHT auf den Fallback greift
   und `.forEach` auf eine Funktion aufgerufen wird (TypeError). `{}.entries`
   ist dagegen `undefined` -- der `|| []`-Zweig greift wie vorgesehen. Diese
   Wahl ist bewusst konsistent mit den bereits bestehenden lokalen Stubs
   (transfer-picker-race.test.js, board-background-race.test.js).

   LAUT ODER STILL BEI UNBEKANNTER URL?
   Eine "laute" Variante (Test schlaegt bei unbekannter URL fehl, URL wird
   genannt) waere fuer einen URL-SPEZIFISCHEN Stub die bessere Wahl -- hier
   passt sie nicht: Dieses Modul ist ein reiner FALLBACK fuer Dateien OHNE
   eigenen Fetch-Test. Die Fetches, die es abfaengt, sind fuer diese Tests
   Rendering-NEBENEFFEKTE (Projekte/Identitaeten/LLM-Anbieter fuer den
   Sidebar-Aufbau), keine Testgegenstaende -- ein hartes Fail wuerde sieben
   Bestandsdateien zwingen, einen Stub zu kennen und zu pflegen, nur um eine
   Nebenwirkung zu befriedigen, die sie gar nicht pruefen wollen. Das waere
   genau die "reihenweise Umschreiberei", die vermieden werden soll.
   Der Mittelweg statt totaler Stille: JEDE erstmals gesehene URL pro Datei
   wird einmalig genannt (dedupliziert, damit sidebar.test.js nicht 85 Zeilen
   Rauschen erzeugt), aber ohne Tests zu brechen. Dateien, die fetch
   tatsaechlich testen wollen, ueberschreiben diesen Fallback ohnehin komplett
   mit ihrem eigenen, spezifischeren Stub.

   WARUM stderr UND NICHT console.debug (F-617)
   Die erste Fassung nutzte console.debug -- und war damit vollstaendig
   wirkungslos: Vitest gibt Konsolenausgaben BESTANDENER Tests im
   Default-Reporter nicht aus. Gemessen im vollstaendigen `bun run test`-Log:
   0 Vorkommen von "[fetch-stub]", obwohl der Fallback 304-mal griff. Die
   dokumentierte Sichtbarkeit bestand also nicht, und `seenUrls` war toter
   Code -- eine Zusage, die niemand einloesen konnte.

   `process.stderr.write` geht am Reporter vorbei und kommt an. Nur aktiv,
   wenn ABOARD_FETCH_STUB_LAUT gesetzt ist: standardmaessig still, weil 304
   Zeilen bei jedem Lauf niemand liest und Rauschen die naechste echte Meldung
   verdeckt. Wer wissen will, was der Stub abfaengt, setzt die Variable --
   und bekommt dann eine Ausgabe, die auch wirklich erscheint.

   OPT-IN: GEZIELTE ANTWORTEN + STRENGER MODUS (F-622c)
   ⚠️ OHNE OPT-IN BEANTWORTET DIESER STUB JEDE URL MIT `200 {}` -- Fehlerpfade
   (4xx/5xx, kaputtes JSON, Netzwerkfehler) sind damit fuer jede Datei
   UNERREICHBAR, die keinen eigenen `globalThis.fetch`-Mock setzt. Ein Test,
   der einen Fehlerpfad pruefen will, haengt entweder seinen eigenen
   `globalThis.fetch` ein (wie bisher, siehe llm-tab.test.js) ODER nutzt die
   beiden Methoden auf dem von `macheFallbackFetch()` gelieferten Objekt:

     const stub = macheFallbackFetch()
     stub.antworte('/llm/pricing', { status: 200, body: { ... } })  // Muster: String (Teiltreffer) oder RegExp
     stub.streng()                                                  // ab hier: unregistrierte URL -> Promise.reject statt 200 {}
     globalThis.fetch = stub

   `antworte(muster, antwort)` registriert eine kanonische Antwort fuer jede
   URL, die `muster` enthaelt (String) oder gegen die `muster` matcht (RegExp);
   `antwort` ist `{status, body}` oder eine Funktion `(url, init) => {status,
   body}`. `streng()` schaltet um: JEDE URL ohne registrierte Antwort liefert
   danach eine ABGELEHNTE Promise (wie ein echter Netzwerkfehler) statt der
   stillen `200 {}`-Antwort -- der Fehlerpfad des Produktionscodes (typischer-
   weise ein catch-Zweig um `await fetch(...)`) greift dann wie im echten
   Betrieb. Ohne `streng()` aendert `antworte()` nur DIE registrierten URLs;
   alle anderen bleiben beim stillen `200 {}`-Fallback -- die Opt-in-Methoden
   sind additiv, kein Ersatz fuer das Standardverhalten. */

const ORIGINAL_FETCH = globalThis.fetch
/* Standardmaessig still (siehe F-617 im Kopfkommentar). `bun run test` mit
   ABOARD_FETCH_STUB_LAUT=1 zeigt, welche URLs der Fallback abfaengt. */
const LAUT = !!(typeof process !== 'undefined' && process.env?.ABOARD_FETCH_STUB_LAUT)

/* Als Fabrik exportiert, damit die F-617-Zusage pruefbar ist und nicht ein
   zweites Mal lautlos verfaellt (F-630, Sprint-55-Verify-Lauf). Genau das war
   F-617: die erste Fassung nutzte console.debug, war damit vollstaendig
   wirkungslos -- und niemandem fiel es auf, weil nichts es pruefte. Die
   Abhaengigkeiten (laut? wohin schreiben?) kommen deshalb von aussen herein
   statt aus dem Modulzustand; sonst laesst sich das Verhalten nur ueber eine
   Umgebungsvariable und einen erneuten Modul-Import beobachten, und ein Test,
   der dafuer `beforeEach` ausserhalb einer Suite ausloest, prueft mehr Vitest
   als Stub. `seenUrls` gehoert bewusst in die Fabrik und nicht ins Modul:
   pro Instanz eine eigene Menge, sonst traegt der Test die Dedup-Historie des
   Produktivlaufs mit sich herum. */
export function macheFallbackFetch({ laut = LAUT, schreibe = (t) => process.stderr.write(t) } = {}) {
  const seenUrls = new Set()
  /* F-622(c): registrierte Antworten (Reihenfolge = Registrierungs-Reihenfolge,
     erster Treffer gewinnt) + der strenge Schalter. Beides lebt PRO INSTANZ,
     aus demselben Grund wie `seenUrls` oben: ein Test darf nicht die
     Registrierungen eines anderen Tests erben. */
  const registrierungen = []
  let streng = false

  function passtMuster(url, muster) {
    if (typeof muster === 'string') return url.includes(muster)
    if (muster instanceof RegExp) return muster.test(url)
    return false
  }

  function fallbackFetch(input, init) {
    const url = typeof input === 'string' ? input : (input?.url ?? String(input))

    for (const { muster, antwort } of registrierungen) {
      if (!passtMuster(url, muster)) continue
      const a = typeof antwort === 'function' ? antwort(url, init) : antwort
      const status = a?.status ?? 200
      const body = a?.body !== undefined ? a.body : {}
      const bodyText = typeof body === 'string' ? body : JSON.stringify(body)
      return Promise.resolve(new Response(bodyText, {
        status,
        headers: { 'Content-Type': 'application/json' },
      }))
    }

    if (streng) {
      /* Abgelehnte Promise, nicht `throw` -- so verhaelt sich ein echter
         Netzwerkfehler (`fetch` wirft nie synchron), und der bestehende
         catch-Zweig um `await fetch(...)` im Produktionscode greift genau
         wie im echten Betrieb, statt den Test mit einem synchronen Fehler
         ausserhalb jedes try/catch abzuwuergen. */
      return Promise.reject(new Error(
        `[fetch-stub] STRENG: keine Antwort registriert fuer ${url} -- fetchStub.antworte(...) fehlt (s. tests/helpers/fetch-stub.js)`))
    }

    if (laut && !seenUrls.has(url)) {
      seenUrls.add(url)
      schreibe(
        `[fetch-stub] Fallback greift fuer: ${url} (leere JSON-Antwort, s. tests/helpers/fetch-stub.js)\n`)
    }
    return Promise.resolve(new Response('{}', {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
  }

  /* Verkettbar (`stub.antworte(...).streng()`), damit Tests die Registrierung
     kompakt in einer Zeile aufbauen koennen. */
  fallbackFetch.antworte = (muster, antwort) => { registrierungen.push({ muster, antwort }); return fallbackFetch }
  fallbackFetch.streng   = () => { streng = true; return fallbackFetch }

  return fallbackFetch
}

const fallbackFetch = macheFallbackFetch()

beforeEach(() => {
  globalThis.fetch = fallbackFetch
})

afterEach(() => {
  globalThis.fetch = ORIGINAL_FETCH
})
