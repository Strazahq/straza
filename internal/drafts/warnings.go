package drafts

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/strazahq/straza/internal/policy"
)

// The codes of the warnings: what will not work yet, what a change takes
// away, and what a heuristic suspects.
const (
	codeMatchUnknown    = "ready.match-unknown"
	codeRuleNothing     = "ready.rule-names-nothing"
	codePoolEmpty       = "ready.pool-empty"
	codeDeciderDevice   = "ready.decider-device"
	codeNoPush          = "ready.no-push"
	codeHoldShort       = "ready.hold-short"
	codeSecretMissing   = "ready.secret-missing"
	codeNobodyConnected = "ready.nobody-connected"
	codeToolUnknown     = "ready.tool-unknown"
	codeApproveDeny     = "ready.approve-deny"
	codeTicketArgs      = "ready.ticket-args"
	codeWindowLow       = "ready.window-low"
	codeNoDocker        = "ready.no-docker"
	codeSetName         = "ready.set-name"
	codeRoleLoses       = "narrow.role-loses"
	codeSetOrphaned     = "narrow.set-orphaned"
	codeDestructive     = "heuristic.destructive"
	codeRegistryAddress = "heuristic.registry-address"
	codeRevertLossy     = "revert.lossy"
	codeFileLinked      = "server.file-linked"
)

// holdShortSeconds is the hold below which a person who is not watching
// the console rarely answers in time when no push lane tells them, and
// defaultHoldSeconds the hold a rule gets when it names none, as the
// policy engine fills it in.
const (
	holdShortSeconds   = 300
	defaultHoldSeconds = 90
)

// lowWindows are the approval windows ready.window-low reads, each with the
// value below which a window sits at the low end of its range.
var lowWindows = []struct {
	field string
	value func(*policy.ApproveSpec) int
	below int
}{
	{"approve.timeoutSeconds", func(a *policy.ApproveSpec) int { return a.TimeoutSeconds }, 30},
	{"approve.retryTTLSeconds", func(a *policy.ApproveSpec) int { return a.RetryTTLSeconds }, 10},
	{"approve.ticketTTLSeconds", func(a *policy.ApproveSpec) int { return a.TicketTTLSeconds }, 3600},
	{"approve.grantTTLSeconds", func(a *policy.ApproveSpec) int { return a.GrantTTLSeconds }, 300},
}

// destructiveVerbs are the words of a tool name that say the tool destroys
// something, each with the verb heuristic.destructive says it with.
var destructiveVerbs = map[string]string{"delete": "deletes", "remove": "removes", "drop": "drops", "destroy": "destroys",
	"purge": "purges", "wipe": "wipes", "revoke": "revokes"}

// volatileWords are the words of a tool name that suggest its arguments may
// carry new content on every call, which ready.ticket-args reads.
var volatileWords = map[string]bool{"append": true, "comment": true, "create": true, "execute": true, "post": true, "push": true,
	"query": true, "run": true, "search": true, "send": true, "upload": true, "write": true}

// warning is a warning finding, its texts spelled with visible.
func warning(code, object, sentence, fix string) Finding {
	return Finding{Code: code, Class: ClassWarning, Object: visible(object), Sentence: visible(sentence), Fix: visible(fix)}
}

// warnings answers the warnings of step 7: the readiness of the sets,
// servers and roles the draft writes, what its removals take away, the
// warnings of who gains, and scanned, the secret scan's warnings.
func (c *check) warnings(rows []gainRow, scanned []Finding) []Finding {
	var out []Finding
	changed := c.changedSets()
	for _, name := range sortedKeys(c.docsA) {
		out = append(out, c.setWarnings(name, changed[name])...)
	}
	for _, it := range c.d.Items {
		out = append(out, c.itemWarnings(it)...)
	}
	out = append(out, c.lossy()...)
	for _, f := range scanned {
		if f.Class == ClassWarning {
			out = append(out, f)
		}
	}
	return dedupe(append(out, c.gainWarnings(rows)...))
}

