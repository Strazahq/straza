package server

import (
	"net/http"
	"testing"
	"time"
)

// TestLockedUserLoginRefused pins that a lock closes every lane that takes an
// ID token: the admin plane with the self-unlock, the person routes, enroll
// and the session exchange. Each refusal says how the lock is lifted and
// writes one login failure record, and an unlock opens the lanes again.
func TestLockedUserLoginRefused(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	kimTok := loginDeviceFlow(t, base, "kim", "hunter2!")
	lee := mkHuman(t, app, "lee", AdminRole)
	held := loginDeviceFlow(t, base, "lee", "hunter2!")

	call := func(method, path, bearer string, body any) (int, string) {
		var out any
		code := adminReq(t, method, base+path, bearer, body, &out)
		m, _ := out.(map[string]any)
		msg, _ := m["error"].(string)
		return code, msg
	}
	device := map[string]any{"name": "lee-laptop", "platform": "linux", "fingerprint": "fp-lee"}
	lanes := []struct {
		name string
		call func(idToken string) (int, string)
	}{
		{"admin plane", func(tok string) (int, string) { return call("GET", "/v1/admin/apps", tok, nil) }},
		{"self-unlock", func(tok string) (int, string) {
			return call("POST", "/v1/admin/users/"+lee.ID+"/unlock", tok, map[string]any{})
		}},
		{"person routes", func(tok string) (int, string) { return call("POST", "/v1/approvals/self/enroll-token", tok, nil) }},
		{"enroll", func(tok string) (int, string) {
			return call("POST", "/v1/enroll", "", map[string]any{"id_token": tok, "device": device})
		}},
		{"session exchange", func(tok string) (int, string) {
			return call("POST", "/v1/checkin", "", map[string]any{
				"id_token":    tok,
				"harness":     map[string]string{"name": "console", "version": "1"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			})
		}},
	}
	want := "the user lee is locked, so this sign-in is refused. An administrator lifts the lock with strazactl users unlock lee."
	lockedRecords := func() int {
		return countAuthn(t, app, func(d map[string]any) bool {
			return d["action"] == "login" && d["outcome"] == "failure" && d["reason"] == "user is locked"
		})
	}

	if code, msg := lanes[0].call(held); code != http.StatusOK {
		t.Fatalf("control, lee on the admin plane before the lock = %d %q, want 200", code, msg)
	}
	if code, msg := call("POST", "/v1/admin/users/"+lee.ID+"/lock", kimTok, map[string]any{"reason": "test"}); code != http.StatusOK {
		t.Fatalf("kim locks lee = %d %q", code, msg)
	}
	fresh := loginDeviceFlow(t, base, "lee", "hunter2!")

	refused := 0
	for _, lane := range lanes {
		for _, tok := range []struct{ name, value string }{{"held from before the lock", held}, {"fetched after the lock", fresh}} {
			t.Run(lane.name+", ID token "+tok.name, func(t *testing.T) {
				refused++
				if code, msg := lane.call(tok.value); code != http.StatusForbidden || msg != want {
					t.Errorf("answer = %d %q, want 403 %q", code, msg, want)
				}
			})
		}
	}
	if !app.denylist.userBlocked(lee.ID) {
		t.Fatalf("lee is no longer locked after the refused calls")
	}
	record := waitAuthn(t, app, "locked login failure", func(d map[string]any) bool {
		return d["reason"] == "user is locked"
	})
	if record["via"] != "id-token" || record["user"] != "lee" || record["userId"] != lee.ID || record["outcome"] != "failure" {
		t.Errorf("locked login record = %v, want via id-token, user lee, userId %s, outcome failure", record, lee.ID)
	}
	deadline := time.Now().Add(10 * time.Second)
	for lockedRecords() < refused && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := lockedRecords(); n != refused {
		t.Errorf("locked login records = %d, want one per refused call, %d", n, refused)
	}

	t.Run("a restart keeps the lock", func(t *testing.T) {
		app.denylist.allowUser(lee.ID)
		if err := app.rebuildDenylist(t.Context(), app.store); err != nil {
			t.Fatal(err)
		}
		if code, msg := lanes[0].call(held); code != http.StatusForbidden || msg != want {
			t.Errorf("after the replay of stored locks = %d %q, want 403 %q", code, msg, want)
		}
	})

	if code, msg := call("POST", "/v1/admin/users/"+lee.ID+"/unlock", kimTok, map[string]any{}); code != http.StatusOK {
		t.Fatalf("kim unlocks lee = %d %q", code, msg)
	}
	if code, msg := lanes[0].call(held); code != http.StatusOK {
		t.Errorf("lee on the admin plane after the unlock = %d %q, want 200", code, msg)
	}
}

// TestBreakGlassSurvivesLockAttempt pins the lockout guarantee next to the
// lock check on sign-in: the break-glass admin cannot be locked, so its ID
// token still opens the admin plane after the attempt.
func TestBreakGlassSurvivesLockAttempt(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	kimTok := loginDeviceFlow(t, base, "kim", "hunter2!")
	bg := setPasswordDirect(t, app, BreakGlassUsername, "vaulted-secret")

	if code := adminReq(t, "POST", base+"/v1/admin/users/"+bg.ID+"/lock", kimTok, map[string]any{"reason": "test"}, nil); code != http.StatusForbidden {
		t.Fatalf("lock of break-glass = %d, want 403", code)
	}
	bgTok := loginDeviceFlow(t, base, BreakGlassUsername, "vaulted-secret")
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bgTok, nil, nil); code != http.StatusOK {
		t.Errorf("break-glass on the admin plane after the lock attempt = %d, want 200", code)
	}
}
