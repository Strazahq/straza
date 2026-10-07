package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/redact"
)

// legacyRunner is a command server whose stored manifest holds what the
// secret scan refuses today and a server stored before the scan may hold:
// an env value under a secret's name, a plain env value, a word after a
// flag naming a secret, a plain argument, an address with a user name and
// a query in an argument and in the description, and a string and a
// number in the server block.
func legacyRunner(name, version string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: " + name +
		"\n  description: The legacy runner, see http://svcuser7@d.example/?session=DescS3cr3t for more.\n" +
		"server:\n  name: io.x/" + name + "\n  version: " + version + "\n  packages:\n    - registryType: npm\n      identifier: \"@x/legacy\"\n" +
		"      environmentVariables:\n        - {name: REGION, value: eu-west-9-plain}\n        - {name: ACCOUNT_PIN, value: 482913}\n" +
		"straza:\n  runtime:\n    kind: command\n    command:\n      exec: /bin/sh\n" +
		"      args: [-c, \"exec sleep 30\", --api-key, Ak7Zq9Lm2Xw4Rt6Y, --verbose-mode, --upstream, \"http://svcuser7@up.example/mcp?session=ArgS3cr3tQ\"]\n" +
		"      env:\n        - {name: LOG_LEVEL, value: debugplain}\n        - {name: API_TOKEN, value: Tk8Hn3Vb5Qe1Sd7F}\n"
}

// legacyRunnerValues are the stored values of legacyRunner that no read
// answers.
var legacyRunnerValues = []string{"eu-west-9-plain", "482913", "Ak7Zq9Lm2Xw4Rt6Y", "debugplain", "Tk8Hn3Vb5Qe1Sd7F", "svcuser7", "ArgS3cr3tQ", "DescS3cr3t"}

// listed answers the list's row of the server name and the whole list body.
func listed(t *testing.T, base, bearer, name string) (appPayload, string) {
	t.Helper()
	var body json.RawMessage
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/apps", bearer, nil, &body); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	var rows []appPayload
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	for _, p := range rows {
		if p.Name == name {
			return p, string(body)
		}
	}
	t.Fatalf("the list has no %s: %s", name, body)
	return appPayload{}, ""
}

