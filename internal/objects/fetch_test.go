package objects

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// demoTarget is the objects-demo server as a listing sees it.
func demoTarget(needsSecret bool) target {
	return target{app: store.App{ID: "app-1", Name: "objects-demo"}, spec: demoSpec(),
		declHash: DeclHash(demoSpec()), needsSecret: needsSecret}
}

// fetchWith runs one listing of objects-demo against pages, by cursor.
func fetchWith(t *testing.T, pages map[string]*mcp.CallToolResult) ([]Object, string) {
	t.Helper()
	caller := newCaller()
	caller.serve("objects-demo", pages)
	l := New(Options{Caller: caller, Secrets: fakeSecrets{}})
	objs, reason, _ := l.fetch(context.Background(), demoTarget(false))
	return objs, reason
}

// TestFetchReadsPages pins the listing result shape: the first page is
// called with no cursor and each next one with the nextCursor before it,
// ids arrive as strings or whole numbers or default to the value, and only
// the attributes the declaration pins are kept, as a string or a list.
func TestFetchReadsPages(t *testing.T) {
	caller := newCaller()
	caller.serve("objects-demo", map[string]*mcp.CallToolResult{
		"": page("4",
			item("Strazahq/straza", "9012331", "product"),
			item("Strazahq/site", 9012332, []any{"web", "docs"})),
		"4": page("",
			map[string]any{"value": "Strazahq/no-id", "attributes": map[string]any{"area": "ops", "topic": "x"}}),
	})
	listing := &manager.Secret{ID: "l-1", Value: "tok-listing"}
	l := New(Options{Caller: caller, Secrets: fakeSecrets{"app-1": listing}})
	objs, reason, soon := l.fetch(context.Background(), demoTarget(true))
	if reason != "" || soon {
		t.Fatalf("reason = %q, soon %v", reason, soon)
	}
	want := []Object{
		{Value: "Strazahq/straza", ID: "9012331", Title: "Strazahq/straza", Attributes: map[string]Attr{"area": {Values: []string{"product"}}}},
		{Value: "Strazahq/site", ID: "9012332", Title: "Strazahq/site", Attributes: map[string]Attr{"area": {Values: []string{"web", "docs"}, List: true}}},
		{Value: "Strazahq/no-id", ID: "Strazahq/no-id", Attributes: map[string]Attr{"area": {Values: []string{"ops"}}}},
	}
	if !reflect.DeepEqual(objs, want) {
		t.Errorf("objects = %+v\nwant %+v", objs, want)
	}
	if len(caller.calls) != 2 || caller.calls[0].args != `{}` || caller.calls[1].args != `{"cursor":"4"}` {
		t.Errorf("calls = %+v, want {} then the cursor 4", caller.calls)
	}
	for _, c := range caller.calls {
		if c.secret != listing {
			t.Errorf("a page carried %+v, want exactly the listing secret", c.secret)
		}
	}
}

