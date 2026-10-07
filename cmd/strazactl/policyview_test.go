package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// runPolicyView drives show/diff on their own tree: main's root gains the
// commands at integration (the AddCommand wiring in policy.go), so these
// tests bind the constructors directly.
func runPolicyView(t *testing.T, srvURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	credsPath := writeCreds(t, srvURL)
	client := func() *ctl.Client {
		c := ctl.NewClient(srvURL)
		c.CredsPath = credsPath
		return c
	}
	root := &cobra.Command{Use: "strazactl", SilenceUsage: true, SilenceErrors: true}
	pol := &cobra.Command{Use: "policy"}
	pol.AddCommand(policyShowCmd(client), policyDiffCmd(client))
	root.AddCommand(pol)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// policyDetailServer serves one set on the by-name read (plus the checkin
// lane every call walks with the opaque test token).
func policyDetailServer(t *testing.T, name string, payload map[string]any) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("GET /v1/admin/policies/"+name, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// headerValue reads one key's value out of a show header, whatever column
// width the tabwriter picked for that header's key set.
func headerValue(stderr, key string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, key+" ") {
			return strings.TrimSpace(strings.TrimPrefix(line, key))
		}
	}
	return ""
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

const showYAML = "apiVersion: straza.dev/v1beta1\nkind: PolicySet\n"

// The golden output: the whole story block on stderr, the document alone on
// stdout, byte-exact both.
func TestPolicyShowGoldenHeader(t *testing.T) {
	srv := policyDetailServer(t, "dev-guardrails", map[string]any{
		"id": "ps-1", "name": "dev-guardrails", "priority": 150, "status": "active",
		"yaml": showYAML, "updated_at": "2026-08-24T12:02:31Z",
		"summary": map[string]any{
			"name": "dev-guardrails", "priority": 150, "rules": 12,
			"postures":   map[string]int{"deny": 2, "hold": 4, "ticket": 2, "allow": 4},
			"matchRoles": []string{"dev"}, "capture": "verbatim",
		},
	})
	t.Setenv("STRAZA_SERVER", "")
	stdout, stderr, err := runPolicyView(t, srv.URL, "policy", "show", "dev-guardrails")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if stdout != showYAML {
		t.Errorf("stdout = %q, want the stored YAML verbatim and nothing else", stdout)
	}
	want := strings.Join([]string{
		"name        dev-guardrails",
		"status      Live",
		"priority    150",
		"updated     " + fmtStamp("2026-08-24T12:02:31Z"),
		"applies to  role dev",
		"rules       12 · denied 2 · needs approval 6 (4 hold, 2 ticket) · checked 0 · allowed 4",
		"recording   word for word",
		"",
	}, "\n")
	if stderr != want {
		t.Errorf("stderr:\n%q\nwant:\n%q", stderr, want)
	}
}

// Drift is claimed only on an explicit server true.
func TestPolicyShowDrift(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"id": "ps-1", "name": "ops-freeze", "priority": 10, "status": "active", "yaml": showYAML,
		}
	}
	t.Run("true gets the status word and the note", func(t *testing.T) {
		p := base()
		p["drift"] = true
		srv := policyDetailServer(t, "ops-freeze", p)
		t.Setenv("STRAZA_SERVER", "")
		_, stderr, err := runPolicyView(t, srv.URL, "policy", "show", "ops-freeze")
		if err != nil {
			t.Fatalf("show: %v", err)
		}
		if !strings.Contains(stderr, "Live, edits not published") {
			t.Errorf("status word missing:\n%s", stderr)
		}
		if !strings.Contains(stderr, "the stored source below differs from what is enforcing right now; publish to make it live") {
			t.Errorf("note missing:\n%s", stderr)
		}
	})
	for name, p := range map[string]map[string]any{"absent": base(), "false": base()} {
		if name == "false" {
			p["drift"] = false
		}
		t.Run(name+" makes no claim", func(t *testing.T) {
			srv := policyDetailServer(t, "ops-freeze", p)
			t.Setenv("STRAZA_SERVER", "")
			_, stderr, err := runPolicyView(t, srv.URL, "policy", "show", "ops-freeze")
			if err != nil {
				t.Fatalf("show: %v", err)
			}
			if strings.Contains(stderr, "note") || strings.Contains(stderr, "edits not published") {
				t.Errorf("drift claimed without server truth:\n%s", stderr)
			}
			if got := headerValue(stderr, "status"); got != "Live" {
				t.Errorf("status = %q, want plain Live:\n%s", got, stderr)
			}
		})
	}
}

