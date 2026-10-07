package approval

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/sameorigin"
)

// approvalRedirect is the redirect policy of every client the approval
// channels send with: the Slack Web API client, the push client that web
// push, UnifiedPush, FCM and the relay share, and the APNs client. It is
// sameorigin.CheckMethod: a request follows a redirect only within the scheme,
// host and port it was sent to, and only while its method stays, as a 307 or
// 308 keeps it. Go's default forwards a bearer token to another port or scheme
// of the same host name, re-sends the body on a 307 or 308 to any host, one
// outside approval.push.allowedPushHosts included, and turns a POST into a
// bodiless GET on a 301, 302 or 303, whose 2xx reads as a delivery. The tenth
// redirect stops the chain.
func approvalRedirect(req *http.Request, via []*http.Request) error {
	var cross *sameorigin.RedirectError
	var method *sameorigin.MethodError
	err := sameorigin.CheckMethod(req, via)
	switch {
	case errors.As(err, &cross):
		return &redirectError{from: cross.From, to: cross.To}
	case errors.As(err, &method):
		return &redirectError{from: method.Origin, status: method.Status, method: method.From, into: method.To}
	case errors.Is(err, sameorigin.ErrTooManyRedirects):
		return &redirectError{from: via[0].URL.Scheme + "://" + via[0].URL.Host, loop: true}
	}
	return err
}

// doRequest sends req on c. A redirect whose Location header does not parse
// fails in Go's client before approvalRedirect runs, so doRequest gives that
// failure the redirect sentence too. Every other result passes through.
func doRequest(c *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := c.Do(req)
	if origin, ok := sameorigin.BadLocation(err); ok {
		return nil, &redirectError{from: origin, badLocation: true}
	}
	return resp, err
}

// redirectError is a redirect an approval client did not follow. from and to
// are scheme://host[:port] only, so no push path, query or credential reaches
// the delivery record or the log. status is set when the redirect stays on
// the origin but would change the method from method into into, loop when the
// chain reached ten redirects, and badLocation when the Location header did
// not parse.
type redirectError struct {
	from, to          string
	status            int
	method, into      string
	loop, badLocation bool
}

// redirectNextStep ends every redirect sentence of the approval clients.
const redirectNextStep = "Check that no proxy between strazad and that service redirects, and that the address strazad has for it is the final one"

func (e *redirectError) Error() string {
	switch {
	case e.loop:
		return e.from + " redirected the request 10 times in a row, " +
			"so strazad stopped following it as a redirect loop and the message was not delivered. " + redirectNextStep
	case e.badLocation:
		return e.from + " answered with a redirect whose Location header is not a valid address, " +
			"so strazad could not follow it and the message was not delivered. " + redirectNextStep
	case e.status != 0:
		return fmt.Sprintf("%s answered with a %d redirect, which turns the %s into a %s without its body, "+
			"so strazad did not follow it and the message was not delivered. %s", e.from, e.status, e.method, e.into, redirectNextStep)
	}
	return e.from + " answered with a redirect to " + e.to + ", which is another scheme, host or port, " +
		"so strazad did not follow it and sent neither the message nor its credentials there. " +
		"An approval message and its credentials must not leave the address strazad was given. " + redirectNextStep
}
