package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
)

// This file owns the built-in virtual app `straza`: five native MCP tools the
// gateway ORIGINATES (rather than proxies) on the same /mcp surface. They are
// actions like any other tool call: the PDP governs `mcp.call straza:*`, they
// appear in tools/list under the same default-deny discipline as a real app,
// and handleToolCall dispatches them here, not to the manager.
//
//   - approval_request(action, reason) opens (or dedupes onto) a TICKET for a
//     DESCRIBED concrete call, evaluated against the caller's own snapshot.
//     The fingerprint is computed server-side (EventKey, the same key the PEP
//     binds), never from the client: free text is rejected, no advisory mode.
//   - approval_status(ref) reads one ticket the caller owns.
//   - approval_await(ref, max_wait_seconds) waits server-side, capped at 60s.
//   - draft_submit and draft_status are native_drafts.go's, for holders of
//     straza-draft-config. Deciding and publishing are never tools.

const (
	// nativeAppName is the reserved built-in virtual-app name. It namespaces the
	// native tools as `straza__<tool>` in the catalog and is what a PDP rule
	// authorizes (apps: [straza]). There is no apps-table row for it; the tools
	// are originated in-process and gated by policy, not a tool binding.
	nativeAppName = "straza"

	nativeToolApprovalRequest = "approval_request"
	nativeToolApprovalStatus  = "approval_status"
	nativeToolApprovalAwait   = "approval_await"
	nativeToolDraftSubmit     = "draft_submit"
	nativeToolDraftStatus     = "draft_status"

	// awaitDefaultSeconds / awaitMaxSeconds bound the server-side wait so a slow
	// agent cannot pin a gateway goroutine for the day-scale ticketTTL.
	awaitDefaultSeconds = 30
	awaitMaxSeconds     = 60
)

// nativeTool is a built-in catalog entry: a bare (un-namespaced) tool the
// gateway originates. InputSchema is a plain map so it ships verbatim in
// tools/list (no upstream jsonschema round-trip).
type nativeTool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
}

// nativeStrazaTools is the built-in `straza` app's tool inventory. The
// `action` predicate is a CONCRETE call (tool + app/tool_name or
// command/argv), never free prose.
var nativeStrazaTools = []nativeTool{
	{
		Name:        nativeToolApprovalRequest,
		Title:       "Request approval (ticket)",
		Description: "Open a long-running approval ticket for a described concrete call and return its reference. Non-blocking: a person decides within the ticket window while you do other work; the ticket's approval is consumed by a later real call that matches it. The action must be a concrete call (free-text requests are rejected).",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"action"},
			"properties": map[string]any{
				"action": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"tool"},
					"description":          "The concrete call to pre-authorize. The server fingerprints it (never trusts a client hash) so a later real call matches this approval.",
					"properties": map[string]any{
						"tool":      map[string]any{"type": "string", "description": "Canonical tool identity, e.g. mcp.call or shell.exec."},
						"app":       map[string]any{"type": "string", "description": "mcp.call: the upstream MCP server name."},
						"tool_name": map[string]any{"type": "string", "description": "mcp.call: the upstream tool name."},
						"args":      map[string]any{"type": "object", "description": "mcp.call: the EXACT tool arguments the later real call will use. Required when the gating rule covers the exact call (the default); the approval is consumable only by a call with these arguments."},
						"command":   map[string]any{"type": "string", "description": "shell.exec: the exact command line."},
						"argv":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional parsed argv."},
					},
				},
				"reason": map[string]any{"type": "string", "description": "Why this action is needed; the approver reads it. Advisory only: it gives no bypass."},
			},
		},
	},
	{
		Name:        nativeToolApprovalStatus,
		Title:       "Approval status",
		Description: "Read the current state of one approval ticket you own. No side effects.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"ref"},
			"properties": map[string]any{
				"ref": map[string]any{"type": "string", "description": "Ticket reference returned by approval_request."},
			},
		},
	},
	{
		Name:        nativeToolApprovalAwait,
		Title:       "Await approval",
		Description: "Block up to a bounded wait (capped at 60s, default 30s) for a ticket to be decided, for when you would rather wait than park. A timeout is not a deny; the ticket stays open.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"ref"},
			"properties": map[string]any{
				"ref":              map[string]any{"type": "string", "description": "Ticket reference returned by approval_request."},
				"max_wait_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": awaitMaxSeconds, "default": awaitDefaultSeconds, "description": "Requested wait bound; the server clamps it to [1,60] (default 30)."},
			},
		},
	},
	{
		Name:        nativeToolDraftSubmit,
		Title:       "Submit a config draft",
		Description: "Store a draft of MCP server, role and policy set documents for a person to review and publish. It answers at once with the draft's id. Read the server's check with straza__draft_status. You cannot publish.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"documents"},
			"properties": map[string]any{
				"documents": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"},
					"description": "App, Role, PolicySet and Removal documents as YAML texts, one or more documents in each text."},
				"note":  map[string]any{"type": "string", "description": "Why you propose this draft, for the person who reviews it."},
				"draft": map[string]any{"type": "string", "description": "Your own open draft to revise instead of making a new one."},
			},
		},
	},
	{
		Name:        nativeToolDraftStatus,
		Title:       "Read a config draft's check",
		Description: "Read the state of one draft you submitted and the server's check of its latest revision. No side effects.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"draft"},
			"properties": map[string]any{
				"draft": map[string]any{"type": "string", "description": "The draft's id, which straza__draft_submit answered."},
			},
		},
	},
}

