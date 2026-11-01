package authn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

func testRepo(t testing.TB) store.SigningKeyRepo {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "t.db")
	storetest.SeedSQLite(t, dsn)
	s, err := store.Open(config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    dsn,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s.SigningKeys()
}

func testService(t *testing.T, ttl time.Duration) *TokenService {
	t.Helper()
	svc, err := NewTokenService(context.Background(), testRepo(t), "https://straza.local", ttl)
	if err != nil {
		t.Fatalf("NewTokenService: %v", err)
	}
	return svc
}

func sampleClaims() Claims {
	return Claims{
		Subject: "user-1", Session: "ses-1", Device: "dev-1",
		Harness: "claude-code/2.1.0", Attestation: "managed",
		RolesHash: RolesHash([]string{"r2", "r1"}), Snapshot: "snap-1",
	}
}

func TestMintVerifyRoundTrip(t *testing.T) {
	svc := testService(t, 0)
	raw, minted, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if minted.JTI == "" || minted.Expiry.Sub(minted.IssuedAt) != DefaultTokenTTL {
		t.Errorf("minted claims: %+v", minted)
	}

	got, err := svc.Verify(raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := sampleClaims()
	if got.Subject != want.Subject || got.Session != want.Session || got.Device != want.Device ||
		got.Harness != want.Harness || got.Attestation != want.Attestation ||
		got.RolesHash != want.RolesHash || got.Snapshot != want.Snapshot || got.JTI != minted.JTI {
		t.Errorf("claims round-trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	svc := testService(t, 0)
	// A token that expired an hour ago, signed with the service's own key:
	// signature valid, expiry not.
	expired := mintWithTimes(t, svc, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if _, err := svc.Verify(expired); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expired token accepted: %v", err)
	}
	// A token just inside the 30s acceptable skew still verifies.
	fresh := mintWithTimes(t, svc, time.Now().Add(-time.Minute), time.Now().Add(-10*time.Second))
	if _, err := svc.Verify(fresh); err != nil {
		t.Fatalf("token within skew rejected: %v", err)
	}
}

// TestVerifyRejectsClockSkew is fault injection: a token minted by a clock
// far AHEAD of the verifier must be refused beyond the 30 s window. A
// future-dated iat/exp pair would otherwise extend the token's effective
// lifetime past the 300 s revocation bound. Small skew stays fine.
func TestVerifyRejectsClockSkew(t *testing.T) {
	svc := testService(t, 0)
	now := time.Now()

	future := mintWithTimes(t, svc, now.Add(2*time.Minute), now.Add(7*time.Minute))
	if _, err := svc.Verify(future); err == nil {
		t.Fatal("future-iat token accepted: a skewed minter can extend token lifetime past the TTL bound")
	}

	// 10 s ahead is inside the 30 s tolerance: real clusters drift.
	slight := mintWithTimes(t, svc, now.Add(10*time.Second), now.Add(5*time.Minute))
	if _, err := svc.Verify(slight); err != nil {
		t.Fatalf("token within forward skew rejected: %v", err)
	}
}

// mintWithTimes signs a token with explicit iat/exp using the service's
// active key, for expiry-path testing only.
func mintWithTimes(t *testing.T, svc *TokenService, iat, exp time.Time) string {
	t.Helper()
	tok, err := jwt.NewBuilder().
		Issuer("https://straza.local").Subject("user-1").JwtID("jti-x").
		IssuedAt(iat).Expiration(exp).Build()
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.RLock()
	key := svc.active
	svc.mu.RUnlock()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	svc := testService(t, 0)
	other, err := NewTokenService(context.Background(), testRepo(t), "https://evil.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := other.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(raw); err == nil {
		t.Fatal("token from another issuer accepted")
	}
}

func TestVerifyRejectsUnknownKey(t *testing.T) {
	svc := testService(t, 0)
	// Forge a token signed by a key the service has never seen.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	forgeKey, _ := jwk.Import(priv)
	_ = forgeKey.Set(jwk.KeyIDKey, "unknown-kid")
	_ = forgeKey.Set(jwk.AlgorithmKey, jwa.EdDSA())
	tok, _ := jwt.NewBuilder().Issuer("https://straza.local").Subject("user-1").
		IssuedAt(time.Now()).Expiration(time.Now().Add(time.Minute)).Build()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), forgeKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(string(signed)); err == nil {
		t.Fatal("forged token accepted")
	}
}

func TestVerifyRejectsAlgConfusion(t *testing.T) {
	svc := testService(t, 0)
	// HS256 token with the issuer string as a shared secret; it must not pass.
	symKey, _ := jwk.Import([]byte("https://straza.local"))
	tok, _ := jwt.NewBuilder().Issuer("https://straza.local").Subject("user-1").
		IssuedAt(time.Now()).Expiration(time.Now().Add(time.Minute)).Build()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), symKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(string(signed)); err == nil {
		t.Fatal("HS256 token accepted (algorithm confusion)")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	svc := testService(t, 0)
	for _, raw := range []string{"", "not-a-jwt", "a.b.c", strings.Repeat("x", 5000)} {
		if _, err := svc.Verify(raw); err == nil {
			t.Errorf("garbage %q accepted", raw[:min(len(raw), 12)])
		}
	}
}

// kidOf reads the kid header of a signed token, so a test can tell which
// key minted it.
func kidOf(t *testing.T, raw string) string {
	t.Helper()
	msg, err := jws.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	kid, ok := msg.Signatures()[0].ProtectedHeaders().KeyID()
	if !ok || kid == "" {
		t.Fatal("token carries no kid header")
	}
	return kid
}

// keyStatuses reads the session keys back from the store as kid to status.
func keyStatuses(t *testing.T, repo store.SigningKeyRepo) map[string]string {
	t.Helper()
	keys, err := repo.ListByPurpose(context.Background(), store.KeyPurposeSession)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k.KID] = k.Status
	}
	return out
}

