package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

func ceIDOf(data []byte) string {
	var env struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &env)
	return env.ID
}

// waitForAudit polls the audit_log until it has at least n records or times
// out. Returns the records.
func waitForAudit(t *testing.T, app *App, n int) []store.AuditRecord {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		recs, err := app.store.Audit().List(context.Background(), 0, 1000)
		if err == nil && len(recs) >= n {
			return recs
		}
		time.Sleep(30 * time.Millisecond)
	}
	recs, _ := app.store.Audit().List(context.Background(), 0, 1000)
	t.Fatalf("audit_log reached %d records, want ≥%d", len(recs), n)
	return nil
}

// TestAuditChainEndToEnd pins the chain end to end: decisions land in the
// outbox, the
// relay publishes them to JetStream, the consumer builds a hash chain, and
// `strazactl audit verify` (via the same code path) passes; then a flipped
// bit is detected.
func TestAuditChainEndToEnd(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	// Generate several decisions → audit events.
	token, _ := checkinToken(t, app, base)
	for i := 0; i < 5; i++ {
		decide(t, base, token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "echo hi"})
	}
	_ = adminTok

	records := waitForAudit(t, app, 5)

	// The chain verifies via the shared audit.Verify (what the CLI uses).
	recs := make([]audit.Record, len(records))
	for i, r := range records {
		recs[i] = audit.Record{Seq: r.Seq, CE: r.CE, PrevHash: r.PrevHash, Hash: r.Hash}
	}
	if ok, broken := audit.Verify(recs, audit.Genesis); !ok {
		t.Fatalf("fresh chain failed verify at seq %d", broken)
	}

	// Every link is well-formed (prev links to prior hash).
	for i := 1; i < len(recs); i++ {
		if recs[i].PrevHash != recs[i-1].Hash {
			t.Fatalf("chain break at %d: prev %s != %s", recs[i].Seq, recs[i].PrevHash, recs[i-1].Hash)
		}
	}

	// Tamper: flip a byte in one record's CE and re-verify → detected.
	tampered := make([]audit.Record, len(recs))
	copy(tampered, recs)
	victim := len(tampered) / 2
	tampered[victim].CE = tampered[victim].CE + " "
	if ok, broken := audit.Verify(tampered, audit.Genesis); ok {
		t.Fatal("tampered chain passed verify")
	} else if broken != tampered[victim].Seq {
		t.Errorf("break reported at %d, want %d", broken, tampered[victim].Seq)
	}
}

// TestAuditAtLeastOnceDedup pins crash safety: the same CloudEvent
// delivered twice (relay re-publish after a crash between publish and mark)
// produces exactly one chain entry.
func TestAuditAtLeastOnceDedup(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	token, _ := checkinToken(t, app, base)

	decide(t, base, token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "echo one"})
	first := waitForAudit(t, app, 1)
	ceID := ceIDOf([]byte(first[0].CE))
	if ceID == "" {
		t.Fatal("audit CE missing id")
	}

	// Re-publish the same CE directly (simulating relay redelivery).
	if err := app.bus.Publish(context.Background(), "straza.audit.tool", []byte(first[0].CE)); err != nil {
		t.Fatal(err)
	}
	// Give the consumer time to (not) double-append.
	time.Sleep(500 * time.Millisecond)
	recs, _ := app.store.Audit().List(context.Background(), 0, 1000)
	seen := 0
	for _, r := range recs {
		if ceIDOf([]byte(r.CE)) == ceID {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("CloudEvent %s appears %d times in the chain, want 1 (dedup)", ceID, seen)
	}
}

