package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// auditArgsMax caps the tools/call arguments recorded per audit event:
// enough to see exactly what was asked of the tool, bounded so a giant
// payload cannot bloat the chain.
const auditArgsMax = 8 << 10

// unresolvedNameMax is the most bytes of a server or tool name the catalog
// did not resolve that a refusal and its record quote.
const unresolvedNameMax = 256

// cutUnresolved answers a server or tool name the catalog did not resolve
// as a refusal and its record quote it, cut by cutQuoted at
// unresolvedNameMax bytes. A record of a name the catalog resolved names
// it whole.
func cutUnresolved(name string) string {
	return cutQuoted(name, unresolvedNameMax)
}

// cutQuoted answers v, a string from the caller that a refusal and its
// record quote, whole up to max bytes, and past that cut on a rune boundary
// and followed by "... (<n> bytes)", where n is the full length of v. Such
// a string is bounded only by the request body, and the record escapes each
// <, > and & into six bytes, so the cut keeps one refusal's record small.
func cutQuoted(v string, max int) string {
	cut, ok := objects.Cut(v, max)
	if !ok {
		return v
	}
	return fmt.Sprintf("%s... (%d bytes)", cut, len(v))
}

// describedReason is the reason that approval_request's refusal quotes for
// the decision d of ev, the call it describes: the engine's own, except for
// a default decision of a tool, app or tool name longer than
// unresolvedNameMax. The engine's default reason quotes the caller's names,
// so it is worded again from a copy of ev with each name cut by
// cutUnresolved. The decision itself stays on the names as sent, because a
// rule's glob may match a long name and the later real call carries them.
// When the copy is not decided by the same default, its reason is not d's,
// so the answer is empty and the caller says that no rule allows the call.
func (a *App) describedReason(ev policy.Event, sub policy.Subject, d policy.Decision) string {
	shown := ev
	shown.Tool, shown.App, shown.ToolName = cutUnresolved(ev.Tool), cutUnresolved(ev.App), cutUnresolved(ev.ToolName)
	if !d.Default || (shown.Tool == ev.Tool && shown.App == ev.App && shown.ToolName == ev.ToolName) {
		return d.Reason
	}
	if c := a.snapshots.Current().Engine.Evaluate(shown, sub); c.Default && c.Effect == d.Effect {
		return c.Reason
	}
	return ""
}

// auditRefused records refusedRecord and reports whether the record
// entered the queue.
func (a *App) auditRefused(ctx context.Context, claims authn.Claims, ev policy.Event, target gwTarget, reason string) bool {
	return a.auditMCP(ctx, a.refusedRecord(claims, ev, target, reason))
}

// refusedRecord is the straza.audit.mcp record of a call the PDP never
// judged: a deny whose reason is the refusal's own sentence and whose rule
// and set are empty, so an agent probing unbound or throttled tools reaches
// the SIEM.
func (a *App) refusedRecord(claims authn.Claims, ev policy.Event, target gwTarget, reason string) mcpAuditRecord {
	return mcpAuditRecord{
		claims: claims, ev: ev, target: target,
		decision:   policy.Decision{Effect: policy.EffectDeny, Reason: reason},
		snapshotID: a.snapshots.Current().ID,
	}
}

// gateRefusedRecord is the record of a call the object gate refused: a deny
// with the refusal as reason, no rule, set, role or binding, granted false,
// the object the call names and the held roles that bind the tool in
// rolesChecked.
func (a *App) gateRefusedRecord(claims authn.Claims, ev policy.Event, target gwTarget, args json.RawMessage, v objects.Verdict, reason string) mcpAuditRecord {
	ev.Granted = false
	checked := v.Checked
	if checked == nil {
		checked = []string{}
	}
	return mcpAuditRecord{
		claims: claims, ev: ev, args: args,
		target:     gwTarget{app: target.app, tool: target.tool},
		decision:   policy.Decision{Effect: policy.EffectDeny, Reason: reason},
		snapshotID: a.snapshots.Current().ID,
		object:     v.Object, rolesChecked: checked,
	}
}

// hiddenRecord is the record of a call of a tool the session's overlay
// hides. The engine answers the same question the overlay asked, so the
// record names the denying rule. An allow means the overlay is stale, and
// the record then says the overlay hid the call, matching the -32602 the
// client got.
func (a *App) hiddenRecord(claims authn.Claims, sub policy.Subject, ev policy.Event, target gwTarget) mcpAuditRecord {
	cur := a.snapshots.Current()
	d := cur.Engine.Evaluate(ev, sub)
	if d.Effect == policy.EffectAllow {
		d = policy.Decision{Effect: policy.EffectDeny, Reason: "hidden by the session's catalog overlay"}
	}
	return mcpAuditRecord{claims: claims, ev: ev, decision: d, snapshotID: cur.ID, target: target}
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
	// object is the object the call names on a server that pins an object
	// type, and rolesChecked the held roles that bind the tool when the
	// object gate refused the call.
	object       *objects.Hit
	rolesChecked []string
	// event replaces ev.Kind as the record's event when set: grant on the
	// git grant route, whose engine event stays tool.pre. id is the
	// CloudEvents id when the caller chose it, as the grant route does to
	// sign it into the grant's dec claim. grant is the grant member of an
	// issued git grant's record.
	event string
	id    string
	grant *grantMember
}

// grantMember is the grant member of the record of an issued git grant
// (spec/events revision 48): the grant's id, op, audience and expiry, and
// the approval request it used, empty when no approval ran.
type grantMember struct {
	jti, op, aud, approval string
	exp                    time.Time
}

// recordData is the grant member as the record writes it.
func (g grantMember) recordData() map[string]any {
	out := map[string]any{"jti": g.jti, "op": g.op, "aud": g.aud, "exp": g.exp.UTC().Format(time.RFC3339)}
	if g.approval != "" {
		out["approval"] = g.approval
	}
	return out
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
// capped as arguments are, with uriTruncated when cut. Revision 44 adds
// object and rolesChecked (objectRecord), and revision 48 the grant member.
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
	if rec.object != nil {
		data["object"] = objectRecord(*rec.object)
	}
	if rec.rolesChecked != nil {
		data["rolesChecked"] = rec.rolesChecked
	}
	if rec.event != "" {
		data["event"] = rec.event
	}
	if rec.grant != nil {
		data["grant"] = rec.grant.recordData()
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
	// The grant route chooses the id, because the grant's dec claim names it.
	id := rec.id
	if id == "" {
		id = uuid.NewString()
	}
	ce, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          id,
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
