package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
)

const (
	// whoamiDenyFor is a set that turns whoami off for one user.
	whoamiDenyFor = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: no-whoami}
spec:
  match: {users: [%s]}
  rules:
    - id: no-whoami
      tools: [mcp.call]
      apps: [straza]
      toolNames: {deny: ["whoami"]}
      effect: deny
      reason: "Straza: whoami is turned off in this deployment. Ask an admin what this session may do."
`
	// whoamiGates denies env and holds echo for a person, so one session's
	// list carries a tool that runs, one that waits and one policy denies.
	whoamiGates = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: whoami-gates}
spec:
  match: {roles: [dev]}
  rules:
    - id: deny-env
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "env reads are blocked for role dev"
    - id: hold-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
`
	wantWhoamiOff = "Straza: whoami is turned off in this deployment. Ask an admin what this session may do."
)

// TestWhoamiCarriesTheGrantedFact pins whoami's grant on its callers: tier
// one lists it to every role set, the granted fact keeps it visible with
// no rule, a deny hides it under policyFilter and marks it denied without,
// simulate's access check grants it, and the approval tools stay hidden.
func TestWhoamiCarriesTheGrantedFact(t *testing.T) {
	t.Parallel()
	for _, roles := range [][]string{nil, {"dev"}, {DraftConfigRole}} {
		if !nativeListed(nativeToolWhoami, roles) || !(&App{}).hasAccess(roles, nativeAppName, nativeToolWhoami) {
			t.Errorf("roles %v: whoami is not listed and granted", roles)
		}
	}
	deny, err := policy.Parse(fmt.Appendf(nil, whoamiDenyFor, "joe"))
	if err != nil {
		t.Fatal(err)
	}
	joe := policy.Subject{User: "joe", Roles: []string{"dev"}}
	for _, tc := range []struct {
		name    string
		filter  bool
		docs    []policy.Document
		visible []string
		denied  bool
	}{
		{"no rule, filter off", false, nil, []string{"straza__whoami"}, false},
		{"no rule, filter on", true, nil, []string{"straza__whoami"}, false},
		{"a deny, filter off, lists it and marks it denied", false, []policy.Document{deny}, []string{"straza__whoami"}, true},
		{"a deny, filter on, hides it", true, []policy.Document{deny}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := policy.NewEngine(tc.docs, policy.EffectAllow)
			if err != nil {
				t.Fatal(err)
			}
			a := &App{cfg: config.Config{Apps: config.Apps{Catalog: config.Catalog{PolicyFilter: tc.filter}}}}
			tier1 := &sessionCatalog{targets: map[string]gwTarget{}}
			a.appendNativeCatalog(tier1, joe.Roles)
			ov := a.buildOverlay("k", joe, tier1, eng)
			var got []string
			for _, tl := range overlayTools(tier1, ov) {
				got = append(got, tl.Name)
			}
			if !slices.Equal(got, tc.visible) {
				t.Errorf("the overlay shows %v, want %v (the approval tools stay hidden without a rule)", got, tc.visible)
			}
			if ov.denied["straza__whoami"] != tc.denied {
				t.Errorf("denied = %v, want %v", ov.denied["straza__whoami"], tc.denied)
			}
		})
	}
}

