package objects

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// newStore opens a migrated sqlite store in the test's temp directory.
func newStore(t *testing.T) store.Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "objects.db")
	storetest.SeedSQLite(t, dsn)
	s, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: dsn}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// demoSpec is the pinned block of the objects-demo server.
func demoSpec() manager.ObjectsSpec {
	return manager.ObjectsSpec{Type: "repo", List: manager.ObjectListSpec{Tool: "repo_list",
		Attributes: []manager.ObjectAttribute{{Name: "area", SetBy: "org"}}}}
}

// manifestJSON is a stored manifest of a remote server named name that
// pins spec, with a static credential when static is set.
func manifestJSON(t *testing.T, name string, spec *manager.ObjectsSpec, static bool) string {
	t.Helper()
	m := manager.Manifest{APIVersion: manager.APIVersion, Kind: "App", Metadata: manager.Metadata{Name: name},
		Server: map[string]any{"name": "straza.test/" + name, "version": "1.0.0"},
		Straza: manager.Extensions{Runtime: manager.RuntimeSpec{Kind: manager.RuntimeRemote,
			Remote: &manager.RemoteSpec{URL: "http://objects-demo:3002/mcp"}}, Objects: spec}}
	if static {
		m.Straza.Credential = &manager.CredentialSpec{Kind: manager.CredentialStatic,
			Inject: &manager.InjectSpec{As: manager.InjectHeader, Name: "Authorization", Template: "Bearer {{secret}}"}}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// installApp stores a server row with the manifest manifestJSON builds.
func installApp(t *testing.T, st store.Store, name string, spec *manager.ObjectsSpec, static bool) store.App {
	t.Helper()
	app, err := st.Apps().Create(context.Background(), store.App{Name: name, RuntimeKind: "remote", Manifest: manifestJSON(t, name, spec, static)})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// item is one listed object as a server sends it.
func item(value string, id any, area any) map[string]any {
	m := map[string]any{"value": value, "title": value}
	if id != nil {
		m["id"] = id
	}
	if area != nil {
		m["attributes"] = map[string]any{"area": area}
	}
	return m
}

// page is one structured answer of the listing tool.
func page(next string, items ...map[string]any) *mcp.CallToolResult {
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	sc := map[string]any{"objects": list}
	if next != "" {
		sc["nextCursor"] = next
	}
	return &mcp.CallToolResult{StructuredContent: sc}
}

// listingCall is one call the fake caller got.
type listingCall struct {
	app    string
	args   string
	secret *manager.Secret
}

// fakeCaller is the manager as a listing sees it: each server's live tools
// or the error its inventory answers, and the pages its listing tool
// answers, by cursor.
type fakeCaller struct {
	mu      sync.Mutex
	tools   map[string][]*mcp.Tool
	invErrs map[string]error
	pages   map[string]map[string]*mcp.CallToolResult
	errs    map[string]error
	block   bool
	calls   []listingCall
	release chan struct{}
}

func newCaller() *fakeCaller {
	return &fakeCaller{tools: map[string][]*mcp.Tool{}, invErrs: map[string]error{},
		pages: map[string]map[string]*mcp.CallToolResult{}, errs: map[string]error{}}
}

// serve gives app a read-only repo_list answering pages, by cursor.
func (f *fakeCaller) serve(app string, pages map[string]*mcp.CallToolResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools[app] = []*mcp.Tool{{Name: "repo_list", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, {Name: "repo_read"}}
	f.pages[app] = pages
}

func (f *fakeCaller) LiveInventory(app string) ([]*mcp.Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.invErrs[app]; err != nil {
		return nil, err
	}
	tools, ok := f.tools[app]
	if !ok {
		return nil, manager.ErrNotReady
	}
	return tools, nil
}

func (f *fakeCaller) CallListing(ctx context.Context, app, _ string, args json.RawMessage, secret *manager.Secret) (*mcp.CallToolResult, error) {
	var a struct{ Cursor string }
	_ = json.Unmarshal(args, &a)
	f.mu.Lock()
	f.calls = append(f.calls, listingCall{app: app, args: string(args), secret: secret})
	res, err, block, release := f.pages[app][a.Cursor], f.errs[app], f.block, f.release
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeSecrets holds listing secrets by app id.
type fakeSecrets map[string]*manager.Secret

func (s fakeSecrets) ListingSecret(appID string) *manager.Secret { return s[appID] }

// recorder keeps the records a Lists writes.
type recorder struct {
	mu   sync.Mutex
	recs []map[string]any
}

func (r *recorder) record(_ context.Context, data map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, data)
}

func (r *recorder) all() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.recs...)
}

// clock is a test clock moved by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rig is one replica's Lists over a shared store, caller and clock.
func rig(st store.Store, caller *fakeCaller, secrets fakeSecrets, rec *recorder, clk *clock, owner string) *Lists {
	l := New(Options{Store: st, Caller: caller, Secrets: secrets, Owner: owner, Record: rec.record})
	l.now = clk.now
	return l
}

// settle runs one tick of l and waits for the listings it started.
func settle(l *Lists) {
	l.tick(context.Background())
	l.wg.Wait()
}
