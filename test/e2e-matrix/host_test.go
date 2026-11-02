package e2ematrix

import (
	"strings"
	"testing"
)

// TestHostGuardRefusesASystemService pins that the lane stops before it
// spawns anything on a machine that runs the Straza system service, where
// the clients ignore STRAZA_HOME and would replace the key and enrollment
// of the account running the lane.
func TestHostGuardRefusesASystemService(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		present bool
		want    string
	}{
		{"no system service", false, ""},
		{"a system service", true, "Nothing ran. Run the lane in a container or on a machine without a Straza install"},
	} {
		err := hostGuard(tc.present)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: got %v, want a refusal holding %q", tc.name, err, tc.want)
		}
	}
}
