package drafts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/bits"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/strazahq/straza/internal/policy"
)

// appliesTo reports whether a set whose match is m applies to sub, as the
// policy engine matches: every selector list that is present holds the
// subject's value, match.roles any of the subject's roles, so a field the
// subject leaves unset fails every identity list.
func appliesTo(m policy.Match, sub policy.Subject) bool {
	if len(m.Roles) > 0 && !slices.ContainsFunc(sub.Roles, func(r string) bool { return slices.Contains(m.Roles, r) }) {
		return false
	}
	if len(m.Users) > 0 && !slices.Contains(m.Users, sub.User) {
		return false
	}
	if id := m.Identity; id != nil {
		for _, sel := range []struct {
			list  []string
			value string
		}{{id.UserType, sub.UserType}, {id.AgencyMode, sub.AgencyMode}, {id.SwarmID, sub.SwarmID}} {
			if len(sel.list) > 0 && !slices.Contains(sel.list, sel.value) {
				return false
			}
		}
	}
	return true
}

// globs caches the compiled form of each tool glob that holds a *.
var globs sync.Map

// matchGlob reports whether name matches the tool glob pattern, where *
// matches any run of characters and everything else is literal: the form
// of a manifest's exposure list and of an access row's tools, read the way
// the manager and the gateway read them.
func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	if re, ok := globs.Load(pattern); ok {
		return re.(*regexp.Regexp).MatchString(name)
	}
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
	globs.Store(pattern, re)
	return re.MatchString(name)
}

// matchAnyGlob reports whether name matches any of patterns.
func matchAnyGlob(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return matchGlob(p, name) })
}

// admitsMore reports whether the globs next admit a name the globs old do
// not, read from the globs alone, for a server whose tools nobody has
// listed: some glob of next that no glob of old matches as a name. A * of
// old stands for any text, a * of next included, so a glob that old matches
// admits nothing old does not.
func admitsMore(old, next []string) bool {
	return slices.ContainsFunc(next, func(g string) bool { return !matchAnyGlob(old, g) })
}

// closures answers the implication closure of each role of one World: the
// role with every role it reaches, sorted. On first use it walks the edges
// once, by Tarjan's algorithm, and keeps the closure of each strongly
// connected component as a bitset over the sorted names, which the
// components it reaches complete first. A graph whose every role implies
// every later role, which a draft may hold, so costs one bitset union for
// each edge, where a walk for each role would cost the cube of the roles.
type closures struct {
	implies    map[string][]string
	names      []string
	bit        map[string]int
	index, low map[string]int
	onStack    map[string]bool
	stack      []string
	comp       map[string]int
	bits       [][]uint64
	one, many  map[string][]string
}

// newClosures answers the closures over implies, walked on first use.
func newClosures(implies map[string][]string) *closures {
	return &closures{implies: implies, one: map[string][]string{}, many: map[string][]string{}}
}

// of answers the closure of role. The answer is shared, so the caller must
// not change it.
func (c *closures) of(role string) []string {
	if out, ok := c.one[role]; ok {
		return out
	}
	if c.bit == nil {
		c.walk()
	}
	out := []string{role}
	if comp, ok := c.comp[role]; ok {
		out = out[:0]
		for i, word := range c.bits[comp] {
			for ; word != 0; word &= word - 1 {
				out = append(out, c.names[i*64+bits.TrailingZeros64(word)])
			}
		}
	}
	c.one[role] = out
	return out
}

// walk numbers every role on an edge by its sorted name and visits each
// once.
func (c *closures) walk() {
	c.bit, c.index, c.low, c.onStack, c.comp = map[string]int{}, map[string]int{}, map[string]int{}, map[string]bool{}, map[string]int{}
	for from, to := range c.implies {
		c.bit[from] = 0
		for _, x := range to {
			c.bit[x] = 0
		}
	}
	c.names = sortedKeys(c.bit)
	for i, name := range c.names {
		c.bit[name] = i
	}
	for _, name := range c.names {
		if _, seen := c.index[name]; !seen {
			c.visit(name)
		}
	}
}

