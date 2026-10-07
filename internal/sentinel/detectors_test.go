package sentinel

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
)

var t0 = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

func testCfg() config.Sentinel {
	return config.Sentinel{
		Enabled:           true,
		DenyBurstWarn:     5,
		DenyBurstCritical: 10,
		DenyBurstWindow:   time.Minute,
		VariantWindow:     10 * time.Minute,
		WriteExecWindow:   30 * time.Minute,
		BaselineMinEvents: 50,
	}
}

// fakeClock drives the engine's wall clock (eviction/sweep); detector windows
// run on EVENT time, which each test sets explicitly.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func testEngine() (*engine, *fakeClock) {
	clk := &fakeClock{t: t0}
	return newEngine(testCfg(), clk.now), clk
}

func shellEv(id, session, cmd, effect string, at time.Time) event {
	return event{ID: id, Kind: "tool", At: at, Session: session, User: "u1",
		Tool: policy.ToolShellExec, Command: cmd, Effect: effect}
}

func writeEv(id, session string, paths []string, effect string, at time.Time) event {
	return event{ID: id, Kind: "tool", At: at, Session: session, User: "u1",
		Tool: policy.ToolFileWrite, Paths: paths, Effect: effect}
}

func captureEv(id, session, kind, content string, at time.Time) event {
	return event{ID: id, Kind: kind, At: at, Session: session, User: "u1", Content: content}
}

// fired flattens verdicts to "detector/severity" for compact assertions.
func fired(vs []Verdict) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Detector+"/"+v.Severity)
	}
	return out
}

func wantFired(t *testing.T, got []Verdict, want ...string) {
	t.Helper()
	g := fired(got)
	if len(g) != len(want) {
		t.Fatalf("verdicts = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("verdicts = %v, want %v", g, want)
		}
	}
}

func TestDenyBurst(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, e *engine)
	}{
		{"four denies stay silent", func(t *testing.T, e *engine) {
			for i := 0; i < 4; i++ {
				ev := shellEv(fmt.Sprintf("d%d", i), "s1", "rm -rf /", policy.EffectDeny, t0.Add(time.Duration(i)*time.Second))
				wantFired(t, e.observe(ev))
			}
		}},
		{"fifth deny in window fires warn with all evidence", func(t *testing.T, e *engine) {
			var vs []Verdict
			for i := 0; i < 5; i++ {
				vs = e.observe(shellEv(fmt.Sprintf("d%d", i), "s1", "x", policy.EffectDeny, t0.Add(time.Duration(i)*time.Second)))
			}
			wantFired(t, vs, "denyBurst/warn")
			if len(vs[0].Evidence) != 5 || vs[0].Evidence[0] != "d0" || vs[0].Evidence[4] != "d4" {
				t.Fatalf("evidence = %v, want the 5 deny CE ids", vs[0].Evidence)
			}
			if vs[0].Window != "1m0s" {
				t.Errorf("window = %q, want 1m0s", vs[0].Window)
			}
		}},
		{"warn dedups then escalates to critical at ten", func(t *testing.T, e *engine) {
			for i := 0; i < 9; i++ {
				vs := e.observe(shellEv(fmt.Sprintf("d%d", i), "s1", "x", policy.EffectDeny, t0.Add(time.Duration(i)*time.Second)))
				if i == 4 {
					wantFired(t, vs, "denyBurst/warn")
				} else {
					wantFired(t, vs) // 5..8: warn already fired this window
				}
			}
			vs := e.observe(shellEv("d9", "s1", "x", policy.EffectDeny, t0.Add(9*time.Second)))
			wantFired(t, vs, "denyBurst/critical")
			vs = e.observe(shellEv("d10", "s1", "x", policy.EffectDeny, t0.Add(10*time.Second)))
			wantFired(t, vs) // critical dedups within the window too
		}},
		{"denies spread beyond the window never accumulate", func(t *testing.T, e *engine) {
			for i := 0; i < 8; i++ {
				vs := e.observe(shellEv(fmt.Sprintf("d%d", i), "s1", "x", policy.EffectDeny, t0.Add(time.Duration(i)*20*time.Second)))
				wantFired(t, vs)
			}
		}},
		{"warn re-fires after the window has passed", func(t *testing.T, e *engine) {
			for i := 0; i < 5; i++ {
				e.observe(shellEv(fmt.Sprintf("a%d", i), "s1", "x", policy.EffectDeny, t0.Add(time.Duration(i)*time.Second)))
			}
			later := t0.Add(3 * time.Minute)
			var vs []Verdict
			for i := 0; i < 5; i++ {
				vs = e.observe(shellEv(fmt.Sprintf("b%d", i), "s1", "x", policy.EffectDeny, later.Add(time.Duration(i)*time.Second)))
			}
			wantFired(t, vs, "denyBurst/warn")
		}},
		{"allows do not count", func(t *testing.T, e *engine) {
			for i := 0; i < 20; i++ {
				vs := e.observe(shellEv(fmt.Sprintf("a%d", i), "s1", "x", policy.EffectAllow, t0.Add(time.Duration(i)*time.Second)))
				wantFired(t, vs)
			}
		}},
		{"sessions are independent", func(t *testing.T, e *engine) {
			for i := 0; i < 4; i++ {
				e.observe(shellEv(fmt.Sprintf("d%d", i), "s1", "x", policy.EffectDeny, t0.Add(time.Duration(i)*time.Second)))
			}
			vs := e.observe(shellEv("other", "s2", "x", policy.EffectDeny, t0.Add(4*time.Second)))
			wantFired(t, vs)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine()
			tc.run(t, e)
		})
	}
}

