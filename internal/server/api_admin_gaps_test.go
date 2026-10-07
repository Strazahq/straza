package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUsersDeleteAdmin pins DELETE /v1/admin/users/{id}: the row is
// soft-deleted (gone from the admin list and every store getter), the delete
// doubles as a kill switch (revocation row + in-memory denylist), and
// both a re-delete and a made-up id are a clean 404.
func TestUsersDeleteAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	doomed, err := app.store.Users().Create(ctx, store.User{Username: "doomed"})
	if err != nil {
		t.Fatal(err)
	}

	var out map[string]string
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+doomed.ID, adminTok, nil, &out); code != http.StatusOK || out["status"] != "deleted" {
		t.Fatalf("delete = %d %v", code, out)
	}
	// Soft delete: invisible to the admin list and to store getters.
	var users []userPayload
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users", adminTok, nil, &users); code != http.StatusOK {
		t.Fatalf("list users = %d", code)
	}
	for _, u := range users {
		if u.ID == doomed.ID {
			t.Fatalf("deleted user still listed: %+v", u)
		}
	}
	if _, err := app.store.Users().GetByID(ctx, doomed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetByID after delete = %v, want ErrNotFound", err)
	}
	// The kill-switch cascade fired: persisted revocation + denylist entry.
	revs, err := app.store.Revocations().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundRev := false
	for _, rv := range revs {
		if rv.Kind == store.RevokeUser && rv.TargetID == doomed.ID {
			foundRev = true
		}
	}
	if !foundRev {
		t.Errorf("no user revocation recorded for deleted user: %+v", revs)
	}
	if !app.denylist.userBlocked(doomed.ID) {
		t.Error("deleted user not on the in-memory denylist")
	}
	for _, id := range []string{doomed.ID, "no-such-user"} {
		if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+id, adminTok, nil, nil); code != http.StatusNotFound {
			t.Errorf("delete %q = %d, want 404", id, code)
		}
	}
}

// TestRolesDeleteAdmin pins DELETE /v1/admin/roles/{id}: a created role
// disappears from the list, and both a re-delete and an unknown id 404.
func TestRolesDeleteAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	var role rolePayload
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles", adminTok, map[string]string{"name": "ephemeral"}, &role); code != http.StatusCreated {
		t.Fatalf("create role = %d", code)
	}
	var out map[string]string
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/roles/"+role.ID, adminTok, nil, &out); code != http.StatusOK || out["status"] != "deleted" {
		t.Fatalf("delete role = %d %v", code, out)
	}
	var roles []rolePayload
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/roles", adminTok, nil, &roles); code != http.StatusOK {
		t.Fatalf("list roles = %d", code)
	}
	for _, r := range roles {
		if r.ID == role.ID {
			t.Fatalf("deleted role still listed: %+v", r)
		}
	}
	for _, id := range []string{role.ID, "no-such-role"} {
		if code := adminReq(t, http.MethodDelete, base+"/v1/admin/roles/"+id, adminTok, nil, nil); code != http.StatusNotFound {
			t.Errorf("delete %q = %d, want 404", id, code)
		}
	}
}

