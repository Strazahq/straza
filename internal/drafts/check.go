package drafts

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// The standing each kind of item needs from its publisher, in the words of
// today's direct routes.
const (
	needApps     = "the scope apps:write or the role " + MCPAdminRole
	needIdentity = "the scope identity:write"
	needRow      = needIdentity + ", and the scope apps:write or the role " + MCPAdminRole + " for its access row"
	needPolicy   = "the scope policy:write"
)

// CheckInput is what Check needs beside the World and the draft. Apps
// holds the facts of every App the draft puts, each with its manifest as
// canonical JSON in Manifest. Contacted holds, by Item.Object(), the tool
// names a person's Contact read for the item's current document. Changed
// holds, by Item.Object(), the last publish of each item that went stale.
// Proposer is the user who wrote the draft's first revision, as its user
// row reads. Advisories is the server's validate advisories, events
// coverage included, because that matrix lives in internal/server, and
// policy.Advisories stands in when it is nil. Now stamps the verdict.
type CheckInput struct {
	Apps       map[string]App
	Contacted  map[string][]string
	Changed    map[string]LastChange
	Proposer   Holder
	Advisories func(policy.Document) []policy.Advisory
	Now        time.Time
	// RefusalsOnly stops Check after its refusals, steps 1 to 4, with no
	// who-gains table, risks, warnings or notes. A direct route sets
	// it while admin.secondPerson is off, because nothing reads its risks
	// then, and who gains at 10,000 users would hold the config mutex on
	// every write. An agent's draft still reads who gains for the roles the
	// agent holds, for agent.own-reach. The zero value runs every step.
	RefusalsOnly bool
}

// check is one run of Check: its inputs, the World after the draft, the
// sets of each world parsed, the implication closures and holders of each
// world, the two policy engines once step 4 compiled them, the prober of
// steps 5 to 7, the loosenings once read, and the verdict so far.
type check struct {
	w, after                  World
	d                         Draft
	in                        CheckInput
	docsW, docsA              map[string]policy.Document
	clW, clA                  *closures
	hW, hA                    holding
	beforeEngine, afterEngine *policy.Engine
	g                         *gainer
	loose                     []loosening
	looseDone                 bool
	v                         Verdict
}

// Check reads the current revision of d against w and answers its verdict.
// It reads no store, opens no connection and runs no Rego module, live or
// new. Every item must be stamped: its Base is the fingerprint of its
// object when it entered the draft, so a base that is not the live
// fingerprint refuses as stale. A step that refuses ends the reading, so a
// refused draft gets no who-gains table, and the needs and the risk digest
// are answered whatever the steps found. Every list of the verdict is empty
// rather than nil, and each sorts by object, then code. in.RefusalsOnly
// stops the reading after the refusals.
func Check(w World, d Draft, in CheckInput) Verdict {
	c := &check{w: w, d: d, in: in, v: Verdict{
		Draft: d.ID, Revision: d.Revision, Snapshot: w.SnapshotID, CheckedAt: in.Now.UTC().Format(time.RFC3339),
		Refused: []Finding{}, Risks: []Finding{}, Warnings: []Finding{}, Unchecked: []Finding{},
		Passed: []Finding{}, Info: []Finding{}, Gains: []Gain{},
	}}
	c.run()
	c.v.Needs = needs(w, c.after, d, in.Apps)
	for _, list := range [][]Finding{c.v.Refused, c.v.Risks, c.v.Warnings, c.v.Unchecked, c.v.Passed, c.v.Info} {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Object != list[j].Object {
				return list[i].Object < list[j].Object
			}
			return list[i].Code < list[j].Code
		})
	}
	c.v.RiskDigest = riskDigest(c.v.Risks)
	return c.v
}

// codeFileRefused is the refusal of a file of the apps directory that did
// not become items: it does not parse, names a provider this server lacks,
// or holds a secret.
const codeFileRefused = "file.refused"

// fileRefusal answers file.refused for a draft of the apps directory whose
// file became no items, with the sentence its door stored and, after "Fix
// the file.", the fix stored on the line after it. Such a draft never
// publishes.
func fileRefusal(d Draft) []Finding {
	if d.Door != DoorAppsDir || d.Refusal == "" {
		return nil
	}
	sentence, fix, _ := strings.Cut(d.Refusal, "\n")
	fixes := "Fix the file."
	if fix != "" {
		fixes += " " + visible(strings.TrimSuffix(fix, ".")) + "."
	}
	return []Finding{refusal(codeFileRefused, "",
		fmt.Sprintf("%s does not read as an MCP server manifest: %s.", visible(filepath.Base(d.Source)), visible(strings.TrimSuffix(sentence, "."))),
		fixes+" Straza proposes it again once it is saved.")}
}

