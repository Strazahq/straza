package service

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/dpop"
)

// Bounds of the held git grants.
const (
	// maxGitOps is how many helper runs of one uid the service holds grants
	// for. A grant outlives its run, so past the bound a new run takes the
	// place of the run whose newest grant is oldest, the one least likely
	// to be still running.
	maxGitOps = 256
	// maxGrantLen bounds a grant strazad answers.
	maxGrantLen = 16 << 10
)

// Sentences of the relay to a resource server. The helper prints each one
// for git.
const (
	gitNoGrant = "Straza: `straza service` holds no git grant for this %s, so it did not send the request to the git server. " +
		"Run git again, and if this keeps happening, update Straza so the git helper and the service are the same version."
	gitHostUnreachable = "Straza: `straza service` could not reach the git server at %s (%v). " +
		"Check that this machine reaches that address, then run git again."
)

// gitSlot is which of a helper run's grants a request needs.
type gitSlot int

// The grants one helper run may hold at once.
const (
	slotFetch  gitSlot = iota // a fetch grant
	slotAdvert                // a push grant without refs, for the advertisement
	slotPush                  // a push grant that names the updates
	gitSlots
)

// heldGrant is one grant the service holds, never shown to the API half:
// the grant, the resource server's base URL, the canonical repo, the op
// and when it expires.
type heldGrant struct {
	grant, aud, repo, op string
	exp                  time.Time
}

// gitKey names one helper run of one uid.
type gitKey struct{ uid, op string }

// gitRun is what one helper run holds: a grant per slot, and when it took
// its newest one.
type gitRun struct {
	slots [gitSlots]heldGrant
	last  time.Time
}

// gitState is the git door's part of the credential half: the grants each
// helper run holds, and the transport to resource servers. mu guards held.
type gitState struct {
	// waitEvery is the longest gap between two wait lines.
	waitEvery time.Duration
	// send reaches resource servers. It is not the strazad transport, which
	// buffers bodies and signs proofs for strazad's origin.
	send http.RoundTripper

	mu   sync.Mutex
	held map[gitKey]*gitRun
}

// gitState returns the git door's state, creating it on first use.
func (s *credStore) gitState() *gitState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.git == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		// The answer reaches git byte for byte, so the transport must not ask
		// for gzip and unpack it on its own.
		t.DisableCompression = true
		t.ReadBufferSize, t.WriteBufferSize = gitCopyBuf, gitCopyBuf
		s.git = &gitState{waitEvery: gitWaitEvery, send: t, held: map[gitKey]*gitRun{}}
	}
	return s.git
}

// slotOf is the slot a grant for req fills.
func slotOf(req localapi.GitGrantRequest) gitSlot {
	switch {
	case req.Op == gitFetch:
		return slotFetch
	case len(req.Refs) == 0:
		return slotAdvert
	}
	return slotPush
}

// slotFor is the slot whose grant opens route. A receive-pack POST takes
// only the grant that names the updates, never the one without refs, which
// skipped the approval.
func slotFor(route gitRoute) gitSlot {
	switch {
	case route.op == gitFetch:
		return slotFetch
	case route.tail == "info/refs":
		return slotAdvert
	}
	return slotPush
}

// hold keeps h in slot for uid's helper run op until it expires. A new run
// past maxGitOps for uid takes the place of uid's run whose newest grant is
// oldest.
func (g *gitState) hold(uid, op string, slot gitSlot, h heldGrant, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep(now)
	k := gitKey{uid, op}
	if g.held[k] == nil {
		g.evictFor(uid)
		g.held[k] = &gitRun{}
	}
	g.held[k].slots[slot] = h
	g.held[k].last = now
}

// evictFor drops uid's run whose newest grant is oldest when uid holds
// maxGitOps runs. The caller holds g.mu.
func (g *gitState) evictFor(uid string) {
	n := 0
	var oldest gitKey
	var at time.Time
	for k, run := range g.held {
		if k.uid != uid {
			continue
		}
		n++
		if at.IsZero() || run.last.Before(at) {
			oldest, at = k, run.last
		}
	}
	if n >= maxGitOps {
		delete(g.held, oldest)
	}
}

// take returns the unexpired grant of uid's helper run op that opens
// route.
func (g *gitState) take(uid, op string, route gitRoute, now time.Time) (heldGrant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	run := g.held[gitKey{uid, op}]
	if run == nil {
		return heldGrant{}, false
	}
	if h := run.slots[slotFor(route)]; h.grant != "" && now.Before(h.exp) {
		return h, true
	}
	return heldGrant{}, false
}

// sweep drops every expired grant and every run left with none. The
// caller holds g.mu.
func (g *gitState) sweep(now time.Time) {
	for k, run := range g.held {
		live := false
		for i := range run.slots {
			if run.slots[i].grant != "" && !now.Before(run.slots[i].exp) {
				run.slots[i] = heldGrant{}
			}
			live = live || run.slots[i].grant != ""
		}
		if !live {
			delete(g.held, k)
		}
	}
}

// GitSend sends in to the resource server under the grant ses's uid holds
// for opID and route. The URL is the grant's aud, /git/, its canonical
// repo, .git/ and the route, never the helper's own path, and the proof's
// htu is that URL without its query. The body streams once, and only git's
// own headers pass.
func (s *credStore) GitSend(ses Session, opID string, route gitRoute, in *http.Request) (*http.Response, error) {
	a, _, err := s.held(ses)
	if err != nil {
		return nil, err
	}
	if _, _, err := a.ready(); err != nil {
		return nil, err
	}
	g := s.gitState()
	h, ok := g.take(ses.UID, opID, route, s.now())
	if !ok {
		return nil, &Refused{Status: http.StatusForbidden, Sentence: fmt.Sprintf(gitNoGrant, route.op)}
	}
	target, err := url.Parse(h.aud + "/git/" + h.repo + ".git/" + route.tail)
	if err != nil {
		return nil, &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitHostUnreachable, h.aud, err)}
	}
	htu := target.String()
	if route.service != "" {
		target.RawQuery = "service=" + route.service
	}
	a.mu.Lock()
	key := a.key
	a.mu.Unlock()
	proof, err := dpop.NewProof(key, in.Method, htu, "", h.grant, time.Now())
	if err != nil {
		return nil, fmt.Errorf("sign the DPoP proof for the git server: %w", err)
	}
	var body io.Reader
	if in.ContentLength != 0 {
		body = in.Body
	}
	out, err := http.NewRequestWithContext(in.Context(), in.Method, target.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		out.ContentLength = in.ContentLength
	}
	for _, k := range gitHeaders {
		if v := in.Header.Values(k); len(v) > 0 {
			out.Header[k] = v
		}
	}
	out.Header.Set("Authorization", dpop.Scheme+" "+h.grant)
	out.Header.Set(dpop.HeaderProof, proof)
	resp, err := g.send.RoundTrip(out)
	if err != nil {
		return nil, &Refused{Status: http.StatusBadGateway, Sentence: fmt.Sprintf(gitHostUnreachable, originOf(h.aud), err)}
	}
	return resp, nil
}
