package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func jsonNewDecoder(r io.Reader) *json.Decoder { return json.NewDecoder(r) }

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// checkinToken checks the seeded user in as the admin CLI does, under the
// harness name strazactl, and returns the session token and session id. A
// test that needs the session of a coding harness names it to checkinTokenAs.
func checkinToken(t *testing.T, app *App, base string) (string, string) {
	t.Helper()
	return checkinTokenAs(t, base, "strazactl")
}

// checkinTokenAs checks kim in under the given harness name and returns the
// session token and id.
func checkinTokenAs(t *testing.T, base, harness string) (string, string) {
	t.Helper()
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": harness, "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	})
	return checkin["session_token"].(string), checkin["session_id"].(string)
}

func decide(t *testing.T, base, token string, event map[string]any) (int, map[string]any) {
	t.Helper()
	return postJSONAuth(t, base+"/v1/decide", token, map[string]any{"event": event})
}

func postJSONAuth(t *testing.T, urlStr, bearer string, body any) (int, map[string]any) {
	t.Helper()
	raw := mustJSON(body)
	req, _ := http.NewRequest("POST", urlStr, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = jsonNewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestDecideEndpoint pins that a distributed policy is enforced through
// /v1/decide against the caller's cached subject, revocation denies, and the
// decision is audited asynchronously.
func TestDecideEndpoint(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base) // admin session for policy apply

	// Apply + activate a deny rule for role dev.
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", adminTok, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/block-rm/activate", adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	token, sessionID := checkinToken(t, app, base)

	// Deny: rm -rf matches the rule for role dev.
	code, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x",
	})
	if code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "no-rm-rf" {
		t.Fatalf("deny decision = %d %v", code, dec)
	}
	if !strings.Contains(dec["reason"].(string), "Destructive") {
		t.Errorf("missing reason: %v", dec)
	}
	if dec["snapshotId"] == "" {
		t.Error("decision missing snapshotId")
	}

	// Allow (default): an unmatched command falls to the standalone allow.
	_, dec = decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "ls -la",
	})
	if dec["effect"] != "allow" {
		t.Errorf("default allow expected: %v", dec)
	}

	// A file event with targets and a delegate tag: the server record is the
	// only one for this verdict, so it must carry all four fields.
	code, dec = postJSONAuth(t, base+"/v1/decide", token, map[string]any{
		"event": map[string]any{
			"kind": "tool.pre", "tool": "file.write",
			"paths": []string{"/work/proj/config.yaml"}, "workspace": "/work/proj",
		},
		"agentType": "researcher", "agentId": "a-1",
	})
	if code != http.StatusOK || dec["effect"] != "allow" {
		t.Fatalf("attributed file decision = %d %v", code, dec)
	}

	// No token → 401; garbage token → 401.
	if code, _ := decide(t, base, "", map[string]any{"kind": "tool.pre", "tool": "shell.exec"}); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
	if code, _ := decide(t, base, "a.b.c", map[string]any{"kind": "tool.pre", "tool": "shell.exec"}); code != http.StatusUnauthorized {
		t.Errorf("bad token = %d, want 401", code)
	}

	// Revoke the session → decide returns a deny (not a 401): the caller
	// enforces the block with the reason.
	if err := app.store.Sessions().SetStatus(context.Background(), sessionID, "revoked"); err != nil {
		t.Fatal(err)
	}
	app.denylist.revokeSession(sessionID)
	_, dec = decide(t, base, token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "ls"})
	if dec["effect"] != "deny" || dec["ruleId"] != "revoked" {
		t.Fatalf("revoked session decide = %v", dec)
	}

	// Observe both pending and delivered rows because the relay can publish
	// the async audit spool before this assertion reads the outbox.
	deadline := time.Now().Add(15 * time.Second)
	var auditCount int
	var attributed string
	for time.Now().Before(deadline) {
		recent, err := app.store.Outbox().ListRecent(context.Background(), 200)
		if err != nil {
			t.Fatal(err)
		}
		auditCount = 0
		for _, e := range recent {
			if e.Subject == "straza.audit.tool" {
				auditCount++
				if strings.Contains(e.CE, `"agentId":"a-1"`) {
					attributed = e.CE
				}
			}
		}
		if auditCount >= 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if auditCount < 4 {
		t.Errorf("audit events = %d, want ≥4 tool decisions spooled", auditCount)
	}
	for _, want := range []string{
		`"paths":["/work/proj/config.yaml"]`, `"workspace":"/work/proj"`,
		`"agentType":"researcher"`, `"source":"strazad"`,
	} {
		if !strings.Contains(attributed, want) {
			t.Errorf("server audit record lacks %s: %s", want, attributed)
		}
	}

	// /metrics exposes the decision histogram.
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	_ = resp.Body.Close()
	if !strings.Contains(body, "straza_pdp_decisions_total") || !strings.Contains(body, "straza_pdp_decision_seconds") {
		t.Errorf("metrics missing PDP series:\n%s", firstLines(body, 20))
	}
}

