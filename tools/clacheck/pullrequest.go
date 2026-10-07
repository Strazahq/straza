package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// statusContext is the commit status the rulesets require.
const statusContext = "straza/cla"

// botID is the numeric id of github-actions[bot], the account that the
// workflow's token comments as. Only its comments count as the check's own.
const botID = 41898282

func (r *runner) repoPath(format string, a ...any) string {
	return "/repos/" + r.cfg.Repo + fmt.Sprintf(format, a...)
}

// again is the sentence that ends an error about the pull request's own
// repository.
func (r *runner) again(n int) string {
	return fmt.Sprintf("Run the cla workflow by hand for pull request #%d once GitHub answers again", n)
}

func (r *runner) pull(n int) (pullRequest, error) {
	var pr pullRequest
	data, _, err := r.gh.do("GET", r.repoPath("/pulls/%d", n), jsonAccept, nil)
	if statusOf(err) == 404 {
		return pr, fmt.Errorf("the check could not find pull request #%d in %s. %v. Check that the number names a pull request there, then run the cla workflow by hand for it", n, r.cfg.Repo, err)
	}
	if err != nil {
		return pr, fmt.Errorf("the check could not read pull request #%d of %s. %v. %s", n, r.cfg.Repo, err, r.again(n))
	}
	if err := json.Unmarshal(data, &pr); err != nil || pr.Number != n || !shaRE.MatchString(pr.Head.SHA) {
		return pr, fmt.Errorf("GitHub's answer for pull request #%d is not a pull request with a head commit. %s", n, r.again(n))
	}
	return pr, nil
}

// pages calls fn with every item of a list, following the pages to the end.
func (r *runner) pages(path, what string, n int, fn func(json.RawMessage) error) error {
	for next := path; next != ""; {
		data, hdr, err := r.gh.do("GET", next, jsonAccept, nil)
		if err != nil {
			return fmt.Errorf("the check could not list the %s of pull request #%d. %v. %s", what, n, err, r.again(n))
		}
		var page []json.RawMessage
		if err := json.Unmarshal(data, &page); err != nil {
			return fmt.Errorf("GitHub's list of the %s of pull request #%d is not the expected JSON. %v. %s", what, n, err, r.again(n))
		}
		for _, raw := range page {
			if err := fn(raw); err != nil {
				return err
			}
		}
		next = linkRel(hdr.Get("Link"), "next")
	}
	return nil
}

// list reads every comment of one kind from path.
func (r *runner) list(path, kind string, n int) ([]issueComment, error) {
	var all []issueComment
	err := r.pages(path, kindWords(kind)+"s", n, func(raw json.RawMessage) error {
		c, err := parseComment(raw, kind)
		all = append(all, c)
		return err
	})
	return all, err
}

// comments reads the whole history of the pull request: the conversation,
// which also holds the check's own comments, and then every review comment
// and review.
func (r *runner) comments(n int) (conv, all []issueComment, err error) {
	if conv, err = r.list(r.repoPath("/issues/%d/comments?per_page=100", n), kindComment, n); err != nil {
		return nil, nil, err
	}
	lines, err := r.list(r.repoPath("/pulls/%d/comments?per_page=100", n), kindReviewComment, n)
	if err != nil {
		return nil, nil, err
	}
	reviews, err := r.list(r.repoPath("/pulls/%d/reviews?per_page=100", n), kindReview, n)
	if err != nil {
		return nil, nil, err
	}
	all = append(append(append(all, conv...), lines...), reviews...)
	return conv, all, nil
}

