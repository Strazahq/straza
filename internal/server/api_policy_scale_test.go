package server

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// The list contract (openapi 0.78.0): the list is an envelope of
// summary-only rows with server-side filters applied before paging, plus a
// per-role facet over ALL matches so group views render whole-truth rollups
// while rows page. YAML leaves the list; the editor fetches one set by name.

type policyListRow struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Priority  int     `json:"priority"`
	Status    string  `json:"status"`
	UpdatedAt string  `json:"updated_at"`
	YAML      *string `json:"yaml"`
	Drift     *bool   `json:"drift"`
	Summary   *struct {
		Name        string                    `json:"name"`
		Description string                    `json:"description"`
		Priority    int                       `json:"priority"`
		Rules       int                       `json:"rules"`
		Postures    map[string]int            `json:"postures"`
		MatchRoles  []string                  `json:"matchRoles"`
		MatchOther  bool                      `json:"matchOther"`
		Capture     string                    `json:"capture"`
		Lanes       map[string]map[string]int `json:"lanes"`
	} `json:"summary"`
}

type policyListEnvelope struct {
	Items  []policyListRow `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
	Roles  []struct {
		Role     string         `json:"role"`
		Sets     int            `json:"sets"`
		Postures map[string]int `json:"postures"`
	} `json:"roles"`
}

func listPolicies(t *testing.T, base, tok, query string) policyListEnvelope {
	t.Helper()
	u := base + "/v1/admin/policies"
	if query != "" {
		u += "?" + query
	}
	code, body, _ := adminBytes(t, "GET", u, tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list %q = %d (%s)", query, code, body)
	}
	var env policyListEnvelope
	if err := jsonUnmarshal(body, &env); err != nil {
		t.Fatalf("list %q does not decode as the envelope: %v (%s)", query, err, body)
	}
	return env
}

func listNames(env policyListEnvelope) []string {
	out := make([]string, len(env.Items))
	for i, r := range env.Items {
		out[i] = r.Name
	}
	return out
}

func applyPolicyYAML(t *testing.T, base, tok, doc string, wantCode int) {
	t.Helper()
	code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(doc))
	if code != wantCode {
		t.Fatalf("apply = %d, want %d (%s)", code, wantCode, body)
	}
}

const scaleDevBlock = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: scale-dev-block
  description: Shell blocks for dev
spec:
  priority: 300
  match: { roles: [dev] }
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Straza: no"
`

const scaleDevAllow = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: scale-dev-allow
  description: MCP allowances for dev
spec:
  priority: 250
  match: { roles: [dev] }
  capture: { conversations: true, mode: verbatim }
  rules:
    - id: mcp-ok
      tools: [mcp.call]
      effect: allow
      reason: "Straza: ok"
`

const scaleMulti = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: scale-multi
  description: Cross-team hold
spec:
  priority: 200
  match: { roles: [dev, ops] }
  rules:
    - id: hold-all
      effect: allow
      mode: approve
      reason: "Straza: held"
`

const scaleEveryone = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: scale-everyone
  description: Org floor
spec:
  priority: 150
  rules:
    - id: no-writes
      tools: [file.write]
      effect: deny
      reason: "Straza: no"
`

const scaleOutside = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: scale-outside
  description: Dated exception for kim
spec:
  priority: 120
  match: { users: [kim] }
  rules:
    - id: fetch-ok
      tools: [net.fetch]
      effect: allow
      reason: "Straza: ok"
`

const scaleEvents = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: scale-events
  description: Session-open note
spec:
  priority: 110
  match: { roles: [ops] }
  rules:
    - id: on-open
      events: [session.start]
      effect: allow
      reason: "Straza: ok"
`

// TestPolicyListEnvelope pins the response shape: envelope fields, no yaml
// on any row, priority-desc order, the description in the summary, the
// unparseable row listed summary-less and excluded from the facet, and the
// facet order "*" first, role names sorted, "~" last.
func TestPolicyListEnvelope(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	for _, doc := range []string{scaleDevBlock, scaleEveryone, scaleOutside} {
		applyPolicyYAML(t, base, tok, doc, http.StatusCreated)
	}
	if _, err := app.store.Policies().Create(context.Background(), store.PolicySet{
		Name: "rotten-set", Priority: 10, YAMLSource: "spec: [broken", Status: "draft",
	}); err != nil {
		t.Fatal(err)
	}

	// testApp runs the standalone profile, which seeds the starter set
	// (active, match-all, one shell deny); it is part of the expected world.
	env := listPolicies(t, base, tok, "")
	if env.Total != 5 || len(env.Items) != 5 {
		t.Fatalf("total/items = %d/%d, want 5/5", env.Total, len(env.Items))
	}
	if env.Limit != 0 || env.Offset != 0 {
		t.Errorf("limit/offset echo = %d/%d, want 0/0", env.Limit, env.Offset)
	}
	want := []string{"scale-dev-block", "scale-everyone", "scale-outside", "rotten-set", "standalone-starter"}
	if got := listNames(env); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	for _, r := range env.Items {
		if r.YAML != nil {
			t.Errorf("row %s carries yaml; the list is summary-only", r.Name)
		}
		if r.ID == "" || r.UpdatedAt == "" {
			t.Errorf("row %s misses id/updated_at", r.Name)
		}
	}
	byName := map[string]policyListRow{}
	for _, r := range env.Items {
		byName[r.Name] = r
	}
	if s := byName["scale-dev-block"].Summary; s == nil || s.Description != "Shell blocks for dev" {
		t.Errorf("summary description not carried: %+v", s)
	}
	if byName["rotten-set"].Summary != nil {
		t.Error("unparseable set carries a summary; absence is the honest signal")
	}

	if len(env.Roles) != 3 {
		t.Fatalf("facet = %+v, want 3 groups", env.Roles)
	}
	if env.Roles[0].Role != "*" || env.Roles[1].Role != "dev" || env.Roles[2].Role != "~" {
		t.Errorf("facet order = %v", env.Roles)
	}
	if env.Roles[0].Sets != 2 || env.Roles[0].Postures["deny"] != 2 {
		t.Errorf("facet * = %+v (scale-everyone + the starter)", env.Roles[0])
	}
	if env.Roles[2].Sets != 1 || env.Roles[2].Postures["allow"] != 1 {
		t.Errorf("facet ~ = %+v", env.Roles[2])
	}
}

// TestPolicyListFilters pins every filter and their combination: applied
// before paging, facet computed over the filtered matches, the events-only
// set invisible to every lane chip.
func TestPolicyListFilters(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	for _, doc := range []string{scaleDevBlock, scaleDevAllow, scaleMulti, scaleEveryone, scaleOutside, scaleEvents} {
		applyPolicyYAML(t, base, tok, doc, http.StatusCreated)
	}
	if code, body, _ := adminBytes(t, "POST", base+"/v1/admin/policies/scale-dev-block/activate", tok, "application/json", []byte(`{"status":"active"}`)); code != http.StatusOK {
		t.Fatalf("activate = %d (%s)", code, body)
	}

	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"scale-dev-block", "scale-dev-allow", "scale-multi", "scale-everyone", "scale-outside", "scale-events", "standalone-starter"}},
		{"q=dev-block", []string{"scale-dev-block"}},
		{"q=" + url.QueryEscape("Shell Blocks"), []string{"scale-dev-block"}},
		{"status=active", []string{"scale-dev-block", "standalone-starter"}},
		{"status=draft", []string{"scale-dev-allow", "scale-multi", "scale-everyone", "scale-outside", "scale-events"}},
		{"role=dev", []string{"scale-dev-block", "scale-dev-allow", "scale-multi"}},
		{"role=ops", []string{"scale-multi", "scale-events"}},
		{"role=" + url.QueryEscape("*"), []string{"scale-everyone", "standalone-starter"}},
		{"role=" + url.QueryEscape("~"), []string{"scale-outside"}},
		{"lane=shell", []string{"scale-dev-block", "scale-multi", "standalone-starter"}},
		{"lane=mcp", []string{"scale-dev-allow", "scale-multi"}},
		{"lane=files", []string{"scale-multi", "scale-everyone"}},
		{"lane=net", []string{"scale-multi", "scale-outside"}},
		{"lane=other", []string{"scale-multi"}},
		{"recording=true", []string{"scale-dev-allow"}},
		{"role=dev&lane=shell", []string{"scale-dev-block", "scale-multi"}},
		{"q=nothing-matches-this", nil},
	}
	for _, tc := range cases {
		env := listPolicies(t, base, tok, tc.query)
		got := listNames(env)
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q -> %v, want %v", tc.query, got, tc.want)
		}
		if env.Total != len(tc.want) {
			t.Errorf("%q total = %d, want %d", tc.query, env.Total, len(tc.want))
		}
	}

	// Facet reflects the filtered matches: under role=dev the multi set
	// still contributes its ops membership.
	env := listPolicies(t, base, tok, "role=dev")
	facet := map[string]int{}
	for _, g := range env.Roles {
		facet[g.Role] = g.Sets
	}
	if facet["dev"] != 3 || facet["ops"] != 1 || len(env.Roles) != 2 {
		t.Errorf("role=dev facet = %+v, want dev:3 ops:1", env.Roles)
	}
}

// TestPolicyListPaging pins limit/offset semantics: page windows walk the
// filtered matches, total stays the match count, limit=0 means everything.
func TestPolicyListPaging(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	docs := []string{scaleDevBlock, scaleDevAllow, scaleMulti, scaleEveryone, scaleOutside}
	for _, doc := range docs {
		applyPolicyYAML(t, base, tok, doc, http.StatusCreated)
	}

	env := listPolicies(t, base, tok, "limit=2")
	if env.Total != 6 || env.Limit != 2 || env.Offset != 0 {
		t.Fatalf("page 1 meta = %d/%d/%d, want 6/2/0", env.Total, env.Limit, env.Offset)
	}
	if got := listNames(env); !reflect.DeepEqual(got, []string{"scale-dev-block", "scale-dev-allow"}) {
		t.Errorf("page 1 = %v", got)
	}
	env = listPolicies(t, base, tok, "limit=2&offset=2")
	if got := listNames(env); !reflect.DeepEqual(got, []string{"scale-multi", "scale-everyone"}) {
		t.Errorf("page 2 = %v", got)
	}
	env = listPolicies(t, base, tok, "limit=2&offset=4")
	if got := listNames(env); !reflect.DeepEqual(got, []string{"scale-outside", "standalone-starter"}) {
		t.Errorf("last page = %v", got)
	}
	env = listPolicies(t, base, tok, "limit=2&offset=10")
	if len(env.Items) != 0 || env.Total != 6 {
		t.Errorf("past-the-end = %d items, total %d", len(env.Items), env.Total)
	}
	env = listPolicies(t, base, tok, "limit=0")
	if len(env.Items) != 6 {
		t.Errorf("limit=0 = %d items, want all 6", len(env.Items))
	}
	env = listPolicies(t, base, tok, "role=dev&limit=2&offset=2")
	if env.Total != 3 || !reflect.DeepEqual(listNames(env), []string{"scale-multi"}) {
		t.Errorf("filtered paging = %v total %d", listNames(env), env.Total)
	}
}

// TestPolicyListBadParams pins the 400 lane: every invalid value names the
// parameter and the accepted forms instead of being silently ignored.
func TestPolicyListBadParams(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	cases := []struct {
		query string
		param string
	}{
		{"limit=x", "limit"},
		{"limit=-1", "limit"},
		{"offset=-1", "offset"},
		{"offset=x", "offset"},
		{"status=frozen", "status"},
		{"lane=warp", "lane"},
		{"recording=maybe", "recording"},
	}
	for _, tc := range cases {
		code, body, _ := adminBytes(t, "GET", base+"/v1/admin/policies?"+tc.query, tok, "", nil)
		if code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", tc.query, code)
			continue
		}
		if !strings.Contains(string(body), tc.param) {
			t.Errorf("%q rejection does not name %q: %s", tc.query, tc.param, body)
		}
	}
}

// TestPolicyGetByName pins the editor's fetch: the full payload (yaml
// included) lives on GET by name now that the list is summary-only, drift
// claims ride it the same as the list, and unknown names 404 with the
// same words the delete lane uses.
func TestPolicyGetByName(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	applyPolicyYAML(t, base, tok, scaleDevBlock, http.StatusCreated)

	code, body, _ := adminBytes(t, "GET", base+"/v1/admin/policies/scale-dev-block", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("get by name = %d (%s)", code, body)
	}
	var row policyListRow
	if err := jsonUnmarshal(body, &row); err != nil {
		t.Fatal(err)
	}
	if row.YAML == nil || *row.YAML != scaleDevBlock {
		t.Error("get by name does not carry the stored yaml byte-exact")
	}
	if row.Summary == nil || row.ID == "" || row.Status != "draft" || row.UpdatedAt == "" {
		t.Errorf("get by name misses fields: %+v", row)
	}
	if row.Drift != nil {
		t.Errorf("draft drift = %v, want absent", *row.Drift)
	}

	// Saved-over-active drift shows on the single-set payload too.
	if code, _, _ := adminBytes(t, "POST", base+"/v1/admin/policies/scale-dev-block/activate", tok, "application/json", []byte(`{"status":"active"}`)); code != http.StatusOK {
		t.Fatal("activate failed")
	}
	edited := strings.Replace(scaleDevBlock, "Straza: no", "Straza: nope", 1)
	applyPolicyYAML(t, base, tok, edited, http.StatusOK)
	code, body, _ = adminBytes(t, "GET", base+"/v1/admin/policies/scale-dev-block", tok, "", nil)
	if code != http.StatusOK {
		t.Fatalf("get after edit = %d", code)
	}
	row = policyListRow{}
	if err := jsonUnmarshal(body, &row); err != nil {
		t.Fatal(err)
	}
	if row.Drift == nil || !*row.Drift {
		t.Error("saved-over-active drift not claimed on get by name")
	}

	code, body, _ = adminBytes(t, "GET", base+"/v1/admin/policies/no-such-set", tok, "", nil)
	if code != http.StatusNotFound || !strings.Contains(string(body), "no such policy set") {
		t.Errorf("unknown name = %d %q, want 404 naming no such policy set", code, body)
	}
}

// TestPolicyLanesClassifier pins the summary lanes field, rule by rule:
// explicit tools map to their lane, tool-less rules classify by their
// matcher shape (apps/toolNames = mcp, command/interpreters = shell,
// paths = files), an unscoped rule governs every lane, and a rule bound
// to non-tool events governs none.
func TestPolicyLanesClassifier(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	const lanesProbe = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: lanes-probe
spec:
  priority: 100
  rules:
    - id: by-tools
      tools: [shell.exec, file.read, task.spawn]
      effect: deny
      reason: "Straza: no"
    - id: by-apps
      apps: [demo-tools]
      effect: allow
      reason: "Straza: ok"
    - id: by-command
      command: { denyPatterns: ["mkfs*"] }
      effect: deny
      reason: "Straza: no"
    - id: by-paths
      paths: { deny: ["/etc/*"] }
      effect: deny
      reason: "Straza: no"
    - id: unscoped-hold
      effect: allow
      mode: approve
      reason: "Straza: held"
    - id: events-only
      events: [session.start]
      effect: allow
      reason: "Straza: ok"
`
	applyPolicyYAML(t, base, tok, lanesProbe, http.StatusCreated)

	env := listPolicies(t, base, tok, "q=lanes-probe")
	if len(env.Items) != 1 || env.Items[0].Summary == nil {
		t.Fatalf("lanes-probe not summarized: %+v", env.Items)
	}
	want := map[string]map[string]int{
		"shell": {"deny": 2, "hold": 1},
		"files": {"deny": 2, "hold": 1},
		"mcp":   {"allow": 1, "hold": 1},
		"net":   {"hold": 1},
		"other": {"deny": 1, "hold": 1},
	}
	if got := env.Items[0].Summary.Lanes; !reflect.DeepEqual(got, want) {
		t.Errorf("lanes = %v, want %v", got, want)
	}
}

