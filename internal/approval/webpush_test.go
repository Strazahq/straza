package approval

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// testSubscriptionKeys generates fresh subscription material (uncompressed
// public point + 16-octet auth secret) for registration/delivery tests.
func testSubscriptionKeys(t *testing.T) (uaPublic, authSecret []byte) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate subscription key: %v", err)
	}
	authSecret = make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	return priv.PublicKey().Bytes(), authSecret
}

// newTestWebPushSender builds a sender around a fresh in-memory VAPID key and
// a controllable clock (no key file involved; file semantics have their own
// tests).
func newTestWebPushSender(t *testing.T, contact string, now func() time.Time) *webPushSender {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate VAPID key: %v", err)
	}
	w, err := newWebPushSenderFromKey(key, contact)
	if err != nil {
		t.Fatalf("newWebPushSenderFromKey: %v", err)
	}
	if now != nil {
		w.now = now
	}
	return w
}

// parseVAPID splits an Authorization header into (jwt, key) and fails the test
// on any deviation from the RFC 8292 §3 `vapid t=…, k=…` shape.
func parseVAPID(t *testing.T, header string) (jwt, k string) {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid t=")
	if !ok {
		t.Fatalf("Authorization %q does not start with %q", header, "vapid t=")
	}
	jwt, k, ok = strings.Cut(rest, ", k=")
	if !ok {
		t.Fatalf("Authorization %q has no k parameter", header)
	}
	return jwt, k
}

// verifyVAPIDJWT checks the ES256 signature against pub and returns the
// decoded claims.
func verifyVAPIDJWT(t *testing.T, jwt string, pub *ecdsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts, want 3", len(parts))
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("JWT header b64: %v", err)
	}
	var hdr map[string]string
	if err := json.Unmarshal(hb, &hdr); err != nil {
		t.Fatalf("JWT header json: %v", err)
	}
	if hdr["alg"] != "ES256" || hdr["typ"] != "JWT" {
		t.Fatalf("JWT header = %v, want alg=ES256 typ=JWT", hdr)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("JWT sig b64: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("ES256 signature is %d octets, want 64 (raw R‖S, not DER)", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("ES256 signature does not verify against the VAPID public key")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("JWT claims b64: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatalf("JWT claims json: %v", err)
	}
	return claims
}

// TestVAPIDAuthorizationHeader: the minted header is RFC 8292-shaped: k is
// the sender's 65-octet uncompressed public point, the JWT verifies under it,
// aud is the push resource's ORIGIN (never the full URL), exp ≤ 24h out, and
// sub carries the configured contact.
func TestVAPIDAuthorizationHeader(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	w := newTestWebPushSender(t, "mailto:ops@example.com", func() time.Time { return at })

	header, err := w.vapidAuthorization("https://push.example.net/wpush/v2/gAAAA?x=1")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	jwt, k := parseVAPID(t, header)
	if k != w.publicKeyB64() {
		t.Errorf("k = %q, want the sender's public key %q", k, w.publicKeyB64())
	}
	kb, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil || len(kb) != 65 || kb[0] != 0x04 {
		t.Fatalf("k is not a 65-octet uncompressed point: len=%d err=%v", len(kb), err)
	}
	claims := verifyVAPIDJWT(t, jwt, &w.key.PublicKey)
	if claims["aud"] != "https://push.example.net" {
		t.Errorf("aud = %v, want the push resource origin https://push.example.net", claims["aud"])
	}
	if claims["sub"] != "mailto:ops@example.com" {
		t.Errorf("sub = %v, want the configured contact", claims["sub"])
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp claim missing/not numeric: %v", claims["exp"])
	}
	if got := time.Unix(int64(exp), 0); got.After(at.Add(24*time.Hour)) || !got.After(at) {
		t.Errorf("exp = %s, must be within (now, now+24h] (RFC 8292 §2)", got)
	}

	// No contact configured ⇒ no sub claim (never an empty string).
	w2 := newTestWebPushSender(t, "", func() time.Time { return at })
	hdr2, err := w2.vapidAuthorization("https://push.example.net/x")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	jwt2, _ := parseVAPID(t, hdr2)
	if _, has := verifyVAPIDJWT(t, jwt2, &w2.key.PublicKey)["sub"]; has {
		t.Error("sub claim present with no configured contact")
	}

	// A non-https endpoint never gets a token (push resources are https-only).
	if _, err := w.vapidAuthorization("http://push.example.net/x"); err == nil {
		t.Error("http endpoint must not mint a VAPID token")
	}
}

