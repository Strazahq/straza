package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/dpop"
)

// fakeUpstream is the git host the fake strazad serves git for.
const fakeUpstream = "https://github.com"

// fakeGit is the git grant side of the fake strazad, guarded by its mu. It
// answers POST /v1/grants/resolve and POST /v1/grants for a bound session,
// as contract 20.12 states them.
type fakeGit struct {
	// aud is the resource server's base URL every grant names.
	aud string
	// upstreams, when set, is the git_upstreams of every check-in answer.
	upstreams []string
	// repos maps a lower-case owner/name to the canonical name the
	// object list holds.
	repos map[string]string
	// pending is how many 202 answers come before the grant, each held
	// for hold unless the request asked for no hold, naming expires as its
	// expiresAt.
	pending int
	hold    time.Duration
	expires time.Time
	// refuse, when set, is the 403 sentence every request of both routes
	// gets.
	refuse string
	// noRoute answers both routes as a server older than them does.
	noRoute bool
	// grants maps each minted grant to what it opens.
	grants map[string]fakeGrant
	// asked holds the body of every grant request, and resolves counts
	// the resolve requests.
	asked    []string
	resolves int
}

// fakeGrant is what one minted grant names.
type fakeGrant struct {
	cnf, op, repo string
	refs          []localapi.GitRef
}

