package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// publishBody decodes the body of the one publish among requests, or fails
// the test when there is none.
func publishBody(t *testing.T, requests []draftRequest) map[string]any {
	t.Helper()
	for _, r := range requests {
		if r.method == http.MethodPost && strings.HasSuffix(r.uri, "/publish") {
			var body map[string]any
			if err := json.Unmarshal([]byte(r.body), &body); err != nil {
				t.Fatalf("publish body %q: %v", r.body, err)
			}
			return body
		}
	}
	t.Fatalf("no publish among %+v", requests)
	return nil
}

// fixPublished is a publish answer with a started, a failed and a removed
// server and one next step.
const fixPublished = `{"draft":{"id":"41","revision":2,"state":"published","door":"straza-app","authors":[],"items":[],` +
	`"title":"Add server github","created_at":"2026-09-24T10:14:03Z","updated_at":"2026-09-24T10:51:00Z"},` +
	`"snapshot":"3be0a1","servers":[{"name":"github","change":"created","status":"starting"},` +
	`{"name":"jira","change":"changed","status":"failed","detail":"the start timed out after 20 seconds"},` +
	`{"name":"old-tools","change":"removed","status":"removed"}],` +
	`"next":["Assign github-readers or github-writers in your identity manager."]}`

// TestDraftsPublishAcknowledges pins the publish body the acknowledgments
// make: the revision and risk digest the GET read, the key of every risk
// shown in ticked, and the text typed for each typed risk by key, from the
// questions on one stdin or from --ack.
func TestDraftsPublishAcknowledges(t *testing.T) {
	tests := []struct {
		name       string
		risks      string
		args       []string
		stdin      string
		wantTicked []any
		wantTyped  map[string]any
		wantOut    []string
	}{
		{
			name:       "the typed text and yes from one stdin",
			risks:      fixTyped + "," + fixTick,
			stdin:      "api.githubcopilot.com\ny\n",
			wantTicked: []any{"k-host", "k-impl"},
			wantTyped:  map[string]any{"k-host": "api.githubcopilot.com"},
			wantOut: []string{
				"Publish draft 41: Add server github and then change role developer.\nIt widens access:\n",
				"  widens    App/github Straza will send every caller's own token to api.githubcopilot.com, a host github has not used before. Type api.githubcopilot.com to publish.\n",
				"  widens    Role/developer Holders of developer will also hold github-readers and reach what it reaches.\n",
				"Type api.githubcopilot.com to acknowledge that App/github widens access: Publish? [y/N] ",
			},
		},
		{
			name:       "--yes with an --ack in another case and spaces",
			risks:      fixTyped + "," + fixTick,
			args:       []string{"--yes", "--ack", " API.githubcopilot.com "},
			wantTicked: []any{"k-host", "k-impl"},
			wantTyped:  map[string]any{"k-host": "API.githubcopilot.com"},
			wantOut:    []string{"It widens access:\n"},
		},
		{
			name:       "no risk asks only the last question",
			stdin:      "yes\n",
			wantTicked: []any{},
			wantTyped:  map[string]any{},
			wantOut:    []string{"Publish draft 41: Add server github and then change role developer.\nPublish? [y/N] "},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{
				"GET /v1/admin/drafts/41":          {body: fixDetail("open", "", tc.risks, true, "")},
				"POST /v1/admin/drafts/41/publish": {body: fixPublished},
			})
			t.Setenv("STRAZA_SERVER", "")
			args := append([]string{"drafts", "publish", "41"}, tc.args...)
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), tc.stdin, args...)
			if code != 0 {
				t.Fatalf("exit %d: %v\n%s", code, err, stdout)
			}
			body := publishBody(t, s.requests())
			want := map[string]any{"revision": float64(2), "risk_digest": "rd-1", "ticked": tc.wantTicked, "typed": tc.wantTyped}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("publish body = %v, want %v", body, want)
			}
			wantInOrder(t, stdout, tc.wantOut...)
		})
	}
}

