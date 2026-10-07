package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	jsonAccept = "application/vnd.github+json"
	rawAccept  = "application/vnd.github.raw+json"
	diffAccept = "application/vnd.github.diff"
	// maxBody caps one answer from GitHub. A larger answer is an error, never
	// a silent cut, because a cut diff could hide a marked line.
	maxBody = 64 << 20
)

// client calls one GitHub API root with one token. The check holds two: one
// with the workflow's token for the pull request's repository and one with
// the records token for the records repository.
type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(base, token string) *client {
	return &client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// apiError is an answer outside 2xx. It carries the method, the path and
// GitHub's message, never a token. RateLimited is set when GitHub refused
// because the token used up its rate limit, which resets at Reset.
type apiError struct {
	Method      string
	Path        string
	Status      int
	Message     string
	RateLimited bool
	Reset       time.Time
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("GitHub answered %d to %s %s", e.Status, e.Method, e.Path)
	if e.Message != "" {
		msg += " (" + e.Message + ")"
	}
	if e.RateLimited {
		msg += ", because the token reached GitHub's rate limit"
		if !e.Reset.IsZero() {
			msg += ", which resets at " + e.Reset.Format("15:04 UTC")
		}
	}
	return msg
}

// rateLimited reports whether err is an answer that refused a used-up rate limit.
func rateLimited(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.RateLimited
}

// statusOf returns the HTTP status of an apiError, or 0 for any other error.
func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// do sends one request to target, which is a path under the client's root or
// a full URL under it, and returns the body and the headers of a 2xx answer.
func (c *client) do(method, target, accept string, in any) ([]byte, http.Header, error) {
	if !strings.HasPrefix(target, "http") {
		target = c.base + target
	}
	if !strings.HasPrefix(target, c.base+"/") && target != c.base {
		return nil, nil, fmt.Errorf("the check refused to send its token to %s, which is outside %s, so it stops. GitHub's paging link pointed elsewhere. Run the cla workflow again, and look into it if it repeats", target, c.base)
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "straza-clacheck")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s did not reach GitHub: %w", method, pathOf(target, c.base), err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: reading the answer failed: %w", method, pathOf(target, c.base), err)
	}
	if len(data) > maxBody {
		return nil, nil, fmt.Errorf("%s %s: the answer is larger than %d MiB", method, pathOf(target, c.base), maxBody>>20)
	}
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &m)
		ae := &apiError{Method: method, Path: pathOf(target, c.base), Status: resp.StatusCode, Message: m.Message}
		if resp.StatusCode == http.StatusTooManyRequests || resp.Header.Get("X-RateLimit-Remaining") == "0" ||
			strings.Contains(strings.ToLower(m.Message), "rate limit") {
			ae.RateLimited = true
			if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				ae.Reset = time.Unix(n, 0).UTC()
			}
		}
		return nil, nil, ae
	}
	return data, resp.Header, nil
}

func pathOf(target, base string) string { return strings.TrimPrefix(target, base) }

// linkRel returns the URL of a Link header with the relation rel, such as
// "next" or "last", or "".
func linkRel(header, rel string) string {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			if strings.TrimSpace(f) == `rel="`+rel+`"` {
				return strings.Trim(strings.TrimSpace(fields[0]), "<>")
			}
		}
	}
	return ""
}

// The kinds of comment the check reads.
const (
	kindComment       = "comment"
	kindReviewComment = "review-comment"
	kindReview        = "review"
)

