# Server verwalten

Dieses Kapitel richtet sich an Personen, die einen aBoard-Server betreiben und
verwalten: Wer ist Admin, was lässt sich im Browser einstellen, wie werden
Mail-Versand, Mail-Eingang und KI-Zugang eingerichtet, wie sichert man Boards
und spielt sie zurück, und wie hält man den Server vom Internet fern.

Wie der Container gestartet wird, steht in [container.md](container.md), alle
Umgebungsvariablen in [konfiguration.md](konfiguration.md), die Anmeldung und
die Rollen in [anmeldung.md](anmeldung.md). Die einzelnen REST-Pfade der
Admin-API mit allen Feldern und Antworten beschreibt [api.md](api.md).

> **Voraussetzung:** Fast alles in diesem Kapitel setzt den Betrieb **mit
> Anmeldung** voraus (Forward-Auth oder eingebaute Anmeldung, siehe
> [anmeldung.md](anmeldung.md)). Der Abschnitt
> [Konten und Einladungen](#konten-und-einladungen-eingebaute-anmeldung) gilt
> nur für die eingebaute Anmeldung. Ohne
> Anmeldung kennt der Server keine Personen; die Admin-Funktionen antworten
> dann mit `403`, und es gibt keine Zugangskontrolle.

---

## Zwei Arten von Einstellungen

Der Server unterscheidet zwei Arten von Einstellungen:

| Art | Beispiele | Wo gesetzt | Wann wirksam |
|---|---|---|---|
| **Umgebungsvariablen** | Datenbankpfad, Listen-Adresse, Master-Schlüssel `ABOARD_SECRET_KEY`, Ausgangs-Schalter, KI-Firmenmodus (ein gemeinsamer KI-Zugang des Betreibers für alle, siehe [Firmenmodus](#firmenmodus-ein-zugang-für-alle)) | im Container (Compose-Datei, `docker run -e`) | nur beim Start |
| **Server-Konfiguration** in der Datenbank | Mail-Server (SMTP), Mail-Eingang (IMAP), Auto-Reply, Service-Board, Board-Backup | im Browser unter „Server-Administration" oder über die Admin-API | zur Laufzeit, ohne Neustart (Einzelheiten je Abschnitt) |

Für SMTP, IMAP und das Service-Board gibt es zusätzlich Umgebungsvariablen
(`SMTP_*`, `IMAP_*`, `SERVICE_BOARD_*`). Sie sind nur **Startwerte**: Beim
Serverstart übernimmt der Server einen Wert in die Datenbank, solange es dort
noch keinen gibt. Danach gilt der Wert aus der Datenbank. Eine später
geänderte Umgebungsvariable wirkt also **nicht** mehr — ändere den Wert in
der Server-Administration.

Geheime Werte (Passwörter, Schlüssel) speichert der Server verschlüsselt. Dafür
braucht er den Master-Schlüssel `ABOARD_SECRET_KEY` (siehe
[Master-Schlüssel](#master-schlüssel-aboard_secret_key)). Fehlt er, lehnt der
Server das Speichern geheimer Werte mit `503` ab, und ein `SMTP_PASS` oder
`IMAP_PASS` aus der Umgebung wird mit einer Warnung im Log übergangen.


---

## Admins

### Wer Admin wird

- **Wessen Konto der Server anlegt, während es noch keinen Admin gibt,** der
  wird automatisch Admin. Ein Konto entsteht beim ersten Board-Zugriff einer
  neuen E-Mail-Adresse; wer schon ein Konto hat, wird nicht nachträglich Admin.
  Melde dich deshalb nach dem ersten Start selbst als Erste an, bevor du den
  Zugang für andere freigibst.
- **Eingebaute Anmeldung auf einem leeren Server:** Es gibt noch niemanden, der
  über die Oberfläche einlädt. Der erste Admin entsteht über einen Befehl im
  Container (`aboard-server nutzer einladen --email …`); wer diesen Link
  einlöst, wird Admin (mit gesetztem `ABOARD_BOOTSTRAP_OWNER` nur diese Adresse). Einzelheiten: [Erster Admin auf einem leeren
  Server](#erster-admin-auf-einem-leeren-server).
- Weitere Admins ernennt ein Admin im Reiter **„User-Verwaltung"** (Umschalter
  „Admin") oder per `PATCH /admin/users/{id}/admin`.
- Der **letzte Admin** kann sich die Rechte nicht selbst entziehen und auch von
  niemandem entzogen bekommen (Antwort `409`). Gehen trotzdem alle
  Admin-Rechte verloren, hilft nur ein direkter Eingriff in die Datenbank.

> ⚠️ **Admin ist eine Vertrauensstellung.** Das Admin-Recht ist keine
> Board-Rolle, erreicht aber mittelbar jedes Board: Ein Admin sieht die Liste
> aller Boards (`GET /admin/boards`), kann für jedes Board ein Agenten-Token
> anlegen und damit dessen Inhalt lesen und ändern, und er kann die Sicherungen
> jedes Boards herunterladen und zurückspielen. Vergib es nur an Personen, die
> alle Daten auf dem Server sehen dürfen.

### Die Server-Administration im Browser

Admins sehen in der Seitenleiste den Eintrag **„Server-Administration"**. Der
Dialog hat diese Reiter:

| Reiter | Inhalt |
|---|---|
| System-Status | Version, Laufzeit, Zahl der Admins, ob ein gültiger Master-Schlüssel gesetzt ist, ob die Systemuhr gestellt ist, KI-Firmenmodus (nur Anzeige), Ausgangsschleuse |
| Audit-Log | alle Änderungen an der Server-Konfiguration und an Admin-Rechten |
| Mail-Server (SMTP) | Versand, „Verbindung testen" |
| Mail-Eingang (IMAP) | Postfach-Abruf, „Verbindung testen" |
| Mail-Auto-Reply | automatische Eingangsbestätigung |
| Service-Board | Titel, Spam-Verhalten des Boards für eingehende Mails |
| Backup | Board-Sicherungen, Verschlüsselung, Wiederherstellung |
| User-Verwaltung | Personen, Admin-Rechte, Löschen; bei der eingebauten Anmeldung darüber außerdem **Konten und Einladungen** (siehe [unten](#konten-und-einladungen-eingebaute-anmeldung)) |
| Agenten-Zugänge | Tokens für Agenten |
| Kalender | Zeitzone der Uhrzeiten |
| Wissen | Lesesicht auf einen eingehängten Markdown-Ordner (siehe [unten](#wissensquelle)) |
| Board-Paar | Seite dieser Instanz (aus, innen, außen), Partnerlisten innen und außen, Zugänge `abs_`/`abe_`/`abr_` für das Push-Skript, externe Kopien, Board-Paar-Team und der Schalter „Bearbeiten außen erlauben“ (siehe [unten](#kopie-abbauen)) |
| Clients & Projekte | Kunden und Projekte, denen Karten zugeordnet werden können |
| Ausgangsschleuse | nur sichtbar, wenn die Schleuse eingeschaltet ist |

### Grenzen der Admin-API

- Alle Pfade unter `/admin/` verlangen ein Konto mit Admin-Recht, sonst `403`.
- Ändernde Aufrufe — dazu zählen auch die Verbindungstests — sind auf
  **30 pro Stunde und Admin** begrenzt (danach `429`). Die Zählung liegt im
  Arbeitsspeicher und beginnt nach einem Neustart von vorn.
- Jede Änderung landet im Audit-Log. Bei geheimen Werten steht dort nur, **dass**
  geändert wurde, nie der Wert.

### Änderungen wirksam machen

Gespeicherte Werte liest der Server je nach Bereich unterschiedlich:

- **Mail-Server (SMTP), Mail-Eingang (IMAP), Auto-Reply und Service-Board:**
  „Speichern" in einem dieser vier Reiter startet den Arbeitsprozess des
  Mail-Eingangs selbst neu (`POST /admin/config/reload?section=imap`, nur dieser
  Bereich) — gespeichert heißt wirksam. Der Neustart ist nötig, weil der
  Mail-Eingang SMTP-Werte (Auto-Reply-Versand, Eigenversand-Filter),
  Service-Board- und Auto-Reply-Werte beim Start liest; der normale
  Mail-Versand der Karten liest SMTP ohnehin bei jedem Versand neu. Die Meldung
  unter dem Knopf unterscheidet drei Fälle: „neu gestartet", „ausgeschaltet"
  (`imap.enabled` aus — nichts läuft, nichts ist zu starten) und einen Fehler
  mit dem Grund (z. B. ungültige IMAP-Angaben; der Mail-Eingang läuft dann
  nicht). Der Neustart lässt eine gerade bearbeitete Mail zu Ende laufen
  (höchstens 20 s Wartezeit); hängt der alte Mail-Eingang länger, meldet der
  Dialog einen Fehler und startet keinen zweiten — dann etwas später erneut
  speichern. Im Service-Board-Reiter wird zuerst das Service-Board neu
  angelegt (Re-Seed, bestehende Karten bleiben), danach der Mail-Eingang
  neu gestartet.
  Ohne Browser geht dasselbe mit `POST /admin/config/reload?section=imap`; ohne
  `section` lädt der Aufruf **alle** Bereiche neu (u. a. bricht das einen
  laufenden Board-Backup-Lauf ab).
- **Benachrichtigungen** lesen die Konfiguration bei jedem Versand neu —
  gespeichert heißt wirksam.
- Der Schalter **„Backups verschlüsseln"** lädt nach dem Umlegen nur den
  Backup-Bereich neu (`POST /admin/config/reload?section=backup_board`); andere
  Bereiche bleiben unberührt.

---

## Konten und Einladungen (eingebaute Anmeldung)

Dieser Abschnitt gilt, wenn der Server die Personen selbst anmeldet
(`ABOARD_AUTH_MODE=builtin`, siehe [anmeldung.md](anmeldung.md)). Bei
Forward-Auth gibt es keine Einladungen: Dort entscheidet der vorgeschaltete
Anmeldedienst, wer hereinkommt. Ohne die eingebaute Anmeldung antworten die
Pfade dieses Abschnitts mit `404`, und der Konten-Teil im Reiter fehlt.

Es gibt keine Selbstregistrierung. Wer ein Konto bekommt, entscheidest du: Du
lädst eine Adresse ein, die Person legt fest, wie sie sich anmeldet, und du
bestätigst anschließend ihren Zugang.

### Der Weg eines Kontos

Ein Konto hat einen von vier Zuständen:

| Zustand (Reiter) | Bedeutung | Was du tun kannst |
|---|---|---|
| **Eingeladen** | Der Link ist erzeugt, die Person hat ihn noch nicht eingelöst. | Frische Einladung: Widerrufen, oder erneut einladen (neuer Link: dieselbe Adresse noch einmal eintragen). Zurückgesetztes Konto oder Person mit Nutzerzeile: **Neuer Link** (mit Rückfrage); „Widerrufen“ nur, wenn nicht zugleich Rücksetz-Link und Nutzerzeile da sind. |
| **Wartet auf Bestätigung** | Die Person hat eingelöst (Passwort mit Zwei-Faktor oder Anbieter gewählt). Sie kommt noch **nicht** herein, auch nicht, wenn ihre Adresse auf der Vorab-Liste steht. | Bestätigen, Ablehnen, Sperren, Zurücksetzen |
| **Aktiv** | Du hast bestätigt; die Person hat Zugang und ein persönliches Board. | Sperren, Zurücksetzen |
| **Gesperrt** | Die Person kommt nicht mehr herein, ihre Sitzungen sind beendet. | Entsperren, Zurücksetzen |

Die Bestätigung gibt es, damit ein weitergeleiteter Link nicht genügt: Wer den
Link einlöst, ist noch nicht drin, solange du ihn nicht bestätigst.

Nur das **Konto** ist ein Zustand in dieser Liste. Personen, die allein über die
Vorab-Liste (`ABOARD_OIDC_ALLOWED`) hereinkommen, haben kein Konto und stehen
dort nicht. Ihnen entziehst du den Zugang, indem du die Adresse aus der Liste
streichst (Neustart nötig, siehe [anmeldung.md](anmeldung.md#wer-kommt-herein-vorab-liste-und-konten))
oder die Person löschst.

Eine Person auf der Vorab-Liste **kann aber ein Konto haben**, etwa seit sie sich
selbst ein Passwort angelegt hat. Dann gilt: **Der Konto-Zustand geht der Liste
vor, sobald das Konto gesperrt ist, auf Bestätigung wartet oder zurückgesetzt
wurde** (Zustand „Eingeladen“ mit Rücksetz-Link, auch wenn der Link abgelaufen
ist; der Server räumt den jüngsten Rücksetz-Link eines noch eingeladenen Kontos
nie ab, solange das Konto so steht). Erst nach dem Bestätigen kommt die Person wieder herein. Nur eine **frische
Einladung** (kein Zurücksetzen) einer gelisteten Person bleibt durch die Liste
gedeckt: Sie kommt über einen Anbieter schon vor dem Einlösen herein.

### Personen einladen

1. Öffne die Server-Administration und den Reiter **„User-Verwaltung"**. Über
   der Nutzerliste steht **„Konten und Einladungen"**.
2. Trag im Feld **„E-Mail-Adressen zum Einladen"** eine oder mehrere Adressen
   ein (eine pro Zeile oder durch Komma getrennt, höchstens 50 auf einmal) und
   drück **„Einladen"**.
3. Unter **„Einladungslinks"** erscheint für jede Adresse ein Link. Jede Zeile
   zeigt, ob eine Mail verschickt wurde, und hat einen **„Kopieren"**-Knopf.

Der Link gilt **7 Tage** und lässt sich **einmal** einlösen. Eine Einladung gibt
nie die Admin-Rolle mit; weitere Admins ernennst du wie sonst (siehe
[Wer Admin wird](#wer-admin-wird)).

**Link weitergeben, auch ohne Mailversand.** Ist ein Mailserver eingerichtet
([Mail-Versand](#mail-versand-smtp)), schickt der Server den Link zusätzlich an die
Adresse. Das hängt **nicht** am Schalter `ABOARD_NOTIFICATIONS_DELIVERY`, weil
Einladen eine ausdrückliche Handlung von dir ist. Ohne eingerichteten
Mailserver steht in der Zeile „Mailversand nicht eingerichtet — Link selbst
weitergeben"; ebenso, wenn der Versand fehlschlägt. Gib den Link dann selbst
weiter (Messenger, persönlich), und nur an die gemeinte Person.

> ⚠️ **Den Link siehst du nur jetzt.** Der Server speichert ihn nicht, sondern
> nur einen Fingerabdruck. Der Link steht nur auf dieser Seite, nirgends im
> Browser-Speicher, und verschwindet mit **„Links ausblenden"** oder beim
> Schließen des Dialogs. Brauchst du ihn später noch einmal, trag dieselbe
> Adresse erneut ein („erneut eingeladen") oder setze das Konto zurück
> (nur nach dem Einlösen); ein
> neuer Link macht alle älteren sofort ungültig.

Adressen, die nicht klappen, bleiben im Feld stehen, damit du sie korrigieren
kannst. Gründe: keine gültige Adresse (erlaubt sind nur Adressen aus
ASCII-Zeichen, ohne Anzeigenamen), „hat schon Zugang" (die Person hat bereits
ein Konto in aBoard), „Einladung wurde schon eingelöst" (dann bestätigen oder
zurücksetzen) und „Datenbank gerade belegt" (gleich noch einmal versuchen).

Abgelaufene und benutzte Links sowie verbrauchte Wiederherstellungscodes löscht
der Server 30 Tage danach. Das Konto bleibt bis dahin als „Eingeladen" mit dem
Vermerk „abgelaufen" in der Liste; mit **„Einladung widerrufen"** entfernst du es.

### Erster Admin auf einem leeren Server

Über die Oberfläche lädt nur ein Admin ein. Auf einem Server ohne Admin geht
das nur über den Befehl im Container (`ABOARD_AUTH_MODE=builtin` und
`ABOARD_PUBLIC_BASE_URL` müssen dabei gesetzt sein, wie beim Server selbst):

```bash
docker exec aboard-server aboard-server nutzer einladen --email anna@example.org
```

Der Befehl gibt den Link aus, nicht ins Log des Servers. Gibt es beim Einlösen
noch keinen Admin, ist die Einladung damit bestätigt: Die Person ist sofort
aktiv und wird Admin (ist `ABOARD_BOOTSTRAP_OWNER` gesetzt, nur dann, wenn es
ihre Adresse ist; jede andere Einladung wartet dann auf Bestätigung). Gib den Link deshalb nur an die gemeinte Person, bis sie
eingelöst hat. Ab dem ersten Admin bestätigt immer ein Admin. Den Befehl kannst
du auch später nutzen, um jemanden von der Kommandozeile einzuladen; dann gilt
die Bestätigung wie sonst.

### Bestätigen oder ablehnen

Zeilen im Zustand „Wartet auf Bestätigung" haben die Knöpfe **„Bestätigen"** und
**„Ablehnen"**. Prüfe vorher die Spalte **„Anmeldeweg"** und den Text darunter:

- **Anmeldeweg** zeigt, was die Person gewählt hat: „Passwort + zweiter Faktor",
  oder den Namen des Anbieters (etwa „google"). „Bisheriger Anbieter" steht für
  den Anbieter `standard`, also den einzelnen Anbieter aus den alten Variablen
  `ABOARD_OIDC_ISSUER` usw.; das ist nur eine alte Beschriftung. Ein Strich
  heißt: noch kein Weg gewählt (Zustand „Eingeladen").
- **„Gemeldet: Name <Adresse>"** erscheint beim Weg über einen Anbieter. Das sind
  Name und Adresse, die der Anbieter beim Anmelden gemeldet hat. Sie kommen
  **ungeprüft vom Anbieter** und sagen nur, wer sich dort angemeldet hat, nicht,
  ob es die Person ist, die du einladen wolltest.
- Steht **„Achtung: weicht von der eingeladenen Adresse ab"** dabei, hat sich
  jemand mit einem Anbieter-Konto angemeldet, dessen Adresse nicht die
  eingeladene ist. Das ist nicht verboten: Viele Leute haben ein privates
  Anbieter-Konto und wurden an die Firmenadresse eingeladen. Bestätige aber nur,
  wenn du die Person kennst und das Konto zu ihr gehört. Im Zweifel frag auf
  einem anderen Weg nach, bevor du bestätigst. Bei Zweifel an einem
  weitergeleiteten Link lehnst du ab.

**Bestätigen** macht das Konto aktiv und legt die Person samt persönlichem Board
an. **Ablehnen** (mit Rückfrage) entfernt das wartende Konto samt Link und
Anbieter-Bindung; dieselbe Adresse oder dasselbe Anbieter-Konto lässt sich danach
neu einladen. Hat die Person schon Zugang (zum Beispiel nach einem
Zurücksetzen), lehnt der Server ab; dafür sind Sperren oder Löschen da.

> **Bekannte Grenze (zwei Admins gleichzeitig):** Bestätigt ein Admin ein Konto,
> während ein anderer dasselbe ablehnt, kann eine Person mit persönlichem Board
> ohne Konto zurückbleiben. Die Adresse gilt dann als „hat Zugang" und lässt sich
> nur noch einladen, nachdem du die Person gelöscht hast
> ([Personen löschen](#personen-löschen-datenschutz)). Stimm dich bei mehreren Admins ab,
> wer die wartenden Konten bearbeitet.

### Sperren und entsperren

**„Sperren"** (mit Rückfrage; bei aktiven und wartenden Konten) beendet sofort
alle Sitzungen der Person, und das Tor lässt sie nicht mehr herein. Eine
gesperrte Person bleibt gesperrt, **auch wenn ihre Adresse auf der Vorab-Liste
steht**. Boards und Daten bleiben bestehen.

**„Entsperren"** führt nie direkt zu „Aktiv". Hat die Person Anmeldedaten
(Passwort oder Anbieter), springt das Konto auf „Wartet auf Bestätigung",
sonst auf „Eingeladen". Danach bestätigst du ausdrücklich; der Fokus springt
nach dem Entsperren auf den Knopf „Bestätigen". Der Grund: Der Server weiß nicht,
ob das Konto vor der Sperre bestätigt war.

### Zurücksetzen

**„Zurücksetzen"** (mit Rückfrage) nimmst du, wenn jemand das Passwort oder das
Handy mit der Authenticator-App verloren hat, oder wenn du einem Konto nicht mehr
traust. Es gibt bewusst kein Zurücksetzen per Mail durch die Person selbst; das
wäre ein zweiter Weg ins Konto, der den zweiten Faktor umgeht.

Das Zurücksetzen verwirft Passwort, Zwei-Faktor, Wiederherstellungscodes und
alle Anbieter-Bindungen der Person, beendet ihre Sitzungen, lässt offene Links
verfallen und erzeugt einen **neuen Link**, den du genau einmal siehst (wie beim
Einladen; per Mail, wenn ein Mailserver eingerichtet ist). Die Person löst ihn
ein und wählt den Weg neu; danach **wartet das Konto wieder auf Bestätigung**.

- Es geht auch bei einem **gesperrten** Konto. Bei einem Verdacht: erst sperren,
  dann zurücksetzen. Das Zurücksetzen hebt die Sperre auf; Zugang gibt es aber
  erst nach Einlösen und Bestätigung.
- Das **eigene** Konto kannst du weder sperren noch zurücksetzen: Du sperrtest dich
  sonst selbst aus. Dort steht „(dein eigenes Konto)".
- Bei einer **frischen Einladung** (Zustand „Eingeladen“, die Person hatte nie Zugang)
  gibt es nichts zurückzusetzen; der Server lehnt das ab, und die Oberfläche zeigt dort
  keinen Knopf „Zurücksetzen“. Dort lädst du dieselbe Adresse erneut ein (das ergibt einen
  neuen Link) oder widerrufst.
- Bei einem Konto, das „Eingeladen“ ist und entweder **zurückgesetzt** wurde (es hat
  einen Rücksetz-Link; der Link ist abgelaufen oder verloren, auch nach Sperren und
  Entsperren) oder dessen Person eine **Nutzerzeile** hat (sie hatte Zugang, etwa weil der
  Betreiber ihre letzte Anmeldung per Befehlszeile gelöst hat und das Konto danach
  gesperrt und entsperrt wurde), zeigt die Zeile den Knopf **„Neuer Link“** (mit
  Rückfrage, denn er setzt das Konto erneut zurück): Er erzeugt einen neuen
  Rücksetz-Link, der alte gilt nicht mehr.
- **„Widerrufen“** fehlt nur dort, wo das Konto einen Rücksetz-Link **und** die Person
  eine Nutzerzeile hat: Das Löschen des Kontos ließe die Person mit ihrer Nutzerzeile
  ohne Konto zurück, ausgesperrt und nie wieder einladbar; der Server antwortet dann mit
  409. In allen anderen Fällen ist Widerrufen möglich und die Zeile zeigt es: bei einer
  frischen Einladung, bei einem zurückgesetzten Konto ohne Nutzerzeile (es gibt nichts,
  was verwaist) und bei einer Person mit Nutzerzeile ohne Rücksetz-Link (etwa über die
  Vorab-Liste angemeldet; dort stehen „Neuer Link“ und „Widerrufen“ nebeneinander). Eine
  Nutzerzeile bleibt beim Widerrufen stehen; die Person kommt ggf. weiter über die Liste
  herein.
- **Entsperren ohne Anmeldedaten** (die Person hat keinen Anmeldeweg mehr, etwa weil der
  Betreiber die letzte Bindung gelöst hat): Das Konto wird „Eingeladen“ und gilt wie ein
  zurückgesetztes (neuer Rücksetz-Link im Hintergrund, nicht ausgegeben). Die Vorab-Liste
  gilt für die Person erst wieder nach Einlösen und Bestätigung; „Widerrufen“ ist bei
  vorhandener Nutzerzeile gesperrt, „Neuer Link“ gibt den Link aus. Hatte die Person
  schon Zugang, nennt die Rückfrage zu „Widerrufen“ die Folge. Dasselbe gilt für eine
  gesperrte und wieder entsperrte **frische Einladung**, die noch nicht eingelöst war:
  Auch sie bekommt den Rücksetz-Link, und eine Person auf der Vorab-Liste kommt erst nach
  Einlösen und Bestätigung wieder herein (bewusst die sichere Richtung).
- **Sperren** lässt offene Links des Kontos verfallen. Nach dem Entsperren eines
  eingeladenen Kontos braucht die Person deshalb einen neuen Link („Neuer Link“ bzw.
  dieselbe Adresse erneut einladen).
- **Bestätigen** kann mit 409 scheitern, wenn die Person bei diesem Anbieter schon eine
  Bindung hat (etwa über die Vorab-Liste gebunden): Erst die alte Bindung lösen
  (`aboard-server nutzer bindung-loesen`) oder das Konto zurücksetzen. Ablehnen
  geht dort nicht, weil die Person schon Zugang hat.

### Löschen

Eine Person mit Zugang löschst du wie jede andere im Reiter (siehe
[Personen löschen](#personen-löschen-datenschutz)). Das entfernt auch ihr Konto,
Links, Wiederherstellungscodes und Anbieter-Bindungen. Danach kannst du die
Adresse neu einladen.

### Fehlermeldungen in der Oberfläche

- „Das geht in diesem Zustand nicht (mehr), oder es ist dein eigenes Konto" (`409`):
  Jemand war schneller, oder die Aktion passt nicht zum Zustand. Die Liste wurde
  neu geladen; prüfe den Stand und versuche es erneut.
- „Die Person hat schon Zugang" (`409` beim Ablehnen): sperren oder löschen statt
  ablehnen.
- „Datenbank gerade belegt" (`503`): gleich noch einmal versuchen.
- „Dieses Konto gibt es nicht mehr" (`404`): schon gelöscht oder widerrufen.

Alle Änderungen landen im Audit-Log (Schlüssel `konten/<id>`, nie die
Adresse). Die Pfade der Admin-API stehen in [api.md](api.md); die Oberfläche
braucht nur sie.

### Datenschutz: was die Datenbank über Konten speichert

Für Konten mit eigenem Passwort legt der Server in der Datenbankdatei ab:

- das Passwort **nur als argon2id-Hash**, nie im Klartext;
- das Geheimnis der Authenticator-App **verschlüsselt** mit `ABOARD_SECRET_KEY`;
- die zehn Wiederherstellungscodes **nur als SHA-256-Fingerabdruck**; sie sind
  einmal nutzbar;
- Einladungs- und Rücksetz-Links **nur als Fingerabdruck**;
- die eingeladene Adresse, den Anmeldeweg und bei Anbietern die vom Anbieter
  **gemeldete** Adresse und den Namen im Klartext, dazu die Bindung an das
  Anbieter-Konto (Aussteller und Kennung).

Folgen für den Betrieb:

- **Die Datenbankdatei und das Server-Backup enthalten diese Hashes und die
  verschlüsselten Geheimnisse.** Behandle beide wie Zugangsdaten: sichere sie nicht
  an Orte, die andere lesen können (siehe [Server-Backup](#server-backup-der-rückweg-für-alles)).
  Die Board-Sicherung berührt Konten nicht.
- Ein Wechsel von `ABOARD_SECRET_KEY` macht die Geheimnisse unlesbar (siehe
  [Master-Schlüssel](#master-schlüssel-aboard_secret_key)).
- Das **Löschen einer Person** entfernt Konto, Links, Codes und Bindungen mit
  (siehe [Personen löschen](#personen-löschen-datenschutz)). Ein **Ablehnen** oder
  **Widerrufen** entfernt dasselbe für das wartende bzw. eingeladene Konto.
- Aus alten Sicherungen verschwinden die Daten erst nach deren Aufbewahrungsfrist.

---

## Personen löschen (Datenschutz)

Ein Admin kann eine Person vollständig entfernen — im Reiter
**„User-Verwaltung"** über „Löschen" oder per `DELETE /admin/users/{id}`.
Im Dialog muss die E-Mail-Adresse der Person exakt eingetippt werden, bevor
der Bestätigungsknopf aktiv wird. Das eigene Konto lässt sich nicht löschen.

Die Löschung läuft in **einer** Transaktion: Entweder passiert alles oder
nichts.

| Was | Wie |
|---|---|
| Das Konto, bei der eingebauten Anmeldung zusätzlich ihr Anmelde-Konto (Passwort-Hash, Zwei-Faktor, Wiederherstellungscodes, Einladungs-Links, Anbieter-Bindungen), alle Board-Freigaben, gespeicherte KI-Schlüssel, KI-Verläufe, Benachrichtigungs-Einstellungen und -Protokoll, Kalender-Abos, Schlüssel und offene Aufträge der Ausgangsschleuse, die Einträge der Adresse im Auto-Reply-Protokoll | **gelöscht** |
| Agenten-Tokens der Person | **widerrufen**, offene Verbindungen werden geschlossen |
| Protokolle: Aktivitäten, Audit-Log, „zuletzt geändert von" in der Konfiguration, Ersteller von Tokens, bereits gesendete Schleusen-Aufträge u. a. | **pseudonymisiert** zu `geloescht:` plus 12 Hex-Zeichen — zufällig, nicht aus der Adresse ableitbar, innerhalb einer Löschung überall gleich |
| Namen und Adressen **in den Board-Inhalten** (Karten-Owner, Zugewiesene, Kommentar-Autoren) und Dateien an ihren Kommentaren | **unverändert** — bei Bedarf im Board von Hand ersetzen bzw. die Datei an der Karte löschen |
| Ein Eintrag der Adresse in der Spam-Sperrliste des Mail-Eingangs | **unverändert** (Schutzfunktion) |

**Boards, deren einziger Owner die Person ist,** blockieren die Löschung. Der
Server antwortet mit `409` und der Liste dieser Boards; es ändert sich nichts.
Im Dialog erscheint dann je Board eine Auswahl für den künftigen Owner. Über
die API gibst du die Übertragung im Body mit:

```http
DELETE /admin/users/<id>
Content-Type: application/json

{"uebertragen": {"<board-id>": "person@example.org"}}
```

Das Ziel muss ein bestehendes Konto sein. Soll ein solches Board ganz weg,
überträgst du es an dich selbst und löschst es danach wie jedes eigene Board.
Boards mit weiteren Ownern und Boards, auf denen die Person nur mitarbeitete,
bleiben einfach ohne sie bestehen.

> ⚠️ **Die Löschung sperrt die Anmeldung nicht.** Das Konto beim
> Identitätsanbieter entfernst du selbst. Meldet sich die Person vorher wieder
> an, bekommt sie ein neues, leeres Konto — nie ihr altes.

> ⚠️ **Backups enthalten die gelöschten Daten weiter,** bis sie nach ihrer Frist
> verschwinden: Board-Backups nach `backup_board.keep_days`, Server-Backups nach
> deiner eigenen Aufbewahrung. Wer nicht warten will, löscht die betroffenen
> Backups gezielt.

---

## Mail-Versand (SMTP)

Mit einem konfigurierten Mailserver können Personen mit Schreibrecht
(`editor` oder höher) Karten als Mail verschicken, und der Server stellt
Benachrichtigungen per Mail zu.

### Einrichten

Im Reiter **„Mail-Server (SMTP)"** oder als Startwerte über die Umgebung:

| Umgebungsvariable | Schlüssel in der Konfiguration | Voreinstellung | Bedeutung |
|---|---|---|---|
| `SMTP_HOST` | `smtp.host` | — | Mailserver, z. B. `smtp.example.org` (Pflicht) |
| `SMTP_PORT` | `smtp.port` | `587` | Port |
| `SMTP_USER` | `smtp.user` | — | Anmeldename (Pflicht) |
| `SMTP_PASS` | `smtp.pass` | — | Passwort (Pflicht, geheim, braucht `ABOARD_SECRET_KEY`) |
| `SMTP_FROM` | `smtp.from` | — | Absenderadresse, z. B. `board@example.org` (Pflicht) |
| `SMTP_FROM_NAME` | `smtp.from_name` | — | Anzeigename des Absenders |
| `SMTP_STARTTLS` | `smtp.starttls` | an | `false` schaltet STARTTLS ab |

„Verbindung testen" baut eine Verbindung auf, verschlüsselt sie per STARTTLS
und meldet sich an — **ohne** eine Mail zu senden.

Wichtig zu wissen:

- **Ohne Anmeldung kein Versand.** Benutzername und Passwort sind Pflicht; der
  Server verschickt nichts über einen offenen Relay.
- **Verschlüsselung über STARTTLS.** Der Server verbindet sich unverschlüsselt
  und schaltet auf TLS (mindestens 1.2) um, wenn der Mailserver STARTTLS
  anbietet. Ein Port mit TLS von Anfang an (üblicherweise 465) wird nicht
  unterstützt — verwende 587. Über eine unverschlüsselte Verbindung gibt
  der Server das Passwort nur an einen Mailserver auf demselben Rechner
  (`localhost`) heraus.
- **SPF und DKIM** richtest du beim Mail-Anbieter ein. aBoard signiert nichts
  selbst; ohne diese Einträge landen Mails oft im Spam.
- SMTP folgt **keinem** HTTP-Proxy, sondern baut eine direkte TCP-Verbindung
  auf.

### Karten per Mail verschicken

`POST /boards/{board}/cards/{karte}/email` mit Empfängern, optional Kopie,
Betreff und Begleittext. Höchstens 10 Empfänger insgesamt, höchstens 10 Mails
pro Minute und Person. Die Mail enthält den Kartentext einmal als Klartext und
einmal als HTML; das HTML erzeugt der Server selbst aus dem Markdown, eingebettetes
HTML aus der Karte wird dabei nicht übernommen. Ohne vollständige
SMTP-Konfiguration antwortet der Endpunkt mit `503`.

---

## Mail-Eingang (IMAP)

Der Server kann ein Postfach regelmäßig abrufen und eingehende Mails in Karten
verwandeln:

- **Antwort auf eine Karte:** Steht im Betreff (ersatzweise im Text) eine
  Kennung der Form `[aBoard #123]`, wird die Mail als Kommentar an Karte 123
  gehängt. Kartennummern sind je Board fortlaufend; gibt es dieselbe Nummer auf
  mehreren Boards, gewinnt das Board mit der alphabetisch ersten Board-ID.
- **Neue Anfrage:** Alle anderen Mails werden neue Karten im **Service-Board**
  (Spalte „Inbox"). Das Service-Board legt der Server beim Start selbst an,
  solange es nicht abgeschaltet ist.
- **Spam:** Absender auf der Sperrliste oder Absender, die mehr als
  `imap.rate_limit_per_hour` Mails pro Stunde schicken, landen in der Spalte
  „Spam" — oder werden verworfen, wenn im Reiter „Service-Board" das Verwerfen
  eingestellt ist (`service-board.spam_drop`).

### Einrichten

Im Reiter **„Mail-Eingang (IMAP)"** oder als Startwerte über die Umgebung:

| Umgebungsvariable | Voreinstellung | Bedeutung |
|---|---|---|
| `IMAP_ENABLED` | `false` | `true` schaltet den Abruf ein |
| `IMAP_HOST` | — | Postfach-Server, z. B. `imap.example.org` |
| `IMAP_PORT` | `993` | Port |
| `IMAP_USER` | — | Anmeldename (Pflicht) |
| `IMAP_PASS` | — | Passwort (Pflicht, geheim, braucht `ABOARD_SECRET_KEY`) |
| `IMAP_TLS` | `true` | `true` = TLS von Anfang an (993), `false` = STARTTLS |
| `IMAP_FOLDER` | `INBOX` | abgerufener Ordner |
| `IMAP_PROCESSED_FOLDER` | `Processed` | Ziel für verarbeitete Mails |
| `IMAP_ERROR_FOLDER` | `Errors` | Ziel für Mails, die nicht verarbeitet werden konnten |
| `IMAP_POLL_INTERVAL_SEC` | `600` | Sekunden zwischen zwei Abrufen (mindestens 60) |
| `IMAP_MAX_MAIL_SIZE_MB` | `25` | größere Mails gehen in den Fehler-Ordner |
| `IMAP_MAX_ATTACHMENT_SIZE_MB` | `10` | größere Anhänge werden verworfen, ein Hinweis bleibt im Text |
| `IMAP_RATE_LIMIT_PER_HOUR` | `20` | Mails pro Stunde und Absender, darüber Spam; `0` schaltet die Grenze ab |

Die Schlüssel in der Konfiguration heißen entsprechend `imap.host`,
`imap.port` usw. (das Abrufintervall: `imap.polling_interval_sec`).

„Verbindung testen" meldet sich an und öffnet den eingestellten Ordner, ohne
Mails abzurufen. „Speichern" startet den Abruf neu (siehe
[Änderungen wirksam machen](#änderungen-wirksam-machen)).

Empfehlungen:

- Ein **eigenes Postfach** nur für das Board verwenden, kein persönliches.
- Die Ordner für verarbeitete und fehlerhafte Mails **im Postfach anlegen** —
  der Server legt sie nicht selbst an.
- SPF/DKIM prüft aBoard bei eingehenden Mails nicht; das übernimmt der Filter
  des Mail-Anbieters.
- Eingehende Mails können personenbezogene Daten enthalten. Gib das
  Service-Board nur den Personen frei, die Anfragen bearbeiten.

### Schutz vor Mail-Schleifen

- Mails mit `Auto-Submitted` (außer `no`), `Precedence: bulk`, `list` oder
  `junk` oder einem `X-Loop`-Kopf werden ohne Karte verworfen.
- Mails von der eigenen Absenderadresse (`smtp.from`) werden verworfen.
- Die Ratengrenze je Absender (siehe oben) fängt den Rest ab.

Bereits verarbeitete Mails merkt sich der Server anhand der Message-ID 30 Tage
lang, damit keine Mail doppelt ankommt.

---

## Auto-Reply

Nach einer **neuen** Karte im Service-Board kann der Server dem Absender eine
Eingangsbestätigung schicken. Das ist **voreingestellt aus**, weil automatische
Mails an Außenstehende in vielen Fällen eine datenschutzrechtliche Grundlage
brauchen. Prüf vor dem Einschalten, ob deine Datenschutzerklärung den
Versand abdeckt.

Einstellungen im Reiter **„Mail-Auto-Reply"**:

| Schlüssel | Voreinstellung | Bedeutung |
|---|---|---|
| `mail_auto_reply.enabled` | `false` | an/aus |
| `mail_auto_reply.template` | eingebaute deutsche Vorlage | Markdown mit den Platzhaltern `{{.TicketId}}`, `{{.CardTitle}}`, `{{.BoardName}}` |
| `mail_auto_reply.from` | `smtp.from` | Absender |
| `mail_auto_reply.reply_to` | Absender | Antwortadresse |

Der Betreff der Bestätigung trägt die Kennung `[aBoard #…]` — antwortet der
Absender, landet seine Antwort als Kommentar an derselben Karte.

Schutz vor Schleifen und Mehrfachversand:

- Antworten auf bestehende Karten lösen nie eine Bestätigung aus.
- Je Absender und Karte geht höchstens **eine** Bestätigung hinaus, auch über
  Neustarts hinweg; zusätzlich höchstens eine pro Stunde und Absender.
- Ausgehende Bestätigungen tragen `Auto-Submitted: auto-replied` und
  `X-Loop: aboard`; Abwesenheitsnotizen und Rückläufer darauf verwirft der
  Mail-Eingang.

„Verbindung testen" rendert hier die Vorlage mit Beispieldaten und zeigt
Betreff und Text — es wird keine Mail verschickt. Die Bestätigung braucht einen
funktionierenden Mail-Versand **und** einen laufenden Mail-Eingang; „Speichern"
startet den Mail-Abruf neu (siehe
[Änderungen wirksam machen](#änderungen-wirksam-machen)).

---

## KI-Zugang

Personen können Karten an ein Sprachmodell schicken. Der Server unterstützt
zwei Betriebsarten.

### Eigene Schlüssel je Person (Voreinstellung)

Jede Person hinterlegt in den **Benutzer-Einstellungen** („KI-Anbieter
verwalten…") je Anbieter einen eigenen API-Schlüssel. Anbieter sind
`anthropic`, `openai`, `google` und `mistral`.

- Der Server prüft einen Schlüssel vor dem Speichern mit einem minimalen
  Aufruf beim Anbieter und speichert ihn dann verschlüsselt (AES-256-GCM mit
  `ABOARD_SECRET_KEY`). Ohne Master-Schlüssel antworten diese Funktionen mit
  `503`.
- Den gespeicherten Schlüssel gibt der Server **nie** zurück, auch nicht an
  die Person selbst. Wer ihn verliert, trägt ihn neu ein.
- Höchstens 10 Änderungen pro Stunde und Person.
- Abgerechnet wird beim Anbieter über das Konto der jeweiligen Person.
- Der Browser schickt den Schlüssel im Klartext an den Server. **Betreibe
  den Server deshalb nur hinter HTTPS.**

### Firmenmodus: ein Zugang für alle

Statt dass jede Person einen Schlüssel hinterlegt, richtet der Betreiber den
Zugang einmal über Umgebungsvariablen ein — etwa zu einem eigenen,
OpenAI-kompatiblen Modellserver im eigenen Netz:

```
ABOARD_LLM_OPENAI_BASE_URL=http://llm.example.org:8000/v1
ABOARD_LLM_OPENAI_MODELLE=modell-a,modell-b
ABOARD_LLM_SERVERSCHLUESSEL_FILE=/run/secrets/aboard-llm-schluessel
```

- Der Schlüssel steht in einer **Datei** (z. B. einem Docker-Secret), nicht im
  Variablenwert. Er erscheint weder im Log noch in einer Antwort. Ist die Datei
  für andere lesbar, warnt das Start-Log.
- Es gibt dann nur den Anbieter „openai" mit den Modellen aus
  `ABOARD_LLM_OPENAI_MODELLE`; eine leere Modellliste hält den Start an.
  Personen können keine eigenen Schlüssel mehr eintragen.
- KI-Verläufe sieht nur die Person, die sie angelegt hat.
- Höchstens `ABOARD_LLM_ANFRAGEN_PRO_STUNDE` Anfragen je Person und Stunde
  (Voreinstellung 60).
- Die Server-Administration zeigt den Firmenmodus, das Ziel und die Modelle nur
  an; geändert wird er ausschließlich über die Umgebung, mit Neustart.

Weitere Stellschrauben (Kontextgrenze, Werkzeugaufrufe, PII-Filter) stehen in
[konfiguration.md](konfiguration.md).

---

## Master-Schlüssel (`ABOARD_SECRET_KEY`)

Ein einziger Schlüssel schützt alle geheimen Daten des Servers: Passwörter in
der Server-Konfiguration, die KI-Schlüssel der Personen, Gotify-Tokens,
Schlüssel der Ausgangsschleuse und — wenn eingeschaltet — die Board-Backups.

**Erzeugen** — 32 zufällige Bytes, wahlweise hexadezimal oder Base64:

```bash
# 64 Hex-Zeichen
openssl rand -hex 32

# 44 Zeichen Base64
openssl rand -base64 32
```

**Ablegen**, z. B. in einer `.env`-Datei neben der Compose-Datei (nur für den
Betreiber lesbar, `chmod 600`):

```yaml
services:
  aboard-server:
    environment:
      ABOARD_SECRET_KEY: "${ABOARD_SECRET_KEY}"
```

Ein fehlender oder ungültiger Schlüssel hält den Server nicht an: Er startet,
meldet das im Log, und alle Funktionen, die etwas Geheimes speichern müssen,
antworten mit `503`.

> ⚠️ **Bewahr den Schlüssel getrennt vom Server auf.** Geht er verloren,
> sind verschlüsselte Backups, gespeicherte Passwörter und KI-Schlüssel
> unlesbar. Wer Zugriff auf den Container-Host hat, kann den Schlüssel aus der
> Container-Umgebung lesen — schütz den Host entsprechend.

**Schlüssel wechseln:** Setz den neuen Wert als `ABOARD_SECRET_KEY` und
den alten als `ABOARD_SECRET_KEY_PREVIOUS`. Der alte Schlüssel wird
ausschließlich zum **Einspielen** älterer verschlüsselter Board-Backups
benutzt; geschrieben wird immer mit dem neuen. Es gibt nur einen
Vorgängerschlüssel. Passwörter in der Server-Konfiguration, KI- und
Gotify-Schlüssel kennen den Vorgänger nicht — trag sie nach dem Wechsel
neu ein.

> ⚠️ **Eingebaute Anmeldung mit eigenen Konten:** Die Zwei-Faktor-Geheimnisse
> der Passwort-Konten sind ebenfalls mit diesem Schlüssel verschlüsselt und
> kennen den Vorgänger nicht; ein Umschlüsseln gibt es nicht. Nach einem Wechsel
> nimmt der Server keinen Code der Authenticator-App mehr an (die Seite meldet,
> die Zwei-Faktor-Anmeldung sei nicht lesbar). Setze dann die Personen unter
> „Konten und Einladungen" zurück
> ([Zurücksetzen](#zurücksetzen)); sie richten alles neu ein. Siehe auch
> [Anmeldung, „Bekannte Grenze“](anmeldung.md#eingebaute-anmeldung-per-openid-connect).

---

## Board-Backups und Wiederherstellung

Es gibt zwei unabhängige Sicherungen:

| | Board-Backup | Server-Backup |
|---|---|---|
| Auslöser | automatisch täglich, dazu per Knopf | Befehl `aboard-server backup` |
| Umfang | ein Board mit seinen Anhängen | die ganze Datenbank mit allen Anhängen |
| Zurückspielen | im Browser, ein Board | Befehl `aboard-server restore` bei angehaltenem Server in ein geleertes Datenverzeichnis (siehe unten) |
| Wer | Board-Owner und Admins | wer Zugriff auf den Container hat |
| Gedacht für | versehentliche Änderungen, Mitnahme eines Boards | Hardware-Tausch, Datenverlust, Rückweg nach einem Update |

### Tägliche Board-Sicherung

Voreingestellt sichert der Server jede Nacht **alle Boards** einzeln. Die
Einstellungen stehen im Reiter **„Backup"** bzw. in der Konfiguration:

| Schlüssel | Voreinstellung | Bedeutung |
|---|---|---|
| `backup_board.enabled` | `true` | tägliche Sicherung an/aus |
| `backup_board.cron` | `0 2 * * *` | Zeitplan im Cron-Format (fünf Felder), gerechnet in der Zeitzone des Servers — im Container ohne `TZ` ist das UTC |
| `backup_board.dir` | `/data/backups/boards` | Ablageort |
| `backup_board.keep_days` | `7` | Aufbewahrung in Tagen; die jüngsten Sicherungen bleiben in jedem Fall erhalten |
| `backup_board.max_upload_mb` | `500` | größte Datei beim Zurückspielen |
| `backup_board.encryption_enabled` | `false` | neue Sicherungen verschlüsseln |

Die Dateien liegen je Board in einem eigenen Verzeichnis:

```
/data/backups/boards/
└── <board-id>/
    ├── boardbackup-<board-id>-20260521-020000-aabbccdd.tar.gz
    └── …
```

Verschlüsselte Sicherungen enden auf `.tar.gz.enc`.

- **Verschlüsselung:** Der Schalter „Backups verschlüsseln" steht oben im
  Reiter „Backup". Er lässt sich nur einschalten, wenn ein gültiger
  `ABOARD_SECRET_KEY` gesetzt ist. Ist er an und der Schlüssel fehlt, schreibt
  der Server **keine** Sicherung, statt auf Klartext auszuweichen, und meldet
  das als fehlgeschlagene Sicherung. Bereits vorhandene Dateien bleiben, wie sie
  sind.
- **Fehlschläge** meldet der Server allen Admins als Benachrichtigung (siehe
  [Benachrichtigungen](#benachrichtigungen)).
- **Gelöschte Boards:** Ihre Sicherungen bleiben zunächst liegen, damit sich das
  Board noch zurückholen lässt. Erst nach `keep_days` (gezählt ab dem Löschen)
  räumt ein späterer Lauf sie ab. Sicherungsverzeichnisse, zu denen der Server
  kein Löschen kennt — etwa nach einem Datenbankverlust —, rührt er nie an.
- **Gestellte Uhr:** Alte Sicherungen löscht der Server nur, wenn die
  Systemuhr nachweislich gestellt ist (ein Zeitdienst wie `systemd-timesyncd`
  oder `chrony` läuft). Ohne Zeitdienst bleibt alles liegen; für Systeme, deren
  Uhr trotzdem stimmt, gibt es `ABOARD_UHR_PRUEFUNG=aus`. Den Zustand zeigt der
  Reiter „System-Status".
- **Plattenplatz:** Bei vielen Boards summiert sich das. Leg
  `/data/backups` auf ein eigenes Volume und überwache es; der Server
  selbst schlägt nicht Alarm.
- **Auslagern** an einen anderen Ort (Off-Site) ist Sache des Betriebs, etwa
  mit rsync, rclone oder restic auf das Backup-Verzeichnis.

### Sofort sichern

- **Ein Board:** Im Reiter „Backup" ein Board wählen und „Jetzt sichern"
  drücken. Board-Owner finden denselben Knopf in den **Board-Einstellungen**
  ihres Boards. Höchstens 10 manuelle Sicherungen pro Stunde und Person.
- **Alle Boards:** Ohne Board-Auswahl heißt der Knopf **„Alle Boards jetzt
  sichern"**. Das ist derselbe Lauf wie in der Nacht, samt Aufräumen. Läuft
  bereits einer, antwortet der Knopf mit „läuft bereits" (`409`) — und die
  nächtliche Sicherung überspringt sich, wenn gerade ein Lauf per Knopf läuft.
  Höchstens 10 gestartete Läufe pro Stunde und Admin. Das Ergebnis des letzten
  Laufs (gesichert, Fehler) zeigt der Reiter an; es geht bei einem Neustart
  verloren.

### Im Browser zurückspielen

Board-Owner tun das in den **Board-Einstellungen** ihres Boards, Admins im
Reiter „Backup". Dort lassen sich Sicherungen auch herunterladen und löschen.
Zum Zurückspielen wählst du eine Sicherungsdatei und einen Modus:

| Modus | Wirkung | Wer darf |
|---|---|---|
| **Überschreiben** | ersetzt den Inhalt eines bestehenden Boards samt Anhängen; die Freigaben des Boards bleiben | Owner des Ziel-Boards oder Admin |
| **Als neues Board** | legt ein neues Board an, die zurückspielende Person wird Owner | Owner des Boards, aus dem die Sicherung stammt, oder Admin |

Beim Zurückspielen prüft der Server die Prüfsumme der Datei und die
Schema-Version. Eine beschädigte oder veränderte Datei, eine zu alte oder zu
neue Sicherung lehnt er mit einer klaren Meldung ab (`400`), eine zu große mit
`413`. Eine verschlüsselte Sicherung, die zu keinem der konfigurierten
Schlüssel passt, meldet er ausdrücklich als „mit einem anderen Schlüssel
erzeugt".

Nach „Überschreiben" zeigt der Browser den zurückgespielten Stand sofort an;
ungesicherte Änderungen im offenen Board werden dabei verworfen. Jede
Rückspielung — auch eine abgelehnte — hinterlässt eine Zeile im Server-Log.

> **Tipp:** Wer unsicher ist, spielt zuerst **als neues Board** zurück und
> vergleicht. Das bestehende Board bleibt dabei unberührt.


### Server-Backup: der Rückweg für alles

Das Server-Backup sichert die gesamte Datenbank (als konsistente Kopie, der
Betrieb läuft weiter) und alle Anhänge in ein `.tar.gz`. Board-Backups sind
**nicht** enthalten. Die Beispiele nutzen den Containernamen aus dem `docker run`-Beispiel; mit Compose heißt es `docker compose exec aboard-server aboard-server …` (siehe [Server im Container](container.md)).

```bash
# einzelnes Archiv
docker exec aboard-server aboard-server backup --out /data/backups/server.tar.gz

# Verzeichnis: ein Archiv je Tag, alte werden aufgeräumt
docker exec aboard-server aboard-server backup --out /data/backups \
  --keep-daily 7 --keep-weekly 4
```

Mit einem Verzeichnis als Ziel schreibt der Befehl `aboard-YYYY-MM-DD.tar.gz`
und behält die jüngsten `--keep-daily` Tagesarchive sowie zusätzlich
`--keep-weekly` Montagsarchive.

- **Platzbedarf:** Für die konsistente Kopie legt der Befehl zuerst eine
  vollständige Kopie der Datenbank als temporäre Datei an, im
  Temp-Verzeichnis des Containers (`/tmp`, sofern `TMPDIR` nichts anderes
  sagt). Dort muss so viel freier Platz sein, wie die Datenbank groß ist;
  danach löscht der Befehl die Datei wieder.
- **Nicht von Hand kopieren:** Die Datenbankdatei oder das Datenverzeichnis im
  laufenden Betrieb zu kopieren, ergibt leicht eine unvollständige Kopie, weil
  jüngste Änderungen zeitweise nur in `aboard.db-wal` stehen. Im laufenden
  Betrieb deshalb immer dieses eingebaute Backup verwenden.

> ⚠️ Das Server-Backup ist **nicht verschlüsselt**. Die darin enthaltenen
> Passwörter und KI-Schlüssel sind zwar mit dem Master-Schlüssel geschützt,
> Board-Inhalte und Anhänge aber nicht. Bei der eingebauten Anmeldung stehen
> darin außerdem die Passwort-Hashes, die Fingerabdrücke der
> Wiederherstellungscodes und die verschlüsselten Zwei-Faktor-Geheimnisse
> (siehe [Datenschutz bei Konten](#datenschutz-was-die-datenbank-über-konten-speichert)). Nach dem Zurückspielen braucht der
> Server **denselben** `ABOARD_SECRET_KEY` wie vorher.

`restore` schreibt die Datei `aboard.db` und die Anhänge, die im Archiv stehen.
Eine vorhandene `aboard.db-wal` oder `aboard.db-shm` legt es vorher beiseite
(umbenannt in `…vor-restore-<Zeit>`, nicht gelöscht): SQLite würde eine alte WAL
sonst auf die zurückgespielte Datenbank anwenden, und danach stünde wieder der
alte Inhalt da. Anhänge, die im Archiv nicht vorkommen, bleiben liegen; `restore`
nennt ihre Zahl und ein Beispiel im Log. Sie gehören zu keiner Karte der
zurückgespielten Datenbank. Deshalb:

1. Server anhalten — `restore` bei laufendem Server zerstört dessen offene WAL.
2. Am saubersten: Datenbank- und Anhang-Verzeichnis leeren oder frische Volumes
   einbinden.
3. Erst zur Probe, dann wirklich zurückspielen:

   ```bash
   aboard-server restore --dry-run /data/backups/aboard-2026-05-21.tar.gz
   aboard-server restore --force   /data/backups/aboard-2026-05-21.tar.gz
   ```

4. Server starten.

Ohne `--force` bricht der Befehl ab, wenn schon eine Datenbank existiert oder
das Anhang-Verzeichnis nicht leer ist. Wie das mit
einem Container aussieht und warum vor jedem Update ein Server-Backup gehört,
beschreibt [container.md](container.md) im Abschnitt über Updates.

---

## Benachrichtigungen

Der Server kann Personen über Ereignisse informieren — per **Mail** (über den
konfigurierten SMTP-Server) und per **Gotify** (an einen Gotify-Server, den
jede Person selbst einträgt). Was sie bekommen möchten, stellt jede Person in
den **Benutzer-Einstellungen** unter „Benachrichtigungen…" ein.

Ereignisse sind unter anderem: Karte zugewiesen, kommentiert, erwähnt,
verschoben oder blockiert, eine neue Karte im Service-Board, eine
fehlgeschlagene Board-Sicherung und ein Teilsystem, das wegen falscher
Konfiguration nicht läuft (die beiden letzten gehen an alle Admins).

> ⚠️ **Der Versand ist voreingestellt aus.** Ohne
> `ABOARD_NOTIFICATIONS_DELIVERY=on` verarbeitet der Server alle Ereignisse und
> protokolliert sie, stellt aber nichts zu — auch die Tagesübersicht nicht. Das
> Start-Log sagt, welcher Zustand gilt. So geht beim Einrichten oder Testen
> keine Post an echte Empfänger.

Weitere Punkte:

- **Links:** Damit Mails einen Link auf die Karte enthalten, setz
  `ABOARD_PUBLIC_BASE_URL` auf die öffentliche Adresse, z. B.
  `https://board.example.org`. Ohne gültigen Wert gehen die Mails ohne Link
  hinaus.
- **Bündeln:** Gleichartige Ereignisse zur selben Karte innerhalb von 5 Minuten
  werden zusammengefasst.
- **Drosseln:** Höchstens 30 Zustellungen pro Stunde je Person und Kanal. Was
  darüber liegt, sammelt der Server und schickt es um **18:00 Uhr**
  (Serverzeit) als Tagesübersicht per Mail.
- **Test:** `POST /admin/config/notifications/test` löst die Tagesübersicht
  sofort aus.
- Noch nicht zugestellte Ereignisse liegen im Arbeitsspeicher; bei einem
  Neustart können einzelne verloren gehen.

---

## Ausgänge abschalten

Für einen Betrieb ohne Internetzugang schaltet `ABOARD_AUSGAENGE=aus` jede
ausgehende Verbindung des Servers ab. Das betrifft genau diese Wege:

| Weg | Wirkung bei `aus` |
|---|---|
| KI-Anbieter | Senden und Schlüsseltest scheitern sofort |
| Gotify | Benachrichtigung und Testversand scheitern sofort |
| SMTP | Karten-Mails, Benachrichtigungs-Mails und der Verbindungstest scheitern sofort |
| IMAP | Abruf und Verbindungstest bauen keine Verbindung auf |
| Ausgangsschleuse | Versand an externe Anbieter scheitert |

Der eigene Healthcheck spricht nur den eigenen Server an und ist nicht
betroffen. Andere Werte als `an` und `aus` halten den Start an.

**Einzelne Ziele freigeben** mit einer Komma-Liste aus `host` oder
`host:port`:

```
ABOARD_AUSGAENGE=aus
ABOARD_AUSGAENGE_ERLAUBT=llm.example.org:8000,smtp.example.org:587
```

- Bei HTTP zählt der Rechner in der URL, nicht ein dazwischengeschalteter
  Proxy. Wer über `HTTPS_PROXY` hinausgeht, trägt das eigentliche Ziel ein.
- Bei SMTP und IMAP zählt der konfigurierte Mailserver. Diese beiden folgen
  keinem HTTP-Proxy; führt der einzige Weg nach draußen über einen Proxy,
  bleiben Mail-Versand und Mail-Eingang aus.
- Ohne Port in der Liste gilt jeder Port; ohne Port in der URL gilt der
  Standard des Schemas (443 bzw. 80).
- Verglichen wird der Name genau so, wie er eingetragen ist. Eine Regel wie
  „lokale Adressen sind immer erlaubt" gibt es bewusst nicht, weil sie von der
  Namensauflösung abhinge.

Das Start-Log nennt den Zustand, z. B.
`Ausgänge: AUS — erlaubt nur: llm.example.org:8000, smtp.example.org:587`. Ein
gesperrter Versuch steht mit seinem Ziel in der Fehlermeldung. Beide Variablen
werden nur beim Start gelesen.

---

## Ausgangsschleuse (Kurzform)

Die Ausgangsschleuse ist ein kontrollierter Weg, eine Karte von einem
**externen** Sprachmodell bearbeiten zu lassen, ohne dass Namen, Adressen und
ähnliche Angaben das Haus verlassen. Sie ist **voreingestellt aus**
(`ABOARD_SCHLEUSE=an` schaltet sie ein) und vom normalen KI-Zugang getrennt:
Sie benutzt eigene, gesondert gespeicherte Schlüssel.

**Voraussetzungen:**

- ein gültiger `ABOARD_SECRET_KEY`;
- ein **lokales** Modell im Firmenmodus (siehe [KI-Zugang](#ki-zugang)),
  dessen `ABOARD_LLM_OPENAI_BASE_URL` auf eine interne Adresse zeigt (privates
  Netz, `localhost`, ein Name ohne Punkt oder mit Endungen wie `.intern`,
  `.local`, `.lan`). Ohne dieses Modell lehnt der Server jeden Auszug ab.

**Ablauf:**

1. **Freigabe (Admin):** Im Reiter „Ausgangsschleuse" trägt ein Admin die
   erlaubten externen Anbieter ein — Art (`openai-kompatibel`, `anthropic`,
   `mistral`, `google`), HTTPS-Adresse und erlaubte Modelle — und bestätigt die
   Vertragsform (geschäftliches API-Konto mit Auftragsverarbeitungsvertrag,
   ohne Training). Nur öffentliche Adressen sind zulässig. Wahlweise verlangt
   eine Freigabe ein **Vier-Augen-Prinzip**.
2. **Schlüssel (Person):** Jede Person hinterlegt in den Benutzer-Einstellungen
   ihren eigenen Schlüssel für einen freigegebenen Anbieter.
3. **Auszug:** An der Karte wählt die Person „Extern bearbeiten", die Teile
   (Beschreibung, einzelne Kommentare, Anhänge) und schreibt eine
   Aufgabenstellung. Den Text liest der Server selbst aus dem gespeicherten
   Board.
4. **Pseudonymisierung:** Der Server ersetzt Namen, Adressen, Telefonnummern,
   IBANs, URLs und Ähnliches durch Platzhalter wie `[PERSON_1]` — zuerst nach
   festen Mustern und den Personen des Boards, danach findet das lokale Modell
   weitere Stellen. Die Zuordnung Platzhalter → Original bleibt verschlüsselt
   auf dem Server.
5. **Durchsicht:** Die Person sieht den Auszug mit markierten Platzhaltern und
   kann ihn bearbeiten. Bilder gehen nur mit, wenn sie einzeln freigegeben
   werden; der Server entfernt vorher alle Metadaten.
6. **Freigabe und Versand:** „Freigeben und senden" bindet die Freigabe an
   genau den gespeicherten Stand (Prüfsumme). Bei Vier-Augen muss eine zweite
   Person mit Schreibrecht zustimmen. Dann sendet der Server im Hintergrund.
7. **Antwort:** Sie erscheint als gekennzeichneter Kommentar an der Karte, mit
   den Originalnamen zurückgesetzt. Den Kommentar kann niemand ändern.

Board-Owner können ein Board für die Schleuse **sperren** (Board-Einstellungen,
Abschnitt „Freigabe"). Steht `ABOARD_AUSGAENGE=aus`, müssen die Hosts der
freigegebenen Anbieter zusätzlich in `ABOARD_AUSGAENGE_ERLAUBT` stehen; der
Reiter „Ausgangsschleuse" nennt fehlende Einträge.

Weitere Variablen (`ABOARD_SCHLEUSE_AUFBEWAHRUNG_TAGE`,
`ABOARD_SCHLEUSE_AUSZUG_ZEICHEN`, `ABOARD_SCHLEUSE_AUFTRAEGE_PRO_STUNDE`,
`ABOARD_SCHLEUSE_PSEUDO_MODELL`) stehen in [konfiguration.md](konfiguration.md),
die Pfade in [api.md](api.md).

**Empfohlene Reihenfolge beim Einschalten:** Master-Schlüssel prüfen → mit
`ABOARD_SCHLEUSE=an` starten → Anbieter freigeben und Vertragsform bestätigen →
bei abgeschalteten Ausgängen die gemeldeten Hosts freigeben und neu starten →
im System-Status prüfen, dass Anbieter als „nutzbar" gezählt werden → erst
dann die Personen ihre Schlüssel hinterlegen lassen.


---

## Wissensquelle

Der Server kann einen **Markdown-Ordner nur lesend anzeigen**: Personen sehen ihn
in der Seitenleiste unter „Wissen" mit Übersicht, Suche und Seitenansicht.
Gedacht ist ein Git-Checkout, den ein Prozess des Betreibers aktuell
hält und den zugleich ein anderes Werkzeug liest
— eine Quelle für beide. aBoard holt nichts aus Git, hält keine Git-Zugangsdaten
und schreibt nie in den Ordner. Standard ist **aus**.

**Einhängen:** den Ordner nur lesend in den Container legen, unterhalb von
`/data/`, etwa in der Compose-Datei `- /pfad/zum/checkout:/data/wissen:ro`.
Andere Orte lehnt der Server ab; `/data` selbst ebenfalls.

**Einschalten:** im Reiter **„Wissen"**:

| Feld | Schlüssel | Bedeutung |
|---|---|---|
| Wissen in der Seitenleiste zeigen | `wissen.enabled` | `true` schaltet ein; aus heißt: kein Eintrag, alle Wege `404` |
| Ordner | `wissen.dir` | absoluter Pfad echt unterhalb von `/data/`; der Ordner muss beim Speichern existieren |
| Anzeigename | `wissen.name` | Titel in Seitenleiste und Dialog, leer = „Wissen" |
| Sichtbar für | `wissen.sichtbar` | `alle` (jedes Konto dieses Servers), `admins` oder `gruppe:<name>` — dann nur Personen, die der Anmeldedienst in `Remote-Groups` dieser Gruppe zuordnet, dazu Admins. Nötig, wo auch Fremde über eine Board-Freigabe ein Konto bekommen: „jedes Konto“ schließt sie ein |
| Link „Im Repo bearbeiten" | `wissen.bearbeiten_url` | optional, nur `https`, mit genau einem `{pfad}` hinter dem Host (im Pfad oder in der Abfrage); der Seitenpfad wird je Ordner URL-kodiert eingesetzt |

Gespeichert wird sofort, ohne Neustart. Ein ungültiger Wert wird beim Speichern
mit Grund abgelehnt; eingeschaltet werden kann nur, wenn schon ein gültiger,
vorhandener Ordner gespeichert ist. Den Ordner leeren oder löschen kann man nur,
solange das Wissen aus ist; Ausschalten geht immer, auch wenn der Ordner
inzwischen fehlt. Scheitert ein Wert, nennt der Dialog, welche davor schon
gespeichert sind.

Wird der Ordner unter demselben Pfad ausgetauscht (neu geklont, Mount neu),
liest der Server beim nächsten Abruf den neuen.

„Gespeicherten Ordner prüfen" öffnet den Ordner und nennt Seitenzahl und Stand —
auch solange das Wissen noch aus ist.

**Stand:** Schreibt der Pull-Prozess eine Datei `.wissen-stand.json` in den
Wurzelordner (`{"commit": "c0db7e3", "datum": "2026-10-08T10:00:00Z"}`), zeigt
die Lesesicht Commit und Datum, und der Server baut seinen Index neu, sobald sich
die Datei ändert. Ohne die Datei baut er ihn spätestens nach fünf Minuten neu.
`.git/` liest aBoard nie.

**Was nie angezeigt wird:** Punktdateien und -ordner (`.git/` ausdrücklich),
symbolische Links — auch solche, die im Ordner bleiben —, alles außer `.md`,
Ordner tiefer als 16 Ebenen; höchstens 5000 Seiten und 50000 Einträge insgesamt.
Seiten über 1 MiB stehen in der Übersicht, öffnen sich aber nur mit dem Hinweis
„zu groß“ und werden nicht durchsucht. Die Suche vergleicht reinen Text (kein
Regex, Teilzeichenfolgen, ohne Rücksicht auf Groß- und Kleinschreibung), ab 2 und
bis 200 Zeichen, höchstens 50 Treffer, je Suche höchstens 16 MiB gelesen (darüber
meldet sie `gekappt`), höchstens zwei Suchen gleichzeitig (weitere warten; bricht der Aufrufer ab,
endet auch seine Suche) und 30
Suchen je Minute und Person; Übersicht und Seiten
höchstens 120 Abrufe je Minute und Person. Seiten zeigt die Oberfläche so an, dass
nichts nachlädt: keine Bilder, kein eingebettetes HTML.

**Wer sieht es:** angemeldete Personen mit Konto auf diesem Server (bei
`admins` nur Admins). **Agenten-Zugänge erreichen die Wege nicht**, auch nicht
ein delegierendes Token (etwa für eine Chat-Oberfläche).

Die Wege für Menschen: `GET /wissen/index`, `GET /wissen/seite?pfad=…`,
`GET /wissen/suche?q=…` (JSON). `/me` nennt `wissen` mit dem Anzeigenamen, wenn
die Funktion an ist und die Person sie sehen darf.


---

## Board-Paar: intern und extern

Ein Betrieb kann jedes Board zweimal führen: auf einem **internen** Server, der
nur im Firmennetz erreichbar ist, und auf einem **externen**. Es gibt zwei
Arten, das einzurichten:

> Das Push-Skript, das Änderungen zwischen innen und außen trägt, gehört nicht zu diesem Repository. Seine
> Schnittstelle (Zugänge `abs_`/`abe_`/`abr_`, Pfade unter `/agent/schatten/` und `/agent/boardpaar/`) beschreibt das [API-Kapitel](api.md).

- **Externe Kopie (empfohlen).** Das externe Board ist eine **Kopie** des
  internen: Swimlanes, Spalten, Karten und Termine stehen außen an ihrem
  Platz. Innen ist das Original. Die Kopie gibt es ab v1.54.0 (zunächst nur
  mit Schatten); Spiegel und das Bearbeiten außen kommen mit dem Release
  danach. Nicht vertrauliche Karten und Termine stehen außen vollständig (als
  **Spiegel**), vertrauliche als grauer **Schatten** mit Titel. Ob das Team
  außen Spiegel bearbeiten darf, schaltet der Admin außen mit „Bearbeiten
  außen erlauben“ ein.
  Siehe [Externe Kopie anlegen](#externe-kopie-anlegen), [Spiegel und
  „vertraulich“](#spiegel-und-vertraulich) und [Bearbeiten außen
  erlauben](#bearbeiten-außen-erlauben).
- **Schatten-Kopplung (Stufe 1).** Jede Karte lebt auf genau einem der beiden
  Server. Der externe Server zeigt von internen Karten und Terminen nur
  **Schatten**: Nummer `i-<nr>`, Titel (bei vertraulichen nur „Interne
  Karte“), Spalte, Priorität, Termine und Bearbeiter — nur lesbar, in einer
  eigenen Tabelle, nie im Board selbst. Das externe Board hat eigene Karten.

In beiden Fällen bringt ein Skript des Betriebs die Daten per Push von innen
nach außen; der externe Server ruft nie nach innen. Ein Board ist außen
entweder eine Kopie oder ein Stufe-1-Board, nie beides; der jeweils andere Weg
des Skripts antwortet mit `404`.

Eingerichtet wird das von einem Admin, im Reiter **„Board-Paar"** oder auf der
Kommandozeile im Container. Board-Owner können es nicht (`403`): Koppeln gibt
Titel nach außen frei und ist eine Entscheidung für den ganzen Server.

| Teil | Bedeutung |
|---|---|
| Seite dieser Instanz | `aus` (Voreinstellung), `innen` oder `aussen`. Der Server liest sie bei jeder Anfrage; Umschalten wirkt ohne Neustart. Andere offene Browser sehen die Folgen (Schalter „vertraulich“ innen, Band „Intern“ außen) erst nach dem Neuladen. Ab v1.53.0 geht ein Wechsel nur, wenn auf der bisherigen Seite kein Board mehr gekoppelt ist — sonst lehnt der Reiter ab („zuerst unten entkoppeln“), die Kommandozeile nennt die Boards. Erst entkoppeln (außen zuerst), dann umschalten; Zugänge bleiben bestehen |
| Partnerliste innen | Boards, deren Schatten der Ausgang an das Skript liefern darf. Ab v1.53.0 mit Spalte „Letzter Abgleich“: letzter Abruf und, rot, der Grund des letzten Fehlschlags (z. B. über 5000 Einträge); ein erfolgreicher Lauf löscht ihn |
| Partnerliste außen | Boards, die Schatten annehmen, je mit der Adresse des internen Gegenstücks (`https://<intern>/#board=<id>`); ab v1.53.0 mit letztem Eingang und Fehlgrund wie innen. Externe Kopien tragen den Zusatz „— Kopie“ (Kommandozeile: Spalte „Kopie“ statt „Stufe 1“) |
| Zugänge für das Skript | `abs_…` (Ausgang, auf dem internen Server), `abe_…` (Eingang, auf dem externen) und `abr_…` (Rückweg, auf dem internen: das Skript meldet die Ziele, quittiert Aufträge und trägt Änderungen von außen ins interne Board ein, ab Stufe 2c). `abe_` kann zusätzlich das Recht „darf Kopien anlegen“ tragen (nur `abe_`). Der Klartext erscheint genau einmal. Ab v1.53.0 erneuert das Skript sein Token selbst (wöchentlich): Spalte „Zuletzt erneuert“ zeigt Zeit und Adresse, der Vorgänger steht zehn Minuten als „erneuert, gilt bis …“ da und lässt sich einzeln widerrufen; der Widerruf des aktuellen nimmt ihn mit. Eine Erneuerung, die das Skript nicht selbst ausgelöst hat, ist ein Diebstahl — Zugang widerrufen, neu anlegen |

Der Reiter bietet nur Boards dieses Servers an, nie das Service-Board. Passt
die Seite nicht zur Liste, zeigt er einen Hinweis; die Liste wirkt dann erst
nach dem Umschalten. Dasselbe auf der Kommandozeile (direkt auf der Datenbank,
auch ohne angemeldeten Admin):

```bash
aboard-server boardpaar liste [--json]
aboard-server boardpaar seite [--setzen aus|innen|aussen]
aboard-server boardpaar koppeln --seite innen --board <id>
aboard-server boardpaar koppeln --seite aussen --board <id> --link 'https://<intern>/#board=<id>'
aboard-server boardpaar entkoppeln --seite innen|aussen --board <id>
aboard-server boardpaar token-neu --typ abs|abe|abr --name <name> [--darf-anlegen]
aboard-server boardpaar token-widerrufen --typ abs|abe|abr --name <name>
aboard-server boardpaar kopie-anlegen --board <id> --ziel <name> [--link 'https://<intern>/#board=<id>']
```

`liste` nennt ab v1.53.0 je Board den letzten Abruf bzw. Eingang und gegebenenfalls
„FEHLER beim letzten …“ mit Grund.

- **Notaus:** Den Abgleich sofort anhalten, ohne die Schatten außen zu löschen,
  geht über **Zugang widerrufen** (`token-widerrufen`, das Skript bekommt `401`).
  Die Seite auf `aus` zu stellen geht ab v1.53.0 erst nach dem Entkoppeln, und
  Entkoppeln außen löscht die Schatten des Boards.

- **Adresse des internen Boards:** nur `https`, ohne Zugangsdaten und ohne
  Abfrage, im Fragment genau `board=<id>` (1–128 Zeichen aus Buchstaben,
  Ziffern, `. _ ~ -`), höchstens 512 Zeichen. Sie dient nur dem Link
  „Trotzdem öffnen“ für Menschen und geht nie an Agenten.
- **Zugänge:** Name 1–64 Zeichen aus Buchstaben, Ziffern, `. _ -`; je Typ ein
  aktiver Zugang je Name. Sie gelten für den ganzen Server, ohne Person, Board
  oder Ablaufdatum; gespeichert wird nur ein Hash. Ein Board-Paar-Token kommt
  nur auf seinem eigenen Weg durch, überall sonst gibt es `403`.
- **Entkoppeln (Stufe 1): erst außen, dann innen.** Außen entkoppeln löscht die Schatten
  dieses Boards sofort. Innen entkoppeln lässt nur den nächsten Abruf des
  Skripts scheitern; die Schatten außen bleiben stehen, bis außen entkoppelt
  wird. Ein gelöschtes Board nimmt seine Kopplung und seine Schatten mit.
- **Beide Server brauchen dieselbe Version.** Der Eingang außen lehnt eine
  abweichende Hauptversion ab (`409`).

**Was die Seite im Browser ändert:** Innen bekommen Karten und Termine den
Schalter **„vertraulich“**. Auf einem **Stufe-1-Board** steht außen dann nach dem nächsten **erfolgreichen** Lauf nur
„Interne Karte“. Auf einer **externen Kopie** behält der graue Schatten seinen Titel und verliert den übrigen Inhalt
([Spiegel und „vertraulich“](#spiegel-und-vertraulich)). Scheitert der Abgleich, bleibt der alte Stand außen bis dahin
stehen. Notweg auf einem Stufe-1-Board: außen entkoppeln, das löscht die Schatten sofort. Auf einer externen Kopie
hilft Entkoppeln nicht — die Spiegel bleiben mit vollem Inhalt stehen, und das Board ist danach frei bearbeitbar (Ops,
Agenten, ab v1.55.0 auch im Browser). Notweg dort: im Admin-Reiter „Board-Paar“ des
**externen** Servers **„Kopie außen löschen“** (ab v1.55.0; davor nur als Eigentümer über
`DELETE /boards/{id}`). Jeder Server-Admin darf das, auch ohne Eigentümer des Boards zu sein; zur Bestätigung tippt man
den Namen des Boards (oder seine Board-ID) ein. Der Knopf steht an jeder Kopie in der Partnerliste außen und, nach einem Entkoppeln, unter
„Außen entkoppelte Kopien“ ([Kopie abbauen](#kopie-abbauen)). Das Löschen nimmt auch Änderungen von außen mit, die das
Skript noch nicht bestätigt hat; der erledigte Auftrag bleibt innen stehen, und ein neuer Auftrag innen legt die Kopie
frisch an. Den Schalter „vertraulich“ einschalten darf jeder, abschalten nur ein Mensch. **Vor dem ersten Push** den Bestand durchsehen: bestehende Karten
sind nicht vertraulich, ihre Titel gehen mit dem ersten Lauf hinaus. Bei einer **externen Kopie** gehen nicht
vertrauliche Karten sogar vollständig hinaus und die Titel vertraulicher mit; deshalb vor dem ersten Auftrag den
Bestand auf „vertraulich“ durchsehen. Neue Kalender-Abos legt man innen nicht
mehr an (`409`), sondern auf dem externen Board; bestehende bleiben. Außen
zeigt die Board-Ansicht eines **Stufe-1-Boards** über den Karten ein ausgegrautes Band **„Intern“** mit
den Schatten und dem Stand „Interne Karten zuletzt abgeglichen: vor N Minuten“
(ab 30 Minuten als Warnung). Ein Klick auf einen Schatten zeigt „Diese Karte
ist intern. Bitte ins Firmennetz einwählen (VPN).“ mit dem Link „Trotzdem
öffnen“. Die Kalenderansicht eines externen Boards zeigt Schatten mit Datum
ausgegraut und nur lesbar (im Modus „Alle Boards“ nicht), der Abo-Feed außen
enthält sie ebenfalls. Auf einer **externen Kopie** gibt es das Band nicht: Schatten und Spiegel stehen in ihrer
Swimlane und Spalte; das Band fängt dort nur Schatten auf, deren Swimlane oder Spalte es außen nicht gibt.

### Externe Kopie anlegen

Eine Kopie legt ein Admin **innen** an; ausgeführt wird sie außen vom Skript. Der interne Server ruft nie
nach außen. Einmalige Vorbereitung:

1. **Seiten setzen:** Die Instanz innen steht auf `innen`, die außen auf `aussen` (Einstellung `boardpaar.seite`).
   Innen braucht das Skript außerdem einen `abs_`-Zugang, mit dem es Aufträge und den Hinweg (den Stand des internen
   Boards, der nach außen geht) holt; wer Stufe 1 betreibt, hat ihn schon.
2. **Außen, einmal je Server:** einen Zugang mit dem Recht „darf Kopien anlegen“ erzeugen und ans Skript geben
   (Reiter „Board-Paar“, Abschnitt „Zugänge für das Skript“: Typ `abe_`, Häkchen „darf Kopien anlegen“, oder auf der Kommandozeile):

   ```bash
   aboard-server boardpaar token-neu --typ abe --name <name> --darf-anlegen
   ```

   Ein `abe_` aus Stufe 1 hat das Recht nicht und bekommt es nicht von selbst. `--darf-anlegen` gibt es nur für
   `abe_` (sonst Fehler). Ohne das Recht antwortet das Anlegen mit `403`.
3. **Außen:** das **Board-Paar-Team** festlegen ([unten](#board-paar-team)).
4. **Innen, einmal:** einen Rückweg-Zugang `abr_` erzeugen und ebenfalls ans Skript geben. Mit ihm meldet das Skript
   die Namen seiner Ziele (nur Namen, keine Adressen), quittiert Aufträge und trägt Änderungen von außen ins interne
   Board ein (ab Stufe 2c):

   ```bash
   aboard-server boardpaar token-neu --typ abr --name <name>
   ```

Dann je Board, **innen** (die Instanz muss auf `innen` stehen, sonst `409`):

- Im Reiter „Board-Paar“, Abschnitt **„Externe Kopien“**: das Board und das **Ziel** (den externen Server) wählen,
  **„Externe Kopie anlegen“**. Hat das Skript noch keine Ziele gemeldet, gibt man den Namen des Ziels von Hand ein,
  so wie das Skript ihn nennt. Ist `ABOARD_PUBLIC_BASE_URL` nicht gesetzt, fragt die Maske zusätzlich nach der
  Adresse des Boards innen.
- Oder auf der Kommandozeile:

  ```bash
  aboard-server boardpaar kopie-anlegen --board <id> --ziel <name> [--link 'https://<intern>/#board=<id>']
  ```

  Ohne `--link` gilt `ABOARD_PUBLIC_BASE_URL` + `/#board=<id>`; fehlt beides, bricht der Befehl ab. Der Name des
  Ziels besteht aus 1–64 Zeichen (Buchstaben, Ziffern, `. _ -`).

Das erzeugt einen **Auftrag**. Das Board steht damit in der Partnerliste innen, die Liste zeigt „wartet auf das
Skript (seit …)“. Beim nächsten Lauf legt das Skript außen das Board an, schiebt den ersten Hinweg (den Stand des internen Boards, der nach außen geht) und meldet
„erledigt“; die Liste zeigt dann „angelegt …“. Ein offener Auftrag lässt sich mit „Zurückziehen“ löschen, ein
erledigter nicht (`409`). Zweimal derselbe offene Auftrag für Board und Ziel geht nicht (`409`). Nach dem Zurückziehen
bleibt das Board innen in der Partnerliste stehen; wer es ganz lösen will, entkoppelt es innen — außer ein
Stufe-1-Betrieb oder eine andere Kopie hängt am selben Board (siehe „Stufe-1-Boards umstellen“).

- **Außen** steht die Kopie in der Partnerliste mit dem Zusatz „— Kopie“. Sie bekommt die Mitglieder aus dem
  Board-Paar-Team ([unten](#board-paar-team)) und **alle Server-Admins als Eigentümer** (die beim Anlegen Server-Admin sind; später ernannte Admins nicht).
- **Nicht von Hand koppeln:** Eine Kopie lässt sich nach dem Entkoppeln außen nicht von Hand wieder koppeln
  (`409`). Wer sie zurückhaben will, legt innen einen neuen Auftrag an: Das Skript trifft dasselbe Board, und der
  Server **nimmt die Kopie wieder auf** (Board und Mitglieder bleiben) — sofern er sie selbst als Kopie angelegt hat.
  Eine Kopie, die schon vor v1.55.0 außen entkoppelt wurde, gilt als gewöhnliches Board; der Auftrag
  endet dann außen mit `409` (siehe [Kopie abbauen](#kopie-abbauen)).
- **Außen entkoppelte Kopie:** Sie ist für die Schreibschranke ein gewöhnliches Board (Ops, Agenten): Bearbeiter können alles ändern, nichts davon geht nach
  innen. Der Server merkt sich aber ihre Herkunft (Notweg, Liste „Außen entkoppelte Kopien“, Wiederaufnahme); von Hand
  koppeln lehnt er deshalb auch ab, wenn die Spiegel-Marken fehlen. Die Karten von innen bleiben dabei **mit vollem Inhalt** stehen — der Bestätigungsdialog beim Entkoppeln
  einer Kopie sagt das und verweist auf „Kopie außen löschen“. Ab v1.55.0 gibt auch der
  Browser die früheren Spiegel frei: Er fragt beim Öffnen des Boards den Server, ob es noch eine Kopie ist; nur bei
  einer klaren Antwort „keine Kopie“ fällt die Sperre, bei einem Fehler bleibt sie (bis v1.54.0 blieben sie im
  Browser immer gesperrt). Eine spätere Wiederaufnahme überschreibt Änderungen von außen mit der inneren Fassung; ein
  Browser, der das Board während der Wiederaufnahme offen hat, sperrt erst nach dem Neuladen wieder (bis dahin lehnt
  der Server Änderungen an Spiegeln mit `403` ab). Änderungen von außen,
  die das Skript noch nicht bestätigt hat, löscht der Server nach 25 Tagen (beim nächsten Aufräumlauf); wird die Kopie vorher
  wieder aufgenommen, kommen sie noch nach innen.
- **Entkoppeln innen** löscht alle Aufträge des Boards, auch erledigte. Das externe Board bleibt bestehen; ein Admin
  außen löscht es mit „Kopie außen löschen“ ([Kopie abbauen](#kopie-abbauen)). Gelöscht wird nie über die Grenze hinweg.
- **Stufe-1-Boards umstellen:** Ein Stufe-1-Board wird nicht umgebaut. Innen eine Kopie anlegen (es entsteht ein
  **neues** Board außen), eigene Karten des alten Boards außen von Hand innen anlegen, das alte **externe** Board außen
  entkoppeln und als dessen Eigentümer über `DELETE /boards/{id}` löschen („Kopie außen löschen“ gilt nur für Kopien
  und lehnt ein Stufe-1-Board mit `409` ab; einen Knopf zum Löschen eines Boards hat der Browser nicht). **Nicht innen entkoppeln:** Das interne Board trägt jetzt die Kopie, und Entkoppeln innen
  löscht ihren Auftrag. Das Skript muss dafür auf die neuen Wege umgestellt sein.
- <a id="kopie-abbauen"></a>**Kopie abbauen („Kopie außen löschen“, ab v1.55.0):** Im Reiter
  „Board-Paar“ des externen Servers steht an jeder Kopie der Partnerliste außen und an jeder außen entkoppelten Kopie
  (eigene Liste „Außen entkoppelte Kopien“) der Knopf **„Kopie außen löschen“**. Er öffnet eine Bestätigung: Erst wenn
  der Name des Boards oder seine Board-ID eingetippt ist, wird „Endgültig löschen“ aktiv. Gelöscht wird das Board mit allem, was daran
  hängt — Karten, Termine, Anhänge, Schatten, Kopplung und noch nicht bestätigte Änderungen von außen. Das darf jeder
  Server-Admin, auch ohne Eigentümer des Boards zu sein, aber **nur für Kopien**: Ein Stufe-1-Board, ein gewöhnliches
  Board und das Service-Board lehnt der Server ab (`409`); auf der Seite innen oder „aus“ gibt es den Weg nicht. Als
  Kopie gilt nur, was der Server selbst als Kopie angelegt hat — eine Kopie, die schon **vor** diesem Release außen
  entkoppelt wurde, steht nicht in der Liste und wird als gewöhnliches Board behandelt: Kein Knopf „Kopie außen
  löschen“, und ein neuer Auftrag für dieses Board scheitert außen mit `409`, statt die Kopie wieder aufzunehmen.
  Wegräumen geht nur als Eigentümer (die Server-Admins zum Zeitpunkt des Anlegens) über `DELETE /boards/{id}` mit
  Browser-Anmeldung — einen Knopf zum Löschen eines Boards gibt es im Browser nicht. Danach legt ein neuer Auftrag die
  Kopie frisch an.
  Innen ändert sich nichts. Bis dort ein neuer Auftrag die Kopie frisch anlegt oder das Board innen entkoppelt wird,
  bekommt das Skript für dieses Board `404` (wie für jedes nicht gekoppelte Board). REST:
  `DELETE /admin/boardpaar/kopien/{board}` ([API](api.md)).
- **Nur beauftragte Boards:** Den ersten Hinweg liefert der interne Server nur für ein Board mit Auftrag. Ein reines
  Stufe-1-Board bekommt dort `404`; seine vertraulichen Titel bleiben innen.
- **Bestand durchsehen:** Karten, deren Spalte oder Swimlane innen fehlt, gehen nicht hinaus. Ihre Zahl steht als
  Fehlgrund am Abgleich-Stand innen (Reiter, `boardpaar liste`).

### Board-Paar-Team

Die Einstellung **`boardpaar.team`** (nur auf dem **externen** Server) legt fest, wer jede neue Kopie sieht. Man
trägt sie im Reiter „Board-Paar“ (Abschnitt „Board-Paar-Team“) ein oder über `PUT /admin/boardpaar/team` (siehe
unten: Austritt nur mit Bestätigung).
Eingetragen werden **E-Mail-Adressen oder `@domain`**, getrennt durch Komma (auch Semikolon oder Zeilenumbruch);
höchstens 100 Einträge, ein ungültiger Eintrag lehnt das Speichern ab.

- Der Server kennt keine Gruppen. Eine `@domain` wirkt wie eine Gruppe, aber **nur**, wenn die Domain in
  `ABOARD_ACL_DOMAINS` steht ([Konfiguration](konfiguration.md)).
- Jede neue Kopie bekommt das Team als **Leser**, mit „Bearbeiten außen erlauben“ als **Bearbeiter**, und alle
  Server-Admins als **Eigentümer** (nur, wer beim Anlegen Server-Admin ist). Ohne Team sehen nur diese Admins die Kopie.
- Wer **nachträglich** ins Team kommt, wird auf schon angelegten Kopien **nicht** eingetragen; dort teilt ein Owner
  von Hand.
- Wer **aus dem Team genommen** wird (auch beim Leeren der Einstellung), verliert auf **allen** Kopien — auch auf
  außen entkoppelten — die Rolle Leser bzw. Bearbeiter; Eigentümer bleibt Eigentümer (ab v1.55.0).
  **Ausnahme:** Eine Kopie, die schon **vor** diesem Release außen entkoppelt wurde, gilt als gewöhnliches Board
  (keine Herkunft, Migration 0042) — dort die Freigaben von Hand prüfen.
  War dieselbe Person zusätzlich von Hand mit derselben Rolle geteilt, ist das nicht zu unterscheiden — ein Owner
  teilt dann neu.
- **Austritt nur mit Bestätigung** (ab v1.55.0): Fällt beim Speichern jemand heraus, der auf Kopien
  noch Zugriff hat, zeigt der Dialog vorher, **wer** betroffen ist und **wie viele** Freigaben wegfallen, und
  speichert erst nach „OK“. Danach nennt die Meldung die Zahl der entzogenen Freigaben. **Erneutes Eintragen stellt
  nichts wieder her** — auf bestehenden Kopien teilt dann ein Owner von Hand. Hat inzwischen jemand anderes das Team
  geändert, lehnt der Server ab („inzwischen geändert“): Reiter neu laden und erneut prüfen. Über die API heißt
  das: `PUT /admin/boardpaar/team` antwortet ohne `"bestaetigt": true` mit `409` und der Liste; der allgemeine Weg
  `/admin/config/boardpaar.team` lehnt jeden Entzug mit `409` ab ([API](api.md)).
- **Achtung beim Umstellen zwischen `@domain` und einzelnen Adressen:** Verglichen wird der Eintrag, nicht die Person.
  Wer `@example.org` durch `anna@example.org` ersetzt, nimmt die Domain heraus — auch Anna verliert dann den Zugriff auf
  bestehende Kopien, weil sie dort nur über die Domain eingetragen war. Umgekehrt wird beim Wechsel auf die Domain
  nichts nachgetragen. Bestehende Kopien danach von Hand teilen.

### Spiegel und „vertraulich“

Außen steht eine Kopie so:

| | Innen (Original) | Außen |
|---|---|---|
| Swimlanes, Spalten, Reihenfolge | führt | Kopie, nicht änderbar; Lanes tragen den Hinweis „Kommt vom internen Board“ |
| Nicht vertrauliche Karten und Termine | führt | **Spiegel**: vollständig, mit Kommentaren und Checkliste; Karten tragen „↪ von innen“, bei Anhängen „📎 N nur intern“ (Anhänge gehen nicht hinaus) |
| Vertrauliche Karten und Termine | führt | **Schatten**: grau, mit **Titel**, Priorität, Terminen und Bearbeitern, nicht bearbeitbar |
| Karten, die man außen anlegt (nur mit „Bearbeiten außen erlauben“) | entstehen hier | werden außen angelegt, wandern nach innen und kommen als Spiegel zurück |

- **Titel vertraulicher Karten:** Auf einer Kopie zeigt der Schatten seinen **Titel**. „Interne Karte“ steht nur
  noch an Schatten von **Stufe-1-Boards** und an Karten ohne Titel. Das gilt in Anzeige, Kalender-Feed und
  Agenten-Werkzeugen.
- **Karte wird innen vertraulich:** Beim nächsten **erfolgreichen** Abgleich verschwindet ihr voller Inhalt außen, auch
  die Kommentare. Übrig bleibt der Schatten. Schaltet ein Mensch „vertraulich“ innen wieder ab, steht die Karte beim
  nächsten Lauf wieder vollständig da.
- **Außen nur lesbar, solange „Bearbeiten außen erlauben“ aus ist:** Der Server lehnt jede Änderung an Spiegeln,
  Schatten, gespiegelten Lanes und Spalten ab (Ops `422`, Ganz-PUT `403`, Karte auf ein anderes Board übertragen `403`).
  Ist die Kopplung dabei nicht lesbar: `503`, nichts geschrieben. Eigene Swimlanes außen (etwa „Vorschläge“) bleiben
  frei; schreiben dürfen dort Eigentümer und Bearbeiter, das Team ist bei „aus“ nur Leser. Eine Board-Sicherung lässt sich nicht über eine Kopie zurückspielen („Überschreiben“ → `409`); „Als neues Board“
  ergibt ein gewöhnliches Board ohne Spiegel.
- **Titel von Swimlanes, Spalten und des Boards** gehen unverändert nach außen (nur von Steuerzeichen befreit,
  Lane 200, Spalte 100 Zeichen). Wer die Kopie sehen darf, sieht den Aufbau so wie innen.
- **Eigene Spalten und Lanes außen** bleiben stehen, wenn sie Karten tragen; leere, die innen nicht vorkommen,
  verschwinden beim nächsten Abgleich.

### Bearbeiten außen erlauben

Die Einstellung **`boardpaar.rueckweg`** (`aus` oder `an`, Voreinstellung `aus`; nur auf dem **externen** Server)
schaltet den Rückweg. Im Admin-Reiter „Board-Paar“ heißt sie **„Bearbeiten außen erlauben“**; man setzt sie dort oder
über `PUT /admin/config/boardpaar.rueckweg` (`DELETE` heißt `aus`).

- **Aus** ist der Stand „außen nur lesbar“ (siehe oben).
- **An:** Das Team kann Spiegelkarten außen bearbeiten und in gespiegelten Lanes neue Karten anlegen. Der Server hält
  jede Änderung in einem Journal (einer Liste der Änderungen außen) fest, das Skript trägt sie nach innen. Innen bleibt das Original; außen erscheint die
  angewendete Fassung beim nächsten Hinweg wieder.
- **Das Team wird Bearbeiter oder Leser:** Beim Einschalten wird das Team (`boardpaar.team`) **auf allen Kopien
  Bearbeiter**, beim Ausschalten (auch beim Löschen der Einstellung) wieder **Leser**. Das geschieht in derselben
  Transaktion wie der Schalter. Andere Rollen (Eigentümer, von Hand Geteilte) bleiben unberührt, und erneutes Speichern
  desselben Werts dreht keine Rolle zurück. Andere offene Browser zeigen den Wechsel erst nach dem Neuladen.
- **Reihenfolge beim Einschalten:** (1) beide Server auf dem Release mit Rückweg (vorher je ein Backup; die Migrationen
  0041 und 0042 haben kein Down), (2) das Skript mit Rückweg, (3) erst dann „Bearbeiten außen erlauben“. Wer früher einschaltet,
  verliert nichts sofort: Änderungen außen bleiben im Journal, bis das Skript sie holt, und der Hinweg überschreibt
  unbestätigte Stellen nicht. Bleibt ein Eintrag **25 Tage** unbestätigt, meldet der Abruf „veraltet“ und das Skript
  hält für dieses Board an und meldet Alarm.
- **Zurück ohne Versionswechsel:** „Bearbeiten außen erlauben“ ausschalten. Dann gilt wieder „nur lesbar“; offene
  Journal-Einträge werden weiter abgeholt.

**Was außen geht** (bei „an“, an Spiegelkarten und außen angelegten Karten): Titel, Beschreibung samt Checkliste,
Priorität, kritisch, blockiert, Owner (nur eine Person, die innen bekannt ist), Bearbeiter, Labels, Datum und Uhrzeit, archivieren, Kommentare anfügen,
„vertraulich“ **einschalten**, eine Karte übernehmen oder die Übernahme aufheben (das geht nicht nach innen);
Verschieben und Umsortieren, aber nur in eine gespiegelte Spalte einer gespiegelten Swimlane; neue Karten dort. Auch
eine eigene Karte, die man in eine gespiegelte Swimlane und Spalte zieht, geht nach innen — sofern sie keine Anhänge
oder Verweise trägt (die gehen nicht nach innen; dann lehnt der Server mit der allgemeinen Spiegel-Meldung ab); zurück in eine Swimlane
oder Spalte ohne Spiegel lässt sie sich nicht schieben. Termine: alle Felder, „vertraulich“ einschalten;
neue Termine anlegen.

**Was innen bleibt:** Löschen, QA, Komplexität, Aufwand, Wiederholung, Projekt, Anhänge (auch an Kommentaren),
Verweise, Agent-Kontext und KI, „vertraulich“ **abschalten**, Swimlanes und Spalten (anlegen, umbenennen, löschen),
das Löschen von Kommentaren (gleich welcher Herkunft) und das Bearbeiten von Kommentaren, die von innen stammen. Der Server lehnt das ab; der Browser bietet es
nicht an. Auch die Mehrfachauswahl nimmt keine Spiegelkarten, und das Menü „Verschieben“ bietet gespiegelte Swimlanes
nicht an; einzeln lässt sich alles Erlaubte tun. Eigene Termine einer Kopie lassen sich außen nicht löschen. Wartet
eine außen angelegte Karte noch darauf, dass das Skript sie nach innen gebracht hat, lässt sie sich nur archivieren, nicht löschen.

**Konflikt-Kommentar „Board-Paar“:** Hat innen jemand dieselbe Stelle inzwischen geändert, **gilt die innere Fassung**.
Die Änderung von außen geht nicht verloren: An der Karte **innen** steht ein Kommentar mit dem Autor **„Board-Paar“**
und etwa dem Wortlaut „⚠️ Konflikt über das Board-Paar: <Person> hat außen am <Zeit> <Stelle> geändert auf: <Wert>.
Innen war diese Stelle inzwischen schon geändert — es gilt die innere Fassung. Bei Bedarf von Hand übernehmen.“ Wer
das möchte, übernimmt den Wert von Hand. Lange Werte sind gekürzt. Bei einem Termin steht der Kommentar an seiner Karte;
ist Karte oder Termin vertraulich oder hat der Termin keine Karte, gibt es keinen Kommentar. Der Kommentar selbst ist
außen nicht bearbeitbar.

**Sonst verworfen:** Ändert jemand außen etwas, das innen nicht mehr geht (die Karte ist innen gelöscht oder
inzwischen vertraulich, die Swimlane oder Spalte gibt es nicht mehr), wendet der interne Server die Änderung nicht an;
das Skript bestätigt sie trotzdem. Der Hinweg stellt außen danach die innere Fassung wieder her. Das betrifft auch:
ein gesetzter Bearbeiter, den das interne Board nicht als Person kennt (er verschwindet nach dem nächsten Lauf wieder;
ebenso ein unbekannter Name in der Personenliste eines Termins), einen bearbeiteten Kommentar, den es innen nicht mehr
gibt, einen neuen Kommentar, dessen Kennung innen schon mit anderem Text belegt ist, einen Termin, der innen gelöscht
wurde, und jedes Feld oder Löschen, das der Rückweg nicht trägt. Ebenso ein Verantwortlicher (Owner), den das interne
Board nicht als Person kennt.

**Ausnahme: neu angelegte Karten werden nie verworfen** (ab v1.55.0). Gibt es ihre Swimlane innen nicht (mehr),
ist sie archiviert, eine Service-Swimlane oder sonst eine, die der Hinweg nicht spiegelt, legt der interne Server die
Karte in der Backlog-Swimlane an; fehlt
die Spalte, in `col-backlog`, sonst in der ersten Spalte, die der Hinweg spiegelt. Hat das interne Board keine
nutzbare Backlog-Swimlane (fehlt, archiviert oder Service-Swimlane), endet das Anlegen mit `422`, und das Skript hält
an — dann die Backlog-Swimlane innen wiederherstellen bzw. aus dem Archiv holen. Die Karte trägt einen Kommentar des Autors
**„Board-Paar“**, der den gewünschten Ort mit Titel nennt. Der nächste Hinweg
zeigt sie außen dann an diesem Ort. Früher wurde eine solche Karte verworfen und verschwand außen danach still.

**Sicherungen:** Die Board-Sicherung enthält weder Schatten noch Kopplung
noch Zugänge; Schatten kommen mit dem nächsten Lauf des Skripts neu. Ein
Board-Restore „Überschreiben“ lässt Kopplung und Schatten unberührt, „Als neues
Board“ ergibt ein ungekoppeltes Board. Das Server-Backup enthält alles; nach
einem Server-Restore gelten Zugänge, die seit dem Backup widerrufen wurden,
wieder — die Liste deshalb danach prüfen.

Die Wege des Skripts und ihre Antworten stehen in [api.md](api.md#board-paar).

---

## Einbett-Schutz

Der Server setzt auf seine Antworten — die Weboberfläche und die REST-API —
den Kopf `Content-Security-Policy: frame-ancestors 'self'`. Fremde Seiten
können die Board-Oberfläche damit nicht in einen Rahmen einbetten (Schutz vor
Clickjacking); eine Seite unter derselben Adresse darf es weiterhin.

Ein vorgeschalteter Reverse-Proxy sollte diesen Kopf weder entfernen noch
überschreiben. Sicherheitsköpfe wie HSTS setzt der Server nicht selbst; sie
gehören in den Reverse-Proxy.