// grants answers the two grant routes.
func (f *fakeStrazad) grants(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	noRoute := f.git.noRoute
	f.mu.Unlock()
	if noRoute {
		http.NotFound(w, r)
		return
	}
	ses, ok := f.bound(w, r)
	if !ok {
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Upstream    string            `json:"upstream"`
		Op          string            `json:"op"`
		Refs        []localapi.GitRef `json:"refs"`
		WaitSeconds *int              `json:"waitSeconds"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		f.fail(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	repo, listed := f.listed(body.Upstream)
	f.mu.Lock()
	refuse := f.git.refuse
	f.mu.Unlock()
	if refuse != "" {
		f.fail(w, http.StatusForbidden, refuse)
		return
	}
	if r.URL.Path == "/v1/grants/resolve" {
		f.mu.Lock()
		f.git.resolves++
		aud := f.git.aud
		f.mu.Unlock()
		if !listed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Straza: the repo is not listed yet.", "code": "not_listed"})
			return
		}
		f.ok(w, map[string]string{"srv": "ghapp", "repo": repo, "aud": aud})
		return
	}
	f.mu.Lock()
	f.git.asked = append(f.git.asked, string(raw))
	g := f.git
	if g.pending > 0 {
		f.git.pending--
	}
	f.mu.Unlock()
	switch {
	case !listed:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Straza: the repo is not listed yet.", "code": "not_listed"})
		return
	case body.WaitSeconds == nil || (body.Op != "fetch" && body.Op != "push") || (body.Op == "fetch" && body.Refs != nil):
		f.fail(w, http.StatusBadRequest, "Straza: the grant request is malformed.")
		return
	case g.pending > 0:
		hold := g.hold
		if *body.WaitSeconds == 0 {
			hold = 0
		}
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"pending": map[string]string{"ref": "apr-1", "expiresAt": g.expires.UTC().Format(time.RFC3339)}})
		return
	}
	grant := "eyJ0eXAiOiJzdHJhemEtZ3JhbnQrand0In0." + randHex(24) + ".c2ln"
	f.mu.Lock()
	if f.git.grants == nil {
		f.git.grants = map[string]fakeGrant{}
	}
	f.git.grants[grant] = fakeGrant{cnf: ses.cnf, op: body.Op, repo: repo, refs: body.Refs}
	exp := f.clock.Now().Add(120 * time.Second).UTC().Format(time.RFC3339)
	f.mu.Unlock()
	f.ok(w, map[string]any{"grant": grant, "aud": g.aud, "repo": repo, "srv": "ghapp", "exp": exp})
}

// listed returns the canonical owner/name of upstream, a URL under
// fakeUpstream, when the fake lists it.
func (f *fakeStrazad) listed(upstream string) (string, bool) {
	rest, ok := strings.CutPrefix(upstream, fakeUpstream+"/")
	if !ok {
		return "", false
	}
	key := strings.ToLower(strings.TrimSuffix(rest, ".git"))
	f.mu.Lock()
	defer f.mu.Unlock()
	repo, ok := f.git.repos[key]
	return repo, ok
}

// grantOf returns what grant opens.
func (f *fakeStrazad) grantOf(grant string) (fakeGrant, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.git.grants[grant]
	return g, ok
}

// ghappSeen is one request the fake resource server accepted.
type ghappSeen struct {
	method, path, query, grant, jti string
	header                          http.Header
	bytes                           int64
}

// fakeGhapp is a resource server that checks each request as ghapp does:
// the grant a fake strazad minted, one proof whose htu is its own URL plus
// the path, ath over the grant, a fresh jti, no nonce and the grant's key,
// the repo and op against the route, and only git's own headers.
type fakeGhapp struct {
	t       *testing.T
	srv     *httptest.Server
	strazad *fakeStrazad
	// answer, when set, writes the body of an accepted request.
	answer func(w io.Writer, seen ghappSeen)

	mu      sync.Mutex
	seen    []ghappSeen
	refused []string
	jtis    map[string]bool
	proofs  []string
}

// ghappHeaders are the headers a holder may send a resource server, with
// the length the transport adds.
var ghappHeaders = []string{"Authorization", "Dpop", "Git-Protocol", "Content-Type", "Content-Encoding", "Accept", "User-Agent", "Content-Length"}

func newFakeGhapp(t *testing.T, strazad *fakeStrazad) *fakeGhapp {
	t.Helper()
	g := &fakeGhapp{t: t, strazad: strazad, jtis: map[string]bool{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGhapp) serve(w http.ResponseWriter, r *http.Request) {
	seen, why := g.check(r)
	if why != "" {
		g.mu.Lock()
		g.refused = append(g.refused, why)
		g.mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("WWW-Authenticate", `DPoP error="invalid_token"`)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "ghapp refused: "+why+"\n")
		return
	}
	n, _ := io.Copy(io.Discard, r.Body)
	seen.bytes = n
	g.mu.Lock()
	g.seen = append(g.seen, seen)
	answer := g.answer
	g.mu.Unlock()
	svc := seen.path[strings.LastIndex(seen.path, "/")+1:]
	if seen.query != "" {
		svc = strings.TrimPrefix(seen.query, "service=")
		w.Header().Set("Content-Type", "application/x-"+svc+"-advertisement")
	} else {
		w.Header().Set("Content-Type", "application/x-"+svc+"-result")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Ghapp-Internal", "secret-of-ghapp")
	if answer != nil {
		answer(w, seen)
		return
	}
	_, _ = fmt.Fprintf(w, "ok %s %d", seen.path, n)
}

// check returns the request as seen, or why ghapp refuses it.
func (g *fakeGhapp) check(r *http.Request) (ghappSeen, string) {
	seen := ghappSeen{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()}
	for k := range r.Header {
		if !slices.Contains(ghappHeaders, k) {
			return seen, "header " + k
		}
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/git/")
	if !ok || r.URL.RawPath != "" {
		return seen, "path"
	}
	i := strings.Index(rest, ".git/")
	if i < 0 {
		return seen, "path"
	}
	repo, tail := rest[:i], rest[i+len(".git/"):]
	grant, ok := strings.CutPrefix(r.Header.Get("Authorization"), "DPoP ")
	if !ok {
		return seen, "authorization"
	}
	seen.grant = grant
	held, ok := g.strazad.grantOf(grant)
	if !ok {
		return seen, "unknown grant"
	}
	proofs := r.Header.Values("DPoP")
	if len(proofs) != 1 {
		return seen, fmt.Sprintf("%d proofs", len(proofs))
	}
	jti, why := g.proof(r, proofs[0], grant, held.cnf)
	if why != "" {
		return seen, why
	}
	seen.jti = jti
	if repo != held.repo {
		return seen, "repo " + repo
	}
	switch {
	case tail == "info/refs" && r.Method == http.MethodGet && r.URL.RawQuery == "service=git-upload-pack":
		if held.op != "fetch" {
			return seen, "op for upload-pack info/refs"
		}
	case tail == "info/refs" && r.Method == http.MethodGet && r.URL.RawQuery == "service=git-receive-pack":
		if held.op != "push" || held.refs != nil {
			return seen, "op for receive-pack info/refs"
		}
	case tail == "git-upload-pack" && r.Method == http.MethodPost && r.URL.RawQuery == "":
		if held.op != "fetch" {
			return seen, "op for upload-pack"
		}
	case tail == "git-receive-pack" && r.Method == http.MethodPost && r.URL.RawQuery == "":
		if held.op != "push" || held.refs == nil {
			return seen, "op for receive-pack"
		}
	default:
		return seen, "route"
	}
	return seen, ""
}

// proof checks one proof and returns its jti.
func (g *fakeGhapp) proof(r *http.Request, raw, grant, cnf string) (string, string) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", "proof form"
	}
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var hdr struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
		JWK struct {
			X string `json:"x"`
		} `json:"jwk"`
	}
	var claims map[string]any
	if json.Unmarshal(hb, &hdr) != nil || json.Unmarshal(cb, &claims) != nil {
		return "", "proof json"
	}
	pub, _ := base64.RawURLEncoding.DecodeString(hdr.JWK.X)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	iat, _ := claims["iat"].(float64)
	jti, _ := claims["jti"].(string)
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case hdr.Typ != dpop.ProofType || hdr.Alg != "EdDSA" || len(pub) != ed25519.PublicKeySize:
		return "", "proof header"
	case !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig):
		return "", "proof signature"
	case claims["htm"] != r.Method:
		return "", "htm"
	case claims["htu"] != g.srv.URL+r.URL.Path:
		return "", fmt.Sprintf("htu %v", claims["htu"])
	case claims["ath"] != dpop.AccessTokenHash(grant):
		return "", "ath"
	case claims["nonce"] != nil:
		return "", "nonce sent"
	case dpop.Thumbprint(pub) != cnf:
		return "", "proof key is not the grant's"
	case time.Since(time.Unix(int64(iat), 0)).Abs() > time.Minute:
		return "", "iat"
	case jti == "" || g.jtis[jti]:
		return "", "jti reused"
	}
	g.jtis[jti] = true
	g.proofs = append(g.proofs, raw)
	return jti, ""
}

// accepted returns the requests the fake accepted so far.
func (g *fakeGhapp) accepted() []ghappSeen {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ghappSeen(nil), g.seen...)
}

// refusals returns why the fake refused requests so far.
func (g *fakeGhapp) refusals() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.refused...)
}

// signed returns every proof the fake accepted.
func (g *fakeGhapp) signed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.proofs...)
}
