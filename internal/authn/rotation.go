package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/strazahq/straza/internal/store"
)

// KeyReloadInterval is how often every replica reloads the signing keys from
// the store, and the unit the rotation clock counts in. A staged key is
// promoted only after two intervals: one so that every replica has reloaded
// since the staging write and holds the key in its verify set, and one more
// so that a replica whose reload raced that write has reloaded again. Only
// then may any replica sign with it.
const KeyReloadInterval = 30 * time.Second

// Reload pulls the session keys from the store and rebuilds the signing key
// and the verify set: the newest active key signs, and every key that is not
// retired verifies. A store with no active key gets a fresh one, so a boot
// against an empty or staged-only store still signs. The database holds one
// active session key, so when a peer writes it first, Reload signs with the
// peer's key. Every key that is not retired is opened, so a key sealed under
// another KEK fails the load, and any failure leaves the previous state.
func (s *TokenService) Reload(ctx context.Context) error {
	keys, activeRec, err := s.sessionKeys(ctx)
	if err != nil {
		return err
	}
	if activeRec == nil {
		rec, err := s.generate(ctx, store.KeyActive)
		switch {
		case errors.Is(err, store.ErrConflict):
			if keys, activeRec, err = s.sessionKeys(ctx); err != nil {
				return err
			}
			if activeRec == nil {
				return errors.New("authn: another replica created the session signing key at the same moment, and that key is no longer active, " +
					"so this replica has no key to sign with. Start this replica again, and it loads the current key when it starts")
			}
		case err != nil:
			return err
		default:
			activeRec = &rec
			keys = append([]store.SigningKey{rec}, keys...)
		}
	}

	var active jwk.Key
	verify := jwk.NewSet()
	for _, rec := range keys {
		if rec.Status == store.KeyRetired {
			continue
		}
		priv, err := openSeed(s.sealer, rec, store.KeyPurposeSession)
		if err != nil {
			return err
		}
		pub, err := importPublic(rec.KID, priv.Public().(ed25519.PublicKey))
		if err != nil {
			return err
		}
		if err := verify.AddKey(pub); err != nil {
			return fmt.Errorf("authn: build key set: %w", err)
		}
		if rec.KID == activeRec.KID {
			if active, err = importPrivate(rec.KID, priv); err != nil {
				return err
			}
		}
	}

	s.mu.Lock()
	s.active = active
	s.verify = verify
	s.mu.Unlock()
	return nil
}

// sessionKeys reads the session keys from the store, newest first, seals
// every raw seed among them, and answers them with the first active one, the
// key that signs, or nil when none is active.
func (s *TokenService) sessionKeys(ctx context.Context) ([]store.SigningKey, *store.SigningKey, error) {
	keys, err := s.repo.ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil {
		return nil, nil, fmt.Errorf("authn: list signing keys: %w", err)
	}
	var active *store.SigningKey
	for i := range keys {
		if err := checkPurpose(keys[i], store.KeyPurposeSession); err != nil {
			return nil, nil, err
		}
	}
	if err := sealStored(ctx, s.repo, s.sealer, keys); err != nil {
		return nil, nil, err
	}
	for i := range keys {
		if keys[i].Status == store.KeyActive && active == nil {
			active = &keys[i]
		}
	}
	return keys, active, nil
}

// generate creates a fresh ed25519 session key with the given status and
// persists it with its seed sealed.
func (s *TokenService) generate(ctx context.Context, status string) (store.SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return store.SigningKey{}, fmt.Errorf("authn: generate key: %w", err)
	}
	kid := uuid.NewString()
	sealed, err := sealSeed(s.sealer, store.KeyPurposeSession, kid, priv.Seed())
	if err != nil {
		return store.SigningKey{}, err
	}
	rec := store.SigningKey{
		KID:        kid,
		Purpose:    store.KeyPurposeSession,
		Status:     status,
		PrivateKey: sealed,
		PublicKey:  pub,
	}
	created, err := s.repo.Create(ctx, rec)
	if err != nil {
		return store.SigningKey{}, fmt.Errorf("authn: persist key: %w", err)
	}
	return created, nil
}

