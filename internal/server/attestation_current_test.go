package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestAttestationHashCurrentFlag pins the `current` field on the registry
// rows: true only when the row's hash is the wiring this server renders
// now, read from the index harnessWiringStatus classifies sessions against.
// Boot registers the current render, so the list already holds the true
// case; the other rows are registered here.
func TestAttestationHashCurrentFlag(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	type row struct {
		ID       string `json:"id"`
		Artifact string `json:"artifact"`
		Harness  string `json:"harness"`
		Hash     string `json:"hash"`
		Current  bool   `json:"current"`
	}
	list := func() []row {
		t.Helper()
		var out []row
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/attestation-hashes", idToken, nil, &out); code != http.StatusOK {
			t.Fatalf("list = %d", code)
		}
		return out
	}

	// The boot-registered render of the hooks artifact this server serves.
	var live row
	for _, r := range list() {
		if r.Artifact == "hooks.claude-code" {
			live = r
			break
		}
	}
	if live.Hash == "" {
		t.Fatal("boot registered no hooks.claude-code row: the harness-config render did not run")
	}
	if !live.Current {
		t.Errorf("the boot-registered row %s reads current=false", live.Hash)
	}

	for _, tc := range []struct {
		name     string
		artifact string
		harness  string
		hash     string
		want     bool
	}{
		{"the render this server publishes now", "hooks.claude-code", "claude-code", live.Hash, true},
		{"an older render of the same artifact", "hooks.claude-code", "claude-code",
			"sha256:" + strings.Repeat("ab", 32), false},
		{"a harness this server does not render", "hooks.cursor", "cursor",
			"sha256:" + strings.Repeat("cd", 32), false},
		{"another artifact carrying a current hooks hash", "self", "claude-code", live.Hash, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"artifact": tc.artifact, "harness": tc.harness,
				"platform": "linux/amd64", "hash": tc.hash}
			var created row
			code := adminReq(t, http.MethodPost, base+"/v1/admin/attestation-hashes", idToken, body, &created)
			if code == http.StatusConflict {
				// Boot already registered this exact measurement; the list
				// below still carries the answer under test.
				created = row{Artifact: tc.artifact, Harness: tc.harness, Hash: tc.hash}
			} else if code != http.StatusCreated {
				t.Fatalf("register = %d", code)
			} else if created.Current != tc.want {
				t.Errorf("create response current = %v, want %v", created.Current, tc.want)
			}
			found := false
			for _, r := range list() {
				if r.Artifact == tc.artifact && r.Harness == tc.harness && r.Hash == tc.hash {
					found = true
					if r.Current != tc.want {
						t.Errorf("listed current = %v, want %v", r.Current, tc.want)
					}
				}
			}
			if !found {
				t.Errorf("the registered row is not in the list")
			}
		})
	}
}
