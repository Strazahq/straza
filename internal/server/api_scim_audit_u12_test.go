package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSCIMUserWritesChainAdminRecords pins that a user created or changed
// over SCIM chains the records the admin API writes: user.create on a
// create and on the revive of a deactivated row, and user.update with the
// names of the changed fields on every write that changes one, a status
// flip included, each naming the admin API token that acted and origin
// scim on user.create. A write that changes nothing and a refused write
// chain none. A status flip also chains its straza.audit.identity record.
// The rows run in order on one server, because later rows use what
// earlier rows created.
func TestSCIMUserWritesChainAdminRecords(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	var minted struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", adminTok,
		map[string]any{"name": "idm-provisioner", "scope": "scim:read,scim:write"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint the SCIM admin API token = %d", code)
	}
	asToken := func(m map[string]any) map[string]any {
		m["actor"], m["actorId"], m["actorVia"] = "idm-provisioner", minted.ID, "api-token"
		return m
	}
	const (
		core    = `"urn:ietf:params:scim:schemas:core:2.0:User"`
		agentic = `"urn:ietf:params:scim:schemas:extension:agent:2.0:Agent"`
		patch   = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":`
	)

	// got holds the ids earlier rows created, for the rows after them.
	got := map[string]string{}
	userPath := func(name string) func() string {
		return func() string { return "/scim/v2/Users/" + got[name] }
	}
	usersPath := func() string { return "/scim/v2/Users" }
	rows := []struct {
		name   string
		method string
		path   func() string
		body   string
		code   int
		// keep names the user whose id the answer carries, for later rows.
		keep string
		// want is the exact data of the one straza.audit.admin record the
		// write chains, or nil when it must chain none.
		want func() map[string]any
		// identity is the one straza.audit.identity action the write chains
		// for the user, or "" when it must chain none.
		identity string
	}{
		{
			name: "create a person", method: http.MethodPost, path: usersPath,
			body: `{"schemas":[` + core + `],"userName":"ada","displayName":"Ada","title":"Engineer",` +
				`"emails":[{"value":"ada@x.io","primary":true}],"active":true}`,
			code: http.StatusCreated, keep: "ada",
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.create", "target": got["ada"],
					"user": got["ada"], "username": "ada", "kind": "human", "origin": "scim"})
			},
		},
		{
			name: "create an AI agent", method: http.MethodPost, path: usersPath,
			body: `{"schemas":[` + core + `,` + agentic + `],"userName":"bot-ci"}`,
			code: http.StatusCreated, keep: "bot",
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.create", "target": got["bot"],
					"user": got["bot"], "username": "bot-ci", "kind": "nhi", "userType": "agent", "origin": "scim"})
			},
		},
		{
			name: "refuse a create over an active userName", method: http.MethodPost, path: usersPath,
			body: `{"schemas":[` + core + `],"userName":"ada"}`,
			code: http.StatusConflict,
		},
		{
			name: "PATCH the email and the title", method: http.MethodPatch, path: userPath("ada"),
			body: patch + `[{"op":"replace","path":"emails","value":[{"value":"ada@y.io","primary":true}]},` +
				`{"op":"replace","path":"title","value":"Staff engineer"}]}`,
			code: http.StatusOK,
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.update", "target": got["ada"],
					"user": got["ada"], "username": "ada", "changed": []any{"email", "title"}})
			},
		},
		{
			name: "PATCH that sets the current value", method: http.MethodPatch, path: userPath("ada"),
			body: patch + `[{"op":"replace","path":"title","value":"Staff engineer"}]}`,
			code: http.StatusOK,
		},
		{
			name: "refuse a PATCH of the read-only lock block", method: http.MethodPatch, path: userPath("ada"),
			body: patch + `[{"op":"replace","path":"locked","value":true}]}`,
			code: http.StatusBadRequest,
		},
		{
			name: "PUT that replaces the display name", method: http.MethodPut, path: userPath("ada"),
			body: `{"schemas":[` + core + `],"userName":"ada","displayName":"Ada Lovelace","title":"Staff engineer",` +
				`"emails":[{"value":"ada@y.io","primary":true}],"active":true}`,
			code: http.StatusOK,
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.update", "target": got["ada"],
					"user": got["ada"], "username": "ada", "changed": []any{"display"}})
			},
		},
		{
			name: "deactivate over PATCH", method: http.MethodPatch, path: userPath("ada"),
			body: patch + `[{"op":"replace","path":"active","value":false}]}`,
			code: http.StatusOK, identity: "user.killed",
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.update", "target": got["ada"],
					"user": got["ada"], "username": "ada", "changed": []any{"status"}})
			},
		},
		{
			name: "reactivate over PATCH", method: http.MethodPatch, path: userPath("ada"),
			body: patch + `[{"op":"replace","path":"active","value":true}]}`,
			code: http.StatusOK, identity: "user.reactivated",
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.update", "target": got["ada"],
					"user": got["ada"], "username": "ada", "changed": []any{"status"}})
			},
		},
		{
			name: "DELETE deactivates", method: http.MethodDelete, path: userPath("ada"),
			code: http.StatusNoContent, identity: "user.killed",
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.update", "target": got["ada"],
					"user": got["ada"], "username": "ada", "changed": []any{"status"}})
			},
		},
		{
			name: "DELETE of a deactivated user", method: http.MethodDelete, path: userPath("ada"),
			code: http.StatusNoContent,
		},
		{
			name: "revive through a conflicting create", method: http.MethodPost, path: usersPath,
			body: `{"schemas":[` + core + `],"userName":"ada","displayName":"Ada Lovelace","active":true}`,
			code: http.StatusCreated, keep: "revived", identity: "user.reactivated",
			want: func() map[string]any {
				return asToken(map[string]any{"action": "user.create", "target": got["ada"],
					"user": got["ada"], "username": "ada", "kind": "human", "origin": "scim"})
			},
		},
	}
	for _, row := range rows {
		admins := len(adminAuditEvents(t, app))
		identities := len(outboxDataFor(t, app, "straza.audit.identity"))
		code, resp := scimReq(t, row.method, base, minted.Token, row.path(), row.body)
		if code != row.code {
			t.Fatalf("%s: status %d, want %d (%v)", row.name, code, row.code, resp)
		}
		if row.keep != "" {
			got[row.keep], _ = resp["id"].(string)
		}

		idEvents := outboxDataFor(t, app, "straza.audit.identity")[identities:]
		switch {
		case row.identity == "" && len(idEvents) != 0:
			t.Errorf("%s: chained identity records %v, want none", row.name, idEvents)
		case row.identity != "" && (len(idEvents) != 1 || idEvents[0]["action"] != row.identity || idEvents[0]["user"] != got["ada"]):
			t.Errorf("%s: chained identity records %v, want exactly one %s for %s", row.name, idEvents, row.identity, got["ada"])
		}

		events := adminAuditEvents(t, app)[admins:]
		if row.want == nil {
			if len(events) != 0 {
				t.Errorf("%s: chained %d admin records, want none: %v", row.name, len(events), events)
			}
			continue
		}
		if len(events) != 1 {
			t.Errorf("%s: chained %d admin records, want exactly one: %v", row.name, len(events), events)
			continue
		}
		gotJSON, _ := json.Marshal(events[0])
		wantJSON, _ := json.Marshal(row.want())
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: record\n got %s\nwant %s", row.name, gotJSON, wantJSON)
		}
	}
	if got["revived"] != got["ada"] {
		t.Errorf("the revive answered id %q, want the deactivated row's %q", got["revived"], got["ada"])
	}
}