// TestAuditBatchIngest pins that straza's spool drain endpoint
// ingests client audit events into the same chain, bound to the caller's
// session (a client cannot forge another principal).
func TestAuditBatchIngest(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	token, sessionID := checkinToken(t, app, base)

	batch := map[string]any{
		"events": []map[string]any{
			{"data": map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": "spooled-1", "user": "SPOOFED"}},
			{"data": map[string]any{"event": "tool.pre", "tool": "file.write", "paths": []string{"/x"}}},
		},
	}
	code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch)
	if code != http.StatusOK || resp["accepted"].(float64) != 2 {
		t.Fatalf("batch = %d %v", code, resp)
	}

	records := waitForAudit(t, app, 2)
	// The server rebinds session/user; the spoofed "user" is overwritten.
	foundSpooled := false
	for _, r := range records {
		var ce map[string]any
		_ = json.Unmarshal([]byte(r.CE), &ce)
		data, _ := ce["data"].(map[string]any)
		if data == nil {
			continue
		}
		if data["command"] == "spooled-1" {
			foundSpooled = true
			if data["session"] != sessionID {
				t.Errorf("batch event not bound to session: %v", data["session"])
			}
			if data["user"] == "SPOOFED" {
				t.Error("client-supplied user was not overwritten (forgery risk)")
			}
		}
	}
	if !foundSpooled {
		t.Error("spooled event did not reach the chain")
	}
}

// TestAuditBatchStoreFault pins the all-or-nothing ingest contract: the
// client spool deletes its records on any 2xx WITHOUT reading `accepted`, so
// a store fault must answer a retryable 503; a per-event skip would
// return 200 with a partial count and silently lose the rest.
func TestAuditBatchStoreFault(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	token, _ := checkinToken(t, app, base)

	// Kill the store out from under the handler. The backlog gate fails
	// open by design; the batch insert is the first hard store dependency.
	if err := app.store.Close(); err != nil {
		t.Fatal(err)
	}
	batch := map[string]any{
		"events": []map[string]any{
			{"data": map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": "kept-in-spool"}},
		},
	}
	code, _ := postJSONAuth(t, base+"/v1/audit/batch", token, batch)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("batch on dead store = %d, want 503 (spool must keep + retry)", code)
	}
}

// sessionReads wraps a store, counts the session row reads per id, and
// answers a store fault for an id armed with setFail.
type sessionReads struct {
	store.Store
	mu    sync.Mutex
	count map[string]int
	fail  map[string]bool
}

func (s *sessionReads) Sessions() store.SessionRepo { return countedSessions{s.Store.Sessions(), s} }

func (s *sessionReads) reads(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count[id]
}

func (s *sessionReads) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.count {
		n += c
	}
	return n
}

func (s *sessionReads) setFail(id string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[id] = on
}

type countedSessions struct {
	store.SessionRepo
	r *sessionReads
}

func (c countedSessions) GetByID(ctx context.Context, id string) (store.Session, error) {
	c.r.mu.Lock()
	c.r.count[id]++
	fail := c.r.fail[id]
	c.r.mu.Unlock()
	if fail {
		return store.Session{}, errors.New("injected session read fault")
	}
	return c.SessionRepo.GetByID(ctx, id)
}

// spoolRig is a booted server with counted session reads and a captured log,
// for the tests of records a client spooled under one session and uploads
// under another.
type spoolRig struct {
	app   *App
	base  string
	reads *sessionReads
	logs  *syncBuffer
}

func newSpoolRig(t *testing.T) spoolRig {
	t.Helper()
	log, logs := captureLogger()
	reads := &sessionReads{count: map[string]int{}, fail: map[string]bool{}}
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		a.log = log
		reads.Store, a.store = a.store, reads
	}})
	return spoolRig{app: app, base: base, reads: reads, logs: logs}
}

