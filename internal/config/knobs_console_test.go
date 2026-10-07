package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestConsoleConfigWordsNameRealKnobs pins the console's Configuration
// rows (web/ui/src/lib/config-words.ts) to the knob registry: every key a
// row names is a knob path, and the environment variable it names is the
// knob's own face, or empty where the knob has no face. The page tells an
// operator where to set a value, so a name that drifts from the registry
// sends them to a key that does not exist.
func TestConsoleConfigWordsNameRealKnobs(t *testing.T) {
	src, err := os.ReadFile("../../web/ui/src/lib/config-words.ts")
	if err != nil {
		t.Skipf("console words not present: %v", err)
	}
	byPath := map[string]Knob{}
	for _, k := range Knobs() {
		byPath[k.Path] = k
	}
	rows := regexp.MustCompile(`key: "([^"]+)", env: "([^"]*)"`).FindAllStringSubmatch(string(src), -1)
	if len(rows) < 10 {
		t.Fatalf("found %d rows naming a key in config-words.ts, expected the Configuration table", len(rows))
	}
	for _, m := range rows {
		keys := strings.Split(m[1], ",")
		envs := strings.Split(m[2], ",")
		if strings.Contains(m[1], " ") && len(keys) == 1 {
			continue // a prose "key", such as the capture block of a policy set
		}
		for i, key := range keys {
			key = strings.TrimSpace(key)
			k, ok := byPath[key]
			if !ok {
				t.Errorf("config-words row names key %q, which is no knob path", key)
				continue
			}
			env := ""
			if i < len(envs) {
				env = strings.TrimSpace(envs[i])
			}
			if env != k.Env {
				t.Errorf("config-words row for %s names environment %q, the knob's face is %q", key, env, k.Env)
			}
		}
	}
}