// visit is Tarjan's step from role v. A component is complete only after
// every component it reaches, so their closures are known when its own is
// gathered.
func (c *closures) visit(v string) {
	c.index[v], c.low[v] = len(c.index), len(c.index)
	c.stack, c.onStack[v] = append(c.stack, v), true
	for _, u := range c.implies[v] {
		if _, seen := c.index[u]; !seen {
			c.visit(u)
			c.low[v] = min(c.low[v], c.low[u])
		} else if c.onStack[u] {
			c.low[v] = min(c.low[v], c.index[u])
		}
	}
	if c.low[v] != c.index[v] {
		return
	}
	id, set := len(c.bits), make([]uint64, (len(c.names)+63)/64)
	var members []string
	for {
		u := c.stack[len(c.stack)-1]
		c.stack, c.onStack[u], c.comp[u] = c.stack[:len(c.stack)-1], false, id
		members = append(members, u)
		set[c.bit[u]/64] |= 1 << (c.bit[u] % 64)
		if u == v {
			break
		}
	}
	for _, u := range members {
		for _, x := range c.implies[u] {
			if other := c.comp[x]; other != id {
				for i, word := range c.bits[other] {
					set[i] |= word
				}
			}
		}
	}
	c.bits = append(c.bits, set)
}

// union answers the closure of every role of roles together, sorted. roles
// must be sorted, and the answer is shared, so the caller must not change
// it.
func (c *closures) union(roles []string) []string {
	key := strings.Join(roles, "\x00")
	if out, ok := c.many[key]; ok {
		return out
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range roles {
		for _, x := range c.of(r) {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	sort.Strings(out)
	c.many[key] = out
	return out
}

// holding is who holds what in one World: each user's own record, the
// roles the user holds through any path, and the users who hold each role
// through any path, every list sorted.
type holding struct {
	users  map[string]Holder
	roles  map[string][]string
	byRole map[string][]string
}

// holdingOf folds w.Holders, the direct holders, through the closures of
// w, each user once per role.
func holdingOf(w World, cl *closures) holding {
	h := holding{users: map[string]Holder{}, roles: map[string][]string{}, byRole: map[string][]string{}}
	direct := map[string][]string{}
	for role, hs := range w.Holders {
		for _, u := range hs {
			if _, ok := h.users[u.Username]; !ok {
				h.users[u.Username] = u
			}
			direct[u.Username] = append(direct[u.Username], role)
		}
	}
	// Walking the users in name order leaves every role's list sorted.
	for _, name := range sortedKeys(direct) {
		roles := direct[name]
		if len(roles) == 1 {
			h.roles[name] = cl.of(roles[0])
		} else {
			sort.Strings(roles)
			h.roles[name] = cl.union(slices.Compact(roles))
		}
		for _, r := range h.roles[name] {
			h.byRole[r] = append(h.byRole[r], name)
		}
	}
	return h
}

// subjectOf is the policy subject of the user u holding roles.
func subjectOf(u Holder, roles []string) policy.Subject {
	return policy.Subject{User: u.Username, Roles: roles, UserType: u.UserType, AgencyMode: u.AgencyMode, SwarmID: u.SwarmID}
}

// parseSets parses every set of policies by name, taking the set of known
// whose text in live is the same instead of parsing it again. A set that
// does not parse is left out, and the first error by set name is answered
// with the sets that do.
func parseSets(policies, live map[string]Policy, known map[string]policy.Document) (map[string]policy.Document, error) {
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	sort.Strings(names)
	docs := make(map[string]policy.Document, len(names))
	var first error
	for _, name := range names {
		if doc, ok := known[name]; ok && live[name].Text == policies[name].Text {
			docs[name] = doc
			continue
		}
		doc, err := policy.Parse([]byte(policies[name].Text))
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		docs[name] = doc
	}
	return docs, first
}

// gateMode reports whether r's mode makes a call wait for more than the
// policy: a person's approval, the requester's confirmation, the server's
// decision or a classifier's.
func gateMode(r policy.Rule) bool {
	switch r.Mode {
	case policy.ModeApprove, policy.ModeConfirm, policy.ModeServerCheck, policy.ModeClassify:
		return true
	}
	return false
}

// ruleSides reports whether r matches on allow-side patterns and on
// deny-side patterns.
func ruleSides(r policy.Rule) (allow, deny bool) {
	if r.Command != nil {
		allow, deny = len(r.Command.AllowPatterns) > 0, len(r.Command.DenyPatterns) > 0
	}
	for _, ad := range []*policy.AllowDeny{r.Paths, r.ToolNames, r.Interpreters} {
		if ad != nil {
			allow, deny = allow || len(ad.Allow) > 0, deny || len(ad.Deny) > 0
		}
	}
	return allow, deny
}

// canAllow reports whether r can answer allow. A rule with patterns
// answers by the side that matched, whatever its effect says, so only a
// rule without patterns answers with its effect.
func canAllow(r policy.Rule) bool {
	allow, deny := ruleSides(r)
	return allow || (!allow && !deny && r.Effect == policy.EffectAllow)
}

// isGate reports whether r is a gate: its effect is deny, it can
// deny through a deny-side pattern or a require predicate, or its mode is
// a gate.
func isGate(r policy.Rule) bool {
	_, deny := ruleSides(r)
	return gateMode(r) || deny || r.Effect == policy.EffectDeny || r.Require != nil
}

// tightens reports whether adding r to a set can only tighten it, which
// an agent may do: r cannot allow, or its mode is a gate that lets through
// no call the deployment denies by default.
func tightens(r policy.Rule, localDefault string) bool {
	return !canAllow(r) || (gateMode(r) && !opensDefault(r, localDefault))
}

// opensDefault reports whether r, a gate, turns a call the deployment
// denies by default into a held or checked one: a rule of a gate
// mode that can allow, on an event that waits for its decision, for a tool
// deniedKinds names.
func opensDefault(r policy.Rule, localDefault string) bool {
	return gateMode(r) && canAllow(r) && blocks(r) && len(deniedKinds(r, localDefault)) > 0
}

// deniedKinds answers the local tool kinds r governs that the deployment
// denies when no rule fires, sorted: those kinds while localToolDefault is
// deny, and none otherwise. The answer may be shared, so the caller must
// not change it. A gate on mcp.call never counts: the gateway enforces MCP
// calls where the access row grants the tool, the hook lane's default for
// an MCP tool is advisory, and who gains judges such a gate by the
// gateway's outcome.
func deniedKinds(r policy.Rule, localDefault string) []string {
	if localDefault != policy.EffectDeny {
		return nil
	}
	return localKinds(r)
}

// blocks reports whether r decides an event that waits for its answer,
// tool.pre or permission.request, where the deployment's default denies.
func blocks(r policy.Rule) bool {
	return slices.Contains(r.Events, policy.EventToolPre) || slices.Contains(r.Events, policy.EventPermissionRequest)
}

// ruleRef names r in the words of a risk: its id and the first 12 hex of
// the sha256 of its canonical JSON.
func ruleRef(r policy.Rule) string {
	b, err := json.Marshal(r)
	if err != nil {
		return r.ID + "@unreadable"
	}
	sum := sha256.Sum256(b)
	return r.ID + "@" + hex.EncodeToString(sum[:])[:12]
}

// sameRule reports whether a and b differ in nothing but their reason text.
func sameRule(a, b policy.Rule) bool {
	a.Reason, b.Reason = "", ""
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

// sameMatch reports whether two sets select the same sessions in the same
// order: the same match and the same priority.
func sameMatch(a, b policy.Document) bool {
	x, errA := json.Marshal(a.Spec.Match)
	y, errB := json.Marshal(b.Spec.Match)
	return errA == nil && errB == nil && string(x) == string(y) && a.Spec.Priority == b.Spec.Priority
}

// localTools are the tool kinds of a managed machine that
// governance.localToolDefault decides when no rule fires.
var localTools = []string{policy.ToolFileEdit, policy.ToolFileRead, policy.ToolFileWrite, policy.ToolNetFetch,
	policy.ToolOther, policy.ToolShellExec, policy.ToolTaskSpawn}

// toolKinds answers the tool kinds r can govern, sorted: the kinds it
// names, or the kinds its patterns exist for, and nil for a rule that
// governs every kind.
func toolKinds(r policy.Rule) []string {
	if len(r.Tools) > 0 {
		return sortedCopy(r.Tools)
	}
	var out []string
	if len(r.Apps) > 0 || r.ToolNames != nil {
		out = append(out, policy.ToolMCPCall)
	}
	if r.Command != nil || r.Interpreters != nil {
		out = append(out, policy.ToolShellExec)
	}
	if r.Paths != nil {
		out = append(out, policy.ToolFileEdit, policy.ToolFileRead, policy.ToolFileWrite)
	}
	return sortedCopy(out)
}

// kindsWords words the tool kinds r governs for a sentence.
func kindsWords(r policy.Rule) string {
	kinds := toolKinds(r)
	if len(kinds) == 0 {
		return "every tool"
	}
	return listWords(kinds, "and")
}

// listWords joins words for a sentence, as "a", "a and b" or "a, b and c",
// with join in place of and when it is set otherwise.
func listWords(words []string, join string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + join + " " + words[len(words)-1]
}

// sortedKeys answers the keys of every map, each once and sorted.
func sortedKeys[V any](maps ...map[string]V) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// unionSorted answers the words of a and b, each once and sorted.
func unionSorted(a, b []string) []string {
	out := append(slices.Clone(a), b...)
	sort.Strings(out)
	return slices.Compact(out)
}

// meets reports whether any of roles is in set.
func meets(roles []string, set map[string]bool) bool {
	return slices.ContainsFunc(roles, func(r string) bool { return set[r] })
}

// matchersOf answers, by server, the tool matchers of the access rows of
// every role in roles.
func matchersOf(w World, roles []string) map[string][]string {
	out := map[string][]string{}
	for _, r := range roles {
		if acc, ok := w.Access[r]; ok && acc.Server != "" {
			out[acc.Server] = append(out[acc.Server], acc.Tools...)
		}
	}
	return out
}

// roleNames answers the names of the roles of either world, sorted.
func roleNames(w, after World) []string {
	return sortedKeys(w.Roles, after.Roles)
}

// appNames answers the names of the servers of either world, sorted.
func appNames(w, after World) []string {
	return sortedKeys(w.Apps, after.Apps)
}

// everyTool reports whether tools holds a pattern made only of *, which
// matches every tool name, the names a server adds later included.
func everyTool(tools []string) bool {
	return slices.ContainsFunc(tools, func(t string) bool { return t != "" && strings.Trim(t, "*") == "" })
}

// matchWidens reports whether match b, turned into a, can select a
// session b did not: a selector list dropped, or a value added to a list.
func matchWidens(b, a policy.Match) bool {
	grows := func(was, now []string) bool {
		return (len(was) > 0 && len(now) == 0) || (len(was) > 0 && slices.ContainsFunc(now, func(v string) bool { return !slices.Contains(was, v) }))
	}
	bi, ai := identityLists(b), identityLists(a)
	return grows(b.Roles, a.Roles) || grows(b.Users, a.Users) || grows(bi[0], ai[0]) || grows(bi[1], ai[1]) || grows(bi[2], ai[2])
}

// matchNarrows reports whether match b, turned into a, can leave out a
// session b selected: a selector list added, or a value taken out of one.
func matchNarrows(b, a policy.Match) bool {
	shrinks := func(was, now []string) bool {
		return (len(was) == 0 && len(now) > 0) || (len(now) > 0 && slices.ContainsFunc(was, func(v string) bool { return !slices.Contains(now, v) }))
	}
	bi, ai := identityLists(b), identityLists(a)
	return shrinks(b.Roles, a.Roles) || shrinks(b.Users, a.Users) || shrinks(bi[0], ai[0]) || shrinks(bi[1], ai[1]) || shrinks(bi[2], ai[2])
}

// identityLists answers the user type, agency mode and swarm lists of m,
// each nil when m selects by no identity.
func identityLists(m policy.Match) [3][]string {
	if m.Identity == nil {
		return [3][]string{}
	}
	return [3][]string{m.Identity.UserType, m.Identity.AgencyMode, m.Identity.SwarmID}
}

// matchWords spells a match as its canonical JSON.
func matchWords(m policy.Match) string {
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}
