package server

import (
	"context"

	"github.com/strazahq/straza/internal/store"
)

// resolveSponsor turns the user row's sponsor reference into the username
// and user id the session subject carries. A reference that names no
// active user resolves to nothing, so the credential broker never serves a
// row for a sponsor that does not exist (fail closed). Control plane only:
// the checkin is a store path already.
func (a *App) resolveSponsor(ctx context.Context, u store.User) (string, string) {
	if u.Sponsor == "" {
		return "", ""
	}
	sp, err := a.store.Users().GetByUsername(ctx, u.Sponsor)
	if err != nil || sp.Status != store.UserActive {
		return "", ""
	}
	return sp.Username, sp.ID
}
