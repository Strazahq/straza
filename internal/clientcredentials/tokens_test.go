package clientcredentials

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

var bg = context.Background()

// TestTokenIsCachedAndRenewedAhead pins the life of one entry: many calls
// make one fetch, a call inside the last 60 seconds is served the old token
// and starts exactly one renewal, and the next call gets the new token.
func TestTokenIsCachedAndRenewedAhead(t *testing.T) {
	r := newRig(t)
	first, err := r.tokens.Token(bg, sam())
	if err != nil || first != "tok-sam-sre-agent-1" {
		t.Fatalf("first Token = %q, %v", first, err)
	}
	for range 100 {
		if got, err := r.tokens.Token(bg, sam()); err != nil || got != first {
			t.Fatalf("cached Token = %q, %v, want %q", got, err, first)
		}
	}
	if n := r.idp.requests.Load(); n != 1 {
		t.Fatalf("101 calls made %d fetches, want 1", n)
	}

	r.clock.add(300*time.Second - renewWindow - time.Second)
	if got, _ := r.tokens.Token(bg, sam()); got != first {
		t.Fatalf("Token 61 s before expiry = %q, want the cached one", got)
	}
	if n := r.idp.requests.Load(); n != 1 {
		t.Fatalf("a call 61 s before expiry renewed: %d fetches", n)
	}

	r.clock.add(2 * time.Second)
	// The provider holds the renewal's answer until every caller has
	// returned, so a caller that waited on the renewal hangs the burst, and a
	// renewal that lands mid-burst under load cannot hand late callers the
	// new token.
	release := make(chan struct{})
	r.idp.setHandler(func(w http.ResponseWriter, req *http.Request) {
		<-release
		answer(w, http.StatusOK, map[string]any{
			"access_token": fmt.Sprintf("tok-%s-%d", req.PostForm.Get("client_id"), r.idp.requests.Load()),
			"token_type":   "Bearer", "expires_in": 300,
		})
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := r.tokens.Token(bg, sam()); err != nil || got != first {
				t.Errorf("Token inside the renewal window = %q, %v, want the old token without waiting", got, err)
			}
		}()
	}
	burst := make(chan struct{})
	go func() { wg.Wait(); close(burst) }()
	select {
	case <-burst:
	case <-time.After(5 * time.Second):
		t.Fatal("a call inside the renewal window waited for the renewal")
	}
	close(release)
	waitRequests(t, r.idp, 2)
	waitIdle(t, r.tokens)
	if n := r.idp.requests.Load(); n != 2 {
		t.Fatalf("20 calls inside the renewal window made %d fetches in all, want 2", n)
	}
	if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok-sam-sre-agent-2" {
		t.Fatalf("Token after the renewal = %q, %v, want the new token", got, err)
	}
}

// TestExpiredTokenIsNeverServed pins the hard edge: at its expiry and after
// it an entry is gone, whatever the provider says then. The 30 second
// lifetime ends before the next sweep, so there the check at serve time is
// the only guard, and the 300 second one ends after a sweep.
func TestExpiredTokenIsNeverServed(t *testing.T) {
	for _, life := range []time.Duration{30 * time.Second, 300 * time.Second} {
		for _, after := range []time.Duration{0, time.Second, time.Hour} {
			r := newRig(t)
			r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				answer(w, http.StatusOK, map[string]any{"access_token": "tok-short", "token_type": "Bearer", "expires_in": life.Seconds()})
			})
			first, err := r.tokens.Token(bg, sam())
			if err != nil {
				t.Fatal(err)
			}
			waitIdle(t, r.tokens)
			r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
			})
			// The entry's expiry counts from before the request was sent, so
			// the fake clock, which stood still, puts it exactly life ahead.
			r.clock.add(life + after)
			got, err := r.tokens.Token(bg, sam())
			if err == nil || got != "" {
				t.Fatalf("lifetime %v, %v after expiry: Token = %q, %v, want a refusal and no token", life, after, got, err)
			}
			if strings.Contains(err.Error(), first) {
				t.Fatalf("the refusal carries the expired token: %v", err)
			}
		}
	}
}

