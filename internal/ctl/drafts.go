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
// names, counted across every text, and 0 when it names none. Group, on a
// risk only, names the risks a publisher confirms together, and is empty on
// a risk confirmed on its own and on every line of a server before 0.149.0.
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
	Group    string `json:"group"`
	Document int    `json:"document"`
}

// DraftObjectName is one object of a server: the id Straza resolved and the
// value a role file writes for it.
type DraftObjectName struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// DraftCoverage is what one role reaches on one server that pins an object
// type, today and after publishing. Type and Plural are the server's own
// words for its objects. EveryBefore and EveryAfter say the role covers
// every object, those listed later included; otherwise Before and After
// name the covered objects. Holders follows DraftGain.
type DraftCoverage struct {
	Role         string            `json:"role"`
	Server       string            `json:"server"`
	Type         string            `json:"type"`
	Plural       string            `json:"plural"`
	Holders      []string          `json:"holders"`
	HoldersCount int               `json:"holders_count"`
	EveryBefore  bool              `json:"every_before"`
	EveryAfter   bool              `json:"every_after"`
	Before       []DraftObjectName `json:"before"`
	After        []DraftObjectName `json:"after"`
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
// Objects is empty from a server before 0.149.0.
type DraftVerdict struct {
	CheckedAt  string          `json:"checked_at"`
	Refused    []DraftFinding  `json:"refused"`
	Risks      []DraftFinding  `json:"risks"`
	Warnings   []DraftFinding  `json:"warnings"`
	Unchecked  []DraftFinding  `json:"unchecked"`
	Passed     []DraftFinding  `json:"passed"`
	Info       []DraftFinding  `json:"info"`
	Gains      []DraftGain     `json:"gains"`
	Objects    []DraftCoverage `json:"objects"`
	Needs      []DraftNeed     `json:"needs"`
	RiskDigest string          `json:"risk_digest"`
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

// draftsPage is the largest page the drafts list gives, which strazactl asks
// for in one call.
const draftsPage = 200

// draftsCall sends one call of the drafts routes, with body as JSON or no
// body when it is nil, and answers the body and the status as
// the server sent them, so a verb can print the verdict or the findings of a
// refusal. A route strazad does not serve is an error, as served says.
func (c *Client) draftsCall(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	buf, code, err := c.doRaw(ctx, method, path, body)
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
		"Upgrade strazad, or check that --server or STRAZA_SERVER points at strazad", code, method, route)
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
	buf, code, err := c.doRaw(ctx, http.MethodPost, path, b)
	buf, code, err = storedLost("drafts create", "the draft", "send it again", buf, code, err)
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
	buf, code, err := c.doRaw(ctx, http.MethodPost, path, body)
	buf, code, err = storedLost("drafts revert", "the undo draft", "revert again", buf, code, err)
	return served(http.MethodPost, path, buf, code, err)
}

// RebaseDraft checks draft id again on live state: each object that moved
// keeps the fields the draft changed and takes every other field from live.
func (c *Client) RebaseDraft(ctx context.Context, id string, b DraftRebase) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodPost, draftPath(id)+"/rebase", b)
}