// jwksKIDs reads the kid of every key the JWKS document publishes.
func jwksKIDs(t *testing.T, svc *TokenService) map[string]bool {
	t.Helper()
	raw, err := svc.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("JWKS not valid JSON: %v", err)
	}
	out := make(map[string]bool, len(doc.Keys))
	for _, k := range doc.Keys {
		out[k["kid"].(string)] = true
	}
	return out
}

// TestStageIsIdempotent pins the first phase of a rotation: Stage creates one
// staged key, a second call returns that same key instead of a new one, and
// the active key keeps signing until the janitor promotes the staged one.
func TestStageIsIdempotent(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	svc, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Stage(ctx)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	second, err := svc.Stage(ctx)
	if err != nil {
		t.Fatalf("second Stage: %v", err)
	}
	if first.KID != second.KID || first.Status != store.KeyStaged {
		t.Errorf("Stage twice = %s/%s and %s/%s, want the same staged key", first.KID, first.Status, second.KID, second.Status)
	}
	var staged, active int
	for _, status := range keyStatuses(t, repo) {
		switch status {
		case store.KeyStaged:
			staged++
		case store.KeyActive:
			active++
		}
	}
	if staged != 1 || active != 1 {
		t.Errorf("store holds %d staged and %d active keys, want 1 and 1", staged, active)
	}
	after, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if kidOf(t, after) != kidOf(t, before) {
		t.Error("a staged key signed a token before it was promoted")
	}
}

