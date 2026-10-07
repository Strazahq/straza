package policy

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/fxamacker/cbor/v2"
)

// SnapshotFormat is the on-wire snapshot schema version. It changes only
// through the spec process, when the compiled shape changes.
const SnapshotFormat = 1

// Snapshot is the distributable, signed policy bundle. It is
// content-addressed by the hash of its signed bytes and carries everything a
// PDP needs to build an Engine offline. The DB is never on the decision path.
type Snapshot struct {
	Format       int      `cbor:"format"`
	Documents    [][]byte `cbor:"documents"`    // canonical YAML of each active set
	LocalDefault string   `cbor:"localDefault"` // profile default for local tools
	MaxAgeSecs   int64    `cbor:"maxAgeSecs"`   // offline grace bound
	CreatedUnix  int64    `cbor:"createdUnix"`
}

// SignedSnapshot wraps a CBOR-encoded Snapshot with an ed25519 signature.
type SignedSnapshot struct {
	Format    int    `cbor:"format"`
	Payload   []byte `cbor:"payload"` // CBOR(Snapshot)
	KeyID     string `cbor:"keyId"`
	Signature []byte `cbor:"sig"`
}

// CompileInput is the compiler's input: the active PolicySet documents (raw
// YAML) plus profile context.
type CompileInput struct {
	Documents    [][]byte
	LocalDefault string
	MaxAge       int64 // seconds; snapshot embeds it as the grace bound
	CreatedUnix  int64 // pass in (Date.now is unavailable in some contexts)
}

// Compile validates every document, canonicalizes them (sorted, re-encoded),
// and returns the unsigned Snapshot. Validation failure aborts the compile:
// a snapshot never contains an unparseable set.
func Compile(in CompileInput) (Snapshot, error) {
	if in.LocalDefault != EffectAllow && in.LocalDefault != EffectDeny {
		return Snapshot{}, fmt.Errorf("policy: compile: localDefault must be allow or deny, got %q", in.LocalDefault)
	}
	type named struct {
		name string
		raw  []byte
	}
	docs := make([]named, 0, len(in.Documents))
	seen := map[string]bool{}
	for i, raw := range in.Documents {
		doc, err := Parse(raw)
		if err != nil {
			return Snapshot{}, fmt.Errorf("policy: compile document %d: %w", i, err)
		}
		if seen[doc.Metadata.Name] {
			return Snapshot{}, fmt.Errorf("policy: compile: duplicate PolicySet name %q", doc.Metadata.Name)
		}
		seen[doc.Metadata.Name] = true
		docs = append(docs, named{name: doc.Metadata.Name, raw: raw})
	}
	// Deterministic order by set name: identical inputs → identical bytes →
	// identical content hash.
	sort.Slice(docs, func(i, j int) bool { return docs[i].name < docs[j].name })
	ordered := make([][]byte, len(docs))
	for i := range docs {
		ordered[i] = docs[i].raw
	}
	return Snapshot{
		Format:       SnapshotFormat,
		Documents:    ordered,
		LocalDefault: in.LocalDefault,
		MaxAgeSecs:   in.MaxAge,
		CreatedUnix:  in.CreatedUnix,
	}, nil
}

// Sign CBOR-encodes and ed25519-signs the snapshot, returning the wire bytes
// and the content id (hex sha256 of those bytes).
func (s Snapshot) Sign(keyID string, priv ed25519.PrivateKey) (signed []byte, id string, err error) {
	payload, err := cbor.Marshal(s)
	if err != nil {
		return nil, "", fmt.Errorf("policy: encode snapshot: %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	ss := SignedSnapshot{Format: SnapshotFormat, Payload: payload, KeyID: keyID, Signature: sig}
	signed, err = cbor.Marshal(ss)
	if err != nil {
		return nil, "", fmt.Errorf("policy: encode signed snapshot: %w", err)
	}
	sum := sha256.Sum256(signed)
	return signed, hex.EncodeToString(sum[:]), nil
}

// KeyLookup returns the ed25519 public key for a key id, or false if unknown.
type KeyLookup func(keyID string) (ed25519.PublicKey, bool)

// OpenSnapshot verifies signed snapshot bytes against the keys from lookup,
// checks the content id, and returns a ready Engine plus the decoded
// Snapshot. Verification failure ⇒ error, and callers fail closed.
func OpenSnapshot(signed []byte, wantID string, lookup KeyLookup) (*Engine, Snapshot, error) {
	sum := sha256.Sum256(signed)
	if got := hex.EncodeToString(sum[:]); wantID != "" && got != wantID {
		return nil, Snapshot{}, fmt.Errorf("policy: snapshot id mismatch: got %s want %s", got, wantID)
	}
	var ss SignedSnapshot
	if err := cbor.Unmarshal(signed, &ss); err != nil {
		return nil, Snapshot{}, fmt.Errorf("policy: decode signed snapshot: %w", err)
	}
	pub, ok := lookup(ss.KeyID)
	if !ok {
		return nil, Snapshot{}, fmt.Errorf("policy: unknown snapshot signing key %q", ss.KeyID)
	}
	if !ed25519.Verify(pub, ss.Payload, ss.Signature) {
		return nil, Snapshot{}, fmt.Errorf("policy: snapshot signature invalid (tampered or wrong key)")
	}
	var snap Snapshot
	if err := cbor.Unmarshal(ss.Payload, &snap); err != nil {
		return nil, Snapshot{}, fmt.Errorf("policy: decode snapshot payload: %w", err)
	}
	eng, err := snap.Engine()
	if err != nil {
		return nil, Snapshot{}, err
	}
	return eng, snap, nil
}

// Engine builds an evaluator from a decoded (already-trusted) snapshot.
func (s Snapshot) Engine() (*Engine, error) {
	docs := make([]Document, 0, len(s.Documents))
	for i, raw := range s.Documents {
		doc, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("policy: snapshot document %d: %w", i, err)
		}
		docs = append(docs, doc)
	}
	def := s.LocalDefault
	if def == "" {
		def = EffectAllow
	}
	return NewEngine(docs, def)
}
