package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// approvalPayload is the admin/API JSON view of one approval record. Times
// are RFC3339; decidedAt is null while pending.
type approvalPayload struct {
	ID            string   `json:"id"`
	State         string   `json:"state"`
	CreatedAt     string   `json:"createdAt"`
	ExpiresAt     string   `json:"expiresAt"`
	DecidedAt     *string  `json:"decidedAt"`
	User          string   `json:"user"`
	Username      string   `json:"username"`
	Session       string   `json:"session"`
	Rule          string   `json:"rule"`
	Set           string   `json:"set"`
	Lane          string   `json:"lane"`
	Summary       string   `json:"summary"`
	Justification string   `json:"justification"`
	ApproverRoles []string `json:"approverRoles"`
	// ApproverUsers: the user-scoped decide pool (policyset revision 13
	// approve.deciders, 0.72.0), usernames resolved at request time (today:
	// the requester's sponsor). Additive + omitempty.
	ApproverUsers []string `json:"approverUsers,omitempty"`
	SelfApproval  bool     `json:"selfApproval"`
	Mode          string   `json:"mode"` // approve|confirm (0.56.0)
	DecidedBy     string   `json:"decidedBy"`
	DecidedByName string   `json:"decidedByName"`
	Channel       string   `json:"channel"` // decider surface: console|slack|phone|browser (legacy rows: api)
	// Decided attribution (0.63.0): the decider's own words and, on the
	// signed lane, the enrolled device that signed.
	DecidedReason   string `json:"decidedReason,omitempty"`
	DecidedDeviceID string `json:"decidedDeviceId,omitempty"`
	// Ticket wire fields (spec/policyset revision 6 `class: ticket`). Omitted for
	// a hold record (Class ""); the console + mobile queue panes render them.
	// class is "ticket"; grantExpiresAt is the post-approval consume deadline;
	// consumedAt/consumedBy record the single grant consumption.
	Class          string  `json:"class,omitempty"`
	GrantExpiresAt *string `json:"grantExpiresAt,omitempty"`
	ConsumedAt     *string `json:"consumedAt,omitempty"`
	ConsumedBy     string  `json:"consumedBy,omitempty"`
	// Args preview fields, camelCase to match this payload's siblings. The
	// five fields travel together (see ApproverRow); argsBytes is the
	// redacted+neutralized length BEFORE truncation.
	ArgsPreview    string `json:"argsPreview,omitempty"`
	ArgsTruncated  bool   `json:"argsTruncated,omitempty"`
	ArgsBytes      int    `json:"argsBytes,omitempty"`
	ArgvHashPrefix string `json:"argvHashPrefix,omitempty"`
	BindingScope   string `json:"bindingScope,omitempty"`
}

// requireIdentified authenticates any active USER identity, a session token
// (`ses` claim, session still active) or a login/ID token, and hands the
// handler the user id. It deliberately accepts more principals than
// requireUser (which is session-token-only, built for the connect flow):
// approvers decide from the console or strazactl, whose credential is a login
// token, not an agent session. `wat_` admin API tokens are rejected: they carry no
// user identity ("no user, no roles"), and an approval decision must bind to
// a person (the service still validates approver roles per record).
func (a *App) requireIdentified(next func(w http.ResponseWriter, r *http.Request, userID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, _, ok := a.identify(w, r); ok {
			next(w, r, u.ID)
		}
	}
}

