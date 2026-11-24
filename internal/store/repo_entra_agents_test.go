package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestEntraAgents pins what the store keeps of an agent from Entra on both
// drivers: a row is stored whole and replaced whole, an unknown word is
// refused, a user that does not exist gets no row, and the move below
// migration 46 and back up keeps the agent and drops only the row.
func TestEntraAgents(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		agent, err := s.Users().Create(ctx, User{
			Username: "lab-entra-born-agent", UserType: UserTypeAgent, Origin: OriginEntra,
			EntraAgentID: "d3d3d3d3-0000-4000-8000-000000000003",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.EntraAgents().Get(ctx, agent.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get before any row = %v, want ErrNotFound", err)
		}
		first := EntraAgent{UserID: agent.ID, Identity: EntraObjectEnabled}
		if err := s.EntraAgents().Put(ctx, first); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if got, err := s.EntraAgents().Get(ctx, agent.ID); err != nil || !reflect.DeepEqual(got, first) {
			t.Errorf("a new row = %+v, %v, want %+v", got, err, first)
		}
		second := EntraAgent{
			UserID: agent.ID, Identity: EntraObjectEnabled, AgentUser: EntraObjectDisabled, AgentUserID: "d5d5d5d5-0000-4000-8000-000000000005",
			SponsorState: EntraSponsorSeveral,
			Sponsors:     []EntraSponsor{{ID: "d4d4d4d4-0000-4000-8000-000000000004", Kind: "user"}, {ID: "d6d6d6d6-0000-4000-8000-000000000006", Kind: "user"}},
		}
		if err := s.EntraAgents().Put(ctx, second); err != nil {
			t.Fatalf("Put again: %v", err)
		}
		if got, err := s.EntraAgents().Get(ctx, agent.ID); err != nil || !reflect.DeepEqual(got, second) {
			t.Errorf("a replaced row = %+v, %v, want %+v", got, err, second)
		}
		if got, err := s.EntraAgents().GetByAgentUser(ctx, second.AgentUserID); err != nil || !reflect.DeepEqual(got, second) {
			t.Errorf("the row by its agent user = %+v, %v, want %+v", got, err, second)
		}
		for _, id := range []string{"", "no-such-agent-user"} {
			if _, err := s.EntraAgents().GetByAgentUser(ctx, id); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetByAgentUser(%q) = %v, want ErrNotFound", id, err)
			}
		}
		if rows, err := s.EntraAgents().List(ctx); err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], second) {
			t.Errorf("List = %+v, %v, want the one row", rows, err)
		}
		for _, bad := range []EntraAgent{
			{UserID: agent.ID, Identity: "asleep"},
			{UserID: agent.ID, AgentUser: "asleep"},
			{UserID: agent.ID, SponsorState: "unsure"},
			{UserID: "no-such-user", Identity: EntraObjectEnabled},
		} {
			if err := s.EntraAgents().Put(ctx, bad); err == nil {
				t.Errorf("Put(%+v) was stored, want it refused", bad)
			}
		}
		if got, err := s.EntraAgents().Get(ctx, agent.ID); err != nil || !reflect.DeepEqual(got, second) {
			t.Errorf("the row after the refused writes = %+v, %v, want it unchanged", got, err)
		}

		// The row of a deleted user is not found by its agent user.
		gone, err := s.Users().Create(ctx, User{Username: "old-agent", UserType: UserTypeAgent, Origin: OriginEntra})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.EntraAgents().Put(ctx, EntraAgent{UserID: gone.ID, AgentUser: EntraObjectDisabled, AgentUserID: "agent-user-of-the-old-agent"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().SoftDelete(ctx, gone.ID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EntraAgents().GetByAgentUser(ctx, "agent-user-of-the-old-agent"); !errors.Is(err, ErrNotFound) {
			t.Errorf("the row of a deleted user by its agent user = %v, want ErrNotFound", err)
		}

		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		for v := head; v >= 46; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		if got, err := s.Users().GetByID(ctx, agent.ID); err != nil || got.EntraAgentID != agent.EntraAgentID {
			t.Errorf("the agent below 46 = %+v, %v; want it kept", got, err)
		}
		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(head) = %d, %v", got, err)
		}
		if _, err := s.EntraAgents().Get(ctx, agent.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after the move down and up = %v, want ErrNotFound", err)
		}
	})
}

// TestEntraAgentActive pins when Entra lets an agent act: at least one of
// its two objects is listed and neither is disabled.
func TestEntraAgentActive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		identity, agentUser string
		want                bool
	}{
		{"", "", false},
		{EntraObjectEnabled, "", true},
		{"", EntraObjectEnabled, true},
		{EntraObjectEnabled, EntraObjectEnabled, true},
		{EntraObjectDisabled, "", false},
		{"", EntraObjectDisabled, false},
		{EntraObjectEnabled, EntraObjectDisabled, false},
		{EntraObjectDisabled, EntraObjectEnabled, false},
	} {
		if got := (EntraAgent{Identity: tc.identity, AgentUser: tc.agentUser}).Active(); got != tc.want {
			t.Errorf("identity %q, agent user %q: Active = %v, want %v", tc.identity, tc.agentUser, got, tc.want)
		}
	}
}
