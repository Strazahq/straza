package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSkillsTree(t *testing.T) {
	root := t.TempDir()
	must := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("plugins/skills/straza/SKILL.md", "---\nname: straza\ndescription: Operate Straza.\n---\n\n# Straza\n")
	must("plugins/skills/straza/references/policy.md", "# Policy\n")
	must("plugins/skills/straza/scripts/replay.sh", "#!/bin/sh\n")
	must("plugins/.claude-plugin/plugin.json", `{"name":"straza","description":"One skill for operating Straza.","version":"0.3.0"}`)

	out := filepath.Join(root, "well-known")
	if err := writeSkillsTree(root, out); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(out, "skills", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx skillsIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Skills) != 1 || idx.Skills[0].Name != "straza" || idx.Skills[0].Description != "Operate Straza." {
		t.Fatalf("unexpected index: %+v", idx)
	}
	wantFiles := []string{"SKILL.md", "references/policy.md", "scripts/replay.sh"}
	if len(idx.Skills[0].Files) != len(wantFiles) {
		t.Fatalf("files: got %v want %v", idx.Skills[0].Files, wantFiles)
	}
	for i, f := range wantFiles {
		if idx.Skills[0].Files[i] != f {
			t.Fatalf("files: got %v want %v", idx.Skills[0].Files, wantFiles)
		}
		if _, err := os.Stat(filepath.Join(out, "skills", "straza", f)); err != nil {
			t.Fatalf("copied file %s missing: %v", f, err)
		}
	}

	raw, err = os.ReadFile(filepath.Join(out, "claude-code", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mp marketplace
	if err := json.Unmarshal(raw, &mp); err != nil {
		t.Fatal(err)
	}
	if mp.Name != "straza" || len(mp.Plugins) != 1 || mp.Plugins[0].Source.Source != "git-subdir" ||
		mp.Plugins[0].Source.URL != publicRepoGitURL || mp.Plugins[0].Source.Path != "plugins" {
		t.Fatalf("unexpected marketplace: %+v", mp)
	}

	// A second run over the same sources is byte-identical, which docs-drift
	// relies on, and a removed source file disappears from the output.
	before, _ := os.ReadFile(filepath.Join(out, "skills", "index.json"))
	if err := os.Remove(filepath.Join(root, "plugins/skills/straza/scripts/replay.sh")); err != nil {
		t.Fatal(err)
	}
	if err := writeSkillsTree(root, out); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(out, "skills", "index.json"))
	if string(before) == string(after) {
		t.Fatal("index did not change after a source file was removed")
	}
	if _, err := os.Stat(filepath.Join(out, "skills", "straza", "scripts", "replay.sh")); !os.IsNotExist(err) {
		t.Fatal("stale copy survived the rebuild")
	}
}

func TestFrontMatterNameDescription(t *testing.T) {
	cases := []struct {
		doc, name, desc string
	}{
		{"---\nname: a\ndescription: b c\n---\nbody", "a", "b c"},
		{"no front matter", "", ""},
		{"---\nname: only\n---\n", "only", ""},
	}
	for _, tc := range cases {
		n, d := frontMatterNameDescription(tc.doc)
		if n != tc.name || d != tc.desc {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", tc.doc, n, d, tc.name, tc.desc)
		}
	}
}
