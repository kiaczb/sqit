// Package merge implements the three-way, field-level merge of database
// states. It is independent of MySQL.
package merge

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"sqit/internal/canonical"
	"sqit/internal/object"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

// ConflictKind classifies a conflict.
type ConflictKind string

const (
	ModifyModify ConflictKind = "modify/modify" // both sides changed a column differently
	AddAdd       ConflictKind = "add/add"       // both sides inserted the key with different data
	ModifyDelete ConflictKind = "modify/delete" // local modified, upstream deleted
	DeleteModify ConflictKind = "delete/modify" // local deleted, upstream modified
)

// Conflict is one unresolvable difference. For column conflicts Base, Local
// and Upstream are canonical.Value (Base is nil for add/add); for record
// conflicts Column is empty and they hold the full record data or nil.
type Conflict struct {
	Table      string                     `json:"table"`
	PrimaryKey map[string]canonical.Value `json:"primaryKey"`
	Key        canonical.PKKey            `json:"-"`
	PKColumns  []string                   `json:"-"`
	Column     string                     `json:"column,omitempty"`
	Kind       ConflictKind               `json:"kind"`
	Base       any                        `json:"base"`
	Local      any                        `json:"local"`
	Upstream   any                        `json:"upstream"`
}

// ID renders the record identifier, e.g. "users/42".
func (c *Conflict) ID() string {
	return canonical.DisplayPK(c.Table, c.PKColumns, c.Key)
}

// Result is the outcome of a merge.
type Result struct {
	// State is the merged state. It is complete even when there are
	// conflicts (conflicting records keep their LOCAL version) but must not
	// be applied unless Conflicts is empty.
	State     *snapshot.Memory
	Conflicts []Conflict
	// Counters describe how record-level decisions were made.
	Stats Stats
}

// Stats counts merge decisions.
type Stats struct {
	Unchanged    int `json:"unchanged"`
	LocalOnly    int `json:"localOnly"`
	UpstreamOnly int `json:"upstreamOnly"`
	Same         int `json:"same"`   // both sides made the identical change
	Merged       int `json:"merged"` // field-level merge of both sides
	Conflicted   int `json:"conflicted"`
}

// Clean reports whether the merge produced no conflicts.
func (r *Result) Clean() bool { return len(r.Conflicts) == 0 }

