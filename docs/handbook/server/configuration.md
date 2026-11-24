# Configuration: all environment variables

**English** · [Deutsch](../../handbuch/server/konfiguration.md)

The aBoard server is configured exclusively through **environment variables**. There is no
configuration file. This chapter lists **every** variable the server reads, with its default
and its meaning. How to pass the variables to the container
(`docker run -e …`, `environment:` in Compose, an `.env` file) is described in
[Container](container.md).

> **What is checked and what is not.** Defaults and descriptions were checked against the code when
> they were written, but they are not monitored automatically; when in doubt, the code is authoritative.

## General rules

- **Variables are read at startup.** After a change, restart the container.
- **Empty means not set.** A variable with an empty value behaves like a missing one:
  the default applies.
- **Three kinds of values:**
  - **Word switches** (`an`/`aus`, i.e. on/off, for some also further words): an unknown value stops
    the start. The server does not guess what was meant.
  - **On/off switches** (`ABOARD_TRUST_FORWARD_AUTH`, `ABOARD_FORWARD_AUTH_INSECURE_BIND`,
    `ABOARD_FORWARD_AUTH_VERBOSE`): empty, `0`, `false`, `no` and `off` mean **off**, any
    other value means **on**. `1` is customary.
  - **Values** (paths, addresses, numbers, lists): depending on the variable, invalid values stop
    the start or fall back to the default with a warning in the log. The tables
    say which applies.
- **Lists** are separated by commas; spaces around the entries do not matter.

## Secrets

Some variables carry secrets. The following applies to them:

- **Two variables take a file path instead of the value:** `ABOARD_LLM_SERVERSCHLUESSEL_FILE`
  and `ABOARD_OWUI_JWT_SECRET_FILE`. The server reads the secret from the file. That way it appears
  in no process list and in no container description. If the file is readable by other accounts
  (more open than `0640`), the server warns at startup. Recommended: `chmod 600`.
- **There is no general `…_FILE` variant.** The remaining secrets
  (`ABOARD_SECRET_KEY`, `ABOARD_SECRET_KEY_PREVIOUS`, `ABOARD_FORWARD_AUTH_SECRET`, `SMTP_PASS`,
  `IMAP_PASS`) are passed as values. They belong in an `.env` file with `chmod 600` next to
  the Compose file, never in an image, never in a repository and never in the database.
- **Generating keys:** `openssl rand -hex 32` produces a suitable value for
  `ABOARD_SECRET_KEY` and for `ABOARD_FORWARD_AUTH_SECRET`.
- Error messages and log lines of the server never state the content of a secret, at most
  the name of the variable or the path of the file.

## Server variables

### Basic operation: address, database, files

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_ADDR` | `:8080` | Address the server listens on, in the format `[host]:port`. `:8080` means all network interfaces. `127.0.0.1:8080` listens locally only. |
| `ABOARD_UNIX_SOCKET` | empty | Path to a Unix socket, e.g. `/run/aboard/aboard.sock`. If set, the server listens there instead of on `ABOARD_ADDR`. |
| `ABOARD_UNIX_SOCKET_MODE` | `0660` | File permissions of the Unix sockets (also of the agent socket). Only `0600`, `0660` and `0666` are allowed, also without the leading zero; any other value stops the start. `0666` is needed if proxy and server run in separate user namespaces; the directory of the socket then limits access. |
| `ABOARD_DB` | `/data/db/aboard.db` | Path of the SQLite database. SQLite also creates `-wal` and `-shm` files next to it; the directory must be writable. |
| `ABOARD_ATTACHMENTS_DIR` | `/data/attachments` | Directory for file attachments on cards and comments. |
| `ABOARD_ATTACHMENT_MAX_BYTES` | `52428800` (50 MB) | Maximum size per attachment in bytes. An invalid value (not a positive integer) silently falls back to the default. |
| `ABOARD_PUBLIC_BASE_URL` | empty | Public address of the board, e.g. `https://board.example.org`. The server builds links in notification mails and in replies to agent tools from it. Empty: no links. Only `http`/`https`, without credentials, query or fragment; an invalid value does not stop the start, but switches the links off and appears as a warning in the log. **With the built-in login switched on (`ABOARD_AUTH_MODE=builtin` or `ABOARD_ANMELDUNG=oidc`) it is mandatory, must be `https://` and must not carry a path** (it forms the callback address `<base>/anmeldung/rueckruf` or `<base>/anmeldung/rueckruf/<abbreviation>`, which the server serves at the root); otherwise the start aborts. |
| `ABOARD_SCHEMA_NEUER_ZULASSEN` | empty | Emergency switch: with `1` the server also starts on a database that a newer program version has already converted (with a warning). Without it, the start aborts in this case. Intended only for going back to an older version without a matching backup, see [Container](container.md). |
| `ABOARD_UHR_PRUEFUNG` | `an` | Time-limited deletion (logs, histories, backup rotation) and the claiming of cards by agents only run if the operating system reports the clock as synchronized. `aus` switches this check off, only for systems without a time service whose clock is correct anyway. Other values stop the start. |

