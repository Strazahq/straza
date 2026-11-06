package service

import (
	"net/http"
	"sync"
	"time"
)

// maxTextCalls is how many decide and capture calls one uid may have in
// flight at once. A local decide answers in milliseconds, so the parallel
// tool calls of several windows stay well below it.
const maxTextCalls = 16

// maxTextBytes bounds the bodies of one uid's decide and capture calls in
// flight, counted by their declared length. Decoding and deciding a body
// built to amplify peaks near 30 times its size: 16 MiB of empty paths
// peaked at 287 MiB of heap and 16 MiB of one-letter words at 466 MiB,
// where a plain 16 MiB command peaked at 81 MiB. So this holds one
// account's calls under 500 MiB, and a decide of a few KiB beside a large
// call still fits.
const maxTextBytes = textBodyMax + 1<<20

// textReadBudget bounds the read of a decide or capture body, so a caller
// cannot hold one of its maxTextCalls places with a body it never
// finishes. A client on the same machine sends 16 MiB far faster. Var, not
// const, so a test can shrink it.
var textReadBudget = 5 * time.Second

// Refusals of a decide or capture call past maxTextCalls or maxTextBytes.
const (
	tooManyDecides = "The Straza background service is already working on as many tool calls and captured turns of your " +
		"account on this machine as it takes at once, so it denied this call. Try the call once more in a minute. " +
		"If it is denied again, tell the user."
	tooManyCaptures = "The Straza background service is already working on as many tool calls and captured turns of your " +
		"account on this machine as it takes at once, so this captured text was not recorded. Your work is not affected. " +
		"Tell your Straza admin if this keeps happening, because those turns are missing from the audit record."
)

// bounded refuses a call to h with sentence, before its body is read, when
// the call would take the caller's uid past maxTextCalls decide and capture
// calls or maxTextBytes of their bodies, and notes the refused call, named
// by call, in the uid's loss ledger. A body of unknown length counts as
// textBodyMax. A call it admits must send its body within textReadBudget.
func (a *api) bounded(call, sentence string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := callerOf(r)
		n := r.ContentLength
		if n < 0 || n > textBodyMax {
			n = textBodyMax
		}
		if !a.text.enter(c.UID, n) {
			a.noteRefused(c, call)
			a.refuse(w, r, http.StatusTooManyRequests, sentence)
			return
		}
		defer a.text.leave(c.UID, n)
		// net/http lifts this deadline itself once the body is read, so a
		// decide that then waits on strazad keeps its context.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(textReadBudget))
		h(w, r)
	}
}

// refusedLedger is the credential half's loss ledger. It counts a call the
// API half refused before the call could be recorded, so the next loss
// record names it. credStore keeps one per account.
type refusedLedger interface {
	NoteRefused(c Caller, call string)
}

// noteRefused counts in c's loss ledger one call, decide or capture, that
// the service refused before it could record it.
func (a *api) noteRefused(c Caller, call string) {
	if l, ok := a.creds.(refusedLedger); ok {
		l.NoteRefused(c, call)
	}
}

// inFlight counts the decide and capture calls each uid has open and the
// bytes of their bodies.
type inFlight struct {
	mu   sync.Mutex
	open map[string]load
}

// load is one uid's open calls and the bytes of their bodies.
type load struct {
	calls int
	bytes int64
}

// enter counts one more open call of uid with a body of n bytes and reports
// false, counting nothing, when that would pass maxTextCalls or
// maxTextBytes. A uid with no call open is always admitted.
func (f *inFlight) enter(uid string, n int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.open[uid]
	if l.calls >= maxTextCalls || (l.calls > 0 && l.bytes+n > maxTextBytes) {
		return false
	}
	f.open[uid] = load{calls: l.calls + 1, bytes: l.bytes + n}
	return true
}

// leave counts one open call of uid with a body of n bytes less.
func (f *inFlight) leave(uid string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.open[uid]
	if l.calls <= 1 {
		delete(f.open, uid)
		return
	}
	f.open[uid] = load{calls: l.calls - 1, bytes: l.bytes - n}
}
