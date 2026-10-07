package drafts

import (
	"reflect"
	"testing"
)

// noteLines spells each line as its code, object and sentence.
func noteLines(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code+" "+f.Object+" "+f.Sentence)
	}
	return out
}

func TestNotes(t *testing.T) {
	t.Parallel()
	newsrv := App{Runtime: "remote", URL: "https://mcp.example.net/mcp", Credential: CredentialNone, Exposure: []string{"*"}, AdminRole: "mcp-admin-x", RolePrefix: "x-"}
	cases := []struct {
		name  string
		items []Item
		apps  map[string]App
		class Class
		want  []string
	}{
		{"a remote server nobody contacted", []Item{appItem("newsrv")}, map[string]App{"newsrv": newsrv}, ClassUnchecked, []string{
			"unchecked.tools App/newsrv Straza has not contacted mcp.example.net, because an address in a draft is contacted only when a person asks, so the tool names of newsrv are unknown."}},
		{"a command server not started", []Item{appItem("local")}, map[string]App{"local": {Runtime: "command", Exec: "mcp", Credential: CredentialNone}},
			ClassUnchecked, []string{"unchecked.tools App/local Straza has not started local, so its tool names are unknown until it is published."}},
		{"a rule whose gate depends on the session", []Item{{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }",
			"    - id: managed\n      tools: [shell.exec]\n      effect: allow\n      require: { attestation: managed, harness: [claude-code, codex] }\n")}}, nil, ClassUnchecked, []string{
			"unchecked.require  Straza read who gains what for a session that meets every require predicate of readers-access, the widest reach a holder can have, so a session that does not meet them reaches less."}},
		{"a set that compiles, pools that decide, no secret", []Item{{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }",
			"    - id: hold\n      tools: [mcp.call]\n      effect: allow\n      mode: approve\n      approve: { roles: [sec-approvers] }\n")}}, nil, ClassPassed, []string{
			"passed.compile  The policy compiles with this draft.",
			"passed.pools  Every approver role the draft's rules name exists and may decide.",
			"passed.secrets  No document holds a secret or the shape of one."}},
		{"a new role nobody holds, with packs", []Item{gainRole("writers", "    kind: application\n    packs: [go-style]\n")}, nil, ClassInfo, []string{
			"info.nobody-holds Role/writers Nobody holds writers yet, so it reaches nothing until someone is assigned it.",
			"info.packs Role/writers The knowledge packs of writers are bound directly, not through drafts, so this draft leaves them as they are."}},
		{"a server that is down", []Item{appItem("jira")},
			map[string]App{"jira": {Runtime: "remote", URL: "https://jira.example.com/mcp", Credential: CredentialNone, Manifest: "{}"}}, ClassInfo, []string{
				"info.server-down App/jira jira reads degraded now: upstream answered 502."}},
		{"documents equal to live", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me]\n"),
			{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess}, {Kind: KindRole, Name: "nosuch", Op: OpRemove}}, nil, ClassInfo, []string{
			"info.no-change PolicySet/dev-access PolicySet/dev-access equals live, so publishing changes nothing for it.",
			"info.no-change Role/nosuch Role/nosuch equals live, so publishing changes nothing for it.",
			"info.no-change Role/readers Role/readers equals live, so publishing changes nothing for it."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			jira := w.Apps["jira"]
			jira.Status, jira.Detail = "degraded", "upstream answered 502"
			w.Apps["jira"] = jira
			v := Check(w, stamped(w, tc.items...), CheckInput{Apps: tc.apps, Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			list := map[Class][]Finding{ClassUnchecked: v.Unchecked, ClassPassed: v.Passed, ClassInfo: v.Info}[tc.class]
			if got := noteLines(list); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s\n got %q\nwant %q", tc.class, got, tc.want)
			}
		})
	}
}