### Retention periods

All periods are whole numbers of days from 1 to 36500. An invalid value falls back to the default
with a warning in the log. Deletion only happens with a synchronized clock
(`ABOARD_UHR_PRUEFUNG`).

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_ACTIVITY_RETENTION_DAYS` | `365` | Retention period of the activity log per board. |
| `ABOARD_ACTIVITY_MAX_PER_BOARD` | `100000` | Upper limit of change entries in the activity log per board, in addition to the period; beyond it the oldest are deleted. Mainly relevant for agents that write many changes. Allowed: 1 to 10000000. Changes to shares do not count and are never trimmed. |
| `ABOARD_LLM_RETENTION_DAYS` | `365` | Retention period of stored AI chat histories, measured from the most recent message of a history. |
| `ABOARD_AUDIT_RETENTION_DAYS` | `730` | Retention period of the audit log (configuration changes). Entries about admin rights, agent access, deleted users and the outbound gateway are kept indefinitely. |

### Login, forward auth and multi-user operation

Without `ABOARD_TRUST_FORWARD_AUTH` the server runs in single-user mode without login: anyone who
can reach it sees all boards. Multi-user operation behind a login proxy is explained in
[Login](authentication.md).

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_TRUST_FORWARD_AUTH` | off | On/off switch. On: the server takes the identity from the headers of the upstream login proxy (`Remote-Email`, `Remote-User`, `Remote-Name`, `Remote-Groups`), evaluates shares and rejects requests without an identity with `401`. Requires `ABOARD_FORWARD_AUTH_SECRET`. |
| `ABOARD_FORWARD_AUTH_SECRET` | empty | **Secret.** Mandatory as soon as `ABOARD_TRUST_FORWARD_AUTH` is on, otherwise the server does not start. The proxy sends the same value in the header `X-Aboard-Forward-Auth-Secret`; if it does not match, the server answers with `401`. |
| `ABOARD_FORWARD_AUTH_INSECURE_BIND` | off | On/off switch. With forward auth active, the server refuses to start if it listens on a TCP address that is reachable from outside (instead of only locally or on a Unix socket). On: it starts anyway, with a loud warning. Set it only if the network is sealed off in some other way. |
| `ABOARD_FORWARD_AUTH_VERBOSE` | off | On/off switch. On: one log line per request (accepted or rejected) instead of at most one warning line per minute. For troubleshooting. |
| `ABOARD_AUTH_MODE` | empty | Switch of the built-in login, English name of `ABOARD_ANMELDUNG`. `builtin` switches it on; `oidc` means the same; empty = off (single-user operation or forward auth). Both names at the same time are allowed (both values mean the same); if one value is invalid, the start aborts. Everything else as for `ABOARD_ANMELDUNG`. Example: `builtin`. |
| `ABOARD_ACCOUNT_SELF_PASSWORD` | off | On/off switch. On: people without a password (provider login only, or on the pre-approved list) can create a password with mandatory two-factor authentication for themselves in the account settings after a fresh login at their provider; off: the section is missing, the endpoints answer `404`. Values follow the on/off convention, but strictly: on = `1`, `true`, `yes`, `on` (also `ja`, `an`); off = empty, `0`, `false`, `no`, `off`; any other value (such as `aus`, `nein`, a typo) stops the start. The start also aborts if the switch is on and `ABOARD_AUTH_METHODS` does not offer the `password` method, or if it is on without the built-in login switched on. ⚠️ Anyone on the pre-approved list who creates a password then has an active account: removing them from the list no longer withdraws access; use "Block" for that (see [Login](authentication.md)). Example: `1`. |
| `ABOARD_OIDC_REAUTH_FALLBACK` | off | On/off switch. Anyone without a password confirms changes in the account settings by a fresh login at the provider. For this, aBoard requires the claim `auth_time`. **Google only supplies it for a published and verified app** with "Session age claims" switched on; otherwise the confirmation always fails (audit `ergebnis=auth_time_fehlt`, message "Your login service does not confirm a fresh login"). On: if `auth_time` is missing, an ID token that the provider issued in exactly this process suffices (`iat` not before its start, with 1 minute of clock skew, at most 5 minutes old; audit `ok_ohne_auth_time`, otherwise `iat_alt`). If the provider supplies `auth_time`, the strict check always applies. ⚠️ Consequence: without `auth_time`, the confirmation only proves that someone currently has access to the provider account at the browser. It still protects against a stolen aBoard cookie, but not against someone at an open computer that is logged in to Google. Values strict as for `ABOARD_ACCOUNT_SELF_PASSWORD`; a typo stops the start. ⚠️ **Enter it only after the update:** servers up to v1.61.0 do not know the variable and abort at startup (any unknown `ABOARD_OIDC_*` variable stops the start). Example: `1`. |
| `ABOARD_AUTH_METHODS` | empty | Which login methods the page `/anmeldung` offers. Comma list (also semicolon or whitespace) of `password` (own accounts, always set up) and the abbreviations of the configured OIDC providers: `standard` for the provider from `ABOARD_OIDC_ISSUER`/`_CLIENT_ID`/`_CLIENT_SECRET`, otherwise the abbreviations from `ABOARD_OIDC_PROVIDERS`. **Empty = all configured methods**, i.e. including the password form. A provider that is not named is switched off completely: its start and its callback answer with `404` "Unknown login service"; without `password`, `POST /anmeldung/passwort` answers with `404`. The start aborts on a value that is not a configured method (typo), on duplicate values and on a list without any method. If exactly one method remains and it is a provider, `/anmeldung` redirects straight to it; to get that, set `ABOARD_AUTH_METHODS=<abbreviation>`. Only effective with the login switched on; without it the variable stops the start. A typo in the name (`ABOARD_AUTH_METHOD`) also stops the start. Example: `google,authelia`. |
| `ABOARD_INVITE_METHODS` | empty | Which methods an invited person can choose when redeeming the link. Empty = all methods that `ABOARD_AUTH_METHODS` offers. Otherwise a comma list of these methods, e.g. `password` (own accounts only) or `google`. A method that `ABOARD_AUTH_METHODS` does not offer, a duplicate value or an empty list stop the start. Only effective with the login switched on (otherwise the variable stops the start), and only when redeeming; whether someone gets in afterwards is decided by an admin's confirmation ([Server administration](administration.md)). The invitation link is valid for 7 days and can be redeemed once; whether it is additionally sent by mail depends only on the configured mail server (SMTP), not on this variable. Example: `password,google`. |
| `ABOARD_ANMELDUNG` | empty | Former name of the switch of the built-in login; it remains valid and means the same as `ABOARD_AUTH_MODE=builtin`. `oidc` switches on the built-in login (OpenID Connect providers and/or own accounts with a password) instead of forward auth; empty = off. Values other than empty, `oidc` and `builtin` stop the start, as does `oidc` together with `ABOARD_TRUST_FORWARD_AUTH` or `ABOARD_DEV_FAKE_USER`/`_EMAIL`. Requires `ABOARD_PUBLIC_BASE_URL` (https) and `ABOARD_SECRET_KEY`. An OIDC provider is no longer mandatory: without a provider, the server only offers own accounts with a password (method `password`, see `ABOARD_AUTH_METHODS`); a half-configured provider still stops the start. Conversely, any `ABOARD_OIDC_*`, `ABOARD_AUTH_*` or `ABOARD_INVITE_*` variable that is set without the switch being on stops the start — otherwise the server would run without a login. Flow, admission and troubleshooting: [Login](authentication.md). Example: `oidc`. |
| `ABOARD_OIDC_ISSUER` | empty | Address (https) of the provider `standard`, e.g. `https://accounts.google.com`. The three variables `ABOARD_OIDC_ISSUER`, `_CLIENT_ID` and `_CLIENT_SECRET` together form the provider `standard` (the former single provider); its callback remains `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf`. Mandatory (all three together) only if no provider is configured through `ABOARD_OIDC_PROVIDERS`; if one of the three is set, all three must be present, otherwise the start aborts. With `ABOARD_AUSGAENGE=aus`, its host and the hosts from its discovery document (`<issuer>/.well-known/openid-configuration`: `token_endpoint`, `jwks_uri`) must be listed in `ABOARD_AUSGAENGE_ERLAUBT`. |
| `ABOARD_OIDC_CLIENT_ID` | empty | Client ID of the OIDC client created at the provider, part of the provider `standard` (see `ABOARD_OIDC_ISSUER`). Callback address there: `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf`, character for character. Example: `1234-abc.apps.googleusercontent.com`. |
| `ABOARD_OIDC_CLIENT_SECRET` | empty | **Secret.** Client secret of this client, part of the provider `standard` (see `ABOARD_OIDC_ISSUER`). Not into Git, not into the log. Example: `<client-secret>`. |
| `ABOARD_OIDC_ZUGELASSEN` | empty | Former name of `ABOARD_OIDC_ALLOWED`; it remains valid and means the same: the pre-approved list — addresses and `@domain`, separated by comma, semicolon or whitespace. Plus always `ABOARD_BOOTSTRAP_OWNER`. Anyone on this list gets in as long as their account is not blocked. Besides that, anyone with an active account gets in (an invitation that an admin has confirmed, see [Server administration](administration.md)); new installations therefore do not need the list. The status of the OAuth client at the provider (at Google "Testing") does not protect. Withdrawing access means removing the address and restarting. A share or a domain share does **not** let anyone in. An invalid entry stops the start; empty and without a bootstrap owner (and without an active account), the server starts with a warning and nobody gets in. Changes take effect after the restart, also for people who are already logged in. Example: `anna@example.org, @example.net`. |
| `ABOARD_OIDC_ALLOWED` | empty | English name of `ABOARD_OIDC_ZUGELASSEN`, same meaning. If one or the other name is present, it applies; if both are present with different content, the start aborts. Example: `anna@example.org, @example.net`. |
| `ABOARD_OIDC_PROVIDERS` | empty | Abbreviations of further OIDC providers, separated by comma (also semicolon or whitespace), e.g. `google,authelia`. For each abbreviation the four following variables apply; `<ID>` is the abbreviation in capital letters, `-` becomes `_` (`heim-2` → `ABOARD_OIDC_HEIM_2_…`). Abbreviations: 1–32 characters `a–z`, `0–9`, `-`; `standard` and `password` are reserved; an abbreviation may only appear once. Callback address at the provider: `<ABOARD_PUBLIC_BASE_URL>/anmeldung/rueckruf/<abbreviation>`. A listed abbreviation without its own variables is not offered (warning in the log); if half-configured it stops the start, as do variables of an abbreviation that is not listed and unknown `ABOARD_OIDC_*` names (typos). Example: `google,authelia`. |
| `ABOARD_OIDC_<ID>_ISSUER` | empty; for `google` `https://accounts.google.com` | Address (https) of the provider with the abbreviation `<ID>`. Mandatory, except for `google`. Example: `https://auth.example.org`. |
| `ABOARD_OIDC_<ID>_CLIENT_ID` | empty | Client ID of the OIDC client created at the provider; mandatory as soon as a variable of this abbreviation is set. Example: `aboard`. |
| `ABOARD_OIDC_<ID>_CLIENT_SECRET` | empty | **Secret.** Client secret of this client; mandatory as soon as a variable of this abbreviation is set. Example: `<client-secret>`. |
| `ABOARD_OIDC_<ID>_LABEL` | `Google` for Google, otherwise the host of the issuer | Label of the button "Sign in with <LABEL>" on the login page. Example: `Company account`. |
| `ABOARD_BOOTSTRAP_OWNER` | empty | Email address of the first responsible person, e.g. `person@example.org`. **Default board (`default`):** only takes effect on the first start with an empty database, when that person becomes its owner; on an existing database it has no effect (taking over existing boards: see [Login](authentication.md)). A value that is not a personal address (without `@`, or `@company.example`) is skipped for both boards with an error message in the log. **Service board for incoming mail:** if it is switched on, the server sets the address as its owner again on **every** start, as long as the variable is set. After setup, therefore remove it — but only once there is an administrator. **Administrator:** as long as there is none, only this person becomes administrator (at their first login, or at the next start if they already have an account); if the value is invalid, nobody does — see [Login](authentication.md). |
| `ABOARD_ACL_DOMAINS` | empty (none) | Mail domains for which board owners may set a share "everyone with @domain", e.g. `example.org,example.net`. At most read or write permission, never ownership. Enter only domains whose addresses the login service really verifies, never freemail domains. |
| `ABOARD_DEV_FAKE_USER` | empty | **For development only, never in operation.** Fakes the header `Remote-User` so that multi-user operation can be tried out without a proxy. Only effective together with `ABOARD_TRUST_FORWARD_AUTH`; if it is set, the start warns clearly. |
| `ABOARD_DEV_FAKE_EMAIL` | empty | **Development only.** Fakes `Remote-Email` (only effective if `ABOARD_DEV_FAKE_USER` is set). |
| `ABOARD_DEV_FAKE_GROUPS` | empty | **Development only.** Fakes `Remote-Groups` (only effective if `ABOARD_DEV_FAKE_USER` is set). |
| `ABOARD_DEV_FAKE_NAME` | empty | **Development only.** Fakes `Remote-Name`, the display name (only effective if `ABOARD_DEV_FAKE_USER` is set). |

