package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// The points where a leavingStore runs an armed hook.
const (
	beforeAssign = "before assign"
	afterAssign  = "after assign"
	beforeList   = "before list"
	afterUpdate  = "after update"
	afterGet     = "after get"
	afterWrite   = "after write"
	beforeRevoke = "before session revoke"
)

// leavingStore stands in for a client that leaves while an admin call is in
// the store. An armed hook runs at its point in the role, user or session
// repo, and only for a call on an admin request's context, which carries a
// named actor, so the server's own background calls pass untouched. A
// hook's error is the call's answer.
type leavingStore struct {
	store.Store
	mu    sync.Mutex
	hooks map[string]func(context.Context) error
}

func (s *leavingStore) arm(point string, hook func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks[point] = hook
}

// run runs the hook armed at point for a call on an admin request's
// context and answers its error, or nil when none applies.
func (s *leavingStore) run(ctx context.Context, point string) error {
	if act, ok := actorFrom(ctx); !ok || act.Name == "" {
		return nil
	}
	s.mu.Lock()
	hook := s.hooks[point]
	s.mu.Unlock()
	if hook == nil {
		return nil
	}
	return hook(ctx)
}

func (s *leavingStore) Roles() store.RoleRepo { return leavingRoles{s.Store.Roles(), s} }
func (s *leavingStore) Users() store.UserRepo { return leavingUsers{s.Store.Users(), s} }
func (s *leavingStore) Sessions() store.SessionRepo {
	return leavingSessions{s.Store.Sessions(), s}
}

type leavingRoles struct {
	store.RoleRepo
	s *leavingStore
}

func (r leavingRoles) Assign(ctx context.Context, as store.RoleAssignment) (store.RoleAssignment, error) {
	if err := r.s.run(ctx, beforeAssign); err != nil {
		return store.RoleAssignment{}, err
	}
	created, err := r.RoleRepo.Assign(ctx, as)
	if err != nil {
		return created, err
	}
	if err := r.s.run(ctx, afterAssign); err != nil {
		return store.RoleAssignment{}, err
	}
	return created, nil
}

func (r leavingRoles) ListAllAssignments(ctx context.Context) ([]store.RoleAssignment, error) {
	if err := r.s.run(ctx, beforeList); err != nil {
		return nil, err
	}
	return r.RoleRepo.ListAllAssignments(ctx)
}

type leavingUsers struct {
	store.UserRepo
	s *leavingStore
}

func (u leavingUsers) GetByID(ctx context.Context, id string) (store.User, error) {
	got, err := u.UserRepo.GetByID(ctx, id)
	if err != nil {
		return got, err
	}
	if err := u.s.run(ctx, afterGet); err != nil {
		return store.User{}, err
	}
	return got, nil
}

func (u leavingUsers) Update(ctx context.Context, row store.User) (store.User, error) {
	updated, err := u.UserRepo.Update(ctx, row)
	if err != nil {
		return updated, err
	}
	if err := u.s.run(ctx, afterWrite); err != nil {
		return store.User{}, err
	}
	return updated, nil
}

func (u leavingUsers) UpdateFields(ctx context.Context, id string, f store.UserFields) (store.User, error) {
	updated, err := u.UserRepo.UpdateFields(ctx, id, f)
	if err != nil {
		return updated, err
	}
	if err := u.s.run(ctx, afterUpdate); err != nil {
		return store.User{}, err
	}
	return updated, nil
}

type leavingSessions struct {
	store.SessionRepo
	s *leavingStore
}

func (l leavingSessions) SetStatusIfChanged(ctx context.Context, id, status string) (bool, error) {
	if err := l.s.run(ctx, beforeRevoke); err != nil {
		return false, err
	}
	return l.SessionRepo.SetStatusIfChanged(ctx, id, status)
}

