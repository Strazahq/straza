package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// The gate's sentences on ghapp, as the agent reads them.
const (
	gwG2 = "Straza: pr_create needs the repo it works on in its argument repo, and this call carries none. Send repo as a string, then try again."
	gwG3 = "Straza: pr_create needs the repo in its argument repo as a string, and this call sends another type. Send repo as a string, then try again."
	gwG5 = "Straza: pr_create's arguments carry both repo and Repo, and ghapp might read either. Send repo once, as its input schema spells it, then try again."
	gwG7 = "Straza: pr_merge on ghapp changed how it names its repo since an admin pinned it, so roles on some repos cannot use it until an admin publishes the change. Ask an admin to review ghapp's Repos tab."
	gwG8 = "Straza: Straza's repo checks are off on this deployment, so only a role on every repo may call pr_create on ghapp. Ask an admin to turn apps.objects.enabled back on, or for a role on every repo."
)

func gwG1(value, covered string) string {
	s := "Straza: pr_create on " + value + " was refused: none of your roles on ghapp covers that repo."
	if covered != "" {
		s += " Your roles cover pr_create on " + covered + "."
	}
	return s + " Check the repo's name, or ask an admin for a role that covers it."
}

func gwG4(fault string) string {
	return "Straza: pr_create's arguments could not be read the same way by Straza and by ghapp: " + fault +
		". Send each argument once, in one JSON object, then try again."
}

func gwG6(tool string) string {
	return "Straza: " + tool + " on ghapp can reach every repo, so only a role on every repo may call it, and none of your roles on ghapp is one. Ask for a role on every repo, or use a tool that names the repo."
}

// gateCase is one tools/call against ghapp and what it must cause. args is
// the raw arguments with %s where the case's nonce goes, so its one record
// can be found. A refusal reaches no upstream and records rolesChecked.
type gateCase struct {
	name, user, tool, args, meta string
	refusal                      string // "" for a call that runs
	object                       map[string]any
	role                         string         // the granting role of a call that runs
	checked                      []string       // rolesChecked of a refusal
	sentMeta                     map[string]any // the _meta the upstream must get
	sentArgs                     string         // the bytes the upstream must get, when pinned
	auth                         string         // the Authorization the upstream must get
	credential                   string         // the role whose credential row the record names
}

func repoObject(reach, value, id string) map[string]any {
	o := map[string]any{"type": "repo", "reach": reach}
	if value != "" {
		o["value"] = value
	}
	if id != "" {
		o["id"] = id
	}
	return o
}