// TestDraftsPublishRefusesBeforeAsking pins every refusal that comes before
// the publish is sent, with its exit status and the calls it made: nothing
// is posted, and a refusal the read decides comes before the check of the
// publish route.
func TestDraftsPublishRefusesBeforeAsking(t *testing.T) {
	read := []string{"GET /v1/admin/drafts/41"}
	checked := []string{"GET /v1/admin/drafts/41", "GET /v1/admin/drafts/41/publish"}
	tests := []struct {
		name      string
		detail    string
		args      []string
		stdin     string
		wantOut   []string
		wantErr   string
		wantCode  int
		wantCalls []string
	}{
		{
			name:      "a refused verdict exits 1 with the refusal lines",
			detail:    fixDetail("open", fixRefused, fixTick, true, ""),
			wantOut:   []string{"  refused   PolicySet/github-writers-access Rule github-pr-hold of github-writers-access names release-approvers, which is not a role in Straza. Create it first, or fix the name.\n"},
			wantErr:   "draft 41 cannot be published: Rule github-pr-hold of github-writers-access names release-approvers, which is not a role in Straza. Create it first, or fix the name.",
			wantCode:  1,
			wantCalls: read,
		},
		{
			name:      "a draft that is not open exits 1",
			detail:    fixDetail("discarded", "", "", false, ""),
			wantErr:   "draft 41 is discarded, so it cannot be published. Read it with strazactl drafts show 41",
			wantCode:  1,
			wantCalls: read,
		},
		{
			name:      "a publisher the server turns away exits 2",
			detail:    fixDetail("open", "", fixTick, false, "You cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write."),
			wantErr:   "You cannot publish draft 41: this draft also changes policy sets, which needs the scope policy:write.",
			wantCode:  2,
			wantCalls: read,
		},
		{
			name:      "--yes with a typed risk no --ack meets exits 2",
			detail:    fixDetail("open", "", fixTyped, true, ""),
			args:      []string{"--yes", "--ack", "api.github.com"},
			wantErr:   "draft 41 needs a typed acknowledgment for App/github. Pass --ack api.githubcopilot.com, or run the command without --yes and type it",
			wantCode:  2,
			wantCalls: checked,
		},
		{
			name:      "a mistyped text exits 2",
			detail:    fixDetail("open", "", fixTyped, true, ""),
			stdin:     "api.github.com\ny\n",
			wantErr:   "the text typed for App/github is not api.githubcopilot.com, so nothing was published. Run the command again and type api.githubcopilot.com, or pass --ack api.githubcopilot.com",
			wantCode:  2,
			wantCalls: checked,
		},
		{
			name:      "an empty text declines",
			detail:    fixDetail("open", "", fixTyped, true, ""),
			stdin:     "\n",
			wantErr:   errAborted().Error(),
			wantCode:  2,
			wantCalls: checked,
		},
		{
			name:      "no at the last question declines",
			detail:    fixDetail("open", "", fixTick, true, ""),
			stdin:     "n\n",
			wantOut:   []string{"Publish? [y/N] "},
			wantErr:   errAborted().Error(),
			wantCode:  2,
			wantCalls: checked,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{
				"GET /v1/admin/drafts/41":          {body: tc.detail},
				"POST /v1/admin/drafts/41/publish": {body: fixPublished},
			})
			t.Setenv("STRAZA_SERVER", "")
			args := append([]string{"drafts", "publish", "41"}, tc.args...)
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), tc.stdin, args...)
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			wantInOrder(t, stdout, tc.wantOut...)
			var calls []string
			for _, r := range s.calls() {
				calls = append(calls, r.method+" "+r.uri)
			}
			if strings.Join(calls, "|") != strings.Join(tc.wantCalls, "|") {
				t.Errorf("requests = %q, want %q", calls, tc.wantCalls)
			}
		})
	}
}

