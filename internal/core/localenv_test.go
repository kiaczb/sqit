package core_test

// End-to-end test of the documented local workflow against the Docker
// environment from docker-compose.yml (README "Local development / testing").
//
// Needs all three variables, e.g. from .env:
//
//	SQIT_IT_DSN    root DSN (used only to reseed and to inspect the databases)
//	SQIT_LOCAL_DSN sqit_local_user  -> sqit_local
//	SQIT_TEST_DSN  sqit_upstream_user -> sqit_upstream (SELECT only)
//
// The databases are reset from testdata/*.sql before the test runs.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sqit/internal/core"
	"sqit/internal/repo"
)

type localEnv struct {
	t        *testing.T
	root     *sql.DB
	dir      string
	ws       *core.Workspace
	localDSN string
	testDSN  string
}

func newLocalEnv(t *testing.T) *localEnv {
	t.Helper()
	rootDSN, localDSN, testDSN := os.Getenv("SQIT_IT_DSN"), os.Getenv("SQIT_LOCAL_DSN"), os.Getenv("SQIT_TEST_DSN")
	if rootDSN == "" || localDSN == "" || testDSN == "" {
		t.Skip("SQIT_IT_DSN, SQIT_LOCAL_DSN and SQIT_TEST_DSN are required (see .env.example)")
	}
	root, err := sql.Open("mysql", rootDSN+"?multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	if err := root.Ping(); err != nil {
		t.Fatalf("cannot reach MySQL at SQIT_IT_DSN: %v", err)
	}
	e := &localEnv{t: t, root: root, localDSN: localDSN, testDSN: testDSN}
	e.reseed()

	e.dir = t.TempDir()
	cfg := repo.DefaultConfig()
	cfg.Tables = []string{"users", "orders"}
	cfg.LocalOnly = true
	if _, err := repo.Init(e.dir, cfg); err != nil {
		t.Fatal(err)
	}
	ws, err := core.Open(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	e.ws = ws
	return e
}

// reseed replays testdata/*.sql so every test starts from the documented state.
func (e *localEnv) reseed() {
	e.t.Helper()
	td := filepath.Join("..", "..", "testdata")
	schema := filepath.Join(td, "00-schema.sql")
	var version string
	if err := e.root.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		e.t.Fatal(err)
	}
	var major, minor int
	fmt.Sscanf(version, "%d.%d", &major, &minor)
	if major < 5 || (major == 5 && minor < 7) {
		// CREATE USER IF NOT EXISTS needs 5.7; older servers use the
		// GRANT-based variant (also used for existing servers).
		schema = filepath.Join(td, "existing-server", "00-schema.sql")
	}
	files := []string{schema, filepath.Join(td, "10-local.sql"), filepath.Join(td, "20-upstream.sql")}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			e.t.Fatal(err)
		}
		if _, err := e.root.Exec(string(b)); err != nil {
			e.t.Fatalf("%s: %v", f, err)
		}
	}
}

