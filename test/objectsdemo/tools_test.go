package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testApp     = "test-app-value"
	testListing = "test-listing-value"
)

// bearer sets one Authorization header on every request the client sends.
type bearer struct{ value string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.value != "" {
		r.Header.Set("Authorization", "Bearer "+b.value)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// startDemo serves objects-demo on a test listener with a state file in a
// fresh directory and answers the server's URL and that file's path.
func startDemo(t *testing.T) (string, string) {
	t.Helper()
	return startDemoOn(t, filepath.Join(t.TempDir(), "repos.json"))
}

func startDemoOn(t *testing.T, state string) (string, string) {
	t.Helper()
	d := newDemo(tokens{app: testApp, listing: testListing}, state)
	srv := httptest.NewServer(d.handler())
	t.Cleanup(srv.Close)
	return srv.URL, state
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "objectsdemo-test", Version: "1.0.0"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect with %q: %v", token, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func call(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return res, text.String()
}

func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMarksOnEveryTool(t *testing.T) {
	url, _ := startDemo(t)
	s := connect(t, url, testApp)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"repo_list":    `{"ai.straza/lists-objects":{"type":"repo","attributes":[{"name":"area","setBy":"org"}]}}`,
		"repo_read":    `{"ai.straza/object":{"type":"repo","argument":"repo"}}`,
		"issue_create": `{"ai.straza/object":{"type":"repo","argument":"repo"}}`,
		"search_code":  `{"ai.straza/object":{"type":"repo","reach":"all"}}`,
		"whoami":       `{"ai.straza/object":{"type":"repo","reach":"none"}}`,
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	if len(got) != len(want) {
		t.Errorf("tools/list answered %d tools, want %d", len(got), len(want))
	}
	for name, mark := range want {
		tool := got[name]
		if tool == nil {
			t.Errorf("tool %s is missing", name)
			continue
		}
		var wantMeta any
		if err := json.Unmarshal([]byte(mark), &wantMeta); err != nil {
			t.Fatal(err)
		}
		if gotMeta := asJSON(t, tool.Meta); !reflect.DeepEqual(gotMeta, wantMeta) {
			t.Errorf("%s _meta = %v, want %v", name, gotMeta, wantMeta)
		}
	}
	if l := got["repo_list"]; l == nil || l.Annotations == nil || !l.Annotations.ReadOnlyHint {
		t.Error("repo_list must carry readOnlyHint: true")
	}
	for _, name := range []string{"repo_read", "issue_create"} {
		if got[name] == nil {
			continue
		}
		schema, _ := asJSON(t, got[name].InputSchema).(map[string]any)
		prop, _ := schema["properties"].(map[string]any)["repo"].(map[string]any)
		if prop["type"] != "string" {
			t.Errorf("%s: the repo argument has type %v, want string", name, prop["type"])
		}
		if req, _ := schema["required"].([]any); !containsAny(req, "repo") {
			t.Errorf("%s: repo is not in required %v", name, req)
		}
	}
}

func containsAny(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestTokenRulesPerTool(t *testing.T) {
	url, _ := startDemo(t)
	sessions := map[string]*mcp.ClientSession{
		"app":     connect(t, url, testApp),
		"listing": connect(t, url, testListing),
		"none":    connect(t, url, ""),
	}
	repo := map[string]any{"repo": "Strazahq/straza"}
	cases := []struct {
		tool    string
		args    map[string]any
		token   string
		wantErr bool
		want    string
	}{
		{"repo_list", nil, "listing", false, "Strazahq/straza"},
		{"repo_list", nil, "app", true, "answers only to objects-demo's listing token, and this call carried the app token"},
		{"repo_list", nil, "none", true, "this call carried no token"},
		{"repo_read", repo, "app", false, "9012331"},
		{"repo_read", repo, "listing", true, "repo_read refuses the listing token"},
		{"repo_read", repo, "none", true, "this call carried no token"},
		{"issue_create", map[string]any{"repo": "Strazahq/straza", "title": "Fix it"}, "app", false, "Opened issue #1 in Strazahq/straza"},
		{"issue_create", map[string]any{"repo": "Strazahq/straza", "title": "Fix it"}, "listing", true, "issue_create refuses the listing token"},
		{"search_code", map[string]any{"query": "mcp"}, "app", false, "Strazahq/midpoint-mcp-server"},
		{"search_code", map[string]any{"query": "mcp"}, "listing", true, "search_code refuses the listing token"},
		{"whoami", nil, "app", false, "This call carried the app token."},
		{"whoami", nil, "listing", false, "This call carried the listing token."},
		{"whoami", nil, "none", false, "This call carried no token."},
		{"repo_read", map[string]any{"repo": "strazahq/straza"}, "app", true, `objects-demo holds no repo named "strazahq/straza"`},
	}
	for _, c := range cases {
		res, text := call(t, sessions[c.token], c.tool, c.args)
		if res.IsError != c.wantErr {
			t.Errorf("%s with the %s token: isError = %v, want %v (%s)", c.tool, c.token, res.IsError, c.wantErr, text)
		}
		if !strings.Contains(text, c.want) {
			t.Errorf("%s with the %s token answered %q, want it to contain %q", c.tool, c.token, text, c.want)
		}
		if strings.Contains(text, testApp) || strings.Contains(text, testListing) {
			t.Errorf("%s answered a token value: %q", c.tool, text)
		}
	}
}

func TestUnknownTokenIsRefusedAtTheDoor(t *testing.T) {
	d := newDemo(tokens{app: testApp, listing: testListing}, filepath.Join(t.TempDir(), "repos.json"))
	for _, h := range []string{"Bearer not-a-known-value", "Basic dXNlcjpwYXNz", testApp} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
		req.Header.Set("Authorization", h)
		rec := httptest.NewRecorder()
		d.handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "objects-demo does not know this token") {
			t.Errorf("Authorization %q: %d %q, want 401 naming the fix", h, rec.Code, rec.Body.String())
		}
	}
}

// page is one repo_list answer as Straza reads it.
type page struct {
	Objects []struct {
		Value      string            `json:"value"`
		ID         json.RawMessage   `json:"id"`
		Title      string            `json:"title"`
		Attributes map[string]string `json:"attributes"`
	} `json:"objects"`
	NextCursor string `json:"nextCursor"`
}

func listAll(t *testing.T, s *mcp.ClientSession) []page {
	t.Helper()
	var pages []page
	args := map[string]any{}
	for range 10 {
		res, text := call(t, s, "repo_list", args)
		if res.IsError {
			t.Fatalf("repo_list answered an error: %s", text)
		}
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var p page
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("structuredContent %s: %v", raw, err)
		}
		pages = append(pages, p)
		if p.NextCursor == "" {
			return pages
		}
		args = map[string]any{"cursor": p.NextCursor}
	}
	t.Fatal("repo_list never ended its listing")
	return nil
}

