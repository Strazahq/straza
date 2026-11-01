package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
)

// objectIDMeta is the _meta key that carries the object list id to the
// server on a call a partial role granted.
const objectIDMeta = "ai.straza/object-id"

// auditObjectValueMax is how many bytes of an object's value the record
// keeps. A longer value is cut on a rune boundary, flagged, and hashed.
const auditObjectValueMax = 512

// heldBinding is one held binding of a catalog with its covers line
// compiled for the object gate.
type heldBinding struct {
	matchers []string
	holding  objects.Holding
}

// compileHoldings compiles the held bindings of one app, already sorted by
// role name, for held.
func compileHoldings(bs []gwBinding) []heldBinding {
	out := make([]heldBinding, len(bs))
	for i, b := range bs {
		out[i] = heldBinding{matchers: b.Matchers,
			holding: objects.Holding{Role: b.Role, BindingID: b.ID, Scope: objects.CompileScope(b.Scope)}}
	}
	return out
}

// held answers the held bindings of app whose matchers admit tool, sorted
// by role name, as the object gate reads them. In-memory only.
func (c *sessionCatalog) held(app, tool string) []objects.Holding {
	var out []objects.Holding
	for _, b := range c.holdings[app] {
		if manager.MatchAnyGlob(b.matchers, tool) {
			out = append(out, b.holding)
		}
	}
	return out
}

// objectGate decides a call to target on a proxied server that pins an
// object type, from the pinned declaration, the catalog's held bindings
// and this replica's object list, all in memory. pinned is false when the
// server pins no type, and the call then goes on as before. It changes no
// state, so simulate and the preview may ask it too: a caller that acts on
// a real call passes a verdict with Missed set to objectMissed.
func (a *App) objectGate(tier1 *sessionCatalog, target gwTarget, args json.RawMessage) (v objects.Verdict, pinned bool) {
	if target.app == nativeAppName {
		return objects.Verdict{}, false
	}
	oc, ok := a.manager.ObjectsFor(target.app, target.tool)
	if !ok {
		return objects.Verdict{}, false
	}
	list := a.objects.Get(oc.AppID)
	if list != nil && list.Type != oc.Type {
		// The list was made for another pinned type, so its ids say
		// nothing about this one until the loop lists the server again.
		list = nil
	}
	return objects.Decide(objects.Call{Server: oc.Server, Type: oc.Type, Plural: oc.Plural, Tool: target.tool,
		Reach: oc.Reach, Drifted: oc.Drifted, Enabled: a.cfg.Apps.Objects.Enabled, Args: args,
		Held: tier1.held(target.app, target.tool), List: list}), true
}

// objectMissed asks for a listing of target's server, as a real call the
// gate refused for an object the list does not hold does. The list takes at
// most one a minute per server.
func (a *App) objectMissed(target gwTarget) {
	if oc, ok := a.manager.ObjectsFor(target.app, target.tool); ok {
		a.objects.Missed(oc.AppID)
	}
}

// approvalObject is the object an approval key binds for a call the gate
// let through: the type and the value the call names, on a tool whose reach
// is argument, whichever role granted it and whether the list holds the
// value. The grant can flip between a role on every object and a role on
// some as a tool drifts, the checks turn off or a replica's list lags, and
// a key built from the grant would then miss the ticket a person approved.
// A value key never lets a partial role act on a recreated object, because
// the gate checks coverage by id again at every use.
func approvalObject(v objects.Verdict) approval.Object {
	if v.Object == nil || v.Object.Reach != manager.ReachArgument {
		return approval.Object{}
	}
	return approval.Object{Type: v.Object.Type, Value: v.Object.Value}
}

// objectMeta is the _meta Straza sets on the upstream call: the object
// list id when a partial role granted the call, nil otherwise.
func objectMeta(v objects.Verdict) map[string]any {
	if !v.SendID || v.Object == nil {
		return nil
	}
	return map[string]any{objectIDMeta: v.Object.ID}
}

// objectRecord is the object member of a straza.audit.mcp record: type and
// reach, and the value and id when the call yielded them, the value cut at
// auditObjectValueMax with valueTruncated and the SHA-256 of the whole
// value when longer. Attribute values never ride it.
func objectRecord(h objects.Hit) map[string]any {
	out := map[string]any{"type": h.Type, "reach": h.Reach}
	if h.Value != "" {
		v, cut := objects.Cut(h.Value, auditObjectValueMax)
		out["value"] = v
		if cut {
			sum := sha256.Sum256([]byte(h.Value))
			out["valueTruncated"] = true
			out["valueSha256"] = hex.EncodeToString(sum[:])
		}
	}
	if h.ID != "" {
		out["id"] = h.ID
	}
	return out
}
