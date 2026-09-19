package canonical

import (
	"testing"
)

func TestEncodeSortsKeysAndEscapes(t *testing.T) {
	got := string(MustEncode(map[string]any{
		"b":     1,
		"a":     "x\"y\\z\n\x01<&>",
		"é":     nil,
		"array": []any{true, false, int64(-5), uint64(18446744073709551615)},
	}))
	want := `{"a":"x` + q + `y` + bs + bs + `z` + bs + `n` + bs + `u0001<&>","array":[true,false,-5,18446744073709551615],"b":1,"é":null}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	m := map[string]Value{"z": "1", "y": `"a"`, "x": Null}
	a := string(MustEncode(m))
	for i := 0; i < 50; i++ {
		if b := string(MustEncode(m)); b != a {
			t.Fatalf("non-deterministic: %s vs %s", a, b)
		}
	}
	if a != `{"x":null,"y":"a","z":1}` {
		t.Fatalf("unexpected %s", a)
	}
}

func TestFormatFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{-1.5, "-1.5"},
		{3.14, "3.14"},
		{sum(0.1, 0.2), "0.30000000000000004"},
		{1e21, "1e+21"},
		{1e20, "100000000000000000000"},
		{123456789012345680000, "123456789012345680000"},
		{0.000001, "0.000001"},
		{0.0000001, "1e-7"},
		{1.5e-10, "1.5e-10"},
		{5e-324, "5e-324"},
		{1.7976931348623157e308, "1.7976931348623157e+308"},
	}
	for _, c := range cases {
		got, err := FormatFloat64(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("FormatFloat64(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestFormatFloat32(t *testing.T) {
	// 3.14 as float32 is 3.1400001049041748 as float64; the float32-shortest
	// form must still be 3.14.
	got, err := FormatFloat32(float32(3.14))
	if err != nil || string(got) != "3.14" {
		t.Fatalf("got %s, %v", got, err)
	}
	got, _ = FormatFloat32(float32(0.1))
	if string(got) != "0.1" {
		t.Fatalf("got %s", got)
	}
}

func TestNormalizeNumber(t *testing.T) {
	cases := map[string]string{
		"007":                  "7",
		"-0":                   "0",
		"-007":                 "-7",
		"0":                    "0",
		"1.50":                 "1.5",
		"1e3":                  "1000",
		"1E+2":                 "100",
		"-2.5e-3":              "-0.0025",
		"18446744073709551615": "18446744073709551615",
	}
	for in, want := range cases {
		got, err := NormalizeNumber(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if string(got) != want {
			t.Errorf("NormalizeNumber(%s) = %s, want %s", in, got, want)
		}
	}
	if _, err := NormalizeNumber("abc"); err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeJSON(t *testing.T) {
	in := []byte(` { "z" : [1.0, 2.50, {"b":null,"a":"x"}], "a" : 1e2, "m": "é" } `)
	got, err := NormalizeJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":100,"m":"é","z":[1,2.5,{"a":"x","b":null}]}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// Re-normalising is idempotent.
	again, _ := NormalizeJSON(got)
	if string(again) != want {
		t.Fatalf("not idempotent: %s", again)
	}
}

func TestValueKinds(t *testing.T) {
	if String("abc").Kind() != KindString || Null.Kind() != KindNull {
		t.Fatal("kind")
	}
	if Int64(-3).Kind() != KindNumber || Uint64(3).Kind() != KindNumber {
		t.Fatal("kind number")
	}
	b := Binary([]byte{0, 1, 2, 255})
	if b.Kind() != KindBinary {
		t.Fatalf("kind binary: %s", b)
	}
	if bs, ok := b.AsBinary(); !ok || len(bs) != 4 || bs[3] != 255 {
		t.Fatalf("AsBinary: %v %v", bs, ok)
	}
	if s, ok := String("hé\"llo").AsString(); !ok || s != "hé\"llo" {
		t.Fatalf("AsString: %q", s)
	}
	if Null.Display() != "NULL" || String("x").Display() != "x" || Int64(5).Display() != "5" {
		t.Fatal("display")
	}
	if String("a\nb").Display() != `"a\nb"` {
		t.Fatalf("display control: %s", String("a\nb").Display())
	}
}

func TestValueRoundTrip(t *testing.T) {
	type rec struct {
		Data map[string]Value `json:"data"`
	}
	in := rec{Data: map[string]Value{"a": Null, "b": String("x"), "c": Int64(7), "d": Binary([]byte("hi"))}}
	enc := MustEncode(map[string]any{"data": in.Data})
	var out rec
	if err := unmarshal(enc, &out); err != nil {
		t.Fatal(err)
	}
	for k, v := range in.Data {
		if out.Data[k] != v {
			t.Errorf("%s: %s != %s", k, out.Data[k], v)
		}
	}
}

func TestPKKey(t *testing.T) {
	k := MakePKKey([]Value{Int64(10), String("a,b"), Null})
	if string(k) != `[10,"a,b",null]` {
		t.Fatalf("key %s", k)
	}
	vals, err := k.Values()
	if err != nil || len(vals) != 3 || vals[0] != "10" || vals[1] != `"a,b"` || vals[2] != Null {
		t.Fatalf("values %v %v", vals, err)
	}
	if d := DisplayPK("users", []string{"id"}, MakePKKey([]Value{Int64(42)})); d != "users/42" {
		t.Fatalf("display %s", d)
	}
	if d := DisplayPK("orders", []string{"customer_id", "order_id"}, MakePKKey([]Value{Int64(10), Int64(55)})); d != "orders/customer_id=10,order_id=55" {
		t.Fatalf("display %s", d)
	}
	// Composite keys with separators in the values do not collide.
	a := MakePKKey([]Value{String("a,b"), String("c")})
	b := MakePKKey([]Value{String("a"), String("b,c")})
	if a == b {
		t.Fatal("collision")
	}
}

func TestSortPKKeys(t *testing.T) {
	keys := []PKKey{
		MakePKKey([]Value{Int64(10)}),
		MakePKKey([]Value{Int64(9)}),
		MakePKKey([]Value{Int64(-1)}),
		MakePKKey([]Value{Int64(100)}),
	}
	SortPKKeys(keys)
	want := []string{"[-1]", "[9]", "[10]", "[100]"}
	for i, k := range keys {
		if string(k) != want[i] {
			t.Fatalf("order %v", keys)
		}
	}
}
