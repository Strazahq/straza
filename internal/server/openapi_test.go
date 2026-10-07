package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPIMatchesRoutes fails when pkg/api/openapi.yaml and the registered
// /v1 routes diverge in either direction: the spec is the contract, and
// this is its lint.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc struct {
		OpenAPI string                    `yaml:"openapi"`
		Info    map[string]any            `yaml:"info"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi version = %q, want 3.1.x", doc.OpenAPI)
	}
	if doc.Info["title"] == nil || doc.Info["version"] == nil {
		t.Error("info.title and info.version are required")
	}

	specSet := map[string]bool{}
	for path, ops := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/") {
			continue // infra/discovery paths: TestOpenAPIMatchesInfraRoutes' world (0.43.0)
		}
		for method := range ops {
			switch method {
			case "get", "post", "put", "patch", "delete":
				specSet[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	routeSet := map[string]bool{}
	for _, rt := range (&App{}).routeTable() {
		if strings.HasPrefix(rt.pattern, "/v1/") {
			routeSet[rt.method+" "+rt.pattern] = true
		}
	}

	for key := range routeSet {
		if !specSet[key] {
			t.Errorf("route %s is registered but missing from openapi.yaml", key)
		}
	}
	for key := range specSet {
		if !routeSet[key] {
			t.Errorf("openapi.yaml documents %s but no such route is registered", key)
		}
	}
	fmt.Printf("openapi drift check: %d /v1 operations in sync\n", len(routeSet))
}
