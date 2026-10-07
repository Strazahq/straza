package server

import (
	"fmt"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/drafts"
)

// builtinApp is the name the gateway serves Straza's own tools under, as
// who gains names the built-in straza app.
const builtinApp = "straza"

// The words verdictFor puts in a line that names a server its reader may
// not read, cutPart for the line's before and after words and cutLine for
// its sentence, and in a line that names a role or a policy set its reader
// may not read, objectPart and objectLine.
const (
	cutPart    = "a tool on a server you cannot read"
	cutLine    = "This line names " + cutPart + ", so this view leaves its words out. Ask an administrator for the scope apps:read to see it."
	objectPart = "an object you cannot read"
	objectLine = "This line names " + objectPart + ", so this view leaves its words out. Ask an administrator for the grant that reads it."
)

// findingPayload is a finding as a route answers it. Key is the finding's
// Finding.Key from before verdictFor cut its words. A publish acknowledges
// a risk by that key, so a client echoes it and never computes it from the
// words it reads.
type findingPayload struct {
	drafts.Finding
	Key string `json:"key"`
}

// verdictPayload is a verdict as a route answers it: the fields of
// drafts.Verdict, cut for the reader by verdictFor, every finding keyed.
type verdictPayload struct {
	Draft      string           `json:"draft"`
	Revision   int              `json:"revision"`
	Snapshot   string           `json:"snapshot"`
	CheckedAt  string           `json:"checked_at"`
	Refused    []findingPayload `json:"refused"`
	Risks      []findingPayload `json:"risks"`
	Warnings   []findingPayload `json:"warnings"`
	Unchecked  []findingPayload `json:"unchecked"`
	Passed     []findingPayload `json:"passed"`
	Info       []findingPayload `json:"info"`
	Gains      []drafts.Gain    `json:"gains"`
	Needs      []drafts.Need    `json:"needs"`
	RiskDigest string           `json:"risk_digest"`
}

// agentNameCodes are the warnings an agent reads whole on a draft whose
// every revision it wrote, because they name the server, tool or role it
// must fix: ready.tool-unknown and ready.rule-names-nothing.
var agentNameCodes = map[string]bool{"ready.tool-unknown": true, "ready.rule-names-nothing": true}

