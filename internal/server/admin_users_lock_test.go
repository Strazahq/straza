package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUserLockAnswersWhereItHolds pins what POST /v1/admin/users/{id}/lock
// answers when its writes fail. Only a lock whose revocation row and
// straza.revocation.user event were both written answers 200. A failed
// event leaves the other servers admitting the user until they restart, or
// for good when the row failed too, and a failed row leaves a lock that the
// console and the SCIM lock block do not show and an identity manager can
// lift. Each of those answers 500 with the sentence for how far the lock
// reached and the request id. In every case this server's denylist blocks
// the user, the user.lock and user.killed records are written exactly once,
// and the Error records of a 500 carry its request id. Locking again then
// answers 200 and writes the missing row or event.
func TestUserLockAnswersWhereItHolds(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	cases := []struct {
		name   string
		arm    []string // the faultStore keys armed around the lock
		status int
		rows   int    // the user's revocation rows after the lock
		events int    // the user's straza.revocation.user events after the lock
		msg    string // the 500 sentence, formatted with the username and the request id
	}{
		{name: "both writes land", status: http.StatusOK, rows: 1, events: 1},
		{name: "the row fails and the event lands", arm: []string{"revocations"},
			status: http.StatusInternalServerError, rows: 0, events: 1, msg: userLockUnsavedMsg},
		{name: "the event fails and the row lands", arm: []string{"outbox:straza.revocation.user"},
			status: http.StatusInternalServerError, rows: 1, events: 0, msg: userLockUnsentMsg},
		{name: "both writes fail", arm: []string{"revocations", "outbox:straza.revocation.user"},
			status: http.StatusInternalServerError, rows: 0, events: 0, msg: userLockHereOnlyMsg},
	}
	// lock locks u and checks what this server and the store hold after it:
	// the denylist entry, rows revocation rows, events revocation events, and
	// one user.lock and one user.killed record per lock sent so far.
	lock := func(t *testing.T, u store.User, locks, rows, events int) (int, http.Header, map[string]any) {
		t.Helper()
		code, hdr, out := callJSON(t, http.MethodPost, base+"/v1/admin/users/"+u.ID+"/lock", bearer,
			map[string]string{"reason": "suspected leak"})
		if !app.denylist.userBlocked(u.ID) {
			t.Error("the user is not on this server's denylist after the lock")
		}
		if got, err := app.store.Revocations().ListByTarget(ctx, store.RevokeUser, u.ID); err != nil || len(got) != rows {
			t.Errorf("revocation rows = %d (%v), want %d", len(got), err, rows)
		}
		if got := eventsNaming(t, app, "straza.revocation.user", "user", u.ID); len(got) != events {
			t.Errorf("straza.revocation.user events = %v, want %d", got, events)
		}
		admin := eventsNaming(t, app, "straza.audit.admin", "target", u.ID)
		if len(admin) != locks {
			t.Errorf("straza.audit.admin records = %v, want %d", admin, locks)
		}
		for _, rec := range admin {
			if rec["action"] != "user.lock" || rec["reason"] != "suspected leak" || rec["actor"] != "kim" {
				t.Errorf("straza.audit.admin record = %v, want user.lock by kim with the reason", rec)
			}
		}
		killed := eventsNaming(t, app, "straza.audit.identity", "user", u.ID)
		if len(killed) != locks {
			t.Errorf("straza.audit.identity records = %v, want %d", killed, locks)
		}
		for _, rec := range killed {
			if rec["action"] != "user.killed" || rec["reason"] != "suspected leak" {
				t.Errorf("straza.audit.identity record = %v, want user.killed with the reason", rec)
			}
		}
		return code, hdr, out
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("lockee%d", i)
			u := mkHuman(t, app, name)
			for _, key := range tc.arm {
				fs.arm(key, pgDown)
			}
			code, hdr, out := lock(t, u, 1, tc.rows, tc.events)
			fs.disarm()
			if code != tc.status {
				t.Fatalf("lock = %d %v, want %d", code, out, tc.status)
			}
			id := hdr.Get(requestIDHeader)
			var tagged []string
			for _, rec := range errorRecords(buf) {
				if strings.Contains(rec, "correlation_id="+id) {
					tagged = append(tagged, rec)
				}
			}
			switch tc.msg {
			case "":
				if out["locked"] != true || out["id"] != u.ID {
					t.Errorf("lock body = %v, want locked true for %s", out, u.ID)
				}
				if len(tagged) != 0 {
					t.Errorf("Error records of the lock = %q, want none", tagged)
				}
				return
			case userLockUnsentMsg:
				if want := fmt.Sprintf(tc.msg, name, name, id); out["error"] != want || out[correlationKey] != id {
					t.Errorf("lock error = %v, want %q under %s", out, want, id)
				}
			default:
				if want := fmt.Sprintf(tc.msg, name, id); out["error"] != want || out[correlationKey] != id {
					t.Errorf("lock error = %v, want %q under %s", out, want, id)
				}
			}
			// One revoke line and the answer's line, both under the request id.
			if len(tagged) != 2 || !strings.Contains(tagged[0]+tagged[1], "user="+u.ID) {
				t.Errorf("Error records under %s = %q, want the revoke line and the answer's line", id, tagged)
			}
			// Locking again writes the missing row or event and answers 200.
			if code, _, out := lock(t, u, 2, tc.rows+1, tc.events+1); code != http.StatusOK {
				t.Errorf("second lock = %d %v, want 200", code, out)
			}
		})
	}
}
