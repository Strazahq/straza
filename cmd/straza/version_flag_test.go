package main

import (
	"strings"
	"testing"
)

// TestVersionFlagNamesTheSubcommand pins the answer to `straza --version`:
// the one canonical form is `straza version`, so the unknown-flag error
// names it, and every other unknown flag keeps cobra's own wording.
func TestVersionFlagNamesTheSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"version flag", []string{"--version"}, "unknown flag: --version. Run `straza version` to print the version"},
		{"other unknown flag", []string{"--bogus"}, "unknown flag: --bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := rootCmd()
			root.SetArgs(tt.args)
			err := root.Execute()
			if err == nil || err.Error() != tt.want {
				t.Errorf("straza %s: error = %v, want %q", strings.Join(tt.args, " "), err, tt.want)
			}
		})
	}
}
