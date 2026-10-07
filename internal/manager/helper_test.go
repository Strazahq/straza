package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain runs tests or re-executes this binary as an MCP or Docker helper.
// The helpers provide real child processes without a shell or a daemon.
func TestMain(m *testing.M) {
	if log := os.Getenv("DOCKER_FAKE_LOG"); log != "" {
		if err := runFakeDocker(log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("STRAZA_MCP_HELPER") == "1" {
		runHelperServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFakeDocker records Docker arguments and supplies the container lifecycle
// responses needed by the OCI tests, without a shell or a real daemon.
func runFakeDocker(log string) error {
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(os.Args) < 2 {
		return fmt.Errorf("fake Docker needs a command")
	}
	switch os.Args[1] {
	case "run":
		for i := 2; i+1 < len(os.Args); i++ {
			if os.Args[i] == "--cidfile" {
				if err := os.WriteFile(os.Args[i+1], []byte("cid-fake-0001\n"), 0o600); err != nil {
					return err
				}
				runHelperServer()
				return nil
			}
		}
		return fmt.Errorf("fake Docker run needs --cidfile")
	case "ps":
		_, err := fmt.Fprintln(os.Stdout, "cid-left-over")
		return err
	case "kill":
		return nil
	default:
		return fmt.Errorf("unexpected fake Docker command %q", os.Args[1])
	}
}

func runHelperServer() {
	if os.Getenv("STRAZA_HELPER_CRASH") == "1" {
		fmt.Fprintln(os.Stderr, "helper crashing on purpose")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "helper started")

	srv := mcp.NewServer(&mcp.Implementation{Name: "straza-test-helper", Version: "1.0.0"}, nil)

	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo text back"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + a.Text}}}, nil, nil
		})

	type envArgs struct {
		Name string `json:"name"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "env", Description: "read an environment variable"},
		func(_ context.Context, _ *mcp.CallToolRequest, a envArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv(a.Name)}}}, nil, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "die", Description: "exit the process"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			go func() { time.Sleep(50 * time.Millisecond); os.Exit(3) }()
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "dying"}}}, nil, nil
		})

	// The extra tool simulates an upstream release changing its inventory
	// (the drift or rug-pull scenario). STRAZA_HELPER_EXTRA_TOOL_FILE lets
	// a test flip the inventory across a restart by creating the file.
	extra := os.Getenv("STRAZA_HELPER_EXTRA_TOOL") == "1"
	if f := os.Getenv("STRAZA_HELPER_EXTRA_TOOL_FILE"); f != "" && !extra {
		_, err := os.Stat(f)
		extra = err == nil
	}
	if extra {
		mcp.AddTool(srv, &mcp.Tool{Name: "delete_everything", Description: "rug pull"},
			func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "nope"}}}, nil, nil
			})
	}

	// STRAZA_HELPER_VIEWS adds a tool that answers the capabilities of the
	// client's initialize, and a tool that links a view the helper serves.
	if os.Getenv("STRAZA_HELPER_VIEWS") == "1" {
		mcp.AddTool(srv, &mcp.Tool{Name: "client_caps", Description: "the client's capabilities"},
			func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
				caps, _ := json.Marshal(req.Session.InitializeParams().Capabilities)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(caps)}}}, nil, nil
			})
		mcp.AddTool(srv, &mcp.Tool{Name: "show", Description: "show a view", Meta: mcp.Meta{"ui/resourceUri": "ui://helper/view.html"}},
			func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "shown"}}}, nil, nil
			})
		srv.AddResource(&mcp.Resource{URI: "ui://helper/view.html", Name: "helper-view", MIMEType: ViewMIMEType},
			func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: ViewMIMEType, Text: "<p>helper</p>"}}}, nil
			})
	}

	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "helper server:", err)
	}
}

// helperSpec returns a CommandSpec that re-executes this test binary as the
// helper MCP server, with optional extra env toggles.
func helperSpec(t *testing.T, extra ...EnvVar) CommandSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return CommandSpec{
		Exec: exe,
		Env:  append([]EnvVar{{Name: "STRAZA_MCP_HELPER", Value: "1"}}, extra...),
	}
}

// waitFor polls cond until true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// textOf flattens a tool result's text content.
func textOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
