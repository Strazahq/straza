package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// viewsUpstream is an in-process streamable-HTTP MCP server that records the
// capabilities of every initialize it receives, raw, and counts the
// resources/read requests, so a test sees what Straza advertised and how
// often it read the views.
type viewsUpstream struct {
	*httptest.Server
	srv   *mcp.Server
	mu    sync.Mutex
	inits []string
	reads int
	// failReads answers every resources/read with HTTP 500 while set.
	failReads atomic.Bool
}

func newViewsUpstream(t *testing.T, srv *mcp.Server) *viewsUpstream {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	u := &viewsUpstream{srv: srv}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var msg struct {
				Method string `json:"method"`
				Params struct {
					Capabilities json.RawMessage `json:"capabilities"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &msg) == nil {
				u.mu.Lock()
				switch msg.Method {
				case "initialize":
					u.inits = append(u.inits, string(msg.Params.Capabilities))
				case "resources/read":
					u.reads++
				}
				u.mu.Unlock()
				if msg.Method == "resources/read" && u.failReads.Load() {
					http.Error(w, "the view store is down", http.StatusInternalServerError)
					return
				}
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *viewsUpstream) initializes() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string{}, u.inits...)
}

func (u *viewsUpstream) readCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reads
}

// The capabilities of Straza's upstream initialize, byte for byte: the SDK's
// default with views off, and the same plus the MCP Apps extension with
// views on.
const (
	capsViewsOff = `{"roots":{"listChanged":true}}`
	capsViewsOn  = `{"extensions":{"io.modelcontextprotocol/ui":{"mimeTypes":["text/html;profile=mcp-app"]}},"roots":{"listChanged":true}}`
)

// addViewTool adds a tool whose _meta is meta to srv.
func addViewTool(srv *mcp.Server, name string, meta mcp.Meta) {
	mcp.AddTool(srv, &mcp.Tool{Name: name, Description: name, Meta: meta},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name}}}, nil, nil
		})
}

// addView adds a listed resource at uri that answers contents.
func addView(srv *mcp.Server, res *mcp.Resource, contents ...*mcp.ResourceContents) {
	srv.AddResource(res, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: contents}, nil
	})
}

// uiLink is the _meta of a tool that links uri the current way.
func uiLink(uri string) mcp.Meta {
	return mcp.Meta{"ui": map[string]any{"resourceUri": uri}}
}

// viewsManifest builds a validated remote manifest for url with the given
// exposure globs and views switch.
func viewsManifest(t *testing.T, name, url string, tools string, views bool) Manifest {
	t.Helper()
	raw := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: " + name + "}\n" +
		"server: {name: straza.test/" + name + ", version: \"1.0.0\"}\n" +
		"straza:\n  runtime:\n    kind: remote\n    remote: {url: " + url + "}\n" +
		"  exposure:\n    tools: " + tools + "\n"
	if views {
		raw += "    views: true\n"
	}
	mf, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("views manifest invalid: %v", err)
	}
	return mf
}

// viewsManager is a manager with a log the test reads.
func viewsManager(t *testing.T) (*Manager, *eventSink, *lockedBuffer) {
	t.Helper()
	sink, logs := &eventSink{}, &lockedBuffer{}
	mgr := New(Options{Store: testStore(t), Emit: sink.emit, HealthInterval: time.Hour, AllowLoopbackUpstreams: true,
		Log: slog.New(slog.NewTextHandler(logs, nil))})
	t.Cleanup(mgr.stopAll)
	return mgr, sink, logs
}

// TestUpstreamInitializeAdvertisesViews pins Straza's own initialize to an
// upstream, captured raw: with views off it is the one Straza sent before
// the switch existed, and with views on it adds the MCP Apps extension and
// keeps the roots capability the SDK default carries.
func TestUpstreamInitializeAdvertisesViews(t *testing.T) {
	for _, tc := range []struct {
		name  string
		views bool
		want  string
	}{
		{"views off", false, capsViewsOff},
		{"views on", true, capsViewsOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newViewsUpstream(t, mcp.NewServer(&mcp.Implementation{Name: "up", Version: "1.0.0"}, nil))
			r := besideRuntime("up", RemoteSpec{URL: up.URL, Auth: AuthInject}, nil)
			r.Views = tc.views
			t.Cleanup(r.Stop)
			if _, err := r.Tools(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if got := up.initializes(); len(got) != 1 || got[0] != tc.want {
				t.Errorf("initialize capabilities = %q, want [%s]", got, tc.want)
			}
		})
	}
}

// TestCommandRuntimeAdvertisesViews: a command server started with views on
// sees the MCP Apps extension in its initialize, and one started with views
// off does not, so the stdio and container runtimes follow the switch too.
func TestCommandRuntimeAdvertisesViews(t *testing.T) {
	for _, views := range []bool{false, true} {
		rt := NewCommandRuntime("caps", helperSpec(t, EnvVar{Name: "STRAZA_HELPER_VIEWS", Value: "1"}), nil, nil)
		rt.Views = views
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 15*time.Second, "helper session", rt.Ready)
		res, err := rt.Call(context.Background(), CallInput{Tool: "client_caps"})
		rt.Stop()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(textOf(res), viewsExtension); got != views {
			t.Errorf("views %v: the server saw capabilities %s", views, textOf(res))
		}
	}
}

// TestViewsFlipStartsANewUpstreamSession: an upstream session keeps the
// capabilities of its initialize, so a change of straza.exposure.views must
// reach the server as a new session. The replica that takes the change
// starts the server again, and another replica's converge does the same,
// because the stored manifest changed.
func TestViewsFlipStartsANewUpstreamSession(t *testing.T) {
	a, b, _ := twoReplicas(t)
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "1.0.0"}, nil)
	addViewTool(srv, "show", uiLink("ui://flip/view.html"))
	addView(srv, &mcp.Resource{URI: "ui://flip/view.html", Name: "view", MIMEType: ViewMIMEType},
		&mcp.ResourceContents{URI: "ui://flip/view.html", MIMEType: ViewMIMEType, Text: "<p>flip</p>"})
	up := newViewsUpstream(t, srv)

	for _, step := range []struct {
		views bool
		want  []string // the initializes of a's install and b's converge
	}{
		{false, []string{capsViewsOff, capsViewsOff}},
		{true, []string{capsViewsOn, capsViewsOn}},
		{false, []string{capsViewsOff, capsViewsOff}},
	} {
		before := len(up.initializes())
		if _, err := a.Install(ctx, viewsManifest(t, "flip", up.URL, `["*"]`, step.views), store.AppSourceAPI); err != nil {
			t.Fatal(err)
		}
		if err := converge(ctx, b); err != nil {
			t.Fatal(err)
		}
		got := up.initializes()[before:]
		if strings.Join(got, "|") != strings.Join(step.want, "|") {
			t.Errorf("views %v: initializes after the change = %q, want %q", step.views, got, step.want)
		}
		for _, m := range []*Manager{a, b} {
			v, ok := m.View("flip")
			if !ok || v.ViewsOn != step.views || (len(v.Views) == 1) != step.views {
				t.Errorf("views %v: view = ViewsOn %v, %d views", step.views, v.ViewsOn, len(v.Views))
			}
		}
	}
}

// slowViews is an upstream with the views a, b, c and d under ui://slow/.
// While hang is set, the reads of b, c and d wait until the read is
// cancelled, and entered gets a value each time one starts to wait.
func slowViews(t *testing.T, hang *atomic.Bool, entered chan<- string) *viewsUpstream {
	t.Helper()
	release := make(chan struct{})
	srv := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "1.0.0"}, nil)
	for _, name := range []string{"a", "b", "c", "d"} {
		uri := "ui://slow/" + name + ".html"
		addViewTool(srv, "show_"+name, uiLink(uri))
		srv.AddResource(&mcp.Resource{URI: uri, Name: name, MIMEType: ViewMIMEType},
			func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				if name != "a" && hang.Load() {
					select {
					case entered <- uri:
					default:
					}
					select {
					case <-ctx.Done():
					case <-release:
					}
					return nil, ctx.Err()
				}
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: ViewMIMEType, Text: "<p>" + name + "</p>"}}}, nil
			})
	}
	up := newViewsUpstream(t, srv)
	t.Cleanup(func() { close(release) })
	return up
}

// TestViewsPassHasOneDeadline: a server whose view reads stop answering
// holds its probe for one viewReadTimeout at most, not one per view, so the
// serial health loop and the admin verbs go on. The views it did not answer
// keep their earlier copies, so an unchanged server neither drifts nor
// degrades, and each view the pass did not reach is logged as not read.
func TestViewsPassHasOneDeadline(t *testing.T) {
	t.Parallel()
	hang := &atomic.Bool{}
	up := slowViews(t, hang, make(chan string, 8))
	mgr, sink, logs := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "slow", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if v := waitStatus(t, mgr, "slow", StatusRunning); len(v.Views) != 4 {
		t.Fatalf("%d views served at the start, want 4", len(v.Views))
	}

	hang.Store(true)
	start := time.Now()
	v, _ := mgr.HealthCheckOne(ctx, "slow")
	if took := time.Since(start); took > viewReadTimeout+5*time.Second {
		t.Errorf("the recheck took %s, want about one viewReadTimeout of %s", took, viewReadTimeout)
	}
	var texts []string
	for _, view := range v.Views {
		texts = append(texts, view.Text)
	}
	if strings.Join(texts, "") != "<p>a</p><p>b</p><p>c</p><p>d</p>" || v.Status != StatusRunning || sink.count("straza.apps.drift") != 0 {
		t.Errorf("after the slow pass: views %q, status %s %q, %d drift events, want the four earlier copies, running and none",
			texts, v.Status, v.Detail, sink.count("straza.apps.drift"))
	}
	for _, uri := range []string{"ui://slow/c.html", "ui://slow/d.html"} {
		if !strings.Contains(logs.String(), "uri="+uri+" reason=\"the read of the server's views ran out of its 15s before this view\"") {
			t.Errorf("no warning says %s was not read in time: %s", uri, logs.String())
		}
	}
}

// TestViewsKeepTheLastReadWhenTheProbeEnds: a probe whose own context ends
// during a read of the views answers the last read and stores nothing, so
// the end of a request or of strazad never shortens the served views.
func TestViewsKeepTheLastReadWhenTheProbeEnds(t *testing.T) {
	hang, entered := &atomic.Bool{}, make(chan string, 8)
	up := slowViews(t, hang, entered)
	mgr, _, _ := viewsManager(t)
	if _, err := mgr.Install(context.Background(), viewsManifest(t, "slow", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "slow", StatusRunning)
	inst := instanceOf(mgr, "slow")
	inst.mu.Lock()
	tools, before, readAt := append([]*mcp.Tool{}, inst.inventory...), inst.viewCache.views, inst.viewsRead
	inst.mu.Unlock()

	hang.Store(true)
	ctx, cancel := context.WithCancel(asked(context.Background()))
	go func() { <-entered; cancel() }()
	got := mgr.refreshViews(ctx, inst, tools, nil)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if len(got) != 4 || len(inst.viewCache.views) != 4 || !inst.viewsRead.Equal(readAt) || &inst.viewCache.views[0] != &before[0] {
		t.Errorf("after the probe ended: %d views answered, %d cached, read at %v (was %v), want the last read kept as it was",
			len(got), len(inst.viewCache.views), inst.viewsRead, readAt)
	}
}

// TestRemoteViewReadWaitsOutTheLock: a view read whose time ran out while it
// waited for the runtime's lock answers its context's error and leaves the
// pooled session, which says nothing about, to the other callers.
func TestRemoteViewReadWaitsOutTheLock(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "1.0.0"}, nil)
	addView(srv, &mcp.Resource{URI: "ui://lock/v.html", Name: "v", MIMEType: ViewMIMEType},
		&mcp.ResourceContents{URI: "ui://lock/v.html", MIMEType: ViewMIMEType, Text: "<p>v</p>"})
	up := newViewsUpstream(t, srv)
	r := besideRuntime("up", RemoteSpec{URL: up.URL, Auth: AuthInject}, nil)
	t.Cleanup(r.Stop)
	if _, err := r.Tools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := r.ReadResource(ctx, nil, "ui://lock/v.html")
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	r.mu.Unlock()
	err := <-done
	r.mu.Lock()
	pooled := r.pool[""] != nil
	r.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "within") || evictions(r) != 0 || !pooled {
		t.Errorf("read after the lock wait: err %v, %d evictions, session pooled %v, want the context's error and the session kept", err, evictions(r), pooled)
	}
}

// TestViewsKeptCopyExpires: a server that stops answering the reads of a
// view keeps its copy served for one read past viewsMaxAge, so a passing
// failure neither drifts nor degrades it, and loses it once the server has
// not answered for viewKeepAge, so a view the server retired leaves within
// about 20 minutes. The drop is logged and drifts once, naming the view.
func TestViewsKeptCopyExpires(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "gone", Version: "1.0.0"}, nil)
	addViewTool(srv, "show", uiLink("ui://gone/v.html"))
	addView(srv, &mcp.Resource{URI: "ui://gone/v.html", Name: "v", MIMEType: ViewMIMEType},
		&mcp.ResourceContents{URI: "ui://gone/v.html", MIMEType: ViewMIMEType, Text: "<p>v</p>"})
	up := newViewsUpstream(t, srv)
	mgr, sink, logs := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "gone", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "gone", StatusRunning)
	inst := instanceOf(mgr, "gone")
	up.failReads.Store(true)
	for _, step := range []struct {
		name      string
		answered  time.Duration // how long ago the server last answered
		served    bool
		drifts    int
		wantInLog string
	}{
		{"a recheck eleven minutes after the answer", viewsMaxAge + time.Minute, true, 0, viewNotRead},
		{"a recheck past the keep age", viewKeepAge + time.Minute, false, 1, viewExpired},
	} {
		inst.mu.Lock()
		for uri := range inst.viewCache.answered {
			inst.viewCache.answered[uri] = time.Now().Add(-step.answered)
		}
		inst.mu.Unlock()
		v, _ := mgr.HealthCheckOne(ctx, "gone")
		linked := len(v.Tools) == 1 && len(toolLinks(v.Tools[0])) == 1
		if (len(v.Views) == 1) != step.served || linked != step.served || sink.count("straza.apps.drift") != step.drifts {
			t.Errorf("%s: %d views, tool linked %v, %d drifts, want served %v and %d drifts", step.name, len(v.Views), linked, sink.count("straza.apps.drift"), step.served, step.drifts)
		}
		if !strings.Contains(logs.String(), step.wantInLog) {
			t.Errorf("%s: the log lacks %q", step.name, step.wantInLog)
		}
	}
	if v, _ := mgr.View("gone"); v.Detail != "inventory drift: the server changed the views ui://gone/v.html" {
		t.Errorf("the drift reason is %q", v.Detail)
	}
}

// TestViewsCapHoldsWhenThePassRunsOut: earlier copies kept for views the
// pass did not reach count toward maxViews, so a pass that runs out of time
// never serves more than maxViews views.
func TestViewsCapHoldsWhenThePassRunsOut(t *testing.T) {
	t.Parallel()
	srv := mcp.NewServer(&mcp.Implementation{Name: "cap", Version: "1.0.0"}, nil)
	answer := func(uri string) {
		addView(srv, &mcp.Resource{URI: uri, Name: uri, MIMEType: ViewMIMEType}, &mcp.ResourceContents{URI: uri, MIMEType: ViewMIMEType, Text: uri})
	}
	for i := range maxViews {
		uri := fmt.Sprintf("ui://cap/b%02d.html", i)
		addViewTool(srv, fmt.Sprintf("b%02d", i), uiLink(uri))
		answer(uri)
	}
	up := newViewsUpstream(t, srv)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	mgr, _, _ := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "cap", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	if v := waitStatus(t, mgr, "cap", StatusRunning); len(v.Views) != maxViews {
		t.Fatalf("%d views at the start, want %d", len(v.Views), maxViews)
	}
	// 31 new links sort before the 32 served ones, and the last of them
	// hangs, so the pass runs out after 31 reads.
	for i := range maxViews - 1 {
		uri := fmt.Sprintf("ui://cap/a%02d.html", i)
		addViewTool(srv, fmt.Sprintf("a%02d", i), uiLink(uri))
		if i < maxViews-2 {
			answer(uri)
			continue
		}
		srv.AddResource(&mcp.Resource{URI: uri, Name: uri, MIMEType: ViewMIMEType},
			func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				select {
				case <-ctx.Done():
				case <-release:
				}
				return nil, ctx.Err()
			})
	}
	if v, _ := mgr.HealthCheckOne(ctx, "cap"); len(v.Views) > maxViews {
		t.Errorf("%d views served after a pass that ran out, want at most %d", len(v.Views), maxViews)
	}
}

// TestDriftNamesALinkChange: when only the views the tools open changed, the
// drift reason says so in plain words.
func TestDriftNamesALinkChange(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "swap", Version: "1.0.0"}, nil)
	for _, name := range []string{"a", "b"} {
		uri := "ui://swap/" + name + ".html"
		addView(srv, &mcp.Resource{URI: uri, Name: name, MIMEType: ViewMIMEType}, &mcp.ResourceContents{URI: uri, MIMEType: ViewMIMEType, Text: name})
	}
	addViewTool(srv, "one", uiLink("ui://swap/a.html"))
	addViewTool(srv, "two", uiLink("ui://swap/b.html"))
	up := newViewsUpstream(t, srv)
	mgr, _, _ := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "swap", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "swap", StatusRunning)
	addViewTool(srv, "one", uiLink("ui://swap/b.html"))
	addViewTool(srv, "two", uiLink("ui://swap/a.html"))
	mgr.HealthCheck(ctx)
	if v, _ := mgr.View("swap"); v.Detail != "inventory drift: the server changed which views its tools open" {
		t.Errorf("the drift reason is %q", v.Detail)
	}
}
