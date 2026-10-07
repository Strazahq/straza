package approval

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

func genP256(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(der)
}

func signDecision(t *testing.T, priv *ecdsa.PrivateKey, requestID, verdict, challenge string, ts int64) string {
	t.Helper()
	msg := requestID + "\n" + verdict + "\n" + challenge + "\n" + strconv.FormatInt(ts, 10)
	d := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func (h *harness) enroll(t *testing.T, userID, spki string) string {
	t.Helper()
	ctx := context.Background()
	grant, err := h.svc.MintEnrollToken(ctx, userID, "")
	if err != nil {
		t.Fatalf("MintEnrollToken: %v", err)
	}
	res, err := h.svc.Enroll(ctx, EnrollInput{
		EnrollToken: grant.Token, Name: "phone", Platform: "android",
		KeyAlg: "ecdsa-p256", PublicKeyB64: spki, KeySecurityLevel: "strongbox",
		AttestationKind: "play-integrity",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if res.UserID != userID {
		t.Fatalf("enrolled user = %s, want %s", res.UserID, userID)
	}
	if res.Username == "" || res.Username != grant.Username {
		t.Fatalf("enrolled username = %q, want %q", res.Username, grant.Username)
	}
	return res.DeviceID
}

// TestEnrollChannelScope pins the channel-scoped token contract: a
// self-minted token is stamped with the
// channel it was authorized for and consume refuses a platform outside that
// channel; an admin-minted (empty-channel) token enrolls any platform; an
// unknown stamp fails closed. Every mismatch answers the one coarse
// ErrEnrollTokenInvalid (no oracle) and burns the token like any failed
// consume after the CAS.
func TestEnrollChannelScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := h.seedUser(t, "kim", "sec-approvers")

	cases := []struct {
		name     string
		channel  string
		platform string
		wantErr  bool
	}{
		{"mobile token, ios", "mobile", "ios", false},
		{"mobile token, android", "mobile", "android", false},
		{"mobile token, browser", "mobile", "browser", true},
		{"browser token, browser", "browser", "browser", false},
		{"browser token, ios", "browser", "ios", true},
		{"browser token, android", "browser", "android", true},
		{"admin token, ios", "", "ios", false},
		{"admin token, browser", "", "browser", false},
		{"mobile token, unknown platform", "mobile", "toaster", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spki := genP256(t)
			grant, err := h.svc.MintEnrollToken(ctx, u.ID, tc.channel)
			if err != nil {
				t.Fatalf("MintEnrollToken(%q): %v", tc.channel, err)
			}
			_, err = h.svc.Enroll(ctx, EnrollInput{
				EnrollToken: grant.Token, Name: "d", Platform: tc.platform,
				KeyAlg: "ecdsa-p256", PublicKeyB64: spki,
				KeySecurityLevel: "software",
				AttestationKind:  "none",
			})
			if tc.wantErr {
				if !errors.Is(err, ErrEnrollTokenInvalid) {
					t.Fatalf("Enroll = %v, want ErrEnrollTokenInvalid", err)
				}
			} else if err != nil {
				t.Fatalf("Enroll: %v", err)
			}
		})
	}

	// An unknown channel stamp on the row (never minted by this build) fails
	// closed even for a matching-looking platform.
	_, spki := genP256(t)
	raw := "weird-stamp-token"
	if _, err := h.st.Approvers().InsertEnrollToken(ctx, store.ApproverEnrollToken{
		TokenHash: sha256hex(raw), UserID: u.ID, Channel: "carrier-pigeon",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Enroll(ctx, EnrollInput{
		EnrollToken: raw, Name: "d", Platform: "ios",
		KeyAlg: "ecdsa-p256", PublicKeyB64: spki,
	}); !errors.Is(err, ErrEnrollTokenInvalid) {
		t.Fatalf("unknown channel stamp: Enroll = %v, want ErrEnrollTokenInvalid", err)
	}

	// A mismatch burns the token: the same mobile token refuses ios after a
	// browser-platform attempt spent it.
	grant, err := h.svc.MintEnrollToken(ctx, u.ID, "mobile")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Enroll(ctx, EnrollInput{
		EnrollToken: grant.Token, Name: "d", Platform: "browser",
		KeyAlg: "ecdsa-p256", PublicKeyB64: spki,
	}); !errors.Is(err, ErrEnrollTokenInvalid) {
		t.Fatalf("mismatch = %v, want ErrEnrollTokenInvalid", err)
	}
	if _, err := h.svc.Enroll(ctx, EnrollInput{
		EnrollToken: grant.Token, Name: "d", Platform: "ios",
		KeyAlg: "ecdsa-p256", PublicKeyB64: spki,
	}); !errors.Is(err, ErrEnrollTokenInvalid) {
		t.Fatalf("burned token re-use = %v, want ErrEnrollTokenInvalid", err)
	}
}

func (h *harness) seedNHI(t *testing.T, username string, roles ...string) store.User {
	t.Helper()
	u, err := h.st.Users().Create(context.Background(), store.User{
		Username: username, Email: username + "@x.io", Attrs: `{"kind":"nhi"}`,
	})
	if err != nil {
		t.Fatalf("Create nhi: %v", err)
	}
	rs := make([]store.Role, len(roles))
	for i, r := range roles {
		rs[i] = store.Role{ID: "role-" + r, Name: r}
	}
	h.resolver.roles[u.ID] = rs
	return u
}

// TestApproverDecideSignedEndToEnd drives the whole mobile loop: admin mints an
// enroll token, the hardware key enrolls, the approver fetches the decidable
// row (with a fresh challenge), signs, and the signed verdict resolves the
// record. Then a replay of the same signed request is rejected, and a tampered
// verdict fails signature verification.
func TestApproverDecideSignedEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)

	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, err := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	rows, err := h.svc.Decidable(ctx, approver.ID, deviceID)
	if err != nil {
		t.Fatalf("Decidable: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != rec.ID || rows[0].Challenge == "" {
		t.Fatalf("decidable = %+v", rows)
	}
	if rows[0].Summary.Tool != "mcp.call" || rows[0].Summary.App != "midpoint" || rows[0].Summary.ToolName != "disable_user" {
		t.Errorf("summary parse = %+v", rows[0].Summary)
	}
	if rows[0].RequesterKind != "human" {
		t.Errorf("requester kind = %s, want human", rows[0].RequesterKind)
	}
	challenge := rows[0].Challenge

	ts := time.Now().Unix()
	sig := signDecision(t, priv, rec.ID, "approve", challenge, ts)
	out, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "approve", Challenge: challenge, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	})
	if err != nil {
		t.Fatalf("DecideSigned: %v", err)
	}
	if out.State != StateApproved || out.DecidedByName != "kim" {
		t.Fatalf("decided = %+v", out)
	}

	// Replay of the exact signed request: challenge already consumed → 401 class.
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "approve", Challenge: challenge, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	}); err != ErrChallengeInvalid {
		t.Errorf("replay = %v, want ErrChallengeInvalid", err)
	}
}

