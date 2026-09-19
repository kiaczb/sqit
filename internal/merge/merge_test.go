package merge

import (
	"encoding/json"
	"strings"
	"testing"

	"sqit/internal/canonical"
	"sqit/internal/diff"
	"sqit/internal/object"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

func testSchema() *schema.Schema {
	return &schema.Schema{Tables: []schema.Table{
		{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.Column{
			{Name: "id", Position: 1, DataType: "int"},
			{Name: "name", Position: 2, DataType: "varchar"},
			{Name: "email", Position: 3, DataType: "varchar"},
		}},
		{Name: "orders", PrimaryKey: []string{"customer_id", "order_id"}, Columns: []schema.Column{
			{Name: "customer_id", Position: 1, DataType: "int"},
			{Name: "order_id", Position: 2, DataType: "int"},
			{Name: "total", Position: 3, DataType: "decimal"},
		}},
	}}
}

type row map[string]string // column -> display value; "NULL" means NULL

func user(id int64, name, email string) *object.Record {
	data := map[string]canonical.Value{"id": canonical.Int64(id), "name": val(name), "email": val(email)}
	return &object.Record{Table: "users", PK: map[string]canonical.Value{"id": canonical.Int64(id)}, Data: data}
}

func order(cust, id int64, total string) *object.Record {
	pk := map[string]canonical.Value{"customer_id": canonical.Int64(cust), "order_id": canonical.Int64(id)}
	data := map[string]canonical.Value{"customer_id": canonical.Int64(cust), "order_id": canonical.Int64(id), "total": val(total)}
	return &object.Record{Table: "orders", PK: pk, Data: data}
}

func val(s string) canonical.Value {
	if s == "NULL" {
		return canonical.Null
	}
	return canonical.String(s)
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

func get(t *testing.T, s snapshot.Source, table string, key canonical.PKKey) *object.Record {
	t.Helper()
	tb, err := s.Table(table)
	if err != nil {
		t.Fatal(err)
	}
	if tb == nil {
		return nil
	}
	h, ok := tb.Records[key]
	if !ok {
		return nil
	}
	r, err := s.Record(h)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func k(vals ...canonical.Value) canonical.PKKey { return canonical.MakePKKey(vals) }

func TestMergeLocalOnlyUpstreamOnlySameAndUnchanged(t *testing.T) {
	base := state(t, user(1, "a", "a@x"), user(2, "b", "b@x"), user(3, "c", "c@x"), user(4, "d", "d@x"))
	local := state(t, user(1, "a2", "a@x"), user(2, "b", "b@x"), user(3, "c2", "c@x"), user(4, "d", "d@x"))
	up := state(t, user(1, "a", "a@x"), user(2, "b2", "b@x"), user(3, "c2", "c@x"), user(4, "d", "d@x"))

	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean() {
		t.Fatalf("unexpected conflicts: %+v", res.Conflicts)
	}
	want := map[int64]string{1: "a2", 2: "b2", 3: "c2", 4: "d"}
	for id, name := range want {
		r := get(t, res.State, "users", k(canonical.Int64(id)))
		if r == nil || r.Data["name"] != val(name) {
			t.Errorf("user %d: got %v want %s", id, r, name)
		}
	}
	if res.Stats.LocalOnly != 1 || res.Stats.UpstreamOnly != 1 || res.Stats.Same != 1 || res.Stats.Unchanged != 1 {
		t.Errorf("stats %+v", res.Stats)
	}
}

func TestMergeFieldLevel(t *testing.T) {
	base := state(t, user(42, "Peter", "old@example.com"))
	local := state(t, user(42, "Peter Smith", "old@example.com"))
	up := state(t, user(42, "Peter", "new@example.com"))

	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean() {
		t.Fatalf("unexpected conflicts: %+v", res.Conflicts)
	}
	r := get(t, res.State, "users", k(canonical.Int64(42)))
	if r.Data["name"] != val("Peter Smith") || r.Data["email"] != val("new@example.com") {
		t.Fatalf("merged record %v", r.Data)
	}
	if res.Stats.Merged != 1 {
		t.Errorf("stats %+v", res.Stats)
	}
}

func TestMergeConflictModifyModify(t *testing.T) {
	base := state(t, user(42, "Peter", "p@x"))
	local := state(t, user(42, "Peter Smith", "p@x"))
	up := state(t, user(42, "Péter", "p@x"))

	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %+v", res.Conflicts)
	}
	c := res.Conflicts[0]
	if c.Table != "users" || c.Column != "name" || c.Kind != ModifyModify {
		t.Fatalf("conflict %+v", c)
	}
	if c.Base != val("Peter") || c.Local != val("Peter Smith") || c.Upstream != val("Péter") {
		t.Fatalf("conflict values %+v", c)
	}
	if c.ID() != "users/42" {
		t.Fatalf("id %s", c.ID())
	}
	// Structured JSON for UIs.
	b, _ := json.Marshal(c)
	want := `{"table":"users","primaryKey":{"id":42},"column":"name","kind":"modify/modify","base":"Peter","local":"Peter Smith","upstream":"Péter"}`
	if string(b) != want {
		t.Fatalf("json\n got %s\nwant %s", b, want)
	}
	var sb strings.Builder
	WriteConflicts(&sb, res.Conflicts)
	if !strings.Contains(sb.String(), "BASE:     Peter\n") || !strings.Contains(sb.String(), "UPSTREAM: Péter") {
		t.Fatalf("render:\n%s", sb.String())
	}
}

func TestMergeDeletes(t *testing.T) {
	base := state(t, user(1, "a", "x"), user(2, "b", "x"), user(3, "c", "x"), user(4, "d", "x"))
	// 1: local unchanged, upstream deleted -> deleted
	// 2: local deleted, upstream unchanged -> deleted
	// 3: local modified, upstream deleted -> conflict modify/delete
	// 4: local deleted, upstream modified -> conflict delete/modify
	local := state(t, user(1, "a", "x"), user(3, "c2", "x"))
	up := state(t, user(2, "b", "x"), user(4, "d2", "x"))

	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	if get(t, res.State, "users", k(canonical.Int64(1))) != nil || get(t, res.State, "users", k(canonical.Int64(2))) != nil {
		t.Fatal("1 and 2 should be deleted")
	}
	if len(res.Conflicts) != 2 {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	if res.Conflicts[0].Kind != ModifyDelete || res.Conflicts[0].Key != k(canonical.Int64(3)) {
		t.Errorf("c0 %+v", res.Conflicts[0])
	}
	if res.Conflicts[1].Kind != DeleteModify || res.Conflicts[1].Key != k(canonical.Int64(4)) {
		t.Errorf("c1 %+v", res.Conflicts[1])
	}
	if res.Conflicts[1].Local != nil || res.Conflicts[0].Upstream != nil {
		t.Errorf("record conflicts should carry nil for the deleted side")
	}
}

func TestMergeInserts(t *testing.T) {
	base := state(t)
	local := state(t, user(1, "l", "x"), user(3, "same", "x"), user(4, "x", "a"))
	up := state(t, user(2, "u", "x"), user(3, "same", "x"), user(4, "x", "b"))

	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2, 3} {
		if get(t, res.State, "users", k(canonical.Int64(id))) == nil {
			t.Errorf("user %d missing", id)
		}
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Kind != AddAdd || res.Conflicts[0].Column != "email" {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	if res.Conflicts[0].Base != nil {
		t.Errorf("add/add base should be nil")
	}
}

func TestMergeNoBaseUnrelatedHistoriesIdenticalData(t *testing.T) {
	base := snapshot.Empty()
	local := state(t, user(1, "a", "x"), order(1, 1, "10.00"))
	up := state(t, user(1, "a", "x"), order(1, 1, "10.00"))
	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean() {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	_, hl, _ := local.Hash()
	_, hr, _ := res.State.Hash()
	if hl != hr {
		t.Fatalf("merged state should equal local: %s vs %s", hl, hr)
	}
}

func TestMergeCompositeKeyAndNull(t *testing.T) {
	base := state(t, order(10, 55, "1.00"), order(10, 56, "NULL"))
	local := state(t, order(10, 55, "1.00"), order(10, 56, "2.00"))
	up := state(t, order(10, 55, "NULL"), order(10, 56, "NULL"))
	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean() {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	r55 := get(t, res.State, "orders", k(canonical.Int64(10), canonical.Int64(55)))
	r56 := get(t, res.State, "orders", k(canonical.Int64(10), canonical.Int64(56)))
	if r55.Data["total"] != canonical.Null || r56.Data["total"] != val("2.00") {
		t.Fatalf("got %v / %v", r55.Data, r56.Data)
	}
	// Display of composite ids.
	d, err := diff.Compare(base, res.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Changes) != 2 || d.Changes[0].ID() != "orders/customer_id=10,order_id=55" {
		t.Fatalf("diff %+v", d.Changes)
	}
}

func TestMergeResultIsApplicableSnapshot(t *testing.T) {
	// The merged state must contain every record (so it can be written and
	// applied), including records taken unchanged from either side.
	base := state(t, user(1, "a", "x"))
	local := state(t, user(1, "a", "x"), user(2, "l", "x"))
	up := state(t, user(1, "a", "x"), user(3, "u", "x"))
	res, err := Merge(base, local, up, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := res.State.Table("users")
	if len(tb.Records) != 3 {
		t.Fatalf("records %v", tb.Records)
	}
	for _, h := range tb.Records {
		if _, err := res.State.Record(h); err != nil {
			t.Fatal(err)
		}
	}
}
