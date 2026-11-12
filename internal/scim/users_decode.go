package scim

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// wrongTypeMsg is the SCIM error detail of a User document that holds a
// value of the wrong JSON type.
const wrongTypeMsg = "%s must be %s, and this request sent %s, so nothing was saved. " +
	"Set its type to %s in the identity manager's attribute mapping, then send the user again"

// wrongTypeDetail answers the SCIM error detail for a User document whose
// decode failed on a value of the wrong JSON type, and "" for every other
// error, a body that is not a JSON object included. The detail names the
// attribute as SCIM writes it and the two types, never the value sent.
func wrongTypeDetail(err error) string {
	var te *json.UnmarshalTypeError
	if !errors.As(err, &te) || te.Field == "" {
		return ""
	}
	var want string
	switch te.Type.Kind() {
	case reflect.String:
		want = "string"
	case reflect.Bool:
		want = "boolean"
	case reflect.Slice:
		want = "array"
	case reflect.Struct:
		want = "object"
	default:
		return ""
	}
	sent := te.Value
	if sent == "bool" {
		sent = "boolean"
	}
	// The decoder joins an extension's name and its attribute with a dot,
	// where SCIM writes a colon.
	attr := strings.Replace(te.Field, ":User.", ":User:", 1)
	return fmt.Sprintf(wrongTypeMsg, attr, withArticle(want), withArticle(sent), want)
}

// withArticle puts "a" or "an" before the name of a JSON type.
func withArticle(jsonType string) string {
	if strings.HasPrefix(jsonType, "a") || strings.HasPrefix(jsonType, "o") {
		return "an " + jsonType
	}
	return "a " + jsonType
}