// TestVAPIDTokenCachePerOrigin: one JWT per origin within its lifetime; two
// endpoints on the same origin share a token, different origins get different
// aud claims, and advancing the clock past the refresh horizon mints anew.
func TestVAPIDTokenCachePerOrigin(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	now := &at
	w := newTestWebPushSender(t, "", func() time.Time { return *now })

	h1, err := w.vapidAuthorization("https://push.example.net/sub/AAA")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	h2, err := w.vapidAuthorization("https://push.example.net/sub/BBB?zzz=1")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	if h1 != h2 {
		t.Error("same origin minted two different tokens inside the cache window")
	}

	h3, err := w.vapidAuthorization("https://push.example.net:8443/sub")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	if h3 == h1 {
		t.Error("different origin (explicit port) reused the cached token")
	}
	jwt3, _ := parseVAPID(t, h3)
	if claims := verifyVAPIDJWT(t, jwt3, &w.key.PublicKey); claims["aud"] != "https://push.example.net:8443" {
		t.Errorf("aud = %v, want https://push.example.net:8443", claims["aud"])
	}

	// Past the refresh horizon a fresh token is minted.
	at = at.Add(vapidTokenTTL)
	h4, err := w.vapidAuthorization("https://push.example.net/sub/AAA")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	if h4 == h1 {
		t.Error("expired token was served from cache")
	}

	// An https default port normalizes away; origins compare equal.
	h5, err := w.vapidAuthorization("https://push.example.net:443/sub/CCC")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	if h5 != h4 {
		t.Error(":443 origin did not normalize to the portless https origin")
	}
}

// TestVAPIDKeyFileMintAndLoad: generate-once-persist. A configured path with
// no file mints a P-256 key (0600, parent dir created), a second boot loads
// the SAME key, and a corrupted file fails boot with the rotation caution
// instead of silently minting a new push identity.
func TestVAPIDKeyFileMintAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push", "vapid.pem")
	cfg := config.WebPush{VAPIDKeyFile: path, Contact: "mailto:ops@example.com"}

	w1, err := newWebPushSender(cfg, slog.Default())
	if err != nil {
		t.Fatalf("first boot (mint): %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("minted key file: %v", err)
	}
	// Windows FileMode reports the read-only attribute, not Unix permissions.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %o, want 0600 (the key is a credential)", fi.Mode().Perm())
	}

	w2, err := newWebPushSender(cfg, slog.Default())
	if err != nil {
		t.Fatalf("second boot (load): %v", err)
	}
	if w1.publicKeyB64() != w2.publicKeyB64() {
		t.Error("reload produced a different public key; the push identity did not persist")
	}

	// Corrupt file: boot fails loudly with the rotation consequence named,
	// never a silent re-mint (that would strand every subscription).
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := newWebPushSender(cfg, slog.Default()); err == nil ||
		!strings.Contains(err.Error(), "push identity") {
		t.Errorf("corrupt key file: want unusable-with-consequence error, got %v", err)
	}

	// A valid PEM of the wrong curve is refused (VAPID is ES256/P-256 only).
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("p384: %v", err)
	}
	writePEMKey(t, path, p384)
	if _, err := newWebPushSender(cfg, slog.Default()); err == nil ||
		!strings.Contains(err.Error(), "P-256") {
		t.Errorf("P-384 key: want curve error, got %v", err)
	}
}

// TestVAPIDKeyFileMintRaceLosesCleanly: two first boots racing one shared
// vapidKeyFile (multi-pod, shared volume, no key yet): the O_EXCL loser must
// load the winner's key and boot, not crash-loop.
// The winner is simulated by the race hook, which fires in the
// loser's window between the absence check and its own persist.
func TestVAPIDKeyFileMintRaceLosesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.pem")
	winner, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("winner key: %v", err)
	}
	testHookVAPIDMintRace = func() {
		if err := persistVAPIDKey(path, winner); err != nil {
			t.Errorf("winner persist: %v", err)
		}
	}
	t.Cleanup(func() { testHookVAPIDMintRace = nil })

	loser, err := newWebPushSender(config.WebPush{VAPIDKeyFile: path}, slog.Default())
	if err != nil {
		t.Fatalf("the race loser must boot on the winner's key, got: %v", err)
	}
	want, err := newWebPushSenderFromKey(winner, "")
	if err != nil {
		t.Fatalf("winner sender: %v", err)
	}
	if loser.publicKeyB64() != want.publicKeyB64() {
		t.Error("loser did not adopt the winner's key: split push identity across pods")
	}

	// And the file on disk IS the winner's key for every later boot.
	testHookVAPIDMintRace = nil
	again, err := newWebPushSender(config.WebPush{VAPIDKeyFile: path}, slog.Default())
	if err != nil {
		t.Fatalf("post-race boot: %v", err)
	}
	if again.publicKeyB64() != want.publicKeyB64() {
		t.Error("post-race boot loaded a different key")
	}
}

