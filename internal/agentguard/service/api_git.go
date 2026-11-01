package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// Bounds of the git door.
const (
	// maxGitStreams is how many git door requests one uid may have open at
	// once. Each helper run sends one at a time, so this leaves room for
	// parallel submodule fetches of many windows.
	maxGitStreams = 32
	// gitGrantBodyMax bounds a grant request, which names up to maxGitRefs
	// ref updates.
	gitGrantBodyMax = 1 << 20
	// maxGitRefs is the most ref updates one push may name, as spec/grant
	// caps refs.
	maxGitRefs = 1000
	// maxGitURL bounds the URL git gave the helper.
	maxGitURL = 2048
	// gitCopyBuf is the size of each copy of a relayed body, both ways.
	gitCopyBuf = 32 << 10
)

// The ops of a git grant.
const (
	gitFetch = "fetch"
	gitPush  = "push"
)

// gitHeaders are the only headers the door passes between the helper and
// a resource server, in both directions. Credentials, cookies and the
// window headers never cross.
var gitHeaders = []string{"Git-Protocol", "Content-Type", "Content-Encoding", "Accept", "User-Agent"}

// Refusals of the git door. The helper prints each one for git.
const (
	tooManyGitStreams = "Straza: your user already has %d git commands running through `straza service` on this machine, " +
		"which is the most it runs at once, so this git command did not run. Wait for the others to finish, then run git again."
	badGitOp = "Straza: the git helper sent no valid run id, so `straza service` refused the request. " +
		"Update Straza so the git helper and the service are the same version."
	badGitURL = "Straza: `straza service` cannot use the repo URL git gave, because Straza takes only a plain http or https URL " +
		"with no user name, query or fragment. Set the remote to the repo's https URL, such as https://github.com/owner/name.git, " +
		"then run git again."
	badGitGrantOp = "Straza: the git helper asked for a git grant for the operation %q, and `straza service` knows only fetch and push. " +
		"Update Straza so the git helper and the service are the same version."
	refsOnFetch = "Straza: the git helper named ref updates for a fetch, and only a push names them. " +
		"Update Straza so the git helper and the service are the same version."
	tooManyGitRefs = "Straza: this push names %d ref updates, and Straza takes at most %d in one push. " +
		"Push fewer branches at once, then run git push again."
	badGitRef = "Straza: this push names the ref update %q, which is not a full ref name with old and new object ids " +
		"in lower-case hex. Update Straza so the git helper and the service are the same version."
	gitEncodedPath = "Straza: the git helper sent the path %s with percent-encoded characters, and `straza service` takes " +
		"only plain paths on the git door. Update Straza so the git helper and the service are the same version."
	badInfoRefs = "Straza: the git helper asked for info/refs with the query %q, and `straza service` takes only " +
		"service=git-upload-pack or service=git-receive-pack. Update Straza so the git helper and the service are the same version."
)

// gitRoute is one smart HTTP request of the git door: the path below the
// repo, the service an info/refs names, and the op of the grant it needs.
type gitRoute struct {
	tail    string
	service string
	op      string
}

// gitDoor serves every route under localapi.RouteGit for one API half and
// counts each uid's open requests.
type gitDoor struct {
	*api

	mu   sync.Mutex
	open map[string]int
}

// gitRoutes returns the handler of the git door. Only straza's own git
// helper calls it.
func (a *api) gitRoutes() http.Handler {
	d := &gitDoor{api: a, open: map[string]int{}}
	return http.HandlerFunc(d.serve)
}

