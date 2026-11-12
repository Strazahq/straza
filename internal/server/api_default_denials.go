package server

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// The read behind the console's Denied by default view: the tool calls of
// the decision block's window that no rule decided, on an MCP server Straza
// has not registered or on a harness tool it does not classify. It rides
// the scan the overview's decision block already runs, so it costs no scan
// of its own, and it carries no caller and no arguments, which belong to
// the audit area.

// defaultDenialMax caps the tools the scan tracks. A harness names its own
// tools, so the names are the caller's to choose, and the cap bounds what
// one window can make strazad hold.
const defaultDenialMax = 200

// defaultDenial accumulates the default denials of one tool: a tool of an
// MCP server, or a harness tool, which has no server.
type defaultDenial struct {
	kind, app, tool string
	harnesses       map[string]bool
	count           int
	lastSeq         int64
	lastTime        time.Time
	// covered is true once a newer record of the same tool was allowed or
	// was decided by a rule, so the tool is no longer denied by default.
	covered bool
}

// denialTally is what one scan knows of the tools denied by default. cut
// says a tool went untracked because the scan already held
// defaultDenialMax of them.
type denialTally struct {
	groups map[string]*defaultDenial
	cut    bool
}

// tally reads one decision record. A deny that names no rule, of an
// mcp.call or of the kind other, counts for its tool. A record that names
// a rule marks a tracked tool covered. An allow that names none does not,
// because a client can spool one for a call the gateway decides.
func (t *denialTally) tally(ce decisionCE, seq int64) {
	if ce.Type != "straza.audit.tool" || (ce.Data.Tool != policy.ToolMCPCall && ce.Data.Tool != policy.ToolOther) {
		return
	}
	key := ce.Data.Tool + "\x00" + ce.Data.App + "\x00" + ce.Data.ToolName
	g := t.groups[key]
	if ce.Data.RuleID != "" {
		if g != nil {
			g.covered = true
		}
		return
	}
	if ce.Data.Effect != policy.EffectDeny {
		return
	}
	groups := t.groups
	if g == nil {
		if len(groups) >= defaultDenialMax {
			t.cut = true
			return
		}
		g = &defaultDenial{kind: ce.Data.Tool, app: ce.Data.App, tool: ce.Data.ToolName, harnesses: map[string]bool{}}
		groups[key] = g
	}
	g.count, g.covered, g.lastSeq, g.lastTime = g.count+1, false, seq, ce.Time.UTC()
	if name, _, _ := strings.Cut(ce.Data.Harness, "/"); name != "" {
		g.harnesses[name] = true
	}
}

// deniedToolPayload is one tool that was denied by default: how often in
// the window, and the newest record that says so.
type deniedToolPayload struct {
	Name      string    `json:"name"`
	Harnesses []string  `json:"harnesses"`
	Count     int       `json:"count"`
	LastSeq   int64     `json:"lastSeq"`
	LastTime  time.Time `json:"lastTime"`
}

// deniedServerPayload is one MCP server Straza has not registered, with
// the tools of it that were denied by default.
type deniedServerPayload struct {
	Server    string              `json:"server"`
	Harnesses []string            `json:"harnesses"`
	Count     int                 `json:"count"`
	LastSeq   int64               `json:"lastSeq"`
	LastTime  time.Time           `json:"lastTime"`
	Tools     []deniedToolPayload `json:"tools"`
}

// defaultDenialsPayload answers GET /v1/admin/default-denials. Servers and
// Tools are newest first. Truncated says the window named more tools than
// the scan tracks.
type defaultDenialsPayload struct {
	Since     time.Time             `json:"since"`
	Servers   []deniedServerPayload `json:"servers"`
	Tools     []deniedToolPayload   `json:"tools"`
	Truncated bool                  `json:"truncated,omitempty"`
}

// defaultDenials answers the tools denied by default in the decision
// block's window, from the block's own scan and cache.
func (a *App) defaultDenials(ctx context.Context) (time.Time, []defaultDenial, bool, error) {
	block, err := a.decisionsBlock(ctx)
	if err != nil {
		return time.Time{}, nil, false, err
	}
	a.decisions.mu.Lock()
	defer a.decisions.mu.Unlock()
	return block.Since, a.decisions.denials, a.decisions.denialsCut, nil
}

// handleDefaultDenials lists the tools that were denied because no rule
// covers them. A server registered since its calls were denied is left
// out, because role access decides its tools now.
func (a *App) handleDefaultDenials(w http.ResponseWriter, r *http.Request) {
	since, denials, cut, err := a.defaultDenials(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "default denials: decisions unavailable", err)
		return
	}
	apps, err := a.store.Apps().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "default denials: MCP servers unavailable", err)
		return
	}
	registered := map[string]bool{nativeAppName: true}
	for _, app := range apps {
		registered[app.Name] = true
	}
	writeJSON(w, http.StatusOK, defaultDenialsOf(since, denials, cut, registered))
}

// defaultDenialsOf renders the tracked denials: the harness tools, and the
// MCP tools grouped under each server registered does not hold.
func defaultDenialsOf(since time.Time, denials []defaultDenial, cut bool, registered map[string]bool) defaultDenialsPayload {
	out := defaultDenialsPayload{Since: since, Servers: []deniedServerPayload{}, Tools: []deniedToolPayload{}, Truncated: cut}
	servers := map[string]*deniedServerPayload{}
	for _, d := range denials {
		if d.covered || (d.kind == policy.ToolMCPCall && registered[d.app]) {
			continue
		}
		tool := deniedToolPayload{Name: d.tool, Harnesses: sortedNames(d.harnesses), Count: d.count, LastSeq: d.lastSeq, LastTime: d.lastTime}
		if d.kind != policy.ToolMCPCall {
			out.Tools = append(out.Tools, tool)
			continue
		}
		s := servers[d.app]
		if s == nil {
			s = &deniedServerPayload{Server: d.app, Harnesses: []string{}}
			servers[d.app] = s
		}
		s.Tools, s.Count = append(s.Tools, tool), s.Count+d.count
		if d.lastSeq > s.LastSeq {
			s.LastSeq, s.LastTime = d.lastSeq, d.lastTime
		}
		s.Harnesses = append(s.Harnesses, tool.Harnesses...)
	}
	newest := func(x, y deniedToolPayload) int { return cmp.Compare(y.LastSeq, x.LastSeq) }
	for _, s := range servers {
		slices.Sort(s.Harnesses)
		s.Harnesses = slices.Compact(s.Harnesses)
		slices.SortFunc(s.Tools, newest)
		out.Servers = append(out.Servers, *s)
	}
	slices.SortFunc(out.Servers, func(x, y deniedServerPayload) int { return cmp.Compare(y.LastSeq, x.LastSeq) })
	slices.SortFunc(out.Tools, newest)
	return out
}

// sortedNames answers the names set holds, sorted, and never nil.
func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
