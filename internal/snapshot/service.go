// Package snapshot is the control-plane side of policy distribution: it
// compiles the active PolicySet documents into a signed,
// content-addressed snapshot, persists and activates it, and holds the
// live in-memory Engine that the PDP evaluates against (the DB is never
// on the decision path).
package snapshot

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// Signer supplies the active snapshot signing key and resolves any key id to
// a public key for verification (backed by store.SigningKeyRepo).
type Signer interface {
	Active(ctx context.Context) (kid string, priv ed25519.PrivateKey, err error)
	Public(ctx context.Context, kid string) (ed25519.PublicKey, bool)
}

// Publisher emits the policy.updated event when a new snapshot activates.
type Publisher func(ctx context.Context, subject string, data map[string]any)

// Current is an atomically-swappable active snapshot: the compiled Engine
// plus its wire bytes and id, served without touching the DB.
type Current struct {
	Engine *policy.Engine
	ID     string
	Signed []byte
	MaxAge int64
}

// Service compiles, signs, activates, and serves snapshots.
type Service struct {
	store     store.Store
	signer    Signer
	publish   Publisher
	localDef  string
	graceSecs int64
	log       *slog.Logger

	// mu serializes this process's Recompile, which reads the stored sets
	// and swaps in the snapshot it built from them.
	mu      sync.Mutex
	current atomic.Pointer[Current]
}

// New builds the service. localDefault is the profile default for local
// tools, graceSecs is the offline grace bound embedded in snapshots, and
// log (nil = silent) carries the swap transition lines.
func New(st store.Store, signer Signer, publish Publisher, localDefault string, graceSecs int64, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: st, signer: signer, publish: publish, localDef: localDefault, graceSecs: graceSecs, log: log}
}

// Load activates the newest persisted snapshot, or compiles an empty one if
// none exists, so the PDP always has a live Engine at boot (fail-closed:
// with no snapshot every governed action would deny; an empty snapshot
// applies profile defaults). A live Rego module that calls a built-in the
// policy compiler refuses fails Load with the sentence of bootRefusal.
func (s *Service) Load(ctx context.Context) error {
	active, err := s.store.Snapshots().GetActive(ctx)
	if err == nil {
		eng, snap, err := policy.OpenSnapshot(active.Blob, active.ID, s.publicLookup(ctx))
		if err != nil {
			return bootRefusal(fmt.Errorf("snapshot: load active %s: %w", active.ID, err))
		}
		s.swap(&Current{Engine: eng, ID: active.ID, Signed: active.Blob, MaxAge: snap.MaxAgeSecs}, "load")
		return nil
	}
	// No active snapshot: compile whatever active policy sets exist (possibly
	// none) so the engine reflects current state.
	_, err = s.Recompile(ctx)
	return bootRefusal(err)
}

// bootRefusal words a boot whose live policy holds a Rego module that calls
// a refused built-in. Only an earlier release can publish such a policy, and
// only that release can open it, so the way out runs through it. Every other
// err, nil included, is answered as it is.
func bootRefusal(err error) error {
	var refused *policy.RefusedBuiltinError
	if !errors.As(err, &refused) {
		return err
	}
	return fmt.Errorf("snapshot: %w. strazad did not start, because the live policy holds that module. "+
		"Start the release you upgraded from, remove the call or turn the set off and publish, then start this release again", refused)
}

// Current returns the live snapshot, or nil before Load.
func (s *Service) Current() *Current {
	return s.current.Load()
}

// Recompile builds a snapshot from the stored source of every active set,
// signs, persists and activates it, swaps the live Engine, and emits
// policy.updated. It is the boot path for a store with no snapshot yet: an
// admin publish builds with Build, which never reads another set's stored
// source. Returns the new snapshot id.
func (s *Service) Recompile(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets, err := s.store.Policies().List(ctx)
	if err != nil {
		return "", fmt.Errorf("snapshot: list policies: %w", err)
	}
	var docs [][]byte
	for _, ps := range sets {
		if ps.Status == "active" {
			docs = append(docs, []byte(ps.YAMLSource))
		}
	}
	return s.install(ctx, docs)
}

