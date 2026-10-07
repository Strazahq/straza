package main

import (
	"strings"
	"testing"
)

const splitDocA = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: set-a}
spec:
  rules: [{id: r1, effect: deny, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}, reason: "no"}]
`

const splitDocB = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: set-b}
spec:
  rules: [{id: r1, effect: allow, tools: [file.read]}]
`

// TestSplitPolicyDocs pins multi-doc apply: every document in a multi-doc
// file must reach the server as its own request, byte-exact, and anything
// the line splitter cannot reconcile with the parser's document count fails
// loudly instead of half-applying.
func TestSplitPolicyDocs(t *testing.T) {
	t.Run("single doc passes through untouched", func(t *testing.T) {
		chunks, err := splitPolicyDocs([]byte(splitDocA), 1)
		if err != nil || len(chunks) != 1 || string(chunks[0]) != splitDocA {
			t.Fatalf("chunks=%d err=%v", len(chunks), err)
		}
	})

	t.Run("two docs split on the separator, bytes preserved", func(t *testing.T) {
		raw := splitDocA + "---\n" + splitDocB
		chunks, err := splitPolicyDocs([]byte(raw), 2)
		if err != nil || len(chunks) != 2 {
			t.Fatalf("chunks=%d err=%v", len(chunks), err)
		}
		if string(chunks[0]) != splitDocA || string(chunks[1]) != splitDocB {
			t.Errorf("split did not preserve document bytes:\n[0]=%q\n[1]=%q", chunks[0], chunks[1])
		}
	})

	t.Run("leading separator and comments survive", func(t *testing.T) {
		raw := "---\n# first set\n" + splitDocA + "---\n# second set\n" + splitDocB
		chunks, err := splitPolicyDocs([]byte(raw), 2)
		if err != nil || len(chunks) != 2 {
			t.Fatalf("chunks=%d err=%v", len(chunks), err)
		}
		if !strings.HasPrefix(string(chunks[0]), "# first set") || !strings.HasPrefix(string(chunks[1]), "# second set") {
			t.Errorf("comments lost: [0]=%q [1]=%q", chunks[0], chunks[1])
		}
	})

	t.Run("chunk/parse count mismatch refuses", func(t *testing.T) {
		if _, err := splitPolicyDocs([]byte(splitDocA+"---\n"+splitDocB), 3); err == nil {
			t.Fatal("mismatched document count must refuse, not guess")
		}
	})
}
