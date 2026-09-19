// Package core implements the sqit workflows (commit, fetch, status, diff,
// pull) on top of the repository, snapshot, diff, merge and MySQL layers.
// The CLI is a thin printer around it.
package core

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sqit/internal/mysql"
	"sqit/internal/object"
	"sqit/internal/repo"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

// Workspace binds a repository to its databases.
type Workspace struct {
	Repo *repo.Repository
	// Now is overridable for tests.
	Now func() time.Time
}

// Open opens the repository found at or above dir.
func Open(dir string) (*Workspace, error) {
	r, err := repo.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Workspace{Repo: r, Now: time.Now}, nil
}

// LocalTx is an open read (or read-write) transaction on the LOCAL
// database together with the working-tree state read through it.
type LocalTx struct {
	Session *mysql.Session
	Tx      *sql.Tx
	State   *snapshot.Memory
}

// Close rolls back (if still open) and closes the session.
func (l *LocalTx) Close() {
	if l == nil {
		return
	}
	if l.Tx != nil {
		l.Tx.Rollback()
	}
	if l.Session != nil {
		l.Session.Close()
	}
}

// ReadLocal opens the LOCAL database and reads the working tree inside a
// transaction. The caller must Close the result.
func (w *Workspace) ReadLocal(ctx context.Context, readOnly bool) (*LocalTx, error) {
	dsn, err := w.Repo.Config.Local.DSN()
	if err != nil {
		return nil, fmt.Errorf("local: %w", err)
	}
	sess, err := mysql.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("local (%s): %w", w.Repo.Config.Local.Describe(), err)
	}
	tx, err := sess.Begin(ctx, readOnly)
	if err != nil {
		sess.Close()
		return nil, err
	}
	state, err := mysql.ReadState(ctx, tx, w.Repo.Config.Tables)
	if err != nil {
		tx.Rollback()
		sess.Close()
		return nil, fmt.Errorf("local: %w", err)
	}
	return &LocalTx{Session: sess, Tx: tx, State: state}, nil
}

// ReadTest reads the TEST database state (schema + rows).
func (w *Workspace) ReadTest(ctx context.Context) (*snapshot.Memory, error) {
	dsn, err := w.Repo.Config.Test.DSN()
	if err != nil {
		return nil, fmt.Errorf("test: %w", err)
	}
	sess, err := mysql.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("test (%s): %w", w.Repo.Config.Test.Describe(), err)
	}
	defer sess.Close()
	tx, err := sess.Begin(ctx, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	state, err := mysql.ReadState(ctx, tx, w.Repo.Config.Tables)
	if err != nil {
		return nil, fmt.Errorf("test: %w", err)
	}
	return state, nil
}

// HeadState returns the state HEAD points at (empty when unborn).
func (w *Workspace) HeadState() (*snapshot.Repo, object.Hash, error) {
	h, err := w.Repo.Head()
	if err != nil {
		return nil, "", err
	}
	s, err := snapshot.FromCommit(w.Repo, h)
	return s, h, err
}

// UpstreamState returns the last fetched upstream state (nil if none).
func (w *Workspace) UpstreamState() (*snapshot.Repo, object.Hash, error) {
	h, err := w.Repo.Upstream()
	if err != nil || h == "" {
		return nil, "", err
	}
	s, err := snapshot.FromCommit(w.Repo, h)
	return s, h, err
}

// writeState stores the state's objects and its schema, returning both hashes.
func (w *Workspace) writeState(state *snapshot.Memory) (snap, sch object.Hash, err error) {
	snap, err = state.Write(w.Repo)
	if err != nil {
		return "", "", err
	}
	sch, err = w.Repo.Put(object.TypeSchema, state.Schema())
	return snap, sch, err
}

// stateHashes computes (without writing) the snapshot and schema hashes.
func stateHashes(state *snapshot.Memory) (snap, sch object.Hash, err error) {
	_, snap, err = state.Hash()
	if err != nil {
		return "", "", err
	}
	sch, err = object.HashOf(state.Schema())
	return snap, sch, err
}

// ---------------------------------------------------------------------------
// commit

// Commit snapshots LOCAL and records a new commit on the current branch.
func (w *Workspace) Commit(ctx context.Context, message string, allowEmpty bool) (object.Hash, *object.Commit, error) {
	local, err := w.ReadLocal(ctx, true)
	if err != nil {
		return "", nil, err
	}
	defer local.Close()

	head, err := w.Repo.Head()
	if err != nil {
		return "", nil, err
	}
	snap, sch, err := stateHashes(local.State)
	if err != nil {
		return "", nil, err
	}
	if head != "" && !allowEmpty {
		hc, err := w.Repo.Commit(head)
		if err != nil {
			return "", nil, err
		}
		if hc.Snapshot == snap && hc.Schema == sch {
			return "", nil, ErrNothingToCommit
		}
	}
	if _, _, err := w.writeState(local.State); err != nil {
		return "", nil, err
	}
	c := &object.Commit{
		Kind:      object.KindLocal,
		Parents:   parents(head),
		Snapshot:  snap,
		Schema:    sch,
		Message:   message,
		Timestamp: w.Now().UTC(),
		Metadata:  map[string]string{},
	}
	h, err := w.Repo.Put(object.TypeCommit, c)
	if err != nil {
		return "", nil, err
	}
	if err := w.Repo.SetHead(h); err != nil {
		return "", nil, err
	}
	return h, c, nil
}

