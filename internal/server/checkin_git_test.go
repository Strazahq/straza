package server

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestCheckinGitUpstreams pins git_upstreams in the check-in answer: absent
// with no git server, the one upstream with one, and with three servers on
// two upstreams each upstream once, sorted. A user with no role on any of
// them gets the same list, because it feeds one file for the whole machine.
func TestCheckinGitUpstreams(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	checkin := func() (any, bool) {
		t.Helper()
		code, body := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":    loginDeviceFlow(t, base, "kim", "hunter2!"),
			"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
		})
		if code != http.StatusOK {
			t.Fatalf("checkin = %d: %v", code, body)
		}
		v, ok := body["git_upstreams"]
		return v, ok
	}
	install := func(name, upstream string) {
		t.Helper()
		ctx := context.Background()
		mf, err := managerParse(t, secondManifest(name, startGitUpstream(t).URL, upstream, "{fetch: git_fetch, push: git_push}"))
		if err != nil {
			t.Fatal(err)
		}
		row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.broker.Set(ctx, row.ID, "", "app-tok"); err != nil {
			t.Fatal(err)
		}
		app.manager.SecretUpdated(ctx, row.ID)
	}

	if v, ok := checkin(); ok {
		t.Errorf("with no git server the answer carries git_upstreams %v", v)
	}
	install("ghapp", "https://github.com")
	if v, _ := checkin(); !reflect.DeepEqual(v, []any{"https://github.com"}) {
		t.Errorf("with one git server git_upstreams = %v", v)
	}
	install("ghtwin", "https://github.com")
	install("ghe", "https://ghe.example.com:8443")
	if v, _ := checkin(); !reflect.DeepEqual(v, []any{"https://ghe.example.com:8443", "https://github.com"}) {
		t.Errorf("with three git servers git_upstreams = %v, want each upstream once, sorted", v)
	}
}
