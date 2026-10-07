package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPIMatchesInfraRoutes is TestOpenAPIMatchesRoutes' companion for
// everything in the route table OUTSIDE /v1: the infra/discovery routes
// (/healthz, /readyz, /version, /.well-known/straza/*). They are contract, not
// build stamps: /version carries the approver pin block phones and doctor
// consume, and the .well-known documents carry the JWKS and snapshot-signing
// keys enrolling clients pin. Drift must fail the build in both directions,
// same as /v1.
//
// Known blind spot (both tests): only routeTable() is compared. Surfaces
// registered directly on the mux in routes() (/metrics, /console, /mcp, SCIM,
// the standalone OIDC issuer) are invisible here and deliberately out of
// openapi.yaml scope (SCIM's contract is spec/scim-profile; /mcp speaks MCP,
// not REST). A new REST route added via mux.Handle instead of the route table
// bypasses this tripwire, so the table stays the single home for REST routes.
func TestOpenAPIMatchesInfraRoutes(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}

	specSet := map[string]bool{}
	for path, ops := range doc.Paths {
		if strings.HasPrefix(path, "/v1/") {
			continue // the existing test's world
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
		if !strings.HasPrefix(rt.pattern, "/v1/") {
			routeSet[rt.method+" "+rt.pattern] = true
		}
	}

	for key := range routeSet {
		if !specSet[key] {
			t.Errorf("infra route %s is registered but missing from openapi.yaml", key)
		}
	}
	for key := range specSet {
		if !routeSet[key] {
			t.Errorf("openapi.yaml documents %s but no such route is registered", key)
		}
	}
}
