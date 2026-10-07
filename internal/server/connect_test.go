package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// testConnectPage is the credentials tab the deny sentences name under
// testApp, whose config carries no public URL.
const testConnectPage = "/self-service/credentials"

// oauthProviderMock plays the GitHub OAuth authorization server: /authorize
// redirects straight back with a one-shot code bound to whoever the test
// declared as the browser identity, /token redeems codes and refresh tokens.
// Access tokens are versioned per user ("gho_<user>-v<n>") so a refresh is
// observable end to end.
type oauthProviderMock struct {
	*httptest.Server
	expiresIn int64 // 0 = non-expiring tokens, no refresh handle
	// onToken runs once, before the token endpoint answers a code, which is
	// the moment between strazad's read of the server row and its write of
	// the grant. A test sets it to move live state in that window.
	onToken func()

	mu         sync.Mutex
	nextUser   string
	codes      map[string]string // code → user
	versions   map[string]int
	refreshes  int
	tokenCalls int
}

func startOAuthProviderMock(t *testing.T, expiresIn int64) *oauthProviderMock {
	t.Helper()
	m := &oauthProviderMock{expiresIn: expiresIn, codes: map[string]string{}, versions: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "cid-test" || q.Get("state") == "" || q.Get("redirect_uri") == "" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		user := m.nextUser
		code := fmt.Sprintf("code-%s-%d", user, len(m.codes))
		m.codes[code] = user
		m.mu.Unlock()
		cb, _ := url.Parse(q.Get("redirect_uri"))
		cq := cb.Query()
		cq.Set("code", code)
		cq.Set("state", q.Get("state"))
		cb.RawQuery = cq.Encode()
		http.Redirect(w, r, cb.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") != "cid-test" || r.PostForm.Get("client_secret") != "csec-NEVER-CLIENT-SIDE" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"incorrect_client_credentials"}`))
			return
		}
		var user string
		m.mu.Lock()
		m.tokenCalls++
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			code := r.PostForm.Get("code")
			user = m.codes[code]
			delete(m.codes, code) // one-shot
		case "refresh_token":
			user = strings.TrimPrefix(r.PostForm.Get("refresh_token"), "ghr_")
			if _, known := m.versions[user]; !known {
				user = ""
			} else {
				m.refreshes++
			}
		}
		if user == "" {
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		m.versions[user]++
		v := m.versions[user]
		hook := m.onToken
		m.onToken = nil
		m.mu.Unlock()
		if hook != nil {
			hook()
		}
		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"access_token":"gho_%s-v%d","token_type":"bearer"`, user, v)
		if m.expiresIn > 0 {
			body += fmt.Sprintf(`,"expires_in":%d,"refresh_token":"ghr_%s"`, m.expiresIn, user)
		}
		_, _ = w.Write([]byte(body + "}"))
	})
	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)
	return m
}

func (m *oauthProviderMock) refreshCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refreshes
}

// tokenCallCount is how often the token endpoint was asked with the right
// client secret, for a code or for a refresh.
func (m *oauthProviderMock) tokenCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokenCalls
}

func oauthManifest(upstreamURL string) string {
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: github
  namespace: io.github.github
server:
  name: io.github.github/github-mcp-server
  version: "0.17.1"
straza:
  runtime:
    kind: remote
    remote:
      url: %s
      auth: inject
  credential:
    kind: oauth
    oauth:
      provider: github
      scopes: [repo, read:org]
    inject:
      as: header
      name: Authorization
      template: "Bearer {{secret}}"
  exposure:
    tools: ["*"]
`, upstreamURL)
}

func withOAuthGithub(provider *oauthProviderMock) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Apps.PollInterval = 100 * time.Millisecond
		cfg.Apps.HealthInterval = time.Hour
		cfg.OAuth.RefreshInterval = time.Hour // tests trigger refreshes via boot passes only
		cfg.OAuth.Providers = map[string]config.OAuthProvider{"github": {
			ClientID: "cid-test", ClientSecret: "csec-NEVER-CLIENT-SIDE",
			AuthURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token",
		}}
	}
}

