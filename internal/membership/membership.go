// Package membership stores a change of who holds a role and reports it.
// Every identity source that assigns roles calls Apply, so a grant reaches
// the store, the role resolver and the audit trail by one path whatever
// source wrote it.
package membership

import (
	"context"

	"github.com/strazahq/straza/internal/store"
)

// Directions of a Step and of a Change.
const (
	Assign   = "assign"
	Unassign = "unassign"
)

// Step is one requested change of a role's holders: UserID starts holding
// the role, or the assignment row RowID of UserID ends.
type Step struct {
	UserID string
	Action string // Assign or Unassign
	RowID  string // the row an Unassign ends, unused by an Assign
}

// Change is one assignment that a write started or ended: the row id, the
// role, the user, the row's origin and the direction.
type Change struct {
	AssignmentID string
	RoleID       string
	UserID       string
	Origin       string
	Action       string
}

// Roles is the part of the store that Apply writes through.
type Roles interface {
	ApplyMembership(ctx context.Context, roleID string, add []store.RoleAssignment, removeIDs []string) (added, removed []store.RoleAssignment, err error)
}

// Deps are what Apply writes to and reports through.
type Deps struct {
	Roles Roles
	// IdentityChanged reports the id of a role or a user whose holding
	// changed, so role resolutions are read again and the identity event is
	// written.
	IdentityChanged func(ctx context.Context, id string)
	// Changed reports one started or ended assignment, so the audit record
	// for it is written. nil means nobody records it.
	Changed func(ctx context.Context, c Change)
}

// Apply stores steps for the role roleID in one transaction, with origin on
// every row it starts, and returns the changes that landed in step order. A
// step whose row a concurrent write already changed lands nothing and is
// left out. After the write it reports the role, then each changed user
// once, then every change. A failed write returns the error and reports
// nothing, because nothing changed. With no steps it writes nothing and
// still reports the role.
func Apply(ctx context.Context, d Deps, roleID, origin string, steps []Step) ([]Change, error) {
	changed, err := write(ctx, d.Roles, roleID, origin, steps)
	if err != nil {
		return nil, err
	}
	d.IdentityChanged(ctx, roleID)
	seen := map[string]bool{}
	for _, c := range changed {
		if !seen[c.UserID] {
			seen[c.UserID] = true
			d.IdentityChanged(ctx, c.UserID)
		}
	}
	if d.Changed != nil {
		for _, c := range changed {
			d.Changed(ctx, c)
		}
	}
	return changed, nil
}

func write(ctx context.Context, roles Roles, roleID, origin string, steps []Step) ([]Change, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	var adds []store.RoleAssignment
	var removeIDs []string
	for _, st := range steps {
		if st.Action == Assign {
			adds = append(adds, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: st.UserID, Origin: origin})
		} else {
			removeIDs = append(removeIDs, st.RowID)
		}
	}
	added, removed, err := roles.ApplyMembership(ctx, roleID, adds, removeIDs)
	if err != nil {
		return nil, err
	}
	landed := make(map[string]store.RoleAssignment, len(added)+len(removed))
	for _, a := range added {
		landed[Assign+"/"+a.SubjectID] = a
	}
	for _, a := range removed {
		landed[Unassign+"/"+a.SubjectID] = a
	}
	var changed []Change
	for _, st := range steps {
		a, ok := landed[st.Action+"/"+st.UserID]
		if !ok {
			continue
		}
		changed = append(changed, Change{
			AssignmentID: a.ID, RoleID: roleID, UserID: st.UserID, Origin: a.Origin, Action: st.Action,
		})
	}
	return changed, nil
}
