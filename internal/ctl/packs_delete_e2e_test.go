package ctl

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCLIPackDelete drives the pack delete through the strazactl client
// against a real strazad: a pack a role is bound to is refused with the role
// and the fix, the unbound pack deletes and leaves the list, and a deleted
// pack cannot be named again.
func TestCLIPackDelete(t *testing.T) {
	base, adminPassword := bootStrazad(t)
	client := NewClient(base)
	client.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
	driveLogin(t, client, base, adminPassword)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := client.CreateRole(ctx, "finance", "money people", "", "", nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := client.CreatePack(ctx, "fin-rules", "1", "no wire transfers"); err != nil {
		t.Fatalf("CreatePack: %v", err)
	}
	if err := client.BindPack(ctx, "fin-rules", "finance"); err != nil {
		t.Fatalf("BindPack: %v", err)
	}
	for _, tc := range []struct {
		name    string
		before  func() error
		wantErr []string
		listed  bool
	}{
		{"a bound pack is refused with the role and the fix", nil,
			[]string{"still bound to the role finance", "Unbind it from the role first, then delete it"}, true},
		{"the unbound pack deletes", func() error { return client.UnbindPack(ctx, "fin-rules", "finance") }, nil, false},
		{"a deleted pack cannot be named again", nil, []string{`no pack named "fin-rules"`}, false},
	} {
		if tc.before != nil {
			if err := tc.before(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		err := client.DeletePack(ctx, "fin-rules")
		if (err == nil) != (tc.wantErr == nil) {
			t.Fatalf("%s: DeletePack = %v, want error %v", tc.name, err, tc.wantErr)
		}
		for _, want := range tc.wantErr {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: DeletePack = %v, want it to say %q", tc.name, err, want)
			}
		}
		packs, err := client.Packs(ctx)
		if err != nil {
			t.Fatalf("%s: Packs: %v", tc.name, err)
		}
		listed := false
		for _, p := range packs {
			listed = listed || p.Name == "fin-rules"
		}
		if listed != tc.listed {
			t.Errorf("%s: fin-rules listed = %v, want %v", tc.name, listed, tc.listed)
		}
	}
}
