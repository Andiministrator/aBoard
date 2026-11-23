# Usage

**English** · [Deutsch](../handbuch/bedienung.md)

This chapter explains everyday work with the board: columns, cards, swimlanes, filters, calendar and keyboard. Some functions exist only with the multi-user server, which has not been published yet; this is noted where it applies.

Which variant you have in front of you is explained in [Variants](variants.md).

## In five minutes: the essentials

- aBoard shows a **Kanban board**: columns with cards that represent tasks. The columns reflect the status, from left to right.
- As a **single file**, aBoard is one HTML file: double-click, open in the browser, done. **Save** writes the board into this file.
- With a **folder connection**, the board lives in a folder as `aboard-sync.json`. You never touch the file by hand and keep **only one browser tab** with the board open.
- What you control yourself: putting cards in the right column, setting flags, entering due dates.

## The screen

**Header:** logo, board title, search field, filters, the buttons **＋** (add column), **⊞** (add swimlane) and **Save**. On narrow screens some of them move into the **⋯** menu.

**Menu:** A click on the **logo** opens the sidebar. It contains:

| Area | Contents |
|---|---|
| File | Open, Export (JSON) |
| Views | Calendar |
| Settings | **User Settings** (identities, theme light/dark/system, language German/English) and **Board Settings** (mode and project folder, board background, templates, attachment limits) |
| Documentation | project mode only: read the project's Markdown files, such as the README |
| more | server only: Knowledge (if set up), Server administration (for admins), Clients and Projects |

At the bottom of the sidebar is the version of aBoard.

## Columns

A fresh board has four columns: **Backlog → Ready → In Progress → Done**.

| Column | Meaning |
|---|---|
| Backlog | parking lot for ideas: captured, but not yet released for work |
| Ready | commitment point: this is to be done next |
| In Progress | currently being worked on |
| Done | completed |

Columns can be renamed (click the title), collapsed, added (**＋**) and deleted. The column header also offers bulk actions: archive or delete all cards of the column. Deleting applies across all swimlanes, which is why aBoard asks first.

The column names are a default, not a fixed rule of the software.

## Swimlanes: sprints and lanes

Swimlanes are horizontal rows across the whole board. Every card belongs to exactly one swimlane. At the very top there is always the **Backlog** swimlane for everything that is not assigned to a sprint.

With **⊞** you create a new swimlane and choose its type:

- **Sprint** — a work period with a start and an end. Every sprint gets two special cards: **📋 Sprint start** for instructions and notes at the beginning, **🏁 Sprint end** for the retrospective at the end. As soon as the first card of the sprint leaves the Backlog, the start card moves to Done when the board is loaded; this shows that the sprint is running. The sprint header shows progress, for example "3/8 cards", and, once effort has been estimated, done against estimated hours.
- **Lane** — a topic or person row without sprint mechanics: no special cards, no progress display.

A swimlane can later be switched between sprint and lane, collapsed and expanded, and moved (drag the handle or use the arrow keys up/down). When a sprint is deleted, its cards move to the Backlog.

The special cards 📋 and 🏁 can be neither deleted, moved nor archived; only description and comments can be edited.

**Archiving swimlanes:** With **📦** in the sprint header, a whole swimlane disappears from the board. Its cards are preserved unchanged. With the "Show archive" filter it reappears, collapsed and with the note "Archived".

## Cards

### Creating and moving

- **"+ Add card"** at the bottom of a column, or the `n` key. If templates exist, you first choose "Empty" or a template.
- **Moving** by drag & drop, also between swimlanes. This works on smartphones too.
- **Several cards at once:** with Ctrl-click (Mac: Cmd-click) select cards one by one, with Shift-click a range, with `Ctrl+A` all visible ones. A bar then offers Move, Set owner, Add/Remove label and Delete. `Esc` clears the selection.

Every card has a running number (`#12`) by which it can be referred to unambiguously.

### The card window

A click on a card opens it. The window has several tabs:

| Tab | Contents |
|---|---|
| Details | title, description (Markdown), labels |
| Comments | comment history; `Ctrl+Enter` sends a comment |
| ⛓ Links | links to other cards |
| 📎 Attachments | files and images |
| 📋 History | who changed what and when |
| 🤖 Agent context | project mode and server only, when Claude is owner or involved: a context package for AI agents |

