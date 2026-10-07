package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// subjectCache holds the resolved policy.Subject per active session, built at
// checkin (control plane, DB reads allowed) so /v1/decide never touches the
// DB. A pod without the entry answers the check-in-again 401, and that
// check-in fills it.
type subjectCache struct {
	mu sync.RWMutex
	m  map[string]cachedSubject // session id → subject
	// now is the clock that roles resolve on at check-in and that ends a
	// cached subject at its edge. Tests replace it before the server runs.
	now func() time.Time
}

// cachedSubject is one session's subject, what check-in knew of the
// session beside it, and the time its roles stop being true: the next
// window edge of the user's assignments, zero when none.
type cachedSubject struct {
	sub   policy.Subject
	facts sessionFacts
	until time.Time
}

// sessionFacts is what check-in records beside a session's subject, which
// policy.Subject does not carry: whether the user is a person by userKind,
// and the name of the harness the session checked in with. The drafting
// tools read both (native_drafts.go). The zero value reads as not a person,
// the stricter reading.
type sessionFacts struct {
	person  bool
	harness string
}

func newSubjectCache() *subjectCache {
	return &subjectCache{m: map[string]cachedSubject{}, now: time.Now}
}

// put caches s for the session until the given edge, with no session
// facts, and returns the subject it replaces, even one past its edge, so a
// check-in compares its new subject with the one the session last held.
func (c *subjectCache) put(sessionID string, s policy.Subject, until time.Time) (policy.Subject, bool) {
	c.mu.Lock()
	prev, ok := c.m[sessionID]
	c.m[sessionID] = cachedSubject{sub: s, until: until}
	c.mu.Unlock()
	return prev.sub, ok
}

// putIf caches s with facts for the session as put does, but only while ok
// holds, judged under the cache's lock, and answers the subject it
// replaced, whether there was one, and whether it cached s. An apply drops
// sessions under the same lock after it bumped the resolution epoch, so a
// check-in whose ok compares the epoch it resolved in never caches roles
// that an apply dropped while they were read.
func (c *subjectCache) putIf(sessionID string, s policy.Subject, facts sessionFacts, until time.Time, ok func() bool) (policy.Subject, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !ok() {
		return policy.Subject{}, false, false
	}
	prev, had := c.m[sessionID]
	c.m[sessionID] = cachedSubject{sub: s, facts: facts, until: until}
	return prev.sub, had, true
}

// get returns the session's cached subject while its roles still hold. A
// subject at or past its edge reads as missing, so the request path answers
// its check-in-again 401 and that check-in resolves the roles again.
func (c *subjectCache) get(sessionID string) (policy.Subject, bool) {
	e, ok := c.lookup(sessionID)
	return e.sub, ok
}

// lookup returns the session's whole cache entry, its subject and its
// session facts, under the edge rule of get.
func (c *subjectCache) lookup(sessionID string) (cachedSubject, bool) {
	c.mu.RLock()
	e, ok := c.m[sessionID]
	c.mu.RUnlock()
	if !ok || (!e.until.IsZero() && !c.now().Before(e.until)) {
		return cachedSubject{}, false
	}
	return e, true
}

func (c *subjectCache) drop(sessionID string) {
	c.mu.Lock()
	delete(c.m, sessionID)
	c.mu.Unlock()
}

// dropHolding drops every cached subject whose roles hold one of roles and
// answers how many. A subject's roles are its resolved closure, so a
// subject that holds a role through another role is dropped too, and its
// next call checks in again and resolves its roles anew.
func (c *subjectCache) dropHolding(roles map[string]bool) int {
	if len(roles) == 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for id, e := range c.m {
		for _, r := range e.sub.Roles {
			if roles[r] {
				delete(c.m, id)
				n++
				break
			}
		}
	}
	return n
}

// dropAll drops every cached subject and answers how many, for a replica
// that has applied no config yet and so cannot tell which roles changed.
func (c *subjectCache) dropAll() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.m)
	c.m = map[string]cachedSubject{}
	return n
}

// denylist is the in-memory revocation set checked on every token verify
// (revocation travels as events to in-memory denylists, never as per-call
// DB lookups). Admin revoke and disable feed it directly on this pod, and
// the NATS revocation consumer converges every other pod.
type denylist struct {
	mu       sync.RWMutex
	sessions map[string]bool
	users    map[string]bool
	devices  map[string]bool
	jtis     map[string]bool
	log      *slog.Logger
	// onUser and onSession, when set, hear every user and session revocation
	// after the set holds it, on every pod, so that state kept elsewhere for
	// the principal goes with it. Both are set once, before the server runs.
	onUser, onSession func(id string)
}

