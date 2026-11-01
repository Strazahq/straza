package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// gitUpstream is the MCP server behind the git grant tests: repo_list
// lists its repos, git_fetch takes repo, and git_push takes repo and branch
// and refuses any other member, so a push whose arguments carry refs would
// fail its schema check.
type gitUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	repos [][2]string // value, id
	lists int
}

// listings answers how many times repo_list ran.
func (u *gitUpstream) listings() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lists
}

// add lists a new repo at the next listing.
func (u *gitUpstream) add(value, id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.repos = append(u.repos, [2]string{value, id})
}

func startGitUpstream(t *testing.T) *gitUpstream {
	t.Helper()
	u := &gitUpstream{repos: [][2]string{{"Strazahq/pay-api", "101"}, {"Strazahq/pay-web", "102"}, {"Strazahq/site", "103"}}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "ghapp", Version: "1.0.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "repo_list", InputSchema: map[string]any{"type": "object"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        mcp.Meta{"ai.straza/lists-objects": map[string]any{"type": "repo"}}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.lists++
			items := []any{}
			for _, r := range u.repos {
				items = append(items, map[string]any{"value": r[0], "id": r[1]})
			}
			return &mcp.CallToolResult{StructuredContent: map[string]any{"objects": items}}, nil
		})
	done := func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done " + req.Params.Name}}}, nil
	}
	str := map[string]any{"type": "string"}
	srv.AddTool(&mcp.Tool{Name: "git_fetch", InputSchema: map[string]any{"type": "object",
		"properties": map[string]any{"repo": str}, "required": []any{"repo"}}}, done)
	srv.AddTool(&mcp.Tool{Name: "git_push", InputSchema: map[string]any{"type": "object",
		"properties": map[string]any{"repo": str, "branch": str}, "required": []any{"repo"}, "additionalProperties": false}}, done)
	u.Server = httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(u.Close)
	return u
}

// gitManifest is ghapp at url with a git block for https://github.com and
// both git tools pinned to repo, with limits.rps when rps is set.
func gitManifest(url string, rps float64) string {
	limits := ""
	if rps > 0 {
		limits = fmt.Sprintf("  limits: {rps: %v}\n", rps)
	}
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: ghapp}
server: {name: straza.test/ghapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
%s  objects:
    type: repo
    list: {tool: repo_list}
    tools:
      git_fetch: {argument: repo}
      git_push: {argument: repo}
  git:
    url: https://ghapp.example.com/
    upstream: https://github.com
    ops: {fetch: git_fetch, push: git_push}
`, url, limits)
}

// grantPolicies gate the rig's roles: git-hold and git-quick hold a push
// for an approver of git-approvers for 60 and 1 seconds, git-ticket opens a
// ticket for a push, and git-deny may not fetch.
var grantPolicies = map[string]string{
	"git-hold": `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: git-hold}
spec:
  match: {roles: [git-hold]}
  rules:
    - id: hold-git-push
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {allow: ["git_push"]}
      effect: allow
      mode: approve
      reason: a person approves every git push
      approve: {roles: [git-approvers], timeoutSeconds: 60}
`,
	"git-quick": `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: git-quick}
spec:
  match: {roles: [git-quick]}
  rules:
    - id: hold-git-push-quick
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {allow: ["git_push"]}
      effect: allow
      mode: approve
      approve: {roles: [git-approvers], timeoutSeconds: 1}
`,
	"git-ticket": `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: git-ticket}
