# sqit – design

`sqit` is a Git-like version control and synchronisation CLI for **MySQL data**.
It versions the concrete, configured database state as content-addressed
objects, not SQL dumps and not migration files.

This document fixes the storage model and the semantics of the core
operations. Where it deviates from the original brief it says why.

---

## 1. Mental model

```text
                    TEST MySQL DB
                          │
                        fetch            (sqit fetch / sqit pull)
                          ▼
                 refs/remotes/test  ─►  T3 → T2 → T1      (upstream history)
                          │
                          │  3-way merge (BASE = merge-base(HEAD, upstream))
                          ▼
   HEAD ─► refs/heads/main ─► C3 → C2 → C1              (local history)
                          │
                    diff  │
                          ▼
                    LOCAL MySQL DB  =  WORKING TREE
```

### Concepts

| Concept | Definition | Where it lives |
|---|---|---|
| **WORKING TREE** | The *current* contents of the configured tables in the LOCAL MySQL database. It is never stored; every `status` / `diff` / `commit` / `pull` reads it fresh from the DB inside a `REPEATABLE READ` transaction, so multi-table reads are consistent. No triggers, binlog, daemon or monitoring. | LOCAL DB |
| **HEAD** | The commit the local branch currently points at (`.sqit/HEAD` → `refs/heads/main` → commit hash). Its snapshot is the baseline against which working-tree changes are measured. Before the first commit HEAD is *unborn* and behaves like an empty snapshot. | `.sqit/HEAD`, `.sqit/refs/heads/main` |
| **UPSTREAM** | The most recently fetched, immutable snapshot of the TEST database, stored as a commit of kind `upstream`. `refs/remotes/test` always points at the latest one. Each fetch that observes a changed TEST state appends a new upstream commit (`T1 → T2 → T3`); an unchanged TEST state produces no new commit. TEST itself is *not* a repository. | `.sqit/refs/remotes/test` |
| **BASE** | The merge base: the nearest common ancestor of HEAD and UPSTREAM in the commit graph. If there is no common ancestor (the local branch was started with `sqit commit` before the first `sqit pull`) the **first local commit** is used as BASE – it is taken to be the state at which LOCAL mirrored TEST. With an unborn HEAD the base is the empty snapshot. | computed |
| **SNAPSHOT** | The full state of all configured tables at one instant, expressed as a tree of hashes: snapshot → table manifests → record hashes. A snapshot object is tiny; it only lists manifest hashes. | `objects/snapshots/` |
| **RECORD OBJECT** | One row in canonical JSON form, addressed by the SHA-256 of that form. Identical rows (same table, PK and data) are stored exactly once no matter how many snapshots reference them. | `objects/records/` |
| **COMMIT** | An immutable node in the history: parent hashes, snapshot hash, schema hash, kind (`local` / `upstream` / `merge`), message, timestamp, metadata. | `objects/commits/` |
| **MERGE** | The three-way, field-level reconciliation of BASE, LOCAL and UPSTREAM that `pull` performs. It yields a merged snapshot, a list of structured conflicts and a plan of INSERT/UPDATE/DELETE operations. The merge engine works purely on snapshots and knows nothing about MySQL. | `internal/merge` |

---

## 2. Repository layout (`.sqit/`)

```text
.sqit/
├── config.json                 connection references + table list (no secrets)
├── HEAD                        "ref: refs/heads/main"
├── refs/
│   ├── heads/main              hash of the current local commit
│   └── remotes/test            hash of the latest upstream commit
└── objects/
    ├── records/<aa>/<hash>.json
    ├── manifests/<aa>/<hash>.json
    ├── snapshots/<aa>/<hash>.json
    ├── schemas/<aa>/<hash>.json
    └── commits/<aa>/<hash>.json
```

* Every object file is the canonical JSON encoding of the object followed by a
  single `\n`. The object's name is `sha256(file bytes)` in lowercase hex.
  Objects are therefore verifiable by re-hashing the file.