// TestGatewayObjectGate is acceptance cases 1, 3 to 10, 12, 13, 32 and 33
// on both endpoints: who each reach admits, the sentence of every refusal,
// that a refusal reaches neither the engine's rule nor the upstream, the
// bytes and the _meta the upstream gets, the role and credential of the
// grant, and that every call leaves exactly one record with the actor, the
// tool, the decision, the reason and the object.
func TestGatewayObjectGate(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	cases := []gateCase{
		{name: "a role on some repos allows its repo", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			object: repoObject("argument", "Strazahq/pay-api", "101"), role: "gh-pay",
			sentMeta: map[string]any{objectIDMeta: "101"}, auth: "Bearer gh-app-tok"},
		{name: "every allows a listed repo without the id", user: "eve", tool: "pr_create", args: `{"repo":"Strazahq/site","n":"%s"}`,
			object: repoObject("argument", "Strazahq/site", "103"), role: "gh-every"},
		{name: "every allows an unlisted repo", user: "eve", tool: "pr_create", args: `{"repo":"Strazahq/new","n":"%s"}`,
			object: repoObject("argument", "Strazahq/new", ""), role: "gh-every"},
		{name: "an uncovered repo is refused", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/site","n":"%s"}`,
			refusal: gwG1("Strazahq/site", "Strazahq/pay-api and Strazahq/pay-web"), object: repoObject("argument", "Strazahq/site", "103"), checked: []string{"gh-pay"}},
		{name: "an unlisted repo reads the same", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/new","n":"%s"}`,
			refusal: gwG1("Strazahq/new", "Strazahq/pay-api and Strazahq/pay-web"), object: repoObject("argument", "Strazahq/new", ""), checked: []string{"gh-pay"}},
		{name: "a binding with no covers line covers nothing", user: "old", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			refusal: gwG1("Strazahq/pay-api", ""), object: repoObject("argument", "Strazahq/pay-api", "101"), checked: []string{"gh-bare"}},
		{name: "a missing argument", user: "pam", tool: "pr_create", args: `{"n":"%s"}`,
			refusal: gwG2, object: repoObject("argument", "", ""), checked: []string{"gh-pay"}},
		{name: "a number", user: "pam", tool: "pr_create", args: `{"repo":7,"n":"%s"}`,
			refusal: gwG3, object: repoObject("argument", "", ""), checked: []string{"gh-pay"}},
		{name: "a key twice, even for every", user: "eve", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","repo":"Strazahq/site","n":"%s"}`,
			refusal: gwG4("a key appears twice"), object: repoObject("argument", "", ""), checked: []string{"gh-every"}},
		{name: "a case twin", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","Repo":"Strazahq/site","n":"%s"}`,
			refusal: gwG5, object: repoObject("argument", "", ""), checked: []string{"gh-pay"}},
		{name: "bytes that are not UTF-8", user: "pam", tool: "pr_create", args: "{\"repo\":\"Strazahq/pay-api\xff\",\"n\":\"%s\"}",
			refusal: gwG4("they are not valid UTF-8"), object: repoObject("argument", "", ""), checked: []string{"gh-pay"}},
		{name: "the raw bytes go upstream", user: "eve", tool: "pr_create",
			args:   `{"repo":"Strazahq/site","big":12345678901234567890,"dec":0.1000000000000000000000001,"n":"%s"}`,
			object: repoObject("argument", "Strazahq/site", "103"), role: "gh-every",
			sentArgs: `{"repo":"Strazahq/site","big":12345678901234567890,"dec":0.1000000000000000000000001,"n":"%s"}`},
		{name: "the raw bytes go upstream with a justification", user: "pam", tool: "pr_create",
			args:   `{"_straza_justification":"ship it","repo":"Strazahq/pay-web","big":98765432109876543210,"n":"%s"}`,
			object: repoObject("argument", "Strazahq/pay-web", "102"), role: "gh-pay", sentMeta: map[string]any{objectIDMeta: "102"},
			sentArgs: `{"_straza_justification":"ship it","repo":"Strazahq/pay-web","big":98765432109876543210,"n":"%s"}`},
		{name: "the second role covers and its credential runs", user: "duo", tool: "pr_create", args: `{"repo":"Strazahq/site","n":"%s"}`,
			object: repoObject("argument", "Strazahq/site", "103"), role: "gh-b-site", credential: "gh-b-site",
			sentMeta: map[string]any{objectIDMeta: "103"}, auth: "Bearer tok-gh-b-site"},
		{name: "the first role covers and its credential runs", user: "duo", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			object: repoObject("argument", "Strazahq/pay-api", "101"), role: "gh-a-pay", credential: "gh-a-pay",
			sentMeta: map[string]any{objectIDMeta: "101"}, auth: "Bearer tok-gh-a-pay"},
		{name: "reach all allows every", user: "eve", tool: "repo_admin", args: `{"n":"%s"}`, role: "gh-every"},
		{name: "reach all refuses some repos", user: "pam", tool: "repo_admin", args: `{"n":"%s"}`,
			refusal: gwG6("repo_admin"), object: repoObject("all", "", ""), checked: []string{"gh-pay"}},
		{name: "an unmarked tool is all", user: "pam", tool: "repo_create", args: `{"n":"%s"}`,
			refusal: gwG6("repo_create"), object: repoObject("all", "", ""), checked: []string{"gh-pay"}},
		{name: "an unmarked tool allows every", user: "eve", tool: "repo_create", args: `{"n":"%s"}`, role: "gh-every"},
		{name: "reach credential refuses some repos", user: "duo", tool: "db_query", args: `{"n":"%s"}`,
			refusal: gwG6("db_query"), object: repoObject("credential", "", ""), checked: []string{"gh-a-pay", "gh-b-site"}},
		{name: "reach credential allows every", user: "eve", tool: "db_query", args: `{"n":"%s"}`,
			object: repoObject("credential", "", ""), role: "gh-every"},
		{name: "reach none allows some repos", user: "pam", tool: "whoami", args: `{"n":"%s"}`, role: "gh-pay"},
		{name: "reach none allows no covers line", user: "old", tool: "whoami", args: `{"n":"%s"}`, role: "gh-bare"},
		{name: "drift refuses some repos", user: "pam", tool: "pr_merge", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			refusal: gwG7, object: repoObject("argument", "Strazahq/pay-api", "101"), checked: []string{"gh-pay"}},
		{name: "drift allows every", user: "eve", tool: "pr_merge", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			object: repoObject("argument", "Strazahq/pay-api", "101"), role: "gh-every"},
		{name: "an agent's own meta never reaches the server", user: "eve", tool: "pr_create", args: `{"repo":"Strazahq/site","n":"%s"}`,
			meta:   `{"ai.straza/object-id":"999","ai.straza/scope":"all"}`,
			object: repoObject("argument", "Strazahq/site", "103"), role: "gh-every"},
		{name: "an agent's own id loses to Straza's", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			meta:   `{"ai.straza/object-id":"999","ai.straza/scope":"all"}`,
			object: repoObject("argument", "Strazahq/pay-api", "101"), role: "gh-pay", sentMeta: map[string]any{objectIDMeta: "101"}},
	}
	r.runGateCases(t, "nonceg", cases)
}

