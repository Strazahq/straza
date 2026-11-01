//go:build darwin

package service

import (
	"fmt"
	"os/exec"
	"regexp"
)

// machineUnreadable stops the service on a Mac whose hardware UUID it
// cannot read, and says why.
func machineUnreadable(why string) error {
	return fmt.Errorf("`straza service` could not read this Mac's hardware UUID from /usr/sbin/ioreg (%s), "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
		"so it cannot tell a copied key from this Mac's own and will not start. Check that ioreg runs, then start the service "+
		"again with `sudo launchctl kickstart -k system/ai.straza.service`.", why)
}

// ioregPlatform returns what ioreg prints for the platform expert device,
// whose IOPlatformUUID is this Mac's hardware UUID. Tests replace it.
var ioregPlatform = func() ([]byte, error) {
	return exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
}

// noPlatformUUID is the reason given when ioreg runs but prints no UUID.
const noPlatformUUID = "its output holds no IOPlatformUUID"

// platformUUID matches the IOPlatformUUID property in ioreg's output.
var platformUUID = regexp.MustCompile(`"IOPlatformUUID" = "([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})"`)

// readSystemMachine returns this Mac's hardware UUID as ioreg prints it.
// It is the machine's own and survives macOS updates, unlike kern.uuid,
// which names the running kernel. A UUID that cannot be read stops the
// service rather than fall back to an identity the state folder would
// carry to a copy.
func readSystemMachine() (string, error) {
	out, err := ioregPlatform()
	if err != nil {
		return "", machineUnreadable(err.Error())
	}
	m := platformUUID.FindSubmatch(out)
	if m == nil {
		return "", machineUnreadable(noPlatformUUID)
	}
	return string(m[1]), nil
}
