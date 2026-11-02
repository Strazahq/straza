package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/sameorigin"
)

// Timing of git grant requests.
const (
	// gitWaitSeconds caps how long strazad holds a repeated grant request
	// for an open approval, so the service writes a wait line between
	// requests. The first request asks for no hold, so a grant that needs an
	// approval prints its first wait line at once.
	gitWaitSeconds = 5
	// gitWaitEvery is the longest gap between two wait lines.
	gitWaitEvery = 5 * time.Second
	// gitAskTimeout bounds one grant request: the approval hold plus the
	// time any other strazad call gets. The whole wait has no bound but the
	// approval's own window, because git waits as long as the helper does.
	gitAskTimeout = requestTimeout + gitWaitSeconds*time.Second
)

// Sentences of the credential half's git grant requests. The helper prints
// each one for git.
const (
	gitServerTooOld = "Straza: the Straza server at %s does not give git grants yet. " +
		"Ask an administrator to upgrade it, then run git again."
	gitWaiting       = "Straza: waiting for an approver to allow %s on %s (%ds left)."
	gitPushTimedOut  = "Straza: nobody approved this push to %s in time, so it did not run. Ask your approver, then run git push again."
	gitFetchTimedOut = "Straza: nobody approved this fetch from %s in time, so it did not run. Ask your approver, then run git again."
	gitUnreachable   = "Straza: `straza service` could not reach the Straza server at %s to ask for a git grant (%v). " +
		"Check that this machine reaches the server, then run git again, or run `straza doctor`."
	gitNoReason = "Straza: the Straza server at %s answered the git grant request with HTTP %d and gave no reason. " +
		"Run git again in a minute, and run `straza doctor` if it keeps failing."
	gitUnreadable = "Straza: the Straza server at %s answered the git grant request with an answer `straza service` " +
		"cannot use (%s), so git did not run. Update Straza so the client and the server are the same version, then run git again."
	gitGrantExpired = "Straza: the git grant from the Straza server at %s had already expired by this machine's clock, " +
		"so git did not run. Check that this machine's clock is set correctly, then run git again."
	gitStopped = "Straza: git stopped waiting for its git grant, so `straza service` stopped asking the Straza server for it. " +
		"Run git again."
)

// gitBody is the body of POST /v1/grants and, with only Upstream set, of
// POST /v1/grants/resolve.
type gitBody struct {
	Upstream    string            `json:"upstream"`
	Op          string            `json:"op,omitempty"`
	Refs        []localapi.GitRef `json:"refs,omitempty"`
	WaitSeconds *int              `json:"waitSeconds,omitempty"`
}

// GitResolve asks ses's strazad whether it serves git for upstream: 200
// answers true, 404 with code not_listed answers false, and anything else
// is the refusal with strazad's sentence.
func (s *credStore) GitResolve(ctx context.Context, ses Session, upstream string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	ans, err := postJSON(ctx, s.strazadClient(ses), "/v1/grants/resolve", gitBody{Upstream: upstream})
	switch {
	case err != nil:
		return false, gitUnreached(ses.ServerURL, err)
	case ans.status == http.StatusOK:
		return true, nil
	case ans.status == http.StatusNotFound && codeOf(ans) == "not_listed":
		return false, nil
	}
	return false, gitRefusal(ses.ServerURL, ans)
}

