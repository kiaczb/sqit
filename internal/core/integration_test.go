package core_test

// Integration tests against a real MySQL server.
//
// Set SQIT_IT_DSN to a go-sql-driver DSN with privileges to create
// databases, e.g.
//
//	SQIT_IT_DSN="root:secret@tcp(127.0.0.1:3307)/"
//
// The tests create and drop databases named sqit_it_*.

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"sqit/internal/canonical"
	"sqit/internal/cli"
	"sqit/internal/core"
	"sqit/internal/merge"
	"sqit/internal/object"
	"sqit/internal/repo"
)

var dbCounter int64

type fixture struct {
	t       *testing.T
	root    *sql.DB
	local   *sql.DB
	test    *sql.DB
	localDB string
	testDB  string
	dir     string
	ws      *core.Workspace
}

func newFixture(t *testing.T, tables []string, ddl ...string) *fixture {
	t.Helper()
	dsn := os.Getenv("SQIT_IT_DSN")
	if dsn == "" {
		t.Skip("SQIT_IT_DSN not set; skipping MySQL integration test")
	}
	root, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Ping(); err != nil {
		t.Fatalf("cannot reach MySQL at SQIT_IT_DSN: %v", err)
	}
	n := atomic.AddInt64(&dbCounter, 1)
	suffix := fmt.Sprintf("%d_%d", time.Now().UnixNano()%1_000_000, n)
	f := &fixture{t: t, root: root, localDB: "sqit_it_local_" + suffix, testDB: "sqit_it_test_" + suffix}
	for _, name := range []string{f.localDB, f.testDB} {
		if _, err := root.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		root.Exec("DROP DATABASE IF EXISTS `" + f.localDB + "`")
		root.Exec("DROP DATABASE IF EXISTS `" + f.testDB + "`")
		root.Close()
	})
	f.local = f.openDB(dsn, f.localDB)
	f.test = f.openDB(dsn, f.testDB)
	for _, db := range []*sql.DB{f.local, f.test} {
		for _, stmt := range ddl {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("ddl %q: %v", stmt, err)
			}
		}
	}

	// Repository with DSNs coming from per-test environment variables.
	f.dir = t.TempDir()
	cfg := repo.DefaultConfig()
	cfg.Tables = tables
	cfg.Local.DSNEnv = "SQIT_IT_LOCAL_" + suffix
	cfg.Test.DSNEnv = "SQIT_IT_TEST_" + suffix
	cfg.Local.PasswordEnv, cfg.Test.PasswordEnv = "", ""
	t.Setenv(cfg.Local.DSNEnv, withDB(dsn, f.localDB))
	t.Setenv(cfg.Test.DSNEnv, withDB(dsn, f.testDB))
	if _, err := repo.Init(f.dir, cfg); err != nil {
		t.Fatal(err)
	}
	ws, err := core.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws
	return f
}

func (f *fixture) openDB(dsn, name string) *sql.DB {
	db, err := sql.Open("mysql", withDB(dsn, name)+"?parseTime=false&charset=utf8mb4")
	if err != nil {
		f.t.Fatal(err)
	}
	db.Exec("SET time_zone = '+00:00'")
	f.t.Cleanup(func() { db.Close() })
	return db
}

// withDB replaces the database name of a DSN of the form user:pw@tcp(h:p)/[db][?params].
func withDB(dsn, name string) string {
	base := dsn
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	i := strings.LastIndex(base, "/")
	return base[:i+1] + name
}

// supports reports whether the server is at least the given major.minor.
func (f *fixture) supports(major, minor int) bool {
	f.t.Helper()
	var v string
	if err := f.root.QueryRow("SELECT VERSION()").Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	var ma, mi int
	fmt.Sscanf(v, "%d.%d", &ma, &mi)
	return ma > major || (ma == major && mi >= minor)
}

