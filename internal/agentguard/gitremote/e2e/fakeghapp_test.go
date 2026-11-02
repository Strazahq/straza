package e2e

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/dpop"
)

// grantClaims is what the fake resource server reads from a grant.
type grantClaims struct {
	Aud string            `json:"aud"`
	Iat int64             `json:"iat"`
	Exp int64             `json:"exp"`
	JTI string            `json:"jti"`
	Op  string            `json:"op"`
	Obj map[string]string `json:"obj"`
	Cnf struct {
		JKT string `json:"jkt"`
	} `json:"cnf"`
	Refs []localapi.GitRef `json:"refs"`
}

// resourceServer stands in for ghapp: it serves smart HTTP at
// <base>/git/<owner>/<name>.git/ and checks each request as contract 20.12
// says a resource server must, then runs git on the bare repo. Every
// failed check lands in faults, which the rows want empty.
type resourceServer struct {
	base  string
	pub   ed25519.PublicKey
	repos string

	mu     sync.Mutex
	jtis   map[string]bool
	spent  map[string]bool
	proofs []string
	faults []string
	pushes [][]localapi.GitRef
}

func (s *resourceServer) fault(format string, a ...any) string {
	f := fmt.Sprintf(format, a...)
	s.mu.Lock()
	s.faults = append(s.faults, f)
	s.mu.Unlock()
	return f
}

func (s *resourceServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, _ := strings.CutPrefix(r.URL.Path, "/git/")
	repo, tail, ok := strings.Cut(rest, ".git/")
	if !ok || strings.Count(repo, "/") != 1 {
		http.Error(w, s.fault("path %s is not a repo route", r.URL.Path), http.StatusNotFound)
		return
	}
	service, op := tail, ""
	if tail == "info/refs" {
		service = r.URL.Query().Get("service")
	}
	switch service {
	case "git-upload-pack":
		op = "fetch"
	case "git-receive-pack":
		op = "push"
	default:
		http.Error(w, s.fault("route %s names no git service", r.URL.String()), http.StatusNotFound)
		return
	}
	c, why := s.check(r, repo)
	if why == "" && c.Op != op {
		why = s.fault("a %s grant was sent to %s", c.Op, r.URL.String())
	}
	if why != "" {
		http.Error(w, "fake ghapp refused the request: "+why, http.StatusForbidden)
		return
	}
	var body io.Reader = r.Body
	if tail == "git-receive-pack" {
		head, refs, err := readCommands(r.Body)
		if err != nil {
			http.Error(w, s.fault("push commands: %v", err), http.StatusBadRequest)
			return
		}
		if len(refs) > 0 {
			s.mu.Lock()
			spent := s.spent[c.JTI]
			s.spent[c.JTI] = true
			s.pushes = append(s.pushes, refs)
			s.mu.Unlock()
			if spent || !sameRefs(refs, c.Refs) {
				http.Error(w, s.fault("push commands %v against grant refs %v, spent %v", refs, c.Refs, spent), http.StatusForbidden)
				return
			}
		}
		body = io.MultiReader(bytes.NewReader(head), r.Body)
	}
	serveGit(w, r, filepath.Join(s.repos, repo+".git"), service, tail == "info/refs", body)
}

// check verifies the grant and the proof of r for repo and returns the
// grant's claims, or the fault.
func (s *resourceServer) check(r *http.Request, repo string) (grantClaims, string) {
	var c grantClaims
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "straza-") || k == "Cookie" {
			return c, s.fault("the header %s reached the resource server", k)
		}
	}
	grant, ok := strings.CutPrefix(r.Header.Get("Authorization"), "DPoP ")
	parts := strings.Split(grant, ".")
	if !ok || len(parts) != 3 {
		return c, s.fault("no DPoP grant in Authorization")
	}
	var hdr map[string]string
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	switch {
	case err != nil || !ed25519.Verify(s.pub, []byte(parts[0]+"."+parts[1]), sig):
		return c, s.fault("the grant's signature does not verify")
	case decodePart(parts[0], &hdr) != nil || hdr["typ"] != "straza-grant+jwt" || hdr["alg"] != "EdDSA":
		return c, s.fault("the grant header %v", hdr)
	case decodePart(parts[1], &c) != nil:
		return c, s.fault("the grant claims do not decode")
	}
	now := time.Now().Unix()
	switch {
	case c.Aud != s.base:
		return c, s.fault("aud %q is not %q", c.Aud, s.base)
	case c.Exp <= now || c.Iat > now+60 || c.Exp-c.Iat > 300:
		return c, s.fault("the grant's times iat %d exp %d at %d", c.Iat, c.Exp, now)
	case c.Obj["value"] != repo:
		return c, s.fault("obj %v is not %s", c.Obj, repo)
	}
	proofs := r.Header.Values("DPoP")
	if len(proofs) != 1 {
		return c, s.fault("%d DPoP proofs", len(proofs))
	}
	thumb, jti, err := verifyProof(proofs[0], r.Method, s.base+r.URL.Path, grant)
	s.mu.Lock()
	s.proofs = append(s.proofs, proofs[0])
	reused := s.jtis[jti]
	s.jtis[jti] = true
	s.mu.Unlock()
	switch {
	case err != nil:
		return c, s.fault("proof: %v", err)
	case reused:
		return c, s.fault("proof jti %s used twice", jti)
	case thumb != c.Cnf.JKT:
		return c, s.fault("the proof key %s is not the grant's cnf.jkt %s", thumb, c.Cnf.JKT)
	}
	return c, ""
}

