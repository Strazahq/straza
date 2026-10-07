// Command notices writes THIRD_PARTY_NOTICES.txt, the copyright notices and
// license texts of the third-party software that Straza ships. It runs from
// the repository root. The console mode reads web/ui/package-lock.json, the
// package imports of the console's CSS and the installed packages, and writes
// the notices of the console bundle beside its chunks, where strazad serves
// them at /console/THIRD_PARTY_NOTICES.txt. The release mode copies those
// sections and adds the Go modules that the three binaries link on every
// release platform and the Go standard library, for the release archives and
// the container image. A component without license text stops the run with
// its name, so nothing ships without its notice.
//
//	go run ./tools/notices -console [-o FILE]
//	go run ./tools/notices -release -version VERSION -o FILE
//	go run ./tools/notices -release -version VERSION -verify FILE
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// consoleOut is the console's notices file inside the committed bundle.
const consoleOut = "internal/server/console/dist/THIRD_PARTY_NOTICES.txt"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fl := flag.NewFlagSet("notices", flag.ContinueOnError)
	console := fl.Bool("console", false, "write the notices of the console bundle")
	release := fl.Bool("release", false, "write the notices of a release archive or image")
	version := fl.String("version", "", "the release version the notices name, for example 1.2.0")
	out := fl.String("o", "", "the file to write (the console mode defaults to "+consoleOut+")")
	verify := fl.String("verify", "", "a release notices file to compare with a fresh run")
	if err := fl.Parse(args); err != nil {
		return err
	}
	switch {
	case fl.NArg() > 0:
		return fmt.Errorf("unexpected argument %q: pass -console or -release with their flags only", fl.Arg(0))
	case *console == *release:
		return errors.New("choose one mode: -console writes the console's notices, and -release writes the notices of a release archive or image")
	case *console && (*version != "" || *verify != ""):
		return errors.New("-version and -verify belong to the release mode: drop them, or use -release")
	case *console:
		body, err := consoleNotices(".")
		if err != nil {
			return err
		}
		if *out == "" {
			*out = consoleOut
		}
		return write(*out, body)
	case *version == "":
		return errors.New("-release needs -version, the version the notices name, for example -version 1.2.0")
	case (*out == "") == (*verify == ""):
		return errors.New("-release needs either -o FILE to write the notices or -verify FILE to compare a file with a fresh run, and not both")
	}
	body, err := releaseNotices(".", *version)
	if err != nil {
		return err
	}
	if *verify != "" {
		return verifyFile(*verify, body, *version)
	}
	return write(*out, body)
}

func write(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("cannot create the folder for %s: %w", path, err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil { //nolint:gosec // G306: a license notice is world-readable by design
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}

// verifyFile compares the notices file at path with fresh, the notices of the
// current module graph and console, and names the first line that differs.
func verifyFile(path string, fresh []byte, version string) error {
	have, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the file to verify
	if err != nil {
		return fmt.Errorf("cannot read %s to verify it: %w", path, err)
	}
	if bytes.Equal(have, fresh) {
		return nil
	}
	line := 1
	for i := 0; i < len(have) && i < len(fresh) && have[i] == fresh[i]; i++ {
		if have[i] == '\n' {
			line++
		}
	}
	return fmt.Errorf("%s differs from a fresh run for version %s over this tree at line %d, so it was written for another version or from other Go modules, another Go version or other console notices: a release built with it ships the wrong notices, so delete that draft release and build the release again from this tree, which writes the notices afresh",
		path, version, line)
}
