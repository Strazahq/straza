package manager

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// TestListingWithholdsCredentialEcho pins the inventory and the views of a
// server whose listing holds the credential Straza injects for it: a tool
// whose description holds it is not listed, a view whose document holds it
// is not served, and the tool that links that view is listed without the
// link. Each is logged at Warn with the server and the tool or the view's
// address and never the value, the tool once however many probes see it.
// The clean tool and view are served as before.
func TestListingWithholdsCredentialEcho(t *testing.T) {
	t.Parallel()
	const secret = "sk-view-4471-NEVER-LEAK"
	srv := mcp.NewServer(&mcp.Implementation{Name: "leaky-listing", Version: "1.0.0"}, nil)
	addViewTool(srv, "plain", uiLink("ui://leaky/ok.html"))
	addViewTool(srv, "show", uiLink("ui://leaky/bad.html"))
	mcp.AddTool(srv, &mcp.Tool{Name: "debug", Description: "Calls the API with Authorization: Bearer " + secret},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, nil
		})
	addView(srv, &mcp.Resource{URI: "ui://leaky/ok.html", Name: "ok", MIMEType: ViewMIMEType},
		&mcp.ResourceContents{URI: "ui://leaky/ok.html", MIMEType: ViewMIMEType, Text: "<p>ok</p>"})
	addView(srv, &mcp.Resource{URI: "ui://leaky/bad.html", Name: "bad", MIMEType: ViewMIMEType},
		&mcp.ResourceContents{URI: "ui://leaky/bad.html", MIMEType: ViewMIMEType, Text: `<script>const key = "` + secret + `"</script>`})
	up := newViewsUpstream(t, srv)

	mf, err := Parse([]byte("apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: leaky}\n" +
		"server: {name: straza.test/leaky, version: \"1.0.0\"}\nstraza:\n  runtime:\n    kind: remote\n    remote: {url: " + up.URL + "}\n" +
		"  credential:\n    kind: static\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n" +
		"  exposure:\n    tools: [\"*\"]\n    views: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	secrets := &fakeSecrets{byApp: map[string]*Secret{}}
	mgr := New(Options{Store: testStore(t), Secrets: secrets, HealthInterval: time.Hour, AllowLoopbackUpstreams: true,
		Log: slog.New(slog.NewTextHandler(logs, nil))})
	t.Cleanup(mgr.stopAll)
	ctx := context.Background()
	row, err := mgr.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	secrets.set(row.ID, &Secret{ID: "cred-1", Value: secret})
	mgr.SecretUpdated(ctx, row.ID)
	waitStatus(t, mgr, "leaky", StatusRunning)
	mgr.HealthCheckOne(ctx, "leaky")
	v, _ := mgr.View("leaky")

	var names []string
	for _, tl := range v.Tools {
		names = append(names, tl.Name)
		if tl.Name == "show" && strings.Contains(fmt.Sprint(tl.Meta), "ui://leaky/bad.html") {
			t.Errorf("the tool show keeps its link to the withheld view: %v", tl.Meta)
		}
	}
	if !slices.Equal(names, []string{"plain", "show"}) {
		t.Errorf("listed tools = %v, want [plain show]", names)
	}
	if len(v.Views) != 1 || v.Views[0].URI != "ui://leaky/ok.html" {
		t.Errorf("served views = %v, want the clean view alone", v.Views)
	}
	out := logs.String()
	if strings.Contains(out, secret) {
		t.Fatalf("the log holds the credential:\n%s", out)
	}
	if n := strings.Count(out, "msg=\"manager: a tool of an MCP server is not listed"); n != 1 {
		t.Errorf("%d Warn lines for the withheld tool over two probes, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "tool=debug") || !strings.Contains(out, "uri=ui://leaky/bad.html") ||
		!strings.Contains(out, "reason=\"it holds the credential Straza injects for the server, and a view must not\"") {
		t.Errorf("the log does not name the withheld tool and view:\n%s", out)
	}
}

// TestDroppedToolNameStaysOutOfDrift pins a tool that was listed and is
// dropped once the credential rotates to a value its name holds: the drift
// it causes names it by the stand-in in the Warn lines, the degraded
// detail and the straza.apps.drift event, and never by its real name.
func TestDroppedToolNameStaysOutOfDrift(t *testing.T) {
	t.Parallel()
	const rotated = "sk7741neverleak"
	srv := mcp.NewServer(&mcp.Implementation{Name: "named-leak", Version: "1.0.0"}, nil)
	for _, name := range []string{"plain", "debug_" + rotated} {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: name},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return nil, nil, nil
			})
	}
	up := newViewsUpstream(t, srv)
	mf, err := Parse([]byte("apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: named}\n" +
		"server: {name: straza.test/named, version: \"1.0.0\"}\nstraza:\n  runtime:\n    kind: remote\n    remote: {url: " + up.URL + "}\n" +
		"  credential:\n    kind: static\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	logs, sink := &lockedBuffer{}, &eventSink{}
	secrets := &fakeSecrets{byApp: map[string]*Secret{}}
	mgr := New(Options{Store: testStore(t), Secrets: secrets, Emit: sink.emit, HealthInterval: time.Hour, AllowLoopbackUpstreams: true,
		Log: slog.New(slog.NewTextHandler(logs, nil))})
	t.Cleanup(mgr.stopAll)
	ctx := context.Background()
	row, err := mgr.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	secrets.set(row.ID, &Secret{ID: "cred-1", Value: "sk-first-12345678"})
	mgr.SecretUpdated(ctx, row.ID)
	waitStatus(t, mgr, "named", StatusRunning)

	secrets.set(row.ID, &Secret{ID: "cred-1", Value: rotated})
	mgr.SecretUpdated(ctx, row.ID)
	v := waitStatus(t, mgr, "named", StatusDegraded)
	drift, ok := sink.find("straza.apps.drift")
	if !ok {
		t.Fatal("no straza.apps.drift event")
	}
	for what, text := range map[string]string{"the log": logs.String(), "the degraded detail": v.Detail, "the drift event": fmt.Sprint(drift.data)} {
		if strings.Contains(text, rotated) {
			t.Errorf("%s holds the credential: %s", what, text)
		}
	}
	if !strings.Contains(fmt.Sprint(drift.data["removed"]), "(a tool whose name holds the credential)") {
		t.Errorf("the drift event's removed list = %v, want the stand-in", drift.data["removed"])
	}
}
