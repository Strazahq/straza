package main

import (
	"net/http"
	"strings"
	"testing"
)

// fixConflicts is the 409 of a Check again whose field both sides changed.
const fixConflicts = `{"error":"Draft 41 and live state both changed App/demo-tools straza.limits.rps since the draft was checked. Pick which value to keep, then check again.",` +
	`"conflicts":[{"object":"App/demo-tools","field":"straza.limits.rps","base":"5","draft":"20","live":"10"},` +
	`{"object":"App/demo-tools","field":"straza.exposure.tools","base":"","draft":"[\"a\"]","live":"[\"b\"]"}]}`

// TestDraftsRebase pins drafts rebase: it reads the draft for its
// revision and sends it with the picks, prints the new revision with its
// verdict and the create's last line, and on a conflict prints each field
// with its three values and the pick that settles it, exiting 1.
func TestDraftsRebase(t *testing.T) {
	const conflict = "Draft 41 and live state both changed App/demo-tools straza.limits.rps since the draft was checked. Pick which value to keep, then check again."
	tests := []struct {
		name     string
		args     []string
		reply    draftReply
		wantBody string
		wantOut  []string
		wantErr  string
		wantCode int
	}{
		{name: "a mechanical Check again", reply: draftReply{body: fixAnswer("41", 3, "", "")},
			wantBody: `{"revision":2}`,
			wantOut: []string{"Checked draft 41 again against live state: it is at revision 3. Nothing changes until a person publishes it.\n",
				"The draft can be published: strazactl drafts publish 41\n"}},
		{name: "picks", args: []string{"--pick", "App/demo-tools straza.limits.rps=draft", "--pick", "App/demo-tools straza.exposure.tools=live"},
			reply:    draftReply{body: fixAnswer("41", 3, "", "")},
			wantBody: `{"revision":2,"picks":{"App/demo-tools straza.exposure.tools":"live","App/demo-tools straza.limits.rps":"draft"}}`,
			wantOut:  []string{"it is at revision 3."}},
		{name: "fields that need a pick", reply: draftReply{code: http.StatusConflict, body: fixConflicts},
			wantBody: `{"revision":2}`,
			wantOut: []string{"  App/demo-tools straza.limits.rps\n    at the check: 5\n    in the draft: 20\n    on live: 10\n",
				"  App/demo-tools straza.exposure.tools\n    at the check: (not set)\n",
				"Pick each value and check again, as in strazactl drafts rebase 41 --pick \"App/demo-tools straza.limits.rps=draft\", or =live to take live's.\n"},
			wantErr: conflict, wantCode: 1},
		{name: "nothing moved", reply: draftReply{code: http.StatusConflict,
			body: `{"error":"Nothing that draft 41 holds changed on live state since it was checked, so there is nothing to check again."}`},
			wantBody: `{"revision":2}`,
			wantErr:  "Nothing that draft 41 holds changed on live state since it was checked, so there is nothing to check again.", wantCode: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{
				"GET /v1/admin/drafts/41":         {body: fixDetail("open", fixStale, "", false, "")},
				"POST /v1/admin/drafts/41/rebase": tc.reply,
			})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", append([]string{"drafts", "rebase", "41"}, tc.args...)...)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			wantInOrder(t, stdout, tc.wantOut...)
			calls := s.calls()
			if len(calls) != 2 || calls[0].method+" "+calls[0].uri != "GET /v1/admin/drafts/41" || calls[1].body != tc.wantBody {
				t.Errorf("requests = %+v, want the read and a rebase with %s", calls, tc.wantBody)
			}
		})
	}
}

// TestDraftsRebaseRefusesAPickItCannotRead pins the --pick form: a pick
// that is not Kind/Name field=draft or =live exits 2 and sends nothing.
func TestDraftsRebaseRefusesAPickItCannotRead(t *testing.T) {
	for _, pick := range []string{"App/demo-tools=draft", "App/demo-tools straza.limits.rps=both", "demo-tools straza.limits.rps=live", "App/demo-tools straza.limits.rps"} {
		t.Run(pick, func(t *testing.T) {
			s, srv := newDraftsServer(t, nil)
			t.Setenv("STRAZA_SERVER", "")
			_, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "rebase", "41", "--pick", pick)
			want := "--pick \"" + pick + "\" is not Kind/Name field=draft or Kind/Name field=live, such as --pick \"App/demo-tools straza.limits.rps=draft\""
			if code != 2 || errText(err) != want {
				t.Errorf("exit %d, err %v, want 2 %q", code, err, want)
			}
			assertCalls(t, s, 0)
		})
	}
}

// TestDraftsRebaseRefusedInsideACodingAgent pins the guard on rebase: on
// the login inside a coding agent it refuses before its read.
func TestDraftsRebaseRefusedInsideACodingAgent(t *testing.T) {
	s, srv := newDraftsServer(t, map[string]draftReply{"GET /v1/admin/drafts/41": {body: fixDetail("open", "", "", true, "")}})
	t.Setenv("STRAZA_SERVER", "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv(apiTokenEnv, "")
	_, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "rebase", "41")
	if code != 2 || !strings.HasPrefix(errText(err), "CLAUDECODE is set, so strazactl runs inside a coding agent") {
		t.Errorf("exit %d, err %v, want 2 with the guard's refusal", code, err)
	}
	assertCalls(t, s, 0)
}
