//go:build !windows && !darwin

package service

import (
	"os"
	"strings"
)

// readSystemMachine reads /etc/machine-id. A system without one has no
// identity of its own, which is not an error.
func readSystemMachine() (string, error) {
	raw, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(raw)), nil
}
