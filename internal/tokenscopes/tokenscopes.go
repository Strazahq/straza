// Package tokenscopes parses and enforces the per-area admin API token scope
// grammar (full, or area:read / area:write grants) and maps admin routes to areas.
package tokenscopes

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Per-area admin API token scopes. The grammar has NO legacy forms:
//
//	scope = "full" | grant *("," grant)
//	grant = area ":" ("read" | "write")
//
// `full` is the deliberate root word (a root credential should say so in one
// honest token). The bare word `read` is REJECTED at mint AND at verify,
// because it would admit every admin GET, transcript search included. The
// verb derives from the HTTP method at enforcement time (GET = read,
// everything else = write), so a grant list reads exactly like what it
// admits: `identity:read,changes:read` is a pull connector's whole
// credential. The finer form `area/qualifier:verb` is reserved and not
// implemented.

// tokenScopeAreas is the closed set of grant areas. `tokens` is deliberately
// its own area, included in NO other grant: it mints admin API tokens,
// which makes tokens:write root-equivalent, and that must never hide inside
// an innocuous-looking grant like config:write. `scim` is the whole SCIM
// plane: every /scim/v2 route is that area, so the SCIM server checks it
// with AllowsArea instead of a per-route map. `drafts` stores and checks
// config drafts and changes nothing live, because only a person with
// standing over every item publishes one.
var tokenScopeAreas = map[string]bool{
	"identity": true, "sessions": true, "policy": true, "apps": true,
	"audit": true, "transcripts": true, "approvals": true, "config": true,
	"tokens": true, "changes": true, "scim": true, "drafts": true,
}

const tokenScopeHint = `scope is "full" or comma-separated area:verb grants like "identity:read,changes:read" (areas: identity, sessions, policy, apps, audit, transcripts, approvals, config, tokens, changes, scim, drafts)` // #nosec G101 -- operator help text, not a credential

// Scope is a parsed admin API token scope: `full`, or a set of area:verb grants.
type Scope struct {
	Full   bool
	Grants map[string]bool // "area:verb"
}

// Parse parses the scope grammar above (full, or comma-separated area:verb grants).
func Parse(s string) (Scope, error) {
	switch strings.TrimSpace(s) {
	case "":
		return Scope{}, fmt.Errorf("scope is required: %s", tokenScopeHint)
	case "full":
		return Scope{Full: true}, nil
	case "read":
		return Scope{}, fmt.Errorf("scope %q is retired (it admitted every admin GET, transcripts included): %s", "read", tokenScopeHint)
	}
	ts := Scope{Grants: map[string]bool{}}
	for _, g := range strings.Split(s, ",") {
		g = strings.TrimSpace(g)
		area, verb, ok := strings.Cut(g, ":")
		if !ok || !tokenScopeAreas[area] || (verb != "read" && verb != "write") {
			return Scope{}, fmt.Errorf("invalid grant %q: %s", g, tokenScopeHint)
		}
		ts.Grants[area+":"+verb] = true
	}
	return ts, nil
}

