package approval

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// slackDefaultAPIBase is the real Slack Web API root; tests inject an httptest
// server URL instead.
const slackDefaultAPIBase = "https://slack.com/api"

// slackReplayWindow bounds accepted request-timestamp skew (Slack's own
// recommendation): a signed callback older than this is rejected as a replay.
const slackReplayWindow = 300 * time.Second

// slackChannel is the Slack Block Kit approver channel (stdlib
// net/http only, no SDK). It posts an opaque, summary-only card (privacy rule:
// never command text or transcript content) with signed Approve/Deny buttons,
// and verifies inbound block_actions callbacks by X-Slack-Signature.
type slackChannel struct {
	botToken             string
	signingSecret        string
	channel              string
	includeJustification bool
	apiBase              string
	http                 *http.Client
	st                   store.Store
	svc                  *Service
	log                  *slog.Logger
}

func newSlackChannel(cfg config.SlackChannel, st store.Store, svc *Service, log *slog.Logger) (*slackChannel, error) {
	token, err := resolveSecret(cfg.BotToken, cfg.BotTokenFile)
	if err != nil {
		return nil, fmt.Errorf("approval.channels.slack.botToken: %w", err)
	}
	secret, err := resolveSecret(cfg.SigningSecret, cfg.SigningSecretFile)
	if err != nil {
		return nil, fmt.Errorf("approval.channels.slack.signingSecret: %w", err)
	}
	return &slackChannel{
		botToken: token, signingSecret: secret, channel: cfg.Channel,
		includeJustification: cfg.IncludeJustification,
		apiBase:              slackDefaultAPIBase,
		http:                 &http.Client{Timeout: 10 * time.Second, CheckRedirect: approvalRedirect},
		st:                   st, svc: svc, log: log,
	}, nil
}

// resolveSecret reads an inline value or a file (file wins when both empty is
// impossible; validate rejects both-set upstream). Trailing whitespace in the
// file is trimmed.
func resolveSecret(inline, file string) (string, error) {
	if file != "" {
		raw, err := os.ReadFile(file) // #nosec G304 -- operator-configured secret path
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return inline, nil
}

// slackChannel is a lifecycle notifier (notifier.go); the interactive
// callback below is its channel-specific surface.
var _ notifier = (*slackChannel)(nil)

// name returns the `approve.notify` routing vocabulary entry for this channel
// (policy.NotifySlack, spec/policyset revision 8).
func (sc *slackChannel) name() string { return policy.NotifySlack }

// resolved repaints the card for a resolution that carries no human decision.
// The expiry sweep claims the row on one pod and never broadcasts, so this
// hook is the card's only path there. A human decision reaches every pod
// through the resolution broadcast and reconcile repaints the card on it, so
// it is skipped here to keep one update per decision.
func (sc *slackChannel) resolved(rec Record) {
	if rec.DecidedBy != "" {
		return
	}
	sc.reconcile(rec)
}

// created posts the approval card and records the message coordinates so the
// reconcile lane can update it in place later.
func (sc *slackChannel) created(rec Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	blocks := sc.requestBlocks(rec)
	body := map[string]any{"channel": sc.channel, "text": "Approval requested", "blocks": blocks}
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		TS      string `json:"ts"`
		Channel string `json:"channel"`
	}
	err := sc.call(ctx, "chat.postMessage", body, &out)
	if err == nil && !out.OK {
		err = fmt.Errorf("slack: %s", out.Error)
	}
	sc.svc.recordDelivery(policy.NotifySlack, err, "card posted")
	if err != nil {
		sc.log.Warn("approval slack notify failed", "id", rec.ID, "err", err)
		return
	}
	refs := map[string]string{"slack_ts": out.TS, "slack_channel": out.Channel}
	if err := sc.st.Approvals().SetChannelRefs(ctx, rec.ID, refs); err != nil {
		sc.log.Warn("approval slack ref persist failed", "id", rec.ID, "err", err)
	}
}

