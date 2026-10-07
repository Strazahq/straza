package drafts

import (
	"fmt"
	"strconv"
)

// SecretRefusal refuses a static secret on app, the server's own when role
// is empty and that role's override otherwise, or answers nil. It reads the
// credential facts of app, and w.Roles only when role is set. The manifest
// must leave the secret a use, and only an application role holds one.
func SecretRefusal(w World, app App, role string) *Refusal {
	if app.Credential == "" {
		return refused(RefusalUnread, fmt.Sprintf(
			"Straza did not read the manifest of the MCP server %s, so it cannot tell whether a secret has a use there. Try again, and read the strazad log if it keeps failing.", app.Name))
	}
	if msg := secretKindRefusal(app); msg != "" {
		return refused(RefusalInvalid, msg)
	}
	if role == "" {
		return nil
	}
	ro, ok := w.Roles[role]
	if !ok {
		return refused(RefusalMissing, UnknownRoleMessage(role))
	}
	if msg := roleRefusal(ro, "cannot hold a secret", "Set the secret for an application role instead, or for the server itself."); msg != "" {
		return refused(RefusalInvalid, msg)
	}
	return nil
}

// secretKindRefusal says why app's manifest leaves no use for a static
// secret, empty when a secret has one.
func secretKindRefusal(app App) string {
	caller := app.Credential == CredentialOAuth || app.Credential == CredentialToken
	switch {
	case app.Credential == CredentialNone:
		return fmt.Sprintf("the MCP server %s declares no credential (credential.kind none), so a secret would never be used. Change the manifest's credential block first.", app.Name)
	case caller && app.Agents == AgentsShared:
		// Agents without a row of their own run on the shared secret, so
		// the row has a use.
		return ""
	case app.Credential == CredentialOAuth:
		return fmt.Sprintf("the MCP server %s uses each caller's own sign-in (credential.kind oauth) and lets no agent use a shared account (credential.agents %s), so a static secret would never be used. Set credential.agents: shared in the manifest first.", app.Name, app.Agents)
	case app.Credential == CredentialToken:
		return fmt.Sprintf("the MCP server %s uses each caller's own token (credential.kind token) and lets no agent use a shared account (credential.agents %s), so a static secret would never be used. Set credential.agents: shared in the manifest first.", app.Name, app.Agents)
	}
	return ""
}

// RegisterRefusal refuses a caller who is not a full admin the install of
// the server named name unless they administer it, or answers nil. It reads
// w.Apps. A server admin changes the servers they administer and registers
// none.
func RegisterRefusal(w World, st Standing, name string) *Refusal {
	if st.Full {
		return nil
	}
	if app, ok := w.Apps[name]; ok && st.Servers[app.ID] {
		return nil
	}
	return refused(RefusalForbidden, fmt.Sprintf("registering a new server needs the scope apps:write or the role %s. You administer %s.",
		MCPAdminRole, countServers(len(st.Servers))))
}

// countServers words the refusal for one server or several.
func countServers(n int) string {
	if n == 1 {
		return "1 server and may change it"
	}
	return strconv.Itoa(n) + " servers and may change those"
}
