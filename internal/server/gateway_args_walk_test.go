package server

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// schemaJSON decodes s as the go-sdk client decodes a listed schema.
func schemaJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return v
}

// fanOutSchema is a schema with no reference loop whose n definitions each
// reference the next one twice, so a validator applies the last 2^n times.
func fanOutSchema(n int) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","required":["never"],"$ref":"#/$defs/d0","$defs":{`)
	for i := range n {
		fmt.Fprintf(&b, `"d%d":{"allOf":[{"$ref":"#/$defs/d%d"},{"$ref":"#/$defs/d%d"}]},`, i, i+1, i+1)
	}
	fmt.Fprintf(&b, `"d%d":{"type":"object"}}}`, n)
	return b.String()
}

// patternsSchema is a schema of n properties that each carry a pattern.
func patternsSchema(n int) string {
	var b strings.Builder
	b.WriteString(`{"type":"object","required":["never"],"properties":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"p%d":{"type":"string","pattern":"^((a{1,30}){1,30})b%d$"}`, i, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

// TestSchemaMismatchLeavesHostileSchemasUnchecked pins which input schemas
// the argument check hands to the validator. Every schema in the first table
// requires the property never, which the arguments lack, so an answer of ""
// proves the schema was not checked. Each of the looping forms overflows the
// validator's stack, which ends the process, and the fan-out form keeps it
// busy for days.
func TestSchemaMismatchLeavesHostileSchemasUnchecked(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name   string
		schema string
		args   string
	}{
		{"a reference to the schema itself", `{"required":["never"],"$ref":"#"}`, `{}`},
		{"an empty reference", `{"type":"object","required":["never"],"$ref":""}`, `{}`},
		{"a property that references itself", `{"type":"object","required":["never"],"properties":{"a":{"$ref":"#/properties/a"}}}`, `{"a":1}`},
		{"two definitions that reference each other", `{"type":"object","required":["never"],"properties":{"a":{"$ref":"#/$defs/x"}},"$defs":{"x":{"$ref":"#/$defs/y"},"y":{"$ref":"#/$defs/x"}}}`, `{"a":1}`},
		{"a loop through allOf", `{"type":"object","required":["never"],"allOf":[{"$ref":"#"}]}`, `{}`},
		{"a loop through anyOf", `{"type":"object","required":["never"],"anyOf":[{"type":"object"},{"$ref":"#"}]}`, `{}`},
		{"a loop through not", `{"type":"object","required":["never"],"not":{"$ref":"#"}}`, `{}`},
		{"a loop through then", `{"type":"object","required":["never"],"if":{"type":"object"},"then":{"$ref":"#"}}`, `{}`},
		{"a loop through dependentSchemas", `{"type":"object","required":["never"],"dependentSchemas":{"a":{"$ref":"#"}}}`, `{"a":1}`},
		{"a loop through propertyNames", `{"type":"object","required":["never"],"propertyNames":{"$ref":"#/$defs/x"},"$defs":{"x":{"$ref":"#/$defs/x"}}}`, `{"a":1}`},
		{"a loop through an anchor", `{"type":"object","required":["never"],"$defs":{"x":{"$anchor":"foo","$ref":"#foo"}},"properties":{"a":{"$ref":"#foo"}}}`, `{"a":1}`},
		{"a loop through a dynamic reference", `{"type":"object","required":["never"],"$dynamicAnchor":"n","$dynamicRef":"#n"}`, `{}`},
		{"a loop through the schema's own $id", `{"$id":"http://x/s","type":"object","required":["never"],"$ref":"http://x/s"}`, `{}`},
		{"a loop through a keyword in another case", `{"type":"object","required":["never"],"ALLOF":[{"$REF":"#"}]}`, `{}`},
		{"a loop behind a twin key in another case", `{"type":"object","required":["never"],"properties":{"a":{"$ref":"#/$defs/x"}},"$defs":{"x":{"$ref":"#/$Defs/x"}},"$Defs":{"x":{"type":"string"}}}`, `{"a":1}`},
		{"a loop under a map of names in another case", `{"type":"object","required":["never"],"DependentSchemas":{"properties":{"$ref":"#"}}}`, `{"properties":1}`},
		{"a loop under draft-07 dependencies", `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","required":["never"],"dependencies":{"properties":{"$ref":"#"}}}`, `{"properties":1}`},
		{"a loop under properties in another case", `{"type":"object","required":["never"],"Properties":{"properties":{"$ref":"#"}}}`, `{"properties":1}`},
		{"a loop under $defs in another case", `{"type":"object","required":["never"],"$ref":"#/$defs/x","$defs":{"x":{"dependentschemas":{"$defs":{"$ref":"#"}}}}}`, `{"$defs":1}`},
		{"a recursive tree", `{"type":"object","required":["never"],"properties":{"kids":{"type":"array","items":{"$ref":"#"}}}}`, `{"kids":[]}`},
		{"a fan-out of 40 definitions", fanOutSchema(40), `{}`},
		{"a fan-out of 12 definitions", fanOutSchema(12), `{}`},
		{"a pattern", `{"type":"object","required":["never"],"properties":{"a":{"type":"string","pattern":"^a$"}}}`, `{}`},
		{"patternProperties", `{"type":"object","required":["never"],"patternProperties":{"^a":{"type":"string"}}}`, `{}`},
		{"a property named patternProperties", `{"type":"object","required":["never"],"properties":{"patternProperties":{"type":"string"}}}`, `{}`},
		{"2,000 patterns", patternsSchema(2000), `{}`},
		{"an $id at the top", `{"$id":"https://schemas.example/in","type":"object","required":["never"]}`, `{}`},
		{"an $anchor", `{"type":"object","required":["never"],"properties":{"a":{"$anchor":"a","type":"string"}}}`, `{}`},
		{"a reference with an escape", `{"type":"object","required":["never"],"properties":{"a":{"$ref":"#/$defs/a~1b"}},"$defs":{"a/b":{"type":"string"}}}`, `{}`},
		{"a reference with a percent sign", `{"type":"object","required":["never"],"properties":{"a":{"$ref":"#/%24defs/n"}},"$defs":{"n":{"type":"string"}}}`, `{}`},
		{"a definition under a very long name", `{"type":"object","required":["never"],"properties":{"a":{"$ref":"#/$defs/` + strings.Repeat("n", 60_000) + `"}},"$defs":{"` + strings.Repeat("n", 60_000) + `":{"if":{},"then":{"if":{},"then":{"type":"string"}}}}}`, `{}`},
		{"33 levels deep", `{"required":["never"],"a":` + strings.Repeat(`{"a":`, 32) + `1` + strings.Repeat(`}`, 32) + `}`, `{}`},
		{"a schema over the byte cap", `{"type":"object","required":["never"],"description":"` + strings.Repeat("d", schemaCheckMaxBytes) + `"}`, `{}`},
		{"a schema over the cost cap", `{"type":"object","required":["never"],"enum":[` + strings.Repeat(`0,`, schemaCheckMaxCost) + `0]}`, `{}`},
		{"arguments too heavy for the schema", `{"type":"object","required":["never"],"properties":{"a":{"type":"array","items":{"type":"integer"}}}}`, `{"a":[` + strings.Repeat(`0,`, schemaCheckMaxWork/8) + `0]}`},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := schemaMismatch(schemaJSON(t, row.schema), json.RawMessage(row.args)); got != "" {
				t.Errorf("schemaMismatch = %q, want the schema left unchecked", got)
			}
		})
	}
}

