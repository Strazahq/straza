package objects

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/approval"
)

// canonFaults maps each refusal of approval.CanonicalArgs to the words of
// the unreadable-arguments sentence. A refusal the map does not know reads
// as invalid JSON, so a new refusal there still refuses here.
var canonFaults = []struct{ prefix, fault string }{
	{"approval: duplicate object key", "a key appears twice"},
	{"approval: trailing data", "there is data after the arguments object"},
	{"approval: arguments must be a JSON object", "they are not a JSON object"},
	{"approval: arguments nested past", "they nest deeper than 128 levels"},
}

// ArgValue reads the string value of the argument arg from the raw
// arguments of a call to tool on server, whose pinned type is typ. It
// answers the fault sentence instead when the arguments are not valid
// UTF-8, do not canonicalize, carry a case twin of arg, lack arg, or carry
// it as another type than a string.
func ArgValue(tool, server, typ, arg string, raw json.RawMessage) (value, refusal string) {
	unreadable := func(fault string) (string, string) {
		return "", fmt.Sprintf("%s's arguments could not be read the same way by Straza and by %s: %s. Send each argument once, in one JSON object, then try again.",
			tool, server, fault)
	}
	missing := fmt.Sprintf("%s needs the %s it works on in its argument %s, and this call carries none. Send %s as a string, then try again.",
		tool, typ, arg, arg)
	// Go's decoder reads a byte that is not UTF-8 as U+FFFD without an
	// error, so the raw bytes are checked first and the server, whatever
	// language reads them, cannot see another value than the gate does.
	if !utf8.Valid(raw) {
		return unreadable("they are not valid UTF-8")
	}
	if _, err := approval.CanonicalArgs(raw); err != nil {
		for _, f := range canonFaults {
			if strings.HasPrefix(err.Error(), f.prefix) {
				return unreadable(f.fault)
			}
		}
		return unreadable("they are not valid JSON")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return "", missing
	}
	got, present := top[arg]
	if !present {
		return "", missing
	}
	for _, k := range slices.Sorted(maps.Keys(top)) {
		if k != arg && k != approval.JustificationField && strings.EqualFold(k, arg) {
			return "", fmt.Sprintf("%s's arguments carry both %s and %s, and %s might read either. Send %s once, as its input schema spells it, then try again.",
				tool, arg, k, server, arg)
		}
	}
	if err := json.Unmarshal(got, &value); err != nil || !strings.HasPrefix(string(got), `"`) {
		return "", fmt.Sprintf("%s needs the %s in its argument %s as a string, and this call sends another type. Send %s as a string, then try again.",
			tool, typ, arg, arg)
	}
	// An escaped lone surrogate decodes to U+FFFD as well, while another
	// reader may keep it, so a value that carries U+FFFD is refused.
	if strings.ContainsRune(value, utf8.RuneError) {
		return unreadable("they are not valid UTF-8")
	}
	return value, ""
}
