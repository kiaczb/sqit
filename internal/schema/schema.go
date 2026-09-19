// Package schema models the tracked tables' structure, diffs two schemas and
// decides whether two schemas are compatible for a data merge.
package schema

import (
	"fmt"
	"sort"
	"strings"
)

// Column describes one column as read from information_schema.COLUMNS.
type Column struct {
	Name       string  `json:"name"`
	Position   int     `json:"position"`
	ColumnType string  `json:"columnType"` // e.g. "varchar(255)", "int unsigned"
	DataType   string  `json:"dataType"`   // e.g. "varchar", "int"
	Nullable   bool    `json:"nullable"`
	Default    *string `json:"default"`
	Extra      string  `json:"extra"`
	Charset    *string `json:"charset"`
	Collation  *string `json:"collation"`
}

// Table describes one tracked table.
type Table struct {
	Name       string   `json:"name"`
	PrimaryKey []string `json:"primaryKey"`
	Columns    []Column `json:"columns"`
}

// Schema is the set of tracked tables.
type Schema struct {
	Tables []Table `json:"tables"`
}

// Column returns the column with the given name.
func (t *Table) Column(name string) *Column {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}

// ColumnNames returns the column names in ordinal order.
func (t *Table) ColumnNames() []string {
	out := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		out[i] = c.Name
	}
	return out
}

// IsPK reports whether the column is part of the primary key.
func (t *Table) IsPK(col string) bool {
	for _, p := range t.PrimaryKey {
		if p == col {
			return true
		}
	}
	return false
}

// Table returns the table with the given name.
func (s *Schema) Table(name string) *Table {
	if s == nil {
		return nil
	}
	for i := range s.Tables {
		if s.Tables[i].Name == name {
			return &s.Tables[i]
		}
	}
	return nil
}

// TableNames returns the sorted table names.
func (s *Schema) TableNames() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.Tables))
	for i, t := range s.Tables {
		out[i] = t.Name
	}
	sort.Strings(out)
	return out
}

// Normalize sorts tables by name and columns by position so the canonical
// encoding does not depend on the read order.
func (s *Schema) Normalize() {
	sort.Slice(s.Tables, func(i, j int) bool { return s.Tables[i].Name < s.Tables[j].Name })
	for i := range s.Tables {
		cols := s.Tables[i].Columns
		sort.Slice(cols, func(a, b int) bool { return cols[a].Position < cols[b].Position })
	}
}

// Canonical returns the plain value tree used for hashing.
func (s *Schema) Canonical() any {
	if s == nil {
		return map[string]any{"tables": []any{}}
	}
	tables := make([]any, 0, len(s.Tables))
	for _, t := range s.Tables {
		cols := make([]any, 0, len(t.Columns))
		for _, c := range t.Columns {
			cols = append(cols, map[string]any{
				"name":       c.Name,
				"position":   c.Position,
				"columnType": c.ColumnType,
				"dataType":   c.DataType,
				"nullable":   c.Nullable,
				"default":    strPtr(c.Default),
				"extra":      c.Extra,
				"charset":    strPtr(c.Charset),
				"collation":  strPtr(c.Collation),
			})
		}
		pk := make([]any, len(t.PrimaryKey))
		for i, p := range t.PrimaryKey {
			pk[i] = p
		}
		tables = append(tables, map[string]any{
			"name":       t.Name,
			"primaryKey": pk,
			"columns":    cols,
		})
	}
	return map[string]any{"tables": tables}
}

func strPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// ChangeKind enumerates schema differences.
type ChangeKind string

const (
	TableAdded      ChangeKind = "added table"
	TableDeleted    ChangeKind = "deleted table"
	ColumnAdded     ChangeKind = "added column"
	ColumnDeleted   ChangeKind = "deleted column"
	ColumnType      ChangeKind = "changed column type"
	ColumnNullable  ChangeKind = "changed nullable"
	ColumnDefault   ChangeKind = "changed default"
	ColumnExtra     ChangeKind = "changed extra"
	ColumnCollation ChangeKind = "changed collation"
	ColumnPosition  ChangeKind = "changed position"
	PrimaryKey      ChangeKind = "changed primary key"
)

// Change is one schema difference.
type Change struct {
	Kind   ChangeKind `json:"kind"`
	Table  string     `json:"table"`
	Column string     `json:"column,omitempty"`
	From   string     `json:"from,omitempty"`
	To     string     `json:"to,omitempty"`
}

func (c Change) String() string {
	s := string(c.Kind) + ": " + c.Table
	if c.Column != "" {
		s += "." + c.Column
	}
	if c.From != "" || c.To != "" {
		s += fmt.Sprintf(" (%s -> %s)", orEmpty(c.From), orEmpty(c.To))
	}
	return s
}

