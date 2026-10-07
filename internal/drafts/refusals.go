package drafts

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// The codes of the check refusals: the draft is stored and cannot be
// published until they are fixed.
const (
	codeStale             = "draft.stale"
	codeAppProvider       = "app.provider"
	codeAppRuntime        = "app.runtime"
	codeAppRemoveMissing  = "app.remove-missing"
	codeRoleCreate        = "role.create"
	codeRoleKindChange    = "role.kind-change"
	codeRoleOwnerChange   = "role.owner-change"
	codeRoleOwnerRemoved  = "role.owner-removed"
	codeRoleDeleteProduct = "role.delete-product"
	codeRoleDeleteDecider = "role.delete-decider"
	codeRoleDeleteAdmin   = "role.delete-admin-role"
	codeAccessRole        = "access.role"
	codeAccessOwned       = "access.owned"
	codeAccessServer      = "access.server-missing"
	codeImplyRule         = "imply.rule"
	codeImplyMissing      = "imply.missing"
	codePolicyPool        = "policy.pool"
	codePolicyMatch       = "policy.match"
	codePolicyObligations = "policy.obligations"
	codePolicyCompile     = "policy.compile"
	codeAgentStrazaReach  = "agent.straza-reach"
)

// credentialClientCredentials is the agents value that gives each agent a
// token of its own client at the server's OAuth provider.
const credentialClientCredentials = "client_credentials"

// fullStanding is the standing every check rule is judged with, so the verdict
// reads the same for every viewer. The publisher's own standing is judged
// at publish.
var fullStanding = Standing{Full: true}

// staleFindings refuses every item whose base is not the live fingerprint,
// naming the newest publish of its object when changed holds it.
func staleFindings(w World, d Draft, changed map[string]LastChange) []Finding {
	var out []Finding
	for _, it := range d.Items {
		object := it.Object()
		if it.Base == w.Fingerprints[object] {
			continue
		}
		clause := ""
		if c, ok := changed[object]; ok {
			clause = fmt.Sprintf(", when %s published draft %d at %s", visible(c.Publisher), c.Draft, c.At.UTC().Format("2006-01-02 15:04 UTC"))
		}
		// A revision keeps the base of every object it already holds,
		// so an update never clears this. Check again is the one way that
		// moves the base, keeping what the draft changed.
		out = append(out, refusal(codeStale, object,
			fmt.Sprintf("%s changed after this draft was checked%s.", visible(object), clause),
			"Check the draft again with Check again on the console or strazactl drafts rebase "+cmp.Or(d.ID, "<id>")+
				". Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed."))
	}
	return out
}

// checkRefusals answers the check refusals that read w, after and the
// items and need no who-gains table: a text that is not one document, the
// provider of an App, a removal of a server that does not exist, the role
// rules, the access and implication rules, and the policy gates. docsA is
// the sets of after, each parsed once for the whole check, and apps holds
// the facts of every App d puts. Its cost grows with the items and the
// sets, never with their product.
func checkRefusals(w, after World, docsA map[string]policy.Document, d Draft, apps map[string]App) []Finding {
	deciders, admins, removed, graph := decidersOf(docsA), adminRolesOf(after), removedApps(w, d), newRoleGraph(after)
	var out []Finding
	for i, it := range d.Items {
		// ParseRole refuses a Role text that is not one document itself.
		if it.Op != OpRemove && it.Kind != KindRole {
			if fs := oneDocument(i+1, it); len(fs) > 0 {
				out = append(out, fs...)
				continue
			}
		}
		switch {
		case it.Kind == KindApp && it.Op == OpPut:
			out = append(out, appRefusals(w, it, apps)...)
		case it.Kind == KindApp && it.Op == OpRemove:
			if _, ok := w.Apps[it.Name]; !ok {
				out = append(out, refusal(codeAppRemoveMissing, it.Object(), "unknown server",
					"Nothing is removed. List the servers with strazactl apps list, and leave this removal out of the draft."))
			}
		case it.Kind == KindRole && it.Op == OpPut:
			out = append(out, rolePutRefusals(w, after, graph, it, removed)...)
		case it.Kind == KindRole && it.Op == OpRemove:
			out = append(out, roleRemoveRefusals(it, deciders, admins)...)
		case it.Kind == KindPolicySet && it.Op == OpPut:
			out = append(out, policyRefusals(after, docsA, it)...)
		case it.Kind == KindPolicySet && it.Op == OpOff:
			_, fs := readPolicy(it, it.Doc)
			out = append(out, fs...)
		}
	}
	return out
}