// TestApproverSignatureBinding proves the signed message binds the verdict: a
// signature made for "approve" cannot be replayed as "deny" (and a fresh
// challenge for the same call still fails because the bytes differ).
func TestApproverSignatureBinding(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	rows, _ := h.svc.Decidable(ctx, approver.ID, deviceID)
	challenge := rows[0].Challenge

	ts := time.Now().Unix()
	sigApprove := signDecision(t, priv, rec.ID, "approve", challenge, ts)
	// Submit the approve signature but claim verdict "deny": the reconstructed
	// message differs, so verification fails.
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "deny", Challenge: challenge, SignatureB64: sigApprove, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	}); err != ErrBadSignature {
		t.Errorf("verdict-swapped signature = %v, want ErrBadSignature", err)
	}

	// A signature from a different key never verifies.
	other, _ := genP256(t)
	sigOther := signDecision(t, other, rec.ID, "approve", challenge, ts)
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "approve", Challenge: challenge, SignatureB64: sigOther, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	}); err != ErrBadSignature {
		t.Errorf("foreign-key signature = %v, want ErrBadSignature", err)
	}
}

// TestApproverTimestampWindow: a stale timestamp is refused before any DB work,
// even with an otherwise valid signature.
func TestApproverTimestampWindow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	rows, _ := h.svc.Decidable(ctx, approver.ID, deviceID)
	challenge := rows[0].Challenge

	staleTS := time.Now().Add(-10 * time.Minute).Unix()
	sig := signDecision(t, priv, rec.ID, "approve", challenge, staleTS)
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "approve", Challenge: challenge, SignatureB64: sig, TS: staleTS,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	}); err != ErrStaleTimestamp {
		t.Errorf("stale ts = %v, want ErrStaleTimestamp", err)
	}
}

