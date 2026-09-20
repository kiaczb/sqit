// Package repo implements the on-disk .sqit repository: configuration,
// refs, HEAD, the content-addressed object store and commit-graph queries.
package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sqit/internal/object"
	"sqit/internal/schema"
)

// DirName is the repository directory name.
const DirName = ".sqit"

// Well-known refs.
const (
	MainRef     = "refs/heads/main"
	UpstreamRef = "refs/remotes/test"
)

// ErrNotFound is returned for missing objects and refs.
var ErrNotFound = errors.New("not found")

// Repository is an opened .sqit directory.
type Repository struct {
	Root   string // project directory containing .sqit
	Dir    string // the .sqit directory
	Config *Config
}

// Init creates a new repository in root.
func Init(root string, cfg *Config) (*Repository, error) {
	dir := filepath.Join(root, DirName)
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return nil, fmt.Errorf("repository already exists at %s", dir)
	}
	for _, sub := range []string{
		"refs/heads", "refs/remotes",
		"objects/" + string(object.TypeRecord),
		"objects/" + string(object.TypeManifest),
		"objects/" + string(object.TypeSnapshot),
		"objects/" + string(object.TypeSchema),
		"objects/" + string(object.TypeCommit),
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	if err := writeFileAtomic(filepath.Join(dir, "HEAD"), []byte("ref: "+MainRef+"\n")); err != nil {
		return nil, err
	}
	if err := saveConfig(filepath.Join(dir, "config.json"), cfg); err != nil {
		return nil, err
	}
	return &Repository{Root: root, Dir: dir, Config: cfg}, nil
}

// Open finds the repository in dir or any parent directory.
func Open(dir string) (*Repository, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for cur := abs; ; {
		candidate := filepath.Join(cur, DirName)
		if st, err := os.Stat(filepath.Join(candidate, "config.json")); err == nil && !st.IsDir() {
			cfg, err := loadConfig(filepath.Join(candidate, "config.json"))
			if err != nil {
				return nil, err
			}
			if err := LoadEnvFile(filepath.Join(cur, EnvFileName)); err != nil {
				return nil, err
			}
			return &Repository{Root: cur, Dir: candidate, Config: cfg}, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, fmt.Errorf("not a sqit repository (no %s directory found in %s or its parents)", DirName, abs)
		}
		cur = parent
	}
}

// ---------------------------------------------------------------------------
// refs

// HeadRef returns the symbolic ref HEAD points at.
func (r *Repository) HeadRef() (string, error) {
	b, err := os.ReadFile(filepath.Join(r.Dir, "HEAD"))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "ref: ") {
		return "", fmt.Errorf("HEAD is not a symbolic ref: %q", s)
	}
	return strings.TrimPrefix(s, "ref: "), nil
}

// Head returns the commit hash HEAD resolves to, or "" when HEAD is unborn.
func (r *Repository) Head() (object.Hash, error) {
	ref, err := r.HeadRef()
	if err != nil {
		return "", err
	}
	h, err := r.ReadRef(ref)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return h, err
}

// SetHead moves the branch HEAD points at.
func (r *Repository) SetHead(h object.Hash) error {
	ref, err := r.HeadRef()
	if err != nil {
		return err
	}
	return r.WriteRef(ref, h)
}

// Upstream returns the latest upstream commit, or "" if never fetched.
func (r *Repository) Upstream() (object.Hash, error) {
	h, err := r.ReadRef(UpstreamRef)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return h, err
}

// ReadRef reads a ref file.
func (r *Repository) ReadRef(ref string) (object.Hash, error) {
	b, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(ref)))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", ErrNotFound
	}
	return object.Hash(s), nil
}

// WriteRef writes a ref file atomically.
func (r *Repository) WriteRef(ref string, h object.Hash) error {
	p := filepath.Join(r.Dir, filepath.FromSlash(ref))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(p, []byte(string(h)+"\n"))
}

// ---------------------------------------------------------------------------
// object store

func (r *Repository) objectPath(t object.Type, h object.Hash) string {
	s := string(h)
	return filepath.Join(r.Dir, "objects", string(t), s[:2], s[2:]+".json")
}

// Has reports whether the object exists.
func (r *Repository) Has(t object.Type, h object.Hash) bool {
	_, err := os.Stat(r.objectPath(t, h))
	return err == nil
}

// Put stores v and returns its hash. Existing objects are left untouched.
func (r *Repository) Put(t object.Type, v any) (object.Hash, error) {
	b, h, err := object.Encode(v)
	if err != nil {
		return "", err
	}
	p := r.objectPath(t, h)
	if _, err := os.Stat(p); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(p, b); err != nil {
		return "", err
	}
	return h, nil
}

// PutBytes stores already-encoded object bytes under their hash.
func (r *Repository) PutBytes(t object.Type, b []byte) (object.Hash, error) {
	h := object.HashBytes(b)
	p := r.objectPath(t, h)
	if _, err := os.Stat(p); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return h, writeFileAtomic(p, b)
}

// Get loads and verifies an object.
func (r *Repository) Get(t object.Type, h object.Hash) (any, error) {
	if len(h) < 3 {
		return nil, fmt.Errorf("invalid %s hash %q", t, h)
	}
	b, err := os.ReadFile(r.objectPath(t, h))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s %s: %w", t, h.Short(), ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return object.Decode(t, h, b)
}

