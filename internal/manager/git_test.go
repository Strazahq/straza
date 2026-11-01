package manager

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gitObjects is an objects block that pins both git tools to the argument
// repo, indented under straza:.
const gitObjects = `  objects:
    type: repo
    list: {tool: repo_list}
    tools:
      git_fetch: {argument: repo}
      git_push: {argument: repo}
`

// gitManifest is a valid remote App with the given objects block and a git
// block of url, upstream and the ops mapping in YAML flow form.
func gitManifest(objects, url, upstream, ops string) []byte {
	return []byte(`apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: ghapp}
server: {name: example/ghapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: https://ghapp.example.com/mcp}
` + objects + `  git:
    url: ` + strconv.Quote(url) + `
    upstream: ` + strconv.Quote(upstream) + `
    ops: ` + ops + "\n")
}

const bothOps = "{fetch: git_fetch, push: git_push}"

func TestGitBlockCanonicalURLs(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		upstream     string
		wantURL      string
		wantUpstream string
	}{
		{"already canonical", "https://ghapp.example.com", "https://github.com", "https://ghapp.example.com", "https://github.com"},
		{"scheme and host lower-cased", "HTTPS://GHApp.Example.COM", "Https://GitHub.com", "https://ghapp.example.com", "https://github.com"},
		{"trailing slash dropped", "https://ghapp.example.com/", "https://github.com/", "https://ghapp.example.com", "https://github.com"},
		{"default https port dropped", "https://ghapp.example.com:443", "https://github.com:443/", "https://ghapp.example.com", "https://github.com"},
		{"other port kept", "https://ghapp.example.com:8443/", "https://git.example.com:8443", "https://ghapp.example.com:8443", "https://git.example.com:8443"},
		{"path kept with its case", "https://example.com/GHApp/", "https://git.example.com/scm//", "https://example.com/GHApp", "https://git.example.com/scm"},
		{"http on 127.0.0.1", "http://127.0.0.1:3003", "https://github.com", "http://127.0.0.1:3003", "https://github.com"},
		{"http on 127.0.0.0/8", "http://127.1.2.3:3003/", "https://github.com", "http://127.1.2.3:3003", "https://github.com"},
		{"http on localhost with the default port", "http://LOCALHOST:80/", "https://github.com", "http://localhost", "https://github.com"},
		{"http on ::1", "http://[::1]:8080/", "https://github.com", "http://[::1]:8080", "https://github.com"},
		{"https on an IPv6 host", "https://[2001:DB8::1]:443/", "https://github.com", "https://[2001:db8::1]", "https://github.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Parse(gitManifest(gitObjects, tc.url, tc.upstream, bothOps))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			g := m.Straza.Git
			if g == nil {
				t.Fatal("Parse dropped the git block")
			}
			if g.URL != tc.wantURL || g.Upstream != tc.wantUpstream {
				t.Errorf("url, upstream = %q, %q, want %q, %q", g.URL, g.Upstream, tc.wantURL, tc.wantUpstream)
			}
		})
	}
}