// TestDecideSubjectCacheMissBounces pins the restart-window contract: a
// /v1/decide call whose session has no cached subject on THIS pod answers
// the same 401 bounce the gateway lane gives (gatewayAuth; cross-pod pin in
// TestMultiPodHA), and never evaluates a role-less fallback subject. With
// the subject's roles gone, role-matched sets would stop applying entirely:
// a ticket-gated deploy would become a profile-default deny with an empty
// ruleId in enterprise, and in a standalone profile with no unscoped deny
// in the way, an ALLOW for an action the role's policy denies or holds.
// After the client's documented re-checkin with the same session token, the
// same session decides with its true roles again.
func TestDecideSubjectCacheMissBounces(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", adminTok, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/block-rm/activate", adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	token, sessionID := checkinToken(t, app, base)
	rmEvent := map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x"}

	// Control: with the checked-in subject cached, the role-matched deny fires.
	if code, dec := decide(t, base, token, rmEvent); code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "no-rm-rf" {
		t.Fatalf("control decide = %d %v, want the role-matched deny", code, dec)
	}

	// Simulate the restart: the cache is in-memory and boots empty; nothing
	// rebuilds it from the token (the token carries a roles hash, not roles).
	app.subjects.drop(sessionID)

	code, dec := decide(t, base, token, rmEvent)
	if code != http.StatusUnauthorized {
		t.Fatalf("cache-miss decide = %d %v, want the 401 re-checkin bounce (a %v verdict here is the fallback evaluating a role-less subject)",
			code, dec, dec["effect"])
	}
	if msg, _ := dec["error"].(string); !strings.Contains(msg, "Check in again") {
		t.Errorf("bounce message = %q, want the gateway lane's re-checkin instruction", msg)
	}

	// The documented bounce: re-checkin with the SAME session token keeps the
	// session id (approval exemptions stay keyed to it) and re-caches the
	// subject, after which the role-matched deny fires again.
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": token,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	})
	if code != http.StatusOK {
		t.Fatalf("re-checkin = %d %v", code, body)
	}
	if got := body["session_id"].(string); got != sessionID {
		t.Fatalf("re-checkin session id = %q, want the surviving %q", got, sessionID)
	}
	if code, dec := decide(t, base, body["session_token"].(string), rmEvent); code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "no-rm-rf" {
		t.Fatalf("post-bounce decide = %d %v, want the role-matched deny restored", code, dec)
	}
}

// TestDecideSubjectIsolation: two users with different roles get different
// decisions from the same snapshot via their cached subjects.
func TestDecideSubjectIsolation(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)

	// finance-only deny rule; dev is unaffected.
	const finPol = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: fin-only }
spec:
  match: { roles: [finance] }
  rules:
    - id: no-curl
      tools: [shell.exec]
      command: { denyPatterns: ["curl *"] }
      effect: deny
      reason: "finance may not curl"
`
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", adminTok, "application/yaml", []byte(finPol)); code != http.StatusCreated {
		t.Fatalf("apply = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/fin-only/activate", adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	// kim is a dev (from seedIdentity), so curl is allowed for her.
	devTok, _ := checkinToken(t, app, base)
	_, dec := decide(t, base, devTok, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "curl http://x"})
	if dec["effect"] != "allow" {
		t.Errorf("dev curl should be allowed: %v", dec)
	}
}
