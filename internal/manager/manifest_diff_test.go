package manager

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestChangedPaths pins the paths the apps.install record names for a
// change: objects walk key by key, an array is one leaf, the free-form
// server block is one path, a block that appears or goes is named by its
// leaves, an empty one by its own path, the result is sorted, and an
// identical manifest gives an empty list.
func TestChangedPaths(t *testing.T) {
	doc := func(t *testing.T, mutate func(m map[string]any)) string {
		t.Helper()
		m := map[string]any{
			"apiVersion": APIVersion,
			"kind":       "App",
			"metadata":   map[string]any{"name": "a"},
			"server":     map[string]any{"name": "straza.test/a", "version": "1.0.0"},
			"straza": map[string]any{
				"runtime": map[string]any{"kind": "command", "command": map[string]any{"exec": "/bin/a", "args": []any{"--one", "--two"}}},
				"limits":  map[string]any{"rps": 1, "timeoutSeconds": 30},
			},
		}
		if mutate != nil {
			mutate(m)
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	straza := func(m map[string]any) map[string]any { return m["straza"].(map[string]any) }
	runtime := func(m map[string]any) map[string]any { return straza(m)["runtime"].(map[string]any) }

	cases := []struct {
		name   string
		mutate func(m map[string]any)
		want   []string
	}{
		{name: "identical", mutate: nil, want: []string{}},
		{name: "nested change", mutate: func(m map[string]any) {
			straza(m)["limits"].(map[string]any)["rps"] = 5
		}, want: []string{"straza.limits.rps"}},
		{name: "array change is one leaf", mutate: func(m map[string]any) {
			runtime(m)["command"].(map[string]any)["args"] = []any{"--one", "--three"}
		}, want: []string{"straza.runtime.command.args"}},
		{name: "added key", mutate: func(m map[string]any) {
			m["metadata"].(map[string]any)["description"] = "a server"
		}, want: []string{"metadata.description"}},
		{name: "a removed block names its leaves", mutate: func(m map[string]any) {
			delete(straza(m), "limits")
		}, want: []string{"straza.limits.rps", "straza.limits.timeoutSeconds"}},
		{name: "a new block names its leaves", mutate: func(m map[string]any) {
			straza(m)["exposure"] = map[string]any{"tools": []any{"echo"}}
		}, want: []string{"straza.exposure.tools"}},
		{name: "a nested new block", mutate: func(m map[string]any) {
			straza(m)["credential"] = map[string]any{"kind": "static",
				"inject": map[string]any{"as": "header", "name": "Authorization", "template": "Bearer x"}}
		}, want: []string{"straza.credential.inject.as", "straza.credential.inject.name",
			"straza.credential.inject.template", "straza.credential.kind"}},
		{name: "an empty new block keeps its own path", mutate: func(m map[string]any) {
			straza(m)["exposure"] = map[string]any{}
		}, want: []string{"straza.exposure"}},
		{name: "a block on each side", mutate: func(m map[string]any) {
			delete(straza(m), "limits")
			straza(m)["exposure"] = map[string]any{"tools": []any{"echo"}}
		}, want: []string{"straza.exposure.tools", "straza.limits.rps", "straza.limits.timeoutSeconds"}},
		{name: "a removed server block is one path", mutate: func(m map[string]any) {
			delete(m, "server")
		}, want: []string{"server"}},
		{name: "server block is one path", mutate: func(m map[string]any) {
			m["server"].(map[string]any)["version"] = "2.0.0"
			m["server"].(map[string]any)["remotes"] = []any{map[string]any{"type": "streamable-http"}}
		}, want: []string{"server"}},
		{name: "a publisher key inside the server block", mutate: func(m map[string]any) {
			m["server"].(map[string]any)["_meta"] = map[string]any{"io.example/publisher": map[string]any{"token": "abc"}}
		}, want: []string{"server"}},
		{name: "runtime switch", mutate: func(m map[string]any) {
			straza(m)["runtime"] = map[string]any{"kind": "remote", "remote": map[string]any{"url": "https://mcp.example/mcp?token=abc"}}
		}, want: []string{"straza.runtime.command.args", "straza.runtime.command.exec",
			"straza.runtime.kind", "straza.runtime.remote.url"}},
		{name: "an object turned null", mutate: func(m map[string]any) {
			straza(m)["limits"] = nil
		}, want: []string{"straza.limits"}},
		{name: "several changes sort", mutate: func(m map[string]any) {
			runtime(m)["command"].(map[string]any)["exec"] = "/bin/b"
			straza(m)["limits"].(map[string]any)["rps"] = 5
			m["metadata"].(map[string]any)["description"] = "a server"
		}, want: []string{"metadata.description", "straza.limits.rps", "straza.runtime.command.exec"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChangedPaths([]byte(doc(t, nil)), []byte(doc(t, tc.mutate)))
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("ChangedPaths = %#v, want %#v", got, tc.want)
			}
		})
	}

	for _, bad := range []struct{ name, before, after string }{
		{name: "stored manifest not JSON", before: "{", after: doc(t, nil)},
		{name: "new manifest not an object", before: doc(t, nil), after: "[1]"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if got, err := ChangedPaths([]byte(bad.before), []byte(bad.after)); err == nil {
				t.Errorf("ChangedPaths = %v, want an error", got)
			}
		})
	}
}
