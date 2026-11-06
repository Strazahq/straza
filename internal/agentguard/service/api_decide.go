package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
)

// Decide budgets. renewBudget bounds the session's renewal, policyBudget
// the snapshot, classifyBudget the classifier and onlineBudget one
// /v1/decide call with its 401 bounce (decide 2 s, the bounce 3 s, the
// retry 2 s). decideBudget caps the whole decision below the thin client's
// timeout, so the hook never denies while the service still decides.
const (
	renewBudget    = 2 * time.Second
	policyBudget   = 2 * time.Second
	classifyBudget = time.Second
	onlineBudget   = 7 * time.Second
	decideBudget   = localapi.DecideTimeout - time.Second
)

// captureMaxBytes caps one captured turn's stored content. The hash covers
// the full original, so the record witnesses what was cut.
const captureMaxBytes = 64 << 10

// toolTextMax caps what a tool record keeps of the command and of the
// paths, as captureMaxBytes caps a turn. A cut value adds <field>Truncated
// and the <field>Hash of the whole value, so the record still witnesses
// what the call named.
const toolTextMax = 64 << 10

// decide answers POST decide for one hook event of the caller's window.
func (a *api) decide(w http.ResponseWriter, r *http.Request) {
	var req localapi.DecideRequest
	c := callerOf(r)
	if !a.read(w, r, textBodyMax, &req) || !a.named(w, r, req.Harness) ||
		!a.fit(w, r, maxEventField, field{"tool", req.Event.Tool}, field{"app", req.Event.App}, field{"tool name", req.Event.ToolName},
			field{"workspace", req.Event.Workspace}, field{"agent type", req.AgentType}, field{"agent id", req.AgentID}) {
		a.noteRefused(c, "decide")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), decideBudget)
	defer cancel()
	sctx, scancel := context.WithTimeout(ctx, renewBudget)
	s, err := a.creds.Session(sctx, c, req.Harness, false)
	scancel()
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, a.decideFor(ctx, c, s, req))
}

// decideFor decides req on s. Tool calls and permission requests go
// through the offline gate (D11), the refused-policy deadline, the gateway
// deferral, the engine and the escalations, and each decision the service
// made itself is recorded once. Every other event allows with the capture
// directive and records nothing. A snapshot that cannot be had denies
// every event.
func (a *api) decideFor(ctx context.Context, c Caller, s Session, req localapi.DecideRequest) localapi.DecideAnswer {
	ans := localapi.DecideAnswer{Journal: localapi.Journal{Snapshot: s.SnapshotID}}
	req.Event.NativeTool = req.NativeTool
	// A shell event that arrives without an interpreter tag, as straza exec
	// sends one, is tagged here the way strazad's tagInterpreter does, so
	// interpreter rules hold on every lane. A client's tag is kept.
	if req.Event.Tool == policy.ToolShellExec && req.Event.Interpreter == "" {
		req.Event.Interpreter = policy.DetectInterpreter(req.Event.Command, req.Event.Argv)
	}
	pctx, cancel := context.WithTimeout(ctx, policyBudget)
	eng, maxAge, snapshot, err := a.creds.Policy(pctx, s)
	cancel()
	if err != nil {
		ans.Decision, ans.Journal.FailClosed = failClosed(policyFailure(s, err)), true
		return ans
	}
	ans.Journal.Snapshot, ans.Capture = snapshot, eng.Capture(s.Subject())
	if k := req.Event.Kind; k != policy.EventToolPre && k != policy.EventPermissionRequest {
		ans.Decision = policy.Decision{Effect: policy.EffectAllow, Default: true}
		return ans
	}
	recorded := false
	grace := time.Duration(maxAge) * time.Second
	end := s.ExpiresAt.Add(grace)
	if s.Renewal.Class == RenewTerminal {
		// A refused renewal is the server's answer, not being offline, so it
		// ends the grace at once. A transport failure, an answer that judged
		// nothing and a 401 whose re-acquire could not complete keep it,
		// because the server's judgment of the session is unknown.
		end = s.ExpiresAt
	}
	switch now := time.Now(); {
	case now.After(end):
		ans.Decision, ans.Journal.FailClosed = failClosed(expiredReason(s, grace)), true
	case !s.PolicyDeadline.IsZero() && now.After(s.PolicyDeadline.Add(grace)):
		// Renewing the token does not move the deadline of a policy the
		// server refused to send, so the same bound holds for it.
		ans.Decision, ans.Journal.FailClosed = failClosed(policyRefusedReason(s.PolicyRefused)), true
	case req.GatewayProxied:
		// A call the harness routes through the Straza gateway is decided by
		// the gateway on current policy with the true app and tool names. The
		// hook's copy of those names is the model-facing sanitized one on
		// codex, so deciding here would be a second, worse gate. The offline
		// gate above stays in front of the deferral.
		ans.Journal.Escalation = "gateway"
		ans.Decision = policy.Decision{Effect: policy.EffectAllow,
			Reason: "Straza: decision deferred to the gateway PEP, which evaluates current policy server-side on this call (single gate)"}
	default:
		ans.Decision, recorded = a.escalate(ctx, s, req, eng.Evaluate(req.Event, s.Subject()), &ans.Journal)
	}
	if !recorded {
		ans.Journal.Spooled = true
		ans.Journal.SpoolError = spoolClass(a.creds.Record(c, "straza.audit.tool", toolRecord(s, snapshot, req, ans.Decision)))
	}
	return ans
}