// appRefusals answers the refusals of an App put: facts the server did not
// hand in, which fail closed, a command server on a server that refuses
// them, as the manager's CheckRuntime does, and the provider rules of the
// manifest parser's CheckProvider, over the providers of w.
func appRefusals(w World, it Item, apps map[string]App) []Finding {
	app, ok := apps[it.Name]
	if !ok {
		return []Finding{refusal(codeAppParse, it.Object(),
			fmt.Sprintf("Straza did not read the manifest of %s, so it cannot check the draft.", visible(it.Name)),
			"Check the draft again, and read the strazad log if it keeps failing.")}
	}
	if w.RefuseCommand && app.Runtime == runtimeCommand {
		return []Finding{refusal(codeAppRuntime, it.Object(), it.Name+
			" runs as a command, which this server refuses under the enterprise profile, because the process would run inside Straza with access to its keys and database.",
			"Run the server as its own service or pod and add it as a remote server over HTTP.")}
	}
	if app.Credential != CredentialOAuth {
		return nil
	}
	known := make([]string, 0, len(w.Providers))
	for name := range w.Providers {
		known = append(known, name)
	}
	sort.Strings(known)
	p, ok := w.Providers[app.Provider]
	switch {
	case !ok:
		msg := fmt.Sprintf("credential.oauth.provider %q is not configured on this server. Add oauth.providers.%s to the strazad config", app.Provider, app.Provider)
		if len(known) > 0 {
			msg += ", or pick one of: " + strings.Join(known, ", ")
		}
		return []Finding{refusal(codeAppProvider, it.Object(), msg+".", "")}
	case app.Agents == credentialClientCredentials && !p.ClientCredentials:
		return []Finding{refusal(codeAppProvider, it.Object(), fmt.Sprintf(
			"server %s sets credential.agents to client_credentials, and the provider %s has no clientCredentials settings. "+
				"Add oauth.providers.%s.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared.",
			it.Name, app.Provider, app.Provider), "")}
	}
	return nil
}

// rolePutRefusals answers the refusals of a Role put: a document that does
// not read, a role whose server the draft removes, the create rules for an
// absent role and the kind and owner rules for a present one, the access
// row, and every implication the draft adds, its cycles read from graph,
// the graph of after. removed names the live servers the draft removes,
// which take the roles they own with them.
func rolePutRefusals(w, after World, graph *roleGraph, it Item, removed map[string]bool) []Finding {
	object := it.Object()
	doc, read := after.roleDocs[it.Name]
	fs := roleShape(it.Name, doc)
	if !read {
		doc, fs = readRole(it.Name, it.Doc)
	}
	if doc.Metadata.Name == "" || len(fs) > 0 {
		return fs
	}
	if server := doc.Spec.Server; removed[server] {
		return []Finding{refusal(codeRoleOwnerRemoved, object,
			fmt.Sprintf("%s belongs to the server %s, which this draft removes, so the role goes with it.", visible(it.Name), visible(server)),
			"Remove the role's item, or keep the server.")}
	}
	var out []Finding
	add := func(code string, ref *Refusal) {
		if ref != nil {
			out = append(out, refusal(code, object, ref.Sentence, ""))
		}
	}
	var tools []string
	if len(doc.Spec.Bindings) > 0 {
		tools = doc.Spec.Bindings[0].Tools
	}
	live, present := w.Roles[it.Name]
	switch {
	case !present:
		// The servers are the ones the draft leaves, so a role may belong
		// to a server the same draft adds, and the names are live, where
		// the role does not exist yet.
		view := World{Apps: after.Apps, Roles: w.Roles}
		add(codeRoleCreate, RoleCreateRefusal(view, fullStanding, RoleSpec{Name: it.Name, Kind: doc.Spec.Kind, Server: doc.Spec.Server, Tools: tools}))
	default:
		if doc.Spec.Kind != wireKind(live) {
			add(codeRoleKindChange, RoleKindChangeRefusal(live, fullStanding, doc.Spec.Kind))
		}
		if doc.Spec.Server != live.Owner || (doc.Spec.Server != "") != live.Owned {
			out = append(out, refusal(codeRoleOwnerChange, object,
				fmt.Sprintf("%s is %s, and a role's owner is fixed at create.", visible(it.Name), ownerWords(live)),
				"Create a new role for the server you mean, move its holders, then remove this one."))
		}
	}
	if ro, ok := after.Roles[it.Name]; ok && len(doc.Spec.Bindings) > 0 {
		b := doc.Spec.Bindings[0]
		// The row rules judge a row the put adds or changes. A row live state
		// holds and the put keeps, as every direct route sends it back, was
		// judged when it was made, perhaps before a rule existed.
		if !rowKept(w, it.Name, b) {
			if msg := roleRefusal(ro, "cannot be given tool access", "Give an application role access instead."); msg != "" {
				out = append(out, refusal(codeAccessRole, object, msg, ""))
			} else if _, ok := after.Apps[b.App]; ok {
				// The live rows decide, so a role keeps the server it reaches.
				view := after
				view.Access = w.Access
				add(codeAccessOwned, unownedRowRefusal(view, ro, b.App))
			}
			add(codeAccessOwned, ownedAccessRefusal(after, fullStanding, ro, b.App, b.Tools, true))
		}
		_, live := w.Apps[b.App]
		switch _, ok := after.Apps[b.App]; {
		case !ok && live:
			out = append(out, refusal(codeAccessServer, object,
				fmt.Sprintf("%s gives access on %s, which this draft removes.", visible(it.Name), visible(b.App)),
				fmt.Sprintf("Drop the binding, or keep %s.", visible(b.App))))
		case !ok:
			out = append(out, refusal(codeAccessServer, object,
				fmt.Sprintf("%s gives access on %s, which is not a registered MCP server and is not added by this draft.", visible(it.Name), visible(b.App)),
				"Check the name with strazactl apps list."))
		}
	}
	kept := make(map[string]bool, len(w.Implies[it.Name]))
	for _, implied := range w.Implies[it.Name] {
		kept[implied] = true
	}
	for _, implied := range doc.Spec.Implies {
		if _, ok := after.Roles[implied]; !ok {
			if _, live := w.Roles[implied]; live {
				out = append(out, refusal(codeImplyMissing, object,
					fmt.Sprintf("%s implies %s, which this draft removes.", visible(it.Name), visible(implied)),
					fmt.Sprintf("Drop the implication, or keep %s.", visible(implied))))
				continue
			}
			out = append(out, refusal(codeImplyMissing, object,
				fmt.Sprintf("%s implies %s, which is not a role in Straza and is not created by this draft.", visible(it.Name), visible(implied)),
				"Create it first, or fix the name."))
			continue
		}
		if !kept[implied] {
			add(codeImplyRule, graph.implicationRefusal(after.Roles, it.Name, implied))
		}
	}
	return dedupe(out)
}