* Object directories are sharded by the first two hex characters so no
  directory grows unbounded (Git-hosting UIs and file systems stay happy).
* Objects are immutable and written atomically (temp file + rename). An
  object that already exists is never rewritten – this is the deduplication.
* Ref files contain one hash and a trailing newline; `HEAD` contains a
  symbolic ref. These are the only files that change on normal use, which
  keeps Git diffs of `.sqit/` small and readable.
* `.sqit/` contains **no secrets**: connection config holds host/port/user/
  database names only; the password comes from an environment variable named
  in the config (default `SQIT_LOCAL_PASSWORD` / `SQIT_TEST_PASSWORD`), or an
  entire DSN can be supplied through `SQIT_LOCAL_DSN` / `SQIT_TEST_DSN`.
* `sqit init` also writes `sqit.env` next to `.sqit/` (and lists it in
  `.gitignore`): a KEY=VALUE file for the passwords / DSNs that every command
  loads before reading the config; real environment variables win over it.
* `"localOnly": true` (or `SQIT_LOCAL_ONLY=1`) makes every TEST/upstream
  connection attempt fail unless the DSN targets localhost. It is a guard for
  local development against the Docker MySQL in `docker-compose.yml`; it
  changes nothing about the upstream model.
* The whole directory is intended to be committed into an ordinary Git
  repository next to the application code. `sqit` keeps its own history
  inside the objects; Git merely versions the files.

`config.json`:

```json
{
  "version": 1,
  "local": { "host": "127.0.0.1", "port": 3306, "user": "root", "database": "app_local", "passwordEnv": "SQIT_LOCAL_PASSWORD", "dsnEnv": "SQIT_LOCAL_DSN" },
  "test":  { "host": "test-db",   "port": 3306, "user": "app",  "database": "app_test",  "passwordEnv": "SQIT_TEST_PASSWORD",  "dsnEnv": "SQIT_TEST_DSN" },
  "tables": ["users", "orders"]
}
```

Only the listed tables are tracked. Every tracked table must have a primary
key (composite keys are supported).

---

## 3. Object model

### 3.1 Canonical JSON

All objects are hashed over a canonical JSON encoding (`internal/canonical`),
modelled on RFC 8785 (JCS):

* object keys sorted by UTF-16 code units (byte order for the ASCII keys we
  use), no whitespace;
