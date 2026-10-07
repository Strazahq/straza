package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

const ccAudience = "https://idp.example/realms/straza"

// ccProvider is an identity provider double for the client credentials
// grant. It verifies every client assertion against the key document the
// server under test publishes, as a real provider does, and answers a token
// named after the client, so a token that reaches the wrong agent shows.
type ccProvider struct {
	*httptest.Server
	keys func() []byte

	mu      sync.Mutex
	clients []string
	down    bool
}

func startCCProvider(t *testing.T) *ccProvider {
	t.Helper()
	p := &ccProvider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		down := p.down
		p.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		set, err := jwk.Parse(p.keys())
		if err != nil {
			t.Errorf("the key document does not parse: %v", err)
		}
		tok, err := jwt.Parse([]byte(r.PostForm.Get("client_assertion")), jwt.WithKeySet(set), jwt.WithAudience(ccAudience))
		client := r.PostForm.Get("client_id")
		if err != nil {
			t.Errorf("the client assertion does not verify: %v", err)
		} else if iss, _ := tok.Issuer(); iss != client {
			t.Errorf("assertion iss = %q, want the client id %q", iss, client)
		} else if sub, _ := tok.Subject(); sub != client {
			t.Errorf("assertion sub = %q, want the client id %q", sub, client)
		}
		p.mu.Lock()
		p.clients = append(p.clients, client)
		n := len(p.clients)
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"cc-tok-%s-%d","token_type":"Bearer","expires_in":300}`, client, n)
	}))
	t.Cleanup(p.Close)
	return p
}

// asked returns the client ids the provider was asked for, in order.
func (p *ccProvider) asked() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.clients, ",")
}

// installCCApp installs an oauth server whose agents use client credentials.
func installCCApp(t *testing.T, app *App, up *gatewayUpstream, name string) store.App {
	t.Helper()
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: oauth
    agents: client_credentials
    oauth: {provider: keycloak}
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`, name, name, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.manager.CheckProvider(mf); err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(context.Background(), mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	catReach(t, app, "dev", row, `["*"]`)
	catWaitRunning(t, app, name)
	return row
}

func withCCProvider(idp *ccProvider) func(*config.Config) {
	return func(cfg *config.Config) {
		cfg.Server.PublicURL = "https://straza.example"
		cfg.OAuth.RefreshInterval = time.Hour
		cfg.OAuth.Providers = map[string]config.OAuthProvider{"keycloak": {
			ClientID: "straza-connect", ClientSecret: "s3cret",
			AuthURL: idp.URL + "/auth", TokenURL: idp.URL + "/token",
			ClientCredentials: &config.ClientCredentials{AssertionAudience: ccAudience},
		}}
	}
}