// ownerWords says whom a role belongs to, for role.owner-change.
func ownerWords(ro Role) string {
	switch {
	case ro.Owned && ro.Owner != "":
		return "owned by the server " + ro.Owner
	case ro.Owned:
		return "owned by a server that was removed"
	}
	return "a global role"
}

// roleRemoveRefusals answers the refusals of a Role removal, in the order
// the delete route checks them: a product role, a role that decides a rule
// of a set the draft leaves live, and a server's admin role. deciders and
// admins are decidersOf and adminRolesOf, read once for the whole check.
func roleRemoveRefusals(it Item, deciders, admins map[string][]string) []Finding {
	object := it.Object()
	var out []Finding
	if strings.HasPrefix(strings.ToLower(it.Name), "straza-") {
		out = append(out, refusal(codeRoleDeleteProduct, object, fmt.Sprintf(
			"role %q comes with the product and cannot be deleted. To take someone's access away, remove their assignment instead", it.Name), ""))
	}
	if naming := deciders[it.Name]; len(naming) > 0 {
		out = append(out, refusal(codeRoleDeleteDecider, object, fmt.Sprintf(
			"role %q is named among the deciders of a live policy (%s): deleting it would leave those approvals to expire unanswered. Remove it from approve.roles and publish the set again, or turn the set off, then delete the role",
			it.Name, strings.Join(naming, ", ")), ""))
	}
	if servers := admins[it.Name]; len(servers) > 0 {
		out = append(out, refusal(codeRoleDeleteAdmin, object, fmt.Sprintf(
			"role %q is the admin role of server %s and lives as long as the server does. Remove the server instead.",
			it.Name, strings.Join(servers, ", ")), ""))
	}
	return out
}

// decidersOf maps each role an approve pool of docs names to the set and
// rule that name it, as `set "S" rule "R"`, the sets by name and the rules
// in order, each rule once. A set that does not parse is not in docs and
// names nobody, as the delete route skips it.
func decidersOf(docs map[string]policy.Document) map[string][]string {
	names := make([]string, 0, len(docs))
	for name := range docs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string][]string{}
	for _, name := range names {
		for _, r := range docs[name].Spec.Rules {
			if r.Mode != policy.ModeApprove || r.Approve == nil {
				continue
			}
			line := fmt.Sprintf("set %q rule %q", name, r.ID)
			for _, role := range r.Approve.Roles {
				if list := out[role]; len(list) == 0 || list[len(list)-1] != line {
					out[role] = append(list, line)
				}
			}
		}
	}
	return out
}

