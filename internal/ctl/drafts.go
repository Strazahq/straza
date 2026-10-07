package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DraftBody is the body of createDraft and checkDraft: one YAML stream of
// App, Role, PolicySet and Removal documents for each file, which the server
// splits, and the proposer's note.
type DraftBody struct {
	Documents []string `json:"documents"`
	Note      string   `json:"note,omitempty"`
}

// DraftUpdate is the body of updateDraft: the revision the change is made
// on, the whole new document list, and the note when the caller gives one,
// an empty note included. A nil Note keeps the draft's note.
type DraftUpdate struct {
	Revision  int      `json:"revision"`
	Documents []string `json:"documents"`
	Note      *string  `json:"note,omitempty"`
}

// DraftPublish is the body of publishDraft: the revision and risk digest the
// publisher reviewed, the key of every risk acknowledged, tick and typed, and
// the text typed for each typed risk by key. Every key is a finding's own
// key as the server answered it.
type DraftPublish struct {
	Revision   int               `json:"revision"`
	RiskDigest string            `json:"risk_digest"`
	Ticked     []string          `json:"ticked"`
	Typed      map[string]string `json:"typed"`
}

// DraftItem is one object of a draft as the drafts routes answer it.
// Existed says whether the object existed at the draft's last check, which
// every reader gets. The fingerprint beside it goes only to a reader who
// may read the object, so strazactl does not read it.
type DraftItem struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Op      string `json:"op"`
	Doc     string `json:"doc"`
	Existed bool   `json:"existed"`
}

// Object is the item's Kind/Name, the key the verdict and the live block use.
func (it DraftItem) Object() string { return it.Kind + "/" + it.Name }

// DraftPrincipal is who wrote a revision of a draft or decided it. Agent
// marks a user who is not a person, and Sponsor names an agent's sponsor.
type DraftPrincipal struct {
	Username string `json:"username"`
	Agent    bool   `json:"agent"`
	Sponsor  string `json:"sponsor"`
}

// Draft is a draft as the drafts routes answer it. A decided draft carries
// who decided it, when, and the reason given.
type Draft struct {
	ID            string          `json:"id"`
	Revision      int             `json:"revision"`
	State         string          `json:"state"`
	Door          string          `json:"door"`
	Note          string          `json:"note"`
	Items         []DraftItem     `json:"items"`
	Title         string          `json:"title"`
	CreatedAt     string          `json:"created_at"`
	DecidedAt     string          `json:"decided_at"`
	DecidedBy     *DraftPrincipal `json:"decided_by"`
	DecidedReason string          `json:"decided_reason"`
}

// DraftRevision is who wrote one revision of a draft, through which door
// and when.
type DraftRevision struct {
	Revision  int            `json:"revision"`
	Author    DraftPrincipal `json:"author"`
	Door      string         `json:"door"`
	CreatedAt string         `json:"created_at"`
}

