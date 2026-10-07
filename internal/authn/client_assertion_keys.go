package authn

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/store"
)

// ClientAssertionTTL is the lifetime of one client assertion (RFC 7523
// section 2.2). The assertion is presented once, within moments of signing,
// so the lifetime only has to cover the request and the two clocks.
const ClientAssertionTTL = 30 * time.Second

const (
	// clientAssertionKeyBits is the size of a new key. 3072 bits is the RSA
	// size NIST SP 800-57 accepts beyond 2030, and every large identity
	// provider takes it.
	clientAssertionKeyBits = 3072
	// clientAssertionMinBits is the smallest stored key a load accepts.
	clientAssertionMinBits = 2048
	// assertionKeysMaxStale is how long a replica serves and signs from its
	// last good load. A replica that lost the store would otherwise keep a
	// key that an administrator retired, so after two missed reloads and the
	// third it refuses both.
	assertionKeysMaxStale = 3 * KeyReloadInterval
)

var (
	// ErrNoAssertionKey says no client assertion key is active: none was
	// created yet, the staged key has not been promoted, or the signing key
	// was retired by hand.
	ErrNoAssertionKey = errors.New("authn: no client assertion key is active")
	// ErrAssertionKeyStaged is the ErrNoAssertionKey of a deployment whose
	// staged key waits for its promotion, so signing starts by itself.
	ErrAssertionKeyStaged = fmt.Errorf("%w: a staged key waits for its promotion", ErrNoAssertionKey)
	// ErrAssertionKeysStale says this replica has not read the key store for
	// longer than assertionKeysMaxStale and therefore publishes and signs
	// nothing.
	ErrAssertionKeysStale = errors.New("authn: the client assertion keys were not reloaded in time")
)

// WrongPurposeError says a key id names a signing key of another purpose.
type WrongPurposeError struct {
	KID     string
	Purpose string
}

func (e *WrongPurposeError) Error() string {
	return fmt.Sprintf("authn: signing key %s has the purpose %s", e.KID, e.Purpose)
}

// checkPurpose refuses a record that is not of the purpose its loader serves.
// The query and the CHECK constraint say the same; this is the code's own
// word, given before any key byte is read.
func checkPurpose(rec store.SigningKey, want string) error {
	if rec.Purpose != want {
		return fmt.Errorf("authn: signing key %s has the purpose %q and was handed to the %s loader, refused", rec.KID, rec.Purpose, want)
	}
	return nil
}

// KeySealer seals the private half of a client assertion key for the store
// and opens it again. The server hands in the secrets KEK provider, so every
// replica must hold the same KEK.
type KeySealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(ciphertext []byte) ([]byte, error)
}

// ClientAssertionKeys manages the RSA keys that sign the client assertion an
// agent's client presents at the customer's identity provider. Whoever holds
// a private key can get a token as any agent, so no method returns one: the
// type signs and it publishes public keys. Keys are born staged, the store
// allows one staged and one active key, and the life cycle and its timing
// are those of the session keys (rotation.go). A load that meets a record it
// cannot trust fails as a whole and leaves the previous state in place.
type ClientAssertionKeys struct {
	repo   store.SigningKeyRepo
	sealer KeySealer

	mu        sync.RWMutex
	active    jwk.Key // nil while no key is active
	hasStaged bool    // a staged key was in the last good load
	doc       []byte  // the JWKS document of the last good load
	loadedAt  time.Time
}

// NewClientAssertionKeys loads the keys and returns a ready service. An
// empty store is a good load with an empty document. A stored key that does
// not open is an error, because a replica with the wrong KEK must not serve.
func NewClientAssertionKeys(ctx context.Context, repo store.SigningKeyRepo, sealer KeySealer, now time.Time) (*ClientAssertionKeys, error) {
	if sealer == nil {
		return nil, errors.New("authn: client assertion keys need the secrets KEK to seal the private key")
	}
	s := &ClientAssertionKeys{repo: repo, sealer: sealer}
	if err := s.Reload(ctx, now); err != nil {
		return nil, err
	}
	return s, nil
}