// requestBlocks builds the Block Kit payload. The Approve/Deny button values
// are decision tokens bound to (id, verdict) and expiring with the record.
func (sc *slackChannel) requestBlocks(rec Record) []map[string]any {
	approveTok, _ := sc.svc.MintDecisionToken(rec.ID, string(StateApproved), rec.ExpiresAt)
	denyTok, _ := sc.svc.MintDecisionToken(rec.ID, string(StateDenied), rec.ExpiresAt)
	fields := []map[string]any{
		{"type": "mrkdwn", "text": "*Requester:*\n" + slackEscape(rec.Username)},
		{"type": "mrkdwn", "text": "*Action:*\n" + slackEscape(rec.Summary)},
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": "Approval requested"}},
		{"type": "section", "fields": fields},
	}
	// Params preview: the concrete, redacted, display-safe call
	// arguments in a code block ABOVE the justification (no chip; the server's own
	// record). Hard-clamped to stay under Block Kit's 3000-char section limit.
	if rec.ArgsPreview != "" {
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": "*Call parameters (preview):*\n```" + slackClampPreview(rec.ArgsPreview) + "```"},
		})
	}
	if sc.includeJustification && rec.Justification != "" {
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": "*Agent's stated reason (unverified):*\n" + slackEscape(rec.Justification)},
		})
	}
	// Honesty line beneath BOTH (ordering hierarchy), plus a truncation footnote.
	if rec.ArgsPreview != "" {
		note := HonestyLine(BindingScopeForRecord(rec.ArgvHash, rec.Summary), HashPrefix(rec.ArgvHash))
		if rec.ArgsTruncated {
			note += " · preview truncated (" + strconv.Itoa(rec.ArgsBytes) + " bytes)"
		}
		blocks = append(blocks, map[string]any{
			"type":     "context",
			"elements": []map[string]any{{"type": "mrkdwn", "text": slackEscape(note)}},
		})
	}
	blocks = append(blocks, map[string]any{
		"type":     "actions",
		"block_id": rec.ID,
		"elements": []map[string]any{
			{"type": "button", "action_id": "approve", "style": "primary",
				"text": map[string]any{"type": "plain_text", "text": "Approve"}, "value": approveTok},
			{"type": "button", "action_id": "deny", "style": "danger",
				"text": map[string]any{"type": "plain_text", "text": "Deny"}, "value": denyTok},
		},
	})
	return blocks
}

