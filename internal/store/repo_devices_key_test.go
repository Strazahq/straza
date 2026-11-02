package store

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// keyedDevice answers a device row of userID enrolled with a key, as the key
// lane of POST /v1/enroll writes it.
func keyedDevice(userID, fingerprint string) Device {
	return Device{
		UserID: userID, Name: "laptop", Fingerprint: fingerprint, Platform: "linux", ClientKind: DeviceClientKit,
		KeyJWK:         `{"crv":"Ed25519","kty":"OKP","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}`,
		KeyThumbprint:  "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k",
		Custody:        `{"kind":"account","platform":"linux","version":"6.12"}`,
		MachineBinding: "machine-binding-1",
		SignedInAt:     now().Add(-time.Hour),
	}
}

// deviceDiff answers "" when got and want hold the same row, comparing the
// two times as instants, and otherwise both rows.
func deviceDiff(got, want Device) string {
	if !got.EnrolledAt.Equal(want.EnrolledAt) || !got.SignedInAt.Equal(want.SignedInAt) {
		return fmt.Sprintf("got %+v, want %+v", got, want)
	}
	g, w := got, want
	g.EnrolledAt, w.EnrolledAt = time.Time{}, time.Time{}
	g.SignedInAt, w.SignedInAt = time.Time{}, time.Time{}
	if g != w {
		return fmt.Sprintf("got %+v, want %+v", got, want)
	}
	return ""
}

// createKeyedDevice creates a user and one keyed device of that user.
func createKeyedDevice(t *testing.T, s Store) Device {
	t.Helper()
	ctx := context.Background()
	u, err := s.Users().Create(ctx, User{Username: "kim"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Devices().Create(ctx, keyedDevice(u.ID, "fp-key"))
	if err != nil {
		t.Fatalf("Devices.Create: %v", err)
	}
	return d
}

// TestDeviceKeyFieldsRoundTrip pins the six key fields of a device on both
// drivers. Create writes them, GetByID and ListByUser read them back, and
// Update replaces every one of them, as a re-enrollment does, the counter
// included.
func TestDeviceKeyFieldsRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u, err := s.Users().Create(ctx, User{Username: "kim"})
		if err != nil {
			t.Fatal(err)
		}
		want := keyedDevice(u.ID, "fp-key")
		want.KeyCounter = 3
		created, err := s.Devices().Create(ctx, want)
		if err != nil {
			t.Fatalf("Devices.Create: %v", err)
		}
		want.ID, want.Status, want.EnrolledAt = created.ID, "active", created.EnrolledAt
		if d := deviceDiff(created, want); d != "" {
			t.Errorf("Create answered %s", d)
		}
		got, err := s.Devices().GetByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if d := deviceDiff(got, want); d != "" {
			t.Errorf("GetByID read %s", d)
		}
		list, err := s.Devices().ListByUser(ctx, u.ID)
		if err != nil || len(list) != 1 {
			t.Fatalf("ListByUser = %+v, %v; want one row", list, err)
		}
		if d := deviceDiff(list[0], want); d != "" {
			t.Errorf("ListByUser read %s", d)
		}

		next := got
		next.KeyJWK = `{"crv":"Ed25519","kty":"OKP","x":"0Vn7AW0bEfMDfMcl0SvjYvf4dLaC4M4bAsaqJLv5d4E"}`
		next.KeyThumbprint = "second-thumbprint"
		next.Custody = `{"kind":"user","platform":"darwin","version":"15.1"}`
		next.MachineBinding = "machine-binding-2"
		next.KeyCounter = 0
		next.SignedInAt = now()
		updated, err := s.Devices().Update(ctx, next)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if d := deviceDiff(updated, next); d != "" {
			t.Errorf("Update answered %s", d)
		}
		if got, err = s.Devices().GetByID(ctx, next.ID); err != nil || deviceDiff(got, next) != "" {
			t.Errorf("GetByID after the update = %v, %s", err, deviceDiff(got, next))
		}
	})
}