// TestDevicesListAdmin pins GET /v1/admin/users/{id}/devices: an enrolled
// device shows up under its owner, and a device-less user lists empty rather
// than erroring.
func TestDevicesListAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	code, enroll := postJSON(t, base+"/v1/enroll", map[string]any{
		"id_token": idToken,
		"device":   map[string]string{"name": "kim-laptop", "platform": "windows", "fingerprint": "sha256:gaps"},
	})
	if code != http.StatusOK {
		t.Fatalf("enroll = %d %v", code, enroll)
	}
	deviceID := enroll["device_id"].(string)

	// Decode through the wire shape (snake_case devicePayload, 0.46.0), not
	// the store struct.
	var devs []struct {
		ID     string `json:"id"`
		UserID string `json:"user_id"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+user.ID+"/devices", idToken, nil, &devs); code != http.StatusOK {
		t.Fatalf("list devices = %d", code)
	}
	if len(devs) != 1 || devs[0].ID != deviceID || devs[0].UserID != user.ID {
		t.Fatalf("devices = %+v, want exactly the enrolled device %s", devs, deviceID)
	}
	bare, err := app.store.Users().Create(context.Background(), store.User{Username: "deviceless"})
	if err != nil {
		t.Fatal(err)
	}
	devs = nil
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/users/"+bare.ID+"/devices", idToken, nil, &devs); code != http.StatusOK || len(devs) != 0 {
		t.Fatalf("deviceless list = %d %+v, want 200 with no devices", code, devs)
	}
}

// TestBindingsListAdmin pins GET /v1/admin/bindings: empty on a fresh store,
// then one entry with the role and app resolved to names (operators read
// "ghost/ghost-readers", not UUIDs) after a role of the server is created
// with its row via the API.
func TestBindingsListAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	var bindings []bindingPayload
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/bindings", adminTok, nil, &bindings); code != http.StatusOK || len(bindings) != 0 {
		t.Fatalf("fresh bindings list = %d %+v, want 200 empty", code, bindings)
	}
	// A stored app row is enough; bindings are store rows, no runtime needed.
	row, err := app.store.Apps().Create(ctx, store.App{Name: "ghost", Version: "1", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	// The role and its row on the server land in one create.
	var created rolePayload
	code := adminReq(t, http.MethodPost, base+"/v1/admin/roles", adminTok,
		map[string]any{"name": "ghost-readers", "server": row.Name, "tools": []string{"get_*", "list_*"}}, &created)
	if code != http.StatusCreated || created.ID == "" {
		t.Fatalf("create role = %d %+v", code, created)
	}
	bindings = nil
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/bindings", adminTok, nil, &bindings); code != http.StatusOK {
		t.Fatalf("bindings list = %d", code)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings = %+v, want exactly the created one", bindings)
	}
	b := bindings[0]
	if b.ID == "" || b.App != "ghost" || b.Role != "ghost-readers" ||
		len(b.Tools) != 2 || b.Tools[0] != "get_*" || b.Tools[1] != "list_*" {
		t.Errorf("binding = %+v, want resolved app/role names + tool matchers", b)
	}
}

// TestAppSecretSetAdmin pins POST /v1/admin/apps/{id}/secrets:
// the set succeeds and upserts (rotating never stacks rows), the row stores
// ciphertext only, and the plaintext never appears in the set response or
// any app listing (credentials never leave the gateway).
func TestAppSecretSetAdmin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	// The manifest declares a static credential: a kind none app refuses a
	// secret it would never use.
	mf, err := managerParse(t, `
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: vaultish}
server: {name: straza.test/vaultish, version: "1"}
straza:
  runtime:
    kind: remote
    remote: {url: "http://127.0.0.1:9/mcp"}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := mf.JSON()
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.store.Apps().Create(ctx, store.App{Name: "vaultish", Version: "1", RuntimeKind: "remote", Manifest: manifestJSON})
	if err != nil {
		t.Fatal(err)
	}
	const secretValue = "sk-live-4711-NEVER-ECHO"

	code, body, _ := adminBytes(t, http.MethodPost, base+"/v1/admin/apps/"+row.ID+"/secrets", adminTok,
		"application/json", mustJSON(map[string]string{"role": "dev", "value": secretValue}))
	if code != http.StatusCreated {
		t.Fatalf("set secret = %d %s", code, body)
	}
	if bytes.Contains(body, []byte(secretValue)) {
		t.Fatalf("set response echoes the plaintext: %s", body)
	}
	var created map[string]string
	if err := json.Unmarshal(body, &created); err != nil || created["id"] == "" ||
		created["app"] != "vaultish" || created["role"] != "dev" {
		t.Fatalf("set response = %s (%v)", body, err)
	}

	// Rotation upserts the same role-bound row and stores ciphertext only.
	if code, body, _ := adminBytes(t, http.MethodPost, base+"/v1/admin/apps/"+row.ID+"/secrets", adminTok,
		"application/json", mustJSON(map[string]string{"role": "dev", "value": secretValue + "-rotated"})); code != http.StatusCreated {
		t.Fatalf("rotate secret = %d %s", code, body)
	}
	creds, err := app.store.Credentials().ListByApp(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("credential rows after rotate = %d, want 1 (upsert)", len(creds))
	}
	if bytes.Contains(creds[0].EncPayload, []byte(secretValue)) {
		t.Error("credential row contains the plaintext, want ciphertext only")
	}
	if code, body, _ := adminBytes(t, http.MethodGet, base+"/v1/admin/apps", adminTok, "", nil); code != http.StatusOK ||
		bytes.Contains(body, []byte(secretValue)) {
		t.Errorf("apps list = %d, plaintext leaked = %v", code, bytes.Contains(body, []byte(secretValue)))
	}

	// Refusals: bad bodies 400, unknown app or role 404.
	cases := []struct {
		name string
		url  string
		body string
		want int
	}{
		{"missing value", base + "/v1/admin/apps/" + row.ID + "/secrets", `{"role":"dev"}`, http.StatusBadRequest},
		// No role stores the server's own secret (the app-scoped row).
		{"missing role stores the app row", base + "/v1/admin/apps/" + row.ID + "/secrets", `{"value":"x"}`, http.StatusCreated},
		{"malformed JSON", base + "/v1/admin/apps/" + row.ID + "/secrets", `{`, http.StatusBadRequest},
		{"unknown role", base + "/v1/admin/apps/" + row.ID + "/secrets", `{"role":"ghost-role","value":"x"}`, http.StatusNotFound},
		{"unknown app", base + "/v1/admin/apps/nope/secrets", `{"role":"dev","value":"x"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, _ := adminBytes(t, http.MethodPost, tc.url, adminTok, "application/json", []byte(tc.body)); code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestDenylistRevokeSessionSink pins the spine.DenylistSink seam on the
// server denylist (pdp.go). Caller trace: the exported RevokeSession is
// reached only through spine.RevocationConsumer applying straza.revocation.>
// events (wired in New); the HTTP admin revoke uses the unexported method
// alongside its store write. Driving the sink exactly as the consumer does
// proves the in-memory entry alone flips /v1/decide to deny (revocation
// is pushed events → denylist, never a per-call DB read); the
// session row in the store stays active and untouched.
func TestDenylistRevokeSessionSink(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	token, sessionID := checkinToken(t, app, base)

	if code, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "git status",
	}); code != http.StatusOK || dec["effect"] != "allow" {
		t.Fatalf("pre-revoke decision = %d %v, want allow", code, dec)
	}

	app.denylist.RevokeSession(sessionID) // as the revocation consumer would

	code, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "git status",
	})
	if code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "revoked" {
		t.Fatalf("post-revoke decision = %d %v, want deny by rule \"revoked\"", code, dec)
	}
	if reason, _ := dec["reason"].(string); !strings.Contains(reason, "revoked") {
		t.Errorf("deny reason not actionable: %v", dec)
	}
	// Enforcement came from the in-memory set alone: the row never changed.
	ses, err := app.store.Sessions().GetByID(context.Background(), sessionID)
	if err != nil || ses.Status != store.SessionActive {
		t.Errorf("session row = %+v (%v), want still active in the store", ses, err)
	}
}

// TestUsersDeleteEndsAssignments pins the grant half of the user delete: the
// subject's assignment rows go with the row, the assignments list and the
// roles' assigned_count stop counting it, and one straza.audit.admin record
// per ended grant names the actor, the subject, the grant and the reason. A
// user without grants adds no record.
func TestUsersDeleteEndsAssignments(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	type roleRow struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		AssignedCount int    `json:"assigned_count"`
	}
	rolesByName := func(t *testing.T) map[string]roleRow {
		t.Helper()
		var rows []roleRow
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/roles", adminTok, nil, &rows); code != http.StatusOK {
			t.Fatalf("list roles = %d", code)
		}
		out := map[string]roleRow{}
		for _, r := range rows {
			out[r.Name] = r
		}
		return out
	}
	unassignRecords := func(t *testing.T, userID string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == actionRolesUnassign && ev["user"] == userID {
				out = append(out, ev)
			}
		}
		return out
	}

	cases := []struct {
		name     string
		username string
		roles    []string
	}{
		{"two grants", "doomed-two", []string{"dev", "reader"}},
		{"zero grants", "doomed-zero", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doomed, err := app.store.Users().Create(ctx, store.User{Username: tc.username})
			if err != nil {
				t.Fatal(err)
			}
			before := rolesByName(t)
			granted := map[string]store.RoleAssignment{}
			for _, name := range tc.roles {
				as, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
					SubjectKind: store.SubjectUser, SubjectID: doomed.ID, RoleID: before[name].ID,
				})
				if err != nil {
					t.Fatal(err)
				}
				granted[as.ID] = as
			}
			mid := rolesByName(t)
			for _, name := range tc.roles {
				if mid[name].AssignedCount != before[name].AssignedCount+1 {
					t.Fatalf("%s assigned_count after grant = %d, want %d", name, mid[name].AssignedCount, before[name].AssignedCount+1)
				}
			}

			var out map[string]string
			if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+doomed.ID, adminTok, nil, &out); code != http.StatusOK || out["status"] != "deleted" {
				t.Fatalf("delete = %d %v", code, out)
			}
			var listed []assignmentPayload
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/assignments", adminTok, nil, &listed); code != http.StatusOK {
				t.Fatalf("list assignments = %d", code)
			}
			for _, a := range listed {
				if a.SubjectID == doomed.ID {
					t.Errorf("deleted subject still listed: %+v", a)
				}
			}
			after := rolesByName(t)
			for _, name := range tc.roles {
				if after[name].AssignedCount != before[name].AssignedCount {
					t.Errorf("%s assigned_count after delete = %d, want %d", name, after[name].AssignedCount, before[name].AssignedCount)
				}
			}

			records := unassignRecords(t, doomed.ID)
			if len(records) != len(tc.roles) {
				t.Fatalf("roles.unassign records for the subject = %d, want %d: %v", len(records), len(tc.roles), records)
			}
			seen := map[string]bool{}
			for _, rec := range records {
				target, _ := rec["target"].(string)
				as, ok := granted[target]
				if !ok || seen[target] {
					t.Errorf("record target %q is not one of the subject's grants, or repeats one: %v", target, rec)
					continue
				}
				seen[target] = true
				roleName := ""
				for name, r := range before {
					if r.ID == as.RoleID {
						roleName = name
					}
				}
				want := map[string]any{
					"actor": "kim", "actorId": user.ID, "actorVia": "session",
					"user": doomed.ID, "role": roleName, "roleId": as.RoleID,
					"origin": store.OriginAdmin, "reason": "user deleted by admin",
				}
				for k, v := range want {
					if rec[k] != v {
						t.Errorf("record %s = %v, want %v (record %v)", k, rec[k], v, rec)
					}
				}
			}
		})
	}
}
