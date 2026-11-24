package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestPolicyActivateRefusesWhatValidateRefuses pins that activating a set
// whose Rego module calls a refused built-in answers as validate answers the
// same text: 400 with the compiler's sentence, for the stored text of a set
// that is off and for the saved edit of a live set alike. The refusal
// publishes nothing. The drafts check route refuses the same text with
// policy.compile, and a set that compiles passes all three routes.
func TestPolicyActivateRefusesWhatValidateRefuses(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	tok := loginDeviceFlow(t, base, "kim", "hunter2!")
	set := func(name, call string) string {
		doc := strings.Replace(guardrailsYAML, "sim-guardrails", name, 1)
		if call != "" {
			doc += "  escape:\n    rego: |\n      package straza.ext\n\n      deny contains msg if { " + call + " }\n"
		}
		return doc
	}
	const glob = `glob.match("a*", [], "abc"); msg := "no"`
	const fetch = `msg := http.send({"method": "get", "url": "http://a/"}).raw_body`
	refused := func(name, builtin, why string) string {
		return "policy: set " + name + ": its Rego module calls " + builtin + " on line 3, and Straza refuses that built-in because " +
			why + ". Remove the call from the module"
	}
	const runsLong = "a single call of it can run far past the 100 ms decision deadline or crash the process, " +
		"and Straza checks that deadline only between calls"
	const reachesOut = "a policy module must not reach the network, the file system or the process environment"

	for _, tc := range []struct {
		name string
		text string
		// live publishes a set that compiles under the name first, so text
		// is that live set's saved edit.
		live   bool
		status int
		error  string // validate's and activate's refusal, "" for none
		check  string // the sentence of the drafts check's policy.compile, "" for none
	}{
		{name: "glob-off", text: set("glob-off", glob), status: http.StatusBadRequest,
			error: refused("glob-off", "glob.match", runsLong),
			check: "The policy does not compile with this draft: " + refused("glob-off", "glob.match", runsLong) + "."},
		{name: "fetch-off", text: set("fetch-off", fetch), status: http.StatusBadRequest,
			error: refused("fetch-off", "http.send", reachesOut),
			check: "The policy does not compile with this draft: " + refused("fetch-off", "http.send", reachesOut) + "."},
		{name: "glob-edit", text: set("glob-edit", glob), live: true, status: http.StatusBadRequest,
			error: refused("glob-edit", "glob.match", runsLong),
			check: "The policy does not compile with this draft: " + refused("glob-edit", "glob.match", runsLong) + "."},
		{name: "fine", text: set("fine", ""), status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var validated struct {
				Error string `json:"error"`
			}
			code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, tc.text, &validated)
			if code != tc.status || validated.Error != tc.error {
				t.Fatalf("validate = %d %q, want %d %q", code, validated.Error, tc.status, tc.error)
			}
			var a wireAnswer
			body, _ := json.Marshal(map[string]any{"items": []map[string]string{{"kind": "PolicySet", "name": tc.name, "op": "put", "doc": tc.text}}})
			code, out, _ := adminBytes(t, http.MethodPost, base+"/v1/admin/drafts/check", tok, "application/json", body)
			_ = json.Unmarshal(out, &a)
			var got []string
			for _, f := range a.Verdict.Refused {
				got = append(got, f.Code+" "+f.Sentence+" "+f.Fix)
			}
			want := []string(nil)
			if tc.check != "" {
				want = []string{"policy.compile " + tc.check + " Fix the set the error names."}
			}
			if code != http.StatusOK || strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("drafts check = %d refused %q, want 200 refused %q", code, got, want)
			}
			if tc.live {
				if code := yamlReq(t, http.MethodPut, base+"/v1/admin/policies", tok, set(tc.name, ""), nil); code != http.StatusCreated {
					t.Fatalf("apply of the live text = %d", code)
				}
				if code, out, _ := adminBytes(t, http.MethodPost, base+"/v1/admin/policies/"+tc.name+"/activate", tok, "application/json", nil); code != http.StatusOK {
					t.Fatalf("activate of the live text = %d %s", code, out)
				}
				if code := yamlReq(t, http.MethodPut, base+"/v1/admin/policies", tok, tc.text, nil); code != http.StatusOK {
					t.Fatalf("save of the edit = %d, want 200", code)
				}
			} else if code := yamlReq(t, http.MethodPut, base+"/v1/admin/policies", tok, tc.text, nil); code != http.StatusCreated {
				t.Fatalf("apply = %d, want 201", code)
			}
			before := app.snapshots.Current().ID
			code, out, _ = adminBytes(t, http.MethodPost, base+"/v1/admin/policies/"+tc.name+"/activate", tok, "application/json", nil)
			var activated struct {
				Error   string `json:"error"`
				Changed bool   `json:"changed"`
			}
			_ = json.Unmarshal(out, &activated)
			if code != tc.status || activated.Error != tc.error {
				t.Fatalf("activate = %d %s, want %d with validate's sentence %q", code, out, tc.status, tc.error)
			}
			if published := app.snapshots.Current().ID != before; published != (tc.status == http.StatusOK) || activated.Changed != published {
				t.Errorf("activate published a snapshot: %v, changed %v, want both %v", published, activated.Changed, tc.status == http.StatusOK)
			}
		})
	}
}
