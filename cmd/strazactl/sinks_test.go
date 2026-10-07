package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sinksServer is the admin surface the sinks family walks.
type sinksServer struct {
	mu      sync.Mutex
	replays []string
	bodies  []string
}

func (s *sinksServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/sinks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"elastic","type":"webhook","target":"http://elasticsearch:9200/straza-events/_doc","batch":0,"subjects":["straza.audit.>"],"parked":414,` +
			`"streams":[{"stream":"STRAZA_AUDIT","pending":3,"inflight":2,"delivered":1200,"duplicates":1,"parked":414,"last_error":"sink elastic: endpoint returned HTTP 400","last_error_at":"2026-08-20T10:00:00Z"}]},` +
			`{"name":"archive","type":"file","target":"/var/log/straza/events.jsonl","batch":64,"subjects":["straza.>"],"parked":0,"streams":[{"stream":"STRAZA_AUDIT","delivered":5000},{"stream":"STRAZA_EVENTS","delivered":40}]}]`))
	})
	mux.HandleFunc("POST /v1/admin/sinks/{name}/replay", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.replays = append(s.replays, r.PathValue("name"))
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.PathValue("name") {
		case "elastic":
			_ = json.NewEncoder(w).Encode(map[string]any{"sink": "elastic", "replayed": 400, "remaining": 14, "stopped": "sink elastic: endpoint returned HTTP 400"})
		case "archive":
			_ = json.NewEncoder(w).Encode(map[string]any{"sink": "archive", "replayed": 0, "remaining": 0, "stopped": ""})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such sink: sinks are named in strazad config (sinks[].name)"}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestSinksList pins `strazactl sinks list`: one row per sink with backlog
// (pending + inflight), the parked count flagged as needing a replay, the
// delivered total across streams, and the last failure.
func TestSinksList(t *testing.T) {
	s := &sinksServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, _, err := runCLI(t, credsPath, "sinks", "list")
	if err != nil {
		t.Fatalf("sinks list: %v", err)
	}
	for _, want := range []string{
		"NAME", "TYPE", "TARGET", "BACKLOG", "PARKED", "DELIVERED", "LAST ERROR",
		"elastic", "webhook", "http://elasticsearch:9200/straza-events/_doc", "414 (replay needed)", "1200",
		"2026-08-20T10:00:00Z sink elastic: endpoint returned HTTP 400",
		"archive", "file", "/var/log/straza/events.jsonl", "5040",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	// elastic backlog = pending 3 + inflight 2.
	line := ""
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, "elastic") {
			line = l
		}
	}
	if !strings.Contains(line, "  5  ") && !strings.Contains(line, "\t5\t") {
		t.Errorf("elastic row must show backlog 5:\n%s", line)
	}
}

// TestSinksReplay pins `strazactl sinks replay <name> [--limit N]`: the
// server's counts are printed, a stopped replay exits non-zero with the
// receiver's refusal, and the limit rides the request body.
func TestSinksReplay(t *testing.T) {
	s := &sinksServer{}
	srv := s.start(t)
	t.Setenv("STRAZA_SERVER", "")
	credsPath := writeCreds(t, srv.URL)

	stdout, _, err := runCLI(t, credsPath, "sinks", "replay", "elastic", "--limit", "400")
	if err == nil || !strings.Contains(err.Error(), "replay stopped") || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("stopped replay err = %v, want the refusal", err)
	}
	if !strings.Contains(stdout, "replayed 400 event(s) to sink elastic; 14 remain parked") {
		t.Errorf("stdout = %q", stdout)
	}
	s.mu.Lock()
	replays, bodies := append([]string(nil), s.replays...), append([]string(nil), s.bodies...)
	s.mu.Unlock()
	if len(replays) != 1 || replays[0] != "elastic" || !strings.Contains(bodies[0], `"limit":400`) {
		t.Errorf("server saw %v %v, want one elastic replay with limit 400", replays, bodies)
	}

	stdout, _, err = runCLI(t, credsPath, "sinks", "replay", "archive")
	if err != nil {
		t.Fatalf("clean replay: %v", err)
	}
	if !strings.Contains(stdout, "replayed 0 event(s) to sink archive; 0 remain parked") {
		t.Errorf("stdout = %q", stdout)
	}
	s.mu.Lock()
	last := s.bodies[len(s.bodies)-1]
	s.mu.Unlock()
	if last != "" {
		t.Errorf("no --limit must send no body, got %q", last)
	}

	if _, _, err := runCLI(t, credsPath, "sinks", "replay", "ghost"); err == nil ||
		!strings.Contains(err.Error(), "no such sink") {
		t.Errorf("unknown sink = %v, want the server's 404 reason", err)
	}
}

// TestSinksListLastErrorTime pins the LAST ERROR cell: the newest stream
// error as its time in RFC3339 to the second in UTC, then its text, so an
// error from boot reads as old an hour later. A sink with no error keeps
// the empty cell, and an error with no time prints its text alone.
func TestSinksListLastErrorTime(t *testing.T) {
	tests := []struct {
		name    string
		streams string
		want    string // the row's last cell
	}{
		{"no error", `[{"stream":"STRAZA_AUDIT","delivered":5}]`, "-"},
		{"an error in UTC",
			`[{"stream":"STRAZA_AUDIT","last_error":"sink s: endpoint returned HTTP 400","last_error_at":"2026-08-20T10:00:00Z"}]`,
			"2026-08-20T10:00:00Z sink s: endpoint returned HTTP 400"},
		{"a local time with a fraction",
			`[{"stream":"STRAZA_AUDIT","last_error":"sink s: refused","last_error_at":"2026-08-20T12:00:00.123456789+02:00"}]`,
			"2026-08-20T10:00:00Z sink s: refused"},
		{"the newest of two streams",
			`[{"stream":"STRAZA_AUDIT","last_error":"sink s: newer","last_error_at":"2026-08-20T11:00:00Z"},` +
				`{"stream":"STRAZA_EVENTS","last_error":"sink s: older","last_error_at":"2026-08-20T09:00:00Z"}]`,
			"2026-08-20T11:00:00Z sink s: newer"},
		{"the newest of two streams, listed second",
			`[{"stream":"STRAZA_AUDIT","last_error":"sink s: older","last_error_at":"2026-08-20T09:00:00Z"},` +
				`{"stream":"STRAZA_EVENTS","last_error":"sink s: newer","last_error_at":"2026-08-20T11:00:00Z"}]`,
			"2026-08-20T11:00:00Z sink s: newer"},
		{"an error with no time", `[{"stream":"STRAZA_AUDIT","last_error":"sink s: refused"}]`, "sink s: refused"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
			})
			mux.HandleFunc("GET /v1/admin/sinks", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"name":"s","type":"webhook","target":"http://receiver/in","parked":0,"streams":` + tc.streams + `}]`))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			t.Setenv("STRAZA_SERVER", "")

			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "sinks", "list")
			if err != nil {
				t.Fatalf("sinks list: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(stdout), "\n")
			row := lines[len(lines)-1]
			if !strings.HasPrefix(row, "s ") || !strings.HasSuffix(row, "  "+tc.want) {
				t.Errorf("row = %q, want its last cell %q", row, tc.want)
			}
			if strings.Contains(row, "older") {
				t.Errorf("row shows the older error: %q", row)
			}
		})
	}
}
