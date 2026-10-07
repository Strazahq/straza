package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// auditArgsMax caps the tools/call arguments recorded per audit event:
// enough to see exactly what was asked of the tool, bounded so a giant
// payload cannot bloat the chain.
const auditArgsMax = 8 << 10

// auditRefused records a call the PDP never judged as a straza.audit.mcp
// deny whose reason is the refusal's own sentence and whose rule and set are
// empty, so an agent probing unbound or throttled tools reaches the SIEM. It
// reports whether the record entered the queue.
func (a *App) auditRefused(ctx context.Context, claims authn.Claims, ev policy.Event, target gwTarget, reason string) bool {
	return a.auditMCP(ctx, mcpAuditRecord{
		claims: claims, ev: ev, target: target,
		decision:   policy.Decision{Effect: policy.EffectDeny, Reason: reason},
		snapshotID: a.snapshots.Current().ID,
	})
}

// auditHidden records a call of a tool the session's overlay hides. The
// engine answers the same question the overlay asked, so the record names
// the denying rule. An allow means the overlay is stale, and the record then
// says the overlay hid the call, matching the -32602 the client got.
func (a *App) auditHidden(ctx context.Context, claims authn.Claims, sub policy.Subject, ev policy.Event, target gwTarget) {
	cur := a.snapshots.Current()
	d := cur.Engine.Evaluate(ev, sub)
	if d.Effect == policy.EffectAllow {
		d = policy.Decision{Effect: policy.EffectDeny, Reason: "hidden by the session's catalog overlay"}
	}
	a.auditMCP(ctx, mcpAuditRecord{claims: claims, ev: ev, decision: d, snapshotID: cur.ID, target: target})
}

// auditToolsList records one tools/list answer before it is served, as a
// straza.audit.mcp record with the event tools.list, effect allow, reason
// "catalog served", count equal to the tools in that answer, and app the
// server of a server endpoint, empty on /mcp. It returns spoolMCP's error,
// and the caller answers it through rpcFail.
func (a *App) auditToolsList(ctx context.Context, claims authn.Claims, app string, count int) error {
	return a.spoolMCP(ctx, mcpAuditRecord{
		claims:     claims,
		ev:         policy.Event{Kind: "tools.list", App: app},
		decision:   policy.Decision{Effect: policy.EffectAllow, Reason: "catalog served"},
		snapshotID: a.snapshots.Current().ID,
		listCount:  &count,
	})
}

// mcpAuditRecord is everything one straza.audit.mcp record names: the
// session, the event with its granted fact, the decision, the snapshot, the
// arguments, the access row that admitted the call and the credential row
// the upstream call uses (empty when none). listCount is set on a
// tools.list or resources.list record only: the number of tools or views
// that answer served. uri is set on a resources.read record only.
type mcpAuditRecord struct {
	claims       authn.Claims
	ev           policy.Event
	decision     policy.Decision
	snapshotID   string
	args         json.RawMessage
	target       gwTarget
	credentialID string
	// credentialSource says whose row the call ran on (own, sponsor or
	// shared) and credentialOwner names the sponsor's user id when it was
	// the sponsor's; both ride the record beside the credential id.
	credentialSource string
	credentialOwner  string
	listCount        *int
	uri              string
}

// auditMCP spools rec through spoolMCP and reports whether it entered the
// queue. A record that stays out logs the one fail-closed Error line of its
// call, and the caller must not let the call run.
func (a *App) auditMCP(ctx context.Context, rec mcpAuditRecord) bool {
	if err := a.spoolMCP(ctx, rec); err != nil {
		a.failClosed(ctx, "gateway", rec.decision.RuleID, err)
		return false
	}
	return true
}

// spoolMCP spools a straza.audit.mcp CloudEvent and returns the spool's
// error: under block a full queue holds it for at most the submit bound or
// until ctx ends. args is the verbatim tools/call arguments JSON (the MCP
// analog of the shell audit's exact command), capped at auditArgsMax with a
// truncation marker (spec/events §2 sanctions additional fields). Revision
// 23 adds granted, default, bindingId, role and credentialId, the last
// three only when set, and count rides on a tools.list record. A call of
// straza__draft_submit records argumentsDigest and argumentsSize in place of
// its arguments (revision 37), because the record is written before the
// handler can refuse a secret in a document, and an approval_request that
// describes one records its documents masked (maskDescribedSubmit). Revision
// 42 adds the resources.list and resources.read events, uri on the latter,
// capped as arguments are, with uriTruncated when cut.
func (a *App) spoolMCP(ctx context.Context, rec mcpAuditRecord) error {
	claims, ev, d, args := rec.claims, rec.ev, rec.decision, rec.args
	data := map[string]any{
		"session":  claims.Session,
		"user":     claims.Subject,
		"harness":  claims.Harness,
		"event":    ev.Kind,
		"tool":     ev.Tool,
		"app":      ev.App,
		"toolName": ev.ToolName,
		"effect":   d.Effect,
		"ruleId":   d.RuleID,
		"setName":  d.SetName,
		"reason":   d.Reason,
		"snapshot": rec.snapshotID,
		"granted":  ev.Granted,
		"default":  d.Default,
	}
	if rec.target.bindingID != "" {
		data["bindingId"] = rec.target.bindingID
		data["role"] = rec.target.role
	}
	if rec.credentialID != "" {
		data["credentialId"] = rec.credentialID
		data["credentialSource"] = rec.credentialSource
		if rec.credentialOwner != "" {
			data["credentialOwner"] = rec.credentialOwner
		}
	}
	if rec.listCount != nil {
		data["count"] = *rec.listCount
	}
	if rec.uri != "" {
		uri, cut := cappedURI(rec.uri)
		data["uri"] = uri
		if cut {
			data["uriTruncated"] = true
		}
	}
	if ev.App == nativeAppName && ev.ToolName == nativeToolApprovalRequest {
		args = maskDescribedSubmit(args)
	}
	switch {
	case len(args) == 0:
	case ev.App == nativeAppName && ev.ToolName == nativeToolDraftSubmit:
		data["argumentsDigest"], data["argumentsSize"] = argumentsDigest(args)
	case len(args) > auditArgsMax:
		data["arguments"] = string(args[:auditArgsMax])
		data["argumentsTruncated"] = true
	default:
		data["arguments"] = string(args)
	}
	ce, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          uuid.NewString(),
		"type":        "straza.audit.mcp",
		"source":      "strazad",
		"time":        time.Now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	})
	if err != nil {
		return nil
	}
	return a.audit.submit(ctx, store.OutboxEvent{Subject: "straza.audit.mcp", CE: string(ce)})
}
