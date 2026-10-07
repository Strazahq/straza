package console

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/server/console/distbudget"
)

// TestUIDist guards the embedded app build: index.html is present, it
// loads at least one script under the /console/ base, and nothing on the
// page points off-origin. Deployments are air-gapped and the page must be
// served entirely from the strazad binary.
func TestUIDist(t *testing.T) {
	dist := Dist()

	html, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatalf("index.html: %v", err)
	}
	page := string(html)

	scripts := regexp.MustCompile(`<script[^>]+src="([^"]+)"`).FindAllStringSubmatch(page, -1)
	if len(scripts) == 0 {
		t.Fatal("index.html loads no script: is this the compiled app build?")
	}
	for _, m := range scripts {
		src := m[1]
		if !strings.HasPrefix(src, "/console/") {
			t.Errorf("script %q is not under the /console/ base", src)
			continue
		}
		body, err := fs.ReadFile(dist, strings.TrimPrefix(src, "/console/"))
		if err != nil {
			t.Errorf("script %q is referenced but not in dist: %v", src, err)
			continue
		}
		if len(body) < 10<<10 {
			t.Errorf("script %q is %d bytes: looks like a stub, not the compiled page", src, len(body))
		}
	}

	attr := regexp.MustCompile(`(?i)(src|href)\s*=\s*"([^"]*)"`)
	for _, m := range attr.FindAllStringSubmatch(page, -1) {
		val := m[2]
		if strings.HasPrefix(val, "data:") {
			continue
		}
		if strings.Contains(val, "://") || strings.HasPrefix(val, "//") {
			t.Errorf("index.html references an external origin: %s=%q", m[1], val)
		}
	}
	if strings.Contains(page, "<script>") {
		t.Error("index.html carries an inline script: every script must be a file under dist")
	}
}

// TestUIBudget holds every entry page of the embedded build under its
// gzipped budget through the same check tools/uibuild runs, so the dist in
// the binary and the dist a box just built are measured alike.
func TestUIBudget(t *testing.T) {
	entries, err := distbudget.Check(Dist())
	if err != nil {
		t.Fatalf("ui asset contract:\n%v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entry page measured: is dist the compiled app build?")
	}
	for _, e := range entries {
		t.Logf("%s: %d B gzipped of %d B over %d assets", e.Page, e.Gzipped, e.Budget, len(e.Assets))
	}
}

// TestHandler pins the fallback contract behind /console/: an app route and
// the mount both get index.html, a file in the dist gets the file, and a
// missing path with an extension gets 404 rather than HTML.
func TestHandler(t *testing.T) {
	scripts, err := fs.Glob(Dist(), "assets/*.js")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no script under dist/assets: %v", err)
	}
	h := http.StripPrefix("/console/", Handler())
	cases := []struct {
		name     string
		path     string
		status   int
		ctype    string // Content-Type must contain this
		contains string // the body must contain this
	}{
		{"app route", "/console/servers", http.StatusOK, "text/html", "<script"},
		{"mount", "/console/", http.StatusOK, "text/html", "<script"},
		{"nested app route", "/console/servers/abc", http.StatusOK, "text/html", "<script"},
		{"dist file", "/console/" + scripts[0], http.StatusOK, "javascript", ""},
		{"stale script", "/console/assets/missing.js", http.StatusNotFound, "", ""},
	}
	self := http.StripPrefix("/self-service/", SelfServiceHandler())
	selfCases := []struct {
		name     string
		path     string
		status   int
		ctype    string
		contains string
	}{
		{"self-service mount", "/self-service/", http.StatusOK, "text/html", "self-service"},
		{"self-service tab", "/self-service/credentials", http.StatusOK, "text/html", "self-service"},
		{"self-service worker", "/self-service/sw.js", http.StatusOK, "javascript", "straza-approvals"},
	}
	for _, tc := range selfCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			self.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.status {
				t.Fatalf("%s: status %d, want %d", tc.path, rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tc.ctype) {
				t.Errorf("%s: Content-Type %q, want it to contain %q", tc.path, ct, tc.ctype)
			}
			body, _ := io.ReadAll(rec.Body)
			if !strings.Contains(string(body), tc.contains) {
				t.Errorf("%s: body lacks %q", tc.path, tc.contains)
			}
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.status {
				t.Fatalf("%s: status %d, want %d", tc.path, rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tc.ctype) {
				t.Errorf("%s: Content-Type %q, want it to contain %q", tc.path, ct, tc.ctype)
			}
			body, _ := io.ReadAll(rec.Body)
			if tc.contains != "" && !strings.Contains(string(body), tc.contains) {
				t.Errorf("%s: body lacks %q: %.120s", tc.path, tc.contains, body)
			}
			if tc.status == http.StatusNotFound && strings.Contains(string(body), "<html") {
				t.Errorf("%s: a 404 answered HTML", tc.path)
			}
		})
	}
}
