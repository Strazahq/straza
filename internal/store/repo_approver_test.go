package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApproverDeviceLifecycle(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d, err := s.Approvers().InsertDevice(ctx, ApproverDevice{
			UserID: "u-1", Name: "kim-pixel", Platform: "android",
			KeyAlg: "ecdsa-p256", PublicKey: "b64spki", KeySecurityLevel: "strongbox",
			AttestationKind: "play-integrity", AttestationBlob: "blob",
		})
		if err != nil {
			t.Fatalf("InsertDevice: %v", err)
		}
		if !strings.HasPrefix(d.ID, "apd_") {
			t.Errorf("device id = %q, want apd_ prefix", d.ID)
		}
		got, err := s.Approvers().GetDevice(ctx, d.ID)
		if err != nil || got.PublicKey != "b64spki" || got.KeySecurityLevel != "strongbox" {
			t.Fatalf("GetDevice = %+v, %v", got, err)
		}
		if err := s.Approvers().TouchDevice(ctx, d.ID, time.Now().UTC()); err != nil {
			t.Errorf("TouchDevice: %v", err)
		}
		// Revocation: delete makes the row (and thus the token) disappear.
		if err := s.Approvers().DeleteDevice(ctx, d.ID); err != nil {
			t.Fatalf("DeleteDevice: %v", err)
		}
		if _, err := s.Approvers().GetDevice(ctx, d.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleted device still present: %v", err)
		}
		if err := s.Approvers().DeleteDevice(ctx, d.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("second delete = %v, want ErrNotFound", err)
		}
	})
}

// TestApproverDeviceList pins ListDevices, the discoverability half of the
// approver-device kill switch. The enroll response goes to the phone, not the
// admin, so the list is where an operator finds the apd_ id that DELETE
// /v1/admin/approvers/{id} needs. Each row carries its push-registration
// count, so "2 enrolled · 1 push route" is attributable to a specific phone.
func TestApproverDeviceList(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if devs, err := s.Approvers().ListDevices(ctx, ""); err != nil || len(devs) != 0 {
			t.Fatalf("fresh list = %+v, %v; want empty, no error", devs, err)
		}
		pixel, err := s.Approvers().InsertDevice(ctx, ApproverDevice{
			UserID: "u-1", Name: "kim-pixel", Platform: "android",
			KeyAlg: "ecdsa-p256", PublicKey: "b64spki-a", KeySecurityLevel: "strongbox",
		})
		if err != nil {
			t.Fatal(err)
		}
		iphone, err := s.Approvers().InsertDevice(ctx, ApproverDevice{
			UserID: "u-2", Name: "bob-iphone", Platform: "ios",
			KeyAlg: "ecdsa-p256", PublicKey: "b64spki-b", KeySecurityLevel: "secure-enclave",
		})
		if err != nil {
			t.Fatal(err)
		}
		// Two push routes on the pixel, none on the iphone: the counts must
		// attribute, not just total.
		for _, endpoint := range []string{"https://ntfy.example/a", "https://ntfy.example/b"} {
			if err := s.Approvers().UpsertPush(ctx, ApproverPush{
				DeviceID: pixel.ID, Kind: "unifiedpush", TokenOrEndpoint: endpoint,
			}); err != nil {
				t.Fatal(err)
			}
		}

		all, err := s.Approvers().ListDevices(ctx, "")
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(all) != 2 || all[0].ID != pixel.ID || all[1].ID != iphone.ID {
			t.Fatalf("list = %+v, want [pixel, iphone] in enrolment order", all)
		}
		if all[0].PushRoutes != 2 || all[1].PushRoutes != 0 {
			t.Errorf("push routes = %d/%d, want 2/0", all[0].PushRoutes, all[1].PushRoutes)
		}
		if all[0].Name != "kim-pixel" || all[0].UserID != "u-1" || all[0].KeySecurityLevel != "strongbox" {
			t.Errorf("row fields = %+v, want the enrolled posture", all[0])
		}

		// The user filter scopes; an unknown user is an empty list, not an error.
		mine, err := s.Approvers().ListDevices(ctx, "u-2")
		if err != nil || len(mine) != 1 || mine[0].ID != iphone.ID {
			t.Errorf("ListDevices(u-2) = %+v, %v; want just the iphone", mine, err)
		}
		if none, err := s.Approvers().ListDevices(ctx, "u-ghost"); err != nil || len(none) != 0 {
			t.Errorf("ListDevices(u-ghost) = %+v, %v; want empty", none, err)
		}

		// A revoked device leaves the list immediately (row-backed, like the
		// credential itself).
		if err := s.Approvers().DeleteDevice(ctx, pixel.ID); err != nil {
			t.Fatal(err)
		}
		if after, err := s.Approvers().ListDevices(ctx, ""); err != nil || len(after) != 1 || after[0].ID != iphone.ID {
			t.Errorf("list after delete = %+v, %v; want just the iphone", after, err)
		}
	})
}