// sealHeader is the first line of the sealed plaintext. It binds the key to
// its row, so a sealed key copied into another row does not load.
func sealHeader(kid string) []byte {
	return []byte(store.KeyPurposeClientAssertion + " " + kid + "\n")
}

// open returns the private key of rec after every check a load makes: the
// purpose, the seal, the row binding, the key type and size, and that the
// public column is this key's own public half.
func (s *ClientAssertionKeys) open(rec store.SigningKey) (*rsa.PrivateKey, error) {
	if err := checkPurpose(rec, store.KeyPurposeClientAssertion); err != nil {
		return nil, err
	}
	plain, err := s.sealer.Open(rec.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("authn: client assertion key %s does not open: it was sealed under another KEK or changed in the database. "+
			"Every replica must mount the same secrets.kekFile: %w", rec.KID, err)
	}
	der, ok := bytes.CutPrefix(plain, sealHeader(rec.KID))
	if !ok {
		return nil, fmt.Errorf("authn: the sealed key in row %s belongs to another key, refused", rec.KID)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("authn: client assertion key %s does not parse: %w", rec.KID, err)
	}
	priv, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("authn: client assertion key %s is not an RSA key, refused", rec.KID)
	}
	if priv.N.BitLen() < clientAssertionMinBits {
		return nil, fmt.Errorf("authn: client assertion key %s has %d bits, below the %d this server accepts", rec.KID, priv.N.BitLen(), clientAssertionMinBits)
	}
	if err := priv.Validate(); err != nil {
		return nil, fmt.Errorf("authn: client assertion key %s is not a valid RSA key: %w", rec.KID, err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("authn: encode the public half of %s: %w", rec.KID, err)
	}
	if !bytes.Equal(pub, rec.PublicKey) {
		return nil, fmt.Errorf("authn: the public column of client assertion key %s is not the key's own public half, refused", rec.KID)
	}
	return priv, nil
}

// Reload pulls the keys from the store and rebuilds the signing key and the
// key document: the active key signs, and the staged, active and retiring
// keys are published. The document is built from the opened private keys,
// which the KEK authenticates, never from the public column, so a database
// write alone cannot publish a key. Any failure leaves the previous state.
func (s *ClientAssertionKeys) Reload(ctx context.Context, now time.Time) error {
	recs, err := s.repo.ListByPurpose(ctx, store.KeyPurposeClientAssertion)
	if err != nil {
		return fmt.Errorf("authn: list client assertion keys: %w", err)
	}
	var active jwk.Key
	staged := false
	set := jwk.NewSet()
	for _, rec := range recs {
		if rec.Status == store.KeyRetired {
			continue
		}
		staged = staged || rec.Status == store.KeyStaged
		priv, err := s.open(rec)
		if err != nil {
			return err
		}
		pub, err := jwk.Import(&priv.PublicKey)
		if err != nil {
			return fmt.Errorf("authn: import public key %s: %w", rec.KID, err)
		}
		if err := annotateRS256(pub, rec.KID); err != nil {
			return err
		}
		if err := set.AddKey(pub); err != nil {
			return fmt.Errorf("authn: build the client assertion key set: %w", err)
		}
		if rec.Status != store.KeyActive {
			continue
		}
		if active != nil {
			return fmt.Errorf("authn: two active client assertion keys, %s and another, refused", rec.KID)
		}
		if active, err = jwk.Import(priv); err != nil {
			return fmt.Errorf("authn: import private key %s: %w", rec.KID, err)
		}
		if err := annotateRS256(active, rec.KID); err != nil {
			return err
		}
	}
	doc, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("authn: encode the client assertion key set: %w", err)
	}
	s.mu.Lock()
	s.active, s.hasStaged, s.doc, s.loadedAt = active, staged, doc, now
	s.mu.Unlock()
	return nil
}

