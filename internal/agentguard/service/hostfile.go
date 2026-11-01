package service

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/agentguard"
)

// systemHostFile returns the git host file of the system service on goos
// other than Windows, the path the rendered harness wiring names, or ""
// where the service writes none yet. On Linux it sits in the socket's
// folder, the one folder an agent container shares with its service
// container. It is a variable only so the package's tests write elsewhere.
var systemHostFile = agentguard.GitHostsFile

// maxUpstreamLen bounds one upstream in the host file. A git host's base
// URL is far shorter.
const maxUpstreamLen = 2048

// hostFilePath returns where the service writes the git host file: in the
// state folder for a user-mode service and for the system service on
// Windows, else the system path of this system, "" where the service
// writes none yet. The Windows state folder sits on the volume Windows
// runs from, so on a default machine its host file is the one the
// rendered wiring names.
func hostFilePath(opts Options) string {
	if opts.UserMode || runtime.GOOS == "windows" {
		return filepath.Join(opts.StateDir, "git", "hosts.conf")
	}
	return systemHostFile(runtime.GOOS)
}

// writeHostFile writes the git host file from the git upstreams of one
// check-in answer, so git in agent processes hands exactly those hosts to
// the straza helper. An answer with none writes an empty file. It writes
// only when the bytes change, so a git that holds the file open on Windows
// cannot block an update that changes nothing. It logs each upstream it
// leaves out and a write that failed, and never fails the check-in.
func (s *credStore) writeHostFile(upstreams []string) {
	path := hostFilePath(s.opts)
	if path == "" {
		return
	}
	body, skipped := hostFileBody(upstreams)
	for _, sk := range skipped {
		s.log.Warn("the Straza server named a git upstream that is not valid, so the git host file leaves it out",
			"upstream", sk.raw, "reason", sk.why)
	}
	if err := writePublicFile(path, []byte(body)); err != nil {
		s.log.Error("the git host file could not be written, so git on this machine keeps the git settings it had",
			"path", path, "err", err)
	}
}

// skippedUpstream is an upstream the host file leaves out, cut to 256
// bytes, and why.
type skippedUpstream struct{ raw, why string }

// hostFileBody renders the host file for upstreams: for each valid one,
// sorted and without repeats, a url section that rewrites its own form to
// the straza transport, then the protocol line that lets git run that
// transport for submodules too. Only an https upstream with a bare host,
// no port and no path, also rewrites the two ssh forms of that host,
// because no ssh form maps to a port or a path prefix. No valid upstream
// renders an empty body.
func hostFileBody(upstreams []string) (string, []skippedUpstream) {
	var valid []upstream
	var skipped []skippedUpstream
	for _, raw := range upstreams {
		u, why := parseUpstream(raw)
		if why != "" {
			skipped = append(skipped, skippedUpstream{raw: cut(raw, 256), why: why})
			continue
		}
		valid = append(valid, u)
	}
	slices.SortFunc(valid, func(a, b upstream) int { return strings.Compare(a.base, b.base) })
	valid = slices.CompactFunc(valid, func(a, b upstream) bool { return a.base == b.base })
	if len(valid) == 0 {
		return "", skipped
	}
	var b strings.Builder
	for _, u := range valid {
		fmt.Fprintf(&b, "[url \"straza::%s/\"]\n", u.base)
		fmt.Fprintf(&b, "\tinsteadOf = %s/\n", u.base)
		if u.bareHost != "" {
			fmt.Fprintf(&b, "\tinsteadOf = git@%s:\n", u.bareHost)
			fmt.Fprintf(&b, "\tinsteadOf = ssh://git@%s/\n", u.bareHost)
		}
	}
	b.WriteString("[protocol \"straza\"]\n\tallow = always\n")
	return b.String(), skipped
}

// upstream is one valid git host: its canonical base URL, and its host
// when the upstream is https with a bare host, the only form whose ssh
// forms the host file rewrites too.
type upstream struct{ base, bareHost string }

// parseUpstream checks raw as the manifest parser canonicalizes a git
// upstream: https, or http on a loopback host, a lower-case scheme and
// host, no default port, no userinfo, query, fragment or trailing slash,
// and only characters that need no quoting in a git config file. It
// returns the reason when raw is not valid.
func parseUpstream(raw string) (upstream, string) {
	if len(raw) == 0 || len(raw) > maxUpstreamLen {
		return upstream{}, fmt.Sprintf("it is empty or longer than %d bytes", maxUpstreamLen)
	}
	for _, c := range raw {
		if !upstreamChar(c) {
			return upstream{}, fmt.Sprintf("it holds the character %q, which a git config value cannot carry unquoted", c)
		}
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return upstream{}, "it is not a URL: " + err.Error()
	case !secureScheme(u):
		return upstream{}, "it is neither https nor http on a loopback host"
	case u.Host == "" || u.Opaque != "" || u.User != nil || u.ForceQuery:
		return upstream{}, "it has no host, or it has a user name or a query"
	case u.Host != strings.ToLower(u.Host) || defaultPort(u):
		return upstream{}, "its host is not canonical: upper-case letters or a default port"
	case strings.HasSuffix(u.Path, "/") || strings.Contains(u.Path, "//") || dotSegment(u.Path):
		return upstream{}, "its path ends with a slash or has an empty, . or .. segment"
	}
	up := upstream{base: u.Scheme + "://" + u.Host + u.Path}
	if u.Scheme == "https" && u.Port() == "" && u.Path == "" {
		up.bareHost = u.Host
	}
	return up, ""
}

// upstreamChar reports whether c may appear in an upstream: letters,
// digits and the URL characters the host and path forms need, but no
// quote, backslash, comment character, space, @, ?, # or percent sign.
func upstreamChar(c rune) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.ContainsRune(".-_~/:[]", c)
}

// secureScheme reports whether u is https, or http on a loopback host.
func secureScheme(u *url.URL) bool {
	return u.Scheme == "https" || u.Scheme == "http" && loopbackHost(u.Hostname())
}

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// defaultPort reports whether u names its scheme's default port.
func defaultPort(u *url.URL) bool {
	p := u.Port()
	return u.Scheme == "https" && p == "443" || u.Scheme == "http" && p == "80"
}

// dotSegment reports whether path has a . or .. segment.
func dotSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// cut returns s, or its first n bytes when it is longer.
func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// writePublicFile writes data to path, readable by every local user,
// through a synced temp file in the same folder that replaces the target
// in one rename. prepareGitDir readies the folder first, and a file that
// already holds data stays as it is. On Unix the file gets mode 0644, and
// on Windows it inherits the folder's DACL.
func writePublicFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := prepareGitDir(dir); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) { // #nosec G304 -- the service's own host file
		return nil
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(0o644) // #nosec G302 -- git in every local user's agent reads the host file
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return replaceDurably(tmp, path)
}
