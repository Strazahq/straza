package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
)

// The obligations list (spec/policyset revision 18):
// drafts.ObligationViolations holds the rule and its reasons. The field
// stays on the wire and stored documents keep parsing, the engine reports an
// empty list, and this gate refuses a document that carries the list at
// validate and at activation. Sets active from before the revision keep
// governing and are named once at boot. Admin plane: a control-plane read,
// never a request path.

// obligationsGate answers the request with a 400 listing every violation
// and reports whether the caller may proceed. Validate and activate share
// it, beside roleGates, so the two surfaces can never disagree.
func (a *App) obligationsGate(w http.ResponseWriter, doc policy.Document) bool {
	if viol := drafts.ObligationViolations(doc); len(viol) > 0 {
		apiError(w, http.StatusBadRequest, strings.Join(viol, "; "))
		return false
	}
	return true
}

// legacyObligations lists, per ACTIVE set, the violations today's gate would
// raise. Such sets were activated before revision 18 (or stored around the
// API); they keep governing, and this is what the boot WARN names so the
// operator removes the lists before their next activation.
func (a *App) legacyObligations(ctx context.Context) (map[string][]string, error) {
	sets, err := a.store.Policies().List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, ps := range sets {
		if ps.Status != "active" {
			continue
		}
		doc, err := policy.Parse([]byte(ps.YAMLSource))
		if err != nil {
			continue // an unparseable stored set is a different failure, reported by the compiler
		}
		if viol := drafts.ObligationViolations(doc); len(viol) > 0 {
			out[ps.Name] = viol
		}
	}
	return out, nil
}

// warnLegacyObligations is the boot half: one WARN per offending active set.
// Never fatal: the sets keep governing exactly as before the upgrade.
func (a *App) warnLegacyObligations(ctx context.Context) {
	legacy, err := a.legacyObligations(ctx)
	if err != nil {
		a.log.Warn("obligations: could not audit active sets at boot", "err", err)
		return
	}
	for name, viol := range legacy {
		a.log.Warn("a live policy set carries obligations that never ran; it keeps governing, but the next publish will be refused until every obligations list is removed",
			"set", name, "violations", strings.Join(viol, "; "))
	}
}
