package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// keyRemoveEvents returns the straza.audit.admin payloads with action
// nhi-key.removed for one user.
func keyRemoveEvents(t *testing.T, app *App, userID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "nhi-key.removed" && ev["user"] == userID {
			out = append(out, ev)
		}
	}
	return out
}

// TestUsersDeleteDeprovisionsKeyAndGrants pins the credential half of the
// admin user delete: the user's NHI assertion key and every per-user OAuth
// grant go with the row, one straza.audit.admin record per removed row
// names the actor, the user and the reason, and another user's key and
// grants on the same apps stay in place. A user without a key or grants
// adds no record.
func TestUsersDeleteDeprovisionsKeyAndGrants(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keeper := seedNHIWithKey(t, app, "keeper-bot", pub)
	github, _ := grantUserApp(t, app, "github", keeper.ID)
	jira, _ := grantUserApp(t, app, "jira", keeper.ID)
	apps := map[string]store.App{"github": github, "jira": jira}

	cases := []struct {
		name     string
		username string
		nhi      bool
		withKey  bool
		grants   []string
	}{
		{"nhi with key and one grant", "doomed-bot", true, true, []string{"github"}},
		{"human with two grants and no key", "doomed-human", false, false, []string{"github", "jira"}},
		{"nhi without key or grants", "doomed-bare", true, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doomed store.User
			if tc.withKey {
				doomed = seedNHIWithKey(t, app, tc.username, pub)
			} else {
				u := store.User{Username: tc.username}
				if tc.nhi {
					u.Attrs = `{"kind":"nhi"}`
				}
				var err error
				if doomed, err = app.store.Users().Create(ctx, u); err != nil {
					t.Fatal(err)
				}
			}
			credByApp := map[string]store.Credential{}
			for _, name := range tc.grants {
				cred, err := app.broker.SetGrant(ctx, apps[name].ID, doomed.ID,
					secrets.Grant{AccessToken: "gho_" + tc.username}, secrets.GrantMeta{Provider: name})
				if err != nil {
					t.Fatal(err)
				}
				credByApp[name] = cred
			}
			if got := len(userGrants(t, app, doomed.ID)); got != len(tc.grants) {
				t.Fatalf("grants before delete = %d, want %d", got, len(tc.grants))
			}

			var out map[string]string
			if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+doomed.ID, adminTok, nil, &out); code != http.StatusOK || out["status"] != "deleted" {
				t.Fatalf("delete = %d %v", code, out)
			}

			if rows := userGrants(t, app, doomed.ID); len(rows) != 0 {
				t.Errorf("grants after delete = %d rows, want none", len(rows))
			}
			for _, name := range tc.grants {
				if app.broker.ForUser(apps[name].ID, doomed.ID).Secret != nil {
					t.Errorf("broker cache still resolves the %s grant of the deleted user", name)
				}
			}
			if _, err := app.store.Settings().Get(ctx, nhiKeyPrefix+doomed.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("key row after delete: err = %v, want ErrNotFound", err)
			}

			removes := grantRemoveEvents(t, app, doomed.ID)
			if len(removes) != len(tc.grants) {
				t.Fatalf("apps.grant.remove records = %d %v, want %d", len(removes), removes, len(tc.grants))
			}
			for _, ev := range removes {
				name, _ := ev["app"].(string)
				cred, ok := credByApp[name]
				if !ok {
					t.Errorf("record for an app the user held no grant on: %v", ev)
					continue
				}
				want := map[string]any{
					"action": "apps.grant.remove", "app": name, "user": doomed.ID, "credentialId": cred.ID,
					"reason": "user deleted by admin", "actor": "kim", "actorId": admin.ID, "actorVia": "session",
				}
				for k, v := range want {
					if ev[k] != v {
						t.Errorf("grant record %s = %v, want %v (record %v)", k, ev[k], v, ev)
					}
				}
			}
			keyRecords := keyRemoveEvents(t, app, doomed.ID)
			wantKeyRecords := 0
			if tc.withKey {
				wantKeyRecords = 1
			}
			if len(keyRecords) != wantKeyRecords {
				t.Fatalf("nhi-key.removed records = %d %v, want %d", len(keyRecords), keyRecords, wantKeyRecords)
			}
			for _, ev := range keyRecords {
				want := map[string]any{
					"action": "nhi-key.removed", "target": doomed.ID, "user": doomed.ID,
					"reason": "user deleted by admin", "actor": "kim", "actorId": admin.ID, "actorVia": "session",
				}
				for k, v := range want {
					if ev[k] != v {
						t.Errorf("key record %s = %v, want %v (record %v)", k, ev[k], v, ev)
					}
				}
			}

			// The keeper's rows never moved.
			if rows := userGrants(t, app, keeper.ID); len(rows) != 2 {
				t.Errorf("keeper grants = %d rows, want 2", len(rows))
			}
			if app.broker.ForUser(github.ID, keeper.ID).Secret == nil {
				t.Error("broker cache lost the keeper's github grant")
			}
			if _, err := app.store.Settings().Get(ctx, nhiKeyPrefix+keeper.ID); err != nil {
				t.Errorf("keeper key row: %v", err)
			}
			if got := grantRemoveEvents(t, app, keeper.ID); len(got) != 0 {
				t.Errorf("apps.grant.remove records for the keeper = %v, want none", got)
			}
			if got := keyRemoveEvents(t, app, keeper.ID); len(got) != 0 {
				t.Errorf("nhi-key.removed records for the keeper = %v, want none", got)
			}
		})
	}
}