// DraftFinding is one line of a verdict. Key is the server's, from before any
// cut for the reader, and a publish echoes it. Before and After are what the
// line is about today and after publishing, in words that carry no value.
// Document is the number of the sent document a refusal of reading it
// names, counted across every text, and 0 when it names none.
type DraftFinding struct {
	Key      string `json:"key"`
	Code     string `json:"code"`
	Class    string `json:"class"`
	Ack      string `json:"ack"`
	Object   string `json:"object"`
	Sentence string `json:"sentence"`
	Fix      string `json:"fix"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Typed    string `json:"typed"`
	Document int    `json:"document"`
}

// DraftGain is one row of who gains what, with the gate in words when the
// server has them. Holders names the holders for a reader the server shows
// role membership to, and is empty for every other reader.
type DraftGain struct {
	Role         string   `json:"role"`
	Server       string   `json:"server"`
	Tool         string   `json:"tool"`
	Holders      []string `json:"holders"`
	HoldersCount int      `json:"holders_count"`
	Before       string   `json:"before"`
	After        string   `json:"after"`
	BeforeWords  string   `json:"before_words"`
	AfterWords   string   `json:"after_words"`
}

// DraftNeed is the standing one object of a draft needs from its publisher,
// in words.
type DraftNeed struct {
	Object   string `json:"object"`
	Standing string `json:"standing"`
}

// DraftVerdict is the server's reading of one revision against live state.
type DraftVerdict struct {
	CheckedAt  string         `json:"checked_at"`
	Refused    []DraftFinding `json:"refused"`
	Risks      []DraftFinding `json:"risks"`
	Warnings   []DraftFinding `json:"warnings"`
	Unchecked  []DraftFinding `json:"unchecked"`
	Passed     []DraftFinding `json:"passed"`
	Info       []DraftFinding `json:"info"`
	Gains      []DraftGain    `json:"gains"`
	Needs      []DraftNeed    `json:"needs"`
	RiskDigest string         `json:"risk_digest"`
}

// DraftAnswer is what create, update and revert answer: the draft and its
// verdict.
type DraftAnswer struct {
	Draft   Draft        `json:"draft"`
	Verdict DraftVerdict `json:"verdict"`
}

// DraftCheckAnswer is what check answers: the items as the server read them
// and their verdict, with nothing stored.
type DraftCheckAnswer struct {
	Items   []DraftItem  `json:"items"`
	Verdict DraftVerdict `json:"verdict"`
}

// DraftLive is the live state of one object a draft names: its op in the
// words of an item's, remove for an object that does not exist, and its
// document with env values and address secrets masked.
type DraftLive struct {
	Op  string `json:"op"`
	Doc string `json:"doc"`
}

// DraftDetail is one draft as GET /v1/admin/drafts/{id} answers it, with a
// verdict computed on the read for an open draft. A draft that is not open
// answers a verdict with no line and, in Checks, the counts of the check
// the server stored for its revision. MayPublish is display only, because
// the publish checks again.
type DraftDetail struct {
	Draft          Draft                `json:"draft"`
	Verdict        DraftVerdict         `json:"verdict"`
	Revisions      []DraftRevision      `json:"revisions"`
	Live           map[string]DraftLive `json:"live"`
	Checks         *DraftChecks         `json:"checks"`
	MayPublish     bool                 `json:"may_publish"`
	PublishRefusal string               `json:"publish_refusal"`
}

// DraftChecks is the counts the server stored with a check, the revision
// they belong to, and when the check ran: on a list row the last check of
// an open draft, and on a read the check of the revision of a draft that
// is not open.
type DraftChecks struct {
	Refused   int    `json:"refused"`
	Risks     int    `json:"risks"`
	Warnings  int    `json:"warnings"`
	Unchecked int    `json:"unchecked"`
	Revision  int    `json:"revision"`
	CheckedAt string `json:"checked_at"`
}

// DraftSummary is one row of the drafts list. Checks is nil on a decided
// draft and before the first check.
type DraftSummary struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	Door      string         `json:"door"`
	Revision  int            `json:"revision"`
	Proposer  DraftPrincipal `json:"proposer"`
	Checks    *DraftChecks   `json:"checks"`
	UpdatedAt string         `json:"updated_at"`
}

// DraftPage is one page of the drafts list. NextCursor is empty on the last
// page.
type DraftPage struct {
	Items      []DraftSummary `json:"items"`
	NextCursor string         `json:"next_cursor"`
}

// DraftServer is one server a publish created, changed or removed, with its
// status after the apply and, in Detail, the start's error or the health
// reason.
type DraftServer struct {
	Name   string `json:"name"`
	Change string `json:"change"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// DraftPublished is what a publish answers.
type DraftPublished struct {
	Draft    Draft         `json:"draft"`
	Snapshot string        `json:"snapshot"`
	Servers  []DraftServer `json:"servers"`
	Next     []string      `json:"next"`
}

// DraftRefused is the body of a 409 or 422 that carries detail: the
// sentence, the findings of a refusal at intake, the verdict when it is the
// reason, and the fields a Check again needs a pick for.
type DraftRefused struct {
	Error     string          `json:"error"`
	Findings  []DraftFinding  `json:"findings"`
	Verdict   *DraftVerdict   `json:"verdict"`
	Conflicts []DraftConflict `json:"conflicts"`
}

// DraftConflict is a field that a draft and live state both changed, to
// different values, since the draft was checked, each value as text.
type DraftConflict struct {
	Object string `json:"object"`
	Field  string `json:"field"`
	Base   string `json:"base"`
	Draft  string `json:"draft"`
	Live   string `json:"live"`
}

// DraftRebase is the body of Check again: the revision read, and the value
// to keep, draft or live, for each field both sides changed, keyed
// "Kind/Name field".
type DraftRebase struct {
	Revision int               `json:"revision"`
	Picks    map[string]string `json:"picks,omitempty"`
}

// DraftContacted is what a contact of a proposed server answers: the host
// Straza dialed, when, the server's name and version, and its tools, all in
// the server's own words.
type DraftContacted struct {
	Object      string `json:"object"`
	Host        string `json:"host"`
	ContactedAt string `json:"contacted_at"`
	Server      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"server"`
	Tools []DraftContactedTool `json:"tools"`
}

// DraftContactedTool is one tool a contacted server listed.
type DraftContactedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
}

// draftsPage is the largest page the drafts list gives, which strazactl asks
// for in one call.
const draftsPage = 200

// publishTimeout bounds a publish, which checks the draft again, writes it in
// one transaction and then waits for every server it starts, each for up to
// 20 seconds, so it can outlast the client's own timeout. A test shortens it.
var publishTimeout = 2 * time.Minute

// draftsCall sends one call of the drafts routes through sendCode, with body
// as JSON or no body when it is nil, and answers the body and the status as
// the server sent them, so a verb can print the verdict or the findings of a
// refusal. A route strazad does not serve is an error, as served says.
func (c *Client) draftsCall(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	buf, code, err := c.sendCode(ctx, method, path, func(bearer string) ([]byte, int, error) {
		return c.doRaw(ctx, method, path, bearer, body)
	})
	return served(method, path, buf, code, err)
}

// served answers the call method path as it came back, unless strazad
// answered 404 or 405 with no sentence of its own, which its router does
// for a route it does not serve: then the error names the route, without
// its query.
func served(method, path string, buf []byte, code int, err error) ([]byte, int, error) {
	if err != nil || code != http.StatusNotFound && code != http.StatusMethodNotAllowed || sentenceOf(buf) != "" {
		return buf, code, err
	}
	route, _, _ := strings.Cut(path, "?")
	return nil, 0, fmt.Errorf("strazad answered HTTP %d with no sentence of its own, which it does when it does not serve %s %s. "+
		"Upgrade strazad, or check that --server or your login points at strazad", code, method, route)
}

// errNoPublishRoute is the answer of CheckPublishRoute on a strazad that
// serves no publish route.
var errNoPublishRoute = errors.New("this strazad serves no publish route yet, so the draft stays open. " +
	"Upgrade strazad to a version with drafts publish, and publish again")

// CheckPublishRoute answers errNoPublishRoute when strazad serves no publish
// route for draft id, so publish can say so before it asks any question. It
// sends a GET of the publish path, which a strazad that serves the route, a
// POST, answers with 405 and one without it with 404, so it never
// publishes. Any other answer counts as served, and the publish decides.
func (c *Client) CheckPublishRoute(ctx context.Context, id string) error {
	path := draftPath(id) + "/publish"
	_, code, err := c.sendCode(ctx, http.MethodGet, path, func(bearer string) ([]byte, int, error) {
		return c.doRaw(ctx, http.MethodGet, path, bearer, nil)
	})
	if err == nil && code == http.StatusNotFound {
		return errNoPublishRoute
	}
	return err
}

// draftPath is the path of the draft id.
func draftPath(id string) string { return "/v1/admin/drafts/" + url.PathEscape(id) }

// CheckDraft checks documents against live state and stores nothing.
func (c *Client) CheckDraft(ctx context.Context, b DraftBody) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodPost, "/v1/admin/drafts/check", b)
}

// CreateDraft stores a new draft of documents with its check. An answer
// that does not say whether the draft was stored, lost after the request
// went out or a 5xx, answers an error that says it may have been, because a
// second create would make a second draft.
func (c *Client) CreateDraft(ctx context.Context, b DraftBody) ([]byte, int, error) {
	const path = "/v1/admin/drafts"
	buf, code, err := c.sendCode(ctx, http.MethodPost, path, func(bearer string) ([]byte, int, error) {
		buf, code, err := c.doRaw(ctx, http.MethodPost, path, bearer, b)
		return storedLost("drafts create", "the draft", "send it again", buf, code, err)
	})
	return served(http.MethodPost, path, buf, code, err)
}

// ListDrafts reads the first page of the drafts in state, newest first, only
// those with a revision the caller wrote when mine is set.
func (c *Client) ListDrafts(ctx context.Context, state string, mine bool) ([]byte, int, error) {
	q := url.Values{"limit": {strconv.Itoa(draftsPage)}, "state": {state}}
	if mine {
		q.Set("mine", "true")
	}
	return c.draftsCall(ctx, http.MethodGet, "/v1/admin/drafts?"+q.Encode(), nil)
}

// GetDraft reads one draft with its verdict, which the server computes on
// the read for an open draft.
func (c *Client) GetDraft(ctx context.Context, id string) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodGet, draftPath(id), nil)
}

// UpdateDraft replaces an open draft's documents with a new revision.
func (c *Client) UpdateDraft(ctx context.Context, id string, b DraftUpdate) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodPut, draftPath(id), b)
}

// PublishDraft publishes a draft, waiting up to publishTimeout. A request
// whose answer was lost after it went out, its timeout included, and a
// proxy's 502, 503 or 504 with no sentence of strazad's answer an error
// that says the publish may have landed, because the server may have
// written it and only the answer was lost. A dial that failed sent nothing
// and keeps the transport's error.
func (c *Client) PublishDraft(ctx context.Context, id string, b DraftPublish) ([]byte, int, error) {
	if b.Ticked == nil {
		b.Ticked = []string{}
	}
	if b.Typed == nil {
		b.Typed = map[string]string{}
	}
	long := *c
	h := *c.HTTP
	h.Timeout = publishTimeout
	long.HTTP = &h
	path := draftPath(id) + "/publish"
	buf, code, err := long.sendCode(ctx, http.MethodPost, path, func(bearer string) ([]byte, int, error) {
		buf, code, err := long.doRaw(ctx, http.MethodPost, path, bearer, b)
		return publishLost(id, buf, code, err)
	})
	return served(http.MethodPost, path, buf, code, err)
}

// publishLost answers the publish of draft id as it came back, unless the
// answer does not say what happened: then the error says it may have
// landed.
func publishLost(id string, buf []byte, code int, err error) ([]byte, int, error) {
	cause, timedOut, lost := lostCause(err)
	if timedOut {
		return nil, 0, fmt.Errorf("strazad did not answer the publish of draft %s within 2 minutes, so it may have landed. "+
			"Read it with strazactl drafts show %s before you publish again", id, id)
	}
	gateway := code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
	if err == nil && gateway && sentenceOf(buf) == "" {
		cause, lost = fmt.Sprintf("a proxy answered HTTP %d", code), true
	}
	if !lost {
		return buf, code, err
	}
	return nil, 0, fmt.Errorf("strazad's answer to the publish of draft %s was lost (%s), so it may have landed. "+
		"Read it with strazactl drafts show %s before you publish again", id, cause, id)
}

// storedLost answers a call that stores a new draft as it came back, unless
// the answer does not say whether draft was stored: then the error says it
// may have been, with strazad's sentence first when a 5xx carries one, and
// names verb and what the person should not do again yet.
func storedLost(verb, draft, again string, buf []byte, code int, err error) ([]byte, int, error) {
	next := "List your drafts with strazactl drafts list --mine before you " + again
	cause, timedOut, lost := lostCause(err)
	switch {
	case timedOut:
		return nil, 0, fmt.Errorf("strazad did not answer %s in time, so %s may have been stored. %s", verb, draft, next)
	case lost:
		return nil, 0, fmt.Errorf("strazad's answer to %s was lost (%s), so %s may have been stored. %s", verb, cause, draft, next)
	case err != nil || code < http.StatusInternalServerError:
		return buf, code, err
	}
	if sentence := sentenceOf(buf); sentence != "" {
		return nil, 0, fmt.Errorf("%s. %s may have been stored. %s", strings.TrimSuffix(sentence, "."), strings.ToUpper(draft[:1])+draft[1:], next)
	}
	return nil, 0, fmt.Errorf("strazad or a proxy in front of it answered HTTP %d without a sentence, so %s may have been stored. %s", code, draft, next)
}

// lostCause reports whether the transport error err came after the request
// went out, so that its answer was lost, whether it timed out, and its
// cause in words. No error and a dial that failed, which sent nothing, are
// not lost.
func lostCause(err error) (cause string, timedOut, lost bool) {
	var dial *net.OpError
	if err == nil || errors.As(err, &dial) && dial.Op == "dial" {
		return "", false, false
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "", true, true
	}
	var sent *url.Error
	if errors.As(err, &sent) {
		err = sent.Err
	}
	return err.Error(), false, true
}

// sentenceOf is the error sentence an answer's body carries, empty when it
// carries none, as a proxy's page does not.
func sentenceOf(buf []byte) string {
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(buf, &answer)
	return answer.Error
}

// DiscardDraft discards an open draft, which changes nothing live. The reason
// is optional, and no revision is sent.
func (c *Client) DiscardDraft(ctx context.Context, id, reason string) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodPost, draftPath(id)+"/discard", struct {
		Reason string `json:"reason,omitempty"`
	}{reason})
}

// RevertDraft makes a new draft that undoes a published one, with an
// optional note. An answer that does not say whether the undo draft was
// stored answers an error that says it may have been, as CreateDraft does.
func (c *Client) RevertDraft(ctx context.Context, id, note string) ([]byte, int, error) {
	path := draftPath(id) + "/revert"
	body := struct {
		Note string `json:"note,omitempty"`
	}{note}
	buf, code, err := c.sendCode(ctx, http.MethodPost, path, func(bearer string) ([]byte, int, error) {
		buf, code, err := c.doRaw(ctx, http.MethodPost, path, bearer, body)
		return storedLost("drafts revert", "the undo draft", "revert again", buf, code, err)
	})
	return served(http.MethodPost, path, buf, code, err)
}

// RebaseDraft checks draft id again on live state: each object that moved
// keeps the fields the draft changed and takes every other field from live.
func (c *Client) RebaseDraft(ctx context.Context, id string, b DraftRebase) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodPost, draftPath(id)+"/rebase", b)
}

// ContactDraftServer contacts the remote server the draft id proposes as
// server once, with no credential, and reads its tool names.
func (c *Client) ContactDraftServer(ctx context.Context, id, server string) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodPost, draftPath(id)+"/contact", map[string]string{"object": "App/" + server})
}