func (f *fixture) exec(db *sql.DB, query string, args ...any) {
	f.t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

func (f *fixture) queryString(db *sql.DB, query string, args ...any) string {
	f.t.Helper()
	var s sql.NullString
	if err := db.QueryRow(query, args...).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	if !s.Valid {
		return "NULL"
	}
	return s.String
}

func (f *fixture) count(db *sql.DB, table string) int {
	f.t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM `" + table + "`").Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// run executes a CLI command in the fixture directory and returns stdout.
func (f *fixture) run(args ...string) (string, int) {
	f.t.Helper()
	wd, _ := os.Getwd()
	if err := os.Chdir(f.dir); err != nil {
		f.t.Fatal(err)
	}
	defer os.Chdir(wd)
	var out bytes.Buffer
	stdout, stderr := os.Stdout, os.Stderr
	r, w, _ := os.Pipe()
	os.Stdout, os.Stderr = w, w
	done := make(chan struct{})
	go func() { out.ReadFrom(r); close(done) }()
	code := cli.Main(args)
	w.Close()
	os.Stdout, os.Stderr = stdout, stderr
	<-done
	return out.String(), code
}

const usersDDL = `CREATE TABLE users (
	id INT NOT NULL PRIMARY KEY,
	name VARCHAR(100) NOT NULL,
	email VARCHAR(100) NULL
) ENGINE=InnoDB`

func seedUsers(f *fixture) {
	for _, db := range []*sql.DB{f.local, f.test} {
		f.exec(db, "INSERT INTO users (id, name, email) VALUES (42, 'Peter', 'old@example.com'), (12, 'Old', 'o@x'), (50, 'Same', 's@x')")
	}
}

// ---------------------------------------------------------------------------

func TestAcceptanceConflict(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()

	// sqit commit -m "Initial data"
	h1, _, err := f.ws.Commit(ctx, "Initial data", false)
	if err != nil {
		t.Fatal(err)
	}
	// LOCAL modification
	f.exec(f.local, "UPDATE users SET name = 'Peter Smith' WHERE id = 42")

	st, err := f.ws.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	a, m, d := st.Working.Counts()
	if a != 0 || m != 1 || d != 0 || !st.Dirty() {
		t.Fatalf("status counts %d %d %d", a, m, d)
	}
	ch := st.Working.Changes[0]
	if ch.ID() != "users/42" || len(ch.Fields) != 1 || ch.Fields[0].Old != canonical.String("Peter") || ch.Fields[0].New != canonical.String("Peter Smith") {
		t.Fatalf("diff %+v", ch)
	}
	out, code := f.run("diff")
	if code != 0 || !strings.Contains(out, "-  name: Peter\n+  name: Peter Smith\n") {
		t.Fatalf("cli diff (%d):\n%s", code, out)
	}

	// TEST modification -> conflict
	f.exec(f.test, "UPDATE users SET name = 'Péter' WHERE id = 42")
	rep, err := f.ws.Pull(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", rep)
	}
	c := rep.Conflicts[0]
	if c.Base != canonical.String("Peter") || c.Local != canonical.String("Peter Smith") || c.Upstream != canonical.String("Péter") || c.Column != "name" {
		t.Fatalf("conflict %+v", c)
	}
	// Dry run must not touch anything.
	if up, _ := f.ws.Repo.Upstream(); up != "" {
		t.Fatal("dry run recorded an upstream commit")
	}
	if head, _ := f.ws.Repo.Head(); head != h1 {
		t.Fatal("dry run moved HEAD")
	}
	if got := f.queryString(f.local, "SELECT name FROM users WHERE id = 42"); got != "Peter Smith" {
		t.Fatalf("dry run modified LOCAL: %s", got)
	}

	// Real pull also refuses and leaves LOCAL alone.
	out, code = f.run("pull")
	if code != 1 || !strings.Contains(out, "CONFLICTS: 1") || !strings.Contains(out, "UPSTREAM: Péter") {
		t.Fatalf("cli pull (%d):\n%s", code, out)
	}
	if got := f.queryString(f.local, "SELECT name FROM users WHERE id = 42"); got != "Peter Smith" {
		t.Fatalf("pull with conflict modified LOCAL: %s", got)
	}
	if head, _ := f.ws.Repo.Head(); head != h1 {
		t.Fatal("conflicting pull moved HEAD")
	}
	// ...but the fetch part did record the upstream snapshot.
	if up, _ := f.ws.Repo.Upstream(); up == "" {
		t.Fatal("pull did not record upstream")
	}
}

func TestAcceptanceFieldLevelAutoMerge(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()

	if _, _, err := f.ws.Commit(ctx, "Initial data", false); err != nil {
		t.Fatal(err)
	}
	f.exec(f.local, "UPDATE users SET name = 'Peter Smith' WHERE id = 42")
	f.exec(f.test, "UPDATE users SET email = 'new@example.com' WHERE id = 42")

	rep, err := f.ws.Pull(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() {
		t.Fatalf("unexpected block: %+v", rep.Conflicts)
	}
	ins, upd, del := rep.Plan.Counts()
	if ins != 0 || upd != 1 || del != 0 {
		t.Fatalf("plan %d %d %d", ins, upd, del)
	}
	if op := rep.Plan.Ops[0]; op.Values["email"] != canonical.String("new@example.com") || len(op.Values) != 1 {
		t.Fatalf("op %+v", op)
	}

	rep, err = f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Applied {
		t.Fatalf("not applied: %+v", rep)
	}
	if got := f.queryString(f.local, "SELECT CONCAT(name, '|', email) FROM users WHERE id = 42"); got != "Peter Smith|new@example.com" {
		t.Fatalf("LOCAL after pull: %s", got)
	}
	// Uncommitted local change is still uncommitted; upstream part is committed.
	st, err := f.ws.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	_, m, _ := st.Working.Counts()
	if m != 1 || st.Working.Changes[0].Fields[0].Column != "name" {
		t.Fatalf("working tree after pull: %+v", st.Working.Changes)
	}
	// The local "Initial data" commit and the first TEST snapshot share no
	// history: the initial commit acts as base and a merge commit joins them.
	if st.Relation != core.RelLocalAhead || rep.FastForward {
		t.Fatalf("relation %s ff=%v", st.Relation, rep.FastForward)
	}
	head, _ := f.ws.Repo.Head()
	up, _ := f.ws.Repo.Upstream()
	c, _ := f.ws.Repo.Commit(head)
	if c.Kind != object.KindMerge || len(c.Parents) != 2 || c.Parents[1] != up {
		t.Fatalf("merge commit %+v (upstream %s)", c, up)
	}
	// The merge commit holds HEAD merged with upstream: name is still Peter
	// there, email is the upstream value.
	headState, _, _ := f.ws.HeadState()
	tb, _ := headState.Table("users")
	rec, _ := headState.Record(tb.Records[canonical.MakePKKey([]canonical.Value{canonical.Int64(42)})])
	if rec.Data["name"] != canonical.String("Peter") || rec.Data["email"] != canonical.String("new@example.com") {
		t.Fatalf("merge commit record %v", rec.Data)
	}

	// Commit the local change, then pull again: nothing to do.
	if _, _, err := f.ws.Commit(ctx, "rename", false); err != nil {
		t.Fatal(err)
	}
	rep, err = f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.UpToDate {
		t.Fatalf("expected up to date, got %+v", rep)
	}
}

func TestInsertUpdateDeleteRoundTrip(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()

	// Start from a common base: pull the initial TEST state (fast-forward).
	if rep, err := f.ws.Pull(ctx, false); err != nil || rep.Blocked() {
		t.Fatalf("initial pull: %v %+v", err, rep)
	}
	if rep, _ := f.ws.Pull(ctx, false); !rep.UpToDate {
		t.Fatal("second pull should be up to date")
	}

	// TEST: insert, update, delete.
	f.exec(f.test, "INSERT INTO users (id, name, email) VALUES (57, 'New', NULL)")
	f.exec(f.test, "UPDATE users SET name = 'Peter Updated' WHERE id = 42")
	f.exec(f.test, "DELETE FROM users WHERE id = 12")

	out, code := f.run("pull", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run (%d):\n%s", code, out)
	}
	for _, want := range []string{"INSERT users/57", "UPDATE users/42.name  Peter -> Peter Updated", "DELETE users/12", "Planned LOCAL changes: INSERT 1, UPDATE 1, DELETE 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	if f.count(f.local, "users") != 3 {
		t.Fatal("dry run changed LOCAL")
	}

	out, code = f.run("pull")
	if code != 0 {
		t.Fatalf("pull (%d):\n%s", code, out)
	}
	if f.count(f.local, "users") != 3 { // 3 - 1 + 1
		t.Fatalf("count after pull %d", f.count(f.local, "users"))
	}
	if got := f.queryString(f.local, "SELECT email FROM users WHERE id = 57"); got != "NULL" {
		t.Fatalf("inserted NULL email: %s", got)
	}
	if got := f.queryString(f.local, "SELECT name FROM users WHERE id = 42"); got != "Peter Updated" {
		t.Fatalf("updated name: %s", got)
	}
	if got := f.queryString(f.local, "SELECT COUNT(*) FROM users WHERE id = 12"); got != "0" {
		t.Fatal("row 12 not deleted")
	}
	st, err := f.ws.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Dirty() || st.Relation != core.RelUpToDate {
		t.Fatalf("status after pull: dirty=%v rel=%s", st.Dirty(), st.Relation)
	}
}

func TestDivergedMergeCommitAndDeleteConflicts(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()
	if _, err := f.ws.Pull(ctx, false); err != nil {
		t.Fatal(err)
	}
	base, _ := f.ws.Repo.Head()

	// Local commit: modify 50, add 60.
	f.exec(f.local, "UPDATE users SET name = 'Local50' WHERE id = 50")
	f.exec(f.local, "INSERT INTO users (id, name) VALUES (60, 'Local60')")
	if _, _, err := f.ws.Commit(ctx, "local work", false); err != nil {
		t.Fatal(err)
	}
	// Upstream: modify 42, delete 12, add 70.
	f.exec(f.test, "UPDATE users SET name = 'Test42' WHERE id = 42")
	f.exec(f.test, "DELETE FROM users WHERE id = 12")
	f.exec(f.test, "INSERT INTO users (id, name) VALUES (70, 'Test70')")

	st, err := f.ws.Status(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Relation != core.RelDiverged || st.Base != base {
		t.Fatalf("relation %s base %s (want %s)", st.Relation, st.Base, base)
	}
	// HEAD -> upstream: 42 modified, 50 modified (local change absent
	// upstream), 12 deleted, 60 deleted (local add absent upstream), 70 added.
	ua, um, ud := st.UpstreamDiff.Counts()
	if ua != 1 || um != 2 || ud != 2 {
		t.Fatalf("upstream diff %d %d %d", ua, um, ud)
	}

	rep, err := f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() || rep.FastForward || !rep.Applied {
		t.Fatalf("pull %+v", rep)
	}
	head, _ := f.ws.Repo.Head()
	c, err := f.ws.Repo.Commit(head)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != object.KindMerge || len(c.Parents) != 2 {
		t.Fatalf("merge commit %+v", c)
	}
	// LOCAL now has 42(Test42), 50(Local50), 60, 70 and not 12.
	if f.count(f.local, "users") != 4 {
		t.Fatalf("count %d", f.count(f.local, "users"))
	}
	if got := f.queryString(f.local, "SELECT name FROM users WHERE id = 42"); got != "Test42" {
		t.Fatal(got)
	}
	st, _ = f.ws.Status(ctx, false)
	if st.Dirty() || st.Relation != core.RelLocalAhead {
		t.Fatalf("after merge: dirty=%v rel=%s", st.Dirty(), st.Relation)
	}
	if rep, _ := f.ws.Pull(ctx, false); !rep.UpToDate {
		t.Fatal("pull right after a merge should be up to date")
	}
	// The merge commit's snapshot equals the working tree.
	out, code := f.run("log")
	if code != 0 || !strings.Contains(out, "(merge)") || !strings.Contains(out, "local work") {
		t.Fatalf("log:\n%s", out)
	}

	// Delete conflicts (base is now the upstream snapshot T2, which has 50
	// and 70 but never had the locally added 60): local modifies 50,
	// upstream deletes 50; local deletes 70, upstream modifies 70.
	f.exec(f.local, "UPDATE users SET name = 'Local50b' WHERE id = 50")
	f.exec(f.local, "DELETE FROM users WHERE id = 70")
	f.exec(f.test, "DELETE FROM users WHERE id = 50")
	f.exec(f.test, "UPDATE users SET name = 'Test70b' WHERE id = 70")
	rep, err = f.ws.Pull(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Conflicts) != 2 {
		t.Fatalf("conflicts %+v", rep.Conflicts)
	}
	kinds := map[merge.ConflictKind]bool{}
	for _, c := range rep.Conflicts {
		kinds[c.Kind] = true
	}
	if !kinds[merge.ModifyDelete] || !kinds[merge.DeleteModify] {
		t.Fatalf("conflict kinds %v", kinds)
	}

	// Working tree merges cleanly once 50 is back at the base value, but HEAD
	// (50=Local50, committed) still conflicts with the upstream delete: pull
	// asks to commit first.
	f.exec(f.local, "UPDATE users SET name = 'Same' WHERE id = 50")
	f.exec(f.local, "INSERT INTO users (id, name) VALUES (70, 'Test70')")
	f.exec(f.test, "UPDATE users SET name = 'Test70' WHERE id = 70")
	rep, err = f.ws.Pull(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Conflicts) != 1 || rep.ConflictSource != "HEAD" || rep.Conflicts[0].Kind != merge.ModifyDelete {
		t.Fatalf("conflicts %+v source %q", rep.Conflicts, rep.ConflictSource)
	}
	if _, _, err := f.ws.Commit(ctx, "back to base", false); err != nil {
		t.Fatal(err)
	}
	// Now HEAD's 50 equals the base: the upstream delete applies cleanly.
	rep, err = f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() {
		t.Fatalf("conflicts %+v", rep.Conflicts)
	}
	if got := f.queryString(f.local, "SELECT COUNT(*) FROM users WHERE id = 50"); got != "0" {
		t.Fatal("upstream delete not applied")
	}
}

func TestSameChangeBothSides(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()
	if _, err := f.ws.Pull(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.exec(f.local, "UPDATE users SET name = 'Both' WHERE id = 42")
	f.exec(f.test, "UPDATE users SET name = 'Both' WHERE id = 42")
	rep, err := f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() || !rep.Plan.Empty() {
		t.Fatalf("pull %+v stats %+v", rep.Plan, rep.Stats)
	}
	if got := f.queryString(f.local, "SELECT name FROM users WHERE id = 42"); got != "Both" {
		t.Fatal(got)
	}
}

func TestSchemaIncompatibleAborts(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()
	if _, err := f.ws.Pull(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.exec(f.test, "ALTER TABLE users ADD COLUMN age INT NULL")
	f.exec(f.test, "UPDATE users SET name = 'changed' WHERE id = 42")
	rep, err := f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SchemaIncompatibilities) != 1 || rep.SchemaIncompatibilities[0].Column != "age" {
		t.Fatalf("incompat %+v", rep.SchemaIncompatibilities)
	}
	if got := f.queryString(f.local, "SELECT name FROM users WHERE id = 42"); got != "Peter" {
		t.Fatal("LOCAL modified despite schema abort")
	}
	out, code := f.run("diff", "--schema", "--upstream")
	if code != 0 || !strings.Contains(out, "added column: users.age") {
		t.Fatalf("schema diff (%d):\n%s", code, out)
	}
	// Local schema change shows up in status / diff --schema too.
	f.exec(f.local, "ALTER TABLE users MODIFY name VARCHAR(200) NOT NULL DEFAULT 'x'")
	out, _ = f.run("diff", "--schema")
	if !strings.Contains(out, "changed column type: users.name (varchar(100) -> varchar(200))") || !strings.Contains(out, "changed default") {
		t.Fatalf("local schema diff:\n%s", out)
	}
	st, err := f.ws.Status(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Dirty() || len(st.SchemaChanges) == 0 {
		t.Fatal("schema change should make the tree dirty")
	}
}

func TestTypesCanonicalisationAndRoundTrip(t *testing.T) {
	f0 := newFixture(t, []string{"users"}, usersDDL)
	if !f0.supports(5, 7) {
		t.Skip("JSON and fractional seconds need MySQL 5.7+")
	}
	ddl := `CREATE TABLE kinds (
		a INT NOT NULL,
		b VARCHAR(20) NOT NULL,
		i_big BIGINT UNSIGNED NULL,
		i_tiny TINYINT(1) NULL,
		f FLOAT NULL,
		d DOUBLE NULL,
		dc DECIMAL(10,2) NULL,
		dt DATETIME(3) NULL,
		ts TIMESTAMP NULL,
		da DATE NULL,
		tm TIME NULL,
		yr YEAR NULL,
		bin VARBINARY(16) NULL,
		bl BLOB NULL,
		js JSON NULL,
		en ENUM('x','y') NULL,
		bt BIT(8) NULL,
		txt TEXT NULL,
		PRIMARY KEY (a, b)
	) ENGINE=InnoDB`
	f := newFixture(t, []string{"kinds"}, ddl)
	ctx := context.Background()

	insert := `INSERT INTO kinds VALUES
		(1, 'x,y', 18446744073709551615, 1, 3.14, 0.1, 12.50, '2024-01-15 10:30:00.123', '2024-06-01 12:00:00', '2024-02-29', '-10:30:00', 2024, X'00FF10', 'blob data', '{"b": [1, 2.50, {"z": null, "a": "é"}], "a": 1e2}', 'y', b'00000101', 'héllo\nworld'),
		(1, 'x', NULL, 0, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`
	f.exec(f.test, insert)

	// Fresh LOCAL: pull inserts everything.
	rep, err := f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() || !rep.Applied {
		t.Fatalf("pull %+v", rep)
	}
	ins, _, _ := rep.Plan.Counts()
	if ins != 2 {
		t.Fatalf("inserts %d", ins)
	}
	// The apply verified the tables hash-equal; additionally compare a few
	// values through MySQL itself.
	for _, col := range []string{"i_big", "i_tiny", "f", "d", "dc", "dt", "ts", "da", "tm", "yr", "HEX(bin)", "bl", "JSON_EXTRACT(js, '$.b[2].a')", "en", "bt+0", "txt"} {
		q := fmt.Sprintf("SELECT %s FROM kinds WHERE a = 1 AND b = 'x,y'", col)
		if got, want := f.queryString(f.local, q), f.queryString(f.test, q); got != want {
			t.Errorf("%s: local %q != test %q", col, got, want)
		}
	}
	// Status clean, and a second pull is a no-op.
	st, err := f.ws.Status(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Dirty() || st.Relation != core.RelUpToDate {
		t.Fatalf("status dirty=%v rel=%s changes=%+v", st.Dirty(), st.Relation, st.Working.Changes)
	}

	// Inspect canonical values of the record.
	head, _ := f.ws.Repo.Head()
	c, _ := f.ws.Repo.Commit(head)
	snap, _ := f.ws.Repo.Snapshot(c.Snapshot)
	man, _ := f.ws.Repo.Manifest(snap.Tables["kinds"])
	key := canonical.MakePKKey([]canonical.Value{canonical.Int64(1), canonical.String("x,y")})
	rec, err := f.ws.Repo.Record(man.Records[key])
	if err != nil {
		t.Fatalf("record for %s: %v (keys %v)", key, err, man.Records)
	}
	want := map[string]string{
		"i_big":  "18446744073709551615",
		"i_tiny": "1",
		"f":      "3.14",
		"d":      "0.1",
		"dc":     `"12.50"`,
		"dt":     `"2024-01-15 10:30:00.123"`,
		"ts":     `"2024-06-01 12:00:00"`,
		"da":     `"2024-02-29"`,
		"tm":     `"-10:30:00"`,
		"yr":     `"2024"`,
		"bin":    `{"$b64":"AP8Q"}`,
		"js":     `"{\"a\":100,\"b\":[1,2.5,{\"a\":\"é\",\"z\":null}]}"`,
		"en":     `"y"`,
		"bt":     "5",
	}
	for col, w := range want {
		if got := string(rec.Data[col]); got != w {
			t.Errorf("%s: got %s want %s", col, got, w)
		}
	}
	if rec.Data["txt"].Display() != `"héllo\nworld"` {
		t.Errorf("txt display %s", rec.Data["txt"].Display())
	}

	// Composite key display and NULL handling in diff.
	f.exec(f.local, "UPDATE kinds SET dc = 1.5, js = '{\"k\": 1}' WHERE a = 1 AND b = 'x'")
	out, _ := f.run("diff")
	if !strings.Contains(out, "modified: kinds/a=1,b=x\n") || !strings.Contains(out, "-  dc: NULL\n+  dc: 1.50\n") || !strings.Contains(out, `+  js: {"k":1}`) {
		t.Fatalf("diff:\n%s", out)
	}
	// JSON key order does not matter.
	f.exec(f.test, "UPDATE kinds SET js = '{\"a\": 1e2, \"b\": [1, 2.5, {\"a\": \"é\", \"z\": null}]}' WHERE a = 1 AND b = 'x,y'")
	st, _ = f.ws.Status(ctx, true)
	if !st.UpstreamDiff.Empty() {
		t.Fatalf("json reordering produced a diff: %+v", st.UpstreamDiff.Changes)
	}
}

func TestCommitDedupAndRepositoryLayout(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()
	if _, _, err := f.ws.Commit(ctx, "one", false); err != nil {
		t.Fatal(err)
	}
	recordsBefore := countFiles(t, filepath.Join(f.dir, ".sqit", "objects", "records"))
	if recordsBefore != 3 {
		t.Fatalf("records after first commit: %d", recordsBefore)
	}
	if _, _, err := f.ws.Commit(ctx, "empty", false); err != core.ErrNothingToCommit {
		t.Fatalf("expected nothing to commit, got %v", err)
	}
	f.exec(f.local, "UPDATE users SET name = 'Changed' WHERE id = 42")
	h2, c2, err := f.ws.Commit(ctx, "two", false)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, filepath.Join(f.dir, ".sqit", "objects", "records")); n != 4 {
		t.Fatalf("records after second commit: %d (want 4: only the changed row is new)", n)
	}
	if n := countFiles(t, filepath.Join(f.dir, ".sqit", "objects", "schemas")); n != 1 {
		t.Fatalf("schemas: %d (want 1, unchanged schema is shared)", n)
	}
	if c2.Parents[0] == "" || len(c2.Parents) != 1 {
		t.Fatalf("parents %v", c2.Parents)
	}
	// Refs are plain files with a trailing newline (Git friendly).
	b, _ := os.ReadFile(filepath.Join(f.dir, ".sqit", "refs", "heads", "main"))
	if string(b) != string(h2)+"\n" {
		t.Fatalf("ref content %q", b)
	}
	// No secrets in config.
	cfg, _ := os.ReadFile(filepath.Join(f.dir, ".sqit", "config.json"))
	if strings.Contains(string(cfg), "sqit@") || strings.Contains(strings.ToLower(string(cfg)), "password\":") {
		t.Fatalf("config leaks credentials:\n%s", cfg)
	}
	out, code := f.run("show", string(h2[:8]))
	if code != 0 || !strings.Contains(out, "-  name: Peter\n+  name: Changed\n") {
		t.Fatalf("show (%d):\n%s", code, out)
	}
	out, code = f.run("status")
	if code != 0 || !strings.Contains(out, "CLEAN") || !strings.Contains(out, "NO UPSTREAM") {
		t.Fatalf("status (%d):\n%s", code, out)
	}
}

func TestUnrelatedHistoriesAndFirstPullFastForward(t *testing.T) {
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()
	// Local commit first, then pull: no common ancestor -> merge with empty base.
	if _, _, err := f.ws.Commit(ctx, "local first", false); err != nil {
		t.Fatal(err)
	}
	st, err := f.ws.Status(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Relation != core.RelUnrelated {
		t.Fatalf("relation %s", st.Relation)
	}
	rep, err := f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() || rep.FastForward || !rep.Plan.Empty() {
		t.Fatalf("pull %+v", rep)
	}
	st, _ = f.ws.Status(ctx, false)
	if st.Relation != core.RelLocalAhead {
		t.Fatalf("relation after merge %s", st.Relation)
	}
	head, _ := f.ws.Repo.Head()
	c, _ := f.ws.Repo.Commit(head)
	if c.Kind != object.KindMerge {
		t.Fatalf("expected a merge commit joining the unrelated histories, got %s", c.Kind)
	}
}

func TestConcurrentModificationRollsBack(t *testing.T) {
	// If a row changes between the snapshot and the apply, the exact-one-row
	// check fails and nothing is committed. Simulated by removing a row the
	// plan wants to update, from another connection, after snapshotting.
	// We cannot interleave inside Pull, so verify the weaker property: the
	// apply refuses when the DELETE finds no row (0 rows affected).
	f := newFixture(t, []string{"users"}, usersDDL)
	seedUsers(f)
	ctx := context.Background()
	if _, err := f.ws.Pull(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.exec(f.test, "DELETE FROM users WHERE id = 12")
	// Make LOCAL differ from what was snapshotted by having the row already
	// gone, but leave HEAD believing it exists: local delete + upstream delete
	// is "same", plan is empty -> not the case we want. Instead create a
	// trigger that prevents the delete so 0 rows are affected.
	f.exec(f.local, "CREATE TRIGGER keep12 BEFORE DELETE ON users FOR EACH ROW BEGIN IF OLD.id = 12 THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'row 12 is protected'; END IF; END")
	_, err := f.ws.Pull(ctx, false)
	if err == nil || !strings.Contains(err.Error(), "row 12 is protected") {
		t.Fatalf("expected apply error, got %v", err)
	}
	if f.count(f.local, "users") != 3 {
		t.Fatal("transaction not rolled back")
	}
	if head, _ := f.ws.Repo.Head(); head == "" {
		t.Fatal("head lost")
	}
	st, _ := f.ws.Status(ctx, false)
	if st.Dirty() {
		t.Fatal("HEAD moved despite failed apply")
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTrackAllTablesWithIgnore(t *testing.T) {
	f := newFixture(t, nil, usersDDL,
		"CREATE TABLE orders (id INT PRIMARY KEY, total DECIMAL(10,2)) ENGINE=InnoDB",
		"CREATE TABLE sessions (id INT PRIMARY KEY, token VARCHAR(50)) ENGINE=InnoDB",
		"CREATE TABLE tmp_import (id INT PRIMARY KEY) ENGINE=InnoDB",
		"CREATE TABLE audit_log (msg VARCHAR(100)) ENGINE=InnoDB") // no primary key
	seedUsers(f)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(f.dir, repo.IgnoreFileName), []byte("sessions\ntmp_*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	f.ws.Warn = func(m string) { warnings = append(warnings, m) }

	tables, err := f.ws.TrackedTables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tables, ",") != "orders,users" {
		t.Fatalf("tracked %v", tables)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "audit_log") {
		t.Fatalf("warnings %v", warnings)
	}
	// Personal ignore from the environment (sqit.env) adds to .sqitignore.
	t.Setenv(repo.IgnoreEnv, "orders")
	tables, _ = f.ws.TrackedTables(ctx)
	if strings.Join(tables, ",") != "users" {
		t.Fatalf("tracked with env ignore %v", tables)
	}
	os.Unsetenv(repo.IgnoreEnv)

	// The whole workflow works without an explicit table list.
	if _, _, err := f.ws.Commit(ctx, "all tables", false); err != nil {
		t.Fatal(err)
	}
	f.exec(f.test, "INSERT INTO orders VALUES (1, 5.00)")
	f.exec(f.test, "INSERT INTO sessions VALUES (1, 'ignored')")
	rep, err := f.ws.Pull(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() || !rep.Applied {
		t.Fatalf("pull %+v", rep)
	}
	if f.count(f.local, "orders") != 1 || f.count(f.local, "sessions") != 0 {
		t.Fatal("ignored table must not be synchronised, tracked table must")
	}
	out, code := f.run("status", "--json")
	if code != 0 || !strings.Contains(out, `"tables": [`) || strings.Contains(out, `"sessions"`) {
		t.Fatalf("status --json (%d):\n%s", code, out)
	}
}

func TestPullCreatesMissingTables(t *testing.T) {
	f := newFixture(t, nil, usersDDL)
	seedUsers(f)
	ctx := context.Background()
	if _, err := f.ws.Pull(ctx, false); err != nil {
		t.Fatal(err)
	}
	// A new table appears upstream only.
	f.exec(f.test, "CREATE TABLE newtable (id INT PRIMARY KEY, label VARCHAR(20) NULL) ENGINE=InnoDB")
	f.exec(f.test, "INSERT INTO newtable VALUES (1, 'one'), (2, NULL)")

	rep, err := f.ws.Pull(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SchemaIncompatibilities) != 1 || len(rep.MissingTableDDL) != 1 || !strings.Contains(rep.MissingTableDDL[0], "CREATE TABLE `newtable`") {
		t.Fatalf("dry run: %+v", rep)
	}
	out, code := f.run("pull")
	if code != 1 || !strings.Contains(out, "added table: newtable") || !strings.Contains(out, "sqit pull --create-missing-tables") {
		t.Fatalf("pull (%d):\n%s", code, out)
	}
	// Dry run with the flag still creates nothing.
	rep, err = f.ws.PullWith(ctx, true, core.PullOptions{CreateMissingTables: true})
	if err != nil || len(rep.CreatedTables) != 0 {
		t.Fatalf("dry run created tables: %+v %v", rep.CreatedTables, err)
	}
	// Real pull with the flag: table created, rows pulled, merge recorded.
	rep, err = f.ws.PullWith(ctx, false, core.PullOptions{CreateMissingTables: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked() || !rep.Applied || len(rep.CreatedTables) != 1 {
		t.Fatalf("pull: %+v", rep)
	}
	if f.count(f.local, "newtable") != 2 {
		t.Fatalf("rows in created table: %d", f.count(f.local, "newtable"))
	}
	if got := f.queryString(f.local, "SELECT label FROM newtable WHERE id = 2"); got != "NULL" {
		t.Fatal(got)
	}
	st, _ := f.ws.Status(ctx, false)
	if st.Dirty() {
		t.Fatalf("dirty after pull: %+v", st.Working.Changes)
	}
	// A column difference in an existing table is still refused, even with the flag.
	f.exec(f.test, "ALTER TABLE users ADD COLUMN age INT NULL")
	f.exec(f.test, "CREATE TABLE another (id INT PRIMARY KEY) ENGINE=InnoDB")
	rep, err = f.ws.PullWith(ctx, false, core.PullOptions{CreateMissingTables: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SchemaIncompatibilities) != 2 || len(rep.CreatedTables) != 0 || len(rep.MissingTableDDL) != 1 {
		t.Fatalf("mixed: %+v", rep)
	}
}
