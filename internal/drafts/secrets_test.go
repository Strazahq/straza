package drafts

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The fake credentials are built at run time from split prefixes and the
// hex of a digest, so the source holds no credential-shaped literal for a
// repository scanner to report, while each value matches its shape with a
// body as varied as a real one.
var (
	fakeGitHub = "gh" + "p_" + testHex("github", 36)
	fakeAWS    = "AK" + "IA" + strings.ToUpper(testHex("aws", 16))
	fakeStraza = "ws" + "t_" + testHex("straza", 24)
	fakeBearer = "Bearer " + testHex("bearer", 24)
	fakeKey    = "-----BEGIN " + "RSA PRIVATE KEY-----"
	fakePass   = "s3cr3t" + "pass"
	fakeBasic  = "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+fakePass))
	// fakeExample is the kind of example a registry record prints as a
	// placeholder, which is no credential.
	fakeExample = "Bearer hf" + "_" + strings.Repeat("x", 29)
	// fakeRandom and fakeHex read like generated secrets: the base64 and the
	// hex of a digest.
	fakeRandom = testDigest("draft-random", base64.RawURLEncoding.EncodeToString)
	fakeHex    = testHex("draft-hex", 32)
)

func testDigest(seed string, encode func([]byte) string) string {
	sum := sha256.Sum256([]byte(seed))
	return encode(sum[:])
}

func testHex(seed string, n int) string { return testDigest(seed, hex.EncodeToString)[:n] }

// testSecrets are every secret value the cases use. No finding may carry one.
func testSecrets() []string {
	return []string{fakeGitHub, fakeAWS, fakeStraza, fakeBearer[len("Bearer "):], fakeKey, fakePass, fakeBasic[len("Basic "):],
		fakeRandom, fakeHex, "hunter2", "abc123", "plain-text-value"}
}

const (
	testHead   = "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: github\n"
	testServer = "server:\n  name: io.github.github/github-mcp-server\n  version: \"0.17.1\"\n"
	testRemote = "straza:\n  runtime:\n    kind: remote\n    remote:\n      url: https://mcp.example.com/mcp\n"
)

func testApp(doc string) Item { return Item{Kind: KindApp, Name: "github", Op: OpPut, Doc: doc} }

// testCommandEnv is a command server whose env list holds the given entries,
// each already indented as a list item.
func testCommandEnv(entries string) Item {
	return testApp(testHead + testServer + "straza:\n  runtime:\n    kind: command\n    command:\n      exec: npx\n      env:\n" + entries)
}

func testCommandArgs(args string) Item {
	return testApp(testHead + testServer + "straza:\n  runtime:\n    kind: command\n    command:\n      exec: npx\n      args: " + args + "\n")
}

func testRemoteURL(url string) Item {
	return testApp(testHead + testServer + "straza:\n  runtime:\n    kind: remote\n    remote:\n      url: '" + url + "'\n")
}

// testRegistry is a remote server whose server block copies a registry
// record with the given remotes or packages block.
func testRegistry(block string) Item {
	return testApp(testHead + "server:\n  name: io.example/acme\n  version: \"1.0.0\"\n" + block + testRemote)
}

