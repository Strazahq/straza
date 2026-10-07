package server

import (
	"net/http"
	"strings"
	"testing"
)

// yamlReq posts PolicySet YAML via the shared rawReq helper.
func yamlReq(t *testing.T, method, urlStr, bearer, body string, out any) int {
	t.Helper()
	return rawReq(t, method, urlStr, bearer, "application/yaml", []byte(body), out)
}

const guardrailsYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: sim-guardrails }
spec:
  priority: 100
  match: { roles: [dev] }
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Straza: blocked"
`

// TestPolicyValidate pins the builder's validate endpoint: the REAL parser
// answers (never a UI-side reimplementation): ok with a structural summary,
// or the parser's error verbatim.
func TestPolicyValidate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	var ok struct {
		OK         bool     `json:"ok"`
		Name       string   `json:"name"`
		Rules      int      `json:"rules"`
		Priority   int      `json:"priority"`
		MatchRoles []string `json:"matchRoles"`
	}
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, guardrailsYAML, &ok); code != http.StatusOK {
		t.Fatalf("validate = %d", code)
	}
	if !ok.OK || ok.Name != "sim-guardrails" || ok.Rules != 1 || ok.Priority != 100 ||
		len(ok.MatchRoles) != 1 || ok.MatchRoles[0] != "dev" {
		t.Errorf("summary = %+v", ok)
	}

	var bad struct {
		Error string `json:"error"`
	}
	noRules := strings.Replace(guardrailsYAML, "rules:", "norules:", 1)
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, noRules, &bad); code != http.StatusBadRequest {
		t.Fatalf("invalid policy = %d, want 400", code)
	}
	if bad.Error == "" {
		t.Error("400 without a parser message")
	}
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, "{not yaml", nil); code != http.StatusBadRequest {
		t.Errorf("garbage = %d, want 400", code)
	}
	// The Rego module compiles at validate, so a module that activation
	// would refuse is refused here first.
	fetch := guardrailsYAML + "  escape:\n    rego: |\n      package straza.ext\n\n" +
		"      deny contains msg if { msg := http.send({\"method\": \"get\", \"url\": \"http://a/\"}).raw_body }\n"
	const refusal = "policy: set sim-guardrails: its Rego module calls http.send on line 3, and Straza refuses that built-in " +
		"because a policy module must not reach the network, the file system or the process environment. Remove the call from the module"
	var refused struct {
		Error string `json:"error"`
	}
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, fetch, &refused); code != http.StatusBadRequest || refused.Error != refusal {
		t.Errorf("refused built-in = %d %q, want 400 %q", code, refused.Error, refusal)
	}

	// A VALID set with an unsatisfiable predicate answers ok PLUS warnings
	// (spec/policyset rev 10): require.deviceCert cannot be met until a
	// device-certificate factor ships, and the author hears it at
	// validate time, not after activation.
	certGate := strings.Replace(guardrailsYAML,
		"      effect: deny\n",
		"      effect: deny\n    - id: cert-gate\n      tools: [shell.exec]\n      effect: allow\n      require: { deviceCert: true }\n", 1)
	var warned struct {
		OK         bool     `json:"ok"`
		Warnings   []string `json:"warnings"`
		Advisories []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Rule     string `json:"rule"`
			Text     string `json:"text"`
		} `json:"advisories"`
	}
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, certGate, &warned); code != http.StatusOK {
		t.Fatalf("deviceCert validate = %d, want 200 (advisory, not error)", code)
	}
	if !warned.OK || len(warned.Warnings) != 1 ||
		!strings.Contains(warned.Warnings[0], `"cert-gate"`) ||
		!strings.Contains(warned.Warnings[0], "deviceCert") {
		t.Errorf("deviceCert validate = %+v, want ok with one advisory naming the rule", warned)
	}
	// The typed twin (0.84.0): same sentence, plus the code + severity +
	// rule the console keys tone and placement on.
	if len(warned.Advisories) != 1 {
		t.Fatalf("advisories = %+v, want exactly 1", warned.Advisories)
	}
	if a := warned.Advisories[0]; a.Code != "require-unsatisfiable" || a.Severity != "warn" ||
		a.Rule != "cert-gate" || a.Text != warned.Warnings[0] {
		t.Errorf("typed advisory = %+v, want require-unsatisfiable/warn/cert-gate with text matching warnings[0]", a)
	}
	if len(ok.MatchRoles) == 1 { // the plain set must stay warning-free
		var plain struct {
			Warnings []string `json:"warnings"`
		}
		if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, guardrailsYAML, &plain); code != http.StatusOK || len(plain.Warnings) != 0 {
			t.Errorf("plain set warnings = %v, want none", plain.Warnings)
		}
	}
}

type simResult struct {
	Active struct {
		Effect  string `json:"effect"`
		RuleID  string `json:"ruleId"`
		SetName string `json:"setName"`
		Reason  string `json:"reason"`
	} `json:"active"`
	Draft *struct {
		Effect string `json:"effect"`
		RuleID string `json:"ruleId"`
	} `json:"draft"`
	Subject struct {
		User  string   `json:"user"`
		Roles []string `json:"roles"`
	} `json:"subject"`
}

// TestPolicySimulate pins the simulation endpoint: an event + subject
// evaluated against the ACTIVE snapshot, and optionally a draft PolicySet
// overlaid in place of its same-named stored set: the "active says ALLOW,
// draft says DENY" delta the builder renders before activation.
func TestPolicySimulate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	// Activate the guardrails set for role dev.
	if code := yamlReq(t, http.MethodPut, base+"/v1/admin/policies", adminTok, guardrailsYAML, nil); code != http.StatusCreated {
		t.Fatalf("apply = %d", code)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/sim-guardrails/activate",
		adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	// Explicit roles subject: the deny rule matches.
	var res simResult
	req := map[string]any{
		"event":   map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x"},
		"subject": map[string]any{"roles": []string{"dev"}},
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", adminTok, req, &res); code != http.StatusOK {
		t.Fatalf("simulate = %d", code)
	}
	if res.Active.Effect != "deny" || res.Active.RuleID != "no-rm-rf" {
		t.Errorf("active = %+v", res.Active)
	}

	// Subject by username: roles resolve server-side (seedIdentity already
	// assigned kim the dev role).
	req["subject"] = map[string]any{"user": user.Username}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", adminTok, req, &res); code != http.StatusOK {
		t.Fatalf("simulate by user = %d", code)
	}
	if res.Active.Effect != "deny" {
		t.Errorf("by-user active = %+v (subject %+v)", res.Active, res.Subject)
	}
	hasDev := false
	for _, r := range res.Subject.Roles {
		if r == "dev" {
			hasDev = true
		}
	}
	if !hasDev {
		t.Errorf("resolved subject lacks dev: %+v", res.Subject)
	}

	// Draft overlay: the draft adds a deny the active policy lacks; active
	// allows, draft denies. That's the pre-activation delta. (The event must
	// be one no OTHER active set governs: the starter policy also denies
	// rm -rf, and simulate composes ALL active sets, as activation would.)
	draft := strings.Replace(guardrailsYAML,
		`command: { denyPatterns: ["rm -rf *"] }`,
		`command: { denyPatterns: ["shutdown *"] }`, 1)
	req["event"] = map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "shutdown now"}
	req["draft"] = draft
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", adminTok, req, &res); code != http.StatusOK {
		t.Fatalf("simulate draft = %d", code)
	}
	if res.Active.Effect != "allow" {
		t.Errorf("draft run: active = %+v, want allow (no active rule governs shutdown)", res.Active)
	}
	if res.Draft == nil || res.Draft.Effect != "deny" || res.Draft.RuleID != "no-rm-rf" {
		t.Errorf("draft decision = %+v, want deny by the draft's rule", res.Draft)
	}

	// Bad requests are loud.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", adminTok,
		map[string]any{"subject": map[string]any{"roles": []string{"dev"}}}, nil); code != http.StatusBadRequest {
		t.Errorf("missing event = %d, want 400", code)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", adminTok,
		map[string]any{"event": map[string]any{"kind": "tool.pre"}, "subject": map[string]any{"roles": []string{"dev"}}, "draft": "{broken"}, nil); code != http.StatusBadRequest {
		t.Errorf("broken draft = %d, want 400", code)
	}
}

// TestPolicyValidateBindReserved pins the bind-reserved advisory at
// validate: a ticket rule that sets bind: predicate stays valid, and the
// answer carries the reserved-value sentence with its code and rule.
func TestPolicyValidateBindReserved(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	ticket := guardrailsYAML + "    - id: deploy-ticket\n      tools: [shell.exec]\n      command: { allowPatterns: [\"deploy-prod*\"] }\n" +
		"      effect: allow\n      mode: approve\n      approve: { class: ticket, roles: [straza-admin], bind: predicate }\n"
	var got struct {
		OK         bool     `json:"ok"`
		Warnings   []string `json:"warnings"`
		Advisories []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Rule     string `json:"rule"`
			Text     string `json:"text"`
		} `json:"advisories"`
	}
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", adminTok, ticket, &got); code != http.StatusOK {
		t.Fatalf("validate = %d, want 200 (an advisory, not an error)", code)
	}
	if !got.OK || len(got.Warnings) != 1 || len(got.Advisories) != 1 {
		t.Fatalf("validate = %+v, want ok with exactly the bind-reserved advisory", got)
	}
	if a := got.Advisories[0]; a.Code != "bind-reserved" || a.Severity != "warn" || a.Rule != "deploy-ticket" ||
		a.Text != got.Warnings[0] || !strings.HasPrefix(a.Text, `rule "deploy-ticket": approve.bind: predicate is reserved`) {
		t.Errorf("advisory = %+v, want bind-reserved/warn/deploy-ticket with the reserved sentence as warnings[0]", a)
	}
}
