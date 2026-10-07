package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

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

	raw := bearerToken(r)
	if raw == "" {
		apiError(w, http.StatusUnauthorized, "missing session token")
		return
	}
	claims, err := a.tokens.Verify(raw)
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
			"Straza: attestation level %q is below the required level %q. Reinstall with `straza install --managed <harness>`",
			claims.Attestation, minAtt))
		return
	}

	sub, ok := a.subjects.get(claims.Session)
	if !ok {
		// No cached subject: this pod restarted, never saw this session's
		// checkin, or a role window of the user opened or closed since that
		// checkin. The token alone cannot prove roles (it carries a roles
		// hash), and evaluating a role-less stand-in silently un-applies every
		// role-matched set: the audit then records a verdict policy never
		// made, and where the profile default is allow the window fails OPEN.
		// Same honest bounce as the gateway lane: 401, the client re-checks
		// in with this token and retries.
		apiError(w, http.StatusUnauthorized, sessionStateExpiredMsg)
		return
	}

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
// with the classifier's reason. lane names the PEP lane, hook or gateway,
// for the fail-closed log record and counter.
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
	"Try again once the database is back. An administrator sees audit queue waiting for the database in the strazad log."

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
	if err := a.audit.submit(ctx, store.OutboxEvent{Subject: "straza.audit.tool", CE: string(ce)}); err != nil {
		a.failClosed(ctx, "hook", d.RuleID, err)
		return false
	}
	return true
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}