// newDenylist builds an empty set; log (nil = silent) carries the one Debug
// "denylist updated" record every mutation writes. Debug, not
// Info: revocations are per-request volume and a mass revocation at fleet
// scale must not flood the operator log.
func newDenylist(log *slog.Logger) *denylist {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &denylist{sessions: map[string]bool{}, users: map[string]bool{}, devices: map[string]bool{}, jtis: map[string]bool{}, log: log}
}

func (d *denylist) revokeSession(id string) {
	d.mu.Lock()
	d.sessions[id] = true
	d.mu.Unlock()
	d.logUpdate("revoke", "session", id)
	if d.onSession != nil {
		d.onSession(id)
	}
}

func (d *denylist) revokeUser(id string) {
	d.mu.Lock()
	d.users[id] = true
	d.mu.Unlock()
	d.logUpdate("revoke", "user", id)
	if d.onUser != nil {
		d.onUser(id)
	}
}

// RevokeUser/RevokeSession/RevokeDevice/AllowUser satisfy spine.DenylistSink
// so the event-spine revocation consumer converges every pod on the same set,
// including reactivation lifts.
func (d *denylist) RevokeUser(id string)    { d.revokeUser(id) }
func (d *denylist) RevokeSession(id string) { d.revokeSession(id) }
func (d *denylist) RevokeDevice(id string) {
	d.mu.Lock()
	d.devices[id] = true
	d.mu.Unlock()
	d.logUpdate("revoke", "device", id)
}
func (d *denylist) AllowUser(id string) { d.allowUser(id) }

// allowUser lifts a user-level entry (SCIM re-activation). Session-level
// entries are never lifted; a revoked session stays dead.
func (d *denylist) allowUser(id string) {
	d.mu.Lock()
	delete(d.users, id)
	d.mu.Unlock()
	d.logUpdate("allow", "user", id)
}

// logUpdate writes the one Debug record per mutation, after the lock is
// released so logging never sits inside the hot read path's lock.
func (d *denylist) logUpdate(op, scope, id string) {
	d.log.Debug("denylist updated", "component", "denylist", "op", op, "scope", scope, "id", id)
}

// size reports the total entry count across scopes (console overview).
func (d *denylist) size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.sessions) + len(d.users) + len(d.devices) + len(d.jtis)
}

func (d *denylist) blocked(c authn.Claims) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sessions[c.Session] || d.users[c.Subject] || d.devices[c.Device] || d.jtis[c.JTI]
}

// revokedMsg picks the refusal for claims that blocked reports, in the
// order the refresh lane checks them. A user or device entry judges the
// identity, so a new session is refused too and identity is returned. A
// session or token entry ends only this session, and a check-in from the
// same device credential opens a new one, so session is returned.
func (d *denylist) revokedMsg(c authn.Claims, session, identity string) string {
	if d.userBlocked(c.Subject) || d.deviceBlocked(c.Device) {
		return identity
	}
	return session
}

// userBlocked/deviceBlocked are the scoped checks for principals that carry
// no session yet (the device-token checkin path).
func (d *denylist) userBlocked(id string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.users[id]
}

func (d *denylist) deviceBlocked(id string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.devices[id]
}

// auditSpool is the async audit path: a bounded ring drained by a
// background goroutine into the outbox. A decision never blocks on audit
// I/O. Standalone drops-with-counter when full; enterprise blocks.
// The goroutine writes what the ring holds in one transaction, up to
// auditBatchMax records.
// A record the loop cannot confirm as written is counted in lost and leaves
// one trace at Error: its own line, or its place in the stop's count.
type auditSpool struct {
	ch      chan store.OutboxEvent
	dropped atomic.Uint64
	lost    atomic.Uint64
	block   bool
	insert  func(context.Context, store.OutboxEvent) error
	// insertBatch stores several records in one transaction, all of them
	// or none. While it is nil, every record is written alone.
	insertBatch func(context.Context, []store.OutboxEvent) error
	log         *slog.Logger
	// waits are the pauses before each retry of a failed write,
	// drainBound is how long run keeps writing once its context ends, and
	// submitBound is the longest submit waits for room under block. Tests
	// shorten them.
	waits       []time.Duration
	drainBound  time.Duration
	submitBound time.Duration
	// cutAtStop counts the records lost at the stop without a line of their
	// own, which the stop's Error line reports. Only run's goroutine uses it.
	cutAtStop int
}

