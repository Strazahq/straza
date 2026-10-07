package manager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// upstreamEcho is an in-process streamable-HTTP MCP server that records the
// Authorization header of every request it receives.
type upstreamEcho struct {
	*httptest.Server
	mu      sync.Mutex
	headers []string
}

func newUpstreamEcho(t *testing.T) *upstreamEcho {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "upstream-echo", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + a.Text}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	u := &upstreamEcho{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.headers = append(u.headers, r.Header.Get("Authorization"))
		u.mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstreamEcho) sawAuth(value string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, h := range u.headers {
		if h == value {
			return true
		}
	}
	return false
}

func (u *upstreamEcho) sawAnyAuth() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, h := range u.headers {
		if h != "" {
			return true
		}
	}
	return false
}

// TestRemoteRuntimeRoundTripAndInjection pins that a proxied echo
// server round-trips and the injected header is present upstream.
func TestRemoteRuntimeRoundTripAndInjection(t *testing.T) {
	up := newUpstreamEcho(t)
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
	r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
	t.Cleanup(r.Stop)
	ctx := context.Background()

	secret := &Secret{ID: "cred-1", Value: "sk-test-123"}
	res, err := r.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "hi"}), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); got != "echo: hi" {
		t.Errorf("echo = %q", got)
	}
	if !up.sawAuth("Bearer sk-test-123") {
		t.Error("upstream did not receive the injected Authorization header")
	}

	tools, err := r.Tools(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Errorf("tools = %v", tools)
	}
	if err := r.Ping(ctx, secret); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func TestRemoteRuntimeFailsClosedWithoutSecret(t *testing.T) {
	up := newUpstreamEcho(t)
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
	r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
	t.Cleanup(r.Stop)

	_, err := r.Call(context.Background(), CallInput{Tool: "echo"})
	if err == nil {
		t.Fatal("credentialed app must refuse to call upstream without a resolved secret")
	}
	if err.Error() != "requires a credential and none is stored" {
		t.Errorf("refusal = %q, want the sentence the operator and the agent read", err)
	}
	if up.sawAnyAuth() {
		t.Error("no request should have carried a credential")
	}
}

func TestRemoteRuntimeUncredentialed(t *testing.T) {
	up := newUpstreamEcho(t)
	r := besideRuntime("echo", RemoteSpec{URL: up.URL}, nil)
	t.Cleanup(r.Stop)

	res, err := r.Call(context.Background(), CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "open"})})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(res); got != "echo: open" {
		t.Errorf("echo = %q", got)
	}
	if up.sawAnyAuth() {
		t.Error("uncredentialed app must not send an Authorization header")
	}
}

func TestRemoteRuntimePoolsPerCredential(t *testing.T) {
	up := newUpstreamEcho(t)
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "{{secret}}"}
	r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
	t.Cleanup(r.Stop)
	ctx := context.Background()

	for _, s := range []*Secret{{ID: "a", Value: "token-A"}, {ID: "b", Value: "token-B"}} {
		if _, err := r.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": s.ID}), Secret: s}); err != nil {
			t.Fatal(err)
		}
	}
	if !up.sawAuth("token-A") || !up.sawAuth("token-B") {
		t.Error("each credential must reach upstream under its own identity")
	}
	r.mu.Lock()
	pooled := len(r.pool)
	r.mu.Unlock()
	if pooled != 2 {
		t.Errorf("expected 2 pooled sessions, got %d", pooled)
	}
}

// TestRemoteRuntimeRotatedSecretTakesEffect pins that a rotated secret
// takes effect on the next call. The credential row ID SURVIVES rotation
// (broker.Set and SetGrant update in place), so a pool keyed by row ID
// alone would keep a reconnected OAuth grant or a rotated static secret on
// the stale session, with the OLD token on the wire until an upstream
// error evicted it. The pool key must fingerprint the secret value, and the
// superseded session must be closed, not leaked.
func TestRemoteRuntimeRotatedSecretTakesEffect(t *testing.T) {
	up := newUpstreamEcho(t)
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
	r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
	t.Cleanup(r.Stop)
	ctx := context.Background()

	call := func(value string) {
		t.Helper()
		if _, err := r.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": value}),
			Secret: &Secret{ID: "cred-1", Value: value}}); err != nil {
			t.Fatal(err)
		}
	}
	call("tok-v1")
	if !up.sawAuth("Bearer tok-v1") {
		t.Fatal("first token not injected")
	}
	// Same credential row, rotated value: the very next call must carry it.
	call("tok-v2")
	if !up.sawAuth("Bearer tok-v2") {
		t.Error("rotated token never reached upstream: stale pooled session reused")
	}
	r.mu.Lock()
	n := len(r.pool)
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("pool holds %d sessions for one credential, want 1 (superseded session must be closed)", n)
	}
}

