package server

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestSignInRelinkedAudit pins the record of a sign-in that rewrote an
// account's link to its Entra object id: one user.update that names the
// user, the changed field external_id and the reason, and no actor.
func TestSignInRelinkedAudit(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	ada, err := app.store.Users().Create(ctx, store.User{Username: "ada", ExternalID: "d6d6d6d6-0000-4000-8000-000000000006"})
	if err != nil {
		t.Fatal(err)
	}
	app.signInRelinked(ctx, ada)

	var got []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == actionUserUpdate && ev["user"] == ada.ID {
			got = append(got, ev)
		}
	}
	if len(got) != 1 {
		t.Fatalf("user.update records of ada = %v, want one", got)
	}
	ev := got[0]
	noActorIn(t, ev)
	changed, _ := ev["changed"].([]any)
	if ev["target"] != ada.ID || ev["username"] != "ada" || len(changed) != 1 || changed[0] != "external_id" || ev["reason"] != reasonSignInRelinked {
		t.Errorf("record = %v, want ada, changed [external_id] and the relink reason", ev)
	}
}
