package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"sqit/internal/apply"
	"sqit/internal/object"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

// Apply executes the plan inside tx. Every statement must affect exactly one
// row; anything else indicates a concurrent modification and aborts.
// Foreign key checks are disabled for the transaction so the order of
// operations across tables does not matter.
func Apply(ctx context.Context, tx *sql.Tx, sch *schema.Schema, plan *apply.Plan) error {
	if plan.Empty() {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return err
	}
	for i := range plan.Ops {
		op := &plan.Ops[i]
		t := sch.Table(op.Table)
		if t == nil {
			return fmt.Errorf("apply: table %s not in schema", op.Table)
		}
		query, args, err := statement(t, op)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("apply %s %s: %w", op.Kind, op.ID(), err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("apply %s %s: expected 1 row affected, got %d (concurrent modification?)", op.Kind, op.ID(), n)
		}
	}
	return nil
}

func statement(t *schema.Table, op *apply.Op) (string, []any, error) {
	where, whereArgs, err := whereClause(t, op)
	if err != nil {
		return "", nil, err
	}
	switch op.Kind {
	case apply.Delete:
		return fmt.Sprintf("DELETE FROM %s WHERE %s", Quote(t.Name), where), whereArgs, nil
	case apply.Update:
		var sets []string
		var args []any
		for _, c := range t.Columns {
			v, ok := op.Values[c.Name]
			if !ok {
				continue
			}
			arg, err := ToArg(&c, v)
			if err != nil {
				return "", nil, err
			}
			sets = append(sets, Quote(c.Name)+" = ?")
			args = append(args, arg)
		}
		if len(sets) == 0 {
			return "", nil, fmt.Errorf("apply: empty update for %s", op.ID())
		}
		return fmt.Sprintf("UPDATE %s SET %s WHERE %s", Quote(t.Name), strings.Join(sets, ", "), where),
			append(args, whereArgs...), nil
	case apply.Insert:
		var cols, marks []string
		var args []any
		for _, c := range t.Columns {
			v, ok := op.Values[c.Name]
			if !ok {
				continue
			}
			arg, err := ToArg(&c, v)
			if err != nil {
				return "", nil, err
			}
			cols = append(cols, Quote(c.Name))
			marks = append(marks, "?")
			args = append(args, arg)
		}
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", Quote(t.Name), strings.Join(cols, ", "), strings.Join(marks, ", ")),
			args, nil
	}
	return "", nil, fmt.Errorf("apply: unknown op %s", op.Kind)
}

func whereClause(t *schema.Table, op *apply.Op) (string, []any, error) {
	var parts []string
	var args []any
	for _, name := range t.PrimaryKey {
		c := t.Column(name)
		if c == nil {
			return "", nil, fmt.Errorf("apply: pk column %s missing in %s", name, t.Name)
		}
		v, ok := op.PK[name]
		if !ok {
			return "", nil, fmt.Errorf("apply: op for %s lacks pk column %s", op.ID(), name)
		}
		arg, err := ToArg(c, v)
		if err != nil {
			return "", nil, err
		}
		if arg == nil {
			parts = append(parts, Quote(name)+" IS NULL")
			continue
		}
		parts = append(parts, Quote(name)+" = ?")
		args = append(args, arg)
	}
	return strings.Join(parts, " AND "), args, nil
}

// Verify re-reads the given tables through q and checks that each hashes to
// the manifest expected by want. It returns a descriptive error otherwise.
func Verify(ctx context.Context, q Querier, sch *schema.Schema, tables []string, want snapshot.Source) error {
	for _, name := range tables {
		t := sch.Table(name)
		if t == nil {
			return fmt.Errorf("verify: table %s not in schema", name)
		}
		got := snapshot.NewMemory(sch)
		if err := ReadTable(ctx, q, t, func(rec *object.Record) error {
			_, err := got.Add(rec)
			return err
		}); err != nil {
			return err
		}
		gh, err := got.ManifestHash(name)
		if err != nil {
			return err
		}
		wh, err := want.ManifestHash(name)
		if err != nil {
			return err
		}
		if gh != wh {
			return fmt.Errorf("verify: table %s does not match the expected state after apply (%s != %s); rolled back", name, gh.Short(), wh.Short())
		}
	}
	return nil
}
