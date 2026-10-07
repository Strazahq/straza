package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// TestManifestViewsSwitch pins straza.exposure.views on the wire: it parses,
// survives JSON and FromJSON, and a manifest without it stores no views key,
// so the stored form and its fingerprint stay as they were for every server
// that does not set it. An exposure block that only turns views on is
// refused with a sentence that names the fix.
func TestManifestViewsSwitch(t *testing.T) {
	doc := func(exposure string) string {
		return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: v}\n" +
			"server: {name: straza.test/v, version: \"1.0.0\"}\n" +
			"straza:\n  runtime:\n    kind: remote\n    remote: {url: \"https://v.example/mcp\"}\n" + exposure
	}
	const stored = `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"v"},` +
		`"server":{"name":"straza.test/v","version":"1.0.0"},` +
		`"straza":{"runtime":{"kind":"remote","remote":{"url":"https://v.example/mcp","auth":"inject"}},"exposure":{"tools":["*"]%s}}}`
	cases := []struct {
		name     string
		exposure string
		on       bool
		json     string // the stored form, empty when refused
		err      string
	}{
		{name: "no exposure block", json: fmt.Sprintf(stored, "")},
		{name: "views absent", exposure: "  exposure: {tools: [\"*\"]}\n", json: fmt.Sprintf(stored, "")},
		{name: "views false", exposure: "  exposure: {tools: [\"*\"], views: false}\n", json: fmt.Sprintf(stored, "")},
		{name: "views true", exposure: "  exposure: {tools: [\"*\"], views: true}\n", on: true, json: fmt.Sprintf(stored, `,"views":true`)},
		{name: "views without tools", exposure: "  exposure: {views: true}\n",
			err: `manifest: invalid App "v": exposure.views needs exposure.tools beside it. Write tools: ["*"] to keep every tool of the server`},
		{name: "views not a boolean", exposure: "  exposure: {tools: [\"*\"], views: 1}\n", err: "cannot unmarshal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mf, err := Parse([]byte(doc(tc.exposure)))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("Parse err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			js, err := mf.JSON()
			if err != nil {
				t.Fatal(err)
			}
			if js != tc.json {
				t.Errorf("stored form\n got %s\nwant %s", js, tc.json)
			}
			back, err := FromJSON(js)
			if err != nil {
				t.Fatal(err)
			}
			if mf.viewsOn() != tc.on || back.viewsOn() != tc.on {
				t.Errorf("viewsOn = %v after Parse, %v after FromJSON, want %v", mf.viewsOn(), back.viewsOn(), tc.on)
			}
			if again, _ := back.JSON(); again != js {
				t.Errorf("FromJSON then JSON changed the stored form: %s", again)
			}
		})
	}
	entries, err := os.ReadDir(specExamples)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(specExamples, e.Name()))
		mf, err := Parse(raw)
		if err != nil || strings.Contains(string(raw), "views:") {
			continue
		}
		if js, _ := mf.JSON(); strings.Contains(js, "views") {
			t.Errorf("%s: the stored form names views: %s", e.Name(), js)
		}
	}
}

// goldenToolsHash is the drift hash of goldenTools as Straza computed it
// before views existed. A server with no view links keeps it, with the
// switch off and on alike, so turning views on drifts no such server.
const goldenToolsHash = "c387d9a81e7da68595fccf4542cec409d18d7a22d1bd14982ea374c34171dbcc"

