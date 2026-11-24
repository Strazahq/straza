package manager

import (
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolInputRefused is the Warn line of tools left out of a server's
// inventory because their input schema is not an object schema.
const toolInputRefused = "manager: tools of an MCP server are not listed, because the input schema of each is not a JSON object of type object, which MCP requires and MCP clients fail a whole tool list over. " +
	"The server's author publishes an object schema for the tool's arguments, and an administrator then runs strazactl apps recheck for the server"

// objectInput answers the tool as the inventory lists it, and false for a
// tool whose input schema no catalog can list. MCP arguments are always a
// JSON object, so a missing schema is listed as the schema of any object,
// and an object schema that names no type is listed with the type object.
// Both are set on a copy of the tool. A schema that is not a JSON object,
// or that names any other type, is refused, because it describes arguments
// no call can carry.
func objectInput(t *mcp.Tool) (*mcp.Tool, bool) {
	with := map[string]any{}
	switch m := t.InputSchema.(type) {
	case nil:
	case map[string]any:
		if typ, typed := m["type"]; typ == "object" {
			return t, true
		} else if typed {
			return nil, false
		}
		maps.Copy(with, m)
	default:
		return nil, false
	}
	with["type"] = "object"
	c := *t
	c.InputSchema = with
	return &c, true
}

// withObjectInput answers tools as objectInput lists each, without the
// tools it refuses, so no catalog lists them and no call reaches them. An
// MCP client fails its whole tool list over one such schema, and the straza
// client's own server refuses to register the tool. A change of the
// left-out set logs toolInputRefused once, with the server and the tools'
// names in order, so a server that only lists them in another order logs
// nothing. It runs after withoutEchoes, so no name it logs holds the
// credential. A left-out tool that was listed before reads as inventory
// drift.
func (m *Manager) withObjectInput(inst *instance, tools []*mcp.Tool) []*mcp.Tool {
	out := make([]*mcp.Tool, 0, len(tools))
	var refused []string
	for _, t := range tools {
		if listed, ok := objectInput(t); ok {
			out = append(out, listed)
		} else {
			refused = append(refused, t.Name)
		}
	}
	slices.Sort(refused)
	key := strings.Join(refused, "\n")
	inst.mu.Lock()
	changed := key != inst.inputRefused
	inst.inputRefused = key
	inst.mu.Unlock()
	if changed && key != "" {
		m.opts.Log.Warn(toolInputRefused, "app", inst.app.Name, "tool", strings.Join(refused, ", "))
	}
	return out
}