// An absent summary never invents a shape, and the bytes still print.
func TestPolicyShowNotDecomposable(t *testing.T) {
	const badYAML = "not: [valid\n"
	srv := policyDetailServer(t, "legacy-block", map[string]any{
		"id": "ps-2", "name": "legacy-block", "priority": 5, "status": "draft", "yaml": badYAML,
	})
	t.Setenv("STRAZA_SERVER", "")
	stdout, stderr, err := runPolicyView(t, srv.URL, "policy", "show", "legacy-block")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if stdout != badYAML {
		t.Errorf("stdout = %q, want the unparseable bytes verbatim", stdout)
	}
	if !strings.Contains(stderr, "not decomposable: the stored YAML no longer parses; the bytes below are the record, which is exactly when you need them") {
		t.Errorf("honest summary line missing:\n%s", stderr)
	}
	if strings.Contains(stderr, "applies to") || strings.Contains(stderr, "rules ") {
		t.Errorf("invented shape rendered for an unparseable set:\n%s", stderr)
	}
	if got := headerValue(stderr, "status"); got != "Off" {
		t.Errorf("status = %q, want Off:\n%s", got, stderr)
	}
}

const diffStoredYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: t
spec:
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *"]
      effect: deny
`

const diffLocalYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: t
spec:
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *", "chmod 777 *"]
      effect: deny
    - id: no-chmod-777
      tools: [shell.exec]
      command:
        denyPatterns: ["chmod 777 -R *"]
      effect: deny
`

func diffServer(t *testing.T) *httptest.Server {
	t.Helper()
	return policyDetailServer(t, "t", map[string]any{
		"id": "ps-1", "name": "t", "priority": 10, "status": "active",
		"yaml": diffStoredYAML, "updated_at": "2026-08-24T12:02:31Z",
	})
}

func TestPolicyDiffDiffers(t *testing.T) {
	srv := diffServer(t)
	file := writeTemp(t, "local.yaml", diffLocalYAML)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runPolicyView(t, srv.URL, "policy", "diff", "t", "-f", file)

	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 1 || ec.err != nil {
		t.Fatalf("err = %#v, want the silent differs sentinel (code 1, nil err)", err)
	}
	if ec.Error() != "" {
		t.Errorf("sentinel message = %q, want empty (the diff already said everything)", ec.Error())
	}
	for _, want := range []string{
		"stored t (Live, updated " + fmtStamp("2026-08-24T12:02:31Z") + ") vs " + file,
		"rules: 1 changed (no-rm-rf) · 1 added (no-chmod-777) · 0 removed",
		"--- server/t",
		"+++ " + file,
		"@@ -",
		`-        denyPatterns: ["rm -rf *"]`,
		`+        denyPatterns: ["rm -rf *", "chmod 777 *"]`,
		"+    - id: no-chmod-777",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff output missing %q:\n%s", want, stdout)
		}
	}
}

func TestPolicyDiffIdentical(t *testing.T) {
	srv := diffServer(t)
	file := writeTemp(t, "unchanged.yaml", diffStoredYAML)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runPolicyView(t, srv.URL, "policy", "diff", "t", "-f", file)
	if err != nil {
		t.Fatalf("identical diff must exit 0: %v", err)
	}
	want := `identical: stored "t" and ` + file + " match byte for byte\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestPolicyDiffUnparseableLocal(t *testing.T) {
	srv := diffServer(t)
	file := writeTemp(t, "broken.yaml", "::: not yaml\n")
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runPolicyView(t, srv.URL, "policy", "diff", "t", "-f", file)
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Fatalf("err = %#v, want code 1 (the line diff still ran)", err)
	}
	if !strings.Contains(stdout, file+" does not parse; rule-level diff skipped") {
		t.Errorf("honest parse note missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "--- server/t") {
		t.Errorf("line diff missing after the parse note:\n%s", stdout)
	}
}

