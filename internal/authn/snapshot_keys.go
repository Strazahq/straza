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
// independently. Implements snapshot.Signer.
type SnapshotKeys struct {
	repo store.SigningKeyRepo

	mu      sync.RWMutex
	activeK store.SigningKey
	pubs    map[string]ed25519.PublicKey
}

// NewSnapshotKeys loads (or creates) the snapshot signing key.
func NewSnapshotKeys(ctx context.Context, repo store.SigningKeyRepo) (*SnapshotKeys, error) {
	s := &SnapshotKeys{repo: repo, pubs: map[string]ed25519.PublicKey{}}
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
	pubs, active, err := s.list(ctx)
	if err != nil {
		return err
	}
	if active == nil {
		rec, err := s.generate(ctx)
		switch {
		case errors.Is(err, store.ErrConflict):
			if pubs, active, err = s.list(ctx); err != nil {
				return err
			}
			if active == nil {
				return errors.New("authn: another replica created the snapshot signing key at the same moment, and that key is no longer active, " +
					"so this replica has no key to sign policy snapshots with. Start this replica again, and it loads the current key when it starts")
			}
		case err != nil:
			return err
		default:
			active = &rec
			pubs[rec.KID] = ed25519.PublicKey(rec.PublicKey)
		}
	}
	s.mu.Lock()
	s.activeK = *active
	s.pubs = pubs
	s.mu.Unlock()
	return nil
}

// list reads the snapshot keys from the store and answers the public key of
// each by kid with the first active key, or nil when none is active.
func (s *SnapshotKeys) list(ctx context.Context) (map[string]ed25519.PublicKey, *store.SigningKey, error) {
	keys, err := s.repo.ListByPurpose(ctx, store.KeyPurposeSnapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("authn: list snapshot keys: %w", err)
	}
	var active *store.SigningKey
	pubs := map[string]ed25519.PublicKey{}
	for i := range keys {
		if err := checkPurpose(keys[i], store.KeyPurposeSnapshot); err != nil {
			return nil, nil, err
		}
		pubs[keys[i].KID] = ed25519.PublicKey(keys[i].PublicKey)
		if keys[i].Status == store.KeyActive && active == nil {
			active = &keys[i]
		}
	}
	return pubs, active, nil
}

func (s *SnapshotKeys) generate(ctx context.Context) (store.SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return store.SigningKey{}, fmt.Errorf("authn: generate snapshot key: %w", err)
	}
	return s.repo.Create(ctx, store.SigningKey{
		KID: uuid.NewString(), Purpose: store.KeyPurposeSnapshot, Status: store.KeyActive,
		PrivateKey: priv.Seed(), PublicKey: pub,
	})
}

// Active returns the current signing key id and private key
// (snapshot.Signer).
func (s *SnapshotKeys) Active(_ context.Context) (string, ed25519.PrivateKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.activeK.PrivateKey) == 0 {
		return "", nil, fmt.Errorf("authn: no active snapshot key")
	}
	return s.activeK.KID, ed25519.NewKeyFromSeed(s.activeK.PrivateKey), nil
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
