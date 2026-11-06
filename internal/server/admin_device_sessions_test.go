package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestDeviceRevokeEndsItsSessions pins that a device revoke ends the device's
// sessions with it, so GET /v1/admin/sessions lists them as revoked at once,
// and that each ended session gets exactly one session.end record with its
// user, its id and the revoke's reason. An admin revoke's outcome is
// revoked-admin, and the revoke of a reused key's is revoked-key-reuse, with
// the address of the check-in that used the copied key. A session of
// another device of the same user stays active and gets no record.
func TestDeviceRevokeEndsItsSessions(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	enrolled := 0
	enroll := func(t *testing.T) *keyHolder {
		t.Helper()
		enrolled++
		h := newKeyHolder(t, base)
		h.enroll(idToken, fmt.Sprintf("sha256:b070-%d", enrolled))
		return h
	}
	listed := func(t *testing.T) map[string]string {
		t.Helper()
		var rows []sessionPayload
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/sessions?user="+kim.ID, admin, nil, &rows); code != http.StatusOK {
			t.Fatalf("the sessions list = %d", code)
		}
		out := map[string]string{}
		for _, s := range rows {
			out[s.ID] = s.Status
		}
		return out
	}
	ends := func(session string) []map[string]any {
		var out []map[string]any
		for _, ev := range outboxDataFor(t, app, "straza.audit.authn") {
			if ev["action"] == "session.end" && ev["session"] == session {
				out = append(out, ev)
			}
		}
		return out
	}

	cases := []struct {
		name, reason, outcome string
		// revoke revokes the device h holds.
		revoke func(t *testing.T, h *keyHolder)
	}{
		{"an admin revoke", deviceRevokedByAdmin, "revoked-admin", func(t *testing.T, h *keyHolder) {
			code, _, out := callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+kim.ID+"/devices/"+h.device, admin, nil)
			if code != http.StatusOK {
				t.Fatalf("the revoke = %d %v", code, out)
			}
		}},
		{"the key reuse refusal", "device key reused", "revoked-key-reuse", func(t *testing.T, h *keyHolder) {
			if code, out := h.call("/v1/checkin", "", h.checkinBody(1)); code != http.StatusForbidden || out["error"] != keyReusedMsg {
				t.Fatalf("a reused counter = %d %v, want 403 %q", code, out, keyReusedMsg)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, other := enroll(t), enroll(t)
			_, first := h.mustCheckin()
			_, second := h.mustCheckin()
			_, kept := other.mustCheckin()
			tc.revoke(t, h)

			statuses := listed(t)
			for id, want := range map[string]string{first: "revoked", second: "revoked", kept: "active"} {
				if statuses[id] != want {
					t.Errorf("the sessions list shows session %s as %q, want %s", id, statuses[id], want)
				}
			}
			for _, id := range []string{first, second} {
				got := ends(id)
				if len(got) != 1 {
					t.Fatalf("%d session.end records for session %s, want 1: %v", len(got), id, got)
				}
				for k, v := range map[string]any{"outcome": tc.outcome, "userId": kim.ID, "session": id, "reason": tc.reason} {
					if got[0][k] != v {
						t.Errorf("the session.end record's %s = %v, want %v", k, got[0][k], v)
					}
				}
				if ip, _ := got[0]["sourceIp"].(string); ip == "" {
					t.Errorf("the session.end record carries no sourceIp: %v", got[0])
				}
			}
			if got := ends(kept); len(got) != 0 {
				t.Errorf("session.end records for the other device's session: %v, want none", got)
			}
		})
	}
}

// TestDeviceRevokeLostAnswer pins a device revoke whose write ended without
// a clear answer. When the device row is gone, the revoke committed, so it
// answers 200 and writes device.revoke and a session.end for each session
// it ended. When the sessions could not be read before it, their
// session.end records are missing, which one Error line and one count of
// straza_outbox_records_lost_total say. When the row is still there,
// nothing changed, so it answers 500 with the sentence that says so and
// writes no record.
func TestDeviceRevokeLostAnswer(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	lostConn := &pgconn.PgError{Severity: "FATAL", Code: "08006", Message: "connection failure during the commit"}
	const unlisted = "device revoke: the revoke committed but the device's sessions could not be listed, so the sessions it ended have no session.end record"
	cases := []struct {
		name          string
		arm           []string
		status        int
		revokes, ends int
		session       string
		lost          float64
	}{
		{"a commit whose answer was lost", []string{"devices.revoke.lost"}, http.StatusOK, 1, 1, "revoked", 0},
		{"a lost answer after the sessions could not be read", []string{"devices.revoke.lost", "session-list"}, http.StatusOK, 1, 0, "revoked", 1},
		{"a write that ended without an answer and did not commit", []string{"devices.revoke"}, http.StatusInternalServerError, 0, 0, "active", 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.enroll(idToken, fmt.Sprintf("sha256:b070-lost-%d", i))
			_, sid := h.mustCheckin()
			lostBefore := counterValue(t, app, "straza_outbox_records_lost_total")
			for _, a := range tc.arm {
				fs.arm(a, lostConn)
			}
			code, _, out := callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+kim.ID+"/devices/"+h.device, admin, nil)
			fs.disarm()
			if code != tc.status {
				t.Fatalf("the revoke = %d %v, want %d", code, out, tc.status)
			}
			nothing := fmt.Sprintf("device %s was not revoked: Straza could not write the revocation to its database, so nothing changed", h.device)
			if msg, _ := out["error"].(string); tc.status != http.StatusOK && !strings.HasPrefix(msg, nothing) {
				t.Errorf("the answer = %q, want the sentence that nothing changed", msg)
			}
			revokes := 0
			for _, ev := range adminAuditEvents(t, app) {
				if ev["action"] == actionDeviceRevoke && ev["target"] == h.device {
					revokes++
				}
			}
			ends := 0
			for _, ev := range outboxDataFor(t, app, "straza.audit.authn") {
				if ev["action"] == "session.end" && ev["session"] == sid {
					ends++
				}
			}
			if revokes != tc.revokes || ends != tc.ends {
				t.Errorf("%d device.revoke and %d session.end records, want %d and %d", revokes, ends, tc.revokes, tc.ends)
			}
			if got := counterValue(t, app, "straza_outbox_records_lost_total") - lostBefore; got != tc.lost {
				t.Errorf("straza_outbox_records_lost_total grew by %v, want %v", got, tc.lost)
			}
			lines := 0
			for _, line := range errorRecords(buf) {
				if strings.Contains(line, unlisted) && strings.Contains(line, "device="+h.device) {
					lines++
				}
			}
			if lines != int(tc.lost) {
				t.Errorf("%d Error lines that the ended sessions could not be listed, want %v", lines, tc.lost)
			}
			if ses, err := app.store.Sessions().GetByID(context.Background(), sid); err != nil || ses.Status != tc.session {
				t.Errorf("the session is %q (%v), want %s", ses.Status, err, tc.session)
			}
		})
	}
}
