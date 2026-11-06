//go:build linux || darwin

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// TestTextCallsReadDeadline pins the body read deadline inside the
// in-flight bound on the real server. A caller that holds every place with
// bodies it never finishes loses them once textReadBudget passes, so the
// person's capture gets a place again. A decide whose online check outlasts
// the deadline still gets strazad's verdict, because the deadline ends with
// the body.
func TestTextCallsReadDeadline(t *testing.T) {
	old := textReadBudget
	textReadBudget = 300 * time.Millisecond
	t.Cleanup(func() { textReadBudget = old })
	f := gateDouble(t, 900, time.Hour, Renewal{})
	f.engine = apiEngine(t, fetchGateDoc, 900)
	f.decide = func(ctx context.Context, _ policy.Event) (DecideOutcome, error) {
		select {
		case <-time.After(3 * textReadBudget):
		case <-ctx.Done():
			return DecideOutcome{}, ctx.Err()
		}
		return DecideOutcome{Answer: agentguard.DecideResponse{Effect: policy.EffectAllow, RuleID: "central-ok"}, Status: 200}, nil
	}
	l := &ledgerDouble{apiDouble: f}
	a := newAPI(l, Options{Log: slog.New(slog.DiscardHandler)})
	sock := filepath.Join(shortTempDir(t), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, ln, a)
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	post := func(route string, body any) (int, string) {
		raw, _ := json.Marshal(body)
		resp, err := hc.Post("http://straza"+route, "application/json", strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	me := strconv.Itoa(os.Getuid())
	open := func() int {
		a.text.mu.Lock()
		defer a.text.mu.Unlock()
		return a.text.open[me].calls
	}

	for range maxTextCalls {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_, _ = fmt.Fprintf(c, "POST %s HTTP/1.1\r\nHost: straza\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{", localapi.RouteDecide)
	}
	waitFor(t, "every place held by a body that never ends", func() bool { return open() == maxTextCalls })
	capture := localapi.CaptureRequest{Harness: localapi.Harness{Name: "claude-code", Session: "w1"}, Kind: localapi.CapturePrompt, Text: "the person's prompt"}
	if code, _ := post(localapi.RouteCapture, capture); code != http.StatusTooManyRequests {
		t.Fatalf("a capture while every place is held = %d, want 429", code)
	}
	l.mu.Lock()
	if got := strings.Join(l.refused, ", "); got != me+" capture" {
		t.Errorf("ledger notes = %q, want the refused capture", got)
	}
	l.mu.Unlock()
	waitFor(t, "the held places freed by the read deadline", func() bool { return open() == 0 })
	if code, body := post(localapi.RouteCapture, capture); code != http.StatusNoContent {
		t.Fatalf("a capture after the deadline = %d %s, want it served", code, body)
	}
	fetch := localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code", Session: "w1"},
		Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{"https://x.example"}}}
	code, body := post(localapi.RouteDecide, fetch)
	if code != http.StatusOK || !strings.Contains(body, "central-ok") {
		t.Fatalf("a decide whose online check outlasts the read deadline = %d %s, want strazad's allow", code, body)
	}
}