// run takes the steps of a check in order and stops after the first that
// refuses.
func (c *check) run() {
	// The world after the draft is laid before the first refusal, so the
	// needs of a stale draft read the same rows as any other.
	c.after = c.w.Overlay(c.d, c.appFacts())
	if c.refuse(fileRefusal(c.d)) || c.refuse(staleFindings(c.w, c.d, c.in.Changed)) {
		return
	}
	var errW, errA error
	c.docsW, errW = parseSets(c.w.Policies, nil, nil)
	c.docsA, errA = parseSets(c.after.Policies, c.w.Policies, c.docsW)
	c.clW, c.clA = newClosures(c.w.Implies), newClosures(c.after.Implies)
	fs := checkRefusals(c.w, c.after, c.docsA, c.d, c.in.Apps)
	agent := agentDraft(c.d, c.in.Proposer)
	if agent {
		fs = append(fs, agentRefusals(c.w, c.after, c.docsW, c.docsA, c.d, c.in.Proposer)...)
		guard, _ := c.guardrail()
		fs = append(fs, guard...)
	}
	if c.refuse(dedupe(fs)) {
		return
	}
	if c.refuse(c.compile(errW, errA)) {
		return
	}
	c.signals(agent)
}

// signals takes steps 5 to 7: the refusal of an agent's draft that widens
// the agent's own reach, read over the agent's whole subject, then who
// gains, with every Rego module left out, the risks, and the
// warnings, unchecked, passed and info lines. With RefusalsOnly it stops
// after the agent's own reach.
func (c *check) signals(agent bool) {
	if agent && c.refuse(c.ownReach()) || c.in.RefusalsOnly {
		return
	}
	c.hW = holdingOf(c.w, c.clW)
	c.hA = c.hW
	// Only a Role item or a server's removal changes the edges or the
	// holders Overlay leaves, so any other draft holds the same roles for
	// every user after publishing.
	if slices.ContainsFunc(c.d.Items, func(it Item) bool { return it.Kind == KindRole || (it.Kind == KindApp && it.Op == OpRemove) }) {
		c.hA = holdingOf(c.after, c.clA)
	}
	rows := c.whoGains()
	for _, row := range rows {
		c.v.Gains = append(c.v.Gains, row.Gain)
	}
	var scanned []Finding
	for _, it := range c.d.Items {
		scanned = append(scanned, ScanSecrets(it)...)
	}
	c.v.Risks = append(c.v.Risks, c.risks(rows)...)
	c.v.Warnings = append(c.v.Warnings, c.warnings(rows, scanned)...)
	c.notes(len(scanned) == 0)
}

// newModules answers, sorted, the sets whose Rego module the draft adds or
// changes.
func (c *check) newModules() []string {
	var out []string
	for _, name := range sortedKeys(c.docsA) {
		a := c.docsA[name]
		b, had := c.docsW[name]
		if a.Spec.Escape != nil && (!had || b.Spec.Escape == nil || b.Spec.Escape.Rego != a.Spec.Escape.Rego) {
			out = append(out, name)
		}
	}
	return out
}

// refuse adds fs to the refusals and reports whether there were any.
func (c *check) refuse(fs []Finding) bool {
	c.v.Refused = append(c.v.Refused, fs...)
	return len(fs) > 0
}

// appFacts is the facts of every App the draft puts, each offering the
// tools a Contact read for its document, so Overlay takes them where the
// live list no longer describes the server.
func (c *check) appFacts() map[string]App {
	apps := make(map[string]App, len(c.in.Apps))
	for name, app := range c.in.Apps {
		app.Offered, app.ReadOnly = slices.Clone(c.in.Contacted[string(KindApp)+"/"+name]), nil
		apps[name] = app
	}
	return apps
}

// compile builds the policy engine of live state and the one of the state
// after the draft, both with the deployment's local tool default. errW and
// errA are the first parse errors of each world's sets. A live policy that
// does not compile fails the check closed, and a draft that leaves a
// policy that does not compile is refused.
func (c *check) compile(errW, errA error) []Finding {
	var err error
	if c.beforeEngine, err = engineOf(c.docsW, errW, c.w.LocalToolDefault); err != nil {
		return []Finding{refusal(codePolicyCompile, "",
			fmt.Sprintf("Straza could not compile the live policy to check the draft: %v.", err),
			"Check the draft again, and read the strazad log if it keeps failing.")}
	}
	if c.afterEngine, err = engineOf(c.docsA, errA, c.w.LocalToolDefault); err != nil {
		return []Finding{refusal(codePolicyCompile, "",
			fmt.Sprintf("The policy does not compile with this draft: %v.", err), "Fix the set the error names.")}
	}
	return nil
}

// engineOf compiles docs, by name, into one engine, or answers parseErr, a
// set's parse error, when it is set.
func engineOf(docs map[string]policy.Document, parseErr error, localDefault string) (*policy.Engine, error) {
	if parseErr != nil {
		return nil, parseErr
	}
	names := sortedKeys(docs)
	list := make([]policy.Document, 0, len(names))
	for _, name := range names {
		list = append(list, docs[name])
	}
	return policy.NewEngine(list, localDefault)
}

// agentDraft reports whether the agent rules hold for d: it came through the
// straza-app door, its proposer is not a person, or a user who is not a
// person wrote any revision of it. A draft keeps them once an agent wrote
// it, because the draft does not say who wrote which revision.
func agentDraft(d Draft, proposer Holder) bool {
	return d.Door == DoorAgent || proposer.Agent || slices.ContainsFunc(d.Authors, func(p Principal) bool { return p.Agent })
}

