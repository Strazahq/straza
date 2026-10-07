package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const simulatePage = `---
title: strazactl policy simulate
---
## Options

` + "```" + `
      --command string       shell.exec: the raw command line
  -f, --file string          draft PolicySet YAML to overlay on the active policies
      --user string          simulate this user
` + "```" + `

## Options inherited from parent commands

` + "```" + `
      --server string   strazad base URL
` + "```" + `
`

const goodSkill = `---
name: straza
description: Operate Straza.
compatibility: Straza 1.x, verified against strazad v1.0.0.
---

# Straza

Run ` + "`strazactl policy simulate --user alice --command date -f draft.yaml`" + ` first.
The install step is ` + "`straza install claude-code`" + `.

` + "```sh" + `
STRAZA_SECRET_VALUE=x strazactl policy simulate --user "$u" --file d.yaml | grep -E '^active'
verdict=$(strazactl policy simulate --server http://h --user alice --command "rm -rf x")
` + "```" + `

` + "```text" + `
--> run ` + "`straza install claude-code`" + ` (or codex)
` + "```" + `

Read https://docs.straza.ai/guides/write-policy/first-deny/ and https://docs.straza.ai/reference/ and https://docs.straza.ai/llms.txt.
`

const goodPolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: example
spec:
  rules:
    - id: no-rm-rf
      events: [tool.pre]
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *"]
      effect: deny
      reason: "Straza: refused"
`

const goodApp = `apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: my-tools
server:
  name: my-tools
  version: "0"
straza:
  runtime:
    kind: remote
    remote:
      url: http://my-tools.internal:3001/mcp
  exposure:
    tools: ["*"]