func (r spoolRig) user(t *testing.T, name, userType string) store.User {
	t.Helper()
	u, err := r.app.store.Users().Create(context.Background(), store.User{Username: name, Email: name + "@x.io", UserType: userType})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// session opens a session row of a user on a device ("" for none), as a
// check-in does.
func (r spoolRig) session(t *testing.T, userID, deviceID string) store.Session {
	t.Helper()
	s, err := r.app.store.Sessions().Create(context.Background(), store.Session{
		UserID: userID, DeviceID: deviceID, HarnessName: "claude-code", HarnessVersion: "2.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// upload posts the records to /v1/audit/batch with the session token a
// check-in of the uploader's session hands its client.
func (r spoolRig) upload(t *testing.T, uploader store.Session, recs ...spooled) (int, map[string]any) {
	t.Helper()
	tok, _, err := r.app.tokens.Mint(authn.Claims{Subject: uploader.UserID, Session: uploader.ID, Device: uploader.DeviceID})
	if err != nil {
		t.Fatal(err)
	}
	events := make([]map[string]any, len(recs))
	for i, rec := range recs {
		data := map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": rec.command, "effect": "allow"}
		if rec.session != "" {
			data["session"] = rec.session
		}
		events[i] = map[string]any{"specversion": "1.0", "id": rec.id, "type": "straza.audit.tool", "source": "straza", "data": data}
	}
	return postJSONAuth(t, r.base+"/v1/audit/batch", tok, map[string]any{"events": events})
}

// spooled is one record as the client spool writes it: a CE id, the command
// decided, and data.session ("" leaves the field out).
type spooled struct{ id, command, session string }

// chainData returns the data of the chain record whose data.command is
// command, nil when the chain has none.
func chainData(app *App, command string) map[string]any {
	recs, _ := app.store.Audit().List(context.Background(), 0, 1000)
	for _, rec := range recs {
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(rec.CE), &ce) == nil && ce.Data["command"] == command {
			return ce.Data
		}
	}
	return nil
}

func waitChainData(t *testing.T, app *App, command string) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(30 * time.Millisecond) {
		if d := chainData(app, command); d != nil {
			return d
		}
	}
	t.Fatalf("the record of %q never reached the chain", command)
	return nil
}

// warnLines counts the WARN log lines that carry every needle.
func warnLines(buf *syncBuffer, needles ...string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		hit := strings.Contains(line, "level=WARN")
		for _, s := range needles {
			hit = hit && strings.Contains(line, s)
		}
		if hit {
			n++
		}
	}
	return n
}

// The reason fragments of the refusal Warn lines, one per check, and the
// sentence every one of them carries.
const (
	whyMalformed   = "is not a session id"
	whyUnknown     = "is not known to this server"
	whyOtherUser   = "belongs to another user"
	whyNoDevice    = "was opened without a device"
	whyOtherDevice = "was opened on another device of the same user"
	onlyTrace      = "so this line is their only trace"
)

// batchOutcome is what a spooled record should come to: the session its chain
// record carries, or the reason its Warn line gives.
type batchOutcome struct{ keep, reason string }

func kept(session string) batchOutcome   { return batchOutcome{keep: session} }
func refusedFor(why string) batchOutcome { return batchOutcome{reason: why} }

// TestAuditBatchKeepsDecidingSession pins how a spooled record's session
// survives the upload: the session it was decided under is kept when it
// belongs to the uploader's user and device, and otherwise the record is
// refused for the reason of the one check that fails, logged once and
// counted, while the batch still answers 200 so the client spool does not
// retry it forever.
func TestAuditBatchKeepsDecidingSession(t *testing.T) {
	t.Parallel()
	r := newSpoolRig(t)
	kim := seedIdentity(t, r.app)
	joe := r.user(t, "joe", store.UserTypeHuman)
	bot := r.user(t, "build-bot", store.UserTypeAgent)
	bot2 := r.user(t, "review-bot", store.UserTypeAgent)
	kimA, kimB := r.session(t, kim.ID, "dev-kim"), r.session(t, kim.ID, "dev-kim")
	kimLaptop, kimConsole := r.session(t, kim.ID, "dev-kim-laptop"), r.session(t, kim.ID, "")
	joeA := r.session(t, joe.ID, "dev-joe")
	botA, botB := r.session(t, bot.ID, ""), r.session(t, bot.ID, "")
	bot2A := r.session(t, bot2.ID, "")

	cases := []struct {
		name     string
		uploader store.Session
		records  []spooled
		want     []batchOutcome
	}{
		{"an earlier session of the same person and device", kimB,
			[]spooled{{"u5-1", "u5-earlier-session", kimA.ID}}, []batchOutcome{kept(kimA.ID)}},
		{"a session of another person", joeA,
			[]spooled{{"u5-2", "u5-other-person", kimA.ID}}, []batchOutcome{refusedFor(whyOtherUser)}},
		{"a session that does not exist", kimB,
			[]spooled{{"u5-3", "u5-unknown-session", uuid.NewString()}}, []batchOutcome{refusedFor(whyUnknown)}},
		{"a session of the same person on another device", kimB,
			[]spooled{{"u5-4", "u5-other-device", kimLaptop.ID}}, []batchOutcome{refusedFor(whyOtherDevice)}},
		{"no session field binds to the uploader", kimB,
			[]spooled{{"u5-5", "u5-no-session", ""}}, []batchOutcome{kept(kimB.ID)}},
		{"the uploader's own session", kimB,
			[]spooled{{"u5-6", "u5-own-session", kimB.ID}}, []batchOutcome{kept(kimB.ID)}},
		{"a batch of three stores two", kimB,
			[]spooled{{"u5-7a", "u5-mixed-none", ""}, {"u5-7b", "u5-mixed-earlier", kimA.ID}, {"u5-7c", "u5-mixed-foreign", joeA.ID}},
			[]batchOutcome{kept(kimB.ID), kept(kimA.ID), refusedFor(whyOtherUser)}},
		{"a headless agent keeps its earlier session", botB,
			[]spooled{{"u5-8", "u5-agent-earlier", botA.ID}}, []batchOutcome{kept(botA.ID)}},
		{"a headless agent names another agent's session", botB,
			[]spooled{{"u5-9", "u5-agent-other-agent", bot2A.ID}}, []batchOutcome{refusedFor(whyOtherUser)}},
		{"the same person's session without a device", kimB,
			[]spooled{{"u5-10", "u5-deviceless", kimConsole.ID}}, []batchOutcome{refusedFor(whyNoDevice)}},
		{"a session value that is not a UUID", kimB,
			[]spooled{{"u5-11", "u5-malformed", "no-such-session"}}, []batchOutcome{refusedFor(whyMalformed)}},
	}
	var refused []string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantAccepted, wantRefused := 0, 0
			for _, o := range c.want {
				if o.reason != "" {
					wantRefused++
				} else {
					wantAccepted++
				}
			}
			code, resp := r.upload(t, c.uploader, c.records...)
			if code != http.StatusOK || resp["accepted"] != float64(wantAccepted) || resp["refused"] != float64(wantRefused) {
				t.Fatalf("batch = %d %v, want 200 with accepted %d and refused %d", code, resp, wantAccepted, wantRefused)
			}
			for i, rec := range c.records {
				if why := c.want[i].reason; why != "" {
					refused = append(refused, rec.command)
					if n := warnLines(r.logs, "record="+rec.id, " session="+c.uploader.ID, why, onlyTrace); n != 1 {
						t.Errorf("WARN lines naming record %s, the uploader's session and %q = %d, want 1:\n%s", rec.id, why, n, r.logs.String())
					}
					continue
				}
				data := waitChainData(t, r.app, rec.command)
				if data["session"] != c.want[i].keep || data["user"] != c.uploader.UserID {
					t.Errorf("record %s landed under session %v of user %v, want session %s of user %s",
						rec.id, data["session"], data["user"], c.want[i].keep, c.uploader.UserID)
				}
			}
		})
	}
	// The outbox keeps submission order, so once a later record is on the
	// chain, a refused record that had been stored would be there too.
	if code, _ := r.upload(t, kimB, spooled{"u5-last", "u5-last", ""}); code != http.StatusOK {
		t.Fatalf("control batch = %d", code)
	}
	waitChainData(t, r.app, "u5-last")
	for _, command := range refused {
		if chainData(r.app, command) != nil {
			t.Errorf("refused record %q reached the chain", command)
		}
	}
	if got := counterValue(t, r.app, "straza_audit_refused_total"); got != float64(len(refused)) {
		t.Errorf("straza_audit_refused_total = %v, want %d", got, len(refused))
	}
}