// testAppLeaving boots a full standalone strazad over a leavingStore that
// logs to log, and answers the App, its base URL and the store.
func testAppLeaving(t *testing.T, log *slog.Logger) (*App, string, *leavingStore) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	seedStoreTemplate(t, cfg)
	raw, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ls := &leavingStore{Store: raw, hooks: map[string]func(context.Context) error{}}
	ctx, cancel := context.WithCancel(context.Background())
	app, err := build(ctx, cfg, log, ls)
	if err != nil {
		cancel()
		t.Fatalf("build: %v", err)
	}
	app.gateway.notify.SetDelay(0)
	base := "http://" + app.Addr()
	app.cfg.Server.PublicURL = base
	tokens, err := authn.NewTokenService(ctx, app.store.SigningKeys(), base, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	app.tokens = tokens
	app.http.Handler = app.routes()
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Log("app did not stop in time")
		}
	})
	return app, base, ls
}

// serveOn serves one request in process on ctx, the context a client's
// connection gives its request, and answers the recorder.
func serveOn(ctx context.Context, app *App, method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.http.Handler.ServeHTTP(rec, req)
	return rec
}

// eventsNaming answers the outbox payloads on subject whose key holds id.
func eventsNaming(t *testing.T, app *App, subject, key, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range outboxDataFor(t, app, subject) {
		if ev[key] == id {
			out = append(out, ev)
		}
	}
	return out
}

// adminWithTarget boots the app over a leavingStore with the person kim as
// straza-admin, and answers kim's login bearer, the person ada the writes
// target and a scratch business role.
func adminWithTarget(t *testing.T, log *slog.Logger) (*App, *leavingStore, string, store.User, store.Role) {
	t.Helper()
	app, base, ls := testAppLeaving(t, log)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ada := mkHuman(t, app, "ada")
	role, err := app.store.Roles().Create(context.Background(), store.Role{Name: "l4-role"})
	if err != nil {
		t.Fatal(err)
	}
	return app, ls, bearer, ada, role
}

// TestAdminWriteOnAGoneClient pins that an authorized admin write whose
// client leaves while the write is in the store still completes: the row
// lands, the handler answers as it does for a client that stayed, and the
// chained record and the identity announcement reach the outbox. The role
// assignment row stands in for the store that answers the cancel's error
// after the row committed, as pgx does. The user disable row stands in for
// a client that leaves right after the write, before the kill switch runs.
func TestAdminWriteOnAGoneClient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		point  string
		answer func(ctx context.Context) error
		method string
		path   func(ada store.User) string
		body   func(ada store.User, role store.Role) string
		status int
		check  func(t *testing.T, app *App, ada store.User, role store.Role)
	}{
		{
			name: "role assignment", point: afterAssign,
			answer: func(ctx context.Context) error { return ctx.Err() },
			method: http.MethodPost,
			path:   func(store.User) string { return "/v1/admin/assignments" },
			body: func(ada store.User, role store.Role) string {
				return `{"subject_kind":"user","subject_id":"` + ada.ID + `","role_id":"` + role.ID + `"}`
			},
			status: http.StatusCreated,
			check: func(t *testing.T, app *App, ada store.User, role store.Role) {
				held, err := app.store.Roles().ListAssignments(context.Background(), store.SubjectUser, ada.ID)
				if err != nil || len(held) != 1 || held[0].RoleID != role.ID {
					t.Fatalf("ada holds %+v (%v), want the one assignment of %s", held, err, role.Name)
				}
				recs := eventsNaming(t, app, "straza.audit.admin", "target", held[0].ID)
				if len(recs) != 1 || recs[0]["action"] != actionRolesAssign || recs[0]["role"] != role.Name ||
					recs[0]["actor"] != "kim" || recs[0]["actorVia"] != "login" {
					t.Errorf("roles.assign records of the row: %v, want one naming the role %s and the actor kim", recs, role.Name)
				}
			},
		},
		{
			name: "user disable", point: afterUpdate,
			answer: func(context.Context) error { return nil },
			method: http.MethodPatch,
			path:   func(ada store.User) string { return "/v1/admin/users/" + ada.ID },
			body:   func(store.User, store.Role) string { return `{"status":"disabled"}` },
			status: http.StatusOK,
			check: func(t *testing.T, app *App, ada store.User, _ store.Role) {
				row, err := app.store.Users().GetByID(context.Background(), ada.ID)
				if err != nil || row.Status != store.UserDisabled {
					t.Fatalf("ada's row is %q (%v), want disabled", row.Status, err)
				}
				recs := eventsNaming(t, app, "straza.audit.identity", "user", ada.ID)
				if len(recs) != 1 || recs[0]["action"] != "user.killed" || recs[0]["origin"] != store.RevocationOriginAdmin {
					t.Errorf("straza.audit.identity records of ada: %v, want one user.killed of origin admin", recs)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, ls, bearer, ada, role := adminWithTarget(t, slog.New(slog.DiscardHandler))
			reqCtx, leave := context.WithCancel(context.Background())
			defer leave()
			ls.arm(tc.point, func(ctx context.Context) error {
				leave()
				return tc.answer(ctx)
			})
			rec := serveOn(reqCtx, app, tc.method, tc.path(ada), bearer, tc.body(ada, role))
			if rec.Code != tc.status {
				t.Fatalf("%s %s on a gone client = %d %s, want %d", tc.method, tc.path(ada), rec.Code, rec.Body.String(), tc.status)
			}
			tc.check(t, app, ada, role)
			if got := eventsNaming(t, app, "straza.identity.updated", "id", ada.ID); len(got) != 1 {
				t.Errorf("straza.identity.updated events naming ada: %v, want one", got)
			}
		})
	}
}

