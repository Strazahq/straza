package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/server"
	"github.com/strazahq/straza/internal/store"
)

// strazad is one in-process platform instance driven over its real HTTP
// surface. The harness holds a second store handle (control-plane seeding
// only; the request paths under test never see it) and a TokenService over
// the same signing keys so it can mint valid ID tokens in bulk without
// paying a bcrypt login per virtual session.
type strazad struct {
	base    string
	cfg     config.Config
	st      store.Store
	tokens  *authn.TokenService
	cancel  context.CancelFunc
	done    chan error
	started time.Duration // New→/healthz-200 (the cold-start budget metric)
}

type strazadOpts struct {
	dataDir string
	natsURL string // non-empty: external NATS with a client listener (push path)
	// pgDSN switches the store to Postgres (the multi-pod topology; SQLite
	// is single-pod by design). issuer overrides PublicURL so pods share one
	// token identity, exactly like production pods behind one LB.
	pgDSN  string
	issuer string
}

// bootStrazad starts a standalone strazad on an ephemeral loopback port and
// waits for /healthz. The measured New→serving time is kept for the
// cold-start probe.
func bootStrazad(ctx context.Context, o strazadOpts) (*strazad, error) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	base := "http://" + addr

	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: o.dataDir,
		Server:  config.Server{Listen: addr, PublicURL: base},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: o.dataDir + "/straza.db"},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
		Apps: config.Apps{HealthInterval: time.Hour, AllowLoopbackUpstreams: true},
	}
	if o.natsURL != "" {
		cfg.Events = config.Events{Embedded: false, URL: o.natsURL}
	}
	if o.pgDSN != "" {
		cfg.Store = config.Store{Driver: config.DriverPostgres, DSN: o.pgDSN}
	}
	if o.issuer != "" {
		cfg.Server.PublicURL = o.issuer
	}

	runCtx, cancel := context.WithCancel(ctx)
	start := time.Now()
	app, err := server.New(runCtx, cfg, logging.New(cfg.Log, io.Discard))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("server.New: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			return nil, fmt.Errorf("strazad not serving within 10s")
		}
		time.Sleep(2 * time.Millisecond)
	}
	served := time.Since(start)

	st, err := store.Open(cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	// Harness-minted tokens carry the pod's effective issuer (the shared one
	// when a multi-pod probe overrode it) so they verify on every pod.
	tokens, err := authn.NewTokenService(runCtx, st.SigningKeys(), cfg.Server.PublicURL, 0)
	if err != nil {
		cancel()
		return nil, err
	}
	return &strazad{base: base, cfg: cfg, st: st, tokens: tokens, cancel: cancel, done: done, started: served}, nil
}

func (w *strazad) stop() {
	w.cancel()
	select {
	case <-w.done:
	case <-time.After(15 * time.Second):
	}
	if w.st != nil {
		_ = w.st.Close()
	}
}

// seedUsers creates n users holding role, which installEchoApp created as
// the echo app's own role, or which is created here on demand. Direct store
// writes are safe here: they happen before the server resolves any of these
// users, so no resolver cache is stale.
func (w *strazad) seedUsers(ctx context.Context, n int, roleName string) ([]store.User, error) {
	role, err := w.st.Roles().GetByName(ctx, roleName)
	if err != nil {
		// Application kind: a policy matches this role, and spec/policyset
		// revision 16 lets only application-kind roles appear in match.roles.
		// The store would default "" to business, which the activation gate
		// refuses. It belongs to no server, so it reaches no tool.
		role, err = w.st.Roles().Create(ctx, store.Role{Name: roleName, Kind: store.RoleKindApplication})
		if err != nil {
			return nil, err
		}
	}
	users := make([]store.User, 0, n)
	for i := 0; i < n; i++ {
		u, err := w.st.Users().Create(ctx, store.User{
			Username: fmt.Sprintf("load-%05d", i), Email: fmt.Sprintf("load-%05d@load.test", i),
		})
		if err != nil {
			return nil, err
		}
		if _, err := w.st.Roles().Assign(ctx, store.RoleAssignment{
			SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID,
		}); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// idToken mints a built-in-issuer ID token for a user (what the device flow
// would have produced), valid for checkin.
func (w *strazad) idToken(u store.User) (string, error) {
	return w.tokens.MintIDToken(u.ID, "straza", 10*time.Minute, u.Username, u.Email)
}

// adminIDToken creates one straza-admin user and mints its ID token for the
// /v1/admin surface.
func (w *strazad) adminIDToken(ctx context.Context) (string, error) {
	u, err := w.st.Users().Create(ctx, store.User{Username: "load-admin", Email: "load-admin@load.test"})
	if err != nil {
		return "", err
	}
	role, err := w.st.Roles().GetByName(ctx, server.AdminRole)
	if err != nil {
		return "", fmt.Errorf("bootstrap admin role missing: %w", err)
	}
	if _, err := w.st.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID,
	}); err != nil {
		return "", err
	}
	return w.tokens.MintIDToken(u.ID, "strazactl", time.Hour, u.Username, u.Email)
}

