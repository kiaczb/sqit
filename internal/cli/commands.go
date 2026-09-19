package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"sqit/internal/core"
	"sqit/internal/diff"
	"sqit/internal/merge"
	"sqit/internal/object"
	"sqit/internal/repo"
	"sqit/internal/schema"
	"sqit/internal/snapshot"
)

func coreDefaultConfig() *repo.Config { return repo.DefaultConfig() }

func coreInit(dir string, cfg *repo.Config) (*repo.Repository, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return repo.Init(abs, cfg)
}

func short(h object.Hash) string {
	if h == "" {
		return "(none)"
	}
	return h.Short()
}

func columnOrder(sch *schema.Schema) func(string) []string {
	return func(table string) []string {
		if t := sch.Table(table); t != nil {
			return t.ColumnNames()
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// status

func runStatus(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("status", env)
	fetch := fs.Bool("fetch", false, "snapshot TEST first")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := open(env)
	if err != nil {
		return err
	}
	rep, err := w.Status(ctx, *fetch)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(env.Stdout, statusJSON(rep))
	}
	printStatus(env.Stdout, rep)
	return nil
}

func printStatus(out io.Writer, rep *core.StatusReport) {
	added, modified, deleted := rep.Working.Counts()
	fmt.Fprintln(out, "WORKING TREE (LOCAL vs HEAD)")
	fmt.Fprintln(out, "----------------------------")
	if rep.Head == "" {
		fmt.Fprintln(out, "HEAD:     (no commits yet)")
	}
	fmt.Fprintf(out, "Modified: %d\n", modified)
	fmt.Fprintf(out, "Added:    %d\n", added)
	fmt.Fprintf(out, "Deleted:  %d\n", deleted)
	if len(rep.SchemaChanges) > 0 {
		fmt.Fprintf(out, "Schema:   %d change(s)  (sqit diff --schema)\n", len(rep.SchemaChanges))
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "UPSTREAM (TEST)")
	fmt.Fprintln(out, "---------------")
	fmt.Fprintf(out, "Local HEAD: %s\n", short(rep.Head))
	fmt.Fprintf(out, "Upstream:   %s\n", short(rep.Upstream))
	if rep.UpstreamDiff != nil {
		ua, um, ud := rep.UpstreamDiff.Counts()
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Upstream changes (vs HEAD):")
		fmt.Fprintf(out, "Modified: %d\n", um)
		fmt.Fprintf(out, "Added:    %d\n", ua)
		fmt.Fprintf(out, "Deleted:  %d\n", ud)
		if len(rep.UpstreamSchemaChanges) > 0 {
			fmt.Fprintf(out, "Schema:   %d change(s)  (sqit diff --schema --upstream)\n", len(rep.UpstreamSchemaChanges))
		}
	} else {
		fmt.Fprintln(out, "(no upstream snapshot yet – run: sqit fetch)")
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "State:")
	if rep.Dirty() {
		fmt.Fprintln(out, "  LOCAL MODIFIED")
	} else {
		fmt.Fprintln(out, "  CLEAN")
	}
	switch rep.Relation {
	case core.RelUpstreamAhead:
		fmt.Fprintf(out, "  UPSTREAM AHEAD (%d commit(s), fast-forward possible)\n", rep.UpstreamAheadBy)
	case core.RelLocalAhead:
		fmt.Fprintf(out, "  LOCAL AHEAD (%d commit(s))\n", rep.LocalAheadBy)
	case core.RelDiverged:
		fmt.Fprintf(out, "  DIVERGED (local +%d, upstream +%d)\n", rep.LocalAheadBy, rep.UpstreamAheadBy)
	default:
		fmt.Fprintf(out, "  %s\n", rep.Relation)
	}
}

func statusJSON(rep *core.StatusReport) map[string]any {
	a, m, d := rep.Working.Counts()
	out := map[string]any{
		"head":     rep.Head,
		"upstream": rep.Upstream,
		"base":     rep.Base,
		"relation": rep.Relation,
		"dirty":    rep.Dirty(),
		"workingTree": map[string]any{
			"added": a, "modified": m, "deleted": d,
			"changes":       rep.Working.Changes,
			"schemaChanges": rep.SchemaChanges,
		},
	}
	if rep.UpstreamDiff != nil {
		ua, um, ud := rep.UpstreamDiff.Counts()
		out["upstreamChanges"] = map[string]any{
			"added": ua, "modified": um, "deleted": ud,
			"changes":       rep.UpstreamDiff.Changes,
			"schemaChanges": rep.UpstreamSchemaChanges,
			"aheadBy":       rep.UpstreamAheadBy,
			"localAheadBy":  rep.LocalAheadBy,
		}
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// ---------------------------------------------------------------------------
// diff

func runDiff(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("diff", env)
	upstream := fs.Bool("upstream", false, "compare UPSTREAM with HEAD instead of the working tree")
	schemaOnly := fs.Bool("schema", false, "show schema differences")
	fetch := fs.Bool("fetch", false, "snapshot TEST first (with --upstream)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := open(env)
	if err != nil {
		return err
	}

	headState, _, err := w.HeadState()
	if err != nil {
		return err
	}
	var other snapshot.Source
	if *upstream {
		if *fetch {
			if _, err := w.Fetch(ctx); err != nil {
				return err
			}
		}
		up, h, err := w.UpstreamState()
		if err != nil {
			return err
		}
		if h == "" {
			return errors.New("no upstream snapshot yet (run: sqit fetch)")
		}
		other = up
	} else {
		local, err := w.ReadLocal(ctx, true)
		if err != nil {
			return err
		}
		defer local.Close()
		other = local.State
	}

	if *schemaOnly {
		changes := schema.Diff(headState.Schema(), other.Schema())
		if *asJSON {
			return writeJSON(env.Stdout, changes)
		}
		for _, c := range changes {
			fmt.Fprintln(env.Stdout, c.String())
		}
		return nil
	}
	d, err := diff.Compare(headState, other)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(env.Stdout, d.Changes)
	}
	d.Write(env.Stdout, columnOrder(other.Schema()))
	if sc := schema.Diff(headState.Schema(), other.Schema()); len(sc) > 0 {
		fmt.Fprintf(env.Stdout, "(%d schema change(s); see sqit diff --schema)\n", len(sc))
	}
	return nil
}

// ---------------------------------------------------------------------------
// commit

func runCommit(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("commit", env)
	msg := fs.String("m", "", "commit message")
	allowEmpty := fs.Bool("allow-empty", false, "commit even if nothing changed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*msg) == "" {
		return errors.New("a commit message is required (-m \"...\")")
	}
	w, err := open(env)
	if err != nil {
		return err
	}
	h, c, err := w.Commit(ctx, *msg, *allowEmpty)
	if errors.Is(err, core.ErrNothingToCommit) {
		fmt.Fprintln(env.Stdout, err.Error())
		return ExitError{Code: 1}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "[main %s] %s\n", h.Short(), c.Message)
	fmt.Fprintf(env.Stdout, "snapshot %s  schema %s\n", c.Snapshot.Short(), c.Schema.Short())
	return nil
}

// ---------------------------------------------------------------------------
// fetch

func runFetch(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("fetch", env)
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := open(env)
	if err != nil {
		return err
	}
	res, err := w.Fetch(ctx)
	if err != nil {
		return err
	}
	if res.Changed {
		fmt.Fprintf(env.Stdout, "upstream: %s -> %s (new TEST snapshot)\n", short(res.Previous), res.Upstream.Short())
	} else {
		fmt.Fprintf(env.Stdout, "upstream: %s (TEST unchanged)\n", res.Upstream.Short())
	}
	return nil
}

// ---------------------------------------------------------------------------
// pull

func runPull(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("pull", env)
	dryRun := fs.Bool("dry-run", false, "show what would happen without touching LOCAL or the refs")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := open(env)
	if err != nil {
		return err
	}
	rep, err := w.Pull(ctx, *dryRun)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := writeJSON(env.Stdout, pullJSON(rep)); err != nil {
			return err
		}
	} else {
		printPull(env.Stdout, rep)
	}
	if rep.Blocked() {
		return ExitError{Code: 1}
	}
	return nil
}

func printPull(out io.Writer, rep *core.PullReport) {
	if rep.DryRun {
		fmt.Fprintln(out, "Pull preview (dry run – nothing was modified)")
	} else {
		fmt.Fprintln(out, "Pull")
	}
	fmt.Fprintln(out, "====")
	fmt.Fprintf(out, "HEAD:     %s\n", short(rep.Head))
	if rep.Upstream == "" && rep.DryRun {
		fmt.Fprintln(out, "Upstream: (new TEST snapshot, not recorded in dry run)")
	} else if rep.UpstreamChanged {
		fmt.Fprintf(out, "Upstream: %s (new TEST snapshot)\n", short(rep.Upstream))
	} else {
		fmt.Fprintf(out, "Upstream: %s\n", short(rep.Upstream))
	}
	fmt.Fprintf(out, "Base:     %s\n", short(rep.Base))
	fmt.Fprintf(out, "Relation: %s\n", rep.Relation)
	fmt.Fprintln(out)

	if rep.UpToDate {
		fmt.Fprintln(out, "Already up to date.")
		return
	}
	if len(rep.SchemaIncompatibilities) > 0 {
		fmt.Fprintln(out, "ABORTED: LOCAL and TEST schemas are incompatible; sqit does not alter tables.")
		for _, c := range rep.SchemaIncompatibilities {
			fmt.Fprintf(out, "  %s\n", c.String())
		}
		return
	}
	if len(rep.Conflicts) > 0 {
		fmt.Fprintf(out, "CONFLICTS: %d (%s vs upstream) – nothing was applied\n\n", len(rep.Conflicts), rep.ConflictSource)
		merge.WriteConflicts(out, rep.Conflicts)
		if rep.ConflictSource == "HEAD" {
			fmt.Fprintln(out, "Committed local changes conflict with upstream while the working tree does not;")
			fmt.Fprintln(out, "commit the working tree (sqit commit) and pull again, or resolve in LOCAL.")
		} else {
			fmt.Fprintln(out, "Resolve by changing the conflicting values in LOCAL (or TEST) and pull again.")
		}
		return
	}

	if rep.LocalChanges != nil && !rep.LocalChanges.Empty() {
		a, m, d := rep.LocalChanges.Counts()
		fmt.Fprintf(out, "Uncommitted local changes kept: modified %d, added %d, deleted %d\n", m, a, d)
	}
	if rep.UpstreamChanges != nil {
		a, m, d := rep.UpstreamChanges.Counts()
		fmt.Fprintf(out, "Upstream changes vs HEAD:       modified %d, added %d, deleted %d\n", m, a, d)
	}
	fmt.Fprintf(out, "Merge: unchanged %d, local-only %d, upstream-only %d, same %d, field-merged %d\n",
		rep.Stats.Unchanged, rep.Stats.LocalOnly, rep.Stats.UpstreamOnly, rep.Stats.Same, rep.Stats.Merged)
	fmt.Fprintln(out)

	ins, upd, del := rep.Plan.Counts()
	if rep.DryRun {
		fmt.Fprintf(out, "Planned LOCAL changes: INSERT %d, UPDATE %d, DELETE %d\n", ins, upd, del)
	} else {
		fmt.Fprintf(out, "Applied LOCAL changes: INSERT %d, UPDATE %d, DELETE %d\n", ins, upd, del)
	}
	rep.Plan.Write(out)
	fmt.Fprintln(out)
	switch {
	case rep.DryRun && rep.FastForward:
		fmt.Fprintln(out, "Would fast-forward HEAD to upstream.")
	case rep.DryRun:
		fmt.Fprintln(out, "Would create a merge commit.")
	case rep.FastForward:
		fmt.Fprintf(out, "Fast-forwarded HEAD to %s.\n", rep.NewHead.Short())
	default:
		fmt.Fprintf(out, "Merge commit %s created.\n", rep.NewHead.Short())
	}
}

func pullJSON(rep *core.PullReport) map[string]any {
	out := map[string]any{
		"dryRun":                  rep.DryRun,
		"head":                    rep.Head,
		"upstream":                rep.Upstream,
		"base":                    rep.Base,
		"relation":                rep.Relation,
		"upstreamChanged":         rep.UpstreamChanged,
		"upToDate":                rep.UpToDate,
		"fastForward":             rep.FastForward,
		"applied":                 rep.Applied,
		"newHead":                 rep.NewHead,
		"conflicts":               rep.Conflicts,
		"conflictSource":          rep.ConflictSource,
		"schemaIncompatibilities": rep.SchemaIncompatibilities,
		"stats":                   rep.Stats,
	}
	if rep.Plan != nil {
		out["plan"] = rep.Plan.Ops
	}
	if rep.LocalChanges != nil {
		out["localChanges"] = rep.LocalChanges.Changes
	}
	if rep.UpstreamChanges != nil {
		out["upstreamChanges"] = rep.UpstreamChanges.Changes
	}
	return out
}

// ---------------------------------------------------------------------------
// log / show / inspect

func runLog(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("log", env)
	upstream := fs.Bool("upstream", false, "show the upstream history instead of the local one")
	limit := fs.Int("n", 50, "maximum number of commits")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := open(env)
	if err != nil {
		return err
	}
	var start object.Hash
	if *upstream {
		start, err = w.Repo.Upstream()
	} else {
		start, err = w.Repo.Head()
	}
	if err != nil {
		return err
	}
	if start == "" {
		fmt.Fprintln(env.Stdout, "(no commits)")
		return nil
	}
	// First-parent walk: follows the local line through merges.
	cur := start
	for n := 0; cur != "" && n < *limit; n++ {
		c, err := w.Repo.Commit(cur)
		if err != nil {
			return err
		}
		printCommit(env.Stdout, cur, c)
		if len(c.Parents) == 0 {
			break
		}
		cur = c.Parents[0]
	}
	return nil
}

func printCommit(out io.Writer, h object.Hash, c *object.Commit) {
	fmt.Fprintf(out, "commit %s  (%s)\n", h, c.Kind)
	for _, p := range c.Parents {
		fmt.Fprintf(out, "parent %s\n", p)
	}
	fmt.Fprintf(out, "date   %s\n", c.Timestamp.UTC().Format("2006-01-02 15:04:05 UTC"))
	fmt.Fprintf(out, "snapshot %s  schema %s\n", c.Snapshot.Short(), c.Schema.Short())
	fmt.Fprintln(out)
	fmt.Fprintf(out, "    %s\n\n", c.Message)
}

func runShow(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("show", env)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ref := "HEAD"
	if fs.NArg() > 0 {
		ref = fs.Arg(0)
	}
	w, err := open(env)
	if err != nil {
		return err
	}
	h, err := w.Repo.ResolveCommit(ref)
	if err != nil {
		return err
	}
	c, err := w.Repo.Commit(h)
	if err != nil {
		return err
	}
	printCommit(env.Stdout, h, c)
	var parent object.Hash
	if len(c.Parents) > 0 {
		parent = c.Parents[0]
	}
	from, err := snapshot.FromCommit(w.Repo, parent)
	if err != nil {
		return err
	}
	to, err := snapshot.FromCommit(w.Repo, h)
	if err != nil {
		return err
	}
	for _, ch := range schema.Diff(from.Schema(), to.Schema()) {
		fmt.Fprintln(env.Stdout, "schema:", ch.String())
	}
	d, err := diff.Compare(from, to)
	if err != nil {
		return err
	}
	d.Write(env.Stdout, columnOrder(to.Schema()))
	return nil
}

func runInspect(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("inspect", env)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: sqit inspect <records|manifests|snapshots|schemas|commits> <hash>")
	}
	w, err := open(env)
	if err != nil {
		return err
	}
	t := object.Type(fs.Arg(0))
	h := object.Hash(fs.Arg(1))
	if t == object.TypeCommit {
		if h, err = w.Repo.ResolveCommit(fs.Arg(1)); err != nil {
			return err
		}
	}
	v, err := w.Repo.Get(t, h)
	if err != nil {
		return err
	}
	return writeJSON(env.Stdout, v)
}
