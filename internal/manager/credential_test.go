package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestManifestCallerKinds pins the credential grammar of the caller kinds:
// token is a kind of its own, agents is valid only with a caller kind and
// takes own, sponsor, shared or client_credentials, the last with kind oauth
// only because the provider issues the token, and a caller kind on a command
// or oci runtime is refused at parse time, because one process is one
// identity. Each inject refusal is pinned whole: it says what failed, why,
// and what to set.
func TestManifestCallerKinds(t *testing.T) {
	const remote = `
    kind: remote
    remote: {url: https://example.com/mcp}`
	const command = `
    kind: command
    command: {exec: npx, args: ["-y", "example-mcp"]}`
	const oci = `
    kind: oci
    oci: {image: example/mcp:1}`
	doc := func(runtime, credential string) []byte {
		return []byte(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: callers}
server: {name: example/callers, version: "1.0.0"}
straza:
  runtime:` + runtime + `
  credential:` + credential)
	}
	header := `
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}`
	env := `
    inject: {as: env, name: TOKEN}`
	cases := []struct {
		name       string
		runtime    string
		credential string
		wantErr    string
		wantAgents string
	}{
		{name: "token on remote", runtime: remote, credential: "\n    kind: token" + header, wantAgents: AgentsOwn},
		{name: "token with sponsor fallback", runtime: remote, credential: "\n    kind: token\n    agents: sponsor" + header, wantAgents: AgentsSponsor},
		{name: "oauth with shared fallback", runtime: remote, credential: "\n    kind: oauth\n    agents: shared\n    oauth: {provider: github}" + header, wantAgents: AgentsShared},
		{name: "token without inject", runtime: remote, credential: "\n    kind: token", wantErr: `credential.kind token needs an inject block, because Straza must know where the secret goes on each call. Add credential.inject, for example as: header, name: Authorization, template: "Bearer {{secret}}" on a remote runtime, or as: env, name: API_TOKEN on a command or oci runtime`},
		{name: "inject without a name", runtime: remote, credential: "\n    kind: static\n    inject: {as: header, template: \"Bearer {{secret}}\"}", wantErr: "credential.inject.name is empty, so Straza does not know which header or environment variable carries the secret. Set it, for example Authorization for a header or API_TOKEN for an environment variable"},
		{name: "template without the placeholder", runtime: remote, credential: "\n    kind: static\n    inject: {as: header, name: Authorization, template: \"Bearer token\"}", wantErr: `credential.inject.template does not contain {{secret}}, so the secret would never be sent. Put {{secret}} where the secret goes, for example "Bearer {{secret}}", or leave template out to send the secret as it is`},
		{name: "header inject on command", runtime: command, credential: "\n    kind: static" + header, wantErr: "credential.inject.as header needs a remote runtime, because a command or oci runtime starts a process that reads an environment variable and gets no request headers. Use as: env with the variable name the process reads, for example API_TOKEN"},
		{name: "env inject on remote", runtime: remote, credential: "\n    kind: static" + env, wantErr: "credential.inject.as env does not work on a remote runtime, because an HTTP server reads the secret from a request header and Straza starts no process there. Use as: header with the header name the server reads, for example Authorization"},
		{name: "unknown inject as", runtime: remote, credential: "\n    kind: static\n    inject: {as: query, name: key}", wantErr: `credential.inject.as must be header or env, got "query", so Straza does not know where the secret goes. Use header on a remote runtime, because an HTTP server reads a request header, or env on a command or oci runtime, because the process reads an environment variable`},
		{name: "token with an oauth block", runtime: remote, credential: "\n    kind: token\n    oauth: {provider: github}" + header, wantErr: "credential.oauth is forbidden with kind token"},
		{name: "unknown agents value", runtime: remote, credential: "\n    kind: token\n    agents: everyone" + header, wantErr: `credential.agents must be own|sponsor|shared|client_credentials, got "everyone"`},
		{name: "oauth with client credentials", runtime: remote, credential: "\n    kind: oauth\n    agents: client_credentials\n    oauth: {provider: keycloak}" + header, wantAgents: AgentsClientCredentials},
		{name: "client credentials on token", runtime: remote, credential: "\n    kind: token\n    agents: client_credentials" + header, wantErr: "credential.agents client_credentials needs kind oauth, because the provider named under oauth.provider issues each agent's token. Use kind oauth, or set credential.agents to own, sponsor or shared"},
		{name: "client credentials on command", runtime: command, credential: "\n    kind: oauth\n    agents: client_credentials\n    oauth: {provider: keycloak}" + env, wantErr: "credential.kind oauth gives each caller their own credential, which a command runtime cannot take"},
		{name: "client credentials on oci", runtime: oci, credential: "\n    kind: oauth\n    agents: client_credentials\n    oauth: {provider: keycloak}" + env, wantErr: "credential.kind oauth gives each caller their own credential, which a oci runtime cannot take"},
		{name: "agents on static", runtime: remote, credential: "\n    kind: static\n    agents: shared" + header, wantErr: "credential.agents is only valid with kind oauth or token"},
		{name: "agents on none", runtime: remote, credential: "\n    kind: none\n    agents: own", wantErr: "credential.agents is only valid with kind oauth or token"},
		{name: "token on command", runtime: command, credential: "\n    kind: token" + env, wantErr: "credential.kind token gives each caller their own credential, which a command runtime cannot take: it is one process and one identity"},
		{name: "oauth on oci", runtime: oci, credential: "\n    kind: oauth\n    oauth: {provider: github}" + env, wantErr: "credential.kind oauth gives each caller their own credential, which a oci runtime cannot take"},
		{name: "static on command still fine", runtime: command, credential: "\n    kind: static" + env},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Parse(doc(tc.runtime, tc.credential))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := m.AgentsSource(); got != tc.wantAgents {
				t.Errorf("AgentsSource = %q, want %q", got, tc.wantAgents)
			}
		})
	}
}

// tableSecrets is a SecretSource scripted per app: one shared row and one
// own row per user.
type tableSecrets struct {
	shared map[string]*Secret
	users  map[string]map[string]UserCredential
}

func (s *tableSecrets) AppSecret(appID string) *Secret { return s.shared[appID] }
func (s *tableSecrets) ForRoles(appID string, _ []string, _ string) *Secret {
	return s.shared[appID]
}
func (s *tableSecrets) ForUser(appID, userID string) UserCredential { return s.users[appID][userID] }

// tableTokens is a ClientTokens double that records every request and
// answers a token named after the call count, or the scripted error.
type tableTokens struct {
	requests []ClientTokenRequest
	err      error
}

func (t *tableTokens) Token(_ context.Context, r ClientTokenRequest) (string, error) {
	t.requests = append(t.requests, r)
	if t.err != nil {
		return "", t.err
	}
	return "client-tok-" + string(rune('0'+len(t.requests))), nil
}

// TestCredentialResolution is the three-source table: a static app runs on
// the shared row; a caller-kind app runs on the caller's own row, denies an
// expired or unopenable own row without falling through, denies a human
// with no row, and lets an agent with no row follow credential.agents
// (own denies, sponsor needs the sponsor's usable row and opt-in, shared
// takes the static rows). A caller kind on a process runtime never
// resolves an own or sponsor row, whatever the manifest says. Every deny
// is pinned whole: it names the credentials page for the person who must
// act and strazactl only where an administrator must act. On agents
// client_credentials an agent with no row runs on its own client's token,
// asked for under the session's username and nothing else, and every other
// row proves by its fetch count that no person and no other path asks.
func TestCredentialResolution(t *testing.T) {
	const page = "https://straza.example/self-service/credentials"
	const admin = "strazactl connect github --user joe-java-developer-agent"
	past := time.Now().Add(-time.Hour)
	day := past.UTC().Format("2006-01-02")
	ok := &Secret{ID: "own-1", Value: "tok-own"}
	sponsorOK := &Secret{ID: "sp-1", Value: "tok-sponsor"}
	shared := &Secret{ID: "shared-1", Value: "tok-shared"}

	type row = UserCredential
	human := Caller{UserID: "alice", User: "alice", Roles: []string{"dev"}}
	agent := Caller{UserID: "joe", User: "joe-java-developer-agent", Roles: []string{"dev"}, Agent: true, Sponsor: "alice", SponsorID: "alice"}
	orphan := Caller{UserID: "joe", User: "joe-java-developer-agent", Roles: []string{"dev"}, Agent: true}
	// The client credentials callers carry a session, and a role named like
	// another agent, which must never become the client id.
	agentCC := Caller{UserID: "joe", User: "joe-java-developer-agent", Roles: []string{"sam-sre-agent"}, GrantingRole: "sam-sre-agent", Agent: true, Sponsor: "alice", SponsorID: "alice", Session: "ses-1"}
	orphanCC := Caller{UserID: "joe", User: "joe-java-developer-agent", Roles: []string{"dev"}, Agent: true, Session: "ses-1"}
	humanCC := Caller{UserID: "alice", User: "alice", Roles: []string{"dev"}, Session: "ses-2"}
	joeRequest := ClientTokenRequest{Provider: "keycloak", Server: "github", ServerID: "app-1", UserID: "joe", ClientID: "joe-java-developer-agent", Session: "ses-1"}
	const refused = "agent joe-java-developer-agent could not get a github token. keycloak refused the client joe-java-developer-agent: invalid_client."

	cases := []struct {
		name       string
		kind       string
		agents     string
		runtime    string
		caller     Caller
		shared     *Secret
		users      map[string]row
		noPage     bool
		tokensErr  error
		noLane     bool
		wantSource string
		wantOwner  string
		wantID     string
		wantFetch  *ClientTokenRequest
		wantErr    string
	}{
		{name: "none kind resolves nothing", kind: CredentialNone, runtime: RuntimeRemote, caller: human},
		{name: "static runs on the shared row", kind: CredentialStatic, runtime: RuntimeRemote, caller: human, shared: shared, wantSource: SourceShared},
		{name: "static without a row denies", kind: CredentialStatic, runtime: RuntimeRemote, caller: human,
			wantErr: "app github requires a credential and none is stored for the server or bound to your roles. An administrator sets one with strazactl apps secret set github, which asks for the value at a hidden prompt"},
		{name: "own row wins", kind: CredentialToken, runtime: RuntimeRemote, caller: human, users: map[string]row{"alice": {Secret: ok, Present: true}}, wantSource: SourceOwn},
		{name: "expired own row denies, never falls through", kind: CredentialToken, agents: AgentsShared, runtime: RuntimeRemote, caller: agent, shared: shared,
			users:   map[string]row{"joe": {Present: true, ExpiresAt: &past}},
			wantErr: "your token for app github expired on " + day + " (as it was recorded). Paste a new one on your credentials page at " + page},
		{name: "unopenable own row denies", kind: CredentialToken, runtime: RuntimeRemote, caller: human, users: map[string]row{"alice": {Present: true}},
			wantErr: "your token for app github cannot be opened by this server. Paste it again on your credentials page at " + page},
		{name: "human with no token row denies", kind: CredentialToken, agents: AgentsShared, runtime: RuntimeRemote, caller: human, shared: shared,
			wantErr: "MCP server github needs your own token and none is stored for you. Run straza connect github, or paste one on your credentials page at " + page},
		{name: "human with no oauth row denies", kind: CredentialOAuth, runtime: RuntimeRemote, caller: human,
			wantErr: "MCP server github needs your own keycloak sign-in and you have not connected. Run straza connect github, or sign in on your credentials page at " + page},
		{name: "a bare manager names the page without a URL", kind: CredentialToken, runtime: RuntimeRemote, caller: human, noPage: true,
			wantErr: "MCP server github needs your own token and none is stored for you. Run straza connect github, or paste one on your credentials page"},
		{name: "agent on agents own denies", kind: CredentialToken, agents: AgentsOwn, runtime: RuntimeRemote, caller: agent,
			wantErr: "agent joe-java-developer-agent has no token for app github. Its sponsor alice sets one on their credentials page at " + page + ", or an administrator sets it with " + admin},
		{name: "agent on agents own without a sponsor", kind: CredentialToken, agents: AgentsOwn, runtime: RuntimeRemote, caller: orphan,
			wantErr: "agent joe-java-developer-agent has no token for app github. An administrator sets one with " + admin},
		{name: "agent without a sponsor", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeRemote, caller: orphan,
			wantErr: "agent joe-java-developer-agent has no token for app github and no sponsor whose connection it could use. An administrator sets one with " + admin},
		{name: "sponsor row with opt-in", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			users: map[string]row{"alice": {Secret: sponsorOK, Present: true, AllowAgents: true}}, wantSource: SourceSponsor, wantOwner: "alice"},
		{name: "sponsor row without opt-in", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			users:   map[string]row{"alice": {Secret: sponsorOK, Present: true}},
			wantErr: "agent joe-java-developer-agent has no token for app github, and its sponsor alice has not allowed agents on their github connection. alice allows it on their credentials page at " + page + ", or an administrator sets the agent's own with " + admin},
		{name: "sponsor without a row", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			wantErr: "agent joe-java-developer-agent has no token for app github, and its sponsor alice has no github connection either. alice connects on their credentials page at " + page + " and allows agents there, or an administrator sets the agent's own with " + admin},
		{name: "sponsor row expired", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			users:   map[string]row{"alice": {Present: true, ExpiresAt: &past, AllowAgents: true}},
			wantErr: "alice's token for app github expired on " + day + " (as it was recorded). alice pastes a new one on their credentials page at " + page},
		{name: "sponsor row unopenable", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			users:   map[string]row{"alice": {Present: true, AllowAgents: true}},
			wantErr: "alice's token for app github cannot be opened by this server. alice pastes it again on their credentials page at " + page},
		{name: "oauth agent on agents own denies", kind: CredentialOAuth, agents: AgentsOwn, runtime: RuntimeRemote, caller: agent,
			wantErr: "agent joe-java-developer-agent has no github sign-in of its own. An agent cannot sign in through a browser. An administrator sets credential.agents on github to sponsor so it runs on alice's sign-in once alice allows it, or to shared or client_credentials"},
		{name: "oauth agent on agents own without a sponsor", kind: CredentialOAuth, agents: AgentsOwn, runtime: RuntimeRemote, caller: orphan,
			wantErr: "agent joe-java-developer-agent has no github sign-in of its own. An agent cannot sign in through a browser. An administrator sets credential.agents on github to shared or client_credentials"},
		{name: "oauth agent without a sponsor", kind: CredentialOAuth, agents: AgentsSponsor, runtime: RuntimeRemote, caller: orphan,
			wantErr: "agent joe-java-developer-agent has no github sign-in of its own and no sponsor whose connection it could use. An agent cannot sign in through a browser. An administrator sets credential.agents on github to shared or client_credentials"},
		{name: "oauth sponsor row without opt-in", kind: CredentialOAuth, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			users:   map[string]row{"alice": {Secret: sponsorOK, Present: true}},
			wantErr: "agent joe-java-developer-agent has no github sign-in of its own, and its sponsor alice has not allowed agents on their github connection. alice allows it on their credentials page at " + page},
		{name: "oauth sponsor without a row", kind: CredentialOAuth, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			wantErr: "agent joe-java-developer-agent has no github sign-in of its own, and its sponsor alice has no github connection either. alice connects on their credentials page at " + page + " and allows agents there"},
		{name: "oauth agent on agents shared without a shared row", kind: CredentialOAuth, agents: AgentsShared, runtime: RuntimeRemote, caller: agent,
			wantErr: "agent joe-java-developer-agent has no github sign-in of its own and no shared secret is set for the server. An administrator sets a shared one with strazactl apps secret set github, which asks for the value at a hidden prompt"},
		{name: "oauth sponsor row expired", kind: CredentialOAuth, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			users:   map[string]row{"alice": {Present: true, ExpiresAt: &past, AllowAgents: true}},
			wantErr: "alice's sign-in for app github expired on " + day + " (as it was recorded). alice signs in again on their credentials page at " + page},
		{name: "oauth own row expired", kind: CredentialOAuth, runtime: RuntimeRemote, caller: human,
			users:   map[string]row{"alice": {Present: true, ExpiresAt: &past}},
			wantErr: "your sign-in for app github expired on " + day + " (as it was recorded). Sign in again on your credentials page at " + page},
		{name: "oauth own row unopenable", kind: CredentialOAuth, runtime: RuntimeRemote, caller: human,
			users:   map[string]row{"alice": {Present: true}},
			wantErr: "your sign-in for app github cannot be opened by this server. Sign in again on your credentials page at " + page},
		{name: "agent own row beats the sponsor's", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeRemote, caller: agent,
			users: map[string]row{"joe": {Secret: ok, Present: true}, "alice": {Secret: sponsorOK, Present: true, AllowAgents: true}}, wantSource: SourceOwn},
		{name: "agent on agents shared", kind: CredentialToken, agents: AgentsShared, runtime: RuntimeRemote, caller: agent, shared: shared, wantSource: SourceShared},
		{name: "agent on agents shared without a shared row", kind: CredentialToken, agents: AgentsShared, runtime: RuntimeRemote, caller: agent,
			wantErr: "agent joe-java-developer-agent has no token for app github and no shared secret is set for the server. Its sponsor alice sets one on their credentials page at " + page + ", or an administrator sets it with " + admin + ", or a shared one with strazactl apps secret set github, which asks for the value at a hidden prompt"},
		{name: "token on a command runtime never resolves", kind: CredentialToken, agents: AgentsSponsor, runtime: RuntimeCommand, caller: agent,
			users:   map[string]row{"joe": {Secret: ok, Present: true}, "alice": {Secret: sponsorOK, Present: true, AllowAgents: true}},
			wantErr: "app github runs as one command process and cannot carry a credential per caller. Its manifest must use kind static"},
		{name: "oauth on an oci runtime never resolves", kind: CredentialOAuth, agents: AgentsShared, runtime: RuntimeOCI, caller: human, shared: shared,
			users:   map[string]row{"alice": {Secret: ok, Present: true}},
			wantErr: "app github runs as one oci process and cannot carry a credential per caller. Its manifest must use kind static"},
		{name: "client credentials: an agent runs on its own client's token", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: agentCC,
			wantSource: SourceClientCredentials, wantID: "client_credentials:app-1:joe", wantFetch: &joeRequest},
		{name: "client credentials: an agent without a sponsor needs none", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: orphanCC,
			wantSource: SourceClientCredentials, wantID: "client_credentials:app-1:joe", wantFetch: &joeRequest},
		{name: "client credentials: a person is untouched", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: humanCC,
			wantErr: "MCP server github needs your own keycloak sign-in and you have not connected. Run straza connect github, or sign in on your credentials page at " + page},
		{name: "client credentials: a person keeps their own sign-in", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: humanCC,
			users: map[string]row{"alice": {Secret: ok, Present: true}}, wantSource: SourceOwn},
		{name: "client credentials: an agent's stored sign-in wins", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: agentCC,
			users: map[string]row{"joe": {Secret: ok, Present: true}}, wantSource: SourceOwn},
		{name: "client credentials: an agent's expired sign-in denies and nothing is fetched", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: agentCC,
			users:   map[string]row{"joe": {Present: true, ExpiresAt: &past}},
			wantErr: "your sign-in for app github expired on " + day + " (as it was recorded). Sign in again on your credentials page at " + page},
		{name: "client credentials: the sponsor's sign-in is never used", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: agentCC,
			users:      map[string]row{"alice": {Secret: sponsorOK, Present: true, AllowAgents: true}},
			wantSource: SourceClientCredentials, wantID: "client_credentials:app-1:joe", wantFetch: &joeRequest},
		{name: "client credentials: a refusal passes through whole", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: agentCC,
			tokensErr: errors.New(refused), wantFetch: &joeRequest, wantErr: refused},
		{name: "client credentials on a command runtime never resolves", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeCommand, caller: agentCC,
			wantErr: "app github runs as one command process and cannot carry a credential per caller. Its manifest must use kind static"},
		{name: "client credentials without the lane denies", kind: CredentialOAuth, agents: AgentsClientCredentials, runtime: RuntimeRemote, caller: agentCC, noLane: true,
			wantErr: "agent joe-java-developer-agent could not get a github token, because this strazad started without its client credentials lane. An administrator reads the strazad log from its start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secrets := &tableSecrets{shared: map[string]*Secret{}, users: map[string]map[string]UserCredential{}}
			if tc.shared != nil {
				secrets.shared["app-1"] = tc.shared
			}
			secrets.users["app-1"] = tc.users
			tokens := &tableTokens{err: tc.tokensErr}
			opts := Options{Store: testStore(t), Secrets: secrets, HealthInterval: time.Hour, ConnectPageURL: page, ClientTokens: tokens}
			if tc.noPage {
				opts.ConnectPageURL = ""
			}
			if tc.noLane {
				opts.ClientTokens = nil
			}
			mgr := New(opts)
			mgr.byName["github"] = &instance{
				app:      store.App{ID: "app-1", Name: "github"},
				manifest: callerManifest(tc.kind, tc.agents, tc.runtime),
				runtime:  NewRemoteRuntime("github", RemoteSpec{URL: "http://127.0.0.1:9/mcp"}, nil, nil),
			}
			got, err := mgr.Credential(context.Background(), "github", tc.caller)
			switch {
			case tc.wantFetch == nil && len(tokens.requests) != 0:
				t.Errorf("a token was asked for on a path that must never ask: %+v", tokens.requests)
			case tc.wantFetch != nil && (len(tokens.requests) != 1 || tokens.requests[0] != *tc.wantFetch):
				t.Errorf("token requests = %+v, want exactly %+v", tokens.requests, *tc.wantFetch)
			}
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if tc.noPage && strings.Contains(err.Error(), " at ") {
					t.Errorf("a bare manager rendered a page URL: %v", err)
				}
				if got.Secret != nil {
					t.Errorf("a deny handed out a secret: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Credential: %v", err)
			}
			if got.Source != tc.wantSource || got.Owner != tc.wantOwner {
				t.Errorf("resolved source %q owner %q, want %q %q", got.Source, got.Owner, tc.wantSource, tc.wantOwner)
			}
			if tc.wantSource != "" && got.Secret == nil {
				t.Error("resolved without a secret")
			}
			if tc.wantID != "" && (got.Secret.ID != tc.wantID || got.Secret.Value != "client-tok-1") {
				t.Errorf("secret = %s with value %q, want %s with the fetched token", got.Secret.ID, got.Secret.Value, tc.wantID)
			}
		})
	}
}

// callerManifest builds a manifest struct directly, bypassing Parse, so the
// resolver's own runtime check is exercised on combinations Parse refuses.
func callerManifest(kind, agents, runtime string) Manifest {
	m := Manifest{
		APIVersion: APIVersion, Kind: "App", Metadata: Metadata{Name: "github"},
		Server: map[string]any{"name": "straza.test/github", "version": "1.0.0"},
		Straza: Extensions{Runtime: RuntimeSpec{Kind: runtime}},
	}
	switch runtime {
	case RuntimeRemote:
		m.Straza.Runtime.Remote = &RemoteSpec{URL: "http://127.0.0.1:9/mcp"}
	case RuntimeCommand:
		m.Straza.Runtime.Command = &CommandSpec{Exec: "npx"}
	case RuntimeOCI:
		m.Straza.Runtime.OCI = &OCISpec{Image: "example/mcp:1"}
	}
	if kind != CredentialNone {
		m.Straza.Credential = &CredentialSpec{Kind: kind, Agents: agents, Inject: &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}}
		if kind == CredentialOAuth {
			m.Straza.Credential.OAuth = &OAuthSpec{Provider: "keycloak"}
		}
	}
	return m
}

// TestProbeWithRefusesProcessRuntime: the paste-time probe exists for
// remote servers only; a process runtime answers the one-identity sentence.
func TestProbeWithRefusesProcessRuntime(t *testing.T) {
	mgr, _ := testManager(t)
	spec := helperSpec(t)
	mgr.byName["proc"] = &instance{
		app:      store.App{ID: "app-2", Name: "proc"},
		manifest: helperManifest(t, "proc", nil),
		runtime:  NewCommandRuntime("proc", spec, nil, NewRing(8)),
	}
	err := mgr.ProbeWith(context.Background(), "proc", &Secret{ID: "probe", Value: "x"})
	if err == nil || !strings.Contains(err.Error(), "runs as one process and cannot take a credential per caller") {
		t.Fatalf("ProbeWith on a command runtime: err = %v", err)
	}
}

// staticRemoteManifest builds a validated remote manifest whose credential is
// the server's own static secret, injected as a header.
func staticRemoteManifest(t *testing.T, name, url string) Manifest {
	t.Helper()
	mf, err := Parse([]byte("apiVersion: straza.dev/v1beta1\n" +
		"kind: App\n" +
		"metadata: {name: " + name + "}\n" +
		"server: {name: straza.test/" + name + ", version: \"1.0.0\"}\n" +
		"straza:\n" +
		"  runtime:\n" +
		"    kind: remote\n" +
		"    remote: {url: " + url + "}\n" +
		"  credential:\n" +
		"    kind: static\n" +
		"    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n"))
	if err != nil {
		t.Fatalf("static remote manifest invalid: %v", err)
	}
	return mf
}

// TestCredentiallessHealthReason pins the health reason of a server whose
// manifest takes a secret that nobody has set. The operator reads one plain
// sentence, the same one after the install probe and after the liveness ping,
// because the reason is printed straight after "Health: degraded," and in the
// REASON column of apps list, where a package name or a probe phase would
// read as noise.
func TestCredentiallessHealthReason(t *testing.T) {
	mgr, _ := testManager(t)
	ctx := context.Background()
	const want = "requires a credential and none is stored"

	if _, err := mgr.Install(ctx, staticRemoteManifest(t, "scout-tools", "http://127.0.0.1:9/mcp"), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if got := waitStatus(t, mgr, "scout-tools", StatusDegraded).Detail; got != want {
		t.Errorf("reason after the install probe = %q, want %q", got, want)
	}
	pinged, ok := mgr.HealthCheckOne(ctx, "scout-tools")
	if !ok {
		t.Fatal("HealthCheckOne: an installed app reported not managed")
	}
	if pinged.Detail != want {
		t.Errorf("reason after the liveness ping = %q, want %q", pinged.Detail, want)
	}
}
