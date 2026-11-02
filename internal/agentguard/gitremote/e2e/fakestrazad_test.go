package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// Refs the fake strazad treats apart: a push naming refBlocked is refused,
// and one naming refHeld waits for an approval for holdFor.
const (
	refBlocked = "refs/heads/blocked"
	refHeld    = "refs/heads/held"
	approvalID = "apr-e2e-1"
	grantLife  = 120 * time.Second
)

// refusedSentence is the fake strazad's refusal of a push to refBlocked,
// in the form of sentence 8 of the contract.
const refusedSentence = "Straza refused this push: no role allows git_push on refs/heads/blocked. " +
	"Ask an administrator for a role that allows git_push on acme/pay-api."

// grantAsk is the body of POST /v1/grants and /v1/grants/resolve.
type grantAsk struct {
	Upstream    string            `json:"upstream"`
	Op          string            `json:"op"`
	Refs        []localapi.GitRef `json:"refs"`
	WaitSeconds *int              `json:"waitSeconds"`
}

// grantServer answers strazad's two git grant routes in front of the real
// strazad. It signs grants with its own key, which the fake resource
// server trusts, and binds each to the session token's cnf.jkt.
type grantServer struct {
	key      ed25519.PrivateKey
	iss      string
	aud      string
	upstream string
	repos    map[string]string // lower-case owner/name to the canonical name
	holdFor  time.Duration

	mu       sync.Mutex
	resolved []string
	asked    []grantAsk
	issued   []string
	held     map[string]time.Time
}

// sessionClaims is what the fake reads from the session token. The real
// strazad verified the token on every other route, so the fake only
// decodes it.
type sessionClaims struct {
	Sub string `json:"sub"`
	Ses string `json:"ses"`
	Hrn string `json:"hrn"`
	Cnf struct {
		JKT string `json:"jkt"`
	} `json:"cnf"`
}

func (g *grantServer) session(r *http.Request) (sessionClaims, error) {
	var c sessionClaims
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "DPoP ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 || r.Header.Get("DPoP") == "" {
		return c, errors.New("the request carries no DPoP session token and proof")
	}
	if err := decodePart(parts[1], &c); err != nil || c.Cnf.JKT == "" || c.Sub == "" {
		return c, errors.New("the session token names no subject or key")
	}
	return c, nil
}

// lookup maps an upstream URL to the canonical repo the way the contract's
// resolve route does: the git host followed by a slash, then owner and
// name with .git stripped, matched without case.
func (g *grantServer) lookup(upstream string) (string, bool) {
	rest, ok := strings.CutPrefix(upstream, g.upstream+"/")
	if !ok {
		return "", false
	}
	owner, name, ok := strings.Cut(strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git"), "/")
	if !ok || strings.Contains(name, "/") {
		return "", false
	}
	canon, ok := g.repos[strings.ToLower(owner+"/"+name)]
	return canon, ok
}

func (g *grantServer) read(w http.ResponseWriter, r *http.Request) (grantAsk, sessionClaims, bool) {
	var ask grantAsk
	ses, err := g.session(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return ask, ses, false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ask); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed body"})
		return ask, ses, false
	}
	return ask, ses, true
}

func (g *grantServer) notListed(w http.ResponseWriter, upstream string) {
	writeJSON(w, http.StatusNotFound, map[string]string{"code": "not_listed",
		"error": "Straza: " + upstream + " is not in Straza's list of ghapp's repos yet."})
}

func (g *grantServer) resolve(w http.ResponseWriter, r *http.Request) {
	ask, _, ok := g.read(w, r)
	if !ok {
		return
	}
	g.mu.Lock()
	g.resolved = append(g.resolved, ask.Upstream)
	g.mu.Unlock()
	canon, ok := g.lookup(ask.Upstream)
	if !ok {
		g.notListed(w, ask.Upstream)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"srv": "ghapp", "repo": canon, "aud": g.aud})
}

func (g *grantServer) grant(w http.ResponseWriter, r *http.Request) {
	ask, ses, ok := g.read(w, r)
	if !ok {
		return
	}
	g.mu.Lock()
	g.asked = append(g.asked, ask)
	g.mu.Unlock()
	canon, ok := g.lookup(ask.Upstream)
	if !ok {
		g.notListed(w, ask.Upstream)
		return
	}
	names := func(ref string) bool {
		return slices.ContainsFunc(ask.Refs, func(r localapi.GitRef) bool { return r.Ref == ref })
	}
	apr := ""
	switch {
	case ask.Op == "push" && names(refBlocked):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": refusedSentence})
		return
	case ask.Op == "push" && names(refHeld):
		if !g.hold(w, r, ask) {
			return
		}
		apr = approvalID
	}
	now := time.Now()
	claims := map[string]any{
		"iss": g.iss, "aud": g.aud, "sub": ses.Sub,
		"act": map[string]string{"ses": ses.Ses, "hrn": ses.Hrn, "kind": "human"},
		"iat": now.Unix(), "exp": now.Add(grantLife).Unix(), "jti": randHex(16),
		"cnf": map[string]string{"jkt": ses.Cnf.JKT}, "srv": "ghapp",
		"obj": map[string]string{"type": "repo", "value": canon, "id": "1"},
		"op":  ask.Op, "dec": randHex(16),
	}
	if len(ask.Refs) > 0 {
		claims["refs"] = ask.Refs
	}
	if apr != "" {
		claims["apr"] = apr
	}
	grant := signJWT(g.key, map[string]string{"alg": "EdDSA", "typ": "straza-grant+jwt", "kid": "e2e"}, claims)
	g.mu.Lock()
	g.issued = append(g.issued, grant)
	g.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"grant": grant, "aud": g.aud, "repo": canon, "srv": "ghapp",
		"exp": now.Add(grantLife).UTC().Format(time.RFC3339)})
}

// hold plays an approval that an approver allows holdFor after the first
// ask: each ask waits up to its waitSeconds, then answers 202 while the
// approval is open. It reports true once the approval is allowed.
func (g *grantServer) hold(w http.ResponseWriter, r *http.Request, ask grantAsk) bool {
	key, _ := json.Marshal(ask.Refs)
	g.mu.Lock()
	first, seen := g.held[string(key)]
	if !seen {
		first = time.Now()
		g.held[string(key)] = first
	}
	g.mu.Unlock()
	wait := 0
	if ask.WaitSeconds != nil {
		wait = *ask.WaitSeconds
	}
	if left := g.holdFor - time.Since(first); left > 0 {
		select {
		case <-time.After(min(left, time.Duration(wait)*time.Second)):
		case <-r.Context().Done():
			return false
		}
	}
	if time.Since(first) >= g.holdFor {
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pending": map[string]any{"ref": approvalID,
		"expiresAt": first.Add(2 * time.Minute).UTC().Format(time.RFC3339)}})
	return false
}

// asks returns the grant requests the fake saw for repo, a path suffix
// such as /acme/pay-api.git.
func (g *grantServer) asks(repo string) []grantAsk {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []grantAsk
	for _, a := range g.asked {
		if strings.HasSuffix(a.Upstream, repo) {
			out = append(out, a)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func b64JSON(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// signJWT signs header and claims as a compact JWS with key.
func signJWT(key ed25519.PrivateKey, header, claims any) string {
	in := b64JSON(header) + "." + b64JSON(claims)
	return in + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(in)))
}

func decodePart(part string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
