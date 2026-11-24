package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestMembershipRemovalDropsSubjects pins that a role taken from a user
// drops the cached subject of each of the user's sessions and of nobody
// else, on the server that wrote the removal and on a server that hears of
// it, so the session's next call checks in again and loses the role then.
func TestMembershipRemovalDropsSubjects(t *testing.T) {
	t.Parallel()
	app, base, token := scimTestSetup(t)
	ctx := context.Background()
	role, err := app.store.Roles().Create(ctx, store.Role{Name: "removal-dev", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	bob := scimCreateUser(t, base, token, `{"userName":"bob@x.io"}`)
	sam := scimCreateUser(t, base, token, `{"userName":"sam@x.io"}`)
	sessionOf := func(userID string) string {
		t.Helper()
		ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: userID, HarnessName: "claude-code"})
		if err != nil {
			t.Fatal(err)
		}
		return ses.ID
	}
	cache := func(ids ...string) {
		for _, id := range ids {
			app.subjects.put(id, policy.Subject{User: id, Roles: []string{role.Name}}, time.Time{})
		}
	}
	held := func(id string) bool { _, ok := app.subjects.get(id); return ok }
	bobSes, samSes := sessionOf(bob), sessionOf(sam)
	add := func(user string) {
		t.Helper()
		scimGroupPatch(t, base, token, role.ID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"`+user+`"}]}]}`)
	}
	add(bob)
	add(sam)

	steps := []struct {
		name   string
		remove func()
	}{
		{"a SCIM removal", func() {
			scimGroupPatch(t, base, token, role.ID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"remove","path":"members[value eq \"`+bob+`\"]"}]}`)
		}},
		{"an identity event from another server", func() {
			ev, err := json.Marshal(map[string]any{"source": "strazad/other", "data": map[string]any{"id": bob}})
			if err != nil {
				t.Fatal(err)
			}
			app.convCons.Handle(ctx, "straza.identity.updated", ev)
		}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			cache(bobSes, samSes)
			step.remove()
			if held(bobSes) {
				t.Errorf("the session of the user who lost the role kept its cached subject")
			}
			if !held(samSes) {
				t.Errorf("the session of a user who kept the role lost its cached subject")
			}
		})
	}
}
