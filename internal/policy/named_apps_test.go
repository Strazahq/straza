package policy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// namedAppsEngine compiles the policies the NamedApps table reads: one for
// the role audio-makers, one for another role, one for everyone, and one
// whose only rule names no server.
func namedAppsEngine(t *testing.T) *Engine {
	t.Helper()
	sources := []string{`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: audio-access }
spec:
  match: { roles: [audio-makers] }
  rules:
    - id: speak
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames: { allow: ["text_to_speech", "search_voices"] }
      effect: allow
    - id: no-deletes
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames: { deny: ["delete_*", "re:purge_.+"] }
      effect: deny
    - id: clone-needs-yes
      tools: [mcp.call]
      apps: [elevenlabs, soundbank]
      toolNames: { allow: ["voice_clone"] }
      mode: approve
      effect: allow
      approve:
        roles: [sec-approvers]
    - id: no-shell
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
    - id: shell-on-server
      tools: [shell.exec]
      apps: [shellsrv]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
    - id: after-only
      events: [tool.post]
      tools: [mcp.call]
      apps: [postsrv]
      toolNames: { allow: ["read"] }
      effect: allow
`, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: other-access }
spec:
  match: { roles: [someone-else] }
  rules:
    - id: theirs
      tools: [mcp.call]
      apps: [secret-server]
      toolNames: { allow: ["read_all"] }
      effect: allow
`, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: floor }
spec:
  rules:
    - id: no-vault
      tools: [mcp.call]
      apps: [vault]
      effect: deny
    - id: every-server
      tools: [mcp.call]
      toolNames: { deny: ["drop_database"] }
      effect: deny
`, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: lists-everywhere }
spec:
  match: { roles: [list-readers] }
  rules:
    - id: lists
      tools: [mcp.call]
      toolNames: { allow: ["list_*"] }
      effect: allow
