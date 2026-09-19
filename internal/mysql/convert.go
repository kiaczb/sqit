// Package mysql is the only layer that talks to MySQL: it reads the schema
// from information_schema, reads rows into canonical records, and applies
// planned operations inside a transaction.
package mysql

import (
	"fmt"
	"strconv"
	"strings"

	"sqit/internal/canonical"
	"sqit/internal/schema"
)

// Family classifies MySQL data types by how sqit canonicalises them.
type Family int

const (
	FamilyString Family = iota
	FamilyInt
	FamilyBit
	FamilyFloat
	FamilyDouble
	FamilyDecimal
	FamilyTemporal
	FamilyBinary
	FamilyJSON
)

// FamilyOf maps a column to its canonicalisation family.
func FamilyOf(c *schema.Column) Family {
	dt := strings.ToLower(c.DataType)
	switch dt {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
		return FamilyInt
	case "bit":
		return FamilyBit
	case "float":
		return FamilyFloat
	case "double", "real", "double precision":
		return FamilyDouble
	case "decimal", "numeric", "dec", "fixed":
		return FamilyDecimal
	case "date", "datetime", "timestamp", "time", "year":
		return FamilyTemporal
	case "json":
		return FamilyJSON
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob",
		"geometry", "point", "linestring", "polygon", "multipoint",
		"multilinestring", "multipolygon", "geometrycollection", "geomcollection":
		return FamilyBinary
	}
	if c.Charset != nil && strings.EqualFold(*c.Charset, "binary") {
		return FamilyBinary
	}
	return FamilyString
}

// FromText converts a value received over the MySQL text protocol into its
// canonical form. raw == nil means SQL NULL.
func FromText(c *schema.Column, raw []byte) (canonical.Value, error) {
	if raw == nil {
		return canonical.Null, nil
	}
	switch FamilyOf(c) {
	case FamilyInt:
		v, err := canonical.Int(string(raw))
		if err != nil {
			return "", fmt.Errorf("column %s: %w", c.Name, err)
		}
		return v, nil
	case FamilyBit:
		// BIT values arrive as big-endian binary strings.
		if len(raw) > 8 {
			return "", fmt.Errorf("column %s: BIT value wider than 64 bits", c.Name)
		}
		var u uint64
		for _, b := range raw {
			u = u<<8 | uint64(b)
		}
		return canonical.Uint64(u), nil
	case FamilyFloat:
		f, err := strconv.ParseFloat(string(raw), 32)
		if err != nil {
			return "", fmt.Errorf("column %s: %w", c.Name, err)
		}
		return canonical.Float32(float32(f))
	case FamilyDouble:
		f, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return "", fmt.Errorf("column %s: %w", c.Name, err)
		}
		return canonical.Float64(f)
	case FamilyDecimal:
		return canonical.String(normalizeDecimal(string(raw))), nil
	case FamilyTemporal:
		return canonical.String(string(raw)), nil
	case FamilyJSON:
		n, err := canonical.NormalizeJSON(raw)
		if err != nil {
			return "", fmt.Errorf("column %s: %w", c.Name, err)
		}
		return canonical.String(string(n)), nil
	case FamilyBinary:
		return canonical.Binary(raw), nil
	default:
		return canonical.String(string(raw)), nil
	}
}

// normalizeDecimal keeps the server's fixed-scale text but folds negative
// zero ("-0.00") into "0.00".
func normalizeDecimal(s string) string {
	if !strings.HasPrefix(s, "-") {
		return s
	}
	for _, ch := range s[1:] {
		if ch != '0' && ch != '.' {
			return s
		}
	}
	return s[1:]
}

// ToArg converts a canonical value into a driver argument for the column.
func ToArg(c *schema.Column, v canonical.Value) (any, error) {
	if v.IsNull() || v == "" {
		return nil, nil
	}
	switch FamilyOf(c) {
	case FamilyInt:
		if v.Kind() != canonical.KindNumber {
			return nil, fmt.Errorf("column %s: expected number, got %s", c.Name, v)
		}
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i, nil
		}
		if u, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			return u, nil
		}
		return nil, fmt.Errorf("column %s: integer out of range: %s", c.Name, v)
	case FamilyBit:
		u, err := strconv.ParseUint(string(v), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("column %s: bad BIT value %s", c.Name, v)
		}
		return u, nil
	case FamilyFloat, FamilyDouble:
		if v.Kind() != canonical.KindNumber {
			return nil, fmt.Errorf("column %s: expected number, got %s", c.Name, v)
		}
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", c.Name, err)
		}
		return f, nil
	case FamilyBinary:
		b, ok := v.AsBinary()
		if !ok {
			// Tolerate a plain string (e.g. a column that changed to binary).
			if s, ok := v.AsString(); ok {
				return []byte(s), nil
			}
			return nil, fmt.Errorf("column %s: expected binary, got %s", c.Name, v)
		}
		return b, nil
	default:
		if s, ok := v.AsString(); ok {
			return s, nil
		}
		if v.Kind() == canonical.KindNumber {
			return string(v), nil
		}
		return nil, fmt.Errorf("column %s: cannot convert %s", c.Name, v)
	}
}

// Quote quotes an identifier.
func Quote(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}
