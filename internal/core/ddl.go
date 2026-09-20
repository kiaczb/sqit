package core

import (
	"context"
	"fmt"
	"strings"

	"sqit/internal/mysql"
	"sqit/internal/schema"
)

// missingTables returns the tables that exist upstream but not locally,
// according to the schema incompatibility list.
func missingTables(incompat []schema.Change) []string {
	var out []string
	for _, c := range incompat {
		if c.Kind == schema.TableAdded {
			out = append(out, c.Table)
		}
	}
	return out
}

// upstreamCreateStatements fetches the CREATE TABLE statements of the given
// tables from TEST.
func (w *Workspace) upstreamCreateStatements(ctx context.Context, tables []string) ([]string, error) {
	dsn, err := w.Repo.Config.Test.DSN()
	if err != nil {
		return nil, err
	}
	if w.Repo.Config.IsLocalOnly() {
		if err := mysql.AssertLoopback(dsn); err != nil {
			return nil, err
		}
	}
	sess, err := mysql.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("test: %w", err)
	}
	defer sess.Close()
	var out []string
	for _, t := range tables {
		ddl, err := mysql.ShowCreateTable(ctx, sess.Conn(), t)
		if err != nil {
			return nil, err
		}
		out = append(out, ddl)
	}
	return out, nil
}

// createLocalTables runs CREATE TABLE statements on LOCAL. DDL commits
// implicitly in MySQL, so this happens before the pull transaction.
func (w *Workspace) createLocalTables(ctx context.Context, ddl []string) error {
	dsn, err := w.Repo.Config.Local.DSN()
	if err != nil {
		return err
	}
	sess, err := mysql.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("local: %w", err)
	}
	defer sess.Close()
	for _, stmt := range ddl {
		if _, err := sess.Conn().ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating table: %w\n%s", err, stmt)
		}
	}
	return nil
}

// ddlSummary renders statements for humans.
func ddlSummary(ddl []string) string {
	var b strings.Builder
	for _, s := range ddl {
		b.WriteString(s)
		b.WriteString(";\n")
	}
	return b.String()
}