// serve answers one git door request: the plain path rule, the per-uid
// cap, then the route.
func (d *gitDoor) serve(w http.ResponseWriter, r *http.Request) {
	// The plain path rule of the MCP proxy, with a sentence for the helper.
	if r.URL.EscapedPath() != r.URL.Path {
		d.refuse(w, r, http.StatusBadRequest, fmt.Sprintf(gitEncodedPath, r.URL.EscapedPath()))
		return
	}
	uid := callerOf(r).UID
	if !d.enter(uid) {
		d.refuse(w, r, http.StatusTooManyRequests, fmt.Sprintf(tooManyGitStreams, maxGitStreams))
		return
	}
	defer d.leave(uid)
	switch tail := strings.TrimPrefix(r.URL.Path, localapi.RouteGit); tail {
	case "resolve":
		d.only(http.MethodPost, d.resolve)(w, r)
	case "grant":
		d.only(http.MethodPost, d.grant)(w, r)
	case "info/refs":
		d.only(http.MethodGet, d.advertise)(w, r)
	case "git-upload-pack", "git-receive-pack":
		op := gitFetch
		if tail == "git-receive-pack" {
			op = gitPush
		}
		d.only(http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
			d.relay(w, r, gitRoute{tail: tail, op: op})
		})(w, r)
	default:
		d.refuse(w, r, http.StatusNotFound, "The Straza background service has no route "+r.URL.Path+
			". Update Straza so the client and the service are the same version.")
	}
}

// enter counts one more open request of uid and reports false, counting
// nothing, when uid already has maxGitStreams open.
func (d *gitDoor) enter(uid string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open[uid] >= maxGitStreams {
		return false
	}
	d.open[uid]++
	return true
}

// leave counts one open request of uid less.
func (d *gitDoor) leave(uid string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open[uid]--; d.open[uid] <= 0 {
		delete(d.open, uid)
	}
}

// window returns the session of the request's harness window and the
// helper run's id, or answers the refusal and reports false. A named
// window gets its own live session or the refusal and never checks in, so
// a burst of git commands adds no key check-in. An unnamed one takes the
// caller's newest live session of the harness, as the MCP proxy does.
func (d *gitDoor) window(w http.ResponseWriter, r *http.Request) (Session, string, bool) {
	opID := r.Header.Get(localapi.HeaderGitOp)
	if !validOpID(opID) {
		d.refuse(w, r, http.StatusBadRequest, badGitOp)
		return Session{}, "", false
	}
	h := localapi.Harness{
		Name:    r.Header.Get(localapi.HeaderHarness),
		Version: r.Header.Get(localapi.HeaderHarnessVersion),
		Session: r.Header.Get(localapi.HeaderHarnessSession),
	}
	if !d.named(w, r, h) {
		return Session{}, "", false
	}
	s, err := d.proxySession(r.Context(), callerOf(r), h)
	if err != nil {
		d.fail(w, r, http.StatusInternalServerError, err)
		return Session{}, "", false
	}
	return s, opID, true
}

// gitURL returns the URL git gave the helper, or answers the refusal and
// reports false. The refusal never repeats the URL, which could carry a
// token in its user part.
func (d *gitDoor) gitURL(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := r.Header.Get(localapi.HeaderGitURL)
	if !validGitURL(raw) {
		d.refuse(w, r, http.StatusBadRequest, badGitURL)
		return "", false
	}
	return raw, true
}

