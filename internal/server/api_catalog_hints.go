package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/identity"
	"github.com/strazahq/straza/internal/policy"
)

// The hint sentences of the catalog preview (GET /v1/admin/catalog/preview).
// Every row carries exactly one, so the CLI and the console print the same
// plain words for a status token.
const (
	hintVisible     = "has access, no policy gates it"
	hintMatcherMiss = "not in the role's access row on this server"
	hintNoBinding   = "no role of this subject has access to this server"
)

// hintApproveGated names the set, the rule, the pool and the gate for an
// approve-gated tool, in the words of the rule's approve class. A hold blocks
// the call for a bounded wait, so it names that wait. A ticket never holds a
// call: the first call raises the request and is refused, so it names the
// window a decider has and the window the grant then stays good for. Both
// windows are the compiled values, so a rule that omits them says what the
// defaults gave it.
func hintApproveGated(d policy.Decision) string {
	if d.Approve.Class == policy.ClassTicket {
		return fmt.Sprintf("has access; %s, rule %s, the first call raises a ticket for %s to decide within %s, and the approval is good for %s",
			d.SetName, d.RuleID, approvePool(d),
			humanDuration(time.Duration(d.Approve.TicketTTLSeconds)*time.Second),
			humanDuration(time.Duration(d.Approve.GrantTTLSeconds)*time.Second))
	}
	return fmt.Sprintf("has access; %s, rule %s, holds it for %s up to %d s",
		d.SetName, d.RuleID, approvePool(d), d.Approve.TimeoutSeconds)
}

// approvePool says who decides for an approve decision in plain words.
func approvePool(d policy.Decision) string {
	switch {
	case d.Confirm:
		return "the requester"
	case len(d.Approve.Roles) > 0:
		return strings.Join(d.Approve.Roles, ", ")
	default:
		return "the person behind the agent"
	}
}

// hintHiddenPolicy is the rule's own reason when it states one, else the
// set and rule that refuse the tool.
func hintHiddenPolicy(d policy.Decision) string {
	if d.Reason != "" {
		return d.Reason
	}
	return fmt.Sprintf("has access, but %s rule %s denies it", d.SetName, d.RuleID)
}

// hintNotRunning names the server's state and the probe reason for a bound
// app that cannot serve: stopped, absent, degraded, or running with no
// known tools.
func hintNotRunning(status, detail string) string {
	switch {
	case status == "":
		return "has access, but the server is not running"
	case detail == "":
		return fmt.Sprintf("has access, but the server is %s with no known tools", status)
	default:
		return fmt.Sprintf("has access, but the server is %s: %s", status, detail)
	}
}

// roleLaneSubject resolves the preview's role lane the way the user lane
// does: every named role that exists expands through the implication
// closure, so asking for a business role sees what its application roles
// reach. A name that is no role in Straza stays in the subject as a
// hypothetical selector and yields one note. Admin plane: store reads are
// allowed here.
func (a *App) roleLaneSubject(ctx context.Context, names []string) (policy.Subject, []string, error) {
	all, err := a.store.Roles().List(ctx)
	if err != nil {
		return policy.Subject{}, nil, err
	}
	imps, err := a.store.Roles().ListImplications(ctx)
	if err != nil {
		return policy.Subject{}, nil, err
	}
	idByName := make(map[string]string, len(all))
	nameByID := make(map[string]string, len(all))
	for _, r := range all {
		idByName[r.Name] = r.ID
		nameByID[r.ID] = r.Name
	}
	adj := map[string][]string{}
	for _, imp := range imps {
		adj[imp.RoleID] = append(adj[imp.RoleID], imp.ImpliesRoleID)
	}
	seed := map[string]bool{}
	var notes []string
	roles := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		id, ok := idByName[n]
		if !ok {
			notes = append(notes, fmt.Sprintf("no role named %s exists; this preview is for a hypothetical subject.", n))
		} else {
			seed[id] = true
		}
		if !seen[n] {
			seen[n] = true
			roles = append(roles, n)
		}
	}
	for id := range identity.Closure(seed, adj) {
		if n := nameByID[id]; n != "" && !seen[n] {
			seen[n] = true
			roles = append(roles, n)
		}
	}
	return policy.Subject{Roles: roles}, notes, nil
}
