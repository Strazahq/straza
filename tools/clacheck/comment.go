package main

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// statusMarker starts the one status comment the check keeps on a pull request.
const statusMarker = "<!-- straza-cla-status -->"

// pendingText is the status description while a run is in progress.
const pendingText = "The contributor agreement check is running. Wait for it to finish."

// limitText is the status description on a commit near GitHub's limit of
// statuses.
const limitText = "This commit reached GitHub's limit of statuses, so the check can no longer judge it. Push a new commit to check again."

// changedText is the status description when the pull request changed while
// a run read it.
const changedText = "This pull request changed while the check ran. The run that the change started decides."

// acceptSentence is the sentence that a contributor posts, as a comment of
// its own, to accept the version.
func acceptSentence(version string) string {
	return "I have read the Straza Contributor License Agreement version " + version + " and I accept it."
}

// confirmMarker ends the confirmation of the acceptance comment id.
func confirmMarker(key string) string { return "<!-- straza-cla-confirm:" + key + " -->" }

// confirmationText is the written confirmation of one acceptance, dated by
// at, when the comment's body became the sentence. It names
// the work and the conditions of use that section 65(4) of the Slovak
// Copyright Act asks a confirmation to contain, and it mentions the
// contributor so that GitHub notifies them.
func confirmationText(c issueComment, at time.Time, version, textURL, sha string) string {
	return fmt.Sprintf("@%s Thank you. SynapTech s. r. o. confirms in writing that on %s you concluded "+
		"the Straza Contributor License Agreement version %s with it, by your comment at %s. "+
		"The text of that version is at %s, and its SHA-256 is %s. Under it you license to "+
		"SynapTech s. r. o. every Contribution you submit to the Project, starting with your commits "+
		"in this pull request, non-exclusively, worldwide and free of charge, unlimited in extent, for "+
		"the whole duration of the economic rights, for every manner of use including those that "+
		"section 19(4) of the Slovak Copyright Act lists, with the right to grant sublicenses and to "+
		"assign the license, on the terms of sections 2 to 11 of the agreement. This comment is the "+
		"written confirmation that section 65(4) of that Act describes.\n\n%s",
		c.User.Login, at.UTC().Format("2006-01-02"), version, c.HTMLURL, textURL, sha, confirmMarker(c.key()))
}

// statusView is what the status comment shows.
type statusView struct {
	Version string
	TextURL string
	Verdict verdict
	Markers []string
	Changes []change
}

