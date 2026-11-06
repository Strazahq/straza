package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/classifier"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/version"
)

// Request body bounds. Decide and capture carry prompt and reply text,
// whose hash covers the whole text even when the capture mode caps the
// stored copy, so they read more than the other routes.
const (
	smallBodyMax = 64 << 10
	textBodyMax  = 16 << 20
)

// Per-uid limits on check-ins and enrollments. Each window starts twice,
// once for its hook and once for its `straza mcp` bridge, and a start that
// reuses a live session gets its token back. A burst of 60 refilled at 10
// per second so admits 15 windows with hook and bridge starting at once
// twice over.
const (
	startBurst = 60
	startRate  = 10
)

// sessionBudget bounds one POST sessions below the thin client's timeout,
// so a start queued behind other windows' check-ins answers the busy
// sentence instead of the client giving up first.
const sessionBudget = localapi.SessionTimeout - 2*time.Second

// busyStarting is the answer to a start that sessionBudget cut short.
var busyStarting = fmt.Sprintf("The Straza background service was busy checking in your other Straza sessions "+
	"on this machine and could not start this one within %d seconds. Start the session again.", int(sessionBudget/time.Second))

// maxHarnessField bounds the harness name, version and session id of a
// request, because each becomes part of a held session's key and of every
// save of the session table.
const maxHarnessField = 256

// maxEventField bounds the other strings a decide or capture record copies
// from its request: the tool, app, tool name and workspace and the
// delegate's type and id. No harness sends one near it.
const maxEventField = 4 << 10

// harnessFieldTooLong is the refusal of a harness or event field over its
// bound: the field, its length and the bound.
const harnessFieldTooLong = "The request names a %s of %d bytes, and the Straza background service takes at most %d, " +
	"so it refused the request. Update Straza so the client and the service are the same version, and tell your Straza admin " +
	"if this keeps happening."

// tooManyStarts is the 429 sentence of the start limit.
const tooManyStarts = "The Straza background service refused this request because your account started too many " +
	"Straza sessions or enrollments within a few seconds. Wait a few seconds, then try again."

// Run serves the local API until ctx ends. It takes the state directory's
// lock through the credential half first, so a second service on the same
// directory stops with that half's sentence before it touches the socket,
// and it resumes the saved sessions before it answers anyone. It calls
// opts.Ready once the socket listens, just before it serves.
func Run(ctx context.Context, opts Options) error {
	if err := supported(); err != nil {
		return err
	}
	creds, err := newCredentials(ctx, opts)
	if err != nil {
		return err
	}
	ln, err := listen(opts)
	if err != nil {
		return err
	}
	creds.resumeAccounts()
	a := newAPI(creds, opts)
	if err := agentguard.ManagedRootUntrusted(); err != nil {
		// The service then uses each enrollment's server and keys, so this
		// line is where an admin learns why the managed config was ignored.
		a.log.Warn("the managed config is not used", "reason", err.Error())
	}
	if opts.Ready != nil {
		opts.Ready()
	}
	return serve(ctx, ln, a)
}

// serve answers the local API on ln until ctx ends. Request contexts derive
// from ctx, so open streams end with it.
func serve(ctx context.Context, ln net.Listener, a *api) error {
	srv := &http.Server{
		Handler:           a.routes(),
		ConnContext:       withPeer,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 10 * time.Second,
	}
	a.log.Info("straza service listening", "socket", ln.Addr().String(), "user_mode", a.userMode)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	a.log.Info("straza service stopped")
	return nil
}

// api is the API half of the service. It reaches the credential half only
// through creds.
type api struct {
	creds      credentials
	log        *slog.Logger
	userMode   bool
	self       string
	classifier policy.Classifier
	starts     *limiter
	text       *inFlight
}

// newAPI returns the API half over creds. The service's own uid decides
// each caller's custody.
func newAPI(creds credentials, opts Options) *api {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &api{
		creds:      creds,
		log:        log,
		userMode:   opts.UserMode,
		self:       ownUID(),
		classifier: classifier.NewHeuristic(),
		starts:     newLimiter(startBurst, startRate),
		text:       &inFlight{open: map[string]load{}},
	}
}