func TestPolicyDiffMultiDocLocal(t *testing.T) {
	srv := diffServer(t)
	file := writeTemp(t, "two.yaml", diffStoredYAML+"---\n"+diffLocalYAML)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runPolicyView(t, srv.URL, "policy", "diff", "t", "-f", file)
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Fatalf("err = %#v, want code 1", err)
	}
	if !strings.Contains(stdout, "carries 2 documents; rule-level diff needs exactly one, line diff only") {
		t.Errorf("multi-doc note missing:\n%s", stdout)
	}
}

func TestPolicyDiffFailuresExitTwo(t *testing.T) {
	srv := diffServer(t)
	t.Setenv("STRAZA_SERVER", "")
	t.Run("missing -f", func(t *testing.T) {
		_, _, err := runPolicyView(t, srv.URL, "policy", "diff", "t")
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 || ec.Error() != "a -f file is required" {
			t.Fatalf("err = %#v, want code 2 naming the missing flag", err)
		}
	})
	t.Run("unreadable file", func(t *testing.T) {
		_, _, err := runPolicyView(t, srv.URL, "policy", "diff", "t", "-f", filepath.Join(t.TempDir(), "absent.yaml"))
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 || ec.err == nil {
			t.Fatalf("err = %#v, want code 2 with the read error", err)
		}
	})
}

// The unified renderer's numbers, pinned on a hand-checked case.
func TestUnifiedDiffNumbers(t *testing.T) {
	a := []string{"1", "2", "3", "4", "5", "6", "7", "8"}
	b := []string{"1", "2", "3", "4x", "5", "6", "7", "8"}
	var buf bytes.Buffer
	writeUnified(&buf, diffOps(a, b))
	want := "@@ -1,7 +1,7 @@\n 1\n 2\n 3\n-4\n+4x\n 5\n 6\n 7\n"
	if buf.String() != want {
		t.Errorf("unified = %q, want %q", buf.String(), want)
	}
}

// Two changes far apart stay two hunks; close together they fold into one.
func TestUnifiedDiffHunks(t *testing.T) {
	a := make([]string, 30)
	for i := range a {
		a[i] = string(rune('a' + i))
	}
	far := append([]string{}, a...)
	far[2], far[27] = "X", "Y"
	var buf bytes.Buffer
	writeUnified(&buf, diffOps(a, far))
	if got := strings.Count(buf.String(), "@@ -"); got != 2 {
		t.Errorf("hunks = %d, want 2:\n%s", got, buf.String())
	}
	near := append([]string{}, a...)
	near[10], near[14] = "X", "Y"
	buf.Reset()
	writeUnified(&buf, diffOps(a, near))
	if got := strings.Count(buf.String(), "@@ -"); got != 1 {
		t.Errorf("hunks = %d, want 1 (contexts touch):\n%s", got, buf.String())
	}
}

// TestPolicyDiffStatusWords pins policy diff's first line in the words the
// other policy verbs use, in apply's parenthesis: Off for a set that is off
// and Live for a live one, while the wire keeps its status. It leaves
// time.Local alone, which a -race run shares with every goroutine, and
// reads the stamp through fmtStamp, which TestPolicyDiffDiffers pins.
func TestPolicyDiffStatusWords(t *testing.T) {
	file := writeTemp(t, "local.yaml", diffLocalYAML)
	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name: "a set that is off",
			payload: map[string]any{"id": "ps-2", "name": "t", "priority": 10, "status": "draft",
				"yaml": diffStoredYAML, "updated_at": "2026-08-24T12:02:31Z"},
			want: "stored t (Off, updated " + fmtStamp("2026-08-24T12:02:31Z") + ") vs " + file,
		},
		{
			name:    "a live set with no update time",
			payload: map[string]any{"id": "ps-1", "name": "t", "priority": 10, "status": "active", "yaml": diffStoredYAML},
			want:    "stored t (Live) vs " + file,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := policyDetailServer(t, "t", tc.payload)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, _ := runPolicyView(t, srv.URL, "policy", "diff", "t", "-f", file)
			if first, _, _ := strings.Cut(stdout, "\n"); first != tc.want {
				t.Errorf("first line = %q, want %q", first, tc.want)
			}
		})
	}
}
