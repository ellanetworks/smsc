package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPISpec(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/openapi+yaml" {
		t.Fatalf("Content-Type = %q", ct)
	}

	var spec struct {
		OpenAPI string                            `yaml:"openapi"`
		Paths   map[string]map[string]interface{} `yaml:"paths"`
	}

	if err := yaml.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}

	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("openapi = %q", spec.OpenAPI)
	}

	routes := []string{
		"POST /api/v1/messages",
		"GET /api/v1/messages",
		"GET /api/v1/messages/{id}",
		"GET /api/v1/messages/{id}/attempts",
		"GET /api/v1/diameter",
		"GET /api/v1/operator",
		"PUT /api/v1/operator",
		"GET /api/v1/delivery",
		"PUT /api/v1/delivery",
		"GET /api/v1/status",
		"GET /api/v1/metrics",
		"GET /api/v1/openapi.yaml",
	}

	for _, route := range routes {
		method, path, _ := strings.Cut(route, " ")

		if _, ok := spec.Paths[path][strings.ToLower(method)]; !ok {
			t.Errorf("spec is missing %s", route)
		}
	}
}
