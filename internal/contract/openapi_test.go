// Package contract guards the GUI-facing API contract. The OpenAPI document is
// the single source of truth; the kernel route table must match it exactly.
package contract

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Mrjwj34/nullkey/internal/api"
	"gopkg.in/yaml.v3"
)

type openAPISpec struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Title   string `yaml:"title"`
		Version string `yaml:"version"`
	} `yaml:"info"`
	Servers []struct {
		URL string `yaml:"url"`
	} `yaml:"servers"`
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]struct {
			Type   string `yaml:"type"`
			Scheme string `yaml:"scheme"`
		} `yaml:"securitySchemes"`
	} `yaml:"components"`
}

var httpMethods = map[string]bool{
	"get":     true,
	"post":    true,
	"put":     true,
	"patch":   true,
	"delete":  true,
	"head":    true,
	"options": true,
}

func loadSpec(t *testing.T) openAPISpec {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}
	return spec
}

func basePath(t *testing.T, spec openAPISpec) string {
	t.Helper()
	if len(spec.Servers) == 0 {
		t.Fatal("OpenAPI has no servers entry")
	}
	// Server URLs may contain variables such as http://127.0.0.1:{port}/api/v1.
	serverURL := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(spec.Servers[0].URL, "0")
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL %q: %v", spec.Servers[0].URL, err)
	}
	base := strings.TrimSuffix(u.Path, "/")
	if base == "" {
		t.Fatal("OpenAPI server URL has no base path")
	}
	return base
}

func TestRoutesMatchOpenAPI(t *testing.T) {
	spec := loadSpec(t)
	if !strings.HasPrefix(spec.OpenAPI, "3.0") && !strings.HasPrefix(spec.OpenAPI, "3.1") {
		t.Fatalf("openapi version = %q, want 3.0.x or 3.1.x", spec.OpenAPI)
	}
	base := basePath(t, spec)
	if base != "/api/v1" {
		t.Fatalf("base path = %q, want /api/v1", base)
	}

	expected := make(map[string]bool)
	for path, operations := range spec.Paths {
		if !strings.HasPrefix(path, "/") {
			t.Errorf("OpenAPI path %q must start with /", path)
		}
		for method, raw := range operations {
			method = strings.ToLower(method)
			if !httpMethods[method] {
				continue
			}
			key := strings.ToUpper(method) + " " + base + path
			expected[key] = true

			op, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s: operation is not a mapping", key)
				continue
			}
			if _, ok := op["responses"]; !ok {
				t.Errorf("%s: operation has no responses", key)
			}
		}
	}
	if len(expected) == 0 {
		t.Fatal("OpenAPI contains no operations")
	}

	actual := make(map[string]bool)
	for _, route := range api.RouteSpecs() {
		key := route.Method + " " + route.Path
		if actual[key] {
			t.Errorf("duplicate kernel route: %s", key)
		}
		actual[key] = true
		if !strings.HasPrefix(route.Path, base+"/") {
			t.Errorf("kernel route %s is outside base path %s", key, base)
		}
	}

	var missing, extra []string
	for key := range expected {
		if !actual[key] {
			missing = append(missing, key)
		}
	}
	for key := range actual {
		if !expected[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("kernel is missing %d OpenAPI operations:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("kernel has %d routes not in OpenAPI:\n  %s", len(extra), strings.Join(extra, "\n  "))
	}
	if t.Failed() {
		t.Logf("OpenAPI operations: %d, kernel routes: %d", len(expected), len(actual))
	}
}

func TestOpenAPIHasBearerSecurity(t *testing.T) {
	spec := loadSpec(t)
	if spec.Info.Title == "" || spec.Info.Version == "" {
		t.Fatalf("OpenAPI info is incomplete: %+v", spec.Info)
	}
	scheme, ok := spec.Components.SecuritySchemes["bearerAuth"]
	if !ok {
		t.Fatal("OpenAPI is missing bearerAuth security scheme")
	}
	if scheme.Type != "http" || !strings.EqualFold(scheme.Scheme, "bearer") {
		t.Fatalf("bearerAuth = %+v, want type=http scheme=bearer", scheme)
	}
}

func TestAllRoutesAreImplemented(t *testing.T) {
	var pending []string
	for _, route := range api.RouteSpecs() {
		if !route.Implemented {
			pending = append(pending, route.Method+" "+route.Path)
		}
	}
	sort.Strings(pending)
	if len(pending) > 0 {
		t.Fatalf("routes still marked unimplemented (%d):\n  %s", len(pending), strings.Join(pending, "\n  "))
	}
	for _, key := range []string{"GET /api/v1/health", "GET /api/v1/capabilities"} {
		found := false
		for _, route := range api.RouteSpecs() {
			if route.Method+" "+route.Path == key && route.Implemented {
				found = true
			}
		}
		if !found {
			t.Errorf("%s should be implemented", key)
		}
	}
}

func TestOAuthCallbackIsUnauthenticated(t *testing.T) {
	spec := loadSpec(t)
	operations, ok := spec.Paths["/oauth/callback"]
	if !ok {
		t.Fatal("OpenAPI is missing /oauth/callback")
	}
	raw, ok := operations["get"]
	if !ok {
		t.Fatal("OpenAPI is missing GET /oauth/callback")
	}
	op, ok := raw.(map[string]any)
	if !ok {
		t.Fatal("GET /oauth/callback operation is not a mapping")
	}
	security, ok := op["security"]
	if !ok {
		t.Fatal("GET /oauth/callback must override global security with security: []")
	}
	list, ok := security.([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("GET /oauth/callback security = %#v, want []", security)
	}
}

func TestOpenAPIYAMLIsParsable(t *testing.T) {
	spec := loadSpec(t)
	if len(spec.Paths) == 0 {
		t.Fatal("OpenAPI has no paths")
	}
	t.Log(fmt.Sprintf("openapi %s: %d paths, %d security schemes", spec.OpenAPI, len(spec.Paths), len(spec.Components.SecuritySchemes)))
}
