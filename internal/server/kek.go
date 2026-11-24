package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// loadKEK loads the key-encryption key before any signing key is loaded. It
// creates the key only for a store that holds no signing key yet, and it
// refuses a key that does not open a signing key the store already holds
// sealed. It logs how many raw seeds this start is about to seal. Both
// refusals come before the first write, because the first start over raw
// seeds seals them under the key it was given, and a key that is not the
// store's own would lock the server out once the right file comes back.
func loadKEK(ctx context.Context, cfg config.Config, log *slog.Logger, st store.Store) (*secrets.Builtin, error) {
	path := cfg.KEKFile()
	raws := 0
	var keys, sealed []store.SigningKey
	for _, purpose := range []string{store.KeyPurposeSession, store.KeyPurposeSnapshot, store.KeyPurposeClientAssertion} {
		recs, err := st.SigningKeys().ListByPurpose(ctx, purpose)
		if err != nil {
			return nil, fmt.Errorf("list the %s signing keys: %w", purpose, err)
		}
		keys = append(keys, recs...)
		for _, rec := range recs {
			// A raw seed is exactly ed25519.SeedSize bytes, and a start opens
			// every snapshot key and every other key that is not retired.
			raw := purpose != store.KeyPurposeClientAssertion && len(rec.PrivateKey) == ed25519.SeedSize
			if raw {
				raws++
			} else if purpose == store.KeyPurposeSnapshot || rec.Status != store.KeyRetired {
				sealed = append(sealed, rec)
			}
		}
	}
	const putBack = "Put back the secret.key that belongs to this database"
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) && len(keys) > 0 {
		advice := putBack
		if len(sealed) == 0 {
			advice += fmt.Sprintf(". If this deployment never kept that file, write 32 random bytes to it, for example with head -c 32 /dev/urandom > %s, "+
				"and keep a copy apart from the database backups", path)
		}
		return nil, fmt.Errorf("the key-encryption key file %s is missing, and this database already holds signing keys, "+
			"so strazad makes no new key and does not start. %s. Then start strazad again", path, advice)
	}
	kek, err := secrets.LoadOrCreateKEK(path)
	if err != nil {
		return nil, fmt.Errorf("%w. Put a readable file of exactly 32 raw bytes there, not text such as base64, the secret.key that belongs to this database, then start strazad again", err)
	}
	for _, rec := range sealed {
		if _, err := kek.Open(rec.PrivateKey); err != nil {
			return nil, fmt.Errorf("the %s signing key %s does not open: it was sealed under another secret.key or changed in the database. "+
				"strazad changed nothing and does not start. %s at %s, and mount the same secrets.kekFile on every replica", rec.Purpose, rec.KID, putBack, path)
		}
	}
	if raws > 0 {
		log.Info("this start seals the signing keys that the database holds unsealed", "keys", raws, "kekFile", path)
	}
	return kek, nil
}
