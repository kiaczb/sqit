package core

import (
	"context"
	"fmt"

	"sqit/internal/canonical"
	"sqit/internal/diff"
	"sqit/internal/object"
	"sqit/internal/snapshot"
)

// Cell is one column value of a table row, ready for display.
type Cell struct {
	Value   string `json:"value"`
	Null    bool   `json:"null,omitempty"`
	Changed bool   `json:"changed,omitempty"`
	Old     string `json:"old,omitempty"`
	OldNull bool   `json:"oldNull,omitempty"`
}

// Row is one record of a table with its change status.
type Row struct {
	Key    canonical.PKKey `json:"key"`
	ID     string          `json:"id"`
	Status string          `json:"status"` // unchanged | added | modified | deleted
	Cells  map[string]Cell `json:"cells"`
}

// TableView is the content of one table annotated with the changes from a
// base state ("from") to a target state ("to").
type TableView struct {
	Table      string   `json:"table"`
	Mode       string   `json:"mode"` // working: HEAD -> working tree; upstream: HEAD -> upstream
	PrimaryKey []string `json:"primaryKey"`
	Columns    []string `json:"columns"`
	Rows       []Row    `json:"rows"`
	Counts     struct {
		Total    int `json:"total"`
		Added    int `json:"added"`
		Modified int `json:"modified"`
		Deleted  int `json:"deleted"`
	} `json:"counts"`
}

// Table renders one table. mode is "working" (HEAD vs LOCAL database) or
// "upstream" (HEAD vs last fetched upstream snapshot).
func (w *Workspace) Table(ctx context.Context, name, mode string) (*TableView, error) {
	headState, _, err := w.HeadState()
	if err != nil {
		return nil, err
	}
	var to snapshot.Source
	switch mode {
	case "", "working":
		mode = "working"
		local, err := w.ReadLocal(ctx, true)
		if err != nil {
			return nil, err
		}
		defer local.Close()
		to = local.State
	case "upstream":
		up, h, err := w.UpstreamState()
		if err != nil {
			return nil, err
		}
		if h == "" {
			return nil, fmt.Errorf("no upstream snapshot yet (run: sqit fetch)")
		}
		to = up
	default:
		return nil, fmt.Errorf("unknown mode %q (working|upstream)", mode)
	}
	return buildTableView(name, mode, headState, to)
}

func buildTableView(name, mode string, from, to snapshot.Source) (*TableView, error) {
	tv := &TableView{Table: name, Mode: mode}
	st := to.Schema().Table(name)
	if st == nil {
		st = from.Schema().Table(name)
	}
	if st == nil {
		return nil, fmt.Errorf("table %q is not tracked", name)
	}
	tv.PrimaryKey = st.PrimaryKey
	tv.Columns = st.ColumnNames()

	toTable, err := to.Table(name)
	if err != nil {
		return nil, err
	}
	fromTable, err := from.Table(name)
	if err != nil {
		return nil, err
	}

	// Changes for the table, indexed by key.
	d, err := diff.Compare(from, to)
	if err != nil {
		return nil, err
	}
	changes := map[canonical.PKKey]*diff.RecordChange{}
	for i := range d.Changes {
		if d.Changes[i].Table == name {
			changes[d.Changes[i].Key] = &d.Changes[i]
		}
	}

	keySet := map[canonical.PKKey]bool{}
	if toTable != nil {
		for k := range toTable.Records {
			keySet[k] = true
		}
	}
	if fromTable != nil {
		for k := range fromTable.Records {
			keySet[k] = true
		}
	}
	keys := make([]canonical.PKKey, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	canonical.SortPKKeys(keys)

	for _, k := range keys {
		row := Row{Key: k, ID: canonical.DisplayPK(name, st.PrimaryKey, k), Status: "unchanged", Cells: map[string]Cell{}}
		var rec *object.Record
		if c, ok := changes[k]; ok {
			row.Status = string(c.Kind)
			switch c.Kind {
			case diff.Deleted:
				rec = c.Old
			default:
				rec = c.New
			}
			if c.Kind == diff.Modified {
				for _, f := range c.Fields {
					row.Cells[f.Column] = Cell{Value: f.New.Display(), Null: f.New.IsNull(), Changed: true, Old: f.Old.Display(), OldNull: f.Old.IsNull()}
				}
			}
		} else {
			h := toTable.Records[k]
			if rec, err = to.Record(h); err != nil {
				return nil, err
			}
		}
		for col, v := range rec.Data {
			if _, done := row.Cells[col]; done {
				continue
			}
			row.Cells[col] = Cell{Value: v.Display(), Null: v.IsNull()}
		}
		switch row.Status {
		case "added":
			tv.Counts.Added++
		case "modified":
			tv.Counts.Modified++
		case "deleted":
			tv.Counts.Deleted++
		}
		tv.Counts.Total++
		tv.Rows = append(tv.Rows, row)
	}
	if tv.Rows == nil {
		tv.Rows = []Row{}
	}
	return tv, nil
}
