package spine

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestChainForm pins the rev-16 storage rule: capture events chain
// content-free (contentBytes added, contentHash kept), everything else
// chains verbatim.
func TestChainForm(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		stripped bool
	}{
		{"prompt stripped", `{"id":"a","type":"straza.audit.prompt","data":{"content":"secret text","contentHash":"sha256:x","mode":"verbatim"}}`, true},
		{"reply stripped", `{"id":"b","type":"straza.audit.reply","data":{"content":"reply text","contentHash":"sha256:y"}}`, true},
		{"tool verbatim", `{"id":"c","type":"straza.audit.tool","data":{"command":"rm -rf /tmp/x"}}`, false},
		{"prompt without content verbatim", `{"id":"d","type":"straza.audit.prompt","data":{"contentHash":"sha256:z"}}`, false},
		{"unparseable verbatim", `not json at all`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := chainForm([]byte(tc.in))
			if !tc.stripped {
				if out != tc.in {
					t.Fatalf("changed a non-capture event:\n in: %s\nout: %s", tc.in, out)
				}
				return
			}
			var ce struct {
				ID   string         `json:"id"`
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(out), &ce); err != nil {
				t.Fatalf("stored form unparseable: %v", err)
			}
			if _, has := ce.Data["content"]; has {
				t.Fatal("data.content survived into the chain")
			}
			if strings.Contains(out, "secret text") || strings.Contains(out, "reply text") {
				t.Fatal("content text leaked into the stored bytes")
			}
			if _, has := ce.Data["contentHash"]; !has {
				t.Fatal("contentHash lost; the witness must survive")
			}
			n, ok := ce.Data["contentBytes"].(float64)
			if !ok || n <= 0 {
				t.Fatalf("contentBytes = %v, want the original content length", ce.Data["contentBytes"])
			}
			if ce.ID == "" || ce.Type == "" {
				t.Fatal("envelope fields lost")
			}
		})
	}
}
