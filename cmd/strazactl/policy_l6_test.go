package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPolicyValidateApproveRoles pins that validate refuses a set whose
// approve.roles names a reserved Straza role other than straza-admin, with
// activation's own sentence for each such name, and reports it before a
// match.roles refusal, in activation's order. A server's admin role, named
// mcp-admin-<server>, is refused in approve.roles and in match.roles with
// activation's sentences. straza-admin, a role validate cannot judge
// offline, and a name in other capitals, which no role has, pass with the
// note that says what validate checked (positive controls).
func TestPolicyValidateApproveRoles(t *testing.T) {
	refusal := func(role string) string {
		return `rule "r1": approve.roles names "` + role + `" (straza role): only approver roles or straza-admin may decide. ` +
			"Create one (strazactl roles create <name> --kind approver), assign it in your identity manager, then name it here"
	}
	tests := []struct {
		name    string
		match   string
		approve string
		wantErr string // after "<file>: PolicySet "guard": "; empty for a pass
	}{
		{"the global MCP admin role", "[dev]", "[straza-global-mcp-admin]", refusal("straza-global-mcp-admin")},
		{"the drafting role beside straza-admin", "[dev]", "[straza-admin, straza-draft-config]", refusal("straza-draft-config")},
		{"both enrollment roles, each named", "[dev]", "[straza-enroll-mobile, straza-enroll-browser]",
			refusal("straza-enroll-mobile") + "; " + refusal("straza-enroll-browser")},
		{"a reserved decider and a reserved selector, the decider first", "[straza-admin]", "[straza-global-mcp-admin]",
			refusal("straza-global-mcp-admin")},
		{"a server's admin role among the deciders", "[dev]", "[mcp-admin-echoapp]", refusal("mcp-admin-echoapp")},
		{"a server's admin role as the selector", "[mcp-admin-echoapp]", "[sec-approvers]",
			`match.roles names "mcp-admin-echoapp" (straza role): a Straza role governs Straza itself and never matches sessions`},
		{"straza-admin", "[dev]", "[straza-admin]", ""},
		{"a role the server judges", "[dev]", "[sec-approvers]", ""},
		{"a name in other capitals, which no role has", "[dev]", "[sec-approvers, Straza-Global-MCP-Admin]", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "guard.yaml")
			doc := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: { name: guard }\nspec:\n" +
				"  match: { roles: " + tc.match + " }\n" +
				"  rules:\n    - { id: r1, tools: [shell.exec], effect: allow, mode: approve, approve: { roles: " + tc.approve + " } }\n"
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, noCreds(t), "policy", "validate", "-f", path)
			if tc.wantErr != "" {
				if want := path + `: PolicySet "guard": ` + tc.wantErr; err == nil || err.Error() != want {
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
			want := path + `: PolicySet "guard" OK (1 rules, priority 0)` + "\n" +
				"note: validate refuses only the roles the product reserves, whose names start with straza- or mcp-admin-, in match.roles, " +
				"and any of them but straza-admin in approve.roles. " +
				"The server judges every other role a set names, in match.roles and approve.roles, and strazactl drafts check -f " + path +
				" runs those checks against live state.\n"
			if stdout != want {
				t.Errorf("stdout\n got %q\nwant %q", stdout, want)
			}
		})
	}
}
