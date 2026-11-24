package entragraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// maxCode and maxMessage cap what is kept of Graph's error.
	maxCode    = 64
	maxMessage = 300
	// maxRetryAfter caps the wait a Retry-After header can name, so a broken
	// header neither overflows nor parks the caller for days.
	maxRetryAfter = time.Hour
)

// ErrNotFound matches an *Error whose status is 404, with errors.Is. Graph
// answers 404 for an object that is gone, and also for one created so
// recently that the read reached a replica that has not seen it yet.
var ErrNotFound = errors.New("entragraph: Microsoft Graph has no such object")

// Error is an answer of Microsoft Graph with an HTTP status of 400 or more.
// Code and Message come from Graph's error body when it has one: the code
// only when it is a plain identifier, the message cleaned of control
// characters, cut to 300 bytes and withheld when it repeats the token.
// RetryAfter is the wait that a 429 or 503 names in its Retry-After header
// in seconds, and zero otherwise. The client never waits or retries itself.
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
	// what names the request in the sentence.
	what string
}

// Error is one sentence: what was asked, the status and Graph's own words.
func (e *Error) Error() string {
	what := e.what
	if what == "" {
		what = "a request"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Microsoft Graph answered %s with HTTP %d", what, e.Status)
	switch {
	case e.Code != "" && e.Message != "":
		fmt.Fprintf(&b, " %s: %s", e.Code, e.Message)
	case e.Code != "":
		fmt.Fprintf(&b, " %s", e.Code)
	case e.Message != "":
		fmt.Fprintf(&b, ": %s", e.Message)
	default:
		b.WriteString(" and no Graph error")
	}
	if !strings.HasSuffix(b.String(), ".") {
		b.WriteString(".")
	}
	if e.RetryAfter > 0 {
		fmt.Fprintf(&b, " Graph asks Straza to wait %d seconds before its next request.", int(e.RetryAfter.Seconds()))
	}
	return b.String()
}

// Is reports a 404 as ErrNotFound.
func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// graphError builds the *Error of an answer of 400 or more. token is the
// token the request carried, so a message that repeats it is withheld.
func graphError(what string, resp *http.Response, body []byte, token string) *Error {
	e := &Error{Status: resp.StatusCode, what: what}
	var g struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &g) == nil {
		e.Code = plainCode(g.Error.Code)
		e.Message = cleanMessage(g.Error.Message, token)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
	}
	return e
}

// plainCode returns an error code that is safe to repeat: letters, digits,
// underscores and dots, at most maxCode of them, as Graph's and OAuth's
// codes are. Anything else answers the empty string.
func plainCode(code string) string {
	if len(code) > maxCode {
		return ""
	}
	for _, r := range code {
		plain := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.'
		if !plain {
			return ""
		}
	}
	return code
}

// cleanMessage prepares Graph's message for a sentence: withheld whole when
// it holds the token, control characters made spaces, and cut to maxMessage
// bytes on a rune boundary.
func cleanMessage(msg, token string) string {
	if token != "" && strings.Contains(msg, token) {
		return "Graph's message is withheld, because it repeats Straza's token"
	}
	msg = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, msg))
	if len(msg) <= maxMessage {
		return msg
	}
	cut := maxMessage
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}

// retryAfter reads a Retry-After header in whole seconds. A missing,
// negative or non-numeric value is zero, and a value over maxRetryAfter is
// maxRetryAfter.
func retryAfter(v string) time.Duration {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	if n > int64(maxRetryAfter/time.Second) {
		return maxRetryAfter
	}
	return time.Duration(n) * time.Second
}
