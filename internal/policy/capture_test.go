package policy

import (
	"strings"
	"testing"
)

// Conversation capture is opted in by a policy set: a PolicySet-level
// `capture:` block bound by the set's match selectors.

func captureDoc(t *testing.T, name, match, capture string) Document {
	t.Helper()
	yaml := `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: ` + name + `
spec:
` + match + capture + `  rules:
    - id: placeholder
      effect: deny
      tools: [shell.exec]
`
	doc, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc
}

func TestParseCaptureBlock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		capture string
		wantErr string
	}{
		{"conversations only", "  capture:\n    conversations: true\n", ""},
		{"verbatim mode", "  capture:\n    conversations: true\n    mode: verbatim\n", ""},
		{"redact mode", "  capture:\n    conversations: true\n    mode: redact\n", ""},
		{"explicit off", "  capture:\n    conversations: false\n", ""},
		{"bad mode", "  capture:\n    conversations: true\n    mode: sanitize\n", "capture.mode"},
		{"mode without conversations", "  capture:\n    mode: redact\n", "capture.mode without"},
	} {
		yaml := `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: cap-parse
spec:
` + tc.capture + `  rules:
    - id: r1
      effect: deny
      tools: [shell.exec]
`
		_, err := Parse([]byte(yaml))
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: error = %v, want mention of %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestCaptureDirective(t *testing.T) {
	devOn := captureDoc(t, "dev-capture", "  match:\n    roles: [dev]\n", "  capture:\n    conversations: true\n")
	devRedact := captureDoc(t, "dev-redact", "  match:\n    roles: [dev]\n", "  capture:\n    conversations: true\n    mode: redact\n")
	opsOn := captureDoc(t, "ops-capture", "  match:\n    roles: [ops]\n", "  capture:\n    conversations: true\n    mode: verbatim\n")
	plain := captureDoc(t, "plain", "", "")
	off := captureDoc(t, "explicit-off", "  match:\n    roles: [dev]\n", "  capture:\n    conversations: false\n")

	dev := Subject{User: "bob", Roles: []string{"dev"}}
	ops := Subject{User: "kim", Roles: []string{"ops"}}
	other := Subject{User: "zoe", Roles: []string{"viewer"}}

	for _, tc := range []struct {
		name     string
		docs     []Document
		sub      Subject
		wantOn   bool
		wantMode string
	}{
		{"no capture blocks anywhere", []Document{plain}, dev, false, ""},
		{"matched set enables, default mode verbatim", []Document{devOn, plain}, dev, true, CaptureModeVerbatim},
		{"unmatched subject stays uncaptured", []Document{devOn}, other, false, ""},
		{"role-scoped: ops set does not capture dev", []Document{opsOn}, dev, false, ""},
		{"explicit redact", []Document{devRedact}, dev, true, CaptureModeRedact},
		{"redact wins over verbatim when both match", []Document{devOn, devRedact}, dev, true, CaptureModeRedact},
		{"conversations false does not enable", []Document{off}, dev, false, ""},
		{"ops still verbatim when dev is redacted", []Document{devRedact, opsOn}, ops, true, CaptureModeVerbatim},
	} {
		eng, err := NewEngine(tc.docs, EffectDeny)
		if err != nil {
			t.Fatalf("%s: engine: %v", tc.name, err)
		}
		d := eng.Capture(tc.sub)
		if d.Conversations != tc.wantOn {
			t.Errorf("%s: conversations = %v, want %v", tc.name, d.Conversations, tc.wantOn)
		}
		if tc.wantOn && d.Mode != tc.wantMode {
			t.Errorf("%s: mode = %q, want %q", tc.name, d.Mode, tc.wantMode)
		}
	}
}