// TestApproverEnrollKeyRequired: only ECDSA P-256 SPKI keys enroll. The curve
// OID in the key is authoritative, so a lying key_alg cannot smuggle a wrong
// key, and a valid P-256 key with a wrong key_alg still enrolls.
func TestApproverEnrollKeyRequired(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := h.seedUser(t, "kim", "sec-approvers")

	mint := func() string {
		g, err := h.svc.MintEnrollToken(ctx, u.ID, "")
		if err != nil {
			t.Fatalf("MintEnrollToken: %v", err)
		}
		return g.Token
	}

	// ed25519 SPKI (not ECDSA) → rejected.
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	edDER, _ := x509.MarshalPKIXPublicKey(edPub)
	if _, err := h.svc.Enroll(ctx, EnrollInput{EnrollToken: mint(), PublicKeyB64: base64.StdEncoding.EncodeToString(edDER)}); err != ErrBadKey {
		t.Errorf("ed25519 key = %v, want ErrBadKey", err)
	}

	// ECDSA on P-384 (wrong curve) → rejected.
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	if _, err := h.svc.Enroll(ctx, EnrollInput{EnrollToken: mint(), PublicKeyB64: base64.StdEncoding.EncodeToString(p384DER)}); err != ErrBadKey {
		t.Errorf("P-384 key = %v, want ErrBadKey", err)
	}

	// Garbage → rejected.
	if _, err := h.svc.Enroll(ctx, EnrollInput{EnrollToken: mint(), PublicKeyB64: "not base64 spki!!"}); err != ErrBadKey {
		t.Errorf("garbage key = %v, want ErrBadKey", err)
	}

	// Valid P-256 but a LYING key_alg: the curve wins, enrolment succeeds.
	_, spki := genP256(t)
	if _, err := h.svc.Enroll(ctx, EnrollInput{EnrollToken: mint(), KeyAlg: "ed25519", PublicKeyB64: spki}); err != nil {
		t.Errorf("valid P-256 with lying key_alg = %v, want success", err)
	}
}

// TestApproverEnrollTokenOneTime: an enroll token opens exactly one enrolment.
func TestApproverEnrollTokenOneTime(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := h.seedUser(t, "kim")
	_, spki := genP256(t)
	grant, err := h.svc.MintEnrollToken(ctx, u.ID, "")
	if err != nil {
		t.Fatalf("MintEnrollToken: %v", err)
	}
	if _, err := h.svc.Enroll(ctx, EnrollInput{EnrollToken: grant.Token, PublicKeyB64: spki}); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if _, err := h.svc.Enroll(ctx, EnrollInput{EnrollToken: grant.Token, PublicKeyB64: spki}); err != ErrEnrollTokenInvalid {
		t.Errorf("second enroll = %v, want ErrEnrollTokenInvalid", err)
	}
}

// TestApproverDeviceRevocation: deleting the device row makes signed decisions
// fail. Row-backed verification IS the revocation mechanism.
func TestApproverDeviceRevocation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))
	rows, _ := h.svc.Decidable(ctx, approver.ID, deviceID)
	challenge := rows[0].Challenge

	if err := h.st.Approvers().DeleteDevice(ctx, deviceID); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	ts := time.Now().Unix()
	sig := signDecision(t, priv, rec.ID, "approve", challenge, ts)
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "approve", Challenge: challenge, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: approver.ID,
	}); err != ErrDeviceRevoked {
		t.Errorf("revoked device decide = %v, want ErrDeviceRevoked", err)
	}
}

