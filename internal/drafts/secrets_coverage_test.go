package drafts

import (
	"encoding/base64"
	"strings"
	"testing"
)

// More fake credentials, built from split prefixes as the ones in
// secrets_test.go are, one for each shape the drafts battery adds to the
// redact battery, and an AWS secret access key, which has no shape of its
// own and is read by the word that names it.
var (
	fakeFineGrained = "github" + "_pat_" + testHex("fine", 22) + "_" + testHex("grained", 59)
	fakeGitLab      = "gl" + "pat-" + testHex("gitlab", 20)
	fakeSlack       = "xo" + "xb-" + "2745010221-2745010221000-" + testDigest("slack", base64.RawURLEncoding.EncodeToString)[:24]
	fakeAnthropic   = "sk-" + "ant-api03-" + testDigest("anthropic", base64.RawURLEncoding.EncodeToString)
	fakeOpenAI      = "sk-" + "proj-" + testDigest("openai", base64.RawURLEncoding.EncodeToString)
	fakeGoogle      = "AI" + "za" + testDigest("google", base64.RawURLEncoding.EncodeToString)[:35]
	fakeJWT         = "ey" + "JhbGciOiJIUzI1NiJ9." + "ey" + "JzdWIiOiIxMjM0In0." + testDigest("jwt", base64.RawURLEncoding.EncodeToString)
	fakeAWSSecret   = "wJ" + "alrXUtnFEMI/" + "K7MDENG/" + "bPxRfiCYzEXAMPLEKEY9"
	fakeCapability  = "q8Lp3Xv9" + "Rt2Wm7Kc" + "4Hb1Nd6Z"
	fakeHF          = "hf" + "_" + "QxWmZtRvKpLnYbHs" + "JdFgCaEuTiOwPzMrNq"
	fakeStripe      = "sk" + "_live_" + testHex("stripe", 32)
)

// testMoreSecrets are the values of this file that no finding may carry.
func testMoreSecrets() []string {
	return []string{fakeFineGrained, fakeGitLab, fakeSlack, fakeAnthropic, fakeOpenAI, fakeGoogle, fakeJWT, fakeAWSSecret, fakeCapability,
		fakeHF, fakeStripe, "s3cr3t-Pa55word!", "Hunter2Hunter2x9", "Tr0ub4dor-and-3", "correcthorsebatterystaple", "/k9Zx7Qp2Lm4Wn8R",
		"Hunter2Hunter2", "Tr0ub4dor3", "s3ss10nV4lu3xyz"}
}

