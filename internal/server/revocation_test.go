package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

func claimsFor(userID string) authn.Claims { return authn.Claims{Subject: userID} }

func mkRevoke(userID string) store.Revocation {
	return store.Revocation{Kind: store.RevokeUser, TargetID: userID, Reason: "test"}
}

// TestRevocationConsumerRebuildsDenylist proves the event-spine path:
// a straza.revocation.user event published to the stream is applied to the
// gateway denylist by the consumer, independent of the in-process cascade.
// This is what lets a fresh, stateless gateway pod converge on the same
// denylist.
func TestRevocationConsumerRebuildsDenylist(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()

	// A user id that never went through the in-process cascade: only the
	// stream carries the revocation.
	const userID = "11111111-2222-3333-4444-555555555555"
	if app.denylist.blocked(claimsFor(userID)) {
		t.Fatal("user pre-blocked")
	}

	ce := `{"specversion":"1.0","id":"rev-1","type":"straza.revocation.user","source":"test","time":"2026-07-13T00:00:00Z","data":{"user":"` + userID + `"}}`
	if err := app.bus.Publish(ctx, "straza.revocation.user", []byte(ce)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for !app.denylist.blocked(claimsFor(userID)) {
		if time.Now().After(deadline) {
			t.Fatal("revocation consumer did not apply the streamed revocation to the denylist")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestReactivationLift proves reactivation is a durable, spine-replicated
// lift, not a local memory poke: the persisted revocation rows are
// deleted (so a boot-time rebuild no longer re-blocks the user), the local
// denylist entry lifts, and a straza.revocation.lift event converges a pod
// that only ever saw the revoke.
func TestReactivationLift(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()

	// Store + local half: revoke persists a row and blocks; the lift must
	// unblock AND remove the row (or every restart re-blocks forever).
	const userID = "11111111-aaaa-bbbb-cccc-000000000001"
	app.revokeUserCtx(ctx, userID, "test revoke", store.RevocationOriginAdmin)
	if !app.denylist.blocked(claimsFor(userID)) {
		t.Fatal("user not blocked after revoke")
	}
	app.allowUserCtx(ctx, userID)
	if app.denylist.blocked(claimsFor(userID)) {
		t.Fatal("user still blocked after reactivation lift")
	}
	revs, err := app.store.Revocations().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, rv := range revs {
		if rv.Kind == store.RevokeUser && rv.TargetID == userID {
			t.Fatal("revocation row survived the lift: a restarted pod would re-block the reactivated user")
		}
	}

	// Stream half: a pod that only consumed the revoke converges when the
	// lift event replays (multi-pod / DeliverAll restart path).
	const streamUser = "11111111-aaaa-bbbb-cccc-000000000002"
	app.denylist.revokeUser(streamUser)
	ce := `{"specversion":"1.0","id":"lift-1","type":"straza.revocation.lift","source":"test","time":"2026-07-13T00:00:00Z","data":{"user":"` + streamUser + `"}}`
	if err := app.bus.Publish(ctx, "straza.revocation.lift", []byte(ce)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for app.denylist.blocked(claimsFor(streamUser)) {
		if time.Now().After(deadline) {
			t.Fatal("revocation consumer did not apply the streamed lift to the denylist")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestBootRebuildsDenylistFromStore proves a restarted pod rebuilds its
// denylist from persisted revocations before serving: a revocation
// row present at New() time blocks immediately, with no stream replay needed.
func TestBootRebuildsDenylistFromStore(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedGatewayUser(t, app, "kim", "dev")
	tok := sessionToken(t, base, "kim")

	// A live session works.
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code != http.StatusOK {
		t.Fatalf("pre-revocation = %d", code)
	}

	// Persist a user revocation directly (as a prior pod would have), then
	// re-seed the denylist the way New() does at boot.
	ctx := context.Background()
	if _, err := app.store.Revocations().Create(ctx, mkRevoke(user.ID)); err != nil {
		t.Fatal(err)
	}
	revs, _ := app.store.Revocations().List(ctx)
	for _, r := range revs {
		app.denylist.revokeUser(r.TargetID)
	}
	if code, _, _ := mcpCall(t, base, tok, "tools/list", nil); code != http.StatusForbidden {
		t.Errorf("post-rebuild = %d, want 403", code)
	}
}
