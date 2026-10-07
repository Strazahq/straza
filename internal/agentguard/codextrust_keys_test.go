package agentguard

import (
	"reflect"
	"testing"
)

func TestCodexQuotedKeyPath(t *testing.T) {
	cases := []struct {
		name, key string
		want      []string
	}{
		{"bare", `hooks.state`, []string{"hooks", "state"}},
		{"unix path", `hooks.state."/home/a/.codex/hooks.json:stop:0:0"`, []string{"hooks", "state", "/home/a/.codex/hooks.json:stop:0:0"}},
		{"windows basic string", `hooks.state."C:\\Users\\a\\.codex\\hooks.json:stop:0:0"`, []string{"hooks", "state", `C:\Users\a\.codex\hooks.json:stop:0:0`}},
		{"windows literal string", `hooks.state.'C:\Users\a\.codex\hooks.json:stop:0:0'`, []string{"hooks", "state", `C:\Users\a\.codex\hooks.json:stop:0:0`}},
		{"escaped quote", `hooks.state."a\"b.json:stop:0:0"`, []string{"hooks", "state", `a"b.json:stop:0:0`}},
		{"quoted whitespace", ` " a " . state `, []string{" a ", "state"}},
		{"unicode escape", `hooks.state."\U0001F600.json"`, []string{"hooks", "state", "😀.json"}},
		{"unterminated", `hooks.state."C:\\Users`, nil},
		{"invalid escape", `hooks.state."C:\Users\a"`, nil},
		{"trailing text", `hooks.state."key"other`, nil},
		{"empty bare segment", `hooks..state`, nil},
		{"go-only escape", `hooks.state."\x61"`, nil},
		{"json-only escape", `hooks.state."\/etc"`, nil},
		{"unicode surrogate", `hooks.state."\uD800"`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexQuotedKeyPath(tc.key); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("codexQuotedKeyPath(%q) = %#v, want %#v", tc.key, got, tc.want)
			}
		})
	}
}
