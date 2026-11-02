package localapi

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// SystemSocket is the named pipe of the Windows service straza-service. It is
// a variable only so a test can keep a fake system service off a machine
// that runs a real one; nothing outside tests sets it.
var SystemSocket = `\\.\pipe\straza-service`

// ServiceAccount is the OS account the system service runs under, NT
// SERVICE\straza-service, whose SID is ServiceSID.
const ServiceAccount = "straza-service"

// SystemStateDir is the state folder of the Windows service straza-service:
// straza-service in ProgramData.
var SystemStateDir = filepath.Join(ProgramData(), "straza-service")

// ProgramData returns the ProgramData folder on the volume Windows runs
// from, such as C:\ProgramData, or C:\ProgramData when Windows does not name
// that volume. It comes from the Windows directory the kernel reports and
// never from an environment variable. The ProgramData known folder expands
// the caller's SystemDrive variable, and a live run showed that a moved
// SystemDrive breaks it. Microsoft does not support moving ProgramData.
func ProgramData() string {
	dir, err := windows.GetSystemWindowsDirectory()
	if vol := filepath.VolumeName(dir); err == nil && vol != "" {
		return vol + `\ProgramData`
	}
	return `C:\ProgramData`
}

// Paths returns the system pipe and state folder whatever home says. On
// Windows only the system service runs, and STRAZA_HOME never selects
// another, so the environment a harness starts with cannot point a thin
// client at a pipe the caller controls.
func Paths(string) (socket, stateDir string, userMode bool) {
	return SystemSocket, SystemStateDir, false
}
