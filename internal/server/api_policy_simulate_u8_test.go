package server

import (
	"net/http"
	"testing"
)

// u8InterpreterSet is a PolicySet whose one rule denies a shell command run
// by any python interpreter; name and rule id vary so a draft can replace it.
func u8InterpreterSet(name, rule string) string {
	return `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: ` + name + `}
spec:
  match: {roles: [dev]}
  rules:
    - id: ` + rule + `
      tools: [shell.exec]
      interpreters: {deny: ["python*"]}
      effect: deny
`
}

// TestPolicySimulateTagsInterpreter pins that policy simulate tags the
// interpreter of a shell command before it evaluates, as the live decision
// point does, so an interpreters rule fires in the active answer and in the
// draft answer alike. A command with no interpreter keeps its verdict, and
// an event that already names its interpreter keeps that name.
func TestPolicySimulateTagsInterpreter(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	if code := yamlReq(t, http.MethodPut, base+"/v1/admin/policies", adminTok,
		u8InterpreterSet("u8-interp", "u8-deny-python"), nil); code != http.StatusCreated {
		t.Fatalf("apply = %d", code)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/u8-interp/activate",
		adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	type verdict struct {
		Effect  string `json:"effect"`
		RuleID  string `json:"ruleId"`
		Default bool   `json:"default"`
	}
	denied := func(rule string) verdict { return verdict{Effect: "deny", RuleID: rule} }
	allowed := verdict{Effect: "allow", Default: true}
	const python = "python3 -c 'print(1)'"
	cases := []struct {
		name        string
		command     string
		interpreter string
		draft       bool
		wantActive  verdict
		wantDraft   verdict
	}{
		{name: "a python command meets the active rule", command: python,
			wantActive: denied("u8-deny-python")},
		{name: "a python command meets the draft rule", command: python, draft: true,
			wantActive: denied("u8-deny-python"), wantDraft: denied("u8-draft-deny-python")},
		{name: "a command with no interpreter keeps its verdict", command: "git status", draft: true,
			wantActive: allowed, wantDraft: allowed},
		{name: "an interpreter the event names is kept", command: python, interpreter: "node", draft: true,
			wantActive: allowed, wantDraft: allowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": tc.command}
			if tc.interpreter != "" {
				event["interpreter"] = tc.interpreter
			}
			req := map[string]any{"event": event, "subject": map[string]any{"roles": []string{"dev"}}}
			if tc.draft {
				req["draft"] = u8InterpreterSet("u8-interp", "u8-draft-deny-python")
			}
			var res struct {
				Active verdict  `json:"active"`
				Draft  *verdict `json:"draft"`
			}
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", adminTok, req, &res); code != http.StatusOK {
				t.Fatalf("simulate = %d", code)
			}
			if res.Active != tc.wantActive {
				t.Errorf("active = %+v, want %+v", res.Active, tc.wantActive)
			}
			if (res.Draft != nil) != tc.draft {
				t.Fatalf("draft answer present = %v, want %v", res.Draft != nil, tc.draft)
			}
			if tc.draft && *res.Draft != tc.wantDraft {
				t.Errorf("draft = %+v, want %+v", *res.Draft, tc.wantDraft)
			}
		})
	}
}
