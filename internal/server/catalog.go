package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// catalogSeq issues the monotonic sessionCatalog.id. It is folded into overlay
// keys and pagination cursors so a rebuilt tier-1 catalog (new id) invalidates
// both: an overlay can never pair with a stale tier-1 and a cursor minted
// against the old catalog fails validation.
var catalogSeq atomic.Uint64

func nextCatalogID() uint64 { return catalogSeq.Add(1) }

// This file owns the gateway's virtual tool catalog: the two-tier build and
// the justification-schema mechanics. gateway.go keeps the RPC plumbing and
// PEP handlers.
//
// Tier 1 (sessionCatalog) is role-keyed and policy-independent: the
// intersection of upstream inventory ∩ manifest exposure ∩ role bindings, with
// raw shared schemas only, so one build serves every session with that role set.
//
// Tier 2 (catalogOverlay) is per-session and policy-dependent: it probes the
// active engine ONCE per tier-1 tool with the session's FULL subject (user,
// roles, attestation), producing the hidden set (policy-denied tools,
// when policyFilter is on) and the approve-gated schema swaps (injected
// `_straza_justification`). The full subject is what lets user-scoped
// approve and deny rules take effect; a roles-only probe misses them.

// sessionCatalog is one precomputed tier-1 virtual tool catalog.
type sessionCatalog struct {
	tools   []gwTool
	targets map[string]gwTarget // namespaced name → upstream target

	// servers indexes the proxied servers that have a tool in this catalog by
	// name, for the per-server endpoint /mcp/{server}. The built-in straza app
	// is never in it.
	servers map[string]*serverCatalog

	// key is the cache key this catalog was built under (roles|epoch|snapshot).
	// id is a monotonic build id, folded into overlay keys and pagination cursors
	// so neither can outlive the exact tier-1 they were probed against. roles is
	// the sorted role set this catalog was built for, used for targeted drops.
	key   string
	id    uint64
	roles []string
}

// gwTarget is the upstream (app, tool) behind one namespaced catalog entry.
// bindingID and role name the access row that admits the tool for the
// catalog's role set: the first matching binding in role-name order, so the
// choice is the same on every build. Both are empty on the native app.
type gwTarget struct {
	app, tool       string
	bindingID, role string
}

// grantingBinding returns the first binding in the given order whose
// matchers admit the tool.
func grantingBinding(bs []gwBinding, tool string) (gwBinding, bool) {
	for _, b := range bs {
		if manager.MatchAnyGlob(b.Matchers, tool) {
			return b, true
		}
	}
	return gwBinding{}, false
}

