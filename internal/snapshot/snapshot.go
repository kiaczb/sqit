// Package snapshot provides a uniform, lazily loaded view over a database
// state – whether it lives in memory (the working tree just read from
// MySQL, or a merge result) or in the repository (HEAD, upstream, base).
//
// Diff and merge operate on Source so they compare manifests by hash first
// and only touch the records that actually differ.
package snapshot

import (
	"fmt"
	"sort"

	"sqit/internal/canonical"
	"sqit/internal/object"
	"sqit/internal/repo"
	"sqit/internal/schema"
)

// Table is the manifest of one table plus its primary-key column order.
type Table struct {
	Name      string
	PKColumns []string
	Records   map[canonical.PKKey]object.Hash
}

// Keys returns the record keys in display order.
func (t *Table) Keys() []canonical.PKKey {
	keys := make([]canonical.PKKey, 0, len(t.Records))
	for k := range t.Records {
		keys = append(keys, k)
	}
	canonical.SortPKKeys(keys)
	return keys
}

// Source is a readable database state.
type Source interface {
	// TableNames lists the tables present, sorted.
	TableNames() []string
	// Table returns the table's manifest, or nil if the table is absent.
	Table(name string) (*Table, error)
	// Record loads a record object by hash.
	Record(h object.Hash) (*object.Record, error)
	// Schema returns the schema the state was read with.
	Schema() *schema.Schema
	// ManifestHash returns the hash of the table's manifest object, or ""
	// when the table is absent. Equal hashes mean identical table contents.
	ManifestHash(name string) (object.Hash, error)
}

// ---------------------------------------------------------------------------
// in-memory state

// Memory is a fully materialised state.
type Memory struct {
	schema  *schema.Schema
	tables  map[string]*Table
	records map[object.Hash]*object.Record
}

// NewMemory creates an empty state for the given schema. Every table of the
// schema is present (possibly with no records).
func NewMemory(sch *schema.Schema) *Memory {
	m := &Memory{
		schema:  sch,
		tables:  map[string]*Table{},
		records: map[object.Hash]*object.Record{},
	}
	for _, t := range sch.Tables {
		m.tables[t.Name] = &Table{Name: t.Name, PKColumns: append([]string(nil), t.PrimaryKey...), Records: map[canonical.PKKey]object.Hash{}}
	}
	return m
}

// Add stores a record and registers it in its table's manifest.
func (m *Memory) Add(rec *object.Record) (object.Hash, error) {
	t := m.tables[rec.Table]
	if t == nil {
		return "", fmt.Errorf("snapshot: table %q is not part of the schema", rec.Table)
	}
	h, err := object.HashOf(rec)
	if err != nil {
		return "", err
	}
	key := rec.Key(t.PKColumns)
	if prev, dup := t.Records[key]; dup && prev != h {
		return "", fmt.Errorf("snapshot: duplicate primary key %s in %s", key, rec.Table)
	}
	t.Records[key] = h
	m.records[h] = rec
	return h, nil
}

// AddHash registers an already stored record (shared with another source).
func (m *Memory) AddHash(table string, key canonical.PKKey, h object.Hash, rec *object.Record) {
	t := m.tables[table]
	if t == nil {
		t = &Table{Name: table, Records: map[canonical.PKKey]object.Hash{}}
		if st := m.schema.Table(table); st != nil {
			t.PKColumns = st.PrimaryKey
		}
		m.tables[table] = t
	}
	t.Records[key] = h
	if rec != nil {
		m.records[h] = rec
	}
}

// Delete removes a record from the manifest.
func (m *Memory) Delete(table string, key canonical.PKKey) {
	if t := m.tables[table]; t != nil {
		delete(t.Records, key)
	}
}

