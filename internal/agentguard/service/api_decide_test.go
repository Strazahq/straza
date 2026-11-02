package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// apiOfflineDoc is a role-scoped PolicySet with one deny rule over a
// default-allow floor: git status reads out as the default allow and rm -rf
// as the rule deny, so a deny can be told apart as the gate's or the rule's.
const apiOfflineDoc = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: offline-gate-test}
spec:
  match: {roles: [dev]}
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: {denyPatterns: ["rm -rf *"]}
      effect: deny
      reason: "Straza: rule-level deny"
`

// shellReq is a governed shell call of the claude-code window w1.
func shellReq(command string) localapi.DecideRequest {
	return localapi.DecideRequest{
		Harness: localapi.Harness{Name: "claude-code", Version: "2.1.0", Session: "w1"},
		Event:   policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: command},
	}
}

// decideVia sends req to POST decide as uid 1000 and returns the answer.
func decideVia(t *testing.T, a *api, req localapi.DecideRequest) localapi.DecideAnswer {
	t.Helper()
	w := serveAs(a, "1000", http.MethodPost, localapi.RouteDecide, req)
	var ans localapi.DecideAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil || w.Code != http.StatusOK {
		t.Fatalf("decide = %d %q", w.Code, w.Body.String())
	}
	return ans
}

// gateDouble is a double whose session expires at expires with renewal r,
// deciding by apiOfflineDoc under the offline grace maxAge.
func gateDouble(t *testing.T, maxAge int64, expires time.Duration, r Renewal) *apiDouble {
	s := liveSession()
	s.ExpiresAt, s.Renewal = time.Now().Add(expires), r
	return &apiDouble{session: s, engine: apiEngine(t, apiOfflineDoc, maxAge), maxAge: maxAge}
}

// TestDecideOfflineGate pins the failure matrix of D11 on the service: a
// session decides from its snapshot while its token is unexpired, and past
// expiry only within the offline grace (0 on enterprise, 900 seconds on
// standalone). Beyond expiry plus grace it denies, and the boundary falls
// on the allow side.
func TestDecideOfflineGate(t *testing.T) {
	rows := []struct {
		name    string
		maxAge  int64
		expires time.Duration
		command string
		want    string // offline, rule or allow
	}{
		{"enterprise: token expired 1s ago, offline => deny", 0, -time.Second, "git status", "offline"},
		{"enterprise: token valid => engine allow", 0, 5 * time.Minute, "git status", "allow"},
		{"enterprise: token valid => engine RULE deny", 0, 5 * time.Minute, "rm -rf /", "rule"},
		{"standalone: token expired 60s ago within 900s grace => allow", 900, -60 * time.Second, "git status", "allow"},
		{"standalone: token expired 20min ago, grace exhausted => deny", 900, -20 * time.Minute, "git status", "offline"},
		{"boundary: 1s inside grace edge => allow", 900, -899 * time.Second, "git status", "allow"},
		{"boundary: 1s past grace edge => deny", 900, -901 * time.Second, "git status", "offline"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			a, _ := testAPI(gateDouble(t, row.maxAge, row.expires, Renewal{}), false)
			ans := decideVia(t, a, shellReq(row.command))
			d := ans.Decision
			offline := d.Effect == policy.EffectDeny && strings.Contains(d.Reason, "expired") &&
				strings.Contains(d.Reason, "grace") && strings.Contains(d.Reason, "doctor")
			switch row.want {
			case "offline":
				if !offline || strings.Contains(d.Reason, "rule-level deny") || !ans.Journal.FailClosed {
					t.Fatalf("want the offline-grace deny, got %+v", ans)
				}
			case "rule":
				if d.Effect != policy.EffectDeny || offline || d.RuleID != "no-rm" || ans.Journal.FailClosed {
					t.Fatalf("want the rule deny, got %+v", ans)
				}
			default:
				if d.Effect != policy.EffectAllow {
					t.Fatalf("want allow, got %+v", d)
				}
			}
		})
	}
}

// TestDecideRenewalWording pins what the offline gate says and when, by
// what the session's last renewal saw. A renewal the server refused ends
// the grace at once and quotes the server. A transport failure, a 401
// whose re-acquire could not complete and an answer that judged nothing
// keep the grace. Only a transport failure, or no renewal, may claim the
// server is unreachable.
func TestDecideRenewalWording(t *testing.T) {
	const disabled = "user is disabled. Contact your administrator"
	const bare403 = "the server answered HTTP 403 and gave no reason. Ask your administrator whether this user or device is still enabled, " +
		"and check whether a proxy in front of strazad replaces its answers."
	rows := []struct {
		name    string
		maxAge  int64
		expires time.Duration
		renewal Renewal
		want    []string // phrases of the deny, nil for an allow
		absent  []string
	}{
		{"401 refresh and no identity to re-acquire", 900, -10 * time.Hour,
			Renewal{RenewRejected, "no enrolled identity: open u/1000/key.pem: no such file or directory"},
			[]string{"expired", "renewal failed", "Restart the session"}, []string{"unreachable"}},
		{"the re-acquire is refused because the device was revoked", 900, -10 * time.Hour,
			Renewal{RenewTerminal, "this device or user has been revoked. Contact your administrator"},
			[]string{"refused", "revoked", "administrator"}, []string{"unreachable", "restart the session"}},
		{"the re-acquire is refused and names straza enroll", 900, -10 * time.Hour,
			Renewal{RenewTerminal, "device credential rejected. Run `straza enroll` again"},
			[]string{"refused", "straza enroll"}, []string{"unreachable"}},
		{"the refresh is refused because the user is disabled", 900, -10 * time.Hour,
			Renewal{RenewTerminal, disabled}, []string{"refused", "disabled", "administrator"}, []string{"unreachable"}},
		{"the refresh landed", 0, 5 * time.Minute, Renewal{}, nil, nil},
		{"strazad cannot be reached", 900, -10 * time.Hour,
			Renewal{RenewTransport, "dial tcp 127.0.0.1:1: connect: connection refused"},
			[]string{"expired", "grace", "unreachable", "doctor"}, nil},
		{"a refused re-acquire inside the grace", 900, -60 * time.Second,
			Renewal{RenewRejected, "session token rejected"}, nil, nil},
		{"standalone, the renewal is refused", 900, -time.Minute,
			Renewal{RenewTerminal, disabled}, []string{"Straza: session renewal was refused: " + disabled}, nil},
		{"standalone, strazad cannot be reached", 900, -time.Minute, Renewal{RenewTransport, "connection refused"}, nil, nil},
		{"standalone, the headless agent's issuer refuses it", 900, -time.Minute,
			Renewal{RenewTerminal, "headless login refused (invalid_client): client authentication failed."},
			[]string{"Straza: session renewal was refused: headless login refused (invalid_client): client authentication failed."}, nil},
		{"standalone, a proxy answers with a 429 and a reason", 900, -time.Minute,
			Renewal{RenewUnjudged, "the server answered HTTP 429: rate limited. Retry shortly."}, nil, nil},
		{"standalone, a 403 with no reason", 900, -time.Minute,
			Renewal{RenewTerminal, bare403}, []string{"Straza: session renewal was refused: the server answered HTTP 403 and gave no reason."}, nil},
		{"enterprise, a proxy answers with a 429 and no reason", 0, -time.Minute,
			Renewal{RenewUnjudged, "the server answered HTTP 429 and gave no reason."},
			[]string{"the server answered HTTP 429 and gave no reason. The next tool call tries the renewal again."}, nil},
		{"enterprise, the renewal is refused", 0, -time.Minute,
			Renewal{RenewTerminal, disabled}, []string{"Straza: session renewal was refused: " + disabled}, nil},
		{"enterprise, strazad cannot be reached", 0, -time.Minute,
			Renewal{RenewTransport, "connection refused"}, []string{"the offline grace period (0s) is exhausted"}, nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			a, _ := testAPI(gateDouble(t, row.maxAge, row.expires, row.renewal), false)
			d := decideVia(t, a, shellReq("echo hi")).Decision
			if row.want == nil {
				if d.Effect != policy.EffectAllow {
					t.Fatalf("want the cached policy's allow, got %+v", d)
				}
				return
			}
			if d.Effect != policy.EffectDeny {
				t.Fatalf("want a deny, got %+v", d)
			}
			for _, p := range row.want {
				if !strings.Contains(d.Reason, p) {
					t.Errorf("reason %q lacks %q", d.Reason, p)
				}
			}
			for _, p := range row.absent {
				if strings.Contains(d.Reason, p) {
					t.Errorf("reason %q must not say %q", d.Reason, p)
				}
			}
		})
	}
}

// TestDecidePolicyFailureAfterRenewal pins the deny when the session's
// snapshot cannot be had: a session past its time whose renewal failed
// denies for the renewal, with the server's own sentence when it refused,
// and otherwise the credential half's sentence stands. None of it records.
func TestDecidePolicyFailureAfterRenewal(t *testing.T) {
	const missing = "this session is live, but the policy snapshot is missing on this machine, so tool calls are denied until straza has it."
	failed := "). Restart the session, or run `straza doctor`."
	rows := []struct {
		name    string
		expires time.Duration
		renewal Renewal
		want    string
		parts   []string
	}{
		{"the server refuses the renewal because the user is disabled", -10 * time.Minute,
			Renewal{RenewTerminal, "user is disabled. Contact your administrator"},
			"Straza: session renewal was refused: user is disabled. Contact your administrator", nil},
		{"the token is refused and no identity can check in", -10 * time.Minute,
			Renewal{RenewRejected, "no enrolled identity: open key.pem: no such file or directory"}, "",
			[]string{"session token expired ", " ago and automatic renewal failed (no enrolled identity: ", failed}},
		{"strazad answers the renewal with a 503", -10 * time.Minute,
			Renewal{RenewTransport, "/v1/checkin: store unavailable"}, "",
			[]string{"session token expired ", " ago and automatic renewal failed (", "store unavailable", failed}},
		{"a proxy answers the renewal with a 429 and no reason", -10 * time.Minute,
			Renewal{RenewUnjudged, "the server answered HTTP 429 and gave no reason."}, "",
			[]string{"session token expired ", " ago, and the renewal got an answer that does not say whether this session is still accepted: " +
				"the server answered HTTP 429 and gave no reason. The next tool call tries the renewal again."}},
		{"a renewal that landed on a time that has passed", -10 * time.Minute, Renewal{}, "Straza: " + missing, nil},
		{"a live session", time.Hour, Renewal{}, "Straza: " + missing, nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 900, row.expires, row.renewal)
			f.policyErr = errors.New(missing)
			a, _ := testAPI(f, false)
			ans := decideVia(t, a, shellReq("b-cmd /x"))
			if ans.Decision.Effect != policy.EffectDeny || !ans.Journal.FailClosed || len(f.recorded()) != 0 {
				t.Fatalf("answer = %+v with %d records, want a fail-closed deny and no record", ans, len(f.recorded()))
			}
			if row.want != "" && ans.Decision.Reason != row.want {
				t.Errorf("reason = %q, want %q", ans.Decision.Reason, row.want)
			}
			for _, p := range row.parts {
				if !strings.Contains(ans.Decision.Reason, p) {
					t.Errorf("reason %q lacks %q", ans.Decision.Reason, p)
				}
			}
		})
	}
}

// TestDecideRefusedPolicyDeadline pins that a session whose newer policy
// the server refused to send denies every governed call with the refusal's
// reason once the deadline plus the offline grace has passed, however
// fresh its token, and decides by the held policy before that.
func TestDecideRefusedPolicyDeadline(t *testing.T) {
	const phrase = "this session's policy is out of date: the server holds a newer policy that straza could not fetch"
	const why = "The server refused it: the snapshot is not served to this session."
	for _, row := range []struct {
		name  string
		grace time.Duration
	}{{"enterprise, no offline grace", 0}, {"standalone, 900 s offline grace", 900 * time.Second}} {
		t.Run(row.name, func(t *testing.T) {
			for _, step := range []struct {
				denyIn time.Duration
				deny   bool
			}{{time.Minute, false}, {-time.Second, true}, {-72 * time.Hour, true}} {
				f := gateDouble(t, int64(row.grace/time.Second), time.Hour, Renewal{})
				f.session.PolicyRefused = why
				f.session.PolicyDeadline = time.Now().Add(step.denyIn).Add(-row.grace)
				a, _ := testAPI(f, false)
				for _, command := range []string{"b-cmd /x", "rm -rf /x", "git status"} {
					d := decideVia(t, a, shellReq(command)).Decision
					if !step.deny {
						if command == "git status" && d.Effect != policy.EffectAllow {
							t.Errorf("%v before the deny moment, git status = %+v, want the held policy's allow", step.denyIn, d)
						}
						continue
					}
					if d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, phrase) || !strings.Contains(d.Reason, why) {
						t.Errorf("%v past the deny moment, %q = %+v, want the refused-policy deny with the reason", -step.denyIn, command, d)
					}
				}
			}
		})
	}
}

// TestDecideGatewayDeferral pins the whole-decision deferral: a call the
// harness routes through the Straza gateway allows and observes with an
// honest reason and no server traffic, even when the engine would deny,
// while the same call outside the gateway keeps full enforcement. The
// offline gate stays in front of the deferral.
func TestDecideGatewayDeferral(t *testing.T) {
	doc := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: gateway-defer-test}
spec:
  match: {roles: [dev]}
  rules:
    - id: no-mcp
      tools: [mcp.call]
      effect: deny
      reason: "Straza: rule-level deny"
`
	mangled := localapi.DecideRequest{
		Harness:        localapi.Harness{Name: "codex", Version: "0.146.0"},
		Event:          policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "demo_tools", ToolName: "echo"},
		GatewayProxied: true,
	}
	for _, row := range []struct {
		name    string
		expires time.Duration
		proxied bool
		check   func(*testing.T, localapi.DecideAnswer, *apiDouble)
	}{
		{"gateway-proxied defers even a rule deny", time.Hour, true, func(t *testing.T, ans localapi.DecideAnswer, f *apiDouble) {
			if ans.Decision.Effect != policy.EffectAllow || !strings.Contains(ans.Decision.Reason, "gateway") || ans.Journal.Escalation != "gateway" {
				t.Errorf("decision = %+v, want the deferred allow", ans)
			}
			if len(f.decideCalls) != 0 {
				t.Errorf("the deferral asked strazad %d times", len(f.decideCalls))
			}
			if recs := f.recorded(); len(recs) != 1 || recs[0].data["reason"] != ans.Decision.Reason {
				t.Errorf("records = %+v, want the deferral recorded once", recs)
			}
		}},
		{"the same event without the gateway flag keeps full enforcement", time.Hour, false, func(t *testing.T, ans localapi.DecideAnswer, _ *apiDouble) {
			if ans.Decision.Effect != policy.EffectDeny || ans.Decision.RuleID != "no-mcp" {
				t.Errorf("direct MCP decision = %+v, want the rule deny", ans.Decision)
			}
		}},
		{"the offline fail-closed gate still precedes the deferral", -time.Hour, true, func(t *testing.T, ans localapi.DecideAnswer, _ *apiDouble) {
			if ans.Decision.Effect != policy.EffectDeny || !strings.Contains(ans.Decision.Reason, "expired") {
				t.Errorf("expired gateway-proxied decision = %+v, want the offline deny", ans.Decision)
			}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 0, row.expires, Renewal{})
			f.engine = apiEngine(t, doc, 0)
			a, _ := testAPI(f, false)
			req := mangled
			req.GatewayProxied = row.proxied
			row.check(t, decideVia(t, a, req), f)
		})
	}
}

