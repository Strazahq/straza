package membership

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// fakeRoles lands every add and every remove except the user ids in skip,
// which stand for rows a concurrent write changed first.
type fakeRoles struct {
	err    error
	skip   map[string]bool
	owner  map[string]string // row id to user id, for removes
	origin string            // the origin of the rows a remove returns
	adds   []store.RoleAssignment
}

func (f *fakeRoles) ApplyMembership(_ context.Context, _ string, add []store.RoleAssignment, removeIDs []string) ([]store.RoleAssignment, []store.RoleAssignment, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	f.adds = add
	var added, removed []store.RoleAssignment
	for _, a := range add {
		if !f.skip[a.SubjectID] {
			a.ID = "new-" + a.SubjectID
			added = append(added, a)
		}
	}
	for _, id := range removeIDs {
		if u := f.owner[id]; !f.skip[u] {
			removed = append(removed, store.RoleAssignment{ID: id, SubjectID: u, Origin: f.origin})
		}
	}
	return added, removed, nil
}

func TestApply(t *testing.T) {
	boom := errors.New("the store is down")
	cases := []struct {
		name      string
		roles     *fakeRoles
		steps     []Step
		want      []Change
		wantIDs   []string // IdentityChanged ids in order
		wantErr   error
		wantWrite bool
	}{
		{
			name:  "a remove and an add land in step order with the origin of each row",
			roles: &fakeRoles{owner: map[string]string{"row-a": "ann"}, origin: "admin"},
			steps: []Step{{UserID: "ann", Action: Unassign, RowID: "row-a"}, {UserID: "bob", Action: Assign}},
			want: []Change{
				{AssignmentID: "row-a", RoleID: "r1", UserID: "ann", Origin: "admin", Action: Unassign},
				{AssignmentID: "new-bob", RoleID: "r1", UserID: "bob", Origin: "entra", Action: Assign},
			},
			wantIDs: []string{"r1", "ann", "bob"}, wantWrite: true,
		},
		{
			name:    "a step whose row a concurrent write changed first is left out",
			roles:   &fakeRoles{skip: map[string]bool{"bob": true}},
			steps:   []Step{{UserID: "bob", Action: Assign}, {UserID: "cy", Action: Assign}},
			want:    []Change{{AssignmentID: "new-cy", RoleID: "r1", UserID: "cy", Origin: "entra", Action: Assign}},
			wantIDs: []string{"r1", "cy"}, wantWrite: true,
		},
		{
			name:    "a failed write reports nothing",
			roles:   &fakeRoles{err: boom},
			steps:   []Step{{UserID: "bob", Action: Assign}},
			wantErr: boom,
		},
		{
			name:    "no steps write nothing and still report the role",
			roles:   &fakeRoles{},
			wantIDs: []string{"r1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ids []string
			var reported []Change
			got, err := Apply(context.Background(), Deps{
				Roles:           tc.roles,
				IdentityChanged: func(_ context.Context, id string) { ids = append(ids, id) },
				Changed:         func(_ context.Context, c Change) { reported = append(reported, c) },
			}, "r1", "entra", tc.steps)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Apply error = %v, want %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Apply changes = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(reported, tc.want) {
				t.Errorf("Changed saw %+v, want each change once: %+v", reported, tc.want)
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Errorf("IdentityChanged saw %v, want %v", ids, tc.wantIDs)
			}
			if wrote := tc.roles.adds != nil; wrote != tc.wantWrite && tc.wantErr == nil && len(tc.steps) > 0 {
				t.Errorf("store written = %v, want %v", wrote, tc.wantWrite)
			}
			for _, a := range tc.roles.adds {
				if a.Origin != "entra" {
					t.Errorf("an added row carries origin %q, want the origin Apply was given", a.Origin)
				}
			}
		})
	}
}
