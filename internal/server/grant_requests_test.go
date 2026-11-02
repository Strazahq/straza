package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// sha1Ref is a push of name that moves it between two SHA-1 ids.
func sha1Ref(name string) authn.GrantRef {
	return authn.GrantRef{Ref: name, Old: strings.Repeat("a", 40), New: strings.Repeat("b", 40)}
}

// TestGrantBadRequests pins the 400 answers: a body that is not JSON, a
// missing upstream, an unknown op, refs on a fetch, too many refs, a
// negative wait, each ref name git's check-ref-format refuses, ids that
// are not lower-case hex of one hash length, and a push whose grant would
// be too long. None decides anything, opens an approval or writes a
// record, and a SHA-256 push of a valid name gets its grant.
func TestGrantBadRequests(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	bad := func(why string) string { return fmt.Sprintf(grantBadRequestMsg, why) }
	var many []authn.GrantRef
	for i := range 1001 {
		many = append(many, sha1Ref(fmt.Sprintf("refs/heads/b%d", i)))
	}
	var long []authn.GrantRef
	for i := range 60 {
		long = append(long, sha1Ref(fmt.Sprintf("refs/heads/feature/a-rather-long-branch-name-%03d", i)))
	}
	refRow := func(name string) []authn.GrantRef { return []authn.GrantRef{sha1Ref(name)} }
	cases := []struct {
		name, body string
		want       string
	}{
		{"not JSON", `{"upstream":`, bad("its body is not a JSON object")},
		{"no upstream", `{"op":"fetch"}`, bad("it names no upstream URL")},
		{"an unknown op", `{"upstream":"` + payAPI + `","op":"advertise"}`, bad(`its op "advertise" is neither fetch nor push`)},
		{"refs on a fetch", grantBody("fetch", refRow("refs/heads/main"), nil), bad("a fetch names no refs, and this request names 1")},
		{"too many refs", grantBody("push", many, nil), bad("it names 1001 refs, and a push may name at most 1000")},
		{"a negative wait", grantBody("fetch", nil, intp(-1)), bad("its waitSeconds is -1, and it must be 0 or more")},
		{"an old id in upper case", grantBody("push", []authn.GrantRef{{Ref: "refs/heads/main", Old: strings.Repeat("A", 40), New: zeros}}, nil),
			bad(`the old id of "refs/heads/main" is not lower-case hex of 40 or 64 characters`)},
		{"a short new id", grantBody("push", []authn.GrantRef{{Ref: "refs/heads/main", Old: zeros, New: "abc"}}, nil),
			bad(`the new id of "refs/heads/main" is not lower-case hex of 40 or 64 characters`)},
		{"ids of two hash lengths", grantBody("push", []authn.GrantRef{{Ref: "refs/heads/main", Old: zeros, New: strings.Repeat("b", 64)}}, nil),
			bad(`the old and new ids of "refs/heads/main" differ in length`)},
		{"a grant too long for one push", grantBody("push", long, nil), fmt.Sprintf(grantTooLargeMsg, 60)},
	}
	for _, name := range []string{"refs/", "heads/main", "refs/heads/ma in", "refs/heads/a..b", "refs/heads//x", "refs/heads/x/",
		"refs/heads/x.lock", "refs/heads/.x", "refs/heads/x@{1}", "refs/heads/x\x01", "refs/heads/x\x7f", "refs/heads/x.",
		"refs/heads/x~1", "refs/heads/x^", "refs/heads/x:y", "refs/heads/x?", "refs/heads/x*", "refs/heads/x[", `refs/heads/x\y`} {
		cases = append(cases, struct{ name, body, want string }{"the ref " + name, grantBody("push", refRow(name), nil),
			bad(fmt.Sprintf("the ref %q is not a full ref name under refs/ that git accepts", name))})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := r.session(t, "hol")
			status, out := r.post(t, s, "/v1/grants", tc.body)
			if status != 400 || out["error"] != tc.want {
				t.Errorf("answer = %d %q\nwant 400 %q", status, out["error"], tc.want)
			}
			if n := r.approvalsOf(t, s); n != 0 {
				t.Errorf("the refused request opened %d approvals", n)
			}
			if recs := r.records(t, s); len(recs) != 0 {
				t.Errorf("the refused request wrote %d records: %v", len(recs), recs)
			}
		})
	}
	t.Run("a SHA-256 push of a valid name", func(t *testing.T) {
		t.Parallel()
		ref := authn.GrantRef{Ref: "refs/heads/release/v1.2-rc_3", Old: strings.Repeat("0", 64), New: strings.Repeat("c", 64)}
		if status, out := r.grant(t, r.session(t, "eve"), payAPI, "push", []authn.GrantRef{ref}, nil); status != 200 {
			t.Errorf("answer = %d %v, want the grant", status, out)
		}
	})
}