// requirePersonClient is requireIdentified for the routes that decide a
// request or enroll a device that decides. It refuses a user who is not a
// person, as the admin plane does, before the route reads anything. It
// refuses a session that a coding harness checked in, because the agent in
// that harness runs as the user and holds that session token. A login token
// carries no session and passes. The harness name is the one the client
// declared at check-in.
func (a *App) requirePersonClient(next func(w http.ResponseWriter, r *http.Request, userID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ses, ok := a.identify(w, r)
		if !ok {
			return
		}
		if !personUser(u) {
			a.refuseNonPerson(w, r, u, ses)
			return
		}
		userID := u.ID
		if ses.ID != "" && !adminHarnesses[ses.HarnessName] {
			a.log.Warn("coding-harness session refused on a decide route",
				"user", userID, "session", ses.ID, "harness", ses.HarnessName, "path", r.URL.Path)
			apiError(w, http.StatusForbidden, fmt.Sprintf(
				"this session belongs to the coding harness %q, and a coding harness cannot decide a request or enroll a device that decides. Use %s",
				ses.HarnessName, a.approvalSurfacesHint()))
			return
		}
		// The person is the actor of any straza.audit.admin record the
		// route writes, through the credential lane requireAdmin names.
		act := auditActor{Name: u.Username, ID: u.ID, Via: "login"}
		if ses.ID != "" {
			act.Via = "session"
		}
		next(w, r.WithContext(withActor(r.Context(), act)), userID)
	}
}

// identify authenticates the bearer for requireIdentified and
// requirePersonClient. It returns the user row and, for a session token, the
// active session row, judged by judgeSession as a refresh judges it; a login
// token leaves the row zero. It writes the refusal itself and reports false.
func (a *App) identify(w http.ResponseWriter, r *http.Request) (store.User, store.Session, bool) {
	raw := bearerToken(r)
	if raw == "" {
		apiError(w, http.StatusUnauthorized, "missing bearer token")
		return store.User{}, store.Session{}, false
	}
	if claims, err := a.tokens.Verify(raw); err == nil && claims.Session != "" {
		return a.judgeSession(w, r, claims, "approvals bearer", claims.Harness)
	}
	u, err := a.verifyLogin(r, raw)
	if err == nil {
		return u, store.Session{}, true
	}
	var refused *loginRefusal
	if errors.As(err, &refused) {
		a.refuseLogin(w, r, refused, "")
		return store.User{}, store.Session{}, false
	}
	// A store/issuer outage is not a rejected token.
	if a.answerOutage(w, r, "approvals bearer", err) {
		return store.User{}, store.Session{}, false
	}
	apiError(w, http.StatusUnauthorized, "token rejected: this route needs a person's session or ID token; an admin API token carries no user and cannot decide")
	return store.User{}, store.Session{}, false
}

// approvalModeOut maps a pre-migration empty mode to "approve" so the wire
// never carries an empty enum value.
func approvalModeOut(mode string) string {
	if mode == "" {
		return policy.ModeApprove
	}
	return mode
}

func toApprovalPayload(r approval.Record) approvalPayload {
	var decidedAt *string
	if r.DecidedAt != nil {
		s := r.DecidedAt.UTC().Format(time.RFC3339)
		decidedAt = &s
	}
	roles := r.ApproverRoles
	if roles == nil {
		roles = []string{}
	}
	p := approvalPayload{
		ID: r.ID, State: string(r.State),
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt: r.ExpiresAt.UTC().Format(time.RFC3339),
		DecidedAt: decidedAt,
		User:      r.UserID, Username: r.Username, Session: r.SessionID,
		Rule: r.RuleID, Set: r.SetName, Lane: r.Lane, Summary: r.Summary,
		Justification: r.Justification, ApproverRoles: roles, ApproverUsers: r.ApproverUsers,
		SelfApproval: r.SelfApproval,
		Mode:         approvalModeOut(r.Mode),
		DecidedBy:    r.DecidedBy, DecidedByName: r.DecidedByName, Channel: r.Channel,
		DecidedReason: r.DecidedReason, DecidedDeviceID: r.DecidedDeviceID,
		Class:          r.Class,
		GrantExpiresAt: formatTimePtr(r.GrantExpiresAt),
		ConsumedAt:     formatTimePtr(r.ConsumedAt),
		ConsumedBy:     r.ConsumedBy,
		ArgsPreview:    r.ArgsPreview,
		ArgsTruncated:  r.ArgsTruncated,
		ArgsBytes:      r.ArgsBytes,
	}
	// Field-pairing invariant: the hash prefix + binding scope ride ALONG with a
	// present preview (see ApproverRow), never alone.
	if r.ArgsPreview != "" {
		p.ArgvHashPrefix = approval.HashPrefix(r.ArgvHash)
		p.BindingScope = approval.BindingScopeForRecord(r.ArgvHash, r.Summary)
	}
	return p
}

