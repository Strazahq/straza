package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/store"
)

// ghCall is one tools/call the ghapp upstream received: the tool, the raw
// arguments bytes, the _meta and the Authorization header.
type ghCall struct {
	tool string
	args string
	meta map[string]any
	auth string
}

// ghUpstream is a repo server named ghapp in the gate tests. pr_create
// names its repo in repo, pr_merge's own mark names repository while the
// pin says repo, repo_create carries no mark, and repo_admin, whoami and
// db_query are pinned all, none and credential. repo_list lists repos,
// which a test may rename, and a test may serve a tool again with another
// input schema.
type ghUpstream struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []ghCall
	repos   [][2]string // value, id
	srv     *mcp.Server
	handler mcp.ToolHandler
}

// reschema serves tool again with schema and its own mark kept, as a
// release of the server that changes the tool's arguments does.
func (u *ghUpstream) reschema(tool string, schema map[string]any) {
	u.srv.AddTool(&mcp.Tool{Name: tool, Meta: mcp.Meta{"ai.straza/object": map[string]any{"type": "repo", "argument": "repo"}},
		InputSchema: schema}, u.handler)
}

// toolCalls answers the calls the upstream got of tool, the listing aside.
func (u *ghUpstream) toolCalls(tool string) []ghCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []ghCall
	for _, c := range u.calls {
		if c.tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// rename gives the repo with id a new value at the next listing.
func (u *ghUpstream) rename(id, value string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := range u.repos {
		if u.repos[i][1] == id {
			u.repos[i][0] = value
		}
	}
}

// add lists a new repo at the next listing.
func (u *ghUpstream) add(value, id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.repos = append(u.repos, [2]string{value, id})
}

func startGHUpstream(t *testing.T) *ghUpstream {
	t.Helper()
	u := &ghUpstream{repos: [][2]string{{"Strazahq/pay-api", "101"}, {"Strazahq/pay-web", "102"}, {"Strazahq/site", "103"}}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "ghapp", Version: "1.0.0"}, nil)
	mark := func(m map[string]any) mcp.Meta { return mcp.Meta{"ai.straza/object": m} }
	repoSchema := func(arg string) map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{arg: map[string]any{"type": "string"}}, "required": []any{arg}}
	}
	record := func(req *mcp.CallToolRequest) *mcp.CallToolResult {
		u.mu.Lock()
		u.calls = append(u.calls, ghCall{tool: req.Params.Name, args: string(req.Params.Arguments), meta: req.Params.Meta,
			auth: req.Extra.Header.Get("Authorization")})
		u.mu.Unlock()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done " + req.Params.Name}}}
	}
	srv.AddTool(&mcp.Tool{Name: "repo_list", InputSchema: map[string]any{"type": "object"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        mcp.Meta{"ai.straza/lists-objects": map[string]any{"type": "repo"}}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.calls = append(u.calls, ghCall{tool: "repo_list"})
			items := []any{}
			for _, r := range u.repos {
				items = append(items, map[string]any{"value": r[0], "id": r[1]})
			}
			return &mcp.CallToolResult{StructuredContent: map[string]any{"objects": items}}, nil
		})
	handler := func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return record(req), nil
	}
	srv.AddTool(&mcp.Tool{Name: "pr_create", Meta: mark(map[string]any{"type": "repo", "argument": "repo"}), InputSchema: repoSchema("repo")}, handler)
	srv.AddTool(&mcp.Tool{Name: "pr_merge", Meta: mark(map[string]any{"type": "repo", "argument": "repository"}), InputSchema: repoSchema("repo")}, handler)
	srv.AddTool(&mcp.Tool{Name: "repo_create", InputSchema: map[string]any{"type": "object"}}, handler)
	srv.AddTool(&mcp.Tool{Name: "repo_admin", InputSchema: map[string]any{"type": "object"}}, handler)
	srv.AddTool(&mcp.Tool{Name: "whoami", Meta: mark(map[string]any{"type": "repo", "reach": "none"}), InputSchema: map[string]any{"type": "object"}}, handler)
	srv.AddTool(&mcp.Tool{Name: "db_query", InputSchema: map[string]any{"type": "object"}}, handler)
	u.srv, u.handler = srv, handler
	u.Server = httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(u.Close)
	return u
}