### Agents and Open WebUI

Agents (scripts, AI assistants) log in with an access token (`abt_…`), not through the login
proxy. How to create access tokens is described in [Server administration](administration.md); the
proxy is described in [Login](authentication.md).

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_AGENT_PFAD` | `an` | Agent entry under the path `/agent/` on the normal port (tokens only, no browser login). `aus` switches it off; `/agent/…` then answers with `400`. Other values stop the start. The path only becomes publicly reachable once the proxy routes it past the login. |
| `ABOARD_AGENT_LISTEN` | empty (no separate entry) | TCP address of an **additional**, separate agent entry, e.g. `127.0.0.1:8081`. Excludes `ABOARD_AGENT_UNIX_SOCKET` and must not be the same address as `ABOARD_ADDR`. If it does not listen locally only, the start warns: tokens then travel unencrypted over the network unless a TLS proxy is in front. |
| `ABOARD_AGENT_UNIX_SOCKET` | empty | Unix socket for the separate agent entry, e.g. `/run/aboard/agent.sock`; permissions per `ABOARD_UNIX_SOCKET_MODE`. Alternative to `ABOARD_AGENT_LISTEN`. |
| `ABOARD_AGENT_PROXY` | empty | Trusted peers for the lockout after failed attempts (ten wrong tokens in ten minutes lock for ten minutes; at the password form of the built-in login five wrong entries in 15 minutes); from v1.53.0 also for the throttle of the board-pair paths (`abs_`/`abe_`/`abr_`: shadow paths 120 failed attempts per hour, renewal 30 per hour). Comma list of networks (`198.51.100.0/24`), single IP addresses, DNS names (e.g. `proxy`, resolved again every minute) and the word `unix`. Only from these peers is the header `X-Real-IP` taken over as the client address. **The proxy must overwrite `X-Real-IP` with the real client address** (Caddy: `header_up X-Real-IP {client_ip}` in the `reverse_proxy` block — `{client_ip}` is the TCP peer; if another entry point that is listed in `trusted_proxies` stands in front of Caddy, it is the address from its `X-Forwarded-For`; nginx: `proxy_set_header X-Real-IP $remote_addr;`). Caddy does not set the header by itself and passes one sent by the client through unchanged; the attacker then chooses their own address and the lockout does not take effect. Empty behind a proxy: all clients share the address of the proxy, and the start warns. An invalid network stops the start. |
| `ABOARD_OWUI_URL` | empty | Address of an Open WebUI instance, e.g. `https://chat.example.org`. If set: the card window shows a button "Continue in Open WebUI". Only `http`/`https`, without credentials, query or fragment; an invalid value stops the start. |
| `ABOARD_OWUI_JWT_SECRET_FILE` | empty | **Secret as a file.** Path to the file with the shared secret of Open WebUI and aBoard (at least 32 characters). If set: Open WebUI may act on behalf of the logged-in person with a delegating token. Empty: such tokens are rejected with `401`. An unreadable file or a secret that is too short stops the start. |
| `ABOARD_OWUI_JWT_MAX_AGE_SECONDS` | `300` | Maximum age of the confirmation of the person that Open WebUI sends with every chat request, in seconds (60 to 3600). For long replies in Open WebUI, set the same value there. Only read if `ABOARD_OWUI_JWT_SECRET_FILE` is set; an invalid value then stops the start. |

