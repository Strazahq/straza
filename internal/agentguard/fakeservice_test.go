package agentguard

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/policy"
)

// fakeService is a test double of `straza service`: the local API on a unix
// socket in a fresh STRAZA_HOME, in user mode, the way the journey matrix
// runs one service per identity. It decides tool calls with a real policy
// engine and records what the thin clients sent. Fields set before the
// first request steer its answers.
type fakeService struct {
	home string

	// engine decides tool.pre and permission.request; nil allows them.
	engine *policy.Engine
	// capture is the directive every decide answer carries.
	capture policy.CaptureDirective
	// journal, when set, is the journal every decide answer carries.
	journal *localapi.Journal
	// decision, when set, is the decision every decide answer carries.
	decision *policy.Decision
	// session is the answer to a session start.
	session localapi.SessionAnswer
	// status is the answer to a status request.
	status localapi.Status
	// refuse, when set, is the sentence every route answers with 403.
	refuse string
	// enroll is what the enroll route streams, one JSON object per line.
	enroll []localapi.EnrollEvent
	// keyHeld makes keygen without Force refuse, as when a key exists.
	keyHeld bool
	// mcp serves /local/v1/mcp and below, with the route prefix removed.
	mcp http.Handler
	// connect serves /local/v1/connect and below as strazad's /v1/connect.
	connect http.Handler

	mu       sync.Mutex
	starts   []localapi.SessionRequest
	decides  []localapi.DecideRequest
	captures []localapi.CaptureRequest
	enrolls  []localapi.EnrollRequest
	proxied  []http.Header
}

// newFakeService starts a fake service and points STRAZA_HOME at its home,
// so the thin client's store and its service client both use it. The home
// sits under the system temp directory, because a unix socket path must stay
// under about 100 bytes and t.TempDir names the test.
func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	home, err := os.MkdirTemp("", "straza-svc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	f := &fakeService{
		home: home,
		session: localapi.SessionAnswer{
			SessionID: "s-fake", User: "alice", Roles: []string{"dev"},
			Attestation: "advisory", SnapshotID: "snap-fake", ServerURL: "https://straza.example",
		},
		status: localapi.Status{Version: "test", UserMode: true, Custody: localapi.CustodyUser},
	}
	socket, _, _ := localapi.Paths(home)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(f.routes())
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	t.Setenv("STRAZA_HOME", home)
	return f
}

// noService points STRAZA_HOME at a home where no service listens.
func noService(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	return home
}

func (f *fakeService) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(localapi.RouteSessions, func(w http.ResponseWriter, r *http.Request) {
		var req localapi.SessionRequest
		if !f.read(w, r, &req) {
			return
		}
		f.mu.Lock()
		f.starts = append(f.starts, req)
		f.mu.Unlock()
		_ = writeJSON(w, f.session)
	})
	mux.HandleFunc(localapi.RouteDecide, func(w http.ResponseWriter, r *http.Request) {
		var req localapi.DecideRequest
		if !f.read(w, r, &req) {
			return
		}
		f.mu.Lock()
		f.decides = append(f.decides, req)
		f.mu.Unlock()
		d := policy.Decision{Effect: policy.EffectAllow, Default: true}
		enforced := req.Event.Kind == policy.EventToolPre || req.Event.Kind == policy.EventPermissionRequest
		if enforced && f.engine != nil {
			d = f.engine.Evaluate(req.Event, policy.Subject{User: f.session.User, Roles: f.session.Roles})
		}
		if f.decision != nil {
			d = *f.decision
		}
		j := localapi.Journal{Snapshot: f.session.SnapshotID, Spooled: enforced}
		if f.journal != nil {
			j = *f.journal
		}
		_ = writeJSON(w, localapi.DecideAnswer{Decision: d, Capture: f.capture, Journal: j})
	})
	mux.HandleFunc(localapi.RouteCapture, func(w http.ResponseWriter, r *http.Request) {
		var req localapi.CaptureRequest
		if !f.read(w, r, &req) {
			return
		}
		f.mu.Lock()
		f.captures = append(f.captures, req)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(localapi.RouteEnroll, func(w http.ResponseWriter, r *http.Request) {
		var req localapi.EnrollRequest
		if !f.read(w, r, &req) {
			return
		}
		f.mu.Lock()
		f.enrolls = append(f.enrolls, req)
		f.mu.Unlock()
		enc := json.NewEncoder(w)
		for _, ev := range f.enroll {
			_ = enc.Encode(ev)
		}
	})
	mux.HandleFunc(localapi.RouteKeygen, func(w http.ResponseWriter, r *http.Request) {
		var req localapi.KeygenRequest
		if !f.read(w, r, &req) {
			return
		}
		if f.keyHeld && !req.Force {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(localapi.Refusal{Error: "An agent key already exists for you. Pass --force to replace it."})
			return
		}
		_ = writeJSON(w, localapi.KeygenAnswer{Username: req.Username, PublicKey: "cHVibGljLWtleQ=="})
	})
	mux.HandleFunc(localapi.RouteStatus, func(w http.ResponseWriter, r *http.Request) {
		if f.refused(w) {
			return
		}
		_ = writeJSON(w, f.status)
	})
	// proxy records the headers of a proxied request and hands it on with
	// trim removed from its path.
	proxy := func(next func() http.Handler, trim string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if f.refused(w) {
				return
			}
			f.mu.Lock()
			f.proxied = append(f.proxied, r.Header.Clone())
			h := next()
			f.mu.Unlock()
			if h == nil {
				http.NotFound(w, r)
				return
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, trim)
			r2.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, trim)
			h.ServeHTTP(w, r2)
		}
	}
	mcp := proxy(func() http.Handler { return f.mcp }, localapi.Prefix)
	mux.HandleFunc(localapi.RouteMCP, mcp)
	mux.HandleFunc(localapi.RouteMCP+"/", mcp)
	connect := proxy(func() http.Handler { return f.connect }, "/local")
	mux.HandleFunc(localapi.Prefix+"/connect", connect)
	mux.HandleFunc(localapi.RouteConnect, connect)
	return mux
}

// read decodes a request body after the refusal check.
func (f *fakeService) read(w http.ResponseWriter, r *http.Request, v any) bool {
	if f.refused(w) {
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (f *fakeService) refused(w http.ResponseWriter) bool {
	if f.refuse == "" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(localapi.Refusal{Error: f.refuse})
	return true
}

// decided returns the decide requests the service has seen.
func (f *fakeService) decided() []localapi.DecideRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]localapi.DecideRequest(nil), f.decides...)
}

// captured returns the capture requests the service has seen.
func (f *fakeService) captured() []localapi.CaptureRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]localapi.CaptureRequest(nil), f.captures...)
}

// started returns the session starts the service has seen.
func (f *fakeService) started() []localapi.SessionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]localapi.SessionRequest(nil), f.starts...)
}

// store opens the thin client's store in the fake service's home.
func (f *fakeService) store(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.root != f.home {
		t.Fatalf("store root %s is not the fake service's home %s", store.root, f.home)
	}
	return store
}

// writeUserConfig writes the user's config.yaml, which only an older client
// or a person writes now; the thin client still reads its trace field.
func writeUserConfig(t *testing.T, store *Store, c Config) {
	t.Helper()
	raw, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.configPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// compilePolicy builds an engine from one PolicySet document.
func compilePolicy(t *testing.T, doc string) *policy.Engine {
	t.Helper()
	docs, err := policy.ParseAll([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.NewEngine(docs, policy.EffectAllow)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}
