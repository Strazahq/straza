package main

import (
	"net/http"
	"strings"
	"testing"
)

// fixContacted is what a contact of github answers, a description holding
// an escape sequence and a line break.
const fixContacted = `{"object":"App/github","host":"api.github.example","contacted_at":"2026-09-25T10:00:00Z",` +
	`"server":{"name":"github-mcp","version":"1.4.0"},"tools":[{"name":"get_me","description":"Read the user.","read_only":true},` +
	`{"name":"create_issue","description":"Open an issue.\u001b[31m Red\nnext","read_only":false}]}`

// TestDraftsContact pins drafts contact: the answer line and one line per
// tool, the server's text printed with no control character, the server's
// no, a refused address or an upstream that did not answer, exiting 1, a
// refusal of the caller exiting 2, and the body under --json.
func TestDraftsContact(t *testing.T) {
	const unanswered = "Straza contacted api.github.example and it did not answer as an MCP server: it answered with HTTP 500. Check the address in the draft."
	const refused = "Straza does not contact meta.test: it resolves to 169.254.169.254, a cloud metadata address, where a request from strazad reaches strazad's own host or its cloud's metadata. " +
		"Publish the server if you mean it, or give it an address outside those ranges."
	const standing = "Contacting a proposed server needs the scope apps:write or the role straza-global-mcp-admin, because Straza dials the address the draft names."
	tests := []struct {
		name     string
		args     []string
		reply    draftReply
		wantOut  string
		wantErr  string
		wantCode int
	}{
		{name: "an answer", reply: draftReply{body: fixContacted},
			wantOut: "github at api.github.example answered as github-mcp 1.4.0 with 2 tools:\n" +
				"  get_me  Read the user.\n  create_issue  Open an issue.U+001B[31m Red next\n"},
		{name: "an upstream that did not answer", reply: draftReply{code: http.StatusBadGateway, body: `{"error":"` + unanswered + `"}`},
			wantErr: unanswered, wantCode: 1},
		{name: "a refused address", reply: draftReply{code: http.StatusConflict, body: `{"error":"` + refused + `"}`},
			wantErr: refused, wantCode: 1},
		{name: "a caller without the standing", reply: draftReply{code: http.StatusForbidden, body: `{"error":"` + standing + `"}`},
			wantErr: standing, wantCode: 2},
		{name: "a proxy's 502 with no sentence", reply: draftReply{code: http.StatusBadGateway, body: `<html></html>`},
			wantErr: noSentence(http.StatusBadGateway), wantCode: 2},
		{name: "the body under --json", args: []string{"--json"}, reply: draftReply{code: http.StatusBadGateway, body: `{"error":"` + unanswered + `"}`},
			wantOut: indented(t, `{"error":"`+unanswered+`"}`), wantErr: unanswered, wantCode: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{"POST /v1/admin/drafts/41/contact": tc.reply})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", append([]string{"drafts", "contact", "41", "github"}, tc.args...)...)
			if code != tc.wantCode || errText(err) != tc.wantErr || stdout != tc.wantOut {
				t.Errorf("exit %d, err %v, stdout %q\nwant exit %d, err %q, stdout %q", code, err, stdout, tc.wantCode, tc.wantErr, tc.wantOut)
			}
			if got := s.calls(); len(got) != 1 || got[0].body != `{"object":"App/github"}` {
				t.Errorf("requests = %+v, want one with the object App/github", got)
			}
		})
	}
}

// TestDraftsContactRefusedInsideACodingAgent pins the guard on contact: on
// the login inside a coding agent nothing is sent and it exits 2.
func TestDraftsContactRefusedInsideACodingAgent(t *testing.T) {
	s, srv := newDraftsServer(t, map[string]draftReply{"POST /v1/admin/drafts/41/contact": {body: fixContacted}})
	t.Setenv("STRAZA_SERVER", "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv(apiTokenEnv, "")
	_, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "contact", "41", "github")
	if code != 2 || !strings.HasPrefix(errText(err), "CLAUDECODE is set, so strazactl runs inside a coding agent") {
		t.Errorf("exit %d, err %v, want 2 with the guard's refusal", code, err)
	}
	assertCalls(t, s, 0)
}

// TestDraftsContactTakesTwoOperands pins the usage of contact.
func TestDraftsContactTakesTwoOperands(t *testing.T) {
	s, srv := newDraftsServer(t, nil)
	t.Setenv("STRAZA_SERVER", "")
	_, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "", "drafts", "contact", "41")
	want := "strazactl drafts contact takes two operands, a draft number and a server name, such as 41 github, and got 1. List the drafts with strazactl drafts list"
	if code != 2 || errText(err) != want {
		t.Errorf("exit %d, err %v, want 2 %q", code, err, want)
	}
	assertCalls(t, s, 0)
}