// TestVAPIDKeyFileMintRaceRereadFails: when the loser's re-read finds an
// unusable file (the racing writer was not a healthy strazad), boot fails
// with the real error; the retry is bounded to one.
func TestVAPIDKeyFileMintRaceRereadFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.pem")
	testHookVAPIDMintRace = func() {
		if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
			t.Errorf("race write: %v", err)
		}
	}
	t.Cleanup(func() { testHookVAPIDMintRace = nil })

	if _, err := newWebPushSender(config.WebPush{VAPIDKeyFile: path}, slog.Default()); err == nil ||
		!strings.Contains(err.Error(), "push identity") {
		t.Errorf("unusable racing file: want the unusable-with-consequence boot error, got %v", err)
	}
}

func writePEMKey(t *testing.T, path string, key *ecdsa.PrivateKey) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := persistVAPIDKey(path, key); err != nil {
		t.Fatalf("persist: %v", err)
	}
}

// TestWebPushTopicShape: the Topic header value derived from an approval ref
// is deterministic, ≤32 chars, strictly URL-safe base64 alphabet (RFC 8030
// §5.4), and distinct refs get distinct topics.
func TestWebPushTopicShape(t *testing.T) {
	a := webpushTopic("apr-0198c2f0-79aa-7bbb-8000-3a5c56ab91d2")
	b := webpushTopic("apr-0198c2f0-79aa-7bbb-8000-3a5c56ab91d3")
	if a != webpushTopic("apr-0198c2f0-79aa-7bbb-8000-3a5c56ab91d2") {
		t.Error("topic is not deterministic per ref")
	}
	if a == b {
		t.Error("distinct refs share a topic")
	}
	for _, tp := range []string{a, b} {
		if len(tp) == 0 || len(tp) > 32 {
			t.Errorf("topic %q length %d, want 1..32", tp, len(tp))
		}
		if strings.Trim(tp, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			t.Errorf("topic %q leaves the URL-safe base64 alphabet", tp)
		}
	}
}

// TestKeyedEndpointCodec: the stored token_or_endpoint form for a keyed
// subscription is the endpoint plus a server-owned URL fragment. Splitting is
// exact-shape: a plain endpoint, a foreign fragment, or a half fragment all
// read back as keyless (legacy) rather than shipping garbage keys.
func TestKeyedEndpointCodec(t *testing.T) {
	stored := encodeKeyedEndpoint("https://ntfy.example/up1?up=1", "BPub", "AuTh")
	ep, p256dh, auth, ok := splitKeyedEndpoint(stored)
	if !ok || ep != "https://ntfy.example/up1?up=1" || p256dh != "BPub" || auth != "AuTh" {
		t.Errorf("roundtrip = (%q,%q,%q,%v)", ep, p256dh, auth, ok)
	}

	for _, raw := range []string{
		"https://ntfy.example/topic",            // legacy plain endpoint
		"https://ntfy.example/topic#section",    // foreign fragment
		"https://ntfy.example/topic#p256dh=x",   // half a key set
		"https://ntfy.example/topic#auth=y",     // other half
		"https://ntfy.example/t#auth=y&extra=z", // unknown params
		"opaque-device-token",                   // fcm/apns tokens
	} {
		if _, _, _, ok := splitKeyedEndpoint(raw); ok {
			t.Errorf("splitKeyedEndpoint(%q) claimed keys", raw)
		}
	}
}

// TestDecodeWebPushKeysStrict: registration key material must be UNPADDED
// base64url of exactly a 65-octet on-curve uncompressed point and a 16-octet
// secret; every deviation is refused at registration, never stored.
func TestDecodeWebPushKeysStrict(t *testing.T) {
	uaPub, uaAuth := testSubscriptionKeys(t)
	goodP := base64.RawURLEncoding.EncodeToString(uaPub)
	goodA := base64.RawURLEncoding.EncodeToString(uaAuth)

	if _, _, err := decodeWebPushKeys(goodP, goodA); err != nil {
		t.Fatalf("valid keys refused: %v", err)
	}
	cases := []struct{ name, p, a string }{
		{"padded p256dh", goodP + "=", goodA},
		{"standard-alphabet p256dh", "A+" + goodP[2:], goodA},
		{"p256dh not base64", "!!!", goodA},
		{"p256dh wrong length", goodP[:80], goodA},
		{"auth wrong length", goodP, base64.RawURLEncoding.EncodeToString(uaAuth[:15])},
		{"auth padded", goodP, goodA + "=="},
		{"empty p256dh", "", goodA},
		{"empty auth", goodP, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := decodeWebPushKeys(tc.p, tc.a); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}