### Mail

There are **no** `ABOARD_` variables for mail. Outgoing mail (notifications) and the mail intake
(mails become cards on a service board) are configured in the admin area, see
[Server administration](administration.md). The following variables are only **initial values**:
at startup the server takes them into the database, but only if no value is stored there yet.
After that, what is configured in the admin area applies; a changed variable
then has no effect.

| Variable | Default | Meaning |
|---|---|---|
| `SMTP_HOST` | empty | Mail server for outgoing mail. |
| `SMTP_PORT` | `587` | Port of the mail server. |
| `SMTP_USER` | empty | User name at the mail server. |
| `SMTP_PASS` | empty | **Secret.** Password at the mail server. Only taken over if `ABOARD_SECRET_KEY` is set, otherwise skipped with a warning. |
| `SMTP_FROM` | empty | Sender address. |
| `SMTP_FROM_NAME` | empty | Sender name. |
| `SMTP_STARTTLS` | `true` | Encryption via STARTTLS; `false` switches it off. |
| `IMAP_ENABLED` | `false` | `true` switches the mail intake on. |
| `IMAP_HOST` | empty | Mailbox server for the mail intake. |
| `IMAP_PORT` | `993` | Port of the mailbox server. |
| `IMAP_USER` | empty | User name of the mailbox. |
| `IMAP_PASS` | empty | **Secret.** Password of the mailbox; like `SMTP_PASS` only with `ABOARD_SECRET_KEY`. |
| `IMAP_TLS` | `true` | TLS connection to the mailbox server. |
| `IMAP_FOLDER` | `INBOX` | Folder from which new mails are read. |
| `IMAP_PROCESSED_FOLDER` | `Processed` | Folder for processed mails. |
| `IMAP_ERROR_FOLDER` | `Errors` | Folder for mails that could not be processed. |
| `IMAP_POLL_INTERVAL_SEC` | `600` | Interval between two polls in seconds (at least 60). |
| `IMAP_MAX_MAIL_SIZE_MB` | `25` | Larger mails end up in the error folder. |
| `IMAP_MAX_ATTACHMENT_SIZE_MB` | `10` | Larger attachments are not taken over; the card gets a note. |
| `IMAP_RATE_LIMIT_PER_HOUR` | `20` | Maximum number of mails per sender and hour; further ones end up in the spam column. `0` switches the limit off. |
| `SERVICE_BOARD_ENABLED` | `true` | Creates the service board for incoming mail. |
| `SERVICE_BOARD_TITLE` | `Service` | Title of the service board when it is created. |
| `MAIL_SPAM_DROP` | `false` | `true`: mails recognized as spam are discarded instead of being put into the spam column. |

