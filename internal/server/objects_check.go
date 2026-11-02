package server

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
)

// objectCheck is the object gate's answer to a call that has not been made:
// the server's pinned declaration for the tool and the gate's verdict.
type objectCheck struct {
	call    manager.ObjectsCall
	verdict objects.Verdict
}

// checkObject runs the gateway's object gate for a call by roles to tool on
// app with the raw arguments args, on the same in-memory inputs. It asks for
// no listing: a caller that stands for a real request does that itself. ok
// is false when the gate does not apply: the server pins no type, the
// tool's reach is none and its mark has not drifted, or no held binding
// binds the tool, which the gateway refuses as an unknown tool before the
// gate runs.
func (a *App) checkObject(roles []string, app, tool string, args json.RawMessage) (objectCheck, bool) {
	oc, ok := a.manager.ObjectsFor(app, tool)
	if !ok || (oc.Reach.Reach == manager.ReachNone && !oc.Drifted) {
		return objectCheck{}, false
	}
	held := a.heldCatalog(roles, app)
	if len(held.held(app, tool)) == 0 {
		return objectCheck{}, false
	}
	v, _ := a.objectGate(held, gwTarget{app: app, tool: tool}, args)
	return objectCheck{call: oc, verdict: v}, true
}

// heldCatalog is a catalog that carries only the held bindings of roles on
// app, sorted by role name with their covers lines compiled, as
// buildCatalog keeps them. It reads the gateway's binding table whether or
// not the server runs, so a stopped server still answers the object check,
// and it leaves the session catalog cache alone.
func (a *App) heldCatalog(roles []string, app string) *sessionCatalog {
	held := make(map[string]bool, len(roles))
	for _, r := range roles {
		held[r] = true
	}
	bindings, _ := a.gateway.bindings.Load().([]gwBinding)
	var mine []gwBinding
	for _, b := range bindings {
		if b.App == app && held[b.Role] {
			mine = append(mine, b)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].Role < mine[j].Role })
	return &sessionCatalog{holdings: map[string][]heldBinding{app: compileHoldings(mine)}}
}

// refusal is the sentence the gateway answers a refused call with, with the
// Straza prefix the session banner reads, or "" when the gate admits it.
func (c objectCheck) refusal() string {
	r := c.verdict.Refusal
	if r == "" || strings.HasPrefix(r, "Straza:") {
		return r
	}
	return "Straza: " + r
}

// simObject is the object member of a simulate answer (openapi 0.142.0):
// the server's type, the tool's reach, the object the call names with its
// list id, and either the held role that covers the call or the sentence
// the gateway refuses it with.
type simObject struct {
	Type    string `json:"type"`
	Reach   string `json:"reach"`
	Value   string `json:"value,omitempty"`
	ID      string `json:"id,omitempty"`
	Covered bool   `json:"covered"`
	Role    string `json:"role,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

// sim answers the simulate member of the check.
func (c objectCheck) sim() simObject {
	o := simObject{Type: c.call.Type, Reach: c.call.Reach.Reach, Refusal: c.refusal()}
	if hit := c.verdict.Object; hit != nil {
		o.Value, o.ID = hit.Value, hit.ID
	}
	if o.Refusal == "" && c.verdict.Grant != nil {
		o.Covered, o.Role = true, c.verdict.Grant.Role
	}
	return o
}
