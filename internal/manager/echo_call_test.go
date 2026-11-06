package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/store"
)

// newHeaderUpstream is an in-process streamable-HTTP MCP server whose tool
// headers answers the Authorization header of its call and whose tool echo
// answers its text.
func newHeaderUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "header-upstream", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "headers", Description: "headers"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: req.Extra.Header.Get("Authorization")}}}, nil, nil
		})
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + a.Text}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	up := httptest.NewServer(handler)
	t.Cleanup(up.Close)
	return up
}

// headerManifest is a remote server at url with a static credential
// injected as "Authorization: Bearer <secret>".
func headerManifest(t *testing.T, url string) Manifest {
	t.Helper()
	m := Manifest{
		APIVersion: APIVersion, Kind: "App", Metadata: Metadata{Name: "parked"},
		Server: map[string]any{"name": "straza.test/parked", "version": "1.0.0"},
		Straza: Extensions{
			Runtime:    RuntimeSpec{Kind: RuntimeRemote, Remote: &RemoteSpec{URL: url}},
			Credential: &CredentialSpec{Kind: CredentialStatic, Inject: &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}},
		},
	}
	raw, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("header manifest invalid: %v", err)
	}
	return parsed
}

// TestCallWithholdsCredentialEcho pins that a reply holding the credential
// Straza injected is withheld on every runtime that injects one: the header
// of a remote server's call, and the environment a command or oci process
// was started with, which is checked even when the call resolved another
// credential row, since the process holds the value it started with. A
// reply that holds no credential passes.
func TestCallWithholdsCredentialEcho(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The oci case runs this test binary as docker, which serves the helper
	// MCP server when STRAZA_MCP_HELPER=1 is in its environment.
	old := DefaultDockerBin
	DefaultDockerBin = exe
	t.Cleanup(func() { DefaultDockerBin = old })
	up := newHeaderUpstream(t)
	spawned := &Secret{ID: "cred-app", Value: "tok-spawned-8827"}
	other := &Secret{ID: "cred-role", Value: "tok-role-row-4411"}
	args := func(v map[string]string) json.RawMessage { return mustJSON(t, v) }
	rows := []struct {
		name         string
		mf           Manifest
		call         *Secret
		echo, clean  string
		echoArgs     json.RawMessage
		cleanArgs    json.RawMessage
		cleanAnswers string
	}{
		{"remote header", headerManifest(t, up.URL), spawned,
			"headers", "echo", nil, args(map[string]string{"text": "hi"}), "echo: hi"},
		{"command environment", parkedManifest(t, RuntimeCommand), other,
			"env", "env", args(map[string]string{"name": "TOKEN"}), args(map[string]string{"name": "STRAZA_MCP_HELPER"}), "1"},
		{"oci environment", parkedManifest(t, RuntimeOCI), other,
			"env", "env", args(map[string]string{"name": "TOKEN"}), args(map[string]string{"name": "STRAZA_MCP_HELPER"}), "1"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			secrets := &fakeSecrets{byApp: map[string]*Secret{}}
			mgr := New(Options{Store: testStore(t), Secrets: secrets, HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
			t.Cleanup(mgr.stopAll)
			app, err := mgr.Install(ctx, row.mf, store.AppSourceAPI)
			if err != nil {
				t.Fatal(err)
			}
			secrets.set(app.ID, spawned)
			mgr.SecretUpdated(ctx, app.ID)
			waitStatus(t, mgr, "parked", StatusRunning)

			res, err := mgr.Call(ctx, "parked", row.echo, row.echoArgs, row.call, nil)
			if !errors.Is(err, ErrCredentialEcho) || res != nil {
				t.Fatalf("a reply holding the injected credential = %v, %v, want no result and ErrCredentialEcho", res, err)
			}
			res, err = mgr.Call(ctx, "parked", row.clean, row.cleanArgs, row.call, nil)
			if err != nil {
				t.Fatalf("a clean reply = %v", err)
			}
			if got := textOf(res); got != row.cleanAnswers {
				t.Errorf("a clean reply reads %q, want %q", got, row.cleanAnswers)
			}
		})
	}
}