// TestRotationLifecycle walks one key through staged, active, retiring and
// retired with a caller-supplied clock, and pins at every step which key
// signs, which tokens verify and which public keys the JWKS carries. The
// horizon is the caller's: Advance retires a retiring key once retireAfter
// has passed since it stopped signing.
func TestRotationLifecycle(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	svc, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	const retireAfter = 31 * 24 * time.Hour
	before, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	oldKID := kidOf(t, before)
	staged, err := svc.Stage(ctx)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	// Every store stamp so far is at or before base; the steps below move
	// only the clock Advance is handed.
	base := time.Now()

	steps := []struct {
		name            string
		now             time.Time
		wantPromoted    string
		wantRetired     []string
		wantStatuses    map[string]string
		wantOldVerifies bool
		wantMintKID     string
		wantJWKS        []string
	}{
		{
			name:            "one interval after staging is too early to promote",
			now:             base.Add(KeyReloadInterval),
			wantStatuses:    map[string]string{oldKID: store.KeyActive, staged.KID: store.KeyStaged},
			wantOldVerifies: true, wantMintKID: oldKID, wantJWKS: []string{oldKID, staged.KID},
		},
		{
			name:            "two intervals after staging promote the staged key",
			now:             base.Add(2*KeyReloadInterval + time.Second),
			wantPromoted:    staged.KID,
			wantStatuses:    map[string]string{oldKID: store.KeyRetiring, staged.KID: store.KeyActive},
			wantOldVerifies: true, wantMintKID: staged.KID, wantJWKS: []string{oldKID, staged.KID},
		},
		{
			name:            "a second tick at the same time changes nothing",
			now:             base.Add(2*KeyReloadInterval + time.Second),
			wantStatuses:    map[string]string{oldKID: store.KeyRetiring, staged.KID: store.KeyActive},
			wantOldVerifies: true, wantMintKID: staged.KID, wantJWKS: []string{oldKID, staged.KID},
		},
		{
			name:            "the retiring key holds until the horizon",
			now:             base.Add(retireAfter - KeyReloadInterval),
			wantStatuses:    map[string]string{oldKID: store.KeyRetiring, staged.KID: store.KeyActive},
			wantOldVerifies: true, wantMintKID: staged.KID, wantJWKS: []string{oldKID, staged.KID},
		},
		{
			name:            "the horizon retires the old key",
			now:             base.Add(retireAfter + KeyReloadInterval),
			wantRetired:     []string{oldKID},
			wantStatuses:    map[string]string{oldKID: store.KeyRetired, staged.KID: store.KeyActive},
			wantOldVerifies: false, wantMintKID: staged.KID, wantJWKS: []string{staged.KID},
		},
		{
			name:            "retirement is not repeated",
			now:             base.Add(retireAfter + 2*KeyReloadInterval),
			wantStatuses:    map[string]string{oldKID: store.KeyRetired, staged.KID: store.KeyActive},
			wantOldVerifies: false, wantMintKID: staged.KID, wantJWKS: []string{staged.KID},
		},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			got, err := svc.Advance(ctx, st.now, retireAfter)
			if err != nil {
				t.Fatalf("Advance: %v", err)
			}
			if got.Promoted != st.wantPromoted {
				t.Errorf("promoted = %q, want %q", got.Promoted, st.wantPromoted)
			}
			if len(got.Retired) != len(st.wantRetired) || (len(got.Retired) == 1 && got.Retired[0] != st.wantRetired[0]) {
				t.Errorf("retired = %v, want %v", got.Retired, st.wantRetired)
			}
			statuses := keyStatuses(t, repo)
			for kid, want := range st.wantStatuses {
				if statuses[kid] != want {
					t.Errorf("key %s status = %q, want %q", kid, statuses[kid], want)
				}
			}
			if len(statuses) != len(st.wantStatuses) {
				t.Errorf("store holds %d keys, want %d: %v", len(statuses), len(st.wantStatuses), statuses)
			}
			if _, err := svc.Verify(before); (err == nil) != st.wantOldVerifies {
				t.Errorf("token from the old key verifies = %v (%v), want %v", err == nil, err, st.wantOldVerifies)
			}
			minted, _, err := svc.Mint(sampleClaims())
			if err != nil {
				t.Fatal(err)
			}
			if kid := kidOf(t, minted); kid != st.wantMintKID {
				t.Errorf("Mint signed with %s, want %s", kid, st.wantMintKID)
			}
			if _, err := svc.Verify(minted); err != nil {
				t.Errorf("freshly minted token rejected: %v", err)
			}
			jwks := jwksKIDs(t, svc)
			if len(jwks) != len(st.wantJWKS) {
				t.Errorf("JWKS carries %d keys, want %d", len(jwks), len(st.wantJWKS))
			}
			for _, kid := range st.wantJWKS {
				if !jwks[kid] {
					t.Errorf("JWKS lacks %s (%s)", kid, statuses[kid])
				}
			}
		})
	}

	// A fresh service over the same store agrees with the walk: it signs with
	// the promoted key and refuses the retired one.
	svc2, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.Verify(before); err == nil {
		t.Error("restarted service accepted a token signed by a retired key")
	}
	minted, _, err := svc2.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if kidOf(t, minted) != staged.KID {
		t.Error("restarted service does not sign with the promoted key")
	}
}

