package drafts

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/strazahq/straza/internal/policy"
)

// The risks of a rule that opens a tool on a person's own machine: a
// harness tool Straza does not classify, read by its name, and an MCP
// server a harness runs itself, which Straza has not registered.

// toolKindWords names each local tool kind but other in plain words.
var toolKindWords = map[string]string{
	policy.ToolShellExec: "shell commands", policy.ToolFileRead: "file reads", policy.ToolFileWrite: "file writes",
	policy.ToolFileEdit: "file edits", policy.ToolNetFetch: "network fetches", policy.ToolTaskSpawn: "subagents",
}

// localWhat words the local tool kinds a rule opens: every says all of
// them, each other kind reads in plain words, a rule that names harness
// tools reads as those tools, and the kind other alone reads as every
// harness tool Straza does not classify.
func localWhat(r policy.Rule, kinds []string, every string) string {
	if len(kinds) == len(localTools) {
		return every
	}
	words := slices.Clone(kinds)
	for i, k := range kinds {
		switch names := allowNames(r); {
		case k != policy.ToolOther:
			words[i] = cmp.Or(toolKindWords[k], k)
		case len(kinds) == 1 && len(names) == 1:
			words[i] = "the harness tool " + names[0]
		case len(kinds) == 1 && len(names) > 1:
			words[i] = "the harness tools " + listWords(names, "and")
		default:
			words[i] = "every unclassified harness tool"
		}
	}
	return listWords(words, "and")
}

// allowNames answers the tool names on the allow side of r.
func allowNames(r policy.Rule) []string {
	if r.ToolNames == nil {
		return nil
	}
	return r.ToolNames.Allow
}

// serverAllow answers policy.local-allow for each rule of a set the draft
// writes that allows the tools of an MCP server Straza has not registered,
// by its name or by naming no server at all, under the conditions of
// localAllow. It holds on every profile, because the default that denies
// such a server does not follow localToolDefault.
func serverAllow(name string, b policy.Document, hadB bool, a policy.Document, apps map[string]App) []Finding {
	var out []Finding
	wider := hadB && matchWidens(b.Spec.Match, a.Spec.Match)
	for _, ra := range a.Spec.Rules {
		if gateMode(ra) || !canAllow(ra) || !blocks(ra) {
			continue
		}
		if i := ruleIndex(b, ra.ID); hadB && !wider && i >= 0 && sameRule(b.Spec.Rules[i], ra) {
			continue
		}
		servers := unregistered(ra, apps)
		if governsMCP(ra) && len(ra.Apps) == 0 {
			servers = []string{""}
		}
		for _, s := range servers {
			out = append(out, ruleNamed(risk(codeLocalAllow, "PolicySet/"+name, "", unregisteredWords(name, ra, s, ""), "", ra.ID+": "+cmp.Or(s, "every MCP server")), ra, s))
		}
	}
	return out
}

// ruleNamed is f with the rule r, the server s it names, empty for every
// server or none, and the tool names r allows, each spelled with visible.
func ruleNamed(f Finding, r policy.Rule, s string) Finding {
	f.Rule, f.Server = visible(r.ID), visible(s)
	for _, t := range allowNames(r) {
		f.Tools = append(f.Tools, visible(t))
	}
	return f
}

// registered answers the servers a rule may name without opening one that
// a harness runs itself: the servers after the draft, and for an agent's
// draft only those that were registered before it too. The hook lane
// decides a harness-run server by its name alone, so an agent could
// otherwise register the name it wants opened in the same draft.
func (c *check) registered() map[string]App {
	if !agentDraft(c.d, c.in.Proposer) {
		return c.after.Apps
	}
	out := make(map[string]App, len(c.after.Apps))
	for name, app := range c.after.Apps {
		if _, ok := c.w.Apps[name]; ok {
			out[name] = app
		}
	}
	return out
}

// unregisteredWords is the sentence of the risk that rule r of set opens
// the MCP server s, which Straza has not registered, or every such server
// when s is empty. once says what lets a gate's call run, and is empty for
// a rule that allows.
func unregisteredWords(set string, r policy.Rule, s, once string) string {
	tools := "every tool"
	if names := allowNames(r); len(names) > 0 {
		tools = listWords(names, "and")
	}
	switch {
	case s == "" && once == "":
		return fmt.Sprintf("Rule %s of %s allows %s of every MCP server, including the servers Straza has not registered and denies by default.", r.ID, set, tools)
	case s == "":
		return fmt.Sprintf("Rule %s of %s lets %s of every MCP server run once %s, including the servers Straza has not registered and denies by default.", r.ID, set, tools, once)
	case once == "":
		return fmt.Sprintf("Rule %s of %s allows %s of %s, which Straza has not registered and denies by default.", r.ID, set, tools, s)
	}
	return fmt.Sprintf("Rule %s of %s lets %s of %s run once %s, although Straza has not registered %s and denies its tools by default.", r.ID, set, tools, s, once, s)
}
