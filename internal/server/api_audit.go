package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// handleAuditList returns hash-chained audit records after a sequence number.
// `strazactl audit tail` and `verify` consume this.
// Records are enriched with the resolved username (envelope field, never the
// CE, since the chain hash covers CE bytes). ?q=<substring, case-insensitive
// over the raw CE text> and ?effect=allow|deny are applied in the STORE
// before the limit, so a page is a page of matches and the cursor walks
// matches, which a post-fetch filter could not do past the fetched window.
// ?user=<id or username> lands in the
// store the same way, so a quiet user's records stay in the newest window.
// ?type=<CE type prefix> (e.g. straza.audit.sentinel, the console's
// sentinel-verdict surface) and ?session=<id> keep their original
// within-the-page semantics. This is the admin plane, where store reads are
// allowed.
func (a *App) handleAuditList(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	userFilter := r.URL.Query().Get("user")
	typeFilter := r.URL.Query().Get("type")
	sessionFilter := r.URL.Query().Get("session")
	effect := r.URL.Query().Get("effect")
	switch effect {
	case "", "allow", "deny":
	default:
		apiError(w, http.StatusBadRequest, "effect must be allow or deny")
		return
	}
	filter := store.AuditFilter{Q: strings.TrimSpace(r.URL.Query().Get("q")), Effect: effect}
	if userFilter != "" {
		// The value is an id or a username. Both spellings go to the store,
		// the typed one as is and the other resolved when the user exists,
		// so decision rows keyed by id and admin rows keyed by actor name
		// both match.
		filter.UserID, filter.Username = userFilter, userFilter
		if u, err := a.store.Users().GetByUsername(r.Context(), userFilter); err == nil {
			filter.UserID = u.ID
		} else if u, err := a.store.Users().GetByID(r.Context(), userFilter); err == nil {
			filter.Username = u.Username
		}
	}
	var records []store.AuditRecord
	var err error
	// order=desc serves the newest page (`audit tail`); it is a window,
	// not a cursor walk, so combining it with an explicit after= is
	// ambiguous and refused rather than silently reinterpreted.
	switch r.URL.Query().Get("order") {
	case "", "asc":
		records, err = a.store.Audit().ListFiltered(r.Context(), filter, after, limit)
	case "desc":
		if r.URL.Query().Get("after") != "" {
			apiError(w, http.StatusBadRequest, "order=desc does not combine with after")
			return
		}
		records, err = a.store.Audit().ListRecentFiltered(r.Context(), filter, limit)
	default:
		apiError(w, http.StatusBadRequest, "order must be asc or desc")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list audit failed", err)
		return
	}
	usernames := map[string]string{} // user id -> username, memoized per request
	resolve := func(id string) string {
		if id == "" {
			return ""
		}
		if name, ok := usernames[id]; ok {
			return name
		}
		name := ""
		if u, err := a.store.Users().GetByID(r.Context(), id); err == nil {
			name = u.Username
		}
		usernames[id] = name
		return name
	}
	out := make([]auditListRow, 0, len(records))
	for _, rec := range records {
		m := ceMeta(rec.CE)
		// Authn records carry the id under data.userId and the janitor's
		// session ends carry nothing else, while decision records carry the
		// id under data.user, so both are tried in that order.
		username := resolve(m.userID)
		if username == "" {
			username = resolve(m.user)
		}
		// Admin events carry no data.user, so the Who column falls back to
		// the attributed actor (spec/events rev 18), so console-driven
		// mutations read "kim", not blank.
		if username == "" {
			username = m.actor
		}
		if typeFilter != "" && !strings.HasPrefix(m.ceType, typeFilter) {
			continue
		}
		if sessionFilter != "" && sessionFilter != m.session {
			continue
		}
		out = append(out, auditListRow{
			Record: audit.Record{
				Seq: rec.Seq, CE: rec.CE, PrevHash: rec.PrevHash, Hash: rec.Hash,
				Username: username,
			},
			DecidedByUsername: resolve(m.decidedBy),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// auditListRow is the /v1/admin/audit response item: the chain-record
// envelope plus read-time name enrichments. decidedByUsername resolves an
// approval CE's data.decidedBy (a user ID) so the console ledger can say
// "approved by alice". Enrichment lives on the envelope, never in
// the CE: the chain hash covers CE bytes (verified by TestAuditListResolvesDecidedBy).
type auditListRow struct {
	audit.Record
	DecidedByUsername string `json:"decidedByUsername,omitempty"`
}

// ceFields are the parts of a CloudEvent the list enrichment reads: the
// type, data.user (a user id on decision records, a username on authn
// records), data.userId (the id authn records carry, spec/events rev 21),
// data.session, the admin actor (rev 18) and data.decidedBy (approval
// resolutions).
type ceFields struct {
	ceType, user, userID, session, actor, decidedBy string
}

// ceMeta extracts ceFields from a CloudEvent without trusting its shape; an
// unparseable event yields zero fields.
func ceMeta(ce string) ceFields {
	var m struct {
		Type string `json:"type"`
		Data struct {
			User      string `json:"user"`
			UserID    string `json:"userId"`
			Session   string `json:"session"`
			Actor     string `json:"actor"`
			DecidedBy string `json:"decidedBy"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(ce), &m) != nil {
		return ceFields{}
	}
	return ceFields{ceType: m.Type, user: m.Data.User, userID: m.Data.UserID, session: m.Data.Session,
		actor: m.Data.Actor, decidedBy: m.Data.DecidedBy}
}

// maxAuditBatch caps a single straza spool upload.
const maxAuditBatch = 4 << 20

// batchRequest is the straza spool-drain body: a list of already-
// formed CloudEvents to ingest through the same outbox → chain pipeline.
type batchRequest struct {
	Events []json.RawMessage `json:"events"`
}

// handleAuditBatch ingests a batch of client-spooled audit events into the
// outbox (so they flow through the relay → hash chain like server events).
// Auth: any valid session token off the denylist; a client may only submit
// its own audit. A record keeps the session it names only when that session
// is the uploader's user and device, and any other record is refused and
// counted in the answer's `refused`.
func (a *App) handleAuditBatch(w http.ResponseWriter, r *http.Request) {
	raw := bearerToken(r)
	claims, err := a.tokens.Verify(raw)
	if err != nil || claims.Session == "" {
		apiError(w, http.StatusUnauthorized, "session token rejected")
		return
	}
	if a.denylist.blocked(claims) {
		// A revoked principal writes nothing to the chain. The client keeps
		// the batch parked in its spool, as it does for every final refusal.
		a.log.Warn("revoked session refused on the audit batch route", "user", claims.Subject, "session", claims.Session)
		apiError(w, http.StatusForbidden, a.denylist.revokedMsg(claims,
			"Straza: this session has been revoked, so its audit events are refused and stay in the spool. Start a new session: its check-in uses this device's existing enrollment, and the spool uploads under it",
			"Straza: this device or user has been revoked, so its audit events are refused. Contact your administrator"))
		return
	}
	// Ingest backpressure. A 429 loses nothing: the client spool
	// keeps its records and retries on its own cadence; the server just
	// stops absorbing faster than the relay drains.
	if !a.ingestLimiter.Allow("audit:"+claims.Session, a.cfg.Governance.AuditIngestPerSessionRPS) {
		w.Header().Set("Retry-After", "30")
		apiError(w, http.StatusTooManyRequests, "audit ingest rate limited; the spool retries")
		return
	}
	if a.auditBacklogOver(r.Context()) {
		w.Header().Set("Retry-After", "30")
		apiError(w, http.StatusTooManyRequests, "audit backlog at capacity; the spool retries")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuditBatch))
	if err != nil {
		apiError(w, http.StatusBadRequest, "could not read body")
		return
	}
	var req batchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed batch")
		return
	}
	events := make([]store.OutboxEvent, 0, len(req.Events))
	// A spooled record names the session it was decided under, an earlier
	// one when the client uploads after a new check-in. A refused record is
	// counted and the batch still answers 200, because the spool deletes its
	// file on any 2xx and a 4xx would resend the batch forever. verdicts holds
	// one verdict per named session, so a batch reads each session row once,
	// and at most maxNamedSessions rows in all. A refused session collects
	// the records that name it, and capped collects those past the cap.
	verdicts := map[string]*namedVerdict{claims.Session: {}}
	var refused []*namedVerdict
	capped := &namedVerdict{}
	for _, ev := range req.Events {
		m := decodeClientCE(ev)
		session := claims.Session
		if named := namedSession(m); named != "" {
			id, _ := m["id"].(string)
			v, known := verdicts[named]
			if !known && len(verdicts) > maxNamedSessions {
				capped.add(named, id)
				continue
			}
			if !known {
				why, rerr := a.spooledSessionRefusal(r.Context(), named, claims)
				if rerr != nil {
					w.Header().Set("Retry-After", "30")
					a.fail(w, r, http.StatusServiceUnavailable, "audit store unavailable; the spool retries", fmt.Errorf("audit batch session read: %w", rerr))
					return
				}
				v = &namedVerdict{why: why}
				verdicts[named] = v
				if why != nil {
					refused = append(refused, v)
				}
			}
			if v.why != nil {
				v.add(named, id)
				continue
			}
			session = named
		}
		subject, ce := normalizeClientCE(m, session, claims.Subject)
		events = append(events, store.OutboxEvent{Subject: subject, CE: ce})
	}
	// ONE transaction for the whole batch, because per-event autocommit
	// inserts drag checkin p99 into seconds at ingest saturation.
	// All-or-nothing on purpose: the client spool deletes its records on any
	// 2xx without reading `accepted`, so a per-event skip could silently lose
	// events under a store fault; a loud 503 keeps them spooled and retrying
	// instead.
	accepted, err := a.store.Outbox().InsertBatch(r.Context(), events)
	if err != nil {
		w.Header().Set("Retry-After", "30")
		a.fail(w, r, http.StatusServiceUnavailable, "audit store unavailable; the spool retries", fmt.Errorf("audit batch insert (events=%d): %w", len(events), err))
		return
	}
	a.relay.Nudge()
	// Refusals are logged and counted only now that the batch is stored, so
	// a batch answered 503 and retried reports each refusal once.
	writeJSON(w, http.StatusOK, map[string]int{"accepted": accepted, "refused": a.reportSpoolRefusals(claims, refused, capped)})
}

// maxNamedSessions caps the distinct sessions other than the uploader's that
// one batch may name. It mirrors drainChunkRecords in internal/agentguard/spool,
// the most records a straza client sends in one upload, so no real client
// reaches it.
const maxNamedSessions = 1000

// spoolRefusal is why the batch route refused the spooled records that name
// one session and what an operator can do next, two parts of their Warn line.
type spoolRefusal struct{ why, next string }

// The refusals a named session can earn, in the order spooledSessionRefusal
// judges them. Each why completes the sentence "that session ...".
var (
	refusedMalformed = &spoolRefusal{
		"is not a session id, which this server always issues as a UUID",
		"A straza client writes only the session ids this server gave it, so check this client's version and whether its spool files were edited."}
	refusedUnknown = &spoolRefusal{
		"is not known to this server",
		"Check whether this client was pointed at another Straza server, or whether this server's database was restored from a backup older than that session."}
	refusedOtherUser = &spoolRefusal{
		"belongs to another user",
		"Read the uploader's session in the audit chain, and check whether several users share one STRAZA_HOME, which is the usual cause."}
	refusedNoDevice = &spoolRefusal{
		"was opened without a device, such as a console session, and this upload comes from an enrolled device",
		"Read the uploader's session in the audit chain, and check whether this STRAZA_HOME is shared with a client of the same user that signs in without a device."}
	refusedOtherDevice = &spoolRefusal{
		"was opened on another device of the same user",
		"Read the uploader's session in the audit chain, and check whether this STRAZA_HOME was copied from another machine, which is the usual cause."}
)

// namedVerdict is the verdict on one session a batch names: why it is
// refused (nil keeps its records), and the records it refused, counted, with
// the first record's id. The group past maxNamedSessions is one too.
type namedVerdict struct {
	why          *spoolRefusal
	named, first string
	count        int
}

// add counts one refused record, keeping the first one's session and id.
func (v *namedVerdict) add(named, id string) {
	if v.count == 0 {
		v.named, v.first = named, id
	}
	v.count++
}

// spooledSessionRefusal judges the session a spooled record names and
// returns why the uploader may not file the record under it, nil when it
// may: the session's user and device must be the token's, and a headless
// agent has no device on either side. A value that is not a UUID is refused
// without a read. A store fault is an error, never a refusal, since a
// refusal answers 200 and the spool deletes what a 200 answers.
func (a *App) spooledSessionRefusal(ctx context.Context, id string, c authn.Claims) (*spoolRefusal, error) {
	if len(id) != 36 || uuid.Validate(id) != nil {
		return refusedMalformed, nil
	}
	ses, err := a.store.Sessions().GetByID(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refusedUnknown, nil
	case err != nil:
		return nil, err
	case ses.UserID != c.Subject:
		return refusedOtherUser, nil
	case ses.DeviceID == c.Device:
		return nil, nil
	case ses.DeviceID == "":
		return refusedNoDevice, nil
	}
	return refusedOtherDevice, nil
}

// reportSpoolRefusals writes one Warn line per refused named session and one
// for the records past maxNamedSessions, each with the count of records it
// covers, counts every refused record in straza_audit_refused_total, and
// returns that count. The first record's id and the named session are
// client-chosen, so each is cut to 64 bytes.
func (a *App) reportSpoolRefusals(c authn.Claims, refused []*namedVerdict, capped *namedVerdict) int {
	total := capped.count
	for _, v := range refused {
		total += v.count
		a.log.Warn("audit batch: spooled records that name one session were refused because that session "+v.why.why+
			". The client has already deleted them, because the upload was answered 200, so this line is their only trace, and count says how many it covers. "+v.why.next,
			"count", v.count, "record", logCut(v.first), "session", c.Session, "user", c.Subject, "named_session", logCut(v.named))
	}
	if capped.count > 0 {
		a.log.Warn(fmt.Sprintf("audit batch: %d spooled records were refused because their batch names more than %d sessions other than the uploader's, "+
			"and a straza client never sends more than %d records in one upload. The client has already deleted them, because the upload was answered 200, "+
			"so this line is their only trace. Read the uploader's session in the audit chain, and find out what sent this upload, since a straza client cannot.",
			capped.count, maxNamedSessions, maxNamedSessions),
			"count", capped.count, "record", logCut(capped.first), "session", c.Session, "user", c.Subject, "named_session", logCut(capped.named))
	}
	a.metrics.AuditRefused(total)
	return total
}

// logCut keeps at most 64 bytes of a client-chosen value for a log line and
// marks a cut with "...".
func logCut(s string) string {
	if len(s) <= 64 {
		return s
	}
	return s[:64] + "..."
}

// auditBacklogOver reports whether the outbox backlog exceeds the ingest
// gate, cached for 2 s so 100k clients cost one bounded count every 2 s, not
// one per request. A failed count fails OPEN: refusing all ingest on a
// transient read error only delays spools that would retry anyway, while the
// records stay safe client-side either way.
func (a *App) auditBacklogOver(ctx context.Context) bool {
	limit := a.cfg.Governance.AuditIngestBacklogLimit
	if limit <= 0 {
		return false
	}
	g := &a.backlogGate
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Since(g.at) < 2*time.Second {
		return g.over
	}
	n, err := a.store.Outbox().CountUnpublished(ctx, limit)
	if err != nil {
		a.log.Warn("audit ingest: backlog count", "err", err)
		return false
	}
	g.at, g.over = time.Now(), n >= limit
	return g.over
}

// clientCETypes is the allowlist of CE types a CLIENT may submit through
// /v1/audit/batch: tool decisions plus capture turns (spec/events rev 5).
// Anything else is coerced to straza.audit.tool: a client must never mint
// server-side subjects (admin/authn/identity) into the chain. That includes
// straza.audit.sentinel: verdicts are the server-side sentinel's output (the
// judge), so a client-submitted "verdict" is a forgery attempt, not audit.
var clientCETypes = map[string]bool{
	"straza.audit.tool":   true,
	"straza.audit.prompt": true,
	"straza.audit.reply":  true,
}

// decodeClientCE parses one client audit event. An unparseable event decodes
// to an empty map, which normalizeClientCE still stamps and binds.
func decodeClientCE(ev json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(ev, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// namedSession returns the session id a decoded client event names in
// data.session, "" when it names none or the value is not a string.
func namedSession(m map[string]any) string {
	data, _ := m["data"].(map[string]any)
	s, _ := data["session"].(string)
	return s
}

// normalizeClientCE stamps a decoded client audit event with a server-side id
// (if absent) and binds it to the given session and the authenticated user,
// so a client cannot forge another principal's audit trail. The caller picks
// the session: the uploader's, or the earlier session the record names when
// spooledSessionRefusal accepts it. Returns the outbox subject (== the
// normalized CE type) and the CE JSON.
func normalizeClientCE(m map[string]any, session, user string) (string, string) {
	if _, ok := m["id"].(string); !ok {
		m["id"] = uuid.NewString()
	}
	ceType, _ := m["type"].(string)
	if !clientCETypes[ceType] {
		ceType = "straza.audit.tool"
	}
	m["specversion"] = "1.0"
	m["type"] = ceType
	m["source"] = "straza"
	data, _ := m["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	data["session"] = session
	data["user"] = user
	m["data"] = data
	out, _ := json.Marshal(m)
	return ceType, string(out)
}
