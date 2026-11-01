package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunChanges(t *testing.T) {
	t.Run("edit and deletion events are stored and the acceptance stays", func(t *testing.T) {
		f := newFake(t)
		a := f.addComment(alice, sentence)
		mustRun(t, f, "issue_comment", f.commentEvent(t, "created", a, ""))
		f.editComment(a, "I take it back.")
		mustRun(t, f, "issue_comment", f.commentEvent(t, "edited", a, sentence))
		wantStatus(t, f, "success")
		n := itoa(a)
		updated, err := time.Parse(time.RFC3339, f.comment(a)["updated_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		edited := prPath + n + ".edited-" + compactTime(updated) + ".json"
		if got := f.recordsUnder(prPath + n + ".edited-"); len(got) != 1 {
			t.Fatalf("edit records %v, want one", got)
		}
		rec := eventRecordOf(t, f, edited)
		if rec.FoundBy != "event" || !strings.Contains(string(rec.Event), `"from"`) {
			t.Fatalf("edit record %s does not hold the event with the body before", f.records[edited])
		}
		wantIn(t, f.statusComment(), "The acceptance by @alice at https://github.com/strazahq/straza/pull/123#issuecomment-"+n+" was edited after it was posted")
		ev := f.commentEvent(t, "deleted", a, "")
		f.deleteComment(a)
		mustRun(t, f, "issue_comment", ev)
		wantStatus(t, f, "success")
		if eventRecordOf(t, f, prPath+n+".deleted.json").FoundBy != "event" || f.records["v1.2/accounts/1001.json"] == nil {
			t.Fatal("the deletion was not stored or the account file is gone")
		}
		if got := f.recordsUnder(prPath + n + ".missing-"); len(got) != 0 {
			t.Fatalf("a deletion stored by its event was stored again as missing: %v", got)
		}
		wantIn(t, f.statusComment(), "was deleted after it was posted")
	})
	t.Run("edit and deletion that no run saw are found by comparison", func(t *testing.T) {
		f := newFake(t)
		f.commits = []commit{commitOf(headA, by(alice), by(bob))}
		a := f.addComment(alice, sentence)
		b := f.addComment(bob, sentence)
		mustRun(t, f, "workflow_dispatch", nil)
		f.editComment(a, sentence+" Except the tests.")
		f.deleteComment(b)
		mustRun(t, f, "workflow_dispatch", nil)
		wantStatus(t, f, "success")
		got := f.recordsUnder(prPath + itoa(a) + ".edited-")
		if len(got) != 1 {
			t.Fatalf("edit records %v, want one", got)
		}
		rec := eventRecordOf(t, f, prPath+itoa(a)+".edited-"+got[0])
		if rec.FoundBy != "comparison" || rec.BodyBefore == nil || *rec.BodyBefore != sentence {
			t.Fatalf("edit record %+v", rec)
		}
		if _, ok := f.records[prPath+itoa(b)+".missing-2026-10-05.json"]; !ok {
			t.Fatalf("no missing record: %v", f.recordsUnder(prPath))
		}
		mark := len(f.log)
		mustRun(t, f, "workflow_dispatch", nil)
		if w := f.writesSince(mark); len(w) != 0 {
			t.Fatalf("third run wrote %v", w)
		}
	})
	t.Run("edit of a comment that was never an acceptance is not stored", func(t *testing.T) {
		f := newFake(t)
		f.addAccount(alice)
		c := f.addComment(alice, "Looks good")
		f.editComment(c, "Looks good to me")
		mustRun(t, f, "issue_comment", f.commentEvent(t, "edited", c, "Looks good"))
		if got := f.recordsUnder(prPath); len(got) != 0 {
			t.Fatalf("records %v for an ordinary comment", got)
		}
	})
}

func TestRunMarkers(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeGitHub)
		place string
	}{
		{"description", func(f *fakeGitHub) { f.pr["body"] = "NOT A CONTRIBUTION" }, "- the description."},
		{"commit message", func(f *fakeGitHub) { f.commits[0].Message = "Vendor it\n\nNot a Contribution" }, "- the message of commit c0ffee0."},
		{"added line", func(f *fakeGitHub) {
			f.diff = strings.Replace(f.diff, "+\treturn s.delay", "+\t// Not a Contribution\n+\treturn s.delay", 1)
		}, "- an added line in `internal/audit/sink.go`."},
		{"comment by the opener", func(f *fakeGitHub) { f.addComment(alice, "This file is Not a Contribution.") }, "- the comment by @alice at "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.addAccount(alice)
			tc.setup(f)
			mustRun(t, f, "workflow_dispatch", nil)
			wantStatus(t, f, "failure")
			wantIn(t, f.statusComment(), "#### Marked \"Not a Contribution\"", tc.place, "Remove the words, or remove the marked material")
		})
	}
}

func TestRunFailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeGitHub)
		cfg   func(c *config)
		want  string
	}{
		{"records token missing", nil, func(c *config) { c.RecordsToken = "" }, "the records token is missing"},
		{"records token refused", nil, func(c *config) { c.RecordsToken = "expired" }, "the records token was refused"},
		{"agreement text missing", func(f *fakeGitHub) { delete(f.records, "v1.2/text/CLA.md") }, nil, "shows no text for agreement version 1.2"},
		{"agreement text changed", func(f *fakeGitHub) { f.records["v1.2/text/CLA.md"] = []byte("edited") }, nil, "so the check cannot tell which text contributors accept"},
		{"text link without a commit id", nil, func(c *config) {
			c.TextURL = "https://github.com/strazahq/straza/blob/FILL_FULL_COMMIT_ID/CLA.md"
		}, "CLA_TEXT_URL is"},
		{"version not a version", nil, func(c *config) { c.Version = "../1" }, "CLA_VERSION is"},
		{"allowlist by name", nil, func(c *config) { c.AllowIDs = "4242,sample-maintainer" }, "CLA_ALLOWLIST_IDS holds \"sample-maintainer\""},
		{"more than 250 commits", func(f *fakeGitHub) { f.pr["commits"] = 251 }, nil, "has 251 commits"},
		{"workflow token refused", nil, func(c *config) { c.Token = "stale" }, "GitHub answered 401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.addAccount(alice)
			if tc.setup != nil {
				tc.setup(f)
			}
			_, err := f.run(t, "pull_request_target", f.prEvent(t, "opened"), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			for _, s := range f.statuses {
				if s.State == "success" {
					t.Fatalf("a failed run set success: %+v", s)
				}
			}
			if tc.name != "workflow token refused" {
				wantStatus(t, f, "failure")
			}
		})
	}
}