func TestDenyThenVariant(t *testing.T) {
	denied := "rm -rf /srv/data"
	b64 := base64.RawStdEncoding.EncodeToString([]byte(denied))
	cases := []struct {
		name    string
		attempt string        // the follow-up shell.exec command
		gap     time.Duration // time between the deny and the attempt
		want    []string      // expected detector/severity on the attempt
	}{
		{"interpreter -c wrap fires", `bash -c 'rm -rf /srv/data'`, time.Minute, []string{"denyThenVariant/critical"}},
		{"token-overlap rewrite fires", `sudo rm -rf /srv/data/backup`, time.Minute, []string{"denyThenVariant/critical"}},
		{"base64 re-encode fires", "echo " + b64 + " | base64 -d | sh", time.Minute, []string{"denyThenVariant/critical"}},
		{"identical retry is not a variant", denied, time.Minute, nil},
		{"unrelated command stays silent", "git status", time.Minute, nil},
		{"variant after the window stays silent", `bash -c 'rm -rf /srv/data'`, 11 * time.Minute, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine()
			wantFired(t, e.observe(shellEv("deny1", "s1", denied, policy.EffectDeny, t0)))
			vs := e.observe(shellEv("try1", "s1", tc.attempt, policy.EffectAllow, t0.Add(tc.gap)))
			wantFired(t, vs, tc.want...)
			if len(tc.want) > 0 {
				if len(vs[0].Evidence) != 2 || vs[0].Evidence[0] != "deny1" || vs[0].Evidence[1] != "try1" {
					t.Fatalf("evidence = %v, want [deny1 try1]", vs[0].Evidence)
				}
			}
		})
	}

	t.Run("overlap boundary at 60 percent", func(t *testing.T) {
		e, _ := testEngine()
		// 5 normalized tokens; 3/5 present = 60% (fires), 2/5 = 40% (silent).
		e.observe(shellEv("deny1", "s1", "alpha beta gamma delta epsilon", policy.EffectDeny, t0))
		vs := e.observe(shellEv("try1", "s1", "alpha beta gamma zeta eta", policy.EffectAllow, t0.Add(time.Minute)))
		wantFired(t, vs, "denyThenVariant/critical")
		e2, _ := testEngine()
		e2.observe(shellEv("deny1", "s1", "alpha beta gamma delta epsilon", policy.EffectDeny, t0))
		vs = e2.observe(shellEv("try1", "s1", "alpha beta zeta eta theta", policy.EffectAllow, t0.Add(time.Minute)))
		wantFired(t, vs)
	})

	t.Run("dedup fires once per window", func(t *testing.T) {
		e, _ := testEngine()
		e.observe(shellEv("deny1", "s1", denied, policy.EffectDeny, t0))
		wantFired(t, e.observe(shellEv("try1", "s1", `bash -c 'rm -rf /srv/data'`, policy.EffectAllow, t0.Add(time.Minute))), "denyThenVariant/critical")
		wantFired(t, e.observe(shellEv("try2", "s1", `sh -c 'rm -rf /srv/data'`, policy.EffectAllow, t0.Add(2*time.Minute))))
	})

	t.Run("other sessions do not inherit denied commands", func(t *testing.T) {
		e, _ := testEngine()
		e.observe(shellEv("deny1", "s1", denied, policy.EffectDeny, t0))
		wantFired(t, e.observe(shellEv("try1", "s2", `bash -c 'rm -rf /srv/data'`, policy.EffectAllow, t0.Add(time.Minute))))
	})

	t.Run("a denied variant still fires and is recorded", func(t *testing.T) {
		e, _ := testEngine()
		e.observe(shellEv("deny1", "s1", denied, policy.EffectDeny, t0))
		vs := e.observe(shellEv("deny2", "s1", `bash -c "rm -rf /srv/data"`, policy.EffectDeny, t0.Add(time.Minute)))
		wantFired(t, vs, "denyThenVariant/critical")
	})
}