// gwTool is the wire representation of a catalog entry. Meta is the
// upstream tool's _meta, served only on the server's own endpoint.
type gwTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema any            `json:"inputSchema"`
	Annotations any            `json:"annotations,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// catalogOverlay is the tier-2 per-session refinement of a tier-1 catalog.
// hidden lists namespaced tools the session's policy denies (populated only
// when policyFilter is enabled); schemas holds justification-injected input
// schemas for approve-gated tools (built regardless of the filter knob;
// justification must keep working when filtering is off).
type catalogOverlay struct {
	key     string // subjectDigest|epoch|snapshotID|tier1.id
	hidden  map[string]bool
	schemas map[string]any
	roles   []string // copy of the session subject's roles, for targeted drops
}

// catalogFor returns the tier-1 catalog for a role set, computing and caching
// it per (roles, epoch, snapshot). All inputs are in-memory snapshots. The build
// is singleflighted so a post-invalidation herd of sessions on the same role key
// computes it once, and a bindGen guard keeps a build that raced SetBindings out
// of the cache.
func (a *App) catalogFor(roles []string) *sessionCatalog {
	sorted := append([]string{}, roles...)
	sort.Strings(sorted)
	cur := a.snapshots.Current()
	// Tier-1 key discipline: roles|epoch|snapshotID. Tier-1 tools are
	// policy-independent (approve gating lives in the tier-2 overlay), so
	// the snapshot id does not change the tier-1 result; it is kept in the
	// key as a conservative belt-and-suspenders (the activation hook in build.go
	// also bumps the epoch via gateway.Invalidate(), which flushes everything).
	key := fmt.Sprintf("%s|%d|%s", strings.Join(sorted, ","), a.gateway.epoch.Load(), cur.ID)

	a.gateway.mu.Lock()
	if v, ok := a.gateway.catalogs.Get(key); ok {
		a.gateway.mu.Unlock()
		return v.(*sessionCatalog)
	}
	a.gateway.mu.Unlock()

	return a.gateway.flight.Do(key, func() any {
		// Re-check under the lock: a concurrent caller may have filled the cache
		// between our miss above and our winning the flight.
		a.gateway.mu.Lock()
		if v, ok := a.gateway.catalogs.Get(key); ok {
			a.gateway.mu.Unlock()
			return v.(*sessionCatalog)
		}
		a.gateway.mu.Unlock()

		// Capture the binding generation BEFORE the build so a SetBindings that
		// lands mid-build is detectable below.
		g0 := a.gateway.bindGen.Load()
		c := a.buildCatalog(sorted)
		c.key = key
		c.id = nextCatalogID()
		c.roles = sorted

		a.gateway.mu.Lock()
		// Only cache when the generation we built against still holds. If a
		// binding change raced us, we still RETURN this catalog: serving one
		// slightly stale tools/list is harmless (a list_changed broadcast always
		// follows a binding change), but we never poison the cache with it.
		if a.gateway.bindGen.Load() == g0 {
			a.gateway.catalogs.Put(key, c)
		}
		a.gateway.mu.Unlock()
		return c
	}).(*sessionCatalog)
}

// buildCatalog intersects, per app: cached upstream inventory ∩ manifest
// exposure (already applied in the manager view) ∩ the union of the session
// roles' tool bindings. No binding row ⇒ the app is invisible (default-deny).
// Tier-1 tools carry raw shared schemas; policy probing and
// justification injection are the tier-2 overlay's job.
func (a *App) buildCatalog(roles []string) *sessionCatalog {
	held := make(map[string]bool, len(roles))
	for _, r := range roles {
		held[r] = true
	}
	bindings, _ := a.gateway.bindings.Load().([]gwBinding)

	c := &sessionCatalog{targets: map[string]gwTarget{}, servers: map[string]*serverCatalog{}}
	for _, view := range a.manager.Views() {
		if view.Status != manager.StatusRunning && view.Status != manager.StatusDegraded {
			continue
		}
		var heldBindings []gwBinding
		for _, b := range bindings {
			if b.App == view.Name && held[b.Role] {
				heldBindings = append(heldBindings, b)
			}
		}
		if len(heldBindings) == 0 {
			continue
		}
		sort.SliceStable(heldBindings, func(i, j int) bool { return heldBindings[i].Role < heldBindings[j].Role })
		sc := &serverCatalog{viewsOn: view.ViewsOn, views: view.Views}
		for _, t := range view.Tools {
			grant, ok := grantingBinding(heldBindings, t.Name)
			if !ok {
				continue
			}
			namespaced := view.Name + "__" + t.Name
			c.targets[namespaced] = gwTarget{app: view.Name, tool: t.Name, bindingID: grant.ID, role: grant.Role}
			entry := gwTool{
				Name:        namespaced,
				Title:       t.Title,
				Description: t.Description,
				InputSchema: t.InputSchema,
				Annotations: t.Annotations,
				Meta:        t.Meta,
			}
			c.tools = append(c.tools, entry)
			sc.tools = append(sc.tools, entry)
		}
		if len(sc.tools) > 0 && view.Name != nativeAppName {
			sort.Slice(sc.tools, func(i, j int) bool { return sc.tools[i].Name < sc.tools[j].Name })
			c.servers[view.Name] = sc
		}
	}
	// Built-in `straza` app: originate the approval tools as candidates for
	// every role, which the tier-2 overlay hides unless the session's policy
	// authorizes them, and the drafting tools for a holder of
	// straza-draft-config only.
	a.appendNativeCatalog(c, roles)
	sort.Slice(c.tools, func(i, j int) bool { return c.tools[i].Name < c.tools[j].Name })
	a.warnCatalogSize(roles, len(c.tools))
	return c
}

// warnCatalogSize emits a single warning (and bumps the oversize counter) when
// a freshly built tier-1 catalog exceeds the configured WarnSize, a signal
// that a role's bindings are too broad. Builds are cached per role key, so the
// warning is naturally rate-limited to once per (roles, epoch, snapshot).
// WarnSize < 0 disables the check.
func (a *App) warnCatalogSize(roles []string, size int) {
	warn := a.cfg.Apps.Catalog.WarnSize
	if warn < 0 || size <= warn {
		return
	}
	a.log.Warn("gateway tool catalog is large. Consider tightening this role's access rows",
		"roleKey", strings.Join(roles, ","), "size", size, "warnSize", warn)
	if a.metrics != nil {
		a.metrics.Oversize()
	}
}

// overlayFor returns the tier-2 overlay for a live session, rebuilding it when
// the subject digest, epoch, active snapshot, or tier-1 build id changed. Folding
// tier1.id into the key means an overlay can never outlive the tier-1 catalog it
// probed: a rebuilt tier-1 with new tools always pairs with a freshly probed
// overlay, never a stale one (unprobed tools would otherwise slip past hiding and
// justification injection). Cached per session in a bounded LRU under the gateway
// mutex. All inputs are in-memory snapshots (request-path safe).
func (a *App) overlayFor(sessionID string, sub policy.Subject, tier1 *sessionCatalog) *catalogOverlay {
	cur := a.snapshots.Current()
	key := fmt.Sprintf("%s|%d|%s|%d", subjectDigest(sub), a.gateway.epoch.Load(), cur.ID, tier1.id)

	a.gateway.mu.Lock()
	if v, ok := a.gateway.overlays.Get(sessionID); ok {
		if ov := v.(*catalogOverlay); ov.key == key {
			a.gateway.mu.Unlock()
			return ov
		}
	}
	a.gateway.mu.Unlock()

	ov := a.buildOverlay(key, sub, tier1, cur.Engine)
	ov.roles = append([]string{}, sub.Roles...)

	a.gateway.mu.Lock()
	a.gateway.overlays.Put(sessionID, ov)
	a.gateway.mu.Unlock()
	return ov
}

// buildOverlay probes the engine once per tier-1 tool with the FULL subject.
// A non-allow decision hides the tool (only when policyFilter is enabled; the
// justification lane must keep working when filtering is off); an approve-gated
// allow deep-copies the raw schema and injects `_straza_justification`. A nil
// engine (never in production) refines nothing. The probe carries the granted
// fact grantedOf answers (spec/policyset revisions 17 and 20): a bound tool
// and a drafting tool with no rule stay visible. The approval tools never
// carry it, so policy stays their only visibility gate.
func (a *App) buildOverlay(key string, sub policy.Subject, tier1 *sessionCatalog, eng *policy.Engine) *catalogOverlay {
	ov := &catalogOverlay{key: key, hidden: map[string]bool{}, schemas: map[string]any{}}
	if eng == nil {
		return ov
	}
	filter := a.cfg.Apps.Catalog.PolicyFilter
	for _, t := range tier1.tools {
		target, ok := tier1.targets[t.Name]
		if !ok {
			continue
		}
		d := eng.Evaluate(policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
			App: target.app, ToolName: target.tool,
			Granted: grantedOf(target),
		}, sub)
		switch {
		case d.Effect != policy.EffectAllow:
			// A denied granted tool, a proxied or a drafting one, is hidden
			// only under policyFilter. A denied approval tool is ALWAYS hidden:
			// it has no grant, so policy authorization is its only visibility
			// gate (default-deny: an unauthorized session sees none).
			if filter || !grantedOf(target) {
				ov.hidden[t.Name] = true
			}
		case d.Approve != nil:
			ov.schemas[t.Name] = injectJustification(t.InputSchema)
		}
	}
	return ov
}

// overlayTools assembles the combined endpoint's tools/list response from the
// tier-1 catalog and a session overlay: tier-1 minus hidden, with approve-gated
// schema swaps and without _meta, which only a server's own endpoint serves.
// It builds a fresh slice and never mutates the cached tier-1 tools (the range
// value is a struct copy, so overwriting InputSchema and Meta is local).
func overlayTools(tier1 *sessionCatalog, ov *catalogOverlay) []gwTool {
	out := make([]gwTool, 0, len(tier1.tools))
	for _, t := range tier1.tools {
		if ov.hidden[t.Name] {
			continue
		}
		if s, ok := ov.schemas[t.Name]; ok {
			t.InputSchema = s
		}
		t.Meta = nil
		out = append(out, t)
	}
	return out
}

// invalidCursorMessage is the fail-closed reason a bad or stale tools/list
// cursor answers with (paired with JSON-RPC error -32602). It tells the client
// exactly how to recover: start the listing over.
const invalidCursorMessage = "invalid or stale cursor: re-issue tools/list from the start"

// catalogCursorDigest binds a cursor to the exact (tier-1, overlay) pair it was
// issued against and to the endpoint that listed it: the hex of the first 16
// bytes of sha256(tier1.key \x00 overlay.key), with \x00 server appended on a
// server's own endpoint. Any binding, policy, or role change rebuilds one or
// both keys (the overlay key already folds in tier1.id), so a cursor minted
// against the old catalog fails the digest check and the client re-lists from
// the start rather than paging a shifted list. The server suffix keeps a
// cursor of one endpoint from paging another endpoint's list.
func catalogCursorDigest(tier1 *sessionCatalog, ov *catalogOverlay, server string) string {
	bound := tier1.key + "\x00" + ov.key
	if server != "" {
		bound += "\x00" + server
	}
	sum := sha256.Sum256([]byte(bound))
	return hex.EncodeToString(sum[:16])
}

// catalogCursor encodes an opaque, self-validating pagination cursor:
// base64url(raw) where raw is "1\x00<digest>\x00<offset>". The data plane stays
// stateless: the cursor carries everything needed to validate and resume,
// with no server-side page state. server is empty on the combined endpoint.
func catalogCursor(tier1 *sessionCatalog, ov *catalogOverlay, server string, offset int) string {
	raw := fmt.Sprintf("1\x00%s\x00%d", catalogCursorDigest(tier1, ov, server), offset)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// parseCatalogCursor decodes a cursor and validates it against the current
// catalog, returning the resume offset. It fails closed (ok=false, the caller
// answers -32602 with invalidCursorMessage) on any decode error, a version or
// digest mismatch (the catalog changed under the client), or an offset outside
// [0, total]. offset == total is legal; it addresses the final empty page.
// server names the endpoint the cursor is replayed on, as in catalogCursor.
func parseCatalogCursor(cursor string, tier1 *sessionCatalog, ov *catalogOverlay, server string, total int) (int, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 || parts[0] != "1" || parts[1] != catalogCursorDigest(tier1, ov, server) {
		return 0, false
	}
	offset, err := strconv.Atoi(parts[2])
	if err != nil || offset < 0 || offset > total {
		return 0, false
	}
	return offset, true
}

// subjectDigest hashes the policy-relevant subject fields so an overlay is
// rebuilt whenever any of them change (User, sorted Roles, Attestation,
// DeviceCert, Harness). Order-insensitive on the slice.
func subjectDigest(sub policy.Subject) string {
	roles := append([]string{}, sub.Roles...)
	sort.Strings(roles)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\x00%s",
		sub.User, strings.Join(roles, ","),
		sub.Attestation, sub.DeviceCert, sub.Harness)
	return hex.EncodeToString(h.Sum(nil))
}

// justificationField is the synthetic string property the gateway injects into
// an approval-gated tool's input schema; justificationDescription is its
// prompt to the model.
const (
	justificationField       = "_straza_justification"
	justificationDescription = "Required by Straza: briefly state why this call is needed; an approver will read it."
)

// injectJustification returns a deep copy of a tool input schema with a
// required string property `_straza_justification` added. It round-trips
// through JSON into a fresh map, so the manager's shared schema is never
// mutated and the copy is independent of the source's concrete type
// (*jsonschema.Schema or map[string]any). A nil or non-object schema yields a
// minimal object schema carrying just the injected field.
func injectJustification(schema any) any {
	m := map[string]any{}
	if schema != nil {
		if raw, err := json.Marshal(schema); err == nil {
			_ = json.Unmarshal(raw, &m) // a fresh tree; no aliasing with the source
		}
	}
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	props, _ := m["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props[justificationField] = map[string]any{
		"type":        "string",
		"description": justificationDescription,
	}
	m["properties"] = props
	req := stringSlice(m["required"])
	if !containsStr(req, justificationField) {
		req = append(req, justificationField)
	}
	// Keep the tree as unmarshaled-JSON types ([]any, not []string) so
	// readers of the copied schema see one consistent shape.
	reqAny := make([]any, len(req))
	for i, s := range req {
		reqAny[i] = s
	}
	m["required"] = reqAny
	return m
}

// stripJustification removes the injected `_straza_justification` property from
// tools/call arguments, returning the cleaned argument JSON and the extracted
// justification (empty when absent; still proceed). Cleaned args are what the
// gateway audits and forwards upstream; the justification rides the approval
// record only. Non-object or malformed arguments pass through untouched.
func stripJustification(args json.RawMessage) (json.RawMessage, string) {
	if len(args) == 0 {
		return args, ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return args, "" // not a JSON object; leave verbatim
	}
	raw, ok := m[justificationField]
	if !ok {
		return args, ""
	}
	var justification string
	_ = json.Unmarshal(raw, &justification) // best-effort; a non-string value ⇒ ""
	delete(m, justificationField)
	cleaned, err := json.Marshal(m)
	if err != nil {
		return args, justification
	}
	return cleaned, justification
}

func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// sameStringSet reports whether a and b hold the same elements, order- and
// duplicate-insensitive. Used by the checkin role-change nudge to decide
// whether a session's resolved roles actually changed.
func sameStringSet(a, b []string) bool {
	seen := make(map[string]struct{}, len(a))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	bset := make(map[string]struct{}, len(b))
	for _, s := range b {
		bset[s] = struct{}{}
		if _, ok := seen[s]; !ok {
			return false
		}
	}
	return len(seen) == len(bset)
}

// changedRoles returns the set of role names whose bindings differ between the
// old and new tables. A role is "changed" when its fingerprint differs,
// including roles added or removed outright. Catalogs and overlays built for a
// role in this set are stale; every other role's cache entry is still valid. It
// is what lets SetBindings evict surgically instead of flushing the fleet.
func changedRoles(oldBs, newBs []gwBinding) map[string]bool {
	oldFP := roleFingerprints(oldBs)
	newFP := roleFingerprints(newBs)
	changed := map[string]bool{}
	for role, fp := range oldFP {
		if newFP[role] != fp {
			changed[role] = true
		}
	}
	for role, fp := range newFP {
		if oldFP[role] != fp {
			changed[role] = true
		}
	}
	return changed
}

// roleFingerprints reduces a binding table to one canonical string per role: the
// role's (app, sorted matchers) pairs in sorted order. Two tables that produce
// the same fingerprint for a role expose the same tools to it, so its cached
// catalog need not be dropped. Control-character separators keep app names and
// glob matchers from colliding at the joins.
func roleFingerprints(bs []gwBinding) map[string]string {
	byRole := map[string][]string{}
	for _, b := range bs {
		m := append([]string{}, b.Matchers...)
		sort.Strings(m)
		byRole[b.Role] = append(byRole[b.Role], b.App+"\x00"+strings.Join(m, "\x01"))
	}
	out := make(map[string]string, len(byRole))
	for role, entries := range byRole {
		sort.Strings(entries)
		out[role] = strings.Join(entries, "\x02")
	}
	return out
}

// rolesIntersect reports whether any of roles is in the changed set, the
// predicate targeted invalidation drops cache entries by.
func rolesIntersect(roles []string, changed map[string]bool) bool {
	for _, r := range roles {
		if changed[r] {
			return true
		}
	}
	return false
}
