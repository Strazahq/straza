package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// The rule blocks of the sets the role delete tests store: a deny rule, an
// allow rule alone, and an allow rule under conversation capture.
const (
	denyRules = `  rules:
    - id: no-shell
      events: [tool.pre]
      tools: [shell.exec]
      effect: deny
      reason: "Straza: no shell"
`
	allowRules = `  rules:
    - id: ok-read
      events: [tool.pre]
      tools: [file.read]
      effect: allow
`
	captureAllowRules = "  capture: {conversations: true, mode: redact}\n" + allowRules
)

// roleSetText is the text of a policy set named name over the match block
// match with the rule block rules.
func roleSetText(name, match, rules string) string {
	return fmt.Sprintf("apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: %s}\nspec:\n  priority: 10\n  match: %s\n%s", name, match, rules)
}

// roleSet is the text of a live policy set named name over the match
// block match with a deny rule, which a role delete reads for the roles
// it names.
func roleSet(name, match string) string {
	return roleSetText(name, match, denyRules)
}

// policyRecords counts the straza.audit.admin records with action on the
// set name and answers the last one.
func policyRecords(t *testing.T, app *App, action, name string) (int, map[string]any) {
	t.Helper()
	n := 0
	var last map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == action && ev["name"] == name {
			n++
			last = ev
		}
	}
	return n, last
}

// discardRecords counts the draft.discard records whose reason holds want
// and answers the last one.
func discardRecords(t *testing.T, app *App, want string) (int, map[string]any) {
	t.Helper()
	n := 0
	var last map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if reason, _ := ev["reason"].(string); ev["action"] == "draft.discard" && strings.Contains(reason, want) {
			n++
			last = ev
		}
	}
	return n, last
}

// roleDeleteRig is a test app for the role delete tests: kim is root, and
// ida holds identity:read and identity:write through role-managers and
// cannot read policy sets.
type roleDeleteRig struct {
	app       *App
	base      string
	root, ida string
}

func newRoleDeleteRig(t *testing.T, mutators ...func(*config.Config)) *roleDeleteRig {
	t.Helper()
	app, base := testApp(t, append([]func(*config.Config){func(c *config.Config) {
		c.Admin.RoleAreas = map[string][]string{"role-managers": {"identity:read", "identity:write"}}
	}}, mutators...)...)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	if _, err := app.store.Roles().Create(context.Background(), store.Role{Name: "role-managers"}); err != nil {
		t.Fatal(err)
	}
	mkHuman(t, app, "ida", "role-managers")
	return &roleDeleteRig{app: app, base: base, root: loginDeviceFlow(t, base, "kim", "hunter2!"), ida: loginDeviceFlow(t, base, "ida", "hunter2!")}
}

// createRole makes name an application role as root.
func (rig *roleDeleteRig) createRole(t *testing.T, name string) rolePayload {
	t.Helper()
	var role rolePayload
	if code := adminReq(t, http.MethodPost, rig.base+"/v1/admin/roles", rig.root, map[string]string{"name": name, "kind": "application"}, &role); code != http.StatusCreated {
		t.Fatalf("create %s = %d", name, code)
	}
	return role
}

// liveSet stores text as root and turns the set on.
func (rig *roleDeleteRig) liveSet(t *testing.T, name, text string) {
	t.Helper()
	if code, out, _ := adminBytes(t, http.MethodPut, rig.base+"/v1/admin/policies", rig.root, "application/yaml", []byte(text)); code != http.StatusCreated {
		t.Fatalf("store %s = %d %s", name, code, out)
	}
	if code := adminReq(t, http.MethodPost, rig.base+"/v1/admin/policies/"+name+"/activate", rig.root, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate %s = %d", name, code)
	}
}

// deleteRole deletes the role as tok and answers the status and the
// decoded answer.
func (rig *roleDeleteRig) deleteRole(t *testing.T, tok string, role rolePayload) (int, roleDeleted) {
	t.Helper()
	var out roleDeleted
	code := adminReq(t, http.MethodDelete, rig.base+"/v1/admin/roles/"+role.ID, tok, nil, &out)
	return code, out
}

