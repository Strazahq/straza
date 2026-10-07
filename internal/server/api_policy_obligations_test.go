package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

const obligationsGateYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: obligations-gate }
spec:
  priority: 90
  match: { roles: [dev] }
  rules:
    - id: quiet
      tools: [shell.exec]
      command: { allowPatterns: ["echo *"] }
      effect: allow
    - id: watch-secrets
      tools: [file.read]
      paths: { allow: ["**/secrets/**"] }
      effect: allow
      OBLIGATIONS
`

func withObligations(list string) string {
	return strings.Replace(obligationsGateYAML, "OBLIGATIONS", list, 1)
}

// TestObligationsGate pins the revision 18 gate at validate AND activate: a
// rule that carries the retired obligations list is refused with one line
// per rule that names the rule, says the list never ran, and names the two
// replacements. Drafts stay permissive (git-first flow), activation is the
// gate, and a set without the list is untouched.
func TestObligationsGate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	const refusal = "rules[1] (watch-secrets): obligations are not supported and never ran. Remove the obligations list. To keep recorded content out of the transcript, set capture.mode: redact on the set. To tell people about a call waiting on a hold, set approve.notify on the rule."
	cases := []struct {
		list     string
		wantCode int
		wantText string
	}{
		{"", http.StatusOK, ""},
		{"obligations: [notify]", http.StatusBadRequest, refusal},
		{"obligations: [redact, notify]", http.StatusBadRequest, refusal},
	}
	for _, tc := range cases {
		var res map[string]any
		code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, withObligations(tc.list), &res)
		if code != tc.wantCode {
			t.Errorf("validate %q = %d (%v), want %d", tc.list, code, res, tc.wantCode)
			continue
		}
		if tc.wantText != "" {
			if msg, _ := res["error"].(string); msg != tc.wantText {
				t.Errorf("validate %q error = %q, want %q", tc.list, msg, tc.wantText)
			}
		}
	}
	// Every offending rule in one answer, in rule order.
	both := strings.Replace(withObligations("obligations: [redact]"),
		"effect: allow\n    - id: watch-secrets", "effect: allow\n      obligations: [notify]\n    - id: watch-secrets", 1)
	var res map[string]any
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, both, &res); code != http.StatusBadRequest {
		t.Fatalf("two offending rules = %d (%v), want 400", code, res)
	}
	if msg, _ := res["error"].(string); !strings.Contains(msg, "rules[0] (quiet): obligations") || !strings.Contains(msg, "rules[1] (watch-secrets): obligations") {
		t.Errorf("two-rule message = %q, want both rules named", msg)
	}

	// Apply stays permissive (draft), activation is the gate.
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(withObligations("obligations: [notify]"))); code != http.StatusCreated {
		t.Fatalf("apply draft with obligations = %d %s, want 201 (drafts are not gated)", code, b)
	}
	var act map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/policies/obligations-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusBadRequest {
		t.Fatalf("activate with obligations = %d (%v), want 400", code, act)
	}
	if msg, _ := act["error"].(string); msg != refusal {
		t.Errorf("activate error = %q, want %q", msg, refusal)
	}
	if ps, err := app.store.Policies().GetByName(ctx, "obligations-gate"); err != nil || ps.Status != "draft" {
		t.Fatalf("set after refused activation = %+v, %v; want still draft", ps, err)
	}
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(withObligations(""))); code != http.StatusOK {
		t.Fatalf("re-apply without obligations = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/obligations-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusOK {
		t.Fatalf("activate without obligations = %d (%v), want 200", code, act)
	}
}

// TestLegacyObligationsWarnOnce pins the boot half: a set stored active with
// an obligations list (as a pre-revision-18 upgrade leaves it) keeps
// governing, still compiles, and the boot audit names it exactly once per
// set with the same words the gate uses. Drafts are not named.
func TestLegacyObligationsWarnOnce(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { a.log = log }})
	ctx := context.Background()

	for _, seed := range []struct{ name, status string }{
		{"legacy-obl-a", "active"}, {"legacy-obl-b", "active"}, {"legacy-obl-draft", "draft"},
	} {
		if _, err := app.store.Policies().Create(ctx, store.PolicySet{
			Name: seed.name, Priority: 80, Status: seed.status,
			YAMLSource: strings.Replace(withObligations("obligations: [notify, redact]"), "name: obligations-gate", "name: "+seed.name, 1),
		}); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := app.legacyObligations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 2 {
		t.Fatalf("legacyObligations = %v, want exactly the two active sets", legacy)
	}
	for _, name := range []string{"legacy-obl-a", "legacy-obl-b"} {
		if viol := legacy[name]; len(viol) != 1 || !strings.Contains(viol[0], "rules[1] (watch-secrets): obligations are not supported and never ran") {
			t.Errorf("legacyObligations[%s] = %v, want one violation naming watch-secrets", name, viol)
		}
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("recompile with the legacy sets still compiles: %v", err)
	}

	buf.Reset()
	app.warnLegacyObligations(ctx)
	var warns []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "carries obligations that never ran") {
			warns = append(warns, line)
		}
	}
	if len(warns) != 2 {
		t.Fatalf("boot WARN lines = %d, want one per offending active set:\n%s", len(warns), strings.Join(warns, "\n"))
	}
	for _, name := range []string{"legacy-obl-a", "legacy-obl-b"} {
		named := 0
		for _, line := range warns {
			if strings.Contains(line, "set="+name) {
				named++
			}
		}
		if named != 1 {
			t.Errorf("set %s named on %d WARN lines, want exactly one", name, named)
		}
	}
}
