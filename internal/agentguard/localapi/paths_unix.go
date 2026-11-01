//go:build unix && !darwin

package localapi

import "path/filepath"

// System paths of the service, and the OS account it runs under. They carry
// the service's role so nothing collides with strazad's /var/lib/straza or
// deploy/sandbox's /run/straza.
const (
	SystemStateDir = "/var/lib/straza-service"
	ServiceAccount = "straza-service"
)

// SystemSocket is the socket of the system service. It is a variable only so
// a test can keep a fake system service off a machine that runs a real one;
// nothing outside tests sets it.
var SystemSocket = "/run/straza-service/service.sock"

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