// Stage begins a rotation: it creates a session key with status staged and
// reloads, so this replica verifies tokens signed with it at once and every
// other replica does at its next Reload. The staged key signs nothing until
// Advance promotes it. A second call while a staged key exists returns that
// key instead of creating another. The returned record carries no private
// key bytes; callers only need the kid.
func (s *TokenService) Stage(ctx context.Context) (store.SigningKey, error) {
	keys, err := s.repo.ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil {
		return store.SigningKey{}, fmt.Errorf("authn: list signing keys: %w", err)
	}
	var rec store.SigningKey
	for _, k := range keys {
		if k.Status == store.KeyStaged {
			rec = k
			break
		}
	}
	if rec.KID == "" {
		if rec, err = s.generate(ctx, store.KeyStaged); err != nil {
			return store.SigningKey{}, err
		}
		if err := s.Reload(ctx); err != nil {
			return store.SigningKey{}, err
		}
	}
	rec.PrivateKey = nil
	return rec, nil
}

// Advance reports what one Advance pass changed: the kid promoted from staged
// to active, empty when none, the kid that signed until that promotion, and
// the kids retired. A pass that fails after a change still reports it.
type Advance struct {
	Promoted string
	Previous string
	Retired  []string
}

// Advance is the rotation step every replica runs once per KeyReloadInterval.
// A staged key created two intervals or more before now is promoted: it
// becomes active and every other active key becomes retiring, which stamps
// the moment it stopped signing. A retiring key that stopped signing
// retireAfter or more before now is retired and leaves every verify set at
// the next Reload. retireAfter is the caller's contract: at least the longest
// lifetime of any credential signed with these keys, plus one
// KeyReloadInterval for the replica that signed with the old key until its
// own reload after the promotion. The promotion is one compare-and-set in the
// store that demotes the active key before it promotes the staged one,
// because the database holds one active session key, and the retirement is
// one too, so of two replicas running the pass in the same tick exactly one
// reports each change. Retirement is by age, so an already retiring key
// stays retiring. The service reloads after a change or a lost retirement.
func (s *TokenService) Advance(ctx context.Context, now time.Time, retireAfter time.Duration) (Advance, error) {
	var out Advance
	keys, err := s.repo.ListByPurpose(ctx, store.KeyPurposeSession)
	if err != nil {
		return out, fmt.Errorf("authn: list signing keys: %w", err)
	}
	// ListByPurpose is newest first. The oldest eligible staged key is the
	// one promoted, so replicas that staged concurrently converge on one key.
	var promote *store.SigningKey
	for i := len(keys) - 1; i >= 0; i-- {
		if keys[i].Status == store.KeyStaged && !keys[i].CreatedAt.After(now.Add(-2*KeyReloadInterval)) {
			promote = &keys[i]
			break
		}
	}
	if promote != nil {
		changed, err := s.repo.Promote(ctx, promote.KID)
		if err != nil {
			return out, fmt.Errorf("authn: promote key %s: %w", promote.KID, err)
		}
		if changed {
			out.Promoted = promote.KID
			for _, k := range keys {
				if k.Status == store.KeyActive {
					out.Previous = k.KID
				}
			}
		}
	}
	reload := out.Promoted != ""
	for _, k := range keys {
		if k.Status == store.KeyRetiring && k.RotatedAt != nil && !k.RotatedAt.After(now.Add(-retireAfter)) {
			changed, err := s.repo.Retire(ctx, k.KID, store.KeyRetiring)
			if err != nil {
				return out, fmt.Errorf("authn: retire key %s: %w", k.KID, err)
			}
			// A peer that won this write retired the key, so this replica
			// drops it from its verify set in the same pass.
			reload = true
			if changed {
				out.Retired = append(out.Retired, k.KID)
			}
		}
	}
	if !reload {
		return out, nil
	}
	return out, s.Reload(ctx)
}