// TestGatewayClientCredentialsLane is the lane end to end. Until a key signs,
// an agent is refused with the sentence for its key state. Then each agent
// runs on the token of its own client, fetched once and served from memory
// with no store read, while a person is refused as on any oauth server and
// the provider never hears of them. Every call leaves exactly one record: an
// allow that names the source client_credentials, or a deny with the refusal
// and no credential fields. A revoked session and a locked user cost the
// agent its cached token. No answer and no record carries a token or an
// assertion.
func TestGatewayClientCredentialsLane(t *testing.T) {
	t.Parallel()
	idp := startCCProvider(t)
	app, base, cs := testAppCounting(t, withCCProvider(idp))
	idp.keys = func() []byte {
		doc, err := app.assertionKeys.JWKS(time.Now())
		if err != nil {
			t.Errorf("JWKS: %v", err)
		}
		return doc
	}
	up := startGatewayUpstream(t)
	ctx := context.Background()
	seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	sam := seedAgent(t, app, "sam", "", "dev")
	row := installCCApp(t, app, up, "ccapp")
	catRecompile(t, app)

	aliceTok := sessionToken(t, base, "alice")
	joeTok, joeSID := gatewaySession(t, base, "joe")
	samTok := sessionToken(t, base, "sam")
	var answers []string
	call := func(tok string) string {
		raw := echoCall(t, base, tok, "ccapp")
		answers = append(answers, raw)
		return raw
	}
	denied := func(user store.User, reason string) {
		t.Helper()
		recs := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
			return d["app"] == "ccapp" && d["user"] == user.ID && d["effect"] == "deny" && d["reason"] == reason
		})
		if recs[0]["credentialSource"] != nil || recs[0]["credentialId"] != nil || recs[0]["credentialOwner"] != nil {
			t.Errorf("the refused record carries credential fields: %v", recs[0])
		}
	}

	const open = "agent joe could not get a ccapp token. "
	noKey := open + "This Straza deployment has no client assertion key that signs: none was created yet, or the last one was retired. An administrator runs strazactl signing-keys rotate client_assertion, and agents can call about a minute later."
	if raw := call(joeTok); !strings.Contains(raw, noKey) {
		t.Fatalf("joe before any key = %s", raw)
	}
	denied(joe, noKey)

	now := time.Now()
	if _, err := app.assertionKeys.Stage(ctx, now); err != nil {
		t.Fatal(err)
	}
	staged := open + "The client assertion key was created moments ago and starts signing within a minute. Call again then."
	if raw := call(joeTok); !strings.Contains(raw, staged) {
		t.Fatalf("joe on a staged key = %s", raw)
	}
	denied(joe, staged)
	if got := idp.asked(); got != "" {
		t.Fatalf("the provider was asked for %q before any key signed", got)
	}
	if step, err := app.assertionKeys.Advance(ctx, time.Now().Add(2*authn.KeyReloadInterval), time.Minute); err != nil || step.Promoted == "" {
		t.Fatalf("Advance = %+v, %v, want the staged key promoted", step, err)
	}

	// The first call fetches and the second is served from memory, and
	// neither reads the store on its way.
	before := cs.snapshot()
	if raw := call(joeTok); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("joe on a signing key = %s", raw)
	}
	if raw := call(joeTok); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("joe's second call = %s", raw)
	}
	assertNoRepoAccess(t, cs, "client credentials calls, the fetch and the cached one", before)
	if got := idp.asked(); got != "joe" {
		t.Fatalf("two calls by joe asked the provider for %q, want joe once", got)
	}
	if !up.sawAuth("Bearer cc-tok-joe-1") {
		t.Fatal("joe's token never reached the upstream")
	}
	allowed := awaitMCPRecords(t, app, 2, func(d map[string]any) bool {
		return d["app"] == "ccapp" && d["user"] == joe.ID && d["effect"] == "allow"
	})
	for _, rec := range allowed {
		if rec["credentialSource"] != "client_credentials" || rec["credentialId"] != "client_credentials:"+row.ID+":"+joe.ID || rec["credentialOwner"] != nil {
			t.Errorf("joe's record = %v, want the source client_credentials, the stable id and no owner", rec)
		}
	}

	// Another agent gets the token of its own client and never joe's.
	if raw := call(samTok); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("sam = %s", raw)
	}
	if got := idp.asked(); got != "joe,sam" {
		t.Fatalf("the provider was asked for %q, want joe then sam", got)
	}
	if !up.sawAuth("Bearer cc-tok-sam-2") {
		t.Fatal("sam's own token never reached the upstream")
	}

	// A person is refused as on every oauth server, and nothing is fetched.
	person := "MCP server ccapp needs your own keycloak sign-in and you have not connected. Run straza connect ccapp, or sign in on your credentials page at https://straza.example/self-service/credentials"
	if raw := call(aliceTok); !strings.Contains(raw, person) {
		t.Fatalf("alice = %s", raw)
	}
	if got := idp.asked(); got != "joe,sam" {
		t.Fatalf("a person's call asked the provider: %q", got)
	}

	// A revoked session costs joe his cached token, so his next session asks
	// again, and a locked user loses theirs the same way.
	app.denylist.RevokeSession(joeSID)
	if raw := call(sessionToken(t, base, "joe")); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("joe on a new session = %s", raw)
	}
	if got := idp.asked(); got != "joe,sam,joe" {
		t.Fatalf("after joe's session was revoked the provider was asked for %q, want a third fetch for joe", got)
	}
	app.denylist.RevokeUser(sam.ID)
	app.denylist.AllowUser(sam.ID)
	if raw := call(samTok); !strings.Contains(raw, "echo: hi") {
		t.Fatalf("sam after the lock was lifted = %s", raw)
	}
	if got := idp.asked(); got != "joe,sam,joe,sam" {
		t.Fatalf("after sam was locked the provider was asked for %q, want a fourth fetch for sam", got)
	}

	// A provider that is down refuses with the provider's name and no detail
	// of its answer.
	idp.mu.Lock()
	idp.down = true
	idp.mu.Unlock()
	app.denylist.RevokeSession("no-such-session")
	app.clientTokens.DropUser(joe.ID)
	downReason := open + "keycloak answered the token request at " + idp.URL + "/token with HTTP 503 and no OAuth error. Check oauth.providers.keycloak.tokenUrl in strazad's config and that the provider is healthy, then call again."
	if raw := call(sessionToken(t, base, "joe")); !strings.Contains(raw, downReason) {
		t.Fatalf("joe while the provider is down = %s", raw)
	}
	denied(joe, downReason)

	events := outboxDataFor(t, app, "straza.audit.mcp")
	for _, leak := range []string{"cc-tok-", "eyJ"} {
		for _, raw := range answers {
			if strings.Contains(raw, leak) {
				t.Errorf("a gateway answer carries %q: %s", leak, raw)
			}
		}
		if s := fmt.Sprint(events); strings.Contains(s, leak) {
			t.Errorf("an audit record carries %q", leak)
		}
	}
}