func orEmpty(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

// Diff lists the changes from a to b.
func Diff(a, b *Schema) []Change {
	var out []Change
	names := union(a.TableNames(), b.TableNames())
	for _, name := range names {
		ta, tb := a.Table(name), b.Table(name)
		switch {
		case ta == nil:
			out = append(out, Change{Kind: TableAdded, Table: name})
		case tb == nil:
			out = append(out, Change{Kind: TableDeleted, Table: name})
		default:
			out = append(out, diffTable(ta, tb)...)
		}
	}
	return out
}

func diffTable(a, b *Table) []Change {
	var out []Change
	if strings.Join(a.PrimaryKey, ",") != strings.Join(b.PrimaryKey, ",") {
		out = append(out, Change{Kind: PrimaryKey, Table: a.Name,
			From: strings.Join(a.PrimaryKey, ","), To: strings.Join(b.PrimaryKey, ",")})
	}
	cols := union(a.ColumnNames(), b.ColumnNames())
	for _, name := range cols {
		ca, cb := a.Column(name), b.Column(name)
		switch {
		case ca == nil:
			out = append(out, Change{Kind: ColumnAdded, Table: a.Name, Column: name, To: cb.ColumnType})
		case cb == nil:
			out = append(out, Change{Kind: ColumnDeleted, Table: a.Name, Column: name, From: ca.ColumnType})
		default:
			if ca.ColumnType != cb.ColumnType {
				out = append(out, Change{Kind: ColumnType, Table: a.Name, Column: name, From: ca.ColumnType, To: cb.ColumnType})
			}
			if ca.Nullable != cb.Nullable {
				out = append(out, Change{Kind: ColumnNullable, Table: a.Name, Column: name, From: nullable(ca.Nullable), To: nullable(cb.Nullable)})
			}
			if !eqPtr(ca.Default, cb.Default) {
				out = append(out, Change{Kind: ColumnDefault, Table: a.Name, Column: name, From: defaultStr(ca.Default), To: defaultStr(cb.Default)})
			}
			if ca.Extra != cb.Extra {
				out = append(out, Change{Kind: ColumnExtra, Table: a.Name, Column: name, From: ca.Extra, To: cb.Extra})
			}
			if !eqPtr(ca.Collation, cb.Collation) {
				out = append(out, Change{Kind: ColumnCollation, Table: a.Name, Column: name, From: defaultStr(ca.Collation), To: defaultStr(cb.Collation)})
			}
			if ca.Position != cb.Position {
				out = append(out, Change{Kind: ColumnPosition, Table: a.Name, Column: name, From: fmt.Sprint(ca.Position), To: fmt.Sprint(cb.Position)})
			}
		}
	}
	return out
}

// Incompatibilities returns the subset of differences between local and
// upstream that make a data merge unsafe: different table sets, different
// column sets, different base data types, different nullability or a
// different primary key. Display width, defaults, charset, collation, EXTRA
// and column order are tolerated.
func Incompatibilities(local, upstream *Schema) []Change {
	var out []Change
	for _, name := range union(local.TableNames(), upstream.TableNames()) {
		tl, tu := local.Table(name), upstream.Table(name)
		switch {
		case tl == nil:
			out = append(out, Change{Kind: TableAdded, Table: name})
			continue
		case tu == nil:
			out = append(out, Change{Kind: TableDeleted, Table: name})
			continue
		}
		if strings.Join(tl.PrimaryKey, ",") != strings.Join(tu.PrimaryKey, ",") {
			out = append(out, Change{Kind: PrimaryKey, Table: name,
				From: strings.Join(tl.PrimaryKey, ","), To: strings.Join(tu.PrimaryKey, ",")})
		}
		for _, col := range union(tl.ColumnNames(), tu.ColumnNames()) {
			cl, cu := tl.Column(col), tu.Column(col)
			switch {
			case cl == nil:
				out = append(out, Change{Kind: ColumnAdded, Table: name, Column: col, To: cu.ColumnType})
			case cu == nil:
				out = append(out, Change{Kind: ColumnDeleted, Table: name, Column: col, From: cl.ColumnType})
			default:
				if cl.DataType != cu.DataType {
					out = append(out, Change{Kind: ColumnType, Table: name, Column: col, From: cl.ColumnType, To: cu.ColumnType})
				}
				if cl.Nullable != cu.Nullable {
					out = append(out, Change{Kind: ColumnNullable, Table: name, Column: col, From: nullable(cl.Nullable), To: nullable(cu.Nullable)})
				}
			}
		}
	}
	return out
}

func nullable(b bool) string {
	if b {
		return "NULL"
	}
	return "NOT NULL"
}

func defaultStr(p *string) string {
	if p == nil {
		return "NULL"
	}
	return "'" + *p + "'"
}

func eqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
