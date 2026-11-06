package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// fetchGateDoc allows net.fetch through strazad's online check.
const fetchGateDoc = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: online-gate}
spec:
  match: {roles: [dev]}
  rules:
    - id: gated-fetch
      tools: [net.fetch]
      effect: allow
      mode: serverCheck
      reason: "online gate"
`

// ledgerDouble is an apiDouble that keeps the loss ledger's notes of
// refused calls.
type ledgerDouble struct {
	*apiDouble
	mu      sync.Mutex
	refused []string
}

func (l *ledgerDouble) NoteRefused(c Caller, call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refused = append(l.refused, c.UID+" "+call)
}

// TestTextCallsInFlightCap pins the per-uid cap on decide and capture calls
// in flight: while one uid has maxTextCalls open, its next decide is
// refused and denied and its next capture is refused, each with its
// sentence and before the body is read, and each refusal is one note in
// that uid's loss ledger. Another uid is still served, and a call that ends
// frees its place.
func TestTextCallsInFlightCap(t *testing.T) {
	f := gateDouble(t, 900, time.Hour, Renewal{})
	f.engine = apiEngine(t, fetchGateDoc, 900)
	release := make(chan struct{})
	entered := make(chan struct{}, maxTextCalls)
	f.decide = func(ctx context.Context, ev policy.Event) (DecideOutcome, error) {
		if len(ev.Paths) > 0 && ev.Paths[0] == "https://hold.example" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return DecideOutcome{Answer: agentguard.DecideResponse{Effect: policy.EffectAllow, RuleID: "central-ok"}, Status: 200}, nil
	}
	l := &ledgerDouble{apiDouble: f}
	a := newAPI(l, Options{Log: slog.New(slog.DiscardHandler)})
	fetch := func(url string) localapi.DecideRequest {
		return localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code", Session: "w1"},
			Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{url}}}
	}

	var wg sync.WaitGroup
	codes := make(chan int, maxTextCalls)
	for range maxTextCalls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, fetch("https://hold.example")).Code
		}()
	}
	for range maxTextCalls {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the held decides did not all reach the online gate")
		}
	}

	probes := []struct {
		name     string
		uid      string
		route    string
		body     any
		code     int
		sentence string
	}{
		{"the uid's next decide", "1000", localapi.RouteDecide, fetch("https://x.example"),
			http.StatusTooManyRequests, tooManyDecides},
		{"the uid's next capture", "1000", localapi.RouteCapture,
			localapi.CaptureRequest{Harness: localapi.Harness{Name: "claude-code", Session: "w1"}, Kind: localapi.CapturePrompt, Text: "hi"},
			http.StatusTooManyRequests, tooManyCaptures},
		{"a body the uid cannot send is not read", "1000", localapi.RouteDecide, "{not json",
			http.StatusTooManyRequests, tooManyDecides},
		{"another uid's decide", "1001", localapi.RouteDecide, fetch("https://x.example"), http.StatusOK, ""},
	}
	for _, p := range probes {
		t.Run(p.name, func(t *testing.T) {
			w := serveAs(a, p.uid, http.MethodPost, p.route, p.body)
			if w.Code != p.code {
				t.Fatalf("answer = %d %q, want %d", w.Code, w.Body.String(), p.code)
			}
			if p.sentence != "" && refusalOf(t, w) != p.sentence {
				t.Fatalf("refusal = %q, want %q", refusalOf(t, w), p.sentence)
			}
		})
	}

	l.mu.Lock()
	if got := strings.Join(l.refused, ", "); got != "1000 decide, 1000 capture, 1000 decide" {
		t.Errorf("ledger notes = %q, want one per refused call of uid 1000", got)
	}
	l.mu.Unlock()

	close(release)
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusOK {
			t.Errorf("a held decide answered %d", c)
		}
	}
	if w := serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, fetch("https://x.example")); w.Code != http.StatusOK {
		t.Errorf("a decide after the held ones ended = %d %q, want it served", w.Code, w.Body.String())
	}
}

// TestTextBytesInFlightCap pins the per-uid bound on the body bytes of the
// decide and capture calls in flight, counted by their declared length.
// Beside one call of textBodyMax, a decide of a few KiB still fits, one of
// 2 MiB is refused and noted, another uid is still served, and the bytes
// come back when the large call ends.
func TestTextBytesInFlightCap(t *testing.T) {
	f := gateDouble(t, 900, time.Hour, Renewal{})
	f.engine = apiEngine(t, fetchGateDoc, 900)
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	f.decide = func(ctx context.Context, ev policy.Event) (DecideOutcome, error) {
		if ev.Paths[0] == "https://hold.example" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return DecideOutcome{Answer: agentguard.DecideResponse{Effect: policy.EffectAllow, RuleID: "central-ok"}, Status: 200}, nil
	}
	l := &ledgerDouble{apiDouble: f}
	a := newAPI(l, Options{Log: slog.New(slog.DiscardHandler)})
	fetch := func(url string, pad int) localapi.DecideRequest {
		return localapi.DecideRequest{Harness: localapi.Harness{Name: "claude-code", Session: "w1"},
			Event: policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{url, strings.Repeat("p", pad)}}}
	}
	held := make(chan int, 1)
	go func() {
		held <- serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, fetch("https://hold.example", textBodyMax-4096)).Code
	}()
	select {
	case <-entered:
	case code := <-held:
		t.Fatalf("the large decide answered %d before it reached the online gate", code)
	case <-time.After(60 * time.Second): // a 16 MiB body under -race on a busy machine
		t.Fatal("the large decide did not reach the online gate")
	}
	if w := serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, fetch("https://x.example", 0)); w.Code != http.StatusOK {
		t.Fatalf("a small decide beside the large one = %d %q, want it served", w.Code, w.Body.String())
	}
	if w := serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, fetch("https://x.example", 2<<20)); w.Code != http.StatusTooManyRequests || refusalOf(t, w) != tooManyDecides {
		t.Fatalf("a 2 MiB decide past the byte bound = %d %q, want the refusal", w.Code, w.Body.String())
	}
	if w := serveAs(a, "1001", http.MethodPost, localapi.RouteDecide, fetch("https://x.example", 0)); w.Code != http.StatusOK {
		t.Fatalf("another uid's decide = %d %q, want it served", w.Code, w.Body.String())
	}
	close(release)
	if code := <-held; code != http.StatusOK {
		t.Fatalf("the large decide answered %d", code)
	}
	if w := serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, fetch("https://x.example", 2<<20)); w.Code != http.StatusOK {
		t.Fatalf("a 2 MiB decide after the large one ended = %d %q, want it served", w.Code, w.Body.String())
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if got := strings.Join(l.refused, ", "); got != "1000 decide" {
		t.Errorf("ledger notes = %q, want the one refused decide", got)
	}
}