// TestWhoamiAnswersTheToolList pins that whoami answers exactly the
// session's tools/list, with and without policyFilter: the same names, the
// approval flag where the listed schema carries the justification field,
// and the denied flag on a tool the list still shows. A second user with
// no access row sees only whoami and nothing of the first user's access.
func TestWhoamiAnswersTheToolList(t *testing.T) {
	t.Parallel()
	for _, filter := range []bool{true, false} {
		t.Run(fmt.Sprintf("policyFilter %v", filter), func(t *testing.T) {
			t.Parallel()
			app, base, _ := testAppCounting(t, func(c *config.Config) { c.Apps.Catalog.PolicyFilter = filter })
			up := startGatewayUpstream(t)
			seedGatewayUser(t, app, "kim", "dev")
			seedGatewayUser(t, app, "lee", "intern")
			catInstallEcho(t, app, up, "echoapp", []string{"*"})
			catActivate(t, app, "whoami-gates", whoamiGates)
			for _, who := range []string{"kim", "lee"} {
				tok := sessionToken(t, base, who)
				_, list, _ := mcpCall(t, base, tok, "tools/list", nil)
				approvalListed := map[string]bool{}
				for _, raw := range list["result"].(map[string]any)["tools"].([]any) {
					tl := raw.(map[string]any)
					props, _ := tl["inputSchema"].(map[string]any)["properties"].(map[string]any)
					_, approvalListed[tl["name"].(string)] = props[justificationField]
				}
				text, isErr, sc := callTool(t, base, tok, "straza__whoami", map[string]any{})
				if isErr || sc == nil {
					t.Fatalf("%s: whoami = %q (error %v)", who, text, isErr)
				}
				if sc["user"] != who || !strings.HasPrefix(text, fmt.Sprintf("This session acts as %q", who)) {
					t.Errorf("%s: whoami answers user %v and text %q", who, sc["user"], text)
				}
				var answered []string
				for _, raw := range sc["tools"].([]any) {
					tl := raw.(map[string]any)
					name := tl["server"].(string) + "__" + tl["tool"].(string)
					answered = append(answered, name)
					if tl["approval"] != approvalListed[name] {
						t.Errorf("%s: %s approval %v, the listed schema says %v", who, name, tl["approval"], approvalListed[name])
					}
					if wantDenied := !filter && name == "echoapp__env"; tl["denied"] != wantDenied {
						t.Errorf("%s: %s denied %v, want %v", who, name, tl["denied"], wantDenied)
					}
				}
				listed := toolNamesOf(t, list)
				slices.Sort(listed)
				slices.Sort(answered)
				if !slices.Equal(answered, listed) {
					t.Errorf("%s: whoami answers %v, tools/list shows %v", who, answered, listed)
				}
				if who == "lee" && (!slices.Equal(answered, []string{"straza__whoami"}) || strings.Contains(text, "echoapp")) {
					t.Errorf("lee's whoami names more than its own access: %v\n%s", answered, text)
				}
			}
		})
	}
}

// TestWhoamiAuditedOnce pins whoami's record: one straza.audit.mcp record
// per call, with the actor, the session, the decision and its reason, for
// a call that runs by the granted fact and for one a deny rule refuses.
func TestWhoamiAuditedOnce(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedGatewayUser(t, app, "kim", "dev")
	quiet := seedGatewayUser(t, app, "quiet", "dev")
	catActivate(t, app, "no-whoami", fmt.Sprintf(whoamiDenyFor, "quiet"))

	kimTok, kimSid := gatewaySession(t, base, "kim")
	if text, isErr, _ := callTool(t, base, kimTok, "straza__whoami", map[string]any{}); isErr {
		t.Fatalf("kim's whoami = %q", text)
	}
	rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["user"] == kim.ID && d["toolName"] == nativeToolWhoami })[0]
	for k, v := range map[string]any{"effect": "allow", "app": nativeAppName, "granted": true, "default": true, "ruleId": "",
		"session": kimSid, "reason": "allowed by role access. No policy rule gates this tool."} {
		if rec[k] != v {
			t.Errorf("kim's record %s = %v, want %v", k, rec[k], v)
		}
	}

	quietTok, quietSid := gatewaySession(t, base, "quiet")
	if text, isErr, _ := callTool(t, base, quietTok, "straza__whoami", map[string]any{}); !isErr || text != wantWhoamiOff {
		t.Errorf("quiet's whoami = %q (error %v), want the rule's refusal", text, isErr)
	}
	rec = awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["user"] == quiet.ID && d["toolName"] == nativeToolWhoami })[0]
	for k, v := range map[string]any{"effect": "deny", "app": nativeAppName, "granted": true, "ruleId": "no-whoami",
		"setName": "no-whoami", "session": quietSid, "reason": wantWhoamiOff} {
		if rec[k] != v {
			t.Errorf("quiet's record %s = %v, want %v", k, rec[k], v)
		}
	}
}

