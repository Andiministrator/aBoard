# Server im Container betreiben

[English](../../handbook/server/container.md) · **Deutsch**

Der aBoard-Server ist ein einzelnes Go-Programm (`aboard-server`), das die
Weboberfläche, die REST-API und eine SQLite-Datenbank in sich trägt.
Er läuft als Container-Image: fertig aus der GitHub Container Registry
(`ghcr.io`) oder selbst aus dem Repository gebaut.
Dieses Kapitel beschreibt, wie man das Image
startet, wo die Daten liegen, wie man aktualisiert und was man dabei nicht
falsch machen darf.

> **Sicherheitsgrundsatz vorab:** Ein aBoard-Server ohne vorgeschaltete
> Anmeldung hat **keine** Zugangskontrolle. Wer den Port erreicht, kann alle
> Boards lesen und ändern. Stell den Container deshalb **nie ungeschützt
> ins Internet**. Für den Betrieb mit mehreren Personen gehört ein
> Reverse-Proxy mit Anmeldung davor — beschrieben in
> [anmeldung.md](anmeldung.md).

---

## Image und Tags

Die Beispiele in diesem Handbuch verwenden den Platzhalter
`registry.example.org/aboard`. Neben dem fertigen Image (unten) kann man das Image
aus dem Repository selbst bauen (siehe [Image selbst bauen](#image-selbst-bauen)) und
bei Bedarf in die eigene Registry legen. Setz dann deren Adresse anstelle des
Platzhalters ein.

**Fertiges Image:** Ab v1.66.0-145 entsteht zu jedem Release auf GitHub ein Image unter
`ghcr.io/andiministrator/aboard`, gebaut aus genau diesem Release-Stand. Ein Tag
enthält beide Architekturen (`amd64` und `arm64`, z. B. Raspberry Pi); Docker wählt
die passende selbst.

| Tag | Inhalt |
|---|---|
| Release-Tag (z. B. `v1.66.0-140`) | Ein bestimmtes Release. **Empfohlen für den Betrieb.** |
| `latest` | Das höchste reguläre Release. Vorabversionen bekommen ihr Release-Tag, aber nie `latest`. Zum Ausprobieren, nicht für den Betrieb. |

Wer das fertige Image nutzt, setzt `ghcr.io/andiministrator/aboard` anstelle des
Platzhalters ein. Wer selbst baut, nimmt die Version des eigenen Images; in den
Beispielen steht `v1.50.0` dafür.

```bash
docker pull ghcr.io/andiministrator/aboard:v1.66.0-140
```

Oder, bei selbst gebautem Image aus der eigenen Registry:

```bash
docker pull registry.example.org/aboard:v1.50.0
```

Welche Version ein laufender Container hat, steht in der ersten Zeile seines
Logs (`aboard-server v1.50.0 …: startet`) und in der Antwort von `/healthz`
(siehe [Healthcheck](#healthcheck)).

### Image selbst bauen

Das Dockerfile liegt unter `server/Dockerfile`. Der Build-Kontext ist das
**Wurzelverzeichnis** des Repositorys, nicht `server/`, weil das Image auch die
Weboberfläche aus den Frontend-Quellen baut:

```bash
docker build -f server/Dockerfile --build-arg VERSION=v1.50.0 -t aboard-server:v1.50.0 .
```

`VERSION` (dazu optional `COMMIT` und `BUILD_DATE`) landet im Programm und
erscheint im Log und in `/healthz`. Ohne Angabe steht dort `dev`. Die
mitgelieferte `docker-compose.yml` baut auf dieselbe Weise (`docker compose
build`).

### Auf eine exakte Fassung festlegen (Digest-Pinning)

Ein Tag ist ein Name, den man neu vergeben kann. Wer sicher sein will, dass
genau das geprüfte Image läuft, hängt den **Digest** an:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:v1.50.0@sha256:<digest>
```

Den Digest eines Images in der Registry zeigt:

```bash
docker buildx imagetools inspect registry.example.org/aboard:v1.50.0
```

Beim nächsten Update Tag **und** Digest gemeinsam ändern. Zwei Grenzen sollte
man kennen: Der Digest garantiert, dass jeder Pull denselben Inhalt liefert —
nicht, dass ein erneuter Build aus denselben Quellen denselben Digest ergibt.
Und die Basis-Images im Dockerfile (`alpine:3`, `golang:1.27-alpine`,
`oven/bun:1-alpine`) sind per Tag, nicht per Digest angegeben; Sicherheits-
Updates der Basis fließen so beim nächsten Build automatisch ein.



---

## Was im Image steckt

- **Ein Prozess, ein Port.** Der Server lauscht standardmäßig auf `:8080`
  (TCP, alle Schnittstellen im Container). Unter `/` liefert er die
  Weboberfläche, unter `/boards…` die REST-API, unter `/healthz` den
  Gesundheitsstatus. Die vollständige Pfadliste steht in [api.md](api.md).
- **Kein root.** Der Prozess läuft als numerischer Benutzer `10001:10001`
  (siehe [Rootless](#rootless-benutzer-10001)).
- **Unterbefehle.** Dasselbe Programm kennt neben dem Serverbetrieb einige
  Werkzeuge: `backup`, `restore`, `import`, `agent-token`, `acl`, `nutzer`, `boardpaar`, `healthcheck`. Ein
  Überblick erscheint mit `docker run --rm registry.example.org/aboard:v1.50.0 help`.
  Backup und Wiederherstellung beschreibt [administration.md](administration.md).

---

## Volumes

| Pfad im Container | Inhalt | Persistent halten? |
|---|---|---|
| `/data/db` | SQLite-Datenbank `aboard.db` samt `-wal`/`-shm`-Dateien: Boards, Nutzer, Freigaben, Konfiguration | **Ja** — sonst sind alle Boards weg |
| `/data/attachments` | Dateianhänge (an Karten und an Kommentaren) | **Ja** — sonst sind alle Anhänge weg |
| `/data/backups` | Ziel der Board-Backups (Voreinstellung `/data/backups/boards`) und ein naheliegendes Ziel für `aboard-server backup` | Empfohlen |

Die Pfade für Datenbank und Anhänge lassen sich mit `ABOARD_DB` und
`ABOARD_ATTACHMENTS_DIR` ändern; die Voreinstellungen sind
`/data/db/aboard.db` und `/data/attachments`. Fehlende Verzeichnisse für
Datenbank und Anhänge legt der Server beim Start an, das Backup-Verzeichnis
beim ersten Backup.

Verwende **benannte Volumes** (oder Bind-Mounts). Das Image erklärt die
drei Pfade zwar als `VOLUME`, sodass Docker ohne Angabe anonyme Volumes anlegt
— die sind aber schwer wiederzufinden und gehen leicht verloren, wenn der
Container entfernt und neu erstellt wird.

**Datenverzeichnis nicht im laufenden Betrieb kopieren.** SQLite arbeitet mit
einem Schreibprotokoll (`aboard.db-wal`): Jüngste Änderungen stehen zeitweise
nur dort, nicht in `aboard.db`. Wer die Datei oder das Volume kopiert, während
der Server läuft, erhält leicht eine unvollständige oder widersprüchliche Kopie.
Für eine Sicherung im laufenden Betrieb gibt es das eingebaute Server-Backup
(`aboard-server backup`, siehe [administration.md](administration.md)); eine
Kopie auf Dateiebene nur bei angehaltenem Server.

Ein fehlendes Volume verhindert den Start nicht: Der Server läuft dann normal,
speichert aber nur im Container. Startet er dagegen gar nicht, liegt es fast
immer an Dateirechten (siehe nächster Abschnitt) — der Start bricht ab, wenn
sich die Datenbank nicht öffnen lässt.

---

## Rootless: Benutzer 10001

Der Container läuft als UID/GID `10001:10001`. Im Image gehören die
Datenverzeichnisse (`/data/db`, `/data/attachments`, `/data/backups`) und das
Socket-Verzeichnis `/run/aboard` bereits diesem Benutzer.

- **Neue benannte Volumes** übernehmen beim ersten Anlegen die Rechte aus dem
  Image — es ist nichts zu tun.
- **Bestehende Volumes**, die einmal root gehört haben (etwa von einem älteren,
  als root laufenden Image), müssen einmalig umgestellt werden. Symptom sonst:
  der Start bricht mit `unable to open database file` oder `permission denied`
  ab. Mit Compose:

  ```bash
  docker compose down
  docker compose run --rm --no-deps --user 0:0 --entrypoint chown aboard-server \
    -R 10001:10001 /data/db /data/attachments /data/backups
  docker compose up -d
  ```

  `docker compose run` ist hier wichtig: Es meint die Volumes des
  Compose-Projekts (deren Namen tragen den Projektnamen als Präfix). Ein
  `docker run -v db-data:…` würde stattdessen ein neues, leeres Volume anlegen.
- **Bind-Mounts** (ein Host-Verzeichnis statt eines benannten Volumes) müssen
  auf dem Host für UID 10001 beschreibbar sein, z. B. mit
  `chown -R 10001:10001 /srv/aboard`. Bei rootless Podman wird UID 10001 im
  Container auf eine andere Host-UID abgebildet; prüf die Rechte dann
  gegen diese Abbildung.

Den Container als root zu starten (`--user 0:0`) funktioniert, unterläuft aber
den Zweck und ist höchstens als Übergang gedacht.

---

## Netz: TCP oder Unix-Socket

Der Server nimmt Verbindungen auf genau einem von zwei Wegen an:

| Variable | Wirkung |
|---|---|
| `ABOARD_ADDR` | TCP-Adresse, Voreinstellung `:8080` (alle Schnittstellen). `127.0.0.1:8080` lauscht nur auf Loopback. |
| `ABOARD_UNIX_SOCKET` | Pfad zu einem Unix-Domain-Socket, z. B. `/run/aboard/aboard.sock`. **Ist er gesetzt, wird `ABOARD_ADDR` ignoriert** — es gibt dann keinen TCP-Port. |
| `ABOARD_UNIX_SOCKET_MODE` | Dateirechte des Sockets: `0660` (Voreinstellung), `0600` oder `0666`. Ein anderer Wert bricht den Start ab. |

**TCP** ist der einfachste Weg für einen Test oder den Betrieb in einem
vertrauenswürdigen Netz: Port veröffentlichen, im Browser öffnen.

**Der Unix-Socket** ist der empfohlene Weg hinter einem Reverse-Proxy auf
demselben Rechner. Es gibt dann keinen Port, den irgendjemand anderes als der
Proxy erreichen könnte. Der Socket liegt auf einem Volume, das sich aBoard und
der Proxy teilen:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:v1.50.0
    environment:
      ABOARD_UNIX_SOCKET: /run/aboard/aboard.sock
    volumes:
      - aboard-socket:/run/aboard
      # … plus die Daten-Volumes, siehe unten

  proxy:
    # dein Reverse-Proxy (z. B. Caddy)
    group_add: ["10001"]       # darf den Socket (0660, Gruppe 10001) öffnen
    volumes:
      - aboard-socket:/run/aboard

volumes:
  aboard-socket:
```

Der Server legt den Socket mit Eigentümer `10001:10001` und Modus `0660` an. Ein
Proxy, der nicht als root läuft, braucht deshalb die Gruppe 10001
(`group_add` bzw. bei Podman `--group-add 10001`) oder muss selbst als 10001
laufen. Laufen Proxy und aBoard in verschiedenen Benutzer-Namensräumen (etwa
rootless Podman mit `UserNS=auto`), genügt die Gruppe nicht; dann
`ABOARD_UNIX_SOCKET_MODE=0666` setzen und den Zugriff über die Rechte des
Socket-Verzeichnisses begrenzen. Gehört ein bestehendes Socket-Volume noch
root, scheitert der Start mit `bind: permission denied` — dann `/run/aboard`
wie oben mit `chown` umstellen.

### Mit Anmeldung: der Server prüft seinen eigenen Bind

Ist die Anmeldung über den Proxy eingeschaltet (`ABOARD_TRUST_FORWARD_AUTH=1`,
siehe [anmeldung.md](anmeldung.md)), vertraut der Server den
Identitäts-Headern des Proxys. Damit niemand am Proxy vorbei Header fälschen
kann, **verweigert er dann den Start**, wenn er per TCP auf einer anderen
Adresse als Loopback lauscht — also auch bei der Voreinstellung `:8080`.
Zulässig sind:

1. `ABOARD_UNIX_SOCKET` — im Container der übliche Weg (siehe oben);
2. `ABOARD_ADDR=127.0.0.1:<port>` — im Container nur sinnvoll, wenn der Proxy
   im selben Netz-Namensraum läuft (z. B. beide mit Host-Netz), denn ein auf
   Loopback lauschender Container ist über eine Port-Weiterleitung nicht
   erreichbar;
3. nur wenn das Netz anderweitig abgeschottet ist (z. B. ein internes
   Container-Netz ohne veröffentlichten Port, in dem nur der Proxy steckt):
   `ABOARD_FORWARD_AUTH_INSECURE_BIND=1`. Der Server startet dann mit einer
   Warnung.

Zusätzlich verlangt der Server in diesem Modus ein gemeinsames Geheimnis
zwischen Proxy und aBoard (`ABOARD_FORWARD_AUTH_SECRET`); ohne es startet er
nicht. Einzelheiten in [anmeldung.md](anmeldung.md).



---

## `docker run`

Minimaler Start für einen Test auf dem eigenen Rechner — der Port wird nur auf
Loopback des Hosts veröffentlicht:

```bash
docker run -d \
  --name aboard-server \
  --restart unless-stopped \
  --stop-timeout 40 \
  -p 127.0.0.1:8080:8080 \
  -v aboard-db:/data/db \
  -v aboard-attachments:/data/attachments \
  -v aboard-backups:/data/backups \
  registry.example.org/aboard:v1.50.0
```

Danach ist das Board unter `http://127.0.0.1:8080/` erreichbar. In diesem
Zustand gibt es keine Anmeldung: Es ist ein Board für eine Person bzw. für
alle, die den Port erreichen.

`--stop-timeout 40` gibt dem Server beim Anhalten genug Zeit für ein sauberes
Ende: Er braucht im ungünstigsten Fall rund 30 Sekunden (laufende Anfragen
abschließen, dann ausstehende Benachrichtigungen zustellen). Ohne die Angabe
beendet Docker ihn nach 10 Sekunden hart.

---

## `docker compose`

Ein vollständiges Beispiel mit Registry-Image:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:v1.50.0
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      ABOARD_DB: /data/db/aboard.db
      ABOARD_ATTACHMENTS_DIR: /data/attachments
      ABOARD_ADDR: ":8080"
    volumes:
      - db-data:/data/db
      - attachments:/data/attachments
      - backups:/data/backups
    restart: unless-stopped
    stop_grace_period: 40s

volumes:
  db-data:
  attachments:
  backups:
```

```bash
docker compose up -d
docker compose logs -f aboard-server
```

Die `docker-compose.yml` im Repository entspricht diesem Beispiel, baut das
Image aber lokal (`build:`). Auch sie veröffentlicht den Port nur auf der
Loopback-Schnittstelle des Rechners (`127.0.0.1:${ABOARD_PORT:-8080}:8080`):
Ohne vorgeschaltete Anmeldung ist der Server damit nur vom eigenen Rechner aus
erreichbar. Diese Bindung nicht auf alle Schnittstellen erweitern, solange keine
Anmeldung davorsteht. Wer das Board im Netz anbieten will, setzt einen
Reverse-Proxy mit Anmeldung davor ([anmeldung.md](anmeldung.md)). Die Datei
enthält zusätzlich einen optionalen Dienst
`aboard-backup`, der stündlich `aboard-server backup` aufruft und Tages- und
Wochenarchive rollierend aufbewahrt. Er startet nur mit
`docker compose --profile backup up -d`. Er schreibt das **Server-Backup**
(Datenbank und Anhänge im Klartext) — siehe
[administration.md](administration.md). Er liest die Datenbank nur lesend
(`:ro`). Das funktioniert, **solange der Server-Container läuft**: Dann
liegen die Hilfsdateien `aboard.db-wal`/`-shm` daneben, und das Archiv trägt
auch die jüngsten Änderungen (gemessen). Steht der Server, scheitert
der Lauf mit `unable to open database file` — laut, ohne Schaden; die nächste
Stunde holt ihn nach, sobald der Server wieder läuft.

Alle Umgebungsvariablen stehen in [konfiguration.md](konfiguration.md).

---

## Erster Start

Beim allerersten Start mit leerer Datenbank legt der Server die Tabellen an und
ein leeres Board mit der ID `default`. Im Log steht dann
`leere DB erkannt — kanonisches Default-Board angelegt (id=default)` und
schließlich `lauscht auf … — Frontend unter /, API unter /boards, Health /healthz`.
Erscheint diese Zeile, sind Oberfläche und API vollständig bereit.

**Ohne Anmeldung** (Voreinstellung) ist damit alles erledigt: Jede Person, die
den Server erreicht, arbeitet mit vollen Rechten. Einen Administrator gibt es in
diesem Modus nicht; die Admin-Funktionen sind dann nicht erreichbar.

**Mit Anmeldung** (`ABOARD_TRUST_FORWARD_AUTH=1`) gilt:

- **Erster Administrator:** Ist `ABOARD_BOOTSTRAP_OWNER` gesetzt, wird nur
  diese Person Administrator (bei ihrer ersten Anmeldung). Ohne die Variable
  wird Administrator, wer als Erste ein Konto bekommt — dann melde dich
  selbst als Erste an, **bevor** du den Zugang für andere freigibst. Ein Konto
  legt der Server beim ersten Board-Zugriff einer neuen E-Mail-Adresse an;
  jede weitere Person bekommt ein eigenes, leeres Board und keine
  Admin-Rechte. Einzelheiten:
  [anmeldung.md](anmeldung.md#der-erste-administrator).
- **Optional: Besitzer des `default`-Boards festlegen.** Mit
  `ABOARD_BOOTSTRAP_OWNER=person@example.org` trägt der Server diese Adresse
  beim Anlegen des `default`-Boards als dessen Owner ein. Dieser Teil wirkt
  **nur** beim allerersten Start mit leerer Datenbank; auf einer bestehenden
  Datenbank bleibt das `default`-Board unberührt (wie man bestehende Boards
  übernimmt, beschreibt [anmeldung.md](anmeldung.md#umstieg-vom-einzelnutzer--auf-den-mehrnutzer-betrieb)).
  Ist das Service-Board für den Mail-Eingang eingeschaltet (Voreinstellung),
  trägt der Server dieselbe Adresse dagegen bei **jedem** Start als dessen Owner
  ein, solange die Variable gesetzt ist. Entferne sie deshalb nach der
  Einrichtung wieder — **aber erst, wenn es einen Administrator gibt**. Ein
  ungültiger Wert (ohne `@` oder ein Domain-Eintrag wie `@firma.de`) erzeugt
  eine Fehlerzeile im Log, keinen Owner-Eintrag, und solange er gesetzt ist,
  wird **niemand** automatisch Administrator.

Details zu Rollen und Freigaben: [anmeldung.md](anmeldung.md).

---

## Healthcheck

`/healthz` ist ohne Anmeldung erreichbar — auch wenn der Proxy-Anmeldemodus
aktiv ist — und antwortet mit Status `200` und etwa:

```json
{"ok":true,"db":"up","version":"v1.50.0","commit":"abc1234","build_date":"2026-01-01T12:00:00Z"}
```

Kann der Server die Datenbank nicht erreichen, kommt Status `503` mit
`"ok":false,"db":"down"`.

**Von außen** (nur bei TCP):

```bash
curl http://127.0.0.1:8080/healthz
```

**Von innen** — funktioniert immer, auch mit Unix-Socket oder abweichendem
`ABOARD_ADDR`, weil der Unterbefehl dieselben Variablen liest wie der Server:

```bash
docker exec aboard-server aboard-server healthcheck
echo $?   # 0 = gesund, 1 = nicht gesund oder nicht erreichbar
```

> **Containername:** `docker exec aboard-server …` setzt den Namen aus dem `docker run --name aboard-server`-Beispiel voraus. Mit der Compose-Datei heißt der Container anders (`<projekt>-aboard-server-1`); dort schreibst du stattdessen `docker compose exec aboard-server aboard-server …`. Das gilt für alle `docker exec`-Beispiele im Handbuch.

Genau dieser Aufruf ist als `HEALTHCHECK` im Image hinterlegt (alle 30 s,
Zeitlimit 5 s, drei Fehlversuche, 10 s Anlaufzeit). Er öffnet selbst nichts —
keine Datenbank, keinen Port — und fragt nur den laufenden Server.

Ein Hilfscontainer, der dasselbe Image für etwas anderes nutzt (etwa der
Backup-Dienst), erbt diesen Healthcheck und stünde dauerhaft auf „unhealthy";
dort `healthcheck: disable: true` setzen, wie in der mitgelieferten
`docker-compose.yml`.

---

## Updates — und der Rückweg nur über ein Backup

Beim Start bringt der Server die Datenbank automatisch auf das Schema seiner
Version. **Diese Migrationen laufen nur vorwärts.** Eine ältere Version kann eine
von einer neueren Version migrierte Datenbank nicht zurückverwandeln. Deshalb
vor **jedem** Update:

1. **Backup ziehen**, während der Server läuft (das Backup ist eine konsistente
   Kopie, der Betrieb muss nicht unterbrochen werden):

   ```bash
   docker exec aboard-server aboard-server backup --out /data/backups
   ```

   Das Archiv enthält Datenbank und Anhänge **im Klartext**. Behandle es
   entsprechend.
2. **Update einspielen:** Tag (und gegebenenfalls Digest) erhöhen, neu starten,
   z. B. `docker compose pull && docker compose up -d`.
3. **Prüfen:** Startlog (Versionszeile, keine Fehler) und `/healthz`.

**Muss die neue Version zurück,** reicht es nicht, das alte Image zu starten:

1. Server anhalten.
2. Am besten frische, leere Volumes nehmen; die alten bleiben dann als
   Rückfall stehen, bis die alte Version sicher läuft. Wer über den alten
   Bestand zurückspielt: `restore` legt eine vorhandene `aboard.db-wal` und
   `aboard.db-shm` vorher beiseite (umbenannt in `…vor-restore-<Zeit>`, nicht
   gelöscht) — SQLite würde eine alte WAL sonst auf die zurückgespielte
   Datenbank anwenden. Anhänge, die nicht im Archiv stehen, bleiben liegen;
   `restore` nennt sie im Log. ⚠️ **Das Beiseitelegen kann `restore` erst ab
   v1.50.0.** Wer mit einer älteren Version zurückspielt (Schritt 3 nimmt die
   *alte* Version), benennt `aboard.db-wal` und `aboard.db-shm` vorher selbst
   um, bei angehaltenem Server — nicht löschen. Gemessen beim
   Rückweg-Test v1.50.0 → v1.49.0 am 2026-10-09.
3. Das Backup aus Schritt 1 mit der **alten** Version einspielen — erst zur
   Probe, dann wirklich:

   ```bash
   docker compose stop aboard-server
   docker run --rm \
     -v <neues-db-volume>:/data/db -v <neues-anhang-volume>:/data/attachments \
     -v <projekt>_backups:/data/backups \
     registry.example.org/aboard:<alte-version> \
     restore --dry-run /data/backups/<archiv>.tar.gz
   # … und danach dasselbe mit --force statt --dry-run
   ```

   (`<projekt>_backups` ist der Volume-Name, den `docker volume ls` zeigt.
   Die neuen Volumes müssen danach auch in der Compose-Datei stehen, oder du
   leerst die bisherigen, statt neue anzulegen.)
4. Die alte Version starten.

Startet eine ältere Version auf einer Datenbank mit neuerem Schema, **bricht sie
den Start mit einer Meldung ab**, statt stillschweigend auf einem fremden Schema
zu laufen. Den Notschalter `ABOARD_SCHEMA_NEUER_ZULASSEN=1` gibt es für den
Fall, dass kein Backup existiert — er ist gefährlich: Eine ältere Version kennt
Prüfungen nicht, die neuere Schemata voraussetzen, und kann dadurch mehr
erlauben als vorgesehen. Verwende ihn nur in voller Kenntnis dieses
Risikos. Ein Beispiel: Eine Version vor Schema 38 kennt die Token-Rolle
`intern` (Board-Paar, externer Server) nicht und ließe ein solches Token das
ganze Board lesen — **vor so einem Rückweg alle Tokens der Rolle `intern`
widerrufen** (`aboard-server agent-token list`, dann `revoke`).

Backup und Wiederherstellung im Einzelnen: [administration.md](administration.md).

---

## Sicherheit im Überblick

- **Nie ungeschützt ins Internet.** Ohne Anmeldung hat jede Person mit
  Netzzugang volle Rechte. Entweder nur im vertrauenswürdigen Netz (LAN, VPN,
  `127.0.0.1`) betreiben oder einen Reverse-Proxy mit Anmeldung davor schalten
  — [anmeldung.md](anmeldung.md).
- **Ports nur so weit öffnen wie nötig.** `-p 127.0.0.1:8080:8080` statt
  `-p 8080:8080`, hinter einem Proxy am besten gar kein Port, sondern der
  Unix-Socket.
- **Kein root.** Den Container nicht mit `--user 0:0` betreiben.
- **Backups schützen.** Das Server-Backup ist Klartext. Für die
  verschlüsselten Board-Backups siehe [administration.md](administration.md).
- **Version festnageln.** Im Betrieb einen Release-Tag mit Digest verwenden,
  nicht `latest`.