// untilBound waits for the call's context to end, as a store that answers
// only after the write's bound does, with a safety wait the test reports.
func untilBound(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return errors.New("the store's safety wait ran out: nothing bounded the write")
	}
}

// stallOnce answers a hook that holds its first call until the write bound
// has passed, as a store that stops answering for a while does, and lets
// every later call through.
func stallOnce() func(context.Context) error {
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() { time.Sleep(adminWriteBound + 100*time.Millisecond) })
		return nil
	}
}

// TestAdminWriteOnAHungStore pins the bound of an admin write. A store that
// answers only after adminWriteBound ends the write there with the bound's
// 503, whatever the handler then answers: a refusal that words the
// deadline as its own cause, a success whose later steps failed on the
// expired context, or a 5xx. Each answer leaves exactly one Error record
// with its correlation id and one count in straza_http_errors_total. The
// kill switch of a lock still completes after the bound, because its writes
// run on afterCommit's context, and a stand-down finishes the session write
// in flight and starts no other. It shrinks the bound, so it does not run in
// parallel.
func TestAdminWriteOnAHungStore(t *testing.T) {
	old := adminWriteBound
	adminWriteBound = 300 * time.Millisecond
	t.Cleanup(func() { adminWriteBound = old })
	cases := []struct {
		name   string
		point  string
		answer func(ctx context.Context) error
		method string
		route  string
		path   func(ada store.User) string
		body   func(ada store.User, role store.Role) string
		// status and words describe the one Error record of the answer.
		status int
		words  []string
		absent []string
		setup  func(t *testing.T, app *App, ada store.User)
		check  func(t *testing.T, app *App, ada store.User)
	}{
		{
			name: "role assignment on a store that never answers", point: beforeAssign,
			answer: untilBound, method: http.MethodPost, route: "POST /v1/admin/assignments",
			path: func(store.User) string { return "/v1/admin/assignments" },
			body: func(ada store.User, role store.Role) string {
				return `{"subject_kind":"user","subject_id":"` + ada.ID + `","role_id":"` + role.ID + `"}`
			},
			status: http.StatusServiceUnavailable,
			words:  []string{"did not finish within", "deadline exceeded", "answered 400", "assign failed (unknown role or subject?)"},
		},
		{
			name: "user lock whose read answers after the bound", point: afterGet,
			answer: func(ctx context.Context) error { _ = untilBound(ctx); return nil },
			method: http.MethodPost, route: "POST /v1/admin/users/{id}/lock",
			path:   func(ada store.User) string { return "/v1/admin/users/" + ada.ID + "/lock" },
			body:   func(store.User, store.Role) string { return `{"reason":"l4 soar alert"}` },
			status: http.StatusServiceUnavailable,
			words:  []string{"did not finish within", "deadline exceeded", "answered 200"},
			absent: []string{"locked", "l4 soar alert"},
			check: func(t *testing.T, app *App, ada store.User) {
				rows, err := app.store.Revocations().ListByTarget(context.Background(), store.RevokeUser, ada.ID)
				if err != nil || len(rows) != 1 {
					t.Errorf("revocation rows of ada: %d (%v), want 1", len(rows), err)
				}
				if got := eventsNaming(t, app, "straza.audit.identity", "user", ada.ID); len(got) != 1 || got[0]["action"] != "user.killed" {
					t.Errorf("straza.audit.identity records of ada: %v, want one user.killed", got)
				}
			},
		},
		{
			name: "stand-down whose first session write answers after the bound", point: beforeRevoke,
			answer: stallOnce(),
			method: http.MethodPost, route: "POST /v1/admin/sessions/revoke",
			path:   func(store.User) string { return "/v1/admin/sessions/revoke" },
			body:   func(store.User, store.Role) string { return `{"user":"ada"}` },
			status: http.StatusServiceUnavailable,
			words:  []string{"did not finish within", "deadline exceeded", "answered 200"},
			absent: []string{"revoked\\\""},
			setup: func(t *testing.T, app *App, ada store.User) {
				for range 3 {
					if _, err := app.store.Sessions().Create(context.Background(), store.Session{UserID: ada.ID, HarnessName: "console"}); err != nil {
						t.Fatal(err)
					}
				}
			},
			check: func(t *testing.T, app *App, ada store.User) {
				rows, err := app.store.Sessions().ListByUser(context.Background(), ada.ID)
				live := 0
				for _, row := range rows {
					if row.Status == store.SessionActive {
						live++
					}
				}
				if err != nil || len(rows) != 3 || live != 2 {
					t.Errorf("ada's sessions: %d, %d of them active (%v), want 3 with one revoked", len(rows), live, err)
				}
				got := outboxDataFor(t, app, "straza.revocation.sessions")
				var named []any
				if len(got) == 1 {
					named, _ = got[0]["sessions"].([]any)
				}
				if len(got) != 1 || len(named) != 1 {
					t.Errorf("straza.revocation.sessions events: %v, want one naming the one revoked session", got)
				}
			},
		},
		{
			name: "user disable whose write answers the deadline after its commit", point: afterUpdate,
			answer: untilBound, method: http.MethodPatch, route: "PATCH /v1/admin/users/{id}",
			path:   func(ada store.User) string { return "/v1/admin/users/" + ada.ID },
			body:   func(store.User, store.Role) string { return `{"status":"disabled"}` },
			status: http.StatusInternalServerError,
			words:  []string{"update failed", "deadline exceeded"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := captureLogger()
			app, ls, bearer, ada, role := adminWithTarget(t, logger)
			if tc.setup != nil {
				tc.setup(t, app, ada)
			}
			ls.arm(tc.point, tc.answer)
			logs.Reset()
			start := time.Now()
			rec := serveOn(context.Background(), app, tc.method, tc.path(ada), bearer, tc.body(ada, role))
			took := time.Since(start)
			if took < adminWriteBound || took > adminWriteBound+2*time.Second {
				t.Errorf("the write ended after %v, want about %v", took, adminWriteBound)
			}
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s past the bound = %d %s, want 503", tc.route, rec.Code, rec.Body.String())
			}
			for _, want := range []string{"did not finish within", "may or may not have been applied",
				"Read the change back before you try again", "strazad's log around this time for " + tc.route, `"correlation_id"`} {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("the answer %s lacks %q", rec.Body.String(), want)
				}
			}
			id := rec.Header().Get(requestIDHeader)
			var records []string
			for _, line := range errorRecords(logs) {
				if strings.Contains(line, "correlation_id="+id) {
					records = append(records, line)
				}
			}
			if len(records) != 1 {
				t.Fatalf("Error records with the answer's correlation id = %d, want 1:\n%s", len(records), strings.Join(records, "\n"))
			}
			if !strings.Contains(records[0], "status="+strconv.Itoa(tc.status)) || !strings.Contains(records[0], "actor=kim") {
				t.Errorf("the Error record lacks status=%d or the actor: %s", tc.status, records[0])
			}
			for _, want := range tc.words {
				if !strings.Contains(records[0], want) {
					t.Errorf("the Error record lacks %q: %s", want, records[0])
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(records[0], bad) {
					t.Errorf("the Error record names %q, which a success's body carries: %s", bad, records[0])
				}
			}
			var counted float64
			for _, status := range []string{"500", "503"} {
				n, _, _ := metricSample(t, app, "straza_http_errors_total", map[string]string{"route": tc.route, "status": status})
				counted += n
			}
			if counted != 1 {
				t.Errorf("straza_http_errors_total for %s = %v, want 1", tc.route, counted)
			}
			if tc.check != nil {
				tc.check(t, app, ada)
			}
		})
	}
}

