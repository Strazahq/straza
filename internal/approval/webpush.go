package approval

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/redact"
)

// Web Push sender identity + subscription plumbing (RFC 8292 VAPID; the
// message encryption itself lives in webpush_encrypt.go, the HTTP delivery in
// push.go). One deployment owns ONE VAPID P-256 keypair, its identity to
// every push service. The key loads from approval.push.webpush.vapidKeyFile
// and, when the configured file is absent, is minted ONCE at boot and
// persisted (generate-once, the approverTLS auto-mint idiom). Deliberate
// design: the PATH is explicit operator configuration (enabling a new
// outbound push protocol is an explicit act, the FCM/Slack enable idiom, and
// the key's location must be a conscious backup decision), while the KEY
// inside it is minted automatically (there is nothing an operator could
// choose about it). Rotating or deleting the file invalidates EVERY keyed
// subscription (each device/browser must re-register), so a bad file fails
// boot with that consequence named instead of silently re-minting.

const (
	// vapidTokenTTL is the exp horizon of a minted VAPID JWT. RFC 8292 §2
	// caps it at 24 hours; 12 keeps a healthy margin against clock skew at
	// the push service.
	vapidTokenTTL = 12 * time.Hour
	// vapidRefreshMargin retires a cached JWT this long before exp so an
	// in-flight send never carries a token that expires mid-request.
	vapidRefreshMargin = time.Hour
)

// webPushSender holds the VAPID signing identity and the per-origin JWT
// cache. Safe for concurrent use.
type webPushSender struct {
	key     *ecdsa.PrivateKey
	pubB64  string // base64url uncompressed public point: the k= parameter and the enroll advertisement
	contact string // RFC 8292 §2.1 sub claim; empty = claim omitted
	now     func() time.Time

	mu     sync.Mutex
	tokens map[string]cachedVAPID // push-resource origin → cached Authorization value
}

type cachedVAPID struct {
	header    string
	refreshAt time.Time
}

// newWebPushSender loads (or first-boot-mints) the VAPID key named by the
// config and builds the sender. Any key problem fails boot (like a bad FCM
// service-account file): delivery is best-effort and only logs, so a key
// error surfacing there would be swallowed forever.
func newWebPushSender(cfg config.WebPush, log *slog.Logger) (*webPushSender, error) {
	key, minted, err := loadOrMintVAPIDKey(cfg.VAPIDKeyFile)
	if err != nil {
		return nil, err
	}
	w, err := newWebPushSenderFromKey(key, cfg.Contact)
	if err != nil {
		return nil, err
	}
	if minted {
		log.Info("webpush VAPID key minted. This file is the deployment's push identity; back it up (rotation strands every keyed subscription)",
			"file", cfg.VAPIDKeyFile)
	} else {
		log.Info("webpush VAPID key loaded", "file", cfg.VAPIDKeyFile)
	}
	return w, nil
}

// newWebPushSenderFromKey wraps an already-held P-256 key (the file-less seam
// tests use). ES256 is P-256 by definition, so any other curve is refused.
func newWebPushSenderFromKey(key *ecdsa.PrivateKey, contact string) (*webPushSender, error) {
	if key.Curve != elliptic.P256() {
		return nil, errors.New("webpush: the VAPID key is not a P-256 key (RFC 8292 mandates ES256/P-256)")
	}
	ecdhPub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID public key: %w", err)
	}
	return &webPushSender{
		key:     key,
		pubB64:  base64.RawURLEncoding.EncodeToString(ecdhPub.Bytes()),
		contact: contact,
		now:     time.Now,
		tokens:  map[string]cachedVAPID{},
	}, nil
}

// publicKeyB64 is the deployment's VAPID public key (base64url, 65-octet
// uncompressed point), what a subscribing client hands its push service as
// applicationServerKey, advertised to enrolling devices.
func (w *webPushSender) publicKeyB64() string { return w.pubB64 }

// vapidAuthorization returns the RFC 8292 §3 Authorization value
// (`vapid t=<jwt>, k=<pubkey>`) for one push resource, minting an ES256 JWT
// whose aud is the resource's ORIGIN, not the full URL (getting this wrong
// yields 401s that look like key problems), and caching it per origin until
// the refresh horizon.
func (w *webPushSender) vapidAuthorization(endpoint string) (string, error) {
	origin, err := pushResourceOrigin(endpoint)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.tokens[origin]; ok && w.now().Before(c.refreshAt) {
		return c.header, nil
	}
	now := w.now()
	jwt, err := w.signVAPIDJWT(origin, now)
	if err != nil {
		return "", err
	}
	header := "vapid t=" + jwt + ", k=" + w.pubB64
	w.tokens[origin] = cachedVAPID{header: header, refreshAt: now.Add(vapidTokenTTL - vapidRefreshMargin)}
	return header, nil
}