// exported renders a list row's manifest as strazactl apps export prints it.
func exported(t *testing.T, p appPayload) string {
	t.Helper()
	var mf manager.Manifest
	if err := json.Unmarshal(p.Manifest, &mf); err != nil {
		t.Fatalf("the list manifest of %s does not decode: %v", p.Name, err)
	}
	out, err := yaml.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// withEnvMask answers doc, a command server's document, with an env entry
// name added in front of the others whose value is the mask.
func withEnvMask(t *testing.T, doc, name string) string {
	t.Helper()
	var mf manager.Manifest
	if err := yaml.Unmarshal([]byte(doc), &mf); err != nil {
		t.Fatal(err)
	}
	c := mf.Straza.Runtime.Command
	c.Env = append([]manager.EnvVar{{Name: name, Value: redact.Mark}}, c.Env...)
	out, err := yaml.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestAppsListMasksManifest pins the list's manifest and url as every route
// answers a server's document, for rows stored before the secret scan: no
// stored value the mask covers appears anywhere in the body, the mask
// stands at its place, a plain argument stays readable, url is the masked
// address, and a manifest the mask cannot render answers neither field.
func TestAppsListMasksManifest(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	token := "ghp_" + strings.Repeat("aB3", 12)
	cases := []struct {
		name, doc, url  string
		plain, readable []string
		check           func(manager.Manifest) bool
	}{
		{name: "legacy", doc: legacyRunner("legacy", "1.0.0"), plain: legacyRunnerValues, readable: []string{"--verbose-mode", "exec sleep 30", "--api-key", "up.example", "d.example"},
			check: func(mf manager.Manifest) bool {
				c := mf.Straza.Runtime.Command
				return c.Env[0].Value == redact.Mark && c.Env[1].Value == redact.Mark && c.Args[3] == redact.Mark && c.Args[4] == "--verbose-mode" &&
					c.Args[6] == "http://%5BREDACTED%5D@up.example/mcp?\u2026" &&
					mf.Metadata.Description == "The legacy runner, see http://%5BREDACTED%5D@d.example/?\u2026 for more."
			}},
		{name: "fragment", doc: draftApp("fragment", "https://remote.example/mcp/T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3#access_token=Fr4gS3cr3t", "Fragment."),
			url: "https://remote.example/mcp/[REDACTED]#\u2026", plain: []string{"T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3", "Fr4gS3cr3t"}, readable: []string{"remote.example"}},
		{name: "quoted", doc: draftApp("quoted", "\"https://svcuser7:It'sPw0rdS3cr3t@remote.example/mcp?session=Qt0kS3cr3t\"", "Quoted."),
			url: "https://%5BREDACTED%5D@remote.example/mcp?\u2026", plain: []string{"svcuser7", "Pw0rdS3cr3t", "Qt0kS3cr3t"}, readable: []string{"remote.example"}},
		{name: "secretremote", doc: draftApp("secretremote", "\"https://svcuser7:Pw0rdS3cr3t@remote.example/mcp?session=Qt0kS3cr3t\"", "Remote."),
			url: "https://%5BREDACTED%5D@remote.example/mcp?…", plain: []string{"svcuser7", "Pw0rdS3cr3t", "Qt0kS3cr3t"}, readable: []string{"remote.example"}},
		{name: "useronly", doc: draftApp("useronly", "https://svcuser7@user.example/mcp", "User."),
			url: "https://%5BREDACTED%5D@user.example/mcp", plain: []string{"svcuser7"}, readable: []string{"user.example"}},
		{name: "capability", doc: draftApp("capability", "https://hooks.example/services/T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3", "Hook."),
			plain: []string{"T0AB1CD2E3F4G5H6I7J8K9L0M1N2P3"}, readable: []string{"hooks.example"}},
		{name: "unreadable", doc: strings.Replace(draftApp("unreadable", "https://un.example/mcp", "x"), "description: \"x\"", "description: \"  see "+token+"\\nsecond line\"", 1),
			plain: []string{token}},
	}
	for _, tc := range cases {
		stored := putServer(t, app, tc.doc)
		for _, v := range tc.plain {
			if !strings.Contains(stored.Manifest, v) {
				t.Fatalf("%s: the stored manifest lacks %q, so the row proves nothing", tc.name, v)
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, body := listed(t, base, root, tc.name)
			for _, v := range tc.plain {
				if strings.Contains(body, v) {
					t.Errorf("the list body holds %q", v)
				}
			}
			if tc.name == "unreadable" {
				if p.Manifest != nil || p.URL != "" {
					t.Errorf("a manifest the mask cannot render answered manifest %s and url %q, want neither", p.Manifest, p.URL)
				}
				return
			}
			for _, v := range tc.readable {
				if !strings.Contains(string(p.Manifest), v) {
					t.Errorf("the list manifest lost %q: %s", v, p.Manifest)
				}
			}
			var mf manager.Manifest
			if err := json.Unmarshal(p.Manifest, &mf); err != nil {
				t.Fatalf("the list manifest does not decode: %v", err)
			}
			if tc.check != nil && !tc.check(mf) {
				t.Errorf("the masks do not stand at their places: %s", p.Manifest)
			}
			if tc.url != "" && (p.URL != tc.url || mf.Straza.Runtime.Remote.URL != tc.url) {
				t.Errorf("url = %q, manifest url = %q, want %q", p.URL, mf.Straza.Runtime.Remote.URL, tc.url)
			}
			if _, err := manager.Parse([]byte(exported(t, p))); err != nil {
				t.Errorf("the export of the list manifest does not validate: %v", err)
			}
		})
	}
}

// TestAppsListManifestRoundTrips pins the export sent back through the
// install route: unchanged it is a no-op with no record, with a new version
// it publishes and keeps every stored value, for a full admin and for the
// server's own admin, whose masked runtime values do not read as a runtime
// change, and a mask at a place the stored manifest does not mask is
// refused with the fix that says what to write.
func TestAppsListManifestRoundTrips(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	putServer(t, app, legacyRunner("legacy", "1.0.0"))
	if _, err := app.store.Apps().BackfillAdminRoles(ctx); err != nil {
		t.Fatal(err)
	}
	mkHuman(t, app, "erin", "mcp-admin-legacy")
	erin := loginDeviceFlow(t, base, "erin", "hunter2!")
	installs := func() int {
		n := 0
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == "apps.install" {
				n++
			}
		}
		return n
	}
	p, _ := listed(t, base, root, "legacy")
	doc := exported(t, p)
	cases := []struct {
		name, bearer, body string
		code, records      int
		version, error     string
	}{
		{name: "the export unchanged", bearer: root, body: doc, code: http.StatusCreated, version: "1.0.0"},
		{name: "a new version by a full admin", bearer: root, body: strings.Replace(doc, "version: 1.0.0", "version: 1.0.1", 1), code: http.StatusCreated, records: 1, version: "1.0.1"},
		{name: "a new version by the server's admin", bearer: erin, body: strings.Replace(doc, "version: 1.0.0", "version: 1.0.2", 1), code: http.StatusCreated, records: 2, version: "1.0.2"},
		{name: "a mask at a new env entry", bearer: root, code: http.StatusUnprocessableEntity, records: 2, version: "1.0.2",
			body:  withEnvMask(t, doc, "EXTRA"),
			error: "Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and make the change again."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out map[string]any
			code := rawReq(t, http.MethodPost, base+"/v1/admin/apps", tc.bearer, "application/yaml", []byte(tc.body), &out)
			if code != tc.code {
				t.Fatalf("install = %d %v, want %d", code, out, tc.code)
			}
			if msg, _ := out["error"].(string); tc.error != "" && !strings.Contains(msg, tc.error) {
				t.Errorf("error = %q\nwant it to hold %q", msg, tc.error)
			}
			if got := installs(); got != tc.records {
				t.Errorf("apps.install records = %d, want %d", got, tc.records)
			}
			row, err := app.store.Apps().GetByName(ctx, "legacy")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(row.Manifest, `"version":"`+tc.version+`"`) || strings.Contains(row.Manifest, redact.Mark) {
				t.Errorf("stored manifest = %s, want version %s and no mask", row.Manifest, tc.version)
			}
			for _, v := range legacyRunnerValues {
				if !strings.Contains(row.Manifest, v) {
					t.Errorf("the stored manifest lost %q: %s", v, row.Manifest)
				}
			}
		})
	}
}

