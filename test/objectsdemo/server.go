package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The marks a server sends in tool _meta, under Straza's reverse-DNS prefix,
// and the key Straza sets on a call's _meta with the id of the object it
// approved.
const (
	markObject = "ai.straza/object"
	markLists  = "ai.straza/lists-objects"
	metaID     = "ai.straza/object-id"
)

// objectType is the one type objects-demo marks, and pageSize the number of
// repos on one repo_list page.
const (
	objectType = "repo"
	pageSize   = 4
)

// caller says which token a request carried, in the words whoami answers.
type caller string

const (
	appToken     caller = "the app token"
	listingToken caller = "the listing token"
	noToken      caller = "no token"
	unknownToken caller = "a token objects-demo does not know"
)

// tokens are objects-demo's two bearer tokens: the app token for every tool
// but repo_list, and the listing token for repo_list alone.
type tokens struct{ app, listing string }

// classify answers which token the Authorization header in h carries.
func (t tokens) classify(h http.Header) caller {
	auth := h.Get("Authorization")
	if auth == "" {
		return noToken
	}
	value, ok := strings.CutPrefix(auth, "Bearer ")
	switch {
	case !ok:
		return unknownToken
	case subtle.ConstantTimeCompare([]byte(value), []byte(t.app)) == 1:
		return appToken
	case subtle.ConstantTimeCompare([]byte(value), []byte(t.listing)) == 1:
		return listingToken
	}
	return unknownToken
}

// demo is the objects-demo MCP server: its tokens, its state file and the
// count of issues it opened.
type demo struct {
	tokens tokens
	state  string
	issues atomic.Int64
	server *mcp.Server
}

func newDemo(t tokens, state string) *demo {
	d := &demo{tokens: t, state: state}
	d.server = mcp.NewServer(&mcp.Implementation{Name: "objects-demo", Version: "1.0.0"}, nil)
	d.addTools()
	return d
}

