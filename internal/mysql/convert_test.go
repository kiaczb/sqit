package mysql

import (
	"testing"

	"sqit/internal/canonical"
	"sqit/internal/schema"
)

func col(dataType string, charset ...string) *schema.Column {
	c := &schema.Column{Name: "c", DataType: dataType}
	if len(charset) > 0 {
		cs := charset[0]
		c.Charset = &cs
	}
	return c
}

func TestFromText(t *testing.T) {
	cases := []struct {
		typ  string
		raw  string
		want string
	}{
		{"int", "42", "42"},
		{"int", "-0", "0"},
		{"bigint", "18446744073709551615", "18446744073709551615"},
		{"tinyint", "1", "1"},
		{"float", "3.14", "3.14"},
		{"float", "3.1400001", "3.14"}, // float32 shortest form
		{"double", "0.1", "0.1"},
		{"double", "1e300", "1e+300"},
		{"decimal", "12.50", `"12.50"`},
		{"decimal", "-0.00", `"0.00"`},
		{"decimal", "-1.00", `"-1.00"`},
		{"datetime", "2024-01-15 10:30:00.123", `"2024-01-15 10:30:00.123"`},
		{"time", "-838:59:59", `"-838:59:59"`},
		{"year", "2024", `"2024"`},
		{"varchar", "héllo", `"héllo"`},
		{"json", `{"b":1,"a":[1.0,{"z":null}]}`, `"{\"a\":[1,{\"z\":null}],\"b\":1}"`},
		{"enum", "x", `"x"`},
	}
	for _, c := range cases {
		got, err := FromText(col(c.typ), []byte(c.raw))
		if err != nil {
			t.Errorf("%s %q: %v", c.typ, c.raw, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s %q: got %s want %s", c.typ, c.raw, got, c.want)
		}
	}
	if v, _ := FromText(col("varchar"), nil); v != canonical.Null {
		t.Errorf("nil -> %s", v)
	}
	if v, _ := FromText(col("bit"), []byte{0x01, 0x02}); v != "258" {
		t.Errorf("bit -> %s", v)
	}
	if v, _ := FromText(col("varbinary"), []byte{0, 255}); v != `{"$b64":"AP8="}` {
		t.Errorf("binary -> %s", v)
	}
	if v, _ := FromText(col("varchar", "binary"), []byte("ab")); v != `{"$b64":"YWI="}` {
		t.Errorf("varchar binary -> %s", v)
	}
	if _, err := FromText(col("json"), []byte("{bad")); err == nil {
		t.Error("expected json error")
	}
}

func TestToArgRoundTrip(t *testing.T) {
	check := func(typ string, v canonical.Value, want any) {
		t.Helper()
		got, err := ToArg(col(typ), v)
		if err != nil {
			t.Errorf("%s %s: %v", typ, v, err)
			return
		}
		switch w := want.(type) {
		case []byte:
			g, ok := got.([]byte)
			if !ok || string(g) != string(w) {
				t.Errorf("%s %s: got %#v want %#v", typ, v, got, want)
			}
		default:
			if got != want {
				t.Errorf("%s %s: got %#v (%T) want %#v (%T)", typ, v, got, got, want, want)
			}
		}
	}
	check("int", canonical.Null, nil)
	check("int", "42", int64(42))
	check("bigint", "18446744073709551615", uint64(18446744073709551615))
	check("bit", "5", uint64(5))
	check("double", "0.1", 0.1)
	check("decimal", `"12.50"`, "12.50")
	check("varchar", `"héllo"`, "héllo")
	check("json", `"{\"a\":1}"`, `{"a":1}`)
	check("varbinary", `{"$b64":"AP8="}`, []byte{0, 255})
	check("datetime", `"2024-01-15 10:30:00"`, "2024-01-15 10:30:00")
	if _, err := ToArg(col("int"), `"abc"`); err == nil {
		t.Error("expected error for non-number into int")
	}
}

func TestQuote(t *testing.T) {
	if Quote("a`b") != "`a``b`" {
		t.Fatal(Quote("a`b"))
	}
}

func TestAssertLoopback(t *testing.T) {
	ok := []string{
		"u:p@tcp(127.0.0.1:3306)/db",
		"u:p@tcp(localhost:3307)/db",
		"u:p@tcp([::1]:3306)/db",
		"u:p@unix(/var/run/mysqld.sock)/db",
		"u:p@/db", // driver default: tcp 127.0.0.1:3306
	}
	for _, dsn := range ok {
		if err := AssertLoopback(dsn); err != nil {
			t.Errorf("%s: unexpected %v", dsn, err)
		}
	}
	bad := []string{
		"u:p@tcp(test-db.corp.example:3306)/db",
		"u:p@tcp(10.0.0.5:3306)/db",
		"u:p@tcp(mysql:3306)/db",
	}
	for _, dsn := range bad {
		if err := AssertLoopback(dsn); err == nil {
			t.Errorf("%s: expected refusal", dsn)
		}
	}
}
