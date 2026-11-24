package authn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/strazahq/straza/internal/store"
)

// errNoSeedSealer says a loader of the session or the snapshot keys was built
// without the secrets KEK.
var errNoSeedSealer = errors.New("authn: the session and snapshot signing keys need the secrets KEK, which seals their seeds")

// sealSeed seals the seed of the ed25519 key kid of purpose for the store.
// The sealed plaintext starts with sealHeader, so the value opens only in its
// own row.
func sealSeed(sealer KeySealer, purpose, kid string, seed []byte) ([]byte, error) {
	sealed, err := sealer.Seal(append(sealHeader(purpose, kid), seed...))
	if err != nil {
		return nil, fmt.Errorf("authn: seal the %s signing key %s: %w", purpose, kid, err)
	}
	return sealed, nil
}

// openSeed returns the private key of rec after every check a load makes: the
// purpose, the seal, the row binding, the seed size, and that the public
// column is this key's own public half.
func openSeed(sealer KeySealer, rec store.SigningKey, purpose string) (ed25519.PrivateKey, error) {
	if err := checkPurpose(rec, purpose); err != nil {
		return nil, err
	}
	plain, err := sealer.Open(rec.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("authn: the %s signing key %s does not open: it was sealed under another secret.key or changed in the database. "+
			"Put back the secret.key that belongs to this database, and mount the same secrets.kekFile on every replica: %w",
			purpose, rec.KID, err)
	}
	seed, ok := bytes.CutPrefix(plain, sealHeader(purpose, rec.KID))
	if !ok {
		return nil, fmt.Errorf("authn: the sealed key in row %s belongs to another key, refused", rec.KID)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("authn: the %s signing key %s is not an ed25519 seed, refused", purpose, rec.KID)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), rec.PublicKey) {
		return nil, fmt.Errorf("authn: the public column of the %s signing key %s is not the key's own public half, refused", purpose, rec.KID)
	}
	return priv, nil
}

// sealStored seals, in the store and in recs, every key that still holds a
// raw seed. A raw seed is exactly ed25519.SeedSize bytes, shorter than any
// sealed value, and is what a strazad without sealing stored. The write is
// compare-and-set, so when a peer sealed the row first, recs takes the value
// the peer stored.
func sealStored(ctx context.Context, repo store.SigningKeyRepo, sealer KeySealer, recs []store.SigningKey) error {
	for i := range recs {
		rec := &recs[i]
		if len(rec.PrivateKey) != ed25519.SeedSize {
			continue
		}
		sealed, err := sealSeed(sealer, rec.Purpose, rec.KID, rec.PrivateKey)
		if err != nil {
			return err
		}
		changed, err := repo.ReplacePrivateKey(ctx, rec.KID, rec.PrivateKey, sealed)
		if err != nil {
			return fmt.Errorf("authn: store the sealed %s signing key %s: %w", rec.Purpose, rec.KID, err)
		}
		if changed {
			rec.PrivateKey = sealed
			continue
		}
		cur, err := repo.Get(ctx, rec.KID)
		if err != nil {
			return fmt.Errorf("authn: read the %s signing key %s again: %w", rec.Purpose, rec.KID, err)
		}
		rec.PrivateKey = cur.PrivateKey
	}
	return nil
}

// opensAtStart reports whether a strazad start opens the private half of rec:
// every snapshot key, and every key of another purpose that is not retired.
func opensAtStart(rec store.SigningKey) bool {
	return rec.Purpose == store.KeyPurposeSnapshot || rec.Status != store.KeyRetired
}

// UnsealSeeds stores the session and snapshot seeds a start opens raw again,
// the form a strazad without sealing reads, and reports how many rows it
// changed. It is the step a rollback to such a release runs with every
// replica stopped. It opens every key before it writes one, so a key that
// does not open leaves every row sealed. A nil sealer is an error only when a
// sealed key exists.
func UnsealSeeds(ctx context.Context, repo store.SigningKeyRepo, sealer KeySealer) (int, error) {
	type opened struct {
		rec  store.SigningKey
		seed []byte
	}
	var keys []opened
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot} {
		recs, err := repo.ListByPurpose(ctx, purpose)
		if err != nil {
			return 0, fmt.Errorf("authn: list the %s signing keys: %w", purpose, err)
		}
		for _, rec := range recs {
			if len(rec.PrivateKey) == ed25519.SeedSize || !opensAtStart(rec) {
				continue
			}
			if sealer == nil {
				return 0, errNoSeedSealer
			}
			priv, err := openSeed(sealer, rec, purpose)
			if err != nil {
				return 0, err
			}
			keys = append(keys, opened{rec: rec, seed: priv.Seed()})
		}
	}
	n := 0
	for _, k := range keys {
		changed, err := repo.ReplacePrivateKey(ctx, k.rec.KID, k.rec.PrivateKey, k.seed)
		if err != nil {
			return n, fmt.Errorf("authn: store the %s signing key %s: %w", k.rec.Purpose, k.rec.KID, err)
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// ResealKeys moves every signing key a start opens from the KEK from to the
// KEK to and reports how many rows it changed. It is the step that replaces
// the KEK, run with every replica stopped. Each key goes from one seal to the
// other in a single write, so no row holds it unsealed. A key that opens
// under to already is left alone, so a run that stopped part way finishes on
// the next one. It opens every key before it writes one, so a key that
// neither KEK opens leaves every row as it was. A raw seed is left for the
// next start, which seals it.
func ResealKeys(ctx context.Context, repo store.SigningKeyRepo, from, to KeySealer) (int, error) {
	type moved struct {
		rec    store.SigningKey
		sealed []byte
	}
	var keys []moved
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot, store.KeyPurposeClientAssertion} {
		recs, err := repo.ListByPurpose(ctx, purpose)
		if err != nil {
			return 0, fmt.Errorf("authn: list the %s signing keys: %w", purpose, err)
		}
		for _, rec := range recs {
			if err := checkPurpose(rec, purpose); err != nil {
				return 0, err
			}
			raw := purpose != store.KeyPurposeClientAssertion && len(rec.PrivateKey) == ed25519.SeedSize
			if raw || !opensAtStart(rec) {
				continue
			}
			if _, err := to.Open(rec.PrivateKey); err == nil {
				continue
			}
			plain, err := from.Open(rec.PrivateKey)
			if err != nil {
				return 0, fmt.Errorf("authn: the %s signing key %s opens under neither the key in use nor the new one, so no key was changed. "+
					"Run the command with the secret.key this database was sealed with in place: %w", purpose, rec.KID, err)
			}
			if !bytes.HasPrefix(plain, sealHeader(purpose, rec.KID)) {
				return 0, fmt.Errorf("authn: the sealed key in row %s belongs to another key, so no key was changed", rec.KID)
			}
			sealed, err := to.Seal(plain)
			if err != nil {
				return 0, fmt.Errorf("authn: seal the %s signing key %s: %w", purpose, rec.KID, err)
			}
			keys = append(keys, moved{rec: rec, sealed: sealed})
		}
	}
	n := 0
	for _, k := range keys {
		changed, err := repo.ReplacePrivateKey(ctx, k.rec.KID, k.rec.PrivateKey, k.sealed)
		if err != nil {
			return n, fmt.Errorf("authn: store the %s signing key %s: %w", k.rec.Purpose, k.rec.KID, err)
		}
		if changed {
			n++
		}
	}
	return n, nil
}