func TestApproverEnrollTokenOneTime(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := s.Approvers().InsertEnrollToken(ctx, ApproverEnrollToken{
			TokenHash: "hash-a", UserID: "u-1", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
		}); err != nil {
			t.Fatalf("InsertEnrollToken: %v", err)
		}
		// First consume wins and returns the bound user.
		tok, err := s.Approvers().ConsumeEnrollToken(ctx, "hash-a", now.Add(time.Minute))
		if err != nil || tok.UserID != "u-1" || tok.UsedAt == nil {
			t.Fatalf("ConsumeEnrollToken = %+v, %v", tok, err)
		}
		// Second consume loses (one-time).
		if _, err := s.Approvers().ConsumeEnrollToken(ctx, "hash-a", now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
			t.Errorf("replayed enroll token = %v, want ErrNotFound", err)
		}
		// An expired token cannot be consumed.
		if _, err := s.Approvers().InsertEnrollToken(ctx, ApproverEnrollToken{
			TokenHash: "hash-b", UserID: "u-1", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
		}); err != nil {
			t.Fatalf("InsertEnrollToken b: %v", err)
		}
		if _, err := s.Approvers().ConsumeEnrollToken(ctx, "hash-b", now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
			t.Errorf("expired enroll token = %v, want ErrNotFound", err)
		}
		n, err := s.Approvers().PurgeEnrollTokensBefore(ctx, now.Add(time.Hour))
		if err != nil || n == 0 {
			t.Errorf("PurgeEnrollTokensBefore = %d, %v", n, err)
		}
	})
}

func TestApproverChallengeSingleUse(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		mk := func(ch string, ttl time.Duration) {
			if err := s.Approvers().InsertChallenge(ctx, ApproverChallenge{
				Challenge: ch, DeviceID: "apd_1", ApprovalID: "apr_1",
				CreatedAt: now, ExpiresAt: now.Add(ttl),
			}); err != nil {
				t.Fatalf("InsertChallenge %s: %v", ch, err)
			}
		}
		mk("n1", 5*time.Minute)

		// Wrong device / wrong approval must not consume the nonce.
		if won, _ := s.Approvers().ConsumeChallenge(ctx, "n1", "apd_other", "apr_1", now); won {
			t.Error("challenge consumed with wrong device")
		}
		if won, _ := s.Approvers().ConsumeChallenge(ctx, "n1", "apd_1", "apr_other", now); won {
			t.Error("challenge consumed with wrong approval")
		}
		// Correct binding wins exactly once.
		if won, err := s.Approvers().ConsumeChallenge(ctx, "n1", "apd_1", "apr_1", now); err != nil || !won {
			t.Fatalf("ConsumeChallenge = %v, %v (want won)", won, err)
		}
		if won, _ := s.Approvers().ConsumeChallenge(ctx, "n1", "apd_1", "apr_1", now); won {
			t.Error("challenge must be single-use")
		}
		// Expired nonce cannot be consumed.
		mk("n2", time.Minute)
		if won, _ := s.Approvers().ConsumeChallenge(ctx, "n2", "apd_1", "apr_1", now.Add(2*time.Minute)); won {
			t.Error("expired challenge must not be consumable")
		}
		if n, err := s.Approvers().PurgeChallengesBefore(ctx, now.Add(time.Hour)); err != nil || n == 0 {
			t.Errorf("PurgeChallengesBefore = %d, %v", n, err)
		}
	})
}

