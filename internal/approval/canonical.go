package approval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// JCS-style canonicalization of mcp.call arguments. The canonical form
// (sorted keys, no inter-token whitespace, deterministic escaping, normalized
// numbers) is what the v2 fingerprint hashes, never raw bytes: key order and
// formatting differ between the gateway lane (post-strip re-marshal), the hook
// lane (client re-marshal of tool_input), and a model-authored native action,
// and none of that noise may re-prompt a human. Server-side only, so
// cross-implementation byte compatibility (full RFC 8785) is a non-goal;
// determinism and noise-collapse are the contract, pinned by canonical_test.go.
//
// Ambiguity is REJECTED, not papered over: duplicate object keys and nesting
// past the depth cap return an error so the caller denies fail-closed: the
// args a human sees must be the args the grant binds.

// JustificationField is the reserved property the gateway injects into
// approval-gated tool schemas (internal/server/catalog.go writes it, the
// gateway strips it before audit/upstream). The canonicalizer drops it at the
// top level so a retry carrying a different justification (or none) still
// matches the grant its approval minted.
const JustificationField = "_straza_justification"

// canonMaxDepth bounds recursion; past it the payload is rejected (deny),
// never hashed part-read.
const canonMaxDepth = 128

var (
	errCanonDepth   = errors.New("approval: arguments nested past the canonicalization depth cap")
	errCanonDupKey  = errors.New("approval: duplicate object key in arguments")
	errCanonNotObj  = errors.New("approval: arguments must be a JSON object")
	errCanonTrailer = errors.New("approval: trailing data after arguments object")
)

// CanonicalArgs canonicalizes an mcp.call arguments payload. Empty and JSON
// null both normalize to "{}" (the observed no-argument call); any other
// non-object payload is an error. The returned bytes are stable: canonicalizing
// them again is the identity.
func CanonicalArgs(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []byte("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("approval: arguments are not valid JSON: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errCanonNotObj
	}
	var buf bytes.Buffer
	if err := canonObject(dec, &buf, 1, true); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errCanonTrailer
	}
	return buf.Bytes(), nil
}

// canonObject canonicalizes the members of an object whose '{' was already
// consumed. dropJustification is true only at the top level.
func canonObject(dec *json.Decoder, buf *bytes.Buffer, depth int, dropJustification bool) error {
	if depth > canonMaxDepth {
		return errCanonDepth
	}
	type member struct {
		key string
		val []byte
	}
	var members []member
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("approval: arguments are not valid JSON: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return errCanonNotObj
		}
		if seen[key] {
			return errCanonDupKey
		}
		seen[key] = true
		var vb bytes.Buffer
		if err := canonValue(dec, &vb, depth); err != nil {
			return err
		}
		if dropJustification && key == JustificationField {
			continue
		}
		members = append(members, member{key, vb.Bytes()})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return fmt.Errorf("approval: arguments are not valid JSON: %w", err)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].key < members[j].key })
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeCanonString(buf, m.key)
		buf.WriteByte(':')
		buf.Write(m.val)
	}
	buf.WriteByte('}')
	return nil
}

// canonValue canonicalizes one JSON value read from dec into buf.
func canonValue(dec *json.Decoder, buf *bytes.Buffer, depth int) error {
	if depth > canonMaxDepth {
		return errCanonDepth
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("approval: arguments are not valid JSON: %w", err)
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return canonObject(dec, buf, depth+1, false)
		case '[':
			buf.WriteByte('[')
			first := true
			for dec.More() {
				if !first {
					buf.WriteByte(',')
				}
				first = false
				if err := canonValue(dec, buf, depth+1); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return fmt.Errorf("approval: arguments are not valid JSON: %w", err)
			}
			buf.WriteByte(']')
			return nil
		}
		return errCanonNotObj
	case string:
		writeCanonString(buf, v)
	case json.Number:
		buf.WriteString(canonNumber(v))
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case nil:
		buf.WriteString("null")
	}
	return nil
}

// canonNumber normalizes a number literal. int64-exact literals keep their
// digits (never a lossy float round-trip); float-form literals render in Go's
// shortest form, collapsing to integer digits when the value is integral and
// exactly representable (|v| < 2^53); anything else (integer literals past
// int64, unparseable exotica) stays verbatim, so two spellings of a huge
// value remain distinct and fail toward re-prompting, never toward collision.
func canonNumber(n json.Number) string {
	s := string(n)
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return strconv.FormatInt(i, 10)
	}
	if !strings.ContainsAny(s, ".eE") {
		return s // integer literal past int64: verbatim, never float-rewritten
	}
	f, err := n.Float64()
	if err != nil {
		return s
	}
	if f == float64(int64(f)) && f >= -1<<53 && f <= 1<<53 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// writeCanonString writes a string in Go's deterministic JSON escaping.
func writeCanonString(buf *bytes.Buffer, s string) {
	b, err := json.Marshal(s)
	if err != nil { // cannot happen for a string; be defensive
		buf.WriteString(`""`)
		return
	}
	buf.Write(b)
}
