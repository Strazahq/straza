package server

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestGatewayRemoteDefaultRate pins the default rate of a remote server whose
// manifest sets none: one session is served until its 20 calls a second are
// spent, the next call is refused with the rate sentence naming 20 and leaves
// exactly one deny record naming the person, the server, the tool and that
// sentence, and a second session of the same person is served at once,
// because each session spends from a bucket of its own.
func TestGatewayRemoteDefaultRate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	up := startGatewayUpstream(t)
	user := seedGatewayUser(t, app, "rhea", "dev")
	installEchoApp(t, app, up, "unrated", 0)
	catActivate(t, app, "unrated-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: unrated-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-echo
      tools: [mcp.call]
      apps: [unrated]
      toolNames: {allow: ["echo"]}
      effect: allow
`)
	tokA, sidA := gatewaySession(t, base, "rhea")
	tokB, sidB := gatewaySession(t, base, "rhea")
	if sidA == sidB {
		t.Fatalf("both check-ins answered session %s, want two sessions", sidA)
	}
	call := func(tok string) string {
		t.Helper()
		_, res, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
			"name": "unrated__echo", "arguments": map[string]string{"text": "x"},
		})
		result, _ := res["result"].(map[string]any)
		content, _ := result["content"].([]any)
		if len(content) != 1 {
			t.Fatalf("tools/call answer = %s, want one text content", raw)
		}
		first, _ := content[0].(map[string]any)
		text, _ := first["text"].(string)
		return text
	}
	refusal := `Straza: rate limit exceeded for the MCP server "unrated" (20 rps). Wait a few seconds, then try the call once more. If it is denied again, tell the user.`

	start := time.Now()
	served, refused := 0, false
	for !refused && served < 100 {
		switch got := call(tokA); got {
		case "echo: x":
			served++
		case refusal:
			refused = true
		default:
			t.Fatalf("call %d answered %q, want the echo or the rate refusal", served+1, got)
		}
	}
	elapsed := time.Since(start)
	// The bucket starts with 20 and refills 20 a second, so the calls served
	// before the first refusal are 20 plus what refilled while they ran: the
	// 21st is refused when the calls take under 50 ms.
	if !refused || served < 20 || served > 20+int(elapsed.Seconds()*20) {
		t.Fatalf("%d calls served in %v, refused %v; want 20 plus at most the refill, then the refusal", served, elapsed, refused)
	}
	t.Logf("%d calls served in %v before the refusal", served, elapsed)

	rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
		return d["session"] == sidA && d["effect"] == "deny"
	})[0]
	for k, v := range map[string]any{"user": user.ID, "app": "unrated", "toolName": "echo", "reason": refusal} {
		if rec[k] != v {
			t.Errorf("refusal record %s = %v, want %v", k, rec[k], v)
		}
	}

	if got := call(tokB); got != "echo: x" {
		t.Fatalf("the second session answered %q, want the echo", got)
	}
	awaitMCPRecords(t, app, 0, func(d map[string]any) bool {
		return d["session"] == sidB && d["effect"] == "deny"
	})
}

// userRateSetup installs the remote echo server shared with the session
// rate rps, 0 for the default, and a policy that lets the role dev call it,
// and seeds the people rhea and theo with that role.
func userRateSetup(t *testing.T, rps int) (*App, string, store.User) {
	t.Helper()
	app, base := testApp(t)
	up := startGatewayUpstream(t)
	user := seedGatewayUser(t, app, "rhea", "dev")
	seedGatewayUser(t, app, "theo", "dev")
	installEchoApp(t, app, up, "shared", rps)
	catActivate(t, app, "shared-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: shared-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-echo
      tools: [mcp.call]
      apps: [shared]
      toolNames: {allow: ["echo"]}
      effect: allow
`)
	return app, base, user
}

