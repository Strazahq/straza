package approval

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/config"
)

// APNs sender for the native iOS approver app. Hand-rolled on the standard
// library like the FCM and WebPush senders beside it: the one maintained
// third-party client (sideshow/apns2 v0.25.0) pins a 2022 x/net with two
// govulncheck findings in called code and no tagged fix, and every alternative
// is archived. The full sender is about 300 lines. Each trap it encodes fails
// SILENTLY when gotten wrong, so the tests in push_apns_test.go pin them
// individually.
//
// Payload privacy is the same rule as every other lane: generic fixed alert
// copy plus the opaque {v,ref,kind} envelope. No tool, requester, or
// request-derived string ever reaches a lock screen (App Review guideline
// 4.5.4 is satisfied by construction).

const (
	apnsHostProduction = "https://api.push.apple.com"
	apnsHostSandbox    = "https://api.sandbox.push.apple.com"

	// apnsJWTBucket is the provider-token refresh cadence. Apple wants
	// refresh inside a 20-60 minute window and tracks minting GLOBALLY per
	// Team ID (DTS 702056: not per connection, not per key), so replicas must
	// not mint on independent schedules: the token renews when the wall
	// clock crosses a 45-minute bucket boundary, which every pod computes
	// identically. Per-request minting earns 429 TooManyProviderTokenUpdates
	// immediately.
	apnsJWTBucket = 45 * time.Minute

	// apnsPingInterval is HTTP2Config.SendPingTimeout: how long a silent
	// connection sits before a PING frame health-checks it. This is Apple's
	// own guidance for detecting half-open connections; without it a zombie
	// connection hangs the sender while approvals silently stop ringing.
	// 15-30s is the sane band (TestAPNSTransportPinned).
	apnsPingInterval = 20 * time.Second

	// apnsDefaultExpiry backs apns-expiration when the push is not a decide
	// nudge with a known decision window: the resolution fact stays true, so
	// it may wait for a briefly-offline phone (the pushTTL 24h idiom).
	apnsDefaultExpiry = 24 * time.Hour
)

// apnsSender holds the ES256 signing identity, the per-team provider-token
// cache, and the long-lived HTTP/2 client. Safe for concurrent use.
type apnsSender struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string
	topic  string
	host   string // production or sandbox endpoint; tests override
	httpc  *http.Client
	now    func() time.Time

	mu        sync.Mutex
	jwt       string // cached provider token
	jwtBucket int64  // wall-clock bucket the cached token belongs to
}

// apnsResult is one delivery attempt's outcome: the HTTP status, Apple's
// reason string (empty on success), the invalidation timestamp (only on 410
// Unregistered, the prune guard's horizon), and the self-minted apns-id that
// correlates strazad logs with Apple's delivery records.
type apnsResult struct {
	status    int
	reason    string
	timestamp time.Time
	id        string
}

// newAPNSSender loads the .p8 auth key and prepares the sender. Any key
// problem fails boot (the newWebPushSender rationale: delivery is best-effort
// and only logs, so a key error surfacing there would be swallowed forever).
// Unlike the VAPID key the file is NEVER minted: Apple is the only mint, so
// an absent file is a boot error naming the fix, not a generate-once trigger.
func newAPNSSender(cfg config.APNSPush) (*apnsSender, error) {
	raw, err := os.ReadFile(cfg.KeyFile) // #nosec G304 -- operator-configured credential path
	if err != nil {
		return nil, fmt.Errorf("approval.push.apns.keyFile: %w (the .p8 is minted in the Apple developer portal and copied to the server; strazad never creates one)", err)
	}
	key, err := parseAPNSAuthKey(raw)
	if err != nil {
		return nil, fmt.Errorf("approval.push.apns.keyFile: %s: %w", cfg.KeyFile, err)
	}
	return newAPNSSenderFromKey(key, cfg)
}

// newAPNSSenderFromKey wraps an already-held key (the file-less test seam).
// ES256 is P-256 by definition, so any other curve is refused.
func newAPNSSenderFromKey(key *ecdsa.PrivateKey, cfg config.APNSPush) (*apnsSender, error) {
	if key.Curve != elliptic.P256() {
		return nil, errors.New("apns: the auth key is not a P-256 key (APNs provider tokens are ES256)")
	}
	host := apnsHostProduction
	if cfg.Environment == "sandbox" {
		host = apnsHostSandbox
	}
	return &apnsSender{
		key:    key,
		keyID:  cfg.KeyID,
		teamID: cfg.TeamID,
		topic:  cfg.Topic,
		host:   host,
		httpc:  &http.Client{Transport: newAPNSTransport(), CheckRedirect: approvalRedirect},
		now:    time.Now,
	}, nil
}

