# REST-API

[English](../../handbook/server/api.md) · **Deutsch**

Diese Seite listet alle Pfade, die der aBoard-Server beantwortet. Sie richtet sich an Betreiber:
Wer einen Proxy davor konfiguriert, Monitoring einrichtet oder Rechte prüfen will, findet hier,
welcher Pfad was tut und wer ihn aufrufen darf.

Anmeldung und Rollen beschreibt [Anmeldung und Rollen](anmeldung.md), alle Umgebungsvariablen
stehen in [Konfiguration](konfiguration.md).

## Grundlagen

- **Kein `/api`-Präfix, kein `/login`.** Die Pfade beginnen direkt mit `/boards`, `/me`, `/admin`
  usw. `curl https://board.example.org/api` antwortet deshalb mit `404`; richtig ist
  `curl https://board.example.org/boards`.
- **Ein Prozess, ein Port.** Derselbe Server liefert die Weboberfläche (unter `/` und allen Pfaden,
  die keine API-Route sind) und die API aus.
- **Menschen melden sich auf einem von zwei Wegen an:** über einen vorgeschalteten Proxy per
  Forward-Auth (der Server liest die Identität aus den Kopfzeilen, die der Proxy setzt) oder über die
  eingebaute Anmeldung ([Anmeldung und Rollen](anmeldung.md)). Agenten
  melden sich mit einem Bearer-Token an, und zwar nur unter `/agent/` oder auf einem eigenen
  Listener (siehe [Agenten-Pfade](#agenten-pfade)).
- **Ohne Anmeldung** (Einzelbetrieb) gibt es keine Identität. Dann gelten die
  Board-Rollen dieser Seite nicht: Die Board-Pfade sind für jeden offen, der den Port erreicht.
  Pfade, die eine Person oder einen Admin voraussetzen (`/admin/*`, `/users/me/*`, `/clients`,
  `/projects`), antworten dort mit `403`. Einzelheiten: [Anmeldung und Rollen](anmeldung.md).
- **Bodies sind JSON**, Antworten ebenso (Ausnahmen: Datei-Downloads, Ereignisströme, iCal-Feeds,
  die Weboberfläche).

### Wie die Rechte geprüft werden

Für alle Pfade unter `/boards/{id}/…` gilt eine gemeinsame Grundregel, die vor dem eigentlichen
Handler greift:

| Pfad | `GET`/`HEAD` | `POST`/`PUT`/`PATCH` | `DELETE` |
|---|---|---|---|
| `/boards/{id}` | viewer | editor | owner |
| `/boards/{id}/…` (alles darunter) | viewer | editor | editor |

Wer am Board keinen Eintrag hat, bekommt `403` („no access to board“), wer eine zu niedrige Rolle
hat, ebenfalls `403` („insufficient role“). Einige Handler prüfen zusätzlich strenger, etwa die
Freigabeliste (nur owner). In den Tabellen unten steht jeweils die **tatsächlich nötige** Rolle.

Rollen-Angaben in den Tabellen:

| Angabe | Bedeutung |
|---|---|
| öffentlich | keine Anmeldung; ggf. Token in der URL |
| angemeldet | jede angemeldete Person |
| viewer / editor / owner | Mindestrolle der Person am betroffenen Board |
| Admin | Server-Admin (Kennzeichen in der Nutzertabelle) |
| eigene | nur Daten der angemeldeten Person selbst |
| Agent | nur mit Agenten-Token über den Agenten-Eingang |

**Agenten** erreichen nur die Pfade, die ausdrücklich für sie freigegeben sind (Spalte „Agent“ bzw.
Abschnitt [Agenten-Pfade](#agenten-pfade)). Alles andere antwortet einem Agenten mit `403`, auch
jeder künftig hinzukommende Pfad.

## Fehlercodes

Fehler kommen als JSON mit genau einem Feld:

```json
{"error": "forbidden: insufficient role"}
```

Die Meldungen sind überwiegend englisch; die Werkzeug-Pfade für Agenten antworten mit deutschen
Sätzen, die ein Sprachmodell weitergeben kann.

| Code | Bedeutung |
|---|---|
| `400` | Anfrage ungültig: kaputtes JSON, fehlendes Pflichtfeld, unbekannter Schlüssel in einer Op, mehrdeutiger `/agent`-Pfad |
| `401` | keine Identität (Forward-Auth im strengen Modus), Agenten-Token fehlt, ist unbekannt, abgelaufen oder widerrufen — oder ein Agenten-Token (`abt_…`) an einem Pfad für Menschen |
| `403` | angemeldet, aber nicht berechtigt (keine oder zu niedrige Rolle, kein Admin, Pfad für Agenten nicht freigegeben, Cross-Origin-Anfrage aus dem Browser) |
| `404` | Board, Karte oder Datei gibt es nicht; auch Funktionen, die abgeschaltet sind (etwa die Ausgangsschleuse) |
| `409` | Konflikt: `PUT /boards/{id}` mit veraltetem `If-Match`, Name schon vergeben, letzter Owner würde entfernt |
| `413` | Body oder Ergebnis zu groß (z. B. mehr als 200 Ops, Board über 10 MiB) |
| `422` | eine Op würde anders wirken als beabsichtigt; die Antwort trägt ein `protokoll` mit `index` je abgelehnter Op |
| `428` | `PUT /boards/{id}` ohne `If-Match` |
| `429` | Ratenbegrenzung oder Fehlversuch-Sperre; teils mit `Retry-After` |
| `503` | Teilsystem nicht verfügbar (Datenbank, Schreibsperre nicht rechtzeitig frei, fehlender Serverschlüssel für verschlüsselte Funktionen) |

## CSRF-Schutz für Browser-Anfragen

Der Eingang für Menschen ist gegen Cross-Site Request Forgery geschützt. Eine **schreibende**
Anfrage (alles außer `GET`, `HEAD`, `OPTIONS`) wird mit `403` („cross-origin request rejected“)
abgewiesen, wenn

- der Browser sie mit `Sec-Fetch-Site: cross-site` oder `same-site` kennzeichnet, oder
- bei älteren Browsern der Host im `Origin`-Header nicht zum Host der Anfrage passt.

Auch `same-site` gilt als fremd: Eine andere Anwendung unter derselben Domain (etwa
`wiki.example.org` neben `board.example.org`) ist keine vertrauenswürdige Quelle.

Anfragen **ohne** diese Kopfzeilen (curl, Skripte, Agenten) sind keine Browser-Anfragen und laufen
durch. Unter `/agent/` und auf dem eigenen Agenten-Listener gibt es diesen Schutz nicht; dort zählt
nur das Bearer-Token.

## Boards

| Pfad | Methode | Rolle | Agent | Bedeutung |
|---|---|---|---|---|
| `/boards` | GET | angemeldet | – | Liste der Boards, auf denen die Person eine Rolle hat; je Eintrag `id`, `title`, `updated_at`, bei Forward-Auth `role` und, wenn auf dem Board jemand auf eine Antwort wartet, `awaiting_user` (Abbildung Person → Eintrag wie `board.awaitingUser`) |
| `/boards` | POST | angemeldet | – | Board anlegen; die anlegende Person wird owner |
| `/boards/{id}` | GET, HEAD | viewer | ✓ (eigenes Board) | ganzer Board-Stand; `ETag`, mit `If-None-Match` → `304` |
| `/boards/{id}` | PUT | editor | – | ganzen Stand ersetzen; `If-Match` Pflicht (`428` ohne, `409` bei fremder Änderung) |
| `/boards/{id}` | DELETE | owner | – | Board löschen |
| `/boards/{id}/ops` | POST | editor | ✓ (editor, eingeschränkt: vorschlag) | Änderungen als Op-Batch, am Server gefaltet; der normale Schreibweg |
| `/boards/{id}/events` | GET | viewer | ✓ | Ereignisstrom (Server-Sent Events) mit Live-Änderungen |
| `/boards/{id}/activities` | GET | viewer | – | Protokoll; `?card=<id>` für eine Karte, `?limit=<n>` (Standard 100, höchstens 500); Freigabe-Einträge nur für owner, Einträge zu Vorschlägen des internen Agenten (`vorschlag-*`) nur ab editor |
| `/boards/{id}/ich` | GET, HEAD | – | ✓ | Auskunft über das eigene Agenten-Token (Name, Akteur, Board, Rolle, Person); Menschen bekommen `404` |
| `/boards/{id}/vorschlaege` | GET | editor | – | offene Vorschläge des internen Agenten (Board-Paar, nur Seite außen, sonst `404`); ab v1.53.0 |
| `/boards/{id}/vorschlaege/{vid}/bestaetigen` | POST | editor | – | Vorschlag ausführen, Body `{"hash"}` (der angezeigte); nur angemeldete Sitzung. `409` bei anderem Hash, widerrufenem Zugang oder seit dem Vorschlag geänderter Karte, `410` abgelaufen |
| `/boards/{id}/vorschlaege/{vid}` | DELETE | editor | – | Vorschlag ablehnen und löschen |
| `/boards/{boardId}/vorschaubild` | GET | viewer | – | verkleinertes Hintergrundbild (JPEG) für die Board-Übersicht; `204` ohne Bild |
| `/boards/{boardId}/cards/{cardId}/transfer` | POST | editor (Quell- und Ziel-Board) | – | Karte samt Anhängen auf ein anderes Board verschieben |
| `/boards/{boardId}/cards/{cardId}/email` | POST | editor | – | Karte als HTML-Mail versenden (10 je Minute und Person) |
| `/boards/{id}/notifications/events` | POST | editor | – | Benachrichtigungs-Ereignis aus dem Browser auslösen |
| `/boards/{id}/schatten` | GET | viewer | – | Board-Paar, nur auf der Seite außen: Schatten interner Karten und Termine des Boards für die Anzeige; auf einer externen Kopie die vertraulichen — und alle Karten, solange das Skript den Hinweg im Format 2 holt (siehe [Board-Paar](#board-paar)) |

`POST /boards/{id}/ops` nimmt einen Batch von Änderungen (Ops) entgegen, höchstens 200 je Aufruf.
Eine fehlerhafte Anfrage antwortet mit `400`, eine Op, die anders wirken würde als beabsichtigt, mit `422`.

Auf einer **externen Kopie** des Board-Paars (siehe [Board-Paar](#board-paar)) lehnt der Server Änderungen an Spiegeln
ab: Ops antworten `422` (Protokollart `spiegel-geschuetzt`), `PUT /boards/{id}` und das Übertragen einer Karte auf ein
anderes Board `403`, das Zurückspielen einer Sicherung per „Überschreiben“ auf die Kopie `409`. Mit eingeschaltetem
„Bearbeiten außen erlauben“ gilt am Ops-Weg eine feinere Regel: Erlaubtes geht durch (und wird im Journal festgehalten), alles
andere bleibt `422`. `PUT /boards/{id}` und das Übertragen einer Karte bleiben für Spiegel auch dann immer `403`. Ist die Kopplung oder der Schalter beim Ops-Aufruf nicht lesbar: `503`, nichts geschrieben.

`PUT /boards/{id}` lässt den Schalter „vertraulich“ einer Karte oder eines Termins stehen, der im
gespeicherten Stand an ist, auch wenn der neue Stand ihn weglässt oder `false` trägt (Board-Paar). Abschalten geht nur über eine Op eines Menschen. Hat der Server dabei etwas stehen
lassen, nennt die Antwort es im Kopf `X-Aboard-Vertraulich-Bewahrt: <anzahl>; ids=<id>,…` (höchstens
20 Kennungen, die Anzahl zählt alle); der Body bleibt der Board-Stand.

## Freigaben und Personen eines Boards

| Pfad | Methode | Rolle | Agent | Bedeutung |
|---|---|---|---|---|
| `/boards/{id}/acl` | GET | owner | – | Freigabeliste (E-Mail und Rolle je Eintrag) |
| `/boards/{id}/acl` | POST | owner | – | Eintrag setzen, Body `{"email": "person@example.org", "role": "editor"}` |
| `/boards/{id}/acl?email=<adresse>` | DELETE | owner | – | Eintrag entfernen; der letzte owner lässt sich nicht entfernen (`409`) |
| `/boards/{id}/mitglieder` | GET | viewer | – | nur Anzeigenamen der Personen mit Rolle: `{"namen": [...], "gekappt": false}`; keine E-Mail-Adressen |
| `/known-users` | GET | angemeldet | – | bekannte Personen und Freigabe-Domains für die Freigabe-Auswahl |

## Anhänge

| Pfad | Methode | Rolle | Agent | Bedeutung |
|---|---|---|---|---|
| `/boards/{boardId}/attachments/{cardId}` | POST | editor | ✓ (editor) | Datei an eine Karte hochladen; Feld `attachmentId` (vom Client gewählt) ist neu zu wählen, eine schon belegte gibt `409` (nach v1.53.0). Auf einer externen Kopie des Board-Paars `422` für Spiegel-, wartende und neue Karten in markierten Lanes (mit und ohne Rückweg); ist Kopplung oder Stand nicht lesbar `503`, wiederholen |
| `/boards/{boardId}/attachments/{cardId}/{attId}` | GET, HEAD | viewer | ✓ | Datei herunterladen |
| `/boards/{boardId}/attachments/{cardId}/{attId}` | DELETE | editor | – | eine Datei löschen (`403`, wenn sie an einem fremden Kommentar hängt). Agenten löschen über `anhang_loeschen` |
| `/boards/{boardId}/attachments/{cardId}` | DELETE | editor | – | alle Dateien einer Karte löschen. Steht die Karte noch, bleiben Dateien an fremden Kommentaren (KF-3); dann `200` mit `{"geschuetztGeblieben": n}` statt `204`, der Browser holt den Rest nach dem Speichern nach. Ein Server bis v1.64.0 ignoriert `nurVerwaist` und löscht alles; beim Rückweg offene Tabs neu laden. Mit `?nurVerwaist=1` nur, wenn die Karte im gespeicherten Stand fehlt, sonst `409`. Geprüft wird zweimal, vor und nach dem Wegnehmen des Ordners; kommt die Karte dazwischen zurück, ebenfalls `409`, und die Dateien bleiben. Gibt es nichts zu löschen, kommt `204`; ebenso `409`, wenn der Stand nicht lesbar ist, `404` ohne Board (der Browser holt gescheiterte Purges so nach, nach v1.64.0) |
| `/boards/{boardId}/board-attachments` | POST | editor | ✓ (editor) | Datei ans Board hängen (z. B. Hintergrundbild); belegte `attachmentId` → `409` wie oben |
| `/boards/{boardId}/board-attachments/{attId}` | GET, HEAD | viewer | ✓ | Board-Datei herunterladen |
| `/boards/{boardId}/board-attachments/{attId}` | DELETE | editor | – | Board-Datei löschen. Agenten löschen über `anhang_loeschen` |

## Board-Sicherungen

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/boards/{id}/backup/files` | GET | owner oder Admin | vorhandene Sicherungen des Boards |
| `/boards/{id}/backup/download/{filename}` | GET | owner oder Admin | Sicherung herunterladen |
| `/boards/{id}/backup/now` | POST | owner oder Admin | sofort sichern |
| `/boards/{id}/backup/files/{filename}` | DELETE | owner oder Admin | Sicherung löschen |
| `/backup/restore` | POST | angemeldet; owner oder Admin am Quell-Board des Archivs, beim Überschreiben zusätzlich am Ziel-Board | Archiv zurückspielen (als neues Board oder über ein bestehendes) |
| `/admin/backup/all-now` | POST | Admin | alle Boards sofort sichern |
| `/admin/backup/all-status` | GET | Admin | Stand des letzten Gesamtlaufs |

Agenten erreichen keinen dieser Pfade. Betrieb und Aufbewahrung beschreibt
[Administration](administration.md).

## Persönliche Daten (`/me`, `/users/me`)

Alle Pfade dieses Abschnitts beziehen sich auf die angemeldete Person selbst; eine Personen-ID im
Pfad gibt es nicht. Agenten erreichen sie nicht.

| Pfad | Methode | Bedeutung |
|---|---|---|
| `/me` | GET | eigene Identität (Name, E-Mail, Admin-Kennzeichen); ohne Anmeldung leere Felder. Je nach Einrichtung zusätzlich `owui_url`, `schleuse: "an"`, `wissen` (Anzeigename der Wissensquelle, nur wenn sie an ist und die Person sie sehen darf), `boardpaar` (`"innen"` oder `"aussen"`; fehlt bei `aus`; statt seiner steht `boardpaar_fehler: true`, wenn die Seite nicht lesbar ist) und, nur außen bei eingeschaltetem „Bearbeiten außen erlauben“ (ab v1.55.0), `boardpaar_rueckweg: "an"` (fehlt sonst; bei nicht lesbarer Einstellung ebenfalls `boardpaar_fehler: true`, und `boardpaar` fehlt dann). Ab v1.56.0 gilt der Wert nur zusammen mit dem Schalter „bearbeitbar“ der jeweiligen Kopie aus `GET /boards/{id}/schatten` |
| `/me/cards?dueDateRange=<von>~<bis>` | GET | Karten mit Datum über alle Boards mit Leserecht (Kalender „alle Boards“) |
| `/me/termine?dueDateRange=<von>~<bis>` | GET | Termine ohne Karte über alle Boards mit Leserecht |
| `/users/me/agent-tokens` | GET | eigene Agenten-Tokens und Boards, für die die Selbstbedienung an ist |
| `/users/me/agent-tokens` | POST | Agenten-Token anlegen (Klartext nur in dieser Antwort); `"board"` oder für mehrere Boards `"boards": [...]` bzw. `"alle_boards": true`; 20 Änderungen je Stunde; `"anbieter"`/`"beleg"` wie bei `/admin/agent-tokens` (Pflicht außen, sonst optional), die Liste trägt beide |
| `/users/me/agent-tokens/{id}` | DELETE | eigenes Token widerrufen |
| `/users/me/llm-keys` | GET | hinterlegte KI-Anbieter (ohne Schlüssel) |
| `/users/me/llm-keys/{provider}` | POST, DELETE | eigenen Anbieter-Schlüssel setzen bzw. löschen |
| `/users/me/llm-keys/{provider}/test` | POST | Schlüssel gegen den Anbieter prüfen |
| `/users/me/notifications/event-types` | GET | verfügbare Ereignisarten |
| `/users/me/notifications/subscriptions` | GET | eigene Abos |
| `/users/me/notifications/subscriptions/{eventType}/{channel}` | PUT | Abo setzen |
| `/users/me/notifications/config/{channel}` | GET, PUT, DELETE | Zustellweg einrichten (z. B. Adresse) |
| `/users/me/notifications/test/{channel}` | POST | Testnachricht senden |

## Kalender-Feeds

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/me/ical/subscriptions` | GET, POST | eigene | eigene iCal-Abos auflisten bzw. anlegen (braucht den Serverschlüssel, sonst `503`) |
| `/me/ical/subscriptions/{id}` | DELETE | eigene | Abo widerrufen |
| `/ical/calendar.ics?token=<token>` | GET | öffentlich (Token in der URL) | iCal-Feed für Kalender-Apps; höchstens 120 Abrufe je Stunde und Abo, Abrufe mit unbekanntem Token höchstens 120 je Stunde und Adresse, sonst `429` |

Kalender-Apps sprechen kein Forward-Auth. Steht der Server hinter einem Anmelde-Proxy, muss `/ical/*`
dort von der Anmeldung ausgenommen werden; die Anmeldung ist das Token. Die Adresse für Abrufe mit
unbekanntem Token bestimmt dieselbe Regel wie bei der Agenten-Bremse: `X-Real-IP` nur von der Gegenstelle
aus `ABOARD_AGENT_PROXY`, sonst die TCP-Adresse, nie `X-Forwarded-For` (nach v1.53.0). Steht ein
Proxy davor und ist `ABOARD_AGENT_PROXY` leer, teilen sich alle Abrufe mit unbekanntem Token die Adresse des
Proxys und damit einen Topf; gültige Abos betrifft das nicht, sie zählen je Abo. **Grenze dieser Wahl:** Wer die Feed-Adresse eines Abos kennt, kann dessen Kontingent (120 je Stunde) aufbrauchen, und ein Abo, das viele
Geräte teilen, teilt sich auch das Kontingent. Abhilfe ist ein eigenes Abo je Gerät; eine geleakte Adresse widerruft
man ohnehin.

**Board-Paar:** Auf einer Instanz mit der Seite `innen` lehnt `POST /me/ical/subscriptions` neue Abos mit
`409` („Kalender abonnierst du auf dem externen Board“) ab; Liste, Feed und Löschen bestehender Abos
bleiben. Auf der Seite `aussen` enthält der Feed zusätzlich die Schatten mit Datum der gekoppelten Boards,
die die Person lesen darf, und jeder Eintrag trägt `X-ABOARD-ART` (`card`, `termin`, `schatten-card`,
`schatten-termin`). Ist die Seite dort nicht lesbar, antwortet der Feed mit `503`.

Den datensparsamen Feed „für außen“ (`/ical/aussen/…`, Token-Art `kalender-ausgang`) gibt es nicht
mehr. Ein noch vorhandenes Abo „für außen“ liefert nichts und lässt sich nur löschen.

## Kunden und Projekte

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/clients` | GET | angemeldet | Kunden auflisten |
| `/clients` | POST | Admin | Kunden anlegen |
| `/clients/{id}` | PATCH, DELETE | Admin | Kunden ändern bzw. löschen |
| `/clients/{id}/projects` | GET | angemeldet | Projekte eines Kunden |
| `/projects` | GET | angemeldet | Projekte auflisten |
| `/projects` | POST | Admin | Projekt anlegen |
| `/projects/{id}` | PATCH, DELETE | Admin | Projekt ändern bzw. löschen |
| `/projects/{id}/cards` | GET | angemeldet | Karten, die einem Projekt zugeordnet sind |

## KI-Funktionen

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/boards/{boardId}/cards/{cardId}/llm-send` | POST | editor | Karte an ein Sprachmodell schicken, Antwort am Stück |
| `/boards/{boardId}/cards/{cardId}/llm-send-stream` | POST | editor | dasselbe als Ereignisstrom |
| `/boards/{boardId}/cards/{cardId}/llm-pending` | GET | viewer | laufende bzw. vorgemerkte Anfrage einer Karte |
| `/boards/{boardId}/cards/{cardId}/llm-discard` | POST | editor | eigene KI-Antwort verwerfen |
| `/boards/{boardId}/cards/{cardId}/conversations` | GET, POST | viewer bzw. editor | Gesprächsverläufe einer Karte lesen bzw. anlegen |
| `/boards/{boardId}/cards/{cardId}/conversations/{convId}/turns` | GET | viewer | Beiträge eines Verlaufs |
| `/boards/{boardId}/cards/{cardId}/conversations/{convId}/archive` | POST | editor | Verlauf archivieren |
| `/boards/{boardId}/cards/{cardId}/conversations/{convId}` | DELETE | editor | Verlauf löschen |
| `/llm/pricing` | GET | angemeldet | Preistabelle der Modelle |

Einrichtung der Anbieter und des Firmenmodus (ein gemeinsamer KI-Zugang des Betreibers für alle Personen): [Administration](administration.md#ki-zugang).

## Mail-Eingang

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/mail-spam-report` | POST | editor am Service-Board | Absender auf die Sperrliste setzen, Body `{"senderEmail": "…"}`; mit `"undo": true` wieder entfernen (30 je Minute und Person) |

## Wissensquelle

Diese Pfade gibt es nur, wenn ein Admin die Wissensquelle eingeschaltet hat (Reiter „Wissen“, siehe
[Administration](administration.md#wissensquelle)); sonst antworten sie mit `404`. Sie lesen nur,
Antworten sind JSON. Agenten-Zugänge erreichen sie nicht.

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/wissen/index` | GET | Konto (bei `wissen.sichtbar=admins` nur Admin, bei `gruppe:<name>` nur Gruppe laut `Remote-Groups` oder Admin) | Name, Stand, Seiten (`pfad`, `titel`, `groesse`), ggf. `gekappt` |
| `/wissen/seite?pfad=…` | GET | wie oben | eine Seite als Markdown-Text (`inhalt`), ggf. Link `bearbeiten` |
| `/wissen/suche?q=…` | GET | wie oben | Treffer mit `pfad`, `titel`, `schnipsel`, `zeile`; `q` 2 bis 200 Zeichen |

Fehler: `401` ohne Anmeldung (hinter dem Anmelde-Proxy; ohne Proxy `403`), `403` ohne Konto auf
diesem Server oder ohne Sichtbarkeit, `404` aus oder unbekannte Seite, `413` Seite über 1 MiB, `429`
mehr als 120 Abrufe bzw. 30 Suchen je Minute und Person, `503` Ordner nicht lesbar.

## Ausgangsschleuse

Diese Pfade gibt es nur, wenn die Ausgangsschleuse eingeschaltet ist (siehe
[Konfiguration](konfiguration.md)); sonst antworten sie mit `404`. Was die Schleuse tut, beschreibt
[Administration](administration.md).

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/users/me/schleuse-schluessel` | GET | eigene | eigene hinterlegte Schlüssel (ohne Klartext) |
| `/users/me/schleuse-schluessel/{id}` | PUT, DELETE | eigene | Schlüssel hinterlegen bzw. löschen |
| `/users/me/schleuse-auftraege/{aid}` | DELETE | eigene | eigenen Auftrag löschen |
| `/boards/{id}/schleuse-sperre` | GET | viewer | ist das Board für die Schleuse gesperrt? |
| `/boards/{id}/schleuse-sperre` | PUT | owner | Sperre setzen oder aufheben |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege` | GET, POST | viewer bzw. editor | Aufträge einer Karte auflisten bzw. Auszug erstellen |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}` | GET, PUT, DELETE | viewer bzw. editor | Auftrag lesen, ändern, löschen |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/freigabe` | POST | editor | Auftrag freigeben |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/zweitfreigabe` | POST | editor | zweite Freigabe (Vier-Augen-Prinzip) |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/bilder/{bid}` | GET | viewer | Bildvorschau eines Auftrags |
| `/boards/{id}/cards/{cardId}/schleuse-belege/{aid}` | GET | viewer | Beleg eines versandten Auftrags |
| `/admin/schleuse/freigaben` | GET, POST | Admin | Liste der freigegebenen Ziele |
| `/admin/schleuse/freigaben/{id}` | PUT, DELETE | Admin | Ziel ändern bzw. entfernen |

Die Rollenangaben dieser Tabelle folgen der Grundregel für `/boards/…`; die Handler prüfen darüber
hinaus die Schleusen-eigenen Bedingungen (Sperre, Schlüssel, Freigaben).

## Administration (`/admin/*`)

Alle Pfade verlangen einen Server-Admin; schreibende Aufrufe sind zusätzlich ratenbegrenzt. Ohne
Anmeldung (Einzelbetrieb ohne Forward-Auth) gibt es keinen Admin, dann antworten sie mit `403` —
für Agenten-Tokens bleibt dort die Kommandozeile (`aboard-server agent-token`, siehe [Container](container.md)).

| Pfad | Methode | Bedeutung |
|---|---|---|
| `/admin/status` | GET | Server-Status (Version, Teilsysteme) |
| `/admin/config` | GET | alle Einstellungen der Admin-Oberfläche |
| `/admin/config/{section}` | GET | Einstellungen eines Bereichs |
| `/admin/config/{key}` | PUT, DELETE | Einstellung setzen bzw. auf Vorgabe zurücksetzen |
| `/admin/config/{section}/test` | POST | Bereich testen (z. B. Mail-Verbindung) |
| `/admin/config/reload` | POST | Einstellungen neu laden |
| `/admin/audit` | GET | Änderungsprotokoll der Admin-Aktionen |
| `/admin/users` | GET | Personen auflisten |
| `/admin/users/{id}/admin` | PATCH | Admin-Kennzeichen setzen oder entziehen, Body `{"is_admin": true}` |
| `/admin/users/{id}` | DELETE | Person löschen (widerruft auch ihre Agenten-Tokens) |
| `/admin/einladungen` | POST | nur eingebaute Anmeldung: einladen, Body `{"emails": ["…"]}` (1–50); Antwort `{"ergebnisse": [...]}`, je Adresse `email`, `konto_id`, `link` (nur hier sichtbar), `ablauf`, `erneut`, `mail` (`gesendet`, `nicht_eingerichtet`, `fehlgeschlagen`) oder `fehler` (`adresse`, `hat_zugang`, `schon_eingeloest`, `belegt` = Datenbank gerade belegt, gleich erneut, `intern`); Mails laufen parallel mit gemeinsamer Frist von 20 s; zählt einmal in der Drossel; ohne eingebaute Anmeldung `404` |
| `/admin/einladungen/{id}` | DELETE | nur eingebaute Anmeldung: Einladung zurückziehen (Konto im Zustand „eingeladen“ samt Links); `204`, `404` bei unbekannter ID, `409`, wenn schon eingelöst |
| `/admin/konten` | GET | nur eingebaute Anmeldung: Antwort `{"konten": [...]}` mit Zustand, gewähltem Weg, gemeldeter Adresse, Ablauf des Links sowie `hat_zugang` (Person hat eine Nutzerzeile), `neuer_link` (Knopf „Neuer Link“ möglich: eingeladen mit Rücksetz-Link oder Nutzerzeile) und `widerrufbar` (fehlt bei Rücksetz-Link und Nutzerzeile zugleich) |
| `/users/me/konto` und `/users/me/konto/…` | GET/POST | nur eingebaute Anmeldung, nur mit Sitzung (kein Agent): Konto-Einstellungen der angemeldeten Person — Stand, `passwort`, `2fa/start`+`2fa/abschluss`, `codes`, `sitzungen/beenden`, `wege/verknuepfen`, `wege/loesen`, `frisch/start` (frische Anbieter-Anmeldung), `passwort/anlegen/start`+`/abschluss` (nur mit `ABOARD_ACCOUNT_SELF_PASSWORD`). Fehler als `{"fehler": "…"}`, u. a. `409` `frische_anmeldung_noetig`, `passwort_nutzen`, `kein_passwort`, `konto_zustand`, `letzter_weg`; falsche Bestätigung `403` |
| `/admin/boards` | GET | alle Boards des Servers |
| `/admin/agent-tokens` | GET | alle Agenten-Tokens (nie der Klartext) |
| `/admin/agent-tokens` | POST | Agenten-Token anlegen, Body `{"name", "board", "role"}`, optional `"user"` (E-Mail) und `"gueltig_tage"`; mit `"user"` statt `"board"` auch `"boards": [...]` oder `"alle_boards": true` (Token für mehrere Boards); `"anbieter"` (höchstens 80 Zeichen) und `"beleg"` (höchstens 300 Zeichen, Fundstelle des Trainingsausschlusses; beide ohne Steuer-, Nullbreiten- und Bidi-Zeichen, ohne Semikolon und mit mindestens einem Buchstaben, einer Ziffer oder einem Satz-/Symbolzeichen, sonst `400`) sind auf der Seite außen eines Board-Paars Pflicht (sonst `400`), sonst optional; die Liste trägt beide Felder |
| `/admin/agent-tokens/{idOderName}` | DELETE | Token widerrufen (bevorzugt per id) |
| `/admin/users/{id}/agent-tokens/revoke` | POST | alle Agenten-Tokens einer Person widerrufen |
| `/admin/agent-selbstbedienung` | GET | Boards mit eingeschalteter Selbstbedienung |
| `/admin/boards/{id}/agent-selbstbedienung` | PUT | Selbstbedienung für ein Board schalten, Body `{"an": true}` |
| `/admin/backup/all-now`, `/admin/backup/all-status` | POST, GET | siehe [Board-Sicherungen](#board-sicherungen) |
| `/admin/schleuse/freigaben[/{id}]` | GET, POST, PUT, DELETE | siehe [Ausgangsschleuse](#ausgangsschleuse) |
| `/admin/config/boardpaar.seite` | PUT, DELETE | Seite im Board-Paar: `aus`, `innen` oder `aussen` (sonst `400`); `DELETE` heißt `aus`. Ab v1.53.0 `409`, solange auf der bisherigen Seite Boards gekoppelt sind (die Meldung nennt sie), `503`, wenn die heutige Seite nicht lesbar ist |
| `/admin/boardpaar` | GET | Seite, Partnerlisten innen und außen (ab v1.53.0 je Board `letzte_abfrage` bzw. `letzter_eingang` und `letzter_fehlgrund`; außen je Board `kopie`), Zugänge (ohne Geheimnis), `ziele`, `auftraege`, `team`, `rueckweg` (`aus`/`an`), `link_basis_vorgabe` und (ab v1.55.0) `kopien_entkoppelt` — außen entkoppelte Kopien als `[{"board_id", "titel"}]` |
| `/admin/boardpaar/innen` | POST | internes Board koppeln, Body `{"board_id"}` |
| `/admin/boardpaar/innen/{board}` | DELETE | internes Board entkoppeln |
| `/admin/boardpaar/aussen` | POST | externes Board koppeln oder seine Adresse ändern, Body `{"board_id", "innen_link_basis"}`; `409` für eine außen entkoppelte Kopie (Spiegel-Marken oder Herkunft „Kopie“) — sie kommt nur über einen neuen Auftrag zurück |
| `/admin/boardpaar/aussen/{board}` | DELETE | externes Board entkoppeln; seine Schatten gehen mit. Eine externe Kopie behandelt die Schreibschranke danach wie ein gewöhnliches Board (Ops, Agenten); der Server merkt sich aber ihre Herkunft (Notweg, Liste „Außen entkoppelte Kopien“, Wiederaufnahme, und von Hand koppeln ergibt `409`); ab v1.55.0 gibt auch der Browser die früheren Spiegel frei (bis v1.54.0 blieben sie dort gesperrt). Die Spiegel bleiben **mit vollem Inhalt** stehen, Änderungen von außen gehen nicht mehr nach innen — soll die Kopie weg, ist `DELETE /admin/boardpaar/kopien/{board}` der Weg (Einzelheiten: [Administration](administration.md#externe-kopie-anlegen)) |
| `/admin/boardpaar/kopien/{board}` | DELETE | ab v1.55.0: Notweg „Kopie außen löschen“ — löscht eine externe Kopie samt Kopplung, Schatten, Journal, Anhängen, ohne die Board-Rolle owner zu verlangen. Nur auf der Seite `aussen` (sonst `409`) und nur für Kopien: gekoppelt mit `kopie` oder außen entkoppelt (vom Server als Kopie angelegt — Migration 0042 —, keine Kopplung); jedes andere Board `409`, auch mit selbst gesetzten Spiegel-Marken, unbekannt `404`, Datenbank belegt `503`. Antwort `{"ok": true, "board_id", "entkoppelt"}`. Innen ändert sich nichts; ein neuer Auftrag legt die Kopie frisch an ([Administration](administration.md#board-paar-intern-und-extern)) |
| `/admin/boardpaar/kopien/{board}/bearbeitbar` | PUT | ab v1.56.0: Schalter „bearbeitbar“ einer Kopie, Body `{"bearbeitbar": true\|false}` (Pflicht; fehlt er oder hat einen anderen Typ: `400`). Bearbeitbar ist die Kopie nur zusammen mit `boardpaar.rueckweg = an`; sonst gilt für sie der Schreibschutz. Nur auf der Seite `aussen` und nur für gekoppelte Kopien (`kopie`): Stufe-1-Kopplung oder andere Seite `409`, nicht gekoppelt `404`. Antwort `{"ok": true, "board_id", "bearbeitbar"}`. Schickt das Skript das Feld am nächsten Hinweg, gewinnt sein Wert. Die Partnerliste (`GET /admin/boardpaar`, `aussen[]`) trägt den Wert je Eintrag als `bearbeitbar` |
| `/admin/boardpaar/zugaenge` | POST | Zugang anlegen, Body `{"typ": "abs", "abe" oder "abr", "name"}`, optional `"darf_anlegen": true` (nur `abe`: darf außen Kopien anlegen); `201` mit dem Klartext, nur in dieser Antwort |
| `/admin/boardpaar/zugaenge/{id}` | DELETE | Zugang widerrufen |
| `/admin/boardpaar/auftraege` | POST | „Externe Kopie anlegen“ (nur Seite `innen`, sonst `409`), Body `{"board_id", "ziel"}`, optional `"innen_link_basis"` (sonst `ABOARD_PUBLIC_BASE_URL` + `/#board=<id>`, fehlt beides `400`); `201` mit dem Auftrag |
| `/admin/boardpaar/auftraege/{id}` | DELETE | offenen Auftrag zurückziehen; `409`, wenn er schon erledigt ist |
| `/admin/boardpaar/team` | PUT | ab v1.55.0: Board-Paar-Team speichern, Body `{"team", "bisher", "bestaetigt"}` — `bisher` (Pflicht, sonst `400`) ist der zuletzt gelesene Wert. Entzöge das Speichern Zugriff auf Kopien und fehlt `"bestaetigt": true`: `409 {"bestaetigung_noetig": true, "weg": [...], "zeilen": n}`, nichts geschrieben. Stimmt `bisher` nicht mehr: `409 {"veraltet": true}`. Sonst `200 {"ok", "team", "weg", "zeilen"}` |
| `/admin/config/boardpaar.team` | PUT, DELETE | Board-Paar-Team (nur außen): E-Mail-Adressen oder `@domain`, durch Komma getrennt; ungültige Einträge lehnt der Server beim Speichern ab. Ab v1.55.0 nur noch für Änderungen **ohne** Entzug — fiele jemand mit Zugriff auf Kopien heraus (auch beim `DELETE`), `409`; dafür `PUT /admin/boardpaar/team` |
| `/admin/config/boardpaar.rueckweg` | PUT, DELETE | „Bearbeiten außen erlauben“ (nur außen; ab v1.55.0): `aus` oder `an` (sonst `400`); `DELETE` heißt `aus`. Beim Wechsel gleicht der Server in derselben Transaktion die Rolle des Teams auf allen Kopien an (`an`: Leser werden Bearbeiter, sonst umgekehrt) |

Fehler beim Board-Paar: `400` (fehlende Board-ID, Service-Board, ungültige Adresse, Typ oder Name, `darf_anlegen`
bei einem anderen Typ als `abe`, ungültiger `ziel`-Name, fehlende `innen_link_basis` ohne `ABOARD_PUBLIC_BASE_URL`), `404` (Board unbekannt, nicht gekoppelt, kein aktiver Zugang mit dieser id, kein
solcher Auftrag), `409` (aktiver Zugang dieses Typs mit diesem Namen besteht schon; für Board und Ziel liegt schon ein
offener Auftrag; Auftrag schon erledigt; das Board ist eine externe Kopie und lässt sich nicht von Hand koppeln).

## Board-Paar

Wege des Board-Paars intern/extern (Schatten, Kopie, Spiegel und Rückweg). Welche es gibt,
entscheidet die Seite der Instanz (`boardpaar.seite`), die der Server bei jeder Anfrage liest. Auf jeder anderen
Seite, bei fehlendem oder ungültigem Wert und für Boards außerhalb der jeweiligen Partnerliste antworten sie mit
`404`, ohne Unterschied zu „gibt es nicht“. Einrichten: [Administration](administration.md#board-paar-intern-und-extern).

| Pfad | Methode | Seite | Zugang | Bedeutung |
|---|---|---|---|---|
| `/agent/schatten/{boardId}` | GET | innen | `Bearer abs_…` | Schatten-Ausgang: ganze Schattenliste eines internen Boards für das Push-Skript, `{"version", "stand", "schatten": [...]}` |
| `/agent/schatten/{boardId}` | PUT | außen | `Bearer abe_…` | Schatten-Eingang: ersetzt die Schatten eines externen Boards durch die gesendete Menge, Body genau `{"version", "schatten": [...]}`; Antwort `{"neu", "geaendert", "entfernt", "anzahl"}` |
| `/agent/boardpaar/erneuern` | POST | innen / außen | `Bearer abs_…` oder `abr_…` innen, `abe_…` außen; `abb_…` auf jeder Seite (ab v1.56.0) | ab v1.53.0: tauscht das Token gegen ein neues, ohne Body; Antwort `{"token", "typ", "name", "alt_gilt_bis", "wiederholt"}`. Das alte gilt bis `alt_gilt_bis` (10 min), eine Wiederholung in dieser Zeit liefert dasselbe neue Token (`"wiederholt": true`); danach, widerrufen oder unbekannt: `401`; `503` + `Retry-After`, solange die Systemuhr nicht gestellt ist. Eigene Drossel: 30 Fehlversuche je Stunde und Adresse. |
| `/boards/{id}/schatten` | GET | außen | Person, viewer | Schatten für die Anzeige im Browser: `{"letzter_abgleich", "schatten": [...]}`, je Eintrag `schatten_id`, `art`, `nummer`, `titel`, `titel_platzhalter`, `spalte`, `prioritaet`, Datum und Uhrzeit, `bearbeiter`, `von_agent` und `link` (Adresse des internen Boards, bei Karten mit `&card=<nr>`); auf oberster Ebene `kopie` und ab v1.56.0 `bearbeitbar` (Schalter der Kopie — der Browser macht Spiegel nur bearbeitbar, wenn zusätzlich `/me` `boardpaar_rueckweg: "an"` meldet); `Cache-Control: no-store` |

Am eigenen Agenten-Listener heißen die Wege ohne Präfix `/agent`: `/schatten/{boardId}`, `/boardpaar/…`.

**Externe Kopie, Spiegel und Rückweg (Stufe 2).** Die Kopie selbst (Aufträge, `boards`, Hinweg im bisherigen Format) gibt es
ab v1.54.0; Spiegel (`?format=3`, `faehigkeiten`), Journal (`aenderungen`), `anwenden` und `boardpaar.rueckweg` erst mit
v1.55.0. Diese Wege gelten nur für Boards mit Auftrag „Externe Kopie“ innen
bzw. nur für externe Kopien außen. Wege mit Board-ID antworten `404`, wenn das Board auf der Gegenseite liegt,
nicht gekoppelt ist oder außen keine Kopie ist; die Wege ohne Board-ID (`auftraege`, `ziele`, `boards`, `erneuern`)
antworten auf der falschen Seite mit `404`. Die Seite wird **vor** dem Token geprüft.

| Pfad | Methode | Seite | Zugang | Bedeutung |
|---|---|---|---|---|
| `/agent/boardpaar/auftraege` | GET, HEAD | innen | `abs_…` | offene Aufträge „Kopie anlegen“: `{"auftraege": [{"id", "board_id", "ziel", "titel", "innen_link_basis"}]}`, nur von Boards, die noch in der Partnerliste innen stehen; `Cache-Control: no-store` |
| `/agent/boardpaar/auftraege/{id}/erledigt` | POST | innen | `abr_…` | Auftrag erledigt, ohne Body; Antwort `{"id", "status"}`; wiederholbar, unbekannte id `404` |
| `/agent/boardpaar/ziele` | PUT | innen | `abr_…` | Namen der Ziele melden, Body `{"ziele": ["name", …]}` (höchstens 20, je 1–64 Zeichen aus Buchstaben, Ziffern, `. _ -`); ersetzt die bisherige Menge; Antwort `{"ziele": [...]}`; `422` bei Abweichung |
| `/agent/boardpaar/spiegel/{innenBoardId}` | GET, HEAD | innen | `abs_…` | Hinweg holen: `{"format", "version", "stand", "struktur", "schatten", "spiegel", "faehigkeiten"}`. `?format=3` liefert zusätzlich nicht vertrauliche Karten und Termine als Spiegel und den Titel des Boards; ohne den Parameter (oder mit `2`, wie ihn das Skript aus Stufe 2a schickt) kommen alle Karten als Schatten; anderes `format` `400`. `faehigkeiten: ["anwenden"]` heißt: dieser Server nimmt den Rückweg an. `404` ohne Kopplung oder ohne Auftrag, `500` bei mehr als 5000 Einträgen („archive done cards“) oder unlesbarem Board-Zustand |
| `/agent/boardpaar/boards` | POST | außen | `abe_…` mit „darf Kopien anlegen“ | Kopie anlegen, Body genau `{"id", "titel", "innen_link_basis"}` und optional `"bearbeitbar"` (`true`/`false`, ab v1.56.0; fehlt es oder ist `null`, bekommt eine neue Kopie `false` und eine bestehende behält ihren Wert; gesendet gilt es auch bei `200` und bei der Wiederaufnahme; ein anderer Typ ist `422`) (`id`: UUID in Kleinbuchstaben); `201` neu angelegt oder wiederaufgenommen (`"wiederaufgenommen": true`), `200` gab es schon (wiederholbar), `403` ohne das Recht, `409` die id gehört einem Board, das keine Kopie ist (auch eine Kopie, die vor Migration 0042 außen entkoppelt wurde), `422` bei falschem Rumpf, `503` + `Retry-After: 5` bei belegter Datenbank. Antwort `{"id", "titel", "innen_link_basis", "wiederholt", "wiederaufgenommen", "mitglieder", "bearbeitbar"}` (nur die Zahl der Mitglieder; `bearbeitbar` = Wert nach dem Aufruf) |
| `/agent/boardpaar/spiegel/{aussenBoardId}` | PUT | außen | `abe_…` | Hinweg einspielen; Antwort wie beim Schatten-Eingang (`neu`, `geaendert`, `entfernt`, `anzahl`, dazu `struktur_geaendert` für „Board-Zustand geschrieben“) und `spiegel: {neu, geaendert, entfernt, anzahl}`, ab v1.56.0 dazu `bearbeitbar` (Wert nach dem Lauf). Optionaler Hüllen-Schlüssel `bearbeitbar` (`true`/`false`): setzt den Schalter der Kopie in derselben Transaktion; fehlt er oder ist `null`, bleibt der Wert; ein anderer Typ ist `422` (nichts geschrieben). Mit dem Schlüssel nur ein `abe_` mit „darf Kopien anlegen“, sonst `403`: Board, Schalter und Rollen bleiben unverändert, auch der übrige Hinweg wird nicht eingespielt; nur der Eingangs-Merker hält den Fehlgrund fest. Ein Wechsel geht als `schatten:aktualisiert` an offene Browser. Fehler wie der Schatten-Eingang: `409` abweichende Hauptversion, `413`, `422`, `429` + `Retry-After: 5` (Lauf läuft schon), `503` + `Retry-After: 5` |
| `/agent/boardpaar/aenderungen/{aussenBoardId}` | GET, HEAD | außen | `abe_…` | Journal der Änderungen außen holen (`?nach=<seq>&max=<n>`, höchstens 200): `{"eintraege", "weitere", "letzte_seq", "veraltet"}`. `veraltet: true`, wenn ein Eintrag 25 Tage unbestätigt ist. `HEAD` liest nur und merkt die Abholung nicht. `409`: `nach` liegt hinter dem Journal-Ende (außen wurde eine Sicherung zurückgespielt), `422` bei ungültigen Zahlen, `503` + `Retry-After: 5` |
| `/agent/boardpaar/aenderungen/{aussenBoardId}/bestaetigt` | POST | außen | `abe_…` | abgeholte Einträge bestätigen, Body `{"bis": <seq>}`; Antwort `{"bestaetigt": <Zahl>}`; `422` bei falschem Rumpf |
| `/agent/boardpaar/anwenden/{innenBoardId}` | POST | innen | `abr_…` | **einen** Rückweg-Eintrag auf das interne Board anwenden (Rumpf, genau diese Schlüssel, unbekannte ergeben `422`: `op_id` (1–126 Byte, die Op-ID des Skripts), `objekt` (`karte` oder `termin`), `id` (innere ID), `art`, `stelle`, `vorher`, `nachher`, `ursprung` (nur und immer bei `angelegt`, Karte oder Termin: die Außen-ID), `person`, `zeit`; `stelle`, `art`, `person` und `zeit` stammen unverändert aus dem Journal, in `vorher`/`nachher` übersetzt das Skript Kennungen in innere IDs (Lane, Spalte, Anker `nach`, `karteId`)). Antwort `200 {"ergebnis": "angewendet" \| "schon-angewendet" \| "konflikt" \| "verworfen", "grund"?}`. Bei einem Konflikt schreibt der Server an der Karte einen Kommentar des Autors „Board-Paar“ (siehe [Administration](administration.md#bearbeiten-außen-erlauben)). `404` ohne Kopplung oder Auftrag, `409` dieselbe `op_id` mit anderem Inhalt, `413` über 1 MiB, `422` fehlerhafter Eintrag, `503` + `Retry-After: 5` wenn sich das Board währenddessen zu schnell ändert; Ratenbegrenzer des Ops-Wegs: `429` `rate limit exceeded` (ohne `Retry-After`) bzw. `lock time budget exceeded` (mit `Retry-After: 10`) |

**Sicherungen lesen (`abb_`, ab v1.56.0).**
Ein Abholskript holt mit diesem Zugang die nächtlichen Board-Sicherungen ab. Die beiden Wege gelten **auf jeder Seite**,
auch bei `boardpaar.seite = aus`, und nur für fertige, verschlüsselte Archive (`.tar.gz.enc`); ohne Verschlüsselung ist
die Liste leer. Zurückspielen geht nur über `POST /backup/restore` (angemeldet).

| Pfad | Methode | Seite | Zugang | Bedeutung |
|---|---|---|---|---|
| `/agent/sicherungen` | GET, HEAD | jede | `abb_…` | `{"boards": [{"board_id", "kopie", "files": [{"filename", "size", "mtime", "encrypted"}]}]}` — alle Boards mit mindestens einer abholbaren Sicherung, ohne Titel; `kopie`: außen als Kopie entstanden |
| `/agent/sicherungen/{boardId}/{filename}` | GET, HEAD | jede | `abb_…` | das Archiv Byte für Byte (`application/octet-stream`, `Content-Length`); `404` für alles andere (unverschlüsselt, unfertig, fremdes oder unbekanntes Board). Schreibfrist 60 s + 1 s je 256 KiB, höchstens 30 min |

**Die Skript-Wege** laufen über den Agenten-Eingang, aber nicht durch die Agenten-Prüfkette: Ein
Board-Paar-Token geht direkt an den Handler, ohne Agentenrolle und ohne Board-Freigabe.

- Jeder Zugang erreicht nur seine Wege aus den Tabellen oben: `abs_` die lesenden innen (`GET` auf `/schatten/<ein Segment>`,
  `/boardpaar/auftraege`, `/boardpaar/spiegel/<ein Segment>`), `abe_` die Wege außen (`PUT` auf
  `/schatten/<ein Segment>` und `/boardpaar/spiegel/<ein Segment>`, `POST /boardpaar/boards`, das Journal
  `GET`/`HEAD …/aenderungen/<ein Segment>` und `POST …/aenderungen/<ein Segment>/bestaetigt`), `abr_` `PUT /boardpaar/ziele`, `POST /boardpaar/auftraege/<id>/erledigt` und
  `POST /boardpaar/anwenden/<board>`, `abb_` `GET`/`HEAD /sicherungen` und `/sicherungen/<board>/<datei>`; alle vier dazu
  `POST /boardpaar/erneuern` (`abb_` auf jeder Seite, auch bei `aus`). Auf jedem anderen Weg, mit jeder
  anderen Methode und am Eingang für Menschen antwortet der Server mit `403`.
- `401`: ein unbekanntes oder widerrufenes `abs_`/`abe_`/`abr_`/`abb_`. Nach 120 solchen Fehlversuchen je Stunde und Adresse (am
  Erneuern-Weg: 30) kommt
  `429` ohne `Retry-After`. Die Adresse folgt ab v1.53.0 der Regel der Agenten-Bremse: `X-Real-IP` nur von einer
  Gegenstelle aus `ABOARD_AGENT_PROXY`, sonst die TCP-Adresse, nie `X-Forwarded-For` (bis v1.51.0: erster Eintrag von
  `X-Forwarded-For`). Eine Anfrage ganz ohne
  Token beantwortet unter `/agent/` die Agenten-Prüfkette (`401`, mit deren Fehlversuch-Bremse).
- Eine Proxy-Identität (`Remote-Email`) wertet keiner der Skript-Wege aus.
- **Ausgang:** `500` ohne Teilliste, wenn das Board nicht lesbar ist oder mehr als 5000 Schatten entstünden („too many
  shadows … archive done cards“).
- **Eingang:** Seite, Token und Kopplung entscheiden, bevor der Body gelesen wird. Dann `422` bei jeder Abweichung vom
  Feldsatz (die Meldung nennt Stelle und Feld, nie den Wert; nichts geschrieben). Die Antwort nennt **alle**
  fehlerhaften Schatten: `{"error": "<erster Fehler> (und N weitere …)", "fehler": [{"index": 3, "grund": "titel: …"}, …]}`,
  `index` ist die Stelle in der Liste `schatten` (ab 0). Die Liste hat höchstens 50 Einträge; darüber zählt
  `"weitere": N` den Rest. `error` bleibt für ältere Skripte bestehen. Fehler der Hülle (kein JSON, `version` fehlt)
  haben nur `error`. Die Testvektoren der Titelbehandlung stehen in `server/testdata/schatten/t16-vektoren.json`
  (erzeugt von `erzeuge-t16.py` im selben Ordner). Titel, Spalte und
  jeder Bearbeiter müssen **Fixpunkt** derselben Behandlung sein: der Eingang säubert nie, er lehnt ab — auch bei zwei Leerzeichen
  hintereinander, Leerzeichen am Rand, einem Tabulator oder der 🤖-Marke im Titel (die Marke läuft über `vonAgent`).
  Im Merker der Admin-Anzeige („Abgleich-Stand“) und im Server-Log steht die Zahl der weiteren Fehler („… (und N
  weitere fehlerhafte Schatten)“), aber nicht der Verweis „siehe fehler“. In den Gründen stehen nie Werte (Titel,
  Spalte, Bearbeiter); bei fremden Objektschlüsseln steht der Schlüsselname, auf 40 Zeichen gekürzt. Weiter: `409` bei abweichender
  Hauptversion (nur wenn beide Seiten SemVer melden), `413` über 32 MiB, `429` mit `Retry-After: 5`, solange für
  dasselbe Board ein Lauf läuft, `503` mit `Retry-After: 5`, wenn die Datenbank belegt ist, `400`, wenn der Upload
  länger als 60 Sekunden dauert. Bei einer Änderung geht das Ereignis `schatten:aktualisiert` an den
  Ereignisstrom des Boards (Board, Anzahl, Zeitpunkt; kein Inhalt, kein `ETag`).

**Die Anzeige** (`/boards/{id}/schatten`) gibt es nur am Eingang für Menschen: Rolle nach der Grundregel (viewer),
Agenten-Tokens und Board-Paar-Tokens bekommen `403`. Ist die Seite, die Kopplung oder die Tabelle nicht lesbar,
antwortet sie mit `503`. Kennungen interner Karten und E-Mail-Adressen enthält sie nicht; die interne Board-ID steht nur im Menschen-Link `link` (`innen_link_basis`).
Auf einer **externen Kopie** trägt die Antwort zusätzlich `"kopie": true`, und jeder Eintrag nennt seinen Platz mit
`lane` und `spalte_id`. Dort zeigt ein vertraulicher Schatten seinen echten Titel (`titel_platzhalter: false`); der
Platzhalter „Interne Karte“ (`titel_platzhalter: true`) bleibt Schatten von Stufe-1-Boards und Einträgen ohne Titel.

## Health

| Pfad | Methode | Rolle | Bedeutung |
|---|---|---|---|
| `/healthz` | GET, HEAD | öffentlich | Zustand als JSON |

```bash
curl -s https://board.example.org/healthz
# {"ok":true,"db":"up","version":"…","commit":"…","build_date":"…"}
```

Ist die Datenbank nicht erreichbar, antwortet der Pfad mit `503` und `"ok": false, "db": "down"`.
`/healthz` ist auch bei eingeschalteter Anmeldung ohne Identität erreichbar und nennt Version,
Commit und Build-Datum. Wer das nicht nach außen zeigen will, sperrt den Pfad am Proxy für externe
Netze; der Container-eigene Health-Check braucht nur den lokalen Zugriff.

## Agenten-Pfade

Agenten sprechen den Server über einen von zwei Eingängen an:

- **`/agent/` am selben Port** (Voreinstellung an, abschaltbar mit `ABOARD_AGENT_PFAD=aus`):
  `https://board.example.org/agent/boards/{id}/ops` entspricht `POST /boards/{id}/ops`.
- **eigener Listener** über `ABOARD_AGENT_LISTEN` oder `ABOARD_AGENT_UNIX_SOCKET`: dort ohne Präfix.

Unter beiden gilt nur das Bearer-Token (`Authorization: Bearer abt_…`), kein Forward-Auth und kein
CSRF-Schutz. Die Zugänge des Board-Paars (`abs_…`, `abe_…`, `abr_…`) sind keine Agenten-Tokens; sie erreichen
nur ihren eigenen Weg (siehe [Board-Paar](#board-paar)). Pfade, die ein Proxy dem Agenten-Zweig zuordnen könnte, die beim Server aber nicht
genau mit `/agent/` beginnen (`/AGENT/x`, `//agent/x`, `/agent` ohne Schrägstrich), beantwortet der
Server mit `400`. Ist der Pfad-Eingang abgeschaltet, gilt das für jeden `/agent`-Pfad.

### Freigegebene Board-Pfade

Jeweils nur für das Board, an das das Token gebunden ist:

| Pfad | Methode | viewer | editor | vorschlag |
|---|---|---|---|---|
| `/boards/{eigenes}` | GET, HEAD | ✓ | ✓ | – |
| `/boards/{eigenes}/events` | GET | ✓ | ✓ | – |
| `/boards/{eigenes}/ich` | GET, HEAD | ✓ | ✓ | ✓ |
| `/boards/{eigenes}/ops` | POST | – | ✓ | ✓ (nur neue Karten) |
| `/boards/{eigenes}/attachments/{cardId}/{attId}`, `/board-attachments/{attId}` | GET, HEAD | ✓ | ✓ | – |
| `/boards/{eigenes}/attachments/{cardId}`, `/board-attachments` | POST | – | ✓ | – |
| `/boards/{eigenes}/attachments/{cardId}/{attId}`, `/board-attachments/{attId}` | DELETE | – | – | – |

### Werkzeug-Pfade

Die Werkzeuge mit OpenAPI-Beschreibung (für Open WebUI und andere Werkzeug-Clients) gibt es **nur**
am Agenten-Eingang; am Eingang für Menschen antworten sie mit `404`.

| Pfad | Methode | Werkzeug | Rolle |
|---|---|---|---|
| `/openapi.json` | GET | (Beschreibung) | jedes Token außer vorschlag |
| `/mcp` | POST (GET, DELETE → 405) | MCP-Eingang, dieselben Werkzeuge | jedes Token außer vorschlag |
| `/werkzeuge/boards` | GET | `boards` | viewer |
| `/werkzeuge/boards/{boardId}` | GET | `board_lesen` | viewer |
| `/werkzeuge/boards/{boardId}/karten/{karte}` | GET | `karte_zeigen` | viewer |
| `/werkzeuge/termine` | GET | `termine_lesen` | viewer |
| `/werkzeuge/boards/{boardId}/karten` | POST | `karte_anlegen` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/kommentare` | POST | `kommentieren` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/verschieben` | POST | `verschieben` | editor |
| `/werkzeuge/boards/{boardId}/findings` | POST | `finding_anlegen` | editor |
| `/werkzeuge/boards/{boardId}/termine` | POST | `termin_anlegen` | editor |
| `/werkzeuge/karten` | GET | `karten_suchen` | viewer |
| `/werkzeuge/boards/{boardId}/verlauf` | GET | `board_verlauf` | viewer |
| `/werkzeuge/boards/{boardId}/mitglieder` | GET | `mitglieder` | viewer |
| `/werkzeuge/boards/{boardId}/karten/{karte}/anhaenge` | GET | `anhang_lesen` | viewer |
| `/werkzeuge/boards/{boardId}/karten/{karte}/aendern` | POST | `karte_aendern` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/checkliste` | POST | `checkliste_abhaken` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/archivieren` | POST | `archivieren` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/wiederherstellen` | POST | `wiederherstellen` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/kommentare/{kommentar}` | POST | `kommentar_bearbeiten` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/verweise` | POST | `verweis_setzen` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/verweise/entfernen` | POST | `verweis_entfernen` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/uebernehmen` | POST | `uebernehmen` | editor |
| `/werkzeuge/boards/{boardId}/karten/{karte}/freigeben` | POST | `freigeben` | editor |
| `/werkzeuge/boards/{boardId}/termine/{termin}` | POST | `termin_aendern` | editor |
| `/werkzeuge/boards/{boardId}/swimlanes` | POST | `swimlane_anlegen` | editor |
| `/werkzeuge/boards/{boardId}/swimlanes/{swimlane}` | POST | `swimlane_aendern` | editor |
| `/werkzeuge/boards/{boardId}/warten` | POST | `auf_antwort_warten` | editor, nur Personen-Tokens |
| `/werkzeuge/boards/{boardId}/karten/{karte}/loeschen` | GET, POST | `karte_loeschen_pruefen`, `karte_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/swimlanes/{swimlane}/loeschen` | GET, POST | `swimlane_loeschen_pruefen`, `swimlane_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/termine/{termin}/loeschen` | GET, POST | `termin_loeschen_pruefen`, `termin_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/anhaenge/{anhang}/loeschen` | GET, POST | `anhang_loeschen_pruefen`, `anhang_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/spalten/{spalte}/loeschen` | GET, POST | `spalte_loeschen_pruefen`, `spalte_loeschen` | editor, Person ist owner |
| `/werkzeuge/boards/{boardId}/aendern` | POST | `board_aendern` | editor, Person ist owner |

Ab `karten_suchen` sind die Werkzeuge für ein delegierendes Token (Open WebUI) nicht freigegeben;
`kommentar_bearbeiten`, `uebernehmen`, `freigeben` und `board_aendern` können es nie
sein. Gelöscht wird nur zweistufig: `GET` liefert Vorschau und Bestätigungscode, `POST {code}` löscht.

Ein **delegierendes Token** (Open WebUI) erreicht ausschließlich diese Werkzeug-Pfade, keinen der
Board-Pfade darüber, ebenso ein **Personen-Token für mehrere Boards** (das Board aus dem Pfad muss in
seiner Liste stehen und die Person dort einen eigenen Eintrag haben). Ein **Vorschlags-Token** erreicht
keinen Werkzeug-Pfad. Zu den Token-Arten siehe
[Server verwalten](administration.md).

### Gesperrt für Agenten

Ganz-Board-`PUT`, die Board-Liste, Protokoll, Freigaben, Mitglieder, Transfer, KI, Mail, Sicherungen,
`/me`, `/users/me/*`, `/known-users`, `/clients`, `/projects` und `/admin/*`. Ein gültiges Token auf
einem gesperrten Pfad ergibt `403`, ein fehlendes oder ungültiges `401` — auch für Pfade, die es gar
nicht gibt, damit ein Unbekannter nichts über die Routen erfährt.
