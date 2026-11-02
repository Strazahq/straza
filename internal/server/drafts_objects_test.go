package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestDraftCheckReadsObjectsAgainstTheRunningServer pins the objects check
// on a server's manifest: a listing tool that is not marked read-only, an
// argument the tool's input schema does not require as a string, and a
// pinned reach looser than the server's own mark each refuse the change
// with the sentence that names it, while a block that keeps the server's
// marks, or one for a server not yet running, publishes.
func TestDraftCheckReadsObjectsAgainstTheRunningServer(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up, _ := objectsUpstream(t, nil)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(objectsManifest(up.URL, "")), nil); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	waitAppStatus(t, base, bearer, "repos", "running")

	cases := []struct {
		name  string
		block string
		code  int
		want  string
	}{
		{"a listing tool not marked read-only", strings.Replace(strings.Replace(pinnedRepos, "tool: repo_list", "tool: whoami", 1), "      whoami: none\n", "", 1), http.StatusConflict,
			"whoami is not marked read-only, so Straza will not call it to list repos."},
		{"an argument the schema lacks", strings.Replace(pinnedRepos, "{argument: repo}", "{argument: name}", 1), http.StatusConflict,
			"repo_read has no argument name that is a required top-level string in its input schema"},
		{"a reach looser than the server's mark", strings.Replace(pinnedRepos, "{argument: repo}", "none", 1), http.StatusConflict,
			"repos marks repo_read with the reach argument repo, and the pinned reach none is looser."},
		{"the server's own marks", pinnedRepos, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		var out map[string]any
		code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(objectsManifest(up.URL, tc.block)), &out)
		if code != tc.code {
			t.Errorf("%s: install = %d %v, want %d", tc.name, code, out, tc.code)
			continue
		}
		if msg, _ := out["error"].(string); tc.want != "" && !strings.Contains(msg, tc.want) {
			t.Errorf("%s: refusal %q, want it to contain %q", tc.name, msg, tc.want)
		}
	}

	fresh := strings.Replace(objectsManifest(up.URL, pinnedRepos), "name: repos}", "name: repos-two}", 1)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(fresh), nil); code != http.StatusCreated {
		t.Errorf("a new server with a pinned block = %d, want 201", code)
	}
}
