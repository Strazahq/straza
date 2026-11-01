package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// repo is one repository objects-demo holds. The id is a number, as
// GitHub's repository ids are, so the listing sends it as a JSON number.
type repo struct {
	Value string `json:"value"`
	ID    int64  `json:"id"`
	Area  string `json:"area,omitempty"`
}

// listItem is one repo in a repo_list page, in the shape Straza reads.
type listItem struct {
	Value      string            `json:"value"`
	ID         int64             `json:"id"`
	Title      string            `json:"title"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// item answers the repo as a listing item: the title is the name after the
// owner, and a repo with no area carries no attributes.
func (r repo) item() listItem {
	it := listItem{Value: r.Value, ID: r.ID, Title: r.Value[strings.LastIndex(r.Value, "/")+1:]}
	if r.Area != "" {
		it.Attributes = map[string]string{"area": r.Area}
	}
	return it
}

// builtinRepos is the list objects-demo serves while no state file exists:
// ten repos in the shape of the console mock, named after public or made-up repos.
var builtinRepos = []repo{
	{"Strazahq/straza", 9012331, "product"},
	{"Strazahq/straza-mobile", 8811207, "product"},
	{"Strazahq/push-service", 8811342, "product"},
	{"Strazahq/docs-site", 7730016, "site"},
	{"Strazahq/demo-agents", 8920154, "site"},
	{"Strazahq/adaptive-ssh-mcp", 8455101, "servers"},
	{"Strazahq/midpoint-mcp-server", 8455388, "servers"},
	{"Strazahq/policy-packs", 8601772, "servers"},
	{"Strazahq/connector-keycloak", 8102935, "servers"},
	{"Strazahq/prod-config", 7999420, "ops"},
}

// loadRepos answers the repos in the state file, or the built-in ten when
// the file does not exist.
func loadRepos(state string) ([]repo, error) {
	raw, err := os.ReadFile(filepath.Clean(state))
	if errors.Is(err, fs.ErrNotExist) {
		return slices.Clone(builtinRepos), nil
	}
	if err != nil {
		return nil, fmt.Errorf("objects-demo could not read its repo list %s: %w", state, err)
	}
	var repos []repo
	if err := json.Unmarshal(raw, &repos); err != nil {
		return nil, fmt.Errorf("objects-demo's repo list %s is not a JSON list of repos (%v). Delete it to start again from the ten built-in repos", state, err)
	}
	return repos, nil
}

// addRepo appends one repo to the state file and answers the new count. It
// refuses an empty name, an id that is not a positive whole number, and a
// name or id the list already holds, and then writes nothing.
func addRepo(state, value, id, area string) (int, error) {
	if value == "" || !utf8.ValidString(value) {
		return 0, errors.New("the repo name must not be empty and must be valid UTF-8, such as Strazahq/straza-desktop")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("the id must be a positive whole number, such as 9120487, because objects-demo sends ids as JSON numbers, and %q is not one", id)
	}
	repos, err := loadRepos(state)
	if err != nil {
		return 0, err
	}
	for _, r := range repos {
		if r.Value == value {
			return 0, fmt.Errorf("objects-demo already holds %s. Pick another name", value)
		}
		if r.ID == n {
			return 0, fmt.Errorf("objects-demo already holds a repo with the id %d, %s. Pick another id", n, r.Value)
		}
	}
	repos = append(repos, repo{Value: value, ID: n, Area: area})
	return len(repos), writeRepos(state, repos)
}

// writeRepos replaces the state file in one rename, so a listing that reads
// it meanwhile sees the old list or the new one, never half of either.
func writeRepos(state string, repos []repo) error {
	raw, err := json.MarshalIndent(repos, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(state), ".repos-*.json")
	if err != nil {
		return fmt.Errorf("objects-demo could not write its repo list next to %s: %w", state, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("objects-demo could not write its repo list: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("objects-demo could not write its repo list: %w", err)
	}
	return os.Rename(tmp.Name(), state)
}
