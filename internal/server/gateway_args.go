package server

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// argumentsMismatchMsg is the refusal of an approval-gated call whose
// arguments break the tool's input schema. It does not start with "Straza:",
// because that prefix tells a model never to retry, and a corrected call
// should be retried.
const argumentsMismatchMsg = "Straza checked the arguments against %s's input schema before asking for approval, and they do not match: %s. Fix the arguments and call the tool again."

// inputSchema returns the input schema the server published for the
// catalog name, or nil when the catalog holds no such tool.
func (c *sessionCatalog) inputSchema(name string) any {
	for _, t := range c.tools {
		if t.Name == name {
			return t.InputSchema
		}
	}
	return nil
}

// schemaMismatch reports why args break schema, or "" when they fit. It also
// answers "" when the check cannot judge: no schema, one the validator cannot
// read, such as draft-04's boolean exclusiveMaximum, or one whose $ref points
// outside itself. The validator's default loader refuses every external
// reference, so a schema from an upstream server never makes strazad fetch a
// URL or read a file. The check is never stricter than a server that can read
// its own schema, and the call then goes on as it did before the check.
func schemaMismatch(schema any, args json.RawMessage) string {
	if schema == nil {
		return ""
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return ""
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var instance any
	if err := json.Unmarshal(args, &instance); err != nil {
		return ""
	}
	if err := resolved.Validate(instance); err != nil {
		return err.Error()
	}
	return ""
}
