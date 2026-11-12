package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// unknownFieldPrefix starts the error of an encoding/json decoder that
// refuses unknown fields. The field's name follows it in quotes.
const unknownFieldPrefix = "json: unknown field "

// decodeAdmin decodes one JSON value from an admin request's body into v and
// refuses a field v does not declare, so a misspelled field is an error and
// never a silent default. A caller passes the error to refusedUnknownField
// before it words any other failure.
func decodeAdmin(body io.Reader, v any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// refusedUnknownField answers 400 when err is decodeAdmin's refusal of an
// unknown field, naming the field, and reports whether it answered. Any
// other error stays the route's to word.
func refusedUnknownField(w http.ResponseWriter, err error) bool {
	if err == nil || !strings.HasPrefix(err.Error(), unknownFieldPrefix) {
		return false
	}
	field := strings.TrimPrefix(err.Error(), unknownFieldPrefix)
	apiError(w, http.StatusBadRequest, "The request carries a field named "+field+", which this request does not take, so nothing was changed. "+
		"Check its spelling against the API reference at https://docs.straza.ai/reference/api/ and send the request again.")
	return true
}
