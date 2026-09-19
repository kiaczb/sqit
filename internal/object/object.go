// Package object defines the content-addressed objects of a sqit repository
// (record, manifest, snapshot, schema, commit) and how they are hashed.
//
// An object's bytes are its canonical JSON encoding followed by "\n"; its
// hash is the lowercase hex SHA-256 of exactly those bytes.
package object

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"sqit/internal/canonical"
	"sqit/internal/schema"
)

// Hash is the hex SHA-256 name of an object.
type Hash string

// Type names the object kinds; they double as store sub-directories.
type Type string

const (
	TypeRecord   Type = "records"
	TypeManifest Type = "manifests"
	TypeSnapshot Type = "snapshots"
	TypeSchema   Type = "schemas"
	TypeCommit   Type = "commits"
)

// Short returns the abbreviated hash used in human output.
func (h Hash) Short() string {
	if len(h) > 12 {
		return string(h[:12])
	}
	return string(h)
}

// Encode returns the object bytes (canonical JSON + newline) and their hash.
func Encode(v any) ([]byte, Hash, error) {
	b, err := canonical.Encode(v)
	if err != nil {
		return nil, "", err
	}
	b = append(b, '\n')
	return b, HashBytes(b), nil
}

// HashBytes hashes raw object bytes.
func HashBytes(b []byte) Hash {
	sum := sha256.Sum256(b)
	return Hash(hex.EncodeToString(sum[:]))
}

// HashOf returns the hash of v's canonical encoding.
func HashOf(v any) (Hash, error) {
	_, h, err := Encode(v)
	return h, err
}

// ---------------------------------------------------------------------------
// record

// Record is one row of one table in canonical form.
type Record struct {
	Table string                     `json:"table"`
	PK    map[string]canonical.Value `json:"pk"`
	Data  map[string]canonical.Value `json:"data"`
}

// Canonical implements canonical.Canonicalizer.
func (r *Record) Canonical() any {
	return map[string]any{
		"table": r.Table,
		"pk":    r.PK,
		"data":  r.Data,
	}
}

// Key returns the manifest key of the record for the given PK column order.
func (r *Record) Key(pkColumns []string) canonical.PKKey {
	vals := make([]canonical.Value, len(pkColumns))
	for i, c := range pkColumns {
		vals[i] = r.PK[c]
	}
	return canonical.MakePKKey(vals)
}

// ---------------------------------------------------------------------------
// manifest

// Manifest maps every primary key of a table to its record object.
type Manifest struct {
	Table   string                   `json:"table"`
	Records map[canonical.PKKey]Hash `json:"records"`
}

// Canonical implements canonical.Canonicalizer.
func (m *Manifest) Canonical() any {
	recs := make(map[string]any, len(m.Records))
	for k, h := range m.Records {
		recs[string(k)] = string(h)
	}
	return map[string]any{
		"table":   m.Table,
		"records": recs,
	}
}

// ---------------------------------------------------------------------------
// snapshot

// Snapshot maps every tracked table to its manifest object.
type Snapshot struct {
	Tables map[string]Hash `json:"tables"`
}

// Canonical implements canonical.Canonicalizer.
func (s *Snapshot) Canonical() any {
	t := make(map[string]any, len(s.Tables))
	for k, h := range s.Tables {
		t[k] = string(h)
	}
	return map[string]any{"tables": t}
}

// EmptySnapshot is the snapshot with no tables (unborn HEAD, no merge base).
func EmptySnapshot() *Snapshot { return &Snapshot{Tables: map[string]Hash{}} }

// EmptySchema is the schema with no tables.
func EmptySchema() *schema.Schema { return &schema.Schema{} }

// ---------------------------------------------------------------------------
// commit

// CommitKind classifies commits.
type CommitKind string

const (
	KindLocal    CommitKind = "local"
	KindMerge    CommitKind = "merge"
	KindUpstream CommitKind = "upstream"
)

// Commit is a node in the history graph.
type Commit struct {
	Kind      CommitKind        `json:"kind"`
	Parents   []Hash            `json:"parents"`
	Snapshot  Hash              `json:"snapshot"`
	Schema    Hash              `json:"schema"`
	Message   string            `json:"message"`
	Timestamp time.Time         `json:"timestamp"`
	Metadata  map[string]string `json:"metadata"`
}

// Canonical implements canonical.Canonicalizer.
func (c *Commit) Canonical() any {
	parents := make([]string, len(c.Parents))
	for i, p := range c.Parents {
		parents[i] = string(p)
	}
	meta := c.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	return map[string]any{
		"kind":      string(c.Kind),
		"parents":   parents,
		"snapshot":  string(c.Snapshot),
		"schema":    string(c.Schema),
		"message":   c.Message,
		"timestamp": c.Timestamp.UTC().Format(time.RFC3339),
		"metadata":  meta,
	}
}

// Decode parses object bytes of the given type. The hash is verified.
func Decode(t Type, want Hash, b []byte) (any, error) {
	if got := HashBytes(b); want != "" && got != want {
		return nil, fmt.Errorf("object %s/%s is corrupt (hash %s)", t, want.Short(), got.Short())
	}
	var v any
	switch t {
	case TypeRecord:
		v = &Record{}
	case TypeManifest:
		v = &Manifest{}
	case TypeSnapshot:
		v = &Snapshot{}
	case TypeSchema:
		v = &schema.Schema{}
	case TypeCommit:
		v = &Commit{}
	default:
		return nil, fmt.Errorf("unknown object type %q", t)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return nil, fmt.Errorf("decoding %s %s: %w", t, want.Short(), err)
	}
	return v, nil
}
