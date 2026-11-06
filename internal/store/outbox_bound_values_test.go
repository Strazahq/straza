package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestCountValues pins the cheap count the value bound reads: one more
// than the commas, brackets and braces outside strings, never fewer than
// the values the text holds.
func TestCountValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in string
		want     int
	}{
		{"a scalar", `1`, 1},
		{"an empty array", `[]`, 2},
		{"an array of three", `[1,2,3]`, 4},
		{"an object of two", `{"a":1,"b":2}`, 3},
		{"punctuation inside strings is not counted", `{"a":"x,[{\"","b":[1,{}]}`, 6},
		{"an escaped backslash ends no string early", `["\\",","]`, 3},
		{"a nested object", `{"data":{"paths":["a","b"]}}`, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := CountValues(c.in); got != c.want {
				t.Fatalf("CountValues(%s) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// fullOmit is the record boundCE gave a record over the bound before the
// value bound, from a full decode: its envelope, its kept data fields and
// dataOmitted.
func fullOmit(t *testing.T, ce string) string {
	t.Helper()
	env := decodeBound(t, ce)
	return omitData(env, keptOf(env["data"]), ce)
}

// TestBoundCEOmitsARecordOfManyValuesWithoutDecodingIt pins the value
// bound: a record over MaxRecordBytes that holds more than MaxRecordValues
// values comes out exactly as a full decode would have omitted it, with a
// few hundred allocations where a full decode makes one or more per value.
// A record that is not a JSON object is still answered whole. It runs
// serially, because testing.AllocsPerRun refuses a parallel test.
func TestBoundCEOmitsARecordOfManyValuesWithoutDecodingIt(t *testing.T) {
	kept := map[string]any{
		"command": "rm -rf build", "session": "0198a3c1-0000-7000-8000-000000000001", "user": "kim",
		"tool": "shell.exec", "effect": "allow", "ruleId": "r1", "workspace": strings.Repeat("w", 1000),
		"truncated": true, "count": 7,
	}
	withData := func(extra map[string]any) map[string]any {
		data := map[string]any{}
		for k, v := range kept {
			data[k] = v
		}
		for k, v := range extra {
			data[k] = v
		}
		return data
	}
	cases := []struct {
		name string
		ce   func(*testing.T) string
	}{
		{"many numbers in a kept key", func(t *testing.T) string {
			return recordOf(t, withData(map[string]any{"arguments": make([]int, 400000)}))
		}},
		{"many short strings", func(t *testing.T) string {
			return recordOf(t, withData(map[string]any{"paths": strings.Split(strings.Repeat("a,", 200000), ",")}))
		}},
		{"a wide data object", func(t *testing.T) string {
			extra := map[string]any{}
			for i := range 100000 {
				extra[fmt.Sprintf("k%d", i)] = 1
			}
			return recordOf(t, withData(extra))
		}},
		{"data that is an array", func(t *testing.T) string {
			return recordOf(t, make([]int, 400000))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.ce(t)
			if len(in) <= MaxRecordBytes || CountValues(in) <= MaxRecordValues {
				t.Fatalf("the record is %d bytes of %d values, want it over both bounds", len(in), CountValues(in))
			}
			out := boundCE(in)
			if want := fullOmit(t, in); out != want {
				t.Fatalf("bounded record\n%.600s\nwant the full decode's\n%.600s", out, want)
			}
			if len(out) > MaxRecordBytes {
				t.Fatalf("bounded record is %d bytes, over %d", len(out), MaxRecordBytes)
			}
			if allocs := testing.AllocsPerRun(2, func() { boundCE(in) }); allocs > 2000 {
				t.Fatalf("bounding the record took %.0f allocations, want a few hundred, so it decoded every value", allocs)
			}
		})
	}
	t.Run("a record that is not an object", func(t *testing.T) {
		in := "[" + strings.Repeat("1,", 200000) + "1]"
		if out := boundCE(in); out != in {
			t.Fatalf("a record that is not an object came out as %.200s, want it whole", out)
		}
	})
}

// TestBoundCEStillCutsARecordOfFewValues pins that the value bound leaves
// the common case alone: a long record of few values keeps its strings cut
// to a common length, not omitted.
func TestBoundCEStillCutsARecordOfFewValues(t *testing.T) {
	t.Parallel()
	in := recordOf(t, map[string]any{"command": strings.Repeat("x", 400<<10), "paths": make([]int, 1000)})
	out := decodeBound(t, boundCE(in))
	data, _ := out["data"].(map[string]any)
	if _, omitted := data["dataOmitted"]; omitted || len(data["command"].(string)) < 200<<10 {
		t.Fatalf("a record of %d values lost its data: %.300v", CountValues(in), data)
	}
}

// TestBoundCEExported pins that BoundCE is the bound every write applies,
// so a producer that bounds early gets the record the write would store.
func TestBoundCEExported(t *testing.T) {
	t.Parallel()
	in := recordOf(t, map[string]any{"command": strings.Repeat("<", 300<<10)})
	if BoundCE(in) != boundCE(in) {
		t.Fatal("BoundCE answers another record than boundCE")
	}
	if out := BoundCE(in); boundCE(out) != out {
		t.Fatal("a record BoundCE answered is cut again by the write's own bound")
	}
	var v map[string]any
	if json.Unmarshal([]byte(BoundCE(in)), &v) != nil {
		t.Fatal("BoundCE answered a record that is not JSON")
	}
}
