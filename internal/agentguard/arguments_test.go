package agentguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCutArgumentsKeepsEveryMember pins how a record keeps an input over
// ArgumentsMax: every member survives, the longest strings share one
// length and end with their whole length, the kept text is JSON within the
// bound, and the hash is of the whole text. A long value before a short
// one in key order, such as a skill's arguments before its name, cannot
// push the short one out.
func TestCutArgumentsKeepsEveryMember(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n/2) }
	rows := []struct {
		name string
		in   map[string]any
		// whole lists the members that must come back unchanged.
		whole []string
		cut   []string
	}{
		{"a skill's long arguments keep its name", map[string]any{"args": long(9000), "skill": "deploy"}, []string{"skill"}, []string{"args"}},
		{"a long write keeps its path", map[string]any{"content": long(20000), "file_path": "/w/secret.env"}, []string{"file_path"}, []string{"content"}},
		{"two long strings share the room", map[string]any{"new_string": long(9000), "old_string": long(30000), "replace_all": true}, []string{"replace_all"}, []string{"new_string", "old_string"}},
		{"a short string beside a long one stays whole", map[string]any{"description": "sweep", "prompt": long(50000), "n": json.Number("9007199254740993")}, []string{"description", "n"}, []string{"prompt"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			text, err := json.Marshal(row.in)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(text)
			kept, hash := CutArguments(text)
			if len(kept) > ArgumentsMax || len(kept) < ArgumentsMax-64 || hash != "sha256:"+hex.EncodeToString(sum[:]) {
				t.Fatalf("kept %d bytes with hash %q, want close to the bound of %d and the whole text's hash", len(kept), hash, ArgumentsMax)
			}
			var got map[string]any
			dec := json.NewDecoder(strings.NewReader(kept))
			dec.UseNumber()
			if err := dec.Decode(&got); err != nil || len(got) != len(row.in) {
				t.Fatalf("the kept text decodes to %d members (%v), want all %d", len(got), err, len(row.in))
			}
			for _, k := range row.whole {
				if got[k] != row.in[k] {
					t.Errorf("%s = %v, want it unchanged as %v", k, got[k], row.in[k])
				}
			}
			var lens []int
			for _, k := range row.cut {
				s, whole := got[k].(string), row.in[k].(string)
				mark := fmt.Sprintf("... (%d bytes)", len(whole))
				prefix, found := strings.CutSuffix(s, mark)
				if !found || !utf8.ValidString(prefix) || !strings.HasPrefix(whole, prefix) {
					t.Errorf("%s ends %q, want a prefix of the value followed by %q", k, s[max(0, len(s)-24):], mark)
				}
				lens = append(lens, len(prefix))
			}
			for _, n := range lens {
				if n < lens[0]-1 || n > lens[0]+1 {
					t.Errorf("the cut strings keep %v bytes, want one common length", lens)
				}
			}
		})
	}
}

// TestCutArgumentsBounds pins the edges: a text at the bound is kept whole
// with no hash, and a text that is not JSON or that cannot fit with every
// string cut keeps its first bytes, cut on a character boundary.
func TestCutArgumentsBounds(t *testing.T) {
	atBound := `{"q":"` + strings.Repeat("a", ArgumentsMax-len(`{"q":""}`)) + `"}`
	many := map[string]any{}
	for i := range 2000 {
		many[fmt.Sprintf("k%d", i)] = i
	}
	manyText, _ := json.Marshal(many)
	rows := []struct {
		name, text string
		prefix     bool // the kept text is a plain prefix of the text
	}{
		{"a text at the bound stays whole", atBound, false},
		{"a text that is not JSON keeps its first bytes", strings.Repeat("é", ArgumentsMax), true},
		{"a text of too many members keeps its first bytes", string(manyText), true},
		{"a text of more values than can fit is not decoded", "[" + strings.Repeat("0,", ArgumentsMax) + "0]", true},
		{"a value followed by more text is not one JSON value", `{"skill":"deploy"}` + strings.Repeat(" ", ArgumentsMax) + `{"skill":"other"}`, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			kept, hash := CutArguments([]byte(row.text))
			if !row.prefix {
				if kept != row.text || hash != "" {
					t.Fatalf("kept %d of %d bytes with hash %q, want the whole text and no hash", len(kept), len(row.text), hash)
				}
				return
			}
			sum := sha256.Sum256([]byte(row.text))
			if !utf8.ValidString(kept) || !strings.HasPrefix(row.text, kept) || len(kept) > ArgumentsMax || len(kept) < ArgumentsMax-3 ||
				hash != "sha256:"+hex.EncodeToString(sum[:]) {
				t.Fatalf("kept %d bytes with hash %q, want a valid prefix at the bound and the whole text's hash", len(kept), hash)
			}
		})
	}
}

// TestHoldsDraft pins which tool names keep no arguments on a tool record:
// the gateway's draft_submit and approval_request as a harness names them
// under another server name, and no tool of another server, whatever it
// is called.
func TestHoldsDraft(t *testing.T) {
	for name, want := range map[string]bool{
		"straza__draft_submit":            true,
		"straza__approval_request":        true,
		"mcp_gw_straza__draft_submit":     true,
		"mcp_gw_straza__approval_request": true,
		"draft_submit":                    false,
		"approval_request":                false,
		"jira__approval_request":          false,
		"straza__draft_status":            false,
		"form_input":                      false,
		"":                                false,
	} {
		if got := HoldsDraft(name); got != want {
			t.Errorf("HoldsDraft(%q) = %v, want %v", name, got, want)
		}
	}
}
