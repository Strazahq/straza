package server

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// sseStream opens GET /v1/push and returns a line channel plus a cancel.
// Each SSE line arrives as-is (data/event/comment); the caller asserts on
// what it needs and cancels to close the stream.
func sseStream(t *testing.T, base, token string) (<-chan string, context.CancelFunc, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/v1/push", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	lines := make(chan string, 32)
	if resp.StatusCode == http.StatusOK {
		go func() {
			defer close(lines)
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				lines <- sc.Text()
			}
		}()
	}
	return lines, cancel, resp
}

// waitForLine pumps the stream until a line containing want arrives (fatal on
// timeout); the sub-second push budget makes 5 s generous.
func waitForLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stream closed before %q", want)
			}
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no %q line within 5s", want)
		}
	}
}

// TestPushEdgeLane pins the edge push lane: a daemon subscribed over plain
// HTTPS SSE (no client-reachable NATS anywhere) hears its own revocation
// sub-second, and the policy nudge broadcast reaches every stream.
func TestPushEdgeLane(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	token, sessionID := checkinToken(t, app, base)

	// No token → refused; garbage token → refused.
	for _, tok := range []string{"", "wat_garbage"} {
		_, cancel, resp := sseStream(t, base, tok)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q = %d, want 401", tok, resp.StatusCode)
		}
		_ = resp.Body.Close()
		cancel()
	}

	// Live stream: the ready event confirms the lane (subjects from claims).
	lines, cancel, resp := sseStream(t, base, token)
	defer cancel()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	waitForLine(t, lines, "event: ready")

	// Fleet push health: the overview counts this live subscription.
	grantAdmin(t, app, user.ID)
	idTokenHealth := loginDeviceFlow(t, base, "kim", "hunter2!")
	var ov struct {
		Push struct {
			Connected int  `json:"connected"`
			LaneUp    bool `json:"lane_up"`
		} `json:"push"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/overview", idTokenHealth, nil, &ov); code != http.StatusOK {
		t.Fatalf("overview = %d", code)
	}
	if !ov.Push.LaneUp || ov.Push.Connected != 1 {
		t.Errorf("push health = %+v, want lane_up + 1 connected", ov.Push)
	}

	// A policy nudge broadcast reaches the stream (notify-only hint).
	app.pushPolicyNudge(map[string]any{"snapshot": "snap-test"})
	waitForLine(t, lines, "event: policy")

	// The admin kill switch: the target-scoped revocation arrives as an SSE
	// event on the session's own stream.
	idToken := idTokenHealth
	if code := adminReq(t, "POST", base+"/v1/admin/sessions/"+sessionID+"/revoke", idToken, nil, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	waitForLine(t, lines, "event: revocation")

	// A revoked session cannot re-subscribe (in-memory denylist, no DB).
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, cancel2, resp2 := sseStream(t, base, token)
		code := resp2.StatusCode
		_ = resp2.Body.Close()
		cancel2()
		if code == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("revoked session still accepted (last = %d)", code)
		}
		time.Sleep(50 * time.Millisecond) // denylist converges via the revocation consumer
	}
}

// TestPushEdgeCap pins the per-pod connection cap: over events.pushEdgeMaxConns
// the endpoint answers 503 + Retry-After, and a freed slot admits again.
func TestPushEdgeCap(t *testing.T) {
	t.Parallel()
	// The capture logger pins the one-record contract on this 5xx: the over-cap 503
	// logs exactly one Error record carrying the answer's correlation id.
	log, buf := captureLogger()
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }},
		func(c *config.Config) { c.Events.PushEdgeMaxConns = 1 })
	seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)

	lines, cancel, resp := sseStream(t, base, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first conn = %d", resp.StatusCode)
	}
	waitForLine(t, lines, "event: ready")

	buf.Reset()
	_, cancel2, resp2 := sseStream(t, base, token)
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over-cap conn = %d, want 503", resp2.StatusCode)
	}
	if resp2.Header.Get("Retry-After") == "" {
		t.Error("503 must carry Retry-After (the daemon backs off to poll)")
	}
	assertOneErrorWithCorrelation(t, buf, http.StatusServiceUnavailable, resp2.Header.Get("X-Request-Id"))
	_ = resp2.Body.Close()
	cancel2()

	// Freeing the slot admits the next subscriber.
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		lines3, cancel3, resp3 := sseStream(t, base, token)
		code := resp3.StatusCode
		if code == http.StatusOK {
			waitForLine(t, lines3, "event: ready")
			cancel3()
			_ = resp3.Body.Close()
			break
		}
		_ = resp3.Body.Close()
		cancel3()
		if time.Now().After(deadline) {
			t.Fatalf("slot never freed (last = %d)", code)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = lines
}
