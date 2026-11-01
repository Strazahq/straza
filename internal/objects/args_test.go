package objects

import (
	"encoding/json"
	"strings"
	"testing"
)

// g4 is the unreadable-arguments sentence of pr_create on ghapp with fault.
func g4(fault string) string {
	return "pr_create's arguments could not be read the same way by Straza and by ghapp: " + fault +
		". Send each argument once, in one JSON object, then try again."
}

const (
	g2Repo = "pr_create needs the repo it works on in its argument repo, and this call carries none. Send repo as a string, then try again."
	g3Repo = "pr_create needs the repo in its argument repo as a string, and this call sends another type. Send repo as a string, then try again."
	g5Repo = "pr_create's arguments carry both repo and Repo, and ghapp might read either. Send repo once, as its input schema spells it, then try again."
)

// TestArgValue pins D7: the order of the argument checks, the value each
// readable call yields and the sentence of every fault.
func TestArgValue(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 130) + `1` + strings.Repeat(`}`, 130)
	cases := []struct {
		name, raw, value, refusal string
	}{
		{"a plain string", `{"repo":"Strazahq/site"}`, "Strazahq/site", ""},
		{"the justification aside", `{"repo":"Strazahq/site","_straza_justification":"needed"}`, "Strazahq/site", ""},
		{"other arguments of any type", `{"title":"x","n":12345678901234567890,"d":0.10000000000000000000001,"repo":"a"}`, "a", ""},
		{"an escaped value", `{"repo":"Strazahq\/siteé"}`, "Strazahq/siteé", ""},
		{"bytes that are not UTF-8", "{\"repo\":\"a\xff\"}", "", g4("they are not valid UTF-8")},
		{"a lone surrogate escape", `{"repo":"a\ud800"}`, "", g4("they are not valid UTF-8")},
		{"a key twice at the top", `{"repo":"a","repo":"b"}`, "", g4("a key appears twice")},
		{"a key twice deep down", `{"repo":"a","x":[{"k":1,"k":2}]}`, "", g4("a key appears twice")},
		{"data after the object", `{"repo":"a"} {}`, "", g4("there is data after the arguments object")},
		{"an array", `["repo"]`, "", g4("they are not a JSON object")},
		{"a string", `"repo"`, "", g4("they are not a JSON object")},
		{"nesting past 128", deep, "", g4("they nest deeper than 128 levels")},
		{"broken JSON", `{"repo":`, "", g4("they are not valid JSON")},
		{"a case twin", `{"repo":"a","Repo":"b"}`, "", g5Repo},
		{"a case twin first", `{"Repo":"b","repo":"a"}`, "", g5Repo},
		{"only the twin", `{"Repo":"a"}`, "", g2Repo},
		{"no arguments", ``, "", g2Repo},
		{"null arguments", `null`, "", g2Repo},
		{"an empty object", `{}`, "", g2Repo},
		{"a number", `{"repo":5}`, "", g3Repo},
		{"null", `{"repo":null}`, "", g3Repo},
		{"a list", `{"repo":["a"]}`, "", g3Repo},
		{"an object", `{"repo":{"name":"a"}}`, "", g3Repo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, refusal := ArgValue("pr_create", "ghapp", "repo", "repo", json.RawMessage(tc.raw))
			if v != tc.value || refusal != tc.refusal {
				t.Errorf("ArgValue(%s)\n got (%q, %q)\nwant (%q, %q)", tc.raw, v, refusal, tc.value, tc.refusal)
			}
		})
	}
}

// TestArgValueTwinNames pins that G5 names the twin key as the call
// spells it.
func TestArgValueTwinNames(t *testing.T) {
	_, refusal := ArgValue("pr_create", "ghapp", "repo", "repo", json.RawMessage(`{"repo":"a","REPO":"b"}`))
	want := "pr_create's arguments carry both repo and REPO, and ghapp might read either. Send repo once, as its input schema spells it, then try again."
	if refusal != want {
		t.Errorf("refusal = %q, want %q", refusal, want)
	}
}

// FuzzArgValue checks that ArgValue never panics and never answers a value
// that Go's own decoder would not read from the same bytes.
func FuzzArgValue(f *testing.F) {
	for _, seed := range []string{`{"repo":"a"}`, `{"repo":"a","Repo":"b"}`, `{"repo":"\ud800"}`,
		"{\"repo\":\"\xff\"}", `{"repo":1}`, `{"repo":"a"} x`, `[]`, ``, `{"repo":"a","x":{"y":{"y":1}}}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		v, refusal := ArgValue("pr_create", "ghapp", "repo", "repo", raw)
		if refusal != "" {
			if v != "" {
				t.Fatalf("a refusal answered the value %q", v)
			}
			return
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("answered %q for arguments Go does not decode: %v", v, err)
		}
		var s string
		if err := json.Unmarshal(m["repo"], &s); err != nil || s != v {
			t.Fatalf("answered %q, Go's decoder reads %q (%v)", v, s, err)
		}
	})
}
