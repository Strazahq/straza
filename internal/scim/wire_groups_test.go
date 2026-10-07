package scim

// Wire-groups behavior pins (spec/scim-profile revision 12, the unified
// role model): roles render as SCIM Groups, membership IS assignment,
// lifecycle verbs refuse with 501, every role is exposed, and the
// break-glass admin does not exist on this surface.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

func newPlanesTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(config.Config{
		Store: config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(t.TempDir(), "scim.db")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// wireGroupsServer builds a handler-capable Server over a real sqlite
// store; emitted identity ids are captured for the dual-emit pin.
func wireGroupsServer(t *testing.T) (*Server, store.Store, *[]string) {
	t.Helper()
	st := newPlanesTestStore(t)
	var emitted []string
	s := New(Deps{
		Store:        st,
		Authenticate: func(r *http.Request, _ string) (context.Context, int, string) { return r.Context(), 0, "" },
		Deactivate:   func(context.Context, string, string) {},
		Reactivate:   func(context.Context, string) {},
		IdentityChanged: func(_ context.Context, _ string, id string) {
			emitted = append(emitted, id)
		},
		ProtectedUsername: "break-glass",
	})
	return s, st, &emitted
}

func scimReq(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/scim+json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestWireGroupsRenderAndExposure: every role renders as a Group (id = role
// id, displayName = role name, members = direct holders), the control plane
// included, because the IdM masters membership everywhere, straza-admin's
// membership included.
func TestWireGroupsRenderAndExposure(t *testing.T) {
	ctx := context.Background()
	s, st, _ := wireGroupsServer(t)
	mux := http.NewServeMux()
	s.Routes(mux)

	dev, _ := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	admin, _ := st.Roles().Create(ctx, store.Role{Name: "straza-admin", Plane: store.RolePlaneControl})
	bob, _ := st.Users().Create(ctx, store.User{Username: "bob"})
	if _, err := st.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: bob.ID, RoleID: dev.ID, Origin: store.OriginSCIM,
	}); err != nil {
		t.Fatal(err)
	}

	rec := scimReq(t, mux, "GET", "/scim/v2/Groups", "")
	var list struct {
		Total     int              `json:"totalResults"`
		Resources []map[string]any `json:"Resources"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("list = %+v, want dev AND straza-admin (all roles exposed)", list)
	}
	byName := map[string]map[string]any{}
	for _, res := range list.Resources {
		name, _ := res["displayName"].(string)
		byName[name] = res
	}
	if byName["dev"] == nil || byName["dev"]["id"] != dev.ID {
		t.Fatalf("list = %+v, want the dev role", list)
	}
	members, _ := byName["dev"]["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("members = %v, want bob", byName["dev"]["members"])
	}

	// Control plane renders and accepts membership like any other role.
	if rec := scimReq(t, mux, "GET", "/scim/v2/Groups/"+admin.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("control-plane GET = %d, want 200", rec.Code)
	}
	patch := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"` + bob.ID + `"}]}]}`
	if rec := scimReq(t, mux, "PATCH", "/scim/v2/Groups/"+admin.ID, patch); rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Errorf("control-plane membership PATCH = %d, want 2xx", rec.Code)
	}
	if rec := scimReq(t, mux, "GET", `/scim/v2/Groups?filter=displayName+eq+%22straza-admin%22`, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"totalResults":1`) {
		t.Errorf("control-plane filter must find the role, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestWireGroupsLifecycleRefusals: create and delete answer 501 (not 403:
// RFC 7644 §3.12 makes 403 an authorization statement and IdM runbooks map
// it to broken credentials); displayName is readOnly via PATCH; the
// externalId filter retired.
func TestWireGroupsLifecycleRefusals(t *testing.T) {
	ctx := context.Background()
	s, st, _ := wireGroupsServer(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	dev, _ := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})

	if rec := scimReq(t, mux, "POST", "/scim/v2/Groups", `{"displayName":"new-team"}`); rec.Code != http.StatusNotImplemented {
		t.Errorf("POST = %d, want 501", rec.Code)
	}
	if rec := scimReq(t, mux, "DELETE", "/scim/v2/Groups/"+dev.ID, ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("DELETE = %d, want 501", rec.Code)
	}
	rename := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"renamed"}]}`
	rec := scimReq(t, mux, "PATCH", "/scim/v2/Groups/"+dev.ID, rename)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "mutability") {
		t.Errorf("rename = %d %s, want 400 mutability", rec.Code, rec.Body.String())
	}
	if rec := scimReq(t, mux, "GET", `/scim/v2/Groups?filter=externalId+eq+%22x%22`, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("externalId filter = %d, want 400 invalidFilter", rec.Code)
	}
}

// TestWireGroupsMembershipIsAssignment: members ops maintain THE (user,
// role) assignment: add creates scim-origin, re-add is idempotent, remove
// deletes the row WHATEVER origin wrote it (single-writer doctrine: a
// console grant of an exported role is drift the IdM reconciles away),
// replace converges, and both the role id and each affected user id are
// emitted (LiveSync re-reads the render; the account shadow refreshes).
func TestWireGroupsMembershipIsAssignment(t *testing.T) {
	ctx := context.Background()
	s, st, emitted := wireGroupsServer(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	dev, _ := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	bob, _ := st.Users().Create(ctx, store.User{Username: "bob"})
	kim, _ := st.Users().Create(ctx, store.User{Username: "kim"})

	holders := func() map[string]string {
		out := map[string]string{}
		asg, err := st.Roles().AssignmentsByRole(ctx, dev.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range asg {
			out[a.SubjectID] = a.Origin
		}
		return out
	}
	patch := func(body string) *httptest.ResponseRecorder {
		return scimReq(t, mux, "PATCH", "/scim/v2/Groups/"+dev.ID,
			`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":`+body+`}`)
	}

	// add creates the scim-origin assignment; re-add is a no-op.
	for i := 0; i < 2; i++ {
		if rec := patch(`[{"op":"add","path":"members","value":[{"value":"` + bob.ID + `"}]}]`); rec.Code != http.StatusOK {
			t.Fatalf("add(#%d) = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	if h := holders(); len(h) != 1 || h[bob.ID] != store.OriginSCIM {
		t.Fatalf("holders after add = %v, want bob scim-origin", h)
	}
	// The first add emitted role + user; the idempotent one only the role.
	if len(*emitted) != 3 || (*emitted)[0] != dev.ID || (*emitted)[1] != bob.ID || (*emitted)[2] != dev.ID {
		t.Errorf("emitted = %v, want [dev bob dev]", *emitted)
	}

	// A console (admin-origin) grant renders as a member and is removable
	// over the wire: SCIM is the single writer for exported-role holding.
	if _, err := st.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: dev.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := patch(`[{"op":"remove","path":"members[value eq \"` + kim.ID + `\"]"}]`); rec.Code != http.StatusOK {
		t.Fatalf("value-filter remove = %d: %s", rec.Code, rec.Body.String())
	}
	if h := holders(); len(h) != 1 || h[bob.ID] == "" {
		t.Fatalf("holders after admin-origin remove = %v, want bob only", h)
	}

	// replace converges to the desired set.
	if rec := patch(`[{"op":"replace","path":"members","value":[{"value":"` + kim.ID + `"}]}]`); rec.Code != http.StatusOK {
		t.Fatalf("replace = %d: %s", rec.Code, rec.Body.String())
	}
	if h := holders(); len(h) != 1 || h[kim.ID] != store.OriginSCIM {
		t.Fatalf("holders after replace = %v, want kim only", h)
	}

	// An unknown member id refuses the whole request (no partial write).
	if rec := patch(`[{"op":"replace","path":"members","value":[{"value":"nope"}]}]`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown member replace = %d, want 400", rec.Code)
	}
	if h := holders(); len(h) != 1 || h[kim.ID] == "" {
		t.Errorf("holders after refused replace = %v, want kim untouched", h)
	}
}

// TestWireGroupsTolerantPUT: PUT is a members replace that IGNORES the
// name fields (Okta sends the full document, displayName included, on
// every membership update; erroring would break membership entirely).
func TestWireGroupsTolerantPUT(t *testing.T) {
	ctx := context.Background()
	s, st, _ := wireGroupsServer(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	dev, _ := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	bob, _ := st.Users().Create(ctx, store.User{Username: "bob"})

	body := `{"schemas":["` + SchemaGroup + `"],"displayName":"Okta Developers","externalId":"x1","members":[{"value":"` + bob.ID + `"}]}`
	rec := scimReq(t, mux, "PUT", "/scim/v2/Groups/"+dev.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["displayName"] != "dev" {
		t.Errorf("displayName after PUT = %v, want the server-truth role name", out["displayName"])
	}
	role, _ := st.Roles().GetByID(ctx, dev.ID)
	if role.Name != "dev" {
		t.Errorf("role renamed by PUT to %q: names never change over this wire", role.Name)
	}
	asg, _ := st.Roles().AssignmentsByRole(ctx, dev.ID)
	if len(asg) != 1 || asg[0].SubjectID != bob.ID {
		t.Errorf("PUT members not applied: %+v", asg)
	}
}

// captureMembership wires the audit seam of a test server to a slice, in
// call order.
func captureMembership(s *Server) *[]MembershipChange {
	var out []MembershipChange
	s.deps.MembershipChanged = func(_ context.Context, c MembershipChange) { out = append(out, c) }
	return &out
}

// TestWireGroupsMembershipChangeOrder: every Group write reports each
// assignment it started or ended through Deps.MembershipChanged, once, in
// write order, with the row id and direction, and a write that changes
// nothing reports nothing. The identity events are unchanged (pinned by
// TestWireGroupsMembershipIsAssignment); the other tests run with a nil
// seam, which pins that the seam is optional.
func TestWireGroupsMembershipChangeOrder(t *testing.T) {
	ctx := context.Background()
	s, st, _ := wireGroupsServer(t)
	changes := captureMembership(s)
	mux := http.NewServeMux()
	s.Routes(mux)
	dev, _ := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	bob, _ := st.Users().Create(ctx, store.User{Username: "bob"})
	kim, _ := st.Users().Create(ctx, store.User{Username: "kim"})
	sam, _ := st.Users().Create(ctx, store.User{Username: "sam"})

	holders := func() map[string]string {
		out := map[string]string{}
		asg, err := st.Roles().AssignmentsByRole(ctx, dev.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range asg {
			out[a.SubjectID] = a.ID
		}
		return out
	}
	patchBody := func(ops string) string {
		return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":` + ops + `}`
	}
	type want struct{ user, action string }
	steps := []struct {
		name, method, body string
		want               []want
	}{
		{"PATCH add two members", "PATCH",
			patchBody(`[{"op":"add","path":"members","value":[{"value":"` + bob.ID + `"},{"value":"` + kim.ID + `"}]}]`),
			[]want{{bob.ID, MembershipAssign}, {kim.ID, MembershipAssign}}},
		{"repeated add reports nothing", "PATCH",
			patchBody(`[{"op":"add","path":"members","value":[{"value":"` + bob.ID + `"},{"value":"` + kim.ID + `"}]}]`),
			nil},
		{"PATCH value-filter remove", "PATCH",
			patchBody(`[{"op":"remove","path":"members[value eq \"` + bob.ID + `\"]"}]`),
			[]want{{bob.ID, MembershipUnassign}}},
		{"PUT replace ends kim and starts sam", "PUT",
			`{"schemas":["` + SchemaGroup + `"],"displayName":"dev","members":[{"value":"` + sam.ID + `"}]}`,
			[]want{{kim.ID, MembershipUnassign}, {sam.ID, MembershipAssign}}},
		{"PUT of the current set reports nothing", "PUT",
			`{"schemas":["` + SchemaGroup + `"],"displayName":"dev","members":[{"value":"` + sam.ID + `"}]}`,
			nil},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			before, seen := holders(), len(*changes)
			if rec := scimReq(t, mux, step.method, "/scim/v2/Groups/"+dev.ID, step.body); rec.Code != http.StatusOK {
				t.Fatalf("%s = %d: %s", step.method, rec.Code, rec.Body.String())
			}
			after, got := holders(), (*changes)[seen:]
			if len(got) != len(step.want) {
				t.Fatalf("changes = %+v, want %v", got, step.want)
			}
			for i, w := range step.want {
				c := got[i]
				rowID := after[w.user]
				if w.action == MembershipUnassign {
					rowID = before[w.user]
				}
				if c.UserID != w.user || c.Action != w.action || c.RoleID != dev.ID ||
					c.AssignmentID == "" || c.AssignmentID != rowID || c.Origin != store.OriginSCIM {
					t.Errorf("change[%d] = %+v, want user %s %s of role %s on row %s origin scim", i, c, w.user, w.action, dev.ID, rowID)
				}
			}
		})
	}
}

// TestWireGroupsUserReflection: the User `groups` attribute reflects the
// user's DIRECT assignments of exported roles (value = role id, display =
// role name), control-plane holdings included.
func TestWireGroupsUserReflection(t *testing.T) {
	ctx := context.Background()
	s, st, _ := wireGroupsServer(t)
	dev, _ := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	admin, _ := st.Roles().Create(ctx, store.Role{Name: "straza-admin", Plane: store.RolePlaneControl})
	bob, _ := st.Users().Create(ctx, store.User{Username: "bob"})
	for _, roleID := range []string{dev.ID, admin.ID} {
		if _, err := st.Roles().Assign(ctx, store.RoleAssignment{
			SubjectKind: store.SubjectUser, SubjectID: bob.ID, RoleID: roleID, Origin: store.OriginSCIM,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Every holding reflects, control plane included.
	res := s.userResourceCtx(ctx, bob)
	groups, ok := res["groups"].([]map[string]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("groups = %#v, want dev AND straza-admin reflections", res["groups"])
	}
	byDisplay := map[string]map[string]any{}
	for _, g := range groups {
		display, _ := g["display"].(string)
		byDisplay[display] = g
	}
	if g := byDisplay["dev"]; g == nil || g["value"] != dev.ID || g["type"] != "direct" {
		t.Errorf("dev reflection rendered wrong: %#v", byDisplay["dev"])
	}
	if g := byDisplay["straza-admin"]; g == nil || g["value"] != admin.ID {
		t.Errorf("straza-admin reflection rendered wrong: %#v", byDisplay["straza-admin"])
	}

	// A user with no exported holdings omits the attribute.
	carol, _ := st.Users().Create(ctx, store.User{Username: "carol"})
	if _, present := s.userResourceCtx(ctx, carol)["groups"]; present {
		t.Error("holder of nothing should omit groups")
	}
}

// TestWireGroupsBreakGlassInvisible: the break-glass admin does not exist
// on this surface: not in user lists, 404 by id, never a member, not
// assignable, and the username filter answers empty.
func TestWireGroupsBreakGlassInvisible(t *testing.T) {
	ctx := context.Background()
	s, st, _ := wireGroupsServer(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	dev, _ := st.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindBusiness})
	bg, _ := st.Users().Create(ctx, store.User{Username: "break-glass"})
	if _, err := st.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: bg.ID, RoleID: dev.ID,
	}); err != nil {
		t.Fatal(err)
	}

	if rec := scimReq(t, mux, "GET", "/scim/v2/Users", ""); strings.Contains(rec.Body.String(), "break-glass") {
		t.Error("break-glass rendered in the users list")
	}
	if rec := scimReq(t, mux, "GET", "/scim/v2/Users/"+bg.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("break-glass by id = %d, want 404", rec.Code)
	}
	if rec := scimReq(t, mux, "DELETE", "/scim/v2/Users/"+bg.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("break-glass DELETE = %d, want 404 (cannot be deactivated over SCIM)", rec.Code)
	}
	if rec := scimReq(t, mux, "GET", `/scim/v2/Users?filter=userName+eq+%22break-glass%22`, ""); !strings.Contains(rec.Body.String(), `"totalResults":0`) {
		t.Errorf("break-glass filter must answer empty: %s", rec.Body.String())
	}
	if rec := scimReq(t, mux, "GET", "/scim/v2/Groups/"+dev.ID, ""); strings.Contains(rec.Body.String(), bg.ID) {
		t.Error("break-glass rendered as a member")
	}
	add := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"` + bg.ID + `"}]}]}`
	if rec := scimReq(t, mux, "PATCH", "/scim/v2/Groups/"+dev.ID, add); rec.Code != http.StatusBadRequest {
		t.Errorf("break-glass member add = %d, want 400 unknown user", rec.Code)
	}
}
