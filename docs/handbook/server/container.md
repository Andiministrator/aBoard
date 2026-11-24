# Running the server in a container

**English** · [Deutsch](../../handbuch/server/container.md)

The aBoard server is a single Go program (`aboard-server`) that carries the web
interface, the REST API and a SQLite database within itself. It is delivered
as a container image. This chapter describes how to start the image,
where the data lives, how to update and what not to get wrong along the way.

> **Security principle up front:** An aBoard server without an upstream
> login has **no** access control. Anyone who can reach the port can read
> and change all boards. Therefore **never expose the container
> unprotected to the internet**. For operation with several people, a
> reverse proxy with login belongs in front of it — described in
> [authentication.md](authentication.md).

---

## Image and tags

The examples in this handbook use the placeholder
`registry.example.org/aboard`. This handbook does not assume a public registry from which the image
could be pulled: whoever runs aBoard themselves
builds the image from the repository (see [Building the image yourself](#building-the-image-yourself))
and, if needed, puts it into their own registry. Then use its address instead
of the placeholder.

In the examples, `v1.50.0` stands for the version of the image you built yourself.

```bash
docker pull registry.example.org/aboard:v1.50.0
```

Which version a running container has is shown in the first line of its
log (`aboard-server v1.50.0 …: startet`) and in the response of `/healthz`
(see [Healthcheck](#healthcheck)).

### Building the image yourself

The Dockerfile is located at `server/Dockerfile`. The build context is the
**root directory** of the repository, not `server/`, because the image also builds the
web interface from the frontend sources:

```bash
docker build -f server/Dockerfile --build-arg VERSION=v1.50.0 -t aboard-server:v1.50.0 .
```

`VERSION` (plus optionally `COMMIT` and `BUILD_DATE`) ends up in the program and
appears in the log and in `/healthz`. Without it, `dev` is shown there. The
supplied `docker-compose.yml` builds in the same way (`docker compose
build`).

### Pinning to an exact version (digest pinning)

A tag is a name that can be reassigned. Anyone who wants to be sure that
exactly the checked image runs appends the **digest**:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:v1.50.0@sha256:<digest>
```

The digest of an image in the registry is shown by:

```bash
docker buildx imagetools inspect registry.example.org/aboard:v1.50.0
```

At the next update, change tag **and** digest together. You should know two limits:
the digest guarantees that every pull delivers the same content —
not that a new build from the same sources yields the same digest.
And the base images in the Dockerfile (`alpine:3`, `golang:1.27-alpine`,
`oven/bun:1-alpine`) are given by tag, not by digest; security
updates of the base therefore flow in automatically at the next build.


---

## What is in the image

- **One process, one port.** By default the server listens on `:8080`
  (TCP, all interfaces in the container). At `/` it serves the
  web interface, at `/boards…` the REST API, at `/healthz` the
  health status. The complete list of paths is in [api.md](api.md).
- **No root.** The process runs as the numeric user `10001:10001`
  (see [Rootless](#rootless-user-10001)).
- **Subcommands.** Besides server operation, the same program knows a few
  tools: `backup`, `restore`, `import`, `agent-token`, `acl`, `nutzer`, `boardpaar`, `healthcheck`. An
  overview appears with `docker run --rm registry.example.org/aboard:v1.50.0 help`.
  Backup and restore are described in [administration.md](administration.md).

---

## Volumes

| Path in the container | Content | Keep persistent? |
|---|---|---|
| `/data/db` | SQLite database `aboard.db` including `-wal`/`-shm` files: boards, users, shares, configuration | **Yes** — otherwise all boards are gone |
| `/data/attachments` | File attachments (on cards and on comments) | **Yes** — otherwise all attachments are gone |
| `/data/backups` | Target of the board backups (default `/data/backups/boards`) and an obvious target for `aboard-server backup` | Recommended |

The paths for database and attachments can be changed with `ABOARD_DB` and
`ABOARD_ATTACHMENTS_DIR`; the defaults are
`/data/db/aboard.db` and `/data/attachments`. Missing directories for
database and attachments are created by the server at startup, the backup directory
at the first backup.

Use **named volumes** (or bind mounts). The image does declare the
three paths as `VOLUME`, so that Docker creates anonymous volumes if nothing is specified
— but those are hard to find again and easily get lost when the
container is removed and recreated.

**Do not copy the data directory while the server is running.** SQLite works with
a write-ahead log (`aboard.db-wal`): the most recent changes are temporarily
only there, not in `aboard.db`. Anyone who copies the file or the volume while
the server is running easily gets an incomplete or inconsistent copy.
For a backup during operation there is the built-in server backup
(`aboard-server backup`, see [administration.md](administration.md)); a
file-level copy only with the server stopped.

A missing volume does not prevent the start: the server then runs normally,
but stores only inside the container. If it does not start at all, the cause is almost
always file permissions (see the next section) — the start aborts if
the database cannot be opened.

---

## Rootless: user 10001

The container runs as UID/GID `10001:10001`. In the image, the
data directories (`/data/db`, `/data/attachments`, `/data/backups`) and the
socket directory `/run/aboard` already belong to this user.

- **New named volumes** take over the permissions from the image when they are first created
  — nothing needs to be done.
- **Existing volumes** that once belonged to root (for example from an older
  image that ran as root) must be converted once. Otherwise the symptom is:
  the start aborts with `unable to open database file` or `permission denied`.
  With Compose:

  ```bash
  docker compose down
  docker compose run --rm --no-deps --user 0:0 --entrypoint chown aboard-server \
    -R 10001:10001 /data/db /data/attachments /data/backups
  docker compose up -d
  ```

  `docker compose run` matters here: it refers to the volumes of the
  Compose project (whose names carry the project name as a prefix). A
  `docker run -v db-data:…` would instead create a new, empty volume.
- **Bind mounts** (a host directory instead of a named volume) must be
  writable on the host for UID 10001, e.g. with
  `chown -R 10001:10001 /srv/aboard`. With rootless Podman, UID 10001 in the
  container is mapped to a different host UID; check the permissions
  against that mapping.

Starting the container as root (`--user 0:0`) works, but defeats
the purpose and is at most intended as a transition.

---

## Network: TCP or Unix socket

The server accepts connections in exactly one of two ways:

| Variable | Effect |
|---|---|
| `ABOARD_ADDR` | TCP address, default `:8080` (all interfaces). `127.0.0.1:8080` listens on loopback only. |
| `ABOARD_UNIX_SOCKET` | Path to a Unix domain socket, e.g. `/run/aboard/aboard.sock`. **If it is set, `ABOARD_ADDR` is ignored** — there is then no TCP port. |
| `ABOARD_UNIX_SOCKET_MODE` | File permissions of the socket: `0660` (default), `0600` or `0666`. Any other value aborts the start. |

**TCP** is the simplest way for a test or for operation in a
trusted network: publish the port, open it in the browser.

**The Unix socket** is the recommended way behind a reverse proxy on
the same machine. There is then no port that anyone other than the
proxy could reach. The socket lives on a volume that aBoard and
the proxy share:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:v1.50.0
    environment:
      ABOARD_UNIX_SOCKET: /run/aboard/aboard.sock
    volumes:
      - aboard-socket:/run/aboard
      # … plus the data volumes, see below

  proxy:
    # your reverse proxy (e.g. Caddy)
    group_add: ["10001"]       # may open the socket (0660, group 10001)
    volumes:
      - aboard-socket:/run/aboard

volumes:
  aboard-socket:
```

The server creates the socket with owner `10001:10001` and mode `0660`. A
proxy that does not run as root therefore needs the group 10001
(`group_add` or, with Podman, `--group-add 10001`) or must itself run as 10001.
If proxy and aBoard run in different user namespaces (for example
rootless Podman with `UserNS=auto`), the group is not enough; then set
`ABOARD_UNIX_SOCKET_MODE=0666` and limit access through the permissions of the
socket directory. If an existing socket volume still belongs to
root, the start fails with `bind: permission denied` — then convert `/run/aboard`
with `chown` as above.

### With login: the server checks its own bind

If the login via the proxy is switched on (`ABOARD_TRUST_FORWARD_AUTH=1`,
see [authentication.md](authentication.md)), the server trusts the
identity headers of the proxy. So that nobody can forge headers bypassing the proxy,
it **then refuses to start** if it listens via TCP on any
address other than loopback — that includes the default `:8080`.
Permitted are:

1. `ABOARD_UNIX_SOCKET` — the usual way in a container (see above);
2. `ABOARD_ADDR=127.0.0.1:<port>` — in a container only useful if the proxy
   runs in the same network namespace (e.g. both with host networking), because a container
   listening on loopback is not reachable through a port forward;
3. only if the network is sealed off in some other way (e.g. an internal
   container network without a published port, containing only the proxy):
   `ABOARD_FORWARD_AUTH_INSECURE_BIND=1`. The server then starts with a
   warning.

In addition, in this mode the server requires a shared secret
between proxy and aBoard (`ABOARD_FORWARD_AUTH_SECRET`); without it, it does not
start. Details in [authentication.md](authentication.md).

---

## `docker run`

Minimal start for a test on your own machine — the port is published only on the
loopback of the host:

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

After that, the board is reachable at `http://127.0.0.1:8080/`. In this
state there is no login: it is a board for one person, or for
everyone who can reach the port.

`--stop-timeout 40` gives the server enough time for a clean shutdown when stopping:
in the worst case it needs about 30 seconds (finish running requests,
then deliver pending notifications). Without it,
Docker kills it hard after 10 seconds.

---

## `docker compose`

A complete example with a registry image:

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

The `docker-compose.yml` in the repository corresponds to this example, but builds the
image locally (`build:`). It also publishes the port only on the
loopback interface of the machine (`127.0.0.1:${ABOARD_PORT:-8080}:8080`):
without an upstream login, the server is then reachable only from the machine itself.
Do not widen this binding to all interfaces as long as no login
stands in front. Anyone who wants to offer the board on the network puts a
reverse proxy with login in front of it ([authentication.md](authentication.md)). The file
also contains an optional service
`aboard-backup`, which calls `aboard-server backup` hourly and keeps daily and
weekly archives on a rolling basis. It only starts with
`docker compose --profile backup up -d`. It writes the **server backup**
(database and attachments in plain text) — see
[administration.md](administration.md). It reads the database read-only
(`:ro`). This works **as long as the server container is running**: then
the auxiliary files `aboard.db-wal`/`-shm` are next to it, and the archive also
contains the most recent changes (measured). If the server is down, the
run fails with `unable to open database file` — loudly, without damage; the next
hour makes up for it as soon as the server runs again.

All environment variables are listed in [configuration.md](configuration.md).

---

## First start

At the very first start with an empty database, the server creates the tables and
an empty board with the ID `default`. The log then says
`leere DB erkannt — kanonisches Default-Board angelegt (id=default)` and
finally `lauscht auf … — Frontend unter /, API unter /boards, Health /healthz`.
When this line appears, interface and API are fully ready.

**Without login** (default) that is all there is to it: every person who can
reach the server works with full rights. There is no administrator in
this mode; the admin functions are then not reachable.

**With login** (`ABOARD_TRUST_FORWARD_AUTH=1`) the following applies:

- **First administrator:** if `ABOARD_BOOTSTRAP_OWNER` is set, only
  this person becomes administrator (at their first login). Without the variable,
  whoever is first to get an account becomes administrator — then log in
  yourself first, **before** you open up access for others. The server
  creates an account at the first board access of a new email address;
  every further person gets their own, empty board and no
  admin rights. Details:
  [authentication.md](authentication.md).
- **Optional: set the owner of the `default` board.** With
  `ABOARD_BOOTSTRAP_OWNER=person@example.org` the server enters this address
  as owner when it creates the `default` board. This part takes effect
  **only** at the very first start with an empty database; on an existing
  database the `default` board stays untouched (how to take over existing boards
  is described in [authentication.md](authentication.md)).
  If the service board for the mail intake is switched on (default),
  the server, by contrast, enters the same address as its owner at **every** start,
  as long as the variable is set. Therefore remove it after
  setup — **but only once there is an administrator**. An
  invalid value (without `@`, or a domain entry such as `@company.example`) produces
  an error line in the log, no owner entry, and as long as it is set,
  **nobody** automatically becomes administrator.

Details on roles and shares: [authentication.md](authentication.md).

---

## Healthcheck

`/healthz` is reachable without login — even when the proxy login mode
is active — and answers with status `200` and roughly:

```json
{"ok":true,"db":"up","version":"v1.50.0","commit":"abc1234","build_date":"2026-01-01T12:00:00Z"}
```

If the server cannot reach the database, status `503` comes with
`"ok":false,"db":"down"`.

**From outside** (TCP only):

```bash
curl http://127.0.0.1:8080/healthz
```

**From inside** — always works, also with a Unix socket or a different
`ABOARD_ADDR`, because the subcommand reads the same variables as the server:

```bash
docker exec aboard-server aboard-server healthcheck
echo $?   # 0 = healthy, 1 = not healthy or not reachable
```

> **Container name:** `docker exec aboard-server …` assumes the name from the `docker run --name aboard-server` example. With the Compose file the container has a different name (`<project>-aboard-server-1`); there you write `docker compose exec aboard-server aboard-server …` instead. This applies to all `docker exec` examples in the handbook.

Exactly this call is stored as the `HEALTHCHECK` in the image (every 30 s,
time limit 5 s, three failed attempts, 10 s start period). It opens nothing itself —
no database, no port — and only queries the running server.

A helper container that uses the same image for something else (for example the
backup service) inherits this healthcheck and would be permanently "unhealthy";
set `healthcheck: disable: true` there, as in the supplied
`docker-compose.yml`.

---

## Updates — and the way back only via a backup

At startup the server automatically brings the database to the schema of its
version. **These migrations only run forward.** An older version cannot convert a
database migrated by a newer version back. Therefore, before **every** update:

1. **Take a backup** while the server is running (the backup is a consistent
   copy, operation does not need to be interrupted):

   ```bash
   docker exec aboard-server aboard-server backup --out /data/backups
   ```

   The archive contains database and attachments **in plain text**. Treat it
   accordingly.
2. **Apply the update:** raise the tag (and, if applicable, the digest), restart,
   e.g. `docker compose pull && docker compose up -d`.
3. **Check:** start log (version line, no errors) and `/healthz`.

**If the new version has to go back,** it is not enough to start the old image:

1. Stop the server.
2. Preferably use fresh, empty volumes; the old ones then remain as a
   fallback until the old version is running safely. If you restore over the old
   data: `restore` first sets aside an existing `aboard.db-wal` and
   `aboard.db-shm` (renamed to `…vor-restore-<time>`, not
   deleted) — otherwise SQLite would apply an old WAL to the restored
   database. Attachments that are not in the archive stay in place;
   `restore` names them in the log. ⚠️ **`restore` can only set files aside from
   v1.50.0.** Anyone restoring with an older version (step 3 uses the
   *old* version) renames `aboard.db-wal` and `aboard.db-shm` themselves beforehand,
   with the server stopped — do not delete. Measured in the
   rollback test v1.50.0 → v1.49.0 on 2026-10-09.
3. Restore the backup from step 1 with the **old** version — first as a
   dry run, then for real:

   ```bash
   docker compose stop aboard-server
   docker run --rm \
     -v <new-db-volume>:/data/db -v <new-attachments-volume>:/data/attachments \
     -v <project>_backups:/data/backups \
     registry.example.org/aboard:<old-version> \
     restore --dry-run /data/backups/<archive>.tar.gz
   # … and then the same with --force instead of --dry-run
   ```

   (`<project>_backups` is the volume name shown by `docker volume ls`.
   The new volumes must afterwards also be listed in the Compose file, or you
   empty the existing ones instead of creating new ones.)
4. Start the old version.

If an older version starts on a database with a newer schema, **it aborts
the start with a message** instead of silently running on a foreign schema.
The emergency switch `ABOARD_SCHEMA_NEUER_ZULASSEN=1` exists for the
case that no backup exists — it is dangerous: an older version does not know
checks that newer schemas presuppose, and may therefore allow more than
intended. Use it only in full knowledge of this
risk. One example: a version before schema 38 does not know the token role
`intern` (board pair, external server) and would let such a token read the
whole board — **before such a rollback, revoke all tokens of the role `intern`**
(`aboard-server agent-token list`, then `revoke`).

Backup and restore in detail: [administration.md](administration.md).

---

## Security at a glance

- **Never unprotected on the internet.** Without login, every person with
  network access has full rights. Either operate only in a trusted network (LAN, VPN,
  `127.0.0.1`) or put a reverse proxy with login in front
  — [authentication.md](authentication.md).
- **Open ports only as far as necessary.** `-p 127.0.0.1:8080:8080` instead of
  `-p 8080:8080`, behind a proxy preferably no port at all, but the
  Unix socket.
- **No root.** Do not run the container with `--user 0:0`.
- **Protect backups.** The server backup is plain text. For the
  encrypted board backups see [administration.md](administration.md).
- **Pin the version.** In operation, use a release tag with a digest,
  not `latest`.