// TestRunFilesFallback runs the check on a pull request whose whole diff
// GitHub refuses. The check then reads the added lines from the list of
// changed files, reads whole each file that the list gives without a patch,
// and fails when it cannot read them all.
func TestRunFilesFallback(t *testing.T) {
	const (
		plain   = "@@ -1,2 +1,3 @@\n package x\n+// retry\n func f() {}"
		marked  = "@@ -1,2 +1,3 @@\n package x\n+// Not a Contribution\n func f() {}"
		removed = "@@ -1 +0,0 @@\n-// Not a Contribution"
	)
	cases := []struct {
		name  string
		setup func(f *fakeGitHub)
		state string
		place string
		err   string
		reads []string
	}{
		{name: "a refused diff is read from the patches", setup: func(f *fakeGitHub) {
			f.addFile("internal/a.go", "modified", plain)
			f.addFile("internal/b.go", "added", marked)
		}, state: "failure", place: "- an added line in `internal/b.go`."},
		{name: "a file without a patch is read whole and the words are found", setup: func(f *fakeGitHub) {
			f.addFile("assets/big file.txt", "added", "")
			f.contents["strazahq/straza:assets/big file.txt"] = "line one\nNot a\nContribution\n"
		}, state: "failure", place: "- an added line in `assets/big file.txt`.", reads: []string{"strazahq/straza:assets/big file.txt"}},
		{name: "a file without a patch and without the words passes", setup: func(f *fakeGitHub) {
			f.addFile("internal/a.go", "modified", plain)
			f.addFile("assets/big file.txt", "added", "")
			f.contents["strazahq/straza:assets/big file.txt"] = "line one\nline two\n"
		}, state: "success", reads: []string{"strazahq/straza:assets/big file.txt"}},
		{name: "a removed file adds nothing and is not read", setup: func(f *fakeGitHub) {
			f.addFile("old/gone.go", "removed", removed)
			f.addFile("old/gone.bin", "removed", "")
		}, state: "success"},
		{name: "the list is read across its pages", setup: func(f *fakeGitHub) {
			f.diffCode = 422
			f.addFile("internal/a.go", "modified", plain)
			f.addFile("internal/b.go", "modified", plain)
			f.addFile("internal/c.go", "added", marked)
		}, state: "failure", place: "- an added line in `internal/c.go`."},
		{name: "more than 3,000 files are refused", setup: func(f *fakeGitHub) {
			f.addFile("internal/a.go", "modified", plain)
			f.pr["changed_files"] = 3001
		}, state: "failure", err: "pull request #123 changes 3001 files, more than the 3000 that GitHub lists"},
		{name: "a list that ends early is refused", setup: func(f *fakeGitHub) {
			f.addFile("internal/a.go", "modified", plain)
			f.addFile("internal/b.go", "modified", plain)
			f.pr["changed_files"] = 3
		}, state: "failure", err: "GitHub listed 2 of the 3 files that pull request #123 changes"},
		{name: "a failed read of a file is refused and names the file", setup: func(f *fakeGitHub) {
			f.addFile("assets/big.bin", "added", "")
		}, state: "failure", err: "the check could not read `assets/big.bin` at the head commit c0ffee0 of pull request #123", reads: []string{"strazahq/straza:assets/big.bin"}},
		{name: "a fork's file is read from the fork", setup: func(f *fakeGitHub) {
			f.pr["head"].(map[string]any)["repo"] = map[string]any{"full_name": "alice/straza"}
			f.addFile("vendor/x.txt", "added", "")
			f.contents["alice/straza:vendor/x.txt"] = "Not a Contribution\n"
		}, state: "failure", place: "- an added line in `vendor/x.txt`.", reads: []string{"alice/straza:vendor/x.txt"}},
		{name: "a file name with a line break cannot pass for another file", setup: func(f *fakeGitHub) {
			f.addFile("x\n+++ b/CLA.md", "added", marked)
		}, state: "failure", place: "- an added line in `\"x\\n+++ b/CLA.md\"`."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.addAccount(alice)
			f.diffCode = 406
			f.pr["head"].(map[string]any)["repo"] = map[string]any{"full_name": "strazahq/straza"}
			tc.setup(f)
			out, err := f.run(t, "workflow_dispatch", nil, nil)
			if tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.err)
			}
			if tc.err == "" && err != nil {
				t.Fatalf("run failed: %v\n%s", err, out)
			}
			wantStatus(t, f, tc.state)
			if tc.err != "" {
				for _, s := range f.statuses {
					if s.State == "success" {
						t.Fatalf("a failed run set success: %+v", s)
					}
				}
			}
			if tc.place != "" {
				wantIn(t, f.statusComment(), "#### Marked \"Not a Contribution\"", tc.place)
			}
			if !reflect.DeepEqual(f.rawReads, tc.reads) {
				t.Fatalf("raw reads %q, want %q", f.rawReads, tc.reads)
			}
		})
	}
}