// TestSchemaMismatchChecksSchemasWithLocalReferences pins that the bounded
// walk does not cost the check on schemas servers really publish: local
// references into $defs, definitions and properties, and properties named
// like the keywords the walk refuses.
func TestSchemaMismatchChecksSchemasWithLocalReferences(t *testing.T) {
	t.Parallel()
	nested := `{"type":"object","required":["filter"],"properties":{"filter":{"$ref":"#/$defs/Filter"}},
		"$defs":{"Filter":{"type":"object","required":["field","op"],"properties":{"field":{"type":"string"},"op":{"$ref":"#/$defs/Op"},"sort":{"$ref":"#/$defs/Sort"}}},
		"Op":{"type":"string","enum":["eq","ne"]},
		"Sort":{"type":"object","required":["by"],"properties":{"by":{"type":"string"},"dir":{"$ref":"#/$defs/Op"}}}}}`
	rows := []struct {
		name   string
		schema string
		args   string
		want   string
	}{
		{"a nested model, valid", nested, `{"filter":{"field":"a","op":"eq","sort":{"by":"a"}}}`, ""},
		{"a nested model, a required property missing two references deep", nested, `{"filter":{"field":"a","op":"eq","sort":{}}}`, "by"},
		{"a nested model, a value outside a referenced enum", nested, `{"filter":{"field":"a","op":"like"}}`, "enum"},
		{"a reference into properties", `{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"},"b":{"$ref":"#/properties/a"}}}`, `{"a":"x","b":7}`, "b"},
		{"a reference into definitions", `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","required":["a"],"properties":{"a":{"$ref":"#/definitions/n"}},"definitions":{"n":{"type":"integer"}}}`, `{"a":"x"}`, "a"},
		{"a reference by an index", `{"type":"object","allOf":[{"required":["a"]}],"properties":{"b":{"$ref":"#/allOf/0"}}}`, `{"b":{}}`, "a"},
		{"a reference to a boolean definition", `{"type":"object","required":["x"],"properties":{"a":{"$ref":"#/$defs/any"}},"$defs":{"any":true}}`, `{"a":1}`, "x"},
		{"properties named like refused keywords", `{"type":"object","required":["pattern","$id","$anchor","$ref"],"properties":{"pattern":{"type":"string"},"$id":{"type":"string"},"$anchor":{"type":"string"},"$ref":{"type":"string"}}}`, `{"pattern":"^a"}`, "$id"},
		{"a property named $ref under dependentSchemas", `{"type":"object","dependentSchemas":{"$ref":{"required":["with"]}}}`, `{"$ref":"x"}`, "with"},
		{"a schema at the cost cap", `{"type":"object","required":["x"],"examples":[` + strings.Repeat(`0,`, schemaCheckMaxCost/3-10) + `0]}`, `{}`, "x"},
		{"one long string against a small schema", `{"type":"object","required":["x"],"properties":{"doc":{"type":"string"}}}`, `{"doc":"` + strings.Repeat("d", 256<<10) + `"}`, "x"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got := schemaMismatch(schemaJSON(t, row.schema), json.RawMessage(row.args))
			if row.want == "" && got != "" {
				t.Fatalf("schemaMismatch = %q, want the call to pass", got)
			}
			if row.want != "" && !strings.Contains(got, row.want) {
				t.Fatalf("schemaMismatch = %q, want a mismatch naming %q", got, row.want)
			}
		})
	}
}

