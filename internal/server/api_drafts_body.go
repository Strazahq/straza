package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/strazahq/straza/internal/drafts"
)

// draftBody is the body of the routes that write or check a draft:
// documents are YAML streams, items single objects, note the proposer's
// words, working whether the items join the caller's working draft, and
// revision the revision an update is made on. A nil Note is a note left
// out, which an update reads as keeping the one the draft has.
type draftBody struct {
	Revision  int             `json:"revision"`
	Documents []string        `json:"documents"`
	Items     []draftBodyItem `json:"items"`
	Note      *string         `json:"note"`
	Working   bool            `json:"working"`
}

// draftBodyItem is one object of the items form. It accepts and ignores the
// base, existed and withheld fields of an item read back from a draft,
// because the server stamps every item it stores or checks.
type draftBodyItem struct {
	Kind     drafts.Kind `json:"kind"`
	Name     string      `json:"name"`
	Op       drafts.Op   `json:"op"`
	Doc      string      `json:"doc"`
	Base     string      `json:"base"`
	Existed  bool        `json:"existed"`
	Withheld string      `json:"withheld"`
}

// The sentences of a body that does not decode, one per route family.
const (
	draftBodyRefusal   = "The request body is not a draft: %s. Send documents, items or both as JSON."
	discardBodyRefusal = "The request body is not a discard: %s. Send revision and reason as JSON, or no body."
	revertBodyRefusal  = "The request body is not an undo: %s. Send a note as JSON, or no body."
)

// decodeDraftBody decodes the request body into v and answers the request
// itself when it cannot: 400 that names a field v does not declare, 413
// for a body over server.maxBodyBytes, and 400 in the words of frame
// otherwise. With optional, a request with no body reads as an empty object.
func decodeDraftBody(w http.ResponseWriter, r *http.Request, v any, frame string, optional bool) bool {
	err := decodeAdmin(r.Body, v)
	if refusedUnknownField(w, err) {
		return false
	}
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil, optional && errors.Is(err, io.EOF):
		return true
	case errors.As(err, &tooLarge):
		apiError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("The request body holds more than %d bytes, the most strazad reads in one request (server.maxBodyBytes). "+
			"Split the draft into smaller drafts, or raise server.maxBodyBytes.", tooLarge.Limit))
	default:
		apiError(w, http.StatusBadRequest, fmt.Sprintf(frame, err))
	}
	return false
}