// reconcile replaces the card's buttons with a resolution note (best effort).
// Triggered on every pod from the resolution broadcast for ANY channel's
// decision, and once from resolved on the pod that expired the record;
// chat.update with the same content is idempotent, and the ref guard below
// skips records this channel never carded.
func (sc *slackChannel) reconcile(rec Record) {
	ts := rec.ChannelRefs["slack_ts"]
	ch := rec.ChannelRefs["slack_channel"]
	if ts == "" || ch == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	when := ""
	if rec.DecidedAt != nil {
		when = " · " + rec.DecidedAt.Format("15:04")
	}
	verb := capitalize(string(rec.State)) // "Approved"/"Denied"/"Expired"
	note := verb
	if rec.DecidedByName != "" {
		note = verb + " by " + rec.DecidedByName
	}
	body := map[string]any{
		"channel": ch, "ts": ts, "text": note + when,
		"blocks": []map[string]any{
			{"type": "header", "text": map[string]any{"type": "plain_text", "text": "Approval requested"}},
			{"type": "section", "fields": []map[string]any{
				{"type": "mrkdwn", "text": "*Requester:*\n" + slackEscape(rec.Username)},
				{"type": "mrkdwn", "text": "*Action:*\n" + slackEscape(rec.Summary)},
			}},
			{"type": "context", "elements": []map[string]any{
				{"type": "mrkdwn", "text": note + when},
			}},
		},
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	err := sc.call(ctx, "chat.update", body, &out)
	if err == nil && !out.OK {
		err = fmt.Errorf("slack: %s", out.Error)
	}
	sc.svc.recordDelivery(policy.NotifySlack, err, "card updated")
	if err != nil {
		sc.log.Warn("approval slack card update failed", "id", rec.ID, "err", err)
	}
}

// testMessage posts a content-free test line to the configured channel, the
// admin channel card's active verification (status.go TestChannel).
func (sc *slackChannel) testMessage(ctx context.Context) error {
	body := map[string]any{"channel": sc.channel,
		"text": "Straza test notification. The Slack approval channel is wired correctly."}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	err := sc.call(ctx, "chat.postMessage", body, &out)
	if err == nil && !out.OK {
		err = fmt.Errorf("slack: %s", out.Error)
	}
	sc.svc.recordDelivery(policy.NotifySlack, err, "test message")
	return err
}

// slackInteraction is the block_actions callback payload subset we consume.
type slackInteraction struct {
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	ResponseURL string `json:"response_url"`
	Actions     []struct {
		ActionID string `json:"action_id"`
		BlockID  string `json:"block_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

// HandleCallback verifies, maps, and applies an inbound Slack decision. It
// answers within the ack budget (Decide is inline; the card update rides the
// resolution broadcast). Signature failure ⇒ 401; everything else ⇒ 200 with
// an ephemeral note so the tapping user gets feedback.
func (sc *slackChannel) HandleCallback(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !sc.verifySignature(r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), raw) {
		http.Error(w, "bad slack signature", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var in slackInteraction
	if err := json.Unmarshal([]byte(form.Get("payload")), &in); err != nil || len(in.Actions) == 0 {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	act := in.Actions[0]
	verdict := ""
	switch act.ActionID {
	case "approve":
		verdict = string(StateApproved)
	case "deny":
		verdict = string(StateDenied)
	default:
		slackEphemeral(w, "Unrecognized action.")
		return
	}
	id := act.BlockID
	if err := sc.svc.VerifyDecisionToken(act.Value, id, verdict); err != nil {
		sc.log.Warn("approval slack callback: bad decision token", "id", id, "err", err)
		slackEphemeral(w, "This approval button is no longer valid.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	u, ok := sc.mapUser(ctx, in.User.ID)
	if !ok {
		// Unmappable tap: no record change and no audit CE, only a log line.
		sc.log.Warn("approval slack callback: unmappable slack user", "slackUser", in.User.ID, "approval", id)
		slackEphemeral(w, "Your Slack identity is not linked to a Straza user, so this decision was not recorded.")
		return
	}
	// The mapping is this lane's sign-in, so it judges the person as the
	// console's session and the phone's device token are judged before they
	// reach Decide: a disabled or locked user never decides.
	if words, reason := sc.standing(u); words != "" {
		sc.auditRefusal(ctx, u, id, reason)
		slackEphemeral(w, "Could not record decision: "+words)
		return
	}
	// Emails are not unique, so every reply after the mapping names the
	// Straza user it picked, and a person who shares an email sees who that is.
	matched := " Your Slack email matched the Straza user " + slackEscape(u.Username) + "."
	rec, err := sc.svc.decide(ctx, id, verdict, u.ID, "slack", "", "")
	if errors.Is(err, errAlreadyDecided) {
		slackEphemeral(w, slackAlreadyDecided(rec)+matched)
		return
	}
	if err != nil {
		slackEphemeral(w, "Could not record decision: "+decideErrorText(err)+"."+matched)
		return
	}
	slackEphemeral(w, "Recorded: "+verdict+"."+matched)
}

// slackAlreadyDecided answers a tap whose verdict the record already held:
// the verdict, who decided it, on which lane and when, so a repeated tap is
// not told that it recorded anything.
func slackAlreadyDecided(rec Record) string {
	text := "This request was already " + string(rec.State)
	if rec.DecidedByName != "" {
		text += " by " + slackEscape(rec.DecidedByName)
	}
	switch rec.Channel {
	case "slack":
		text += " in Slack"
	case "console":
		text += " in the console"
	case "phone":
		text += " on an enrolled phone"
	case "browser":
		text += " in an enrolled browser"
	}
	if rec.DecidedAt != nil {
		text += " at " + rec.DecidedAt.UTC().Format("2006-01-02 15:04:05") + " UTC"
	}
	return text + ", so this tap recorded nothing new."
}

// mapUser resolves a Slack user id to a Straza user via users.info (email)
// and the undeleted users with that email. Emails are not unique, so it picks
// the oldest active person, one neither disabled nor locked; else the oldest
// disabled or locked person, whom the standing refusal then names; and never
// an AI agent or a service account, because only a person decides and an
// agent row often carries its sponsor's email. A match on non-persons only is
// unmappable and logs a Warn line that names them. Returns false when the
// lookup or mapping fails.
func (sc *slackChannel) mapUser(ctx context.Context, slackUserID string) (store.User, bool) {
	endpoint := sc.apiBase + "/users.info?user=" + url.QueryEscape(slackUserID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return store.User{}, false
	}
	req.Header.Set("Authorization", "Bearer "+sc.botToken)
	resp, err := sc.http.Do(req)
	if err != nil {
		return store.User{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK   bool `json:"ok"`
		User struct {
			Profile struct {
				Email string `json:"email"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK || out.User.Profile.Email == "" {
		return store.User{}, false
	}
	us, err := sc.st.Users().ListByEmail(ctx, out.User.Profile.Email)
	if err != nil {
		return store.User{}, false
	}
	pick := -1
	var agents []string
	for i, u := range us {
		switch {
		case userIsNHI(u):
			agents = append(agents, u.Username)
		case u.Status == store.UserActive && !sc.svc.userBlocked(u.ID):
			return u, true
		case pick < 0:
			pick = i
		}
	}
	if pick >= 0 {
		return us[pick], true
	}
	if len(agents) > 0 {
		sc.log.Warn("approval slack callback: the Slack email matches only an AI agent or a service account, which never decides",
			"slackUser", slackUserID, "users", strings.Join(agents, ","))
	}
	return store.User{}, false
}

// slackStandingRefusal answers a tap whose Slack email matches a disabled or
// locked Straza user, after "Could not record decision: ", formatted with the
// username and the state. It names the matched user and asks for no enable or
// unlock, because the email can match an older account that is not the
// tapping person's own.
const slackStandingRefusal = "your Slack email matches the Straza user %[1]s, which is %[2]s, so it cannot decide a request. " +
	"Ask another person who may approve the request to decide it. " +
	"If you should still decide requests, tell your Straza administrator that your Slack email matches the %[2]s user %[1]s"

// standing answers the words and the audit reason that refuse a mapped user
// whose status is not active or who sits on the lock denylist, in the login
// lane's order and reasons, and empty strings for a user who may go on to
// Decide.
func (sc *slackChannel) standing(u store.User) (words, reason string) {
	state := ""
	switch {
	case u.Status != store.UserActive:
		state, reason = "disabled", "user is disabled"
	case sc.svc.userBlocked(u.ID):
		state, reason = "locked", "user is locked"
	default:
		return "", ""
	}
	return fmt.Sprintf(slackStandingRefusal, slackEscape(u.Username), state), reason
}

// auditRefusal writes the one straza.audit.authn login failure of a tap that
// standing refused, naming the person, the request the tap tried to decide
// and the reason. It carries no sourceIp or userAgent, because the
// connection is Slack's and not the person's client.
func (sc *slackChannel) auditRefusal(ctx context.Context, u store.User, approvalID, reason string) {
	const subject = "straza.audit.authn"
	ce, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": uuid.NewString(), "type": subject, "source": "strazad",
		"time": sc.svc.now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"action": "login", "outcome": "failure", "via": "slack",
			"user": u.Username, "userId": u.ID, "approvalId": approvalID, "reason": reason,
		},
	})
	if err != nil {
		return
	}
	if _, err := sc.st.Outbox().Insert(ctx, store.OutboxEvent{Subject: subject, CE: string(ce)}); err != nil {
		sc.log.Warn("approval slack callback: the refusal record could not be written", "user", u.ID, "approval", approvalID, "err", err)
	}
}

