package agentguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ArgumentsMax caps what an audit record keeps of a call's input, and what
// travels of it beside a classified tool's event.
const ArgumentsMax = 8 << 10

// CutArguments answers text, a call's input as JSON, as an audit record
// keeps it, and the hash of the whole text when it was shortened: "sha256:"
// and the hex SHA-256, or "" for a text kept whole. A text over
// ArgumentsMax keeps every member. Its longest strings are cut to one
// common length, the largest that fits, each to a prefix on a character
// boundary followed by its whole length as "... (<n> bytes)". The kept text
// is still JSON, and a long value cannot push a short one, such as a
// skill's name or a file's path, out of the record. A text that is not one
// JSON value, or that does not fit with every string cut, keeps its first
// ArgumentsMax bytes, cut on a character boundary.
func CutArguments(text []byte) (kept, hash string) {
	if len(text) <= ArgumentsMax {
		return string(text), ""
	}
	var v any
	if !tooManyValues(text) {
		dec := json.NewDecoder(bytes.NewReader(text))
		dec.UseNumber()
		if dec.Decode(&v) != nil || dec.More() {
			v = nil
		}
	}
	return cutArguments(v, text)
}

// tooManyValues reports whether text holds more JSON values than any text
// of ArgumentsMax bytes can. Each value costs at least two bytes there, so
// such a text cannot fit however short its strings are cut, and decoding
// it would cost time and memory for a text that keeps its first bytes
// anyway. It counts the commas, brackets and braces outside strings.
func tooManyValues(text []byte) bool {
	n := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '"':
			for i++; i < len(text) && text[i] != '"'; i++ {
				if text[i] == '\\' {
					i++
				}
			}
		case ',', '[', '{':
			if n++; n > ArgumentsMax/2 {
				return true
			}
		}
	}
	return false
}

// cutArguments is CutArguments for a text over the bound whose decoded
// value the caller already holds, nil when it is not JSON.
func cutArguments(v any, text []byte) (kept, hash string) {
	sum := sha256.Sum256(text)
	hash = "sha256:" + hex.EncodeToString(sum[:])
	fit := func(limit int) []byte {
		if v == nil {
			return nil
		}
		b, err := json.Marshal(shorten(v, limit))
		if err != nil || len(b) > ArgumentsMax {
			return nil
		}
		return b
	}
	best := fit(0)
	if best == nil {
		n := ArgumentsMax
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		return string(text[:n]), hash
	}
	for lo, hi := 1, ArgumentsMax; lo <= hi; {
		mid := (lo + hi) / 2
		if b := fit(mid); b != nil {
			best, lo = b, mid+1
		} else {
			hi = mid - 1
		}
	}
	return string(best), hash
}

// shorten copies v with every string cut to a prefix of at most limit bytes
// on a character boundary, followed by its whole length. A string that the
// cut would not make shorter stays whole.
func shorten(v any, limit int) any {
	switch t := v.(type) {
	case string:
		mark := fmt.Sprintf("... (%d bytes)", len(t))
		if len(t) <= limit+len(mark) {
			return t
		}
		n := limit
		for n > 0 && !utf8.RuneStart(t[n]) {
			n--
		}
		return t[:n] + mark
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = shorten(e, limit)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = shorten(e, limit)
		}
		return out
	}
	return v
}

// HoldsDraft reports whether toolName is the Straza gateway's draft_submit
// or approval_request as a harness names it when it registered the gateway
// under another server name: the built-in straza server's name, then the
// tool. The gateway records a draft as a digest and masks a described one,
// because its documents can hold a secret that the draft's own scan has
// not refused yet, so a tool record keeps no arguments of such a call.
// Under the gateway's own registration the call is gateway-proxied, which
// the callers check themselves.
func HoldsDraft(toolName string) bool {
	return strings.HasSuffix(toolName, "straza__draft_submit") || strings.HasSuffix(toolName, "straza__approval_request")
}