// needs answers the standing each item of d needs from its publisher, in
// item order, the same for every viewer. apps holds the facts of every App
// d puts.
func needs(w, after World, d Draft, apps map[string]App) []Need {
	out := make([]Need, 0, len(d.Items))
	for _, it := range d.Items {
		out = append(out, Need{Object: it.Object(), Standing: standingNeeded(w, after, it, apps)})
	}
	return out
}

// standingNeeded words the standing item it needs, what today's direct
// routes need for the same change. A new server, a change under
// straza.runtime and a switch to client credentials need an area grant or
// the MCP admin role, as does any App change whose facts the server did
// not hand in. Another App change may also be made by the server's admin
// role. A global role, a role whose server is gone included, needs
// identity:write; a change of its access row alone needs what the access
// row routes need, and together with any other change both. A role a
// server owns follows its server's routes: identity:write, apps:write, the
// MCP admin or the server's admin role create or describe it, and a change
// of its row or its removal needs the server's side while nobody holds it,
// or identity:write for a removal.
func standingNeeded(w, after World, it Item, apps map[string]App) string {
	switch it.Kind {
	case KindApp:
		live, ok := w.Apps[it.Name]
		next, known := apps[it.Name]
		if it.Op == OpRemove || !ok || !known || live.AdminRole == "" || runtimeChanged(live, next) || agentTokensChanged(live, next) || viewsChanged(live, next) {
			return needApps
		}
		return "the scope apps:write, the role " + MCPAdminRole + ", or the server's admin role " + live.AdminRole
	case KindRole:
		owner := ""
		live, existed := w.Roles[it.Name]
		if existed {
			owner = live.Owner
		} else if doc, read := after.roleDocs[it.Name]; read {
			owner = doc.Spec.Server
		} else if doc, err := ParseRole(it.Doc); err == nil {
			owner = doc.Spec.Server
		}
		admin := ""
		if owner != "" {
			admin = cmp.Or(w.Apps[owner].AdminRole, apps[owner].AdminRole)
		}
		row := it.Op == OpPut && rowChanged(w, after, it.Name)
		switch {
		case admin == "" && row && existed && live.Description == after.Roles[it.Name].Description &&
			slices.Equal(sortedCopy(w.Implies[it.Name]), sortedCopy(after.Implies[it.Name])):
			return needApps
		case admin == "" && row:
			return needRow
		case admin == "":
			return needIdentity
		case it.Op == OpRemove:
			return "the scope identity:write, or while nobody holds the role the scope apps:write, the role " + MCPAdminRole + " or " + admin
		case existed && row:
			return "the scope apps:write, the role " + MCPAdminRole + ", or " + admin + " while nobody holds the role"
		}
		return "the scope identity:write or apps:write, the role " + MCPAdminRole + ", or " + admin
	}
	return needPolicy
}

// runtimeChanged reports whether next changes anything under straza.runtime
// of live, reading both manifests as JSON. A manifest that does not read
// counts as changed, so the higher standing is asked.
func runtimeChanged(live, next App) bool {
	a, okA := runtimeOf(live.Manifest)
	b, okB := runtimeOf(next.Manifest)
	return !okA || !okB || !reflect.DeepEqual(a, b)
}

// runtimeOf answers the straza.runtime block of a manifest in JSON.
func runtimeOf(manifest string) (any, bool) {
	var m struct {
		Straza struct {
			Runtime any `json:"runtime"`
		} `json:"straza"`
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil || m.Straza.Runtime == nil {
		return nil, false
	}
	return m.Straza.Runtime, true
}

// viewsChanged reports whether next turns straza.exposure.views of live on
// or off, reading both manifests as JSON as runtimeChanged does. A manifest
// that does not read counts as changed, so the higher standing is asked.
func viewsChanged(live, next App) bool {
	a, okA := viewsOf(live.Manifest)
	b, okB := viewsOf(next.Manifest)
	return !okA || !okB || a != b
}

// viewsOf answers straza.exposure.views of a manifest in JSON, false when
// the key is absent.
func viewsOf(manifest string) (views, ok bool) {
	var m struct {
		Straza struct {
			Exposure struct {
				Views bool `json:"views"`
			} `json:"exposure"`
		} `json:"straza"`
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		return false, false
	}
	return m.Straza.Exposure.Views, true
}

// agentTokensChanged reports whether next switches the server to
// credential.agents client_credentials, or changes the provider of a
// server that uses it, as the install route's server-admin rule reads it.
func agentTokensChanged(live, next App) bool {
	if next.Agents != credentialClientCredentials {
		return false
	}
	return live.Agents != credentialClientCredentials || live.Provider != next.Provider
}

// riskDigest is the hex sha256 of the sorted keys of risks joined by
// newlines, which a publish carries back to show which risks the person
// reviewed.
func riskDigest(risks []Finding) string {
	keys := make([]string, len(risks))
	for i, r := range risks {
		keys[i] = r.Key()
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}
