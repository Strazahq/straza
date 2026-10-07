package config

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// yamlLeafPaths walks Config by yaml tags and returns every knob path the
// struct actually defines. Structs recurse; maps, slices, pointers and
// scalar kinds (time.Duration included; it is an int64 kind) are leaves,
// so a collection like `sinks` is ONE row (its element shape is the
// element type's business, not the knob table's).
func yamlLeafPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Duration(0)) {
			out = append(out, yamlLeafPaths(f.Type, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// fieldByYAMLPath resolves a dot-joined yaml path to the field value inside
// cfg (structs only; table rows probed generically never cross a map).
func fieldByYAMLPath(cfg reflect.Value, path string) (reflect.Value, error) {
	v := cfg
	for _, seg := range strings.Split(path, ".") {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, fmt.Errorf("segment %q of %q is not a struct", seg, path)
		}
		found := false
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0] == seg {
				v = v.Field(i)
				found = true
				break
			}
		}
		if !found {
			return reflect.Value{}, fmt.Errorf("no field with yaml tag %q in %q", seg, path)
		}
	}
	return v, nil
}

func knobByPath(path string) (Knob, bool) {
	for _, k := range knobs {
		if k.Path == path {
			return k, true
		}
	}
	return Knob{}, false
}

// TestKnobTableCovers: every yaml leaf in Config has a knob row, and every
// knob row (bar the map pseudo-rows) names a real leaf: adding a config
// field without deciding its faces fails HERE, not later.
func TestKnobTableCovers(t *testing.T) {
	leaves := yamlLeafPaths(reflect.TypeOf(Config{}), "")
	leafSet := map[string]bool{}
	for _, p := range leaves {
		leafSet[p] = true
		k, ok := knobByPath(p)
		if !ok {
			t.Errorf("config field %q has no row in knobs (internal/config/knobs.go): decide its env face (or record why it has none) and its doc section", p)
			continue
		}
		if k.Env == "" && k.NoEnvReason == "" {
			t.Errorf("knob %q has neither an env face nor a NoEnvReason (\"no face\" is a decision, not an omission)", p)
		}
		if k.Doc == "" {
			t.Errorf("knob %q names no doc section", p)
		}
	}
	for _, k := range knobs {
		if k.EnvSpecial == "map" {
			continue // pseudo-rows for map entries the walker cannot see
		}
		if !leafSet[k.Path] {
			t.Errorf("knob row %q matches no config field: stale row (field renamed or removed?)", k.Path)
		}
	}
}

// probeValue returns an env value guaranteed to change the baseline field:
// booleans flip, durations/numbers use a sentinel no default equals, strings
// use a marker.
func probeValue(field reflect.Value) string {
	switch {
	case field.Kind() == reflect.Bool:
		if field.Bool() {
			return "false"
		}
		return "true"
	case field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.Bool:
		if !field.IsNil() && field.Elem().Bool() {
			return "false"
		}
		return "true"
	case field.Type() == reflect.TypeOf(time.Duration(0)):
		return "777h"
	case field.Kind() == reflect.Int || field.Kind() == reflect.Int64:
		return "777"
	case field.Kind() == reflect.Float64:
		return "7.5"
	case field.Kind() == reflect.Slice:
		return "probe-a,probe-b"
	default:
		return "knob-probe-value"
	}
}

// TestKnobEnvFacesWired: every env face the table claims actually reaches
// its field; a row wired to nothing is a documented lie and fails here.
func TestKnobEnvFacesWired(t *testing.T) {
	for _, k := range knobs {
		if k.Env == "" || k.EnvSpecial != "" {
			continue
		}
		base := defaults(ProfileStandalone)
		before, err := fieldByYAMLPath(reflect.ValueOf(base), k.Path)
		if err != nil {
			t.Fatalf("%s: %v", k.Path, err)
		}
		probe := probeValue(before)
		beforeCopy := reflect.ValueOf(before.Interface())

		cfg := defaults(ProfileStandalone)
		applyEnv(&cfg, func(key string) string {
			if key == k.Env {
				return probe
			}
			return ""
		})
		after, err := fieldByYAMLPath(reflect.ValueOf(cfg), k.Path)
		if err != nil {
			t.Fatalf("%s: %v", k.Path, err)
		}
		if reflect.DeepEqual(beforeCopy.Interface(), after.Interface()) {
			t.Errorf("env face %s claims to set %q but the probe %q left it unchanged (%v)", k.Env, k.Path, probe, after.Interface())
		}
	}
}

