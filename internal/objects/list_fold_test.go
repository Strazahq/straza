package objects

import (
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestListByValueFold pins the lookup without case: the exact value first,
// then the one object whose value equals the asked one but for case, and
// nothing when two objects differ from it only by case.
func TestListByValueFold(t *testing.T) {
	t.Parallel()
	l := newList(store.ObjectList{AppID: "app-ghapp", Type: "repo"}, []Object{
		{Value: "Strazahq/straza", ID: "101"},
		{Value: "Strazahq/Twin", ID: "201"},
		{Value: "strazahq/twin", ID: "202"},
		{Value: "Strazahq/Grüße", ID: "301"},
	})
	cases := []struct {
		name, asked string
		wantID      string // "" for no object
	}{
		{"the exact value", "Strazahq/straza", "101"},
		{"another case", "strazahq/STRAZA", "101"},
		{"an exact twin wins over its case twin", "strazahq/twin", "202"},
		{"the other exact twin", "Strazahq/Twin", "201"},
		{"two case twins and no exact value", "STRAZAHQ/TWIN", ""},
		{"a non-ASCII value in another case", "strazahq/GRÜSSE", ""},
		{"a non-ASCII value folded", "strazahq/GRÜßE", "301"},
		{"a value the list does not hold", "Strazahq/site", ""},
		{"an empty value", "", ""},
	}
	for _, tc := range cases {
		got, ok := l.ByValueFold(tc.asked)
		if ok != (tc.wantID != "") || got.ID != tc.wantID {
			t.Errorf("%s: ByValueFold(%q) = %q %v, want %q", tc.name, tc.asked, got.ID, ok, tc.wantID)
		}
	}
}