// TestAdminReadOnAGoneClient pins that a read keeps the request's context:
// a client that leaves a GET stops the store call it was waiting on.
func TestAdminReadOnAGoneClient(t *testing.T) {
	t.Parallel()
	app, ls, bearer, _, _ := adminWithTarget(t, slog.New(slog.DiscardHandler))
	reqCtx, leave := context.WithCancel(context.Background())
	defer leave()
	saw := make(chan error, 1)
	ls.arm(beforeList, func(ctx context.Context) error {
		leave()
		saw <- ctx.Err()
		return nil
	})
	serveOn(reqCtx, app, http.MethodGet, "/v1/admin/assignments", bearer, "")
	select {
	case err := <-saw:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the store call of a GET whose client left saw %v, want context.Canceled", err)
		}
	default:
		t.Fatal("the GET never reached the store")
	}
}

// TestRecordsOnAGoneClient pins that the records of a change that already
// happened reach the outbox when the context they are written on is
// cancelled: a login failure and a session end on straza.audit.authn, and
// the straza.identity.updated announcement.
func TestRecordsOnAGoneClient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, subject, key, want string
		emit                     func(ctx context.Context, app *App)
	}{
		{
			name: "login failure", subject: "straza.audit.authn", key: "reason", want: "l4 wrong password",
			emit: func(ctx context.Context, app *App) {
				r := httptest.NewRequest(http.MethodPost, "/v1/checkin", nil).WithContext(ctx)
				app.emitAuthnLogin(r, "failure", authnFields{Via: "id-token", User: "ada", UserID: "ada-id", Reason: "l4 wrong password"})
			},
		},
		{
			name: "session end", subject: "straza.audit.authn", key: "session", want: "ses-l4",
			emit: func(ctx context.Context, app *App) {
				r := httptest.NewRequest(http.MethodPost, "/v1/session/revoke", nil).WithContext(ctx)
				app.emitAuthnSessionEnd(ctx, r, "revoked-self", authnFields{User: "ada", UserID: "ada-id", Session: "ses-l4"})
			},
		},
		{
			name: "identity announcement", subject: "straza.identity.updated", key: "id", want: "ada-id",
			emit: func(ctx context.Context, app *App) {
				app.identityChangedCtx(ctx, "straza.identity.updated", "ada-id")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := bootApp(bootStore(t), slog.New(slog.DiscardHandler))
			ctx, leave := context.WithCancel(context.Background())
			leave()
			tc.emit(ctx, app)
			if got := eventsNaming(t, app, tc.subject, tc.key, tc.want); len(got) != 1 {
				t.Errorf("%s events with %s %s after a cancelled context: %v, want one", tc.subject, tc.key, tc.want, got)
			}
		})
	}
}