// adminRolesOf maps each server's admin role in w to the servers it
// administers, sorted.
func adminRolesOf(w World) map[string][]string {
	out := map[string][]string{}
	for name, app := range w.Apps {
		if app.AdminRole != "" {
			out[app.AdminRole] = append(out[app.AdminRole], name)
		}
	}
	for _, list := range out {
		sort.Strings(list)
	}
	return out
}

// removedApps names the live servers d removes.
func removedApps(w World, d Draft) map[string]bool {
	out := map[string]bool{}
	for _, it := range d.Items {
		if _, live := w.Apps[it.Name]; live && it.Kind == KindApp && it.Op == OpRemove {
			out[it.Name] = true
		}
	}
	return out
}

// policyRefusals answers the refusals of a PolicySet put: a text that does
// not parse, and the three gates an activate runs, over the roles the draft
// leaves. docsA holds the set as the check parsed it.
func policyRefusals(after World, docsA map[string]policy.Document, it Item) []Finding {
	doc, ok := docsA[it.Name]
	if !ok {
		_, fs := readPolicy(it, it.Doc)
		return fs
	}
	var out []Finding
	for _, gate := range []struct {
		code  string
		lines []string
	}{
		{codePolicyPool, approvePoolViolations(after, doc, true)},
		{codePolicyMatch, MatchRoleViolations(after, doc)},
		{codePolicyObligations, ObligationViolations(doc)},
	} {
		for _, line := range gate.lines {
			out = append(out, refusal(gate.code, it.Object(), line, ""))
		}
	}
	return out
}

// agentRefusals answers the agent rules of a check: the rules of every item
// that need no live state, an agent with no sponsor, a Straza role the draft
// writes or removes, a role that would reach a Straza role, and a set whose
// recording the draft changes. docsW and docsA are the sets of w and of
// after, each parsed once for the whole check.
func agentRefusals(w, after World, docsW, docsA map[string]policy.Document, d Draft, proposer Holder) []Finding {
	graph := newRoleGraph(after)
	var out []Finding
	if proposer.Agent && proposer.Sponsor == "" {
		out = append(out, sponsorRefusal(proposer.Username))
	}
	for _, it := range d.Items {
		out = append(out, agentItem(it, after.roleDocs)...)
		switch it.Kind {
		case KindRole:
			if ro, ok := w.Roles[it.Name]; ok && ro.Plane == PlaneControl {
				out = append(out, strazaRoleRefusal(it.Object(), it.Name))
			}
			if it.Op == OpPut {
				out = append(out, strazaReach(graph, it)...)
			}
		case KindPolicySet:
			if recordingChanged(docsW, docsA, it) {
				out = append(out, refusal(codeAgentCapture, it.Object(),
					fmt.Sprintf("The policy set %s changes conversation recording, and an agent cannot propose that, because recording decides what Straza keeps of every session the set matches.", visible(it.Name)),
					"Leave spec.capture as it is. A person changes it."))
			}
		}
	}
	return dedupe(out)
}

// recordingChanged reports whether item it changes what its set records:
// whether the set records conversations, in which mode, or, for a set that
// records before or after, the sessions its match selects. docsW and docsA
// are the sets of live state and of the state after the draft. A text that
// does not parse changes nothing here, because its parse refusal stands.
func recordingChanged(docsW, docsA map[string]policy.Document, it Item) bool {
	var before, after *policy.Capture
	var matchBefore, matchAfter policy.Match
	if doc, ok := docsW[it.Name]; ok {
		before, matchBefore = doc.Spec.Capture, doc.Spec.Match
	}
	if it.Op == OpPut {
		doc, ok := docsA[it.Name]
		if !ok {
			return false
		}
		after, matchAfter = doc.Spec.Capture, doc.Spec.Match
	}
	rb, ra := recordMode(before), recordMode(after)
	if rb == "" && ra == "" {
		return false
	}
	mb, _ := json.Marshal(matchBefore)
	ma, _ := json.Marshal(matchAfter)
	return rb != ra || string(mb) != string(ma)
}

// recordMode is the mode a capture block records conversations in, or ""
// when it records none.
func recordMode(c *policy.Capture) string {
	switch {
	case c == nil || !c.Conversations:
		return ""
	case c.Mode == "":
		return policy.CaptureModeVerbatim
	}
	return c.Mode
}