// installOAuthGithubApp seeds the app row, role binding, and an allow policy,
// then installs the oauth manifest over the row and waits for it to run.
func installOAuthGithubApp(t *testing.T, app *App, upstreamURL string) store.App {
	t.Helper()
	ctx := context.Background()
	appRow, err := app.store.Apps().Create(ctx, store.App{Name: "github", RuntimeKind: "remote", Manifest: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: appRow.ID, ToolMatcher: `["*"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "github-oauth-allow", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: github-oauth-allow}
spec:
  match: {roles: [dev]}
  rules:
    - id: github-all
      tools: [mcp.call]
      apps: [github]
      effect: allow
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}

	mf, err := manager.Parse([]byte(oauthManifest(upstreamURL)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.manager.Install(ctx, mf, store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("github"); ok && v.Status == "running" {
			return appRow
		}
		if time.Now().After(deadline) {
			v, _ := app.manager.View("github")
			t.Fatalf("github app not running: %+v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// connectUser drives one user through the full connect flow: start, the
// browser's round trip through the provider to strazad's callback, and the
// signed-in finish. Returns every client-visible byte.
func connectUser(t *testing.T, base, sessionTok string, provider *oauthProviderMock, username string) []byte {
	t.Helper()
	var clientBytes bytes.Buffer
	authorize, _, startBody := startConnect(t, base, sessionTok)
	clientBytes.Write(startBody)
	resp, cbBody := browserReturn(t, authorize, provider, username)
	clientBytes.Write(cbBody)
	handed := handedOver(t, resp)
	code, body := finishConnect(t, base, sessionTok, map[string]string{"code": handed.Get("code"), "state": handed.Get("state")})
	clientBytes.WriteString(body)
	if code != http.StatusOK || !strings.Contains(body, `"app":"github"`) {
		t.Fatalf("connect finish = %d: %s", code, body)
	}
	return clientBytes.Bytes()
}

// TestP52ConnectTwoUsersDistinctIdentities pins that two users connect
// their own accounts to one oauth-kind app and provably act as two distinct
// upstream identities; an unconnected user is denied with an actionable
// reason; no token material ever reaches a client; disconnect cuts access.
func TestP52ConnectTwoUsersDistinctIdentities(t *testing.T) {
	t.Parallel()
	gh := startGithubMock(t)
	provider := startOAuthProviderMock(t, 0) // classic non-expiring tokens

	app, base := testApp(t, withOAuthGithub(provider))
	var clientBytes bytes.Buffer

	seedGatewayUser(t, app, "alice", "dev")
	seedGatewayUser(t, app, "bob", "dev")
	seedGatewayUser(t, app, "carol", "dev")
	installOAuthGithubApp(t, app, gh.URL)

	aliceTok := sessionToken(t, base, "alice")
	bobTok := sessionToken(t, base, "bob")
	carolTok := sessionToken(t, base, "carol")

	clientBytes.Write(connectUser(t, base, aliceTok, provider, "alice"))
	clientBytes.Write(connectUser(t, base, bobTok, provider, "bob"))

	// Connection status: alice sees herself connected; carol does not.
	code, statusBody := getJSONAuth(t, base+"/v1/connect", aliceTok)
	clientBytes.WriteString(statusBody)
	if code != http.StatusOK || !strings.Contains(statusBody, `"connected":true`) {
		t.Errorf("alice connect status = %d %s", code, statusBody)
	}
	code, statusBody = getJSONAuth(t, base+"/v1/connect", carolTok)
	clientBytes.WriteString(statusBody)
	if code != http.StatusOK || strings.Contains(statusBody, `"connected":true`) {
		t.Errorf("carol connect status = %d %s", code, statusBody)
	}

	// Same app, same tool, two distinct upstream identities.
	for _, c := range []struct{ tok, want string }{
		{aliceTok, "Bearer gho_alice-v1"},
		{bobTok, "Bearer gho_bob-v1"},
	} {
		_, _, raw := mcpCall(t, base, c.tok, "tools/call", map[string]any{
			"name": "github__get_issue", "arguments": map[string]int{"number": 7},
		})
		clientBytes.Write(raw)
		if !strings.Contains(string(raw), "issue #7") {
			t.Errorf("call as %s failed: %s", c.want, raw)
		}
		if !gh.sawAuth(c.want) {
			t.Errorf("upstream never saw %s", c.want)
		}
	}

	// Unconnected carol: denied, with the page where she signs in.
	_, _, carolRaw := mcpCall(t, base, carolTok, "tools/call", map[string]any{
		"name": "github__get_issue", "arguments": map[string]int{"number": 1},
	})
	clientBytes.Write(carolRaw)
	const signIn = "MCP server github needs your own github sign-in and you have not connected. Run straza connect github, or sign in on your credentials page at " + testConnectPage
	if !strings.Contains(string(carolRaw), signIn) {
		t.Errorf("carol's deny is not actionable: %s", carolRaw)
	}

	// No token material or provider client secret in any client-visible
	// byte: credentials never reach the agent.
	for _, secret := range []string{"gho_", "ghr_", "csec-NEVER-CLIENT-SIDE"} {
		if bytes.Contains(clientBytes.Bytes(), []byte(secret)) {
			t.Fatalf("%q LEAKED into client-visible bytes", secret)
		}
	}

	// Disconnect alice: the next call is denied again.
	req, _ := http.NewRequest(http.MethodDelete, base+"/v1/connect/github", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disconnect = %d", resp.StatusCode)
	}
	_, _, raw := mcpCall(t, base, aliceTok, "tools/call", map[string]any{
		"name": "github__get_issue", "arguments": map[string]int{"number": 1},
	})
	if !strings.Contains(string(raw), signIn) {
		t.Errorf("post-disconnect call not denied: %s", raw)
	}
}

// TestP52RefreshSurvivesRestart pins that a grant
// whose access token is near expiry gets rotated by the refresh worker after
// a full strazad restart, from nothing but the sealed credential row: no
// re-login, no re-connect.
func TestP52RefreshSurvivesRestart(t *testing.T) {
	t.Parallel()
	gh := startGithubMock(t)
	provider := startOAuthProviderMock(t, 120) // expiring tokens with refresh handles
	sharedDir := t.TempDir()
	shared := func(cfg *config.Config) {
		cfg.DataDir = sharedDir
		cfg.Store.DSN = filepath.Join(sharedDir, "straza.db")
	}

	t.Run("connect before restart", func(t *testing.T) {
		app, base := testApp(t, withOAuthGithub(provider), shared)
		seedGatewayUser(t, app, "alice", "dev")
		installOAuthGithubApp(t, app, gh.URL)
		aliceTok := sessionToken(t, base, "alice")
		connectUser(t, base, aliceTok, provider, "alice")
		_, _, raw := mcpCall(t, base, aliceTok, "tools/call", map[string]any{
			"name": "github__get_issue", "arguments": map[string]int{"number": 1},
		})
		if !strings.Contains(string(raw), "issue #1") || !gh.sawAuth("Bearer gho_alice-v1") {
			t.Fatalf("pre-restart call failed: %s", raw)
		}
	}) // subtest cleanup = full strazad shutdown

	if provider.refreshCount() != 0 {
		t.Fatalf("refresh ran before the restart (interval knob broken)")
	}

	t.Run("restart rotates and serves", func(t *testing.T) {
		app, base := testApp(t, withOAuthGithub(provider), shared)
		_ = app
		// The boot-time refresher pass finds the sealed row (expiry within
		// the 10-min window) and rotates it with the stored refresh token.
		deadline := time.Now().Add(10 * time.Second)
		for provider.refreshCount() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("refresh worker never rotated the grant after restart")
			}
			time.Sleep(50 * time.Millisecond)
		}
		// No re-connect happened, yet the gateway serves the NEW token.
		aliceTok := sessionToken(t, base, "alice")
		var raw []byte
		for time.Now().Before(deadline) { // cache refresh trails the rotation by a hair
			_, _, raw = mcpCall(t, base, aliceTok, "tools/call", map[string]any{
				"name": "github__get_issue", "arguments": map[string]int{"number": 2},
			})
			if strings.Contains(string(raw), "issue #2") && gh.sawAuth("Bearer gho_alice-v2") {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("post-restart call never used the refreshed token: %s", raw)
	})
}

// TestConnectFinishRefusesAGrantForAServerRemovedAndAddedAgain pins what a
// sign-in meets when github is removed and added again while it is under
// way, which gives the name a new row and a new id. Between strazad's read
// of the row and its write of the grant, the grant names the old id, meets
// no row and is refused with 409 saying what happened and what to do; at
// the row read, the finish answers 400 the same way. Nothing is stored on
// the new server either time, and alice's next sign-in lands on it.
func TestConnectFinishRefusesAGrantForAServerRemovedAndAddedAgain(t *testing.T) {
	t.Parallel()
	gh := startGithubMock(t)
	provider := startOAuthProviderMock(t, 0)
	app, base := testApp(t, withOAuthGithub(provider))
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	seedGatewayUser(t, app, "alice", "dev")
	installOAuthGithubApp(t, app, gh.URL)
	aliceTok := sessionToken(t, base, "alice")
	ctx := context.Background()
	readd := func(t *testing.T) {
		t.Helper()
		if code := adminReq(t, http.MethodDelete, base+"/v1/admin/apps/github", root, nil, nil); code != http.StatusOK {
			t.Errorf("remove github = %d", code)
		}
		if code := rawReq(t, http.MethodPost, base+"/v1/admin/apps", root, "application/yaml", []byte(oauthManifest(gh.URL)), nil); code != http.StatusCreated {
			t.Errorf("add github again = %d", code)
		}
	}
	rowID := func(t *testing.T) string {
		t.Helper()
		row, err := app.store.Apps().GetByName(ctx, "github")
		if err != nil {
			t.Fatalf("read github: %v", err)
		}
		return row.ID
	}
	// nothingStored checks that the row id holds no credential row and that
	// no oauth.connect record was written since before, the count taken
	// before the refused finish.
	nothingStored := func(t *testing.T, id string, before int) {
		t.Helper()
		if creds, err := app.store.Credentials().ListByApp(ctx, id); err != nil || len(creds) != 0 {
			t.Errorf("the new github row carries %d credential rows, %v; want none", len(creds), err)
		}
		if n := countAction(t, app, "oauth.connect"); n != before {
			t.Errorf("%d oauth.connect records were written, want %d, since a refused finish writes none", n, before)
		}
	}

	t.Run("between the read and the write", func(t *testing.T) {
		was := rowID(t)
		authorize, _, _ := startConnect(t, base, aliceTok)
		resp, _ := browserReturn(t, authorize, provider, "alice")
		handed := handedOver(t, resp)
		provider.onToken = func() { readd(t) }
		before := countAction(t, app, "oauth.connect")
		code, body := finishConnect(t, base, aliceTok, map[string]string{"code": handed.Get("code"), "state": handed.Get("state")})
		const want = "The MCP server github was removed and added again while this sign-in was finishing, so nothing was stored. Press the Sign in button on its row again"
		if code != http.StatusConflict || !strings.Contains(body, want) {
			t.Fatalf("finish after the removal and the new row = %d %s, want 409 saying %q", code, body, want)
		}
		now := rowID(t)
		if now == was {
			t.Fatalf("github still reads the id %s; want a new row", was)
		}
		nothingStored(t, now, before)
		connectUser(t, base, aliceTok, provider, "alice")
		if creds, err := app.store.Credentials().ListByApp(ctx, now); err != nil || len(creds) != 1 {
			t.Errorf("the new github row carries %d credential rows after alice's next sign-in, %v; want one", len(creds), err)
		}
	})

	t.Run("at the row read", func(t *testing.T) {
		authorize, _, _ := startConnect(t, base, aliceTok)
		resp, _ := browserReturn(t, authorize, provider, "alice")
		handed := handedOver(t, resp)
		readd(t)
		before := countAction(t, app, "oauth.connect")
		code, body := finishConnect(t, base, aliceTok, map[string]string{"code": handed.Get("code"), "state": handed.Get("state")})
		const want = "The MCP server this sign-in was started for was removed after the sign-in started, so nothing was stored. If it was added again, press the Sign in button on its row again"
		if code != http.StatusBadRequest || !strings.Contains(body, want) {
			t.Fatalf("finish after the row went = %d %s, want 400 saying %q", code, body, want)
		}
		nothingStored(t, rowID(t), before)
	})
}

// countAction counts the outbox records whose data names action.
func countAction(t *testing.T, app *App, action string) int {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if strings.Contains(row.CE, `"action":"`+action+`"`) {
			n++
		}
	}
	return n
}

func getJSONAuth(t *testing.T, urlStr, bearer string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, urlStr, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
