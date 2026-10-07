package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/store"
)

// selfMintResponse is the self-service mint envelope: the admin envelope's
// shape (token + structured qr + compact qr_payload) plus the bound user.
type selfMintResponse struct {
	EnrollToken string `json:"enroll_token"`
	ExpiresIn   int    `json:"expires_in"`
	QRPayload   string `json:"qr_payload"`
	QR          struct {
		V     int    `json:"v"`
		Token string `json:"token"`
	} `json:"qr"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// TestSelfEnrollToken drives the self-service approver enroll-token mint
// (POST /v1/approvals/self/enroll-token): a plain logged-in user (no admin
// role) holding straza-enroll-browser mints a one-time enroll token bound to
// THEMSELF, the token really enrolls a device, a body naming someone else
// cannot redirect the mint, an unauthenticated call is refused, and rapid
// re-mints hit the per-user cooldown.
func TestSelfEnrollToken(t *testing.T) {
	// Serial: it lowers the package knob selfEnrollCooldown for its run.
	orig := selfEnrollCooldown
	selfEnrollCooldown = 200 * time.Millisecond
	t.Cleanup(func() { selfEnrollCooldown = orig })

	app, base := testApp(t)
	kim := seedIdentity(t, app) // kim holds role dev; NEVER granted admin here
	grantRole(t, app, kim.ID, "straza-enroll-browser")
	idTok := loginDeviceFlow(t, base, "kim", "hunter2!")

	// 1. Happy path: channel named, mints for the caller.
	var mint selfMintResponse
	if code := adminReq(t, "POST", base+"/v1/approvals/self/enroll-token", idTok,
		map[string]any{"channel": "browser"}, &mint); code != 200 {
		t.Fatalf("self mint = %d", code)
	}
	if mint.EnrollToken == "" || mint.QR.Token != mint.EnrollToken || mint.QR.V != 1 {
		t.Fatalf("self mint response = %+v", mint)
	}
	if mint.User.ID != kim.ID {
		t.Fatalf("self mint bound to %q, want caller %q", mint.User.ID, kim.ID)
	}

	// 2. The token is a real enroll token: a browser-shaped device enrolls.
	_, pubB64 := genApproverKey(t)
	var enrolled struct {
		DeviceID    string `json:"approver_device_id"`
		DeviceToken string `json:"device_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "",
		map[string]any{
			"enroll_token": mint.EnrollToken,
			"device": map[string]any{
				"name": "Edge on work laptop", "platform": "browser",
				"key_alg": "ES256", "public_key": pubB64,
				"key_security_level": "software",
				"attestation":        map[string]any{"kind": "none", "blob": ""},
			},
		}, &enrolled); code != 201 {
		t.Fatalf("enroll with self-minted token = %d", code)
	}
	if enrolled.DeviceID == "" || enrolled.DeviceToken == "" {
		t.Fatalf("enroll response = %+v", enrolled)
	}

	// 3. Self-only by construction: a body naming another user is ignored,
	// the mint still binds to the caller. (Waits out the cooldown first.)
	time.Sleep(selfEnrollCooldown + 50*time.Millisecond)
	other, err := app.store.Users().Create(context.Background(),
		store.User{Username: "mallory", Email: "mallory@x.io"})
	if err != nil {
		t.Fatal(err)
	}
	var redirected selfMintResponse
	if code := adminReq(t, "POST", base+"/v1/approvals/self/enroll-token", idTok,
		map[string]any{"channel": "browser", "user_id": other.ID, "username": other.Username},
		&redirected); code != 200 {
		t.Fatalf("self mint with foreign body = %d", code)
	}
	if redirected.User.ID != kim.ID {
		t.Fatalf("foreign body redirected the mint to %q", redirected.User.ID)
	}

	// 4. No bearer: refused.
	if code := adminReq(t, "POST", base+"/v1/approvals/self/enroll-token", "",
		map[string]any{"channel": "browser"}, nil); code != 401 {
		t.Fatalf("unauthenticated self mint = %d, want 401", code)
	}

	// 5. Cooldown: an immediate re-mint answers 429.
	if code := adminReq(t, "POST", base+"/v1/approvals/self/enroll-token", idTok,
		map[string]any{"channel": "browser"}, nil); code != 429 {
		t.Fatalf("rapid re-mint = %d, want 429", code)
	}
}