func TestWriteThenExecute(t *testing.T) {
	cases := []struct {
		name string
		path string
		wEff string
		cmd  string
		gap  time.Duration
		want []string
	}{
		{"absolute path exec fires", "/tmp/evil.py", policy.EffectAllow, "python3 /tmp/evil.py", time.Minute, []string{"writeThenExecute/critical"}},
		{"basename exec fires", "/work/repo/run.sh", policy.EffectAllow, "cd /work/repo && ./run.sh", time.Minute, []string{"writeThenExecute/critical"}},
		{"unrelated exec stays silent", "/tmp/evil.py", policy.EffectAllow, "ls -la /tmp", time.Minute, nil},
		{"denied write wrote nothing", "/tmp/evil.py", policy.EffectDeny, "python3 /tmp/evil.py", time.Minute, nil},
		{"exec beyond the window stays silent", "/tmp/evil.py", policy.EffectAllow, "python3 /tmp/evil.py", 31 * time.Minute, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine()
			wantFired(t, e.observe(writeEv("w1", "s1", []string{tc.path}, tc.wEff, t0)))
			vs := e.observe(shellEv("x1", "s1", tc.cmd, policy.EffectAllow, t0.Add(tc.gap)))
			wantFired(t, vs, tc.want...)
			if len(tc.want) > 0 {
				if len(vs[0].Evidence) != 2 || vs[0].Evidence[0] != "w1" || vs[0].Evidence[1] != "x1" {
					t.Fatalf("evidence = %v, want [w1 x1]", vs[0].Evidence)
				}
			}
		})
	}

	t.Run("ring caps at 256 written paths", func(t *testing.T) {
		e, _ := testEngine()
		for i := 0; i < 257; i++ {
			e.observe(writeEv(fmt.Sprintf("w%d", i), "s1", []string{fmt.Sprintf("/tmp/f%03d.py", i)}, policy.EffectAllow, t0))
		}
		// Only the writeThenExecute lane is under test: 257 same-user events
		// also establish a tool-mix baseline, which fires independently.
		only := func(vs []Verdict) []Verdict {
			out := vs[:0]
			for _, v := range vs {
				if v.Detector == DetectorWriteExec {
					out = append(out, v)
				}
			}
			return out
		}
		// The oldest write fell off the ring; a recent one is still tracked.
		wantFired(t, only(e.observe(shellEv("x1", "s1", "python3 /tmp/f000.py", policy.EffectAllow, t0.Add(time.Minute)))))
		wantFired(t, only(e.observe(shellEv("x2", "s1", "python3 /tmp/f256.py", policy.EffectAllow, t0.Add(2*time.Minute)))), "writeThenExecute/critical")
	})

	t.Run("short paths are ignored", func(t *testing.T) {
		e, _ := testEngine()
		e.observe(writeEv("w1", "s1", []string{"x"}, policy.EffectAllow, t0))
		wantFired(t, e.observe(shellEv("x1", "s1", "expand xylophone", policy.EffectAllow, t0.Add(time.Minute))))
	})

	t.Run("basename must be a token not a substring", func(t *testing.T) {
		e, _ := testEngine()
		e.observe(writeEv("w1", "s1", []string{"/tmp/a.sh"}, policy.EffectAllow, t0))
		wantFired(t, e.observe(shellEv("x1", "s1", "cat data.sh", policy.EffectAllow, t0.Add(time.Minute))))
	})
}

