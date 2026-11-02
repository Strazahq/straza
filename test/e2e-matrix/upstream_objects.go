package e2ematrix

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The object marks a server sends on its tools and the _meta key the
// gateway sets on a call a role on some objects granted, from
// spec/app-manifest/SPEC.md section 7 and the "Marks for server authors"
// section of website/content/reference/spec.md.
const (
	markObject      = "ai.straza/object"
	markListsObject = "ai.straza/lists-objects"
	metaObjectID    = "ai.straza/object-id"
)

// objectRepo is one repo /mcp-objects lists. The id is a number, as a
// code host's repository ids are, so the listing sends it as a JSON number
// and Straza keeps its decimal string.
type objectRepo struct {
	value string
	id    int64
	team  string
}

// objectRepos are the seven repos repo_list serves in id order, three to a
// page, so a whole listing takes three pages joined by nextCursor.
var objectRepos = []objectRepo{
	{"acme/payments", 1001, "payments"},
	{"acme/payments-web", 1002, "payments"},
	{"acme/billing", 1003, "billing"},
	{"acme/site", 1004, "site"},
	{"acme/docs", 1005, "site"},
	{"acme/infra", 1006, "ops"},
	{"acme/ledger", 1007, "billing"},
}

// objectPage is the number of repos on one repo_list page.
const objectPage = 3

// objectTools are the four tools of /mcp-objects, one per mark: repo_list
// lists the repos, repo_read names one repo in its repo argument,
// search_code reaches every repo, and calls_seen touches none and says
// which repo_read calls reached the server, held repo or not.
func objectTools() []any {
	str := map[string]any{"type": "string"}
	mark := func(key, value string) map[string]any {
		return map[string]any{markObject: map[string]any{"type": "repo", key: value}}
	}
	readOnly := map[string]any{"readOnlyHint": true}
	return []any{
		map[string]any{"name": "repo_list", "description": "List the repos, three to a page. Pass a page's nextCursor to get the next page.",
			"annotations": readOnly,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"cursor": str}},
			"_meta": map[string]any{markListsObject: map[string]any{"type": "repo",
				"attributes": []any{map[string]any{"name": "team", "setBy": "org"}}}}},
		map[string]any{"name": "repo_read", "description": "Read one repo, and say whether the call carried ai.straza/object-id.",
			"annotations": readOnly,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"repo": str}, "required": []string{"repo"}},
			"_meta":       mark("argument", "repo")},
		map[string]any{"name": "search_code", "description": "Search every repo for the query, which this upstream matches against repo names.",
			"annotations": readOnly,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": str}, "required": []string{"query"}},
			"_meta":       mark("reach", "all")},
		map[string]any{"name": "calls_seen", "description": "Say which repo_read calls reached this server. Touches no repo.",
			"annotations": readOnly,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			"_meta":       mark("reach", "none")},
	}
}

// callObjects answers one tools/call on /mcp-objects.
func (u *upstream) callObjects(msg rpcMessage) any {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Meta      map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return rpcErr(msg.ID, -32602, "params must carry name and arguments")
	}
	switch p.Name {
	case "repo_list":
		return repoListPage(msg.ID, p.Arguments)
	case "repo_read":
		// Every call that reaches the server is logged, a repo it does not
		// hold included, so calls_seen proves a refused call never arrived.
		value, _ := p.Arguments["repo"].(string)
		line := "repo_read on " + value + " with no " + metaObjectID
		if id, ok := p.Meta[metaObjectID]; ok {
			line = fmt.Sprintf("repo_read on %s with %s %v", value, metaObjectID, id)
		}
		u.mu.Lock()
		u.reads = append(u.reads, line)
		u.mu.Unlock()
		if !holdsRepo(value) {
			return rpcErr(msg.ID, -32602, fmt.Sprintf("repo_read: this upstream holds no repo named %q", value))
		}
		return textResult(msg.ID, line+".")
	case "search_code":
		query, _ := p.Arguments["query"].(string)
		var hits []string
		for _, r := range objectRepos {
			if strings.Contains(r.value, query) {
				hits = append(hits, r.value)
			}
		}
		return textResult(msg.ID, fmt.Sprintf("search_code matched %d repos: %s.", len(hits), strings.Join(hits, ", ")))
	case "calls_seen":
		u.mu.Lock()
		text := fmt.Sprintf("repo_read calls: %d.", len(u.reads))
		for _, line := range u.reads {
			text += " " + line + "."
		}
		u.mu.Unlock()
		return textResult(msg.ID, text)
	}
	return rpcErr(msg.ID, -32602, fmt.Sprintf("unknown tool %q", p.Name))
}

// repoListPage answers the repo_list page the cursor names in the result
// shape of spec/app-manifest/SPEC.md section 7.2: structuredContent with
// objects and nextCursor, absent on the last page, and the same document
// as text for a client that reads only content.
func repoListPage(id json.RawMessage, args map[string]any) any {
	start := 0
	if c, _ := args["cursor"].(string); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n <= 0 || n >= len(objectRepos) {
			return rpcErr(id, -32602, fmt.Sprintf("repo_list never answered the cursor %q. Start again without a cursor", c))
		}
		start = n
	}
	end := min(start+objectPage, len(objectRepos))
	items := make([]any, 0, end-start)
	for _, r := range objectRepos[start:end] {
		items = append(items, map[string]any{"value": r.value, "id": r.id,
			"title": r.value[strings.LastIndex(r.value, "/")+1:], "attributes": map[string]any{"team": r.team}})
	}
	page := map[string]any{"objects": items}
	if end < len(objectRepos) {
		page["nextCursor"] = strconv.Itoa(end)
	}
	raw, _ := json.Marshal(page)
	return rpcResult(id, map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(raw)}},
		"structuredContent": page,
	})
}

// holdsRepo reports whether value names a repo of the list exactly.
func holdsRepo(value string) bool {
	for _, r := range objectRepos {
		if r.value == value {
			return true
		}
	}
	return false
}

// textResult is a tool result with one text item.
func textResult(id json.RawMessage, text string) map[string]any {
	return rpcResult(id, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
}
