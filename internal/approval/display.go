package approval

import (
	"fmt"
	"unicode/utf8"
)

// displayMax is the most bytes of a summary or a justification that an
// approval stores, shows and records. The console and the phone show the
// whole text and a push names only the request, so 4 KiB is more than a
// person reads at a decision, and it bounds the request record, which
// escapes each <, > and & into six bytes.
const displayMax = 4 << 10

// clampDisplay answers s whole up to displayMax bytes, and past that cut on
// a rune boundary and followed by "... (<n> bytes)", where n is the full
// length of s.
func clampDisplay(s string) string {
	if len(s) <= displayMax {
		return s
	}
	n := displayMax
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return fmt.Sprintf("%s... (%d bytes)", s[:n], len(s))
}
