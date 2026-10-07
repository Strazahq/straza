package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
)

func regoSet(t *testing.T, escape string) *Engine {
	t.Helper()
	eng, err := NewEngine([]Document{escapeDoc(t, escape)}, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

// escapeYAML is the set escape-set with escape as its Rego module.
func escapeYAML(escape string) string {
	return `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: escape-set }
spec:
  match: { roles: [contractors] }
  rules:
    - id: allow-shell
      tools: [shell.exec]
      command: { allowPatterns: ["*"] }
      effect: allow
  escape:
    rego: |
` + indent(escape, "      ")
}

func escapeDoc(t *testing.T, escape string) Document {
	t.Helper()
	doc, err := Parse([]byte(escapeYAML(escape)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

const denySudo = `package straza.ext

deny contains msg if {
	input.event.tool == "shell.exec"
	contains(input.event.command, "sudo")
	msg := "contractors may not use sudo"
}`

// TestRegoDenyObserved pins that an escape deny flips the declarative
// allow, with the module's message as reason.
func TestRegoDenyObserved(t *testing.T) {
	eng := regoSet(t, denySudo)
	sub := Subject{User: "bob", Roles: []string{"contractors"}, Attestation: "none"}

	d := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "sudo rm x"}, sub)
	if d.Effect != EffectDeny || d.RuleID != "rego" || !strings.Contains(d.Reason, "sudo") {
		t.Fatalf("escape deny not applied: %+v", d)
	}

	// Non-matching input keeps the declarative allow.
	d = eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "ls"}, sub)
	if d.Effect != EffectAllow || d.RuleID != "allow-shell" {
		t.Fatalf("escape must not affect non-matching input: %+v", d)
	}

	// Escapes from non-applicable sets never run.
	outsider := Subject{User: "kim", Roles: nil, Attestation: "none"}
	d = eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "sudo id"}, outsider)
	if d.Effect != EffectAllow || !d.Default {
		t.Fatalf("escape leaked outside its set's match: %+v", d)
	}
}

// TestRegoEscapeCannotOverrideDeny: escapes only tighten; a declarative
// deny is final even if the module would not deny.
func TestRegoEscapeTightensOnly(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: deny-set }
spec:
  rules:
    - id: hard-deny
      tools: [shell.exec]
      effect: deny
      reason: "declarative deny"
  escape:
    rego: |
      package straza.ext

      deny contains msg if {
        false
        msg := "never"
      }
`))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine([]Document{doc}, EffectAllow)
	if err != nil {
		t.Fatal(err)
	}
	d := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "ls"}, Subject{User: "x"})
	if d.Effect != EffectDeny || d.RuleID != "hard-deny" {
		t.Fatalf("declarative deny must stand: %+v", d)
	}
}

// TestRegoAllowRejectedAtCompile pins that modules declaring allow are
// rejected when the engine compiles.
func TestRegoAllowRejectedAtCompile(t *testing.T) {
	for name, module := range map[string]string{
		"allow rule": `package straza.ext

allow if {
	input.subject.user == "bob"
}`,
		"default allow": `package straza.ext

default allow := true`,
	} {
		doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: sneaky }
spec:
  rules:
    - id: r1
      tools: [shell.exec]
      effect: deny
      reason: x
  escape:
    rego: |
` + indent(module, "      ")))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if _, err := NewEngine([]Document{doc}, EffectAllow); err == nil ||
			!strings.Contains(err.Error(), "only tighten") {
			t.Errorf("%s: engine must reject allow-declaring escapes, got %v", name, err)
		}
	}
}

func TestRegoWrongPackageRejected(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: wrongpkg }
spec:
  rules:
    - { id: r1, tools: [shell.exec], effect: deny, reason: x }
  escape:
    rego: |
      package other.place

      deny contains msg if { msg := "x" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewEngine([]Document{doc}, EffectAllow); err == nil ||
		!strings.Contains(err.Error(), "straza.ext") {
		t.Errorf("wrong package must be rejected, got %v", err)
	}
}

