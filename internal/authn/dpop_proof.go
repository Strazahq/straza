package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// NonceKeySetting is the settings row that holds the DPoP nonce key, shared by
// every replica.
const NonceKeySetting = "authn.dpopNonceKey"

// SpentProofLifetime is how long a spent proof id stays spent, in memory and
// in the store. It outlasts the longest life of a nonce, so a proof cannot
// be replayed while its nonce still passes.
const SpentProofLifetime = 3 * time.Minute

const (
	// nonceWindowSeconds is how long one nonce is current. The previous
	// window's nonce is accepted too, so a nonce lives 60 to 120 seconds.
	nonceWindowSeconds = 60
	// maxJTILength bounds what one spent proof id costs in memory.
	maxJTILength = 128
)

// ErrStaleNonce reports a proof whose nonce is missing, malformed or outside
// the current and previous window. The caller answers 401 with
// error="use_dpop_nonce" and a fresh nonce, writes no audit record and moves
// no counter.
var ErrStaleNonce = errors.New("authn: dpop nonce missing or stale")

// ProofError is a proof Verify refused for a reason other than its nonce.
// Reason is a short sentence fragment a person can read, such as "htu path
// does not match the request". It names no key, token or secret.
type ProofError struct{ Reason string }

func (e *ProofError) Error() string { return "authn: dpop proof rejected: " + e.Reason }

func refuse(reason string) error { return &ProofError{Reason: reason} }

// ProofCheck is what one request expects of its DPoP proof.
type ProofCheck struct {
	Method      string   // the request method, compared to htm
	Path        string   // the request path, compared exactly to htu's path
	Origins     []string // allowed scheme://host[:port] origins for htu
	AccessToken string   // when set, ath must hash it
	Thumbprint  string   // when set, the proof's key must have this thumbprint
	// Counter, when above 0, is the body counter of a key check-in, which
	// the proof must sign as ctr. Otherwise a proof that carries ctr is
	// refused, so ctr is never accepted where it means nothing.
	Counter int64
	// CallerSpends leaves a passing proof's jti unspent, for the key lanes
	// that spend it in the store once they know who is calling. The caller
	// MUST spend Proof.JTI until now plus SpentProofLifetime before it acts
	// on the request.
	CallerSpends bool
}

// Proof is what a verified proof carries.
type Proof struct {
	Thumbprint string            // RFC 7638 thumbprint of the proof key
	PublicKey  ed25519.PublicKey // the proof key
	JWK        string            // the proof key as canonical JWK JSON, for storage
	JTI        string
}

// ProofVerifier makes DPoP nonces and verifies proofs. Nonces are a 60 s
// window plus an HMAC under the nonce key, so every replica that loads the
// same key accepts them, and spent proof ids are refused per replica. It is
// safe for concurrent use.
type ProofVerifier struct {
	key []byte

	mu        sync.Mutex
	spent     map[string]time.Time // jti to the time it may be used again
	nextSweep time.Time
}

// NewProofVerifier returns a verifier for nonceKey. It panics when nonceKey
// is shorter than 32 bytes, because a short key makes nonces guessable.
func NewProofVerifier(nonceKey []byte) *ProofVerifier {
	if len(nonceKey) < 32 {
		panic("authn: the dpop nonce key is shorter than 32 bytes")
	}
	return &ProofVerifier{key: append([]byte(nil), nonceKey...), spent: map[string]time.Time{}}
}

// Nonce returns the nonce of the window now falls in.
func (v *ProofVerifier) Nonce(now time.Time) string {
	return v.nonceFor(now.Unix() / nonceWindowSeconds)
}

// nonceFor is the unpadded base64url of the window number as 8 big-endian
// bytes followed by HMAC-SHA256 of those 8 bytes under the nonce key.
func (v *ProofVerifier) nonceFor(window int64) string {
	w := binary.BigEndian.AppendUint64(nil, uint64(window)) // #nosec G115 -- minutes since 1970, never negative
	mac := hmac.New(sha256.New, v.key)
	mac.Write(w)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(w))
}

