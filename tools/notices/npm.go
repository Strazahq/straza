package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	consoleScope  = "the Straza console"
	uiDir         = "web/ui"
	lockFile      = "web/ui/package-lock.json"
	cssRoot       = "web/ui/src"
	qrFile        = "web/ui/src/vendor/qrcodegen.js"
	qrVersion     = "1.8.0"
	shadcnDir     = "web/ui/src/components/ui/"
	shadcnLicense = "web/ui/src/components/ui/LICENSE"
	modulesPrefix = "node_modules/"
)

// consoleNotices returns the notices file of the console bundle.
func consoleNotices(root string) ([]byte, error) {
	vendored, err := vendoredComponents(root)
	if err != nil {
		return nil, err
	}
	npm, err := npmComponents(root, vendored)
	if err != nil {
		return nil, err
	}
	return render(consoleScope, sectionsOf(append(vendored, npm...))), nil
}

// vendoredComponents returns the third-party sources copied into web/ui:
// the QR encoder with its own header, and the components generated from
// shadcn/ui with the LICENSE file beside them.
func vendoredComponents(root string) ([]component, error) {
	qr, err := os.ReadFile(filepath.Join(root, qrFile)) //nolint:gosec // G304: a fixed path under the repository root
	if err != nil {
		return nil, fmt.Errorf("cannot read the vendored QR encoder %s: run tools/notices from the repository root: %w", qrFile, err)
	}
	start, end := strings.Index(string(qr), "/*"), strings.Index(string(qr), "*/")
	if start < 0 || end < start {
		return nil, fmt.Errorf("%s has no license header comment: re-vendor the file from upstream with its header", qrFile)
	}
	if !strings.Contains(string(qr), "v"+qrVersion) {
		return nil, fmt.Errorf("%s no longer names version v%s: set qrVersion in tools/notices/npm.go to the version it was re-vendored from", qrFile, qrVersion)
	}
	shadcn, err := os.ReadFile(filepath.Join(root, shadcnLicense)) //nolint:gosec // G304: a fixed path under the repository root
	if err != nil {
		return nil, fmt.Errorf("cannot read %s, the shadcn/ui license of the generated interface components: restore the file from the shadcn/ui repository: %w", shadcnLicense, err)
	}
	return []component{
		{kind: "vendored", name: "QR Code generator library", version: qrVersion, license: "MIT", origin: qrFile,
			texts: []text{{name: filepath.Base(qrFile), body: normalize(qr[start : end+2])}}},
		{kind: "vendored", name: "shadcn/ui components", version: "generated", license: "MIT", origin: shadcnDir,
			texts: []text{{name: "LICENSE", body: normalize(shadcn)}}},
	}, nil
}

type lockEntry struct {
	Version     string          `json:"version"`
	License     json.RawMessage `json:"license"`
	Dev         bool            `json:"dev"`
	DevOptional bool            `json:"devOptional"`
}