// histories reads GitHub's edit history of the comments, a hundred comments
// at a time, and pages each history to its oldest revision.
func (r *runner) histories(cs []issueComment, n int) (map[string]*history, error) {
	fail := func(err error) error {
		return fmt.Errorf("the check could not read the edit history of comments on pull request #%d, so it cannot tell whether an edited comment once held the sentence. %v. %s", n, err, r.again(n))
	}
	out := map[string]*history{}
	for from := 0; from < len(cs); from += 100 {
		var ids []string
		for _, c := range cs[from:min(from+100, len(cs))] {
			ids = append(ids, c.NodeID)
		}
		data, err := r.graphql(historyQuery, map[string]any{"ids": ids})
		var got map[string]*history
		if err == nil {
			got, err = parseHistories(data)
		}
		if err != nil {
			return nil, fail(err)
		}
		for id, h := range got {
			for h.after != "" {
				data, err := r.graphql(historyPageQuery, map[string]any{"id": id, "cursor": h.after})
				var page editsConn
				if err == nil {
					page, err = parseHistoryPage(data)
				}
				if err == nil {
					err = h.add(page)
				}
				if err != nil {
					return nil, fail(err)
				}
			}
			if err := h.finish(); err != nil {
				return nil, fail(err)
			}
			out[id] = h
		}
	}
	return out, nil
}

func (r *runner) postComment(n int, body string) (issueComment, error) {
	data, _, err := r.gh.do("POST", r.repoPath("/issues/%d/comments", n), jsonAccept, map[string]string{"body": body})
	switch s := statusOf(err); {
	case s == 403 || s == 404:
		return issueComment{}, fmt.Errorf("the check could not post a comment on pull request #%d. %v. The workflow's token is not allowed to comment there, so check that .github/workflows/cla.yml grants pull-requests: write, then run the cla workflow by hand for this pull request", n, err)
	case err != nil:
		return issueComment{}, fmt.Errorf("the check could not post a comment on pull request #%d. %v. %s", n, err, r.again(n))
	}
	return parseComment(data, kindComment)
}

func (r *runner) editComment(n int, id int64, body string) error {
	if _, _, err := r.gh.do("PATCH", r.repoPath("/issues/comments/%d", id), jsonAccept, map[string]string{"body": body}); err != nil {
		return fmt.Errorf("the check could not update its comment on pull request #%d. %v. %s", n, err, r.again(n))
	}
	return nil
}

// upsertStatus posts the status comment, or updates the one the check posted
// before when its text changed, and returns its URL.
func (r *runner) upsertStatus(n int, conv []issueComment, body string) (string, error) {
	for _, c := range conv {
		if c.User.ID == botID && strings.HasPrefix(c.Body, statusMarker) {
			if c.Body == body {
				return c.HTMLURL, nil
			}
			return c.HTMLURL, r.editComment(n, c.ID, body)
		}
	}
	c, err := r.postComment(n, body)
	return c.HTMLURL, err
}

// statusGuard is the count of statuses on a commit from which the check
// writes only failure. GitHub keeps at most 1000 statuses for one commit and
// context and refuses the rest, so the last status written stands. Spending
// the last slots on a failure means that no earlier success can stand once a
// run can no longer write its verdict. The margin of 100 is wider than the
// writes other jobs can land between one run's count and its write.
const statusGuard = 900

// limitError is the error of a run that met a commit near GitHub's limit of
// statuses and has set straza/cla to failure on it.
type limitError struct {
	sha   string
	count int
}

func (e *limitError) Error() string {
	return fmt.Sprintf("commit %s has reached GitHub's limit of statuses: it holds %d, and GitHub keeps at most 1000 for one commit. The check set straza/cla to failure and can no longer judge this commit, because a later verdict might not be written. Push a new commit to the pull request, which starts a new count, and the check runs again on it", shortOID(e.sha), e.count)
}

// setStatus writes the status on the commit, or the limit failure once the
// commit holds statusGuard statuses, and then returns a limitError. A failure
// is always safe to write, so it never waits on the count.
func (r *runner) setStatus(sha, state, desc, target string) error {
	if state == "failure" {
		return r.postStatus(sha, state, desc, target)
	}
	n, err := r.statusCount(sha)
	if err != nil {
		return err
	}
	if n >= statusGuard {
		if err := r.postStatus(sha, "failure", limitText, target); err != nil {
			return err
		}
		return &limitError{sha: sha, count: n}
	}
	return r.postStatus(sha, state, desc, target)
}

