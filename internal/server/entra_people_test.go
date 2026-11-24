package server

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/store"
)

// TestEntraSyncPeopleAudit pins what the server writes and does when the
// Entra sync creates, disables and enables a person. Each step writes its
// records once, with the user, the origin entra, a reason and no actor. A
// disable ends the person's sessions and puts the person on the denylist.
// An enable lifts that, and it does not lift a lock an admin set.
func TestEntraSyncPeopleAudit(t *testing.T) {
	t.Parallel()
	app := entraSyncApp(t, "people")
	ctx := context.Background()
	graph := &stubGraph{}
	syncer := app.entraSyncer(graph)
	syncer.Pass(ctx) // creates the groups

	member := func(enabled bool) []entragraph.Member {
		return []entragraph.Member{{ID: "oid-dan", Kind: entragraph.KindUser, Enabled: &enabled, UPN: "dan@example.com", DisplayName: "Dan Lab"}}
	}
	graph.members = member(true)
	syncer.Pass(ctx)
	dan, err := app.store.Users().GetByUsername(ctx, "dan@example.com")
	if err != nil {
		t.Fatalf("the person the sync created: %v", err)
	}
	if dan.Origin != store.OriginEntra || dan.UserType != store.UserTypeHuman {
		t.Errorf("dan = %+v, want a person of origin entra", dan)
	}
	// admin and identity answer the records that name dan under the two
	// audit subjects, by action.
	admin := func(action string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == action && ev["user"] == dan.ID {
				out = append(out, ev)
			}
		}
		return out
	}
	identity := func(action string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, ev := range eventsNaming(t, app, "straza.audit.identity", "user", dan.ID) {
			if ev["action"] == action {
				out = append(out, ev)
			}
		}
		return out
	}
	one := func(what string, recs []map[string]any) map[string]any {
		t.Helper()
		if len(recs) != 1 {
			t.Fatalf("%s = %v, want one record", what, recs)
		}
		noActorIn(t, recs[0])
		if recs[0]["origin"] != store.OriginEntra {
			t.Errorf("%s = %v, want the origin entra", what, recs[0])
		}
		return recs[0]
	}
	created := one("user.create", admin(actionUserCreate))
	if created["username"] != "dan@example.com" || created["userType"] != store.UserTypeHuman ||
		created["reason"] != "a member of a role's group in Entra" {
		t.Errorf("user.create = %v, want dan as a human with the reason that names Entra", created)
	}

	ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: dan.ID, HarnessName: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	graph.members = member(false)
	syncer.Pass(ctx)
	syncer.Pass(ctx) // changes nothing, so it records nothing
	if after, _ := app.store.Users().GetByID(ctx, dan.ID); after.Status != store.UserDisabled {
		t.Errorf("dan after the disable in Entra = %+v, want disabled", after)
	}
	if !app.denylist.userBlocked(dan.ID) {
		t.Error("dan is not on the denylist after the disable in Entra")
	}
	if after, _ := app.store.Sessions().GetByID(ctx, ses.ID); after.Status != store.SessionRevoked {
		t.Errorf("dan's session after the disable = %q, want revoked", after.Status)
	}
	killed := one("user.killed", identity("user.killed"))
	if killed["reason"] != "the account is disabled in Entra" {
		t.Errorf("user.killed = %v, want the reason that names Entra", killed)
	}
	update := one("user.update of the disable", admin(actionUserUpdate))
	if update["reason"] != "the account is disabled in Entra" {
		t.Errorf("user.update = %v, want the reason that names Entra", update)
	}

	// An admin locks dan. Entra's enable must not let dan back in.
	if rowErr, eventErr := app.revokeUserCtx(ctx, dan.ID, "under investigation", store.RevocationOriginAdmin); rowErr != nil || eventErr != nil {
		t.Fatalf("lock: %v, %v", rowErr, eventErr)
	}
	graph.members = member(true)
	syncer.Pass(ctx)
	if after, _ := app.store.Users().GetByID(ctx, dan.ID); after.Status != store.UserActive {
		t.Errorf("dan after the enable in Entra = %+v, want the status active", after)
	}
	if !app.denylist.userBlocked(dan.ID) {
		t.Error("Entra's enable lifted the lock an admin set")
	}
	blocked := one("user.lift.blocked", identity("user.lift.blocked"))
	if held, _ := blocked["heldBy"].([]any); len(held) != 1 || held[0] != store.RevocationOriginAdmin {
		t.Errorf("user.lift.blocked = %v, want it held by admin", blocked)
	}
	if got := identity("user.reactivated"); len(got) != 0 {
		t.Errorf("user.reactivated records while the lock holds = %v, want none", got)
	}

	// Without the lock, a disable and an enable from Entra end with dan
	// let back in.
	app.allowUserCtx(ctx, dan.ID)
	graph.members = member(false)
	syncer.Pass(ctx)
	graph.members = member(true)
	syncer.Pass(ctx)
	if app.denylist.userBlocked(dan.ID) {
		t.Error("dan is still on the denylist after Entra enabled the account")
	}
	var fromEntra []map[string]any
	for _, ev := range identity("user.reactivated") {
		if ev["origin"] == store.OriginEntra {
			fromEntra = append(fromEntra, ev)
		}
	}
	one("user.reactivated of origin entra", fromEntra)
}

// TestEntraEnableLiftsASCIMDeactivation pins a person the identity manager
// deactivated before Entra owned people: once Entra says the account is
// enabled, the person is let back in, because SCIM can no longer lift its
// own deactivation of a person.
func TestEntraEnableLiftsASCIMDeactivation(t *testing.T) {
	t.Parallel()
	app := entraSyncApp(t, "people")
	ctx := context.Background()
	ben, err := app.store.Users().Create(ctx, store.User{Username: "ben@example.com", ExternalID: "oid-ben", Origin: store.OriginSCIM})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.scimDeactivate(ctx, ben.ID, "deactivated via SCIM"); err != nil {
		t.Fatal(err)
	}
	graph := &stubGraph{}
	syncer := app.entraSyncer(graph)
	syncer.Pass(ctx)
	enabled := true
	graph.members = []entragraph.Member{{ID: "oid-ben", Kind: entragraph.KindUser, Enabled: &enabled, UPN: "ben@example.com"}}
	syncer.Pass(ctx)

	after, err := app.store.Users().GetByID(ctx, ben.ID)
	if err != nil || after.Status != store.UserActive || after.Origin != store.OriginEntra {
		t.Fatalf("ben after Entra's enable = %+v, %v; want active with origin entra", after, err)
	}
	if app.denylist.userBlocked(ben.ID) {
		t.Error("ben is active and still on the denylist, held by the identity manager's deactivation")
	}
}