// Typed getters.

func (r *Repository) Record(h object.Hash) (*object.Record, error) {
	v, err := r.Get(object.TypeRecord, h)
	if err != nil {
		return nil, err
	}
	return v.(*object.Record), nil
}

func (r *Repository) Manifest(h object.Hash) (*object.Manifest, error) {
	v, err := r.Get(object.TypeManifest, h)
	if err != nil {
		return nil, err
	}
	return v.(*object.Manifest), nil
}

func (r *Repository) Snapshot(h object.Hash) (*object.Snapshot, error) {
	if h == "" {
		return object.EmptySnapshot(), nil
	}
	v, err := r.Get(object.TypeSnapshot, h)
	if err != nil {
		return nil, err
	}
	return v.(*object.Snapshot), nil
}

func (r *Repository) Schema(h object.Hash) (*schema.Schema, error) {
	if h == "" {
		return object.EmptySchema(), nil
	}
	v, err := r.Get(object.TypeSchema, h)
	if err != nil {
		return nil, err
	}
	return v.(*schema.Schema), nil
}

func (r *Repository) Commit(h object.Hash) (*object.Commit, error) {
	v, err := r.Get(object.TypeCommit, h)
	if err != nil {
		return nil, err
	}
	return v.(*object.Commit), nil
}

// ResolveCommit accepts a full or abbreviated (>= 6 hex chars) commit hash
// or a ref name (HEAD, main, test, refs/...).
func (r *Repository) ResolveCommit(s string) (object.Hash, error) {
	switch s {
	case "HEAD", "":
		h, err := r.Head()
		if err != nil {
			return "", err
		}
		if h == "" {
			return "", fmt.Errorf("HEAD has no commits yet")
		}
		return h, nil
	case "main":
		return r.ReadRef(MainRef)
	case "test", "upstream":
		h, err := r.Upstream()
		if err != nil {
			return "", err
		}
		if h == "" {
			return "", fmt.Errorf("no upstream fetched yet (run: sqit fetch)")
		}
		return h, nil
	}
	if strings.HasPrefix(s, "refs/") {
		return r.ReadRef(s)
	}
	if len(s) < 6 {
		return "", fmt.Errorf("commit reference %q is too short", s)
	}
	dir := filepath.Join(r.Dir, "objects", string(object.TypeCommit), s[:2])
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("unknown commit %q", s)
	}
	var matches []object.Hash
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		full := s[:2] + name
		if strings.HasPrefix(full, s) {
			matches = append(matches, object.Hash(full))
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("unknown commit %q", s)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("commit reference %q is ambiguous", s)
	}
}

// ---------------------------------------------------------------------------
// commit graph

// Ancestors returns the set of h and all its ancestors.
func (r *Repository) Ancestors(h object.Hash) (map[object.Hash]bool, error) {
	seen := map[object.Hash]bool{}
	queue := []object.Hash{h}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == "" || seen[cur] {
			continue
		}
		seen[cur] = true
		c, err := r.Commit(cur)
		if err != nil {
			return nil, err
		}
		queue = append(queue, c.Parents...)
	}
	return seen, nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r *Repository) IsAncestor(a, b object.Hash) (bool, error) {
	if a == "" {
		return true, nil // the empty history is everyone's ancestor
	}
	if b == "" {
		return false, nil
	}
	anc, err := r.Ancestors(b)
	if err != nil {
		return false, err
	}
	return anc[a], nil
}

// MergeBase returns the nearest common ancestor of a and b, walking
// breadth-first from b through the ancestors of a. "" means no common
// ancestor (unrelated histories or an unborn side).
func (r *Repository) MergeBase(a, b object.Hash) (object.Hash, error) {
	if a == "" || b == "" {
		return "", nil
	}
	anc, err := r.Ancestors(a)
	if err != nil {
		return "", err
	}
	seen := map[object.Hash]bool{}
	queue := []object.Hash{b}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if anc[cur] {
			return cur, nil
		}
		c, err := r.Commit(cur)
		if err != nil {
			return "", err
		}
		queue = append(queue, c.Parents...)
	}
	return "", nil
}

// RootCommit returns the root of h's first-parent chain.
func (r *Repository) RootCommit(h object.Hash) (object.Hash, error) {
	for {
		c, err := r.Commit(h)
		if err != nil {
			return "", err
		}
		if len(c.Parents) == 0 {
			return h, nil
		}
		h = c.Parents[0]
	}
}

// CountBetween counts commits reachable from tip that are not reachable
// from base (first-parent walk is not used; the full set is counted).
func (r *Repository) CountBetween(base, tip object.Hash) (int, error) {
	if tip == "" {
		return 0, nil
	}
	tipAnc, err := r.Ancestors(tip)
	if err != nil {
		return 0, err
	}
	baseAnc := map[object.Hash]bool{}
	if base != "" {
		if baseAnc, err = r.Ancestors(base); err != nil {
			return 0, err
		}
	}
	n := 0
	for h := range tipAnc {
		if !baseAnc[h] {
			n++
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Windows cannot rename over an existing file with os.Rename in all
		// cases; fall back to remove + rename.
		if rmErr := os.Remove(path); rmErr == nil {
			if err2 := os.Rename(tmpName, path); err2 == nil {
				return nil
			}
		}
		os.Remove(tmpName)
		return err
	}
	return nil
}
