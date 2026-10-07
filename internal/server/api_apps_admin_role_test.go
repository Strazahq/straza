package server

import (
	"testing"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// TestServerAdminRoleViewsRefusal: a server admin may not change
// straza.exposure.views of a server they administer, either way, because
// the switch decides whether the server's own HTML is shown to people. A
// change of the tools beside it stays theirs.
func TestServerAdminRoleViewsRefusal(t *testing.T) {
	const views = "changing straza.exposure.views of a server needs the scope apps:write or the role straza-global-mcp-admin, " +
		"because with views on, chat apps show the server's own HTML pages to the people who use it. Ask a holder of straza-global-mcp-admin to make that change."
	manifest := func(exposure string) manager.Manifest {
		t.Helper()
		mf, err := manager.Parse([]byte("apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: dash}\n" +
			"server: {name: straza.test/dash, version: \"1.0.0\"}\n" +
			"straza:\n  runtime:\n    kind: remote\n    remote: {url: \"https://dash.example/mcp\"}\n" + exposure))
		if err != nil {
			t.Fatal(err)
		}
		return mf
	}
	stored := func(mf manager.Manifest) store.App {
		t.Helper()
		js, err := mf.JSON()
		if err != nil {
			t.Fatal(err)
		}
		return store.App{Name: "dash", Manifest: js, RuntimeKind: manager.RuntimeRemote}
	}
	off, on := manifest(""), manifest("  exposure: {tools: [\"*\"], views: true}\n")
	for _, tc := range []struct {
		name       string
		prev, next manager.Manifest
		want       string
	}{
		{"views turned on", off, on, views},
		{"views turned off", on, off, views},
		{"views stay on while the tools change", on, manifest("  exposure: {tools: [\"show_*\"], views: true}\n"), ""},
		{"views stay off", off, manifest("  exposure: {tools: [\"show_*\"]}\n"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := serverAdminRuntimeRefusal(stored(tc.prev), tc.next, nil)
			if err != nil || got != tc.want {
				t.Errorf("refusal = %q, %v\nwant %q", got, err, tc.want)
			}
		})
	}
}