func TestGitBlockRefusals(t *testing.T) {
	const good, hub = "https://ghapp.example.com", "https://github.com"
	cases := []struct {
		name     string
		objects  string
		url      string
		upstream string
		ops      string
		want     string
		hidden   string // a part of the input the refusal must not echo
	}{
		{"empty url", gitObjects, "", hub, bothOps, "git.url is empty", ""},
		{"empty upstream", gitObjects, good, "", bothOps, "git.upstream is empty", ""},
		{"relative url", gitObjects, "ghapp.example.com", hub, bothOps, `git.url "ghapp.example.com" is not an absolute URL`, ""},
		{"no host", gitObjects, "https:///git", hub, bothOps, "is not an absolute URL", ""},
		{"opaque url", gitObjects, "https:ghapp.example.com", hub, bothOps, "is not an absolute URL", ""},
		{"ssh upstream", gitObjects, good, "ssh://git@github.com", bothOps, `git.upstream "ssh://github.com" uses the scheme ssh`, ""},
		{"ftp scheme", gitObjects, "ftp://ghapp.example.com", hub, bothOps, "uses the scheme ftp", ""},
		{"http on a public host", gitObjects, "http://ghapp.example.com", hub, bothOps, "git.url \"http://ghapp.example.com\" uses http on ghapp.example.com", ""},
		{"http on a private address", gitObjects, good, "http://10.0.0.7", bothOps, "git.upstream \"http://10.0.0.7\" uses http on 10.0.0.7", ""},
		{"http on a name that only starts like loopback", gitObjects, "http://127.0.0.1.example.com", hub, bothOps, "uses http on 127.0.0.1.example.com", ""},
		{"http on an IPv4-mapped loopback", gitObjects, "http://[::ffff:127.0.0.1]", hub, bothOps, "uses http on", ""},
		{"userinfo with a password", gitObjects, "https://bot:s3cret@ghapp.example.com", hub, bothOps, "git.url carries a user name or password", "s3cret"},
		{"empty userinfo", gitObjects, good, "https://@github.com", bothOps, "git.upstream carries a user name or password", ""},
		{"query", gitObjects, "https://ghapp.example.com/?token=abc123", hub, bothOps, "git.url carries a query", "abc123"},
		{"empty query", gitObjects, "https://ghapp.example.com/?", hub, bothOps, "git.url carries a query", ""},
		{"fragment", gitObjects, good, "https://github.com/#top", bothOps, "git.upstream carries a fragment", ""},
		{"empty fragment", gitObjects, good, "https://github.com/#", bothOps, "git.upstream carries a fragment", ""},
		{"ops maps neither op", gitObjects, good, hub, "{}", "git.ops maps neither fetch nor push", ""},
		{"ops maps a tool with a bad name", gitObjects, good, hub, `{push: "git push"}`, `git.ops.push names "git push", which is not a valid MCP tool name`, ""},
		{"ops carries an unknown op", gitObjects, good, hub, "{fetch: git_fetch, advertise: git_push}", `git.ops maps only fetch and push, so the key "advertise" has no meaning`, ""},
		{"ops maps fetch to an empty string beside push", gitObjects, good, hub, `{fetch: "", push: git_push}`, "git.ops.fetch does not name a tool", ""},
		{"ops maps fetch to null beside push", gitObjects, good, hub, "{fetch: ~, push: git_push}", "git.ops.fetch does not name a tool", ""},
		{"ops maps push to a bare key beside fetch", gitObjects, good, hub, "{fetch: git_fetch, push: }", "git.ops.push does not name a tool", ""},
		{"ops maps push to a number", gitObjects, good, hub, "{push: 5}", "git.ops.push does not name a tool", ""},
		{"ops maps push to a boolean", gitObjects, good, hub, "{push: true}", "git.ops.push does not name a tool", ""},
		{"ops maps push to a list", gitObjects, good, hub, "{push: [git_push]}", "git.ops.push does not name a tool", ""},
		{"ops maps push twice", gitObjects, good, hub, "{push: git_push, push: git_fetch}", `git.ops names "push" twice`, ""},
		{"ops is null", gitObjects, good, hub, "~", "git.ops maps neither fetch nor push", ""},
		{"ops is a list", gitObjects, good, hub, "[git_fetch]", "git.ops must be a mapping of fetch and push to tool names", ""},
		{"no objects block", "", good, hub, bothOps, "the git block needs an objects block", ""},
		{"mapped tool the objects block does not pin", "  objects:\n    type: repo\n    list: {tool: repo_list}\n    tools:\n      git_fetch: {argument: repo}\n", good, hub, bothOps,
			"git.ops.push names git_push, which objects.tools does not pin, so it reaches every repo", ""},
		{"mapped tool pinned all", "  objects:\n    type: repo\n    list: {tool: repo_list}\n    tools:\n      git_fetch: all\n      git_push: {argument: repo}\n", good, hub, bothOps,
			"git.ops.fetch names git_fetch, which objects.tools pins to the reach all", ""},
		{"mapped tool pinned none", "  objects:\n    type: repo\n    list: {tool: repo_list}\n    tools:\n      git_fetch: {argument: repo}\n      git_push: none\n", good, hub, bothOps,
			"git.ops.push names git_push, which objects.tools pins to the reach none", ""},
		{"mapped tool pinned credential", "  objects:\n    type: repo\n    list: {tool: repo_list}\n    tools:\n      git_push: credential\n", good, hub, "{push: git_push}",
			"git.ops.push names git_push, which objects.tools pins to the reach credential", ""},
		{"listing tool mapped", gitObjects, good, hub, "{fetch: repo_list}", "git.ops.fetch names repo_list, which objects.tools does not pin", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(gitManifest(tc.objects, tc.url, tc.upstream, tc.ops))
			if err == nil {
				t.Fatalf("Parse accepted the manifest, want a refusal naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q, want one naming %q", err, tc.want)
			}
			if tc.hidden != "" && strings.Contains(err.Error(), tc.hidden) {
				t.Errorf("refusal %q echoes %q", err, tc.hidden)
			}
		})
	}
}

