package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
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

// MaxRecordValues is the most JSON values a record may hold and still be
// decoded in full when it is cut. Decoding costs time for every value, so a
// record of many tiny values costs about ten times what a record of one long
// string of the same length does. A longer record over it gets its data
// omitted by OmitCE, which never decodes the rest. A straza client's record
// holds a few dozen values.
const MaxRecordValues = 1 << 16

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
// out under the bound. A longer record of more than MaxRecordValues values
// gets its data omitted without being decoded. A ce that is not a JSON
// object, which no producer writes, is answered whole.
func boundCE(ce string) string {
	if len(ce) <= MaxRecordBytes {
		return ce
	}
	if CountValues(ce) > MaxRecordValues {
		return OmitCE(ce)
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

// BoundCE is boundCE for a producer that bounds its record before it queues
// it, so the cost lands on the request that made the record and not on the
// writer every record shares. The write's own bound then finds the record
// within MaxRecordBytes and keeps it whole.
func BoundCE(ce string) string { return boundCE(ce) }

// CountValues answers an upper bound of the JSON values in ce: one more
// than the commas, brackets and braces outside its strings. It reads ce
// byte by byte without decoding it, so a caller learns what decoding ce
// would cost before paying for it. On text that is not JSON the number
// means nothing.
func CountValues(ce string) int {
	n := 1
	for i := 0; i < len(ce); i++ {
		switch ce[i] {
		case '"':
			for i++; i < len(ce) && ce[i] != '"'; i++ {
				if ce[i] == '\\' {
					i++
				}
			}
		case ',', '[', '{':
			n++
		}
	}
	return n
}

// rawRecord is the type OmitCE decodes a record into: the envelopeKeys, and
// under data the keptKeys, each held as raw JSON, so the decoder skips every
// other value without building it. The JSON tags carry the keys, and the
// field names only number them. encoding/json matches a tag without regard
// to case, so a key that differs from a kept key only in case fills it too.
var rawRecord = func() reflect.Type {
	field := func(name, key string) reflect.StructField {
		return reflect.StructField{Name: name, Type: reflect.TypeFor[json.RawMessage](), Tag: reflect.StructTag(`json:"` + key + `"`)}
	}
	var kept, top []reflect.StructField
	for i, k := range keptKeys {
		kept = append(kept, field(fmt.Sprintf("K%d", i), k))
	}
	for i, k := range envelopeKeys {
		top = append(top, field(fmt.Sprintf("E%d", i), k))
	}
	return reflect.StructOf(append(top, reflect.StructField{Name: "Data", Type: reflect.StructOf(kept), Tag: `json:"data"`}))
}()

// OmitCE answers ce in the form boundCE gives a record whose strings it
// cannot cut: its envelopeKeys, the keptKeys of its data, each string cut to
// omitKeepMax, and dataOmitted with the length and the SHA-256 of ce. It
// decodes only those keys and skips every other value, so its cost grows
// with the bytes of ce and never with the count of its values. A ce that is
// not a JSON object is answered whole.
func OmitCE(ce string) string {
	if !strings.HasPrefix(strings.TrimLeft(ce, " \t\r\n"), "{") {
		return ce
	}
	v := reflect.New(rawRecord).Elem()
	// A data that is not an object is a type error the decoder reports at
	// the end, after it filled the envelope, and such a record keeps no
	// data fields.
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(ce), v.Addr().Interface()); err != nil && !errors.As(err, &typeErr) {
		return ce
	}
	env := rawScalars(v, envelopeKeys)
	data := rawScalars(v.Field(len(envelopeKeys)), keptKeys)
	return omitData(env, scalars(data, keptKeys), ce)
}

// rawScalars answers keys, read from the raw JSON fields of s in order,
// whose values are strings, numbers or booleans, decoded one by one.
func rawScalars(s reflect.Value, keys []string) map[string]any {
	out := map[string]any{}
	for i, k := range keys {
		raw := s.Field(i).Bytes()
		switch {
		case len(raw) == 0, raw[0] == '[', raw[0] == '{', raw[0] == 'n':
		case raw[0] == '"':
			var str string
			if json.Unmarshal(raw, &str) == nil {
				out[k] = str
			}
		case raw[0] == 't', raw[0] == 'f':
			out[k] = raw[0] == 't'
		default:
			out[k] = json.Number(raw)
		}
	}
	return out
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