Below are the fields of the card (see the next section). When the window is closed, aBoard saves the changes automatically.

**Description:** It opens in reading mode. **"✏ Edit"** switches to editing mode: a click on a paragraph makes it editable, and a toolbar helps with formatting (`Ctrl+B` bold, `Ctrl+I` italic, headings, lists, checklists, links, quotes). **"✓ Done"** switches back. Checkmarks in checklists can also be set in reading mode. aBoard loads images from foreign servers only after a click, so that nothing goes onto the network unnoticed.

**Exporting a card:** The "📄 Export" menu in the card window prints the card or saves it as a PDF, saves it as Markdown, or opens an email with its content.

### Card fields

| Field | Meaning |
|---|---|
| **Priority** | Low, Medium, High |
| **Labels** | free keywords, separated by commas, for example `bug, feature` |
| **Owner** | who is responsible for the card: a person or, in project mode and on the server, a Claude model |
| **Assignees** | further people or models who contribute |
| **Complexity** (`einfach`/`mittel`/`hoch`) | a rough estimate of how demanding the task is — project mode and server only |
| **Effort (h)** | estimated hours, for example `0.5` — project mode and server only |
| **⚡ Critical** | the card is urgent and blocks progress |
| **⊘ Blocked** | the card is waiting for something else (an answer, an external system) |
| **✓ QA** | the card has been checked and is ready for acceptance |
| **Due date** | a deadline; optionally with a time |
| **Start date** | a start date; optionally with a time, for information only |
| **Recurring** | daily, weekly (with weekday) or monthly (with day of the month) |

Complexity and effort help to estimate a task realistically before it starts; the tile shows both as small badges. The flags Critical, Blocked and QA are simple yes/no switches, visible as symbols on the card.

**Due date:** An overdue card gets a red badge, one due within the next three days a yellow one. In the Done column the badge is omitted.

**Owner and identities:** Which names are available in the Owner field is set under User Settings → Identities (display name, color). On a server board, the Owner field instead offers the people who have a role on the board; Claude models only on boards that already use them.

### Recurring cards

A card with a repeat is a template that stays in the board. When the next date falls due, aBoard creates a new card as a copy when the board is loaded. The copies themselves carry no repeat.

### Templates

In the card window, **"Save as template"** saves a card as a template. When you create a new card, the template is then offered for selection. Templates are managed in the Board Settings.

### Links between cards

In the **⛓ Links** tab you connect cards with each other:

| Type | Counterpart on the other card | Meaning |
|---|---|---|
| blocks | blocked by | this card holds up the other one |
| blocked by | blocks | this card is waiting for the other one |
| depends on | required for | soft dependency, can be worked on in parallel |
| required for | depends on | the other card needs this one |
| related to | related to | for information only |

aBoard enters the counterpart on the other card automatically. A card with an open "blocked by" shows the ⛔ badge. If you move it anyway, aBoard asks. When the blocking card is done, aBoard reports that the other one is now free.

### Attachments

In the **📎 Attachments** tab you add files: via "Choose file…" or by drag & drop onto the window. An image can serve as the card's **cover** and then appears on the tile.

Where the files end up depends on the variant:

- **Single file:** in the HTML file itself. That is why there are limits for the individual file and for the total size of the board (adjustable in the Board Settings). Large attachments make the file sluggish.
- **Project mode:** in the `attachments/` subfolder of the project folder.
- **Server:** on the server. There, files can also be attached to comments. Deleting works only while the server is reachable and you are logged in: if it fails, the attachment stays in the card and a note explains why (no permission, or try again later). If you delete a whole card while the server is not responding or your login has expired, aBoard tries to remove its files again as soon as your deletion has reached the server — as long as the tab stays open. If you close it beforehand, the files remain on the server.

### Archiving

Done cards that you no longer want to see but want to keep are archived (**📦 Archive** in the card window, or for a whole column in the column header). Archived cards disappear from the column but are kept and included in the export. The column header shows with 📦 and a number how many are there. The special cards 📋 and 🏁 cannot be archived.

## Searching and filtering

