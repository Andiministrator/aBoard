<div align="center">
  <img src="assets/aBoard-250px.png" alt="aBoard Logo" width="180" />
  <h1>aBoard</h1>
  <p><strong>Offline Kanban as a single HTML file</strong></p>
</div>

**English** · [Deutsch](README.de.md)

---

aBoard is a Kanban board that runs directly in the browser as a single HTML file: no installation, no internet, no backend. Double-click, and off you go.

The idea comes from [Nullboard](https://github.com/apankrat/nullboard) by Alex Pankratov, but aBoard goes further: sprints as swimlanes (horizontal rows across all columns), structured cards with Markdown, flags, estimates and links, a calendar, templates and recurring cards.

The documentation is available in English and German.

## What is in this repository

This repository is a **public extract** of the project. It contains the board app (source code, build, tests) and the user handbook. The optional multi-user server will follow later. Development itself takes place in an internal repository; comments in the code therefore occasionally refer to decisions (`ADR-…`), findings (`F-…`) or cards (`#…`) that are not included here.

## Quick start

**Ready-made file:** `aboard.template.html` is attached to every release (the "Releases" page of this repository). Download it, open it in the browser, done. The file can be copied, sent around and opened anywhere; the data lives inside it, and a JSON export is built in.

**Build it yourself** (with a current version of [Bun](https://bun.sh)):

```bash
bun install
bun run build:template      # creates aboard.template.html
```

`bun run build` additionally creates `aboard.html` with the op-inbox layer for shared writing with tools outside the browser. Which version suits which purpose is described under [Variants](docs/handbook/variants.md).

## Documentation

The **[Handbook](docs/handbook/README.md)**: [Variants](docs/handbook/variants.md) · [Installation](docs/handbook/installation.md) · [Usage](docs/handbook/usage.md) · [Development](docs/handbook/development.md).

## Features

| Area | What |
|---|---|
| **Board** | Columns Backlog → Ready → In Progress → Done, drag & drop, multiple boards |
| **Swimlanes** | Sprints and thematic rows, collapsible, archivable |
| **Cards** | Markdown description, owner and assignees, labels, comments, attachments, due date with time |
| **Flags and estimates** | ⚡ Critical · ⊘ Blocked · ✓ QA; complexity and effort in hours |
| **Links** | blocks / depends on / related, with a back-link on the target card |
| **Templates** | Card templates and recurring cards |
| **Calendar** | Month and week view, events even without a card |
| **Search and filter** | Full text (`/`), filter by owner, label and flags |
| **Keyboard** | `n` new card · `j`/`k` navigate · `?` help |
| **Interface** | Light/dark, German/English, usable on mobile |

## Development

```bash
bun install
bun run dev                              # development server
bun run build && bun run build:template  # both versions
bun run test                             # tests in both build contexts (not: bun test)
```

`bun test` (Bun's own test runner) does not work because it does not set up the browser environment (happy-dom); always use `bun run test`.

## License

Apache License 2.0, see [LICENSE](LICENSE).