// issueComment is a conversation comment, a review comment on a line of the
// diff, or a review, as Kind says. Raw keeps the full object GitHub returned,
// which the records store unchanged.
type issueComment struct {
	Kind        string    `json:"-"`
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"user"`
	Raw json.RawMessage `json:"-"`
}

// key names the comment in the records and in the confirmation marker. A
// conversation comment keeps its bare id, as the records layout names it,
// and the two other kinds carry their kind, because their ids come from
// other sequences.
func (c issueComment) key() string {
	if c.Kind == kindReview || c.Kind == kindReviewComment {
		return c.Kind + "-" + itoa(c.ID)
	}
	return itoa(c.ID)
}

// kindOfKey returns the kind of comment that a records key names.
func kindOfKey(key string) string {
	switch {
	case strings.HasPrefix(key, kindReviewComment+"-"):
		return kindReviewComment
	case strings.HasPrefix(key, kindReview+"-"):
		return kindReview
	default:
		return kindComment
	}
}

// parseComment reads one object of the kind. A review has no created_at, so
// its submitted_at stands in, and a comment that was never changed has
// updated_at equal to created_at.
func parseComment(raw json.RawMessage, kind string) (issueComment, error) {
	c := issueComment{Kind: kind}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("a comment from GitHub is not the expected JSON, so the check cannot tell who wrote it. %w. Run the cla workflow again for this pull request", err)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = c.SubmittedAt
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	c.Raw = append(json.RawMessage(nil), raw...)
	return c, nil
}

// pullRequest is the part of GitHub's pull request object that the check reads.
type pullRequest struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	Locked  bool   `json:"locked"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Commits int    `json:"commits"`
	User    struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// commitsQuery reads a page of the pull request's commits with every author
// and co-author and the account GitHub links to each email.
const commitsQuery = `query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      commits(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes { commit { oid message authors(first: 100) { ` + authorsFields + ` } } }
      }
    }
  }
}`

// authorsQuery reads the further authors of one commit.
const authorsQuery = `query($owner: String!, $name: String!, $oid: GitObjectID!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    object(oid: $oid) { ... on Commit { authors(first: 100, after: $cursor) { ` + authorsFields + ` } } }
  }
}`

const authorsFields = `pageInfo { hasNextPage endCursor } nodes { name email user { databaseId login } }`

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// next is the cursor of the next page, or "" on the last page.
func (p pageInfo) next() string {
	if p.HasNextPage {
		return p.EndCursor
	}
	return ""
}

type authorsConn struct {
	PageInfo pageInfo `json:"pageInfo"`
	Nodes    []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		User  *struct {
			DatabaseID int64  `json:"databaseId"`
			Login      string `json:"login"`
		} `json:"user"`
	} `json:"nodes"`
}

func (a authorsConn) actors() []actor {
	var out []actor
	for _, n := range a.Nodes {
		act := actor{Name: n.Name, Email: n.Email}
		if n.User != nil {
			act.ID, act.Login = n.User.DatabaseID, n.User.Login
		}
		out = append(out, act)
	}
	return out
}

