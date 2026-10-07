package server

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// summaryProbe carries every posture the engine knows plus a WRAPPED flow
// sequence (the exact shape the console's client-side mini-parser cannot
// decompose): the server summary must be complete
// precisely where the client decomposition fails, so the list can render
// truth from the server instead of "?".
const summaryProbe = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: summary-probe
spec:
  priority: 140
  match:
    roles: [dev, ops]
    users: [kim]
  capture:
    conversations: true
    mode: verbatim
  rules:
    - id: block-rm
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *",
          "mkfs*"]
      effect: deny
      reason: "Straza: no"
    - id: hold-deploy
      tools: [shell.exec]
      command: { allowPatterns: ["./deploy*"] }
      effect: allow
      mode: approve
      reason: "Straza: held"
    - id: ticket-deploy
      tools: [shell.exec]
      command: { allowPatterns: ["./release*"] }
      effect: allow
      mode: approve
      approve: { class: ticket }
      reason: "Straza: ticket"
    - id: confirm-py
      tools: [shell.exec]
      command: { allowPatterns: ["python3 *"] }
      effect: allow
      mode: confirm
      reason: "Straza: confirm"
    - id: checked
      tools: [net.fetch]
      effect: allow
      mode: serverCheck
      reason: "Straza: checked"
    - id: classified
      tools: [file.write]
      effect: allow
      mode: classify
      reason: "Straza: classified"
    - id: plain-allow
      tools: [file.read]
      effect: allow
      reason: "Straza: ok"
`

// TestPolicyListCarriesServerSummary pins the summary contract: the policies list
// payload carries a server-computed summary per set (same parse the server
// already trusts), and a stored set whose YAML no longer parses lists with
// the summary honestly ABSENT rather than invented.
func TestPolicyListCarriesServerSummary(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(summaryProbe)); code != http.StatusCreated {
		t.Fatalf("apply summary-probe = %d (%s)", code, body)
	}
	// A stored set that no longer parses (planted directly; apply would refuse
	// it) must list without a summary: absence is the honest signal.
	if _, err := app.store.Policies().Create(context.Background(), store.PolicySet{
		Name: "rotten-set", Priority: 10, YAMLSource: "spec: [broken", Status: "draft",
	}); err != nil {
		t.Fatal(err)
	}

	code, body, _ := adminBytes(t, "GET", base+"/v1/admin/policies", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	var env struct {
		Items []struct {
			Name    string `json:"name"`
			Summary *struct {
				Name       string         `json:"name"`
				Priority   int            `json:"priority"`
				Rules      int            `json:"rules"`
				Postures   map[string]int `json:"postures"`
				MatchRoles []string       `json:"matchRoles"`
				MatchOther bool           `json:"matchOther"`
				Capture    string         `json:"capture"`
			} `json:"summary"`
		} `json:"items"`
	}
	if err := jsonUnmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	byName := map[string]*struct {
		Name       string         `json:"name"`
		Priority   int            `json:"priority"`
		Rules      int            `json:"rules"`
		Postures   map[string]int `json:"postures"`
		MatchRoles []string       `json:"matchRoles"`
		MatchOther bool           `json:"matchOther"`
		Capture    string         `json:"capture"`
	}{}
	seen := map[string]bool{}
	for _, r := range env.Items {
		byName[r.Name] = r.Summary
		seen[r.Name] = true
	}

	s := byName["summary-probe"]
	if s == nil {
		t.Fatal("summary-probe row carries no server summary")
	}
	if s.Name != "summary-probe" || s.Priority != 140 || s.Rules != 7 {
		t.Errorf("summary head = %q/%d/%d, want summary-probe/140/7", s.Name, s.Priority, s.Rules)
	}
	if !reflect.DeepEqual(s.MatchRoles, []string{"dev", "ops"}) {
		t.Errorf("matchRoles = %v", s.MatchRoles)
	}
	if !s.MatchOther {
		t.Error("matchOther = false, want true (users selector present)")
	}
	if s.Capture != "verbatim" {
		t.Errorf("capture = %q, want verbatim", s.Capture)
	}
	wantPostures := map[string]int{
		"deny": 1, "hold": 1, "ticket": 1, "confirm": 1,
		"serverCheck": 1, "classify": 1, "allow": 1,
	}
	if !reflect.DeepEqual(s.Postures, wantPostures) {
		t.Errorf("postures = %v, want %v", s.Postures, wantPostures)
	}

	if !seen["rotten-set"] {
		t.Fatal("rotten-set missing from the list entirely")
	}
	if byName["rotten-set"] != nil {
		t.Error("unparseable set carries a summary; absence is the honest signal")
	}
}