// TestServerLogMask pins the read-time mask of a server's log: a value the
// secret scan flags in the stored manifest is replaced wherever a line
// holds it, the scan's text mask runs over each line, and a plain value
// and a plain line read as they were written.
func TestServerLogMask(t *testing.T) {
	t.Parallel()
	mask := serverLogMask(manifestJSON(t, legacyRunner("legacy", "1.0.0")))
	token := "ghp_" + strings.Repeat("aB3", 12)
	cases := []struct{ name, in, want string }{
		{"an env value under a secret's name", "starting with API_TOKEN=Tk8Hn3Vb5Qe1Sd7F", "starting with API_TOKEN=" + redact.Mark},
		{"a word after a flag naming a secret", "argv: --api-key Ak7Zq9Lm2Xw4Rt6Y --verbose-mode", "argv: --api-key " + redact.Mark + " --verbose-mode"},
		{"a credential shape the scan knows", "loaded key " + token, "loaded key " + redact.Mark},
		{"a plain env value", "log level debugplain", "log level debugplain"},
		{"a plain line", "listening on 8080", "listening on 8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mask(tc.in); got != tc.want {
				t.Errorf("mask(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := serverLogMask("")("listening on 8080"); got != "listening on 8080" {
		t.Errorf("the mask of a server with no manifest changed a plain line: %q", got)
	}
}

// TestFileDoorRefusesAMaskedExport pins the apps directory's answer to a
// file copied from strazactl apps export: the door keeps no mask, so the
// file is refused, and the refusal says the file holds a masked value and
// that the person writes the real value or moves a secret into a stored
// credential.
func TestFileDoorRefusesAMaskedExport(t *testing.T) {
	t.Parallel()
	f := newFileFixture(t)
	putServer(t, f.app, legacyRunner("legacy", "1.0.0"))
	p, _ := listed(t, f.base, f.root, "legacy")
	f.put(t, "legacy.yaml", strings.Replace(exported(t, p), "version: 1.0.0", "version: 1.0.1", 1))
	rows := f.bySource(t, f.path("legacy.yaml"))
	if len(rows) != 1 {
		t.Fatalf("drafts of the file = %+v, want one", rows)
	}
	_, v := f.read(t, f.root, strconv.FormatInt(rows[0].ID, 10))
	sentence := "legacy.yaml does not read as an MCP server manifest: App/legacy holds a value that Straza masked for display at "
	fix := "Fix the file. Write the real value in place of the mask, because Straza publishes a file of the apps directory as it is written. " +
		"An argument or an environment value that the stored manifest holds is kept when the file holds the same value. " +
		"To take one secret out of the file, set credential.inject so Straza adds it as a header or an environment entry, and store it with strazactl apps secret set legacy, " +
		"which asks for the value at a hidden prompt. " +
		"Straza injects one secret per server, so a second secret, or one the server takes only as an argument, cannot leave the file yet. Straza proposes it again once it is saved."
	if len(v.Refused) != 1 || !strings.HasPrefix(v.Refused[0].Sentence, sentence) || v.Refused[0].Fix != fix {
		t.Errorf("refused = %+v\nwant the sentence to open %q\nand the fix %q", v.Refused, sentence, fix)
	}
}

// TestInstallRefusesAQuotedPassword pins that a quote in the password of
// an address does not hide it from the secret scan: the install is
// refused as the same address without the quote is, and nothing is stored.
func TestInstallRefusesAQuotedPassword(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	for name, address := range map[string]string{
		"plainpw": "https://svcuser7:Pw0rdS3cr3t@remote.example/mcp",
		"quotepw": "https://svcuser7:It'sPw0rdS3cr3t@remote.example/mcp",
	} {
		t.Run(name, func(t *testing.T) {
			var out map[string]any
			code := rawReq(t, http.MethodPost, base+"/v1/admin/apps", root, "application/yaml", []byte(draftApp(name, strconv.Quote(address), "x")), &out)
			msg, _ := out["error"].(string)
			if code != http.StatusUnprocessableEntity || !strings.Contains(msg, "holds an address that carries a password or a credential before its host") {
				t.Errorf("install = %d %q, want 422 for the password", code, msg)
			}
			if _, err := app.store.Apps().GetByName(context.Background(), name); err == nil {
				t.Error("the refused server was stored")
			}
		})
	}
}

// TestServerAdminCannotTestAGuess pins that the server's own admin, who
// reads every env value masked, gets the same status, error and refusals
// for a wrong and a right guess sent in clear at a hidden place, on a dry
// run, on an install and on a draft's create and publish, for a value
// under a secret's name and for a plain one. The risks and notes of a
// draft's check still differ for a value the scan does not flag.
func TestServerAdminCannotTestAGuess(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	putServer(t, app, legacyRunner("legacy", "1.0.0"))
	if _, err := app.store.Apps().BackfillAdminRoles(ctx); err != nil {
		t.Fatal(err)
	}
	mkHuman(t, app, "erin", "mcp-admin-legacy")
	erin := loginDeviceFlow(t, base, "erin", "hunter2!")
	p, _ := listed(t, base, erin, "legacy")
	doc := strings.Replace(exported(t, p), "version: 1.0.0", "version: 1.0.9", 1)
	guess := func(name, value string) string {
		var mf manager.Manifest
		if err := yaml.Unmarshal([]byte(doc), &mf); err != nil {
			t.Fatal(err)
		}
		for i, e := range mf.Straza.Runtime.Command.Env {
			if e.Name == name {
				mf.Straza.Runtime.Command.Env[i].Value = value
			}
		}
		out, err := yaml.Marshal(mf)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	answer := func(path, body string) string {
		var out map[string]any
		code := rawReq(t, http.MethodPost, base+path, erin, "application/yaml", []byte(body), &out)
		return strconv.Itoa(code) + " " + fmt.Sprint(out["error"])
	}
	// A draft answers at create and again at publish, where the standing
	// of the server admin is judged.
	draft := func(body string) string {
		var out struct {
			Error   string      `json:"error"`
			Draft   wireDraft   `json:"draft"`
			Verdict wireVerdict `json:"verdict"`
		}
		code := adminReq(t, http.MethodPost, base+"/v1/admin/drafts", erin, map[string]any{"documents": []string{body}}, &out)
		refused := []string{}
		for _, f := range out.Verdict.Refused {
			refused = append(refused, f.Sentence)
		}
		created := fmt.Sprint(code, " ", out.Error, " ", refused)
		if code != http.StatusCreated {
			return created
		}
		var pub map[string]any
		code = adminReq(t, http.MethodPost, base+"/v1/admin/drafts/"+out.Draft.ID+"/publish", erin, acksFor(out.Draft.Revision, pubVerdict{}), &pub)
		// The draft's number differs from one draft to the next.
		return created + " then " + strconv.Itoa(code) + " " + strings.Replace(fmt.Sprint(pub["error"]), "draft "+out.Draft.ID+":", "draft N:", 1)
	}
	for _, tc := range []struct{ name, right string }{{"API_TOKEN", "Tk8Hn3Vb5Qe1Sd7F"}, {"LOG_LEVEL", "debugplain"}} {
		wrong, right := guess(tc.name, "wrong-guess-1"), guess(tc.name, tc.right)
		for _, route := range []struct {
			name string
			send func(string) string
		}{
			{"a dry run", func(b string) string { return answer("/v1/admin/apps?dryRun=1", b) }},
			{"an install", func(b string) string { return answer("/v1/admin/apps", b) }},
			{"a draft", draft},
		} {
			t.Run(tc.name+" on "+route.name, func(t *testing.T) {
				w, r := route.send(wrong), route.send(right)
				if w != r {
					t.Errorf("a wrong guess answers %q and the right one %q", w, r)
				}
				hidden := "403 writing a value at straza.runtime.command.env"
				if route.name != "a draft" && !strings.HasPrefix(r, hidden) || route.name == "a draft" && strings.HasPrefix(r, "201") && !strings.Contains(r, "writing a value at straza.runtime.command.env") {
					t.Errorf("the guess answers %q, want a refusal", r)
				}
			})
		}
	}
	if code := adminReq(t, http.MethodGet, base+"/v1/admin/apps", erin, nil, nil); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if row, err := app.store.Apps().GetByName(ctx, "legacy"); err != nil || !strings.Contains(row.Manifest, `"version":"1.0.0"`) {
		t.Errorf("a guess changed the stored manifest: %s %v", row.Manifest, err)
	}
}