func TestApproverPushDedupe(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d, err := s.Approvers().InsertDevice(ctx, ApproverDevice{UserID: "u-1", Name: "kim-pixel"})
		if err != nil {
			t.Fatalf("InsertDevice: %v", err)
		}
		t1 := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
		t2 := t1.Add(90 * time.Second)
		reg := ApproverPush{DeviceID: d.ID, Kind: "fcm", TokenOrEndpoint: "tok-1", RegisteredAt: t1}
		if err := s.Approvers().UpsertPush(ctx, reg); err != nil {
			t.Fatalf("UpsertPush: %v", err)
		}
		// Re-registering the same triple stays ONE row (dedupe) but REFRESHES
		// registered_at: the timestamp means "the app last confirmed this
		// token", the input of the guarded prune (DeletePushBefore). The app
		// re-PUTs on every launch, so a live device keeps outranking stale
		// upstream invalidations.
		reg.RegisteredAt = t2
		if err := s.Approvers().UpsertPush(ctx, reg); err != nil {
			t.Fatalf("UpsertPush dedupe: %v", err)
		}
		targets, err := s.Approvers().ListPushTargets(ctx)
		if err != nil || len(targets) != 1 {
			t.Fatalf("ListPushTargets = %v, %v; want exactly one row", targets, err)
		}
		if !targets[0].RegisteredAt.Equal(t2) {
			t.Errorf("RegisteredAt after re-upsert = %v, want refreshed to %v", targets[0].RegisteredAt, t2)
		}
		if err := s.Approvers().DeletePush(ctx, d.ID, "fcm", "tok-1"); err != nil {
			t.Fatalf("DeletePush: %v", err)
		}
		// Delete of an absent registration is idempotent.
		if err := s.Approvers().DeletePush(ctx, d.ID, "fcm", "tok-1"); err != nil {
			t.Errorf("idempotent DeletePush: %v", err)
		}
	})
}

func TestApproverPushDeletePushBefore(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d, err := s.Approvers().InsertDevice(ctx, ApproverDevice{UserID: "u-1", Name: "kim-iphone"})
		if err != nil {
			t.Fatalf("InsertDevice: %v", err)
		}
		registered := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
		reg := ApproverPush{DeviceID: d.ID, Kind: "apns", TokenOrEndpoint: "a1b2c3", RegisteredAt: registered}
		if err := s.Approvers().UpsertPush(ctx, reg); err != nil {
			t.Fatalf("UpsertPush: %v", err)
		}
		// A horizon OLDER than the registration must not prune: the device
		// re-confirmed the token after the upstream invalidated it (the APNs
		// 410 race), and unconditional pruning would silently un-ring a live
		// phone forever.
		deleted, err := s.Approvers().DeletePushBefore(ctx, d.ID, "apns", "a1b2c3", registered.Add(-time.Second))
		if err != nil || deleted {
			t.Fatalf("DeletePushBefore (older horizon) = %v, %v; want guard-skip", deleted, err)
		}
		if targets, err := s.Approvers().ListPushTargets(ctx); err != nil || len(targets) != 1 {
			t.Fatalf("registration newer than the horizon must survive: %v, %v", targets, err)
		}
		// A horizon at or after the registration prunes.
		deleted, err = s.Approvers().DeletePushBefore(ctx, d.ID, "apns", "a1b2c3", registered)
		if err != nil || !deleted {
			t.Fatalf("DeletePushBefore (at horizon) = %v, %v; want deleted", deleted, err)
		}
		if targets, err := s.Approvers().ListPushTargets(ctx); err != nil || len(targets) != 0 {
			t.Fatalf("registration at the horizon must prune: %v, %v", targets, err)
		}
		// Idempotent on absent rows.
		if deleted, err := s.Approvers().DeletePushBefore(ctx, d.ID, "apns", "a1b2c3", registered); err != nil || deleted {
			t.Errorf("idempotent DeletePushBefore = %v, %v; want no-op", deleted, err)
		}
	})
}

