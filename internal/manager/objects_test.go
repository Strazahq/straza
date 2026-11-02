package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// objectsManifest is a valid remote App whose straza.objects block is the
// given YAML, indented under objects:.
func objectsManifest(block string) []byte {
	return []byte(`apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: ghapp}
server: {name: example/ghapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: https://example.com/mcp}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
  objects:
` + block)
}

const goodObjects = `    type: repo
    plural: repos
    list:
      tool: repo_list
      attributes:
        - {name: team, setBy: org}
        - {name: topic}
    tools:
      pr_create: {argument: repo}
      search_code: all
      get_me: none
      query: credential
`

func TestToolReachRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		reach    ToolReach
		wantJSON string
		wantYAML string
	}{
		{"argument", ToolReach{Reach: ReachArgument, Argument: "repo"}, `{"argument":"repo"}`, "argument: repo\n"},
		{"all", ToolReach{Reach: ReachAll}, `"all"`, "all\n"},
		{"none", ToolReach{Reach: ReachNone}, `"none"`, "none\n"},
		{"credential", ToolReach{Reach: ReachCredential}, `"credential"`, "credential\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.reach)
			if err != nil || string(b) != tc.wantJSON {
				t.Fatalf("json = %s, %v; want %s", b, err, tc.wantJSON)
			}
			var back ToolReach
			if err := json.Unmarshal(b, &back); err != nil || back != tc.reach {
				t.Fatalf("json back = %+v, %v; want %+v", back, err, tc.reach)
			}
			y, err := yaml.Marshal(tc.reach)
			if err != nil || string(y) != tc.wantYAML {
				t.Fatalf("yaml = %q, %v; want %q", y, err, tc.wantYAML)
			}
			var yback ToolReach
			if err := yaml.Unmarshal(y, &yback); err != nil || yback != tc.reach {
				t.Fatalf("yaml back = %+v, %v; want %+v", yback, err, tc.reach)
			}
		})
	}
}

func TestToolReachRefusesOtherForms(t *testing.T) {
	cases := []struct {
		name string
		json string
		yaml string
	}{
		{"two forms in one mark", `{"argument":"repo","reach":"all"}`, "{argument: repo, reach: all}"},
		{"unknown key", `{"arg":"repo"}`, "{arg: repo}"},
		{"empty mapping", `{}`, "{}"},
		{"argument not a string", `{"argument":5}`, "{argument: [repo]}"},
		{"list", `["all"]`, "[all]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var j ToolReach
			if err := json.Unmarshal([]byte(tc.json), &j); err == nil {
				t.Errorf("json %s parsed as %+v, want a refusal", tc.json, j)
			}
			var y ToolReach
			err := yaml.Unmarshal([]byte(tc.yaml), &y)
			if err == nil {
				t.Errorf("yaml %s parsed as %+v, want a refusal", tc.yaml, y)
			} else if !strings.Contains(err.Error(), "{argument: <name>}") {
				t.Errorf("yaml refusal %q does not name the forms", err)
			}
		})
	}
}

func TestObjectsSpecParses(t *testing.T) {
	m, err := Parse(objectsManifest(goodObjects))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := &ObjectsSpec{
		Type:   "repo",
		Plural: "repos",
		List: ObjectListSpec{Tool: "repo_list", Attributes: []ObjectAttribute{
			{Name: "team", SetBy: "org"}, {Name: "topic"},
		}},
		Tools: map[string]ToolReach{
			"pr_create":   {Reach: ReachArgument, Argument: "repo"},
			"search_code": {Reach: ReachAll},
			"get_me":      {Reach: ReachNone},
			"query":       {Reach: ReachCredential},
		},
	}
	if !reflect.DeepEqual(m.Straza.Objects, want) {
		t.Fatalf("objects = %+v, want %+v", m.Straza.Objects, want)
	}
}

