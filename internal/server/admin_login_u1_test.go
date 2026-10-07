package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestDisabledUserIDTokenRefused pins that a disabled user's ID token is
// refused alike on every lane that takes one: the admin plane, the person
// routes, enroll and the session exchange each answer 403 with the words
// of the session lane and write exactly one login failure record that
// names the user, with via id-token and the reason. An active user's ID
// token still opens the admin plane, and a locked user keeps its sentence.
func TestDisabledUserIDTokenRefused(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	kimTok := loginDeviceFlow(t, base, "kim", "hunter2!")
	dee := mkHuman(t, app, "dee", AdminRole)
	held := loginDeviceFlow(t, base, "dee", "hunter2!")
	lee := mkHuman(t, app, "lee", AdminRole)
	leeTok := loginDeviceFlow(t, base, "lee", "hunter2!")

	call := func(method, path, bearer string, body any) (int, string) {
		code, _, out := callJSON(t, method, base+path, bearer, body)
		msg, _ := out["error"].(string)
		return code, msg
	}
	if code, msg := call(http.MethodGet, "/v1/admin/users", held, nil); code != http.StatusOK {
		t.Fatalf("control, dee on the admin plane while active = %d %q, want 200", code, msg)
	}
	dee.Status = store.UserDisabled
	if _, err := app.store.Users().Update(ctx, dee); err != nil {
		t.Fatal(err)
	}

	const disabled = "user is disabled. Contact your administrator"
	device := map[string]any{"name": "dee-laptop", "platform": "linux", "fingerprint": "fp-dee"}
	lanes := []struct {
		name, method, path, harness string
		body                        func(tok string) any
		bearer                      bool
	}{
		{"admin plane", http.MethodGet, "/v1/admin/users", "", nil, true},
		{"person routes", http.MethodPost, "/v1/approvals/self/enroll-token", "", nil, true},
		{"enroll", http.MethodPost, "/v1/enroll", "", func(tok string) any {
			return map[string]any{"id_token": tok, "device": device}
		}, false},
		{"session exchange", http.MethodPost, "/v1/checkin", "console/1", func(tok string) any {
			return map[string]any{
				"id_token":    tok,
				"harness":     map[string]string{"name": "console", "version": "1"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
			}
		}, false},
	}
	for _, lane := range lanes {
		t.Run("a disabled user on the "+lane.name, func(t *testing.T) {
			before := len(authnFailures(t, app, dee.ID, "user is disabled"))
			bearer := ""
			var body any
			if lane.bearer {
				bearer = held
			}
			if lane.body != nil {
				body = lane.body(held)
			}
			if code, msg := call(lane.method, lane.path, bearer, body); code != http.StatusForbidden || msg != disabled {
				t.Errorf("answer = %d %q, want 403 %q", code, msg, disabled)
			}
			recs := authnFailures(t, app, dee.ID, "user is disabled")
			if len(recs) != before+1 {
				t.Fatalf("login failure records naming dee = %d, want exactly one more than %d", len(recs), before)
			}
			rec := recs[len(recs)-1]
			if rec["via"] != "id-token" || rec["user"] != "dee" || rec["userId"] != dee.ID {
				t.Errorf("record = %v, want via id-token, user dee, userId %s", rec, dee.ID)
			}
			if lane.harness != "" && rec["harness"] != lane.harness {
				t.Errorf("record harness = %v, want %s", rec["harness"], lane.harness)
			}
		})
	}

	t.Run("an active user, the positive control", func(t *testing.T) {
		before := len(authnFailures(t, app, kim.ID, ""))
		if code, msg := call(http.MethodGet, "/v1/admin/users", kimTok, nil); code != http.StatusOK {
			t.Errorf("kim on the admin plane = %d %q, want 200", code, msg)
		}
		if n := len(authnFailures(t, app, kim.ID, "")); n != before {
			t.Errorf("login failure records for kim = %d, want %d", n, before)
		}
	})

	t.Run("a locked user keeps its sentence", func(t *testing.T) {
		if code, msg := call(http.MethodPost, "/v1/admin/users/"+lee.ID+"/lock", kimTok, map[string]any{"reason": "test"}); code != http.StatusOK {
			t.Fatalf("kim locks lee = %d %q", code, msg)
		}
		want := "the user lee is locked, so this sign-in is refused. An administrator lifts the lock with strazactl users unlock lee."
		if code, msg := call(http.MethodGet, "/v1/admin/users", leeTok, nil); code != http.StatusForbidden || msg != want {
			t.Errorf("answer = %d %q, want 403 %q", code, msg, want)
		}
		if n := len(authnFailures(t, app, lee.ID, "user is locked")); n != 1 {
			t.Errorf("locked login failure records = %d, want 1", n)
		}
	})
}