// TestAuditBatchReadsEachNamedSessionOnce pins the cost of the session check,
// one store read per distinct session id a batch names, none for the
// uploader's own and none for a value that is not a UUID, and that a client
// value is cut to 64 bytes in the log line.
func TestAuditBatchReadsEachNamedSessionOnce(t *testing.T) {
	t.Parallel()
	r := newSpoolRig(t)
	kim := seedIdentity(t, r.app)
	joe := r.user(t, "joe", store.UserTypeHuman)
	kimA, kimB := r.session(t, kim.ID, "dev-kim"), r.session(t, kim.ID, "dev-kim")
	joeA := r.session(t, joe.ID, "dev-joe")
	unknown, nul, long := uuid.NewString(), "\x00", strings.Repeat("x", 64<<10)

	code, resp := r.upload(t, kimB,
		spooled{"u5-r1", "u5-reads-1", kimA.ID}, spooled{"u5-r2", "u5-reads-2", kimA.ID},
		spooled{"u5-r3", "u5-reads-3", joeA.ID}, spooled{"u5-r4", "u5-reads-4", joeA.ID},
		spooled{"u5-r5", "u5-reads-5", unknown}, spooled{"u5-r6", "u5-reads-6", unknown},
		spooled{"u5-r7", "u5-reads-7", kimB.ID}, spooled{"u5-r8", "u5-reads-8", ""},
		spooled{"u5-r9", "u5-reads-9", nul}, spooled{"u5-r10-" + long, "u5-reads-10", long})
	if code != http.StatusOK || resp["accepted"] != float64(4) || resp["refused"] != float64(6) {
		t.Fatalf("batch = %d %v, want 200 with accepted 4 and refused 6", code, resp)
	}
	for id, want := range map[string]int{kimA.ID: 1, joeA.ID: 1, unknown: 1, kimB.ID: 0, nul: 0, long: 0} {
		if got := r.reads.reads(id); got != want {
			t.Errorf("session reads of %.40q = %d, want %d", id, got, want)
		}
	}
	if n := warnLines(r.logs, "record=u5-r9 ", whyMalformed); n != 1 {
		t.Errorf("WARN lines for the NUL session = %d, want 1", n)
	}
	cut := "record=u5-r10-" + long[:64-len("u5-r10-")] + "..."
	if n := warnLines(r.logs, cut, "named_session="+long[:64]+"...", whyMalformed); n != 1 {
		t.Errorf("WARN lines with the record id and session cut to 64 bytes = %d, want 1", n)
	}
	for _, line := range strings.Split(r.logs.String(), "\n") {
		if strings.Contains(line, "audit batch:") && len(line) > 2048 {
			t.Errorf("a log line is %d bytes long, want the client values cut: %.200s", len(line), line)
		}
	}
}

