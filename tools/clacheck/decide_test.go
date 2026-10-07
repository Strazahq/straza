package main

import (
	"fmt"
	"reflect"
	"testing"
)

var (
	alice      = account{ID: 1001, Login: "alice"}
	bob        = account{ID: 1002, Login: "bob"}
	carol      = account{ID: 1003, Login: "carol"}
	maintainer = account{ID: 4242, Login: "sample-maintainer"}
	depbot     = account{ID: 49699333, Login: "dependabot[bot]"}
	ghost      = account{ID: ghostID, Login: "ghost"}
)

const (
	oidA = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	oidB = "b2c3d4e5f60718293a4b5c6d7e8f901234567890"
)

// by is an author whose email GitHub links to the account.
func by(a account) actor {
	return actor{Name: a.Login, Email: a.Login + "@example.com", ID: a.ID, Login: a.Login}
}

// unlinked is an author whose email GitHub links to no account.
func unlinked(name, email string) actor { return actor{Name: name, Email: email} }

func ids(as ...account) map[int64]bool {
	m := map[int64]bool{}
	for _, a := range as {
		m[a.ID] = true
	}
	return m
}

func commitOf(oid string, authors ...actor) commit {
	return commit{OID: oid, Message: "Change something", Authors: authors}
}

// summary renders a verdict as short strings so that a table row can state it.
func summary(v verdict) (passes, gaps, tools []string) {
	passNames := map[passKind]string{passOpener: "opener", passAllowlist: "allowlist", passHere: "here", passCarried: "carried"}
	gapNames := map[gapKind]string{gapOpener: "opener", gapHere: "here", gapCarried: "carried", gapUnlinked: "unlinked", gapGhost: "ghost"}
	for _, p := range v.Passed {
		passes = append(passes, p.Account.Login+":"+passNames[p.Kind])
	}
	for _, g := range v.Gaps {
		who := g.Account.Login
		if g.Kind == gapUnlinked {
			who = g.Email
		}
		gaps = append(gaps, fmt.Sprintf("%s:%s@%s", who, gapNames[g.Kind], g.Commit))
	}
	for _, u := range v.Tools {
		tools = append(tools, u.Email+"@"+u.Commit)
	}
	return passes, gaps, tools
}

