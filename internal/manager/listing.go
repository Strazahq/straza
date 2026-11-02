package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ListingNeedsSecret reports whether the call that lists the objects of a
// server with manifest m carries a listing credential: a remote runtime
// whose manifest names a credential. A remote without one carries none,
// and a command or oci runtime is one process with one identity, so its
// listing carries no per-call credential.
func ListingNeedsSecret(m Manifest) bool {
	return m.Straza.Runtime.Kind == RuntimeRemote && m.CredentialKind() != CredentialNone
}

// CallListing calls the listing tool of appName for the background object
// listing, with args as the call's arguments. A server that takes a
// listing credential gets exactly secret and is refused the call when
// secret is nil, so the listing never falls back to another credential.
// Every other server is called with no per-call credential, whatever
// secret holds.
func (m *Manager) CallListing(ctx context.Context, appName, tool string, args json.RawMessage, secret *Secret) (*mcp.CallToolResult, error) {
	m.mu.RLock()
	inst, ok := m.byName[appName]
	m.mu.RUnlock()
	if !ok || inst.runtime == nil {
		return nil, notRunning(appName)
	}
	if !ListingNeedsSecret(inst.manifest) {
		secret = nil
	} else if secret == nil {
		return nil, fmt.Errorf("%s has no listing credential, so Straza did not call %s. Add one with strazactl apps secret set %s --listing",
			appName, tool, appName)
	}
	res, err := inst.runtime.Call(ctx, CallInput{Tool: tool, Args: args, Secret: secret})
	return res, shownErr(err)
}

// ErrToolsUnread is returned by LiveInventory for a server that has a
// runtime but whose tool list Straza has not read yet.
var ErrToolsUnread = errors.New("its tool list was not read yet")

// LiveInventory answers every tool appName's server lists, before the
// manifest's exposure cuts it down, from the last inventory probe. It
// refuses with the not-running error while the server has no runtime, and
// with ErrToolsUnread while it has one that was never probed. In-memory
// only.
func (m *Manager) LiveInventory(appName string) ([]*mcp.Tool, error) {
	m.mu.RLock()
	inst, ok := m.byName[appName]
	m.mu.RUnlock()
	if !ok || inst.runtime == nil {
		return nil, notRunning(appName)
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if !inst.invFilled {
		return nil, fmt.Errorf("%s: %w. An administrator reads why with strazactl apps show %s", appName, ErrToolsUnread, appName)
	}
	return slices.Clone(inst.inventory), nil
}
