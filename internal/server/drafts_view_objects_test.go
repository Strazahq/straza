package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
)

// TestVerdictForCutsCoverage pins the read cut of the coverage list, the
// same cut the gain rows take: root reads every entry with its holders, a
// reader without identity:read counts the holders and reads no name, a
// server's admin reads the entries on that server alone, a reader who may
// not read a role loses that role's entries, an agent reads none, and a
// verdict with no entry leaves objects out, as the openapi allows. Every
// entry left out counts as a row in the one info line that says part of
// Access after publishing is hidden. The servers pin the invented type
// ticket, so a noun the cut wrote would show.
func TestVerdictForCutsCoverage(t *testing.T) {
	t.Parallel()
	tickets := func(role, server string, holders ...string) drafts.Coverage {
		return drafts.Coverage{Role: role, Server: server, Type: "ticket", Plural: "tickets", Holders: holders, HolderCount: len(holders),
			Before: []drafts.ObjectName{{ID: "7", Value: "T-7"}}, After: []drafts.ObjectName{{ID: "7", Value: "T-7"}, {ID: "9", Value: "T-9"}}}
	}
	desk, triage, onCall := tickets("helpdesk-agents", "helpdesk", "bob", "carol"), tickets("tracker-triage", "tracker", "alice"), tickets("on-call", "tracker", "dave")
	w := drafts.World{Apps: map[string]drafts.App{"tracker": {Name: "tracker"}, "helpdesk": {Name: "helpdesk"}},
		Roles: map[string]drafts.Role{
			"helpdesk-agents": {ID: "r1", Name: "helpdesk-agents", Kind: drafts.RoleKindApplication, Owner: "helpdesk", Owned: true},
			"tracker-triage":  {ID: "r2", Name: "tracker-triage", Kind: drafts.RoleKindApplication, Owner: "tracker", Owned: true},
			"on-call":         {ID: "r3", Name: "on-call", Kind: drafts.RoleKindApplication},
		}}
	d := drafts.Draft{ID: "52", Revision: 1, Items: []drafts.Item{
		{Kind: drafts.KindRole, Name: "helpdesk-agents", Op: drafts.OpPut}, {Kind: drafts.KindRole, Name: "tracker-triage", Op: drafts.OpPut},
		{Kind: drafts.KindRole, Name: "on-call", Op: drafts.OpPut}}}
	v := drafts.Verdict{Draft: "52", Revision: 1, Objects: []drafts.Coverage{desk, triage, onCall}}
	unnamed := func(cs ...drafts.Coverage) []drafts.Coverage {
		out := []drafts.Coverage{}
		for _, c := range cs {
			c.Holders = nil
			out = append(out, c)
		}
		return out
	}
	const (
		oneServer = "1 row under Access after publishing is on a server you cannot read, so this view leaves it out. Ask an administrator for the scope apps:read to see it."
		oneRole   = "1 row under Access after publishing names a role or a server you cannot read, so this view leaves it out. Ask an administrator for the grant that reads it."
		twoRoles  = "2 rows under Access after publishing name roles or servers you cannot read, so this view leaves them out. Ask an administrator for the grants that read them."
	)
	cases := []struct {
		name string
		c    draftCaller
		want []drafts.Coverage
		note string
	}{
		{"root reads every entry and the holders", rootCaller("kim"), []drafts.Coverage{desk, triage, onCall}, ""},
		{"apps:read and identity:read read every entry and the holders", adminAPICaller("ci", "drafts:read", "apps:read", "identity:read"), []drafts.Coverage{desk, triage, onCall}, ""},
		{"apps:read without identity:read counts the holders", adminAPICaller("ci", "drafts:read", "apps:read"), unnamed(desk, triage, onCall), ""},
		{"tracker's server admin with identity:read reads tracker's entries", administering(personCaller("erin", "identity:read"), map[string]string{"a1": "tracker"}), []drafts.Coverage{triage, onCall}, oneRole},
		{"tracker's server admin reads tracker's entries of the roles it may read", administering(personCaller("erin"), map[string]string{"a1": "tracker"}), unnamed(triage), twoRoles},
		{"the global MCP admin reads no entry of a global role", personCaller("max", "apps:read", "apps:write"), unnamed(desk, triage), oneRole},
		{"an agent reads no entry", agentCaller("bot", "drafts:read", "apps:read", "identity:read"), nil, ""},
	}
	for _, tc := range cases {
		got := verdictFor(tc.c, w, d, v)
		if !reflect.DeepEqual(got.Objects, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got.Objects, tc.want)
		}
		note := ""
		for _, f := range got.Info {
			if f.Code == "info.gains-hidden" {
				note += f.Sentence
			}
		}
		if note != tc.note {
			t.Errorf("%s: the hidden note reads %q, want %q", tc.name, note, tc.note)
		}
	}
	// A global role the reader may read, on a server it may not, is left
	// out by the server rule alone.
	elsewhere := drafts.Verdict{Objects: []drafts.Coverage{tickets("on-call", "helpdesk", "dave")}}
	got := verdictFor(administering(personCaller("erin", "identity:read"), map[string]string{"a1": "tracker"}), w, d, elsewhere)
	if len(got.Objects) != 0 || len(got.Info) != 1 || got.Info[0].Sentence != oneServer {
		t.Errorf("on-call on helpdesk reads %+v with the notes %+v, want no entry and %q", got.Objects, got.Info, oneServer)
	}
	raw, err := json.Marshal(verdictFor(rootCaller("kim"), w, d, drafts.Verdict{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"objects"`) {
		t.Errorf("a verdict with no coverage answers %s, want objects left out", raw)
	}
}

// TestVerdictForCountsARoleOnAHiddenServerOnce pins the hidden note's count
// when a role on a server the reader may not read has both gain rows and a
// coverage entry: the entry adds no row of its own, so three gain rows and
// the entry read as three rows, while an entry of a role and server with no
// hidden gain row still counts.
func TestVerdictForCountsARoleOnAHiddenServerOnce(t *testing.T) {
	t.Parallel()
	w := drafts.World{Apps: map[string]drafts.App{"tracker": {Name: "tracker"}, "helpdesk": {Name: "helpdesk"}},
		Roles: map[string]drafts.Role{"on-call": {ID: "r3", Name: "on-call", Kind: drafts.RoleKindApplication}}}
	d := drafts.Draft{ID: "52", Revision: 1, Items: []drafts.Item{{Kind: drafts.KindRole, Name: "on-call", Op: drafts.OpPut}}}
	gain := func(server, tool string) drafts.Gain {
		return drafts.Gain{Role: "on-call", Server: server, Tool: tool, HolderCount: 1, Before: "not-reachable", After: "runs"}
	}
	cov := func(server string) drafts.Coverage {
		return drafts.Coverage{Role: "on-call", Server: server, Type: "ticket", Plural: "tickets", HolderCount: 1, EveryAfter: true,
			Before: []drafts.ObjectName{}, After: []drafts.ObjectName{}}
	}
	erin := administering(personCaller("erin", "identity:read"), map[string]string{"a1": "tracker"})
	cases := []struct {
		name string
		v    drafts.Verdict
		want string
	}{
		{"three gain rows and the entry of one role and server", drafts.Verdict{
			Gains:   []drafts.Gain{gain("helpdesk", "ticket_close"), gain("helpdesk", "ticket_read"), gain("helpdesk", "ticket_assign")},
			Objects: []drafts.Coverage{cov("helpdesk")}},
			"3 rows under Access after publishing are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them."},
		{"an entry with no hidden gain row beside a hidden gain row", drafts.Verdict{
			Gains:   []drafts.Gain{gain("helpdesk", "ticket_close")},
			Objects: []drafts.Coverage{cov("helpdesk"), cov("archive")}},
			"2 rows under Access after publishing are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them."},
	}
	for _, tc := range cases {
		got := verdictFor(erin, w, d, tc.v)
		if len(got.Info) != 1 || got.Info[0].Code != "info.gains-hidden" || got.Info[0].Sentence != tc.want || len(got.Objects) != 0 || len(got.Gains) != 0 {
			t.Errorf("%s: gains %+v, objects %+v, notes %+v, want none and %q", tc.name, got.Gains, got.Objects, got.Info, tc.want)
		}
	}
}