func testRole(description string) Item {
	return Item{Kind: KindRole, Name: "dev", Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: dev\nspec:\n    kind: application\n    description: '" + description + "'\n"}
}

func testPolicy(doc string) Item {
	return Item{Kind: KindPolicySet, Name: "lockdown", Op: OpPut, Doc: doc}
}

type wantSecret struct {
	code  string
	class Class
	where string
}

func TestScanSecrets(t *testing.T) {
	refused := func(code, where string) wantSecret { return wantSecret{code, ClassRefused, where} }
	warned := func(where string) wantSecret { return wantSecret{codeSecretEntropy, ClassWarning, where} }
	policy := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: lockdown\nspec:\n  rules:\n    - id: lockdown\n"
	tests := []struct {
		name   string
		it     Item
		want   []wantSecret
		object string // the Object every finding carries, when not it.Object()
	}{
		// App: the credential shapes, in any string.
		{name: "a GitHub token in an env value", it: testCommandEnv("        - name: GITHUB_TOKEN\n          value: " + fakeGitHub + "\n"),
			want: []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[GITHUB_TOKEN].value")}},
		{name: "a private key header in an argument", it: testCommandArgs(`["--key", "` + fakeKey + `"]`),
			want: []wantSecret{refused(codeSecretShape, "straza.runtime.command.args[1]")}},
		{name: "a Straza token in the description", it: testApp(testHead + "  description: 'rotate " + fakeStraza + "'\n" + testServer + testRemote),
			want: []wantSecret{refused(codeSecretShape, "metadata.description")}},
		{name: "an AWS key in a registry header", it: testRegistry("  remotes:\n    - type: streamable-http\n      url: https://mcp.example.com/mcp\n      headers:\n        - name: X-Api-Key\n          value: " + fakeAWS + "\n"),
			want: []wantSecret{refused(codeSecretShape, "server.remotes[0].headers[X-Api-Key].value")}},
		{name: "a bearer token in a key of the server block", it: testRegistry("  notes:\n    '" + fakeBearer + "': seen\n"),
			want: []wantSecret{refused(codeSecretShape, "a key under server.notes")}},
		{name: "an example token in a registry placeholder", it: testRegistry("  remotes:\n    - type: streamable-http\n      url: https://huggingface.co/mcp\n      headers:\n        - name: Authorization\n          isSecret: true\n          placeholder: " + fakeExample + "\n")},
		{name: "a token in a head comment", it: testApp(testHead + testServer + "straza:\n  runtime:\n    kind: remote\n    remote:\n      # rotated: " + fakeGitHub + "\n      url: https://mcp.example.com/mcp\n"),
			want: []wantSecret{refused(codeSecretShape, "a comment on line 12")}},
		{name: "a token in a line comment", it: testApp(testHead + testServer + "straza:\n  runtime:\n    kind: remote\n    remote:\n      url: https://mcp.example.com/mcp # was " + fakeGitHub + "\n"),
			want: []wantSecret{refused(codeSecretShape, "a comment on line 12")}},
		{name: "a token in a second YAML document", it: testApp(testHead + testServer + testRemote + "---\nnotes: " + fakeGitHub + "\n"),
			want: []wantSecret{refused(codeSecretShape, "notes")}},
		{name: "a document that holds only a comment", it: testApp("# " + fakeGitHub + "\n"),
			want: []wantSecret{refused(codeSecretShape, "line 1")}},
		{name: "a document that does not decode", it: testApp("apiVersion: [unclosed\ntoken: " + fakeGitHub + "\n"),
			want: []wantSecret{refused(codeSecretShape, "line 2")}},
		{name: "a JSON manifest", it: testApp(`{"apiVersion": "straza.dev/v1beta1", "kind": "App", "metadata": {"name": "github"},` +
			`"server": {"name": "io.github.github/github-mcp-server", "version": "0.17.1"},` +
			`"straza": {"runtime": {"kind": "command", "command": {"exec": "npx", "env": [{"name": "GITHUB_TOKEN", "value": "` + fakeGitHub + `"}]}}}}`),
			want: []wantSecret{refused(codeSecretShape, "straza.runtime.command.env[GITHUB_TOKEN].value")}},

		// App: values under a secret's name.
		{name: "a plain value under a secret's name", it: testCommandEnv("        - name: API_KEY\n          value: hunter2\n"),
			want: []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[API_KEY].value")}},
		{name: "a word with digits under a secret's name", it: testCommandEnv("        - name: API_KEY\n          value: abc123\n"),
			want: []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[API_KEY].value")}},
		{name: "a camelCase secret name in an oci env", it: testApp(testHead + testServer + "straza:\n  runtime:\n    kind: oci\n    oci:\n      image: ghcr.io/acme/mcp:1\n      env:\n        - name: apiKey\n          value: hunter2\n"),
			want: []wantSecret{refused(codeSecretValue, "straza.runtime.oci.env[apiKey].value")}},
		{name: "an env mapping from name to value", it: testApp(testHead + testServer + "straza:\n  runtime:\n    kind: command\n    command:\n      exec: npx\n      env:\n        DB_PASSWORD: hunter2\n"),
			want: []wantSecret{refused(codeSecretValue, "straza.runtime.command.env.DB_PASSWORD")}},
		{name: "an isSecret entry takes no plain word", it: testRegistry("  packages:\n    - registryType: npm\n      identifier: acme-mcp\n      environmentVariables:\n        - name: SERVICE_CONFIG\n          isSecret: true\n          value: plain-text-value\n"),
			want: []wantSecret{refused(codeSecretValue, "server.packages[0].environmentVariables[SERVICE_CONFIG].value")}},
		{name: "a registry argument under a secret's name", it: testRegistry("  packages:\n    - registryType: npm\n      identifier: acme-mcp\n      packageArguments:\n        - type: named\n          name: --voyageApiKey\n          value: hunter2\n"),
			want: []wantSecret{refused(codeSecretValue, "server.packages[0].packageArguments[0].value")}},
		{name: "a literal Authorization header", it: testRegistry("  remotes:\n    - type: streamable-http\n      url: https://mcp.example.com/mcp\n      headers:\n        - name: Authorization\n          value: " + fakeBasic + "\n"),
			want: []wantSecret{refused(codeSecretValue, "server.remotes[0].headers[Authorization].value")}},
		{name: "a random value under a secret's name is refused, not warned", it: testCommandEnv("        - name: API_KEY\n          value: " + fakeRandom + "\n"),
			want: []wantSecret{refused(codeSecretValue, "straza.runtime.command.env[API_KEY].value")}},
		{name: "an env placeholder", it: testCommandEnv("        - name: API_KEY\n          value: '{api_key}'\n        - name: GITHUB_TOKEN\n          value: '${GITHUB_TOKEN}'\n")},
		{name: "an empty env value", it: testCommandEnv("        - name: DB_PASSWORD\n          value: ''\n")},
		{name: "a null env value", it: testCommandEnv("        - name: DB_PASSWORD\n          value: ~\n        - name: API_KEY\n          value: null\n")},
		{name: "benign values under a secret's name", it: testCommandEnv("        - name: REQUIRE_TOKEN\n          value: true\n" +
			"        - name: TLS_KEY\n          value: /etc/tls/key.pem\n        - name: CLIENT_KEY\n          value: 'C:\\certs\\client.key'\n" +
			"        - name: SSH_KEY\n          value: ~/.ssh/id_ed25519\n        - name: CACHE_KEY\n          value: 42\n")},
		{name: "a name that only mentions a token", it: testCommandEnv("        - name: TOKEN_URL\n          value: https://auth.example.com/token\n")},

		// App: addresses.
		{name: "a user and password in the remote address", it: testRemoteURL("https://bob:" + fakePass + "@mcp.example.com/mcp"),
			want: []wantSecret{refused(codeSecretUserinfo, "straza.runtime.remote.url")}},
		{name: "a quote in the password of the remote address", it: testRemoteURL("https://bob:It''sPw0rd@mcp.example.com/mcp?region=eu"),
			want: []wantSecret{refused(codeSecretUserinfo, "straza.runtime.remote.url")}},
		{name: "a quote in the query before a secret parameter", it: testRemoteURL("https://mcp.example.com/mcp?note=it''s&api_key=abc123"),
			want: []wantSecret{refused(codeSecretQuery, "straza.runtime.remote.url")}},
		{name: "quoted addresses joined by a comma in an argument", it: testCommandArgs(`["--urls=['http://a.example.com/x','http://bob:` + fakePass + `@b.example.com/y']"]`),
			want: []wantSecret{refused(codeSecretUserinfo, "straza.runtime.command.args[0]")}},
		{name: "a token as the user of an address", it: testRemoteURL("https://" + fakeGitHub + "@github.com/acme/mcp"),
			want: []wantSecret{refused(codeSecretShape, "straza.runtime.remote.url"), refused(codeSecretUserinfo, "straza.runtime.remote.url")}},
		{name: "a password in an address inside the description", it: testApp(testHead + "  description: 'mirror of https://ci:" + fakePass + "@git.example.com/repo'\n" + testServer + testRemote),
			want: []wantSecret{refused(codeSecretUserinfo, "metadata.description")}},
		{name: "a user name alone in an address", it: testApp(testHead + "  description: 'mirror of https://deploy@git.example.com/repo'\n" + testServer + testRemote)},
		{name: "an ssh address with a user in an argument", it: testCommandArgs(`["--from", "git+ssh://git@github.com/acme/mcp.git", "acme-mcp"]`)},
		{name: "a secret query parameter", it: testRemoteURL("https://mcp.example.com/mcp?region=eu&api_key=abc123"),
			want: []wantSecret{refused(codeSecretQuery, "straza.runtime.remote.url")}},
		{name: "an at sign in the path is not a user part", it: testRemoteURL("https://server.smithery.ai:443/@Hint-Services/obsidian-github-mcp/mcp")},
		{name: "query parameters that name no secret", it: testRemoteURL("https://huggingface.co/mcp?login&version=2")},
		{name: "a query placeholder", it: testRemoteURL("https://mcp.example.com/mcp?token={token}")},
		{name: "a benign query value", it: testRemoteURL("https://mcp.example.com/mcp?token=none")},

		// App: the injection template.
		{name: "a shape outside {{secret}} in the template", it: testApp(testHead + testServer + testRemote + "  credential:\n    kind: static\n    inject:\n      as: header\n      name: X-Auth\n      template: '{{secret}} " + fakeAWS + "'\n"),
			want: []wantSecret{refused(codeSecretTemplate, "straza.credential.inject.template")}},
		{name: "the registry import's Bearer {{secret}} template", it: testApp(testHead + "server:\n  name: ai.smithery/Hint-Services-obsidian-github-mcp\n  version: \"0.4.0\"\n" +
			"  remotes:\n    - type: streamable-http\n      url: https://server.smithery.ai/@Hint-Services/obsidian-github-mcp/mcp\n" +
			"      headers:\n        - name: Authorization\n          isSecret: true\n          value: Bearer {smithery_api_key}\n" +
			"straza:\n  runtime:\n    kind: remote\n    remote:\n      url: https://server.smithery.ai/@Hint-Services/obsidian-github-mcp/mcp\n" +
			"  credential:\n    kind: static\n    inject:\n      as: header\n      name: Authorization\n      template: \"Bearer {{secret}}\"\n")},

		// App: long random-looking values warn.
		{name: "a long random env value", it: testCommandEnv("        - name: CONFIG_BLOB\n          value: " + fakeRandom + "\n"),
			want: []wantSecret{warned("straza.runtime.command.env[CONFIG_BLOB].value")}},
		{name: "a long random hex argument", it: testCommandArgs(`["--seed", "` + fakeHex + `"]`),
			want: []wantSecret{warned("straza.runtime.command.args[1]")}},
		{name: "a long random registry header value", it: testRegistry("  remotes:\n    - type: streamable-http\n      url: https://mcp.example.com/mcp\n      headers:\n        - name: X-Trace\n          value: " + fakeRandom + "\n"),
			want: []wantSecret{warned("server.remotes[0].headers[X-Trace].value")}},
		{name: "a UUID", it: testCommandEnv("        - name: TENANT_ID\n          value: 9b2f6c1e-3d4a-4e8b-9f7c-2a1d5e6b8c9d\n        - name: RESOURCE\n          value: urn_uuid_550e8400-e29b-41d4-a716-446655440000\n")},
		{name: "a certificate is not a secret", it: testCommandEnv("        - name: CA_CERT\n          value: |\n            -----BEGIN CERTIFICATE-----\n            MIIBszCCAVmgAwIBAgIUQ\n            -----END CERTIFICATE-----\n")},
		{name: "an identifier with a year", it: testCommandArgs(`["mcp-server-kubernetes-readonly-profile-2025", "--retry-backoff-multiplier-seconds-2"]`)},

		// App: an alias bomb and a list that names itself stay linear.
		{name: "an alias bomb", it: testApp("a: &a [x, x, x, x, x, x, x, x, x, x]\nb: &b [*a, *a, *a, *a, *a, *a, *a, *a, *a, *a]\nc: &c [*b, *b, *b, *b, *b, *b, *b, *b, *b, *b]\n" +
			"d: &d [*c, *c, *c, *c, *c, *c, *c, *c, *c, *c]\ne: &e [*d, *d, *d, *d, *d, *d, *d, *d, *d, *d]\nf: &f [*e, *e, *e, *e, *e, *e, *e, *e, *e, *e]\n" +
			"g: &g [*f, *f, *f, *f, *f, *f, *f, *f, *f, *f]\nh: &h [*g, *g, *g, *g, *g, *g, *g, *g, *g, *g]\nenv: &env [*h, *env, {<<: *env}]\n")},

		// Role: every string, the description first.
		{name: "a GitHub token in a role description", it: testRole("Developer seat " + fakeGitHub),
			want: []wantSecret{refused(codeSecretShape, "spec.description")}},
		{name: "a password in an address in a role description", it: testRole("Wiki at https://dev:" + fakePass + "@wiki.example.com/dev"),
			want: []wantSecret{refused(codeSecretUserinfo, "spec.description")}},
		{name: "a plain role", it: testRole("Developer seat: governed demo-tools access")},
		{name: "a role whose name holds a token", it: Item{Kind: KindRole, Name: fakeGitHub, Op: OpPut,
			Doc: "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: " + fakeGitHub + "\nspec:\n    kind: application\n"},
			want: []wantSecret{refused(codeSecretShape, "metadata.name")}, object: "Role/(name withheld)"},

		// PolicySet: the raw text, comments included.
		{name: "a token in a policy comment", it: testPolicy(policy + "      # break-glass token: " + fakeGitHub + "\n      tools: [shell.exec]\n      effect: deny\n"),
			want: []wantSecret{refused(codeSecretShape, "line 8")}},
		{name: "a Straza token in a rule reason", it: testPolicy(policy + "      tools: [shell.exec]\n      effect: deny\n      reason: 'use " + fakeStraza + "'\n"),
			want: []wantSecret{refused(codeSecretShape, "line 10")}},
		{name: "a plain policy", it: testPolicy(policy + "      tools: [shell.exec]\n      effect: deny\n      reason: \"Lockdown in effect. Contact security\"\n")},
		{name: "an address pattern in a command pattern", it: testPolicy(policy + "      tools: [shell.exec]\n      command:\n        allowPatterns: [\"git clone ssh://git@github.com/*\", \"curl https://*:*@api.example.com/*?token=*\"]\n      effect: allow\n")},

		// A removal carries no document.
		{name: "a removal", it: Item{Kind: KindApp, Name: "github", Op: OpRemove}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A YAML document that does not decode is read raw, so a negative
			// control must decode, or it would pass without the field rules.
			var probe yaml.Node
			if tc.it.Kind != KindPolicySet && tc.it.Doc != "" && tc.want == nil && yaml.Unmarshal([]byte(tc.it.Doc), &probe) != nil {
				t.Fatalf("the document of a negative control must decode as YAML")
			}
			got := ScanSecrets(tc.it)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d findings, want %d:\n%s", len(got), len(tc.want), testDescribe(got))
			}
			for i, w := range tc.want {
				f := got[i]
				if f.Code != w.code || f.Class != w.class || !strings.Contains(f.Sentence, ", "+w.where+" ") {
					t.Errorf("finding %d is %s %s %q, want %s %s at %q", i, f.Code, f.Class, f.Sentence, w.code, w.class, w.where)
				}
			}
			object := tc.object
			if object == "" {
				object = tc.it.Object()
			}
			testFindingRules(t, object, got)
		})
	}
}

// testFindingRules checks what every secret finding keeps to: its object,
// its code and class, plain sentences, and never a secret value.
func testFindingRules(t *testing.T, object string, got []Finding) {
	t.Helper()
	for _, f := range got {
		if f.Object != object {
			t.Errorf("finding object %q, want %q", f.Object, object)
		}
		want := ClassRefused
		if f.Code == codeSecretEntropy || (f.Code == codeSecretMore && f.Class == ClassWarning) {
			want = ClassWarning
		}
		if !strings.HasPrefix(f.Code, "secret.") || f.Class != want {
			t.Errorf("finding %s has class %s", f.Code, f.Class)
		}
		for _, text := range []string{f.Sentence, f.Fix} {
			if !strings.HasSuffix(text, ".") || strings.ContainsAny(text, "\u2014;") {
				t.Errorf("text %q must end with a full stop and hold no em dash or semicolon", text)
			}
		}
		for _, secret := range testSecrets() {
			if strings.Contains(f.Sentence+f.Fix+f.Object+f.Code, secret) {
				t.Errorf("finding %s repeats a secret value: %q", f.Code, f.Sentence)
			}
		}
	}
}

func testDescribe(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("  " + f.Code + " " + string(f.Class) + ": " + f.Sentence + "\n")
	}
	return b.String()
}

// TestScanSecretsWords pins the operator's words of a refusal and a warning
// for each kind, and of a finding whose object's name is withheld.
func TestScanSecretsWords(t *testing.T) {
	appTail := func(name string) string {
		return " If the server needs the secret, publish the draft without it, then store it with strazactl apps secret set " + name + ", which asks for the value at a hidden prompt, and set credential.inject so Straza adds it for you."
	}
	withheld := testApp(strings.Replace(testHead, "name: github", "name: "+fakeGitHub, 1) + testServer + testRemote)
	withheld.Name = fakeGitHub
	tests := []struct {
		name, sentence, fix string
		it                  Item
	}{
		{"app refusal",
			"In the server github, straza.runtime.command.env[GITHUB_TOKEN].value holds what looks like a GitHub token. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"Remove it. If the server needs the secret, set credential.kind: static and credential.inject with as: env, name: GITHUB_TOKEN and template: {{secret}}, " +
				"publish the draft, then store the secret with strazactl apps secret set github, which asks for the value at a hidden prompt.",
			testCommandEnv("        - name: GITHUB_TOKEN\n          value: " + fakeGitHub + "\n")},
		{"app value under a secret's name in a container's env",
			"In the server github, straza.runtime.oci.env[CRM_API_KEY].value holds a value, and its name marks it as a secret. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"Remove the entry. If the server needs the secret, set credential.kind: static and credential.inject with as: env, name: CRM_API_KEY and template: {{secret}}, " +
				"publish the draft, then store the secret with strazactl apps secret set github, which asks for the value at a hidden prompt.",
			testApp(testHead + testServer + "straza:\n  runtime:\n    kind: oci\n    oci:\n      image: ghcr.io/acme/crm:1\n      env:\n        - name: CRM_API_KEY\n          value: hunter2\n")},
		{"app value in a registry record keeps the placeholder",
			"In the server github, server.packages[0].environmentVariables[CRM_API_KEY].value holds a value, and its name marks it as a secret. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"Remove the value, or write a {placeholder} in its place." + appTail("github"),
			testRegistry("  packages:\n    - registryType: npm\n      identifier: acme-crm\n      environmentVariables:\n        - name: CRM_API_KEY\n          value: hunter2\n")},
		{"app warning",
			"In the server github, straza.runtime.command.env[CONFIG_BLOB].value holds a long random-looking string, which may be a secret. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"If it is a secret, remove it. If the server needs the secret, set credential.kind: static and credential.inject with as: env, name: CONFIG_BLOB and template: {{secret}}, " +
				"publish the draft, then store the secret with strazactl apps secret set github, which asks for the value at a hidden prompt. If it is not a secret, nothing needs to change.",
			testCommandEnv("        - name: CONFIG_BLOB\n          value: " + fakeRandom + "\n")},
		{"app argument after a flag that names a secret",
			"In the server github, straza.runtime.command.args[1] holds a value after a word that names a secret. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"Remove the flag and its value." + appTail("github"),
			testCommandArgs(`[--password, hunter2]`)},
		{"app argument that holds a flag and its value",
			"In the server github, straza.runtime.command.args[0] holds a value after a word that names a secret. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"Remove the flag and its value." + appTail("github"),
			testCommandArgs(`["--password=hunter2"]`)},
		{"app with a withheld name",
			"In a server whose name looks like a secret, metadata.name holds what looks like a GitHub token. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"Remove it." + appTail("NAME"),
			withheld},
		{"role refusal",
			"In the role dev, spec.description holds an address that carries a password or a credential before its host. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it.",
			"Remove the user name and password from the address. A role never needs a secret.",
			testRole("Wiki at https://dev:" + fakePass + "@wiki.example.com/dev")},
		{"policy refusal",
			"In the policy set lockdown, line 1 holds what looks like an AWS access key id. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it. Once published, its text, comments included, is served to anyone who asks, without a sign-in.",
			"Remove it. A policy set never needs a secret, in a rule or in a comment.",
			testPolicy("# " + fakeAWS + "\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanSecrets(tc.it)
			if len(got) != 1 || got[0].Sentence != tc.sentence || got[0].Fix != tc.fix {
				t.Fatalf("got:\n%s\nwant sentence %q\nwant fix %q", testDescribe(got), tc.sentence, tc.fix)
			}
		})
	}
}

// TestScanSecretsNameWithoutDocument pins the refusal of an item that
// carries no document and whose name holds a secret, such as a removal named
// like a token: the draft would keep the name, so it is refused as a put of
// that name is, in words of its own and with the name withheld.
func TestScanSecretsNameWithoutDocument(t *testing.T) {
	fix := "Leave that item out of the draft. If a live object carries the name, treat the secret in it as exposed and rotate it."
	tests := []struct {
		name     string
		it       Item
		sentence string
	}{
		{"a role removal named like a GitHub token", Item{Kind: KindRole, Name: fakeGitHub, Op: OpRemove},
			"The name of a role in this draft holds what looks like a GitHub token. " + scanWhy},
		{"a policy set removal named like a Straza token", Item{Kind: KindPolicySet, Name: fakeStraza, Op: OpRemove},
			"The name of a policy set in this draft holds what looks like a Straza token. " + scanWhy},
		{"a server removal named like an AWS key", Item{Kind: KindApp, Name: fakeAWS, Op: OpRemove},
			"The name of a server in this draft holds what looks like an AWS access key id. " + scanWhy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanSecrets(tc.it)
			if len(got) != 1 || got[0].Code != codeSecretShape || got[0].Class != ClassRefused || got[0].Sentence != tc.sentence || got[0].Fix != fix {
				t.Fatalf("got:\n%s\nwant sentence %q\nwant fix %q", testDescribe(got), tc.sentence, fix)
			}
			testFindingRules(t, string(tc.it.Kind)+"/(name withheld)", got)
		})
	}
	if got := ScanSecrets(Item{Kind: KindRole, Name: "helpers", Op: OpRemove}); len(got) != 0 {
		t.Errorf("a removal with a plain name drew:\n%s", testDescribe(got))
	}
}