func goldenTools(meta mcp.Meta) []*mcp.Tool {
	return []*mcp.Tool{
		{Name: "echo", Description: "echo text back", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		{Name: "time", Description: "the time", InputSchema: map[string]any{"type": "object"}, Meta: meta},
	}
}

// TestInventoryHashCoversViews pins the drift hash: byte-identical to the
// tools-only hash for a server with no links, and changed by a link, a
// served view's document or its _meta while the switch is on. With the
// switch off, a link the server sends anyway is never served, so it does
// not count.
func TestInventoryHashCoversViews(t *testing.T) {
	plain := goldenTools(mcp.Meta{"other": "x"})
	linked := goldenTools(mcp.Meta{"other": "x", "ui": map[string]any{"resourceUri": "ui://t/v.html"}})
	flat := goldenTools(mcp.Meta{"other": "x", "ui/resourceUri": "ui://t/v.html"})
	view := View{URI: "ui://t/v.html", Text: "<p>a</p>"}
	cspView := View{URI: "ui://t/v.html", Text: "<p>a</p>", Meta: map[string]any{"ui": map[string]any{"csp": map[string]any{"connectDomains": []any{"https://x.example"}}}}}
	otherText := View{URI: "ui://t/v.html", Text: "<p>b</p>"}

	for _, tc := range []struct {
		name  string
		tools []*mcp.Tool
		views []View
		on    bool
		same  bool // equal to goldenToolsHash
	}{
		{"no links, views off", plain, nil, false, true},
		{"no links, views on", plain, nil, true, true},
		{"a link the server sends with views off", linked, nil, false, true},
		{"a flat link the server sends with views off", flat, nil, false, true},
		{"a link with views on", linked, nil, true, false},
		{"a flat link with views on", flat, nil, true, false},
	} {
		if got := inventoryHash(tc.tools, tc.views, tc.on); (got == goldenToolsHash) != tc.same {
			t.Errorf("%s: hash %s, golden %v, want %v", tc.name, got, got == goldenToolsHash, tc.same)
		}
	}
	seen := map[string]string{}
	for name, views := range map[string][]View{"no view": nil, "a view": {view}, "its csp": {cspView}, "another document": {otherText}} {
		h := inventoryHash(linked, views, true)
		if prev, dup := seen[h]; dup {
			t.Errorf("%s and %s hash alike", name, prev)
		}
		seen[h] = name
	}
}

// viewsServer is an upstream whose exposed tools link a view each way a
// server can: a listed view, a view only a template serves, and views that
// fail each check. The hidden tool is outside the exposure, so its view is
// never read. text is the document of the listed view.
func viewsServer(text *atomic.Value) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "views", Version: "1.0.0"}, nil)
	ok := func(uri string) *mcp.ResourceContents {
		return &mcp.ResourceContents{URI: uri, MIMEType: ViewMIMEType, Text: "<p>" + uri + "</p>"}
	}
	addViewTool(srv, "show_clock", mcp.Meta{"ui": map[string]any{"resourceUri": "ui://clock/app.html", "visibility": []any{"model", "app"}}})
	srv.AddResource(&mcp.Resource{URI: "ui://clock/app.html", Name: "clock", Title: "Clock", Description: "the time", MIMEType: ViewMIMEType},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "ui://clock/app.html", MIMEType: ViewMIMEType,
				Text: text.Load().(string), Meta: mcp.Meta{"ui": map[string]any{"prefersBorder": true}}}}}, nil
		})
	addViewTool(srv, "show_legacy", mcp.Meta{"ui/resourceUri": "ui://legacy/card.html"})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "ui://legacy/{file}", Name: "legacy"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{ok(req.Params.URI)}}, nil
		})
	addViewTool(srv, "show_type", uiLink("ui://bad/type.html"))
	addView(srv, &mcp.Resource{URI: "ui://bad/type.html", Name: "type"}, &mcp.ResourceContents{URI: "ui://bad/type.html", MIMEType: "text/html", Text: "<p></p>"})
	addViewTool(srv, "show_two", uiLink("ui://bad/two.html"))
	addView(srv, &mcp.Resource{URI: "ui://bad/two.html", Name: "two"}, ok("ui://bad/two.html"), ok("ui://bad/two.html"))
	addViewTool(srv, "show_big", uiLink("ui://bad/big.html"))
	big := ok("ui://bad/big.html")
	big.Text = strings.Repeat("x", maxViewBytes+1)
	addView(srv, &mcp.Resource{URI: "ui://bad/big.html", Name: "big"}, big)
	addViewTool(srv, "show_web", uiLink("https://web.example/view.html"))
	addViewTool(srv, "show_missing", uiLink("ui://bad/missing.html"))
	addViewTool(srv, "show_blob", uiLink("ui://bad/blob.html"))
	addView(srv, &mcp.Resource{URI: "ui://bad/blob.html", Name: "blob"}, &mcp.ResourceContents{URI: "ui://bad/blob.html", MIMEType: ViewMIMEType, Blob: []byte("<p></p>")})
	addViewTool(srv, "plain", nil)
	addViewTool(srv, "hidden", uiLink("ui://hidden/secret.html"))
	return srv
}