// TestGitBlockOneOp pins that a block may map one op alone.
func TestGitBlockOneOp(t *testing.T) {
	for _, ops := range []string{"{fetch: git_fetch}", "{push: git_push}"} {
		if _, err := Parse(gitManifest(gitObjects, "https://ghapp.example.com", "https://github.com", ops)); err != nil {
			t.Errorf("ops %s: %v", ops, err)
		}
	}
}

func TestGitSpecTool(t *testing.T) {
	both := &GitSpec{Ops: GitOps{Fetch: "git_fetch", Push: "git_push"}}
	fetchOnly := &GitSpec{Ops: GitOps{Fetch: "git_fetch"}}
	cases := []struct {
		name string
		spec *GitSpec
		op   string
		want string
	}{
		{"fetch", both, GitOpFetch, "git_fetch"},
		{"push", both, GitOpPush, "git_push"},
		{"an op the block does not map", fetchOnly, GitOpPush, ""},
		{"an unknown op", both, "advertise", ""},
		{"an op in another case", both, "Push", ""},
		{"no git block", nil, GitOpFetch, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Tool(tc.op); got != tc.want {
				t.Errorf("Tool(%q) = %q, want %q", tc.op, got, tc.want)
			}
		})
	}
}

// TestGitBlockStoredForm pins that the stored JSON carries the canonical
// URLs under straza.git and that reopening it keeps them.
func TestGitBlockStoredForm(t *testing.T) {
	m, err := Parse(gitManifest(gitObjects, "HTTPS://GHApp.Example.com:443/", "https://GitHub.com/", "{push: git_push}"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	raw, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if want := `"git":{"url":"https://ghapp.example.com","upstream":"https://github.com","ops":{"push":"git_push"}}`; !strings.Contains(raw, want) {
		t.Errorf("stored form %s, want it to hold %s", raw, want)
	}
	back, err := FromJSON(raw)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if g := back.Straza.Git; g == nil || g.URL != "https://ghapp.example.com" || g.Tool(GitOpPush) != "git_push" {
		t.Errorf("reopened git block = %+v", g)
	}
	plain, err := Parse(objectsManifest(goodObjects))
	if err != nil {
		t.Fatalf("Parse without git: %v", err)
	}
	if raw, _ := plain.JSON(); strings.Contains(raw, `"git"`) {
		t.Errorf("a manifest without a git block stores one: %s", raw)
	}
}

// TestGitExamplesRefusedForTheirRule pins that each git example in the
// spec corpus parses or is refused for the rule its comment names.
func TestGitExamplesRefusedForTheirRule(t *testing.T) {
	for file, want := range map[string]string{
		"invalid-git-without-objects.yaml":   "the git block needs an objects block",
		"invalid-git-tool-not-argument.yaml": "git.ops.push names git_push, which objects.tools pins to the reach all",
	} {
		raw, err := os.ReadFile(filepath.Join(specExamples, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: refusal %v, want one naming %q", file, err, want)
		}
	}
	raw, err := os.ReadFile(filepath.Join(specExamples, "valid-remote-git.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("valid-remote-git.yaml: %v", err)
	}
	g := m.Straza.Git
	if g == nil || g.URL != "https://ghapp.example.com" || g.Upstream != "https://github.com" ||
		g.Tool(GitOpFetch) != "git_fetch" || g.Tool(GitOpPush) != "git_push" {
		t.Errorf("valid-remote-git.yaml git block = %+v", g)
	}
}