// TestPolicyListSummaryRecompute pins the cache-freshness contract: a save
// over an existing set serves the NEW summary on the very next list (the
// cache keys on updated_at, so staleness is structurally impossible).
func TestPolicyListSummaryRecompute(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)

	applyPolicyYAML(t, base, tok, scaleDevBlock, http.StatusCreated)
	env := listPolicies(t, base, tok, "q=dev-block")
	if len(env.Items) != 1 || env.Items[0].Summary == nil || env.Items[0].Summary.Rules != 1 {
		t.Fatalf("initial summary = %+v", env.Items)
	}

	twoRules := strings.Replace(scaleDevBlock, "      reason: \"Straza: no\"\n",
		"      reason: \"Straza: no\"\n    - id: second\n      tools: [file.read]\n      effect: allow\n      reason: \"Straza: ok\"\n", 1)
	applyPolicyYAML(t, base, tok, twoRules, http.StatusOK)
	env = listPolicies(t, base, tok, "q=dev-block")
	if len(env.Items) != 1 || env.Items[0].Summary == nil || env.Items[0].Summary.Rules != 2 {
		t.Errorf("post-save summary = %+v, want 2 rules (stale cache?)", env.Items)
	}
	// A second read serves from the warm cache with identical content.
	env = listPolicies(t, base, tok, "q=dev-block")
	if len(env.Items) != 1 || env.Items[0].Summary == nil || env.Items[0].Summary.Rules != 2 {
		t.Errorf("warm-cache summary = %+v", env.Items)
	}
}
