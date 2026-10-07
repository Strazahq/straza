package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// simCmd builds the simulate command against a client factory, capturing
// stdout. The command is exercised directly, outside the policy command that
// adds it; flag validation and rendering are what this file pins.
func simCmd(client func() *ctl.Client, args ...string) (string, error) {
	cmd := policySimulateCmd(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestPolicySimulateFlagValidation: subject and event-json rules fail loud
// BEFORE any request is made.
func TestPolicySimulateFlagValidation(t *testing.T) {
	calls := 0
	client := func() *ctl.Client { calls++; return nil }
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"neither subject flag", []string{"--tool", "shell.exec"},
			"exactly one of --user or --roles is required"},
		{"both subject flags", []string{"--user", "bob", "--roles", "dev"},
			"exactly one of --user or --roles is required"},
		{"event-json with per-field flag", []string{"--user", "bob", "--event-json", "e.json", "--tool", "shell.exec"},
			"--event-json cannot be combined with the per-field event flags (--event, --tool, --command, --path, --app, --tool-name)"},
		{"bad attestation", []string{"--user", "bob", "--attestation", "sworn"},
			"--attestation must be none, advisory, or managed"},
		{"stray operand", []string{"bob", "--roles", "dev"},
			`unknown command "bob" for "simulate"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := simCmd(client, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if calls != 0 {
		t.Errorf("client constructed %d times before validation passed", calls)
	}
}

// simServer fakes the two endpoints simulate touches and records the
// simulate request body.
type simServer struct {
	mu       sync.Mutex
	simBody  []byte
	simResp  string
	overview string // empty = 500 (the degrade case)
}

func (s *simServer) start(t *testing.T) (*httptest.Server, func() *ctl.Client) {
	t.Helper()
	mux := http.NewServeMux()
	// The bare test token always reads as expiring, so the client refreshes
	// first: answer the checkin like the sibling fakes do.
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("/v1/admin/policies/simulate", func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		s.mu.Lock()
		s.simBody = body.Bytes()
		resp := s.simResp
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	})
	mux.HandleFunc("/v1/admin/overview", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		ov := s.overview
		s.mu.Unlock()
		if ov == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ov))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	creds := writeCreds(t, srv.URL)
	return srv, func() *ctl.Client {
		c := ctl.NewClient(srv.URL)
		c.CredsPath = creds
		return c
	}
}

// TestPolicySimulateEndToEnd drives the command against a canned server and
// pins the full output, plus the wire body built from the flags.
func TestPolicySimulateEndToEnd(t *testing.T) {
	fake := &simServer{
		simResp: `{"active":{"effect":"deny","ruleId":"no-rm-rf","setName":"dev-guardrails",` +
			`"reason":"Straza: destructive command blocked for role dev"},` +
			`"subject":{"user":"bob","roles":["dev"],"attestation":""},` +
			`"snapshot":"4f2a9c01d7e6b3a8"}`,
		overview: `{"profile":"enterprise","snapshot_id":"4f2a9c01d7e6b3a8"}`,
	}
	_, client := fake.start(t)
	out, err := simCmd(client, "--user", "bob", "--tool", "shell.exec", "--command", "rm -rf /tmp/x")
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	want := `This call would be denied.
Decided by rule no-rm-rf in policy dev-guardrails: Straza: destructive command blocked for role dev.
Simulated against the policies live right now, your file included if you passed one. This is today's answer, not a replay of any past decision.

subject   bob · roles dev · attestation none
snapshot  4f2a9c01d7e6b3a8 (live)
wire      effect=deny · ruleId=no-rm-rf · setName=dev-guardrails · snapshot=4f2a9c01
note      this verdict applies once this snapshot reaches the client;
          straza doctor proves a client is governed
`
	if out != want {
		t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	var req map[string]any
	if err := json.Unmarshal(fake.simBody, &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	ev, _ := req["event"].(map[string]any)
	if ev["kind"] != "tool.pre" || ev["tool"] != "shell.exec" || ev["command"] != "rm -rf /tmp/x" {
		t.Errorf("event = %v (the tool.pre default must apply)", ev)
	}
}

// TestPolicySimulateEventJSONAndDraft: --event-json carries the full event
// verbatim and -f ships the draft bytes; overview failure degrades quietly.
func TestPolicySimulateEventJSONAndDraft(t *testing.T) {
	fake := &simServer{
		simResp: `{"active":{"effect":"deny","default":true},` +
			`"draft":{"effect":"deny","ruleId":"no-rm-rf","setName":"dev-guardrails",` +
			`"reason":"Straza: destructive command blocked for role dev"},` +
			`"subject":{"user":"","roles":["dev"],"attestation":"managed"},` +
			`"snapshot":"4f2a9c01d7e6b3a8"}`,
	}
	_, client := fake.start(t)

	dir := t.TempDir()
	evPath := filepath.Join(dir, "event.json")
	if err := os.WriteFile(evPath, []byte(`{"kind":"tool.pre","tool":"file.write","paths":["/etc/passwd"],"workspace":"/w"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(dir, "draft.yaml")
	if err := os.WriteFile(draftPath, []byte("apiVersion: straza.dev/v1beta1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := simCmd(client, "--roles", "dev", "--attestation", "managed",
		"--event-json", evPath, "-f", draftPath)
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(fake.simBody, &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	ev, _ := req["event"].(map[string]any)
	if ev["tool"] != "file.write" || ev["workspace"] != "/w" {
		t.Errorf("event-json fields lost on the wire: %v", ev)
	}
	if req["draft"] != "apiVersion: straza.dev/v1beta1\n" {
		t.Errorf("draft = %v", req["draft"])
	}
	sub, _ := req["subject"].(map[string]any)
	if sub["attestation"] != "managed" {
		t.Errorf("subject = %v", sub)
	}
	// Dual rendering with the overview down: no (live) marker, generic
	// profile wording on the no-rule active row.
	for _, wantLine := range []string{
		"live    DENY   No policy matched this call. Denied by the profile default: no policy allowed this call.",
		"file    DENY   Decided by rule no-rm-rf in policy dev-guardrails: Straza: destructive command blocked for role dev.",
		"live and file agree: DENY",
		"snapshot  4f2a9c01d7e6b3a8\n",
	} {
		if !strings.Contains(out, wantLine) {
			t.Errorf("output missing %q\n--- got ---\n%s", wantLine, out)
		}
	}
	if strings.Contains(out, "(live)") {
		t.Errorf("live marker printed without overview evidence:\n%s", out)
	}
}
