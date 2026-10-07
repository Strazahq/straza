package manager

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// ChangedPaths lists, sorted, the dot-joined paths at which two manifest
// JSON documents differ, for the audit record of a change to a server. It
// walks objects key by key, treats an array or any other value as one leaf,
// and names a block that appears or goes by the paths of its leaves, so a
// reader of the record sees the field that changed and not the block around
// it. An empty block has no leaf and keeps its own path. The server block is
// the verbatim registry record, whose keys are not manifest grammar, so it
// counts as the one path "server". It returns paths only, never a value,
// because an address can carry a token. Both documents must decode as JSON
// objects. Identical documents give an empty list.
func ChangedPaths(before, after []byte) ([]string, error) {
	var a, b map[string]any
	if err := json.Unmarshal(before, &a); err != nil {
		return nil, fmt.Errorf("manager: the stored manifest is not a JSON object: %w", err)
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return nil, fmt.Errorf("manager: the new manifest is not a JSON object: %w", err)
	}
	out := append([]string{}, changedPaths("", a, b)...)
	sort.Strings(out)
	return out, nil
}

// changedPaths returns the paths under prefix at which a and b differ.
func changedPaths(prefix string, a, b map[string]any) []string {
	join := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	var out []string
	for k, av := range a {
		bv, ok := b[k]
		ao, aObj := av.(map[string]any)
		bo, bObj := bv.(map[string]any)
		switch {
		case !ok:
			out = append(out, leafPaths(join(k), av)...)
		case aObj && bObj && join(k) != "server":
			out = append(out, changedPaths(join(k), ao, bo)...)
		case !reflect.DeepEqual(av, bv):
			out = append(out, join(k))
		}
	}
	for k, bv := range b {
		if _, ok := a[k]; !ok {
			out = append(out, leafPaths(join(k), bv)...)
		}
	}
	return out
}

// leafPaths names a value that only one side holds: the paths of the leaves
// inside a block, so the record says which field appeared or went, and the
// path itself for anything else, an empty block and the server block
// included.
func leafPaths(path string, v any) []string {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) == 0 || path == "server" {
		return []string{path}
	}
	var out []string
	for k, val := range obj {
		out = append(out, leafPaths(path+"."+k, val)...)
	}
	return out
}
