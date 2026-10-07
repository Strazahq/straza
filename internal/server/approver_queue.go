package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/policy"
)

// --- pending / history ---

// approverRequester is the SUBJECT of the request: the identity whose
// authority is on the line (0.63.0). It is deliberately NOT "who
// typed it": today every record is raised by an agent session (hook or gateway
// lane), and that actor is named by approverOrigin. Kind human|nhi.
type approverRequester struct {
	Username string `json:"username"`
	Kind     string `json:"kind"`
}

// approverOrigin names who actually raised the request. actor is
// "agent_session" for every record today; the field exists so a future
// human-raised lane is a value change, not a wire change.
type approverOrigin struct {
	Actor string `json:"actor"`
}

// approverDecidedVia is the decider half of the attribution: the surface
// the decision came from (phone|browser|console|slack; legacy rows carry the
// retired transport constant "api") and, on the signed lane, the enrolled
// device whose key signed it.
type approverDecidedVia struct {
	Surface  string `json:"surface"`
	DeviceID string `json:"device_id,omitempty"`
}

// approverRow is the JSON view of one pending/history record. Challenge appears
// on decidable rows only; state/decided_by/decided_at describe resolution on
// mine/history rows. Summary is the redacted tool identity (never raw args).
type approverRow struct {
	ID            string               `json:"id"`
	CreatedAt     string               `json:"created_at"`
	ExpiresAt     string               `json:"expires_at"`
	Requester     approverRequester    `json:"requester"`
	Origin        approverOrigin       `json:"origin"`
	RuleID        string               `json:"rule_id"`
	SetName       string               `json:"set_name"`
	Summary       approval.SummaryView `json:"summary"`
	Justification string               `json:"justification"`
	Challenge     string               `json:"challenge,omitempty"`
	State         string               `json:"state,omitempty"`
	DecidedBy     string               `json:"decided_by,omitempty"`
	DecidedAt     *string              `json:"decided_at,omitempty"`
	// DecidedVia + DecidedReason (0.63.0): how the decision was made
	// and the decider's own words. Settled rows only; reason omits when the
	// decider gave none.
	DecidedVia    *approverDecidedVia `json:"decided_via,omitempty"`
	DecidedReason string              `json:"decided_reason,omitempty"`
	// Mode is "confirm" for requester-decides records (0.56.0); omitted for
	// pool-decided ("approve") records so pre-0.56.0 phone builds see no change.
	Mode string `json:"mode,omitempty"`
	// SessionID + Harness: the execution context an approver decides
	// against. SessionID is the record's own field; Harness is resolved from
	// the session store at list time (approver-authed read path, not a
	// request path). Additive +
	// omitempty: absent when the record carries no session / the session row
	// is gone, so nothing is ever claimed that was not resolved.
	SessionID string `json:"session_id,omitempty"`
	Harness   string `json:"harness,omitempty"`
	// ApproverRoles names who may decide this record (the winning rule's
	// approve.roles, persisted at request time). Added 0.60.0 so a requester's
	// own surface can say "waiting on straza-admin" instead of a bare row with
	// no verb and no explanation. Additive + omitempty.
	ApproverRoles []string `json:"approver_roles,omitempty"`
	// ApproverUsers: the user-scoped decide pool (policyset revision 13
	// approve.deciders, 0.72.0), usernames resolved at request time (today:
	// the requester's sponsor). Additive + omitempty.
	ApproverUsers []string `json:"approver_users,omitempty"`
	// Class and use wire fields (spec/policyset revisions 6 and 23). class is
	// "hold" or "ticket"; grant_expires_at is the post-approval use deadline;
	// consumed_at/consumed_by record the single use. A hold carries them once
	// approved (revision 23); the phone's standing ticket queue renders them.
	Class          string  `json:"class,omitempty"`
	GrantExpiresAt *string `json:"grant_expires_at,omitempty"`
	ConsumedAt     *string `json:"consumed_at,omitempty"`
	ConsumedBy     string  `json:"consumed_by,omitempty"`
	// Args preview. The five fields travel together: args_preview is
	// the redacted, display-safe, bounded call arguments; args_bytes is
	// the redacted+neutralized length BEFORE truncation;
	// argv_hash_prefix (first 12 hex of the fingerprint, no "sha256:")
	// and binding_scope drive the server-composed honesty line. All omit-empty, so
	// a knob-off / no-args row carries none of them.
	ArgsPreview    string `json:"args_preview,omitempty"`
	ArgsTruncated  bool   `json:"args_truncated,omitempty"`
	ArgsBytes      int    `json:"args_bytes,omitempty"`
	ArgvHashPrefix string `json:"argv_hash_prefix,omitempty"`
	BindingScope   string `json:"binding_scope,omitempty"`
}

// applyApproverTicketFields copies the class and use wire fields from a full
// record onto an approver row (null timestamps omitted; a pending record
// leaves the use fields zero). The passthrough lives here rather than in
// approval.PendingRow so the mobile surface gains the fields without an
// internal/approval change; the record is fetched via the exported
// approval.Get.
func applyApproverTicketFields(row *approverRow, rec approval.Record) {
	row.Class = rec.Class
	row.GrantExpiresAt = formatTimePtr(rec.GrantExpiresAt)
	row.ConsumedAt = formatTimePtr(rec.ConsumedAt)
	row.ConsumedBy = rec.ConsumedBy
	row.ApproverRoles = rec.ApproverRoles
	row.ApproverUsers = rec.ApproverUsers
}

