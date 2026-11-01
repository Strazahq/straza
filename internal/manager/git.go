package manager

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/redact"
)

// Git ops a git block maps to tools.
const (
	GitOpFetch = "fetch"
	GitOpPush  = "push"
)

// GitSpec is the straza.git block: the resource server's public base URL, the git
// host it serves, and the tools its two ops map to.
type GitSpec struct {
	URL      string `yaml:"url" json:"url"`
	Upstream string `yaml:"upstream" json:"upstream"`
	Ops      GitOps `yaml:"ops" json:"ops"`
}

// GitOps maps the fetch and push ops to tool names.
type GitOps struct {
	Fetch string `yaml:"fetch,omitempty" json:"fetch,omitempty"`
	Push  string `yaml:"push,omitempty" json:"push,omitempty"`
}

// UnmarshalYAML reads the ops mapping and refuses a key other than fetch
// and push, a key given twice, and a value that is not a non-empty string,
// so a key that is present always names a tool, as the schema requires.
func (o *GitOps) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: git.ops must be a mapping of fetch and push to tool names, such as {fetch: git_fetch, push: git_push}", n.Line)
	}
	var out GitOps
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, v := n.Content[i], n.Content[i+1]
		var slot *string
		switch key.Value {
		case GitOpFetch:
			slot = &out.Fetch
		case GitOpPush:
			slot = &out.Push
		default:
			return fmt.Errorf("line %d: git.ops maps only fetch and push, so the key %q has no meaning. Remove it, or name the op fetch or push", key.Line, key.Value)
		}
		if seen[key.Value] {
			return fmt.Errorf("line %d: git.ops names %q twice. Keep one entry for it", key.Line, key.Value)
		}
		seen[key.Value] = true
		if v.Kind != yaml.ScalarNode || v.Tag != "!!str" || v.Value == "" {
			return fmt.Errorf("line %d: git.ops.%s does not name a tool. Write the tool's name as the server lists it, such as %[2]s: git_%[2]s, or leave %[2]s out of ops", v.Line, key.Value)
		}
		*slot = v.Value
	}
	*o = out
	return nil
}

// Tool answers the tool op maps to, or "" when the block does not map op.
// A nil block maps nothing.
func (g *GitSpec) Tool(op string) string {
	if g == nil {
		return ""
	}
	switch op {
	case GitOpFetch:
		return g.Ops.Fetch
	case GitOpPush:
		return g.Ops.Push
	}
	return ""
}

// validateGit checks the straza.git block against SPEC.md section 8 and
// stores the canonical form of both URLs, so the grant's aud is the URL
// byte for byte. The JSON Schema mirrors every rule but the reach of a
// mapped tool, since a schema cannot tie a value of ops to a key of
// objects.tools. A manifest without the block passes untouched.
func validateGit(m *Manifest, fail func(string, ...any)) {
	g := m.Straza.Git
	if g == nil {
		return
	}
	if u, err := canonicalGitURL("git.url", "the resource server's public base URL, such as https://ghapp.example.com", g.URL); err != nil {
		fail("%v", err)
	} else {
		g.URL = u
	}
	if u, err := canonicalGitURL("git.upstream", "the git host's base URL, such as https://github.com", g.Upstream); err != nil {
		fail("%v", err)
	} else {
		g.Upstream = u
	}
	if g.Ops.Fetch == "" && g.Ops.Push == "" {
		fail("git.ops maps neither fetch nor push, so Straza has no tool to decide a git grant with. Map fetch, push or both to the server's tools, such as fetch: git_fetch and push: git_push")
	}
	o := m.Straza.Objects
	if o == nil {
		fail("the git block needs an objects block beside it, because each git grant names one repo from the server's object list. Add objects with the object type, the listing tool and {argument: <name>} for each tool under git.ops")
	}
	for _, op := range []string{GitOpFetch, GitOpPush} {
		tool := g.Tool(op)
		switch {
		case tool == "":
		case !mcpToolNameRe.MatchString(tool):
			fail("git.ops.%s names %q, which is not a valid MCP tool name. Write the tool's name as the server lists it: 1 to 128 letters, digits, underscores, hyphens or dots", op, tool)
		case o == nil:
		default:
			gitToolPin(op, tool, o, fail)
		}
	}
}

// gitToolPin refuses a mapped tool whose pin is not an argument: a grant
// names exactly one object, and the object gate, the approval and its card
// read that object from the argument the pin names.
func gitToolPin(op, tool string, o *ObjectsSpec, fail func(string, ...any)) {
	r, pinned := o.Tools[tool]
	switch {
	case !pinned:
		fail("git.ops.%s names %s, which objects.tools does not pin, so it reaches every %s. A git grant names one %[3]s, so pin %[2]s under objects.tools to {argument: <name>} with the argument that carries the %[3]s", op, tool, o.Type)
	case r.Reach != ReachArgument:
		fail("git.ops.%s names %s, which objects.tools pins to the reach %s. A git grant names one %s, so pin %[2]s to {argument: <name>} with the argument that carries the %[4]s", op, tool, reachWords(r), o.Type)
	}
}

// canonicalGitURL answers the canonical form of a git block URL: the scheme
// and host in lower case, no port when it is the scheme's default, and no
// trailing slash. It refuses a URL that is not absolute, a scheme other than
// https, http on a host that is not loopback, userinfo, a query and a
// fragment. field names the key and what says what the key holds. A
// refusal quotes the URL redacted or not at all, because a URL can carry a
// secret.
func canonicalGitURL(field, what, raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%s is empty. Write %s", field, what)
	}
	shown := redact.URL(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
		return "", fmt.Errorf("%s %q is not an absolute URL. Write %s", field, shown, what)
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
	switch {
	case scheme == "https":
	case scheme == "http" && !loopbackHost(host):
		return "", fmt.Errorf("%s %q uses http on %s, which is not this machine, so git grants and repo data would cross the network unencrypted. Write an https URL, or keep http for a loopback host such as localhost or 127.0.0.1", field, shown, host)
	case scheme != "http":
		return "", fmt.Errorf("%s %q uses the scheme %s. Write %s", field, shown, scheme, what)
	}
	if u.User != nil {
		return "", fmt.Errorf("%s carries a user name or password before the host. Remove it, because Straza signs each git grant itself and a manifest must hold no secret. Write %s", field, what)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("%s carries a query. Remove the part from the question mark on, because Straza adds the paths git needs to this base URL", field)
	}
	if strings.Contains(raw, "#") {
		return "", fmt.Errorf("%s carries a fragment. Remove the part from the # on, because Straza adds the paths git needs to this base URL", field)
	}
	port := u.Port()
	if scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// loopbackHost reports whether a lower-case host names this machine only:
// localhost, ::1 or a dotted IPv4 address in 127.0.0.0/8. Other IPv6 forms
// of a loopback address do not count, so the schema's pattern can say the
// same rule.
func loopbackHost(host string) bool {
	if host == "localhost" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && !strings.Contains(host, ":") && ip.IsLoopback()
}