// TestRemoteRuntimePerUserSemantics pins the per-user split: a per-user
// (oauth) app may list tools and ping anonymously (no app-level identity
// exists), but an uncredentialed tool CALL still fails closed and nothing
// anonymous reaches upstream from it.
func TestRemoteRuntimePerUserSemantics(t *testing.T) {
	up := newUpstreamEcho(t)
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
	r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
	r.PerUser = true
	t.Cleanup(r.Stop)
	ctx := context.Background()

	if _, err := r.Tools(ctx, nil); err != nil {
		t.Fatalf("per-user app inventory must run uncredentialed: %v", err)
	}
	if err := r.Ping(ctx, nil); err != nil {
		t.Fatalf("per-user app ping must run uncredentialed: %v", err)
	}
	if _, err := r.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "x"})}); err == nil {
		t.Fatal("per-user app must refuse an uncredentialed tool call")
	}
	if up.sawAnyAuth() {
		t.Error("no request should have carried a credential")
	}

	// With a grant, the call round-trips and injects that user's token.
	if _, err := r.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "hi"}),
		Secret: &Secret{ID: "grant-1", Value: "gho_u1"}}); err != nil {
		t.Fatal(err)
	}
	if !up.sawAuth("Bearer gho_u1") {
		t.Error("grant token not injected upstream")
	}
}

func TestRemoteRuntimePassthroughDisablesInjection(t *testing.T) {
	up := newUpstreamEcho(t)
	inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}
	r := besideRuntime("echo", RemoteSpec{URL: up.URL, Auth: AuthPassthrough}, inject)
	t.Cleanup(r.Stop)

	if _, err := r.Call(context.Background(), CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "x"})}); err != nil {
		t.Fatal(err)
	}
	if up.sawAnyAuth() {
		t.Error("passthrough (reserved, v1beta1 = no injection) must not inject")
	}
}

