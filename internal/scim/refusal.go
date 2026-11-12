package scim

import "net/http"

// Refusal is one SCIM write the server refused with a 4xx or a 501: the
// status, scimType and detail of the error it answered, the id the route
// names, and the name of the user or role the write was about. Target is ""
// on a route without an id. Username is the stored user's name, or on a
// create the userName the body asked for. Role is the stored role's name.
type Refusal struct {
	Status   int
	SCIMType string
	Detail   string
	Target   string
	Username string
	Role     string
}

// refusalWriter is the writer a SCIM write runs on. writeError notes the
// first error answered on it, and the handlers note the name of the user or
// role the write is about, so requireToken can report the refusal once.
type refusalWriter struct {
	http.ResponseWriter
	ref Refusal
}

// refused answers whether the noted error is a refusal: a 4xx, or the 501
// Straza answers for an operation outside its subset. A 5xx other than 501
// is a failure, which the server logs on its own lane.
func (w *refusalWriter) refused() bool {
	s := w.ref.Status
	return (s >= 400 && s < 500) || s == http.StatusNotImplemented
}

// noteError keeps the first error written on w.
func noteError(w http.ResponseWriter, status int, scimType, detail string) {
	if rw, ok := w.(*refusalWriter); ok && rw.ref.Status == 0 {
		rw.ref.Status, rw.ref.SCIMType, rw.ref.Detail = status, scimType, detail
	}
}

// noteUser names the user a write on w is about.
func noteUser(w http.ResponseWriter, username string) {
	if rw, ok := w.(*refusalWriter); ok {
		rw.ref.Username = username
	}
}

// noteRole names the role a write on w is about.
func noteRole(w http.ResponseWriter, name string) {
	if rw, ok := w.(*refusalWriter); ok {
		rw.ref.Role = name
	}
}

// reportRefusal runs the write next on a refusalWriter and reports the
// refusal it answered, once, to Deps.WriteRefused.
func (s *Server) reportRefusal(next http.HandlerFunc, w http.ResponseWriter, r *http.Request) {
	rw := &refusalWriter{ResponseWriter: w}
	next(rw, r)
	if rw.refused() && s.deps.WriteRefused != nil {
		rw.ref.Target = r.PathValue("id")
		s.deps.WriteRefused(r, rw.ref)
	}
}