// setWarnings answers the warnings of the set name after the draft: a rule
// that names a server the draft removes, and when the draft writes the set,
// every readiness line of its match and rules and the server's advisories.
func (c *check) setWarnings(name string, written bool) []Finding {
	a, object := c.docsA[name], "PolicySet/"+name
	var out []Finding
	for _, rule := range a.Spec.Rules {
		for _, s := range rule.Apps {
			_, live := c.w.Apps[s]
			if _, ok := c.after.Apps[s]; !ok && s != nativeApp && (written || live) {
				out = append(out, warning(codeRuleNothing, object, fmt.Sprintf("Rule %s of %s names %s, which is not a registered server.", rule.ID, name, s), "Fix the name."))
			}
		}
	}
	if !written {
		return out
	}
	if roles := a.Spec.Match.Roles; len(roles) > 0 && !slices.ContainsFunc(roles, func(r string) bool { _, ok := c.after.Roles[r]; return ok }) {
		for _, r := range roles {
			out = append(out, warning(codeMatchUnknown, object, fmt.Sprintf("match.roles of %s names %s, which is not a role in Straza, so the set matches nobody.", name, r),
				"Fix the name, or create the role in this draft."))
		}
	}
	for _, rule := range a.Spec.Rules {
		out = append(out, c.ruleWarnings(name, a, rule)...)
	}
	if m := a.Spec.Match; len(m.Roles) == 1 && len(m.Users) == 0 && m.Identity == nil && c.after.Roles[m.Roles[0]].Kind == RoleKindApplication && name != m.Roles[0]+"-access" {
		out = append(out, warning(codeSetName, object, fmt.Sprintf("%s gates %s and is not named %s-access, so the console's role editor will not read it back.", name, m.Roles[0], m.Roles[0]),
			fmt.Sprintf("Name it %s-access.", m.Roles[0])))
	}
	advisories := c.in.Advisories
	if advisories == nil {
		advisories = policy.Advisories
	}
	for _, adv := range advisories(a) {
		out = append(out, warning("advisory."+adv.Code, object, adv.Text, ""))
	}
	return out
}

// ruleWarnings answers the readiness lines of one rule of a set the draft
// writes: names nothing offers, a pool nobody can decide, a hold nobody
// learns of in time, and a gate that never holds or never lets its
// approval be used.
func (c *check) ruleWarnings(name string, doc policy.Document, rule policy.Rule) []Finding {
	object := "PolicySet/" + name
	var out []Finding
	if len(rule.Apps) == 1 && rule.ToolNames != nil {
		s := rule.Apps[0]
		offered, known := c.after.Apps[s].Offered, c.after.Apps[s].Offered != nil
		if s == nativeApp {
			offered, known = nativeTools, true
		}
		for _, t := range append(slices.Clone(rule.ToolNames.Allow), rule.ToolNames.Deny...) {
			if known && !strings.Contains(t, "*") && !strings.HasPrefix(t, "re:") && !slices.Contains(offered, t) {
				out = append(out, warning(codeRuleNothing, object, fmt.Sprintf("Rule %s of %s names %s on %s, which the server does not offer.", rule.ID, name, t, s), "Fix the name."))
			}
		}
	}
	if rule.Mode != policy.ModeApprove && rule.Mode != policy.ModeConfirm {
		return out
	}
	a := rule.Approve
	if a == nil {
		a = &policy.ApproveSpec{}
	}
	if rule.Effect == policy.EffectDeny {
		out = append(out, warning(codeApproveDeny, object, fmt.Sprintf("Rule %s of %s sets mode %s with effect deny, so its calls are denied and never held.", rule.ID, name, rule.Mode),
			fmt.Sprintf("Drop effect: deny, or drop the %s mode.", rule.Mode)))
	}
	out = append(out, c.poolWarnings(name, doc, rule, a)...)
	if !c.w.Push && !c.w.Slack {
		out = append(out, warning(codeNoPush, object, fmt.Sprintf("Rule %s of %s holds calls, and neither a push lane nor Slack is configured, so a person learns of a request only on the console.", rule.ID, name),
			"Configure approval.push or approval.channels.slack, or keep someone watching Approvals."))
		if timeout := cmp.Or(a.TimeoutSeconds, defaultHoldSeconds); a.Class != policy.ClassTicket && timeout < holdShortSeconds {
			out = append(out, warning(codeHoldShort, object, fmt.Sprintf("Rule %s of %s holds a call for %s, which is short for a person who is not watching the console.", rule.ID, name, duration(timeout)),
				"Lengthen approve.timeoutSeconds, or configure a push lane or Slack."))
		}
	}
	if a.Class == policy.ClassTicket && a.Binding != policy.ApproveBindingTool && rule.ToolNames != nil {
		for _, t := range rule.ToolNames.Allow {
			if slices.ContainsFunc(nameWords(t), func(w string) bool { return volatileWords[w] }) {
				out = append(out, warning(codeTicketArgs, object, fmt.Sprintf(
					"Rule %s of %s binds a ticket to the exact arguments of %s, so an approval is used only by a later call with the same arguments.", rule.ID, name, t),
					fmt.Sprintf("If the arguments of %s change on every call, set approve.binding: tool, which lets one approval cover any arguments of %s.", t, t)))
			}
		}
	}
	for _, lw := range lowWindows {
		if v := lw.value(a); v > 0 && v < lw.below {
			out = append(out, warning(codeWindowLow, object, fmt.Sprintf("Rule %s of %s sets %s to %s, the low end of its range, which a person rarely meets.", rule.ID, name, lw.field, strconv.Itoa(v)),
				"Raise it."))
		}
	}
	return out
}