func TestCaptureContent(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		content string
		want    []string
	}{
		{"aws key in reply", "reply", "use AKIAABCDEFGHIJKLMNOP for the deploy", []string{"captureContent/critical"}},
		{"github token in prompt", "prompt", "push with ghp_" + strings.Repeat("a", 24), []string{"captureContent/critical"}},
		{"pem private key", "reply", "-----BEGIN RSA PRIVATE KEY-----\nMIIE...", []string{"captureContent/critical"}},
		{"bearer token", "prompt", "curl -H 'Authorization: Bearer " + strings.Repeat("t", 32) + "'", []string{"captureContent/critical"}},
		{"straza token", "reply", "wst_" + strings.Repeat("k", 24), []string{"captureContent/critical"}},
		{"clean content", "prompt", "please review the quarterly report", nil},
		{"short bearer stays silent", "prompt", "Bearer abc", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine()
			vs := e.observe(captureEv("c1", "s1", tc.kind, tc.content, t0))
			wantFired(t, vs, tc.want...)
			if len(tc.want) > 0 && (len(vs[0].Evidence) != 1 || vs[0].Evidence[0] != "c1") {
				t.Fatalf("evidence = %v, want [c1]", vs[0].Evidence)
			}
		})
	}

	t.Run("dedup fires once per window", func(t *testing.T) {
		e, _ := testEngine()
		leak := "AKIAABCDEFGHIJKLMNOP"
		wantFired(t, e.observe(captureEv("c1", "s1", "prompt", leak, t0)), "captureContent/critical")
		wantFired(t, e.observe(captureEv("c2", "s1", "reply", leak, t0.Add(time.Minute))))
		wantFired(t, e.observe(captureEv("c3", "s1", "reply", leak, t0.Add(11*time.Minute))), "captureContent/critical")
	})

	t.Run("tool events never run content detection", func(t *testing.T) {
		e, _ := testEngine()
		wantFired(t, e.observe(shellEv("x1", "s1", "echo AKIAABCDEFGHIJKLMNOP", policy.EffectAllow, t0)))
	})
}

func TestToolMixAnomaly(t *testing.T) {
	baselineEv := func(id, session, user, tool string, at time.Time) event {
		return event{ID: id, Kind: "tool", At: at, Session: session, User: user, Tool: tool, Effect: policy.EffectAllow}
	}

	t.Run("novel tool after established baseline fires info", func(t *testing.T) {
		e, _ := testEngine()
		for i := 0; i < 50; i++ {
			wantFired(t, e.observe(baselineEv(fmt.Sprintf("b%d", i), "s1", "alice", policy.ToolShellExec, t0.Add(time.Duration(i)*time.Second))))
		}
		vs := e.observe(baselineEv("novel", "s1", "alice", policy.ToolFileWrite, t0.Add(time.Hour/2)))
		wantFired(t, vs, "toolMixAnomaly/info")
		if len(vs[0].Evidence) != 1 || vs[0].Evidence[0] != "novel" {
			t.Fatalf("evidence = %v, want [novel]", vs[0].Evidence)
		}
		// The novel tool joins the baseline: no re-fire.
		wantFired(t, e.observe(baselineEv("again", "s1", "alice", policy.ToolFileWrite, t0.Add(time.Hour))))
	})

	t.Run("below the event floor everything is baseline building", func(t *testing.T) {
		e, _ := testEngine()
		for i := 0; i < 49; i++ {
			e.observe(baselineEv(fmt.Sprintf("b%d", i), "s1", "alice", policy.ToolShellExec, t0))
		}
		wantFired(t, e.observe(baselineEv("early", "s1", "alice", policy.ToolFileWrite, t0.Add(time.Second))))
	})

	t.Run("baseline is per user across sessions", func(t *testing.T) {
		e, _ := testEngine()
		for i := 0; i < 50; i++ {
			e.observe(baselineEv(fmt.Sprintf("b%d", i), "s1", "alice", policy.ToolShellExec, t0.Add(time.Duration(i)*time.Second)))
		}
		// Same user, NEW session: the baseline carries over and a novel tool fires.
		vs := e.observe(baselineEv("novel", "s2", "alice", policy.ToolNetFetch, t0.Add(time.Hour/2)))
		wantFired(t, vs, "toolMixAnomaly/info")
		if vs[0].Session != "s2" {
			t.Errorf("session = %q, want s2", vs[0].Session)
		}
		// A different user has no baseline yet: silent.
		wantFired(t, e.observe(baselineEv("bob1", "s3", "bob", policy.ToolNetFetch, t0.Add(time.Hour/2))))
	})

	t.Run("mcp calls key by app and tool name", func(t *testing.T) {
		e, _ := testEngine()
		mcp := func(id, app, toolName string, at time.Time) event {
			return event{ID: id, Kind: "mcp", At: at, Session: "s1", User: "alice",
				Tool: policy.ToolMCPCall, App: app, ToolName: toolName, Effect: policy.EffectAllow}
		}
		for i := 0; i < 50; i++ {
			e.observe(mcp(fmt.Sprintf("b%d", i), "github", "list_issues", t0.Add(time.Duration(i)*time.Second)))
		}
		wantFired(t, e.observe(mcp("novel", "github", "delete_repo", t0.Add(time.Hour/2))), "toolMixAnomaly/info")
	})
}

