package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/store"
)

// TestIdentityWritesChainAdminRecords pins that each identity setup write
// chains exactly one straza.audit.admin record: a user create, a user
// PATCH that changes a field, an enroll token mint on the admin and the
// self route, an approver enrolment and an agent key set or removal. Each
// record names the actor of the lane that made the call, the target and
// subject fields of spec/events revision 40 and no secret value. A refused
// write and a PATCH that changes nothing chain no record. The rows run in
// order on one server, because later rows use what earlier rows created.
func TestIdentityWritesChainAdminRecords(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	lin := mkHuman(t, app, "lin")
	login := loginDeviceFlow(t, base, "kim", "hunter2!")
	var minted map[string]string
	if code := adminReq(t, "POST", base+"/v1/admin/api-tokens", login,
		map[string]string{"name": "iga-pull", "scope": "full"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint admin API token = %d", code)
	}
	wat := minted["token"]
	asKim := func(m map[string]any) map[string]any {
		m["actor"], m["actorId"], m["actorVia"] = "kim", kim.ID, "login"
		return m
	}
	asToken := func(m map[string]any) map[string]any {
		m["actor"], m["actorId"], m["actorVia"] = "iga-pull", minted["id"], "api-token"
		return m
	}

	_, deviceKey := genApproverKey(t)
	agentPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agentKey := base64.StdEncoding.EncodeToString(agentPub)
	sum := sha256.Sum256(agentPub)
	fingerprint := "sha256:" + hex.EncodeToString(sum[:])
	const firstPass, secondPass = "ada-first-passphrase", "ada-second-passphrase"
	device := func(name, platform string) map[string]any {
		return map[string]any{
			"name": name, "platform": platform, "key_alg": "ecdsa-p256",
			"public_key": deviceKey, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"},
		}
	}

	// got holds what earlier rows created, for the rows after them.
	got := map[string]string{}
	rows := []struct {
		name   string
		bearer string
		method string
		path   func() string
		body   func() any
		code   int
		// want is the exact data of the one record the call chains, actor
		// triple included, or nil when the call must chain no record.
		want func() map[string]any
		// keep reads the response into got.
		keep func(resp map[string]any)
		// secrets are values that must never appear in the record.
		secrets func() []string
	}{
		{
			name: "create a person with a login token", bearer: login, method: "POST",
			path: func() string { return "/v1/admin/users" },
			body: func() any {
				return map[string]any{"username": "ada", "email": "ada@x.io", "password": firstPass}
			},
			code: http.StatusCreated,
			keep: func(resp map[string]any) { got["ada"], _ = resp["id"].(string) },
			want: func() map[string]any {
				return asKim(map[string]any{"action": "user.create", "target": got["ada"],
					"user": got["ada"], "username": "ada", "kind": "human", "origin": "admin"})
			},
			secrets: func() []string { return []string{firstPass} },
		},
		{
			name: "create an AI agent with an admin API token", bearer: wat, method: "POST",
			path: func() string { return "/v1/admin/users" },
			body: func() any { return map[string]any{"username": "bot-ci", "kind": "nhi"} },
			code: http.StatusCreated,
			keep: func(resp map[string]any) { got["bot"], _ = resp["id"].(string) },
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.create", "target": got["bot"],
					"user": got["bot"], "username": "bot-ci", "kind": "nhi", "userType": "agent", "origin": "admin"})
			},
		},
		{
			name: "refuse a username that exists", bearer: login, method: "POST",
			path: func() string { return "/v1/admin/users" },
			body: func() any { return map[string]any{"username": "ada"} },
			code: http.StatusConflict,
		},
		{
			name: "refuse an unknown kind", bearer: login, method: "POST",
			path: func() string { return "/v1/admin/users" },
			body: func() any { return map[string]any{"username": "eve", "kind": "robot"} },
			code: http.StatusBadRequest,
		},
		{
			name: "set the agent's sponsor", bearer: login, method: "PATCH",
			path: func() string { return "/v1/admin/users/" + got["bot"] },
			body: func() any { return map[string]any{"sponsor": "ada"} },
			code: http.StatusOK,
			want: func() map[string]any {
				return asKim(map[string]any{"action": "user.update", "target": got["bot"],
					"user": got["bot"], "username": "bot-ci", "changed": []any{"sponsor"}})
			},
		},
		{
			name: "change a password", bearer: wat, method: "PATCH",
			path: func() string { return "/v1/admin/users/" + got["ada"] },
			body: func() any { return map[string]any{"password": secondPass} },
			code: http.StatusOK,
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.update", "target": got["ada"],
					"user": got["ada"], "username": "ada", "changed": []any{"password"}})
			},
			secrets: func() []string { return []string{secondPass, "$2"} },
		},
		{
			name: "disable a person and rename them", bearer: login, method: "PATCH",
			path: func() string { return "/v1/admin/users/" + lin.ID },
			body: func() any { return map[string]any{"status": "disabled", "display": "Lin Mei"} },
			code: http.StatusOK,
			want: func() map[string]any {
				return asKim(map[string]any{"action": "user.update", "target": lin.ID,
					"user": lin.ID, "username": "lin", "changed": []any{"display", "status"}})
			},
		},
		{
			name: "a PATCH with no field", bearer: login, method: "PATCH",
			path: func() string { return "/v1/admin/users/" + got["ada"] },
			body: func() any { return map[string]any{} },
			code: http.StatusOK,
		},
		{
			name: "a PATCH that sets the current values", bearer: wat, method: "PATCH",
			path: func() string { return "/v1/admin/users/" + got["ada"] },
			body: func() any { return map[string]any{"email": "ada@x.io", "sponsor": "", "ephemeral": false} },
			code: http.StatusOK,
		},
		{
			name: "mint an enroll token on the admin route", bearer: wat, method: "POST",
			path: func() string { return "/v1/admin/approvers/enroll-token" },
			body: func() any { return map[string]any{"username": "ada"} },
			code: http.StatusOK,
			keep: func(resp map[string]any) { got["enroll"], _ = resp["enroll_token"].(string) },
			want: func() map[string]any {
				return asToken(map[string]any{"action": "enroll-token.create", "target": got["ada"],
					"user": got["ada"], "username": "ada"})
			},
			secrets: func() []string { return []string{got["enroll"]} },
		},
		{
			name: "mint an enroll token on the self route", bearer: login, method: "POST",
			path: func() string { return "/v1/approvals/self/enroll-token" },
			body: func() any { return map[string]any{"channel": "browser"} },
			code: http.StatusOK,
			keep: func(resp map[string]any) { got["self"], _ = resp["enroll_token"].(string) },
			want: func() map[string]any {
				return asKim(map[string]any{"action": "enroll-token.create", "target": kim.ID,
					"user": kim.ID, "username": "kim", "channel": "browser", "self": true})
			},
			secrets: func() []string { return []string{got["self"]} },
		},
		{
			name: "refuse an enrolment with a bad enroll token", method: "POST",
			path: func() string { return "/v1/approver/enroll" },
			body: func() any {
				return map[string]any{"enroll_token": "not-an-enroll-credential", "device": device("eve-pixel", "android")}
			},
			code: http.StatusUnauthorized,
		},
		{
			name: "enrol an approver device", method: "POST",
			path: func() string { return "/v1/approver/enroll" },
			body: func() any {
				return map[string]any{"enroll_token": got["enroll"], "device": device("ada-pixel", "android")}
			},
			code: http.StatusCreated,
			keep: func(resp map[string]any) {
				got["device"], _ = resp["approver_device_id"].(string)
				got["deviceCredential"], _ = resp["device_token"].(string)
			},
			want: func() map[string]any {
				return map[string]any{"action": "approver.enroll", "target": got["device"],
					"user": got["ada"], "username": "ada", "device": got["device"], "platform": "android", "name": "ada-pixel",
					"actor": "ada", "actorId": got["ada"], "actorVia": "enroll-token"}
			},
			secrets: func() []string { return []string{got["enroll"], got["deviceCredential"], deviceKey} },
		},
		{
			name: "refuse an agent key for a person", bearer: login, method: "PUT",
			path: func() string { return "/v1/admin/users/" + got["ada"] + "/nhi-key" },
			body: func() any { return map[string]any{"public_key": agentKey} },
			code: http.StatusBadRequest,
		},
		{
			name: "register the agent's key", bearer: login, method: "PUT",
			path: func() string { return "/v1/admin/users/" + got["bot"] + "/nhi-key" },
			body: func() any { return map[string]any{"public_key": agentKey} },
			code: http.StatusOK,
			keep: func(map[string]any) {
				var read map[string]any
				if code := adminReq(t, "GET", base+"/v1/admin/users/"+got["bot"]+"/nhi-key", login, nil, &read); code != http.StatusOK {
					t.Fatalf("read the agent key = %d", code)
				}
				if read["fingerprint"] != fingerprint {
					t.Errorf("the key read renders fingerprint %v, the record must carry the same %q", read["fingerprint"], fingerprint)
				}
			},
			want: func() map[string]any {
				return asKim(map[string]any{"action": "nhi-key.set", "target": got["bot"],
					"user": got["bot"], "username": "bot-ci", "fingerprint": fingerprint})
			},
			secrets: func() []string { return []string{agentKey} },
		},
		{
			name: "remove the agent's key", bearer: wat, method: "DELETE",
			path: func() string { return "/v1/admin/users/" + got["bot"] + "/nhi-key" },
			code: http.StatusOK,
			want: func() map[string]any {
				return asToken(map[string]any{"action": "nhi-key.removed", "target": got["bot"], "user": got["bot"]})
			},
		},
	}
	for _, row := range rows {
		before := len(adminAuditEvents(t, app))
		var body any
		if row.body != nil {
			body = row.body()
		}
		var resp map[string]any
		if code := adminReq(t, row.method, base+row.path(), row.bearer, body, &resp); code != row.code {
			t.Fatalf("%s: status %d, want %d (%v)", row.name, code, row.code, resp)
		}
		if row.keep != nil {
			row.keep(resp)
		}
		events := adminAuditEvents(t, app)
		if row.want == nil {
			if len(events) != before {
				t.Errorf("%s: chained %d admin records, want none: %v", row.name, len(events)-before, events[before:])
			}
			continue
		}
		if len(events) != before+1 {
			t.Errorf("%s: chained %d admin records, want exactly one: %v", row.name, len(events)-before, events[before:])
			continue
		}
		rec := events[len(events)-1]
		gotJSON, _ := json.Marshal(rec)
		wantJSON, _ := json.Marshal(row.want())
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: record\n got %s\nwant %s", row.name, gotJSON, wantJSON)
		}
		if row.secrets != nil {
			for _, s := range row.secrets() {
				if s != "" && strings.Contains(string(gotJSON), s) {
					t.Errorf("%s: the record carries a secret value %q: %s", row.name, s, gotJSON)
				}
			}
		}
	}
}

