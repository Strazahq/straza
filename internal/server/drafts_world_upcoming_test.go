package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestAssignmentAt pins where an assignment stands at an instant, as the
// identity resolver reads its window: valid_from inclusive and valid_to
// exclusive, so a row that starts at that instant is in force and a row
// that ends at it is not.
func TestAssignmentAt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		v := now.Add(d)
		return &v
	}
	cases := []struct {
		name           string
		from, to       *time.Time
		inForce, later bool
	}{
		{"no window", nil, nil, true, false},
		{"a start equal to now", at(0), nil, true, false},
		{"a start before now and an end after it", at(-time.Hour), at(time.Hour), true, false},
		{"a start one nanosecond after now", at(time.Nanosecond), nil, false, true},
		{"a start after now and an end after that", at(time.Hour), at(2 * time.Hour), false, true},
		{"an end equal to now", nil, at(0), false, false},
		{"an end before now", at(-2 * time.Hour), at(-time.Hour), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inForce, later := assignmentAt(store.RoleAssignment{ValidFrom: tc.from, ValidTo: tc.to}, now)
			if inForce != tc.inForce || later != tc.later {
				t.Errorf("in force %v, later %v, want %v and %v", inForce, later, tc.inForce, tc.later)
			}
		})
	}
}

// TestAgentDraftChecksEveryRoleSetItWillHold pins the check of an agent's
// draft through the straza-app door against every set of roles the agent
// holds while its assignments start and end. A draft that widens a role
// the agent holds from tomorrow is refused as agent.own-reach. So is a
// draft that widens a role starting after a wider role ends, though the
// two roles together reach no more. The agent reads that refusal, the one
// draft.check record of the draft names that code alone with no actor, and
// the one draft.create record names the agent as its actor.
func TestAgentDraftChecksEveryRoleSetItWillHold(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()
	// grant is an owned role of jira with its tools, assigned from from to
	// to after now, where zero leaves that bound open.
	type grant struct {
		role, tools string
		from, to    time.Duration
	}
	cases := []struct {
		name, agent, widened string
		grants               []grant
	}{
		{"a role the agent holds from tomorrow", "later-bot", "jira-later", []grant{{"jira-later", `["get_issue"]`, 24 * time.Hour, 0}}},
		{"a role that starts after a wider role ends", "rotating-bot", "jira-next", []grant{
			{"jira-now", `["get_issue","delete_issue"]`, 0, time.Hour}, {"jira-next", `["get_issue"]`, 2 * time.Hour, 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, tok := r.agent(t, tc.agent)
			for _, g := range tc.grants {
				role, _, err := r.app.store.Roles().CreateOwned(ctx, store.Role{Name: g.role, OwnerAppID: r.jira.ID}, g.tools)
				if err != nil {
					t.Fatal(err)
				}
				asg := store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID}
				if g.from != 0 {
					from := time.Now().Add(g.from)
					asg.ValidFrom = &from
				}
				if g.to != 0 {
					to := time.Now().Add(g.to)
					asg.ValidTo = &to
				}
				if _, err := r.app.store.Roles().Assign(ctx, asg); err != nil {
					t.Fatal(err)
				}
			}
			doc := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: " + tc.widened + "\nspec:\n    kind: application\n    server: jira\n" +
				"    bindings:\n        - app: jira\n          tools: [get_issue, delete_issue]\n"
			text, isErr, sc := submit(t, r.base, tok, map[string]any{"documents": []string{doc}})
			if isErr {
				t.Fatalf("submit = %q, want the draft stored for the check", text)
			}
			id, _ := sc["draft"].(string)
			r.awaitChecked(t, id)
			_, _, sc = status(t, r.base, tok, id)
			want := []string{"agent.own-reach"}
			if got := agentCodes(sc["refused"]); !reflect.DeepEqual(got, want) {
				t.Fatalf("refused codes = %v, want %v, in the status %v", got, want, sc)
			}
			const sentence = "This draft would let you reach tools of jira that nobody has listed yet, which widens your own reach. An agent's draft may not."
			if f := findingOf(sc["refused"], "agent.own-reach"); f["sentence"] != sentence || f["fix"] != "Leave that change out. A person can make it." {
				t.Errorf("the refusal = %v, want the sentence %q and its fix", f, sentence)
			}
			var checks, creates []map[string]any
			waitFor(t, "the draft.create and draft.check records of draft "+id, func() bool {
				checks, creates = draftRecords(t, r.app, "draft.check", id), draftRecords(t, r.app, "draft.create", id)
				return len(checks) > 0 && len(creates) > 0
			})
			if len(checks) != 1 || !reflect.DeepEqual(agentCodes(checks[0]["refused"]), want) {
				t.Errorf("draft.check records = %v, want one whose refused codes are %v", checks, want)
			}
			noActorIn(t, checks[0])
			if len(creates) != 1 || creates[0]["actor"] != tc.agent || creates[0]["actorId"] != u.ID {
				t.Errorf("draft.create records = %v, want one with %s as its actor", creates, tc.agent)
			}
		})
	}
}
