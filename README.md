# sqit

Git-like version control and synchronisation for **MySQL data**.

`sqit` treats your LOCAL MySQL database as a working tree, records
content-addressed snapshots of the configured tables as commits, tracks the
TEST database as an upstream, and merges TEST changes into LOCAL with a
three-way, field-level merge – without SQL dumps, triggers, binlog or daemons.

```bash
sqit init --local-db app_local --test-db app_test --test-host test-db
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
* every tracked table must have a primary key (composite keys are fine);
  tables without one are skipped with a warning
* the LOCAL user needs `SELECT/INSERT/UPDATE/DELETE` on the tracked tables;
  the TEST user needs `SELECT`; both need read access to `information_schema`

## Build

```bash
go build -o sqit ./cmd/sqit
```

## Setup

```bash
sqit init
```

asks for the LOCAL and the UPSTREAM/TEST connection (host, port, user,
database, password), writes the config, stores the passwords in the
git-ignored `sqit.env` and tries both connections. Everything can also be
given as flags (then nothing is asked; `--no-input` never prompts, e.g. in
scripts):

```bash
sqit init \
  --local-host 127.0.0.1 --local-port 3306 --local-user root --local-db app_local \
  --test-host  test-db   --test-port  3306 --test-user  app  --test-db  app_test
