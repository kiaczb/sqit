package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"sqit/internal/canonical"
	"sqit/internal/schema"
)

type canonicalValue = canonical.Value

// ListTables returns the base tables of the connected database, sorted.
func ListTables(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ShowCreateTable returns the CREATE TABLE statement of a table.
func ShowCreateTable(ctx context.Context, q Querier, table string) (string, error) {
	rows, err := q.QueryContext(ctx, "SHOW CREATE TABLE "+Quote(table))
	if err != nil {
		return "", fmt.Errorf("SHOW CREATE TABLE %s: %w", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return "", fmt.Errorf("SHOW CREATE TABLE %s returned nothing", table)
	}
	cols, _ := rows.Columns()
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	if len(vals) < 2 {
		return "", fmt.Errorf("unexpected SHOW CREATE TABLE result for %s", table)
	}
	return string(vals[1]), nil
}

// ReadSchema reads the structure of the given tables from
// information_schema. Every table must exist. Tables without a primary key
// are an error unless skipNoPK is set, in which case they are left out and
// returned in skipped.
func ReadSchema(ctx context.Context, q Querier, tables []string, skipNoPK bool) (*schema.Schema, []string, error) {
	if len(tables) == 0 {
		return nil, nil, fmt.Errorf("no tables to track (the database has no tables, or all are ignored)")
	}
	placeholders := strings.Repeat("?,", len(tables))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(tables))
	for i, t := range tables {
		args[i] = t
	}

	// Existence check.
	rows, err := q.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("reading table list: %w", err)
	}
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		present[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for _, t := range tables {
		if !present[t] {
			return nil, nil, fmt.Errorf("table %q does not exist in the database", t)
		}
	}

	// Columns.
	rows, err = q.QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME, ORDINAL_POSITION, COLUMN_TYPE, DATA_TYPE,
			IS_NULLABLE, COLUMN_DEFAULT, EXTRA, CHARACTER_SET_NAME, COLLATION_NAME
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (`+placeholders+`)
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("reading columns: %w", err)
	}
	byTable := map[string]*schema.Table{}
	for rows.Next() {
		var (
			table, col, colType, dataType, nullable, extra string
			pos                                            int
			def, charset, collation                        sql.NullString
		)
		if err := rows.Scan(&table, &col, &pos, &colType, &dataType, &nullable, &def, &extra, &charset, &collation); err != nil {
			rows.Close()
			return nil, nil, err
		}
		t := byTable[table]
		if t == nil {
			t = &schema.Table{Name: table}
			byTable[table] = t
		}
		c := schema.Column{
			Name:       col,
			Position:   pos,
			ColumnType: strings.ToLower(colType),
			DataType:   strings.ToLower(dataType),
			Nullable:   strings.EqualFold(nullable, "YES"),
			Extra:      strings.ToLower(extra),
		}
		if def.Valid {
			s := def.String
			c.Default = &s
		}
		if charset.Valid {
			s := charset.String
			c.Charset = &s
		}
		if collation.Valid {
			s := collation.String
			c.Collation = &s
		}
		t.Columns = append(t.Columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Primary keys.
	rows, err = q.QueryContext(ctx, `SELECT TABLE_NAME, COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'PRIMARY' AND TABLE_NAME IN (`+placeholders+`)
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("reading primary keys: %w", err)
	}
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if t := byTable[table]; t != nil {
			t.PrimaryKey = append(t.PrimaryKey, col)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	sch := &schema.Schema{}
	var skipped []string
	for _, name := range tables {
		t := byTable[name]
		if t == nil {
			return nil, nil, fmt.Errorf("table %q has no columns", name)
		}
		if len(t.PrimaryKey) == 0 {
			if skipNoPK {
				skipped = append(skipped, name)
				continue
			}
			return nil, nil, fmt.Errorf("table %q has no primary key; sqit requires one", name)
		}
		sch.Tables = append(sch.Tables, *t)
	}
	sch.Normalize()
	return sch, skipped, nil
}