// TestUserChangesNamesPathsNeverValues pins the changed[] list of a
// user.update record: one wire name per field whose value changed, in the
// request's field order, a password listed whenever the request carries
// one, and nothing for a field set to the value it already had.
func TestUserChangesNamesPathsNeverValues(t *testing.T) {
	t.Parallel()
	base := store.User{Email: "ada@x.io", Display: "Ada", Title: "Engineer", Status: store.UserActive,
		UserType: store.UserTypeHuman, AgencyMode: store.AgencyInteractive, Sponsor: "kim", SwarmID: "s-1"}
	for _, tc := range []struct {
		name     string
		edit     func(u *store.User)
		password bool
		want     []string
	}{
		{"nothing", func(*store.User) {}, false, nil},
		{"email", func(u *store.User) { u.Email = "ada@y.io" }, false, []string{"email"}},
		{"display", func(u *store.User) { u.Display = "Ada L" }, false, []string{"display"}},
		{"title", func(u *store.User) { u.Title = "" }, false, []string{"title"}},
		{"status", func(u *store.User) { u.Status = store.UserDisabled }, false, []string{"status"}},
		{"password only", func(*store.User) {}, true, []string{"password"}},
		{"user_type", func(u *store.User) { u.UserType = "" }, false, []string{"user_type"}},
		{"agency_mode", func(u *store.User) { u.AgencyMode = store.AgencyAutonomous }, false, []string{"agency_mode"}},
		{"sponsor", func(u *store.User) { u.Sponsor = "lin" }, false, []string{"sponsor"}},
		{"swarm_id", func(u *store.User) { u.SwarmID = "s-2" }, false, []string{"swarm_id"}},
		{"ephemeral", func(u *store.User) { u.Ephemeral = true }, false, []string{"ephemeral"}},
		{"several in request order", func(u *store.User) { u.Sponsor, u.Status = "", store.UserDisabled }, true,
			[]string{"status", "password", "sponsor"}},
	} {
		after := base
		tc.edit(&after)
		if got := userChanges(base, after, tc.password); !slices.Equal(got, tc.want) {
			t.Errorf("%s: changed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIdentityRecordsSurviveACancelledRequest pins that each identity setup
// writer chains its record, with the actor, on a request whose client has
// already gone: the store write it records has happened, so the record
// must not depend on the client waiting.
func TestIdentityRecordsSurviveACancelledRequest(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ada := mkHuman(t, app, "ada")
	ctx, cancel := context.WithCancel(withActor(context.Background(), auditActor{Name: "kim", ID: "kim-id", Via: "login"}))
	cancel()
	enrolled := approval.EnrollResult{DeviceID: "device-id", UserID: ada.ID, Username: ada.Username}
	for _, tc := range []struct {
		action string
		write  func()
	}{
		{actionUserCreate, func() { app.auditUserCreate(ctx, ada, store.OriginAdmin) }},
		{actionUserUpdate, func() { app.auditUserUpdate(ctx, ada, []string{"email"}) }},
		{actionEnrollTokenCreate, func() { app.auditEnrollTokenCreate(ctx, ada, "") }},
		{actionApproverEnroll, func() { app.auditApproverEnroll(ctx, enrolled, "browser", "Edge on work laptop") }},
		{actionNHIKeySet, func() { app.auditNHIKeySet(ctx, ada, "AAAA") }},
		{actionNHIKeyRemoved, func() { app.auditNHIKeyRemoved(ctx, ada.ID) }},
	} {
		before := len(adminAuditEvents(t, app))
		tc.write()
		events := adminAuditEvents(t, app)
		if len(events) != before+1 || events[len(events)-1]["action"] != tc.action {
			t.Errorf("%s on a cancelled request chained %d records, want exactly one: %v", tc.action, len(events)-before, events[before:])
			continue
		}
		if got := events[len(events)-1]["actor"]; got == nil {
			t.Errorf("%s on a cancelled request lost its actor: %v", tc.action, events[len(events)-1])
		}
	}
}
