package agentguard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// unjudgedHead and unjudgedTail frame the daemon's line for a device
// check-in answer that judged nothing about the session.
const (
	unjudgedHead = "daemon: session renewal got an answer that does not say whether this session is still accepted: "
	unjudgedTail = " The daemon keeps the session and tries again at the next poll. If this keeps happening, " +
		"check that the configured server URL reaches strazad with nothing in front of it that limits or blocks straza, and run `straza doctor`."
)

// TestDaemonReacquireClassesLikeTheHook pins that the daemon classes the
// device check-in's answer after a 401 on the refresh the way the hook's
// renewal does (renewalAnswer). A 408 or a 429, with a reason or without
// one, and another status below 500 with no reason keep the session and
// mark nothing, with one line that says the answer judged nothing and that
// the daemon tries again. A refusal that judged the session, strazad's 403
// or a 400 with its reason and a 403 with no reason, drops the session and
// marks it revoked, as before.
func TestDaemonReacquireClassesLikeTheHook(t *testing.T) {
	rows := []struct {
		name     string
		status   int
		msg      string
		wantGone bool
		wantLine string
	}{
		{name: "a proxy's 429 with no reason", status: http.StatusTooManyRequests,
			wantLine: unjudgedHead + "the server answered HTTP 429 and gave no reason." + unjudgedTail},
		{name: "a 429 with a reason", status: http.StatusTooManyRequests, msg: "rate limited. Retry shortly",
			wantLine: unjudgedHead + "the server answered HTTP 429: rate limited. Retry shortly." + unjudgedTail},
		{name: "a proxy's 408 with no reason", status: http.StatusRequestTimeout,
			wantLine: unjudgedHead + "the server answered HTTP 408 and gave no reason." + unjudgedTail},
		{name: "a 404 with no reason", status: http.StatusNotFound,
			wantLine: unjudgedHead + "the server answered HTTP 404 and gave no reason." + unjudgedTail},
		{name: "strazad's 403 with its reason", status: http.StatusForbidden,
			msg: "Straza: this device or user has been revoked. Contact your administrator", wantGone: true,
			wantLine: "daemon: session renewal refused (this device or user has been revoked. Contact your administrator)"},
		{name: "a 403 with no reason", status: http.StatusForbidden, wantGone: true,
			wantLine: "daemon: session renewal refused (the server answered HTTP 403 and gave no reason. " +
				"Ask your administrator whether this user or device is still enabled, and check whether a proxy in front of strazad replaces its answers.)"},
		{name: "a 400 with a reason", status: http.StatusBadRequest, msg: "device credential is malformed: enroll this machine again", wantGone: true,
			wantLine: "daemon: session renewal refused (device credential is malformed: enroll this machine again)"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := &reacquireCheckin{
				refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected: re-enroll or restart the session",
				deviceStatus: row.status, deviceMsg: row.msg, snapshotID: "snap",
			}
			srv := httptest.NewServer(h.handler(t))
			defer srv.Close()
			store := seedDaemonReacquire(t, srv.URL)

			var out bytes.Buffer
			d := NewDaemon(store, &out)
			if gone := d.refreshOnce(t); gone != row.wantGone {
				t.Fatalf("the daemon reported the session gone %v, want %v; output:\n%s", gone, row.wantGone, out.String())
			}
			if got := h.deviceCalls.Load(); got != 1 {
				t.Errorf("device-lane check-ins = %d, want 1", got)
			}
			ses, serr := store.LoadSession()
			rev, rerr := store.LoadRevocation()
			switch {
			case row.wantGone && serr == nil:
				t.Error("session state survived a refusal that judged the session")
			case row.wantGone && (rerr != nil || rev.Reason == ""):
				t.Errorf("no revocation marker after a refusal that judged the session (rev %+v, err %v)", rev, rerr)
			case !row.wantGone && serr != nil:
				t.Errorf("session state dropped after an answer that judged nothing: %v", serr)
			case !row.wantGone && (ses.SessionID != "s-old" || ses.SessionToken != "tok-old"):
				t.Errorf("session = %s/%s after an answer that judged nothing, want the kept s-old/tok-old", ses.SessionID, ses.SessionToken)
			case !row.wantGone && rerr == nil:
				t.Errorf("revocation marker %+v after an answer that judged nothing", rev)
			}
			var lines []string
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.HasPrefix(line, "daemon: session renewal") {
					lines = append(lines, line)
				}
			}
			if len(lines) != 1 || lines[0] != row.wantLine {
				t.Errorf("renewal lines = %q, want one line %q", lines, row.wantLine)
			}
		})
	}
}