```

By default **every table** of the database (with a primary key) is
versioned. Exclude tables in the generated **`.sqitignore`** (committed, one
glob pattern per line – `sessions`, `tmp_*`, `*_log`) or personally via
`SQIT_IGNORE_TABLES=a,b,tmp_*` in `sqit.env`. `--tables users,orders` (or
`"tables"` in `config.json`) switches to an explicit allow-list instead.

This writes `.sqit/config.json` (no secrets) and a **`sqit.env`** file next
to it (added to `.gitignore`) where you put the connection secrets:

```ini
SQIT_LOCAL_PASSWORD=...
SQIT_TEST_PASSWORD=...
# or full DSNs (go-sql-driver format), which override host/port/user/db:
#SQIT_LOCAL_DSN=root:secret@tcp(127.0.0.1:3306)/app_local
#SQIT_TEST_DSN=app:secret@tcp(test-db:3306)/app_test
#SQIT_LOCAL_ONLY=1
```

Every `sqit` command loads `sqit.env` automatically; variables that are
already set in the environment take precedence, so CI or a shell profile can
override the file.

Commit `.sqit/` into your Git repository together with the application code;
everything in it is deterministic JSON. Never commit `sqit.env`.

## Commands

| command | meaning |
|---|---|
| `sqit init [--tables a,b] [--local-only]` | create `.sqit/`, `sqit.env`, `.sqitignore`; `--tables` = explicit allow-list instead of "all tables minus `.sqitignore`"; `--local-only` restricts upstream connections to localhost |
| `sqit status [--fetch] [--json]` | working-tree changes vs HEAD, upstream changes vs HEAD, relation (`CLEAN`, `LOCAL MODIFIED`, `UPSTREAM AHEAD`, `LOCAL AHEAD`, `DIVERGED`, `UNRELATED`, `NO UPSTREAM`) |
| `sqit diff [--json]` | WORKING TREE vs HEAD, field level |
| `sqit diff --upstream [--fetch]` | UPSTREAM vs HEAD |
| `sqit diff --schema [--upstream]` | schema differences |
| `sqit commit -m "..."` | snapshot LOCAL into a new commit |
| `sqit fetch` | snapshot TEST as a new upstream commit (only if it changed) |
| `sqit pull [--dry-run] [--create-missing-tables] [--json]` | fetch, three-way merge, apply to LOCAL, record a merge commit or fast-forward; `--create-missing-tables` creates upstream-only tables on LOCAL first |
| `sqit table <name> [--upstream] [--json]` | all rows of a table with their status (added / modified / deleted, old -> new per cell) – the data source of the VS Code extension |
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
  key) abort the pull; `sqit` never runs `ALTER TABLE`. The one exception is
  a table that exists **only upstream**: its `CREATE TABLE` (from `SHOW CREATE
  TABLE` on TEST) is printed, and `sqit pull --create-missing-tables` creates
  it on LOCAL before merging (the LOCAL user needs the `CREATE` privilege).
  Alternatively list the table in `.sqitignore`.

## Local development / testing

You do not need the real TEST database to try `sqit`: `docker-compose.yml`
starts one MySQL 8 server with two databases that play the two roles.

| role | database | user | privileges |
|---|---|---|---|
| LOCAL (working tree) | `sqit_local` | `sqit_local_user` | SELECT, INSERT, UPDATE, DELETE |
| TEST / UPSTREAM | `sqit_upstream` | `sqit_upstream_user` | **SELECT only** |

The upstream user deliberately has no write privilege, so the local setup
also proves that `sqit` never modifies the upstream. Both databases are
seeded from `testdata/` with the same small, readable data set (`users`,
`orders`; composite key, nullable columns, DECIMAL):

```text
users:  1 Anna | 2 John | 3 Maria (email NULL, inactive) | 42 Peter old@example.com
orders: (1,1) 10.00 'first order' | (1,2) 25.50 | (42,1) 99.99 'gift'
```

Nothing about the upstream concept changes: later you only point
`SQIT_TEST_DSN` (or `.sqit/config.json`'s `test` block) at the real TEST
database instead of the Docker one.

> Already have a MySQL server (e.g. a Docker container) you want to reuse
> instead of the compose one? `scripts/setup-existing-mysql.ps1 -Container
> <name> -RootPassword <pw>` seeds the same two databases into it (MySQL
> 5.5+ compatible, users `sqit_local` / `sqit_upstream`). A complete
> step-by-step walkthrough in Hungarian is in
> [docs/LOKALIS_TESZT.md](docs/LOKALIS_TESZT.md).

### 1. Start the local MySQL

```bash
cp .env.example .env        # adjust SQIT_MYSQL_PORT if 3306 is taken
docker compose up -d
```

`testdata/*.sql` run on the first start. Reset everything to the seed state
with:

```bash
docker compose down -v && docker compose up -d
```

(`.env` is git-ignored; it only holds the throwaway Docker credentials.)

### 2. Configure LOCAL and UPSTREAM

Load the variables from `.env` – bash:

```bash
set -a; source .env; set +a
```

PowerShell:

```powershell
Get-Content .env | Where-Object { $_ -match '^[A-Z_]+=' } | ForEach-Object { $k,$v = $_ -split '=',2; Set-Item "env:$k" $v.Trim("'") }
```

`SQIT_LOCAL_DSN` configures LOCAL, `SQIT_TEST_DSN` configures UPSTREAM
(both override whatever host/port/user/database is in `config.json`).

**Local-only mode.** `sqit init --local-only` writes `"localOnly": true`
into `.sqit/config.json` (the environment variable `SQIT_LOCAL_ONLY=1` has
the same effect). In this mode every upstream connection attempt is checked
*before* connecting and refused unless it targets `localhost`, a loopback IP
or a unix socket – a stray DSN can never reach the real TEST database while
you test. Remove the flag when you switch to the real TEST.

### 3. Run the workflow

```bash
go build -o sqit ./cmd/sqit
mkdir demo && cd demo
../sqit init --tables users,orders --local-only
../sqit commit -m "Initial state"
```

Make a local change and look at it:

```bash
docker exec sqit-mysql mysql -uroot -psqit-root -e "UPDATE sqit_local.users SET name='Peter Smith' WHERE id=42"
../sqit status
../sqit diff            # users/42: name Peter -> Peter Smith
```

Change the upstream (only ever through the root account or a client of your
own – `sqit` cannot), fetch and compare:

```bash
docker exec sqit-mysql mysql -uroot -psqit-root -e "UPDATE sqit_upstream.users SET email='new@example.com' WHERE id=42"
../sqit fetch
../sqit diff --upstream # users/42: email old@example.com -> new@example.com
```

Merge (non-conflicting: `name` from LOCAL, `email` from UPSTREAM):

```bash
../sqit pull --dry-run
../sqit pull
docker exec sqit-mysql mysql -uroot -psqit-root -e "SELECT * FROM sqit_local.users WHERE id=42"
# 42 | Peter Smith | new@example.com | 1
```

Conflict (both sides change `name`): `pull --dry-run` lists it with BASE /
LOCAL / UPSTREAM values, `pull` exits 1 and leaves `sqit_local`, HEAD and the
refs untouched – even if other, clean upstream changes were pending:

```bash
docker exec sqit-mysql mysql -uroot -psqit-root -e "UPDATE sqit_upstream.users SET name='Péter' WHERE id=42"
../sqit pull --dry-run
../sqit pull; echo "exit=$?"
```

Resolve by setting the value you want in `sqit_local`, `commit`, then `pull`
again. Inserts and deletes work the same way; a row modified locally and
deleted upstream (or vice versa) is a conflict, a row left untouched locally
and deleted upstream is simply deleted by `pull`.

### 4. Run the integration tests

With `.env` loaded (see step 2), the whole suite runs against the Docker
MySQL:

```bash
go test ./...
```

* `sqit init` without `--tables` tracks all seeded tables; `.sqitignore` /
  `SQIT_IGNORE_TABLES` exclude some (covered by `TestTrackAllTablesWithIgnore`).
* `SQIT_IT_DSN` (root) drives the general integration tests in
  `internal/core/integration_test.go`; they create and drop throwaway
  `sqit_it_*` databases and check results directly with SQL.
* `SQIT_IT_DSN` + `SQIT_LOCAL_DSN` + `SQIT_TEST_DSN` together enable
  `TestLocalEnvironmentWorkflow` (`internal/core/localenv_test.go`), which
  reseeds `sqit_local` / `sqit_upstream` from `testdata/` and replays the
  exact workflow above through the CLI: it asserts that the upstream user
  cannot write, that local-only mode refuses a non-localhost DSN, and that
  after each `pull` the `sqit_local` tables contain the merge result.

Without these variables the integration tests are skipped and only the unit
tests run.

## Tests

Unit tests need nothing:

```bash
go test ./...
```

Integration tests run against a real MySQL server when `SQIT_IT_DSN` points
at an account that may create databases (they create and drop `sqit_it_*`).
The easiest is the Docker environment above; any other server works too:

```bash
SQIT_IT_DSN='root:secret@tcp(127.0.0.1:3306)/' go test ./...
```

## VS Code extension

[vscode-sqit/](vscode-sqit/) contains a VS Code extension: an *SQit* activity
bar with the tracked tables and their changes (LOCAL vs HEAD and UPSTREAM vs
HEAD), a table grid per table with GitHub-style red/green highlighting of
deleted / added rows and old -> new cell values, and commands for commit,
fetch, pull (dry run) and pull. It drives the `sqit` CLI (`status --json`,
`table --json`), so it needs `sqit` on the `PATH` or the `sqit.executable`
setting. Build and install:

```bash
cd vscode-sqit && npm install && npm run compile && npm run package
code --install-extension sqit-0.1.0.vsix
```

## Layout

```text
cmd/sqit            CLI entry point
internal/cli        commands and output
internal/core       workflows: commit, fetch, status, pull, table view
internal/mysql      information_schema reader, row reader, apply + verify
internal/snapshot   in-memory / repository-backed states
internal/diff       record + field level diff
internal/merge      three-way merge and conflict model
internal/apply      INSERT/UPDATE/DELETE plan
internal/repo       .sqit layout, refs, object store, commit graph
internal/object     record / manifest / snapshot / commit objects, hashing
internal/schema     schema model, diff, compatibility
internal/canonical  canonical JSON, values, primary-key keys
vscode-sqit/        VS Code extension (TypeScript)
```
