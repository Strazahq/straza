package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// listingRuntime records the one call it gets and answers a fixed result.
type listingRuntime struct {
	calls []CallInput
}

func (r *listingRuntime) Start(context.Context) error { return nil }
func (r *listingRuntime) Stop()                       {}
func (r *listingRuntime) Ready() bool                 { return true }
func (r *listingRuntime) Tools(context.Context, *Secret) ([]*mcp.Tool, error) {
	return nil, nil
}
func (r *listingRuntime) Call(_ context.Context, in CallInput) (*mcp.CallToolResult, error) {
	r.calls = append(r.calls, in)
	return &mcp.CallToolResult{StructuredContent: map[string]any{"objects": []any{}}}, nil
}
func (r *listingRuntime) Resources(context.Context, *Secret) ([]*mcp.Resource, error) {
	return nil, nil
}
func (r *listingRuntime) ReadResource(context.Context, *Secret, string) (*mcp.ReadResourceResult, error) {
	return nil, nil
}
func (r *listingRuntime) Ping(context.Context, *Secret) error { return nil }
func (r *listingRuntime) Logs(int) []string                   { return nil }

// TestListingNeedsSecret pins which servers take a listing credential: a
// remote runtime whose manifest names a credential, and no process runtime
// and no remote without one.
func TestListingNeedsSecret(t *testing.T) {
	cases := []struct {
		kind, runtime string
		want          bool
	}{
		{CredentialStatic, RuntimeRemote, true},
		{CredentialOAuth, RuntimeRemote, true},
		{CredentialToken, RuntimeRemote, true},
		{CredentialNone, RuntimeRemote, false},
		{CredentialStatic, RuntimeCommand, false},
		{CredentialStatic, RuntimeOCI, false},
		{CredentialNone, RuntimeCommand, false},
	}
	for _, tc := range cases {
		if got := ListingNeedsSecret(callerManifest(tc.kind, "", tc.runtime)); got != tc.want {
			t.Errorf("ListingNeedsSecret(%s on %s) = %v, want %v", tc.kind, tc.runtime, got, tc.want)
		}
	}
}

// TestCallListing pins the listing call: a remote server that takes a
// credential gets exactly the listing secret and is never called without
// one, a remote without a credential and a process runtime get none, and a
// server that is not running is not called.
func TestCallListing(t *testing.T) {
	listing := &Secret{ID: "listing-1", Value: "tok-listing"}
	args := json.RawMessage(`{"cursor":"4"}`)
	cases := []struct {
		name       string
		kind       string
		runtime    string
		secret     *Secret
		wantSecret *Secret
		wantErr    string
	}{
		{name: "remote static carries the listing secret", kind: CredentialStatic, runtime: RuntimeRemote, secret: listing, wantSecret: listing},
		{name: "remote token carries the listing secret", kind: CredentialToken, runtime: RuntimeRemote, secret: listing, wantSecret: listing},
		{name: "remote static without one is refused", kind: CredentialStatic, runtime: RuntimeRemote,
			wantErr: "github has no listing credential, so Straza did not call repo_list. Add one with strazactl apps secret set github --listing"},
		{name: "remote without a credential carries none", kind: CredentialNone, runtime: RuntimeRemote, secret: listing},
		{name: "a command runtime carries none", kind: CredentialStatic, runtime: RuntimeCommand, secret: listing},
		{name: "a command runtime is called without one", kind: CredentialStatic, runtime: RuntimeCommand},
		{name: "an oci runtime carries none", kind: CredentialStatic, runtime: RuntimeOCI, secret: listing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _ := testManager(t)
			rt := &listingRuntime{}
			mgr.byName["github"] = &instance{app: store.App{ID: "app-1", Name: "github"},
				manifest: callerManifest(tc.kind, "", tc.runtime), runtime: rt}
			res, err := mgr.CallListing(context.Background(), "github", "repo_list", args, tc.secret)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if len(rt.calls) != 0 {
					t.Fatalf("the refused listing reached the server: %+v", rt.calls)
				}
				return
			}
			if err != nil || res == nil {
				t.Fatalf("CallListing = %v, %v", res, err)
			}
			if len(rt.calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(rt.calls))
			}
			got := rt.calls[0]
			if got.Tool != "repo_list" || string(got.Args) != string(args) || got.Secret != tc.wantSecret {
				t.Errorf("call = %+v, want repo_list with the cursor and secret %+v", got, tc.wantSecret)
			}
		})
	}

	mgr, _ := testManager(t)
	if _, err := mgr.CallListing(context.Background(), "absent", "repo_list", nil, listing); !errors.Is(err, ErrNotReady) {
		t.Errorf("listing a server that is not running = %v, want ErrNotReady", err)
	}
}

// TestLiveInventory pins the full live inventory the listing checks its
// tool against: every tool the server lists, the ones the exposure leaves
// out included, a not-running refusal for a server with no instance or no
// runtime, and the tool-list-unread refusal before the first probe.
func TestLiveInventory(t *testing.T) {
	mgr, _ := testManager(t)
	if _, err := mgr.LiveInventory("absent"); !errors.Is(err, ErrNotReady) || errors.Is(err, ErrToolsUnread) {
		t.Errorf("absent server = %v, want ErrNotReady only", err)
	}
	mf := callerManifest(CredentialNone, "", RuntimeRemote)
	mf.Straza.Exposure = &ExposureSpec{Tools: []string{"repo_read"}}
	mgr.byName["stopped"] = &instance{app: store.App{ID: "app-0", Name: "stopped"}, manifest: mf}
	if _, err := mgr.LiveInventory("stopped"); !errors.Is(err, ErrNotReady) || errors.Is(err, ErrToolsUnread) {
		t.Errorf("a server with no runtime = %v, want ErrNotReady only", err)
	}
	inst := &instance{app: store.App{ID: "app-1", Name: "github"}, manifest: mf, runtime: &listingRuntime{}}
	mgr.byName["github"] = inst
	_, err := mgr.LiveInventory("github")
	if !errors.Is(err, ErrToolsUnread) || errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "strazactl apps show github") {
		t.Errorf("before the first probe = %v, want ErrToolsUnread naming strazactl apps show github", err)
	}
	inst.inventory = []*mcp.Tool{{Name: "repo_list"}, {Name: "repo_read"}}
	inst.invFilled = true
	tools, err := mgr.LiveInventory("github")
	if err != nil || len(tools) != 2 || tools[0].Name != "repo_list" {
		t.Fatalf("inventory = %+v, %v; want both tools", tools, err)
	}
}