// nonceFresh reports whether nonce is this key's nonce for the window of
// now or the one before it.
func (v *ProofVerifier) nonceFresh(nonce string, now time.Time) bool {
	current := now.Unix() / nonceWindowSeconds
	return hmac.Equal([]byte(nonce), []byte(v.nonceFor(current))) ||
		hmac.Equal([]byte(nonce), []byte(v.nonceFor(current-1)))
}

// Verify checks raw against c at now. Once raw parses as a compact JWS whose
// header and claims are JSON objects, a missing or stale nonce returns
// ErrStaleNonce before any other check. Any other failure returns a
// *ProofError. Only a proof that passes every check spends its jti, and
// only when c.CallerSpends is false.
func (v *ProofVerifier) Verify(raw string, c ProofCheck, now time.Time) (Proof, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Proof{}, refuse("the proof is not a compact JWS")
	}
	header, ok := decodeMembers(parts[0])
	if !ok {
		return Proof{}, refuse("the proof header is malformed")
	}
	claims, ok := decodeMembers(parts[1])
	if !ok {
		return Proof{}, refuse("the proof claims are malformed")
	}
	if nonce, _ := claims.str("nonce"); !v.nonceFresh(nonce, now) {
		return Proof{}, ErrStaleNonce
	}

	if typ, _ := header.str("typ"); typ != dpop.ProofType {
		return Proof{}, refuse("typ is not dpop+jwt")
	}
	if alg, _ := header.str("alg"); alg != "EdDSA" {
		return Proof{}, refuse("alg is not EdDSA")
	}
	if _, ok := header["crit"]; ok {
		return Proof{}, refuse("the proof header lists critical extensions this server does not support")
	}
	pub, err := proofKey(header)
	if err != nil {
		return Proof{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return Proof{}, refuse("the signature does not verify under the proof's jwk")
	}

	if htm, _ := claims.str("htm"); htm != c.Method {
		return Proof{}, refuse("htm does not match the request method")
	}
	htu, _ := claims.str("htu")
	if err := checkHTU(htu, c); err != nil {
		return Proof{}, err
	}
	var iat float64
	if rawIAT, ok := claims["iat"]; !ok || json.Unmarshal(rawIAT, &iat) != nil || iat <= 0 {
		return Proof{}, refuse("iat is missing or not a number")
	}
	jti, _ := claims.str("jti")
	if jti == "" {
		return Proof{}, refuse("jti is missing")
	}
	if len(jti) > maxJTILength {
		return Proof{}, refuse("jti is longer than 128 bytes")
	}
	ath, hasATH := claims.str("ath")
	switch {
	case c.AccessToken == "" && hasATH:
		return Proof{}, refuse("ath is present but the request carries no access token")
	case c.AccessToken != "" && !hasATH:
		return Proof{}, refuse("ath is missing")
	case c.AccessToken != "" && ath != dpop.AccessTokenHash(c.AccessToken):
		return Proof{}, refuse("ath does not match the access token")
	}
	rawCTR, hasCTR := claims["ctr"]
	var ctr int64
	switch {
	case c.Counter <= 0 && hasCTR:
		return Proof{}, refuse("ctr is present but the request is not a key check-in")
	case c.Counter > 0 && !hasCTR:
		return Proof{}, refuse("ctr is missing")
	case c.Counter > 0 && (json.Unmarshal(rawCTR, &ctr) != nil || ctr != c.Counter):
		return Proof{}, refuse("ctr does not match the check-in counter")
	}
	thumbprint := dpop.Thumbprint(pub)
	if c.Thumbprint != "" && thumbprint != c.Thumbprint {
		return Proof{}, refuse("the proof key is not the key this credential is bound to")
	}

	if !c.CallerSpends && !v.spend(jti, now) {
		return Proof{}, refuse("jti was already used")
	}
	return Proof{Thumbprint: thumbprint, PublicKey: pub, JWK: dpop.PublicJWK(pub), JTI: jti}, nil
}