### AI assistant (LLM)

Without these variables, each person brings their own key for an AI provider
(stored encrypted, requires `ABOARD_SECRET_KEY`). With
`ABOARD_LLM_SERVERSCHLUESSEL_FILE` the **company mode** applies: one key of the operator for
everyone, only approved models. Details in [Server administration](administration.md).

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_LLM_OPENAI_BASE_URL` | empty (`https://api.openai.com/v1`) | Address of an OpenAI-compatible provider, e.g. your own model server in the company network: `http://llm.example.org:8000/v1`, with the version path. Only `http`/`https`, port 1 to 65535, without credentials, query or fragment; an invalid value stops the start. Without a path, the start warns. |
| `ABOARD_LLM_OPENAI_MODELLE` | empty | Comma list of the model names the provider serves; the interface offers exactly these. Mandatory in company mode and at the same time the list of allowed models. Empty with `ABOARD_LLM_OPENAI_BASE_URL` set: warning in the start log. Names with whitespace stop the start. |
| `ABOARD_LLM_OPENAI_KONTEXT` | empty (no limit) | Context size of your own model in tokens (512 to 10000000). If set: the server rejects histories that are too long before sending, instead of waiting for the model's error. Only together with `ABOARD_LLM_OPENAI_BASE_URL`, otherwise the start aborts. |
| `ABOARD_LLM_OPENAI_WERKZEUGE` | `an` | `aus` does not send tool descriptions to the OpenAI-compatible provider, for models that cannot handle tools. The model then makes no change proposals and does not load linked cards. Other values stop the start. |
| `ABOARD_LLM_SERVERSCHLUESSEL_FILE` | empty | **Secret as a file.** Path to the file with exactly one provider key of the operator. Setting it switches company mode on: only the provider "openai" (with `ABOARD_LLM_OPENAI_BASE_URL`, if set), no personal keys, only models from `ABOARD_LLM_OPENAI_MODELLE`. An unreadable, empty or multi-line file or an empty model list stops the start. |
| `ABOARD_LLM_PII` | depends on the mode of operation | Default for the filter of personal data: `aus`, `pflicht` or `nutzer` (each person chooses). Default: without company mode `nutzer`; in company mode with its own `ABOARD_LLM_OPENAI_BASE_URL` `aus`, without it `pflicht`. A value that is set also applies without company mode; `aus` without company mode produces a warning in the log. Other values stop the start. |
| `ABOARD_LLM_ANFRAGEN_PRO_STUNDE` | `60` | Maximum number of chat requests per person and hour, effective only in company mode; beyond it the server answers with `429`. Whole number 1 to 100000; an invalid value stops the start, also without company mode. |