// TestScanSecretsCap pins the cap: past 20 places one last finding counts
// the rest, and it is a refusal when any place it counts is one.
func TestScanSecretsCap(t *testing.T) {
	env := func(values ...string) Item {
		var b strings.Builder
		for i, v := range values {
			fmt.Fprintf(&b, "        - name: VAR_%d\n          value: %s\n", i, v)
		}
		return testCommandEnv(b.String())
	}
	var tokens, randoms []string
	for i := 0; i < 25; i++ {
		tokens = append(tokens, "gh"+"p_"+testHex(fmt.Sprint("token", i), 36))
		randoms = append(randoms, testHex(fmt.Sprint("random", i), 32))
		if !secretLooksGenerated(randoms[i]) {
			t.Fatalf("the fixture value %d must read as generated", i)
		}
	}
	tests := []struct {
		name  string
		it    Item
		count int
		last  string
		class Class
	}{
		{"exactly 20 places", env(tokens[:20]...), 20, codeSecretShape, ClassRefused},
		{"25 refusals", env(tokens...), 21, codeSecretMore, ClassRefused},
		{"a refusal past 20 warnings", env(append(append([]string{}, randoms[:20]...), tokens[0])...), 21, codeSecretMore, ClassRefused},
		{"warnings only", env(randoms[:22]...), 21, codeSecretMore, ClassWarning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanSecrets(tc.it)
			if len(got) != tc.count || got[len(got)-1].Code != tc.last || got[len(got)-1].Class != tc.class {
				t.Fatalf("got %d findings, want %d ending in %s %s:\n%s", len(got), tc.count, tc.last, tc.class, testDescribe(got))
			}
			testFindingRules(t, tc.it.Object(), got)
		})
	}
	more := ScanSecrets(env(tokens...))[20]
	if want := "In the server github, 5 more places hold what looks like a secret. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it."; more.Sentence != want {
		t.Errorf("sentence %q, want %q", more.Sentence, want)
	}
	if want := "Fix the places above, then save the draft again, and the check names the rest."; more.Fix != want {
		t.Errorf("fix %q, want %q", more.Fix, want)
	}
	if one := ScanSecrets(env(tokens[:21]...))[20]; !strings.Contains(one.Sentence, " 1 more place holds ") {
		t.Errorf("one cut place reads %q", one.Sentence)
	}
}