func (e *localEnv) sql(db, query string) {
	e.t.Helper()
	if _, err := e.root.Exec("USE " + db + "; " + query); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func (e *localEnv) get(query string) string {
	e.t.Helper()
	var s sql.NullString
	if err := e.root.QueryRow(query).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	if !s.Valid {
		return "NULL"
	}
	return s.String
}

func (e *localEnv) run(args ...string) (string, int) {
	e.t.Helper()
	f := &fixture{t: e.t, dir: e.dir}
	return f.run(args...)
}

func (e *localEnv) mustRun(args ...string) string {
	e.t.Helper()
	out, code := e.run(args...)
	if code != 0 {
		e.t.Fatalf("sqit %s exited %d:\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

func TestLocalEnvironmentWorkflow(t *testing.T) {
	e := newLocalEnv(t)
	ctx := context.Background()
	const localName = "SELECT name FROM sqit_local.users WHERE id = 42"

	// Security invariant: the upstream user must not be able to write.
	up, err := sql.Open("mysql", e.testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	if _, err := up.Exec("UPDATE users SET name = 'hacked' WHERE id = 42"); err == nil {
		t.Fatal("sqit_upstream_user could UPDATE the upstream database")
	}
	if _, err := up.Exec("INSERT INTO users (id, name) VALUES (999, 'x')"); err == nil {
		t.Fatal("sqit_upstream_user could INSERT into the upstream database")
	}

	// Local-only guard: a non-localhost upstream DSN is refused before any
	// connection attempt.
	t.Setenv("SQIT_TEST_DSN", "sqit_upstream_user:pw@tcp(test-db.corp.example:3306)/prod")
	if _, err := e.ws.Fetch(ctx); err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("expected local-only refusal, got %v", err)
	}
	t.Setenv("SQIT_TEST_DSN", e.testDSN)

	// 1-2. initial state + initial commit
	e.mustRun("commit", "-m", "Initial state")

	// 3. local modification
	e.sql("sqit_local", "UPDATE users SET name = 'Peter Smith' WHERE id = 42")
	out := e.mustRun("status")
	if !strings.Contains(out, "Modified: 1") || !strings.Contains(out, "LOCAL MODIFIED") {
		t.Fatalf("status:\n%s", out)
	}
	out = e.mustRun("diff")
	if !strings.Contains(out, "modified: users/42\n") || !strings.Contains(out, "-  name: Peter\n+  name: Peter Smith\n") {
		t.Fatalf("diff:\n%s", out)
	}

	// 4. upstream modification (non-conflicting: email only) + fetch
	e.sql("sqit_upstream", "UPDATE users SET email = 'new@example.com' WHERE id = 42")
	out = e.mustRun("fetch")
	if !strings.Contains(out, "new TEST snapshot") {
		t.Fatalf("fetch:\n%s", out)
	}
	out = e.mustRun("diff", "--upstream")
	if !strings.Contains(out, "-  email: old@example.com\n+  email: new@example.com\n") {
		t.Fatalf("diff --upstream:\n%s", out)
	}

	// 5. non-conflicting merge lands in sqit_local
	out = e.mustRun("pull", "--dry-run")
	if !strings.Contains(out, "UPDATE users/42.email  old@example.com -> new@example.com") {
		t.Fatalf("pull --dry-run:\n%s", out)
	}
	if got := e.get("SELECT email FROM sqit_local.users WHERE id = 42"); got != "old@example.com" {
		t.Fatalf("dry run modified LOCAL: %s", got)
	}
	e.mustRun("pull")
	if got := e.get("SELECT CONCAT(name, '|', email) FROM sqit_local.users WHERE id = 42"); got != "Peter Smith|new@example.com" {
		t.Fatalf("after pull: %s", got)
	}
	out = e.mustRun("status")
	if !strings.Contains(out, "Modified: 1") { // Peter Smith is still uncommitted
		t.Fatalf("status after pull:\n%s", out)
	}
	e.mustRun("commit", "-m", "Rename Peter")

	// 6. conflict: LOCAL name vs UPSTREAM name
	e.sql("sqit_local", "UPDATE users SET name = 'Peter Smith Jr' WHERE id = 42")
	e.sql("sqit_upstream", "UPDATE users SET name = 'Péter' WHERE id = 42")
	headBefore, _ := e.ws.Repo.Head()
	out, code := e.run("pull", "--dry-run")
	if code != 1 || !strings.Contains(out, "CONFLICTS: 1") || !strings.Contains(out, "users/42.name  [modify/modify]") ||
		!strings.Contains(out, "BASE:     Peter\n") || !strings.Contains(out, "LOCAL:    Peter Smith Jr\n") || !strings.Contains(out, "UPSTREAM: Péter\n") {
		t.Fatalf("pull --dry-run (%d):\n%s", code, out)
	}
	// Also add an unrelated upstream insert: with a conflict NOTHING may be
	// applied, not even the clean parts.
	e.sql("sqit_upstream", "INSERT INTO users (id, name, email) VALUES (5, 'Eve', 'eve@example.com')")
	out, code = e.run("pull")
	if code != 1 || !strings.Contains(out, "CONFLICTS: 1") {
		t.Fatalf("pull (%d):\n%s", code, out)
	}
	if got := e.get(localName); got != "Peter Smith Jr" {
		t.Fatalf("conflicting pull modified LOCAL: %s", got)
	}
	if got := e.get("SELECT COUNT(*) FROM sqit_local.users WHERE id = 5"); got != "0" {
		t.Fatal("conflicting pull partially applied the upstream insert")
	}
	if head, _ := e.ws.Repo.Head(); head != headBefore {
		t.Fatal("conflicting pull moved HEAD")
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, ".sqit", "refs", "heads", "main"))
	if strings.TrimSpace(string(b)) != string(headBefore) {
		t.Fatal("refs/heads/main damaged")
	}
	// Resolve by accepting the upstream name in LOCAL. The committed
	// "Peter Smith" still conflicts with upstream, so pull asks to commit the
	// resolution first; after that it merges cleanly.
	e.sql("sqit_local", "UPDATE users SET name = 'Péter' WHERE id = 42")
	out, code = e.run("pull")
	if code != 1 || !strings.Contains(out, "CONFLICTS: 1 (HEAD vs upstream)") {
		t.Fatalf("pull (%d):\n%s", code, out)
	}
	e.mustRun("commit", "-m", "Resolve")
	e.mustRun("pull")
	if got := e.get("SELECT COUNT(*) FROM sqit_local.users WHERE id = 5"); got != "1" {
		t.Fatal("upstream insert not applied after resolution")
	}

	// 7. INSERT / DELETE on both sides.
	e.sql("sqit_upstream", "INSERT INTO users (id, name) VALUES (6, 'Frank')") // UPSTREAM INSERT
	e.sql("sqit_upstream", "DELETE FROM users WHERE id = 1")                   // UPSTREAM DELETE, local unchanged
	e.sql("sqit_upstream", "DELETE FROM users WHERE id = 2")                   // UPSTREAM DELETE, local modified -> conflict
	e.sql("sqit_local", "UPDATE users SET name = 'Johnny' WHERE id = 2")
	e.sql("sqit_local", "INSERT INTO users (id, name) VALUES (7, 'Grace')") // LOCAL INSERT
	e.sql("sqit_local", "DELETE FROM users WHERE id = 3")                   // LOCAL DELETE
	e.sql("sqit_local", "INSERT INTO orders VALUES (7, 1, 5.00, NULL)")     // composite-key LOCAL INSERT
	e.sql("sqit_upstream", "UPDATE orders SET total = 11.00 WHERE customer_id = 1 AND order_id = 1")

	out, code = e.run("pull", "--dry-run")
	if code != 1 || !strings.Contains(out, "users/2  [modify/delete]") {
		t.Fatalf("expected modify/delete conflict (%d):\n%s", code, out)
	}
	// Drop the local modification of row 2 -> upstream delete applies.
	e.sql("sqit_local", "UPDATE users SET name = 'John' WHERE id = 2")
	out = e.mustRun("pull")
	for _, want := range []string{"INSERT users/6", "DELETE users/1", "DELETE users/2", "UPDATE orders/customer_id=1,order_id=1.total  10.00 -> 11.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("pull output lacks %q:\n%s", want, out)
		}
	}
	checks := map[string]string{
		"SELECT COUNT(*) FROM sqit_local.users WHERE id IN (1, 2)":                   "0",
		"SELECT name FROM sqit_local.users WHERE id = 6":                             "Frank",
		"SELECT name FROM sqit_local.users WHERE id = 7":                             "Grace",
		"SELECT COUNT(*) FROM sqit_local.users WHERE id = 3":                         "0",
		"SELECT total FROM sqit_local.orders WHERE customer_id = 1 AND order_id = 1": "11.00",
		"SELECT total FROM sqit_local.orders WHERE customer_id = 7 AND order_id = 1": "5.00",
		"SELECT GROUP_CONCAT(id ORDER BY id) FROM sqit_local.users":                  "5,6,7,42",
		"SELECT GROUP_CONCAT(id ORDER BY id) FROM sqit_upstream.users":               "3,5,6,42",
		"SELECT name FROM sqit_upstream.users WHERE id = 42":                         "Péter",
	}
	for q, want := range checks {
		if got := e.get(q); got != want {
			t.Errorf("%s = %q, want %q", q, got, want)
		}
	}
	out = e.mustRun("status")
	if !strings.Contains(out, "Modified: 0") || !strings.Contains(out, "Added:    2") || !strings.Contains(out, "Deleted:  1") {
		// local INSERT users/7 + orders/7,1 and DELETE users/3 stay uncommitted
		t.Fatalf("status after pull:\n%s", out)
	}
}