// poolWarnings answers who cannot decide the requests of a gate: an
// approver pool no person holds, and people who decide their own requests
// with no enrolled phone or browser.
func (c *check) poolWarnings(name string, doc policy.Document, rule policy.Rule, a *policy.ApproveSpec) []Finding {
	object := "PolicySet/" + name
	var out []Finding
	sponsor := slices.Contains(a.Deciders, policy.DeciderSponsor) || (len(a.Roles) == 0 && len(a.Deciders) == 0 && !a.SelfApproval)
	if rule.Mode == policy.ModeApprove && len(a.Roles) > 0 && !sponsor && !a.SelfApproval {
		people := slices.ContainsFunc(a.Roles, func(r string) bool {
			return slices.ContainsFunc(c.hA.byRole[r], func(u string) bool { return !c.hA.users[u].Agent })
		})
		for _, r := range a.Roles {
			if !people {
				out = append(out, warning(codePoolEmpty, object, fmt.Sprintf("No person who could decide rule %s of %s holds %s yet, so every request of that rule waits until it expires.", rule.ID, name, r),
					fmt.Sprintf("Assign %s to a person in your identity manager, or with strazactl assign %s --user <name>.", r, r)))
			}
		}
	}
	confirm := rule.Mode == policy.ModeConfirm
	if (confirm || sponsor) && c.ownDecidersWithoutDevice(doc, confirm) {
		out = append(out, warning(codeDeciderDevice, object, fmt.Sprintf(
			"The requests of rule %s of %s go to people who have no enrolled phone or browser, and the console and strazactl cannot decide a person's own request.", rule.ID, name),
			"Assign them straza-enroll-browser or straza-enroll-mobile, which let them enroll on the self-service page, "+
				"or run strazactl approvals enroll-token <username> for each of them."))
	}
	return out
}

// ownDecidersWithoutDevice reports whether doc selects a person who decides
// their own requests and has no enrolled device: under a confirmation every
// person, and under the sponsor route a person who has no sponsor.
func (c *check) ownDecidersWithoutDevice(doc policy.Document, confirm bool) bool {
	users := sortedKeys(c.hA.users)
	if m := doc.Spec.Match; len(m.Roles) > 0 {
		users = nil
		for _, r := range m.Roles {
			users = append(users, c.hA.byRole[r]...)
		}
	}
	return slices.ContainsFunc(users, func(name string) bool {
		u := c.hA.users[name]
		return !u.Agent && u.Devices == 0 && (confirm || u.Sponsor == "") && appliesTo(doc.Spec.Match, subjectOf(u, c.hA.roles[name]))
	})
}