type graphqlErrors []struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// parseCommits turns one GraphQL answer into commits and the cursor of the
// next page, which is empty on the last page. A commit whose authors go on
// past the first page carries the cursor of the rest in authorsAfter.
func parseCommits(data []byte) ([]commit, string, error) {
	var p struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					Commits struct {
						PageInfo pageInfo `json:"pageInfo"`
						Nodes    []struct {
							Commit struct {
								OID     string      `json:"oid"`
								Message string      `json:"message"`
								Authors authorsConn `json:"authors"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors graphqlErrors `json:"errors"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, "", fmt.Errorf("GitHub's list of commits is not the expected JSON: %w", err)
	}
	if len(p.Errors) > 0 {
		return nil, "", fmt.Errorf("GitHub refused the query for the commits: %s", p.Errors[0].Message)
	}
	pr := p.Data.Repository.PullRequest
	if pr == nil {
		return nil, "", errors.New("GitHub returned no pull request for the query for the commits")
	}
	var out []commit
	for _, n := range pr.Commits.Nodes {
		c := n.Commit
		out = append(out, commit{OID: c.OID, Message: c.Message, Authors: c.Authors.actors(), authorsAfter: c.Authors.PageInfo.next()})
	}
	return out, pr.Commits.PageInfo.next(), nil
}

// parseAuthors turns an answer to authorsQuery into authors and the cursor
// of the next page.
func parseAuthors(data []byte) ([]actor, string, error) {
	var p struct {
		Data struct {
			Repository struct {
				Object *struct {
					Authors authorsConn `json:"authors"`
				} `json:"object"`
			} `json:"repository"`
		} `json:"data"`
		Errors graphqlErrors `json:"errors"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, "", fmt.Errorf("GitHub's list of a commit's authors is not the expected JSON: %w", err)
	}
	if len(p.Errors) > 0 || p.Data.Repository.Object == nil {
		return nil, "", fmt.Errorf("GitHub refused the query for a commit's further authors: %v", p.Errors)
	}
	a := p.Data.Repository.Object.Authors
	return a.actors(), a.PageInfo.next(), nil
}

// historyQuery reads GitHub's edit history of comments, reviews and review
// comments by their node ids. Each revision's diff holds the whole body of
// that revision, editedAt is when the body became it, and editor is the
// account that wrote it. Revisions come newest first, and the oldest is the
// body as first posted, written by the author (verified against the live API).
const historyQuery = `query($ids: [ID!]!) {
  nodes(ids: $ids) {
    ... on Node { id }
    ... on Comment { lastEditedAt userContentEdits(first: 100) { ` + editFields + ` } }
  }
}`

// historyPageQuery reads the older revisions of one comment.
const historyPageQuery = `query($id: ID!, $cursor: String) {
  node(id: $id) { ... on Comment { userContentEdits(first: 100, after: $cursor) { ` + editFields + ` } } }
}`

const editFields = `pageInfo { hasNextPage endCursor } nodes { editedAt diff editor { login ... on User { databaseId } ... on Bot { databaseId } } }`

// revision is one body a comment had, when it got it and who wrote it.
type revision struct {
	EditedAt time.Time `json:"editedAt"`
	Diff     *string   `json:"diff"`
	Editor   *struct {
		Login      string `json:"login"`
		DatabaseID int64  `json:"databaseId"`
	} `json:"editor"`
}

// history is the edit history of one comment, oldest revision first. Raw
// holds every revision as GitHub returned it, for the records.
type history struct {
	Edited    bool
	Revisions []revision
	Raw       json.RawMessage
	after     string
	raws      []json.RawMessage
}

type editsConn struct {
	PageInfo pageInfo          `json:"pageInfo"`
	Nodes    []json.RawMessage `json:"nodes"`
}

// add takes one page of revisions into h.
func (h *history) add(page editsConn) error {
	for _, raw := range page.Nodes {
		var v revision
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("GitHub's edit history is not the expected JSON: %w", err)
		}
		h.Revisions = append(h.Revisions, v)
		h.raws = append(h.raws, raw)
	}
	h.after = page.PageInfo.next()
	return nil
}

// finish orders the revisions oldest first and keeps GitHub's copy of them.
func (h *history) finish() error {
	sort.SliceStable(h.Revisions, func(i, j int) bool { return h.Revisions[i].EditedAt.Before(h.Revisions[j].EditedAt) })
	raw, err := json.Marshal(h.raws)
	h.Raw = raw
	return err
}

// parseHistories turns an answer to historyQuery into histories by node id,
// each with the cursor of its older revisions when there are more. A node
// GitHub no longer finds, because the comment was deleted meanwhile, is left
// out.
func parseHistories(data []byte) (map[string]*history, error) {
	var p struct {
		Data struct {
			Nodes []*struct {
				ID           string     `json:"id"`
				LastEditedAt *time.Time `json:"lastEditedAt"`
				Edits        *editsConn `json:"userContentEdits"`
			} `json:"nodes"`
		} `json:"data"`
		Errors graphqlErrors `json:"errors"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("GitHub's edit history is not the expected JSON: %w", err)
	}
	for _, e := range p.Errors {
		if e.Type != "NOT_FOUND" {
			return nil, fmt.Errorf("GitHub refused the query for the edit history: %s", e.Message)
		}
	}
	out := map[string]*history{}
	for _, n := range p.Data.Nodes {
		if n == nil {
			continue
		}
		h := &history{Edited: n.LastEditedAt != nil}
		if n.Edits != nil {
			if err := h.add(*n.Edits); err != nil {
				return nil, err
			}
		}
		out[n.ID] = h
	}
	return out, nil
}

// parseHistoryPage turns an answer to historyPageQuery into its page.
func parseHistoryPage(data []byte) (editsConn, error) {
	var p struct {
		Data struct {
			Node *struct {
				Edits editsConn `json:"userContentEdits"`
			} `json:"node"`
		} `json:"data"`
		Errors graphqlErrors `json:"errors"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return editsConn{}, fmt.Errorf("GitHub's edit history is not the expected JSON: %w", err)
	}
	if len(p.Errors) > 0 || p.Data.Node == nil {
		return editsConn{}, fmt.Errorf("GitHub refused the query for older revisions: %v", p.Errors)
	}
	return p.Data.Node.Edits, nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
