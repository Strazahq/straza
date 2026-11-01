package server

import (
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/policy"
)

// harnessTools is the table of harness tools the built-in mappings classify,
// which the by-name gate of spec/policyset revision 26 reads. The mappings
// are embedded, so the table never changes within a process.
var harnessTools = sync.OnceValues(agentguard.ClassifiedTools)

// byNameGate answers the request with a 400 listing every by-name violation
// of doc against the set of the same name the active snapshot carries, and
// reports whether the caller may proceed. It is the draft check's gate
// (drafts.ByNameViolations) at validate, so the two surfaces never disagree.
// Admin plane: a control-plane read, never a request path.
func (a *App) byNameGate(w http.ResponseWriter, r *http.Request, doc policy.Document) bool {
	if !slices.ContainsFunc(doc.Spec.Rules, policy.Rule.NamesHarnessTools) {
		return true
	}
	var world drafts.World
	if err := a.readPolicies(r.Context(), &world); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the active policy sets could not be read", err)
		return false
	}
	var live policy.Document
	if p, ok := world.Policies[doc.Metadata.Name]; ok {
		live, _ = policy.Parse([]byte(p.Text))
	}
	if viol := drafts.ByNameViolations(doc, live, harnessTools); len(viol) > 0 {
		apiError(w, http.StatusBadRequest, strings.Join(viol, "; "))
		return false
	}
	return true
}
