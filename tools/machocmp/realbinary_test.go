package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealBinaries cross-builds a small program for darwin/arm64 and
// darwin/amd64 the way the release builds straza, then checks each binary
// against a copy re-signed the way codesign_allocate does it, and against
// that copy with one byte of its code flipped.
func TestRealBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-builds two darwin binaries")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), []byte("module example.com/tiny\n\ngo 1.22\n"))
	writeFile(t, filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"tiny\") }\n"))
	for _, goarch := range []string{"arm64", "amd64"} {
		t.Run(goarch, func(t *testing.T) {
			bin := filepath.Join(dir, "tiny-"+goarch)
			cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", bin, ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=darwin", "GOARCH="+goarch,
				"GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go build for darwin/%s: %v\n%s", goarch, err, out)
			}
			img, err := os.ReadFile(bin)
			if err != nil {
				t.Fatal(err)
			}
			f, err := macho.NewFile(bytes.NewReader(img))
			if err != nil {
				t.Fatal(err)
			}
			last := f.Loads[len(f.Loads)-1].Raw()
			t.Logf("the linker's darwin/%s binary has a code signature: %v", goarch, binary.LittleEndian.Uint32(last) == lcCodeSignature)
			signed := resign(t, img, 0x5000)
			path := filepath.Join(dir, "signed-"+goarch)
			writeFile(t, path, signed)
			var stdout bytes.Buffer
			if err := run([]string{path, bin}, &stdout); err != nil {
				t.Fatalf("run on the re-signed copy: %v", err)
			}
			if want := "the " + goarch + " slice of " + path + " is " + bin; !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout %q lacks %q", stdout.String(), want)
			}
			writeFile(t, path, flip(signed, uint64(f.Section("__text").Offset)+64))
			if err := run([]string{path, bin}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "differs from "+bin) {
				t.Errorf("run on a copy with a flipped code byte = %v, want a difference", err)
			}
		})
	}
}
