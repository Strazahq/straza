package authn

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestRelinkLeavesWhatItMustNot pins the two accounts a sign-in at an Entra
// tenant finds by sub and does not relink to the oid: a disabled account,
// whose sign-in is refused, and an account whose oid a deleted user still
// holds, whose sign-in goes on with the link by sub.
func TestRelinkLeavesWhatItMustNot(t *testing.T) {
	cases := []struct {
		name     string
		disabled bool // the account is disabled
		taken    bool // a deleted user holds the oid
	}{
		{name: "a disabled account", disabled: true},
		{name: "an account whose oid a deleted user holds", taken: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			idp := newSplitIDP(t, entraLabIssuer, http.StatusOK)
			users := platformUsers(t)
			row := store.User{Username: labUser, ExternalID: labSub}
			if tc.disabled {
				row.Status = store.UserDisabled
			}
			ada, err := users.Create(ctx, row)
			if err != nil {
				t.Fatal(err)
			}
			if tc.taken {
				gone, err := users.Create(ctx, store.User{Username: "gone", ExternalID: labOID})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := users.SoftDelete(ctx, gone.ID, nil); err != nil {
					t.Fatal(err)
				}
			}
			v, err := NewExternalVerifier(ctx, entraLabIssuer, idp.discoveryURL(), "straza", users, false)
			if err != nil {
				t.Fatal(err)
			}
			relinks := 0
			v.Relinked = func(context.Context, store.User) { relinks++ }

			got, err := v.VerifyLogin(ctx, idp.signAs(t, entraLabIssuer, labSub, map[string]any{"oid": labOID}))
			var off *DisabledUserError
			switch {
			case tc.disabled && !errors.As(err, &off):
				t.Fatalf("VerifyLogin = %v, want a *DisabledUserError", err)
			case !tc.disabled && (err != nil || got.ID != ada.ID):
				t.Fatalf("VerifyLogin = %+v, %v; want the sign-in to resolve to the account", got, err)
			}
			after, err := users.GetByID(ctx, ada.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.ExternalID != labSub || relinks != 0 {
				t.Errorf("externalId after the sign-in = %q with %d relinks reported, want the link by sub kept and none", after.ExternalID, relinks)
			}
		})
	}
}
