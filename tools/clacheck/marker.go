package main

import (
	"strings"
	"unicode"
)

// markerWords is the phrase that excludes material from the agreement, in
// the form that normalize writes, so that it matches whole words only.
const markerWords = " not a contribution "

// markerInput holds the texts of a pull request that the marker scan reads.
// Speakers are the accounts whose comments, review comments and reviews
// count: the opener and every account that a commit names.
type markerInput struct {
	Title    string
	Body     string
	Commits  []commit
	Diff     string
	Comments []issueComment
	Speakers map[int64]bool
}

// scanMarkers returns, in plain words, every place of the pull request that
// carries the words "Not a Contribution": the title, the description, a
// commit message, the added lines of a file, or a comment, review comment or
// review by the opener or by an account that a commit names. Removed lines,
// context lines, the files that exemptFromLineScan names and the check's own
// comments are not read.
func scanMarkers(in markerInput) []string {
	var places []string
	if hasMarker(in.Title) {
		places = append(places, "the title")
	}
	if hasMarker(in.Body) {
		places = append(places, "the description")
	}
	for _, c := range in.Commits {
		if hasMarker(c.Message) {
			places = append(places, "the message of commit "+shortOID(c.OID))
		}
	}
	for _, file := range markedFiles(in.Diff) {
		places = append(places, "an added line in "+code(file))
	}
	for _, c := range in.Comments {
		if in.Speakers[c.User.ID] && c.User.ID != botID && hasMarker(c.Body) {
			places = append(places, "the "+kindWords(c.Kind)+" by @"+c.User.Login+" at "+c.HTMLURL)
		}
	}
	return places
}

// kindWords names a kind of comment in plain words.
func kindWords(kind string) string {
	switch kind {
	case kindReviewComment:
		return "review comment"
	case kindReview:
		return "review"
	default:
		return "comment"
	}
}

// hasMarker reports whether s carries the words, as the phrase or as one
// word, after the Apache License's own definition of them is taken out.
func hasMarker(s string) bool {
	n := strings.ReplaceAll(normalize(s), apacheDefinition, " ")
	return strings.Contains(n, markerWords) || strings.Contains(n, " notacontribution ")
}

// apacheDefinition is the clause of the Apache License 2.0 that defines the
// words, in the form normalize writes. It is the only use of the words that
// is never read as a marking, wherever it appears, so a license text can be
// added in any folder. Any other use of the words in the same file is read.
var apacheDefinition = normalize(`excluding communication that is conspicuously marked or otherwise
designated in writing by the copyright owner as "Not a Contribution."`)

// lookalikes maps letters that look like the Latin letters of the marker to
// those letters: the Cyrillic and Greek lookalikes, the Greek lunate sigma,
// the dotless i, and the lower-case letters with an accent. It is applied
// before and after lower-casing, so capitals with an accent fold too.
var lookalikes = foldTable(map[rune]string{
	'a': "\u0430\u0410\u03b1\u0391àáâãäåāăą",
	'b': "\u0412\u0392ƀḃ",
	'c': "\u0441\u0421\u03f2\u03f9çćĉċč",
	'i': "\u0456\u0406\u03b9\u0399\u0131ìíîïĩīĭį",
	'n': "\u039dñńņňŉ",
	'o': "\u043e\u041e\u03bf\u039fòóôõöøōŏő",
	'r': "ŕŗř",
	't': "\u0422\u03a4ţťŧ",
	'u': "ùúûüũūŭůűų",
})

func foldTable(from map[rune]string) map[rune]rune {
	out := map[rune]rune{}
	for to, letters := range from {
		for _, r := range letters {
			out[r] = to
		}
	}
	return out
}

// normalize folds s for the marker comparison. It drops format characters,
// such as a zero-width space or a soft hyphen, and combining marks, such as
// an accent typed after its letter. It turns fullwidth and mathematical
// letters and the lookalikes into Latin letters and lower-cases every
// letter. Every run of anything that is not a letter or a digit, such as
// white space, line breaks, hyphens, underscores or emphasis marks, becomes
// one space. The result starts and ends with a space.
func normalize(s string) string {
	var b strings.Builder
	b.WriteByte(' ')
	space := true
	for _, r := range s {
		if unicode.In(r, unicode.Cf, unicode.Mn) {
			continue
		}
		switch {
		case r >= '\uff01' && r <= '\uff5e':
			r -= 0xfee0
		case r >= 0x1d400 && r < 0x1d400+13*52:
			r = 'a' + (r-0x1d400)%52%26
		}
		if l, ok := lookalikes[r]; ok {
			r = l
		}
		r = unicode.ToLower(r)
		if l, ok := lookalikes[r]; ok {
			r = l
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}

// ownSources lists the files of the check itself that hold the words, by
// exact path. TestOwnSourcesListed keeps the list equal to the files.
var ownSources = map[string]bool{
	"tools/clacheck/comment.go": true, "tools/clacheck/main.go": true, "tools/clacheck/marker.go": true,
	"tools/clacheck/pullrequest.go": true, "tools/clacheck/marker_test.go": true, "tools/clacheck/run_test.go": true,
	"tools/clacheck/run_fail_test.go": true, "tools/clacheck/run_fix_test.go": true,
}

// exemptFromLineScan reports whether the added lines of a file stay out of
// the marker scan. Only the agreement texts and CONTRIBUTING.md at the root,
// which define the words, and the check's own sources are left out, each by
// its exact path. The title, the description, the commit messages and the
// comments are read whatever the files.
func exemptFromLineScan(path string) bool {
	return path == "CLA.md" || path == "CLA-ENTITY.md" || path == "CONTRIBUTING.md" || ownSources[path]
}

// markedFiles returns each file of a unified diff whose added lines carry the
// marker, once per file. A line is an added line when it starts with a plus
// sign inside a hunk, so the "+++ b/path" header never counts. Each run of
// consecutive added lines is read as one text, so a line break between the
// words does not hide them.
func markedFiles(diff string) []string {
	var files, block []string
	seen := map[string]bool{}
	file, inHunk := "", false
	flush := func() {
		if len(block) > 0 && !seen[file] && !exemptFromLineScan(file) && hasMarker(strings.Join(block, "\n")) {
			seen[file] = true
			files = append(files, file)
		}
		block = block[:0]
	}
	for _, line := range strings.Split(diff, "\n") {
		if inHunk && strings.HasPrefix(line, "+") {
			block = append(block, line[1:])
			continue
		}
		flush()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, inHunk = "", false
		case !inHunk && strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		}
	}
	flush()
	return files
}