// Merge performs the three-way merge. sch is the schema of the resulting
// state (the local schema; callers verify compatibility first).
func Merge(base, local, upstream snapshot.Source, sch *schema.Schema) (*Result, error) {
	res := &Result{State: snapshot.NewMemory(sch)}
	for _, name := range snapshot.TableUnion(base, local, upstream) {
		hb, err := base.ManifestHash(name)
		if err != nil {
			return nil, err
		}
		hl, err := local.ManifestHash(name)
		if err != nil {
			return nil, err
		}
		hu, err := upstream.ManifestHash(name)
		if err != nil {
			return nil, err
		}
		pk := snapshot.PKColumns(name, local, upstream, base)
		// Whole-table fast paths.
		switch {
		case hl == hu:
			if err := copyTable(res.State, name, local); err != nil {
				return nil, err
			}
			continue
		case hl == hb:
			if err := copyTable(res.State, name, upstream); err != nil {
				return nil, err
			}
			continue
		case hu == hb:
			if err := copyTable(res.State, name, local); err != nil {
				return nil, err
			}
			continue
		}
		tb, err := base.Table(name)
		if err != nil {
			return nil, err
		}
		tl, err := local.Table(name)
		if err != nil {
			return nil, err
		}
		tu, err := upstream.Table(name)
		if err != nil {
			return nil, err
		}
		if err := mergeTable(res, name, pk, tb, base, tl, local, tu, upstream); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(res.Conflicts, func(i, j int) bool {
		a, b := res.Conflicts[i], res.Conflicts[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Column < b.Column
	})
	return res, nil
}

func copyTable(dst *snapshot.Memory, name string, src snapshot.Source) error {
	t, err := src.Table(name)
	if err != nil || t == nil {
		return err
	}
	for k, h := range t.Records {
		rec, err := src.Record(h)
		if err != nil {
			return err
		}
		dst.AddHash(name, k, h, rec)
	}
	return nil
}

func records(t *snapshot.Table) map[canonical.PKKey]object.Hash {
	if t == nil {
		return nil
	}
	return t.Records
}

func mergeTable(res *Result, name string, pk []string,
	tb *snapshot.Table, base snapshot.Source,
	tl *snapshot.Table, local snapshot.Source,
	tu *snapshot.Table, upstream snapshot.Source) error {

	rb, rl, ru := records(tb), records(tl), records(tu)
	keySet := map[canonical.PKKey]bool{}
	for k := range rb {
		keySet[k] = true
	}
	for k := range rl {
		keySet[k] = true
	}
	for k := range ru {
		keySet[k] = true
	}
	keys := make([]canonical.PKKey, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	canonical.SortPKKeys(keys)

	for _, k := range keys {
		hb, inB := rb[k]
		hl, inL := rl[k]
		hu, inU := ru[k]

		take := func(src snapshot.Source, h object.Hash) error {
			rec, err := src.Record(h)
			if err != nil {
				return err
			}
			res.State.AddHash(name, k, h, rec)
			return nil
		}

		switch {
		case inL == inU && hl == hu:
			// Both sides agree (same record, or both absent/deleted).
			if inL {
				if err := take(local, hl); err != nil {
					return err
				}
			}
			if inL && (!inB || hb != hl) {
				res.Stats.Same++
			} else {
				res.Stats.Unchanged++
			}
		case inL == inB && hl == hb:
			// Only upstream changed (modified, added or deleted).
			if inU {
				if err := take(upstream, hu); err != nil {
					return err
				}
			}
			res.Stats.UpstreamOnly++
		case inU == inB && hu == hb:
			// Only local changed.
			if inL {
				if err := take(local, hl); err != nil {
					return err
				}
			}
			res.Stats.LocalOnly++
		case inB && !inL:
			// Local deleted, upstream modified.
			baseRec, err := base.Record(hb)
			if err != nil {
				return err
			}
			upRec, err := upstream.Record(hu)
			if err != nil {
				return err
			}
			res.Conflicts = append(res.Conflicts, Conflict{
				Table: name, PrimaryKey: baseRec.PK, Key: k, PKColumns: pk, Kind: DeleteModify,
				Base: baseRec.Data, Local: nil, Upstream: upRec.Data,
			})
			res.Stats.Conflicted++
		case inB && !inU:
			// Local modified, upstream deleted.
			baseRec, err := base.Record(hb)
			if err != nil {
				return err
			}
			locRec, err := local.Record(hl)
			if err != nil {
				return err
			}
			res.Conflicts = append(res.Conflicts, Conflict{
				Table: name, PrimaryKey: baseRec.PK, Key: k, PKColumns: pk, Kind: ModifyDelete,
				Base: baseRec.Data, Local: locRec.Data, Upstream: nil,
			})
			// Keep the local record in the (unusable) state.
			if err := take(local, hl); err != nil {
				return err
			}
			res.Stats.Conflicted++
		default:
			// All three differ (or no base and both added differently):
			// merge field by field.
			var baseData map[string]canonical.Value
			if inB {
				baseRec, err := base.Record(hb)
				if err != nil {
					return err
				}
				baseData = baseRec.Data
			}
			locRec, err := local.Record(hl)
			if err != nil {
				return err
			}
			upRec, err := upstream.Record(hu)
			if err != nil {
				return err
			}
			merged, conflicts := mergeFields(name, pk, k, locRec.PK, baseData, locRec.Data, upRec.Data, inB)
			if len(conflicts) > 0 {
				res.Conflicts = append(res.Conflicts, conflicts...)
				res.Stats.Conflicted++
				if err := take(local, hl); err != nil {
					return err
				}
				continue
			}
			rec := &object.Record{Table: name, PK: locRec.PK, Data: merged}
			if _, err := res.State.Add(rec); err != nil {
				return err
			}
			res.Stats.Merged++
		}
	}
	return nil
}

func mergeFields(table string, pkCols []string, key canonical.PKKey, pk map[string]canonical.Value,
	base, local, upstream map[string]canonical.Value, hasBase bool) (map[string]canonical.Value, []Conflict) {

	cols := map[string]bool{}
	for c := range base {
		cols[c] = true
	}
	for c := range local {
		cols[c] = true
	}
	for c := range upstream {
		cols[c] = true
	}
	names := make([]string, 0, len(cols))
	for c := range cols {
		names = append(names, c)
	}
	sort.Strings(names)

	merged := make(map[string]canonical.Value, len(names))
	var conflicts []Conflict
	for _, c := range names {
		bv, lv, uv := base[c], local[c], upstream[c]
		switch {
		case lv == uv:
			merged[c] = lv
		case hasBase && lv == bv:
			merged[c] = uv
		case hasBase && uv == bv:
			merged[c] = lv
		default:
			kind := ModifyModify
			var baseVal any = bv
			if !hasBase {
				kind = AddAdd
				baseVal = nil
			}
			conflicts = append(conflicts, Conflict{
				Table: table, PrimaryKey: pk, Key: key, PKColumns: pkCols, Column: c, Kind: kind,
				Base: baseVal, Local: lv, Upstream: uv,
			})
			merged[c] = lv
		}
	}
	return merged, conflicts
}

// WriteConflicts renders conflicts for humans.
func WriteConflicts(w io.Writer, conflicts []Conflict) {
	for i := range conflicts {
		c := &conflicts[i]
		if c.Column != "" {
			fmt.Fprintf(w, "%s.%s  [%s]\n", c.ID(), c.Column, c.Kind)
			fmt.Fprintf(w, "  BASE:     %s\n", displayAny(c.Base))
			fmt.Fprintf(w, "  LOCAL:    %s\n", displayAny(c.Local))
			fmt.Fprintf(w, "  UPSTREAM: %s\n", displayAny(c.Upstream))
		} else {
			fmt.Fprintf(w, "%s  [%s]\n", c.ID(), c.Kind)
			fmt.Fprintf(w, "  BASE:     %s\n", displayAny(c.Base))
			fmt.Fprintf(w, "  LOCAL:    %s\n", displayAny(c.Local))
			fmt.Fprintf(w, "  UPSTREAM: %s\n", displayAny(c.Upstream))
		}
		fmt.Fprintln(w)
	}
}

func displayAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "<absent>"
	case canonical.Value:
		if x == "" {
			return "<absent>"
		}
		return x.Display()
	case map[string]canonical.Value:
		return displayRecord(x)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func displayRecord(data map[string]canonical.Value) string {
	if data == nil {
		return "<deleted>"
	}
	names := make([]string, 0, len(data))
	for c := range data {
		names = append(names, c)
	}
	sort.Strings(names)
	s := "{"
	for i, c := range names {
		if i > 0 {
			s += ", "
		}
		s += c + ": " + data[c].Display()
	}
	return s + "}"
}