// reachWhy and longWhy are the two reasons a refusal gives: the built-in
// reaches past the module's input, or one call of it runs past the deadline.
const (
	reachWhy = "a policy module must not reach the network, the file system or the process environment"
	longWhy  = "a single call of it can run far past the 100 ms decision deadline or crash the process, " +
		"and Straza checks that deadline only between calls"
)

// refusedSentence is the compile refusal of escape-set for builtin on line.
func refusedSentence(builtin string, line int, why string) string {
	return fmt.Sprintf("policy: set escape-set: its Rego module calls %s on line %d, and Straza refuses that built-in "+
		"because %s. Remove the call from the module", builtin, line, why)
}

// statement is a module whose deny rule makes call on line 4.
func statement(call string) string {
	return "package straza.ext\n\ndeny contains msg if {\n\t" + call + "\n\tmsg := \"x\"\n}"
}

// refusedCalls is one well-typed call of every refused built-in, with why.
var refusedCalls = []struct{ builtin, call, why string }{
	{"http.send", `http.send({"method": "get", "url": "http://169.254.169.254/latest/meta-data/"})`, reachWhy},
	{"net.lookup_ip_addr", `net.lookup_ip_addr("example.com")`, reachWhy},
	{"json.match_schema", `json.match_schema({}, {"$ref": "file:///etc/passwd"})`, reachWhy},
	{"json.verify_schema", `json.verify_schema({"$ref": "http://169.254.169.254/s.json"})`, reachWhy},
	{"opa.runtime", `opa.runtime()`, reachWhy},
	{"strings.render_template", `strings.render_template("{{.a}}", {"a": 1}) == "1"`, longWhy},
	{"rego.parse_module", `count(rego.parse_module("p.rego", "package p")) == 1`, longWhy},
	{"graph.reachable_paths", `count(graph.reachable_paths({"a": ["b"]}, ["a"])) == 1`, longWhy},
	{"bits.lsh", `bits.lsh(1, 3) == 8`, longWhy},
	{"net.cidr_contains_matches", `count(net.cidr_contains_matches(["10.0.0.0/8"], ["10.1.2.3"])) == 1`, longWhy},
	{"glob.match", `glob.match("sudo *", [], "sudo ls")`, longWhy},
	{"graphql.is_valid", `graphql.is_valid("{ a }", "type Query { a: Int }")`, longWhy},
	{"graphql.parse", `count(graphql.parse("{ a }", "type Query { a: Int }")) == 2`, longWhy},
	{"graphql.parse_and_verify", `count(graphql.parse_and_verify("{ a }", "type Query { a: Int }")) == 3`, longWhy},
	{"graphql.parse_query", `count(graphql.parse_query("{ a }")) == 1`, longWhy},
	{"graphql.parse_schema", `count(graphql.parse_schema("type Query { a: Int }")) == 1`, longWhy},
	{"graphql.schema_is_valid", `graphql.schema_is_valid("type Query { a: Int }")`, longWhy},
}

// templateBurn and templateAlloc are two probes: one
// strings.render_template call that ran 11.3 s past the deadline, and one
// that allocated 352 MB before the deny.
const (
	templateBurn = `package straza.ext

deny contains msg if {
	t := strings.render_template("{{range $.a}}{{range $.a}}{{range $.a}}{{end}}{{end}}{{end}}", {"a": numbers.range(1, 500)})
	t == "never"
	msg := "never"
}`
	templateAlloc = `package straza.ext

deny contains msg if {
	t := strings.render_template("{{range $.a}}{{range $.a}}{{range $.a}}0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789{{end}}{{end}}{{end}}", {"a": numbers.range(1, 100)})
	t == "never"
	msg := "never"
}`
)

