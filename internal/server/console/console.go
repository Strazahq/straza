// Package console embeds the Straza console, the app built on shadcn/ui
// components. The source lives in web/ui and is compiled by Vite into dist/,
// which is checked in so building strazad needs no Node. The app is served
// same-origin at /console/ on the main listener and talks to /v1 same-origin.
package console

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist
var embedded embed.FS

// Dist returns the app's asset tree rooted at its dist directory.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// The embed directive guarantees dist exists at compile time.
		panic(err)
	}
	return sub
}

// Handler serves the console from its dist for request paths relative to
// the mount, the way http.StripPrefix hands them over. A path that names a
// file in the dist gets that file. A path whose last segment has no
// extension is an app route and gets index.html, so a reload on
// /console/servers lands in the app. A missing path with an extension
// answers 404, so a stale script name from an older page never receives
// HTML.
func Handler() http.Handler {
	return handlerFor("index.html")
}

// SelfServiceHandler serves the self-service page from the same dist: its
// own entry page for the tab routes, and the worker at sw.js in the
// directory the page is served from, which is the worker's scope.
func SelfServiceHandler() http.Handler {
	return handlerFor("self-service.html")
}

func handlerFor(page string) http.Handler {
	dist := Dist()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean folds every dot segment away and ValidPath refuses what is
		// left of one, so name can only address a file inside the embedded
		// tree; the embed FS refuses anything else on its own as well.
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && fs.ValidPath(name) {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				http.ServeFileFS(w, r, dist, name) // #nosec G703 -- cleaned and validated above, embedded tree only
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, dist, page)
	})
}
