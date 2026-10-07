package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// actionRecords answers the straza.audit.admin records with action whose
// field key equals value.
func actionRecords(t *testing.T, app *App, action, key, value string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == action && ev[key] == value {
			out = append(out, ev)
		}
	}
	return out
}

// TestRemovalRecordsTheSameOnEitherLane pins that a server removed by
// publishing the removal its deleted file proposed goes the way a removal
// from the console goes: exactly one apps.remove record with the app, the
// roles that lost access and the admin role, the minted admin role and the
// owned role gone, one roles.unassign record per holder and one
// roles.delete record per role, each naming the person who made it. Later
// sweeps and a restart propose nothing more.
func TestRemovalRecordsTheSameOnEitherLane(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		fromFile bool
	}{
		{name: "removed from the console", fromFile: false},
		{name: "removed by publishing its file's removal", fromFile: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t, func(cfg *config.Config) { cfg.Apps.PollInterval = time.Hour })
			ctx := context.Background()
			kim := seedIdentity(t, app)
			grantAdmin(t, app, kim.ID)
			root := loginDeviceFlow(t, base, "kim", "hunter2!")
			f := &draftsFixture{app: app, base: base, kim: kim, root: root}
			up := startEchoUpstream(t)
			path := filepath.Join(app.cfg.AppsDir(), "echoapp.yaml")
			if tc.fromFile {
				if err := os.WriteFile(path, []byte(echoManifest(up.URL)), 0o600); err != nil {
					t.Fatal(err)
				}
				app.watcher.Sweep(ctx)
				publishOpenOf(t, f, path)
			} else if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(echoManifest(up.URL)), nil); code != http.StatusCreated {
				t.Fatalf("install = %d", code)
			}
			waitAppStatus(t, base, root, "echoapp", "running")
			erin := mkHuman(t, app, "erin", "mcp-admin-echoapp")
			var readers rolePayload
			if code := adminReq(t, "POST", base+"/v1/admin/roles", root,
				map[string]any{"name": "echoapp-readers", "description": "Reads echo", "server": "echoapp", "tools": []string{"echo"}}, &readers); code != http.StatusCreated {
				t.Fatalf("create echoapp-readers = %d", code)
			}
			uma := mkHuman(t, app, "uma", "echoapp-readers")
			minted, err := app.store.Roles().GetByName(ctx, "mcp-admin-echoapp")
			if err != nil {
				t.Fatal(err)
			}

			if tc.fromFile {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				app.watcher.Sweep(ctx)
				publishOpenOf(t, f, path)
			} else if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp", root, nil, nil); code != http.StatusOK {
				t.Fatalf("delete = %d", code)
			}

			removed := actionRecords(t, app, "apps.remove", "app", "echoapp")
			if len(removed) != 1 {
				t.Fatalf("apps.remove records = %d, want exactly one: %v", len(removed), removed)
			}
			rec := removed[0]
			if roles, _ := rec["roles"].([]any); len(roles) != 1 || roles[0] != "echoapp-readers" || rec["adminRole"] != "mcp-admin-echoapp" {
				t.Errorf("apps.remove record = %v, want roles [echoapp-readers] and adminRole mcp-admin-echoapp", rec)
			}
			for _, name := range []string{"mcp-admin-echoapp", "echoapp-readers"} {
				if _, err := app.store.Roles().GetByName(ctx, name); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("%s survived its server: %v", name, err)
				}
			}
			holders := []struct {
				role, roleID, user, server string
			}{
				{"mcp-admin-echoapp", minted.ID, erin.ID, ""},
				{"echoapp-readers", readers.ID, uma.ID, "echoapp"},
			}
			chained := []map[string]any{rec}
			for _, h := range holders {
				unassigned := actionRecords(t, app, "roles.unassign", "role", h.role)
				if len(unassigned) != 1 || unassigned[0]["user"] != h.user || unassigned[0]["roleId"] != h.roleID || unassigned[0]["reason"] != "server removed" {
					t.Errorf("roles.unassign records for %s = %v, want one for its holder with the reason server removed", h.role, unassigned)
				}
				deleted := actionRecords(t, app, "roles.delete", "role", h.role)
				if len(deleted) != 1 || deleted[0]["target"] != h.roleID || deleted[0]["reason"] != "removed with the server echoapp" {
					t.Fatalf("roles.delete records for %s = %v, want one with the reason", h.role, deleted)
				}
				if server, _ := deleted[0]["server"].(string); server != h.server {
					t.Errorf("roles.delete record for %s names server %q, want %q", h.role, server, h.server)
				}
				chained = append(chained, append(unassigned, deleted...)...)
			}
			for _, ev := range chained {
				if ev["actor"] != "kim" {
					t.Errorf("%s record names actor %v, want kim", ev["action"], ev["actor"])
				}
			}
			if !tc.fromFile {
				return
			}

			before := len(adminAuditEvents(t, app))
			app.watcher.Sweep(ctx)
			app.proposeUnfiled(ctx)
			if after := len(adminAuditEvents(t, app)); after != before {
				t.Errorf("admin records after another sweep and the boot pass = %d, want %d", after, before)
			}
		})
	}
}