// spoolClass names a failed record's class for the content-free decision
// journal, never its text, which can name a path: "timeout" for a blown
// deadline, the syscall op of a path error ("open", "write" and so on),
// "http" for a server refusal and "other" for the rest. It is "" when
// nothing failed.
func spoolClass(err error) string {
	var pe *fs.PathError
	var se *agentguard.StatusError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &pe):
		return pe.Op
	case errors.As(err, &se):
		return "http"
	}
	return "other"
}

// escalate applies the escalation lanes a rule opted into: classify first
// (local), then approve, then serverCheck (both online). A classify deny is
// final, and an allow continues, so a decision with both flags still gets
// the server's verdict. approve returns before serverCheck. It reports
// whether strazad recorded the verdict, which it does for every verdict it
// gives, so the service records none for it.
func (a *api) escalate(ctx context.Context, s Session, req localapi.DecideRequest, d policy.Decision, j *localapi.Journal) (policy.Decision, bool) {
	if d.Effect == policy.EffectAllow && d.Classify {
		j.Escalation = "classify"
		d = a.classifyCheck(ctx, req.Event, d, j)
	}
	if d.Effect == policy.EffectAllow && d.Approve != nil {
		j.Escalation = "approve"
		return a.online(ctx, s, req, d, j, "an approval-gated action", "Denied (fail-closed)")
	}
	if d.Effect == policy.EffectAllow && d.ServerCheck {
		j.Escalation = "server"
		return a.online(ctx, s, req, d, j, "a server-checked action", "Denied (run `straza doctor`)")
	}
	return d, false
}

// online asks strazad's /v1/decide for an approve or serverCheck allow and
// passes its verdict through. Any failure is a deny on the local rule,
// worded by onlineDenyReason.
func (a *api) online(ctx context.Context, s Session, req localapi.DecideRequest, local policy.Decision, j *localapi.Journal, action, tail string) (policy.Decision, bool) {
	octx, cancel := context.WithTimeout(ctx, onlineBudget)
	defer cancel()
	out, err := a.creds.Decide(octx, s, req.Event, req.AgentType, req.AgentID)
	j.Status, j.Correlation = out.Status, out.Correlation
	if err != nil {
		j.FailClosed = true
		return policy.Decision{Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
			Reason: onlineDenyReason(err, action, tail)}, false
	}
	return policy.Decision{Effect: out.Answer.Effect, RuleID: out.Answer.RuleID, Reason: out.Answer.Reason,
		Obligations: out.Answer.Obligations}, true
}

