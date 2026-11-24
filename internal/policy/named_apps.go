package policy

import (
	"sort"
	"strings"
)

// NamedApp is one MCP server that the policy rules applying to a subject
// name in apps, with the tool names the rules list whole for it.
type NamedApp struct {
	// App is the server's name as the rules write it.
	App string
	// Tools are the tool names the rules list whole on either side of
	// toolNames, sorted. A pattern is not among them.
	Tools []string
	// Open says that a rule decides tools of the server which Tools does
	// not hold: it names them by a pattern, it names no tool and so
	// reaches every one, the subject fails its require block and so it
	// denies every one, or the answer's bound cut the names it lists.
	Open bool
	// Policies are the names of the policies whose rules name the server
	// in apps, sorted.
	Policies []string
}

// namedToolsMax bounds the tool names one NamedApps answer lists over all
// its servers. A rule's servers times its tool names can be far more than
// the policy's own size, so without a bound one policy could make each
// reading cost that product.
const namedToolsMax = 2000

// namedBuild collects one server's NamedApp.
type namedBuild struct {
	tools    map[string]bool
	policies map[string]bool
	open     bool
}

// NamedApps lists the MCP servers that the rules of every policy applying
// to sub name in apps, sorted by name. It reads the compiled policies and
// decides nothing: the outcome of a tool is what Evaluate answers. Only a
// rule that can decide an MCP call before it runs counts. A rule with no
// apps list names no server, and what it lists and reaches counts for
// every server the other rules name. keep chooses the servers to list, and
// nil keeps every one: a server it refuses is left out and spends nothing
// of the bound. The answer lists at most namedToolsMax tool names and is
// never nil.
func (e *Engine) NamedApps(sub Subject, keep func(app string) bool) []NamedApp {
	apps := map[string]*namedBuild{}
	left := namedToolsMax
	add := func(nb *namedBuild, names []string, more bool) {
		nb.open = nb.open || more
		for _, n := range names {
			if nb.tools[n] {
				continue
			}
			if left == 0 {
				nb.open = true
				return
			}
			nb.tools[n] = true
			left--
		}
	}
	var anywhere []*compiledRule
	for i := range e.sets {
		set := &e.sets[i]
		if !set.applies(sub) {
			continue
		}
		for j := range set.rules {
			r := &set.rules[j]
			if !r.events[EventToolPre] || (r.tools != nil && !r.tools[ToolMCPCall]) {
				continue
			}
			if r.apps == nil {
				anywhere = append(anywhere, r)
				continue
			}
			names, more := r.reachOn(sub)
			for _, app := range sortedNames(r.apps) {
				if keep != nil && !keep(app) {
					continue
				}
				nb := apps[app]
				if nb == nil {
					nb = &namedBuild{tools: map[string]bool{}, policies: map[string]bool{}}
					apps[app] = nb
				}
				nb.policies[set.name] = true
				add(nb, names, more)
			}
		}
	}
	order := make([]string, 0, len(apps))
	for app := range apps {
		order = append(order, app)
	}
	sort.Strings(order)
	for _, r := range anywhere {
		names, more := r.reachOn(sub)
		for _, app := range order {
			add(apps[app], names, more)
		}
	}
	out := make([]NamedApp, 0, len(order))
	for _, app := range order {
		nb := apps[app]
		out = append(out, NamedApp{App: app, Tools: sortedNames(nb.tools), Open: nb.open, Policies: sortedNames(nb.policies)})
	}
	return out
}

// sortedNames answers the members of set in order, nil for an empty set.
func sortedNames(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// reachOn answers the tool names r lists whole in toolNames, on the allow
// and the deny side, and more, which says that r also decides names it
// does not list for sub: one of its toolNames is a pattern, it has an
// effect and no matcher and so applies to every tool of its servers, or
// sub fails its require block and so it denies every tool of them.
func (r *compiledRule) reachOn(sub Subject) (names []string, more bool) {
	more = r.require != nil && !r.require.satisfiedBy(sub)
	if !r.hasMatcher {
		return nil, more || r.effect != ""
	}
	for _, side := range [][]matcher{r.tnAllow, r.tnDeny} {
		for i := range side {
			if isToolPattern(side[i].src) {
				more = true
				continue
			}
			names = append(names, side[i].src)
		}
	}
	return names, more
}

// isToolPattern reports whether a toolNames entry can match more than one
// name: an anchored regex, or a glob with a wildcard (SPEC.md §4).
func isToolPattern(p string) bool {
	return strings.HasPrefix(p, "re:") || strings.ContainsAny(p, "*?")
}
