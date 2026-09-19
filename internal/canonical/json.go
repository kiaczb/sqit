// Package canonical implements the deterministic JSON encoding that every
// sqit object is hashed over, plus the canonical representation of column
// values and primary keys.
//
// The encoding follows RFC 8785 (JCS) closely: sorted object keys, minimal
// string escaping, no whitespace. Numbers are emitted as pre-formatted text
// (see Number) so 64-bit integers never go through float64.
package canonical

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Raw is pre-encoded canonical JSON that is written verbatim.
type Raw string

// Number is a JSON number in its final textual form.
type Number string

// Encode returns the canonical JSON encoding of v.
//
// Supported types: nil, bool, string, Raw, Number, Value, json.Number,
// int, int64, uint64, float64, []byte (as base64 string), []any, []string,
// map[string]any, map[string]string, map[string]Value, map[string]Raw and
// any type implementing Canonicalizer.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encode(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MustEncode is Encode for values that are known to be encodable.
func MustEncode(v any) []byte {
	b, err := Encode(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Canonicalizer lets object types describe themselves as a plain value tree.
type Canonicalizer interface {
	Canonical() any
}

func encode(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		encodeString(buf, x)
	case Raw:
		if x == "" {
			return fmt.Errorf("canonical: empty Raw value")
		}
		buf.WriteString(string(x))
	case Value:
		if x == "" {
			return fmt.Errorf("canonical: empty Value")
		}
		buf.WriteString(string(x))
	case Number:
		buf.WriteString(string(x))
	case json.Number:
		n, err := NormalizeNumber(string(x))
		if err != nil {
			return err
		}
		buf.WriteString(string(n))
	case int:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		buf.WriteString(strconv.FormatInt(x, 10))
	case uint64:
		buf.WriteString(strconv.FormatUint(x, 10))
	case float64:
		n, err := FormatFloat64(x)
		if err != nil {
			return err
		}
		buf.WriteString(string(n))
	case []byte:
		encodeString(buf, base64Encode(x))
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encode(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case []string:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, e)
		}
		buf.WriteByte(']')
	case []Value:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encode(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		return encodeMap(buf, x, func(k string) any { return x[k] })
	case map[string]string:
		return encodeMap(buf, x, func(k string) any { return x[k] })
	case map[string]Value:
		return encodeMap(buf, x, func(k string) any { return x[k] })
	case map[string]Raw:
		return encodeMap(buf, x, func(k string) any { return x[k] })
	case Canonicalizer:
		return encode(buf, x.Canonical())
	default:
		return fmt.Errorf("canonical: unsupported type %T", v)
	}
	return nil
}

func encodeMap[V any](buf *bytes.Buffer, m map[string]V, get func(string) any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodeString(buf, k)
		buf.WriteByte(':')
		if err := encode(buf, get(k)); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// lessUTF16 orders strings by their UTF-16 code units, as JCS requires.
// For strings without surrogate pairs (all keys sqit produces) this equals
// ordering by code point.
func lessUTF16(a, b string) bool {
	ar, br := []rune(a), []rune(b)
	for i := 0; i < len(ar) && i < len(br); i++ {
		ua, ub := utf16Units(ar[i]), utf16Units(br[i])
		for j := 0; j < len(ua) && j < len(ub); j++ {
			if ua[j] != ub[j] {
				return ua[j] < ub[j]
			}
		}
		if len(ua) != len(ub) {
			return len(ua) < len(ub)
		}
	}
	return len(ar) < len(br)
}

func utf16Units(r rune) []uint16 {
	if r < 0x10000 {
		return []uint16{uint16(r)}
	}
	r -= 0x10000
	return []uint16{uint16(0xD800 + (r >> 10)), uint16(0xDC00 + (r & 0x3FF))}
}

const hexDigits = "0123456789abcdef"

func encodeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				buf.WriteString(`\"`)
			case '\\':
				buf.WriteString(`\\`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			case '\b':
				buf.WriteString(`\b`)
			case '\f':
				buf.WriteString(`\f`)
			default:
				if c < 0x20 {
					buf.WriteString(`\u00`)
					buf.WriteByte(hexDigits[c>>4])
					buf.WriteByte(hexDigits[c&0xF])
				} else {
					buf.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8 byte: emit U+FFFD so the output is valid JSON.
			// Binary data must go through the tagged binary form instead.
			buf.WriteString(string(utf8.RuneError))
		} else {
			buf.WriteString(s[i : i+size])
		}
		i += size
	}
	buf.WriteByte('"')
}

// FormatFloat64 formats f the way ECMAScript Number.prototype.toString does
// (shortest round-trip digits, exponent only outside [1e-7, 1e21)).
func FormatFloat64(f float64) (Number, error) {
	return formatFloat(f, 64)
}

// FormatFloat32 is FormatFloat64 for values that are exactly representable
// as float32 (MySQL FLOAT columns): the shortest digits that round-trip
// through float32 are used.
func FormatFloat32(f float32) (Number, error) {
	return formatFloat(float64(f), 32)
}

func formatFloat(f float64, bits int) (Number, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("canonical: NaN/Inf cannot be represented")
	}
	if f == 0 {
		return "0", nil
	}
	neg := f < 0
	if neg {
		f = -f
	}
	// 'e' format with -1 precision gives the shortest round-trip digits.
	e := strconv.FormatFloat(f, 'e', -1, bits)
	// e looks like "d.ddddde±XX" or "de±XX".
	mant, expStr, _ := bytes.Cut([]byte(e), []byte("e"))
	exp, err := strconv.Atoi(string(expStr))
	if err != nil {
		return "", err
	}
	digits := bytes.ReplaceAll(mant, []byte("."), nil)
	k := len(digits)
	n := exp + 1 // decimal point position relative to digits

	var out bytes.Buffer
	if neg {
		out.WriteByte('-')
	}
	switch {
	case k <= n && n <= 21:
		out.Write(digits)
		for i := 0; i < n-k; i++ {
			out.WriteByte('0')
		}
	case 0 < n && n <= 21:
		out.Write(digits[:n])
		out.WriteByte('.')
		out.Write(digits[n:])
	case -6 < n && n <= 0:
		out.WriteString("0.")
		for i := 0; i < -n; i++ {
			out.WriteByte('0')
		}
		out.Write(digits)
	default:
		out.WriteByte(digits[0])
		if k > 1 {
			out.WriteByte('.')
			out.Write(digits[1:])
		}
		out.WriteByte('e')
		if n-1 >= 0 {
			out.WriteByte('+')
		}
		out.WriteString(strconv.Itoa(n - 1))
	}
	return Number(out.String()), nil
}

