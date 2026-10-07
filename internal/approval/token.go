package approval

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// tokenKeySetting is the settings-table key holding the shared decision-token
// HMAC secret (random 32 bytes, at rest like the scim/api token keys). Sharing
// it across pods/restarts means a token minted anywhere verifies everywhere.
const tokenKeySetting = "approval.tokenKey"

// loadOrCreateTokenKey reads the shared HMAC key, creating it on first boot.
// Set is a last-writer-wins upsert, so on a concurrent first-boot race two pods
// briefly generate different keys; the re-read after Set converges both onto
// whichever write landed last.
func loadOrCreateTokenKey(st store.Store) ([]byte, error) {
	ctx := context.Background()
	if enc, err := st.Settings().Get(ctx, tokenKeySetting); err == nil {
		if key, derr := base64.StdEncoding.DecodeString(enc); derr == nil && len(key) == 32 {
			return key, nil
		}
		// A corrupt value is overwritten below.
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(key)
	if err := st.Settings().Set(ctx, tokenKeySetting, enc); err != nil {
		return nil, err
	}
	if stored, err := st.Settings().Get(ctx, tokenKeySetting); err == nil {
		if k, derr := base64.StdEncoding.DecodeString(stored); derr == nil && len(k) == 32 {
			return k, nil
		}
	}
	return key, nil
}

// sign is the HMAC-SHA256 over the id|verdict|expUnix triple.
func (s *Service) sign(id, verdict string, expUnix int64) []byte {
	mac := hmac.New(sha256.New, s.tokenKey)
	mac.Write([]byte(id + "|" + verdict + "|" + strconv.FormatInt(expUnix, 10)))
	return mac.Sum(nil)
}

// MintDecisionToken returns an opaque, self-describing token binding an
// approval id + verdict, valid until exp. Form: "<expUnix>.<base64url(mac)>".
// The token carries NO one-time marker: single-use is enforced by the atomic
// DB state transition in Decide (the record can leave `pending` exactly once),
// so a replayed token that verifies still resolves to a no-op or conflict. No
// separate jti cache is needed.
func (s *Service) MintDecisionToken(id, verdict string, exp time.Time) (string, error) {
	if len(s.tokenKey) == 0 {
		return "", errors.New("approval: decision-token key not initialized")
	}
	expUnix := exp.Unix()
	mac := s.sign(id, verdict, expUnix)
	return strconv.FormatInt(expUnix, 10) + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// VerifyDecisionToken checks tok binds exactly (id, verdict) and is unexpired.
func (s *Service) VerifyDecisionToken(tok, id, verdict string) error {
	dot := strings.IndexByte(tok, '.')
	if dot <= 0 {
		return ErrBadToken
	}
	expUnix, err := strconv.ParseInt(tok[:dot], 10, 64)
	if err != nil {
		return ErrBadToken
	}
	if s.now().Unix() > expUnix {
		return ErrTokenExpired
	}
	sig, err := base64.RawURLEncoding.DecodeString(tok[dot+1:])
	if err != nil {
		return ErrBadToken
	}
	if !hmac.Equal(sig, s.sign(id, verdict, expUnix)) {
		return ErrBadToken
	}
	return nil
}
