package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// DecideRequest is the POST /v1/decide body: a canonical event to evaluate
// against the caller's session (serverCheck path + Tier-2 helper).
type DecideRequest struct {
	Event policy.Event `json:"event"`
	// AgentType and AgentID are the harness's delegate attribution of the
	// call, carried into the audit record (spec/events rev 13); both are
	// optional and absent for the main agent.
	AgentType string `json:"agentType,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	// NativeTool is the harness's own name of a classified local tool, which
	// a rule that names harness tools reads (spec/policyset revision 26).
	NativeTool string `json:"nativeTool,omitempty"`
	// ToolInput is the input of a classified local tool as the client cut
	// it for the audit record (spec/events revision 65). No rule reads it
	// and no approval key binds it.
	ToolInput *ToolInput `json:"toolInput,omitempty"`
}

// ToolInput is what a client sends of a classified local tool's input for
// the audit record: Text, the tool input as JSON, shortened to the record's
// bound, and Hash, "sha256:" and the hex SHA-256 of the whole text, only
// when Text was shortened.
type ToolInput struct {
	Text string `json:"text"`
	Hash string `json:"hash,omitempty"`
}

// DecideResponse is the decision returned to the caller.
type DecideResponse struct {
	Effect      string   `json:"effect"`
	RuleID      string   `json:"ruleId,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Obligations []string `json:"obligations,omitempty"`
	SnapshotID  string   `json:"snapshotId"`
	// ApprovalID is the id of the approval record a `mode: approve` escalation
	// created or consumed (empty for every other decision); the PEP echoes it
	// so a client can correlate the deny-with-reference to its resolution.
	ApprovalID string `json:"approvalId,omitempty"`
}

// sessionStateExpiredMsg is the any-401 bounce message shared by every PEP
// lane that depends on checkin-built in-memory session state (the subject
// cache): the client re-checks in with its still-valid session token and
// retries. One string for the gateway and /v1/decide so the two lanes never
// drift (cross-pod pin: TestMultiPodHA; restart pin:
// TestDecideSubjectCacheMissBounces).
const sessionStateExpiredMsg = "Straza: session state expired. Check in again"

// revokedSessionMsg refuses a request whose session or token is revoked.
// The device credential is untouched, so the next check-in opens a new
// session without a new enrollment.
const revokedSessionMsg = "Straza: this session has been revoked. Start a new session: its check-in uses this device's existing enrollment, so re-enrolling is not needed"

// revokedIdentityMsg refuses a request or a check-in whose user or device
// is revoked. Only an administrator can lift that.
const revokedIdentityMsg = "Straza: this device or user has been revoked. Contact your administrator"