// TestRenewalFailureKeepsTheLiveToken: a failed renewal never takes away a
// token that is still good, and it is not retried on every call.
func TestRenewalFailureKeepsTheLiveToken(t *testing.T) {
	r := newRig(t)
	first, err := r.tokens.Token(bg, sam())
	if err != nil {
		t.Fatal(err)
	}
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusServiceUnavailable, map[string]any{})
	})
	r.clock.add(300*time.Second - 30*time.Second)
	for range 50 {
		if got, err := r.tokens.Token(bg, sam()); err != nil || got != first {
			t.Fatalf("Token while the renewal fails = %q, %v, want the live token", got, err)
		}
		waitIdle(t, r.tokens)
	}
	if n := r.idp.requests.Load(); n != 2 {
		t.Fatalf("50 calls during one pause made %d fetches in all, want 2", n)
	}
}

// TestOneFetchInFlightPerKey: callers that find nothing wait for one fetch,
// and another key does not wait behind it.
func TestOneFetchInFlightPerKey(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	r.idp.setHandler(func(w http.ResponseWriter, req *http.Request) {
		if req.PostForm.Get("client_id") == "sam-sre-agent" {
			<-release
		}
		answer(w, http.StatusOK, map[string]any{"access_token": "tok-" + req.PostForm.Get("client_id"), "token_type": "Bearer", "expires_in": 300})
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok-sam-sre-agent" {
				t.Errorf("waiter Token = %q, %v", got, err)
			}
		}()
	}
	waitRequests(t, r.idp, 1)
	if got, err := r.tokens.Token(bg, joe()); err != nil || got != "tok-joe-java-developer-agent" {
		t.Fatalf("another key waited or failed: %q, %v", got, err)
	}
	close(release)
	wg.Wait()
	if n := r.idp.requests.Load(); n != 2 {
		t.Fatalf("20 waiters and one other key made %d fetches, want 2", n)
	}
}

// TestCallerThatHangsUpLeavesTheFetchRunning: the fetch belongs to the key,
// not to the first caller, so its result serves the next one.
func TestCallerThatHangsUpLeavesTheFetchRunning(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		answer(w, http.StatusOK, map[string]any{"access_token": "tok-late", "token_type": "Bearer", "expires_in": 300})
	})
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		_, err := r.tokens.Token(ctx, sam())
		done <- err
	}()
	waitRequests(t, r.idp, 1)
	cancel()
	const want = "agent sam-sre-agent could not get a midpoint token. The call ended before keycloak answered. Call again."
	if err := <-done; err == nil || err.Error() != want {
		t.Fatalf("cancelled caller got %v, want %q", err, want)
	}
	close(release)
	waitIdle(t, r.tokens)
	if got, err := r.tokens.Token(bg, sam()); err != nil || got != "tok-late" {
		t.Fatalf("Token after the abandoned fetch = %q, %v", got, err)
	}
	if n := r.idp.requests.Load(); n != 1 {
		t.Fatalf("%d fetches, want 1", n)
	}
}

// TestKeysNeverCross is the impersonation table: a token is served only to a
// request that names the same provider, server, user and client it was
// fetched for, and the client the provider sees is the request's own.
func TestKeysNeverCross(t *testing.T) {
	r := newRig(t)
	r.tokens.providers["okta"] = r.tokens.providers["keycloak"]
	base, err := r.tokens.Token(bg, sam())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*Request)
	}{
		{"another agent", func(q *Request) { *q = joe() }},
		{"the same user id under another client id", func(q *Request) { q.ClientID = "joe-java-developer-agent" }},
		{"the same client id under another user id", func(q *Request) { q.UserID = "u-joe" }},
		{"another server", func(q *Request) { q.ServerID, q.Server = "app-2", "other" }},
		{"another provider", func(q *Request) { q.Provider = "okta" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := sam()
			tc.edit(&q)
			before := r.idp.requests.Load()
			got, err := r.tokens.Token(bg, q)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("%s was served sam's token", tc.name)
			}
			if r.idp.requests.Load() != before+1 {
				t.Fatalf("%s made no fetch of its own", tc.name)
			}
			if form := r.idp.lastForm(); form.Get("client_id") != q.ClientID {
				t.Fatalf("the provider saw client %q, want %q", form.Get("client_id"), q.ClientID)
			}
			if r.signer.clientID != q.ClientID {
				t.Fatalf("the assertion names %q, want %q", r.signer.clientID, q.ClientID)
			}
		})
	}
	if got, _ := r.tokens.Token(bg, sam()); got != base {
		t.Fatalf("sam's own entry changed to %q", got)
	}
}

