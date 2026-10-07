package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/wire"
)

// TestEnterpriseRefusesCommandServers: under the enterprise profile /version
// lists no command runtime, the install API refuses a command manifest, dry
// run or not, with the manager's sentence, while it still takes a remote
// one. A command manifest in the apps directory becomes a draft whose check
// refuses it with app.runtime, and contacting it answers the manager's
// sentence. The standalone side of /version is pinned by TestAppsWizardAPI.
func TestEnterpriseRefusesCommandServers(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, enterpriseProfile, func(c *config.Config) { c.Apps.PollInterval = time.Hour })
	setPasswordDirect(t, app, BreakGlassUsername, "vaulted-secret")
	_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    loginDeviceFlow(t, base, BreakGlassUsername, "vaulted-secret"),
		"harness":     map[string]string{"name": "console", "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	bearer, _ := checkin["session_token"].(string)
	if bearer == "" {
		t.Fatalf("no break-glass session: %v", checkin)
	}

	var v wire.VersionStatus
	if code := adminReq(t, "GET", base+"/version", "", nil, &v); code != http.StatusOK {
		t.Fatalf("/version = %d", code)
	}
	wantRuntimes := "remote"
	if manager.DockerOnPath() {
		wantRuntimes += ",oci"
	}
	if got := strings.Join(v.Runtimes, ","); got != wantRuntimes {
		t.Errorf("runtimes = %s, want %s", got, wantRuntimes)
	}

	command := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: files}\nserver: {name: straza.test/files, version: \"1.0.0\"}\n" +
		"straza:\n  runtime:\n    kind: command\n    command: {exec: /usr/local/bin/files-mcp}\n"
	for _, path := range []string{"/v1/admin/apps?dryRun=1", "/v1/admin/apps"} {
		var refused map[string]any
		if code := rawReq(t, "POST", base+path, bearer, "application/yaml", []byte(command), &refused); code != http.StatusUnprocessableEntity ||
			refused["error"] != manager.CommandRefusal("files") {
			t.Errorf("POST %s = %d %v, want 422 with the refusal", path, code, refused)
		}
	}
	var apps []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK || len(apps) != 0 {
		t.Errorf("apps after the refusals = %d %+v, want none", code, apps)
	}

	var dry map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps?dryRun=1", bearer, "application/yaml", []byte(echoManifest("https://files.example.com/mcp")), &dry); code != http.StatusOK {
		t.Errorf("dry run of a remote server = %d %v, want 200", code, dry)
	}

	file := filepath.Join(app.cfg.AppsDir(), "files.yaml")
	if err := os.WriteFile(file, []byte(command), 0o600); err != nil {
		t.Fatal(err)
	}
	app.watcher.Sweep(context.Background())
	rows, err := app.store.Drafts().BySource(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("drafts of the apps directory file = %+v, want one", rows)
	}
	id := strconv.FormatInt(rows[0].ID, 10)
	var read struct {
		Verdict struct {
			Refused []struct{ Code, Sentence, Fix string } `json:"refused"`
		} `json:"verdict"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/drafts/"+id, bearer, nil, &read); code != http.StatusOK ||
		len(read.Verdict.Refused) != 1 || read.Verdict.Refused[0].Code != "app.runtime" ||
		read.Verdict.Refused[0].Sentence+" "+read.Verdict.Refused[0].Fix != manager.CommandRefusal("files") {
		t.Errorf("the check of the apps directory draft = %d %+v, want app.runtime with the refusal", code, read.Verdict.Refused)
	}
	var contact map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/drafts/"+id+"/contact", bearer, map[string]any{"object": "App/files"}, &contact); code != http.StatusConflict ||
		contact["error"] != manager.CommandRefusal("files") {
		t.Errorf("contact of the command server = %d %v, want 409 with the refusal", code, contact)
	}
}
