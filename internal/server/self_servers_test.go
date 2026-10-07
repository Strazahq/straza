package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// installReach installs one app from its manifest and binds every tool on
// it to the named role, which must exist. The app need not reach running:
// the servers read lists managed apps whatever their state.
func installReach(t *testing.T, app *App, manifest, roleName string) store.App {
	t.Helper()
	ctx := context.Background()
	mf, err := managerParse(t, manifest)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	catReach(t, app, roleName, row, `["*"]`)
	return row
}

// reachManifest renders an app manifest from a runtime block and a
// credential block, the latter empty for credential kind none.
func reachManifest(name, runtime, credential string) string {
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:%s%s
`, name, name, runtime, credential)
}

// selfServers reads GET /v1/self/servers as raw rows, so a case can assert
// a key's absence, and hands back the raw body for leak checks.
func selfServers(t *testing.T, base, tok, user string) (int, []map[string]any, string) {
	t.Helper()
	url := base + "/v1/self/servers"
	if user != "" {
		url += "?user=" + user
	}
	code, body := getJSONAuth(t, url, tok)
	if code != http.StatusOK {
		return code, nil, body
	}
	var rows []map[string]any
	if err := jsonUnmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("servers body %s: %v", body, err)
	}
	return code, rows, body
}

func rowFor(rows []map[string]any, app string) (map[string]any, bool) {
	for _, row := range rows {
		if row["app"] == app {
			return row, true
		}
	}
	return nil, false
}

// TestSelfServers pins GET /v1/self/servers, the read behind the
// connections panel: one row per managed app the acting user reaches or is
// connected to, in app name order, with the row fields of the connect list
// and never a value. user= follows the connect routes' rule: the caller,
// an agent they sponsor, or anyone for an administrator.
func TestSelfServers(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()
	alice := seedGatewayUser(t, app, "alice", "dev")
	seedGatewayUser(t, app, "mallory", "ops")
	seedAgent(t, app, "joe", "alice", "ops")
	kim := seedGatewayUser(t, app, "kim", AdminRole)

	remote := fmt.Sprintf("\n    kind: remote\n    remote: {url: %q}", up.URL)
	const command = "\n    kind: command\n    command: {exec: npx, args: [\"-y\", \"example-mcp\"]}"
	const header = "\n    inject: {as: header, name: Authorization, template: \"Bearer {{secret}}\"}"
	token := func(agents string) string { return "\n  credential:\n    kind: token\n    agents: " + agents + header }
	installReach(t, app, reachManifest("tokapp", remote, token("sponsor")), "dev")
	adminSet := installReach(t, app, reachManifest("adminset", remote, token("own")), "dev")
	installReach(t, app, reachManifest("staticapp", remote, "\n  credential:\n    kind: static"+header), "dev")
	installReach(t, app, reachManifest("cmdapp", command, "\n  credential:\n    kind: static\n    inject: {as: env, name: TOKEN}"), "dev")
	installReach(t, app, reachManifest("noneapp", remote, ""), "dev")
	heldOnly := installReach(t, app, reachManifest("heldonly", remote, token("own")), "ops")
	installReach(t, app, reachManifest("neither", remote, token("own")), "ops")

	const adminToken = "tok-admin-set-NEVER-LEAK"
	const heldToken = "tok-held-NEVER-LEAK"
	future := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	if _, err := app.broker.SetToken(ctx, adminSet.ID, alice.ID, adminToken, secrets.GrantMeta{ExpiresAt: &future, SetBy: kim.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.broker.SetToken(ctx, heldOnly.ID, alice.ID, heldToken, secrets.GrantMeta{}); err != nil {
		t.Fatal(err)
	}
	aliceTok := sessionToken(t, base, "alice")
	malloryTok := sessionToken(t, base, "mallory")

	// In want, a nil value means the key must be absent and present means
	// it must exist with any value.
	type present struct{}
	cases := []struct {
		name     string
		tok      string
		user     string
		app      string
		absent   bool
		want     map[string]any
		wantCode int
		wantErr  string
	}{
		{name: "reached token app with no row", tok: aliceTok, app: "tokapp",
			want: map[string]any{"runtime": "remote", "kind": "token", "agents": "sponsor", "reached": true, "connected": false, "allow_agents": false, "provider": nil, "fingerprint": nil, "set_by": nil, "expires_at": nil, "updated_at": nil}},
		{name: "reached token app with a row set by an admin", tok: aliceTok, app: "adminset",
			want: map[string]any{"kind": "token", "agents": "own", "reached": true, "connected": true, "fingerprint": secrets.Fingerprint(adminToken), "expires_at": future.Format(time.RFC3339), "updated_at": present{}, "set_by": "kim", "allow_agents": false}},
		{name: "static remote app", tok: aliceTok, app: "staticapp",
			want: map[string]any{"runtime": "remote", "kind": "static", "agents": nil, "reached": true, "connected": false, "allow_agents": nil, "fingerprint": nil}},
		{name: "command app", tok: aliceTok, app: "cmdapp",
			want: map[string]any{"runtime": "command", "kind": "static", "agents": nil, "reached": true, "connected": false}},
		{name: "kind none app", tok: aliceTok, app: "noneapp",
			want: map[string]any{"runtime": "remote", "kind": "none", "agents": nil, "reached": true, "connected": false, "allow_agents": nil}},
		{name: "held row without reach", tok: aliceTok, app: "heldonly",
			want: map[string]any{"kind": "token", "reached": false, "connected": true, "fingerprint": secrets.Fingerprint(heldToken), "set_by": nil, "expires_at": nil, "allow_agents": false}},
		{name: "neither reached nor held", tok: aliceTok, app: "neither", absent: true},
		{name: "sponsored agent reaches through its own roles", tok: aliceTok, user: "joe", app: "heldonly",
			want: map[string]any{"reached": true, "connected": false, "allow_agents": false, "fingerprint": nil}},
		{name: "sponsored agent does not inherit the sponsor's reach", tok: aliceTok, user: "joe", app: "tokapp", absent: true},
		{name: "stranger refused in words", tok: malloryTok, user: "alice", wantCode: http.StatusForbidden,
			wantErr: "only alice's sponsor or an administrator can manage alice's connections"},
		{name: "unknown user", tok: aliceTok, user: "nobody", wantCode: http.StatusNotFound, wantErr: "unknown user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, rows, body := selfServers(t, base, tc.tok, tc.user)
			if tc.wantCode != 0 {
				if code != tc.wantCode || !strings.Contains(body, tc.wantErr) {
					t.Fatalf("servers = %d %s, want %d %q", code, body, tc.wantCode, tc.wantErr)
				}
				return
			}
			if code != http.StatusOK {
				t.Fatalf("servers = %d %s", code, body)
			}
			row, ok := rowFor(rows, tc.app)
			if tc.absent {
				if ok {
					t.Fatalf("%s listed: %v", tc.app, row)
				}
				return
			}
			if !ok {
				t.Fatalf("%s missing from %v", tc.app, rows)
			}
			for key, want := range tc.want {
				got, has := row[key]
				switch want.(type) {
				case nil:
					if has {
						t.Errorf("%s: %s = %v, want the key absent", tc.app, key, got)
					}
				case present:
					if !has {
						t.Errorf("%s: %s absent, want it present", tc.app, key)
					}
				default:
					if !has || got != want {
						t.Errorf("%s: %s = %v, want %v", tc.app, key, got, want)
					}
				}
			}
		})
	}

	// The whole answer is in app name order and carries no value under any
	// name.
	code, rows, raw := selfServers(t, base, aliceTok, "")
	if code != http.StatusOK {
		t.Fatalf("servers = %d %s", code, raw)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, fmt.Sprint(row["app"]))
	}
	if len(names) != 6 || !sort.StringsAreSorted(names) {
		t.Errorf("apps = %v, want six in name order", names)
	}
	for _, leak := range []string{`"value":`, `"token":`, adminToken, heldToken} {
		if strings.Contains(raw, leak) {
			t.Errorf("servers body carries %q: %s", leak, raw)
		}
	}
}
