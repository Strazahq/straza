package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddAppendsToTheStateFile(t *testing.T) {
	state := filepath.Join(t.TempDir(), "repos.json")
	var out strings.Builder
	if err := run([]string{"add", "-state", state, "Strazahq/straza-desktop", "9120487", "product"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "objects-demo now holds 11 repos") {
		t.Errorf("add printed %q", out.String())
	}
	if err := run([]string{"add", "-state", state, "Strazahq/scratch", "9120533", ""}, &out); err != nil {
		t.Fatal(err)
	}
	repos, err := loadRepos(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 12 {
		t.Fatalf("the state file holds %d repos, want 12", len(repos))
	}
	if r := repos[10]; r.Value != "Strazahq/straza-desktop" || r.ID != 9120487 || r.Area != "product" {
		t.Errorf("repo 11 = %+v", r)
	}
	if it := repos[11].item(); it.Attributes != nil || it.Title != "scratch" {
		t.Errorf("a repo added with an empty area lists as %+v, want no attributes and the title scratch", it)
	}

	// The server reads the same file at each listing.
	url, _ := startDemoOn(t, state)
	pages := listAll(t, connect(t, url, testListing))
	last := pages[len(pages)-1]
	if len(pages) != 3 || len(last.Objects) != 4 || last.Objects[2].Value != "Strazahq/straza-desktop" {
		t.Errorf("listing after two adds: %d pages, last page %+v", len(pages), last.Objects)
	}
}

func TestAddRefusals(t *testing.T) {
	state := filepath.Join(t.TempDir(), "repos.json")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"Strazahq/straza", "1", "product"}, "objects-demo already holds Strazahq/straza"},
		{[]string{"Strazahq/new", "9012331", "product"}, "objects-demo already holds a repo with the id 9012331"},
		{[]string{"Strazahq/new", "12a", "product"}, "the id must be a positive whole number"},
		{[]string{"Strazahq/new", "0", "product"}, "the id must be a positive whole number"},
		{[]string{"", "5", "product"}, "the repo name must not be empty"},
		{[]string{"Strazahq/new", "5"}, "usage: objectsdemo add"},
	}
	for _, c := range cases {
		var out strings.Builder
		err := run(append([]string{"add", "-state", state}, c.args...), &out)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("add %q: err %v, want it to contain %q", c.args, err, c.want)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("a refused add wrote the state file: %v", err)
	}
}

func TestBrokenStateFileSaysHowToRecover(t *testing.T) {
	state := filepath.Join(t.TempDir(), "repos.json")
	if err := os.WriteFile(state, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadRepos(state)
	if err == nil || !strings.Contains(err.Error(), "Delete it to start again from the ten built-in repos") {
		t.Errorf("loadRepos on a broken file: %v", err)
	}
}
