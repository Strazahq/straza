package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// RoleConfig is one role's config as drafts read and write it: the row,
// the name of the server that owns it, its one access row by server name
// and tool matchers, and the names of the roles it implies directly.
type RoleConfig struct {
	Role    Role
	Owner   string
	Server  string
	Tools   []string
	Implies []string
}

// roleKindStraza is the wire kind of a control-plane role, the kind the
// admin API shows and the Role fingerprint reads.
const roleKindStraza = "straza"

// maxNumberExp bounds the exponent of a manifest number the fingerprint
// compares, far past any float64, so that no exponent overflows an int.
const maxNumberExp = 1 << 20

// FingerprintApp answers the hex sha256 of a live server's config, its
// stored manifest re-encoded as canonical JSON, and "" for an empty
// manifest. Status, source, version column, admin role and timestamps are
// not inputs, so a health or bookkeeping write never reads as a change.
// Canonical JSON sorts keys, turns HTML escaping off and spells every
// number one way, so Postgres's JSONB rendering of a manifest and the text
// sqlite keeps answer alike. It fails on a manifest that is not one JSON
// value.
func FingerprintApp(manifest string) (string, error) {
	if manifest == "" {
		return "", nil
	}
	canon, err := canonicalJSON(manifest)
	if err != nil {
		return "", fmt.Errorf("store: the stored manifest cannot be compared: %w", err)
	}
	return fingerprint("App\n", canon), nil
}

// FingerprintRole answers the hex sha256 of a role's config: name,
// description, wire kind, owner, access row server and sorted tools, and
// sorted implied names. The zero RoleConfig answers "".
func FingerprintRole(c RoleConfig) string {
	if c.Role == (Role{}) && c.Owner == "" && c.Server == "" && len(c.Tools) == 0 && len(c.Implies) == 0 {
		return ""
	}
	kind := c.Role.Kind
	if c.Role.Plane == RolePlaneControl {
		kind = roleKindStraza
	}
	// Marshal cannot fail on strings and string slices.
	doc, _ := json.Marshal(struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Kind        string   `json:"kind"`
		Owner       string   `json:"owner"`
		Server      string   `json:"server"`
		Tools       []string `json:"tools"`
		Implies     []string `json:"implies"`
	}{c.Role.Name, c.Role.Description, kind, c.Owner, c.Server, sortedNames(c.Tools), sortedNames(c.Implies)})
	return fingerprint("Role\n", doc)
}

// FingerprintPolicySet answers the hex sha256 of a set's name, whether its
// row says it is on, and its stored text. The zero PolicySet answers "".
func FingerprintPolicySet(p PolicySet) string {
	if p == (PolicySet{}) {
		return ""
	}
	state := "off"
	if p.Status == "active" {
		state = "on"
	}
	return fingerprint("PolicySet\n", []byte(p.Name+"\n"+state+"\n"+p.YAMLSource))
}

func fingerprint(kind string, body []byte) string {
	sum := sha256.Sum256(append([]byte(kind), body...))
	return hex.EncodeToString(sum[:])
}

// sortedNames answers a sorted copy of names that is never nil, so an
// absent list and an empty one encode alike.
func sortedNames(names []string) []string {
	out := append([]string{}, names...)
	slices.Sort(out)
	return out
}

// canonicalJSON decodes one JSON value with its numbers as written and
// encodes it again with sorted keys, no HTML escaping and every number in
// the spelling canonicalNumber gives.
func canonicalJSON(raw string) ([]byte, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("it holds more than one JSON value")
	}
	v, err := canonicalNumbers(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// canonicalNumbers replaces every number in a decoded value with its
// canonical spelling.
func canonicalNumbers(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			c, err := canonicalNumbers(e)
			if err != nil {
				return nil, err
			}
			x[k] = c
		}
	case []any:
		for i, e := range x {
			c, err := canonicalNumbers(e)
			if err != nil {
				return nil, err
			}
			x[i] = c
		}
	case json.Number:
		return canonicalNumber(string(x))
	}
	return v, nil
}

// canonicalNumber spells a JSON number as its significant digits with the
// decimal point after the first and a decimal exponent, such as 5e-7 or
// 1.5e1, and zero as 0. Postgres prints a number it stored as JSONB in
// plain decimal notation with the scale it was given, so 5e-07, 0.0000005
// and 5.0e-7 all name one value and must spell alike.
func canonicalNumber(s string) (json.Number, error) {
	sign := ""
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		sign, s = "-", rest
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || e > maxNumberExp || e < -maxNumberExp {
			return "", fmt.Errorf("it holds the number %s%s, whose exponent is too large to compare", sign, s)
		}
		s, exp = s[:i], e
	}
	whole, frac, _ := strings.Cut(s, ".")
	all := whole + frac
	digits := strings.TrimLeft(all, "0")
	// The decimal point falls after the first point digits of digits, a
	// count that is negative or runs past the end when zeros stand between.
	point := len(whole) + exp - (len(all) - len(digits))
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return "0", nil
	}
	mantissa := digits[:1]
	if len(digits) > 1 {
		mantissa += "." + digits[1:]
	}
	return json.Number(sign + mantissa + "e" + strconv.Itoa(point-1)), nil
}