// GitGrant asks ses's strazad for a git grant and holds it for ses's uid
// and opID. The first request has waitSeconds 0. A 202 means an approval
// is open: it writes a wait line at once and then every wait period, also
// while a request is in flight, and asks again with waitSeconds 5 and the
// rest of the body unchanged, no sooner than one wait period after a
// repeated ask, until strazad answers otherwise or the approval's
// expiresAt passes.
func (s *credStore) GitGrant(ctx context.Context, ses Session, opID, upstream string, req localapi.GitGrantRequest, wait func(string)) error {
	g := s.gitState()
	waitSeconds := 0
	body := gitBody{Upstream: upstream, Op: req.Op, Refs: req.Refs, WaitSeconds: &waitSeconds}
	client := s.strazadClient(ses)
	every := g.waitEvery
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	var expires time.Time
	tick := func() {
		if !expires.IsZero() {
			wait(waitSentence(req, upstream, expires, s.now()))
		}
	}
	for {
		asked := time.Now()
		ans, err := askGrant(ctx, client, body, ticker, tick)
		switch {
		case err != nil && ctx.Err() != nil:
			return &Refused{Status: http.StatusBadGateway, Sentence: gitStopped}
		case err != nil:
			return gitUnreached(ses.ServerURL, err)
		case ans.status == http.StatusOK:
			return s.holdGrant(ses, opID, req, ans)
		case ans.status != http.StatusAccepted:
			return gitRefusal(ses.ServerURL, ans)
		}
		var held struct {
			Pending struct {
				ExpiresAt time.Time `json:"expiresAt"`
			} `json:"pending"`
		}
		if err := json.Unmarshal(ans.body, &held); err != nil || held.Pending.ExpiresAt.IsZero() {
			return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitUnreadable, ses.ServerURL, "a 202 without the approval's expiry")}
		}
		if !s.now().Before(held.Pending.ExpiresAt) {
			return &Refused{Status: http.StatusForbidden, Sentence: timedOutSentence(req, upstream)}
		}
		first := expires.IsZero()
		expires = held.Pending.ExpiresAt
		if first {
			tick()
			ticker.Reset(every)
			waitSeconds = gitWaitSeconds
			continue
		}
		if err := pause(ctx, every-time.Since(asked), ticker, tick); err != nil {
			return &Refused{Status: http.StatusBadGateway, Sentence: gitStopped}
		}
	}
}

// askGrant sends one grant request and calls tick on each tick of t until
// the answer arrives.
func askGrant(ctx context.Context, client *agentguard.Client, body gitBody, t *time.Ticker, tick func()) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, gitAskTimeout)
	defer cancel()
	type result struct {
		ans answer
		err error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := postJSON(ctx, client, "/v1/grants", body)
		done <- result{ans, err}
	}()
	for {
		select {
		case r := <-done:
			return r.ans, r.err
		case <-t.C:
			tick()
		}
	}
}

// pause waits d, calling tick on each tick of t, and returns ctx's error
// when ctx ends first.
func pause(ctx context.Context, d time.Duration, t *time.Ticker, tick func()) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return nil
		case <-t.C:
			tick()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// holdGrant checks a 200 grant answer and holds the grant for ses's uid
// and opID in the slot req asked for.
func (s *credStore) holdGrant(ses Session, opID string, req localapi.GitGrantRequest, ans answer) error {
	var got struct {
		Grant string    `json:"grant"`
		Aud   string    `json:"aud"`
		Repo  string    `json:"repo"`
		Exp   time.Time `json:"exp"`
	}
	err := json.Unmarshal(ans.body, &got)
	_, audWhy := parseUpstream(got.Aud)
	why := ""
	switch {
	case err != nil:
		why = "not the JSON of a grant answer"
	case !validGrant(got.Grant):
		why = "no grant in the compact JWS form"
	case audWhy != "":
		why = "an audience that is not a canonical https URL, because " + audWhy
	case !validRepo(got.Repo):
		why = "a repo that is not owner/name"
	case got.Exp.IsZero():
		why = "no expiry"
	}
	if why != "" {
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitUnreadable, ses.ServerURL, why)}
	}
	if !s.now().Before(got.Exp) {
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitGrantExpired, ses.ServerURL)}
	}
	s.gitState().hold(ses.UID, opID, slotOf(req), heldGrant{grant: got.Grant, aud: got.Aud, repo: got.Repo, op: req.Op, exp: got.Exp}, s.now())
	return nil
}

