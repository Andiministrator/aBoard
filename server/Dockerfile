# Multi-Stage-Build (ADR-0044/0051, F-248; rootless seit ADR-0129 §5, Karte #542;
# arm64-Cross-Compile seit ADR-0129 §5, Karte #543).
# WICHTIG: Build-Kontext ist das REPO-ROOT (nicht server/), damit Stage 1
# die Frontend-Quellen (src/, package.json, vite.config.server.js) sieht.
# `release.sh` (der Release-Bau, ADR-0151) ruft entsprechend `docker buildx build
# --platform linux/amd64|linux/arm64 -f server/Dockerfile .` auf.
#
# Cross-Compile statt QEMU-Emulation: Die Stufen 1-3 laufen mit
# `--platform=$BUILDPLATFORM` nativ auf dem Build-Host (amd64) — sie
# erzeugen entweder plattformunabhängige Dateien (Frontend-JS, CA-Zerti-
# fikate/passwd/group) oder cross-kompilieren (Go via GOARCH=$TARGETARCH).
# Nur Stufe 4 (kein RUN, nur COPY) läuft in der Zielplattform.

# Stage 1 — Server-Frontend bauen (bun → dist/server-frontend/)
# --platform=$BUILDPLATFORM (Karte #543): bun install/build erzeugt reines
# JS/CSS/HTML (kein natives Binary, wird später per go:embed eingebettet) —
# läuft nativ auf dem Build-Host, keine QEMU-Emulation für arm64 nötig.
FROM --platform=$BUILDPLATFORM oven/bun:1-alpine AS frontend
WORKDIR /app
COPY package.json bun.lock ./
# --ignore-scripts (Kritiker nach v1.42.0, N-1): Läuft der Build
# mit `--network host`, bekämen Install-Skripte fremder Pakete damit Zugriff auf Loopback-Dienste des
# Hosts. Der Frontend-Build braucht keine (lokal mit leerem Cache belegt).
RUN bun install --frozen-lockfile --ignore-scripts
COPY . .
# Karte #347: Die Frontend-Versionsanzeige liest ABOARD_BUILD_VERSION. Ohne das
# hier stünde im Container "dev" — `.dockerignore` schliesst `.git` aus, also
# kann `git describe` im Build-Kontext nicht greifen. Frontend und Server-Binary
# bekommen damit dieselbe Zahl; eine Abweichung waere genau das Rätsel, das die
# Anzeige aufloesen soll.
ARG VERSION=dev
ENV ABOARD_BUILD_VERSION=${VERSION}
RUN bun run build:server-frontend

# Stage 2 — Go-Builder (embeddet das Frontend ins Single-Binary)
# modernc/sqlite ist pure Go → CGO_ENABLED=0 reicht.
# --platform=$BUILDPLATFORM (Karte #543): Cross-Compile statt Emulation —
# `go build` läuft nativ auf dem Build-Host (amd64) und erzeugt per
# GOARCH=$TARGETARCH trotzdem ein arm64-Binary. Kein QEMU nötig.
# Karte #974: ≥ 1.27.3 — sperrt die kompromittierten eMudhra-Zwischenzertifikate in crypto/x509 (Go 1.25 bekommt keinen Fix).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ .
# Echtes Frontend über den eingecheckten Platzhalter (server/web/dist/) legen,
# bevor `go build` via //go:embed bäckt.
COPY --from=frontend /app/dist/server-frontend/ ./web/dist/

# VERSION/COMMIT/BUILD_DATE werden per --build-arg übergeben und in die
# api.Build*-Variablen eingebettet (F-198, erweitert Karte #347). Sie sind die
# einzige Versionsquelle des Servers: Startlog, /healthz, Admin-Status und
# Backup-Metadaten lesen alle daraus.
# TARGETARCH (Karte #543): von buildx automatisch gesetzt (z. B. "arm64" bei
# --platform linux/arm64), muss aber je Stufe per ARG neu deklariert werden,
# um in RUN-Befehlen sichtbar zu sein. Ohne --platform (plain `docker build`)
# bleibt TARGETARCH die native Host-Architektur (amd64) — der amd64-Build
# verhält sich dadurch unverändert.
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w \
      -X github.com/Andiministrator/aBoard/server/api.BuildVersion=${VERSION} \
      -X github.com/Andiministrator/aBoard/server/api.BuildCommit=${COMMIT} \
      -X github.com/Andiministrator/aBoard/server/api.BuildDate=${BUILD_DATE}" \
    -o /aboard-server .