// TestClientCredentialsRenewalKeepsOneUpstreamSession: the secret of an
// agent's client token keeps one id per server and agent, so each renewed
// token closes the session of the one before it and a second agent has a
// session of its own.
func TestClientCredentialsRenewalKeepsOneUpstreamSession(t *testing.T) {
	up := newUpstreamEcho(t)
	tokens := &tableTokens{}
	mgr := New(Options{Store: testStore(t), Secrets: &tableSecrets{}, HealthInterval: time.Hour, ClientTokens: tokens})
	mf := callerManifest(CredentialOAuth, AgentsClientCredentials, RuntimeRemote)
	rt := besideRuntime("github", RemoteSpec{URL: up.URL, Auth: AuthInject}, mf.Straza.Credential.Inject)
	t.Cleanup(rt.Stop)
	mgr.byName["github"] = &instance{app: store.App{ID: "app-1", Name: "github"}, manifest: mf, runtime: rt}
	ctx := context.Background()

	call := func(c Caller) string {
		t.Helper()
		res, err := mgr.Credential(ctx, "github", c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rt.Call(ctx, CallInput{Tool: "echo", Args: mustJSON(t, map[string]string{"text": "hi"}), Secret: res.Secret}); err != nil {
			t.Fatal(err)
		}
		return res.Secret.Value
	}
	joe := Caller{UserID: "joe", User: "joe-java-developer-agent", Agent: true, Session: "ses-1"}
	for range 3 {
		if tok := call(joe); !up.sawAuth("Bearer " + tok) {
			t.Fatalf("the renewed token %s never reached the upstream", tok)
		}
	}
	rt.mu.Lock()
	n := len(rt.pool)
	rt.mu.Unlock()
	if n != 1 {
		t.Fatalf("three tokens of one agent left %d upstream sessions, want 1", n)
	}
	call(Caller{UserID: "joe2", User: "joe2", Agent: true, Session: "ses-2"})
	rt.mu.Lock()
	n = len(rt.pool)
	rt.mu.Unlock()
	if n != 2 {
		t.Fatalf("a second agent shares or replaced the first agent's session: %d sessions, want 2", n)
	}
}

// besideRuntime builds a remote runtime that may dial the loopback
// upstreams these tests run, as the standalone profile allows.
func besideRuntime(name string, spec RemoteSpec, inject *InjectSpec) *RemoteRuntime {
	r := NewRemoteRuntime(name, spec, inject, nil)
	r.AllowLoopback = true
	return r
}

// The class words of the dial refusal, after "because it is " or "because
// it resolves to <address>, ".
const (
	loopbackWords = "a loopback address on strazad's own host. An administrator publishes an address other machines can reach, " +
		"or sets apps.allowLoopbackUpstreams when strazad runs beside the server on purpose."
	metadataWords = "a cloud metadata address, which reaches the metadata service of strazad's cloud. An administrator publishes an address other machines can reach."
)

// TestRemoteRuntimeRefusesItsOwnHost pins the dial guard of a remote
// server: an address of a class a request from strazad should not reach is
// refused before a packet leaves, on inventory, ping and the token probe
// alike, with the sentence the health loop reports. A mapped, NAT64 or 6to4
// address reads as the IPv4 address it carries.
func TestRemoteRuntimeRefusesItsOwnHost(t *testing.T) {
	t.Parallel()
	unspecifiedWords := "an unspecified address, which strazad's own host answers. An administrator publishes an address other machines can reach."
	cases := []struct{ host, words string }{
		{"127.0.0.1", loopbackWords}, {"[::1]", loopbackWords}, {"[::ffff:127.0.0.1]", loopbackWords},
		{"[64:ff9b::7f00:1]", loopbackWords}, {"[2002:7f00:1::1]", loopbackWords},
		{"0.0.0.0", unspecifiedWords}, {"[::]", unspecifiedWords},
		{"[fe80::1]", "a link-local address on strazad's own network link. An administrator publishes an address other machines can reach."},
		{"169.254.169.254", metadataWords}, {"[::ffff:169.254.169.254]", metadataWords}, {"[fd00:ec2::254]", metadataWords},
		{"100.100.100.200", metadataWords}, {"[64:ff9b::a9fe:a9fe]", metadataWords}, {"[2002:a9fe:a9fe::1]", metadataWords},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			r := NewRemoteRuntime("far", RemoteSpec{URL: "http://" + tc.host + ":9/mcp"}, nil, nil)
			t.Cleanup(r.Stop)
			want := "Straza does not dial " + strings.Trim(tc.host, "[]") + " for the server far because it is " + tc.words
			if _, err := r.Tools(ctx, nil); err == nil || err.Error() != want || !errors.Is(err, ErrDialRefused) {
				t.Errorf("Tools err = %v\nwant %q matching ErrDialRefused", err, want)
			}
			if err := r.Ping(ctx, nil); err == nil || err.Error() != want {
				t.Errorf("Ping err = %v\nwant %q", err, want)
			}
			if _, err := r.Call(ctx, CallInput{Tool: "echo"}); err == nil || err.Error() != want {
				t.Errorf("Call err = %v\nwant %q", err, want)
			}
			if err := r.Probe(ctx, &Secret{ID: "probe", Value: "t"}); err == nil || err.Error() != want {
				t.Errorf("Probe err = %v\nwant %q", err, want)
			}
		})
	}
}