// newAPNSTransport builds the dedicated APNs transport. Every setting here is
// load-bearing and pinned by TestAPNSTransportPinned:
//
//   - ForceAttemptHTTP2: APNs speaks HTTP/2 only. A client that falls to
//     HTTP/1.1 dies on every push with a malformed-status error (an outage,
//     not a downgrade), which is why the flag is set even while no custom
//     TLSClientConfig demands it.
//   - HTTP2.SendPingTimeout: the half-open-connection health check (see
//     apnsPingInterval). INERT before go 1.26 (go.dev/issue/67813); the
//     go.mod directive plus TestGoDirectiveRequires126 forbid such a build.
//   - IdleConnTimeout 0: one connection serves hours of pushes. Connection
//     churn is the documented way to read as a DoS to Apple, and Go resolves
//     DNS fresh on each NEW connection, satisfying the uncached-DNS advice
//     with no extra code.
func newAPNSTransport() *http.Transport {
	return &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: true,
		IdleConnTimeout:   0,
		HTTP2:             &http.HTTP2Config{SendPingTimeout: apnsPingInterval},
	}
}

// parseAPNSAuthKey decodes the .p8 exactly as the developer portal ships it
// (PEM PKCS#8 wrapping an EC key), accepting SEC1 as a courtesy for keys
// re-encoded by openssl. The curve is checked by the caller.
func parseAPNSAuthKey(raw []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("not PEM (expected the AuthKey_*.p8 exactly as downloaded from the developer portal)")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if ec, secErr := x509.ParseECPrivateKey(block.Bytes); secErr == nil {
			return ec, nil
		}
		return nil, fmt.Errorf("parse .p8: %w", err)
	}
	ec, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the .p8 does not hold an EC key (APNs provider tokens are ES256/P-256)")
	}
	return ec, nil
}

// providerToken returns the cached provider token, minting when the wall
// clock has crossed into a new bucket. Serialized by mu so concurrent sends
// single-flight the mint.
func (a *apnsSender) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	bucketSec := int64(apnsJWTBucket / time.Second)
	bucket := a.now().Unix() / bucketSec
	if a.jwt != "" && a.jwtBucket == bucket {
		return a.jwt, nil
	}
	// iat is the bucket START, so every replica that mints in this bucket
	// carries identical claims (deterministic per-team refresh).
	return a.mintLocked(bucket, bucket*bucketSec)
}

// remintProviderToken force-mints with iat = now, bypassing the cache: the
// ExpiredProviderToken path, where Apple judged the bucket-start iat too old
// (clock skew) and a fresh timestamp is the only fix.
func (a *apnsSender) remintProviderToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	nowUnix := a.now().Unix()
	return a.mintLocked(nowUnix/int64(apnsJWTBucket/time.Second), nowUnix)
}

// mintLocked signs and caches a provider token; the caller holds mu.
func (a *apnsSender) mintLocked(bucket, iat int64) (string, error) {
	jwt, err := a.signProviderJWT(iat)
	if err != nil {
		return "", err
	}
	a.jwt, a.jwtBucket = jwt, bucket
	return jwt, nil
}

// signProviderJWT mints the ES256 provider token: header {alg,kid}, claims
// {iss: team id, iat}, signature raw R‖S (exactly 64 octets, RFC 7518 3.4,
// leading zeros preserved via FillBytes; the JOSE form, never ASN.1 DER).
// SignASN1 output is 69-72 bytes and r.Bytes() goes short roughly 1/256 of
// the time; either way the failure is an INTERMITTENT 403
// InvalidProviderToken, which is why TestAPNSProviderJWTRawRS64 asserts the
// shape across hundreds of signatures.
func (a *apnsSender) signProviderJWT(iat int64) (string, error) {
	hb, err := json.Marshal(map[string]string{"alg": "ES256", "kid": a.keyID})
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(map[string]any{"iss": a.teamID, "iat": iat})
	if err != nil {
		return "", err
	}
	input := b64url(hb) + "." + b64url(cb)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign provider token: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64url(sig), nil
}

// send delivers one push. A provider token Apple refuses as expired is
// re-minted and retried exactly once; every other outcome is returned to the
// caller for classification. err is transport-level failure only (no
// response); an HTTP error status comes back inside apnsResult.
func (a *apnsSender) send(ctx context.Context, deviceToken, kind, ref string, exp time.Time) (apnsResult, error) {
	res, err := a.sendOnce(ctx, deviceToken, kind, ref, exp)
	if err == nil && res.reason == "ExpiredProviderToken" {
		if _, merr := a.remintProviderToken(); merr != nil {
			return res, merr
		}
		return a.sendOnce(ctx, deviceToken, kind, ref, exp)
	}
	return res, err
}

