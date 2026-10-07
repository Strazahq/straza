package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/snapshot"
	"github.com/strazahq/straza/internal/store"
)

// snapshotGet fetches GET /v1/snapshot with an optional bearer and
// If-None-Match, returning the status, the headers, the body and the
// error sentence of a JSON refusal.
func snapshotGet(t *testing.T, base, bearer, ifNoneMatch string) (int, http.Header, []byte, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/v1/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var refusal struct {
		Error string `json:"error"`
	}
	if resp.StatusCode >= 400 {
		_ = json.Unmarshal(body, &refusal)
	}
	return resp.StatusCode, resp.Header, body, refusal.Error
}

// enterpriseProfile switches a test app to the enterprise profile.
func enterpriseProfile(c *config.Config) { c.Profile = config.ProfileEnterprise }

// mintSnapshotSession mints a session token the way check-in does, for the
// named user and session.
func mintSnapshotSession(t *testing.T, app *App, user, session string) string {
	t.Helper()
	tok, _, err := app.tokens.Mint(authn.Claims{Subject: user, Session: session, Device: "dev-1"})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// signedSessionToken signs a session token with the app's own active session
// key and the given expiry, the way Mint does but with an expiry Mint never
// sets, so a test can present a token that is expired and otherwise valid.
func signedSessionToken(t *testing.T, app *App, issuer, session string, exp time.Time) string {
	t.Helper()
	keys, err := app.store.SigningKeys().ListByPurpose(context.Background(), store.KeyPurposeSession)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.Status != store.KeyActive {
			continue
		}
		key, err := jwk.Import(ed25519.NewKeyFromSeed(k.PrivateKey))
		if err != nil {
			t.Fatal(err)
		}
		if err := key.Set(jwk.KeyIDKey, k.KID); err != nil {
			t.Fatal(err)
		}
		tok, err := jwt.NewBuilder().Issuer(issuer).Subject("user-1").JwtID("jti-"+session).
			IssuedAt(exp.Add(-time.Hour)).Expiration(exp).Claim("ses", session).Claim("dev", "dev-1").Build()
		if err != nil {
			t.Fatal(err)
		}
		signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
		if err != nil {
			t.Fatal(err)
		}
		return string(signed)
	}
	t.Fatal("the app has no active session key")
	return ""
}