// npmComponents returns every production package of the lock file, every
// package the console's CSS imports and every package whose code is in the
// console's chunks, each with the texts of its installed folder or its
// exception row, and the texts on the way from its bundled modules to it.
// vendored are the vendored components, whose origins are the only files
// under src/vendor/ that the bundle may carry.
func npmComponents(root string, vendored []component) ([]component, error) {
	raw, err := os.ReadFile(filepath.Join(root, lockFile)) //nolint:gosec // G304: a fixed path under the repository root
	if err != nil {
		return nil, fmt.Errorf("cannot read the console's lock file %s: run tools/notices from the repository root: %w", lockFile, err)
	}
	var lock struct {
		LockfileVersion int                  `json:"lockfileVersion"`
		Packages        map[string]lockEntry `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: install the dependencies again with make ui: %w", lockFile, err)
	}
	if lock.LockfileVersion != 3 {
		return nil, fmt.Errorf("%s has lockfileVersion %d, and tools/notices reads version 3 only: write the lock file again with npm 7 or newer", lockFile, lock.LockfileVersion)
	}
	keys := map[string]bool{}
	for key, e := range lock.Packages {
		if strings.HasPrefix(key, modulesPrefix) && !e.Dev && !e.DevOptional {
			keys[key] = true
		}
	}
	imports, err := cssImports(root)
	if err != nil {
		return nil, err
	}
	for _, imp := range imports {
		key := modulesPrefix + imp.pkg
		if _, ok := lock.Packages[key]; !ok {
			return nil, fmt.Errorf("%s imports the package %s, which %s does not have: add the package with npm install in web/ui, or remove the import", imp.file, imp.pkg, lockFile)
		}
		keys[key] = true
	}
	bundled, err := bundledPackages(root, vendored)
	if err != nil {
		return nil, err
	}
	for key := range bundled {
		if _, ok := lock.Packages[key]; !ok {
			return nil, fmt.Errorf("the console bundle carries code from %s, which %s does not have: run make ui, which installs the locked packages and builds again", key, lockFile)
		}
		keys[key] = true
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	seen := map[string]bool{}
	var comps []component
	for _, key := range sorted {
		e := lock.Packages[key]
		name := key[strings.LastIndex(key, modulesPrefix)+len(modulesPrefix):]
		if seen[name+"@"+e.Version] {
			continue
		}
		seen[name+"@"+e.Version] = true
		c, err := npmComponent(root, key, name, e, bundled[key])
		if err != nil {
			return nil, err
		}
		comps = append(comps, c)
	}
	return comps, nil
}

func npmComponent(root, key, name string, e lockEntry, moduleDirs []string) (component, error) {
	folder := uiDir + "/" + key
	dir := filepath.Join(root, filepath.FromSlash(folder))
	if _, err := os.Stat(dir); err != nil {
		return component{}, fmt.Errorf("the npm package %s %s is not installed in %s: run make ui, which installs the locked packages, then run tools/notices again", name, e.Version, folder)
	}
	texts, err := licenseTexts(dir)
	if err != nil {
		return component{}, fmt.Errorf("cannot read the license files of the npm package %s %s in %s: %w", name, e.Version, folder, err)
	}
	if len(texts) == 0 {
		ex, ok := exceptions[name+"@"+e.Version]
		if !ok {
			return component{}, fmt.Errorf("the npm package %s %s ships no license file, and tools/notices has no exception for it: read the license the package declares, then add a row for this name and version to the exception table in tools/notices/exceptions.go, or replace the package", name, e.Version)
		}
		texts = []text{{name: "exception table", body: ex}}
	}
	dirs := make([]string, 0, len(moduleDirs))
	for _, d := range moduleDirs {
		dirs = append(dirs, filepath.Join(root, uiDir, filepath.FromSlash(d)))
	}
	nested, err := nestedTexts(dir, dirs)
	if err != nil {
		return component{}, fmt.Errorf("cannot read the license files in the folders of the npm package %s %s: %w", name, e.Version, err)
	}
	texts = append(texts, nested...)
	license := seeTexts
	var declared string
	if json.Unmarshal(e.License, &declared) == nil && declared != "" {
		license = declared
	}
	return component{kind: "npm", name: name, version: e.Version, license: license,
		origin: "https://www.npmjs.com/package/" + name + "/v/" + e.Version, texts: texts}, nil
}

// cssImport is a package that a CSS file under web/ui/src imports by name.
type cssImport struct {
	file string
	pkg  string
}

var importLine = regexp.MustCompile(`^\s*@import\s+"([^"]+)"`)

// cssImports returns the packages that the console's CSS imports by bare
// name: the first path segment of the target, or the first two for a scoped
// name. A relative or absolute target is the console's own file.
func cssImports(root string) ([]cssImport, error) {
	var out []cssImport
	base := filepath.Join(root, cssRoot)
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".css") {
			return err
		}
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a CSS file of the console source
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, line := range strings.Split(string(raw), "\n") {
			m := importLine.FindStringSubmatch(line)
			if m == nil || strings.HasPrefix(m[1], ".") || strings.HasPrefix(m[1], "/") {
				continue
			}
			parts := strings.Split(m[1], "/")
			pkg := parts[0]
			if strings.HasPrefix(pkg, "@") && len(parts) > 1 {
				pkg += "/" + parts[1]
			}
			out = append(out, cssImport{file: filepath.ToSlash(rel), pkg: pkg})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cannot read the console's CSS under %s: %w", cssRoot, err)
	}
	return out, nil
}
