package server

import (
	"strings"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// serverAdminRuntimeRefusal answers the sentence that refuses a server
// admin's change when it touches the runtime. A command or oci runtime is a
// process on the gateway host, and a remote server's address is where the
// server's credentials and every caller's token are sent, so only an area
// grant may choose either. Every path under straza.runtime is refused: a
// place of hidden, the paths hiddenWrites answers, with the hidden value
// sentence whether the value written there matches the stored one or not,
// the address block of a remote server with the address sentence, and
// anything else, the kind first of all, with the runtime sentence. A change
// of straza.exposure.views is refused with the views sentence, because it
// decides whether the server's own HTML is shown to people.
func serverAdminRuntimeRefusal(prev store.App, mf manager.Manifest, hidden []string) (string, error) {
	if len(hidden) > 0 {
		return "writing a value at " + hidden[0] + " needs the scope apps:write or the role " + MCPAdminRole +
			", because Straza hides the value stored there, and whether a value written there matches it must not show. " +
			"Leave the mask that strazactl apps export prints at that place to keep the stored value, or ask a holder of " + MCPAdminRole + " to make that change.", nil
	}
	next, err := mf.JSON()
	if err != nil {
		return "", err
	}
	changed, err := manager.ChangedPaths([]byte(prev.Manifest), []byte(next))
	if err != nil {
		return "", err
	}
	address, views := false, false
	for _, path := range changed {
		if path == "straza.exposure.views" {
			views = true
			continue
		}
		if !strings.HasPrefix(path, "straza.runtime") {
			continue
		}
		if prev.RuntimeKind == manager.RuntimeRemote && strings.HasPrefix(path, "straza.runtime.remote.") {
			address = true
			continue
		}
		return "changing a server's runtime needs the scope apps:write or the role " + MCPAdminRole +
			", because a command or oci runtime is a process on the gateway host. Ask a holder of " + MCPAdminRole + " to make that change.", nil
	}
	if address {
		return "changing a server's address needs the scope apps:write or the role " + MCPAdminRole +
			", because the server's credentials and every caller's token are sent to that address. Ask a holder of " + MCPAdminRole + " to make that change.", nil
	}
	if views {
		return "changing straza.exposure.views of a server needs the scope apps:write or the role " + MCPAdminRole +
			", because with views on, chat apps show the server's own HTML pages to the people who use it. Ask a holder of " + MCPAdminRole + " to make that change.", nil
	}
	return "", nil
}

// serverAdminAgentTokensRefusal answers the sentence that refuses a server
// admin's change when it switches the server to credential.agents
// client_credentials, or changes the provider of a server that uses it. From
// then on every agent that calls the server gets a token of its own at the
// provider with no step of consent, and the token goes to the server's
// address, so only an area grant may opt a server in.
func serverAdminAgentTokensRefusal(prev store.App, mf manager.Manifest) (string, error) {
	if mf.AgentsSource() != manager.AgentsClientCredentials {
		return "", nil
	}
	was, err := manager.FromJSON(prev.Manifest)
	if err != nil {
		return "", err
	}
	if was.AgentsSource() == manager.AgentsClientCredentials && was.Straza.Credential.OAuth != nil && mf.Straza.Credential.OAuth != nil &&
		was.Straza.Credential.OAuth.Provider == mf.Straza.Credential.OAuth.Provider {
		return "", nil
	}
	return "setting credential.agents to client_credentials, or changing the provider of a server that uses it, needs the scope apps:write or the role " + MCPAdminRole +
		", because every agent that calls the server then gets a token of its own at the provider and the token is sent to the server's address. Ask a holder of " + MCPAdminRole + " to make that change.", nil
}

// hiddenWrites answers the paths under straza.runtime of doc, an App
// document, that hold a value in clear, no mask, at a place where live,
// the stored manifest, holds a value every route masks. A server admin may
// change nothing there, and comparing such a value with the stored one
// would tell a caller whether a guess of the hidden value was right, so
// the check counts every such place as a change, equal or not.
func hiddenWrites(doc, live string) []string {
	kept := livePlaces(live)
	var out []string
	for _, s := range scalarsOf(parsed(doc), "", "", "") {
		lp := kept[s.place]
		if !s.key && lp != nil && lp.masked != lp.plain && !maskedValue(s.node.Value) && strings.HasPrefix(s.path, "straza.runtime.") {
			out = append(out, s.path)
		}
	}
	return out
}
