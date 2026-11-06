package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestKeyCheckinCounterOutage pins that a store failure at the counter write
// answers the honest 503 and never reaches the reuse path: the device row
// survives with its counter, and no revocation or reuse record is written.
func TestKeyCheckinCounterOutage(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	_ = seedIdentity(t, app)
	h := newKeyHolder(t, base)
	h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:outage")
	h.mustCheckin()

	fs.arm("devices.counter", errors.New("db down"))
	h.counter++
	code, hdr, out := h.send("/v1/checkin", "", h.checkinBody(h.counter))
	requireOutage(t, "key checkin counter", code, hdr, out)
	fs.disarm()

	if d := deviceRow(t, app, h.device); d.KeyCounter != 1 {
		t.Errorf("stored counter = %d, want 1", d.KeyCounter)
	}
	if revs, err := app.store.Revocations().ListByTarget(context.Background(), store.RevokeDevice, h.device); err != nil || len(revs) != 0 {
		t.Errorf("revocations = %+v %v, want none", revs, err)
	}
	if n := len(reuseRecords(t, app, h.device)); n != 0 {
		t.Errorf("reuse records = %d, want none", n)
	}
	if code, out := h.checkin(); code != http.StatusOK {
		t.Errorf("check-in after the outage = %d %v", code, out)
	}
}

// TestKeyReuseOfAGoneDevice pins that the reuse path writes nothing when
// another revoke deleted the device row first, so the caller answers it as
// a device not enrolled for the key.
func TestKeyReuseOfAGoneDevice(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	h := newKeyHolder(t, base)
	h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:gone")
	d := deviceRow(t, app, h.device)
	r := httptest.NewRequest(http.MethodPost, "/v1/checkin", nil)
	if err := app.revokeDevice(r, d, "device revoked by admin", "revoked-admin"); err != nil {
		t.Fatal(err)
	}
	if app.revokeReusedKey(r, d, 1) {
		t.Error("revokeReusedKey reported a revoke of a row that was already gone")
	}
	if n := len(reuseRecords(t, app, d.ID)); n != 0 {
		t.Errorf("reuse records = %d, want none", n)
	}
	revs, err := app.store.Revocations().ListByTarget(context.Background(), store.RevokeDevice, d.ID)
	if err != nil || len(revs) != 1 || revs[0].Reason != "device revoked by admin" {
		t.Errorf("revocations = %+v %v, want only the admin's", revs, err)
	}
}

// beforeAdvanceStore runs hook once, before the first counter write, so a
// test can change the device row between a check-in's read and its write.
type beforeAdvanceStore struct {
	store.Store
	once *sync.Once
	hook func(id string)
}

func (s beforeAdvanceStore) Devices() store.DeviceRepo {
	return beforeAdvanceDevices{s.Store.Devices(), s}
}

type beforeAdvanceDevices struct {
	store.DeviceRepo
	s beforeAdvanceStore
}

func (d beforeAdvanceDevices) AdvanceCounter(ctx context.Context, id, thumbprint string, n int64) (bool, error) {
	d.s.once.Do(func() { d.s.hook(id) })
	return d.DeviceRepo.AdvanceCounter(ctx, id, thumbprint, n)
}

// TestKeyCheckinRowChangedBeforeTheWrite pins that a counter write the row
// refused because an admin deleted it, or a re-enrollment replaced its key,
// after the check-in read it, is answered as a device not enrolled for the
// key with one login failure, and never as key reuse.
func TestKeyCheckinRowChangedBeforeTheWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name    string
		change  func(t *testing.T, raw store.Store, id string)
		rowGone bool
	}{
		{"an admin revoke", func(t *testing.T, raw store.Store, id string) {
			ev := store.OutboxEvent{Subject: "straza.revocation.device", CE: `{"specversion":"1.0","id":"l4-admin-revoke",` +
				`"type":"straza.revocation.device","source":"test","data":{"device":"` + id + `"}}`}
			if _, err := raw.Devices().Revoke(ctx, id, "device revoked by admin", ev); err != nil {
				t.Error(err)
			}
		}, true},
		{"a re-enrollment", func(t *testing.T, raw store.Store, id string) {
			d, err := raw.Devices().GetByID(ctx, id)
			if err != nil {
				t.Error(err)
				return
			}
			d.KeyJWK, d.KeyThumbprint, d.KeyCounter = "{}", "the-thumbprint-of-a-new-key", 0
			if _, err := raw.Devices().Update(ctx, d); err != nil {
				t.Error(err)
			}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var raw store.Store
			hooked := func(a *App) {
				raw = a.store
				a.store = beforeAdvanceStore{Store: raw, once: &sync.Once{}, hook: func(id string) { tc.change(t, raw, id) }}
			}
			app, base := testAppPreRun(t, []func(*App){hooked})
			_ = seedIdentity(t, app)
			h := newKeyHolder(t, base)
			h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:changed")
			before := len(deviceKeyFailures(t, app))

			code, out := h.checkin()
			if code != http.StatusForbidden || out["error"] != keyNotEnrolledMsg {
				t.Fatalf("checkin = %d %v, want 403 %q", code, out, keyNotEnrolledMsg)
			}
			if n := len(deviceKeyFailures(t, app)); n != before+1 {
				t.Errorf("device-key login failures = %d, want exactly one more than %d", n, before)
			}
			if n := len(reuseRecords(t, app, h.device)); n != 0 {
				t.Errorf("reuse records = %d, want none", n)
			}
			revs, err := raw.Revocations().ListByTarget(ctx, store.RevokeDevice, h.device)
			if err != nil {
				t.Fatal(err)
			}
			for _, rv := range revs {
				if rv.Reason != "device revoked by admin" {
					t.Errorf("a revocation of reason %q, want none but the admin's", rv.Reason)
				}
			}
			_, err = raw.Devices().GetByID(ctx, h.device)
			if gone := errors.Is(err, store.ErrNotFound); gone != tc.rowGone {
				t.Errorf("device row gone = %v (%v), want %v", gone, err, tc.rowGone)
			}
		})
	}
}
