# Administering the server

This chapter is for people who run and administer an aBoard server: who is an
admin, what can be configured in the browser, how outgoing mail, incoming mail
and AI access are set up, how to back up and restore boards, and how to keep the
server away from the internet.

How the container is started is described in [container.md](container.md), all
environment variables in [configuration.md](configuration.md), sign-in and
roles in [authentication.md](authentication.md). The individual REST paths of
the admin API, with all fields and responses, are described in [api.md](api.md).

> **Prerequisite:** Almost everything in this chapter assumes operation **with
> sign-in** (forward auth or built-in sign-in, see
> [authentication.md](authentication.md)). The section
> [Accounts and invitations](#accounts-and-invitations-built-in-sign-in) applies
> only to built-in sign-in. Without
> sign-in the server knows no people; the admin functions then answer with
> `403`, and there is no access control.

---

## Two kinds of settings

The server distinguishes two kinds of settings:

| Kind | Examples | Where set | When effective |
|---|---|---|---|
| **Environment variables** | database path, listen address, master key `ABOARD_SECRET_KEY`, outbound switch, AI company mode (one shared AI access provided by the operator for everyone, see [Company mode](#company-mode-one-access-for-everyone)) | in the container (Compose file, `docker run -e`) | only at start |
| **Server configuration** in the database | mail server (SMTP), incoming mail (IMAP), auto-reply, service board, board backup | in the browser under "Server administration" or via the admin API | at runtime, without a restart (details per section) |

For SMTP, IMAP and the service board there are also environment variables
(`SMTP_*`, `IMAP_*`, `SERVICE_BOARD_*`). They are only **initial values**: When
the server starts, it copies a value into the database as long as there is none
there yet. After that, the value from the database applies. An environment
variable changed later therefore has **no** effect any more — change the value
in the server administration.

Secret values (passwords, keys) are stored encrypted by the server. It needs
the master key `ABOARD_SECRET_KEY` for this (see
[Master key](#master-key-aboard_secret_key)). If it is missing, the server
refuses to store secret values with `503`, and an `SMTP_PASS` or `IMAP_PASS`
from the environment is skipped with a warning in the log.

---

## Admins

### Who becomes an admin

- **Whoever the server creates an account for while there is no admin yet**
  automatically becomes admin. An account is created on the first board access
  of a new email address; someone who already has an account does not become
  admin retroactively. So sign in yourself first after the first start, before
  you open access to others.
- **Built-in sign-in on an empty server:** There is nobody yet who could invite
  through the interface. The first admin is created with a command in the
  container (`aboard-server nutzer einladen --email …`); whoever redeems this
  link becomes admin (with `ABOARD_BOOTSTRAP_OWNER` set, only this address).
  Details: [First admin on an empty
  server](#first-admin-on-an-empty-server).
- Further admins are appointed by an admin in the **"User management"** tab
  (toggle "Admin") or with `PATCH /admin/users/{id}/admin`.
- The **last admin** cannot remove their own rights, and nobody else can remove
  them either (response `409`). If all admin rights are lost anyway, only a
  direct intervention in the database helps.

> ⚠️ **Admin is a position of trust.** The admin right is not a board role, but
> it indirectly reaches every board: An admin sees the list of all boards
> (`GET /admin/boards`), can create an agent token for any board and thereby
> read and change its content, and can download and restore the backups of any
> board. Grant it only to people who may see all data on the server.

### The server administration in the browser

Admins see the entry **"Server administration"** in the sidebar. The dialog has
these tabs:

| Tab | Content |
|---|---|
| System status | version, uptime, number of admins, whether a valid master key is set, whether the system clock is set, AI company mode (display only), outbound sluice |
| Audit log | all changes to the server configuration and to admin rights |
| Mail server (SMTP) | sending, "Test connection" |
| Incoming mail (IMAP) | mailbox polling, "Test connection" |
| Mail auto-reply | automatic acknowledgement of receipt |
| Service board | title, spam behaviour of the board for incoming mail |
| Backup | board backups, encryption, restore |
| User management | people, admin rights, deleting; with built-in sign-in also **accounts and invitations** (see [below](#accounts-and-invitations-built-in-sign-in)) |
| Agent access | tokens for agents |
| Calendar | time zone of the times |
| Knowledge | read-only view of a mounted Markdown folder (see [below](#knowledge-source)) |
| Board pair | side of this instance (off, inside, outside), partner lists inside and outside, access tokens `abs_`/`abe_`/`abr_` for the push script, external copies, board-pair team and the switch "Allow editing outside" (see [below](#board-pair-internal-and-external)) |
| Clients & Projects | clients and projects that cards can be assigned to |
| Outbound sluice | only visible if the sluice is switched on |

### Limits of the admin API

- All paths under `/admin/` require an account with the admin right, otherwise
  `403`.
- Changing calls — including the connection tests — are limited to
  **30 per hour and admin** (then `429`). The count is kept in memory and starts
  over after a restart.
- Every change ends up in the audit log. For secret values, it records only
  **that** something was changed, never the value.

### Making changes take effect

The server reads stored values differently depending on the area:

- **Mail server (SMTP), incoming mail (IMAP), auto-reply and service board:**
  "Save" in one of these four tabs restarts the worker process of the incoming
  mail by itself (`POST /admin/config/reload?section=imap`, only this area) —
  saved means effective. The restart is needed because the incoming-mail worker
  reads SMTP values (auto-reply sending, own-sender filter), service board and
  auto-reply values at start; the normal mail sending of cards reads SMTP anew
  for every send anyway. The message under the button distinguishes three
  cases: "restarted", "switched off" (`imap.enabled` off — nothing runs,
  nothing to start) and an error with the reason (e.g. invalid IMAP settings;
  the incoming mail then does not run). The restart lets a mail that is
  currently being processed finish (at most 20 s wait); if the old incoming-mail
  worker hangs for longer, the dialog reports an error and does not start a
  second one — save again a little later. In the service board tab, the service
  board is first created anew (re-seed, existing cards stay), then the incoming
  mail is restarted.
  Without a browser, the same works with `POST /admin/config/reload?section=imap`;
  without `section`, the call reloads **all** areas (among other things, this
  aborts a running board backup run).
- **Notifications** read the configuration anew on every send — saved means
  effective.
- The switch **"Encrypt backups"** reloads only the backup area after it is
  toggled (`POST /admin/config/reload?section=backup_board`); other areas are
  left untouched.

---

## Accounts and invitations (built-in sign-in)

This section applies when the server signs people in itself
(`ABOARD_AUTH_MODE=builtin`, see [authentication.md](authentication.md)). With
forward auth there are no invitations: There, the upstream sign-in service
decides who gets in. Without built-in sign-in, the paths of this section answer
with `404`, and the accounts part of the tab is missing.

There is no self-registration. You decide who gets an account: You invite an
address, the person chooses how to sign in, and you then confirm their access.

### The path of an account

An account is in one of four states:

| State (tab) | Meaning | What you can do |
|---|---|---|
| **Invited** | The link has been created, the person has not redeemed it yet. | Fresh invitation: revoke, or invite again (new link: enter the same address once more). Reset account or person with a user row: **New link** (with confirmation prompt); "Revoke" only if there is not both a reset link and a user row. |
| **Waiting for confirmation** | The person has redeemed it (chose password with two-factor, or a provider). They do **not** get in yet, not even if their address is on the pre-list. | Confirm, reject, block, reset |
| **Active** | You have confirmed; the person has access and a personal board. | Block, reset |
| **Blocked** | The person no longer gets in, their sessions have ended. | Unblock, reset |

The confirmation exists so that a forwarded link is not enough: Whoever redeems
the link is not in yet as long as you have not confirmed them.

Only the **account** is a state in this list. People who get in solely via the
pre-list (`ABOARD_OIDC_ALLOWED`) have no account and do not appear there. You
withdraw their access by removing the address from the list (restart needed, see
[authentication.md](authentication.md)) or by deleting the person.

A person on the pre-list **can have an account**, though, for example since they
created a password for themselves. In that case: **The account state takes
precedence over the list as soon as the account is blocked, waiting for
confirmation or has been reset** (state "Invited" with a reset link, even if the
link has expired; the server never cleans up the most recent reset link of an
account that is still invited as long as the account stands like this). Only
after confirmation does the person get in again. Only a **fresh invitation** (no
reset) of a listed person stays covered by the list: They get in via a provider
even before redeeming.

### Inviting people

1. Open the server administration and the tab **"User management"**. Above the
   user list is **"Accounts and invitations"**.
2. In the field **"Email addresses to invite"**, enter one or more addresses
   (one per line or separated by commas, at most 50 at a time) and press
   **"Invite"**.
3. Under **"Invitation links"**, a link appears for each address. Each row shows
   whether an email was sent and has a **"Copy"** button.

The link is valid for **7 days** and can be redeemed **once**. An invitation
never carries the admin role; you appoint further admins as usual (see
[Who becomes an admin](#who-becomes-an-admin)).

**Passing on the link, even without mail sending.** If a mail server is set up
([Mail sending](#mail-sending-smtp)), the server additionally sends the link to
the address. This does **not** depend on the switch
`ABOARD_NOTIFICATIONS_DELIVERY`, because inviting is an explicit action by you.
Without a configured mail server, the row says "Mail sending not set up — pass
the link on yourself"; likewise if sending fails. Then pass the link on
yourself (messenger, in person), and only to the intended person.

> ⚠️ **You only see the link now.** The server does not store it, only a
> fingerprint. The link appears only on this page, nowhere in browser storage,
> and disappears with **"Hide links"** or when the dialog is closed. If you need
> it again later, enter the same address again ("invited again") or reset the
> account (only after redemption); a
> new link immediately invalidates all older ones.

Addresses that do not work stay in the field so that you can correct them.
Reasons: not a valid address (only addresses made of ASCII characters, without
display name, are allowed), "already has access" (the person already has an
account in aBoard), "Invitation was already redeemed" (then confirm or reset)
and "Database currently busy" (try again in a moment).

The server deletes expired and used links as well as used recovery codes 30 days
afterwards. Until then the account stays in the list as "Invited" with the note
"expired"; with **"Revoke invitation"** you remove it.

### First admin on an empty server

Through the interface, only an admin can invite. On a server without an admin,
this works only with the command in the container (`ABOARD_AUTH_MODE=builtin`
and `ABOARD_PUBLIC_BASE_URL` must be set, as for the server itself):

```bash
docker exec aboard-server aboard-server nutzer einladen --email anna@example.org
```

The command prints the link, not into the server log. If there is still no
admin when the link is redeemed, the invitation counts as confirmed: The person
is active immediately and becomes admin (if `ABOARD_BOOTSTRAP_OWNER` is set, only
if it is their address; any other invitation then waits for confirmation). So
give the link only to the intended person until it has been redeemed. From the
first admin on, an admin always confirms. You can also use the command later to
invite someone from the command line; the confirmation then applies as usual.

### Confirming or rejecting

Rows in the state "Waiting for confirmation" have the buttons **"Confirm"** and
**"Reject"**. Check the column **"Sign-in method"** and the text below it first:

- **Sign-in method** shows what the person chose: "Password + second factor",
  or the name of the provider (for example "google"). "Previous provider" stands
  for the provider `standard`, i.e. the single provider from the old variables
  `ABOARD_OIDC_ISSUER` etc.; this is just an old label. A dash means: no method
  chosen yet (state "Invited").
- **"Reported: Name <address>"** appears for the provider route. This is the
  name and address the provider reported at sign-in. They come **unchecked from
  the provider** and only say who signed in there, not whether this is the
  person you wanted to invite.
- If **"Attention: differs from the invited address"** is shown, someone signed
  in with a provider account whose address is not the invited one. This is not
  forbidden: Many people have a private provider account and were invited at
  their work address. But confirm only if you know the person and the account
  belongs to them. If in doubt, ask through another channel before you confirm.
  If you doubt a forwarded link, reject.

**Confirm** makes the account active and creates the person together with a
personal board. **Reject** (with confirmation prompt) removes the waiting
account together with the link and provider binding; the same address or the
same provider account can be invited again afterwards. If the person already has
access (for example after a reset), the server refuses; that is what blocking or
deleting is for.

> **Known limitation (two admins at the same time):** If one admin confirms an
> account while another rejects the same one, a person with a personal board but
> without an account can be left behind. The address then counts as "already has
> access" and can only be invited again after you have deleted the person
> ([Deleting people](#deleting-people-privacy)). With several admins, agree on
> who processes the waiting accounts.

### Blocking and unblocking

**"Block"** (with confirmation prompt; for active and waiting accounts)
immediately ends all sessions of the person, and the gate no longer lets them
in. A blocked person stays blocked, **even if their address is on the
pre-list**. Boards and data remain.

**"Unblock"** never leads directly to "Active". If the person has sign-in data
(password or provider), the account goes to "Waiting for confirmation",
otherwise to "Invited". Then you confirm explicitly; after unblocking, the focus
jumps to the "Confirm" button. The reason: The server does not know whether the
account was confirmed before the block.

### Resetting

**"Reset"** (with confirmation prompt) is what you use when someone has lost
their password or the phone with the authenticator app, or when you no longer
trust an account. There is deliberately no reset by email by the person
themselves; that would be a second way into the account that bypasses the second
factor.

Resetting discards the person's password, two-factor, recovery codes and all
provider bindings, ends their sessions, lets open links expire and creates a
**new link** that you see exactly once (like when inviting; by email if a mail
server is set up). The person redeems it and chooses the method anew; afterwards
**the account waits for confirmation again**.

- It also works for a **blocked** account. If you suspect something: block
  first, then reset. Resetting lifts the block; but access is given only after
  redemption and confirmation.
- You can neither block nor reset your **own** account: You would lock yourself
  out. There it says "(your own account)".
- For a **fresh invitation** (state "Invited", the person never had access)
  there is nothing to reset; the server refuses, and the interface shows no
  "Reset" button there. There you invite the same address again (this yields a
  new link) or revoke.
- For an account that is "Invited" and either was **reset** (it has a reset
  link; the link has expired or been lost, also after blocking and unblocking)
  or whose person has a **user row** (they had access, for example because the
  operator detached their last sign-in via the command line and the account was
  subsequently blocked and unblocked), the row shows the button **"New link"**
  (with confirmation prompt, because it resets the account again): It creates a
  new reset link; the old one is no longer valid.
- **"Revoke"** is missing only where the account has a reset link **and** the
  person has a user row: Deleting the account would leave the person with their
  user row but without an account, locked out and never invitable again; the
  server then answers with 409. In all other cases revoking is possible and the
  row shows it: for a fresh invitation, for a reset account without a user row
  (there is nothing that would be orphaned) and for a person with a user row
  without a reset link (for example signed in via the pre-list; there "New link"
  and "Revoke" stand side by side). A user row stays when revoking; the person
  may continue to get in via the list.
- **Unblocking without sign-in data** (the person has no sign-in method any
  more, for example because the operator detached the last binding): The account
  becomes "Invited" and counts like a reset one (new reset link in the
  background, not output). The pre-list applies to the person again only after
  redemption and confirmation; "Revoke" is disabled while a user row exists,
  "New link" outputs the link. If the person already had access, the
  confirmation prompt for "Revoke" names the consequence. The same applies to a
  blocked and unblocked **fresh invitation** that had not been redeemed yet: It
  also gets the reset link, and a person on the pre-list gets in again only
  after redemption and confirmation (deliberately the safe direction).
- **Blocking** lets open links of the account expire. After unblocking an
  invited account, the person therefore needs a new link ("New link" or invite
  the same address again).
- **Confirm** can fail with 409 if the person already has a binding with this
  provider (for example bound via the pre-list): First detach the old binding
  (`aboard-server nutzer bindung-loesen`) or reset the account. Rejecting does
  not work there because the person already has access.

### Deleting

You delete a person with access like any other in the tab (see
[Deleting people](#deleting-people-privacy)). This also removes their account,
links, recovery codes and provider bindings. Afterwards you can invite the
address anew.

### Error messages in the interface

- "That is not possible in this state (any more), or it is your own account"
  (`409`): Someone was faster, or the action does not fit the state. The list
  was reloaded; check the state and try again.
- "The person already has access" (`409` on rejecting): block or delete instead
  of rejecting.
- "Database currently busy" (`503`): try again in a moment.
- "This account no longer exists" (`404`): already deleted or revoked.

All changes end up in the audit log (key `konten/<id>`, never the address). The
paths of the admin API are in [api.md](api.md); the interface needs only these.

### Privacy: what the database stores about accounts

For accounts with their own password, the server stores in the database file:

- the password **only as an argon2id hash**, never in plain text;
- the secret of the authenticator app **encrypted** with `ABOARD_SECRET_KEY`;
- the ten recovery codes **only as a SHA-256 fingerprint**; each is usable once;
- invitation and reset links **only as a fingerprint**;
- the invited address, the sign-in method and, for providers, the address and
  name **reported** by the provider in plain text, plus the binding to the
  provider account (issuer and identifier).

Consequences for operation:

- **The database file and the server backup contain these hashes and the
  encrypted secrets.** Treat both like credentials: do not store them in places
  that others can read (see [Server backup](#server-backup-the-way-back-for-everything)).
  The board backup does not touch accounts.
- Changing `ABOARD_SECRET_KEY` makes the secrets unreadable (see
  [Master key](#master-key-aboard_secret_key)).
- **Deleting a person** also removes account, links, codes and bindings (see
  [Deleting people](#deleting-people-privacy)). **Rejecting** or **revoking**
  removes the same for the waiting or invited account.
- From old backups, the data disappears only after their retention period.

---

## Deleting people (privacy)

An admin can remove a person completely — in the tab **"User management"** via
"Delete" or with `DELETE /admin/users/{id}`. In the dialog, the person's email
address must be typed exactly before the confirmation button becomes active. You
cannot delete your own account.

The deletion runs in **one** transaction: Either everything happens or nothing.

| What | How |
|---|---|
| The account, with built-in sign-in additionally their sign-in account (password hash, two-factor, recovery codes, invitation links, provider bindings), all board shares, stored AI keys, AI histories, notification settings and log, calendar subscriptions, keys and open jobs of the outbound sluice, the entries of the address in the auto-reply log | **deleted** |
| Agent tokens of the person | **revoked**, open connections are closed |
| Logs: activities, audit log, "last changed by" in the configuration, token creators, already sent sluice jobs, among others | **pseudonymised** to `geloescht:` plus 12 hex characters — random, not derivable from the address, identical everywhere within one deletion |
| Names and addresses **in the board contents** (card owners, assignees, comment authors) and files attached to their comments | **unchanged** — replace by hand in the board as needed, or delete the file on the card |
| An entry of the address in the spam block list of the incoming mail | **unchanged** (protective function) |

**Boards of which the person is the only owner** block the deletion. The server
answers with `409` and the list of these boards; nothing changes. In the dialog,
a selection for the future owner then appears per board. Via the API, you pass
the transfer in the body:

```http
DELETE /admin/users/<id>
Content-Type: application/json

{"uebertragen": {"<board-id>": "person@example.org"}}
```

The target must be an existing account. If such a board is to disappear
entirely, you transfer it to yourself and then delete it like any of your own
boards. Boards with further owners and boards on which the person only
collaborated simply continue to exist without them.

> ⚠️ **The deletion does not block sign-in.** You remove the account at the
> identity provider yourself. If the person signs in again before that, they get
> a new, empty account — never their old one.

> ⚠️ **Backups still contain the deleted data** until it disappears after its
> period: board backups after `backup_board.keep_days`, server backups after
> your own retention. Whoever does not want to wait deletes the affected backups
> specifically.

---

## Mail sending (SMTP)

With a configured mail server, people with write permission (`editor` or higher)
can send cards as email, and the server delivers notifications by email.

### Setting up

In the tab **"Mail server (SMTP)"** or as initial values via the environment:

| Environment variable | Key in the configuration | Default | Meaning |
|---|---|---|---|
| `SMTP_HOST` | `smtp.host` | — | mail server, e.g. `smtp.example.org` (required) |
| `SMTP_PORT` | `smtp.port` | `587` | port |
| `SMTP_USER` | `smtp.user` | — | login name (required) |
| `SMTP_PASS` | `smtp.pass` | — | password (required, secret, needs `ABOARD_SECRET_KEY`) |
| `SMTP_FROM` | `smtp.from` | — | sender address, e.g. `board@example.org` (required) |
| `SMTP_FROM_NAME` | `smtp.from_name` | — | display name of the sender |
| `SMTP_STARTTLS` | `smtp.starttls` | on | `false` switches STARTTLS off |

"Test connection" opens a connection, encrypts it via STARTTLS and signs in —
**without** sending a mail.

Important to know:

- **No sending without sign-in.** Username and password are required; the server
  sends nothing through an open relay.
- **Encryption via STARTTLS.** The server connects unencrypted and switches to
  TLS (at least 1.2) if the mail server offers STARTTLS. A port with TLS from the
  start (usually 465) is not supported — use 587. Over an unencrypted connection,
  the server hands the password only to a mail server on the same machine
  (`localhost`).
- **SPF and DKIM** are set up at the mail provider. aBoard does not sign
  anything itself; without these records, mails often end up in spam.
- SMTP follows **no** HTTP proxy but opens a direct TCP connection.

### Sending cards by mail

`POST /boards/{board}/cards/{card}/email` with recipients, optionally cc,
subject and accompanying text. At most 10 recipients in total, at most 10 mails
per minute and person. The mail contains the card text once as plain text and
once as HTML; the server generates the HTML itself from the Markdown, and
embedded HTML from the card is not carried over. Without a complete SMTP
configuration, the endpoint answers with `503`.

---

## Incoming mail (IMAP)

The server can poll a mailbox regularly and turn incoming mails into cards:

- **Reply to a card:** If the subject (or else the text) contains an identifier
  of the form `[aBoard #123]`, the mail is attached to card 123 as a comment.
  Card numbers run consecutively per board; if the same number exists on several
  boards, the board with the alphabetically first board ID wins.
- **New request:** All other mails become new cards in the **service board**
  (column "Inbox"). The server creates the service board itself at start as long
  as it is not switched off.
- **Spam:** Senders on the block list, or senders who send more than
  `imap.rate_limit_per_hour` mails per hour, end up in the column "Spam" — or
  are discarded if discarding is set in the tab "Service board"
  (`service-board.spam_drop`).

### Setting up

In the tab **"Incoming mail (IMAP)"** or as initial values via the environment:

| Environment variable | Default | Meaning |
|---|---|---|
| `IMAP_ENABLED` | `false` | `true` switches polling on |
| `IMAP_HOST` | — | mailbox server, e.g. `imap.example.org` |
| `IMAP_PORT` | `993` | port |
| `IMAP_USER` | — | login name (required) |
| `IMAP_PASS` | — | password (required, secret, needs `ABOARD_SECRET_KEY`) |
| `IMAP_TLS` | `true` | `true` = TLS from the start (993), `false` = STARTTLS |
| `IMAP_FOLDER` | `INBOX` | folder that is polled |
| `IMAP_PROCESSED_FOLDER` | `Processed` | destination for processed mails |
| `IMAP_ERROR_FOLDER` | `Errors` | destination for mails that could not be processed |
| `IMAP_POLL_INTERVAL_SEC` | `600` | seconds between two polls (at least 60) |
| `IMAP_MAX_MAIL_SIZE_MB` | `25` | larger mails go to the error folder |
| `IMAP_MAX_ATTACHMENT_SIZE_MB` | `10` | larger attachments are discarded, a note stays in the text |
| `IMAP_RATE_LIMIT_PER_HOUR` | `20` | mails per hour and sender, above that spam; `0` switches the limit off |

The keys in the configuration are named accordingly: `imap.host`, `imap.port`
etc. (the polling interval: `imap.polling_interval_sec`).

"Test connection" signs in and opens the configured folder without retrieving
any mail. "Save" restarts polling (see
[Making changes take effect](#making-changes-take-effect)).

Recommendations:

- Use a **dedicated mailbox** just for the board, not a personal one.
- **Create the folders** for processed and failed mails **in the mailbox** — the
  server does not create them itself.
- aBoard does not check SPF/DKIM on incoming mail; the mail provider's filter
  does that.
- Incoming mails can contain personal data. Give access to the service board
  only to the people who handle requests.

### Protection against mail loops

- Mails with `Auto-Submitted` (other than `no`), `Precedence: bulk`, `list` or
  `junk`, or an `X-Loop` header, are discarded without a card.
- Mails from the own sender address (`smtp.from`) are discarded.
- The per-sender rate limit (see above) catches the rest.

The server remembers already processed mails by their Message-ID for 30 days so
that no mail arrives twice.

---

## Auto-reply

After a **new** card in the service board, the server can send the sender an
acknowledgement of receipt. This is **off by default**, because automatic mails
to outsiders in many cases need a legal basis under data protection law. Before
switching it on, check whether your privacy policy covers the sending.

Settings in the tab **"Mail auto-reply"**:

| Key | Default | Meaning |
|---|---|---|
| `mail_auto_reply.enabled` | `false` | on/off |
| `mail_auto_reply.template` | built-in German template | Markdown with the placeholders `{{.TicketId}}`, `{{.CardTitle}}`, `{{.BoardName}}` |
| `mail_auto_reply.from` | `smtp.from` | sender |
| `mail_auto_reply.reply_to` | sender | reply address |

The subject of the acknowledgement carries the identifier `[aBoard #…]` — if the
sender replies, the reply ends up as a comment on the same card.

Protection against loops and repeated sending:

- Replies to existing cards never trigger an acknowledgement.
- At most **one** acknowledgement goes out per sender and card, also across
  restarts; in addition at most one per hour and sender.
- Outgoing acknowledgements carry `Auto-Submitted: auto-replied` and
  `X-Loop: aboard`; out-of-office notes and bounces in response are discarded by
  the incoming mail.

"Test connection" renders the template here with sample data and shows subject
and text — no mail is sent. The acknowledgement needs working mail sending
**and** running incoming mail; "Save" restarts mail polling (see
[Making changes take effect](#making-changes-take-effect)).

---

## AI access

People can send cards to a language model. The server supports two modes of
operation.

### Own keys per person (default)

Each person stores their own API key per provider in the **user settings**
("Manage AI providers…"). Providers are `anthropic`, `openai`, `google` and
`mistral`.

- The server checks a key with a minimal call to the provider before storing it
  and then stores it encrypted (AES-256-GCM with `ABOARD_SECRET_KEY`). Without a
  master key, these functions answer with `503`.
- The server **never** returns the stored key, not even to the person
  themselves. Whoever loses it enters it again.
- At most 10 changes per hour and person.
- Billing happens at the provider, via the account of the respective person.
- The browser sends the key to the server in plain text. **Therefore run the
  server only behind HTTPS.**

### Company mode: one access for everyone

Instead of each person storing a key, the operator sets up the access once via
environment variables — for example to an own, OpenAI-compatible model server in
the own network:

```
ABOARD_LLM_OPENAI_BASE_URL=http://llm.example.org:8000/v1
ABOARD_LLM_OPENAI_MODELLE=modell-a,modell-b
ABOARD_LLM_SERVERSCHLUESSEL_FILE=/run/secrets/aboard-llm-schluessel
```

- The key is in a **file** (e.g. a Docker secret), not in the variable value. It
  appears neither in the log nor in a response. If the file is readable by
  others, the start log warns.
- There is then only the provider "openai" with the models from
  `ABOARD_LLM_OPENAI_MODELLE`; an empty model list stops the start. People can
  no longer enter their own keys.
- AI histories are visible only to the person who created them.
- At most `ABOARD_LLM_ANFRAGEN_PRO_STUNDE` requests per person and hour
  (default 60).
- The server administration only displays the company mode, the target and the
  models; it is changed exclusively via the environment, with a restart.

Further adjustments (context limit, tool calls, PII filter) are in
[configuration.md](configuration.md).

---

## Master key (`ABOARD_SECRET_KEY`)

A single key protects all secret data of the server: passwords in the server
configuration, the AI keys of the people, Gotify tokens, keys of the outbound
sluice and — if switched on — the board backups.

**Generating it** — 32 random bytes, either hexadecimal or Base64:

```bash
# 64 hex characters
openssl rand -hex 32

# 44 characters Base64
openssl rand -base64 32
```

**Storing it**, e.g. in an `.env` file next to the Compose file (readable only
by the operator, `chmod 600`):

```yaml
services:
  aboard-server:
    environment:
      ABOARD_SECRET_KEY: "${ABOARD_SECRET_KEY}"
```

A missing or invalid key does not stop the server: It starts, reports this in
the log, and all functions that have to store something secret answer with
`503`.

> ⚠️ **Keep the key separate from the server.** If it is lost, encrypted
> backups, stored passwords and AI keys are unreadable. Whoever has access to
> the container host can read the key from the container environment — protect
> the host accordingly.

**Changing the key:** Set the new value as `ABOARD_SECRET_KEY` and the old one
as `ABOARD_SECRET_KEY_PREVIOUS`. The old key is used exclusively for
**restoring** older encrypted board backups; writing always uses the new one.
There is only one previous key. Passwords in the server configuration, AI keys
and Gotify keys do not know the previous key — enter them again after the
change.

> ⚠️ **Built-in sign-in with own accounts:** The two-factor secrets of the
> password accounts are also encrypted with this key and do not know the
> previous key; there is no re-encryption. After a change, the server no longer
> accepts any code from the authenticator app (the page reports that two-factor
> sign-in is not readable). Then reset the people under "Accounts and
> invitations"
> ([Resetting](#resetting)); they set everything up anew. See also
> [Authentication, "Known limitation"](authentication.md).

---

## Board backups and restore

There are two independent backups:

| | Board backup | Server backup |
|---|---|---|
| Trigger | automatically daily, plus by button | command `aboard-server backup` |
| Scope | one board with its attachments | the whole database with all attachments |
| Restore | in the browser, one board | command `aboard-server restore` with the server stopped, into an emptied data directory (see below) |
| Who | board owner and admins | whoever has access to the container |
| Meant for | accidental changes, taking a board along | hardware replacement, data loss, the way back after an update |

### Daily board backup

By default, the server backs up **all boards** individually every night. The
settings are in the tab **"Backup"** or in the configuration:

| Key | Default | Meaning |
|---|---|---|
| `backup_board.enabled` | `true` | daily backup on/off |
| `backup_board.cron` | `0 2 * * *` | schedule in cron format (five fields), calculated in the server's time zone — in the container without `TZ` this is UTC |
| `backup_board.dir` | `/data/backups/boards` | storage location |
| `backup_board.keep_days` | `7` | retention in days; the most recent backups are kept in any case |
| `backup_board.max_upload_mb` | `500` | largest file when restoring |
| `backup_board.encryption_enabled` | `false` | encrypt new backups |

The files are stored per board in a directory of its own:

```
/data/backups/boards/
└── <board-id>/
    ├── boardbackup-<board-id>-20260521-020000-aabbccdd.tar.gz
    └── …
```

Encrypted backups end in `.tar.gz.enc`.

- **Encryption:** The switch "Encrypt backups" is at the top of the tab
  "Backup". It can only be switched on if a valid `ABOARD_SECRET_KEY` is set. If
  it is on and the key is missing, the server writes **no** backup instead of
  falling back to plain text, and reports this as a failed backup. Files that
  already exist stay as they are.
- **Failures** are reported by the server to all admins as a notification (see
  [Notifications](#notifications)).
- **Deleted boards:** Their backups initially stay in place so that the board can
  still be brought back. Only after `keep_days` (counted from the deletion) does
  a later run clear them away. Backup directories for which the server knows of
  no deletion — for example after a database loss — are never touched.
- **Set clock:** The server deletes old backups only if the system clock is
  demonstrably set (a time service such as `systemd-timesyncd` or `chrony` is
  running). Without a time service, everything stays; for systems whose clock is
  correct anyway, there is `ABOARD_UHR_PRUEFUNG=aus`. The state is shown in the
  tab "System status".
- **Disk space:** With many boards this adds up. Put `/data/backups` on a
  separate volume and monitor it; the server itself does not raise an alarm.
- **Moving them off** to another place (off-site) is a matter for operations,
  for example with rsync, rclone or restic on the backup directory.

### Back up now

- **One board:** In the tab "Backup", choose a board and press "Back up now".
  Board owners find the same button in the **board settings** of their board. At
  most 10 manual backups per hour and person.
- **All boards:** Without a board selection, the button is called **"Back up all
  boards now"**. This is the same run as at night, including clean-up. If one is
  already running, the button answers "already running" (`409`) — and the nightly
  backup skips itself if a run started by button is currently going on. At most
  10 started runs per hour and admin. The result of the last run (backed up,
  errors) is shown by the tab; it is lost on a restart.

### Restoring in the browser

Board owners do this in the **board settings** of their board, admins in the tab
"Backup". There, backups can also be downloaded and deleted. To restore, you
choose a backup file and a mode:

| Mode | Effect | Who may |
|---|---|---|
| **Overwrite** | replaces the content of an existing board including attachments; the board's shares remain | owner of the target board or admin |
| **As new board** | creates a new board, the restoring person becomes owner | owner of the board the backup came from, or admin |

When restoring, the server checks the file's checksum and the schema version. It
rejects a damaged or modified file, or a backup that is too old or too new, with
a clear message (`400`), and one that is too large with `413`. An encrypted
backup that matches none of the configured keys is explicitly reported as
"created with a different key".

After "Overwrite", the browser shows the restored state immediately; unsaved
changes in the open board are discarded. Every restore — also a rejected one —
leaves a line in the server log.

> **Tip:** If you are unsure, restore **as a new board** first and compare. The
> existing board stays untouched.

### Server backup: the way back for everything

The server backup saves the entire database (as a consistent copy, operation
continues) and all attachments into a `.tar.gz`. Board backups are **not**
included. The examples use the container name from the `docker run` example; with
Compose it is `docker compose exec aboard-server aboard-server …` (see [Server in
a container](container.md)).

```bash
# single archive
docker exec aboard-server aboard-server backup --out /data/backups/server.tar.gz

# directory: one archive per day, old ones are pruned
docker exec aboard-server aboard-server backup --out /data/backups \
  --keep-daily 7 --keep-weekly 4
```

With a directory as the target, the command writes `aboard-YYYY-MM-DD.tar.gz`
and keeps the most recent `--keep-daily` daily archives plus `--keep-weekly`
Monday archives.

- **Space needed:** For the consistent copy, the command first creates a
  complete copy of the database as a temporary file, in the container's temp
  directory (`/tmp`, unless `TMPDIR` says otherwise). There must be as much free
  space as the database is large; afterwards the command deletes the file again.
- **Do not copy by hand:** Copying the database file or the data directory while
  the server is running easily yields an incomplete copy, because the most
  recent changes are temporarily only in `aboard.db-wal`. While the server is
  running, always use this built-in backup.

> ⚠️ The server backup is **not encrypted**. The passwords and AI keys it
> contains are protected by the master key, but board contents and attachments
> are not. With built-in sign-in, it also contains the password hashes, the
> fingerprints of the recovery codes and the encrypted two-factor secrets (see
> [Privacy for accounts](#privacy-what-the-database-stores-about-accounts)). After
> restoring, the server needs the **same** `ABOARD_SECRET_KEY` as before.

`restore` writes the file `aboard.db` and the attachments contained in the
archive. An existing `aboard.db-wal` or `aboard.db-shm` it moves aside first
(renamed to `…vor-restore-<time>`, not deleted): SQLite would otherwise apply an
old WAL to the restored database, and the old content would be back afterwards.
Attachments that do not appear in the archive stay in place; `restore` states
their number and an example in the log. They belong to no card of the restored
database. Therefore:

1. Stop the server — `restore` with the server running destroys its open WAL.
2. Cleanest: empty the database and attachment directories, or mount fresh
   volumes.
3. First as a trial, then restore for real:

   ```bash
   aboard-server restore --dry-run /data/backups/aboard-2026-05-21.tar.gz
   aboard-server restore --force   /data/backups/aboard-2026-05-21.tar.gz
   ```

4. Start the server.

Without `--force`, the command aborts if a database already exists or the
attachment directory is not empty. What this looks like with
a container, and why a server backup belongs before every update, is described
in [container.md](container.md) in the section on updates.

---

## Notifications

The server can inform people about events — by **email** (via the configured
SMTP server) and by **Gotify** (to a Gotify server that each person enters
themselves). What they want to receive is set by each person in the **user
settings** under "Notifications…".

Events include: card assigned, commented, mentioned, moved or blocked, a new
card in the service board, a failed board backup and a subsystem that is not
running because of wrong configuration (the last two go to all admins).

> ⚠️ **Delivery is off by default.** Without
> `ABOARD_NOTIFICATIONS_DELIVERY=on`, the server processes and logs all events
> but delivers nothing — not even the daily digest. The start log says which
> state applies. This way no mail goes to real recipients while setting up or
> testing.

Further points:

- **Links:** For mails to contain a link to the card, set
  `ABOARD_PUBLIC_BASE_URL` to the public address, e.g.
  `https://board.example.org`. Without a valid value, the mails go out without a
  link.
- **Bundling:** Similar events on the same card within 5 minutes are combined.
- **Throttling:** At most 30 deliveries per hour per person and channel.
  Whatever exceeds that, the server collects and sends at **18:00**
  (server time) as a daily digest by mail.
- **Test:** `POST /admin/config/notifications/test` triggers the daily digest
  immediately.
- Events not yet delivered are held in memory; on a restart, individual ones can
  be lost.

---

## Disabling outbound connections

For operation without internet access, `ABOARD_AUSGAENGE=aus` switches off every
outgoing connection of the server. This affects exactly these paths:

| Path | Effect with `aus` |
|---|---|
| AI provider | sending and key test fail immediately |
| Gotify | notification and test send fail immediately |
| SMTP | card mails, notification mails and the connection test fail immediately |
| IMAP | polling and connection test do not open a connection |
| Outbound sluice | sending to external providers fails |

The server's own health check only talks to the server itself and is not
affected. Values other than `an` and `aus` stop the start.

**Allowing individual targets** with a comma-separated list of `host` or
`host:port`:

```
ABOARD_AUSGAENGE=aus
ABOARD_AUSGAENGE_ERLAUBT=llm.example.org:8000,smtp.example.org:587
```

- For HTTP, the host in the URL counts, not an intermediate proxy. Whoever goes
  beyond `HTTPS_PROXY` enters the actual target.
- For SMTP and IMAP, the configured mail server counts. These two follow no HTTP
  proxy; if the only way out leads through a proxy, mail sending and incoming
  mail stay off.
- Without a port in the list, any port applies; without a port in the URL, the
  scheme's default applies (443 or 80).
- The name is compared exactly as entered. A rule such as "local addresses are
  always allowed" deliberately does not exist, because it would depend on name
  resolution.

The start log states the state, e.g.
`Ausgänge: AUS — erlaubt nur: llm.example.org:8000, smtp.example.org:587`. A blocked
attempt appears with its target in the error message. Both variables are read
only at start.

---

## Outbound sluice (short form)

The outbound sluice is a controlled way to have a card processed by an
**external** language model without names, addresses and similar details leaving
the house. It is **off by default** (`ABOARD_SCHLEUSE=an` switches it on) and
separate from the normal AI access: It uses its own, separately stored keys.

**Prerequisites:**

- a valid `ABOARD_SECRET_KEY`;
- a **local** model in company mode (see [AI access](#ai-access)), whose
  `ABOARD_LLM_OPENAI_BASE_URL` points to an internal address (private network,
  `localhost`, a name without a dot or with endings such as `.intern`,
  `.local`, `.lan`). Without this model, the server rejects every excerpt.

**Procedure:**

1. **Approval (admin):** In the tab "Outbound sluice", an admin enters the
   permitted external providers — type (`openai-kompatibel`, `anthropic`,
   `mistral`, `google`), HTTPS address and permitted models — and confirms the
   contract form (business API account with data processing agreement, without
   training). Only public addresses are allowed. Optionally an approval requires
   a **four-eyes principle**.
2. **Key (person):** Each person stores their own key for an approved provider
   in the user settings.
3. **Excerpt:** On the card, the person chooses "Process externally", the parts
   (description, individual comments, attachments) and writes a task. The server
   reads the text itself from the stored board.
4. **Pseudonymisation:** The server replaces names, addresses, phone numbers,
   IBANs, URLs and similar with placeholders such as `[PERSON_1]` — first by
   fixed patterns and the board's people, after which the local model finds
   further places. The mapping placeholder → original stays encrypted on the
   server.
5. **Review:** The person sees the excerpt with marked placeholders and can edit
   it. Images go along only if they are approved individually; the server removes
   all metadata beforehand.
6. **Approval and sending:** "Approve and send" binds the approval to exactly the
   stored state (checksum). With four-eyes, a second person with write
   permission must agree. Then the server sends in the background.
7. **Response:** It appears as a labelled comment on the card, with the original
   names restored. Nobody can change the comment.

Board owners can **block** a board for the sluice (board settings, section
"Sharing"). If `ABOARD_AUSGAENGE=aus` is set, the hosts of the approved
providers must additionally be in `ABOARD_AUSGAENGE_ERLAUBT`; the tab "Outbound
sluice" names missing entries.

Further variables (`ABOARD_SCHLEUSE_AUFBEWAHRUNG_TAGE`,
`ABOARD_SCHLEUSE_AUSZUG_ZEICHEN`, `ABOARD_SCHLEUSE_AUFTRAEGE_PRO_STUNDE`,
`ABOARD_SCHLEUSE_PSEUDO_MODELL`) are in [configuration.md](configuration.md),
the paths in [api.md](api.md).

**Recommended order when switching on:** check the master key → start with
`ABOARD_SCHLEUSE=an` → approve providers and confirm the contract form → with
outbound connections disabled, allow the reported hosts and restart → check in
the system status that providers are counted as "usable" → only then let the
people store their keys.

---

## Knowledge source

The server can **display a Markdown folder read-only**: People see it in the
sidebar under "Knowledge" with overview, search and page view. It is meant to be
a Git checkout that a process of the operator keeps up to date and that another
tool reads at the same time — one source for both. aBoard fetches nothing from
Git, holds no Git credentials and never writes into the folder. The default is
**off**.

**Mounting:** Put the folder into the container read-only, below `/data/`, for
example in the Compose file `- /path/to/checkout:/data/wissen:ro`. The server
rejects other locations; `/data` itself as well.

**Switching on:** in the tab **"Knowledge"**:

| Field | Key | Meaning |
|---|---|---|
| Show knowledge in the sidebar | `wissen.enabled` | `true` switches on; off means: no entry, all paths `404` |
| Folder | `wissen.dir` | absolute path strictly below `/data/`; the folder must exist when saving |
| Display name | `wissen.name` | title in sidebar and dialog, empty = "Wissen" |
| Visible to | `wissen.sichtbar` | `alle` (every account on this server), `admins` or `gruppe:<name>` — then only people whom the sign-in service assigns to this group in `Remote-Groups`, plus admins. Needed where outsiders also get an account through a board share: "every account" includes them |
| Link "Edit in the repo" | `wissen.bearbeiten_url` | optional, `https` only, with exactly one `{pfad}` after the host (in the path or in the query); the page path is inserted URL-encoded per folder |

Saving takes effect immediately, without a restart. An invalid value is rejected
with a reason when saving; it can only be switched on if a valid, existing
folder is already stored. Emptying or deleting the folder is only possible while
the knowledge feature is off; switching off always works, even if the folder is
missing in the meantime. If a value fails, the dialog names which ones have
already been saved before.

If the folder is replaced under the same path (cloned anew, mount renewed), the
server reads the new one on the next access.

"Check stored folder" opens the folder and names page count and state — even
while the knowledge feature is still off.

**State:** If the pull process writes a file `.wissen-stand.json` into the root
folder (`{"commit": "c0db7e3", "datum": "2026-10-08T10:00:00Z"}`), the read view
shows commit and date, and the server rebuilds its index as soon as the file
changes. Without the file, it rebuilds it after five minutes at the latest.
aBoard never reads `.git/`.

**What is never displayed:** dot files and folders (`.git/` explicitly),
symbolic links — also those that stay inside the folder —, everything except
`.md`, folders deeper than 16 levels; at most 5000 pages and 50000 entries in
total. Pages over 1 MiB appear in the overview but open only with the note "too
large" and are not searched. The search compares plain text (no regex,
substrings, case-insensitive), from 2 to 200 characters, at most 50 hits, at
most 16 MiB read per search (beyond that it reports `gekappt`, i.e. truncated),
at most two searches at the same time (others wait; if the caller aborts, its
search ends too) and 30 searches per minute and person; overview and pages at
most 120 requests per minute and person. The interface displays pages in such a
way that nothing is loaded in: no images, no embedded HTML.

**Who sees it:** signed-in people with an account on this server (with `admins`
only admins). **Agent access tokens do not reach the paths**, not even a
delegating token.

The paths for humans: `GET /wissen/index`, `GET /wissen/seite?pfad=…`,
`GET /wissen/suche?q=…` (JSON). `/me` names `wissen` with the display name if
the feature is on and the person may see it.

---

## Board pair: internal and external

An organisation can run every board twice: on an **internal** server that is
reachable only in the company network, and on an **external** one. There are two
ways to set this up:

> The push script that carries changes between inside and outside is not part of this repository. Its
> interface (tokens `abs_`/`abe_`/`abr_`, paths under `/agent/schatten/` and `/agent/boardpaar/`) is described in the [API chapter](api.md).

- **External copy (recommended).** The external board is a **copy** of the
  internal one: swimlanes, columns, cards and appointments are in their place
  outside. Inside is the original. The copy exists from v1.54.0 (at first only
  with shadows); mirrors and editing outside come with the release after that.
  Non-confidential cards and appointments appear outside in full (as a
  **mirror**), confidential ones as a grey **shadow** with title. Whether the
  team may edit mirrors outside is switched on by the admin outside with "Allow
  editing outside".
  See [Creating an external copy](#creating-an-external-copy), [Mirrors and
  "confidential"](#mirrors-and-confidential) and [Allow editing
  outside](#allow-editing-outside).
- **Shadow coupling (stage 1).** Each card lives on exactly one of the two
  servers. The external server shows of internal cards and appointments only
  **shadows**: number `i-<nr>`, title (for confidential ones only "Internal
  card"), column, priority, appointments and assignees — read-only, in a table of
  its own, never in the board itself. The external board has cards of its own.

In both cases, a script run by the operator pushes the data from inside to
outside; the external server never calls inwards. A board is outside either a
copy or a stage-1 board, never both; the respective other path of the script
answers with `404`.

An admin sets this up, in the tab **"Board pair"** or on the command line in the
container. Board owners cannot do it (`403`): Coupling releases titles to the
outside and is a decision for the whole server.

| Part | Meaning |
|---|---|
| Side of this instance | `aus` (off, the default), `innen` (inside) or `aussen` (outside). The server reads it on every request; switching takes effect without a restart. Other open browsers see the consequences (switch "confidential" inside, band "Internal" outside) only after reloading. From v1.53.0, a change is only possible if no board is coupled any more on the previous side — otherwise the tab refuses ("uncouple below first"), and the command line names the boards. First uncouple (outside first), then switch; access tokens remain |
| Partner list inside | boards whose shadows the outbound path may deliver to the script. From v1.53.0 with the column "Last sync": last retrieval and, in red, the reason for the last failure (e.g. over 5000 entries); a successful run clears it |
| Partner list outside | boards that accept shadows, each with the address of the internal counterpart (`https://<intern>/#board=<id>`); from v1.53.0 with last receipt and failure reason as inside. External copies carry the suffix "— Copy" (command line: column "Kopie" instead of "Stufe 1") |
| Access tokens for the script | `abs_…` (outbound, on the internal server), `abe_…` (inbound, on the external one) and `abr_…` (return path, on the internal one: the script reports the targets, acknowledges jobs and enters changes from outside into the internal board, from stage 2c). `abe_` can additionally carry the right "may create copies" (only `abe_`). The plain text appears exactly once. From v1.53.0 the script renews its token itself (weekly): the column "Last renewed" shows time and address, the predecessor stays for ten minutes as "renewed, valid until …" and can be revoked individually; revoking the current one takes it along. A renewal that the script did not trigger itself is a theft — revoke the access token, create a new one |

The tab offers only boards of this server, never the service board. If the side
does not match the list, it shows a note; the list then takes effect only after
switching. The same on the command line (directly on the database, even without
a signed-in admin):

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

From v1.53.0, `liste` names per board the last retrieval or receipt and, where
applicable, "FEHLER beim letzten …" (error on the last …) with the reason.

- **Emergency stop:** Stopping the sync immediately, without deleting the
  shadows outside, works by **revoking the access token** (`token-widerrufen`,
  the script gets `401`). Setting the side to `aus` is only possible from
  v1.53.0 after uncoupling, and uncoupling outside deletes the shadows of the
  board.

- **Address of the internal board:** `https` only, without credentials and
  without a query, in the fragment exactly `board=<id>` (1–128 characters made
  of letters, digits, `. _ ~ -`), at most 512 characters. It serves only the
  link "Open anyway" for humans and never goes to agents.
- **Access tokens:** name 1–64 characters made of letters, digits, `. _ -`; one
  active access token per name and type. They apply to the whole server, without
  person, board or expiry date; only a hash is stored. A board-pair token gets
  through only on its own path, everywhere else there is `403`.
- **Uncoupling (stage 1): first outside, then inside.** Uncoupling outside
  deletes the shadows of this board immediately. Uncoupling inside only makes the
  script's next retrieval fail; the shadows outside stay until it is uncoupled
  outside. A deleted board takes its coupling and its shadows along.
- **Both servers need the same version.** The inbound path outside rejects a
  differing major version (`409`).

**What the side changes in the browser:** Inside, cards and appointments get the
switch **"confidential"**. On a **stage-1 board**, outside then shows only
"Internal card" after the next **successful** run. On an **external copy**, the grey shadow keeps its title and
loses the rest of the content
([Mirrors and "confidential"](#mirrors-and-confidential)). If the sync fails, the old state stays outside until then.
Emergency route on a stage-1 board: uncouple outside, which deletes the shadows immediately. On an external copy,
uncoupling does not help — the mirrors stay with full content, and the board is then freely editable afterwards (ops,
agents, from v1.55.0 also in the browser). Emergency route there: in the admin tab "Board pair" of the
**external** server, **"Delete copy outside"** (from v1.55.0; before that only as owner via
`DELETE /boards/{id}`). Every server admin may do this, even without being owner of the board; to confirm, one types
the name of the board (or its board ID). The button is at every copy in the partner list outside and, after uncoupling, under
"Copies uncoupled outside" ([Removing a copy](#removing-a-copy)). Deleting also takes along changes from outside that the
script has not yet acknowledged; the completed job stays inside, and a new job inside creates the copy
anew. Anyone may switch the "confidential" switch on, only a human may switch it off. **Before the first push**, review the existing content: existing cards
are not confidential, their titles go out with the first run. With an **external copy**, non-confidential cards even go out
in full, and the titles of confidential ones with them; therefore review the content for "confidential" before the first job. New calendar subscriptions are no longer
created inside (`409`) but on the external board; existing ones remain. Outside,
the board view of a **stage-1 board** shows above the cards a greyed band **"Internal"** with
the shadows and the state "Internal cards last synced: N minutes ago"
(from 30 minutes as a warning). A click on a shadow shows "This card
is internal. Please connect to the company network (VPN)." with the link "Open
anyway". The calendar view of an external board shows shadows with a date
greyed out and read-only (not in the mode "All boards"); the subscription feed outside
contains them as well. On an **external copy** there is no band: shadows and mirrors stand in their
swimlane and column; the band there catches only shadows whose swimlane or column does not exist outside.

### Creating an external copy

An admin creates a copy **inside**; it is carried out outside by the script. The internal server never calls
outwards. One-time preparation:

1. **Set the sides:** The instance inside is set to `innen`, the one outside to `aussen` (setting `boardpaar.seite`).
   Inside, the script additionally needs an `abs_` access token, with which it fetches jobs and the outbound path (the state of the internal
   board that goes outside); whoever runs stage 1 already has it.
2. **Outside, once per server:** create an access token with the right "may create copies" and give it to the script
   (tab "Board pair", section "Access tokens for the script": type `abe_`, tick "may create copies", or on the command line):

   ```bash
   aboard-server boardpaar token-neu --typ abe --name <name> --darf-anlegen
   ```

   An `abe_` from stage 1 does not have the right and does not get it by itself. `--darf-anlegen` exists only for
   `abe_` (otherwise an error). Without the right, creating answers with `403`.
3. **Outside:** set the **board-pair team** ([below](#board-pair-team)).
4. **Inside, once:** create a return-path access token `abr_` and give it to the script as well. With it, the script reports
   the names of its targets (names only, no addresses), acknowledges jobs and enters changes from outside into the internal
   board (from stage 2c):

   ```bash
   aboard-server boardpaar token-neu --typ abr --name <name>
   ```

Then per board, **inside** (the instance must be set to `innen`, otherwise `409`):

- In the tab "Board pair", section **"External copies"**: choose the board and the **target** (the external server),
  **"Create external copy"**. If the script has not yet reported any targets, enter the name of the target by hand,
  as the script calls it. If `ABOARD_PUBLIC_BASE_URL` is not set, the form additionally asks for the
  address of the board inside.
- Or on the command line:

  ```bash
  aboard-server boardpaar kopie-anlegen --board <id> --ziel <name> [--link 'https://<intern>/#board=<id>']
  ```

  Without `--link`, `ABOARD_PUBLIC_BASE_URL` + `/#board=<id>` applies; if both are missing, the command aborts. The name of the
  target consists of 1–64 characters (letters, digits, `. _ -`).

This creates a **job**. The board is then in the partner list inside, and the list shows "waiting for the
script (since …)". On the next run, the script creates the board outside, pushes the first outbound transfer (the state of the internal board that goes outside) and reports
"done"; the list then shows "created …". An open job can be deleted with "Withdraw", a
completed one cannot (`409`). The same open job twice for board and target is not possible (`409`). After withdrawing,
the board stays in the partner list inside; whoever wants to detach it completely uncouples it inside — unless a
stage-1 operation or another copy hangs on the same board (see "Converting stage-1 boards").

- **Outside**, the copy appears in the partner list with the suffix "— Copy". It gets the members from the
  board-pair team ([below](#board-pair-team)) and **all server admins as owners** (those who are server admins when it is created; admins appointed later are not).
- **Do not couple by hand:** A copy cannot be coupled again by hand after uncoupling outside
  (`409`). Whoever wants it back creates a new job inside: The script hits the same board, and the
  server **takes the copy back in** (board and members stay) — provided it created it as a copy itself.
  A copy that was uncoupled outside before v1.55.0 counts as an ordinary board; the job
  then ends outside with `409` (see [Removing a copy](#removing-a-copy)).
- **Copy uncoupled outside:** For the write guard it is an ordinary board (ops, agents): Editors can change everything, none of it goes inwards. The server does remember its origin, though (emergency route, list "Copies uncoupled outside", taking it back in); it therefore also refuses
  coupling by hand if the mirror markers are missing. The cards from inside stay **with full content** — the confirmation dialog when uncoupling
  a copy says so and points to "Delete copy outside". From v1.55.0 the
  browser also releases the former mirrors: When opening the board it asks the server whether it is still a copy; only on
  a clear answer "not a copy" does the lock fall, on an error it stays (until v1.54.0 they stayed
  locked in the browser always). A later taking-back-in overwrites changes from outside with the inner version; a
  browser that has the board open during the taking-back-in locks again only after reloading (until then the
  server rejects changes to mirrors with `403`). Changes from outside
  that the script has not yet acknowledged are deleted by the server after 25 days (on the next clean-up run); if the copy is
  taken back in before that, they still go inwards.
- **Uncoupling inside** deletes all jobs of the board, including completed ones. The external board remains; an admin
  outside deletes it with "Delete copy outside" ([Removing a copy](#removing-a-copy)). Nothing is ever deleted across the boundary.
- **Converting stage-1 boards:** A stage-1 board is not rebuilt. Create a copy inside (a
  **new** board is created outside), create the old board's own cards outside by hand inside, uncouple the old **external** board outside
  and delete it as its owner via `DELETE /boards/{id}` ("Delete copy outside" applies only to copies
  and rejects a stage-1 board with `409`; the browser has no button to delete a board). **Do not uncouple inside:** The internal board now carries the copy, and uncoupling inside
  deletes its job. The script must be switched to the new paths for this.
- <a id="removing-a-copy"></a>**Removing a copy ("Delete copy outside", from v1.55.0):** In the tab
  "Board pair" of the external server, every copy in the partner list outside and every copy uncoupled outside
  (separate list "Copies uncoupled outside") has the button **"Delete copy outside"**. It opens a confirmation: Only when
  the name of the board or its board ID is typed does "Delete permanently" become active. The board is deleted with everything that
  hangs on it — cards, appointments, attachments, shadows, coupling and not yet acknowledged changes from outside. Every
  server admin may do this, even without being owner of the board, but **only for copies**: A stage-1 board, an ordinary
  board and the service board are rejected by the server (`409`); on the side inside or "off" the path does not exist. A
  copy counts only as what the server created as a copy itself — a copy that was uncoupled outside **before** this release
  is not in the list and is treated as an ordinary board: No "Delete copy outside" button,
  and a new job for this board fails outside with `409` instead of taking the copy back in.
  Clearing it away works only as owner (the server admins at the time of creation) via `DELETE /boards/{id}` with
  browser sign-in — there is no button in the browser to delete a board. Afterwards a new job creates the
  copy anew.
  Nothing changes inside. Until a new job creates the copy anew there or the board is uncoupled inside,
  the script gets `404` for this board (as for any board that is not coupled). REST:
  `DELETE /admin/boardpaar/kopien/{board}` ([API](api.md)).
- **Only commissioned boards:** The internal server delivers the first outbound transfer only for a board with a job. A pure
  stage-1 board gets `404` there; its confidential titles stay inside.
- **Review the content:** Cards whose column or swimlane is missing inside do not go out. Their number appears as the
  failure reason at the sync state inside (tab, `boardpaar liste`).

### Board-pair team

The setting **`boardpaar.team`** (only on the **external** server) determines who sees each new copy. It is
entered in the tab "Board pair" (section "Board-pair team") or via `PUT /admin/boardpaar/team` (see
below: leaving only with confirmation).
Entries are **email addresses or `@domain`**, separated by commas (also semicolon or line break);
at most 100 entries, an invalid entry makes saving fail.

- The server knows no groups. A `@domain` acts like a group, but **only** if the domain is in
  `ABOARD_ACL_DOMAINS` ([Configuration](configuration.md)).
- Every new copy gets the team as **viewers**, with "Allow editing outside" as **editors**, and all
  server admins as **owners** (only those who are server admins when it is created). Without a team, only these admins see the copy.
- Whoever joins the team **later** is **not** entered on copies that already exist; there an owner shares
  by hand.
- Whoever is **removed from the team** (also when emptying the setting) loses on **all** copies — also on
  those uncoupled outside — the role of viewer or editor; owner stays owner (from v1.55.0).
  **Exception:** A copy that was uncoupled outside **before** this release counts as an ordinary board
  (no origin, migration 0042) — check the shares there by hand.
  If the same person was additionally shared by hand with the same role, this cannot be told apart — an owner
  then shares again.
- **Leaving only with confirmation** (from v1.55.0): If someone drops out on saving who still has access
  to copies, the dialog first shows **who** is affected and **how many** shares are removed, and
  saves only after "OK". Afterwards the message names the number of shares withdrawn. **Entering them again restores
  nothing** — on existing copies an owner then shares by hand. If someone else has changed the team in the meantime,
  the server refuses ("changed in the meantime"): reload the tab and check again. Via the API this means:
  `PUT /admin/boardpaar/team` without `"bestaetigt": true` answers with `409` and the list; the general path
  `/admin/config/boardpaar.team` rejects every withdrawal with `409` ([API](api.md)).
- **Caution when switching between `@domain` and individual addresses:** The entry is compared, not the person.
  Whoever replaces `@example.org` with `anna@example.org` removes the domain — Anna then also loses access to
  existing copies, because she was entered there only via the domain. Conversely, switching to the domain
  adds nothing. Share existing copies by hand afterwards.

### Mirrors and "confidential"

Outside, a copy looks like this:

| | Inside (original) | Outside |
|---|---|---|
| Swimlanes, columns, order | leads | copy, not changeable; lanes carry the note "Comes from the internal board" |
| Non-confidential cards and appointments | leads | **mirror**: complete, with comments and checklist; cards carry "↪ from inside", for attachments "📎 N internal only" (attachments do not go out) |
| Confidential cards and appointments | leads | **shadow**: grey, with **title**, priority, appointments and assignees, not editable |
| Cards created outside (only with "Allow editing outside") | arise here | are created outside, move inwards and come back as a mirror |

- **Titles of confidential cards:** On a copy, the shadow shows its **title**. "Internal card" is
  now only shown on shadows of **stage-1 boards** and on cards without a title. This applies in the display, the calendar feed and
  the agent tools.
- **Card becomes confidential inside:** On the next **successful** sync, its full content disappears outside, also
  the comments. Only the shadow remains. If a human switches "confidential" off again inside, the card is
  shown in full again on the next run.
- **Outside read-only as long as "Allow editing outside" is off:** The server rejects every change to mirrors,
  shadows, mirrored lanes and columns (ops `422`, whole-board PUT `403`, transferring a card to another board `403`).
  If the coupling is not readable: `503`, nothing written. Own swimlanes outside (such as "Proposals") stay
  free; owners and editors may write there, the team is only a viewer when "off". A board backup cannot be restored over a copy ("Overwrite" → `409`); "As new board"
  yields an ordinary board without mirrors.
- **Titles of swimlanes, columns and of the board** go outside unchanged (only stripped of control characters,
  lane 200, column 100 characters). Whoever may see the copy sees the structure as it is inside.
- **Own columns and lanes outside** stay if they carry cards; empty ones that do not exist inside
  disappear on the next sync.

### Allow editing outside

The setting **`boardpaar.rueckweg`** (`aus` or `an`, default `aus`; only on the **external** server)
switches the return path. In the admin tab "Board pair" it is called **"Allow editing outside"**; it is set there or
via `PUT /admin/config/boardpaar.rueckweg` (`DELETE` means `aus`).

- **Off** is the state "outside read-only" (see above).
- **On:** The team can edit mirror cards outside and create new cards in mirrored lanes. The server records
  every change in a journal (a list of the changes outside), and the script carries them inwards. Inside the original remains; outside the
  applied version appears again on the next outbound transfer.
- **The team becomes editors or viewers:** When switching on, the team (`boardpaar.team`) becomes **editors
  on all copies**, when switching off (also when deleting the setting) **viewers** again. This happens in the same
  transaction as the switch. Other roles (owners, those shared by hand) stay untouched, and saving
  the same value again does not turn any role back. Other open browsers show the change only after reloading.
- **Order when switching on:** (1) both servers on the release with the return path (one backup each beforehand; migrations
  0041 and 0042 have no down), (2) the script with the return path, (3) only then "Allow editing outside". Whoever switches on earlier
  loses nothing immediately: Changes outside stay in the journal until the script fetches them, and the outbound transfer does not overwrite
  unacknowledged places. If an entry stays unacknowledged for **25 days**, the retrieval reports "stale" and the script
  stops for this board and raises an alarm.
- **Back without a version change:** Switch "Allow editing outside" off. Then "read-only" applies again; open
  journal entries are still picked up.

**What works outside** (when "on", on mirror cards and cards created outside): title, description including checklist,
priority, critical, blocked, owner (only a person known inside), assignees, labels, date and time, archive, add comments,
**switch on** "confidential", take over a card or release the takeover (this does not go inwards);
moving and reordering, but only into a mirrored column of a mirrored swimlane; new cards there. Even
an own card that is dragged into a mirrored swimlane and column goes inwards — provided it carries no attachments
or references (they do not go inwards; the server then rejects with the general mirror message); back into a swimlane
or column without a mirror it cannot be moved. Appointments: all fields, switch on "confidential";
create new appointments.

**What stays inside:** deleting, QA, complexity, effort, repetition, project, attachments (also on comments),
references, agent context and AI, **switching off** "confidential", swimlanes and columns (create, rename, delete),
deleting comments (of any origin) and editing comments that come from inside. The server rejects this; the browser does not
offer it. Multi-select also takes no mirror cards, and the menu "Move" does not offer mirrored swimlanes;
everything permitted can be done individually. Own appointments of a copy cannot be deleted outside. If
a card created outside is still waiting for the script to bring it inwards, it can only be archived, not deleted.

**Conflict comment "Board-Paar":** If someone inside has meanwhile changed the same place, **the inner version applies**.
The change from outside is not lost: On the card **inside** there is a comment with the author **"Board-Paar"**
and roughly the wording "⚠️ Conflict via the board pair: <person> changed <place> outside on <time> to: <value>.
Inside, this place had meanwhile already been changed — the inner version applies. Adopt by hand if needed." Whoever
wants to adopts the value by hand. Long values are shortened. For an appointment, the comment is on its card;
if the card or appointment is confidential, or the appointment has no card, there is no comment. The comment itself is
not editable outside.

**Otherwise discarded:** If someone changes something outside that is no longer possible inside (the card has been deleted inside or
has meanwhile become confidential, the swimlane or column no longer exists), the internal server does not apply the change;
the script acknowledges it anyway. The outbound transfer then restores the inner version outside. This also covers:
an assignee that was set which the internal board does not know as a person (it disappears again after the next run;
likewise an unknown name in the people list of an appointment), an edited comment that no longer exists inside,
a new comment whose identifier is already taken inside with different text, an appointment that was deleted inside, and any field or deletion that the return path does not support. Likewise an owner that the internal
board does not know as a person.

**Exception: newly created cards are never discarded** (from v1.55.0). If their swimlane does not exist inside (any more),
is archived, is a service swimlane or otherwise one that the outbound transfer does not mirror, the internal server places the
card in the backlog swimlane; if the column is missing, in `col-backlog`, otherwise in the first column that the outbound transfer mirrors. If the internal board has no
usable backlog swimlane (missing, archived or a service swimlane), creating ends with `422`, and the script stops
— then restore the backlog swimlane inside or retrieve it from the archive. The card carries a comment by the author
**"Board-Paar"** that names the desired place with title. The next outbound transfer
then shows it outside at this place. Previously such a card was discarded and silently disappeared outside afterwards.

**Backups:** The board backup contains neither shadows nor coupling nor access tokens; shadows come back with the script's next run. A
board restore "Overwrite" leaves coupling and shadows untouched, "As new
board" yields an uncoupled board. The server backup contains everything; after
a server restore, access tokens that were revoked since the backup apply
again — check the list afterwards.

The paths of the script and their responses are in [api.md](api.md#board-pair).

---

## Embedding protection

The server sets the header `Content-Security-Policy: frame-ancestors 'self'` on
its responses — the web interface and the REST API. Foreign pages can thereby
not embed the board interface in a frame (protection against clickjacking); a
page under the same address still may.

An upstream reverse proxy should neither remove nor overwrite this header.
Security headers such as HSTS are not set by the server itself; they belong in
the reverse proxy.
