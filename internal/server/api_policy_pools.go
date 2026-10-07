package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
)

// Approve pools and the approver role kind (spec/policyset revision 15):
// drafts.ApprovePoolViolations holds the rule and its reasons. The gate
// runs where the server KNOWS the role table: validate (the Builder and
// git-imported sets get identical words), activation (the moment a set
// starts governing), and boot (an honesty WARN for sets activated before
// this revision, which keep governing so an upgrade never strands a fleet).
// Decide time is untouched: a decider still has to hold one of the record's
// roles freshly. Admin plane: a control-plane read of the role table, never
// a request path.

// roleGates answers the request with a 400 listing every approve-pool
// violation, else every match.roles violation, or a 500 when the role table
// cannot be read, and reports whether the caller may proceed. It reads the
// roles once, and only when doc names one. Validate and activate share it
// so the two surfaces can never disagree about what a legal pool or
// selector is.
func (a *App) roleGates(w http.ResponseWriter, r *http.Request, doc policy.Document) bool {
	var world drafts.World
	if drafts.NamesRoles(doc) {
		roles, err := a.readRoles(r.Context())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role lookup failed", err)
			return false
		}
		world.Roles = roles
	}
	for _, viol := range [][]string{drafts.ApprovePoolViolations(world, doc), drafts.MatchRoleViolations(world, doc)} {
		if len(viol) > 0 {
			apiError(w, http.StatusBadRequest, strings.Join(viol, "; "))
			return false
		}
	}
	return true
}

// legacyApprovePools lists, per ACTIVE set, the violations today's gate
// would raise. Such sets were activated before revision 15 (or stored
// around the API); they keep governing, and this is what the boot WARN
// names so the operator fixes them before their next activation.
func (a *App) legacyApprovePools(ctx context.Context) (map[string][]string, error) {
	sets, err := a.store.Policies().List(ctx)
	if err != nil {
		return nil, err
	}
	var world drafts.World
	out := map[string][]string{}
	for _, ps := range sets {
		if ps.Status != "active" {
			continue
		}
		doc, err := policy.Parse([]byte(ps.YAMLSource))
		if err != nil {
			continue // an unparseable stored set is a different failure, reported by the compiler
		}
		if world.Roles == nil && drafts.NamesRoles(doc) {
			if world.Roles, err = a.readRoles(ctx); err != nil {
				return nil, err
			}
		}
		if viol := drafts.ApprovePoolViolations(world, doc); len(viol) > 0 {
			out[ps.Name] = viol
		}
	}
	return out, nil
}

// warnLegacyApprovePools is the boot half: one WARN per offending active set.
// Never fatal: the sets keep governing exactly as before the upgrade.
func (a *App) warnLegacyApprovePools(ctx context.Context) {
	legacy, err := a.legacyApprovePools(ctx)
	if err != nil {
		a.log.Warn("approve pools: could not audit active sets at boot", "err", err)
		return
	}
	for name, viol := range legacy {
		a.log.Warn("a live policy set names non-approver roles in approve.roles; it keeps governing, but the next publish will be refused until the deciders are approver roles (or straza-admin)",
			"set", name, "violations", strings.Join(viol, "; "))
	}
}

// poolNaming is one rule of an ACTIVE set that names a role in
// approve.roles: the role-delete guard's evidence, and what the roles list
// folds into an approver row's decider_in.
type poolNaming struct {
	Set  string
	Rule string
}

// activeApprovePools lists the (set, rule) pairs of ACTIVE sets naming
// roleName in approve.roles, in store order. Drafts govern nothing and are
// not listed. Unparseable stored sets are skipped here for the same reason
// as in legacyApprovePools. Admin plane only: a control-plane read, never a
// request path.
func (a *App) activeApprovePools(ctx context.Context, roleName string) ([]poolNaming, error) {
	sets, err := a.store.Policies().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []poolNaming
	for _, ps := range sets {
		if ps.Status != "active" {
			continue
		}
		doc, err := policy.Parse([]byte(ps.YAMLSource))
		if err != nil {
			continue
		}
		for _, r := range doc.Spec.Rules {
			if r.Mode != policy.ModeApprove || r.Approve == nil {
				continue
			}
			for _, name := range r.Approve.Roles {
				if name == roleName {
					out = append(out, poolNaming{Set: ps.Name, Rule: r.ID})
					break
				}
			}
		}
	}
	return out, nil
}

// activeApprovePoolsNaming words activeApprovePools one line per (set,
// rule) for the role-delete refusal.
func (a *App) activeApprovePoolsNaming(ctx context.Context, roleName string) ([]string, error) {
	pools, err := a.activeApprovePools(ctx, roleName)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pools {
		out = append(out, fmt.Sprintf("set %q rule %q", p.Set, p.Rule))
	}
	return out, nil
}

// deciderSets names the sets of pools once each, in order.
func deciderSets(pools []poolNaming) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range pools {
		if !seen[p.Set] {
			seen[p.Set] = true
			out = append(out, p.Set)
		}
	}
	return out
}