// routes returns the local API's handler. Every route checks its method
// itself, so a wrong method gets a Refusal and not the mux's plain text.
func (a *api) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(localapi.RouteStatus, a.only(http.MethodGet, a.status))
	mux.HandleFunc(localapi.RouteEnroll, a.only(http.MethodPost, a.enroll))
	mux.HandleFunc(localapi.RouteKeygen, a.only(http.MethodPost, a.keygen))
	mux.HandleFunc(localapi.RouteSessions, a.only(http.MethodPost, a.sessions))
	mux.HandleFunc(localapi.RouteDecide, a.only(http.MethodPost, a.bounded("decide", tooManyDecides, a.decide)))
	mux.HandleFunc(localapi.RouteCapture, a.only(http.MethodPost, a.bounded("capture", tooManyCaptures, a.capture)))
	mux.HandleFunc(localapi.RouteMCP, a.proxyMCP)
	mux.HandleFunc(localapi.RouteMCP+"/{server}", a.proxyMCP)
	mux.HandleFunc(strings.TrimSuffix(localapi.RouteConnect, "/"), a.proxyConnect)
	mux.HandleFunc(localapi.RouteConnect+"{rest...}", a.proxyConnect)
	mux.Handle(localapi.RouteGit, a.gitRoutes())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.refuse(w, r, http.StatusNotFound, "The Straza background service has no route "+r.URL.Path+
			". Update Straza so the client and the service are the same version.")
	})
	return a.identify(mux)
}

// peerKey keys the connection's peer in a request context.
type peerKey struct{}

// peer is a connection's other end: the connection withPeer stored, and
// the caller and error identify read from it for one request.
type peer struct {
	conn   net.Conn
	caller Caller
	err    error
}

// withPeer records conn in the connection's context, so identify names
// each request's caller from the connection and never from the request.
// It reads nothing itself, because net/http calls it in its accept loop
// and Windows names a pipe client only after the service read from it.
func withPeer(ctx context.Context, conn net.Conn) context.Context {
	return context.WithValue(ctx, peerKey{}, peer{conn: conn})
}

// callerOf returns the caller of r, which identify has checked.
func callerOf(r *http.Request) Caller {
	p, _ := r.Context().Value(peerKey{}).(peer)
	return p.caller
}

// Refusals of a request whose caller the system did not name. On Windows
// that is a client that dialed the pipe without letting the service
// identify its account, or a failed impersonation.
const (
	callerUnknown = "The Straza background service could not tell which user is calling, so it refused the request. " +
		"Run `straza doctor`, and if this keeps happening, check the service's log."
	callerUnknownWindows = "The Straza background service could not tell which Windows account sent this request, " +
		"so it refused it. Run the command again from your own signed-in session."
)

// identify names the caller of each request from its connection. It
// refuses a request whose caller the system did not name, and in user mode
// a caller who is not the user the service runs as.
func (a *api) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(peerKey{}).(peer)
		if ok && p.conn != nil {
			p.caller, p.err = peerOf(p.conn)
			r = r.WithContext(context.WithValue(r.Context(), peerKey{}, p))
		}
		if !ok || p.err != nil {
			why := "no peer credentials"
			if p.err != nil {
				why = p.err.Error()
			}
			a.log.Warn("local API refusal", "uid", "unknown", "route", r.URL.Path,
				"status", http.StatusForbidden, "reason", "the kernel did not name the caller: "+why)
			sentence := callerUnknown
			if runtime.GOOS == "windows" {
				sentence = callerUnknownWindows
			}
			writeJSON(w, http.StatusForbidden, localapi.Refusal{Error: sentence})
			return
		}
		if a.userMode && p.caller.UID != a.self {
			// A user-mode service holds one user's enrollment, so the socket's
			// file modes are not its only check on who may call.
			a.refuse(w, r, http.StatusForbidden, "This Straza background service runs in user mode for another user, "+
				"so it refused your request. Start your own with `straza service` in a shell that has your own STRAZA_HOME.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// only refuses a method other than method on h's route.
func (a *api) only(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			a.refuse(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("The Straza background service takes %s on %s, not %s. "+
				"Update Straza so the client and the service are the same version.", method, r.URL.Path, r.Method))
			return
		}
		h(w, r)
	}
}