// auditInsertTimeout bounds one attempt to write a record, or a batch of
// records, to the outbox.
const auditInsertTimeout = 5 * time.Second

// auditBatchMax is the most records the spool writes in one transaction. It
// is one statement of the store's batch insert.
const auditBatchMax = 200

// auditSubmitBound is the longest a server decision waits for room in the
// audit queue under block. It is shorter than the client's 30 second
// request timeout, so the refusal still reaches the client, and it bounds
// the handlers that wait while the database is down.
const auditSubmitBound = 25 * time.Second

// errAuditQueueFull is the cause of a record that submit could not queue.
var errAuditQueueFull = errors.New("the audit queue stayed full because the database does not take its records")

func newAuditSpool(block bool, log *slog.Logger, insert func(context.Context, store.OutboxEvent) error) *auditSpool {
	return &auditSpool{ch: make(chan store.OutboxEvent, 4096), block: block, insert: insert, log: log,
		waits:      []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond},
		drainBound: 5 * time.Second, submitBound: auditSubmitBound}
}

// submit puts e in the queue. When the queue is full, drop counts and
// drops e and returns nil, and block waits for room until ctx ends or
// submitBound passes, and then returns errAuditQueueFull with the reason.
// A decision whose record gets an error must not run.
func (a *auditSpool) submit(ctx context.Context, e store.OutboxEvent) error {
	select {
	case a.ch <- e:
		return nil
	default:
	}
	if !a.block {
		a.dropped.Add(1)
		return nil
	}
	t := time.NewTimer(a.submitBound)
	defer t.Stop()
	select {
	case a.ch <- e:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: the request ended while it waited for room", errAuditQueueFull)
	case <-t.C:
		return fmt.Errorf("%w: no room within %s", errAuditQueueFull, a.submitBound)
	}
}

// run writes the ring to the outbox until ctx ends, then drains what the
// ring still holds and returns. The caller ends ctx only after the last
// submitter has stopped, and closes the store only after run returns. No
// write outlives drainBound after ctx ends, one begun before it included,
// except on SQLite: a write waiting for a lock that another process holds
// waits out the store's 5 second busy timeout, because SQLite's busy wait
// does not see the interrupt that a cancelled context sends.
func (a *auditSpool) run(ctx context.Context) {
	stopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer context.AfterFunc(ctx, func() { time.AfterFunc(a.drainBound, cancel) })()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case e := <-a.ch:
			a.writeQueued(stopCtx, e)
		}
	}
	a.drain(stopCtx)
}

// drain writes what the ring holds until it is empty or stopCtx ends, and
// logs at Error how many records the stop lost without a line of their own.
// Once stopCtx has ended it counts the records the ring still holds as lost
// and returns without receiving: a receive would free a slot for a decision
// that waits in submit under block, and that decision would then run
// without its record. It stays blocked until the process exits instead.
func (a *auditSpool) drain(stopCtx context.Context) {
	for empty := false; !empty && stopCtx.Err() == nil; {
		select {
		case e := <-a.ch:
			a.writeQueued(stopCtx, e)
		default:
			empty = true
		}
	}
	if stopCtx.Err() != nil {
		n := len(a.ch)
		a.lost.Add(uint64(n))
		a.cutAtStop += n
	}
	if a.cutAtStop > 0 {
		a.log.Error("audit records lost at shutdown: strazad stopped before it could write them to the database. "+
			"Check that the database was reachable while strazad stopped", "count", a.cutAtStop)
	}
}

// writeQueued writes e together with the records the ring already holds
// behind it, up to auditBatchMax, in one transaction. It never waits for
// another record, so a record that is alone is written alone by write. It
// stops taking records once ctx has ended, as drain does. A batch the
// database did not confirm is not tried again as a batch: each record goes
// through writeKeyed under the id the batch gave it. A batch that was
// stored after all then meets its keys and counts as written, and a row the
// database refuses loses only itself.
func (a *auditSpool) writeQueued(ctx context.Context, e store.OutboxEvent) {
	batch := []store.OutboxEvent{e}
	for more := a.insertBatch != nil; more && len(batch) < auditBatchMax && ctx.Err() == nil; {
		select {
		case next := <-a.ch:
			batch = append(batch, next)
		default:
			more = false
		}
	}
	if len(batch) == 1 {
		a.write(ctx, e)
		return
	}
	for i := range batch {
		batch[i].ID = uuid.Must(uuid.NewV7()).String()
	}
	// Once ctx has ended no attempt is made, as in writeKeyed, so the
	// records go to the stop's count and not to lines that say they may be
	// stored.
	unsure := false
	if ctx.Err() == nil {
		ic, cancel := context.WithTimeout(ctx, auditInsertTimeout)
		err := a.insertBatch(ic, batch)
		cancel()
		if err == nil {
			return
		}
		unsure = store.MaybeWritten(err)
	}
	for _, e := range batch {
		a.writeKeyed(ctx, e, unsure)
	}
}