// handleDecide is the server PDP endpoint. Auth is a session token
// verified locally (JWKS) + denylist; the subject comes from the in-memory
// cache; evaluation uses the live snapshot Engine. Zero DB reads on the
// request path (invariant).
func (a *App) handleDecide(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	raw, scheme := sessionCredential(r)
	if raw == "" {
		apiError(w, http.StatusUnauthorized, "missing session token")
		return
	}
	claims, err := a.verifySessionToken(w, r, raw, scheme)
	if errors.Is(err, errAnswered) {
		return
	}
	if err != nil || claims.Session == "" {
		apiError(w, http.StatusUnauthorized, "session token rejected")
		return
	}
	var req DecideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Event.Kind == "" {
		apiError(w, http.StatusBadRequest, "event.kind is required")
		return
	}
	// The granted fact belongs to the gateway's binding lookup alone
	// (spec/policyset revision 17): a client cannot grant itself access,
	// so whatever the body carried is dropped before evaluation.
	req.Event.Granted = false
	req.Event.NativeTool = req.NativeTool
	tagInterpreter(&req.Event)

	cur := a.snapshots.Current()
	if a.denylist.blocked(claims) {
		// A revoked principal is denied, not errored; the caller enforces
		// it. The denial is still audited (security-relevant).
		a.denyBeforeEvaluation(r.Context(), w, start, claims, req, cur.ID, "revoked",
			a.denylist.revokedMsg(claims, revokedSessionMsg, revokedIdentityMsg))
		return
	}
	// The gateway enforces the attestation minimum from the token claims
	// with no exemption (gatewayAuth); the same check here keeps the
	// check-in exemption of the human clients from becoming a decide bypass.
	if minAtt := a.cfg.Governance.MinAttestation; minAtt != "" &&
		config.AttestationRank(claims.Attestation) < config.AttestationRank(minAtt) {
		a.denyBeforeEvaluation(r.Context(), w, start, claims, req, cur.ID, "attestation", fmt.Sprintf(
			"Straza: attestation level %q is below the required level %q. "+managedInstallStep,
			claims.Attestation, minAtt))
		return
	}

	e, ok := a.subjects.forToken(claims)
	if !ok {
		// No cached subject for the token's roles: this pod restarted, never
		// saw this session's checkin, a role window of the user opened or
		// closed since that checkin, or another pod minted the token after
		// a role changed. The token alone cannot prove roles (it carries
		// only their hash), and evaluating a role-less stand-in silently
		// un-applies every role-matched set: the audit then records a verdict
		// policy never made, and where the profile default is allow the
		// window fails OPEN. Same honest bounce as the gateway lane: 401, the
		// client re-checks in with this token and retries.
		apiError(w, http.StatusUnauthorized, sessionStateExpiredMsg)
		return
	}
	sub := e.sub

	decision := cur.Engine.Evaluate(req.Event, sub)
	if decision.Effect == policy.EffectAllow && decision.Classify {
		// The /v1/decide answer is final (a serverCheck client acts on it
		// verbatim), so a classify-flagged allow is resolved HERE, never
		// returned unresolved for the caller to maybe skip.
		decision = a.resolveClassify(r.Context(), "hook", req.Event, decision)
	}
	var approvalID string
	if decision.Effect == policy.EffectAllow && decision.Approve != nil {
		// A `mode: approve` allow needs a human. The hook lane is
		// request-then-retry: this NEVER blocks; it returns an allow when a
		// fresh exemption covers the retry, otherwise a deny-with-reference
		// while a pending record is created (fail-closed on any service error).
		decision, approvalID = a.resolveApproveHook(r.Context(), claims, sub, req.Event, decision)
	}

	if !a.auditDecision(r.Context(), claims, req, decision, cur.ID) && decision.Effect != policy.EffectDeny {
		// Under block the record could not be queued, so the decision
		// must not run: the client gets a deny that says why.
		decision, approvalID = refusedDecision(decision, auditQueueFullMsg), ""
	}
	a.metrics.Observe(decision.Effect, time.Since(start))

	writeJSON(w, http.StatusOK, DecideResponse{
		Effect:      decision.Effect,
		RuleID:      decision.RuleID,
		Reason:      decision.Reason,
		Obligations: decision.Obligations,
		SnapshotID:  cur.ID,
		ApprovalID:  approvalID,
	})
}

// denyBeforeEvaluation answers a /v1/decide call that policy never sees, a
// revoked session or a token below the attestation minimum: counted and
// audited like every other decision, so the chain shows the refusal.
func (a *App) denyBeforeEvaluation(ctx context.Context, w http.ResponseWriter, start time.Time, claims authn.Claims, req DecideRequest, snapshotID, ruleID, reason string) {
	d := policy.Decision{Effect: policy.EffectDeny, RuleID: ruleID, Reason: reason}
	a.metrics.Observe(policy.EffectDeny, time.Since(start))
	a.auditDecision(ctx, claims, req, d, snapshotID)
	writeJSON(w, http.StatusOK, DecideResponse{
		Effect: d.Effect, RuleID: d.RuleID, Reason: d.Reason, SnapshotID: snapshotID,
	})
}

// tagInterpreter recomputes the interpreter tag server-side
// when the client sent none, so an old or lying client cannot dodge
// interpreter rules. Same DetectInterpreter straza normalize uses, so the
// two PEPs cannot drift. A client-provided tag is kept as-is.
func tagInterpreter(ev *policy.Event) {
	if ev.Tool == policy.ToolShellExec && ev.Interpreter == "" {
		ev.Interpreter = policy.DetectInterpreter(ev.Command, ev.Argv)
	}
}