// TestDraftsPublishPrintsTheOutcome pins what publish prints after the
// server answered: the snapshot, one line per server, the next steps and the
// way back, or a refusal's verdict lines and exit 1. A typed risk the reader
// cannot read in full is neither asked for nor typed, and the server's
// refusal decides.
func TestDraftsPublishPrintsTheOutcome(t *testing.T) {
	tests := []struct {
		name      string
		risks     string
		reply     draftReply
		wantOut   []string
		wantTyped map[string]any
		wantErr   string
		wantCode  int
	}{
		{
			name:  "a publish that went through",
			risks: fixTick,
			reply: draftReply{body: fixPublished},
			wantOut: []string{"Publish? [y/N] Published draft 41. The live policy snapshot is 3be0a1.\n" +
				"github is starting: strazactl apps show github\n" +
				"jira is failed: strazactl apps show jira\n  the start timed out after 20 seconds\n" +
				"old-tools is removed.\n" +
				"Assign github-readers or github-writers in your identity manager.\n" +
				"To undo it: strazactl drafts revert 41\n"},
			wantTyped: map[string]any{},
		},
		{
			name:  "a risk that came up meanwhile exits 1 with the new verdict",
			risks: fixTick,
			reply: draftReply{code: http.StatusConflict, body: `{"error":"Publish refused: Straza will send every caller's own token to api.githubcopilot.com, a host github has not used before. ` +
				`Acknowledge it, typing api.githubcopilot.com, and publish again.","verdict":` + fixVerdict("", fixTyped+","+fixTick) + `}`},
			wantOut:   []string{"Publish? [y/N]   widens    App/github Straza will send", "  widens    Role/developer"},
			wantTyped: map[string]any{},
			wantErr: "Publish refused: Straza will send every caller's own token to api.githubcopilot.com, a host github has not used before. " +
				"Acknowledge it, typing api.githubcopilot.com, and publish again.",
			wantCode: 1,
		},
		{
			name:      "a proxy's 504 may have landed",
			risks:     fixTick,
			reply:     draftReply{code: http.StatusGatewayTimeout, body: "<html>504 Gateway Time-out</html>"},
			wantOut:   []string{"Publish? [y/N] "},
			wantTyped: map[string]any{},
			wantErr: "strazad's answer to the publish of draft 41 was lost (a proxy answered HTTP 504), so it may have landed. " +
				"Read it with strazactl drafts show 41 before you publish again",
			wantCode: 2,
		},
		{
			name:  "a typed risk cut for the reader goes to the server",
			risks: fixCutTyped,
			reply: draftReply{code: http.StatusConflict, body: `{"error":"Publish refused: This line names a tool on a server you cannot read, so this view leaves its words out. ` +
				`Ask an administrator for the scope apps:read to see it. Acknowledge it and publish again."}`},
			wantOut:   []string{"It widens access:\n  widens    This line names a tool on a server you cannot read", "Publish? [y/N] "},
			wantTyped: map[string]any{},
			wantErr: "Publish refused: This line names a tool on a server you cannot read, so this view leaves its words out. " +
				"Ask an administrator for the scope apps:read to see it. Acknowledge it and publish again.",
			wantCode: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{
				"GET /v1/admin/drafts/41":          {body: fixDetail("open", "", tc.risks, true, "")},
				"POST /v1/admin/drafts/41/publish": tc.reply,
			})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "y\n", "drafts", "publish", "41")
			if code != tc.wantCode || errText(err) != tc.wantErr {
				t.Errorf("exit %d, err %v, want exit %d, err %q", code, err, tc.wantCode, tc.wantErr)
			}
			wantInOrder(t, stdout, tc.wantOut...)
			if typed := publishBody(t, s.requests())["typed"]; !reflect.DeepEqual(typed, tc.wantTyped) {
				t.Errorf("typed = %v, want %v", typed, tc.wantTyped)
			}
		})
	}
}

// TestDraftsPublishNeedsThePublishRoute pins a publish on a strazad that
// serves no publish route: it says so before any line or question, exits 2
// and posts nothing, whether it would ask or runs with --yes.
func TestDraftsPublishNeedsThePublishRoute(t *testing.T) {
	const want = "this strazad serves no publish route yet, so the draft stays open. " +
		"Upgrade strazad to a version with drafts publish, and publish again"
	for _, args := range [][]string{nil, {"--yes"}} {
		t.Run(strings.Join(append([]string{"publish"}, args...), " "), func(t *testing.T) {
			s, srv := newDraftsServer(t, map[string]draftReply{"GET /v1/admin/drafts/41": {body: fixDetail("open", "", fixTick, true, "")}})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, writeCreds(t, srv.URL), "y\n", append([]string{"drafts", "publish", "41"}, args...)...)
			if code != 2 || errText(err) != want || stdout != "" {
				t.Errorf("exit %d, err %v, stdout %q, want exit 2, err %q and nothing printed", code, err, stdout, want)
			}
			var calls []string
			for _, r := range s.calls() {
				calls = append(calls, r.method+" "+r.uri)
			}
			if got := strings.Join(calls, "|"); got != "GET /v1/admin/drafts/41|GET /v1/admin/drafts/41/publish" {
				t.Errorf("requests = %q, want the read and the check of the publish route", calls)
			}
		})
	}
}