func TestApproverListPushTargets(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		mustPush := func(deviceID, kind, tok string) {
			t.Helper()
			if err := s.Approvers().UpsertPush(ctx, ApproverPush{DeviceID: deviceID, Kind: kind, TokenOrEndpoint: tok}); err != nil {
				t.Fatalf("UpsertPush %s/%s: %v", deviceID, tok, err)
			}
		}

		// Empty store yields no targets.
		if tg, err := s.Approvers().ListPushTargets(ctx); err != nil || len(tg) != 0 {
			t.Fatalf("empty ListPushTargets = %v, %v; want none", tg, err)
		}

		d1, err := s.Approvers().InsertDevice(ctx, ApproverDevice{UserID: "u-1", Name: "kim-pixel"})
		if err != nil {
			t.Fatalf("InsertDevice d1: %v", err)
		}
		d2, err := s.Approvers().InsertDevice(ctx, ApproverDevice{UserID: "u-2", Name: "lee-phone"})
		if err != nil {
			t.Fatalf("InsertDevice d2: %v", err)
		}
		mustPush(d1.ID, "fcm", "tok-1")
		mustPush(d1.ID, "unifiedpush", "https://ntfy.sh/abc")
		mustPush(d2.ID, "fcm", "tok-2")

		// A registration whose device is later revoked must not be joined (the
		// inner join drops it); deleting the device stops delivery.
		orphan, err := s.Approvers().InsertDevice(ctx, ApproverDevice{UserID: "u-3"})
		if err != nil {
			t.Fatalf("InsertDevice orphan: %v", err)
		}
		mustPush(orphan.ID, "fcm", "tok-3")
		if err := s.Approvers().DeleteDevice(ctx, orphan.ID); err != nil {
			t.Fatalf("DeleteDevice orphan: %v", err)
		}

		targets, err := s.Approvers().ListPushTargets(ctx)
		if err != nil {
			t.Fatalf("ListPushTargets: %v", err)
		}
		if len(targets) != 3 {
			t.Fatalf("targets = %d, want 3 (orphaned registration pruned by the join)", len(targets))
		}
		byUser := map[string]int{}
		for _, x := range targets {
			byUser[x.UserID]++
			if x.TokenOrEndpoint == "tok-1" && x.UserID != "u-1" {
				t.Errorf("tok-1 user = %q, want u-1", x.UserID)
			}
			if x.UserID == "u-3" {
				t.Errorf("revoked device's registration still present: %+v", x)
			}
		}
		if byUser["u-1"] != 2 || byUser["u-2"] != 1 {
			t.Errorf("per-user registration counts = %v, want u-1:2 u-2:1", byUser)
		}
	})
}

// TestApproverCountDevices: the channel-status surface's device count follows
// enrolments and revocations.
func TestApproverCountDevices(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if n, err := s.Approvers().CountDevices(ctx); err != nil || n != 0 {
			t.Fatalf("empty count = %d, %v; want 0", n, err)
		}
		d1, err := s.Approvers().InsertDevice(ctx, ApproverDevice{UserID: "u-1", Name: "pixel"})
		if err != nil {
			t.Fatalf("InsertDevice: %v", err)
		}
		if _, err := s.Approvers().InsertDevice(ctx, ApproverDevice{UserID: "u-2", Name: "tablet"}); err != nil {
			t.Fatalf("InsertDevice: %v", err)
		}
		if n, _ := s.Approvers().CountDevices(ctx); n != 2 {
			t.Fatalf("count after 2 enrolments = %d, want 2", n)
		}
		if err := s.Approvers().DeleteDevice(ctx, d1.ID); err != nil {
			t.Fatalf("DeleteDevice: %v", err)
		}
		if n, _ := s.Approvers().CountDevices(ctx); n != 1 {
			t.Fatalf("count after revocation = %d, want 1", n)
		}
	})
}