// statusBody renders the status comment. Names and emails come from commits
// that anyone can write, so they are shown as code, which GitHub renders
// without mentions, links or markup.
func statusBody(s statusView) string {
	var b strings.Builder
	b.WriteString(statusMarker + "\n### Contributor License Agreement\n\n")
	if s.Verdict.pass() && len(s.Markers) == 0 {
		fmt.Fprintf(&b, "Everyone named in this pull request has accepted the Straza Contributor License Agreement version %s, so the agreement does not hold it back.\n\n", s.Version)
	} else {
		b.WriteString("This pull request cannot merge yet. The lists below say who still has to act, why, and how.\n\n")
	}
	fmt.Fprintf(&b, "Every person who opens a pull request, authors a commit in it or is named in it as a co-author accepts the Straza Contributor License Agreement version %s before it can merge. The text is at %s.\n\n", s.Version, s.TextURL)
	if len(s.Verdict.Gaps) > 0 {
		fmt.Fprintf(&b, "To accept, read the agreement, then post this sentence as a comment of its own on this pull request, copied exactly:\n\n    %s\n\n", acceptSentence(s.Version))
	}
	if len(s.Verdict.Passed) > 0 {
		b.WriteString("#### Accepted\n\n")
		for _, p := range s.Verdict.Passed {
			b.WriteString("- " + passLine(p, s.Version) + "\n")
		}
		b.WriteString("\n")
	}
	if len(s.Verdict.Gaps) > 0 {
		b.WriteString("#### Still to accept\n\n")
		for _, g := range s.Verdict.Gaps {
			b.WriteString("- " + gapLine(g, s.Version) + "\n")
		}
		b.WriteString("\n")
	}
	if len(s.Verdict.Tools) > 0 {
		b.WriteString("#### AI tools\n\nThese commits name an AI coding tool. A tool needs no acceptance, because the person who submitted its output answers for it under section 6 point 4 of the agreement.\n\n")
		for _, u := range s.Verdict.Tools {
			fmt.Fprintf(&b, "- %s in commit %s.\n", code(u.Email), u.Commit)
		}
		b.WriteString("\n")
	}
	if len(s.Markers) > 0 {
		b.WriteString("#### Marked \"Not a Contribution\"\n\nThe agreement excludes material marked with the words \"Not a Contribution\", so this pull request cannot merge while they appear in:\n\n")
		for _, m := range s.Markers {
			b.WriteString("- " + m + ".\n")
		}
		b.WriteString("\nRemove the words, or remove the marked material, and the check runs again.\n\n")
	}
	if len(s.Changes) > 0 {
		b.WriteString("#### Acceptances changed after they were recorded\n\n")
		for _, c := range s.Changes {
			how := "edited"
			if c.Deleted {
				how = "deleted"
			}
			fmt.Fprintf(&b, "- The acceptance by @%s at %s was %s after it was posted. It stays recorded, because it concluded the agreement when it was posted.\n", c.Login, c.URL, how)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func passLine(p pass, version string) string {
	switch p.Kind {
	case passOpener:
		return fmt.Sprintf("@%s opened this pull request and has accepted version %s.", p.Account.Login, version)
	case passAllowlist:
		return fmt.Sprintf("@%s opened this pull request and is on the check's list of project accounts, which need no acceptance.", p.Account.Login)
	case passCarried:
		return fmt.Sprintf("@%s is named as a co-author in commit %s, which the maintainer carried over, and has accepted version %s.", p.Account.Login, p.Commit, version)
	default:
		return fmt.Sprintf("@%s is named in commit %s and accepted version %s on this pull request.", p.Account.Login, p.Commit, version)
	}
}

func gapLine(g gap, version string) string {
	switch g.Kind {
	case gapOpener:
		return fmt.Sprintf("@%s opened this pull request and has not accepted version %s yet. Post the sentence above as a comment on this pull request.", g.Account.Login, version)
	case gapCarried:
		return fmt.Sprintf("@%s is named as a co-author in commit %s and has not accepted version %s. Post the sentence above on this pull request.", g.Account.Login, g.Commit, version)
	case gapGhost:
		return "This pull request was opened by an account that has since been deleted, so nobody can accept the agreement for it. Close it, and open a new pull request from a live account."
	case gapUnlinked:
		return fmt.Sprintf("Commit %s names %s, an email that is not added to any GitHub account, so the check cannot tell who wrote it. Add the email to your GitHub account under Settings, Emails, then post the sentence above. You can also rewrite the commit with an email that is on your account.", g.Commit, code(g.Name+" <"+g.Email+">"))
	default:
		return fmt.Sprintf("@%s is named in commit %s and has not posted the sentence on this pull request. An acceptance on an earlier pull request does not count here, because anyone can put another person's email in a commit. Post the sentence above on this pull request.", g.Account.Login, g.Commit)
	}
}

// code shows untrusted text as inline code on one line.
func code(s string) string {
	s = strings.NewReplacer("`", "'", "\r", " ", "\n", " ").Replace(s)
	return "`" + s + "`"
}

// verdictStatus returns the commit status state and description of a
// finished run.
func verdictStatus(v verdict, markers []string, version string) (string, string) {
	switch {
	case len(markers) > 0:
		return "failure", "This pull request carries the words Not a Contribution. The check's comment says where and what to do."
	case !v.pass():
		return "failure", "Someone named in this pull request has not accepted the agreement. The check's comment says who and how."
	default:
		return "success", "Everyone named in this pull request has accepted the contributor agreement version " + version + "."
	}
}

// statusLine turns an error into a status description: its first sentence,
// capitalized, cut to fit.
func statusLine(err error) string {
	s := err.Error()
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i]
	}
	if s != "" {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return clip(strings.TrimSuffix(s, ".") + ".")
}

// maxDescription is the longest status description GitHub takes.
const maxDescription = 140

// clip cuts s to fit a status description, ending with "..." when it cuts.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxDescription {
		return s
	}
	return string([]rune(s)[:maxDescription-3]) + "..."
}