// classifyCheck resolves a classify allow through the classifier under its
// one second bound. A missing classifier, an error or a blown bound denies
// with the firing rule kept, and a verdict that does not allow denies with
// the classifier's reason.
func (a *api) classifyCheck(ctx context.Context, ev policy.Event, local policy.Decision, j *localapi.Journal) policy.Decision {
	unavailable := policy.Decision{Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
		Reason: "Straza: classifier unavailable for a classify-gated action. Denied (run `straza doctor`)"}
	if a.classifier == nil {
		j.FailClosed = true
		return unavailable
	}
	cctx, cancel := context.WithTimeout(ctx, classifyBudget)
	defer cancel()
	v, err := a.classifier.Classify(cctx, ev)
	if err != nil {
		j.FailClosed = true
		return unavailable
	}
	if !v.Allowed {
		return policy.Decision{Effect: policy.EffectDeny, RuleID: local.RuleID, SetName: local.SetName,
			Reason: "Straza: classifier: " + v.Reason}
	}
	// The classifier resolved the escalation, so its flag goes: the hook
	// denies an allow that still carries one (agentguard.Unresolved).
	local.Classify = false
	return local
}

// failClosed is a deny the service gives when it cannot decide by policy.
func failClosed(reason string) policy.Decision {
	return policy.Decision{Effect: policy.EffectDeny, Reason: "Straza: " + reason}
}

// policyFailure words the deny when s's snapshot cannot be had. A session
// whose time has run out and whose renewal failed denies for the renewal,
// because no snapshot is fetched with a token past its time. Otherwise the
// credential half's sentence stands.
func policyFailure(s Session, err error) string {
	if time.Now().Before(s.ExpiresAt) {
		return err.Error()
	}
	age := time.Since(s.ExpiresAt).Round(time.Second)
	switch s.Renewal.Class {
	case RenewTerminal:
		return renewRefusedReason(s.Renewal.Detail)
	case RenewUnjudged:
		return renewUnjudgedReason(age, s.Renewal.Detail)
	case RenewTransport, RenewRejected:
		return renewFailedReason(age, s.Renewal.Detail)
	}
	return err.Error()
}

// expiredReason words the offline gate's deny by what the renewal saw.
// Only a renewal that was not tried or that failed in transport may claim
// the server is unreachable.
func expiredReason(s Session, grace time.Duration) string {
	age := time.Since(s.ExpiresAt).Round(time.Second)
	switch s.Renewal.Class {
	case RenewRejected:
		return renewFailedReason(age, s.Renewal.Detail)
	case RenewTerminal:
		return renewRefusedReason(s.Renewal.Detail)
	case RenewUnjudged:
		return renewUnjudgedReason(age, s.Renewal.Detail)
	}
	return fmt.Sprintf("session token expired %s ago and the offline grace period (%s) is exhausted. "+
		"Straza cannot verify current policy while the platform is unreachable. "+
		"This is a connection problem, not a policy decision. Try the call once more in a minute. "+
		"If it is denied again, tell the user to check the network and run `straza doctor`.", age, grace)
}

// renewRefusedReason words the deny of a session whose renewal the server
// refused. detail is the server's own sentence, which names the remedy.
func renewRefusedReason(detail string) string {
	return "session renewal was refused: " + detail
}

// renewFailedReason words the deny of a session whose token expired age ago
// and whose renewal could not complete. detail says what failed.
func renewFailedReason(age time.Duration, detail string) string {
	return fmt.Sprintf("session token expired %s ago and automatic renewal failed (%s). "+
		"Try the call once more in a minute. If it is denied again, tell the user to start a new session "+
		"or run `straza doctor`.", age, detail)
}

