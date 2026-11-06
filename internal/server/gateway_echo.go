package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/sentinel"
	"github.com/strazahq/straza/internal/store"
)

// credentialEchoMsg is the refusal an agent reads in place of a reply that
// held the credential Straza injects for the server, with the tool and then
// the server. It never quotes the reply.
const credentialEchoMsg = "Straza: the reply of the tool %s on the MCP server %s was withheld, because it held the credential Straza injects for that server. " +
	"The same call will be withheld again. Tell the user."

// credentialEchoVerdict is the reason of the verdict the gateway raises for
// a withheld reply, with the server and then the tool.
const credentialEchoVerdict = "The MCP server %s answered a call of its tool %s with the credential Straza injects for it, so the gateway withheld the reply." // #nosec G101 -- a verdict sentence, not a credential

// withholdEcho answers a call whose reply held the credential Straza
// injected for it, as manager.ErrCredentialEcho says. The call's record rec
// was queued before the call ran and says the policy allowed it, so a
// second record follows under a CloudEvents id of its own: the event
// tool.post, a deny that keeps the allowing rule and set, with the refusal
// as its reason. A critical credentialEcho verdict then names that record
// in its evidence, whether the sentinel runs or not, the counter of
// withheld replies counts one for the server, and the agent reads the
// refusal in place of the reply. None of it reads the database. Under audit
// backpressure block, the two records together wait at most one submit
// bound, and the verdict is not queued after a deny record the full queue
// refused, so the refusal reaches the agent inside its request timeout.
func (a *App) withholdEcho(w http.ResponseWriter, r *http.Request, req rpcRequest, rec mcpAuditRecord, srv *serverScope) {
	until := time.Now().Add(auditSubmitBound)
	msg := fmt.Sprintf(credentialEchoMsg, rec.target.tool, rec.target.app)
	a.metrics.ReplyWithheld(rec.target.app)
	rec.event = policy.EventToolPost
	rec.id = uuid.NewString()
	rec.decision = refusedDecision(rec.decision, msg)
	audited := a.auditMCP(r.Context(), rec)
	a.raiseEchoVerdict(r.Context(), rec, audited, until)
	writeRPCResult(w, req.ID, toolRefusal(msg, srv, deniedSlot(msg, audited, "")))
}

// raiseEchoVerdict queues the critical credentialEcho verdict of the
// withheld reply whose record is rec, on the audit queue every gateway
// record takes, waiting for room no later than until, and logs it at Warn
// as the sentinel logs its own verdicts. When audited is false the queue
// refused rec, so the verdict is not offered to it. A verdict the queue does
// not take logs an Error line, since the reply is withheld either way.
func (a *App) raiseEchoVerdict(ctx context.Context, rec mcpAuditRecord, audited bool, until time.Time) {
	v := sentinel.Verdict{
		Session: rec.claims.Session, User: rec.claims.Subject,
		Detector: sentinel.DetectorCredentialEcho, Severity: sentinel.SeverityCritical,
		Reason:   fmt.Sprintf(credentialEchoVerdict, rec.target.app, rec.target.tool),
		Evidence: []string{rec.id},
	}
	ce, err := sentinel.VerdictCE(v, "strazad")
	switch {
	case !audited:
		err = errors.New("the queue refused the reply's deny record before it")
	case err == nil:
		vctx, cancel := context.WithDeadline(ctx, until)
		err = a.audit.submit(vctx, store.OutboxEvent{Subject: sentinel.SubjectVerdict, CE: string(ce)})
		cancel()
	}
	if err != nil {
		a.log.Error("the credentialEcho verdict of a withheld reply could not be queued, so the strazad log is its only trace. "+
			"Check that the database is reachable and takes audit records",
			"app", rec.target.app, "tool", rec.target.tool, "session", v.Session, "err", err)
		return
	}
	a.log.Warn("sentinel verdict", "detector", v.Detector, "severity", v.Severity,
		"session", v.Session, "user", v.User, "reason", v.Reason)
}
