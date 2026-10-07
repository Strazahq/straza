package secrets

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestBrokerAppScope pins the server's own secret: Set with no role stores
// one app-scoped row and rotates it in place, AppSecret prefers that row
// over every role row, and ForRoles resolves the granting role first, then
// the smallest held role, then the app row, and nothing when none applies.
func TestBrokerAppScope(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	broker := NewBroker(st, testKEK(t))

	app, err := st.Apps().Create(ctx, store.App{Name: "scout-tools", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.Roles().Create(ctx, store.Role{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := st.Roles().Create(ctx, store.Role{Name: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, app.ID, dev.ID, "dev-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, app.ID, ops.ID, "ops-token"); err != nil {
		t.Fatal(err)
	}

	// Before the app row exists the first role row serves health checks and
	// a session holding none of the roles resolves nothing.
	if s := broker.AppSecret(app.ID); s == nil || s.Value != "dev-token" {
		t.Errorf("app secret before the app row = %+v", s)
	}
	if s := broker.ForRoles(app.ID, []string{"finance"}, ""); s != nil {
		t.Errorf("unbound role without an app row resolved %+v", s)
	}

	appRow, err := broker.Set(ctx, app.ID, "", "server-token")
	if err != nil {
		t.Fatal(err)
	}
	if appRow.Scope != store.CredScopeApp || appRow.OwnerID != app.ID || appRow.Kind != store.CredStatic {
		t.Fatalf("app row = %+v, want scope app owned by the app", appRow)
	}

	cases := []struct {
		name     string
		roles    []string
		granting string
		want     string
	}{
		{name: "granting role wins over the smaller held role", roles: []string{"dev", "ops"}, granting: "ops", want: "ops-token"},
		{name: "no granting role falls to the smallest held role", roles: []string{"ops", "dev"}, granting: "", want: "dev-token"},
		{name: "granting role without its own row falls to the held rows", roles: []string{"ops"}, granting: "finance", want: "ops-token"},
		{name: "no held role row falls to the app row", roles: []string{"finance"}, granting: "finance", want: "server-token"},
		{name: "no roles at all falls to the app row", roles: nil, granting: "", want: "server-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := broker.ForRoles(app.ID, tc.roles, tc.granting)
			if s == nil || s.Value != tc.want {
				t.Errorf("ForRoles = %+v, want %q", s, tc.want)
			}
		})
	}
	if s := broker.AppSecret(app.ID); s == nil || s.Value != "server-token" || s.ID != appRow.ID {
		t.Errorf("app secret = %+v, want the app row", s)
	}

	// Rotation of the app row is in place.
	if _, err := broker.Set(ctx, app.ID, "", "server-token-2"); err != nil {
		t.Fatal(err)
	}
	if s := broker.AppSecret(app.ID); s == nil || s.Value != "server-token-2" {
		t.Errorf("rotated app secret = %+v", s)
	}
	creds, err := st.Credentials().ListByApp(ctx, app.ID)
	if err != nil || len(creds) != 3 {
		t.Fatalf("credential rows = %d err=%v, want 3 (two roles, one app row)", len(creds), err)
	}
	if s := broker.ForRoles("nonexistent-app", nil, ""); s != nil {
		t.Errorf("unknown app resolved %+v", s)
	}

	// The listing names every static row, app row first, with a fingerprint
	// of the plaintext and never the value.
	statics, err := broker.Statics(ctx, app.ID)
	if err != nil || len(statics) != 3 {
		t.Fatalf("statics = %+v err=%v, want 3 rows", statics, err)
	}
	if statics[0].Scope != store.CredScopeApp || statics[0].RoleID != "" || statics[0].Fingerprint != Fingerprint("server-token-2") {
		t.Errorf("app row listing = %+v", statics[0])
	}
	if statics[1].Scope != store.CredScopeRole || statics[1].RoleID != dev.ID || statics[1].Fingerprint != Fingerprint("dev-token") {
		t.Errorf("dev row listing = %+v", statics[1])
	}
	if len(Fingerprint("x")) != 4 {
		t.Errorf("fingerprint length = %d, want 4 hex characters", len(Fingerprint("x")))
	}

	// Removing the app row falls health checks back to the first role row;
	// removing a role row falls its sessions back to the app row or nothing.
	if _, err := broker.Remove(ctx, app.ID, ""); err != nil {
		t.Fatal(err)
	}
	if s := broker.AppSecret(app.ID); s == nil || s.Value != "dev-token" {
		t.Errorf("app secret after removing the app row = %+v", s)
	}
	if _, err := broker.Remove(ctx, app.ID, ""); err == nil {
		t.Error("removing an absent app row did not answer not found")
	}
	if _, err := broker.Remove(ctx, app.ID, dev.ID); err != nil {
		t.Fatal(err)
	}
	if s := broker.ForRoles(app.ID, []string{"dev"}, "dev"); s != nil {
		t.Errorf("dev resolved a secret after its row was removed: %+v", s)
	}
	if err := broker.RemoveAll(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if statics, _ := broker.Statics(ctx, app.ID); len(statics) != 0 {
		t.Errorf("statics after RemoveAll = %+v, want none", statics)
	}
}
