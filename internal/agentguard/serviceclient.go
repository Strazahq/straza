package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/agentguard/trace"
)

// NewServiceClient returns the client for the Straza background service this
// process talks to (serviceHome).
func NewServiceClient() *localapi.Client {
	return localapi.NewClient(serviceHome())
}

// Seams for the two facts serviceHome trusts, so a test can set them. Both
// read fixed paths only, never an environment variable.
var (
	systemPresent    = localapi.SystemPresent
	managedInstalled = managedConfigFixed
)

// serviceHome is the STRAZA_HOME the thin clients choose their service by:
// empty for the system service, else the user-mode home. A machine with a
// system service or a managed install always uses the system service and
// ignores STRAZA_HOME, because whoever launches a harness controls its
// environment and must not point a hook at a socket the caller owns.
func serviceHome() string {
	if systemPresent() || managedInstalled() {
		return ""
	}
	return os.Getenv("STRAZA_HOME")
}

// ServiceSentence words a failed call to the Straza background service for a
// person running a command: the service's own sentence for a refusal, and
// otherwise why the service gave no answer.
func ServiceSentence(err error) string {
	return serviceSentence(err, "this command stopped")
}

// serviceSentence is ServiceSentence with outcome saying what the failure
// meant for the caller, such as "Straza denied this call" for a hook.
func serviceSentence(err error, outcome string) string {
	var refused *localapi.RefusalError
	switch {
	case errors.As(err, &refused):
		return refused.Msg
	case errors.Is(err, localapi.ErrTimeout):
		return "Straza's background service did not answer in time, so " + outcome + ". Run `straza doctor` to see why."
	case errors.Is(err, localapi.ErrUnreachable):
		return notRunningSentence(serviceHome())
	}
	return "Straza could not read the answer of its background service (" + err.Error() + "). Run `straza doctor` to see why."
}

// Outcomes the thin clients name in a timeout sentence.
const (
	outcomeDenied = "Straza denied this call"
	outcomeNotRun = "Straza did not run this command"
	outcomeNoMCP  = "the Straza MCP server did not start"
)

// notRunningSentence is the deny every thin client gives when the service
// for home cannot be reached. An empty home is the system service, which
// is the only one on Windows, because localapi.Paths ignores home there.
// Linux, macOS and Windows each name where to see why, and any other
// system runs no service in this version and says so.
func notRunningSentence(home string) string {
	goos := osName()
	if goos != "linux" && goos != "darwin" && goos != "windows" {
		return "`straza service` runs only on Linux, macOS and Windows in this version of Straza, so this machine cannot hold Straza sessions yet."
	}
	if _, _, user := localapi.Paths(home); user {
		return "Straza's background service for STRAZA_HOME " + home + " isn't running, so this command can't reach your Straza session. " +
			"Start it with `straza service` in a shell that has the same STRAZA_HOME."
	}
	where := "On Linux, `systemctl status straza-service` shows why."
	switch goos {
	case "windows":
		where = windowsWhy + "."
	case "darwin":
		where = macWhy + "."
	}
	return "Straza's background service isn't running on this machine, so this command can't reach your Straza session. " +
		"Run `straza doctor` to see why, or ask IT to reinstall Straza. " + where
}

// windowsWhy tells a person on Windows where to see whether the service
// runs and why it stopped.
const windowsWhy = "On Windows, `Get-Service straza-service` shows whether it runs, " +
	"and the Application event log under straza-service shows why it stopped"

// macWhy tells a person on macOS where to see whether the service runs and
// why it stopped.
const macWhy = "On macOS, `sudo launchctl print system/ai.straza.service` shows whether it runs, " +
	"and /var/log/straza-service/service.log shows why it stopped"

// sessionHarness names the harness a caller that does not know its window
// uses: the override, then the STRAZA_HARNESS environment variable, then
// claude-code. The MCP bridge and the caller's API client share it, so they
// pick the same session in the service.
func sessionHarness(override string) string {
	if override != "" {
		return override
	}
	if h := os.Getenv("STRAZA_HARNESS"); h != "" {
		return h
	}
	return "claude-code"
}

// proxyHarnessVersion is the harness version the MCP bridge and the API
// client send. The service checks in under it when the harness has no live
// session, so the session names the role that started it.
const proxyHarnessVersion = "mcp"

// harnessTransport names the caller's harness on every request to the
// service's proxied routes, and records the answer's status and correlation
// id for the decision journal of the call in flight.
type harnessTransport struct {
	base    http.RoundTripper
	harness string
}

func (t harnessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set(localapi.HeaderHarness, t.harness)
	r.Header.Set(localapi.HeaderHarnessVersion, proxyHarnessVersion)
	resp, err := t.base.RoundTrip(r)
	if err == nil {
		trace.CallFrom(req.Context()).Observe(resp.StatusCode, resp.Header.Get("X-Request-Id"))
	}
	return resp, err
}

// serviceHTTPClient is an HTTP client whose requests reach the service over
// its socket and name harness. It carries no overall timeout, because the
// MCP routes stream and the gateway may hold a call for an approval.
func serviceHTTPClient(svc *localapi.Client, harness string) *http.Client {
	return &http.Client{Transport: harnessTransport{base: svc.HTTPClient().Transport, harness: harness}}
}

// SessionAPI calls strazad's /v1/connect routes as the enrolled caller,
// through the service, which adds the caller's session.
type SessionAPI struct {
	http *http.Client
}

// NewSessionAPI returns the caller's API client for `straza connect` and
// `straza disconnect`.
func NewSessionAPI() *SessionAPI {
	return &SessionAPI{http: serviceHTTPClient(NewServiceClient(), sessionHarness(""))}
}

// Do sends one JSON request and decodes the answer into out. path is a
// strazad /v1/connect path, which the service serves under /local/v1. A
// refusal comes back as the sentence of whoever refused, the way strazactl
// prints it.
func (a *SessionAPI) Do(ctx context.Context, method, path string, body, out any) error {
	if !strings.HasPrefix(path, "/v1/connect") {
		return fmt.Errorf("the Straza background service does not pass %s to the server", path)
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, localapi.BaseURL+"/local"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return errors.New(ServiceSentence(err))
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var refusal localapi.Refusal
		if json.Unmarshal(buf, &refusal) != nil || refusal.Error == "" {
			return fmt.Errorf("the server answered HTTP %d and gave no reason", resp.StatusCode)
		}
		return errors.New(refusal.Error)
	}
	if out != nil && len(buf) > 0 {
		return json.Unmarshal(buf, out)
	}
	return nil
}