// TestDeviceBearerRowHasNoKey pins a device enrolled without a key on both
// drivers. It reads back with empty key fields, a zero counter and a zero
// SignedInAt, which the store holds as NULL, and an update of its status
// keeps it that way.
func TestDeviceBearerRowHasNoKey(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		u, err := s.Users().Create(ctx, User{Username: "kim"})
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.Devices().Create(ctx, Device{UserID: u.ID, Name: "laptop", Fingerprint: "fp1", ClientKind: DeviceClientKit})
		if err != nil {
			t.Fatalf("Devices.Create: %v", err)
		}
		check := func(when string) {
			t.Helper()
			got, err := s.Devices().GetByID(ctx, d.ID)
			if err != nil {
				t.Fatalf("GetByID %s: %v", when, err)
			}
			if got.KeyJWK != "" || got.KeyThumbprint != "" || got.Custody != "" || got.MachineBinding != "" ||
				got.KeyCounter != 0 || !got.SignedInAt.IsZero() {
				t.Errorf("the bearer row %s reads key fields %+v, want none", when, got)
			}
			var null bool
			if err := sq.queryRow(ctx, `SELECT signed_in_at IS NULL FROM devices WHERE id = $1`, d.ID).Scan(&null); err != nil || !null {
				t.Errorf("signed_in_at of the bearer row %s is NULL = %v, %v; want true", when, null, err)
			}
		}
		check("after Create")
		d.Status = "disabled"
		if _, err := s.Devices().Update(ctx, d); err != nil {
			t.Fatalf("Update: %v", err)
		}
		check("after a status update")
	})
}