**Search:** the `/` key or a click into the search field. The search ignores upper and lower case and covers title, description, comments, labels and owner. Cards without a match are hidden, matches are highlighted. A cross in the field resets the search.

**Filter:** in the header you filter by owner (also matches assignees), by label, and by the flags ⚡ Critical and ⊘ Blocked. Unlike the search, the filter does not hide but **dims** the cards that do not match; this keeps the context visible. The "Show archive" switch shows archived cards and swimlanes.

Search and filter are not saved; after a reload everything is visible again.

## Calendar

**Menu → Views → Calendar** shows all cards with a due or start date. There are three views: **Month**, **Week** and **Agenda** (the next 30 days). Cards with a time appear at that time.

- **Dragging** a card to another day moves its dates.
- A click into an empty day creates a **new card** with that date.
- Filters: show done cards, restrict by owner.

Additionally, on the server only:

- **All boards** — the calendar shows cards from all boards you may read,
- **Events without a card** — "＋ Event" creates a pure event (title, date, optionally multi-day, from/to, location, description),
- **Subscribe** — a secret calendar address for calendar apps,
- filter by project.

## Board overview and multiple boards

A click on the board title in the header opens a menu with the current and the most recently used boards; "Show all boards…" shows the full list. The pencil ✎ next to the title renames the board. On the server there is also "New board" there.

On a **server board**, "Show all boards…" opens the **board overview**: all boards you may see, as tiles with a background image, separated into "My boards" and "Shared with me". Each tile shows role, modification date and the board ID, which can be copied with a button. A board can be shared in the Board Settings in the "Sharing" section.

## Keyboard

| Key | Effect |
|---|---|
| `/` | focus the search field |
| `n` | new card in the first visible column |
| `j` / `k` | highlight next / previous card |
| `Enter` | open the highlighted card |
| `Esc` | clear multi-selection, otherwise clear the highlight or close the help |
| `?` | open/close the overview of shortcuts |
| `Ctrl+A` / `Cmd+A` | select all visible cards |

The shortcuts do not work while an input field has focus or a card window is open. The card window has its own shortcuts: `Esc` closes, `Ctrl+B`/`Ctrl+I` format, `Ctrl+Enter` sends a comment.

## Saving, opening, exporting

| | Single file | Project mode | Server |
|---|---|---|---|
| **automatic** | interim state in browser storage | every change lands in the project folder | every change lands on the server |
| **Save** | writes board and app into the HTML file | writes into the project folder | — |
| **Open** | loads a board from a JSON or aBoard HTML file | like single file | — |
| **Export** | JSON file | JSON file | JSON file |

In the single file there is also the **AI Context** button at the bottom left of the settings dialog: it creates a Markdown summary of the board (`ki-kontext.md`) to pass on to a language model.

If the single file finds unsaved changes from the last session when opening, it offers to load or discard them.

On the server, aBoard warns if someone else has changed the board at the same time, and lets you choose: keep your own version or load the server's.

## Board in a folder (project mode)

With **Logo → Board Settings → "Choose folder…"**, aBoard puts the board into a folder as `aboard-sync.json` and regularly checks whether anything there has changed. This requires the **File System Access API** (Chrome, Edge and other Chromium browsers); after a browser restart, the browser asks once for permission. Never edit `aboard-sync.json` by hand, always use the board interface.

### The one-tab principle

**Keep only one browser tab with the board open at any time.**

As long as a tab is open, the browser holds its own interim storage (`localStorage`) with your latest editing state. If you open a second tab, for example because the first seems frozen, it brings its **own, possibly outdated** interim storage. If a state is restored or compared from it, this can look like a huge number of deleted cards, even though none of that was intended.

`aboard.html`, by contrast, has a built-in brake (the single file does not): a deletion that affects an unusually large number of cards (more than 20) or swimlanes (more than 2) at once triggers a safety prompt with the exact count before anything is written. The default of this prompt is "Cancel". The brake is an additional safety net, but no substitute for the rule: **one tab, one current state.**

In practice this means: before opening a new tab, really close the old one; do not just open a new window next to it.

## Further reading

- [Variants](variants.md) — what each operating mode can do
- [Installation](installation.md) — setting up and updating
