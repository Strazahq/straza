package objects

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
)

// The limits of one listing. A listing over any of them fails as a whole
// and the last good list stays.
const (
	maxObjects    = 100_000
	maxValueBytes = 512
	maxTitleBytes = 256
	maxAttrs      = 16
	maxAttrBytes  = 128
	maxAttrValues = 16
	// maxIDNumber is the largest id a server may send as a JSON number,
	// because the MCP client reads numbers as float64.
	maxIDNumber = 1 << 53
)

var wholeRe = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// collector gathers the pages of one listing and stops it at the first
// broken rule. keep names the attributes the declaration pins.
type collector struct {
	tool, typ, plural string
	keep              map[string]bool
	objs              []Object
	values, ids       map[string]int // index in objs by value and by id
}

func newCollector(spec manager.ObjectsSpec) *collector {
	c := &collector{tool: spec.List.Tool, typ: spec.Type, plural: spec.PluralWord(),
		keep: map[string]bool{}, values: map[string]int{}, ids: map[string]int{}}
	for _, a := range spec.List.Attributes {
		c.keep[a.Name] = true
	}
	return c
}

// wireItem is one item of a page as the server sent it.
type wireItem struct {
	Value      json.RawMessage            `json:"value"`
	ID         json.RawMessage            `json:"id"`
	Title      json.RawMessage            `json:"title"`
	Attributes map[string]json.RawMessage `json:"attributes"`
}

// page reads one answer of the listing tool, keeps its objects and answers
// its nextCursor, empty on the last page, or the reason the listing fails.
func (c *collector) page(res *mcp.CallToolResult) (next, reason string) {
	var wire struct {
		Objects    []json.RawMessage `json:"objects"`
		NextCursor *string           `json:"nextCursor"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil || res.StructuredContent == nil || json.Unmarshal(raw, &wire) != nil || wire.Objects == nil {
		return "", reasonShape(c.tool)
	}
	for _, rawItem := range wire.Objects {
		var item wireItem
		if json.Unmarshal(rawItem, &item) != nil {
			return "", reasonShape(c.tool)
		}
		o, reason := c.object(item)
		if reason == "" {
			reason = c.add(o)
		}
		if reason != "" {
			return "", reason
		}
	}
	if wire.NextCursor != nil {
		next = *wire.NextCursor
	}
	return next, ""
}

// add keeps o unless the listing read it already. A server that pages by
// number shifts its later pages when an object is made or deleted during
// the listing, so an exact repeat is dropped. A repeat that differs fails
// the listing, because the server named two objects alike.
func (c *collector) add(o Object) string {
	if i, ok := c.values[o.Value]; ok {
		prev := c.objs[i]
		switch {
		case prev.ID != o.ID:
			return fmt.Sprintf("%s returned %s twice with different ids.", c.tool, o.Value)
		case prev.Title != o.Title:
			return fmt.Sprintf("%s returned %s twice with different titles.", c.tool, o.Value)
		case !reflect.DeepEqual(prev.Attributes, o.Attributes):
			return fmt.Sprintf("%s returned %s twice with different attributes.", c.tool, o.Value)
		}
		return ""
	}
	if _, ok := c.ids[o.ID]; ok {
		return fmt.Sprintf("%s returned the id %s twice with different values.", c.tool, o.ID)
	}
	if len(c.objs) == maxObjects {
		return reasonLimit(c.tool, fmt.Sprintf("more than 100,000 %s", c.plural))
	}
	c.values[o.Value], c.ids[o.ID] = len(c.objs), len(c.objs)
	c.objs = append(c.objs, o)
	return ""
}

// object checks one item against the rules and limits and answers it as an
// object, or the reason the listing fails.
func (c *collector) object(item wireItem) (Object, string) {
	var o Object
	if json.Unmarshal(item.Value, &o.Value) != nil || o.Value == "" || !utf8.ValidString(o.Value) ||
		strings.ContainsRune(o.Value, utf8.RuneError) {
		return o, reasonLimit(c.tool, "a "+c.typ+" whose value is empty or not a string of valid UTF-8")
	}
	if len(o.Value) > maxValueBytes {
		return o, reasonLimit(c.tool, "a value longer than 512 bytes")
	}
	switch id := string(item.ID); {
	case id == "" || id == "null":
		o.ID = o.Value
	case strings.HasPrefix(id, `"`):
		if json.Unmarshal(item.ID, &o.ID) != nil || o.ID == "" {
			return o, reasonLimit(c.tool, "a "+c.typ+" with an empty id")
		}
	default:
		n, err := strconv.ParseUint(id, 10, 64)
		if !wholeRe.MatchString(id) || err != nil || n > maxIDNumber {
			return o, reasonLimit(c.tool, "an id that is neither a string nor a whole number from 0 to 2^53")
		}
		o.ID = id
	}
	if len(o.ID) > maxValueBytes {
		return o, reasonLimit(c.tool, "an id longer than 512 bytes")
	}
	if t := string(item.Title); t != "" && t != "null" {
		if json.Unmarshal(item.Title, &o.Title) != nil {
			return o, reasonLimit(c.tool, "a "+c.typ+" whose title is not a string")
		}
	}
	if len(o.Title) > maxTitleBytes {
		return o, reasonLimit(c.tool, "a title longer than 256 bytes")
	}
	if len(item.Attributes) > maxAttrs {
		return o, reasonLimit(c.tool, "a "+c.typ+" with more than 16 attributes")
	}
	o.Attributes = map[string]Attr{}
	for name, raw := range item.Attributes {
		if !c.keep[name] {
			continue
		}
		var a Attr
		if json.Unmarshal(raw, &a) != nil {
			return o, reasonLimit(c.tool, "an attribute that is neither a string nor a list of strings")
		}
		if len(a.Values) > maxAttrValues {
			return o, reasonLimit(c.tool, "an attribute with more than 16 values")
		}
		for _, v := range a.Values {
			if len(v) > maxAttrBytes {
				return o, reasonLimit(c.tool, "an attribute value longer than 128 bytes")
			}
		}
		o.Attributes[name] = a
	}
	return o, ""
}

// hashItem is one object as the list hash reads it.
type hashItem struct {
	Value      string          `json:"v"`
	ID         string          `json:"i"`
	Title      string          `json:"t"`
	Attributes map[string]Attr `json:"a"`
}

// listHash is the hash of a listing's objects sorted by value, which tells
// an unchanged listing from a changed one.
func listHash(sorted []Object) string {
	items := make([]hashItem, len(sorted))
	for i, o := range sorted {
		items[i] = hashItem{o.Value, o.ID, o.Title, o.Attributes}
	}
	b, _ := json.Marshal(items)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DeclHash is the hash of the parts of a pinned declaration a listing
// depends on: the type, the listing tool and the attributes. A change of
// any of them starts the server's list over.
func DeclHash(spec manager.ObjectsSpec) string {
	b, _ := json.Marshal(struct {
		Type       string                    `json:"type"`
		Tool       string                    `json:"tool"`
		Attributes []manager.ObjectAttribute `json:"attributes"`
	}{spec.Type, spec.List.Tool, spec.List.Attributes})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
