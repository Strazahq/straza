package main

import (
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// TestVerdictLineShowsBeforeAndAfter pins the words under a verdict line: a
// risk or a warning whose before and after words differ prints each side it
// has, indented under the line, and nothing else does.
func TestVerdictLineShowsBeforeAndAfter(t *testing.T) {
	const line = "  widens    App/wiki Publishing starts python3 wiki.py on the Straza host.\n"
	risk := func(before, after string) ctl.DraftFinding {
		return ctl.DraftFinding{Class: "risk", Ack: "typed", Object: "App/wiki", Typed: "wiki",
			Sentence: "Publishing starts python3 wiki.py on the Straza host.", Before: before, After: after}
	}
	tests := []struct {
		name string
		word string
		f    ctl.DraftFinding
		want string
	}{
		{"a risk whose words differ shows both", "widens", risk("command python3 wiki.py", "command python3 wiki.py, env WIKI_SPACE (value changed)"),
			line + "            before: command python3 wiki.py\n            after:  command python3 wiki.py, env WIKI_SPACE (value changed)\n"},
		{"a risk with after words alone shows them alone", "widens", risk("", "token api.githubcopilot.com"),
			line + "            after:  token api.githubcopilot.com\n"},
		{"a risk whose words are equal shows neither", "widens", risk("github-readers", "github-readers"), line},
		{"a warning shows them too", "warning",
			ctl.DraftFinding{Class: "warning", Object: "Role/readers", Sentence: "Holders of readers lose get_ticket on tickets.", Before: "get_ticket", After: ""},
			"  warning   Role/readers Holders of readers lose get_ticket on tickets.\n            before: get_ticket\n"},
		{"a refusal never shows them", "refused",
			ctl.DraftFinding{Class: "refused", Object: "App/wiki", Sentence: "App/wiki changed after this draft was checked.", Before: "a", After: "b"},
			"  refused   App/wiki changed after this draft was checked.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			printFindings(&b, tc.word, []ctl.DraftFinding{tc.f}, nil)
			if b.String() != tc.want {
				t.Errorf("printed %q, want %q", b.String(), tc.want)
			}
		})
	}
}

// TestOutcomeWords pins who gains what in the words the policy pages use,
// and an outcome a newer strazad may send as it is.
func TestOutcomeWords(t *testing.T) {
	for outcome, want := range map[string]string{
		"runs": "runs at once", "needs-approval": "needs approval", "not-reachable": "not reachable",
		"unknown": "unknown", "denied": "denied", "held-forever": "held-forever",
	} {
		if got := outcomeWords(outcome); got != want {
			t.Errorf("outcomeWords(%q) = %q, want %q", outcome, got, want)
		}
	}
}