// formatTimePtr renders a nullable timestamp as an RFC3339 string pointer,
// preserving null (nil ⇒ omitted from the wire).
func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// handleApprovalsList serves GET /v1/admin/approvals?state=... (requireAdmin).
// Absent keeps the documented pending default; concrete states pass through;
// an unknown value is a loud 400, never a silent coercion to pending (a
// filter typo must not render the pending queue as "denied").
func (a *App) handleApprovalsList(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	switch state {
	case "all":
		state = "" // service treats "" as all
	case "":
		state = string(approval.StatePending)
	case string(approval.StatePending), string(approval.StateApproved),
		string(approval.StateDenied), string(approval.StateExpired):
	default:
		apiError(w, http.StatusBadRequest, "state must be pending, approved, denied, expired or all")
		return
	}
	p, ok := parsePageParams(w, r)
	if !ok {
		return
	}
	if p.paged {
		recs, next, err := a.approval.PageByState(r.Context(), state, p.before, p.limit)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "list approvals failed", err)
			return
		}
		out := make([]approvalPayload, len(recs))
		for i, rec := range recs {
			out[i] = toApprovalPayload(rec)
		}
		writePage(w, out, next)
		return
	}
	recs, err := a.approval.List(r.Context(), state)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list approvals failed", err)
		return
	}
	out := make([]approvalPayload, len(recs))
	for i, rec := range recs {
		out[i] = toApprovalPayload(rec)
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": out})
}

// handleApprovalGet serves GET /v1/admin/approvals/{id} (requireAdmin): the
// single-record read for deep links and post-decision refresh without a
// re-list. Same envelope as the decide endpoints.
func (a *App) handleApprovalGet(w http.ResponseWriter, r *http.Request) {
	rec, err := a.approval.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such approval")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "get approval failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": toApprovalPayload(rec)})
}

// handleApprovalApprove/Deny serve POST /v1/admin/approvals/{id}/approve|deny
// (requirePersonClient: a person's session from the console, strazactl or the
// self-service page, OR a login token; a coding-harness session is refused.
// The service validates approver roles, so a non-admin approver can decide).
// Channel = "console".
func (a *App) handleApprovalApprove(w http.ResponseWriter, r *http.Request, userID string) {
	a.decideApproval(w, r, userID, string(approval.StateApproved))
}

func (a *App) handleApprovalDeny(w http.ResponseWriter, r *http.Request, userID string) {
	a.decideApproval(w, r, userID, string(approval.StateDenied))
}

