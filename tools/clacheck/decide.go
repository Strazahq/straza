package main

import "strings"

// aiTools lists the commit emails of AI coding tools. An author or co-author
// with one of these emails is recorded as a tool, not as a person, and needs
// no acceptance, because section 6 point 4 of the agreement makes the person
// who submits the output answer for it. Add an address with a test row in
// TestDecide when a new tool shows up.
var aiTools = []string{"noreply@anthropic.com"}

// account is a GitHub account named by its permanent numeric id.
type account struct {
	ID    int64
	Login string
}

// actor is one author or co-author of a commit as GitHub's GraphQL API lists
// it. ID and Login are empty when GitHub links the email to no account.
type actor struct {
	Name  string
	Email string
	ID    int64
	Login string
}

// commit is one commit of the pull request with its authors, the git author
// first and then the co-authors that GitHub read from the message trailers.
type commit struct {
	OID     string
	Message string
	Authors []actor
	// authorsAfter is the cursor of the authors past the first page, which
	// the check reads before it decides.
	authorsAfter string
}

// verdictInput holds everything the decision reads: the account that opened
// the pull request, its commits, the accounts that have an account file for
// the version, the accounts that posted the sentence on this pull request,
// the tool emails and the allowlisted account ids.
type verdictInput struct {
	Opener       account
	Commits      []commit
	HasAccount   map[int64]bool
	AcceptedHere map[int64]bool
	Tools        []string
	Allow        map[int64]bool
}

// passKind says why an account needs nothing more.
type passKind int

const (
	passOpener    passKind = iota // opened the pull request and has an account file
	passAllowlist                 // opened the pull request and is on the allowlist
	passHere                      // posted the sentence on this pull request and has an account file
	passCarried                   // co-author of a commit that an allowlisted account authored
)

// gapKind says why an account or an email still has to act.
type gapKind int

const (
	gapOpener   gapKind = iota // opened the pull request and has no account file
	gapHere                    // named in a commit and has not posted the sentence here
	gapCarried                 // co-author of a carried commit without an account file
	gapUnlinked                // an email that GitHub links to no account
	gapGhost                   // the opener's account was deleted
)

// ghostID is the account GitHub shows for a deleted account, which nobody can
// act for.
const ghostID = 10137

// pass is an account that needs nothing more, with the first commit that
// names it when a commit brought it in.
type pass struct {
	Account account
	Kind    passKind
	Commit  string
}

// gap is an account or an unlinked email that still has to act, with the
// first commit that names it.
type gap struct {
	Account account
	Name    string
	Email   string
	Commit  string
	Kind    gapKind
}

// toolUse is an AI tool email found in a commit.
type toolUse struct {
	Email  string
	Commit string
}

// verdict is the result of decide.
type verdict struct {
	Passed []pass
	Gaps   []gap
	Tools  []toolUse
}

// pass reports whether nobody has to act any more.
func (v verdict) pass() bool { return len(v.Gaps) == 0 }

// decide applies the rules of the agreement check to one pull request. The
// opener passes with an account file or through the allowlist. Every other
// author and co-author is judged by the account GitHub links to the email,
// never by the email or the name alone: a linked account other than the
// opener passes only when it posted the sentence on this pull request and
// has an account file, because a commit email is not authenticated. On a
// pull request an allowlisted account opened, a co-author of a commit that
// account authored passes with an account file, because the maintainer wrote
// that line when he carried the work over. Anywhere else such a commit may
// carry a borrowed email, so it gets no carry-over. An email linked to no
// account, or to the deleted account, fails. Signed-off-by lines are never
// read.
func decide(in verdictInput) verdict {
	var v verdict
	passed := map[int64]bool{}
	gapped := map[string]bool{}
	tools := map[string]bool{}
	addPass := func(p pass) {
		if !passed[p.Account.ID] {
			passed[p.Account.ID] = true
			v.Passed = append(v.Passed, p)
		}
	}
	addGap := func(g gap) {
		key := "id:" + itoa(g.Account.ID)
		if g.Kind == gapUnlinked {
			key = "email:" + strings.ToLower(g.Email)
		}
		if !gapped[key] {
			gapped[key] = true
			v.Gaps = append(v.Gaps, g)
		}
	}

	has := func(id int64) bool { return id != ghostID && in.HasAccount[id] }
	switch {
	case in.Opener.ID == ghostID:
		addGap(gap{Account: in.Opener, Kind: gapGhost})
	case has(in.Opener.ID):
		addPass(pass{Account: in.Opener, Kind: passOpener})
	case in.Allow[in.Opener.ID]:
		addPass(pass{Account: in.Opener, Kind: passAllowlist})
	default:
		addGap(gap{Account: in.Opener, Kind: gapOpener})
	}

	for _, c := range in.Commits {
		short := shortOID(c.OID)
		carried := in.Allow[in.Opener.ID] && len(c.Authors) > 0 && c.Authors[0].ID != 0 && in.Allow[c.Authors[0].ID]
		for i, a := range c.Authors {
			acct := account{ID: a.ID, Login: a.Login}
			switch {
			case isTool(a.Email, in.Tools):
				if key := strings.ToLower(strings.TrimSpace(a.Email)); !tools[key] {
					tools[key] = true
					v.Tools = append(v.Tools, toolUse{Email: a.Email, Commit: short})
				}
			case a.ID == 0 || a.ID == ghostID:
				addGap(gap{Name: a.Name, Email: a.Email, Commit: short, Kind: gapUnlinked})
			case a.ID == in.Opener.ID:
				// The opener's own entry decides.
			case i > 0 && carried:
				if has(a.ID) {
					addPass(pass{Account: acct, Kind: passCarried, Commit: short})
				} else {
					addGap(gap{Account: acct, Commit: short, Kind: gapCarried})
				}
			case in.AcceptedHere[a.ID] && has(a.ID):
				addPass(pass{Account: acct, Kind: passHere, Commit: short})
			default:
				addGap(gap{Account: acct, Commit: short, Kind: gapHere})
			}
		}
	}

	kept := v.Passed[:0]
	for _, p := range v.Passed {
		if !gapped["id:"+itoa(p.Account.ID)] {
			kept = append(kept, p)
		}
	}
	v.Passed = kept
	return v
}

// isTool reports whether email is on the tool list, without regard to case.
func isTool(email string, tools []string) bool {
	email = strings.TrimSpace(email)
	for _, t := range tools {
		if strings.EqualFold(email, t) {
			return true
		}
	}
	return false
}

// shortOID is the seven-character commit id that GitHub shows.
func shortOID(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}