// TestRequestMustBeComplete: a request with any empty field is refused before
// anything is signed, because an empty client id or user id is unknown state.
func TestRequestMustBeComplete(t *testing.T) {
	r := newRig(t)
	for _, edit := range []func(*Request){
		func(q *Request) { q.Provider = "" }, func(q *Request) { q.Server = "" }, func(q *Request) { q.ServerID = "" },
		func(q *Request) { q.UserID = "" }, func(q *Request) { q.ClientID = "" }, func(q *Request) { q.Session = "" },
	} {
		q := sam()
		edit(&q)
		if got, err := r.tokens.Token(bg, q); err == nil || got != "" {
			t.Errorf("Token(%+v) = %q, %v, want a refusal", q, got, err)
		}
	}
	if r.signer.calls != 0 || r.idp.requests.Load() != 0 {
		t.Errorf("an incomplete request signed %d assertions and made %d fetches", r.signer.calls, r.idp.requests.Load())
	}
}

// TestFailingAgentCannotHammerTheProvider pins the pause after a refusal: it
// starts at one second, doubles to thirty, answers the same sentence without
// a request while it lasts, and ends with the first success.
func TestFailingAgentCannotHammerTheProvider(t *testing.T) {
	r := newRig(t)
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
	})
	want := int64(0)
	for _, pause := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		pause *= time.Second
		want++
		_, first := r.tokens.Token(bg, sam())
		if first == nil {
			t.Fatal("the refused client got a token")
		}
		for range 50 {
			_, err := r.tokens.Token(bg, sam())
			if err == nil || !strings.HasPrefix(err.Error(), first.Error()) || !strings.Contains(err.Error(), "Straza asks keycloak again in ") {
				t.Fatalf("paused Token = %v, want %q and when Straza asks again", err, first)
			}
		}
		if n := r.idp.requests.Load(); n != want {
			t.Fatalf("after %d refusals and 50 paused calls each the provider saw %d requests", want, n)
		}
		r.clock.add(pause - time.Millisecond)
		if _, _ = r.tokens.Token(bg, sam()); r.idp.requests.Load() != want {
			t.Fatalf("a call %v into a %v pause reached the provider", pause-time.Millisecond, pause)
		}
		r.clock.add(time.Millisecond)
	}
	r.idp.setHandler(nil)
	if _, err := r.tokens.Token(bg, sam()); err != nil {
		t.Fatalf("Token after the provider recovered: %v", err)
	}
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
	})
	r.clock.add(301 * time.Second)
	before := r.idp.requests.Load()
	_, _ = r.tokens.Token(bg, sam())
	r.clock.add(time.Second)
	_, _ = r.tokens.Token(bg, sam())
	if got := r.idp.requests.Load() - before; got != 2 {
		t.Fatalf("after a success the pause did not start again at one second: %d requests, want 2", got)
	}
}

// TestSignerRefusalsAreNotPaced: a missing key costs the provider nothing, so
// the first call after the promotion must sign at once.
func TestSignerRefusalsAreNotPaced(t *testing.T) {
	r := newRig(t)
	r.signer.err = errNoKeyForTest
	if _, err := r.tokens.Token(bg, sam()); err == nil {
		t.Fatal("Token without a key succeeded")
	}
	r.signer.mu.Lock()
	r.signer.err = nil
	r.signer.mu.Unlock()
	if _, err := r.tokens.Token(bg, sam()); err != nil {
		t.Fatalf("Token right after the key appeared: %v", err)
	}
}

// TestDropUserAndSession pins the two drops: a disabled user and a revoked
// session each cost exactly that agent its entries.
func TestDropUserAndSession(t *testing.T) {
	r := newRig(t)
	for _, q := range []Request{sam(), joe()} {
		if _, err := r.tokens.Token(bg, q); err != nil {
			t.Fatal(err)
		}
	}
	other := sam()
	other.ServerID, other.Server = "app-2", "other"
	if _, err := r.tokens.Token(bg, other); err != nil {
		t.Fatal(err)
	}
	fetches := func() int64 { return r.idp.requests.Load() }

	r.tokens.DropSession("ses-unknown")
	r.tokens.DropUser("u-unknown")
	before := fetches()
	_, _ = r.tokens.Token(bg, sam())
	_, _ = r.tokens.Token(bg, joe())
	if fetches() != before {
		t.Fatal("a drop for an unknown id cost an entry")
	}

	r.tokens.DropSession("ses-joe")
	_, _ = r.tokens.Token(bg, sam())
	if fetches() != before {
		t.Fatal("revoking joe's session dropped sam's entry")
	}
	_, _ = r.tokens.Token(bg, joe())
	if fetches() != before+1 {
		t.Fatal("revoking joe's session kept joe's entry")
	}

	r.tokens.DropUser("u-sam")
	_, _ = r.tokens.Token(bg, joe())
	if fetches() != before+1 {
		t.Fatal("disabling sam dropped joe's entry")
	}
	_, _ = r.tokens.Token(bg, sam())
	_, _ = r.tokens.Token(bg, other)
	if fetches() != before+3 {
		t.Fatalf("disabling sam kept an entry of his: %d fetches after the drop, want 2", fetches()-before-1)
	}
}