// TestKnobEnvSpecials covers the faces the generic probe cannot: the profile
// (resolved in resolveProfile, before defaults) and the github provider map.
func TestKnobEnvSpecials(t *testing.T) {
	cfg, err := Loader{Getenv: func(k string) string {
		switch k {
		case "STRAZA_PROFILE":
			return ProfileEnterprise
		case "STRAZA_STORE_DSN":
			return "postgres://probe"
		case "STRAZA_OIDC_ISSUER":
			return "https://idp.example.com"
		case "STRAZA_PUBLIC_URL":
			return "https://straza.example.com"
		}
		return ""
	}}.Load()
	if err != nil {
		t.Fatalf("enterprise env-only load: %v", err)
	}
	if cfg.Profile != ProfileEnterprise {
		t.Fatalf("STRAZA_PROFILE face not wired: got %q", cfg.Profile)
	}

	var gh Config
	applyEnv(&gh, func(k string) string {
		switch k {
		case "STRAZA_OAUTH_GITHUB_CLIENT_ID":
			return "probe-id"
		case "STRAZA_OAUTH_GITHUB_CLIENT_SECRET":
			return "probe-secret"
		case "STRAZA_OAUTH_GITHUB_CLIENT_SECRET_FILE":
			return "/probe/secret"
		}
		return ""
	})
	p := gh.OAuth.Providers["github"]
	if p.ClientID != "probe-id" || p.ClientSecret != "probe-secret" || p.ClientSecretFile != "/probe/secret" {
		t.Fatalf("github provider env faces not wired: %+v", p)
	}
}

// TestKnobTableMatchesApplyEnv greps the loader source (every non-test file
// in this package: the env faces live in the section files beside their
// knobs) for wired env variables and holds both directions: a
// variable wired in code but absent from the table, and a table face the
// source never wires.
func TestKnobTableMatchesApplyEnv(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var src []byte
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src = append(src, raw...)
		src = append(src, '\n')
	}
	wired := map[string]bool{"STRAZA_PROFILE": true} // resolveProfile's getenv
	for _, m := range regexp.MustCompile(`set\("(STRAZA_[A-Z_0-9]+)"`).FindAllStringSubmatch(string(src), -1) {
		wired[m[1]] = true
	}
	inTable := map[string]bool{}
	for _, k := range knobs {
		if k.Env != "" {
			inTable[k.Env] = true
			if !wired[k.Env] {
				t.Errorf("knob table claims env face %s (%s) but applyEnv never wires it", k.Env, k.Path)
			}
		}
	}
	for env := range wired {
		if !inTable[env] {
			t.Errorf("applyEnv wires %s but the knob table has no row claiming it; add the row (and its docs)", env)
		}
	}
}

// TestKnobDocAnchorsExist: every Doc names a section KnobSections lists, and
// every listed section holds at least one knob, so the generated
// configuration reference never has a dangling knob or an empty heading.
func TestKnobDocAnchorsExist(t *testing.T) {
	used := map[string]bool{}
	sections := map[string]bool{}
	for _, s := range KnobSections() {
		if sections[s.ID] {
			t.Errorf("section id %q is listed twice", s.ID)
		}
		sections[s.ID] = true
		if s.Title == "" {
			t.Errorf("section %q has no title", s.ID)
		}
	}
	for _, k := range knobs {
		if !sections[k.Doc] {
			t.Errorf("knob %q: Doc %q is not a KnobSections id (add the section or fix the row)", k.Path, k.Doc)
		}
		used[k.Doc] = true
	}
	for _, s := range KnobSections() {
		if !used[s.ID] {
			t.Errorf("section %q has no knob: remove it from KnobSections or point a row at it", s.ID)
		}
	}
}

// TestKnobsIsACopy: a caller that edits the returned slice must not change
// the table the loader and the generator read.
func TestKnobsIsACopy(t *testing.T) {
	got := Knobs()
	if len(got) != len(knobs) {
		t.Fatalf("Knobs returned %d rows, table has %d", len(got), len(knobs))
	}
	got[0].Path = "edited"
	if knobs[0].Path == "edited" {
		t.Fatal("Knobs returned the table itself, not a copy")
	}
}

// TestKnobNotesArePlainSentences: every knob but a removed one carries a
// Note, and every Note and NoEnvReason is plain sentences ending in a
// period, because the configuration page prints them word for word.
func TestKnobNotesArePlainSentences(t *testing.T) {
	for _, k := range knobs {
		if k.Note == "" && !strings.HasPrefix(k.NoEnvReason, "Removed.") {
			t.Errorf("knob %q has no Note: say what it does, when it is required and its default", k.Path)
		}
		for field, s := range map[string]string{"Note": k.Note, "NoEnvReason": k.NoEnvReason} {
			if s != "" && (!strings.HasSuffix(s, ".") || strings.ContainsAny(s, "\u2014\u2013|")) {
				t.Errorf("knob %q: %s must be sentences ending in a period, with no dash and no pipe: %q", k.Path, field, s)
			}
		}
	}
}
