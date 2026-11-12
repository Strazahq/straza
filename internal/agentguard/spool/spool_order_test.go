package spool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// acceptedCEs decodes every record srv accepted, in the order it accepted
// them.
func acceptedCEs(t *testing.T, srv *orderedServer) []lossCE {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	var out []lossCE
	for _, up := range srv.accepted {
		for _, rec := range up {
			var ce lossCE
			if err := json.Unmarshal(rec, &ce); err != nil {
				t.Fatalf("an uploaded record is not JSON: %v", err)
			}
			out = append(out, ce)
		}
	}
	return out
}

// TestSpoolDrainKeepsEventOrderAcrossFailedDrains pins the upload order of
// parked files. Each failed drain parks the live file, so a record appended
// after it lands in a later file. The server numbers records in the order it
// receives them, so the files must reach it in the order they were parked.
// With eight files a random order passes by chance once in 40,320 runs.
func TestSpoolDrainKeepsEventOrderAcrossFailedDrains(t *testing.T) {
	const batches = 8
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	srv := &orderedServer{refuse: func(int) bool { return true }}
	for i := range batches {
		if err := sp.AppendCE("straza.audit.tool", map[string]any{"batch": i}); err != nil {
			t.Fatal(err)
		}
		if _, err := sp.Drain(context.Background(), srv, "tok"); err == nil {
			t.Fatalf("drain %d against a refusing server succeeded", i)
		}
	}
	if parked, _ := filepath.Glob(filepath.Join(dir, "audit-pending-*.jsonl")); len(parked) != batches {
		t.Fatalf("parked files = %d, want one per failed drain, %d", len(parked), batches)
	}

	srv.refuse = nil
	if n, err := sp.Drain(context.Background(), srv, "tok"); err != nil || n != batches {
		t.Fatalf("healed drain = %d, %v; want %d records", n, err, batches)
	}
	var got []int
	for _, ce := range acceptedCEs(t, srv) {
		b, ok := ce.Data["batch"].(float64)
		if !ok {
			t.Fatalf("record %s carries no batch number: %v", ce.ID, ce.Data)
		}
		got = append(got, int(b))
	}
	want := []int{0, 1, 2, 3, 4, 5, 6, 7}
	if !slices.Equal(got, want) {
		t.Fatalf("server saw the batches in order %v, want %v", got, want)
	}
}

// TestSpoolDrainOrdersParkedFiles pins the order across an upgrade. A file an
// older client parked under a random UUID carries no park time in its name,
// so such files go first, oldest modification time first and the name on a
// tie. Files named with a version 7 UUID follow in name order, whatever their
// modification times say.
func TestSpoolDrainOrdersParkedFiles(t *testing.T) {
	v7a := uuid.Must(uuid.NewV7()).String()
	v7b := uuid.Must(uuid.NewV7()).String()
	const v4old, v4new = "f47ac10b-58cc-4372-a567-0e02b2c3d479", "e47ac10b-58cc-4372-a567-0e02b2c3d479"
	type parked struct {
		mid string
		age time.Duration
	}
	cases := []struct {
		name  string
		files []parked
		want  []string
	}{
		{"old-form files first by age, then version 7 files by name",
			[]parked{{v7b, 4 * time.Hour}, {v7a, 3 * time.Hour}, {v4old, 2 * time.Hour}, {v4new, time.Hour}},
			[]string{v4old, v4new, v7a, v7b}},
		{"old-form files of equal age go by name",
			[]parked{{"bbb", time.Hour}, {"aaa", time.Hour}, {v7a, 2 * time.Hour}},
			[]string{"aaa", "bbb", v7a}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now()
			for _, f := range tc.files {
				p := filepath.Join(dir, "audit-pending-"+f.mid+".jsonl")
				if err := os.WriteFile(p, []byte(`{"id":"`+f.mid+`"}`+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				mt := now.Add(-f.age)
				if err := os.Chtimes(p, mt, mt); err != nil {
					t.Fatal(err)
				}
			}
			srv := &orderedServer{}
			if _, err := NewSpool(filepath.Join(dir, "spool.jsonl")).Drain(context.Background(), srv, "tok"); err != nil {
				t.Fatalf("drain: %v", err)
			}
			var got []string
			for _, ce := range acceptedCEs(t, srv) {
				got = append(got, ce.ID)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("upload order = %v, want %v", got, tc.want)
			}
		})
	}
}