// TestFetchRefusals pins every rule that fails a listing as a whole (case
// 17), each with its reason sentence.
func TestFetchRefusals(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	many := func(n int) map[string]any {
		attrs := map[string]any{}
		for i := 0; i < n; i++ {
			attrs[fmt.Sprintf("k%d", i)] = "v"
		}
		return map[string]any{"value": "x", "attributes": attrs}
	}
	values := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "v"
		}
		return out
	}
	bulk := func(from, n int, next string) *mcp.CallToolResult {
		items := make([]map[string]any, n)
		for i := range items {
			items[i] = map[string]any{"value": fmt.Sprintf("org/r-%06d", from+i)}
		}
		return page(next, items...)
	}
	const tail = ", so the listing was refused as a whole and the last good list stays."
	cases := []struct {
		name  string
		pages map[string]*mcp.CallToolResult
		want  string
	}{
		{name: "a tool error", pages: map[string]*mcp.CallToolResult{"": {IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "upstream said no.\nstack trace"}}}},
			want: "repo_list answered an error: upstream said no. Check objects-demo's logs on its Activity tab."},
		{name: "no structured content", pages: map[string]*mcp.CallToolResult{"": {Content: []mcp.Content{&mcp.TextContent{Text: "[]"}}}},
			want: "repo_list answered without an objects list in its structured content. The server's author must return {objects, nextCursor} as the Straza object marks describe."},
		{name: "objects not a list", pages: map[string]*mcp.CallToolResult{"": {StructuredContent: map[string]any{"objects": "none"}}},
			want: "repo_list answered without an objects list in its structured content. The server's author must return {objects, nextCursor} as the Straza object marks describe."},
		{name: "an item not an object", pages: map[string]*mcp.CallToolResult{"": {StructuredContent: map[string]any{"objects": []any{"x"}}}},
			want: "repo_list answered without an objects list in its structured content. The server's author must return {objects, nextCursor} as the Straza object marks describe."},
		{name: "an empty value", pages: map[string]*mcp.CallToolResult{"": page("", item("", nil, nil))},
			want: "repo_list returned a repo whose value is empty or not a string of valid UTF-8" + tail},
		{name: "a value not a string", pages: map[string]*mcp.CallToolResult{"": page("", map[string]any{"value": 7})},
			want: "repo_list returned a repo whose value is empty or not a string of valid UTF-8" + tail},
		{name: "a value not valid UTF-8", pages: map[string]*mcp.CallToolResult{"": page("", item("bad\xff", nil, nil))},
			want: "repo_list returned a repo whose value is empty or not a string of valid UTF-8" + tail},
		{name: "a value over 512 bytes", pages: map[string]*mcp.CallToolResult{"": page("", item(long(513), nil, nil))},
			want: "repo_list returned a value longer than 512 bytes" + tail},
		{name: "an id over 512 bytes", pages: map[string]*mcp.CallToolResult{"": page("", item("x", long(513), nil))},
			want: "repo_list returned an id longer than 512 bytes" + tail},
		{name: "an empty id", pages: map[string]*mcp.CallToolResult{"": page("", item("x", "", nil))},
			want: "repo_list returned a repo with an empty id" + tail},
		{name: "an id with a fraction", pages: map[string]*mcp.CallToolResult{"": page("", item("x", 1.5, nil))},
			want: "repo_list returned an id that is neither a string nor a whole number from 0 to 2^53" + tail},
		{name: "an id over 2^53", pages: map[string]*mcp.CallToolResult{"": page("", item("x", uint64(1<<53+1), nil))},
			want: "repo_list returned an id that is neither a string nor a whole number from 0 to 2^53" + tail},
		{name: "a negative id", pages: map[string]*mcp.CallToolResult{"": page("", item("x", -1, nil))},
			want: "repo_list returned an id that is neither a string nor a whole number from 0 to 2^53" + tail},
		{name: "a title over 256 bytes", pages: map[string]*mcp.CallToolResult{"": page("", map[string]any{"value": "x", "title": long(257)})},
			want: "repo_list returned a title longer than 256 bytes" + tail},
		{name: "a title not a string", pages: map[string]*mcp.CallToolResult{"": page("", map[string]any{"value": "x", "title": 3})},
			want: "repo_list returned a repo whose title is not a string" + tail},
		{name: "17 attributes", pages: map[string]*mcp.CallToolResult{"": page("", many(17))},
			want: "repo_list returned a repo with more than 16 attributes" + tail},
		{name: "an attribute value over 128 bytes", pages: map[string]*mcp.CallToolResult{"": page("", item("x", nil, long(129)))},
			want: "repo_list returned an attribute value longer than 128 bytes" + tail},
		{name: "an attribute list value over 128 bytes", pages: map[string]*mcp.CallToolResult{"": page("", item("x", nil, []any{"a", long(129)}))},
			want: "repo_list returned an attribute value longer than 128 bytes" + tail},
		{name: "an attribute with 17 values", pages: map[string]*mcp.CallToolResult{"": page("", item("x", nil, values(17)))},
			want: "repo_list returned an attribute with more than 16 values" + tail},
		{name: "an attribute of numbers", pages: map[string]*mcp.CallToolResult{"": page("", item("x", nil, 5))},
			want: "repo_list returned an attribute that is neither a string nor a list of strings" + tail},
		{name: "a value twice with different ids", pages: map[string]*mcp.CallToolResult{"": page("2", item("Strazahq/a", "1", nil)), "2": page("", item("Strazahq/a", "2", nil))},
			want: "repo_list returned Strazahq/a twice with different ids."},
		{name: "an id twice with different values", pages: map[string]*mcp.CallToolResult{"": page("", item("Strazahq/a", 7, nil), item("Strazahq/b", "7", nil))},
			want: "repo_list returned the id 7 twice with different values."},
		{name: "a value twice with different attributes", pages: map[string]*mcp.CallToolResult{"": page("2", item("Strazahq/a", "1", "web")), "2": page("", item("Strazahq/a", "1", []any{"web"}))},
			want: "repo_list returned Strazahq/a twice with different attributes."},
		{name: "a value twice with different titles", pages: map[string]*mcp.CallToolResult{"": page("", item("Strazahq/a", "1", nil),
			map[string]any{"value": "Strazahq/a", "id": "1", "title": "other"})},
			want: "repo_list returned Strazahq/a twice with different titles."},
		{name: "more than 100,000", pages: map[string]*mcp.CallToolResult{"": bulk(0, 60000, "2"), "2": bulk(60000, 40001, "")},
			want: "repo_list returned more than 100,000 repos" + tail},
		{name: "a cursor that never ends", pages: map[string]*mcp.CallToolResult{"": page("4", item("a", nil, nil)), "4": page("4", item("b", nil, nil))},
			want: "repo_list answered with the cursor it was called with as nextCursor, so the listing would never end. The server's author must return a new nextCursor, or none on the last page."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs, reason := fetchWith(t, tc.pages)
			if reason != tc.want {
				t.Errorf("reason = %q\nwant     %q", reason, tc.want)
			}
			if objs != nil {
				t.Errorf("a refused listing answered %d objects", len(objs))
			}
		})
	}
	if objs, reason := fetchWith(t, map[string]*mcp.CallToolResult{"": bulk(0, 60000, "2"), "2": bulk(60000, 40000, "")}); reason != "" || len(objs) != maxObjects {
		t.Errorf("exactly 100,000 = %d objects, %q", len(objs), reason)
	}
}

