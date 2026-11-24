package server

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// mismatchHead and mismatchTail are the words of argumentsMismatchMsg
// before and after the tool's name.
const (
	mismatchHead = "Straza checked the arguments against "
	mismatchTail = "'s input schema before asking for approval, and they do not match: "
)

// argumentsMismatchMsg is the refusal of an approval-gated call whose
// arguments break the tool's input schema, as the caller reads it. It does
// not start with "Straza:", because that prefix tells a model never to
// retry, and a corrected call should be retried.
const argumentsMismatchMsg = mismatchHead + "%s" + mismatchTail + "%s. Fix the arguments and call the tool again."

// argumentsMismatchRecord is the reason the record of that refusal keeps.
// The validator's detail quotes argument values and keys among the schema's
// words, and nothing in its text tells them apart, so the record names the
// tool and leaves the detail out. A draft's documents would otherwise reach
// the chain, which records only their digest.
const argumentsMismatchRecord = "Straza checked the arguments against %s's input schema before asking for approval, and they do not match. Fix the arguments and call the tool again."

// mismatchRecord answers the reason the record of approval_request keeps for
// its refusal msg: argumentsMismatchRecord when msg is the schema check's
// refusal of the call it describes, and msg whole otherwise. The name ends
// at the first mismatchTail, which comes before the detail, so a detail that
// repeats the sentence cannot reach the record.
func mismatchRecord(msg string) string {
	name, ok := strings.CutPrefix(msg, mismatchHead)
	if !ok {
		return msg
	}
	name, _, ok = strings.Cut(name, mismatchTail)
	if !ok {
		return msg
	}
	return fmt.Sprintf(argumentsMismatchRecord, name)
}

// schemaDetailMax is the most bytes of the schema check's detail that
// argumentsMismatchMsg quotes. The validator's detail can repeat the
// arguments that broke the schema, so it is cut by cutQuoted.
const schemaDetailMax = 512

// inputSchema returns the input schema the server published for the
// catalog name, or nil when the catalog holds no such tool.
func (c *sessionCatalog) inputSchema(name string) any {
	for _, t := range c.tools {
		if t.Name == name {
			return t.InputSchema
		}
	}
	return nil
}

// outputSchemaMaxNodes is the most maps and arrays the walk of an output
// schema enters before it leaves the schema out.
const outputSchemaMaxNodes = 2000

// outputSchemaMaxDepth is the deepest nesting of maps and arrays, the top
// schema at depth 1, that the walk of an output schema enters.
const outputSchemaMaxDepth = 32

// outputSchemaRefusedKeys are the keywords that make a schema refer to or
// name another schema, or carry a regular expression.
var outputSchemaRefusedKeys = []string{"$ref", "$dynamicRef", "$recursiveRef", "$id", "$anchor", "$dynamicAnchor", "$recursiveAnchor", "pattern", "patternProperties"}

// outputSchemaTypes are the type names of JSON Schema.
var outputSchemaTypes = []string{"object", "array", "string", "number", "integer", "boolean", "null"}

// servedOutputSchema answers an upstream tool's output schema unchanged
// when it is plain, and otherwise nil, which leaves the key out while the
// tool stays listed. Plain is a JSON object of type "object" whose
// top-level properties are all objects, and in which plainSchema finds
// nothing it refuses. An MCP client SDK may refuse or fail a whole
// tools/list over one output schema it cannot read, so one server's schema
// must not take every tool from a client. The walk is bounded because the
// schema is the server's own text and the check runs at every catalog
// build. A schema with references or patterns is left out, though it may
// be valid.
func servedOutputSchema(schema any) any {
	m, ok := schema.(map[string]any)
	if !ok || m["type"] != "object" {
		return nil
	}
	if p, has := m["properties"]; has {
		props, ok := p.(map[string]any)
		if !ok {
			return nil
		}
		for _, v := range props {
			if _, ok := v.(map[string]any); !ok {
				return nil
			}
		}
	}
	if !(&plainSchema{}).schema(m, 1) {
		return nil
	}
	return schema
}

// plainSchema walks an output schema and counts the maps and arrays it
// enters, refusing past outputSchemaMaxNodes or outputSchemaMaxDepth.
type plainSchema struct{ nodes int }

// enter counts one map or array at depth and reports whether the walk may
// go on.
func (w *plainSchema) enter(depth int) bool {
	w.nodes++
	return w.nodes <= outputSchemaMaxNodes && depth <= outputSchemaMaxDepth
}