// renewUnjudgedReason words the deny of a session whose token expired age
// ago and whose renewal got an answer that judged nothing about it.
func renewUnjudgedReason(age time.Duration, detail string) string {
	return fmt.Sprintf("session token expired %s ago, and the renewal got an answer that does not say whether this session is still accepted: %s "+
		"Try the call once more in a minute, which tries the renewal again. If it is denied again, tell the user to check "+
		"that the configured server URL reaches strazad with nothing in front of it that limits or blocks straza, "+
		"and to run `straza doctor`.", age, detail)
}

// policyRefusedReason words the deny of a session whose newer policy could
// not be fetched, once the session time held at the refusal has run out.
// why is the refusal's own reason, which names the remedy.
func policyRefusedReason(why string) string {
	return "this session's policy is out of date: the server holds a newer policy that straza could not fetch. " +
		why + " Tool calls stay denied until straza can fetch the current policy, and `straza doctor` shows this reason."
}

// onlineDenyReason words a dead online gate: unreachable only when no
// answer came, and the server's refusal when it answered and refused.
func onlineDenyReason(err error, action, tail string) string {
	var refuse *agentguard.StatusError
	if errors.As(err, &refuse) {
		return fmt.Sprintf(
			"Straza: the server refused this session for %s (%s) and re-checkin could not restore it. Denied (fail-closed); restart the session or run `straza doctor`",
			action, strings.TrimPrefix(refuse.Msg, "Straza: "))
	}
	return fmt.Sprintf("Straza: security layer unreachable for %s. %s. "+
		"This is a connection problem, not a policy decision. Try the call once more in a minute, "+
		"and if it is denied again, tell the user.", action, tail)
}

// toolRecord is the straza.audit.tool record of one decision on s by the
// snapshot the engine was built from. It carries the event and that
// snapshot, because an audit line that cannot say what was attempted is not
// evidence (spec/events section 2). It keeps at most toolTextMax of the
// command and of the paths, with the hash of what it cut.
func toolRecord(s Session, snapshot string, req localapi.DecideRequest, d policy.Decision) map[string]any {
	ev := req.Event
	command, cut, hash := capText(ev.Command)
	data := map[string]any{
		"session":  s.ID,
		"harness":  harnessLabel(req.Harness),
		"event":    ev.Kind,
		"tool":     ev.Tool,
		"app":      ev.App,
		"command":  command,
		"effect":   d.Effect,
		"ruleId":   d.RuleID,
		"setName":  d.SetName,
		"reason":   d.Reason,
		"snapshot": snapshot,
	}
	if cut {
		data["commandTruncated"], data["commandHash"] = true, hash
	}
	if ev.ToolName != "" {
		data["toolName"] = ev.ToolName
	}
	if len(ev.Paths) > 0 {
		paths, cut, hash := capPaths(ev.Paths)
		data["paths"] = paths
		if cut {
			data["pathsTruncated"], data["pathsHash"] = true, hash
		}
	}
	if ev.Workspace != "" {
		data["workspace"] = ev.Workspace
	}
	addAgentAttribution(data, req.AgentType, req.AgentID)
	return data
}

// capText keeps at most toolTextMax bytes of s, cut on a character
// boundary, and reports whether it cut and the hash of the whole of s.
func capText(s string) (kept string, cut bool, hash string) {
	if len(s) <= toolTextMax {
		return s, false, ""
	}
	h := sha256.New()
	writeString(h, s)
	return cutUTF8(s, toolTextMax), true, "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// capPaths keeps paths in order while they fit in toolTextMax bytes,
// counting one byte past each, cuts the path that crosses the bound on a
// character boundary and drops the rest. When it cuts, hash covers the
// whole list, each path after its length in bytes as an unsigned 64-bit
// big-endian number, so two different lists never hash alike.
func capPaths(paths []string) (kept []string, cut bool, hash string) {
	budget := toolTextMax
	for i, p := range paths {
		if len(p)+1 <= budget {
			budget -= len(p) + 1
			continue
		}
		kept = paths[:i:i]
		if budget > 1 {
			if part := cutUTF8(p, budget-1); part != "" {
				kept = append(kept, part)
			}
		}
		h := sha256.New()
		for _, q := range paths {
			h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(q))))
			writeString(h, q)
		}
		return kept, true, "sha256:" + hex.EncodeToString(h.Sum(nil))
	}
	return paths, false, ""
}

