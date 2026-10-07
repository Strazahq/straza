package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// The list surface (openapi 0.78.0): GET /v1/admin/policies answers an
// envelope of summary-only rows with server-side filters applied before
// paging plus a per-role facet over ALL matches, and GET
// /v1/admin/policies/{name} carries the full payload (YAML included) for
// the editor. Summaries come from an in-memory cache keyed by updated_at,
// so a list call re-parses nothing that has not changed and the running
// binary always computes its own summary shape.

// Lane vocabulary: the tool taxonomy folded to the five surfaces a rule
// can govern. The facet special role names: "*" = match-all sets (no
// selector, they cover everyone), "~" = users/identity-scoped sets
// outside every role group.
const (
	laneMCP   = "mcp"
	laneShell = "shell"
	laneFiles = "files"
	laneNet   = "net"
	laneOther = "other"

	facetEveryone = "*"
	facetOutside  = "~"
)

var allLanes = []string{laneMCP, laneShell, laneFiles, laneNet, laneOther}

// ruleLanes classifies one rule onto the lanes it can govern. Explicit
// tools map directly; a tool-less rule classifies by its matcher shape
// (apps/toolNames only exist on MCP calls, command/interpreters on shell,
// paths on files); a rule with neither tools nor matchers governs every
// lane unless its events bind it to non-tool moments only.
func ruleLanes(r policy.Rule) []string {
	if len(r.Tools) > 0 {
		seen := map[string]bool{}
		var out []string
		add := func(l string) {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
		for _, tool := range r.Tools {
			switch tool {
			case policy.ToolMCPCall:
				add(laneMCP)
			case policy.ToolShellExec:
				add(laneShell)
			case policy.ToolFileRead, policy.ToolFileWrite, policy.ToolFileEdit:
				add(laneFiles)
			case policy.ToolNetFetch:
				add(laneNet)
			default:
				add(laneOther)
			}
		}
		return out
	}
	var out []string
	if len(r.Apps) > 0 || r.ToolNames != nil {
		out = append(out, laneMCP)
	}
	if r.Command != nil || r.Interpreters != nil {
		out = append(out, laneShell)
	}
	if r.Paths != nil {
		out = append(out, laneFiles)
	}
	if len(out) > 0 {
		return out
	}
	if len(r.Events) > 0 {
		toolEvent := false
		for _, ev := range r.Events {
			if ev == policy.EventToolPre || ev == policy.EventToolPost || ev == policy.EventPermissionRequest {
				toolEvent = true
			}
		}
		if !toolEvent {
			return nil
		}
	}
	return allLanes
}

// setLanes folds every rule's posture into its lanes (lane -> posture ->
// count); nil when no rule governs a tool lane.
func setLanes(doc policy.Document) map[string]map[string]int {
	var lanes map[string]map[string]int
	for _, r := range doc.Spec.Rules {
		posture := rulePosture(r)
		for _, l := range ruleLanes(r) {
			if lanes == nil {
				lanes = map[string]map[string]int{}
			}
			if lanes[l] == nil {
				lanes[l] = map[string]int{}
			}
			lanes[l][posture]++
		}
	}
	return lanes
}

// policySummaryEntry is one cached decomposition: valid for exactly the
// updated_at it was computed from, so a write of the row (which bumps
// updated_at) structurally invalidates it. sum stays nil for an
// unparseable source; caching that too keeps a rotten set from being
// re-parsed every call.
type policySummaryEntry struct {
	updatedAt time.Time
	sum       *policySummary
}

type policySummaryCache struct {
	mu sync.Mutex
	m  map[string]policySummaryEntry
}

// fill returns the meta rows paired with their cache entries. Rows whose
// entry is missing or stale trigger ONE full List to re-read YAML for all
// misses; ids that no longer exist are pruned so deletes cannot leak
// entries, and a row deleted between the two reads is dropped rather than
// served with an invented summary.
func (c *policySummaryCache) fill(ctx context.Context, st store.Store, metas []store.PolicySet) ([]store.PolicySet, []policySummaryEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]policySummaryEntry{}
	}
	stale := false
	for _, ps := range metas {
		if e, ok := c.m[ps.ID]; !ok || !e.updatedAt.Equal(ps.UpdatedAt) {
			stale = true
			break
		}
	}
	if stale {
		full, err := st.Policies().List(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, ps := range full {
			if e, ok := c.m[ps.ID]; ok && e.updatedAt.Equal(ps.UpdatedAt) {
				continue
			}
			entry := policySummaryEntry{updatedAt: ps.UpdatedAt}
			if doc, perr := policy.Parse([]byte(ps.YAMLSource)); perr == nil {
				entry.sum = summarizePolicy(doc)
			}
			c.m[ps.ID] = entry
		}
	}
	live := map[string]bool{}
	kept := make([]store.PolicySet, 0, len(metas))
	entries := make([]policySummaryEntry, 0, len(metas))
	for _, ps := range metas {
		e, ok := c.m[ps.ID]
		if !ok {
			continue
		}
		live[ps.ID] = true
		kept = append(kept, ps)
		entries = append(entries, e)
	}
	for id := range c.m {
		if !live[id] {
			delete(c.m, id)
		}
	}
	return kept, entries, nil
}

