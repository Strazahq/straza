package sameorigin

import (
	"fmt"
	"net/http"
)

// MethodError refuses a redirect that stays on the origin but changes the
// request's method, as Go's client turns a POST into a GET without its body on
// a 301, 302 or 303. Origin is scheme://host[:port] only, and From and To are
// the methods before and after the redirect.
type MethodError struct {
	Origin   string
	Status   int
	From, To string
}

func (e *MethodError) Error() string {
	return fmt.Sprintf("%s answered with a %d redirect, which turns the %s into a %s without its body, so the request was not followed",
		e.Origin, e.Status, e.From, e.To)
}

// CheckMethod is the redirect policy of a client whose requests deliver
// something, such as a POST with a body. It applies Check and also refuses a
// redirect that changes the method, because the GET that Go's client sends
// after a 301, 302 or 303 drops the body, and its 2xx would read as a
// delivery. A 307 or 308 keeps the method and the body, and a GET stays a GET
// on every redirect, so both follow within the origin.
func CheckMethod(req *http.Request, via []*http.Request) error {
	if err := Check(req, via); err != nil {
		return err
	}
	if first := via[0]; req.Method != first.Method {
		return &MethodError{Origin: first.URL.Scheme + "://" + first.URL.Host, Status: req.Response.StatusCode,
			From: first.Method, To: req.Method}
	}
	return nil
}
