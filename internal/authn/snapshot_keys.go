package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/store"
)

// SnapshotKeys manages the ed25519 keys that sign policy snapshots.
// Separate purpose from session tokens so the two rotate
// independently. Implements snapshot.Signer. The seed of every key is sealed
// under the KEK in the store.
type SnapshotKeys struct {
	repo   store.SigningKeyRepo
	sealer KeySealer

	mu        sync.RWMutex
	activeKID string
	active    ed25519.PrivateKey
	pubs      map[string]ed25519.PublicKey
}

// NewSnapshotKeys loads (or creates) the snapshot signing key. Every replica
// must hold the same KEK in sealer, and a stored key that does not open is an
// error.
func NewSnapshotKeys(ctx context.Context, repo store.SigningKeyRepo, sealer KeySealer) (*SnapshotKeys, error) {
	if sealer == nil {
		return nil, errNoSeedSealer
	}
	s := &SnapshotKeys{repo: repo, sealer: sealer, pubs: map[string]ed25519.PublicKey{}}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads the snapshot keys: the first active key signs, and every stored
// key verifies. A store with no active key gets a fresh one. The database
// holds one active snapshot key, so when a peer writes it first, load signs
// with the peer's key.
func (s *SnapshotKeys) load(ctx context.Context) error {
	pubs, kid, active, err := s.list(ctx)
	if err != nil {
		return err
	}
	if active == nil {
		kid, active, err = s.generate(ctx)
		switch {
		case errors.Is(err, store.ErrConflict):
			if pubs, kid, active, err = s.list(ctx); err != nil {
				return err
			}
			if active == nil {
				return errors.New("authn: another replica created the snapshot signing key at the same moment, and that key is no longer active, " +
					"so this replica has no key to sign policy snapshots with. Start this replica again, and it loads the current key when it starts")
			}
		case err != nil:
			return err
		default:
			pubs[kid] = active.Public().(ed25519.PublicKey)
		}
	}
	s.mu.Lock()
	s.activeKID, s.active = kid, active
	s.pubs = pubs
	s.mu.Unlock()
	return nil
}

// list reads the snapshot keys from the store, seals every raw seed among
// them and opens each key. It answers the public key of each by kid, built
// from the opened seed, with the id and the private key of the first active
// one, or a nil key when none is active.
func (s *SnapshotKeys) list(ctx context.Context) (map[string]ed25519.PublicKey, string, ed25519.PrivateKey, error) {
	keys, err := s.repo.ListByPurpose(ctx, store.KeyPurposeSnapshot)
	if err != nil {
		return nil, "", nil, fmt.Errorf("authn: list snapshot keys: %w", err)
	}
	for i := range keys {
		if err := checkPurpose(keys[i], store.KeyPurposeSnapshot); err != nil {
			return nil, "", nil, err
		}
	}
	if err := sealStored(ctx, s.repo, s.sealer, keys); err != nil {
		return nil, "", nil, err
	}
	var (
		kid    string
		active ed25519.PrivateKey
	)
	pubs := map[string]ed25519.PublicKey{}
	for _, rec := range keys {
		priv, err := openSeed(s.sealer, rec, store.KeyPurposeSnapshot)
		if err != nil {
			return nil, "", nil, err
		}
		pubs[rec.KID] = priv.Public().(ed25519.PublicKey)
		if rec.Status == store.KeyActive && active == nil {
			kid, active = rec.KID, priv
		}
	}
	return pubs, kid, active, nil
}

// generate creates the active snapshot key and persists it with its seed
// sealed.
func (s *SnapshotKeys) generate(ctx context.Context) (string, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("authn: generate snapshot key: %w", err)
	}
	kid := uuid.NewString()
	sealed, err := sealSeed(s.sealer, store.KeyPurposeSnapshot, kid, priv.Seed())
	if err != nil {
		return "", nil, err
	}
	if _, err := s.repo.Create(ctx, store.SigningKey{
		KID: kid, Purpose: store.KeyPurposeSnapshot, Status: store.KeyActive,
		PrivateKey: sealed, PublicKey: pub,
	}); err != nil {
		return "", nil, err
	}
	return kid, priv, nil
}

// Active returns the current signing key id and private key
// (snapshot.Signer).
func (s *SnapshotKeys) Active(_ context.Context) (string, ed25519.PrivateKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return "", nil, fmt.Errorf("authn: no active snapshot key")
	}
	return s.activeKID, s.active, nil
}

// Public resolves a snapshot key id to its public key (snapshot.Signer).
func (s *SnapshotKeys) Public(_ context.Context, kid string) (ed25519.PublicKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pub, ok := s.pubs[kid]
	return pub, ok
}

// PublicKeys returns all known snapshot verification keys, for the
// distribution pubkey endpoint (straza/gateway verify against these).
func (s *SnapshotKeys) PublicKeys() map[string]ed25519.PublicKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]ed25519.PublicKey, len(s.pubs))
	for k, v := range s.pubs {
		out[k] = v
	}
	return out
}