`}
	docs := make([]Document, len(sources))
	for i, src := range sources {
		doc, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse policy %d: %v", i, err)
		}
		docs[i] = doc
	}
	eng, err := NewEngine(docs, EffectDeny)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

// TestNamedApps pins the read-only list of MCP servers that the policies
// applying to a subject name: only the policies that apply, each server
// once across rules and policies with the policies that name it, the tool
// names a rule lists whole, and Open for a pattern or for a rule that
// names no tool. A rule with no apps list names no server, and what it
// lists and reaches counts for every server the others name. A rule that
// decides no call before it runs, or no MCP call, adds nothing.
func TestNamedApps(t *testing.T) {
	eng := namedAppsEngine(t)
	cases := []struct {
		name string
		sub  Subject
		want []NamedApp
	}{
		{"a holder of the role", Subject{User: "altair", Roles: []string{"audio-makers"}}, []NamedApp{
			{App: "elevenlabs", Tools: []string{"drop_database", "search_voices", "text_to_speech", "voice_clone"}, Open: true, Policies: []string{"audio-access"}},
			{App: "soundbank", Tools: []string{"drop_database", "voice_clone"}, Policies: []string{"audio-access"}},
			{App: "vault", Tools: []string{"drop_database"}, Open: true, Policies: []string{"floor"}},
		}},
		{"a holder of the other role", Subject{User: "ben", Roles: []string{"someone-else"}}, []NamedApp{
			{App: "secret-server", Tools: []string{"drop_database", "read_all"}, Policies: []string{"other-access"}},
			{App: "vault", Tools: []string{"drop_database"}, Open: true, Policies: []string{"floor"}},
		}},
		{"a subject with no role", Subject{User: "carol"}, []NamedApp{
			{App: "vault", Tools: []string{"drop_database"}, Open: true, Policies: []string{"floor"}},
		}},
		{"a holder whose other policy reaches tools of every server by a pattern", Subject{User: "dana", Roles: []string{"audio-makers", "list-readers"}}, []NamedApp{
			{App: "elevenlabs", Tools: []string{"drop_database", "search_voices", "text_to_speech", "voice_clone"}, Open: true, Policies: []string{"audio-access"}},
			{App: "soundbank", Tools: []string{"drop_database", "voice_clone"}, Open: true, Policies: []string{"audio-access"}},
			{App: "vault", Tools: []string{"drop_database"}, Open: true, Policies: []string{"floor"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eng.NamedApps(tc.sub, nil)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NamedApps = %+v\nwant        %+v", got, tc.want)
			}
		})
	}
}

// TestNamedAppsOpenMeansEvaluateReachesMore pins what Open promises: a
// server that is not Open has no tool outside Tools that a rule decides,
// and the tool a rule with no apps list allows is among the Tools of every
// listed server.
func TestNamedAppsOpenMeansEvaluateReachesMore(t *testing.T) {
	eng := namedAppsEngine(t)
	probe := func(sub Subject, app, tool string) Decision {
		return eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: app, ToolName: tool}, sub)
	}
	plain := Subject{User: "altair", Roles: []string{"audio-makers"}}
	if d := probe(plain, "soundbank", "list_voices"); !d.Default {
		t.Errorf("soundbank is not Open for altair, yet a rule decides list_voices: %+v", d)
	}
	lists := Subject{User: "dana", Roles: []string{"audio-makers", "list-readers"}}
	if d := probe(lists, "soundbank", "list_voices"); d.Effect != EffectAllow || d.RuleID != "lists" {
		t.Errorf("soundbank is Open for dana, and list_voices = %+v, want an allow by the rule lists", d)
	}
	if d := probe(plain, "postsrv", "read"); !d.Default {
		t.Errorf("postsrv is not listed, yet a rule decides its call before it runs: %+v", d)
	}
}

// TestNamedAppsCutsALongList pins the bound of one answer: it lists at most
// namedToolsMax tool names over all servers, in the order of the servers'
// names within a rule, and a server whose names are cut is Open, because
// the rules reach tools the answer does not hold.
func TestNamedAppsCutsALongList(t *testing.T) {
	per := namedToolsMax * 3 / 4
	names := make([]string, per)
	for i := range names {
		names[i] = fmt.Sprintf("tool_%04d", i)
	}
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: bulk }
spec:
  rules:
    - id: many
      tools: [mcp.call]
      apps: [c-srv, a-srv, b-srv]
      toolNames: { allow: ["` + strings.Join(names, `", "`) + `"] }
      effect: allow
`))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine([]Document{doc}, EffectDeny)
	if err != nil {
		t.Fatal(err)
	}
	got := eng.NamedApps(Subject{User: "altair"}, nil)
	if len(got) != 3 {
		t.Fatalf("servers = %d, want all three listed", len(got))
	}
	want := []struct {
		app   string
		tools int
		open  bool
	}{{"a-srv", per, false}, {"b-srv", namedToolsMax - per, true}, {"c-srv", 0, true}}
	for i, w := range want {
		if got[i].App != w.app || len(got[i].Tools) != w.tools || got[i].Open != w.open {
			t.Errorf("server %d = %s with %d tools, open %v, want %s with %d, open %v", i, got[i].App, len(got[i].Tools), got[i].Open, w.app, w.tools, w.open)
		}
	}
}

// TestNamedAppsKeep pins the caller's choice of servers: a server that
// keep refuses is not listed and spends nothing of the answer's bound, for
// a rule that names it and for a rule with no apps list alike, so a policy
// that lists many tools of a server the caller drops cannot empty the rows
// of the servers it keeps.
func TestNamedAppsKeep(t *testing.T) {
	names := make([]string, namedToolsMax)
	for i := range names {
		names[i] = fmt.Sprintf("tool_%04d", i)
	}
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: estate }
spec:
  rules:
    - id: registered-tools
      tools: [mcp.call]
      apps: [github]
      toolNames: { allow: ["` + strings.Join(names, `", "`) + `"] }
      effect: allow
    - id: speak
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames: { allow: ["text_to_speech"] }
      effect: allow
    - id: floor
      tools: [mcp.call]
      toolNames: { deny: ["drop_database"] }
      effect: deny