// schema reports whether the schema map m at depth is plain: no refused
// keyword, type names of JSON Schema, a non-empty enum, a required list of
// strings, and every subschema plain. The values of enum, const, default
// and examples are data and are not walked. The keys under properties,
// $defs, definitions and dependentSchemas are names, and each value there
// is a plain schema or a boolean.
func (w *plainSchema) schema(m map[string]any, depth int) bool {
	if !w.enter(depth) {
		return false
	}
	for _, k := range outputSchemaRefusedKeys {
		if _, has := m[k]; has {
			return false
		}
	}
	if t, has := m["type"]; has {
		names, isList := t.([]any)
		if !isList {
			names = []any{t}
		}
		if len(names) == 0 {
			return false
		}
		for _, n := range names {
			if s, ok := n.(string); !ok || !slices.Contains(outputSchemaTypes, s) {
				return false
			}
		}
	}
	if e, has := m["enum"]; has {
		if list, ok := e.([]any); !ok || len(list) == 0 {
			return false
		}
	}
	if r, has := m["required"]; has {
		list, ok := r.([]any)
		if !ok {
			return false
		}
		for _, n := range list {
			if _, ok := n.(string); !ok {
				return false
			}
		}
	}
	for k, v := range m {
		switch k {
		case "enum", "const", "default", "examples":
		case "properties", "$defs", "definitions", "dependentSchemas":
			named, ok := v.(map[string]any)
			if !ok || !w.enter(depth+1) {
				return false
			}
			for _, sub := range named {
				switch s := sub.(type) {
				case bool:
				case map[string]any:
					if !w.schema(s, depth+2) {
						return false
					}
				default:
					return false
				}
			}
		default:
			if !w.value(v, depth+1) {
				return false
			}
		}
	}
	return true
}

// value reports whether v, the value of any other keyword, at depth is
// plain: a map is walked as a schema, each map in an array is walked as a
// schema, and a scalar passes.
func (w *plainSchema) value(v any, depth int) bool {
	switch x := v.(type) {
	case map[string]any:
		return w.schema(x, depth)
	case []any:
		if !w.enter(depth) {
			return false
		}
		for _, e := range x {
			if m, ok := e.(map[string]any); ok && !w.schema(m, depth+1) {
				return false
			}
		}
	}
	return true
}

// schemaCheckMaxBytes is the most bytes of an input schema, as JSON, that
// the argument check reads.
const schemaCheckMaxBytes = 128 << 10

// schemaCheckMaxCost is the most the walk of an input schema may spend.
// Every value costs its depth, once and once more for each 64 bytes of the
// keys and references that lead to it. The validator copies the error of a
// value that fails once for each level above it, and each copy quotes the
// path to that level. A text or a number costs the 64-byte units an error
// writes for it, times its depth. The target of a reference is counted
// each time it is used.
const schemaCheckMaxCost = 10_000

// schemaCheckMaxDepth is the deepest nesting of values and references, the
// top schema at depth 1, that the walk of an input schema enters.
const schemaCheckMaxDepth = 32

// schemaCheckMaxWork is the most the argument check may cost: the walk's
// cost of the schema times the weight of the arguments. The validator
// applies each part of the schema to each value it fits, and an error
// quotes both, so its time and memory follow that product.
const schemaCheckMaxWork = 250_000

// argumentWeight is the weight of decoded arguments: one for every value,
// and one more for each 64 bytes that its keys, texts and numbers take when
// an error quotes them.
func argumentWeight(v any) int {
	values, quoted := 0, 0
	var weigh func(any)
	weigh = func(v any) {
		values++
		switch x := v.(type) {
		case string:
			quoted += quotedLen(x)
		case float64:
			quoted += numberLen(x)
		case []any:
			for _, e := range x {
				weigh(e)
			}
		case map[string]any:
			for k, e := range x {
				quoted += quotedLen(k)
				weigh(e)
			}
		}
	}
	weigh(v)
	return values + quoted/64
}

// numberLen is the most bytes f takes when an error writes it out. The
// validator writes a bound in full and a value as a fraction, so a number
// far from one takes hundreds of digits, twice.
func numberLen(f float64) int {
	var buf [400]byte
	return 2 * len(strconv.AppendFloat(buf[:0], f, 'f', -1, 64))
}

// quotedLen is the most bytes s takes when an error quotes it, where a
// byte outside printable ASCII is written as an escape of four bytes.
func quotedLen(s string) int {
	n := len(s)
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			n += 3
		}
	}
	return n
}

// schemaCheckRefusedKeys are the keywords with a text value that make the
// walk refuse a schema: the ones that name a schema or reach one in a way
// the walk does not follow, and pattern, which the validator compiles at
// every check.
var schemaCheckRefusedKeys = []string{"$dynamicRef", "$recursiveRef", "$id", "$anchor", "$dynamicAnchor", "pattern"}

// schemaCheckVersions are the values of $schema the validator checks
// against. For any other it reports an error whatever the arguments are.
var schemaCheckVersions = []string{"http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft-07/schema#", "https://json-schema.org/draft/2020-12/schema"}

// schemaWalk decides whether an input schema may go to the validator, which
// follows every reference with no bound of its own: a reference loop
// overflows its stack, which ends the process, and references that fan out
// keep it busy for days. The walk spends schemaCheckMaxCost on the schema as
// it would be with every reference replaced by its target.
//
// The walk never asks whether a map is a schema, a map of property names or
// data, because the validator's decoder answers that by keyword names
// matched without regard to case, and a walk that judged it differently
// once would miss a reference. It reads every map alike and tells a keyword
// from a property of the same name by the value: a keyword's is text and a
// property's is a schema.
type schemaWalk struct {
	root map[string]any
	cost int
}

