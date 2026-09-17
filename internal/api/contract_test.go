package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRoutesMatchOpenAPISpec keeps docs/api/openapi.yaml (the API contract) and
// the actual route table honest in both directions: every route must be
// documented in the spec, and every spec path+method must exist as a route.
// Static frontend serving is not a contract route (documented in the spec).
func TestRoutesMatchOpenAPISpec(t *testing.T) {
	specPath := filepath.Join("..", "..", "docs", "api", "openapi.yaml")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read contract spec: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse contract spec: %v", err)
	}

	contract := make(map[string]bool)
	for path, item := range spec.Paths {
		for method := range item {
			switch method {
			case "get", "post", "put", "patch", "delete":
				contract[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	implemented := make(map[string]bool, len(routes))
	for _, route := range routes {
		implemented[route.method+" "+route.path] = true
	}

	for _, route := range routes {
		key := route.method + " " + route.path
		if !contract[key] {
			t.Errorf("route %s is implemented but missing from docs/api/openapi.yaml", key)
		}
	}
	for key := range contract {
		if !implemented[key] {
			t.Errorf("spec documents %s but no handler registers it", key)
		}
	}
	if len(contract) != len(implemented) {
		t.Errorf("route count drift: spec=%d implemented=%d", len(contract), len(implemented))
	}
}
