package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/store"
)

// faultStore wraps a real store and, once armed for a repo, fails that repo's
// reads with an injected error: the fixture for "the database went away
// between the login and the lookup". Embedding is fine here (this is a fault
// injector, not the no-DB proof harness, which deliberately does not embed).
type faultStore struct {
	store.Store
	mu      sync.Mutex
	faults  map[string]error
	pingErr error
}

func (f *faultStore) arm(repo string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults[repo] = err
}

func (f *faultStore) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = map[string]error{}
	f.pingErr = nil
}

func (f *faultStore) setPing(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingErr = err
}

func (f *faultStore) fault(repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.faults[repo]
}

func (f *faultStore) Ping(ctx context.Context) error {
	f.mu.Lock()
	err := f.pingErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.Store.Ping(ctx)
}

func (f *faultStore) Users() store.UserRepo         { return faultUsers{f.Store.Users(), f} }
func (f *faultStore) Sessions() store.SessionRepo   { return faultSessions{f.Store.Sessions(), f} }
func (f *faultStore) Devices() store.DeviceRepo     { return faultDevices{f.Store.Devices(), f} }
func (f *faultStore) Approvers() store.ApproverRepo { return faultApprovers{f.Store.Approvers(), f} }
func (f *faultStore) Approvals() store.ApprovalRepo { return faultApprovals{f.Store.Approvals(), f} }

type faultUsers struct {
	store.UserRepo
	f *faultStore
}

func (u faultUsers) GetByID(ctx context.Context, id string) (store.User, error) {
	if err := u.f.fault("users"); err != nil {
		return store.User{}, err
	}
	return u.UserRepo.GetByID(ctx, id)
}

type faultSessions struct {
	store.SessionRepo
	f *faultStore
}

func (s faultSessions) GetByID(ctx context.Context, id string) (store.Session, error) {
	if err := s.f.fault("sessions"); err != nil {
		return store.Session{}, err
	}
	return s.SessionRepo.GetByID(ctx, id)
}

type faultDevices struct {
	store.DeviceRepo
	f *faultStore
}

func (d faultDevices) GetByID(ctx context.Context, id string) (store.Device, error) {
	if err := d.f.fault("devices"); err != nil {
		return store.Device{}, err
	}
	return d.DeviceRepo.GetByID(ctx, id)
}

type faultApprovers struct {
	store.ApproverRepo
	f *faultStore
}

func (a faultApprovers) GetDevice(ctx context.Context, id string) (store.ApproverDevice, error) {
	if err := a.f.fault("approvers"); err != nil {
		return store.ApproverDevice{}, err
	}
	return a.ApproverRepo.GetDevice(ctx, id)
}

// testAppFault boots a full standalone strazad over a fault-injecting store.
func testAppFault(t *testing.T) (*App, string, *faultStore) {
	t.Helper()
	return testAppFaultLog(t, logging.New(config.Log{Level: "error", Format: "json"}, io.Discard))
}

// testAppFaultLog is testAppFault with the caller's logger (the capture
// harness in logcapture_test.go reads the records back).
func testAppFaultLog(t *testing.T, log *slog.Logger) (*App, string, *faultStore) {
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
	fs := &faultStore{Store: raw, faults: map[string]error{}}
	ctx, cancel := context.WithCancel(context.Background())
	app, err := build(ctx, cfg, log, fs)
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
	return app, base, fs
}

// callJSON posts (or gets) and returns status, headers, and the decoded body.
func callJSON(t *testing.T, method, url, bearer string, body any) (int, http.Header, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, resp.Header, out
}

// pgDown is the shape of a Postgres outage: Postgres answering every connect with
// SQLSTATE 57P03 while it starts up (or is out of disk).
var pgDown = &pgconn.PgError{Severity: "FATAL", Code: "57P03", Message: "the database system is starting up"}

// requireOutage asserts the honest outage answer: 503, the generic body, a
// Retry-After, and not one byte of the cause in the body.
func requireOutage(t *testing.T, lane string, code int, hdr http.Header, out map[string]any) {
	t.Helper()
	if code != http.StatusServiceUnavailable {
		t.Fatalf("%s: status = %d %v, want 503", lane, code, out)
	}
	msg, _ := out["error"].(string)
	if msg != loginOutageBody {
		t.Fatalf("%s: body = %q, want %q", lane, msg, loginOutageBody)
	}
	for _, leak := range []string{"57P03", "starting up", "SQLSTATE", "boom"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("%s: body leaks the cause (%q): %q", lane, leak, msg)
		}
	}
	if hdr.Get("Retry-After") == "" {
		t.Fatalf("%s: no Retry-After on a 503", lane)
	}
}