func TestScanNote(t *testing.T) {
	tests := []struct {
		name, note string
		want       []wantSecret
	}{
		{"a token on the second line", "Adds the github server.\nUse " + fakeGitHub + " to test it.",
			[]wantSecret{{codeSecretShape, ClassRefused, "line 2"}}},
		{"a password in an address", "Mirrors https://ci:" + fakePass + "@git.example.com/repo",
			[]wantSecret{{codeSecretUserinfo, ClassRefused, "line 1"}}},
		{"a private key header", "Key below\n" + fakeKey, []wantSecret{{codeSecretShape, ClassRefused, "line 2"}}},
		{"an ssh address with a user", "Clone ssh://git@github.com/acme/mcp.git first.", nil},
		{"a plain note", "Gives the analysts read access to the github issues tools.", nil},
		{"an empty note", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanNote(tc.note)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d findings, want %d:\n%s", len(got), len(tc.want), testDescribe(got))
			}
			for i, w := range tc.want {
				if f := got[i]; f.Code != w.code || f.Class != w.class || !strings.Contains(f.Sentence, ", "+w.where+" ") {
					t.Errorf("finding %d is %s %s %q, want %s %s at %q", i, f.Code, f.Class, f.Sentence, w.code, w.class, w.where)
				}
			}
			testFindingRules(t, "Note", got)
		})
	}
	got := ScanNote("Use " + fakeGitHub)
	sentence := "In the draft's note, line 1 holds what looks like a GitHub token. A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it."
	fix := "Remove it. If a server needs the secret, a person stores it after publish with strazactl apps secret set and the server's name, which asks for the value at a hidden prompt."
	if len(got) != 1 || got[0].Sentence != sentence || got[0].Fix != fix {
		t.Errorf("got:\n%s\nwant sentence %q\nwant fix %q", testDescribe(got), sentence, fix)
	}
}