// strazaReach refuses an agent's Role put whose role would reach a Straza
// role through its implications, once for each such role, from the Straza
// roles graph found its component reaching.
func strazaReach(graph *roleGraph, it Item) []Finding {
	c, ok := graph.comp[it.Name]
	if !ok {
		return nil
	}
	var out []Finding
	for _, name := range graph.reach[c] {
		if name != it.Name {
			out = append(out, refusal(codeAgentStrazaReach, it.Object(),
				fmt.Sprintf("%s would imply %s, a Straza role, and an agent's draft may not reach one.", visible(it.Name), name),
				"Leave the implication out. A person can add it."))
		}
	}
	return out
}

// roleGraph is the implication graph of one world, walked once for a whole
// check: the strongly connected component of every role on an edge, so an
// edge closes a cycle exactly when its two roles share a component, and
// the Straza roles each component reaches, sorted.
type roleGraph struct {
	roles      map[string]Role
	implies    map[string][]string
	index, low map[string]int
	onStack    map[string]bool
	stack      []string
	comp       map[string]int
	reach      [][]string
}

// newRoleGraph walks the edges of w once, by Tarjan's algorithm, in time
// linear in its roles and edges.
func newRoleGraph(w World) *roleGraph {
	g := &roleGraph{roles: w.Roles, implies: w.Implies, index: map[string]int{}, low: map[string]int{},
		onStack: map[string]bool{}, comp: map[string]int{}}
	names := make([]string, 0, len(w.Implies))
	for name := range w.Implies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, seen := g.index[name]; !seen {
			g.visit(name)
		}
	}
	return g
}

// visit is Tarjan's step from role v. A component is complete only after
// every component it reaches, so the Straza roles those reach are known
// when its own are gathered.
func (g *roleGraph) visit(v string) {
	g.index[v], g.low[v] = len(g.index), len(g.index)
	g.stack, g.onStack[v] = append(g.stack, v), true
	for _, u := range g.implies[v] {
		if _, seen := g.index[u]; !seen {
			g.visit(u)
			g.low[v] = min(g.low[v], g.low[u])
		} else if g.onStack[u] {
			g.low[v] = min(g.low[v], g.index[u])
		}
	}
	if g.low[v] != g.index[v] {
		return
	}
	id := len(g.reach)
	var members []string
	for {
		u := g.stack[len(g.stack)-1]
		g.stack, g.onStack[u], g.comp[u] = g.stack[:len(g.stack)-1], false, id
		members = append(members, u)
		if u == v {
			break
		}
	}
	reach := map[string]bool{}
	for _, u := range members {
		if g.roles[u].Plane == PlaneControl {
			reach[u] = true
		}
		for _, x := range g.implies[u] {
			if c := g.comp[x]; c != id {
				for _, s := range g.reach[c] {
					reach[s] = true
				}
			}
		}
	}
	names := make([]string, 0, len(reach))
	for name := range reach {
		names = append(names, name)
	}
	sort.Strings(names)
	g.reach = append(g.reach, names)
}

// implicationRefusal is ImplicationRefusal for the edge from role to
// implies in the graph's world, whose roles are roles.
func (g *roleGraph) implicationRefusal(roles map[string]Role, role, implies string) *Refusal {
	// ImplicationRefusal walks the edges it is given to find a cycle, and
	// the components have answered that for every edge at once. It is given
	// no edge, or the one edge back when both roles share a component, so
	// it answers the owned and approver rules and a cycle in its own words
	// and order.
	var back map[string][]string
	cr, okR := g.comp[role]
	ci, okI := g.comp[implies]
	if okR && okI && cr == ci {
		back = map[string][]string{implies: {role}}
	}
	return ImplicationRefusal(World{Roles: roles, Implies: back}, role, implies)
}

// closure answers role and every role it reaches over implies, sorted.
func closure(implies map[string][]string, role string) []string {
	seen := map[string]bool{role: true}
	queue := []string{role}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range implies[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// dedupe drops a finding whose object and sentence an earlier one already
// has, such as the every-tool sentence that the create rules and the
// access rules both answer for one owned role.
func dedupe(fs []Finding) []Finding {
	seen := map[string]bool{}
	out := fs[:0:0]
	for _, f := range fs {
		key := f.Object + "\n" + f.Sentence
		if !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	return out
}

// rowKept reports whether the binding b of the role name is the access row
// live state w holds for it: the same server and the same tools, each once
// and in any order.
func rowKept(w World, name string, b RoleBinding) bool {
	acc, ok := w.Access[name]
	return ok && acc.Server == b.App && slices.Equal(slices.Compact(sortedCopy(acc.Tools)), slices.Compact(sortedCopy(b.Tools)))
}