// TestKillSwitchOnAnEndedContext pins that the kill switch cascade of a
// committed deactivation runs whole when its caller's context has already
// ended, whether its client left or its write ran past the bound: the
// revocation row, the revoked session, the straza.revocation.user event and
// the user.killed record all land.
func TestKillSwitchOnAnEndedContext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		ended func() context.Context
	}{
		{name: "cancelled", ended: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{name: "past its deadline", ended: func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, _ := testApp(t)
			ada := mkHuman(t, app, "ada")
			ses, err := app.store.Sessions().Create(context.Background(), store.Session{UserID: ada.ID, HarnessName: "console"})
			if err != nil {
				t.Fatal(err)
			}
			app.revokeUserCtx(tc.ended(), ada.ID, "l4 cut", store.RevocationOriginAdmin)
			bg := context.Background()
			if rows, err := app.store.Revocations().ListByTarget(bg, store.RevokeUser, ada.ID); err != nil || len(rows) != 1 {
				t.Errorf("revocation rows of ada: %d (%v), want 1", len(rows), err)
			}
			if got, err := app.store.Sessions().GetByID(bg, ses.ID); err != nil || got.Status != store.SessionRevoked {
				t.Errorf("ada's session is %q (%v), want revoked", got.Status, err)
			}
			if got := eventsNaming(t, app, "straza.revocation.user", "user", ada.ID); len(got) != 1 {
				t.Errorf("straza.revocation.user events of ada: %v, want one", got)
			}
			got := eventsNaming(t, app, "straza.audit.identity", "user", ada.ID)
			if len(got) != 1 || got[0]["action"] != "user.killed" || got[0]["sessionsRevoked"] != float64(1) {
				t.Errorf("straza.audit.identity records of ada: %v, want one user.killed that revoked one session", got)
			}
		})
	}
}