// TestAuditBatchSessionReadFault pins that a failed session read answers a
// retryable 503 that stores nothing and logs and counts no refusal, because
// a refusal answers 200 and the client spool deletes what a 200 answers, and
// that the retry after the fault logs and counts each refusal once.
func TestAuditBatchSessionReadFault(t *testing.T) {
	t.Parallel()
	r := newSpoolRig(t)
	kim := seedIdentity(t, r.app)
	joe := r.user(t, "joe", store.UserTypeHuman)
	kimA, kimB := r.session(t, kim.ID, "dev-kim"), r.session(t, kim.ID, "dev-kim")
	joeA := r.session(t, joe.ID, "dev-joe")
	batch := []spooled{{"u5-f1", "u5-fault-foreign", joeA.ID}, {"u5-f2", "u5-fault-own", ""}, {"u5-f3", "u5-fault-earlier", kimA.ID}}

	r.reads.setFail(kimA.ID, true)
	if code, _ := r.upload(t, kimB, batch...); code != http.StatusServiceUnavailable {
		t.Fatalf("batch over a failed session read = %d, want 503 so the spool keeps it", code)
	}
	if n := warnLines(r.logs, "record=u5-f1"); n != 0 {
		t.Errorf("WARN lines for the refused record of a 503 batch = %d, want 0", n)
	}
	if got := counterValue(t, r.app, "straza_audit_refused_total"); got != 0 {
		t.Errorf("straza_audit_refused_total after the 503 = %v, want 0", got)
	}

	r.reads.setFail(kimA.ID, false)
	code, resp := r.upload(t, kimB, batch...)
	if code != http.StatusOK || resp["accepted"] != float64(2) || resp["refused"] != float64(1) {
		t.Fatalf("retry = %d %v, want 200 with accepted 2 and refused 1", code, resp)
	}
	if n := warnLines(r.logs, "record=u5-f1", whyOtherUser); n != 1 {
		t.Errorf("WARN lines for the refused record after the retry = %d, want 1", n)
	}
	if got := counterValue(t, r.app, "straza_audit_refused_total"); got != 1 {
		t.Errorf("straza_audit_refused_total after the retry = %v, want 1", got)
	}
	if d := waitChainData(t, r.app, "u5-fault-earlier"); d["session"] != kimA.ID {
		t.Errorf("the retried record landed under session %v, want %s", d["session"], kimA.ID)
	}
}

