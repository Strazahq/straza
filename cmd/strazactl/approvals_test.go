package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// TestHonestyLine pins the two CLI honesty strings (kept in step with
// approval.HonestyLine).
func TestHonestyLine(t *testing.T) {
	if got := honestyLine("call", "abc123abc123"); got != "preview only; this approval covers the exact call (sha256:abc123abc123)" {
		t.Errorf("call: %q", got)
	}
	if got := honestyLine("tool_identity", "abc123abc123"); got != "preview only; this approval covers tool identity (sha256:abc123abc123)" {
		t.Errorf("tool_identity: %q", got)
	}
}

// TestPrintApprovalsWide: the detail view shows the preview and honesty line when
// present, and omits both when there is no preview (the pairing invariant).
func TestPrintApprovalsWide(t *testing.T) {
	var buf bytes.Buffer
	recs := []ctl.ApprovalInfo{
		{
			ID: "a1", State: "pending", Username: "nova", Summary: "mcp.call midpoint:disable_user",
			ArgsPreview: "{\n  \"user\": \"nova\"\n}", ArgsTruncated: true, ArgsBytes: 4096,
			ArgvHashPrefix: "0123456789ab", BindingScope: "tool_identity", Justification: "offboard",
		},
		{ID: "a2", State: "approved", Username: "kim", Summary: "shell.exec: ls"},
	}
	printApprovalsWide(&buf, recs)
	out := buf.String()
	if !strings.Contains(out, "parameters (preview)") || !strings.Contains(out, "nova") {
		t.Errorf("preview not rendered:\n%s", out)
	}
	if !strings.Contains(out, "preview truncated; 4096 bytes") {
		t.Errorf("truncation footnote missing:\n%s", out)
	}
	if !strings.Contains(out, "preview only; this approval covers tool identity (sha256:0123456789ab)") {
		t.Errorf("honesty line missing:\n%s", out)
	}
	// The no-preview record must not emit a params block or honesty line.
	a2 := out[strings.Index(out, "a2"):]
	if strings.Contains(a2, "parameters (preview)") || strings.Contains(a2, "preview only;") {
		t.Errorf("no-preview record leaked preview affordances:\n%s", a2)
	}
}

// TestDecideVerbsAreRetired pins approve and deny: whatever the old verbs
// took, they send nothing, need no token or server, say that a decision must
// name a person and where a person decides, and exit 2 as a retired verb
// does.
func TestDecideVerbsAreRetired(t *testing.T) {
	const want = "strazactl no longer decides requests, because it runs on an admin API token, which is not a person, " +
		"and a decision must name the person who made it. Decide in the console under Approvals, on the self-service page, or on your phone"
	for _, args := range [][]string{
		{"approvals", "approve", "ap1"},
		{"approvals", "approve", "ap1", "--reason", "Change window CR-2041 is open"},
		{"approvals", "deny", "ap1"},
		{"approvals", "deny"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv(apiTokenEnv, "")
			root := newRootCmd(noCreds(t))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)
			cmd, err := root.ExecuteC()
			if err == nil || err.Error() != want {
				t.Fatalf("err\n got %v\nwant %s", err, want)
			}
			if code := exitCode(cmd, err); code != 2 || out.Len() != 0 {
				t.Errorf("exit %d, printed %q, want 2 and nothing printed", code, out.String())
			}
		})
	}
}

// TestEnrollTokenIsRetired pins enroll-token: whatever the old verb took, it
// sends nothing even with a server and a token at hand, says that only the
// person enrolls their own phone and how they do it, exits 2 as a retired
// verb does, and is off the help.
func TestEnrollTokenIsRetired(t *testing.T) {
	const want = "strazactl no longer enrolls approver phones, because a phone or browser that decides as a person " +
		"is enrolled only by whoever is signed in as that person on the self-service page. Give the person the role " +
		"straza-enroll-mobile in your identity manager, and they choose Add a phone on the self-service page"
	for _, args := range [][]string{
		{"approvals", "enroll-token", "bob"},
		{"approvals", "enroll-token", "--user-id", "usr_01b"},
		{"approvals", "enroll-token", "--help"},
		{"approvals", "enroll-token"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("sent %s %s, want nothing sent", r.Method, r.URL.RequestURI())
			}))
			t.Cleanup(srv.Close)
			root := newRootCmd(onToken(t, srv.URL))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)
			cmd, err := root.ExecuteC()
			if err == nil || err.Error() != want {
				t.Fatalf("err\n got %v\nwant %s", err, want)
			}
			if code := exitCode(cmd, err); code != 2 || out.Len() != 0 {
				t.Errorf("exit %d, printed %q, want 2 and nothing printed", code, out.String())
			}
		})
	}

	t.Setenv("STRAZA_SERVER", "")
	help, _, err := runCLI(t, noCreds(t), "approvals", "--help")
	if err != nil {
		t.Fatalf("approvals --help: %v", err)
	}
	if strings.Contains(help, "enroll-token") || strings.Contains(help, "enroll token") {
		t.Errorf("approvals help still offers enrolling:\n%s", help)
	}
}

// TestApprovalsListSaysWhereToDecide pins the line approvals list ends with
// on stderr: the console page and the self-service tab at the server, and
// the phone. The table stays on stdout for a script.
func TestApprovalsListSaysWhereToDecide(t *testing.T) {
	for _, args := range [][]string{{"approvals", "list"}, {"approvals", "list", "--wide"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RequestURI() != "/v1/admin/approvals?state=pending" || r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Errorf("request %s %s with %q", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"approvals":[]}`))
			}))
			t.Cleanup(srv.Close)
			_, stderr, err := runCLI(t, onToken(t, srv.URL), args...)
			if err != nil {
				t.Fatalf("approvals list: %v", err)
			}
			want := "A person decides these requests in the console under Approvals at " + srv.URL + "/console/approvals, " +
				"on the Requests tab of the self-service page at " + srv.URL + "/self-service/requests, or on their phone.\n"
			if stderr != want {
				t.Errorf("stderr\n got %q\nwant %q", stderr, want)
			}
		})
	}
}
