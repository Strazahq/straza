package agentguard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/sameorigin"
)

// edgePush maintains the daemon's gateway-edge push subscription: one SSE
// stream to {server}/v1/push over the HTTPS channel the daemon already
// authenticates to, with no client-reachable NATS required. The CURRENT
// session token is re-read from the store on every (re)connect, so after a
// session re-acquire the stream carries the NEW session's subjects.
//
// The lane is an accelerator, never a dependency: the poll loop runs
// regardless, every refusal backs off and retries, and only an explicit
// revocation EVENT kills the session; a 401 on connect is left to the poll
// lane's authoritative refresh, so a push-lane hiccup can never terminate a
// healthy session.
type edgePush struct {
	base string
	// token returns the current session token; false = session state gone
	// (daemon is exiting) and the subscriber stops.
	token   func() (string, bool)
	revoked chan<- struct{}
	nudged  chan<- struct{}
	out     io.Writer
	// http is a streaming client: header timeout only, NO overall timeout
	// (the stream lives until revocation or disconnect).
	http *http.Client
}

func newEdgePush(base string, token func() (string, bool), revoked, nudged chan<- struct{}, out io.Writer) *edgePush {
	return &edgePush{
		base: strings.TrimRight(base, "/"), token: token,
		revoked: revoked, nudged: nudged, out: out,
		http: &http.Client{CheckRedirect: sameorigin.Check, Transport: &http.Transport{ResponseHeaderTimeout: 15 * time.Second}},
	}
}

// run subscribes until ctx ends, the session state disappears, a revocation
// event fires, or the server declares it has no push lane (404: an older
// server, parked for this daemon's lifetime, poll covers it).
func (e *edgePush) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		tok, ok := e.token()
		if !ok {
			return
		}
		status, connected, err := e.stream(ctx, tok)
		switch {
		case ctx.Err() != nil:
			return
		case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
			fmt.Fprintln(e.out, "daemon: server has no edge push lane; poll-refresh covers this session")
			return
		case status == statusPushRevoked:
			return // revocation delivered; the daemon is exiting
		}
		if connected {
			backoff = time.Second // a live stream ended: reconnect promptly
		} else if err != nil || status != 0 {
			backoff = min(backoff*2, 30*time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// statusPushRevoked is stream's sentinel for "revocation event delivered".
const statusPushRevoked = -1

// stream opens one SSE connection and dispatches its events. It returns the
// HTTP status (0 on transport error, statusPushRevoked after a delivered
// revocation) and whether the stream got as far as the server's ready event.
func (e *edgePush) stream(ctx context.Context, token string) (int, bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", e.base+"/v1/push", nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := e.http.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, false, nil
	}

	connected := false
	event := ""
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case line == "": // dispatch boundary
			switch event {
			case "ready":
				if !connected {
					connected = true
					fmt.Fprintln(e.out, "daemon: kill-switch push active (gateway edge)")
				}
			case "revocation":
				select {
				case e.revoked <- struct{}{}:
				default:
				}
				return statusPushRevoked, true, nil
			case "policy":
				select {
				case e.nudged <- struct{}{}:
				default:
				}
			}
			event = ""
		}
		// data: and comment (heartbeat) lines need no handling: revocation
		// is act-on-receipt for this principal's own subjects, and policy
		// nudges refetch through the verified channel anyway.
	}
	return 0, connected, sc.Err()
}