// itemWarnings answers the warnings of one item: the readiness of a server
// or role it writes, and the file a removed server came from.
func (c *check) itemWarnings(it Item) []Finding {
	object := it.Object()
	switch {
	case it.Kind == KindApp && it.Op == OpPut:
		return c.appWarnings(it.Name)
	case it.Kind == KindApp && it.Op == OpRemove:
		if live := c.w.Apps[it.Name]; live.File != "" {
			return []Finding{warning(codeFileLinked, object, fmt.Sprintf(
				"%s came from the file %s. Removing it here leaves the file in place, and Straza proposes the server again only when the file changes.", it.Name, live.File), "")}
		}
	case it.Kind == KindRole && it.Op == OpPut:
		return c.roleWarnings(it.Name)
	}
	return nil
}

// lossy answers revert.lossy for each object an undo re-creates: a put
// with no base in a draft that reverts another re-creates what
// that draft removed, and a server comes back without its secrets, the
// connections and a pause, and a role without its memberships.
func (c *check) lossy() []Finding {
	if c.d.Reverts == "" {
		return nil
	}
	var out []Finding
	for _, it := range c.d.Items {
		if it.Op != OpPut || it.Base != "" {
			continue
		}
		switch it.Kind {
		case KindApp:
			out = append(out, warning(codeRevertLossy, it.Object(), fmt.Sprintf(
				"Undoing draft %s re-creates %s, and does not bring back its stored secrets, each person's connection or a pause.", c.d.Reverts, it.Name),
				fmt.Sprintf("After publishing, store its secrets again with strazactl apps secret set %s, and each person runs straza connect %s.", it.Name, it.Name)))
		case KindRole:
			out = append(out, warning(codeRevertLossy, it.Object(), fmt.Sprintf("Undoing draft %s re-creates %s, and does not bring back its memberships.", c.d.Reverts, it.Name),
				fmt.Sprintf("Assign %s again in your identity manager, or with strazactl assign %s --user <name>.", it.Name, it.Name)))
		}
	}
	return out
}

// appWarnings answers the readiness of the server name after the draft
// puts it.
func (c *check) appWarnings(name string) []Finding {
	next, object := c.after.Apps[name], "App/"+name
	var out []Finding
	if next.Credential == credentialStatic && !next.SharedSecret {
		out = append(out, warning(codeSecretMissing, object, fmt.Sprintf("%s needs a static secret and none is stored yet, so every call fails until one is.", name),
			fmt.Sprintf("After publishing, run strazactl apps secret set %s.", name)))
	}
	out = append(out, c.unconnected(name)...)
	if next.Runtime == runtimeOCI && !c.w.DockerOnPath {
		out = append(out, warning(codeNoDocker, object, fmt.Sprintf("%s runs as a container and this host has no docker, so it will not start.", name),
			"Install docker on the Straza host, or run the server remotely."))
	}
	if next.Runtime == runtimeRemote && next.RegistryURL != "" && strings.TrimSuffix(next.RegistryURL, "/") != strings.TrimSuffix(next.URL, "/") {
		out = append(out, warning(codeRegistryAddress, object, fmt.Sprintf("The address of %s differs from the one in the registry record its server block copies.", name),
			"Check which address the server's publisher gives."))
	}
	return append(out, unoffered(object, "The exposure of "+name, name, next.Exposure, next.Offered)...)
}

// roleWarnings answers the readiness of the role name after the draft puts
// it: tools its row names that its server does not offer, and a server of
// callers' own credentials nobody has connected to.
func (c *check) roleWarnings(name string) []Finding {
	acc, ok := c.after.Access[name]
	if !ok || acc.Server == "" {
		return nil
	}
	out := unoffered("Role/"+name, name, acc.Server, acc.Tools, c.after.Apps[acc.Server].Offered)
	return append(out, c.unconnected(acc.Server)...)
}

