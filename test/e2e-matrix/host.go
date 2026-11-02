package e2ematrix

import (
	"fmt"
	"path/filepath"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// hostServiceRefusal is the sentence the lane stops with on a machine that
// runs the Straza system service. There every straza ignores STRAZA_HOME
// (localapi.SystemPresent), so each identity of a scenario would reach the
// system service and replace the key and enrollment of the account that
// runs the lane.
const hostServiceRefusal = "this machine runs the Straza system service (%s), so the journey lane's clients would use it and " +
	"replace this account's key and enrollment. Nothing ran. Run the lane in a container or on a machine without a Straza install"

// hostGuard answers the refusal when present says this machine runs the
// Straza system service, and nil otherwise.
func hostGuard(present bool) error {
	if present {
		return fmt.Errorf(hostServiceRefusal, filepath.Dir(localapi.SystemSocket))
	}
	return nil
}

// refuseHostService is hostGuard for this machine.
func refuseHostService() error { return hostGuard(localapi.SystemPresent()) }
