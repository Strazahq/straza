package server

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/store"
)

// TestEntraSyncAgentAudit pins what the server writes and does when the
// Entra sync creates an agent, finds that it has no single sponsor, takes
// a sponsor from Entra, and disables and enables the agent. Each step
// writes its records once, with the agent, a reason and no actor, and a
// disable puts the agent on the denylist and ends its sessions.
func TestEntraSyncAgentAudit(t *testing.T) {
	t.Parallel()
	app := entraSyncApp(t, "agentIdentities")
	ctx := context.Background()
	ben, err := app.store.Users().Create(ctx, store.User{Username: "ben@example.com", ExternalID: "oid-ben", UserType: store.UserTypeHuman})
	if err != nil {
		t.Fatal(err)
	}
	const id = "3a3a3a3a-1111-4222-8333-444455556666"
	graph := &stubGraph{agents: map[string]entragraph.Agent{}}
	set := func(enabled bool, sponsors ...string) {
		a := entragraph.Agent{ID: id, DisplayName: "ledger-agent", Enabled: &enabled}
		for _, sp := range sponsors {
			a.Sponsors = append(a.Sponsors, entragraph.Sponsor{ID: sp, Kind: entragraph.KindUser})
		}
		graph.agents[id] = a
	}
	syncer := app.entraSyncer(graph)
	syncer.Pass(ctx) // creates the groups

	graph.identities = []entragraph.Member{{ID: id, Kind: entragraph.KindAgentIdentity}}
	set(true)
	syncer.Pass(ctx)
	syncer.Pass(ctx) // changes nothing, so it records nothing
	agent, err := app.store.Users().GetByEntraAgentID(ctx, id)
	if err != nil {
		t.Fatalf("the agent the sync created: %v", err)
	}
	if agent.Origin != store.OriginEntra || agent.Kind() != store.UserKindAgent || agent.Username != "ledger-agent" {
		t.Errorf("the agent = %+v, want an agent of origin entra named as in Entra", agent)
	}
	admin := func(action string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == action && ev["user"] == agent.ID {
				out = append(out, ev)
			}
		}
		return out
	}
	identity := func(action string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, ev := range eventsNaming(t, app, "straza.audit.identity", "user", agent.ID) {
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
		return recs[0]
	}
	if created := one("user.create", admin(actionUserCreate)); created["origin"] != store.OriginEntra || created["userType"] != store.UserTypeAgent {
		t.Errorf("user.create = %v, want an agent of origin entra", created)
	}
	unresolved := one("entra.sponsor.unresolved", admin("entra.sponsor.unresolved"))
	if unresolved["state"] != store.EntraSponsorNone || unresolved["entraAgentId"] != id || unresolved["reason"] == "" {
		t.Errorf("entra.sponsor.unresolved = %v, want the state none, the agent id and a reason", unresolved)
	}

	if got := app.entraAgentOf(ctx, agent); got == nil || got.Identity != store.EntraObjectEnabled || got.AgentUser != "" || got.AgentUserID != "" ||
		got.SponsorState != store.EntraSponsorNone || len(got.Sponsors) != 0 {
		t.Errorf("the agent's Entra state in the admin answer = %+v, want the identity enabled, no agent user and no sponsor", got)
	}
	if got := app.entraAgentOf(ctx, ben); got != nil {
		t.Errorf("a person's Entra state in the admin answer = %+v, want none", got)
	}

	set(true, "oid-ben")
	syncer.Pass(ctx)
	syncer.Pass(ctx)
	if after, _ := app.store.Users().GetByID(ctx, agent.ID); after.Sponsor != ben.Username {
		t.Errorf("the agent's sponsor = %q, want ben", after.Sponsor)
	}
	if resolved := one("entra.sponsor.resolved", admin("entra.sponsor.resolved")); resolved["sponsor"] != ben.Username {
		t.Errorf("entra.sponsor.resolved = %v, want it to name ben", resolved)
	}
	sponsored := one("user.update of the sponsor", admin(actionUserUpdate))
	if sponsored["origin"] != store.OriginEntra || sponsored["reason"] != "the agent's sponsor changed in Entra" {
		t.Errorf("user.update = %v, want the origin entra and the reason that names Entra", sponsored)
	}
	one("entra.sponsor.unresolved after the sponsor was set", admin("entra.sponsor.unresolved"))

	ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: agent.ID, HarnessName: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	set(false, "oid-ben")
	syncer.Pass(ctx)
	syncer.Pass(ctx)
	if after, _ := app.store.Users().GetByID(ctx, agent.ID); after.Status != store.UserDisabled {
		t.Errorf("the agent after the disable in Entra = %+v, want disabled", after)
	}
	if !app.denylist.userBlocked(agent.ID) {
		t.Error("the agent is not on the denylist after the disable in Entra")
	}
	if after, _ := app.store.Sessions().GetByID(ctx, ses.ID); after.Status != store.SessionRevoked {
		t.Errorf("the agent's session after the disable = %q, want revoked", after.Status)
	}
	if killed := one("user.killed", identity("user.killed")); killed["origin"] != store.OriginEntra || killed["reason"] != "the agent identity is disabled in Entra" {
		t.Errorf("user.killed = %v, want the origin entra and the reason that names the agent identity", killed)
	}

	set(true, "oid-ben")
	syncer.Pass(ctx)
	syncer.Pass(ctx)
	if after, _ := app.store.Users().GetByID(ctx, agent.ID); after.Status != store.UserActive {
		t.Errorf("the agent after the enable in Entra = %+v, want active", after)
	}
	if app.denylist.userBlocked(agent.ID) {
		t.Error("the agent is still on the denylist after the enable in Entra")
	}
	one("user.reactivated", identity("user.reactivated"))
}
