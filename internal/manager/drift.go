package manager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxChangedShown is how many changed tools a drift reason names before it
// counts the rest, so a server with a long tool list cannot make the reason
// long.
const maxChangedShown = 10

// hintNames are the annotation hints the drift check covers, in the order
// hintsOf answers them and a drift reason prints them.
var hintNames = [4]string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"}

// noHints is hintsOf of a tool whose server sent no hint.
var noHints = [4]string{"false", "unset", "false", "unset"}

// hintsOf answers the value of each of hintNames on t as a word. A hint the
// protocol gives no absent form reads true or false, and destructiveHint and
// openWorldHint read unset when the server left them out, because Straza
// reads an absent one as no hint and not as its default.
func hintsOf(t *mcp.Tool) [4]string {
	a := t.Annotations
	if a == nil {
		return noHints
	}
	word := func(b *bool) string {
		if b == nil {
			return "unset"
		}
		return strconv.FormatBool(*b)
	}
	return [4]string{strconv.FormatBool(a.ReadOnlyHint), word(a.DestructiveHint), strconv.FormatBool(a.IdempotentHint), word(a.OpenWorldHint)}
}

// marksOf answers the ai.straza/* marks of t as one JSON object with its
// keys sorted, or "" for a tool with none.
func marksOf(t *mcp.Tool) string {
	marks := map[string]any{}
	for k, v := range t.Meta {
		if strings.HasPrefix(k, markPrefix) {
			marks[k] = v
		}
	}
	if len(marks) == 0 {
		return ""
	}
	b, _ := json.Marshal(marks)
	return string(b)
}

// hashRecord writes one record of the drift hash: its kind, then each
// field, every one behind its own length. A reader of the bytes knows where
// each field ends without looking inside it, so a field that holds a
// separator or the text of another record changes nothing around it.
func hashRecord(h io.Writer, kind string, fields ...string) {
	fmt.Fprintf(h, "%d:%s%d;", len(kind), kind, len(fields))
	for _, f := range fields {
		fmt.Fprintf(h, "%d:%s", len(f), f)
	}
}

// toolChange is one tool that two listings both hold, with the parts of it
// that differ, in the words a drift reason prints.
type toolChange struct {
	name  string
	parts []string
}

// changedTools answers, in the order of next, each tool of next that prev
// holds under the same name with another description, input schema, set of
// marks or annotation hint. Those are the parts of a tool inventoryHash
// covers on every server, so a tool reads as changed exactly when they move
// the hash. A changed view link is linksChanged's to report.
func changedTools(prev, next []*mcp.Tool) []toolChange {
	was := make(map[string]*mcp.Tool, len(prev))
	for _, t := range prev {
		was[t.Name] = t
	}
	var out []toolChange
	for _, t := range next {
		p, ok := was[t.Name]
		if !ok {
			continue
		}
		var parts []string
		if p.Description != t.Description {
			parts = append(parts, "description")
		}
		ps, _ := json.Marshal(p.InputSchema)
		ts, _ := json.Marshal(t.InputSchema)
		if !bytes.Equal(ps, ts) {
			parts = append(parts, "input schema")
		}
		if marksOf(p) != marksOf(t) {
			parts = append(parts, "marks")
		}
		ph, th := hintsOf(p), hintsOf(t)
		for i, name := range hintNames {
			if ph[i] != th[i] {
				parts = append(parts, name+" is now "+th[i])
			}
		}
		if len(parts) > 0 {
			out = append(out, toolChange{name: t.Name, parts: parts})
		}
	}
	return out
}

// changedNames answers the names of changed in their order, and an empty
// list for none, so an event prints [] and never null.
func changedNames(changed []toolChange) []string {
	names := make([]string, 0, len(changed))
	for _, c := range changed {
		names = append(names, c.name)
	}
	return names
}

// changedWords words changed for a drift reason: each tool with its parts
// in parentheses, and past maxChangedShown tools the count of the rest.
func changedWords(changed []toolChange) string {
	shown := changed[:min(len(changed), maxChangedShown)]
	words := make([]string, 0, len(shown))
	for _, c := range shown {
		words = append(words, fmt.Sprintf("%s (%s)", c.name, strings.Join(c.parts, ", ")))
	}
	out := strings.Join(words, ", ")
	if rest := len(changed) - len(shown); rest > 0 {
		out += fmt.Sprintf(" and %d more", rest)
	}
	return out
}