// handler serves MCP over streamable HTTP. A request whose token is neither
// of the two is refused before MCP reads it, so a wrong secret in Straza
// shows as a failed start instead of failing tool calls.
func (d *demo) handler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return d.server }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.tokens.classify(r.Header) == unknownToken {
			http.Error(w, "objects-demo does not know this token. Send its app token or its listing token as Authorization: Bearer <token>.", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// callerOf answers which token a call carried and logs it, never its value.
func (d *demo) callerOf(req *mcp.CallToolRequest) caller {
	got := noToken
	if req.Extra != nil {
		got = d.tokens.classify(req.Extra.Header)
	}
	log.Printf("%s called with %s", req.Params.Name, got)
	return got
}

// require answers nil when the call carried want, else the refusal the
// tool answers.
func (d *demo) require(req *mcp.CallToolRequest, want caller) error {
	tool, got := req.Params.Name, d.callerOf(req)
	if got == want {
		return nil
	}
	if got == listingToken {
		return fmt.Errorf("%s refuses the listing token: it answers only to objects-demo's app token. A listing token on a tool call means the gateway sent the wrong credential", tool)
	}
	return fmt.Errorf("%s answers only to objects-demo's %s, and this call carried %s", tool, strings.TrimPrefix(string(want), "the "), got)
}

// objectMark is the ai.straza/object mark of a tool: an argument that
// names the repo, or a reach of all or none.
func objectMark(key, value string) mcp.Meta {
	return mcp.Meta{markObject: map[string]any{"type": objectType, key: value}}
}

type listInput struct {
	Cursor string `json:"cursor,omitempty" jsonschema:"the nextCursor of the previous page. Leave it out for the first page."`
}

type listPage struct {
	Objects    []listItem `json:"objects"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

type repoInput struct {
	Repo string `json:"repo" jsonschema:"the repo's full name, such as Strazahq/straza"`
}

type issueInput struct {
	Repo  string `json:"repo" jsonschema:"the repo's full name, such as Strazahq/straza"`
	Title string `json:"title" jsonschema:"the title of the issue"`
}

type searchInput struct {
	Query string `json:"query" jsonschema:"the text to search for"`
}

func (d *demo) addTools() {
	mcp.AddTool(d.server, &mcp.Tool{
		Name:        "repo_list",
		Description: "Lists the repos objects-demo holds, four per page. Pass a page's nextCursor to get the next page. Answers only to the listing token.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta: mcp.Meta{markLists: map[string]any{
			"type": objectType, "attributes": []map[string]string{{"name": "area", "setBy": "org"}},
		}},
	}, d.repoList)
	mcp.AddTool(d.server, &mcp.Tool{
		Name:        "repo_read",
		Description: "Shows one repo: its id, its title and its area.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        objectMark("argument", "repo"),
	}, d.repoRead)
	mcp.AddTool(d.server, &mcp.Tool{
		Name:        "issue_create",
		Description: "Opens an issue in one repo.",
		Meta:        objectMark("argument", "repo"),
	}, d.issueCreate)
	mcp.AddTool(d.server, &mcp.Tool{
		Name:        "search_code",
		Description: "Searches the code of every repo objects-demo holds. This demo matches the query against repo names.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        objectMark("reach", "all"),
	}, d.searchCode)
	mcp.AddTool(d.server, &mcp.Tool{
		Name:        "whoami",
		Description: "Says which of objects-demo's tokens this call carried: the app token, the listing token or none. Never a token's value.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        objectMark("reach", "none"),
	}, d.whoami)
}

func (d *demo) repoList(_ context.Context, req *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, listPage, error) {
	if err := d.require(req, listingToken); err != nil {
		return nil, listPage{}, err
	}
	repos, err := loadRepos(d.state)
	if err != nil {
		return nil, listPage{}, err
	}
	start := 0
	if in.Cursor != "" {
		start, err = strconv.Atoi(in.Cursor)
		if err != nil || start <= 0 || start >= len(repos) {
			return nil, listPage{}, fmt.Errorf("repo_list never answered the cursor %q for its %d repos. Start the listing again without a cursor", in.Cursor, len(repos))
		}
	}
	end := min(start+pageSize, len(repos))
	page := listPage{Objects: make([]listItem, 0, end-start)}
	for _, r := range repos[start:end] {
		page.Objects = append(page.Objects, r.item())
	}
	if end < len(repos) {
		page.NextCursor = strconv.Itoa(end)
	}
	log.Printf("repo_list answered repos %d to %d of %d", start+1, end, len(repos))
	return nil, page, nil
}

// find answers the repo whose name is exactly value: no case folding and
// no alias, as the object marks promise.
func (d *demo) find(value string) (repo, error) {
	repos, err := loadRepos(d.state)
	if err != nil {
		return repo{}, err
	}
	for _, r := range repos {
		if r.Value == value {
			return r, nil
		}
	}
	return repo{}, fmt.Errorf("objects-demo holds no repo named %q. Check the name, which must match exactly, case included", value)
}

// findSent answers the repo named value, and refuses it when the call's
// ai.straza/object-id names another id: the repo was deleted and made again
// under the same name after Straza listed it. A call without the key is a
// call through a role on every repo and resolves the name alone.
func (d *demo) findSent(req *mcp.CallToolRequest, value string) (repo, error) {
	r, err := d.find(value)
	if err != nil {
		return repo{}, err
	}
	raw, ok := req.Params.Meta[metaID]
	if !ok {
		return r, nil
	}
	if sent, _ := raw.(string); sent != strconv.FormatInt(r.ID, 10) {
		return repo{}, fmt.Errorf("%s refused the call: Straza approved the repo with the id %v, and %s has the id %d now, so it was made again after Straza listed it. Refresh the repo list in Straza, then try again",
			req.Params.Name, raw, r.Value, r.ID)
	}
	return r, nil
}

func text(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}
}

func (d *demo) repoRead(_ context.Context, req *mcp.CallToolRequest, in repoInput) (*mcp.CallToolResult, any, error) {
	if err := d.require(req, appToken); err != nil {
		return nil, nil, err
	}
	r, err := d.findSent(req, in.Repo)
	if err != nil {
		return nil, nil, err
	}
	it := r.item()
	area := r.Area
	if area == "" {
		area = "none"
	}
	return text("%s: id %d, title %s, area %s.", it.Value, it.ID, it.Title, area), nil, nil
}

func (d *demo) issueCreate(_ context.Context, req *mcp.CallToolRequest, in issueInput) (*mcp.CallToolResult, any, error) {
	if err := d.require(req, appToken); err != nil {
		return nil, nil, err
	}
	r, err := d.findSent(req, in.Repo)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, nil, errors.New("issue_create needs a title. Send one in the title argument")
	}
	return text("Opened issue #%d in %s: %s", d.issues.Add(1), r.Value, in.Title), nil, nil
}

func (d *demo) searchCode(_ context.Context, req *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
	if err := d.require(req, appToken); err != nil {
		return nil, nil, err
	}
	repos, err := loadRepos(d.state)
	if err != nil {
		return nil, nil, err
	}
	var hits []string
	for _, r := range repos {
		if strings.Contains(strings.ToLower(r.Value), strings.ToLower(in.Query)) {
			hits = append(hits, r.Value)
		}
	}
	if len(hits) == 0 {
		return text("No repo matched %q.", in.Query), nil, nil
	}
	return text("%d repos matched %q: %s.", len(hits), in.Query, strings.Join(hits, ", ")), nil, nil
}

func (d *demo) whoami(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	return text("This call carried %s.", d.callerOf(req)), nil, nil
}