func TestDecide(t *testing.T) {
	tools := []string{"noreply@anthropic.com"}
	allow := ids(maintainer, depbot)
	cases := []struct {
		name      string
		opener    account
		commits   []commit
		has, here map[int64]bool
		passes    []string
		gaps      []string
		tools     []string
	}{
		{name: "opener with an account file and own commits passes", opener: alice,
			commits: []commit{commitOf(oidA, by(alice))}, has: ids(alice),
			passes: []string{"alice:opener"}},
		{name: "opener who has not accepted fails", opener: alice,
			commits: []commit{commitOf(oidA, by(alice))},
			gaps:    []string{"alice:opener@"}},
		{name: "opener on the allowlist passes without an account file", opener: maintainer,
			commits: []commit{commitOf(oidA, by(maintainer))},
			passes:  []string{"sample-maintainer:allowlist"}},
		{name: "dependabot passes through the allowlist", opener: depbot,
			commits: []commit{commitOf(oidA, actor{Name: "dependabot[bot]", Email: "49699333+dependabot[bot]@users.noreply.github.com", ID: depbot.ID, Login: depbot.Login})},
			passes:  []string{"dependabot[bot]:allowlist"}},
		{name: "commit email linked to no account fails and names the commit", opener: alice,
			commits: []commit{commitOf(oidA, unlinked("alice", "alice@laptop.local"))}, has: ids(alice),
			passes: []string{"alice:opener"}, gaps: []string{"alice@laptop.local:unlinked@a1b2c3d"}},
		{name: "allowlisted account name with an unlinked email fails", opener: alice,
			commits: []commit{commitOf(oidA, unlinked("sample-maintainer", "sample-maintainer@example.org"))}, has: ids(alice),
			passes: []string{"alice:opener"}, gaps: []string{"sample-maintainer@example.org:unlinked@a1b2c3d"}},
		{name: "borrowed email of an account that accepted earlier fails", opener: bob,
			commits: []commit{commitOf(oidA, by(carol))}, has: ids(bob, carol),
			passes: []string{"bob:opener"}, gaps: []string{"carol:here@a1b2c3d"}},
		{name: "borrowed email passes once that account accepts on this pull request", opener: bob,
			commits: []commit{commitOf(oidA, by(carol))}, has: ids(bob, carol), here: ids(carol),
			passes: []string{"bob:opener", "carol:here"}},
		{name: "opener who has not accepted fails when every commit author accepted here", opener: carol,
			commits: []commit{commitOf(oidA, by(bob))}, has: ids(bob), here: ids(bob),
			passes: []string{"bob:here"}, gaps: []string{"carol:opener@"}},
		{name: "AI co-author is a tool and needs no acceptance", opener: alice,
			commits: []commit{commitOf(oidA, by(alice), unlinked("Claude", "noreply@anthropic.com"))}, has: ids(alice),
			passes: []string{"alice:opener"}, tools: []string{"noreply@anthropic.com@a1b2c3d"}},
		{name: "tool email matches without regard to case", opener: alice,
			commits: []commit{commitOf(oidA, by(alice), unlinked("Claude", "NoReply@Anthropic.com"))}, has: ids(alice),
			passes: []string{"alice:opener"}, tools: []string{"NoReply@Anthropic.com@a1b2c3d"}},
		{name: "tool as the git author is a tool too", opener: alice,
			commits: []commit{commitOf(oidA, unlinked("Claude", "noreply@anthropic.com"))}, has: ids(alice),
			passes: []string{"alice:opener"}, tools: []string{"noreply@anthropic.com@a1b2c3d"}},
		{name: "co-author who has not accepted fails", opener: alice,
			commits: []commit{commitOf(oidA, by(alice), by(bob))}, has: ids(alice, bob),
			passes: []string{"alice:opener"}, gaps: []string{"bob:here@a1b2c3d"}},
		{name: "co-author who accepted on this pull request passes", opener: alice,
			commits: []commit{commitOf(oidA, by(alice), by(bob))}, has: ids(alice, bob), here: ids(bob),
			passes: []string{"alice:opener", "bob:here"}},
		{name: "acceptance here without an account file still fails", opener: alice,
			commits: []commit{commitOf(oidA, by(alice), by(bob))}, has: ids(alice), here: ids(bob),
			passes: []string{"alice:opener"}, gaps: []string{"bob:here@a1b2c3d"}},
		{name: "co-author of a commit the maintainer carried over passes with an account file", opener: maintainer,
			commits: []commit{commitOf(oidA, by(maintainer), by(carol))}, has: ids(carol),
			passes: []string{"sample-maintainer:allowlist", "carol:carried"}},
		{name: "co-author of a carried commit without an account file fails", opener: maintainer,
			commits: []commit{commitOf(oidA, by(maintainer), by(carol))},
			passes:  []string{"sample-maintainer:allowlist"}, gaps: []string{"carol:carried@a1b2c3d"}},
		{name: "unlinked co-author of a carried commit fails", opener: maintainer,
			commits: []commit{commitOf(oidA, by(maintainer), unlinked("Dave", "dave@example.org"))},
			passes:  []string{"sample-maintainer:allowlist"}, gaps: []string{"dave@example.org:unlinked@a1b2c3d"}},
		{name: "commit linked to the opener adds no second entry", opener: alice,
			commits: []commit{commitOf(oidA, by(alice)), commitOf(oidB, by(alice))},
			gaps:    []string{"alice:opener@"}},
		{name: "allowlisted author in another person's pull request needs an acceptance here", opener: alice,
			commits: []commit{commitOf(oidA, by(maintainer))}, has: ids(alice),
			passes: []string{"alice:opener"}, gaps: []string{"sample-maintainer:here@a1b2c3d"}},
		{name: "an account is listed once with its first commit", opener: alice,
			commits: []commit{commitOf(oidA, by(bob)), commitOf(oidB, by(bob))}, has: ids(alice),
			passes: []string{"alice:opener"}, gaps: []string{"bob:here@a1b2c3d"}},
		{name: "an account with a gap is not also listed as passed", opener: maintainer,
			commits: []commit{commitOf(oidA, by(maintainer), by(carol)), commitOf(oidB, by(carol))}, has: ids(carol),
			passes: []string{"sample-maintainer:allowlist"}, gaps: []string{"carol:here@b2c3d4e"}},
		{name: "co-author linked to the opener passes with the opener", opener: alice,
			commits: []commit{commitOf(oidA, by(bob), by(alice))}, has: ids(alice, bob), here: ids(bob),
			passes: []string{"alice:opener", "bob:here"}},
		{name: "carried commit on a contributor's pull request gets no carry-over", opener: alice,
			commits: []commit{commitOf(oidA, by(alice)), commitOf(oidB, by(maintainer), by(carol))},
			has:     ids(alice, carol, maintainer), here: ids(maintainer),
			passes: []string{"alice:opener", "sample-maintainer:here"}, gaps: []string{"carol:here@b2c3d4e"}},
		{name: "opener whose account was deleted never passes, even with a ghost account file", opener: ghost,
			commits: []commit{commitOf(oidA, unlinked("Claude", "noreply@anthropic.com"))}, has: ids(ghost),
			gaps: []string{"ghost:ghost@"}, tools: []string{"noreply@anthropic.com@a1b2c3d"}},
		{name: "author shown as the deleted account is judged by the email alone", opener: alice,
			commits: []commit{commitOf(oidA, actor{Name: "Gone", Email: "gone@example.org", ID: ghostID, Login: "ghost"})},
			has:     ids(alice, ghost), here: ids(ghost),
			passes: []string{"alice:opener"}, gaps: []string{"gone@example.org:unlinked@a1b2c3d"}},
		{name: "signed-off-by lines in the message grant nothing and need nothing", opener: alice,
			commits: []commit{{OID: oidA, Message: "Fix\n\nSigned-off-by: Dave <dave@example.org>", Authors: []actor{by(bob)}}},
			has:     ids(alice, bob),
			passes:  []string{"alice:opener"}, gaps: []string{"bob:here@a1b2c3d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := decide(verdictInput{Opener: tc.opener, Commits: tc.commits, HasAccount: tc.has,
				AcceptedHere: tc.here, Tools: tools, Allow: allow})
			passes, gaps, gotTools := summary(v)
			if !reflect.DeepEqual(passes, tc.passes) || !reflect.DeepEqual(gaps, tc.gaps) || !reflect.DeepEqual(gotTools, tc.tools) {
				t.Fatalf("got passes %q gaps %q tools %q\nwant passes %q gaps %q tools %q",
					passes, gaps, gotTools, tc.passes, tc.gaps, tc.tools)
			}
			if v.pass() != (len(tc.gaps) == 0) {
				t.Fatalf("pass() = %v with gaps %q", v.pass(), gaps)
			}
		})
	}
}