// verifySignature checks the timestamp is within the replay window and the v0
// HMAC matches (constant-time).
func (sc *slackChannel) verifySignature(ts, sig string, body []byte) bool {
	if ts == "" || sig == "" {
		return false
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	skew := sc.svc.now().Unix() - tsInt
	if skew < 0 {
		skew = -skew
	}
	if time.Duration(skew)*time.Second > slackReplayWindow {
		return false
	}
	mac := hmac.New(sha256.New, []byte(sc.signingSecret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// call POSTs a JSON body to a Slack Web API method with the bot token.
func (sc *slackChannel) call(ctx context.Context, method string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sc.apiBase+"/"+method, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+sc.botToken)
	resp, err := doRequest(sc.http, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func slackEphemeral(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"response_type": "ephemeral", "text": text, "replace_original": false})
}

// slackEscape neutralizes the three characters Slack mrkdwn treats specially so
// a requester name or summary cannot inject formatting/mentions.
func slackEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// slackPreviewMax hard-clamps the preview text embedded in a Slack code block so
// it stays under Block Kit's 3000-char section limit even after the 2 KiB store
// cap plus fencing and escaping.
const slackPreviewMax = 2800

// slackClampPreview prepares a stored args preview for a Slack code block: escape
// the mrkdwn specials, defuse any embedded triple-backtick that would close the
// fence early, then clamp to slackPreviewMax runes. The preview is already
// redacted and display-safe (neutralized) at storage time.
func slackClampPreview(s string) string {
	s = slackEscape(s)
	s = strings.ReplaceAll(s, "```", "` ` `")
	if r := []rune(s); len(r) > slackPreviewMax {
		s = string(r[:slackPreviewMax-1]) + "…"
	}
	return s
}

// decideErrorText renders a Decide error as short approver-facing text.
func decideErrorText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSelfApproval):
		return "you may not approve your own request"
	case errors.Is(err, ErrNotApprover):
		return "you are not an authorized approver"
	case errors.Is(err, ErrUnsignedOwnDecision):
		return "this is your own request, so Slack cannot decide it. Confirm it on your enrolled phone, or on the self-service page under This browser"
	case errors.Is(err, ErrConflict):
		return "already decided the other way"
	case errors.Is(err, ErrExpired):
		return "the request has expired"
	case errors.Is(err, ErrNotFound):
		return "no such request"
	case errors.Is(err, ErrNHIDecider):
		return "your Straza user is an AI agent or a service account, and only a person can decide a request. Ask a person who may approve it to decide it"
	case errors.Is(err, ErrStoreUnavailable):
		return "Straza is temporarily unavailable. Try again in a moment"
	default:
		return "internal error"
	}
}

// capitalize upper-cases the first byte (ASCII state strings only).
func capitalize(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}