// TestDecideUsesSessionIdentity pins that the decision reads the roles and
// the identity type of the session as the credential half holds it now, so
// a role an IGA system assigned or removed is enforced from the refresh
// that carried it, and a type-scoped set decides the first call.
func TestDecideUsesSessionIdentity(t *testing.T) {
	roles := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: contractor-lockdown}
spec:
  match:
    roles: [contractor]
  rules:
    - {id: r-contractor, tools: [shell.exec], command: {denyPatterns: ["secret-cmd *"]}, effect: deny, reason: "contractors may not run secret-cmd"}
`
	agents := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: agent-lockdown}
spec:
  match:
    identity:
      userType: [agent]
  rules:
    - {id: r-agent, tools: [shell.exec], command: {denyPatterns: ["secret-cmd *"]}, effect: deny, reason: "agents may not run secret-cmd"}
`
	for _, row := range []struct {
		name     string
		doc      string
		roles    []string
		userType string
		want     string
	}{
		{"role assigned in the IGA system is enforced", roles, []string{"dev", "contractor"}, "", policy.EffectDeny},
		{"role removed in the IGA system stops being enforced", roles, []string{"dev"}, "", policy.EffectAllow},
		{"a user stripped of every role", roles, nil, "", policy.EffectAllow},
		{"an agent session meets the agent-scoped set", agents, []string{"dev"}, "agent", policy.EffectDeny},
		{"a person's session does not", agents, []string{"dev"}, "human", policy.EffectAllow},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := gateDouble(t, 900, time.Hour, Renewal{})
			f.engine = apiEngine(t, row.doc, 900)
			f.session.Roles, f.session.UserType = row.roles, row.userType
			a, _ := testAPI(f, false)
			if d := decideVia(t, a, shellReq("secret-cmd --dump")).Decision; d.Effect != row.want {
				t.Errorf("decision = %+v, want %s", d, row.want)
			}
		})
	}
}

