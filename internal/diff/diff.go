// Package diff compares two database states record by record and field by
// field. It is independent of MySQL.
package diff

import (
	"fmt"
	"io"
	"sort"

	"sqit/internal/canonical"
	"sqit/internal/object"
	"sqit/internal/snapshot"
)

// Kind is the kind of a record change.
type Kind string

const (
	Added    Kind = "added"
	Modified Kind = "modified"
	Deleted  Kind = "deleted"
)

// FieldChange is one changed column of a modified record.
type FieldChange struct {
	Column string          `json:"column"`
	Old    canonical.Value `json:"old"`
	New    canonical.Value `json:"new"`
}

// RecordChange is one added, modified or deleted record.
type RecordChange struct {
	Table     string          `json:"table"`
	Key       canonical.PKKey `json:"key"`
	PKColumns []string        `json:"-"`
	Kind      Kind            `json:"kind"`
	Old       *object.Record  `json:"old,omitempty"`
	New       *object.Record  `json:"new,omitempty"`
	Fields    []FieldChange   `json:"fields,omitempty"`
	OldHash   object.Hash     `json:"-"`
	NewHash   object.Hash     `json:"-"`
}

// ID renders the record identifier, e.g. "users/42".
func (c *RecordChange) ID() string {
	return canonical.DisplayPK(c.Table, c.PKColumns, c.Key)
}

// Diff is the full comparison of two states.
type Diff struct {
	Changes []RecordChange
}

// Counts returns the number of added, modified and deleted records.
func (d *Diff) Counts() (added, modified, deleted int) {
	for _, c := range d.Changes {
		switch c.Kind {
		case Added:
			added++
		case Modified:
			modified++
		case Deleted:
			deleted++
		}
	}
	return
}

// Empty reports whether there are no changes.
func (d *Diff) Empty() bool { return len(d.Changes) == 0 }

// Compare returns the changes that turn state a into state b.
func Compare(a, b snapshot.Source) (*Diff, error) {
	d := &Diff{}
	for _, name := range snapshot.TableUnion(a, b) {
		ha, err := a.ManifestHash(name)
		if err != nil {
			return nil, err
		}
		hb, err := b.ManifestHash(name)
		if err != nil {
			return nil, err
		}
		if ha == hb {
			continue
		}
		ta, err := a.Table(name)
		if err != nil {
			return nil, err
		}
		tb, err := b.Table(name)
		if err != nil {
			return nil, err
		}
		pk := snapshot.PKColumns(name, b, a)
		changes, err := compareTable(name, pk, ta, a, tb, b)
		if err != nil {
			return nil, err
		}
		d.Changes = append(d.Changes, changes...)
	}
	return d, nil
}

func compareTable(name string, pk []string, ta *snapshot.Table, a snapshot.Source, tb *snapshot.Table, b snapshot.Source) ([]RecordChange, error) {
	var ra, rb map[canonical.PKKey]object.Hash
	if ta != nil {
		ra = ta.Records
	}
	if tb != nil {
		rb = tb.Records
	}
	keySet := map[canonical.PKKey]bool{}
	for k := range ra {
		keySet[k] = true
	}
	for k := range rb {
		keySet[k] = true
	}
	keys := make([]canonical.PKKey, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	canonical.SortPKKeys(keys)

	var out []RecordChange
	for _, k := range keys {
		ha, inA := ra[k]
		hb, inB := rb[k]
		switch {
		case inA && inB && ha == hb:
			continue
		case !inA:
			rec, err := b.Record(hb)
			if err != nil {
				return nil, err
			}
			out = append(out, RecordChange{Table: name, Key: k, PKColumns: pk, Kind: Added, New: rec, NewHash: hb})
		case !inB:
			rec, err := a.Record(ha)
			if err != nil {
				return nil, err
			}
			out = append(out, RecordChange{Table: name, Key: k, PKColumns: pk, Kind: Deleted, Old: rec, OldHash: ha})
		default:
			oldRec, err := a.Record(ha)
			if err != nil {
				return nil, err
			}
			newRec, err := b.Record(hb)
			if err != nil {
				return nil, err
			}
			fields := CompareFields(oldRec.Data, newRec.Data)
			if len(fields) == 0 {
				// Different hash but identical data can only happen when the
				// table name differs, which cannot occur inside one table.
				continue
			}
			out = append(out, RecordChange{Table: name, Key: k, PKColumns: pk, Kind: Modified,
				Old: oldRec, New: newRec, Fields: fields, OldHash: ha, NewHash: hb})
		}
	}
	return out, nil
}

// CompareFields lists the columns whose values differ between two rows.
// Columns missing on one side are reported with an empty Value.
func CompareFields(oldData, newData map[string]canonical.Value) []FieldChange {
	cols := map[string]bool{}
	for c := range oldData {
		cols[c] = true
	}
	for c := range newData {
		cols[c] = true
	}
	names := make([]string, 0, len(cols))
	for c := range cols {
		names = append(names, c)
	}
	sort.Strings(names)
	var out []FieldChange
	for _, c := range names {
		o, n := oldData[c], newData[c]
		if o != n {
			out = append(out, FieldChange{Column: c, Old: o, New: n})
		}
	}
	return out
}

// Write renders the diff in the human-readable format used by `sqit diff`.
// Columns are shown in schema order when the schema knows the table,
// otherwise alphabetically.
func (d *Diff) Write(w io.Writer, columnOrder func(table string) []string) {
	for i := range d.Changes {
		c := &d.Changes[i]
		cols := orderFor(c, columnOrder)
		switch c.Kind {
		case Added:
			fmt.Fprintf(w, "added: %s\n", c.ID())
			for _, col := range cols {
				fmt.Fprintf(w, "+  %s: %s\n", col, c.New.Data[col].Display())
			}
		case Deleted:
			fmt.Fprintf(w, "deleted: %s\n", c.ID())
			for _, col := range cols {
				fmt.Fprintf(w, "-  %s: %s\n", col, c.Old.Data[col].Display())
			}
		case Modified:
			fmt.Fprintf(w, "modified: %s\n", c.ID())
			changed := map[string]FieldChange{}
			for _, f := range c.Fields {
				changed[f.Column] = f
			}
			for _, col := range cols {
				if f, ok := changed[col]; ok {
					fmt.Fprintf(w, "-  %s: %s\n", col, f.Old.Display())
					fmt.Fprintf(w, "+  %s: %s\n", col, f.New.Display())
				} else {
					fmt.Fprintf(w, "   %s: %s\n", col, c.New.Data[col].Display())
				}
			}
		}
		fmt.Fprintln(w)
	}
}

func orderFor(c *RecordChange, columnOrder func(string) []string) []string {
	var cols []string
	if columnOrder != nil {
		cols = columnOrder(c.Table)
	}
	present := map[string]bool{}
	if c.New != nil {
		for k := range c.New.Data {
			present[k] = true
		}
	}
	if c.Old != nil {
		for k := range c.Old.Data {
			present[k] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, col := range cols {
		if present[col] {
			out = append(out, col)
			seen[col] = true
		}
	}
	var rest []string
	for col := range present {
		if !seen[col] {
			rest = append(rest, col)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}
