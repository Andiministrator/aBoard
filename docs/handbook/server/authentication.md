# Authentication, multi-user operation and permissions

This chapter describes how the aBoard server finds out **who** is making a request, and how it
derives from that **what** this person may do. It is aimed at everyone who runs the server for
more than one person or wants to make it reachable from the internet. It knows three ways:
without sign-in (one person), with an upstream authentication service (forward auth), and with
the built-in sign-in via OpenID Connect (one container, no proxy in front, one or more providers).

How to start the container in general is described in [`container.md`](container.md); the
complete list of all environment variables is in [`configuration.md`](configuration.md).

---

## Contents

1. [Why an upstream authentication service?](#why-an-upstream-authentication-service)
2. [The two operating modes at a glance](#the-two-operating-modes-at-a-glance)
3. [Single-user operation](#single-user-operation)
   (the built-in sign-in has its own section:
   [Built-in sign-in with OpenID Connect](#built-in-sign-in-with-openid-connect))
4. [Multi-user operation with forward auth](#multi-user-operation-with-forward-auth)
5. [Example: forward auth over a Unix socket](#example-forward-auth-over-a-unix-socket)
6. [Example: forward auth over localhost TCP](#example-forward-auth-over-localhost-tcp)
7. [Who may use aBoard at all?](#who-may-use-aboard-at-all)
8. [Roles: owner, editor, viewer](#roles-owner-editor-viewer)
9. [Sharing boards](#sharing-boards)
10. [The first owner and the first administrator](#the-first-owner-and-the-first-administrator)
11. [Switching from single-user to multi-user operation](#switching-from-single-user-to-multi-user-operation)
12. [Testing locally without an authentication service](#testing-locally-without-an-authentication-service)
13. [Troubleshooting](#troubleshooting)

---

## Why an upstream authentication service?

In forward-auth operation the aBoard server manages **no passwords** and has **no sign-in form of
its own**. (The [built-in sign-in](#built-in-sign-in-with-openid-connect) is a different mode:
there the server knows its own accounts with a password and a second factor, and the address
stays the name.) It leaves this job to a specialised service that sits in front of it — typically a
reverse proxy such as [Caddy](https://caddyserver.com/) together with an authentication service such as
[Authelia](https://www.authelia.com/). This pattern is called **forward auth**:

```
Browser ──HTTPS──▶ Caddy ──(1) Is this person signed in?──▶ Authelia
                     │   ◀─(2) Yes: Remote-User, Remote-Email, … ──┘
                     │
                     └──(3) Request + identity headers + shared secret──▶ aBoard server
```

1. For every request, Caddy asks Authelia whether the person is signed in. If not,
   Authelia redirects them to its sign-in page (password, second factor, single sign-on —
   whatever you have set up in Authelia).
2. If they are signed in, Authelia returns the identity in HTTP headers.
3. Caddy passes the request on to aBoard with these headers **and** a secret known only to it and
   the aBoard server.

So aBoard trusts the identity headers without being able to check them itself. That is only
safe if **nobody except the proxy** can reach the aBoard server. This is why the server
enforces two safeguards in multi-user operation, described further below: the shared secret and
a check of the listen address.

The advantage of this setup: for aBoard you use the same sign-in (and the same user management,
e.g. LDAP or an OpenID Connect provider) as for your other services. Instead of Authelia, any
proxy works that sets the headers named below — the examples in this chapter use Caddy and
Authelia.

---

## The two operating modes at a glance

The first two modes are selected by **one** switch: `ABOARD_TRUST_FORWARD_AUTH`. The third, the
built-in sign-in, is switched on by `ABOARD_AUTH_MODE=builtin` (former name:
`ABOARD_ANMELDUNG=oidc`, equivalent); it has its own section
([below](#built-in-sign-in-with-openid-connect)). The built-in sign-in and
`ABOARD_TRUST_FORWARD_AUTH` together are not allowed.

| | Single-user operation | Multi-user operation |
|---|---|---|
| `ABOARD_TRUST_FORWARD_AUTH` | not set (or `0`, `false`, `no`, `off`) | `1` (or any other non-empty value) |
| `ABOARD_FORWARD_AUTH_SECRET` | irrelevant | **required** |
| Authentication service in front | not needed | **required** |
| Who gets in? | anyone who can reach the port | only requests with a valid secret and identity |
| Which boards can you see? | all | only your own and shared ones |
| Roles (owner/editor/viewer) | not checked | checked on every request |
| Server administration in the browser | not available | for administrators |
| Typical use | alone, on a home network, over a VPN, on your own machine | team, access from the internet |

There is no separate setting for access rights: as soon as the server knows an identity, it
checks the rights. There is no intermediate state "sign-in on, rights off".

In the table, the built-in sign-in behaves like multi-user operation: people and roles are
checked, and the server administration is available. Instead of a secret and an authentication
service in front, it needs an OIDC client at at least one provider or its own accounts with a
password. Whoever is on the pre-approval list or has an active account gets in (invitation with
confirmation by an admin).

> **Careful with the value `0`:** `ABOARD_TRUST_FORWARD_AUTH=0` means **off**. An empty value as
> well as `0`, `false`, `no` and `off` count as "off" (case does not matter). Any other value
> switches multi-user operation on.

### State matrix at startup

Before the server even starts listening, it checks its configuration. This gives the following
cases:

| `ABOARD_TRUST_FORWARD_AUTH` | Listen address | `ABOARD_FORWARD_AUTH_INSECURE_BIND` | `ABOARD_FORWARD_AUTH_SECRET` | Result |
|---|---|---|---|---|
| off | any | irrelevant | irrelevant | starts in **single-user operation** |
| on | Unix socket or loopback (`127.x.x.x`, `localhost`, `::1`) | irrelevant | set | starts in **multi-user operation** |
| on | all interfaces or a foreign address (e.g. `:8080`, `0.0.0.0:8080`) | not set | any | **startup aborts** (insecure listen address) |
| on | all interfaces or a foreign address | `1` | set | starts in **multi-user operation**, with a loud warning in the log |
| on | any (safe or released) | — | missing or only spaces | **startup aborts** (secret missing) |

Both aborts name the cause and the remedy in the log.

With the built-in sign-in, a second matrix applies. It is checked first. `Switch on` means:
`ABOARD_AUTH_MODE=builtin` or `ABOARD_ANMELDUNG=oidc` (or both with the same meaning).

| Switch (`ABOARD_AUTH_MODE` / `ABOARD_ANMELDUNG`) | Further conditions | Result |
|---|---|---|
| both empty | no variable `ABOARD_OIDC_*`, `ABOARD_AUTH_*`, `ABOARD_INVITE_*` set | The matrix above applies. |
| both empty | any variable `ABOARD_OIDC_*`, `ABOARD_AUTH_*` or `ABOARD_INVITE_*` set | **startup aborts** (the server would run without sign-in) |
| on | everything below fulfilled | starts with **built-in sign-in**; forward auth is off |
| `builtin` and `oidc` at the same time | — | equivalent, no error |
| a value under one of the two names is invalid | — | **startup aborts** |
| on | `ABOARD_TRUST_FORWARD_AUTH` on | **startup aborts** (two sign-in paths) |
| on | `ABOARD_DEV_FAKE_USER` or `ABOARD_DEV_FAKE_EMAIL` set | **startup aborts** |
| on | a provider is half set up, its issuer is not an https address, a variable belongs to a short name that is not listed, or a name `ABOARD_OIDC_…` is unknown | **startup aborts** |
| on | `ABOARD_AUTH_METHODS` names a provider that is not set up, a duplicate value or no method | **startup aborts** |
| on | a name `ABOARD_AUTH_…` or `ABOARD_INVITE_…` is unknown (typos such as `ABOARD_AUTH_METHOD`; known are `ABOARD_AUTH_MODE`, `ABOARD_AUTH_METHODS`, `ABOARD_INVITE_METHODS`) | **startup aborts** |
| on | `ABOARD_INVITE_METHODS` names a method that `ABOARD_AUTH_METHODS` does not offer, a duplicate value or no method | **startup aborts** |
| on | `ABOARD_SECRET_KEY` missing or invalid | **startup aborts** |
| on | `ABOARD_PUBLIC_BASE_URL` missing, not `https://`, or carries a path | **startup aborts** |
| on | an entry in the pre-approval list is invalid, or `ABOARD_OIDC_ALLOWED` and `ABOARD_OIDC_ZUGELASSEN` contradict each other | **startup aborts** |
| on | pre-approval list and `ABOARD_BOOTSTRAP_OWNER` empty, and there is no active account | starts, with a warning in the log; **nobody gets in** |
| on | a short name in `ABOARD_OIDC_PROVIDERS` without variables of its own | starts, with a warning in the log; the provider is not offered |
| on | a provider is not reachable at startup | starts; its sign-in answers `503` until it is reachable (no restart needed), the other providers work |
| a switch with any other value | — | **startup aborts** (allowed: empty, `builtin`, `oidc`) |

The abort names all defects found in the log at once.

---

## Single-user operation

Without `ABOARD_TRUST_FORWARD_AUTH` the server works **without any sign-in**. It knows no
people and no roles; every request may do everything — read, change and delete all boards and
upload attachments. This is the default and is convenient for one person on their own machine or
in their own network:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:<version>
    ports:
      - "127.0.0.1:8080:8080"   # reachable from your own machine only
    volumes:
      - aboard-db:/data/db
      - aboard-anhaenge:/data/attachments
    restart: unless-stopped

volumes:
  aboard-db:
  aboard-anhaenge:
```

> 🛑 **Never make a server in single-user operation reachable from the internet.**
> Anyone who can reach the port has full access to all data. It is only allowed in a network
> you trust (own machine, home network, VPN). If you want to access it from outside, use
> multi-user operation — even if only one person uses the board.

Some functions require a known person and are not available in single-user operation, in
particular the server administration in the browser (see
[`administration.md`](administration.md)).

---

## Multi-user operation with forward auth

### The identity headers

In multi-user operation the server reads these headers, which the proxy takes over from the
authentication service:

| Header | Required | What aBoard uses it for |
|---|---|---|
| `Remote-Email` | **yes** (for everything concerning boards) | The email address **is** the identity: assignment of the account, of all shares and roles. Case and spaces at the edges are ignored. |
| `Remote-User` | no | Only a display name, if `Remote-Name` is missing (and the value does not consist only of digits — some authentication services deliver a numeric account ID here). |
| `Remote-Name` | no | Display name (avatar, people lists). If it is missing, aBoard shows `Remote-User` or the email address. |
| `Remote-Groups` | no | Is read, but **not** evaluated for rights. If you want to let only certain groups in, regulate that in the authentication service (see [below](#who-may-use-aboard-at-all)). |
| `X-Aboard-Forward-Auth-Secret` | **yes** | The shared secret, see the next section. Set by the proxy itself, not by the authentication service. |

A request needs at least `Remote-User` or `Remote-Email`, otherwise it is rejected.
For all board access `Remote-Email` is required — if it is missing, the server answers with
`403` there. So make sure your authentication service delivers the email address and that
this address stays **stable** (see [Who may use aBoard at all?](#who-may-use-aboard-at-all)).

### Strict mode: what gets through without sign-in

In multi-user operation the server answers **every** request with `401` where

- the secret is missing or does not match, **or**
- neither `Remote-User` nor `Remote-Email` is set.

This also applies to the web interface itself. The answer deliberately does not reveal which of
the two cases applied; the reason is in the server log (see [Troubleshooting](#troubleshooting)).

Only a few **read** paths (`GET` and `HEAD`) are exempt, because they have to be reachable
without sign-in:

| Path | Why open | Own protection |
|---|---|---|
| `/healthz` | health check by the container runtime and proxy | returns no board data |
| `/ical/calendar.ics` | calendar subscriptions (calendar programs cannot sign in at the authentication service) | own token in the URL |

On these paths the server deliberately does **not** accept an identity that is sent along.

If you use the calendar feed, the proxy must route this path past the authentication service —
otherwise the calendar program ends up on the sign-in page. An example is in the
[Caddyfile below](#caddyfile). The agent access under `/agent/` is described in
[`api.md`](api.md); it, too, bypasses the sign-in and needs a proxy rule of its own there.

**Board pair internal/external** (see [Administration](administration.md#board-pair-internal-and-external)):
The push script also comes in through the agent entry, with its own tokens (`abs_…` and
`abr_…` inside, `abe_…` outside; the paths `aenderungen` and `anwenden` of the return path only from v1.55.0). The server needs **no** further open path for this. The same applies to fetching the board backups with `abb_…` (`GET`/`HEAD /agent/sicherungen` and `/agent/sicherungen/*`, on either side, from v1.56.0). At the proxy the following applies:

| Server | What has to bypass the sign-in | If the proxy already routes `/agent/*` past it |
|---|---|---|
| internal (side `innen`) | `GET /agent/schatten/*`; with external copies additionally `GET`/`HEAD /agent/boardpaar/auftraege`, `GET`/`HEAD /agent/boardpaar/spiegel/*`, `PUT /agent/boardpaar/ziele`, `POST /agent/boardpaar/auftraege/*/erledigt`, `POST /agent/boardpaar/anwenden/*` and `POST /agent/boardpaar/erneuern` | nothing more to do |
| external (side `aussen`) | `PUT /agent/schatten/*`; with external copies additionally `POST /agent/boardpaar/boards`, `PUT /agent/boardpaar/spiegel/*`, `GET`/`HEAD /agent/boardpaar/aenderungen/*`, `POST /agent/boardpaar/aenderungen/*/bestaetigt` and `POST /agent/boardpaar/erneuern`, nothing else on this path | nothing more to do; then only token, barrier and failed-attempt throttle protect, as with all agent paths |

If you let through exactly these paths instead of `/agent/*` as a whole, restrict the branch
with a matcher, on the external server for example:

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

The example shows only the shadow path of stage 1; for the paths of an external copy, their paths and
methods from the table are added. On the internal server the shadow path uses `method GET`. None of these
paths evaluates an identity that is sent along. The display of the shadows in the browser (`GET /boards/{id}/schatten`), in contrast, goes
through the sign-in as normal.

### The shared secret

The secret is a random value that **only** the proxy and the aBoard server know. The
proxy sends it with every request in the header `X-Aboard-Forward-Auth-Secret`; the server
compares it with `ABOARD_FORWARD_AUTH_SECRET`. If it does not match, the answer is `401`.

Why this is necessary even though the server only listens internally anyway: on the same machine,
other programs or containers can reach the internal port or socket as well. Without the
secret they could pose as any person with a self-set `Remote-Email` header — including
owner rights. That is why there is **no** way to switch the secret off; without a secret the
server does not start in multi-user operation.

To generate a secret, e.g.:

```bash
openssl rand -hex 32
```

Put the value in an `.env` file next to your `docker-compose.yml` (do not check it into
version control) and pass it on **to both** containers — to aBoard **and** to the proxy:

```dotenv
# .env
ABOARD_FORWARD_AUTH_SECRET=<secret>
```

Spaces and a trailing line break at the end of the value are ignored on the server side.

> **Order when setting up or changing the secret:** first set the value in **both**
> configurations, then restart both containers. If the secret is missing in the aBoard container,
> the server does not start; if it is missing in the proxy, every person gets `401`.

### The listen address (`ABOARD_FORWARD_AUTH_INSECURE_BIND`)

In multi-user operation the server checks at startup whether it is reachable only internally.
The following count as **safe**:

- a Unix socket (`ABOARD_UNIX_SOCKET` set — then `ABOARD_ADDR` is not used), or
- a TCP address on the loopback interface: `127.0.0.1:8080` (generally `127.x.x.x`),
  `localhost:8080`, `[::1]:8080`.

Everything else counts as **insecure**, in particular the default `:8080` (all
interfaces), `0.0.0.0:8080`, `[::]:8080` or a concrete network address. In this case
**startup aborts**.

With `ABOARD_FORWARD_AUTH_INSECURE_BIND=1` you can deliberately override this check. The
server then starts and writes a clear warning to the log. This is intended for setups
in which the network is sealed off **in some other way** and the server just cannot recognise
that itself, for example:

- aBoard listens in the container on `:8080`, but the port is published only on the loopback
  interface of the machine (`-p 127.0.0.1:8080:8080`). Inside the container `:8080` looks
  insecure; from outside it is not.
- aBoard and the proxy are in a container network of their own, and aBoard publishes no port.

The secret remains mandatory even then and is the actual protective layer in such setups.

> **Containers and `127.0.0.1`:** Inside a container, `127.0.0.1` means the container
> itself. A Caddy in a *different* container therefore does **not** reach `ABOARD_ADDR=127.0.0.1:8080`.
> The localhost TCP variant only works if Caddy and aBoard share a network namespace —
> e.g. when aBoard runs in the host network and Caddy directly on the machine. For two separate
> containers the Unix socket is the easier choice.

---

## Example: forward auth over a Unix socket

Recommended setup when the proxy (here Caddy) and aBoard both run as containers: aBoard opens **no**
TCP port, but a Unix socket in a volume that both containers share.

Prerequisites:

- Caddy from version 2.7 (directive `forward_auth`)
- a set-up Authelia at `auth.example.org` that checks the requests for
  `board.example.org`
- DNS for `board.example.org` points to the machine on which Caddy runs

### docker-compose.yml

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:<version>
    environment:
      # Unix socket instead of a TCP port — safe bind, no port to the outside
      ABOARD_UNIX_SOCKET: /run/aboard/aboard.sock
      # Switch on multi-user operation …
      ABOARD_TRUST_FORWARD_AUTH: "1"
      # … with mandatory secret (same value as in the proxy container)
      ABOARD_FORWARD_AUTH_SECRET: "${ABOARD_FORWARD_AUTH_SECRET}"
      # Only effective at the very first start with an empty database, see below
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
      # The proxy inserts the value when loading the Caddyfile
      ABOARD_FORWARD_AUTH_SECRET: "${ABOARD_FORWARD_AUTH_SECRET}"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - proxy-data:/data
      - aboard-socket:/run/aboard
    restart: unless-stopped

  # authelia: … (see the Authelia documentation)

volumes:
  aboard-db:
  aboard-anhaenge:
  aboard-socket:
  proxy-data:
```

**Permissions on the socket:** The aBoard process runs in the container as user and group `10001` and
creates the socket with mode `0660` (owner and group may read and write). A
Caddy container that runs as `root` (default of the official image) can use it. If Caddy runs as a different user, give it the group `10001`
(`group_add: ["10001"]`) or set `ABOARD_UNIX_SOCKET_MODE` in aBoard. Allowed are
`0600`, `0660` (default) and `0666`; an invalid value or a mode that cannot be
set aborts startup. Use `0666` only if the socket volume is available exclusively to
Caddy and aBoard.

The health check of the image (`aboard-server healthcheck`) reads the same variables as
the server and also works with a Unix socket.

### Caddyfile

```caddyfile
board.example.org {
    # Optional: let the calendar feed through without sign-in.
    # aBoard checks its own token here; only needed if you use calendar subscriptions.
    @kalender path /ical/calendar.ics
    handle @kalender {
        reverse_proxy unix//run/aboard/aboard.sock
    }

    handle {
        # route: Caddy executes the directives in it in exactly this order.
        route {
            # Discard identity headers that the browser itself sends along.
            request_header -Remote-User
            request_header -Remote-Email
            request_header -Remote-Groups
            request_header -Remote-Name

            # Have every request checked by Authelia. On success, Caddy
            # takes over the identity headers from Authelia's answer.
            forward_auth authelia:9091 {
                uri /api/authz/forward-auth
                copy_headers Remote-User Remote-Email Remote-Groups Remote-Name
            }

            # On to aBoard — with the shared secret.
            reverse_proxy unix//run/aboard/aboard.sock {
                header_up X-Aboard-Forward-Auth-Secret {$ABOARD_FORWARD_AUTH_SECRET}
            }
        }
    }
}
```

Notes on the Caddyfile:

- **Remove `Remote-*` headers that are sent along:** aBoard believes every `Remote-*` header that arrives with the
  right secret. If the authentication service does not deliver one of these headers (for example
  `Remote-Name`), depending on the proxy and version the value that the browser set itself could otherwise get through — the four
  `request_header -…` lines make sure that only the authentication service's data arrives. The
  `route` block is necessary because otherwise Caddy executes the directives in a fixed order of its own,
  and the removal could then happen only after `forward_auth`.
- **`{$ABOARD_FORWARD_AUTH_SECRET}`** is inserted when the configuration is loaded, from the environment of the
  **Caddy** container. Use this notation, not the
  runtime placeholder `{env.ABOARD_FORWARD_AUTH_SECRET}`.
- **Authelia endpoint:** `/api/authz/forward-auth` is the endpoint of current Authelia versions.
  Older versions use `uri /api/verify?rd=https://auth.example.org`. The Caddy guide in the Authelia
  documentation for your version is authoritative.
- **TLS** is obtained by Caddy automatically as soon as the name resolves publicly. aBoard itself
  speaks only unencrypted HTTP over the socket.
- **Session cookie of the authentication service:** aBoard itself rejects writing browser requests from foreign
  sites (CSRF protection, see [`api.md`](api.md#csrf-protection-for-browser-requests)).
  Still set the session cookie of your authentication service to `SameSite=Lax` or
  `SameSite=Strict`, so that the browser does not send it at all with requests from foreign sites. What the setting is called is in the
  documentation of your authentication service.
- **No buffering for the event stream:** Live changes arrive as Server-Sent Events
  (`/boards/{id}/events`). A proxy that collects responses before passing them on holds back these
  events, and the board no longer updates by itself. Caddy passes on
  responses of type `text/event-stream` immediately anyway; `flush_interval -1` in
  `reverse_proxy` enforces that explicitly for every response (every written chunk goes straight to
  the client). With nginx the equivalent is `proxy_buffering off`.

---

## Example: forward auth over localhost TCP

If Caddy runs directly on the machine (not in a container) and aBoard in the host network, aBoard can
listen on the loopback interface:

```yaml
services:
  aboard-server:
    image: registry.example.org/aboard:<version>
    network_mode: host            # shares the loopback interface with the machine
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

The secret must then be in the environment of the Caddy service on the machine (e.g. in the
service configuration of your init system).

Alternative without the host network: aBoard listens in the container on `:8080`, and the port is published only on the
loopback interface (`ports: ["127.0.0.1:8080:8080"]`). Because the server sees a listen address on all interfaces
inside the container, this setup additionally needs `ABOARD_FORWARD_AUTH_INSECURE_BIND: "1"` (see
[The listen address](#the-listen-address-aboard_forward_auth_insecure_bind)).

---

## Built-in sign-in with OpenID Connect

With `ABOARD_AUTH_MODE=builtin` the server signs people in itself. The former name
`ABOARD_ANMELDUNG=oidc` means the same and remains valid; you do not have to change anything. The server
needs no proxy and no authentication service in front, so one container instead of several. Sign-in
runs through one or more OpenID Connect providers, for example Google and Authelia
side by side, and through own accounts with email address and password (method `password`). With a
provider, the provider manages the password and second factor; the server never sees them. With own
accounts, the server stores only an argon2id hash of the password. After the password it always
demands a second factor: the six-digit code of an authenticator app or one of the ten
recovery codes. Inviting, confirming, blocking and resetting is done by an admin in the
tab "User management" of the server administration; how to use it is described in
[Managing the server](administration.md#accounts-and-invitations-built-in-sign-in), the rules for who
gets in [further below](#who-gets-in-pre-approval-list-and-accounts).

The two switches:

- You can set both `ABOARD_AUTH_MODE` and `ABOARD_ANMELDUNG` if they mean the same
  (`builtin` and `oidc` are equivalent; there are no other values). If a value is invalid,
  startup aborts.
- If you set a variable `ABOARD_OIDC_*`, `ABOARD_AUTH_*` or `ABOARD_INVITE_*` without switching
  the feature on, startup aborts. Otherwise the server would run without any sign-in.

### How a sign-in works

1. A person opens the board without a session. The server redirects them to the sign-in page
   `/anmeldung` (`302`). Programs and scripts that call the programming interface get
   `401` instead.
2. If exactly one provider is set up, `/anmeldung` immediately forwards the person to that
   provider. If there are several, the page shows one button per provider
   ([The sign-in page](#the-sign-in-page)). The person signs in at the provider.
3. The provider sends them back to `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<short-name>`
   (for the provider `standard` to `/anmeldung/rueckruf`). The server checks the process, fetches the
   confirmed email address and checks whether the person
   [gets in](#who-gets-in-pre-approval-list-and-accounts). A sign-in process is valid for 10 minutes.
4. If they get in, the server creates a session and redirects to `/`. The first time,
   an account and a personal board are created as
   [described above](#who-may-use-aboard-at-all).

Without a session only `/healthz` (health check), `/ical/calendar.ics`
(calendar subscription with its own token) as well as `/anmeldung`, `/anmeldung/start`,
`/anmeldung/rueckruf` and `/anmeldung/rueckruf/<short-name>`, `POST /anmeldung/passwort` (password form),
`POST /anmeldung/code` (second factor), the page `/anmeldung/abgemeldet` after signing out and
`GET`/`POST /einladung/<token>` (redeeming an invitation) remain reachable. An unknown, expired or already
used invitation link answers `404`, as do `POST /anmeldung/passwort` and
`POST /anmeldung/code` if `ABOARD_AUTH_METHODS` does not name the method `password`. Every other call without
a session is handled as everywhere: a page request in the browser is redirected to `/anmeldung`,
everything else gets `401`.

Sessions:

- A session ends after **8 hours without use**, at the latest **7 days** after sign-in.
- The "Sign out" button in the sidebar ends it immediately (`POST /anmeldung/ende`) and shows the
  page "Signed out". With the sign-in service (e.g. Google) you stay signed in — on a shared
  machine, sign out there as well. The link "Sign in again" on this page asks the provider for
  the account, instead of silently signing in again with the open provider session.
- A new sign-in in the same browser ends the previous session.
- If the session expires in an open browser tab, the board shows a banner "Session
  expired — sign in again". Unsent changes are kept in the tab. The sign-in
  opens in a new tab. After signing in, **close this new tab** and carry on in the old one
  — otherwise two tabs with the same board are open (the banner says so too).
- If you delete a person in the server administration, their sessions and
  their binding to the provider account disappear as well.

### The sign-in page

`/anmeldung` shows a button "Sign in with <label>" for every offered provider.

- **If exactly one method remains and it is a provider** (because `ABOARD_AUTH_METHODS` names only it):
  The page shows nothing but forwards immediately to it. Without this restriction
  `password` is always a method, so the page appears with a password form.
- **Language:** German or English, according to the browser's language setting. If neither
  is set, German appears.
- **Provider not reachable:** The page never queries the providers, so it always appears.
  If a provider is not reachable, the person notices only on clicking (message "Sign-in currently
  not possible", status `503`). The buttons of the other providers keep working.
- **Label:** `ABOARD_OIDC_<ID>_LABEL`. If it is missing, "Google" appears (for the Google address)
  or else the host name of the provider, such as `auth.example.org`. So for every
  provider except Google set a label your people know.

With `ABOARD_AUTH_METHODS` you choose which providers the page offers:

- Empty (default) means: **all set-up methods**, that is the password form and every
  completely set-up provider. The method `password` is always set up when sign-in is switched on;
  a server with no provider at all therefore starts too, with password only.
- Otherwise you enter the desired methods, separated by commas, for example
  `ABOARD_AUTH_METHODS=google` or `password,google`. The provider from the three variables
  `ABOARD_OIDC_ISSUER`, `_CLIENT_ID` and `_CLIENT_SECRET` is called `standard` here.
- A provider you do not name is switched off entirely: its buttons are missing, and its start and
  callback answer `404`.
- If `password` is missing from the list, there is no password form, and `POST /anmeldung/passwort`
  answers `404`.
- Startup aborts with a value that is not a set-up method (typo), with duplicate
  values and with a list without any method.
- A typo in the **name** also stops startup (`ABOARD_AUTH_METHOD` instead of
  `ABOARD_AUTH_METHODS`). Otherwise the server would silently offer all providers, including the one you
  wanted to exclude.
- If exactly one method remains and it is a provider, `/anmeldung` forwards directly to it.

> **For operators with only one provider:** A server configured unchanged now shows
> the sign-in page with password form and provider button, instead of forwarding directly. That is
> intended: by default all sign-in methods are available. Whoever wants to keep signing in only through the provider
> sets `ABOARD_AUTH_METHODS=<short-name>` (for a provider from the old variables
> `standard`); then `/anmeldung` forwards directly again.

**Password form, barriers:** After five wrong entries from one connection within 15 minutes,
this connection is blocked (`429`) until 15 minutes after the oldest of these five failed attempts. The address of the connection is determined by the same
rule as for the agents: `X-Real-IP` only from a peer in `ABOARD_AGENT_PROXY`, otherwise the
TCP address, IPv6 per `/64`. If a proxy is in front and `ABOARD_AGENT_PROXY` is empty, all
people share one block. If it is set, **the proxy must overwrite `X-Real-IP` with the client
address** — with Caddy `header_up X-Real-IP {client_ip}` in the `reverse_proxy` block (if another
entry point sits in front of Caddy, it belongs in Caddy's `trusted_proxies`; `{remote_host}` would then be the same address for everyone), with nginx
`proxy_set_header X-Real-IP $remote_addr;`. Otherwise Caddy passes on an `X-Real-IP` sent by the browser
unchanged, and whoever changes it with every attempt gets around the block. Per entered email address there is no block, but an
increasing waiting time (1, 2, 4, … up to 30 seconds per attempt), so that nobody can lock out a foreign account
permanently. Both live in memory and start over after a restart. The
message is the same for an unknown address and a wrong password. The waiting time, too,
answers with `429`. From a connection from which the person has signed in completely
(with code) within the last 30 days, the waiting time of their address does not apply — so a stranger cannot lock out a known
account through failed attempts from other connections; the block of the connection still
applies. This applies only at the known connection: whoever is on the move with changing addresses (mobile network,
travel) gets the waiting time like everyone, and their own typos count for their other devices as well. This too lives in memory. Per connection and per address at most one check runs
at a time; a second one immediately gets `429` "a check is already running" (otherwise parallel
requests would get past the counters). At most two
password checks run at the same time (64 MiB of memory each); whoever waits longer than five seconds for a
slot gets `503` and tries again shortly after.

**Second factor:** After the correct password the server sets a cookie of its own
(`__Host-aboard_zweiter_schritt`, valid for five minutes, usable only once) and asks for the
code. Per account there is at most one started sign-in; a new one replaces the old one. An app code is valid exactly once (not a second time within its 30 seconds either),
a recovery code likewise. After five wrong codes the sign-in starts over at the
password; wrong codes also count towards the block of the connection and the waiting time of the
address. Only after the correct code is the session created; an old session brought along ends
at that point. A restart of the server discards started sign-ins (then enter the password once more).

> **Known limit — changing `ABOARD_SECRET_KEY`:** The secrets of the authenticator apps are stored
> encrypted in the database with this key. There is no way to re-key them.
> After a change the server no longer accepts an app code (page "The
> two-factor sign-in of this account is not readable"). The people
> then have to be reset in the admin tab and invited again. A recovery code
> is independent of the key and would still be accepted; do not rely on that, but reset
> instead. `ABOARD_SECRET_KEY_PREVIOUS` does not help here
> (it applies only to board backups). So change the key only if you must.

### Setting up

The basic settings apply to every provider. Example with two providers:

```yaml
    environment:
      ABOARD_AUTH_MODE: "builtin"
      ABOARD_PUBLIC_BASE_URL: "https://board.example.org"
      ABOARD_SECRET_KEY: "<32 Bytes hex oder base64>"
      ABOARD_BOOTSTRAP_OWNER: "person@example.org"
      ABOARD_OIDC_ALLOWED: "person@example.org, @example.net"
      # The providers (see below):
      ABOARD_OIDC_PROVIDERS: "google,authelia"
      ABOARD_OIDC_GOOGLE_CLIENT_ID: "<client-id>"
      ABOARD_OIDC_GOOGLE_CLIENT_SECRET: "<client-secret>"
      ABOARD_OIDC_AUTHELIA_ISSUER: "https://auth.example.org"
      ABOARD_OIDC_AUTHELIA_CLIENT_ID: "<client-id>"
      ABOARD_OIDC_AUTHELIA_CLIENT_SECRET: "<client-secret>"
      ABOARD_OIDC_AUTHELIA_LABEL: "Firmen-Konto"
```

- `ABOARD_PUBLIC_BASE_URL` is mandatory and must be `https://`, without a path. The callback
  address is derived from it. It must correspond **exactly** to the one entered at the provider.
- `ABOARD_SECRET_KEY` signs the sign-in process. It is the same key that protects stored
  credentials (see [`configuration.md`](configuration.md)).
- The client secret is a secret: not into Git, not into chats. Put it in an `.env` file with permissions `0600`.
- All connections to the providers run through the outgoing layer of the server. If
  `ABOARD_AUSGAENGE=aus` is set, `ABOARD_AUSGAENGE_ERLAUBT` must contain the hosts of **every** provider:
  the host of its issuer and the hosts from its discovery document
  (`<issuer>/.well-known/openid-configuration`: `token_endpoint`, `jwks_uri`). Otherwise sign-in
  at this provider stops with `503`.
- The variables in detail are in [`configuration.md`](configuration.md).
- At startup the server writes a line to the log for every provider, which also names the callback address
  you have to enter at the provider:
  `Anmeldung: OIDC-Anbieter "Firmen-Konto" (https://auth.example.org), Rückruf https://board.example.org/anmeldung/rueckruf/authelia`.

### Setting up providers

Every provider needs an OIDC client at the provider and three to four variables in the container.
As the **callback address** (redirect URI) you enter at the provider:
`<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<short-name>`.

**Google**

1. Create an OAuth client of type "Web application" in the Google Cloud Console.
2. Enter as authorised redirect URI: `https://board.example.org/anmeldung/rueckruf/google`.
3. In the container:

   ```yaml
       ABOARD_OIDC_PROVIDERS: "google"
       ABOARD_OIDC_GOOGLE_CLIENT_ID: "<client-id>"
       ABOARD_OIDC_GOOGLE_CLIENT_SECRET: "<client-secret>"
   ```

For the short name `google` the issuer `https://accounts.google.com` is preset, and the button is called
"Sign in with Google". You can override both with `ABOARD_OIDC_GOOGLE_ISSUER` and
`ABOARD_OIDC_GOOGLE_LABEL`.

**Authelia** (or another OIDC provider)

For aBoard, Authelia is an ordinary provider without special treatment. You choose the short name
yourself; `authelia` is just an example.

1. Create an OIDC client for aBoard in Authelia and enter as redirect URI:
   `https://board.example.org/anmeldung/rueckruf/authelia`. How to do that is in the
   Authelia documentation for your version.
2. In the container:

   ```yaml
       ABOARD_OIDC_PROVIDERS: "authelia"
       ABOARD_OIDC_AUTHELIA_ISSUER: "https://auth.example.org"
       ABOARD_OIDC_AUTHELIA_CLIENT_ID: "<client-id>"
       ABOARD_OIDC_AUTHELIA_CLIENT_SECRET: "<client-secret>"
       ABOARD_OIDC_AUTHELIA_LABEL: "Firmen-Konto"
   ```

The issuer must be an `https://` address. `ABOARD_OIDC_AUTHELIA_LABEL` is the label of the
button ("Sign in with Firmen-Konto"). Choose a name your people know; without `_LABEL` the
host name `auth.example.org` would appear there.

**Several providers:** Enter all short names in `ABOARD_OIDC_PROVIDERS`, separated by commas, and
set the variables per short name. The buttons appear in the order of this list; a provider `standard` comes first.

**The former single provider (`standard`):** The three variables `ABOARD_OIDC_ISSUER`,
`ABOARD_OIDC_CLIENT_ID` and `ABOARD_OIDC_CLIENT_SECRET` still form one provider. It is called
`standard` internally and keeps the callback address `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf`
(without a short name). **An existing installation therefore does not have to change anything, neither at the provider nor in the environment.** You can mix the old provider and new providers. You must not enter `standard`
in `ABOARD_OIDC_PROVIDERS`.

**Short names and variable names:**

- A short name consists of 1 to 32 characters: lowercase letters `a–z`, digits `0–9` and `-`.
  `standard` and `password` are reserved. Every short name may appear only once in the list.
- The part `<ID>` in the variable name is the short name in capitals, with `_` instead of `-`. The
  short name `heim-2` gives `ABOARD_OIDC_HEIM_2_ISSUER`, `ABOARD_OIDC_HEIM_2_CLIENT_ID` and so on.
- The short name is part of the callback address: `…/anmeldung/rueckruf/heim-2`.
- If you rename a short name later, change the callback address at the provider at the same time.

**What aborts at startup and what only warns:**

| Case | Consequence |
|---|---|
| A provider is half set up: one of its variables is set, but `_CLIENT_ID`, `_CLIENT_SECRET` or (except for `google`) `_ISSUER` is missing | **Abort.** Message: `Anbieter <kürzel> halb eingerichtet: <Variable> fehlt` |
| The issuer is not an `https://` address | **Abort** |
| A variable `ABOARD_OIDC_<ID>_…` for a short name that is not in `ABOARD_OIDC_PROVIDERS`, or a typo in the name (`ABOARD_OIDC_…` is unknown) | **Abort.** Message: `ABOARD_OIDC_… ist unbekannt — Tippfehler, oder fehlt das Kürzel in ABOARD_OIDC_PROVIDERS?` |
| An invalid, reserved or duplicate short name in `ABOARD_OIDC_PROVIDERS` | **Abort** |
| An unknown name `ABOARD_AUTH_…` or `ABOARD_INVITE_…` (known are `ABOARD_AUTH_MODE`, `ABOARD_AUTH_METHODS` and `ABOARD_INVITE_METHODS`) | **Abort.** Message: `… ist unbekannt — Tippfehler?` |
| `ABOARD_INVITE_METHODS` names a method that `ABOARD_AUTH_METHODS` does not offer, appears twice or contains no method | **Abort.** The message names the offered methods |
| Two providers with the same issuer (such as `standard` and `google`, both on Google) | **Abort.** The binding hangs on the issuer; per issuer there is only one provider |
| No provider set up at all | Start, with the password method only (if `ABOARD_AUTH_METHODS` is set and does not name `password`, **abort**) |
| A value in `ABOARD_AUTH_METHODS` is not a set-up method, appears twice, or the list contains no method | **Abort** |
| A short name is in `ABOARD_OIDC_PROVIDERS` but has not a single variable of its own | Start, **warning** in the log; the provider is not offered |
| A provider is not reachable at startup | Start; sign-in at this provider answers `503` until it is reachable (no restart needed) |

The abort names all defects found in the log at once.

### Who gets in? Pre-approval list and accounts

Whether a person gets in is decided by **one gate**. It lets in whoever is **on the pre-approval list
OR has an active account**. The account state takes precedence over the list, though: a **blocked account
never lets in**, nor does one that is **waiting for confirmation** or has been **reset** (state
"invited" with a reset link), even if the address is on the pre-approval list. A fresh
invitation of a listed person remains covered by the list. The gate checks on every request, at
startup and at sign-in.

An account has a state: **invited** → (person redeems the link) **waiting for confirmation**
→ (admin confirms) **active**; plus **blocked**. Only "active" lets in.

> **Binding only via invitation or pre-approval list.** If someone signs in through a provider whose
> provider account is not yet bound anywhere, the server lets them in only if their address is on
> the pre-approval list; then the binding happens the first time. An active account alone is not enough for this.
> Otherwise anyone who gets the same address confirmed at any provider would get into an account with a password and
> second factor. Invited people get their binding when redeeming.
>
> **Pre-approval list and second factor.** An address on the pre-approval list gets in through a still unbound provider account, **even if the same address has a password account with two-factor sign-in**:
> The pre-approval list replaces the invitation, and the provider path does not know the second factor of the
> password path (intended). Whoever does not want that does not put the address on
> the pre-approval list, but invites the person.

**Inviting:** The admin enters only an email address (in the admin tab "User management" or via
`POST /admin/einladungen`, up to 50 addresses per call). The server creates an account in the state "invited" and generates a
link `<ABOARD_PUBLIC_BASE_URL>/einladung/<token>`: valid for 7 days, usable only once, stored
only as a hash. The admin sees the link exactly once in the response; if SMTP is set up, it also
goes by mail to the address (independent of `ABOARD_NOTIFICATIONS_DELIVERY`, because inviting
is an explicit action of the admin). Inviting again creates a new link, and the old one is no longer
valid. An invitation does not carry an admin role (the only exception: the first admin, see
below). Inviting works only for addresses made of ASCII characters, without display names. Expired and used links as well as
used-up recovery codes are deleted by the server 30 days after expiry or use.

**First admin on an empty server:** Without an admin there is nobody who invites through the
interface. For this there is the command in the container:

```
docker exec aboard-server aboard-server nutzer einladen --email anna@example.org
```

It prints the link (not into the log). Give the link only to the intended person: on a server without
an admin, redeeming it makes them admin without further confirmation (with `ABOARD_BOOTSTRAP_OWNER` set,
only the owner address; any other invitation then waits for confirmation). `ABOARD_AUTH_MODE=builtin` and `ABOARD_PUBLIC_BASE_URL`
must be set, as for the server itself. If there is no admin yet when redeeming, the person
becomes active and admin directly (with `ABOARD_BOOTSTRAP_OWNER` set only if it is their address); otherwise
an admin confirms the access.

**Redeeming:** The link opens a page with the invited address and the methods from
`ABOARD_INVITE_METHODS`. The person chooses:

- **Password and two-factor:** Enter the password twice (at least 12 characters), then in an
  authenticator app enter the key shown (or the link "Open in the authenticator app") and
  enter the first code. Only with a correct code is it redeemed. After that
  the page shows the ten recovery codes **once**.
- **A provider link** ("Sign in with Google" etc., `GET /einladung/<token>?weg=<short-name>`): The person signs in at the provider; the
  server binds this provider account to the invited account, **even if the provider reports a different
  address**. The admin sees the reported address and name before confirming. If the
  provider account is already bound to someone here, it is not redeemed; the link then remains valid.

The link is valid only once. After that the **account waits for confirmation**, because a forwarded
link would otherwise give a stranger access. If the person starts a sign-in elsewhere while redeeming,
the provider path shows "Sign-in process no longer valid"; then open the invitation link once
more. The pages under `/einladung/` send no referrer (`Referrer-Policy:
no-referrer`), so that the link is not passed on through a reference.

**Confirming, rejecting, blocking, resetting** (in the admin tab "User management"; step by step in
[Managing the server](administration.md#accounts-and-invitations-built-in-sign-in). The paths, each `POST`):

| Call | Effect |
|---|---|
| `/admin/konten/<id>/bestaetigen` | waiting → active. Creates the person with a personal board. |
| `/admin/konten/<id>/ablehnen` | Deletes a waiting account together with link and binding; the same person or the same provider account can be invited again afterwards. Does not work for a reset person who already had access (then block or delete the user). |
| `/admin/konten/<id>/sperren` | Blocked; all sessions of the person end immediately. |
| `/admin/konten/<id>/entsperren` | Back to "waiting for confirmation" (with sign-in data) or "invited" (without). Never directly active: afterwards you confirm the access explicitly. |
| `/admin/konten/<id>/zuruecksetzen` | Password, two-factor, recovery codes and all provider bindings of the person lapse, their sessions end, the state becomes "invited". The response contains a new link (by mail, if SMTP is set up). After redeeming, the account waits for confirmation again. Also works for a blocked account (block first, then reset, e.g. on suspicion); the block is lifted afterwards, but access is given only after redeeming and confirmation. |

An admin cannot block or reset their **own** account. `GET /admin/konten` lists
all accounts with state, chosen method and reported address. An invitation that nobody has
redeemed yet is withdrawn by the admin with `DELETE /admin/einladungen/<id>` ("Revoke invitation").

**Checking the reported address:** Name and address that a provider reports when redeeming come unchecked
from the provider. The admin sees them before confirming; if the reported address differs from the invited
address, "Warning: differs from the invited address" is shown next to it. That is allowed, but
a reason to ask before confirming.
If the provider reports the address without confirmation (`email_verified` missing or not true), it says
"Achtung: Adresse vom Anbieter nicht bestätigt" (English "Warning: address not verified by the
provider"), and the row is highlighted — even if the address equals the invited one, because a
provider with unverified addresses can claim any address. If it also differs, both
notes appear. In `GET /admin/konten` this appears as a separate field `gemeldete_email_unbestaetigt`; the
address itself carries no suffix (new since v1.63.0).

The **pre-approval list** is in `ABOARD_OIDC_ALLOWED` (former name: `ABOARD_OIDC_ZUGELASSEN`,
equivalent). If both are set with different content, startup aborts. Entries are
single addresses and whole domains (`@example.net`), separated by comma, semicolon or
whitespace. The address from `ABOARD_BOOTSTRAP_OWNER` is always added.

> 🛑 **The status of your OAuth client at the provider is not a barrier.** According to the Google help
> "Manage App Audience" (support.google.com/cloud/answer/15549945, read on 02.11.2026), even a client in the status "Testing" admits every Google account if it only
> requests the scopes `openid`, `email` and `profile`. Do not rely on a list of test users.

> ⚠️ **A domain entry (`@example.net`) lets in every account with a confirmed address of this domain**
> — also a private Google account that someone created with their company address and that
> persists after they leave. Whoever wants to rule that out enters individual addresses.
>
> **Once bound, stays in:** The gate checks the address the person has **in aBoard**,
> not the one the provider reports today ([binding](#one-account-one-address-binding-to-the-provider)).
> If someone later switches their bound provider account to an address outside the domain, they
> still get in. A domain entry cannot be struck for a single person. You then withdraw the access by
> moving the person with `aboard-server nutzer umziehen` to an address
> outside the pre-approval list ([move](#moving-an-account-to-a-different-address)): The move ends
> their sessions immediately, and the gate no longer lets them in under the new address; their
> boards remain. A restart is not needed for this. The harder way is deleting the person (admin dialog, tab "User management", see
> [Administration](administration.md#deleting-people-privacy)).

Addresses with characters outside ASCII (such as umlauts in the address) are accepted by aBoard neither in the
list nor at sign-in: otherwise, when lowercasing, different addresses would become equal.

For operation this means:

- **Grant access:** enter the address in `ABOARD_OIDC_ALLOWED`, restart the server.
- **Withdraw access:** strike the address from `ABOARD_OIDC_ALLOWED`, restart the server. Afterwards
  the server also ends running sessions of this person: the next request is redirected to
  `/anmeldung` (page request in the browser) or gets `401` (request from the interface).
  Their account and their boards remain.
- **Board shares and domain shares do not open the server.** An entry in the share
  of a board or in `ABOARD_ACL_DOMAINS` gives a person rights on the board, but no
  entry.
- The server reads the list at startup. Changes take effect only after the restart.
- The provider must report the address as confirmed; otherwise there is no access.
- An invalid entry stops startup. If the list is empty, there is no
  bootstrap owner and no active account, the server starts with a warning in the log, and nobody
  gets in.

Whoever does not get in sees the page "No access" after signing in at the provider. No account
is created. In the log there is at most one line per minute with the rejected address.

### One account, one address: binding to the provider

At the first sign-in the server remembers which provider account (issuer `iss` and identifier
`sub`) belongs to which person. From then on this **binding** determines the person, not the address
that the provider reports:

- **Bound account, different address:** If a provider account that is already bound later reports
  a different address (the provider changed it or delivers an alias once), it remains
  the same person with the same boards and rights. A move with `nutzer umziehen` is not
  necessary for this. Whether the person gets in is decided by the [gate](#who-gets-in-pre-approval-list-and-accounts)
  for their address **in aBoard**, not for the reported one.
- **Second provider account with the same address:** There is **no linking via the address.**
  If the same address that is already bound to a provider account signs in with a different provider account (also at a second provider), it gets `403` "No access" ("This address
  belongs to a different account here"). The log says what helps: link a new provider account in the account settings (the person signs in with their previous method for this), release the old one with `nutzer bindung-loesen` (see below) or, if the address has changed, `nutzer umziehen`. This
  prevents someone with a taken-over address from inheriting the boards of the previous holder. Today
  this applies to every person exactly once: a person has one provider account, not one per provider.
- **Several providers: the weakest counts.** At the first login **every** set-up provider may
  bind any address of the pre-approval list. Whoever binds first wins; the real person then gets
  "No access". So include only providers whose confirmation of the address you trust, and
  restrict to the desired ones with `ABOARD_AUTH_METHODS`.
- **Short names after the switch:** Whoever switches from the old names (provider `standard`) to a short name such as
  `google` keeps all bindings, because they hang on the issuer. In the database
  these bindings still carry the short name `standard`. That is an old label from the time before the
  provider short names; the binding applies per person and changes nothing about signing in. In the admin tab
  it appears as "former provider" as the sign-in method of the account.
- **First sign-in without a binding:** If there is no binding, the address reported and confirmed by the provider
  applies. It must pass the gate, and the first login binds the provider account to this person.
- **Waiting for confirmation:** If an invited person redeems the link through a provider, the
  binding at first belongs only to their account, not to a person in aBoard. If they sign in before
  confirmation, they see the page "Waiting for confirmation". With the admin's confirmation
  the binding is attached to the person.
- **Invitation with a different reported address:** When redeeming, the server binds the provider account to
  the invited account, even if the provider reports a different address. If the provider account
  is already bound to someone, it is not redeemed.

### Releasing the binding to a provider account

People link and release their provider accounts themselves in the **account settings** (menu at the logo). There it is also possible to switch to a new Google account: link the new account, it replaces the previous one. People **with a password** confirm every change with password and code. Whoever signs in only through a provider (such as people from the pre-approval list) confirms with a **fresh sign-in at the already bound provider**: aBoard demands `max_age=0` and explicitly the claim `auth_time` (`claims` parameter), plus `prompt=login` if the provider offers it in its discovery document (otherwise `prompt=select_account`). Only an ID token with the same provider account (`sub`) and an `auth_time` that is at most 5 minutes old is accepted. People with an account that is not active (invited, waiting, blocked) cannot do this. **The provider must deliver `auth_time`**, otherwise the confirmation fails as long as `ABOARD_OIDC_REAUTH_FALLBACK` is off (default): the interface then reports "Your sign-in service does not confirm a fresh sign-in" (return `/?konto=nicht_unterstuetzt`), and nothing changes. With the switch, a token that the provider issued in exactly this process is enough ([Configuration](configuration.md)). Audit reasons for a failure: `sub` (different provider account), `auth_time_alt`, `auth_time_ungueltig` (present but unreadable; never the fallback), `auth_time_fehlt` (switch off), `iat_alt` (switch on, token not from this process); success without `auth_time`: `ok_ohne_auth_time`.

- **Google:** delivers `auth_time` according to the Google documentation only if it is requested (aBoard does this) **and** switched on in the Google Auth Platform: "Settings" → "Advanced Settings" → **"Session age claims"**. According to the Google documentation (page "Security package", read on 05.11.2026) this works only if the app is published ("In production") **and** verified ("Verified"). **Measured on 09.11.2026:** A Google client without "Session age claims" switched on delivers no `auth_time`. The confirmation then always fails, in the audit with `konto-frisch-fehlgeschlagen` and `ergebnis=auth_time_fehlt`. Signing in again at Google does not help. **Remedy without verification:** `ABOARD_OIDC_REAUTH_FALLBACK=1` ([Configuration](configuration.md)). Then a token that Google issued in exactly this process is enough, with the limit stated there; the age of the Google session then plays no role. **Tried on 10.11.2026 on a server (v1.62.0) with `ABOARD_OIDC_REAUTH_FALLBACK=1`:** "Create password with second factor" succeeds with Google without a verified app (visual check; the audit line was not read in the process). **If Google delivers `auth_time` (verified app):** Google does not ask for the password again if the person is signed in there; if their last real Google sign-in is more than 5 minutes ago, the confirmation fails ("not recognised"). The interface then advises signing out of Google and in again and trying once more.
- **Authelia:** **not measured** — try it once before use; if `auth_time` is missing, `ABOARD_OIDC_REAUTH_FALLBACK=1` helps, otherwise the command below remains for these people. Success and failure are in the audit (`konto-frisch-bestaetigt`, `konto-frisch-fehlgeschlagen`).

**Own password later (switch `ABOARD_ACCOUNT_SELF_PASSWORD`, off).** If the switch is on, people without a password can, after such a fresh sign-in, create a password with mandatory 2FA themselves ([Configuration](configuration.md)). ⚠️ **Consequence for the pre-approval list:** Afterwards the person has an **active account**. Striking the address from `ABOARD_OIDC_ALLOWED` then **no longer** withdraws their access — use **Block** in the tab "User management" for that. Where the pre-approval list is to be the only access control, the switch stays off.

If the person has **lost** access to their provider account and therefore no longer gets in, you release the binding:

```bash
docker exec aboard-server aboard-server nutzer bindung-loesen \
  --email person@example.org --anbieter standard
```

- `--anbieter` is the short name (`standard` for the provider from `ABOARD_OIDC_ISSUER`, otherwise e.g. `google`).
- Boards, rights and account remain untouched; the binding lapses, and all sessions of the person end. The command writes an audit entry (`personen/<ID>`, action `konto-bindung-geloest-cli`). Unlike in the account settings, the command does **not** check whether this is the last sign-in method.
- If the person is on the pre-approval list, their next provider login binds anew (with the account that then confirms the address). Without a pre-approval entry and without a password they then no longer get in: in that case use **Reset** in the admin area (new invitation).

### Moving an account to a different address

The command `nutzer umziehen` changes the address under which aBoard keeps a person. You need it
when the person is kept under a different address than the provider reports at the first login. An example: Google delivers only the main address of an account, no alias; whoever
was kept so far under a different address (for instance via Authelia) and has never signed in through the built-in
sign-in needs the move. For an **already bound** provider account you do not need
it if only the reported address changes (see above). It keeps boards, rights and
keys:

```bash
docker exec aboard-server aboard-server nutzer umziehen \
  --von alt@example.org --nach neu@example.org --dry-run
```

- `--dry-run` shows only the numbers and writes nothing. Run it first, then the same
  command without `--dry-run`.
- The command rewrites the address in all tables that carry it as a key: account,
  board shares, release jobs of the sluice and AI conversations. The provider binding hangs
  since schema 46 on the person, not on the address, and moves along by itself. Everything runs in
  one transaction; on an error it aborts without a change.
- Sessions of the old address are deleted; the person signs in again.
- It aborts if the old address has no account, if the new one is already taken, or
  if both addresses have a share on the same board. In the last case you first remove
  the share of the new address (`acl set` or board shares in the interface).
- **Order:** first move, then add the new address to `ABOARD_OIDC_ALLOWED`
  (strike the old one) and restart. If the person has already signed in under the new address,
  there is an account of its own there, mostly empty; then the move aborts. Delete the
  empty account in the admin area and move afterwards. Nothing is merged.
- Way back: the same command with the addresses swapped.
- It applies in every operating mode, including forward auth.
- The log of activities, comment authors and addresses in the content of the boards remain
  unchanged.
- If the old address is in `ABOARD_BOOTSTRAP_OWNER`, change this variable to the new one as well.
  Otherwise the old address stays admitted, and the server enters it again at every start as
  owner of the service board.
- The database is at `ABOARD_DB`; with `--db <path>` you point to a different file.

### Updating to this version

This version brings schema 46 (migration 0046). It rebuilds the table of provider bindings and
creates tables for accounts and invitations. They fill only when someone invites.

- **Back up the database beforehand.** A way back to the previous version is only possible via this backup:
  the previous version no longer finds the old binding table.
- **You do not have to change anything in the environment.** The former names (`ABOARD_ANMELDUNG`,
  `ABOARD_OIDC_ISSUER`, `ABOARD_OIDC_ZUGELASSEN`, …) remain valid, and the callback address of the
  former provider stays `/anmeldung/rueckruf`.
- Existing bindings are taken over (as provider `standard`). A binding for which there is no
  person in the board (sign-in completed but never opened a board) lapses; the person
  binds again at the next login. For that the address must be on the pre-approval list as before.
  Before the update you can count whether this occurs for you (tool `sqlite3` on a **copy**
  of the database): `SELECT count(*) FROM oidc_bindungen b WHERE NOT EXISTS (SELECT 1 FROM users u
  WHERE lower(u.email) = lower(b.email));` — `0` means: no binding lapses for lack of a person. If
  a person exists twice in two spellings (`SELECT lower(email), count(*) FROM users GROUP BY 1
  HAVING count(*) > 1;`), the binding afterwards hangs on the older row.
- No accounts are created for existing people. Their access still depends solely on the pre-approval list.
- A sign-in that was **begun before the update** and returned only afterwards ends with
  "Sign-in expired". The person simply signs in again. Existing sessions remain
  valid.

### Problems with sign-in

| Observation | Cause | Remedy |
|---|---|---|
| Startup aborts, log begins with "Eingebaute Anmeldung (ABOARD_AUTH_MODE=builtin / ABOARD_ANMELDUNG=oidc) nicht startbar" | mandatory variable missing or invalid | fix the listed points; the message names all of them |
| Startup aborts: "… gesetzt, aber die eingebaute Anmeldung ist aus" (names the variables) | `ABOARD_OIDC_*`, `ABOARD_AUTH_*` or `ABOARD_INVITE_*` set, switch forgotten — the server would otherwise run entirely without sign-in | set `ABOARD_AUTH_MODE=builtin`, or remove the variables |
| Startup aborts: "ABOARD_AUTH_MODE=… ungültig" or "ABOARD_ANMELDUNG=… ungültig" | a switch has a value other than `builtin`, `oidc` or empty | correct the value |
| Startup aborts: "Anbieter … halb eingerichtet: … fehlt" | one of a provider's variables is missing | set the named variable, or remove all variables of this provider |
| Startup aborts: "ABOARD_OIDC_… ist unbekannt — Tippfehler, oder fehlt das Kürzel in ABOARD_OIDC_PROVIDERS?" | typo in the name, or the variables of a provider without its short name in `ABOARD_OIDC_PROVIDERS` | correct the name or add the short name to the list |
| Startup aborts: "ABOARD_AUTH_METHODS: … ist kein eingerichteter Anmeldeweg" | typo in the method list, or the provider is not (completely) set up | check the short name; the message names the set-up methods |
| Sign-in page suddenly shows a password form instead of forwarding directly to the provider | since own accounts are possible, `password` is a method, and an empty `ABOARD_AUTH_METHODS` means "all methods" | set `ABOARD_AUTH_METHODS=<short-name>` (see above) |
| Password form: "Zu viele Fehlversuche von diesem Anschluss" (`429`) | five wrong entries within 15 minutes from this connection; behind a proxy without `ABOARD_AGENT_PROXY` all people count together (startup then warns) | wait (the page names the minutes); set `ABOARD_AGENT_PROXY` to the proxy |
| Page "No access" after sign-in | address is not on the pre-approval list, provider does not report it as confirmed, the account is blocked, or the address already belongs to a different provider account | read the log; enter the address and restart, or `nutzer umziehen` |
| `503` at `/anmeldung/start` for a provider | this provider is not reachable | check network and `ABOARD_AUSGAENGE_ERLAUBT`; the server retries by itself; the other providers keep running |
| `404` "Unbekannter Anmeldedienst" at the callback or on click | the short name in the callback address at the provider is misspelled, the provider is not set up, or `ABOARD_AUTH_METHODS` excludes it | compare the callback address at the provider with the line in the server log; check `ABOARD_AUTH_METHODS` |
| "Anmeldung abgelaufen" | the sign-in process is older than 10 minutes, the cookie is missing, it began before an update, or a second sign-in was begun in another tab in the meantime (only the last process counts) | simply sign in again, in one tab |
| Provider reports "redirect_uri_mismatch" | callback address at the provider differs from `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<short-name>` (for the provider `standard` without a short name) | enter exactly this address at the provider |
| `401` again and again despite signing in | session expired, address struck in the meantime, or the browser does not accept the session cookie (it requires https) | sign in again; check the admission; open the board via `https://` |

---

## Who may use aBoard at all?

> Applies to forward auth. With the built-in sign-in, the gate made of pre-approval list and
> accounts decides, see
> [Who gets in?](#who-gets-in-pre-approval-list-and-accounts).

**In forward-auth operation the authentication service is the only access barrier.** Every person whom Authelia signs in
successfully and for whom an email address comes along is a full user in aBoard. There
is no additional invitation in aBoard.

At the **first** board access of a new email address the server automatically creates:

- an account for this address,
- an empty **personal board** with the title "Board",
- an owner entry of this person for the personal board.

After that the person sees their personal board and all boards that someone has shared with them.
If they later delete their personal board, it is not created again.

For operation this follows:

- **Limit the circle of users in the authentication service.** Whoever wants to let only certain people in
  regulates that there — aBoard does not evaluate `Remote-Groups`. Example for Authelia that admits
  only members of the group `aboard-nutzer`:

  ```yaml
  access_control:
    default_policy: deny
    rules:
      - domain: board.example.org
        policy: two_factor
        subject:
          - "group:aboard-nutzer"
  ```

  Open self-registration in the authentication service means open access to aBoard.
- **No `bypass` rule for the board address.** A rule that waves requests through without sign-in
  delivers no identity; aBoard then answers with `401`. The only exception is the calendar path named
  above, which you handle separately in the proxy.
- **Keep email addresses stable.** The email address is the key for account, shares
  and roles. If a person's address changes in the authentication service, they are a
  **new** person for aBoard: they get a new, empty personal board, and shares to the old
  address no longer apply.
- **One-time visitors leave traces.** Whoever signs in only once leaves an account
  and an empty board. Administrators can clean up in the server administration (see
  [`administration.md`](administration.md)).
- Addresses that begin with `@` are not accepted by the server as an identity (`403`) — this form is
  reserved for domain shares (see [below](#sharing-with-a-whole-email-domain)).

---

## Roles: owner, editor, viewer

Rights apply **per board**. A person can be owner on one board and viewer on another.
The order is viewer < editor < owner.

| Role | Read board and cards | Create, change, move cards; upload attachments | See and manage shares | Delete board |
|---|---|---|---|---|
| **Owner** | ✓ | ✓ | ✓ | ✓ |
| **Editor** | ✓ | ✓ | — | — |
| **Viewer** (`viewer`) | ✓ | — | — | — |

The server checks the role on **every** request to a board, also for attachments and for the
live update stream. If a person tries something their role does not allow, it answers
with `403`. If they have no role on a board at all, also `403` — the board then does not
appear in their board list either.

Whoever creates a new board becomes its owner. A board always has at least one owner: the server
refuses to demote or remove the last owner (`409`).

Separate from this is the property **administrator** for the server administration (settings,
user list, agent tokens, backups). It is not a board role: an administrator is thereby
in no share list and does not see foreign boards in their own board list. **Indirectly
they do reach every board, however:** they see the list of all boards of the server, can create for every board an
agent token with write or read permission and thereby read and change its content, and they can
download and restore the backups of every board. Administrator is therefore a role
for trustworthy people; assign it only to people who may see all data on the server.
See [`administration.md`](administration.md).

---

## Sharing boards

### In the interface

Owners share a board in the **board settings** in the section **Sharing**:

1. Choose a person from the list.
2. Choose a role (owner, editor or viewer).
3. Click **Add**.

Shares take effect **immediately** — "Cancel" in the settings dialog does not undo them. With
**Remove** next to an entry you take a share back. Editors and viewers
see only their own role in this section.

In the selection list there are people who have **signed in at least once** (status
"active"), as well as addresses that are already shared on some board but have not yet
signed in (status "pending"). A completely new address cannot be typed into the
interface: ask the person to sign in once — after that they appear
in the list.

### Through the programming interface

Through the REST interface an owner can also set a share for an address that has
never signed in. It takes effect as soon as the person signs in for the first time. The paths
`GET`, `POST` and `DELETE` on `/boards/{id}/acl` are described in [`api.md`](api.md).

### Sharing with a whole email domain

Instead of entering every person individually, an owner can share a board with **all addresses of a domain**
— for example all with `@example.org`. The prerequisite is that the operator explicitly allows this domain:

```yaml
    environment:
      ABOARD_ACL_DOMAINS: "example.org,example.net"
```

- Comma-separated list; case and a leading `@` do not matter.
- Only the **exact** domain counts: `example.org` does **not** allow `team.example.org`.
- If the list is empty or not set, no domain shares are possible.

If a domain is allowed, an entry "All with
@example.org" appears in the selection list of the share. For domain shares these rules apply:

- A domain can be **at most editor**, never owner.
- A **personal entry always takes precedence**, even if it is weaker. If a person
  is entered personally as viewer and their domain as editor, they stay viewer.
  If the owner removes the personal entry, the domain share applies again.
- The permission is checked on **every** request. If you strike a domain from
  `ABOARD_ACL_DOMAINS`, all domain shares for it lose their effect after the restart; in
  the share list they appear as "inactive". If you add the domain again, they take effect
  again.
- The board for the mail intake (service board) cannot be shared by domain.

---

## The first owner and the first administrator

### Bootstrap owner (`ABOARD_BOOTSTRAP_OWNER`)

If the server starts with an **empty** database, it creates a first board with the ID
`default`. In multi-user operation this board would have no owner without further action — and
would thus be visible to nobody.

With `ABOARD_BOOTSTRAP_OWNER` you enter an email address as owner of this board when it is
created:

```yaml
    environment:
      ABOARD_BOOTSTRAP_OWNER: "person@example.org"
```

The log then shows:

```
aboard-server: leere DB erkannt — kanonisches Default-Board angelegt (id=default)
aboard-server: Bootstrap-Owner eingetragen: "person@example.org" → owner auf Board 'default'
```

Important:

- For the `default` board the variable works **only at the very first start with an empty
  database**. If any board exists already, the server leaves the `default` board untouched —
  even after a restart, even if you have changed the shares in the meantime. You take over existing boards
  as described in [Switching](#switching-from-single-user-to-multi-user-operation).
- **Different for the service board** for the mail intake (see [`administration.md`](administration.md)):
  If it is switched on (default), the server enters the address as its owner at **every** start, as long as the variable is set. Whoever later demotes or
  removes this person there finds them again as owner after the next restart. **So remove the variable
  from the configuration after setup.**
- The address is normalised in the same way as the one from the authentication service (lowercase, without
  spaces). It must match the address that Authelia later delivers in `Remote-Email`.
- A value that is not a personal address (without `@`, or a domain entry such as `@firma.de`)
  is entered neither on the `default` board nor on the service board; the server starts anyway
  and reports the error in the log.
- At their first sign-in the person **additionally** gets their own personal board.
  They therefore see two boards: `default` and their personal one.

Without `ABOARD_BOOTSTRAP_OWNER` multi-user operation is still usable — every person then works
in their personal board, and the board `default` stays unused.

### The first administrator

As long as there is no administrator yet, this applies:

- **If `ABOARD_BOOTSTRAP_OWNER` is set, only this person becomes administrator** — at their first
  sign-in, no matter who comes before them. Other people get a normal account in this time;
  the log then says that the admin slot is waiting for the set address.
- If the person already has an account (the variable was set or corrected only later), the server
  makes them administrator **at the next start**.
- **If the variable is set but invalid** (without `@`, or a domain entry such as `@firma.de`),
  **nobody** automatically becomes administrator; the log reports it at server start and at every
  first sign-in of a new person. Correct the value and restart.
- **Built-in sign-in without an admin:** Whoever redeems the link of an invitation while there is no
  admin yet is immediately active and admin; no admin has to confirm them. On an empty
  server you therefore invite with `aboard-server nutzer einladen --email …` (see
  [Who gets in?](#who-gets-in-pre-approval-list-and-accounts)). If
  `ABOARD_BOOTSTRAP_OWNER` is set, only this person becomes active and administrator without confirmation;
  every other invitation waits for confirmation by the admin, even as long as there is none.
  If the value is invalid, nobody becomes active without confirmation: correct the value and restart.
- **Without the variable** the administrator is whoever's account the server creates first. An account arises at the first board access of a new email address (see
  [Who may use aBoard at all?](#who-may-use-aboard-at-all)). Sign in directly
  after setup as the person who is to manage the server, before you open access for
  others.

If there is already an administrator, the variable changes nothing about that. You appoint further administrators
in the server administration ([`administration.md`](administration.md)).

---

## Switching from single-user to multi-user operation

Boards that arose in single-user operation have **no owner entries** — in
single-user operation there are no people. If you only switch multi-user operation on, nobody
sees these boards. `ABOARD_BOOTSTRAP_OWNER` does **not** assign them, because the database is not
empty. The server reports them in multi-user operation at startup
(`HINWEIS: N Board(s) ohne Owner`), but does not assign them by itself — a board without an owner can
also be hidden on purpose.

**Easiest with the command `acl`** in the server container (works in both operating modes, also
afterwards):

```bash
aboard-server acl orphans                               # list boards without an owner
aboard-server acl adopt --email person@example.org      # owner of all boards without an owner
aboard-server acl set --board <id> --email person@example.org --role editor   # individually
```

`adopt` does not touch boards that already have an owner, and skips the service board for the
mail intake (it gets its owner through `ABOARD_BOOTSTRAP_OWNER` or specifically with `set`).
`set` does not demote the only owner of a board — first set a second owner. A
domain entry (`@firma.de`) is not a valid address. The command does not create a database: if the
path (`--db` or `ABOARD_DB`) is wrong, it aborts.

**Alternatively through the HTTP API**, as long as the server still runs in single-user operation. In
this operating mode the server accepts shares without sign-in:

```bash
# 1. Find out the board IDs (in single-user operation /boards returns all boards)
curl -s http://127.0.0.1:8080/boards

# 2. Enter the future owner for each board
curl -s -X POST http://127.0.0.1:8080/boards/default/acl \
  -H 'Content-Type: application/json' \
  -d '{"email":"person@example.org","role":"owner"}'
```

After that:

3. Generate a secret and store it in aBoard **and** in the proxy.
4. Switch the listen address to a Unix socket or loopback (or deliberately set
   `ABOARD_FORWARD_AUTH_INSECURE_BIND=1`).
5. Set `ABOARD_TRUST_FORWARD_AUTH=1` and restart.

The data itself stays unchanged on switching; the tables for accounts and shares
exist in every database, even if they stay empty in single-user operation.

---

## Testing locally without an authentication service

### Easiest: single-user operation

To simply try out the interface, single-user operation on your own
machine is enough (see [above](#single-user-operation)).

### Simulating multi-user behavior

To test roles and shares without Authelia, the server can inject the identity headers itself.
It then plays the proxy: it removes all `Remote-*` headers sent by the client, sets
the configured values and sends the secret along itself.

```bash
docker run --rm \
  --name aboard-test \
  -p 127.0.0.1:8080:8080 \
  -v aboard-test-db:/data/db \
  -e ABOARD_TRUST_FORWARD_AUTH=1 \
  -e ABOARD_FORWARD_AUTH_SECRET=<secret> \
  -e ABOARD_FORWARD_AUTH_INSECURE_BIND=1 \
  -e ABOARD_DEV_FAKE_USER=alice \
  -e ABOARD_DEV_FAKE_EMAIL=alice@example.org \
  -e ABOARD_DEV_FAKE_NAME="Alice Beispiel" \
  -e ABOARD_BOOTSTRAP_OWNER=alice@example.org \
  registry.example.org/aboard:<version>
```

Then open `http://127.0.0.1:8080/` — every request counts as a request from
`alice@example.org`.

- `ABOARD_DEV_FAKE_USER` switches the simulation on; it works **only** together with
  `ABOARD_TRUST_FORWARD_AUTH`. At startup a clear warning appears in the log.
- Also set `ABOARD_DEV_FAKE_EMAIL` — without an email address the server refuses every
  board access with `403`. `ABOARD_DEV_FAKE_GROUPS` and `ABOARD_DEV_FAKE_NAME` are optional.
- The secret is mandatory here too. `ABOARD_FORWARD_AUTH_INSECURE_BIND=1` is necessary because the
  server in the container listens on `:8080`; the port is nevertheless reachable only
  from your own machine through `-p 127.0.0.1:…`.
- To simulate a second person, restart the container with different
  `ABOARD_DEV_FAKE_*` values (same database).

> 🛑 **The simulation must never be active on a reachable server.** With
> `ABOARD_DEV_FAKE_USER` set, **every** request counts as the configured person, no matter who makes it.

### With `curl` against a server without a proxy

If a test server in multi-user operation listens on `127.0.0.1:8080`, you can also reproduce the proxy
by hand:

```bash
curl -s http://127.0.0.1:8080/boards \
  -H "X-Aboard-Forward-Auth-Secret: $ABOARD_FORWARD_AUTH_SECRET" \
  -H "Remote-Email: alice@example.org"
```

Without the secret header the server answers with `401`.

---

## Troubleshooting

### The server does not start

| Message in the log (shortened) | Cause | Remedy |
|---|---|---|
| `unsicherer Forward-Auth-Bind: ABOARD_TRUST_FORWARD_AUTH ist aktiv, aber der Server bindet auf ":8080" …` | multi-user operation with a listen address on all interfaces | set `ABOARD_UNIX_SOCKET` or `ABOARD_ADDR=127.0.0.1:8080` — or, if the network is sealed off in some other way, `ABOARD_FORWARD_AUTH_INSECURE_BIND=1` |
| `Forward-Auth ohne Secret: ABOARD_TRUST_FORWARD_AUTH ist aktiv, aber ABOARD_FORWARD_AUTH_SECRET ist nicht gesetzt …` | secret missing or consists only of spaces | generate and set a secret (see [above](#the-shared-secret)) |
| `ABOARD_UNIX_SOCKET_MODE="…" ungültig — erlaubt sind 0600, 0660 und 0666` | typo in the socket mode | use one of the three values |
| `Eingebaute Anmeldung (ABOARD_AUTH_MODE=builtin / ABOARD_ANMELDUNG=oidc) nicht startbar: …` | defects of the built-in sign-in (half-set-up provider, typo in the name or short name, wrong `ABOARD_AUTH_METHODS`, missing key …) | fix the listed points; table under [Problems with sign-in](#problems-with-sign-in) |

### Every request ends with `401`

Answer: `unauthorized: forward-auth identity required`. The server received either no matching
secret or no identity. Check in this order:

1. **Secret:** Does the proxy send `X-Aboard-Forward-Auth-Secret`, and is the value
   **identical** to `ABOARD_FORWARD_AUTH_SECRET` in the aBoard container? Is the variable in the
   environment of the **Caddy** container, and does the Caddyfile use `{$…}` instead of `{env.…}`? After
   a change restart both containers.
2. **Identity:** Does the proxy take over the headers from the answer of the authentication service
   (`copy_headers Remote-User Remote-Email …` in `forward_auth`)? Does the authentication service deliver an identity at all for
   this address (no `bypass` rule)?
3. **Read the log:** For rejected requests the server writes at most one
   line per minute with the reason — `Secret fehlt oder ist falsch` or `keine Identität`:

   ```
   aboard-server: forward-auth: Anfragen werden mit 401 abgewiesen (Secret fehlt oder ist falsch) — Proxy-Konfiguration prüfen …
   ```

   With `ABOARD_FORWARD_AUTH_VERBOSE=1` a line **per** request appears instead — for
   rejected requests with path and reason, for accepted ones with the transmitted identity.
   Switch it on only for troubleshooting; every open browser tab continuously generates requests.

The health check (`/healthz`) stays green meanwhile, because it needs no sign-in. A
green health check therefore does not prove that sign-in works.

### `403` when accessing boards

| Answer | Cause | Remedy |
|---|---|---|
| `forbidden: no identity email` | `Remote-User` arrives, but no `Remote-Email` | add `Remote-Email` to `copy_headers`; check whether the authentication service knows an email address for this person |
| `forbidden: invalid identity email` | The transmitted address begins with `@` | check the configuration of the authentication service |
| `forbidden: no access to board` | The person has no role on this board | owner shares the board; check whether the address in the authentication service corresponds exactly to the shared one (after an address change it is a new person) |
| `forbidden: insufficient role` | The role is not enough (e.g. viewer tries to write, editor tries to delete the board) | owner raises the role |
| `forbidden: nur der Owner darf …` | Only owners may see or change shares | ask the owner |
| `forbidden: cross-origin request rejected` | A writing request came from a different website (also one under the same main domain). With older browsers that send no `Sec-Fetch-Site` header, also if the proxy rewrites the `Host` header | open the board directly under its own address; do not rewrite the `Host` header in the proxy (Caddy keeps it by default) |
| `forbidden: admin endpoints require authentication` | Server administration in single-user operation | available only in multi-user operation |

### Further cases

- **`409` when changing a share:** The change would remove or demote the last owner of the board.
  First make another person an owner.
- **`400 Domain ist für Freigaben nicht erlaubt (ABOARD_ACL_DOMAINS)`:** The domain is not in
  `ABOARD_ACL_DOMAINS` (or is a subdomain of an allowed domain).
- **A person sees only an empty board after the first sign-in:** That is their personal
  board. Shared boards appear as soon as an owner shares them — or, if one has
  already been shared, the address does not match exactly.
- **Nobody sees the old boards after switching to multi-user operation:** The boards have
  no owner entries. For the procedure see
  [Switching](#switching-from-single-user-to-multi-user-operation).
- **Proxy reports `502 Bad Gateway` with a Unix socket:** Caddy may not open the socket or
  does not see it. Check whether both containers mount the same volume under the same path
  and whether Caddy has the necessary rights (see
  [Permissions on the socket](#docker-composeyml)).
- **Calendar subscription ends up on the sign-in page:** The path `/ical/calendar.ics` must be routed
  past the authentication service in the proxy (see [Caddyfile](#caddyfile)).