// resolveClassify applies the server's classifier to a classify-flagged
// allow (mode: classify). Fail closed, same posture as the
// straza classify lane: a missing backend, an error, or a blown deadline
// is a deny with the firing rule preserved; a not-allowed verdict denies
// with the classifier's reason. lane names the caller, hook, gateway or
// simulate, for the fail-closed log record and counter.
func (a *App) resolveClassify(ctx context.Context, lane string, ev policy.Event, local policy.Decision) policy.Decision {
	unavailable := policy.Decision{
		Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
		Reason: "Straza: classifier unavailable for a classify-gated action. Denied",
	}
	if a.classifier == nil {
		return unavailable
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	v, err := a.classifier.Classify(cctx, ev)
	if err != nil {
		a.failClosed(ctx, lane, local.RuleID, fmt.Errorf("classifier: %w", err))
		return unavailable
	}
	if !v.Allowed {
		return policy.Decision{
			Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
			Reason: "Straza: classifier: " + v.Reason,
		}
	}
	return local
}

// auditQueueFullMsg is the answer to a server decision whose audit record
// could not enter the queue under block, on the hook and the gateway lane.
const auditQueueFullMsg = "Straza: this action did not run, because its audit record could not be written while Straza cannot reach its database. " +
	"The audit setting is block, so nothing runs without its record. " +
	"Try the call once more in a minute. If it is denied again, tell the user. " +
	"An administrator sees audit queue waiting for the database in the strazad log."

// auditDecision spools a straza.audit.tool CloudEvent and reports whether
// it entered the queue. Under block a full queue holds it for at most the
// spool's submit bound or until ctx ends, and a record that stays out logs
// the one fail-closed Error line of its decision; the caller must not let
// the decision run. A client adopts this verdict without spooling its own
// record, so the event's targets and the delegate attribution the request
// carried ride on this record when present.
func (a *App) auditDecision(ctx context.Context, claims authn.Claims, req DecideRequest, d policy.Decision, snapshotID string) bool {
	ev := req.Event
	data := map[string]any{
		"session":  claims.Session,
		"user":     claims.Subject,
		"harness":  claims.Harness,
		"event":    ev.Kind,
		"tool":     ev.Tool,
		"app":      ev.App,
		"toolName": ev.ToolName,
		"command":  ev.Command,
		"effect":   d.Effect,
		"ruleId":   d.RuleID,
		"setName":  d.SetName,
		"reason":   d.Reason,
		"snapshot": snapshotID,
	}
	if len(ev.Paths) > 0 {
		data["paths"] = ev.Paths
	}
	if ev.NativeTool != "" {
		data["nativeTool"] = ev.NativeTool
	}
	if args, hash, ok := decideInput(req); ok {
		data["arguments"] = args
		if hash != "" {
			data["argumentsTruncated"], data["argumentsHash"] = true, hash
		}
	}
	if ev.Workspace != "" {
		data["workspace"] = ev.Workspace
	}
	if req.AgentType != "" {
		data["agentType"] = req.AgentType
	}
	if req.AgentID != "" {
		data["agentId"] = req.AgentID
	}
	ce, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          uuid.NewString(),
		"type":        "straza.audit.tool",
		"source":      "strazad",
		"time":        time.Now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	})
	if err != nil {
		return true
	}
	// The record is bounded here, in the request that made it, so a long
	// one never costs the audit writer that every decision shares.
	if err := a.audit.submit(ctx, store.OutboxEvent{Subject: "straza.audit.tool", CE: store.BoundCE(string(ce))}); err != nil {
		a.failClosed(ctx, "hook", d.RuleID, err)
		return false
	}
	return true
}

// decideInput answers what the record of req keeps of the call's input as
// agentguard.CutArguments shortens it, the hash of the whole text when it
// was shortened, and whether req carries an input the record keeps. The
// event's args of an mcp.call or an unclassified tool come first, then the
// input a client sent beside a classified tool's event, whose hash counts
// only when it is a SHA-256. A text over the bound is shortened here under
// the hash of what arrived. An mcp.call that names the built-in straza
// server keeps none, and neither does a call of the gateway's drafting
// tools under any server name: only the gateway serves them, and its own
// record masks a draft.
func decideInput(req DecideRequest) (args, hash string, ok bool) {
	ev := req.Event
	switch {
	case ev.Tool == policy.ToolMCPCall && ev.App == nativeAppName, agentguard.HoldsDraft(ev.ToolName):
		return "", "", false
	case ev.Args != nil:
		args = string(ev.Args)
	case req.ToolInput != nil:
		args = req.ToolInput.Text
		if digest, tagged := strings.CutPrefix(req.ToolInput.Hash, "sha256:"); tagged && len(digest) == 2*sha256.Size {
			if _, err := hex.DecodeString(digest); err == nil {
				hash = req.ToolInput.Hash
			}
		}
	default:
		return "", "", false
	}
	if kept, whole := agentguard.CutArguments([]byte(args)); whole != "" {
		args, hash = kept, whole
	}
	return args, hash, true
}

// bearerToken returns the token of r's Authorization header under the
// Bearer scheme, or "" for any other header.
func bearerToken(r *http.Request) string {
	if raw, scheme := sessionCredential(r); scheme == schemeBearer {
		return raw
	}
	return ""
}