// TestDeviceAdvanceCounter pins the conditional counter write on both
// drivers. A counter above the stored one under the device's key raises it.
// An equal or a lower one, one under another key or under no key, and one
// for a missing device leave it and answer false with no error. Only a
// store failure answers an error. After a re-enrollment a check-in on the
// old key moves nothing, and the new key's first check-in raises it.
func TestDeviceAdvanceCounter(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d := createKeyedDevice(t, s)
		key := d.KeyThumbprint
		steps := []struct {
			name       string
			thumbprint string
			n          int64
			moved      bool
			stored     int64
		}{
			{"the first counter above zero raises it", key, 1, true, 1},
			{"an equal counter is refused", key, 1, false, 1},
			{"a lower counter is refused", key, 0, false, 1},
			{"a negative counter is refused", key, -1, false, 1},
			{"a higher counter under another key is refused", "another-thumbprint", 5, false, 1},
			{"a higher counter under no key is refused", "", 5, false, 1},
			{"a jump raises it", key, 9, true, 9},
			{"a counter past 32 bits raises it", key, 1 << 40, true, 1 << 40},
			{"a lower counter after the jump is refused", key, 10, false, 1 << 40},
			{"the largest counter raises it", key, math.MaxInt64, true, math.MaxInt64},
			{"nothing passes the largest counter", key, math.MaxInt64, false, math.MaxInt64},
		}
		for _, st := range steps {
			moved, err := s.Devices().AdvanceCounter(ctx, d.ID, st.thumbprint, st.n)
			if err != nil || moved != st.moved {
				t.Errorf("%s: AdvanceCounter(%q, %d) = %v, %v; want %v", st.name, st.thumbprint, st.n, moved, err, st.moved)
			}
			got, err := s.Devices().GetByID(ctx, d.ID)
			if err != nil || got.KeyCounter != st.stored {
				t.Errorf("%s: stored counter = %d, %v; want %d", st.name, got.KeyCounter, err, st.stored)
			}
		}
		if moved, err := s.Devices().AdvanceCounter(ctx, "no-such-device", key, 1); err != nil || moved {
			t.Errorf("AdvanceCounter on a missing device = %v, %v; want false and no error", moved, err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if moved, err := s.Devices().AdvanceCounter(cancelled, d.ID, key, math.MaxInt64); err == nil || moved {
			t.Errorf("AdvanceCounter with the store unreachable = %v, %v; want false and an error", moved, err)
		}

		renewed, err := s.Devices().GetByID(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		renewed.KeyJWK = `{"crv":"Ed25519","kty":"OKP","x":"0Vn7AW0bEfMDfMcl0SvjYvf4dLaC4M4bAsaqJLv5d4E"}`
		renewed.KeyThumbprint = "renewed-thumbprint"
		renewed.KeyCounter = 0
		renewed.SignedInAt = now()
		if _, err := s.Devices().Update(ctx, renewed); err != nil {
			t.Fatalf("re-enroll through Update: %v", err)
		}
		if moved, err := s.Devices().AdvanceCounter(ctx, d.ID, key, 7); err != nil || moved {
			t.Errorf("a check-in on the old key after the re-enrollment = %v, %v; want false and no error", moved, err)
		}
		if moved, err := s.Devices().AdvanceCounter(ctx, d.ID, renewed.KeyThumbprint, 1); err != nil || !moved {
			t.Errorf("the new key's first check-in = %v, %v; want true", moved, err)
		}
		if got, err := s.Devices().GetByID(ctx, d.ID); err != nil || got.KeyCounter != 1 {
			t.Errorf("stored counter after the new key's first check-in = %d, %v; want 1", got.KeyCounter, err)
		}
	})
}

// TestDeviceAdvanceCounterConcurrent races many check-ins on one device on
// both drivers. Of many calls with the same counter exactly one wins. In a
// mixed race no counter wins twice, no counter at or below the stored one
// wins, the highest one always wins, and the stored counter ends at the
// highest, so no concurrent check-in loses a counter update.
func TestDeviceAdvanceCounterConcurrent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		d := createKeyedDevice(t, s)
		// race sends every counter at once and answers the wins per counter.
		race := func(counters []int64) map[int64]int {
			t.Helper()
			moved := make([]bool, len(counters))
			errs := make([]error, len(counters))
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, n := range counters {
				wg.Go(func() {
					<-start
					moved[i], errs[i] = s.Devices().AdvanceCounter(ctx, d.ID, d.KeyThumbprint, n)
				})
			}
			close(start)
			wg.Wait()
			wins := map[int64]int{}
			for i, n := range counters {
				if errs[i] != nil {
					t.Fatalf("AdvanceCounter(%d): %v", n, errs[i])
				}
				if moved[i] {
					wins[n]++
				}
			}
			return wins
		}
		stored := func() int64 {
			t.Helper()
			got, err := s.Devices().GetByID(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			return got.KeyCounter
		}

		if wins := race(slices.Repeat([]int64{5}, 32)); wins[5] != 1 {
			t.Errorf("32 calls with counter 5 won %d times, want exactly once", wins[5])
		}
		if got := stored(); got != 5 {
			t.Errorf("stored counter after the same-counter race = %d, want 5", got)
		}

		var mixed []int64
		for range 4 {
			for n := int64(1); n <= 21; n++ {
				mixed = append(mixed, n)
			}
		}
		wins := race(mixed)
		for n, c := range wins {
			if c > 1 {
				t.Errorf("counter %d won %d times, want at most once", n, c)
			}
			if n <= 5 {
				t.Errorf("counter %d won although the stored counter was already 5", n)
			}
		}
		if wins[21] != 1 {
			t.Errorf("the highest counter 21 won %d times, want exactly once", wins[21])
		}
		if got := stored(); got != 21 {
			t.Errorf("stored counter after the mixed race = %d, want 21", got)
		}
	})
}

