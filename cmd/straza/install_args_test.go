package main

import (
	"strings"
	"testing"
)

// TestHarnessArgs pins the install/uninstall operand contract: one canonical
// form, positional operands, no silent default. `straza install codex` wires
// codex. A missing operand errors NAMING the valid harnesses, because a
// default would silently wire the wrong harness. An unknown name fails the
// whole run before any file is touched.
func TestHarnessArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr string
	}{
		{name: "single operand", args: []string{"codex"}, want: []string{"codex"}},
		{name: "multiple operands", args: []string{"codex", "gemini"},
			want: []string{"codex", "gemini"}},
		{name: "no operand errors with the valid names", args: nil,
			wantErr: "specify at least one harness: claude-code, codex, gemini"},
		{name: "unknown harness fails before wiring", args: []string{"kodex"},
			wantErr: `no installer for harness "kodex"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := harnessArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("harnesses = %v, want %v", got, tt.want)
			}
		})
	}
}
