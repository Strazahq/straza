package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestServerAdminAddressChangeRefused pins that a server admin never moves a
// remote server, whatever credential it declares, because the server's
// credentials and every caller's token follow the address. The refusal
// covers the dry run and the auth mode, the route over a credential kind
// switched off and on again stops at the move, every other field stays the
// server admin's, and an area grant still makes the change.
func TestServerAdminAddressChangeRefused(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	first, second := startEchoUpstream(t), startEchoUpstream(t)
	erin := mkHuman(t, app, "erin")

	const want = "changing a server's address needs the scope apps:write or the role straza-global-mcp-admin, because the server's credentials and every caller's token are sent to that address. Ask a holder of straza-global-mcp-admin to make that change."
	manifest := func(name, remote, credential string) string {
		return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {%s}
%s`, name, name, remote, credential)
	}
	at := func(url string) string { return fmt.Sprintf("url: %q", url) }
	post := func(tok, body string, dry bool) (int, string) {
		t.Helper()
		url := base + "/v1/admin/apps"
		if dry {
			url += "?dryRun=1"
		}
		var out map[string]any
		code := rawReq(t, "POST", url, tok, "application/yaml", []byte(body), &out)
		msg, _ := out["error"].(string)
		return code, msg
	}
	const static = "  credential:\n    kind: static\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n"
	const token = "  credential:\n    kind: token\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}\n"

	cases := []struct {
		name, credential string
		// status is where the server settles after the install.
		status string
		// change is the address block erin posts.
		change string
	}{
		{"none", "", "running", at(second.URL)},
		{"static", static, "degraded", at(second.URL)},
		{"token", token, "running", at(second.URL)},
		{"auth-mode", static, "degraded", at(first.URL) + ", auth: passthrough"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := manifest(tc.name, at(first.URL), tc.credential)
			if code, msg := post(root, stored, false); code != http.StatusCreated {
				t.Fatalf("kim installs = %d %q", code, msg)
			}
			waitAppStatus(t, base, root, tc.name, tc.status)
			grantRole(t, app, erin.ID, "mcp-admin-"+tc.name)
			erinTok := loginDeviceFlow(t, base, "erin", "hunter2!")

			changed := manifest(tc.name, tc.change, tc.credential)
			for _, dry := range []bool{true, false} {
				if code, msg := post(erinTok, changed, dry); code != http.StatusForbidden || msg != want {
					t.Errorf("erin changes the address block (dry run %v) = %d %q, want 403 %q", dry, code, msg, want)
				}
			}
			if tc.name == "token" {
				if code, msg := post(erinTok, manifest(tc.name, at(first.URL), ""), false); code != http.StatusCreated {
					t.Fatalf("erin switches the credential kind off = %d %q, want 201", code, msg)
				}
				stored = manifest(tc.name, at(first.URL), "")
				if code, msg := post(erinTok, manifest(tc.name, at(second.URL), ""), false); code != http.StatusForbidden || msg != want {
					t.Errorf("erin moves the server with its credential kind switched off = %d %q, want 403 %q", code, msg, want)
				}
			}
			row, err := app.store.Apps().GetByName(context.Background(), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(row.Manifest, second.URL) || strings.Contains(row.Manifest, "passthrough") {
				t.Errorf("the stored manifest took erin's address block: %s", row.Manifest)
			}
			if code, msg := post(erinTok, strings.Replace(stored, `version: "1.0.0"`, `version: "2.0.0"`, 1), false); code != http.StatusCreated {
				t.Errorf("erin's version change on the same address = %d %q, want 201", code, msg)
			}
			if code, msg := post(root, changed, false); code != http.StatusCreated {
				t.Errorf("kim changes the address block = %d %q, want 201", code, msg)
			}
		})
	}
}