// TestViewCache walks the views of one server through the manager. With
// the switch on, the views that pass are served sorted by URI with their
// listed names or their last path segment, every tool that links a failed
// view is listed without that link, and each failure is logged once with
// the server, the address and the reason. With the switch off, no view is
// read or served and no tool carries a link. The cached inventory keeps
// the upstream's _meta either way.
func TestViewCache(t *testing.T) {
	failed := map[string]string{
		"ui://bad/type.html":            `the server answered it as \"text/html\", and a view must be text of type text/html;profile=mcp-app`,
		"ui://bad/two.html":             "the server answered 2 content items, and a view must be exactly one HTML document",
		"ui://bad/big.html":             "it is 2097153 bytes, and a view may be at most 2097152 bytes",
		"https://web.example/view.html": "the address is not a ui:// address, and a view must have one",
		"ui://bad/missing.html":         "reading it from the server failed: ",
		"ui://bad/blob.html":            "the server answered it as binary data, and a view must be one HTML text",
	}
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("views %v", on), func(t *testing.T) {
			text := &atomic.Value{}
			text.Store("<p>clock</p>")
			up := newViewsUpstream(t, viewsServer(text))
			mgr, _, logs := viewsManager(t)
			if _, err := mgr.Install(context.Background(), viewsManifest(t, "views", up.URL, `["show_*", "plain"]`, on), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
			v := waitStatus(t, mgr, "views", StatusRunning)

			var uris []string
			for _, view := range v.Views {
				uris = append(uris, view.URI+" "+view.Name+" "+view.Title)
			}
			want := []string{"ui://clock/app.html clock Clock", "ui://legacy/card.html card.html "}
			if !on {
				want = nil
			}
			if !slices.Equal(uris, want) || v.ViewsOn != on {
				t.Errorf("views = %q, ViewsOn %v, want %q", uris, v.ViewsOn, want)
			}
			if on && (v.Views[0].Text != "<p>clock</p>" || v.Views[0].MIMEType != ViewMIMEType || v.Views[0].Meta["ui"] == nil) {
				t.Errorf("clock view = %+v", v.Views[0])
			}
			if !on && up.readCount() != 0 {
				t.Errorf("views off read %d views", up.readCount())
			}

			for _, tool := range v.Tools {
				links := toolLinks(tool)
				_, hasUI := tool.Meta["ui"]
				switch {
				case !on && (hasUI || tool.Meta["ui/resourceUri"] != nil):
					t.Errorf("views off: %s carries %v", tool.Name, tool.Meta)
				case on && slices.ContainsFunc(links, func(l string) bool { return failed[l] != "" }):
					t.Errorf("views on: %s keeps a link to a view that is not served: %v", tool.Name, tool.Meta)
				case on && (tool.Name == "show_clock" || tool.Name == "show_legacy") && len(links) != 1:
					t.Errorf("views on: %s lost its link: %v", tool.Name, tool.Meta)
				case on && tool.Name == "show_clock" && tool.Meta["ui"].(map[string]any)["visibility"] == nil:
					t.Errorf("views on: show_clock lost the rest of its ui block: %v", tool.Meta)
				}
				if tool.Name == "hidden" {
					t.Errorf("the hidden tool is listed")
				}
			}
			inst := instanceOf(mgr, "views")
			inst.mu.Lock()
			for _, tool := range inst.inventory {
				if strings.HasPrefix(tool.Name, "show_") && len(toolLinks(tool)) != 1 {
					t.Errorf("the cached %s lost its upstream _meta: %v", tool.Name, tool.Meta)
				}
			}
			inst.mu.Unlock()

			out := logs.String()
			for uri, reason := range failed {
				line := ""
				for _, l := range strings.Split(out, "\n") {
					if strings.Contains(l, "uri="+uri+" ") {
						line = l
					}
				}
				switch {
				case !on && line != "":
					t.Errorf("views off logged %s", line)
				case on && (!strings.Contains(line, "app=views") || !strings.Contains(line, reason) || strings.Count(out, "uri="+uri+" ") != 1):
					t.Errorf("views on: the warning for %s is %q, want one naming app=views and %q", uri, line, reason)
				}
			}
			if strings.Contains(out, "ui://hidden/secret.html") {
				t.Errorf("the view of a tool outside the exposure was read: %s", out)
			}
		})
	}
}