// strazadClient is a client for ses's strazad through the session
// transport, with no overall timeout, because a grant request may hold for
// an approval. The caller bounds each request with its context.
func (s *credStore) strazadClient(ses Session) *agentguard.Client {
	return &agentguard.Client{Base: strings.TrimRight(ses.ServerURL, "/"), HTTP: &http.Client{
		Transport: s.Transport(ses), CheckRedirect: sameorigin.CheckCredentialed,
	}}
}

// codeOf returns the code of a strazad refusal, "" when it has none.
func codeOf(a answer) string {
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(a.body, &body)
	return body.Code
}

// gitRefusal words a strazad answer that is neither a grant nor a hold. A
// route strazad does not know means a server older than git grants.
// Strazad's own sentence passes unchanged.
func gitRefusal(server string, a answer) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(a.body, &body)
	switch {
	case body.Error == "" && (a.status == http.StatusNotFound || a.status == http.StatusMethodNotAllowed):
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitServerTooOld, server)}
	case body.Error == "":
		return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitNoReason, server, a.status)}
	case a.status >= http.StatusInternalServerError:
		return &Refused{Status: http.StatusBadGateway, Sentence: body.Error}
	}
	return &Refused{Status: a.status, Sentence: body.Error}
}

// gitUnreached words a grant or resolve request that got no answer. A
// refusal of the session itself passes unchanged.
func gitUnreached(server string, err error) error {
	var refused *Refused
	if errors.As(err, &refused) {
		return refused
	}
	return &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitUnreachable, server, err)}
}

// waitSentence is the wait line for req on upstream while an approval
// open until expires waits at now.
func waitSentence(req localapi.GitGrantRequest, upstream string, expires, now time.Time) string {
	left := int(math.Ceil(expires.Sub(now).Seconds()))
	return fmt.Sprintf(gitWaiting, opWords(req), repoLabel(upstream), max(left, 0))
}

// timedOutSentence is the refusal of req on upstream whose approval ended
// with no decision.
func timedOutSentence(req localapi.GitGrantRequest, upstream string) string {
	if req.Op == gitFetch {
		return fmt.Sprintf(gitFetchTimedOut, repoLabel(upstream))
	}
	return fmt.Sprintf(gitPushTimedOut, repoLabel(upstream))
}

// opWords names what an approver allows: the refs a push updates, at most
// three by name, or the fetch.
func opWords(req localapi.GitGrantRequest) string {
	if req.Op == gitFetch {
		return "a fetch"
	}
	if len(req.Refs) == 0 {
		return "this push"
	}
	var names []string
	for _, r := range req.Refs[:min(len(req.Refs), 3)] {
		names = append(names, r.Ref)
	}
	if more := len(req.Refs) - len(names); more > 0 {
		return strings.Join(names, ", ") + fmt.Sprintf(" and %d more", more)
	}
	return strings.Join(names, ", ")
}

// repoLabel names the repo of upstream for a person: host and path,
// without the scheme, a trailing .git or a trailing slash.
func repoLabel(upstream string) string {
	u, err := url.Parse(upstream)
	if err != nil {
		return "this repo"
	}
	return strings.TrimSuffix(strings.TrimSuffix(u.Host+u.Path, "/"), ".git")
}

// validGrant reports whether g has the compact JWS form, three base64url
// parts, so it is safe in an Authorization header.
func validGrant(g string) bool {
	if g == "" || len(g) > maxGrantLen || strings.Count(g, ".") != 2 {
		return false
	}
	for _, c := range g {
		if !nameChar(c) {
			return false
		}
	}
	return true
}

// validRepo reports whether repo is owner/name with GitHub's name
// characters, so it is one plain path segment pair in the resource
// server's URL.
func validRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || len(repo) > 256 {
		return false
	}
	for _, seg := range []string{owner, name} {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, c := range seg {
			if !nameChar(c) {
				return false
			}
		}
	}
	return true
}

// nameChar reports whether c is an ASCII letter, a digit, '-', '_' or
// '.', the characters of a repo name and of base64url with its dots.
func nameChar(c rune) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.'
}
