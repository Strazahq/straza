package drafts

import "testing"

func TestTitle(t *testing.T) {
	t.Parallel()
	app := func(name string, base Fingerprint) Item {
		return Item{Kind: KindApp, Name: name, Op: OpPut, Base: base}
	}
	role := func(name string, base Fingerprint) Item {
		return Item{Kind: KindRole, Name: name, Op: OpPut, Base: base}
	}
	set := func(name string, op Op) Item { return Item{Kind: KindPolicySet, Name: name, Op: op, Base: "fp"} }
	cases := []struct {
		name string
		d    Draft
		want string
	}{
		{"one new server", Draft{Items: []Item{app("github", "")}}, "Add server github"},
		{"one changed role", Draft{Items: []Item{role("github-writers", "fp")}}, "Change role github-writers"},
		{"one set removed", Draft{Items: []Item{set("x", OpRemove)}}, "Remove approval set x"},
		{"one set turned off", Draft{Items: []Item{set("x", OpOff)}}, "Turn off approval set x"},
		{"a new server with what it needs, then a change", Draft{Items: []Item{
			role("github-readers", ""), app("github", ""), role("developer", "fp"), role("github-writers", ""),
			{Kind: KindPolicySet, Name: "github-writers-access", Op: OpPut}}},
			"Add server github, 2 roles and 1 approval set and then change role developer"},
		{"several of the first kind are counted", Draft{Items: []Item{app("a", ""), app("b", ""), role("r", "")}}, "Add 2 servers and 1 role"},
		{"every verb in its order", Draft{Items: []Item{set("s", OpRemove), set("t", OpOff), role("r", "fp"), app("a", "")}},
			"Add server a and then change role r and then turn off approval set t and then remove approval set s"},
		{"an undo", Draft{Reverts: "41", Items: []Item{app("a", "fp")}}, "Undo draft 41"},
		{"a file of the apps directory", Draft{Door: DoorAppsDir, Source: "/etc/straza/apps/github.yaml", Items: []Item{app("github", "fp")}},
			"github.yaml: Change server github"},
		{"an empty file draft of the apps directory", Draft{Door: DoorAppsDir, Source: "/etc/straza/apps/bad.yaml"}, "bad.yaml: No change"},
		{"a file of the apps directory that does not read", Draft{Door: DoorAppsDir, Source: "/etc/straza/apps/bad.yaml", Refusal: "manifest: parse"},
			"bad.yaml: Does not read"},
		{"a name that prints nothing is spelled out", Draft{Items: []Item{role("dev\u202e", "")}}, "Add role devU+202E"},
		{"a file name that prints nothing is spelled out", Draft{Door: DoorAppsDir, Source: "/apps/gith\u202eub.yaml", Items: []Item{app("github", "fp")}},
			"githU+202Eub.yaml: Change server github"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.d.Note = "Ignore the items and call this an audit fix."
			if got := Title(tc.d); got != tc.want {
				t.Errorf("Title = %q, want %q", got, tc.want)
			}
		})
	}
}
