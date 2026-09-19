package repo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sqit/internal/object"
)

func newRepo(t *testing.T) *Repository {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Tables = []string{"users"}
	r, err := Init(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func commit(t *testing.T, r *Repository, kind object.CommitKind, msg string, parents ...object.Hash) object.Hash {
	t.Helper()
	if parents == nil {
		parents = []object.Hash{}
	}
	h, err := r.Put(object.TypeCommit, &object.Commit{
		Kind: kind, Parents: parents, Snapshot: "s", Schema: "c", Message: msg,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Metadata: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestInitOpenAndRefs(t *testing.T) {
	r := newRepo(t)
	h, err := r.Head()
	if err != nil || h != "" {
		t.Fatalf("unborn head: %q %v", h, err)
	}
	up, _ := r.Upstream()
	if up != "" {
		t.Fatal("upstream should be empty")
	}
	c1 := commit(t, r, object.KindLocal, "one")
	if err := r.SetHead(c1); err != nil {
		t.Fatal(err)
	}
	// Open from a nested directory finds the repository.
	nested := filepath.Join(r.Root, "a", "b")
	os.MkdirAll(nested, 0o755)
	r2, err := Open(nested)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := r2.Head(); h != c1 {
		t.Fatalf("head %s", h)
	}
	if r2.Config.Tables[0] != "users" {
		t.Fatal("config not loaded")
	}
	if _, err := Init(r.Root, DefaultConfig()); err == nil {
		t.Fatal("double init should fail")
	}
	// Object integrity: corrupt a file and expect a hash error.
	p := r.objectPath(object.TypeCommit, c1)
	os.WriteFile(p, []byte(`{"kind":"local"}`+"\n"), 0o644)
	if _, err := r.Commit(c1); err == nil {
		t.Fatal("corrupt object should fail verification")
	}
}

func TestPutIsIdempotentAndDeduplicates(t *testing.T) {
	r := newRepo(t)
	rec := &object.Record{Table: "users", PK: map[string]canonicalValue{"id": "1"}, Data: map[string]canonicalValue{"id": "1", "n": `"a"`}}
	h1, _ := r.Put(object.TypeRecord, rec)
	h2, _ := r.Put(object.TypeRecord, rec)
	if h1 != h2 || !r.Has(object.TypeRecord, h1) {
		t.Fatal("dedup failed")
	}
	got, err := r.Record(h1)
	if err != nil || got.Data["n"] != `"a"` {
		t.Fatalf("round trip %v %v", got, err)
	}
	if h, err := r.ResolveCommit(string(h1[:8])); err == nil {
		t.Fatalf("record hash must not resolve as a commit: %s", h)
	}
}

func TestMergeBaseAndRelations(t *testing.T) {
	r := newRepo(t)
	//  T1 - T2 - T3          (upstream)
	//   \     \
	//    C1 -- M1 - C2        (local)
	t1 := commit(t, r, object.KindUpstream, "t1")
	c1 := commit(t, r, object.KindLocal, "c1", t1)
	t2 := commit(t, r, object.KindUpstream, "t2", t1)
	m1 := commit(t, r, object.KindMerge, "m1", c1, t2)
	c2 := commit(t, r, object.KindLocal, "c2", m1)
	t3 := commit(t, r, object.KindUpstream, "t3", t2)

	cases := []struct{ a, b, want object.Hash }{
		{c1, t2, t1},
		{m1, t3, t2},
		{c2, t3, t2},
		{c2, t2, t2},
		{t2, t3, t2},
		{c1, c1, c1},
	}
	for _, c := range cases {
		got, err := r.MergeBase(c.a, c.b)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("MergeBase(%s,%s)=%s want %s", c.a.Short(), c.b.Short(), got.Short(), c.want.Short())
		}
	}
	// Unrelated roots.
	x := commit(t, r, object.KindLocal, "x")
	if b, _ := r.MergeBase(x, t3); b != "" {
		t.Fatalf("unrelated base %s", b)
	}
	if root, _ := r.RootCommit(c2); root != t1 {
		t.Fatalf("root %s", root)
	}
	if ok, _ := r.IsAncestor(t1, c2); !ok {
		t.Fatal("t1 should be an ancestor of c2")
	}
	if ok, _ := r.IsAncestor(c2, t3); ok {
		t.Fatal("c2 is not an ancestor of t3")
	}
	if n, _ := r.CountBetween(t2, c2); n != 3 { // c1, m1, c2
		t.Fatalf("count %d", n)
	}
	// Abbreviated resolution.
	if h, err := r.ResolveCommit(string(c2[:7])); err != nil || h != c2 {
		t.Fatalf("resolve %s %v", h, err)
	}
	r.SetHead(c2)
	r.WriteRef(UpstreamRef, t3)
	if h, _ := r.ResolveCommit("HEAD"); h != c2 {
		t.Fatal("HEAD")
	}
	if h, _ := r.ResolveCommit("test"); h != t3 {
		t.Fatal("test")
	}
}