// runGateCases fires every case on both endpoints, checking each answer
// and what the upstream got, then waits once for the records and checks
// that each call left exactly one.
func (r *ghRig) runGateCases(t *testing.T, prefix string, cases []gateCase) {
	t.Helper()
	type fired struct {
		name, nonce string
		tc          gateCase
	}
	var all []fired
	for _, ep := range ghEndpoints {
		for _, tc := range cases {
			f := fired{name: ep.name + "/" + tc.name, nonce: fmt.Sprintf("%s%03dz", prefix, len(all)), tc: tc}
			all = append(all, f)
			t.Run(f.name, func(t *testing.T) { r.fireGateCase(t, ep, tc, f.nonce) })
		}
	}
	recs := r.mcpRecords(t, prefix, len(all))
	for _, f := range all {
		t.Run(f.name+"/record", func(t *testing.T) {
			var mine []map[string]any
			for _, rec := range recs {
				if args, _ := rec["arguments"].(string); strings.Contains(args, f.nonce) {
					mine = append(mine, rec)
				}
			}
			if len(mine) != 1 {
				t.Fatalf("%d records for one call, want exactly one", len(mine))
			}
			r.checkGateRecord(t, f.tc, mine[0])
		})
	}
}

// fireGateCase fires tc on ep and checks the answer and the upstream.
func (r *ghRig) fireGateCase(t *testing.T, ep endpoint, tc gateCase, nonce string) {
	t.Helper()
	before := len(r.up.toolCalls(tc.tool))
	text, isErr := r.call(t, ep, tc.user, tc.tool, fmt.Sprintf(tc.args, nonce), tc.meta)
	calls := r.up.toolCalls(tc.tool)
	if tc.refusal != "" {
		if !isErr || text != tc.refusal {
			t.Errorf("answer = %q (error %v)\nwant %q", text, isErr, tc.refusal)
		}
		if len(calls) != before {
			t.Errorf("a refused call reached the upstream")
		}
	} else {
		if isErr || text != "done "+tc.tool {
			t.Fatalf("answer = %q (error %v), want the upstream's", text, isErr)
		}
		if len(calls) != before+1 {
			t.Fatalf("the upstream got %d calls of %s, want one more than %d", len(calls), tc.tool, before)
		}
		got := calls[len(calls)-1]
		if !reflect.DeepEqual(got.meta, tc.sentMeta) && (len(got.meta) != 0 || len(tc.sentMeta) != 0) {
			t.Errorf("upstream _meta = %v, want %v", got.meta, tc.sentMeta)
		}
		if tc.sentArgs != "" && got.args != fmt.Sprintf(tc.sentArgs, nonce) {
			t.Errorf("upstream arguments = %s\nwant %s", got.args, fmt.Sprintf(tc.sentArgs, nonce))
		}
		if tc.auth != "" && got.auth != tc.auth {
			t.Errorf("upstream Authorization = %q, want %q", got.auth, tc.auth)
		}
	}
}