// verifyProof checks a DPoP proof for one request and returns the
// thumbprint of its key and its jti.
func verifyProof(proof, method, htu, grant string) (string, string, error) {
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("not a compact JWS")
	}
	var hdr struct {
		Typ string            `json:"typ"`
		Alg string            `json:"alg"`
		JWK map[string]string `json:"jwk"`
	}
	var c struct {
		HTM   string `json:"htm"`
		HTU   string `json:"htu"`
		IAT   int64  `json:"iat"`
		JTI   string `json:"jti"`
		ATH   string `json:"ath"`
		Nonce string `json:"nonce"`
	}
	if decodePart(parts[0], &hdr) != nil || decodePart(parts[1], &c) != nil {
		return "", "", fmt.Errorf("the proof does not decode")
	}
	x, err := base64.RawURLEncoding.DecodeString(hdr.JWK["x"])
	sig, serr := base64.RawURLEncoding.DecodeString(parts[2])
	switch {
	case hdr.Typ != dpop.ProofType || hdr.Alg != "EdDSA" || hdr.JWK["kty"] != "OKP" || hdr.JWK["crv"] != "Ed25519" ||
		err != nil || len(x) != ed25519.PublicKeySize:
		return "", "", fmt.Errorf("the proof header %+v", hdr)
	case serr != nil || !ed25519.Verify(ed25519.PublicKey(x), []byte(parts[0]+"."+parts[1]), sig):
		return "", "", fmt.Errorf("the proof signature does not verify")
	case c.HTM != method || c.HTU != htu:
		return "", "", fmt.Errorf("htm %s htu %s, want %s %s", c.HTM, c.HTU, method, htu)
	case c.ATH != dpop.AccessTokenHash(grant):
		return "", "", fmt.Errorf("ath does not hash the grant")
	case c.Nonce != "":
		return "", "", fmt.Errorf("the proof carries Straza's nonce")
	case c.JTI == "" || c.IAT < time.Now().Unix()-60 || c.IAT > time.Now().Unix()+60:
		return "", "", fmt.Errorf("jti %q iat %d", c.JTI, c.IAT)
	}
	return dpop.Thumbprint(ed25519.PublicKey(x)), c.JTI, nil
}

// readCommands reads a receive-pack command section up to its flush and
// returns the bytes it read and the ref updates.
func readCommands(r io.Reader) ([]byte, []localapi.GitRef, error) {
	var head []byte
	var refs []localapi.GitRef
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return head, refs, fmt.Errorf("read a pkt-line length: %w", err)
		}
		head = append(head, hdr[:]...)
		n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
		switch {
		case err != nil:
			return head, refs, err
		case n == 0:
			return head, refs, nil
		case n < 4:
			return head, refs, fmt.Errorf("pkt-line length %d", n)
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(r, line); err != nil {
			return head, refs, err
		}
		head = append(head, line...)
		cmd, _, _ := strings.Cut(strings.TrimSuffix(string(line), "\n"), "\x00")
		if strings.HasPrefix(cmd, "shallow ") {
			continue
		}
		f := strings.Fields(cmd)
		if len(f) != 3 {
			return head, refs, fmt.Errorf("command %q", cmd)
		}
		refs = append(refs, localapi.GitRef{Ref: f[2], Old: f[0], New: f[1]})
	}
}

// sameRefs reports whether a and b hold the same updates as a multiset.
func sameRefs(a, b []localapi.GitRef) bool {
	key := func(r localapi.GitRef) string { return r.Ref + " " + r.Old + " " + r.New }
	ka, kb := make([]string, 0, len(a)), make([]string, 0, len(b))
	for _, r := range a {
		ka = append(ka, key(r))
	}
	for _, r := range b {
		kb = append(kb, key(r))
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}

// serveGit answers one smart HTTP request for the bare repo dir by running
// `git upload-pack` or `git receive-pack --stateless-rpc` directly, as
// `git http-backend` would, without the CGI layer that refuses a chunked
// push over 1 MiB.
func serveGit(w http.ResponseWriter, r *http.Request, dir, service string, advertise bool, body io.Reader) {
	if _, err := os.Stat(dir); err != nil {
		http.Error(w, "no such repo", http.StatusNotFound)
		return
	}
	args := []string{strings.TrimPrefix(service, "git-"), "--stateless-rpc"}
	if advertise {
		args = append(args, "--advertise-refs")
	}
	cmd := exec.CommandContext(r.Context(), "git", append(args, dir)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+filepath.Dir(dir))
	v2 := strings.Contains(r.Header.Get("Git-Protocol"), "version=2")
	if v2 {
		cmd.Env = append(cmd.Env, "GIT_PROTOCOL=version=2")
	}
	if advertise {
		w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
		if !v2 {
			_, _ = io.WriteString(w, pktLine("# service="+service+"\n")+"0000")
		}
	} else {
		_ = http.NewResponseController(w).EnableFullDuplex()
		w.Header().Set("Content-Type", "application/x-"+service+"-result")
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(body)
			if err != nil {
				http.Error(w, "bad gzip body", http.StatusBadRequest)
				return
			}
			body = gz
		}
		cmd.Stdin = body
	}
	cmd.Stdout = flushWriter{w}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
}

// flushWriter flushes each write to the client, so git's progress and
// answers stream.
type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	_ = http.NewResponseController(f.w).Flush()
	return n, err
}

func pktLine(payload string) string { return fmt.Sprintf("%04x", len(payload)+4) + payload }
