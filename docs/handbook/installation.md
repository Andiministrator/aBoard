# Installation

**English** · [Deutsch](../handbuch/installation.md)

This chapter describes how to obtain, set up and update aBoard. Which version suits you is explained in [Variants](variants.md).

## Obtaining

**Ready-made file:** The finished `aboard.template.html` is attached to every release of this repository (the "Releases" page). Download it, done.

**Build it yourself:** You need a current version of [Bun](https://bun.sh).

```bash
git clone <repo-url> aBoard
cd aBoard
bun install
bun run build:template      # aboard.template.html (single file)
bun run build               # aboard.html (project file)
```

`<repo-url>` is the address of this repository. Both files are created in the root directory.

## Single file

### Setting up

1. Put `aboard.template.html` in a fixed place and rename it if you like, for example `my-planning.html`.
2. Open it in the browser with a double-click. Done.

On first opening, aBoard creates an empty board with the columns Backlog, Ready, In Arbeit (in progress) and Erledigt (done); you can rename them.

**Do not forget to save.** Until the first save, the board lives only in browser storage. The **Save** button in the header writes it into the HTML file:

- In Chrome, Edge and other Chromium browsers, aBoard asks for the save location the first time and afterwards writes directly into that file.
- In Firefox and Safari, the browser downloads a new file. It contains app and data; put it in place of the old file.

In addition, **Menu → File → Export** backs up the board as a JSON file. The menu opens with a click on the logo at the top left.

### Updating

Your data lives in your saved HTML file, whereas a new version of aBoard comes with an empty board. This is how you take your board with you:

1. In the old file, **Save** and, to be safe, **Export** (JSON).
2. Keep the old file until the move has worked.
3. Open the new `aboard.template.html`.
4. **Menu → File → Open** and choose the exported JSON file or the old HTML file.
5. **Save** — this puts the board into the new file.

## Board in a folder

1. Put the file (`aboard.template.html` or `aboard.html`) into the folder where the board should live.
2. Open it in Chrome or Edge.
3. **Logo → Board Settings → "Choose folder…"** and choose the same folder. The browser asks for permission to read and write.

Afterwards the board lives in the folder as `aboard-sync.json`. Do not edit the file by hand, and keep **only one browser tab** with the board open. If you version the folder with git, include `aboard-sync.json`.

**Updating:** Close the browser tab, put the new file in place of the old one and open it again. The data lives in the folder and is preserved; after a browser restart, confirm the folder permission once.
