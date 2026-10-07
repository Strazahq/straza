package ctl

import (
	"errors"
	"fmt"
	"net/http"
)

// codingAgentMarkers are the environment variables that coding agents set in
// the commands they run, in the order a refusal names them. An agent can
// unset them, so the guard they feed stops an honest mistake or a naive
// prompt injection, never an agent that means to act as its person.
var codingAgentMarkers = []string{
	// Claude Code sets both in every command it runs.
	"CLAUDECODE",
	"CLAUDE_CODE_ENTRYPOINT",
	// The launcher of Codex's npm package sets these on the codex process,
	// and the commands it runs inherit them. A Codex started from a bare
	// binary sets neither and goes unseen.
	"CODEX_MANAGED_PACKAGE_ROOT",
	"CODEX_MANAGED_BY_NPM",
	// The Gemini CLI sets GEMINI_CLI=1 in every shell command it runs.
	"GEMINI_CLI",
}

// changeFreeCalls are the calls strazactl sends with a method other than GET
// that change nothing in Straza, keyed as method and exact path, so the guard
// lets them out on the login. strazactl calls neither the server's policy
// validate route nor the install dry run, so neither is listed.
var changeFreeCalls = map[string]bool{
	// policy simulate evaluates calls against the live sets and a draft and stores nothing.
	"POST /v1/admin/policies/simulate": true,
	// drafts check checks documents against live state and stores nothing.
	"POST /v1/admin/drafts/check": true,
}

// CodingAgentMarker returns the first coding-agent marker that getenv reports
// non-empty, or the empty string outside a coding agent. strazactl passes
// os.Getenv and hands the answer to Client.AgentMarker.
func CodingAgentMarker(getenv func(string) string) string {
	for _, name := range codingAgentMarkers {
		if getenv(name) != "" {
			return name
		}
	}
	return ""
}

// guardRefuses reports whether the coding-agent guard stops a call that would
// go out on the stored login: inside a coding agent, every call but a GET and
// the change-free calls.
func (c *Client) guardRefuses(method, path string) bool {
	return c.AgentMarker != "" && method != http.MethodGet && !changeFreeCalls[method+" "+path]
}

// GuardChange answers the refusal the coding-agent guard gives a call that
// changes Straza on the stored login, or nil when such a call may go out: on
// an admin API token, or outside a coding agent. A command that reads or asks
// before it changes calls it first, so the refusal comes before the read is
// sent or the question asked.
func (c *Client) GuardChange() error {
	if c.APIToken != "" || c.AgentMarker == "" {
		return nil
	}
	return agentGuardError(c.AgentMarker)
}

// agentGuardError is the refusal of a call that changes Straza and would go
// out on the stored login inside a coding agent. marker names the variable
// that gave the agent away.
func agentGuardError(marker string) error {
	return fmt.Errorf("%s is set, so strazactl runs inside a coding agent, and this command changes Straza. "+
		"On the strazactl login stored on this machine the agent would act as the person who logged in, so nothing was sent. "+
		"Reads and checks such as policy simulate still work. For automation, use an admin API token in STRAZA_API_TOKEN "+
		"that a person mints outside the agent with strazactl api-token create. "+
		"An agent proposes config changes through the built-in straza MCP server's drafting tools", marker)
}

// errTokenOffAdmin refuses a call outside the admin API while an admin API
// token is in use: those routes take a person's session, which a token is
// not, so the token is never sent there.
var errTokenOffAdmin = errors.New("this command acts as a person and needs a strazactl login, but STRAZA_API_TOKEN is set, " +
	"and an admin API token works on the admin API only. Unset STRAZA_API_TOKEN, then run the command again on your login")