// refuse answers status with sentence and logs one line with the caller's
// uid, the route and the sentence. Sentences never carry a token.
func (a *api) refuse(w http.ResponseWriter, r *http.Request, status int, sentence string) {
	a.logRefusal(r, status, sentence)
	writeJSON(w, status, localapi.Refusal{Error: sentence})
}

// logRefusal logs one refusal of r.
func (a *api) logRefusal(r *http.Request, status int, sentence string) {
	a.log.Warn("local API refusal", "uid", callerOf(r).UID, "route", r.URL.Path, "status", status, "reason", sentence)
}

// fail answers err: a *Refused with its own status and sentence, any other
// error with status and its text, which the credential half words as a
// sentence.
func (a *api) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	var refused *Refused
	if errors.As(err, &refused) {
		a.refuse(w, r, refused.Status, refused.Sentence)
		return
	}
	a.refuse(w, r, status, err.Error())
}

// read decodes r's JSON body into v, reading at most limit bytes into one
// buffer sized by the request's length. It answers the refusal itself and
// reports false when the body is too large, not UTF-8 or not the route's
// JSON. The decoder would turn each stray byte into a three-byte
// replacement character, so a body that is not UTF-8 never reaches it.
func (a *api) read(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	body := bytes.NewBuffer(make([]byte, 0, min(max(r.ContentLength, 0), limit)+bytes.MinRead))
	_, err := body.ReadFrom(http.MaxBytesReader(w, r.Body, limit))
	switch {
	case err != nil:
	case !utf8.Valid(body.Bytes()):
		err = errors.New("the body is not UTF-8")
	default:
		err = json.Unmarshal(body.Bytes(), v)
	}
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooBig):
		a.refuse(w, r, http.StatusRequestEntityTooLarge, tooLarge(r.URL.Path, limit))
	default:
		a.refuse(w, r, http.StatusBadRequest, "The Straza background service could not read the request ("+err.Error()+
			"). Update Straza so the client and the service are the same version.")
	}
	return false
}

// named refuses a request whose harness has no name, because the service
// keys every session by it, and one whose harness name, version or
// session id is longer than maxHarnessField.
func (a *api) named(w http.ResponseWriter, r *http.Request, h localapi.Harness) bool {
	if h.Name == "" {
		a.refuse(w, r, http.StatusBadRequest, "The request names no harness, so the Straza background service cannot tell "+
			"which session it belongs to. Update Straza so the client and the service are the same version.")
		return false
	}
	return a.fit(w, r, maxHarnessField, field{"harness name", h.Name}, field{"harness version", h.Version},
		field{"harness session id", h.Session})
}

// field is one named string of a request that fit bounds.
type field struct{ name, value string }

// fit refuses a request with one of fields longer than limit bytes, with
// the sentence that names the field.
func (a *api) fit(w http.ResponseWriter, r *http.Request, limit int, fields ...field) bool {
	for _, f := range fields {
		if len(f.value) > limit {
			a.refuse(w, r, http.StatusBadRequest, fmt.Sprintf(harnessFieldTooLong, f.name, len(f.value), limit))
			return false
		}
	}
	return true
}

// tooLarge words the refusal of a body over limit on route.
func tooLarge(route string, limit int64) string {
	switch route {
	case localapi.RouteCapture:
		return fmt.Sprintf("The captured text is larger than the %s the Straza background service reads, so it was not "+
			"recorded. Your work is not affected. Tell your Straza admin if this keeps happening, because those turns "+
			"are missing from the audit record.", sizeWords(limit))
	case localapi.RouteDecide:
		return fmt.Sprintf("The request for this tool call is larger than the %s the Straza background service reads, "+
			"so the service refused it and the call is denied. Make the tool call smaller, or tell your Straza admin "+
			"if this keeps happening.", sizeWords(limit))
	}
	return fmt.Sprintf("The request is larger than the %s the Straza background service reads on %s, so the service "+
		"refused it. Update Straza so the client and the service are the same version.", sizeWords(limit), route)
}

// sizeWords words a body bound.
func sizeWords(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d KiB", n>>10)
}

// writeJSON writes v as the answer with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// custodyOf is the custody of the key a service running as self holds for
// uid: account when uid is another account than the service's, user when
// the service runs as that user.
func custodyOf(self, uid string) string {
	if uid == self {
		return localapi.CustodyUser
	}
	return localapi.CustodyAccount
}

