// Package apply turns the difference between two states into an ordered
// list of INSERT / UPDATE / DELETE operations. Executing them is the job of
// the database layer.
package apply

import (
	"fmt"
	"io"
	"sort"

	"sqit/internal/canonical"
	"sqit/internal/diff"
	"sqit/internal/snapshot"
)

// OpKind is the kind of a planned operation.
type OpKind string

const (
	Insert OpKind = "INSERT"
	Update OpKind = "UPDATE"
	Delete OpKind = "DELETE"
)

// Op is one planned row operation.
type Op struct {
	Kind      OpKind                     `json:"kind"`
	Table     string                     `json:"table"`
	Key       canonical.PKKey            `json:"key"`
	PKColumns []string                   `json:"-"`
	PK        map[string]canonical.Value `json:"primaryKey"`
	// Values holds every column for Insert and only the changed columns for
	// Update; it is nil for Delete.
	Values map[string]canonical.Value `json:"values,omitempty"`
	// Old holds the previous values of the changed columns for Update.
	Old map[string]canonical.Value `json:"old,omitempty"`
}

// ID renders the record identifier.
func (o *Op) ID() string { return canonical.DisplayPK(o.Table, o.PKColumns, o.Key) }

// Plan is the ordered list of operations that turn one state into another.
type Plan struct {
	Ops []Op
}

// Counts returns the number of inserts, updates and deletes.
func (p *Plan) Counts() (inserts, updates, deletes int) {
	for _, o := range p.Ops {
		switch o.Kind {
		case Insert:
			inserts++
		case Update:
			updates++
		case Delete:
			deletes++
		}
	}
	return
}

// Empty reports whether nothing needs to be done.
func (p *Plan) Empty() bool { return len(p.Ops) == 0 }

// Tables lists the tables touched by the plan.
func (p *Plan) Tables() []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range p.Ops {
		if !seen[o.Table] {
			seen[o.Table] = true
			out = append(out, o.Table)
		}
	}
	return out
}

// FromDiff converts a diff (from → to) into a plan. Deletes come first,
// then updates, then inserts, each in diff order.
func FromDiff(d *diff.Diff) *Plan {
	p := &Plan{}
	for _, kind := range []diff.Kind{diff.Deleted, diff.Modified, diff.Added} {
		for i := range d.Changes {
			c := &d.Changes[i]
			if c.Kind != kind {
				continue
			}
			switch kind {
			case diff.Deleted:
				p.Ops = append(p.Ops, Op{Kind: Delete, Table: c.Table, Key: c.Key, PKColumns: c.PKColumns, PK: c.Old.PK})
			case diff.Modified:
				vals := map[string]canonical.Value{}
				old := map[string]canonical.Value{}
				for _, f := range c.Fields {
					vals[f.Column] = f.New
					old[f.Column] = f.Old
				}
				p.Ops = append(p.Ops, Op{Kind: Update, Table: c.Table, Key: c.Key, PKColumns: c.PKColumns, PK: c.Old.PK, Values: vals, Old: old})
			case diff.Added:
				p.Ops = append(p.Ops, Op{Kind: Insert, Table: c.Table, Key: c.Key, PKColumns: c.PKColumns, PK: c.New.PK, Values: c.New.Data})
			}
		}
	}
	return p
}

// Between computes the plan that turns state from into state to.
func Between(from, to snapshot.Source) (*Plan, error) {
	d, err := diff.Compare(from, to)
	if err != nil {
		return nil, err
	}
	return FromDiff(d), nil
}

// Write renders the plan for humans.
func (p *Plan) Write(w io.Writer) {
	for i := range p.Ops {
		o := &p.Ops[i]
		switch o.Kind {
		case Insert:
			fmt.Fprintf(w, "INSERT %s\n", o.ID())
		case Delete:
			fmt.Fprintf(w, "DELETE %s\n", o.ID())
		case Update:
			for _, col := range sortedKeys(o.Values) {
				fmt.Fprintf(w, "UPDATE %s.%s  %s -> %s\n", o.ID(), col, o.Old[col].Display(), o.Values[col].Display())
			}
		}
	}
}

func sortedKeys(m map[string]canonical.Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
