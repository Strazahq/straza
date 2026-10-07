package main

import (
	"bytes"
	"fmt"

	policyengine "github.com/strazahq/straza/internal/policy"
)

// splitPolicyDocs splits multi-document PolicySet YAML into one chunk per
// document on standalone `---` separator lines, preserving each document's
// bytes (comments and formatting included) for storage as that set's source.
// want is the document count ParseAll reported for the whole file; the split
// refuses to guess when the two disagree (a column-0 `---` inside scalar
// content would fool a line splitter), failing loudly instead of letting a
// file be half-applied. A single-document file passes through untouched.
func splitPolicyDocs(raw []byte, want int) ([][]byte, error) {
	if want <= 1 {
		return [][]byte{raw}, nil
	}
	var chunks [][]byte
	var cur []byte
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		if string(bytes.TrimRight(line, "\r\n")) == "---" {
			if len(bytes.TrimSpace(cur)) > 0 {
				chunks = append(chunks, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, line...)
	}
	if len(bytes.TrimSpace(cur)) > 0 {
		chunks = append(chunks, cur)
	}
	if len(chunks) != want {
		return nil, fmt.Errorf("cannot split the file cleanly (%d chunks vs %d parsed documents): apply the documents from separate files", len(chunks), want)
	}
	for i, c := range chunks {
		docs, err := policyengine.ParseAll(c)
		if err != nil || len(docs) != 1 {
			return nil, fmt.Errorf("document %d does not stand alone after splitting: apply the documents from separate files", i+1)
		}
	}
	return chunks, nil
}
