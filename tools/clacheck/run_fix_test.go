package main

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// accountOf reads an account file the check wrote.
func accountOf(t *testing.T, f *fakeGitHub, a account) accountFile {
	t.Helper()
	var acct accountFile
	if err := json.Unmarshal(f.records["v1.2/accounts/"+itoa(a.ID)+".json"], &acct); err != nil {
		t.Fatalf("account file of %s: %v", a.Login, err)
	}
	return acct
}

func TestRunFixes(t *testing.T) {
	unfinished := strings.TrimSuffix(sentence, ".")
	cases := []struct {
		name  string
		steps func(t *testing.T, f *fakeGitHub)
	}{
		{"spoofed maintainer commit on a contributor's pull request gets no carry-over", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.addAccount(carol)
			f.commits = []commit{commitOf(headA, by(alice)), commitOf(headB, by(maintainer), by(carol))}
			f.addComment(maintainer, sentence)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "@carol is named in commit beef000 and has not posted the sentence on this pull request")
		}},
		{"edit that makes a comment the sentence is an acceptance dated by the edit", func(t *testing.T, f *fakeGitHub) {
			c := f.addComment(alice, unfinished)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "created", c, ""))
			wantStatus(t, f, "failure")
			f.clock = f.clock.Add(72 * time.Hour)
			f.editComment(c, sentence)
			mustRun(t, f, "issue_comment", f.commentEvent(t, "edited", c, unfinished))
			wantStatus(t, f, "success")
			n := itoa(c)
			if got, want := f.recordsUnder(prPath), []string{n + ".accepted.json", n + ".confirmed.json"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("events %v, want %v", got, want)
			}
			if got := accountOf(t, f, alice).FirstAcceptance.AcceptedAt; got != "2026-10-08T12:33:00Z" {
				t.Fatalf("accepted_at %q, want the edit's time", got)
			}
			wantIn(t, f.confirmation(n), "that on 2026-10-08 you concluded")
			if strings.Contains(f.statusComment(), "Acceptances changed") {
				t.Fatalf("the edit that made the acceptance is listed as a change:\n%s", f.statusComment())
			}
		}},
		{"acceptance edited away before any run is recorded from the edit history", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			f.editComment(a, "Actually, never mind.")
			mustRun(t, f, "issue_comment", f.commentEvent(t, "edited", a, sentence))
			wantStatus(t, f, "success")
			n := itoa(a)
			rec := eventRecordOf(t, f, prPath+n+".accepted.json")
			if rec.AcceptedAt != "2026-10-05T12:31:00Z" || rec.AcceptedBody != sentence || len(rec.History) == 0 {
				t.Fatalf("acceptance record %+v", rec)
			}
			wantIn(t, f.confirmation(n), "that on 2026-10-05 you concluded")
			if got := f.recordsUnder(prPath + n + ".edited-"); len(got) != 1 {
				t.Fatalf("edit records %v, want one", got)
			}
			wantIn(t, f.statusComment(), "was edited after it was posted")
		}},
		{"acceptance deleted before any run is recorded from the deletion event", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			ev := f.commentEvent(t, "deleted", a, "")
			f.deleteComment(a)
			mustRun(t, f, "issue_comment", ev)
			wantStatus(t, f, "success")
			n := itoa(a)
			if eventRecordOf(t, f, prPath+n+".accepted.json").FoundBy != "event" || f.records[prPath+n+".deleted.json"] == nil {
				t.Fatalf("records %v", f.recordsUnder(prPath))
			}
			if f.confirmations(n) != 1 {
				t.Fatalf("%d confirmations, want 1", f.confirmations(n))
			}
		}},
		{"acceptance deleted before any run, with no event in hand, is lost: the stated limit", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			f.deleteComment(a)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "failure")
			if got := f.recordsUnder("v1.2/accounts/"); len(got) != 0 {
				t.Fatalf("account files %v", got)
			}
		}},
		{"acceptance posted as a review comment is recorded and confirmed", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.commits = []commit{commitOf(headA, by(alice), by(bob))}
			id := f.addReviewComment(bob, sentence)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "success")
			key := "review-comment-" + itoa(id)
			if f.records[prPath+key+".accepted.json"] == nil || f.confirmations(key) != 1 {
				t.Fatalf("records %v, %d confirmations", f.recordsUnder(prPath), f.confirmations(key))
			}
			wantIn(t, f.confirmation(key), "#discussion_r"+itoa(id))
		}},
		{"acceptance posted as a review body is recorded and confirmed", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.commits = []commit{commitOf(headA, by(alice), by(bob))}
			id := f.addReview(bob, sentence)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "success")
			if f.confirmations("review-"+itoa(id)) != 1 {
				t.Fatalf("records %v", f.recordsUnder(prPath))
			}
		}},
		{"marker in a review comment or a review fails the check", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			rc := f.addReviewComment(alice, "This file is Not a Contribution.")
			rv := f.addReview(alice, "Parts are not a contribution")
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "the review comment by @alice at https://github.com/strazahq/straza/pull/123#discussion_r"+itoa(rc),
				"the review by @alice at https://github.com/strazahq/straza/pull/123#pullrequestreview-"+itoa(rv))
		}},
		{"a change while the run reads leaves pending for the run the change started", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.during["POST /graphql"] = func() { f.pr["body"] = "All of it is Not a Contribution." }
			mustRun(t, f, "workflow_dispatch", nil)
			if got := f.lastStatus(); got.State != "pending" || !strings.Contains(got.Description, "changed while the check ran") {
				t.Fatalf("status %+v, want pending for the change", got)
			}
			mustRun(t, f, "pull_request_target", f.prEvent(t, "edited"))
			wantStatus(t, f, "failure")
		}},
		{"a comment posted while the run reads leaves pending", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.during["POST /graphql"] = func() { f.addLocked(kindComment, alice, "Not a Contribution, sorry.") }
			mustRun(t, f, "workflow_dispatch", nil)
			if got := f.lastStatus(); got.State != "pending" {
				t.Fatalf("status %+v, want pending", got)
			}
		}},
		{"the deleted account's comments are never an acceptance", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.addComment(ghost, sentence)
			mustRun(t, f, "workflow_dispatch", nil)
			if f.records["v1.2/accounts/10137.json"] != nil || len(f.recordsUnder(prPath)) != 0 {
				t.Fatalf("records for the deleted account: %v", f.recordsUnder(""))
			}
		}},
		{"authors past the first page of a commit are judged", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.commits = []commit{commitOf(headA, by(alice), by(bob), by(carol))}
			f.addComment(bob, sentence)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "@carol is named in commit c0ffee0")
		}},
		{"another open pull request with the same head refuses success for both", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			other := decodeMap(t, mustJSON(f.pr))
			other["number"], other["user"] = 124, map[string]any{"login": "carol", "id": carol.ID}
			f.openPRs = append(f.openPRs, other)
			_, err := f.run(t, "workflow_dispatch", nil, nil)
			if err == nil || !strings.Contains(err.Error(), "#124") {
				t.Fatalf("err = %v", err)
			}
			wantStatus(t, f, "failure")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			tc.steps(t, f)
		})
	}
}

