package server

import (
	"context"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// oldDraftConfigWords is the drafting role's description as stores booted
// before the rename hold it.
const oldDraftConfigWords = "Lets an agent propose config drafts through the built-in straza app's tools straza__draft_submit and straza__draft_status. " +
	"A person publishes them. It opens no console area, and straza-admin does not include it."

// descriptionLine opens the Info line of a boot that rewrites a product
// role's stored description.
const descriptionLine = "replaced the description an earlier release gave a product role with the current product text"

// personWords is a description a person gave the drafting role.
const personWords = "ACME: only the platform bot drafts here; ticket OPS-12."

// TestProductRoleDescriptionAtBoot pins that a boot gives a product role
// that already exists the product's current description only when the
// stored one is the text an earlier release created it with, on SQLite and
// on Postgres. Only the description changes: the id, name, plane, kind and
// holders stay. The change logs one Info line naming the role and announces
// the role once. A role that holds the product's words, or words a person
// gave it, gets no write, no line and no event.
func TestProductRoleDescriptionAtBoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name          string
		stored, want  string
		lines, events int
	}{
		{name: "a role that holds the old words gets the product's words", stored: oldDraftConfigWords, want: DraftConfigRoleDescription, lines: 1, events: 1},
		{name: "a role that holds the product's words is left as it is", stored: DraftConfigRoleDescription, want: DraftConfigRoleDescription},
		{name: "a role that holds a person's own words keeps them", stored: personWords, want: personWords},
	}
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					raw := repairStores(t, dialect == "postgres", 1)[0]
					if _, err := raw.Roles().Create(ctx, store.Role{Name: DraftConfigRole, Description: tc.stored, Plane: store.RolePlaneControl}); err != nil {
						t.Fatal(err)
					}
					holder, err := raw.Users().Create(ctx, store.User{Username: "l6-drafter", Display: "L6 drafter", UserType: "agent"})
					if err != nil {
						t.Fatal(err)
					}
					before, err := raw.Roles().GetByName(ctx, DraftConfigRole)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := raw.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: holder.ID, RoleID: before.ID}); err != nil {
						t.Fatal(err)
					}
					logger, logs := captureLogger()
					if err := bootApp(raw, logger).ensureProductRoles(ctx); err != nil {
						t.Fatalf("ensureProductRoles = %v, want the boot to continue", err)
					}
					after, err := raw.Roles().GetByName(ctx, DraftConfigRole)
					if err != nil {
						t.Fatal(err)
					}
					if after.Description != tc.want {
						t.Errorf("description after the boot = %q, want %q", after.Description, tc.want)
					}
					if after.ID != before.ID || after.Name != before.Name || after.Plane != before.Plane || after.Kind != before.Kind {
						t.Errorf("the boot changed more than the description: before %+v, after %+v", before, after)
					}
					if tc.lines == 0 && !after.UpdatedAt.Equal(before.UpdatedAt) {
						t.Errorf("a role the boot must leave was written: updated_at %v, before %v", after.UpdatedAt, before.UpdatedAt)
					}
					held, err := raw.Roles().AssignmentsByRole(ctx, after.ID)
					if err != nil {
						t.Fatal(err)
					}
					if len(held) != 1 || held[0].SubjectID != holder.ID {
						t.Errorf("holders after the boot = %+v, want the one drafter", held)
					}
					lines := 0
					for _, line := range strings.Split(logs.String(), "\n") {
						if !strings.Contains(line, descriptionLine) {
							continue
						}
						lines++
						if !strings.Contains(line, "level=INFO") || !strings.Contains(line, "role="+DraftConfigRole) {
							t.Errorf("description line = %q, want an INFO line that names %s", line, DraftConfigRole)
						}
					}
					if lines != tc.lines {
						t.Errorf("description lines = %d, want %d:\n%s", lines, tc.lines, logs.String())
					}
					if n := announced(t, raw, before.ID); n != tc.events {
						t.Errorf("straza.identity.updated events naming the drafting role = %d, want %d", n, tc.events)
					}
				})
			}
		})
	}
}
