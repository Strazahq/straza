package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// bundleFiles are the lists of bundled modules that the two Vite builds of
// the console write, the pages and the self-service worker, through the
// bundleModules plugin in web/ui/vite.notices.ts. npm ci empties
// node_modules, so a list is never older than the last build.
var bundleFiles = []string{
	"web/ui/node_modules/.straza/bundle-pages.json",
	"web/ui/node_modules/.straza/bundle-sw.json",
}

// viteKey is the lock key of the bundler, whose own helpers, the module
// preload helper and the CommonJS interop helper, land in the chunks.
const viteKey = "node_modules/vite"

// vendorDir is the folder of third-party files copied into the console
// source, relative to web/ui.
const vendorDir = "src/vendor/"

// bundledPackages returns the lock key of every installed package whose code
// is in the console's chunks, whatever the lock file says of it, each with
// the folders of its bundled modules relative to web/ui. The console's own
// source is src/ outside src/vendor/. A file under src/vendor/ that no
// vendored component names as its origin stops the run, and so does a module
// that is neither the console's own source, an installed package nor a
// helper of the bundler, so no code reaches the bundle without a notice.
func bundledPackages(root string, vendored []component) (map[string][]string, error) {
	named := map[string]bool{}
	for _, c := range vendored {
		named[c.origin] = true
	}
	out := map[string][]string{}
	for _, file := range bundleFiles {
		raw, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // G304: a fixed path under the repository root
		if err != nil {
			return nil, fmt.Errorf("the list of bundled modules %s is missing: run make ui, whose Vite build writes it, then run tools/notices again", file)
		}
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, fmt.Errorf("%s is not a JSON list of module ids: run make ui, which writes it again: %w", file, err)
		}
		for _, id := range ids {
			switch i := strings.LastIndex(id, modulesPrefix); {
			case i >= 0:
				rest := strings.Split(id[i+len(modulesPrefix):], "/")
				name := rest[0]
				if strings.HasPrefix(name, "@") && len(rest) > 1 {
					name += "/" + rest[1]
				}
				key := id[:i] + modulesPrefix + name
				out[key] = append(out[key], path.Dir(id))
			case strings.HasPrefix(id, "vite/") || id == "commonjsHelpers.js":
				out[viteKey] = append(out[viteKey], viteKey)
			case strings.HasPrefix(id, vendorDir) && !named[uiDir+"/"+id]:
				return nil, fmt.Errorf("%s lists %q, a file under %s/%s that no vendored component names, so the console would ship its code without its notice: add a row for it with the license header it came with to vendoredComponents in tools/notices/npm.go, then run tools/notices again", file, id, uiDir, vendorDir)
			case strings.HasPrefix(id, "src/") || (!strings.Contains(id, "/") && strings.HasSuffix(id, ".html")):
			default:
				return nil, fmt.Errorf("%s lists the module %q, which is neither the console's own source, an installed package nor a helper of the bundler: find where its code comes from and what license it carries, then teach tools/notices/bundle.go its origin", file, id)
			}
		}
	}
	return out, nil
}