// spend records jti as used until now plus SpentProofLifetime and reports
// false when it is already spent. Expired entries are swept at most once a
// minute, so a busy replica does not walk the whole map on every request.
func (v *ProofVerifier) spend(jti string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !now.Before(v.nextSweep) {
		for k, free := range v.spent {
			if !now.Before(free) {
				delete(v.spent, k)
			}
		}
		v.nextSweep = now.Add(time.Minute)
	}
	if free, ok := v.spent[jti]; ok && now.Before(free) {
		return false
	}
	v.spent[jti] = now.Add(SpentProofLifetime)
	return true
}

// members is one decoded JSON object of a proof, read by exact member name.
type members map[string]json.RawMessage

func decodeMembers(segment string) (members, bool) {
	b, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return nil, false
	}
	var m members
	if json.Unmarshal(b, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// str returns member name when it is a JSON string. The bool reports
// whether the member is present at all.
func (m members) str(name string) (string, bool) {
	raw, ok := m[name]
	if !ok {
		return "", false
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s, true
}

// proofKey reads the header's jwk as an Ed25519 public key and refuses a jwk
// that carries a private part.
func proofKey(header members) (ed25519.PublicKey, error) {
	raw, ok := header["jwk"]
	if !ok {
		return nil, refuse("the proof header carries no jwk")
	}
	var jwk members
	if json.Unmarshal(raw, &jwk) != nil || jwk == nil {
		return nil, refuse("jwk is not an Ed25519 public key")
	}
	if _, ok := jwk["d"]; ok {
		return nil, refuse("jwk carries a private key")
	}
	kty, _ := jwk.str("kty")
	crv, _ := jwk.str("crv")
	x, _ := jwk.str("x")
	pub, err := base64.RawURLEncoding.DecodeString(x)
	if kty != "OKP" || crv != "Ed25519" || err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, refuse("jwk is not an Ed25519 public key")
	}
	return ed25519.PublicKey(pub), nil
}

// checkHTU compares htu to the request: query and fragment are ignored, the
// path must equal c.Path exactly, and the origin must be one of c.Origins.
func checkHTU(htu string, c ProofCheck) error {
	u, err := url.Parse(htu)
	if err != nil || origin(u) == "" {
		return refuse("htu is not an absolute URL")
	}
	if u.Path != c.Path {
		return refuse("htu path does not match the request")
	}
	want := origin(u)
	for _, allowed := range c.Origins {
		if a, err := url.Parse(allowed); err == nil && origin(a) == want {
			return nil
		}
	}
	return refuse("htu origin is not this server")
}

// origin returns scheme://host[:port] of u with the scheme and host
// lower-cased and the scheme's default port dropped, or "" when u has no
// scheme or host.
func origin(u *url.URL) string {
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if scheme == "" || host == "" {
		return ""
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// LoadOrCreateNonceKey returns the nonce key from settings, creating it with
// SetIfAbsent on first boot and reading it back, so every replica holds the
// winner. A row that does not hold a base64 32-byte key is an error, and the
// row is left as it is.
func LoadOrCreateNonceKey(ctx context.Context, settings store.SettingsRepo) ([]byte, error) {
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("authn: make the dpop nonce key: %w", err)
	}
	if err := settings.SetIfAbsent(ctx, NonceKeySetting, base64.StdEncoding.EncodeToString(fresh)); err != nil {
		return nil, fmt.Errorf("authn: store the dpop nonce key in settings row %s: %w", NonceKeySetting, err)
	}
	stored, err := settings.Get(ctx, NonceKeySetting)
	if err != nil {
		return nil, fmt.Errorf("authn: read the dpop nonce key from settings row %s: %w", NonceKeySetting, err)
	}
	key, err := base64.StdEncoding.DecodeString(stored)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("authn: the settings row %s does not hold a base64 32-byte key, so strazad cannot check DPoP nonces. "+
			"Delete that row in the database and restart every strazad replica, so they make and share a fresh key", NonceKeySetting)
	}
	return key, nil
}