`

const goodReplay = `#!/bin/sh
verdict=$(strazactl policy simulate --user "$u" -f "$d" | grep -E '^(live and file agree|the live policy says)')
`

// fixture writes a consistent tree and returns its root.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"website/content/reference/cli/strazactl/strazactl_policy.md":          "---\ntitle: strazactl policy\n---\n",
		"website/content/reference/cli/strazactl/strazactl_policy_simulate.md": simulatePage,
		"website/content/reference/cli/straza/straza_install.md":               "---\ntitle: straza install\n---\n## Options\n```\n      --managed   root-owned layout\n```\n",
		"website/content/guides/write-policy/first-deny.md":                    "---\ntitle: x\n---\n",
		"website/content/reference/_index.md":                                  "---\ntitle: r\n---\n",
		"plugins/skills/straza/SKILL.md":                                       goodSkill,
		"plugins/skills/straza/scripts/replay.sh":                              goodReplay,
		"plugins/skills/straza/references/examples/deny.yaml":                  goodPolicy,
		"plugins/skills/straza/references/examples/app-remote.yaml":            goodApp,
		"plugins/.claude-plugin/plugin.json":                                   `{"name":"straza","version":"0.2.0"}`,
		"plugins/gemini-extension.json":                                        `{"name":"straza","version":"0.2.0"}`,
		"plugins/.codex-plugin/plugin.json":                                    `{"name":"straza","version":"0.2.0"}`,
	}
	for rel, body := range files {
		write(t, root, rel, body)
	}
	return root
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// walkPlugins stands in for git ls-files in a tree that is not a repository.
func walkPlugins(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(filepath.Join(root, pluginsDir), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}

func TestCleanTreePasses(t *testing.T) {
	root := fixture(t)
	got, err := run(root, walkPlugins)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %v", got)
	}
}

func TestViolations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, root string)
		want   string
	}{
		{
			name: "unknown flag on a known command",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/SKILL.md", strings.Replace(goodSkill, "--command date", "--commandline date", 1))
			},
			want: "flag --commandline is not on the strazactl policy simulate reference page",
		},
		{
			name: "unknown short flag",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/SKILL.md", strings.Replace(goodSkill, "-f draft.yaml", "-x draft.yaml", 1))
			},
			want: "flag -x is not on the strazactl policy simulate reference page",
		},
		{
			name: "unknown verb",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/SKILL.md", strings.Replace(goodSkill, "straza install claude-code`.", "straza wire claude-code`.", 1))
			},
			want: `unknown command "straza wire"`,
		},
		{
			name: "a verb its group lacks, with no flag after it",
			mutate: func(t *testing.T, root string) {
				writeGroup(t, root, "strazactl drafts", "strazactl drafts check -f <path>... [flags]")
				write(t, root, "plugins/skills/straza/SKILL.md", goodSkill+"\nThen `strazactl drafts contact 41 github`.\n")
			},
			want: `unknown command "strazactl drafts contact"`,
		},
		{
			name: "a verb its group no longer has",
			mutate: func(t *testing.T, root string) {
				writeGroup(t, root, "strazactl bindings", "strazactl bindings list [flags]")
				write(t, root, "plugins/skills/straza/SKILL.md", goodSkill+"\nThen `strazactl bindings apply`.\n")
			},
			want: `unknown command "strazactl bindings apply"`,
		},
		{
			name: "command inside a script",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/scripts/x.sh", "#!/bin/sh\n# strazactl policy nothing --x\nout=$(strazactl policy simulate --user a --nope 1 \\\n  | grep x)\n")
			},
			want: "x.sh:3: flag --nope is not on the strazactl policy simulate reference page",
		},
		{
			name: "script parses simulate text the CLI no longer prints",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/scripts/replay.sh", strings.Replace(goodReplay, "live and file agree", "live and draft agree", 1))
			},
			want: `replay.sh: runs strazactl policy simulate but does not quote its output line "live and file agree"`,
		},
		{
			name: "script drops the differ line",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/scripts/replay.sh", strings.Replace(goodReplay, "the live policy says", "active policy says", 1))
			},
			want: `does not quote its output line "the live policy says"`,
		},
		{
			name: "dead docs link",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/SKILL.md", strings.Replace(goodSkill, "write-policy/first-deny/", "write-policy/first-denial/", 1))
			},
			want: "link https://docs.straza.ai/guides/write-policy/first-denial/ has no page",
		},
		{
			name: "example the validator rejects",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/references/examples/deny.yaml", strings.Replace(goodPolicy, "effect: deny", "effect: refuse", 1))
			},
			want: "deny.yaml: the validator rejects this example",
		},
		{
			name: "app example the validator rejects",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/references/examples/app-remote.yaml", strings.Replace(goodApp, "kind: remote", "kind: cloud", 1))
			},
			want: "app-remote.yaml: the validator rejects this example",
		},
		{
			name: "missing compatibility line",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/SKILL.md", strings.Replace(goodSkill, "compatibility: Straza 1.x, verified against strazad v1.0.0.\n", "", 1))
			},
			want: "no compatibility line",
		},
		{
			name: "compatibility without a version",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/skills/straza/SKILL.md", strings.Replace(goodSkill, "strazad v1.0.0.", "strazad.", 1))
			},
			want: "names no product version",
		},
		{
			name: "manifest versions disagree",
			mutate: func(t *testing.T, root string) {
				write(t, root, "plugins/gemini-extension.json", `{"name":"straza","version":"0.3.0"}`)
			},
			want: "version 0.3.0 disagrees with 0.2.0",
		},
		{
			name: "version is not semantic",
			mutate: func(t *testing.T, root string) {
				for _, m := range manifestFiles {
					write(t, root, filepath.Join(pluginsDir, m), `{"name":"straza","version":"v0.2"}`)
				}
			},
			want: `version "v0.2" is not MAJOR.MINOR.PATCH`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t)
			tc.mutate(t, root)
			got, err := run(root, walkPlugins)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for _, f := range got {
				lines = append(lines, f.String())
			}
			all := strings.Join(lines, "\n")
			if !strings.Contains(all, tc.want) {
				t.Fatalf("want a finding containing %q, got:\n%s", tc.want, all)
			}
		})
	}
}

// writeGroup writes the page of a group that runs no command of its own and
// the page of one runnable child, named by its use line as Cobra prints it.
func writeGroup(t *testing.T, root, group, childUse string) {
	t.Helper()
	dir := "website/content/reference/cli/" + strings.Fields(group)[0] + "/"
	write(t, root, dir+strings.ReplaceAll(group, " ", "_")+".md", "---\ntitle: "+group+"\n---\n## Options\n")
	child := strings.Join(strings.Fields(childUse)[:len(strings.Fields(group))+1], " ")
	write(t, root, dir+strings.ReplaceAll(child, " ", "_")+".md",
		"---\ntitle: "+child+"\n---\n```\n"+childUse+"\n```\n\n## Options\n\n```\n  -f, --file stringArray   documents\n```\n")
}

// TestVerbRuleKeepsOperands pins what the verb rule lets through: a verb its
// group has, a group named alone, and a word after a group that runs a
// command of its own, which is that command's operand.
func TestVerbRuleKeepsOperands(t *testing.T) {
	root := fixture(t)
	writeGroup(t, root, "strazactl drafts", "strazactl drafts check -f <path>... [flags]")
	writeGroup(t, root, "strazactl roles", "strazactl roles implications <name> [flags]")
	write(t, root, "website/content/reference/cli/strazactl/strazactl_roles_implications_add.md",
		"---\ntitle: strazactl roles implications add\n---\n```\nstrazactl roles implications add <name> <implied> [flags]\n```\n")
	for _, line := range []string{
		"strazactl drafts check -f onboard/",
		"strazactl drafts",
		"strazactl roles implications developers",
		"strazactl roles implications add developers gh-devs",
	} {
		write(t, root, "plugins/skills/straza/SKILL.md", goodSkill+"\nThen `"+line+"`.\n")
		got, err := run(root, walkPlugins)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("%s: want no finding, got %v", line, got)
		}
	}
}

func TestSegmentsHonorQuotesAndSeparators(t *testing.T) {
	segs := segments(`printf '%s' '{"a":"b|c;d"}' | straza hook --harness claude-code; echo "exit $?"`)
	if len(segs) != 3 {
		t.Fatalf("want 3 segments, got %d: %v", len(segs), segs)
	}
	if segs[1][0] != "straza" || segs[1][1] != "hook" || segs[1][3] != "claude-code" {
		t.Fatalf("unexpected second segment %v", segs[1])
	}
	if segs[0][2] != `{"a":"b|c;d"}` {
		t.Fatalf("quoted JSON was split: %v", segs[0])
	}
}

func TestStaticAssetLinkPasses(t *testing.T) {
	root := fixture(t)
	write(t, root, "website/static/.well-known/skills/index.json", "{}")
	write(t, root, "plugins/skills/straza/SKILL.md", strings.Replace(goodSkill, "https://docs.straza.ai/llms.txt.", "https://docs.straza.ai/.well-known/skills/index.json.", 1))
	got, err := run(root, walkPlugins)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %v", got)
	}
}
