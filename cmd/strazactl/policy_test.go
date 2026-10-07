package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// policyServer serves the policy-list envelope (summary-only items,
// total/limit/offset, role facet). `policy list` prints NAME/PRIORITY/
// STATUS/ID from the summary items.
func policyServer(t *testing.T, listBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(listBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPolicyListDecodesEnvelope(t *testing.T) {
	srv := policyServer(t, `{"items":[`+
		`{"id":"ps-1","name":"dev-guardrails","priority":10,"status":"active",`+
		`"updated_at":"2026-08-24T10:00:00Z","drift":true,`+
		`"summary":{"name":"dev-guardrails","priority":10,"rules":12,"description":"working set",`+
		`"postures":{"deny":2,"hold":4,"ticket":2,"allow":4},"matchRoles":["dev"],`+
		`"lanes":{"mcp":{"deny":2,"allow":4},"shell":{"hold":4,"ticket":2}}}},`+
		`{"id":"ps-2","name":"org-baseline","priority":100,"status":"draft","updated_at":"2026-08-01T09:00:00Z"}`+
		`],"total":2,"limit":0,"offset":0,`+
		`"roles":[{"role":"*","sets":1,"postures":{"deny":3}},{"role":"dev","sets":1,"postures":{"deny":2,"hold":4,"ticket":2,"allow":4}}]}`)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "policy", "list")
	if err != nil {
		t.Fatalf("policy list: %v", err)
	}
	for _, want := range []string{
		"NAME", "PRIORITY", "STATUS", "ID",
		"dev-guardrails", "10", "Live", "ps-1",
		"org-baseline", "100", "Off", "ps-2",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

func TestPolicyListEmptyEnvelope(t *testing.T) {
	srv := policyServer(t, `{"items":[],"total":0,"limit":0,"offset":0,"roles":[]}`)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "policy", "list")
	if err != nil {
		t.Fatalf("policy list (empty): %v", err)
	}
	if !strings.Contains(stdout, "NAME") || strings.Contains(stdout, "ps-") {
		t.Errorf("empty list should print the header alone:\n%s", stdout)
	}
}

// A pre-0.78.0 strazad answers a bare array; the client refuses with the
// skew named instead of surfacing a raw JSON decode error.
func TestPolicyListPre078ServerNamesTheSkew(t *testing.T) {
	srv := policyServer(t, `[{"id":"ps-1","name":"old","priority":1,"status":"draft","yaml":"..."}]`)
	t.Setenv("STRAZA_SERVER", "")
	_, _, err := runCLI(t, writeCreds(t, srv.URL), "policy", "list")
	if err == nil || !strings.Contains(err.Error(), "0.78.0") {
		t.Fatalf("err = %v, want the version-skew hint naming 0.78.0", err)
	}
}

// TestPolicyValidateCompilesTheModule pins that validate compiles a set's
// Rego module, so a module activation would refuse is refused at validate.
func TestPolicyValidateCompilesTheModule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fetcher.yaml")
	doc := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: { name: fetcher }\nspec:\n" +
		"  rules:\n    - { id: r1, tools: [shell.exec], effect: deny, reason: x }\n" +
		"  escape:\n    rego: |\n      package straza.ext\n\n      deny contains msg if { msg := http.send({\"method\": \"get\", \"url\": \"http://a/\"}).raw_body }\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	want := path + ": policy: set fetcher: its Rego module calls http.send on line 3, and Straza refuses that built-in " +
		"because a policy module must not reach the network, the file system or the process environment. Remove the call from the module"
	t.Setenv("STRAZA_SERVER", "")
	if _, _, err := runCLI(t, noCreds(t), "policy", "validate", "-f", path); err == nil || err.Error() != want {
		t.Errorf("validate error\n got %v\nwant %s", err, want)
	}
}

// TestPolicyHelpWords pins the policy help sentences. A set that is off
// reads off, because draft names a config draft only, and the help names no
// draft in the singular. validate points at drafts check for the server's
// checks.
func TestPolicyHelpWords(t *testing.T) {
	draftWord := regexp.MustCompile(`(?i)\bdraft\b`)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"deactivate in the list", []string{"policy", "--help"}, "Turn a PolicySet off and recompile and distribute the snapshot"},
		{"delete in the list", []string{"policy", "--help"}, "Delete a PolicySet that is off (turn a live one off first)"},
		{"deactivate", []string{"policy", "deactivate", "--help"}, "Turn a PolicySet off and recompile and distribute the snapshot"},
		{"simulate's file", []string{"policy", "simulate", "--help"}, "PolicySet YAML to overlay on the live policies"},
		{"simulate's overlay", []string{"policy", "simulate", "--help"}, "optionally with a PolicySet from a local file overlaid in place of its"},
		{"validate", []string{"policy", "validate", "--help"}, "`strazactl drafts check -f` runs the server's checks against live state."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := runCLI(t, noCreds(t), tc.args...)
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Errorf("help missing %q:\n%s", tc.want, stdout)
			}
			if m := draftWord.FindString(stdout); m != "" {
				t.Errorf("help still says %q:\n%s", m, stdout)
			}
		})
	}
}

