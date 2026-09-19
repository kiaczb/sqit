package core

import (
	"context"

	"sqit/internal/diff"
	"sqit/internal/object"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

// StatusReport is the result of `sqit status`.
type StatusReport struct {
	Head     object.Hash
	Upstream object.Hash
	Base     object.Hash
	Relation Relation

	// Working tree vs HEAD.
	Working       *diff.Diff
	SchemaChanges []schema.Change

	// Upstream vs HEAD (nil when there is no upstream).
	UpstreamDiff          *diff.Diff
	UpstreamSchemaChanges []schema.Change
	UpstreamAheadBy       int
	LocalAheadBy          int

	// Sources, for callers that want to print diffs.
	WorkingState  *snapshot.Memory
	HeadState     *snapshot.Repo
	UpstreamState *snapshot.Repo
}

// Dirty reports whether the working tree differs from HEAD.
func (s *StatusReport) Dirty() bool {
	return !s.Working.Empty() || len(s.SchemaChanges) > 0
}

// Status reads the working tree and compares it with HEAD and upstream.
// With fetch set, TEST is snapshotted first.
func (w *Workspace) Status(ctx context.Context, fetch bool) (*StatusReport, error) {
	if fetch {
		if _, err := w.Fetch(ctx); err != nil {
			return nil, err
		}
	}
	local, err := w.ReadLocal(ctx, true)
	if err != nil {
		return nil, err
	}
	defer local.Close()
	return w.statusFrom(local.State)
}

func (w *Workspace) statusFrom(working *snapshot.Memory) (*StatusReport, error) {
	headState, head, err := w.HeadState()
	if err != nil {
		return nil, err
	}
	upState, up, err := w.UpstreamState()
	if err != nil {
		return nil, err
	}
	rep := &StatusReport{Head: head, Upstream: up, WorkingState: working, HeadState: headState, UpstreamState: upState}

	if rep.Working, err = diff.Compare(headState, working); err != nil {
		return nil, err
	}
	rep.SchemaChanges = schema.Diff(headState.Schema(), working.Schema())

	rep.Relation, rep.Base, err = w.Relate(head, up)
	if err != nil {
		return nil, err
	}
	if upState != nil {
		if rep.UpstreamDiff, err = diff.Compare(headState, upState); err != nil {
			return nil, err
		}
		rep.UpstreamSchemaChanges = schema.Diff(headState.Schema(), upState.Schema())
		if rep.UpstreamAheadBy, err = w.Repo.CountBetween(rep.Base, up); err != nil {
			return nil, err
		}
		if rep.LocalAheadBy, err = w.Repo.CountBetween(rep.Base, head); err != nil {
			return nil, err
		}
	}
	return rep, nil
}
