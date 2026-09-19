package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"sqit/internal/object"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

// Querier is satisfied by *sql.Conn and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Session is a single dedicated connection with sqit's session settings
// applied (UTC time zone, utf8mb4).
type Session struct {
	db   *sql.DB
	conn *sql.Conn
}

// Connect opens a session.
func Connect(ctx context.Context, dsn string) (*Session, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to MySQL: %w", err)
	}
	for _, stmt := range []string{
		"SET time_zone = '+00:00'",
		"SET NAMES utf8mb4",
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			conn.Close()
			db.Close()
			return nil, fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return &Session{db: db, conn: conn}, nil
}

// Close releases the connection.
func (s *Session) Close() error {
	s.conn.Close()
	return s.db.Close()
}

// Conn exposes the underlying connection.
func (s *Session) Conn() *sql.Conn { return s.conn }

// Begin starts a transaction. Reads inside it see one consistent snapshot
// (REPEATABLE READ), which is what makes multi-table snapshots coherent.
// readOnly is informational only: START TRANSACTION READ ONLY needs MySQL
// 5.6.5+ and buys nothing here, so it is not sent.
func (s *Session) Begin(ctx context.Context, readOnly bool) (*sql.Tx, error) {
	_ = readOnly
	return s.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}

// ReadState reads the schema and every row of the configured tables through
// q (normally a transaction) into an in-memory state.
func ReadState(ctx context.Context, q Querier, tables []string) (*snapshot.Memory, error) {
	sch, err := ReadSchema(ctx, q, tables)
	if err != nil {
		return nil, err
	}
	state := snapshot.NewMemory(sch)
	for i := range sch.Tables {
		t := &sch.Tables[i]
		if err := ReadTable(ctx, q, t, func(rec *object.Record) error {
			_, err := state.Add(rec)
			return err
		}); err != nil {
			return nil, err
		}
	}
	return state, nil
}

// ReadTable streams every row of a table as canonical records.
func ReadTable(ctx context.Context, q Querier, t *schema.Table, sink func(*object.Record) error) error {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = Quote(c.Name)
	}
	pk := make([]string, len(t.PrimaryKey))
	for i, c := range t.PrimaryKey {
		pk[i] = Quote(c)
	}
	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", strings.Join(cols, ", "), Quote(t.Name), strings.Join(pk, ", "))
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("reading %s: %w", t.Name, err)
	}
	defer rows.Close()

	raw := make([]sql.RawBytes, len(t.Columns))
	ptrs := make([]any, len(t.Columns))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("reading %s: %w", t.Name, err)
		}
		rec := &object.Record{Table: t.Name, PK: map[string]canonicalValue{}, Data: map[string]canonicalValue{}}
		for i := range t.Columns {
			c := &t.Columns[i]
			v, err := FromText(c, raw[i])
			if err != nil {
				return fmt.Errorf("table %s: %w", t.Name, err)
			}
			rec.Data[c.Name] = v
			if t.IsPK(c.Name) {
				rec.PK[c.Name] = v
			}
		}
		if err := sink(rec); err != nil {
			return err
		}
	}
	return rows.Err()
}