// activeUnreadable words a failed read of the active snapshot by a publish
// of what, the set names it changes.
func activeUnreadable(what string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("snapshot: publish %s: no policy snapshot is active, so the text the other sets were published with is unknown and nothing was published. Restart strazad so it builds its snapshot, then publish again", what)
	}
	return fmt.Errorf("snapshot: publish %s: read the active snapshot: %w", what, err)
}

// activeUnopenable words an active snapshot id that the snapshot keys
// cannot open, met by a publish of what.
func activeUnopenable(what, id string, err error) error {
	return fmt.Errorf("snapshot: publish %s: the active snapshot %s cannot be opened with the snapshot signing keys (%v), so the text the other sets were published with is unknown and nothing was published. The snapshot or its signing key changed outside strazad: restore both from a backup, then publish again", what, id, err)
}

// Built is a snapshot compiled, signed and verified but not stored. The
// publish transaction stores and activates it, and Adopt makes it this
// process's live snapshot after that transaction committed. Base is the
// active snapshot it was built on.
type Built struct {
	ID, SignerKeyID, Base string
	Blob                  []byte
	Engine                *policy.Engine
	MaxAge                int64
	Sets                  int
}

// Build compiles the successor of the active snapshot base with the named
// sets changed: a name mapped to text enters the snapshot or replaces the
// set of that name, and a name mapped to nil leaves it. Every other set
// keeps the text it was published with. It stores, activates and emits
// nothing. It refuses when the store's active snapshot is not base, and,
// with no active snapshot or one the snapshot keys cannot open, with the
// sentences of activeUnreadable and activeUnopenable. It also refuses a
// text whose set name is not the name it is mapped to.
func (s *Service) Build(ctx context.Context, base string, changes map[string][]byte) (Built, error) {
	names := make([]string, 0, len(changes))
	for name := range changes {
		names = append(names, name)
	}
	sort.Strings(names)
	what := strings.Join(names, ", ")
	act, err := s.store.Snapshots().GetActive(ctx)
	if err != nil {
		return Built{}, activeUnreadable(what, err)
	}
	if act.ID != base {
		return Built{}, fmt.Errorf("snapshot: publish %s: the change was checked against the policy snapshot %s, and %s is active now, so nothing was built. Check the change again, then publish it", what, base, act.ID)
	}
	_, running, err := policy.OpenSnapshot(act.Blob, act.ID, s.publicLookup(ctx))
	if err != nil {
		return Built{}, activeUnopenable(what, act.ID, err)
	}
	docs := make([][]byte, 0, len(running.Documents)+len(changes))
	for _, raw := range running.Documents {
		doc, err := policy.Parse(raw)
		if err != nil {
			return Built{}, fmt.Errorf("snapshot: publish %s: a set in the active snapshot %s does not parse: %w", what, act.ID, err)
		}
		if _, changed := changes[doc.Metadata.Name]; !changed {
			docs = append(docs, raw)
		}
	}
	for _, name := range names {
		text := changes[name]
		if text == nil {
			continue
		}
		// A text that names another set would replace that set while the
		// set of its key stayed, and the map says otherwise.
		doc, err := policy.Parse(text)
		if err != nil {
			return Built{}, fmt.Errorf("snapshot: publish %s: the text given for the set %s does not parse, so nothing was built: %w", what, name, err)
		}
		if doc.Metadata.Name != name {
			return Built{}, fmt.Errorf("snapshot: publish %s: the text given for the set %s names the set %s, so nothing was built", what, name, doc.Metadata.Name)
		}
		docs = append(docs, text)
	}
	b, err := s.seal(ctx, docs)
	if err != nil {
		return Built{}, err
	}
	b.Base = act.ID
	return b, nil
}

// Adopt makes b the live snapshot of this process. The caller committed the
// transaction that activated b, which also carried straza.policy.updated,
// so Adopt emits nothing.
func (s *Service) Adopt(b Built) {
	s.swap(&Current{Engine: b.Engine, ID: b.ID, Signed: b.Blob, MaxAge: b.MaxAge}, "publish")
}

