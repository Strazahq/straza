package drafts

import (
	"reflect"
	"testing"
)

// TestRolePutJudgesOnlyTheRowItChanges pins that the access row rules
// judge the row a Role put adds or changes, never the row live state holds
// and the put leaves as it is: a business role bound before the kind rules
// keeps its row through a change of its description, and is refused the
// moment the draft changes that row or moves it.
func TestRolePutJudgesOnlyTheRowItChanges(t *testing.T) {
	t.Parallel()
	refused := []string{"access.role Role/engineering () business role: it composes application roles and reaches tools through them. Give an application role access instead. | - | -"}
	cases := []struct {
		name string
		row  string
		want []string
	}{
		{"the live row kept", "        - app: github\n          tools: [x, y]\n", []string{}},
		{"the live row kept in another order", "        - app: github\n          tools: [y, x]\n", []string{}},
		{"the row's tools changed", "        - app: github\n          tools: [x]\n", refused},
		{"the row moved to another server", "        - app: jira\n          tools: [x, y]\n", refused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := checkWorld()
			w.Access["engineering"] = Access{ID: "b9", Server: "github", Tools: []string{"x", "y"}}
			item := intakeRole("engineering", "    kind: business\n    description: Changed.\n    implies: [dev]\n    bindings:\n"+tc.row)
			v := Check(w, stamped(w, item), CheckInput{Now: checkNow, RefusalsOnly: true})
			if got := riskLines(v.Refused); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("refused %q\nwant %q", got, tc.want)
			}
		})
	}
}