// TestLoginLanesAnswerOutageHonestly pins that a
// store outage on enroll or any checkin lane (fresh login, device credential,
// session refresh, device check) answers 503 "temporarily unavailable", never
// a 401/403 that blames the credential (and that would make the client daemon
// drop its session or the approver app wipe its key). Credential refusals on
// a healthy store keep today's 401 as the control.
func TestLoginLanesAnswerOutageHonestly(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	device := map[string]string{"name": "kim-laptop", "platform": "linux", "fingerprint": "sha256:fp-outage"}
	harness := map[string]string{"name": "claude-code", "version": "1.0"}

	// Healthy baseline: enroll, start a session by device token, keep both
	// credentials for the armed lanes below.
	code, _, enroll := callJSON(t, "POST", base+"/v1/enroll", "", map[string]any{"id_token": idToken, "device": device})
	if code != http.StatusOK {
		t.Fatalf("baseline enroll = %d %v", code, enroll)
	}
	deviceToken, _ := enroll["device_token"].(string)
	deviceID, _ := enroll["device_id"].(string)
	code, _, ses := callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"device_token": deviceToken, "harness": harness})
	if code != http.StatusOK {
		t.Fatalf("baseline device checkin = %d %v", code, ses)
	}
	sessionToken, _ := ses["session_token"].(string)

	lanes := []struct {
		name string
		repo string
		call func() (int, http.Header, map[string]any)
	}{
		{"enroll", "users", func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/enroll", "", map[string]any{"id_token": idToken, "device": device})
		}},
		{"exchange id_token", "users", func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"id_token": idToken, "harness": harness})
		}},
		{"exchange id_token device lookup", "devices", func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"id_token": idToken, "device_id": deviceID, "harness": harness})
		}},
		{"device token user lookup", "users", func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"device_token": deviceToken, "harness": harness})
		}},
		{"session refresh session lookup", "sessions", func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"session_token": sessionToken, "harness": harness})
		}},
		{"session refresh user lookup", "users", func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"session_token": sessionToken, "harness": harness})
		}},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			fs.arm(lane.repo, pgDown)
			defer fs.disarm()
			code, hdr, out := lane.call()
			requireOutage(t, lane.name, code, hdr, out)
		})
	}

	// Control: the same lanes on a healthy store refuse a bad credential
	// with 401 and today's copy, so the classifier never softens a refusal.
	t.Run("control bad id_token stays 401", func(t *testing.T) {
		code, _, out := callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"id_token": "not-a-token", "harness": harness})
		if code != http.StatusUnauthorized || out["error"] != "login not accepted" {
			t.Fatalf("bad id_token = %d %v, want 401 login not accepted", code, out)
		}
		code, _, out = callJSON(t, "POST", base+"/v1/enroll", "", map[string]any{"id_token": "not-a-token", "device": device})
		if code != http.StatusUnauthorized || !strings.HasPrefix(out["error"].(string), "login not accepted") {
			t.Fatalf("bad enroll = %d %v, want 401 login not accepted", code, out)
		}
	})
	t.Run("control healthy lanes still 200", func(t *testing.T) {
		if code, _, out := callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"device_token": deviceToken, "harness": harness}); code != http.StatusOK {
			t.Fatalf("healthy device checkin = %d %v", code, out)
		}
	})

	// Unknown error shapes: the store ping decides. Ping failing = outage
	// (503); ping fine = today's refusal (401), the safe default.
	t.Run("opaque error with store down is 503", func(t *testing.T) {
		fs.arm("users", errors.New("boom"))
		fs.setPing(errors.New("boom"))
		defer fs.disarm()
		code, hdr, out := callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"id_token": idToken, "harness": harness})
		requireOutage(t, "opaque+ping-down", code, hdr, out)
	})
	t.Run("opaque error with store up stays 401", func(t *testing.T) {
		fs.arm("users", errors.New("boom"))
		defer fs.disarm()
		code, _, out := callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"id_token": idToken, "harness": harness})
		if code != http.StatusUnauthorized {
			t.Fatalf("opaque+ping-ok = %d %v, want 401 (safe default)", code, out)
		}
		if msg, _ := out["error"].(string); strings.Contains(msg, "boom") {
			t.Fatalf("401 body leaks the cause: %q", msg)
		}
	})

	// The approver lane: a store outage under an approver bearer must not
	// render as device_revoked or user_inactive, both of which make the
	// approver app destroy its local credential.
	t.Run("approver lane outage is 503 not device_revoked", func(t *testing.T) {
		_, _, tok := enrollApprover(t, app, user.ID)
		for _, repo := range []string{"approvers", "users"} {
			fs.arm(repo, pgDown)
			code, hdr, out := callJSON(t, "GET", base+"/v1/approver/pending?scope=decidable", tok, nil)
			fs.disarm()
			requireOutage(t, "approver "+repo, code, hdr, out)
			if out["code"] != "service_unavailable" {
				t.Fatalf("approver %s: code = %v, want service_unavailable", repo, out["code"])
			}
		}
		// Control: healthy bearer lists fine.
		if code, _, out := callJSON(t, "GET", base+"/v1/approver/pending?scope=decidable", tok, nil); code != http.StatusOK {
			t.Fatalf("healthy approver pending = %d %v", code, out)
		}
	})
}

// faultApprovals faults the first store call of approval.Service.Request (the
// dedupe lookup) so a hook-lane approve fails closed through the REAL service
// without closing the whole store (background runners would then log too).
type faultApprovals struct {
	store.ApprovalRepo
	f *faultStore
}

func (a faultApprovals) FindPendingByKey(ctx context.Context, sessionID, ruleID, argvHash string) (store.Approval, error) {
	if err := a.f.fault("approvals"); err != nil {
		return store.Approval{}, err
	}
	return a.ApprovalRepo.FindPendingByKey(ctx, sessionID, ruleID, argvHash)
}
