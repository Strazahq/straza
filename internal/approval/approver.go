package approval

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// Durations for the mobile approver surface.
const (
	// EnrollTokenTTL bounds an admin-minted one-time enroll token: long enough
	// to scan a QR, short enough that a lifted token goes stale fast.
	EnrollTokenTTL = 10 * time.Minute
	// ChallengeTTL bounds a decision or refresh challenge to at most 5
	// minutes, the approver app's limit. Exported so the server can report
	// expires_in on the refresh-challenge response without re-deriving the
	// window.
	ChallengeTTL = 5 * time.Minute
	// challengeBytes is the challenge nonce size. The approver app requires
	// at least 16 bytes.
	challengeBytes = 32
	// decisionSkew is the ±window the signed decision timestamp may fall in.
	decisionSkew = 5 * time.Minute
)

// Errors surfaced by the approver surface, mapped to HTTP status in the API.
var (
	// ErrEnrollTokenInvalid: the enroll token is unknown, expired, or already used (→401).
	ErrEnrollTokenInvalid = errors.New("approval: enroll token invalid, expired, or already used")
	// ErrBadKey: the device public key is not an ECDSA P-256 SPKI key (→400).
	ErrBadKey = errors.New("approval: device public key must be an ECDSA P-256 SPKI DER key")
	// ErrBadSignature: the decision signature did not verify against the enrolled key (→401).
	ErrBadSignature = errors.New("approval: decision signature verification failed")
	// ErrChallengeInvalid: the challenge is missing, expired, or already used, a replay (→401).
	ErrChallengeInvalid = errors.New("approval: challenge missing, expired, or already used")
	// ErrStaleTimestamp: the decision timestamp is outside the ±5m window (→401).
	ErrStaleTimestamp = errors.New("approval: decision timestamp outside the acceptable window")
	// ErrDeviceRevoked: the approver device row is gone or does not match the token (→401).
	ErrDeviceRevoked = errors.New("approval: approver device is revoked or unknown")
	// ErrUserInactive: the device's bound user is disabled, missing, or
	// lock-revoked on the denylist lane (→401).
	ErrUserInactive = errors.New("approval: approver device's bound user is not active")
	// ErrStoreUnavailable: the store behind a device/user read was
	// unreachable or timed out. Distinct from
	// ErrDeviceRevoked/ErrUserInactive on purpose: those two make the app
	// destroy its key, an outage must read as "retry later, key untouched".
	// Wraps the cause so the handler can log it; the body stays generic.
	ErrStoreUnavailable = errors.New("approval: store temporarily unavailable")
	// ErrBadPushKind: the push kind is not one of fcm|apns|unifiedpush|webpush (→400).
	ErrBadPushKind = errors.New("approval: push kind must be fcm|apns|unifiedpush|webpush")
)

// EnrollInput is the device half of POST /v1/approver/enroll.
type EnrollInput struct {
	EnrollToken      string
	Name             string
	Platform         string
	KeyAlg           string
	PublicKeyB64     string // base64 X.509 SubjectPublicKeyInfo DER
	KeySecurityLevel string
	AttestationKind  string
	AttestationBlob  string
}

// EnrollResult is what the API needs to mint the device token after enrolment
// and to name the person whose enroll token was consumed in the audit record.
type EnrollResult struct {
	DeviceID string
	UserID   string
	Username string
}

// EnrollTokenGrant is the one-time enroll credential; Token is the opaque
// secret returned to the minting surface exactly once (rendered into the QR).
type EnrollTokenGrant struct {
	Token     string
	UserID    string
	Username  string
	ExpiresAt time.Time
}

// Enroll-token channel scopes: a self-minted
// token is stamped with the channel its role authorized and consume refuses a
// platform outside it. The empty channel is the admin lane: unscoped.
const (
	EnrollChannelMobile  = "mobile"
	EnrollChannelBrowser = "browser"
)