// verdictFor is v, the verdict of draft d over live state w, as the caller
// c may read it: the one cut every route that answers a verdict makes.
// Every finding keeps the key it had before the cut. An
// agent reads the refusals, risks, warnings and unchecked lines ForAgent
// keeps, and nothing else, with its lines cut as a person's are but for the
// lines of agentNameCodes on a draft whose every revision it wrote. Anyone
// else reads v with three cuts. A gain row on a server the caller may not
// read, without the scope apps:read or the server's admin role, or of a role
// that unreadObjects finds, is left out, and one info line counts the rows
// left out. The built-in straza app's rows pass the server rule, because
// its tools are the product's own. A risk, warning, unchecked or info line
// that names a server the caller may not read, or a role or set that
// unreadObjects finds, loses its words, as cutFinding says. Holders are
// named only for root and a holder of identity:read, and counted for everyone.
func verdictFor(c draftCaller, w drafts.World, d drafts.Draft, v drafts.Verdict) verdictPayload {
	unread, objects := c.unreadServers(w, d), c.unreadObjects(w, d, v)
	if !c.adminAPI() && !c.person {
		av := v.ForAgent()
		own := len(d.Authors) > 0 && !slices.ContainsFunc(d.Authors, func(p drafts.Principal) bool { return !c.isAuthor(p.UserID, p.Via) })
		warnings := keyed(av.Warnings, unread, objects)
		for i, f := range av.Warnings {
			if own && agentNameCodes[f.Code] {
				warnings[i].Finding = f
			}
		}
		return verdictPayload{Draft: v.Draft, Revision: v.Revision, Snapshot: v.Snapshot, CheckedAt: v.CheckedAt,
			Refused: keyed(av.Refused, nil, nil), Risks: keyed(av.Risks, unread, objects), Warnings: warnings, Unchecked: keyed(av.Unchecked, unread, objects),
			Passed: []findingPayload{}, Info: []findingPayload{}, Gains: []drafts.Gain{}, Needs: []drafts.Need{}}
	}
	g := c.p.scope.Grants
	names, everyServer := c.p.root || g["identity:read"], c.p.root || g["apps:read"]
	gains := make([]drafts.Gain, 0, len(v.Gains))
	hidden, byRole := 0, false
	for _, gain := range v.Gains {
		role := slices.Contains(objects, gain.Role)
		if role || !everyServer && gain.Server != builtinApp && !c.holdsAdminRoleOf(gain.Server) {
			hidden, byRole = hidden+1, byRole || role
			continue
		}
		if !names {
			gain.Holders = nil
		}
		gains = append(gains, gain)
	}
	info := keyed(v.Info, unread, objects)
	if hidden > 0 {
		sentence := fmt.Sprintf("%d rows of who gains what are on servers you cannot read, so this view leaves them out. "+
			"Ask an administrator for the scope apps:read to see them.", hidden)
		switch {
		case byRole && hidden == 1:
			sentence = "1 row of who gains what names a role or a server you cannot read, so this view leaves it out. " +
				"Ask an administrator for the grant that reads it."
		case byRole:
			sentence = fmt.Sprintf("%d rows of who gains what name roles or servers you cannot read, so this view leaves them out. "+
				"Ask an administrator for the grants that read them.", hidden)
		case hidden == 1:
			sentence = "1 row of who gains what is on a server you cannot read, so this view leaves it out. " +
				"Ask an administrator for the scope apps:read to see it."
		}
		// The line names no object, so it sorts first, as Check sorts by
		// object and then by code.
		info = append(keyed([]drafts.Finding{{Code: "info.gains-hidden", Class: drafts.ClassInfo, Sentence: sentence}}, nil, nil), info...)
	}
	return verdictPayload{Draft: v.Draft, Revision: v.Revision, Snapshot: v.Snapshot, CheckedAt: v.CheckedAt,
		Refused: keyed(v.Refused, nil, nil), Risks: keyed(v.Risks, unread, objects), Warnings: keyed(v.Warnings, unread, objects),
		Unchecked: keyed(v.Unchecked, unread, objects), Passed: keyed(v.Passed, nil, nil), Info: info,
		Gains: gains, Needs: v.Needs, RiskDigest: v.RiskDigest}
}

// unreadServers answers the servers of live state w and of draft d that c
// may not read: none for root and a holder of apps:read, and for anyone
// else every server but those whose admin role c holds.
func (c draftCaller) unreadServers(w drafts.World, d drafts.Draft) []string {
	if c.p.root || c.p.scope.Grants["apps:read"] {
		return nil
	}
	var out []string
	for name := range w.Apps {
		if !c.holdsAdminRoleOf(name) {
			out = append(out, name)
		}
	}
	for _, it := range d.Items {
		if it.Kind == drafts.KindApp && !c.holdsAdminRoleOf(it.Name) {
			out = append(out, it.Name)
		}
	}
	return out
}