spec:
  match: {roles: [git-ticket]}
  rules:
    - id: ticket-git-push
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {allow: ["git_push"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [git-approvers], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`,
	"git-deny": `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: git-deny}
spec:
  match: {roles: [git-deny]}
  rules:
    - id: deny-git-fetch
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {deny: ["git_fetch"]}
      effect: deny
      reason: "Straza: git fetch is off for this role"
`,
}

// grantRigOpts shapes one rig: filter turns the catalog's policy filter on,
// rps caps ghapp's calls, and pre runs on the App before it runs.
type grantRigOpts struct {
	filter bool
	rps    float64
	pre    func(*App)
}

// grantRig is a booted strazad serving ghapp with a git block, the key
// every session of the rig is bound to, and per user the subject and
// session facts its check-in cached.
type grantRig struct {
	app      *App
	base     string
	up       *gitUpstream
	clock    *ghClock
	cs       *countingStore
	appID    string
	key      ed25519.PrivateKey
	users    map[string]store.User
	checked  map[string]cachedSubject
	approver store.User
}

// grantSessionT is one session of the rig: its claims, a token bound to the
// rig's key, and how many fences records has sent for it.
type grantSessionT struct {
	user   string
	claims authn.Claims
	token  string
	fences int
}

// grantRoles are the rig's roles on ghapp, with the tools each binds and
// whether it covers every repo or Strazahq/pay-api only. nob holds a role
// with no access row on ghapp.
var grantRoles = []struct {
	name, tools string
	every       bool
}{
	{"git-all", `["*"]`, true},
	{"git-pay", `["*"]`, false},
	{"git-fetch", `["git_fetch"]`, true},
	{"git-hold", `["*"]`, true},
	{"git-quick", `["*"]`, true},
	{"git-ticket", `["*"]`, true},
	{"git-deny", `["*"]`, true},
}

// grantUsers maps each person of the rig to its one role.
var grantUsers = map[string]string{"eve": "git-all", "pam": "git-pay", "fen": "git-fetch", "hol": "git-hold",
	"qui": "git-quick", "tic": "git-ticket", "den": "git-deny", "nob": "other"}

func newGrantRig(t *testing.T, o grantRigOpts) *grantRig {
	t.Helper()
	clock := &ghClock{}
	app, base, cs := bootGrantApp(t, clock, o)
	ctx := context.Background()
	r := &grantRig{app: app, base: base, up: startGitUpstream(t), clock: clock, cs: cs, key: newProofKey(t),
		users: map[string]store.User{}, checked: map[string]cachedSubject{}}
	mf, err := managerParse(t, gitManifest(r.up.URL, o.rps))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	r.appID = row.ID
	if _, err := app.broker.Set(ctx, row.ID, "", "gh-app-tok"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.broker.SetListing(ctx, row.ID, "gh-listing-tok"); err != nil {
		t.Fatal(err)
	}
	for _, role := range grantRoles {
		ro, err := app.store.Roles().Create(ctx, store.Role{Name: role.name, Kind: store.RoleKindApplication})
		if err != nil {
			t.Fatal(err)
		}
		scope := &store.ObjectScope{Every: true}
		if !role.every {
			scope = &store.ObjectScope{Objects: []store.CoveredObject{{ID: "101", Value: "Strazahq/pay-api"}}}
		}
		if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: row.ID, ToolMatcher: role.tools, Scope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	app.manager.SecretUpdated(ctx, row.ID)
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	for user, role := range grantUsers {
		r.users[user] = seedGatewayUser(t, app, user, role)
	}
	r.approver = seedApprover(t, app, "secops", "git-approvers")
	for name, src := range grantPolicies {
		catCreatePolicy(t, app, name, src)
	}
	catRecompile(t, app)
	catWaitRunning(t, app, "ghapp")
	if err := app.objects.MarkDue(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ghapp's object list", func() bool { l := app.objects.Get(row.ID); return l != nil && len(l.Objects) == 3 })
	for user := range grantUsers {
		tok := sessionToken(t, base, user)
		c, err := app.tokens.Verify(tok)
		if err != nil {
			t.Fatal(err)
		}
		e, ok := app.subjects.lookup(c.Session)
		if !ok {
			t.Fatalf("%s's check-in cached no subject", user)
		}
		r.checked[user] = e
	}
	return r
}

// bootGrantApp boots strazad over an instrumented store as bootGHApp does,
// with the object loop on clock, and with the policy filter and o.pre
// applied before it runs.
func bootGrantApp(t *testing.T, clock *ghClock, o grantRigOpts) (*App, string, *countingStore) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone, DataDir: dir,
		Server: config.Server{Listen: "127.0.0.1:0"},
		Log:    config.Log{Level: "error", Format: "json"},
		Store:  config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events: config.Events{Embedded: true},
		Governance: config.Governance{OfflineGraceTTL: 15 * time.Minute, LocalToolDefault: config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop},
		Apps: config.Apps{PollInterval: time.Second, HealthInterval: time.Hour, AllowLoopbackUpstreams: true,
			Catalog: config.Catalog{WarnSize: 100, PolicyFilter: o.filter}, Objects: config.AppsObjects{Enabled: true}},
	}
	seedStoreTemplate(t, cfg)
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cs := &countingStore{inner: st}
	ctx, cancel := context.WithCancel(context.Background())
	app, err := build(ctx, cfg, logging.New(cfg.Log, io.Discard), cs)
	if err != nil {
		cancel()
		t.Fatalf("build: %v", err)
	}
	app.objects.SetPace(time.Hour, clock.now)
	app.gateway.notify.SetDelay(0)
	base := "http://" + app.Addr()
	app.cfg.Server.PublicURL = base
	if app.tokens, err = authn.NewTokenService(ctx, app.store.SigningKeys(), base, 0); err != nil {
		cancel()
		t.Fatal(err)
	}
	app.http.Handler = app.routes()
	if o.pre != nil {
		o.pre(app)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Log("app did not stop in time")
		}
	})
	return app, base, cs
}

// session answers a fresh session of user, cached with the subject and the
// facts the user's check-in cached, so each case has its own records, rate
// bucket and approval key, and a token bound to the rig's key.
func (r *grantRig) session(t *testing.T, user string) *grantSessionT {
	t.Helper()
	e := r.checked[user]
	return r.sessionAs(t, r.users[user].ID, e.sub, e.facts)
}

// sessionAs answers a fresh session of the user userID with the subject sub
// and the facts facts in the subject cache.
func (r *grantRig) sessionAs(t *testing.T, userID string, sub policy.Subject, facts sessionFacts) *grantSessionT {
	t.Helper()
	id := uuid.NewString()
	if _, _, ok := r.app.subjects.putIf(id, cachedSubject{sub: sub, facts: facts}, func() bool { return true }); !ok {
		t.Fatal("the subject cache refused the session")
	}
	c := authn.Claims{Subject: userID, Session: id, Harness: "claude-code/2.1.0", Attestation: "none"}
	return &grantSessionT{user: sub.User, claims: c, token: bindToken(t, r.app, c, r.key)}
}

// post sends body to path as s, with a fresh DPoP proof, and answers the
// status and the decoded JSON body.
func (r *grantRig) post(t *testing.T, s *grantSessionT, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.base+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", dpop.Scheme+" "+s.token)
	p, err := dpop.NewProof(r.key, http.MethodPost, r.base+path, r.app.proofs.Nonce(time.Now()), s.token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(dpop.HeaderProof, p)
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// grant asks for a git grant of op on upstream as s, with refs and wait
// when set.
func (r *grantRig) grant(t *testing.T, s *grantSessionT, upstream, op string, refs []authn.GrantRef, wait *int) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"upstream": upstream, "op": op}
	if refs != nil {
		body["refs"] = refs
	}
	if wait != nil {
		body["waitSeconds"] = *wait
	}
	return r.post(t, s, "/v1/grants", string(mustJSON(body)))
}

// callTool posts a tools/call of name with args on /mcp as s and answers the
// JSON-RPC answer.
func (r *grantRig) callTool(t *testing.T, s *grantSessionT, name, args string) map[string]any {
	t.Helper()
	_, out := r.post(t, s, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":`+jsonString(name)+`,"arguments":`+args+`}}`)
	return out
}

// mcpRecord is one straza.audit.mcp record: its CloudEvents id and data.
type mcpRecord struct {
	id   string
	data map[string]any
}

// records answers the straza.audit.mcp records of s's session, oldest
// first, other than tools.list, after a fence: a tools/list of the session
// whose record the queue writes after every record the session wrote
// before it.
func (r *grantRig) records(t *testing.T, s *grantSessionT) []mcpRecord {
	t.Helper()
	_, _ = r.post(t, s, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	s.fences++
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, err := r.app.store.Outbox().ListAfter(context.Background(), "", []string{"straza.audit.mcp"}, 100000)
		if err != nil {
			t.Fatal(err)
		}
		var out []mcpRecord
		lists := 0
		for _, row := range rows {
			var ce struct {
				ID   string         `json:"id"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(row.CE), &ce); err != nil {
				t.Fatal(err)
			}
			switch {
			case ce.Data["session"] != s.claims.Session:
			case ce.Data["event"] == "tools.list":
				lists++
			default:
				out = append(out, mcpRecord{id: ce.ID, data: ce.Data})
			}
		}
		if lists >= s.fences {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fence record of session %s never reached the outbox", s.claims.Session)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
