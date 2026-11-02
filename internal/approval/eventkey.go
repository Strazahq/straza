package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/strazahq/straza/internal/policy"
)

// Key format tags. The version and effective scope ride INSIDE the stored
// key string:
// every grant records its version by construction, lookups stay exact-match,
// and binding_scope for a stored row derives from the key with no migration.
const (
	keyPrefixV2Call = "v2:call:sha256:"
	keyPrefixV2Tool = "v2:tool:sha256:"
)

// EventKeyV2 computes the v2 fingerprint: a domain-separated digest whose
// scope is either the concrete call (canonicalized mcp args / shell command)
// or the bare tool identity. binding is the rule's normalized approve.binding
// (call|tool), consulted for mcp.call events only; every other lane binds the
// concrete call.
//
// Scope for mcp.call: binding tool gives tool; binding call with OBSERVED args
// ("{}" counts, it is the observed no-argument call) gives call; binding call
// with args never observed (nil: old client, argless dialect) degrades to
// tool, because the key must not pretend to bind what the server never saw.
// The degraded key equals the explicit binding:tool key for the same identity,
// and a tool-scoped grant authorizes any-args by explicit human decision, so
// the alias stays within what was approved. An error (ambiguous or malformed
// args) means NO key exists, and callers deny fail-closed.
func EventKeyV2(ev policy.Event, binding string) (string, error) {
	return EventKeyObject(ev, binding, Object{})
}

// Object is the object a call names, for the approval key: the server's
// pinned type and the value the call's argument carries.
type Object struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

// EventKeyObject is EventKeyV2 with the object a call names, in both scopes. A
// zero Object gives exactly EventKeyV2's key, so stored grants keep matching.
func EventKeyObject(ev policy.Event, binding string, obj Object) (string, error) {
	scope := policy.ApproveBindingCall
	if ev.Tool == policy.ToolMCPCall && (binding == policy.ApproveBindingTool || (ev.Args == nil && ev.Command == "" && len(ev.Argv) == 0)) {
		scope = policy.ApproveBindingTool
	}

	pre := struct {
		V        int             `json:"v"`
		Binding  string          `json:"binding"`
		Tool     string          `json:"tool"`
		App      string          `json:"app"`
		ToolName string          `json:"toolName"`
		Command  string          `json:"command"`
		Argv     []string        `json:"argv"`
		Args     json.RawMessage `json:"args"`
		Object   *Object         `json:"object,omitempty"`
	}{V: 2, Binding: scope, Tool: ev.Tool, App: ev.App, ToolName: ev.ToolName}
	if obj != (Object{}) {
		pre.Object = &obj
	}

	prefix := keyPrefixV2Tool
	if scope == policy.ApproveBindingCall {
		prefix = keyPrefixV2Call
		pre.Command, pre.Argv = ev.Command, ev.Argv
		canon, err := CanonicalArgs(ev.Args)
		if err != nil {
			return "", err
		}
		pre.Args = canon
	} else {
		pre.Args = json.RawMessage("{}")
	}

	b, err := json.Marshal(pre)
	if err != nil {
		return "", err // cannot happen (fixed fields, valid RawMessage); defensive
	}
	sum := sha256.Sum256(b)
	return prefix + hex.EncodeToString(sum[:]), nil
}
