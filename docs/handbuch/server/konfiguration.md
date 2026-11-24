# Konfiguration: alle Umgebungsvariablen

[English](../../handbook/server/configuration.md) · **Deutsch**

Der aBoard-Server wird ausschließlich über **Umgebungsvariablen** eingestellt. Es gibt keine
Konfigurationsdatei. Dieses Kapitel listet **jede** Variable, die der Server liest, mit ihrer
Voreinstellung und ihrer Bedeutung. Wie man die Variablen an den Container übergibt
(`docker run -e …`, `environment:` in Compose, `.env`-Datei), steht in
[Container](container.md).


> **Was geprüft wird und was nicht.** Voreinstellungen und Beschreibungen sind beim Schreiben am
> Code geprüft worden, werden aber nicht automatisch überwacht; im Zweifel gilt der Code.

## Grundregeln

- **Gelesen wird beim Start.** Nach einer Änderung den Container neu starten.
- **Leer heißt nicht gesetzt.** Eine Variable mit leerem Wert verhält sich wie eine fehlende:
  Es gilt die Voreinstellung.
- **Drei Arten von Werten:**
  - **Wort-Schalter** (`an`/`aus`, bei einigen auch weitere Wörter): Ein unbekannter Wert hält
    den Start an. Der Server rät nicht, was gemeint war.
  - **Ein/Aus-Schalter** (`ABOARD_TRUST_FORWARD_AUTH`, `ABOARD_FORWARD_AUTH_INSECURE_BIND`,
    `ABOARD_FORWARD_AUTH_VERBOSE`): leer, `0`, `false`, `no` und `off` bedeuten **aus**, jeder
    andere Wert **an**. Üblich ist `1`.
  - **Werte** (Pfade, Adressen, Zahlen, Listen): Ungültige Werte halten je nach Variable den
    Start an oder fallen mit einer Warnung im Log auf die Voreinstellung zurück. Die Tabellen
    sagen, was gilt.
- **Listen** werden mit Komma getrennt, Leerzeichen um die Einträge herum sind egal.

## Geheimnisse

Einige Variablen tragen Geheimnisse. Für sie gilt:

- **Zwei Variablen nehmen einen Dateipfad statt des Werts:** `ABOARD_LLM_SERVERSCHLUESSEL_FILE`
  und `ABOARD_OWUI_JWT_SECRET_FILE`. Der Server liest das Geheimnis aus der Datei. So steht es in
  keiner Prozessliste und in keiner Container-Beschreibung. Ist die Datei für andere Konten
  lesbar (offener als `0640`), warnt der Server beim Start. Empfohlen: `chmod 600`.
- **Eine allgemeine `…_FILE`-Variante gibt es nicht.** Die übrigen Geheimnisse
  (`ABOARD_SECRET_KEY`, `ABOARD_SECRET_KEY_PREVIOUS`, `ABOARD_FORWARD_AUTH_SECRET`, `SMTP_PASS`,
  `IMAP_PASS`) werden als Wert übergeben. Sie gehören in eine `.env`-Datei mit `chmod 600` neben
  der Compose-Datei, nie in ein Image, nie in ein Repository und nie in die Datenbank.
- **Schlüssel erzeugen:** `openssl rand -hex 32` liefert einen passenden Wert für
  `ABOARD_SECRET_KEY` und für `ABOARD_FORWARD_AUTH_SECRET`.
- Fehlermeldungen und Log-Zeilen des Servers nennen nie den Inhalt eines Geheimnisses, höchstens
  den Namen der Variable oder den Pfad der Datei.

## Variablen des Servers

### Grundbetrieb: Adresse, Datenbank, Dateien

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_ADDR` | `:8080` | Adresse, auf der der Server lauscht, im Format `[host]:port`. `:8080` heißt: alle Netzwerkschnittstellen. `127.0.0.1:8080` lauscht nur lokal. |
| `ABOARD_UNIX_SOCKET` | leer | Pfad zu einem Unix-Socket, z. B. `/run/aboard/aboard.sock`. Ist er gesetzt, lauscht der Server dort statt auf `ABOARD_ADDR`. |
| `ABOARD_UNIX_SOCKET_MODE` | `0660` | Dateirechte der Unix-Sockets (auch des Agenten-Sockets). Erlaubt sind nur `0600`, `0660` und `0666`, auch ohne führende Null; jeder andere Wert hält den Start an. `0666` braucht, wer Proxy und Server in getrennten Benutzer-Namensräumen betreibt; dann begrenzt das Verzeichnis des Sockets den Zugriff. |
| `ABOARD_DB` | `/data/db/aboard.db` | Pfad der SQLite-Datenbank. Daneben legt SQLite `-wal`- und `-shm`-Dateien an; das Verzeichnis muss beschreibbar sein. |
| `ABOARD_ATTACHMENTS_DIR` | `/data/attachments` | Verzeichnis für Dateianhänge an Karten und Kommentaren. |
| `ABOARD_ATTACHMENT_MAX_BYTES` | `52428800` (50 MB) | Höchstgröße je Anhang in Byte. Ein ungültiger Wert (keine positive ganze Zahl) fällt still auf die Voreinstellung zurück. |
| `ABOARD_PUBLIC_BASE_URL` | leer | Öffentliche Adresse des Boards, z. B. `https://board.example.org`. Daraus baut der Server Links in Benachrichtigungs-Mails und in Antworten an Agenten-Werkzeuge. Leer: keine Links. Nur `http`/`https`, ohne Zugangsdaten, Query oder Fragment; ein ungültiger Wert hält den Start nicht an, schaltet aber die Links ab und steht als Warnung im Log. **Mit eingeschalteter eingebauter Anmeldung (`ABOARD_AUTH_MODE=builtin` oder `ABOARD_ANMELDUNG=oidc`) ist sie Pflicht, muss `https://` sein und darf keinen Pfad tragen** (daraus entsteht die Rückruf-Adresse `<basis>/anmeldung/rueckruf` bzw. `<basis>/anmeldung/rueckruf/<kürzel>`, die der Server an der Wurzel bedient); sonst bricht der Start ab. |
| `ABOARD_SCHEMA_NEUER_ZULASSEN` | leer | Notschalter: Mit `1` startet der Server auch auf einer Datenbank, die eine neuere Programmversion bereits umgebaut hat (mit Warnung). Ohne ihn bricht der Start in diesem Fall ab. Gedacht nur für ein Zurückgehen auf eine ältere Version ohne passendes Backup, siehe [Container](container.md). |
| `ABOARD_UHR_PRUEFUNG` | `an` | Fristgebundenes Löschen (Protokolle, Verläufe, Backup-Rotation) und das Übernehmen von Karten durch Agenten laufen nur, wenn das Betriebssystem die Uhr als synchronisiert meldet. `aus` schaltet diese Prüfung ab, nur für Systeme ohne Zeitdienst mit trotzdem richtiger Uhr. Andere Werte halten den Start an. |