// policyListFilter is the parsed query surface; zero value = no filtering.
type policyListFilter struct {
	q         string
	status    string
	role      string
	lane      string
	recording *bool
	limit     int
	offset    int
}

// parsePolicyListFilter rejects invalid values with the accepted forms
// named; unknown params are ignored per house convention.
func parsePolicyListFilter(r *http.Request) (policyListFilter, string) {
	f := policyListFilter{q: r.URL.Query().Get("q"), role: r.URL.Query().Get("role")}
	switch v := r.URL.Query().Get("status"); v {
	case "", "active", "draft":
		f.status = v
	default:
		return f, "status must be active or draft"
	}
	switch v := r.URL.Query().Get("lane"); v {
	case "", laneMCP, laneShell, laneFiles, laneNet, laneOther:
		f.lane = v
	default:
		return f, "lane must be mcp, shell, files, net, or other"
	}
	if v := r.URL.Query().Get("recording"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, "recording must be a boolean (true or false)"
		}
		f.recording = &b
	}
	var err string
	if f.limit, err = nonNegInt(r, "limit"); err != "" {
		return f, err
	}
	if f.offset, err = nonNegInt(r, "offset"); err != "" {
		return f, err
	}
	return f, ""
}

func nonNegInt(r *http.Request, name string) (int, string) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, ""
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, name + " must be a non-negative integer"
	}
	return n, ""
}