func (a *App) decideApproval(w http.ResponseWriter, r *http.Request, userID, verdict string) {
	// Optional body {"reason": "..."} (0.63.0). The endpoint predates the
	// field, so an absent/empty body stays legal: console builds and strazactl
	// that send nothing keep working. The service validates the words.
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		// A bad body is a client bug worth naming, not something to swallow
		// into a reason-less decide.
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			apiError(w, http.StatusBadRequest, "body must be JSON: {\"reason\": \"...\"}")
			return
		}
	}
	rec, err := a.approval.Decide(r.Context(), r.PathValue("id"), verdict, userID, "console", body.Reason, "")
	if err != nil {
		switch status := approvalErrorStatus(err); {
		case errors.Is(err, approval.ErrStoreUnavailable):
			// The users read failed: no decision was recorded, and the
			// person reads the outage, never a refusal of who they are.
			w.Header().Set("Retry-After", loginOutageRetryAfter)
			a.fail(w, r, http.StatusServiceUnavailable, loginOutageBody, err)
		case status >= http.StatusInternalServerError:
			a.fail(w, r, status, err.Error(), err)
		case errors.Is(err, approval.ErrUnsignedOwnDecision):
			apiError(w, status, a.unsignedOwnDecisionMessage())
		default:
			apiError(w, status, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": toApprovalPayload(rec)})
}

// approvalErrorStatus maps a Decide error to its HTTP status.
func approvalErrorStatus(err error) int {
	switch {
	case errors.Is(err, approval.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, approval.ErrSelfApproval), errors.Is(err, approval.ErrNotApprover),
		errors.Is(err, approval.ErrNotRequester), errors.Is(err, approval.ErrUnsignedOwnDecision):
		return http.StatusForbidden
	case errors.Is(err, approval.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, approval.ErrExpired):
		return http.StatusGone
	case errors.Is(err, approval.ErrBadReason):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// unsignedOwnDecisionMessage is what the console and strazactl show a person
// who tries to decide their own request: what was refused, why, where to go,
// and the config key an operator sets to accept the risk.
func (a *App) unsignedOwnDecisionMessage() string {
	return "this is your own request, and the console and strazactl cannot decide it, because an agent on your machine could do the same. " +
		"Confirm it on your enrolled phone, or on the self-service page under This browser (" + a.selfServicePage() + "). " +
		"An operator who accepts that risk sets approval.unsignedOwnDecisions to true in the server config"
}

// handleSlackCallback serves POST /v1/approval/callbacks/slack (NO auth
// wrapper; the Slack signature is verified inside the channel).
func (a *App) handleSlackCallback(w http.ResponseWriter, r *http.Request) {
	a.approval.HandleSlackCallback(w, r)
}

// --- Channel status + test ---

// channelStatusPayload is the wire shape of one admin channel-status row.
type channelStatusPayload struct {
	Name          string                 `json:"name"`
	Configured    bool                   `json:"configured"`
	Detail        string                 `json:"detail,omitempty"`
	Devices       int                    `json:"devices,omitempty"`
	Registrations int                    `json:"registrations,omitempty"`
	LastDelivery  *deliveryRecordPayload `json:"last_delivery,omitempty"`
}

type deliveryRecordPayload struct {
	At   string `json:"at"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// handleApprovalChannels serves GET /v1/admin/approvals/channels
// (requireAdmin): every notification channel with its configuration state,
// the enrolled-devices vs push-routes gap, and the last delivery attempt.
func (a *App) handleApprovalChannels(w http.ResponseWriter, r *http.Request) {
	statuses, err := a.approval.ChannelStatuses(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "channel status failed", err)
		return
	}
	out := make([]channelStatusPayload, len(statuses))
	for i, s := range statuses {
		p := channelStatusPayload{
			Name: s.Name, Configured: s.Configured, Detail: s.Detail,
			Devices: s.Devices, Registrations: s.Registrations,
		}
		if s.LastDelivery != nil {
			p.LastDelivery = &deliveryRecordPayload{
				At: s.LastDelivery.At.UTC().Format(time.RFC3339), OK: s.LastDelivery.OK, Note: s.LastDelivery.Note,
			}
		}
		out[i] = p
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// handleApprovalChannelTest serves POST /v1/admin/approvals/channels/{name}/test
// (requireAdmin): a synchronous test-send through one channel's REAL delivery
// path, reporting per-target outcomes. 200 even when individual targets fail:
// the report carries the truth; only an unusable request errors.
func (a *App) handleApprovalChannelTest(w http.ResponseWriter, r *http.Request) {
	report, err := a.approval.TestChannel(r.Context(), r.PathValue("name"))
	switch {
	case errors.Is(err, approval.ErrChannelUnknown):
		apiError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, approval.ErrChannelNotTestable), errors.Is(err, approval.ErrChannelOff):
		apiError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, "channel test failed", err)
		return
	}
	targets := make([]map[string]any, len(report.Targets))
	for i, t := range report.Targets {
		m := map[string]any{"target": t.Target, "ok": t.OK}
		if t.Error != "" {
			m["error"] = t.Error
		}
		targets[i] = m
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": report.Channel, "targets": targets})
}
