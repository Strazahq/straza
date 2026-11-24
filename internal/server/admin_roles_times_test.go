package server

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

// TestRoleUpdatedAtMovesWithEveryPublish pins created_at and updated_at on
// the roles list: updated_at moves when a published draft changes a role's
// tools, when one changes only its description, and when one removes a
// role it composes, and it stays when a published draft leaves the role
// alone. created_at never moves. The update route answers both times too.
func TestRoleUpdatedAtMovesWithEveryPublish(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root,
		map[string]any{"name": "github-helpers", "server": "github", "tools": []string{"get_*"}}, nil); code != http.StatusCreated {
		t.Fatalf("create github-helpers = %d", code)
	}
	publish := func(t *testing.T, body map[string]any) {
		t.Helper()
		code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, body)
		if code != http.StatusCreated {
			t.Fatalf("create draft = %d %q %v", code, a.Error, a.Findings)
		}
		if code, ans := f.publishAll(t, f.root, a.Draft.ID); code != http.StatusOK {
			t.Fatalf("publish draft %s = %d %q", a.Draft.ID, code, ans.Error)
		}
	}
	docs := func(d ...string) map[string]any { return map[string]any{"documents": d} }
	team := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: zz-team\nspec:\n    kind: business\n    implies:\n        - zz-gone\n"
	publish(t, docs(draftRole("zz-desc", "One."), draftRole("zz-gone", "Goes.")))
	publish(t, docs(team))

	type times struct{ created, updated time.Time }
	read := func(t *testing.T) map[string]times {
		t.Helper()
		var rows []map[string]any
		if code := adminReq(t, http.MethodGet, f.base+"/v1/admin/roles", f.root, nil, &rows); code != http.StatusOK {
			t.Fatalf("roles list = %d", code)
		}
		out := map[string]times{}
		for _, row := range rows {
			c, errC := time.Parse(time.RFC3339, row["created_at"].(string))
			u, errU := time.Parse(time.RFC3339, row["updated_at"].(string))
			if errC != nil || errU != nil || u.Before(c) {
				t.Fatalf("role %v answers created_at %v and updated_at %v", row["name"], row["created_at"], row["updated_at"])
			}
			out[row["name"].(string)] = times{c, u}
		}
		return out
	}

	steps := []struct {
		name  string
		body  map[string]any
		moved []string
	}{
		{"a draft that changes the tools", docs(typedRole("github-helpers", "github", "get_*", "list_*")), []string{"github-helpers"}},
		{"a draft that changes only the description", docs(draftRole("zz-desc", "Two.")), []string{"zz-desc"}},
		{"a draft that leaves the roles alone", docs(draftRole("zz-other", "Other.")), nil},
		{"a draft that removes a composed role", map[string]any{"items": []map[string]string{{"kind": "Role", "name": "zz-gone", "op": "remove"}}}, []string{"zz-team"}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			before := read(t)
			publish(t, step.body)
			after := read(t)
			for _, name := range []string{"github-helpers", "zz-desc", "zz-team"} {
				was, now := before[name], after[name]
				if moved := now.updated.After(was.updated); moved != slices.Contains(step.moved, name) || now.updated.Before(was.updated) {
					t.Errorf("%s updated_at %v then %v, want it to move only when the step names it", name, was.updated, now.updated)
				}
				if !now.created.Equal(was.created) {
					t.Errorf("%s created_at moved from %v to %v", name, was.created, now.created)
				}
			}
		})
	}

	desc := read(t)["zz-desc"]
	var updated map[string]any
	if code := adminReq(t, http.MethodPatch, f.base+"/v1/admin/roles/"+mustRole(t, f.app, "zz-desc").ID, f.root,
		map[string]any{"description": "Three."}, &updated); code != http.StatusOK {
		t.Fatalf("update zz-desc = %d %v", code, updated)
	}
	stamp, _ := updated["updated_at"].(string)
	u, err := time.Parse(time.RFC3339, stamp)
	if err != nil || !u.After(desc.updated) || updated["created_at"] == nil {
		t.Errorf("update answered created_at %v and updated_at %v, want both, updated_at after %v", updated["created_at"], updated["updated_at"], desc.updated)
	}
}