// checkGateRecord checks the one record of tc's call.
func (r *ghRig) checkGateRecord(t *testing.T, tc gateCase, rec map[string]any) {
	t.Helper()
	want := map[string]any{"toolName": tc.tool, "event": "tool.pre", "tool": "mcp.call", "app": "ghapp"}
	if tc.refusal != "" {
		want["effect"], want["reason"], want["granted"], want["ruleId"], want["setName"] = "deny", tc.refusal, false, "", ""
	} else {
		want["effect"], want["granted"], want["role"], want["bindingId"] = "allow", true, tc.role, r.bindings[tc.role]
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("record %s = %v, want %v", k, rec[k], v)
		}
	}
	if u, _ := rec["user"].(string); u == "" {
		t.Error("the record names no actor")
	}
	if tc.refusal != "" {
		if _, ok := rec["role"]; ok {
			t.Errorf("a refusal's record names the role %v", rec["role"])
		}
		if _, ok := rec["bindingId"]; ok {
			t.Errorf("a refusal's record names the binding %v", rec["bindingId"])
		}
		if got := fmt.Sprint(rec["rolesChecked"]); got != fmt.Sprint(tc.checked) {
			t.Errorf("rolesChecked = %v, want %v", got, tc.checked)
		}
	} else if _, ok := rec["rolesChecked"]; ok {
		t.Errorf("an allowed call's record carries rolesChecked %v", rec["rolesChecked"])
	}
	if tc.credential != "" && rec["credentialId"] != r.creds[tc.credential] {
		t.Errorf("record credentialId = %v, want %s's %s", rec["credentialId"], tc.credential, r.creds[tc.credential])
	}
	gotObj, _ := rec["object"].(map[string]any)
	if !reflect.DeepEqual(gotObj, tc.object) && (gotObj != nil || tc.object != nil) {
		t.Errorf("record object = %v, want %v", rec["object"], tc.object)
	}
}

// TestGatewayObjectRename is acceptance case 14 on both endpoints: a
// renamed repo stays covered by its id, and its old value is not found.
func TestGatewayObjectRename(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	r.up.rename("101", "Strazahq/payments-api")
	if err := r.app.objects.MarkDue(context.Background(), r.appID); err != nil {
		t.Fatal(err)
	}
	r.waitList(t, func(l []string) bool { return len(l) == 3 && l[0] == "Strazahq/pay-web" })
	r.runGateCases(t, "noncer", []gateCase{
		{name: "the new value is covered by the id", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/payments-api","n":"%s"}`,
			object: repoObject("argument", "Strazahq/payments-api", "101"), role: "gh-pay", sentMeta: map[string]any{objectIDMeta: "101"}},
		{name: "the old value is not found", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			refusal: gwG1("Strazahq/pay-api", "Strazahq/pay-web and Strazahq/payments-api"),
			object:  repoObject("argument", "Strazahq/pay-api", ""), checked: []string{"gh-pay"}},
	})
}

// TestGatewayObjectSchemaDrift pins on both endpoints that a tool whose
// live input schema no longer requires its pinned argument as a string has
// drifted, though its mark still names that argument. A role on some repos
// is refused with the drift sentence in one deny record, so a key the gate
// never reads cannot carry another repo, and a role on every repo keeps
// calling the tool. The server's Repos tab, which the refusal points to,
// says why among the faults of what the marks declare.
func TestGatewayObjectSchemaDrift(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	faults := func() []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/apps/ghapp/objects", nil)
		req.SetPathValue("id", "ghapp")
		w := httptest.NewRecorder()
		r.app.handleAppObjects(w, req)
		var out objectsAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
			t.Fatalf("GET objects = %d %s", w.Code, w.Body)
		}
		return out.DeclaredFaults
	}
	schemaFault := func(tool, arg string) string {
		return tool + " has no argument " + arg + " that is a required top-level string in its input schema, so Straza cannot read the repo a call names. Name the argument that carries the repo, or pin the reach all."
	}
	if got, want := faults(), []string{schemaFault("pr_merge", "repository")}; !slices.Equal(got, want) {
		t.Errorf("faults before the new schema = %q, want %q", got, want)
	}
	r.up.reschema("pr_create", map[string]any{"type": "object",
		"properties": map[string]any{"repository": map[string]any{"type": "string"}}, "required": []any{"repository"}})
	renamed := func() bool {
		tools, err := r.app.manager.LiveInventory("ghapp")
		if err != nil {
			return false
		}
		for _, tl := range tools {
			if tl.Name == "pr_create" {
				s, _ := tl.InputSchema.(map[string]any)
				props, _ := s["properties"].(map[string]any)
				_, ok := props["repository"]
				return ok
			}
		}
		return false
	}
	for deadline := time.Now().Add(10 * time.Second); !renamed(); {
		if time.Now().After(deadline) {
			t.Fatal("ghapp's new pr_create schema never reached the inventory")
		}
		r.app.manager.HealthCheck(context.Background())
	}
	if got, want := faults(), []string{schemaFault("pr_create", "repo"), schemaFault("pr_merge", "repository")}; !slices.Equal(got, want) {
		t.Errorf("faults after the new schema = %q, want %q", got, want)
	}
	const drift = "Straza: pr_create on ghapp changed how it names its repo since an admin pinned it, so roles on some repos cannot use it until an admin publishes the change. Ask an admin to review ghapp's Repos tab."
	r.runGateCases(t, "noncex", []gateCase{
		{name: "a second key cannot carry another repo", user: "pam", tool: "pr_create",
			args:    `{"repo":"Strazahq/pay-api","repository":"Strazahq/site","n":"%s"}`,
			refusal: drift, object: repoObject("argument", "Strazahq/pay-api", "101"), checked: []string{"gh-pay"}},
		{name: "every keeps calling it", user: "eve", tool: "pr_create",
			args:   `{"repo":"Strazahq/pay-api","repository":"Strazahq/site","n":"%s"}`,
			object: repoObject("argument", "Strazahq/pay-api", "101"), role: "gh-every"},
	})
}