// appendNativeCatalog originates the built-in `straza` tools into a tier-1
// catalog for the role set roles. Unlike a proxied app (visibility = role
// bindings), the native tools need no binding row; there is no apps-table row
// to bind (the tool_bindings FK would reject a synthetic id). The approval
// tools are candidates for every role set, and the drafting tools only for
// one holding straza-draft-config (nativeListed). The per-session tier-2
// overlay (buildOverlay) then HIDES an approval tool unless the session's
// policy authorizes `mcp.call straza:<tool>`, so default-deny holds.
func (a *App) appendNativeCatalog(c *sessionCatalog, roles []string) {
	for _, nt := range nativeStrazaTools {
		if !nativeListed(nt.Name, roles) {
			continue
		}
		namespaced := nativeAppName + "__" + nt.Name
		c.targets[namespaced] = gwTarget{app: nativeAppName, tool: nt.Name}
		c.tools = append(c.tools, gwTool{
			Name: namespaced, Title: nt.Title, Description: nt.Description, InputSchema: nt.InputSchema,
		})
	}
}

// callNativeStraza dispatches a governed tools/call for the built-in app to its
// native handler. The PDP has already allowed the call (and any approve gate has
// resolved) upstream in handleToolCall; this only originates the tool result.
func (a *App) callNativeStraza(ctx context.Context, tool string, args json.RawMessage, claims authn.Claims, sub policy.Subject) *mcp.CallToolResult {
	switch tool {
	case nativeToolApprovalRequest:
		return a.nativeApprovalRequest(ctx, args, claims, sub)
	case nativeToolApprovalStatus:
		return a.nativeApprovalStatus(ctx, args, claims)
	case nativeToolApprovalAwait:
		return a.nativeApprovalAwait(ctx, args, claims)
	case nativeToolDraftSubmit:
		return a.nativeDraftSubmit(ctx, args, claims)
	case nativeToolDraftStatus:
		return a.nativeDraftStatus(ctx, args, claims)
	default:
		return nativeToolError("Straza: unknown built-in tool " + tool)
	}
}

// nativeAction is the described call approval_request opens a ticket for.
type nativeAction struct {
	Tool     string          `json:"tool"`
	App      string          `json:"app"`
	ToolName string          `json:"tool_name"`
	Args     json.RawMessage `json:"args"` // mcp.call: exact tool arguments (fingerprint v2)
	Command  string          `json:"command"`
	Argv     []string        `json:"argv"`
}

// event builds the policy Event for the described call, or (_, false) when the
// action is not a CONCRETE call, meaning a tool plus at least one identity
// field (app+tool_name for mcp.call, or a command/argv). Free-text or a bare
// tool is rejected, never opened as an advisory ticket.
func (act nativeAction) event() (policy.Event, bool) {
	if act.Tool == "" {
		return policy.Event{}, false
	}
	concrete := (act.App != "" && act.ToolName != "") || act.Command != "" || len(act.Argv) > 0
	if !concrete {
		return policy.Event{}, false
	}
	ev := policy.Event{
		Kind: policy.EventToolPre, Tool: act.Tool, App: act.App, ToolName: act.ToolName,
		Command: act.Command, Argv: act.Argv, Args: act.Args,
	}
	tagInterpreter(&ev) // recompute the interpreter tag server-side, as the PEP does
	return ev, true
}

