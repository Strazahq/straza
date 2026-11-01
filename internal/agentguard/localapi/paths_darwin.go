package localapi

import "path/filepath"

// System paths of the service on macOS, and the hidden role account its
// launchd daemon runs under. The Straza package makes both folders owned by
// that account, because the account cannot make a folder under /opt or
// /var/db itself.
const (
	SystemStateDir = "/var/db/straza-service"
	ServiceAccount = "_straza-service"
)

// SystemSocket is the socket of the system service. It is a variable only so
// a test can keep a fake system service off a machine that runs a real one;
// nothing outside tests sets it.
var SystemSocket = "/opt/straza/run/service.sock"

// Paths returns the socket and the state directory for home, the value of
// STRAZA_HOME. An empty home selects the system service. A set one selects
// a user-mode service at $STRAZA_HOME/service.sock with its state in
// $STRAZA_HOME/service, which is how one OS user runs a service per
// identity.
func Paths(home string) (socket, stateDir string, userMode bool) {
	if home == "" {
		return SystemSocket, SystemStateDir, false
	}
	return filepath.Join(home, "service.sock"), filepath.Join(home, "service"), true
}