func TestRepoListPages(t *testing.T) {
	url, _ := startDemo(t)
	s := connect(t, url, testListing)
	pages := listAll(t, s)
	var sizes []int
	var cursors []string
	seen := map[string]bool{}
	for _, p := range pages {
		sizes = append(sizes, len(p.Objects))
		cursors = append(cursors, p.NextCursor)
		for _, o := range p.Objects {
			if seen[o.Value] {
				t.Errorf("%s listed twice", o.Value)
			}
			seen[o.Value] = true
			if len(o.ID) == 0 || o.ID[0] == '"' {
				t.Errorf("%s: id %s is not a JSON number", o.Value, o.ID)
			}
			if o.Attributes["area"] == "" {
				t.Errorf("%s carries no area", o.Value)
			}
		}
	}
	if !reflect.DeepEqual(sizes, []int{4, 4, 2}) || !reflect.DeepEqual(cursors, []string{"4", "8", ""}) {
		t.Errorf("pages %v with cursors %q, want [4 4 2] with [4 8 \"\"]", sizes, cursors)
	}
	first := pages[0].Objects[0]
	if first.Value != "Strazahq/straza" || string(first.ID) != "9012331" || first.Title != "straza" || first.Attributes["area"] != "product" {
		t.Errorf("first repo = %+v", first)
	}
	for _, bad := range []string{"0", "-4", "x", "10", "99"} {
		res, text := call(t, s, "repo_list", map[string]any{"cursor": bad})
		if !res.IsError || !strings.Contains(text, "Start the listing again without a cursor") {
			t.Errorf("cursor %q answered %q, want an error that names the fix", bad, text)
		}
	}
}