The retention of the chat histories is governed by `ABOARD_LLM_RETENTION_DAYS` (section
Retention periods).

### Encryption and backup

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_SECRET_KEY` | empty (off) | **Secret.** Master key, 32 bytes as hex (64 characters) or Base64. Encrypts everything secret in the database: mail passwords, personal AI keys, notification credentials, calendar subscriptions, keys of the outbound gateway, and board backups if their encryption is switched on. If it is missing, the server keeps working, but these functions answer with `503`. A change makes the values encrypted so far unusable; they must be entered again. **Caution, built-in login:** the two-factor secrets of the own accounts are encrypted with it as well; after a change the server no longer accepts any code from the authenticator app, and the people must be reset in the admin tab and invited again (`ABOARD_SECRET_KEY_PREVIOUS` does not help there; see [Login](authentication.md), "Known limitation"). |
| `ABOARD_SECRET_KEY_PREVIOUS` | empty | **Secret.** The previous master key after a change. Only used when restoring board backups, so that archives made with the old key remain readable; writing always uses the current one. If it is unusable, the restore continues without it and the log states the reason. |

Backup and restore are described in [Server administration](administration.md).

### Notifications

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_NOTIFICATIONS_DELIVERY` | off | Enables the actual delivery of notifications (mail, Gotify). Only `1`, `on`, `true`, `yes`, `ja` or `an` switch it on; any other value means off. Without the enabling, everything runs up to the delivery and is logged, but nothing is sent. That way the setup can be tested without writing to real recipients. |