// String renders the canonical (sorted, deduplicated) form: what mint
// stores and lists display, so two tokens with the same authority read
// identically.
func (ts Scope) String() string {
	if ts.Full {
		return "full"
	}
	out := make([]string, 0, len(ts.Grants))
	for g := range ts.Grants {
		out = append(out, g)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// scopeVerb is the enforcement-side verb for a request method.
func scopeVerb(method string) string {
	if method == http.MethodGet {
		return "read"
	}
	return "write"
}

// Allows answers whether the scope covers the matched mux pattern
// (r.Pattern, exactly the `method + " " + pattern` string routeTable
// registered). needed names the missing grant for the 403; mapped=false
// means the route is absent from AdminRouteArea entirely (fail closed,
// loud log at the caller).
func (ts Scope) Allows(method, pattern string) (allowed bool, needed string, mapped bool) {
	if ts.Full {
		return true, "", true
	}
	area, ok := AdminRouteArea[pattern]
	if !ok {
		return false, "", false
	}
	needed = area + ":" + scopeVerb(method)
	return ts.Grants[needed], needed, true
}

// AllowsArea answers whether the scope covers one whole area for the request
// method, for a plane whose every route is that area: the SCIM server checks
// `scim` this way because /scim/v2 has no per-route map. needed names the
// missing grant for the 403.
func (ts Scope) AllowsArea(area, method string) (allowed bool, needed string) {
	if ts.Full {
		return true, ""
	}
	needed = area + ":" + scopeVerb(method)
	return ts.Grants[needed], needed
}

// AdminRouteArea maps EVERY /v1/admin route (keyed exactly as registered)
// to its grant area. TestAdminRouteAreaMapMatchesRouteTable pins two-way
// parity with routeTable(): a new admin endpoint fails tests until mapped
// here, and a stale entry fails when its route dies. A route unmapped at
// runtime is a 403, never a pass.
var AdminRouteArea = map[string]string{
	// policy: the governing rules themselves.
	"GET /v1/admin/policies":                  "policy",
	"PUT /v1/admin/policies":                  "policy",
	"GET /v1/admin/policies/event-support":    "policy",
	"GET /v1/admin/policies/{name}":           "policy",
	"DELETE /v1/admin/policies/{name}":        "policy",
	"POST /v1/admin/policies/{name}/activate": "policy",
	"POST /v1/admin/policies/validate":        "policy",
	"POST /v1/admin/policies/simulate":        "policy",

	// identity: users, groups, roles, assignments, packs, devices, locks.
	"GET /v1/admin/users":                                      "identity",
	"POST /v1/admin/users":                                     "identity",
	"GET /v1/admin/users/{id}":                                 "identity",
	"PATCH /v1/admin/users/{id}":                               "identity",
	"DELETE /v1/admin/users/{id}":                              "identity",
	"GET /v1/admin/users/{id}/devices":                         "identity",
	"GET /v1/admin/revocations":                                "identity",
	"DELETE /v1/admin/users/{id}/devices/{deviceId}":           "identity",
	"POST /v1/admin/users/{id}/lock":                           "identity",
	"POST /v1/admin/users/{id}/unlock":                         "identity",
	"GET /v1/admin/users/{id}/nhi-key":                         "identity",
	"PUT /v1/admin/users/{id}/nhi-key":                         "identity",
	"DELETE /v1/admin/users/{id}/nhi-key":                      "identity",
	"GET /v1/admin/roles":                                      "identity",
	"POST /v1/admin/roles":                                     "identity",
	"PATCH /v1/admin/roles/{id}":                               "identity",
	"DELETE /v1/admin/roles/{id}":                              "identity",
	"GET /v1/admin/roles/{id}/export":                          "identity",
	"GET /v1/admin/roles/{id}/implications":                    "identity",
	"POST /v1/admin/roles/{id}/implications":                   "identity",
	"DELETE /v1/admin/roles/{id}/implications/{implicationId}": "identity",
	"GET /v1/admin/assignments":                                "identity",
	"POST /v1/admin/assignments":                               "identity",
	"DELETE /v1/admin/assignments/{id}":                        "identity",
	"GET /v1/admin/packs":                                      "identity",
	"POST /v1/admin/packs":                                     "identity",
	"DELETE /v1/admin/packs/{id}":                              "identity",
	"POST /v1/admin/packs/{id}/bindings":                       "identity",
	"DELETE /v1/admin/packs/{id}/bindings/{bindingId}":         "identity",

	// sessions: the kill switch, so sessions:write is high-impact. The session
	// signing key lives here too: rotating it is the same plane as ending
	// sessions, and it grants nothing.
	"GET /v1/admin/sessions":              "sessions",
	"POST /v1/admin/sessions/{id}/revoke": "sessions",
	"POST /v1/admin/sessions/revoke":      "sessions",
	"POST /v1/admin/signing-keys/rotate":  "sessions",
	"GET /v1/admin/signing-keys":          "sessions",

	// transcripts: captured conversation content, its own area by design, so
	// no broad read grant admits it.
	"GET /v1/admin/sessions/{id}/transcript": "transcripts",
	"GET /v1/admin/transcripts":              "transcripts",
	"GET /v1/admin/transcripts/search":       "transcripts",

	// audit: decision/admin event history (content-free rows).
	"GET /v1/admin/audit": "audit",

	// changes: its own grant so a pull connector is exactly
	// identity:read,changes:read (+ apps:read,config:read when it also
	// recons apps/tools and testEndpoints against overview).
	"GET /v1/admin/changes": "changes",

	// drafts: config drafts, stored and checked against live state.
	"GET /v1/admin/drafts":               "drafts",
	"POST /v1/admin/drafts":              "drafts",
	"POST /v1/admin/drafts/check":        "drafts",
	"GET /v1/admin/drafts/{id}":          "drafts",
	"PUT /v1/admin/drafts/{id}":          "drafts",
	"POST /v1/admin/drafts/{id}/discard": "drafts",
	"POST /v1/admin/drafts/{id}/revert":  "drafts",
	"POST /v1/admin/drafts/{id}/publish": "drafts",
	"POST /v1/admin/drafts/{id}/rebase":  "drafts",
	"POST /v1/admin/drafts/{id}/contact": "drafts",

	// apps: MCP app lifecycle, bindings, catalog.
	"GET /v1/admin/apps":                "apps",
	"POST /v1/admin/apps":               "apps",
	"DELETE /v1/admin/apps/{id}":        "apps",
	"GET /v1/admin/apps/{id}/logs":      "apps",
	"POST /v1/admin/apps/{id}/health":   "apps",
	"POST /v1/admin/apps/{id}/enable":   "apps",
	"POST /v1/admin/apps/{id}/disable":  "apps",
	"GET /v1/admin/tools":               "apps",
	"GET /v1/admin/catalog/preview":     "apps",
	"POST /v1/admin/apps/{id}/secrets":  "apps",
	"POST /v1/admin/apps/{id}/bindings": "apps",
	"GET /v1/admin/bindings":            "apps",
	"DELETE /v1/admin/bindings/{id}":    "apps",
	// The server wizard's lanes: secrets by scope, the registry import, the
	// provider list its credential step reads, and the access rows no rule
	// names. The apps grant is the one that installs and binds, so it owns them.
	"GET /v1/admin/apps/{id}/secrets":           "apps",
	"DELETE /v1/admin/apps/{id}/secrets":        "apps",
	"DELETE /v1/admin/apps/{id}/secrets/{role}": "apps",
	"POST /v1/admin/apps/import":                "apps",
	"GET /v1/admin/oauth/providers":             "apps",
	"GET /v1/admin/access/grant-only":           "apps",

	// approvals: pending decisions, channels, approver devices.
	// approve/deny ride requirePersonClient (human identities), so a wat_
	// token cannot reach them today; mapped anyway so the parity test stays
	// exhaustive and a future move to requireAdmin inherits enforcement.
	"GET /v1/admin/approvals":                       "approvals",
	"GET /v1/admin/approvals/{id}":                  "approvals",
	"POST /v1/admin/approvals/{id}/approve":         "approvals",
	"POST /v1/admin/approvals/{id}/deny":            "approvals",
	"GET /v1/admin/approvals/channels":              "approvals",
	"POST /v1/admin/approvals/channels/{name}/test": "approvals",
	"POST /v1/admin/approvers/enroll-token":         "approvals",
	"GET /v1/admin/approvers":                       "approvals",
	"DELETE /v1/admin/approvers/{id}":               "approvals",

	// config: posture reads and the attestation registry. Minting a SCIM
	// credential is not here: a SCIM members write assigns the role outright,
	// and admin.roleAreas gives role names admin power, so the mint lives in
	// identity, the area whose objects the credential shapes.
	"GET /v1/admin/config":                     "config",
	"GET /v1/admin/overview":                   "config",
	"GET /v1/admin/sinks":                      "config",
	"POST /v1/admin/sinks/{name}/replay":       "config",
	"GET /v1/admin/attestation-hashes":         "config",
	"POST /v1/admin/attestation-hashes":        "config",
	"DELETE /v1/admin/attestation-hashes/{id}": "config",

	// tokens mints admin credentials: tokens:write is ROOT-EQUIVALENT
	// (a holder mints itself full) and is included in no other grant.
	"POST /v1/admin/api-tokens":        "tokens",
	"GET /v1/admin/api-tokens":         "tokens",
	"DELETE /v1/admin/api-tokens/{id}": "tokens",
	// The client assertion key routes pass root only (requireFullAdmin), so
	// no grant reaches them. They are listed for the parity test, under the
	// root-equivalent area in case a later change swaps the wrapper.
	"POST /v1/admin/signing-keys/client-assertion/rotate":       "tokens",
	"POST /v1/admin/signing-keys/client-assertion/{kid}/retire": "tokens",
}
