package approval

import (
	"strings"
	"testing"
)

// The canonicalizer is crypto-adjacent (its output is hashed into the
// fingerprint a human decision binds to), so it is written test-first.
// The contract is JCS-style (sorted keys, no inter-token
// whitespace, deterministic string escaping, normalized numbers), with the
// injected justification field dropped at the top level, duplicate keys and
// over-deep nesting REJECTED (ambiguity fails toward deny, never toward a
// collision), and precision-lossy number rewrites forbidden.
func TestCanonicalArgs(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string // canonical bytes, or "" when wantErr
		wantErr bool
	}{
		{"empty input", "", "{}", false},
		{"empty object", "{}", "{}", false},
		{"null", "null", "{}", false},
		{"whitespace collapsed", "{ \"a\" : 1 ,\n\t\"b\" : \"x\" }", `{"a":1,"b":"x"}`, false},
		{"keys sorted", `{"b":1,"a":2}`, `{"a":2,"b":1}`, false},
		{"nested keys sorted", `{"z":{"b":1,"a":[{"y":1,"x":2}]}}`, `{"z":{"a":[{"x":2,"y":1}],"b":1}}`, false},
		{"array order preserved", `{"a":[3,1,2]}`, `{"a":[3,1,2]}`, false},
		{"float integral collapses", `{"a":1.0}`, `{"a":1}`, false},
		{"exponent expands", `{"a":1e3}`, `{"a":1000}`, false},
		{"negative exponent", `{"a":5e-1}`, `{"a":0.5}`, false},
		{"plain fraction stable", `{"a":0.5}`, `{"a":0.5}`, false},
		{"int stays int", `{"a":42}`, `{"a":42}`, false},
		{"large int64 exact", `{"a":9007199254740993}`, `{"a":9007199254740993}`, false},
		{"minus zero float collapses", `{"a":-0.0}`, `{"a":0}`, false},
		{"huge literal kept verbatim", `{"a":123456789012345678901234567890}`, `{"a":123456789012345678901234567890}`, false},
		{"bool and null values", `{"a":true,"b":null}`, `{"a":true,"b":null}`, false},
		{"unicode string round-trips", `{"a":"hélloA"}`, `{"a":"hélloA"}`, false},
		{"justification dropped top-level", `{"_straza_justification":"why","a":1}`, `{"a":1}`, false},
		{"justification kept nested", `{"a":{"_straza_justification":"data"}}`, `{"a":{"_straza_justification":"data"}}`, false},
		{"justification only", `{"_straza_justification":"why"}`, "{}", false},
		{"duplicate keys rejected", `{"a":1,"a":2}`, "", true},
		{"nested duplicate keys rejected", `{"x":{"a":1,"a":2}}`, "", true},
		{"malformed rejected", `{"a":`, "", true},
		{"trailing garbage rejected", `{"a":1} extra`, "", true},
		{"non-object rejected", `[1,2]`, "", true},
		{"scalar rejected", `"str"`, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalArgs([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CanonicalArgs(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalArgs(%q): %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Errorf("CanonicalArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Idempotence: canonical form canonicalizes to itself.
			again, err := CanonicalArgs(got)
			if err != nil || string(again) != string(got) {
				t.Errorf("not idempotent: %q → %q (err %v)", got, again, err)
			}
		})
	}
}

// TestCanonicalArgsDepthCap pins the fail-closed depth bound: a payload nested
// past the cap is rejected (→ the PEP denies), never hashed part-read.
func TestCanonicalArgsDepthCap(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 200) + "1" + strings.Repeat("}", 200)
	if _, err := CanonicalArgs([]byte(deep)); err == nil {
		t.Fatal("200-deep payload canonicalized, want depth error")
	}
	ok := strings.Repeat(`{"a":`, 100) + "1" + strings.Repeat("}", 100)
	if _, err := CanonicalArgs([]byte(ok)); err != nil {
		t.Fatalf("100-deep payload rejected: %v", err)
	}
}

// TestCanonicalArgsConvergence pins the reason the canonicalizer exists: the
// same logical call serialized by different producers (gateway raw bytes, the
// hook lane's client re-marshal, a model-authored native action) must hash
// identically, and a justification injected on one lane must not split keys.
func TestCanonicalArgsConvergence(t *testing.T) {
	variants := []string{
		`{"user":"alice","force":true,"count":2}`,
		`{"force":true,"count":2,"user":"alice"}`,
		"{ \"count\" : 2.0 ,\n \"force\" : true , \"user\" : \"alice\" }",
		`{"_straza_justification":"cleanup run","count":2,"force":true,"user":"alice"}`,
	}
	first, err := CanonicalArgs([]byte(variants[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range variants[1:] {
		got, err := CanonicalArgs([]byte(v))
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if string(got) != string(first) {
			t.Errorf("variant %q canonicalized to %q, want %q", v, got, first)
		}
	}
}
