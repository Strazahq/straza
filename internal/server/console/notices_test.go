package console

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// uiSource is the console source, relative to this package.
const uiSource = "../../../web/ui"

var (
	noticeHead = regexp.MustCompile(`(?m)^={80}\n(vendored|npm|go-stdlib|go) (.+) (\S+)\nLicense: (.*)\nOrigin: .*\n={80}\n`)
	cssImport  = regexp.MustCompile(`(?m)^\s*@import\s+"([^"]+)"`)
	textBlock  = regexp.MustCompile(`\n--- [^\n]+ ---\n\s*\S`)
)

// TestUIDistNotices proves that the bundle in the binary carries the notices
// of every package it ships: a section for each production entry of the
// lock file and each package the CSS imports, the two vendored sources, no
// section for a package the lock does not have, and a license text in each.
// A dependency change without make ui fails here.
func TestUIDistNotices(t *testing.T) {
	body, err := fs.ReadFile(Dist(), "THIRD_PARTY_NOTICES.txt")
	if err != nil {
		t.Fatalf("the embedded console carries no notices file, run make ui: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(uiSource, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages map[string]struct {
			Version     string `json:"version"`
			Dev         bool   `json:"dev"`
			DevOptional bool   `json:"devOptional"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	locked := map[string]bool{}
	want := map[string]bool{}
	for key, e := range lock.Packages {
		i := strings.LastIndex(key, "node_modules/")
		if i < 0 {
			continue
		}
		id := key[i+len("node_modules/"):] + " " + e.Version
		locked[id] = true
		if strings.HasPrefix(key, "node_modules/") && !e.Dev && !e.DevOptional {
			want[id] = true
		}
	}
	for _, pkg := range cssPackages(t) {
		e, ok := lock.Packages["node_modules/"+pkg]
		if !ok {
			t.Errorf("the CSS imports %s, which the lock file does not have", pkg)
			continue
		}
		want[pkg+" "+e.Version] = true
	}

	heads := noticeHead.FindAllSubmatchIndex(body, -1)
	have := map[string]bool{}
	for i, h := range heads {
		end := len(body)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		kind, id, license := string(body[h[2]:h[3]]), string(body[h[4]:h[5]])+" "+string(body[h[6]:h[7]]), string(body[h[8]:h[9]])
		sec := string(body[h[0]:end])
		have[kind+" "+id] = true
		if kind == "npm" && !locked[id] {
			t.Errorf("the notices name npm %s, which the lock file does not have", id)
		}
		if !textBlock.MatchString(sec) {
			t.Errorf("section %s %s carries no license text", kind, id)
		}
		phrase := map[string]string{"MIT": "Permission is hereby granted", "ISC": "Permission to use, copy, modify", "0BSD": "Permission to use, copy, modify", "Apache-2.0": "Apache License"}[license]
		if phrase != "" && !strings.Contains(sec, phrase) {
			t.Errorf("section %s %s is %s but lacks %q", kind, id, license, phrase)
		}
	}
	for id := range want {
		if !have["npm "+id] {
			t.Errorf("the notices lack npm %s: run make ui", id)
		}
	}
	for _, v := range []string{"vendored QR Code generator library 1.8.0", "vendored shadcn/ui components generated"} {
		if !have[v] {
			t.Errorf("the notices lack the section %q", v)
		}
	}
}

// cssPackages returns the packages the console's CSS imports by bare name.
func cssPackages(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(filepath.Join(uiSource, "src"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".css") {
			return err
		}
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a CSS file of the console source
		if err != nil {
			return err
		}
		for _, m := range cssImport.FindAllStringSubmatch(string(raw), -1) {
			if strings.HasPrefix(m[1], ".") || strings.HasPrefix(m[1], "/") {
				continue
			}
			parts := strings.Split(m[1], "/")
			pkg := parts[0]
			if strings.HasPrefix(pkg, "@") && len(parts) > 1 {
				pkg += "/" + parts[1]
			}
			out = append(out, pkg)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUIDistQRHeader proves that the built chunk that carries the QR encoder
// carries the encoder's license header, which the minifier would drop.
func TestUIDistQRHeader(t *testing.T) {
	chunks, err := fs.Glob(Dist(), "assets/*.js")
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, name := range chunks {
		body, err := fs.ReadFile(Dist(), name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "Version value out of range") {
			continue
		}
		found++
		if !strings.Contains(string(body), "Copyright (c) Project Nayuki. (MIT License)") {
			t.Errorf("%s carries the QR encoder without its copyright line: the qrLicenseHeader plugin in web/ui/vite.config.ts puts it there, so check the plugin and run make ui", name)
		}
	}
	if found == 0 {
		t.Fatal("no chunk carries the QR encoder: has its error text changed?")
	}
}

// TestUIDistNoticesServed pins the address that NOTICE names: the console
// serves its notices at /console/THIRD_PARTY_NOTICES.txt as plain text.
func TestUIDistNoticesServed(t *testing.T) {
	rec := httptest.NewRecorder()
	http.StripPrefix("/console/", Handler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/console/THIRD_PARTY_NOTICES.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type %q, want text/plain", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "THIRD-PARTY NOTICES FOR the Straza console\n") {
		t.Errorf("body starts %.60q, want the notices header", rec.Body.String())
	}
}