// signVAPIDJWT mints the ES256 JWT: header {alg,typ}, claims {aud, exp,
// sub?}, signature raw R‖S (64 octets, the JOSE form, NOT ASN.1 DER).
func (w *webPushSender) signVAPIDJWT(origin string, now time.Time) (string, error) {
	hb, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims := map[string]any{"aud": origin, "exp": now.Add(vapidTokenTTL).Unix()}
	if w.contact != "" {
		claims["sub"] = w.contact
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64url(hb) + "." + b64url(cb)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, w.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("webpush: sign VAPID JWT: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64url(sig), nil
}

// pushResourceOrigin extracts the https origin of a push resource URL,
// normalizing away an explicit default port (an origin never carries :443).
// Fail closed: non-https or hostless URLs never mint a token.
func pushResourceOrigin(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		// url.Parse's error echoes its full input, and a push resource path
		// is a per-subscription capability token; redact to scheme://host.
		return "", fmt.Errorf("webpush: push resource URL: %w", redact.SanitizeURLError(err, redact.Host))
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("webpush: push resource must be an https URL, got %q", redact.Host(endpoint))
	}
	host := u.Host
	if u.Port() == "443" {
		host = u.Hostname()
		if strings.Contains(host, ":") { // IPv6 literal keeps its brackets
			host = "[" + host + "]"
		}
	}
	return "https://" + host, nil
}

// --- VAPID key file (generate-once-persist) ---

// testHookVAPIDMintRace, when non-nil, fires in loadOrMintVAPIDKey's race
// window: after the absence check, before the persist. Tests use it
// to play the concurrent boot that wins the mint; always nil in production.
var testHookVAPIDMintRace func()

// loadOrMintVAPIDKey loads the PEM key at path, minting a fresh P-256 key
// there on first boot (0600, parent dir created 0700). It NEVER overwrites or
// silently replaces existing state: an unusable file is a boot error naming
// the consequence of re-minting, because the key IS the push identity every
// subscription is bound to. When a concurrent first boot wins the
// persist race (multi-pod, shared volume), the loser re-reads ONCE and boots
// on the winner's key; a re-read that also fails is the real boot error.
func loadOrMintVAPIDKey(path string) (*ecdsa.PrivateKey, bool, error) {
	key, err := readVAPIDKeyFile(path)
	switch {
	case err == nil:
		return key, false, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, false, err
	}

	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("webpush: mint VAPID key: %w", err)
	}
	if testHookVAPIDMintRace != nil {
		testHookVAPIDMintRace()
	}
	if err := persistVAPIDKey(path, key); err != nil {
		if errors.Is(err, os.ErrExist) {
			// A concurrent boot won the race and its key is the identity now;
			// adopt it (minted=false; this pod minted nothing that survived).
			key, rerr := readVAPIDKeyFile(path)
			if rerr != nil {
				return nil, false, fmt.Errorf("webpush: a concurrent mint won %s but its key is unreadable: %w", path, rerr)
			}
			return key, false, nil
		}
		return nil, false, err
	}
	return key, true, nil
}

// readVAPIDKeyFile loads and parses the key at path. A missing file returns
// the bare not-exist error (the caller's mint signal); any other problem is
// the boot error, an unusable file naming the re-mint consequence.
func readVAPIDKeyFile(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-configured credential path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("approval.push.webpush.vapidKeyFile: %w", err)
	}
	key, perr := parseVAPIDKeyPEM(raw)
	if perr != nil {
		return nil, fmt.Errorf(
			"approval.push.webpush.vapidKeyFile: %s is unusable (%v). Restore it from backup; deleting it mints a NEW push identity and every keyed webpush/unifiedpush subscription must re-register",
			path, perr)
	}
	return key, nil
}

// persistVAPIDKey writes the key as PKCS#8 PEM, 0600, refusing to clobber an
// existing file. The key is written to a temporary file in the same
// directory and hard-linked to path, and the link fails with os.ErrExist
// when path exists, so path never appears without its whole key: a
// concurrent boot that reads path, or loses the link and re-reads, reads
// the winner's key whole.
func persistVAPIDKey(path string, key *ecdsa.PrivateKey) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("webpush: VAPID key dir: %w", err)
		}
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("webpush: encode VAPID key: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("webpush: persist VAPID key: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	werr := pem.Encode(tmp, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("webpush: persist VAPID key: %w", werr)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return fmt.Errorf("webpush: persist VAPID key: %w", err)
	}
	return nil
}