// TestWhoamiStartTextSentence pins that check-in's start text names whoami
// only where the session's policy lets it call the tool.
func TestWhoamiStartTextSentence(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	seedGatewayUser(t, app, "quiet", "dev")
	catActivate(t, app, "no-whoami", fmt.Sprintf(whoamiDenyFor, "quiet"))
	for who, offered := range map[string]bool{"kim": true, "quiet": false} {
		code, body := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":    loginDeviceFlow(t, base, who, "hunter2!"),
			"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
		})
		if code != http.StatusOK {
			t.Fatalf("%s: checkin = %d %v", who, code, body)
		}
		if got, want := body["start_text"], agentguard.StartText(who, offered); got != want {
			t.Errorf("%s: start_text = %q, want %q", who, got, want)
		}
	}
}

// TestWhoamiText pins the words whoami answers the agent with.
func TestWhoamiText(t *testing.T) {
	t.Parallel()
	head := func(who string) string {
		return who + "\nRoles: dev.\nRecording: the prompts and responses of this session are not recorded.\nKnowledge packs for this session's roles: none.\n"
	}
	tools := "Tools this session can call, by server. Each call is still checked when it is made, so a rule on its arguments can deny it.\n"
	for _, tc := range []struct {
		name string
		ans  whoamiAnswer
		want string
	}{
		{"a person with no tools", whoamiAnswer{User: "alice", Kind: "person", Harness: "claude-code/2.1.0", Roles: []string{"dev"}},
			head(`This session acts as "alice", a person, through claude-code/2.1.0.`) +
				"Tools: none. Ask an administrator for a role that grants the tools you need.\n"},
		{"an agent with a sponsor, recorded, with packs", whoamiAnswer{User: "build-agent", Kind: "agent", Sponsor: "alice", Roles: []string{"dev", "readers"},
			Recorded: true, Recording: policy.CaptureModeRedact, Packs: []whoamiPack{{"golang-style", "1"}, {"review", "2"}}},
			`This session acts as "build-agent", an AI agent sponsored by "alice".` + "\nRoles: dev, readers.\n" +
				"Recording: the prompts and responses of this session are recorded with secrets masked in the audit log.\n" +
				"Knowledge packs for this session's roles: golang-style version 1, review version 2.\nTools: none. Ask an administrator for a role that grants the tools you need.\n"},
		{"a service account", whoamiAnswer{User: "nightly", Kind: "service", Roles: []string{"dev"}},
			head(`This session acts as "nightly", a service account.`) +
				"Tools: none. Ask an administrator for a role that grants the tools you need.\n"},
		{"an agent with no sponsor and every kind of tool", whoamiAnswer{User: "ci", Kind: "agent", Roles: []string{"dev"}, Tools: []whoamiTool{
			{Server: "ledger", Tool: "read", Objects: &whoamiObjects{Type: "book", Every: true, plural: "books"}},
			{Server: "ledger", Tool: "post", Approval: true, Objects: &whoamiObjects{Type: "book", Names: []string{"cash", "fees"}, Count: 2, plural: "books"}},
			{Server: "ledger", Tool: "close", Objects: &whoamiObjects{Type: "book", Names: []string{"cash"}, Count: 4, plural: "books"}},
			{Server: "ledger", Tool: "purge", Objects: &whoamiObjects{Type: "book", plural: "books"}},
			{Server: "ledger", Tool: "export", Denied: true},
			{Server: "straza", Tool: "whoami"},
		}},
			head(`This session acts as "ci", an AI agent with no sponsor.`) + tools + "ledger:\n" +
				"- read: runs without approval; any book\n" +
				"- post: a person approves each call first; books: cash, fees\n" +
				"- close: runs without approval; books: cash and 3 more\n" +
				"- purge: runs without approval; no book that this session's roles cover, so each call is refused\n" +
				"- export: listed, but policy denies every call\n" +
				"straza:\n- whoami: runs without approval\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ans.text(); got != tc.want {
				t.Errorf("text =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
