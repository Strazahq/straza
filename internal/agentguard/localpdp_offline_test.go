package agentguard

import (
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// offlinePolicyDoc is a role-scoped PolicySet with one deny rule over a
// default-allow floor: `git status` reads out as the default allow, `rm -rf`
// as the rule deny. It lets the offline gate be tested against BOTH engine
// verdicts so a deny can be attributed to the gate vs. the rule.
const offlinePolicyDoc = `
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

// offlineEngine compiles+signs+opens offlinePolicyDoc the way the client does.
func offlineEngine(t *testing.T) *policy.Engine {
	t.Helper()
	priv, keys := testSnapshotKey(t)
	signed, id := testSignedPolicy(t, priv, offlinePolicyDoc)
	lookup, err := keyLookup(keys)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, err := policy.OpenSnapshot(signed, id, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// offlinePDP builds a LocalPDP whose only variables are the profile's offline
// grace bound (maxAge, the value the snapshot carries to the client) and the
// session's token expiry, the two inputs the fail-closed offline gate reads.
func offlinePDP(t *testing.T, maxAge int64, expiresAt time.Time) *LocalPDP {
	t.Helper()
	return &LocalPDP{
		subject: policy.Subject{User: "kim", Roles: []string{"dev"}},
		engine:  offlineEngine(t),
		session: Session{SessionToken: "tok-1", ExpiresAt: expiresAt},
		maxAge:  maxAge,
	}
}

// isOfflineDeny reports whether a decision is the fail-closed offline-grace
// deny (not a rule deny, not an allow): it names the exhausted grace and the
// `straza doctor` remedy.
func isOfflineDeny(d policy.Decision) bool {
	return d.Effect == policy.EffectDeny &&
		strings.Contains(d.Reason, "expired") &&
		strings.Contains(d.Reason, "grace") &&
		strings.Contains(d.Reason, "doctor")
}

// TestLocalPDPOfflineGate is the executable form of the failure matrix
// rows for the client decision path, which fails closed. A client may
// decide from the cached snapshot while the session token is unexpired; past
// token expiry it may keep deciding only within the profile's offline grace
// TTL (snapshot maxAge, seconds). Beyond ExpiresAt+grace it DENIES: Straza
// can no longer prove the policy is current and must fail closed.
//
// Enterprise grace 0 ⇒ deny the instant the token expires (tolerating only the
// ≤300 s token lifetime). Standalone grace 900 s ⇒ deny 15 min past expiry.
func TestLocalPDPOfflineGate(t *testing.T) {
	// git status is the default-allow floor; a deny on it can ONLY be the gate.
	gitStatus := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status"}
	// rm -rf is the rule deny; used to prove a VALID token yields the engine's
	// verdict, not the offline deny.
	rmRF := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "rm -rf /"}

	cases := []struct {
		name    string
		maxAge  int64
		expires time.Duration // relative to now; negative = already expired
		event   policy.Event
		// exactly one of the three expectations holds:
		wantOfflineDeny bool
		wantRuleDeny    bool
		wantAllow       bool
	}{
		{
			name: "enterprise: token expired 1s ago, offline => deny",
			// Failure matrix: "Token expired, refresh fails → deny all" (grace 0).
			maxAge: 0, expires: -1 * time.Second, event: gitStatus, wantOfflineDeny: true,
		},
		{
			name:   "enterprise: token valid => engine allow, not offline deny",
			maxAge: 0, expires: 5 * time.Minute, event: gitStatus, wantAllow: true,
		},
		{
			name:   "enterprise: token valid => engine RULE deny, not offline deny",
			maxAge: 0, expires: 5 * time.Minute, event: rmRF, wantRuleDeny: true,
		},
		{
			name: "standalone: token expired 60s ago within 900s grace => allow",
			// Failure matrix: "Platform unreachable, valid snapshot within grace → allow (standalone)".
			maxAge: 900, expires: -60 * time.Second, event: gitStatus, wantAllow: true,
		},
		{
			name:   "standalone: token expired 20min ago, grace exhausted => deny",
			maxAge: 900, expires: -20 * time.Minute, event: gitStatus, wantOfflineDeny: true,
		},
		{
			// Boundary: the gate is `now.After(ExpiresAt+grace)`, a strict >, so
			// equality falls on the ALLOW side; the deny begins strictly AFTER
			// the deadline. 1 s inside the edge still allows...
			name:   "boundary: 1s inside grace edge => allow",
			maxAge: 900, expires: -899 * time.Second, event: gitStatus, wantAllow: true,
		},
		{
			// ...and 1 s past it denies.
			name:   "boundary: 1s past grace edge => deny",
			maxAge: 900, expires: -901 * time.Second, event: gitStatus, wantOfflineDeny: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := offlinePDP(t, tc.maxAge, time.Now().Add(tc.expires))
			d := p.Decide(Normalized{Event: tc.event})
			switch {
			case tc.wantOfflineDeny:
				if !isOfflineDeny(d) {
					t.Fatalf("want offline-grace deny, got %+v", d)
				}
				if strings.Contains(d.Reason, "rule-level deny") {
					t.Errorf("offline deny leaked the rule reason: %q", d.Reason)
				}
			case tc.wantRuleDeny:
				if d.Effect != policy.EffectDeny {
					t.Fatalf("want rule deny, got %+v", d)
				}
				if isOfflineDeny(d) {
					t.Fatalf("valid token produced the OFFLINE deny, not the rule verdict: %+v", d)
				}
				if !strings.Contains(d.Reason, "rule-level deny") {
					t.Errorf("rule deny reason = %q, want the engine's", d.Reason)
				}
			case tc.wantAllow:
				if d.Effect != policy.EffectAllow {
					t.Fatalf("want allow (within grace / valid token), got %+v", d)
				}
			}
		})
	}
}
