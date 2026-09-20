package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoredPatterns(t *testing.T) {
	pat := []string{"sessions", "tmp_*", "*_log", "cache?"}
	yes := []string{"sessions", "SESSIONS", "tmp_users", "audit_log", "cache1"}
	no := []string{"users", "tmp", "logs", "cache10"}
	for _, n := range yes {
		if !Ignored(n, pat) {
			t.Errorf("%s should be ignored", n)
		}
	}
	for _, n := range no {
		if Ignored(n, pat) {
			t.Errorf("%s should not be ignored", n)
		}
	}
}

func TestSelectTables(t *testing.T) {
	r := newRepo(t)
	r.Config.Tables = nil
	os.WriteFile(filepath.Join(r.Root, IgnoreFileName), []byte("# comment\nsessions   # trailing comment\ntmp_*\n\n"), 0o644)
	t.Setenv(IgnoreEnv, "scratch, *_log")
	got, err := r.SelectTables([]string{"users", "sessions", "tmp_1", "orders", "scratch", "audit_log"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "orders" || got[1] != "users" {
		t.Fatalf("got %v", got)
	}
	// Explicit list wins over ignores.
	r.Config.Tables = []string{"sessions", "users"}
	got, _ = r.SelectTables([]string{"users", "sessions", "orders"})
	if len(got) != 2 || got[0] != "sessions" {
		t.Fatalf("explicit: %v", got)
	}
}
