package redact

import (
	"math"
	"regexp"
	"slices"
)

var (
	// generatedRunRe finds the runs a random-looking string is judged on.
	// Slashes, dots, colons and equals signs end a run, so paths, hosts and
	// flag=value pairs are judged part by part.
	generatedRunRe = regexp.MustCompile(`[A-Za-z0-9+_-]{32,}`)
	// pathRunRe finds the runs of an address's path, which a secret fills
	// with fewer characters than a value the entropy warning reads.
	pathRunRe = regexp.MustCompile(`[A-Za-z0-9+_-]{20,}`)
	uuidRe    = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)
	hexRe     = regexp.MustCompile(`^[0-9A-Fa-f]+$`)
)

// Capability reports whether path, the part of an address after its host
// or one segment of it, carries a part that reads like a generated secret:
// a run of 20 or more token characters, 32 or more when they are hex
// digits, that reads as random. A UUID is an identifier and does not
// count. Such a part works as a password for anyone who has the address.
func Capability(path string) bool {
	for _, run := range pathRunRe.FindAllString(uuidRe.ReplaceAllString(path, " "), -1) {
		if (len(run) >= 32 || !hexRe.MatchString(run)) && random(run) {
			return true
		}
	}
	return false
}

// Generated reports whether s holds a run of 32 or more token characters
// that reads as random. Identifiers such as mcp-server-2025 keep their
// digits in clumps, and a UUID is an identifier, so neither counts.
func Generated(s string) bool {
	return slices.ContainsFunc(generatedRunRe.FindAllString(uuidRe.ReplaceAllString(s, " "), -1), random)
}

// random reports whether run, a run of token characters, reads like a
// generated secret: a letter and a digit stand side by side at least four
// times, and it carries at least 3.8 bits per character, or 3 bits for a
// run of hex digits, whose alphabet is smaller.
func random(run string) bool {
	var counts [256]int
	for i := 0; i < len(run); i++ {
		counts[run[i]]++
	}
	bits := 0.0
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / float64(len(run))
			bits -= p * math.Log2(p)
		}
	}
	floor := 3.8
	if hexRe.MatchString(run) {
		floor = 3.0
	}
	return Switches(run) >= 4 && bits >= floor
}

// Switches counts the places in s where a letter and a digit stand side by
// side.
func Switches(s string) int {
	class := func(b byte) int {
		switch {
		case b >= '0' && b <= '9':
			return 1
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
			return 2
		}
		return 0
	}
	n := 0
	for i := 1; i < len(s); i++ {
		if a, b := class(s[i-1]), class(s[i]); a != 0 && b != 0 && a != b {
			n++
		}
	}
	return n
}