func TestObjectsSpecRefusals(t *testing.T) {
	attrs := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString("        - {name: a" + strings.Repeat("x", i) + "}\n")
		}
		return b.String()
	}
	cases := []struct {
		name  string
		block string
		want  string
	}{
		{"type missing", "    list: {tool: repo_list}\n", "objects.type"},
		{"type upper case", "    type: Repo\n    list: {tool: repo_list}\n", "objects.type"},
		{"type too long", "    type: " + strings.Repeat("r", 33) + "\n    list: {tool: repo_list}\n", "objects.type"},
		{"plural bad", "    type: repo\n    plural: Repos\n    list: {tool: repo_list}\n", "objects.plural"},
		{"list tool missing", "    type: repo\n    list: {}\n", "objects.list.tool"},
		{"list tool not a tool name", "    type: repo\n    list: {tool: \"repo list\"}\n", "objects.list.tool"},
		{"attribute name bad", "    type: repo\n    list:\n      tool: repo_list\n      attributes: [{name: 1team}]\n", "objects.list.attributes"},
		{"attribute twice", "    type: repo\n    list:\n      tool: repo_list\n      attributes: [{name: team}, {name: team}]\n", "objects.list.attributes"},
		{"attribute setBy bad", "    type: repo\n    list:\n      tool: repo_list\n      attributes: [{name: team, setBy: owner}]\n", "setBy"},
		{"seventeen attributes", "    type: repo\n    list:\n      tool: repo_list\n      attributes:\n" + attrs(17), "objects.list.attributes"},
		{"unknown reach", "    type: repo\n    list: {tool: repo_list}\n    tools: {pr_create: some}\n", "objects.tools.pr_create"},
		{"argument as a word", "    type: repo\n    list: {tool: repo_list}\n    tools: {pr_create: argument}\n", "objects.tools.pr_create"},
		{"empty reach", "    type: repo\n    list: {tool: repo_list}\n    tools: {pr_create: \"\"}\n", "objects.tools.pr_create"},
		{"argument name bad", "    type: repo\n    list: {tool: repo_list}\n    tools: {pr_create: {argument: \"re po\"}}\n", "objects.tools.pr_create"},
		{"tool name bad", "    type: repo\n    list: {tool: repo_list}\n    tools: {\"pr create\": all}\n", "objects.tools"},
		{"two forms in one mark", "    type: repo\n    list: {tool: repo_list}\n    tools: {pr_create: {argument: repo, reach: all}}\n", "objects.tools"},
		{"listing tool pinned none", "    type: repo\n    list: {tool: repo_list}\n    tools: {repo_list: none}\n",
			"objects.tools.repo_list gives the listing tool the reach none. The listing tool returns every repo, so its reach is all. Write all, or leave repo_list out of objects.tools"},
		{"listing tool pinned credential", "    type: repo\n    list: {tool: repo_list}\n    tools: {repo_list: credential}\n", "objects.tools.repo_list gives the listing tool the reach credential."},
		{"listing tool pinned argument", "    type: repo\n    list: {tool: repo_list}\n    tools: {repo_list: {argument: repo}}\n", "objects.tools.repo_list gives the listing tool the reach argument repo."},
		{"unknown key source", "    type: repo\n    source: admin\n    list: {tool: repo_list}\n", "source"},
		{"unknown key fields", "    type: repo\n    list: {tool: repo_list, fields: {value: name}}\n", "fields"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(objectsManifest(tc.block))
			if err == nil {
				t.Fatalf("parsed cleanly, want a refusal naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestObjectsSixteenAttributesParse(t *testing.T) {
	var b strings.Builder
	b.WriteString("    type: repo\n    list:\n      tool: repo_list\n      attributes:\n")
	for i := 0; i < 16; i++ {
		b.WriteString("        - {name: a" + strings.Repeat("x", i) + ", setBy: object}\n")
	}
	if _, err := Parse(objectsManifest(b.String())); err != nil {
		t.Fatalf("16 attributes: %v", err)
	}
}

// TestObjectsListingToolFaultCount pins that a listing tool named under
// tools with a malformed reach or name gets only the fault of its own
// form, and a well-formed reach other than all gets the listing fault once.
func TestObjectsListingToolFaultCount(t *testing.T) {
	spec := func(list, tool string, r ToolReach) *ObjectsSpec {
		return &ObjectsSpec{Type: "repo", List: ObjectListSpec{Tool: list}, Tools: map[string]ToolReach{tool: r}}
	}
	cases := []struct {
		name string
		spec *ObjectsSpec
		want []string // each fault must contain the matching text
	}{
		{"unknown word", spec("repo_list", "repo_list", ToolReach{Reach: "some"}), []string{`objects.tools.repo_list has the reach "some"`}},
		{"empty word", spec("repo_list", "repo_list", ToolReach{}), []string{`objects.tools.repo_list has the reach ""`}},
		{"argument without a name", spec("repo_list", "repo_list", ToolReach{Reach: ReachArgument}), []string{"objects.tools.repo_list says argument without naming one"}},
		{"argument with a bad name", spec("repo_list", "repo_list", ToolReach{Reach: ReachArgument, Argument: "re po"}), []string{`objects.tools.repo_list names the argument "re po"`}},
		{"empty tool name", spec("", "", ToolReach{Reach: ReachNone}), []string{"objects.list.tool is empty", `objects.tools names ""`}},
		{"well-formed none", spec("repo_list", "repo_list", ToolReach{Reach: ReachNone}), []string{"objects.tools.repo_list gives the listing tool the reach none."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var faults []string
			validateObjects(tc.spec, func(format string, args ...any) { faults = append(faults, fmt.Sprintf(format, args...)) })
			if len(faults) != len(tc.want) {
				t.Fatalf("faults = %q, want %d containing %q", faults, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(faults[i], want) {
					t.Errorf("fault %d = %q, want it to contain %q", i, faults[i], want)
				}
			}
		})
	}
}

// TestObjectsListingToolPinnedAllParses pins that a block may name its
// listing tool under tools with the reach all, the one reach it has.
func TestObjectsListingToolPinnedAllParses(t *testing.T) {
	if _, err := Parse(objectsManifest("    type: repo\n    list: {tool: repo_list}\n    tools: {repo_list: all}\n")); err != nil {
		t.Fatalf("listing tool pinned all: %v", err)
	}
}

// TestObjectsSurviveStoredRoundTrip pins that a stored manifest keeps its
// objects block through FromJSON and back, the path every reader of a
// stored manifest takes.
func TestObjectsSurviveStoredRoundTrip(t *testing.T) {
	m, err := Parse(objectsManifest(goodObjects))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	stored, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(stored, `"pr_create":{"argument":"repo"}`) || !strings.Contains(stored, `"search_code":"all"`) {
		t.Fatalf("stored JSON lacks the tool reaches: %s", stored)
	}
	back, err := FromJSON(stored)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if !reflect.DeepEqual(back.Straza.Objects, m.Straza.Objects) {
		t.Fatalf("objects after FromJSON = %+v, want %+v", back.Straza.Objects, m.Straza.Objects)
	}
	again, err := back.JSON()
	if err != nil || again != stored {
		t.Fatalf("second JSON differs:\n%s\n%s", again, stored)
	}
}

func TestObjectsPluralWordAndReachOf(t *testing.T) {
	o := ObjectsSpec{Type: "repo", Tools: map[string]ToolReach{"get_me": {Reach: ReachNone}}}
	if got := o.PluralWord(); got != "repos" {
		t.Errorf("PluralWord = %q, want repos", got)
	}
	o.Plural = "repositories"
	if got := o.PluralWord(); got != "repositories" {
		t.Errorf("PluralWord = %q, want repositories", got)
	}
	if got := o.ReachOf("get_me"); got != (ToolReach{Reach: ReachNone}) {
		t.Errorf("ReachOf(get_me) = %+v", got)
	}
	if got := o.ReachOf("unnamed"); got != (ToolReach{Reach: ReachAll}) {
		t.Errorf("ReachOf(unnamed) = %+v, want all", got)
	}
}

// TestObjectsExamplesRefusedForTheirRule pins that each invalid objects
// example in the spec corpus is refused for the rule its comment names.
func TestObjectsExamplesRefusedForTheirRule(t *testing.T) {
	for file, want := range map[string]string{
		"invalid-objects-bad-type.yaml":           `objects.type "Repo"`,
		"invalid-objects-two-forms.yaml":          "takes only the key argument",
		"invalid-objects-unknown-reach.yaml":      `objects.tools.search_code has the reach "some"`,
		"invalid-objects-listing-tool-reach.yaml": "objects.tools.repo_list gives the listing tool the reach none",
	} {
		raw, err := os.ReadFile(filepath.Join(specExamples, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: refusal %v, want one naming %q", file, err, want)
		}
	}
}

// TestSameButObjects pins the restart comparison: two stored manifests
// that differ only under straza.objects are one server, and the answer
// carries the second block.
func TestSameButObjects(t *testing.T) {
	stored := func(block, limits string) string {
		m, err := Parse(objectsManifest(block))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if limits != "" {
			m.Straza.Limits = &LimitsSpec{RPS: 5}
		}
		raw, err := m.JSON()
		if err != nil {
			t.Fatalf("JSON: %v", err)
		}
		return raw
	}
	pinned, other := stored(goodObjects, ""), stored("    type: project\n    list: {tool: projects}\n", "")
	cases := []struct {
		name     string
		a, b     string
		same     bool
		wantType string
	}{
		{"equal", pinned, pinned, true, "repo"},
		{"objects only", pinned, other, true, "project"},
		{"objects and limits", pinned, stored("    type: project\n    list: {tool: projects}\n", "rps"), false, ""},
		{"a manifest that does not decode", pinned, "{", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objects, same := sameButObjects(tc.a, tc.b)
			if same != tc.same {
				t.Fatalf("same = %v, want %v", same, tc.same)
			}
			if tc.same && (objects == nil || objects.Type != tc.wantType) {
				t.Fatalf("objects = %+v, want type %q", objects, tc.wantType)
			}
		})
	}
}