// TestRolesDeleteTurnsOffTheSetsOnlyItMatches pins that deleting a role
// turns off, in the same publish, every live policy set whose match.roles
// names the role and no other live role, since such a set would match
// nobody once the role is gone: the answer names those sets to every
// caller, each writes one policy.deactivate record naming the caller, an
// open saved edit of the set is closed with the reason recorded, and
// the set's later delete answers 204 as for any set that is off. A set
// that names another live role, or matches users alone, stays live, and
// its delete still answers 409.
func TestRolesDeleteTurnsOffTheSetsOnlyItMatches(t *testing.T) {
	t.Parallel()
	rig := newRoleDeleteRig(t)
	rig.createRole(t, "other")

	cases := []struct {
		name      string
		role      string
		match     string
		tok       string
		actor     string
		savedEdit bool
		wantOff   bool
	}{
		{"a set matching only the role is turned off", "gated", "{roles: [gated]}", rig.root, "kim", false, true},
		{"a set that names another live role stays live", "shared", "{roles: [shared, other]}", rig.root, "kim", false, false},
		{"a set matching users alone stays live", "userbound", "{users: [kim]}", rig.root, "kim", false, false},
		{"a caller who cannot read policy sets turns the set off and reads its name", "unread", "{roles: [unread]}", rig.ida, "ida", false, true},
		{"a set with an open saved edit is turned off and the edit is closed", "edited", "{roles: [edited]}", rig.root, "kim", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := rig.createRole(t, tc.role)
			set := tc.role + "-access"
			rig.liveSet(t, set, roleSet(set, tc.match))
			if tc.savedEdit {
				edit := strings.Replace(roleSet(set, tc.match), "priority: 10", "priority: 11", 1)
				if code, out, _ := adminBytes(t, http.MethodPut, rig.base+"/v1/admin/policies", rig.root, "application/yaml", []byte(edit)); code != http.StatusOK {
					t.Fatalf("save an edit of %s = %d %s", set, code, out)
				}
				if _, _, err := rig.app.store.Drafts().BySlot(context.Background(), "policy:"+set); err != nil {
					t.Fatalf("the saved edit of %s is not open: %v", set, err)
				}
			}
			code, out := rig.deleteRole(t, tc.tok, role)
			if code != http.StatusOK || out.Status != "deleted" {
				t.Fatalf("delete %s = %d %+v, want 200 deleted", tc.role, code, out)
			}
			var wantNamed []string
			if tc.wantOff {
				wantNamed = []string{set}
			}
			if !slices.Equal(out.SetsOff, wantNamed) {
				t.Errorf("the answer names the sets %q, want %q", out.SetsOff, wantNamed)
			}
			wantStatus, wantDelete, wantRecords := "active", http.StatusConflict, 0
			if tc.wantOff {
				wantStatus, wantDelete, wantRecords = "draft", http.StatusNoContent, 1
			}
			if row := rowOf(t, rig.app, set); row.Status != wantStatus {
				t.Errorf("%s reads %s after the delete, want %s", set, row.Status, wantStatus)
			}
			if n, ev := policyRecords(t, rig.app, "policy.deactivate", set); n != wantRecords || (n == 1 && ev["actor"] != tc.actor) {
				t.Errorf("policy.deactivate records for %s = %d, last %v; want %d by %s", set, n, ev, wantRecords, tc.actor)
			}
			if tc.savedEdit {
				reason := set + " was turned off with the role " + tc.role + ", the only role it matched, and it keeps its published text while it is off"
				if _, _, err := rig.app.store.Drafts().BySlot(context.Background(), "policy:"+set); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("the saved edit of %s is still open after the delete, %v; want it closed", set, err)
				}
				if n, ev := discardRecords(t, rig.app, reason); n != 1 || ev["actor"] != tc.actor {
					t.Errorf("draft.discard records with the reason %q = %d, last %v; want one by %s", reason, n, ev, tc.actor)
				}
			}
			if code, body, _ := adminBytes(t, http.MethodDelete, rig.base+"/v1/admin/policies/"+set, rig.root, "", nil); code != wantDelete {
				t.Errorf("delete %s after the role = %d %s, want %d", set, code, body, wantDelete)
			}
		})
	}
}

// TestRolesDeleteUnderSecondPersonJudgesTheBareDelete pins that under
// admin.secondPerson the second-person rule judges the role removal
// alone: a set the delete turns off adds no second-person need, so a role
// whose own set records conversations or also selects by user is deleted
// with 200 by a caller who cannot read policy sets, and the set is turned
// off with it.
func TestRolesDeleteUnderSecondPersonJudgesTheBareDelete(t *testing.T) {
	t.Parallel()
	rig := newRoleDeleteRig(t)
	cases := []struct {
		name, role, match, rules string
	}{
		{"a set that records conversations", "recorded", "{roles: [recorded]}", captureAllowRules},
		{"a set that also selects by user", "picked", "{roles: [picked], users: [kim]}", allowRules},
	}
	// The sets go live before the rule is on, since turning such a set on
	// is itself a change the rule holds for a second person.
	roles := map[string]rolePayload{}
	for _, tc := range cases {
		roles[tc.role] = rig.createRole(t, tc.role)
		rig.liveSet(t, tc.role+"-access", roleSetText(tc.role+"-access", tc.match, tc.rules))
	}
	rig.app.cfg.Admin.SecondPerson = true
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role, set := roles[tc.role], tc.role+"-access"
			code, out := rig.deleteRole(t, rig.ida, role)
			if code != http.StatusOK || out.Status != "deleted" || !slices.Equal(out.SetsOff, []string{set}) {
				t.Fatalf("ida deletes %s under secondPerson = %d %+v, want 200 deleted naming %s", tc.role, code, out, set)
			}
			if row := rowOf(t, rig.app, set); row.Status != "draft" {
				t.Errorf("%s reads %s after the delete, want draft", set, row.Status)
			}
			if n, ev := policyRecords(t, rig.app, "policy.deactivate", set); n != 1 || ev["actor"] != "ida" {
				t.Errorf("policy.deactivate records for %s = %d, last %v; want one by ida", set, n, ev)
			}
		})
	}
}