// TestSchemaMismatchWorstCaseIsBounded runs, for each form that costs the
// validator most, every size of that form the walk accepts against the
// heaviest arguments the check still reads for it. The forms are: every
// branch fails and quotes a long enum, a failure deep under a long name or
// over a long constant, and many branches that each refuse and quote every
// key of the arguments under many levels, each of which copies the error
// once, and numeric bounds against numbers that an error writes out in
// hundreds of digits. It fails when one call allocates or takes more than a
// server and a caller should be able to buy with it.
func TestSchemaMismatchWorstCaseIsBounded(t *testing.T) {
	// branches builds n references to the definition e.
	branches := func(n int) string {
		return strings.TrimSuffix(strings.Repeat(`{"$ref":"#/$defs/e"},`, n), ",")
	}
	enum := func(size int) string { return `{"enum":["` + strings.Repeat("e", size) + `"]}` }
	// nest wraps inner in depth levels of open and close.
	nest := func(depth int, open, inner, end string) string {
		return strings.Repeat(open, depth) + inner + strings.Repeat(end, depth)
	}
	// numbers is a list of n numbers under the key a.
	numbers := func(n int) string {
		return `{"a":[` + strings.TrimSuffix(strings.Repeat(`1,`, n), ",") + `]}`
	}
	// keys is n properties whose 63-byte names are control characters,
	// which an error quotes at four bytes each.
	keys := func(n int) string {
		var b strings.Builder
		b.WriteString(`{`)
		for i := range n {
			fmt.Fprintf(&b, `"%s%05d":0,`, strings.Repeat(`\u0001`, 58), i)
		}
		return strings.TrimSuffix(b.String(), ",") + `}`
	}
	// tiny is n properties whose value is the smallest normal number, which
	// an error writes out as a fraction of several hundred digits.
	tiny := func(n int) string {
		var b strings.Builder
		b.WriteString(`{`)
		for i := range n {
			fmt.Fprintf(&b, `"%x":2.225073858507201e-308,`, i)
		}
		return strings.TrimSuffix(b.String(), ",") + `}`
	}
	rows := []struct {
		name   string
		schema func(k int) string // a schema of the form, larger with k
		args   func(n int) string // arguments of the form, heavier with n
	}{
		{"k branches quoting a long enum, no early exit", func(k int) string {
			return `{"type":"object","properties":{"a":{"type":"array","items":{"not":{"anyOf":[` + branches(k) + `]}}}},"$defs":{"e":` + enum(380) + `}}`
		}, numbers},
		{"k short branches, no early exit", func(k int) string {
			return `{"type":"object","properties":{"a":{"type":"array","items":{"not":{"anyOf":[` + branches(k) + `]}}}},"$defs":{"e":` + enum(8) + `}}`
		}, numbers},
		{"k branches that all pass", func(k int) string {
			return `{"type":"object","properties":{"a":{"type":"array","items":{"allOf":[` + branches(k) + `]}}},"$defs":{"e":{"type":"integer"}}}`
		}, numbers},
		{"one branch quoting an enum of 20k bytes", func(k int) string {
			return `{"type":"object","properties":{"a":{"type":"array","items":{"not":{"anyOf":[` + branches(1) + `]}}}},"$defs":{"e":` + enum(20*k) + `}}`
		}, numbers},
		{"a failure 24 levels deep under a name of 4k bytes", func(k int) string {
			name := strings.Repeat("n", 4*k)
			return `{"type":"object","properties":{"a":{"type":"array","contains":{"$ref":"#/$defs/` + name + `"}}},"$defs":{"` + name + `":` + nest(24, `{"if":{},"then":`, `{"type":"string"}`, `}`) + `}}`
		}, numbers},
		{"a constant of 10k bytes 28 levels deep", func(k int) string {
			return `{"type":"object","additionalProperties":{"contains":` + nest(28, `{"if":{},"then":`, `{"const":"`+strings.Repeat("c", 10*k)+`"}`, `}`) + `}}`
		}, numbers},
		{"k branches refusing every key, 14 levels deep", func(k int) string {
			refuse := strings.TrimSuffix(strings.Repeat(`{"additionalProperties":false},`, k), ",")
			return `{"type":"object",` + nest(14, `"allOf":[{`, `"anyOf":[`+refuse+`]`, `}]`) + `}`
		}, keys},
		{"k object variants refusing every key", func(k int) string {
			variant := `{"type":"object","properties":{"kind":{"const":"v"},"id":{"type":"string"}},"required":["kind"],"additionalProperties":false},`
			return `{"type":"object","anyOf":[` + strings.TrimSuffix(strings.Repeat(variant, k), ",") + `]}`
		}, keys},
		{"k sets of numeric bounds that all fail, against tiny numbers", func(k int) string {
			bounds := `{"minimum":1.7976931348623157e308},{"maximum":-1.7976931348623157e308},{"exclusiveMinimum":1.7976931348623157e308},{"exclusiveMaximum":-1.7976931348623157e308},`
			return `{"type":"object","additionalProperties":{"if":{"anyOf":[` + strings.TrimSuffix(strings.Repeat(bounds, k), ",") + `]}}}`
		}, tiny},
		{"k numeric bounds under contains, against tiny numbers", func(k int) string {
			bounds := strings.TrimSuffix(strings.Repeat(`{"minimum":1.7976931348623157e308},`, k), ",")
			return `{"type":"object","properties":{"a":{"type":"array","contains":{"anyOf":[` + bounds + `]}}}}`
		}, func(n int) string {
			return `{"a":[` + strings.TrimSuffix(strings.Repeat(`2.225073858507201e-308,`, n), ",") + `]}`
		}},
		{"a small schema against many values", func(int) string {
			return `{"type":"object","properties":{"a":{"type":"array","items":{"not":{"anyOf":[` + branches(2) + `]}}}},"$defs":{"e":` + enum(8) + `}}`
		}, numbers},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			measured, worst := 0, uint64(0)
			for _, k := range []int{3000, 1000, 400, 150, 70, 30, 10, 5, 2, 1} {
				schema := schemaJSON(t, row.schema(k))
				walk := schemaWalk{root: schema.(map[string]any)}
				if !walk.value(walk.root, 1, 0) {
					continue
				}
				// The heaviest arguments of the form that the work cap lets through.
				n, args := 0, ""
				for step := 1 << 14; step > 0; step /= 2 {
					try := row.args(n + step)
					if walk.cost*argumentWeight(schemaJSON(t, try)) <= schemaCheckMaxWork {
						n, args = n+step, try
					}
				}
				if n == 0 {
					continue
				}
				measured++
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				start := time.Now()
				got := schemaMismatch(schema, json.RawMessage(args))
				took := time.Since(start)
				runtime.ReadMemStats(&after)
				allocated := (after.TotalAlloc - before.TotalAlloc) >> 20
				worst = max(worst, allocated)
				if allocated > 64 || took > 5*time.Second {
					t.Errorf("size %d at cost %d with %d items of %d bytes: the check allocated %d MiB in %v and answered %d bytes, want at most 64 MiB and well under 5s",
						k, walk.cost, n, len(args), allocated, took, len(got))
				}
			}
			if measured == 0 {
				t.Fatalf("the walk or the work cap refuses every size, so the row measures nothing")
			}
			t.Logf("%d sizes measured, the costliest allocated %d MiB", measured, worst)
		})
	}
}

// TestArgumentCheckRunsFewAtOnce pins the bound on how many argument checks
// run in the validator at one time: with every slot taken a check answers
// as one that cannot judge, and with a slot free it reports the mismatch.
func TestArgumentCheckRunsFewAtOnce(t *testing.T) {
	// Serial: it fills the package's slots, which every gated call shares.
	schema := map[string]any{"type": "object", "required": []any{"path"}}
	for range cap(schemaCheckSlots) {
		schemaCheckSlots <- struct{}{}
	}
	got := boundedSchemaMismatch(schema, json.RawMessage(`{}`))
	for range cap(schemaCheckSlots) {
		<-schemaCheckSlots
	}
	if got != "" {
		t.Errorf("with every slot taken the check answered %q, want it to leave the call unchecked", got)
	}
	if got := boundedSchemaMismatch(schema, json.RawMessage(`{}`)); !strings.Contains(got, "path") {
		t.Errorf("with a slot free the check answered %q, want the mismatch naming path", got)
	}
	if len(schemaCheckSlots) != 0 {
		t.Errorf("%d slots stay taken after the checks, want none", len(schemaCheckSlots))
	}
}
