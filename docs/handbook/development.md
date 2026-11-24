# Development

**English** · [Deutsch](../handbuch/entwicklung.md)

This chapter is aimed at everyone who contributes to the aBoard code.

## Requirements

- [Bun](https://bun.sh) in a current version (package management, scripts, tests)
- a Chromium browser for trying out the folder connection

```bash
bun install
```

## Developing the frontend

```bash
bun run dev
```

starts the Vite development server; the address is shown in the output. It loads `index.html` with the sources from `src/` and reloads on changes.

## Building the two versions

```bash
bun run build && bun run build:template
```

| Command | Result | Contents |
|---|---|---|
| `bun run build` | `aboard.html` | with op-inbox and projector layer (project file) |
| `bun run build:template` | `aboard.template.html` | without projector layer, with size limit (single file) |
| `bun run build:server` | `server/web/dist/` | user interface for the server, embedded into the server program via `go:embed` |

All of them are built from the same `src/`. Anyone who changes something in `src/`, `assets/`, `index.html` or `vite.config*.js` therefore always builds the two single files; the server interface is only needed by whoever builds the server (the Dockerfile does that itself).

The displayed version number comes from `ABOARD_BUILD_VERSION`, otherwise from `git describe`:

```bash
ABOARD_BUILD_VERSION=v1.2.3 bun run build && ABOARD_BUILD_VERSION=v1.2.3 bun run build:template
```

## Build flags

aBoard is built from **one** source into several versions. Two constants that Vite substitutes at compile time control which code ends up in the result; the bundler removes unneeded branches completely.

- **`__SERVER_BUILD__`**: code for the multi-user server (REST connection, accounts, mail, AI). `false` in the two single files, `true` in the server interface; the code lives in `src/` because server and single file share the same sources.
- **`__PROJECTOR_BUILD__`**: the op-inbox and projector layer (`src/ops.js`, `src/inbox.js`, `src/diff.js` and the projector part of `src/sync.js`).

| Build configuration | Result | `__SERVER_BUILD__` | `__PROJECTOR_BUILD__` | Size limit |
|---|---|---|---|---|
| `vite.config.js` | `aboard.html` | `false` | `true` | none |
| `vite.config.template.js` | `aboard.template.html` | `false` | `false` | < 360000 bytes |
| `vite.config.server.js` | `server/web/dist/` | `true` | `false` | none |

Shared parts live in `vite.config.base.js`.

⚠️ **Not every path belongs behind `__PROJECTOR_BUILD__`.** Plain file access (choosing a folder, reading, writing, regular reloading) is present in every build. Only what really needs the op inbox belongs behind the flag.

**Size limit:** `aboard.template.html` must be smaller than 360000 bytes (decimal). The limit is a target against unnoticed growth, typically server or projector code that accidentally slips into the single file; in this repository no script checks it, so measure it yourself. To measure: `wc -c aboard.template.html`.

## Tests

```bash
bun run test
```

**Never `bun test`:** Bun's own test runner does not know the browser environment (happy-dom) and reports errors such as `document is not defined` in bulk.

`bun run test` runs three times, once per version:

| vitest configuration | `__PROJECTOR_BUILD__` | mirrors | Single run |
|---|---|---|---|
| `vitest.config.js` | `true` | `aboard.html` | `bun run test:standalone` |
| `vitest.config.slim.js` | `false` | `aboard.template.html` | `bun run test:slim` |
| `vitest.config.server-frontend.js` | `false` | server interface | `bun run test:server-frontend` |

The number of skipped tests differs between the runs; this is intended, because code behind the flags is missing in the other builds. Tests that apply only to the server build run only in the third run.

To run individual files specifically:

```bash
bunx vitest run --config vitest.config.js tests/<file>.test.js
```

New test files use the helpers from `tests/helpers/build-flags.js` (`describeProjektor` & co.) for flag-dependent parts.

### Server (Go)

The server lives under `server/` and has its own tests, separate from `bun run test`:

```bash
cd server && go test ./...
```

The server folds board changes with the same code as the app (`server/fold/`, an embedded bundle built from `src/ops.js`). Whoever changes `src/ops.js` regenerates the bundle with `bun run build:ops-bundle`. The server program including its interface is built by `server/Dockerfile`; see [Container](server/container.md).

## Structure

| Location | Contents |
|---|---|
| `index.html` | entry point, loads `src/main.js` |
| `src/` | the app: model, rendering, dialogs, saving, folder connection, op inbox |
| `src/style.css` | all styles, light/dark via `data-theme` |
| `src/i18n.js` | texts German/English |
| `tests/` | vitest tests with happy-dom, helpers under `tests/helpers/` |
| `assets/` | logo and icon |
| `docs/handbook/` | this handbook |

Comments in the code occasionally refer to decisions (`ADR-…`), findings (`F-…`) or cards (`#…`) from internal development. The documents for them are not in this repository; the comment itself carries the reasoning.

## Principles

- **One HTML file**, no runtime dependencies, no CDN links, no network access in the product.
- **Data stays local** (file, browser storage, shared folder); export as JSON.
- New features first ask themselves: do they belong in both versions or only behind a flag?
