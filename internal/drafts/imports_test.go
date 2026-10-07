package drafts

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// draftsForbiddenImports are the imports the drafts package must never take,
// because a check contacts nothing, starts nothing and reads no store. An
// entry that starts with a slash names a Straza package by its path under the
// module, so the rule survives a module rename, and it covers the package's
// subpackages. The standard library entries match exactly, except that
// net/http covers its subpackages.
var draftsForbiddenImports = []struct{ path, why string }{
	{"net", "a check opens no connection and makes no DNS lookup"},
	{"net/http", "a check sends no request"},
	{"os/exec", "a check starts no process"},
	{"/internal/manager", "the manager starts servers and dials remote ones"},
	{"/internal/server", "the server package serves routes and reads the store"},
	{"/internal/store", "a check reads live state only as the World value it is given"},
	{"/internal/events", "a check emits no event"},
	{"/internal/spine", "a check publishes nothing"},
	{"/internal/connect", "the connect package contacts OAuth providers"},
}

// draftsImportRefusal returns why the drafts package may not import path, or
// the empty string when it may.
func draftsImportRefusal(path string) string {
	for _, f := range draftsForbiddenImports {
		hit := path == f.path || (f.path == "net/http" && strings.HasPrefix(path, "net/http/"))
		if strings.HasPrefix(f.path, "/") {
			hit = strings.HasSuffix(path, f.path) || strings.Contains(path, f.path+"/")
		}
		if hit {
			return f.why
		}
	}
	return ""
}

// TestDraftsImportsContactNothing pins the rule that a check contacts
// nothing and reads no store: no non-test file of the drafts package imports
// a package that dials, serves, starts a process or reads the store.
func TestDraftsImportsContactNothing(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	read := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		read++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s: %v", name, imp.Path.Value, err)
			}
			if why := draftsImportRefusal(path); why != "" {
				t.Errorf("%s imports %s, which the drafts package must not import, because %s. Pass the value the check needs in as an argument instead.", name, path, why)
			}
		}
	}
	if read == 0 {
		t.Fatal("the test found no non-test Go file in the drafts package, so the import rule checked nothing")
	}
}

// TestDraftsImportRefusal is the positive control of the import rule: each
// forbidden family is caught, subpackages included, and the neighbours the
// package may use pass.
func TestDraftsImportRefusal(t *testing.T) {
	const module = "github.com/strazahq/straza"
	tests := []struct {
		path    string
		refused bool
	}{
		{"net", true},
		{"net/http", true},
		{"net/http/httputil", true},
		{"os/exec", true},
		{module + "/internal/manager", true},
		{module + "/internal/server", true},
		{module + "/internal/server/console", true},
		{module + "/internal/store", true},
		{module + "/internal/store/storetest", true},
		{module + "/internal/events", true},
		{module + "/internal/spine", true},
		{module + "/internal/connect", true},
		{"example.com/renamed/internal/store", true},
		{"net/url", false},
		{"net/netip", false},
		{"os", false},
		{"gopkg.in/yaml.v3", false},
		{module + "/internal/redact", false},
		{module + "/internal/storex", false},
		{module + "/internal/serverless", false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := draftsImportRefusal(tc.path) != ""; got != tc.refused {
				t.Errorf("draftsImportRefusal(%q) refused = %v, want %v", tc.path, got, tc.refused)
			}
		})
	}
}
