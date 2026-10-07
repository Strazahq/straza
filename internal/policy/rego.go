package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
)

// regoPackage is the only package escape modules may declare (SPEC.md §5).
const regoPackage = "data.straza.ext"

// regoDeadline bounds the escape modules of one decision together. A
// decision whose modules run past it is a deny (SPEC.md §5).
const regoDeadline = 100 * time.Millisecond

// reachesOut and runsLong are why a built-in is refused, as the refusal
// sentence words it: it reaches past the module's input, or one call of it
// runs past regoDeadline, which OPA checks only between evaluation steps.
const (
	reachesOut = "a policy module must not reach the network, the file system or the process environment"
	runsLong   = "a single call of it can run far past the 100 ms decision deadline or crash the process, " +
		"and Straza checks that deadline only between calls"
)

// refusedBuiltins maps each OPA built-in an escape module may not call to
// why. http.send fetches any URL and reads local files and environment
// variables through its TLS options. net.lookup_ip_addr queries the system
// resolver. json.match_schema and json.verify_schema fetch a schema's $ref
// over http and from file:// paths. opa.runtime answers the process
// environment once a runtime is set. Each runsLong built-in ran seconds past
// the deadline in one call on a small module, or overflowed the stack and
// ended the process, as the graphql parsers and glob.match do on deep
// nesting.
var refusedBuiltins = map[string]string{
	"http.send":                 reachesOut,
	"json.match_schema":         reachesOut,
	"json.verify_schema":        reachesOut,
	"net.lookup_ip_addr":        reachesOut,
	"opa.runtime":               reachesOut,
	"strings.render_template":   runsLong,
	"rego.parse_module":         runsLong,
	"graph.reachable_paths":     runsLong,
	"bits.lsh":                  runsLong,
	"net.cidr_contains_matches": runsLong,
	"glob.match":                runsLong,
	"graphql.is_valid":          runsLong,
	"graphql.parse":             runsLong,
	"graphql.parse_and_verify":  runsLong,
	"graphql.parse_query":       runsLong,
	"graphql.parse_schema":      runsLong,
	"graphql.schema_is_valid":   runsLong,
}

// regoCapabilities is OPA's capabilities for this version less
// refusedBuiltins, built once on first use so a process whose snapshot has
// no module never builds it. The compiler only reads it, so every module
// shares the one value.
var regoCapabilities = sync.OnceValue(func() *ast.Capabilities {
	caps := ast.CapabilitiesForThisVersion()
	caps.Builtins = slices.DeleteFunc(caps.Builtins, func(b *ast.Builtin) bool {
		_, refused := refusedBuiltins[b.Name]
		return refused
	})
	return caps
})

// RefusedBuiltinError is the compile refusal of an escape module that calls
// one of refusedBuiltins. Its text names the set, the built-in, the line of
// the call and why, and says to remove the call.
type RefusedBuiltinError struct {
	set, builtin string
	line         int
}

// Error is the refusal sentence, without a closing period.
func (e *RefusedBuiltinError) Error() string {
	return fmt.Sprintf("policy: set %s: its Rego module calls %s on line %d, and Straza refuses that built-in "+
		"because %s. Remove the call from the module", e.set, e.builtin, e.line, refusedBuiltins[e.builtin])
}

// regoEscape is one compiled per-set escape hatch. It can only add
// denies: the engine queries `deny` and nothing else, and compilation
// rejects modules that declare `allow`.
type regoEscape struct {
	setName string
	query   rego.PreparedEvalQuery
}

// compileRego validates and prepares an escape module. Monotonic-tightening
// guard: a module declaring any `allow` rule is rejected at compile time.
func compileRego(setName, src string) (*regoEscape, error) {
	module, err := ast.ParseModule("escape.rego", src)
	if err != nil {
		return nil, fmt.Errorf("policy: set %s: rego parse: %w", setName, err)
	}
	if got := module.Package.Path.String(); got != regoPackage {
		return nil, fmt.Errorf("policy: set %s: escape package must be straza.ext, got %s", setName, got)
	}
	for _, rule := range module.Rules {
		if rule.Head.Name.String() == "allow" {
			return nil, fmt.Errorf("policy: set %s: escape modules may only tighten; rule %q at %v is not permitted",
				setName, "allow", rule.Location)
		}
	}
	if refusal := refusedCall(setName, module); refusal != nil {
		return nil, refusal
	}
	return prepareRego(setName, src)
}

// prepareRego compiles src with regoCapabilities, which are the backstop
// for a call form the walk of refusedCall misses. A module that fails only
// for want of a refused built-in answers the walk's RefusedBuiltinError, so
// every door still reads the refusal sentence.
func prepareRego(setName, src string) (*regoEscape, error) {
	query, err := rego.New(
		rego.Query("data.straza.ext.deny"),
		rego.Module("escape.rego", src),
		rego.Capabilities(regoCapabilities()),
	).PrepareForEval(context.Background())
	if err != nil {
		if refusal := capabilityRefusal(setName, src, err); refusal != nil {
			return nil, refusal
		}
		return nil, fmt.Errorf("policy: set %s: rego compile: %w", setName, err)
	}
	return &regoEscape{setName: setName, query: query}, nil
}