func TestSessionEvictionAndCaps(t *testing.T) {
	t.Run("idle sessions evict after the TTL", func(t *testing.T) {
		e, clk := testEngine()
		e.observe(shellEv("d1", "s1", "x", policy.EffectDeny, t0))
		if e.sessions.len() != 1 {
			t.Fatalf("sessions = %d, want 1", e.sessions.len())
		}
		clk.t = t0.Add(2 * time.Hour) // beyond sessionTTL; next observe sweeps
		e.observe(shellEv("d2", "s2", "x", policy.EffectDeny, clk.t))
		if _, ok := e.sessions.m["s1"]; ok {
			t.Error("idle session s1 survived the sweep")
		}
		if _, ok := e.sessions.m["s2"]; !ok {
			t.Error("live session s2 was evicted")
		}
	})

	t.Run("session table LRU-evicts at capacity", func(t *testing.T) {
		e, _ := testEngine()
		e.maxSessions = 3
		for i := 0; i < 4; i++ {
			e.observe(shellEv(fmt.Sprintf("d%d", i), fmt.Sprintf("s%d", i), "x", policy.EffectDeny, t0.Add(time.Duration(i)*time.Second)))
		}
		if e.sessions.len() != 3 {
			t.Fatalf("sessions = %d, want cap 3", e.sessions.len())
		}
		if _, ok := e.sessions.m["s0"]; ok {
			t.Error("least-recently-seen session s0 survived cap eviction")
		}
	})

	t.Run("user baseline table LRU-evicts at capacity", func(t *testing.T) {
		e, _ := testEngine()
		e.maxUsers = 2
		for i := 0; i < 3; i++ {
			ev := shellEv(fmt.Sprintf("d%d", i), "s1", "x", policy.EffectAllow, t0)
			ev.User = fmt.Sprintf("u%d", i)
			e.observe(ev)
		}
		if e.users.len() != 2 {
			t.Fatalf("users = %d, want cap 2", e.users.len())
		}
		if _, ok := e.users.m["u0"]; ok {
			t.Error("least-recently-seen user u0 survived cap eviction")
		}
	})
}

func TestParseCE(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"tool event", `{"id":"e1","type":"straza.audit.tool","time":"2026-07-17T12:00:00Z","data":{"session":"s1","user":"u1","tool":"shell.exec","command":"ls","effect":"allow"}}`, true},
		{"prompt event", `{"id":"e2","type":"straza.audit.prompt","data":{"session":"s1","content":"hi"}}`, true},
		{"malformed json", `{"id":`, false},
		{"missing session", `{"id":"e3","type":"straza.audit.tool","data":{"tool":"shell.exec"}}`, false},
		{"foreign type", `{"id":"e4","type":"straza.audit.identity","data":{"session":"s1"}}`, false},
		{"own verdict type never loops", `{"id":"e5","type":"straza.audit.sentinel","data":{"session":"s1"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := parseCE([]byte(tc.raw), t0)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if ev.Session != "s1" {
				t.Errorf("session = %q", ev.Session)
			}
		})
	}

	t.Run("event time parsed, fallback otherwise", func(t *testing.T) {
		ev, _ := parseCE([]byte(`{"id":"e1","type":"straza.audit.tool","time":"2026-07-17T09:30:00Z","data":{"session":"s1"}}`), t0)
		if !ev.At.Equal(time.Date(2026, 7, 17, 9, 30, 0, 0, time.UTC)) {
			t.Errorf("at = %s, want the CE time", ev.At)
		}
		ev, _ = parseCE([]byte(`{"id":"e1","type":"straza.audit.tool","data":{"session":"s1"}}`), t0)
		if !ev.At.Equal(t0) {
			t.Errorf("at = %s, want the fallback", ev.At)
		}
	})
}

func TestTokenizeAndOverlap(t *testing.T) {
	toks := tokenize(`bash -c 'rm -rf "/srv/data"'`)
	for _, want := range []string{"bash", "-c", "rm", "-rf", "/srv/data"} {
		if !toks[want] {
			t.Errorf("tokenize missed %q in %v", want, toks)
		}
	}
	if got := overlap(tokenize("a b c d e"), tokenize("a b c x y")); got < 0.59 || got > 0.61 {
		t.Errorf("overlap = %v, want 0.6", got)
	}
	if got := overlap(map[string]bool{}, tokenize("a")); got != 0 {
		t.Errorf("empty denied overlap = %v, want 0", got)
	}
}
