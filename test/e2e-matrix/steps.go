package e2ematrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// attempt is one try of a step: whether want held, why not, and the decoded
// response body a save key may read.
type attempt struct {
	ok     bool
	detail string
	body   any
}

// scenarioRun is one scenario under one harness: its own booted stack, its
// identities, saved values, audit window and verdict.
type scenarioRun struct {
	r       *run
	sc      *scenario
	harness string
	dir     string
	stack   *stack
	clients map[string]*client
	tokens  map[string]string
	// approvers holds the signing device an approver enroll step made for a
	// person, so a later decide step signs with the same key.
	approvers map[string]*approverDevice
	vars      map[string]string
	auditSeq  int64
	lastEnd   time.Time
	failure   string
	stopped   bool
}

// fail records the first failure and stops the run, since every later step
// depends on the one that failed.
func (s *scenarioRun) fail(stepName, reason string) {
	if s.stopped {
		return
	}
	s.failure = stepName + ": " + redact(reason)
	s.stopped = true
	s.r.logf("FAIL %s [%s] %s: %s", s.sc.ID, s.harness, stepName, redact(reason))
}

// runStep executes one step under the run's harness.
func (s *scenarioRun) runStep(ctx context.Context, st *step) {
	defer func() { s.lastEnd = time.Now() }()
	if st.Action == "hook" && !st.runsUnder(s.harness) {
		return
	}
	args, want, err := s.expandStep(st)
	if err != nil {
		s.fail(st.Name, err.Error())
		return
	}
	var try func() (attempt, error)
	within := st.Within
	switch st.Action {
	case "admin":
		try = func() (attempt, error) {
			bearer, err := s.adminBearer(ctx, st)
			if err != nil {
				return attempt{}, err
			}
			return s.httpStep(ctx, args, want, bearer)
		}
	case "scim":
		try = func() (attempt, error) { return s.httpStep(ctx, args, want, s.stack.server.idm) }
	case "enroll":
		try = func() (attempt, error) { return attempt{ok: true}, s.clients[st.str("as")].enroll(ctx) }
	case "approver":
		try = func() (attempt, error) { return s.approverStep(ctx, args, want) }
	case "daemon":
		try = func() (attempt, error) { return s.daemonStep(st) }
	case "server":
		try = func() (attempt, error) { return s.serverStep(ctx, st) }
	case "hook":
		payload, err := s.hookPayload(args)
		if err != nil {
			s.fail(st.Name, err.Error())
			return
		}
		try = func() (attempt, error) { return s.hookStep(ctx, st, want, payload) }
	case "mcp":
		try = func() (attempt, error) { return s.mcpStep(ctx, st, args, want) }
	case "audit":
		if within == 0 {
			within = 10 * time.Second
		}
		try = func() (attempt, error) { return s.auditStep(ctx, args) }
	case "wait":
		// A window that lapses leaves nothing to poll for: the call that
		// proves it comes after the sleep, so the step sleeps and asserts
		// nothing of its own.
		try = func() (attempt, error) {
			select {
			case <-time.After(st.Wait):
			case <-ctx.Done():
			}
			return attempt{ok: true}, nil
		}
	}
	a, elapsed, err := s.untilHolds(within, try)
	if err != nil {
		s.fail(st.Name, err.Error())
		return
	}
	if !a.ok {
		s.fail(st.Name, a.detail)
		return
	}
	if st.Timer != "" {
		s.r.timer(st.Timer, s.harness, elapsed)
	}
	if err := s.save(st, a.body); err != nil {
		s.fail(st.Name, err.Error())
	}
}

// untilHolds repeats try at the poll cadence until want holds or the
// deadline passes, and returns the elapsed time from the end of the previous
// step to the attempt that held.
func (s *scenarioRun) untilHolds(within time.Duration, try func() (attempt, error)) (attempt, time.Duration, error) {
	deadline := time.Now().Add(within)
	cadence := pollCadence(within)
	for {
		a, err := try()
		elapsed := time.Since(s.lastEnd)
		if err != nil {
			return a, elapsed, err
		}
		if a.ok || within == 0 || time.Now().After(deadline) {
			return a, elapsed, nil
		}
		time.Sleep(cadence)
	}
}

// pollCadence is the pause between attempts of a step with within: 100 ms,
// or one two-hundredth of the wait when that is longer, so a clock-driven
// wait of minutes polls about once a second instead of spawning thousands
// of hooks whose records would flood the chain.
func pollCadence(within time.Duration) time.Duration {
	if c := within / 200; c > 100*time.Millisecond {
		return c
	}
	return 100 * time.Millisecond
}

// expandStep substitutes the placeholders in the step's arguments and want.
func (s *scenarioRun) expandStep(st *step) (map[string]any, map[string]any, error) {
	args, err := expand(st.Args, s.vars)
	if err != nil {
		return nil, nil, err
	}
	want, err := expand(st.Want, s.vars)
	if err != nil {
		return nil, nil, err
	}
	w, _ := want.(map[string]any)
	return args.(map[string]any), w, nil
}

