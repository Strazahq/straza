package store

import (
	"context"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/audit"
)

// TestAuditListFiltered pins the server-side audit narrowing: q is a
// case-insensitive substring over the raw CE text with LIKE metacharacters
// neutralized, effect matches data.effect exactly and excludes rows
// carrying no decision effect, both compose, and both apply BEFORE the
// limit so a page is a page of matches and the seq cursor walks matches.
// Filtering after the fetch would make "what was denied yesterday"
// unanswerable past the fetched window.
func TestAuditListFiltered(t *testing.T) {
	forEachStore(t, testAuditListFiltered)
}

func testAuditListFiltered(t *testing.T, s Store) {
	ctx := context.Background()

	seed := []struct{ id, ce string }{
		{"ce-f-1", `{"id":"ce-f-1","type":"straza.audit.tool","data":{"command":"kubectl GET pods","effect":"allow"}}`},
		{"ce-f-2", `{"id":"ce-f-2","type":"straza.audit.tool","data":{"command":"rm -rf /tmp/x","effect":"deny"}}`},
		{"ce-f-3", `{"id":"ce-f-3","type":"straza.identity.updated","data":{"action":"approver-enroll"}}`},
		{"ce-f-4", `{"id":"ce-f-4","type":"straza.audit.mcp","data":{"toolName":"straza__approval_await","effect":"deny"}}`},
		{"ce-f-5", `{"id":"ce-f-5","type":"straza.audit.tool","data":{"command":"rm -rf /tmp/y","effect":"deny"}}`},
	}
	prev := audit.Genesis
	seqs := map[string]int64{}
	for _, row := range seed {
		rec, err := s.Audit().Append(ctx, row.id, row.ce, prev, "h-"+row.id)
		if err != nil {
			t.Fatal(err)
		}
		prev = "h-" + row.id
		seqs[row.id] = rec.Seq
	}

	list := func(f AuditFilter, after int64, limit int) []AuditRecord {
		t.Helper()
		out, err := s.Audit().ListFiltered(ctx, f, after, limit)
		if err != nil {
			t.Fatalf("ListFiltered(%+v): %v", f, err)
		}
		return out
	}
	ids := func(recs []AuditRecord) []string {
		var out []string
		for _, r := range recs {
			for _, row := range seed {
				if seqs[row.id] == r.Seq {
					out = append(out, row.id)
				}
			}
		}
		return out
	}
	wantIDs := func(got []AuditRecord, want ...string) {
		t.Helper()
		g := ids(got)
		if len(g) != len(want) {
			t.Fatalf("got %v, want %v", g, want)
		}
		for i := range want {
			if g[i] != want[i] {
				t.Fatalf("got %v, want %v", g, want)
			}
		}
	}

	// q is a case-insensitive substring over the raw CE text.
	wantIDs(list(AuditFilter{Q: "RM -RF"}, 0, 10), "ce-f-2", "ce-f-5")

	// LIKE metacharacters in the needle mean themselves: "rm_-rf" is NOT
	// "rm" + any character + "-rf", so it matches nothing.
	wantIDs(list(AuditFilter{Q: "rm_-rf"}, 0, 10))

	// The literal double underscore still matches (escaped, not wildcard).
	wantIDs(list(AuditFilter{Q: "straza__approval_await"}, 0, 10), "ce-f-4")

	// effect narrows to data.effect; the no-effect identity row is excluded
	// from BOTH values, never coerced into either.
	wantIDs(list(AuditFilter{Effect: "allow"}, 0, 10), "ce-f-1")
	wantIDs(list(AuditFilter{Effect: "deny"}, 0, 10), "ce-f-2", "ce-f-4", "ce-f-5")

	// q and effect compose (AND).
	wantIDs(list(AuditFilter{Q: "rm -rf", Effect: "deny"}, 0, 10), "ce-f-2", "ce-f-5")
	wantIDs(list(AuditFilter{Q: "kubectl", Effect: "deny"}, 0, 10))

	// Filters apply BEFORE the limit, and the seq cursor walks MATCHES: a
	// limit-1 page returns one match, and paging after it skips the
	// non-matching rows without consuming the page.
	first := list(AuditFilter{Effect: "deny"}, 0, 1)
	wantIDs(first, "ce-f-2")
	wantIDs(list(AuditFilter{Effect: "deny"}, first[0].Seq, 10), "ce-f-4", "ce-f-5")

	// ListRecentFiltered is the newest-first window of MATCHES.
	recent, err := s.Audit().ListRecentFiltered(ctx, AuditFilter{Effect: "deny"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(recent, "ce-f-5", "ce-f-4")

	// The zero filter narrows nothing (List equivalence).
	if got := list(AuditFilter{}, 0, 100); len(got) != len(seed) {
		t.Fatalf("zero filter returned %d rows, want %d", len(got), len(seed))
	}
}

// TestAuditListFilteredByUser pins the user narrowing behind audit tail
// --user: a principal's rows match on data.user, data.actorId and
// data.actor, the filter lands before the limit so a quiet user's records
// are not pushed out of the newest window by busier users, and the
// unresolved spelling still matches the actor column.
func TestAuditListFilteredByUser(t *testing.T) {
	forEachStore(t, testAuditListFilteredByUser)
}

func testAuditListFilteredByUser(t *testing.T, s Store) {
	ctx := context.Background()
	seed := []struct{ id, ce string }{
		{"ce-u-1", `{"id":"ce-u-1","type":"straza.audit.tool","data":{"user":"u-joe","command":"ls","effect":"allow"}}`},
		{"ce-u-2", `{"id":"ce-u-2","type":"straza.identity.updated","data":{"user":"u-joe","action":"user.killed"}}`},
		{"ce-u-3", `{"id":"ce-u-3","type":"straza.audit.admin","data":{"actor":"joe","actorId":"u-joe","action":"roles.assign"}}`},
		{"ce-u-4", `{"id":"ce-u-4","type":"straza.audit.tool","data":{"user":"u-kim","command":"ls","effect":"allow"}}`},
		{"ce-u-5", `{"id":"ce-u-5","type":"straza.audit.tool","data":{"user":"u-kim","command":"ls","effect":"allow"}}`},
		{"ce-u-6", `{"id":"ce-u-6","type":"straza.audit.tool","data":{"user":"u-kim","command":"ls","effect":"deny"}}`},
		{"ce-u-7", `{"id":"ce-u-7","type":"straza.audit.authn","data":{"action":"session.end","outcome":"lifetime-closed","userId":"u-joe"}}`},
	}
	prev := audit.Genesis
	seqs := map[int64]string{}
	for _, row := range seed {
		rec, err := s.Audit().Append(ctx, row.id, row.ce, prev, "h-"+row.id)
		if err != nil {
			t.Fatal(err)
		}
		prev = "h-" + row.id
		seqs[rec.Seq] = row.id
	}
	ids := func(recs []AuditRecord) []string {
		out := []string{}
		for _, r := range recs {
			out = append(out, seqs[r.Seq])
		}
		return out
	}
	tests := []struct {
		name   string
		filter AuditFilter
		limit  int
		recent bool
		want   []string
	}{
		{"resolved pair matches user, userId, actorId and actor", AuditFilter{UserID: "u-joe", Username: "joe"}, 10, false, []string{"ce-u-1", "ce-u-2", "ce-u-3", "ce-u-7"}},
		{"id alone matches user, userId and actorId", AuditFilter{UserID: "u-joe"}, 10, false, []string{"ce-u-1", "ce-u-2", "ce-u-3", "ce-u-7"}},
		{"unresolved spelling matches the actor column", AuditFilter{UserID: "joe", Username: "joe"}, 10, false, []string{"ce-u-3"}},
		{"newest window holds the quiet user's rows", AuditFilter{UserID: "u-joe", Username: "joe"}, 2, true, []string{"ce-u-7", "ce-u-3"}},
		{"composes with effect", AuditFilter{UserID: "u-joe", Username: "joe", Effect: "allow"}, 10, false, []string{"ce-u-1"}},
		{"unknown principal matches nothing", AuditFilter{UserID: "u-nobody", Username: "nobody"}, 10, false, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []AuditRecord
			var err error
			if tt.recent {
				got, err = s.Audit().ListRecentFiltered(ctx, tt.filter, tt.limit)
			} else {
				got, err = s.Audit().ListFiltered(ctx, tt.filter, 0, tt.limit)
			}
			if err != nil {
				t.Fatal(err)
			}
			if g := ids(got); strings.Join(g, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v, want %v", g, tt.want)
			}
		})
	}
}