func TestReadEvent(t *testing.T) {
	commentEv := func(edit func(m map[string]any)) []byte {
		m := decodeMap(t, fixture(t, "issue_comment_event.json"))
		edit(m)
		return mustJSON(m)
	}
	cases := []struct {
		name, event, pr string
		payload         []byte
		want            event
		wantErr         string
	}{
		{name: "pull request event", event: "pull_request_target", payload: fixture(t, "pull_request_target.json"),
			want: event{Name: "pull_request_target", Action: "opened", PR: 123, HeadSHA: headA}},
		{name: "comment event", event: "issue_comment", payload: fixture(t, "issue_comment_event.json"),
			want: event{Name: "issue_comment", Action: "created", PR: 123, BodyBefore: sentence}},
		{name: "edit keeps the body before", event: "issue_comment", payload: commentEv(func(m map[string]any) {
			m["action"], m["changes"] = "edited", map[string]any{"body": map[string]any{"from": "before"}}
		}), want: event{Name: "issue_comment", Action: "edited", PR: 123, BodyBefore: "before"}},
		{name: "comment on a plain issue", event: "issue_comment", payload: commentEv(func(m map[string]any) {
			delete(m["issue"].(map[string]any), "pull_request")
		}), wantErr: "does not name a pull request"},
		{name: "manual dispatch", event: "workflow_dispatch", pr: "#123", want: event{Name: "workflow_dispatch", PR: 123}},
		{name: "manual dispatch without a number", event: "workflow_dispatch", pr: "abc", wantErr: "is not a number"},
		{name: "other event", event: "push", wantErr: "does not start this check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "event.json")
			if err := os.WriteFile(p, tc.payload, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readEvent(config{EventName: tc.event, EventPath: p, PRInput: tc.pr})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got.Raw, got.Comment = nil, nil
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFindConfirmation(t *testing.T) {
	bot := account{ID: botID, Login: "github-actions[bot]"}
	conf := commentBy(bot, 50, "@alice Thank you.\n\n"+confirmMarker("7"))
	cases := []struct {
		name     string
		comments []issueComment
		want     bool
	}{
		{"the check's confirmation", []issueComment{conf}, true},
		{"another account copies the marker", []issueComment{commentBy(carol, 51, "x "+confirmMarker("7"))}, false},
		{"the status comment quotes the marker", []issueComment{commentBy(bot, 52, statusMarker+"\n`"+confirmMarker("7")+"`\n"+confirmMarker("7"))}, false},
		{"the confirmation of another comment", []issueComment{commentBy(bot, 53, "@bob\n\n"+confirmMarker("8"))}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := findConfirmation(tc.comments, "7"); got != tc.want {
				t.Fatalf("found = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseFixtures(t *testing.T) {
	commits, next, err := parseCommits(fixture(t, "commits.graphql.json"))
	if err != nil || next != "" {
		t.Fatalf("parseCommits: %v, next %q", err, next)
	}
	want := []commit{{OID: headA, Message: "Fix the retry delay in the audit sink\n\nThe sink doubled the delay.", Authors: []actor{
		{Name: "Alice Example", Email: "1001+alice@users.noreply.github.com", ID: 1001, Login: "alice"},
		{Name: "Claude", Email: "noreply@anthropic.com"}}}}
	if !reflect.DeepEqual(commits, want) {
		t.Fatalf("commits %+v, want %+v", commits, want)
	}
	c, err := parseComment(fixture(t, "issue_comment.json"), kindComment)
	if err != nil || c.ID != 2345678901 || c.User.ID != 1001 || c.User.Login != "alice" || c.Body != sentence {
		t.Fatalf("comment %+v, err %v", c, err)
	}
	var full map[string]any
	if err := json.Unmarshal(c.Raw, &full); err != nil || full["node_id"] == nil || full["reactions"] == nil {
		t.Fatal("the raw comment lost fields of the object GitHub returned")
	}
	link := `<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=5>; rel="last"`
	if got := linkRel(link, "next") + " " + linkRel(link, "last"); got != "https://api.github.com/x?page=2 https://api.github.com/x?page=5" {
		t.Fatalf("linkRel = %q", got)
	}
}