// parseVAPIDKeyPEM accepts PKCS#8 ("PRIVATE KEY", what we mint) or SEC1 ("EC
// PRIVATE KEY", the openssl ecparam default) and requires P-256.
func parseVAPIDKeyPEM(raw []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	var key *ecdsa.PrivateKey
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		key = k
	} else if anyKey, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := anyKey.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("not an EC key")
		}
		key = ec
	} else {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 key (RFC 8292 mandates ES256/P-256)")
	}
	return key, nil
}

// --- subscription key material + stored form ---

// decodeWebPushKeys strictly validates registration key material: p256dh must
// be unpadded base64url of exactly a 65-octet on-curve uncompressed P-256
// point, auth unpadded base64url of exactly 16 octets (RFC 8291 §3.2, and the
// exact serialization PushSubscription.toJSON() emits). Anything else is
// refused at registration; bad material is never stored.
func decodeWebPushKeys(p256dhB64, authB64 string) (uaPublic, authSecret []byte, err error) {
	uaPublic, err = base64.RawURLEncoding.Strict().DecodeString(p256dhB64)
	if err != nil {
		return nil, nil, fmt.Errorf("p256dh is not unpadded base64url: %w", err)
	}
	if len(uaPublic) != webpushPointLen {
		return nil, nil, fmt.Errorf("p256dh decodes to %d octets, want %d (uncompressed P-256 point)", len(uaPublic), webpushPointLen)
	}
	if _, err = ecdh.P256().NewPublicKey(uaPublic); err != nil {
		return nil, nil, fmt.Errorf("p256dh is not a valid uncompressed P-256 point: %w", err)
	}
	authSecret, err = base64.RawURLEncoding.Strict().DecodeString(authB64)
	if err != nil {
		return nil, nil, fmt.Errorf("auth is not unpadded base64url: %w", err)
	}
	if len(authSecret) != webpushAuthSecretLen {
		return nil, nil, fmt.Errorf("auth decodes to %d octets, want %d", len(authSecret), webpushAuthSecretLen)
	}
	return uaPublic, authSecret, nil
}

// A keyed subscription is stored in the existing token_or_endpoint column as
//
//	<endpoint>#p256dh=<b64url>&auth=<b64url>
//
// One row stays one subscription (endpoint and keys change together on
// every resubscribe, so they ARE one value), the store schema is untouched,
// and because a URL fragment is never sent on the wire the plain endpoint
// prefix stays what logs and url.Parse see. The fragment namespace is
// server-owned: registration refuses client endpoints containing '#', so a
// stored fragment can only be ours. splitKeyedEndpoint is exact-shape: any
// other fragment reads as keyless (legacy), never as garbage keys.

// encodeKeyedEndpoint builds the stored form. Inputs are already-validated
// base64url (no '&', '=' or '#' possible in that alphabet).
func encodeKeyedEndpoint(endpoint, p256dhB64, authB64 string) string {
	return endpoint + "#p256dh=" + p256dhB64 + "&auth=" + authB64
}

// splitKeyedEndpoint parses a stored token_or_endpoint. ok reports whether
// the exact server-written key fragment is present; !ok means a legacy/plain
// value (returned unchanged as endpoint).
func splitKeyedEndpoint(stored string) (endpoint, p256dhB64, authB64 string, ok bool) {
	i := strings.LastIndex(stored, "#")
	if i < 0 {
		return stored, "", "", false
	}
	rest, hasPrefix := strings.CutPrefix(stored[i+1:], "p256dh=")
	p, a, hasAuth := strings.Cut(rest, "&auth=")
	if !hasPrefix || !hasAuth || p == "" || a == "" ||
		strings.ContainsAny(p, "&=#") || strings.ContainsAny(a, "&=#") {
		return stored, "", "", false
	}
	return stored[:i], p, a, true
}

// webpushTopic derives the RFC 8030 §5.4 Topic header value from an approval
// reference: 32 chars of the URL-safe base64 alphabet, deterministic per ref.
// A later push with the same topic REPLACES a still-queued earlier one at the
// push service, exactly the reminder-replaces-notification behavior the
// mobile app implements with a stable notification id, now enforced upstream
// too. The ref is hashed (not truncated) so the topic leaks nothing and stays
// within the 32-char cap regardless of ref shape.
func webpushTopic(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}