* strings escaped minimally (`"`, `\`, control chars; `\n \r \t \b \f` short
  forms, others `\u00xx`); no HTML escaping;
* numbers emitted as pre-formatted text (see 3.2), never re-parsed as
  `float64` where that could lose information (64-bit integers).

### 3.2 Canonical column values

A column value is stored as a **canonical JSON value** (`canonical.Value`,
the raw JSON text). The mapping from the MySQL text-protocol representation is
decided by the column's `DATA_TYPE` (from `information_schema`):

| MySQL type | Canonical form | Notes |
|---|---|---|
| `NULL` (any type) | `null` | |
| `TINYINT … BIGINT` (signed/unsigned) | JSON number, e.g. `42`, `-7` | kept as digit string; no float64 round trip, so `BIGINT UNSIGNED` is exact. `BOOLEAN` is `TINYINT(1)` → `0`/`1`. |
| `BIT(n)` | JSON number (unsigned) | big-endian bytes → integer |
| `FLOAT` | JSON number | parsed as float32, re-emitted as the *shortest round-trip* decimal in ES6 `Number.toString` layout |
| `DOUBLE` | JSON number | same, float64 |
| `DECIMAL` | JSON string `"12.50"` | exact text as the server returns it (scale is fixed by the column definition); `-0.00` normalised to `0.00` |
| `DATE DATETIME TIMESTAMP TIME YEAR` | JSON string, MySQL text (`"2024-01-15 10:30:00.123"`, `"2024"`) | session `time_zone = '+00:00'` so `TIMESTAMP` is always UTC regardless of server or client zone |
| `CHAR VARCHAR *TEXT ENUM SET` | JSON string | connection charset `utf8mb4`; the server converts other column charsets |
| `BINARY VARBINARY *BLOB GEOMETRY`, and any text type with `CHARACTER SET binary` | `{"$b64":"<base64>"}` | tagged object so binary is self-describing and unambiguous |
| `JSON` | JSON string containing the *canonical* JSON text | parsed and re-serialised: keys sorted, numbers normalised. MySQL's own key ordering therefore never leaks into hashes |

Rationale for "value = canonical JSON text": equality is byte equality,
records are readable as-is in `.sqit/objects`, and the merge engine needs no
type knowledge. The schema (always stored with every commit) is what maps a
value back to a MySQL parameter on `apply`.

### 3.3 Primary key canonicalisation

The manifest key of a record is the canonical JSON **array** of its primary
key values in primary-key column order, e.g. `[42]`, `[10,55]`, `["ab,c"]`.
This is unambiguous and collision safe for composite keys and for keys that
contain separators. For display the key is rendered as `users/42` or
`orders/customer_id=10,order_id=55`.

### 3.4 Objects

**record** – `objects/records/`
```json
{"data":{"email":"peter@example.com","id":42,"name":"Peter"},"pk":{"id":42},"table":"users"}
```
`data` holds every column (including the key columns); `pk` is repeated for
self-description. Changing one column produces one new record object; the
old one stays referenced by older snapshots.

**manifest** – `objects/manifests/` (one per table per snapshot)
```json
{"records":{"[42]":"<record hash>","[43]":"<record hash>"},"table":"users"}
```

**snapshot** – `objects/snapshots/`
```json
{"tables":{"orders":"<manifest hash>","users":"<manifest hash>"}}
```
An unchanged table re-uses the previous manifest hash; an unchanged row
re-uses the previous record hash. Repository growth is proportional to the
rows that actually changed (plus one manifest per changed table per commit).

**schema** – `objects/schemas/`
```json
{"tables":[{"name":"users","primaryKey":["id"],
  "columns":[{"name":"id","position":1,"columnType":"int","dataType":"int",
              "nullable":false,"default":null,"extra":"auto_increment",
              "charset":null,"collation":null}, …]}]}
```
Tables sorted by name, columns by ordinal position. Read from
`information_schema.COLUMNS` and `KEY_COLUMN_USAGE`.

**commit** – `objects/commits/`
```json
{"kind":"local","message":"Update users","metadata":{},
 "parents":["<hash>"],"schema":"<schema hash>","snapshot":"<snapshot hash>",
 "timestamp":"2026-09-19T20:15:04Z"}
```
`kind` is `local`, `merge` (two parents: `[HEAD, upstream]`) or `upstream`
(metadata `{"remote":"test"}`). Commit hashes are not reproducible across
runs because of the timestamp – exactly like Git.

---

## 4. Operations

### 4.1 `status` and `diff`

* `sqit diff` – **WORKING TREE vs HEAD**, field level. Sections *added*,
  *modified*, *deleted*; a modified record shows changed columns with `-`/`+`
  and unchanged ones as context.
* `sqit diff --upstream` – **UPSTREAM vs HEAD**: what changed in TEST relative
  to where the local branch stands. Uses the last fetched upstream commit;
  `--fetch` refreshes it first.
* `sqit diff --schema` – schema of WORKING TREE vs HEAD (added/deleted table,
  added/deleted column, type, nullability, default, PK changes).
  `--schema --upstream` compares UPSTREAM with HEAD.
* `sqit status` – working-tree counts, schema-changed flag, HEAD/upstream
  hashes, upstream change counts and the relationship state:
  `UP TO DATE`, `UPSTREAM AHEAD`, `LOCAL AHEAD`, `DIVERGED`, `UNRELATED`,
  `NO UPSTREAM`.

`status` and `diff` are offline with respect to TEST unless `--fetch` is
given; hitting the TEST server on every status would be slow and surprising.

### 4.2 `commit`

1. Read schema + snapshot of LOCAL in one transaction.
2. Write only the record/manifest/snapshot/schema objects that do not exist
   yet.
3. Write a commit with parent = HEAD (none when HEAD is unborn).
4. Move `refs/heads/main`.

Refuses with "nothing to commit" when the snapshot and schema hashes equal
HEAD's (override with `--allow-empty`).

### 4.3 `fetch`

Reads schema + snapshot of TEST; if the snapshot *or* schema hash differs
from the current `refs/remotes/test` commit a new `upstream` commit is
created with the previous upstream commit as parent, and the ref is moved.
Otherwise nothing changes.

### 4.4 `pull` (and `pull --dry-run`)

```text
 1. fetch                      → UPSTREAM commit U
 2. open LOCAL transaction     → WORKING snapshot W, LOCAL schema
 3. schema compatibility check  LOCAL vs UPSTREAM  (abort, no ALTER TABLE)
 4. BASE = merge-base(HEAD, U); empty snapshot if none
 5. Mw = merge3(BASE, W,    U.snapshot)   → conflicts?  abort
    Mc = merge3(BASE, HEAD, U.snapshot)   → conflicts?  abort   (only when not fast-forward)
 6. plan = diff(W → Mw) as INSERT / UPDATE / DELETE
 7. --dry-run: print plan + conflicts, rollback, stop
 8. apply plan inside the transaction (FOREIGN_KEY_CHECKS=0, every statement
    must affect exactly one row), re-read the touched tables inside the same
    transaction and verify they hash to Mw; otherwise rollback
 9. write objects; COMMIT the DB transaction
10. fast-forward: refs/heads/main := U
    otherwise:    new merge commit (parents [HEAD, U], snapshot Mc) → main
```

Decisions:

* **The merge's LOCAL side is the working tree** (`Mw`), because that is the
  state the DB will be moved to. Uncommitted local changes participate in
  conflict detection, as the brief requires (`Peter` / `Peter Smith` / `Péter`
  → conflict; `name` local + `email` upstream → auto-merge).
* **Uncommitted changes stay uncommitted.** The merge commit's snapshot is
  `Mc` (committed HEAD merged with upstream), while the DB receives `Mw`.
  After a pull `sqit status` still shows exactly the changes that were
  uncommitted before – like Git, `pull` never silently commits your work.
  In the rare case where committed changes conflict with upstream but the
  working tree does not, pull aborts and suggests committing first.
* **Fast-forward** when HEAD is an ancestor of U (or unborn): the ref is
  simply moved; the DB still receives `Mw` so dirty working trees are
  handled uniformly.
* **Already up to date** when U is HEAD or an ancestor of HEAD: nothing is
  touched.
* **Atomicity**: all DB writes happen in one InnoDB transaction that also
  produced the working-tree snapshot, so a concurrent change to a touched row
  is caught by the "exactly one row affected" check or the final verification
  and rolls everything back. Objects are written before the DB commit
  (orphaned objects are harmless); refs are moved after it.
* **Schema**: pull requires the two schemas to be *compatible* for every
  tracked table: same columns, same `DATA_TYPE`, same nullability, same
  primary key. Column display width, defaults, charset, collation and
  `EXTRA` may differ (different server versions render these differently and
  they do not affect the data representation). Incompatible schemas abort the
  pull with the schema diff printed. `sqit` never emits `ALTER TABLE`.

### 4.5 Three-way merge rules (`internal/merge`)

Per table, per primary key, with `b`/`l`/`u` the BASE/LOCAL/UPSTREAM record
hashes (`nil` = absent):

| case | result |
|---|---|
| `l == u` | `l` (both sides agree, incl. both absent or both deleted) |
| `l == b` | `u` (only upstream changed – includes upstream delete) |
| `u == b` | `l` (only local changed – includes local delete) |
| `b` exists, `l` nil, `u != b` | **conflict** `delete/modify` |
| `b` exists, `u` nil, `l != b` | **conflict** `modify/delete` |
| otherwise (all three differ, or `b` nil and `l != u`) | field-level merge |

Field-level merge, per column with values `bv`/`lv`/`uv` (`bv` absent when
the base record is absent): `lv == uv` → take it; `lv == bv` → `uv`;
`uv == bv` → `lv`; else **conflict** `modify/modify` (`add/add` when there is
no base record).

Conflict model (JSON-serialisable, ready for a merge UI):

```json
{"table":"users","primaryKey":{"id":42},"column":"name","kind":"modify/modify",
 "base":"Peter","local":"Peter Smith","upstream":"Péter"}
```

For record-level conflicts (`delete/modify`, `modify/delete`) `column` is
empty and `base`/`local`/`upstream` hold the whole record or `null`.

### 4.6 Merge base

Ancestors of HEAD are collected into a set; the upstream chain is walked
breadth-first from `refs/remotes/test` and the first commit found in the set
is BASE. Because upstream commits form a single chain and local history only
ever *merges* upstream (there is no push), this is the exact lowest common
ancestor for every graph `sqit` can produce.

**Unrelated histories.** If no common ancestor exists, the root of the local
first-parent chain is used as BASE and the pull always produces a merge
commit (never a fast-forward), so the local history is preserved. This is
what makes the brief's acceptance scenario work: `sqit init`, `sqit commit`
("Initial data"), local edit, TEST edit, `sqit pull` sees the initial commit
as BASE. The recommended alternative is `sqit init` followed directly by
`sqit pull`, which fast-forwards to the first upstream snapshot and gives
the two histories a real common ancestor.

**Relation after a merge.** Right after a merge commit `sqit status` reports
`LOCAL AHEAD` (HEAD contains upstream plus local commits) – the same wording
Git uses; `sqit pull` then answers "Already up to date".

---

## 5. Layering

```text
MySQL (internal/mysql)     information_schema reader, row reader, apply/verify in tx
        ↓
Snapshot (internal/snapshot, internal/object, internal/canonical)
        ↓
Diff / Merge / Plan (internal/diff, internal/merge, internal/apply)   – no SQL here
        ↓
Repository (internal/repo)   object store, refs, commit graph
        ↓
CLI (internal/cli, cmd/sqit)
```

The row reader produces `snapshot.Table` values (`map[pkKey]*object.Record`)
through a small interface, so the diff/merge/plan layers are testable without
a database.

---

## 6. Deviations from the brief, and why

* **`sqit fetch` exists.** The brief lists only `pull`; fetching is the first
  step of pull and is needed by `status --fetch` / `diff --upstream --fetch`,
  so it is exposed as its own tiny command.
* **`status` does not contact TEST by default.** See 4.1.
* **Uncommitted changes are not folded into the merge commit** (4.4). The
  brief's step 8 ("create merge/local commit after apply") is satisfied by the
  merge commit; keeping the working tree's uncommitted changes uncommitted
  is the Git-consistent reading and avoids surprising history.
* **Values are typed by the schema, not self-tagged** (except binary).
  Tagging every value would double the object size and buy nothing, since a
  commit always carries its schema.
* **Commit hashes include a timestamp**, so two identical commits made at
  different times differ – as in Git. Snapshot, manifest, record and schema
  hashes *are* fully reproducible.
* **`FOREIGN_KEY_CHECKS=0` during apply.** The target state comes from a
  consistent TEST snapshot; enforcing FK order across arbitrary table sets
  would need a topological sort for no practical gain in an MVP.

---

## 7. Not in the MVP

push, authentication, remote protocol, daemons/triggers/binlog, automatic
`ALTER TABLE`, migrations, garbage collection of unreferenced objects (the
object layout makes a mark-and-sweep straightforward later), PostgreSQL,
VS Code extension.