// TestDecideWorstCaseUnderClientTimeout pins that one decide answers
// before the thin client gives up, with every step spending its whole
// budget: the renewal, the snapshot, the classifier, and an online lane
// whose decide, 401 bounce and retry never answer. The answer is a
// fail-closed deny, never the client's unreachable sentence.
func TestDecideWorstCaseUnderClientTimeout(t *testing.T) {
	t.Parallel()
	doc := `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: worst-case}
spec:
  match: {roles: [dev]}
  rules:
    - id: classified
      tools: [shell.exec]
      command: {allowPatterns: ["deploy *"]}
      effect: allow
      mode: classify
    - id: gated
      tools: [shell.exec]
      command: {allowPatterns: ["deploy *"]}
      effect: allow
      mode: serverCheck
`
	f := gateDouble(t, 900, 30*time.Second, Renewal{})
	f.engine = apiEngine(t, doc, 900)
	f.sessionWait, f.policyWait = time.Minute, time.Minute
	f.decide = func(ctx context.Context, _ policy.Event) (DecideOutcome, error) {
		<-ctx.Done()
		return DecideOutcome{}, ctx.Err()
	}
	a, _ := testAPI(f, false)
	slow := &apiClassifier{verdict: policy.ClassifyVerdict{Allowed: true}, delay: classifyBudget - 50*time.Millisecond}
	a.classifier = slow
	start := time.Now()
	ans := decideVia(t, a, shellReq("deploy prod"))
	elapsed := time.Since(start)
	if elapsed >= localapi.DecideTimeout {
		t.Fatalf("decide took %v, at or above the client's %v", elapsed, localapi.DecideTimeout)
	}
	if elapsed < renewBudget+policyBudget+onlineBudget-time.Second {
		t.Errorf("decide took %v, so a step did not spend its budget and the row proves nothing", elapsed)
	}
	if ans.Decision.Effect != policy.EffectDeny || !ans.Journal.FailClosed || ans.Journal.Escalation != "server" || slow.calls.Load() != 1 {
		t.Errorf("answer = %+v, want the server lane's fail-closed deny after one classify", ans)
	}
}