// TestSCIMWriteOnAGoneClient pins the SCIM mount's rule for a write: an
// identity manager that leaves a PATCH that deactivates a person, while the
// store answers the cancel's error after the row committed, still gets the
// row disabled, the kill switch cascade and the user.update record.
func TestSCIMWriteOnAGoneClient(t *testing.T) {
	t.Parallel()
	app, base, ls := testAppLeaving(t, slog.New(slog.DiscardHandler))
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	token := mintProvisioningToken(t, base, loginDeviceFlow(t, base, "kim", "hunter2!"))
	id := scimCreateUser(t, base, token, `{"userName":"leaver@x.io","externalId":"idm-l4","active":true}`)
	reqCtx, leave := context.WithCancel(context.Background())
	defer leave()
	ls.arm(afterWrite, func(ctx context.Context) error {
		leave()
		return ctx.Err()
	})
	rec := serveOn(reqCtx, app, http.MethodPatch, "/scim/v2/Users/"+id, token,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("SCIM PATCH on a gone client = %d %s, want 200", rec.Code, rec.Body.String())
	}
	bg := context.Background()
	if row, err := app.store.Users().GetByID(bg, id); err != nil || row.Status != store.UserDisabled {
		t.Fatalf("the row is %q (%v), want disabled", row.Status, err)
	}
	if rows, err := app.store.Revocations().ListByTarget(bg, store.RevokeUser, id); err != nil || len(rows) != 1 {
		t.Errorf("revocation rows: %d (%v), want 1", len(rows), err)
	}
	if got := eventsNaming(t, app, "straza.audit.identity", "user", id); len(got) != 1 || got[0]["action"] != "user.killed" || got[0]["origin"] != "scim" {
		t.Errorf("straza.audit.identity records: %v, want one user.killed of origin scim", got)
	}
	var updates []map[string]any
	for _, ev := range eventsNaming(t, app, "straza.audit.admin", "user", id) {
		if ev["action"] == actionUserUpdate {
			updates = append(updates, ev)
		}
	}
	if len(updates) != 1 || updates[0]["actorVia"] != "api-token" {
		t.Errorf("user.update records: %v, want one by the provisioning token", updates)
	}
}