// unreadObjects answers the names of the roles and policy sets that c may
// not read, by the rule readRefusal applies, among those the verdict
// v of draft d over live state w touches: the items of d and the objects
// its removals take with them, the object of each line, a live role or set
// that a line's before, after or typed words name as a whole name, and the
// role of each gain row. It answers none for root and a holder of
// drafts:read. A live role or set the verdict does not touch is left out,
// so a role named with a common word, such as person, cuts no line whose
// prose holds that word.
func (c draftCaller) unreadObjects(w drafts.World, d drafts.Draft, v drafts.Verdict) []string {
	if c.p.root || c.p.scope.Grants["drafts:read"] {
		return nil
	}
	objects := append(slices.Clone(d.Items), drafts.Implied(w, d)...)
	var words []string
	for _, fs := range [][]drafts.Finding{v.Risks, v.Warnings, v.Unchecked, v.Info} {
		for _, f := range fs {
			kind, name, _ := strings.Cut(f.Object, "/")
			objects = append(objects, drafts.Item{Kind: drafts.Kind(kind), Name: name})
			words = append(words, f.Before, f.After, f.Typed)
		}
	}
	for _, g := range v.Gains {
		objects = append(objects, drafts.Item{Kind: drafts.KindRole, Name: g.Role})
	}
	named := strings.Join(words, "\n")
	for name := range w.Roles {
		if namesServer(named, []string{name}) {
			objects = append(objects, drafts.Item{Kind: drafts.KindRole, Name: name})
		}
	}
	for name := range w.Policies {
		if namesServer(named, []string{name}) {
			objects = append(objects, drafts.Item{Kind: drafts.KindPolicySet, Name: name})
		}
	}
	// A draft's item comes first, so a role live state lacks is read with
	// the owner its document names.
	seen := map[string]bool{}
	var out []string
	for _, it := range objects {
		if it.Kind != drafts.KindRole && it.Kind != drafts.KindPolicySet || it.Name == "" || seen[it.Object()] {
			continue
		}
		seen[it.Object()] = true
		if !c.readsObject(w, it) {
			out = append(out, it.Name)
		}
	}
	return out
}

// keyed answers fs as a route answers them to a reader who may not read
// the servers unread nor the roles and sets hidden: each finding cut by
// cutFinding and keyed by its Key from before the cut.
func keyed(fs []drafts.Finding, unread, hidden []string) []findingPayload {
	out := make([]findingPayload, len(fs))
	for i, f := range fs {
		out[i] = findingPayload{Finding: cutFinding(f, unread, hidden), Key: f.Key()}
	}
	return out
}

// cutFinding is f as a reader who may not read the servers unread nor the
// roles and sets hidden reads it. A line that names one of them anywhere
// keeps its code, class and ack, and its object and typed text unless they
// name one. An App line loses both, because its typed text is that
// server's name, host or provider. Its sentence reads cutLine, its before
// and after words read cutPart where it has them, and it carries no fix.
// A line that names no such server but such a role or set reads objectLine
// and objectPart instead. Every line that names a tool names the tool's
// server too, so the servers find the tools. A typed risk
// without its typed text cannot be acknowledged, so only a reader who may
// read what it names publishes it.
func cutFinding(f drafts.Finding, unread, hidden []string) drafts.Finding {
	texts := []string{f.Object, f.Sentence, f.Fix, f.Before, f.After, f.Typed}
	line, part := cutLine, cutPart
	if !slices.ContainsFunc(texts, func(s string) bool { return namesServer(s, unread) }) {
		if !slices.ContainsFunc(texts, func(s string) bool { return namesServer(s, hidden) }) {
			return f
		}
		line, part = objectLine, objectPart
	}
	names := func(s string) bool { return namesServer(s, unread) || namesServer(s, hidden) }
	if names(f.Object) {
		f.Object, f.Typed = "", ""
	}
	if names(f.Typed) {
		f.Typed = ""
	}
	f.Sentence, f.Fix = line, ""
	if f.Before != "" {
		f.Before = part
	}
	if f.After != "" {
		f.After = part
	}
	return f
}

// namesServer reports whether text names one of servers as a whole name,
// never as a part of a longer name, such as the role jira-writers or the
// tool jira_search for the server jira. It finds the names of roles and
// policy sets the same way.
func namesServer(text string, servers []string) bool {
	for _, s := range servers {
		for at := 0; s != ""; {
			i := strings.Index(text[at:], s)
			if i < 0 {
				break
			}
			i += at
			if !nameByte(text, i-1) && !nameByte(text, i+len(s)) {
				return true
			}
			at = i + 1
		}
	}
	return false
}

// nameByte reports whether text holds at i a byte that goes on with a
// name: a letter, a digit, a hyphen or an underscore.
func nameByte(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return false
	}
	b := text[i]
	return b == '-' || b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}
