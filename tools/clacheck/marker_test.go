package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const markerDiff = `diff --git a/x.go b/x.go
index 1111111..2222222 100644
--- a/x.go
+++ b/x.go
@@ -1,3 +1,4 @@
 package x
%s
diff --git a/y.go b/y.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/y.go
@@ -0,0 +1 @@
+package y
`

func diffWith(line string) string {
	return strings.Replace(markerDiff, "%s", line, 1)
}

func commentBy(a account, id int64, body string) issueComment {
	c := issueComment{ID: id, Body: body, HTMLURL: "https://github.com/strazahq/straza/pull/123#issuecomment-" + itoa(id)}
	c.User.ID, c.User.Login = a.ID, a.Login
	return c
}

func kindOf(c issueComment, kind string) issueComment {
	c.Kind = kind
	return c
}

// addedFile is a diff that adds a file with the text.
func addedFile(path, text string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path +
		"\n@@ -0,0 +1," + strconv.Itoa(len(lines)) + " @@\n+" + strings.Join(lines, "\n+") + "\n"
}

// TestMarkerExemptFiles adds the Apache-2.0 text, whose definition sentence
// holds the words, to files anywhere in the tree, and a sentence that marks
// material to the same and other files. The definition sentence is never read
// as the marker, and a marking is read in every file but the agreement texts
// and CONTRIBUTING.md at the root and the check's own sources.
func TestMarkerExemptFiles(t *testing.T) {
	apache := string(fixture(t, "apache-2.0.txt"))
	const marking = "The code in this directory is Not a Contribution."
	cases := []struct {
		path, text string
		marked     bool
	}{
		{"LICENSE-APACHE", apache, false}, {"pkg/LICENSE", apache, false}, {"plugins/x/LICENSE-MIT", apache, false},
		{"deploy/compose/eval-stack/midpoint/connectors/universal-rest-connector.LICENSE.txt", apache, false},
		{"internal/server/console/dist/THIRD_PARTY_NOTICES.txt", apache, false},
		{"docs/vendored.txt", apache, false}, {"LICENSES/x.txt", apache, false},
		{"internal/vendorlib/LICENSE", marking, true}, {"pkg/helpers/LICENSE-helpers.go", marking, true},
		{"internal/vendorlib/notes.LICENSE.txt", marking, true}, {"docs/THIRD_PARTY_NOTICES.txt", marking, true},
		{"internal/vendorlib/README.md", marking, true}, {"web/CLA.md", marking, true}, {"docs/CONTRIBUTING.md", marking, true},
		{"tools/clacheckx/x.go", marking, true}, {"tools/clacheck/extra.go", marking, true},
		{"LICENSE", apache + "\n" + marking, true},
		{"CLA.md", marking, false}, {"CLA-ENTITY.md", marking, false}, {"CONTRIBUTING.md", marking, false},
		{"tools/clacheck/marker.go", marking, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := scanMarkers(markerInput{Diff: addedFile(tc.path, tc.text)})
			if (len(got) > 0) != tc.marked {
				t.Fatalf("places %q, want marked %v", got, tc.marked)
			}
		})
	}
	got := scanMarkers(markerInput{Title: "Not a Contribution", Body: apache,
		Commits: []commit{{OID: oidA, Message: apache + "\n\nThis commit is Not a Contribution."}}})
	if want := []string{"the title", "the message of commit a1b2c3d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestOwnSourcesListed keeps the exemption of the check's own files exact:
// every file of this package that holds the words is listed, and every listed
// file exists.
func TestOwnSourcesListed(t *testing.T) {
	holding := map[string]bool{}
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err == nil && hasMarker(string(data)) {
			holding["tools/clacheck/"+filepath.ToSlash(p)] = true
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(holding, ownSources) {
		t.Fatalf("files holding the words %v, ownSources %v", holding, ownSources)
	}
}

// mathBold writes the ASCII letters of s as mathematical bold letters.
func mathBold(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(0x1d400 + r - 'A')
		case r >= 'a' && r <= 'z':
			b.WriteRune(0x1d41a + r - 'a')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestScanMarkers(t *testing.T) {
	speakers := ids(alice, bob)
	bot := account{ID: botID, Login: "github-actions[bot]"}
	cases := []struct {
		name string
		in   markerInput
		want []string
	}{
		{name: "clean pull request", in: markerInput{Title: "Fix the retry delay", Body: "It waited too long.", Diff: diffWith("+// fine")}},
		{name: "title", in: markerInput{Title: "Add a cache (Not a Contribution)"}, want: []string{"the title"}},
		{name: "description in any case", in: markerInput{Body: "NOT A CONTRIBUTION"}, want: []string{"the description"}},
		{name: "words split across a line break", in: markerInput{Body: "This part is not a\r\ncontribution."}, want: []string{"the description"}},
		{name: "near miss is not the marker", in: markerInput{Body: "I am not a contributor yet."}},
		{name: "commit message", in: markerInput{Commits: []commit{{OID: oidA, Message: "Vendor the parser\n\nNot a Contribution"}}},
			want: []string{"the message of commit a1b2c3d"}},
		{name: "added line", in: markerInput{Diff: diffWith("+// Not a Contribution")}, want: []string{"an added line in `x.go`"}},
		{name: "added line that starts with plus signs", in: markerInput{Diff: diffWith("+++ not a contribution")}, want: []string{"an added line in `x.go`"}},
		{name: "two added lines in one file are named once", in: markerInput{Diff: diffWith("+// Not a Contribution\n+// Not a Contribution")},
			want: []string{"an added line in `x.go`"}},
		{name: "removed line is not read", in: markerInput{Diff: diffWith("-// Not a Contribution")}},
		{name: "context line is not read", in: markerInput{Diff: diffWith(" // Not a Contribution")}},
		{name: "file header is not an added line", in: markerInput{Diff: "diff --git a/n b/n\n--- a/Not a Contribution\n+++ b/Not a Contribution\n@@ -1 +1 @@\n+ok\n"}},
		{name: "comment by the opener", in: markerInput{Speakers: speakers, Comments: []issueComment{commentBy(alice, 7, "Not a Contribution")}},
			want: []string{"the comment by @alice at https://github.com/strazahq/straza/pull/123#issuecomment-7"}},
		{name: "comment by a commit author", in: markerInput{Speakers: speakers, Comments: []issueComment{commentBy(bob, 8, "part of it is not a contribution")}},
			want: []string{"the comment by @bob at https://github.com/strazahq/straza/pull/123#issuecomment-8"}},
		{name: "comment by another account is not read", in: markerInput{Speakers: speakers, Comments: []issueComment{commentBy(carol, 9, "Not a Contribution")}}},
		{name: "comment by the check itself is not read", in: markerInput{Speakers: speakers, Comments: []issueComment{commentBy(bot, 10, "the words \"Not a Contribution\" appear")}}},
		{name: "words split over two added lines", in: markerInput{Diff: diffWith("+// Not a\n+// Contribution")}, want: []string{"an added line in `x.go`"}},
		{name: "markdown emphasis", in: markerInput{Body: "**Not** a _Contribution_"}, want: []string{"the description"}},
		{name: "zero-width space and soft hyphen", in: markerInput{Body: "Not a Contri\u200bbu\u00adtion"}, want: []string{"the description"}},
		{name: "hyphens between the words", in: markerInput{Body: "Not-a\u2011Contribution"}, want: []string{"the description"}},
		{name: "underscores between the words", in: markerInput{Title: "not_a_contribution"}, want: []string{"the title"}},
		{name: "Cyrillic and Greek lookalike letters", in: markerInput{Body: "N\u043et a C\u03bfntr\u0456bution"}, want: []string{"the description"}},
		{name: "fullwidth letters", in: markerInput{Body: "\uff2e\uff4f\uff54 \uff41 \uff23\uff4f\uff4e\uff54\uff52\uff49\uff42\uff55\uff54\uff49\uff4f\uff4e"}, want: []string{"the description"}},
		{name: "the words inside a longer word are not the marker", in: markerInput{Body: "We cannot a contributions list."}},
		{name: "Cyrillic capital ve for B", in: markerInput{Body: "NOT A CONTRI\u0412UTION"}, want: []string{"the description"}},
		{name: "combining accent", in: markerInput{Body: "Not a Contri\u0301bution"}, want: []string{"the description"}},
		{name: "precomposed accents", in: markerInput{Body: "N\u00f3t \u00e1 Contr\u00edb\u00fati\u00f3n"}, want: []string{"the description"}},
		{name: "mathematical bold letters", in: markerInput{Body: mathBold("Not a Contribution")}, want: []string{"the description"}},
		{name: "Greek lunate sigma for c", in: markerInput{Body: "Not a \u03f2ontribution"}, want: []string{"the description"}},
		{name: "one word", in: markerInput{Title: "NotAContribution"}, want: []string{"the title"}},
		{name: "one word inside a longer identifier is not the marker", in: markerInput{Body: "func isNotAContribution()"}},
		{name: "the Apache definition sentence is not the marker", in: markerInput{Body: "excluding communication that is conspicuously marked or otherwise\n designated in writing by the copyright owner as \"Not a Contribution.\""}},
		{name: "review comment by the opener", in: markerInput{Speakers: speakers, Comments: []issueComment{kindOf(commentBy(alice, 11, "Not a Contribution"), kindReviewComment)}},
			want: []string{"the review comment by @alice at https://github.com/strazahq/straza/pull/123#issuecomment-11"}},
		{name: "review by a commit author", in: markerInput{Speakers: speakers, Comments: []issueComment{kindOf(commentBy(bob, 12, "Not a Contribution"), kindReview)}},
			want: []string{"the review by @bob at https://github.com/strazahq/straza/pull/123#issuecomment-12"}},
		{name: "every place is named in order", in: markerInput{Title: "Not a Contribution", Body: "Not a Contribution",
			Commits: []commit{{OID: oidB, Message: "not a contribution"}}, Diff: diffWith("+Not a Contribution"),
			Speakers: speakers, Comments: []issueComment{commentBy(alice, 7, "Not a Contribution")}},
			want: []string{"the title", "the description", "the message of commit b2c3d4e", "an added line in `x.go`",
				"the comment by @alice at https://github.com/strazahq/straza/pull/123#issuecomment-7"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanMarkers(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
