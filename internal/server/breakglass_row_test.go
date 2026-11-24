package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestBreakGlassHostPasswordRefusesRow pins that SetBreakGlassPassword
// refuses a break-glass row the emergency sign-in refuses whatever its
// password, an AI agent, a service account or a disabled row, as an older
// version could leave it. The sentence says what is wrong with the row and
// what to do, no password is answered, and the hash and the outbox stay as
// they were.
func TestBreakGlassHostPasswordRefusesRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	notPerson := "the break-glass admin is stored as %s, and the emergency sign-in signs in a person only, so nothing changed. " +
		"Make it a person again: an admin sends PATCH /v1/admin/users/%s with the body {\"user_type\": \"\"}, " +
		"or, when no admin can sign in, empty the user_type column of the break-glass row in the users table. Then run the command again"
	cases := []struct {
		name string
		edit func(u *store.User)
		want func(id string) string
	}{
		{"an agent", func(u *store.User) { u.UserType = store.UserTypeAgent }, func(id string) string { return fmt.Sprintf(notPerson, "an agent", id) }},
		{"a service account", func(u *store.User) { u.UserType = store.UserTypeService }, func(id string) string { return fmt.Sprintf(notPerson, "a service account", id) }},
		{"a disabled row", func(u *store.User) { u.Status = store.UserDisabled }, func(string) string {
			return "the break-glass admin has the status disabled, and the emergency sign-in signs in an active account only, so nothing changed. " +
				"Set the status column of the break-glass row in the users table to active, then run the command again"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := bootStore(t)
			if err := bootAppAs(st, slog.New(slog.DiscardHandler), config.ProfileEnterprise).ensureBreakGlass(ctx); err != nil {
				t.Fatal(err)
			}
			u, err := st.Users().GetByUsername(ctx, BreakGlassUsername)
			if err != nil {
				t.Fatal(err)
			}
			u.PasswordHash = "kept-hash"
			tc.edit(&u)
			// The store clears the hash of an AI agent or a service account,
			// so the hash to keep is the one the edit left.
			if u, err = st.Users().Update(ctx, u); err != nil {
				t.Fatal(err)
			}
			before, err := st.Outbox().ListRecent(ctx, 1000)
			if err != nil {
				t.Fatal(err)
			}
			password, err := SetBreakGlassPassword(ctx, st.Users())
			if want := tc.want(u.ID); err == nil || err.Error() != want || password != "" {
				t.Errorf("SetBreakGlassPassword = %q, %v, want no password and %q", password, err, want)
			}
			after, err := st.Outbox().ListRecent(ctx, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if row, err := st.Users().GetByID(ctx, u.ID); err != nil || row.PasswordHash != u.PasswordHash || len(after) != len(before) {
				t.Errorf("the refusal left hash %q and %d new outbox rows, want %q and none", row.PasswordHash, len(after)-len(before), u.PasswordHash)
			}
		})
	}
}

// TestBreakGlassTypeChangeRefused pins the lockout guarantee on the type of
// the break-glass admin. A PATCH that would make it an AI agent or a
// service account answers 403 with the sentence and writes nothing, also
// when it changes another field too. A PATCH that leaves it a person
// passes. A row an older version retyped is refused by the host command
// until such a PATCH makes it a person again, and then takes a password.
func TestBreakGlassTypeChangeRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	bg, err := app.store.Users().GetByUsername(ctx, BreakGlassUsername)
	if err != nil {
		t.Fatal(err)
	}
	patch := func(body map[string]any) (int, map[string]any) {
		code, _, out := callJSON(t, http.MethodPatch, base+"/v1/admin/users/"+bg.ID, admin, body)
		return code, out
	}
	const refused = "the break-glass admin cannot be made an AI agent or a service account (lockout guarantee)"
	for _, body := range []map[string]any{
		{"user_type": "agent"},
		{"user_type": " Service "},
		{"user_type": "agent", "display": "Somebody else"},
	} {
		if code, out := patch(body); code != http.StatusForbidden || out["error"] != refused {
			t.Errorf("PATCH %v = %d %v, want 403 %q", body, code, out, refused)
		}
		row, err := app.store.Users().GetByID(ctx, bg.ID)
		if err != nil || row.UserType != bg.UserType || row.Display != bg.Display || row.PasswordHash != bg.PasswordHash {
			t.Errorf("the refused PATCH %v changed the row to %+v", body, row)
		}
	}
	for _, v := range []string{store.UserTypeHuman, ""} {
		if code, out := patch(map[string]any{"user_type": v}); code != http.StatusOK {
			t.Errorf("PATCH user_type %q = %d %v, want 200", v, code, out)
		}
	}

	retyped, err := app.store.Users().GetByID(ctx, bg.ID)
	if err != nil {
		t.Fatal(err)
	}
	retyped.UserType = store.UserTypeAgent
	if _, err := app.store.Users().Update(ctx, retyped); err != nil {
		t.Fatal(err)
	}
	if _, err := SetBreakGlassPassword(ctx, app.store.Users()); err == nil || !strings.Contains(err.Error(), "the break-glass admin is stored as an agent") {
		t.Fatalf("SetBreakGlassPassword on a retyped row = %v, want the refusal", err)
	}
	if code, out := patch(map[string]any{"user_type": ""}); code != http.StatusOK {
		t.Fatalf("PATCH user_type \"\" on the retyped row = %d %v, want 200", code, out)
	}
	if _, err := SetBreakGlassPassword(ctx, app.store.Users()); err != nil {
		t.Fatalf("SetBreakGlassPassword after the row is a person again: %v", err)
	}
}