// SummaryView is the redacted-by-construction tool identity for a pending row:
// tool/app/tool name only, never raw arguments or command text (privacy rule).
//
// JSON tags are snake_case to match the rest of the approver API envelope
// (created_at, rule_id, decided_by, …). This is the ONLY place SummaryView is
// serialized; the internal policy-event vocabulary keeps its own camelCase
// `toolName` (eventkey.go, pdp.go) and is unaffected.
type SummaryView struct {
	Tool     string `json:"tool"`
	App      string `json:"app,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
}

// PendingRow is one row of GET /v1/approver/pending or /history. Challenge is
// present on decidable rows only; State/DecidedBy/DecidedAt describe resolution
// on mine/history rows.
type PendingRow struct {
	ID            string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	DecidedAt     *time.Time
	RequesterName string
	RequesterKind string // human|nhi
	RuleID        string
	SetName       string
	Summary       SummaryView
	Justification string
	State         string
	DecidedBy     string // decider display name
	Challenge     string
	Mode          string // approve|confirm ("" reads as approve)
	// Decided attribution: the surface the decision came from
	// (phone|browser|console|slack; legacy rows "api"), the enrolled signing
	// device, and the decider's own words. All "" on pending rows.
	Channel         string
	DecidedReason   string
	DecidedDeviceID string
	// Args preview. Present only when the preview knob was on at request time
	// and the call had concrete args. ArgvHashPrefix and BindingScope travel
	// WITH ArgsPreview (set only when it is non-empty) so a surface never
	// renders an honesty line without a body.
	ArgsPreview    string
	ArgsTruncated  bool
	ArgsBytes      int
	ArgvHashPrefix string
	BindingScope   string
}

// SignedDecision is the parsed POST /v1/approver/decide body plus the identity
// the device token proved (DeviceID, DeciderUserID).
type SignedDecision struct {
	RequestID     string
	Verdict       string // approve|deny (wire literal; part of the signed string)
	Challenge     string
	SignatureB64  string // base64 ASN.1 DER (X9.62), variable length
	TS            int64  // client unix seconds
	DeviceID      string
	DeciderUserID string
	// Reason is the decider's optional own words. When present, the
	// signed string grows a 5th line hex(sha256(Reason)), so the words are
	// bound to the same key as the verdict; absent, the 4-line string
	// verifies bit-for-bit. The 4-line form is the CANONICAL encoding of a
	// reason-less decision (optional-field encoding, not a tolerated legacy):
	// there is no sunset, and deployed phone builds are correct by
	// construction rather than by grace.
	Reason string
}

// MintEnrollToken creates a one-time, short-TTL enroll token bound to userID.
// It returns the opaque secret ONCE; only its hash is stored. channel scopes
// what the token may enroll (EnrollChannelMobile|EnrollChannelBrowser for the
// self-service lane; "" for the admin lane, unscoped).
func (s *Service) MintEnrollToken(ctx context.Context, userID, channel string) (EnrollTokenGrant, error) {
	u, err := s.st.Users().GetByID(ctx, userID)
	if err != nil {
		return EnrollTokenGrant{}, err // ErrNotFound → 404
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return EnrollTokenGrant{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	exp := now.Add(EnrollTokenTTL)
	if _, err := s.st.Approvers().InsertEnrollToken(ctx, store.ApproverEnrollToken{
		TokenHash: sha256hex(token), UserID: userID, Channel: channel,
		CreatedAt: now, ExpiresAt: exp,
	}); err != nil {
		return EnrollTokenGrant{}, err
	}
	return EnrollTokenGrant{Token: token, UserID: userID, Username: u.Username, ExpiresAt: exp}, nil
}

// Enroll consumes a one-time enroll token and registers an approver device. The
// public key is parsed as SPKI and REQUIRED to be ECDSA on P-256: the curve is
// read from the key, so a lying key_alg cannot steer the server. Fail closed:
// any bad key, spent/expired token, or disabled/lock-revoked bound user denies
// enrolment with one coarse ErrEnrollTokenInvalid (no oracle for WHY it died).
func (s *Service) Enroll(ctx context.Context, in EnrollInput) (EnrollResult, error) {
	if in.EnrollToken == "" {
		return EnrollResult{}, ErrEnrollTokenInvalid
	}
	// Validate the key before spending the one-time token (no side effect yet).
	if _, err := parseP256SPKI(in.PublicKeyB64); err != nil {
		return EnrollResult{}, ErrBadKey
	}
	tok, err := s.st.Approvers().ConsumeEnrollToken(ctx, sha256hex(in.EnrollToken), s.now())
	if err != nil {
		return EnrollResult{}, ErrEnrollTokenInvalid
	}
	u, err := s.st.Users().GetByID(ctx, tok.UserID)
	if err != nil || u.Status != store.UserActive || s.userBlocked(u.ID) {
		return EnrollResult{}, ErrEnrollTokenInvalid
	}
	// Channel scope: a scoped token enrolls
	// only its channel's platforms; an unknown stamp fails closed. The token
	// is already spent by the CAS above, so a mismatch burns it, matching
	// every other post-consume failure, and answers the same coarse 401.
	switch tok.Channel {
	case "": // admin-minted: any platform
	case EnrollChannelMobile:
		if surfaceForPlatform(in.Platform) != "phone" {
			return EnrollResult{}, ErrEnrollTokenInvalid
		}
	case EnrollChannelBrowser:
		if surfaceForPlatform(in.Platform) != "browser" {
			return EnrollResult{}, ErrEnrollTokenInvalid
		}
	default:
		return EnrollResult{}, ErrEnrollTokenInvalid
	}
	dev, err := s.st.Approvers().InsertDevice(ctx, store.ApproverDevice{
		UserID: tok.UserID, Name: in.Name, Platform: in.Platform, KeyAlg: in.KeyAlg,
		PublicKey: in.PublicKeyB64, KeySecurityLevel: in.KeySecurityLevel,
		AttestationKind: in.AttestationKind, AttestationBlob: in.AttestationBlob,
	})
	if err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{DeviceID: dev.ID, UserID: tok.UserID, Username: u.Username}, nil
}

// Decidable returns the pending records the bound user MAY decide, each with a
// fresh single-use challenge. Eligibility is filtered server-side: kind=nhi
// users NEVER get rows, nor does a user whose row cannot be read until the
// store answers; four-eyes rules hide the requester's own record; the
// bound user must hold one of the rule's approve roles (fresh identity read) or
// the straza-admin fallback.
func (s *Service) Decidable(ctx context.Context, deciderUserID, deviceID string) ([]PendingRow, error) {
	if nhi, _ := s.isNHI(ctx, deciderUserID); nhi {
		return []PendingRow{}, nil
	}
	roles, err := s.resolver.ResolveRoles(ctx, deciderUserID, s.now())
	if err != nil {
		return nil, err
	}
	roleSet := roleNameSet(roles)
	deciderName := s.deciderUsername(ctx, deciderUserID)
	pending, err := s.st.Approvals().List(ctx, string(StatePending))
	if err != nil {
		return nil, err
	}
	out := []PendingRow{}
	for _, a := range pending {
		rec := recordFromStore(a)
		if !eligibleToDecide(rec, deciderUserID, deciderName, roleSet) {
			continue
		}
		ch, err := s.mintChallenge(ctx, deviceID, rec.ID)
		if err != nil {
			return nil, err // fail closed
		}
		out = append(out, s.toPendingRow(ctx, rec, ch))
	}
	_ = s.st.Approvers().TouchDevice(ctx, deviceID, s.now())
	return out, nil
}

// Mine returns the bound user's OWN records (status-watching), newest first,
// with no challenge: a requester can watch but never decide.
func (s *Service) Mine(ctx context.Context, requesterUserID string, limit int) ([]PendingRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.st.Approvals().List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := []PendingRow{}
	for _, a := range rows {
		if a.UserID != requesterUserID {
			continue
		}
		out = append(out, s.toPendingRow(ctx, recordFromStore(a), ""))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// historyScanBudget caps how many rows one History request scans across its
// fill-loop windows. Visibility is a post-fetch Go filter, so a page may span
// several keyset windows to stay dense; the budget bounds that work per request
// (a sparse page still returns with a live cursor to continue from).
const historyScanBudget = 500

// History returns RESOLVED records visible to the bound user, their own OR ones
// they were eligible to decide (role/admin-fallback), newest first, paginated
// by an opaque keyset cursor (beforeID; empty = newest page). It scans
// successive ListBefore windows, filtering visibility in Go, until it has
// `limit` visible rows OR the scan budget is spent OR the feed ends. nextCursor
// is the id of the last SCANNED row (empty ⇒ end of feed), so a filtered-out row
// is never skipped across a page boundary, the rule the changes feed in
// api_changes.go follows too.
func (s *Service) History(ctx context.Context, userID, beforeID string, limit int) ([]PendingRow, string, error) {
	if limit <= 0 {
		limit = 50
	}
	roles, err := s.resolver.ResolveRoles(ctx, userID, s.now())
	if err != nil {
		return nil, "", err
	}
	roleSet := roleNameSet(roles)
	deciderName := s.deciderUsername(ctx, userID)

	out := []PendingRow{}
	cursor := beforeID
	nextCursor := ""
	scanned := 0
	for len(out) < limit && scanned < historyScanBudget {
		window := limit
		if rem := historyScanBudget - scanned; window > rem {
			window = rem
		}
		rows, err := s.st.Approvals().ListBefore(ctx, cursor, window)
		if err != nil {
			return nil, "", err
		}
		for _, a := range rows {
			cursor = a.ID     // advance the keyset seek
			nextCursor = a.ID // every scanned row moves the cursor, even filtered ones
			scanned++
			if a.State == string(StatePending) {
				continue
			}
			rec := recordFromStore(a)
			if rec.UserID != userID && !userEligible(rec, deciderName) && !roleEligible(rec, roleSet) {
				continue
			}
			out = append(out, s.toPendingRow(ctx, rec, ""))
			if len(out) >= limit {
				break
			}
		}
		if len(rows) < window && len(out) < limit {
			// Exhaustion is only provable when the short window was FULLY
			// scanned. If the page filled early inside a short window, rows
			// after the fill point were never scanned, so the cursor must keep
			// pointing at the fill row so the tail stays reachable (the caller
			// pays one extra empty-page hop at the true end instead).
			nextCursor = "" // the feed is exhausted; signal the caller to stop
			break
		}
	}
	return out, nextCursor, nil
}

// DecideSigned verifies a hardware-signed mobile decision and applies it. Order:
// verdict/timestamp/reason sanity → device row (revocation) + enrolled key →
// ECDSA-P256 verify over the reconstructed message (built from PARSED fields,
// never the raw body) → atomic single-use challenge consume (replay gate) → the
// SAME authorization every lane runs via Decide (fresh role check, NHI
// hard-block, one-time state transition, broadcast, audit). The persisted
// channel is the honest surface the enrolled device implies (phone|browser),
// not the legacy transport constant "api".
func (s *Service) DecideSigned(ctx context.Context, in SignedDecision) (Record, error) {
	if in.Verdict != "approve" && in.Verdict != "deny" {
		return Record{}, ErrBadVerdict
	}
	if err := validateReason(in.Reason); err != nil {
		return Record{}, err
	}
	nowUnix := s.now().Unix()
	skew := int64(decisionSkew / time.Second)
	if in.TS < nowUnix-skew || in.TS > nowUnix+skew {
		return Record{}, ErrStaleTimestamp
	}
	dev, err := s.st.Approvers().GetDevice(ctx, in.DeviceID)
	if err != nil && store.IsUnavailable(err) {
		return Record{}, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	if err != nil || dev.UserID != in.DeciderUserID {
		return Record{}, ErrDeviceRevoked
	}
	pub, err := parseP256SPKI(dev.PublicKey)
	if err != nil {
		return Record{}, ErrBadKey
	}
	// The exact signed byte string (UTF-8, no trailing newline). ECDSA sigs are
	// variable-length DER: decode and verify, never length-check. A reason adds
	// a 5th line carrying its sha256 (hex): the signature then binds the words
	// too, so a reason can neither be attached, stripped, nor swapped in
	// transit; without one the 4-line string stays bit-identical for deployed
	// phone builds.
	msg := in.RequestID + "\n" + in.Verdict + "\n" + in.Challenge + "\n" + strconv.FormatInt(in.TS, 10)
	if in.Reason != "" {
		rh := sha256.Sum256([]byte(in.Reason))
		msg += "\n" + hex.EncodeToString(rh[:])
	}
	sig, err := base64.StdEncoding.DecodeString(in.SignatureB64)
	if err != nil {
		return Record{}, ErrBadSignature
	}
	digest := sha256.Sum256([]byte(msg))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return Record{}, ErrBadSignature
	}
	won, err := s.st.Approvers().ConsumeChallenge(ctx, in.Challenge, in.DeviceID, in.RequestID, s.now())
	if err != nil {
		return Record{}, err
	}
	if !won {
		return Record{}, ErrChallengeInvalid
	}
	return s.Decide(ctx, in.RequestID, stateFromVerdict(in.Verdict), in.DeciderUserID,
		surfaceForPlatform(dev.Platform), in.Reason, in.DeviceID)
}

// surfaceForPlatform maps an enrolled device's platform to the decider-facing
// surface persisted on the record. "device" is the honest fallback for a
// platform value this build does not know.
func surfaceForPlatform(platform string) string {
	switch platform {
	case "android", "ios":
		return "phone"
	case "browser":
		return "browser"
	default:
		return "device"
	}
}

// PushRegistration is the registration input for one push route. P256DH and
// Auth are the OPTIONAL Web Push subscription keys (RFC 8291 §3.2, unpadded
// base64url): required for kind=webpush, the encrypted-lane opt-in for
// kind=unifiedpush, refused for fcm/apns. Validation is strict; see
// validatePushRegistration for the full rule set.
type PushRegistration struct {
	Kind            string
	TokenOrEndpoint string
	P256DH          string
	Auth            string
}

// RegisterPush validates and stores (deduped) a push registration for the
// device. A keyed subscription is stored as one canonical endpoint+keys
// value: endpoint and keys change together on every resubscribe, so they
// are one credential.
func (s *Service) RegisterPush(ctx context.Context, deviceID string, in PushRegistration) error {
	if !pushKinds[in.Kind] || in.TokenOrEndpoint == "" {
		return ErrBadPushKind
	}
	stored, err := s.validatePushRegistration(in.Kind, in.TokenOrEndpoint, in.P256DH, in.Auth)
	if err != nil {
		return err
	}
	return s.st.Approvers().UpsertPush(ctx, store.ApproverPush{
		DeviceID: deviceID, Kind: in.Kind, TokenOrEndpoint: stored,
	})
}

// DeletePush removes a push registration (idempotent). Removal mirrors
// registration: a route registered WITH keys is addressed by the same
// endpoint+keys value, reconstructed here WITHOUT validation so a bad or
// stale row can always be removed.
func (s *Service) DeletePush(ctx context.Context, deviceID string, in PushRegistration) error {
	stored := in.TokenOrEndpoint
	if in.P256DH != "" && in.Auth != "" {
		stored = encodeKeyedEndpoint(in.TokenOrEndpoint, in.P256DH, in.Auth)
	}
	return s.st.Approvers().DeletePush(ctx, deviceID, in.Kind, stored)
}

var pushKinds = map[string]bool{"fcm": true, "apns": true, "unifiedpush": true, "webpush": true}

// mintChallenge stores and returns a fresh single-use challenge bound to
// (device, approval).
func (s *Service) mintChallenge(ctx context.Context, deviceID, approvalID string) (string, error) {
	b := make([]byte, challengeBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ch := base64.RawURLEncoding.EncodeToString(b)
	now := s.now()
	if err := s.st.Approvers().InsertChallenge(ctx, store.ApproverChallenge{
		Challenge: ch, DeviceID: deviceID, ApprovalID: approvalID,
		CreatedAt: now, ExpiresAt: now.Add(ChallengeTTL),
	}); err != nil {
		return "", err
	}
	return ch, nil
}

func (s *Service) toPendingRow(ctx context.Context, rec Record, challenge string) PendingRow {
	row := PendingRow{
		ID: rec.ID, CreatedAt: rec.CreatedAt, ExpiresAt: rec.ExpiresAt, DecidedAt: rec.DecidedAt,
		RequesterName: rec.Username, RequesterKind: s.requesterKind(ctx, rec.UserID),
		RuleID: rec.RuleID, SetName: rec.SetName, Summary: parseSummary(rec.Summary),
		Justification: rec.Justification, State: string(rec.State),
		DecidedBy: rec.DecidedByName, Challenge: challenge, Mode: rec.Mode,
		Channel: rec.Channel, DecidedReason: rec.DecidedReason, DecidedDeviceID: rec.DecidedDeviceID,
		ArgsPreview: rec.ArgsPreview, ArgsTruncated: rec.ArgsTruncated, ArgsBytes: rec.ArgsBytes,
	}
	// Field-pairing invariant: the hash prefix + binding scope (and thus the
	// honesty line) ride ALONG with a present preview, never alone.
	if rec.ArgsPreview != "" {
		row.ArgvHashPrefix = HashPrefix(rec.ArgvHash)
		row.BindingScope = BindingScopeForRecord(rec.ArgvHash, rec.Summary)
	}
	return row
}

// requesterKind reports "nhi" for an agent requester, "human" otherwise.
func (s *Service) requesterKind(ctx context.Context, userID string) string {
	if userID == "" {
		return "human"
	}
	if u, err := s.st.Users().GetByID(ctx, userID); err == nil && userIsNHI(u) {
		return "nhi"
	}
	return "human"
}

// isNHI reports whether a user is a non-human identity. It fails closed: a
// user whose row cannot be read is not treated as a person, and err then
// wraps ErrStoreUnavailable and the cause, so a decision answers that the
// store is unavailable and never calls a person non-human. An empty id is no
// user and reports false.
func (s *Service) isNHI(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	u, err := s.st.Users().GetByID(ctx, userID)
	if err != nil {
		return true, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	return userIsNHI(u), nil
}

// eligibleToDecide applies the decide-time gate used to build the decidable
// list: confirm records belong to their requester alone; otherwise a
// non-self-approvable record hides the requester's own row (requester
// exclusion), then the user-scoped pool (revision 13), then role/admin
// fallback.
func eligibleToDecide(rec Record, userID, username string, roleSet map[string]bool) bool {
	if rec.Mode == policy.ModeConfirm {
		return rec.UserID == userID
	}
	if !rec.SelfApproval && rec.UserID == userID {
		return false
	}
	return userEligible(rec, username) || roleEligible(rec, roleSet)
}

// userEligible reports whether the username sits in the record's user-scoped
// decide pool (revision 13 approve.deciders, resolved at request time).
func userEligible(rec Record, username string) bool {
	if username == "" {
		return false
	}
	for _, n := range rec.ApproverUsers {
		if n == username {
			return true
		}
	}
	return false
}

// roleEligible reports whether the role set covers the record's approver
// roles. The straza-admin fallback applies only to a record whose decide
// pool is ENTIRELY empty (no roles and no user-scoped deciders): a
// sponsor-routed record is decided by its sponsor, not by every admin,
// unless the rule also lists an admin role.
func roleEligible(rec Record, roleSet map[string]bool) bool {
	want := rec.ApproverRoles
	if len(want) == 0 {
		if len(rec.ApproverUsers) > 0 {
			return false
		}
		want = []string{AdminFallbackRole}
	}
	for _, r := range want {
		if roleSet[r] {
			return true
		}
	}
	return false
}

// deciderUsername resolves a decider's username for the user-scoped pool
// check; "" (unreadable user) simply fails that pool and leaves the role
// path to speak.
func (s *Service) deciderUsername(ctx context.Context, userID string) string {
	if userID == "" {
		return ""
	}
	u, err := s.st.Users().GetByID(ctx, userID)
	if err != nil {
		return ""
	}
	return u.Username
}

// parseSummary extracts the tool identity from a stored summary label, keeping
// only tool/app/tool name and dropping any command/path detail after ':'.
func parseSummary(s string) SummaryView {
	if rest, ok := strings.CutPrefix(s, "mcp.call "); ok {
		if app, tool, found := strings.Cut(rest, ":"); found {
			return SummaryView{Tool: "mcp.call", App: strings.TrimSpace(app), ToolName: strings.TrimSpace(tool)}
		}
		return SummaryView{Tool: "mcp.call", ToolName: strings.TrimSpace(rest)}
	}
	if head, _, found := strings.Cut(s, ":"); found {
		return SummaryView{Tool: strings.TrimSpace(head)}
	}
	return SummaryView{Tool: strings.TrimSpace(s)}
}

// userIsNHI reports whether a user is a non-human identity. Two signals,
// OR-ed and fail-closed: the create-time attrs kind=nhi flag (agentic SCIM
// or admin create) and the IdM-mastered typology (userType agent|service).
// Either alone is enough: a midPoint-born agent arrives typed but without
// the agentic URN (the SCIM connector cannot send it), and it must still
// never decide an approval. The read-side twin is internal/server userKind.
func userIsNHI(u store.User) bool {
	if u.UserType == store.UserTypeAgent || u.UserType == store.UserTypeService {
		return true
	}
	if u.Attrs == "" {
		return false
	}
	var m map[string]any
	if json.Unmarshal([]byte(u.Attrs), &m) == nil && m["kind"] == "nhi" {
		return true
	}
	return false
}

func parseP256SPKI(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("approval: public key is not ECDSA")
	}
	if ec.Curve != elliptic.P256() {
		return nil, errors.New("approval: ECDSA key is not on P-256")
	}
	return ec, nil
}

func stateFromVerdict(v string) string {
	if v == "approve" {
		return string(StateApproved)
	}
	return string(StateDenied)
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func roleNameSet(roles []store.Role) map[string]bool {
	m := make(map[string]bool, len(roles))
	for _, r := range roles {
		m[r.Name] = true
	}
	return m
}