// TestDropDuringAFetchRefusesItsWaiters: a token that arrives for an agent
// disabled meanwhile is neither handed out nor kept.
func TestDropDuringAFetchRefusesItsWaiters(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		answer(w, http.StatusOK, map[string]any{"access_token": "tok-too-late", "token_type": "Bearer", "expires_in": 300})
	})
	done := make(chan error, 1)
	go func() {
		got, err := r.tokens.Token(bg, sam())
		if got != "" {
			t.Errorf("a waiter of a dropped entry got %q", got)
		}
		done <- err
	}()
	waitRequests(t, r.idp, 1)
	r.tokens.DropUser("u-sam")
	close(release)
	const want = "agent sam-sre-agent could not get a midpoint token. The agent was disabled or its session was revoked while Straza asked keycloak, so the call is refused. An administrator checks the agent's status, then the agent calls again."
	if err := <-done; err == nil || err.Error() != want {
		t.Fatalf("waiter got %v, want %q", err, want)
	}
	r.idp.setHandler(nil)
	if got, _ := r.tokens.Token(bg, sam()); got == "tok-too-late" {
		t.Fatal("the token of the dropped fetch was kept")
	}
}

// TestManySessionsDropConservatively: past the session bound an entry no
// longer knows who used it, so any revoked session drops it.
func TestManySessionsDropConservatively(t *testing.T) {
	r := newRig(t)
	for i := range maxSessions + 1 {
		q := sam()
		q.Session = "ses-" + strings.Repeat("x", i+1)
		if _, err := r.tokens.Token(bg, q); err != nil {
			t.Fatal(err)
		}
	}
	before := r.idp.requests.Load()
	r.tokens.DropSession("ses-never-seen")
	_, _ = r.tokens.Token(bg, sam())
	if r.idp.requests.Load() != before+1 {
		t.Fatal("an entry past its session bound survived a session revoke")
	}
}

// TestSweepDropsDeadEntries: an expired entry and a refused one leave the
// map, so agents that stopped calling cost no memory.
func TestSweepDropsDeadEntries(t *testing.T) {
	r := newRig(t)
	if _, err := r.tokens.Token(bg, sam()); err != nil {
		t.Fatal(err)
	}
	r.idp.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
	})
	_, _ = r.tokens.Token(bg, joe())
	r.clock.add(301*time.Second + pauseMax + sweepEvery)
	r.idp.setHandler(nil)
	third := sam()
	third.UserID, third.ClientID = "u-third", "third-agent"
	if _, err := r.tokens.Token(bg, third); err != nil {
		t.Fatal(err)
	}
	r.tokens.mu.Lock()
	n := len(r.tokens.entries)
	r.tokens.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d entries after the sweep, want only the live one", n)
	}
}

// TestTwoReplicasShareNothing: each replica fetches for itself and signs a
// fresh assertion per fetch, which a provider that refuses reuse accepts.
func TestTwoReplicasShareNothing(t *testing.T) {
	a, b := newRig(t), newRig(t)
	b.tokens.providers = a.tokens.providers
	b.tokens.signer = a.signer
	seen := map[string]bool{}
	var mu sync.Mutex
	a.idp.setHandler(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if seen[req.PostForm.Get("client_assertion")] {
			answer(w, http.StatusBadRequest, map[string]any{"error": "invalid_client", "error_description": "Token reuse detected"})
			return
		}
		seen[req.PostForm.Get("client_assertion")] = true
		answer(w, http.StatusOK, map[string]any{"access_token": "tok", "token_type": "Bearer", "expires_in": 300})
	})
	for _, replica := range []*rig{a, b, a, b} {
		if _, err := replica.tokens.Token(bg, sam()); err != nil {
			t.Fatalf("replica Token: %v", err)
		}
		replica.tokens.DropUser("u-sam")
	}
	if n := a.idp.requests.Load(); n != 4 {
		t.Fatalf("two replicas made %d fetches for four cold calls, want 4", n)
	}
}
