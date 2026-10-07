package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// taggedUpstream is an MCP server whose one tool, whoami, answers its tag, so
// a call shows which address an instance runs against. It counts the
// sessions opened against it, since every instance that starts opens one.
type taggedUpstream struct {
	*httptest.Server
	sessions atomic.Int32
}

func newTaggedUpstream(t *testing.T, tag string) *taggedUpstream {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "tagged", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "names the upstream"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: tag}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	u := &taggedUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An initialize request is the only POST without a session id.
		if r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == "" {
			u.sessions.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

// twoReplicas builds two managers over one store, as two strazad replicas
// share one database. The sink records what replica b emits.
func twoReplicas(t *testing.T) (a, b *Manager, bEmits *eventSink) {
	t.Helper()
	st := testStore(t)
	bEmits = &eventSink{}
	a = New(Options{Store: st, Emit: (&eventSink{}).emit, HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
	b = New(Options{Store: st, Emit: bEmits.emit, HealthInterval: time.Hour, AllowLoopbackUpstreams: true})
	t.Cleanup(a.stopAll)
	t.Cleanup(b.stopAll)
	return a, b, bEmits
}

// TestStatusWritesKeepAnotherReplicasChange: replica b still runs the server
// as it was before replica a moved it to another address, and a status
// change on b, from a failed health probe or from a pause taken through b,
// writes the status only, so the stored row keeps a's change.
func TestStatusWritesKeepAnotherReplicasChange(t *testing.T) {
	cases := []struct {
		name string
		// transition changes the status of b's instance, which still runs
		// against the old address.
		transition func(ctx context.Context, b *Manager, old *taggedUpstream) error
		wantStatus string
	}{
		{name: "a failed health probe on b", wantStatus: StatusDegraded,
			transition: func(ctx context.Context, b *Manager, old *taggedUpstream) error {
				old.Close()
				b.HealthCheck(ctx)
				return nil
			}},
		{name: "a pause taken through b", wantStatus: StatusStopped,
			transition: func(ctx context.Context, b *Manager, _ *taggedUpstream) error {
				_, err := b.Disable(ctx, "tagged")
				return err
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b, _ := twoReplicas(t)
			ctx := context.Background()
			one, two := newTaggedUpstream(t, "one"), newTaggedUpstream(t, "two")
			if _, err := a.Install(ctx, remoteManifest(t, "tagged", one.URL), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
			if err := b.Load(ctx); err != nil {
				t.Fatal(err)
			}
			waitStatus(t, b, "tagged", StatusRunning)
			next := remoteManifest(t, "tagged", two.URL)
			next.Server["version"] = "2.0.0"
			if _, err := a.Install(ctx, next, store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}

			if err := tc.transition(ctx, b, one); err != nil {
				t.Fatal(err)
			}
			row, err := b.opts.Store.Apps().GetByName(ctx, "tagged")
			if err != nil {
				t.Fatal(err)
			}
			stored, err := FromJSON(row.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			if got := stored.Straza.Runtime.Remote.URL; got != two.URL || row.Version != "2.0.0" {
				t.Errorf("stored address %s version %s, want a's change to %s version 2.0.0 kept", got, row.Version, two.URL)
			}
			if row.Status != tc.wantStatus {
				t.Errorf("stored status %s, want %s", row.Status, tc.wantStatus)
			}
		})
	}
}