// AdoptLoaded makes b, the active snapshot another process published and
// this one read from the store, the live snapshot of this process, as Load
// does. It emits nothing and logs the swap with the path word "load", so
// the line tells it from this process's own publish.
func (s *Service) AdoptLoaded(b Built) {
	s.swap(&Current{Engine: b.Engine, ID: b.ID, Signed: b.Blob, MaxAge: b.MaxAge}, "load")
}

// seal compiles docs into a snapshot, signs it with the active snapshot key
// and opens it again, so that no snapshot this process cannot open is ever
// stored or swapped in.
func (s *Service) seal(ctx context.Context, docs [][]byte) (Built, error) {
	snap, err := policy.Compile(policy.CompileInput{
		Documents:    docs,
		LocalDefault: s.localDef,
		MaxAge:       s.graceSecs,
		CreatedUnix:  time.Now().Unix(),
	})
	if err != nil {
		return Built{}, err
	}
	kid, priv, err := s.signer.Active(ctx)
	if err != nil {
		return Built{}, fmt.Errorf("snapshot: signing key: %w", err)
	}
	signed, id, err := snap.Sign(kid, priv)
	if err != nil {
		return Built{}, err
	}
	eng, _, err := policy.OpenSnapshot(signed, id, s.publicLookup(ctx))
	if err != nil {
		return Built{}, fmt.Errorf("snapshot: self-verify: %w", err)
	}
	return Built{ID: id, SignerKeyID: kid, Blob: signed, Engine: eng, MaxAge: snap.MaxAgeSecs, Sets: len(docs)}, nil
}

// install seals docs into a snapshot, persists and activates it, swaps the
// live Engine and emits policy.updated. The caller holds mu.
func (s *Service) install(ctx context.Context, docs [][]byte) (string, error) {
	b, err := s.seal(ctx, docs)
	if err != nil {
		return "", err
	}
	id := b.ID
	if _, err := s.store.Snapshots().Create(ctx, store.Snapshot{
		ID: id, SignerKeyID: b.SignerKeyID, Blob: b.Blob,
	}); err != nil {
		// The id is a content hash and ed25519 signing is deterministic, so
		// recompiling back to a previously-persisted policy state (e.g.
		// activate then deactivate within one CreatedUnix second) reproduces
		// an EXISTING row byte for byte. That is not a failure, so reuse it:
		// refusing here would fail the deactivate with a 500 AND leave the
		// superseded snapshot enforcing.
		if !errors.Is(err, store.ErrConflict) {
			return "", fmt.Errorf("snapshot: persist: %w", err)
		}
		if _, getErr := s.store.Snapshots().GetByID(ctx, id); getErr != nil {
			return "", fmt.Errorf("snapshot: persist: %w", err)
		}
	}
	if err := s.store.Snapshots().SetActive(ctx, id); err != nil {
		return "", fmt.Errorf("snapshot: activate: %w", err)
	}

	s.swap(&Current{Engine: b.Engine, ID: id, Signed: b.Blob, MaxAge: b.MaxAge}, "recompile")
	if s.publish != nil {
		s.publish(ctx, "straza.policy.updated", map[string]any{"snapshot": id, "sets": len(docs)})
	}
	return id, nil
}

func (s *Service) publicLookup(ctx context.Context) policy.KeyLookup {
	return func(kid string) (ed25519.PublicKey, bool) {
		return s.signer.Public(ctx, kid)
	}
}

// swap installs cur as the live snapshot and logs the transition tiered by
// volume: a real change of id is Info (rare, and the operator
// wants to see it), the first install at boot and a reload of the same id
// are Debug. source names the path: "load", "recompile" or "publish".
func (s *Service) swap(cur *Current, source string) {
	prev := s.current.Swap(cur)
	switch {
	case prev == nil:
		s.log.Debug("policy snapshot loaded", "component", "snapshot", "id", cur.ID, "source", source)
	case prev.ID != cur.ID:
		s.log.Info("policy snapshot swapped", "component", "snapshot", "id", cur.ID, "previous", prev.ID, "source", source)
	default:
		s.log.Debug("policy snapshot unchanged", "component", "snapshot", "id", cur.ID, "source", source)
	}
}