`))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine([]Document{doc}, EffectDeny)
	if err != nil {
		t.Fatal(err)
	}
	sub := Subject{User: "altair"}
	got := eng.NamedApps(sub, func(app string) bool { return app != "github" })
	want := []NamedApp{{App: "elevenlabs", Tools: []string{"drop_database", "text_to_speech"}, Policies: []string{"estate"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NamedApps without github = %+v\nwant %+v", got, want)
	}
	all := eng.NamedApps(sub, nil)
	if len(all) != 2 || all[0].App != "elevenlabs" || all[1].App != "github" {
		t.Fatalf("NamedApps with every server = %d servers, want elevenlabs and github", len(all))
	}
	if len(all[1].Tools) == 0 || len(all[0].Tools)+len(all[1].Tools) != namedToolsMax {
		t.Errorf("with every server kept the answer lists %d and %d tools, want %d in all", len(all[0].Tools), len(all[1].Tools), namedToolsMax)
	}
}

// TestNamedAppsOpenForAFailedRequire pins that a rule whose require block
// the subject fails makes its servers Open: the rule then denies every
// tool of them, the ones it does not list too. A subject that meets the
// block is decided by the rule's names alone, and by nothing where the
// rule is the require block alone.
func TestNamedAppsOpenForAFailedRequire(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: managed-only }
spec:
  rules:
    - id: on-managed
      tools: [mcp.call]
      apps: [reqsrv]
      toolNames: { allow: ["read"] }
      require: { attestation: managed }
      effect: allow
    - id: gate
      tools: [mcp.call]
      apps: [gatesrv]
      require: { attestation: managed }
`))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine([]Document{doc}, EffectDeny)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		sub  Subject
		open bool
	}{
		{"a subject with no attestation", Subject{User: "altair"}, true},
		{"a subject on a managed machine", Subject{User: "altair", Attestation: "managed"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eng.NamedApps(tc.sub, nil)
			want := []NamedApp{
				{App: "gatesrv", Open: tc.open, Policies: []string{"managed-only"}},
				{App: "reqsrv", Tools: []string{"read"}, Open: tc.open, Policies: []string{"managed-only"}},
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("NamedApps = %+v\nwant %+v", got, want)
			}
			for app, rule := range map[string]string{"reqsrv": "on-managed", "gatesrv": "gate"} {
				d := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: app, ToolName: "unlisted"}, tc.sub)
				if decided := d.RuleID == rule; decided != tc.open {
					t.Errorf("rule %s decides a tool of %s it does not list: %v, and Open is %v", rule, app, decided, tc.open)
				}
			}
		})
	}
}

// TestNamedAppsDecidesNothing pins that the list is a reading aid and the
// engine alone decides: every tool it names for a subject gets the outcome
// Evaluate gives, and the list is the same before and after the calls.
func TestNamedAppsDecidesNothing(t *testing.T) {
	eng := namedAppsEngine(t)
	sub := Subject{User: "altair", Roles: []string{"audio-makers"}}
	before := eng.NamedApps(sub, nil)
	want := map[string]string{
		"elevenlabs/text_to_speech": EffectAllow,
		"elevenlabs/search_voices":  EffectAllow,
		"elevenlabs/voice_clone":    EffectAllow,
		"soundbank/voice_clone":     EffectAllow,
		"elevenlabs/drop_database":  EffectDeny,
		"soundbank/drop_database":   EffectDeny,
		"vault/drop_database":       EffectDeny,
	}
	for _, app := range before {
		for _, tool := range app.Tools {
			d := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: app.App, ToolName: tool}, sub)
			if d.Effect != want[app.App+"/"+tool] {
				t.Errorf("%s/%s = %q, want %q", app.App, tool, d.Effect, want[app.App+"/"+tool])
			}
		}
	}
	if d := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "elevenlabs", ToolName: "delete_voice"}, sub); d.Effect != EffectDeny {
		t.Errorf("a tool the pattern denies = %q, want deny", d.Effect)
	}
	if after := eng.NamedApps(sub, nil); !reflect.DeepEqual(before, after) {
		t.Fatalf("NamedApps changed across Evaluate calls: %+v then %+v", before, after)
	}
}

// TestNamedAppsOfAnEmptyEngine pins the answer of an engine with no policy:
// an empty list, never nil, so a JSON answer built from it is an array.
func TestNamedAppsOfAnEmptyEngine(t *testing.T) {
	eng, err := NewEngine(nil, EffectDeny)
	if err != nil {
		t.Fatal(err)
	}
	if got := eng.NamedApps(Subject{User: "altair"}, nil); got == nil || len(got) != 0 {
		t.Fatalf("NamedApps of an empty engine = %#v, want an empty list that is not nil", got)
	}
}