// TestRemoteRuntimeDialsPrivateAndChecksNames pins the two sides of the
// guard: a private, shared or public address passes the dial hook, so a
// server on the operator's network or a cluster address is dialed; a name
// is checked by the address it resolves to, in the hook, with a sentence
// that names both; and localhost names strazad's own host by definition.
func TestRemoteRuntimeDialsPrivateAndChecksNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewRemoteRuntime("far", RemoteSpec{URL: "http://localhost:9/mcp"}, nil, nil)
	t.Cleanup(r.Stop)
	for _, addr := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.10", "100.64.0.1", "fd12::1", "93.184.216.34", "2001:db8::1"} {
		if err := r.refuse(addr, netip.MustParseAddr(addr)); err != nil {
			t.Errorf("%s is refused, want it dialed: %v", addr, err)
		}
	}
	want := "Straza does not dial mcp.example for the server far because it resolves to 127.0.0.1, " + loopbackWords
	if err := r.refuse("mcp.example", netip.MustParseAddr("127.0.0.1")); err == nil || err.Error() != want {
		t.Errorf("a name resolving to loopback: err = %v\nwant %q", err, want)
	}
	want = "Straza does not dial 169.254.169.254 for the server far because it is " + metadataWords
	if _, err := r.dialContext(ctx, "tcp", "169.254.169.254:9"); err == nil || refusalOf(err) == nil || refusalOf(err).Error() != want {
		t.Errorf("the hook on a dial to the metadata address: err = %v\nwant %q", err, want)
	}
	want = "Straza does not dial localhost for the server far because it is a name for strazad's own host, as localhost and every name under .localhost are. " +
		"An administrator publishes an address other machines can reach, or sets apps.allowLoopbackUpstreams when strazad runs beside the server on purpose."
	if _, err := r.Tools(ctx, nil); err == nil || err.Error() != want {
		t.Errorf("localhost err = %v\nwant %q", err, want)
	}
}

// TestRemoteRuntimeLoopbackAllowance pins apps.allowLoopbackUpstreams: on,
// a loopback literal and a name that resolves to one are dialed, and the
// metadata class stays refused.
func TestRemoteRuntimeLoopbackAllowance(t *testing.T) {
	t.Parallel()
	up := newUpstreamEcho(t)
	port := hostPortOf(t, up.URL)
	for _, host := range []string{"127.0.0.1", "localhost"} {
		r := besideRuntime("near", RemoteSpec{URL: "http://" + host + ":" + port + "/mcp"}, nil)
		t.Cleanup(r.Stop)
		if tools, err := r.Tools(context.Background(), nil); err != nil || len(tools) != 1 {
			t.Errorf("%s with loopback allowed: tools = %v, err = %v, want the echo tool", host, tools, err)
		}
	}
	r := besideRuntime("far", RemoteSpec{URL: "http://169.254.169.254:9/mcp"}, nil)
	t.Cleanup(r.Stop)
	want := "Straza does not dial 169.254.169.254 for the server far because it is " + metadataWords
	if _, err := r.Tools(context.Background(), nil); err == nil || err.Error() != want {
		t.Errorf("metadata with loopback allowed: err = %v\nwant %q", err, want)
	}
}

