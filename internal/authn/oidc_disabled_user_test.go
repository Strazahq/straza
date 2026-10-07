package authn

import (
	"context"
	"errors"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestExternalVerifierDisabledUserError pins that the external lane refuses
// a disabled user with a *DisabledUserError that carries the user's row, so
// the server can name the person in its refusal, returns the zero user
// beside it, and lets the same token pass while the user is active.
func TestExternalVerifierDisabledUserError(t *testing.T) {
	ctx := context.Background()
	idp := newIDPFixture(t)
	users := platformUsers(t)
	idToken := idp.login(t, "ext-dee", "straza")
	jit, err := NewExternalVerifier(ctx, idp.srv.URL, "", "straza", users, true)
	if err != nil {
		t.Fatal(err)
	}
	u, err := jit.VerifyLogin(ctx, idToken)
	if err != nil {
		t.Fatalf("control, an active user: %v", err)
	}
	u.Status = store.UserDisabled
	if _, err := users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := jit.VerifyLogin(ctx, idToken)
	var disabled *DisabledUserError
	if !errors.As(err, &disabled) {
		t.Fatalf("VerifyLogin = %v, want a *DisabledUserError", err)
	}
	if disabled.User.ID != u.ID || disabled.User.Username != "ext-dee" {
		t.Errorf("error names %q (%s), want ext-dee (%s)", disabled.User.Username, disabled.User.ID, u.ID)
	}
	if got.ID != "" {
		t.Errorf("VerifyLogin returned the user %s beside the refusal, want the zero user", got.ID)
	}
	if err.Error() != "authn: user ext-dee is disabled" {
		t.Errorf("error = %q, want the words of today's refusal", err.Error())
	}
}
