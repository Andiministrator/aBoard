# Which variant suits you?

**English** · [Deutsch](../handbuch/varianten.md)

Two versions of the board app are built from this repository. Both show the same board with the same columns, cards and swimlanes (horizontal rows across all columns, for example one per sprint). They differ in **where the data lives**.

How to set them up is described in [Installation](installation.md).

## The two versions at a glance

| | Single file | Project file |
|---|---|---|
| **File** | `aboard.template.html` | `aboard.html` |
| **Build with** | `bun run build:template` | `bun run build` |
| **Where the data lives** | in the HTML file itself (embedded on save), in between in browser storage; JSON export; optionally in a shared folder | in a shared folder (`aboard-sync.json`) |
| **Who else can write** | only you in the browser | you in the browser; with the project tools also programs such as Claude Code (tools not yet published) |
| **Requirements** | a current browser; for the folder connection a Chromium browser (Chrome, Edge …) | a Chromium browser |
| **Internet required** | no | no |

Rule of thumb: **the single file for almost everyone.** The project file additionally contains the op-inbox layer, through which tools outside the browser can safely write along later; without these tools it offers no advantage over the single file with a folder connection.

## Single file (`aboard.template.html`)

The simplest form: one HTML file that you open in the browser with a double-click. No server, no installation, no network access. All libraries are embedded in the file.

**How the data is kept:**

- While you work, aBoard buffers every state in the browser's storage (`localStorage`). If aBoard finds unsaved changes there the next time it opens, it offers to load or discard them.
- **Save** writes the board into the HTML file itself: afterwards the file contains app and data together and can be passed on or backed up. In Chromium browsers this happens directly into the file via the File System Access API; other browsers download a new file instead, which you put in place of the old one.
- **Export** creates a JSON file. **Open** loads a board from a JSON file or from another aBoard HTML file.

Browser storage is only a buffer. If you clear the browser history, you lose everything that was not saved or exported.

Fields that are only intended for working with AI agents (complexity, effort, agent context) are hidden by the single file as long as it is not connected to a folder.

## Folder connection

Both versions can put the board into a folder: **Logo → Board Settings → "Choose folder…"**. Afterwards the state lives in that folder as `aboard-sync.json`, and aBoard regularly checks whether anything there has changed. This is useful if the board should sit next to a project and be versioned with git.

In the handbook, working with a folder connection is also called **project mode**. Where a server is mentioned there, it refers to the multi-user server, which has not been published yet.

The folder connection requires the **File System Access API** (Chrome, Edge and other Chromium browsers). After a browser restart, the browser asks once for permission to access the folder.

## Project file (`aboard.html`)

The project file additionally contains the **op-inbox and projector layer**: if a folder has been switched to the op inbox (`aboard-sync.base.json` and `aboard-sync.inbox/` next to `aboard-sync.json`), every change ends up as a small file of its own in the inbox, and several writers do not get in each other's way. Switching is done with a command-line tool that will be published later together with the Claude Code integration. Until then, the project file behaves like the single file with a folder connection.