// TestInstallRefusesClientCredentialsWithoutTheBlock: the install lane names
// the config key when the provider's block is missing.
func TestInstallRefusesClientCredentialsWithoutTheBlock(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.OAuth.RefreshInterval = time.Hour
		cfg.OAuth.Providers = map[string]config.OAuthProvider{"keycloak": {
			ClientID: "straza-connect", ClientSecret: "s3cret", AuthURL: "https://idp.example/auth", TokenURL: "https://idp.example/token",
		}}
	})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	body := ccManifest("https://mcp.example/mcp")
	var refused map[string]string
	code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(body), &refused)
	const want = "server echoapp sets credential.agents to client_credentials, and the provider keycloak has no clientCredentials settings. Add oauth.providers.keycloak.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared."
	if code < 400 || !strings.Contains(refused["error"], want) {
		t.Fatalf("install = %d %v, want a refusal with %q", code, refused, want)
	}
}

func ccManifest(url string) string {
	return strings.TrimRight(echoManifest(url), "\n") + `
  credential:
    kind: oauth
    agents: client_credentials
    oauth: {provider: keycloak}
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`
}

// TestServerAdminCannotOptIntoClientCredentials pins who may switch a server
// to agents client_credentials: a full administrator, never the server's own
// delegated administrator, who would receive every calling agent's token at
// an address of their choice. The delegated administrator may still change
// other fields of a server that already uses the lane, and may leave it.
func TestServerAdminCannotOptIntoClientCredentials(t *testing.T) {
	t.Parallel()
	idp := startCCProvider(t)
	app, base := testApp(t, withCCProvider(idp), func(cfg *config.Config) {
		p := cfg.OAuth.Providers["keycloak"]
		cfg.OAuth.Providers["okta"] = p
	})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	own := strings.Replace(ccManifest(up.URL), "agents: client_credentials", "agents: own", 1)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(own), nil); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	mkHuman(t, app, "erin", "mcp-admin-echoapp")
	erinTok := loginDeviceFlow(t, base, "erin", "hunter2!")

	const want = "setting credential.agents to client_credentials, or changing the provider of a server that uses it, needs the scope apps:write or the role straza-global-mcp-admin, because every agent that calls the server then gets a token of its own at the provider and the token is sent to the server's address. Ask a holder of straza-global-mcp-admin to make that change."
	post := func(tok, body string, dry bool) (int, string) {
		t.Helper()
		url := base + "/v1/admin/apps"
		if dry {
			url += "?dryRun=1"
		}
		var out map[string]any
		code := rawReq(t, "POST", url, tok, "application/yaml", []byte(body), &out)
		msg, _ := out["error"].(string)
		return code, msg
	}
	optIn := ccManifest(up.URL)
	for _, dry := range []bool{true, false} {
		if code, msg := post(erinTok, optIn, dry); code != http.StatusForbidden || msg != want {
			t.Errorf("erin opts in (dry run %v) = %d %q, want 403 %q", dry, code, msg, want)
		}
	}
	if code, msg := post(root, optIn, false); code != http.StatusCreated {
		t.Fatalf("kim opts in = %d %q, want 201", code, msg)
	}
	if code, msg := post(erinTok, strings.Replace(optIn, `version: "1.0.0"`, `version: "2.0.0"`, 1), false); code != http.StatusCreated {
		t.Errorf("erin changes the version of an opted-in server = %d %q, want 201", code, msg)
	}
	otherProvider := strings.Replace(optIn, "provider: keycloak", "provider: okta", 1)
	if code, msg := post(erinTok, otherProvider, false); code != http.StatusForbidden || msg != want {
		t.Errorf("erin changes the provider = %d %q, want 403 %q", code, msg, want)
	}
	if code, msg := post(erinTok, own, false); code != http.StatusCreated {
		t.Errorf("erin leaves the lane = %d %q, want 201", code, msg)
	}
	if code, msg := post(erinTok, optIn, false); code != http.StatusForbidden || msg != want {
		t.Errorf("erin opts in again = %d %q, want 403 %q", code, msg, want)
	}
}