// TestGainsNameTheHolders pins the HOLDERS cell: the holders by name when
// the server names them, which it does for root and for a reader of role
// membership, and how many there are for every other reader.
func TestGainsNameTheHolders(t *testing.T) {
	var b strings.Builder
	err := printGains(&b, []ctl.DraftGain{
		{Role: "readers", Server: "tickets", Tool: "get_ticket", Holders: []string{"alice", "dana"}, HoldersCount: 2, Before: "not-reachable", After: "runs"},
		{Role: "writers", Server: "tickets", Tool: "close_ticket", HoldersCount: 3, Before: "runs", After: "needs-approval",
			AfterWords: "a hold, up to 2 minutes, decided by sec-approvers"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(rows) != 3 {
		t.Fatalf("printed %d lines, want the header and two rows:\n%s", len(rows), b.String())
	}
	wantInOrder(t, rows[1], "readers", "tickets", "get_ticket", "not reachable", "runs at once", "alice, dana")
	wantInOrder(t, rows[2], "writers", "tickets", "close_ticket", "runs at once", "a hold, up to 2 minutes, decided by sec-approvers", "3")
}

// TestDoorWords pins a door in words: the list's DOOR cell, and the phrase
// a sentence names it with.
func TestDoorWords(t *testing.T) {
	tests := []struct {
		door, cell, phrase string
	}{
		{"console", "console", "the console"},
		{"strazactl", "strazactl", "strazactl"},
		{"apps-directory", "apps directory", "the apps directory"},
		{"straza-app", "straza app", "the straza app"},
		{"api", "API", "the API"},
	}
	for _, tc := range tests {
		t.Run(tc.door, func(t *testing.T) {
			if cell, phrase := doorWords(tc.door), doorPhrase(tc.door); cell != tc.cell || phrase != tc.phrase {
				t.Errorf("door %q reads %q and %q, want %q and %q", tc.door, cell, phrase, tc.cell, tc.phrase)
			}
		})
	}
}

// TestDraftsShowNamesEveryRevision pins the opening of show: one line per
// revision, each naming its author, the door it came through and when, so
// a change another person wrote is never read as the proposer's.
func TestDraftsShowNamesEveryRevision(t *testing.T) {
	revisions := `"revisions":[` +
		`{"revision":1,"author":{"user_id":"u9","username":"joe-java-developer-agent","agent":true,"via":"session","client":"claude-code","sponsor":"alice"},` +
		`"door":"straza-app","digest":"d1","created_at":"2026-09-24T10:14:03Z"},` +
		`{"revision":2,"author":{"user_id":"u3","username":"carol","agent":false,"via":"session","client":"strazactl"},` +
		`"door":"strazactl","digest":"d2","created_at":"2026-09-24T10:23:40Z"}],`
	detail := fixDetail("open", "", "", true, "")
	start, end := strings.Index(detail, `"revisions":[`), strings.Index(detail, `"live":`)
	_, srv := newDraftsServer(t, map[string]draftReply{"GET /v1/admin/drafts/41": {body: detail[:start] + revisions + detail[end:]}})
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, code, err := runDrafts(t, onToken(t, srv.URL), "", "drafts", "show", "41")
	if code != 0 {
		t.Fatalf("exit %d (%v), want 0", code, err)
	}
	want := "Revision 1 by joe-java-developer-agent (agent, sponsored by alice) through the straza app at 2026-09-24 10:14 UTC.\n" +
		"Revision 2 by carol through strazactl at 2026-09-24 10:23 UTC.\n" +
		"The note, which Straza did not check: The platform team asked for read access to GitHub.\n"
	if !strings.HasPrefix(stdout, want) {
		t.Errorf("stdout does not open with %q:\n%s", want, stdout)
	}
}

// TestDraftsShowNamesWhoDiscarded pins the line show prints under the
// revisions of a discarded draft: who discarded it, when, and the reason
// given as one sentence.
func TestDraftsShowNamesWhoDiscarded(t *testing.T) {
	const by = `"decided_at":"2026-09-24T11:00:05Z","decided_by":{"user_id":"u5","username":"erin","agent":false,"via":"session","client":"strazactl"},`
	tests := []struct {
		name, reason, want string
	}{
		{"a reason that ends its sentence", `"decided_reason":"Superseded by draft 42.",`, "Discarded by erin at 2026-09-24 11:00 UTC: Superseded by draft 42.\n"},
		{"a reason without a period", `"decided_reason":"superseded by draft 42",`, "Discarded by erin at 2026-09-24 11:00 UTC: superseded by draft 42.\n"},
		{"no reason", "", "Discarded by erin at 2026-09-24 11:00 UTC.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detail := strings.Replace(fixDetail("discarded", "", "", false, ""), `"created_at":"2026-09-24T10:14:03Z"`, by+tc.reason+`"created_at":"2026-09-24T10:14:03Z"`, 1)
			_, srv := newDraftsServer(t, map[string]draftReply{"GET /v1/admin/drafts/41": {body: detail}})
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, code, err := runDrafts(t, onToken(t, srv.URL), "", "drafts", "show", "41")
			if code != 0 {
				t.Fatalf("exit %d (%v), want 0", code, err)
			}
			wantInOrder(t, stdout, "Revision 1 by joe-java-developer-agent", "\n"+tc.want+"The note, which Straza did not check:")
		})
	}
}

// TestCoverageInTheServersWords pins the coverage list show prints: one
// table per server, headed in that server's own plural, each role's
// objects now and after publishing, every object in the server's type, a
// role that covers none as nothing, and the holders by name or counted.
// The object types are invented, so a noun strazactl hardcoded would show.
func TestCoverageInTheServersWords(t *testing.T) {
	tickets := []ctl.DraftObjectName{{ID: "t1", Value: "T-1"}, {ID: "t2", Value: "T-2"}}
	var b strings.Builder
	err := printCoverage(&b, []ctl.DraftCoverage{
		{Role: "desk-eu", Server: "helpdesk", Type: "ticket", Plural: "tickets", HoldersCount: 3, Before: tickets[:1], After: tickets},
		{Role: "board", Server: "tracker", Type: "lane", Plural: "lanes", Holders: []string{"alice", "dana"}, HoldersCount: 2, EveryAfter: true,
			Before: []ctl.DraftObjectName{{ID: "l1", Value: "intake"}}},
		{Role: "desk-new", Server: "helpdesk", Type: "ticket", Plural: "tickets", After: tickets[1:]},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(rows) != 7 {
		t.Fatalf("printed %d lines, want a heading, a header and the rows of each server:\n%s", len(rows), b.String())
	}
	if rows[0] != "Tickets on helpdesk, now and after publishing:" || rows[4] != "Lanes on tracker, now and after publishing:" {
		t.Errorf("headings = %q and %q, want each server's own plural", rows[0], rows[4])
	}
	wantInOrder(t, rows[1], "ROLE", "NOW", "AFTER", "HOLDERS")
	wantInOrder(t, rows[2], "desk-eu", "T-1", "T-1, T-2", "3")
	wantInOrder(t, rows[3], "desk-new", "nothing", "T-2", "0")
	wantInOrder(t, rows[6], "board", "intake", "every lane", "alice, dana")
}
