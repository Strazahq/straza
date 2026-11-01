package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// The deadlines of the rows. Every git command here finishes in well under
// a second on a quiet machine, except the held push, which waits for the
// fake approval.
const (
	gitDeadline    = 60 * time.Second
	deleteDeadline = 10 * time.Second
	firstWaitBound = 5 * time.Second
)

const zeroID = "0000000000000000000000000000000000000000"

// TestGitThroughStraza runs real git through git-remote-straza against
// every row of contract 20.12's end-to-end list. Rows run in order, because
// later ones push to and fetch from what earlier ones made.
func TestGitThroughStraza(t *testing.T) {
	r := newRig(t)
	seed := r.seed()
	pay := r.upstream + "/acme/pay-api.git"
	env := r.env(r.hosts)
	work := filepath.Join(r.root, "work")
	ok := func(t *testing.T, res gitResult) {
		t.Helper()
		if res.err != nil {
			t.Fatalf("git failed: %v\nstdout: %s\nstderr: %s", res.err, res.stdout, res.stderr)
		}
	}
	exists := func(t *testing.T, path string) {
		t.Helper()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s is missing: %v", path, err)
		}
	}
	refIn := func(repo, ref string) string {
		out, _ := r.runPlain(filepath.Join(r.repos, repo+".git"), "rev-parse", "--verify", "-q", ref)
		return out
	}

	t.Run("clone", func(t *testing.T) {
		ok(t, r.git(r.root, env, gitDeadline, "clone", "-q", pay, work))
		exists(t, filepath.Join(work, "README"))
		if asks := r.grants.asks("/acme/pay-api.git"); len(asks) != 1 || asks[0].Op != "fetch" || asks[0].Refs != nil {
			t.Fatalf("grant requests %+v, want one fetch grant", asks)
		}
	})

	var feature string
	t.Run("push a new branch", func(t *testing.T) {
		feature = r.commit(work, "CHANGE", "one\n", "")
		before := len(r.grants.asks("/acme/pay-api.git"))
		ok(t, r.git(work, env, gitDeadline, "push", "-q", "origin", "HEAD:refs/heads/feature-1"))
		if got := refIn("acme/pay-api", "refs/heads/feature-1"); got != feature {
			t.Fatalf("feature-1 is %q at the git host, want %s", got, feature)
		}
		asks := r.grants.asks("/acme/pay-api.git")[before:]
		want := []localapi.GitRef{{Ref: "refs/heads/feature-1", Old: zeroID, New: feature}}
		if len(asks) != 2 || asks[0].Op != "push" || asks[0].Refs != nil || asks[1].Op != "push" || !sameRefs(asks[1].Refs, want) {
			t.Fatalf("grant requests %+v, want a push grant without refs, then one naming %v", asks, want)
		}
	})

	t.Run("fetch", func(t *testing.T) {
		next := r.commit(seed, "NEXT", "two\n", filepath.Join(r.repos, "acme/pay-api.git"))
		ok(t, r.git(work, env, gitDeadline, "fetch", "-q", "origin"))
		if got := r.plain(work, "rev-parse", "origin/main"); got != next {
			t.Fatalf("origin/main is %s after the fetch, want %s", got, next)
		}
	})

	t.Run("ls-remote", func(t *testing.T) {
		res := r.git(work, env, gitDeadline, "ls-remote", "origin")
		ok(t, res)
		for _, ref := range []string{"refs/heads/main", "refs/heads/feature-1"} {
			if !strings.Contains(res.stdout, ref) {
				t.Fatalf("ls-remote does not list %s:\n%s", ref, res.stdout)
			}
		}
	})

	t.Run("partial clone", func(t *testing.T) {
		partial := filepath.Join(r.root, "partial")
		before := len(r.grants.asks("/acme/pay-api.git"))
		ok(t, r.git(r.root, env, gitDeadline, "clone", "-q", "--filter=blob:none", pay, partial))
		exists(t, filepath.Join(partial, "README"))
		if got := r.plain(partial, "config", "remote.origin.promisor"); got != "true" {
			t.Fatalf("the clone is not partial: remote.origin.promisor is %q", got)
		}
		// The checkout fetches its missing blobs lazily, which starts the
		// helper a second time with a fetch grant of its own.
		if n := len(r.grants.asks("/acme/pay-api.git")) - before; n < 2 {
			t.Fatalf("%d fetch grants, want the clone's and the lazy fetch's", n)
		}
	})

	t.Run("submodule with protocol.straza.allow", func(t *testing.T) {
		super := filepath.Join(r.root, "super")
		ok(t, r.git(r.root, env, gitDeadline, "clone", "-q", "--recurse-submodules", r.upstream+"/acme/super.git", super))
		exists(t, filepath.Join(super, "lib", "README"))
	})

	t.Run("submodule without protocol.straza.allow", func(t *testing.T) {
		res := r.git(r.root, r.env(r.hostsBare), gitDeadline, "clone", "-q", "--recurse-submodules",
			r.upstream+"/acme/super.git", filepath.Join(r.root, "super-bare"))
		if res.err == nil || !strings.Contains(res.stderr, "transport 'straza' not allowed") {
			t.Fatalf("want the submodule refused for the straza transport, got %v:\n%s", res.err, res.stderr)
		}
	})

	t.Run("nothing to push", func(t *testing.T) {
		before := len(r.grants.asks("/acme/pay-api.git"))
		res := r.git(work, env, gitDeadline, "push", "origin", "HEAD:refs/heads/feature-1")
		ok(t, res)
		if !strings.Contains(res.stderr, "Everything up-to-date") {
			t.Fatalf("stderr %q, want Everything up-to-date", res.stderr)
		}
		if asks := r.grants.asks("/acme/pay-api.git")[before:]; len(asks) != 1 || asks[0].Refs != nil {
			t.Fatalf("grant requests %+v, want only the push grant that opens the advertisement", asks)
		}
	})

	t.Run("delete-only push", func(t *testing.T) {
		res := r.git(work, env, deleteDeadline, "push", "-q", "origin", "--delete", "feature-1")
		ok(t, res)
		if got := refIn("acme/pay-api", "refs/heads/feature-1"); got != "" {
			t.Fatalf("feature-1 is still %s at the git host", got)
		}
		t.Logf("the delete-only push took %v", res.took)
	})

	t.Run("refused ref", func(t *testing.T) {
		res := r.git(work, env, gitDeadline, "push", "origin", "HEAD:refs/heads/blocked")
		if res.err == nil || !strings.Contains(res.stderr, "! [remote rejected] HEAD -> blocked ("+refusedSentence+")") {
			t.Fatalf("want the ref rejected with Straza's reason, got %v:\n%s", res.err, res.stderr)
		}
		if got := refIn("acme/pay-api", refBlocked); got != "" {
			t.Fatalf("blocked moved to %s", got)
		}
	})

	t.Run("held push", func(t *testing.T) {
		res := r.git(work, env, gitDeadline, "push", "-q", "origin", "HEAD:refs/heads/held")
		ok(t, res)
		if res.firstWait == 0 || res.firstWait > firstWaitBound {
			t.Fatalf("the first wait line reached stderr after %v, want within %v:\n%s", res.firstWait, firstWaitBound, res.stderr)
		}
		if got := refIn("acme/pay-api", refHeld); got != feature {
			t.Fatalf("held is %q at the git host after the approval, want %s", got, feature)
		}
		t.Logf("first wait line after %v, push done after %v", res.firstWait, res.took)
	})

	t.Run("unlisted repo goes to plain https", func(t *testing.T) {
		pub := filepath.Join(r.root, "public-lib")
		ok(t, r.git(r.root, env, gitDeadline, "clone", "-q", r.upstream+"/acme/public-lib.git", pub))
		exists(t, filepath.Join(pub, "LIB"))
		if asks := r.grants.asks("/acme/public-lib.git"); len(asks) != 0 {
			t.Fatalf("an unlisted repo took grants %+v", asks)
		}
		r.grants.mu.Lock()
		resolved := strings.Join(r.grants.resolved, " ")
		r.grants.mu.Unlock()
		if !strings.Contains(resolved, "/acme/public-lib.git") {
			t.Fatalf("the helper never asked Straza about the public repo: %s", resolved)
		}
	})

	t.Run("archive refused", func(t *testing.T) {
		res := r.git(work, env, gitDeadline, "archive", "--remote="+pay, "HEAD")
		want := "Straza: git archive --remote does not go through Straza. Clone the repo and run git archive locally."
		if res.err == nil || !strings.Contains(res.stderr, want) {
			t.Fatalf("want the archive refused with %q, got %v:\n%s", want, res.err, res.stderr)
		}
	})

	t.Run("the resource server's checks held", func(t *testing.T) {
		r.rs.mu.Lock()
		defer r.rs.mu.Unlock()
		if len(r.rs.faults) != 0 {
			t.Fatalf("the resource server found %d faults: %q", len(r.rs.faults), r.rs.faults)
		}
		if len(r.rs.proofs) == 0 {
			t.Fatal("no request reached the resource server")
		}
	})

	t.Run("no grant or proof in any output", func(t *testing.T) {
		r.mu.Lock()
		outputs := append([]string{r.logs.String()}, r.outputs...)
		r.mu.Unlock()
		r.grants.mu.Lock()
		secrets := append([]string{}, r.grants.issued...)
		r.grants.mu.Unlock()
		r.rs.mu.Lock()
		secrets = append(secrets, r.rs.proofs...)
		r.rs.mu.Unlock()
		if len(secrets) == 0 {
			t.Fatal("no grant or proof was recorded, so this row checks nothing")
		}
		for i, out := range outputs {
			if strings.Contains(out, "eyJ") {
				t.Fatalf("output %d carries a JWT: %s", i, out)
			}
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Fatalf("output %d carries a grant or a proof", i)
				}
			}
		}
	})
}

// runPlain is plain without failing the test, for a lookup that may find
// nothing.
func (r *rig) runPlain(dir string, args ...string) (string, error) {
	cmd := r.plainCmd(dir, args...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
