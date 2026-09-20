package core

import (
	"context"
	"fmt"

	"sqit/internal/apply"
	"sqit/internal/diff"
	"sqit/internal/merge"
	"sqit/internal/mysql"
	"sqit/internal/object"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

// PullReport describes what a pull did or (for a dry run) would do.
type PullReport struct {
	DryRun bool

	Head     object.Hash
	Upstream object.Hash // "" in a dry run when the upstream commit would be new
	Base     object.Hash
	Relation Relation

	UpstreamChanged bool // TEST differed from the last fetched upstream commit
	UpToDate        bool // nothing to merge
	FastForward     bool

	// SchemaIncompatibilities is non-empty when the pull was refused because
	// LOCAL and TEST schemas differ in a way that makes a data merge unsafe.
	SchemaIncompatibilities []schema.Change
	// MissingTableDDL holds the CREATE TABLE statements of tables that exist
	// upstream but not locally (from SHOW CREATE TABLE on TEST). With
	// PullOptions.CreateMissingTables they were executed on LOCAL (unless
	// DryRun); otherwise they are shown so the user can apply them.
	MissingTableDDL []string
	CreatedTables   []string

	// Conflicts stops the pull. ConflictSource says which local side clashed
	// with upstream: "working tree" or "HEAD".
	Conflicts      []merge.Conflict
	ConflictSource string

	// What would change / changed.
	LocalChanges    *diff.Diff // uncommitted: HEAD -> working tree
	UpstreamChanges *diff.Diff // incoming: HEAD -> upstream
	Plan            *apply.Plan
	Stats           merge.Stats

	Applied bool
	NewHead object.Hash
}

// Blocked reports whether the pull could not proceed.
func (p *PullReport) Blocked() bool {
	return len(p.SchemaIncompatibilities) > 0 || len(p.Conflicts) > 0
}

// PullOptions tune a pull.
type PullOptions struct {
	// CreateMissingTables creates tables that exist only upstream on LOCAL
	// (CREATE TABLE copied from TEST) before merging. Existing tables are
	// never altered.
	CreateMissingTables bool
}

// Pull fetches TEST, merges it into the local state and, unless dryRun is
// set, applies the result to LOCAL and records the merge. See DESIGN.md §4.4.
func (w *Workspace) Pull(ctx context.Context, dryRun bool) (*PullReport, error) {
	return w.PullWith(ctx, dryRun, PullOptions{})
}

// PullWith is Pull with options.
func (w *Workspace) PullWith(ctx context.Context, dryRun bool, opts PullOptions) (*PullReport, error) {
	rep := &PullReport{DryRun: dryRun}
	for attempt := 0; ; attempt++ {
		r, retry, err := w.pullOnce(ctx, dryRun, opts, rep, attempt == 0)
		if err != nil || !retry {
			return r, err
		}
	}
}

// pullOnce runs the pull. It asks for a retry after creating missing tables
// (the schema must be re-read from LOCAL).
func (w *Workspace) pullOnce(ctx context.Context, dryRun bool, opts PullOptions, rep *PullReport, mayCreate bool) (*PullReport, bool, error) {
	rep.SchemaIncompatibilities, rep.Conflicts = nil, nil

	// 1. upstream snapshot
	var upstreamState *snapshot.Memory
	var upstream object.Hash
	if dryRun {
		state, err := w.ReadTest(ctx)
		if err != nil {
			return nil, false, err
		}
		upstreamState = state
		prev, err := w.Repo.Upstream()
		if err != nil {
			return nil, false, err
		}
		snap, sch, err := stateHashes(state)
		if err != nil {
			return nil, false, err
		}
		if prev != "" {
			pc, err := w.Repo.Commit(prev)
			if err != nil {
				return nil, false, err
			}
			if pc.Snapshot == snap && pc.Schema == sch {
				upstream = prev
			}
		}
		rep.UpstreamChanged = upstream == ""
	} else {
		fr, err := w.Fetch(ctx)
		if err != nil {
			return nil, false, err
		}
		upstreamState = fr.State
		upstream = fr.Upstream
		rep.UpstreamChanged = fr.Changed
	}
	rep.Upstream = upstream

	// 2. working tree inside the transaction that will (maybe) apply
	local, err := w.ReadLocal(ctx, dryRun)
	if err != nil {
		return nil, false, err
	}
	defer local.Close()
	working := local.State

	headState, head, err := w.HeadState()
	if err != nil {
		return nil, false, err
	}
	rep.Head = head

	// Relation and base. In a dry run with a new upstream state there is no
	// upstream commit yet; the base is then the previous upstream's base.
	relUp := upstream
	if relUp == "" {
		if relUp, err = w.Repo.Upstream(); err != nil {
			return nil, false, err
		}
	}
	rep.Relation, rep.Base, err = w.Relate(head, relUp)
	if err != nil {
		return nil, false, err
	}
	if upstream == "" {
		// Dry run against a not-yet-recorded TEST state: it is never "up to
		// date" and can only be fast-forwarded if HEAD is at/behind the
		// previous upstream.
		switch rep.Relation {
		case RelUpToDate, RelUpstreamAhead:
			rep.Relation = RelUpstreamAhead
		case RelLocalAhead:
			rep.Relation = RelDiverged
		case RelNoUpstream:
			if head == "" {
				rep.Relation = RelUpstreamAhead
			} else {
				rep.Relation = RelUnrelated
				if rep.Base, err = w.Repo.RootCommit(head); err != nil {
					return nil, false, err
				}
			}
		}
	}

	if rep.Relation == RelUpToDate || rep.Relation == RelLocalAhead {
		rep.UpToDate = true
		rep.NewHead = head
		return rep, false, nil
	}

	// 3. schema compatibility
	rep.SchemaIncompatibilities = schema.Incompatibilities(working.Schema(), upstreamState.Schema())
	if len(rep.SchemaIncompatibilities) > 0 {
		if missing := missingTables(rep.SchemaIncompatibilities); len(missing) > 0 && len(rep.MissingTableDDL) == 0 {
			ddl, err := w.upstreamCreateStatements(ctx, missing)
			if err != nil {
				return nil, false, err
			}
			rep.MissingTableDDL = ddl
			onlyMissing := len(missing) == len(rep.SchemaIncompatibilities)
			if opts.CreateMissingTables && !dryRun && mayCreate && onlyMissing {
				local.Close() // DDL must not run while the read transaction is open
				if err := w.createLocalTables(ctx, ddl); err != nil {
					return nil, false, err
				}
				rep.CreatedTables = missing
				return rep, true, nil
			}
		}
		return rep, false, nil
	}

	// 4. merge base
	baseState, err := snapshot.FromCommit(w.Repo, rep.Base)
	if err != nil {
		return nil, false, err
	}

	// 5. merges
	mw, err := merge.Merge(baseState, working, upstreamState, working.Schema())
	if err != nil {
		return nil, false, err
	}
	rep.Stats = mw.Stats
	if !mw.Clean() {
		rep.Conflicts = mw.Conflicts
		rep.ConflictSource = "working tree"
		return rep, false, nil
	}
	rep.FastForward = rep.Relation == RelUpstreamAhead
	var mc *merge.Result
	if !rep.FastForward {
		if mc, err = merge.Merge(baseState, headState, upstreamState, working.Schema()); err != nil {
			return nil, false, err
		}
		if !mc.Clean() {
			rep.Conflicts = mc.Conflicts
			rep.ConflictSource = "HEAD"
			return rep, false, nil
		}
	}

	// 6. plan + informational diffs
	if rep.Plan, err = apply.Between(working, mw.State); err != nil {
		return nil, false, err
	}
	if rep.LocalChanges, err = diff.Compare(headState, working); err != nil {
		return nil, false, err
	}
	if rep.UpstreamChanges, err = diff.Compare(headState, upstreamState); err != nil {
		return nil, false, err
	}

	// 7. dry run stops here
	if dryRun {
		return rep, false, nil
	}

	// 8. apply + verify inside the transaction
	if err := mysql.Apply(ctx, local.Tx, working.Schema(), rep.Plan); err != nil {
		return nil, false, err
	}
	if err := mysql.Verify(ctx, local.Tx, working.Schema(), rep.Plan.Tables(), mw.State); err != nil {
		return nil, false, err
	}

	// 9. objects first (harmless if we crash before the refs move)
	var newHead object.Hash
	if rep.FastForward {
		newHead = upstream
	} else {
		snap, sch, err := w.writeState(mc.State)
		if err != nil {
			return nil, false, err
		}
		c := &object.Commit{
			Kind:      object.KindMerge,
			Parents:   []object.Hash{head, upstream},
			Snapshot:  snap,
			Schema:    sch,
			Message:   fmt.Sprintf("Merge upstream test (%s) into main", upstream.Short()),
			Timestamp: w.Now().UTC(),
			Metadata:  map[string]string{"remote": "test"},
		}
		if newHead, err = w.Repo.Put(object.TypeCommit, c); err != nil {
			return nil, false, err
		}
	}

	if err := local.Tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("committing LOCAL transaction: %w", err)
	}
	local.Tx = nil
	rep.Applied = true

	// 10. refs
	if err := w.Repo.SetHead(newHead); err != nil {
		return nil, false, fmt.Errorf("LOCAL was updated but HEAD could not be moved to %s: %w", newHead, err)
	}
	rep.NewHead = newHead
	return rep, false, nil
}
