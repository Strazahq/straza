package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/manager"
)

// TestSeedPinsWhatTheMarksDeclare pins that the eval seed's manifest for
// objects-demo parses, and that its straza.objects block equals the block
// the server's marks declare, so the server page on the eval stack shows no
// drift.
func TestSeedPinsWhatTheMarksDeclare(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "compose", "eval-stack", "seed", "apps", "objects-demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := manager.Parse(raw)
	if err != nil {
		t.Fatalf("the seed manifest does not parse: %v", err)
	}
	if m.Straza.Objects == nil {
		t.Fatal("the seed manifest pins no straza.objects block")
	}
	url, _ := startDemo(t)
	res, err := connect(t, url, testApp).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	declared, ok, faults := manager.DeclaredObjects("objects-demo", res.Tools)
	if !ok || len(faults) > 0 {
		t.Fatalf("the marks declare ok=%v with faults %q", ok, faults)
	}
	if !reflect.DeepEqual(*m.Straza.Objects, declared) {
		t.Errorf("the seed pins %+v, the marks declare %+v", *m.Straza.Objects, declared)
	}
}