// TestDeviceKeysMigration pins migration 40 on both drivers. The move down
// drops the six key columns and revokes every keyed device itself: the row
// goes and one device revocation of origin admin names it, so a server
// that reads no key never takes a key-bound token for a bearer one. A
// bearer device stays with no revocation. The spent-proof table goes down
// and comes back up. A device enrolled below 40 reads back after the move
// up as a bearer row.
func TestDeviceKeysMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 40 {
			t.Fatalf("newest migration = %d, want 40 or later", head)
		}
		keyed := createKeyedDevice(t, s)
		second, err := s.Devices().Create(ctx, keyedDevice(keyed.UserID, "fp-key-2"))
		if err != nil {
			t.Fatal(err)
		}
		bearer, err := s.Devices().Create(ctx, Device{UserID: keyed.UserID, Name: "desktop", Fingerprint: "fp-bearer", ClientKind: DeviceClientKit})
		if err != nil {
			t.Fatal(err)
		}
		// down moves the database to 39, one migration per run.
		down := func() {
			t.Helper()
			for v := head; v >= 40; v-- {
				if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
					t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
				}
			}
		}
		up := func() {
			t.Helper()
			if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
				t.Fatalf("MigrateTo(%d) = %d, %v", head, got, err)
			}
		}
		rows := func(id string) int {
			t.Helper()
			var n int
			if err := sq.queryRow(ctx, `SELECT COUNT(*) FROM devices WHERE id = $1`, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}

		if _, err := s.Devices().SpendProof(ctx, keyed.KeyThumbprint, "jti-1", now().Add(time.Minute)); err != nil {
			t.Fatalf("SpendProof before the move down: %v", err)
		}
		before := now().Add(-time.Second)
		down()
		if _, err := sq.exec(ctx, `SELECT 1 FROM spent_proofs`); err == nil {
			t.Error("table spent_proofs survived the move down")
		}
		for _, col := range []string{"key_jwk", "key_thumbprint", "custody", "machine_binding", "key_counter", "signed_in_at"} {
			if _, err := sq.exec(ctx, "SELECT "+col+" FROM devices"); err == nil {
				t.Errorf("column %s survived the move down", col)
			}
		}
		revIDs := map[string]bool{}
		for _, id := range []string{keyed.ID, second.ID} {
			if n := rows(id); n != 0 {
				t.Errorf("keyed device %s has %d rows after the move down, want 0", id, n)
			}
			revs, err := s.Revocations().ListByTarget(ctx, RevokeDevice, id)
			if err != nil || len(revs) != 1 {
				t.Fatalf("revocations of keyed device %s after the move down = %+v, %v; want one", id, revs, err)
			}
			rv := revs[0]
			if _, err := uuid.Parse(rv.ID); err != nil || revIDs[rv.ID] {
				t.Errorf("revocation id %q is not a fresh UUID: %v", rv.ID, err)
			}
			revIDs[rv.ID] = true
			if rv.Reason != "device key binding rolled back" || rv.Origin != RevocationOriginAdmin ||
				rv.CreatedAt.Before(before) || rv.CreatedAt.After(now().Add(time.Second)) {
				t.Errorf("revocation of keyed device %s = %+v, want the rollback reason, origin admin and the time of the move", id, rv)
			}
		}
		if n := rows(bearer.ID); n != 1 {
			t.Errorf("bearer device has %d rows after the move down, want 1", n)
		}
		if revs, err := s.Revocations().ListByTarget(ctx, RevokeDevice, bearer.ID); err != nil || len(revs) != 0 {
			t.Errorf("revocations of the bearer device after the move down = %+v, %v; want none", revs, err)
		}
		if _, err := sq.exec(ctx, `INSERT INTO devices (id, user_id, name, fingerprint, platform, status, enrolled_at, client_kind)
			VALUES ($1, $2, 'old-laptop', 'fp-old', 'linux', 'active', $3, 'kit')`, "dev-old", keyed.UserID, sq.tArg(now())); err != nil {
			t.Fatalf("insert a device below migration 40: %v", err)
		}

		up()
		if spent, err := s.Devices().SpendProof(ctx, keyed.KeyThumbprint, "jti-1", now().Add(time.Minute)); err != nil || !spent {
			t.Errorf("SpendProof after the move up = %v, %v; want true on the empty table", spent, err)
		}
		for _, id := range []string{"dev-old", bearer.ID} {
			got, err := s.Devices().GetByID(ctx, id)
			if err != nil {
				t.Fatalf("GetByID(%s) after the move up: %v", id, err)
			}
			if got.KeyJWK != "" || got.KeyThumbprint != "" || got.Custody != "" || got.MachineBinding != "" ||
				got.KeyCounter != 0 || !got.SignedInAt.IsZero() {
				t.Errorf("device %s after the move up reads key fields %+v, want none", id, got)
			}
		}
	})
}
