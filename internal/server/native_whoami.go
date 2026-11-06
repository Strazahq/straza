package server

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// This file holds the built-in straza app's whoami tool, which tells the
// calling session who it acts as and what it may do. It reads the session's
// cache entry, the catalog and overlay its tools/list reads, the active
// snapshot and the object lists, all in memory, so no database row serves
// it. It answers no rule's patterns, no other subject's access, no
// credential and nobody who decides an approval.

// whoamiNoSession refuses a call whose check-in this replica no longer
// holds. A new session checks in again.
const whoamiNoSession = "Straza: whoami could not read this session's check-in on this server. Start a new session, then call whoami again."

// whoamiPack is a knowledge pack the session received.
type whoamiPack struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// whoamiObjects is what a tool may touch on a server that pins an object
// type: every object, or the first covered names and how many there are.
type whoamiObjects struct {
	Type  string   `json:"type"`
	Every bool     `json:"every"`
	Names []string `json:"names,omitempty"`
	Count int      `json:"count"`

	plural string
}

// whoamiTool is one tool on the session's tool list. Approval is set when
// a person approves each call first, and Denied when policy denies every
// call of a tool the list still shows, as it does with
// apps.catalog.policyFilter off.
type whoamiTool struct {
	Server   string         `json:"server"`
	Tool     string         `json:"tool"`
	Approval bool           `json:"approval"`
	Denied   bool           `json:"denied"`
	Objects  *whoamiObjects `json:"objects,omitempty"`
}

// whoamiAnswer is whoami's structured answer. Kind is person, agent or
// service. Recording is the capture mode, verbatim or redact, when
// Recorded is set. Packs are the knowledge packs bound to the session's
// roles at its last check-in, which may differ from the packs injected at
// session start after an admin binds or unbinds one.
type whoamiAnswer struct {
	User      string       `json:"user"`
	Kind      string       `json:"kind"`
	Sponsor   string       `json:"sponsor,omitempty"`
	Harness   string       `json:"harness,omitempty"`
	Roles     []string     `json:"roles"`
	Recorded  bool         `json:"recorded"`
	Recording string       `json:"recording,omitempty"`
	Packs     []whoamiPack `json:"packs"`
	Tools     []whoamiTool `json:"tools"`
}

// whoami answers the calling session's identity and access, or the refusal
// when this replica no longer holds its check-in. sub is the subject the
// gateway decided the call for. The tools are exactly the session's
// tools/list, from the same catalog and overlay.
func (a *App) whoami(claims authn.Claims, sub policy.Subject) (*mcp.CallToolResult, string) {
	e, ok := a.subjects.forToken(claims)
	if !ok {
		return nil, whoamiNoSession
	}
	ans := whoamiAnswer{User: sub.User, Kind: "agent", Sponsor: sub.Sponsor, Harness: sub.Harness,
		Roles: sub.Roles, Packs: e.facts.packs, Tools: []whoamiTool{}}
	switch {
	case e.facts.person:
		ans.Kind = "person"
	case sub.UserType == store.UserTypeService:
		ans.Kind = "service"
	}
	if ans.Packs == nil {
		ans.Packs = []whoamiPack{}
	}
	if eng := a.snapshots.Current().Engine; eng != nil {
		if c := eng.Capture(sub); c.Conversations {
			ans.Recorded, ans.Recording = true, c.Mode
		}
	}
	tier1 := a.catalogFor(sub.Roles)
	ov := a.overlayFor(claims.Session, sub, tier1)
	for _, t := range overlayTools(tier1, ov) {
		target := tier1.targets[t.Name]
		_, approval := ov.schemas[t.Name]
		wt := whoamiTool{Server: target.app, Tool: target.tool, Approval: approval, Denied: ov.denied[t.Name]}
		if c, pinned := a.objectCall(tier1, target, nil); pinned {
			if cov := c.Covers(); cov.Named {
				wt.Objects = &whoamiObjects{Type: c.Type, Every: cov.Every, Names: cov.First, Count: cov.Count, plural: c.Plural}
			}
		}
		ans.Tools = append(ans.Tools, wt)
	}
	return &mcp.CallToolResult{
		StructuredContent: ans,
		Content:           []mcp.Content{&mcp.TextContent{Text: ans.text()}},
	}, ""
}