// statusCount returns how many statuses the commit holds, in every context,
// which is never less than the count of straza/cla alone. It reads one status
// per page and takes the number of the last page.
func (r *runner) statusCount(sha string) (int, error) {
	data, hdr, err := r.gh.do("GET", r.repoPath("/commits/%s/statuses?per_page=1", sha), jsonAccept, nil)
	if err != nil {
		return 0, fmt.Errorf("the check could not count the statuses on commit %s, so it cannot tell whether a new one would stand. %v. Run the cla workflow by hand for this pull request once GitHub answers again", shortOID(sha), err)
	}
	if last := linkRel(hdr.Get("Link"), "last"); last != "" {
		if u, err := url.Parse(last); err == nil {
			if n, err := strconv.Atoi(u.Query().Get("page")); err == nil {
				return n, nil
			}
		}
		return 0, fmt.Errorf("GitHub's paging link for the statuses on commit %s names no page, so the check cannot count them. Run the cla workflow by hand for this pull request, and look into it if it repeats", shortOID(sha))
	}
	var page []json.RawMessage
	if err := json.Unmarshal(data, &page); err != nil {
		return 0, fmt.Errorf("GitHub's list of the statuses on commit %s is not the expected JSON, so the check cannot count them. %v. Run the cla workflow by hand for this pull request once GitHub answers again", shortOID(sha), err)
	}
	return len(page), nil
}

func (r *runner) postStatus(sha, state, desc, target string) error {
	in := map[string]string{"state": state, "context": statusContext, "description": clip(desc)}
	if target != "" {
		in["target_url"] = target
	}
	_, _, err := r.gh.do("POST", r.repoPath("/statuses/%s", sha), jsonAccept, in)
	switch s := statusOf(err); {
	case s == 422:
		return fmt.Errorf("the check could not set the status %s on commit %s, because the commit holds the 1000 statuses that GitHub keeps for one commit. %v. The status that stands may be out of date, so do not merge this commit. Push a new commit to the pull request, which starts a new count, and the check runs again on it", statusContext, shortOID(sha), err)
	case s == 403 || s == 404:
		return fmt.Errorf("the check could not set the status %s to %s on commit %s. %v. Check that .github/workflows/cla.yml grants statuses: write, then run the workflow by hand for this pull request", statusContext, state, shortOID(sha), err)
	case err != nil:
		return fmt.Errorf("the check could not set the status %s to %s on commit %s. %v. Run the cla workflow by hand for this pull request once GitHub answers again", statusContext, state, shortOID(sha), err)
	}
	fmt.Fprintf(r.out, "Status %s on commit %s: %s. %s\n", statusContext, shortOID(sha), state, in["description"])
	return nil
}

func (r *runner) lock(n int) error {
	if _, _, err := r.gh.do("PUT", r.repoPath("/issues/%d/lock", n), jsonAccept, map[string]string{"lock_reason": "resolved"}); err != nil {
		return fmt.Errorf("the check could not lock the conversation of the closed pull request #%d. %v. Lock it by hand, or run the cla workflow by hand for it once GitHub answers again", n, err)
	}
	fmt.Fprintf(r.out, "Locked the conversation of the closed pull request #%d.\n", n)
	return nil
}

func (r *runner) graphql(query string, vars map[string]any) ([]byte, error) {
	data, _, err := r.gh.do("POST", r.cfg.GraphQLURL, jsonAccept, map[string]any{"query": query, "variables": vars})
	return data, err
}