func (m *Memory) TableNames() []string {
	names := make([]string, 0, len(m.tables))
	for n := range m.tables {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (m *Memory) Table(name string) (*Table, error) { return m.tables[name], nil }

func (m *Memory) Record(h object.Hash) (*object.Record, error) {
	r, ok := m.records[h]
	if !ok {
		return nil, fmt.Errorf("snapshot: record %s not in memory", h.Short())
	}
	return r, nil
}

func (m *Memory) Schema() *schema.Schema { return m.schema }

func (m *Memory) ManifestHash(name string) (object.Hash, error) {
	t := m.tables[name]
	if t == nil {
		return "", nil
	}
	return object.HashOf(&object.Manifest{Table: name, Records: t.Records})
}

// Hash computes the snapshot object and its hash without writing anything.
func (m *Memory) Hash() (*object.Snapshot, object.Hash, error) {
	snap := &object.Snapshot{Tables: map[string]object.Hash{}}
	for name, t := range m.tables {
		mh, err := object.HashOf(&object.Manifest{Table: name, Records: t.Records})
		if err != nil {
			return nil, "", err
		}
		snap.Tables[name] = mh
	}
	h, err := object.HashOf(snap)
	return snap, h, err
}

// Write stores every record, manifest and the snapshot in the repository
// (objects that already exist are skipped) and returns the snapshot hash.
func (m *Memory) Write(r *repo.Repository) (object.Hash, error) {
	for name, t := range m.tables {
		for _, h := range t.Records {
			if r.Has(object.TypeRecord, h) {
				continue
			}
			rec, ok := m.records[h]
			if !ok {
				return "", fmt.Errorf("snapshot: record %s of %s is neither in memory nor in the repository", h.Short(), name)
			}
			if _, err := r.Put(object.TypeRecord, rec); err != nil {
				return "", err
			}
		}
		if _, err := r.Put(object.TypeManifest, &object.Manifest{Table: name, Records: t.Records}); err != nil {
			return "", err
		}
	}
	snap, _, err := m.Hash()
	if err != nil {
		return "", err
	}
	return r.Put(object.TypeSnapshot, snap)
}

// ---------------------------------------------------------------------------
// repository-backed state

// Repo is a lazily loaded state stored in the repository.
type Repo struct {
	repo     *repo.Repository
	snapshot *object.Snapshot
	schema   *schema.Schema
	tables   map[string]*Table
	cache    map[object.Hash]*object.Record
}

// FromCommit opens the state a commit points at. An empty hash yields the
// empty state.
func FromCommit(r *repo.Repository, commit object.Hash) (*Repo, error) {
	if commit == "" {
		return FromSnapshot(r, "", "")
	}
	c, err := r.Commit(commit)
	if err != nil {
		return nil, err
	}
	return FromSnapshot(r, c.Snapshot, c.Schema)
}

// FromSnapshot opens the state of a snapshot object read with a schema.
func FromSnapshot(r *repo.Repository, snap, sch object.Hash) (*Repo, error) {
	s, err := r.Snapshot(snap)
	if err != nil {
		return nil, err
	}
	scm, err := r.Schema(sch)
	if err != nil {
		return nil, err
	}
	return &Repo{repo: r, snapshot: s, schema: scm, tables: map[string]*Table{}, cache: map[object.Hash]*object.Record{}}, nil
}

func (s *Repo) TableNames() []string {
	names := make([]string, 0, len(s.snapshot.Tables))
	for n := range s.snapshot.Tables {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *Repo) Table(name string) (*Table, error) {
	if t, ok := s.tables[name]; ok {
		return t, nil
	}
	mh, ok := s.snapshot.Tables[name]
	if !ok {
		return nil, nil
	}
	m, err := s.repo.Manifest(mh)
	if err != nil {
		return nil, err
	}
	t := &Table{Name: name, Records: m.Records}
	if st := s.schema.Table(name); st != nil {
		t.PKColumns = st.PrimaryKey
	}
	s.tables[name] = t
	return t, nil
}

func (s *Repo) Record(h object.Hash) (*object.Record, error) {
	if r, ok := s.cache[h]; ok {
		return r, nil
	}
	r, err := s.repo.Record(h)
	if err != nil {
		return nil, err
	}
	s.cache[h] = r
	return r, nil
}

func (s *Repo) Schema() *schema.Schema { return s.schema }

func (s *Repo) ManifestHash(name string) (object.Hash, error) {
	return s.snapshot.Tables[name], nil
}

// ---------------------------------------------------------------------------

// Empty is the state with no tables.
func Empty() *Memory { return NewMemory(object.EmptySchema()) }

// TableUnion returns the sorted union of table names of the sources.
func TableUnion(sources ...Source) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sources {
		for _, n := range s.TableNames() {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// PKColumns returns the primary key columns for a table from whichever
// source knows it.
func PKColumns(name string, sources ...Source) []string {
	for _, s := range sources {
		if s == nil {
			continue
		}
		if st := s.Schema().Table(name); st != nil && len(st.PrimaryKey) > 0 {
			return st.PrimaryKey
		}
		t, err := s.Table(name)
		if err == nil && t != nil && len(t.PKColumns) > 0 {
			return t.PKColumns
		}
	}
	return nil
}
