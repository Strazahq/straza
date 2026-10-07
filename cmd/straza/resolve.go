package main

import "strings"

// enrollSource names where `straza enroll` got its strazad base URL, spelled
// the way the operator wrote it so an announcement can point at the exact
// thing to change.
type enrollSource string

const (
	// enrollSourceNone: nothing named a server.
	enrollSourceNone enrollSource = ""
	// enrollSourceFlag is the --server flag: the URL is on the command line
	// the operator just typed, so echoing it back says nothing new.
	enrollSourceFlag enrollSource = "--server"
	// enrollSourceEnv is $STRAZA_SERVER: ambient, invisible, and possibly
	// exported by a profile the operator forgot about.
	enrollSourceEnv enrollSource = "$STRAZA_SERVER"
)

// enrollTarget is the strazad endpoint one `straza enroll` invocation
// resolved, plus where it came from.
type enrollTarget struct {
	// Server is the base URL to enroll against; "" when nothing named one.
	Server string
	// Source is where Server came from.
	Source enrollSource
}

// Announce returns the single line enroll prints before the device flow when
// the target came from the environment, or "" when the command line already
// says on its face what is being enrolled against.
//
// Enroll is not a read-only command: what it resolves it PERSISTS, and the
// stored server outlives the shell that exported the variable. A stale export
// therefore aims an enrolment at the wrong deployment with nothing on screen
// to show for it, so the fallback announces itself exactly once, and only the
// fallback (one canonical announcement, no behaviour change).
func (t enrollTarget) Announce() string {
	if t.Source != enrollSourceEnv {
		return ""
	}
	return "enrolling at " + t.Server + " (from $STRAZA_SERVER)"
}

// resolveEnrollServer applies enroll's server resolution order: --server beats
// $STRAZA_SERVER. Neither is a silent default and there is no localhost
// guess; an empty Server means the caller must fail closed and say so.
func resolveEnrollServer(flagValue, envValue string) enrollTarget {
	if v := strings.TrimSpace(flagValue); v != "" {
		return enrollTarget{Server: v, Source: enrollSourceFlag}
	}
	if v := strings.TrimSpace(envValue); v != "" {
		return enrollTarget{Server: v, Source: enrollSourceEnv}
	}
	return enrollTarget{Source: enrollSourceNone}
}
