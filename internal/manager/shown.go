package manager

import "github.com/strazahq/straza/internal/redact"

// shown is s as the manager lets a client, a log reader or an audit record
// read it: every credential shape redact knows replaced by redact.Mark, and
// every address without its user information and with "?…" in place of its
// query (redact.URLs). A server's address may carry a password, a user name
// or a token in its query, and an upstream error quotes the address.
func shown(s string) string {
	return redact.URLs(redact.Redact(s))
}

// shownError is an error whose text is shown's form of the text of err,
// which it wraps, so errors.Is and errors.As still read err.
type shownError struct{ err error }

func (e *shownError) Error() string { return shown(e.err.Error()) }

func (e *shownError) Unwrap() error { return e.err }

// shownErr answers err wrapped as a shownError, and nil for nil.
func shownErr(err error) error {
	if err == nil {
		return nil
	}
	return &shownError{err: err}
}