// NormalizeNumber canonicalises a JSON number literal. Integer literals are
// kept exact (leading zeros / "-0" fixed up); anything with a fraction or
// exponent is parsed as float64 and re-emitted.
func NormalizeNumber(s string) (Number, error) {
	if s == "" {
		return "", fmt.Errorf("canonical: empty number")
	}
	isInt := true
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c < '0' || c > '9' {
			isInt = false
			break
		}
	}
	if isInt {
		return NormalizeInteger(s)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("canonical: bad number %q: %w", s, err)
	}
	return FormatFloat64(f)
}

// NormalizeInteger canonicalises a decimal integer string.
func NormalizeInteger(s string) (Number, error) {
	neg := false
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return "", fmt.Errorf("canonical: empty integer")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return "", fmt.Errorf("canonical: bad integer %q", s)
		}
	}
	i := 0
	for i < len(s)-1 && s[i] == '0' {
		i++
	}
	s = s[i:]
	if s == "0" {
		return "0", nil
	}
	if neg {
		return Number("-" + s), nil
	}
	return Number(s), nil
}

// NormalizeJSON parses arbitrary JSON text and returns its canonical form.
func NormalizeJSON(text []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical: invalid JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("canonical: trailing data after JSON value")
	}
	return Encode(v)
}