func annotateRS256(key jwk.Key, kid string) error {
	for name, value := range map[string]any{jwk.KeyIDKey: kid, jwk.AlgorithmKey: jwa.RS256(), jwk.KeyUsageKey: "sig"} {
		if err := key.Set(name, value); err != nil {
			return fmt.Errorf("authn: set %s on key %s: %w", name, kid, err)
		}
	}
	return nil
}

// fresh reports the state of the last good load, or ErrAssertionKeysStale.
func (s *ClientAssertionKeys) fresh(now time.Time) (active jwk.Key, staged bool, doc []byte, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if now.Sub(s.loadedAt) > assertionKeysMaxStale {
		return nil, false, nil, ErrAssertionKeysStale
	}
	return s.active, s.hasStaged, s.doc, nil
}

// JWKS returns the public keys as an RFC 7517 document, from memory. It
// answers ErrAssertionKeysStale instead of a document this replica can no
// longer vouch for.
func (s *ClientAssertionKeys) JWKS(now time.Time) ([]byte, error) {
	_, _, doc, err := s.fresh(now)
	return doc, err
}

// SignAssertion signs one client assertion for clientID (RFC 7523 section
// 2.2, self-issued: iss and sub are the client id). The caller vouches that
// clientID is the verified subject of the calling session. It answers
// ErrNoAssertionKey while no key is active, as ErrAssertionKeyStaged when a
// staged key waits for its promotion, and ErrAssertionKeysStale on a replica
// that lost the store.
func (s *ClientAssertionKeys) SignAssertion(now time.Time, clientID, audience string) (string, error) {
	if clientID == "" || audience == "" {
		return "", errors.New("authn: a client assertion needs a client id and an audience")
	}
	key, staged, _, err := s.fresh(now)
	if err != nil {
		return "", err
	}
	if key == nil && staged {
		return "", ErrAssertionKeyStaged
	}
	if key == nil {
		return "", ErrNoAssertionKey
	}
	iat := now.UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(clientID).
		Subject(clientID).
		Audience([]string{audience}).
		JwtID(uuid.NewString()).
		IssuedAt(iat).
		Expiration(iat.Add(ClientAssertionTTL)).
		Build()
	if err != nil {
		return "", fmt.Errorf("authn: build client assertion: %w", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256(), key))
	if err != nil {
		return "", fmt.Errorf("authn: sign client assertion: %w", err)
	}
	return string(signed), nil
}

// StagedKey is what Stage reports: the staged key, whether this call created
// it, and the key that signs today, empty when none does.
type StagedKey struct {
	KID       string
	Created   bool
	ActiveKID string
}

