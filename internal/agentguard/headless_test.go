package agentguard_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// adminJSON performs one authenticated admin call for the headless demo.
func adminJSON(t *testing.T, method, url, bearer string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestHeadlessNHIDemo is the headless enrollment demo, end to end from the
// CLI surfaces: an agent in CI/CD (no browser, no device) is governed exactly
// like a user. keygen → admin registers the public key → enroll --headless →
// hooks decide (the starter policy denies rm -rf, benign allowed), all
// through a deviceless NHI session.
func TestHeadlessNHIDemo(t *testing.T) {
	base, adminPW := bootStrazad(t)

	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	env := []string{"STRAZA_HOME=" + home, "CLAUDECODE=1"}

	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	// 1. keygen: the private half never leaves the agent home.
	var keygenOut bytes.Buffer
	if err := agentguard.Keygen(store, "ci-bot", false, &keygenOut); err != nil {
		t.Fatal(err)
	}
	if err := agentguard.Keygen(store, "ci-bot", false, &keygenOut); err == nil {
		t.Error("second keygen without --force should refuse")
	}
	key, err := store.LoadNHIKey()
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(key.Seed)
	if err != nil {
		t.Fatal(err)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if !strings.Contains(keygenOut.String(), pubB64) {
		t.Error("keygen must print the public key for registration")
	}

	// 2. The administrator provisions the NHI and registers the key.
	adminTok := deviceLogin(t, base, "strazactl", "kim", adminPW)
	code, created := adminJSON(t, http.MethodPost, base+"/v1/admin/users", adminTok,
		map[string]any{"username": "ci-bot", "kind": "nhi"})
	if code != http.StatusCreated {
		t.Fatalf("create nhi = %d %v", code, created)
	}
	nhiID, _ := created["id"].(string)
	if code, body := adminJSON(t, http.MethodPut, base+"/v1/admin/users/"+nhiID+"/nhi-key", adminTok,
		map[string]any{"public_key": pubB64}); code != http.StatusOK {
		t.Fatalf("register key = %d %v", code, body)
	}

	// 3. Headless enroll: non-interactive, verifies the credential works.
	var enrollOut bytes.Buffer
	if err := agentguard.EnrollHeadless(context.Background(), store, base, "ci-bot", &enrollOut); err != nil {
		t.Fatalf("EnrollHeadless: %v\n%s", err, enrollOut.String())
	}
	if !strings.Contains(enrollOut.String(), "ci-bot") {
		t.Errorf("enroll output should name the NHI:\n%s", enrollOut.String())
	}

	// 4. Hooks decide for the NHI exactly as for a human: session starts
	// (deviceless checkin under the hood), the starter policy denies rm -rf
	// with a reason, benign commands pass.
	if _, code := hook(t, "claude-code",
		`{"hook_event_name":"SessionStart","session_id":"h1","cwd":"/work"}`, env); code != 0 {
		t.Fatalf("headless session.start blocked: %d", code)
	}
	rmOut, rmCode := hook(t, "claude-code",
		`{"hook_event_name":"PreToolUse","session_id":"h1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}`, env)
	if rmCode != 2 {
		t.Fatalf("rm -rf not blocked for the NHI (exit %d): %s", rmCode, rmOut)
	}
	if _, lsCode := hook(t, "claude-code",
		`{"hook_event_name":"PreToolUse","session_id":"h1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"ls -la"}}`, env); lsCode != 0 {
		t.Fatalf("benign command blocked for the NHI (exit %d)", lsCode)
	}

	// 5. The client session state names the NHI (deviceless server-side, as
	// pinned in the server suite's TestHeadlessNHILifecycle).
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if ses.User != "ci-bot" {
		t.Errorf("session user = %q, want ci-bot", ses.User)
	}
}