# Stage 3 — Laufzeit-Vorbereitung (ADR-0129 §5, Karte #542, Betreiber-Anfrage A-9).
#
# WARUM EINE EIGENE STUFE: Die letzte Stufe (Stage 4) darf KEIN RUN enthalten
# — sie wird von Karte #543 per `docker buildx build --platform linux/arm64`
# auf einem amd64-Rechner OHNE QEMU-Emulation gebaut, und ein RUN in der
# Ziel-Plattform-Stufe würde arm64-Code auf dem amd64-Host ausführen wollen.
# Alles, was diese Stufe hier erzeugt (apk-Pakete, /etc/passwd, /etc/group,
# leere Verzeichnisse), ist reine Architektur-unabhängige DATEI, kein Binary —
# daher unproblematisch per COPY --from in die Ziel-Stufe zu übernehmen —
# deshalb läuft diese Stufe (Karte #543) mit `--platform=$BUILDPLATFORM`
# nativ auf dem Build-Host statt der Zielarchitektur: apk-Pakete, addgroup/
# adduser und mkdir laufen amd64-nativ, das Ergebnis sind plattformlose
# Dateien.
#
# Numerische UID/GID 10001 statt eines Distro-Users: fest im Image verdrahtet,
# unabhängig davon, ob /etc/passwd in der Ziel-Stufe überhaupt existiert (der
# Go-Server selbst braucht keinen Passwd-Eintrag — reines net/http + pure-Go
# SQLite, kein os/user, kein CGO) — die Einträge sind nur für Werkzeuge wie
# `id`/`sh` in der Ziel-Stufe gedacht.
FROM --platform=$BUILDPLATFORM alpine:3 AS runtime-deps
# Karte #974: Mindestversion statt blindem Cache — der Build bricht ab, statt still
# einen alten Vertrauensspeicher einzubacken; die geänderte Zeile macht den Cache
# dieser Schicht einmalig ungültig.
RUN apk add --no-cache 'ca-certificates>=20261020-r0' tzdata \
    && addgroup -g 10001 aboard \
    && adduser -D -H -u 10001 -G aboard aboard \
    && mkdir -p /data/db /data/attachments /data/backups/boards /run/aboard \
    && chown -R 10001:10001 /data /run/aboard

# Stage 4 — Runtime (Ziel-Plattform, arm64-fähig ohne Emulation, Karte #543).
# KEIN RUN hier — siehe Begründung an Stage 3. Alles kommt per COPY.
#
# BEWUSST OHNE --platform: Dies ist die Ziel-Plattform-Stufe. Ohne eigene
# --platform-Angabe übernimmt buildx automatisch die mit `--platform
# linux/arm64` am Build-Befehl angegebene Zielplattform — alpine:3 ist
# Multi-Arch, buildx zieht dafür das aarch64-Manifest, ohne dass ein RUN
# darauf ausgeführt werden müsste (reines COPY/Pull). Beim normalen
# `docker build` ohne --platform (amd64-Build) bleibt die Zielplattform die
# native Host-Plattform (amd64) — unverändertes Verhalten.
FROM alpine:3

# CA-Zertifikate + Zeitzonendaten aus der Vorbereitungs-Stufe statt per RUN.
COPY --from=runtime-deps /etc/ssl/certs /etc/ssl/certs
COPY --from=runtime-deps /usr/share/zoneinfo /usr/share/zoneinfo
# Numerischer User/Group-Eintrag (10001) — fürs Binary selbst nicht
# erforderlich, aber `docker exec … id`/`sh` in dieser Stufe funktionieren
# sonst nicht sauber.
COPY --from=runtime-deps /etc/passwd /etc/passwd
COPY --from=runtime-deps /etc/group /etc/group
# Datenverzeichnisse MIT Eigentümerschaft — wichtig für benannte Volumes:
# Docker/Podman übernehmen beim ERSTEN Anlegen eines benannten Volumes die
# Rechte des Image-Verzeichnisses an dieser Stelle. Ohne diesen COPY (bzw. mit
# einem `RUN mkdir` in dieser Stufe, das hier verboten ist) gehörten frische
# Volumes root, der Prozess liefe aber als 10001 — Schreibfehler beim ersten
# Start.
COPY --from=runtime-deps --chown=10001:10001 /data /data
COPY --from=runtime-deps --chown=10001:10001 /run/aboard /run/aboard

COPY --from=builder /aboard-server /usr/local/bin/aboard-server

EXPOSE 8080
VOLUME ["/data/db", "/data/attachments", "/data/backups"]

# Numerisch, nicht per Name (ADR-0129 §5) — funktioniert unabhängig davon, ob
# der Container-Runtime die passwd-Einträge oben respektiert (z. B. bei
# read-only /etc in manchen Hardening-Profilen), und ist das, was
# `docker inspect`/`podman inspect`/rootless-UID-Mappings tatsächlich sehen.
USER 10001:10001

# Healthcheck über den `aboard-server healthcheck`-Unterbefehl (server/healthcheck.go)
# statt `wget … http://localhost:8080/healthz`: der alte Aufruf ging bei
# ABOARD_UNIX_SOCKET (kein TCP-Port) und bei abweichendem ABOARD_ADDR (fest
# verdrahteter Port) ins Leere. Der Unterbefehl liest dieselben Env-Variablen
# wie der Server selbst.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/aboard-server", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/aboard-server"]
