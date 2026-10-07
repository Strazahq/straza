package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var sentence = acceptSentence("1.2")

// eventRecordOf reads a records file written by the check.
func eventRecordOf(t *testing.T, f *fakeGitHub, path string) eventRecord {
	t.Helper()
	var rec eventRecord
	if err := json.Unmarshal(f.records[path], &rec); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return rec
}

// recordedBody returns the body of the comment object a record holds.
func recordedBody(t *testing.T, f *fakeGitHub, path string) string {
	t.Helper()
	c, err := parseComment(eventRecordOf(t, f, path).Comment, kindComment)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c.Body
}

func wantStatus(t *testing.T, f *fakeGitHub, state string) {
	t.Helper()
	if got := f.lastStatus(); got.State != state {
		t.Fatalf("status %q (%s), want %q", got.State, got.Description, state)
	}
}

func wantIn(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Fatalf("missing %q in:\n%s", p, text)
		}
	}
}

func mustRun(t *testing.T, f *fakeGitHub, eventName string, payload []byte) {
	t.Helper()
	if out, err := f.run(t, eventName, payload, nil); err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
}

func TestRun(t *testing.T) {
	cases := []struct {
		name  string
		steps func(t *testing.T, f *fakeGitHub)
	}{
		{"opener who has not accepted fails and the comment names the account", func(t *testing.T, f *fakeGitHub) {
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "@alice opened this pull request and has not accepted version 1.2 yet", "    "+sentence, testTextURL)
			if got := f.recordsUnder("v1.2/accounts/"); len(got) != 0 {
				t.Fatalf("account files written without an acceptance: %v", got)
			}
		}},
		{"acceptance records the account, confirms with a mention and turns green", func(t *testing.T, f *fakeGitHub) {
			id := f.addComment(alice, sentence+"\n")
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", id, ""))
			wantStatus(t, f, "success")
			n := itoa(id)
			if got, want := f.recordsUnder(prPath), []string{n + ".accepted.json", n + ".confirmed.json"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("events %v, want %v", got, want)
			}
			var acct accountFile
			if err := json.Unmarshal(f.records["v1.2/accounts/1001.json"], &acct); err != nil {
				t.Fatal(err)
			}
			want := accountFile{ID: 1001, Login: "alice", Version: "1.2", TextSHA256: f.textSHA(), TextURL: testTextURL,
				FirstAcceptance: firstAcceptance{Repo: "strazahq/straza", PR: 123, Kind: kindComment, CommentID: id, CreatedAt: "2026-10-05T12:31:00Z", AcceptedAt: "2026-10-05T12:31:00Z"},
				RecordedAt:      "2026-10-05T13:00:00Z", Basis: "comment"}
			if acct != want {
				t.Fatalf("account file %+v, want %+v", acct, want)
			}
			acc := eventRecordOf(t, f, prPath+n+".accepted.json")
			wantIn(t, string(acc.Comment), `"node_id"`, `"author_association"`, `"reactions"`, sentence)
			if f.confirmations(itoa(id)) != 1 {
				t.Fatalf("%d confirmations, want 1", f.confirmations(itoa(id)))
			}
			conf := f.comments()[len(f.comments())-2]["body"].(string)
			wantIn(t, conf, "@alice Thank you. SynapTech s. r. o. confirms in writing that on 2026-10-05 you concluded",
				"version 1.2", "#issuecomment-"+n, testTextURL, f.textSHA(), "section 65(4)")
			if body := recordedBody(t, f, prPath+n+".confirmed.json"); body != conf {
				t.Fatalf("confirmed record holds %q, want the confirmation", body)
			}
		}},
		{"new commit on an accepted pull request stays green and writes nothing", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			f.setHead(headB)
			mark := len(f.log)
			mustRun(t, f, "pull_request_target", f.prEvent(t, "synchronize"))
			if got := f.lastStatus(); got.State != "success" || got.SHA != headB {
				t.Fatalf("status %+v, want success on the new head", got)
			}
			if w := f.writesSince(mark); len(w) != 0 {
				t.Fatalf("second run wrote %v", w)
			}
		}},
		{"burst of events records and confirms each acceptance once", func(t *testing.T, f *fakeGitHub) {
			f.commits = []commit{commitOf(headA, by(alice), by(bob))}
			a := f.addComment(alice, sentence)
			b := f.addComment(bob, sentence)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", a, ""))
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", b, ""))
			mustRun(t, f, "pull_request_target", f.prEvent(t, "synchronize"))
			wantStatus(t, f, "success")
			if f.confirmations(itoa(a)) != 1 || f.confirmations(itoa(b)) != 1 {
				t.Fatalf("confirmations %d and %d, want 1 each", f.confirmations(itoa(a)), f.confirmations(itoa(b)))
			}
			if got := len(f.recordsUnder(prPath)); got != 4 {
				t.Fatalf("%d event files, want 4: %v", got, f.recordsUnder(prPath))
			}
		}},
		{"run that died after posting a confirmation is repaired without a second one", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			f.failPut[itoa(a)+".confirmed.json"] = 502
			if _, err := f.run(t, "issue_comment", f.commentEvent(t, "created", a, ""), nil); err == nil {
				t.Fatal("run with a failed write succeeded")
			}
			wantStatus(t, f, "failure")
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "success")
			if f.confirmations(itoa(a)) != 1 {
				t.Fatalf("%d confirmations, want 1", f.confirmations(itoa(a)))
			}
			wantIn(t, recordedBody(t, f, prPath+itoa(a)+".confirmed.json"), confirmMarker(itoa(a)))
		}},
		{"acceptance posted while the records token was missing is recorded on the next run", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			_, err := f.run(t, "issue_comment", f.commentEvent(t, "created", a, ""), func(c *config) { c.RecordsToken = "" })
			if err == nil || !strings.Contains(err.Error(), "records token is missing") {
				t.Fatalf("err = %v", err)
			}
			wantStatus(t, f, "failure")
			if len(f.recordsUnder("v1.2/accounts/")) != 0 || f.confirmations(itoa(a)) != 0 {
				t.Fatal("recorded or confirmed without the records token")
			}
			mustRun(t, f, "pull_request_target", f.prEvent(t, "synchronize"))
			wantStatus(t, f, "success")
			if f.confirmations(itoa(a)) != 1 || f.records["v1.2/accounts/1001.json"] == nil {
				t.Fatal("the later run did not record and confirm the acceptance")
			}
		}},
		{"manual re-run of a complete pull request adds nothing", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", a, ""))
			mark := len(f.log)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "success")
			if w := f.writesSince(mark); len(w) != 0 {
				t.Fatalf("re-run wrote %v", w)
			}
		}},
		{"commit email without an account fails and names the commit, whatever the name", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.commits = append(f.commits, commitOf(headB, unlinked("sample-maintainer", "sample-maintainer@example.org")))
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "Commit beef000 names `sample-maintainer <sample-maintainer@example.org>`, an email that is not added to any GitHub account")
		}},
		{"borrowed email fails until that account accepts on this pull request", func(t *testing.T, f *fakeGitHub) {
			f.setOpener(bob)
			f.addAccount(bob)
			f.addAccount(carol)
			f.commits = []commit{commitOf(headA, by(carol))}
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "@carol is named in commit c0ffee0 and has not posted the sentence on this pull request")
			c := f.addComment(carol, sentence)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", c, ""))
			wantStatus(t, f, "success")
			if string(f.records["v1.2/accounts/1003.json"]) != `{"id": 1003, "login": "carol", "basis": "comment"}` {
				t.Fatal("the earlier account file was rewritten")
			}
		}},
		{"opener who has not accepted fails while the commits' author has", func(t *testing.T, f *fakeGitHub) {
			f.setOpener(carol)
			f.commits = []commit{commitOf(headA, by(bob))}
			f.addComment(bob, sentence)
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "@carol opened this pull request and has not accepted", "@bob is named in commit c0ffee0 and accepted version 1.2 on this pull request")
			c := f.addComment(carol, sentence)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", c, ""))
			wantStatus(t, f, "success")
		}},
		{"AI co-author passes and the comment lists the tool", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "success")
			wantIn(t, f.statusComment(), "#### AI tools", "- `noreply@anthropic.com` in commit c0ffee0.")
		}},
		{"co-author who has not accepted fails until they post the sentence", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.commits = []commit{commitOf(headA, by(alice), by(bob))}
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "@bob is named in commit c0ffee0 and has not posted the sentence")
			b := f.addComment(bob, sentence)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", b, ""))
			wantStatus(t, f, "success")
		}},
		{"owner's own pull request passes through the allowlist", func(t *testing.T, f *fakeGitHub) {
			f.setOpener(maintainer)
			f.commits = []commit{commitOf(headA, by(maintainer))}
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "success")
			wantIn(t, f.statusComment(), "@sample-maintainer opened this pull request and is on the check's list of project accounts")
			if got := f.recordsUnder("v1.2/accounts/"); len(got) != 0 {
				t.Fatalf("records written for the owner: %v", got)
			}
		}},
		{"release drop naming an accepted co-author passes, and one who has not accepted fails", func(t *testing.T, f *fakeGitHub) {
			f.setOpener(maintainer)
			f.commits = []commit{commitOf(headA, by(maintainer), by(carol)), commitOf(headB, by(maintainer), by(bob))}
			f.addAccount(carol)
			mustRun(t, f, "pull_request_target", f.prEvent(t, "opened"))
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "@carol is named as a co-author in commit c0ffee0, which the maintainer carried over",
				"@bob is named as a co-author in commit beef000 and has not accepted version 1.2")
			f.addAccount(bob)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "success")
		}},
		{"closed pull request with an acceptance is locked once", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", a, ""))
			f.pr["state"] = "closed"
			mustRun(t, f, "pull_request_target", f.prEvent(t, "closed"))
			if f.pr["locked"] != true {
				t.Fatal("the closed pull request was not locked")
			}
			mark := len(f.log)
			mustRun(t, f, "workflow_dispatch", nil)
			if w := f.writesSince(mark); len(w) != 0 {
				t.Fatalf("a locked pull request was changed again: %v", w)
			}
		}},
		{"closed pull request without an acceptance is not locked", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.pr["state"] = "closed"
			mustRun(t, f, "pull_request_target", f.prEvent(t, "closed"))
			if f.pr["locked"] == true {
				t.Fatal("a pull request without an acceptance was locked")
			}
		}},
		{"first request of a pull request event sets the status to pending", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.pr["body"] = "Part of this is Not a Contribution."
			mustRun(t, f, "pull_request_target", f.prEvent(t, "edited"))
			if f.log[0] != "GET /repos/strazahq/straza/commits/"+headA+"/statuses" || f.log[1] != "POST /repos/strazahq/straza/statuses/"+headA || f.statuses[0].State != "pending" {
				t.Fatalf("first requests %q with status %+v, want the count and the pending status", f.log[:2], f.statuses[0])
			}
			wantStatus(t, f, "failure")
			wantIn(t, f.lastStatus().Description, "Not a Contribution")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			tc.steps(t, f)
			for _, s := range f.statuses {
				if s.SHA == "" || s.Description == "" {
					t.Fatalf("status without a commit or a description: %+v", s)
				}
			}
		})
	}
}