// ghManifest pins repo on ghapp at url.
func ghManifest(url string) string {
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
  objects:
    type: repo
    list: {tool: repo_list}
    tools:
      pr_create: {argument: repo}
      pr_merge: {argument: repo}
      repo_admin: all
      whoami: none
      db_query: credential
`, url)
}

// ghRig is a booted app serving ghapp, its upstream, the object loop's
// clock and one session per user. The users hold: eve gh-every on every repo, pam gh-pay on
// Strazahq/pay-api and Strazahq/pay-web, duo gh-a-pay on Strazahq/pay-api
// and gh-b-site on Strazahq/site, each with a credential of its own, and
// old gh-bare with no covers line. Every binding binds every tool.
type ghRig struct {
	app      *App
	base     string
	up       *ghUpstream
	appID    string
	tokens   map[string]string
	bindings map[string]string // role to binding id
	creds    map[string]string // role to credential id
	cs       *countingStore
	clock    *ghClock
}

// ghClock is the object loop's clock in the rig: the wall clock moved on
// by what advance adds, so a test can step past the minute that limits
// listings while the loop runs.
type ghClock struct{ off atomic.Int64 }

func (c *ghClock) now() time.Time {
	return time.Now().Add(time.Duration(c.off.Load())).UTC().Truncate(time.Microsecond)
}

func (c *ghClock) advance(d time.Duration) { c.off.Add(int64(d)) }

// bootGHApp boots strazad over an instrumented store, as testAppCounting
// does, with the object loop paced before it runs: a tick of an hour, so
// the loop lists only when a mark or a miss wakes it and never reads the
// store inside a test's window on its own, and clock as its clock.
func bootGHApp(t *testing.T, clock *ghClock, mutators ...func(*config.Config)) (*App, string, *countingStore) {
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
			Catalog: config.Catalog{WarnSize: 100}, Objects: config.AppsObjects{Enabled: true}},
	}
	for _, mutate := range mutators {
		mutate(&cfg)
	}
	seedStoreTemplate(t, cfg)
	raw, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cs := &countingStore{inner: raw}
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

// ghScopes are the covers lines of the rig's roles.
var ghScopes = map[string]*store.ObjectScope{
	"gh-every":  {Every: true},
	"gh-pay":    {Objects: []store.CoveredObject{{ID: "101", Value: "Strazahq/pay-api"}, {ID: "102", Value: "Strazahq/pay-web"}}},
	"gh-a-pay":  {Objects: []store.CoveredObject{{ID: "101", Value: "Strazahq/pay-api"}}},
	"gh-b-site": {Objects: []store.CoveredObject{{ID: "103", Value: "Strazahq/site"}}},
	"gh-bare":   nil,
}

// ghUsers maps each user to the roles it holds.
var ghUsers = map[string][]string{"eve": {"gh-every"}, "pam": {"gh-pay"}, "duo": {"gh-a-pay", "gh-b-site"}, "old": {"gh-bare"}}

func newGHRig(t *testing.T, mutators ...func(*config.Config)) *ghRig {
	t.Helper()
	clock := &ghClock{}
	app, base, cs := bootGHApp(t, clock, mutators...)
	ctx := context.Background()
	r := &ghRig{app: app, base: base, up: startGHUpstream(t), cs: cs, clock: clock, tokens: map[string]string{},
		bindings: map[string]string{}, creds: map[string]string{}}
	mf, err := managerParse(t, ghManifest(r.up.URL))
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
	for name, scope := range ghScopes {
		ro, err := app.store.Roles().Create(ctx, store.Role{Name: name, Kind: store.RoleKindApplication})
		if err != nil {
			t.Fatal(err)
		}
		b, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: row.ID, ToolMatcher: `["*"]`, Scope: scope})
		if err != nil {
			t.Fatal(err)
		}
		r.bindings[name] = b.ID
		if name == "gh-a-pay" || name == "gh-b-site" {
			c, err := app.broker.Set(ctx, row.ID, ro.ID, "tok-"+name)
			if err != nil {
				t.Fatal(err)
			}
			r.creds[name] = c.ID
		}
	}
	app.manager.SecretUpdated(ctx, row.ID)
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	for user, roles := range ghUsers {
		u := seedGatewayUser(t, app, user, roles[0])
		for _, name := range roles[1:] {
			ro, err := app.store.Roles().GetByName(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: ro.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	app.resolver.Bump()
	catWaitRunning(t, app, "ghapp")
	catRecompile(t, app)
	if app.cfg.Apps.Objects.Enabled {
		if err := app.objects.MarkDue(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		r.waitList(t, func(l []string) bool { return len(l) == 3 })
	}
	for user := range ghUsers {
		r.tokens[user] = sessionToken(t, base, user)
	}
	return r
}

// waitList waits until this replica's list of ghapp passes ok.
func (r *ghRig) waitList(t *testing.T, ok func(values []string) bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var values []string
		if l := r.app.objects.Get(r.appID); l != nil {
			for _, o := range l.Objects {
				values = append(values, o.Value)
			}
		}
		if ok(values) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ghapp's object list never settled: %v", values)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// endpoint is one of the two gateway endpoints a tools/call reaches ghapp
// through, with the tool name it takes there.
type endpoint struct {
	name string
	path string
	tool func(string) string
}

var ghEndpoints = []endpoint{
	{"/mcp", "/mcp", func(tool string) string { return "ghapp__" + tool }},
	{"/mcp/ghapp", "/mcp/ghapp", func(tool string) string { return tool }},
}

// call posts a tools/call of tool with the raw arguments and the agent's
// own _meta, written as given, and answers the tool result's text and
// whether it is an error.
func (r *ghRig) call(t *testing.T, ep endpoint, user, tool, args string, meta string) (string, bool) {
	t.Helper()
	params := `{"name":` + jsonString(ep.tool(tool))
	if args != "" {
		params += `,"arguments":` + args
	}
	if meta != "" {
		params += `,"_meta":` + meta
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}}`
	req, _ := http.NewRequest(http.MethodPost, r.base+ep.path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.tokens[user])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var answer struct {
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || answer.Result == nil || len(answer.Result.Content) == 0 {
		t.Fatalf("tools/call %s by %s on %s answered %s", tool, user, ep.name, raw)
	}
	return answer.Result.Content[0].Text, answer.Result.IsError
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// mcpRecords answers the data of every straza.audit.mcp record of ghapp
// that carry nonce, waiting until at least want are there.
func (r *ghRig) mcpRecords(t *testing.T, nonce string, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		recs, err := r.app.store.Audit().List(context.Background(), 0, 5000)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		for _, rec := range recs {
			if !strings.Contains(rec.CE, `"type":"straza.audit.mcp"`) || !strings.Contains(rec.CE, nonce) {
				continue
			}
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(rec.CE), &ce); err != nil {
				t.Fatal(err)
			}
			if ce.Data["app"] == "ghapp" {
				out = append(out, ce.Data)
			}
		}
		if len(out) >= want || time.Now().After(deadline) {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
}
