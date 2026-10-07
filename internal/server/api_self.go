package server

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// selfResponse is the GET /v1/self wire shape (openapi 0.83.0): the one
// whoami read both browser surfaces consult after sign-in. AdminGrants
// speaks the checkin vocabulary ("full" or the canonical "area:verb,…"
// union) and is omitted when empty, exactly like the checkin response, so
// the console's tab discovery and the approvals page's "Open the console"
// link read the same truth. EnrollChannels is ALWAYS present ([] legal,
// browser before mobile): absence would be indistinguishable from "not
// eligible" on the page. Sponsored is always present too: the usernames of
// the active users the caller sponsors, sorted, [] when there are none.
type selfResponse struct {
	Username       string   `json:"username"`
	UserKind       string   `json:"user_kind"`
	AdminGrants    string   `json:"admin_grants,omitempty"`
	EnrollChannels []string `json:"enroll_channels"`
	Sponsored      []string `json:"sponsored"`
}

// handleSelf serves GET /v1/self (requireIdentified: session or login
// token, wat_ refused). It resolves the caller's roles ONCE and derives
// both answers from the same helpers the checkin response and the self
// mint gate use (adminGrantsForRoles, channelsForRoles), so no surface can
// disagree with what a mint or a console login would actually do.
func (a *App) handleSelf(w http.ResponseWriter, r *http.Request, userID string) {
	u, err := a.store.Users().GetByID(r.Context(), userID)
	if err != nil {
		apiError(w, http.StatusUnauthorized, "user no longer exists")
		return
	}
	roles, err := a.resolver.ResolveRoles(r.Context(), u.ID, time.Now())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
		return
	}
	sponsored, err := a.sponsoredBy(r.Context(), u.Username)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "the sponsored users could not be listed", err)
		return
	}
	writeJSON(w, http.StatusOK, selfResponse{
		Username:       u.Username,
		UserKind:       userKind(u),
		AdminGrants:    a.adminGrantsForRoles(roles),
		EnrollChannels: channelsForRoles(u, roles),
		Sponsored:      sponsored,
	})
}

// sponsoredBy lists the usernames of the active users whose sponsor is the
// named user, sorted, and an empty list when there are none.
func (a *App) sponsoredBy(ctx context.Context, sponsor string) ([]string, error) {
	users, err := a.store.Users().List(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, u := range users {
		if u.Sponsor == sponsor && u.Status == store.UserActive {
			out = append(out, u.Username)
		}
	}
	sort.Strings(out)
	return out, nil
}
