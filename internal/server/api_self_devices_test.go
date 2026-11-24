package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// selfDeviceKeys are the only keys a row of GET /v1/self/devices carries.
var selfDeviceKeys = map[string]bool{
	"id": true, "name": true, "platform": true, "key_security_level": true,
	"attestation": true, "created_at": true, "last_seen": true, "notifies": true,
}

// TestSelfDevicesList pins GET /v1/self/devices: two people with two devices
// each read their own two, newest first, and never the other's. A revoked
// device is absent. notifies is true only for a device with a push route.
// The raw answer carries none of the public key, the attestation blob, a
// token, a hash or a push endpoint, and no key beyond the documented ones.
func TestSelfDevicesList(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminSession, _ := checkinToken(t, app, base)
	lin := mkHuman(t, app, "lin")
	mo := mkHuman(t, app, "max")

	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	seen := t0.Add(30 * time.Minute)
	device := func(owner store.User, name string, at time.Time, lastSeen *time.Time) store.ApproverDevice {
		t.Helper()
		d, err := app.store.Approvers().InsertDevice(ctx, store.ApproverDevice{
			UserID: owner.ID, Name: name, Platform: "android", KeyAlg: "ecdsa-p256",
			PublicKey: "PUBKEY-" + name, KeySecurityLevel: "strongbox",
			AttestationKind: "play-integrity", AttestationBlob: "BLOB-" + name,
			CreatedAt: at, LastSeenAt: lastSeen,
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	linOld := device(lin, "lin-old", t0, &seen)
	linNew := device(lin, "lin-new", t0.Add(time.Minute), nil)
	linGone := device(lin, "lin-gone", t0.Add(2*time.Minute), nil)
	maxOld := device(mo, "max-old", t0, nil)
	maxNew := device(mo, "max-new", t0.Add(time.Minute), nil)
	if err := app.store.Approvers().UpsertPush(ctx, store.ApproverPush{
		DeviceID: linNew.ID, Kind: "fcm", TokenOrEndpoint: "PUSHROUTE-lin-new",
	}); err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/approvers/"+linGone.ID, adminSession, nil, nil); code != http.StatusOK {
		t.Fatalf("admin revoke = %d", code)
	}
	_, linSession := storedSession(t, app, lin.ID, "", "self-service")
	_, maxSession := storedSession(t, app, mo.ID, "", "console")

	type row struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		Platform string  `json:"platform"`
		Level    string  `json:"key_security_level"`
		Att      string  `json:"attestation"`
		Created  string  `json:"created_at"`
		LastSeen *string `json:"last_seen"`
		Notifies bool    `json:"notifies"`
	}
	for _, tc := range []struct {
		who     string
		bearer  string
		want    []store.ApproverDevice
		notify  map[string]bool
		seenFor string
	}{
		{"lin", linSession, []store.ApproverDevice{linNew, linOld}, map[string]bool{linNew.ID: true}, linOld.ID},
		{"max", maxSession, []store.ApproverDevice{maxNew, maxOld}, map[string]bool{}, ""},
	} {
		code, raw, _ := adminBytes(t, http.MethodGet, base+"/v1/self/devices", tc.bearer, "", nil)
		if code != http.StatusOK {
			t.Fatalf("%s: list = %d %s", tc.who, code, raw)
		}
		var out struct {
			Devices []row `json:"devices"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: %v (%s)", tc.who, err, raw)
		}
		if len(out.Devices) != len(tc.want) {
			t.Fatalf("%s: %d devices %s, want %d", tc.who, len(out.Devices), raw, len(tc.want))
		}
		for i, d := range tc.want {
			got := out.Devices[i]
			if got.ID != d.ID || got.Name != d.Name || got.Platform != "android" || got.Level != "strongbox" || got.Att != "play-integrity" {
				t.Errorf("%s: row %d = %+v, want %s", tc.who, i, got, d.Name)
			}
			if got.Created != d.CreatedAt.Format(time.RFC3339) {
				t.Errorf("%s: %s created_at = %q, want %s", tc.who, d.Name, got.Created, d.CreatedAt.Format(time.RFC3339))
			}
			if got.Notifies != tc.notify[d.ID] {
				t.Errorf("%s: %s notifies = %v, want %v", tc.who, d.Name, got.Notifies, tc.notify[d.ID])
			}
			if (got.LastSeen != nil) != (d.ID == tc.seenFor) {
				t.Errorf("%s: %s last_seen = %v, want it only on the device seen", tc.who, d.Name, got.LastSeen)
			}
		}
		var keys struct {
			Devices []map[string]any `json:"devices"`
		}
		_ = json.Unmarshal(raw, &keys)
		for _, d := range keys.Devices {
			for k := range d {
				if !selfDeviceKeys[k] {
					t.Errorf("%s: a row carries the key %q", tc.who, k)
				}
			}
		}
		for _, leak := range []string{"PUBKEY-", "BLOB-", "PUSHROUTE-", "public_key", "token", "hash", "endpoint", linGone.ID} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s: the answer carries %q: %s", tc.who, leak, raw)
			}
		}
	}
}

// TestSelfDeviceDelete pins DELETE /v1/self/devices/{id}: a person removes
// their own device with 204 and no body, after which the device's token is
// refused on the approver routes and its push route is no target. Another
// person's id, a missing id and a second removal answer the same status
// and the same bytes, and leave the other person's device in place. The
// removal writes exactly one straza.identity.updated record, the record a
// device writes when it retires itself: the action, the device, the person
// and via self, and nothing else. The refused removals write none.
func TestSelfDeviceDelete(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	lin := mkHuman(t, app, "lin")
	mo := mkHuman(t, app, "max")
	_, linDevice, linToken := enrollApprover(t, app, lin.ID)
	_, maxDevice, _ := enrollApprover(t, app, mo.ID)
	if err := app.store.Approvers().UpsertPush(ctx, store.ApproverPush{
		DeviceID: linDevice, Kind: "fcm", TokenOrEndpoint: "fcm-lin",
	}); err != nil {
		t.Fatal(err)
	}
	_, linSession := storedSession(t, app, lin.ID, "", "self-service")
	del := func(id string) (int, []byte) {
		code, raw, _ := adminBytes(t, http.MethodDelete, base+"/v1/self/devices/"+id, linSession, "", nil)
		return code, raw
	}

	var foreignCode, missingCode int
	var foreign, missing []byte
	refused := recordsDuring(t, app, func() {
		foreignCode, foreign = del(maxDevice)
		missingCode, missing = del("apd_01a1ddbc-0000-7000-8000-000000000000")
	})
	if foreignCode != http.StatusNotFound || missingCode != foreignCode || string(foreign) != string(missing) {
		t.Errorf("another person's id = %d %s, a missing id = %d %s, want the same 404 and bytes", foreignCode, foreign, missingCode, missing)
	}
	if !strings.Contains(string(missing), selfDeviceMissing) {
		t.Errorf("missing id body = %s, want the sentence %q", missing, selfDeviceMissing)
	}
	if len(refused) != 0 {
		t.Errorf("refused removals wrote records: %v", refused)
	}
	if _, err := app.store.Approvers().GetDevice(ctx, maxDevice); err != nil {
		t.Errorf("max's device is gone after lin named it: %v", err)
	}

	var code int
	var raw []byte
	removed := recordsDuring(t, app, func() { code, raw = del(linDevice) })
	if code != http.StatusNoContent || len(raw) != 0 {
		t.Fatalf("own removal = %d %q, want 204 and no body", code, raw)
	}
	want := []string{"straza.identity.updated " + string(mustJSON(map[string]any{
		"action": "approver-device-revoked", "device": linDevice, "user": lin.ID, "via": "self",
	}))}
	if string(mustJSON(removed)) != string(mustJSON(want)) {
		t.Errorf("own removal wrote\n got %v\nwant %v", removed, want)
	}
	if _, err := app.store.Approvers().GetDevice(ctx, linDevice); err == nil {
		t.Error("lin's device row survived the removal")
	}
	var errResp struct {
		Code string `json:"code"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/approver/pending", linToken, nil, &errResp); code != http.StatusUnauthorized || errResp.Code != codeDeviceRevoked {
		t.Errorf("the removed device's token on pending = %d %q, want 401 %s", code, errResp.Code, codeDeviceRevoked)
	}
	targets, err := app.store.Approvers().ListPushTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.DeviceID == linDevice {
			t.Errorf("the removed device's push route is still a target: %+v", tg)
		}
	}

	again := recordsDuring(t, app, func() { code, raw = del(linDevice) })
	if code != missingCode || string(raw) != string(missing) || len(again) != 0 {
		t.Errorf("second removal = %d %s with records %v, want the missing id's answer and none", code, raw, again)
	}
}

// goneOnRead is a store whose approver device read deletes the row once it
// has read it, as a removal on another lane between the route's read and
// its delete does. fired counts those deletes, the test's positive control.
type goneOnRead struct {
	store.Store
	fired atomic.Int32
}

func (s *goneOnRead) Approvers() store.ApproverRepo {
	return goneOnReadApprovers{s.Store.Approvers(), s}
}

type goneOnReadApprovers struct {
	store.ApproverRepo
	s *goneOnRead
}

func (r goneOnReadApprovers) GetDevice(ctx context.Context, id string) (store.ApproverDevice, error) {
	d, err := r.ApproverRepo.GetDevice(ctx, id)
	if err == nil && r.DeleteDevice(ctx, id) == nil {
		r.s.fired.Add(1)
	}
	return d, err
}

// TestSelfDeviceDeleteLostRace pins a removal that loses the race to another
// lane: the row is gone between the route's read and its delete, so the
// route answers the missing id's 404, byte for byte, and writes no record.
func TestSelfDeviceDeleteLostRace(t *testing.T) {
	t.Parallel()
	hook := &goneOnRead{}
	app, base := testAppPreRun(t, []func(*App){func(a *App) { hook.Store = a.store; a.store = hook }})
	lin := mkHuman(t, app, "lin")
	_, linDevice, _ := enrollApprover(t, app, lin.ID)
	_, linSession := storedSession(t, app, lin.ID, "", "self-service")
	del := func(id string) (int, []byte) {
		code, raw, _ := adminBytes(t, http.MethodDelete, base+"/v1/self/devices/"+id, linSession, "", nil)
		return code, raw
	}
	missingCode, missing := del("apd_01a1ddbc-0000-7000-8000-000000000000")

	var code int
	var raw []byte
	records := recordsDuring(t, app, func() { code, raw = del(linDevice) })
	if n := hook.fired.Load(); n != 1 {
		t.Fatalf("the store deleted the row %d times between read and delete, want once", n)
	}
	if code != missingCode || string(raw) != string(missing) {
		t.Errorf("a removal that lost the race = %d %s, want the missing id's %d %s", code, raw, missingCode, missing)
	}
	if len(records) != 0 {
		t.Errorf("a removal that lost the race wrote records: %v", records)
	}
}

// ListDevices fails while "approvers-list" is armed, the read of
// GET /v1/self/devices.
func (a faultApprovers) ListDevices(ctx context.Context, userID string) ([]store.ApproverDeviceInfo, error) {
	if err := a.f.fault("approvers-list"); err != nil {
		return nil, err
	}
	return a.ApproverRepo.ListDevices(ctx, userID)
}

// TestSelfDevicesReadFailure pins the 500 of a list the store cannot read:
// a sentence a person can act on that names no server process.
func TestSelfDevicesReadFailure(t *testing.T) {
	t.Parallel()
	app, base, faults := testAppFault(t)
	lin := mkHuman(t, app, "lin")
	_, session := storedSession(t, app, lin.ID, "", "self-service")
	faults.arm("approvers-list", errors.New("database is locked"))
	const want = "Straza could not read your approver devices, so it cannot list them. Try again, and tell your administrator if it keeps failing."
	var out struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/self/devices", session, nil, &out); code != http.StatusInternalServerError || out.Error != want {
		t.Errorf("GET /v1/self/devices = %d %q, want 500 %q", code, out.Error, want)
	}
}