// ErrNothingToCommit is returned when the working tree equals HEAD.
var ErrNothingToCommit = fmt.Errorf("nothing to commit (working tree matches HEAD)")

func parents(h object.Hash) []object.Hash {
	if h == "" {
		return []object.Hash{}
	}
	return []object.Hash{h}
}

// ---------------------------------------------------------------------------
// fetch

// FetchResult describes a fetch.
type FetchResult struct {
	Upstream object.Hash // the upstream commit after the fetch
	Previous object.Hash // the upstream commit before ("" if none)
	Changed  bool        // whether a new upstream commit was created
	State    *snapshot.Memory
}

// Fetch snapshots TEST and appends an upstream commit if its state changed.
func (w *Workspace) Fetch(ctx context.Context) (*FetchResult, error) {
	state, err := w.ReadTest(ctx)
	if err != nil {
		return nil, err
	}
	return w.recordUpstream(state)
}

func (w *Workspace) recordUpstream(state *snapshot.Memory) (*FetchResult, error) {
	prev, err := w.Repo.Upstream()
	if err != nil {
		return nil, err
	}
	snap, sch, err := stateHashes(state)
	if err != nil {
		return nil, err
	}
	res := &FetchResult{Previous: prev, State: state}
	if prev != "" {
		pc, err := w.Repo.Commit(prev)
		if err != nil {
			return nil, err
		}
		if pc.Snapshot == snap && pc.Schema == sch {
			res.Upstream = prev
			return res, nil
		}
	}
	if _, _, err := w.writeState(state); err != nil {
		return nil, err
	}
	c := &object.Commit{
		Kind:      object.KindUpstream,
		Parents:   parents(prev),
		Snapshot:  snap,
		Schema:    sch,
		Message:   "Snapshot of TEST",
		Timestamp: w.Now().UTC(),
		Metadata:  map[string]string{"remote": "test"},
	}
	h, err := w.Repo.Put(object.TypeCommit, c)
	if err != nil {
		return nil, err
	}
	if err := w.Repo.WriteRef(repo.UpstreamRef, h); err != nil {
		return nil, err
	}
	res.Upstream = h
	res.Changed = true
	return res, nil
}

// ---------------------------------------------------------------------------
// helpers shared by status / diff / pull

// Relation describes how HEAD relates to the upstream ref.
type Relation string

const (
	RelNoUpstream    Relation = "NO UPSTREAM"    // never fetched
	RelUpToDate      Relation = "UP TO DATE"     // HEAD == upstream
	RelUpstreamAhead Relation = "UPSTREAM AHEAD" // HEAD is an ancestor of upstream (fast-forwardable)
	RelLocalAhead    Relation = "LOCAL AHEAD"    // upstream is an ancestor of HEAD
	RelDiverged      Relation = "DIVERGED"       // both moved since the merge base
	RelUnrelated     Relation = "UNRELATED"      // no common ancestor; the first local commit acts as base
)

// Relate computes the relation and merge base between HEAD and upstream.
//
// When the two histories share no commit (the local branch was started with
// `sqit commit` before the first `sqit pull`) the root of the local history
// is used as the base: it is taken to be the state at which LOCAL mirrored
// TEST. Local commits after it count as local changes and everything TEST
// differs in counts as upstream changes.
func (w *Workspace) Relate(head, upstream object.Hash) (Relation, object.Hash, error) {
	if upstream == "" {
		return RelNoUpstream, "", nil
	}
	if head == upstream {
		return RelUpToDate, head, nil
	}
	if head == "" {
		return RelUpstreamAhead, "", nil
	}
	base, err := w.Repo.MergeBase(head, upstream)
	if err != nil {
		return "", "", err
	}
	switch base {
	case "":
		root, err := w.Repo.RootCommit(head)
		if err != nil {
			return "", "", err
		}
		return RelUnrelated, root, nil
	case head:
		return RelUpstreamAhead, base, nil
	case upstream:
		return RelLocalAhead, base, nil
	}
	return RelDiverged, base, nil
}

// SchemaOf returns the schema of a source, or the empty schema.
func SchemaOf(s snapshot.Source) *schema.Schema {
	if s == nil {
		return object.EmptySchema()
	}
	return s.Schema()
}
