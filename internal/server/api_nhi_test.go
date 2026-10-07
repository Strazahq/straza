package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/store"
)

// TestHeadlessNHILifecycle pins the headless agent lifecycle: an agent
// in CI/CD is governed like a user: provision an NHI, register its Ed25519
// key, mint a token via client_credentials with a signed assertion, check in
// WITHOUT a device row, decide; then prove the kill-switch and key-removal
// lanes refuse the next grant.
func TestHeadlessNHILifecycle(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)

	// 1. Provision the NHI through the admin API (standalone lane; the
	// enterprise lane provisions via agentic SCIM instead).
	var created struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users", adminTok,
		map[string]any{"username": "ci-bot", "kind": "nhi"}, &created); code != http.StatusCreated {
		t.Fatalf("create nhi = %d", code)
	}
	if created.Kind != "nhi" {
		t.Fatalf("created kind = %q, want nhi", created.Kind)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users", adminTok,
		map[string]any{"username": "bad-kind", "kind": "robot"}, nil); code != http.StatusBadRequest {
		t.Errorf("invalid kind accepted: %d", code)
	}

	// 2. Register the per-NHI public key. Humans must be refused: a stolen
	// key must never become a password-equivalent bypass for a human account.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok,
		map[string]any{"public_key": pubB64}, nil); code != http.StatusOK {
		t.Fatalf("register key = %d", code)
	}
	if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+admin.ID+"/nhi-key", adminTok,
		map[string]any{"public_key": pubB64}, nil); code != http.StatusBadRequest {
		t.Errorf("human key registration accepted: %d", code)
	}
	if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok,
		map[string]any{"public_key": "not-base64!"}, nil); code != http.StatusBadRequest {
		t.Errorf("garbage key accepted: %d", code)
	}

	// 3. Non-interactive token: client_credentials with a signed assertion.
	grant := func() (int, map[string]any) {
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
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		if err := jsonNewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("non-JSON token response: %v", err)
		}
		return resp.StatusCode, body
	}
	code, body := grant()
	if code != http.StatusOK {
		t.Fatalf("grant = %d %v", code, body)
	}
	idToken, _ := body["id_token"].(string)

	// 4. Deviceless checkin: the id_token lane with no device; the session
	// exists, is bound to the NHI, and carries no device id.
	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": idToken,
		"harness":  map[string]string{"name": "exec-wrapper", "version": "1.0"},
		"attestation": map[string]any{
			"managed": false, "hashes": map[string]string{},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("headless checkin = %d %v", code, checkin)
	}
	if checkin["user"] != "ci-bot" {
		t.Errorf("checkin user = %v, want ci-bot", checkin["user"])
	}
	ses, err := app.store.Sessions().GetByID(ctx, checkin["session_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if ses.DeviceID != "" {
		t.Errorf("NHI session has device id %q, want none (deviceless, D29)", ses.DeviceID)
	}

	// 5. The session token decides like any user's (governed like a user).
	sesTok, _ := checkin["session_token"].(string)
	dcode, dec := decide(t, base, sesTok, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x",
	})
	if dcode != http.StatusOK || dec["effect"] != "deny" {
		t.Errorf("NHI decide = %d %v, want the D28 starter deny", dcode, dec)
	}

	// 6. Kill switch: user disable refuses the NEXT grant outright.
	nhi, err := app.store.Users().GetByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	nhi.Status = store.UserDisabled
	if _, err := app.store.Users().Update(ctx, nhi); err != nil {
		t.Fatal(err)
	}
	if code, body := grant(); code != http.StatusBadRequest || body["error"] != "invalid_client" {
		t.Errorf("grant for disabled NHI = %d %v, want invalid_client", code, body)
	}

	// 7. Key removal is the durable credential revocation.
	nhi.Status = store.UserActive
	if _, err := app.store.Users().Update(ctx, nhi); err != nil {
		t.Fatal(err)
	}
	if code, _ := grant(); code != http.StatusOK {
		t.Fatalf("re-enabled NHI grant should work before key removal (%d)", code)
	}
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+created.ID+"/nhi-key", adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("key delete = %d", code)
	}
	if code, body := grant(); code != http.StatusBadRequest || body["error"] != "invalid_client" {
		t.Errorf("grant after key removal = %d %v, want invalid_client", code, body)
	}
}

// TestNHIKeyRead pins the key read surface: posture only, never the key.
func TestNHIKeyRead(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	tok, _ := checkinToken(t, app, base)

	var created struct {
		ID string `json:"id"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/users", tok,
		map[string]any{"username": "builder", "kind": "nhi"}, &created); code != http.StatusCreated {
		t.Fatalf("create nhi = %d", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/nope/nhi-key", tok, nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown user = %d, want 404", code)
	}
	var posture struct {
		Registered  bool   `json:"registered"`
		Fingerprint string `json:"fingerprint"`
		PublicKey   string `json:"publicKey"` // must never appear
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+created.ID+"/nhi-key", tok, nil, &posture); code != http.StatusOK || posture.Registered {
		t.Fatalf("pre-set read = %d registered=%v, want 200 unregistered", code, posture.Registered)
	}
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+created.ID+"/nhi-key", tok,
		map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, nil); code != http.StatusOK {
		t.Fatalf("set = %d", code)
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+created.ID+"/nhi-key", tok, nil, &posture); code != http.StatusOK {
		t.Fatalf("post-set read = %d", code)
	}
	if !posture.Registered || !strings.HasPrefix(posture.Fingerprint, "sha256:") {
		t.Errorf("posture = %+v, want registered with a sha256 fingerprint", posture)
	}
	if posture.PublicKey != "" {
		t.Errorf("read leaked the stored key")
	}
}
