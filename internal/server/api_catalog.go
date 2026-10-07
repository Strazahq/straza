package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// Catalog-preview status taxonomy (GET /v1/admin/catalog/preview). Each entry
// is what a subject would see for one (app, tool), or one app-level row when
// the outcome is app-scoped (no binding / not running).
const (
	previewVisible      = "visible"       // allowed, plain; served with the raw schema
	previewApproveGated = "approve_gated" // allowed but a mode:approve rule gates it
	previewHiddenPolicy = "hidden_policy" // a policy rule denies it (reason carries why)
	previewNoBinding    = "no_binding"    // running app, but no binding exposes it to these roles
	previewMatcherMiss  = "matcher_miss"  // bound, but this tool name misses every matcher
	previewNotRunning   = "not_running"   // bound, but the app is not currently running
)

// previewSubject echoes the subject the preview was computed for.
type previewSubject struct {
	User  string   `json:"user"`
	Roles []string `json:"roles"`
}

// previewEntry is one row of the preview: the status a subject would get for a
// tool (or an app, when tool is empty). RuleID/SetName name the policy rule
// that produced a tool-level outcome (allow, approve, or deny); both are empty
// when no rule fired. Default marks the fail-closed default-deny: nothing
// mentions this call yet, which is a different fact from a rule refusing it
// on purpose, and the console renders the two differently (0.71.0).
type previewEntry struct {
	App     string `json:"app"`
	Tool    string `json:"tool"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	RuleID  string `json:"ruleId,omitempty"`
	SetName string `json:"setName,omitempty"`
	Default bool   `json:"default,omitempty"`
	// Hint is one plain sentence per status, always present, the words the
	// CLI and the console print for the row.
	Hint string `json:"hint"`
}

// catalogPreviewResponse is the preview body. Notes carries one sentence
// per role name in the role lane that is no role in Straza; absent when
// every name exists.
type catalogPreviewResponse struct {
	Subject previewSubject `json:"subject"`
	Entries []previewEntry `json:"entries"`
	Notes   []string       `json:"notes,omitempty"`
}

// handleCatalogPreview answers "what would this subject see, and why" over the
// live catalog (GET /v1/admin/catalog/preview). It is a control-plane admin
// endpoint, NOT a request path, so it may read the store to resolve a user's
// roles. The user lane (`user=<username>`) resolves the subject's roles from
// the store and leaves attestation, deviceCert and harness zero, so
// require-gated rules may differ from a live session. The role lane
// (`role=<name>`, repeatable) passes Subject{Roles: roles} only. At least one
// of role or user is required, else 400.
//
// `app=<name>` narrows the preview to one app. `assume=<matcher>[,<matcher>]`
// (repeatable and comma-splittable) merges hypothetical tool matchers for the
// FILTERED app into the subject's bindings before the probe, so a role that
// binds nothing yet still gets per-tool policy truth instead of one no_binding
// row. Assumed matchers are app-scoped by definition, so assume without app is
// a 400, never silently ignored.
func (a *App) handleCatalogPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	roleParams := q["role"]
	userParam := q.Get("user")
	appFilter := q.Get("app")
	if len(roleParams) == 0 && userParam == "" {
		apiError(w, http.StatusBadRequest, "at least one of role or user is required")
		return
	}
	var assume []string
	for _, v := range q["assume"] {
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				assume = append(assume, m)
			}
		}
	}
	if len(assume) > 0 && appFilter == "" {
		apiError(w, http.StatusBadRequest, "assume requires app: assumed matchers are scoped to one server")
		return
	}

	var sub policy.Subject
	var notes []string
	if userParam != "" {
		u, err := a.store.Users().GetByUsername(r.Context(), userParam)
		if err != nil {
			apiError(w, http.StatusNotFound, "no such user")
			return
		}
		roles, err := a.resolver.ResolveRoles(r.Context(), u.ID, time.Now())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
			return
		}
		roleNames := make([]string, len(roles))
		for i, ro := range roles {
			roleNames[i] = ro.Name
		}
		sub = policy.Subject{
			User: u.Username, Roles: roleNames,
			UserType: u.UserType, AgencyMode: u.AgencyMode, SwarmID: u.SwarmID,
		}
	} else {
		var err error
		if sub, notes, err = a.roleLaneSubject(r.Context(), roleParams); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, catalogPreviewResponse{
		Subject: previewSubject{
			User:  sub.User,
			Roles: nonNilStrings(sub.Roles),
		},
		Entries: a.previewEntries(sub, appFilter, assume),
		Notes:   notes,
	})
}

// previewEntries computes the status taxonomy for a subject over the live
// manager views and in-memory bindings, probing the active engine per bound
// tool. It reuses the same intersection the tier-1 catalog build applies
// (inventory ∩ exposure ∩ bindings) plus the tier-2 policy probe, but emits a
// diagnosable row per outcome instead of a filtered list. Non-empty assume
// matchers join the appFilter app's binding set (the caller has already
// guaranteed appFilter is set when assume is).
func (a *App) previewEntries(sub policy.Subject, appFilter string, assume []string) []previewEntry {
	held := make(map[string]bool, len(sub.Roles))
	for _, role := range sub.Roles {
		held[role] = true
	}
	bindings, _ := a.gateway.bindings.Load().([]gwBinding)
	matchersByApp := map[string][]string{}
	for _, b := range bindings {
		if held[b.Role] {
			matchersByApp[b.App] = append(matchersByApp[b.App], b.Matchers...)
		}
	}
	if len(assume) > 0 && appFilter != "" {
		matchersByApp[appFilter] = append(matchersByApp[appFilter], assume...)
	}

	eng := a.snapshots.Current().Engine
	var entries []previewEntry
	seen := map[string]bool{}
	for _, view := range a.manager.Views() {
		if appFilter != "" && view.Name != appFilter {
			continue
		}
		seen[view.Name] = true
		matchers, bound := matchersByApp[view.Name]
		running := view.Status == manager.StatusRunning || view.Status == manager.StatusDegraded
		// A bound app that cannot serve (not running, or running with no
		// known tools) is one app-level row carrying the probe reason; an
		// unbound one is simply invisible, no row.
		if !running || len(view.Tools) == 0 {
			if bound {
				entries = append(entries, previewEntry{
					App: view.Name, Status: previewNotRunning, Reason: view.Detail,
					Hint: hintNotRunning(view.Status, view.Detail),
				})
			}
			continue
		}
		if !bound {
			entries = append(entries, previewEntry{App: view.Name, Status: previewNoBinding, Hint: hintNoBinding})
			continue
		}
		for _, t := range view.Tools {
			entries = append(entries, previewToolEntry(eng, sub, view.Name, t, matchers))
		}
	}
	// Bound apps with no managed instance at all (stopped/removed) still surface
	// as not_running so the operator sees the binding is inert.
	for appName := range matchersByApp {
		if seen[appName] || (appFilter != "" && appName != appFilter) {
			continue
		}
		entries = append(entries, previewEntry{App: appName, Status: previewNotRunning, Hint: hintNotRunning("", "")})
	}
	return entries
}

// previewToolEntry classifies one exposed tool for the subject. The matcher
// test is the binding check, so a tool it admits is probed with the granted
// fact (spec/policyset revision 17), exactly as the gateway evaluates it.
func previewToolEntry(eng *policy.Engine, sub policy.Subject, app string, t *mcp.Tool, matchers []string) previewEntry {
	if !manager.MatchAnyGlob(matchers, t.Name) {
		return previewEntry{App: app, Tool: t.Name, Status: previewMatcherMiss, Hint: hintMatcherMiss}
	}
	d := eng.Evaluate(policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: app, ToolName: t.Name, Granted: true,
	}, sub)
	// The deciding rule travels on every tool-level row: the engine already knows
	// which rule fired (or that the default applied), and dropping that here
	// forced the console to guess.
	e := previewEntry{
		App: app, Tool: t.Name, Reason: d.Reason,
		RuleID: d.RuleID, SetName: d.SetName, Default: d.Default,
	}
	switch {
	case d.Effect != policy.EffectAllow:
		e.Status = previewHiddenPolicy
		e.Hint = hintHiddenPolicy(d)
		if e.Reason == "" {
			e.Reason = e.Hint
		}
	case d.Approve != nil:
		e.Status = previewApproveGated
		e.Hint = hintApproveGated(d)
	default:
		e.Status = previewVisible
		e.Hint = hintVisible
	}
	return e
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