func TestMarkPending(t *testing.T) {
	t.Run("comment event reads the head and sets pending, nothing else", func(t *testing.T) {
		f := newFake(t)
		id := f.addComment(alice, "hello")
		_, err := f.runMode(t, "issue_comment", f.commentEvent(t, "created", id, ""), func(c *config) { c.RecordsToken = "" }, true)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"GET /repos/strazahq/straza/pulls/123", "GET /repos/strazahq/straza/commits/" + headA + "/statuses", "POST /repos/strazahq/straza/statuses/" + headA}
		if !reflect.DeepEqual(f.log, want) || f.lastStatus().State != "pending" {
			t.Fatalf("requests %q, statuses %+v", f.log, f.statuses)
		}
	})
	t.Run("pull request event sets pending from the payload's head", func(t *testing.T) {
		f := newFake(t)
		if _, err := f.runMode(t, "pull_request_target", f.prEvent(t, "edited"), nil, true); err != nil {
			t.Fatal(err)
		}
		if want := []string{"GET /repos/strazahq/straza/commits/" + headA + "/statuses", "POST /repos/strazahq/straza/statuses/" + headA}; !reflect.DeepEqual(f.log, want) {
			t.Fatalf("requests %q", f.log)
		}
	})
}

func TestRunFixMessages(t *testing.T) {
	reset := strconv.FormatInt(time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC).Unix(), 10)
	cases := []struct {
		name    string
		setup   func(f *fakeGitHub)
		want    string
		notWant string
	}{
		{"rate limit on the records token says rate limit", func(f *fakeGitHub) {
			f.fail["GET /repos/strazahq/cla-records/contents/v1.2/text/CLA.md"] = fakeFailure{code: 403, message: "API rate limit exceeded for user ID 1.",
				headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}}
		}, "rate limit", "expired"},
		{"published text differs from the records copy", func(f *fakeGitHub) {
			f.published = strings.ReplaceAll(testText, "\n", "\r\n")
		}, "differs from the published text", ""},
		{"SHA256SUMS naming another path says which line it needs", func(f *fakeGitHub) {
			f.records["v1.2/text/SHA256SUMS"] = []byte(f.textSHA() + "  v1.2/text/CLA.md\n")
		}, "  CLA.md", ""},
		{"a server error on the pull request does not blame the number", func(f *fakeGitHub) {
			f.fail["GET /repos/strazahq/straza/pulls/123"] = fakeFailure{code: 502, message: "Server Error"}
		}, "once GitHub answers again", "Check that the number"},
		{"a missing pull request names the number", func(f *fakeGitHub) {
			f.fail["GET /repos/strazahq/straza/pulls/123"] = fakeFailure{code: 404, message: "Not Found"}
		}, "Check that the number", ""},
		{"a refused comment names the permission", func(f *fakeGitHub) {
			f.fail["POST /repos/strazahq/straza/issues/123/comments"] = fakeFailure{code: 403, message: "Resource not accessible by integration"}
		}, "pull-requests: write", ""},
		{"a server error on a comment does not blame the permission", func(f *fakeGitHub) {
			f.fail["POST /repos/strazahq/straza/issues/123/comments"] = fakeFailure{code: 500, message: "Server Error"}
		}, "once GitHub answers again", "permission"},
		{"the status limit of a commit has its own sentence", func(f *fakeGitHub) {
			f.fail["POST /repos/strazahq/straza/statuses/"+headA] = fakeFailure{code: 422, message: "This SHA and context has reached the maximum number of statuses."}
		}, "1000", "until its check is green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.addAccount(alice)
			tc.setup(f)
			_, err := f.run(t, "workflow_dispatch", nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || (tc.notWant != "" && strings.Contains(err.Error(), tc.notWant)) {
				t.Fatalf("err = %v\nwant %q and not %q", err, tc.want, tc.notWant)
			}
		})
	}
}
