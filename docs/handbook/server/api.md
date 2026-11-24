# REST API

**English** · [Deutsch](../../handbuch/server/api.md)

This page lists all paths that the aBoard server answers. It is aimed at operators:
anyone who configures a proxy in front of it, sets up monitoring or wants to check permissions will find here
which path does what and who may call it.

Login and roles are described in [Login and roles](authentication.md); all environment variables
are listed in [Configuration](configuration.md).

## Basics

- **No `/api` prefix, no `/login`.** The paths start directly with `/boards`, `/me`, `/admin`
  etc. `curl https://board.example.org/api` therefore answers with `404`; the right call is
  `curl https://board.example.org/boards`.
- **One process, one port.** The same server delivers the web interface (at `/` and all paths
  that are not an API route) and the API.
- **People log in in one of two ways:** through an upstream proxy via
  forward auth (the server reads the identity from the headers the proxy sets) or through the
  built-in login ([Login and roles](authentication.md)). Agents
  log in with a bearer token, and only under `/agent/` or on a separate
  listener (see [Agent paths](#agent-paths)).
- **Without login** (single-user operation) there is no identity. The
  board roles of this page then do not apply: the board paths are open to anyone who can reach the port.
  Paths that require a person or an admin (`/admin/*`, `/users/me/*`, `/clients`,
  `/projects`) answer `403` there. Details: [Login and roles](authentication.md).
- **With login** (forward auth or built-in login) there is no open path for people any more. Without an
  identity the server answers every path with `401`; the only exceptions are `GET` and `HEAD` on `/healthz`
  and `/ical/calendar.ics` and, with the built-in login, the login paths themselves (`/anmeldung…`,
  `/einladung/<token>`). **With the built-in login** the identity comes solely from the session cookie;
  the server discards `Remote-*` headers and the proxy secret before any handler sees them. Without a
  valid session (also an expired or ended one, or when the address is no longer admitted) it answers
  `401` (`unauthorized: session required`); only a page request (`GET`/`HEAD` with `Accept: text/html`)
  is redirected with `302` to `/anmeldung` instead. Roles, error codes and CSRF protection are the same
  as with forward auth from there on.
- **Bodies are JSON**, and so are responses (exceptions: file downloads, event streams, iCal feeds,
  the web interface).

### How permissions are checked

For all paths under `/boards/{id}/…`, a common basic rule applies that takes effect before the actual
handler:

| Path | `GET`/`HEAD` | `POST`/`PUT`/`PATCH` | `DELETE` |
|---|---|---|---|
| `/boards/{id}` | viewer | editor | owner |
| `/boards/{id}/…` (everything below) | viewer | editor | editor |

Anyone with no entry on the board gets `403` ("no access to board"); anyone with too low a role
also gets `403` ("insufficient role"). Some handlers check more strictly on top of that, for example the
share list (owner only). The tables below always state the role that is **actually needed**.

Role specifications in the tables:

| Specification | Meaning |
|---|---|
| öffentlich (public) | no login; possibly a token in the URL |
| angemeldet (logged in) | any logged-in person |
| viewer / editor / owner | minimum role of the person on the board concerned |
| Admin | server admin (flag in the user table) |
| eigene (own) | only data of the logged-in person themselves |
| Agent | only with an agent token through the agent entry |

**Agents** reach only the paths that are explicitly released for them (column "Agent" or
section [Agent paths](#agent-paths)). Everything else answers an agent with `403`, including
every path added in the future.

## Error codes

Errors come as JSON with exactly one field:

```json
{"error": "forbidden: insufficient role"}
```

The messages are mostly in English; the tool paths for agents answer with German
sentences that a language model can pass on.

| Code | Meaning |
|---|---|
| `400` | Invalid request: broken JSON, missing required field, unknown key in an op, ambiguous `/agent` path |
| `401` | no identity (forward auth in strict mode; with the built-in login no valid session), agent token missing, unknown, expired or revoked — or an agent token (`abt_…`) on a path for people |
| `403` | logged in, but not authorized (no role or too low a role, not an admin, path not released for agents, cross-origin request from the browser) |
| `404` | board, card or file does not exist; also functions that are switched off (such as the outbound gateway) |
| `409` | conflict: `PUT /boards/{id}` with a stale `If-Match`, name already taken, last owner would be removed |
| `413` | body or result too large (e.g. more than 200 ops, board over 10 MiB) |
| `422` | an op would work differently than intended; the response carries a `protokoll` with an `index` per rejected op |
| `428` | `PUT /boards/{id}` without `If-Match` |
| `429` | rate limit or lockout after failed attempts; sometimes with `Retry-After` |
| `503` | subsystem not available (database, write lock not free in time, missing server key for encrypted functions) |

## CSRF protection for browser requests

The entry for people is protected against cross-site request forgery, with forward auth and with the
built-in login alike (the check sits in front of the login; it also applies to `POST /anmeldung/ende`). A **writing**
request (anything except `GET`, `HEAD`, `OPTIONS`) is rejected with `403` ("cross-origin request rejected")
if

- the browser marks it with `Sec-Fetch-Site: cross-site` or `same-site`, or
- with older browsers, the host in the `Origin` header does not match the host of the request.

`same-site` also counts as foreign: another application under the same domain (for example
`wiki.example.org` next to `board.example.org`) is not a trustworthy source.

Requests **without** these headers (curl, scripts, agents) are not browser requests and pass
through. Under `/agent/` and on the separate agent listener there is no such protection; there only
the bearer token counts.

## Boards

| Path | Method | Role | Agent | Meaning |
|---|---|---|---|---|
| `/boards` | GET | angemeldet | – | List of the boards on which the person has a role; per entry `id`, `title`, `updated_at`, with forward auth `role` and, if someone on the board is waiting for an answer, `awaiting_user` (mapping person → entry like `board.awaitingUser`) |
| `/boards` | POST | angemeldet | – | Create a board; the creating person becomes owner |
| `/boards/{id}` | GET, HEAD | viewer | ✓ (own board) | Whole board state; `ETag`, with `If-None-Match` → `304` |
| `/boards/{id}` | PUT | editor | – | Replace the whole state; `If-Match` mandatory (`428` without, `409` on a foreign change) |
| `/boards/{id}` | DELETE | owner | – | Delete the board |
| `/boards/{id}/ops` | POST | editor | ✓ (editor, restricted: vorschlag) | Changes as an op batch, folded on the server; the normal write path |
| `/boards/{id}/events` | GET | viewer | ✓ | Event stream (server-sent events) with live changes |
| `/boards/{id}/activities` | GET | viewer | – | Log; `?card=<id>` for one card, `?limit=<n>` (default 100, at most 500); share entries only for owner, entries about proposals of the internal agent (`vorschlag-*`) only from editor |
| `/boards/{id}/ich` | GET, HEAD | – | ✓ | Information about the own agent token (name, actor, board, role, person); people get `404` |
| `/boards/{id}/vorschlaege` | GET | editor | – | Open proposals of the internal agent (board pair, outside side only, otherwise `404`); from v1.53.0 |
| `/boards/{id}/vorschlaege/{vid}/bestaetigen` | POST | editor | – | Execute a proposal, body `{"hash"}` (the one displayed); logged-in session only. `409` on a different hash, revoked access or a card changed since the proposal, `410` expired |
| `/boards/{id}/vorschlaege/{vid}` | DELETE | editor | – | Reject and delete a proposal |
| `/boards/{boardId}/vorschaubild` | GET | viewer | – | Reduced background image (JPEG) for the board overview; `204` without an image |
| `/boards/{boardId}/cards/{cardId}/transfer` | POST | editor (source and target board) | – | Move a card including its attachments to another board |
| `/boards/{boardId}/cards/{cardId}/email` | POST | editor | – | Send a card as an HTML mail (10 per minute and person) |
| `/boards/{id}/notifications/events` | POST | editor | – | Trigger a notification event from the browser |
| `/boards/{id}/schatten` | GET | viewer | – | Board pair, outside side only: shadows of internal cards and appointments of the board for display; on an external copy the confidential ones — and all cards, as long as the script fetches the outbound leg in format 2 (see [Board pair](#board-pair)) |

`POST /boards/{id}/ops` accepts a batch of changes (ops), at most 200 per call.
A faulty request answers with `400`, an op that would work differently than intended with `422`.

On an **external copy** of the board pair (see [Board pair](#board-pair)), the server rejects changes to mirrors:
ops answer `422` (log kind `spiegel-geschuetzt`), `PUT /boards/{id}` and transferring a card to
another board `403`, restoring a backup by "overwrite" onto the copy `409`. With "Allow editing outside"
switched on, a finer rule applies on the ops path: what is allowed goes through (and is recorded in the journal), everything
else stays `422`. `PUT /boards/{id}` and transferring a card always stay `403` for mirrors even then. If the coupling or the switch cannot be read on an ops call: `503`, nothing written.

`PUT /boards/{id}` leaves the switch "confidential" of a card or an appointment that is on in the
stored state as it is, even if the new state omits it or carries `false` (board pair).
Switching it off is only possible through an op by a human. If the server has left something
in place, the response names it in the header `X-Aboard-Vertraulich-Bewahrt: <count>; ids=<id>,…` (at most
20 identifiers, the count counts all); the body remains the board state.

## Shares and people of a board

| Path | Method | Role | Agent | Meaning |
|---|---|---|---|---|
| `/boards/{id}/acl` | GET | owner | – | Share list (email and role per entry) |
| `/boards/{id}/acl` | POST | owner | – | Set an entry, body `{"email": "person@example.org", "role": "editor"}` |
| `/boards/{id}/acl?email=<address>` | DELETE | owner | – | Remove an entry; the last owner cannot be removed (`409`) |
| `/boards/{id}/mitglieder` | GET | viewer | – | Display names of the people with a role only: `{"namen": [...], "gekappt": false}`; no email addresses |
| `/known-users` | GET | angemeldet | – | Known people and share domains for the share picker |

## Attachments

| Path | Method | Role | Agent | Meaning |
|---|---|---|---|---|
| `/boards/{boardId}/attachments/{cardId}` | POST | editor | ✓ (editor) | Upload a file to a card; the field `attachmentId` (chosen by the client) must be chosen anew, one that is already taken gives `409` (after v1.53.0). On an external copy of the board pair `422` for mirror cards, waiting cards and new cards in marked lanes (with and without return path); if the coupling or state is not readable `503`, retry |
| `/boards/{boardId}/attachments/{cardId}/{attId}` | GET, HEAD | viewer | ✓ | Download a file |
| `/boards/{boardId}/attachments/{cardId}/{attId}` | DELETE | editor | – | Delete one file (`403` if it is attached to someone else's comment). Agents delete via `anhang_loeschen` |
| `/boards/{boardId}/attachments/{cardId}` | DELETE | editor | – | Delete all files of a card. If the card still exists, files on other people's comments remain (KF-3); then `200` with `{"geschuetztGeblieben": n}` instead of `204`, and the browser fetches the rest after saving. A server up to v1.64.0 ignores `nurVerwaist` and deletes everything; on the way back, reload open tabs. With `?nurVerwaist=1` only if the card is missing in the stored state, otherwise `409`. The check is made twice, before and after removing the folder; if the card comes back in between, likewise `409`, and the files remain. If there is nothing to delete, `204` comes; likewise `409` if the state is not readable, `404` without a board (the browser makes up for failed purges this way, after v1.64.0) |
| `/boards/{boardId}/board-attachments` | POST | editor | ✓ (editor) | Attach a file to the board (e.g. background image); a taken `attachmentId` → `409` as above |
| `/boards/{boardId}/board-attachments/{attId}` | GET, HEAD | viewer | ✓ | Download a board file |
| `/boards/{boardId}/board-attachments/{attId}` | DELETE | editor | – | Delete a board file. Agents delete via `anhang_loeschen` |

## Board backups

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/boards/{id}/backup/files` | GET | owner or Admin | Existing backups of the board |
| `/boards/{id}/backup/download/{filename}` | GET | owner or Admin | Download a backup |
| `/boards/{id}/backup/now` | POST | owner or Admin | Back up immediately |
| `/boards/{id}/backup/files/{filename}` | DELETE | owner or Admin | Delete a backup |
| `/backup/restore` | POST | angemeldet; owner or Admin on the source board of the archive, when overwriting additionally on the target board | Restore an archive (as a new board or over an existing one) |
| `/admin/backup/all-now` | POST | Admin | Back up all boards immediately |
| `/admin/backup/all-status` | GET | Admin | State of the last full run |

Agents reach none of these paths. Operation and retention are described in
[Server administration](administration.md).

## Personal data (`/me`, `/users/me`)

All paths in this section refer to the logged-in person themselves; there is no person ID in the
path. Agents do not reach them.

| Path | Method | Meaning |
|---|---|---|
| `/me` | GET | Own identity (name, email, admin flag); empty fields without login. Depending on the setup additionally `owui_url`, `schleuse: "an"`, `wissen` (display name of the knowledge source, only if it is on and the person may see it), `boardpaar` (`"innen"` or `"aussen"`; missing for `aus`; instead of it `boardpaar_fehler: true` appears if the side is not readable) and, only outside with "Allow editing outside" switched on (from v1.55.0), `boardpaar_rueckweg: "an"` (missing otherwise; if the setting is not readable likewise `boardpaar_fehler: true`, and `boardpaar` is then missing). From v1.56.0 the value only applies together with the switch "bearbeitbar" of the respective copy from `GET /boards/{id}/schatten` |
| `/me/cards?dueDateRange=<from>~<to>` | GET | Cards with a date across all boards with read access (calendar "all boards") |
| `/me/termine?dueDateRange=<from>~<to>` | GET | Appointments without a card across all boards with read access |
| `/users/me/agent-tokens` | GET | Own agent tokens and boards for which self-service is on |
| `/users/me/agent-tokens` | POST | Create an agent token (plain text only in this response); `"board"` or, for several boards, `"boards": [...]` or `"alle_boards": true`; 20 changes per hour; `"anbieter"`/`"beleg"` as with `/admin/agent-tokens` (mandatory outside, otherwise optional), the list carries both |
| `/users/me/agent-tokens/{id}` | DELETE | Revoke an own token |
| `/users/me/llm-keys` | GET | Stored AI providers (without keys) |
| `/users/me/llm-keys/{provider}` | POST, DELETE | Set or delete an own provider key |
| `/users/me/llm-keys/{provider}/test` | POST | Check a key against the provider |
| `/users/me/notifications/event-types` | GET | Available event types |
| `/users/me/notifications/subscriptions` | GET | Own subscriptions |
| `/users/me/notifications/subscriptions/{eventType}/{channel}` | PUT | Set a subscription |
| `/users/me/notifications/config/{channel}` | GET, PUT, DELETE | Set up the delivery channel (e.g. address) |
| `/users/me/notifications/test/{channel}` | POST | Send a test message |

## Calendar feeds

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/me/ical/subscriptions` | GET, POST | eigene | List or create own iCal subscriptions (needs the server key, otherwise `503`) |
| `/me/ical/subscriptions/{id}` | DELETE | eigene | Revoke a subscription |
| `/ical/calendar.ics?token=<token>` | GET | öffentlich (token in the URL) | iCal feed for calendar apps; at most 120 calls per hour and subscription, calls with an unknown token at most 120 per hour and address, otherwise `429` |

Calendar apps do not speak forward auth. If the server is behind a login proxy, `/ical/*`
must be exempted from the login there; the token is the login. The address for calls with an
unknown token is determined by the same rule as for the agent throttle: `X-Real-IP` only from the peer
in `ABOARD_AGENT_PROXY`, otherwise the TCP address, never `X-Forwarded-For` (after v1.53.0). If a
proxy is in front and `ABOARD_AGENT_PROXY` is empty, all calls with an unknown token share the address of the
proxy and thus one bucket; valid subscriptions are not affected by this, they count per subscription. **Limit of this choice:** anyone who knows the feed address of a subscription can use up its quota (120 per hour), and a subscription that many
devices share also shares the quota. The remedy is a separate subscription per device; a leaked address is
revoked anyway.

**Board pair:** On an instance with the side `innen`, `POST /me/ical/subscriptions` rejects new subscriptions with
`409` ("You subscribe to the calendar on the external board"); listing, feed and deleting of existing subscriptions
remain. On the side `aussen`, the feed additionally contains the shadows with a date of the coupled boards
that the person may read, and every entry carries `X-ABOARD-ART` (`card`, `termin`, `schatten-card`,
`schatten-termin`). If the side is not readable there, the feed answers with `503`.

The data-saving feed "for outside" (`/ical/aussen/…`, token kind `kalender-ausgang`) no longer exists.
A subscription "for outside" that is still present delivers nothing and can only be deleted.

## Customers and projects

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/clients` | GET | angemeldet | List customers |
| `/clients` | POST | Admin | Create a customer |
| `/clients/{id}` | PATCH, DELETE | Admin | Change or delete a customer |
| `/clients/{id}/projects` | GET | angemeldet | Projects of a customer |
| `/projects` | GET | angemeldet | List projects |
| `/projects` | POST | Admin | Create a project |
| `/projects/{id}` | PATCH, DELETE | Admin | Change or delete a project |
| `/projects/{id}/cards` | GET | angemeldet | Cards that are assigned to a project |

## AI functions

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/boards/{boardId}/cards/{cardId}/llm-send` | POST | editor | Send a card to a language model, response in one piece |
| `/boards/{boardId}/cards/{cardId}/llm-send-stream` | POST | editor | The same as an event stream |
| `/boards/{boardId}/cards/{cardId}/llm-pending` | GET | viewer | Running or queued request of a card |
| `/boards/{boardId}/cards/{cardId}/llm-discard` | POST | editor | Discard the own AI response |
| `/boards/{boardId}/cards/{cardId}/conversations` | GET, POST | viewer or editor | Read or create conversation histories of a card |
| `/boards/{boardId}/cards/{cardId}/conversations/{convId}/turns` | GET | viewer | Contributions of a history |
| `/boards/{boardId}/cards/{cardId}/conversations/{convId}/archive` | POST | editor | Archive a history |
| `/boards/{boardId}/cards/{cardId}/conversations/{convId}` | DELETE | editor | Delete a history |
| `/llm/pricing` | GET | angemeldet | Price table of the models |

Setting up the providers and company mode (a shared AI access of the operator for all people): [Server administration](administration.md).

## Mail intake

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/mail-spam-report` | POST | editor on the service board | Put a sender on the block list, body `{"senderEmail": "…"}`; with `"undo": true` remove again (30 per minute and person) |

## Knowledge source

These paths exist only if an admin has switched on the knowledge source (tab "Wissen", see
[Server administration](administration.md)); otherwise they answer `404`. They only read,
responses are JSON. Agent access does not reach them.

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/wissen/index` | GET | Account (with `wissen.sichtbar=admins` only Admin, with `gruppe:<name>` only the group according to `Remote-Groups` or Admin) | Name, state, pages (`pfad`, `titel`, `groesse`), possibly `gekappt` |
| `/wissen/seite?pfad=…` | GET | as above | One page as Markdown text (`inhalt`), possibly a link `bearbeiten` |
| `/wissen/suche?q=…` | GET | as above | Hits with `pfad`, `titel`, `schnipsel`, `zeile`; `q` 2 to 200 characters |

The visibility `gruppe:<name>` follows `Remote-Groups` and applies only with forward auth; with the built-in
login (no groups) only admins then see the source.

Errors: `401` without login (with forward auth or built-in login; with neither `403`), `403` without an account on
this server or without visibility, `404` off or unknown page, `413` page over 1 MiB, `429`
more than 120 calls or 30 searches per minute and person, `503` folder not readable.

## Outbound gateway

These paths exist only if the outbound gateway is switched on (see
[Configuration](configuration.md)); otherwise they answer `404`. What the gateway does is described in
[Server administration](administration.md).

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/users/me/schleuse-schluessel` | GET | eigene | Own stored keys (without plain text) |
| `/users/me/schleuse-schluessel/{id}` | PUT, DELETE | eigene | Store or delete a key |
| `/users/me/schleuse-auftraege/{aid}` | DELETE | eigene | Delete an own job |
| `/boards/{id}/schleuse-sperre` | GET | viewer | Is the board blocked for the gateway? |
| `/boards/{id}/schleuse-sperre` | PUT | owner | Set or lift the block |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege` | GET, POST | viewer or editor | List the jobs of a card or create an excerpt |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}` | GET, PUT, DELETE | viewer or editor | Read, change, delete a job |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/freigabe` | POST | editor | Release a job |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/zweitfreigabe` | POST | editor | Second release (four-eyes principle) |
| `/boards/{id}/cards/{cardId}/schleuse-auftraege/{aid}/bilder/{bid}` | GET | viewer | Image preview of a job |
| `/boards/{id}/cards/{cardId}/schleuse-belege/{aid}` | GET | viewer | Receipt of a sent job |
| `/admin/schleuse/freigaben` | GET, POST | Admin | List of the approved targets |
| `/admin/schleuse/freigaben/{id}` | PUT, DELETE | Admin | Change or remove a target |

The role specifications of this table follow the basic rule for `/boards/…`; the handlers additionally check
the gateway's own conditions (block, keys, releases).

## Administration (`/admin/*`)

All paths require a server admin; writing calls are additionally rate-limited. Without
login (single-user operation without forward auth) there is no admin, and they answer with `403` —
for agent tokens the command line remains there (`aboard-server agent-token`, see [Container](container.md)).
With login (forward auth or built-in login) they answer `401` without an identity, and `403` for a
logged-in person who is not an admin (`forbidden: not an admin`).

| Path | Method | Meaning |
|---|---|---|
| `/admin/status` | GET | Server status (version, subsystems) |
| `/admin/config` | GET | All settings of the admin interface |
| `/admin/config/{section}` | GET | Settings of one area |
| `/admin/config/{key}` | PUT, DELETE | Set a setting or reset it to the default |
| `/admin/config/{section}/test` | POST | Test an area (e.g. mail connection) |
| `/admin/config/reload` | POST | Reload settings |
| `/admin/audit` | GET | Change log of the admin actions |
| `/admin/users` | GET | List people |
| `/admin/users/{id}/admin` | PATCH | Set or withdraw the admin flag, body `{"is_admin": true}` |
| `/admin/users/{id}` | DELETE | Delete a person (also revokes their agent tokens) |
| `/admin/einladungen` | POST | Built-in login only: invite, body `{"emails": ["…"]}` (1–50); response `{"ergebnisse": [...]}`, per address `email`, `konto_id`, `link` (visible only here), `ablauf`, `erneut`, `mail` (`gesendet`, `nicht_eingerichtet`, `fehlgeschlagen`) or `fehler` (`adresse`, `hat_zugang`, `schon_eingeloest`, `belegt` = database currently busy, again in a moment, `intern`); mails run in parallel with a shared deadline of 20 s; counts once in the throttle; without the built-in login `404` |
| `/admin/einladungen/{id}` | DELETE | Built-in login only: withdraw an invitation (account in the state "invited" including links); `204`, `404` for an unknown ID, `409` if already redeemed |
| `/admin/konten` | GET | Built-in login only: response `{"konten": [...]}` with state, chosen method, reported address, expiry of the link as well as `hat_zugang` (person has a user row), `neuer_link` (button "New link" possible: invited with a reset link, or a user row) and `widerrufbar` (missing if there is a reset link and a user row at the same time) |
| `/users/me/konto` and `/users/me/konto/…` | GET/POST | Built-in login only, only with a session (no agent): account settings of the logged-in person — state, `passwort`, `2fa/start`+`2fa/abschluss`, `codes`, `sitzungen/beenden`, `wege/verknuepfen`, `wege/loesen`, `frisch/start` (fresh provider login), `passwort/anlegen/start`+`/abschluss` (only with `ABOARD_ACCOUNT_SELF_PASSWORD`). Errors as `{"fehler": "…"}`, among others `409` `frische_anmeldung_noetig`, `passwort_nutzen`, `kein_passwort`, `konto_zustand`, `letzter_weg`; wrong confirmation `403` |
| `/admin/boards` | GET | All boards of the server |
| `/admin/agent-tokens` | GET | All agent tokens (never the plain text) |
| `/admin/agent-tokens` | POST | Create an agent token, body `{"name", "board", "role"}`, optionally `"user"` (email) and `"gueltig_tage"`; with `"user"` instead of `"board"` also `"boards": [...]` or `"alle_boards": true` (token for several boards); `"anbieter"` (at most 80 characters) and `"beleg"` (at most 300 characters, reference for the training exclusion; both without control, zero-width and bidi characters, without semicolon and with at least one letter, digit or punctuation/symbol character, otherwise `400`) are mandatory on the outside side of a board pair (otherwise `400`), otherwise optional; the list carries both fields |
| `/admin/agent-tokens/{idOderName}` | DELETE | Revoke a token (preferably by id) |
| `/admin/users/{id}/agent-tokens/revoke` | POST | Revoke all agent tokens of a person |
| `/admin/agent-selbstbedienung` | GET | Boards with self-service switched on |
| `/admin/boards/{id}/agent-selbstbedienung` | PUT | Switch self-service for a board, body `{"an": true}` |
| `/admin/backup/all-now`, `/admin/backup/all-status` | POST, GET | see [Board backups](#board-backups) |
| `/admin/schleuse/freigaben[/{id}]` | GET, POST, PUT, DELETE | see [Outbound gateway](#outbound-gateway) |
| `/admin/config/boardpaar.seite` | PUT, DELETE | Side in the board pair: `aus`, `innen` or `aussen` (otherwise `400`); `DELETE` means `aus`. From v1.53.0 `409` as long as boards are coupled on the previous side (the message names them), `503` if the current side is not readable |
| `/admin/boardpaar` | GET | Side, partner lists inside and outside (from v1.53.0 per board `letzte_abfrage` or `letzter_eingang` and `letzter_fehlgrund`; outside per board `kopie`), access (without secret), `ziele`, `auftraege`, `team`, `rueckweg` (`aus`/`an`), `link_basis_vorgabe` and (from v1.55.0) `kopien_entkoppelt` — copies decoupled outside as `[{"board_id", "titel"}]` |
| `/admin/boardpaar/innen` | POST | Couple an internal board, body `{"board_id"}` |
| `/admin/boardpaar/innen/{board}` | DELETE | Decouple an internal board |
| `/admin/boardpaar/aussen` | POST | Couple an external board or change its address, body `{"board_id", "innen_link_basis"}`; `409` for a copy decoupled outside (mirror marks or origin "copy") — it only comes back through a new job |
| `/admin/boardpaar/aussen/{board}` | DELETE | Decouple an external board; its shadows go with it. After that, the write protection treats an external copy like an ordinary board (ops, agents); but the server remembers its origin (emergency path, list "Copies decoupled outside", resumption, and coupling by hand gives `409`); from v1.55.0 the browser also releases the former mirrors (up to v1.54.0 they remained locked there). The mirrors remain **with full content**, changes from outside no longer go inside — if the copy is to go, `DELETE /admin/boardpaar/kopien/{board}` is the way (details: [Server administration](administration.md)) |
| `/admin/boardpaar/kopien/{board}` | DELETE | From v1.55.0: emergency path "delete copy outside" — deletes an external copy including coupling, shadows, journal, attachments, without requiring the board role owner. Only on the side `aussen` (otherwise `409`) and only for copies: coupled with `kopie` or decoupled outside (created as a copy by the server — migration 0042 —, no coupling); any other board `409`, even with mirror marks set by yourself, unknown `404`, database busy `503`. Response `{"ok": true, "board_id", "entkoppelt"}`. Nothing changes inside; a new job creates the copy afresh ([Server administration](administration.md)) |
| `/admin/boardpaar/kopien/{board}/bearbeitbar` | PUT | From v1.56.0: switch "bearbeitbar" (editable) of a copy, body `{"bearbeitbar": true\|false}` (mandatory; if it is missing or has another type: `400`). The copy is only editable together with `boardpaar.rueckweg = an`; otherwise the write protection applies to it. Only on the side `aussen` and only for coupled copies (`kopie`): stage-1 coupling or the other side `409`, not coupled `404`. Response `{"ok": true, "board_id", "bearbeitbar"}`. If the script sends the field on the next outbound leg, its value wins. The partner list (`GET /admin/boardpaar`, `aussen[]`) carries the value per entry as `bearbeitbar` |
| `/admin/boardpaar/zugaenge` | POST | Create an access, body `{"typ": "abs", "abe" or "abr", "name"}`, optionally `"darf_anlegen": true` (`abe` only: may create copies outside); `201` with the plain text, only in this response |
| `/admin/boardpaar/zugaenge/{id}` | DELETE | Revoke an access |
| `/admin/boardpaar/auftraege` | POST | "Create external copy" (side `innen` only, otherwise `409`), body `{"board_id", "ziel"}`, optionally `"innen_link_basis"` (otherwise `ABOARD_PUBLIC_BASE_URL` + `/#board=<id>`, if both are missing `400`); `201` with the job |
| `/admin/boardpaar/auftraege/{id}` | DELETE | Withdraw an open job; `409` if it is already done |
| `/admin/boardpaar/team` | PUT | From v1.55.0: save the board-pair team, body `{"team", "bisher", "bestaetigt"}` — `bisher` (mandatory, otherwise `400`) is the value last read. If saving would withdraw access to copies and `"bestaetigt": true` is missing: `409 {"bestaetigung_noetig": true, "weg": [...], "zeilen": n}`, nothing written. If `bisher` is no longer correct: `409 {"veraltet": true}`. Otherwise `200 {"ok", "team", "weg", "zeilen"}` |
| `/admin/config/boardpaar.team` | PUT, DELETE | Board-pair team (outside only): email addresses or `@domain`, separated by comma; the server rejects invalid entries on saving. From v1.55.0 only for changes **without** withdrawal — if someone with access to copies would drop out (also with `DELETE`), `409`; use `PUT /admin/boardpaar/team` for that |
| `/admin/config/boardpaar.rueckweg` | PUT, DELETE | "Allow editing outside" (outside only; from v1.55.0): `aus` or `an` (otherwise `400`); `DELETE` means `aus`. On a change, the server adjusts the role of the team on all copies in the same transaction (`an`: readers become editors, otherwise the reverse) |

Errors in the board pair: `400` (missing board ID, service board, invalid address, type or name, `darf_anlegen`
with a type other than `abe`, invalid `ziel` name, missing `innen_link_basis` without `ABOARD_PUBLIC_BASE_URL`), `404` (board unknown, not coupled, no active access with this id, no
such job), `409` (an active access of this type with this name already exists; for board and target there is already an
open job; job already done; the board is an external copy and cannot be coupled by hand).

## Board pair

Paths of the internal/external board pair (shadows, copy, mirror and return path). Which of them exist is
decided by the side of the instance (`boardpaar.seite`), which the server reads on every request. On every other
side, with a missing or invalid value, and for boards outside the respective partner list, they answer with
`404`, indistinguishable from "does not exist". Setup: [Server administration](administration.md).

| Path | Method | Side | Access | Meaning |
|---|---|---|---|---|
| `/agent/schatten/{boardId}` | GET | innen | `Bearer abs_…` | Shadow outbound: the whole shadow list of an internal board for the push script, `{"version", "stand", "schatten": [...]}` |
| `/agent/schatten/{boardId}` | PUT | außen | `Bearer abe_…` | Shadow inbound: replaces the shadows of an external board with the set sent, body exactly `{"version", "schatten": [...]}`; response `{"neu", "geaendert", "entfernt", "anzahl"}` |
| `/agent/boardpaar/erneuern` | POST | innen / außen | `Bearer abs_…` or `abr_…` inside, `abe_…` outside; `abb_…` on each side (from v1.56.0) | From v1.53.0: exchanges the token for a new one, without a body; response `{"token", "typ", "name", "alt_gilt_bis", "wiederholt"}`. The old one is valid until `alt_gilt_bis` (10 min), a repetition in this time returns the same new token (`"wiederholt": true`); afterwards, revoked or unknown: `401`; `503` + `Retry-After` as long as the system clock is not set. Own throttle: 30 failed attempts per hour and address. |
| `/boards/{id}/schatten` | GET | außen | Person, viewer | Shadows for display in the browser: `{"letzter_abgleich", "schatten": [...]}`, per entry `schatten_id`, `art`, `nummer`, `titel`, `titel_platzhalter`, `spalte`, `prioritaet`, date and time, `bearbeiter`, `von_agent` and `link` (address of the internal board, for cards with `&card=<nr>`); at the top level `kopie` and from v1.56.0 `bearbeitbar` (switch of the copy — the browser only makes mirrors editable if `/me` additionally reports `boardpaar_rueckweg: "an"`); `Cache-Control: no-store` |

On the own agent listener, the paths are without the prefix `/agent`: `/schatten/{boardId}`, `/boardpaar/…`.

**External copy, mirror and return path (stage 2).** The copy itself (jobs, `boards`, outbound leg in the previous format) exists
from v1.54.0; mirrors (`?format=3`, `faehigkeiten`), journal (`aenderungen`), `anwenden` and `boardpaar.rueckweg` only from
v1.55.0. These paths apply only to boards with a job "external copy" inside
or only to external copies outside. Paths with a board ID answer `404` if the board is on the other side,
is not coupled or is not a copy outside; the paths without a board ID (`auftraege`, `ziele`, `boards`, `erneuern`)
answer `404` on the wrong side. The side is checked **before** the token.

| Path | Method | Side | Access | Meaning |
|---|---|---|---|---|
| `/agent/boardpaar/auftraege` | GET, HEAD | innen | `abs_…` | Open jobs "create copy": `{"auftraege": [{"id", "board_id", "ziel", "titel", "innen_link_basis"}]}`, only from boards that are still in the partner list inside; `Cache-Control: no-store` |
| `/agent/boardpaar/auftraege/{id}/erledigt` | POST | innen | `abr_…` | Job done, without a body; response `{"id", "status"}`; repeatable, unknown id `404` |
| `/agent/boardpaar/ziele` | PUT | innen | `abr_…` | Report the names of the targets, body `{"ziele": ["name", …]}` (at most 20, each 1–64 characters of letters, digits, `. _ -`); replaces the previous set; response `{"ziele": [...]}`; `422` on a deviation |
| `/agent/boardpaar/spiegel/{innenBoardId}` | GET, HEAD | innen | `abs_…` | Fetch the outbound leg: `{"format", "version", "stand", "struktur", "schatten", "spiegel", "faehigkeiten"}`. `?format=3` additionally delivers non-confidential cards and appointments as mirrors and the title of the board; without the parameter (or with `2`, as the script from stage 2a sends it) all cards come as shadows; any other `format` `400`. `faehigkeiten: ["anwenden"]` means: this server accepts the return path. `404` without coupling or without a job, `500` with more than 5000 entries ("archive done cards") or an unreadable board state |
| `/agent/boardpaar/boards` | POST | außen | `abe_…` with "may create copies" | Create a copy, body exactly `{"id", "titel", "innen_link_basis"}` and optionally `"bearbeitbar"` (`true`/`false`, from v1.56.0; if it is missing or `null`, a new copy gets `false` and an existing one keeps its value; if sent it also applies on `200` and on resumption; another type is `422`) (`id`: UUID in lowercase); `201` newly created or resumed (`"wiederaufgenommen": true`), `200` it already existed (repeatable), `403` without the right, `409` the id belongs to a board that is not a copy (also a copy that was decoupled outside before migration 0042), `422` on a wrong body, `503` + `Retry-After: 5` with a busy database. Response `{"id", "titel", "innen_link_basis", "wiederholt", "wiederaufgenommen", "mitglieder", "bearbeitbar"}` (only the number of members; `bearbeitbar` = value after the call) |
| `/agent/boardpaar/spiegel/{aussenBoardId}` | PUT | außen | `abe_…` | Apply the outbound leg; response as with the shadow inbound (`neu`, `geaendert`, `entfernt`, `anzahl`, plus `struktur_geaendert` for "board state written") and `spiegel: {neu, geaendert, entfernt, anzahl}`, from v1.56.0 plus `bearbeitbar` (value after the run). Optional envelope key `bearbeitbar` (`true`/`false`): sets the switch of the copy in the same transaction; if it is missing or `null`, the value remains; another type is `422` (nothing written). With the key only an `abe_` with "may create copies", otherwise `403`: board, switch and roles remain unchanged, the rest of the outbound leg is not applied either; only the inbound marker records the failure reason. A change goes to open browsers as `schatten:aktualisiert`. Errors like the shadow inbound: `409` differing major version, `413`, `422`, `429` + `Retry-After: 5` (a run is already going), `503` + `Retry-After: 5` |
| `/agent/boardpaar/aenderungen/{aussenBoardId}` | GET, HEAD | außen | `abe_…` | Fetch the journal of changes made outside (`?nach=<seq>&max=<n>`, at most 200): `{"eintraege", "weitere", "letzte_seq", "veraltet"}`. `veraltet: true` if an entry has been unconfirmed for 25 days. `HEAD` only reads and does not note the pickup. `409`: `nach` lies beyond the end of the journal (a backup was restored outside), `422` for invalid numbers, `503` + `Retry-After: 5` |
| `/agent/boardpaar/aenderungen/{aussenBoardId}/bestaetigt` | POST | außen | `abe_…` | Confirm entries that were picked up, body `{"bis": <seq>}`; response `{"bestaetigt": <number>}`; `422` on a wrong body |
| `/agent/boardpaar/anwenden/{innenBoardId}` | POST | innen | `abr_…` | Apply **one** return-path entry to the internal board (body, exactly these keys, unknown ones give `422`: `op_id` (1–126 bytes, the op ID of the script), `objekt` (`karte` or `termin`), `id` (inner ID), `art`, `stelle`, `vorher`, `nachher`, `ursprung` (only and always for `angelegt`, card or appointment: the outside ID), `person`, `zeit`; `stelle`, `art`, `person` and `zeit` come unchanged from the journal, in `vorher`/`nachher` the script translates identifiers into inner IDs (lane, column, anchor `nach`, `karteId`)). Response `200 {"ergebnis": "angewendet" \| "schon-angewendet" \| "konflikt" \| "verworfen", "grund"?}`. On a conflict, the server writes a comment by the author "Board-Paar" on the card (see [Server administration](administration.md)). `404` without coupling or job, `409` the same `op_id` with different content, `413` over 1 MiB, `422` faulty entry, `503` + `Retry-After: 5` if the board changes too fast in the meantime; rate limiter of the ops path: `429` `rate limit exceeded` (without `Retry-After`) or `lock time budget exceeded` (with `Retry-After: 10`) |

**Reading backups (`abb_`, from v1.56.0).**
A pickup script uses this access to fetch the nightly board backups. The two paths apply **on every side**,
also with `boardpaar.seite = aus`, and only for finished, encrypted archives (`.tar.gz.enc`); without encryption
the list is empty. Restoring is only possible through `POST /backup/restore` (logged in).

| Path | Method | Side | Access | Meaning |
|---|---|---|---|---|
| `/agent/sicherungen` | GET, HEAD | jede (any) | `abb_…` | `{"boards": [{"board_id", "kopie", "files": [{"filename", "size", "mtime", "encrypted"}]}]}` — all boards with at least one backup that can be picked up, without titles; `kopie`: created outside as a copy |
| `/agent/sicherungen/{boardId}/{filename}` | GET, HEAD | jede (any) | `abb_…` | The archive byte for byte (`application/octet-stream`, `Content-Length`); `404` for everything else (unencrypted, unfinished, foreign or unknown board). Write deadline 60 s + 1 s per 256 KiB, at most 30 min |

**The script paths** run through the agent entry, but not through the agent check chain: a
board-pair token goes directly to the handler, without an agent role and without a board share.

- Each access reaches only its paths from the tables above: `abs_` the reading ones inside (`GET` on `/schatten/<one segment>`,
  `/boardpaar/auftraege`, `/boardpaar/spiegel/<one segment>`), `abe_` the paths outside (`PUT` on
  `/schatten/<one segment>` and `/boardpaar/spiegel/<one segment>`, `POST /boardpaar/boards`, the journal
  `GET`/`HEAD …/aenderungen/<one segment>` and `POST …/aenderungen/<one segment>/bestaetigt`), `abr_` `PUT /boardpaar/ziele`, `POST /boardpaar/auftraege/<id>/erledigt` and
  `POST /boardpaar/anwenden/<board>`, `abb_` `GET`/`HEAD /sicherungen` and `/sicherungen/<board>/<file>`; all four additionally
  `POST /boardpaar/erneuern` (`abb_` on each side, also with `aus`). On any other path, with any
  other method and at the entry for people, the server answers with `403`.
- `401`: an unknown or revoked `abs_`/`abe_`/`abr_`/`abb_`. After 120 such failed attempts per hour and address (on the
  renewal path: 30) comes
  `429` without `Retry-After`. From v1.53.0 the address follows the rule of the agent throttle: `X-Real-IP` only from a
  peer in `ABOARD_AGENT_PROXY`, otherwise the TCP address, never `X-Forwarded-For` (up to v1.51.0: first entry of
  `X-Forwarded-For`). A request entirely without
  a token is answered under `/agent/` by the agent check chain (`401`, with its failed-attempt throttle).
- A proxy identity (`Remote-Email`) is not evaluated by any of the script paths.
- **Outbound:** `500` without a partial list if the board is not readable or more than 5000 shadows would result ("too many
  shadows … archive done cards").
- **Inbound:** side, token and coupling decide before the body is read. Then `422` on any deviation from the
  field set (the message names location and field, never the value; nothing written). The response names **all**
  faulty shadows: `{"error": "<first error> (und N weitere …)", "fehler": [{"index": 3, "grund": "titel: …"}, …]}`,
  `index` is the position in the list `schatten` (from 0). The list has at most 50 entries; beyond that
  `"weitere": N` counts the rest. `error` remains for older scripts. Errors of the envelope (no JSON, `version` missing)
  have only `error`. The test vectors of the title handling are in `server/testdata/schatten/t16-vektoren.json`
  (generated by `erzeuge-t16.py` in the same folder). Title, column and
  every editor must be a **fixed point** of the same handling: the inbound never cleans, it rejects — also for two spaces
  in a row, a space at the edge, a tab or the 🤖 mark in the title (the mark goes through `vonAgent`).
  In the marker of the admin display ("Abgleich-Stand") and in the server log, the number of further errors appears ("… (und N
  weitere fehlerhafte Schatten)"), but not the reference "see fehler". The reasons never contain values (title,
  column, editor); for foreign object keys the key name appears, truncated to 40 characters. Further: `409` on a differing
  major version (only if both sides report SemVer), `413` over 32 MiB, `429` with `Retry-After: 5` as long as a run is going for
  the same board, `503` with `Retry-After: 5` if the database is busy, `400` if the upload
  takes longer than 60 seconds. On a change, the event `schatten:aktualisiert` goes to the
  event stream of the board (board, count, time; no content, no `ETag`).

**The display** (`/boards/{id}/schatten`) exists only at the entry for people: role according to the basic rule (viewer),
agent tokens and board-pair tokens get `403`. If the side, the coupling or the table is not readable,
it answers with `503`. It contains no identifiers of internal cards and no email addresses; the internal board ID appears only in the human link `link` (`innen_link_basis`).
On an **external copy** the response additionally carries `"kopie": true`, and every entry names its place with
`lane` and `spalte_id`. There a confidential shadow shows its real title (`titel_platzhalter: false`); the
placeholder "Interne Karte" (`titel_platzhalter: true`) remains for shadows of stage-1 boards and entries without a title.

## Health

| Path | Method | Role | Meaning |
|---|---|---|---|
| `/healthz` | GET, HEAD | öffentlich | State as JSON |

```bash
curl -s https://board.example.org/healthz
# {"ok":true,"db":"up","version":"…","commit":"…","build_date":"…"}
```

If the database is not reachable, the path answers with `503` and `"ok": false, "db": "down"`.
`/healthz` is reachable without an identity even with the login switched on and states version,
commit and build date. Anyone who does not want to show that to the outside blocks the path at the proxy for external
networks; the container's own health check only needs local access.

## Agent paths

Agents address the server through one of two entries:

- **`/agent/` on the same port** (default on, can be switched off with `ABOARD_AGENT_PFAD=aus`):
  `https://board.example.org/agent/boards/{id}/ops` corresponds to `POST /boards/{id}/ops`.
- **separate listener** via `ABOARD_AGENT_LISTEN` or `ABOARD_AGENT_UNIX_SOCKET`: there without a prefix.

Under both, only the bearer token counts (`Authorization: Bearer abt_…`), no forward auth and no
CSRF protection. The accesses of the board pair (`abs_…`, `abe_…`, `abr_…`) are not agent tokens; they reach
only their own path (see [Board pair](#board-pair)). Paths that a proxy could assign to the agent branch, but that on the server do not
begin exactly with `/agent/` (`/AGENT/x`, `//agent/x`, `/agent` without a slash), are answered by the
server with `400`. If the path entry is switched off, this applies to every `/agent` path.

### Released board paths

In each case only for the board to which the token is bound:

| Path | Method | viewer | editor | vorschlag |
|---|---|---|---|---|
| `/boards/{own}` | GET, HEAD | ✓ | ✓ | – |
| `/boards/{own}/events` | GET | ✓ | ✓ | – |
| `/boards/{own}/ich` | GET, HEAD | ✓ | ✓ | ✓ |
| `/boards/{own}/ops` | POST | – | ✓ | ✓ (new cards only) |
| `/boards/{own}/attachments/{cardId}/{attId}`, `/board-attachments/{attId}` | GET, HEAD | ✓ | ✓ | – |
| `/boards/{own}/attachments/{cardId}`, `/board-attachments` | POST | – | ✓ | – |
| `/boards/{own}/attachments/{cardId}/{attId}`, `/board-attachments/{attId}` | DELETE | – | – | – |

### Tool paths

The tools with an OpenAPI description (for Open WebUI and other tool clients) exist **only**
at the agent entry; at the entry for people they answer with `404`.

| Path | Method | Tool | Role |
|---|---|---|---|
| `/openapi.json` | GET | (description) | any token except vorschlag |
| `/mcp` | POST (GET, DELETE → 405) | MCP entry, the same tools | any token except vorschlag |
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
| `/werkzeuge/boards/{boardId}/warten` | POST | `auf_antwort_warten` | editor, personal tokens only |
| `/werkzeuge/boards/{boardId}/karten/{karte}/loeschen` | GET, POST | `karte_loeschen_pruefen`, `karte_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/swimlanes/{swimlane}/loeschen` | GET, POST | `swimlane_loeschen_pruefen`, `swimlane_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/termine/{termin}/loeschen` | GET, POST | `termin_loeschen_pruefen`, `termin_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/anhaenge/{anhang}/loeschen` | GET, POST | `anhang_loeschen_pruefen`, `anhang_loeschen` | editor |
| `/werkzeuge/boards/{boardId}/spalten/{spalte}/loeschen` | GET, POST | `spalte_loeschen_pruefen`, `spalte_loeschen` | editor, person is owner |
| `/werkzeuge/boards/{boardId}/aendern` | POST | `board_aendern` | editor, person is owner |

From `karten_suchen` onward, the tools are not released for a delegating token (Open WebUI);
`kommentar_bearbeiten`, `uebernehmen`, `freigeben` and `board_aendern` can never be.
Deletion only happens in two steps: `GET` returns a preview and a confirmation code, `POST {code}` deletes.

A **delegating token** (Open WebUI) reaches only these tool paths, none of the
board paths above, and likewise a **personal token for several boards** (the board from the path must be in
its list and the person must have an entry of their own there). A **proposal token** reaches
no tool path. For the token kinds see
the explanation above: a delegating token is the service token of Open WebUI that acts for the
logged-in person on each request; a personal token for several boards belongs to one person and names
several boards; a proposal token may only submit proposals.

### Blocked for agents

Whole-board `PUT`, the board list, log, shares, members, transfer, AI, mail, backups,
`/me`, `/users/me/*`, `/known-users`, `/clients`, `/projects` and `/admin/*`. A valid token on
a blocked path gives `403`, a missing or invalid one `401` — also for paths that do not exist at
all, so that an unknown party learns nothing about the routes.