// applyPolicy PUTs and activates a PolicySet via the admin API, the real
// operator path, which also recompiles and signs the snapshot.
func (w *strazad) applyPolicy(adminToken, name, yaml string) error {
	req, _ := http.NewRequest(http.MethodPut, w.base+"/v1/admin/policies", strings.NewReader(yaml))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("apply policy: %d %s", resp.StatusCode, body)
	}

	req, _ = http.NewRequest(http.MethodPost, w.base+"/v1/admin/policies/"+name+"/activate", strings.NewReader(`{"status":"active"}`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("activate policy: %d %s", resp.StatusCode, body)
	}
	return nil
}

// installEchoApp installs a remote MCP app pointing at upstreamURL and
// creates roleName, which must begin with loadtest-, as the app's own role
// with every tool (admin API end to end). Seed the role's users after it,
// so seedUsers finds the role instead of making one of no server.
func (w *strazad) installEchoApp(adminToken, upstreamURL, roleName string) error {
	manifest := fmt.Sprintf(`apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: loadtest
  namespace: dev.straza.loadtest
  description: load-harness echo upstream
server:
  name: dev.straza.loadtest/echo
  description: echo upstream for the load harness
  version: "1.0.0"
straza:
  runtime:
    kind: remote
    remote:
      url: %s
  exposure:
    tools: ["*"]
`, upstreamURL)
	req, _ := http.NewRequest(http.MethodPost, w.base+"/v1/admin/apps", strings.NewReader(manifest))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("install app: %d %s", resp.StatusCode, body)
	}

	role, _ := json.Marshal(map[string]any{"name": roleName, "server": "loadtest", "tools": []string{"*"}})
	req, _ = http.NewRequest(http.MethodPost, w.base+"/v1/admin/roles", bytes.NewReader(role))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("create the app's role: %d %s", resp.StatusCode, body)
	}
	return nil
}

// checkin performs one POST /v1/checkin and returns (session_id, token).
func (w *strazad) checkin(client *http.Client, idToken string) (string, string, error) {
	body, _ := json.Marshal(map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:load"}},
	})
	resp, err := client.Post(w.base+"/v1/checkin", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("checkin: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		SessionID    string `json:"session_id"`
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.SessionToken == "" {
		return "", "", fmt.Errorf("checkin: bad body %s", raw)
	}
	return out.SessionID, out.SessionToken, nil
}

// refreshSession re-checks-in with a session token, what every real client
// does before the 300 s TTL, returning the replacement token.
func (w *strazad) refreshSession(client *http.Client, sessionToken string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"session_token": sessionToken,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:load"}},
	})
	resp, err := client.Post(w.base+"/v1/checkin", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.SessionToken == "" {
		return "", fmt.Errorf("refresh: bad body %s", raw)
	}
	return out.SessionToken, nil
}

// startEchoUpstream serves a hermetic MCP server (streamable HTTP) with one
// near-zero-latency echo tool. Returns base URL and a shutdown func.
func startEchoUpstream() (string, func(), error) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "load-upstream", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
		})
	// gate is echo under a different name so the fleet probe can put an
	// approval-mode rule on one tool while echo stays a plain allow.
	mcp.AddTool(srv, &mcp.Tool{Name: "gate", Description: "approval-gated echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = hs.Shutdown(ctx)
		cancel()
	}
	return "http://" + ln.Addr().String(), stop, nil
}

// newLoadClient returns an HTTP client tuned for a high-connection-reuse
// closed-loop load (default transports cap idle conns per host at 2).
func newLoadClient(maxConns int) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        maxConns,
		MaxIdleConnsPerHost: maxConns,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}
