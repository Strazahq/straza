package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/store"
)

// exampleData reads the data member of a spec/events example.
func exampleData(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "spec", "events", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	var ce struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &ce); err != nil {
		t.Fatal(err)
	}
	return ce.Data
}

// recordFields answers the sorted field names of data, with the names of a
// nested object as parent.child and of an array's objects as parent[].child.
func recordFields(data map[string]any, prefix string) []string {
	var out []string
	for k, v := range data {
		out = append(out, prefix+k)
		switch v := v.(type) {
		case map[string]any:
			out = append(out, recordFields(v, prefix+k+".")...)
		case []any:
			for _, e := range v {
				if m, ok := e.(map[string]any); ok {
					out = append(out, recordFields(m, prefix+k+"[].")...)
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestObjectExamplesMatchTheProducer pins the field names of the revision
// 44 examples to the records the gateway and the publish write, so an
// example cannot name a field the code does not write or miss one it does.
func TestObjectExamplesMatchTheProducer(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	ep := ghEndpoints[0]
	if _, isErr := r.call(t, ep, "pam", "pr_create", `{"repo":"Strazahq/pay-api","title":"examplea"}`, ""); isErr {
		t.Fatal("the covered call was refused")
	}
	if _, isErr := r.call(t, ep, "pam", "pr_create", `{"repo":"Strazahq/site","title":"exampled"}`, ""); !isErr {
		t.Fatal("the uncovered call ran")
	}
	for example, nonce := range map[string]string{"valid-audit-mcp-object-allow.json": "examplea", "valid-audit-mcp-object-deny.json": "exampled"} {
		recs := r.mcpRecords(t, nonce, 1)
		if len(recs) != 1 {
			t.Fatalf("%d records carry %s, want one", len(recs), nonce)
		}
		want, got := recordFields(exampleData(t, example), ""), recordFields(recs[0], "")
		if !slices.Equal(want, got) {
			t.Errorf("%s names %v\nthe gateway writes %v", example, want, got)
		}
	}

	// The binding record: the publish's own fields, the actor fields every
	// admin record carries, and covers as withCovers writes it.
	row := store.ToolBinding{Scope: &store.ObjectScope{Objects: []store.CoveredObject{{ID: "901233", Value: "Strazahq/pay-api"}}}}
	data := withCovers(map[string]any{"app": "ghapp", "role": "ghapp-write-payments", "tools": []string{"pr_create"}}, row)
	data["action"], data["draft"] = "apps.binding.create", "d-1"
	data["actor"], data["actorId"], data["actorVia"] = "dave", "u-1", "session"
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var written map[string]any
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatal(err)
	}
	want, got := recordFields(exampleData(t, "valid-audit-admin-apps-binding-create-covers.json"), ""), recordFields(written, "")
	if !slices.Equal(want, got) {
		t.Errorf("the binding example names %v\nthe publish writes %v", want, got)
	}
	if every, _ := json.Marshal(withCovers(map[string]any{}, store.ToolBinding{Scope: &store.ObjectScope{Every: true}})); !strings.Contains(string(every), `"covers":{"every":true}`) {
		t.Errorf("a binding on every object records %s, want covers as the object {\"every\":true}", every)
	}
}

// TestObjectRecord pins the record's object member: the value whole up to
// 512 bytes, cut on a character boundary past that with valueTruncated and
// the SHA-256 of the whole value, and the id only when the list held one.
func TestObjectRecord(t *testing.T) {
	long := strings.Repeat("€", 200)
	exact := strings.Repeat("a", 512)
	over := exact + "b"
	sum := func(v string) string { s := sha256.Sum256([]byte(v)); return hex.EncodeToString(s[:]) }
	cases := []struct {
		name string
		hit  objects.Hit
		want map[string]any
	}{
		{"a short value with its id", objects.Hit{Type: "repo", Reach: "argument", Value: "Strazahq/site", ID: "103"},
			map[string]any{"type": "repo", "reach": "argument", "value": "Strazahq/site", "id": "103"}},
		{"no value", objects.Hit{Type: "repo", Reach: "credential"}, map[string]any{"type": "repo", "reach": "credential"}},
		{"exactly 512 bytes", objects.Hit{Type: "repo", Reach: "argument", Value: exact},
			map[string]any{"type": "repo", "reach": "argument", "value": exact}},
		{"513 bytes", objects.Hit{Type: "repo", Reach: "argument", Value: over},
			map[string]any{"type": "repo", "reach": "argument", "value": exact, "valueTruncated": true, "valueSha256": sum(over)}},
		{"a long multibyte value", objects.Hit{Type: "repo", Reach: "argument", Value: long, ID: "7"},
			map[string]any{"type": "repo", "reach": "argument", "value": strings.Repeat("€", 170), "id": "7",
				"valueTruncated": true, "valueSha256": sum(long)}},
	}
	for _, tc := range cases {
		if got := objectRecord(tc.hit); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: objectRecord = %v, want %v", tc.name, got, tc.want)
		}
	}
}
