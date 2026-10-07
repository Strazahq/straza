package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const mitBody = "Permission is hereby granted, free of charge, to any person obtaining a copy."

// fixtureLock is a lock file with a production package, a scoped one, a dev
// and a devOptional entry, a dev package that the CSS imports, a dev package
// whose code is in the bundle, the bundler, and the package the exception
// table covers.
const fixtureLock = `{
  "name": "fixture",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {"name": "fixture"},
    "node_modules/alpha": {"version": "1.0.0", "license": "MIT"},
    "node_modules/@scope/beta": {"version": "2.0.0", "license": "ISC"},
    "node_modules/devonly": {"version": "1.0.0", "dev": true, "license": "MIT"},
    "node_modules/devopt": {"version": "1.0.0", "devOptional": true, "license": "MIT"},
    "node_modules/cssonly": {"version": "3.0.0", "dev": true, "license": "MIT"},
    "node_modules/devbundled": {"version": "0.5.0", "dev": true, "license": "MIT"},
    "node_modules/vite": {"version": "7.3.6", "dev": true, "license": "MIT"},
    "node_modules/react-remove-scroll-bar": {"version": "2.3.8", "license": "MIT"}
  }
}
`

// fixtureFiles is a small repository: the console source with its lock file,
// installed packages and the module lists of its two builds, and a Go module
// whose three binaries link two local modules, one of them with a NOTICE
// file, one with license files in the folders of its linked subpackages, and
// one with a license beside a file it embeds.
var fixtureFiles = map[string]string{
	"web/ui/package-lock.json":                                  fixtureLock,
	"web/ui/node_modules/alpha/LICENSE":                         "MIT License\r\n\r\nCopyright (c) Alpha Author\r\n\r\n" + mitBody + "\r\n\r\n\r\n",
	"web/ui/node_modules/alpha/license.js":                      "module.exports = 'not a license text'\n",
	"web/ui/node_modules/alpha/index.js":                        "module.exports = 1\n",
	"web/ui/node_modules/@scope/beta/LICENCE.md":                "ISC License\n\nCopyright (c) Beta Author\n\nPermission to use, copy, modify, and/or distribute this software.\n",
	"web/ui/node_modules/@scope/beta/NOTICE":                    "Beta includes software by Gamma.\n",
	"web/ui/node_modules/devonly/LICENSE":                       "MIT License\n\nCopyright (c) Dev Author\n",
	"web/ui/node_modules/cssonly/LICENSE":                       "MIT License\n\nCopyright (c) Css Author\n\n" + mitBody + "\n",
	"web/ui/node_modules/react-remove-scroll-bar/package.json":  `{"name": "react-remove-scroll-bar", "license": "MIT"}` + "\n",
	"web/ui/node_modules/devbundled/LICENSE":                    "MIT License\n\nCopyright (c) Devbundled Author\n\n" + mitBody + "\n",
	"web/ui/node_modules/devbundled/dist/esm/x.js":              "export const x = 1\n",
	"web/ui/node_modules/devbundled/dist/esm/copyright.mjs":     "export const copyrightIcon = 1\n",
	"web/ui/node_modules/devbundled/dist/esm/copyright.mjs.map": "{}\n",
	"web/ui/node_modules/devbundled/dist/NOTICE":                "Devbundled vendors Tiny, Copyright Tiny Corp.\n",
	"web/ui/node_modules/devbundled/other/LICENSE":              "Not on the way from a bundled module.\n",
	"web/ui/node_modules/vite/LICENSE.md":                       "# Vite core license\n\nMIT License\n\nCopyright (c) 2019-present, VoidZero Inc. and Vite contributors\n\n" + mitBody + "\n",
	"web/ui/node_modules/.straza/bundle-pages.json":             `["index.html", "src/main.tsx", "src/vendor/qrcodegen.js", "node_modules/alpha/index.js", "node_modules/devbundled/dist/esm/x.js", "vite/preload-helper.js", "commonjsHelpers.js"]` + "\n",
	"web/ui/node_modules/.straza/bundle-sw.json":                `["src/self-service/sw.ts"]` + "\n",
	"web/ui/src/index.css":                                      "@import \"cssonly\";\n@import \"./local.css\";\n",
	"web/ui/src/local.css":                                      "body { margin: 0; }\n",
	"web/ui/src/vendor/qrcodegen.js":                            "/*!\n * QR Code generator library (TypeScript)\n *\n * Copyright (c) Project Nayuki. (MIT License)\n */\n\n/*\n * version  : v1.8.0\n */\nvar qr = 1;\n",
	"web/ui/src/components/ui/LICENSE":                          "MIT License\n\nCopyright (c) 2023 shadcn\n\n" + mitBody + "\n",
	"go.mod":                                                    "module example.com/fixture\n\ngo 1.22\n\nrequire (\n\texample.com/dep v1.0.0\n\texample.com/noticed v1.2.0\n)\n\nreplace (\n\texample.com/dep => ./deps/dep\n\texample.com/noticed => ./deps/noticed\n)\n",
	"cmd/strazad/main.go":                                       "package main\n\nimport _ \"example.com/dep\"\n\nfunc main() {}\n",
	"cmd/straza/main.go":                                        "package main\n\nimport _ \"example.com/noticed\"\n\nfunc main() {}\n",
	"cmd/strazactl/main.go":                                     "package main\n\nimport (\n\t_ \"example.com/dep\"\n\t_ \"example.com/noticed\"\n)\n\nfunc main() {}\n",
	"deps/dep/go.mod":                                           "module example.com/dep\n\ngo 1.22\n",
	"deps/dep/dep.go":                                           "package dep\n\nimport (\n\t_ \"example.com/dep/inner\"\n\t_ \"example.com/dep/third_party/sdk\"\n)\n",
	"deps/dep/inner/inner.go":                                   "package inner\n",
	"deps/dep/inner/LICENSE":                                    "MIT License\n\nCopyright (c) 2016 Inner Author\n",
	"deps/dep/inner/NOTICE.txt":                                 "Inner SDK\nCopyright Inner Corp. This product includes software developed by Inner Corp.\n",
	"deps/dep/third_party/NOTICE":                               "Third Party SDK\nCopyright Third Party Inc.\n",
	"deps/dep/third_party/sdk/sdk.go":                           "package sdk\n",
	"deps/dep/unlinked/unlinked.go":                             "package unlinked\n",
	"deps/dep/unlinked/LICENSE":                                 "Copyright Unlinked Author, not in any binary.\n",
	"deps/dep/license.go":                                       "package dep\n\n// Not a license text.\n",
	"deps/dep/LICENSE":                                          "Copyright (c) 2020 The Dep Authors. All rights reserved.\n\nRedistribution and use in source and binary forms are permitted.\n",
	"deps/noticed/go.mod":                                       "module example.com/noticed\n\ngo 1.22\n",
	"deps/noticed/noticed.go":                                   "package noticed\n\nimport _ \"embed\"\n\n//go:embed assets/data.txt\nvar data string\n",
	"deps/noticed/assets/data.txt":                              "embedded data\n",
	"deps/noticed/assets/COPYING":                               "The data is Copyright Assets Author.\n",
	"deps/noticed/LICENSE":                                      "Apache License\nVersion 2.0, January 2004\n",
	"deps/noticed/NOTICE":                                       "Noticed\nCopyright 2020 The Noticed Authors\n",
}

