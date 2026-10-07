package server

import (
	"context"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
)

// match.roles and the application role kind (spec/policyset revision 16):
// drafts.MatchRoleViolations holds the rule and its reasons. The gate runs
// in roleGates, beside the approve-pool rule, where the server KNOWS the
// role table: validate, activation, and boot, where a set activated before
// this revision gets an honesty WARN and keeps governing so an upgrade never
// strands a fleet. A role later born with a refused kind is caught at the
// set's next activation plus the boot audit. Admin plane: a control-plane
// read, never a request path.

// legacyMatchRoles lists, per ACTIVE set, the violations today's gate would
// raise. Such sets were activated before revision 16 (or stored around the
// API); they keep governing, and this is what the boot WARN names so the
// operator re-aims them before their next activation.
func (a *App) legacyMatchRoles(ctx context.Context) (map[string][]string, error) {
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
		if viol := drafts.MatchRoleViolations(world, doc); len(viol) > 0 {
			out[ps.Name] = viol
		}
	}
	return out, nil
}

// warnLegacyMatchRoles is the boot half: one WARN per offending active set.
// Never fatal: the sets keep governing exactly as before the upgrade.
func (a *App) warnLegacyMatchRoles(ctx context.Context) {
	legacy, err := a.legacyMatchRoles(ctx)
	if err != nil {
		a.log.Warn("match.roles: could not audit active sets at boot", "err", err)
		return
	}
	for name, viol := range legacy {
		a.log.Warn("a live policy set names non-application roles in match.roles; it keeps governing, but the next publish will be refused until the selector names application roles",
			"set", name, "violations", strings.Join(viol, "; "))
	}
}