// capabilityRefusal answers the refusal of src when compileErr came from
// the missing refused built-ins: src then compiles with every built-in, and
// fails without the first refused one it needs, which a with value names
// too. The line is where compileErr points. It answers nil when src fails
// with every built-in as well. It runs only on a failed compile.
func capabilityRefusal(setName, src string, compileErr error) *RefusedBuiltinError {
	module, err := ast.ParseModule("escape.rego", src)
	if err != nil || !compilesWithout(module, "") {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(refusedBuiltins)) {
		if compilesWithout(module, name) {
			continue
		}
		refusal := &RefusedBuiltinError{set: setName, builtin: name}
		var errs ast.Errors
		if errors.As(compileErr, &errs) && len(errs) > 0 && errs[0].Location != nil {
			refusal.line = errs[0].Location.Row
		}
		return refusal
	}
	return nil
}

// compilesWithout reports whether module compiles with every built-in OPA
// ships except the one named builtin.
func compilesWithout(module *ast.Module, builtin string) bool {
	caps := ast.CapabilitiesForThisVersion()
	caps.Builtins = slices.DeleteFunc(caps.Builtins, func(b *ast.Builtin) bool { return b.Name == builtin })
	c := ast.NewCompiler().WithCapabilities(caps)
	c.Compile(map[string]*ast.Module{"escape.rego": module.Copy()})
	return !c.Failed()
}

// refusedCall answers the refusal of the first reference in module to a
// built-in of refusedBuiltins, or nil when there is none. It sees a call in
// any position: a statement, an assignment, a comprehension, a function
// body and a with value.
func refusedCall(setName string, module *ast.Module) *RefusedBuiltinError {
	var found *RefusedBuiltinError
	ast.WalkTerms(module, func(t *ast.Term) bool {
		ref, ok := t.Value.(ast.Ref)
		if !ok || found != nil {
			return found != nil
		}
		if _, refused := refusedBuiltins[ref.String()]; !refused {
			return false
		}
		found = &RefusedBuiltinError{set: setName, builtin: ref.String()}
		if t.Location != nil {
			found.line = t.Location.Row
		}
		return true
	})
	return found
}

// applyEscapes runs every applicable set's escape module over the
// declarative decision. Escapes only tighten: an existing deny is final,
// and any deny message flips an allow to deny. Evaluation errors fail
// closed. The modules of one decision share one regoDeadline, which
// starts when the first applicable module runs, and running past it is a
// deny with its own reason.
func (e *Engine) applyEscapes(ev Event, sub Subject, d Decision) Decision {
	if d.Effect == EffectDeny {
		return d
	}
	var ctx context.Context
	for si := range e.sets {
		set := &e.sets[si]
		if set.escape == nil || !set.applies(sub) {
			continue
		}
		if ctx == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), regoDeadline)
			defer cancel() // runs once: ctx is set on the first pass only
		}
		msg, denied, err := set.escape.eval(ctx, ev, sub, d)
		if err != nil {
			reason := fmt.Sprintf("Straza: policy escape evaluation failed (%s); fail closed", set.name)
			// OPA's cancel error does not wrap the context's, so ask the context.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = fmt.Sprintf("Straza: the Rego module of policy set %s did not finish within %d ms, so this action is denied. "+
					"Ask an admin to make the module faster or to turn the set off.", set.name, regoDeadline.Milliseconds())
			}
			return Decision{
				Effect:  EffectDeny,
				SetName: set.name,
				RuleID:  "rego",
				Reason:  reason,
			}
		}
		if denied {
			return Decision{
				Effect:  EffectDeny,
				SetName: set.name,
				RuleID:  "rego",
				Reason:  msg,
			}
		}
	}
	return d
}

func (r *regoEscape) eval(ctx context.Context, ev Event, sub Subject, d Decision) (string, bool, error) {
	input, err := toPlain(map[string]any{"event": ev, "subject": sub, "decision": d})
	if err != nil {
		return "", false, err
	}
	rs, err := r.query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return "", false, err
	}
	for _, result := range rs {
		for _, expr := range result.Expressions {
			if msgs, ok := expr.Value.([]any); ok && len(msgs) > 0 {
				msg := fmt.Sprintf("Straza: denied by policy escape (%s)", r.setName)
				if s, ok := msgs[0].(string); ok && s != "" {
					msg = "Straza: " + s
				}
				return msg, true, nil
			}
		}
	}
	return "", false, nil
}

// toPlain JSON-roundtrips structs into plain maps for OPA input.
func toPlain(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
