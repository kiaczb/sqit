package diff

import (
	"strings"
	"testing"

	"sqit/internal/canonical"
	"sqit/internal/object"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

func testSchema() *schema.Schema {
	return &schema.Schema{Tables: []schema.Table{
		{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.Column{
			{Name: "id", Position: 1}, {Name: "name", Position: 2}, {Name: "email", Position: 3},
		}},
	}}
}

func user(id int64, name, email string) *object.Record {
	return &object.Record{Table: "users",
		PK:   map[string]canonical.Value{"id": canonical.Int64(id)},
		Data: map[string]canonical.Value{"id": canonical.Int64(id), "name": canonical.String(name), "email": canonical.String(email)}}
}

func state(t *testing.T, recs ...*object.Record) *snapshot.Memory {
	t.Helper()
	m := snapshot.NewMemory(testSchema())
	for _, r := range recs {
		if _, err := m.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestCompare(t *testing.T) {
	a := state(t, user(12, "gone", "g@x"), user(42, "Peter", "peter@example.com"), user(50, "same", "s@x"))
	b := state(t, user(42, "Peter Smith", "peter@example.com"), user(50, "same", "s@x"), user(57, "new", "n@x"))

	d, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	added, modified, deleted := d.Counts()
	if added != 1 || modified != 1 || deleted != 1 {
		t.Fatalf("counts %d %d %d", added, modified, deleted)
	}
	// Sorted numerically by key: 12 (deleted), 42 (modified), 57 (added).
	if d.Changes[0].Kind != Deleted || d.Changes[0].ID() != "users/12" {
		t.Errorf("c0 %+v", d.Changes[0])
	}
	if d.Changes[1].Kind != Modified || len(d.Changes[1].Fields) != 1 || d.Changes[1].Fields[0].Column != "name" {
		t.Errorf("c1 %+v", d.Changes[1])
	}
	if d.Changes[2].Kind != Added || d.Changes[2].ID() != "users/57" {
		t.Errorf("c2 %+v", d.Changes[2])
	}

	var sb strings.Builder
	d.Write(&sb, func(string) []string { return []string{"id", "name", "email"} })
	out := sb.String()
	want := "modified: users/42\n   id: 42\n-  name: Peter\n+  name: Peter Smith\n   email: peter@example.com\n"
	if !strings.Contains(out, want) {
		t.Fatalf("output:\n%s", out)
	}
	if !strings.Contains(out, "deleted: users/12\n-  id: 12\n") || !strings.Contains(out, "added: users/57\n+  id: 57\n") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestCompareIdenticalIsEmptyAndSkipsRecords(t *testing.T) {
	a := state(t, user(1, "a", "x"))
	b := state(t, user(1, "a", "x"))
	d, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() {
		t.Fatalf("expected empty diff, got %+v", d.Changes)
	}
}

func TestCompareAgainstEmpty(t *testing.T) {
	d, err := Compare(snapshot.Empty(), state(t, user(1, "a", "x"), user(2, "b", "x")))
	if err != nil {
		t.Fatal(err)
	}
	added, _, _ := d.Counts()
	if added != 2 {
		t.Fatalf("added %d", added)
	}
	d, err = Compare(state(t, user(1, "a", "x")), snapshot.Empty())
	if err != nil {
		t.Fatal(err)
	}
	_, _, deleted := d.Counts()
	if deleted != 1 {
		t.Fatalf("deleted %d", deleted)
	}
}

func TestCompareFieldsNull(t *testing.T) {
	old := map[string]canonical.Value{"a": canonical.Null, "b": canonical.String("x")}
	new := map[string]canonical.Value{"a": canonical.String(""), "b": canonical.String("x")}
	f := CompareFields(old, new)
	if len(f) != 1 || f[0].Column != "a" || f[0].Old != canonical.Null || f[0].New != canonical.String("") {
		t.Fatalf("fields %+v", f)
	}
}