// publishOpenOf publishes the newest open draft the apps directory file
// path proposed, as the fixture's person.
func publishOpenOf(t *testing.T, f *draftsFixture, path string) {
	t.Helper()
	rows, err := f.app.store.Drafts().BySource(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.State == "open" {
			id := strconv.FormatInt(row.ID, 10)
			if code, a := f.publishAll(t, f.root, id); code != http.StatusOK {
				t.Fatalf("publish %s = %d %q", id, code, a.Error)
			}
			return
		}
	}
	t.Fatalf("no open draft of %s", path)
}

// TestDryRunAnswersLikeTheInstall pins that a dry run of an install answers
// the provider refusals the install answers, an oauth provider this server
// does not have and agents client_credentials against a provider without
// the clientCredentials block, and that a name a file in the apps
// directory names passes both.
func TestDryRunAnswersLikeTheInstall(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.OAuth.RefreshInterval = time.Hour
		cfg.OAuth.Providers = map[string]config.OAuthProvider{"keycloak": {
			ClientID: "straza-connect", ClientSecret: "s3cret", AuthURL: "https://idp.example/auth", TokenURL: "https://idp.example/token",
		}}
	})
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	owned := strings.Replace(echoManifest(up.URL), "name: echoapp", "name: fileapp", 1)
	if err := os.WriteFile(filepath.Join(app.cfg.AppsDir(), "fileapp.yaml"), []byte(owned), 0o600); err != nil {
		t.Fatal(err)
	}
	app.watcher.Sweep(context.Background())

	cases := []struct {
		name, manifest string
		// dry and install are the two statuses, sentence the install's.
		dry, install int
		sentence     string
	}{
		{"an unknown oauth provider", strings.Replace(ccManifest(up.URL), "provider: keycloak", "provider: github", 1),
			http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
			`credential.oauth.provider "github" is not configured on this server. Add oauth.providers.github to the strazad config, or pick one of: keycloak.`},
		{"client credentials without the block", ccManifest(up.URL),
			http.StatusUnprocessableEntity, http.StatusUnprocessableEntity,
			"server echoapp sets credential.agents to client_credentials, and the provider keycloak has no clientCredentials settings. Add oauth.providers.keycloak.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared."},
		{"a name a file names, with a known provider", owned, http.StatusOK, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer := func(url string) (int, string) {
				t.Helper()
				var out map[string]any
				code := rawReq(t, "POST", url, root, "application/yaml", []byte(tc.manifest), &out)
				msg, _ := out["error"].(string)
				return code, msg
			}
			dryCode, dryMsg := answer(base + "/v1/admin/apps?dryRun=1")
			code, msg := answer(base + "/v1/admin/apps")
			wantDry := tc.sentence
			if tc.dry == http.StatusOK {
				wantDry = ""
			}
			if dryCode != tc.dry || dryMsg != wantDry {
				t.Errorf("dry run = %d %q\nwant %d %q", dryCode, dryMsg, tc.dry, wantDry)
			}
			if code != tc.install || msg != tc.sentence {
				t.Errorf("install = %d %q\nwant %d %q", code, msg, tc.install, tc.sentence)
			}
		})
	}
}
