package manager

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// TestNullListEntriesAreLeftOut pins that a server whose tool list or
// resource list holds a JSON null beside its entries is read as the entries
// alone: the install and the next health check both end, the server reads
// running, and its tool and its view are served.
func TestNullListEntriesAreLeftOut(t *testing.T) {
	const uri = "ui://nulls/card.html"
	srv := mcp.NewServer(&mcp.Implementation{Name: "nulls", Version: "1.0.0"}, nil)
	addView(srv, &mcp.Resource{URI: uri, Name: "card", MIMEType: ViewMIMEType}, &mcp.ResourceContents{URI: uri, MIMEType: ViewMIMEType, Text: "<p>card</p>"})
	addViewTool(srv, "pick", uiLink(uri))
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			switch list := res.(type) {
			case *mcp.ListToolsResult:
				return &mcp.ListToolsResult{Tools: append([]*mcp.Tool{nil}, append(list.Tools, nil)...)}, err
			case *mcp.ListResourcesResult:
				return &mcp.ListResourcesResult{Resources: append([]*mcp.Resource{nil}, append(list.Resources, nil)...)}, err
			}
			return res, err
		}
	})
	up := newViewsUpstream(t, srv)
	mgr, sink, _ := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "nulls", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "nulls", StatusRunning)
	mgr.HealthCheck(ctx)
	v, _ := mgr.View("nulls")
	if v.Status != StatusRunning || len(v.Tools) != 1 || v.Tools[0].Name != "pick" || len(v.Views) != 1 || v.Views[0].URI != uri {
		t.Errorf("status = %s (%s) with %d tools and %d views, want a running server with the tool pick and its view", v.Status, v.Detail, len(v.Tools), len(v.Views))
	}
	if got := sink.count("straza.apps.drift"); got != 0 {
		t.Errorf("%d drift events, want none", got)
	}
}
