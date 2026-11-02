//go:build darwin

package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// machineSentence is the refusal of a Mac whose hardware UUID cannot be
// read, for the reason why.
func machineSentence(why string) string {
	return "`straza service` could not read this Mac's hardware UUID from /usr/sbin/ioreg (" + why + "), so it cannot tell a copied key " +
		"from this Mac's own and will not start. Check that ioreg runs, then start the service again with " +
		"`sudo launchctl kickstart -k system/ai.straza.service`."
}

// noSuchFile is the error a missing ioreg gives.
const noSuchFile = "exec: no such file or directory"

// ioregSample is `ioreg -rd1 -c IOPlatformExpertDevice` as an Apple silicon
// Mac prints it, cut to a few properties.
const ioregSample = `+-o J314sAP  <class IOPlatformExpertDevice, id 0x100000110, registered, matched, active, busy 0 (210 ms), retain 37>
    {
      "IOPlatformSerialNumber" = "C02ZX0Y1MD6T"
      "compatible" = <"J314sAP","MacBookPro18,3","AppleARM">
      "IOPlatformUUID" = "6F2B8B1C-3E5A-4D2B-9C1E-0A1B2C3D4E5F"
      "model" = <"MacBookPro18,3">
    }
`

// ioregFrom makes the service read ioreg's output as out and err until the
// test ends.
func ioregFrom(t *testing.T, out string, err error) {
	t.Helper()
	old := ioregPlatform
	ioregPlatform = func() ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { ioregPlatform = old })
}

// TestReadSystemMachine pins the machine identity on macOS: IOPlatformUUID
// as ioreg prints it, and when ioreg fails, prints no IOPlatformUUID, or
// prints one that is not a UUID, the run-stopping sentence and no identity.
func TestReadSystemMachine(t *testing.T) {
	for _, row := range []struct {
		name, out string
		err       error
		want      string // "" means the sentence for why
		why       string
	}{
		{"the hardware UUID", ioregSample, nil, "6F2B8B1C-3E5A-4D2B-9C1E-0A1B2C3D4E5F", ""},
		{"ioreg fails", "", errors.New(noSuchFile), "", noSuchFile},
		{"no IOPlatformUUID", `+-o Root  <class IORegistryEntry, id 0x100000100, retain 22>`, nil, "", noPlatformUUID},
		{"an empty IOPlatformUUID", `      "IOPlatformUUID" = ""`, nil, "", noPlatformUUID},
		{"not a UUID", `      "IOPlatformUUID" = "6F2B8B1C-3E5A-4D2B-9C1E"`, nil, "", noPlatformUUID},
	} {
		t.Run(row.name, func(t *testing.T) {
			ioregFrom(t, row.out, row.err)
			got, err := readSystemMachine()
			switch {
			case row.want != "" && (err != nil || got != row.want):
				t.Errorf("readSystemMachine = %q, %v; want %q", got, err, row.want)
			case row.want == "" && (got != "" || err == nil || err.Error() != machineSentence(row.why)):
				t.Errorf("readSystemMachine = %q, %v; want the sentence %q", got, err, machineSentence(row.why))
			}
		})
	}
}

// TestReadSystemMachineOfThisMac reads this Mac's own ioreg twice and gets
// the same UUID both times.
func TestReadSystemMachineOfThisMac(t *testing.T) {
	first, err := readSystemMachine()
	if err != nil {
		t.Fatal(err)
	}
	second, err := readSystemMachine()
	if err != nil {
		t.Fatal(err)
	}
	uuid := regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	if !uuid.MatchString(first) || first != second {
		t.Errorf("this Mac's machine identity = %q then %q, want one UUID", first, second)
	}
}

// TestMachineUnreadableStopsTheService pins that the credential half does
// not open on a Mac whose hardware UUID cannot be read, and keeps no
// random identity in its state folder instead.
func TestMachineUnreadableStopsTheService(t *testing.T) {
	ioregFrom(t, "", errors.New(noSuchFile))
	dir := t.TempDir()
	if _, err := newCredentials(context.Background(), Options{StateDir: dir}); err == nil || err.Error() != machineSentence(noSuchFile) {
		t.Fatalf("newCredentials = %v, want the sentence %q", err, machineSentence(noSuchFile))
	}
	if _, err := os.Stat(filepath.Join(dir, "machine-id")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the service kept an identity of its own: %v", err)
	}
}