// TestScanSecretsSpecCorpus is the negative control over the documents the
// specs ship: registry imports with their Bearer {{secret}} templates, the
// valid app manifests, the valid policy sets and the role fixtures.
func TestScanSecretsSpecCorpus(t *testing.T) {
	for _, c := range []struct {
		glob string
		kind Kind
	}{
		{"../../spec/conformance/registry/*.app.yaml", KindApp},
		{"../../spec/app-manifest/examples/valid-*.yaml", KindApp},
		{"../../spec/policyset/examples/valid-*.yaml", KindPolicySet},
		{"../../spec/objects/fixtures/role-*.yaml", KindRole},
	} {
		files, err := filepath.Glob(c.glob)
		if err != nil || len(files) == 0 {
			t.Fatalf("glob %s found no file (%v), so the control checked nothing", c.glob, err)
		}
		for _, name := range files {
			doc, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if got := ScanSecrets(Item{Kind: c.kind, Name: "corpus", Op: OpPut, Doc: string(doc)}); len(got) != 0 {
				t.Errorf("%s: want no finding, got:\n%s", name, testDescribe(got))
			}
		}
	}
}

func TestSecretNamed(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"GITHUB_TOKEN", true},
		{"GITHUB_PERSONAL_ACCESS_TOKEN", true},
		{"OPENAI_API_KEY", true},
		{"apiKey", true},
		{"APIKEY", true},
		{"api-key", true},
		{"client_secret", true},
		{"clientSecret", true},
		{"DB_PASSWORD", true},
		{"X-Amz-Signature", true},
		{"sig", true},
		{"access_token", true},
		{"Authorization", true},
		{"Proxy-Authorization", true},
		{"PGPASSWORD", true},
		{"MYSQL_PWD", true},
		{"REDIS_PASS", true},
		{"passwd", true},
		{"GPG_PASSPHRASE", true},
		{"AWS_CREDENTIALS", true},
		{"credential", true},
		{"X-Auth", true},
		{"CLIENTSECRET", true},
		{"OPENAIAPIKEY", true},
		{"OAUTH", false},
		{"BYPASS", false},
		{"MONKEY", false},
		{"AUTH_URL", false},
		{"PGPASSFILE", false},
		{"TOKEN_URL", false},
		{"KEYCLOAK_URL", false},
		{"keyword", false},
		{"AWS_ACCESS_KEY_ID", false},
		{"MAX_TOKENS", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := secretNamed(tc.name); got != tc.want {
			t.Errorf("secretNamed(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSecretBenign(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"true", true}, {"false", true}, {"yes", true}, {"no", true}, {"on", true}, {"off", true}, {"On", true}, {"none", true},
		{"42", true}, {"-1", true},
		{"/etc/tls/key.pem", true}, {"./key.pem", true}, {"../keys/key.pem", true}, {"~/.ssh/id_ed25519", true},
		{`C:\certs\client.key`, true}, {"C:/certs/client.key", true}, {"/key.json", true},
		{"abc123", false}, {"hunter2", false}, {"two words", false}, {"C:key", false}, {"s3cr3t", false},
		{"name", false}, {"sort_key", false}, {"read-only", false}, {"correcthorsebatterystaple", false},
		{"/k9Zx7Qp2Lm4Wn8R", false}, {"/data", false}, {"~/", false},
	}
	for _, tc := range tests {
		if got := secretBenign(tc.value); got != tc.want {
			t.Errorf("benign(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestSecretLooksGenerated(t *testing.T) {
	tests := []struct {
		name, value string
		want        bool
	}{
		{"base64 of a digest", fakeRandom, true},
		{"hex of a digest", fakeHex, true},
		{"a random flag value", "--seed=" + fakeRandom, true},
		{"a UUID", "9b2f6c1e-3d4a-4e8b-9f7c-2a1d5e6b8c9d", false},
		{"a UUID inside a name", "urn_uuid_550e8400-e29b-41d4-a716-446655440000", false},
		{"an identifier with a year", "mcp-server-kubernetes-readonly-profile-2025", false},
		{"a camelCase name with a year", "MyCompanyInternalToolsServer2025Production", false},
		{"a package reference", "@modelcontextprotocol/server-filesystem@1.2.3", false},
		{"a path", "/var/lib/straza/data/2024/backups/mcp-server-01", false},
		{"one letter repeated", strings.Repeat("a", 40) + "1", false},
		{"digits only", strings.Repeat("0123456789", 4), false},
		{"shorter than 32", fakeRandom[:31], false},
	}
	for _, tc := range tests {
		if got := secretLooksGenerated(tc.value); got != tc.want {
			t.Errorf("%s: secretLooksGenerated = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestNameWithheld pins the rule the scan withholds a name by: an item of
// any kind whose name holds a credential shape is withheld, whether or not
// it carries a document, and a plain name is not, so a door can keep such
// an item away from any reading that would quote its name.
func TestNameWithheld(t *testing.T) {
	tests := []struct {
		name     string
		it       Item
		withheld bool
	}{
		{"a server named like a GitHub token", Item{Kind: KindApp, Name: fakeGitHub, Op: OpPut, Doc: "kind: App"}, true},
		{"a server removal named like a Slack token", Item{Kind: KindApp, Name: fakeSlack, Op: OpRemove}, true},
		{"a role named like an AWS key", Item{Kind: KindRole, Name: fakeAWS, Op: OpPut}, true},
		{"a policy set named like a Straza token", Item{Kind: KindPolicySet, Name: fakeStraza, Op: OpRemove}, true},
		{"a server with a plain name", Item{Kind: KindApp, Name: "github", Op: OpPut, Doc: "kind: App"}, false},
		{"a role with a plain name", Item{Kind: KindRole, Name: "helpers", Op: OpRemove}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NameWithheld(tc.it); got != tc.withheld {
				t.Errorf("NameWithheld = %v, want %v", got, tc.withheld)
			}
		})
	}
}

// TestScanSecretsReadsTheMarkAsNoValue pins that the mark a route answers
// in place of a value is no value to the scan: under a name that marks a
// secret, after a flag that names one and in an entry marked isSecret it
// draws no secret.value, because intake refuses the mark as bundle.masked
// and the check waives it for the stored value, while the mark inside a
// longer value, with a space around it or as a block scalar is still a
// value, since intake reads none of those as a mask.
func TestScanSecretsReadsTheMarkAsNoValue(t *testing.T) {
	tests := []struct {
		name  string
		it    Item
		codes []string
	}{
		{"the mark under a name that marks a secret", testCommandEnv("        - name: GITHUB_TOKEN\n          value: '[REDACTED]'\n"), nil},
		{"the mark after a flag that names a secret", testCommandArgs("[--password, '[REDACTED]']"), nil},
		{"the mark in an entry marked isSecret",
			testRegistry("  packages:\n    - registryType: npm\n      identifier: acme-crm\n      environmentVariables:\n        - name: CRM_HOST\n          isSecret: true\n          value: '[REDACTED]'\n"), nil},
		{"the mark inside a longer value", testCommandEnv("        - name: GITHUB_TOKEN\n          value: 'x[REDACTED]'\n"), []string{codeSecretValue}},
		{"the mark with a space before it", testCommandEnv("        - name: GITHUB_TOKEN\n          value: ' [REDACTED]'\n"), []string{codeSecretValue}},
		{"the mark with a space after it", testCommandArgs("[--password, '[REDACTED] ']"), []string{codeSecretValue}},
		{"the mark as a block scalar", testCommandEnv("        - name: GITHUB_TOKEN\n          value: |\n            [REDACTED]\n"), []string{codeSecretValue}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var codes []string
			for _, f := range ScanSecrets(tc.it) {
				codes = append(codes, f.Code)
			}
			if !slices.Equal(codes, tc.codes) {
				t.Errorf("codes = %v, want %v:\n%s", codes, tc.codes, testDescribe(ScanSecrets(tc.it)))
			}
		})
	}
}