// status answers GET status for the caller.
func (a *api) status(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	st := a.creds.Status(c)
	st.Version, st.UserMode, st.Custody = version.Version, a.userMode, custodyOf(a.self, c.UID)
	writeJSON(w, http.StatusOK, st)
}

// keygen answers POST keygen.
func (a *api) keygen(w http.ResponseWriter, r *http.Request) {
	var req localapi.KeygenRequest
	if !a.read(w, r, smallBodyMax, &req) {
		return
	}
	ans, err := a.creds.Keygen(r.Context(), callerOf(r), req)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ans)
}

// enroll answers POST enroll as one JSON event per line, flushed as each
// arrives, ending with the done event or the error event. A failure before
// the first event answers a Refusal with its status instead.
func (a *api) enroll(w http.ResponseWriter, r *http.Request) {
	var req localapi.EnrollRequest
	if !a.read(w, r, smallBodyMax, &req) {
		return
	}
	c := callerOf(r)
	if !a.starts.allow(c.UID) {
		a.refuse(w, r, http.StatusTooManyRequests, tooManyStarts)
		return
	}
	var mu sync.Mutex
	started := false
	rc := http.NewResponseController(w)
	emit := func(ev localapi.EnrollEvent) {
		mu.Lock()
		defer mu.Unlock()
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		_ = json.NewEncoder(w).Encode(ev)
		_ = rc.Flush()
	}
	res, err := a.creds.Enroll(r.Context(), c, req, emit)
	mu.Lock()
	streaming := started
	mu.Unlock()
	switch {
	case err != nil && !streaming:
		a.fail(w, r, http.StatusInternalServerError, err)
	case err != nil:
		a.logRefusal(r, http.StatusOK, err.Error())
		emit(localapi.EnrollEvent{Event: localapi.EnrollError, Error: err.Error()})
	default:
		emit(localapi.EnrollEvent{Event: localapi.EnrollDone, Result: &res})
	}
}

// sessions answers POST sessions: the caller's session for the window,
// started when none is live, with the capture directive its policy gives
// the session's subject. Only a start that checks in keeps its start
// token, and the whole start runs within sessionBudget.
func (a *api) sessions(w http.ResponseWriter, r *http.Request) {
	var req localapi.SessionRequest
	if !a.read(w, r, smallBodyMax, &req) || !a.named(w, r, req.Harness) {
		return
	}
	c := callerOf(r)
	if !a.starts.allow(c.UID) {
		a.refuse(w, r, http.StatusTooManyRequests, tooManyStarts)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sessionBudget)
	defer cancel()
	s, err := a.creds.Session(ctx, c, req.Harness, true)
	switch {
	case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		a.refuse(w, r, http.StatusServiceUnavailable, busyStarting)
		return
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, err)
		return
	case s.Reused:
		a.starts.refund(c.UID)
	}
	eng, _, snapshot, err := a.creds.Policy(ctx, s)
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, localapi.SessionAnswer{
		SessionID: s.ID, User: s.User, Roles: s.Roles, Attestation: s.Attestation,
		SnapshotID: snapshot, ServerURL: s.ServerURL, Capture: eng.Capture(s.Subject()),
		Packs: s.Packs, Reused: s.Reused, StartText: s.StartText,
	})
}

// limiter is a token bucket per uid. The uids on one machine are few, so
// buckets are never evicted.
type limiter struct {
	mu      sync.Mutex
	burst   float64
	rate    float64
	buckets map[string]*bucket
	now     func() time.Time
}

// bucket is one uid's tokens and when they were last counted.
type bucket struct {
	tokens float64
	last   time.Time
}

// newLimiter returns a limiter whose buckets start full at burst and
// refill at rate tokens per second.
func newLimiter(burst, rate float64) *limiter {
	return &limiter{burst: burst, rate: rate, buckets: map[string]*bucket{}, now: time.Now}
}

// allow spends one of uid's tokens and reports whether one was left.
func (l *limiter) allow(uid string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[uid]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[uid] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// refund gives uid back the token a start spent when it checked nothing
// in.
func (l *limiter) refund(uid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.buckets[uid]; b != nil {
		b.tokens = min(l.burst, b.tokens+1)
	}
}