// TestViewsReadAgainOnlyWhenNeeded pins when the probe reads views: once at
// the start, not again on a later health tick, again once the copy is ten
// minutes old, again when an administrator's recheck asks, and again when
// the set of links changes. A changed view raises straza.apps.drift.
func TestViewsReadAgainOnlyWhenNeeded(t *testing.T) {
	text := &atomic.Value{}
	text.Store("<p>clock</p>")
	srv := mcp.NewServer(&mcp.Implementation{Name: "views", Version: "1.0.0"}, nil)
	addViewTool(srv, "show_clock", uiLink("ui://clock/app.html"))
	srv.AddResource(&mcp.Resource{URI: "ui://clock/app.html", Name: "clock", MIMEType: ViewMIMEType},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "ui://clock/app.html", MIMEType: ViewMIMEType, Text: text.Load().(string)}}}, nil
		})
	up := newViewsUpstream(t, srv)
	mgr, sink, _ := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "clock", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "clock", StatusRunning)
	inst := instanceOf(mgr, "clock")

	steps := []struct {
		name   string
		do     func()
		reads  int // reads of the one view so far
		drifts int
	}{
		{"the start", func() {}, 1, 0},
		{"a health tick", func() { mgr.HealthCheck(ctx) }, 1, 0},
		{"a tick after ten minutes", func() {
			inst.mu.Lock()
			inst.viewsRead = time.Now().Add(-viewsMaxAge - time.Second)
			inst.mu.Unlock()
			mgr.HealthCheck(ctx)
		}, 2, 0},
		{"a recheck", func() { mgr.HealthCheckOne(ctx, "clock") }, 3, 0},
		{"a recheck after the view changed", func() {
			text.Store("<p>other</p>")
			if v, _ := mgr.HealthCheckOne(ctx, "clock"); v.Detail != "inventory drift: the server changed the views ui://clock/app.html" {
				t.Errorf("the drift reason is %q, want one that names the changed view", v.Detail)
			}
		}, 4, 1},
		{"a tick after a tool linked a second view", func() {
			addViewTool(srv, "show_more", uiLink("ui://clock/more.html"))
			mgr.HealthCheck(ctx)
		}, 6, 2},
		{"another tick", func() { mgr.HealthCheck(ctx) }, 6, 2},
	}
	for _, step := range steps {
		step.do()
		if got := up.readCount(); got != step.reads {
			t.Errorf("%s: %d reads of the view, want %d", step.name, got, step.reads)
		}
		if got := sink.count("straza.apps.drift"); got != step.drifts {
			t.Errorf("%s: %d drift events, want %d", step.name, got, step.drifts)
		}
	}
	if v, _ := mgr.View("clock"); len(v.Views) != 1 || v.Views[0].Text != "<p>other</p>" {
		t.Errorf("served views = %+v, want the changed clock alone, since more.html does not exist", v.Views)
	}
}

// TestViewsCap: a server serves at most maxViews views. The ones past the
// cap, in URI order, are not read, and their tools lose the link.
func TestViewsCap(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "many", Version: "1.0.0"}, nil)
	for i := range maxViews + 1 {
		uri := fmt.Sprintf("ui://many/%02d.html", i)
		addViewTool(srv, fmt.Sprintf("t%02d", i), uiLink(uri))
		addView(srv, &mcp.Resource{URI: uri, Name: uri}, &mcp.ResourceContents{URI: uri, MIMEType: ViewMIMEType, Text: uri})
	}
	up := newViewsUpstream(t, srv)
	mgr, _, logs := viewsManager(t)
	if _, err := mgr.Install(context.Background(), viewsManifest(t, "many", up.URL, `["*"]`, true), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	v := waitStatus(t, mgr, "many", StatusRunning)
	last := fmt.Sprintf("ui://many/%02d.html", maxViews)
	if len(v.Views) != maxViews || v.Views[maxViews-1].URI == last || up.readCount() != maxViews {
		t.Errorf("%d views served, %d read, want %d without %s", len(v.Views), up.readCount(), maxViews, last)
	}
	for _, tool := range v.Tools {
		if links := toolLinks(tool); (tool.Name == fmt.Sprintf("t%02d", maxViews)) != (len(links) == 0) {
			t.Errorf("%s links %v", tool.Name, links)
		}
	}
	if !strings.Contains(logs.String(), "the server links more than 32 views, and Straza serves at most 32 views of one server") {
		t.Errorf("no warning names the cap: %s", logs.String())
	}
}

// TestCommandServerViews: a command server with views on gets its linked
// view read over stdio, through the flat key.
func TestCommandServerViews(t *testing.T) {
	mgr, _ := testManager(t)
	mf := helperManifest(t, "helper-views", []string{"*"}, EnvVar{Name: "STRAZA_HELPER_VIEWS", Value: "1"})
	mf.Straza.Exposure.Views = true
	if _, err := mgr.Install(context.Background(), mf, store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "the helper's view", func() bool {
		v, ok := mgr.View("helper-views")
		return ok && len(v.Views) == 1
	})
	v, _ := mgr.View("helper-views")
	if got := v.Views[0]; got.URI != "ui://helper/view.html" || got.Name != "helper-view" || got.Text != "<p>helper</p>" {
		t.Errorf("view = %+v", got)
	}
}