type nativeApprovalRequestArgs struct {
	Action nativeAction `json:"action"`
	Reason string       `json:"reason"`
}

// nativeApprovalRequest opens (or dedupes onto) an approval ticket for a
// described concrete call. It resolves the caller's identity from the session
// subject, evaluates the CURRENT policy snapshot for the described call, and
// requires the gating rule to be class=ticket: a plain allow needs no approval,
// a plain deny is refused, and a hold rule is refused (holds are interactive).
// The fingerprint (EventKey) is computed server-side and the ticket rides the
// same dedupe path the PEP uses, so a later real call binds to it. Fail closed.
func (a *App) nativeApprovalRequest(ctx context.Context, args json.RawMessage, claims authn.Claims, sub policy.Subject) *mcp.CallToolResult {
	var in nativeApprovalRequestArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nativeToolError("Straza: approval_request arguments are not valid JSON")
	}
	ev, ok := in.Action.event()
	if !ok {
		return nativeToolError("Straza: a concrete call is required. approval_request needs action.tool plus an app+tool_name, a command, or argv (free-text requests are rejected)")
	}

	// A described drafting call carries the granted fact the gateway would
	// give it, so a holder is not told that a call it may make is denied.
	if ev.Tool == policy.ToolMCPCall && draftingTool(gwTarget{app: ev.App, tool: ev.ToolName}) {
		ev.Granted = a.hasAccess(sub.Roles, ev.App, ev.ToolName)
	}
	d := a.snapshots.Current().Engine.Evaluate(ev, sub)
	switch {
	case d.Effect != policy.EffectAllow:
		reason := d.Reason
		if reason == "" {
			reason = "no rule allows it"
		}
		return nativeToolError("Straza: denied by policy. The described call would be denied (" + reason + "); an approval ticket cannot override a deny")
	case d.Approve == nil:
		return nativeToolError("Straza: no approval needed. The described call is already allowed by policy; just make the call")
	case d.Approve.Class != policy.ClassTicket:
		return nativeToolError("Straza: that action is gated by an interactive approval (a hold), not a ticket. A hold is decided in-line while the real call blocks. Make the real call and a person is prompted; approval_request opens day-scale tickets only")
	}

	// Fingerprint v2 taxonomy: under a call-bound rule (the default), a
	// described mcp action MUST carry the exact args: a tool-scoped key here
	// would never match the later real call, a footgun the model cannot see.
	if ev.Tool == policy.ToolMCPCall && ev.Args == nil && d.Approve.Binding != policy.ApproveBindingTool {
		return nativeToolError("Straza: this rule's approval covers the exact call. approval_request needs action.args (the exact tool arguments the later real call will use); without them the approved ticket could never be consumed")
	}
	key, err := approval.EventKeyV2(ev, d.Approve.Binding)
	if err != nil {
		return nativeToolError("Straza: " + fingerprintUnavailableReason(err))
	}

	username := claims.Subject
	if sub.User != "" {
		username = sub.User
	}
	// Preview the described call: mcp args ride the same bounded redacted
	// preview the gateway lane uses, a described draft_submit its digest;
	// shell-shaped actions preview from the command.
	prev := a.commandArgsPreview(ev.Command)
	if ev.Tool == policy.ToolMCPCall && ev.Args != nil {
		prev = a.callPreview(ev.App, ev.ToolName, ev.Args)
	}
	rec, err := a.approval.Request(ctx, approval.RequestInput{
		SessionID: claims.Session, UserID: claims.Subject, Username: username,
		RuleID: d.RuleID, SetName: d.SetName,
		ArgvHash: key, Lane: "gateway",
		Summary: approveSummary(ev), Justification: in.Reason,
		ArgsPreview: prev.preview, ArgsTruncated: prev.truncated, ArgsBytes: prev.bytes,
		Spec:    *d.Approve,
		Confirm: d.Confirm,
	})
	if err != nil {
		a.failClosed(ctx, "native", d.RuleID, err)
		return nativeToolError(a.approveRequestDenyReason(err))
	}
	return nativeToolOK(map[string]any{
		"ref":        rec.ID,
		"state":      string(rec.State),
		"expires_at": rec.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

type nativeRefArgs struct {
	Ref string `json:"ref"`
}

// nativeApprovalStatus reads one ticket the caller owns. Another user's ref (or
// an unknown ref) is a not-found error; a requester never learns another user's
// ticket exists.
func (a *App) nativeApprovalStatus(ctx context.Context, args json.RawMessage, claims authn.Claims) *mcp.CallToolResult {
	var in nativeRefArgs
	if err := json.Unmarshal(args, &in); err != nil || in.Ref == "" {
		return nativeToolError("Straza: approval_status requires a ref")
	}
	rec, err := a.approval.Get(ctx, in.Ref)
	if err != nil || rec.UserID != claims.Subject {
		return nativeToolError("Straza: no such approval " + in.Ref)
	}
	return nativeToolOK(nativeStatusPayload(rec))
}

type nativeAwaitArgs struct {
	Ref            string `json:"ref"`
	MaxWaitSeconds int    `json:"max_wait_seconds"`
}

// nativeApprovalAwait blocks up to a bounded wait (capped at 60s, default 30s)
// for a ticket the caller owns to be decided, returning the status shape plus a
// timed_out flag. An already-resolved ticket returns at once; a bound that
// elapses returns state=pending (timed_out=true); a timeout is not a deny.
func (a *App) nativeApprovalAwait(ctx context.Context, args json.RawMessage, claims authn.Claims) *mcp.CallToolResult {
	var in nativeAwaitArgs
	if err := json.Unmarshal(args, &in); err != nil || in.Ref == "" {
		return nativeToolError("Straza: approval_await requires a ref")
	}
	rec, err := a.approval.Get(ctx, in.Ref)
	if err != nil || rec.UserID != claims.Subject {
		return nativeToolError("Straza: no such approval " + in.Ref)
	}
	if rec.State != approval.StatePending {
		return nativeToolOK(withTimedOut(nativeStatusPayload(rec), false))
	}

	wctx, cancel := context.WithTimeout(ctx, time.Duration(clampAwaitSeconds(in.MaxWaitSeconds))*time.Second)
	defer cancel()
	final, err := a.approval.Await(wctx, in.Ref)
	if err != nil {
		// The bound elapsed (or ctx canceled) with no decision: re-read and report
		// the current (still-pending) state. Never a deny.
		cur := rec
		if again, e := a.approval.Get(ctx, in.Ref); e == nil {
			cur = again
		}
		return nativeToolOK(withTimedOut(nativeStatusPayload(cur), cur.State == approval.StatePending))
	}
	return nativeToolOK(withTimedOut(nativeStatusPayload(final), final.State == approval.StatePending))
}

// nativeStatusPayload is the shared status view (approval_status and
// approval_await). The use fields, grant_expires_at, consumed_at and
// consumed_by, the session that used the approval, show on a hold and a
// ticket once set; null-valued ones are omitted. decided_by is the
// approver's display name.
func nativeStatusPayload(rec approval.Record) map[string]any {
	m := map[string]any{
		"ref":        rec.ID,
		"state":      string(rec.State),
		"expires_at": rec.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if rec.Class != "" {
		m["class"] = rec.Class
	}
	if rec.GrantExpiresAt != nil {
		m["grant_expires_at"] = rec.GrantExpiresAt.UTC().Format(time.RFC3339)
	}
	if rec.ConsumedAt != nil {
		m["consumed_at"] = rec.ConsumedAt.UTC().Format(time.RFC3339)
	}
	if rec.ConsumedBy != "" {
		m["consumed_by"] = rec.ConsumedBy
	}
	if rec.DecidedByName != "" {
		m["decided_by"] = rec.DecidedByName
	}
	return m
}

func withTimedOut(m map[string]any, timedOut bool) map[string]any {
	m["timed_out"] = timedOut
	return m
}

// clampAwaitSeconds bounds a requested await to [1,60], defaulting a
// non-positive request to 30.
func clampAwaitSeconds(n int) int {
	if n <= 0 {
		return awaitDefaultSeconds
	}
	if n > awaitMaxSeconds {
		return awaitMaxSeconds
	}
	return n
}

// nativeToolError builds an MCP tool-level error result carrying a Straza
// reason; the model reads it and adapts (same shape a policy deny uses).
func nativeToolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// nativeToolOK builds a successful tool result: the structured object plus a
// JSON text rendering for clients that read only text content.
func nativeToolOK(payload map[string]any) *mcp.CallToolResult {
	text, _ := json.Marshal(payload)
	return &mcp.CallToolResult{
		StructuredContent: payload,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
	}
}