// TestReloadPicksUpPeerStaging pins why promotion waits two reload intervals:
// a replica that has not reloaded since a peer staged a key does not know
// that key, so a token the peer signs with it is refused there until the
// replica reloads. On a live deployment every replica reloads once per
// interval, so two intervals guarantee the reload happened before any
// replica signs.
func TestReloadPicksUpPeerStaging(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	peer, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	lagging, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Stage(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Advance(ctx, time.Now().Add(2*KeyReloadInterval+time.Second), time.Hour); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := peer.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lagging.Verify(fresh); err == nil {
		t.Fatal("a replica that never reloaded accepted a token from a key it has not seen")
	}
	if err := lagging.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := lagging.Verify(fresh); err != nil {
		t.Fatalf("token from the promoted key rejected after Reload: %v", err)
	}
}

// TestBootWithOnlyStagedKey pins the load behavior a two-phase rotation
// leans on: a store holding a staged key and no active one still boots with
// an active key (a fresh one is generated), the staged key stays staged and
// both public keys are published.
func TestBootWithOnlyStagedKey(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := repo.Create(ctx, store.SigningKey{
		Purpose: store.KeyPurposeSession, Status: store.KeyStaged, PrivateKey: priv.Seed(), PublicKey: pub,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatalf("NewTokenService: %v", err)
	}
	statuses := keyStatuses(t, repo)
	if statuses[staged.KID] != store.KeyStaged {
		t.Errorf("staged key status after boot = %q, want staged", statuses[staged.KID])
	}
	var active int
	for _, status := range statuses {
		if status == store.KeyActive {
			active++
		}
	}
	if active != 1 {
		t.Errorf("active keys after boot = %d, want 1", active)
	}
	minted, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if kidOf(t, minted) == staged.KID {
		t.Error("boot signed with the staged key")
	}
	if _, err := svc.Verify(minted); err != nil {
		t.Errorf("token from the generated key rejected: %v", err)
	}
	if jwks := jwksKIDs(t, svc); len(jwks) != 2 || !jwks[staged.KID] {
		t.Errorf("JWKS = %v, want the generated and the staged key", jwks)
	}
}

func TestJWKSExposesOnlyPublicMaterial(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	svc, err := NewTokenService(ctx, repo, "https://straza.local", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stage(ctx); err != nil {
		t.Fatal(err)
	}

	raw, err := svc.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("JWKS not valid JSON: %v", err)
	}
	if len(doc.Keys) != 2 {
		t.Errorf("JWKS keys = %d, want 2 (active + staged)", len(doc.Keys))
	}
	for _, k := range doc.Keys {
		if _, hasPriv := k["d"]; hasPriv {
			t.Fatal("JWKS leaks private key material (d present)")
		}
		if k["kty"] != "OKP" || k["kid"] == "" || k["use"] != "sig" {
			t.Errorf("unexpected JWK shape: %v", k)
		}
	}
}

func TestRolesHashDeterministic(t *testing.T) {
	a := RolesHash([]string{"r1", "r2", "r3"})
	b := RolesHash([]string{"r3", "r1", "r2"})
	if a != b {
		t.Errorf("order-sensitive: %s vs %s", a, b)
	}
	if a == RolesHash([]string{"r1", "r2"}) {
		t.Error("different role sets collide")
	}
	if RolesHash(nil) == "" {
		t.Error("empty set must still hash")
	}
}

// TestDeviceTokenLifecycle pins the device credential: a long-lived,
// purpose-scoped token minted at enroll that starts sessions after the
// 10-minute ID token dies, and is NOT interchangeable with session or ID
// tokens in either direction. A stolen device token must open exactly one
// door: checkin, where the denylist and user/device status are re-checked.
func TestDeviceTokenLifecycle(t *testing.T) {
	svc := testService(t, 0)

	raw, err := svc.MintDeviceToken("user-1", "dev-1", time.Hour)
	if err != nil {
		t.Fatalf("MintDeviceToken: %v", err)
	}
	dc, err := svc.VerifyDeviceToken(raw)
	if err != nil {
		t.Fatalf("VerifyDeviceToken: %v", err)
	}
	if dc.Subject != "user-1" || dc.Device != "dev-1" {
		t.Errorf("claims = %+v, want user-1/dev-1", dc)
	}
	// The lifetime rides the claims: check-in renewal reads it.
	if dc.IssuedAt.IsZero() || dc.Expiry.Sub(dc.IssuedAt) != time.Hour {
		t.Errorf("lifetime = %s to %s, want one hour from issue", dc.IssuedAt, dc.Expiry)
	}

	// Purpose separation, all four wrong doors:
	// 1. A session token is not a device token.
	ses, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyDeviceToken(ses); err == nil {
		t.Error("session token accepted as device token")
	}
	// 2. An ID token is not a device token.
	idt, err := svc.MintIDToken("user-1", "straza", time.Hour, "kim", "kim@x.io")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyDeviceToken(idt); err == nil {
		t.Error("ID token accepted as device token")
	}
	// 3. A device token is not a session token (no ses claim ⇒ callers'
	// claims.Session=="" check refuses it on every data-plane surface).
	if c, err := svc.Verify(raw); err == nil && c.Session != "" {
		t.Errorf("device token verified as session token with session %q", c.Session)
	}
	// 4. A device token is not an ID token (no audience ⇒ login refused).
	if _, err := svc.VerifyIDToken(raw, "straza"); err == nil {
		t.Error("device token accepted as ID token")
	}

	// Expiry is enforced (31 s past the 30 s verification skew).
	old, err := svc.MintDeviceToken("user-1", "dev-1", -31*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyDeviceToken(old); err == nil {
		t.Error("expired device token accepted")
	}
}

// TestConnectStateLifecycle pins the OAuth connect `state` parameter: a
// short-lived, purpose-scoped token binding the initiating user to one app,
// verifiable on any pod (stateless callback) and NOT interchangeable with any
// other credential. A forged or repurposed state must never let an attacker
// bind their upstream account to a victim's Straza identity.
func TestConnectStateLifecycle(t *testing.T) {
	svc := testService(t, 0)

	raw, err := svc.MintConnectState("user-1", "app-github")
	if err != nil {
		t.Fatalf("MintConnectState: %v", err)
	}
	cc, err := svc.VerifyConnectState(raw)
	if err != nil {
		t.Fatalf("VerifyConnectState: %v", err)
	}
	if cc.Subject != "user-1" || cc.App != "app-github" {
		t.Errorf("claims = %+v, want user-1/app-github", cc)
	}

	// Purpose separation, every wrong door:
	// 1. A session token is not a connect state.
	ses, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyConnectState(ses); err == nil {
		t.Error("session token accepted as connect state")
	}
	// 2. A device token is not a connect state.
	dev, err := svc.MintDeviceToken("user-1", "dev-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyConnectState(dev); err == nil {
		t.Error("device token accepted as connect state")
	}
	// 3. A connect state is not a session token (no ses claim).
	if c, err := svc.Verify(raw); err == nil && c.Session != "" {
		t.Errorf("connect state verified as session token with session %q", c.Session)
	}
	// 4. A connect state is not a device token.
	if _, err := svc.VerifyDeviceToken(raw); err == nil {
		t.Error("connect state accepted as device token")
	}
	// 5. A connect state is not an ID token (no audience).
	if _, err := svc.VerifyIDToken(raw, "straza"); err == nil {
		t.Error("connect state accepted as ID token")
	}

	// Expiry: states outlive the verification skew by design (10 min TTL),
	// but an expired one must be refused.
	expired, err := svc.mintConnectStateTTL("user-1", "app-github", -31*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyConnectState(expired); err == nil {
		t.Error("expired connect state accepted")
	}
}

// TestApproverTokenLifecycle pins the mobile approver device token: a
// purpose-scoped use=approver credential binding user + approver device,
// verifiable on any pod, and NOT interchangeable with any other credential.
// A session, device, connect or ID token must never open the /v1/approver/*
// surface, and an approver token must never open any other.
func TestApproverTokenLifecycle(t *testing.T) {
	svc := testService(t, 0)

	raw, err := svc.MintApproverToken("user-1", "apd_9", time.Hour)
	if err != nil {
		t.Fatalf("MintApproverToken: %v", err)
	}
	ac, err := svc.VerifyApproverToken(raw)
	if err != nil {
		t.Fatalf("VerifyApproverToken: %v", err)
	}
	if ac.Subject != "user-1" || ac.Device != "apd_9" {
		t.Errorf("claims = %+v, want user-1/apd_9", ac)
	}

	// Purpose separation, every wrong door into /v1/approver/*.
	ses, _, err := svc.Mint(sampleClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyApproverToken(ses); err == nil {
		t.Error("session token accepted as approver token")
	}
	dev, err := svc.MintDeviceToken("user-1", "dev-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyApproverToken(dev); err == nil {
		t.Error("device token accepted as approver token")
	}
	conn, err := svc.MintConnectState("user-1", "app-x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyApproverToken(conn); err == nil {
		t.Error("connect state accepted as approver token")
	}
	idt, err := svc.MintIDToken("user-1", "straza", time.Hour, "kim", "kim@x.io")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyApproverToken(idt); err == nil {
		t.Error("ID token accepted as approver token")
	}

	// The reverse doors: an approver token opens nothing else.
	if c, err := svc.Verify(raw); err == nil && c.Session != "" {
		t.Errorf("approver token verified as session token with session %q", c.Session)
	}
	if _, err := svc.VerifyDeviceToken(raw); err == nil {
		t.Error("approver token accepted as device token")
	}
	if _, err := svc.VerifyConnectState(raw); err == nil {
		t.Error("approver token accepted as connect state")
	}
	if _, err := svc.VerifyIDToken(raw, "straza"); err == nil {
		t.Error("approver token accepted as ID token")
	}

	// Expiry is enforced (31 s past the 30 s verification skew).
	old, err := svc.MintApproverToken("user-1", "apd_9", -31*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyApproverToken(old); err == nil {
		t.Error("expired approver token accepted")
	}
}

// TestMintIDTokenForLoginFacts pins the login facts of a built-in ID token:
// nonce is a string claim and auth_time a NumericDate in whole seconds, each
// minted only when set, and VerifyIDToken reads both back. MintIDToken keeps
// minting neither.
func TestMintIDTokenForLoginFacts(t *testing.T) {
	svc := testService(t, 0)
	signedIn := time.Date(2026, 10, 22, 9, 30, 15, 700_000_000, time.UTC)
	cases := []struct {
		name  string
		facts LoginFacts
	}{
		{name: "nonce and auth_time", facts: LoginFacts{Nonce: "jkt-part.random-part", AuthTime: signedIn}},
		{name: "nonce only", facts: LoginFacts{Nonce: "jkt-part.random-part"}},
		{name: "auth_time only", facts: LoginFacts{AuthTime: signedIn}},
		{name: "neither"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := svc.MintIDTokenFor("user-1", "straza", time.Hour, "kim", "kim@x.io", tc.facts)
			if err != nil {
				t.Fatalf("MintIDTokenFor: %v", err)
			}
			payload := payloadOf(t, raw)
			nonce, hasNonce := payload["nonce"]
			if hasNonce != (tc.facts.Nonce != "") || (hasNonce && nonce != tc.facts.Nonce) {
				t.Errorf("payload nonce = %v (present %v), want %q", nonce, hasNonce, tc.facts.Nonce)
			}
			authTime, hasAuthTime := payload["auth_time"]
			if hasAuthTime != !tc.facts.AuthTime.IsZero() {
				t.Errorf("payload auth_time present = %v, want %v", hasAuthTime, !tc.facts.AuthTime.IsZero())
			}
			if hasAuthTime && authTime != float64(tc.facts.AuthTime.Unix()) {
				t.Errorf("payload auth_time = %v, want the whole second %d", authTime, tc.facts.AuthTime.Unix())
			}

			got, err := svc.VerifyIDToken(raw, "straza")
			if err != nil {
				t.Fatalf("VerifyIDToken: %v", err)
			}
			if got.Subject != "user-1" || got.Username != "kim" || got.Email != "kim@x.io" {
				t.Errorf("claims = %+v, want user-1, kim, kim@x.io", got)
			}
			if got.Nonce != tc.facts.Nonce {
				t.Errorf("Nonce = %q, want %q", got.Nonce, tc.facts.Nonce)
			}
			if want := tc.facts.AuthTime.Truncate(time.Second); !got.AuthTime.Equal(want) {
				t.Errorf("AuthTime = %s, want %s", got.AuthTime, want)
			}
		})
	}

	plain, err := svc.MintIDToken("user-1", "straza", time.Hour, "kim", "kim@x.io")
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range []string{"nonce", "auth_time"} {
		if _, ok := payloadOf(t, plain)[claim]; ok {
			t.Errorf("MintIDToken minted the %s claim", claim)
		}
	}
}

// TestVerifyIDTokenRefusesMalformedAuthTime signs ID tokens with the
// service's own key, so only auth_time, nonce or straza_jkt is wrong, and
// checks that VerifyIDToken refuses each one rather than reading it as
// absent.
func TestVerifyIDTokenRefusesMalformedAuthTime(t *testing.T) {
	svc := testService(t, 0)
	cases := []struct {
		name  string
		claim string
		value any
	}{
		{"auth_time is a word", "auth_time", "yesterday"},
		{"auth_time is an object", "auth_time", map[string]any{"at": 1}},
		{"nonce is a number", "nonce", 7},
		{"straza_jkt is a number", "straza_jkt", 7},
		{"straza_jkt is the cnf object", "straza_jkt", map[string]any{"jkt": "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"}},
		{"straza_jkt is an array", "straza_jkt", []string{"kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"}},
		{"straza_jkt is empty", "straza_jkt", ""},
		{"straza_jkt is null", "straza_jkt", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			tok, err := jwt.NewBuilder().
				Issuer("https://straza.local").Subject("user-1").Audience([]string{"straza"}).
				IssuedAt(now).Expiration(now.Add(time.Minute)).
				Claim(tc.claim, tc.value).Build()
			if err != nil {
				t.Fatal(err)
			}
			svc.mu.RLock()
			key := svc.active
			svc.mu.RUnlock()
			signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := payloadOf(t, string(signed))[tc.claim]; !ok {
				t.Fatalf("the test token carries no %s member", tc.claim)
			}
			if c, err := svc.VerifyIDToken(string(signed), "straza"); err == nil {
				t.Fatalf("VerifyIDToken accepted a malformed %s and returned %+v", tc.claim, c)
			}
		})
	}
}

// TestIDTokenKeyStamp pins the straza_jkt claim of a built-in ID token: the
// thumbprint string from LoginFacts.KeyThumbprint, minted only when it is
// set and beside the other login facts, and read back by VerifyIDToken.
func TestIDTokenKeyStamp(t *testing.T) {
	svc := testService(t, 0)
	stamp := dpop.Thumbprint(testProofKey(0x07).Public().(ed25519.PublicKey))
	signedIn := time.Date(2026, 10, 27, 9, 30, 15, 0, time.UTC)
	cases := []struct {
		name  string
		facts LoginFacts
	}{
		{name: "an agent token names its key", facts: LoginFacts{KeyThumbprint: stamp}},
		{name: "the stamp rides beside nonce and auth_time", facts: LoginFacts{Nonce: "n-1", AuthTime: signedIn, KeyThumbprint: stamp}},
		{name: "a token minted without a key names none", facts: LoginFacts{Nonce: "n-1"}},
		{name: "a token with no facts names none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := svc.MintIDTokenFor("user-1", "straza", time.Hour, "ci-bot", "", tc.facts)
			if err != nil {
				t.Fatalf("MintIDTokenFor: %v", err)
			}
			got, present := payloadOf(t, raw)["straza_jkt"]
			if present != (tc.facts.KeyThumbprint != "") || (present && got != tc.facts.KeyThumbprint) {
				t.Errorf("payload straza_jkt = %v (present %v), want %q", got, present, tc.facts.KeyThumbprint)
			}
			if _, ok := payloadOf(t, raw)["cnf"]; ok {
				t.Error("an ID token carries cnf, which a Bearer verifier refuses")
			}
			claims, err := svc.VerifyIDToken(raw, "straza")
			if err != nil {
				t.Fatalf("VerifyIDToken: %v", err)
			}
			if claims.KeyThumbprint != tc.facts.KeyThumbprint || claims.Nonce != tc.facts.Nonce {
				t.Errorf("verified KeyThumbprint %q nonce %q, want %q and %q", claims.KeyThumbprint, claims.Nonce, tc.facts.KeyThumbprint, tc.facts.Nonce)
			}
		})
	}
	plain, err := svc.MintIDToken("user-1", "straza", time.Hour, "kim", "kim@x.io")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := payloadOf(t, plain)["straza_jkt"]; ok {
		t.Error("MintIDToken minted the straza_jkt claim")
	}
}

// The ID token examples come from fixed seeds and a fixed time, as the grant
// examples do, so TestIDTokenExamples pins every byte of every file.
var exampleIDTokenIat = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

const exampleIDTokenProvenance = "doc spec/session-token/SPEC.md 2026-10-27 (generated by TestIDTokenExamples, " +
	"not captured from strazad: the Straza session key from the Ed25519 seed of 32 bytes 0x0a with kid " +
	exampleGrantKID + ", the agent key behind straza_jkt from 32 bytes 0x07, signed at 2026-10-27T12:00:00Z)"

// idTokenExample is one file of spec/session-token/examples/id-token.
type idTokenExample struct {
	Provenance  string `json:"provenance"`
	Description string `json:"description"`
	At          string `json:"at"`
	Verifier    struct {
		Issuer   string `json:"issuer"`
		Audience string `json:"audience"`
	} `json:"verifier"`
	Key     map[string]any `json:"key"`
	IDToken string         `json:"id_token"`
	Header  map[string]any `json:"header"`
	Claims  map[string]any `json:"claims"`
	Reason  string         `json:"reason,omitempty"`
}

// idTokenExampleCase is one example file: the subject and the claims beside
// iss, sub, aud, iat and exp, which the built-in issuer always mints.
type idTokenExampleCase struct {
	file, description string
	sub               string
	claims            map[string]any
	reason            string
}

func idTokenExampleDir() string {
	return filepath.Join("..", "..", "spec", "session-token", "examples", "id-token")
}

const (
	exampleAgentID  = "0199f3a2-5b7c-7d10-8e2f-3a4b5c6d7e09"
	examplePersonID = "0199f3a2-5b7c-7d10-8e2f-3a4b5c6d7e01"
	stampRefusal    = "straza_jkt is not a non-empty string"
)

var exampleAgentStamp = dpop.Thumbprint(testProofKey(exampleMachineSeed).Public().(ed25519.PublicKey))

// agentClaims returns the claims of an agent's client_credentials ID token
// with straza_jkt set to stamp.
func agentClaims(stamp any) map[string]any {
	return map[string]any{"preferred_username": "build-bot", "email": "", "straza_jkt": stamp}
}

var idTokenExampleCases = []idTokenExampleCase{
	{file: "valid-agent-key-stamp.json", sub: exampleAgentID, claims: agentClaims(exampleAgentStamp),
		description: "An AI agent's ID token from the built-in issuer's client_credentials grant. straza_jkt is the RFC 7638 thumbprint of the Ed25519 key that verified the agent's client assertion. A server that checks in this token compares straza_jkt with the key registered for the agent now, and with the key of the check-in's DPoP proof when one comes with it."},
	{file: "valid-agent-no-stamp.json", sub: exampleAgentID, claims: map[string]any{"preferred_username": "build-bot", "email": ""},
		description: "An AI agent's ID token minted before straza_jkt existed. It verifies as before, and a server checks it in without comparing a key."},
	{file: "valid-person-sign-in.json", sub: examplePersonID,
		claims: map[string]any{"preferred_username": "kim", "email": "kim@straza.example",
			"nonce": exampleAgentStamp + ".q3xZr9bYw1Kc", "auth_time": exampleIDTokenIat.Add(-20 * time.Second).Unix()},
		description: "A person's ID token from a browser sign-in, with nonce and auth_time. The built-in issuer stamps no key on a person's token. The thumbprint in the nonce names the machine key the person enrolls, which is another rule."},
	{file: "invalid-stamp-number.json", sub: exampleAgentID, claims: agentClaims(7), reason: stampRefusal,
		description: "straza_jkt is a number. A verifier refuses the token instead of reading the claim as absent, so a malformed stamp never skips the key comparison."},
	{file: "invalid-stamp-empty.json", sub: exampleAgentID, claims: agentClaims(""), reason: stampRefusal,
		description: "straza_jkt is an empty string, which names no key. A verifier refuses the token instead of reading the claim as absent."},
	{file: "invalid-stamp-cnf-object.json", sub: exampleAgentID, claims: agentClaims(map[string]any{"jkt": exampleAgentStamp}), reason: stampRefusal,
		description: "straza_jkt has the shape of cnf, an object with jkt. The claim is the bare thumbprint string, so a verifier refuses the object."},
}

// buildIDTokenExample signs one example with the example session key and
// returns its file bytes.
func buildIDTokenExample(t *testing.T, c idTokenExampleCase) []byte {
	t.Helper()
	priv, err := importPrivate(exampleKeyRecord(exampleStrazaSeed))
	if err != nil {
		t.Fatal(err)
	}
	b := jwt.NewBuilder().Issuer(exampleGrantIssuer).Subject(c.sub).Audience([]string{"straza"}).
		IssuedAt(exampleIDTokenIat).Expiration(exampleIDTokenIat.Add(10 * time.Minute))
	for name, value := range c.claims {
		b = b.Claim(name, value)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), priv))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := importPublic(exampleKeyRecord(exampleStrazaSeed))
	if err != nil {
		t.Fatal(err)
	}
	ex := idTokenExample{Provenance: exampleIDTokenProvenance, Description: c.description,
		At: exampleIDTokenIat.Add(30 * time.Second).Format(time.RFC3339), Key: jsonMap(t, pub), IDToken: string(signed),
		Header: headerOf(t, string(signed)), Claims: payloadOf(t, string(signed)), Reason: c.reason}
	ex.Verifier.Issuer, ex.Verifier.Audience = exampleGrantIssuer, "straza"
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(ex); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestIDTokenExamples regenerates every spec/session-token/examples/id-token
// file and compares bytes, then replays each file through VerifyIDToken at
// the example's time against the key the file publishes. A valid-* example
// must verify with the straza_jkt it shows, and an invalid-* one must be
// refused for straza_jkt. Run it with STRAZA_UPDATE_SPEC_EXAMPLES=1 to write
// the files after an intentional change.
func TestIDTokenExamples(t *testing.T) {
	update := os.Getenv("STRAZA_UPDATE_SPEC_EXAMPLES") == "1"
	if update {
		if err := os.MkdirAll(idTokenExampleDir(), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	known := map[string]bool{}
	var valid, invalid int
	for _, c := range idTokenExampleCases {
		known[c.file] = true
		path := filepath.Join(idTokenExampleDir(), c.file)
		want := buildIDTokenExample(t, c)
		if update {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s is missing. Regenerate the examples with STRAZA_UPDATE_SPEC_EXAMPLES=1: %v", c.file, err)
		}
		if !bytes.Equal(raw, want) {
			t.Errorf("%s differs from what the generator signs today. Regenerate it on purpose and explain the change in CHANGELOG.md.", c.file)
		}
		if strings.HasPrefix(c.file, "valid-") {
			valid++
		} else {
			invalid++
		}
		t.Run(c.file, func(t *testing.T) { replayIDTokenExample(t, c.file, raw) })
	}
	files, err := filepath.Glob(filepath.Join(idTokenExampleDir(), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !known[filepath.Base(f)] {
			t.Errorf("no case generates %s. Add a case for it or remove the file.", filepath.Base(f))
		}
	}
	if valid < 3 || invalid < 3 {
		t.Fatalf("%d valid and %d invalid examples, the spec asks for at least 3 of each", valid, invalid)
	}
}

// replayIDTokenExample checks one example file as a third party would:
// its decoded header and claims match its token, and the verifier accepts
// or refuses it as its name says.
func replayIDTokenExample(t *testing.T, name string, raw []byte) {
	var ex idTokenExample
	if err := json.Unmarshal(raw, &ex); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(ex.Provenance, "doc ") && !strings.HasPrefix(ex.Provenance, "live ") {
		t.Errorf("provenance %q does not say doc or live", ex.Provenance)
	}
	if !reflect.DeepEqual(headerOf(t, ex.IDToken), ex.Header) || !reflect.DeepEqual(payloadOf(t, ex.IDToken), ex.Claims) {
		t.Errorf("the token decodes to another header or claim set than the example shows")
	}
	keyJSON, err := json.Marshal(ex.Key)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.ParseKey(keyJSON)
	if err != nil {
		t.Fatalf("parse the published key: %v", err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(key); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, ex.At)
	if err != nil {
		t.Fatal(err)
	}
	svc := &TokenService{issuer: ex.Verifier.Issuer, verify: set}
	got, err := svc.verifyIDTokenAt(ex.IDToken, ex.Verifier.Audience, at)
	switch {
	case strings.HasPrefix(name, "valid-") && err != nil:
		t.Fatalf("a valid example is refused: %v", err)
	case strings.HasPrefix(name, "valid-"):
		want, _ := ex.Claims["straza_jkt"].(string)
		if got.KeyThumbprint != want {
			t.Errorf("KeyThumbprint = %q, want %q", got.KeyThumbprint, want)
		}
	case ex.Reason == "":
		t.Fatal("an invalid example states no reason")
	case err == nil:
		t.Fatalf("an invalid example verifies: %+v", got)
	case !strings.Contains(err.Error(), "straza_jkt"):
		t.Errorf("refused for %v, but the example's reason is %q", err, ex.Reason)
	}
}