The channels themselves (mail server, Gotify) are configured in the admin area, see
[Server administration](administration.md).

### Outbound connections and the outbound gateway

`ABOARD_AUSGAENGE` governs whether the server makes any outbound connections at all. The
outbound gateway is a separate, narrow path on which a person sends a pseudonymized and
reviewed excerpt of a card to an external model. [Server administration](administration.md)
explains both.

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_AUSGAENGE` | `an` | `aus` blocks all outbound connections of the server: AI providers, Gotify, SMTP, IMAP. Independently of that, Gotify does not reach internal addresses (loopback, private networks, link-local; checked on the resolved address), not even through `ABOARD_AUSGAENGE_ERLAUBT`; the only exception is `ABOARD_GOTIFY_INTERN_ERLAUBT` (below). The user then sees "not reachable", the cause is in the server log. A blocked path reports "outbound connection switched off" immediately instead of running into a timeout. Other values stop the start. |
| `ABOARD_AUSGAENGE_ERLAUBT` | empty | Targets that remain reachable with `ABOARD_AUSGAENGE=aus` anyway, as a comma list of `host` (any port) or `host:port`, e.g. `llm.example.org:8000,mail.example.org:587`. No URLs, no wildcards; otherwise the start aborts. If traffic goes through a proxy, the actual target is listed here, not the proxy. Without effect with `an`. |
| `ABOARD_GOTIFY_INTERN_ERLAUBT` | empty | Gotify targets that are reachable even when they are internal (LAN, Docker network, `localhost`), as a comma list of `host` or `host:port`, e.g. `gotify.example.org` or `gotify:8443,[::1]:8080`. **The approval applies only to the port that is entered; an entry without a port applies only to 443.** Another port of the same host is rejected on entry and not dialed by the dialer. Literal comparison with the host of the Gotify URL, ignoring case and a trailing dot; no URLs, no wildcards, ASCII only; an invalid entry stops the start. There is no DNS trust: only exactly this name with this port is dialed without an address check, redirects remain forbidden. In addition, entering a Gotify URL rejects internal IP literals (e.g. `192.0.2.1`, `169.254.169.254`, `127.0.0.1`), `localhost` and hosts with non-ASCII characters, unless they are listed here. Applies independently of `ABOARD_AUSGAENGE`: with `aus`, the target must additionally be listed in `ABOARD_AUSGAENGE_ERLAUBT`. If Gotify runs through `HTTPS_PROXY`, the entry is not needed; but if the Gotify host is in `NO_PROXY`, the connection goes direct, and then the entry is needed. |
| `ABOARD_SCHLEUSE` | `aus` | `an` switches the outbound gateway on. With `aus` there are neither routes nor interface for it. Other values stop the start. Requires `ABOARD_SECRET_KEY`. |
| `ABOARD_SCHLEUSE_AUFBEWAHRUNG_TAGE` | `30` | Days for which the mapping of placeholders to real names is kept after a job is completed; drafts that were never sent expire the same time after their last change. Whole number 1 to 3650; an invalid value stops the start. |
| `ABOARD_SCHLEUSE_AUSZUG_ZEICHEN` | `20000` | Size limit per excerpt in characters (500 to 200000); an invalid value stops the start. |
| `ABOARD_SCHLEUSE_AUFTRAEGE_PRO_STUNDE` | `10` | Maximum number of jobs sent per person and hour (1 to 1000); an invalid value stops the start. |
| `ABOARD_SCHLEUSE_PSEUDO_MODELL` | first model from `ABOARD_LLM_OPENAI_MODELLE` | Local model of company mode for the second stage of pseudonymization. Must be listed in `ABOARD_LLM_OPENAI_MODELLE`, otherwise the start aborts with the gateway switched on. |

### Calendar

There is no variable of its own for calendar subscriptions (iCal). They require `ABOARD_SECRET_KEY`, because
the subscription tokens are stored encrypted; without it the subscription functions answer with
`503`. Behind a login proxy, the path `/ical/` must be exempt from the login,
because calendar programs cannot log in, see [Login](authentication.md).

### Knowledge source

No environment variable. Mount the Markdown folder read-only below `/data/` in the Compose file
(for example `- /srv/knowledge:/data/wissen:ro`) and switch it on in the admin tab "Wissen" (knowledge), see
[Server administration](administration.md).

### Board pair

No environment variable of its own; the board pair (the external copy from v1.54.0, mirror and `boardpaar.rueckweg` only from v1.55.0) runs on **settings** that an admin sets in the tab "Board-Paar" of the
admin interface or through the admin API ([Server administration](administration.md),
[REST API](api.md#board-pair)). The server reads them on every request; switching takes effect without a restart. Two
of the environment variables take part: `ABOARD_PUBLIC_BASE_URL` (default for the address of the internal board when
you create an external copy) and `ABOARD_ACL_DOMAINS` (only domains from it take effect in the team as `@domain`).

| Setting | Value | On which server | Meaning |
|---|---|---|---|
| `boardpaar.seite` | `aus` (default), `innen`, `aussen` | both | Side of this instance. From v1.53.0 a change is only possible if no board is coupled any more on the previous side. |
| `boardpaar.team` | empty or email addresses and `@domain`, separated by comma (at most 100) | outside | Who sees every new external copy: as reader, with `boardpaar.rueckweg = an` as editor; all server admins who are admin when the copy is created become owners. A `@domain` only takes effect if it is listed in `ABOARD_ACL_DOMAINS`. Whoever joins the team later is not added on copies that already exist; whoever is removed from the team loses the role of reader or editor on all copies (also those decoupled outside, except those decoupled before the release) (owners remain; from v1.55.0). What is compared is the entry: replacing `@domain` with an address of that domain also withdraws access from that person. Such a withdrawal only works with confirmation (admin dialog or `PUT /admin/boardpaar/team`); the general configuration path rejects it with `409`. An invalid entry rejects the save. |
| `boardpaar.rueckweg` | `aus` (default, also empty or deleted), `an` | outside | "Allow editing outside": the team may edit mirror cards outside, the changes go inside. On a change, the role of the team is adjusted on all coupled copies (`an`: readers become editors, otherwise the reverse); copies decoupled outside stay as they are. Any other value rejects the save. |

### Miscellaneous

| Variable | Default | Meaning |
|---|---|---|
| `ABOARD_SCHLEUSE_PDF_KIND` | empty | **Internal, do not set.** Marks the helper process that the server itself starts to read text from PDF files. Only effective together with an internal start argument; in the environment of the server the variable has no effect. |

## Note on the Compose file

The supplied `docker-compose.yml` uses a few helper variables that are **not** read by the
server, but by Compose when it substitutes the values, or by the backup loop in the
companion container:

- `ABOARD_VERSION`: image version, default `dev`.
- `ABOARD_PORT`: port on the host, default `8080`. The Compose file publishes it only on the loopback interface (`127.0.0.1`), because the server has no access protection without an upstream login.
- `ABOARD_BACKUP_DIR`, `ABOARD_KEEP_DAILY` (default `7`), `ABOARD_KEEP_WEEKLY`
  (default `4`): target directory and retention of the optional backup container; they
  are passed on as arguments to `aboard-server backup`.

Details in [Container](container.md).