// TestGatewayObjectChecksOff is acceptance case 31 on both endpoints: with
// apps.objects.enabled off nothing is listed, a role on some repos loses
// the argument and credential tools, and a role on every repo keeps them.
func TestGatewayObjectChecksOff(t *testing.T) {
	t.Parallel()
	r := newGHRig(t, func(c *config.Config) { c.Apps.Objects.Enabled = false })
	r.runGateCases(t, "nonceo", []gateCase{
		{name: "some repos lose an argument tool", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			refusal: gwG8, object: repoObject("argument", "Strazahq/pay-api", ""), checked: []string{"gh-pay"}},
		{name: "some repos lose a credential tool", user: "pam", tool: "db_query", args: `{"n":"%s"}`,
			refusal: gwG6("db_query"), object: repoObject("credential", "", ""), checked: []string{"gh-pay"}},
		{name: "every keeps an argument tool", user: "eve", tool: "pr_create", args: `{"repo":"Strazahq/pay-api","n":"%s"}`,
			object: repoObject("argument", "Strazahq/pay-api", ""), role: "gh-every"},
		{name: "every keeps a credential tool", user: "eve", tool: "db_query", args: `{"n":"%s"}`,
			object: repoObject("credential", "", ""), role: "gh-every"},
		{name: "some repos keep a tool that reaches none", user: "pam", tool: "whoami", args: `{"n":"%s"}`, role: "gh-pay"},
	})
	user := seedIdentity(t, r.app)
	grantAdmin(t, r.app, user.ID)
	bearer := loginDeviceFlow(t, r.base, "kim", "hunter2!")
	var refused map[string]string
	const off = "Straza does not list objects on this deployment, because apps.objects.enabled is false. Turn it back on to list ghapp's repos."
	if code := adminReq(t, http.MethodPost, r.base+"/v1/admin/apps/ghapp/objects/refresh", bearer, nil, &refused); code != http.StatusConflict || refused["error"] != off {
		t.Errorf("refresh while off = %d %q, want 409 %q", code, refused["error"], off)
	}
	if code := adminReq(t, http.MethodPost, r.base+"/v1/admin/apps/ghapp/objects/credential", bearer, map[string]any{"value": "new-listing-tok"}, nil); code != http.StatusCreated {
		t.Errorf("setting the listing credential while off = %d, want 201", code)
	}
	if calls := r.up.toolCalls("repo_list"); len(calls) != 0 {
		t.Errorf("the checks are off and ghapp was listed %d times", len(calls))
	}
	if l := r.app.objects.Get(r.appID); l != nil {
		t.Errorf("the checks are off and the replica holds a list: %+v", l)
	}
}

// TestChangedRolesScope pins that a change of a binding's covers line alone
// drops the catalogs of its role, and a renamed object does not.
func TestChangedRolesScope(t *testing.T) {
	every := &store.ObjectScope{Every: true}
	some := &store.ObjectScope{Objects: []store.CoveredObject{{ID: "101", Value: "Strazahq/pay-api"}}}
	renamed := &store.ObjectScope{Objects: []store.CoveredObject{{ID: "101", Value: "Strazahq/payments-api"}}}
	other := &store.ObjectScope{Objects: []store.CoveredObject{{ID: "102", Value: "Strazahq/pay-api"}}}
	b := func(role string, s *store.ObjectScope) gwBinding {
		return gwBinding{ID: "b-" + role, App: "ghapp", Role: role, Matchers: []string{"*"}, Scope: s}
	}
	cases := []struct {
		name     string
		old, new *store.ObjectScope
		changed  bool
	}{
		{"no covers line to every", nil, every, true},
		{"every to some", every, some, true},
		{"some to other ids", some, other, true},
		{"a rename", some, renamed, false},
		{"the same", every, every, false},
	}
	for _, tc := range cases {
		got := changedRoles([]gwBinding{b("dev", tc.old)}, []gwBinding{b("dev", tc.new)})
		if got["dev"] != tc.changed || strings.Contains(fmt.Sprint(got), "ops") {
			t.Errorf("%s: changedRoles = %v, want dev changed %v", tc.name, got, tc.changed)
		}
	}
}

