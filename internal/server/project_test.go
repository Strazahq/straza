package server

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// TestProjectIDStableAcrossBoots pins the identity rule: the id is generated
// once, persisted, and every later resolve over the same store returns it;
// identity follows the data, not the process.
func TestProjectIDStableAcrossBoots(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()

	first := app.projectID
	if !strings.HasPrefix(first, "prj_") || len(first) != len("prj_")+36 {
		t.Fatalf("project id shape = %q, want prj_<uuid>", first)
	}
	again, err := resolveProjectID(ctx, app.store)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if again != first {
		t.Errorf("resolve over the same store = %q, want the persisted %q", again, first)
	}
}

// TestProjectIDConcurrentBootstrap races N resolvers over one empty store,
// the multi-pod first-boot case. Exactly one generated id may win; every
// caller must adopt it.
func TestProjectIDConcurrentBootstrap(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	if err := app.store.Settings().Delete(ctx, projectIDSettingsKey); err != nil {
		t.Fatalf("reset settings row: %v", err)
	}

	const n = 8
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := resolveProjectID(ctx, app.store)
			if err != nil {
				t.Errorf("resolver %d: %v", i, err)
				return
			}
			ids[i] = id
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("resolver %d adopted %q, resolver 0 adopted %q: split identity", i, ids[i], ids[0])
		}
	}
}

// TestProjectNameDerivation pins the label rules: config wins (sanitized),
// unset derives the stable straza-<4hex> default from the id.
func TestProjectNameDerivation(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)

	app.cfg.Server.ProjectName = ""
	derived := app.projectName()
	if !regexp.MustCompile(`^straza-[0-9a-f]{4}$`).MatchString(derived) {
		t.Errorf("derived name = %q, want straza-<4hex>", derived)
	}
	if again := app.projectName(); again != derived {
		t.Errorf("derived name not stable: %q then %q", derived, again)
	}

	cases := []struct{ in, want string }{
		{"Acme Production", "Acme Production"},
		{"  padded  ", "padded"},
		{"ctrl\x00\x1fchars", "ctrlchars"},
		{strings.Repeat("x", 80), strings.Repeat("x", 64)},
		{"\x00\x01", derived}, // sanitizes to empty -> falls back to derived
	}
	for _, c := range cases {
		app.cfg.Server.ProjectName = c.in
		if got := app.projectName(); got != c.want {
			t.Errorf("projectName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
