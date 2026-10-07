package store

import (
	"context"
	"testing"
)

// TestPolicyListMeta pins the metadata read: ListMeta returns the same
// rows in the same order as List, every field intact EXCEPT YAMLSource,
// which stays empty by contract (the summary cache fetches YAML only on a
// miss).
func TestPolicyListMeta(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if _, err := s.Policies().Create(ctx, PolicySet{
			Name: "meta-b", Priority: 10, YAMLSource: "b: doc", Status: "draft",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Policies().Create(ctx, PolicySet{
			Name: "meta-a", Priority: 20, YAMLSource: "a: doc", Status: "active", CompiledHash: "abc",
		}); err != nil {
			t.Fatal(err)
		}

		full, err := s.Policies().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := s.Policies().ListMeta(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(meta) != len(full) {
			t.Fatalf("ListMeta rows = %d, List rows = %d", len(meta), len(full))
		}
		for i := range meta {
			if meta[i].YAMLSource != "" {
				t.Errorf("row %s carries yaml_source; ListMeta must not", meta[i].Name)
			}
			if full[i].YAMLSource == "" {
				t.Errorf("row %s: List lost yaml_source", full[i].Name)
			}
			m, f := meta[i], full[i]
			if m.ID != f.ID || m.Name != f.Name || m.Priority != f.Priority ||
				m.CompiledHash != f.CompiledHash || m.Status != f.Status ||
				!m.CreatedAt.Equal(f.CreatedAt) || !m.UpdatedAt.Equal(f.UpdatedAt) {
				t.Errorf("row %d meta/full mismatch:\n meta %+v\n full %+v", i, m, f)
			}
		}
		if meta[0].Name != "meta-a" || meta[1].Name != "meta-b" {
			t.Errorf("order = %s, %s; want priority desc", meta[0].Name, meta[1].Name)
		}
	})
}