### Aufbewahrungsfristen

Alle Fristen sind ganze Zahlen in Tagen von 1 bis 36500. Ein ungültiger Wert fällt mit einer
Warnung im Log auf die Voreinstellung zurück. Gelöscht wird nur bei synchronisierter Uhr
(`ABOARD_UHR_PRUEFUNG`).

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_ACTIVITY_RETENTION_DAYS` | `365` | Aufbewahrungsfrist des Aktivitäten-Protokolls je Board. |
| `ABOARD_ACTIVITY_MAX_PER_BOARD` | `100000` | Obergrenze der Änderungs-Einträge im Aktivitäten-Protokoll je Board, zusätzlich zur Frist; darüber werden die ältesten gelöscht. Greift vor allem bei Agenten, die viele Änderungen schreiben. Erlaubt 1 bis 10000000. Änderungen an Freigaben zählen nicht mit und werden nie gekappt. |
| `ABOARD_LLM_RETENTION_DAYS` | `365` | Aufbewahrungsfrist gespeicherter KI-Chatverläufe, gemessen an der jüngsten Nachricht eines Verlaufs. |
| `ABOARD_AUDIT_RETENTION_DAYS` | `730` | Aufbewahrungsfrist des Prüfprotokolls (Konfigurationsänderungen). Einträge zu Admin-Rechten, Agenten-Zugängen, gelöschten Nutzern und zur Ausgangsschleuse bleiben unbefristet. |

### Anmeldung, Forward-Auth und Mehrnutzerbetrieb

Ohne `ABOARD_TRUST_FORWARD_AUTH` läuft der Server im Einzelbetrieb ohne Anmeldung: Jeder, der
ihn erreicht, sieht alle Boards. Den Mehrnutzerbetrieb hinter einem Anmelde-Proxy erklärt
[Anmeldung](anmeldung.md).

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_TRUST_FORWARD_AUTH` | aus | Ein/Aus-Schalter. An: Der Server übernimmt die Identität aus den Kopfzeilen des vorgeschalteten Anmelde-Proxys (`Remote-Email`, `Remote-User`, `Remote-Name`, `Remote-Groups`), wertet Freigaben aus und weist Anfragen ohne Identität mit `401` ab. Setzt `ABOARD_FORWARD_AUTH_SECRET` voraus. |
| `ABOARD_FORWARD_AUTH_SECRET` | leer | **Geheimnis.** Pflicht, sobald `ABOARD_TRUST_FORWARD_AUTH` an ist, sonst startet der Server nicht. Der Proxy schickt denselben Wert in der Kopfzeile `X-Aboard-Forward-Auth-Secret`; passt er nicht, antwortet der Server mit `401`. |
| `ABOARD_FORWARD_AUTH_INSECURE_BIND` | aus | Ein/Aus-Schalter. Bei aktivem Forward-Auth verweigert der Server den Start, wenn er auf einer von außen erreichbaren TCP-Adresse lauscht (statt nur lokal oder auf einem Unix-Socket). An: Er startet trotzdem, mit lauter Warnung. Nur setzen, wenn das Netz auf andere Weise abgeschottet ist. |
| `ABOARD_FORWARD_AUTH_VERBOSE` | aus | Ein/Aus-Schalter. An: eine Log-Zeile je Anfrage (angenommen oder abgewiesen) statt höchstens einer Warnzeile je Minute. Zur Fehlersuche. |
| `ABOARD_AUTH_MODE` | leer | Schalter der eingebauten Anmeldung, englischer Name von `ABOARD_ANMELDUNG`. `builtin` schaltet sie ein; `oidc` bedeutet dasselbe; leer = aus (Einzelnutzer-Betrieb oder Forward-Auth). Beide Namen gleichzeitig sind erlaubt (beide Werte bedeuten dasselbe); ist ein Wert ungültig, bricht der Start ab. Alles Weitere wie bei `ABOARD_ANMELDUNG`. Beispiel: `builtin`. |
| `ABOARD_ACCOUNT_SELF_PASSWORD` | aus | Ein/Aus-Schalter. An: Personen ohne Passwort (nur Anbieter-Anmeldung oder Vorab-Liste) können sich in den Konto-Einstellungen nach einer frischen Anmeldung bei ihrem Anbieter selbst ein Passwort mit Pflicht-2FA anlegen; aus: Abschnitt fehlt, die Endpunkte antworten `404`. Werte nach der Ein/Aus-Konvention, aber streng: an = `1`, `true`, `yes`, `on` (auch `ja`, `an`); aus = leer, `0`, `false`, `no`, `off`; jeder andere Wert (etwa `aus`, `nein`, ein Tippfehler) hält den Start an. Der Start bricht auch ab, wenn der Schalter an ist und `ABOARD_AUTH_METHODS` den Weg `password` nicht anbietet, oder wenn er ohne eingeschaltete eingebaute Anmeldung an ist. ⚠️ Wer auf der Vorab-Liste steht und sich ein Passwort anlegt, hat danach ein aktives Konto: Streichen von der Liste entzieht den Zugang dann nicht mehr, dafür gibt es „Sperren“ ([Anmeldung](anmeldung.md#bindung-an-ein-anbieter-konto-lösen)). Beispiel: `1`. |
| `ABOARD_OIDC_REAUTH_FALLBACK` | aus | Ein/Aus-Schalter. Wer kein Passwort hat, bestätigt Änderungen in den Konto-Einstellungen durch eine frische Anmeldung beim Anbieter. aBoard verlangt dafür den Claim `auth_time`. **Google liefert ihn nur bei veröffentlichter und verifizierter App** mit eingeschalteten „Session age claims“; sonst scheitert die Bestätigung immer (Audit `ergebnis=auth_time_fehlt`, Meldung „Dein Anmeldedienst bestätigt keine frische Anmeldung“). An: Fehlt `auth_time`, genügt ein ID-Token, das der Anbieter in genau diesem Vorgang ausgestellt hat (`iat` nicht vor dessen Beginn, mit 1 Minute Uhrenspiel, höchstens 5 Minuten alt; Audit `ok_ohne_auth_time`, sonst `iat_alt`). Liefert der Anbieter `auth_time`, gilt immer die strenge Prüfung. ⚠️ Folge: Ohne `auth_time` beweist die Bestätigung nur, dass am Browser gerade jemand Zugang zum Anbieter-Konto hat. Gegen ein gestohlenes aBoard-Cookie schützt das weiter, gegen jemanden am offenen, bei Google angemeldeten Rechner nicht. Werte streng wie bei `ABOARD_ACCOUNT_SELF_PASSWORD`; ein Tippfehler hält den Start an. ⚠️ **Erst nach dem Update eintragen:** Server bis v1.61.0 kennen die Variable nicht und brechen beim Start ab (jede unbekannte `ABOARD_OIDC_*`-Variable hält den Start an). Beispiel: `1`. |
| `ABOARD_AUTH_METHODS` | leer | Welche Anmeldewege die Seite `/anmeldung` anbietet. Kommaliste (auch Semikolon oder Leerraum) aus `password` (eigene Konten, immer eingerichtet) und den Kürzeln der eingerichteten OIDC-Anbieter: `standard` für den Anbieter aus `ABOARD_OIDC_ISSUER`/`_CLIENT_ID`/`_CLIENT_SECRET`, sonst die Kürzel aus `ABOARD_OIDC_PROVIDERS`. **Leer = alle eingerichteten Wege**, also auch das Passwort-Formular. Ein nicht genannter Anbieter ist ganz abgeschaltet: Sein Start und sein Rückruf antworten mit `404` „Unbekannter Anmeldedienst“; ohne `password` antwortet `POST /anmeldung/passwort` mit `404`. Der Start bricht ab bei einem Wert, der kein eingerichteter Weg ist (Tippfehler), bei doppelten Werten und bei einer Liste ohne jeden Weg. Bleibt genau ein Weg übrig und ist es ein Anbieter, leitet `/anmeldung` direkt zu ihm weiter; wer das will, setzt `ABOARD_AUTH_METHODS=<kürzel>`. Wirkt nur mit eingeschalteter Anmeldung; ohne sie hält die Variable den Start an. Ein Tippfehler im Namen (`ABOARD_AUTH_METHOD`) hält den Start ebenfalls an. Beispiel: `google,authelia`. |
| `ABOARD_INVITE_METHODS` | leer | Welche Wege eine eingeladene Person beim Einlösen des Links wählen kann. Leer = alle Wege, die `ABOARD_AUTH_METHODS` anbietet. Sonst eine Kommaliste aus diesen Wegen, z. B. `password` (nur eigene Konten) oder `google`. Ein Weg, den `ABOARD_AUTH_METHODS` nicht anbietet, ein doppelter Wert oder eine leere Liste halten den Start an. Wirkt nur mit eingeschalteter Anmeldung (sonst hält die Variable den Start an), und nur beim Einlösen; ob jemand danach hereinkommt, entscheidet die Bestätigung durch einen Admin ([Server verwalten](administration.md#konten-und-einladungen-eingebaute-anmeldung)). Der Einladungslink gilt 7 Tage und lässt sich einmal einlösen; ob er zusätzlich per Mail geht, hängt nur am eingerichteten Mailserver (SMTP), nicht an dieser Variable. Beispiel: `password,google`. |
| `ABOARD_ANMELDUNG` | leer | Bisheriger Name des Schalters der eingebauten Anmeldung, bleibt gültig und bedeutet dasselbe wie `ABOARD_AUTH_MODE=builtin`. `oidc` schaltet die eingebaute Anmeldung ein (OpenID-Connect-Anbieter und/oder eigene Konten mit Passwort), statt Forward-Auth; leer = aus. Andere Werte als leer, `oidc` und `builtin` halten den Start an, ebenso `oidc` zusammen mit `ABOARD_TRUST_FORWARD_AUTH` oder `ABOARD_DEV_FAKE_USER`/`_EMAIL`. Verlangt `ABOARD_PUBLIC_BASE_URL` (https) und `ABOARD_SECRET_KEY`. Ein OIDC-Anbieter ist nicht mehr Pflicht: Ohne Anbieter bietet der Server nur eigene Konten mit Passwort an (Weg `password`, siehe `ABOARD_AUTH_METHODS`); ein halb eingerichteter Anbieter hält den Start weiterhin an. Umgekehrt hält jede gesetzte `ABOARD_OIDC_*`-, `ABOARD_AUTH_*`- oder `ABOARD_INVITE_*`-Variable ohne eingeschalteten Schalter den Start an — sonst liefe der Server ohne Anmeldung. Ablauf, Zulassung und Fehlersuche: [Anmeldung](anmeldung.md#eingebaute-anmeldung-per-openid-connect). Beispiel: `oidc`. |
| `ABOARD_OIDC_ISSUER` | leer | Adresse (https) des Anbieters `standard`, z. B. `https://accounts.google.com`. Die drei Variablen `ABOARD_OIDC_ISSUER`, `_CLIENT_ID` und `_CLIENT_SECRET` bilden zusammen den Anbieter `standard` (der bisherige Einzel-Anbieter); sein Rückruf bleibt `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf`. Pflicht (alle drei zusammen) nur, wenn kein Anbieter über `ABOARD_OIDC_PROVIDERS` eingerichtet ist; ist eine der drei gesetzt, müssen alle drei da sein, sonst bricht der Start ab. Bei `ABOARD_AUSGAENGE=aus` müssen sein Host und die Hosts aus seinem Discovery-Dokument (`<issuer>/.well-known/openid-configuration`: `token_endpoint`, `jwks_uri`) in `ABOARD_AUSGAENGE_ERLAUBT` stehen. |
| `ABOARD_OIDC_CLIENT_ID` | leer | Client-ID des beim Anbieter angelegten OIDC-Clients, Teil des Anbieters `standard` (siehe `ABOARD_OIDC_ISSUER`). Rückruf-Adresse dort: `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf`, zeichengenau. Beispiel: `1234-abc.apps.googleusercontent.com`. |
| `ABOARD_OIDC_CLIENT_SECRET` | leer | **Geheimnis.** Client-Secret dieses Clients, Teil des Anbieters `standard` (siehe `ABOARD_OIDC_ISSUER`). Nicht ins Git, nicht in den Log. Beispiel: `<client-secret>`. |
| `ABOARD_OIDC_ZUGELASSEN` | leer | Bisheriger Name von `ABOARD_OIDC_ALLOWED`, bleibt gültig und bedeutet dasselbe: die Vorab-Liste — Adressen und `@domain`, getrennt durch Komma, Semikolon oder Leerraum. Dazu immer `ABOARD_BOOTSTRAP_OWNER`. Wer auf dieser Liste steht, kommt herein, solange sein Konto nicht gesperrt ist. Daneben kommt herein, wer ein aktives Konto hat (Einladung, die ein Admin bestätigt hat, siehe [Server verwalten](administration.md#konten-und-einladungen-eingebaute-anmeldung)); neue Betriebe brauchen die Liste deshalb nicht. Der Status des OAuth-Clients beim Anbieter (bei Google „Testing“) schützt nicht. Zugang entziehen heißt Adresse streichen und neu starten. Eine Freigabe oder eine Domain-Freigabe lassen **nicht** herein. Ein ungültiger Eintrag hält den Start an; leer und ohne Bootstrap-Owner (und ohne aktives Konto) startet der Server mit Warnung, und niemand kommt herein. Änderungen wirken nach dem Neustart, auch für schon angemeldete Personen. Beispiel: `anna@example.org, @example.net`. |
| `ABOARD_OIDC_ALLOWED` | leer | Englischer Name von `ABOARD_OIDC_ZUGELASSEN`, gleiche Bedeutung. Steht der eine oder der andere Name da, gilt er; stehen beide mit verschiedenem Inhalt da, bricht der Start ab. Beispiel: `anna@example.org, @example.net`. |
| `ABOARD_OIDC_PROVIDERS` | leer | Kürzel weiterer OIDC-Anbieter, durch Komma (auch Semikolon oder Leerraum) getrennt, z. B. `google,authelia`. Je Kürzel gelten die vier folgenden Variablen; `<ID>` ist das Kürzel in Großbuchstaben, `-` wird `_` (`heim-2` → `ABOARD_OIDC_HEIM_2_…`). Kürzel: 1–32 Zeichen `a–z`, `0–9`, `-`; `standard` und `password` sind reserviert; ein Kürzel darf nur einmal stehen. Rückruf-Adresse beim Anbieter: `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<kürzel>`. Ein gelistetes Kürzel ohne eigene Variablen wird nicht angeboten (Warnung im Log); halb eingerichtet hält es den Start an, ebenso Variablen eines nicht gelisteten Kürzels und unbekannte `ABOARD_OIDC_*`-Namen (Tippfehler). Beispiel: `google,authelia`. |
| `ABOARD_OIDC_<ID>_ISSUER` | leer; bei `google` `https://accounts.google.com` | Adresse (https) des Anbieters mit dem Kürzel `<ID>`. Pflicht, außer bei `google`. Beispiel: `https://auth.example.org`. |
| `ABOARD_OIDC_<ID>_CLIENT_ID` | leer | Client-ID des beim Anbieter angelegten OIDC-Clients; Pflicht, sobald eine Variable dieses Kürzels gesetzt ist. Beispiel: `aboard`. |
| `ABOARD_OIDC_<ID>_CLIENT_SECRET` | leer | **Geheimnis.** Client-Secret dieses Clients; Pflicht, sobald eine Variable dieses Kürzels gesetzt ist. Beispiel: `<client-secret>`. |
| `ABOARD_OIDC_<ID>_LABEL` | `Google` bei Google, sonst der Host des Issuers | Beschriftung des Knopfs „Mit <LABEL> anmelden“ auf der Anmeldeseite. Beispiel: `Firmen-Konto`. |
| `ABOARD_BOOTSTRAP_OWNER` | leer | E-Mail-Adresse der ersten verantwortlichen Person, z. B. `person@example.org`. **Standard-Board (`default`):** wirkt nur beim ersten Start mit leerer Datenbank, dann wird sie dessen Eigentümerin; auf einer bestehenden Datenbank ohne Wirkung (Übernahme bestehender Boards siehe [Anmeldung](anmeldung.md#umstieg-vom-einzelnutzer--auf-den-mehrnutzer-betrieb)). Ein Wert, der keine Personenadresse ist (ohne `@`, oder `@firma.de`), wird an beiden Boards mit Fehlermeldung im Log übergangen. **Service-Board für eingehende Mails:** Ist es eingeschaltet, setzt der Server die Adresse bei **jedem** Start erneut als dessen Eigentümerin, solange die Variable gesetzt ist. Nach der Einrichtung deshalb entfernen — erst, wenn es einen Administrator gibt. **Administrator:** Solange es keinen gibt, wird nur diese Person Administrator (bei ihrer ersten Anmeldung, oder beim nächsten Start, wenn sie schon ein Konto hat); ist der Wert ungültig, niemand — siehe [Anmeldung](anmeldung.md#der-erste-administrator). |
| `ABOARD_ACL_DOMAINS` | leer (keine) | Mail-Domains, für die Board-Eigentümer eine Freigabe „alle mit @domain“ setzen dürfen, z. B. `example.org,example.net`. Höchstens Leserecht oder Schreibrecht, nie Eigentümerschaft. Nur Domains eintragen, deren Adressen der Anmeldedienst wirklich prüft, nie Freemail-Domains. |
| `ABOARD_DEV_FAKE_USER` | leer | **Nur für die Entwicklung, nie im Betrieb.** Täuscht die Kopfzeile `Remote-User` vor, damit sich der Mehrnutzerbetrieb ohne Proxy ausprobieren lässt. Wirkt nur zusammen mit `ABOARD_TRUST_FORWARD_AUTH`; ist sie gesetzt, warnt der Start deutlich. |
| `ABOARD_DEV_FAKE_EMAIL` | leer | **Nur Entwicklung.** Täuscht `Remote-Email` vor (wirkt nur, wenn `ABOARD_DEV_FAKE_USER` gesetzt ist). |
| `ABOARD_DEV_FAKE_GROUPS` | leer | **Nur Entwicklung.** Täuscht `Remote-Groups` vor (wirkt nur, wenn `ABOARD_DEV_FAKE_USER` gesetzt ist). |
| `ABOARD_DEV_FAKE_NAME` | leer | **Nur Entwicklung.** Täuscht `Remote-Name` vor, den Anzeigenamen (wirkt nur, wenn `ABOARD_DEV_FAKE_USER` gesetzt ist). |

### Agenten und Open WebUI

Agenten (Skripte, KI-Assistenten) melden sich mit einem Zugangstoken (`abt_…`) an, nicht über
den Anmelde-Proxy. Wie man Zugänge anlegt, steht in [Administration](administration.md); den Proxy
beschreibt [Anmeldung](anmeldung.md).

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_AGENT_PFAD` | `an` | Agenten-Eingang unter dem Pfad `/agent/` am normalen Port (nur Tokens, keine Browser-Anmeldung). `aus` schaltet ihn ab; `/agent/…` antwortet dann mit `400`. Andere Werte halten den Start an. Öffentlich erreichbar wird der Pfad erst, wenn der Proxy ihn an der Anmeldung vorbeileitet. |
| `ABOARD_AGENT_LISTEN` | leer (kein eigener Eingang) | TCP-Adresse eines **zusätzlichen**, getrennten Agenten-Eingangs, z. B. `127.0.0.1:8081`. Schließt `ABOARD_AGENT_UNIX_SOCKET` aus und darf nicht dieselbe Adresse wie `ABOARD_ADDR` sein. Lauscht er nicht nur lokal, warnt der Start: Tokens gehen dann unverschlüsselt übers Netz, sofern kein TLS-Proxy davorsteht. |
| `ABOARD_AGENT_UNIX_SOCKET` | leer | Unix-Socket für den getrennten Agenten-Eingang, z. B. `/run/aboard/agent.sock`; Rechte nach `ABOARD_UNIX_SOCKET_MODE`. Alternative zu `ABOARD_AGENT_LISTEN`. |
| `ABOARD_AGENT_PROXY` | leer | Vertrauenswürdige Gegenstellen für die Sperre nach Fehlversuchen (zehn falsche Tokens in zehn Minuten sperren für zehn Minuten; am Passwort-Formular der eingebauten Anmeldung fünf falsche Eingaben in 15 Minuten); ab v1.53.0 auch für die Drossel der Board-Paar-Wege (`abs_`/`abe_`/`abr_`: Schattenwege 120 Fehlversuche je Stunde, Erneuern 30 je Stunde). Kommaliste aus Netzen (`198.51.100.0/24`), einzelnen IP-Adressen, DNS-Namen (z. B. `proxy`, jede Minute neu aufgelöst) und dem Wort `unix`. Nur von diesen Gegenstellen wird die Kopfzeile `X-Real-IP` als Client-Adresse übernommen. **Der Proxy muss `X-Real-IP` mit der echten Client-Adresse überschreiben** (Caddy: `header_up X-Real-IP {client_ip}` im `reverse_proxy`-Block — `{client_ip}` ist die TCP-Gegenstelle, steht vor Caddy ein weiterer Eingang, der in `trusted_proxies` eingetragen ist, die Adresse aus dessen `X-Forwarded-For`; nginx: `proxy_set_header X-Real-IP $remote_addr;`). Caddy setzt die Kopfzeile von sich aus nicht und reicht eine vom Client mitgeschickte unverändert durch; dann wählt der Angreifer seine Adresse selbst und die Sperre greift nicht. Leer hinter einem Proxy: Alle Clients teilen sich die Adresse des Proxys, der Start warnt. Ein ungültiges Netz hält den Start an. |
| `ABOARD_OWUI_URL` | leer | Adresse einer Open-WebUI-Instanz, z. B. `https://chat.example.org`. Gesetzt: Das Karten-Fenster zeigt einen Knopf „In Open WebUI weiterbearbeiten“. Nur `http`/`https`, ohne Zugangsdaten, Query oder Fragment; ein ungültiger Wert hält den Start an. |
| `ABOARD_OWUI_JWT_SECRET_FILE` | leer | **Geheimnis als Datei.** Pfad zur Datei mit dem gemeinsamen Geheimnis von Open WebUI und aBoard (mindestens 32 Zeichen). Gesetzt: Open WebUI darf mit einem delegierenden Token im Namen der angemeldeten Person handeln. Leer: Solche Tokens werden mit `401` abgelehnt. Unlesbare Datei oder zu kurzes Geheimnis halten den Start an. |
| `ABOARD_OWUI_JWT_MAX_AGE_SECONDS` | `300` | Höchstalter der Personen-Bestätigung, die Open WebUI je Chat-Anfrage mitschickt, in Sekunden (60 bis 3600). Für lange Antworten in Open WebUI denselben Wert einstellen. Wird nur gelesen, wenn `ABOARD_OWUI_JWT_SECRET_FILE` gesetzt ist; ein ungültiger Wert hält dann den Start an. |

### Mail

Für Mail gibt es **keine** `ABOARD_`-Variablen. Ausgehende Mails (Benachrichtigungen) und der
Mail-Eingang (Mails werden zu Karten auf einem Service-Board) werden im Admin-Bereich
eingestellt, siehe [Administration](administration.md). Die folgenden Variablen sind nur
**Startwerte**: Beim Start übernimmt der Server sie in die Datenbank, aber nur, wenn dort noch
kein Wert steht. Danach gilt, was im Admin-Bereich eingestellt ist; eine geänderte Variable
wirkt dann nicht mehr.

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `SMTP_HOST` | leer | Mailserver für ausgehende Mails. |
| `SMTP_PORT` | `587` | Port des Mailservers. |
| `SMTP_USER` | leer | Benutzername am Mailserver. |
| `SMTP_PASS` | leer | **Geheimnis.** Passwort am Mailserver. Wird nur übernommen, wenn `ABOARD_SECRET_KEY` gesetzt ist, sonst mit Warnung übergangen. |
| `SMTP_FROM` | leer | Absenderadresse. |
| `SMTP_FROM_NAME` | leer | Absendername. |
| `SMTP_STARTTLS` | `true` | Verschlüsselung per STARTTLS; `false` schaltet sie ab. |
| `IMAP_ENABLED` | `false` | `true` schaltet den Mail-Eingang ein. |
| `IMAP_HOST` | leer | Postfach-Server für den Mail-Eingang. |
| `IMAP_PORT` | `993` | Port des Postfach-Servers. |
| `IMAP_USER` | leer | Benutzername des Postfachs. |
| `IMAP_PASS` | leer | **Geheimnis.** Passwort des Postfachs; wie `SMTP_PASS` nur mit `ABOARD_SECRET_KEY`. |
| `IMAP_TLS` | `true` | TLS-Verbindung zum Postfach-Server. |
| `IMAP_FOLDER` | `INBOX` | Ordner, aus dem neue Mails gelesen werden. |
| `IMAP_PROCESSED_FOLDER` | `Processed` | Ordner für verarbeitete Mails. |
| `IMAP_ERROR_FOLDER` | `Errors` | Ordner für Mails, die nicht verarbeitet werden konnten. |
| `IMAP_POLL_INTERVAL_SEC` | `600` | Abstand zwischen zwei Abrufen in Sekunden (mindestens 60). |
| `IMAP_MAX_MAIL_SIZE_MB` | `25` | Größere Mails landen im Fehler-Ordner. |
| `IMAP_MAX_ATTACHMENT_SIZE_MB` | `10` | Größere Anhänge werden nicht übernommen; die Karte bekommt einen Hinweis. |
| `IMAP_RATE_LIMIT_PER_HOUR` | `20` | Höchstzahl Mails je Absender und Stunde; weitere landen in der Spam-Spalte. `0` schaltet die Grenze ab. |
| `SERVICE_BOARD_ENABLED` | `true` | Legt das Service-Board für eingehende Mails an. |
| `SERVICE_BOARD_TITLE` | `Service` | Titel des Service-Boards beim Anlegen. |
| `MAIL_SPAM_DROP` | `false` | `true`: Als Spam erkannte Mails werden verworfen statt in die Spam-Spalte gelegt. |

### KI-Assistent (LLM)

Ohne diese Variablen bringt jede Person ihren eigenen Schlüssel für einen KI-Anbieter mit
(gespeichert verschlüsselt, setzt `ABOARD_SECRET_KEY` voraus). Mit
`ABOARD_LLM_SERVERSCHLUESSEL_FILE` gilt der **Firmenmodus**: ein Schlüssel des Betreibers für
alle, nur freigegebene Modelle. Einzelheiten in [Administration](administration.md).

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_LLM_OPENAI_BASE_URL` | leer (`https://api.openai.com/v1`) | Adresse eines OpenAI-kompatiblen Anbieters, z. B. eines eigenen Modellservers im Firmennetz: `http://llm.example.org:8000/v1`, mit Versionspfad. Nur `http`/`https`, Port 1 bis 65535, ohne Zugangsdaten, Query oder Fragment; ein ungültiger Wert hält den Start an. Ohne Pfad warnt der Start. |
| `ABOARD_LLM_OPENAI_MODELLE` | leer | Kommaliste der Modellnamen, die der Anbieter bedient; die Oberfläche bietet genau diese an. Im Firmenmodus Pflicht und zugleich die Liste der erlaubten Modelle. Leer bei gesetzter `ABOARD_LLM_OPENAI_BASE_URL`: Warnung im Start-Log. Namen mit Leerraum halten den Start an. |
| `ABOARD_LLM_OPENAI_KONTEXT` | leer (keine Grenze) | Kontextgröße des eigenen Modells in Token (512 bis 10000000). Gesetzt: Der Server weist zu lange Verläufe vor dem Senden ab, statt den Fehler des Modells abzuwarten. Nur zusammen mit `ABOARD_LLM_OPENAI_BASE_URL`, sonst Startabbruch. |
| `ABOARD_LLM_OPENAI_WERKZEUGE` | `an` | `aus` schickt dem OpenAI-kompatiblen Anbieter keine Werkzeugbeschreibungen mit, für Modelle, die keine Werkzeuge beherrschen. Dann macht das Modell keine Änderungsvorschläge und lädt keine verknüpften Karten nach. Andere Werte halten den Start an. |
| `ABOARD_LLM_SERVERSCHLUESSEL_FILE` | leer | **Geheimnis als Datei.** Pfad zur Datei mit genau einem Anbieter-Schlüssel des Betreibers. Gesetzt schaltet den Firmenmodus ein: nur der Anbieter „openai“ (mit `ABOARD_LLM_OPENAI_BASE_URL`, falls gesetzt), keine persönlichen Schlüssel, nur Modelle aus `ABOARD_LLM_OPENAI_MODELLE`. Unlesbare, leere oder mehrzeilige Datei oder leere Modellliste halten den Start an. |
| `ABOARD_LLM_PII` | je nach Betriebsart | Vorgabe für den Filter personenbezogener Daten: `aus`, `pflicht` oder `nutzer` (jede Person wählt selbst). Voreinstellung: ohne Firmenmodus `nutzer`; im Firmenmodus mit eigener `ABOARD_LLM_OPENAI_BASE_URL` `aus`, ohne `pflicht`. Ein gesetzter Wert gilt auch ohne Firmenmodus; `aus` ohne Firmenmodus erzeugt eine Warnung im Log. Andere Werte halten den Start an. |
| `ABOARD_LLM_ANFRAGEN_PRO_STUNDE` | `60` | Höchstzahl Chat-Anfragen je Person und Stunde, wirksam nur im Firmenmodus; darüber antwortet der Server mit `429`. Ganze Zahl 1 bis 100000; ein ungültiger Wert hält den Start an, auch ohne Firmenmodus. |

Die Aufbewahrung der Chatverläufe regelt `ABOARD_LLM_RETENTION_DAYS` (Abschnitt
Aufbewahrungsfristen).

### Verschlüsselung und Backup

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_SECRET_KEY` | leer (aus) | **Geheimnis.** Hauptschlüssel, 32 Byte als Hex (64 Zeichen) oder Base64. Verschlüsselt alles Geheime in der Datenbank: Mail-Passwörter, persönliche KI-Schlüssel, Benachrichtigungs-Zugänge, Kalender-Abos, Schlüssel der Ausgangsschleuse, und Board-Backups, wenn deren Verschlüsselung eingeschaltet ist. Fehlt er, arbeitet der Server weiter, aber diese Funktionen antworten mit `503`. Ein Wechsel macht die bisher verschlüsselten Werte unbrauchbar; sie müssen neu eingegeben werden. **Achtung eingebaute Anmeldung:** Auch die Zwei-Faktor-Geheimnisse der eigenen Konten sind damit verschlüsselt; nach einem Wechsel nimmt der Server keinen Code der Authenticator-App mehr an, die Personen müssen im Admin-Reiter zurückgesetzt und neu eingeladen werden (`ABOARD_SECRET_KEY_PREVIOUS` hilft dort nicht; siehe `anmeldung.md`, „Bekannte Grenze“). |
| `ABOARD_SECRET_KEY_PREVIOUS` | leer | **Geheimnis.** Der vorherige Hauptschlüssel nach einem Wechsel. Wird nur beim Einspielen von Board-Backups benutzt, damit Archive mit dem alten Schlüssel lesbar bleiben; geschrieben wird immer mit dem aktuellen. Ist er unbrauchbar, läuft das Einspielen ohne ihn weiter und das Log nennt den Grund. |

Backup und Wiederherstellung beschreibt [Administration](administration.md).

### Benachrichtigungen

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_NOTIFICATIONS_DELIVERY` | aus | Gibt den tatsächlichen Versand von Benachrichtigungen (Mail, Gotify) frei. Nur `1`, `on`, `true`, `yes`, `ja` oder `an` schalten ihn ein; jeder andere Wert bedeutet aus. Ohne Freigabe läuft alles bis zum Versand durch und wird protokolliert, aber nichts verschickt. So lässt sich die Einrichtung prüfen, ohne echte Empfänger anzuschreiben. |

Die Kanäle selbst (Mailserver, Gotify) werden im Admin-Bereich eingestellt, siehe
[Administration](administration.md).

### Ausgänge und Ausgangsschleuse

`ABOARD_AUSGAENGE` regelt, ob der Server überhaupt Verbindungen nach außen aufbaut. Die
Ausgangsschleuse ist ein eigener, enger Weg, auf dem eine Person einen pseudonymisierten und
durchgesehenen Auszug einer Karte an ein externes Modell schickt. Beides erklärt
[Administration](administration.md).

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_AUSGAENGE` | `an` | `aus` sperrt alle ausgehenden Verbindungen des Servers: KI-Anbieter, Gotify, SMTP, IMAP. Unabhängig davon erreicht Gotify keine internen Adressen (Loopback, private Netze, Link-Local; geprüft an der aufgelösten Adresse), auch nicht über `ABOARD_AUSGAENGE_ERLAUBT`; Ausnahme nur über `ABOARD_GOTIFY_INTERN_ERLAUBT` (unten). Der Nutzer sieht dann „nicht erreichbar“, die Ursache steht im Server-Log. Ein gesperrter Weg meldet sofort „ausgehende Verbindung abgeschaltet“ statt in eine Zeitüberschreitung zu laufen. Andere Werte halten den Start an. |
| `ABOARD_AUSGAENGE_ERLAUBT` | leer | Ziele, die bei `ABOARD_AUSGAENGE=aus` trotzdem erreichbar bleiben, als Kommaliste aus `host` (jeder Port) oder `host:port`, z. B. `llm.example.org:8000,mail.example.org:587`. Keine URLs, keine Platzhalter; sonst Startabbruch. Läuft der Verkehr über einen Proxy, steht hier das eigentliche Ziel, nicht der Proxy. Bei `an` ohne Wirkung. |
| `ABOARD_GOTIFY_INTERN_ERLAUBT` | leer | Gotify-Ziele, die auch dann erreichbar sind, wenn sie intern liegen (LAN, Docker-Netz, `localhost`), als Kommaliste aus `host` oder `host:port`, z. B. `gotify.example.org` oder `gotify:8443,[::1]:8080`. **Die Freigabe gilt nur für den eingetragenen Port; ein Eintrag ohne Port gilt nur für 443.** Ein anderer Port desselben Hosts wird beim Eintragen abgelehnt und vom Dialer nicht angewählt. Wörtlicher Vergleich mit dem Host der Gotify-URL, ohne Groß-/Kleinschreibung und ohne Punkt am Ende; keine URLs, keine Platzhalter, nur ASCII; ein ungültiger Eintrag hält den Start an. Es gibt kein DNS-Vertrauen: Nur genau dieser Name mit diesem Port wird ohne Adressprüfung angewählt, Umleitungen bleiben verboten. Dazu lehnt das Eintragen einer Gotify-URL interne IP-Literale (z. B. `192.0.2.1`, `169.254.169.254`, `127.0.0.1`), `localhost` und Hosts mit Nicht-ASCII-Zeichen ab, solange sie nicht hier stehen. Gilt unabhängig von `ABOARD_AUSGAENGE`: Bei `aus` muss das Ziel zusätzlich in `ABOARD_AUSGAENGE_ERLAUBT` stehen. Läuft Gotify über `HTTPS_PROXY`, braucht es den Eintrag nicht; steht der Gotify-Host aber in `NO_PROXY`, geht die Verbindung direkt, und dann ist der Eintrag nötig. |
| `ABOARD_SCHLEUSE` | `aus` | `an` schaltet die Ausgangsschleuse ein. Bei `aus` gibt es weder Routen noch Oberfläche dafür. Andere Werte halten den Start an. Braucht `ABOARD_SECRET_KEY`. |
| `ABOARD_SCHLEUSE_AUFBEWAHRUNG_TAGE` | `30` | Tage, die die Zuordnung von Platzhaltern zu echten Namen nach Abschluss eines Auftrags aufbewahrt wird; nie gesendete Entwürfe verfallen dieselbe Zeit nach ihrer letzten Änderung. Ganze Zahl 1 bis 3650; ein ungültiger Wert hält den Start an. |
| `ABOARD_SCHLEUSE_AUSZUG_ZEICHEN` | `20000` | Größengrenze je Auszug in Zeichen (500 bis 200000); ein ungültiger Wert hält den Start an. |
| `ABOARD_SCHLEUSE_AUFTRAEGE_PRO_STUNDE` | `10` | Höchstzahl gesendeter Aufträge je Person und Stunde (1 bis 1000); ein ungültiger Wert hält den Start an. |
| `ABOARD_SCHLEUSE_PSEUDO_MODELL` | erstes Modell aus `ABOARD_LLM_OPENAI_MODELLE` | Lokales Modell des Firmenmodus für die zweite Stufe der Pseudonymisierung. Muss in `ABOARD_LLM_OPENAI_MODELLE` stehen, sonst bricht der Start bei eingeschalteter Schleuse ab. |

### Kalender

Für Kalender-Abos (iCal) gibt es keine eigene Variable. Sie brauchen `ABOARD_SECRET_KEY`, weil
die Abo-Tokens verschlüsselt gespeichert werden; ohne ihn antworten die Abo-Funktionen mit
`503`. Hinter einem Anmelde-Proxy muss der Pfad `/ical/` von der Anmeldung ausgenommen sein,
weil Kalender-Programme sich nicht anmelden können, siehe [Anmeldung](anmeldung.md).

### Wissensquelle

Keine Umgebungsvariable. Den Markdown-Ordner in der Compose-Datei nur lesend unterhalb von `/data/`
einhängen (etwa `- /srv/wissen:/data/wissen:ro`) und im Admin-Reiter „Wissen“ einschalten, siehe
[Administration](administration.md#wissensquelle).

### Board-Paar

Keine eigene Umgebungsvariable; das Board-Paar (die externe Kopie ab v1.54.0, Spiegel und `boardpaar.rueckweg` erst mit v1.55.0) läuft über **Einstellungen**, die ein Admin im Reiter „Board-Paar“ der
Admin-Oberfläche oder über die Admin-API setzt ([Administration](administration.md#board-paar-intern-und-extern),
[REST-API](api.md#board-paar)). Der Server liest sie bei jeder Anfrage; Umschalten wirkt ohne Neustart. Aus
den Umgebungsvariablen greifen zwei ein: `ABOARD_PUBLIC_BASE_URL` (Vorgabe für die Adresse des internen Boards, wenn man
eine externe Kopie anlegt) und `ABOARD_ACL_DOMAINS` (nur Domains daraus wirken im Team als `@domain`).

| Einstellung | Wert | Auf welchem Server | Bedeutung |
|---|---|---|---|
| `boardpaar.seite` | `aus` (Voreinstellung), `innen`, `aussen` | beide | Seite dieser Instanz. Ab v1.53.0 geht ein Wechsel nur, wenn auf der bisherigen Seite kein Board mehr gekoppelt ist. |
| `boardpaar.team` | leer oder E-Mail-Adressen und `@domain`, durch Komma getrennt (höchstens 100) | außen | Wer jede neue externe Kopie sieht: als Leser, bei `boardpaar.rueckweg = an` als Bearbeiter; alle Server-Admins, die beim Anlegen der Kopie Admin sind, werden Eigentümer. Eine `@domain` wirkt nur, wenn sie in `ABOARD_ACL_DOMAINS` steht. Wer später ins Team kommt, wird auf schon angelegten Kopien nicht eingetragen; wer aus dem Team genommen wird, verliert auf allen Kopien (auch außen entkoppelten, außer vor dem Release entkoppelten) die Rolle Leser bzw. Bearbeiter (Eigentümer bleibt; ab v1.55.0). Verglichen wird der Eintrag: Wer `@domain` durch eine Adresse dieser Domain ersetzt, nimmt auch dieser Person den Zugriff. Ein solcher Entzug geht nur mit Bestätigung (Admin-Dialog bzw. `PUT /admin/boardpaar/team`), der allgemeine Konfig-Weg lehnt ihn mit `409` ab. Ein ungültiger Eintrag lehnt das Speichern ab. |
| `boardpaar.rueckweg` | `aus` (Voreinstellung, auch leer oder gelöscht), `an` | außen | „Bearbeiten außen erlauben“: Das Team darf Spiegelkarten außen bearbeiten, die Änderungen gehen nach innen. Beim Wechsel wird die Rolle des Teams auf allen gekoppelten Kopien angeglichen (`an`: Leser werden Bearbeiter, sonst umgekehrt); außen entkoppelte Kopien bleiben, wie sie sind. Ein anderer Wert lehnt das Speichern ab. |

### Sonstiges

| Variable | Voreinstellung | Bedeutung |
|---|---|---|
| `ABOARD_SCHLEUSE_PDF_KIND` | leer | **Intern, nicht setzen.** Kennzeichnet den Hilfsprozess, den der Server selbst startet, um Text aus PDF-Dateien zu lesen. Wirkt nur zusammen mit einem internen Startargument; in der Umgebung des Servers hat die Variable keine Wirkung. |


## Hinweis zur Compose-Datei

Die mitgelieferte `docker-compose.yml` verwendet einige Hilfsvariablen, die **nicht** der
Server liest, sondern Compose beim Einsetzen der Werte oder die Backup-Schleife im
Begleitcontainer:

- `ABOARD_VERSION`: Image-Version, Voreinstellung `dev`.
- `ABOARD_PORT`: Port auf dem Host, Voreinstellung `8080`. Die Compose-Datei veröffentlicht ihn nur auf der Loopback-Schnittstelle (`127.0.0.1`), weil der Server ohne vorgeschaltete Anmeldung keinen Zugangsschutz hat.
- `ABOARD_BACKUP_DIR`, `ABOARD_KEEP_DAILY` (Voreinstellung `7`), `ABOARD_KEEP_WEEKLY`
  (Voreinstellung `4`): Zielverzeichnis und Aufbewahrung des optionalen Backup-Containers; sie
  werden als Argumente an `aboard-server backup` weitergegeben.

Einzelheiten in [Container](container.md).