// TestFetchDropsExactRepeats pins the paging shift of a server that pages
// by number: an item read again with the same value, id, title and
// attributes is kept once and the listing counts, also when the repeat
// would take the count past the limit.
func TestFetchDropsExactRepeats(t *testing.T) {
	a := item("Strazahq/a", 1, "web")
	objs, reason := fetchWith(t, map[string]*mcp.CallToolResult{
		"":  page("2", a, item("Strazahq/b", 2, nil)),
		"2": page("", item("Strazahq/b", 2, nil), item("Strazahq/c", "3", nil)),
	})
	if reason != "" {
		t.Fatalf("reason = %q, want a good listing", reason)
	}
	got := make([]string, len(objs))
	for i, o := range objs {
		got[i] = o.Value
	}
	if want := []string{"Strazahq/a", "Strazahq/b", "Strazahq/c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
	full := make([]map[string]any, maxObjects)
	for i := range full {
		full[i] = map[string]any{"value": fmt.Sprintf("org/r-%06d", i)}
	}
	objs, reason = fetchWith(t, map[string]*mcp.CallToolResult{"": page("2", full...), "2": page("", full[maxObjects-1])})
	if reason != "" || len(objs) != maxObjects {
		t.Errorf("100,000 and one repeat = %d objects, %q; want 100,000 and a good listing", len(objs), reason)
	}
}

// TestFetchServerRules pins what stops a listing before or between pages:
// no listing credential (no call at all, and no fallback), a server that
// is not running or whose tool list was not read yet, a listing tool it
// does not have or that is not read-only, a call that fails, a call the
// server refuses with an HTTP status, and the page and listing time limits.
// soon marks the failures a server that is still starting answers, which
// the loop tries again after a minute.
func TestFetchServerRules(t *testing.T) {
	ok := map[string]*mcp.CallToolResult{"": page("", item("a", nil, nil))}
	cases := []struct {
		name    string
		secret  bool
		setup   func(f *fakeCaller, l *Lists)
		want    string
		soon    bool
		noCalls bool
	}{
		{name: "no listing credential", secret: true, noCalls: true,
			want: "objects-demo has no listing credential, so Straza cannot list its repos. Set one on its server page under Server authentication, or with strazactl apps secret set objects-demo --listing. Then press List again on its repos tab, or run strazactl apps objects refresh objects-demo."},
		{name: "not running", noCalls: true, soon: true, setup: func(f *fakeCaller, _ *Lists) { delete(f.tools, "objects-demo") },
			want: "objects-demo is not running, so Straza could not call repo_list. Start it, then refresh."},
		{name: "tool list not read yet", noCalls: true, soon: true,
			setup: func(f *fakeCaller, _ *Lists) {
				f.invErrs["objects-demo"] = fmt.Errorf("objects-demo: %w", manager.ErrToolsUnread)
			},
			want: "Straza has not read objects-demo's tool list yet, so it cannot check repo_list before it lists objects-demo's repos. It tries again in about a minute. If objects-demo stays degraded, read why with strazactl apps show objects-demo."},
		{name: "no listing tool", noCalls: true, setup: func(f *fakeCaller, _ *Lists) { f.tools["objects-demo"] = f.tools["objects-demo"][1:] },
			want: "objects-demo has no tool named repo_list, so Straza cannot list its repos. Name the listing tool objects-demo has in straza.objects.list.tool, or ask the server's author to add repo_list."},
		{name: "not read-only", noCalls: true, setup: func(f *fakeCaller, _ *Lists) { f.tools["objects-demo"][0] = &mcp.Tool{Name: "repo_list"} },
			want: "repo_list is not marked read-only, so Straza will not call it to list repos. The server's author must set readOnlyHint on it."},
		{name: "stopped between the check and the call", soon: true, setup: func(f *fakeCaller, _ *Lists) { f.errs["objects-demo"] = fmt.Errorf("gone: %w", manager.ErrNotReady) },
			want: "objects-demo is not running, so Straza could not call repo_list. Start it, then refresh."},
		{name: "a call that fails", setup: func(f *fakeCaller, _ *Lists) { f.errs["objects-demo"] = errors.New("dial tcp: refused") },
			want: "Straza could not reach objects-demo to call repo_list. Check objects-demo's logs on its Activity tab, then refresh."},
		{name: "a listing credential the server refuses", secret: true,
			setup: func(f *fakeCaller, l *Lists) {
				l.opts.Secrets = fakeSecrets{"app-1": {ID: "listing-1", Value: "v"}}
				f.errs["objects-demo"] = fmt.Errorf("shown: %w", &manager.RefusedError{Server: "objects-demo", Status: 401})
			},
			want: "objects-demo refused the listing credential with HTTP 401 Unauthorized, so Straza could not list its repos. objects-demo's own config must hold the value Straza stores: put it there and restart objects-demo, or store a new value under Server authentication on its server page and give objects-demo that one. Then press List again on its repos tab, or run strazactl apps objects refresh objects-demo."},
		{name: "a listing call the server refuses", setup: func(f *fakeCaller, _ *Lists) {
			f.errs["objects-demo"] = &manager.RefusedError{Server: "objects-demo", Status: 403}
		},
			want: "objects-demo answered the call to repo_list with HTTP 403 Forbidden, so Straza could not list its repos. Check objects-demo's logs on its Activity tab, then refresh."},
		{name: "a page over its time", setup: func(f *fakeCaller, l *Lists) { f.block, l.pageTimeout = true, 20*time.Millisecond },
			want: "repo_list did not answer within 30 seconds on page 1."},
		{name: "a listing over its time", setup: func(f *fakeCaller, l *Lists) { f.block, l.listTimeout = true, 20*time.Millisecond },
			want: "The listing took longer than 5 minutes."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller := newCaller()
			caller.serve("objects-demo", ok)
			l := New(Options{Caller: caller, Secrets: fakeSecrets{}})
			if tc.setup != nil {
				tc.setup(caller, l)
			}
			objs, reason, soon := l.fetch(context.Background(), demoTarget(tc.secret))
			if reason != tc.want || objs != nil {
				t.Errorf("fetch = %d objects, %q\nwant %q", len(objs), reason, tc.want)
			}
			if soon != tc.soon {
				t.Errorf("soon = %v, want %v", soon, tc.soon)
			}
			if tc.noCalls && caller.callCount() != 0 {
				t.Errorf("the listing tool was called %d times, want none", caller.callCount())
			}
		})
	}
}
