package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// grantUserApp creates an app row and seals one per-user OAuth grant on it
// for the user, the row the connect callback writes.
func grantUserApp(t *testing.T, app *App, name, userID string) (store.App, store.Credential) {
	t.Helper()
	ctx := context.Background()
	row, err := app.store.Apps().Create(ctx, store.App{Name: name, RuntimeKind: "remote", Manifest: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := app.broker.SetGrant(ctx, row.ID, userID,
		secrets.Grant{AccessToken: "gho_" + name}, secrets.GrantMeta{Provider: name})
	if err != nil {
		t.Fatal(err)
	}
	return row, cred
}

// userGrants lists the user's per-user credential rows.
func userGrants(t *testing.T, app *App, userID string) []store.Credential {
	t.Helper()
	rows, err := app.store.Credentials().ListByOwner(context.Background(), store.CredScopeUser, userID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// outboxDataFor returns the data payloads of every outbox event with the
// subject, oldest first, read through ListRecent like adminAuditEvents.
func outboxDataFor(t *testing.T, app *App, subject string) []map[string]any {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Subject != subject {
			continue
		}
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(rows[i].CE), &ce); err != nil {
			t.Fatal(err)
		}
		out = append(out, ce.Data)
	}
	return out
}

// grantRemoveEvents returns the straza.audit.admin payloads with action
// apps.grant.remove for one user.
func grantRemoveEvents(t *testing.T, app *App, userID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "apps.grant.remove" && ev["user"] == userID {
			out = append(out, ev)
		}
	}
	return out
}

// apiTokenByName resolves a minted admin API token's metadata by its name.
func apiTokenByName(t *testing.T, app *App, name string) apiTokenMeta {
	t.Helper()
	tokens, err := app.apiTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range tokens {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no admin API token named %q", name)
	return apiTokenMeta{}
}

// TestSCIMDeactivateWipesGrantsWithAudit: an IdM deactivation is
// deprovisioning, so every per-user OAuth grant of the user goes away with
// the kill-switch cascade, one straza.audit.admin record per row names the
// SCIM token that acted, and one straza.apps.updated per app tells the
// console the grant is gone.
func TestSCIMDeactivateWipesGrantsWithAudit(t *testing.T) {
	t.Parallel()
	app, base, token := scimTestSetup(t)
	ctx := context.Background()
	uid := scimCreateUser(t, base, token,
		`{"userName":"leaver@x.io","externalId":"idm-9","active":true}`)
	github, ghCred := grantUserApp(t, app, "github", uid)
	_, jiraCred := grantUserApp(t, app, "jira", uid)
	if rows := userGrants(t, app, uid); len(rows) != 2 {
		t.Fatalf("grants before DELETE = %d, want 2", len(rows))
	}

	if code, _ := scimReq(t, "DELETE", base, token, "/scim/v2/Users/"+uid, ""); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", code)
	}
	if rows := userGrants(t, app, uid); len(rows) != 0 {
		t.Fatalf("grants after DELETE = %d rows, want none", len(rows))
	}
	if app.broker.ForUser(github.ID, uid).Secret != nil {
		t.Error("broker cache still resolves the wiped github grant")
	}

	meta := apiTokenByName(t, app, "conformance")
	removes := grantRemoveEvents(t, app, uid)
	if len(removes) != 2 {
		t.Fatalf("apps.grant.remove records = %d %v, want exactly 2", len(removes), removes)
	}
	want := map[string]map[string]any{
		ghCred.ID: {
			"action": "apps.grant.remove", "app": "github", "user": uid, "credentialId": ghCred.ID,
			"reason": "deactivated via SCIM", "actor": "conformance", "actorId": meta.ID, "actorVia": "api-token",
		},
		jiraCred.ID: {
			"action": "apps.grant.remove", "app": "jira", "user": uid, "credentialId": jiraCred.ID,
			"reason": "deactivated via SCIM", "actor": "conformance", "actorId": meta.ID, "actorVia": "api-token",
		},
	}
	for _, ev := range removes {
		id, _ := ev["credentialId"].(string)
		exp, ok := want[id]
		if !ok {
			t.Errorf("record for unexpected credentialId %q: %v", id, ev)
			continue
		}
		delete(want, id)
		for k, v := range exp {
			if ev[k] != v {
				t.Errorf("record %s: %s = %v, want %v", id, k, ev[k], v)
			}
		}
	}
	for id := range want {
		t.Errorf("no apps.grant.remove record for credential %s", id)
	}

	updated := map[string]int{}
	for _, ev := range outboxDataFor(t, app, "straza.apps.updated") {
		if ev["change"] == "grant" {
			name, _ := ev["app"].(string)
			updated[name]++
		}
	}
	if len(updated) != 2 || updated["github"] != 1 || updated["jira"] != 1 {
		t.Errorf("straza.apps.updated grant records per app = %v, want github:1 jira:1", updated)
	}

	// The records reach the hash chain, still exactly one per row.
	deadline := time.Now().Add(10 * time.Second)
	for {
		recs, err := app.store.Audit().List(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var chained []string
		for _, r := range recs {
			if strings.Contains(r.CE, `"action":"apps.grant.remove"`) && strings.Contains(r.CE, `"user":"`+uid+`"`) {
				chained = append(chained, r.CE)
			}
		}
		if len(chained) > 2 {
			t.Fatalf("%d chained apps.grant.remove records, want exactly 2", len(chained))
		}
		if len(chained) == 2 {
			t.Logf("chained grant removal records: %s", strings.Join(chained, "\n"))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chained apps.grant.remove records = %d, want 2", len(chained))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestSCIMPatchInactiveWipesGrants: PATCH active:false shares applyActive
// with DELETE, so it wipes the grants the same way.
func TestSCIMPatchInactiveWipesGrants(t *testing.T) {
	t.Parallel()
	app, base, token := scimTestSetup(t)
	uid := scimCreateUser(t, base, token, `{"userName":"patched@x.io","active":true}`)
	grantUserApp(t, app, "github", uid)

	scimPatch(t, base, token, uid, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if rows := userGrants(t, app, uid); len(rows) != 0 {
		t.Fatalf("grants after PATCH active:false = %d rows, want none", len(rows))
	}
	if removes := grantRemoveEvents(t, app, uid); len(removes) != 1 {
		t.Errorf("apps.grant.remove records = %d %v, want exactly 1", len(removes), removes)
	}
}

// TestAdminDisableAndLockKeepGrants: the admin disable and the lock run the
// same kill-switch cascade as a SCIM deactivation but are reversible, so
// both leave the user's grant rows in place and write no removal record.
func TestAdminDisableAndLockKeepGrants(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	token := mintProvisioningToken(t, base, adminBearer)

	cases := []struct {
		name     string
		userName string
		act      func(t *testing.T, uid string)
	}{
		{"admin disable", "disabled@x.io", func(t *testing.T, uid string) {
			if code := adminReq(t, "PATCH", base+"/v1/admin/users/"+uid, adminBearer,
				map[string]string{"status": "disabled"}, nil); code != http.StatusOK {
				t.Fatalf("disable = %d, want 200", code)
			}
		}},
		{"admin lock", "locked@x.io", func(t *testing.T, uid string) {
			if code := adminReq(t, "POST", base+"/v1/admin/users/"+uid+"/lock", adminBearer,
				map[string]string{"reason": "SOC hold"}, nil); code != http.StatusOK {
				t.Fatalf("lock = %d, want 200", code)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid := scimCreateUser(t, base, token, `{"userName":"`+tc.userName+`","active":true}`)
			grantUserApp(t, app, "app-"+tc.userName, uid)
			tc.act(t, uid)
			if revs, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, uid); err != nil || len(revs) != 1 {
				t.Fatalf("revocations = %v err=%v, want the cascade's one row", revs, err)
			}
			if rows := userGrants(t, app, uid); len(rows) != 1 {
				t.Errorf("grants after %s = %d, want the row kept", tc.name, len(rows))
			}
			if removes := grantRemoveEvents(t, app, uid); len(removes) != 0 {
				t.Errorf("apps.grant.remove records after %s = %v, want none", tc.name, removes)
			}
		})
	}
}
