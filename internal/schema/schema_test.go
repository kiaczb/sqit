package schema

import (
	"testing"

	"sqit/internal/canonical"
)

func str(s string) *string { return &s }

func base() *Schema {
	return &Schema{Tables: []Table{
		{Name: "users", PrimaryKey: []string{"id"}, Columns: []Column{
			{Name: "id", Position: 1, ColumnType: "int", DataType: "int"},
			{Name: "name", Position: 2, ColumnType: "varchar(100)", DataType: "varchar", Nullable: false},
			{Name: "email", Position: 3, ColumnType: "varchar(100)", DataType: "varchar", Nullable: true, Default: str("x")},
		}},
		{Name: "gone", PrimaryKey: []string{"id"}, Columns: []Column{{Name: "id", Position: 1, ColumnType: "int", DataType: "int"}}},
	}}
}

func TestDiff(t *testing.T) {
	a := base()
	b := base()
	b.Tables = b.Tables[:1] // drop "gone"
	b.Tables[0].Columns[1].ColumnType = "varchar(200)"
	b.Tables[0].Columns[2].Nullable = false
	b.Tables[0].Columns[2].Default = nil
	b.Tables[0].Columns = append(b.Tables[0].Columns, Column{Name: "age", Position: 4, ColumnType: "int", DataType: "int"})
	b.Tables = append(b.Tables, Table{Name: "new", PrimaryKey: []string{"a", "b"}})
	b.Tables[0].PrimaryKey = []string{"email"}

	changes := Diff(a, b)
	got := map[string]bool{}
	for _, c := range changes {
		got[c.String()] = true
	}
	for _, want := range []string{
		"deleted table: gone",
		"added table: new",
		"changed column type: users.name (varchar(100) -> varchar(200))",
		"changed nullable: users.email (NULL -> NOT NULL)",
		"changed default: users.email ('x' -> NULL)",
		"added column: users.age (<none> -> int)",
		"changed primary key: users (id -> email)",
	} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if len(Diff(a, a)) != 0 {
		t.Error("self diff should be empty")
	}
}

func TestIncompatibilitiesTolerateCosmetics(t *testing.T) {
	a := base()
	b := base()
	// Display width, default, collation, extra and order differences are OK.
	b.Tables[0].Columns[0].ColumnType = "int(11)"
	b.Tables[0].Columns[2].Default = nil
	b.Tables[0].Columns[2].Collation = str("utf8mb4_bin")
	b.Tables[0].Columns[0].Extra = "auto_increment"
	b.Tables[0].Columns[1].Position, b.Tables[0].Columns[2].Position = 3, 2
	if inc := Incompatibilities(a, b); len(inc) != 0 {
		t.Fatalf("unexpected incompatibilities %v", inc)
	}
	// Data type, nullability, PK and column set differences are not.
	c := base()
	c.Tables[0].Columns[1].DataType = "text"
	c.Tables[0].Columns[2].Nullable = false
	c.Tables[0].PrimaryKey = []string{"id", "name"}
	c.Tables[1].Columns = append(c.Tables[1].Columns, Column{Name: "x", Position: 2, DataType: "int"})
	inc := Incompatibilities(a, c)
	if len(inc) != 4 {
		t.Fatalf("want 4 incompatibilities, got %v", inc)
	}
}

func TestCanonicalIsStable(t *testing.T) {
	a := base()
	b := base()
	// Reverse order; Normalize must restore it.
	b.Tables[0], b.Tables[1] = b.Tables[1], b.Tables[0]
	b.Tables[1].Columns[0], b.Tables[1].Columns[2] = b.Tables[1].Columns[2], b.Tables[1].Columns[0]
	a.Normalize()
	b.Normalize()
	ea, _ := canonical.Encode(a)
	eb, _ := canonical.Encode(b)
	if string(ea) != string(eb) {
		t.Fatalf("encodings differ:\n%s\n%s", ea, eb)
	}
}
