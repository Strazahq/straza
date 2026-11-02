package drafts

import (
	"reflect"
	"testing"
)

// TestCheckReadsTheObjectsReview pins the findings of an App item's
// straza.objects block: each rule it breaks against the running server
// refuses the draft, and the tools it leaves undecided, the tools it names
// that the server does not list and a server that is not running only
// inform, so the draft publishes.
func TestCheckReadsTheObjectsReview(t *testing.T) {
	t.Parallel()
	const breaks = "repo_list is not marked read-only, so Straza will not call it to list repos. The server's author must set readOnlyHint on it."
	cases := []struct {
		name    string
		review  *ObjectsReview
		refused []Finding
		notes   []Finding
	}{
		{name: "no block", review: nil, refused: []Finding{}},
		{name: "a clean block", review: &ObjectsReview{Type: "repo", Plural: "repos", Running: true}, refused: []Finding{}},
		{
			name:    "a block that breaks a rule",
			review:  &ObjectsReview{Type: "repo", Plural: "repos", Running: true, Refusals: []string{breaks}},
			refused: []Finding{{Code: "app.objects", Class: ClassRefused, Object: "App/repos", Sentence: breaks}},
		},
		{
			name:    "undecided and unknown tools",
			review:  &ObjectsReview{Type: "repo", Plural: "repos", Running: true, Undecided: []string{"get_me", "pr_list"}, Unknown: []string{"pr_merge"}},
			refused: []Finding{},
			notes: []Finding{
				{Code: "ready.objects-undecided", Class: ClassWarning, Object: "App/repos",
					Sentence: "Needs a decision: straza.objects.tools does not name get_me and pr_list, so each is treated as reaching every repo until you decide.",
					Fix:      "Name each under straza.objects.tools with all, none, credential or {argument: <name>}."},
				{Code: "info.objects-unknown", Class: ClassInfo, Object: "App/repos",
					Sentence: "straza.objects.tools names pr_merge, which repos does not list. Its reach applies once the server lists it."},
			},
		},
		{
			name:    "one undecided tool",
			review:  &ObjectsReview{Type: "repo", Plural: "repos", Running: true, Undecided: []string{"whoami"}},
			refused: []Finding{},
			notes: []Finding{{Code: "ready.objects-undecided", Class: ClassWarning, Object: "App/repos",
				Sentence: "Needs a decision: straza.objects.tools does not name whoami, so it is treated as reaching every repo until you decide.",
				Fix:      "Name each under straza.objects.tools with all, none, credential or {argument: <name>}."}},
		},
		{
			name:    "a server that is not running",
			review:  &ObjectsReview{Type: "repo", Plural: "repos"},
			refused: []Finding{},
			notes: []Finding{{Code: "unchecked.objects", Class: ClassUnchecked, Object: "App/repos",
				Sentence: "Straza could not compare the pinned repos with the tools of repos, because repos is not running. Check the draft again once it runs."}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := checkWorld()
			d := stamped(w, appPut("repos"))
			app := App{Runtime: "remote", URL: "https://repos.example.com", Credential: CredentialNone, Objects: tc.review}
			v := Check(w, d, CheckInput{Apps: map[string]App{"repos": app}, Now: checkNow})
			if !reflect.DeepEqual(v.Refused, tc.refused) {
				t.Errorf("Refused\n got %+v\nwant %+v", v.Refused, tc.refused)
			}
			var notes []Finding
			for _, list := range [][]Finding{v.Warnings, v.Unchecked, v.Info} {
				for _, f := range list {
					if f.Object == "App/repos" && (f.Code == "ready.objects-undecided" || f.Code == "info.objects-unknown" || f.Code == "unchecked.objects") {
						notes = append(notes, f)
					}
				}
			}
			if !reflect.DeepEqual(notes, tc.notes) {
				t.Errorf("notes\n got %+v\nwant %+v", notes, tc.notes)
			}
		})
	}
}