// resolve answers POST resolve: whether a Straza server serves git for the
// repo URL.
func (d *gitDoor) resolve(w http.ResponseWriter, r *http.Request) {
	upstream, ok := d.gitURL(w, r)
	if !ok {
		return
	}
	s, _, ok := d.window(w, r)
	if !ok {
		return
	}
	listed, err := d.creds.GitResolve(r.Context(), s, upstream)
	if err != nil {
		d.fail(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, localapi.GitResolveAnswer{Listed: listed})
}

// grant answers POST grant as NDJSON once the request is valid and the
// window has a session: a wait line at least every 5 seconds while an
// approval is open, then the ok line or the error line. A refusal before
// that answers a Refusal with its status.
func (d *gitDoor) grant(w http.ResponseWriter, r *http.Request) {
	upstream, ok := d.gitURL(w, r)
	if !ok {
		return
	}
	var req localapi.GitGrantRequest
	if !d.read(w, r, gitGrantBodyMax, &req) {
		return
	}
	if why := checkGrantRequest(req); why != "" {
		d.refuse(w, r, http.StatusBadRequest, why)
		return
	}
	s, opID, ok := d.window(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	line := func(l localapi.GitGrantLine) {
		_ = json.NewEncoder(w).Encode(l)
		_ = rc.Flush()
	}
	err := d.creds.GitGrant(r.Context(), s, opID, upstream, req, func(sentence string) {
		line(localapi.GitGrantLine{Wait: sentence})
	})
	if err != nil {
		status, sentence := http.StatusBadGateway, err.Error()
		var refused *Refused
		if errors.As(err, &refused) {
			status, sentence = refused.Status, refused.Sentence
		}
		d.logRefusal(r, status, sentence)
		line(localapi.GitGrantLine{Error: sentence})
		return
	}
	line(localapi.GitGrantLine{OK: true})
}

// advertise relays GET info/refs, whose one query names the service.
func (d *gitDoor) advertise(w http.ResponseWriter, r *http.Request) {
	switch r.URL.RawQuery {
	case "service=git-upload-pack":
		d.relay(w, r, gitRoute{tail: "info/refs", service: "git-upload-pack", op: gitFetch})
	case "service=git-receive-pack":
		d.relay(w, r, gitRoute{tail: "info/refs", service: "git-receive-pack", op: gitPush})
	default:
		d.refuse(w, r, http.StatusBadRequest, fmt.Sprintf(badInfoRefs, cut(r.URL.RawQuery, 256)))
	}
}

// relay sends r to the resource server under the run's grant for route and
// streams the answer back as it came, with git's own headers only. Both
// directions stream at once, so a resource server may answer while the
// helper still sends.
func (d *gitDoor) relay(w http.ResponseWriter, r *http.Request, route gitRoute) {
	s, opID, ok := d.window(w, r)
	if !ok {
		return
	}
	_ = http.NewResponseController(w).EnableFullDuplex()
	resp, err := d.creds.GitSend(s, opID, route, r)
	if err != nil {
		d.fail(w, r, http.StatusBadGateway, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for _, k := range gitHeaders {
		if v := resp.Header.Values(k); len(v) > 0 {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	_ = copyFlush(w, resp.Body)
}

// copyFlush copies src to w in gitCopyBuf pieces and flushes each one, so
// git sees progress as the resource server sends it.
func copyFlush(w http.ResponseWriter, src io.Reader) error {
	rc := http.NewResponseController(w)
	buf := make([]byte, gitCopyBuf)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if ferr := rc.Flush(); ferr != nil && !errors.Is(ferr, http.ErrNotSupported) {
				return ferr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// validOpID reports whether id is 32 lower-case hex characters, the run id
// the helper picks.
func validOpID(id string) bool {
	return len(id) == 32 && isLowerHex(id)
}

// isLowerHex reports whether s is lower-case hex only.
func isLowerHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// validGitURL reports whether raw is a URL Straza may name in a grant
// request: printable ASCII, http or https with a host, and no user part,
// query or fragment, so no credential of git's reaches strazad.
func validGitURL(raw string) bool {
	if raw == "" || len(raw) > maxGitURL {
		return false
	}
	for _, c := range raw {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.Opaque == "" &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// checkGrantRequest returns why req is not a grant request the door
// forwards, or "".
func checkGrantRequest(req localapi.GitGrantRequest) string {
	switch {
	case req.Op != gitFetch && req.Op != gitPush:
		return fmt.Sprintf(badGitGrantOp, cut(req.Op, 64))
	case req.Op == gitFetch && len(req.Refs) > 0:
		return refsOnFetch
	case len(req.Refs) > maxGitRefs:
		return fmt.Sprintf(tooManyGitRefs, len(req.Refs), maxGitRefs)
	}
	for _, ref := range req.Refs {
		if !validRef(ref) {
			return fmt.Sprintf(badGitRef, cut(ref.Ref, 256))
		}
	}
	return ""
}

// validRef reports whether ref names a full ref under refs/ with printable
// characters git allows in a ref name, and old and new object ids of the
// same hash length, 40 or 64 lower-case hex characters.
func validRef(ref localapi.GitRef) bool {
	if !strings.HasPrefix(ref.Ref, "refs/") || len(ref.Ref) > 1024 || strings.ContainsAny(ref.Ref, "~^:?*[\\") {
		return false
	}
	for _, c := range ref.Ref {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return (len(ref.Old) == 40 || len(ref.Old) == 64) && len(ref.New) == len(ref.Old) && isLowerHex(ref.Old) && isLowerHex(ref.New)
}