// echoShared calls the echo tool of the server shared with tok and returns
// the text of the answer, or "" after reporting an answer without one text.
func echoShared(t *testing.T, base, tok string) string {
	t.Helper()
	_, res, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": "shared__echo", "arguments": map[string]string{"text": "x"},
	})
	result, _ := res["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Errorf("tools/call answer = %s, want one text content", raw)
		return ""
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

// wantUserRate is the refusal of a call past the ceiling n of the
// server shared, and wantSessionRateOf the one past its session rate n.
func wantUserRate(n string) string {
	return `Straza: rate limit exceeded for the MCP server "shared": all your sessions together may make ` + n +
		` calls a second. Wait a few seconds, then try the call once more. If it is denied again, tell the user.`
}

func wantSessionRateOf(n string) string {
	return `Straza: rate limit exceeded for the MCP server "shared" (` + n +
		` rps). Wait a few seconds, then try the call once more. If it is denied again, tell the user.`
}

// TestGatewayUserRate pins the ceiling one user has across its sessions for
// one server, five times the server's rate for a session. Eight sessions of
// one person call at once until the ceiling refuses one of them. The calls
// served are the ceiling plus at most its refill, each refusal names the
// ceiling and leaves exactly one deny record naming the person, a session
// of hers and the server, and a session of another person is served in the
// same instant, because each user spends from a ceiling of its own.
func TestGatewayUserRate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		rps     int
		session string
		ceiling int
	}{
		{name: "the remote default", rps: 0, session: "20", ceiling: 100},
		{name: "a rate of one, where the ceiling holds five sessions' first calls", rps: 1, session: "1", ceiling: 5},
		{name: "the manifest's rate", rps: 4, session: "4", ceiling: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, base, user := userRateSetup(t, tc.rps)
			const sessions = 8
			var toks [sessions]string
			mine := map[any]bool{}
			for i := range toks {
				var sid string
				toks[i], sid = gatewaySession(t, base, "rhea")
				mine[sid] = true
			}
			other, _ := gatewaySession(t, base, "theo")
			wantSession, wantUser := wantSessionRateOf(tc.session), wantUserRate(strconv.Itoa(tc.ceiling))

			// Each session calls from its own goroutine, so together they
			// outrun the ceiling's refill even when one call is slow. A
			// session that spent its own rate just calls again.
			var served, refused atomic.Int64
			var wg sync.WaitGroup
			start := time.Now()
			for _, tok := range toks {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 200 && refused.Load() == 0; i++ {
						switch got := echoShared(t, base, tok); got {
						case "echo: x":
							served.Add(1)
						case wantSession:
						case wantUser:
							refused.Add(1)
							return
						default:
							t.Errorf("a call answered %q, want the echo or a rate refusal", got)
							return
						}
					}
				}()
			}
			wg.Wait()
			// The other person calls before the ceiling can refill, so a
			// ceiling that every user shared would refuse this call.
			if got := echoShared(t, base, other); got != "echo: x" {
				t.Errorf("the other person's session answered %q, want the echo", got)
			}
			elapsed := time.Since(start)
			// The ceiling starts full and refills at its own rate, so the
			// calls served are the ceiling plus at most what refilled.
			if n := int(served.Load()); refused.Load() == 0 || n < tc.ceiling || n > tc.ceiling+int(elapsed.Seconds()*float64(tc.ceiling))+1 {
				t.Fatalf("%d calls served in %v with %d refusals; want %d plus at most the refill, then a refusal",
					n, elapsed, refused.Load(), tc.ceiling)
			}
			t.Logf("%d calls served in %v, %d refused for the ceiling", served.Load(), elapsed, refused.Load())

			recs := awaitMCPRecords(t, app, int(refused.Load()), func(d map[string]any) bool {
				return d["reason"] == wantUser
			})
			for _, rec := range recs {
				for k, v := range map[string]any{"user": user.ID, "app": "shared", "toolName": "echo", "effect": "deny"} {
					if rec[k] != v {
						t.Errorf("refusal record %s = %v, want %v", k, rec[k], v)
					}
				}
				if !mine[rec["session"]] {
					t.Errorf("refusal record names the session %v, want one of the person's eight", rec["session"])
				}
			}
		})
	}
}

// TestGatewayUserRateSessionFirst pins the order of the two limits: a
// session that calls past its own rate is refused there and spends nothing
// of the user's ceiling, so four more sessions of the same person are each
// served their first call.
func TestGatewayUserRateSessionFirst(t *testing.T) {
	t.Parallel()
	_, base, _ := userRateSetup(t, 1)
	loud, _ := gatewaySession(t, base, "rhea")
	var quiet [4]string
	for i := range quiet {
		quiet[i], _ = gatewaySession(t, base, "rhea")
	}
	// Ten calls take far less than the second in which the session's one
	// call refills, so one is served and nine are refused by its own rate.
	served := 0
	for i := 0; i < 10; i++ {
		switch got := echoShared(t, base, loud); got {
		case "echo: x":
			served++
		case wantSessionRateOf("1"):
		default:
			t.Fatalf("call %d of the loud session answered %q, want the echo or its own rate refusal", i+1, got)
		}
	}
	if served < 1 || served > 2 {
		t.Fatalf("the loud session was served %d of 10 calls, want its one call and at most one refill", served)
	}
	for i, tok := range quiet {
		if got := echoShared(t, base, tok); got != "echo: x" {
			t.Errorf("session %d answered %q, want the echo: the loud session's refused calls spent the ceiling", i+1, got)
		}
	}
}

// TestUserCeiling pins the ceiling's rate and burst for a session rate:
// five times the rate, and five sessions' bursts, where one session's burst
// is one call for a rate under 1. A server with no rate has no ceiling.
func TestUserCeiling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ rps, rate, burst float64 }{
		{rps: 20, rate: 100, burst: 100},
		{rps: 1, rate: 5, burst: 5},
		{rps: 0.5, rate: 2.5, burst: 5},
		{rps: 0.01, rate: 0.05, burst: 5},
		{rps: 0, rate: 0, burst: 5},
	} {
		if rate, burst := userCeiling(tc.rps); rate != tc.rate || burst != tc.burst {
			t.Errorf("userCeiling(%v) = %v, %v, want %v, %v", tc.rps, rate, burst, tc.rate, tc.burst)
		}
	}
}