// TestScanSecretsCoverage pins the secrets the scan reads beside the redact
// battery: the token shapes of GitHub fine-grained tokens, GitLab, Slack,
// Anthropic, OpenAI and Google and JSON web tokens, secret names that end in
// a password word or are fused into one, values under a secret's name that
// are words or look like paths without being paths, the address parameters
// auth and passwd, an address whose path carries a random part, a value
// that follows a word naming a secret in free text, an argument after a flag
// that names a secret, a name=value pair under a secret's name inside a
// value, the Hugging Face and Stripe shapes and a Cookie header. The controls
// read no secret where there is none.
func TestScanSecretsCoverage(t *testing.T) {
	refused := func(code, where string) wantSecret { return wantSecret{code, ClassRefused, where} }
	env := func(name, value string) Item {
		return testCommandEnv("        - name: " + name + "\n          value: '" + value + "'\n")
	}
	policy := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: lockdown\nspec:\n  rules:\n    - id: lockdown\n"
	tests := []struct {
		name string
		it   Item
		want []wantSecret
	}{
		{"a GitHub fine-grained token in an argument", testCommandArgs(`["--token", "` + fakeFineGrained + `"]`),
			[]wantSecret{refused(codeSecretShape, "straza.runtime.command.args[1]")}},
		{"a GitLab token in an env value", env("GITLAB", fakeGitLab), []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[GITLAB].value")}},
		{"a Slack token under a name that marks no secret", env("SLACK_BOT", fakeSlack), []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[SLACK_BOT].value")}},
		{"an Anthropic key in an argument", testCommandArgs(`["--api-key", "` + fakeAnthropic + `"]`),
			[]wantSecret{refused(codeSecretShape, "straza.runtime.command.args[1]")}},
		{"an OpenAI key in an env value", env("LLM", fakeOpenAI), []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[LLM].value")}},
		{"a Google API key in an env value", env("MAPS", fakeGoogle), []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[MAPS].value")}},
		{"a JSON web token in an env value", env("SESSION", fakeJWT), []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[SESSION].value")}},
		{"a password under a fused name", env("PGPASSWORD", "s3cr3t-Pa55word!"), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[PGPASSWORD].value")}},
		{"a password under pwd", env("MYSQL_PWD", "Hunter2Hunter2x9"), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[MYSQL_PWD].value")}},
		{"a password under pass", env("REDIS_PASS", "Tr0ub4dor-and-3"), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[REDIS_PASS].value")}},
		{"a password of letters only", env("DB_PASSWORD", "correcthorsebatterystaple"), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[DB_PASSWORD].value")}},
		{"a plain word under a secret's name", env("SORT_KEY", "name"), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[SORT_KEY].value")}},
		{"a key that starts with a slash and is no path", env("API_KEY", "/k9Zx7Qp2Lm4Wn8R"), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[API_KEY].value")}},
		{"a secret query parameter named auth", testRemoteURL("https://api.example.com/mcp?auth=" + testHex("auth", 20)),
			[]wantSecret{refused(codeSecretQuery, "straza.runtime.remote.url")}},
		{"a secret query parameter named passwd", testRemoteURL("https://api.example.com/mcp?passwd=hunter2x"),
			[]wantSecret{refused(codeSecretQuery, "straza.runtime.remote.url")}},
		{"a random part in the path of an address", testRemoteURL("https://hooks.example.com/services/T0AB12CD3/B0EF45GH6/" + fakeCapability),
			[]wantSecret{refused(codeSecretPath, "straza.runtime.remote.url")}},
		{"a Slack token in a role description", testRole("bot " + fakeSlack), []wantSecret{refused(codeSecretShape, "spec.description")}},
		{"an AWS secret access key after the word key", testRole("key " + fakeAWSSecret), []wantSecret{refused(codeSecretValue, "spec.description")}},
		{"an Anthropic key in a policy comment", testPolicy(policy + "    # temp key " + fakeAnthropic + "\n      tools: [mcp.call]\n      effect: deny\n"),
			[]wantSecret{refused(codeSecretShape, "line 8")}},
		{"a password in the argument after a flag that names it", testCommandArgs(`[--password, Hunter2Hunter2]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a password in a flag=value argument", testCommandArgs(`["--password=correcthorsebatterystaple"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[0]")}},
		{"a password inside a connection string", env("DATABASE_DSN", "host=db user=app password=Tr0ub4dor3 dbname=app"),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.env[DATABASE_DSN].value")}},
		{"a Hugging Face token of letters only", env("HF_HUB", fakeHF), []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[HF_HUB].value")}},
		{"a Stripe live key in an argument", testCommandArgs(`["--stripe", "` + fakeStripe + `"]`),
			[]wantSecret{refused(codeSecretShape, "straza.runtime.command.args[1]")}},
		{"a password after a flag inside a shell command", testCommandArgs(`[-c, "server --password Hunter2Hunter2"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a short key after a flag inside a shell command", testCommandArgs(`[-c, "exec mcp-db --api-key s3cr3tK3y"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a short X-API-Key header line in an argument", testCommandArgs(`[--header, "X-API-Key: s3cr3tK3y"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a Basic Authorization header line in an argument", testCommandArgs(`[--header, "Authorization: Basic dXNlcjpwYXNz"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a Cookie header line in an argument", testCommandArgs(`[--header, "Cookie: session=s3ss10nV4lu3xyz"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a JSON key pair in an argument", testCommandArgs(`["--config", "{\"api_key\": \"Hunter2Hunter2\"}"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a JSON key pair in an env value", env("CONFIG", `{"api_key": "Hunter2Hunter2"}`), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[CONFIG].value")}},
		{"a compact JSON pair in an env value", env("CONFIG", `{"password":"Hunter2Hunter2"}`), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[CONFIG].value")}},
		{"a YAML pair in an env value", env("CONFIG", "password: Hunter2Hunter2"), []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[CONFIG].value")}},
		{"a dash-led password after a flag that names it", testCommandArgs(`[--password, "-Hunter2Hunter2"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a dash-led password with symbols after a flag that names it", testCommandArgs(`[--db-password, "-x9Kp!2mQ#vL"]`),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a dash-led password in block style", testCommandArgs("\n        - --password\n        - -k7Rq2Wm9Xp4Ln8Tz3Vb"),
			[]wantSecret{refused(codeSecretValue, "straza.runtime.command.args[1]")}},
		{"a session cookie in a header", testRegistry("  remotes:\n    - type: streamable-http\n      url: https://mcp.example.com/mcp\n      headers:\n" +
			"        - name: Cookie\n          value: session=s3ss10nV4lu3xyz\n"), []wantSecret{refused(codeSecretValue, "server.remotes[0].headers[Cookie].value")}},

		// Controls: none of these is a secret.
		{"words that stay benign under a secret's name", testCommandEnv("        - name: REQUIRE_TOKEN\n          value: 'On'\n        - name: AUTH\n          value: none\n" +
			"        - name: CACHE_KEY\n          value: 42\n        - name: PGPASSFILE\n          value: /run/secrets/pgpass\n" +
			"        - name: GOOGLE_APPLICATION_CREDENTIALS\n          value: ./gcp/service-account.json\n        - name: SERVICE_ACCOUNT_KEY\n          value: /key.json\n"), nil},
		{"names that only look like secret words", testCommandEnv("        - name: BYPASS\n          value: cache\n        - name: OAUTH\n          value: entra\n" +
			"        - name: MONKEY\n          value: banana\n        - name: KEYWORDS\n          value: mcp\n"), nil},
		{"identifiers in the path of an address", testRemoteURL("https://remote.example.net/9b2f6c1e-3d4a-4e8b-9f7c-2a1d5e6b8c9d/github/v2"), nil},
		{"a short hex id in the path of an address", testRemoteURL("https://api.example.com/orgs/507f1f77bcf86cd799439011/mcp"), nil},
		{"a role description that names a key and a date", testRole("Rotates the signing key 2024-11-05-release-candidate for service-account-2024"), nil},
		{"a word that starts like a key", testCommandArgs(`["--mode", "task-runner-for-the-kubernetes-cluster", "sk-learn-compatible-estimator-wrapper"]`), nil},
		{"benign pairs in a value", testCommandEnv("        - name: OPTIONS\n          value: 'MODE=fast retries=3'\n        - name: DSN\n          value: 'host=db password= sslmode=off'\n"), nil},
		{"arguments after flags that name a secret and take no secret", testCommandArgs(
			`[--mode=fast, --no-auth, --port, "8080", --token, "${GITHUB_TOKEN}", --auth, none, --password-file, /run/secrets/pw, --api-key, "{api_key}"]`), nil},
		{"flags after a switch that names a secret", testCommandArgs(`[--no-auth, "--port=8080", --require-token, -v, --no-auth, --log-level=debug, --no-auth, --http2]`), nil},
		{"a shell command and header lines that carry no secret", testCommandArgs(
			`[-c, "server --no-auth --port 8080", --header, "Authorization: Bearer ${TOKEN}", --header, "X-API-Key: {api_key}", "--config", "{\"api_key\": \"\"}"]`), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanSecrets(tc.it)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d findings, want %d:\n%s", len(got), len(tc.want), testDescribe(got))
			}
			for i, w := range tc.want {
				if f := got[i]; f.Code != w.code || f.Class != w.class || !strings.Contains(f.Sentence, ", "+w.where+" ") {
					t.Errorf("finding %d is %s %s %q, want %s %s at %q", i, f.Code, f.Class, f.Sentence, w.code, w.class, w.where)
				}
			}
			testFindingRules(t, tc.it.Object(), got)
			for _, f := range got {
				for _, secret := range testMoreSecrets() {
					if strings.Contains(f.Sentence+f.Fix, secret) {
						t.Errorf("finding %s repeats a secret value: %q", f.Code, f.Sentence)
					}
				}
			}
		})
	}
	if got := ScanNote("Use " + fakeFineGrained + " for now."); len(got) != 1 || got[0].Code != codeSecretShape {
		t.Errorf("a note with a GitHub fine-grained token drew:\n%s", testDescribe(got))
	}
}
