package canonical

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Value is the canonical JSON text of one column value: "null", a number,
// a JSON string, or the tagged binary object {"$b64":"..."}.
//
// Two values are equal exactly when their text is equal.
type Value string

// Null is the canonical NULL.
const Null Value = "null"

// IsNull reports whether v is NULL.
func (v Value) IsNull() bool { return v == Null }

// String returns v as a JSON string value.
func String(s string) Value {
	return Value(MustEncode(s))
}

// Int returns v as a JSON number from a decimal integer string.
func Int(s string) (Value, error) {
	n, err := NormalizeInteger(s)
	if err != nil {
		return "", err
	}
	return Value(n), nil
}

// Int64 returns v as a JSON number.
func Int64(i int64) Value { return Value(strconv.FormatInt(i, 10)) }

// Uint64 returns v as a JSON number.
func Uint64(u uint64) Value { return Value(strconv.FormatUint(u, 10)) }

// Float64 returns v as a canonical JSON number.
func Float64(f float64) (Value, error) {
	n, err := FormatFloat64(f)
	return Value(n), err
}

// Float32 returns v as a canonical JSON number using float32 precision.
func Float32(f float32) (Value, error) {
	n, err := FormatFloat32(f)
	return Value(n), err
}

// Binary returns the tagged binary form of b.
func Binary(b []byte) Value {
	return Value(`{"$b64":"` + base64Encode(b) + `"}`)
}

func base64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// Kind classifies a canonical value.
type Kind int

const (
	KindNull Kind = iota
	KindNumber
	KindString
	KindBinary
	KindOther // any other JSON (should not occur in records)
)

// Kind returns the classification of v.
func (v Value) Kind() Kind {
	if v == "" || v == Null {
		return KindNull
	}
	switch v[0] {
	case '"':
		return KindString
	case '{':
		if strings.HasPrefix(string(v), `{"$b64":`) {
			return KindBinary
		}
		return KindOther
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return KindNumber
	}
	return KindOther
}

// AsString returns the decoded string for KindString values.
func (v Value) AsString() (string, bool) {
	if v.Kind() != KindString {
		return "", false
	}
	var s string
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return "", false
	}
	return s, true
}

// AsBinary returns the decoded bytes for KindBinary values.
func (v Value) AsBinary() ([]byte, bool) {
	if v.Kind() != KindBinary {
		return nil, false
	}
	var t struct {
		B64 string `json:"$b64"`
	}
	if err := json.Unmarshal([]byte(v), &t); err != nil {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(t.B64)
	if err != nil {
		return nil, false
	}
	return b, true
}

// Display renders v for humans: strings unquoted, NULL as NULL, binary as a
// short description. Strings containing control characters are quoted.
func (v Value) Display() string {
	switch v.Kind() {
	case KindNull:
		return "NULL"
	case KindString:
		s, _ := v.AsString()
		if strings.ContainsAny(s, "\n\r\t\x00") {
			return strconv.Quote(s)
		}
		return s
	case KindBinary:
		b, _ := v.AsBinary()
		if len(b) <= 24 {
			return fmt.Sprintf("<binary %d bytes: %x>", len(b), b)
		}
		return fmt.Sprintf("<binary %d bytes: %x...>", len(b), b[:24])
	default:
		return string(v)
	}
}

// MarshalJSON emits the raw canonical text.
func (v Value) MarshalJSON() ([]byte, error) {
	if v == "" {
		return []byte("null"), nil
	}
	return []byte(v), nil
}

// UnmarshalJSON stores the raw text after re-canonicalising it, so values
// read back from disk compare equal to freshly computed ones.
func (v *Value) UnmarshalJSON(b []byte) error {
	c, err := NormalizeJSON(b)
	if err != nil {
		return err
	}
	*v = Value(c)
	return nil
}

// PKKey is the manifest key of a record: the canonical JSON array of its
// primary key values in primary-key column order, e.g. `[42]` or `[10,"x"]`.
type PKKey string

// MakePKKey builds the key from values already in PK column order.
func MakePKKey(values []Value) PKKey {
	return PKKey(MustEncode(values))
}

// Values decodes the key back into its component values.
func (k PKKey) Values() ([]Value, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(k), &raw); err != nil {
		return nil, fmt.Errorf("canonical: bad pk key %q: %w", string(k), err)
	}
	out := make([]Value, len(raw))
	for i, r := range raw {
		c, err := NormalizeJSON(r)
		if err != nil {
			return nil, err
		}
		out[i] = Value(c)
	}
	return out, nil
}

// SortPKKeys orders keys deterministically: numerically where every
// component is a plain integer, otherwise by text. Used for display only;
// object encoding always sorts by JCS key order.
func SortPKKeys(keys []PKKey) {
	sort.Slice(keys, func(i, j int) bool { return pkLess(keys[i], keys[j]) })
}

func pkLess(a, b PKKey) bool {
	av, aerr := a.Values()
	bv, berr := b.Values()
	if aerr != nil || berr != nil {
		return a < b
	}
	for i := 0; i < len(av) && i < len(bv); i++ {
		if av[i] == bv[i] {
			continue
		}
		ai, aok := parseInt(av[i])
		bi, bok := parseInt(bv[i])
		if aok && bok {
			return ai < bi
		}
		return av[i] < bv[i]
	}
	return len(av) < len(bv)
}

func parseInt(v Value) (int64, bool) {
	if v.Kind() != KindNumber {
		return 0, false
	}
	i, err := strconv.ParseInt(string(v), 10, 64)
	return i, err == nil
}

// DisplayPK renders a record identifier as "table/42" or
// "table/customer_id=10,order_id=55".
func DisplayPK(table string, pkColumns []string, key PKKey) string {
	vals, err := key.Values()
	if err != nil || len(vals) != len(pkColumns) {
		return table + "/" + string(key)
	}
	if len(vals) == 1 {
		return table + "/" + vals[0].Display()
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = pkColumns[i] + "=" + v.Display()
	}
	return table + "/" + strings.Join(parts, ",")
}
