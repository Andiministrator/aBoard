# Anmeldung, Mehrnutzer und Rechte

Dieses Kapitel beschreibt, wie der aBoard-Server erfährt, **wer** eine Anfrage stellt, und wie
er daraus ableitet, **was** diese Person darf. Es richtet sich an alle, die den Server für mehr
als eine Person betreiben oder ihn aus dem Internet erreichbar machen wollen. Es kennt drei
Wege: ohne Anmeldung (eine Person), mit vorgeschaltetem Anmeldedienst (Forward-Auth) und mit
der eingebauten Anmeldung per OpenID Connect (ein Container, kein Proxy davor, ein oder mehrere Anbieter).

Wie du den Container grundsätzlich startest, steht in [`container.md`](container.md); die
vollständige Liste aller Umgebungsvariablen in [`konfiguration.md`](konfiguration.md).

---

## Inhalt

1. [Warum ein vorgeschalteter Anmeldedienst?](#warum-ein-vorgeschalteter-anmeldedienst)
2. [Die zwei Betriebsarten im Überblick](#die-zwei-betriebsarten-im-überblick)
3. [Einzelnutzer-Betrieb](#einzelnutzer-betrieb)
   (die eingebaute Anmeldung hat einen eigenen Abschnitt:
   [Eingebaute Anmeldung per OpenID Connect](#eingebaute-anmeldung-per-openid-connect))
4. [Mehrnutzer-Betrieb mit Forward-Auth](#mehrnutzer-betrieb-mit-forward-auth)
5. [Beispiel: Forward-Auth über Unix-Socket](#beispiel-forward-auth-über-unix-socket)
6. [Beispiel: Forward-Auth über Localhost-TCP](#beispiel-forward-auth-über-localhost-tcp)
7. [Wer darf aBoard überhaupt nutzen?](#wer-darf-aboard-überhaupt-nutzen)
8. [Rollen: Owner, Editor, Betrachter](#rollen-owner-editor-betrachter)
9. [Boards freigeben](#boards-freigeben)
10. [Der erste Owner und der erste Administrator](#der-erste-owner-und-der-erste-administrator)
11. [Umstieg vom Einzelnutzer- auf den Mehrnutzer-Betrieb](#umstieg-vom-einzelnutzer--auf-den-mehrnutzer-betrieb)
12. [Lokal testen ohne Anmeldedienst](#lokal-testen-ohne-anmeldedienst)
13. [Fehlersuche](#fehlersuche)

---

## Warum ein vorgeschalteter Anmeldedienst?

Im Forward-Auth-Betrieb verwaltet der aBoard-Server **keine Passwörter** und hat **keine eigene
Anmeldemaske**. (Die [eingebaute Anmeldung](#eingebaute-anmeldung-per-openid-connect) ist ein
anderer Betrieb: Dort kennt der Server eigene Konten mit Passwort und zweitem Faktor, und die
Adresse bleibt der Name.) Diese Aufgabe überlässt er einem spezialisierten Dienst, der vor ihm sitzt — typischerweise ein
Reverse-Proxy wie [Caddy](https://caddyserver.com/) zusammen mit einem Anmeldedienst wie
[Authelia](https://www.authelia.com/). Dieses Muster heißt **Forward-Auth**:

```
Browser ──HTTPS──▶ Caddy ──(1) Ist diese Person angemeldet?──▶ Authelia
                     │   ◀─(2) Ja: Remote-User, Remote-Email, … ──┘
                     │
                     └──(3) Anfrage + Identitäts-Header + gemeinsames Secret──▶ aBoard-Server
```

1. Caddy fragt bei jeder Anfrage Authelia, ob die Person angemeldet ist. Ist sie es nicht,
   leitet Authelia sie auf ihre Anmeldeseite um (Passwort, zweiter Faktor, Single-Sign-On —
   was immer du in Authelia eingerichtet hast).
2. Ist sie angemeldet, liefert Authelia die Identität in HTTP-Headern zurück.
3. Caddy reicht die Anfrage mit diesen Headern **und** einem nur ihm und dem aBoard-Server
   bekannten Secret an aBoard weiter.

aBoard vertraut den Identitäts-Headern also, ohne sie selbst prüfen zu können. Das ist nur
sicher, wenn **niemand außer dem Proxy** den aBoard-Server erreichen kann. Darum erzwingt der
Server im Mehrnutzer-Betrieb zwei Schutzmaßnahmen, die weiter unten beschrieben sind: das
gemeinsame Secret und eine Prüfung der Lauschadresse.

Der Vorteil dieses Aufbaus: Du verwendest für aBoard dieselbe Anmeldung (und dieselbe
Nutzerverwaltung, z. B. LDAP oder einen OpenID-Connect-Anbieter) wie für deine übrigen Dienste.
Statt Authelia funktioniert jeder Proxy, der die unten genannten Header setzt — die Beispiele
in diesem Kapitel verwenden Caddy und Authelia.


---

## Die zwei Betriebsarten im Überblick

Die ersten zwei Betriebsarten wählt **ein** Schalter: `ABOARD_TRUST_FORWARD_AUTH`. Die dritte,
die eingebaute Anmeldung, schaltet `ABOARD_AUTH_MODE=builtin` ein (bisheriger Name:
`ABOARD_ANMELDUNG=oidc`, gleichbedeutend); sie hat einen eigenen Abschnitt
([unten](#eingebaute-anmeldung-per-openid-connect)). Die eingebaute Anmeldung und
`ABOARD_TRUST_FORWARD_AUTH` zusammen sind nicht erlaubt.

| | Einzelnutzer-Betrieb | Mehrnutzer-Betrieb |
|---|---|---|
| `ABOARD_TRUST_FORWARD_AUTH` | nicht gesetzt (oder `0`, `false`, `no`, `off`) | `1` (oder jeder andere nicht-leere Wert) |
| `ABOARD_FORWARD_AUTH_SECRET` | ohne Bedeutung | **Pflicht** |
| Anmeldedienst davor | nicht nötig | **Pflicht** |
| Wer kommt herein? | jeder, der den Port erreicht | nur Anfragen mit gültigem Secret und Identität |
| Welche Boards sieht man? | alle | nur eigene und freigegebene |
| Rollen (Owner/Editor/Betrachter) | werden nicht geprüft | werden bei jeder Anfrage geprüft |
| Server-Administration im Browser | nicht verfügbar | für Administratoren |
| Typischer Einsatz | allein, im Heimnetz, im VPN, auf dem eigenen Rechner | Team, Zugriff aus dem Internet |

Eine eigene Einstellung für die Zugriffsrechte gibt es nicht: Sobald der Server eine Identität
kennt, prüft er die Rechte. Es gibt keinen Zwischenzustand „Anmeldung an, Rechte aus“.

Die eingebaute Anmeldung verhält sich in der Tabelle wie der Mehrnutzer-Betrieb: Personen und
Rollen werden geprüft, die Server-Administration steht zur Verfügung. Statt Secret und
Anmeldedienst davor braucht sie einen OIDC-Client bei mindestens einem Anbieter oder eigene Konten
mit Passwort. Herein kommt, wer auf der Vorab-Liste steht oder ein aktives Konto hat (Einladung mit
Bestätigung durch einen Admin).

> **Achtung beim Wert `0`:** `ABOARD_TRUST_FORWARD_AUTH=0` bedeutet **aus**. Als „aus“ gelten
> ein leerer Wert sowie `0`, `false`, `no` und `off` (Groß-/Kleinschreibung egal). Jeder andere
> Wert schaltet den Mehrnutzer-Betrieb ein.

### Zustandsmatrix beim Start

Bevor der Server überhaupt lauscht, prüft er seine Konfiguration. Daraus ergeben sich diese
Fälle:

| `ABOARD_TRUST_FORWARD_AUTH` | Lauschadresse | `ABOARD_FORWARD_AUTH_INSECURE_BIND` | `ABOARD_FORWARD_AUTH_SECRET` | Ergebnis |
|---|---|---|---|---|
| aus | beliebig | ohne Bedeutung | ohne Bedeutung | startet im **Einzelnutzer-Betrieb** |
| an | Unix-Socket oder Loopback (`127.x.x.x`, `localhost`, `::1`) | ohne Bedeutung | gesetzt | startet im **Mehrnutzer-Betrieb** |
| an | alle Schnittstellen oder fremde Adresse (z. B. `:8080`, `0.0.0.0:8080`) | nicht gesetzt | beliebig | **Start bricht ab** (unsichere Lauschadresse) |
| an | alle Schnittstellen oder fremde Adresse | `1` | gesetzt | startet im **Mehrnutzer-Betrieb**, mit lauter Warnung im Log |
| an | beliebig (sicher oder freigegeben) | — | fehlt oder nur Leerzeichen | **Start bricht ab** (Secret fehlt) |

Beide Abbrüche nennen im Log die Ursache und die Abhilfe.

Mit der eingebauten Anmeldung gilt eine zweite Matrix. Sie wird zuerst geprüft. `Schalter an`
heißt: `ABOARD_AUTH_MODE=builtin` oder `ABOARD_ANMELDUNG=oidc` (oder beide mit gleicher Bedeutung).

| Schalter (`ABOARD_AUTH_MODE` / `ABOARD_ANMELDUNG`) | Weitere Bedingungen | Ergebnis |
|---|---|---|
| beide leer | keine Variable `ABOARD_OIDC_*`, `ABOARD_AUTH_*`, `ABOARD_INVITE_*` gesetzt | Es gilt die Matrix oben. |
| beide leer | irgendeine Variable `ABOARD_OIDC_*`, `ABOARD_AUTH_*` oder `ABOARD_INVITE_*` gesetzt | **Start bricht ab** (der Server liefe ohne Anmeldung) |
| an | alles unten erfüllt | startet mit **eingebauter Anmeldung**; Forward-Auth ist aus |
| `builtin` und `oidc` zugleich | — | gleichbedeutend, kein Fehler |
| ein Wert unter einem der beiden Namen ist ungültig | — | **Start bricht ab** |
| an | `ABOARD_TRUST_FORWARD_AUTH` an | **Start bricht ab** (zwei Anmeldewege) |
| an | `ABOARD_DEV_FAKE_USER` oder `ABOARD_DEV_FAKE_EMAIL` gesetzt | **Start bricht ab** |
| an | ein Anbieter ist halb eingerichtet, sein Issuer ist keine https-Adresse, eine Variable gehört zu einem nicht gelisteten Kürzel, oder ein Name `ABOARD_OIDC_…` ist unbekannt | **Start bricht ab** |
| an | `ABOARD_AUTH_METHODS` nennt einen nicht eingerichteten Anbieter, einen doppelten Wert oder keinen Weg | **Start bricht ab** |
| an | ein Name `ABOARD_AUTH_…` oder `ABOARD_INVITE_…` ist unbekannt (Tippfehler wie `ABOARD_AUTH_METHOD`; bekannt sind `ABOARD_AUTH_MODE`, `ABOARD_AUTH_METHODS`, `ABOARD_INVITE_METHODS`) | **Start bricht ab** |
| an | `ABOARD_INVITE_METHODS` nennt einen Weg, den `ABOARD_AUTH_METHODS` nicht anbietet, einen doppelten Wert oder keinen Weg | **Start bricht ab** |
| an | `ABOARD_SECRET_KEY` fehlt oder ist ungültig | **Start bricht ab** |
| an | `ABOARD_PUBLIC_BASE_URL` fehlt, ist nicht `https://` oder trägt einen Pfad | **Start bricht ab** |
| an | ein Eintrag in der Vorab-Liste ist ungültig, oder `ABOARD_OIDC_ALLOWED` und `ABOARD_OIDC_ZUGELASSEN` widersprechen sich | **Start bricht ab** |
| an | Vorab-Liste und `ABOARD_BOOTSTRAP_OWNER` leer, und es gibt kein aktives Konto | startet, mit Warnung im Log; **niemand kommt herein** |
| an | ein Kürzel in `ABOARD_OIDC_PROVIDERS` ohne eigene Variablen | startet, mit Warnung im Log; der Anbieter wird nicht angeboten |
| an | ein Anbieter beim Start nicht erreichbar | startet; dessen Anmeldung antwortet `503`, bis er erreichbar ist (kein Neustart nötig), die anderen Anbieter laufen |
| ein Schalter mit anderem Wert | — | **Start bricht ab** (erlaubt: leer, `builtin`, `oidc`) |

Der Abbruch nennt im Log alle gefundenen Mängel auf einmal.

---

## Einzelnutzer-Betrieb

Ohne `ABOARD_TRUST_FORWARD_AUTH` arbeitet der Server **ohne jede Anmeldung**. Er kennt keine
Personen und keine Rollen; jede Anfrage darf alles — alle Boards lesen, ändern, löschen und
Anhänge hochladen. Das ist die Voreinstellung und für eine Person auf dem eigenen Rechner oder
im eigenen Netz bequem:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:<version>
    ports:
      - "127.0.0.1:8080:8080"   # nur vom eigenen Rechner aus erreichbar
    volumes:
      - aboard-db:/data/db
      - aboard-anhaenge:/data/attachments
    restart: unless-stopped

volumes:
  aboard-db:
  aboard-anhaenge:
```

> 🛑 **Mach einen Server im Einzelnutzer-Betrieb niemals aus dem Internet erreichbar.**
> Jeder, der den Port erreicht, hat vollen Zugriff auf alle Daten. Erlaubt ist er nur in einem
> Netz, dem du vertraust (eigener Rechner, Heimnetz, VPN). Wer von außen zugreifen will,
> verwendet den Mehrnutzer-Betrieb — auch wenn nur eine Person das Board nutzt.

Einige Funktionen setzen eine bekannte Person voraus und stehen im Einzelnutzer-Betrieb nicht
zur Verfügung, insbesondere die Server-Administration im Browser (siehe
[`administration.md`](administration.md)).

---

## Mehrnutzer-Betrieb mit Forward-Auth

### Die Identitäts-Header

Im Mehrnutzer-Betrieb liest der Server diese Header, die der Proxy vom Anmeldedienst übernimmt:

| Header | Pflicht | Wofür aBoard ihn verwendet |
|---|---|---|
| `Remote-Email` | **ja** (für alles, was Boards betrifft) | Die E-Mail-Adresse **ist** die Identität: Zuordnung des Kontos, aller Freigaben und Rollen. Groß-/Kleinschreibung und Leerzeichen am Rand werden ignoriert. |
| `Remote-User` | nein | Nur Anzeigename, falls `Remote-Name` fehlt (und der Wert nicht nur aus Ziffern besteht — manche Anmeldedienste liefern hier eine numerische Konto-Kennung). |
| `Remote-Name` | nein | Anzeigename (Avatar, Personenlisten). Fehlt er, zeigt aBoard `Remote-User` oder die E-Mail-Adresse. |
| `Remote-Groups` | nein | Wird gelesen, aber **nicht** für Rechte ausgewertet. Wer nur bestimmte Gruppen hereinlassen will, regelt das im Anmeldedienst (siehe [unten](#wer-darf-aboard-überhaupt-nutzen)). |
| `X-Aboard-Forward-Auth-Secret` | **ja** | Das gemeinsame Secret, siehe nächster Abschnitt. Setzt der Proxy selbst, nicht der Anmeldedienst. |

Eine Anfrage braucht mindestens `Remote-User` oder `Remote-Email`, sonst wird sie abgewiesen.
Für alle Board-Zugriffe ist `Remote-Email` nötig — fehlt sie, antwortet der Server dort mit
`403`. Achte also darauf, dass dein Anmeldedienst die E-Mail-Adresse mitliefert und dass
diese Adresse **stabil** bleibt (siehe [Wer darf aBoard überhaupt nutzen?](#wer-darf-aboard-überhaupt-nutzen)).

### Strenger Modus: was ohne Anmeldung durchkommt

Im Mehrnutzer-Betrieb beantwortet der Server **jede** Anfrage mit `401`, bei der

- das Secret fehlt oder nicht passt, **oder**
- weder `Remote-User` noch `Remote-Email` gesetzt ist.

Das gilt auch für die Weboberfläche selbst. Die Antwort verrät absichtlich nicht, welcher der
beiden Fälle vorlag; im Server-Log steht der Grund (siehe [Fehlersuche](#fehlersuche)).

Ausgenommen sind nur wenige **lesende** Pfade (`GET` und `HEAD`), die ohne Anmeldung
erreichbar sein müssen:

| Pfad | Warum offen | Eigener Schutz |
|---|---|---|
| `/healthz` | Gesundheitsprüfung durch Container-Laufzeit und Proxy | liefert keine Board-Daten |
| `/ical/calendar.ics` | Kalender-Abos (Kalenderprogramme können sich nicht beim Anmeldedienst anmelden) | eigenes Token in der URL |

Auf diesen Pfaden übernimmt der Server eine mitgeschickte Identität bewusst **nicht**.

Wenn du den Kalender-Feed nutzt, muss der Proxy diesen Pfad am Anmeldedienst vorbeileiten —
sonst landet das Kalenderprogramm auf der Anmeldeseite. Ein Beispiel steht im
[Caddyfile unten](#caddyfile). Den Agenten-Zugang unter `/agent/` beschreibt
[`api.md`](api.md); auch er läuft an der Anmeldung vorbei und braucht dort eine
eigene Proxy-Regel.

**Board-Paar intern/extern** (siehe [Administration](administration.md#board-paar-intern-und-extern)):
Das Push-Skript kommt ebenfalls über den Agenten-Eingang, mit eigenen Tokens (`abs_…` und
`abr_…` innen, `abe_…` außen; die Wege `aenderungen` und `anwenden` des Rückwegs erst mit v1.55.0). Der Server braucht dafür **keinen** weiteren offenen Pfad. Dasselbe gilt für das Abholen der Board-Sicherungen mit `abb_…` (`GET`/`HEAD /agent/sicherungen` und `/agent/sicherungen/*`, auf jeder Seite, ab v1.56.0). Am Proxy gilt:

| Server | Was an der Anmeldung vorbei muss | Wenn der Proxy `/agent/*` schon vorbeileitet |
|---|---|---|
| intern (Seite `innen`) | `GET /agent/schatten/*`; mit externen Kopien zusätzlich `GET`/`HEAD /agent/boardpaar/auftraege`, `GET`/`HEAD /agent/boardpaar/spiegel/*`, `PUT /agent/boardpaar/ziele`, `POST /agent/boardpaar/auftraege/*/erledigt`, `POST /agent/boardpaar/anwenden/*` und `POST /agent/boardpaar/erneuern` | nichts weiter zu tun |
| extern (Seite `aussen`) | `PUT /agent/schatten/*`; mit externen Kopien zusätzlich `POST /agent/boardpaar/boards`, `PUT /agent/boardpaar/spiegel/*`, `GET`/`HEAD /agent/boardpaar/aenderungen/*`, `POST /agent/boardpaar/aenderungen/*/bestaetigt` und `POST /agent/boardpaar/erneuern`, sonst nichts auf diesem Pfad | nichts weiter zu tun; dann schützen allein Token, Schranke und Fehlversuch-Drossel, wie bei allen Agentenwegen |

Wer nur genau diese Wege durchlässt statt `/agent/*` pauschal, grenzt den Zweig mit einem Matcher
ein, auf dem externen Server etwa:

```caddyfile
	@schatten {
		method PUT
		path /agent/schatten/*
	}
	handle @schatten {
		request_header -Remote-User
		request_header -Remote-Groups
		request_header -Remote-Name
		request_header -Remote-Email
		request_header -X-Aboard-Forward-Auth-Secret
		reverse_proxy unix//run/aboard/aboard.sock
	}
```

Das Beispiel zeigt nur den Schatten-Weg der Stufe 1; für die Wege einer externen Kopie kommen ihre Pfade und
Methoden aus der Tabelle dazu. Auf dem internen Server steht beim Schatten-Weg `method GET`. Eine mitgeschickte Identität wertet keiner dieser
Wege aus. Die Anzeige der Schatten im Browser (`GET /boards/{id}/schatten`) läuft dagegen ganz
normal durch die Anmeldung.

### Das gemeinsame Secret

Das Secret ist ein zufälliger Wert, den **nur** der Proxy und der aBoard-Server kennen. Der
Proxy schickt ihn bei jeder Anfrage im Header `X-Aboard-Forward-Auth-Secret` mit; der Server
vergleicht ihn mit `ABOARD_FORWARD_AUTH_SECRET`. Passt er nicht, gibt es `401`.

Warum das nötig ist, obwohl der Server ohnehin nur intern lauscht: Auf demselben Rechner
können andere Programme oder Container den internen Port bzw. Socket ebenfalls erreichen. Ohne
Secret könnten sie sich mit einem selbst gesetzten `Remote-Email`-Header als beliebige Person
ausgeben — einschließlich Owner-Rechten. Deshalb gibt es **keine** Möglichkeit, das Secret
abzuschalten; ohne Secret startet der Server im Mehrnutzer-Betrieb nicht.

Ein Secret erzeugen, z. B.:

```bash
openssl rand -hex 32
```

Leg den Wert in einer `.env`-Datei neben deiner `docker-compose.yml` ab (nicht ins
Versionsverwaltungssystem einchecken) und reich ihn **an beide** Container weiter — an
aBoard **und** an den Proxy:

```dotenv
# .env
ABOARD_FORWARD_AUTH_SECRET=<geheimnis>
```

Leerzeichen und ein Zeilenumbruch am Ende des Werts werden auf der Server-Seite ignoriert.

> **Reihenfolge beim Einrichten oder Ändern des Secrets:** zuerst den Wert in **beiden**
> Konfigurationen setzen, dann beide Container neu starten. Fehlt das Secret im aBoard-Container,
> startet der Server nicht; fehlt es im Proxy, bekommt jede Person `401`.

### Die Lauschadresse (`ABOARD_FORWARD_AUTH_INSECURE_BIND`)

Im Mehrnutzer-Betrieb prüft der Server beim Start, ob er nur intern erreichbar ist. Als
**sicher** gilt:

- ein Unix-Socket (`ABOARD_UNIX_SOCKET` gesetzt — dann wird `ABOARD_ADDR` nicht verwendet), oder
- eine TCP-Adresse auf der Loopback-Schnittstelle: `127.0.0.1:8080` (allgemein `127.x.x.x`),
  `localhost:8080`, `[::1]:8080`.

Als **unsicher** gilt alles andere, insbesondere die Voreinstellung `:8080` (alle
Schnittstellen), `0.0.0.0:8080`, `[::]:8080` oder eine konkrete Netzwerkadresse. In diesem Fall
**bricht der Start ab**.

Mit `ABOARD_FORWARD_AUTH_INSECURE_BIND=1` kannst du diese Prüfung bewusst überstimmen. Der
Server startet dann und schreibt eine deutliche Warnung ins Log. Das ist gedacht für Aufbauten,
in denen das Netz **auf andere Weise** abgeschottet ist und der Server das nur nicht selbst
erkennen kann, etwa:

- aBoard lauscht im Container auf `:8080`, der Port ist aber nur auf der Loopback-Schnittstelle
  des Rechners veröffentlicht (`-p 127.0.0.1:8080:8080`). Innerhalb des Containers sieht `:8080`
  unsicher aus, von außen ist er es nicht.
- aBoard und Caddy hängen in einem eigenen Container-Netz, und aBoard veröffentlicht keinen Port.

Das Secret bleibt auch dann Pflicht und ist in solchen Aufbauten die eigentliche Schutzschicht.

> **Container und `127.0.0.1`:** Innerhalb eines Containers bezeichnet `127.0.0.1` den
> Container selbst. Ein Caddy in einem *anderen* Container erreicht `ABOARD_ADDR=127.0.0.1:8080`
> deshalb **nicht**. Die Localhost-TCP-Variante funktioniert nur, wenn sich Caddy und aBoard
> einen Netzwerk-Namensraum teilen — z. B. wenn aBoard im Host-Netz läuft und Caddy direkt auf
> dem Rechner. Für zwei getrennte Container ist der Unix-Socket die einfachere Wahl.

---

## Beispiel: Forward-Auth über Unix-Socket

Empfohlener Aufbau, wenn Caddy und aBoard beide als Container laufen: aBoard öffnet **keinen**
TCP-Port, sondern einen Unix-Socket in einem Volume, das sich beide Container teilen.

Voraussetzungen:

- Caddy ab Version 2.7 (Direktive `forward_auth`)
- ein eingerichtetes Authelia unter `auth.example.org`, das die Anfragen für
  `board.example.org` prüft
- DNS für `board.example.org` zeigt auf den Rechner, auf dem Caddy läuft

### docker-compose.yml

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:<version>
    environment:
      # Unix-Socket statt TCP-Port — sicherer Bind, kein Port nach außen
      ABOARD_UNIX_SOCKET: /run/aboard/aboard.sock
      # Mehrnutzer-Betrieb einschalten …
      ABOARD_TRUST_FORWARD_AUTH: "1"
      # … mit Pflicht-Secret (derselbe Wert wie im Proxy-Container)
      ABOARD_FORWARD_AUTH_SECRET: "${ABOARD_FORWARD_AUTH_SECRET}"
      # Nur beim allerersten Start mit leerer Datenbank wirksam, siehe unten
      ABOARD_BOOTSTRAP_OWNER: "person@example.org"
    volumes:
      - aboard-db:/data/db
      - aboard-anhaenge:/data/attachments
      - aboard-socket:/run/aboard
    restart: unless-stopped

  proxy:
    image: caddy:2
    ports:
      - "80:80"
      - "443:443"
    environment:
      # Der Proxy setzt den Wert beim Laden des Caddyfiles ein
      ABOARD_FORWARD_AUTH_SECRET: "${ABOARD_FORWARD_AUTH_SECRET}"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - proxy-data:/data
      - aboard-socket:/run/aboard
    restart: unless-stopped

  # authelia: … (siehe Authelia-Dokumentation)

volumes:
  aboard-db:
  aboard-anhaenge:
  aboard-socket:
  proxy-data:
```

**Rechte am Socket:** Der aBoard-Prozess läuft im Container als Benutzer und Gruppe `10001` und
legt den Socket mit dem Modus `0660` an (Besitzer und Gruppe dürfen lesen und schreiben). Ein
Caddy-Container, der als `root` läuft (Voreinstellung des offiziellen Images), kann ihn
verwenden. Läuft Caddy als anderer Benutzer, gib ihm die Gruppe `10001`
(`group_add: ["10001"]`) oder setz bei aBoard `ABOARD_UNIX_SOCKET_MODE`. Erlaubt sind
`0600`, `0660` (Voreinstellung) und `0666`; ein ungültiger Wert oder ein Modus, der sich nicht
setzen lässt, bricht den Start ab. Verwende `0666` nur, wenn das Socket-Volume ausschließlich
Caddy und aBoard zur Verfügung steht.

Die Gesundheitsprüfung des Images (`aboard-server healthcheck`) liest dieselben Variablen wie
der Server und funktioniert auch mit Unix-Socket.

### Caddyfile

```caddyfile
board.example.org {
    # Optional: Kalender-Feed ohne Anmeldung durchlassen.
    # aBoard prüft hier ein eigenes Token; nur nötig, wenn du Kalender-Abos nutzt.
    @kalender path /ical/calendar.ics
    handle @kalender {
        reverse_proxy unix//run/aboard/aboard.sock
    }

    handle {
        # route: Caddy führt die Direktiven darin in genau dieser Reihenfolge aus.
        route {
            # Identitäts-Header, die der Browser selbst mitschickt, verwerfen.
            request_header -Remote-User
            request_header -Remote-Email
            request_header -Remote-Groups
            request_header -Remote-Name

            # Jede Anfrage bei Authelia prüfen lassen. Bei Erfolg übernimmt
            # Caddy die Identitäts-Header aus der Antwort von Authelia.
            forward_auth authelia:9091 {
                uri /api/authz/forward-auth
                copy_headers Remote-User Remote-Email Remote-Groups Remote-Name
            }

            # Weiter an aBoard — mit dem gemeinsamen Secret.
            reverse_proxy unix//run/aboard/aboard.sock {
                header_up X-Aboard-Forward-Auth-Secret {$ABOARD_FORWARD_AUTH_SECRET}
            }
        }
    }
}
```

Hinweise zum Caddyfile:

- **Mitgeschickte `Remote-*`-Header entfernen:** aBoard glaubt jedem `Remote-*`-Header, der mit dem
  richtigen Secret ankommt. Liefert der Anmeldedienst einen dieser Header nicht (etwa
  `Remote-Name`), könnte je nach Proxy und Version sonst der Wert durchkommen, den der Browser selbst gesetzt hat — die vier
  `request_header -…`-Zeilen sorgen dafür, dass nur die Angaben des Anmeldedienstes ankommen. Der
  `route`-Block ist nötig, weil Caddy die Direktiven sonst nach einer eigenen festen Reihenfolge
  ausführt und das Entfernen dann erst nach `forward_auth` stattfinden könnte.
- **`{$ABOARD_FORWARD_AUTH_SECRET}`** wird beim Laden der Konfiguration aus der Umgebung des
  **Caddy**-Containers eingesetzt. Verwende diese Schreibweise, nicht den
  Laufzeit-Platzhalter `{env.ABOARD_FORWARD_AUTH_SECRET}`.
- **Authelia-Endpunkt:** `/api/authz/forward-auth` ist der Endpunkt aktueller Authelia-Versionen.
  Ältere Versionen verwenden `uri /api/verify?rd=https://auth.example.org`. Maßgeblich ist die
  Caddy-Anleitung in der Authelia-Dokumentation zu deiner Version.
- **TLS** holt Caddy automatisch, sobald der Name öffentlich auflösbar ist. aBoard selbst
  spricht nur unverschlüsseltes HTTP über den Socket.
- **Sitzungs-Cookie des Anmeldedienstes:** aBoard weist schreibende Browser-Anfragen von fremden
  Seiten selbst ab (CSRF-Schutz, siehe [`api.md`](api.md#csrf-schutz-für-browser-anfragen)).
  Stell das Sitzungs-Cookie deines Anmeldedienstes trotzdem auf `SameSite=Lax` oder
  `SameSite=Strict`, damit der Browser es bei Anfragen von fremden Seiten gar nicht erst
  mitschickt. Wie die Einstellung heißt, steht in der Dokumentation deines Anmeldedienstes.
- **Keine Pufferung für den Ereignisstrom:** Live-Änderungen kommen als Server-Sent Events
  (`/boards/{id}/events`). Ein Proxy, der Antworten sammelt, bevor er sie weitergibt, hält diese
  Ereignisse zurück, und das Board aktualisiert sich nicht mehr von selbst. Caddy gibt
  Antworten vom Typ `text/event-stream` ohnehin sofort weiter; `flush_interval -1` in
  `reverse_proxy` erzwingt das ausdrücklich für jede Antwort (jeder geschriebene Teil geht sofort an
  den Client). Bei nginx heißt die Entsprechung `proxy_buffering off`.

---

## Beispiel: Forward-Auth über Localhost-TCP

Wenn Caddy direkt auf dem Rechner läuft (nicht im Container) und aBoard im Host-Netz, kann
aBoard auf der Loopback-Schnittstelle lauschen:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:<version>
    network_mode: host            # teilt sich die Loopback-Schnittstelle mit dem Rechner
    environment:
      ABOARD_ADDR: "127.0.0.1:8080"
      ABOARD_TRUST_FORWARD_AUTH: "1"
      ABOARD_FORWARD_AUTH_SECRET: "${ABOARD_FORWARD_AUTH_SECRET}"
    volumes:
      - aboard-db:/data/db
      - aboard-anhaenge:/data/attachments
    restart: unless-stopped

volumes:
  aboard-db:
  aboard-anhaenge:
```

```caddyfile
board.example.org {
    route {
        request_header -Remote-User
        request_header -Remote-Email
        request_header -Remote-Groups
        request_header -Remote-Name

        forward_auth 127.0.0.1:9091 {
            uri /api/authz/forward-auth
            copy_headers Remote-User Remote-Email Remote-Groups Remote-Name
        }

        reverse_proxy 127.0.0.1:8080 {
            header_up X-Aboard-Forward-Auth-Secret {$ABOARD_FORWARD_AUTH_SECRET}
        }
    }
}
```

Das Secret muss dann in der Umgebung des Caddy-Dienstes auf dem Rechner stehen (z. B. in der
Dienst-Konfiguration deines Init-Systems).

Alternative ohne Host-Netz: aBoard lauscht im Container auf `:8080`, der Port wird nur auf der
Loopback-Schnittstelle veröffentlicht (`ports: ["127.0.0.1:8080:8080"]`). Weil der Server
innerhalb des Containers eine Lauschadresse auf allen Schnittstellen sieht, braucht dieser
Aufbau zusätzlich `ABOARD_FORWARD_AUTH_INSECURE_BIND: "1"` (siehe
[Die Lauschadresse](#die-lauschadresse-aboard_forward_auth_insecure_bind)).

---

## Eingebaute Anmeldung per OpenID Connect


Mit `ABOARD_AUTH_MODE=builtin` meldet der Server die Personen selbst an. Der bisherige Name
`ABOARD_ANMELDUNG=oidc` bedeutet dasselbe und bleibt gültig; du musst nichts umstellen. Der Server
braucht keinen Proxy und keinen Anmeldedienst davor, also einen Container statt mehrerer. Die
Anmeldung läuft über einen oder mehrere OpenID-Connect-Anbieter, zum Beispiel Google und Authelia
nebeneinander, und über eigene Konten mit E-Mail-Adresse und Passwort (Weg `password`). Bei einem
Anbieter verwaltet der Anbieter Passwort und zweiten Faktor; der Server sieht sie nie. Bei eigenen
Konten speichert der Server nur einen argon2id-Hash des Passworts. Nach dem Passwort verlangt er
immer einen zweiten Faktor: den sechsstelligen Code einer Authenticator-App oder einen der zehn
Wiederherstellungscodes. Einladen, Bestätigen, Sperren und Zurücksetzen erledigt ein Admin im
Reiter „User-Verwaltung“ der Server-Administration; die Bedienung steht in
[Server verwalten](administration.md#konten-und-einladungen-eingebaute-anmeldung), die Regeln, wer
hereinkommt, [weiter unten](#wer-kommt-herein-vorab-liste-und-konten).

Die zwei Schalter:

- Du kannst `ABOARD_AUTH_MODE` und `ABOARD_ANMELDUNG` beide setzen, wenn sie dasselbe bedeuten
  (`builtin` und `oidc` sind gleichbedeutend; andere Werte gibt es nicht). Ist ein Wert ungültig,
  bricht der Start ab.
- Setzt du eine Variable `ABOARD_OIDC_*`, `ABOARD_AUTH_*` oder `ABOARD_INVITE_*`, ohne den Schalter
  einzuschalten, bricht der Start ab. Sonst liefe der Server ohne jede Anmeldung.

### So läuft eine Anmeldung ab

1. Eine Person öffnet das Board ohne Sitzung. Der Server leitet sie auf die Anmeldeseite
   `/anmeldung` (`302`). Programme und Skripte, die die Programmierschnittstelle aufrufen, bekommen
   stattdessen `401`.
2. Ist genau ein Anbieter eingerichtet, schickt `/anmeldung` die Person sofort weiter zu diesem
   Anbieter. Sind es mehrere, zeigt die Seite je Anbieter einen Knopf
   ([Die Anmeldeseite](#die-anmeldeseite)). Beim Anbieter meldet sich die Person an.
3. Der Anbieter schickt sie auf `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<kürzel>` zurück
   (beim Anbieter `standard` auf `/anmeldung/rueckruf`). Der Server prüft den Vorgang, holt die
   bestätigte E-Mail-Adresse und prüft, ob die Person
   [hereinkommt](#wer-kommt-herein-vorab-liste-und-konten). Ein Anmeldevorgang gilt 10 Minuten.
4. Kommt sie herein, legt der Server eine Sitzung an und leitet auf `/` weiter. Beim ersten Mal
   entstehen Konto und persönliches Board wie
   [oben beschrieben](#wer-darf-aboard-überhaupt-nutzen).

Ohne Sitzung erreichbar bleiben nur `/healthz` (Gesundheitsprüfung), `/ical/calendar.ics`
(Kalender-Abo mit eigenem Token) sowie `/anmeldung`, `/anmeldung/start`,
`/anmeldung/rueckruf` und `/anmeldung/rueckruf/<kürzel>`, `POST /anmeldung/passwort` (Passwort-Formular),
`POST /anmeldung/code` (zweiter Faktor), die Seite `/anmeldung/abgemeldet` nach dem Abmelden und
`GET`/`POST /einladung/<token>` (Einladung einlösen). Ein unbekannter, abgelaufener oder schon
benutzter Einladungslink antwortet `404`, ebenso `POST /anmeldung/passwort` und
`POST /anmeldung/code`, wenn `ABOARD_AUTH_METHODS` den Weg `password` nicht nennt. Jeder andere Aufruf ohne
Sitzung wird wie überall behandelt: Ein Seitenaufruf im Browser wird auf `/anmeldung`
weitergeleitet, alles andere bekommt `401`.

Sitzungen:

- Eine Sitzung endet nach **8 Stunden ohne Nutzung**, spätestens **7 Tage** nach der Anmeldung.
- Der Knopf „Abmelden“ in der Seitenleiste beendet sie sofort (`POST /anmeldung/ende`) und zeigt die
  Seite „Abgemeldet“. Beim Anmeldedienst (z. B. Google) bleibst du angemeldet — auf einem geteilten
  Rechner dort ebenfalls abmelden. Der Link „Neu anmelden“ auf dieser Seite fragt beim Anbieter nach
  dem Konto, statt still mit der offenen Anbieter-Sitzung wieder anzumelden.
- Eine neue Anmeldung im selben Browser beendet die vorherige Sitzung.
- Läuft die Sitzung in einem offenen Browser-Tab ab, zeigt das Board ein Band „Sitzung
  abgelaufen — neu anmelden“. Ungesendete Änderungen bleiben im Tab erhalten. Die Anmeldung
  öffnet in einem neuen Tab. Nach der Anmeldung **diesen neuen Tab schließen** und im alten
  weitermachen — sonst sind zwei Tabs mit demselben Board offen (das Band sagt das auch).
- Löschst du eine Person in der Server-Administration, verschwinden auch ihre Sitzungen und
  ihre Bindung an das Anbieter-Konto.

### Die Anmeldeseite

`/anmeldung` zeigt für jeden angebotenen Anbieter einen Knopf „Mit <Beschriftung> anmelden“.

- **Bleibt genau ein Weg übrig und ist es ein Anbieter** (weil `ABOARD_AUTH_METHODS` nur ihn nennt):
  Die Seite zeigt nichts an, sondern leitet sofort zu ihm weiter. Ohne diese Einschränkung ist
  `password` immer ein Weg, die Seite erscheint also mit Passwort-Formular.
- **Sprache:** Deutsch oder Englisch, nach der Spracheinstellung des Browsers. Ist keine der beiden
  eingestellt, erscheint Deutsch.
- **Nicht erreichbarer Anbieter:** Die Seite fragt die Anbieter nie ab, sie erscheint also immer.
  Ist ein Anbieter nicht erreichbar, merkt die Person das erst beim Klick (Meldung „Anmeldung gerade
  nicht möglich“, Status `503`). Die Knöpfe der anderen Anbieter funktionieren weiter.
- **Beschriftung:** `ABOARD_OIDC_<ID>_LABEL`. Fehlt sie, steht „Google“ (bei der Google-Adresse)
  oder sonst der Rechnername des Anbieters, etwa `auth.example.org`. Setz deshalb bei jedem
  Anbieter außer Google eine Beschriftung, die deine Leute kennen.

Mit `ABOARD_AUTH_METHODS` wählst du, welche Anbieter die Seite anbietet:

- Leer (Voreinstellung) heißt: **alle eingerichteten Wege**, also das Passwort-Formular und jeder
  vollständig eingerichtete Anbieter. Der Weg `password` ist bei eingeschalteter Anmeldung immer
  eingerichtet; ein Server ganz ohne Anbieter startet deshalb auch, nur mit Passwort.
- Sonst trägst du die gewünschten Wege ein, getrennt durch Komma, zum Beispiel
  `ABOARD_AUTH_METHODS=google` oder `password,google`. Der Anbieter aus den drei Variablen
  `ABOARD_OIDC_ISSUER`, `_CLIENT_ID` und `_CLIENT_SECRET` heißt hier `standard`.
- Ein Anbieter, den du nicht nennst, ist ganz abgeschaltet: Seine Knöpfe fehlen, und sein Start und
  sein Rückruf antworten mit `404`.
- Fehlt `password` in der Liste, gibt es kein Passwort-Formular, und `POST /anmeldung/passwort`
  antwortet mit `404`.
- Der Start bricht ab bei einem Wert, der kein eingerichteter Weg ist (Tippfehler), bei doppelten
  Werten und bei einer Liste ohne jeden Weg.
- Auch ein Tippfehler im **Namen** hält den Start an (`ABOARD_AUTH_METHOD` statt
  `ABOARD_AUTH_METHODS`). Sonst böte der Server still alle Anbieter an, auch den, den du
  ausschließen wolltest.
- Bleibt genau ein Weg übrig und ist es ein Anbieter, leitet `/anmeldung` direkt zu ihm weiter.

> **Für Betreiber mit nur einem Anbieter:** Ein unverändert konfigurierter Server zeigt jetzt
> die Anmeldeseite mit Passwort-Formular und Anbieter-Knopf, statt direkt weiterzuleiten. Das ist
> so gewollt: Standardmäßig sind alle Anmeldewege verfügbar. Wer weiterhin nur über den Anbieter
> anmelden will, setzt `ABOARD_AUTH_METHODS=<kürzel>` (bei einem Anbieter aus den alten Variablen
> `standard`); dann leitet `/anmeldung` wieder direkt weiter.

**Passwort-Formular, Schranken:** Nach fünf falschen Eingaben von einem Anschluss in 15 Minuten
ist dieser Anschluss gesperrt (`429`), bis 15 Minuten nach dem ältesten dieser fünf Fehlversuche. Die Adresse des Anschlusses bestimmt dieselbe
Regel wie bei den Agenten: `X-Real-IP` nur von einer Gegenstelle aus `ABOARD_AGENT_PROXY`, sonst die
TCP-Adresse, IPv6 je `/64`. Steht ein Proxy davor und ist `ABOARD_AGENT_PROXY` leer, teilen sich
alle Personen eine Sperre. Ist er eingetragen, **muss der Proxy `X-Real-IP` mit der Client-Adresse
überschreiben** — bei Caddy `header_up X-Real-IP {client_ip}` im `reverse_proxy`-Block (steht vor Caddy ein weiterer
Eingang, gehört der in Caddys `trusted_proxies`; `{remote_host}` wäre dann für alle dieselbe Adresse), bei nginx
`proxy_set_header X-Real-IP $remote_addr;`. Caddy reicht sonst eine vom Browser mitgeschickte
`X-Real-IP` unverändert durch, und wer sie bei jedem Versuch wechselt, umgeht die Sperre. Je eingegebener E-Mail-Adresse gibt es keine Sperre, sondern eine
ansteigende Wartezeit (1, 2, 4, … bis 30 Sekunden je Versuch), damit niemand ein fremdes Konto
dauerhaft aussperren kann. Beides liegt im Speicher und beginnt nach einem Neustart von vorn. Die
Meldung lautet für eine unbekannte Adresse und ein falsches Passwort gleich. Auch die Wartezeit
antwortet mit `429`. Von einem Anschluss, an dem sich die Person in den letzten 30 Tagen vollständig
(mit Code) angemeldet hat, gilt die Wartezeit ihrer Adresse nicht — so kann ein Fremder ein bekanntes
Konto nicht durch Fehlversuche von anderen Anschlüssen aussperren; die Sperre des Anschlusses gilt
trotzdem. Das gilt nur am bekannten Anschluss: Wer mit wechselnder Adresse unterwegs ist (Mobilfunk,
Reise), bekommt die Wartezeit wie alle, und eigene Tippfehler zählen für die eigenen anderen Geräte mit. Auch das liegt im Speicher. Je Anschluss und je Adresse läuft höchstens eine Prüfung
gleichzeitig; eine zweite bekommt sofort `429` „läuft schon eine Prüfung“ (sonst kämen parallele
Anfragen an den Zählern vorbei). Höchstens zwei
Passwort-Prüfungen laufen gleichzeitig (je 64 MiB Speicher); wer länger als fünf Sekunden auf einen
Platz wartet, bekommt `503` und versucht es kurz darauf erneut.

**Zweiter Faktor:** Nach richtigem Passwort setzt der Server ein eigenes Cookie
(`__Host-aboard_zweiter_schritt`, fünf Minuten gültig, nur einmal verwendbar) und fragt nach dem
Code. Je Konto gibt es höchstens eine angefangene Anmeldung; eine neue ersetzt die alte. Ein App-Code gilt genau einmal (auch nicht ein zweites Mal innerhalb seiner 30 Sekunden),
ein Wiederherstellungscode ebenso. Nach fünf falschen Codes beginnt die Anmeldung wieder beim
Passwort; falsche Codes zählen außerdem in die Sperre des Anschlusses und in die Wartezeit der
Adresse. Erst nach dem richtigen Code entsteht die Sitzung; eine mitgebrachte alte Sitzung endet
dabei. Ein Neustart des Servers verwirft angefangene Anmeldungen (dann noch einmal das Passwort).

> **Bekannte Grenze — `ABOARD_SECRET_KEY` wechseln:** Die Geheimnisse der Authenticator-Apps liegen
> mit diesem Schlüssel verschlüsselt in der Datenbank. Einen Weg zum Umschlüsseln gibt es nicht.
> Nach einem Wechsel nimmt der Server keinen App-Code mehr an (Seite „Die
> Zwei-Faktor-Anmeldung dieses Zugangs ist nicht lesbar“). Die Personen
> müssen dann im Admin-Reiter zurückgesetzt und neu eingeladen werden. Ein Wiederherstellungscode
> ist vom Schlüssel unabhängig und würde noch angenommen; verlass dich nicht darauf, sondern setze
> zurück. `ABOARD_SECRET_KEY_PREVIOUS` hilft hier nicht
> (er gilt nur für Board-Backups). Den Schlüssel also nur wechseln, wenn es sein muss.

### Einrichten

Die Grundeinstellungen gelten für jeden Anbieter. Beispiel mit zwei Anbietern:

```yaml
    environment:
      ABOARD_AUTH_MODE: "builtin"
      ABOARD_PUBLIC_BASE_URL: "https://board.example.org"
      ABOARD_SECRET_KEY: "<32 Bytes hex oder base64>"
      ABOARD_BOOTSTRAP_OWNER: "person@example.org"
      ABOARD_OIDC_ALLOWED: "person@example.org, @example.net"
      # Die Anbieter (siehe unten):
      ABOARD_OIDC_PROVIDERS: "google,authelia"
      ABOARD_OIDC_GOOGLE_CLIENT_ID: "<client-id>"
      ABOARD_OIDC_GOOGLE_CLIENT_SECRET: "<client-secret>"
      ABOARD_OIDC_AUTHELIA_ISSUER: "https://auth.example.org"
      ABOARD_OIDC_AUTHELIA_CLIENT_ID: "<client-id>"
      ABOARD_OIDC_AUTHELIA_CLIENT_SECRET: "<client-secret>"
      ABOARD_OIDC_AUTHELIA_LABEL: "Firmen-Konto"
```

- `ABOARD_PUBLIC_BASE_URL` ist Pflicht und muss `https://` sein, ohne Pfad. Aus ihr entsteht
  die Rückruf-Adresse. Sie muss **genau** der beim Anbieter eingetragenen entsprechen.
- `ABOARD_SECRET_KEY` signiert den Anmeldevorgang. Er ist derselbe Schlüssel, der gespeicherte
  Zugangsdaten schützt (siehe [`konfiguration.md`](konfiguration.md)).
- Das Client-Secret ist ein Geheimnis: nicht ins Git, nicht in Chats. Leg es in eine `.env`-Datei mit Rechten `0600`.
- Alle Verbindungen zu den Anbietern laufen über die Ausgangs-Schicht des Servers. Steht
  `ABOARD_AUSGAENGE=aus`, muss `ABOARD_AUSGAENGE_ERLAUBT` die Hosts **jedes** Anbieters enthalten:
  den Host seines Issuers und die Hosts aus dessen Discovery-Dokument
  (`<issuer>/.well-known/openid-configuration`: `token_endpoint`, `jwks_uri`). Sonst bleibt die
  Anmeldung bei diesem Anbieter mit `503` stehen.
- Die Variablen im Einzelnen stehen in [`konfiguration.md`](konfiguration.md).
- Beim Start schreibt der Server für jeden Anbieter eine Zeile ins Log, die auch die Rückruf-Adresse
  nennt, die du beim Anbieter eintragen musst:
  `Anmeldung: OIDC-Anbieter "Firmen-Konto" (https://auth.example.org), Rückruf https://board.example.org/anmeldung/rueckruf/authelia`.

### Anbieter einrichten

Jeder Anbieter braucht beim Anbieter einen OIDC-Client und im Container drei bis vier Variablen.
Als **Rückruf-Adresse** (Redirect-URI) trägst du beim Anbieter ein:
`<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<kürzel>`.

**Google**

1. In der Google Cloud Console einen OAuth-Client vom Typ „Webanwendung“ anlegen.
2. Als autorisierte Weiterleitungs-URI eintragen: `https://board.example.org/anmeldung/rueckruf/google`.
3. Im Container:

   ```yaml
       ABOARD_OIDC_PROVIDERS: "google"
       ABOARD_OIDC_GOOGLE_CLIENT_ID: "<client-id>"
       ABOARD_OIDC_GOOGLE_CLIENT_SECRET: "<client-secret>"
   ```

Für das Kürzel `google` ist der Issuer `https://accounts.google.com` vorbelegt, und der Knopf heißt
„Mit Google anmelden“. Du kannst beides mit `ABOARD_OIDC_GOOGLE_ISSUER` und
`ABOARD_OIDC_GOOGLE_LABEL` überstimmen.

**Authelia** (oder ein anderer OIDC-Anbieter)

Authelia ist für aBoard ein gewöhnlicher Anbieter ohne Sonderbehandlung. Du wählst das Kürzel
selbst, `authelia` ist nur ein Beispiel.

1. In Authelia einen OIDC-Client für aBoard anlegen und als Weiterleitungs-URI eintragen:
   `https://board.example.org/anmeldung/rueckruf/authelia`. Wie das geht, steht in der
   Authelia-Dokumentation zu deiner Version.
2. Im Container:

   ```yaml
       ABOARD_OIDC_PROVIDERS: "authelia"
       ABOARD_OIDC_AUTHELIA_ISSUER: "https://auth.example.org"
       ABOARD_OIDC_AUTHELIA_CLIENT_ID: "<client-id>"
       ABOARD_OIDC_AUTHELIA_CLIENT_SECRET: "<client-secret>"
       ABOARD_OIDC_AUTHELIA_LABEL: "Firmen-Konto"
   ```

Der Issuer muss eine `https://`-Adresse sein. `ABOARD_OIDC_AUTHELIA_LABEL` ist die Beschriftung des
Knopfs („Mit Firmen-Konto anmelden“). Wähl einen Namen, den deine Leute kennen; ohne `_LABEL` stünde
dort der Rechnername `auth.example.org`.

**Mehrere Anbieter:** Trag alle Kürzel in `ABOARD_OIDC_PROVIDERS` ein, getrennt durch Komma, und
setz die Variablen je Kürzel. Die Knöpfe erscheinen in der Reihenfolge dieser Liste; ein Anbieter `standard` steht davor.

**Der bisherige Einzel-Anbieter (`standard`):** Die drei Variablen `ABOARD_OIDC_ISSUER`,
`ABOARD_OIDC_CLIENT_ID` und `ABOARD_OIDC_CLIENT_SECRET` bilden weiter einen Anbieter. Er heißt
intern `standard` und behält die Rückruf-Adresse `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf`
(ohne Kürzel). **Ein bestehender Betrieb muss deshalb weder beim Anbieter noch in der Umgebung
etwas ändern.** Du kannst den alten Anbieter und neue Anbieter mischen. `standard` darfst du nicht
in `ABOARD_OIDC_PROVIDERS` eintragen.

**Kürzel und Variablennamen:**

- Ein Kürzel besteht aus 1 bis 32 Zeichen: Kleinbuchstaben `a–z`, Ziffern `0–9` und `-`.
  `standard` und `password` sind reserviert. Jedes Kürzel darf nur einmal in der Liste stehen.
- Der Teil `<ID>` im Variablennamen ist das Kürzel in Großbuchstaben, mit `_` statt `-`. Das
  Kürzel `heim-2` ergibt `ABOARD_OIDC_HEIM_2_ISSUER`, `ABOARD_OIDC_HEIM_2_CLIENT_ID` und so weiter.
- Das Kürzel steht in der Rückruf-Adresse: `…/anmeldung/rueckruf/heim-2`.
- Benennst du ein Kürzel später um, ändere gleichzeitig die Rückruf-Adresse beim Anbieter.

**Was beim Start abbricht und was nur warnt:**

| Fall | Folge |
|---|---|
| Ein Anbieter ist halb eingerichtet: eine seiner Variablen ist gesetzt, aber `_CLIENT_ID`, `_CLIENT_SECRET` oder (außer bei `google`) `_ISSUER` fehlt | **Abbruch.** Meldung: `Anbieter <kürzel> halb eingerichtet: <Variable> fehlt` |
| Der Issuer ist keine `https://`-Adresse | **Abbruch** |
| Eine Variable `ABOARD_OIDC_<ID>_…` für ein Kürzel, das nicht in `ABOARD_OIDC_PROVIDERS` steht, oder ein Tippfehler im Namen (`ABOARD_OIDC_…` ist unbekannt) | **Abbruch.** Meldung: `ABOARD_OIDC_… ist unbekannt — Tippfehler, oder fehlt das Kürzel in ABOARD_OIDC_PROVIDERS?` |
| Ein ungültiges, reserviertes oder doppeltes Kürzel in `ABOARD_OIDC_PROVIDERS` | **Abbruch** |
| Ein unbekannter Name `ABOARD_AUTH_…` oder `ABOARD_INVITE_…` (bekannt sind `ABOARD_AUTH_MODE`, `ABOARD_AUTH_METHODS` und `ABOARD_INVITE_METHODS`) | **Abbruch.** Meldung: `… ist unbekannt — Tippfehler?` |
| `ABOARD_INVITE_METHODS` nennt einen Weg, den `ABOARD_AUTH_METHODS` nicht anbietet, steht doppelt oder enthält keinen Weg | **Abbruch.** Meldung nennt die angebotenen Wege |
| Zwei Anbieter mit demselben Issuer (etwa `standard` und `google`, beide auf Google) | **Abbruch.** Die Bindung hängt am Aussteller; je Aussteller gibt es nur einen Anbieter |
| Gar kein Anbieter eingerichtet | Start, nur mit dem Passwort-Weg (ist `ABOARD_AUTH_METHODS` gesetzt und nennt kein `password`, **Abbruch**) |
| Ein Wert in `ABOARD_AUTH_METHODS` ist kein eingerichteter Weg, steht doppelt, oder die Liste enthält keinen Weg | **Abbruch** |
| Ein Kürzel steht in `ABOARD_OIDC_PROVIDERS`, hat aber keine einzige eigene Variable | Start, **Warnung** im Log; der Anbieter wird nicht angeboten |
| Ein Anbieter ist beim Start nicht erreichbar | Start; die Anmeldung bei diesem Anbieter antwortet `503`, bis er erreichbar ist (kein Neustart nötig) |

Der Abbruch nennt im Log alle gefundenen Mängel auf einmal.

### Wer kommt herein? Vorab-Liste und Konten

Ob eine Person hereinkommt, entscheidet **ein Tor**. Es lässt herein, wer **auf der Vorab-Liste
steht ODER ein aktives Konto hat**. Der Konto-Zustand geht der Liste aber vor: Ein **gesperrtes Konto
lässt nie herein**, ebenso keines, das **auf Bestätigung wartet** oder **zurückgesetzt** wurde (Zustand
„eingeladen“ mit Rücksetz-Link), auch nicht, wenn die Adresse auf der Vorab-Liste steht. Eine frische
Einladung einer gelisteten Person bleibt durch die Liste gedeckt. Das Tor prüft bei jeder Anfrage, beim
Start und bei der Anmeldung.

Ein Konto hat einen Zustand: **eingeladen** → (Person löst den Link ein) **wartet auf Bestätigung**
→ (Admin bestätigt) **aktiv**; dazu **gesperrt**. Nur „aktiv“ lässt herein.

> **Bindung nur über Einladung oder Vorab-Liste.** Meldet sich jemand über einen Anbieter an, dessen
> Anbieter-Konto noch nirgends gebunden ist, lässt der Server ihn nur herein, wenn seine Adresse auf
> der Vorab-Liste steht; dann wird beim ersten Mal gebunden. Ein aktives Konto allein genügt dafür
> nicht. Sonst käme in ein Konto mit Passwort und zweitem Faktor, wer bei irgendeinem Anbieter
> dieselbe Adresse bestätigt bekommt. Eingeladene Personen bekommen ihre Bindung beim
> Einlösen.
>
> **Vorab-Liste und zweiter Faktor.** Eine Adresse auf der Vorab-Liste kommt über ein noch ungebundenes Anbieter-Konto herein, **auch wenn dieselbe Adresse ein Passwort-Konto mit Zwei-Faktor-Anmeldung hat**:
> Die Vorab-Liste ersetzt die Einladung, und der Anbieter-Weg kennt den zweiten Faktor des
> Passwort-Wegs nicht (gewollt). Wer das nicht will, nimmt die Adresse nicht in
> die Vorab-Liste auf, sondern lädt die Person ein.

**Einladen:** Der Admin gibt nur eine E-Mail-Adresse ein (im Admin-Reiter „User-Verwaltung“ oder über
`POST /admin/einladungen`, bis zu 50 Adressen je Aufruf). Der Server legt ein Konto im Zustand „eingeladen“ an und erzeugt einen
Link `<ABOARD_PUBLIC_BASE_URL>/einladung/<token>`: 7 Tage gültig, nur einmal nutzbar, gespeichert
nur als Hash. Der Admin sieht den Link genau einmal in der Antwort; ist SMTP eingerichtet, geht er
zusätzlich per Mail an die Adresse (unabhängig von `ABOARD_NOTIFICATIONS_DELIVERY`, weil das Einladen
eine ausdrückliche Handlung des Admins ist). Erneut einladen erzeugt einen neuen Link, der alte gilt
dann nicht mehr. Eine Einladung gibt keine Admin-Rolle mit (einzige Ausnahme: der erste Admin, siehe
unten). Einladen geht nur an Adressen aus ASCII-Zeichen, ohne Anzeigenamen. Abgelaufene und benutzte Links sowie
verbrauchte Wiederherstellungscodes löscht der Server 30 Tage nach Ablauf bzw. Nutzung.

**Erster Admin auf einem leeren Server:** Ohne Admin gibt es niemanden, der über die Oberfläche
einlädt. Dafür gibt es den Befehl im Container:

```
docker exec aboard-server aboard-server nutzer einladen --email anna@example.org
```

Er gibt den Link aus (nicht ins Log). Gib den Link nur an die gemeinte Person: Auf einem Server ohne
Admin macht das Einlösen sie ohne weitere Bestätigung zum Admin (mit gesetztem `ABOARD_BOOTSTRAP_OWNER`
nur die Owner-Adresse; jede andere Einladung wartet dann auf Bestätigung). `ABOARD_AUTH_MODE=builtin` und `ABOARD_PUBLIC_BASE_URL`
müssen gesetzt sein, wie beim Server selbst. Gibt es beim Einlösen noch keinen Admin, wird die Person
direkt aktiv und Admin (mit gesetztem `ABOARD_BOOTSTRAP_OWNER` nur, wenn es deren Adresse ist); sonst
bestätigt ein Admin den Zugang.

**Einlösen:** Der Link öffnet eine Seite mit der eingeladenen Adresse und den Wegen aus
`ABOARD_INVITE_METHODS`. Die Person wählt:

- **Passwort und Zwei-Faktor:** Passwort zweimal eingeben (mindestens 12 Zeichen), dann in einer
  Authenticator-App den angezeigten Schlüssel eintragen (oder den Link „In der Authenticator-App
  öffnen“) und den ersten Code eingeben. Erst mit einem richtigen Code ist eingelöst. Danach zeigt
  die Seite **einmal** die zehn Wiederherstellungscodes.
- **Ein Anbieter-Link** („Mit Google anmelden“ usw., `GET /einladung/<token>?weg=<kürzel>`): Die Person meldet sich beim Anbieter an; der
  Server bindet dieses Anbieter-Konto an das eingeladene Konto, **auch wenn der Anbieter eine andere
  Adresse meldet**. Die gemeldete Adresse und den Namen sieht der Admin vor der Bestätigung. Ist das
  Anbieter-Konto hier schon an jemanden gebunden, wird nicht eingelöst; der Link gilt dann weiter.

Der Link gilt nur einmal. Danach **wartet das Konto auf Bestätigung**, weil ein weitergeleiteter
Link sonst einer fremden Person Zugang gäbe. Startet die Person während des Einlösens anderswo eine
Anmeldung, zeigt der Anbieter-Weg „Anmeldevorgang nicht mehr gültig“; dann den Einladungslink noch
einmal öffnen. Die Seiten unter `/einladung/` senden keinen Referer (`Referrer-Policy:
no-referrer`), damit der Link nicht über einen Verweis weitergegeben wird.

**Bestätigen, Ablehnen, Sperren, Zurücksetzen** (im Admin-Reiter „User-Verwaltung“; Schritt für Schritt in
[Server verwalten](administration.md#konten-und-einladungen-eingebaute-anmeldung). Die Pfade, je `POST`):

| Aufruf | Wirkung |
|---|---|
| `/admin/konten/<id>/bestaetigen` | wartet → aktiv. Legt die Person mit persönlichem Board an. |
| `/admin/konten/<id>/ablehnen` | Löscht ein wartendes Konto samt Link und Bindung; dieselbe Person oder dasselbe Anbieter-Konto lässt sich danach neu einladen. Geht nicht bei einer zurückgesetzten Person, die schon Zugang hatte (dann sperren oder Nutzer löschen). |
| `/admin/konten/<id>/sperren` | Gesperrt; alle Sitzungen der Person enden sofort. |
| `/admin/konten/<id>/entsperren` | Zurück nach „wartet auf Bestätigung“ (mit Anmeldedaten) bzw. „eingeladen“ (ohne). Nie direkt aktiv: Danach bestätigst du den Zugang ausdrücklich. |
| `/admin/konten/<id>/zuruecksetzen` | Passwort, Zwei-Faktor, Wiederherstellungscodes und alle Anbieter-Bindungen der Person verfallen, ihre Sitzungen enden, der Zustand wird „eingeladen“. Die Antwort enthält einen neuen Link (per Mail, wenn SMTP eingerichtet ist). Nach dem Einlösen wartet das Konto wieder auf Bestätigung. Geht auch bei einem gesperrten Konto (erst sperren, dann zurücksetzen, z. B. bei einem Verdacht); die Sperre ist danach aufgehoben, Zugang gibt es aber erst nach Einlösen und Bestätigung. |

Das **eigene** Konto kann ein Admin nicht sperren und nicht zurücksetzen. `GET /admin/konten` listet
alle Konten mit Zustand, gewähltem Weg und gemeldeter Adresse. Eine Einladung, die noch niemand
eingelöst hat, zieht der Admin mit `DELETE /admin/einladungen/<id>` zurück („Einladung widerrufen“).

**Gemeldete Adresse prüfen:** Name und Adresse, die ein Anbieter beim Einlösen meldet, kommen ungeprüft
vom Anbieter. Der Admin sieht sie vor dem Bestätigen; weicht die gemeldete von der eingeladenen
Adresse ab, steht „Achtung: weicht von der eingeladenen Adresse ab“ dabei. Das ist erlaubt, aber
ein Grund zum Nachfragen, bevor bestätigt wird.
Meldet der Anbieter die Adresse ohne Bestätigung (`email_verified` fehlt oder ist nicht wahr), steht
„Achtung: Adresse vom Anbieter nicht bestätigt“ dabei (englisch „Warning: address not verified by the
provider“), und die Zeile ist hervorgehoben — auch wenn die Adresse der eingeladenen gleicht, denn ein
Anbieter mit ungeprüften Adressen kann jede Adresse behaupten. Weicht sie zusätzlich ab, stehen beide
Hinweise da. In `GET /admin/konten` steht das als eigenes Feld `gemeldete_email_unbestaetigt`; die
Adresse selbst trägt keinen Zusatz (neu seit v1.63.0).

Die **Vorab-Liste** steht in `ABOARD_OIDC_ALLOWED` (bisheriger Name: `ABOARD_OIDC_ZUGELASSEN`,
gleichbedeutend). Stehen beide mit verschiedenem Inhalt da, bricht der Start ab. Eingetragen werden
einzelne Adressen und ganze Domains (`@example.net`), getrennt durch Komma, Semikolon oder
Leerraum. Dazu kommt immer die Adresse aus `ABOARD_BOOTSTRAP_OWNER`.

> 🛑 **Der Status deines OAuth-Clients beim Anbieter ist keine Schranke.** Laut Google-Hilfe
> „Manage App Audience“ (support.google.com/cloud/answer/15549945, gelesen am 02.11.2026) lässt auch ein Client im Status „Testing“ jedes Google-Konto zu, wenn er nur
> die Bereiche `openid`, `email` und `profile` anfordert. Verlass dich nicht auf eine Testnutzer-Liste.

> ⚠️ **Ein Domain-Eintrag (`@example.net`) lässt jedes Konto mit bestätigter Adresse dieser Domain
> herein** — auch ein privates Google-Konto, das jemand mit seiner Firmenadresse angelegt hat und das
> nach seinem Ausscheiden weiterbesteht. Wer das ausschließen will, trägt Einzeladressen ein.
>
> **Einmal gebunden, bleibt drin:** Das Tor prüft die Adresse, die die Person **in aBoard** hat,
> nicht die, die der Anbieter heute meldet ([Bindung](#ein-konto-eine-adresse-bindung-an-den-anbieter)).
> Stellt jemand sein gebundenes Anbieter-Konto später auf eine Adresse außerhalb der Domain um, kommt
> er weiter herein. Einen Domain-Eintrag kann man für eine einzelne Person nicht streichen. Den Zugang
> entziehst du dann, indem du die Person mit `aboard-server nutzer umziehen` auf eine Adresse
> außerhalb der Vorab-Liste umziehst ([Umzug](#konto-auf-eine-andere-adresse-umziehen)): Der Umzug beendet
> ihre Sitzungen sofort, und das Tor lässt sie unter der neuen Adresse nicht mehr herein; ihre
> Boards bleiben. Ein Neustart ist dafür nicht nötig. Der härtere Weg ist das Löschen der Person (Admin-Dialog, Reiter „User-Verwaltung“, siehe
> [Administration](administration.md#personen-löschen-datenschutz)).

Adressen mit Zeichen außerhalb von ASCII (etwa Umlaute in der Adresse) nimmt aBoard weder in der
Liste noch bei der Anmeldung an: Beim Kleinschreiben würden sonst verschiedene Adressen gleich.

Das heißt für den Betrieb:

- **Zugang erteilen:** Adresse in `ABOARD_OIDC_ALLOWED` eintragen, Server neu starten.
- **Zugang entziehen:** Adresse aus `ABOARD_OIDC_ALLOWED` streichen, Server neu starten. Danach
  beendet der Server auch laufende Sitzungen dieser Person: Die nächste Anfrage wird auf
  `/anmeldung` weitergeleitet (Seitenaufruf im Browser) oder bekommt `401` (Anfrage der Oberfläche).
  Ihr Konto und ihre Boards bleiben bestehen.
- **Board-Freigaben und Domain-Freigaben öffnen den Server nicht.** Ein Eintrag in der Freigabe
  eines Boards oder in `ABOARD_ACL_DOMAINS` gibt einer Person Rechte auf dem Board, aber keinen
  Zutritt.
- Der Server liest die Liste beim Start. Änderungen wirken erst nach dem Neustart.
- Der Anbieter muss die Adresse als bestätigt melden; sonst gibt es keinen Zugang.
- Ein ungültiger Eintrag hält den Start an. Ist die Liste leer, gibt es keinen
  Bootstrap-Owner und kein aktives Konto, startet der Server mit einer Warnung im Log, und niemand
  kommt herein.

Wer nicht hereinkommt, sieht nach der Anmeldung beim Anbieter die Seite „Kein Zugang“. Es
entsteht kein Konto. Im Log steht höchstens eine Zeile pro Minute mit der abgewiesenen Adresse.

### Ein Konto, eine Adresse: Bindung an den Anbieter

Beim ersten Anmelden merkt sich der Server, welches Anbieter-Konto (Aussteller `iss` und Kennung
`sub`) zu welcher Person gehört. Diese **Bindung** bestimmt von da an die Person, nicht die Adresse,
die der Anbieter meldet:

- **Gebundenes Konto, andere Adresse:** Meldet ein Anbieter-Konto, das schon gebunden ist, später
  eine andere Adresse (der Anbieter hat sie geändert oder liefert einmal einen Alias), bleibt es
  dieselbe Person mit denselben Boards und Rechten. Ein Umzug mit `nutzer umziehen` ist dafür nicht
  nötig. Ob die Person hereinkommt, entscheidet das [Tor](#wer-kommt-herein-vorab-liste-und-konten)
  für ihre Adresse **in aBoard**, nicht für die gemeldete.
- **Zweites Anbieter-Konto mit derselben Adresse:** Es gibt **keine Verknüpfung über die Adresse.**
  Meldet sich mit einem anderen Anbieter-Konto (auch bei einem zweiten Anbieter) dieselbe Adresse an,
  die schon an ein Anbieter-Konto gebunden ist, bekommt sie `403` „Kein Zugang“ („Diese Adresse
  gehört hier zu einem anderen Konto“). Im Log steht, was hilft: ein neues Anbieter-Konto in den Konto-Einstellungen verknüpfen (die Person meldet sich dazu mit dem bisherigen Weg an), das alte mit `nutzer bindung-loesen` lösen (siehe unten) oder bei geänderter Adresse `nutzer umziehen`. Das
  verhindert, dass jemand mit einer übernommenen Adresse die Boards des Vorbesitzers erbt. Heute
  gilt das für jede Person genau einmal: Eine Person hat ein Anbieter-Konto, nicht eines je Anbieter.
- **Mehrere Anbieter: der schwächste zählt.** Beim ersten Login darf **jeder** eingerichtete Anbieter
  jede Adresse der Vorab-Liste binden. Wer zuerst bindet, gewinnt; die echte Person bekommt danach
  „Kein Zugang“. Nimm deshalb nur Anbieter auf, denen du die Bestätigung der Adresse zutraust, und
  beschränke mit `ABOARD_AUTH_METHODS` auf die gewünschten.
- **Kürzel nach dem Umstieg:** Wer von den alten Namen (Anbieter `standard`) auf ein Kürzel wie
  `google` umsteigt, behält alle Bindungen, weil sie am Aussteller hängen. In der Datenbank tragen
  diese Bindungen weiter das Kürzel `standard`. Das ist eine alte Beschriftung aus der Zeit vor den
  Anbieter-Kürzeln; die Bindung gilt je Person und ändert am Anmelden nichts. Im Admin-Reiter steht
  dafür „bisheriger Anbieter“ als Anmeldeweg des Kontos.
- **Erste Anmeldung ohne Bindung:** Gibt es keine Bindung, gilt die vom Anbieter gemeldete, bestätigte
  Adresse. Sie muss durch das Tor, und der erste Login bindet das Anbieter-Konto an diese Person.
- **Wartet auf Bestätigung:** Löst eine eingeladene Person den Link über einen Anbieter ein, gehört die
  Bindung zunächst nur zu ihrem Konto, nicht zu einer Person in aBoard. Meldet sie sich vor der
  Bestätigung an, sieht sie die Seite „Wartet auf Bestätigung“. Mit der Bestätigung des Admins wird
  die Bindung an die Person gehängt.
- **Einladung mit anderer gemeldeter Adresse:** Beim Einlösen bindet der Server das Anbieter-Konto an
  das eingeladene Konto, auch wenn der Anbieter eine andere Adresse meldet. Ist das Anbieter-Konto
  schon an jemanden gebunden, wird nicht eingelöst.

### Bindung an ein Anbieter-Konto lösen

Personen verknüpfen und lösen ihre Anbieter-Konten selbst in den **Konto-Einstellungen** (Menü am Logo). Dort ist auch der Wechsel auf ein neues Google-Konto möglich: neues Konto verknüpfen, es ersetzt das bisherige. Personen **mit Passwort** bestätigen jede Änderung mit Passwort und Code. Wer sich nur über einen Anbieter anmeldet (etwa Personen von der Vorab-Liste), bestätigt mit einer **frischen Anmeldung beim schon gebundenen Anbieter**: aBoard verlangt dort `max_age=0` und ausdrücklich den Claim `auth_time` (`claims`-Parameter), dazu `prompt=login`, wenn der Anbieter es in seinem Discovery-Dokument anbietet (sonst `prompt=select_account`). Angenommen wird nur ein ID-Token mit demselben Anbieter-Konto (`sub`) und einem `auth_time`, das höchstens 5 Minuten alt ist. Personen mit einem Konto, das nicht aktiv ist (eingeladen, wartend, gesperrt), können das nicht. **Der Anbieter muss `auth_time` liefern**, sonst scheitert die Bestätigung, solange `ABOARD_OIDC_REAUTH_FALLBACK` aus ist (Voreinstellung): Die Oberfläche meldet dann „Dein Anmeldedienst bestätigt keine frische Anmeldung“ (Rückkehr `/?konto=nicht_unterstuetzt`), es ändert sich nichts. Mit dem Schalter genügt ein Token, das der Anbieter in genau diesem Vorgang ausgestellt hat ([Konfiguration](konfiguration.md)). Audit-Gründe eines Fehlschlags: `sub` (anderes Anbieter-Konto), `auth_time_alt`, `auth_time_ungueltig` (vorhanden, aber unlesbar; nie der Ersatzweg), `auth_time_fehlt` (Schalter aus), `iat_alt` (Schalter an, Token nicht aus diesem Vorgang); Erfolg ohne `auth_time`: `ok_ohne_auth_time`.

- **Google:** liefert `auth_time` laut Google-Doku nur, wenn es angefordert wird (tut aBoard) **und** in der Google Auth Platform eingeschaltet ist: „Settings“ → „Advanced Settings“ → **„Session age claims“**. Laut Google-Doku (Seite „Sicherheitspaket“, gelesen am 05.11.2026) geht das nur, wenn die App veröffentlicht („In production“) **und** verifiziert („Verified“) ist. **Gemessen am 09.11.2026:** Ein Google-Client ohne eingeschaltete „Session age claims“ liefert kein `auth_time`. Die Bestätigung scheitert dann immer, im Audit mit `konto-frisch-fehlgeschlagen` und `ergebnis=auth_time_fehlt`. Neu anmelden bei Google hilft dagegen nicht. **Abhilfe ohne Verifizierung:** `ABOARD_OIDC_REAUTH_FALLBACK=1` ([Konfiguration](konfiguration.md)). Dann genügt ein Token, das Google in genau diesem Vorgang ausgestellt hat, mit der dort genannten Grenze; das Alter der Google-Sitzung spielt dann keine Rolle. **Ausprobiert am 10.11.2026 auf einem Server (v1.62.0) mit `ABOARD_OIDC_REAUTH_FALLBACK=1`:** „Passwort mit zweitem Faktor anlegen“ gelingt mit Google ohne verifizierte App (Sichtprüfung; die Audit-Zeile wurde dabei nicht gelesen). **Liefert Google `auth_time` (verifizierte App):** Google fragt nicht neu nach dem Passwort, wenn die Person dort angemeldet ist; liegt ihre letzte echte Google-Anmeldung länger als 5 Minuten zurück, scheitert die Bestätigung („nicht anerkannt“). Die Oberfläche rät dann, sich bei Google ab- und wieder anzumelden und es erneut zu versuchen.
- **Authelia:** **nicht gemessen** — vor dem Einsatz einmal ausprobieren; fehlt `auth_time`, hilft `ABOARD_OIDC_REAUTH_FALLBACK=1`, sonst bleibt für diese Personen der Befehl unten. Erfolg und Fehlschlag stehen im Audit (`konto-frisch-bestaetigt`, `konto-frisch-fehlgeschlagen`).

**Eigenes Passwort nachträglich (Schalter `ABOARD_ACCOUNT_SELF_PASSWORD`, aus).** Ist der Schalter an, können sich Personen ohne Passwort nach einer solchen frischen Anmeldung selbst ein Passwort mit Pflicht-2FA anlegen ([Konfiguration](konfiguration.md)). ⚠️ **Folge für die Vorab-Liste:** Danach hat die Person ein **aktives Konto**. Die Adresse aus `ABOARD_OIDC_ALLOWED` zu streichen entzieht ihr den Zugang dann **nicht mehr** — dafür im Reiter „User-Verwaltung“ **Sperren**. Wo die Vorab-Liste die einzige Zugangssteuerung sein soll, bleibt der Schalter aus.

Hat die Person den Zugang zu ihrem Anbieter-Konto **verloren** und kommt deshalb nicht mehr herein, löst du die Bindung:

```bash
docker exec aboard-server aboard-server nutzer bindung-loesen \
  --email person@example.org --anbieter standard
```

- `--anbieter` ist das Kürzel (`standard` für den Anbieter aus `ABOARD_OIDC_ISSUER`, sonst z. B. `google`).
- Boards, Rechte und Konto bleiben unberührt; die Bindung fällt weg, und alle Sitzungen der Person enden. Der Befehl schreibt einen Audit-Eintrag (`personen/<ID>`, Aktion `konto-bindung-geloest-cli`). Anders als in den Konto-Einstellungen prüft der Befehl **nicht**, ob dies der letzte Anmeldeweg ist.
- Steht die Person auf der Vorab-Liste, bindet ihr nächster Anbieter-Login neu (mit dem Konto, das dann die Adresse bestätigt). Ohne Vorab-Eintrag und ohne Passwort kommt sie danach nicht mehr herein: Dann im Admin-Bereich **Zurücksetzen** (neue Einladung).

### Konto auf eine andere Adresse umziehen

Der Befehl `nutzer umziehen` ändert die Adresse, unter der aBoard eine Person führt. Du brauchst ihn,
wenn die Person unter einer anderen Adresse geführt wird, als der Anbieter sie beim ersten Login
meldet. Ein Beispiel: Google liefert nur die Hauptadresse eines Kontos, keinen Alias; wer bisher
unter einer anderen Adresse geführt wurde (etwa über Authelia) und sich noch nie über die eingebaute
Anmeldung angemeldet hat, braucht den Umzug. Bei einem **schon gebundenen** Anbieter-Konto brauchst du
ihn nicht, wenn nur die gemeldete Adresse wechselt (siehe oben). Er behält Boards, Rechte und
Schlüssel:

```bash
docker exec aboard-server aboard-server nutzer umziehen \
  --von alt@example.org --nach neu@example.org --dry-run
```

- `--dry-run` zeigt nur die Zahlen und schreibt nichts. Lass ihn zuerst laufen, dann denselben
  Befehl ohne `--dry-run`.
- Der Befehl schreibt die Adresse in allen Tabellen um, die sie als Schlüssel tragen: Konto,
  Board-Freigaben, Freigabe-Aufträge der Schleuse und KI-Gespräche. Die Anbieter-Bindung hängt
  seit Schema 46 an der Person, nicht an der Adresse, und zieht von selbst mit. Läuft alles in
  einer Transaktion, bricht er bei einem Fehler ohne Änderung ab.
- Sitzungen der alten Adresse werden gelöscht; die Person meldet sich neu an.
- Er bricht ab, wenn die alte Adresse kein Konto hat, wenn die neue schon vergeben ist, oder
  wenn beide Adressen auf demselben Board eine Freigabe haben. Im letzten Fall entfernst du
  zuerst die Freigabe der neuen Adresse (`acl set` oder Board-Freigaben in der Oberfläche).
- **Reihenfolge:** zuerst umziehen, dann die neue Adresse in `ABOARD_OIDC_ALLOWED` aufnehmen
  (die alte streichen) und neu starten. Hat sich die Person schon unter der neuen Adresse
  angemeldet, gibt es dort ein eigenes, meist leeres Konto; dann bricht der Umzug ab. Lösch das
  leere Konto im Admin-Bereich und zieh danach um. Zusammengeführt wird nicht.
- Rückweg: derselbe Befehl mit vertauschten Adressen.
- Er gilt in jeder Betriebsart, auch bei Forward-Auth.
- Das Protokoll der Aktivitäten, Kommentar-Autoren und Adressen im Inhalt der Boards bleiben
  unverändert.
- Steht die alte Adresse in `ABOARD_BOOTSTRAP_OWNER`, stell auch diese Variable auf die neue um.
  Sonst bleibt die alte Adresse zugelassen, und der Server trägt sie bei jedem Start wieder als
  Owner des Service-Boards ein.
- Die Datenbank liegt bei `ABOARD_DB`; mit `--db <pfad>` zeigst du auf eine andere Datei.

### Update auf diese Version

Diese Version bringt Schema 46 (Migration 0046). Sie baut die Tabelle der Anbieter-Bindungen um und
legt Tabellen für Konten und Einladungen an. Sie füllen sich erst, wenn jemand einlädt.

- **Sichere die Datenbank vorher.** Ein Rückweg auf die Vorversion ist nur über diese Sicherung
  möglich: Die Vorversion findet die alte Bindungstabelle nicht mehr.
- **An der Umgebung musst du nichts ändern.** Die bisherigen Namen (`ABOARD_ANMELDUNG`,
  `ABOARD_OIDC_ISSUER`, `ABOARD_OIDC_ZUGELASSEN`, …) gelten weiter, und die Rückruf-Adresse des
  bisherigen Anbieters bleibt `/anmeldung/rueckruf`.
- Bestehende Bindungen werden übernommen (als Anbieter `standard`). Eine Bindung, zu der es keine
  Person im Board gibt (Anmeldung abgeschlossen, aber nie ein Board geöffnet), entfällt; die Person
  bindet sich beim nächsten Login neu. Dafür muss die Adresse wie bisher auf der Vorab-Liste stehen.
  Vor dem Update kannst du zählen, ob das bei dir vorkommt (Werkzeug `sqlite3` auf einer **Kopie**
  der Datenbank): `SELECT count(*) FROM oidc_bindungen b WHERE NOT EXISTS (SELECT 1 FROM users u
  WHERE lower(u.email) = lower(b.email));` — `0` heißt: keine Bindung fällt mangels Person weg. Gibt es
  eine Person doppelt in zwei Schreibweisen (`SELECT lower(email), count(*) FROM users GROUP BY 1
  HAVING count(*) > 1;`), hängt die Bindung danach an der älteren Zeile.
- Für bestehende Personen entstehen keine Konten. Ihr Zugang hängt weiter allein an der Vorab-Liste.
- Eine Anmeldung, die **vor dem Update begonnen** und erst danach zurückgekehrt ist, endet mit
  „Anmeldung abgelaufen“. Die Person meldet sich einfach neu an. Bestehende Sitzungen bleiben
  gültig.

### Probleme bei der Anmeldung

| Beobachtung | Ursache | Abhilfe |
|---|---|---|
| Start bricht ab, Log beginnt mit „Eingebaute Anmeldung (ABOARD_AUTH_MODE=builtin / ABOARD_ANMELDUNG=oidc) nicht startbar“ | Pflichtvariable fehlt oder ist ungültig | die aufgelisteten Punkte beheben; die Meldung nennt alle |
| Start bricht ab: „… gesetzt, aber die eingebaute Anmeldung ist aus“ (nennt die Variablen) | `ABOARD_OIDC_*`, `ABOARD_AUTH_*` oder `ABOARD_INVITE_*` gesetzt, Schalter vergessen — der Server liefe sonst ganz ohne Anmeldung | `ABOARD_AUTH_MODE=builtin` setzen, oder die Variablen entfernen |
| Start bricht ab: „ABOARD_AUTH_MODE=… ungültig“ oder „ABOARD_ANMELDUNG=… ungültig“ | ein Schalter hat einen anderen Wert als `builtin`, `oidc` oder leer | den Wert berichtigen |
| Start bricht ab: „Anbieter … halb eingerichtet: … fehlt“ | von den Variablen eines Anbieters fehlt eine | die genannte Variable setzen, oder alle Variablen dieses Anbieters entfernen |
| Start bricht ab: „ABOARD_OIDC_… ist unbekannt — Tippfehler, oder fehlt das Kürzel in ABOARD_OIDC_PROVIDERS?“ | Tippfehler im Namen, oder die Variablen eines Anbieters ohne sein Kürzel in `ABOARD_OIDC_PROVIDERS` | Namen berichtigen oder das Kürzel in die Liste aufnehmen |
| Start bricht ab: „ABOARD_AUTH_METHODS: … ist kein eingerichteter Anmeldeweg“ | Tippfehler in der Wegeliste, oder der Anbieter ist nicht (vollständig) eingerichtet | Kürzel prüfen; die Meldung nennt die eingerichteten Wege |
| Anmeldeseite zeigt plötzlich ein Passwort-Formular statt direkt zum Anbieter zu leiten | seit eigene Konten möglich sind, ist `password` ein Weg, und leeres `ABOARD_AUTH_METHODS` heißt „alle Wege“ | `ABOARD_AUTH_METHODS=<kürzel>` setzen (siehe oben) |
| Passwort-Formular: „Zu viele Fehlversuche von diesem Anschluss“ (`429`) | fünf falsche Eingaben in 15 Minuten von diesem Anschluss; hinter einem Proxy ohne `ABOARD_AGENT_PROXY` zählen alle Personen gemeinsam (der Start warnt dann) | warten (die Seite nennt die Minuten); `ABOARD_AGENT_PROXY` auf den Proxy setzen |
| Seite „Kein Zugang“ nach der Anmeldung | Adresse steht nicht auf der Vorab-Liste, Anbieter meldet sie nicht als bestätigt, das Konto ist gesperrt, oder die Adresse gehört schon zu einem anderen Anbieter-Konto | Log lesen; Adresse eintragen und neu starten, oder `nutzer umziehen` |
| `503` bei `/anmeldung/start` für einen Anbieter | dieser Anbieter ist nicht erreichbar | Netz und `ABOARD_AUSGAENGE_ERLAUBT` prüfen; der Server versucht es selbst erneut; die anderen Anbieter laufen weiter |
| `404` „Unbekannter Anmeldedienst“ beim Rückruf oder beim Klick | das Kürzel in der Rückruf-Adresse beim Anbieter ist falsch geschrieben, der Anbieter ist nicht eingerichtet, oder `ABOARD_AUTH_METHODS` schließt ihn aus | Rückruf-Adresse beim Anbieter mit der Zeile im Server-Log vergleichen; `ABOARD_AUTH_METHODS` prüfen |
| „Anmeldung abgelaufen“ | der Anmeldevorgang ist älter als 10 Minuten, das Cookie fehlt, er begann vor einem Update, oder in einem anderen Tab wurde inzwischen eine zweite Anmeldung begonnen (es gilt immer nur der letzte Vorgang) | einfach neu anmelden, in einem Tab |
| Anbieter meldet „redirect_uri_mismatch“ | Rückruf-Adresse beim Anbieter weicht von `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<kürzel>` ab (beim Anbieter `standard` ohne Kürzel) | beim Anbieter genau diese Adresse eintragen |
| Immer wieder `401` trotz Anmeldung | Sitzung abgelaufen, Adresse inzwischen gestrichen, oder der Browser nimmt den Sitzungs-Cookie nicht an (er verlangt https) | neu anmelden; Zulassung prüfen; Board über `https://` öffnen |

---

## Wer darf aBoard überhaupt nutzen?

> Gilt für Forward-Auth. Bei der eingebauten Anmeldung entscheidet das Tor aus Vorab-Liste und
> Konten, siehe
> [Wer kommt herein?](#wer-kommt-herein-vorab-liste-und-konten).

**Im Forward-Auth-Betrieb ist der Anmeldedienst die einzige Zugangsschranke.** Jede Person, die Authelia erfolgreich
anmeldet und für die eine E-Mail-Adresse mitkommt, ist in aBoard ein vollwertiger Nutzer. Es
gibt keine zusätzliche Einladung in aBoard.

Beim **ersten** Board-Zugriff einer neuen E-Mail-Adresse legt der Server automatisch an:

- ein Konto für diese Adresse,
- ein leeres **persönliches Board** mit dem Titel „Board“,
- einen Owner-Eintrag dieser Person für das persönliche Board.

Danach sieht die Person ihr persönliches Board und alle Boards, die ihr jemand freigegeben hat.
Löscht sie ihr persönliches Board später, wird es nicht neu angelegt.

Daraus folgt für den Betrieb:

- **Begrenz den Nutzerkreis im Anmeldedienst.** Wer nur bestimmte Personen hereinlassen
  will, regelt das dort — aBoard wertet `Remote-Groups` nicht aus. Beispiel für Authelia, das
  nur Mitglieder der Gruppe `aboard-nutzer` zulässt:

  ```yaml
  access_control:
    default_policy: deny
    rules:
      - domain: board.example.org
        policy: two_factor
        subject:
          - "group:aboard-nutzer"
  ```

  Offene Selbstregistrierung im Anmeldedienst bedeutet offenen Zugang zu aBoard.
- **Keine `bypass`-Regel für die Board-Adresse.** Eine Regel, die Anfragen ohne Anmeldung
  durchwinkt, liefert keine Identität; aBoard antwortet dann mit `401`. Ausnahme ist nur der
  oben genannte Kalender-Pfad, den du im Proxy getrennt behandelst.
- **E-Mail-Adressen stabil halten.** Die E-Mail-Adresse ist der Schlüssel für Konto, Freigaben
  und Rollen. Ändert sich die Adresse einer Person im Anmeldedienst, ist sie für aBoard eine
  **neue** Person: Sie bekommt ein neues, leeres persönliches Board, und Freigaben an die alte
  Adresse greifen nicht mehr.
- **Einmalige Besucher hinterlassen Spuren.** Wer sich nur einmal anmeldet, hinterlässt ein Konto
  und ein leeres Board. Aufräumen können Administratoren in der Server-Administration (siehe
  [`administration.md`](administration.md)).
- Adressen, die mit `@` beginnen, nimmt der Server nicht als Identität an (`403`) — diese Form ist
  für Domain-Freigaben reserviert (siehe [unten](#freigabe-für-eine-ganze-e-mail-domain)).


---

## Rollen: Owner, Editor, Betrachter

Rechte gelten **je Board**. Eine Person kann auf einem Board Owner und auf einem anderen
Betrachter sein. Die Rangfolge ist Betrachter < Editor < Owner.

| Rolle | Board und Karten lesen | Karten anlegen, ändern, verschieben; Anhänge hochladen | Freigaben sehen und verwalten | Board löschen |
|---|---|---|---|---|
| **Owner** | ✓ | ✓ | ✓ | ✓ |
| **Editor** | ✓ | ✓ | — | — |
| **Betrachter** (`viewer`) | ✓ | — | — | — |

Der Server prüft die Rolle bei **jeder** Anfrage an ein Board, auch bei Anhängen und beim
Live-Aktualisierungsstrom. Versucht eine Person etwas, das ihre Rolle nicht erlaubt, antwortet
er mit `403`. Hat sie auf einem Board gar keine Rolle, ebenfalls `403` — das Board taucht dann
auch nicht in ihrer Board-Liste auf.

Wer ein neues Board anlegt, wird dessen Owner. Ein Board hat immer mindestens einen Owner: Den
letzten Owner herabzustufen oder zu entfernen, lehnt der Server ab (`409`).

Getrennt davon gibt es die Eigenschaft **Administrator** für die Server-Administration (Einstellungen,
Nutzerliste, Agenten-Tokens, Sicherungen). Sie ist keine Board-Rolle: Ein Administrator steht dadurch
in keiner Freigabeliste und sieht fremde Boards nicht in seiner eigenen Board-Liste. **Mittelbar
erreicht er aber jedes Board:** Er sieht die Liste aller Boards des Servers, kann für jedes Board ein
Agenten-Token mit Schreib- oder Leserecht anlegen und damit dessen Inhalt lesen und ändern, und er kann
die Sicherungen jedes Boards herunterladen und zurückspielen. Administrator ist deshalb eine Rolle
für vertrauenswürdige Personen; vergib sie nur an Menschen, die alle Daten auf dem Server sehen dürfen.
Siehe [`administration.md`](administration.md).

---

## Boards freigeben

### In der Oberfläche

Owner geben ein Board in den **Board-Einstellungen** im Abschnitt **Freigabe** frei:

1. Person aus der Liste wählen.
2. Rolle wählen (Owner, Editor oder Betrachter).
3. **Hinzufügen** klicken.

Freigaben wirken **sofort** — „Abbrechen“ im Einstellungsdialog nimmt sie nicht zurück. Über
**Entfernen** neben einem Eintrag nimmst du eine Freigabe zurück. Editoren und Betrachter
sehen in diesem Abschnitt nur ihre eigene Rolle.

In der Auswahlliste stehen Personen, die sich **mindestens einmal angemeldet** haben (Status
„aktiv“), sowie Adressen, die bereits auf irgendeinem Board freigegeben sind, sich aber noch
nicht angemeldet haben (Status „ausstehend“). Eine völlig neue Adresse lässt sich in der
Oberfläche nicht eintippen: Bitte die Person, sich einmal anzumelden — danach erscheint sie
in der Liste.

### Über die Programmierschnittstelle

Über die REST-Schnittstelle kann ein Owner eine Freigabe auch für eine Adresse setzen, die sich
noch nie angemeldet hat. Sie greift, sobald sich die Person zum ersten Mal anmeldet. Die Pfade
`GET`, `POST` und `DELETE` auf `/boards/{id}/acl` beschreibt [`api.md`](api.md).

### Freigabe für eine ganze E-Mail-Domain

Statt jede Person einzeln einzutragen, kann ein Owner ein Board **allen Adressen einer Domain**
freigeben — etwa allen mit `@example.org`. Voraussetzung ist, dass der Betreiber diese Domain
ausdrücklich erlaubt:

```yaml
    environment:
      ABOARD_ACL_DOMAINS: "example.org,example.net"
```

- Kommagetrennte Liste; Groß-/Kleinschreibung und ein führendes `@` sind egal.
- Es zählt nur die **exakte** Domain: `example.org` erlaubt **nicht** `team.example.org`.
- Ist die Liste leer oder nicht gesetzt, sind keine Domain-Freigaben möglich.

Ist eine Domain erlaubt, erscheint in der Auswahlliste der Freigabe ein Eintrag „Alle mit
@example.org“. Für Domain-Freigaben gelten diese Regeln:

- Eine Domain kann **höchstens Editor** sein, nie Owner.
- Ein **persönlicher Eintrag geht immer vor**, auch wenn er schwächer ist. Ist eine Person
  persönlich als Betrachter eingetragen und ihre Domain als Editor, bleibt sie Betrachter.
  Entfernt der Owner den persönlichen Eintrag, gilt wieder die Domain-Freigabe.
- Die Erlaubnis wird bei **jeder** Anfrage geprüft. Streichst du eine Domain aus
  `ABOARD_ACL_DOMAINS`, verlieren alle Domain-Freigaben dafür nach dem Neustart ihre Wirkung; in
  der Freigabeliste erscheinen sie als „inaktiv“. Nimmst du die Domain wieder auf, wirken sie
  wieder.
- Das Board für den Mail-Eingang (Service-Board) lässt sich nicht per Domain freigeben.


---

## Der erste Owner und der erste Administrator

### Bootstrap-Owner (`ABOARD_BOOTSTRAP_OWNER`)

Startet der Server mit einer **leeren** Datenbank, legt er ein erstes Board mit der Kennung
`default` an. Im Mehrnutzer-Betrieb hätte dieses Board ohne weiteres Zutun keinen Owner — und
wäre damit für niemanden sichtbar.

Mit `ABOARD_BOOTSTRAP_OWNER` trägst du beim Anlegen eine E-Mail-Adresse als Owner dieses Boards
ein:

```yaml
    environment:
      ABOARD_BOOTSTRAP_OWNER: "person@example.org"
```

Im Log erscheint dann:

```
aboard-server: leere DB erkannt — kanonisches Default-Board angelegt (id=default)
aboard-server: Bootstrap-Owner eingetragen: "person@example.org" → owner auf Board 'default'
```

Wichtig:

- Für das `default`-Board wirkt die Variable **nur beim allerersten Start mit leerer
  Datenbank**. Existiert schon irgendein Board, lässt der Server das `default`-Board unberührt —
  auch nach einem Neustart, auch wenn du die Freigaben inzwischen geändert hast. Bestehende Boards
  übernimmst du wie im [Umstieg](#umstieg-vom-einzelnutzer--auf-den-mehrnutzer-betrieb)
  beschrieben.
- **Anders beim Service-Board** für den Mail-Eingang (siehe [`administration.md`](administration.md)):
  Ist es eingeschaltet (Voreinstellung), trägt der Server die Adresse bei **jedem** Start als
  dessen Owner ein, solange die Variable gesetzt ist. Wer diese Person dort später herabstuft oder
  entfernt, findet sie nach dem nächsten Neustart wieder als Owner vor. **Entferne die Variable
  deshalb nach der Einrichtung aus der Konfiguration.**
- Die Adresse wird genauso normalisiert wie die vom Anmeldedienst (Kleinschreibung, ohne
  Leerzeichen). Sie muss der Adresse entsprechen, die Authelia später in `Remote-Email` liefert.
- Ein Wert, der keine Personenadresse ist (ohne `@`, oder ein Domain-Eintrag wie `@firma.de`),
  wird weder am `default`-Board noch am Service-Board eingetragen; der Server startet trotzdem
  und meldet den Fehler im Log.
- Die Person bekommt bei ihrer ersten Anmeldung **zusätzlich** ihr eigenes persönliches Board.
  Sie sieht also zwei Boards: `default` und ihr persönliches.

Ohne `ABOARD_BOOTSTRAP_OWNER` ist der Mehrnutzer-Betrieb trotzdem nutzbar — jede Person arbeitet
dann in ihrem persönlichen Board, und das Board `default` bleibt unbenutzt.

### Der erste Administrator

Solange es noch keinen Administrator gibt, gilt:

- **Ist `ABOARD_BOOTSTRAP_OWNER` gesetzt, wird nur diese Person Administrator** — bei ihrer ersten
  Anmeldung, egal wer vor ihr kommt. Andere Personen bekommen in dieser Zeit ein normales Konto;
  im Log steht dann, dass der Admin-Platz auf die gesetzte Adresse wartet.
- Hat die Person schon ein Konto (die Variable wurde erst später gesetzt oder berichtigt), macht
  der Server sie **beim nächsten Start** zum Administrator.
- **Ist die Variable gesetzt, aber ungültig** (ohne `@`, oder ein Domain-Eintrag wie `@firma.de`),
  wird **niemand** automatisch Administrator; das Log meldet es beim Serverstart und bei jeder
  ersten Anmeldung einer neuen Person. Wert berichtigen und neu starten.
- **Eingebaute Anmeldung ohne Admin:** Wer den Link einer Einladung einlöst, solange es noch keinen
  Admin gibt, ist sofort aktiv und Admin; ein Admin muss ihn nicht bestätigen. Auf einem leeren
  Server lädt man deshalb mit `aboard-server nutzer einladen --email …` ein (siehe
  [Wer kommt herein?](#wer-kommt-herein-vorab-liste-und-konten)). Ist
  `ABOARD_BOOTSTRAP_OWNER` gesetzt, wird nur diese Person ohne Bestätigung aktiv und Administrator;
  jede andere Einladung wartet auf die Bestätigung durch den Admin, auch solange es noch keinen gibt.
  Ist der Wert ungültig, wird niemand ohne Bestätigung aktiv: Wert berichtigen und neu starten.
- **Ohne die Variable** wird Administrator, wessen Konto der Server als erstes anlegt. Ein Konto entsteht beim ersten Board-Zugriff einer neuen E-Mail-Adresse (siehe
  [Wer darf aBoard überhaupt nutzen?](#wer-darf-aboard-überhaupt-nutzen)). Melde dich dann direkt
  nach der Einrichtung als die Person an, die den Server verwalten soll, bevor du den Zugang für
  andere öffnest.

Gibt es schon einen Administrator, ändert die Variable daran nichts. Weitere Administratoren
ernennst du in der Server-Administration ([`administration.md`](administration.md)).

---

## Umstieg vom Einzelnutzer- auf den Mehrnutzer-Betrieb

Boards, die im Einzelnutzer-Betrieb entstanden sind, haben **keine Owner-Einträge** — im
Einzelnutzer-Betrieb gibt es keine Personen. Schaltest du nur den Mehrnutzer-Betrieb ein, sieht
niemand diese Boards. `ABOARD_BOOTSTRAP_OWNER` teilt sie **nicht** zu, weil die Datenbank nicht
leer ist. Der Server meldet sie im Mehrnutzer-Betrieb beim Start
(`HINWEIS: N Board(s) ohne Owner`), teilt sie aber nicht von selbst zu — ein Board ohne Owner kann
auch gewollt verborgen sein.

**Am einfachsten mit dem Befehl `acl`** im Server-Container (geht in beiden Betriebsarten, auch
nachträglich):

```bash
aboard-server acl orphans                               # Boards ohne Owner auflisten
aboard-server acl adopt --email person@example.org      # Owner aller Boards ohne Owner
aboard-server acl set --board <id> --email person@example.org --role editor   # einzeln
```

`adopt` fasst Boards, die schon einen Owner haben, nicht an, und lässt das Service-Board für den
Mail-Eingang aus (das bekommt seinen Owner über `ABOARD_BOOTSTRAP_OWNER` oder gezielt mit `set`).
`set` stuft den einzigen Owner eines Boards nicht herab — erst einen zweiten Owner setzen. Ein
Domain-Eintrag (`@firma.de`) ist keine gültige Adresse. Der Befehl legt keine Datenbank an: Ist der
Pfad (`--db` bzw. `ABOARD_DB`) falsch, bricht er ab.

**Alternativ über die HTTP-API**, solange der Server noch im Einzelnutzer-Betrieb läuft. In
dieser Betriebsart nimmt der Server Freigaben ohne Anmeldung entgegen:

```bash
# 1. Board-Kennungen ermitteln (im Einzelnutzer-Betrieb liefert /boards alle Boards)
curl -s http://127.0.0.1:8080/boards

# 2. Für jedes Board den künftigen Owner eintragen
curl -s -X POST http://127.0.0.1:8080/boards/default/acl \
  -H 'Content-Type: application/json' \
  -d '{"email":"person@example.org","role":"owner"}'
```

Danach:

3. Secret erzeugen und in aBoard **und** im Proxy hinterlegen.
4. Lauschadresse auf Unix-Socket oder Loopback umstellen (oder bewusst
   `ABOARD_FORWARD_AUTH_INSECURE_BIND=1` setzen).
5. `ABOARD_TRUST_FORWARD_AUTH=1` setzen und neu starten.

Die Daten selbst bleiben beim Umschalten unverändert; die Tabellen für Konten und Freigaben
existieren in jeder Datenbank, auch wenn sie im Einzelnutzer-Betrieb leer bleiben.

---

## Lokal testen ohne Anmeldedienst

### Am einfachsten: Einzelnutzer-Betrieb

Für das reine Ausprobieren der Oberfläche genügt der Einzelnutzer-Betrieb auf dem eigenen
Rechner (siehe [oben](#einzelnutzer-betrieb)).

### Mehrnutzer-Verhalten simulieren

Um Rollen und Freigaben ohne Authelia zu testen, kann der Server die Identitäts-Header selbst
einspielen. Er spielt dann den Proxy: Er entfernt alle vom Client mitgeschickten
`Remote-*`-Header, setzt die konfigurierten Werte und schickt das Secret selbst mit.

```bash
docker run --rm \
  --name aboard-test \
  -p 127.0.0.1:8080:8080 \
  -v aboard-test-db:/data/db \
  -e ABOARD_TRUST_FORWARD_AUTH=1 \
  -e ABOARD_FORWARD_AUTH_SECRET=<geheimnis> \
  -e ABOARD_FORWARD_AUTH_INSECURE_BIND=1 \
  -e ABOARD_DEV_FAKE_USER=alice \
  -e ABOARD_DEV_FAKE_EMAIL=alice@example.org \
  -e ABOARD_DEV_FAKE_NAME="Alice Beispiel" \
  -e ABOARD_BOOTSTRAP_OWNER=alice@example.org \
  registry.example.org/aboard:<version>
```

Danach unter `http://127.0.0.1:8080/` öffnen — jede Anfrage gilt als Anfrage von
`alice@example.org`.

- `ABOARD_DEV_FAKE_USER` schaltet die Simulation ein; sie wirkt **nur** zusammen mit
  `ABOARD_TRUST_FORWARD_AUTH`. Beim Start erscheint eine deutliche Warnung im Log.
- Setz auch `ABOARD_DEV_FAKE_EMAIL` — ohne E-Mail-Adresse verweigert der Server jeden
  Board-Zugriff mit `403`. `ABOARD_DEV_FAKE_GROUPS` und `ABOARD_DEV_FAKE_NAME` sind optional.
- Das Secret ist auch hier Pflicht. `ABOARD_FORWARD_AUTH_INSECURE_BIND=1` ist nötig, weil der
  Server im Container auf `:8080` lauscht; der Port ist durch `-p 127.0.0.1:…` trotzdem nur
  vom eigenen Rechner aus erreichbar.
- Um eine zweite Person zu simulieren, starte den Container mit anderen
  `ABOARD_DEV_FAKE_*`-Werten neu (dieselbe Datenbank).

> 🛑 **Die Simulation darf niemals in einem erreichbaren Server aktiv sein.** Mit gesetztem
> `ABOARD_DEV_FAKE_USER` gilt **jede** Anfrage als die konfigurierte Person, egal wer sie stellt.

### Mit `curl` gegen einen Server ohne Proxy

Lauscht ein Testserver im Mehrnutzer-Betrieb auf `127.0.0.1:8080`, kannst du den Proxy auch
von Hand nachstellen:

```bash
curl -s http://127.0.0.1:8080/boards \
  -H "X-Aboard-Forward-Auth-Secret: $ABOARD_FORWARD_AUTH_SECRET" \
  -H "Remote-Email: alice@example.org"
```

Ohne den Secret-Header antwortet der Server mit `401`.

---

## Fehlersuche

### Der Server startet nicht

| Meldung im Log (gekürzt) | Ursache | Abhilfe |
|---|---|---|
| `unsicherer Forward-Auth-Bind: ABOARD_TRUST_FORWARD_AUTH ist aktiv, aber der Server bindet auf ":8080" …` | Mehrnutzer-Betrieb mit einer Lauschadresse auf allen Schnittstellen | `ABOARD_UNIX_SOCKET` oder `ABOARD_ADDR=127.0.0.1:8080` setzen — oder, wenn das Netz anders abgeschottet ist, `ABOARD_FORWARD_AUTH_INSECURE_BIND=1` |
| `Forward-Auth ohne Secret: ABOARD_TRUST_FORWARD_AUTH ist aktiv, aber ABOARD_FORWARD_AUTH_SECRET ist nicht gesetzt …` | Secret fehlt oder besteht nur aus Leerzeichen | Secret erzeugen und setzen (siehe [oben](#das-gemeinsame-secret)) |
| `ABOARD_UNIX_SOCKET_MODE="…" ungültig — erlaubt sind 0600, 0660 und 0666` | Tippfehler beim Socket-Modus | einen der drei Werte verwenden |
| `Eingebaute Anmeldung (ABOARD_AUTH_MODE=builtin / ABOARD_ANMELDUNG=oidc) nicht startbar: …` | Mängel der eingebauten Anmeldung (halb eingerichteter Anbieter, Tippfehler im Namen oder Kürzel, falsche `ABOARD_AUTH_METHODS`, fehlender Schlüssel …) | die aufgelisteten Punkte beheben; Tabelle unter [Probleme bei der Anmeldung](#probleme-bei-der-anmeldung) |

### Jede Anfrage endet mit `401`

Antwort: `unauthorized: forward-auth identity required`. Der Server hat entweder kein passendes
Secret oder keine Identität bekommen. Prüf in dieser Reihenfolge:

1. **Secret:** Schickt der Proxy `X-Aboard-Forward-Auth-Secret` mit, und ist der Wert
   **identisch** mit `ABOARD_FORWARD_AUTH_SECRET` im aBoard-Container? Steht die Variable in der
   Umgebung des **Caddy**-Containers, und verwendet das Caddyfile `{$…}` statt `{env.…}`? Nach
   einer Änderung beide Container neu starten.
2. **Identität:** Übernimmt der Proxy die Header aus der Antwort des Anmeldedienstes
   (`copy_headers Remote-User Remote-Email …` in `forward_auth`)? Liefert der Anmeldedienst für
   diese Adresse überhaupt eine Identität (keine `bypass`-Regel)?
3. **Log lesen:** Bei abgewiesenen Anfragen schreibt der Server höchstens einmal pro Minute eine
   Zeile mit dem Grund — `Secret fehlt oder ist falsch` oder `keine Identität`:

   ```
   aboard-server: forward-auth: Anfragen werden mit 401 abgewiesen (Secret fehlt oder ist falsch) — Proxy-Konfiguration prüfen …
   ```

   Mit `ABOARD_FORWARD_AUTH_VERBOSE=1` erscheint stattdessen eine Zeile **pro** Anfrage — bei
   abgewiesenen Anfragen mit Pfad und Grund, bei angenommenen mit der übermittelten Identität.
   Nur zur Fehlersuche einschalten; jeder offene Browser-Tab erzeugt laufend Anfragen.

Die Gesundheitsprüfung (`/healthz`) bleibt dabei grün, weil sie keine Anmeldung braucht. Ein
grüner Healthcheck beweist also nicht, dass die Anmeldung funktioniert.

### `403` beim Zugriff auf Boards

| Antwort | Ursache | Abhilfe |
|---|---|---|
| `forbidden: no identity email` | Es kommt `Remote-User`, aber keine `Remote-Email` an | `Remote-Email` in `copy_headers` aufnehmen; prüfen, ob der Anmeldedienst für diese Person eine E-Mail-Adresse kennt |
| `forbidden: invalid identity email` | Die übermittelte Adresse beginnt mit `@` | Konfiguration des Anmeldedienstes prüfen |
| `forbidden: no access to board` | Die Person hat auf diesem Board keine Rolle | Owner gibt das Board frei; prüfen, ob die Adresse im Anmeldedienst genau der freigegebenen entspricht (nach einem Adresswechsel ist es eine neue Person) |
| `forbidden: insufficient role` | Die Rolle reicht nicht (z. B. Betrachter versucht zu schreiben, Editor versucht das Board zu löschen) | Owner hebt die Rolle an |
| `forbidden: nur der Owner darf …` | Freigaben sehen oder ändern dürfen nur Owner | Owner bitten |
| `forbidden: cross-origin request rejected` | Eine schreibende Anfrage kam von einer anderen Website (auch einer unter derselben Hauptdomain). Bei älteren Browsern, die keinen `Sec-Fetch-Site`-Header senden, auch dann, wenn der Proxy den `Host`-Header umschreibt | Board direkt unter seiner eigenen Adresse öffnen; den `Host`-Header im Proxy nicht umschreiben (Caddy behält ihn standardmäßig bei) |
| `forbidden: admin endpoints require authentication` | Server-Administration im Einzelnutzer-Betrieb | nur im Mehrnutzer-Betrieb verfügbar |

### Weitere Fälle

- **`409` beim Ändern einer Freigabe:** Die Änderung würde den letzten Owner des Boards entfernen
  oder herabstufen. Erst eine andere Person zum Owner machen.
- **`400 Domain ist für Freigaben nicht erlaubt (ABOARD_ACL_DOMAINS)`:** Die Domain steht nicht in
  `ABOARD_ACL_DOMAINS` (oder ist eine Subdomain einer erlaubten Domain).
- **Person sieht nach der ersten Anmeldung nur ein leeres Board:** Das ist ihr persönliches
  Board. Freigegebene Boards erscheinen, sobald ein Owner sie freigibt — oder, wenn bereits
  freigegeben wurde, stimmt die Adresse nicht genau überein.
- **Niemand sieht die alten Boards nach dem Umstieg auf den Mehrnutzer-Betrieb:** Die Boards haben
  keine Owner-Einträge. Vorgehen siehe
  [Umstieg](#umstieg-vom-einzelnutzer--auf-den-mehrnutzer-betrieb).
- **Proxy meldet `502 Bad Gateway` mit Unix-Socket:** Caddy darf den Socket nicht öffnen oder
  sieht ihn nicht. Prüf, ob beide Container dasselbe Volume unter demselben Pfad
  einbinden und ob Caddy die nötigen Rechte hat (siehe
  [Rechte am Socket](#docker-composeyml)).
- **Kalender-Abo landet auf der Anmeldeseite:** Der Pfad `/ical/calendar.ics` muss im Proxy am
  Anmeldedienst vorbeigeführt werden (siehe [Caddyfile](#caddyfile)).
