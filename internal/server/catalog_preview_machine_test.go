package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestCatalogPreviewMachineServers pins the preview's servers on a person's
// machine: a server the subject's policies name that Straza has not
// registered is listed with the tools the rules name whole and the outcome
// the engine gives each and the policies that name it, on the user lane
// and on the role lane. A registered server is not listed, whichever way
// it is registered: a row in the store, an instance that still runs after
// its row was removed, or an access row that still names it. Straza's own
// server and a server only another role's policy names are not listed
// either. A tool that a rule with no apps list names is a row of every
// listed server.
func TestCatalogPreviewMachineServers(t *testing.T) {
	t.Parallel()
	app, _, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "regapp", []string{"*"})
	liveOnly := catInstallEcho(t, app, up, "liveonly", nil)
	if err := app.store.Apps().SoftDelete(ctx, liveOnly.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Apps().Create(ctx, store.App{Name: "pausedapp", Version: "1.0.0", RuntimeKind: "remote", Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	ghost, err := app.store.Apps().Create(ctx, store.App{Name: "ghostapp", Version: "1.0.0", RuntimeKind: "remote", Status: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: devRole.ID, AppID: ghost.ID, ToolMatcher: `["*"]`}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.store.Apps().SoftDelete(ctx, ghost.ID); err != nil {
		t.Fatal(err)
	}
	catActivate(t, app, "dev-machines", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: dev-machines}
spec:
  match: {roles: [dev]}
  rules:
    - id: speak
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames: {allow: ["text_to_speech"]}
      effect: allow
    - id: clone-needs-yes
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames: {allow: ["voice_clone"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
    - id: no-deletes
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames: {deny: ["delete_voice", "purge_*"]}
      effect: deny
      reason: "deleting a voice is not for agents"
    - id: registered-too
      tools: [mcp.call]
      apps: [regapp, liveonly, ghostapp, pausedapp, straza]
      toolNames: {allow: ["echo"]}
      effect: allow
    - id: judged
      tools: [mcp.call]
      apps: [elevenlabs]
      toolNames: {allow: ["compose_music"]}
      effect: allow
      mode: classify
    - id: whole-server
      tools: [mcp.call]
      apps: [scratchpad]
      effect: deny
      reason: "the scratchpad server is off limits"
`)
	catActivate(t, app, "dev-floor", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: dev-floor}
spec:
  match: {roles: [dev]}
  rules:
    - id: voices-anywhere
      tools: [mcp.call]
      toolNames: {allow: ["search_voices"]}
      effect: allow
`)
	// A policy that lists more tools of a registered server than one answer
	// holds must not cost the servers on a person's machine their rows.
	many := make([]string, 2100)
	for i := range many {
		many[i] = fmt.Sprintf("tool_%04d", i)
	}
	catActivate(t, app, "dev-registered", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: dev-registered}
spec:
  match: {roles: [dev]}
  rules:
    - id: many-registered
      tools: [mcp.call]
      apps: [regapp]
      toolNames: {allow: ["`+strings.Join(many, `", "`)+`"]}
      effect: allow
`)
	catActivate(t, app, "others-machines", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: others-machines}
spec:
  match: {roles: [someone-else]}
  rules:
    - id: theirs
      tools: [mcp.call]
      apps: [their-server]
      toolNames: {allow: ["read_all"]}
      effect: allow
`)

	get := func(target string) catalogPreviewResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("preview %s = %d: %s", target, rec.Code, rec.Body)
		}
		var resp catalogPreviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	type row struct{ tool, status, set, rule, hint string }
	rows := func(m previewMachine) []row {
		out := make([]row, len(m.Tools))
		for i, e := range m.Tools {
			if e.App != m.App {
				t.Errorf("row %+v is under %s", e, m.App)
			}
			out[i] = row{e.Tool, e.Status, e.SetName, e.RuleID, e.Hint}
		}
		return out
	}
	wantElevenlabs := []row{
		{"compose_music", previewVisible, "dev-machines", "judged", "allowed on the person's machine by dev-machines, policy rule judged, when the classifier passes the call"},
		{"delete_voice", previewHiddenPolicy, "dev-machines", "no-deletes", "deleting a voice is not for agents"},
		{"search_voices", previewVisible, "dev-floor", "voices-anywhere", "allowed on the person's machine by dev-floor, policy rule voices-anywhere"},
		{"text_to_speech", previewVisible, "dev-machines", "speak", "allowed on the person's machine by dev-machines, policy rule speak"},
		{"voice_clone", previewApproveGated, "dev-machines", "clone-needs-yes", "needs approval by sec-approvers on the person's machine: dev-machines, policy rule clone-needs-yes"},
	}
	wantScratchpad := []row{
		{"search_voices", previewHiddenPolicy, "dev-machines", "whole-server", "the scratchpad server is off limits"},
	}

	for _, target := range []string{"/v1/admin/catalog/preview?user=kim", "/v1/admin/catalog/preview?role=dev"} {
		t.Run(target, func(t *testing.T) {
			resp := get(target)
			names := make([]string, len(resp.Machine))
			for i, m := range resp.Machine {
				names[i] = m.App
			}
			if !reflect.DeepEqual(names, []string{"elevenlabs", "scratchpad"}) {
				t.Fatalf("servers on a person's machine = %v, want elevenlabs and scratchpad", names)
			}
			el, sp := resp.Machine[0], resp.Machine[1]
			if got := rows(el); !reflect.DeepEqual(got, wantElevenlabs) || !el.Open {
				t.Errorf("elevenlabs = %+v open %v, want %+v and open for the pattern", got, el.Open, wantElevenlabs)
			}
			if el.Tools[1].Reason != "deleting a voice is not for agents" {
				t.Errorf("the denied tool's reason = %q, want the rule's own", el.Tools[1].Reason)
			}
			if got := rows(sp); !reflect.DeepEqual(got, wantScratchpad) || !sp.Open {
				t.Errorf("scratchpad = %+v open %v, want %+v and open for the rule that names no tool", got, sp.Open, wantScratchpad)
			}
			for _, m := range resp.Machine {
				if !reflect.DeepEqual(m.Policies, []string{"dev-machines"}) {
					t.Errorf("%s is named by %v, want dev-machines alone", m.App, m.Policies)
				}
			}
			for _, e := range resp.Entries {
				if e.App == "elevenlabs" || e.App == "scratchpad" {
					t.Errorf("a server on a person's machine is among the registered rows: %+v", e)
				}
			}
		})
	}

	t.Run("the app filter", func(t *testing.T) {
		resp := get("/v1/admin/catalog/preview?user=kim&app=elevenlabs")
		if len(resp.Machine) != 1 || resp.Machine[0].App != "elevenlabs" {
			t.Fatalf("filtered to elevenlabs = %+v", resp.Machine)
		}
		if got := rows(resp.Machine[0]); !reflect.DeepEqual(got, wantElevenlabs) {
			t.Errorf("elevenlabs alone = %+v, want %+v", got, wantElevenlabs)
		}
		if resp := get("/v1/admin/catalog/preview?user=kim&app=regapp"); len(resp.Machine) != 0 {
			t.Fatalf("filtered to a registered server = %+v, want none", resp.Machine)
		}
	})

	t.Run("a subject whose policies name none", func(t *testing.T) {
		rec := httptest.NewRecorder()
		app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/catalog/preview?role=nobody-holds-this", nil))
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("preview = %d %s, %v", rec.Code, rec.Body, err)
		}
		if _, present := raw["machine"]; present {
			t.Errorf("machine = %s, want the member left out", raw["machine"])
		}
	})
}