// TestRemoteRuntimeWithAProxy pins the guard under a proxy of the
// environment. The dial to the proxy is not classed, since the proxy is
// operator config, so a name goes to the proxy unclassed even from a proxy
// on loopback while loopback is refused, a name outside ASCII in the form
// net/http dials. The manifest's host is classed before the dial: an
// address literal in any spelling a C resolver reads, a trailing dot
// included, and a name for strazad's own host in any case are refused
// before the proxy sees anything, and so are a host that is neither a name
// nor an address and an address with no host.
func TestRemoteRuntimeWithAProxy(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen = append(seen, req.Host)
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	through := func(name, host string, allow bool) error {
		r := NewRemoteRuntime(name, RemoteSpec{URL: "http://" + host + "/mcp"}, nil, nil)
		r.AllowLoopback = allow
		r.transport.Proxy = http.ProxyURL(proxyURL)
		t.Cleanup(r.Stop)
		_, err := r.Tools(context.Background(), nil)
		return err
	}
	passing := []struct {
		host  string
		allow bool
	}{{"mcp.example:9", true}, {"mcp.example:9", false}, {"foo.localhost:9", true}, {"host.docker.internal:9", false}, {"bücher.example:9", false}}
	for _, tc := range passing {
		if err := through("named", tc.host, tc.allow); err == nil || strings.HasPrefix(err.Error(), "Straza does not dial") {
			t.Errorf("%s with loopback allowed %v: err = %v, want the proxy's answer", tc.host, tc.allow, err)
		}
	}
	ownHost := "it is a name for strazad's own host, as localhost and every name under .localhost are. " +
		"An administrator publishes an address other machines can reach, or sets apps.allowLoopbackUpstreams when strazad runs beside the server on purpose."
	unreadable := "it is neither a DNS name nor an IP address as Straza reads them. An administrator publishes the address with a DNS name or an IP address."
	refused := []struct{ host, because string }{
		{"LOCALHOST:9", ownHost}, {"localhost.:9", ownHost}, {"foo.localhost:9", ownHost}, {"Foo.LocalHost.:9", ownHost},
		{"exU+202Eample.com:9", unreadable},
		{"169.254.169.254:9", "it is " + metadataWords},
		{"169.254.169.254.:9", "it is " + metadataWords},
		{"[::ffff:a9fe:a9fe]:9", "it is " + metadataWords},
		{"2852039166:9", "it resolves to 169.254.169.254, " + metadataWords},
		{"0xa9fea9fe:9", "it resolves to 169.254.169.254, " + metadataWords},
		{"0251.0376.0251.0376:9", "it resolves to 169.254.169.254, " + metadataWords},
		{"169.254.43518:9", "it resolves to 169.254.169.254, " + metadataWords},
		{"127.0.0.1.:9", "it is " + loopbackWords},
		{"2130706433:9", "it resolves to 127.0.0.1, " + loopbackWords},
		{"127.1:9", "it resolves to 127.0.0.1, " + loopbackWords},
		{"0x7f000001:9", "it resolves to 127.0.0.1, " + loopbackWords},
		{strings.Repeat("a", 64) + ":9", unreadable},
	}
	for _, tc := range refused {
		host := strings.Trim(strings.TrimSuffix(tc.host, ":9"), "[]")
		want := "Straza does not dial " + host + " for the server literal because " + tc.because
		// The sentence spells a character that prints nothing as its code point.
		if err := through("literal", strings.ReplaceAll(tc.host, "U+202E", "\u202e"), false); err == nil || err.Error() != want {
			t.Errorf("%s: err = %v\nwant %q", tc.host, err, want)
		}
	}
	want := "Straza does not dial the server literal because its address http:///mcp has no host. An administrator publishes the address with a DNS name or an IP address."
	if err := through("literal", "", false); err == nil || err.Error() != want {
		t.Errorf("an address with no host: err = %v\nwant %q", err, want)
	}
	// A password in the address never reaches a probe's connect error either.
	pr := NewRemoteRuntime("probe", RemoteSpec{URL: "http://svc:hunter2@127.0.0.1:9/mcp"}, nil, nil)
	pr.AllowLoopback = true
	pr.ConnectTimeout = 2 * time.Second
	t.Cleanup(pr.Stop)
	if err := pr.Probe(context.Background(), &Secret{ID: "s1", Value: "tok"}); err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "hunter2") || !strings.HasPrefix(err.Error(), "connect http://127.0.0.1:9/mcp: ") {
		t.Errorf("a probe with a password in the address: err = %v, want the address with no user information and no password", err)
	}
	// No user information of the address reaches the sentence.
	want = "Straza does not dial the server literal because its address http:///mcp has no host. An administrator publishes the address with a DNS name or an IP address."
	if err := through("literal", "svc:hunter2@", false); err == nil || err.Error() != want {
		t.Errorf("an address with a password and no host: err = %v\nwant %q", err, want)
	}
	mu.Lock()
	defer mu.Unlock()
	wantSeen := map[string]bool{"mcp.example:9": false, "foo.localhost:9": false, "host.docker.internal:9": false, "xn--bcher-kva.example:9": false}
	for _, host := range seen {
		if _, ok := wantSeen[host]; !ok {
			t.Errorf("the proxy saw a request for %s, want only the passing names", host)
		}
		wantSeen[host] = true
	}
	for host, saw := range wantSeen {
		if !saw {
			t.Errorf("the proxy saw no request for %s", host)
		}
	}
}

// hostPortOf answers the port of an httptest server's URL.
func hostPortOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}