// TestRegoRefusesBuiltins pins the compile refusal of a module that calls a
// refused built-in, for every refused built-in and in every form a call can
// take, and the built-ins that stay.
func TestRegoRefusesBuiltins(t *testing.T) {
	type row struct {
		name, module, builtin string
		line                  int
		why                   string
	}
	var refused []row
	for _, c := range refusedCalls {
		refused = append(refused, row{c.builtin + " as a statement", statement(c.call), c.builtin, 4, c.why})
	}
	refused = append(refused,
		row{"http.send in an assignment", "package straza.ext\n\ndeny contains msg if {\n\tr := http.send({\"method\": \"get\", \"url\": \"http://a/\"})\n\tmsg := r.raw_body\n}",
			"http.send", 4, reachWhy},
		row{"http.send in a comprehension", "package straza.ext\n\ndeny contains msg if {\n\tinput.event.tool == \"shell.exec\"\n" +
			"\tbodies := [b | some u in [\"http://a/\"]; b := http.send({\"method\": \"get\", \"url\": u}).raw_body]\n\tmsg := concat(\",\", bodies)\n}",
			"http.send", 5, reachWhy},
		row{"http.send in a helper function", "package straza.ext\n\nfetch(u) := http.send({\"method\": \"get\", \"url\": u}).raw_body\n\n" +
			"deny contains msg if {\n\tmsg := fetch(\"http://a/\")\n}", "http.send", 3, reachWhy},
		row{"http.send as a with value", statement(`count([1]) == 1 with count as http.send`), "http.send", 4, reachWhy},
		row{"the review's render_template burn", templateBurn, "strings.render_template", 4, longWhy},
		row{"the review's render_template allocation", templateAlloc, "strings.render_template", 4, longWhy},
	)
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewEngine([]Document{escapeDoc(t, tc.module)}, EffectAllow)
			var refusal *RefusedBuiltinError
			if !errors.As(err, &refusal) {
				t.Fatalf("NewEngine error = %v, want a RefusedBuiltinError", err)
			}
			if want := refusedSentence(tc.builtin, tc.line, tc.why); err.Error() != want {
				t.Errorf("refusal\n got %q\nwant %q", err.Error(), want)
			}
		})
	}
	for _, call := range []string{
		`time.now_ns() < 0`,
		`regex.match("^sudo", input.event.command)`,
		`net.cidr_contains("10.0.0.0/8", "10.1.2.3")`,
		`io.jwt.decode(input.event.command)`,
		`contains(input.event.command, "sudo")`,
		`bits.rsh(8, 3) == 1`,
		`count(graph.reachable({"a": ["b"]}, ["a"])) == 2`,
	} {
		t.Run(call, func(t *testing.T) {
			if _, err := NewEngine([]Document{escapeDoc(t, statement(call))}, EffectAllow); err != nil {
				t.Errorf("a module calling %s must compile, got %v", call, err)
			}
		})
	}
}

// pinnedBuiltins is the sha256 of the sorted names of every built-in OPA
// ships at the version go.mod pins.
const pinnedBuiltins = "d07ebb889aa5da8b05b9d0aa2bee74ff5ebdda0d802e96e9f8de51d5b30f11c5"

