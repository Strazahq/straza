package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// storedSecrets are the values the servers of TestNoSurfaceShowsAStoredSecret
// store that no client, log reader or audit record reads: the user name,
// the password with a quote in it, the capability in the path, the query
// token and the fragment token of an address, an env value under a
// secret's name, a line of a private key held in an env value, and a user
// name and a query token of an address in an argument and in a
// description.
var storedSecrets = []string{"svcuser7", "Pw0rdS3cr3t", "Cap9T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3", "Qt0kS3cr3t", "Fr4gS3cr3t",
	"Ev4lS3cr3t", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7Zq9Lm2Xw4Rt6Y", "ArgS3cr3tQ", "DescS3cr3t"}

// secretAddressAt is the address of a server at host with every address
// part of storedSecrets in it.
func secretAddressAt(host string) string {
	return "http://svcuser7:It'sPw0rdS3cr3t@" + host + "/mcp/Cap9T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3/v1?session=Qt0kS3cr3t#frag=Fr4gS3cr3t"
}

// printerDoc is a command server that prints its own env values, a token
// and a private key over several lines, to its error output, as a server
// that logs its settings does, and that carries an address with a user
// name and a query in an argument and in its description.
const printerDoc = `apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: printer, description: "Reaches http://svcuser7@d.example/?session=DescS3cr3t"}
server: {name: straza.test/printer, version: "1.0.0"}
straza:
  runtime:
    kind: command
    command:
      exec: /bin/sh
      args: [-c, "echo \"starting with API_TOKEN=$API_TOKEN\" >&2; printf '%s\\n' \"$TLS_KEY\" >&2; exec sleep 30", --upstream, "http://svcuser7@up.example/mcp?session=ArgS3cr3tQ"]
      env:
        - {name: API_TOKEN, value: Ev4lS3cr3t}
        - {name: TLS_KEY, value: "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7Zq9Lm2Xw4Rt6Y\nQe1Sd7FTk8Hn3Vb5Qe1Sd7FTk8Hn3Vb5Qe1Sd7FTk8Hn3Vb5Qe1Sd7FAbCdEfGh\n-----END PRIVATE KEY-----"}
`

// tokenDoc is a server at url that takes each caller's own token.
const tokenDoc = `apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: tokapp}
server: {name: straza.test/tokapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: token
    agents: sponsor
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
  exposure:
    tools: ["*"]
`

// TestNoSurfaceShowsAStoredSecret is the table over every surface that
// quotes a server's address, answers its manifest or prints its log, fed
// servers stored the way a manifest stored before the secret scan holds
// them. None of the surfaces holds a value of storedSecrets. Each row also
// asserts a word the surface must say, so an empty answer cannot pass.
// The positive controls read the stored rows, the raw log ring and an
// upstream error made directly, and find the values there.
func TestNoSurfaceShowsAStoredSecret(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	app, base := testAppPreRun(t, []func(*App){func(a *App) { a.log = slog.New(slog.NewTextHandler(logs, nil)) }})
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	seedGatewayUser(t, app, "joe", "dev")
	up := slowUpstream(t)
	host := strings.TrimPrefix(up.URL, "http://")
	deploySlowApp(t, app, "slowsec", secretAddressAt(host), 1)
	for _, doc := range []string{fmt.Sprintf(tokenDoc, secretAddressAt("127.0.0.1:1")), printerDoc} {
		mf, err := managerParse(t, doc)
		if err != nil {
			t.Fatal(err)
		}
		row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
		if err != nil {
			t.Fatal(err)
		}
		catReach(t, app, "dev", row, `["*"]`)
	}
	joe := sessionToken(t, base, "joe")
	for name, values := range map[string][]string{"slowsec": storedSecrets[:5], "tokapp": storedSecrets[:5], "printer": storedSecrets[5:]} {
		row, err := app.store.Apps().GetByName(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range values {
			if !strings.Contains(row.Manifest, v) {
				t.Fatalf("control: the stored manifest of %s lacks %s", name, v)
			}
		}
	}
	waitFor(t, "the printer's own lines in the raw ring", func() bool {
		entries, _ := app.manager.LogEntries("printer", 0)
		var all strings.Builder
		for _, e := range entries {
			all.WriteString(e.Line + "\n")
		}
		return strings.Contains(all.String(), "API_TOKEN=Ev4lS3cr3t") && strings.Contains(all.String(), storedSecrets[6])
	})
	body := func(method, path, bearer string, in any) string {
		var out json.RawMessage
		adminReq(t, method, base+path, bearer, in, &out)
		return string(out)
	}
	list := body(http.MethodGet, "/v1/admin/apps", root, nil)
	printer, _ := listed(t, base, root, "printer")
	slowsec, _ := listed(t, base, root, "slowsec")
	up.Close()
	control := manager.NewRemoteRuntime("control", manager.RemoteSpec{URL: secretAddressAt(host)}, nil, nil)
	control.AllowLoopback = true
	t.Cleanup(control.Stop)
	if _, err := control.Tools(ctx, nil); err == nil || !strings.Contains(err.Error(), "svcuser7") || !strings.Contains(err.Error(), storedSecrets[2]) ||
		!strings.Contains(err.Error(), "Qt0kS3cr3t") {
		t.Fatalf("control: the upstream error %v does not quote the address", err)
	}
	recheck := body(http.MethodPost, "/v1/admin/apps/slowsec/health", root, nil)
	_, _, closed := mcpCall(t, base, joe, "tools/call", map[string]any{"name": "slowsec__hang"})
	var records []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "apps.recheck" {
			records = append(records, ev)
		}
	}
	recorded, _ := json.Marshal(records)
	var install json.RawMessage
	ftp := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: ftpapp}\nserver: {name: straza.test/ftpapp, version: \"1.0.0\"}\n" +
		"straza:\n  runtime:\n    kind: remote\n    remote: {url: \"" + strings.Replace(secretAddressAt("ftp.example"), "http", "ftp", 1) + "\"}\n"
	rawReq(t, http.MethodPost, base+"/v1/admin/apps", root, "application/yaml", []byte(ftp), &install)
	paste := map[string]any{"token": "tok-x", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	note := body(http.MethodPost, "/v1/admin/drafts", root, map[string]any{"documents": []string{strings.Replace(exported(t, slowsec), "name: slowsec\n", "name: slowsec\n    description: Changed.\n", 1)}})
	cases := []struct{ name, text, says string }{
		{"the servers list", list, "%5BREDACTED%5D@" + host + "/mcp/[REDACTED]/v1?…#…"},
		{"the export of a server with addresses in an argument and a description", exported(t, printer), "http://%5BREDACTED%5D@up.example/mcp?…"},
		{"the recheck answer after the upstream closed", recheck, "degraded"},
		{"the apps.recheck record", string(recorded), "apps.recheck"},
		{"the AI agent's error on a closed upstream", string(closed), "upstream call failed"},
		{"the servers list after the upstream closed", body(http.MethodGet, "/v1/admin/apps", root, nil), "degraded"},
		{"the stored server-down note of a draft", note, "slowsec reads degraded now"},
		{"the log of the remote server", body(http.MethodGet, "/v1/admin/apps/slowsec/logs", root, nil), "connected http://" + host + "/mcp/%5BREDACTED%5D/v1?…#…"},
		{"the log of a server that prints its settings", body(http.MethodGet, "/v1/admin/apps/printer/logs", root, nil), "starting with API_TOKEN=[REDACTED]"},
		{"the answer to a token paste", body(http.MethodPost, "/v1/connect/tokapp", joe, paste), "refused the token or did not answer"},
		{"the install answer for an address that is not http", string(install), "must be http(s)"},
		{"strazad's log", logs.String(), "upstream call failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.text, tc.says) {
				t.Errorf("the surface does not say %q, so it proves nothing: %s", tc.says, tc.text)
			}
			for _, secret := range storedSecrets {
				if strings.Contains(tc.text, secret) {
					t.Errorf("the surface holds %s: %s", secret, tc.text)
				}
			}
		})
	}
}