func (f policyListFilter) match(ps store.PolicySet, sum *policySummary) bool {
	if f.q != "" {
		q := strings.ToLower(f.q)
		if !strings.Contains(strings.ToLower(ps.Name), q) &&
			(sum == nil || !strings.Contains(strings.ToLower(sum.Description), q)) {
			return false
		}
	}
	if f.status != "" && ps.Status != f.status {
		return false
	}
	switch f.role {
	case "":
	case facetEveryone:
		if sum == nil || len(sum.MatchRoles) > 0 || sum.MatchOther {
			return false
		}
	case facetOutside:
		if sum == nil || len(sum.MatchRoles) > 0 || !sum.MatchOther {
			return false
		}
	default:
		if sum == nil {
			return false
		}
		found := false
		for _, role := range sum.MatchRoles {
			if role == f.role {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if f.lane != "" {
		if sum == nil || sum.Lanes[f.lane] == nil {
			return false
		}
	}
	if f.recording != nil {
		if sum == nil || (sum.Capture != "") != *f.recording {
			return false
		}
	}
	return true
}

type policyRoleFacet struct {
	Role     string         `json:"role"`
	Sets     int            `json:"sets"`
	Postures map[string]int `json:"postures,omitempty"`
}

type policyListResponse struct {
	Items  []policyPayload   `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
	Roles  []policyRoleFacet `json:"roles"`
}

// roleFacet folds the filtered matches into per-role rollups: "*" first
// (match-all sets), role names sorted, "~" (users/identity scoped, no
// roles) last. Unparseable sets contribute nowhere; a multi-role set
// contributes to every role it names.
func roleFacet(matches []policyPayload) []policyRoleFacet {
	acc := map[string]*policyRoleFacet{}
	add := func(key string, postures map[string]int) {
		g := acc[key]
		if g == nil {
			g = &policyRoleFacet{Role: key}
			acc[key] = g
		}
		g.Sets++
		for posture, n := range postures {
			if g.Postures == nil {
				g.Postures = map[string]int{}
			}
			g.Postures[posture] += n
		}
	}
	for _, p := range matches {
		if p.Summary == nil {
			continue
		}
		switch {
		case len(p.Summary.MatchRoles) > 0:
			for _, role := range p.Summary.MatchRoles {
				add(role, p.Summary.Postures)
			}
		case p.Summary.MatchOther:
			add(facetOutside, p.Summary.Postures)
		default:
			add(facetEveryone, p.Summary.Postures)
		}
	}
	names := make([]string, 0, len(acc))
	for name := range acc {
		if name != facetEveryone && name != facetOutside {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]policyRoleFacet, 0, len(acc))
	if g := acc[facetEveryone]; g != nil {
		out = append(out, *g)
	}
	for _, name := range names {
		out = append(out, *acc[name])
	}
	if g := acc[facetOutside]; g != nil {
		out = append(out, *g)
	}
	return out
}

// savedEditPage is how many saved edits one read of the policy list takes.
const savedEditPage = 200

// savedEdit is a set's open saved edit as the policy reads answer it:
// its text, the document it parses to, nil when it does not, and
// when it was last saved.
type savedEdit struct {
	text      string
	doc       *policy.Document
	updatedAt time.Time
}

// over puts the saved edit's priority, summary and time in p with drift
// true, in place of the row's. A text that no longer parses keeps the
// row's priority and has no summary.
func (e savedEdit) over(p *policyPayload) {
	p.UpdatedAt, p.Drift, p.Summary = e.updatedAt.UTC().Format(time.RFC3339), true, nil
	if e.doc != nil {
		p.Priority, p.Summary = e.doc.Spec.Priority, summarizePolicy(*e.doc)
	}
}

// savedEditIn is the saved edit that the open slot draft d holds in items,
// its one put of the slot's set, and false for a draft of another shape.
func savedEditIn(d store.DraftRow, items []store.DraftItemRow) (string, savedEdit, bool) {
	_, set := slotWords(d.Slot)
	if len(items) != 1 || items[0].Kind != string(drafts.KindPolicySet) || items[0].Name != set || items[0].Op != string(drafts.OpPut) {
		return "", savedEdit{}, false
	}
	e := savedEdit{text: items[0].Doc, updatedAt: d.UpdatedAt}
	if doc, err := policy.Parse([]byte(e.text)); err == nil {
		e.doc = &doc
	}
	return set, e, true
}

// savedEdits reads the open saved edit of every set by name, in pages,
// with the items of each page in one read.
func (a *App) savedEdits(ctx context.Context) (map[string]savedEdit, error) {
	out := map[string]savedEdit{}
	for before := int64(0); ; {
		page, err := a.store.Drafts().List(ctx, store.DraftFilter{State: string(drafts.StateOpen), SlotPrefix: "policy:"}, before, savedEditPage)
		if err != nil || len(page) == 0 {
			return out, err
		}
		ids := make([]int64, len(page))
		for i, d := range page {
			ids[i] = d.ID
		}
		items, err := a.store.Drafts().Items(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, d := range page {
			if set, e, ok := savedEditIn(d, items[d.ID]); ok {
				out[set] = e
			}
		}
		if len(page) < savedEditPage {
			return out, nil
		}
		before = page[len(page)-1].ID
	}
}

// handlePolicyList answers the summary-only envelope: filters land before
// paging, the facet reads the whole match set, and no YAML crosses the
// wire (the editor fetches one set by name). A set with an open saved
// edit is answered and filtered with that edit's priority, summary and
// time, and drift true.
func (a *App) handlePolicyList(w http.ResponseWriter, r *http.Request) {
	f, bad := parsePolicyListFilter(r)
	if bad != "" {
		apiError(w, http.StatusBadRequest, bad)
		return
	}
	metas, err := a.store.Policies().ListMeta(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list policies failed", err)
		return
	}
	metas, entries, err := a.polSummaries.fill(r.Context(), a.store, metas)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list policies failed", err)
		return
	}
	edits, err := a.savedEdits(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list policies failed", err)
		return
	}
	matches := make([]policyPayload, 0, len(entries))
	for i, e := range entries {
		ps := metas[i]
		p := policyPayload{ID: ps.ID, Name: ps.Name, Priority: ps.Priority, Status: ps.Status,
			UpdatedAt: ps.UpdatedAt.UTC().Format(time.RFC3339), Summary: e.sum}
		if edit, ok := edits[ps.Name]; ok {
			edit.over(&p)
		}
		if !f.match(ps, p.Summary) {
			continue
		}
		matches = append(matches, p)
	}
	resp := policyListResponse{
		Total:  len(matches),
		Limit:  f.limit,
		Offset: f.offset,
		Roles:  roleFacet(matches),
	}
	window := matches
	if f.offset > 0 {
		if f.offset >= len(window) {
			window = nil
		} else {
			window = window[f.offset:]
		}
	}
	if f.limit > 0 && f.limit < len(window) {
		window = window[:f.limit]
	}
	resp.Items = window
	if resp.Items == nil {
		resp.Items = []policyPayload{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePolicyGet serves one stored set in full: the editor's fetch now
// that the list is summary-only. While the set has an open saved edit, the
// yaml is that edit's text, with its priority, summary and time and drift
// true, as the list rows answer it.
func (a *App) handlePolicyGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ps, err := a.store.Policies().GetByName(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such policy set")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, policyLookupFailed, err)
		return
	}
	out := policyPayload{
		ID: ps.ID, Name: ps.Name, Priority: ps.Priority, Status: ps.Status,
		YAML:      ps.YAMLSource,
		UpdatedAt: ps.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if doc, perr := policy.Parse([]byte(ps.YAMLSource)); perr == nil {
		out.Summary = summarizePolicy(doc)
	}
	d, items, err := a.store.Drafts().BySlot(r.Context(), "policy:"+name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, http.StatusInternalServerError, policyLookupFailed, err)
		return
	}
	if _, edit, ok := savedEditIn(d, items); err == nil && ok {
		edit.over(&out)
		out.YAML = edit.text
	}
	writeJSON(w, http.StatusOK, out)
}
