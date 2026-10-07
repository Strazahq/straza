package server

import (
	"net/http"
	"testing"
)

// TestAppWireKeys pins the App object's admin role keys on the wire: every
// App answer names admin_role and admin_role_id, only the list carries
// may_change, and no camelCase spelling of the three appears. The other
// tests decode into appPayload, so this is the one that reads raw keys.
func TestAppWireKeys(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)

	var installed map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(echoManifest(up.URL)), &installed); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	waitAppStatus(t, base, root, "echoapp", "running")
	var rows []map[string]any
	if code := adminReq(t, "GET", base+"/v1/admin/apps", root, nil, &rows); code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("list = %d with %d rows, want 200 and 1", code, len(rows))
	}
	post := func(verb string) map[string]any {
		var out map[string]any
		if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/"+verb, root, nil, &out); code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", verb, code)
		}
		return out
	}

	cases := []struct {
		route     string
		body      map[string]any
		mayChange bool
	}{
		{"install", installed, false},
		{"list", rows[0], true},
		{"recheck", post("health"), false},
		{"disable", post("disable"), false},
		{"enable", post("enable"), false},
	}
	for _, c := range cases {
		t.Run(c.route, func(t *testing.T) {
			if c.body["admin_role"] != "mcp-admin-echoapp" {
				t.Errorf("admin_role = %v, want mcp-admin-echoapp", c.body["admin_role"])
			}
			if id, _ := c.body["admin_role_id"].(string); id == "" {
				t.Errorf("admin_role_id = %v, want a role id", c.body["admin_role_id"])
			}
			if may, ok := c.body["may_change"]; ok != c.mayChange || (ok && may != true) {
				t.Errorf("may_change = %v (present %v), want present %v and true when present", may, ok, c.mayChange)
			}
			for _, old := range []string{"adminRole", "adminRoleId", "mayChange"} {
				if _, ok := c.body[old]; ok {
					t.Errorf("answer carries the old key %s", old)
				}
			}
		})
	}
}
