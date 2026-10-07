package main

import (
	"strings"
	"testing"
)

func TestRunStatusLimit(t *testing.T) {
	cases := []struct {
		name       string
		base       int
		pending    bool
		wantStates []string
		wantErr    string
	}{
		{name: "from 900 statuses on a run writes one failure and nothing else", base: 950,
			wantStates: []string{"failure"}, wantErr: "limit of statuses"},
		{name: "the pending job at the limit writes one failure", base: 950, pending: true,
			wantStates: []string{"failure"}, wantErr: "limit of statuses"},
		{name: "a run that reaches 900 turns its verdict into the limit failure", base: 899,
			wantStates: []string{"pending", "failure"}, wantErr: "limit of statuses"},
		{name: "a commit already at 1000 keeps the failure the check wrote before", base: 1000,
			wantErr: "do not merge this commit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.addAccount(alice)
			f.statusBase[headA] = tc.base
			_, err := f.runMode(t, "pull_request_target", f.prEvent(t, "edited"), nil, tc.pending)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "Push a new commit") {
				t.Fatalf("err = %v, want %q and the next step", err, tc.wantErr)
			}
			var states []string
			for _, s := range f.statuses {
				states = append(states, s.State)
			}
			if strings.Join(states, ",") != strings.Join(tc.wantStates, ",") {
				t.Fatalf("statuses %v, want %v", states, tc.wantStates)
			}
			if len(f.statuses) > 0 && f.lastStatus().State == "failure" {
				wantIn(t, f.lastStatus().Description, "limit of statuses", "Push a new commit")
			}
		})
	}
}

// TestFailureWriteNeedsNoCount breaks the count of statuses after the first
// write, and a run that then fails must still write its failure.
func TestFailureWriteNeedsNoCount(t *testing.T) {
	f := newFake(t)
	f.during["POST /repos/strazahq/straza/statuses/"+headA] = func() {
		f.fail["GET /repos/strazahq/straza/commits/"+headA+"/statuses"] = fakeFailure{code: 502, message: "Server Error"}
	}
	_, err := f.run(t, "pull_request_target", f.prEvent(t, "opened"), func(c *config) { c.RecordsToken = "expired" })
	if err == nil {
		t.Fatal("a run with a refused records token passed")
	}
	if got := f.lastStatus(); got.State != "failure" {
		t.Fatalf("status %+v, want the failure written without a count", got)
	}
}

func TestRunFixes2(t *testing.T) {
	unfinished := strings.TrimSuffix(sentence, ".")
	cases := []struct {
		name  string
		steps func(t *testing.T, f *fakeGitHub)
	}{
		{"an edit by another account never makes an acceptance", func(t *testing.T, f *fakeGitHub) {
			c := f.addComment(alice, unfinished)
			f.editCommentAs(c, sentence, maintainer)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "failure")
			if len(f.recordsUnder(prPath)) != 0 || f.records["v1.2/accounts/1001.json"] != nil {
				t.Fatalf("recorded an acceptance a maintainer wrote: %v", f.recordsUnder(""))
			}
		}},
		{"the edit history keeps who made each revision", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			f.editComment(a, "Changed.")
			mustRun(t, f, "workflow_dispatch", nil)
			rec := eventRecordOf(t, f, prPath+itoa(a)+".accepted.json")
			wantIn(t, string(rec.History), `"editor"`, `"databaseId": 1001`)
		}},
		{"an acceptance older than the first page of the edit history is found", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			f.editComment(a, "One.")
			f.editComment(a, "Two.")
			f.editComment(a, "Three.")
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "success")
			if rec := eventRecordOf(t, f, prPath+itoa(a)+".accepted.json"); rec.AcceptedAt != "2026-10-05T12:31:00Z" {
				t.Fatalf("accepted_at %q, want the first revision's time", rec.AcceptedAt)
			}
		}},
		{"an early edit is named as after posting, not after recording", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			f.editComment(a, "Changed.")
			mustRun(t, f, "workflow_dispatch", nil)
			if s := f.statusComment(); !strings.Contains(s, "was edited after it was posted") || strings.Contains(s, "after it was recorded") {
				t.Fatalf("status comment:\n%s", s)
			}
		}},
		{"a base branch change while the run reads leaves pending", func(t *testing.T, f *fakeGitHub) {
			f.addAccount(alice)
			f.during["POST /graphql"] = func() { f.pr["base"].(map[string]any)["ref"] = "release" }
			mustRun(t, f, "workflow_dispatch", nil)
			if got := f.lastStatus(); got.State != "pending" {
				t.Fatalf("status %+v, want pending", got)
			}
		}},
		{"an acceptance edited away and then deleted is dated by its posting", func(t *testing.T, f *fakeGitHub) {
			a := f.addComment(alice, sentence)
			f.editComment(a, "Never mind.")
			ev := f.commentEvent(t, "edited", a, sentence)
			f.deleteComment(a)
			mustRun(t, f, "issue_comment", ev)
			if rec := eventRecordOf(t, f, prPath+itoa(a)+".accepted.json"); rec.AcceptedAt != "2026-10-05T12:31:00Z" {
				t.Fatalf("accepted_at %q, want the posting time", rec.AcceptedAt)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			tc.steps(t, f)
		})
	}
}