// Stage begins a rotation, or creates the first key: it stores a new key
// with status staged and reloads, so this replica publishes it at once and
// every other replica does at its next Reload. The key signs nothing until
// Advance promotes it. While a staged key exists Stage answers with that
// key, also when a peer staged it a moment earlier, because the store holds
// one staged key of this purpose.
func (s *ClientAssertionKeys) Stage(ctx context.Context, now time.Time) (StagedKey, error) {
	out, err := s.staged(ctx)
	if err != nil || out.KID != "" {
		return out, err
	}
	priv, err := rsa.GenerateKey(rand.Reader, clientAssertionKeyBits)
	if err != nil {
		return StagedKey{}, fmt.Errorf("authn: generate client assertion key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return StagedKey{}, fmt.Errorf("authn: encode client assertion key: %w", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return StagedKey{}, fmt.Errorf("authn: encode client assertion public key: %w", err)
	}
	kid := uuid.NewString()
	sealed, err := s.sealer.Seal(append(sealHeader(kid), der...))
	if err != nil {
		return StagedKey{}, fmt.Errorf("authn: seal client assertion key: %w", err)
	}
	_, err = s.repo.Create(ctx, store.SigningKey{
		KID: kid, Purpose: store.KeyPurposeClientAssertion, Status: store.KeyStaged,
		PrivateKey: sealed, PublicKey: pub,
	})
	switch {
	case errors.Is(err, store.ErrConflict):
		// A peer staged its key first. Its key is the staged key.
		if out, err = s.staged(ctx); err != nil {
			return StagedKey{}, err
		}
		if out.KID == "" {
			return StagedKey{}, errors.New("authn: a peer staged a client assertion key and it is gone again, run the rotation once more")
		}
	case err != nil:
		return StagedKey{}, fmt.Errorf("authn: persist client assertion key: %w", err)
	default:
		out.KID, out.Created = kid, true
	}
	return out, s.Reload(ctx, now)
}

// staged reads the staged and the active key ids from the store.
func (s *ClientAssertionKeys) staged(ctx context.Context) (StagedKey, error) {
	recs, err := s.repo.ListByPurpose(ctx, store.KeyPurposeClientAssertion)
	if err != nil {
		return StagedKey{}, fmt.Errorf("authn: list client assertion keys: %w", err)
	}
	var out StagedKey
	for _, rec := range recs {
		switch rec.Status {
		case store.KeyStaged:
			out.KID = rec.KID
		case store.KeyActive:
			out.ActiveKID = rec.KID
		}
	}
	return out, nil
}

// Advance is the rotation step every replica runs once per
// KeyReloadInterval, with the timing contract of TokenService.Advance. Both
// writes are compare-and-set in the store, so of several replicas running
// the same step exactly one reports the promotion or the retirement, and
// that replica records it.
func (s *ClientAssertionKeys) Advance(ctx context.Context, now time.Time, retireAfter time.Duration) (Advance, error) {
	var out Advance
	recs, err := s.repo.ListByPurpose(ctx, store.KeyPurposeClientAssertion)
	if err != nil {
		return out, fmt.Errorf("authn: list client assertion keys: %w", err)
	}
	for _, rec := range recs {
		switch {
		case rec.Status == store.KeyStaged && !rec.CreatedAt.After(now.Add(-2*KeyReloadInterval)):
			changed, err := s.repo.Promote(ctx, rec.KID)
			if err != nil {
				return out, fmt.Errorf("authn: promote client assertion key %s: %w", rec.KID, err)
			}
			if changed {
				out.Promoted = rec.KID
			}
		case rec.Status == store.KeyRetiring && rec.RotatedAt != nil && !rec.RotatedAt.After(now.Add(-retireAfter)):
			changed, err := s.repo.Retire(ctx, rec.KID, store.KeyRetiring)
			if err != nil {
				return out, fmt.Errorf("authn: retire client assertion key %s: %w", rec.KID, err)
			}
			if changed {
				out.Retired = append(out.Retired, rec.KID)
			}
		}
	}
	if out.Promoted == "" && len(out.Retired) == 0 {
		return out, nil
	}
	return out, s.Reload(ctx, now)
}

// Retire takes one client assertion key out of service at once, whatever
// state it is in, for a key that may have been copied. It reports the status
// the key held and whether this call retired it. A retired signing key leaves
// nothing to sign with until a new key is staged and promoted. An unknown kid
// answers store.ErrNotFound and a key of another purpose a WrongPurposeError.
func (s *ClientAssertionKeys) Retire(ctx context.Context, now time.Time, kid string) (was string, changed bool, err error) {
	// The status can move under this call once, when the janitor promotes or
	// demotes the key between the read and the write.
	for range 2 {
		rec, err := s.repo.Get(ctx, kid)
		if err != nil {
			return "", false, fmt.Errorf("authn: read signing key %s: %w", kid, err)
		}
		if rec.Purpose != store.KeyPurposeClientAssertion {
			return "", false, &WrongPurposeError{KID: kid, Purpose: rec.Purpose}
		}
		if rec.Status == store.KeyRetired {
			return rec.Status, false, nil
		}
		if changed, err = s.repo.Retire(ctx, kid, rec.Status); err != nil {
			return "", false, fmt.Errorf("authn: retire client assertion key %s: %w", kid, err)
		}
		if changed {
			return rec.Status, true, s.Reload(ctx, now)
		}
	}
	return "", false, fmt.Errorf("authn: client assertion key %s changed state twice while it was retired, run the command again", kid)
}