// TestRegoCapabilities pins the refused built-ins and the capabilities
// escape modules compile with: every built-in OPA ships except the refused
// ones. The hash fails on an OPA upgrade that adds, drops or renames a
// built-in, so a new built-in that reaches out or runs long in one call
// cannot arrive unnoticed.
func TestRegoCapabilities(t *testing.T) {
	var pinned []string
	for _, c := range refusedCalls {
		pinned = append(pinned, c.builtin)
	}
	refused := slices.Sorted(maps.Keys(refusedBuiltins))
	if slices.Sort(pinned); !slices.Equal(refused, pinned) {
		t.Errorf("refused built-ins = %v, want %v", refused, pinned)
	}
	have := map[string]bool{}
	for _, b := range regoCapabilities().Builtins {
		have[b.Name] = true
	}
	var names []string
	for _, b := range ast.CapabilitiesForThisVersion().Builtins {
		names = append(names, b.Name)
		if want := !slices.Contains(refused, b.Name); have[b.Name] != want {
			t.Errorf("built-in %s available to escape modules = %v, want %v", b.Name, have[b.Name], want)
		}
	}
	for _, name := range refused {
		if !slices.Contains(names, name) {
			t.Errorf("OPA no longer ships %s: find what replaced it and refuse that name instead", name)
		}
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	if got := hex.EncodeToString(sum[:]); got != pinnedBuiltins {
		t.Errorf("OPA's %d built-ins changed (sha256 %s, pinned %s). Review every added or renamed built-in "+
			"for network, file or environment access and for one call that can run far past the deadline, "+
			"add each such one to refusedBuiltins, then update the pin",
			len(names), got, pinnedBuiltins)
	}
}

// TestRegoBackstopRefusal pins that a refused call the walk of refusedCall
// misses still meets the refusal sentence: prepareRego compiles without the
// walk, and the capabilities refuse the call in the same words.
func TestRegoBackstopRefusal(t *testing.T) {
	type row struct {
		name, module, builtin, why string
	}
	var rows []row
	for _, c := range refusedCalls {
		rows = append(rows, row{c.builtin, statement(c.call), c.builtin, c.why})
	}
	rows = append(rows, row{"http.send as a with value", statement(`count([1]) == 1 with count as http.send`), "http.send", reachWhy})
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			_, err := prepareRego("escape-set", tc.module)
			var refusal *RefusedBuiltinError
			if !errors.As(err, &refusal) || err.Error() != refusedSentence(tc.builtin, 4, tc.why) {
				t.Errorf("prepareRego error\n got %v\nwant %s", err, refusedSentence(tc.builtin, 4, tc.why))
			}
		})
	}
}

// TestRegoDeadlineFailsClosed pins the per-decision deadline: a module that
// runs past it denies with its own reason, and a module well inside it
// decides as before.
func TestRegoDeadlineFailsClosed(t *testing.T) {
	const slow = `package straza.ext

deny contains msg if {
	some i in numbers.range(1, 1000)
	some j in numbers.range(1, 1000)
	i * j == -1
	msg := "never"
}`
	sub := Subject{User: "bob", Roles: []string{"contractors"}, Attestation: "none"}
	tests := []struct {
		name, module, command  string
		effect, ruleID, reason string
	}{
		{"a module past the deadline", slow, "ls", EffectDeny, "rego",
			"Straza: the Rego module of policy set escape-set did not finish within 100 ms, so this action is denied. " +
				"Ask an admin to make the module faster or to turn the set off."},
		{"a module inside the deadline that denies", denySudo, "sudo rm x", EffectDeny, "rego", "Straza: contractors may not use sudo"},
		{"a module inside the deadline that does not deny", denySudo, "ls", EffectAllow, "allow-shell", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := regoSet(t, tc.module)
			start := time.Now()
			d := eng.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: tc.command}, sub)
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("decision took %v, want well under 2s", took)
			}
			if d.Effect != tc.effect || d.RuleID != tc.ruleID || d.SetName != "escape-set" ||
				(tc.reason != "" && d.Reason != tc.reason) {
				t.Errorf("decision = %+v, want %s by %s of escape-set with reason %q", d, tc.effect, tc.ruleID, tc.reason)
			}
		})
	}
}

// TestOpenSnapshotRefusesReachingBuiltin pins the open path of the server
// and the kit: Compile only parses, so a signed snapshot can carry a module
// that calls http.send, and OpenSnapshot refuses it.
func TestOpenSnapshotRefusesReachingBuiltin(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	module := "package straza.ext\n\ndeny contains msg if {\n\tr := http.send({\"method\": \"get\", \"url\": \"http://a/\"})\n\tmsg := r.raw_body\n}"
	snap := mustCompile(t, EffectAllow, escapeYAML(module))
	signed, id, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, _, err = OpenSnapshot(signed, id, func(string) (ed25519.PublicKey, bool) { return pub, true })
	var refusal *RefusedBuiltinError
	if !errors.As(err, &refusal) || err.Error() != refusedSentence("http.send", 4, reachWhy) {
		t.Errorf("OpenSnapshot error = %v, want the http.send refusal", err)
	}
}
