package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxRecordBytes is the most bytes the CloudEvent of one outbox row holds
// once boundCE has run. The event bus refuses a message over its maximum
// payload, 1 MiB by default on NATS, so the relay could never publish a
// larger row. A quarter of that leaves room for a parked dead-letter
// record, which carries the record whole beside its own fields, and for a
// bus that an operator set lower.
const MaxRecordBytes = 256 << 10

// omitKeepMax is the most bytes, as JSON, of one string that a record
// whose data is omitted keeps.
const omitKeepMax = 256

// cutMark is the text that follows the prefix of a cut string: the
// string's whole length in bytes and the hex SHA-256 of the whole string.
const cutMark = "... (%d bytes, sha256 %s)"

// keptKeys are the data fields a record keeps when cutting its strings
// cannot bring it under MaxRecordBytes: who acted, on what, with what
// outcome and reason, and the command, workspace and arguments it ran.
var keptKeys = []string{
	"action", "actor", "actorId", "actorVia", "agentId", "agentType", "app", "approvalId", "arguments",
	"command", "decidedBy", "device", "effect", "event", "harness", "outcome", "phase", "reason", "ruleId",
	"session", "setName", "snapshot", "state", "target", "tool", "toolName", "user", "userId", "via", "workspace",
}

// envelopeKeys are the CloudEvents attributes a record whose data is
// omitted keeps.
var envelopeKeys = []string{"specversion", "id", "type", "source", "time"}

// boundRecord is the bound every outbox write applies, boundCE, kept in a
// variable so a test can check that a write calls it before its
// transaction opens.
var boundRecord = boundCE

// boundCE answers ce whole when it holds at most MaxRecordBytes, so a
// record under the bound is stored byte for byte as its producer wrote it.
// A longer record is decoded, and its longest strings inside data are cut
// to one common length, the largest that brings the record under the
// bound, each to a prefix on a character boundary followed by cutMark.
// When no length can, the record keeps its envelopeKeys and the keptKeys
// of its data, each string cut to omitKeepMax, and dataOmitted gives the
// length and the SHA-256 of the whole record, so every JSON object comes
// out under the bound. A ce that is not a JSON object, which no producer
// writes, is answered whole.
func boundCE(ce string) string {
	if len(ce) <= MaxRecordBytes {
		return ce
	}
	dec := json.NewDecoder(strings.NewReader(ce))
	dec.UseNumber()
	var env map[string]any
	if dec.Decode(&env) != nil || env == nil {
		return ce
	}
	keep := keptOf(env["data"])
	if out, ok := cutStrings(env, len(ce)); ok {
		return out
	}
	return omitData(env, keep, ce)
}

// leaf is one string inside a record's data and the way to replace it.
type leaf struct {
	v string
	// enc is the string's length as JSON, quotes included, and floor the
	// length of its cut with an empty prefix.
	enc, floor int
	set        func(string)
}

// leaves appends every string inside v, in key order, to out.
func leaves(v any, set func(string), out []leaf) []leaf {
	switch t := v.(type) {
	case string:
		return append(out, leaf{v: t, enc: jsonLen(t), floor: 2 + len(fmt.Sprintf(cutMark, len(t), "")) + 2*sha256.Size, set: set})
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			out = leaves(t[k], func(s string) { t[k] = s }, out)
		}
	case []any:
		for i := range t {
			out = leaves(t[i], func(s string) { t[i] = s }, out)
		}
	}
	return out
}

// cutStrings cuts every string in env's data that is longer than a common
// length c to at most c bytes as JSON, with c the largest length at which
// the record fits. size is the record's length as encoding/json writes
// it, which a producer's json.Marshal output already is. It answers false
// when even cutting every string to its floor leaves the record too long.
func cutStrings(env map[string]any, size int) (string, bool) {
	ls := leaves(env["data"], func(s string) { env["data"] = s }, nil)
	rest := size
	top := 0
	for _, l := range ls {
		rest -= l.enc
		top = max(top, l.enc)
	}
	fits := func(c int) bool {
		n := rest
		for _, l := range ls {
			n += min(l.enc, max(c, l.floor))
		}
		return n <= MaxRecordBytes
	}
	c := sort.Search(top+1, func(c int) bool { return !fits(c) }) - 1
	if c < 0 {
		return "", false
	}
	for _, l := range ls {
		if room := max(c, l.floor); l.enc > room {
			l.set(cutValue(l.v, room))
		}
	}
	out, err := json.Marshal(env)
	if err != nil || len(out) > MaxRecordBytes {
		return "", false
	}
	return string(out), true
}

// cutValue answers the longest prefix of v, on a character boundary, that
// with cutMark after it takes at most budget bytes as JSON, and an empty
// prefix when even cutMark does not fit.
func cutValue(v string, budget int) string {
	sum := sha256.Sum256([]byte(v))
	mark := fmt.Sprintf(cutMark, len(v), hex.EncodeToString(sum[:]))
	room := budget - 2 - len(mark)
	n := 0
	for used := 0; n < len(v); {
		r, size := utf8.DecodeRuneInString(v[n:])
		w := runeJSON(r, size)
		if used+w > room {
			break
		}
		used += w
		n += size
	}
	return v[:n] + mark
}

// keptOf copies the keptKeys of data whose values are strings, numbers or
// booleans.
func keptOf(data any) map[string]any {
	m, _ := data.(map[string]any)
	return scalars(m, keptKeys)
}

// scalars answers the keys of m whose values are strings, numbers or
// booleans, each string cut to omitKeepMax bytes as JSON. The cut is
// deterministic, so an id cut this way is the same on every upload of the
// record and still dedupes.
func scalars(m map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if jsonLen(v) > omitKeepMax {
				v = cutValue(v, omitKeepMax)
			}
			out[k] = v
		case json.Number, bool:
			out[k] = v
		}
	}
	return out
}

// omitData answers the record of env with its envelopeKeys and keep as
// data, and dataOmitted naming the length and the SHA-256 of ce, the whole
// record.
func omitData(env, keep map[string]any, ce string) string {
	sum := sha256.Sum256([]byte(ce))
	keep["dataOmitted"] = fmt.Sprintf("%d bytes, sha256 %s", len(ce), hex.EncodeToString(sum[:]))
	out := scalars(env, envelopeKeys)
	out["data"] = keep
	b, err := json.Marshal(out)
	if err != nil {
		return ce
	}
	return string(b)
}

// asciiJSON holds the bytes encoding/json writes for each ASCII byte inside
// a string, read from encoding/json itself so the two cannot drift.
var asciiJSON = func() (t [utf8.RuneSelf]int) {
	for b := range t {
		out, _ := json.Marshal(string(rune(b)))
		t[b] = len(out) - 2
	}
	return t
}()

// runeJSON answers the bytes encoding/json writes for the rune r, read as
// size bytes, inside a string: an invalid byte becomes \ufffd, U+2028 and
// U+2029 are escaped, and every other rune is written as it is.
func runeJSON(r rune, size int) int {
	switch {
	case r < utf8.RuneSelf:
		return asciiJSON[r]
	case r == utf8.RuneError && size == 1, r == '\u2028', r == '\u2029':
		return len(`\ufffd`)
	}
	return size
}

// jsonLen answers the length of s as encoding/json writes it, quotes
// included.
func jsonLen(s string) int {
	n := 2
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		n += runeJSON(r, size)
		i += size
	}
	return n
}