// spend adds n to the cost and reports whether the walk may go on.
func (w *schemaWalk) spend(n int) bool {
	w.cost += n
	return w.cost <= schemaCheckMaxCost
}

// value reports whether v, at depth and reached by keys and references of
// path bytes, is within the walk's bounds and free of what the walk
// refuses.
func (w *schemaWalk) value(v any, depth, path int) bool {
	if depth > schemaCheckMaxDepth || !w.spend(depth*(1+path/64)) {
		return false
	}
	switch x := v.(type) {
	case string:
		// The validator's error quotes a text and copies it once for each
		// level it sits under.
		return w.spend(depth * len(x) / 64)
	case float64:
		return w.spend(depth * numberLen(x) / 64)
	case []any:
		for _, e := range x {
			if !w.value(e, depth+1, path+2) {
				return false
			}
		}
	case map[string]any:
		for k, sub := range x {
			if !w.member(k, sub, depth+1, path+len(k)+1) {
				return false
			}
		}
	}
	return true
}

// member reports whether the key k with the value v passes. A $ref with a
// text value is followed, and its target is walked in its place. A text
// value under one of schemaCheckRefusedKeys, and patternProperties with any
// value, because its keys are patterns, refuse the schema. Each name is
// matched without regard to case, as the validator's decoder matches it.
func (w *schemaWalk) member(k string, v any, depth, path int) bool {
	fold := func(name string) bool { return strings.EqualFold(k, name) }
	text, isText := v.(string)
	switch {
	case fold("patternProperties"):
		return false
	case isText && fold("$ref"):
		target, ok := w.target(text)
		return ok && w.value(target, depth, path+len(text))
	case isText && slices.ContainsFunc(schemaCheckRefusedKeys, fold):
		return false
	}
	return w.value(v, depth, path)
}

// target answers the value the reference ref names, and false for every
// form but "#" and a JSON pointer from the top of the schema with no escape
// in it. A step into a map must find the key under its exact name and under
// no other spelling, so the walk and the validator cannot mean two targets.
func (w *schemaWalk) target(ref string) (any, bool) {
	if strings.ContainsAny(ref, "~%") {
		return nil, false
	}
	if ref == "#" {
		return w.root, true
	}
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil, false
	}
	var at any = w.root
	for _, step := range strings.Split(path, "/") {
		switch x := at.(type) {
		case map[string]any:
			twins := 0
			for k := range x {
				if strings.EqualFold(k, step) {
					twins++
				}
			}
			if at, ok = x[step]; !ok || twins != 1 {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(step)
			if err != nil || i < 0 || i >= len(x) || strconv.Itoa(i) != step {
				return nil, false
			}
			at = x[i]
		default:
			return nil, false
		}
	}
	return at, true
}

// schemaMismatch reports why args break schema, cut at schemaDetailMax
// bytes, or "" when they fit. It also answers "" when the check cannot
// judge: no schema, one the validator cannot read, such as draft-04's
// boolean exclusiveMaximum or a $schema outside schemaCheckVersions, one
// whose $ref points outside itself, one that schemaWalk refuses or that is
// past schemaCheckMaxBytes, and arguments too heavy for the schema under
// schemaCheckMaxWork. The validator's default loader refuses every
// external reference, so a schema from an upstream server never makes
// strazad fetch a URL or read a file. The check is never stricter than a
// server that can read its own schema, and the call then goes on as it
// would without the check.
func schemaMismatch(schema any, args json.RawMessage) string {
	if schema == nil {
		return ""
	}
	raw, err := json.Marshal(schema)
	if err != nil || len(raw) > schemaCheckMaxBytes {
		return ""
	}
	// The walk reads the bytes the validator decodes, not the value the
	// catalog holds, so both see one document.
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	root, ok := doc.(map[string]any)
	walk := schemaWalk{root: root}
	if !ok || !walk.value(root, 1, 0) {
		return ""
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var instance any
	if err := json.Unmarshal(args, &instance); err != nil {
		return ""
	}
	if walk.cost*argumentWeight(instance) > schemaCheckMaxWork {
		return ""
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	if s.Schema != "" && !slices.Contains(schemaCheckVersions, s.Schema) {
		return ""
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return ""
	}
	if err := resolved.Validate(instance); err != nil {
		return cutQuoted(err.Error(), schemaDetailMax)
	}
	return ""
}

// schemaCheckSlots bounds how many argument checks run in the validator at
// one time. One check is bounded by schemaCheckMaxWork, and nothing else
// bounds how many callers ask at once, so without it the memory the checks
// hold together grows with the callers.
var schemaCheckSlots = make(chan struct{}, 4)

// boundedSchemaMismatch is schemaMismatch for a call in flight. When
// schemaCheckSlots is full it answers "", as for a schema the check cannot
// judge, so a call never waits for another caller's check.
func boundedSchemaMismatch(schema any, args json.RawMessage) string {
	select {
	case schemaCheckSlots <- struct{}{}:
		defer func() { <-schemaCheckSlots }()
		return schemaMismatch(schema, args)
	default:
		return ""
	}
}
