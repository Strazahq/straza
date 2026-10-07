package scim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// patchOp is one normalized PATCH operation. Attribute paths are
// case-insensitive per RFC 7643; ops are lowercased.
type patchOp struct {
	op    string // add|replace|remove
	path  string // lowercased; "" = whole-resource object value
	value any
}

// mutabilityError maps to scimType "mutability".
type mutabilityError struct{ msg string }

func (e mutabilityError) Error() string { return e.msg }

// decodePatch validates the PatchOp envelope and normalizes operations.
func decodePatch(r *http.Request) ([]patchOp, error) {
	var body struct {
		Schemas    []string `json:"schemas"`
		Operations []struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value any    `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("malformed PatchOp body")
	}
	if len(body.Operations) == 0 {
		return nil, fmt.Errorf("the Operations list must not be empty")
	}
	ops := make([]patchOp, 0, len(body.Operations))
	for _, o := range body.Operations {
		op := strings.ToLower(o.Op)
		switch op {
		case "add", "replace", "remove":
		default:
			return nil, fmt.Errorf("op %q is not add/replace/remove", o.Op)
		}
		ops = append(ops, patchOp{op: op, path: normalizePath(o.Path), value: o.Value})
	}
	return ops, nil
}

// normalizePath lowercases the attribute path (RFC 7643: attribute names are
// case-insensitive) while preserving anything inside the outermost quote
// pair: a value filter like members[value eq "ID"] carries case-sensitive
// data, and lowercasing it silently no-ops the member removal.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	open := strings.IndexByte(p, '"')
	if open < 0 {
		return strings.ToLower(p)
	}
	end := strings.LastIndexByte(p, '"')
	return strings.ToLower(p[:open]) + p[open:end+1] + strings.ToLower(p[end+1:])
}

// memberFilterRe matches the value-filter removal form IdMs send:
// members[value eq "<id>"].
var memberFilterRe = regexp.MustCompile(`^members\[value eq "((?:[^"\\]|\\.)*)"\]$`)