// text renders the answer for the agent, one fact a line, then the tools
// grouped by server in tools/list order.
func (w whoamiAnswer) text() string {
	var b strings.Builder
	kind := "an AI agent with no sponsor"
	switch {
	case w.Kind == "person":
		kind = "a person"
	case w.Kind == "service":
		kind = "a service account"
	case w.Sponsor != "":
		kind = fmt.Sprintf("an AI agent sponsored by %q", w.Sponsor)
	}
	fmt.Fprintf(&b, "This session acts as %q, %s", w.User, kind)
	if w.Harness != "" {
		fmt.Fprintf(&b, ", through %s", w.Harness)
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "Roles: %s.\n", joinOr(w.Roles, "none"))
	if w.Recorded {
		fmt.Fprintf(&b, "Recording: the prompts and responses of this session are recorded %s in the audit log.\n",
			agentguard.RecordingWords(w.Recording))
	} else {
		b.WriteString("Recording: the prompts and responses of this session are not recorded.\n")
	}
	packs := make([]string, len(w.Packs))
	for i, p := range w.Packs {
		packs[i] = p.Name + " version " + p.Version
	}
	fmt.Fprintf(&b, "Knowledge packs for this session's roles: %s.\n", joinOr(packs, "none"))
	if len(w.Tools) == 0 {
		b.WriteString("Tools: none. Ask an administrator for a role that grants the tools you need.\n")
		return b.String()
	}
	b.WriteString("Tools this session can call, by server. Each call is still checked when it is made, so a rule on its arguments can deny it.\n")
	server := ""
	for _, t := range w.Tools {
		if t.Server != server {
			server = t.Server
			fmt.Fprintf(&b, "%s:\n", server)
		}
		fmt.Fprintf(&b, "- %s: %s\n", t.Tool, t.words())
	}
	return b.String()
}

// words says how a call of the tool goes and which objects it may touch.
func (t whoamiTool) words() string {
	s := "runs without approval"
	switch {
	case t.Denied:
		return "listed, but policy denies every call"
	case t.Approval:
		s = "a person approves each call first"
	}
	o := t.Objects
	switch {
	case o == nil:
		return s
	case o.Every:
		return s + "; any " + o.Type
	case o.Count == 0:
		return s + "; no " + o.Type + " that this session's roles cover, so each call is refused"
	case o.Count > len(o.Names):
		return fmt.Sprintf("%s; %s: %s and %d more", s, o.plural, strings.Join(o.Names, ", "), o.Count-len(o.Names))
	}
	return s + "; " + o.plural + ": " + strings.Join(o.Names, ", ")
}

// joinOr joins items with commas, or answers none when there are none.
func joinOr(items []string, none string) string {
	if len(items) == 0 {
		return none
	}
	return strings.Join(items, ", ")
}

// whoamiPacksOf answers the names and versions of the knowledge packs a
// check-in delivers, which whoami tells the session.
func whoamiPacksOf(packs []store.KnowledgePack) []whoamiPack {
	out := make([]whoamiPack, len(packs))
	for i, p := range packs {
		out[i] = whoamiPack{Name: p.Name, Version: p.Version}
	}
	return out
}

// whoamiOffered reports whether the gateway serves whoami and sub's policy
// lets it call the tool, by the probe its tools/list makes, so the start
// text names the tool only where the session can call it.
func (a *App) whoamiOffered(sub policy.Subject) bool {
	eng := a.snapshots.Current().Engine
	if a.gateway == nil || eng == nil {
		return false
	}
	d := eng.Evaluate(policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
		App: nativeAppName, ToolName: nativeToolWhoami, Granted: true}, sub)
	return d.Effect == policy.EffectAllow
}