// TestSelfEnrollGate pins the self-enrollment role gate: the self mint refuses
// a caller without the channel's straza-enroll-* role (straza-admin passes),
// refuses NHIs outright, validates the channel enum, never burns the cooldown
// on a refusal, and the boot-ensured reserved roles exist with kind straza.
func TestSelfEnrollGate(t *testing.T) {
	// Serial: it lowers the package knob selfEnrollCooldown for its run.
	orig := selfEnrollCooldown
	selfEnrollCooldown = 200 * time.Millisecond
	t.Cleanup(func() { selfEnrollCooldown = orig })

	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	mintURL := base + "/v1/approvals/self/enroll-token"
	var refusal struct {
		Error string `json:"error"`
	}

	// Boot ensured the reserved roles, wire kind straza, nobody assigned.
	var roles []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/roles", adminTok, nil, &roles); code != 200 {
		t.Fatalf("roles list = %d", code)
	}
	found := map[string]string{}
	for _, r := range roles {
		found[r.Name] = r.Kind
	}
	for _, name := range []string{"straza-enroll-mobile", "straza-enroll-browser"} {
		if kind, ok := found[name]; !ok || kind != "straza" {
			t.Fatalf("bootstrap role %s: present=%v kind=%q, want kind straza", name, ok, kind)
		}
	}

	// straza-admin passes the gate on both channels.
	adminID := loginDeviceFlow(t, base, "kim", "hunter2!")
	if code := adminReq(t, "POST", mintURL, adminID, map[string]any{"channel": "browser"}, nil); code != 200 {
		t.Fatalf("admin browser mint = %d", code)
	}
	time.Sleep(selfEnrollCooldown + 50*time.Millisecond)
	if code := adminReq(t, "POST", mintURL, adminID, map[string]any{"channel": "mobile"}, nil); code != 200 {
		t.Fatalf("admin mobile mint = %d", code)
	}

	// Channel enum: missing and unknown values answer 400.
	for _, body := range []map[string]any{{}, {"channel": "phone"}} {
		if code := adminReq(t, "POST", mintURL, adminID, body, &refusal); code != 400 {
			t.Fatalf("mint %v = %d, want 400", body, code)
		}
		if refusal.Error != "channel must be mobile or browser" {
			t.Fatalf("channel 400 body = %q", refusal.Error)
		}
	}

	// A role-less human is refused with the actionable cause, and the refusal
	// does NOT burn the cooldown: granting the role makes the IMMEDIATE next
	// mint succeed.
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	frank, err := app.store.Users().Create(ctx, store.User{
		Username: "frank", Email: "frank@x.io", PasswordHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	frankTok := loginDeviceFlow(t, base, "frank", "hunter2!")
	if code := adminReq(t, "POST", mintURL, frankTok, map[string]any{"channel": "browser"}, &refusal); code != 403 {
		t.Fatalf("role-less mint = %d, want 403", code)
	}
	if want := "requires role straza-enroll-browser or straza-admin; enrollment roles are assigned in your identity manager"; refusal.Error != want {
		t.Fatalf("role refusal body = %q, want %q", refusal.Error, want)
	}
	grantRole(t, app, frank.ID, "straza-enroll-browser")
	if code := adminReq(t, "POST", mintURL, frankTok, map[string]any{"channel": "browser"}, nil); code != 200 {
		t.Fatalf("post-grant immediate mint = %d, want 200 (403 must not burn the cooldown)", code)
	}
	// The SUCCESS did record: an immediate re-mint answers 429.
	if code := adminReq(t, "POST", mintURL, frankTok, map[string]any{"channel": "browser"}, nil); code != 429 {
		t.Fatalf("re-mint after success = %d, want 429", code)
	}

	// Holding the browser role does not open the mobile channel; the refusal
	// names the role that WOULD.
	time.Sleep(selfEnrollCooldown + 50*time.Millisecond)
	if code := adminReq(t, "POST", mintURL, frankTok, map[string]any{"channel": "mobile"}, &refusal); code != 403 {
		t.Fatalf("wrong-channel mint = %d, want 403", code)
	}
	if want := "requires role straza-enroll-mobile or straza-admin; enrollment roles are assigned in your identity manager"; refusal.Error != want {
		t.Fatalf("wrong-channel refusal body = %q, want %q", refusal.Error, want)
	}

	// Channel scope holds at consume: a mobile token refuses a browser-shaped
	// enroll (and is burned by the attempt, same coarse 401).
	grantRole(t, app, frank.ID, "straza-enroll-mobile")
	time.Sleep(selfEnrollCooldown + 50*time.Millisecond)
	var mobileMint selfMintResponse
	if code := adminReq(t, "POST", mintURL, frankTok, map[string]any{"channel": "mobile"}, &mobileMint); code != 200 {
		t.Fatalf("mobile mint = %d", code)
	}
	_, pubB64 := genApproverKey(t)
	enrollBody := func(platform string) map[string]any {
		return map[string]any{
			"enroll_token": mobileMint.EnrollToken,
			"device": map[string]any{
				"name": "d", "platform": platform,
				"key_alg": "ES256", "public_key": pubB64,
				"key_security_level": "software",
				"attestation":        map[string]any{"kind": "none", "blob": ""},
			},
		}
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", enrollBody("browser"), nil); code != 401 {
		t.Fatalf("mobile token + browser platform = %d, want 401", code)
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", enrollBody("ios"), nil); code != 401 {
		t.Fatalf("burned mobile token + ios = %d, want 401 (mismatch burns)", code)
	}

	// An NHI session is refused outright, role or no role.
	var created struct {
		ID string `json:"id"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/users", adminTok,
		map[string]any{"username": "ci-bot", "kind": "nhi"}, &created); code != http.StatusCreated {
		t.Fatalf("create nhi = %d", code)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "PUT", base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok,
		map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, nil); code != http.StatusOK {
		t.Fatalf("register nhi key = %d", code)
	}
	assertion, err := clientassertion.Mint(priv, "ci-bot", base, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.PostForm(base+"/oidc/token", url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {"ci-bot"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	})
	if err != nil {
		t.Fatal(err)
	}
	var grantBody map[string]any
	if err := jsonNewDecoder(resp.Body).Decode(&grantBody); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	idToken, _ := grantBody["id_token"].(string)
	if idToken == "" {
		t.Fatalf("nhi grant = %v", grantBody)
	}
	// self-service is a person's client, so the NHI cannot even open a
	// session under it; its login token is refused on the mint as on every
	// route that decides.
	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "self-service", "version": "1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusForbidden {
		t.Fatalf("nhi self-service checkin = %d %v, want 403", code, checkin)
	}
	if code := adminReq(t, "POST", mintURL, idToken, map[string]any{"channel": "browser"}, &refusal); code != 403 {
		t.Fatalf("nhi mint = %d, want 403", code)
	}
	if want := fmt.Sprintf(nonPersonAdminRefusal, "ci-bot", "an agent"); refusal.Error != want {
		t.Fatalf("nhi refusal body = %q, want %q", refusal.Error, want)
	}
}

// TestSelfEnrollmentEligibility drives the eligibility half of GET /v1/self
// (0.83.0), the read the
// approvals page consults before drawing enroll offers: it answers the
// caller's eligible channels from the SAME helper the mint gate uses, so
// the two can never disagree. Empty channels is a normal answer (role-less
// humans and NHIs), never a refusal; only a missing bearer is a 401. The
// agreement pin at the end proves read and mint move together: a channel is
// in the read's answer exactly when the mint answers 200.
func TestSelfEnrollmentEligibility(t *testing.T) {
	// Serial: it lowers the package knob selfEnrollCooldown for its run.
	orig := selfEnrollCooldown
	selfEnrollCooldown = 200 * time.Millisecond
	t.Cleanup(func() { selfEnrollCooldown = orig })

	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)
	readURL := base + "/v1/self"
	mintURL := base + "/v1/approvals/self/enroll-token"

	// No bearer: refused like every identified surface.
	if code := adminReq(t, "GET", readURL, "", nil, nil); code != 401 {
		t.Fatalf("unauthenticated eligibility read = %d, want 401", code)
	}

	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	mkHuman := func(name string, roles ...string) string {
		t.Helper()
		u, err := app.store.Users().Create(ctx, store.User{
			Username: name, Email: name + "@x.io", PasswordHash: hash,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range roles {
			grantRole(t, app, u.ID, r)
		}
		return loginDeviceFlow(t, base, name, "hunter2!")
	}

	cases := []struct {
		name  string
		tok   string
		wantC []string
	}{
		{"role-less human", mkHuman("nora"), []string{}},
		{"mobile only", mkHuman("mia", "straza-enroll-mobile"), []string{"mobile"}},
		{"browser only", mkHuman("bea", "straza-enroll-browser"), []string{"browser"}},
		{"both roles", mkHuman("bo", "straza-enroll-mobile", "straza-enroll-browser"), []string{"browser", "mobile"}},
		{"straza-admin implies both", loginDeviceFlow(t, base, "kim", "hunter2!"), []string{"browser", "mobile"}},
	}

	// An NHI session answers empty channels on the read (the mint keeps its
	// 403; the read is a description, not a gate). Reuses the gate test's
	// client-credentials lane.
	var created struct {
		ID string `json:"id"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/users", adminTok,
		map[string]any{"username": "read-bot", "kind": "nhi"}, &created); code != http.StatusCreated {
		t.Fatalf("create nhi = %d", code)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "PUT", base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok,
		map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, nil); code != http.StatusOK {
		t.Fatalf("register nhi key = %d", code)
	}
	assertion, err := clientassertion.Mint(priv, "read-bot", base, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.PostForm(base+"/oidc/token", url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {"read-bot"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	})
	if err != nil {
		t.Fatal(err)
	}
	var grantBody map[string]any
	if err := jsonNewDecoder(resp.Body).Decode(&grantBody); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	idToken, _ := grantBody["id_token"].(string)
	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "exec-wrapper", "version": "1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusOK {
		t.Fatalf("nhi checkin = %d %v", code, checkin)
	}
	nhiTok, _ := checkin["session_token"].(string)
	cases = append(cases, struct {
		name  string
		tok   string
		wantC []string
	}{"nhi agent", nhiTok, []string{}})

	for _, tc := range cases {
		var got struct {
			Channels []string `json:"enroll_channels"`
		}
		got.Channels = nil
		if code := adminReq(t, "GET", readURL, tc.tok, nil, &got); code != 200 {
			t.Fatalf("%s: eligibility read = %d, want 200", tc.name, code)
		}
		if got.Channels == nil {
			t.Fatalf("%s: channels missing or null, want an array (empty is [])", tc.name)
		}
		if len(got.Channels) != len(tc.wantC) {
			t.Fatalf("%s: channels = %v, want %v", tc.name, got.Channels, tc.wantC)
		}
		for i := range tc.wantC {
			if got.Channels[i] != tc.wantC[i] {
				t.Fatalf("%s: channels = %v, want %v (sorted)", tc.name, got.Channels, tc.wantC)
			}
		}

		// Agreement pin: the mint answers 200 exactly when the read lists the
		// channel. NHI minting is a 403 with empty channels, which the pin
		// covers as "not listed, not 200".
		for _, ch := range []string{"browser", "mobile"} {
			listed := false
			for _, c := range got.Channels {
				if c == ch {
					listed = true
				}
			}
			code := adminReq(t, "POST", mintURL, tc.tok, map[string]any{"channel": ch}, nil)
			if listed && code != 200 {
				t.Fatalf("%s: read lists %q but mint = %d", tc.name, ch, code)
			}
			if !listed && code == 200 {
				t.Fatalf("%s: read omits %q but mint = 200", tc.name, ch)
			}
			if listed {
				time.Sleep(selfEnrollCooldown + 50*time.Millisecond)
			}
		}
	}
}