// save stores the requested json paths of the response body as placeholders.
func (s *scenarioRun) save(st *step, body any) error {
	for name, path := range st.Save {
		v, ok := lookup(body, path)
		if !ok {
			return fmt.Errorf("save %s: no %s in the response body", name, path)
		}
		s.vars[name] = asString(v)
	}
	return nil
}

// httpStep sends an admin or scim request and checks status and body.
func (s *scenarioRun) httpStep(ctx context.Context, args, want map[string]any, bearer string) (attempt, error) {
	method, _ := args["method"].(string)
	path, _ := args["path"].(string)
	contentType := "application/json"
	var raw []byte
	switch body := args["body"].(type) {
	case nil:
	case map[string]any:
		if y, ok := body["yaml"].(string); ok && len(body) == 1 {
			contentType, raw = "application/yaml", []byte(y)
		} else {
			raw, _ = json.Marshal(body)
		}
	default:
		raw, _ = json.Marshal(body)
	}
	if y, ok := args["yaml"].(string); ok {
		contentType, raw = "application/yaml", []byte(y)
	}
	target, err := requestURL(s.stack.server.URL, path)
	if err != nil {
		return attempt{}, err
	}
	code, resp, err := httpCall(ctx, strings.ToUpper(method), target, bearer, contentType, raw)
	if err != nil {
		return attempt{}, err
	}
	var body any
	_ = json.Unmarshal(resp, &body)
	a := attempt{ok: true, body: body}
	if wantStatus, ok := wantInt(want, "status"); ok {
		if code != wantStatus {
			a.ok, a.detail = false, fmt.Sprintf("%s %s: status %d, want %d: %s", method, path, code, wantStatus, trim(redact(string(resp))))
		}
	} else if code >= 300 {
		a.ok, a.detail = false, fmt.Sprintf("%s %s: status %d: %s", method, path, code, trim(redact(string(resp))))
	}
	if a.ok {
		if fields, ok := want["body"].(map[string]any); ok {
			for _, p := range sortedKeys(fields) {
				wantVal := fields[p]
				got, found := lookup(body, p)
				// A null expectation asserts the path is absent, which is how
				// a scenario says a list has exactly so many items.
				if wantVal == nil && found {
					a.ok, a.detail = false, fmt.Sprintf("%s %s: body.%s = %s, want it absent", method, path, p, asString(got))
					break
				}
				if wantVal != nil && (!found || !sameValue(got, wantVal)) {
					a.ok, a.detail = false, fmt.Sprintf("%s %s: body.%s = %s, want %s", method, path, p, asString(got), asString(wantVal))
					break
				}
			}
		}
	}
	if sub, ok := want["bodyContains"].(string); ok && a.ok && !strings.Contains(string(resp), sub) {
		a.ok, a.detail = false, fmt.Sprintf("%s %s: the answer does not contain %q: %s", method, path, sub, trim(redact(string(resp))))
	}
	return a, nil
}

// requestURL joins the server base with a step path, encoding the query the
// way a browser would, so a SCIM filter with spaces and quotes stays valid.
func requestURL(base, path string) (string, error) {
	p, query, _ := strings.Cut(path, "?")
	if query == "" {
		return base + p, nil
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("path %q: %w", path, err)
	}
	return base + p + "?" + q.Encode(), nil
}

// wantInt reads an integer field written in YAML.
func wantInt(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

// hookStep spawns the real straza hook once, times it, and judges the
// answer under the run's harness. A session.start that passes has the
// client remember the token it opened, for a gateway call after a revoke.
func (s *scenarioRun) hookStep(ctx context.Context, st *step, want map[string]any, payload []byte) (attempt, error) {
	c := s.clients[st.str("as")]
	o, err := c.hook(ctx, s.harness, payload)
	if err != nil {
		return attempt{}, err
	}
	// A polled hook step is a wait for a clock or a propagation, not an
	// overhead sample: its attempts include the ones that refresh or
	// re-acquire over the network, which the fast-path budget never covered.
	if st.Within == 0 {
		s.r.hookTime(s.harness, st.eventKind(), o.Elapsed)
	}
	ok, detail := judgeHook(s.harness, want, o)
	if ok && st.eventKind() == "session.start" {
		_, _ = c.sessionToken()
	}
	return attempt{ok: ok, detail: detail}, nil
}

// hookPayload encodes the hook's stdin: the canonical event rendered for the
// run's harness, or the raw document the step names for it.
func (s *scenarioRun) hookPayload(args map[string]any) ([]byte, error) {
	if raw, ok := args["payload"].(map[string]any); ok {
		switch doc := raw[s.harness].(type) {
		case string:
			return []byte(doc), nil
		default:
			return json.Marshal(doc)
		}
	}
	ev, err := eventFromMap(args["event"].(map[string]any))
	if err != nil {
		return nil, err
	}
	as, _ := args["as"].(string)
	p, err := renderPayload(s.harness, ev, "e2e-"+s.sc.ID+"-"+as+"-"+s.harness)
	if err != nil {
		return nil, err
	}
	return json.Marshal(p)
}
