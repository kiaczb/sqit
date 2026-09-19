# sqit

Git-like version control and synchronisation for **MySQL data**.

`sqit` treats your LOCAL MySQL database as a working tree, records
content-addressed snapshots of the configured tables as commits, tracks the
TEST database as an upstream, and merges TEST changes into LOCAL with a
three-way, field-level merge – without SQL dumps, triggers, binlog or daemons.

```bash
sqit init --tables users,orders --local-db app_local --test-db app_test --test-host test-db
sqit commit -m "Initial data"
sqit status
sqit diff
sqit diff --upstream
sqit diff --schema
sqit pull --dry-run
sqit pull
```

See [DESIGN.md](DESIGN.md) for the storage model and merge semantics.

## Requirements

* Go 1.25+ (build); MySQL 8.x and 5.7 are tested, the core flow also passes on 5.5 (JSON columns and fractional seconds need 5.7+)
* every tracked table must have a primary key (composite keys are fine)
* the LOCAL user needs `SELECT/INSERT/UPDATE/DELETE` on the tracked tables;
  the TEST user needs `SELECT`; both need read access to `information_schema`

## Build

```bash
go build -o sqit ./cmd/sqit
```

## Setup

```bash
sqit init --tables users,orders \
  --local-host 127.0.0.1 --local-port 3306 --local-user root --local-db app_local \
  --test-host  test-db   --test-port  3306 --test-user  app  --test-db  app_test
```

This writes `.sqit/config.json` (no secrets). Passwords come from the
environment:

```bash
export SQIT_LOCAL_PASSWORD=...
export SQIT_TEST_PASSWORD=...
# or full DSNs (go-sql-driver format), which override host/port/user/db:
export SQIT_LOCAL_DSN='root:secret@tcp(127.0.0.1:3306)/app_local'
export SQIT_TEST_DSN='app:secret@tcp(test-db:3306)/app_test'
```

Commit `.sqit/` into your Git repository together with the application code;
everything in it is deterministic JSON.

## Commands

| command | meaning |
|---|---|
| `sqit init` | create `.sqit/` |
| `sqit status [--fetch] [--json]` | working-tree changes vs HEAD, upstream changes vs HEAD, relation (`CLEAN`, `LOCAL MODIFIED`, `UPSTREAM AHEAD`, `LOCAL AHEAD`, `DIVERGED`, `UNRELATED`, `NO UPSTREAM`) |
| `sqit diff [--json]` | WORKING TREE vs HEAD, field level |
| `sqit diff --upstream [--fetch]` | UPSTREAM vs HEAD |
| `sqit diff --schema [--upstream]` | schema differences |
| `sqit commit -m "..."` | snapshot LOCAL into a new commit |
| `sqit fetch` | snapshot TEST as a new upstream commit (only if it changed) |
| `sqit pull [--dry-run] [--json]` | fetch, three-way merge, apply to LOCAL, record a merge commit or fast-forward |
| `sqit log [--upstream]` | history (first-parent walk) |
| `sqit show [<commit>]` | a commit and its changes vs its parent |
| `sqit inspect <type> <hash>` | dump a raw object |

`status` and `diff --upstream` use the last fetched upstream snapshot and do
not contact TEST unless `--fetch` is given. `pull` always fetches.

### Pull semantics in short

* BASE = merge base of HEAD and the upstream commit (the first local commit
  when the histories are unrelated, e.g. you committed before the first pull).
* The merge's LOCAL side is the **working tree**, so uncommitted changes take
  part in conflict detection and in the automatic field-level merge.
* Uncommitted changes stay uncommitted after a pull; the merge commit records
  HEAD merged with upstream.
* Conflicts (`modify/modify`, `add/add`, `modify/delete`, `delete/modify`)
  abort the pull before anything is written. `--json` gives them as
  structured objects for tooling.
* All LOCAL writes run in one transaction that is verified against the
  expected snapshot before commit; on any error nothing changes.
* Incompatible schemas (different columns, base types, nullability or primary
  key) abort the pull; `sqit` never runs `ALTER TABLE`.

## Tests

Unit tests need nothing:

```bash
go test ./...
```

Integration tests run against a real MySQL server when `SQIT_IT_DSN` points
at an account that may create databases (they create and drop `sqit_it_*`):

```bash
docker run -d --name sqit-mysql-test -e MYSQL_ROOT_PASSWORD=sqit -p 3307:3306 mysql:8.0
SQIT_IT_DSN='root:sqit@tcp(127.0.0.1:3307)/' go test ./...
```

## Layout

```text
cmd/sqit            CLI entry point
internal/cli        commands and output
internal/core       workflows: commit, fetch, status, pull
internal/mysql      information_schema reader, row reader, apply + verify
internal/snapshot   in-memory / repository-backed states
internal/diff       record + field level diff
internal/merge      three-way merge and conflict model
internal/apply      INSERT/UPDATE/DELETE plan
internal/repo       .sqit layout, refs, object store, commit graph
internal/object     record / manifest / snapshot / commit objects, hashing
internal/schema     schema model, diff, compatibility
internal/canonical  canonical JSON, values, primary-key keys
```