// TestNHIDeciderHardBlockedAllLanes pins that a kind=nhi
// identity holding a MISASSIGNED approver role is rejected on the console lane
// (Decide) AND the mobile API lane (DecideSigned), enforced in Decide so every
// channel inherits it.
func TestNHIDeciderHardBlockedAllLanes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	nhi := h.seedNHI(t, "robot", "sec-approvers") // role misassigned to an NHI
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))

	// Console/CLI lane.
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", nhi.ID, "console", "", ""); err != ErrNHIDecider {
		t.Errorf("console NHI decide = %v, want ErrNHIDecider", err)
	}

	// The NHI never even sees a decidable row.
	if rows, err := h.svc.Decidable(ctx, nhi.ID, "apd_none"); err != nil || len(rows) != 0 {
		t.Errorf("Decidable for NHI = %+v, %v (want empty)", rows, err)
	}

	// Mobile API lane: enroll a device for the NHI, mint a challenge by hand,
	// sign: the crypto gate passes but Decide still hard-blocks.
	priv, spki := genP256(t)
	deviceID := h.enroll(t, nhi.ID, spki)
	ch, err := h.svc.mintChallenge(ctx, deviceID, rec.ID)
	if err != nil {
		t.Fatalf("mintChallenge: %v", err)
	}
	ts := time.Now().Unix()
	sig := signDecision(t, priv, rec.ID, "approve", ch, ts)
	if _, err := h.svc.DecideSigned(ctx, SignedDecision{
		RequestID: rec.ID, Verdict: "approve", Challenge: ch, SignatureB64: sig, TS: ts,
		DeviceID: deviceID, DeciderUserID: nhi.ID,
	}); err != ErrNHIDecider {
		t.Errorf("mobile NHI decide = %v, want ErrNHIDecider", err)
	}
}

// TestApproverScopesAndHistory pins scope filtering: mine shows the requester's
// own record with no challenge; decidable applies four-eyes; history shows
// resolved records visible to the requester (own) and the approver (by role).
func TestApproverScopesAndHistory(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	requester := h.seedUser(t, "nova")
	approver := h.seedUser(t, "kim", "sec-approvers")
	stranger := h.seedUser(t, "mallory")
	_, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)
	spec := policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60}
	rec, _ := h.svc.Request(ctx, req("s-1", requester.ID, "nova", spec))

	// mine: requester sees their own, no challenge; approver's mine is empty.
	mine, _ := h.svc.Mine(ctx, requester.ID, 0)
	if len(mine) != 1 || mine[0].Challenge != "" || mine[0].State != "pending" {
		t.Fatalf("mine = %+v", mine)
	}
	if km, _ := h.svc.Mine(ctx, approver.ID, 0); len(km) != 0 {
		t.Errorf("approver mine = %+v, want empty", km)
	}

	// four-eyes: the requester is NOT decidable on their own record.
	if dec, _ := h.svc.Decidable(ctx, requester.ID, deviceID); len(dec) != 0 {
		t.Errorf("requester decidable (four-eyes) = %+v, want empty", dec)
	}

	// Resolve, then check history visibility.
	if _, err := h.svc.Decide(ctx, rec.ID, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if own, _, _ := h.svc.History(ctx, requester.ID, "", 0); len(own) != 1 || own[0].State != "approved" {
		t.Errorf("requester history = %+v", own)
	}
	if byRole, _, _ := h.svc.History(ctx, approver.ID, "", 0); len(byRole) != 1 {
		t.Errorf("approver history = %+v, want 1 (by role)", byRole)
	}
	if none, _, _ := h.svc.History(ctx, stranger.ID, "", 0); len(none) != 0 {
		t.Errorf("stranger history = %+v, want empty", none)
	}
}

// TestApproverPushRegistration: kinds are validated, dedupe is a no-op, delete
// is idempotent.
func TestApproverPushRegistration(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := h.seedUser(t, "kim")
	_, spki := genP256(t)
	deviceID := h.enroll(t, u.ID, spki)

	fcmTok := PushRegistration{Kind: "fcm", TokenOrEndpoint: "tok-1"}
	if err := h.svc.RegisterPush(ctx, deviceID, fcmTok); err != nil {
		t.Fatalf("RegisterPush: %v", err)
	}
	if err := h.svc.RegisterPush(ctx, deviceID, fcmTok); err != nil {
		t.Errorf("RegisterPush dedupe: %v", err)
	}
	if err := h.svc.RegisterPush(ctx, deviceID, PushRegistration{Kind: "carrier-pigeon", TokenOrEndpoint: "x"}); err != ErrBadPushKind {
		t.Errorf("bad kind = %v, want ErrBadPushKind", err)
	}
	if err := h.svc.DeletePush(ctx, deviceID, fcmTok); err != nil {
		t.Errorf("DeletePush: %v", err)
	}
	if err := h.svc.DeletePush(ctx, deviceID, fcmTok); err != nil {
		t.Errorf("idempotent DeletePush: %v", err)
	}
}
