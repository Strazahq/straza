package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// policyApplyServer answers PUT /v1/admin/policies with the stored row the
// real server returns: status draft for a set it did not have, and status
// active when the text was saved over a live set.
func policyApplyServer(t *testing.T, status string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"ses-1","session_token":"stok-2"}`))
	})
	mux.HandleFunc("PUT /v1/admin/policies", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ps-7","name":"set-a","priority":0,"status":"` + status + `"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestPolicyApplySaysWhatWasStored pins the apply line per server answer. A
// set the server did not have is stored off. Text saved over a live
// set is edits that stay unpublished until policy activate checks and
// publishes them, so that line must never read as live or published.
func TestPolicyApplySaysWhatWasStored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "set-a.yaml")
	if err := os.WriteFile(file, []byte(splitDocA), 0o600); err != nil {
		t.Fatalf("write policy file: %v", err)
	}
	tests := []struct {
		name    string
		status  string
		want    []string
		notWant []string
	}{
		{
			name:    "a new set is stored off",
			status:  "draft",
			want:    []string{"applied set-a (Off, ps-7)\n"},
			notWant: []string{"not published", "activate"},
		},
		{
			name:   "text saved over a live set stays unpublished",
			status: "active",
			want: []string{
				"saved edits to the live policy set set-a (ps-7) without publishing them. ",
				"The published version keeps deciding until you run strazactl policy activate set-a, which checks the edits and publishes them.\n",
			},
			notWant: []string{"(Live", "applied set-a"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := policyApplyServer(t, tc.status)
			t.Setenv("STRAZA_SERVER", "")
			stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "policy", "apply", "-f", file)
			if err != nil {
				t.Fatalf("policy apply: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(stdout, w) {
					t.Errorf("output missing %q:\n%s", w, stdout)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(stdout, w) {
					t.Errorf("output carries %q:\n%s", w, stdout)
				}
			}
		})
	}
}

// TestPolicyListStatusWords pins the STATUS column in the words policy show
// uses. A live set whose stored text differs from the text it runs reads
// Live, edits not published. A live set with no such claim, whether the
// field is absent or false, reads Live, and a set that is off reads Off,
// though the wire keeps its status draft.
func TestPolicyListStatusWords(t *testing.T) {
	srv := policyServer(t, `{"items":[`+
		`{"id":"ps-1","name":"edited-set","priority":10,"status":"active","drift":true},`+
		`{"id":"ps-2","name":"clean-set","priority":20,"status":"active"},`+
		`{"id":"ps-3","name":"false-set","priority":30,"status":"active","drift":false},`+
		`{"id":"ps-4","name":"off-set","priority":40,"status":"draft"}`+
		`],"total":4,"limit":0,"offset":0,"roles":[]}`)
	t.Setenv("STRAZA_SERVER", "")
	stdout, _, err := runCLI(t, writeCreds(t, srv.URL), "policy", "list")
	if err != nil {
		t.Fatalf("policy list: %v", err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			rows[f[0]] = line
		}
	}
	tests := []struct {
		set    string
		want   string
		edited bool
	}{
		{"edited-set", "Live, edits not published", true},
		{"clean-set", "Live", false},
		{"false-set", "Live", false},
		{"off-set", "Off", false},
	}
	for _, tc := range tests {
		t.Run(tc.set, func(t *testing.T) {
			line, ok := rows[tc.set]
			if !ok {
				t.Fatalf("no row for %s:\n%s", tc.set, stdout)
			}
			if !strings.Contains(line, tc.want) {
				t.Errorf("row %q, want status %q", line, tc.want)
			}
			if !tc.edited && strings.Contains(line, "edits not published") {
				t.Errorf("row %q claims unpublished edits", line)
			}
		})
	}
}

// TestPolicyPublishHelp pins what apply and activate promise, in the command
// list and in each command's own help. apply stores a new set off and
// leaves edits to a live set unpublished until activate. activate publishes
// only the named set after the server's checks, and every other live set
// keeps the text it was published with.
func TestPolicyPublishHelp(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{
			args: []string{"policy", "--help"},
			want: []string{
				"a new set is stored off, and edits to a live set wait for policy activate",
				"leaving every other set as published",
			},
		},
		{
			args: []string{"policy", "apply", "--help"},
			want: []string{
				"is stored off and governs nothing",
				"the published version keeps deciding",
				"until `strazactl policy activate <name>` checks the",
			},
		},
		{
			args: []string{"policy", "activate", "--help"},
			want: []string{
				"Only this set changes.",
				"Every other live set keeps the text it was last",
				"A refused check changes nothing",
			},
		},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.args[:2], " "), func(t *testing.T) {
			stdout, _, err := runCLI(t, noCreds(t), tc.args...)
			if err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(stdout, w) {
					t.Errorf("help missing %q:\n%s", w, stdout)
				}
			}
		})
	}
}
