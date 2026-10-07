package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestMultiPodHA exercises the stateless-data-plane invariant instead
// of assuming it: two strazad pods share one Postgres and one NATS.
// A session checked in on pod A works through pod B (after the documented
// re-checkin bounce; sticky routing is an optimization, not a
// requirement); a policy activated via B is enforced by A; a binding
// deleted via B empties A's catalogs; a session revoked via B dies on A.
// A server installed, moved, disabled, enabled and removed via A converges
// on B, and a call through B reaches it as A left it. A draft published
// through A reaches B from its one publish event. The convergence path is
// the spine (spine.ConvergeConsumer + straza.apps.*), so this is
// Postgres-gated and CI-run.
func TestMultiPodHA(t *testing.T) {
	t.Parallel()
	dsn := freshPostgresDSN(t) // skips without STRAZA_TEST_POSTGRES_DSN

	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	t.Cleanup(ns.Shutdown)
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}

	// Pods share the store, the spine, AND the KEK: credential rows are
	// sealed with it, and a pod with its own key cannot decrypt them (its
	// credentialed apps fail closed and never reach running). The Helm chart
	// mounts one Secret for exactly this reason.
	kek := filepath.Join(t.TempDir(), "secret.key")
	shared := func(cfg *config.Config) {
		cfg.Store = config.Store{Driver: config.DriverPostgres, DSN: dsn}
		cfg.Events = config.Events{Embedded: false, URL: ns.ClientURL()}
		cfg.Apps.HealthInterval = time.Hour
		cfg.Secrets.KEKFile = kek
	}

	// Production pods share ONE public URL (the LB) and therefore one token
	// issuer; testApp gives each instance its own port-derived issuer, which
	// would make pods reject each other's tokens for a reason that cannot
	// exist in a real deployment. The rebind is a preRun hook: it must land
	// BEFORE the pod's Run goroutine exists, because Run's announce line and
	// every handler read cfg/tokens/Handler (post-Run surgery is a data race).
	const issuer = "http://straza-ha.local"
	rebind := func(pod *App) {
		tokens, err := authn.NewTokenService(context.Background(), pod.store.SigningKeys(), issuer, 0)
		if err != nil {
			t.Fatal(err)
		}
		pod.tokens = tokens
		pod.cfg.Server.PublicURL = issuer
		pod.http.Handler = pod.routes()
	}

	// --- Pod A: first boot migrates + bootstraps; seed identity and the app.
	appA, baseA := testAppPreRun(t, []func(*App){rebind}, shared)
	admin := seedIdentity(t, appA) // kim, roles dev+reader
	grantAdmin(t, appA, admin.ID)
	gh := startGithubMock(t)
	deployGithubApp(t, appA, gh.URL, "ghp_HA-never-client-side")
	// kim's team role for the server of step 3, since dev holds its one
	// access row already, on github. It composes tagged-users, the role
	// that server owns, which goes with the server and comes back with it.
	team, err := appA.store.Roles().Create(context.Background(), store.Role{Name: "tagged-team", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appA.store.Roles().Assign(context.Background(), store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: admin.ID, RoleID: team.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// --- Pod B: same store, same spine. Boot AFTER the seed so its boot-time
	// loads see the app, bindings, and the active snapshot. A server changed
	// later reaches B through its converge consumer (step 3).
	appB, baseB := testAppPreRun(t, []func(*App){rebind}, shared)

	// Session established on A.
	tokA := sessionToken(t, baseA, "kim")

	// --- 1. Any pod serves any session. A token minted by A verifies on B
	// (shared signing keys); B has no cached subject for it, so the first
	// call answers 401 and the client re-checks-in (ON B) with the same
	// session token, exactly what straza/strazactl do on any 401.
	code, _, _ := mcpCall(t, baseB, tokA, "tools/list", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("cross-pod first call = %d, want the documented 401 re-checkin bounce", code)
	}
	code, body := postJSON(t, baseB+"/v1/checkin", map[string]any{
		"session_token": tokA,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
	if code != http.StatusOK {
		t.Fatalf("cross-pod re-checkin = %d %v", code, body)
	}
	tokB := body["session_token"].(string)

	// B runs its own instance of the app, started from the store at boot, so
	// its catalog fills as soon as its own upstream probe lands. Poll until
	// both pods serve the identical catalog.
	waitForHA(t, "identical catalogs on both pods", func() bool {
		_, listA, _ := mcpCall(t, baseA, tokA, "tools/list", nil)
		_, listB, _ := mcpCall(t, baseB, tokB, "tools/list", nil)
		namesA, namesB := toolNamesOf(t, listA), toolNamesOf(t, listB)
		return len(namesA) > 0 && fmt.Sprint(namesA) == fmt.Sprint(namesB)
	})

	// Admin bearer, resolved on B (roles from the shared store).
	adminBearer := loginDeviceFlow(t, baseA, "kim", "hunter2!")

	// --- 2. Policy activated via B is enforced by A (converge: snapshot
	// reload on straza.policy.updated).
	if code := rawYAMLReq(t, "PUT", baseB+"/v1/admin/policies", adminBearer, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: ha-deny}
spec:
  priority: 900
  match: {roles: [dev]}
  rules:
    - id: block-issues
      tools: [mcp.call]
      apps: [github]
      toolNames: {deny: ["get_*"]}
      effect: deny
      reason: "HA test: issues are off-limits"
`); code != http.StatusCreated {
		t.Fatalf("policy apply via B = %d", code)
	}
	if code := adminReq(t, "POST", baseB+"/v1/admin/policies/ha-deny/activate", adminBearer, nil, nil); code != http.StatusOK {
		t.Fatalf("policy activate via B = %d", code)
	}
	waitForHA(t, "policy activated via B enforced by A", func() bool {
		_, _, raw := mcpCall(t, baseA, tokA, "tools/call", map[string]any{
			"name": "github__get_issue", "arguments": map[string]int{"number": 1},
		})
		return strings.Contains(string(raw), "off-limits")
	})

	// --- 3. A server's whole life through A converges on B, and a call
	// through B reaches the server as A left it (converge: straza.apps.* →
	// the ordered config apply of drafts_apply.go).
	one, two := startTaggedUpstream(t, "from-one"), startTaggedUpstream(t, "from-two")
	install := func(url string) func() int {
		return func() int {
			return rawReq(t, "POST", baseA+"/v1/admin/apps", adminBearer, "application/yaml", taggedManifest(url), nil)
		}
	}
	verb := func(method, path string) func() int {
		return func() int { return adminReq(t, method, baseA+"/v1/admin/apps/tagged"+path, adminBearer, nil, nil) }
	}
	lifecycle := []struct {
		what string
		call func() int
		code int
		tag  string // what a call through B answers, empty when B runs no instance
	}{
		{what: "installed", call: install(one.URL), code: http.StatusCreated, tag: "from-one"},
		{what: "moved to another address", call: install(two.URL), code: http.StatusCreated, tag: "from-two"},
		{what: "disabled", call: verb("POST", "/disable"), code: http.StatusOK},
		{what: "enabled", call: verb("POST", "/enable"), code: http.StatusOK, tag: "from-two"},
		{what: "removed", call: verb("DELETE", ""), code: http.StatusOK},
	}
	for i, step := range lifecycle {
		if code := step.call(); code != step.code {
			t.Fatalf("server %s via A = %d, want %d", step.what, code, step.code)
		}
		if i == 0 {
			var users rolePayload
			if code := adminReq(t, "POST", baseA+"/v1/admin/roles", adminBearer,
				map[string]any{"name": "tagged-users", "server": "tagged", "tools": []string{"whoami"}}, &users); code != http.StatusCreated {
				t.Fatalf("the server's role with its access row via A = %d", code)
			}
			if code := adminReq(t, "POST", baseA+"/v1/admin/roles/"+team.ID+"/implications", adminBearer,
				map[string]any{"implies_role_id": users.ID}, nil); code != http.StatusCreated {
				t.Fatalf("the team composes the server's role via A = %d", code)
			}
		}
		waitForHA(t, "server "+step.what+" via A converged on B", func() bool {
			_, live := appB.manager.View("tagged")
			_, _, raw := mcpCall(t, baseB, tokB, "tools/call", map[string]any{"name": "tagged__whoami", "arguments": map[string]any{}})
			if step.tag == "" {
				return !live && !strings.Contains(string(raw), "from-")
			}
			return live && strings.Contains(string(raw), step.tag)
		})
	}

	// --- 3b. Drafts published through A reach B. The first installs the
	// server again, and B may follow on its deploy event as well. The second
	// makes the server's role again with its access row, composed into kim's
	// team, and starts nothing, so B learns of it from its one publish event
	// alone, and a call through B then answers from the address the drafts
	// name.
	publishViaA := func(documents ...string) pubAnswer {
		t.Helper()
		var created struct {
			Draft struct {
				ID       string `json:"id"`
				Revision int    `json:"revision"`
			} `json:"draft"`
			Verdict pubVerdict `json:"verdict"`
		}
		if code := adminReq(t, "POST", baseA+"/v1/admin/drafts", adminBearer, map[string]any{"documents": documents}, &created); code != http.StatusCreated {
			t.Fatalf("draft via A = %d", code)
		}
		var published pubAnswer
		if code := adminReq(t, "POST", baseA+"/v1/admin/drafts/"+created.Draft.ID+"/publish", adminBearer,
			acksFor(created.Draft.Revision, created.Verdict), &published); code != http.StatusOK {
			t.Fatalf("publish via A = %d %+v", code, published)
		}
		return published
	}
	if p := publishViaA(string(taggedManifest(one.URL))); len(p.Servers) != 1 || p.Servers[0].Change != "created" {
		t.Fatalf("publish of the server via A answered %+v", p.Servers)
	}
	waitForHA(t, "server published via A runs on B", func() bool {
		_, live := appB.manager.View("tagged")
		return live
	})
	publishViaA("apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: tagged-users\nspec:\n    kind: application\n    server: tagged\n"+
		"    bindings:\n        - app: tagged\n          tools:\n            - whoami\n",
		"apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: tagged-team\nspec:\n    kind: business\n    implies:\n        - tagged-users\n")
	waitForHA(t, "access row published via A applied on B", func() bool {
		_, _, raw := mcpCall(t, baseB, tokB, "tools/call", map[string]any{"name": "tagged__whoami", "arguments": map[string]any{}})
		return strings.Contains(string(raw), "from-one")
	})

	// --- 4. Binding deleted via B empties A's catalog (converge:
	// straza.apps.updated → bindings reload).
	var bindings []map[string]any
	if code := adminReq(t, "GET", baseB+"/v1/admin/bindings", adminBearer, nil, &bindings); code != http.StatusOK || len(bindings) == 0 {
		t.Fatalf("bindings via B = %d %v", code, bindings)
	}
	for _, b := range bindings {
		if code := adminReq(t, "DELETE", baseB+"/v1/admin/bindings/"+b["id"].(string), adminBearer, nil, nil); code != http.StatusOK {
			t.Fatalf("binding delete via B = %d", code)
		}
	}
	waitForHA(t, "binding deleted via B empties A's catalog", func() bool {
		_, listA, _ := mcpCall(t, baseA, tokA, "tools/list", nil)
		return len(toolNamesOf(t, listA)) == 0
	})

	// --- 5. The kill switch crosses pods: revoke the A-side session via B.
	var sessions []map[string]any
	if code := adminReq(t, "GET", baseB+"/v1/admin/sessions", adminBearer, nil, &sessions); code != http.StatusOK {
		t.Fatalf("sessions via B = %d", code)
	}
	revoked := 0
	for _, s := range sessions {
		if s["status"] == "active" && strings.HasPrefix(fmt.Sprint(s["harness"]), "claude-code") {
			if code := adminReq(t, "POST", baseB+"/v1/admin/sessions/"+s["id"].(string)+"/revoke", adminBearer, nil, nil); code != http.StatusOK {
				t.Fatalf("revoke via B = %d", code)
			}
			revoked++
		}
	}
	if revoked == 0 {
		t.Fatal("no active session found to revoke")
	}
	waitForHA(t, "session revoked via B refused by A", func() bool {
		code, _, _ := mcpCall(t, baseA, tokA, "tools/list", nil)
		return code == http.StatusForbidden || code == http.StatusUnauthorized
	})

	_ = appB // both pods stay up for the whole scenario
}

// rawYAMLReq sends a YAML body (the policy apply contract) with a bearer.
func rawYAMLReq(t *testing.T, method, urlStr, bearer, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, urlStr, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/yaml")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// startTaggedUpstream serves an MCP server whose one tool, whoami, answers
// tag, so a call shows which address a pod reached.
func startTaggedUpstream(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "tagged", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "names the upstream"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: tag}}}, nil, nil
		})
	up := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(up.Close)
	return up
}

// taggedManifest is the uncredentialed remote server named tagged at url.
func taggedManifest(url string) []byte {
	return []byte(fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: tagged}
server: {name: straza.test/tagged, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
`, url))
}

// waitForHA polls cond with a generous CI ceiling; the interesting number
// (how fast pods converge) lands in the log line.
func waitForHA(t *testing.T, what string, cond func() bool) {
	t.Helper()
	start := time.Now()
	deadline := start.Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			t.Logf("%s in %s", what, time.Since(start).Round(time.Millisecond))
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out (15s): %s", what)
}
