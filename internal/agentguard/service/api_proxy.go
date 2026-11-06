package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// strippedHeaders are the caller's credentials, which never reach strazad.
// The credential half's transport sets the session's own.
var strippedHeaders = []string{"Authorization", "DPoP", "Cookie"}

// connectRoot is strazad's connect route, which every connect path stays
// under.
const connectRoot = "/v1/connect"

// proxyMCP passes /local/v1/mcp and /local/v1/mcp/{server} to strazad's
// /mcp and /mcp/{server} for the caller's session. The server segment is
// the matched name, checked against the server name rule, never the
// caller's raw path.
func (a *api) proxyMCP(w http.ResponseWriter, r *http.Request) {
	if !a.plainPath(w, r, localapi.RouteMCP) {
		return
	}
	server := r.PathValue("server")
	if server == "" && r.URL.Path == localapi.RouteMCP {
		a.proxy(w, r, "/mcp")
		return
	}
	// A server name must pass the manifest rule before it becomes a path
	// segment of strazad's.
	if !agentguard.ValidServerName(server) {
		a.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("The server name %q is not valid. A server name has only "+
			"lowercase letters, digits and hyphens, is at most 64 characters long, and starts and ends with a letter or "+
			"digit. Run `strazactl apps list`, or open MCP servers in the console, to see the names.", server))
		return
	}
	a.proxy(w, r, "/mcp/"+server)
}

// proxyConnect passes /local/v1/connect and every path below it to
// strazad's /v1/connect for the caller's session. The rest of the path is
// the decoded, cleaned match, and it must stay under /v1/connect.
func (a *api) proxyConnect(w http.ResponseWriter, r *http.Request) {
	if !a.plainPath(w, r, strings.TrimSuffix(localapi.RouteConnect, "/")) {
		return
	}
	upstream := path.Clean(connectRoot + "/" + r.PathValue("rest"))
	if upstream != connectRoot && !strings.HasPrefix(upstream, connectRoot+"/") {
		a.refuse(w, r, http.StatusBadRequest, "The path "+r.URL.Path+" leaves "+strings.TrimSuffix(localapi.RouteConnect, "/")+
			", so the Straza background service refused it. Run `straza connect` again with the server's name.")
		return
	}
	a.proxy(w, r, upstream)
}

// plainPath refuses a request whose path carries percent-encoded
// characters, so an encoded slash or dot segment never reaches strazad
// still encoded, where a decoding hop in front of it could turn the path
// into another route that the session's credentials would then reach.
func (a *api) plainPath(w http.ResponseWriter, r *http.Request, route string) bool {
	if r.URL.EscapedPath() == r.URL.Path {
		return true
	}
	a.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("The Straza background service refused the path %s, because it "+
		"carries percent-encoded characters and the service forwards only plain paths on %s. Name the server as it is, "+
		"with only lowercase letters, digits and hyphens.", r.URL.EscapedPath(), route))
	return false
}

// proxy streams r to upstream, a plain path, on the session's strazad and
// the answer back with no buffering, which the standalone GET stream and
// the gateway's approval hold need. The session comes from the harness
// headers. Each refusal of its own made before the send carries
// HeaderNotSent, and no answer from strazad does.
func (a *api) proxy(w http.ResponseWriter, r *http.Request, upstream string) {
	// Every answer until the send below is a refusal made before it.
	w.Header().Set(localapi.HeaderNotSent, "1")
	h := localapi.Harness{
		Name:    r.Header.Get(localapi.HeaderHarness),
		Version: r.Header.Get(localapi.HeaderHarnessVersion),
		Session: r.Header.Get(localapi.HeaderHarnessSession),
	}
	if !a.named(w, r, h) {
		return
	}
	s, err := a.proxySession(r.Context(), callerOf(r), h)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	target, err := url.Parse(s.ServerURL)
	if err != nil || target.Host == "" {
		a.refuse(w, r, http.StatusInternalServerError, fmt.Sprintf("The Straza background service holds the server "+
			"address %q, which is not a URL it can call. Run `straza enroll` again with the server's address.", s.ServerURL))
		return
	}
	target.Path, target.RawPath, target.RawQuery = strings.TrimRight(target.Path, "/")+upstream, "", r.URL.RawQuery
	w.Header().Del(localapi.HeaderNotSent)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = ""
			for _, k := range strippedHeaders {
				pr.Out.Header.Del(k)
			}
		},
		Transport:     a.creds.Transport(s),
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del(localapi.HeaderNotSent)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// The credential half refuses before it sends.
			var refused *Refused
			if errors.As(err, &refused) {
				w.Header().Set(localapi.HeaderNotSent, "1")
				a.refuse(w, r, refused.Status, refused.Sentence)
				return
			}
			// A failed dial proves the request never left this machine, so
			// it answers 503, which a caller may try again. Any other
			// failure may come after strazad ran the request, so it answers
			// 502, which a caller must not take as permission to resend.
			var dial *net.OpError
			if errors.As(err, &dial) && dial.Op == "dial" {
				w.Header().Set(localapi.HeaderNotSent, "1")
				a.refuse(w, r, http.StatusServiceUnavailable, fmt.Sprintf("The Straza background service could not reach the "+
					"Straza server at %s (%v). Check that this machine reaches the server, then run `straza doctor`.", s.ServerURL, err))
				return
			}
			a.refuse(w, r, http.StatusBadGateway, fmt.Sprintf("The Straza background service did not get an answer from the "+
				"Straza server at %s (%v), so the request may or may not have run there. Check that this machine reaches the server, "+
				"then run `straza doctor`.", s.ServerURL, err))
		},
	}
	rp.ServeHTTP(w, r)
}

// proxySession returns the caller's session for h. A named window gets its
// own session or the refusal, so a revoked window never checks in again
// from the proxy, and only a session start re-acquires it. With no window
// named, the caller's newest live session of the harness is used, and one
// is started when none is live, which spends one of the caller's start
// tokens.
func (a *api) proxySession(ctx context.Context, c Caller, h localapi.Harness) (Session, error) {
	s, err := a.creds.Session(ctx, c, h, false)
	if err == nil || h.Session != "" {
		return s, err
	}
	if !a.starts.allow(c.UID) {
		return Session{}, &Refused{Status: http.StatusTooManyRequests, Sentence: tooManyStarts}
	}
	s, err = a.creds.Session(ctx, c, h, true)
	if err == nil && s.Reused {
		a.starts.refund(c.UID)
	}
	return s, err
}
