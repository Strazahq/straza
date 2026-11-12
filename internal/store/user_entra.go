package store

import (
	"context"
	"errors"
	"strings"
)

// NormalizeEntraAgentID answers v as an Entra agent id in the form the
// users table stores, a GUID of 8-4-4-4-12 hex digits in lower case, with
// surrounding white space trimmed. It answers false for anything else,
// braces and an empty value included, so every writer refuses the same
// values and the unique index compares one spelling of each id.
func NormalizeEntraAgentID(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 36 {
		return "", false
	}
	for i, c := range v {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return "", false
			}
		}
	}
	return v, true
}

// EntraAgentRefusal says why a user write that touches the Entra agent id
// is refused, or EntraAgentOK when it is not.
type EntraAgentRefusal int

// The refusals of CheckEntraAgentID.
const (
	EntraAgentOK         EntraAgentRefusal = iota
	EntraAgentNotAgent                     // the written row holds an id and is not of type agent
	EntraAgentTypeChange                   // the row held the id as an agent and the write changes its type
	EntraAgentHeld                         // another live user holds the id
)

// CheckEntraAgentID judges the write that turns before into after against
// the rules every write lane shares: only a user of type agent holds an
// Entra agent id, an agent that keeps its id keeps its type, and no two
// live users hold the same id. after.EntraAgentID is already normalized.
// For EntraAgentHeld it answers the user that holds the id. An agent that
// keeps its id as it was needs no lookup, and an error of the holder
// lookup is answered as it came, so the caller fails closed.
func CheckEntraAgentID(ctx context.Context, users UserRepo, before, after User) (EntraAgentRefusal, User, error) {
	if after.EntraAgentID == "" {
		return EntraAgentOK, User{}, nil
	}
	if after.UserType != UserTypeAgent {
		if before.EntraAgentID == after.EntraAgentID && before.UserType == UserTypeAgent {
			return EntraAgentTypeChange, User{}, nil
		}
		return EntraAgentNotAgent, User{}, nil
	}
	if before.EntraAgentID == after.EntraAgentID {
		return EntraAgentOK, User{}, nil
	}
	holder, err := users.GetByEntraAgentID(ctx, after.EntraAgentID)
	switch {
	case errors.Is(err, ErrNotFound):
		return EntraAgentOK, User{}, nil
	case err != nil:
		return EntraAgentOK, User{}, err
	case holder.ID != after.ID:
		return EntraAgentHeld, holder, nil
	}
	return EntraAgentOK, User{}, nil
}
