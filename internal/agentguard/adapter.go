package agentguard

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/adapters"
)

// Adapter is a parsed dialect mapping spec (adapters/<harness>.yaml).
type Adapter struct {
	Harness    string                         `yaml:"harness"`
	Events     map[string]string              `yaml:"events"`
	Fields     Fields                         `yaml:"fields"`
	Tools      map[string]string              `yaml:"tools"`
	ToolInputs map[string]map[string][]string `yaml:"toolInputs"`
}

// Fields names the payload keys carrying common metadata. Prompt and
// TranscriptPath are the conversation-capture inputs; a dialect that leaves
// them empty simply never captures (no verified key = no guessing).
type Fields struct {
	SessionID      string `yaml:"sessionId"`
	Workspace      string `yaml:"workspace"`
	ToolName       string `yaml:"toolName"`
	ToolInput      string `yaml:"toolInput"`
	Prompt         string `yaml:"prompt"`
	TranscriptPath string `yaml:"transcriptPath"`
	// PromptResponse is the payload key carrying the model's reply on a
	// turn-end event (gemini AfterAgent `prompt_response`). When set and
	// present, reply capture uses it directly, with no transcript reading.
	PromptResponse string `yaml:"promptResponse"`
	// McpContext is the payload key carrying a harness-provided MCP
	// server/tool split (gemini `mcp_context`: server_name + bare
	// tool_name). When present it overrides name-based classification:
	// gemini's top-level tool_name (mcp_<server>_<tool>, single
	// underscores) cannot be split back unambiguously.
	McpContext string `yaml:"mcpContext"`
	// Delegation keys. A dialect with no subagent events leaves all four
	// empty (gemini: vendor-documented absent) and the delegate lane degrades
	// to a no-op, the same contract as the capture keys above.
	AgentTranscriptPath string `yaml:"agentTranscriptPath"`
	AgentReply          string `yaml:"agentReply"`
	AgentID             string `yaml:"agentId"`
	AgentType           string `yaml:"agentType"`
}

// LoadAdapters parses every embedded dialect spec, keyed by harness name.
func LoadAdapters() (map[string]*Adapter, error) {
	entries, err := adapters.FS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read adapters: %w", err)
	}
	out := map[string]*Adapter{}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "embed.go" {
			continue
		}
		raw, err := adapters.FS.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var a Adapter
		if err := yaml.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if a.Harness == "" {
			return nil, fmt.Errorf("%s missing harness name", e.Name())
		}
		out[a.Harness] = &a
	}
	return out, nil
}

// Harnesses returns the sorted list of supported harness names.
func Harnesses(adapters map[string]*Adapter) []string {
	names := make([]string, 0, len(adapters))
	for n := range adapters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
