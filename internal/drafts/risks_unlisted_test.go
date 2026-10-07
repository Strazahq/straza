package drafts

import (
	"reflect"
	"testing"
)

// TestUnlistedServerNewRow pins a held role's row that is new on a server
// nobody has listed: who gains cannot name the tools, so the row draws a
// tick access.tools-later that says so, beside its gain on tool *. A role
// nobody holds draws none. The role belongs to that server, since only a
// role a server owns gains a new row.
func TestUnlistedServerNewRow(t *testing.T) {
	t.Parallel()
	const later = "access.tools-later Role/readers (tick) readers will reach tools on jira, which nobody has listed yet, so Straza cannot name them. | "
	cases := []struct {
		name      string
		holders   bool
		wantRisks []string
	}{
		{"a new row", true, []string{later + "- | x"}},
		{"a new row of a role nobody holds", false, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := gainWorld()
			jira := w.Apps["jira"]
			jira.Offered = nil
			w.Apps["jira"] = jira
			readers := w.Roles["readers"]
			readers.Owner, readers.Owned = "jira", true
			w.Roles["readers"] = readers
			delete(w.Access, "readers")
			if !tc.holders {
				delete(w.Holders, "readers")
			}
			spec := "    kind: application\n    server: jira\n    bindings:\n        - app: jira\n          tools: [x]\n"
			v := Check(w, stamped(w, gainRole("readers", spec)), CheckInput{Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", riskLines(v.Refused))
			}
			if got := only(gainLines(v.Gains), "readers jira/*"); len(got) != 1 {
				t.Errorf("gains on jira %q, want the one gain on tool *", got)
			}
			if got := only(riskLines(v.Risks), "access.tools-later"); !reflect.DeepEqual(got, tc.wantRisks) {
				t.Errorf("risks %q\nwant %q", got, tc.wantRisks)
			}
		})
	}
}
