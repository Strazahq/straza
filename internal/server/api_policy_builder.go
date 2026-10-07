package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// Policy Builder server half: validate answers with
// the REAL parser (never a UI-side reimplementation); simulate evaluates an
// event + subject against the ACTIVE snapshot and, optionally, a draft
// PolicySet overlaid in place of its same-named stored set: the
// "active says ALLOW, draft says DENY" delta shown before activation.
// Admin plane: store reads are allowed; nothing here mutates state.

// handlePolicyValidate parses PolicySet YAML and answers ok + a structural
// summary, or 400 with the parser's message verbatim.
func (a *App) handlePolicyValidate(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxPolicyBody))
	if err != nil {
		apiError(w, http.StatusBadRequest, "could not read body")
		return
	}
	doc, err := policy.Parse(raw)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Revision 15: approve.roles must name approver-kind roles (or
	// straza-admin). Revision 16: match.roles must name application-kind
	// roles (the roles that carry tools; unknown names stay legal
	// selectors). Same gate as activation, same words, so the Builder and a
	// git-imported set hear the refusal at the same moment.
	if !a.roleGates(w, r, doc) {
		return
	}
	// Revision 18: the retired obligations list is refused with the fix
	// named. Same gate as activation, same words.
	if !a.obligationsGate(w, doc) {
		return
	}
	// The set compiles as at activation, so a Rego module activation would
	// refuse is refused here, in the compiler's words.
	if _, err := policy.NewEngine([]policy.Document{doc}, policy.EffectAllow); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	// One computation for validate AND the list summary (api_policy.go):
	// the two surfaces must never disagree about what a set contains.
	s := summarizePolicy(doc)
	out := map[string]any{
		"ok":       true,
		"name":     s.Name,
		"rules":    s.Rules,
		"priority": s.Priority,
	}
	if len(s.MatchRoles) > 0 {
		out["matchRoles"] = s.MatchRoles
	}
	if s.Capture != "" {
		out["capture"] = s.Capture
	}
	// Non-fatal author warnings (spec/policyset rev 10): a valid set can
	// still contain a predicate no subject can satisfy today; say so at
	// validate time, where the Builder shows it, not after activation.
	// warnings keeps the original []string wire shape; advisories is the
	// typed twin (0.84.0): code + severity + rule so the console keys
	// tone and placement without guessing from prose. Since 0.85.0 the
	// list also carries the server-known events coverage, via the same
	// helper the activate-time log speaks.
	if adv := a.policyAdvisories(doc); len(adv) > 0 {
		texts := make([]string, len(adv))
		for i, a := range adv {
			texts[i] = a.Text
		}
		out["warnings"] = texts
		out["advisories"] = adv
	}
	writeJSON(w, http.StatusOK, out)
}

// hasAccess reports whether any of the roles holds a binding on app whose
// matchers admit tool, read from the gateway's in-memory binding table. On
// the native straza app it answers what the gateway's tier one and grantedOf
// answer: true for a drafting tool and a role set holding
// straza-draft-config, and false otherwise.
func (a *App) hasAccess(roles []string, app, tool string) bool {
	if app == nativeAppName {
		return nativeListed(tool, roles) && grantedOf(gwTarget{app: app, tool: tool})
	}
	held := make(map[string]bool, len(roles))
	for _, r := range roles {
		held[r] = true
	}
	bindings, _ := a.gateway.bindings.Load().([]gwBinding)
	for _, b := range bindings {
		if b.App == app && held[b.Role] && manager.MatchAnyGlob(b.Matchers, tool) {
			return true
		}
	}
	return false
}

// simulateRequest is the builder's what-if: one event, one subject (explicit
// roles or a user to resolve), optionally a draft PolicySet YAML.
type simulateRequest struct {
	Event   policy.Event `json:"event"`
	Subject struct {
		User        string   `json:"user,omitempty"` // id or username; resolved server-side
		Roles       []string `json:"roles,omitempty"`
		Attestation string   `json:"attestation,omitempty"`
	} `json:"subject"`
	Draft string `json:"draft,omitempty"`
}

func (a *App) handlePolicySimulate(w http.ResponseWriter, r *http.Request) {
	var req simulateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPolicyBody)).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed request")
		return
	}
	if req.Event.Kind == "" {
		apiError(w, http.StatusBadRequest, "event.kind is required (e.g. tool.pre)")
		return
	}

	sub := policy.Subject{
		User: req.Subject.User, Roles: req.Subject.Roles,
		Attestation: req.Subject.Attestation,
	}
	if req.Subject.User != "" && len(req.Subject.Roles) == 0 {
		u, err := a.store.Users().GetByID(r.Context(), req.Subject.User)
		if err != nil {
			if u, err = a.store.Users().GetByUsername(r.Context(), req.Subject.User); err != nil {
				apiError(w, http.StatusNotFound, "no such user (pass roles explicitly to simulate a hypothetical)")
				return
			}
		}
		roles, err := a.resolver.ResolveRoles(r.Context(), u.ID, time.Now())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
			return
		}
		sub.User = u.Username
		for _, role := range roles {
			sub.Roles = append(sub.Roles, role.Name)
		}
		// Typology rides the user lane too (revision 9): a simulation must
		// hit identity-scoped sets exactly like the live checkin subject.
		sub.UserType, sub.AgencyMode, sub.SwarmID = u.UserType, u.AgencyMode, u.SwarmID
	}
	if len(sub.Roles) == 0 && sub.User == "" {
		apiError(w, http.StatusBadRequest, "subject.user or subject.roles is required")
		return
	}
	// The granted fact is never trusted from the body (spec/policyset
	// revision 17): for an mcp.call it is recomputed from the in-memory
	// binding table, so Test a call never says allow for a tool the
	// subject has no access to.
	req.Event.Granted = req.Event.Tool == policy.ToolMCPCall && a.hasAccess(sub.Roles, req.Event.App, req.Event.ToolName)
	// The decision point tags a shell command's interpreter before it
	// evaluates, so simulate does too, or an interpreters rule never fires.
	tagInterpreter(&req.Event)

	out := map[string]any{
		"active":   a.snapshots.Current().Engine.Evaluate(req.Event, sub),
		"subject":  sub,
		"snapshot": a.snapshots.Current().ID,
	}

	if req.Draft != "" {
		draftDoc, err := policy.Parse([]byte(req.Draft))
		if err != nil {
			apiError(w, http.StatusBadRequest, "draft: "+err.Error())
			return
		}
		// The draft replaces its same-named stored set among the ACTIVE ones
		// (or joins them when new): exactly what activation would produce.
		sets, err := a.store.Policies().List(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "list policies failed", err)
			return
		}
		docs := []policy.Document{draftDoc}
		for _, ps := range sets {
			if ps.Status != "active" || ps.Name == draftDoc.Metadata.Name {
				continue
			}
			doc, err := policy.Parse([]byte(ps.YAMLSource))
			if err != nil {
				continue // a stored set that no longer parses can't affect the draft run
			}
			docs = append(docs, doc)
		}
		eng, err := policy.NewEngine(docs, a.cfg.Governance.LocalToolDefault)
		if err != nil {
			apiError(w, http.StatusBadRequest, "draft: "+err.Error())
			return
		}
		out["draft"] = eng.Evaluate(req.Event, sub)
	}
	writeJSON(w, http.StatusOK, out)
}