// writeFiles writes files under root, one entry per relative path.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fixture writes the fixture repository into a fresh folder. The go command
// runs offline in it, because every module it needs is a local folder.
func fixture(t *testing.T) string {
	t.Helper()
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	writeFiles(t, root, fixtureFiles)
	return root
}

// withConsole writes the fixture's console notices where the release mode
// reads them.
func withConsole(t *testing.T, root string) []byte {
	t.Helper()
	body, err := consoleNotices(root)
	if err != nil {
		t.Fatalf("console notices: %v", err)
	}
	writeFiles(t, root, map[string]string{consoleOut: string(body)})
	return body
}

var headLine = regexp.MustCompile(`(?m)^` + rule + `\n(.+)\nLicense: `)

// heads returns the "kind name version" line of every section in order.
func heads(body []byte) []string {
	var out []string
	for _, m := range headLine.FindAllSubmatch(body, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// sectionText returns the section whose head line is head.
func sectionText(t *testing.T, body []byte, head string) string {
	t.Helper()
	for _, s := range parseSections(body) {
		if s.kind+" "+s.name+" "+s.version == head {
			return s.body
		}
	}
	t.Fatalf("no section %q in:\n%s", head, body)
	return ""
}

func TestConsoleNotices(t *testing.T) {
	root := fixture(t)
	body := withConsole(t, root)

	want := []string{
		"vendored QR Code generator library 1.8.0",
		"vendored shadcn/ui components generated",
		"npm @scope/beta 2.0.0",
		"npm alpha 1.0.0",
		"npm cssonly 3.0.0",
		"npm devbundled 0.5.0",
		"npm react-remove-scroll-bar 2.3.8",
		"npm vite 7.3.6",
	}
	if got := heads(body); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sections, each once and in order:\n got %q\nwant %q", got, want)
	}
	if !strings.HasPrefix(string(body), "THIRD-PARTY NOTICES FOR the Straza console\n") || !strings.Contains(string(body), "\nComponents: 8\n") {
		t.Errorf("header wrong:\n%.400s", body)
	}
	cases := []struct {
		head     string
		contains []string
		lacks    []string
	}{
		{"npm alpha 1.0.0", []string{"License: MIT\n", "Origin: https://www.npmjs.com/package/alpha/v/1.0.0\n", "--- LICENSE ---\nMIT License\n", mitBody + "\n"}, []string{"license.js", "\r", mitBody + "\n\n\n"}},
		{"npm @scope/beta 2.0.0", []string{"License: ISC\n", "--- LICENCE.md ---\n", "--- NOTICE ---\nBeta includes software by Gamma.\n"}, nil},
		{"npm cssonly 3.0.0", []string{"--- LICENSE ---\n", "Css Author"}, nil},
		{"npm react-remove-scroll-bar 2.3.8", []string{"--- exception table ---\n", "Copyright (c) Anton Korzunov", "Permission is hereby granted", "ships no license file."}, nil},
		{"vendored QR Code generator library 1.8.0", []string{"Origin: web/ui/src/vendor/qrcodegen.js\n", "--- qrcodegen.js ---\n/*!\n", "Copyright (c) Project Nayuki. (MIT License)\n */\n"}, []string{"var qr", "version  :"}},
		{"npm devbundled 0.5.0", []string{"License: MIT\n", "--- LICENSE ---\n", "--- dist/NOTICE ---\nDevbundled vendors Tiny"}, []string{"other/LICENSE", "Not on the way", "copyright.mjs", "copyrightIcon"}},
		{"npm vite 7.3.6", []string{"--- LICENSE.md ---\n# Vite core license", "VoidZero Inc."}, nil},
		{"vendored shadcn/ui components generated", []string{"Origin: web/ui/src/components/ui/\n", "--- LICENSE ---\n", "Copyright (c) 2023 shadcn"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.head, func(t *testing.T) {
			s := sectionText(t, body, tc.head)
			for _, c := range tc.contains {
				if !strings.Contains(s, c) {
					t.Errorf("section lacks %q:\n%s", c, s)
				}
			}
			for _, l := range tc.lacks {
				if strings.Contains(s, l) {
					t.Errorf("section carries %q:\n%s", l, s)
				}
			}
		})
	}
	for _, left := range []string{"devonly", "devopt", "local.css"} {
		if strings.Contains(string(body), left) {
			t.Errorf("the notices name %s, which the bundle does not ship", left)
		}
	}
	again, err := consoleNotices(root)
	if err != nil || string(again) != string(body) {
		t.Errorf("a second run gave different bytes (err %v)", err)
	}
}

func TestConsoleNoticesRefuses(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		remove string
		want   []string
	}{
		{"package without a license file",
			map[string]string{
				"web/ui/package-lock.json":                strings.Replace(fixtureLock, `"node_modules/alpha"`, `"node_modules/bare": {"version": "4.0.0", "license": "MIT"}, "node_modules/alpha"`, 1),
				"web/ui/node_modules/bare/index.js":       "module.exports = 1\n",
				"web/ui/node_modules/bare/license.json":   "{}\n",
				"web/ui/node_modules/bare/docs/LICENSE":   "not at the top of the folder\n",
				"web/ui/node_modules/bare/README.md":      "# bare\n",
				"web/ui/node_modules/bare/package.json":   "{}\n",
				"web/ui/node_modules/bare/licensed/x.txt": "a folder is not a file\n",
			}, "",
			[]string{"the npm package bare 4.0.0 ships no license file", "exception table"}},
		{"lock file version 2",
			map[string]string{"web/ui/package-lock.json": strings.Replace(fixtureLock, `"lockfileVersion": 3`, `"lockfileVersion": 2`, 1)}, "",
			[]string{"lockfileVersion 2", "version 3 only"}},
		{"package not installed",
			map[string]string{"web/ui/package-lock.json": strings.Replace(fixtureLock, `"node_modules/alpha"`, `"node_modules/ghost": {"version": "5.0.0"}, "node_modules/alpha"`, 1)}, "",
			[]string{"the npm package ghost 5.0.0 is not installed", "run make ui"}},
		{"CSS import the lock does not have",
			map[string]string{"web/ui/src/extra.css": "@import \"@fonts/inter/400.css\";\n"}, "",
			[]string{"web/ui/src/extra.css imports the package @fonts/inter", "does not have"}},
		{"shadcn/ui license missing", nil, shadcnLicense,
			[]string{"web/ui/src/components/ui/LICENSE", "shadcn/ui"}},
		{"bundle list missing", nil, "web/ui/node_modules/.straza/bundle-sw.json",
			[]string{"the list of bundled modules web/ui/node_modules/.straza/bundle-sw.json is missing", "run make ui"}},
		{"bundled module of unknown origin",
			map[string]string{"web/ui/node_modules/.straza/bundle-pages.json": `["src/main.tsx", "../outside/helper.js"]`}, "",
			[]string{`lists the module "../outside/helper.js"`, "neither the console's own source"}},
		{"bundled package the lock does not have",
			map[string]string{"web/ui/node_modules/.straza/bundle-pages.json": `["node_modules/ghost/index.js"]`}, "",
			[]string{"carries code from node_modules/ghost, which web/ui/package-lock.json does not have"}},
		{"vendored file without a notice",
			map[string]string{
				"web/ui/src/vendor/tinylib.js":                  "/*! tinylib v2.0.0 | Copyright (c) Tiny Vendor Corp. | BSD-3-Clause */\nexport const t = 1\n",
				"web/ui/node_modules/.straza/bundle-pages.json": `["index.html", "src/main.tsx", "src/vendor/qrcodegen.js", "src/vendor/tinylib.js", "node_modules/alpha/index.js"]`,
			}, "",
			[]string{`lists "src/vendor/tinylib.js"`, "no vendored component names", "vendoredComponents in tools/notices/npm.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t)
			writeFiles(t, root, tc.files)
			if tc.remove != "" {
				if err := os.Remove(filepath.Join(root, tc.remove)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := consoleNotices(root)
			if err == nil {
				t.Fatal("console notices succeeded, want a refusal")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}
