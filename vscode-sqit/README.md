# SQit for VS Code

Table view and GitHub-style red/green change highlighting for
`sqit` repositories – Git-like version control for MySQL data.

## What you get

* **SQit activity bar** with two trees:
  * **Tables** – every tracked table (all tables of the database minus
    `.sqitignore`, or the explicit list in `.sqit/config.json`) with its uncommitted changes (LOCAL
    database vs HEAD): `+added ~modified -deleted`, expandable to the changed
    records.
  * **Upstream (TEST)** – the same for the last fetched upstream snapshot vs
    HEAD (what a `pull` would bring in).
* **Table grid** (click a table): all rows like in an SQL client. Added rows
  are green, deleted rows red (struck through), modified rows show the old
  value in red and the new value in green inside the cell. Switch between
  *LOCAL vs HEAD* and *UPSTREAM vs HEAD*, filter, or show only changes.
* **Commands** (title bar buttons / Command Palette `SQit:`): Refresh,
  Commit, Fetch, Pull (dry run), Pull, Status, Diff, Log. Output goes to the
  *SQit* output channel; conflicts are reported there with BASE / LOCAL /
  UPSTREAM values.
* **Status bar** item with the working-tree counts; refreshes when the
  window regains focus or `.sqit/` changes (commit/pull from a terminal).

## Requirements

* The `sqit` CLI on your `PATH` (`go install ./cmd/sqit` puts it into
  `~/go/bin`), or set `sqit.executable` to its full path.
* A workspace containing a `.sqit/config.json` (`sqit init …`). Passwords
  are read by the CLI from `sqit.env` / the environment – the extension
  never sees them.

## Build / install

```bash
cd vscode-sqit
npm install
npm run compile
npm run package          # -> sqit-0.1.0.vsix
code --install-extension sqit-0.1.0.vsix
```

Or press F5 in VS Code with this folder open to run it in an Extension
Development Host.
