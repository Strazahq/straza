package drafts

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestWithoutSecrets pins what WithoutSecrets answers: every string the
// secret scan flags replaced whole by a quoted redact.Mark, each flagged
// comment and each flagged line of text that does not decode masked where
// the secret stands, and every other byte as it was.
func TestWithoutSecrets(t *testing.T) {
	stored := func(env string) string {
		return `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"runner","description":"Ünïcode runner"},` +
			`"server":{"name":"io.x/runner","version":"1.0.0"},"straza":{"runtime":{"kind":"command","command":{"exec":"/usr/bin/runner","env":[` + env + `]}}}}`
	}
	command := func(tail string) string {
		return testHead + testServer + "straza:\n  runtime:\n    kind: command\n    command:\n      exec: npx\n" + tail
	}
	remote := func(tail string) string {
		return testHead + testServer + "straza:\n  runtime:\n    kind: remote\n    remote:\n" + tail
	}
	body := testDigest("pem-body", base64.StdEncoding.EncodeToString)
	key := "            " + fakeKey + "\n            " + body + "\n            -----END " + "RSA PRIVATE KEY-----\n"
	tests := []struct {
		name, doc string
		want      string // empty when the document comes back as it is
	}{
		{"a stored manifest with a token in an env value",
			stored(`{"name":"GITHUB_TOKEN","value":"` + fakeGitHub + `"}`),
			stored(`{"name":"GITHUB_TOKEN","value":"[REDACTED]"}`)},
		{"a stored manifest with nothing to mask",
			stored(`{"name":"LOG_LEVEL","value":"debug"},{"name":"TLS_KEY","value":"/etc/tls/key.pem"}`), ""},
		{"a password written with escapes under a secret's name",
			stored(`{"name":"DB_PASSWORD","value":"p\"ss\u0026w0rd"}`),
			stored(`{"name":"DB_PASSWORD","value":"[REDACTED]"}`)},
		{"a token in a quoted argument",
			command(`      args: ["--token", "` + fakeGitHub + `"]` + "\n"),
			command(`      args: ["--token", "[REDACTED]"]` + "\n")},
		{"a long random argument",
			command("      args: [--seed, " + fakeRandom + "]\n"),
			command("      args: [--seed, '[REDACTED]']\n")},
		{"a password after a flag that names it and in a flag=value argument",
			command("      args: [--password, Hunter2Hunter2, \"--db-password=correcthorsebatterystaple\", --port, \"8080\"]\n"),
			command("      args: [--password, '[REDACTED]', \"[REDACTED]\", --port, \"8080\"]\n")},
		{"a password inside a connection string in a stored manifest",
			stored(`{"name":"DATABASE_DSN","value":"host=db user=app password=Tr0ub4dor3 dbname=app"}`),
			stored(`{"name":"DATABASE_DSN","value":"[REDACTED]"}`)},
		{"a plain value under a secret's name",
			command("      env:\n        DB_PASSWORD: hunter2\n        LOG_LEVEL: debug\n"),
			command("      env:\n        DB_PASSWORD: '[REDACTED]'\n        LOG_LEVEL: debug\n")},
		{"a private key in a literal block",
			command("      env:\n        - name: TLS_PRIVATE\n          value: |\n" + key + "        - name: LOG_LEVEL\n          value: debug\n"),
			command("      env:\n        - name: TLS_PRIVATE\n          value: '[REDACTED]'\n        - name: LOG_LEVEL\n          value: debug\n")},
		{"an anchored token and its alias",
			command("      env:\n        - name: GITHUB_TOKEN\n          value: &tok " + fakeGitHub + "\n        - name: GH_TOKEN\n          value: *tok\n"),
			command("      env:\n        - name: GITHUB_TOKEN\n          value: &tok '[REDACTED]'\n        - name: GH_TOKEN\n          value: *tok\n")},
		{"a password in the remote address",
			remote("      url: 'https://bob:" + fakePass + "@mcp.example.com/mcp'\n"),
			remote("      url: \"https://%5BREDACTED%5D@mcp.example.com/mcp\"\n")},
		{"a password in a stored address",
			`{"kind":"App","straza":{"runtime":{"kind":"remote","remote":{"url":"https://robot:` + fakePass + `@api.example.com/mcp"}}}}`,
			`{"kind":"App","straza":{"runtime":{"kind":"remote","remote":{"url":"https://%5BREDACTED%5D@api.example.com/mcp"}}}}`},
		{"a capability in the path of the remote address",
			remote("      url: https://hooks.example.com/services/T0AB12CD3/B0EF45GH6/" + fakeCapability + "\n"),
			remote("      url: \"https://hooks.example.com/services/T0AB12CD3/B0EF45GH6/[REDACTED]\"\n")},
		{"a secret query value in the remote address",
			remote("      url: 'https://mcp.example.com/mcp?region=eu&api_key=abc123'\n"),
			remote("      url: \"https://mcp.example.com/mcp?region=eu&api_key=[REDACTED]\"\n")},
		{"an address with a password inside an argument",
			command("      args: [mcp-remote, \"https://relay:" + fakePass + "@relay.example.com/sse\"]\n"),
			command("      args: [mcp-remote, \"https://%5BREDACTED%5D@relay.example.com/sse\"]\n")},
		{"a token in a head comment",
			remote("      # rotated: " + fakeGitHub + "\n      url: https://mcp.example.com/mcp\n"),
			remote("      # rotated: [REDACTED]\n      url: https://mcp.example.com/mcp\n")},
		{"a value after a word that names a secret in a comment",
			remote("      # key: " + fakeAWSSecret + "\n      url: https://mcp.example.com/mcp\n"),
			remote("      # key: [REDACTED]\n      url: https://mcp.example.com/mcp\n")},
		{"a token in a description over two lines",
			"apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: github\n  description: rotate\n    " + fakeGitHub + " soon\n" + testServer + testRemote,
			"apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: github\n  description: '[REDACTED]'\n" + testServer + testRemote},
		{"a key that holds a token",
			testHead + "server:\n  name: io.example/acme\n  version: \"1.0.0\"\n  notes:\n    '" + fakeBearer + "': seen\n" + testRemote,
			testHead + "server:\n  name: io.example/acme\n  version: \"1.0.0\"\n  notes:\n    '[REDACTED]': seen\n" + testRemote},
		{"a shape outside {{secret}} in the template",
			remote("      url: https://mcp.example.com/mcp\n  credential:\n    kind: static\n    inject:\n      as: header\n      name: X-Auth\n      template: '{{secret}} " + fakeAWS + "'\n"),
			remote("      url: https://mcp.example.com/mcp\n  credential:\n    kind: static\n    inject:\n      as: header\n      name: X-Auth\n      template: '[REDACTED]'\n")},
		{"lines that end in a carriage return",
			strings.ReplaceAll(command("      env:\n        - name: GITHUB_TOKEN\n          value: "+fakeGitHub+"\n"), "\n", "\r\n"),
			strings.ReplaceAll(command("      env:\n        - name: GITHUB_TOKEN\n          value: '[REDACTED]'\n"), "\n", "\r\n")},
		{"a document that does not decode",
			"apiVersion: [unclosed\ntoken: " + fakeGitHub + "\n",
			"apiVersion: [unclosed\ntoken: [REDACTED]\n"},
		{"a block with an explicit indentation indicator",
			"apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: github\n  description: |2\n     rotate " + fakeGitHub + "\n" + testServer + testRemote,
			"[REDACTED]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = tc.doc
			}
			got, changed := WithoutSecrets(tc.doc)
			if got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
			if changed != (want != tc.doc) {
				t.Errorf("changed = %v, want %v", changed, want != tc.doc)
			}
			for _, secret := range append(append(testSecrets(), testMoreSecrets()...), body) {
				if strings.Contains(got, secret) {
					t.Errorf("the answer still holds %q", secret)
				}
			}
		})
	}
}