// write gives e its outbox id and writes it as writeKeyed does.
func (a *auditSpool) write(ctx context.Context, e store.OutboxEvent) bool {
	e.ID = uuid.Must(uuid.NewV7()).String()
	return a.writeKeyed(ctx, e, false)
}

// writeKeyed inserts e under the id it carries, retries a failed attempt
// after each of a.waits, and reports whether the record is in the outbox.
// The id stays the same across attempts, so when an attempt commits and
// still fails, as a timeout after the commit does, the retry meets the key
// and counts as written instead of adding the record twice. unsure says
// that an earlier attempt, a batch, may have stored it. Under block, a
// failure that is not the refusal of the row keeps the record trying at
// the last wait's pace until it is written or ctx ends, with one Error line
// when the queue starts to wait. A record not written is counted in lost.
// It gets its own Error line by type and id, never its data, when its
// retries are used up or an attempt may have stored it; a record the stop
// cut off otherwise goes to the stop's count.
func (a *auditSpool) writeKeyed(ctx context.Context, e store.OutboxEvent, unsure bool) bool {
	var head struct{ ID, Type string }
	_ = json.Unmarshal([]byte(e.CE), &head)
	if head.Type == "" {
		head.Type = e.Subject
	}
	var err error
	attempt := 0
	for ctx.Err() == nil && (attempt <= len(a.waits) || a.holds(err)) {
		if attempt > 0 {
			if attempt == len(a.waits)+1 {
				a.log.Error("audit queue waiting for the database: strazad cannot write the oldest record in its audit queue "+
					"and keeps trying it, because governance.auditBackpressure is block. "+
					"New server decisions wait once the queue of 4,096 records is full. The err attribute says why the write failed. "+
					"Make the database reachable and writable, and the queue is written when it answers",
					"type", head.Type, "id", head.ID, "err", err)
			}
			select {
			case <-time.After(a.waits[min(attempt, len(a.waits))-1]):
			case <-ctx.Done():
				continue
			}
		}
		attempt++
		ic, cancel := context.WithTimeout(ctx, auditInsertTimeout)
		err = a.insert(ic, e)
		cancel()
		if err == nil || errors.Is(err, store.ErrConflict) {
			if attempt > len(a.waits)+1 {
				a.log.Info("audit queue moving again: the database took the record the queue was waiting on",
					"type", head.Type, "id", head.ID, "attempts", attempt)
			}
			return true
		}
		unsure = unsure || store.MaybeWritten(err)
	}
	a.lost.Add(1)
	switch {
	case unsure:
		a.log.Error("audit record not confirmed: an attempt to write it ended without an answer from the database, so it may be stored. "+
			"Search the audit chain for this id: if the record is there, nothing was lost",
			"type", head.Type, "id", head.ID, "attempts", attempt, "err", err)
	case ctx.Err() != nil:
		a.cutAtStop++
	case store.RowRefused(err):
		a.log.Error("audit record lost: the database refused the record itself, so a retry cannot store it. "+
			"Report this line and its err attribute to the Straza project, because strazad should never build a record its database refuses",
			"type", head.Type, "id", head.ID, "attempts", attempt, "err", err)
	default:
		a.log.Error("audit record lost: every attempt to write it to the database failed. "+
			"Check that the database is reachable and has free space, and watch straza_audit_lost_total",
			"type", head.Type, "id", head.ID, "attempts", attempt, "err", err)
	}
	return false
}

// holds reports whether write keeps trying a record past its bounded
// retries after err. Under block every failure holds the record except the
// database's refusal of the row itself: an outage, and an error the store
// does not recognise, keep the queue waiting, so a new decision waits for
// room instead of running without its record. Under drop nothing holds.
func (a *auditSpool) holds(err error) bool {
	return a.block && !store.RowRefused(err)
}