// TestSnapshotRouteSessionGate pins who may read the compiled policy. Under
// the enterprise profile only a checked-in session that is not revoked gets
// the snapshot, and the check runs before the 304 and 503 answers, so an
// anonymous caller learns neither the active id nor whether a snapshot
// exists. The standalone profile answers every caller as before.
func TestSnapshotRouteSessionGate(t *testing.T) {
	t.Parallel()
	ent, entBase := testApp(t, enterpriseProfile)
	std, stdBase := testApp(t)
	// empty boots enterprise with no snapshot loaded, so its 503 is reachable.
	empty, emptyBase := testAppPreRun(t, []func(*App){func(a *App) {
		a.snapshots = snapshot.New(a.store, nil, nil, config.EffectAllow, 0, nil)
	}}, enterpriseProfile)

	live := mintSnapshotSession(t, ent, "user-1", "ses-live")
	revokedSession := mintSnapshotSession(t, ent, "user-1", "ses-revoked")
	revokedUser := mintSnapshotSession(t, ent, "user-gone", "ses-of-gone")
	ent.denylist.revokeSession("ses-revoked")
	ent.denylist.revokeUser("user-gone")
	emptyLive := mintSnapshotSession(t, empty, "user-1", "ses-live")

	deviceTok, err := ent.tokens.MintDeviceToken("user-1", "dev-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	idTok, err := ent.tokens.MintIDToken("user-1", "straza", time.Hour, "kim", "kim@x.io")
	if err != nil {
		t.Fatal(err)
	}
	expiredIDTok, err := ent.tokens.MintIDToken("user-1", "straza", -31*time.Second, "kim", "kim@x.io")
	if err != nil {
		t.Fatal(err)
	}
	approverTok, err := ent.tokens.MintApproverToken("user-1", "apd-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// A session token signed with this server's keys under another issuer
	// stands for a token this server did not issue.
	foreign, err := authn.NewTokenService(context.Background(), ent.store.SigningKeys(), "https://other.example", 0)
	if err != nil {
		t.Fatal(err)
	}
	foreignTok, _, err := foreign.Mint(authn.Claims{Subject: "user-1", Session: "ses-foreign"})
	if err != nil {
		t.Fatal(err)
	}
	// The hand-signed pair differs only in expiry: the live one is the
	// positive control that the signing matches this server's, so the
	// expired one is refused for its expiry alone.
	signedLive := signedSessionToken(t, ent, entBase, "ses-signed", time.Now().Add(time.Hour))
	expiredSession := signedSessionToken(t, ent, entBase, "ses-expired", time.Now().Add(-time.Hour))

	entETag := `"` + ent.snapshots.Current().ID + `"`
	stdETag := `"` + std.snapshots.Current().ID + `"`

	cases := []struct {
		name        string
		base        string
		bearer      string
		ifNoneMatch string
		wantStatus  int
		wantError   string // the exact refusal sentence, checked when set
	}{
		{"enterprise, no token", entBase, "", "", http.StatusUnauthorized, snapshotNoSessionMsg},
		{"enterprise, no token, matching If-None-Match", entBase, "", entETag, http.StatusUnauthorized, snapshotNoSessionMsg},
		{"enterprise, garbage bearer", entBase, "not-a-token", "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, admin API token", entBase, apiTokenPrefix + "0123456789abcdef", "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, device token", entBase, deviceTok, "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, ID token", entBase, idTok, "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, expired ID token", entBase, expiredIDTok, "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, approver token", entBase, approverTok, "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, session token from another issuer", entBase, foreignTok, "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, session token that expired an hour ago", entBase, expiredSession, "", http.StatusUnauthorized, snapshotBadSessionMsg},
		{"enterprise, hand-signed live session token", entBase, signedLive, "", http.StatusOK, ""},
		{"enterprise, revoked session", entBase, revokedSession, "", http.StatusUnauthorized, revokedSessionMsg},
		{"enterprise, revoked user", entBase, revokedUser, "", http.StatusUnauthorized, revokedIdentityMsg},
		{"enterprise, live session", entBase, live, "", http.StatusOK, ""},
		{"enterprise, live session, matching If-None-Match", entBase, live, entETag, http.StatusNotModified, ""},
		{"enterprise without a snapshot, no token", emptyBase, "", "", http.StatusUnauthorized, snapshotNoSessionMsg},
		{"enterprise without a snapshot, live session", emptyBase, emptyLive, "", http.StatusServiceUnavailable, "no active snapshot"},
		{"standalone, no token", stdBase, "", "", http.StatusOK, ""},
		{"standalone, garbage bearer", stdBase, "not-a-token", "", http.StatusOK, ""},
		{"standalone, no token, matching If-None-Match", stdBase, "", stdETag, http.StatusNotModified, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, hdr, body, refusal := snapshotGet(t, tc.base, tc.bearer, tc.ifNoneMatch)
			if status != tc.wantStatus {
				t.Fatalf("status = %d (%q), want %d", status, refusal, tc.wantStatus)
			}
			if tc.wantError != "" && refusal != tc.wantError {
				t.Errorf("refusal = %q, want %q", refusal, tc.wantError)
			}
			switch status {
			case http.StatusOK:
				cur := ent.snapshots.Current()
				if tc.base == stdBase {
					cur = std.snapshots.Current()
				}
				if hdr.Get("ETag") != `"`+cur.ID+`"` || hdr.Get("X-Straza-Snapshot-Id") != cur.ID || !bytes.Equal(body, cur.Signed) {
					t.Errorf("200 served ETag %q, id %q and %d bytes, want the active snapshot %s", hdr.Get("ETag"), hdr.Get("X-Straza-Snapshot-Id"), len(body), cur.ID)
				}
			case http.StatusUnauthorized, http.StatusNotModified:
				if hdr.Get("ETag") != "" || hdr.Get("X-Straza-Snapshot-Id") != "" || bytes.Contains(body, []byte(ent.snapshots.Current().ID)) {
					t.Errorf("a %d answer names the snapshot: ETag %q, id %q, body %s", status, hdr.Get("ETag"), hdr.Get("X-Straza-Snapshot-Id"), body)
				}
			}
		})
	}
}

// TestSnapshotRouteSessionGateReadsNoStore pins the no-database-read rule on
// the gate:
// the enterprise check verifies the token and reads the denylist in memory,
// so neither a served nor a refused fetch touches a control-plane repo.
func TestSnapshotRouteSessionGateReadsNoStore(t *testing.T) {
	t.Parallel()
	app, base, cs := testAppCounting(t, enterpriseProfile)
	live := mintSnapshotSession(t, app, "user-1", "ses-live")

	// The boot owners (the manager's first load and the session janitor's
	// first pass) touch the store right after Run starts, so the window
	// opens once every counter has held still for a quarter second.
	before := cs.snapshot()
	for deadline := time.Now().Add(10 * time.Second); ; {
		time.Sleep(250 * time.Millisecond)
		now := cs.snapshot()
		if maps.Equal(before, now) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the store never went quiet after boot")
		}
		before = now
	}
	if status, _, _, refusal := snapshotGet(t, base, live, ""); status != http.StatusOK {
		t.Fatalf("live session = %d (%q), want 200", status, refusal)
	}
	if status, _, _, _ := snapshotGet(t, base, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", status)
	}
	if status, _, _, _ := snapshotGet(t, base, "not-a-token", ""); status != http.StatusUnauthorized {
		t.Fatalf("garbage bearer = %d, want 401", status)
	}
	assertNoRepoAccess(t, cs, "enterprise snapshot fetch", before)
}