// TestGatewayObjectScopeSwap is the gateway half of acceptance case 22: a
// binding whose covers line alone changes swaps into the gateway and the
// next call decides by the new line, though the role's catalog was cached.
func TestGatewayObjectScopeSwap(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	ctx := context.Background()
	if _, isErr := r.call(t, ghEndpoints[0], "pam", "pr_create", `{"repo":"Strazahq/pay-web"}`, ""); isErr {
		t.Fatal("the covered call was refused before the swap")
	}
	if err := r.app.store.ToolBindings().Delete(ctx, r.bindings["gh-pay"]); err != nil {
		t.Fatal(err)
	}
	role, err := r.app.store.Roles().GetByName(ctx, "gh-pay")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: role.ID, AppID: r.appID, ToolMatcher: `["*"]`,
		Scope: &store.ObjectScope{Objects: []store.CoveredObject{{ID: "103", Value: "Strazahq/site"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	r.bindings["gh-pay"] = b.ID
	r.runGateCases(t, "nonces", []gateCase{
		{name: "the new line covers its repo", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/site","n":"%s"}`,
			object: repoObject("argument", "Strazahq/site", "103"), role: "gh-pay", sentMeta: map[string]any{objectIDMeta: "103"}},
		{name: "the old line's repo is refused", user: "pam", tool: "pr_create", args: `{"repo":"Strazahq/pay-web","n":"%s"}`,
			refusal: gwG1("Strazahq/pay-web", "Strazahq/site"), object: repoObject("argument", "Strazahq/pay-web", "102"), checked: []string{"gh-pay"}},
	})
}

// TestGatewayObjectMissRefresh is the refresh half of acceptance case 5: a
// call that names a repo the list does not hold lists ghapp again once the
// last listing is a minute old, and a second miss within the minute lists
// nothing.
func TestGatewayObjectMissRefresh(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	before := len(r.up.toolCalls("repo_list"))
	r.clock.advance(2 * time.Minute)
	if _, isErr := r.call(t, ghEndpoints[0], "pam", "pr_create", `{"repo":"Strazahq/new"}`, ""); !isErr {
		t.Fatal("a call on an unlisted repo ran")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(r.up.toolCalls("repo_list")) == before {
		if time.Now().After(deadline) {
			t.Fatal("a miss did not list ghapp again")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, isErr := r.call(t, ghEndpoints[1], "pam", "pr_create", `{"repo":"Strazahq/newer"}`, ""); !isErr {
		t.Fatal("a call on an unlisted repo ran")
	}
	time.Sleep(500 * time.Millisecond)
	if got := len(r.up.toolCalls("repo_list")); got != before+1 {
		t.Errorf("ghapp was listed %d times after two misses within a minute, want once", got-before)
	}
}

// TestCoveringRolesDrift pins that the Repos tab's coveredBy agrees with
// the gate: a role on some repos covers a repo only through a bound tool
// that names it and has not drifted, and a binding with no covers line
// covers nothing.
func TestCoveringRolesDrift(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	ctx := context.Background()
	for role, matcher := range map[string]string{"gh-merge": `["pr_merge","whoami"]`, "gh-create": `["pr_create"]`} {
		ro, err := r.app.store.Roles().Create(ctx, store.Role{Name: role, Kind: store.RoleKindApplication})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: r.appID, ToolMatcher: matcher,
			Scope: &store.ObjectScope{Objects: []store.CoveredObject{{ID: "101", Value: "Strazahq/pay-api"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	mf, err := managerParse(t, ghManifest(r.up.URL))
	if err != nil {
		t.Fatal(err)
	}
	got := r.app.coveringRoles("ghapp", *mf.Straza.Objects)("101")
	if want := []string{"gh-a-pay", "gh-create", "gh-every", "gh-pay"}; !slices.Equal(got, want) {
		t.Errorf("coveredBy of Strazahq/pay-api = %v, want %v", got, want)
	}
}
