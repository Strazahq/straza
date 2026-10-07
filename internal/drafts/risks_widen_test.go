package drafts

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/redact"
)

// TestRunsCodeWordsCarryNoArgument pins that no finding of a check carries
// the text of a command's arguments, which a manifest stored before drafts
// may hold a secret in: server.runs-code counts the arguments and marks them
// changed, and its sentence names the command alone.
func TestRunsCodeWordsCarryNoArgument(t *testing.T) {
	t.Parallel()
	runner := func(args ...string) App {
		b, _ := json.Marshal(map[string]any{"straza": map[string]any{"runtime": map[string]any{"kind": "command",
			"command": map[string]any{"exec": "/usr/bin/runner", "args": args}}}})
		return App{Runtime: runtimeCommand, Exec: "/usr/bin/runner", Args: args, Exposure: []string{"*"}, Manifest: string(b)}
	}
	w := gainWorld()
	w.Apps["runner"] = runner("--token", fakeGitHub, "--verbose")
	w.Fingerprints["App/runner"] = "fp-App/runner"
	v := Check(w, stamped(w, appItem("runner")), CheckInput{Apps: map[string]App{"runner": runner("--token", fakeGitHub, "--quiet")}, Now: checkNow})
	want := []string{"server.runs-code App/runner (typed runner) Publishing starts /usr/bin/runner on the Straza host as Straza's own user, now and after every restart. " +
		"| command /usr/bin/runner 3 args | command /usr/bin/runner 3 args (args changed)"}
	if got := only(riskLines(v.Risks), "server.runs-code"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	for _, list := range [][]Finding{v.Refused, v.Risks, v.Warnings, v.Unchecked, v.Passed, v.Info} {
		for _, f := range list {
			if text := strings.Join([]string{f.Object, f.Sentence, f.Fix, f.Before, f.After, f.Typed}, "\n"); strings.Contains(text, fakeGitHub) {
				t.Errorf("%s carries the argument: %q", f.Code, text)
			}
		}
	}
	for _, words := range [][2]string{{"command /usr/bin/runner 1 arg", "one argument"}, {"command /usr/bin/runner 0 args", "none"}} {
		args := []string{"--verbose"}
		if words[1] == "none" {
			args = nil
		}
		if got := runtimeWords(runner(args...), nil, false); got != words[0] {
			t.Errorf("%s: runtimeWords = %q, want %q", words[1], got, words[0])
		}
	}
}

// TestRiskWordsMaskACapabilityInAnAddressPath pins that no finding carries
// a capability that a live server's stored address holds in its path, as a
// manifest stored before the secret scan may: server.new-host,
// server.plain-http and server.runs-code spell the old address with each
// part of its path that the secret.path rule flags replaced by redact.Mark.
func TestRiskWordsMaskACapabilityInAnAddressPath(t *testing.T) {
	t.Parallel()
	masked := "hooks.example.com/services/T0AB12CD3/B0EF45GH6/" + redact.Mark
	remote := func(url string) App {
		return App{Runtime: runtimeRemote, URL: url, Credential: CredentialNone, Exposure: []string{"*"}}
	}
	cases := []struct {
		name string
		next App
		want []string
	}{
		{"the address moves on the same host", remote("https://hooks.example.com/v2/mcp"), []string{
			"server.new-host App/hook (tick) The address of hook moves to /v2/mcp on the same host. | " + masked + " | hooks.example.com/v2/mcp"}},
		{"the address moves to plain http", remote("http://hooks.example.com/v2/mcp"), []string{
			"server.new-host App/hook (tick) The address of hook moves to /v2/mcp on the same host. | " + masked + " | hooks.example.com/v2/mcp",
			"server.plain-http App/hook (typed hooks.example.com) hook will be reached at hooks.example.com over plain http, where it used https, so every call travels unencrypted. | " +
				"https://" + masked + " | http://hooks.example.com/v2/mcp"}},
		{"the address moves to a new host", remote("https://relay.example.net/mcp"), []string{
			"server.new-host App/hook (tick) hook will be reached at relay.example.net, a host no server uses today. | " + masked + " | relay.example.net/mcp"}},
		{"the server comes to run as a command", App{Runtime: runtimeCommand, Exec: "/usr/bin/hook", Credential: CredentialNone, Exposure: []string{"*"}}, []string{
			"server.runs-code App/hook (typed hook) Publishing starts /usr/bin/hook on the Straza host as Straza's own user, now and after every restart. | remote https://" + masked + " | command /usr/bin/hook 0 args"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := gainWorld()
			live := remote("https://hooks.example.com/services/T0AB12CD3/B0EF45GH6/" + fakeCapability)
			live.ID, live.Name, live.Status, live.AdminRole, live.RolePrefix = "a3", "hook", "running", "mcp-admin-hook", "hook-"
			w.Apps["hook"] = live
			w.Fingerprints["App/hook"] = "fp-App/hook"
			v := Check(w, stamped(w, appItem("hook")), CheckInput{Apps: map[string]App{"hook": tc.next}, Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", riskLines(v.Refused))
			}
			if got := only(riskLines(v.Risks), "server."); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
			agent := v.ForAgent()
			for _, list := range [][]Finding{v.Refused, v.Risks, v.Warnings, v.Unchecked, v.Passed, v.Info, agent.Risks, agent.Warnings, agent.Unchecked} {
				for _, f := range list {
					if text := strings.Join([]string{f.Object, f.Sentence, f.Fix, f.Before, f.After, f.Typed}, "\n"); strings.Contains(text, fakeCapability) {
						t.Errorf("%s carries the capability: %q", f.Code, text)
					}
				}
			}
		})
	}
}

// TestExposureWiderWithoutAList pins server.exposure-wider for a live
// server whose exposure widens while its address changes, so no tool list
// is known for the new address: the risk names the globs instead of tools.
func TestExposureWiderWithoutAList(t *testing.T) {
	t.Parallel()
	const wider = "server.exposure-wider App/github (tick) github will offer more of its tools, because its exposure widens from get_* to *. | get_* | *"
	cases := []struct {
		name, url string
		exposure  []string
		want      []string
	}{
		{"the address gains a query", "https://api.github.com/mcp?v=2", []string{"*"}, []string{wider}},
		{"the address gains a user name", "https://robot@api.github.com/mcp", []string{"*"}, []string{wider}},
		{"the address moves to another server's host", "https://jira.example.com/other", []string{"*"}, []string{wider}},
		{"the address changes and the exposure stays", "https://api.github.com/mcp?v=2", []string{"get_*"}, []string{}},
		{"the address changes and the exposure narrows", "https://api.github.com/mcp?v=2", []string{"get_me"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := gainWorld()
			gh := w.Apps["github"]
			gh.Exposure = []string{"get_*"}
			w.Apps["github"] = gh
			next := gh
			next.URL, next.Exposure, next.Offered, next.ReadOnly = tc.url, tc.exposure, nil, nil
			v := Check(w, stamped(w, appItem("github")), CheckInput{Apps: map[string]App{"github": next}, Now: checkNow})
			if got := only(riskLines(v.Risks), "server.exposure-wider"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestScopesWider pins server.scopes-wider: a typed risk when a live OAuth
// server asks for a scope it does not ask for today, at its provider, in
// every person's sign-in.
func TestScopesWider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		provider string
		scopes   []string
		want     []string
	}{
		{"three scopes join", "entra", []string{"read:user", "user:email", "repo", "delete_repo", "admin:org"}, []string{
			"server.scopes-wider App/github (typed github) github will ask for the scopes admin:org, delete_repo and repo in every person's sign-in at entra, " +
				"beyond what it asks for today, so the tokens Straza holds for github can do more. | read:user, user:email | admin:org, delete_repo, read:user, repo, user:email"}},
		{"one scope joins", "entra", []string{"read:user", "user:email", "repo"}, []string{
			"server.scopes-wider App/github (typed github) github will ask for the scope repo in every person's sign-in at entra, " +
				"beyond what it asks for today, so the tokens Straza holds for github can do more. | read:user, user:email | read:user, repo, user:email"}},
		{"the provider changes", "okta", []string{"read:user", "user:email"}, []string{
			"server.scopes-wider App/github (typed github) github will ask for the scopes read:user and user:email in every person's sign-in at okta, " +
				"beyond what it asks for today, so the tokens Straza holds for github can do more. | read:user, user:email | read:user, user:email"}},
		{"the same scopes in another order", "entra", []string{"user:email", "read:user"}, []string{}},
		{"a scope goes", "entra", []string{"read:user"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := gainWorld()
			w.Providers = map[string]Provider{"entra": {Name: "entra"}, "okta": {Name: "okta"}}
			gh := w.Apps["github"]
			gh.Credential, gh.Provider, gh.Scopes = CredentialOAuth, "entra", []string{"read:user", "user:email"}
			w.Apps["github"] = gh
			next := gh
			next.Provider, next.Scopes = tc.provider, tc.scopes
			v := Check(w, stamped(w, appItem("github")), CheckInput{Apps: map[string]App{"github": next}, Now: checkNow})
			if got := only(riskLines(v.Risks), "server.scopes-wider"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
			if agent := v.ForAgent(); len(tc.want) > 0 && !slices.ContainsFunc(agent.Risks, func(f Finding) bool { return f.Code == "server.scopes-wider" }) {
				t.Errorf("the agent's view dropped server.scopes-wider, which names no holder")
			}
		})
	}
}

// TestUnlistedServerRow pins a held role's row on a server nobody has
// listed, before or after the draft: a row whose matchers come to admit
// more names draws one gain on tool * with both sides unknown, the
// matchers in its words, and a tick access.tools-later.
func TestUnlistedServerRow(t *testing.T) {
	t.Parallel()
	later := func(tools string) string {
		return "access.tools-later Role/readers (tick) readers will reach more tools on jira, which nobody has listed yet, so Straza cannot name them. | get_me | " + tools
	}
	cases := []struct {
		name      string
		live      []string
		tools     string
		holders   bool
		wantGains []string
		wantRisks []string
	}{
		{"a prefix glob", []string{"get_me"}, `["delete*"]`, true,
			[]string{"readers jira/* unknown>unknown [tools matching get_me|tools matching delete*] carol"}, []string{later("delete*")}},
		{"a glob of two words", []string{"get_me"}, `["*_*"]`, true,
			[]string{"readers jira/* unknown>unknown [tools matching get_me|tools matching *_*] carol"}, []string{later("*_*")}},
		{"every tool", []string{"get_me"}, `["*"]`, true,
			[]string{"readers jira/* unknown>unknown [tools matching get_me|tools matching *] carol"},
			[]string{"access.tools-later Role/readers (tick) readers will reach every tool on jira, including tools the server adds later. | get_me | *"}},
		{"a narrower row", []string{"get_me", "list_*"}, `["list_issues"]`, true, []string{}, []string{}},
		{"a role nobody holds", []string{"get_me"}, `["delete*"]`, false,
			[]string{"readers jira/* unknown>unknown [tools matching get_me|tools matching delete*] "}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := gainWorld()
			jira := w.Apps["jira"]
			jira.Offered = nil
			w.Apps["jira"] = jira
			w.Access["readers"] = Access{ID: "b2", Server: "jira", Tools: tc.live}
			if !tc.holders {
				delete(w.Holders, "readers")
			}
			spec := "    kind: application\n    bindings:\n        - app: jira\n          tools: " + tc.tools + "\n"
			v := Check(w, stamped(w, gainRole("readers", spec)), CheckInput{Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", riskLines(v.Refused))
			}
			if got := only(gainLines(v.Gains), "readers jira/"); !reflect.DeepEqual(got, tc.wantGains) {
				t.Errorf("gains %q\nwant %q", got, tc.wantGains)
			}
			if got := only(riskLines(v.Risks), "access.tools-later"); !reflect.DeepEqual(got, tc.wantRisks) {
				t.Errorf("risks %q\nwant %q", got, tc.wantRisks)
			}
		})
	}
}