// TestPolicyActivateLinesSayWhetherItChanged pins the lines of activate and
// deactivate per answer. A call that changed the live state prints the
// landed line, and a call that changed nothing says the set is already in
// that state at the running snapshot. An answer without the changed field,
// from a server that predates it, prints the landed line. A set that is off
// reads off, and the wire still sends the status draft.
func TestPolicyActivateLinesSayWhetherItChanged(t *testing.T) {
	tests := []struct {
		name   string
		verb   string
		answer string
		sent   string
		want   string
	}{
		{"an activate that published", "activate", `{"status":"active","snapshot":"snap-9","changed":true}`,
			`{"status":"active"}`, "published set-a, it is live now; new snapshot snap-9\n"},
		{"an activate that changed nothing", "activate", `{"status":"active","snapshot":"snap-8","changed":false}`,
			`{"status":"active"}`, "set-a is already live at snapshot snap-8, so nothing changed.\n"},
		{"an activate on a server without the field", "activate", `{"status":"active","snapshot":"snap-9"}`,
			`{"status":"active"}`, "published set-a, it is live now; new snapshot snap-9\n"},
		{"a deactivate that turned the set off", "deactivate", `{"status":"draft","snapshot":"snap-9","changed":true}`,
			`{"status":"draft"}`, "turned set-a off; new snapshot snap-9\n"},
		{"a deactivate that changed nothing", "deactivate", `{"status":"draft","snapshot":"snap-8","changed":false}`,
			`{"status":"draft"}`, "set-a is already off at snapshot snap-8, so nothing changed.\n"},
		{"a deactivate on a server without the field", "deactivate", `{"snapshot":"snap-9"}`,
			`{"status":"draft"}`, "turned set-a off; new snapshot snap-9\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body string
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
			})
			mux.HandleFunc("POST /v1/admin/policies/set-a/activate", func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				body = string(raw)
				_, _ = w.Write([]byte(tc.answer))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "policy", tc.verb, "set-a")
			if err != nil {
				t.Fatalf("policy %s: %v", tc.verb, err)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
			if body != tc.sent {
				t.Errorf("body = %s, want %s", body, tc.sent)
			}
		})
	}
}

// TestPolicyValidateRoleSelectors pins that validate refuses a set whose
// match.roles names a reserved Straza role with activation's own sentence,
// and that a set naming an application role, or a name in other capitals
// that no role has, passes with the note that says what validate checked
// and quotes a path a shell would split (positive control).
func TestPolicyValidateRoleSelectors(t *testing.T) {
	tests := []struct {
		name    string
		roles   string
		file    string
		wantErr string // after "<file>: "; empty for a pass
	}{
		{"a Straza role", "[straza-admin]", "guard.yaml",
			`PolicySet "guard": match.roles names "straza-admin" (straza role): a Straza role governs Straza itself and never matches sessions`},
		{"an application role", "[dev]", "guard.yaml", ""},
		{"a name in other capitals, which no role has", "[dev, Straza-Admin]", "guard.yaml", ""},
		{"a path with a space", "[dev]", "my guard.yaml", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.file)
			doc := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: { name: guard }\nspec:\n" +
				"  match: { roles: " + tc.roles + " }\n" +
				"  rules:\n    - { id: r1, tools: [shell.exec], effect: deny, reason: x }\n"
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, noCreds(t), "policy", "validate", "-f", path)
			if tc.wantErr != "" {
				if want := path + ": " + tc.wantErr; err == nil || err.Error() != want {
					t.Fatalf("validate error\n got %v\nwant %s", err, want)
				}
				if strings.Contains(stdout, "OK") {
					t.Errorf("a refused set printed OK:\n%s", stdout)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			arg := path
			if strings.Contains(path, " ") {
				arg = "'" + path + "'"
			}
			want := path + `: PolicySet "guard" OK (1 rules, priority 0)` + "\n" +
				"note: validate refuses only the roles the product reserves, whose names start with straza- or mcp-admin-, in match.roles, " +
				"and any of them but straza-admin in approve.roles. " +
				"The server judges every other role a set names, in match.roles and approve.roles, and strazactl drafts check -f " + arg +
				" runs those checks against live state.\n"
			if stdout != want {
				t.Errorf("stdout\n got %q\nwant %q", stdout, want)
			}
		})
	}
}
