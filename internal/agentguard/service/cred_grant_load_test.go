package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestGitRelayStreamsLargeBody pins streaming: a push body of 64 MiB, far
// over the 8 MiB the strazad transport buffers, reaches ghapp whole while
// the service's heap stays small.
func TestGitRelayStreamsLargeBody(t *testing.T) {
	g := newGitRig(t)
	if _, err := g.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	op := newOpID()
	refs := []localapi.GitRef{{Ref: "refs/heads/big", Old: strings.Repeat("0", 40), New: strings.Repeat("b", 40)}}
	if lines := g.grant(t, alice, "w1", op, localapi.GitGrantRequest{Op: "push", Refs: refs}); !okLines(lines) {
		t.Fatalf("grant lines = %+v", lines)
	}
	const size = 64 << 20
	req := gitReq(http.MethodPost, localapi.RouteGit+"git-receive-pack", io.LimitReader(byteSource('P'), size), "w1", op)
	req.ContentLength = -1
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	stop, peak := make(chan struct{}), make(chan uint64, 1)
	go func() {
		var top uint64
		var m runtime.MemStats
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				peak <- top
				return
			case <-tick.C:
				runtime.ReadMemStats(&m)
				top = max(top, m.HeapInuse)
			}
		}
	}()
	w := g.send(alice, req)
	close(stop)
	top := <-peak
	if w.Code != http.StatusOK || w.Body.String() != fmt.Sprintf("ok /git/Acme/pay-api.git/git-receive-pack %d", size) {
		t.Fatalf("receive-pack = %d %q (ghapp refused %v)", w.Code, w.Body.String(), g.ghapp.refusals())
	}
	if grew := int64(top) - int64(base.HeapInuse); grew > 16<<20 {
		t.Errorf("the heap grew by %d MiB while relaying %d MiB", grew>>20, size>>20)
	}
}

// TestGitNoCheckinUnderLoad pins the counter rule: 15 windows running a
// whole fetch and push through the git door at once add no key check-in.
func TestGitNoCheckinUnderLoad(t *testing.T) {
	g := newGitRig(t)
	for i := range 15 {
		if _, err := g.start(alice, fmt.Sprintf("w%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	g.srv.with(func() { before = len(g.srv.accepted) })
	var wg sync.WaitGroup
	errs := make(chan string, 15)
	for i := range 15 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if why := g.fullRun(fmt.Sprintf("w%d", i)); why != "" {
				errs <- why
			}
		}()
	}
	wg.Wait()
	close(errs)
	for why := range errs {
		t.Error(why)
	}
	g.srv.with(func() {
		if after := len(g.srv.accepted); after != before {
			t.Errorf("the git door added %d key check-ins", after-before)
		}
		if n := len(g.srv.git.asked); n != 45 {
			t.Errorf("strazad got %d grant requests, want 45", n)
		}
	})
	g.srv.assertCounters(t)
	g.noSecretsLogged(t)
}

// fullRun runs a resolve, a fetch and a push for window and returns what
// failed.
func (g *gitRig) fullRun(window string) string {
	op := newOpID()
	if w := g.send(alice, gitReq(http.MethodPost, localapi.RouteGitResolve, nil, window, op)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"listed":true`) {
		return fmt.Sprintf("%s resolve = %d %q", window, w.Code, w.Body.String())
	}
	refs := []localapi.GitRef{{Ref: "refs/heads/" + window, Old: strings.Repeat("0", 40), New: strings.Repeat("c", 40)}}
	steps := []struct {
		grant         *localapi.GitGrantRequest
		method, route string
	}{
		{&localapi.GitGrantRequest{Op: "fetch"}, http.MethodGet, localapi.RouteGit + "info/refs?service=git-upload-pack"},
		{nil, http.MethodPost, localapi.RouteGit + "git-upload-pack"},
		{&localapi.GitGrantRequest{Op: "push"}, http.MethodGet, localapi.RouteGit + "info/refs?service=git-receive-pack"},
		{&localapi.GitGrantRequest{Op: "push", Refs: refs}, http.MethodPost, localapi.RouteGit + "git-receive-pack"},
	}
	for _, st := range steps {
		if st.grant != nil {
			raw, _ := json.Marshal(st.grant)
			w := g.send(alice, gitReq(http.MethodPost, localapi.RouteGitGrant, bytes.NewReader(raw), window, op))
			if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
				return fmt.Sprintf("%s grant %s = %d %q", window, st.grant.Op, w.Code, w.Body.String())
			}
		}
		if w := g.relay(alice, window, op, st.method, st.route, "0000"); w.Code != http.StatusOK {
			return fmt.Sprintf("%s %s %s = %d %q", window, st.method, st.route, w.Code, w.Body.String())
		}
	}
	return ""
}

// TestGitHostFileFromCheckin pins the feed of the host file: a start
// writes the upstreams its check-in answer names, and a refresh whose
// answer names none empties the file.
func TestGitHostFileFromCheckin(t *testing.T) {
	g := newGitRig(t)
	path := hostFilePath(g.s.opts)
	g.srv.with(func() { g.srv.git.upstreams = []string{fakeUpstream} })
	if _, err := g.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	if raw, err := readFile(path); err != nil || !strings.Contains(raw, `[url "straza::https://github.com/"]`) {
		t.Fatalf("host file after the start = %q (%v)", raw, err)
	}
	g.srv.with(func() { g.srv.git.upstreams = nil })
	g.clock.Advance(tokenLife - 60*time.Second)
	if _, err := g.start(alice, "w1"); err != nil {
		t.Fatal(err)
	}
	if raw, err := readFile(path); err != nil || raw != "" {
		t.Fatalf("host file after a refresh without upstreams = %q (%v), want empty", raw, err)
	}
}

// byteSource is an endless reader of one byte value, which costs no
// memory however much is read.
type byteSource byte

func (b byteSource) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// readFile returns the text of the file at path.
func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a test's own file
	return string(raw), err
}