// unconnected is ready.nobody-connected for the server name when it runs
// on each caller's own credential and nobody has connected.
func (c *check) unconnected(name string) []Finding {
	app := c.after.Apps[name]
	what := map[string]string{CredentialToken: "token", CredentialOAuth: "sign-in"}[app.Credential]
	if what == "" || app.UserCredentials > 0 {
		return nil
	}
	return []Finding{warning(codeNobodyConnected, "App/"+name, fmt.Sprintf("%s runs on each caller's own %s, and nobody who holds its roles has connected yet.", name, what),
		fmt.Sprintf("Each person runs straza connect %s after publishing.", name))}
}

// unoffered answers ready.tool-unknown for each tool of names, written as
// a literal name, that the server does not offer, when its list was read.
// who begins the sentence, a role or the exposure of a server.
func unoffered(object, who, server string, names, offered []string) []Finding {
	var out []Finding
	for _, t := range names {
		if offered != nil && !strings.Contains(t, "*") && !slices.Contains(offered, t) {
			out = append(out, warning(codeToolUnknown, object, fmt.Sprintf("%s names %s, which %s does not offer.", who, t, server),
				fmt.Sprintf("Fix the name, or check the list with strazactl apps tools %s.", server)))
		}
	}
	return out
}

// gainWarnings answers the warnings of who gains: the tools holders of a
// role lose, a set a removed role leaves matching nothing, and a tool
// newly reached or newly ungated whose name says it destroys something.
func (c *check) gainWarnings(rows []gainRow) []Finding {
	var out []Finding
	var keys []toolKey
	lost := map[toolKey][]string{}
	for _, row := range rows {
		b, a := row.b, row.a
		if row.HolderCount > 0 && (b.outcome == OutcomeRuns || b.outcome == OutcomeApproval) && outcomeRank[a.outcome] == 0 {
			k := toolKey{role: row.Role, server: row.Server}
			if _, ok := lost[k]; !ok {
				keys = append(keys, k)
			}
			lost[k] = append(lost[k], row.Tool)
		}
		reached := (a.outcome == OutcomeRuns || a.outcome == OutcomeApproval) && outcomeRank[b.outcome] == 0
		if row.Server != nativeApp && (reached || (a.outcome == OutcomeRuns && b.outcome == OutcomeApproval)) && !slices.Contains(c.after.Apps[row.Server].ReadOnly, row.Tool) {
			if verb := destructiveVerb(row.Tool); verb != "" {
				out = append(out, warning(codeDestructive, "App/"+row.Server,
					fmt.Sprintf("%s on %s does not say it is read-only, and its name says it %s.", row.Tool, row.Server, verb), "Gate it with an approval rule, or leave it out of the role."))
			}
		}
	}
	for _, k := range keys {
		out = append(out, warning(codeRoleLoses, "Role/"+k.role, fmt.Sprintf("Holders of %s lose %s on %s.", k.role, listWords(slices.Compact(lost[k]), "and"), k.server), ""))
	}
	for _, r := range sortedKeys(c.w.Roles) {
		set := r + "-access"
		doc, ok := c.docsA[set]
		if _, kept := c.after.Roles[r]; kept || !ok || !slices.Contains(doc.Spec.Match.Roles, r) ||
			slices.ContainsFunc(doc.Spec.Match.Roles, func(x string) bool { _, ok := c.after.Roles[x]; return ok }) {
			continue
		}
		out = append(out, warning(codeSetOrphaned, "Role/"+r, fmt.Sprintf("Removing %s leaves %s matching nothing.", r, set),
			fmt.Sprintf("Add a Removal of %s to this draft, or turn it off with strazactl policy deactivate %s after publishing.", set, set)))
	}
	return out
}

// destructiveVerb answers the verb a tool name says it destroys with, or
// "" when no word of the name does.
func destructiveVerb(tool string) string {
	for _, w := range nameWords(tool) {
		if verb, ok := destructiveVerbs[w]; ok {
			return verb
		}
	}
	return ""
}

// nameWords splits a tool name into its words, lower case, at every
// character that is not a letter or a digit and where camelCase starts a
// new word.
func nameWords(name string) []string {
	split := secretCamelRe.ReplaceAllString(name, "${1}_${2}")
	return strings.FieldsFunc(strings.ToLower(split), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