// TestAuditBatchCapsNamedSessions pins the cap on distinct sessions one batch
// may name: past maxNamedSessions a record naming a new session is refused
// without a read, all of them share one Warn line with their count, and a
// record that names no session is still stored.
func TestAuditBatchCapsNamedSessions(t *testing.T) {
	t.Parallel()
	r := newSpoolRig(t)
	kim := seedIdentity(t, r.app)
	kimB := r.session(t, kim.ID, "dev-kim")
	recs := make([]spooled, 0, maxNamedSessions+3)
	for i := range maxNamedSessions + 2 {
		recs = append(recs, spooled{fmt.Sprintf("u5-cap-%d", i), fmt.Sprintf("u5-cap-%d", i), uuid.NewString()})
	}
	recs = append(recs, spooled{"u5-cap-own", "u5-cap-own", ""})

	code, resp := r.upload(t, kimB, recs...)
	if code != http.StatusOK || resp["accepted"] != float64(1) || resp["refused"] != float64(maxNamedSessions+2) {
		t.Fatalf("batch = %d %v, want 200 with accepted 1 and refused %d", code, resp, maxNamedSessions+2)
	}
	if got := r.reads.total(); got != maxNamedSessions {
		t.Errorf("session reads = %d, want %d", got, maxNamedSessions)
	}
	if n := warnLines(r.logs, whyUnknown); n != maxNamedSessions {
		t.Errorf("WARN lines for unknown sessions = %d, want %d", n, maxNamedSessions)
	}
	capLine := fmt.Sprintf("names more than %d sessions other than the uploader's", maxNamedSessions)
	if n := warnLines(r.logs, capLine, "count=2", fmt.Sprintf("record=u5-cap-%d ", maxNamedSessions)); n != 1 {
		t.Errorf("WARN lines for the capped records = %d, want 1 with count=2:\n%s", n, lastLines(r.logs.String(), 3))
	}
	if got := counterValue(t, r.app, "straza_audit_refused_total"); got != float64(maxNamedSessions+2) {
		t.Errorf("straza_audit_refused_total = %v, want %d", got, maxNamedSessions+2)
	}
	waitChainData(t, r.app, "u5-cap-own")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
