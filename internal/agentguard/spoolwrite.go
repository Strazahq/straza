package agentguard

import (
	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

// spoolAppend writes one audit CloudEvent for a decision, fsync'd. The record
// carries the FULL policy-relevant event (exact command, target paths,
// workspace, upstream tool for mcp.call, and the deciding snapshot) because
// an audit line that can't say what was actually attempted is not evidence
// (spec/events §2: `snapshot` is part of the audit.tool minimum; the rest are
// the spec's sanctioned additional fields).
func spoolAppend(s *spool.Spool, n Normalized, session, snapshot string, d policy.Decision) error {
	data := map[string]any{
		"session":  session,
		"harness":  harnessLabel(n.HarnessName, n.HarnessVersion),
		"event":    n.Event.Kind,
		"tool":     n.Event.Tool,
		"app":      n.Event.App,
		"command":  n.Event.Command,
		"effect":   d.Effect,
		"ruleId":   d.RuleID,
		"setName":  d.SetName,
		"reason":   d.Reason,
		"snapshot": snapshot,
	}
	if n.Event.ToolName != "" {
		data["toolName"] = n.Event.ToolName
	}
	if len(n.Event.Paths) > 0 {
		data["paths"] = n.Event.Paths
	}
	if n.Event.Workspace != "" {
		data["workspace"] = n.Event.Workspace
	}
	addAgentAttribution(data, n)
	return s.AppendCE("straza.audit.tool", data)
}

// spoolAppendCapture writes one captured conversation turn (spec/events rev 5,
// kind "prompt" or "reply"). Content arrives already redacted/capped by
// captureContent; hash witnesses the full original.
func spoolAppendCapture(s *spool.Spool, kind string, n Normalized, session, content, mode string, truncated bool, hash string) error {
	data := map[string]any{
		"session":     session,
		"harness":     harnessLabel(n.HarnessName, n.HarnessVersion),
		"content":     content,
		"mode":        mode,
		"truncated":   truncated,
		"contentHash": hash,
	}
	addAgentAttribution(data, n)
	return s.AppendCE("straza.audit."+kind, data)
}

// addAgentAttribution tags a CE with the delegate that produced it (spec/
// events rev 13). Root-lane events carry neither key: absence means the
// main agent, so the tag is only ever written when the harness attributed
// the payload.
func addAgentAttribution(data map[string]any, n Normalized) {
	if n.AgentType != "" {
		data["agentType"] = n.AgentType
	}
	if n.AgentID != "" {
		data["agentId"] = n.AgentID
	}
}