// writeString writes s to h in pieces, so hashing a long string never
// copies it whole.
func writeString(h hash.Hash, s string) {
	for len(s) > 0 {
		n := min(len(s), 64<<10)
		h.Write([]byte(s[:n]))
		s = s[n:]
	}
}

// cutUTF8 is the longest prefix of s of at most n bytes that ends on a
// character boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// recordText records one captured prompt or reply of s in mode.
func (a *api) recordText(c Caller, s Session, req localapi.CaptureRequest, mode string) error {
	content, truncated, hash := captureContent(req.Text, mode)
	data := map[string]any{
		"session":     s.ID,
		"harness":     harnessLabel(req.Harness),
		"content":     content,
		"mode":        mode,
		"truncated":   truncated,
		"contentHash": hash,
	}
	addAgentAttribution(data, req.AgentType, req.AgentID)
	return a.creds.Record(c, "straza.audit."+req.Kind, data)
}

// addAgentAttribution tags a record with the delegate that produced it
// (spec/events revision 13). A root-lane record carries neither key.
func addAgentAttribution(data map[string]any, agentType, agentID string) {
	if agentType != "" {
		data["agentType"] = agentType
	}
	if agentID != "" {
		data["agentId"] = agentID
	}
}

// captureContent prepares one turn for the record: the hash of the full
// original, then the mode, then the size cap.
func captureContent(content, mode string) (stored string, truncated bool, hash string) {
	sum := sha256.Sum256([]byte(content))
	hash = "sha256:" + hex.EncodeToString(sum[:])
	if mode == policy.CaptureModeRedact {
		content = redact.Redact(content)
	}
	if len(content) > captureMaxBytes {
		content = content[:captureMaxBytes]
		truncated = true
	}
	return content, truncated, hash
}

// capture answers POST capture: it records the prompt or reply text in the
// session's capture mode when the session's policy captures conversations,
// and records nothing otherwise, a snapshot that cannot be had included.
func (a *api) capture(w http.ResponseWriter, r *http.Request) {
	var req localapi.CaptureRequest
	c := callerOf(r)
	if !a.read(w, r, textBodyMax, &req) || !a.named(w, r, req.Harness) ||
		!a.fit(w, r, maxEventField, field{"agent type", req.AgentType}, field{"agent id", req.AgentID}) {
		a.noteRefused(c, "capture")
		return
	}
	if req.Kind != localapi.CapturePrompt && req.Kind != localapi.CaptureReply {
		a.noteRefused(c, "capture")
		a.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("The capture request names the kind %q, and the Straza "+
			"background service records only prompt and reply. Update Straza so the client and the service are "+
			"the same version.", req.Kind))
		return
	}
	sctx, cancel := context.WithTimeout(r.Context(), renewBudget)
	s, err := a.creds.Session(sctx, c, req.Harness, false)
	cancel()
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	pctx, cancel := context.WithTimeout(r.Context(), policyBudget)
	eng, _, _, err := a.creds.Policy(pctx, s)
	cancel()
	if err == nil && req.Text != "" {
		if dir := eng.Capture(s.Subject()); dir.Conversations {
			if err := a.recordText(c, s, req, dir.Mode); err != nil {
				a.refuse(w, r, http.StatusInternalServerError, "The Straza background service could not write the captured "+
					req.Kind+" to your audit spool ("+err.Error()+"). Run `straza doctor` to see why.")
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