// commits reads every commit of the pull request with all its authors and
// co-authors through the GraphQL API, following the pages of commits and,
// for a commit with more than a hundred, the pages of its authors.
func (r *runner) commits(n int) ([]commit, error) {
	owner, name, _ := strings.Cut(r.cfg.Repo, "/")
	fail := func(err error) error {
		return fmt.Errorf("the check could not read the commits of pull request #%d, so it cannot tell who wrote them. %v. %s", n, err, r.again(n))
	}
	var all []commit
	for cursor, first := "", true; first || cursor != ""; first = false {
		vars := map[string]any{"owner": owner, "name": name, "number": n}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := r.graphql(commitsQuery, vars)
		var page []commit
		if err == nil {
			page, cursor, err = parseCommits(data)
		}
		if err != nil {
			return nil, fail(err)
		}
		all = append(all, page...)
	}
	for i := range all {
		for all[i].authorsAfter != "" {
			data, err := r.graphql(authorsQuery, map[string]any{"owner": owner, "name": name, "oid": all[i].OID, "cursor": all[i].authorsAfter})
			var more []actor
			if err == nil {
				more, all[i].authorsAfter, err = parseAuthors(data)
			}
			if err != nil {
				return nil, fail(err)
			}
			all[i].Authors = append(all[i].Authors, more...)
		}
	}
	return all, nil
}

// diff returns the pull request's diff. GitHub refuses the diff of a very
// large pull request, and then the check cannot read the added lines.
func (r *runner) diff(n int) (string, error) {
	data, _, err := r.gh.do("GET", r.repoPath("/pulls/%d", n), diffAccept, nil)
	if s := statusOf(err); s == 406 || s == 422 {
		return "", fmt.Errorf("GitHub will not return the diff of pull request #%d, so the check cannot look for the words \"Not a Contribution\" in its added lines. %v. Split it into smaller pull requests", n, err)
	}
	if err != nil {
		return "", fmt.Errorf("the check could not read the diff of pull request #%d. %v. %s", n, err, r.again(n))
	}
	return string(data), nil
}

// sharedHead refuses success when another open pull request has the same
// head commit. A commit status belongs to the commit, so the two pull
// requests would share one verdict, and the other one may name people this
// run never judged.
func (r *runner) sharedHead(pr pullRequest) error {
	return r.pages(r.repoPath("/pulls?state=open&per_page=100"), "open pull requests", pr.Number, func(raw json.RawMessage) error {
		var o pullRequest
		if err := json.Unmarshal(raw, &o); err == nil && o.Number != pr.Number && o.Head.SHA == pr.Head.SHA {
			return fmt.Errorf("pull request #%d has the same head commit as open pull request #%d, and a commit status cannot tell the two apart, so neither can pass. Close one of them, or push a new commit to one, then run the cla workflow by hand for both", pr.Number, o.Number)
		}
		return nil
	})
}

// changedDuringRun reports whether the pull request's head, base branch,
// title or description changed, or a conversation comment by someone other than the
// check was posted or edited, after this run read them. The run that such a
// change started decides then, so this one must not leave a verdict on it.
func (r *runner) changedDuringRun(pr pullRequest, conv []issueComment, start time.Time) (bool, error) {
	now, err := r.pull(pr.Number)
	if err != nil {
		return false, err
	}
	if now.Head.SHA != pr.Head.SHA || now.Base.Ref != pr.Base.Ref || now.Title != pr.Title || now.Body != pr.Body {
		return true, nil
	}
	seen := map[int64]string{}
	for _, c := range conv {
		seen[c.ID] = c.Body
	}
	since := url.QueryEscape(start.Add(-5 * time.Minute).UTC().Format(time.RFC3339))
	fresh, err := r.list(r.repoPath("/issues/%d/comments?per_page=100&since=%s", pr.Number, since), kindComment, pr.Number)
	if err != nil {
		return false, err
	}
	for _, c := range fresh {
		if body, ok := seen[c.ID]; c.User.ID != botID && (!ok || body != c.Body) {
			return true, nil
		}
	}
	return false, nil
}