// grantBody is the JSON body of a grant request on the rig's pay-api.
func grantBody(op string, refs []authn.GrantRef, wait *int) string {
	body := map[string]any{"upstream": payAPI, "op": op}
	if refs != nil {
		body["refs"] = refs
	}
	if wait != nil {
		body["waitSeconds"] = *wait
	}
	return string(mustJSON(body))
}

// secondManifest is a server name at url that serves git for upstream and
// maps the ops in ops, such as "{fetch: git_fetch}".
func secondManifest(name, url, upstream, ops string) string {
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %[1]s}
server: {name: straza.test/%[1]s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %[2]q}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
  objects:
    type: repo
    list: {tool: repo_list}
    tools:
      git_fetch: {argument: repo}
      git_push: {argument: repo}
  git:
    url: https://%[1]s.example.com
    upstream: %[3]s
    ops: %[4]s
`, name, url, upstream, ops)
}

// installGitServer installs a second git server, bound on every repo to a
// role of its own, <name>-all, and waits for its object list.
func (r *grantRig) installGitServer(t *testing.T, name, upstream, ops string) {
	t.Helper()
	ctx := context.Background()
	mf, err := managerParse(t, secondManifest(name, startGitUpstream(t).URL, upstream, ops))
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.broker.Set(ctx, row.ID, "", "app-tok"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.broker.SetListing(ctx, row.ID, "listing-tok"); err != nil {
		t.Fatal(err)
	}
	role, err := r.app.store.Roles().Create(ctx, store.Role{Name: name + "-all", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: role.ID, AppID: row.ID, ToolMatcher: `["*"]`,
		Scope: &store.ObjectScope{Every: true}}); err != nil {
		t.Fatal(err)
	}
	r.app.manager.SecretUpdated(ctx, row.ID)
	if err := r.app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	catWaitRunning(t, r.app, name)
	if err := r.app.objects.MarkDue(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, name+"'s object list", func() bool { l := r.app.objects.Get(row.ID); return l != nil && len(l.Objects) == 3 })
}

// TestGrantSecondServers pins two more servers that eve reaches: ghtwin
// also serves git for https://github.com and lists the same repos, so both
// routes answer 409 ambiguous, and ghe serves only fetch for its own host,
// so a push there answers 403 with the sentence that names the server and
// the op and writes no record, while a fetch gets ghe's grant.
func TestGrantSecondServers(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	r.installGitServer(t, "ghtwin", "https://github.com", "{fetch: git_fetch, push: git_push}")
	r.installGitServer(t, "ghe", "https://ghe.example.com", "{fetch: git_fetch}")
	sub := r.checked["eve"].sub
	sub.Roles = append([]string{"ghe-all", "ghtwin-all"}, sub.Roles...)
	session := func() *grantSessionT { return r.sessionAs(t, r.users["eve"].ID, sub, r.checked["eve"].facts) }
	ambiguous := fmt.Sprintf(grantAmbiguousMsg, "ghapp", "ghtwin", "Strazahq/pay-api")
	for _, path := range []string{"/v1/grants/resolve", "/v1/grants"} {
		status, out := r.post(t, session(), path, grantBody("fetch", nil, nil))
		if status != 409 || out["code"] != "ambiguous" || out["error"] != ambiguous {
			t.Errorf("%s = %d %v, want 409 ambiguous %q", path, status, out, ambiguous)
		}
	}
	s := session()
	gheRepo := "https://ghe.example.com/Strazahq/site.git"
	if status, out := r.grant(t, s, gheRepo, "push", nil, nil); status != 403 || out["error"] != fmt.Sprintf(grantOpUnmappedMsg, "ghe", "push") {
		t.Errorf("push on ghe = %d %v, want 403 with the unmapped op sentence", status, out)
	}
	if recs := r.records(t, s); len(recs) != 0 {
		t.Errorf("the unmapped op wrote %d records, want none: %v", len(recs), recs)
	}
	if status, out := r.grant(t, s, gheRepo, "fetch", nil, nil); status != 200 || out["aud"] != "https://ghe.example.com" || out["srv"] != "ghe" {
		t.Errorf("fetch on ghe = %d %v, want ghe's grant", status, out)
	}
}

// TestGrantArgs pins the arguments a grant's decision runs with: the repo
// under the pinned argument, and on a push with refs branch only when
// exactly one ref is under refs/heads/, then refs, in that order.
func TestGrantArgs(t *testing.T) {
	t.Parallel()
	head, tag, other := sha1Ref("refs/heads/main"), sha1Ref("refs/tags/v1"), sha1Ref("refs/heads/dev")
	refsJSON := func(refs ...authn.GrantRef) string { return string(mustJSON(refs)) }
	cases := []struct {
		name, arg string
		refs      []authn.GrantRef
		want      string
	}{
		{"a fetch", "repo", nil, `{"repo":"Strazahq/pay-api"}`},
		{"another pinned argument", "repository", nil, `{"repository":"Strazahq/pay-api"}`},
		{"one branch", "repo", []authn.GrantRef{head}, `{"repo":"Strazahq/pay-api","branch":"main","refs":` + refsJSON(head) + `}`},
		{"one branch and a tag", "repo", []authn.GrantRef{tag, head}, `{"repo":"Strazahq/pay-api","branch":"main","refs":` + refsJSON(tag, head) + `}`},
		{"two branches", "repo", []authn.GrantRef{head, other}, `{"repo":"Strazahq/pay-api","refs":` + refsJSON(head, other) + `}`},
		{"a tag alone", "repo", []authn.GrantRef{tag}, `{"repo":"Strazahq/pay-api","refs":` + refsJSON(tag) + `}`},
	}
	for _, tc := range cases {
		if got := string(grantArgs(tc.arg, "Strazahq/pay-api", tc.refs)); got != tc.want {
			t.Errorf("%s: grantArgs = %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// TestGrantURLWithQuery pins that both routes refuse an upstream URL with a
// query or a fragment with 400 and a sentence that quotes no part of it,
// before any lookup: no listing is asked for and no record is written.
func TestGrantURLWithQuery(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	r.clock.advance(2 * time.Minute)
	before := r.up.listings()
	for _, path := range []string{"/v1/grants/resolve", "/v1/grants"} {
		for _, u := range []string{payAPI + "?x=1", payAPI + "#sekrit", "https://github.com/Strazahq/new?token=sekrit", "https://github.com/Strazahq/new#x=1"} {
			s := r.session(t, "eve")
			status, out := r.post(t, s, path, string(mustJSON(map[string]any{"upstream": u, "op": "fetch"})))
			msg, _ := out["error"].(string)
			if status != 400 || msg != grantURLQueryMsg || strings.Contains(msg, "sekrit") || strings.Contains(msg, "x=1") {
				t.Errorf("%s %q = %d %q, want 400 %q", path, u, status, msg, grantURLQueryMsg)
			}
			if recs := r.records(t, s); len(recs) != 0 {
				t.Errorf("%s %q wrote %d records", path, u, len(recs))
			}
		}
	}
	time.Sleep(300 * time.Millisecond)
	if got := r.up.listings(); got != before {
		t.Errorf("the refused URLs asked for %d listings, want none", got-before)
	}
}
