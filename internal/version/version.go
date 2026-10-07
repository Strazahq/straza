// Package version exposes build-time version metadata for all Straza binaries.
// Values are injected via -ldflags (see Makefile); defaults apply to `go run`
// and test builds.
package version

import "runtime"

// Set at build time via -ldflags -X.
var (
	// Version is the semantic version or git describe output.
	Version = "dev"
	// Commit is the short git commit hash.
	Commit = "none"
)

// Info is the canonical version payload served at /version and printed by CLIs.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// Get returns the version info for the running binary.
func Get() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}