// enrichApproverRows fills each row's ticket wire fields and its session
// identity from the full record. Non-hot-path (the mobile poll
// surface); a failed record lookup leaves the hold-shaped row unchanged, and
// a failed session lookup leaves harness absent (session_id still names the
// session, because the record itself is the source of that fact). The
// per-call cache keeps one store read per distinct session, not per row.
func (a *App) enrichApproverRows(ctx context.Context, rows []approverRow) {
	harness := map[string]string{}
	for i := range rows {
		rec, err := a.approval.Get(ctx, rows[i].ID)
		if err != nil {
			continue
		}
		applyApproverTicketFields(&rows[i], rec)
		if rec.SessionID == "" {
			continue
		}
		rows[i].SessionID = rec.SessionID
		h, seen := harness[rec.SessionID]
		if !seen {
			if ses, err := a.store.Sessions().GetByID(ctx, rec.SessionID); err == nil {
				h = ses.HarnessName
			}
			harness[rec.SessionID] = h
		}
		rows[i].Harness = h
	}
}

func toApproverRow(pr approval.PendingRow, decidable bool) approverRow {
	row := approverRow{
		ID:        pr.ID,
		CreatedAt: pr.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt: pr.ExpiresAt.UTC().Format(time.RFC3339),
		Requester: approverRequester{Username: pr.RequesterName, Kind: pr.RequesterKind},
		// Every record today is raised by an agent session; the subject
		// (requester) lends the authority, the actor does the asking.
		Origin: approverOrigin{Actor: "agent_session"},
		RuleID: pr.RuleID, SetName: pr.SetName, Summary: pr.Summary,
		Justification:  pr.Justification,
		ArgsPreview:    pr.ArgsPreview,
		ArgsTruncated:  pr.ArgsTruncated,
		ArgsBytes:      pr.ArgsBytes,
		ArgvHashPrefix: pr.ArgvHashPrefix,
		BindingScope:   pr.BindingScope,
	}
	if pr.Mode == policy.ModeConfirm {
		row.Mode = pr.Mode
	}
	if decidable {
		row.Challenge = pr.Challenge
		return row
	}
	row.State = pr.State
	row.DecidedBy = pr.DecidedBy
	if pr.DecidedAt != nil {
		s := pr.DecidedAt.UTC().Format(time.RFC3339)
		row.DecidedAt = &s
	}
	if pr.State != "" && pr.State != "pending" && pr.Channel != "" {
		row.DecidedVia = &approverDecidedVia{Surface: pr.Channel, DeviceID: pr.DecidedDeviceID}
		row.DecidedReason = pr.DecidedReason
	}
	return row
}

// handleApproverPending serves GET /v1/approver/pending?scope=decidable|mine.
func (a *App) handleApproverPending(w http.ResponseWriter, r *http.Request, deviceID, userID string) {
	scope := r.URL.Query().Get("scope")
	if scope == "mine" {
		rows, err := a.approval.Mine(r.Context(), userID, 0)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "list requests failed", err)
			return
		}
		out := approverRows(rows, false)
		a.enrichApproverRows(r.Context(), out)
		writeJSON(w, http.StatusOK, out)
		return
	}
	// Default scope = decidable (eligibility filtered server-side, fresh challenges).
	rows, err := a.approval.Decidable(r.Context(), userID, deviceID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list decidable failed", err)
		return
	}
	out := approverRows(rows, true)
	a.enrichApproverRows(r.Context(), out)
	writeJSON(w, http.StatusOK, out)
}

// historyMaxLimit clamps the history page size; over-max is clamped (like
// api_changes.go), never 400.
const historyMaxLimit = 200

// approverHistoryResponse is the paginated history envelope: an items array plus
// an opaque continuation token. next_cursor empty ⇒ the caller has the last
// page. snake_case matches the rest of the approver surface (created_at, …).
type approverHistoryResponse struct {
	Items      []approverRow `json:"items"`
	NextCursor string        `json:"next_cursor"`
}

// handleApproverHistory serves GET /v1/approver/history?limit=50&cursor=<opaque>.
// The feed is keyset-paginated over the UUIDv7 id; limit defaults to 50 and
// clamps at 200; an absent cursor is the newest page. A malformed/stale cursor
// fails closed with 400 invalid_cursor.
func (a *App) handleApproverHistory(w http.ResponseWriter, r *http.Request, _ /*deviceID*/, userID string) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > historyMaxLimit {
		limit = historyMaxLimit
	}
	beforeID, ok := parseHistoryCursor(r.URL.Query().Get("cursor"))
	if !ok {
		apiErrorCode(w, http.StatusBadRequest, codeInvalidCursor,
			"invalid or stale cursor: re-fetch history from the start")
		return
	}
	rows, next, err := a.approval.History(r.Context(), userID, beforeID, limit)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list history failed", err)
		return
	}
	items := approverRows(rows, false)
	a.enrichApproverRows(r.Context(), items)
	writeJSON(w, http.StatusOK, approverHistoryResponse{
		Items:      items,
		NextCursor: historyCursor(next),
	})
}

// historyCursor encodes an opaque keyset cursor: base64url("1\x00<last_scanned_id>").
// An empty id (end of feed) encodes to an empty cursor so the client stops.
func historyCursor(lastScannedID string) string {
	if lastScannedID == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("1\x00" + lastScannedID))
}

// parseHistoryCursor decodes an opaque history cursor to its before-id. An empty
// cursor is the newest page ⇒ ("", true). Any decode error, wrong version, or an
// empty embedded id fails closed ⇒ ("", false), and the handler answers 400
// invalid_cursor (opaque so a phone client cannot hand-forge row ids). Mirrors
// parseCatalogCursor's fail-closed shape.
func parseHistoryCursor(cursor string) (beforeID string, ok bool) {
	if cursor == "" {
		return "", true
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] != "1" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func approverRows(rows []approval.PendingRow, decidable bool) []approverRow {
	out := make([]approverRow, len(rows))
	for i, pr := range rows {
		out[i] = toApproverRow(pr, decidable)
	}
	return out
}
