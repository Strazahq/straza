package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseNotices(t *testing.T) {
	root := fixture(t)
	console := withConsole(t, root)

	body, err := releaseNotices(root, "1.0.0")
	if err != nil {
		t.Fatalf("release notices: %v", err)
	}
	got := heads(body)
	if len(got) != 11 {
		t.Fatalf("want 11 sections, the eight console ones, the standard library and two modules, got %q", got)
	}
	wantTail := []string{"go example.com/dep v1.0.0", "go example.com/noticed v1.2.0"}
	if strings.Join(got[9:], "\n") != strings.Join(wantTail, "\n") || !strings.HasPrefix(got[8], "go-stdlib std ") {
		t.Errorf("sections out of order: %q", got)
	}
	if !strings.HasPrefix(string(body), "THIRD-PARTY NOTICES FOR Straza 1.0.0\n") || !strings.Contains(string(body), "\nComponents: 11\n") {
		t.Errorf("header wrong:\n%.400s", body)
	}
	if strings.Contains(string(body), root) {
		t.Error("the notices carry the absolute path of the fixture")
	}
	for _, s := range parseSections(console) {
		if !strings.Contains(string(body), "\n"+s.body) {
			t.Errorf("the console section %s %s was not copied unchanged", s.name, s.version)
		}
	}
	cases := []struct {
		head     string
		contains []string
		lacks    []string
	}{
		{"go example.com/dep v1.0.0",
			[]string{"License: see the texts below\n", "Origin: example.com/dep\n", "--- LICENSE ---\nCopyright (c) 2020 The Dep Authors",
				"\n--- inner/LICENSE ---\nMIT License\n\nCopyright (c) 2016 Inner Author\n",
				"\n--- inner/NOTICE.txt ---\nInner SDK\nCopyright Inner Corp.",
				"\n--- third_party/NOTICE ---\nThird Party SDK\n"},
			[]string{"license.go", "Not a license text", "unlinked", "Unlinked Author"}},
		{"go example.com/noticed v1.2.0", []string{"--- LICENSE ---\nApache License\n", "--- NOTICE ---\nNoticed\nCopyright 2020 The Noticed Authors\n", "\n--- assets/COPYING ---\nThe data is Copyright Assets Author.\n"}, nil},
		{got[8], []string{"standard library and runtime\n", "--- LICENSE ---\n", "--- PATENTS ---\n"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.head, func(t *testing.T) {
			s := sectionText(t, body, tc.head)
			for _, c := range tc.contains {
				if !strings.Contains(s, c) {
					t.Errorf("section lacks %q:\n%s", c, s)
				}
			}
			for _, l := range tc.lacks {
				if strings.Contains(s, l) {
					t.Errorf("section carries %q", l)
				}
			}
		})
	}
}

// TestVerifyCatchesNewModule is the drift gate: a module added to the graph
// after the notices were written makes -verify fail and name the fix.
func TestVerifyCatchesNewModule(t *testing.T) {
	root := fixture(t)
	withConsole(t, root)
	t.Chdir(root)
	file := filepath.Join(t.TempDir(), "THIRD_PARTY_NOTICES.txt")
	if err := run([]string{"-release", "-version", "1.0.0", "-o", file}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := run([]string{"-release", "-version", "1.0.0", "-verify", file}); err != nil {
		t.Fatalf("verify of a fresh file: %v", err)
	}

	writeFiles(t, root, map[string]string{
		"go.mod":                "module example.com/fixture\n\ngo 1.22\n\nrequire (\n\texample.com/dep v1.0.0\n\texample.com/extra v0.3.0\n\texample.com/noticed v1.2.0\n)\n\nreplace (\n\texample.com/dep => ./deps/dep\n\texample.com/extra => ./deps/extra\n\texample.com/noticed => ./deps/noticed\n)\n",
		"cmd/strazad/main.go":   "package main\n\nimport (\n\t_ \"example.com/dep\"\n\t_ \"example.com/extra\"\n)\n\nfunc main() {}\n",
		"deps/extra/go.mod":     "module example.com/extra\n\ngo 1.22\n",
		"deps/extra/extra.go":   "package extra\n",
		"deps/extra/COPYING":    "Copyright (c) Extra Author\n",
		"deps/extra/NOTICE.txt": "Extra carries this notice.\n",
	})
	err := run([]string{"-release", "-version", "1.0.0", "-verify", file})
	if err == nil {
		t.Fatal("verify passed after a module was added, want a refusal")
	}
	for _, w := range []string{file + " differs from a fresh run for version 1.0.0", "delete that draft release and build the release again from this tree"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q lacks %q", err, w)
		}
	}
	if strings.Contains(err.Error(), "-o ") {
		t.Errorf("error %q tells the operator to write the file again, which cannot fix a release built with it", err)
	}
	if err := run([]string{"-release", "-version", "1.0.0", "-o", file}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	body, err := os.ReadFile(file) //nolint:gosec // G304: the test's own file
	if err != nil {
		t.Fatal(err)
	}
	s := sectionText(t, body, "go example.com/extra v0.3.0")
	for _, c := range []string{"--- COPYING ---\nCopyright (c) Extra Author\n", "--- NOTICE.txt ---\nExtra carries this notice.\n"} {
		if !strings.Contains(s, c) {
			t.Errorf("the new module's section lacks %q:\n%s", c, s)
		}
	}
}

func TestReleaseNoticesRefuses(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		console bool
		want    []string
	}{
		{"module without a license file",
			map[string]string{"deps/dep/LICENSE": ""}, true,
			[]string{"the Go module example.com/dep v1.0.0 ships no license file", "exception table"}},
		{"console notices missing", nil, false,
			[]string{consoleOut, "run make ui"}},
		{"go list fails",
			map[string]string{"cmd/straza/main.go": "package main\n\nimport _ \"example.com/missing\"\n\nfunc main() {}\n"}, true,
			[]string{"cannot list the Go modules the binaries link for linux/amd64"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t)
			if tc.console {
				withConsole(t, root)
			}
			writeFiles(t, root, tc.files)
			_, err := releaseNotices(root, "1.0.0")
			if err == nil {
				t.Fatal("release notices succeeded, want a refusal")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestRunRefusesBadFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "choose one mode"},
		{[]string{"-console", "-release"}, "choose one mode"},
		{[]string{"-console", "-version", "1.0.0"}, "belong to the release mode"},
		{[]string{"-release", "-o", "x"}, "-release needs -version"},
		{[]string{"-release", "-version", "1.0.0"}, "either -o FILE"},
		{[]string{"-release", "-version", "1.0.0", "-o", "x", "-verify", "y"}, "and not both"},
		{[]string{"-console", "extra"}, "unexpected argument"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			err := run(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("run(%q) = %v, want an error with %q", tc.args, err, tc.want)
			}
		})
	}
}

// TestReleaseNoticesOfThisTree runs the release mode on the repository, so a
// linked Go module without a license file fails the commit gate rather than
// the release. It reads go.mod and go.sum itself, because a change to them
// must invalidate the cached result of this test.
func TestReleaseNoticesOfThisTree(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, f := range []string{"go.mod", "go.sum"} {
		if _, err := os.ReadFile(filepath.Join(root, f)); err != nil { //nolint:gosec // G304: a fixed file of the repository
			t.Fatal(err)
		}
	}
	body, err := releaseNotices(root, "0.0.0-test")
	if err != nil {
		t.Fatalf("the release notices of this tree cannot be written: %v", err)
	}
	var mods int
	for _, s := range parseSections(body) {
		if s.kind == "go" {
			mods++
		}
		if !strings.Contains(s.body, rule+"\n\n--- ") {
			t.Errorf("section %s %s %s carries no text", s.kind, s.name, s.version)
		}
	}
	if mods == 0 {
		t.Fatal("the release notices list no Go module: is go list reading this repository?")
	}
}