// sendOnce performs one HTTP/2 POST to /3/device/<token> with the contract's
// exact header set. apns-collapse-id is deliberately ABSENT: collapse would
// silently REPLACE an older pending approval on the lock screen, and hiding
// a pending request is the one thing an approval surface must not do.
func (a *apnsSender) sendOnce(ctx context.Context, deviceToken, kind, ref string, exp time.Time) (apnsResult, error) {
	token, err := a.providerToken()
	if err != nil {
		return apnsResult{}, err
	}
	body, err := apnsBody(kind, ref)
	if err != nil {
		return apnsResult{}, err
	}
	id := uuid.NewString()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.host+"/3/device/"+deviceToken, bytes.NewReader(body))
	if err != nil {
		return apnsResult{id: id}, err
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("apns-id", id)
	req.Header.Set("apns-topic", a.topic)
	// An omitted or mismatched push type lets APNs drop the notification
	// SILENTLY on some platforms; always send it.
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", strconv.FormatInt(a.expiration(kind, exp), 10))
	resp, err := doRequest(a.httpc, req)
	if err != nil {
		return apnsResult{id: id}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	res := apnsResult{status: resp.StatusCode, id: id}
	if resp.StatusCode != http.StatusOK && len(raw) > 0 {
		var e struct {
			Reason    string `json:"reason"`
			Timestamp int64  `json:"timestamp"`
		}
		if json.Unmarshal(raw, &e) == nil {
			res.reason = e.Reason
			if e.Timestamp > 0 {
				res.timestamp = time.UnixMilli(e.Timestamp)
			}
		}
	}
	return res, nil
}

// expiration computes the apns-expiration header value (absolute epoch
// seconds, APNs' form of the TTL the other lanes send): a decide push with a
// known decision window must never ring after the request can no longer be
// decided, so it dies exactly at expires_at; status pushes and unknown
// expiries get 24 hours, mirroring pushTTL (the resolution fact stays true
// for a briefly-offline phone).
func (a *apnsSender) expiration(kind string, exp time.Time) int64 {
	if kind == pushKindDecide && !exp.IsZero() {
		return exp.Unix()
	}
	return a.now().Add(apnsDefaultExpiry).Unix()
}

// apnsBody renders the alert payload. The aps dictionary carries only
// generic fixed copy, and the opaque {v,ref,kind} envelope rides as PEERS of
// aps (Apple ignores custom keys placed inside it). The time-sensitive
// interruption level goes ONLY on decide pushes: it exists to break through
// Focus for a decision the user must make within the hour, misuse burns the
// privilege per user permanently, and a resolution note is not time-critical.
func apnsBody(kind, ref string) ([]byte, error) {
	alert := map[string]string{
		"title": "Approval requested",
		"body":  "Open Straza to review a pending request.",
	}
	if kind != pushKindDecide {
		alert = map[string]string{
			"title": "Request update",
			"body":  "Open Straza to see the result of a request.",
		}
	}
	aps := map[string]any{"alert": alert, "sound": "default"}
	if kind == pushKindDecide {
		aps["interruption-level"] = "time-sensitive"
	}
	return json.Marshal(map[string]any{"aps": aps, "v": 1, "ref": ref, "kind": kind})
}

// apnsAction is the per-reason handling class (classifyAPNS).
type apnsAction int

const (
	// apnsDrop: never retry, keep the registration.
	apnsDrop apnsAction = iota
	// apnsPrune: never retry AND remove the registration (behind the
	// registered-at guard; see pruneDeadBefore).
	apnsPrune
	// apnsRemint: the provider token was refused as expired; re-mint and
	// retry once (handled inside send; reaching the classifier means the
	// retry ALSO failed).
	apnsRemint
	// apnsRetryLater: transient upstream state. v1 keeps no retry queue:
	// delivery is best-effort, the reminder push re-announces near expiry,
	// and the app's poll floor covers the gap. Classified separately from
	// drop so logs tell a transient miss from a permanent one.
	apnsRetryLater
)

// classifyAPNS maps one response to its handling class. Branch on the reason
// string, never the status alone: the two 429s demand OPPOSITE fixes
// (TooManyProviderTokenUpdates means our JWT cache is broken;
// TooManyRequests backs off the device token), and three different 403s
// carry three different meanings.
func classifyAPNS(status int, reason string) apnsAction {
	switch reason {
	case "Unregistered", "BadDeviceToken", "DeviceTokenNotForTopic", "ExpiredToken":
		return apnsPrune
	case "ExpiredProviderToken":
		return apnsRemint
	case "TooManyRequests":
		return apnsRetryLater
	}
	if status >= 500 {
		return apnsRetryLater
	}
	// PayloadTooLarge, Forbidden, InvalidProviderToken,
	// TooManyProviderTokenUpdates, and anything unknown: drop. Fail closed
	// for the notification, never retry blind (the decision lane is
	// untouched either way).
	return apnsDrop
}
